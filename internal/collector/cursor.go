// Package collector 中 cursor.go 采集 Cursor 本地与 Cursor Remote 的会话状态。
//
// 两边的 agent 都把对话写到同一布局：
//
//	~/.cursor/projects/<slug>/agent-transcripts/<conversationId>/<conversationId>.jsonl
//
// <slug> 是 workspace 绝对路径去掉非字母数字后的结果（与 Cursor 的 project slug 一致）。
// 子代理写在同目录的 subagents/*.jsonl，不单独上报，只用来把父会话抬成「还在跑 / 等审批」。
//
// transcript 末行是判定核心：
//
//	{"type":"turn_ended","status":"success|error|aborted"}  → 这一轮结束，等用户输入
//	其它（user / assistant，或末行还没写完）                  → 这一轮还开着，算工作中
//
// 等审批没有单独的事件。开着的轮次如果最后一条 assistant 消息含 Shell / Write /
// StrReplace / Delete / EditNotebook / CallDynamicTool，并且项目 terminals/ 里
// 没有比这条 transcript 更新的终端文件，就视为卡在确认弹窗上。
//
// 本地和 Remote 的差别只在标签，不在格式：
//
//	本机有 ~/.cursor-server，且该 workspace 出现在 exthost 日志的 workspacePaths 里
//	（或者本机根本没装桌面版 Cursor）→ cursor_remote
//	否则 → cursor
//
// 桌面版 composer（state.vscdb）由 cursor_local.go 补上 transcript 里没有的会话，
// vscode-remote URI 跳过，避免和 Remote 机器上的卡片重复。
//
// 同一 workspace 只留一条：等待审批 > 工作中 > 等待输入，同级取最近活动。
// 超过 30 分钟没有写盘的会话不再上报。
package collector

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

const (
	// cursorStaleWindow：超过此窗口没有任何写盘 → 不再上报。
	cursorStaleWindow = 30 * time.Minute
	// cursorApprovalQuiet：待确认工具写进 transcript 后，静默超过此时长且终端没启动，
	// 才判等待审批。给自动放行的命令留出创建终端文件的时间。
	cursorApprovalQuiet = 3 * time.Second
	// cursorTranscriptTail：从 transcript 尾部读取的字节数，够覆盖最后一条消息。
	cursorTranscriptTail = 256 * 1024
	// cursorLogHead：exthost 启动日志开头就有 workspacePaths，不必读完整文件。
	cursorLogHead = 32 * 1024
)

// cursorApprovalTools 是会弹出用户确认的工具名（transcript 里的 tool_use.name）。
var cursorApprovalTools = map[string]bool{
	"Shell":           true,
	"Write":           true,
	"StrReplace":      true,
	"Delete":          true,
	"EditNotebook":    true,
	"CallDynamicTool": true,
}

// cursorRoots 是一次采集要扫的目录。测试注入临时路径，生产用 defaultCursorRoots。
type cursorRoots struct {
	projectsDir string // ~/.cursor/projects
	serverDir   string // ~/.cursor-server，空表示本机不是 Remote 服务端
	localDB     string // 桌面版 state.vscdb，空表示没有
}

// cursorTurn 是一份 transcript 尾部解析出的状态。
type cursorTurn struct {
	ok         bool
	open       bool
	writing    bool // 末行还没写完，轮次一定还开着
	tools      []string
	startedAt  int64
	lastSeenMs int64
}

// cursorCandidate 是同一 workspace 内参与竞选的一条会话。
type cursorCandidate struct {
	info       protocol.SessionInfo
	rank       int
	activityMs int64
}

// CollectCursorSessions 采集本机 Cursor 本地与 Cursor Remote 会话。
// 目录不存在或单条解析失败时跳过，不拖垮其它采集源。
func CollectCursorSessions() []protocol.SessionInfo {
	return collectCursorFrom(defaultCursorRoots(), time.Now().UnixMilli())
}

