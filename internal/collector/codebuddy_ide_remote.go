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
//   - "[BaseAgent:*] run start" / "[BaseAgent:*] run end" —— 一次问答的开始/结束
//   - "[AcpAgent:<32hex>]" 或行内含 "<32hex>" 字符串     —— 该 conv 有活动
//
// 状态推断（3/30 分钟窗口延续桌面版语义）：
//   - conv 与最新 run start/end 时间邻近（5s 内），最新 run 是 start 且窗口内     → active（正在工作）
//   - conv 与最新 run 邻近，最新 run 是 end                                        → waiting_for_input（工作已完成）
//   - conv 在 log 中未出现但 current.json 3min 内更新                              → active（新建，尚未产生事件）
//   - 30 分钟内无任何写入                                                          → 不上报
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
	// cbIDERemoteActiveWindow：conv 关联到最新 run 且 age < 3min → 视为正在工作/活跃。
	cbIDERemoteActiveWindow = 3 * time.Minute
	// cbIDERemoteStaleWindow：任何会话超过此窗口无写盘 → 剔除。
	cbIDERemoteStaleWindow = 30 * time.Minute
	// runAssociationWindowMs：将 run start/end 与 conv_seen 关联的时间窗口。
	// exthost 是单线程 JS，一次 run 内会持续输出该 conv 的事件，5s 足够覆盖。
	runAssociationWindowMs = 5000
	// remoteLogTailBytes：从日志尾部读取的字节数。多次 run 的关键事件都在最后几十 KB 内。
	remoteLogTailBytes = 256 * 1024
	// chatLogBaseName：扩展写入的对话 log 文件名（含中文）。测试与实现共用同一常量。
	chatLogBaseName = "腾讯云代码助手.log"
)

// cbIDERemoteCurrentIndex 对应 genie-history/<b64ws>/current.json
type cbIDERemoteCurrentIndex struct {
	ConversationID string `json:"conversationId"`
}

// convEventState 单个 conv 从 log tail 提取的状态摘要
type convEventState struct {
	lastSeenMs        int64 // conv id 在 log 中最后出现的时间
	latestRunStartMs  int64 // 最新一次 [BaseAgent:*] run start 时间（全局，不限该 conv）
	latestRunEndMs    int64 // 最新一次 run end 时间
	latestRunTiedThis bool  // true 表示最新 run 与该 conv 时间上邻近，可归属
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

	// 单次扫 log tail：提取全局 latestRunStart/End 以及每个 convID 的 lastSeen。
	latestRunStartMs, latestRunEndMs, convLastSeen := scanLogTail(logTail)

	var out []protocol.SessionInfo
	for _, ws := range workspaces {
		lastSeen := convLastSeen[ws.conversationID]
		st := convEventState{
			lastSeenMs:       lastSeen,
			latestRunStartMs: latestRunStartMs,
			latestRunEndMs:   latestRunEndMs,
		}
		latestRun := st.latestRunStartMs
		if st.latestRunEndMs > latestRun {
			latestRun = st.latestRunEndMs
		}
		if lastSeen > 0 && latestRun > 0 && absMs(latestRun, lastSeen) <= runAssociationWindowMs {
			st.latestRunTiedThis = true
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

// scanLogTail 单次线性扫描 log tail，返回全局最新 run start/end 时间以及每个 convID 的
// 最后出现时间。convID 匹配采用 32 位 hex 字符串精确定位（CodeBuddy 的 conversationId 长度固定）。
func scanLogTail(data []byte) (latestRunStartMs, latestRunEndMs int64, convLastSeen map[string]int64) {
	convLastSeen = map[string]int64{}
	if len(data) == 0 {
		return
	}
	for _, raw := range bytes.Split(data, []byte("\n")) {
		if len(raw) < 24 {
			continue
		}
		ts, ok := parseLogLineTimestamp(raw)
		if !ok {
			continue
		}
		if bytes.Contains(raw, []byte("[BaseAgent")) {
			if bytes.Contains(raw, []byte("] run start")) && ts > latestRunStartMs {
				latestRunStartMs = ts
			} else if bytes.Contains(raw, []byte("] run end")) && ts > latestRunEndMs {
				latestRunEndMs = ts
			}
		}
		for _, id := range extractHex32Tokens(raw) {
			if prev, ok := convLastSeen[id]; !ok || ts > prev {
				convLastSeen[id] = ts
			}
		}
	}
	return
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

func absMs(a, b int64) int64 {
	if a > b {
		return a - b
	}
	return b - a
}

// mapCodeBuddyIDERemoteState 用 (log 事件摘要 + current.json mtime + now) 推断 remote session 状态。
//
//   - 与最新 run 关联（latestRunTiedThis=true）：
//     latestRunStart > latestRunEnd → active（active window 内） / waiting_for_input（超出，僵尸）
//     否则                          → waiting_for_input（工作已完成）
//   - 未与最新 run 关联但 log 里出现过 conv → waiting_for_input（之前完成过一轮）
//   - log 里完全无 conv 出现，仅有 current.json：
//     age < active window → active（新建，未产生事件）；否则 waiting_for_input
//   - 任何情形 age ≥ stale window → 剔除（返回 false）
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

	if ev.latestRunTiedThis {
		if ev.latestRunStartMs > ev.latestRunEndMs {
			if ageMs < activeMs {
				return protocol.StateActive, true
			}
			// run 开始了但很久没写盘 → exthost 可能已挂或该轮被中断。
			return protocol.StateWaitingForInput, true
		}
		return protocol.StateWaitingForInput, true
	}

	if ev.lastSeenMs == 0 && ageMs < activeMs {
		// current.json 刚写、log 尾部还没抓到事件（比如刚 createConversation）。
		return protocol.StateActive, true
	}
	return protocol.StateWaitingForInput, true
}
