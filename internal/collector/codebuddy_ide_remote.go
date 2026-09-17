// Package collector 中 codebuddy_ide_remote.go 采集「CodeBuddy IDE 通过 VS Code Remote
// 连到 Linux 服务器」这一场景的会话状态。
//
// 与 codebuddy_ide.go（桌面版）互补而非替代：
//
//	桌面版 (macOS/Windows) —— 读 CodeBuddyExtension/Data/*/CodeBuddyIDE/*/history/*/index.json
//	远程版 (仅 Linux)      —— 读 ~/.codebuddy-server-cn 目录下由扩展写入的 log/指针
//
// 服务器端稳定信号只有两处：
//
//  1. workspace → 当前 conversationId 指针
//     <root>/data/User/globalStorage/tencent-cloud.coding-copilot/genie-history/<b64ws>/current.json
//     其中 <b64ws> 是 workspace 绝对路径的 URL-safe base64（用 '_' 而非 '=' 做尾部 padding）。
//
//  2. 对话运行时事件日志
//     <root>/data/logs/<sessionTs>/exthost<N>/Tencent-Cloud.coding-copilot/腾讯云代码助手.log
//     每次 exthost 重启（IDE 重连等）目录后缀 +1，日志路径不稳定，故按当前 exthost 进程 fd
//     动态解析。找不到时降级到 glob + mtime 最新。
//
// 关键事件模式（由观察归纳）：
//   - "createConversation: <32hex>"                       —— 新会话
//   - "[BaseAgent:craft] run start" / "run end"           —— 主 agent 一轮问答的开始/结束
//     （只认 craft，subagent 如 code-explorer 的 run 是嵌套在主 run 里的内部循环，忽略；
//     tool 层的 "开始执行/执行结束/All tools execution completed" 也只是主 run 内部的
//     tool 阶段边沿，一轮里会反复出现，不算整轮结束）
//   - "[AcpAgent:<32hex>]" 或行内含 "<32hex>" 字符串     —— 该 conv 有活动
//   - 权限弹窗："[TerminalExecutor] emit event: user_confirm_required" /
//     "[ToolManager] 请求用户确认" / "[AcpAgent:<32hex>] ... reporting permission request to"
//     —— 请求用户确认；"[AcpAgent:<32hex>] Permission response: approved=..." /
//     "requestPermission timed out" —— 弹窗被答复或超时（详见 scanApprovalSignals）
//
// 状态推断（3/30 分钟窗口延续桌面版语义）：
//
//	最优先：该 exthost 日志里"请求用户确认"晚于"确认结束" → waiting_for_approval。
//	  此时主 run 虽然没 end，但 agent 被弹窗阻塞，语义上不是"正在工作"。
//	核心判定基于 [BaseAgent:craft] run start / run end 对：
//	  - run start > run end          → active（当前有 open run 在跑）
//	  - run end >= run start > 0     → waiting_for_input（一轮已完成；run end 之后的
//	                                    cleanupSession / Removed / destroyed 等清理事件
//	                                    都不算新活动）
//	  - log tail 里没抓到 run 事件但 conv 有活动 → active（run 早于 tail 起始且未 end，
//	                                    典型于运行时间长的 tool call 期间）
//	活跃度判活基于 conv 最近 lastSeen（3min 内活跃 / 30min 内保留 / 超时剔除），
//	lastSeen 忽略纯 UI 事件（[ConfigService] chatInputDraft）。
//
// 平台限制：仅 Linux 生效（其他平台 codebuddy-server-cn 不存在），其余平台返回 nil。
package collector

