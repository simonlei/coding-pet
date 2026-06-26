package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/codebuddy-dashboard/internal/protocol"
)

func TestMapClaudeStatus(t *testing.T) {
	cases := []struct {
		name       string
		status     string
		waitingFor string
		want       protocol.SessionState
	}{
		{"busy is active", "busy", "", protocol.StateActive},
		{"shell is active", "shell", "", protocol.StateActive},
		{"idle is active", "idle", "", protocol.StateActive},
		{"empty is active", "", "", protocol.StateActive},
		{"unknown is active", "frobnicate", "", protocol.StateActive},
		{"waiting permission prompt is approval", "waiting", "permission prompt", protocol.StateWaitingForApproval},
		{"waiting sandbox request is approval", "waiting", "sandbox request", protocol.StateWaitingForApproval},
		{"waiting worker request is approval", "waiting", "worker request", protocol.StateWaitingForApproval},
		{"waiting dialog open is input", "waiting", "dialog open", protocol.StateWaitingForInput},
		{"waiting input needed is input", "waiting", "input needed", protocol.StateWaitingForInput},
		{"waiting unknown reason is input", "waiting", "", protocol.StateWaitingForInput},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapClaudeStatus(tc.status, tc.waitingFor)
			if got != tc.want {
				t.Errorf("mapClaudeStatus(%q,%q)=%q, want %q", tc.status, tc.waitingFor, got, tc.want)
			}
		})
	}
}

func TestClaudeConfigDir_EnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/cfg")
	if got := claudeConfigDir(); got != "/custom/cfg" {
		t.Errorf("expected env override /custom/cfg, got %q", got)
	}
}

func TestClaudeConfigDir_FallbackTclaude(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".tclaude")
	if got := claudeConfigDir(); got != want {
		t.Errorf("expected fallback %q, got %q", want, got)
	}
}

// writeClaudePID 写一个 Claude Code pid 文件到 sessions 目录
func writeClaudePID(t *testing.T, dir string, name string, content string) {
	t.Helper()
	sessionsDir := filepath.Join(dir, "sessions")
	if err := os.MkdirAll(sessionsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sessionsDir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCollectClaudeCode_LiveBusy(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid() // 当前进程，保证存活

	content := `{"pid":` + itoa(pid) + `,"sessionId":"sess-live","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-10_000) + `,"kind":"interactive","entrypoint":"cli","status":"busy","version":"2.1","updatedAt":` +
		i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	writeClaudePID(t, dir, "live.json", content)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	s := sessions[0]
	if s.Tool != protocol.ToolClaudeCode {
		t.Errorf("expected Tool=claude_code, got %q", s.Tool)
	}
	if s.State != protocol.StateActive {
		t.Errorf("expected active, got %q", s.State)
	}
	if s.LastActivity != now {
		t.Errorf("expected LastActivity=%d (statusUpdatedAt), got %d", now, s.LastActivity)
	}
	if s.SessionID != "sess-live" {
		t.Errorf("expected SessionID=sess-live, got %q", s.SessionID)
	}
}

func TestCollectClaudeCode_WaitingApproval(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid()

	content := `{"pid":` + itoa(pid) + `,"sessionId":"sess-wait","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-10_000) + `,"kind":"interactive","status":"waiting","waitingFor":"permission prompt","updatedAt":` +
		i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	writeClaudePID(t, dir, "wait.json", content)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].State != protocol.StateWaitingForApproval {
		t.Errorf("expected waiting_for_approval, got %q", sessions[0].State)
	}
}

func TestCollectClaudeCode_StaleIsTerminated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid() // 存活，但 statusUpdatedAt 过期

	content := `{"pid":` + itoa(pid) + `,"sessionId":"sess-stale","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-200_000) + `,"kind":"interactive","status":"busy","updatedAt":` +
		i64toa(now-120_000) + `,"statusUpdatedAt":` + i64toa(now-120_000) + `}`
	writeClaudePID(t, dir, "stale.json", content)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].State != protocol.StateTerminated {
		t.Errorf("expected terminated (stale), got %q", sessions[0].State)
	}
}

func TestCollectClaudeCode_DeadProcessIsTerminated(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()

	content := `{"pid":999999,"sessionId":"sess-dead","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-10_000) + `,"kind":"interactive","status":"busy","updatedAt":` +
		i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	writeClaudePID(t, dir, "dead.json", content)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].State != protocol.StateTerminated {
		t.Errorf("expected terminated (dead pid), got %q", sessions[0].State)
	}
}

func TestCollectClaudeCode_MalformedSkipped(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid()

	writeClaudePID(t, dir, "bad.json", `{not valid json`)
	good := `{"pid":` + itoa(pid) + `,"sessionId":"ok","cwd":"/tmp","startedAt":` +
		i64toa(now) + `,"status":"busy","updatedAt":` + i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	writeClaudePID(t, dir, "good.json", good)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 valid session (bad skipped), got %d", len(sessions))
	}
	if sessions[0].SessionID != "ok" {
		t.Errorf("expected the good session, got %q", sessions[0].SessionID)
	}
}

func TestCollectClaudeCode_NoDir(t *testing.T) {
	dir := t.TempDir() // 无 sessions 子目录
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if sessions := CollectClaudeCodeSessions(); sessions != nil {
		t.Errorf("expected nil when sessions dir absent, got %v", sessions)
	}
}

// 小整数转字符串辅助（避免在 fixture 拼接里引入 strconv import 噪音到测试主体）
func itoa(i int) string { return i64toa(int64(i)) }
func i64toa(i int64) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}
