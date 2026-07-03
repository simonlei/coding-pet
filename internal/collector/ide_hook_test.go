package collector

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

func TestIDEHookStore_StateMachine(t *testing.T) {
	tests := []struct {
		name   string
		events []IDEHookEvent
		want   protocol.SessionState
	}{
		{
			name: "SessionStart -> active",
			events: []IDEHookEvent{
				{HookEventName: HookSessionStart, SessionID: "s1", Source: "startup"},
			},
			want: protocol.StateActive,
		},
		{
			name: "UserPromptSubmit -> active",
			events: []IDEHookEvent{
				{HookEventName: HookSessionStart, SessionID: "s1"},
				{HookEventName: HookUserPromptSubmit, SessionID: "s1", Prompt: "hi"},
			},
			want: protocol.StateActive,
		},
		{
			name: "PreToolUse -> active (approval not modeled in this version)",
			events: []IDEHookEvent{
				{HookEventName: HookUserPromptSubmit, SessionID: "s1"},
				{HookEventName: HookPreToolUse, SessionID: "s1", ToolName: "Bash"},
			},
			want: protocol.StateActive,
		},
		{
			name: "PostToolUse -> active",
			events: []IDEHookEvent{
				{HookEventName: HookPreToolUse, SessionID: "s1", ToolName: "Bash"},
				{HookEventName: HookPostToolUse, SessionID: "s1", ToolName: "Bash"},
			},
			want: protocol.StateActive,
		},
		{
			name: "Stop -> waiting_for_input",
			events: []IDEHookEvent{
				{HookEventName: HookPostToolUse, SessionID: "s1", ToolName: "Bash"},
				{HookEventName: HookStop, SessionID: "s1"},
			},
			want: protocol.StateWaitingForInput,
		},
		{
			name: "PreCompact keeps prev state",
			events: []IDEHookEvent{
				{HookEventName: HookUserPromptSubmit, SessionID: "s1"},
				{HookEventName: HookStop, SessionID: "s1"},
				{HookEventName: HookPreCompact, SessionID: "s1", Trigger: "auto"},
			},
			want: protocol.StateWaitingForInput,
		},
		{
			name: "unknown event without prior state defaults active",
			events: []IDEHookEvent{
				{HookEventName: "SomethingWeird", SessionID: "s1"},
			},
			want: protocol.StateActive,
		},
		{
			name: "full loop: prompt -> tools -> stop",
			events: []IDEHookEvent{
				{HookEventName: HookSessionStart, SessionID: "s1"},
				{HookEventName: HookUserPromptSubmit, SessionID: "s1"},
				{HookEventName: HookPreToolUse, SessionID: "s1", ToolName: "Bash"},
				{HookEventName: HookPostToolUse, SessionID: "s1", ToolName: "Bash"},
				{HookEventName: HookStop, SessionID: "s1"},
			},
			want: protocol.StateWaitingForInput,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := NewIDEHookStore()
			for _, ev := range tt.events {
				s.Apply(ev)
			}
			snap := s.Snapshot()
			if len(snap) != 1 {
				t.Fatalf("expected 1 session, got %d", len(snap))
			}
			if snap[0].State != tt.want {
				t.Errorf("state = %s, want %s", snap[0].State, tt.want)
			}
			if snap[0].Tool != protocol.ToolCodeBuddyIDE {
				t.Errorf("tool = %s, want %s", snap[0].Tool, protocol.ToolCodeBuddyIDE)
			}
		})
	}
}

func TestIDEHookStore_SessionEndRemovesSession(t *testing.T) {
	s := NewIDEHookStore()
	s.Apply(IDEHookEvent{HookEventName: HookSessionStart, SessionID: "sE"})
	if got := len(s.Snapshot()); got != 1 {
		t.Fatalf("want 1, got %d", got)
	}
	s.Apply(IDEHookEvent{HookEventName: HookSessionEnd, SessionID: "sE", Reason: "other"})
	if got := len(s.Snapshot()); got != 0 {
		t.Errorf("want 0 after SessionEnd, got %d", got)
	}
}