import (
	"bytes"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

const (
	// cbIDERemoteActiveWindow：conv 最近活动 age < 3min → 视为活跃/正在工作。
	cbIDERemoteActiveWindow = 3 * time.Minute
	// cbIDERemoteStaleWindow：任何会话超过此窗口无写盘 → 剔除。
	cbIDERemoteStaleWindow = 30 * time.Minute
	// cbIDERemoteApprovalWindow：一次「请求用户确认」超过此窗口仍未看到答复/超时事件，
	// 就不再判定为等待审批。CodeBuddy 侧 requestPermission 的超时是 300s 且会落
	// "requestPermission timed out"，这里留同量级兜底，防止日志缺失让会话永久卡在
	// waiting_for_approval。
	cbIDERemoteApprovalWindow = 5 * time.Minute
	// remoteLogTailBytes：从日志尾部读取的字节数。多次 run 的关键事件都在最后几十 KB 内。
	remoteLogTailBytes = 256 * 1024
	// chatLogBaseName：扩展写入的对话 log 文件名（含中文）。测试与实现共用同一常量。
	chatLogBaseName = "腾讯云代码助手.log"
	// mainAgentTag：主 agent 的 [BaseAgent:xxx] 名字。只有这个 tag 的 run start/end
	// 才算整轮边沿；subagent（code-explorer 等）的 run 属于主 run 内部循环，忽略。
	mainAgentTag = "[BaseAgent:craft]"
)

// cbIDERemoteCurrentIndex 对应 genie-history/<b64ws>/current.json
type cbIDERemoteCurrentIndex struct {
	ConversationID string `json:"conversationId"`
}

// convEventState 单个 conv 从 log tail 提取的状态摘要
type convEventState struct {
	lastSeenMs         int64 // conv id 在 log 中最后出现的时间（用于判活/stale，忽略纯 UI 事件）
	latestRunStartMs   int64 // 最新一次主 agent (mainAgentTag) run start 时间（日志级）
	latestRunEndMs     int64 // 最新一次主 agent run end 时间（日志级）
	lastApprovalAskMs  int64 // 最近一次「请求用户确认」事件时间（日志级）
	lastApprovalDoneMs int64 // 最近一次确认结束（用户答复 / 请求超时）时间（日志级）
	isRunOwner         bool  // 该 conv 是否是它所在 log 的最近活跃者（只有它才认这份日志的信号）
}

// CollectCodeBuddyIDERemoteSessions 扫描本机 ~/.codebuddy-server-cn，为每个远程 workspace
// 上报一条 CodeBuddy IDE 会话。非 Linux / 目录不存在时返回 nil。
func CollectCodeBuddyIDERemoteSessions() []protocol.SessionInfo {
	if runtime.GOOS != "linux" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	root := filepath.Join(home, ".codebuddy-server-cn")
	if _, err := os.Stat(root); err != nil {
		return nil
	}
	return collectCodeBuddyIDERemoteFromRoot(root, time.Now().UnixMilli())
}

// collectCodeBuddyIDERemoteFromRoot 是可测试入口：所有路径基于给定 root（即 ~/.codebuddy-server-cn）。
func collectCodeBuddyIDERemoteFromRoot(root string, nowMs int64) []protocol.SessionInfo {
	workspaces := enumerateRemoteWorkspaces(root)
	if len(workspaces) == 0 {
		return nil
	}

	// 一台机器上往往同时存在多个 exthost 进程（一个 IDE 窗口 / 一条远程连接一个），各自写
	// 各自的对话 log。只读其中一个会让其余 session 全部失真（实测：两个远程会话时，没被
	// 读到的那个 3 分钟后就被降级成 waiting_for_input）。所以这里把全部活跃 log 都扫一遍。
	convLastSeen := map[string]int64{}         // conv → 最近出现时间（跨 log 合并取最大）
	ownerEvents := map[string]convEventState{} // 每个 log 的归属 conv → 该 log 的 run/权限信号
	var globalRunStartMs, globalRunEndMs int64 // 跨 log 的 run 边沿，给非归属 conv 兜底

	// 会话身份白名单：只有 current.json 里登记过的 conversationId 才算数。日志里 32 位 hex
	// 到处都是（tool call / message / file id），不设白名单就会把归属判给噪声。
	knownConvs := map[string]bool{}
	for _, ws := range workspaces {
		knownConvs[ws.conversationID] = true
	}

	for _, logPath := range findActiveExthostLogs(root) {
		logTail, _ := readFileTail(logPath, remoteLogTailBytes)
		runStartMs, runEndMs, seen, runOwnerConv := scanLogTail(logTail, knownConvs)
		approval := scanApprovalSignals(logTail)

		for id, ts := range seen {
			if prev, ok := convLastSeen[id]; !ok || ts > prev {
				convLastSeen[id] = ts
			}
		}
		if runStartMs > globalRunStartMs {
			globalRunStartMs = runStartMs
		}
		if runEndMs > globalRunEndMs {
			globalRunEndMs = runEndMs
		}

		// 归属者 = 该 log 里 lastSeen 最新的 conv。exthost 单线程一次只有一个 conv 在活跃，
		// 因此最近活跃的 conv 就是驱动最新 run / 发起待确认请求的那个。用"最近活跃"而非
		// "距 run start 60s 内"判定归属，才能覆盖长 run（lastSeen 持续前移、与 run start
		// 差值远超 60s）场景。
		ownerID := ""
		var ownerSeenMs int64
		for id, ts := range seen {
			if ts > ownerSeenMs {
				ownerSeenMs, ownerID = ts, id
			}
		}
		// 不带 conv id 的权限事件归谁：优先认领方，其次本 log 的最近活跃 conv。
		anonConvID := approval.anonConvID
		if anonConvID == "" {
			anonConvID = ownerID
		}

		// 权限信号按 conv 记账：带 [AcpAgent:<conv>] 的归各自 conv，不带 conv id 的
		// 归 anonConvID。多个 conv 共用一个 exthost log 时，这样才不会把 A 挂着的
		// 权限弹窗算到最近活跃的 B 头上。
		perConv := map[string]convEventState{}
		if ownerID != "" {
			perConv[ownerID] = convEventState{
				lastSeenMs:       ownerSeenMs,
				latestRunStartMs: runStartMs,
				latestRunEndMs:   runEndMs,
			}
		}
		// open run 的归属在 run start 那一刻就定死（runOwnerConv）：run start 行不带 conv id，
		// 只能取当时最近活跃的 conv。用"此刻的最新活跃者"而不是"tail 末尾的最新活跃者"，
		// 另一个会话随后再说多少话都抢不走这笔 run —— 否则状态会每几秒抖一次。
		// runOwnerConv 为空（run start 之前 tail 里没有任何 conv 活动）时退回最近活跃者。
		if runStartMs > runEndMs {
			runOwnerID := runOwnerConv
			if runOwnerID == "" {
				runOwnerID = ownerID
			}
			if runOwnerID != "" {
				ev := perConv[runOwnerID]
				if seen[runOwnerID] > ev.lastSeenMs {
					ev.lastSeenMs = seen[runOwnerID]
				}
				ev.latestRunStartMs = runStartMs
				ev.latestRunEndMs = runEndMs
				ev.isRunOwner = true
				perConv[runOwnerID] = ev
			}
		}
		for id, st := range approval.byConv {
			ev := perConv[id]
			if seen[id] > ev.lastSeenMs {
				ev.lastSeenMs = seen[id]
			}
			if st.askMs > ev.lastApprovalAskMs {
				ev.lastApprovalAskMs = st.askMs
			}
			if st.doneMs > ev.lastApprovalDoneMs {
				ev.lastApprovalDoneMs = st.doneMs
			}
			perConv[id] = ev
		}
		if anonConvID != "" && (approval.anonAskMs > 0 || approval.anonDoneMs > 0) {
			ev := perConv[anonConvID]
			if approval.anonAskMs > ev.lastApprovalAskMs {
				ev.lastApprovalAskMs = approval.anonAskMs
			}
			if approval.anonDoneMs > ev.lastApprovalDoneMs {
				ev.lastApprovalDoneMs = approval.anonDoneMs
			}
			perConv[anonConvID] = ev
		}

		for id, ev := range perConv {
			// 同一个 conv 出现在多个 log 里时取最近活跃的那份。
			if prev, ok := ownerEvents[id]; ok && prev.lastSeenMs >= ev.lastSeenMs {
				continue
			}
			ownerEvents[id] = ev
		}
	}

	var out []protocol.SessionInfo
	for _, ws := range workspaces {
		st, ok := ownerEvents[ws.conversationID]
		if !ok {
			// 该 conv 不是任何 log 的最近活跃者（多 workspace 共用一个 exthost 时常见）：
			// 沿用全局 run 边沿，但刻意不带权限信号 —— 否则会把别的会话挂着的权限弹窗
			// 算到自己头上。
			st = convEventState{
				lastSeenMs:       convLastSeen[ws.conversationID],
				latestRunStartMs: globalRunStartMs,
				latestRunEndMs:   globalRunEndMs,
			}
		}
		lastSeen := st.lastSeenMs

		state, include := mapCodeBuddyIDERemoteState(st, ws.currentMtimeMs, nowMs)
		if !include {
			continue
		}

		lastActivity := lastSeen
		if lastActivity == 0 {
			lastActivity = ws.currentMtimeMs
		}

		out = append(out, protocol.SessionInfo{
			SessionID:     ws.conversationID,
			Kind:          protocol.KindInteractive,
			Tool:          protocol.ToolCodeBuddyIDERemote,
			CWD:           ws.cwd,
			StartedAt:     lastActivity, // 无独立起始，用最后活动近似
			LastHeartbeat: lastActivity,
			State:         state,
			LastActivity:  lastActivity,
		})
	}
	return out
}

// remoteWorkspace 单个 remote workspace 的必要元数据
type remoteWorkspace struct {
	cwd            string
	conversationID string
	currentMtimeMs int64
}

// enumerateRemoteWorkspaces 扫 genie-history/*/current.json，返回所有非空指针。
func enumerateRemoteWorkspaces(root string) []remoteWorkspace {
	base := filepath.Join(root, "data", "User", "globalStorage",
		"tencent-cloud.coding-copilot", "genie-history")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []remoteWorkspace
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		wsPath := decodeWorkspacePath(e.Name())
		currentJSON := filepath.Join(base, e.Name(), "current.json")
		fi, err := os.Stat(currentJSON)
		if err != nil {
			continue
		}
		var idx cbIDERemoteCurrentIndex
		if !readJSONFile(currentJSON, &idx) || idx.ConversationID == "" {
			continue
		}
		out = append(out, remoteWorkspace{
			cwd:            wsPath,
			conversationID: idx.ConversationID,
			currentMtimeMs: fi.ModTime().UnixMilli(),
		})
	}
	return out
}

