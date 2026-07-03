package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestMapCodeBuddyIDEState 表驱动覆盖 最后request.state × 新鲜度 的所有分支。
func TestMapCodeBuddyIDEState(t *testing.T) {
	oneMin := time.Minute.Milliseconds()

	cases := []struct {
		name        string
		lastState   string
		ageMs       int64
		wantState   protocol.SessionState
		wantInclude bool
	}{
		{"running 刚写盘 → active", "running", 10 * 1000, protocol.StateActive, true},
		{"running 临界2min59s → active", "running", 3*oneMin - 1000, protocol.StateActive, true},
		{"running 5分钟 → 等待(停滞降级)", "running", 5 * oneMin, protocol.StateWaitingForInput, true},
		{"running 29分钟 → 等待", "running", 29 * oneMin, protocol.StateWaitingForInput, true},
		{"running 31分钟 → 剔除", "running", 31 * oneMin, "", false},
		{"complete 刚完成 → 等待(答完提醒)", "complete", 10 * 1000, protocol.StateWaitingForInput, true},
		{"complete 20分钟 → 等待", "complete", 20 * oneMin, protocol.StateWaitingForInput, true},
		{"complete 31分钟 → 剔除", "complete", 31 * oneMin, "", false},
		{"大小写混用 Running → active", "Running", 10 * 1000, protocol.StateActive, true},
		{"未知state 窗口内 → 等待", "paused", 1 * oneMin, protocol.StateWaitingForInput, true},
		{"未知state 超时 → 剔除", "paused", 40 * oneMin, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotState, gotInclude := mapCodeBuddyIDEState(tc.lastState, tc.ageMs)
			if gotInclude != tc.wantInclude {
				t.Fatalf("include = %v, want %v", gotInclude, tc.wantInclude)
			}
			if gotInclude && gotState != tc.wantState {
				t.Errorf("state = %q, want %q", gotState, tc.wantState)
			}
		})
	}
}

// TestParseWorkspaceFolder 覆盖真实 message 正文（转义 \n）与普通换行两种情形。
func TestParseWorkspaceFolder(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "转义换行的信封文本",
			body: `<user_info>\nOS Version: win32\nWorkspace Folder: d:/work/coding-pet\nNote: prefer absolute\n</user_info>`,
			want: "d:/work/coding-pet",
		},
		{
			name: "真实换行",
			body: "OS Version: darwin\nWorkspace Folder: /Users/simon/proj\nShell: zsh",
			want: "/Users/simon/proj",
		},
		{
			name: "无标记",
			body: "no workspace info here",
			want: "",
		},
		{
			name: "行尾空白被裁剪",
			body: `Workspace Folder:   d:/work/x  \nrest`,
			want: "d:/work/x",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseWorkspaceFolder(tc.body); got != tc.want {
				t.Errorf("parseWorkspaceFolder() = %q, want %q", got, tc.want)
			}
		})
	}
}

// writeJSON 是测试辅助：把 v 写成 JSON 文件（含父目录）。
func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeConversation 在 wsDir 下构造一个会话（index.json + 首条 user message），
// 并把会话 index.json 的 mtime 设成 now-ageMs，返回会话 id。
func makeConversation(t *testing.T, wsDir, convID string, requestStates []string, workspaceFolder string, ageMs int64) {
	t.Helper()
	convDir := filepath.Join(wsDir, convID)

	type msgRef struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	type reqRef struct {
		State string `json:"state"`
	}
	idx := struct {
		Messages []msgRef `json:"messages"`
		Requests []reqRef `json:"requests"`
	}{}
	// 首条 user 消息，携带 Workspace Folder 供 CWD 回溯。
	if workspaceFolder != "" {
		idx.Messages = append(idx.Messages, msgRef{ID: "m1", Role: "user"})
		body := "<user_info>\\nWorkspace Folder: " + workspaceFolder + "\\n</user_info>"
		writeJSON(t, filepath.Join(convDir, "messages", "m1.json"),
			map[string]string{"role": "user", "message": body})
	}
	for _, s := range requestStates {
		idx.Requests = append(idx.Requests, reqRef{State: s})
	}

	convIndexPath := filepath.Join(convDir, "index.json")
	writeJSON(t, convIndexPath, idx)

	mt := time.Now().Add(-time.Duration(ageMs) * time.Millisecond)
	if err := os.Chtimes(convIndexPath, mt, mt); err != nil {
		t.Fatal(err)
	}
}

