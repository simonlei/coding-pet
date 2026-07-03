// Package collector 中 ide_hook_store.go 维护由 CodeBuddy / WorkBuddy IDE Hook
// 主动上报的会话状态。
//
// 与 CLI 侧不同，IDE 会话磁盘上没有稳定的 PID 文件、也不会写 JSONL 消息流，
// 所以我们不扫磁盘，改由 IDE 通过 ~/.codebuddy/settings.json 里配置的 Hook
// 主动 POST 到本地 agent 的 HTTP 端口。本文件只负责：接收事件、维护内存 map、
// 按超时衰减、导出 SessionInfo 列表。
//
// 事件规范来自 CodeBuddy IDE 官方文档（兼容 Claude Code Hooks 规范）：
//   https://www.codebuddy.cn/docs/ide/Features/Hooks
// 官方共 7 种事件，事件名与字段均为下划线 / PascalCase 风格：
//   SessionStart / SessionEnd / PreToolUse / PostToolUse
//   UserPromptSubmit / Stop / PreCompact
// 注：本版本暂不做 waiting_for_approval 状态（IDE 侧无法直接观测审批弹窗结果），
// 只维护 active / waiting_for_input / terminated 三态，未来再补降级判定。
package collector

import (
	"strings"
	"sync"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// CodeBuddy IDE 官方 Hook 事件名（PascalCase，与官方文档一致）。
const (
	HookSessionStart     = "SessionStart"
	HookSessionEnd       = "SessionEnd"
	HookUserPromptSubmit = "UserPromptSubmit"
	HookPreToolUse       = "PreToolUse"
	HookPostToolUse      = "PostToolUse"
	HookStop             = "Stop"
	HookPreCompact       = "PreCompact"
)

// 会话新鲜度：最后一次 hook 上报超过此阈值，认定 IDE 已关闭 → 从内存清理。
const ideSessionStaleTTL = 30 * time.Minute

// IDEHookEvent 是 hook 脚本 POST 过来的载荷。
//
// 字段命名对齐官方 stdin JSON（session_id / hook_event_name 等），并额外接受几个
// agent 侧特有字段（Tool / Version）用于前端分类展示。
// 大多数字段是可选的，未识别字段会被 JSON 解码器忽略。
type IDEHookEvent struct {
	// —— 官方通用字段 ——
	HookEventName string `json:"hook_event_name"`      // 必填：7 种事件名之一
	SessionID     string `json:"session_id"`           // 必填：CodeBuddy conversationId
	CWD           string `json:"cwd,omitempty"`        // 可选：workspace 路径
	TranscriptPath string `json:"transcript_path,omitempty"`

	// —— 事件特定字段 ——
	Source           string `json:"source,omitempty"`             // SessionStart: startup
	Reason           string `json:"reason,omitempty"`             // SessionEnd: other
	ToolName         string `json:"tool_name,omitempty"`          // Pre/PostToolUse
	Prompt           string `json:"prompt,omitempty"`             // UserPromptSubmit
	StopHookActive   bool   `json:"stop_hook_active,omitempty"`   // Stop
	Trigger          string `json:"trigger,omitempty"`            // PreCompact: manual/auto

	// —— agent 侧扩展字段（非官方，脚本可自行附加） ——
	Tool      string `json:"tool,omitempty"`      // codebuddy / workbuddy，默认 codebuddy_ide
	Version   string `json:"version,omitempty"`   // IDE 版本
	Timestamp int64  `json:"timestamp,omitempty"` // Unix ms，缺省用服务端时间
}

// ideSession 内存态。
type ideSession struct {
	SessionID    string
	Tool         protocol.SessionTool
	CWD          string
	Version      string
	State        protocol.SessionState
	StartedAt    int64
	LastActivity int64
	LastEvent    string
}

// IDEHookStore 是 process 内单例，供 hook HTTP 服务器写入、Collector 读出。
type IDEHookStore struct {
	mu       sync.RWMutex
	sessions map[string]*ideSession
}

// 全局单例（agent 进程内共享）。
var globalIDEStore = NewIDEHookStore()

// GlobalIDEHookStore 返回进程内共享的 hook 存储。
func GlobalIDEHookStore() *IDEHookStore { return globalIDEStore }

// NewIDEHookStore 独立构造，主要用于测试。
func NewIDEHookStore() *IDEHookStore {
	return &IDEHookStore{sessions: make(map[string]*ideSession)}
}

// Apply 根据一次 hook 事件更新内部状态。
// 未知事件也不会返回错误——记为一次心跳、状态维持不变（未初始化则视作 active）。
func (s *IDEHookStore) Apply(ev IDEHookEvent) {
	if ev.SessionID == "" {
		return
	}
	now := ev.Timestamp
	if now == 0 {
		now = time.Now().UnixMilli()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	sess, ok := s.sessions[ev.SessionID]
	if !ok {
		sess = &ideSession{
			SessionID: ev.SessionID,
			StartedAt: now,
			State:     protocol.StateActive,
		}
		s.sessions[ev.SessionID] = sess
	}

	// 元数据：非空覆盖，空值不覆盖，避免后续心跳事件抹掉首帧信息。
	if ev.Tool != "" || sess.Tool == "" {
		sess.Tool = resolveTool(ev.Tool)
	}
	if ev.CWD != "" {
		sess.CWD = ev.CWD
	}
	if ev.Version != "" {
		sess.Version = ev.Version
	}
	sess.LastActivity = now
	sess.LastEvent = ev.HookEventName

	// 状态机
	sess.State = mapHookEventToState(ev, sess.State)

	// SessionEnd 单独处理：立刻标记 terminated 并从 map 中移除，
	// 避免下一次 Snapshot 前的 30 分钟窗口内还挂着一个僵尸会话。
	if ev.HookEventName == HookSessionEnd {
		delete(s.sessions, ev.SessionID)
	}
}

// Snapshot 返回当前内存中的所有 IDE session（副本），并按超时清理过老条目。
func (s *IDEHookStore) Snapshot() []protocol.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	nowMs := time.Now().UnixMilli()
	staleBefore := nowMs - ideSessionStaleTTL.Milliseconds()

	out := make([]protocol.SessionInfo, 0, len(s.sessions))
	for id, sess := range s.sessions {
		if sess.LastActivity < staleBefore {
			// 超过 30 分钟没有任何 hook 上报，认为 IDE 已关闭，从内存清理。
			delete(s.sessions, id)
			continue
		}
		out = append(out, protocol.SessionInfo{
			SessionID:     sess.SessionID,
			PID:           0, // IDE 侧无独立 PID
			Kind:          protocol.KindInteractive,
			Tool:          sess.Tool,
			CWD:           sess.CWD,
			StartedAt:     sess.StartedAt,
			LastHeartbeat: sess.LastActivity,
			State:         sess.State,
			LastActivity:  sess.LastActivity,
			Version:       sess.Version,
		})
	}
	return out
}

// CollectCodeBuddyIDESessions 把内存态转成 SessionInfo，供顶层 Collector 合并。
func CollectCodeBuddyIDESessions() []protocol.SessionInfo {
	return globalIDEStore.Snapshot()
}

// resolveTool 把 hook 上报的 tool 字符串归一化。默认标为 codebuddy_ide，
// 便于前端与 CLI 版 codebuddy / workbuddy 区分展示。
func resolveTool(raw string) protocol.SessionTool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "workbuddy", "workbuddy_ide":
		return protocol.ToolWorkBuddy
	case "codebuddy":
		return protocol.ToolCodeBuddy
	default:
		return protocol.ToolCodeBuddyIDE
	}
}