// decodeWorkspacePath 解码 VS Code Remote 使用的 URL-safe base64 workspace 目录名。
// 末尾用 '_' 而非 '=' 做 padding；解码失败或结果非合理路径时返回空串。
func decodeWorkspacePath(name string) string {
	// 剥掉尾部 '_' padding 后按 RawURLEncoding 解。
	trimmed := strings.TrimRight(name, "_")
	data, err := base64.RawURLEncoding.DecodeString(trimmed)
	if err != nil {
		return ""
	}
	s := string(data)
	if !strings.ContainsRune(s, '/') && !strings.ContainsRune(s, '\\') {
		// 不是绝对路径，可能是别的编码方式，谨慎返回空。
		return ""
	}
	return s
}

// findActiveExthostLogs 定位当前全部活跃 exthost 正在写的对话 log。
//
// 优先策略：读 /proc/*/cmdline 找所有 `bootstrap-fork --type=extensionHost` 进程，然后遍历
// 每个进程 /proc/<pid>/fd/ 的符号链接，命中以 chatLogBaseName 结尾者即为答案。
//
// 降级：glob 匹配全部候选 log，按 mtime 降序返回。
//
// 返回空切片表示未找到（后续跳过 log tail 扫描，走 current.json mtime 判活）。
//
// 必须返回全部而不是只取一个：多个 exthost 并存时只扫其中一个，其余会话的状态会全部失真。
func findActiveExthostLogs(root string) []string {
	if logs := findExthostLogsViaProc(root); len(logs) > 0 {
		return logs
	}
	return findExthostLogsViaGlob(root)
}

