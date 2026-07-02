package collector

import (
	"os"
	"path/filepath"
	"testing"
)

// TestLastRunState_WaitingForPermission 验证：能从日志中解出该 session
// 最后一条状态机 transition 的 to= 状态。
func TestLastRunState_WaitingForPermission(t *testing.T) {
	base := t.TempDir()
	logDir := filepath.Join(base, "logs", "2026-06-29")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sid := "7d5bb766-c0f0-49ff-8f9e-8b56253fa9a8"
	content := `[6/29/2026, 5:01:09 PM.537] [Info] [SessionRunStateMachine]  transition | sessionId=` + sid + ` | event=MODEL_STREAM_STARTED | from=model_streaming | to=model_streaming | lifecycle=running
[6/29/2026, 5:01:14 PM.092] [Info] [SessionRunStateMachine]  transition | sessionId=` + sid + ` | event=MODEL_RESPONSE_DONE | from=model_streaming | to=model_done | lifecycle=running
[6/29/2026, 5:01:14 PM.100] [Info] [SessionRunStateMachine]  transition | sessionId=` + sid + ` | event=WAITING_FOR_PERMISSION | from=model_done | to=waiting_for_permission | lifecycle=waiting
`
	if err := os.WriteFile(filepath.Join(logDir, "coding-pet__x.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := lastRunStateIn(base, sid); got != "waiting_for_permission" {
		t.Errorf("expected waiting_for_permission, got %q", got)
	}
}

// TestLastRunState_PicksSessionAndLatestFile 验证：多 session 混写、多日志文件时，
// 只取目标 session 的最后一条 transition，且以最新 mtime 的文件为准。
func TestLastRunState_PicksSessionAndLatestFile(t *testing.T) {
	base := t.TempDir()
	logDir := filepath.Join(base, "logs", "2026-06-29")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sid := "aaaa1111"
	other := "bbbb2222"
	// 目标 session 后又出现了 other session 的行，不能被 other 干扰
	content := `[t1] [Info] [SessionRunStateMachine]  transition | sessionId=` + sid + ` | to=tool_executing | lifecycle=running
[t2] [Info] [SessionRunStateMachine]  transition | sessionId=` + other + ` | to=waiting_for_permission | lifecycle=waiting
`
	if err := os.WriteFile(filepath.Join(logDir, "a.log"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	if got := lastRunStateIn(base, sid); got != "tool_executing" {
		t.Errorf("expected tool_executing, got %q", got)
	}
}

// TestLastRunState_NoMatch 验证：无匹配日志时返回空串。
func TestLastRunState_NoMatch(t *testing.T) {
	base := t.TempDir()
	if got := lastRunStateIn(base, "nonexistent-session"); got != "" {
		t.Errorf("expected empty, got %q", got)
	}
}
