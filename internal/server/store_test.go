package server

import (
	"testing"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestUpdateMachine_MixedToolsAggregate 验证两类工具的 session 都被存储，且计数跨工具聚合
func TestUpdateMachine_MixedToolsAggregate(t *testing.T) {
	s := NewStore()
	s.UpdateMachine(protocol.AgentReport{
		MachineID: "m1",
		Hostname:  "host1",
		Sessions: []protocol.SessionInfo{
			{SessionID: "cb1", Tool: protocol.ToolCodeBuddy, State: protocol.StateActive},
			{SessionID: "cc1", Tool: protocol.ToolClaudeCode, State: protocol.StateWaitingForApproval},
			{SessionID: "cc2", Tool: protocol.ToolClaudeCode, State: protocol.StateWaitingForInput},
		},
	})

	d := s.GetDashboard()
	if d.TotalSessions != 3 {
		t.Errorf("expected 3 total sessions, got %d", d.TotalSessions)
	}
	if d.ActiveCount != 1 {
		t.Errorf("expected 1 active, got %d", d.ActiveCount)
	}
	if d.ApprovalCount != 1 {
		t.Errorf("expected 1 approval, got %d", d.ApprovalCount)
	}
	// waiting_for_input + waiting_for_approval 都计入 waiting 总数
	if d.WaitingCount != 2 {
		t.Errorf("expected 2 waiting (input+approval), got %d", d.WaitingCount)
	}
}

// TestUpdateMachine_NormalizesEmptyTool 验证缺失 tool 的 session 被规范化为 codebuddy
func TestUpdateMachine_NormalizesEmptyTool(t *testing.T) {
	s := NewStore()
	s.UpdateMachine(protocol.AgentReport{
		MachineID: "m1",
		Sessions: []protocol.SessionInfo{
			{SessionID: "legacy", State: protocol.StateActive}, // 无 Tool
		},
	})

	d := s.GetDashboard()
	if len(d.Machines) != 1 || len(d.Machines[0].Sessions) != 1 {
		t.Fatalf("expected 1 machine with 1 session")
	}
	if d.Machines[0].Sessions[0].Tool != protocol.ToolCodeBuddy {
		t.Errorf("expected empty Tool normalized to codebuddy, got %q", d.Machines[0].Sessions[0].Tool)
	}
}
