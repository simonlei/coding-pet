package notifier

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestWechatDispatcher_Success(t *testing.T) {
	var gotBody []byte
	var gotContentType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = b
		gotContentType = r.Header.Get("Content-Type")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"errcode":0}`))
	}))
	defer srv.Close()

	d := newWechatWorkDispatcher(srv.URL)
	if err := d.Send(context.Background(), "hello world"); err != nil {
		t.Fatalf("Send: %v", err)
	}
	if gotContentType != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", gotContentType)
	}
	var raw struct {
		Msgtype string `json:"msgtype"`
		Text    struct {
			Content string `json:"content"`
		} `json:"text"`
	}
	if err := json.Unmarshal(gotBody, &raw); err != nil {
		t.Fatalf("parse body: %v (raw=%s)", err, gotBody)
	}
	if raw.Msgtype != "text" {
		t.Errorf("msgtype = %q, want text", raw.Msgtype)
	}
	if raw.Text.Content != "hello world" {
		t.Errorf("text.content = %q, want hello world", raw.Text.Content)
	}
}

func TestWechatDispatcher_Non2xx(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := newWechatWorkDispatcher(srv.URL)
	err := d.Send(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !contains(err.Error(), "500") {
		t.Errorf("expected error to mention 500, got %v", err)
	}
}

func TestWechatDispatcher_Timeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 睡固定 500ms，client 侧的 ctx 会在 50ms 触发 cancel。
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	d := newWechatWorkDispatcher(srv.URL)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := d.Send(ctx, "msg")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
}

func TestNewDispatcher_UnknownKind(t *testing.T) {
	_, err := NewDispatcher(Target{Kind: "dingtalk", URL: "http://x"})
	if err == nil {
		t.Fatal("expected error for unknown kind")
	}
	if !contains(err.Error(), "unknown target kind") {
		t.Errorf("error should mention unknown target kind, got %v", err)
	}
}

func TestNewDispatcher_WechatWork(t *testing.T) {
	d, err := NewDispatcher(Target{Kind: "wechat_work", URL: "http://x"})
	if err != nil {
		t.Fatalf("NewDispatcher: %v", err)
	}
	if d.Kind() != "wechat_work" {
		t.Errorf("Kind = %q, want wechat_work", d.Kind())
	}
}

// TestWechatDispatcher_NetworkErrorDoesNotLeakURL: 网络错误时 err.Error() 不含
// 完整 webhook URL（尤其是 key= secret）。防止上层用 %v 打日志泄漏 secret。
// 底层 dial 错误可能含目标 host:port（企微 webhook host 本身是公开的，不视作 secret）。
func TestWechatDispatcher_NetworkErrorDoesNotLeakURL(t *testing.T) {
	url := "http://127.0.0.1:1/webhook?key=SECRET-abcdef1234567890"
	d := newWechatWorkDispatcher(url)
	err := d.Send(context.Background(), "msg")
	if err == nil {
		t.Fatal("expected network error, got nil")
	}
	msg := err.Error()
	if contains(msg, "SECRET-abcdef1234567890") {
		t.Errorf("error leaks secret: %q", msg)
	}
	if contains(msg, "?key=") {
		t.Errorf("error leaks key= query: %q", msg)
	}
}

// TestWechatDispatcher_TimeoutDoesNotLeakURL: context 超时错误路径同样脱敏。
func TestWechatDispatcher_TimeoutDoesNotLeakURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
	}))
	defer srv.Close()
	url := srv.URL + "?key=TIMEOUT-secret-abcd"
	d := newWechatWorkDispatcher(url)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	err := d.Send(ctx, "msg")
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if contains(err.Error(), "TIMEOUT-secret-abcd") {
		t.Errorf("timeout error leaks secret: %q", err.Error())
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
