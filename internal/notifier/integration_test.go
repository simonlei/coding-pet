package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestIntegration_ActiveToWaitingSendsRealHTTP 覆盖 AE1 的端到端：真 fileTargetStore
// 从磁盘加载 → 真 Notifier → 真 wechatWorkDispatcher POST 到 httptest server。
// 状态机用假时钟推进以避免真实 5s 等待。
func TestIntegration_ActiveToWaitingSendsRealHTTP(t *testing.T) {
	// 起 mock webhook
	var (
		mu       sync.Mutex
		received []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		var body struct {
			Msgtype string `json:"msgtype"`
			Text    struct {
				Content string `json:"content"`
			} `json:"text"`
		}
		_ = json.Unmarshal(b, &body)
		mu.Lock()
		received = append(received, body.Text.Content)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	// 隔离磁盘配置
	dir := t.TempDir()
	path := filepath.Join(dir, "targets.json")
	store := NewFileTargetStore(path)
	if err := store.Add(Target{Kind: "wechat_work", URL: srv.URL + "/webhook?key=test1234"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 构造 Notifier + 假时钟推进 Detector
	n := NewNotifier("dev-01", store)
	clk := &fakeClock{t: time.Unix(0, 0)}
	n.detector = newDetectorWithClock("dev-01", clk.now)

	// 喂 sessions 序列
	s := protocol.SessionInfo{
		SessionID: "sess-abc123def",
		Tool:      protocol.ToolClaudeCode,
		CWD:       "/tmp/proj",
	}
	clk.t = time.Unix(0, 0)
	n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateActive)}, clk.t)
	clk.t = time.Unix(1, 0)
	n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateWaitingForInput)}, clk.t)
	clk.t = time.Unix(6, 0)
	n.Reconcile(context.Background(), []protocol.SessionInfo{sess(s, protocol.StateWaitingForInput)}, clk.t)

	// httptest handler 是同步调用，Reconcile 内 Send 完才返回。
	mu.Lock()
	defer mu.Unlock()
	if len(received) != 1 {
		t.Fatalf("expected 1 msg, got %d: %+v", len(received), received)
	}
	msg := received[0]
	if !strings.Contains(msg, "dev-01") ||
		!strings.Contains(msg, "claude_code") ||
		!strings.Contains(msg, "/tmp/proj") ||
		!strings.Contains(msg, "waiting_for_input") {
		t.Errorf("message missing fields: %q", msg)
	}
}
