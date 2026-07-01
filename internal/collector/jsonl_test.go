package collector

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// writeJSONLFixture 写入一个 jsonl fixture 并把 mtime 设到 >5s 前，
// 以绕过 DetermineStateFromJSONL 的「5 秒内修改 → active」短路。
func writeJSONLFixture(t *testing.T, lines string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "session.jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("chtimes: %v", err)
	}
	return path
}

// TestDetermineState_DanglingFunctionCall_IsApproval 验证：JSONL 末尾是一个
// 悬空的 function_call（后面没有 function_call_result），说明工具尚未执行、
// 正在等待用户授权，应判定为 waiting_for_approval（而非 active）。
func TestDetermineState_DanglingFunctionCall_IsApproval(t *testing.T) {
	lines := `{"type":"message","role":"user"}
{"type":"reasoning"}
{"type":"function_call","name":"WebFetch","arguments":"{\"url\":\"https://example.com\"}"}
`
	path := writeJSONLFixture(t, lines)
	got := DetermineStateFromJSONL(path)
	if got != protocol.StateWaitingForApproval {
		t.Errorf("dangling function_call: expected %q, got %q", protocol.StateWaitingForApproval, got)
	}
}

// TestDetermineState_FunctionCallResult_IsActive 验证：末尾是 function_call_result
// 说明工具已返回、agent 继续执行中，应判定为 active。
func TestDetermineState_FunctionCallResult_IsActive(t *testing.T) {
	lines := `{"type":"function_call","name":"WebFetch","arguments":"{}"}
{"type":"function_call_result","name":"WebFetch","status":"completed"}
`
	path := writeJSONLFixture(t, lines)
	got := DetermineStateFromJSONL(path)
	if got != protocol.StateActive {
		t.Errorf("function_call_result: expected %q, got %q", protocol.StateActive, got)
	}
}

// TestDetermineState_AskUserQuestion_IsInput 验证：AskUserQuestion 仍判 waiting_for_input（浅黄），
// 不因悬空 function_call 逻辑被升级为 approval。
func TestDetermineState_AskUserQuestion_IsInput(t *testing.T) {
	lines := `{"type":"function_call","name":"AskUserQuestion","arguments":"{}"}
`
	path := writeJSONLFixture(t, lines)
	got := DetermineStateFromJSONL(path)
	if got != protocol.StateWaitingForInput {
		t.Errorf("AskUserQuestion: expected %q, got %q", protocol.StateWaitingForInput, got)
	}
}

// TestDetermineState_ExitPlanMode_IsInput 验证：ExitPlanMode（计划审批）判 waiting_for_input。
func TestDetermineState_ExitPlanMode_IsInput(t *testing.T) {
	lines := `{"type":"function_call","name":"ExitPlanMode","arguments":"{}"}
`
	path := writeJSONLFixture(t, lines)
	got := DetermineStateFromJSONL(path)
	if got != protocol.StateWaitingForInput {
		t.Errorf("ExitPlanMode: expected %q, got %q", protocol.StateWaitingForInput, got)
	}
}
