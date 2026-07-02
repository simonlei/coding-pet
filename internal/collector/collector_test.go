package collector

import (
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

// TestCollectOne_TagsWorkBuddyTool 验证同一套采集逻辑作用于 WorkBuddy home 时，
// session 会被打上 ToolWorkBuddy 标记（工具标签由 buddyHome 决定，而非写死 codebuddy）。
func TestCollectOne_TagsWorkBuddyTool(t *testing.T) {
	c := New()

	pf := PIDFile{
		PID:           999999, // 不存在的进程
		SessionID:     "test-workbuddy-session",
		CWD:           "/tmp/project",
		StartedAt:     time.Now().UnixMilli() - 120_000,
		LastHeartbeat: time.Now().UnixMilli() - 120_000,
		Kind:          "interactive",
		Version:       "2.106.4",
	}

	info, err := c.collectOne(buddyHome{dir: "/nonexistent", tool: protocol.ToolWorkBuddy}, pf)
	if err != nil {
		t.Fatalf("collectOne error: %v", err)
	}

	if info.Tool != protocol.ToolWorkBuddy {
		t.Errorf("expected Tool=%q, got %q", protocol.ToolWorkBuddy, info.Tool)
	}
	if info.State != protocol.StateTerminated {
		t.Errorf("expected State=%q (stale heartbeat), got %q", protocol.StateTerminated, info.State)
	}
}

// TestBuddyHomes_IncludesCodeBuddyAndWorkBuddy 验证扫描列表同时包含
// ~/.codebuddy（CodeBuddy CLI/IDE）与 ~/.workbuddy（WorkBuddy IDE），且标签正确。
func TestBuddyHomes_IncludesCodeBuddyAndWorkBuddy(t *testing.T) {
	homes := buddyHomes()
	byTool := map[protocol.SessionTool]string{}
	for _, h := range homes {
		byTool[h.tool] = h.dir
	}

	if dir, ok := byTool[protocol.ToolCodeBuddy]; !ok || filepath.Base(dir) != ".codebuddy" {
		t.Errorf("expected a codebuddy home ending in .codebuddy, got %q (present=%v)", dir, ok)
	}
	if dir, ok := byTool[protocol.ToolWorkBuddy]; !ok || filepath.Base(dir) != ".workbuddy" {
		t.Errorf("expected a workbuddy home ending in .workbuddy, got %q (present=%v)", dir, ok)
	}
}