func findExthostLogsViaProc(root string) []string {
	procDir, err := os.Open("/proc")
	if err != nil {
		return nil
	}
	defer procDir.Close()
	names, err := procDir.Readdirnames(-1)
	if err != nil {
		return nil
	}
	rootLogsPrefix := filepath.Join(root, "data", "logs") + string(filepath.Separator)
	var out []string
	seen := map[string]struct{}{}
	for _, name := range names {
		if !isAllDigits(name) {
			continue
		}
		cmdline, err := os.ReadFile("/proc/" + name + "/cmdline")
		if err != nil {
			continue
		}
		// /proc/<pid>/cmdline 用 NUL 分隔 argv。
		if !bytes.Contains(cmdline, []byte("bootstrap-fork")) ||
			!bytes.Contains(cmdline, []byte("--type=extensionHost")) {
			continue
		}
		// 遍历该 pid 的 fd。
		fdDir := "/proc/" + name + "/fd"
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if strings.HasSuffix(target, string(filepath.Separator)+chatLogBaseName) &&
				strings.HasPrefix(target, rootLogsPrefix) {
				if _, dup := seen[target]; dup {
					continue
				}
				seen[target] = struct{}{}
				out = append(out, target)
			}
		}
	}
	return out
}

func findExthostLogsViaGlob(root string) []string {
	pattern := filepath.Join(root, "data", "logs", "*", "exthost*",
		"Tencent-Cloud.coding-copilot", chatLogBaseName)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return nil
	}
	type entry struct {
		path string
		mod  int64
	}
	entries := make([]entry, 0, len(matches))
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		entries = append(entries, entry{path: m, mod: fi.ModTime().UnixNano()})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].mod > entries[j].mod })
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.path)
	}
	return out
}

