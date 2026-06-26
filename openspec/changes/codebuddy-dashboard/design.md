# Design: CodeBuddy Session 仪表盘（Go + 多服务器架构）

## 1. 系统架构

```
┌─────────────────────────────────────┐
│  开发机 A（IP 不固定）               │
│  ~/.codebuddy/sessions/*.json       │
│  ~/.codebuddy/projects/**/*.jsonl   │
│                                     │
│  ┌──────────────────────────────┐   │
│  │  dashboard-agent             │   │
│  │  - 扫描本机 CodeBuddy 状态   │   │
│  │  - 每 5 秒 POST 到中心服务   │   │
│  └──────────────┬───────────────┘   │
└─────────────────┼───────────────────┘
                  │ HTTP POST /api/report
                  │
┌─────────────────┼───────────────────┐
│  开发机 B（IP 不固定）               │
│  └── dashboard-agent ───────────────┤
└─────────────────┼───────────────────┘
                  │
                  ▼
┌─────────────────────────────────────────────────────┐
│  中心服务器（固定地址，如 NAS）                        │
│                                                       │
│  ┌──────────────────────────────────────────────┐   │
│  │  dashboard-server                             │   │
│  │                                               │   │
│  │  POST /api/report  ← 接收 Agent 上报          │   │
│  │  GET  /api/status  ← 前端轮询                 │   │
│  │  GET  /            ← 返回 index.html          │   │
│  │                                               │   │
│  │  内存状态：map[machineID]MachineStatus        │   │
│  │  掉线检测：每秒扫描，>90s 无上报 → offline   │   │
│  └──────────────────────────────────────────────┘   │
│                                                       │
└──────────────────────────┬──────────────────────────┘
                           │ 局域网 WiFi
                           │ http://<固定IP>:3000
                     ┌─────▼─────┐
                     │ 旧安卓手机 │
                     │ 浏览器     │
                     │ (每秒轮询) │
                     └───────────┘
```

## 2. 目录结构

```
codebuddy-pet/
├── cmd/
│   ├── agent/
│   │   └── main.go          # Agent 入口（运行在各开发机）
│   └── server/
│       └── main.go          # Server 入口（运行在中心服务器）
├── internal/
│   ├── collector/
│   │   ├── collector.go     # 本机 CodeBuddy 状态采集
│   │   ├── pidfile.go       # PID 文件读取
│   │   └── jsonl.go         # JSONL 文件读取 + 状态判定
│   ├── protocol/
│   │   └── types.go         # Agent/Server 共享数据结构
│   └── server/
│       ├── server.go        # HTTP 服务器
│       ├── store.go         # 内存状态存储
│       └── handler.go       # HTTP 路由处理
├── web/
│   └── index.html           # 前端单文件（embed 进 server 二进制）
├── go.mod
└── Makefile                 # 编译脚本
```

## 3. 数据结构（Go）

```go
// internal/protocol/types.go

package protocol

// SessionState 标识单个 session 的状态
type SessionState string

const (
    StateActive           SessionState = "active"            // agent 正在工作
    StateWaitingForInput  SessionState = "waiting_for_input" // 等待用户输入
    StateTerminated       SessionState = "terminated"        // 进程已退出
    StateUnknown          SessionState = "unknown"           // 无法判定
)

// SessionKind 对应 CodeBuddy 的 kind 字段
type SessionKind string

const (
    KindInteractive SessionKind = "interactive"
    KindBg          SessionKind = "bg"
    KindDaemon      SessionKind = "daemon"
)

// SessionInfo 单个 CodeBuddy session 的状态
type SessionInfo struct {
    SessionID    string       `json:"session_id"`
    PID          int          `json:"pid"`
    Kind         SessionKind  `json:"kind"`
    CWD          string       `json:"cwd"`
    StartedAt    int64        `json:"started_at"`    // Unix ms
    LastHeartbeat int64       `json:"last_heartbeat"` // Unix ms，来自 PID 文件
    State        SessionState `json:"state"`
    LastActivity int64        `json:"last_activity"` // JSONL 文件最后修改时间，Unix ms
    Version      string       `json:"version,omitempty"`
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
    LastReport   int64         `json:"last_report"`   // Unix ms，Server 收到请求时的本地时间
    Online       bool          `json:"online"`
    OfflineSince int64         `json:"offline_since"` // Unix ms，首次掉线时间（0 表示在线）
    Sessions     []SessionInfo `json:"sessions"`
}

// DashboardResponse Server 返回给前端的完整数据
type DashboardResponse struct {
    Timestamp      int64           `json:"timestamp"`       // Unix ms
    Machines       []MachineStatus `json:"machines"`
    TotalSessions  int             `json:"total_sessions"`
    ActiveCount    int             `json:"active_count"`
    WaitingCount   int             `json:"waiting_count"`
    OfflineCount   int             `json:"offline_count"`   // 掉线机器数
}
```

