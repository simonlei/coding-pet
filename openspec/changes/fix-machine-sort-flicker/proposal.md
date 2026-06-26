# Proposal: 修复多机器排序闪动

## 变更 ID
`fix-machine-sort-flicker`

## 变更类型
Bugfix — 排序逻辑修复

## 背景

CodeBuddy Dashboard 在多机器环境下，前端列表每秒轮询时机器顺序频繁跳动，影响用户体验。

## 根因

闪动由两处不稳定排序叠加导致：

### 1. 后端排序键不稳定（store.go:108-116）

当前后端按 `LastReport` 降序作为第二级排序键。`LastReport` 在每次 Agent 上报（~5s 间隔）时更新为 Server 本地当前时间戳，导致多台机器的相对排序随上报时刻不停变化。

**示例**：机器 A 和 B 都在 5s 间隔上报，但上报时刻不同步。第一次轮询时 A.LastReport=1001, B.LastReport=1003 → [B, A]；下次轮询时 A.LastReport=1006, B.LastReport=1004 → [A, B]。每秒轮询都可能看到顺序翻转。

### 2. 前端二次排序加剧闪动（index.html:528-531）

前端收到后端已排序的数据后，又对 `onlineMachines` 按 `last_report` 降序做了一次二次排序。由于后端 `LastReport` 本身就不稳定，前端的二次排序进一步放大了闪动。

## 修复方案

### 后端：替换为稳定排序

将 `store.go` 中的排序改为三级：
1. 有 `waiting_for_input` session 的排前面（保持不变）
2. 在线机器排在离线机器前面（显式新增）
3. 按 `MachineID` 字母序升序（替换不稳定的 `LastReport`）

`MachineID` 是固定标识（hostname 或 `--id` 指定值），不随时间变化，排序结果稳定。

### 前端：删除二次排序

删除 `index.html` 中对 `onlineMachines` 的 `last_report` 排序，直接信任后端的排序结果。

## 影响范围

- `internal/server/store.go`（第 108-116 行排序逻辑）
- `internal/server/index.html`（第 528-531 行前端排序）

不涉及数据结构变更、API 变更、数据库 migration。

## 风险与缓解

| 风险 | 缓解 |
|------|------|
| MachineID 字母序可能不符合用户预期的"最近活跃优先" | 用户已确认接受按 MachineID 排序，优先消除闪动 |
| 离线机器穿插在在线机器之间（当前行为） | 显式新增在线优先排序，离线机器统一排在后面 |