// readFileTail 从文件末尾读取最多 n 字节，成功返回原始 bytes。
// 空路径或文件不存在时返回 nil, nil。
func readFileTail(path string, n int64) ([]byte, error) {
	if path == "" {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	seekPos := fi.Size() - n
	if seekPos < 0 {
		seekPos = 0
	}
	if _, err := f.Seek(seekPos, io.SeekStart); err != nil {
		return nil, err
	}
	return io.ReadAll(f)
}

// scanLogTail 单次线性扫描 log tail，返回全局最新主 agent run start / run end 时间、
// 每个 convID 的最后出现时间，以及最新一次 run start 的归属 conv。
//
// 只识别 mainAgentTag（"[BaseAgent:craft]"）的 run start / run end 作为整轮边沿。忽略:
//   - subagent（code-explorer 等）的 run（主 run 内部的嵌套循环）
//   - tool 层的 "开始执行/执行结束/All tools execution completed"（主 run 内会反复出现）
//
// convLastSeen 仅在"agent 活动"事件里更新，跳过纯 UI 状态更新（如 [ConfigService]
// chatInputDraft，用户打字时每按一键都会写一条，含 conv id 但不代表 agent 在工作）。
//
// conversationId 用 32 位 hex 定位，但 32 位 hex 不等于 conv id：日志里 tool call id、
// message id、file id 全是这个形状（实测一份 256KB tail 里有 50 多个）。所以 knownConvs
// 非空时只认登记在其中的 conversationId，其余一律当噪声丢掉 —— 否则「最近活跃的 conv」
// 会被噪声抢走，正在跑 open run 的会话就会在 active / waiting_for_input 之间抖动。
// knownConvs 传 nil 表示不过滤（保留"提取所有 hex32"的原始行为）。
//
// runOwner 是最新一次 run start 那一刻「最近活跃的 conv」。run start 行本身不带 conv id，
// 归属必须在事件发生的那一刻定下来：之后别的会话再说多少话，也不能把这笔 run 抢走。
func scanLogTail(data []byte, knownConvs map[string]bool) (latestRunStartMs, latestRunEndMs int64, convLastSeen map[string]int64, runOwner string) {
	convLastSeen = map[string]int64{}
	if len(data) == 0 {
		return
	}
	mainNeedle := []byte(mainAgentTag)
	var topConv string // 扫描过程中 lastSeen 最新的 conv（截至当前行）
	var topMs int64
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) < 24 {
			continue
		}
		// 终端回显里可能整段粘贴了别的会话的日志，既会污染 conv id 也会伪造 run 事件。
		if isTerminalEchoLine(raw) {
			continue
		}
		ts, ok := parseLogLineTimestamp(raw)
		if !ok {
			continue
		}
		if bytes.Contains(raw, mainNeedle) {
			switch {
			case bytes.Contains(raw, []byte("] run start")):
				if ts > latestRunStartMs {
					latestRunStartMs = ts
					runOwner = topConv
				}
			case bytes.Contains(raw, []byte("] run end")):
				if ts > latestRunEndMs {
					latestRunEndMs = ts
				}
			}
		}
		// UI-only 事件不算 agent 活动 —— 用户在输入框打字时 [ConfigService]
		// 每按一键都会刷 chatInputDraft，含 conv id，但 agent 完全空闲。
		if isNonAgentActivityLine(raw) {
			continue
		}
		for _, id := range extractHex32Tokens(raw) {
			if knownConvs != nil && !knownConvs[id] {
				continue
			}
			if prev, ok := convLastSeen[id]; !ok || ts > prev {
				convLastSeen[id] = ts
			}
			if ts > topMs {
				topMs, topConv = ts, id
			}
		}
	}
	return
}

