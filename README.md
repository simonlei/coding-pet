# CodeBuddy Dashboard

监控多台服务器上运行的 CodeBuddy CLI session 状态，展示在手机浏览器上。

## 功能

- 显示所有机器上的 CodeBuddy session 数量和状态
- 等待用户输入的 session **高亮闪烁提醒**
- 多机器支持：各开发机 Agent 上报到固定地址的中心 Server
- 离线机器检测（90s 无上报标记为离线，24h 后清理）

## 依赖

- Go 1.21+

## 编译

```bash
make build
```

编译产物：
- `dashboard-agent`：运行在各开发机
- `dashboard-server`：运行在中心服务器

## 部署

### 1. 启动中心服务（Server）

```bash
./dashboard-server --port 3000 --token <your-secret>
```

### 2. 各开发机启动 Agent

```bash
# 基本用法
./dashboard-agent --server http://<中心服务器IP>:3000 --token <your-secret>

# 多台机器 hostname 相同时，必须用 --id 指定唯一标识
./dashboard-agent --server http://<中心服务器IP>:3000 --token <your-secret> --id dev-machine-01
```

或通过环境变量：

```bash
export DASHBOARD_SERVER=http://192.168.1.100:3000
export DASHBOARD_TOKEN=mysecret
export DASHBOARD_ID=my-machine
./dashboard-agent
```

### 3. 手机访问仪表盘

在手机浏览器打开：
```
http://<中心服务器IP>:3000
```

## 选项说明

### dashboard-server

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--port` | 3000 | 监听端口 |
| `--token` | 空（无认证） | 认证 token |

### dashboard-agent

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `--server` | 必填 | Server 地址 |
| `--token` | 空 | 认证 token |
| `--id` | hostname | 唯一机器 ID |
| `--hostname` | os.Hostname() | 显示名 |
| `--interval` | 5s | 上报间隔 |

## 常见问题

### 手机屏幕自动熄屏

`Wake Lock API` 在 HTTP 页面下部分浏览器不支持（需要 HTTPS）。
推荐备用方案：手机设置 → 显示 → 屏幕超时 → 设为"永不"。

### 多台机器 hostname 相同

使用 `--id` 参数为每台机器指定唯一标识，否则同名机器的数据会互相覆盖。

### 掉线检测原理

Agent 每 5 秒上报一次，Server 90 秒未收到上报时标记为离线。
离线机器的 session 状态显示为 `unknown`，避免显示过期的"等待输入"。
