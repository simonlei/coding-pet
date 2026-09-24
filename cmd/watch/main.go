// coding-pet-watch 是一个独立的调试工具：只读地监控本机所有 coding agent
// （CodeBuddy CLI / CodeBuddy IDE / IDE 远程 / WorkBuddy / Claude Code）
// session 的状态变化，实时打到控制台并同时写入日志文件。
//
// 与 coding-pet-agent 的区别：
//   - 不需要 --server，不上报、不推送 webhook（纯本地观测）；
//   - 每一轮采集都与上一轮做 diff，只打印「变化」，便于长时间挂着看状态跃迁；
//   - 附带跑一遍 notifier.Detector，打印「按当前规则本来会推送哪些通知」，
//     用于排查"该通知没通知 / 不该通知却通知了"这类问题。
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/collector"
	"github.com/simonlei/coding-pet-dashboard/internal/notifier"
	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

var version = "dev"

func main() {
	interval := flag.Duration("interval", time.Second, "采集间隔")
	logPath := flag.String("log", "coding-pet-watch.log", `日志文件路径（设为 "" 关闭文件日志）`)
	toolFilter := flag.String("tool", "", "只观测这些工具，逗号分隔，如 codebuddy,claude_code,codebuddy_ide,codebuddy_ide_remote,workbuddy")
	once := flag.Bool("once", false, "只采集一次、打印完整快照后退出")
	verbose := flag.Bool("verbose", false, "同时打印 context_tokens / last_activity / heartbeat 等字段变化")
	summary := flag.Duration("summary", time.Minute, "每隔多久打印一次状态汇总（0 关闭）")
	noColor := flag.Bool("no-color", false, "关闭 ANSI 颜色")
	noDetector := flag.Bool("no-detector", false, "不跑 notifier.Detector（默认会跑并打印 WOULD-NOTIFY）")
	explain := flag.Bool("explain", false, "打印每个 session 的状态判定链路（pidfile→JSONL→运行日志→subagent），排查状态判错时用")
	explainEvery := flag.Duration("explain-every", 0, "常驻模式下每隔多久重打一次判定链路（0 只在启动时打一次，需配合 --explain）")
	showVersion := flag.Bool("version", false, "打印版本后退出")

	flag.Usage = func() {
		out := flag.CommandLine.Output()
		fmt.Fprintf(out, `coding-pet-watch — 只读监控本机 coding agent 的状态变化（调试用）。

用法：
  %s [flags]                常驻：每 interval 采集一次，打印状态变化
  %s --once                 打印一次当前完整快照后退出
  %s --once --explain       打印快照 + 每个 session 的判定链路（排查状态判错）
  %s --explain --explain-every 10s
                            常驻并定期重打判定链路（观察状态机怎么变）

输出行首标记：
  NEW    新出现的 session
  STATE  状态跃迁（含在旧状态停留了多久）
  GONE   session 从采集结果中消失
  FIELD  非状态字段变化（需 --verbose）
  NOTIFY notifier 规则判定「本来会推送」的事件
  WARN   可疑现象（如同一 session_id 被多个工具同时采集到）

Flags:
`, os.Args[0], os.Args[0], os.Args[0], os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	lg, err := newLogger(*logPath, !*noColor)
	if err != nil {
		fmt.Fprintf(os.Stderr, "打开日志文件失败: %v\n", err)
		os.Exit(1)
	}
	defer lg.Close()

	hostname, _ := os.Hostname()
	if hostname == "" {
		hostname = "unknown"
	}
	tools := parseToolFilter(*toolFilter)

	lg.raw("=== coding-pet-watch %s 启动 host=%s pid=%d interval=%s tools=%s log=%s ===",
		version, hostname, os.Getpid(), *interval, filterDesc(tools), lg.PathDesc())

	c := collector.New()

	// 首轮：打印完整快照作为基线。
	sessions := filterSessions(c.CollectSessions(), tools)
	now := time.Now()
	lg.raw("初始快照：%d 个 session", len(sessions))
	for _, line := range snapshotTable(sessions, now) {
		lg.raw("%s", line)
	}
	if *explain {
		for _, line := range explainLines(sessions, tools) {
			lg.raw("%s", line)
		}
	}
	if *once {
		return
	}

	state := map[string]*watchEntry{}
	// 首轮结果直接作为基线写入，不当成 NEW 打印。
	applyDiff(state, sessions, now, *verbose)

	var det *notifier.Detector
	if !*noDetector {
		det = notifier.NewDetector(hostname)
		det.ReconcileAt(sessions, now)
	}

	warned := map[string]bool{}
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	var summaryTicker *time.Ticker
	var summaryC <-chan time.Time
	if *summary > 0 {
		summaryTicker = time.NewTicker(*summary)
		defer summaryTicker.Stop()
		summaryC = summaryTicker.C
	}

	var explainC <-chan time.Time
	if *explain && *explainEvery > 0 {
		explainTicker := time.NewTicker(*explainEvery)
		defer explainTicker.Stop()
		explainC = explainTicker.C
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case <-ticker.C:
			sessions = filterSessions(c.CollectSessions(), tools)
			now = time.Now()

			for _, w := range append(dupSessionIDs(sessions), dupWorkspaceSessions(sessions)...) {
				if warned[w] {
					continue
				}
				warned[w] = true
				lg.event("WARN", colorRed, "%s", w)
			}

			for _, ch := range applyDiff(state, sessions, now, *verbose) {
				lg.event(ch.Kind, stateColor(ch.Info.State), "%s", ch.Text(now))
			}

			if det != nil {
				for _, evt := range det.ReconcileAt(sessions, now) {
					lg.event("NOTIFY", colorCyan, "%s  session=%s", notifier.FormatMessage(evt), shortID(evt.SessionID))
				}
			}

		case <-explainC:
			for _, line := range explainLines(sessions, tools) {
				lg.raw("%s", line)
			}

		case <-summaryC:
			lg.raw("汇总：%s", summaryLine(sessions))

		case s := <-sig:
			lg.raw("=== 收到信号 %s，退出。最后一轮 %d 个 session：%s ===", s, len(sessions), summaryLine(sessions))
			return
		}
	}
}

// explainLines 渲染判定链路。CLI session 走 collector 的完整链路诊断（含运行
// 日志 transition 原文）；其他工具只给一行说明（它们没有这条链路）。
func explainLines(sessions []protocol.SessionInfo, tools map[string]bool) []string {
	diags := collector.DiagnoseCLISessions(6)
	diags = append(diags, collector.DiagnoseOther(sessions)...)

	// 应用 --tool 过滤：DiagnoseCLISessions 独立扫盘，不经过 filterSessions。
	if tools != nil {
		kept := diags[:0]
		for _, d := range diags {
			if tools[string(d.Tool)] {
				kept = append(kept, d)
			}
		}
		diags = kept
	}

	if len(diags) == 0 {
		return []string{"判定链路：无可诊断的 session（CodeBuddy CLI 无 pidfile，其他工具也没采到）"}
	}

	lines := []string{fmt.Sprintf("判定链路（%d 个 session）：", len(diags))}
	for _, d := range diags {
		lines = append(lines, fmt.Sprintf("  ── %s  %s  session=%s pid=%d  →  %s",
			d.Tool, tildeCWD(d.CWD), shortID(d.SessionID), d.PID, d.State))
		if d.Note != "" {
			lines = append(lines, "       "+d.Note)
		}
		for _, s := range d.Steps {
			line := fmt.Sprintf("     %-10s %s", s.Name, s.Value)
			if s.Detail != "" {
				line += "\n                  " + s.Detail
			}
			lines = append(lines, line)
		}
		if len(d.Transitions) > 0 {
			lines = append(lines, "     最近 transition（旧→新）：")
			for _, t := range d.Transitions {
				lines = append(lines, "       "+t)
			}
		}
	}
	return lines
}

// ---------- diff ----------

// watchEntry 记录单个 session 上一轮的观测结果。
type watchEntry struct {
	info       protocol.SessionInfo
	stateSince time.Time
}

// change 是一条待打印的变化。
type change struct {
	Kind string // NEW / STATE / GONE / FIELD
	Info protocol.SessionInfo
	Prev protocol.SessionState
	Held time.Duration
	Note string
}

// Text 渲染成人类可读的一行。
func (ch change) Text(now time.Time) string {
	head := fmt.Sprintf("%-22s %-20s %s", string(ch.Info.State), string(ch.Info.Tool), tildeCWD(ch.Info.CWD))
	switch ch.Kind {
	case "STATE":
		return fmt.Sprintf("%s  (%s → %s，前一状态持续 %s)  session=%s pid=%d",
			head, ch.Prev, ch.Info.State, shortDur(ch.Held), shortID(ch.Info.SessionID), ch.Info.PID)
	case "GONE":
		return fmt.Sprintf("%s  (消失前状态 %s，持续 %s)  session=%s pid=%d",
			head, ch.Info.State, shortDur(ch.Held), shortID(ch.Info.SessionID), ch.Info.PID)
	case "FIELD":
		return fmt.Sprintf("%s  %s  session=%s", head, ch.Note, shortID(ch.Info.SessionID))
	default: // NEW
		return fmt.Sprintf("%s  session=%s pid=%d %s",
			head, shortID(ch.Info.SessionID), ch.Info.PID, detailSuffix(ch.Info, now))
	}
}

// sessionKey 用 tool + session_id 做主键：不同工具的 session_id 理论上可能撞车，
// 分开跟踪才不会把两个 session 的状态互相覆盖。
func sessionKey(s protocol.SessionInfo) string {
	return string(s.Tool) + "|" + s.SessionID
}

// applyDiff 用本轮 sessions 更新 state，并返回本轮的变化列表。
// 纯函数式语义：除 state 外不碰任何全局状态，方便测试。
func applyDiff(state map[string]*watchEntry, sessions []protocol.SessionInfo, now time.Time, verbose bool) []change {
	var changes []change
	seen := make(map[string]struct{}, len(sessions))

	for _, s := range sessions {
		k := sessionKey(s)
		seen[k] = struct{}{}
		e, ok := state[k]
		if !ok {
			state[k] = &watchEntry{info: s, stateSince: now}
			changes = append(changes, change{Kind: "NEW", Info: s})
			continue
		}
		if e.info.State != s.State {
			changes = append(changes, change{
				Kind: "STATE",
				Info: s,
				Prev: e.info.State,
				Held: now.Sub(e.stateSince),
			})
			e.stateSince = now
		} else if verbose {
			if note := fieldNote(e.info, s); note != "" {
				changes = append(changes, change{Kind: "FIELD", Info: s, Note: note})
			}
		}
		e.info = s
	}

	for k, e := range state {
		if _, ok := seen[k]; ok {
			continue
		}
		changes = append(changes, change{Kind: "GONE", Info: e.info, Held: now.Sub(e.stateSince)})
		delete(state, k)
	}

	sort.SliceStable(changes, func(i, j int) bool { return changes[i].Kind < changes[j].Kind })
	return changes
}

// fieldNote 描述两次观测之间除 state 外的字段变化；无变化返回 ""。
func fieldNote(oldS, newS protocol.SessionInfo) string {
	var parts []string
	if oldS.ContextTokens != newS.ContextTokens {
		parts = append(parts, fmt.Sprintf("ctx %s→%s", tokens(oldS.ContextTokens), tokens(newS.ContextTokens)))
	}
	if oldS.LastActivity != newS.LastActivity {
		parts = append(parts, "last_activity 更新")
	}
	if oldS.LastHeartbeat != newS.LastHeartbeat {
		parts = append(parts, "heartbeat 更新")
	}
	if oldS.PID != newS.PID {
		parts = append(parts, fmt.Sprintf("pid %d→%d", oldS.PID, newS.PID))
	}
	if oldS.Version != newS.Version {
		parts = append(parts, fmt.Sprintf("version %q→%q", oldS.Version, newS.Version))
	}
	return strings.Join(parts, ", ")
}

// dupSessionIDs 找出被多个工具同时采集到的 session_id。notifier.Detector 只按
// session_id 建索引，这种撞车会让状态互相覆盖，值得单独告警。
func dupSessionIDs(sessions []protocol.SessionInfo) []string {
	byID := map[string]map[string]struct{}{}
	for _, s := range sessions {
		if byID[s.SessionID] == nil {
			byID[s.SessionID] = map[string]struct{}{}
		}
		byID[s.SessionID][string(s.Tool)] = struct{}{}
	}
	var out []string
	for id, tools := range byID {
		if len(tools) < 2 {
			continue
		}
		names := make([]string, 0, len(tools))
		for t := range tools {
			names = append(names, t)
		}
		sort.Strings(names)
		out = append(out, fmt.Sprintf("session_id %s 同时来自多个工具 [%s]，Detector 只按 session_id 索引，状态会互相覆盖",
			shortID(id), strings.Join(names, " ")))
	}
	sort.Strings(out)
	return out
}

// dupWorkspaceSessions 找出同一工具在同一 workspace（cwd）下同时报了多条会话的情况。
// 在 dashboard 上就是「同一项目多张卡片」的重复条目：通常是工具留下了旧会话记录
// （如 IDE 新建/切换对话后旧 conversation 仍在库里），而采集端没按 workspace 去重。
func dupWorkspaceSessions(sessions []protocol.SessionInfo) []string {
	byKey := map[string][]string{} // tool|cwd → session_id 列表
	for _, s := range sessions {
		key := string(s.Tool) + "|" + normWorkspace(s.CWD)
		byKey[key] = append(byKey[key], s.SessionID)
	}
	var out []string
	for key, ids := range byKey {
		if len(ids) < 2 {
			continue
		}
		sort.Strings(ids)
		tool, cwd, _ := strings.Cut(key, "|")
		out = append(out, fmt.Sprintf("工具 %s 在同一 workspace %s 上报了 %d 条会话 [%s]，采集端应按 workspace 只保留当前会话",
			tool, cwd, len(ids), strings.Join(shortIDs(ids), " ")))
	}
	sort.Strings(out)
	return out
}

// normWorkspace 归一化 workspace 路径（分隔符统一为斜杠 + 大小写不敏感平台转小写）。
func normWorkspace(cwd string) string {
	k := filepath.ToSlash(cwd)
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		return strings.ToLower(k)
	}
	return k
}

