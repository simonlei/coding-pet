package collector

import (
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestMapWorkBuddyState 表驱动覆盖 status × 新鲜度 的所有分支。
func TestMapWorkBuddyState(t *testing.T) {
	oneMin := time.Minute.Milliseconds()

	cases := []struct {
		name        string
		status      string
		ageMs       int64
		wantState   protocol.SessionState
		wantInclude bool
	}{
		{"working 刚活动 → active", "working", 30 * 1000, protocol.StateActive, true},
		{"working 临界2min59s → active", "working", 3*oneMin - 1000, protocol.StateActive, true},
		{"working 5分钟 → 等待(降级)", "working", 5 * oneMin, protocol.StateWaitingForInput, true},
		{"working 29分钟 → 等待", "working", 29 * oneMin, protocol.StateWaitingForInput, true},
		{"working 31分钟 → 剔除", "working", 31 * oneMin, "", false},
		{"completed 刚完成 → 等待(答完提醒)", "completed", 10 * 1000, protocol.StateWaitingForInput, true},
		{"completed 大写 Completed → 等待", "Completed", 10 * 1000, protocol.StateWaitingForInput, true},
		{"completed 20分钟 → 等待", "completed", 20 * oneMin, protocol.StateWaitingForInput, true},
		{"completed 31分钟 → 剔除", "completed", 31 * oneMin, "", false},
		{"未知status 窗口内 → 等待", "paused", 1 * oneMin, protocol.StateWaitingForInput, true},
		{"未知status 超时 → 剔除", "paused", 40 * oneMin, "", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotState, gotInclude := mapWorkBuddyState(tc.status, tc.ageMs)
			if gotInclude != tc.wantInclude {
				t.Fatalf("include = %v, want %v", gotInclude, tc.wantInclude)
			}
			if gotInclude && gotState != tc.wantState {
				t.Errorf("state = %q, want %q", gotState, tc.wantState)
			}
		})
	}
}

// TestLowerASCII 验证仅 ASCII 大写被转换。
func TestLowerASCII(t *testing.T) {
	for in, want := range map[string]string{
		"Completed": "completed",
		"WORKING":   "working",
		"working":   "working",
		"":          "",
	} {
		if got := lowerASCII(in); got != want {
			t.Errorf("lowerASCII(%q) = %q, want %q", in, got, want)
		}
	}
}
