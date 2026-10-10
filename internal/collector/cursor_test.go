package collector

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

func TestCursorProjectSlug(t *testing.T) {
	if got := cursorProjectSlug("/data/home/simonlei/crhub"); got != "data-home-simonlei-crhub" {
		t.Fatalf("slug = %q", got)
	}
	if got := cursorProjectSlug(`C:\Users\me\proj`); got != "C-Users-me-proj" {
		t.Fatalf("windows slug = %q", got)
	}
}

func TestParseCursorTimestamp(t *testing.T) {
	ms, ok := parseCursorTimestamp("Saturday, Oct 10, 2026, 10:17 AM (UTC+8)")
	if !ok {
		t.Fatal("expected timestamp to parse")
	}
	got := time.UnixMilli(ms).UTC()
	if got.Year() != 2026 || got.Month() != time.October || got.Day() != 10 || got.Hour() != 2 || got.Minute() != 17 {
		t.Fatalf("parsed %s, want 2026-10-10 02:17 UTC", got)
	}
}

func TestCollectCursor_RemoteWaitingAndActive(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	server := filepath.Join(root, "server")
	ws := "/data/home/simonlei/crhub"
	slug := cursorProjectSlug(ws)

	ended := filepath.Join(projects, slug)
	writeCursorTranscript(t, ended, "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", []string{
		`{"role":"user","message":{"content":[{"type":"text","text":"<timestamp>Saturday, Oct 10, 2026, 10:17 AM (UTC+8)</timestamp>\nhi"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"text","text":"done"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
	}, now.Add(-2*time.Minute))

	writeCursorServerLog(t, server, ws)

	// 同一 workspace 里还有一条开着的轮次，应该压过已结束的那条。
	writeCursorTranscript(t, ended, "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", []string{
		`{"role":"user","message":{"content":[{"type":"text","text":"go"}]}}`,
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{}}]}}`,
	}, now.Add(-20*time.Second))

	got := collectCursorFrom(cursorRoots{projectsDir: projects, serverDir: server}, now.UnixMilli())
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want 1: %+v", len(got), got)
	}
	s := got[0]
	if s.Tool != protocol.ToolCursorRemote {
		t.Fatalf("tool = %s, want cursor_remote", s.Tool)
	}
	if s.CWD != ws {
		t.Fatalf("cwd = %s, want %s", s.CWD, ws)
	}
	if s.State != protocol.StateActive {
		t.Fatalf("state = %s, want active", s.State)
	}
	if s.SessionID != "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb" {
		t.Fatalf("kept %s, want the open turn", s.SessionID)
	}
	if s.StartedAt == 0 {
		t.Fatal("started_at should be set")
	}
}

func TestCollectCursor_ApprovalWhenShellHasNoTerminal(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	slug := "data-home-simonlei-app"
	writeCursorTranscript(t, filepath.Join(projects, slug), "cccccccc-cccc-cccc-cccc-cccccccccccc", []string{
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"rm -rf /"}}]}}`,
	}, now.Add(-10*time.Second))

	got := collectCursorFrom(cursorRoots{projectsDir: projects}, now.UnixMilli())
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want 1", len(got))
	}
	if got[0].Tool != protocol.ToolCursor {
		t.Fatalf("tool = %s, want cursor (no cursor-server)", got[0].Tool)
	}
	if got[0].State != protocol.StateWaitingForApproval {
		t.Fatalf("state = %s, want waiting_for_approval", got[0].State)
	}
}

func TestCollectCursor_ShellWithFreshTerminalStaysActive(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	proj := filepath.Join(projects, "data-home-simonlei-app")
	transcriptAt := now.Add(-10 * time.Second)
	writeCursorTranscript(t, proj, "dddddddd-dddd-dddd-dddd-dddddddddddd", []string{
		`{"role":"assistant","message":{"content":[{"type":"tool_use","name":"Shell","input":{"command":"make"}}]}}`,
	}, transcriptAt)
	term := filepath.Join(proj, "terminals", "1.txt")
	if err := os.MkdirAll(filepath.Dir(term), 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\npid: 1\ncwd: /data/home/simonlei/app\nlast_command: make\n---\nbuilding\n"
	if err := os.WriteFile(term, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	termAt := transcriptAt.Add(time.Second)
	if err := os.Chtimes(term, termAt, termAt); err != nil {
		t.Fatal(err)
	}

	got := collectCursorFrom(cursorRoots{projectsDir: projects}, now.UnixMilli())
	if len(got) != 1 || got[0].State != protocol.StateActive {
		t.Fatalf("got %+v, want one active session", got)
	}
}

func TestCollectCursor_SubagentKeepsParentActive(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	id := "eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"
	proj := filepath.Join(projects, "local-proj")
	writeCursorTranscript(t, proj, id, []string{
		`{"role":"assistant","message":{"content":[{"type":"text","text":"parent done"}]}}`,
		`{"type":"turn_ended","status":"success"}`,
	}, now.Add(-2*time.Minute))
	subDir := filepath.Join(proj, "agent-transcripts", id, "subagents")
	if err := os.MkdirAll(subDir, 0o755); err != nil {
		t.Fatal(err)
	}
	subPath := filepath.Join(subDir, "ffffffff-ffff-ffff-ffff-ffffffffffff.jsonl")
	subAt := now.Add(-5 * time.Second)
	if err := os.WriteFile(subPath, []byte(`{"role":"assistant","message":{"content":[{"type":"text","text":"still working"}]}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(subPath, subAt, subAt); err != nil {
		t.Fatal(err)
	}

	got := collectCursorFrom(cursorRoots{projectsDir: projects}, now.UnixMilli())
	if len(got) != 1 {
		t.Fatalf("sessions = %d, want 1 (subagent must not be its own card)", len(got))
	}
	if got[0].SessionID != id || got[0].State != protocol.StateActive {
		t.Fatalf("got id=%s state=%s, want parent active", got[0].SessionID, got[0].State)
	}
}

