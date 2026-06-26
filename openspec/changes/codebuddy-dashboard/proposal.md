# Proposal: CodeBuddy Session 仪表盘（多服务器版）

## 变更 ID
`codebuddy-dashboard`

## 变更类型
新功能 — 独立监控工具

## 背景

用户在多台开发机上同时运行 CodeBuddy CLI session，需要在一台旧安卓手机上统一监控所有机器的 session 状态。当某个 session 完成任务进入"等待用户输入"状态时，需要高亮提醒，避免漏看。

开发机的 IP 地址不固定（DHCP），但有一台固定地址的机器（如 NAS）可以作为中心服务部署点。

---

## 架构方案选型

### 方案 A：中心服务 + Agent 上报（选定）

各开发机运行轻量 Agent，主动推送状态到中心服务。手机只访问中心服务一个地址。

**选择理由**：
- 开发机 IP 不固定，Agent 主动推送可解决此问题（不需要中心服务知道开发机 IP）
- 手机只需记一个固定地址
- 集中视图，可跨机器对比所有 session 状态

### 方案 B：各机器独立 Dashboard，手机多 Tab

每台机器各自运行 Dashboard，手机开多个浏览器 Tab。

**未选择理由**：没有统一视图，手机 IP 变化后需要手动更新书签。

### 方案 C：各机器 Dashboard + 前端聚合页

前端页面直接 fetch 各机器的 API。

**未选择理由**：手机必须能直接访问所有开发机 IP，当开发机 IP 变化时前端配置也需要更新。

---

## 技术栈选型

### 后端：Go

| 维度 | Go | Python |
|------|----|----|
| 部署方式 | 单二进制，scp 即部署 | 需要 Python 环境 + pip install |
| 内存占用 | ~10MB | ~50MB+ |
| 并发读取 | goroutine 天然支持 | asyncio 需要额外配置 |
| 前端打包 | `//go:embed` 将 HTML 打包进二进制 | 需要额外处理 |
| 跨平台编译 | `GOOS=linux GOARCH=amd64 go build` | 需要目标机器有 Python |

选择 Go 的核心理由：**零依赖单二进制**，在多台服务器上部署 Agent 只需 scp 一个文件。

### 前端：纯 HTML/CSS/JS

无框架，无构建步骤，embed 进 Go 二进制。

---

## 关键设计决策

### 1. CodeBuddy Session 状态数据源

**PID 文件**（`~/.codebuddy/sessions/<pid>.json`）：
- 包含 pid、sessionId、cwd、startedAt、kind、lastHeartbeat 等字段
- `lastHeartbeat` 毫秒时间戳：超过 60 秒无更新 → session 已死亡

**JSONL 会话记录**（`~/.codebuddy/projects/<project-name>/<session-uuid>.jsonl`）：
- 实际路径：项目目录下直接放 `<uuid>.jsonl`，**没有** `sessions/` 子目录
- 记录类型（`type` 字段）：`message`、`function_call`、`function_call_result`、`reasoning`、`file-history-snapshot`、`summary`、`ai-title`
- 工具调用是独立的 `function_call` 记录，不嵌套在 message 里

### 2. "等待用户输入"判定

从 JSONL 末尾向上扫描，跳过噪音类型（`file-history-snapshot`/`summary`/`ai-title`/`topic`），找到最近一条有意义的记录：

| 最后有意义记录 | 判定状态 |
|-------------|---------|
| `message` + `role=assistant` + `status=completed` | **等待用户输入** |
| `function_call` 或 `function_call_result` | **活跃** |
| `reasoning` | **活跃** |
| `message` + `role=user` | **活跃** |
| JSONL 5 秒内有修改（任何情况） | **活跃** |

### 3. Agent 掉线检测

中心服务记录每台机器最后一次上报时间：
- 超过 15 秒无上报 → 机器标记为 `offline`
- offline 机器的所有 session 状态冻结显示，加"离线"标记

### 4. 安全性

可选的 shared token 认证：
- Agent 启动时配置 `--token <secret>`
- 每次 HTTP POST 带 `Authorization: Bearer <token>` 头
- 中心服务校验 token，不匹配则拒绝

---

## 风险与缓解

| 风险 | 缓解措施 |
|------|---------|
| JSONL 格式随版本变化 | 容错处理，fallback 到文件修改时间判断 |
| 中心服务单点故障 | 部署在稳定机器（NAS），Agent 上报失败时本地缓存重试 |
| 旧安卓浏览器 Wake Lock 不支持 | HTTP 页面下 Wake Lock API 不可用，建议手动设置手机屏幕常亮时间 |
| 多台机器 session 数量过多 | 前端按机器分组折叠，"等待输入"卡片置顶 |
