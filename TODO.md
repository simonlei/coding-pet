# Coding Pet Dashboard — 优化 TODO

> 生成日期：2026-08-24 · 基线 commit：`5f25c09`
> 所有条目均已核对真实代码，附 `file:line`。未经核实的推测已剔除（见文末「已排除」）。

---

## P0 — 建议优先做

### [ ] 1. CI 补上 test / vet

**现状**：`.github/workflows/build.yml` 只做交叉编译打包，`grep 'go test\|go vet\|golangci' .github/workflows/` 零命中。
8452 行代码、5 个包有测试，但 PR 合并时一次都不跑。

**改动**：在 build.yml 的 build job 前加一个 `test` job（或在现有 job 的 build 步骤前插入）：

```yaml
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: '1.25', cache: true }
      - run: go vet ./...
      - run: go test -race ./...
```

并让 `build` job `needs: test`。

**验收**：故意改坏一个测试，CI 变红。

---

### [ ] 2. Dashboard 与 API 无鉴权

**现状**：`internal/server/handler.go:28` 注释明写 `/api/status` 无需认证；`handler.go:35` 的 `/`（Web UI）同样。
任何能连到 server 端口的人都可以看到全部机器的 `machine_id` 与每个 session 的 `cwd`（项目路径本身即信息泄露）。
`--token` 目前只保护了 agent → server 的上报方向。

**改动**（三选一，按部署环境定）：
- 最小：Web UI + `/api/status` 加 HTTP Basic Auth，凭据复用 `--token` 或新增 `--web-token`
- 或：只监听内网/回环 + 反向代理做认证（则本条降级为文档说明）
- 注意 Android 客户端（`android/`）需同步支持所配置的认证方式

**验收**：未带凭据请求 `/api/status` 返回 401。

---

### [ ] 3. 请求体大小无限制

**位置**：`internal/server/handler.go:55` — `io.ReadAll(r.Body)` 无上限。

**改动**：

```go
r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1MB
```

**验收**：POST 一个 >1MB 的体，返回 413 而非 OOM。

---

### [ ] 4. HTTP Server 无任何超时

**位置**：`cmd/server/main.go:58-61` — `http.Server{Addr, Handler}`，四个超时字段全缺省（即无限）。
慢速客户端可长期占住 goroutine。

**改动**：

```go
srv := &http.Server{
    Addr:              addr,
    Handler:           mux,
    ReadHeaderTimeout: 5 * time.Second,
    ReadTimeout:       15 * time.Second,
    WriteTimeout:      15 * time.Second,
    IdleTimeout:       60 * time.Second,
}
```

---

## P1 — 明显收益，改动小

### [ ] 5. 采集热路径重复查找 JSONL（每秒 × 每 session 双倍 I/O）

**位置**：`internal/collector/collector.go:67` 与 `collector.go:95` 对同一 session 各调一次 `findSessionJSONLIn`
（该函数会遍历 `projects/` 全目录并逐项 stat）。

**额外**：`collector.go:92` 的注释写着「复用同一 JSONL 路径」，但代码并未复用 —— 注释与实现不一致，改完顺带对齐。

**改动**：把第 67 行的 `jsonlPath, found` 提升到 `if/else` 之外复用，删掉第 95 行的二次查找。纯收益，无行为变化。

**验收**：现有 `collector_test.go` 全绿。

---

### [ ] 6. 每个 session 重复全量扫描运行日志

**位置**：`internal/collector/logstate.go:46` — 每次调用执行 `filepath.Glob(logs/*/*.log)` + 对全部结果 `os.Stat` + `sort.Slice`。
调用点 `collector.go:79` 位于 per-session 循环内 → N 个 session 就是 N 次重复扫描。

**实测**：本机 `~/.codebuddy/logs/` 已有 36 个 `.log` 文件，只增不减。

**改动**：在 `CollectSessions` 每轮开头构建一次「按 mtime 降序的日志文件列表」，作为参数传入 `collectOne` / `lastRunStateIn`。

---

### [ ] 7. 前端每秒全量重绘 + 无退避

**位置**：`internal/server/index.html`

三个叠加问题：

| 问题 | 位置 | 后果 |
|------|------|------|
| `setInterval(fetchStatus, 1000)` 不等上一次完成 | `:947` | 网络慢时请求堆积 |
| `catch` 里重绘 `lastData`，但无退避 | `:922-937` | server 挂掉后手机每秒重试到天亮 |
| 数据未变也 `container.innerHTML = html` 全量重建 | `:730`、`:788` | 每秒 reflow/repaint —— Android 常亮客户端的主要耗电来源 |

**改动**：
1. `setInterval` → `setTimeout` 递归自调度
2. 失败计数 + 指数退避（1s → 上限 30s），成功即复位
3. 重绘前短路：`if (JSON.stringify(data) === JSON.stringify(lastData)) { schedule(); return; }`

第 3 点一行代码即可挡掉绝大多数无谓重绘，优先做。

---

### [ ] 8. token 比较改为常量时间

**位置**：`internal/server/handler.go:49` — `authHeader != expected`。

