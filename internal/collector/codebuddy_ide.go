// Package collector 中 codebuddy_ide.go 采集 CodeBuddy IDE 的会话状态。
//
// CodeBuddy IDE 不写 ~/.codebuddy 那套 PID 文件 + JSONL，而是把每个 workspace 的
// 会话历史落在扩展数据目录下的 history/ 里：
//
//	<extRoot>/Data/<安装GUID>/CodeBuddyIDE/<安装GUID>/history/
//	  <workspaceHash>/
//	    index.json                     —— 该 workspace 的会话列表 + current（当前会话 id）
//	    <conversationId>/
//	      index.json                   —— messages[] 索引 + requests[]（含 state）
//	      messages/<msgId>.json        —— 单条消息正文（外层 {role,message}，message 为 JSON 字符串）
//
// 平台根目录（<extRoot>）：
//   - Windows：%LOCALAPPDATA%\CodeBuddyExtension
//   - macOS：  ~/Library/Application Support/CodeBuddyExtension
//
// 会话状态取该会话 index.json 里 requests[] 最后一个的 state（running / complete）。
// 注意中间轮次可能残留 state=running（回合被中断留下的僵尸），只有最后一个才代表现状。
//
// 磁盘上没有 PID、没有心跳、也无法感知 IDE 是否已关闭，因此判活只能靠 index.json 的
// mtime 做新鲜度衰减，语义与 WorkBuddy 桌面版（workbuddy_db.go）保持一致：
//   - running   且 age < 3min          → active（顶部绿色活跃）
//   - running   且 3min ≤ age < 30min  → waiting_for_input（停滞/僵尸，掉出高亮）
//   - complete  且 age < 30min          → waiting_for_input（一轮答完等用户下条指令）
//   - 任意       且 age ≥ 30min          → 不上报（视为 IDE 已关闭）
//
// 前端 HOOK_ONLY_TOOLS = { codebuddy_ide, workbuddy } 已有 3 分钟降级逻辑，采集端只要
// 打上 Tool=codebuddy_ide 并给对 state / last_activity，即自动复用「黄闪 3min → 淡黄半小时」展示。
//
// 当前上下文占用 token 取自会话 index.json 里最后一个带 usage 的 request 的
// usage.lastTokens（随轮次单调增长的窗口占用）；无 usage 时降级为 0，前端不展示。
package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

const (
	// cbIDEActiveWindow：running 会话最近一次写盘在此窗口内 → 仍算「活跃」。
	cbIDEActiveWindow = 3 * time.Minute
	// cbIDEStaleWindow：任何会话超过此窗口无写盘 → 视为已终止，不再上报。
	cbIDEStaleWindow = 30 * time.Minute
)

// cbIDEWorkspaceIndex 对应 history/<workspaceHash>/index.json。
// 只关心 current（IDE 当前打开可见的会话 id）。
type cbIDEWorkspaceIndex struct {
	Current string `json:"current"`
}

