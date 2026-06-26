package protocol

import (
	"encoding/json"
	"testing"
)

// TestSessionInfo_ToolRoundTrip 验证 Tool 字段能正确序列化/反序列化
func TestSessionInfo_ToolRoundTrip(t *testing.T) {
	in := SessionInfo{
		SessionID: "abc",
		PID:       123,
		Tool:      ToolClaudeCode,
		State:     StateActive,
	}

	data, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// 确认 json key 为 "tool" 且值为 claude_code
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("unmarshal to map error: %v", err)
	}
	if raw["tool"] != "claude_code" {
		t.Errorf("expected tool=claude_code, got %v", raw["tool"])
	}

	var out SessionInfo
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out.Tool != ToolClaudeCode {
		t.Errorf("expected Tool=%q, got %q", ToolClaudeCode, out.Tool)
	}
}

// TestSessionInfo_ToolOmitted 验证缺失 tool 字段时反序列化为空（由 server 端规范化为 codebuddy）
func TestSessionInfo_ToolOmitted(t *testing.T) {
	var out SessionInfo
	if err := json.Unmarshal([]byte(`{"session_id":"x","state":"active"}`), &out); err != nil {
		t.Fatalf("unmarshal error: %v", err)
	}
	if out.Tool != "" {
		t.Errorf("expected empty Tool when omitted, got %q", out.Tool)
	}
}
