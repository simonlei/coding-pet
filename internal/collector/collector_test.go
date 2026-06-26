package collector

import (
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

	info, err := c.collectOne(pf)
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