## 4. Agent → Server 通信协议

### 上报接口

```
POST /api/report
Content-Type: application/json
Authorization: Bearer <token>   （可选，token 为空时跳过认证）

Body: AgentReport（JSON）
```

成功响应：`200 OK`
认证失败：`401 Unauthorized`

### Agent 上报频率

- 默认每 **5 秒** 上报一次（fire-and-forget，不阻塞下一轮采集）
- 上报失败时，下次轮询直接重试，不累积退避（保持 5s 上报节奏）
- 每次上报包含本机所有 session 的完整快照（全量，非增量）

### 前端轮询接口

```
GET /api/status
Response: DashboardResponse（JSON）
```

### 健康检查

```
GET /healthz
Response: 200 OK
```

## 5. CodeBuddy 状态采集（Agent 端）

### 5.1 PID 文件读取

路径：`~/.codebuddy/sessions/<pid>.json`

```go
// internal/collector/pidfile.go

type PIDFile struct {
    PID           int    `json:"pid"`
    LastHeartbeat int64  `json:"lastHeartbeat"` // Unix ms
    SessionID     string `json:"sessionId"`
    CWD           string `json:"cwd"`
    StartedAt     int64  `json:"startedAt"`     // Unix ms
    Kind          string `json:"kind"`
    URL           string `json:"url"`
    Version       string `json:"version"`
    Hostname      string `json:"hostname"`
    UpdatedAt     int64  `json:"updatedAt"`
}
```

### 5.2 进程存活检查

```go
func isProcessAlive(pid int) bool {
    proc, err := os.FindProcess(pid)
    if err != nil {
        return false
    }
    err = proc.Signal(syscall.Signal(0))
    return err == nil
}
```

### 5.3 JSONL 文件查找

实际路径规则：`~/.codebuddy/projects/<project-name>/<session-uuid>.jsonl`

```go
func findSessionJSONL(sessionID string) (string, bool) {
    projectsDir := filepath.Join(os.Getenv("HOME"), ".codebuddy", "projects")
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
```

### 5.4 JSONL 状态判定算法

```go
// internal/collector/jsonl.go

// 噪音类型，扫描时跳过
var noiseTypes = map[string]bool{
    "file-history-snapshot": true,
    "summary":               true,
    "ai-title":              true,
    "topic":                 true,
}

// JSONLEntry 最简解析结构
type JSONLEntry struct {
    Type   string `json:"type"`
    Role   string `json:"role"`
    Status string `json:"status"`
}

func determineStateFromJSONL(path string) SessionState {
    // 1. 检查文件修改时间（5 秒内有修改 → 活跃）
    info, err := os.Stat(path)
    if err != nil {
        return StateUnknown
    }
    if time.Since(info.ModTime()) < 5*time.Second {
        return StateActive
    }

    // 2. 读取文件末尾，反向找最后一条有意义的记录
    // blockSize 设为 16KB，覆盖单行超大内容（如 Write tool 的完整文件内容）
    entry, err := readLastMeaningfulEntry(path)
    if err != nil || entry == nil {
        return StateUnknown
    }

    switch entry.Type {
    case "message":
        if entry.Role == "assistant" && entry.Status == "completed" {
            return StateWaitingForInput
        }
        return StateActive // role=user 或 status!=completed

    case "function_call", "function_call_result":
        return StateActive // 工具调用中

    case "reasoning":
        return StateActive // 推理中

    default:
        return StateUnknown
    }
}

func readLastMeaningfulEntry(path string) (*JSONLEntry, error) {
    // 16KB 窗口，覆盖单行超大内容（如 Write tool 写入完整文件的 function_call 行）
    const blockSize = 16384
    f, err := os.Open(path)
    if err != nil {
        return nil, err
    }
    defer f.Close()

    stat, _ := f.Stat()
    size := stat.Size()
    seekPos := size - blockSize
    if seekPos < 0 {
        seekPos = 0
    }
    f.Seek(seekPos, io.SeekStart)

    data, err := io.ReadAll(f)
    if err != nil {
        return nil, err
    }

    lines := bytes.Split(data, []byte("\n"))
    // 从末尾向前扫描，跳过噪音
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
            continue // 跳过噪音
        }
        return &entry, nil
    }
    return nil, nil
}
```

