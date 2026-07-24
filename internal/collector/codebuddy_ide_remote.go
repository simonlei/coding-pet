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
//
// 状态推断（3/30 分钟窗口延续桌面版语义）：
//   核心判定基于 [BaseAgent:craft] run start / run end 对：
//     - run start > run end          → active（当前有 open run 在跑）
//     - run end >= run start > 0     → waiting_for_input（一轮已完成；run end 之后的
//                                       cleanupSession / Removed / destroyed 等清理事件
//                                       都不算新活动）
//     - log tail 里没抓到 run 事件但 conv 有活动 → active（run 早于 tail 起始且未 end，
//                                       典型于运行时间长的 tool call 期间）
//   活跃度判活基于 conv 最近 lastSeen（3min 内活跃 / 30min 内保留 / 超时剔除），
//   lastSeen 忽略纯 UI 事件（[ConfigService] chatInputDraft）。
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
	"strings"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

const (
	// cbIDERemoteActiveWindow：conv 最近活动 age < 3min → 视为活跃/正在工作。
	cbIDERemoteActiveWindow = 3 * time.Minute
	// cbIDERemoteStaleWindow：任何会话超过此窗口无写盘 → 剔除。
	cbIDERemoteStaleWindow = 30 * time.Minute
	// runAssociationWindowMs：将最新 run start/end 归属到某个 conv 的时间窗口。
	// exthost 是单线程 JS，run 期间会持续输出该 conv 的关联事件；run 结束后 IDE 会
	// 写一批 cleanup 事件（也带 conv id）,持续到 60s 左右。60s 覆盖 cleanup 尾巴，
	// 多 workspace 场景下 conv 切换粒度也远大于 60s。
	runAssociationWindowMs = 60_000
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
	lastSeenMs       int64 // conv id 在 log 中最后出现的时间（用于判活/stale，忽略纯 UI 事件）
	latestRunStartMs int64 // 最新一次主 agent (mainAgentTag) run start 时间（全局）
	latestRunEndMs   int64 // 最新一次主 agent run end 时间（全局）
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
	logPath := findActiveExthostLog(root)
	logTail, _ := readFileTail(logPath, remoteLogTailBytes)

	workspaces := enumerateRemoteWorkspaces(root)
	if len(workspaces) == 0 {
		return nil
	}

	// 单次扫 log tail：提取全局最新 mainAgent run start/end 以及每个 convID 的 lastSeen。
	latestRunStartMs, latestRunEndMs, convLastSeen := scanLogTail(logTail)

	var out []protocol.SessionInfo
	for _, ws := range workspaces {
		lastSeen := convLastSeen[ws.conversationID]
		st := convEventState{
			lastSeenMs:       lastSeen,
			latestRunStartMs: latestRunStartMs,
			latestRunEndMs:   latestRunEndMs,
		}

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

// findActiveExthostLog 定位当前活跃 exthost 正在写的对话 log。
//
// 优先策略：读 /proc/*/cmdline 找 `bootstrap-fork --type=extensionHost` 进程，然后遍历
// 该进程 /proc/<pid>/fd/ 的符号链接，命中以 chatLogBaseName 结尾者即为答案。
//
// 降级：glob 匹配全部候选 log，按 mtime 排序取最新。
//
// 返回空表示未找到（后续跳过 log tail 扫描，走 current.json mtime 判活）。
func findActiveExthostLog(root string) string {
	if p := findExthostLogViaProc(root); p != "" {
		return p
	}
	return findExthostLogViaGlob(root)
}

func findExthostLogViaProc(root string) string {
	procDir, err := os.Open("/proc")
	if err != nil {
		return ""
	}
	defer procDir.Close()
	names, err := procDir.Readdirnames(-1)
	if err != nil {
		return ""
	}
	rootLogsPrefix := filepath.Join(root, "data", "logs") + string(filepath.Separator)
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
				return target
			}
		}
	}
	return ""
}

func findExthostLogViaGlob(root string) string {
	pattern := filepath.Join(root, "data", "logs", "*", "exthost*",
		"Tencent-Cloud.coding-copilot", chatLogBaseName)
	matches, err := filepath.Glob(pattern)
	if err != nil || len(matches) == 0 {
		return ""
	}
	var best string
	var bestMod int64
	for _, m := range matches {
		fi, err := os.Stat(m)
		if err != nil {
			continue
		}
		if mod := fi.ModTime().UnixNano(); mod > bestMod {
			bestMod = mod
			best = m
		}
	}
	return best
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

// scanLogTail 单次线性扫描 log tail，返回全局最新主 agent run start / run end 时间以及
// 每个 convID 的最后出现时间。
//
// 只识别 mainAgentTag（"[BaseAgent:craft]"）的 run start / run end 作为整轮边沿。忽略:
//   - subagent（code-explorer 等）的 run（主 run 内部的嵌套循环）
//   - tool 层的 "开始执行/执行结束/All tools execution completed"（主 run 内会反复出现）
//
// convLastSeen 仅在"agent 活动"事件里更新，跳过纯 UI 状态更新（如 [ConfigService]
// chatInputDraft，用户打字时每按一键都会写一条，含 conv id 但不代表 agent 在工作）。
//
// conversationId 用 32 位 hex 精确定位（CodeBuddy 的 conv id 长度固定）。
func scanLogTail(data []byte) (latestRunStartMs, latestRunEndMs int64, convLastSeen map[string]int64) {
	convLastSeen = map[string]int64{}
	if len(data) == 0 {
		return
	}
	mainNeedle := []byte(mainAgentTag)
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) < 24 {
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
			if prev, ok := convLastSeen[id]; !ok || ts > prev {
				convLastSeen[id] = ts
			}
		}
	}
	return
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
// 一个 conv 在活跃。"最新一次 run" 只归属给邻近有活动的那个 conv;非归属者用自身活跃度
// 单独判定。
//
//   - 该 conv 的 lastSeen 距最新 run 事件 ≤ 60s → 该 conv 是最新 run 的归属者
//     · runStart > runEnd     → active（当前有 open run）
//     · runEnd  ≥ runStart    → waiting_for_input（run 已结束；后续 cleanup 事件也在此窗内）
//   - 该 conv lastSeen 距最新 run 事件 > 60s → 不归属最新 run:
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
	if ageMs >= activeMs {
		// 3–30min:降级为等待,不看 run 边沿。
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

	// 判断该 conv 是否归属最新 run:lastSeen 距最新 run 事件 ≤ 60s。
	tied := absMs(ev.lastSeenMs, latestRun) <= runAssociationWindowMs
	if tied {
		if ev.latestRunStartMs > ev.latestRunEndMs {
			return protocol.StateActive, true
		}
		return protocol.StateWaitingForInput, true
	}

	// 非归属者:该 conv 自身 3min 内有活动但与最新 run 无关(自己之前跑过一轮,或多 workspace
	// 场景下另一个 conv 正在跑),判 waiting_for_input。
	return protocol.StateWaitingForInput, true
}

func absMs(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}