func TestCollectCursor_StaleDropped(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	writeCursorTranscript(t, filepath.Join(projects, "old"), "99999999-9999-9999-9999-999999999999", []string{
		`{"type":"turn_ended","status":"success"}`,
	}, now.Add(-31*time.Minute))

	got := collectCursorFrom(cursorRoots{projectsDir: projects}, now.UnixMilli())
	if len(got) != 0 {
		t.Fatalf("stale session leaked: %+v", got)
	}
}

func TestCollectCursor_MixedLocalAndRemote(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	server := filepath.Join(root, "server")
	dbPath := filepath.Join(root, "state.vscdb")

	remoteWS := "/data/home/simonlei/crhub"
	writeCursorTranscript(t, filepath.Join(projects, cursorProjectSlug(remoteWS)), "12121212-1212-1212-1212-121212121212", []string{
		`{"type":"turn_ended","status":"error","error":"composer_abort_controller_aborted"}`,
	}, now.Add(-time.Minute))
	writeCursorServerLog(t, server, remoteWS)

	writeCursorTranscript(t, filepath.Join(projects, "my-local-app"), "34343434-3434-3434-3434-343434343434", []string{
		`{"type":"turn_ended","status":"success"}`,
	}, now.Add(-time.Minute))

	writeCursorDB(t, dbPath, now, []cursorDBRow{
		{
			id:     "34343434-3434-3434-3434-343434343434", // 与 transcript 重复，应被丢掉
			status: "generating",
			cwd:    "/Users/me/my-local-app",
			scheme: "file",
		},
		{
			id:     "56565656-5656-5656-5656-565656565656",
			status: "generating",
			cwd:    "/Users/me/other",
			scheme: "file",
		},
		{
			id:     "78787878-7878-7878-7878-787878787878",
			status: "generating",
			cwd:    "/data/home/simonlei/crhub",
			scheme: "vscode-remote",
		},
		{
			id:       "90909090-9090-9090-9090-909090909090",
			status:   "none",
			cwd:      "/Users/me/archived",
			scheme:   "file",
			archived: true,
		},
	})

	got := collectCursorFrom(cursorRoots{
		projectsDir: projects,
		serverDir:   server,
		localDB:     dbPath,
	}, now.UnixMilli())

	byID := map[string]protocol.SessionInfo{}
	for _, s := range got {
		byID[s.SessionID] = s
	}
	if byID["12121212-1212-1212-1212-121212121212"].Tool != protocol.ToolCursorRemote {
		t.Fatalf("remote transcript tool = %s", byID["12121212-1212-1212-1212-121212121212"].Tool)
	}
	if byID["12121212-1212-1212-1212-121212121212"].State != protocol.StateWaitingForInput {
		t.Fatalf("aborted turn should be waiting_for_input, got %s", byID["12121212-1212-1212-1212-121212121212"].State)
	}
	if byID["34343434-3434-3434-3434-343434343434"].Tool != protocol.ToolCursor {
		t.Fatalf("unmatched project on a machine with both installs should be cursor, got %s", byID["34343434-3434-3434-3434-343434343434"].Tool)
	}
	local, ok := byID["56565656-5656-5656-5656-565656565656"]
	if !ok || local.Tool != protocol.ToolCursor || local.State != protocol.StateActive || local.CWD != "/Users/me/other" {
		t.Fatalf("db composer = %+v", local)
	}
	if _, ok := byID["78787878-7878-7878-7878-787878787878"]; ok {
		t.Fatal("vscode-remote composer should be skipped")
	}
	if _, ok := byID["90909090-9090-9090-9090-909090909090"]; ok {
		t.Fatal("archived composer should be skipped")
	}
}