func TestIDEHookStore_Metadata(t *testing.T) {
	s := NewIDEHookStore()
	s.Apply(IDEHookEvent{
		HookEventName: HookUserPromptSubmit, SessionID: "sX",
		CWD: "d:/work/x", Tool: "workbuddy", Version: "1.2.3",
	})
	// 后续心跳事件不应把 tool/cwd/version 抹为空。
	s.Apply(IDEHookEvent{HookEventName: HookPreToolUse, SessionID: "sX", ToolName: "Bash"})

	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 session, got %d", len(snap))
	}
	got := snap[0]
	if got.Tool != protocol.ToolWorkBuddy {
		t.Errorf("tool=%s want %s", got.Tool, protocol.ToolWorkBuddy)
	}
	if got.CWD != "d:/work/x" {
		t.Errorf("cwd=%s", got.CWD)
	}
	if got.Version != "1.2.3" {
		t.Errorf("version=%s", got.Version)
	}
	if got.Kind != protocol.KindInteractive {
		t.Errorf("kind=%s", got.Kind)
	}
}

func TestIDEHookStore_StaleCleanup(t *testing.T) {
	s := NewIDEHookStore()
	// 手工写一个上一次上报时间在 40 分钟之前的会话
	oldMs := time.Now().Add(-40 * time.Minute).UnixMilli()
	s.sessions["dead"] = &ideSession{
		SessionID: "dead", Tool: protocol.ToolCodeBuddyIDE,
		LastActivity: oldMs, State: protocol.StateActive, StartedAt: oldMs,
	}
	// 一个新鲜会话
	s.Apply(IDEHookEvent{HookEventName: HookUserPromptSubmit, SessionID: "alive"})

	snap := s.Snapshot()
	if len(snap) != 1 {
		t.Fatalf("want 1 (only alive), got %d", len(snap))
	}
	if snap[0].SessionID != "alive" {
		t.Errorf("expected alive, got %s", snap[0].SessionID)
	}
	if _, ok := s.sessions["dead"]; ok {
		t.Errorf("dead session should have been cleaned")
	}
}

func TestIDEHookStore_IgnoreEmptySessionID(t *testing.T) {
	s := NewIDEHookStore()
	s.Apply(IDEHookEvent{HookEventName: HookUserPromptSubmit, SessionID: ""})
	if got := len(s.Snapshot()); got != 0 {
		t.Errorf("expected 0, got %d", got)
	}
}

func TestIDEHookServer_EndToEnd(t *testing.T) {
	// 隔离全局单例，避免测试间污染。
	origSessions := globalIDEStore.sessions
	globalIDEStore.sessions = map[string]*ideSession{}
	t.Cleanup(func() { globalIDEStore.sessions = origSessions })

	addr, err := StartIDEHookServer("127.0.0.1:0")
	if err != nil {
		t.Fatalf("start server: %v", err)
	}

	// healthz
	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("healthz: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz status %d", resp.StatusCode)
	}

	// POST 一次 Stop 事件
	body, _ := json.Marshal(IDEHookEvent{
		HookEventName: HookStop, SessionID: "http1", CWD: "d:/proj",
	})
	resp, err = http.Post("http://"+addr+"/ide/hook", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post hook: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("hook status=%d body=%s", resp.StatusCode, got)
	}

	// GET /ide/sessions 应能读到刚才注入的 session
	resp, err = http.Get("http://" + addr + "/ide/sessions")
	if err != nil {
		t.Fatalf("get sessions: %v", err)
	}
	var out []protocol.SessionInfo
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	resp.Body.Close()
	if len(out) != 1 {
		t.Fatalf("want 1 session, got %d", len(out))
	}
	if out[0].State != protocol.StateWaitingForInput {
		t.Errorf("state=%s want waiting_for_input", out[0].State)
	}

	// 缺 hook_event_name 字段 -> 400
	resp, err = http.Post("http://"+addr+"/ide/hook", "application/json",
		bytes.NewReader([]byte(`{"session_id":"x"}`)))
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// GET /ide/hook -> 405
	resp, err = http.Get("http://" + addr + "/ide/hook")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", resp.StatusCode)
	}
}

// 确保 Listen 端口占用时 StartIDEHookServer 返回错误但不 panic。
func TestIDEHookServer_PortInUse(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	addr := ln.Addr().String()
	if _, err := StartIDEHookServer(addr); err == nil {
		t.Errorf("expected error when port occupied, got nil")
	}
}
