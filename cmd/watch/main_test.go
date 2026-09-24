package main

import (
	"strings"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

func sess(tool protocol.SessionTool, id string, state protocol.SessionState) protocol.SessionInfo {
	return protocol.SessionInfo{
		SessionID: id,
		Tool:      tool,
		State:     state,
		CWD:       "/tmp/proj",
		PID:       123,
	}
}

func TestApplyDiff_NewStateGone(t *testing.T) {
	state := map[string]*watchEntry{}
	t0 := time.Unix(1_700_000_000, 0)

	// 首次出现 → NEW
	changes := applyDiff(state, []protocol.SessionInfo{sess(protocol.ToolCodeBuddy, "abcdefgh1", protocol.StateActive)}, t0, false)
	if len(changes) != 1 || changes[0].Kind != "NEW" {
		t.Fatalf("want 1 NEW, got %+v", changes)
	}

	// 状态不变 → 无变化
	if got := applyDiff(state, []protocol.SessionInfo{sess(protocol.ToolCodeBuddy, "abcdefgh1", protocol.StateActive)}, t0.Add(time.Second), false); len(got) != 0 {
		t.Fatalf("want no change, got %+v", got)
	}

	// 状态跃迁 → STATE，并带上旧状态停留时长
	changes = applyDiff(state, []protocol.SessionInfo{sess(protocol.ToolCodeBuddy, "abcdefgh1", protocol.StateWaitingForInput)}, t0.Add(10*time.Second), false)
	if len(changes) != 1 || changes[0].Kind != "STATE" {
		t.Fatalf("want 1 STATE, got %+v", changes)
	}
	if changes[0].Prev != protocol.StateActive || changes[0].Held != 10*time.Second {
		t.Fatalf("prev/held wrong: %+v", changes[0])
	}

	// 消失 → GONE，并从 state 中清理
	changes = applyDiff(state, nil, t0.Add(15*time.Second), false)
	if len(changes) != 1 || changes[0].Kind != "GONE" {
		t.Fatalf("want 1 GONE, got %+v", changes)
	}
	if len(state) != 0 {
		t.Fatalf("state not purged: %+v", state)
	}
}

func TestApplyDiff_SameSessionIDDifferentTools(t *testing.T) {
	state := map[string]*watchEntry{}
	now := time.Unix(1_700_000_000, 0)
	in := []protocol.SessionInfo{
		sess(protocol.ToolCodeBuddy, "same-id", protocol.StateActive),
		sess(protocol.ToolClaudeCode, "same-id", protocol.StateWaitingForInput),
	}
	if got := applyDiff(state, in, now, false); len(got) != 2 {
		t.Fatalf("want 2 NEW, got %+v", got)
	}
	if len(state) != 2 {
		t.Fatalf("want 2 entries (tool-scoped key), got %d", len(state))
	}
}

func TestApplyDiff_FieldChangeOnlyWhenVerbose(t *testing.T) {
	state := map[string]*watchEntry{}
	now := time.Unix(1_700_000_000, 0)
	base := sess(protocol.ToolCodeBuddy, "abcdefgh1", protocol.StateActive)
	applyDiff(state, []protocol.SessionInfo{base}, now, true)

	grown := base
	grown.ContextTokens = 12_345

	quiet := map[string]*watchEntry{}
	applyDiff(quiet, []protocol.SessionInfo{base}, now, false)
	if got := applyDiff(quiet, []protocol.SessionInfo{grown}, now.Add(time.Second), false); len(got) != 0 {
		t.Fatalf("non-verbose should ignore field change, got %+v", got)
	}

	got := applyDiff(state, []protocol.SessionInfo{grown}, now.Add(time.Second), true)
	if len(got) != 1 || got[0].Kind != "FIELD" || !strings.Contains(got[0].Note, "ctx") {
		t.Fatalf("want FIELD ctx change, got %+v", got)
	}
}

func TestDupSessionIDs(t *testing.T) {
	out := dupSessionIDs([]protocol.SessionInfo{
		sess(protocol.ToolCodeBuddy, "dup-id-value", protocol.StateActive),
		sess(protocol.ToolClaudeCode, "dup-id-value", protocol.StateActive),
		sess(protocol.ToolWorkBuddy, "solo", protocol.StateActive),
	})
	if len(out) != 1 {
		t.Fatalf("want 1 warning, got %+v", out)
	}
	if !strings.Contains(out[0], "claude_code") || !strings.Contains(out[0], "codebuddy") {
		t.Fatalf("warning should name both tools: %s", out[0])
	}
}

func TestDupWorkspaceSessions(t *testing.T) {
	out := dupWorkspaceSessions([]protocol.SessionInfo{
		sess(protocol.ToolCodeBuddyIDE, "aaaaaaaaaa", protocol.StateActive),
		sess(protocol.ToolCodeBuddyIDE, "bbbbbbbbbb", protocol.StateWaitingForInput),
		sess(protocol.ToolWorkBuddy, "cccccccccc", protocol.StateActive),
	})
	if len(out) != 1 {
		t.Fatalf("want 1 warning, got %+v", out)
	}
	if !strings.Contains(out[0], "codebuddy_ide") || !strings.Contains(out[0], "/tmp/proj") {
		t.Fatalf("warning should name tool and workspace: %s", out[0])
	}
}

func TestDupWorkspaceSessions_NoneWhenDistinct(t *testing.T) {
	a := sess(protocol.ToolCodeBuddyIDE, "aaaaaaaaaa", protocol.StateActive)
	b := sess(protocol.ToolCodeBuddyIDE, "bbbbbbbbbb", protocol.StateActive)
	b.CWD = "/tmp/other"
	if out := dupWorkspaceSessions([]protocol.SessionInfo{a, b}); len(out) != 0 {
		t.Fatalf("want no warning, got %+v", out)
	}
}

func TestParseToolFilterAndFilterSessions(t *testing.T) {
	if parseToolFilter("  ") != nil {
		t.Fatal("blank filter should be nil (means all)")
	}
	f := parseToolFilter("codebuddy, claude_code")
	if !f["codebuddy"] || !f["claude_code"] || len(f) != 2 {
		t.Fatalf("bad filter: %+v", f)
	}
	in := []protocol.SessionInfo{
		sess(protocol.ToolCodeBuddy, "a", protocol.StateActive),
		sess(protocol.ToolWorkBuddy, "b", protocol.StateActive),
	}
	out := filterSessions(in, f)
	if len(out) != 1 || out[0].Tool != protocol.ToolCodeBuddy {
		t.Fatalf("filter failed: %+v", out)
	}
	if len(filterSessions(in, nil)) != 2 {
		t.Fatal("nil filter should keep all")
	}
}

func TestShortDurAndTokens(t *testing.T) {
	cases := []struct{ d, want string }{
		{shortDur(500 * time.Millisecond), "500ms"},
		{shortDur(90 * time.Second), "1m30s"},
		{shortDur(2*time.Hour + 5*time.Minute), "2h05m"},
	}
	for _, c := range cases {
		if c.d != c.want {
			t.Errorf("shortDur = %s, want %s", c.d, c.want)
		}
	}
	if tokens(0) != "-" || tokens(999) != "999" || tokens(12_345) != "12.3k" {
		t.Errorf("tokens formatting wrong: %s %s %s", tokens(0), tokens(999), tokens(12_345))
	}
}

func TestSummaryLine(t *testing.T) {
	got := summaryLine([]protocol.SessionInfo{
		sess(protocol.ToolCodeBuddy, "a", protocol.StateActive),
		sess(protocol.ToolCodeBuddy, "b", protocol.StateWaitingForApproval),
	})
	if !strings.Contains(got, "total=2") || !strings.Contains(got, "waiting_for_approval=1") {
		t.Fatalf("bad summary: %s", got)
	}
}

func TestSnapshotTableEmptyAndRows(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	if lines := snapshotTable(nil, now); len(lines) != 1 || !strings.Contains(lines[0], "无 session") {
		t.Fatalf("empty snapshot wrong: %+v", lines)
	}
	lines := snapshotTable([]protocol.SessionInfo{sess(protocol.ToolCodeBuddy, "abcdefgh1", protocol.StateActive)}, now)
	if len(lines) != 2 || !strings.Contains(lines[1], "abcdefgh") {
		t.Fatalf("snapshot rows wrong: %+v", lines)
	}
}