func TestCollectCursor_CWDFromTerminalWhenLogMissing(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 10, 10, 10, 40, 0, 0, time.UTC)
	projects := filepath.Join(root, "projects")
	slug := cursorProjectSlug("/data/home/simonlei/coding-pet")
	proj := filepath.Join(projects, slug)
	writeCursorTranscript(t, proj, "abababab-abab-abab-abab-abababababab", []string{
		`{"type":"turn_ended","status":"success"}`,
	}, now.Add(-time.Minute))
	termDir := filepath.Join(proj, "terminals")
	if err := os.MkdirAll(termDir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := "---\npid: 9\ncwd: /data/home/simonlei/coding-pet/internal\nlast_command: go test\nlast_exit_code: 0\n---\n"
	if err := os.WriteFile(filepath.Join(termDir, "1.txt"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	// 有 cursor-server 但日志里没有这个 workspace，且没有桌面版库：
	// 纯 Remote 主机上的会话仍标 cursor_remote，cwd 从终端文件往上对 slug。
	server := filepath.Join(root, "empty-server")
	if err := os.MkdirAll(server, 0o755); err != nil {
		t.Fatal(err)
	}
	got := collectCursorFrom(cursorRoots{projectsDir: projects, serverDir: server}, now.UnixMilli())
	if len(got) != 1 {
		t.Fatalf("sessions = %+v", got)
	}
	if got[0].Tool != protocol.ToolCursorRemote {
		t.Fatalf("tool = %s", got[0].Tool)
	}
	if got[0].CWD != "/data/home/simonlei/coding-pet" {
		t.Fatalf("cwd = %s, want workspace root recovered from terminal cwd", got[0].CWD)
	}
}

type cursorDBRow struct {
	id       string
	status   string
	cwd      string
	scheme   string
	archived bool
}

func writeCursorDB(t *testing.T, path string, now time.Time, rows []cursorDBRow) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE ItemTable (key TEXT UNIQUE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE cursorDiskKV (key TEXT UNIQUE, value BLOB)`); err != nil {
		t.Fatal(err)
	}
	headers := make([]cursorComposerHeader, 0, len(rows))
	updated := now.Add(-time.Minute).UnixMilli()
	for _, row := range rows {
		h := cursorComposerHeader{
			ComposerID:    row.id,
			CreatedAt:     updated - 60_000,
			LastUpdatedAt: updated,
			IsArchived:    row.archived,
			WorkspaceIdentifier: &cursorWorkspaceIdent{URI: &cursorURI{
				Scheme: row.scheme,
				FsPath: row.cwd,
				Path:   row.cwd,
			}},
		}
		if row.scheme == "vscode-remote" {
			h.WorkspaceIdentifier.URI.External = "vscode-remote://ssh-remote+host" + row.cwd
			h.WorkspaceIdentifier.URI.FsPath = ""
		}
		headers = append(headers, h)
		body := cursorComposerBody{
			ComposerID:          row.id,
			Status:              row.status,
			CreatedAt:           h.CreatedAt,
			LastUpdatedAt:       updated,
			IsArchived:          row.archived,
			WorkspaceIdentifier: h.WorkspaceIdentifier,
		}
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO cursorDiskKV (key, value) VALUES (?, ?)`, "composerData:"+row.id, string(raw)); err != nil {
			t.Fatal(err)
		}
	}
	wrapped, err := json.Marshal(map[string]any{"allComposers": headers})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO ItemTable (key, value) VALUES (?, ?)`, "composer.composerData", string(wrapped)); err != nil {
		t.Fatal(err)
	}
}

func writeCursorTranscript(t *testing.T, projectDir, id string, lines []string, mtime time.Time) {
	t.Helper()
	dir := filepath.Join(projectDir, "agent-transcripts", id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, line := range lines {
		body += line + "\n"
	}
	path := filepath.Join(dir, id+".jsonl")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func writeCursorServerLog(t *testing.T, serverDir, workspace string) {
	t.Helper()
	dir := filepath.Join(serverDir, "data", "logs", "20261010T101030", "exthost1", "anysphere.cursor-agent-exec")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := `2026-10-10 10:15:17.734 [info] cursor_agent_exec.startup.workspace_paths {"workspacePathCount":1,"workspacePaths":["` + workspace + `"]}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "Cursor Agent Exec.log"), []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
}
