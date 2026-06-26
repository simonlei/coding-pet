// Package collector 中 claudecode.go 负责采集本机所有 Claude Code session 状态。
//
// Claude Code 的磁盘布局与 CodeBuddy 类似但字段不同：
//   - pid 文件位于 $CLAUDE_CONFIG_DIR/sessions/*.json（未设置时回退 ~/.tclaude/sessions）
//   - 没有 lastHeartbeat 字段，使用 statusUpdatedAt（回退 updatedAt）作为新鲜度信号
//   - 状态来自 status 字段（busy/shell/idle/waiting），waiting 时附带 waitingFor 说明原因
package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// claudeStaleThresholdMs：statusUpdatedAt 超过此时间未更新视为已终止（与 CodeBuddy 心跳超时一致，60s）
const claudeStaleThresholdMs = 60_000

// claudePIDFile 对应 Claude Code 的 $CLAUDE_CONFIG_DIR/sessions/<pid>.json
type claudePIDFile struct {
	PID             int    `json:"pid"`
	SessionID       string `json:"sessionId"`
	CWD             string `json:"cwd"`
	StartedAt       int64  `json:"startedAt"`
	Kind            string `json:"kind"`
	Entrypoint      string `json:"entrypoint"`
	Status          string `json:"status"`     // busy / shell / idle / waiting
	WaitingFor      string `json:"waitingFor"` // status=waiting 时的原因
	Version         string `json:"version"`
	UpdatedAt       int64  `json:"updatedAt"`
	StatusUpdatedAt int64  `json:"statusUpdatedAt"`
}

// claudeConfigDir 解析 Claude Code 配置目录：
// 优先 $CLAUDE_CONFIG_DIR，未设置时回退 ~/.tclaude
func claudeConfigDir() string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return dir
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".tclaude")
}

// readClaudeCodePIDFiles 扫描 <configDir>/sessions/*.json，单文件解析失败时跳过
func readClaudeCodePIDFiles() []claudePIDFile {
	configDir := claudeConfigDir()
	if configDir == "" {
		return nil
	}
	pattern := filepath.Join(configDir, "sessions", "*.json")
	files, err := filepath.Glob(pattern)
	if err != nil {
		return nil
	}
	var result []claudePIDFile
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var pf claudePIDFile
		if err := json.Unmarshal(data, &pf); err != nil {
			continue
		}
		result = append(result, pf)
	}
	return result
}

// mapClaudeStatus 将 Claude Code 的 status + waitingFor 映射到 SessionState。
//   - busy / shell / idle / 其它 → active（idle 表示等待下一条用户指令，并非被权限阻塞，不闪烁）
//   - waiting + 权限类原因（permission prompt / sandbox request / worker request）→ waiting_for_approval
//   - waiting + 其它原因（dialog open / input needed / 未知）→ waiting_for_input
func mapClaudeStatus(status, waitingFor string) protocol.SessionState {
	if status != "waiting" {
		return protocol.StateActive
	}
	switch waitingFor {
	case "permission prompt", "sandbox request", "worker request":
		return protocol.StateWaitingForApproval
	default:
		return protocol.StateWaitingForInput
	}
}

// CollectClaudeCodeSessions 扫描所有 Claude Code pid 文件，返回 session 状态列表
func CollectClaudeCodeSessions() []protocol.SessionInfo {
	pidFiles := readClaudeCodePIDFiles()
	if len(pidFiles) == 0 {
		return nil
	}

	now := time.Now().UnixMilli()
	var sessions []protocol.SessionInfo
	for _, pf := range pidFiles {
		// 新鲜度信号：优先 statusUpdatedAt，回退 updatedAt
		lastActivity := pf.StatusUpdatedAt
		if lastActivity == 0 {
			lastActivity = pf.UpdatedAt
		}

		var state protocol.SessionState
		switch {
		case now-lastActivity > claudeStaleThresholdMs:
			state = protocol.StateTerminated
		case !isProcessAlive(pf.PID):
			state = protocol.StateTerminated
		default:
			state = mapClaudeStatus(pf.Status, pf.WaitingFor)
		}

		sessions = append(sessions, protocol.SessionInfo{
			SessionID:     pf.SessionID,
			PID:           pf.PID,
			Kind:          protocol.SessionKind(pf.Kind),
			Tool:          protocol.ToolClaudeCode,
			CWD:           pf.CWD,
			StartedAt:     pf.StartedAt,
			LastHeartbeat: lastActivity,
			State:         state,
			LastActivity:  lastActivity,
			Version:       pf.Version,
		})
	}
	return sessions
}
