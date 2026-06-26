# Design: 修复多机器排序闪动

## 1. 问题定位

### 1.1 后端排序不稳定

文件：`internal/server/store.go`，第 108-116 行

```go
sort.Slice(resp.Machines, func(i, j int) bool {
    wi := haswWaiting(resp.Machines[i])
    wj := haswWaiting(resp.Machines[j])
    if wi != wj {
        return wi
    }
    return resp.Machines[i].LastReport > resp.Machines[j].LastReport
})
```

`LastReport` 在 `UpdateMachine()` 中被设置为 `time.Now().UnixMilli()`。Agent 每 5 秒上报一次，但多台 Agent 的上报时刻不同步。每次前端轮询（每秒）时，各机器的 `LastReport` 值取决于上次上报时刻，导致相对顺序不停变化。

### 1.2 前端二次排序

文件：`internal/server/index.html`，第 528-531 行

```javascript
onlineMachines.sort(function (a, b) {
    return b.last_report - a.last_report;
});
```

前端将后端返回的 `machines` 数组分为 `onlineMachines` 和 `offlineMachines`，然后对 `onlineMachines` 按 `last_report` 降序排序。这与后端的排序逻辑重复且使用同样的不稳定键。

### 1.3 当前排序行为缺陷

当前后端排序只有两级：等待输入优先 → LastReport 降序。没有显式区分在线/离线，离线机器可能穿插在在线机器之间（取决于 LastReport 值）。

## 2. 修复设计

### 2.1 后端排序（store.go）

将第 108-116 行替换为三级排序：

```go
// 排序：1. 等待输入优先 2. 在线优先 3. MachineID 升序（稳定排序）
sort.Slice(resp.Machines, func(i, j int) bool {
    wi := haswWaiting(resp.Machines[i])
    wj := haswWaiting(resp.Machines[j])
    if wi != wj {
        return wi
    }
    oi := resp.Machines[i].Online
    oj := resp.Machines[j].Online
    if oi != oj {
        return oi
    }
    return resp.Machines[i].MachineID < resp.Machines[j].MachineID
})
```

**排序优先级**：
1. 有 `waiting_for_input` session 的机器排前面（保持原有行为）
2. 在线机器排在离线机器前面（新增，改善 UX）
3. 按 `MachineID` 字母序升序（替换 LastReport，稳定不变）

**选择 MachineID 的理由**：
- `MachineID` 是固定标识（hostname 或通过 `--id` 指定），不随时间变化
- 字母序升序可预测，用户能形成位置记忆
- `sort.Slice` 不保证稳定，但 `MachineID` 是唯一 key，不存在相等的情况

### 2.2 前端排序（index.html）

删除第 528-531 行：

```javascript
// 删除以下代码：
// Sort online machines by last report (most recent first)
// onlineMachines.sort(function (a, b) {
//   return b.last_report - a.last_report;
// });
```

前端 `render()` 函数中，`data.machines` 已由后端排好序，按 online/offline 分组后直接使用，不再二次排序。

### 2.3 修改前后排序效果对比

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

## 3. 涉及文件

| 文件 | 修改行 | 修改内容 |
|------|--------|---------|
| `internal/server/store.go` | 108-116 | 替换排序逻辑，LastReport → Online + MachineID |
| `internal/server/index.html` | 528-531 | 删除前端二次排序 |

## 4. 测试要点

- 多台在线机器（≥2）时，连续轮询列表顺序不变
- 在线机器排在离线机器前面
- 有 waiting_for_input 的机器排在最前
- 离线机器恢复上线后，从离线区域移到在线区域（按 MachineID 排序位置）
