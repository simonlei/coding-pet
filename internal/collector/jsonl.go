package collector

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// noiseTypes 扫描时跳过的噪音类型
var noiseTypes = map[string]bool{
	"file-history-snapshot": true,
	"summary":               true,
	"ai-title":              true,
	"topic":                 true,
}

// JSONLEntry 最简解析结构
type JSONLEntry struct {
	Type      string `json:"type"`
	Role      string `json:"role"`
	Status    string `json:"status"`
	Name      string `json:"name"`
	Arguments string `json:"arguments"` // function_call 的参数，JSON 字符串
}

// usageEntry 用于解析 message.usage（Claude Code 与 CodeBuddy CLI 共用此结构）。
// Claude Code 带 cache_read/cache_creation 字段；CodeBuddy CLI 无这些字段（为 0），
// 其 input_tokens 本身已含 cached，公式天然统一。
type usageEntry struct {
	Message struct {
		Usage *struct {
			InputTokens         int64 `json:"input_tokens"`
			CacheReadTokens     int64 `json:"cache_read_input_tokens"`
			CacheCreationTokens int64 `json:"cache_creation_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// contextTokens 计算当前上下文占用：input + cache_read + cache_creation。
func (u usageEntry) contextTokens() int64 {
	us := u.Message.Usage
	if us == nil {
		return 0
	}
	return us.InputTokens + us.CacheReadTokens + us.CacheCreationTokens
}

// findSessionJSONL 在 ~/.codebuddy/projects/<name>/<sessionID>.jsonl 查找
// 注意：直接在 project 目录下找，没有 sessions 子目录
func findSessionJSONL(sessionID string) (string, bool) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	return findSessionJSONLIn(filepath.Join(homeDir, ".codebuddy"), sessionID)
}

// findSessionJSONLIn 在指定 agent home 的 projects/<name>/<sessionID>.jsonl 查找。
// 注意：直接在 project 目录下找，没有 sessions 子目录。
func findSessionJSONLIn(baseDir, sessionID string) (string, bool) {
	projectsDir := filepath.Join(baseDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return "", false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		candidate := filepath.Join(projectsDir, entry.Name(), sessionID+".jsonl")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, true
		}
	}
	return "", false
}

// readLastMeaningfulEntry 从文件末尾读取 16KB，反向扫描找最后一条非噪音记录
func readLastMeaningfulEntry(path string) (*JSONLEntry, error) {
	const blockSize = 16384 // 16KB，覆盖 Write tool 等单行超大内容
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	size := stat.Size()
	seekPos := size - blockSize
	if seekPos < 0 {
		seekPos = 0
	}
	if _, err := f.Seek(seekPos, io.SeekStart); err != nil {
		return nil, err
	}

	data, err := io.ReadAll(f)
	if err != nil {
		return nil, err
	}

	lines := bytes.Split(data, []byte("\n"))
	// 从末尾向前扫描
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var entry JSONLEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if noiseTypes[entry.Type] {
			continue
		}
		return &entry, nil
	}
	return nil, nil
}

// hasDangerouslyDisableSandbox 解析 arguments 中的 dangerouslyDisableSandbox 字段
func hasDangerouslyDisableSandbox(args string) bool {
	if args == "" {
		return false
	}
	var parsed struct {
		DangerouslyDisableSandbox bool `json:"dangerouslyDisableSandbox"`
	}
	if err := json.Unmarshal([]byte(args), &parsed); err != nil {
		return false
	}
	return parsed.DangerouslyDisableSandbox
}

// isWaitingForInput 判断 function_call 是否需要用户提供信息或审批计划
func isWaitingForInput(entry *JSONLEntry) bool {
	return entry.Name == "AskUserQuestion" || entry.Name == "ExitPlanMode"
}

// isWaitingForApproval 判断 function_call 是否需要用户审批工具调用权限
func isWaitingForApproval(entry *JSONLEntry) bool {
	return hasDangerouslyDisableSandbox(entry.Arguments)
}

// DetermineStateFromJSONL 根据 JSONL 文件内容判定 session 状态
func DetermineStateFromJSONL(path string) protocol.SessionState {
	// 1. 检查文件修改时间（5 秒内有修改 → 活跃）
	info, err := os.Stat(path)
	if err != nil {
		return protocol.StateUnknown
	}
	if time.Since(info.ModTime()) < 5*time.Second {
		return protocol.StateActive
	}

	// 2. 读取末尾，反向扫描最后一条有意义的记录
	entry, err := readLastMeaningfulEntry(path)
	if err != nil || entry == nil {
		return protocol.StateUnknown
	}

	switch entry.Type {
	case "message":
		if entry.Role == "assistant" && entry.Status == "completed" {
			// 工作完成，不需要提醒
			return protocol.StateActive
		}
		return protocol.StateActive // role=user 或 status!=completed

	case "function_call":
		if isWaitingForInput(entry) {
			return protocol.StateWaitingForInput
		}
		// 末尾是悬空的 function_call（后面没有 function_call_result），
		// 说明该工具调用尚未执行——绝大多数情况是在等待用户授权（含
		// dangerouslyDisableSandbox 沙箱降级、以及 WebFetch/Bash 等普通工具
		// 的权限弹窗）。日志状态机层（collectOne 中的 lastRunState）会对
		// 「刚发起调用、实际在执行中」的瞬态误判做纠正。
		return protocol.StateWaitingForApproval

	case "function_call_result":
		return protocol.StateActive // 工具结果已返回，继续执行中

	case "reasoning":
		return protocol.StateActive // 推理中

	default:
		return protocol.StateUnknown
	}
}

// ContextTokensFromJSONL 从 JSONL 文件读取末尾一段，反向扫描找最后一条含
// message.usage 的记录，返回当前上下文占用 token 数。
// 只读末尾 64KB（覆盖单条含完整 usage 的 assistant 记录），成本恒定、不全扫文件。
// 文件不存在、无 usage 或解析失败均返回 0（优雅降级，前端不展示）。
func ContextTokensFromJSONL(path string) int64 {
	const blockSize = 65536 // 64KB，单条 assistant 记录可能较大
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	stat, err := f.Stat()
	if err != nil {
		return 0
	}
	seekPos := stat.Size() - blockSize
	if seekPos < 0 {
		seekPos = 0
	}
	if _, err := f.Seek(seekPos, io.SeekStart); err != nil {
		return 0
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return 0
	}

	lines := bytes.Split(data, []byte("\n"))
	// 从末尾向前扫描，返回第一条能解析出 usage 的记录
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 {
			continue
		}
		var entry usageEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if tokens := entry.contextTokens(); tokens > 0 {
			return tokens
		}
	}
	return 0
}

// HasAssistantMessage 扫描 JSONL 判断是否已有 assistant 消息。
// tclaude/Claude Code 的 JSONL 中，assistant 回复以顶层 "type":"assistant" 出现；
// 新启动的 session 只有 mode / permission-mode / file-history-snapshot 等元数据行，
// 用于区分"新启动的 idle"（还没对话）和"一轮答完的 idle"（用户至少收到过一次回复）。
func HasAssistantMessage(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	// JSONL 每行一条记录，扫描全文找 "type":"assistant"。
	// 对新启动的 session，文件通常只有几行，成本可忽略；
	// 对已跑一段时间的 session，命中即返回，也不会读完全文。
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 8*1024*1024) // 单条最大 8MB
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var entry struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(line, &entry); err != nil {
			continue
		}
		if entry.Type == "assistant" {
			return true
		}
	}
	return false
}

// 检查是否有 subagent 正在等待审批。
// 路径推导：主 JSONL 路径去掉 .jsonl 后缀 + /subagents
// 注：不做时间窗口过滤，因为用户可能 5 分钟后才回来审批，过滤会导致审批提示消失
func checkSubagentsForApproval(jsonlPath string) bool {
	sessionDir := strings.TrimSuffix(jsonlPath, ".jsonl")
	subagentsDir := filepath.Join(sessionDir, "subagents")

	entries, err := os.ReadDir(subagentsDir)
	if err != nil {
		return false
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		subagentPath := filepath.Join(subagentsDir, entry.Name())

		subEntry, err := readLastMeaningfulEntry(subagentPath)
		if err != nil || subEntry == nil {
			continue
		}

		if subEntry.Type == "function_call" && isWaitingForApproval(subEntry) {
			return true
		}
	}
	return false
}
