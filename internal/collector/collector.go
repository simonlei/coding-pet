// Package collector 负责采集本机所有 CodeBuddy / WorkBuddy / Claude Code session 状态。
package collector

import (
	"os"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// Collector 负责采集本机所有 CodeBuddy session 状态
type Collector struct{}

// New 创建 Collector 实例
func New() *Collector {
	return &Collector{}
}

// CollectSessions 扫描所有 PID 文件，返回 session 状态列表
// 单个 session 异常时跳过，不返回错误
// 同时采集 CodeBuddy / WorkBuddy 与 Claude Code 各类 session
func (c *Collector) CollectSessions() []protocol.SessionInfo {
	var sessions []protocol.SessionInfo

	// 1. CodeBuddy CLI（agent home ~/.codebuddy，扫描 sessions/ PID 文件 + JSONL + 运行日志）
	//    CodeBuddy IDE / WorkBuddy IDE 的实时状态改由 Hook 上报，见下方第 3 步。
	for _, h := range buddyHomes() {
		for _, pf := range readPIDFilesFrom(h.dir) {
			info, err := c.collectOne(h, pf)
			if err != nil {
				continue
			}
			sessions = append(sessions, info)
		}
	}

	// 2. Claude Code sessions（已带 ToolClaudeCode 标记）
	sessions = append(sessions, CollectClaudeCodeSessions()...)

	// 3. CodeBuddy / WorkBuddy IDE sessions（由 hook 主动上报，从内存 store 读取）
	sessions = append(sessions, CollectCodeBuddyIDESessions()...)

	return sessions
}

// collectOne 处理单个 PID 文件，判定 session 状态。
// home 决定去哪个 agent home 目录查找该 session 的 JSONL / 运行日志，以及打什么工具标签。
func (c *Collector) collectOne(home buddyHome, pf PIDFile) (protocol.SessionInfo, error) {
	var state protocol.SessionState
	now := time.Now().UnixMilli()

	// 1. lastHeartbeat 检查：60 秒无心跳 → 已死
	if now-pf.LastHeartbeat > 60_000 {
		state = protocol.StateTerminated
	} else if !isProcessAlive(pf.PID) {
		// 2. 进程存活检查
		state = protocol.StateTerminated
	} else {
		// 3. 查找并分析 JSONL
		jsonlPath, found := findSessionJSONLIn(home.dir, pf.SessionID)
		if !found {
			state = protocol.StateUnknown
		} else {
			state = DetermineStateFromJSONL(jsonlPath)

			// 用运行日志状态机校正：JSONL 只能看到「末尾悬空 function_call」这类
			// 粗信号，日志能精确区分「等待授权」与「刚发起调用、实际在执行」。
			state = reconcileWithRunState(state, lastRunStateIn(home.dir, pf.SessionID))

			// 如果主 session 显示 active，检查 subagent 是否在等审批
			// （subagent 的 dangerouslyDisableSandbox 调用不会反映在主 JSONL 中）
			if state == protocol.StateActive {
				if checkSubagentsForApproval(jsonlPath) {
					state = protocol.StateWaitingForApproval
				}
			}
		}
	}

	// LastActivity 取 JSONL 文件的最后修改时间（不是 PID 文件的 mtime）
	var lastActivity int64
	if jsonlPath, found := findSessionJSONLIn(home.dir, pf.SessionID); found {
		if ji, err := os.Stat(jsonlPath); err == nil {
			lastActivity = ji.ModTime().UnixMilli()
		}
	}

	return protocol.SessionInfo{
		SessionID:     pf.SessionID,
		PID:           pf.PID,
		Kind:          protocol.SessionKind(pf.Kind),
		Tool:          home.tool,
		CWD:           pf.CWD,
		StartedAt:     pf.StartedAt,
		LastHeartbeat: pf.LastHeartbeat,
		State:         state,
		LastActivity:  lastActivity,
		Version:       pf.Version,
	}, nil
}