// mapHookEventToState 根据事件类型决定新状态；未识别事件保留旧状态。
//
// 语义（本版本暂不区分 waiting_for_approval，均归入 active）：
//   - SessionStart      → active（新会话开）
//   - UserPromptSubmit  → active（用户刚发出下一条消息）
//   - PreToolUse        → active（工具即将执行；IDE 弹窗审批 hook 侧观测不到）
//   - PostToolUse       → active（工具执行完毕、Agent 循环继续）
//   - PreCompact        → 保留旧状态（仅当心跳）
//   - Stop              → waiting_for_input（一轮结束、等用户下一条指令）
//   - SessionEnd        → terminated（会话结束）
func mapHookEventToState(ev IDEHookEvent, prev protocol.SessionState) protocol.SessionState {
	switch ev.HookEventName {
	case HookSessionStart,
		HookUserPromptSubmit,
		HookPreToolUse,
		HookPostToolUse:
		return protocol.StateActive

	case HookStop:
		return protocol.StateWaitingForInput

	case HookSessionEnd:
		return protocol.StateTerminated

	case HookPreCompact:
		if prev == "" {
			return protocol.StateActive
		}
		return prev
	}

	// 未知事件：保留旧状态，不阻塞未来协议扩展。
	if prev == "" {
		return protocol.StateActive
	}
	return prev
}
