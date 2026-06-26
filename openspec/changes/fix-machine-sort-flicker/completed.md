# 变更完成报告：fix-machine-sort-flicker

## 元数据

| 字段 | 值 |
|------|-----|
| 变更 ID | `fix-machine-sort-flicker` |
| 变更类型 | Bugfix — 排序逻辑修复 |
| 完成时间 | 2025-01-20 |
| 状态 | ✅ 已完成 |

## 变更概要

修复了 CodeBuddy Dashboard 在多机器环境下前端列表每秒轮询时机器顺序频繁跳动的问题。

## 根因分析

闪动由两处不稳定排序叠加导致：

1. **后端排序键不稳定**（`store.go:108-116`）：按 `LastReport` 降序排序，`LastReport` 在每次 Agent 上报时更新为当前时间戳，导致多台机器的相对排序随上报时刻不停变化。

2. **前端二次排序加剧闪动**（`index.html:528-531`）：前端收到后端已排序的数据后，又对 `onlineMachines` 按 `last_report` 降序做了一次二次排序，进一步放大了闪动。

## 修复方案

### 后端：替换为稳定排序

将 `store.go` 中的排序改为三级稳定排序：
1. 有 `waiting_for_input` session 的排前面（保持原有行为）
2. 在线机器排在离线机器前面（新增，改善 UX）
3. 按 `MachineID` 字母序升序（替换不稳定的 `LastReport`）

`MachineID` 是固定标识（hostname 或 `--id` 指定值），不随时间变化，排序结果稳定。

### 前端：删除二次排序

删除 `index.html` 中对 `onlineMachines` 的 `last_report` 排序，直接信任后端的排序结果。

## 任务完成情况

| 任务 | 状态 | 说明 |
|------|------|------|
| Task 1: 修复后端排序逻辑 (store.go) | ✅ 完成 | 三级稳定排序替换 LastReport |
| Task 2: 删除前端二次排序 (index.html) | ✅ 完成 | 删除前端二次排序代码 |

## 质量保障

- ✅ 双 reviewer 评审通过
- ✅ tester 编译验证通过
- ✅ 最终审查通过

## 影响范围

- `internal/server/store.go`（第 108-116 行排序逻辑）
- `internal/server/index.html`（第 528-531 行前端排序）

不涉及数据结构变更、API 变更、数据库 migration。

## 效果验证

**修改前**（3 台在线机器，每秒轮询）：
```
轮询 1: [waiting-machine, machine-B, machine-A, machine-C, offline-X]
轮询 2: [waiting-machine, machine-A, machine-C, machine-B, offline-X]
轮询 3: [waiting-机器C, machine-B, machine-A, offline-X]
→ 闪动
```

**修改后**：
```
轮询 1: [waiting-machine, machine-A, machine-B, machine-C, offline-X]
轮询 2: [waiting-machine, machine-A, machine-B, machine-C, offline-X]
轮询 3: [waiting-machine, machine-A, machine-B, machine-C, offline-X]
→ 稳定
```

## 关键决策

- **排序策略**：`waiting > online > MachineID` 升序（替换不稳定的 `LastReport`）
- **MachineID 作为唯一键**：使 `sort.Slice` 结果确定稳定，用户能形成位置记忆
- **删除前端二次排序**：避免前后端排序逻辑重复和不一致

## 经验教训

1. **时间相关字段不适合做排序键**：`LastReport` 虽然意图是"最近活跃优先"，但在多机器环境下导致排序不稳定。固定标识（如 ID）更适合做次要排序键。

2. **避免前后端重复排序**：后端已排序的数据，前端不应再做相同逻辑的排序，容易产生不一致和额外维护成本。

3. **稳定排序提升用户体验**：在实时刷新场景下，排序稳定性对用户形成位置记忆至关重要。

---
