package notifier

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// fakeClock 允许测试手动推进时间。
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newSession(id string, state protocol.SessionState) protocol.SessionInfo {
	return protocol.SessionInfo{
		SessionID: id,
		Tool:      protocol.ToolClaudeCode,
		CWD:       "/home/tester/work/coding-pet",
		State:     state,
	}
}

// reconcileAt 在给定时刻调用 Reconcile。
func reconcileAt(d *Detector, clk *fakeClock, at time.Time, sessions ...protocol.SessionInfo) []TransitionEvent {
	clk.t = at
	return d.Reconcile(sessions)
}

// TestDetector_ActiveToWaitingForApproval_EmitsAfterDebounce covers AE1.
func TestDetector_ActiveToWaitingForApproval_EmitsAfterDebounce(t *testing.T) {
	clk := &fakeClock{t: time.Unix(1000, 0)}
	d := newDetectorWithClock("dev-01", clk.now)

	// t=0: active（arm baseline）
	if evts := reconcileAt(d, clk, time.Unix(1000, 0), newSession("s1", protocol.StateActive)); len(evts) != 0 {
		t.Fatalf("t=0 expected 0 events, got %d", len(evts))
	}
	// t=1: 跃迁到 waiting_for_approval，尚未防抖满
	if evts := reconcileAt(d, clk, time.Unix(1001, 0), newSession("s1", protocol.StateWaitingForApproval)); len(evts) != 0 {
		t.Fatalf("t=1 expected 0 events (debounce not met), got %d", len(evts))
	}
	// t=6: 已保持 5s，emit
	evts := reconcileAt(d, clk, time.Unix(1006, 0), newSession("s1", protocol.StateWaitingForApproval))
	if len(evts) != 1 {
		t.Fatalf("t=6 expected 1 event, got %d", len(evts))
	}
	if evts[0].NewState != string(protocol.StateWaitingForApproval) {
		t.Errorf("event NewState = %q, want waiting_for_approval", evts[0].NewState)
	}
	if evts[0].SessionID != "s1" {
		t.Errorf("event SessionID = %q, want s1", evts[0].SessionID)
	}
}

