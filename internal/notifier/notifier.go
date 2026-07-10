// Package notifier 负责在本机 session 状态从 active 跃迁到 waiting_for_input /
// waiting_for_approval / terminated 时，向用户配置的 target webhook（当前仅企业
// 微信群机器人）主动推送消息。
//
// 组成：
//   - Detector：无外部依赖的状态机，输入本轮 sessions 快照，输出跃迁事件流。
//     负责 KD1 单向阈值、KD2 启动首采样跳过、KD3 5s 防抖。
//   - TargetStore：读写 ~/.coding-pet/targets.json；主进程侧起 goroutine 每 10s
//     重读感知子命令写入的变更。
//   - Dispatcher：按 Target.Kind 分派的接口，当前唯一实现 wechatWorkDispatcher。
//   - Notifier：组合器，暴露 Reconcile(sessions) 给 cmd/agent 主循环。
package notifier

import (
	"context"
	"log"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// dispatchTimeout 是单次 Dispatcher.Send 的上限（与 cmd/agent 现有 server 上报
// 保持一致的 10s）。
const dispatchTimeout = 10 * time.Second

// Notifier 组合 Detector / TargetStore / Dispatcher 工厂，是 cmd/agent 主循环的
// 单一入口。fire-and-forget 语义：Reconcile 内部 log 所有错误，永不返回 error。
type Notifier struct {
	detector      *Detector
	store         TargetStore
	newDispatcher func(Target) (Dispatcher, error)
}

// NewNotifier 构造 Notifier。hostname 用于消息文本拼装（R6）。
func NewNotifier(hostname string, store TargetStore) *Notifier {
	return &Notifier{
		detector:      NewDetector(hostname),
		store:         store,
		newDispatcher: NewDispatcher,
	}
}

// Reconcile 消费本轮 sessions 快照，检测跃迁并向所有 target 分发消息。
// 单个 target 失败不影响其他；无 target 时事件仍被 Detector 消耗（一次性 emit）。
//
// now 由 caller 在采样时刻捕获后传入，保证多 goroutine 并发调用时按 tick 时间
// 演进而非按 goroutine 调度顺序。测试与旧 caller 可传 time.Time{} 让 Detector
// 自己取 now。
func (n *Notifier) Reconcile(ctx context.Context, sessions []protocol.SessionInfo, now time.Time) {
	if now.IsZero() {
		now = time.Now()
	}
	events := n.detector.ReconcileAt(sessions, now)
	if len(events) == 0 {
		return
	}
	targets := n.store.List()
	if len(targets) == 0 {
		log.Printf("notifier: %d transition event(s) dropped (no targets configured)", len(events))
		return
	}
	for _, evt := range events {
		msg := FormatMessage(evt)
		for _, t := range targets {
			n.dispatchOne(ctx, t, msg, evt)
		}
	}
}

// dispatchOne 处理单个 (target, event) 对。任何错误（工厂 / Send）都吞掉写日志，
// 不影响后续 target。
func (n *Notifier) dispatchOne(ctx context.Context, t Target, msg string, evt TransitionEvent) {
	d, err := n.newDispatcher(t)
	if err != nil {
		log.Printf("notifier: skip target [%s]: %v", RedactURL(t.Kind, t.URL), err)
		return
	}
	sendCtx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	if err := d.Send(sendCtx, msg); err != nil {
		log.Printf("notifier: send failed [%s] session=%s state=%s: %v",
			RedactURL(t.Kind, t.URL), shortID(evt.SessionID), evt.NewState, err)
		return
	}
	log.Printf("notifier: sent [%s] session=%s state=%s",
		RedactURL(t.Kind, t.URL), shortID(evt.SessionID), evt.NewState)
}

// shortID 取 SessionID 前 8 字符便于日志聚合。
func shortID(id string) string {
	if len(id) <= 8 {
		return id
	}
	return id[:8]
}
