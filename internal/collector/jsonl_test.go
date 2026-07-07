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

// TestContextTokens_ClaudeFormat 验证：Claude Code 格式的 message.usage
// 当前上下文占用 = input_tokens + cache_read_input_tokens + cache_creation_input_tokens。
func TestContextTokens_ClaudeFormat(t *testing.T) {
	lines := `{"type":"user","message":{"role":"user"}}
{"type":"assistant","message":{"role":"assistant","usage":{"input_tokens":2,"cache_creation_input_tokens":269,"cache_read_input_tokens":198182,"output_tokens":917}}}
`
	path := writeJSONLFixture(t, lines)
	got := ContextTokensFromJSONL(path)
	want := int64(2 + 269 + 198182)
	if got != want {
		t.Errorf("claude usage: expected %d, got %d", want, got)
	}
}

// TestContextTokens_CodeBuddyFormat 验证：CodeBuddy CLI 格式的 message.usage
// 无 cache 字段，input_tokens 本身即上下文占用。
func TestContextTokens_CodeBuddyFormat(t *testing.T) {
	lines := `{"type":"function_call","message":{"usage":{"input_tokens":105393,"output_tokens":2162,"total_tokens":107555}}}
`
	path := writeJSONLFixture(t, lines)
	got := ContextTokensFromJSONL(path)
	want := int64(105393)
	if got != want {
		t.Errorf("codebuddy usage: expected %d, got %d", want, got)
	}
}

// TestContextTokens_ScansBackwardForLastUsage 验证：反向扫描，取最后一条含 usage
// 的记录（末尾若干条不含 usage 应被跳过）。
func TestContextTokens_ScansBackwardForLastUsage(t *testing.T) {
	lines := `{"type":"assistant","message":{"usage":{"input_tokens":100,"cache_read_input_tokens":50}}}
{"type":"assistant","message":{"usage":{"input_tokens":300,"cache_read_input_tokens":200}}}
{"type":"function_call","name":"Bash","arguments":"{}"}
{"type":"reasoning"}
`
	path := writeJSONLFixture(t, lines)
	got := ContextTokensFromJSONL(path)
	want := int64(300 + 200)
	if got != want {
		t.Errorf("scan backward: expected %d, got %d", want, got)
	}
}

// TestContextTokens_NoUsage_ReturnsZero 验证：整个文件无 usage 时优雅降级返回 0。
func TestContextTokens_NoUsage_ReturnsZero(t *testing.T) {
	lines := `{"type":"message","role":"user"}
{"type":"function_call","name":"Bash","arguments":"{}"}
`
	path := writeJSONLFixture(t, lines)
	got := ContextTokensFromJSONL(path)
	if got != 0 {
		t.Errorf("no usage: expected 0, got %d", got)
	}
}

// TestContextTokens_MissingFile_ReturnsZero 验证：文件不存在时返回 0，不 panic。
func TestContextTokens_MissingFile_ReturnsZero(t *testing.T) {
	got := ContextTokensFromJSONL("/nonexistent/path/session.jsonl")
	if got != 0 {
		t.Errorf("missing file: expected 0, got %d", got)
	}
}