// TestDetector_WaitingBackToActive_NoEmit covers AE2.
func TestDetector_WaitingBackToActive_NoEmit(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("dev-01", clk.now)

	reconcileAt(d, clk, time.Unix(0, 0), newSession("s1", protocol.StateActive))
	reconcileAt(d, clk, time.Unix(1, 0), newSession("s1", protocol.StateWaitingForInput))
	if evts := reconcileAt(d, clk, time.Unix(6, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 1 {
		t.Fatalf("t=6 expected 1 emit, got %d", len(evts))
	}
	// waiting → active：不 emit
	if evts := reconcileAt(d, clk, time.Unix(7, 0), newSession("s1", protocol.StateActive)); len(evts) != 0 {
		t.Fatalf("waiting→active expected 0 events, got %d", len(evts))
	}
	// 再次 active → waiting，防抖后应该重新 emit（session 回到工作状态又停下，仍是一次值得关注的跃迁）
	if evts := reconcileAt(d, clk, time.Unix(8, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 0 {
		t.Fatalf("t=8 new candidate expected 0 events, got %d", len(evts))
	}
	if evts := reconcileAt(d, clk, time.Unix(13, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 1 {
		t.Fatalf("t=13 expected 1 emit, got %d", len(evts))
	}
}

// TestDetector_JitterDebounced covers AE3.
func TestDetector_JitterDebounced(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("dev-01", clk.now)

	reconcileAt(d, clk, time.Unix(0, 0), newSession("s1", protocol.StateActive))
	reconcileAt(d, clk, time.Unix(1, 0), newSession("s1", protocol.StateWaitingForInput))
	reconcileAt(d, clk, time.Unix(3, 0), newSession("s1", protocol.StateActive))          // 抖回 active，candidate 清空
	reconcileAt(d, clk, time.Unix(4, 0), newSession("s1", protocol.StateWaitingForInput)) // 新候选
	// t=4 到 t=8 只 4s < 5s，不 emit
	if evts := reconcileAt(d, clk, time.Unix(8, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 0 {
		t.Fatalf("t=8 expected 0 events (only 4s), got %d", len(evts))
	}
	// t=9 满 5s，emit
	if evts := reconcileAt(d, clk, time.Unix(9, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 1 {
		t.Fatalf("t=9 expected 1 emit, got %d", len(evts))
	}
}

// TestDetector_StartupExistingWaitingNotEmitted covers AE4.
func TestDetector_StartupExistingWaitingNotEmitted(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("dev-01", clk.now)

	// 首采样看到已经处于 waiting_for_input：不 arm baseline，永远不 emit（直到见 active）
	for i := 0; i < 20; i++ {
		evts := reconcileAt(d, clk, time.Unix(int64(i), 0), newSession("s1", protocol.StateWaitingForInput))
		if len(evts) != 0 {
			t.Fatalf("iteration %d: expected 0 events, got %d", i, len(evts))
		}
	}
	// 现在给一次 active
	reconcileAt(d, clk, time.Unix(20, 0), newSession("s1", protocol.StateActive))
	// 然后跃迁 + 稳定 5s
	reconcileAt(d, clk, time.Unix(21, 0), newSession("s1", protocol.StateWaitingForInput))
	if evts := reconcileAt(d, clk, time.Unix(26, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 1 {
		t.Fatalf("post-arm: expected 1 emit, got %d", len(evts))
	}
}

// TestDetector_WaitingInputToApproval_NoEmit：KD1 waiting 互切不推。
func TestDetector_WaitingInputToApproval_NoEmit(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("dev-01", clk.now)

	reconcileAt(d, clk, time.Unix(0, 0), newSession("s1", protocol.StateActive))
	reconcileAt(d, clk, time.Unix(1, 0), newSession("s1", protocol.StateWaitingForInput))
	if evts := reconcileAt(d, clk, time.Unix(6, 0), newSession("s1", protocol.StateWaitingForInput)); len(evts) != 1 {
		t.Fatalf("t=6 expected 1 emit, got %d", len(evts))
	}
	// 现在从 waiting_for_input 切到 waiting_for_approval——不 emit
	reconcileAt(d, clk, time.Unix(7, 0), newSession("s1", protocol.StateWaitingForApproval))
	if evts := reconcileAt(d, clk, time.Unix(12, 0), newSession("s1", protocol.StateWaitingForApproval)); len(evts) != 0 {
		t.Fatalf("waiting_input→waiting_approval expected 0 events, got %d", len(evts))
	}
}

// TestDetector_TerminatedTriggersEmit：active→terminated 稳定 5s 后 emit。
func TestDetector_TerminatedTriggersEmit(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("dev-01", clk.now)

	reconcileAt(d, clk, time.Unix(0, 0), newSession("s1", protocol.StateActive))
	reconcileAt(d, clk, time.Unix(1, 0), newSession("s1", protocol.StateTerminated))
	evts := reconcileAt(d, clk, time.Unix(6, 0), newSession("s1", protocol.StateTerminated))
	if len(evts) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(evts))
	}
	if evts[0].NewState != string(protocol.StateTerminated) {
		t.Errorf("NewState = %q, want terminated", evts[0].NewState)
	}
}

// TestDetector_SessionDisappearsPurged：session 消失 >60s 后再回来视作新 session。
func TestDetector_SessionDisappearsPurged(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("dev-01", clk.now)

	reconcileAt(d, clk, time.Unix(0, 0), newSession("s1", protocol.StateActive))
	// 消失 61s
	for i := 1; i <= 61; i++ {
		reconcileAt(d, clk, time.Unix(int64(i), 0))
	}
	// 现在 s1 已从 map 清理；重新出现在 waiting 状态——baseline 未 arm，不 emit
	for i := 62; i <= 80; i++ {
		evts := reconcileAt(d, clk, time.Unix(int64(i), 0), newSession("s1", protocol.StateWaitingForInput))
		if len(evts) != 0 {
			t.Fatalf("after purge iteration %d: expected 0 events, got %d", i, len(evts))
		}
	}
}

// TestFormatMessage_HomeReplacement：CWD 里的 $HOME 替换成 ~/。
func TestFormatMessage_HomeReplacement(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home dir on this host: %v", err)
	}
	evt := TransitionEvent{
		Hostname: "dev-01",
		Tool:     string(protocol.ToolClaudeCode),
		CWD:      filepath.Join(home, "work/coding-pet"),
		NewState: string(protocol.StateWaitingForInput),
	}
	got := FormatMessage(evt)
	want := "dev-01 / claude_code ~/work/coding-pet / waiting_for_input"
	if got != want {
		t.Errorf("FormatMessage:\n got: %q\nwant: %q", got, want)
	}
}

// TestFormatMessage_NonHomePath：CWD 不在 home 下 → 原样保留。
func TestFormatMessage_NonHomePath(t *testing.T) {
	evt := TransitionEvent{
		Hostname: "dev-01",
		Tool:     "claude_code",
		CWD:      "/opt/foo",
		NewState: "waiting_for_input",
	}
	got := FormatMessage(evt)
	want := "dev-01 / claude_code /opt/foo / waiting_for_input"
	if got != want {
		t.Errorf("FormatMessage:\n got: %q\nwant: %q", got, want)
	}
	if strings.Contains(got, "~") {
		t.Errorf("did not expect ~ in output: %q", got)
	}
}

// TestDetector_CarriesHostname：event.Hostname 沿用 Detector 构造时传入的值。
func TestDetector_CarriesHostname(t *testing.T) {
	clk := &fakeClock{}
	d := newDetectorWithClock("machine-xyz", clk.now)
	reconcileAt(d, clk, time.Unix(0, 0), newSession("s1", protocol.StateActive))
	reconcileAt(d, clk, time.Unix(1, 0), newSession("s1", protocol.StateWaitingForInput))
	evts := reconcileAt(d, clk, time.Unix(6, 0), newSession("s1", protocol.StateWaitingForInput))
	if len(evts) != 1 {
		t.Fatalf("expected 1 emit, got %d", len(evts))
	}
	if evts[0].Hostname != "machine-xyz" {
		t.Errorf("Hostname = %q, want machine-xyz", evts[0].Hostname)
	}
	if evts[0].Tool != string(protocol.ToolClaudeCode) {
		t.Errorf("Tool = %q, want claude_code", evts[0].Tool)
	}
	if !strings.HasSuffix(evts[0].CWD, "coding-pet") {
		t.Errorf("CWD = %q, want suffix coding-pet", evts[0].CWD)
	}
}