func shortIDs(ids []string) []string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, shortID(id))
	}
	return out
}

// ---------- 渲染 ----------

// snapshotTable 渲染完整快照（表头 + 每行一个 session，按 tool/cwd 排序）。
func snapshotTable(sessions []protocol.SessionInfo, now time.Time) []string {
	if len(sessions) == 0 {
		return []string{"  (无 session)"}
	}
	sorted := make([]protocol.SessionInfo, len(sessions))
	copy(sorted, sessions)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].Tool != sorted[j].Tool {
			return sorted[i].Tool < sorted[j].Tool
		}
		return sorted[i].CWD < sorted[j].CWD
	})
	lines := []string{fmt.Sprintf("  %-22s %-20s %-10s %8s %8s %8s %8s  %s",
		"STATE", "TOOL", "SESSION", "PID", "CTX", "ACT", "HB", "CWD")}
	for _, s := range sorted {
		lines = append(lines, fmt.Sprintf("  %-22s %-20s %-10s %8d %8s %8s %8s  %s",
			s.State, s.Tool, shortID(s.SessionID), s.PID,
			tokens(s.ContextTokens), ageMs(now, s.LastActivity), ageMs(now, s.LastHeartbeat),
			tildeCWD(s.CWD)))
	}
	return lines
}

// detailSuffix 给 NEW 行补充 ctx / 活跃时间等细节。
func detailSuffix(s protocol.SessionInfo, now time.Time) string {
	return fmt.Sprintf("kind=%s ctx=%s act=%s hb=%s",
		orDash(string(s.Kind)), tokens(s.ContextTokens), ageMs(now, s.LastActivity), ageMs(now, s.LastHeartbeat))
}

