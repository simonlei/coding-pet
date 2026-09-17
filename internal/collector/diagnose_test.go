package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// writeDiagHome 在 tmp 目录里搭一个最小 agent home，返回其路径。
func writeDiagHome(t *testing.T, sessionID, jsonlLastLine, logLines string) string {
	t.Helper()
	dir := t.TempDir()

	projDir := filepath.Join(dir, "projects", "proj")
	if err := os.MkdirAll(projDir, 0o755); err != nil {
		t.Fatal(err)
	}
	jsonlPath := filepath.Join(projDir, sessionID+".jsonl")
	if err := os.WriteFile(jsonlPath, []byte(jsonlLastLine+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// 让 mtime 老于 5s，绕过 DetermineStateFromJSONL 的"刚写过就算 active"短路。
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(jsonlPath, old, old); err != nil {
		t.Fatal(err)
	}

	if logLines != "" {
		logDir := filepath.Join(dir, "logs", "2026-09-17")
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(logDir, "a.log"), []byte(logLines), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func transitionLine(sessionID, to string) string {
	return fmt.Sprintf("2026-09-17 10:00:00 [SessionRunStateMachine]  transition | sessionId=%s | from=x | to=%s | ok\n", sessionID, to)
}

func stepValue(d Diagnosis, name string) string {
	for _, s := range d.Steps {
		if s.Name == name {
			return s.Value + " | " + s.Detail
		}
	}
	return ""
}

// 悬空 function_call + 运行日志停在 tool_executing → 链路把 approval 纠正成 active，
// 这正是"实际在等审批但状态显示 active"的可疑环节，诊断必须明确指出来。
func TestDiagnoseOne_RunningStateSwallowsApproval(t *testing.T) {
	sid := "sess-swallow"
	jsonl := `{"type":"function_call","name":"Bash","arguments":"{\"command\":\"rm -rf /tmp/x\",\"dangerouslyDisableSandbox\":true}"}`
	home := writeDiagHome(t, sid, jsonl, transitionLine(sid, "tool_executing"))

	pf := PIDFile{PID: os.Getpid(), SessionID: sid, CWD: "/tmp/proj", LastHeartbeat: time.Now().UnixMilli()}
	d := diagnoseOne(buddyHome{dir: home, tool: protocol.ToolCodeBuddy}, pf, 5, time.Now())

	if d.State != protocol.StateActive {
		t.Fatalf("state = %s, want active (复刻现有链路行为)", d.State)
	}
	rec := stepValue(d, "reconcile")
	if !strings.Contains(rec, "waiting_for_approval → active") {
		t.Errorf("reconcile step should show the downgrade: %s", rec)
	}
	if !strings.Contains(rec, "问题极可能出在这一步") {
		t.Errorf("reconcile step should flag itself as the suspect: %s", rec)
	}
	if len(d.Transitions) != 1 || !strings.Contains(d.Transitions[0], "to=tool_executing") {
		t.Errorf("transitions not captured: %+v", d.Transitions)
	}
}

// 运行日志明确 waiting_for_permission → 最终 waiting_for_approval。
func TestDiagnoseOne_PermissionHit(t *testing.T) {
	sid := "sess-perm"
	jsonl := `{"type":"function_call","name":"Bash","arguments":"{}"}`
	logs := transitionLine(sid, "tool_executing") + transitionLine(sid, "waiting_for_permission")
	home := writeDiagHome(t, sid, jsonl, logs)

	pf := PIDFile{PID: os.Getpid(), SessionID: sid, CWD: "/tmp/proj", LastHeartbeat: time.Now().UnixMilli()}
	d := diagnoseOne(buddyHome{dir: home, tool: protocol.ToolCodeBuddy}, pf, 5, time.Now())

	if d.State != protocol.StateWaitingForApproval {
		t.Fatalf("state = %s, want waiting_for_approval", d.State)
	}
	if len(d.Transitions) != 2 {
		t.Fatalf("want 2 transitions, got %+v", d.Transitions)
	}
	// 时间正序：最后一条应是 waiting_for_permission
	if !strings.Contains(d.Transitions[1], "waiting_for_permission") {
		t.Errorf("transitions should be oldest→newest: %+v", d.Transitions)
	}
}

// 运行日志里完全没有该 session 的 transition → 保留 JSONL 判定，并在诊断中说明。
func TestDiagnoseOne_NoRunLog(t *testing.T) {
	sid := "sess-nolog"
	jsonl := `{"type":"function_call","name":"Bash","arguments":"{}"}`
	home := writeDiagHome(t, sid, jsonl, transitionLine("other-session", "tool_executing"))

	pf := PIDFile{PID: os.Getpid(), SessionID: sid, CWD: "/tmp/proj", LastHeartbeat: time.Now().UnixMilli()}
	d := diagnoseOne(buddyHome{dir: home, tool: protocol.ToolCodeBuddy}, pf, 5, time.Now())

	if d.State != protocol.StateWaitingForApproval {
		t.Fatalf("state = %s, want waiting_for_approval (JSONL 判定保留)", d.State)
	}
	if got := stepValue(d, "runlog"); !strings.Contains(got, "找不到该 session") {
		t.Errorf("runlog step should say not found: %s", got)
	}
}

// 心跳过期时直接判死，且明确告知后续环节被跳过（排查"session 不见了"用）。
func TestDiagnoseOne_StaleHeartbeatShortCircuits(t *testing.T) {
	sid := "sess-dead"
	home := writeDiagHome(t, sid, `{"type":"function_call","name":"Bash","arguments":"{}"}`, "")

	pf := PIDFile{PID: os.Getpid(), SessionID: sid, LastHeartbeat: time.Now().Add(-5 * time.Minute).UnixMilli()}
	d := diagnoseOne(buddyHome{dir: home, tool: protocol.ToolCodeBuddy}, pf, 5, time.Now())

	if d.State != protocol.StateTerminated {
		t.Fatalf("state = %s, want terminated", d.State)
	}
	if got := stepValue(d, "结论"); !strings.Contains(got, "60s") {
		t.Errorf("should explain the 60s heartbeat rule: %s", got)
	}
}

// JSONL 未落盘 → 保守判 active，诊断需说明原因。
func TestDiagnoseOne_NoJSONL(t *testing.T) {
	dir := t.TempDir()
	pf := PIDFile{PID: os.Getpid(), SessionID: "sess-fresh", LastHeartbeat: time.Now().UnixMilli()}
	d := diagnoseOne(buddyHome{dir: dir, tool: protocol.ToolCodeBuddy}, pf, 0, time.Now())

	if d.State != protocol.StateActive {
		t.Fatalf("state = %s, want active", d.State)
	}
	if got := stepValue(d, "jsonl"); !strings.Contains(got, "未找到") {
		t.Errorf("jsonl step wrong: %s", got)
	}
}

// diagnoseOne 的最终状态必须与 collectOne 一致，否则诊断就是在骗人。
func TestDiagnoseOne_MatchesCollectOne(t *testing.T) {
	cases := []struct {
		name  string
		jsonl string
		logs  string
	}{
		{"dangling call + running log", `{"type":"function_call","name":"Bash","arguments":"{}"}`, "tool_executing"},
		{"dangling call + permission", `{"type":"function_call","name":"Bash","arguments":"{}"}`, "waiting_for_permission"},
		{"ask user question", `{"type":"function_call","name":"AskUserQuestion","arguments":"{}"}`, "tool_executing"},
		{"assistant completed", `{"type":"message","role":"assistant","status":"completed"}`, ""},
		{"tool result", `{"type":"function_call_result"}`, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sid := "sess-cmp"
			logs := ""
			if tc.logs != "" {
				logs = transitionLine(sid, tc.logs)
			}
			home := writeDiagHome(t, sid, tc.jsonl, logs)
			h := buddyHome{dir: home, tool: protocol.ToolCodeBuddy}
			pf := PIDFile{PID: os.Getpid(), SessionID: sid, LastHeartbeat: time.Now().UnixMilli()}

			c := New()
			want, err := c.collectOne(h, pf)
			if err != nil {
				t.Fatal(err)
			}
			got := diagnoseOne(h, pf, 0, time.Now())
			if got.State != want.State {
				t.Errorf("diagnose=%s collectOne=%s — 诊断与真实判定不一致", got.State, want.State)
			}
		})
	}
}

func TestDiagnoseOther_SkipsCLIAndAnnotates(t *testing.T) {
	out := DiagnoseOther([]protocol.SessionInfo{
		{Tool: protocol.ToolCodeBuddy, SessionID: "cli"},
		{Tool: protocol.ToolCodeBuddyIDERemote, SessionID: "remote", State: protocol.StateActive},
	})
	if len(out) != 1 || out[0].Tool != protocol.ToolCodeBuddyIDERemote {
		t.Fatalf("should keep only non-CLI: %+v", out)
	}
	if !strings.Contains(out[0].Note, "exthost") {
		t.Errorf("note should explain remote data source: %s", out[0].Note)
	}
}

func TestLastRunStateInWithSource(t *testing.T) {
	sid := "sess-src"
	home := writeDiagHome(t, sid, `{"type":"message"}`, transitionLine(sid, "waiting_for_permission"))
	state, src := lastRunStateInWithSource(home, sid)
	if state != "waiting_for_permission" {
		t.Fatalf("state = %q", state)
	}
	if !strings.HasSuffix(src, "a.log") {
		t.Errorf("source should point at the hit file, got %q", src)
	}
	if s, _ := lastRunStateInWithSource(home, ""); s != "" {
		t.Error("empty sessionID should yield empty state")
	}
}
