package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"

	"github.com/simonlei/coding-pet-dashboard/internal/notifier"
)

// newTestWrapper 用 tempdir + $CODING_PET_TARGETS_PATH 隔离子命令的配置文件。
func newTestWrapper(t *testing.T) (*fileTargetStoreWrapper, string, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "targets.json")
	t.Setenv("CODING_PET_TARGETS_PATH", path)
	out, errw := &bytes.Buffer{}, &bytes.Buffer{}
	w := &fileTargetStoreWrapper{
		store: notifier.NewFileTargetStore(""), // env 已注入路径
		out:   out,
		errw:  errw,
	}
	return w, path, out, errw
}

func TestRunAddTarget_NewURL_AppendsAndPrints(t *testing.T) {
	w, _, out, _ := newTestWrapper(t)
	code := runAddTargetWith(w, []string{"https://foo/webhook?key=aaaa1111"})
	if code != 0 {
		t.Fatalf("exit code = %d, want 0; stderr=%s", code, w.errw.(*bytes.Buffer).String())
	}
	if !strings.Contains(out.String(), "added:") {
		t.Errorf("expected 'added:' in stdout, got %q", out.String())
	}
	if !strings.Contains(out.String(), "aaaa****") {
		t.Errorf("expected redacted key aaaa**** in stdout, got %q", out.String())
	}
	if len(w.store.List()) != 1 {
		t.Errorf("expected 1 target after add, got %d", len(w.store.List()))
	}
}

func TestRunAddTarget_DuplicateURL_NoOp(t *testing.T) {
	w, _, _, _ := newTestWrapper(t)
	if code := runAddTargetWith(w, []string{"https://a"}); code != 0 {
		t.Fatalf("first add: %d", code)
	}
	// 重置 out
	w.out = &bytes.Buffer{}
	code := runAddTargetWith(w, []string{"https://a"})
	if code != 0 {
		t.Fatalf("dup add exit = %d, want 0", code)
	}
	if !strings.Contains(w.out.(*bytes.Buffer).String(), "already present") {
		t.Errorf("expected 'already present' message, got %q", w.out.(*bytes.Buffer).String())
	}
	if len(w.store.List()) != 1 {
		t.Errorf("expected still 1 target, got %d", len(w.store.List()))
	}
}

func TestRunAddTarget_MissingURL_Error(t *testing.T) {
	w, _, _, errw := newTestWrapper(t)
	code := runAddTargetWith(w, []string{})
	if code == 0 {
		t.Errorf("expected non-zero exit for missing URL")
	}
	if !strings.Contains(errw.String(), "usage:") {
		t.Errorf("expected usage in stderr, got %q", errw.String())
	}
}

