package collector

import (
	"testing"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestReconcile_LogConfirmsPermission 日志状态机为 waiting_for_permission 时，
// 无论 JSONL 判成什么，都判定为 waiting_for_approval（精确命中）。
func TestReconcile_LogConfirmsPermission(t *testing.T) {
	got := reconcileWithRunState(protocol.StateActive, "waiting_for_permission")
	if got != protocol.StateWaitingForApproval {
		t.Errorf("expected waiting_for_approval, got %q", got)
	}
}

// TestReconcile_LogShowsRunning_FixesFalseApproval 日志显示实际在运行
// （tool_executing 等）时，纠正 JSONL 因悬空 function_call 产生的 approval 误判为 active。
func TestReconcile_LogShowsRunning_FixesFalseApproval(t *testing.T) {
	for _, running := range []string{"tool_executing", "model_streaming", "model_requesting", "model_done", "agent_running", "preparing", "pending"} {
		got := reconcileWithRunState(protocol.StateWaitingForApproval, running)
		if got != protocol.StateActive {
			t.Errorf("runState=%s: expected active, got %q", running, got)
		}
	}
}

// TestReconcile_NoLogInfo_KeepsJSONL 日志无信息（空串）时保留 JSONL 判定。
func TestReconcile_NoLogInfo_KeepsJSONL(t *testing.T) {
	if got := reconcileWithRunState(protocol.StateWaitingForApproval, ""); got != protocol.StateWaitingForApproval {
		t.Errorf("expected waiting_for_approval kept, got %q", got)
	}
	if got := reconcileWithRunState(protocol.StateActive, ""); got != protocol.StateActive {
		t.Errorf("expected active kept, got %q", got)
	}
}

// TestReconcile_DoesNotClobberWaitingForInput 运行中的日志状态不应把真正的
// waiting_for_input（AskUserQuestion/ExitPlanMode）覆盖掉——只纠正 approval 误判。
func TestReconcile_DoesNotClobberWaitingForInput(t *testing.T) {
	if got := reconcileWithRunState(protocol.StateWaitingForInput, "tool_executing"); got != protocol.StateWaitingForInput {
		t.Errorf("expected waiting_for_input kept, got %q", got)
	}
}
