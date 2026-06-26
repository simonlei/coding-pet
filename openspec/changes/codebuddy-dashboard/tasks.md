# Tasks: CodeBuddy Session 仪表盘

## 任务概览

| # | 任务 | 标签 | 级别 | 依赖 |
|---|------|------|------|------|
| 1 | Go 项目初始化 | `config_edit` | lite | — |
| 2 | 共享数据结构 (protocol/types.go) | `copy_pattern` | lite | 1 |
| 3 | PID 文件读取 (collector/pidfile.go) | `api_endpoint` | standard | 2 |
| 4 | JSONL 读取与状态判定 (collector/jsonl.go) | `api_endpoint` | senior | 2 |
| 5 | 状态采集器主逻辑 (collector/collector.go) | `api_endpoint` | standard | 3,4 |
| 6 | Agent 主程序 (cmd/agent/main.go) | `api_endpoint` | standard | 2,5 |
| 7 | Server 内存存储 (server/store.go) | `api_endpoint` | standard | 2 |
| 8 | Server HTTP 路由 (server/handler.go) | `api_endpoint` | standard | 2,7 |
| 9 | Server 主程序 (cmd/server/main.go) | `api_endpoint` | standard | 8 |
| 10 | 前端 UI (web/index.html) | `ui_component` | standard | — |
| 11 | 前端 embed 进 Server 二进制 | `config_edit` | lite | 9,10 |
| 12 | 可选 token 认证中间件 | `api_endpoint` | standard | 8 |
| 13 | 集成测试与端到端验证 | `test_only` | standard | 6,9,10 |
| 14 | Makefile + README 部署文档 | `docs_only` | lite | 13 |

---

## 任务详情

### Task 1: Go 项目初始化

**文件**：
- `go.mod`（module 名：`github.com/simonlei/codebuddy-dashboard`）
- 目录结构：`cmd/agent/`、`cmd/server/`、`internal/collector/`、`internal/protocol/`、`internal/server/`、`web/`

**验收**：`go mod tidy` 无报错，目录结构存在

---

### Task 2: 共享数据结构

**文件**：`internal/protocol/types.go`

**内容**：
- `SessionState` string 常量：`active`、`waiting_for_input`、`terminated`、`unknown`
- `SessionKind` string 常量：`interactive`、`bg`、`daemon`
- `SessionInfo` struct（见 design.md §3）
- `AgentReport` struct
- `MachineStatus` struct
- `DashboardResponse` struct

**验收**：`go build ./internal/protocol/` 通过

---

### Task 3: PID 文件读取

**文件**：`internal/collector/pidfile.go`

**内容**：
- `PIDFile` struct，JSON tag 对应 CodeBuddy 实际字段名（`sessionId`、`startedAt`、`lastHeartbeat` 等驼峰命名）
- `ReadPIDFiles() ([]PIDFile, error)`：扫描 `~/.codebuddy/sessions/*.json`，解析为 PIDFile 列表，单文件解析失败跳过
- `isProcessAlive(pid int) bool`：`syscall.Signal(0)` 检测进程存活

**验收**：能正确读取真实 PID 文件（`/data/home/simonlei/.codebuddy/sessions/269107.json`），进程存活检测准确

---

### Task 4: JSONL 读取与状态判定

**文件**：`internal/collector/jsonl.go`

**内容**：
- `noiseTypes`：跳过的类型集合（`file-history-snapshot`、`summary`、`ai-title`、`topic`）
- `JSONLEntry` struct：仅解析 `type`、`role`、`status` 三个字段
- `findSessionJSONL(sessionID string) (string, bool)`：
  - 枚举 `~/.codebuddy/projects/<name>/<sessionID>.jsonl`
  - 直接在各 project 目录下找 `<uuid>.jsonl`，**不进 sessions 子目录**
- `readLastMeaningfulEntry(path string) (*JSONLEntry, error)`：
  - **seek 到末尾前 16KB**（覆盖 Write tool 等单行超大内容，4KB 不够）
  - 从末尾向前扫描，跳过空行和噪音类型
  - 返回第一条有意义的记录
- `DetermineStateFromJSONL(path string) SessionState`：
  - JSONL 5 秒内有修改 → `active`
  - 最后有意义记录 = `message`+`role=assistant`+`status=completed` → `waiting_for_input`
  - 最后有意义记录 = `function_call`/`function_call_result`/`reasoning` → `active`
  - 最后有意义记录 = `message`+`role=user` → `active`
  - 其他/解析失败 → `unknown`

**验收**：
- 能找到 `~/.codebuddy/projects/data-home-simonlei-codebuddy-pet/<uuid>.jsonl`
- 对不同末尾记录类型能正确判定状态
- >1MB 大文件读取 < 10ms；16KB 内无完整行时返回 `unknown` 不崩溃

---

### Task 6: Agent 主程序

**文件**：`cmd/agent/main.go`

