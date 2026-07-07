package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
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

func TestClaudeConfigDirs_EnvOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "/custom/cfg")
	got := claudeConfigDirs()
	if len(got) != 1 || got[0] != "/custom/cfg" {
		t.Errorf("expected env override [/custom/cfg], got %v", got)
	}
}

func TestClaudeConfigDirs_FallbackThreeDirs(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, _ := os.UserHomeDir()
	want := []string{
		filepath.Join(home, ".claude-internal"),
		filepath.Join(home, ".claude"),
		filepath.Join(home, ".tclaude"),
	}
	got := claudeConfigDirs()
	if len(got) != len(want) {
		t.Fatalf("expected %d dirs, got %d: %v", len(want), len(got), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("dir[%d]=%q, want %q", i, got[i], want[i])
		}
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

func TestCollectClaudeCode_StaleTimestampStillActive(t *testing.T) {
	// Claude Code 的 statusUpdatedAt 是事件驱动的（非周期心跳），
	// 因此时间戳很旧但进程存活的 session 仍应视为 active，不能误判 terminated。
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid() // 存活，但 statusUpdatedAt 过期

	content := `{"pid":` + itoa(pid) + `,"sessionId":"sess-stale","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-2_000_000) + `,"kind":"interactive","status":"busy","updatedAt":` +
		i64toa(now-1_800_000) + `,"statusUpdatedAt":` + i64toa(now-1_800_000) + `}`
	writeClaudePID(t, dir, "stale.json", content)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].State != protocol.StateActive {
		t.Errorf("expected active (alive process, stale timestamp), got %q", sessions[0].State)
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

// TestCollectClaudeCode_ContextTokens 验证：pid 文件对应的 JSONL 存在时，
// 解析出 message.usage 填充 ContextTokens；JSONL 缺失时降级为 0。
func TestCollectClaudeCode_ContextTokens(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid()

	content := `{"pid":` + itoa(pid) + `,"sessionId":"sess-tok","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-10_000) + `,"kind":"interactive","status":"busy","updatedAt":` +
		i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	writeClaudePID(t, dir, "tok.json", content)

	// 写对应 JSONL：<configDir>/projects/<proj>/sess-tok.jsonl
	projDir := filepath.Join(dir, "projects", "-tmp-proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonl := `{"type":"assistant","message":{"usage":{"input_tokens":10,"cache_read_input_tokens":90,"cache_creation_input_tokens":0}}}` + "\n"
	if err := os.WriteFile(filepath.Join(projDir, "sess-tok.jsonl"), []byte(jsonl), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ContextTokens != 100 {
		t.Errorf("expected ContextTokens=100, got %d", sessions[0].ContextTokens)
	}
}

// TestCollectClaudeCode_NoJSONL_TokensZero 验证：无对应 JSONL 时 ContextTokens 降级为 0，不报错。
func TestCollectClaudeCode_NoJSONL_TokensZero(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	now := time.Now().UnixMilli()
	pid := os.Getpid()

	content := `{"pid":` + itoa(pid) + `,"sessionId":"sess-notok","cwd":"/tmp/proj","startedAt":` +
		i64toa(now-10_000) + `,"kind":"interactive","status":"busy","updatedAt":` +
		i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	writeClaudePID(t, dir, "notok.json", content)

	sessions := CollectClaudeCodeSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ContextTokens != 0 {
		t.Errorf("expected ContextTokens=0 (no jsonl), got %d", sessions[0].ContextTokens)
	}
}

func TestCollectClaudeCode_NoDir(t *testing.T) {
	dir := t.TempDir() // 无 sessions 子目录
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	if sessions := CollectClaudeCodeSessions(); sessions != nil {
		t.Errorf("expected nil when sessions dir absent, got %v", sessions)
	}
}

// TestReadClaudeCodePIDFilesFrom_MergeAndDedup 验证多目录扫描：
// 多个配置目录下的 session 会被合并；同一 sessionId 在多个目录出现时只保留一份。
func TestReadClaudeCodePIDFilesFrom_MergeAndDedup(t *testing.T) {
	now := time.Now().UnixMilli()
	pid := os.Getpid()
	dirA := t.TempDir()
	dirB := t.TempDir()

	mk := func(sessionID string) string {
		return `{"pid":` + itoa(pid) + `,"sessionId":"` + sessionID + `","cwd":"/tmp","startedAt":` +
			i64toa(now) + `,"status":"busy","updatedAt":` + i64toa(now) + `,"statusUpdatedAt":` + i64toa(now) + `}`
	}
	writeClaudePID(t, dirA, "a.json", mk("sess-a"))
	writeClaudePID(t, dirB, "b.json", mk("sess-b"))
	// sess-a 在两个目录都出现，应去重
	writeClaudePID(t, dirB, "dup.json", mk("sess-a"))

	files := readClaudeCodePIDFilesFrom([]string{dirA, dirB})
	seen := map[string]int{}
	for _, f := range files {
		seen[f.SessionID]++
	}
	if len(seen) != 2 {
		t.Fatalf("expected 2 unique sessions, got %d: %v", len(seen), seen)
	}
	if seen["sess-a"] != 1 {
		t.Errorf("expected sess-a deduped to 1, got %d", seen["sess-a"])
	}
	if seen["sess-b"] != 1 {
		t.Errorf("expected sess-b present once, got %d", seen["sess-b"])
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