func defaultCursorRoots() cursorRoots {
	home, err := os.UserHomeDir()
	if err != nil {
		return cursorRoots{}
	}
	roots := cursorRoots{
		projectsDir: filepath.Join(home, ".cursor", "projects"),
	}
	server := filepath.Join(home, ".cursor-server")
	if fi, err := os.Stat(server); err == nil && fi.IsDir() {
		roots.serverDir = server
	}
	if p, ok := cursorLocalDBPath(); ok {
		roots.localDB = p
	}
	return roots
}

func collectCursorFrom(roots cursorRoots, nowMs int64) []protocol.SessionInfo {
	remotePaths := map[string]string{}
	if roots.serverDir != "" {
		remotePaths = cursorRemoteWorkspaces(roots.serverDir)
	}
	localInstalled := roots.localDB != ""

	best := map[string]cursorCandidate{}
	seen := map[string]bool{}

	if roots.projectsDir != "" {
		for _, proj := range listCursorProjects(roots.projectsDir) {
			tool := cursorToolForProject(proj.slug, roots.serverDir != "", remotePaths, localInstalled)
			cwd := remotePaths[proj.slug]
			if cwd == "" {
				cwd = cwdFromTerminals(proj.dir, proj.slug)
			}
			if cwd == "" {
				cwd = proj.slug
			}
			for _, conv := range proj.convs {
				turn := readCursorTranscript(conv.jsonl)
				sub := readCursorSubagents(conv.dir, nowMs)
				turn = mergeCursorTurn(turn, sub)
				state, include := mapCursorTurn(turn, terminalsBusy(proj.dir, turn.lastSeenMs), nowMs)
				if !include {
					continue
				}
				seen[conv.id] = true
				cand := cursorCandidate{
					rank:       cursorStateRank(state),
					activityMs: turn.lastSeenMs,
					info: protocol.SessionInfo{
						SessionID:     conv.id,
						Kind:          protocol.KindInteractive,
						Tool:          tool,
						CWD:           cwd,
						StartedAt:     turn.startedAt,
						LastHeartbeat: turn.lastSeenMs,
						State:         state,
						LastActivity:  turn.lastSeenMs,
					},
				}
				key := string(tool) + "\x00" + workspaceKey(cwd)
				if prev, ok := best[key]; ok && !preferCursorConversation(prev, cand) {
					continue
				}
				best[key] = cand
			}
		}
	}

	if roots.localDB != "" {
		for _, s := range readCursorLocalComposers(roots.localDB, nowMs) {
			if seen[s.SessionID] {
				continue
			}
			key := string(protocol.ToolCursor) + "\x00" + workspaceKey(s.CWD)
			if _, ok := best[key]; ok {
				continue
			}
			best[key] = cursorCandidate{
				rank:       cursorStateRank(s.State),
				activityMs: s.LastActivity,
				info:       s,
			}
			seen[s.SessionID] = true
		}
	}

	out := make([]protocol.SessionInfo, 0, len(best))
	for _, cand := range best {
		out = append(out, cand.info)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tool != out[j].Tool {
			return out[i].Tool < out[j].Tool
		}
		if out[i].CWD != out[j].CWD {
			return out[i].CWD < out[j].CWD
		}
		return out[i].SessionID < out[j].SessionID
	})
	return out
}

func cursorToolForProject(slug string, serverPresent bool, remotePaths map[string]string, localInstalled bool) protocol.SessionTool {
	if !serverPresent {
		return protocol.ToolCursor
	}
	if _, ok := remotePaths[slug]; ok {
		return protocol.ToolCursorRemote
	}
	if !localInstalled {
		return protocol.ToolCursorRemote
	}
	return protocol.ToolCursor
}

func cursorStateRank(s protocol.SessionState) int {
	switch s {
	case protocol.StateWaitingForApproval:
		return 3
	case protocol.StateActive:
		return 2
	default:
		return 1
	}
}

