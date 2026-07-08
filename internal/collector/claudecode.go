// Package collector 中 claudecode.go 负责采集本机所有 Claude Code session 状态。
//
// Claude Code 的磁盘布局与 CodeBuddy 类似但字段不同：
//   - pid 文件位于 $CLAUDE_CONFIG_DIR/sessions/*.json（未设置时回退扫描
//     ~/.claude-internal、~/.claude、~/.tclaude 三个目录，按 sessionId 去重）
//   - 没有 lastHeartbeat 字段，updatedAt/statusUpdatedAt 是事件驱动的（仅状态变化时写入），
//     并非周期性心跳，因此不能用时间戳新鲜度判活；存活判定完全依赖进程是否存在
//   - 状态来自 status 字段（busy/shell/idle/waiting），waiting 时附带 waitingFor 说明原因
package collector

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

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

// claudeConfigDirs 解析 Claude Code 配置目录列表：
// 优先 $CLAUDE_CONFIG_DIR（单目录）；未设置时回退扫描 ~/.claude-internal、~/.claude、~/.tclaude
// 三个目录，任一目录下有运行中的 session 都会被采集。
func claudeConfigDirs() []string {
	if dir := os.Getenv("CLAUDE_CONFIG_DIR"); dir != "" {
		return []string{dir}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []string{
		filepath.Join(home, ".claude-internal"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".tclaude"),
	}
}

// readClaudeCodePIDFilesFrom 扫描给定配置目录列表下的 <dir>/sessions/*.json，
// 合并结果并按 sessionId 去重（同一 session 在多个目录出现时只保留首个）；
// 单文件解析失败时跳过。
func readClaudeCodePIDFilesFrom(configDirs []string) []claudePIDFile {
	var result []claudePIDFile
	seen := make(map[string]bool)
	for _, configDir := range configDirs {
		if configDir == "" {
			continue
		}
		pattern := filepath.Join(configDir, "sessions", "*.json")
		files, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, f := range files {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			var pf claudePIDFile
			if err := json.Unmarshal(data, &pf); err != nil {
				continue
			}
			if pf.SessionID != "" {
				if seen[pf.SessionID] {
					continue
				}
				seen[pf.SessionID] = true
			}
			result = append(result, pf)
		}
	}
	return result
}

// readClaudeCodePIDFiles 扫描所有回退/环境指定的配置目录下的 session pid 文件
func readClaudeCodePIDFiles() []claudePIDFile {
	return readClaudeCodePIDFilesFrom(claudeConfigDirs())
}

// mapClaudeStatus 将 Claude Code 的 status + waitingFor 映射到 SessionState。
//   - busy / shell / 其它 → active
//   - idle → waiting_for_input（一轮答完等下条指令，与 CodeBuddy IDE 的 complete 语义一致；
//     前端 3 分钟后自动降级为「已完成」淡黄卡片）
//   - waiting + 权限类原因（permission prompt / sandbox request / worker request）→ waiting_for_approval
//   - waiting + 其它原因（dialog open / input needed / 未知）→ waiting_for_input
func mapClaudeStatus(status, waitingFor string) protocol.SessionState {
	switch status {
	case "idle":
		return protocol.StateWaitingForInput
	case "waiting":
		switch waitingFor {
		case "permission prompt", "sandbox request", "worker request":
			return protocol.StateWaitingForApproval
		default:
			return protocol.StateWaitingForInput
		}
	default:
		return protocol.StateActive
	}
}

// findClaudeSessionTokens 在所有 Claude Code 配置目录下查找 <dir>/projects/<*>/<sessionID>.jsonl，
// 解析当前上下文占用 token。找不到或无 usage 时返回 0（优雅降级）。
func findClaudeSessionTokens(sessionID string) int64 {
	for _, dir := range claudeConfigDirs() {
		if dir == "" {
			continue
		}
		if path, found := findSessionJSONLIn(dir, sessionID); found {
			return ContextTokensFromJSONL(path)
		}
	}
	return 0
}

// CollectClaudeCodeSessions 扫描所有 Claude Code pid 文件，返回 session 状态列表
func CollectClaudeCodeSessions() []protocol.SessionInfo {
	pidFiles := readClaudeCodePIDFiles()
	if len(pidFiles) == 0 {
		return nil
	}

	var sessions []protocol.SessionInfo
	for _, pf := range pidFiles {
		// 新鲜度信号：优先 statusUpdatedAt，回退 updatedAt（仅用于展示"最后活跃"，不用于判活）
		lastActivity := pf.StatusUpdatedAt
		if lastActivity == 0 {
			lastActivity = pf.UpdatedAt
		}

		// 存活判定：Claude Code 的时间戳是事件驱动的、非周期心跳，因此只能依据进程是否存在。
		// 进程存活时按 status 判定具体状态，否则视为已终止。
		var state protocol.SessionState
		if !isProcessAlive(pf.PID) {
			state = protocol.StateTerminated
		} else {
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
			ContextTokens: findClaudeSessionTokens(pf.SessionID),
		})
	}
	return sessions
}
