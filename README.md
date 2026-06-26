# Coding Pet Dashboard

监控多台开发机上运行的 **CodeBuddy** 与 **Claude Code** CLI session 状态，在手机浏览器上集中查看。当某个 session 等待你输入（权限审批、计划确认、提问等）时，仪表盘会高亮闪烁提醒，避免 agent 在那里干等。

## 功能

- **多工具采集**：同时识别 CodeBuddy 与 Claude Code 的 session，并在前端按工具分组展示。
- **状态判定**：区分「工作中 / 等待输入 / 等待审批 / 离线未知」四类状态。
- **等待提醒**：处于 `waiting_for_input` / `waiting_for_approval` 的 session 高亮闪烁，并标注所属工具。
- **多机器汇总**：各开发机运行 Agent，上报到中心 Server；机器列表按「等待优先 > 在线优先 > MachineID」稳定排序。
- **离线检测**：90 秒无上报标记为离线，离线机器的 session 状态显示为 `unknown`（避免显示过期的「等待输入」）；离线超过 24 小时后从内存清理。

## 架构

```
┌─────────────┐   每 5s 上报      ┌──────────────┐   轮询 /api/status   ┌────────────┐
│ coding-pet- │ ──────────────▶  │ coding-pet-  │ ◀──────────────────  │  手机浏览器 │
│   agent     │  POST /api/report │   server     │   返回汇总 JSON       │  (Web UI)  │
│ (每台开发机) │                   │ (中心服务器)  │                       └────────────┘
└─────────────┘                   └──────────────┘
      │
      │ 采集本机 session
      ▼
 ~/.codebuddy/sessions/*.json        (CodeBuddy PID 文件 + JSONL 日志)
 $CLAUDE_CONFIG_DIR/sessions/*.json  (Claude Code PID 文件，回退 ~/.tclaude)
```

- **Agent**（`cmd/agent`）：定时扫描本机 session 文件，判定状态，fire-and-forget 上报到 Server。只上报活跃 session（已终止的会被过滤）。
- **Server**（`cmd/server`）：内存维护各机器状态，提供上报接口、状态查询接口和内嵌的 Web UI。无数据库，重启即清空。

### 状态判定逻辑

| 工具 | 存活判定 | 状态来源 |
|------|----------|----------|
| CodeBuddy | 60s 内有心跳 **且** 进程存在 | 解析 session JSONL 末尾记录：`AskUserQuestion`/`ExitPlanMode` → 等待输入；`dangerouslyDisableSandbox` 工具调用（含 subagent）→ 等待审批 |
| Claude Code | 仅依据进程是否存在（时间戳是事件驱动的，非周期心跳，不能用于判活） | `status` 字段：`waiting` + 权限类 `waitingFor`（permission prompt / sandbox request / worker request）→ 等待审批；其余 `waiting` → 等待输入；`idle` 等其它 → 工作中 |

## 依赖

- Go 1.21+

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

> Claude Code session 自动从 `$CLAUDE_CONFIG_DIR/sessions`（未设置时回退 `~/.tclaude/sessions`）采集，CodeBuddy session 从 `~/.codebuddy/sessions` 采集，均无需额外配置。

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
| `--interval` | 5s | 上报间隔 |

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
  collector/    本机 session 采集：PID 文件扫描、JSONL 解析、状态判定
                （pidfile_unix.go / pidfile_windows.go 平台特定的进程存活检查）
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

Agent 每 5 秒上报一次，Server 90 秒未收到上报时标记为离线。
离线机器的 session 状态显示为 `unknown`，避免显示过期的「等待输入」；离线超过 24 小时后自动清理。
