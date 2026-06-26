# Tasks: 修复多机器排序闪动

## 任务概览

| # | 任务 | 标签 | 级别 | 依赖 |
|---|------|------|------|------|
| 1 | 修复后端排序逻辑 (store.go) | `bugfix_simple` | lite | — |
| 2 | 删除前端二次排序 (index.html) | `bugfix_simple`, `ui_component` | lite | — |

---

## 任务详情

### Task 1: 修复后端排序逻辑

**文件**：`internal/server/store.go`

**修改位置**：第 108-116 行

**当前代码**：
```go
// 排序：有等待输入 session 的机器优先，然后按 LastReport 降序
sort.Slice(resp.Machines, func(i, j int) bool {
    wi := haswWaiting(resp.Machines[i])
    wj := haswWaiting(resp.Machines[j])
    if wi != wj {
        return wi // 有等待的排前面
    }
    return resp.Machines[i].LastReport > resp.Machines[j].LastReport
})
```

**替换为**：
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

**验收**：
- `go build ./...` 编译通过
- 多台在线机器时排序顺序稳定不变
- 在线机器排在离线机器前面
- 有 waiting_for_input session 的机器排最前

---

### Task 2: 删除前端二次排序

**文件**：`internal/server/index.html`

**修改位置**：第 528-531 行

**当前代码**：
```javascript
// Sort online machines by last report (most recent first)
onlineMachines.sort(function (a, b) {
    return b.last_report - a.last_report;
});
```

**操作**：删除这 4 行代码

**验收**：
- 前端不再对 onlineMachines 做二次排序
- 列表顺序与后端返回的顺序一致
- 连续轮询时列表不闪动
