package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestCollectOne_TagsCodeBuddyTool 验证采集出的 session 带 ToolCodeBuddy 标记，
// 且状态判定逻辑（心跳超时 → terminated）不受新增字段影响。
func TestCollectOne_TagsCodeBuddyTool(t *testing.T) {
	c := New()

	// LastHeartbeat 远早于现在（>60s）→ 判定为 terminated，无需 JSONL fixture
	pf := PIDFile{
		PID:           999999, // 不存在的进程
		SessionID:     "test-session-id-not-present",
		CWD:           "/tmp/project",
		StartedAt:     time.Now().UnixMilli() - 120_000,
		LastHeartbeat: time.Now().UnixMilli() - 120_000,
		Kind:          "interactive",
		Version:       "1.0.0",
	}

	info, err := c.collectOne(buddyHome{dir: "/nonexistent", tool: protocol.ToolCodeBuddy}, pf)
	if err != nil {
		t.Fatalf("collectOne error: %v", err)
	}

	if info.Tool != protocol.ToolCodeBuddy {
		t.Errorf("expected Tool=%q, got %q", protocol.ToolCodeBuddy, info.Tool)
	}
	if info.State != protocol.StateTerminated {
		t.Errorf("expected State=%q (stale heartbeat), got %q", protocol.StateTerminated, info.State)
	}
	if info.SessionID != pf.SessionID {
		t.Errorf("expected SessionID=%q, got %q", pf.SessionID, info.SessionID)
	}
}

// TestBuddyHomes_OnlyCodeBuddyCLI 验证 PID 文件磁盘扫描列表只含 ~/.codebuddy
// （CodeBuddy CLI）。CodeBuddy IDE 的实时状态由 Hook 上报；WorkBuddy 桌面版则
// 改由 SQLite 采集（见 workbuddy_db.go），二者都不走 buddyHomes() 的 PID 路径。
func TestBuddyHomes_OnlyCodeBuddyCLI(t *testing.T) {
	homes := buddyHomes()
	byTool := map[protocol.SessionTool]string{}
	for _, h := range homes {
		byTool[h.tool] = h.dir
	}

	if dir, ok := byTool[protocol.ToolCodeBuddy]; !ok || filepath.Base(dir) != ".codebuddy" {
		t.Errorf("expected a codebuddy home ending in .codebuddy, got %q (present=%v)", dir, ok)
	}
	if _, ok := byTool[protocol.ToolWorkBuddy]; ok {
		t.Errorf("did not expect a workbuddy home to be scanned (now Hook-reported)")
	}
}

// TestCollectOne_FreshSessionNoJSONLIsActive 验证 CodeBuddy CLI 新启动会话：
// 进程存活、心跳新鲜、但 JSONL 还没落盘时应视为 active，不再报 unknown。
// 与 Claude Code fresh idle 的处理对齐——用户刚打开还没提问，不需要被前端提醒。
func TestCollectOne_FreshSessionNoJSONLIsActive(t *testing.T) {
	c := New()
	now := time.Now().UnixMilli()

	pf := PIDFile{
		PID:           os.Getpid(),
		SessionID:     "sess-fresh-cli-no-jsonl",
		CWD:           "/tmp/proj",
		StartedAt:     now - 2_000,
		LastHeartbeat: now,
		Kind:          "interactive",
		Version:       "1.0.0",
	}

	info, err := c.collectOne(buddyHome{dir: t.TempDir(), tool: protocol.ToolCodeBuddy}, pf)
	if err != nil {
		t.Fatalf("collectOne error: %v", err)
	}
	if info.State != protocol.StateActive {
		t.Errorf("expected State=active (fresh, no JSONL), got %q", info.State)
	}
}