// approvalAskNeedles / approvalDoneNeedles 是「等待用户确认（权限弹窗）」这条时间线的
// 两端。形态取自真实 exthost 日志（2026-09-17 本机 2165ef02 会话）：
//
//	请求确认（ask）：
//	  [TerminalExecutor] emit event: user_confirm_required
//	  [ToolManager] 请求用户确认: <toolId> - execute_command - execute
//	  [TerminalExecutor] requiredApprove: false, needUserConfirm: true, fromHook: undefined
//	  [TerminalExecutor] [beforeExecute] Permission decision: source=safety_rule_ask, ..., needConfirm=true
//	  [AcpAgent:<conv>] HTTP fallback: reporting permission request to ...（唯一带 conv id 的一个）
//	确认结束（done）：
//	  [AcpAgent:<conv>] Permission response: approved=true|false, ...
//	  [AcpAgent:<conv>] requestPermission timed out after 300000ms, treating as unanswered
//	  [TerminalExecutor] [onCancelled], reason: Permission request timed out with no user response
//
// 一条线上只要 ask 晚于 done，就说明此刻弹窗还挂着、agent 被阻塞。
var (
	approvalAskNeedles = [][]byte{
		[]byte("[TerminalExecutor] emit event: user_confirm_required"),
		[]byte("[ToolManager] 请求用户确认:"),
		[]byte("requiredApprove: false, needUserConfirm: true"),
		[]byte("Permission decision: source=safety_rule_ask, allowed=true, needConfirm=true"),
		[]byte("HTTP fallback: reporting permission request to"),
	}
	approvalDoneNeedles = [][]byte{
		[]byte("Permission response: approved="),
		[]byte("requestPermission timed out after"),
		[]byte("[onCancelled], reason: Permission request timed out"),
	}
	// terminalEchoNeedles 命中其一的行是「终端执行」的命令行回显 / 管道日志，不是 agent
	// 事件。典型场景：在会话 A 里 grep 会话 B 的日志，B 的 conv id 与 "Permission response"
	// 等字样会被原样写进 A 的日志，若不排除就会把 A 误判成等待审批、把 B 的 conv id 记到
	// A 的 lastSeen 上（本机实测踩到过）。
	terminalEchoNeedles = [][]byte{
		[]byte("[TerminalExecutor] 命令:"),
		[]byte("[TerminalExecutor] 执行普通命令:"),
		[]byte("[StandaloneTerminalManager]"),
		[]byte("[StandaloneTerminalProcess]"),
		[]byte("[StreamParser] 参数解析完成"),
		[]byte("[StreamParser] finalizeToolCall"),
		[]byte("[ChunkDebug]"),
		[]byte("decodedPreview="),
	}
)

// isTerminalEchoLine 判定一行是否为终端回显/管道日志（详见 terminalEchoNeedles）。
func isTerminalEchoLine(raw []byte) bool {
	for _, needle := range terminalEchoNeedles {
		if bytes.Contains(raw, needle) {
			return true
		}
	}
	return false
}

// approvalState 单个 conv 的「请求确认 / 确认结束」两条时间线。
type approvalState struct{ askMs, doneMs int64 }

// approvalSignals 一份 log tail 里的权限信号。
//
// 带 [AcpAgent:<conv>] 前缀的事件归属明确，直接按 conv 记账（byConv）。剩下的事件
// （user_confirm_required、safety_rule_ask、[onCancelled] 超时）不带 conv id，记在
// anonAskMs/anonDoneMs，由 anonConvID 决定归给谁：
//   - 非空：带 conv 的 ask 不早于 anon ask，说明两者是同一次请求（"进入等待确认"与
//     "上报权限请求"前后脚写入），归给那个 conv；
//   - 空：没有任何带 conv 的 ask 可以认领，交给调用方按 log 的最近活跃 conv 归属。
type approvalSignals struct {
	byConv     map[string]approvalState
	anonAskMs  int64
	anonDoneMs int64
	anonConvID string
}