func preferCursorConversation(best, cand cursorCandidate) bool {
	if best.rank != cand.rank {
		return cand.rank > best.rank
	}
	if best.activityMs != cand.activityMs {
		return cand.activityMs > best.activityMs
	}
	return cand.info.SessionID < best.info.SessionID
}

// cursorProjectSlug 复刻 Cursor 的 project 目录名：非字母数字换成 '-'，合并并去掉首尾。
func cursorProjectSlug(path string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range path {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
			continue
		}
		if !prevDash && b.Len() > 0 {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

type cursorProject struct {
	slug  string
	dir   string
	convs []cursorConv
}

type cursorConv struct {
	id    string
	dir   string
	jsonl string
}

func listCursorProjects(projectsDir string) []cursorProject {
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		return nil
	}
	var out []cursorProject
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		transcripts := filepath.Join(projectsDir, e.Name(), "agent-transcripts")
		convs, err := os.ReadDir(transcripts)
		if err != nil {
			continue
		}
		proj := cursorProject{slug: e.Name(), dir: filepath.Join(projectsDir, e.Name())}
		for _, c := range convs {
			if !c.IsDir() || c.Name() == "subagents" {
				continue
			}
			jsonl := filepath.Join(transcripts, c.Name(), c.Name()+".jsonl")
			if fi, err := os.Stat(jsonl); err != nil || fi.IsDir() {
				continue
			}
			proj.convs = append(proj.convs, cursorConv{
				id:    c.Name(),
				dir:   filepath.Join(transcripts, c.Name()),
				jsonl: jsonl,
			})
		}
		if len(proj.convs) > 0 {
			out = append(out, proj)
		}
	}
	return out
}

func readCursorSubagents(convDir string, nowMs int64) cursorTurn {
	matches, err := filepath.Glob(filepath.Join(convDir, "subagents", "*.jsonl"))
	if err != nil || len(matches) == 0 {
		return cursorTurn{}
	}
	var best cursorTurn
	for _, p := range matches {
		turn := readCursorTranscript(p)
		if !turn.ok {
			continue
		}
		if nowMs-turn.lastSeenMs >= cursorStaleWindow.Milliseconds() {
			continue
		}
		if !best.ok || cursorTurnRank(turn) > cursorTurnRank(best) ||
			(cursorTurnRank(turn) == cursorTurnRank(best) && turn.lastSeenMs > best.lastSeenMs) {
			best = turn
		}
	}
	return best
}

func cursorTurnRank(t cursorTurn) int {
	if !t.ok {
		return 0
	}
	if t.open && hasCursorApprovalTool(t.tools) && !t.writing {
		return 3
	}
	if t.open {
		return 2
	}
	return 1
}

// mergeCursorTurn 让还在跑或卡在审批上的子代理盖过父会话已结束的轮次。
func mergeCursorTurn(parent, sub cursorTurn) cursorTurn {
	if !sub.ok {
		return parent
	}
	if !parent.ok {
		return sub
	}
	if cursorTurnRank(sub) > cursorTurnRank(parent) {
		if parent.startedAt > 0 && (sub.startedAt == 0 || parent.startedAt < sub.startedAt) {
			sub.startedAt = parent.startedAt
		}
		return sub
	}
	if sub.lastSeenMs > parent.lastSeenMs {
		parent.lastSeenMs = sub.lastSeenMs
	}
	return parent
}

func mapCursorTurn(turn cursorTurn, termBusy bool, nowMs int64) (protocol.SessionState, bool) {
	if !turn.ok || turn.lastSeenMs == 0 {
		return "", false
	}
	if nowMs-turn.lastSeenMs >= cursorStaleWindow.Milliseconds() {
		return "", false
	}
	if !turn.open {
		return protocol.StateWaitingForInput, true
	}
	if !turn.writing && hasCursorApprovalTool(turn.tools) && !termBusy &&
		nowMs-turn.lastSeenMs >= cursorApprovalQuiet.Milliseconds() {
		return protocol.StateWaitingForApproval, true
	}
	return protocol.StateActive, true
}

