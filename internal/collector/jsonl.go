package collector

import (
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

// checkSubagentsForApproval 扫描 session 的 subagent JSONL，
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
