# Coding Pet Dashboard

监控多台开发机上运行的 **CodeBuddy**（CLI / IDE）、**WorkBuddy** 与 **Claude Code** 的 session 状态，在手机浏览器上集中查看。当某个 session 等待你输入（权限审批、计划确认、提问等）时，仪表盘会高亮闪烁提醒，避免 agent 在那里干等。

## 功能

- **多工具采集**：同时识别 CodeBuddy CLI、CodeBuddy IDE、WorkBuddy 桌面版与 Claude Code 的 session，并在前端按工具分组展示。
- **状态判定**：区分「工作中 / 等待输入 / 等待审批 / 离线未知」四类状态。
- **等待提醒**：处于 `waiting_for_input` / `waiting_for_approval` 的 session 高亮闪烁，并标注所属工具。
- **无 PID 工具的自动降级**：CodeBuddy IDE 与 WorkBuddy 无法感知 IDE/应用是否已关闭，会话进入「等待输入」超过 3 分钟后撤下顶部闪烁提醒，仅在机器列表里以淡黄色卡片保留（采集端 30 分钟后不再上报）。
- **多机器汇总**：各开发机运行 Agent，上报到中心 Server；机器列表按「等待优先 > 在线优先 > MachineID」稳定排序。
- **离线检测**：90 秒无上报标记为离线，离线机器的 session 状态显示为 `unknown`（避免显示过期的「等待输入」）；离线超过 24 小时后从内存清理。

## 架构

```
┌─────────────┐   每 1s 上报      ┌──────────────┐   轮询 /api/status   ┌────────────┐
│ coding-pet- │ ──────────────▶  │ coding-pet-  │ ◀──────────────────  │  手机浏览器 │
│   agent     │  POST /api/report │   server     │   返回汇总 JSON       │  (Web UI)  │
│ (每台开发机) │                   │ (中心服务器)  │                       └────────────┘
└─────────────┘                   └──────────────┘
      │
      │ 采集本机 session（4 类来源）
      ▼
 ~/.codebuddy/                       CodeBuddy CLI：PID 文件 + JSONL + 运行日志
 <CodeBuddyExtension>/.../history/    CodeBuddy IDE：workspace 会话历史目录
 ~/.workbuddy/workbuddy.db            WorkBuddy 桌面版：SQLite sessions 表
 $CLAUDE_CONFIG_DIR/sessions/*.json   Claude Code：PID 文件（回退扫 ~/.claude-internal、~/.claude、~/.tclaude）
```

- **Agent**（`cmd/agent`）：定时采集本机各类 session，判定状态，fire-and-forget 上报到 Server。只上报活跃 session（已终止的会被过滤）。
- **Server**（`cmd/server`）：内存维护各机器状态，提供上报接口、状态查询接口和内嵌的 Web UI。无数据库，重启即清空。

### 状态判定逻辑

| 工具 | 存活判定 | 状态来源 |
|------|----------|----------|
| CodeBuddy CLI | `~/.codebuddy/sessions/<pid>.json` 60s 内有心跳 **且** 进程存在 | 解析 session JSONL 末尾记录得到粗信号，再用 `logs/` 里的运行状态机校正（精确区分「刚发起调用、实际在执行」与「等待授权」）；subagent 的 `dangerouslyDisableSandbox` 调用也会触发等待审批 |
| CodeBuddy IDE | 无 PID / 心跳，靠 `history/` 目录 `index.json` 的 mtime 新鲜度衰减 | `requests[]` 末态：`running` 且 <3min → 工作中；`running` 3~30min 或 `complete` <30min → 等待输入；≥30min → 不上报 |
| WorkBuddy 桌面版 | 无 PID / 心跳，靠 SQLite `sessions` 表的 last_activity 新鲜度衰减 | `status` 字段：`working` 且 <3min → 工作中；`working` 3~30min 或 `completed` <30min → 等待输入；≥30min → 不上报 |
| Claude Code | 仅依据进程是否存在（时间戳是事件驱动的，非周期心跳，不能用于判活） | `status` 字段：`waiting` + 权限类 `waitingFor`（permission prompt / sandbox request / worker request）→ 等待审批；其余 `waiting` → 等待输入；`busy`/`shell`/`idle` 等 → 工作中 |

## 依赖

- Go 1.25+
- [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite)：纯 Go（无 cgo）SQLite 驱动，用于只读 WorkBuddy 库，便于 Agent 跨平台交叉编译。`go build` 会自动拉取，无需系统依赖。

## 编译

```bash
make build
```

编译产物：

- `coding-pet-agent`：运行在各开发机
- `coding-pet-server`：运行在中心服务器

也可单独编译：`make build-agent` / `make build-server`，清理用 `make clean`。

## 部署

### 1. 启动中心服务（Server）

```bash
./coding-pet-server --port 3000 --token <your-secret>
```