### 5.5 完整状态判定流程

```go
func (c *Collector) CollectSessions() []SessionInfo {
    sessionsDir := filepath.Join(os.Getenv("HOME"), ".codebuddy", "sessions")
    files, _ := filepath.Glob(filepath.Join(sessionsDir, "*.json"))

    var sessions []SessionInfo
    for _, f := range files {
        session, err := c.collectOne(f)
        if err != nil {
            continue // 单个 session 异常，跳过
        }
        sessions = append(sessions, session)
    }
    return sessions
}

func (c *Collector) collectOne(pidFilePath string) (SessionInfo, error) {
    data, err := os.ReadFile(pidFilePath)
    if err != nil {
        return SessionInfo{}, err
    }
    var pf PIDFile
    if err := json.Unmarshal(data, &pf); err != nil {
        return SessionInfo{}, err
    }

    // 判定状态
    var state SessionState
    now := time.Now().UnixMilli()

    // 1. lastHeartbeat 检查（60 秒无心跳 → 已死）
    if now-pf.LastHeartbeat > 60_000 {
        state = StateTerminated
    } else if !isProcessAlive(pf.PID) {
        state = StateTerminated
    } else {
        // 2. 查找并分析 JSONL
        jsonlPath, found := findSessionJSONL(pf.SessionID)
        if !found {
            state = StateUnknown
        } else {
            state = determineStateFromJSONL(jsonlPath)
        }
    }

    info, _ := os.Stat(pidFilePath)
    // LastActivity 取 JSONL 文件的最后修改时间，而非 PID 文件
    var lastActivity int64
    if jsonlPath, found := findSessionJSONL(pf.SessionID); found {
        if ji, err := os.Stat(jsonlPath); err == nil {
            lastActivity = ji.ModTime().UnixMilli()
        }
    }
    _ = info // PID 文件 mtime 不用于 LastActivity
    return SessionInfo{
        SessionID:     pf.SessionID,
        PID:           pf.PID,
        Kind:          SessionKind(pf.Kind),
        CWD:           pf.CWD,
        StartedAt:     pf.StartedAt,
        LastHeartbeat: pf.LastHeartbeat,
        State:         state,
        LastActivity:  lastActivity,
        Version:       pf.Version,
    }, nil
}
```

## 6. Server 端设计

### 6.1 内存状态存储

```go
// internal/server/store.go

// offlineTTL：机器离线超过此时间后从 Store 中清理
const offlineTTL = 24 * time.Hour

// offlineThreshold：超过此时间无上报视为离线
const offlineThreshold = 90 * time.Second

type Store struct {
    mu       sync.RWMutex
    machines map[string]*MachineStatus // key: machine_id（非 hostname，避免同名冲突）
}

func (s *Store) UpdateMachine(report AgentReport) {
    s.mu.Lock()
    defer s.mu.Unlock()
    // LastReport 使用 Server 收到请求的本地时间，避免客户端时钟偏移问题
    now := time.Now().UnixMilli()
    existing, ok := s.machines[report.MachineID]
    if !ok {
        existing = &MachineStatus{MachineID: report.MachineID}
        s.machines[report.MachineID] = existing
    }
    existing.Hostname = report.Hostname
    existing.LastReport = now       // Server 本地时间，非 Agent 时间
    existing.Online = true
    existing.OfflineSince = 0
    existing.Sessions = report.Sessions
}

// CheckOffline 每秒调用：标记掉线机器，清理过期条目，离线机器 session 状态覆盖为 unknown
func (s *Store) CheckOffline() {
    s.mu.Lock()
    defer s.mu.Unlock()
    now := time.Now()
    nowMs := now.UnixMilli()
    for id, m := range s.machines {
        if nowMs-m.LastReport > int64(offlineThreshold/time.Millisecond) {
            if m.Online {
                m.Online = false
                m.OfflineSince = nowMs
            }
            // 离线机器的所有 session 状态覆盖为 unknown，避免显示过期的"等待输入"
            for i := range m.Sessions {
                m.Sessions[i].State = StateUnknown
            }
            // 超过 24 小时未上报 → 从 Store 清理
            if m.OfflineSince > 0 && nowMs-m.OfflineSince > int64(offlineTTL/time.Millisecond) {
                delete(s.machines, id)
            }
        }
    }
}

func (s *Store) GetDashboard() DashboardResponse {
    s.mu.RLock()
    defer s.mu.RUnlock()
    // 汇总统计并返回（仅统计在线机器的 active/waiting session）
}
```