func hasCursorApprovalTool(tools []string) bool {
	for _, name := range tools {
		if cursorApprovalTools[name] {
			return true
		}
	}
	return false
}

type cursorTranscriptLine struct {
	Role    string `json:"role"`
	Type    string `json:"type"`
	Status  string `json:"status"`
	Message struct {
		Content []struct {
			Type string `json:"type"`
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func readCursorTranscript(path string) cursorTurn {
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return cursorTurn{}
	}
	turn := cursorTurn{
		lastSeenMs: fi.ModTime().UnixMilli(),
		startedAt:  fi.ModTime().UnixMilli(),
	}
	if started, ok := cursorStartedAt(path); ok {
		turn.startedAt = started
	}
	data, err := readFileTail(path, cursorTranscriptTail)
	if err != nil || len(data) == 0 {
		return cursorTurn{}
	}
	droppedHead := fi.Size() > cursorTranscriptTail
	parts := bytes.Split(data, []byte("\n"))
	if droppedHead && len(parts) > 0 {
		parts = parts[1:]
	}
	if len(parts) > 0 && len(parts[len(parts)-1]) == 0 {
		parts = parts[:len(parts)-1]
	}

	var lines []cursorTranscriptLine
	incomplete := false
	for i, raw := range parts {
		raw = bytes.TrimSpace(raw)
		if len(raw) == 0 {
			continue
		}
		var line cursorTranscriptLine
		if json.Unmarshal(raw, &line) != nil {
			if i == len(parts)-1 {
				incomplete = true
			}
			continue
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 && !incomplete {
		return cursorTurn{}
	}
	turn.ok = true
	turn.writing = incomplete
	if len(lines) == 0 {
		turn.open = true
		return turn
	}
	last := lines[len(lines)-1]
	if last.Type == "turn_ended" && !incomplete {
		turn.open = false
		return turn
	}
	turn.open = true
	for i := len(lines) - 1; i >= 0; i-- {
		if lines[i].Role != "assistant" {
			continue
		}
		for _, c := range lines[i].Message.Content {
			if c.Type == "tool_use" && c.Name != "" {
				turn.tools = append(turn.tools, c.Name)
			}
		}
		break
	}
	return turn
}

var cursorTimestampRe = regexp.MustCompile(`<timestamp>([^<]+)</timestamp>`)

func cursorStartedAt(path string) (int64, bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, false
	}
	defer f.Close()
	buf := make([]byte, 4096)
	n, _ := f.Read(buf)
	if n == 0 {
		return 0, false
	}
	m := cursorTimestampRe.FindSubmatch(buf[:n])
	if m == nil {
		return 0, false
	}
	return parseCursorTimestamp(string(m[1]))
}

func parseCursorTimestamp(raw string) (int64, bool) {
	raw = strings.TrimSpace(raw)
	loc := time.Local
	if i := strings.LastIndex(raw, "("); i >= 0 {
		tz := strings.Trim(raw[i:], "() \t")
		raw = strings.TrimSpace(raw[:i])
		if off, ok := parseUTCOffset(tz); ok {
			loc = time.FixedZone(tz, off)
		}
	}
	t, err := time.ParseInLocation("Monday, Jan 2, 2006, 3:04 PM", raw, loc)
	if err != nil {
		return 0, false
	}
	return t.UnixMilli(), true
}

func parseUTCOffset(tz string) (int, bool) {
	if !strings.HasPrefix(tz, "UTC") {
		return 0, false
	}
	rest := strings.TrimPrefix(tz, "UTC")
	if rest == "" || rest == "Z" {
		return 0, true
	}
	sign := 1
	switch rest[0] {
	case '+':
	case '-':
		sign = -1
	default:
		return 0, false
	}
	rest = rest[1:]
	hour, min := 0, 0
	if i := strings.IndexByte(rest, ':'); i >= 0 {
		if _, err := parseSmallInt(rest[:i], &hour); err != nil {
			return 0, false
		}
		if _, err := parseSmallInt(rest[i+1:], &min); err != nil {
			return 0, false
		}
	} else if _, err := parseSmallInt(rest, &hour); err != nil {
		return 0, false
	}
	return sign * (hour*3600 + min*60), true
}

func parseSmallInt(s string, out *int) (int, error) {
	n := 0
	if s == "" {
		return 0, os.ErrInvalid
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, os.ErrInvalid
		}
		n = n*10 + int(s[i]-'0')
	}
	*out = n
	return n, nil
}

// cursorRemoteWorkspaces 从最近的 exthost 日志里抽出 workspacePaths，键是 project slug。
func cursorRemoteWorkspaces(serverDir string) map[string]string {
	logsRoot := filepath.Join(serverDir, "data", "logs")
	sessions, err := os.ReadDir(logsRoot)
	if err != nil {
		return nil
	}
	type sess struct {
		path string
		mod  int64
	}
	var found []sess
	for _, e := range sessions {
		if !e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		found = append(found, sess{path: filepath.Join(logsRoot, e.Name()), mod: info.ModTime().UnixNano()})
	}
	sort.Slice(found, func(i, j int) bool { return found[i].mod > found[j].mod })
	if len(found) > 2 {
		found = found[:2]
	}
	out := map[string]string{}
	for _, s := range found {
		pattern := filepath.Join(s.path, "exthost*", "anysphere.cursor-agent-exec", "Cursor Agent Exec.log")
		matches, err := filepath.Glob(pattern)
		if err != nil {
			continue
		}
		for _, logPath := range matches {
			data := readFileHead(logPath, cursorLogHead)
			for _, p := range extractWorkspacePaths(data) {
				slug := cursorProjectSlug(p)
				if slug == "" {
					continue
				}
				if _, ok := out[slug]; !ok {
					out[slug] = p
				}
			}
		}
	}
	return out
}

func readFileHead(path string, n int64) []byte {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	buf := make([]byte, n)
	got, _ := f.Read(buf)
	return buf[:got]
}

func extractWorkspacePaths(data []byte) []string {
	needle := []byte(`"workspacePaths":`)
	var out []string
	rest := data
	for {
		i := bytes.Index(rest, needle)
		if i < 0 {
			return out
		}
		rest = rest[i+len(needle):]
		j := bytes.IndexByte(rest, ']')
		if j < 0 {
			return out
		}
		var paths []string
		if json.Unmarshal(bytes.TrimSpace(rest[:j+1]), &paths) == nil {
			out = append(out, paths...)
		}
		rest = rest[j+1:]
	}
}

func cwdFromTerminals(projectDir, slug string) string {
	entries, err := os.ReadDir(filepath.Join(projectDir, "terminals"))
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		cwd := terminalCWD(filepath.Join(projectDir, "terminals", e.Name()))
		if cwd == "" {
			continue
		}
		if resolved := walkToSlug(cwd, slug); resolved != "" {
			return resolved
		}
	}
	return ""
}

func walkToSlug(cwd, slug string) string {
	dir := cwd
	for {
		if cursorProjectSlug(dir) == slug {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func terminalCWD(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	buf := make([]byte, 1024)
	n, _ := f.Read(buf)
	for _, line := range strings.Split(string(buf[:n]), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "cwd:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "cwd:"))
		}
	}
	return ""
}

// terminalsBusy 判断自 sinceMs 以来有没有终端文件被写过。
// 只看 mtime，不看进程是否还活着：项目里常年挂着的 dev server 不该把新的确认弹窗盖成「工作中」。
// 命令一开始就会创建/更新终端文件，mtime 不早于触发它的那条 transcript。
func terminalsBusy(projectDir string, sinceMs int64) bool {
	if sinceMs <= 0 {
		return false
	}
	dir := filepath.Join(projectDir, "terminals")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".txt") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().UnixMilli() >= sinceMs-500 {
			return true
		}
	}
	return false
}