**内容**：
- flag 解析：`--server`、`--token`（可空）、`--interval`（默认 5s）、`--hostname`（显示名，默认 `os.Hostname()`）、**`--id`（唯一机器 ID，默认与 hostname 相同，多机同名时必须指定）**
- 定时采集 + fire-and-forget 上报：每次采集后 `go func() { POST /api/report }()`，不阻塞下一轮采集
- 上报失败只记录日志，下次 5s 后自动重试（不累积退避，保持上报节奏）
- 支持环境变量：`DASHBOARD_SERVER`、`DASHBOARD_TOKEN`、`DASHBOARD_ID`

**验收**：启动后能成功向 Server 上报；Server 不可达时不崩溃，5s 后自动重试

---

### Task 7: Server 内存存储

**文件**：`internal/server/store.go`

**内容**：
- `Store` struct：`sync.RWMutex` + `map[string]*MachineStatus`
- `UpdateMachine(report AgentReport)`：更新机器状态
- `CheckOffline(threshold time.Duration)`：超时无上报 → `online=false`
- `GetDashboard() DashboardResponse`：汇总统计（总 session 数、活跃数、等待数、离线机器数）、按机器分组，等待输入的 session 置顶

**验收**：并发读写不 panic；以 machine_id 为 key（非 hostname）；超过 90s 无上报的机器标记为 offline，session 状态覆盖为 unknown；超过 24h 的离线条目从 Store 清理

---

### Task 8: Server HTTP 路由

**文件**：`internal/server/handler.go`

**内容**：
- `POST /api/report`：解析 AgentReport JSON → 调用 `store.UpdateMachine()`
- `GET /api/status`：调用 `store.GetDashboard()` → 返回 JSON
- `GET /healthz`：返回 `200 OK`
- `GET /`：返回 embed 的 index.html

**验收**：`curl http://localhost:3000/api/status` 返回正确 JSON；无 Agent 上报时返回空机器列表不报错

---

### Task 9: Server 主程序

**文件**：`cmd/server/main.go`

**内容**：
- flag 解析：`--port`（默认 3000）、`--token`（可空）、`--offline-threshold`（默认 15s）
- 初始化 Store、注册路由、启动 HTTP Server（绑定 `0.0.0.0`）
- 后台 goroutine 每秒调用 `store.CheckOffline()`
- 优雅关闭（SIGTERM/SIGINT）

**验收**：`./dashboard-server --port 3000` 正常启动；Ctrl+C 优雅退出

---

### Task 10: 前端 UI

**文件**：`web/index.html`（单文件，无外部依赖）

**内容**：
- HTML 结构：全局统计栏（等待数/活跃数/机器数）+ 等待输入置顶区 + 机器分组列表
- CSS：
  - viewport meta tag 适配移动端，flexbox 单列布局
  - 等待输入卡片：黄色背景 + `@keyframes pulse` 闪烁
  - 活跃卡片：绿色左边框
  - 离线机器：橙色边框 + 半透明
- JS：
  - `setInterval(fetchStatus, 1000)` 每秒轮询 `/api/status`
  - 等待输入 session 跨机器置顶
  - 机器分组可折叠（点击 hostname 展开/收起）
  - 时间格式化（"运行 X 分钟"）
  - fetch 失败时保留上次数据，不清空界面

**验收**：手机浏览器正常显示；等待输入高亮闪烁；多机器分组正确；机器离线有标识

---

### Task 11: 前端 embed 进 Server 二进制

**文件**：`internal/server/embed.go`（或在 `handler.go` 中）

**内容**：
```go
//go:embed ../../web/index.html
var indexHTML string
```

在 `GET /` 路由中返回 `indexHTML`

**验收**：编译后单二进制可直接访问 Web UI，不需要额外的 `web/` 目录

---

### Task 12: 可选 token 认证中间件

**文件**：`internal/server/auth.go`

**内容**：
- `AuthMiddleware(token string) func(http.Handler) http.Handler`
- token 为空时直接放行
- token 非空时校验 `Authorization: Bearer <token>`，不匹配返回 401
- 仅对 `POST /api/report` 强制认证（状态查询和 Web UI 不需要认证）

**验收**：token 不匹配时 Agent 上报返回 401；token 为空时任意请求均通过

---

### Task 13: 集成测试与端到端验证

**内容**：
- 在本机启动 `dashboard-server`
- 启动 `dashboard-agent --server http://localhost:3000`（本机 Agent）
- 验证：
  - `/api/status` 能返回本机 CodeBuddy session 状态
  - 模拟 JSONL 末尾记录为 `message+assistant+completed` → 状态为 `waiting_for_input`
  - Agent 停止上报 15s 后，机器标记为 offline
- 手机通过局域网 IP 访问前端，验证显示正常

**验收**：所有状态判定正确；前端每秒刷新；手机可访问

---

### Task 14: Makefile + README

**文件**：`Makefile`、`README.md`

**内容**：
- `Makefile`：`make build`（编译两个二进制）、`make build-agent`、`make build-server`、`make clean`
- `README.md`：
  - 功能说明
  - 依赖（Go 1.21+）
  - 编译步骤
  - Server 启动方法
  - Agent 部署方法（含 scp 示例）
  - 手机访问方法
  - token 认证配置
  - 常见问题（Wake Lock 不可用、IP 变化、掉线检测）

**验收**：`make build` 成功；README 步骤可独立执行