### 6.2 HTTP 路由

```go
// internal/server/handler.go

// POST /api/report  — Agent 上报
// GET  /api/status  — 前端轮询
// GET  /            — 返回 index.html
```

## 7. 前端 UI 设计

### 页面结构（多机器视图）

```
┌─────────────────────────────────────┐
│  CodeBuddy Dashboard                │
│  等待: 2  活跃: 4  机器: 3台        │  ← 顶部全局统计
├─────────────────────────────────────┤
│ ⚡ 等待输入 [黄色闪烁卡片]           │  ← "等待输入"置顶，跨机器汇聚
│ machine-a / /home/user/proj-x       │
│ ⚡ 等待输入 [黄色闪烁卡片]           │
│ machine-b / /home/user/proj-y       │
├─────────────────────────────────────┤
│ ▼ machine-a（在线，2个session）      │  ← 按机器折叠分组
│   🟢 活跃 /home/user/proj-z         │
├─────────────────────────────────────┤
│ ▼ machine-b（在线，1个session）      │
│   🟢 活跃 /home/user/proj-w         │
├─────────────────────────────────────┤
│ ▷ machine-c（离线 30s）             │  ← 离线机器折叠
└─────────────────────────────────────┘
```

### 排序规则
1. "等待输入"的 session 全部置顶（不区分机器）
2. 机器分组按最后活跃时间排序
3. 离线机器折叠置底

### CSS 状态样式
- 等待输入：黄色背景 + pulse 闪烁动画
- 活跃：绿色左边框
- 已终止：灰色半透明
- 机器离线：橙色边框 + "离线 Xs" 标签

## 8. 部署方案

### 编译

```bash
# 编译 Server（中心服务）
GOOS=linux GOARCH=amd64 go build -o dashboard-server ./cmd/server

# 编译 Agent（各开发机）
GOOS=linux GOARCH=amd64 go build -o dashboard-agent ./cmd/agent
```

### Server 启动（中心服务器）

```bash
./dashboard-server --port 3000 --token <your-secret>
```

### Agent 启动（各开发机）

```bash
# 基本用法
./dashboard-agent --server http://<中心服务器IP>:3000 --token <your-secret>

# 多台机器 hostname 相同时，必须用 --id 指定唯一标识
./dashboard-agent --server http://<中心服务器IP>:3000 --token <secret> --id machine-work-01
```

或通过环境变量：

```bash
DASHBOARD_SERVER=http://192.168.1.100:3000 DASHBOARD_TOKEN=secret ./dashboard-agent
```

### 手机访问

```
http://<中心服务器IP>:3000
```

### 可选：systemd 自启动（中心服务器）

```ini
[Unit]
Description=CodeBuddy Dashboard Server

[Service]
ExecStart=/usr/local/bin/dashboard-server --port 3000 --token <secret>
Restart=always

[Install]
WantedBy=multi-user.target
```

## 9. 安全说明

- 默认无认证（`--token` 为空时跳过验证），适合纯内网环境
- `/api/status` 和 Web UI 面向局域网手机开放，会暴露 session 的 CWD 路径和状态信息
- **建议在局域网内设置 shared token**，防止局域网内其他设备误访问或伪造上报
- 严格安全场景可为 Server 配置 HTTPS（自签名证书），同时解锁 Wake Lock API

## 10. Wake Lock 限制说明

`navigator.wakeLock.request('screen')` 在 HTTP 页面下，部分安卓浏览器（Chrome 85+）会拒绝调用（需要 HTTPS 才允许）。

**推荐备用方案**：
1. 手机设置 → 显示 → 屏幕超时 → 设为"永不"
2. 使用 Kiosk 类 App 保持屏幕常亮
3. 如需 Wake Lock，可为中心服务配置自签名证书 + HTTPS