func TestRunAddTarget_CustomKind(t *testing.T) {
	w, _, _, _ := newTestWrapper(t)
	code := runAddTargetWith(w, []string{"--kind", "dingtalk", "https://d"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	list := w.store.List()
	if len(list) != 1 || list[0].Kind != "dingtalk" {
		t.Errorf("expected dingtalk target, got %+v", list)
	}
}

func TestRunListTargets_Empty(t *testing.T) {
	w, _, out, _ := newTestWrapper(t)
	code := runListTargetsWith(w, nil)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	if !strings.Contains(out.String(), "(no targets)") {
		t.Errorf("expected '(no targets)', got %q", out.String())
	}
}

func TestRunListTargets_TwoEntries(t *testing.T) {
	w, _, _, _ := newTestWrapper(t)
	_ = runAddTargetWith(w, []string{"https://a?key=aaaa1111"})
	_ = runAddTargetWith(w, []string{"https://b?key=bbbb2222"})
	// 重置 out
	w.out = &bytes.Buffer{}
	code := runListTargetsWith(w, nil)
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	s := w.out.(*bytes.Buffer).String()
	if !strings.Contains(s, "1) wechat_work") || !strings.Contains(s, "2) wechat_work") {
		t.Errorf("expected two indexed rows, got %q", s)
	}
	if !strings.Contains(s, "aaaa****") || !strings.Contains(s, "bbbb****") {
		t.Errorf("expected both redacted keys in output, got %q", s)
	}
}

func TestRunRemoveTarget_ByIndex(t *testing.T) {
	w, _, _, _ := newTestWrapper(t)
	_ = runAddTargetWith(w, []string{"https://a"})
	_ = runAddTargetWith(w, []string{"https://b"})
	// 重置 out
	w.out = &bytes.Buffer{}
	code := runRemoveTargetWith(w, []string{"1"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	list := w.store.List()
	if len(list) != 1 || list[0].URL != "https://b" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestRunRemoveTarget_ByURL(t *testing.T) {
	w, _, _, _ := newTestWrapper(t)
	_ = runAddTargetWith(w, []string{"https://a"})
	_ = runAddTargetWith(w, []string{"https://b"})
	w.out = &bytes.Buffer{}
	code := runRemoveTargetWith(w, []string{"https://a"})
	if code != 0 {
		t.Fatalf("exit = %d", code)
	}
	list := w.store.List()
	if len(list) != 1 || list[0].URL != "https://b" {
		t.Errorf("unexpected list: %+v", list)
	}
}

func TestRunRemoveTarget_NotFound(t *testing.T) {
	w, _, _, _ := newTestWrapper(t)
	_ = runAddTargetWith(w, []string{"https://a"})
	before := len(w.store.List())
	w.out = &bytes.Buffer{}
	if code := runRemoveTargetWith(w, []string{"999"}); code == 0 {
		t.Error("expected non-zero for out-of-range index")
	}
	if code := runRemoveTargetWith(w, []string{"https://not-there"}); code == 0 {
		t.Error("expected non-zero for unknown URL")
	}
	if code := runRemoveTargetWith(w, []string{"0"}); code == 0 {
		t.Error("expected non-zero for zero (1-based)")
	}
	if got := len(w.store.List()); got != before {
		t.Errorf("target list should be unchanged, got %d entries (want %d)", got, before)
	}
}

func TestRunRemoveTarget_MissingArg(t *testing.T) {
	w, _, _, errw := newTestWrapper(t)
	if code := runRemoveTargetWith(w, nil); code == 0 {
		t.Error("expected non-zero for missing arg")
	}
	if !strings.Contains(errw.String(), "usage:") {
		t.Errorf("expected usage in stderr, got %q", errw.String())
	}
}

func TestRunSubcommand_Dispatch(t *testing.T) {
	// 隔离到 tempdir，避免污染真实 $HOME/.coding-pet/targets.json
	dir := t.TempDir()
	t.Setenv("CODING_PET_TARGETS_PATH", filepath.Join(dir, "targets.json"))

	if code, matched := runSubcommand([]string{"add", "target", "--kind", "wechat_work", "https://x"}); !matched || code != 0 {
		t.Errorf("add target dispatch: matched=%v code=%d", matched, code)
	}
	if _, matched := runSubcommand([]string{"--server", "http://x"}); matched {
		t.Error("flag-style args should not match subcommand")
	}
	// bare 'add' 现在被 subcommand 层拦截并提示 usage（不 fall-through）
	if code, matched := runSubcommand([]string{"add"}); !matched || code != 2 {
		t.Errorf("bare 'add' should be caught with usage; matched=%v code=%d", matched, code)
	}
	if _, matched := runSubcommand([]string{"list", "targets"}); !matched {
		t.Error("list targets should match")
	}
	if _, matched := runSubcommand([]string{"remove", "target", "1"}); !matched {
		t.Error("remove target should match")
	}
	// 词序错误 'agent target list' 也被拦截
	if code, matched := runSubcommand([]string{"target", "list"}); !matched || code != 2 {
		t.Errorf("target list should be caught with usage; matched=%v code=%d", matched, code)
	}
	// 未知词序 'agent add xyz'
	if code, matched := runSubcommand([]string{"add", "xyz"}); !matched || code != 2 {
		t.Errorf("add xyz should be caught; matched=%v code=%d", matched, code)
	}
	// 完全无关的 verb 仍应 fall-through 给 daemon
	if _, matched := runSubcommand([]string{"help"}); matched {
		t.Error("'help' as top-level should fall through to daemon (or be caught by flag.Usage on -h)")
	}
}
