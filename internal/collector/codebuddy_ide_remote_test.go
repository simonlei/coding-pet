package collector

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// TestDecodeWorkspacePath 覆盖 URL-safe base64（用 '_' 尾部 padding）解码，包含真实样例。
func TestDecodeWorkspacePath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "真实样例 crhub 双 _ padding",
			in:   "L2RhdGEvaG9tZS9zaW1vbmxlaS9jcmh1Yg__",
			want: "/data/home/simonlei/crhub",
		},
		{
			name: "真实样例 coding-pet 单 _ padding",
			in:   "L2RhdGEvaG9tZS9zaW1vbmxlaS9jb2RpbmctcGV0",
			want: "/data/home/simonlei/coding-pet",
		},
		{
			name: "URL-safe 字符 '-'（tosr-master）",
			in:   "L2RhdGEvaG9tZS9zaW1vbmxlaS90b3NyLW1hc3Rlcg__",
			want: "/data/home/simonlei/tosr-master",
		},
		{
			name: "无效 base64 → 空",
			in:   "not!base64!!",
			want: "",
		},
		{
			name: "解码成功但不像路径 → 空（防止误报）",
			in:   "aGVsbG8", // "hello"
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeWorkspacePath(tc.in); got != tc.want {
				t.Errorf("decodeWorkspacePath(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestParseLogLineTimestamp 覆盖行首带毫秒 / 不带毫秒 / 非日志行。
func TestParseLogLineTimestamp(t *testing.T) {
	// 参考锚点：2026-07-23 16:32:06.113
	loc := time.Local
	ref := time.Date(2026, 7, 23, 16, 32, 6, 113_000_000, loc).UnixMilli()
	refNoMs := time.Date(2026, 7, 23, 16, 32, 6, 0, loc).UnixMilli()

	cases := []struct {
		name    string
		in      string
		wantMs  int64
		wantOK  bool
	}{
		{
			name:   "带毫秒的 info 行",
			in:     "2026-07-23 16:32:06.113 [info] [BaseAgent:craft] run start",
			wantMs: ref,
			wantOK: true,
		},
		{
			name:   "不带毫秒",
			in:     "2026-07-23 16:32:06 [info] xxx",
			wantMs: refNoMs,
			wantOK: true,
		},
		{
			name:   "太短",
			in:     "2026-07-23",
			wantOK: false,
		},
		{
			name:   "非时间戳前缀",
			in:     "some noise line without timestamp",
			wantOK: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := parseLogLineTimestamp([]byte(tc.in))
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if ok && got != tc.wantMs {
				t.Errorf("ms = %d, want %d", got, tc.wantMs)
			}
		})
	}
}

// TestExtractHex32Tokens 校验：conversationId（32 位纯 hex）能识别，tool call id / trace id 不误报。
func TestExtractHex32Tokens(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "AcpAgent 前缀含 conversationId",
			in:   "[AcpAgent:6b278c9a78394489a649f77ee134a142] agent created",
			want: []string{"6b278c9a78394489a649f77ee134a142"},
		},
		{
			name: "createConversation 行",
			in:   "createConversation: 350b839fa62243139636827ee8e3000c, date: xxx",
			want: []string{"350b839fa62243139636827ee8e3000c"},
		},
		{
			name: "tool_call_id 含下划线不匹配 32 hex",
			in:   "[ToolManager] 创建工具实例: read_file (call_05_v4Qw3AWn2SYkicMfIGyT1405)",
			want: nil,
		},
		{
			name: "trace uuid 带 dash 不匹配",
			in:   "traceId: e17671b8-8eb4-452e-a507-239fdd08756a",
			want: nil,
		},
		{
			name: "同一行两个 convId",
			in:   "6b278c9a78394489a649f77ee134a142 vs 350b839fa62243139636827ee8e3000c",
			want: []string{"6b278c9a78394489a649f77ee134a142", "350b839fa62243139636827ee8e3000c"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractHex32Tokens([]byte(tc.in))
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}

// TestScanLogTail 单次扫描：识别 [BaseAgent:craft] run start/end 作为整轮边沿；
// subagent 的 run 与 tool 层事件均忽略；conv id 单独走 lastSeen 表。
func TestScanLogTail(t *testing.T) {
	convA := "6b278c9a78394489a649f77ee134a142"
	convB := "350b839fa62243139636827ee8e3000c"
	log := "" +
		"2026-07-23 16:31:37.000 [info] createConversation: " + convA + "\n" +
		"2026-07-23 16:32:06.113 [info] [BaseAgent:craft] run start\n" +
		"2026-07-23 16:32:06.500 [info] [AcpAgent:" + convA + "] tool execution\n" +
		// subagent run end：应被忽略
		"2026-07-23 16:32:20.000 [info] [BaseAgent:code-explorer] run end\n" +
		// tool 层：应被忽略
		"2026-07-23 16:32:25.000 [info] [ToolManager] 执行结束: call_abc\n" +
		"2026-07-23 16:32:25.000 [info] [ToolManager] [waitForAllToolsExecuted] All tools execution completed\n" +
		// 主 agent 真正的 run end
		"2026-07-23 16:32:34.301 [info] [BaseAgent:craft] run end\n" +
		"2026-07-23 16:33:00.000 [info] [AcpMessageRouter:" + convB + "] some event\n"

	rsMs, reMs, seen := scanLogTail([]byte(log))

	wantRS := time.Date(2026, 7, 23, 16, 32, 6, 113_000_000, time.Local).UnixMilli()
	wantRE := time.Date(2026, 7, 23, 16, 32, 34, 301_000_000, time.Local).UnixMilli()
	if rsMs != wantRS {
		t.Errorf("latestRunStartMs = %d, want %d", rsMs, wantRS)
	}
	if reMs != wantRE {
		t.Errorf("latestRunEndMs = %d, want %d (只应认 [BaseAgent:craft] run end)", reMs, wantRE)
	}
	if len(seen) != 2 {
		t.Fatalf("convLastSeen has %d entries, want 2", len(seen))
	}
	wantASeen := time.Date(2026, 7, 23, 16, 32, 6, 500_000_000, time.Local).UnixMilli()
	if seen[convA] != wantASeen {
		t.Errorf("seen[A] = %d, want %d", seen[convA], wantASeen)
	}
	wantBSeen := time.Date(2026, 7, 23, 16, 33, 0, 0, time.Local).UnixMilli()
	if seen[convB] != wantBSeen {
		t.Errorf("seen[B] = %d, want %d", seen[convB], wantBSeen)
	}
}

// TestScanLogTail_IgnoresToolAndSubagent 明确校验:tool 层和 subagent run 不影响 run start/end。
func TestScanLogTail_IgnoresToolAndSubagent(t *testing.T) {
	log := "" +
		"2026-07-24 09:15:27.225 [info] [ToolManager] 开始执行: call_00_xICpNoY - execute_command\n" +
		"2026-07-24 09:15:32.312 [info] [ToolManager] 执行结束: call_00_xICpNoY - execute_command\n" +
		"2026-07-24 09:15:32.312 [info] [ToolManager] [waitForAllToolsExecuted] All tools execution completed\n" +
		"2026-07-24 09:15:34.000 [info] [BaseAgent:code-explorer] run start\n" +
		"2026-07-24 09:15:34.500 [info] [BaseAgent:code-explorer] run end\n" +
		"2026-07-24 09:15:39.885 [info] [ToolManager] 开始执行: call_00_8XOSkRr - write_to_file - stream\n"

	rsMs, reMs, _ := scanLogTail([]byte(log))
	if rsMs != 0 || reMs != 0 {
		t.Errorf("start=%d end=%d, want both 0 (无 [BaseAgent:craft] run 事件)", rsMs, reMs)
	}
}

// TestScanLogTail_SkipsChatInputDraft 用户在输入框打字时 [ConfigService] 每按一键都写一条
// chatInputDraft,含 conv id 但 agent 完全空闲 —— convLastSeen 不应被这类行刷新。
func TestScanLogTail_SkipsChatInputDraft(t *testing.T) {
	conv := "6b278c9a78394489a649f77ee134a142"
	log := "" +
		"2026-07-24 09:30:00.000 [info] [AcpAgent:" + conv + "] real agent activity\n" +
		"2026-07-24 09:30:05.000 [info] [BaseAgent:craft] run end\n" +
		// 5 分钟后用户开始打字 —— 大量 chatInputDraft
		"2026-07-24 09:35:00.000 [info] [ConfigService] update config: key=chatInputDraft:" + conv + ", value=[{\"text\":\"h\"}]\n" +
		"2026-07-24 09:35:01.000 [info] [ConfigService] update config: key=chatInputDraft:" + conv + ", value=[{\"text\":\"he\"}]\n" +
		"2026-07-24 09:35:02.000 [info] [ConfigService] update config: key=chatInputDraft:" + conv + ", value=[{\"text\":\"hel\"}]\n"

	_, reMs, seen := scanLogTail([]byte(log))

	wantEnd := time.Date(2026, 7, 24, 9, 30, 5, 0, time.Local).UnixMilli()
	wantSeen := time.Date(2026, 7, 24, 9, 30, 0, 0, time.Local).UnixMilli()
	if reMs != wantEnd {
		t.Errorf("latestRunEndMs = %d, want %d", reMs, wantEnd)
	}
	if seen[conv] != wantSeen {
		t.Errorf("seen[conv] = %d, want %d (chatInputDraft 应被忽略,lastSeen 停在真正的 agent 活动)",
			seen[conv], wantSeen)
	}
}

// TestMapCodeBuddyIDERemoteState 表驱动覆盖所有决策分支。
func TestMapCodeBuddyIDERemoteState(t *testing.T) {
	now := int64(1_784_795_000_000) // 任意锚点，Unix ms
	oneMin := time.Minute.Milliseconds()

	cases := []struct {
		name    string
		ev      convEventState
		currMs  int64 // current.json mtime
		want    protocol.SessionState
		include bool
	}{
		{
			name:    "log tail 有 run start 且 > run end + tied → active(正在跑)",
			ev:      convEventState{lastSeenMs: now - 30_000, latestRunStartMs: now - 30_000, latestRunEndMs: now - 5*oneMin},
			want:    protocol.StateActive,
			include: true,
		},
		{
			name:    "run end > run start + tied → waiting_for_input(一轮已完成)",
			ev:      convEventState{lastSeenMs: now - 30_000, latestRunStartMs: now - 2*oneMin, latestRunEndMs: now - 30_000},
			want:    protocol.StateWaitingForInput,
			include: true,
		},
		{
			name:    "只有 run end 无 run start + tied → waiting_for_input",
			ev:      convEventState{lastSeenMs: now - 30_000, latestRunStartMs: 0, latestRunEndMs: now - 30_000},
			want:    protocol.StateWaitingForInput,
			include: true,
		},
		{
			name:    "run end 之后 cleanup 事件刷新 lastSeen 但仍在 60s 内 → tied → waiting_for_input",
			ev:      convEventState{lastSeenMs: now - 10_000, latestRunStartMs: now - 60_000, latestRunEndMs: now - 60_000},
			want:    protocol.StateWaitingForInput,
			include: true,
		},
		{
			name:    "非归属最新 run(距离 > 60s)但 conv 自身 <3min → waiting_for_input(其他 workspace 在跑)",
			ev:      convEventState{lastSeenMs: now - 2*oneMin, latestRunStartMs: now - 10_000, latestRunEndMs: 0},
			want:    protocol.StateWaitingForInput,
			include: true,
		},
		{
			name:    "log tail 无 run 事件但 conv 3min 内有活动 → active(保守,run 早于 tail)",
			ev:      convEventState{lastSeenMs: now - 30_000, latestRunStartMs: 0, latestRunEndMs: 0},
			want:    protocol.StateActive,
			include: true,
		},
		{
			name:    "log 未出现 + current.json 新鲜(1min) → active(新建未产生事件)",
			ev:      convEventState{lastSeenMs: 0},
			currMs:  now - 1*oneMin,
			want:    protocol.StateActive,
			include: true,
		},
		{
			name:    "log 未出现 + current.json 5min 前 → waiting_for_input",
			ev:      convEventState{lastSeenMs: 0},
			currMs:  now - 5*oneMin,
			want:    protocol.StateWaitingForInput,
			include: true,
		},
		{
			name:    "open run 长静默(lastSeen 超 3min 但 run start>run end) → active(保活优先)",
			ev:      convEventState{lastSeenMs: now - 10*oneMin, latestRunStartMs: now - 10*oneMin},
			want:    protocol.StateActive,
			include: true,
		},
		{
			name:    "open run 但非归属者(lastSeen 距 run start > 60s)且超 3min → waiting_for_input",
			ev:      convEventState{lastSeenMs: now - 10*oneMin, latestRunStartMs: now - 30_000, latestRunEndMs: 0},
			want:    protocol.StateWaitingForInput,
			include: true,
		},
		{
			name:    "age ≥ 30min → 剔除",
			ev:      convEventState{lastSeenMs: now - 31*oneMin},
			include: false,
		},
		{
			name:    "无任何时间戳 → 剔除",
			ev:      convEventState{},
			currMs:  0,
			include: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, inc := mapCodeBuddyIDERemoteState(tc.ev, tc.currMs, now)
			if inc != tc.include {
				t.Fatalf("include = %v, want %v", inc, tc.include)
			}
			if inc && got != tc.want {
				t.Errorf("state = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestCollectCodeBuddyIDERemote_OpenRunActive 复现用户观察的场景:
// log 尾部只有 tool 层事件和 subagent run end,没有 [BaseAgent:craft] run end
// (主 run 的 start 在 tail 之外,还没结束) → 应识别为 active。
func TestCollectCodeBuddyIDERemote_OpenRunActive(t *testing.T) {
	root := t.TempDir()
	conv := "9adbf70b8c264edc8484256a49c20820"

	// current.json 指针
	genieBase := filepath.Join(root, "data", "User", "globalStorage",
		"tencent-cloud.coding-copilot", "genie-history")
	wsDir := filepath.Join(genieBase, "L2RhdGEvY3JodWI_") // "/data/crhub"
	if err := os.MkdirAll(wsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wsDir, "current.json"),
		[]byte(`{"conversationId":"`+conv+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	logDir := filepath.Join(root, "data", "logs", "20260722T151156",
		"exthost3", "Tencent-Cloud.coding-copilot")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, chatLogBaseName)

	now := time.Now()
	fmtTs := func(off time.Duration) string {
		return now.Add(off).Format("2006-01-02 15:04:05.000")
	}
	// 模拟真实场景:tail 里全是 subagent + tool 层事件,没有主 agent run end。
	// conv id 最后一次出现在 30s 前,主 agent run 早已开始但未结束。
	logContent := "" +
		fmtTs(-60*time.Second) + " [info] [BaseAgent:code-explorer] run end\n" +
		fmtTs(-45*time.Second) + " [info] [ToolManager] 开始执行: call_prev - execute_command\n" +
		fmtTs(-40*time.Second) + " [info] [AcpAgent:" + conv + "] tool executing\n" +
		fmtTs(-38*time.Second) + " [info] [ToolManager] 执行结束: call_prev\n" +
		fmtTs(-38*time.Second) + " [info] [ToolManager] [waitForAllToolsExecuted] All tools execution completed\n" +
		fmtTs(-30*time.Second) + " [info] [ChatService] onConversationUpdated: conversationId=" + conv + ", fields=lastMessageAt\n" +
		fmtTs(-20*time.Second) + " [info] [ToolManager] 开始执行: call_new - write_to_file - stream\n"

	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	sessions := collectCodeBuddyIDERemoteFromRoot(root, now.UnixMilli())
	if len(sessions) != 1 {
		t.Fatalf("got %d sessions, want 1 (dump: %+v)", len(sessions), sessions)
	}
	if s := sessions[0]; s.State != protocol.StateActive {
		t.Errorf("state = %q, want active (主 agent run 未结束)", s.State)
	}
}

// TestCollectCodeBuddyIDERemote_Integration 用 t.TempDir 构造假的 codebuddy-server-cn 目录，
// 端到端跑一次 collectCodeBuddyIDERemoteFromRoot，验证多 workspace 上报 + 状态推断正确。
//
// findActiveExthostLog 在测试环境下走不到 /proc 分支（pgrep 匹配不到），会走 glob fallback。
func TestCollectCodeBuddyIDERemote_Integration(t *testing.T) {
	root := t.TempDir()

	// --- 1. 构造两个 workspace 指针 ---
	convActive := "aaaa1111bbbb2222cccc3333dddd4444"   // 有 run 在跑
	convDone := "eeee5555ffff66667777888899990000"     // 一轮已完成
	convNoLog := "1234567890abcdef1234567890abcdef"    // 新建但 log 里没事件

	genieBase := filepath.Join(root, "data", "User", "globalStorage",
		"tencent-cloud.coding-copilot", "genie-history")
	writeWS := func(b64Name, convID string) {
		dir := filepath.Join(genieBase, b64Name)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "current.json"),
			[]byte(fmt.Sprintf(`{"conversationId":"%s"}`, convID)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// L2RhdGEvY3JodWI = "/data/crhub"
	writeWS("L2RhdGEvY3JodWI_", convActive)
	// L2RhdGEvY29kaW5n = "/data/coding"
	writeWS("L2RhdGEvY29kaW5n", convDone)
	// L2RhdGEvbmV3 = "/data/new"
	writeWS("L2RhdGEvbmV3", convNoLog)

	// --- 2. 构造 log ---
	// convActive: run start 后无 run end → active
	// convDone:   run start → run end 齐全 → waiting_for_input
	logDir := filepath.Join(root, "data", "logs", "20260722T151156",
		"exthost3", "Tencent-Cloud.coding-copilot")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(logDir, chatLogBaseName)

	now := time.Now()
	fmtTs := func(offset time.Duration) string {
		return now.Add(offset).Format("2006-01-02 15:04:05.000")
	}
	logContent := "" +
		fmtTs(-3*time.Minute) + " [info] createConversation: " + convDone + "\n" +
		fmtTs(-3*time.Minute+time.Second) + " [info] [BaseAgent:craft] run start\n" +
		fmtTs(-3*time.Minute+2*time.Second) + " [info] [AcpAgent:" + convDone + "] event\n" +
		fmtTs(-2*time.Minute) + " [info] [BaseAgent:craft] run end\n" +
		fmtTs(-30*time.Second) + " [info] createConversation: " + convActive + "\n" +
		fmtTs(-15*time.Second) + " [info] [BaseAgent:craft] run start\n" +
		fmtTs(-14*time.Second) + " [info] [AcpAgent:" + convActive + "] tool executing\n"
	if err := os.WriteFile(logPath, []byte(logContent), 0o644); err != nil {
		t.Fatal(err)
	}

	// --- 3. 跑采集 ---
	nowMs := now.UnixMilli()
	sessions := collectCodeBuddyIDERemoteFromRoot(root, nowMs)

	if len(sessions) != 3 {
		t.Fatalf("got %d sessions, want 3 (dump: %+v)", len(sessions), sessions)
	}

	byID := map[string]protocol.SessionInfo{}
	for _, s := range sessions {
		byID[s.SessionID] = s
	}

	if s := byID[convActive]; s.State != protocol.StateActive {
		t.Errorf("convActive state = %q, want active (sess=%+v)", s.State, s)
	}
	if s := byID[convDone]; s.State != protocol.StateWaitingForInput {
		t.Errorf("convDone state = %q, want waiting_for_input (sess=%+v)", s.State, s)
	}
	if s := byID[convNoLog]; s.State != protocol.StateActive {
		// 刚创建、log 里没事件、current.json age < 3min → active
		t.Errorf("convNoLog state = %q, want active (sess=%+v)", s.State, s)
	}

	// 所有 session 都应带对的 Tool 与 Kind
	for _, s := range sessions {
		if s.Tool != protocol.ToolCodeBuddyIDERemote {
			t.Errorf("session %s Tool = %q, want codebuddy_ide_remote", s.SessionID, s.Tool)
		}
		if s.Kind != protocol.KindInteractive {
			t.Errorf("session %s Kind = %q, want interactive", s.SessionID, s.Kind)
		}
		if s.LastActivity == 0 {
			t.Errorf("session %s LastActivity = 0", s.SessionID)
		}
	}

	// convActive 的 CWD 应能解码到 /data/crhub
	if s := byID[convActive]; s.CWD != "/data/crhub" {
		t.Errorf("convActive CWD = %q, want /data/crhub", s.CWD)
	}
}

// TestCollectCodeBuddyIDERemote_StaleWorkspaceExcluded 30min 无写入的 workspace 应被剔除。
func TestCollectCodeBuddyIDERemote_StaleWorkspaceExcluded(t *testing.T) {
	root := t.TempDir()
	genieBase := filepath.Join(root, "data", "User", "globalStorage",
		"tencent-cloud.coding-copilot", "genie-history")
	dir := filepath.Join(genieBase, "L2RhdGEvY3JodWI_")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	convID := "aaaa1111bbbb2222cccc3333dddd4444"
	if err := os.WriteFile(filepath.Join(dir, "current.json"),
		[]byte(`{"conversationId":"`+convID+`"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 把 current.json 的 mtime 拨到 1 小时前
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "current.json"), old, old); err != nil {
		t.Fatal(err)
	}

	sessions := collectCodeBuddyIDERemoteFromRoot(root, time.Now().UnixMilli())
	if len(sessions) != 0 {
		t.Fatalf("expected 0 sessions (stale), got %d: %+v", len(sessions), sessions)
	}
}

// TestCollectCodeBuddyIDERemote_NoServerRoot 服务器目录不存在时应静默返回 nil。
func TestCollectCodeBuddyIDERemote_NoServerRoot(t *testing.T) {
	nonExistent := filepath.Join(t.TempDir(), "does-not-exist")
	sessions := collectCodeBuddyIDERemoteFromRoot(nonExistent, time.Now().UnixMilli())
	if sessions != nil {
		t.Errorf("want nil, got %+v", sessions)
	}
}
