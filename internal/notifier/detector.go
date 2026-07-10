package notifier

import (
	"os"
	"strings"
	"sync"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// debounceWindow 是"新状态稳定多久才触发推送"的门槛（KD3 / R4 默认 5s）。
const debounceWindow = 5 * time.Second

// sessionEntryStalePurge 是 session 从采集结果消失多久后从内存清理（Assumptions）。
const sessionEntryStalePurge = 60 * time.Second

// TransitionEvent 是 Detector 输出的跃迁事件。字段全部为字符串，不携带 protocol
// 类型，避免下游 Dispatcher 层依赖 internal/protocol。
type TransitionEvent struct {
	Hostname  string
	Tool      string
	CWD       string
	NewState  string
	SessionID string // 保留供日志用（脱敏后打 session=<id-8>）
}

// sessionEntry 是 Detector 为每个 SessionID 维护的状态。
type sessionEntry struct {
	// baselineArmed 表示该 session 是否已被观察到过一次 active。
	// R3：启动首采样不算跃迁，必须先见到一次 active 才建立推送基线。
	baselineArmed bool

	// candidateState 是当前正在防抖计时中的新状态。空字符串表示无候选。
	candidateState protocol.SessionState
	// candidateFirstSeen 是 candidateState 第一次被观察到的时间。
	candidateFirstSeen time.Time

	// lastState 是上一轮观察到的状态；用于判定"状态是否发生变化"。
	lastState protocol.SessionState

	// lastSeen 是该 session 最近一次出现在采集结果中的时间；超过
	// sessionEntryStalePurge 未见即清理。
	lastSeen time.Time
}

// Detector 负责跃迁检测。可并发调用（内部有 mu），但主循环通常单 goroutine
// 调用；为跨 goroutine 场景保留 mu 保护。
type Detector struct {
	hostname string
	now      func() time.Time // 保留供无 now 参数的旧调用点使用（现主要走 ReconcileAt）

	mu      sync.Mutex
	entries map[string]*sessionEntry
}

// NewDetector 构造 Detector。hostname 用于消息拼装（R6）。
func NewDetector(hostname string) *Detector {
	return newDetectorWithClock(hostname, time.Now)
}

// newDetectorWithClock 内部构造入口，测试可注入假时钟。
func newDetectorWithClock(hostname string, now func() time.Time) *Detector {
	return &Detector{
		hostname: hostname,
		now:      now,
		entries:  make(map[string]*sessionEntry),
	}
}

// Reconcile 是 ReconcileAt(sessions, d.now()) 的便捷入口。留下供旧测试与
// 单 goroutine 场景使用；主循环用 ReconcileAt 显式传入 tick 捕获的时间戳。
func (d *Detector) Reconcile(sessions []protocol.SessionInfo) []TransitionEvent {
	return d.ReconcileAt(sessions, d.now())
}

// isNotifyTarget 判定给定 state 是否属于"需要通知"的三种（KD1 / R1）。
func isNotifyTarget(s protocol.SessionState) bool {
	switch s {
	case protocol.StateWaitingForInput,
		protocol.StateWaitingForApproval,
		protocol.StateTerminated:
		return true
	default:
		return false
	}
}

// Reconcile 消费本轮 sessions 快照，返回本轮应推送的跃迁事件。
//
// 传入 now 是"这一轮采样发生的时刻"。主循环在启动 goroutine 之前立即捕获 now，
// 保证即使 goroutine 乱序拿到 d.mu，语义上仍按 tick 顺序演进（避免后 tick 先
// 拿锁把 lastState/lastSeen 更新为新值，随后前 tick 拿锁又覆盖回旧值）。
//
// 语义（brainstorm KD1-KD3 / R1-R4）：
//   - 首次见到某 session：新建 entry；若初见即 active 立即 arm baseline，
//     否则 baselineArmed=false（R3 启动跳过存量）。
//   - baselineArmed=false 时任何状态变化都不生成候选（必须先见一次 active）。
//   - lastState=active 且新状态是 waiting_for_input / waiting_for_approval /
//     terminated → 建立防抖候选。
//   - 已有候选期间：
//   - 若又切回 active → 清空候选、baselineArmed=true。
//   - 若切到 *另一个* waiting 类型（互切）→ 清空候选（KD1，不重启新候选）。
//   - 若维持同一新状态 ≥ debounceWindow → emit 一次并清空候选。
func (d *Detector) ReconcileAt(sessions []protocol.SessionInfo, now time.Time) []TransitionEvent {
	d.mu.Lock()
	defer d.mu.Unlock()

	seen := make(map[string]struct{}, len(sessions))
	var events []TransitionEvent

	for _, s := range sessions {
		seen[s.SessionID] = struct{}{}

		e, ok := d.entries[s.SessionID]
		if !ok {
			e = &sessionEntry{
				baselineArmed: s.State == protocol.StateActive,
				lastState:     s.State,
				lastSeen:      now,
			}
			d.entries[s.SessionID] = e
			continue
		}

		prev := e.lastState
		e.lastState = s.State
		e.lastSeen = now

		// R3：还未 arm baseline，只在见到 active 时置 armed，其他状态忽略。
		if !e.baselineArmed {
			if s.State == protocol.StateActive {
				e.baselineArmed = true
			}
			continue
		}

		// 已 armed：正常状态机
		switch {
		case s.State == protocol.StateActive:
			// 回到工作状态：清空任何候选。
			e.candidateState = ""
			e.candidateFirstSeen = time.Time{}

		case e.candidateState == "":
			// 无候选。仅当 lastState 是 active 且新状态是通知目标时才建立候选。
			// （waiting 之间互切不由此触发 —— 那种情况 prev != active。）
			if prev == protocol.StateActive && isNotifyTarget(s.State) {
				e.candidateState = s.State
				e.candidateFirstSeen = now
			}

		case s.State == e.candidateState:
			// 候选状态维持。检查是否满足防抖窗口。
			if now.Sub(e.candidateFirstSeen) >= debounceWindow {
				events = append(events, TransitionEvent{
					Hostname:  d.hostname,
					Tool:      string(s.Tool),
					CWD:       s.CWD,
					NewState:  string(s.State),
					SessionID: s.SessionID,
				})
				// 清空候选：同一状态在下一轮 active→再跃迁前不再重复 emit。
				e.candidateState = ""
				e.candidateFirstSeen = time.Time{}
			}

		default:
			// 候选存在但状态又变了（waiting_input ↔ waiting_approval 互切等）。
			// KD1：waiting 互切不推 —— 清空候选，等下次 active→waiting 才重新建。
			e.candidateState = ""
			e.candidateFirstSeen = time.Time{}
		}
	}

	// 清理长时间未见的 session。
	for id, e := range d.entries {
		if _, ok := seen[id]; ok {
			continue
		}
		if now.Sub(e.lastSeen) > sessionEntryStalePurge {
			delete(d.entries, id)
		}
	}

	return events
}

// FormatMessage 把 event 拼装成 "{hostname} / {tool} {cwd~} / {state}"。
// CWD 若以 $HOME 前缀开始，替换成 ~/；否则原样保留（R7）。
func FormatMessage(evt TransitionEvent) string {
	cwd := evt.CWD
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if cwd == home {
			cwd = "~"
		} else if strings.HasPrefix(cwd, home+string(os.PathSeparator)) {
			cwd = "~" + cwd[len(home):]
		}
	}
	var b strings.Builder
	b.WriteString(evt.Hostname)
	b.WriteString(" / ")
	b.WriteString(evt.Tool)
	b.WriteString(" ")
	b.WriteString(cwd)
	b.WriteString(" / ")
	b.WriteString(evt.NewState)
	return b.String()
}
