package notifier

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestPackageCompiles 只确认 U1 骨架能编译、构造函数可调用不 panic。
func TestPackageCompiles(t *testing.T) {
	store := NewFileTargetStore("")
	if store == nil {
		t.Fatal("NewFileTargetStore returned nil")
	}
	n := NewNotifier("test-host", store)
	if n == nil {
		t.Fatal("NewNotifier returned nil")
	}
	d, err := NewDispatcher(Target{Kind: "wechat_work", URL: "https://example.com/webhook"})
	if err != nil {
		t.Fatalf("NewDispatcher wechat_work: %v", err)
	}
	if d.Kind() != "wechat_work" {
		t.Errorf("Kind() = %q, want wechat_work", d.Kind())
	}
	if _, err := NewDispatcher(Target{Kind: "dingtalk", URL: "..."}); err == nil {
		t.Error("expected error for unknown kind, got nil")
	}
}

// fakeStore 是测试用 TargetStore。
type fakeStore struct{ targets []Target }

func (f *fakeStore) List() []Target { return f.targets }

// fakeDispatcher 记录收到的消息；可控制 Send 返回 error。
type fakeDispatcher struct {
	kind  string
	mu    sync.Mutex
	sends []string
	fail  error
}

func (f *fakeDispatcher) Kind() string { return f.kind }
func (f *fakeDispatcher) Send(ctx context.Context, msg string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, msg)
	return f.fail
}
func (f *fakeDispatcher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sends)
}

// makeNotifierWithFakes 构造一个 Notifier，dispatcher 工厂返回预置的 fake 序列
// （按 Target 顺序返回，与 store.List 顺序一致）。同时返回可推进的时钟。
func makeNotifierWithFakes(t *testing.T, hostname string, targets []Target, fakes []*fakeDispatcher) (*Notifier, *fakeClock) {
	t.Helper()
	if len(targets) != len(fakes) {
		t.Fatalf("targets(%d) and fakes(%d) length mismatch", len(targets), len(fakes))
	}
	byURL := map[string]*fakeDispatcher{}
	for i, tgt := range targets {
		byURL[tgt.URL] = fakes[i]
	}
	n := NewNotifier(hostname, &fakeStore{targets: targets})
	n.newDispatcher = func(tgt Target) (Dispatcher, error) {
		d, ok := byURL[tgt.URL]
		if !ok {
			return nil, errors.New("no fake for URL " + tgt.URL)
		}
		return d, nil
	}
	clk := &fakeClock{t: time.Unix(0, 0)}
	n.detector = newDetectorWithClock(hostname, clk.now)
	return n, clk
}

// triggerTransition 通过喂 3 轮 sessions 触发一次 active→waiting_for_input →
// 稳定 5s 的跃迁，验证事件成功抵达 dispatcher。
func triggerTransition(t *testing.T, n *Notifier, clk *fakeClock) {
	t.Helper()
	s := protocol.SessionInfo{
		SessionID: "sess-1abc",
		Tool:      protocol.ToolClaudeCode,
		CWD:       "/tmp/proj",
	}
	// t=0 active
	clk.t = time.Unix(0, 0)
	n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateActive)}, clk.t)
	// t=1 waiting_for_input（候选）
	clk.t = time.Unix(1, 0)
	n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateWaitingForInput)}, clk.t)
	// t=6 满 5s，emit
	clk.t = time.Unix(6, 0)
	n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateWaitingForInput)}, clk.t)
}

func sess(base protocol.SessionInfo, state protocol.SessionState) protocol.SessionInfo {
	base.State = state
	return base
}

func TestNotifier_EventFansOutToAllTargets(t *testing.T) {
	targets := []Target{
		{Kind: "wechat_work", URL: "https://a"},
		{Kind: "wechat_work", URL: "https://b"},
	}
	fakes := []*fakeDispatcher{{kind: "wechat_work"}, {kind: "wechat_work"}}
	n, clk := makeNotifierWithFakes(t, "dev-01", targets, fakes)
	triggerTransition(t, n, clk)
	if fakes[0].count() != 1 || fakes[1].count() != 1 {
		t.Errorf("expected each fake to receive 1 msg, got %d and %d", fakes[0].count(), fakes[1].count())
	}
	if fakes[0].sends[0] != fakes[1].sends[0] {
		t.Errorf("both dispatchers should get same msg, got %q vs %q", fakes[0].sends[0], fakes[1].sends[0])
	}
}

// TestNotifier_OneTargetFails_OthersReceive covers AE6.
func TestNotifier_OneTargetFails_OthersReceive(t *testing.T) {
	targets := []Target{
		{Kind: "wechat_work", URL: "https://a"},
		{Kind: "wechat_work", URL: "https://b"},
	}
	fakes := []*fakeDispatcher{
		{kind: "wechat_work", fail: errors.New("simulated")},
		{kind: "wechat_work"},
	}
	n, clk := makeNotifierWithFakes(t, "dev-01", targets, fakes)
	triggerTransition(t, n, clk)
	if fakes[0].count() != 1 || fakes[1].count() != 1 {
		t.Errorf("both dispatchers should be attempted; got %d and %d", fakes[0].count(), fakes[1].count())
	}
}

func TestNotifier_UnknownKind_LoggedSkipped(t *testing.T) {
	targets := []Target{
		{Kind: "dingtalk", URL: "https://d"},
		{Kind: "wechat_work", URL: "https://w"},
	}
	// dingtalk 由默认工厂返回 error；wechat_work 用 fake 覆盖
	wfake := &fakeDispatcher{kind: "wechat_work"}
	n := NewNotifier("dev-01", &fakeStore{targets: targets})
	n.newDispatcher = func(tgt Target) (Dispatcher, error) {
		if tgt.Kind == "wechat_work" {
			return wfake, nil
		}
		return NewDispatcher(tgt) // 会返回 unknown kind error
	}
	clk := &fakeClock{}
	n.detector = newDetectorWithClock("dev-01", clk.now)
	triggerTransition(t, n, clk)
	if wfake.count() != 1 {
		t.Errorf("wechat_work should have received 1 msg, got %d", wfake.count())
	}
}

func TestNotifier_NoTargets_EventsSilenced(t *testing.T) {
	n := NewNotifier("dev-01", &fakeStore{})
	clk := &fakeClock{}
	n.detector = newDetectorWithClock("dev-01", clk.now)
	// 不 panic 即可
	triggerTransition(t, n, clk)
}

func TestNotifier_NoEvents_NoDispatch(t *testing.T) {
	fakes := []*fakeDispatcher{{kind: "wechat_work"}}
	targets := []Target{{Kind: "wechat_work", URL: "https://a"}}
	n, clk := makeNotifierWithFakes(t, "dev-01", targets, fakes)
	// 只投喂 active，永远无跃迁
	s := protocol.SessionInfo{SessionID: "s1", Tool: protocol.ToolClaudeCode, CWD: "/tmp"}
	for i := 0; i < 10; i++ {
		clk.t = time.Unix(int64(i), 0)
		n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateActive)}, clk.t)
	}
	if fakes[0].count() != 0 {
		t.Errorf("expected 0 sends, got %d", fakes[0].count())
	}
}