// scanApprovalSignals 扫描 log tail 提取权限信号。
func scanApprovalSignals(data []byte) approvalSignals {
	sig := approvalSignals{byConv: map[string]approvalState{}}
	if len(data) == 0 {
		return sig
	}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) < 24 {
			continue
		}
		if isTerminalEchoLine(raw) {
			continue
		}
		ts, ok := parseLogLineTimestamp(raw)
		if !ok {
			continue
		}
		if convID := acpAgentConvID(raw); convID != "" {
			// 只有真正命中权限关键字才记账：普通 [AcpAgent:<conv>] 行（tool executing
			// 之类）与权限无关，记账会让这个 conv 在 collect 侧丢掉全局 run 边沿。
			isAsk, isDone := containsAny(raw, approvalAskNeedles), containsAny(raw, approvalDoneNeedles)
			if !isAsk && !isDone {
				continue
			}
			st := sig.byConv[convID]
			if isAsk && ts > st.askMs {
				st.askMs = ts
			}
			if isDone && ts > st.doneMs {
				st.doneMs = ts
			}
			sig.byConv[convID] = st
			continue
		}
		if ts > sig.anonAskMs && containsAny(raw, approvalAskNeedles) {
			sig.anonAskMs = ts
		}
		if ts > sig.anonDoneMs && containsAny(raw, approvalDoneNeedles) {
			sig.anonDoneMs = ts
		}
	}
	bestID := ""
	var bestMs int64
	for id, st := range sig.byConv {
		if st.askMs > bestMs && st.askMs >= sig.anonAskMs {
			bestMs, bestID = st.askMs, id
		}
	}
	sig.anonConvID = bestID
	return sig
}

// containsAny 判断 raw 是否含有 needles 中任意一个。
func containsAny(raw []byte, needles [][]byte) bool {
	for _, n := range needles {
		if bytes.Contains(raw, n) {
			return true
		}
	}
	return false
}

// acpAgentTagPrefix 是会话级事件的行内 tag，形如 "[AcpAgent:<32hex>]"。
var acpAgentTagPrefix = []byte("[AcpAgent:")

// acpAgentConvID 抽出 "[AcpAgent:<32hex>]" 里的 conv id；无该 tag 或格式不符返回 ""。
func acpAgentConvID(raw []byte) string {
	i := bytes.Index(raw, acpAgentTagPrefix)
	if i < 0 {
		return ""
	}
	rest := raw[i+len(acpAgentTagPrefix):]
	if len(rest) < 32 {
		return ""
	}
	for k := 0; k < 32; k++ {
		if !isHexLower(rest[k]) {
			return ""
		}
	}
	return string(rest[:32])
}

// isNonAgentActivityLine 判定一行 log 是否属于"非 agent 活动"（UI 状态、心跳等），
// 这类行即便含 conv id 也不应用于 lastSeen 判活。
func isNonAgentActivityLine(raw []byte) bool {
	// [ConfigService] update config: key=chatInputDraft:<conv>, value=[...]
	// —— IDE 输入框草稿。用户打字每按一键都写一条，会把空闲的 conv 错判成 active。
	return bytes.Contains(raw, []byte("[ConfigService]"))
}