// cbIDEConversationIndex 对应 history/<workspaceHash>/<convId>/index.json。
// messages 用于回溯 CWD，requests 的末态用于判定会话状态。
type cbIDEConversationIndex struct {
	Messages []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"messages"`
	Requests []struct {
		State string `json:"state"` // running / complete
		// usage.lastTokens 是该轮结束时的上下文窗口占用（随轮次单调增长），
		// 即「当前上下文占用」的语义；inputTokens 是该轮所有子调用的累计输入
		// （可达百万），不适合作占用展示。running 中的轮次 usage 缺失（为 nil）。
		Usage *struct {
			LastTokens int64 `json:"lastTokens"`
		} `json:"usage"`
	} `json:"requests"`
}

// cbIDEMessageFile 对应 messages/<msgId>.json，外层信封。
// message 字段本身是一段 JSON 字符串（{role,content:[...]}）。
type cbIDEMessageFile struct {
	Message string `json:"message"`
}

// codebuddyIDEHistoryRoots 返回本机所有 CodeBuddy IDE history 根目录。
//
// 安装 GUID 不固定（一台机器可能有多个账号/安装），且不同 profile 的层级略有差异：
//   - GUID profile： <extRoot>/Data/<GUID>/CodeBuddyIDE/<GUID>/history
//   - default profile：<extRoot>/Data/default/CodeBuddyIDE/history（少一层 GUID）
//
// 故用两个 glob 模式分别展开、去重。目录不存在时返回空，上层自然跳过。
func codebuddyIDEHistoryRoots() []string {
	extRoot, ok := codebuddyExtensionRoot()
	if !ok {
		return nil
	}
	patterns := []string{
		filepath.Join(extRoot, "Data", "*", "CodeBuddyIDE", "*", "history"),
		filepath.Join(extRoot, "Data", "*", "CodeBuddyIDE", "history"),
	}
	seen := make(map[string]bool)
	var roots []string
	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, m := range matches {
			if seen[m] {
				continue
			}
			seen[m] = true
			roots = append(roots, m)
		}
	}
	return roots
}

// codebuddyExtensionRoot 返回 CodeBuddyExtension 数据根目录及其是否存在。
func codebuddyExtensionRoot() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	var root string
	switch runtime.GOOS {
	case "windows":
		// %LOCALAPPDATA%，回退 ~/AppData/Local。
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		root = filepath.Join(local, "CodeBuddyExtension")
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support", "CodeBuddyExtension")
	default:
		return "", false // 其它平台暂不支持 CodeBuddy IDE
	}
	if _, err := os.Stat(root); err != nil {
		return "", false
	}
	return root, true
}

// CollectCodeBuddyIDESessions 扫描所有 history 根目录，返回活跃/等待中的 IDE 会话。
// 每个 workspace 只看 current 指向的会话（IDE 当前可见的那个）。
// 任一环节出错都安静跳过，不阻塞其他采集源。
func CollectCodeBuddyIDESessions() []protocol.SessionInfo {
	nowMs := time.Now().UnixMilli()
	var out []protocol.SessionInfo
	seen := make(map[string]bool) // 同一 conversationId 跨 root 去重

	for _, root := range codebuddyIDEHistoryRoots() {
		entries, err := os.ReadDir(root)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			wsDir := filepath.Join(root, e.Name())
			if info, ok := collectCodeBuddyIDEWorkspace(wsDir, nowMs); ok {
				if seen[info.SessionID] {
					continue
				}
				seen[info.SessionID] = true
				out = append(out, info)
			}
		}
	}
	return out
}

// collectCodeBuddyIDEWorkspace 处理单个 workspace 目录，返回其 current 会话的 SessionInfo。
// 第二个返回值为 false 表示无可上报会话（无 current / 解析失败 / 已过期）。
func collectCodeBuddyIDEWorkspace(wsDir string, nowMs int64) (protocol.SessionInfo, bool) {
	var wsIdx cbIDEWorkspaceIndex
	if !readJSONFile(filepath.Join(wsDir, "index.json"), &wsIdx) || wsIdx.Current == "" {
		return protocol.SessionInfo{}, false
	}

	convDir := filepath.Join(wsDir, wsIdx.Current)
	convIndexPath := filepath.Join(convDir, "index.json")

	// 新鲜度：先 stat 判 age，过期的直接跳过，省下解析历史会话的开销。
	fi, err := os.Stat(convIndexPath)
	if err != nil {
		return protocol.SessionInfo{}, false
	}
	lastActivity := fi.ModTime().UnixMilli()
	ageMs := nowMs - lastActivity
	if ageMs >= cbIDEStaleWindow.Milliseconds() {
		return protocol.SessionInfo{}, false
	}

	var convIdx cbIDEConversationIndex
	if !readJSONFile(convIndexPath, &convIdx) {
		return protocol.SessionInfo{}, false
	}
	if len(convIdx.Requests) == 0 {
		// 空会话（IDE 刚新建、还没发消息）：不算活跃，不上报。
		return protocol.SessionInfo{}, false
	}

	lastState := convIdx.Requests[len(convIdx.Requests)-1].State
	state, include := mapCodeBuddyIDEState(lastState, ageMs)
	if !include {
		return protocol.SessionInfo{}, false
	}

	cwd := codebuddyIDEWorkspaceFolder(convDir, convIdx)

	return protocol.SessionInfo{
		SessionID:     wsIdx.Current,
		PID:           0, // IDE 侧无独立 per-session PID
		Kind:          protocol.KindInteractive,
		Tool:          protocol.ToolCodeBuddyIDE,
		CWD:           cwd,
		StartedAt:     lastActivity, // 无独立起始时间，用最后活动近似
		LastHeartbeat: lastActivity,
		State:         state,
		LastActivity:  lastActivity,
		ContextTokens: codebuddyIDEContextTokens(convIdx),
	}, true
}

// codebuddyIDEContextTokens 返回当前上下文占用 token：从后向前找第一个带 usage 的
// request，取其 usage.lastTokens。最后一轮可能是 running（usage 为 nil），故需回溯。
// 无任何 usage 时返回 0（优雅降级，前端不展示）。
func codebuddyIDEContextTokens(convIdx cbIDEConversationIndex) int64 {
	for i := len(convIdx.Requests) - 1; i >= 0; i-- {
		if u := convIdx.Requests[i].Usage; u != nil && u.LastTokens > 0 {
			return u.LastTokens
		}
	}
	return 0
}

// mapCodeBuddyIDEState 把「最后 request 的 state + 距最后写盘的毫秒数」映射为 SessionState。
// 第二个返回值为 false 表示该会话应剔除（已过期）。
func mapCodeBuddyIDEState(lastState string, ageMs int64) (protocol.SessionState, bool) {
	if ageMs >= cbIDEStaleWindow.Milliseconds() {
		return "", false
	}
	switch lowerASCII(strings.TrimSpace(lastState)) {
	case "running":
		if ageMs < cbIDEActiveWindow.Milliseconds() {
			return protocol.StateActive, true
		}
		// 3~30 分钟没有再写盘：多半 IDE 已关或回合被中断，掉出高亮，淡黄列表保留。
		return protocol.StateWaitingForInput, true
	case "complete":
		// 一轮答完、等用户下条指令。
		return protocol.StateWaitingForInput, true
	default:
		// 未知 state（协议扩展）：窗口内保守显示为等待，不丢失可见性。
		return protocol.StateWaitingForInput, true
	}
}

// codebuddyIDEWorkspaceFolder 从会话首条 user 消息里回溯 workspace 路径。
//
// 首条 user 消息正文含 `<user_info> ... Workspace Folder: <path> ...`，
// 解析失败时返回空串（前端仍能按 basename 空处理）。
func codebuddyIDEWorkspaceFolder(convDir string, convIdx cbIDEConversationIndex) string {
	var firstUserMsgID string
	for _, m := range convIdx.Messages {
		if m.Role == "user" {
			firstUserMsgID = m.ID
			break
		}
	}
	if firstUserMsgID == "" {
		return ""
	}

	var mf cbIDEMessageFile
	if !readJSONFile(filepath.Join(convDir, "messages", firstUserMsgID+".json"), &mf) {
		return ""
	}
	return parseWorkspaceFolder(mf.Message)
}

// parseWorkspaceFolder 从消息正文里抽取 `Workspace Folder: <path>` 的路径。
// 正文可能是转义过的 JSON 字符串，故直接按标记子串扫描，取到行尾。
func parseWorkspaceFolder(body string) string {
	const marker = "Workspace Folder:"
	idx := strings.Index(body, marker)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(marker):]
	// 到第一个换行（含转义的 \n）为止。
	rest = strings.SplitN(rest, "\\n", 2)[0]
	rest = strings.SplitN(rest, "\n", 2)[0]
	return strings.TrimSpace(rest)
}

// readJSONFile 读取并解码一个 JSON 文件到 v，成功返回 true。
func readJSONFile(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}