// summaryLine 汇总各状态计数与各工具计数。
func summaryLine(sessions []protocol.SessionInfo) string {
	byState := map[protocol.SessionState]int{}
	byTool := map[protocol.SessionTool]int{}
	for _, s := range sessions {
		byState[s.State]++
		byTool[s.Tool]++
	}
	order := []protocol.SessionState{
		protocol.StateActive, protocol.StateWaitingForInput, protocol.StateWaitingForApproval,
		protocol.StateTerminated, protocol.StateUnknown,
	}
	var parts []string
	for _, st := range order {
		if byState[st] > 0 {
			parts = append(parts, fmt.Sprintf("%s=%d", st, byState[st]))
		}
	}
	toolNames := make([]string, 0, len(byTool))
	for t := range byTool {
		toolNames = append(toolNames, string(t))
	}
	sort.Strings(toolNames)
	var toolParts []string
	for _, t := range toolNames {
		toolParts = append(toolParts, fmt.Sprintf("%s=%d", t, byTool[protocol.SessionTool(t)]))
	}
	return fmt.Sprintf("total=%d [%s] [%s]", len(sessions), strings.Join(parts, " "), strings.Join(toolParts, " "))
}

func tildeCWD(cwd string) string {
	if cwd == "" {
		return "-"
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return cwd
	}
	if cwd == home {
		return "~"
	}
	if strings.HasPrefix(cwd, home+string(filepath.Separator)) {
		return "~" + cwd[len(home):]
	}
	return cwd
}