// parseLogLineTimestamp 解析行首 "2006-01-02 15:04:05.000 " 前缀，返回 Unix ms。
// 不带毫秒（"2006-01-02 15:04:05 "）也接受。行首非该格式返回 false。
func parseLogLineTimestamp(line []byte) (int64, bool) {
	// 至少要有 "YYYY-MM-DD HH:MM:SS" 19 字节。
	if len(line) < 19 {
		return 0, false
	}
	// 快速校验分隔位置，避免每行都进 time.Parse 触发反射开销。
	if line[4] != '-' || line[7] != '-' || line[10] != ' ' ||
		line[13] != ':' || line[16] != ':' {
		return 0, false
	}
	end := 19
	if len(line) >= 23 && line[19] == '.' {
		end = 23
	}
	layout := "2006-01-02 15:04:05"
	if end == 23 {
		layout = "2006-01-02 15:04:05.000"
	}
	// 用本地时区解析，与 log 写入端一致（IDE 扩展写本地时区无 tz 后缀）。
	t, err := time.ParseInLocation(layout, string(line[:end]), time.Local)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

// extractHex32Tokens 扫描一行，返回所有连续 32 位 [0-9a-f] 子串。
// 用于识别 conversationId（CodeBuddy 使用 32 位 hex）。
func extractHex32Tokens(line []byte) []string {
	var out []string
	n := len(line)
	i := 0
	for i < n {
		// 找一段 hex run
		start := i
		for i < n && isHexLower(line[i]) {
			i++
		}
		if i-start >= 32 {
			// 这段 hex 可能包含多个 32 位窗口，但 conversationId 之间总有非 hex 分隔，
			// 而 tool-call-id / trace-id 一般带 '-' 或 '_'，不会形成 32 位纯连续。
			// 取起始 32 字节即可（若整段恰好 32 位则唯一）。
			if i-start == 32 {
				out = append(out, string(line[start:i]))
			}
		}
		if i < n {
			i++
		}
	}
	return out
}

func isHexLower(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f')
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// mapCodeBuddyIDERemoteState 用 (log 事件摘要 + current.json mtime + now) 推断 remote session 状态。
//
// 核心思路:多 workspace 场景下同一个 log 混合多个 conv 的事件,exthost 单线程一次只有
// 一个 conv 在活跃。"最新一次 run" 只归属给全局最近活跃的那个 conv(即 isRunOwner,
// 由 collectCodeBuddyIDERemoteFromRoot 按 lastSeen 最新者确定);非归属者用自身活跃度
// 单独判定。
//
//   - isRunOwner && runStart > runEnd → active（该 conv 正驱动一个 open run，无论 lastSeen
//     距 run start 多远——长 tool call / 长生成期间 lastSeen 会持续前移，与 run start 的
//     差值远超 60s，不能用对称窗口判定归属）
//   - isRunOwner && runEnd ≥ runStart → waiting_for_input（一轮已完成；后续 cleanup 事件也在此窗内）
//   - 非归属者:
//     · lastSeen 3min 内     → waiting_for_input（自己之前完成过一轮）
//     · 3–30min              → waiting_for_input(降级)
//     · ≥30min               → 剔除
//   - log tail 无任何 run 事件但 conv 3min 内有活动 → active（保守,run 早于 tail 起始）
//   - conv 未出现但 current.json 3min 内更新 → active（新建,未产生事件）
func mapCodeBuddyIDERemoteState(ev convEventState, currentJsonMtimeMs, nowMs int64) (protocol.SessionState, bool) {
	activeMs := cbIDERemoteActiveWindow.Milliseconds()
	staleMs := cbIDERemoteStaleWindow.Milliseconds()

	lastActivity := ev.lastSeenMs
	if lastActivity == 0 {
		lastActivity = currentJsonMtimeMs
	}
	if lastActivity == 0 {
		return "", false
	}
	ageMs := nowMs - lastActivity
	if ageMs >= staleMs {
		return "", false
	}

	// 待用户确认（权限弹窗）优先于 open run 保活:此时主 run 虽然没 end,但 agent 被弹窗
	// 阻塞着,语义上是"等待审批"而不是"正在工作"。ask > done 即表示弹窗还挂着没被答复。
	if ev.lastApprovalAskMs > ev.lastApprovalDoneMs &&
		nowMs-ev.lastApprovalAskMs < cbIDERemoteApprovalWindow.Milliseconds() {
		return protocol.StateWaitingForApproval, true
	}

	// open run 保活优先于"3min 无活动降级":模型长静默生成 / 长 tool call 期间,conv 可能
	// 好几分钟不写含 conv id 的日志(只有被忽略的 UI 事件),此时 lastSeen 停摆,但
	// [BaseAgent:craft] run start 之后还没有 run end —— 这是"正在跑"的强信号,不该被误
	// 降级成 waiting_for_input。归属由 isRunOwner(全局最近活跃 conv)判定,而非 lastSeen
	// 距 run start 的对称 60s 窗口:长 run 里 lastSeen 持续前移,对称窗口会把它错判成
	// 非归属者。
	if ev.isRunOwner && ev.latestRunStartMs > ev.latestRunEndMs {
		return protocol.StateActive, true
	}

	if ageMs >= activeMs {
		// 3–30min:无 open run 保活 → 降级为等待。
		return protocol.StateWaitingForInput, true
	}

	// 3min 内有活动:
	if ev.lastSeenMs == 0 {
		// log tail 里该 conv 完全没出现,只有 current.json:视为新建未产生事件。
		return protocol.StateActive, true
	}

	latestRun := ev.latestRunStartMs
	if ev.latestRunEndMs > latestRun {
		latestRun = ev.latestRunEndMs
	}

	// log tail 无任何 run 事件:conv 有活动就保守判 active(run 早于 tail 起始)。
	if latestRun == 0 {
		return protocol.StateActive, true
	}

	// 归属者且最新 run 已结束(run end ≥ run start) → waiting_for_input。
	if ev.isRunOwner {
		return protocol.StateWaitingForInput, true
	}

	// 非归属者:该 conv 自身 3min 内有活动但与最新 run 无关(自己之前跑过一轮,或多 workspace
	// 场景下另一个 conv 正在跑),判 waiting_for_input。
	return protocol.StateWaitingForInput, true
}
