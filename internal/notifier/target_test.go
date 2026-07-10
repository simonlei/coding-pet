package notifier

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testStore 用 t.TempDir 构造一个隔离的 fileTargetStore。
func testStore(t *testing.T) (*fileTargetStore, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "targets.json")
	return NewFileTargetStore(path), path
}

// mustLoad 便于测试断言 Load 无 error。
func mustLoad(t *testing.T, s *fileTargetStore) {
	t.Helper()
	if err := s.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
}

func TestTargetStore_LoadMissingFile(t *testing.T) {
	s, _ := testStore(t)
	mustLoad(t, s)
	if got := s.List(); len(got) != 0 {
		t.Errorf("expected empty list, got %+v", got)
	}
}

func TestTargetStore_AddThenList(t *testing.T) {
	s, path := testStore(t)
	if err := s.Add(Target{Kind: "wechat_work", URL: "https://foo/webhook?key=aaaa1111"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	got := s.List()
	if len(got) != 1 {
		t.Fatalf("expected 1 target, got %d", len(got))
	}
	if got[0].Kind != "wechat_work" || got[0].URL != "https://foo/webhook?key=aaaa1111" {
		t.Errorf("unexpected target: %+v", got[0])
	}
	// 幂等：同一 URL 再 Add 一次
	if err := s.Add(Target{Kind: "wechat_work", URL: "https://foo/webhook?key=aaaa1111"}); err != nil {
		t.Fatalf("second Add: %v", err)
	}
	if got := s.List(); len(got) != 1 {
		t.Errorf("expected still 1 target, got %d", len(got))
	}
	// 落盘验证
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if !strings.Contains(string(b), "aaaa1111") {
		t.Errorf("file does not contain URL: %s", b)
	}
}

func TestTargetStore_LoadCorruptFile(t *testing.T) {
	s, path := testStore(t)
	// 先加一条并落盘
	if err := s.Add(Target{Kind: "wechat_work", URL: "https://ok"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 手动写入非法 JSON
	if err := os.WriteFile(path, []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	err := s.Load()
	if err == nil {
		t.Error("expected error on corrupt file, got nil")
	}
	// cached 保留上一次成功的内容（R12）
	got := s.List()
	if len(got) != 1 || got[0].URL != "https://ok" {
		t.Errorf("expected cached list preserved, got %+v", got)
	}
}

func TestTargetStore_RemoveByURL(t *testing.T) {
	s, _ := testStore(t)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(s.Add(Target{Kind: "wechat_work", URL: "https://a"}))
	must(s.Add(Target{Kind: "wechat_work", URL: "https://b"}))
	must(s.Add(Target{Kind: "wechat_work", URL: "https://c"}))
	if err := s.Remove("https://b"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := s.List()
	if len(got) != 2 || got[0].URL != "https://a" || got[1].URL != "https://c" {
		t.Errorf("unexpected list after remove: %+v", got)
	}
}

func TestTargetStore_RemoveByIndex(t *testing.T) {
	s, _ := testStore(t)
	_ = s.Add(Target{Kind: "wechat_work", URL: "https://a"})
	_ = s.Add(Target{Kind: "wechat_work", URL: "https://b"})
	if err := s.Remove("1"); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	got := s.List()
	if len(got) != 1 || got[0].URL != "https://b" {
		t.Errorf("unexpected list: %+v", got)
	}
}

func TestTargetStore_RemoveNotFound(t *testing.T) {
	s, path := testStore(t)
	_ = s.Add(Target{Kind: "wechat_work", URL: "https://a"})
	before, _ := os.ReadFile(path)
	if err := s.Remove("https://not-there"); err == nil {
		t.Error("expected error for missing URL, got nil")
	}
	if err := s.Remove("999"); err == nil {
		t.Error("expected error for out-of-range index, got nil")
	}
	if err := s.Remove("0"); err == nil {
		t.Error("expected error for zero index (1-based), got nil")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Errorf("file should not change on failed remove\nbefore: %s\nafter:  %s", before, after)
	}
}

func TestTargetStore_AtomicWrite(t *testing.T) {
	s, path := testStore(t)
	// 预先在 tmp 位置写脏数据
	if err := os.WriteFile(path+".tmp", []byte("garbage"), 0o600); err != nil {
		t.Fatalf("prep tmp: %v", err)
	}
	if err := s.Add(Target{Kind: "wechat_work", URL: "https://a"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// 最终 path 内容有效 JSON
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read final: %v", err)
	}
	var raw []Target
	if err := json.Unmarshal(b, &raw); err != nil {
		t.Errorf("final file not valid JSON: %v; content=%s", err, b)
	}
	if len(raw) != 1 || raw[0].URL != "https://a" {
		t.Errorf("unexpected content: %+v", raw)
	}
	// tmp 文件应已被 rename 覆盖或删除（不应残留旧脏数据）
	if _, err := os.Stat(path + ".tmp"); err == nil {
		// tmp 存在可以接受（如果被后续 Add 复用），但内容不该是 "garbage"
		bt, _ := os.ReadFile(path + ".tmp")
		if string(bt) == "garbage" {
			t.Error("stale .tmp garbage still present")
		}
	}
}

func TestTargetStore_WatchReloadsOnChange(t *testing.T) {
	s, path := testStore(t)
	// 初始为空
	mustLoad(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan int, 4)
	s.startWatchWithNotify(ctx, 20*time.Millisecond, func(n int) {
		changes <- n
	})

	// 外部（不走 Add）手写文件模拟子命令
	newContent := []byte(`[{"kind":"wechat_work","url":"https://outside"}]`)
	if err := os.WriteFile(path, newContent, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	select {
	case n := <-changes:
		if n != 1 {
			t.Errorf("expected 1 target after reload, got %d", n)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("watch did not reload within 500ms")
	}
	got := s.List()
	if len(got) != 1 || got[0].URL != "https://outside" {
		t.Errorf("unexpected list after watch: %+v", got)
	}
}

func TestTargetStore_WatchIgnoresIdenticalContent(t *testing.T) {
	s, path := testStore(t)
	_ = s.Add(Target{Kind: "wechat_work", URL: "https://a"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan int, 4)
	s.startWatchWithNotify(ctx, 20*time.Millisecond, func(n int) {
		changes <- n
	})
	// 只改 mtime，不改内容
	future := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(path, future, future); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	select {
	case n := <-changes:
		t.Errorf("expected no change notification, got %d", n)
	case <-time.After(150 * time.Millisecond):
		// good
	}
}

func TestTargetStore_UnknownKindPreserved(t *testing.T) {
	s, path := testStore(t)
	raw := `[{"kind":"dingtalk","url":"https://d"},{"kind":"wechat_work","url":"https://w"}]`
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	mustLoad(t, s)
	got := s.List()
	if len(got) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(got))
	}
	if got[0].Kind != "dingtalk" {
		t.Errorf("expected first kind=dingtalk, got %q", got[0].Kind)
	}
}