func shortID(id string) string {
	if len(id) <= 8 {
		return orDash(id)
	}
	return id[:8]
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func tokens(n int64) string {
	switch {
	case n <= 0:
		return "-"
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	default:
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
}

// ageMs 把 Unix ms 时间戳渲染成「距今多久」。
func ageMs(now time.Time, ms int64) string {
	if ms <= 0 {
		return "-"
	}
	return shortDur(now.Sub(time.UnixMilli(ms)))
}

func shortDur(d time.Duration) string {
	neg := ""
	if d < 0 {
		neg, d = "-", -d
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%s%dms", neg, d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%s%.1fs", neg, d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%s%dm%02ds", neg, int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%s%dh%02dm", neg, int(d.Hours()), int(d.Minutes())%60)
	}
}

// ---------- 过滤 ----------

func parseToolFilter(raw string) map[string]bool {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	out := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out[p] = true
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func filterSessions(sessions []protocol.SessionInfo, tools map[string]bool) []protocol.SessionInfo {
	if tools == nil {
		return sessions
	}
	out := make([]protocol.SessionInfo, 0, len(sessions))
	for _, s := range sessions {
		if tools[string(s.Tool)] {
			out = append(out, s)
		}
	}
	return out
}

func filterDesc(tools map[string]bool) string {
	if tools == nil {
		return "all"
	}
	names := make([]string, 0, len(tools))
	for t := range tools {
		names = append(names, t)
	}
	sort.Strings(names)
	return strings.Join(names, ",")
}

// ---------- 日志 ----------

const (
	colorGreen   = "32"
	colorYellow  = "33"
	colorMagenta = "35"
	colorGray    = "90"
	colorRed     = "31"
	colorCyan    = "36"
)

func stateColor(s protocol.SessionState) string {
	switch s {
	case protocol.StateActive:
		return colorGreen
	case protocol.StateWaitingForInput:
		return colorYellow
	case protocol.StateWaitingForApproval:
		return colorMagenta
	case protocol.StateTerminated:
		return colorGray
	default:
		return colorRed
	}
}

// logger 同时写控制台（可带颜色）与日志文件（纯文本）。
type logger struct {
	f     *os.File
	path  string
	color bool
}

// newLogger 打开日志文件（path 为空表示只输出控制台）。颜色在非 TTY 下自动关闭。
func newLogger(path string, wantColor bool) (*logger, error) {
	l := &logger{color: wantColor && isTTY(os.Stdout)}
	if path == "" {
		return l, nil
	}
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return nil, err
	}
	abs, absErr := filepath.Abs(path)
	if absErr != nil {
		abs = path
	}
	l.f, l.path = f, abs
	return l, nil
}

func (l *logger) PathDesc() string {
	if l.path == "" {
		return "(disabled)"
	}
	return l.path
}

// event 打印一条带标记的变化行。
func (l *logger) event(kind, color, format string, args ...any) {
	l.write(color, fmt.Sprintf("%-6s %s", kind, fmt.Sprintf(format, args...)))
}

// raw 打印一条不带标记的普通行（快照、汇总、启动信息）。
func (l *logger) raw(format string, args ...any) {
	l.write("", fmt.Sprintf(format, args...))
}

func (l *logger) write(color, msg string) {
	line := time.Now().Format("2006-01-02 15:04:05.000") + " " + msg
	if l.color && color != "" {
		fmt.Printf("\033[%sm%s\033[0m\n", color, line)
	} else {
		fmt.Println(line)
	}
	if l.f != nil {
		fmt.Fprintln(l.f, line)
	}
}

func (l *logger) Close() {
	if l.f != nil {
		_ = l.f.Close()
	}
}

func isTTY(f *os.File) bool {
	st, err := f.Stat()
	if err != nil {
		return false
	}
	return st.Mode()&os.ModeCharDevice != 0
}