**改动**：`subtle.ConstantTimeCompare([]byte(authHeader), []byte(expected)) != 1`。

> 对 bearer token 的实际可利用性很低，但一行的事，随 P0-2 一起改。

---

## P2 — 可维护性

### [ ] 9. 三份重复的 3min / 30min 窗口常量

同一业务语义（活跃窗口 / 失效窗口）复制了三遍，命名各不相同：

| 文件 | 常量 |
|------|------|
| `internal/collector/codebuddy_ide.go:43,45` | `cbIDEActiveWindow` / `cbIDEStaleWindow` |
| `internal/collector/workbuddy_db.go:39,41` | `wbActiveWindow` / `wbStaleWindow` |
| `internal/collector/codebuddy_ide_remote.go:57,59` | `cbIDERemoteActiveWindow` / `cbIDERemoteStaleWindow` |

README 把这套阈值当作**统一行为**描述，但调整时要动 6 处，漏一处就前端表现不一致。

**改动**：抽到 `internal/collector` 的共享常量（如 `sessionActiveWindow` / `sessionStaleWindow`），保留各处注释说明其工具语义。

**顺带**：`collector.go:60` 的 `60_000`（CLI 心跳超时）也提成具名常量。

---

### [ ] 10. Makefile 缺质量入口

**现状**：只有 `build` / `build-agent` / `build-server` / `clean`，本地无一键跑测试的入口；且 `go build` 未注入版本（CI 用 ldflags 注了，本地构建出来是空版本）。

**改动**：

```makefile
.PHONY: test vet fmt-check

VERSION ?= dev
LDFLAGS  = -X main.version=$(VERSION)

test:
	go test -race ./...

vet:
	go vet ./...

fmt-check:
	@test -z "$$(gofmt -l ./cmd ./internal)" || (gofmt -l ./cmd ./internal; exit 1)
```

并给两个 `build-*` 目标加上 `-ldflags "$(LDFLAGS)"`。

---

### [ ] 11. 测试覆盖空洞

| 包 | 覆盖率 |
|----|--------|
| `cmd/server` | **0%**（无测试文件） |
| `internal/applog` | **0%**（无测试文件） |
| `internal/server` | 35.3% |
| `cmd/agent` | 37.5% |
| `internal/collector` | 65.2% |
| `internal/selfupdate` | 71.2% |
| `internal/notifier` | 79.3% |

**建议**：P0-2/3/4 要动 `handler.go` 与 `cmd/server/main.go`，正好按 TDD 先补 handler 测试（鉴权 401、超大 body 413、方法校验）再改实现，顺带把 `internal/server` 提到 70%+。

---

### [ ] 12. `json.Encode` 返回值未检查

**位置**：`internal/server/handler.go:87`。编码失败时响应头已发出，前端收到截断 JSON 且服务端无日志。至少记一条 log。

---

### [ ] 13. 临时文件清理

`docs/ideation/2026-07-13-open-ideation.html`（42KB，git 未跟踪）按 CLAUDE.md「临时文件不写入项目目录」规范应移出仓库。

---

## 已排除（核查后确认不是问题）

以下几条是同类项目的常见怀疑点，**本项目已做对**，勿被误导：

| 怀疑点 | 核查结论 |
|--------|----------|
| 仓库提交了 ~50MB 二进制 / `nohup.out` / `logs/` | ❌ 不成立。`.gitignore` 覆盖完整，`git ls-files` 确认均未跟踪；工作区那些只是本地产物 |
| 自更新未校验产物完整性 | ❌ 不成立。`internal/selfupdate/github.go:245` `DownloadAndVerify` 对照 `SHA256SUMS.txt`，`:309` 不匹配即拒绝 |
| 解压存在 zip-slip 路径穿越 | ❌ 不成立。`archive.go` 走 `os.CreateTemp` + `matchEntry` 只取 basename，从不使用归档内路径 |
| `store.GetDashboard` 返回内部引用致 data race | ❌ 不成立。`store.go:94-96` 已值拷贝 + `copy()`，`SessionInfo` 字段全为值类型 |
| `go vet` 有告警 | ❌ 不成立。`go vet ./...` 干净通过 |

## 判定为过度工程（暂不做）

- selfupdate 的重试 / 熔断 / 断点续传 —— 「30 分钟一次检查、失败下轮再来」的场景不需要
- webhook 发送重试与指数退避 —— 同上，agent 每轮都会重新对账
- SQLite 连接缓存加 TTL
- notifier `entries` 内存增长 —— 已有 60s stale purge 兜底，非真泄漏

---

## 建议执行顺序

1. **一次提交**：P0-1（CI）—— 先立护栏，后续改动才有保障
2. **一次提交**：P0-2/3/4 + P1-8 —— 都在 handler / server main，按 TDD 先补测试（顺带完成 P2-11 的一半）
3. **一次提交**：P1-5/6 —— 纯性能，无行为变化，现有测试即可验收
4. **一次提交**：P1-7 —— 前端，可先只做「数据未变则跳过重绘」一行改动验证效果
5. 其余 P2 随手做
