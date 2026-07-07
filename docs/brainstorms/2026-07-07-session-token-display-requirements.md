# 需求：在 Dashboard 展示每个 Session 的当前上下文占用

- **日期**：2026-07-07
- **范围**：Standard（有界特性，采集端 + 数据结构 + 前端小改）
- **状态**：待规划（ce-plan）

## 要解决的问题

Dashboard 现在只告诉你 session 处于什么状态（工作中/等待），但不告诉你每个
session 的 token 用量。在手机上盯着多台机器多个 session 时，你无法一眼判断
**「哪个 session 上下文快满了、该压缩/重开了」**。这个信息现在只能切回到对应
机器、进到 CLI 里才看得到，违背了本项目「手机集中查看」的初衷。

## 目标用户与价值

- **用户**：项目作者本人（在手机上集中监控多台开发机的 AI 编码 session）。
- **改变了什么**：卡片上直接看到每个 session 的当前上下文占用，无需切回机器，
  就能决定要不要去压缩上下文或重开会话。

## 要建的东西

在每个 session 卡片上展示该 session 的**当前上下文占用**（token 数），随每秒
采集实时刷新。

## 数据来源（已实测验证）

各工具的 token 数据可得性，实测结论：

| 工具 | 当前上下文占用取自 | 状态 |
|------|-------------------|------|
| Claude Code（`~/.claude`、`~/.tclaude`） | JSONL 最后一条 assistant 记录 `message.usage` 的 `input_tokens + cache_read_input_tokens + cache_creation_input_tokens` | ✅ 已验证字段存在 |
| CodeBuddy CLI（`~/.codebuddy`） | JSONL 中 `usage` 的 `input_tokens`（`total_tokens` 亦可选，取哪个留给规划） | ✅ 已验证字段存在（`{"input_tokens":...,"output_tokens":...,"total_tokens":...}`） |
| CodeBuddy IDE | 未验证（`history/` 目录结构里是否含 usage 待查） | ⚠️ 未验证，降级处理 |
| WorkBuddy | 未验证（本机无 `~/.workbuddy/workbuddy.db`，无法确认 sessions 表是否有 token 列） | ⚠️ 未验证，降级处理 |

关键语义差异（规划时需保留）：Claude Code 的 usage 是**当前上下文快照**（最后
一条 assistant 消息的 input+cache 约等于当前上下文窗口占用）；CodeBuddy 的
`total_tokens` 更接近单次调用量。两者展示含义不同，卡片文案需能区分或统一为
「当前占用」口径。

## 范围边界

**本次要做：**
- 采集端从各工具 JSONL/数据源解析出「当前上下文占用」token 数。
- 复用现有「读文件末尾 16KB 反向扫描」逻辑（`internal/collector/jsonl.go`），
  顺带解析 usage，采集成本几乎为零。
- 在 `SessionInfo`（`internal/protocol/types.go`）增加承载该数值的字段。
- 前端 session 卡片展示该数值，实时刷新。
- IDE / WorkBuddy 若拿不到 token，**优雅降级**：卡片不显示 token 字段，不阻塞
  也不报错。

**明确不做（已决策推迟）：**
- ❌ **累计 token 消耗**。原因：准确的累计需从头全扫整个 JSONL 累加
  `output_tokens`，而采集是每秒一次，大文件全扫成本高。当前占用只需读末尾，
  几乎零成本。累计价值更偏「事后好奇」，先不做，观察后再定。
- ❌ **历史/持久化**。Server 保持全内存、重启即清空的现状不变。只展示实时快照。
- ❌ 成本估算（token→金额）。
- ❌ cache 命中率、上下文增长速度等深度指标。

## 成功标准

- 在手机 dashboard 上，Claude Code 与 CodeBuddy CLI 的每个活跃 session 卡片
  显示出当前上下文占用 token 数，数值随采集刷新。
- IDE / WorkBuddy 的 session 在拿不到 token 时正常显示（无 token 字段），
  不报错、不崩卡片。
- 采集端 CPU/磁盘开销相比现状无明显增加（不引入全文件扫描）。

## 未决问题（留给规划或后续）

1. **是否显示为占最大窗口的百分比**（如 `120k / 200k`）。JSONL 的 `model` 字段
   可拿到，可据此映射各模型上下文上限。先显示绝对值；百分比作为增强特性，需
   维护「模型→上限」映射表，成本需评估。
2. **CodeBuddy CLI 取 `input_tokens` 还是 `total_tokens`** 作为「当前占用」口径
   ——两者语义不同，规划时结合实际展示需求定。
3. **CodeBuddy IDE / WorkBuddy 的 token 字段验证**：需在有 IDE 会话历史、有
   WorkBuddy 库的机器上实地确认结构后，再决定能否纳入（当前默认降级）。

## 涉及的代码位置（供规划参考）

- `internal/protocol/types.go`：`SessionInfo` 增加 token 字段。
- `internal/collector/jsonl.go`：`JSONLEntry` / `readLastMeaningfulEntry` 附近解析
  usage（Claude Code 与 CodeBuddy CLI 格式不同，需分别处理）。
- `internal/collector/claudecode.go`：Claude Code 采集路径接入 usage 解析。
- `internal/collector/codebuddy_ide.go`、`workbuddy_db.go`：降级路径（不阻塞）。
- `internal/server/embed.go`（内嵌 `index.html`）：前端卡片渲染 token。
- 各采集器对应的 `*_test.go`：按 TDD 先补测试样例（含含 usage / 不含 usage 两种
  fixture）。
