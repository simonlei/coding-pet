package collector

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/simonlei/codebuddy-dashboard/internal/protocol"
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
	projectsDir := filepath.Join(homeDir, ".codebuddy", "projects")
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

// isWaitingFunctionCall 判断 function_call 是否是需要用户介入的等待状态：
// - AskUserQuestion：弹出选项让用户选择
// - dangerouslyDisableSandbox: true 的工具调用：等待用户授权
func isWaitingFunctionCall(entry *JSONLEntry) bool {
	if entry.Name == "AskUserQuestion" {
		return true
	}
	// 检查 arguments 中是否包含 dangerouslyDisableSandbox: true
	if entry.Arguments != "" && strings.Contains(entry.Arguments, `"dangerouslyDisableSandbox":true`) {
		return true
	}
	return false
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
		if isWaitingFunctionCall(entry) {
			// 等待用户选择选项或授权
			return protocol.StateWaitingForInput
		}
		return protocol.StateActive // 普通工具调用中

	case "function_call_result":
		return protocol.StateActive // 工具结果已返回，继续执行中

	case "reasoning":
		return protocol.StateActive // 推理中

	default:
		return protocol.StateUnknown
	}
}