// TestCollectCodeBuddyIDEWorkspace 端到端覆盖单 workspace 采集的关键分支。
func TestCollectCodeBuddyIDEWorkspace(t *testing.T) {
	nowMs := time.Now().UnixMilli()

	t.Run("current=running且新鲜 → active，且末态覆盖中间僵尸running", func(t *testing.T) {
		wsDir := t.TempDir()
		// 中间残留 running（僵尸），最后一个仍是 running。
		makeConversation(t, wsDir, "conv-a", []string{"complete", "running", "running"}, "d:/work/coding-pet", 10*1000)
		writeJSON(t, filepath.Join(wsDir, "index.json"), map[string]string{"current": "conv-a"})

		info, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs)
		if !ok {
			t.Fatal("expected a session, got none")
		}
		if info.State != protocol.StateActive {
			t.Errorf("state = %q, want active", info.State)
		}
		if info.Tool != protocol.ToolCodeBuddyIDE {
			t.Errorf("tool = %q, want codebuddy_ide", info.Tool)
		}
		if info.SessionID != "conv-a" {
			t.Errorf("session id = %q, want conv-a", info.SessionID)
		}
		if info.CWD != "d:/work/coding-pet" {
			t.Errorf("cwd = %q, want d:/work/coding-pet", info.CWD)
		}
	})

	t.Run("末态complete → waiting_for_input", func(t *testing.T) {
		wsDir := t.TempDir()
		makeConversation(t, wsDir, "conv-b", []string{"running", "complete"}, "/p", 10*1000)
		writeJSON(t, filepath.Join(wsDir, "index.json"), map[string]string{"current": "conv-b"})

		info, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs)
		if !ok {
			t.Fatal("expected a session")
		}
		if info.State != protocol.StateWaitingForInput {
			t.Errorf("state = %q, want waiting_for_input", info.State)
		}
	})

	t.Run("超30分钟 → 不上报", func(t *testing.T) {
		wsDir := t.TempDir()
		makeConversation(t, wsDir, "conv-c", []string{"complete"}, "/p", 31*time.Minute.Milliseconds())
		writeJSON(t, filepath.Join(wsDir, "index.json"), map[string]string{"current": "conv-c"})

		if _, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs); ok {
			t.Error("expected stale session to be excluded")
		}
	})

	t.Run("空会话(0 requests) → 不上报", func(t *testing.T) {
		wsDir := t.TempDir()
		makeConversation(t, wsDir, "conv-d", nil, "/p", 10*1000)
		writeJSON(t, filepath.Join(wsDir, "index.json"), map[string]string{"current": "conv-d"})

		if _, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs); ok {
			t.Error("expected empty conversation to be excluded")
		}
	})

	t.Run("无 current → 不上报", func(t *testing.T) {
		wsDir := t.TempDir()
		makeConversation(t, wsDir, "conv-e", []string{"complete"}, "/p", 10*1000)
		writeJSON(t, filepath.Join(wsDir, "index.json"), map[string]string{"current": ""})

		if _, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs); ok {
			t.Error("expected no session when current is empty")
		}
	})

	t.Run("current 指向不存在的会话 → 不上报", func(t *testing.T) {
		wsDir := t.TempDir()
		writeJSON(t, filepath.Join(wsDir, "index.json"), map[string]string{"current": "ghost"})

		if _, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs); ok {
			t.Error("expected no session when current points to missing conversation")
		}
	})
}