### 2. 各开发机启动 Agent

```bash
# 基本用法
./coding-pet-agent --server http://<中心服务器IP>:3000 --token <your-secret>

# 多台机器 hostname 相同时，必须用 --id 指定唯一标识
./coding-pet-agent --server http://<中心服务器IP>:3000 --token <your-secret> --id dev-machine-01
```

或通过环境变量：

```bash
export DASHBOARD_SERVER=http://192.168.1.100:3000
export DASHBOARD_TOKEN=mysecret
export DASHBOARD_ID=my-machine
./coding-pet-agent
```

> 各工具的 session 目录均自动探测，无需额外配置：
> - CodeBuddy CLI：`~/.codebuddy`
> - CodeBuddy IDE：扩展数据目录下的 `history/`（Windows：`%LOCALAPPDATA%\CodeBuddyExtension`；macOS：`~/Library/Application Support/CodeBuddyExtension`）
> - WorkBuddy：`~/.workbuddy/workbuddy.db`
> - Claude Code：`$CLAUDE_CONFIG_DIR/sessions`（未设置时回退扫描 `~/.claude-internal`、`~/.claude`、`~/.tclaude`）

### 便捷脚本

`restart.sh` 会重新编译并在后台重启 Agent + Server（默认 server 端口 3000、agent 上报到 `127.0.0.1:3000`），旧进程先杀后启：

```bash
./restart.sh          # 编译并重启 agent + server
./restart.sh server   # 只重启 server
./restart.sh agent    # 只重启 agent
```

运行参数可通过环境变量覆盖（`SERVER_PORT` / `SERVER_TOKEN` / `AGENT_SERVER` / `AGENT_TOKEN` / `AGENT_ID` / `AGENT_INTERVAL` 等），日志写入 `logs/`。

### 3. 手机访问仪表盘

在手机浏览器打开：

```
http://<中心服务器IP>:3000
```

## 选项说明

### coding-pet-server

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--port` | 3000 | 监听端口 |
| `--token` | 空（无认证） | Agent 上报认证 token，为空时跳过认证 |

### coding-pet-agent

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--server` | 必填 | Server 地址（或 `DASHBOARD_SERVER`） |
| `--token` | 空 | 认证 token（或 `DASHBOARD_TOKEN`） |
| `--id` | hostname | 唯一机器 ID（或 `DASHBOARD_ID`） |
| `--hostname` | os.Hostname() | 显示名 |
| `--interval` | 1s | 上报间隔 |

## API

| 方法 | 路径 | 认证 | 说明 |
|------|------|------|------|
| POST | `/api/report` | 需要 token | Agent 上报 `AgentReport` JSON |
| GET | `/api/status` | 无 | 前端轮询，返回 `DashboardResponse` 汇总 |
| GET | `/healthz` | 无 | 健康检查，返回 `OK` |
| GET | `/` | 无 | 内嵌 Web UI |

## 项目结构

```
cmd/
  agent/        Agent 入口（采集 + 上报）
  server/       Server 入口（接收上报 + Web UI）
internal/
  collector/    本机 session 采集
                collector.go        汇总四类采集器
                buddyhome.go        CodeBuddy CLI agent home 定义
                pidfile.go          PID 文件扫描（pidfile_unix.go / pidfile_windows.go 平台特定进程存活检查）
                jsonl.go            CodeBuddy JSONL 末尾记录解析
                logstate.go         CodeBuddy 运行日志状态机（校正等待审批）
                codebuddy_ide.go    CodeBuddy IDE：扫 history 目录
                workbuddy_db.go     WorkBuddy 桌面版：只读 SQLite sessions 表
                claudecode.go       Claude Code：PID 文件 + status 字段
  protocol/     共享数据结构与状态/工具枚举
  server/       内存 Store、HTTP Handler、内嵌 index.html
```

## 常见问题

### 手机屏幕自动熄屏

`Wake Lock API` 在 HTTP 页面下部分浏览器不支持（需要 HTTPS）。
推荐备用方案：手机设置 → 显示 → 屏幕超时 → 设为「永不」。

### 多台机器 hostname 相同

使用 `--id` 参数为每台机器指定唯一标识，否则同名机器的数据会互相覆盖。

### 掉线检测原理

Agent 每秒上报一次，Server 90 秒未收到上报时标记为离线。
离线机器的 session 状态显示为 `unknown`，避免显示过期的「等待输入」；离线超过 24 小时后自动清理。

### CodeBuddy IDE / WorkBuddy 的会话为什么会「消失」

这两类工具在磁盘上没有 PID、没有心跳，Agent 无法感知 IDE 或应用是否已关闭，只能靠会话文件的写入时间做新鲜度衰减：进入「等待输入」超过 3 分钟撤下顶部闪烁提醒，超过 30 分钟后采集端直接不再上报（视为已关闭）。
