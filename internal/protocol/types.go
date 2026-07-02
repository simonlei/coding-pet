package protocol

// SessionState 标识单个 session 的状态
type SessionState string

const (
	StateActive             SessionState = "active"               // agent 正在工作
	StateWaitingForInput    SessionState = "waiting_for_input"    // 等待用户输入
	StateWaitingForApproval SessionState = "waiting_for_approval" // 等待审批
	StateTerminated         SessionState = "terminated"           // 进程已退出
	StateUnknown            SessionState = "unknown"              // 无法判定
)

// SessionKind 对应 CodeBuddy 的 kind 字段
type SessionKind string

const (
	KindInteractive SessionKind = "interactive"
	KindBg          SessionKind = "bg"
	KindDaemon      SessionKind = "daemon"
)

// SessionTool 标识 session 来源工具（CodeBuddy / WorkBuddy / Claude Code）
type SessionTool string

const (
	ToolCodeBuddy    SessionTool = "codebuddy"     // CodeBuddy CLI（agent home ~/.codebuddy）
	ToolWorkBuddy    SessionTool = "workbuddy"     // WorkBuddy IDE（通过 Hook 上报）
	ToolClaudeCode   SessionTool = "claude_code"   // Claude Code CLI
	ToolCodeBuddyIDE SessionTool = "codebuddy_ide" // CodeBuddy / WorkBuddy IDE 通过 Hook 上报（无 CLI PID 文件）
)

// SessionInfo 单个 CodeBuddy session 的状态
type SessionInfo struct {
	SessionID     string       `json:"session_id"`
	PID           int          `json:"pid"`
	Kind          SessionKind  `json:"kind"`
	Tool          SessionTool  `json:"tool"` // 来源工具：codebuddy / claude_code
	CWD           string       `json:"cwd"`
	StartedAt     int64        `json:"started_at"`     // Unix ms
	LastHeartbeat int64        `json:"last_heartbeat"` // Unix ms，来自 PID 文件
	State         SessionState `json:"state"`
	LastActivity  int64        `json:"last_activity"` // JSONL 文件最后修改时间，Unix ms
	Version       string       `json:"version,omitempty"`
}

// AgentReport Agent 向 Server 推送的上报数据
type AgentReport struct {
	MachineID string        `json:"machine_id"` // 唯一标识，默认 hostname，可通过 --id 覆盖
	Hostname  string        `json:"hostname"`   // 显示用
	ReportAt  int64         `json:"report_at"`  // Unix ms（Agent 本地时间，仅参考）
	Sessions  []SessionInfo `json:"sessions"`
	AgentVer  string        `json:"agent_ver,omitempty"`
}

// MachineStatus Server 中维护的单台机器状态
type MachineStatus struct {
	MachineID    string        `json:"machine_id"`
	Hostname     string        `json:"hostname"`
	LastReport   int64         `json:"last_report"` // Unix ms，Server 收到请求时的本地时间
	Online       bool          `json:"online"`
	OfflineSince int64         `json:"offline_since"` // Unix ms，首次掉线时间（0 表示在线）
	Sessions     []SessionInfo `json:"sessions"`
}

// DashboardResponse Server 返回给前端的完整数据
type DashboardResponse struct {
	Timestamp     int64           `json:"timestamp"` // Unix ms
	Machines      []MachineStatus `json:"machines"`
	TotalSessions int             `json:"total_sessions"`
	ActiveCount   int             `json:"active_count"`
	WaitingCount  int             `json:"waiting_count"`
	ApprovalCount int             `json:"approval_count"` // 仅 waiting_for_approval 的 session 数
	OfflineCount  int             `json:"offline_count"`  // 掉线机器数
}
