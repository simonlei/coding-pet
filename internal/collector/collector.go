// Package collector 负责采集本机所有 CodeBuddy session 状态。
package collector

import (
	"os"
	"time"

	"github.com/simonlei/codebuddy-dashboard/internal/protocol"
)

// Collector 负责采集本机所有 CodeBuddy session 状态
type Collector struct{}

// New 创建 Collector 实例
func New() *Collector {
	return &Collector{}
}

// CollectSessions 扫描所有 PID 文件，返回 session 状态列表
// 单个 session 异常时跳过，不返回错误
func (c *Collector) CollectSessions() []protocol.SessionInfo {
	pidFiles, err := ReadPIDFiles()
	if err != nil {
		return nil
	}

	var sessions []protocol.SessionInfo
	for _, pf := range pidFiles {
		info, err := c.collectOne(pf)
		if err != nil {
			continue
		}
		sessions = append(sessions, info)
	}
	return sessions
}

// collectOne 处理单个 PID 文件，判定 session 状态
func (c *Collector) collectOne(pf PIDFile) (protocol.SessionInfo, error) {
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
		jsonlPath, found := findSessionJSONL(pf.SessionID)
		if !found {
			state = protocol.StateUnknown
		} else {
			state = DetermineStateFromJSONL(jsonlPath)

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
	if jsonlPath, found := findSessionJSONL(pf.SessionID); found {
		if ji, err := os.Stat(jsonlPath); err == nil {
			lastActivity = ji.ModTime().UnixMilli()
		}
	}

	return protocol.SessionInfo{
		SessionID:     pf.SessionID,
		PID:           pf.PID,
		Kind:          protocol.SessionKind(pf.Kind),
		CWD:           pf.CWD,
		StartedAt:     pf.StartedAt,
		LastHeartbeat: pf.LastHeartbeat,
		State:         state,
		LastActivity:  lastActivity,
		Version:       pf.Version,
	}, nil
}
