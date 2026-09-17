package collector

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// 本文件提供只读的诊断入口，把 CodeBuddy CLI session 的状态判定链路
// 逐环暴露出来，供 coding-pet-watch --explain 打印。
//
// 判定链路（与 collector.collectOne 完全一致，此处不改变任何判定）：
//
//	pidfile(heartbeat/alive) → JSONL 末尾记录 → 运行日志状态机校正 → subagent 检查 → 最终状态
//
// 当出现「明明在等审批，dashboard/watch 却显示 active」这类问题时，用它定位
// 是哪一环把状态吞掉了。

// DiagStep 是判定链路中的一环。
type DiagStep struct {
	Name   string // 环节名，如 "pidfile" / "jsonl" / "runlog" / "reconcile"
	Value  string // 该环节的结论
	Detail string // 支撑数据（路径、字段、时间差等）
}

// Diagnosis 是单个 session 的完整判定链路。
type Diagnosis struct {
	Tool      protocol.SessionTool
	SessionID string
	PID       int
	CWD       string
	State     protocol.SessionState // 最终判定，与 CollectSessions 输出一致
	Steps     []DiagStep

	// Transitions 是运行日志中该 session 最近若干条状态机 transition 原文
	// （从旧到新）。为空说明日志里根本找不到该 session 的 transition。
	Transitions []string

	// Note 用于非 CLI 工具：这些工具走各自专用采集器，没有 JSONL+runlog 链路。
	Note string
}

// DiagnoseCLISessions 诊断所有 CodeBuddy CLI session（有 pidfile 的那批）。
// 包含被判定为 terminated 的 session —— 排查「session 压根没出现」时需要看到它们。
func DiagnoseCLISessions(maxTransitions int) []Diagnosis {
	var out []Diagnosis
	for _, h := range buddyHomes() {
		for _, pf := range readPIDFilesFrom(h.dir) {
			out = append(out, diagnoseOne(h, pf, maxTransitions, time.Now()))
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CWD < out[j].CWD })
	return out
}

// DiagnoseOther 为非 CLI 工具的 session 生成一条说明性诊断（无链路可拆）。
func DiagnoseOther(sessions []protocol.SessionInfo) []Diagnosis {
	var out []Diagnosis
	for _, s := range sessions {
		if s.Tool == protocol.ToolCodeBuddy {
			continue // CLI 由 DiagnoseCLISessions 覆盖，避免重复
		}
		out = append(out, Diagnosis{
			Tool:      s.Tool,
			SessionID: s.SessionID,
			PID:       s.PID,
			CWD:       s.CWD,
			State:     s.State,
			Note:      noteForTool(s.Tool),
		})
	}
	return out
}

func noteForTool(t protocol.SessionTool) string {
	switch t {
	case protocol.ToolClaudeCode:
		return "Claude Code：状态取自 ~/.claude/projects 的 JSONL 尾部，无 CodeBuddy 运行日志状态机可比对"
	case protocol.ToolCodeBuddyIDE:
		return "CodeBuddy IDE：状态取自扩展 SQLite(state.vscdb) 的 session 值 + MQ runtime，不读 JSONL/运行日志"
	case protocol.ToolCodeBuddyIDERemote:
		return "CodeBuddy IDE 远程：状态取自 exthost 日志的 run start/end 配对，不读 JSONL/运行日志"
	case protocol.ToolWorkBuddy:
		return "WorkBuddy：状态直读 ~/.workbuddy/workbuddy.db 的 sessions.status 字段"
	default:
		return "该工具无链路诊断"
	}
}

// diagnoseOne 复刻 collectOne 的判定顺序，同时把每一步的依据记下来。
func diagnoseOne(home buddyHome, pf PIDFile, maxTransitions int, now time.Time) Diagnosis {
	d := Diagnosis{
		Tool:      home.tool,
		SessionID: pf.SessionID,
		PID:       pf.PID,
		CWD:       pf.CWD,
	}
	nowMs := now.UnixMilli()

	hbAge := time.Duration(nowMs-pf.LastHeartbeat) * time.Millisecond
	alive := isProcessAlive(pf.PID)
	d.Steps = append(d.Steps, DiagStep{
		Name:   "pidfile",
		Value:  fmt.Sprintf("heartbeat %s 前, 进程存活=%t", humanDur(hbAge), alive),
		Detail: filepath.Join(home.dir, "sessions", fmt.Sprintf("%d.json", pf.PID)),
	})

	// 1/2. 心跳与存活检查
	if nowMs-pf.LastHeartbeat > 60_000 {
		d.State = protocol.StateTerminated
		d.Steps = append(d.Steps, DiagStep{Name: "结论", Value: "terminated", Detail: "心跳超过 60s，直接判死，后续环节全部跳过"})
		return d
	}
	if !alive {
		d.State = protocol.StateTerminated
		d.Steps = append(d.Steps, DiagStep{Name: "结论", Value: "terminated", Detail: "进程不存在，后续环节全部跳过"})
		return d
	}

	// 3. JSONL
	jsonlPath, found := findSessionJSONLIn(home.dir, pf.SessionID)
	if !found {
		d.State = protocol.StateActive
		d.Steps = append(d.Steps, DiagStep{
			Name:   "jsonl",
			Value:  "未找到 → 保守判 active",
			Detail: filepath.Join(home.dir, "projects", "*", pf.SessionID+".jsonl"),
		})
		d.Steps = append(d.Steps, DiagStep{Name: "结论", Value: "active", Detail: "JSONL 未落盘，视为刚启动的新 session"})
		return d
	}

	var jsonlAge time.Duration
	if st, err := os.Stat(jsonlPath); err == nil {
		jsonlAge = now.Sub(st.ModTime())
	}
	jsonlState := DetermineStateFromJSONL(jsonlPath)

	if jsonlAge < 5*time.Second {
		d.Steps = append(d.Steps, DiagStep{
			Name:   "jsonl",
			Value:  fmt.Sprintf("mtime %s 前 (<5s) → 短路判 active，不看末尾记录", humanDur(jsonlAge)),
			Detail: jsonlPath,
		})
	} else {
		entryDesc := "读取失败/无有效记录"
		if e, err := readLastMeaningfulEntry(jsonlPath); err == nil && e != nil {
			entryDesc = fmt.Sprintf("type=%s name=%s role=%s status=%s dangerouslyDisableSandbox=%t",
				dash(e.Type), dash(e.Name), dash(e.Role), dash(e.Status), hasDangerouslyDisableSandbox(e.Arguments))
		}
		d.Steps = append(d.Steps, DiagStep{
			Name:   "jsonl",
			Value:  fmt.Sprintf("mtime %s 前，末尾记录 → %s", humanDur(jsonlAge), jsonlState),
			Detail: entryDesc + "  @ " + jsonlPath,
		})
	}

	// 4. 运行日志状态机校正
	runState, runSource := lastRunStateInWithSource(home.dir, pf.SessionID)
	reconciled := reconcileWithRunState(jsonlState, runState)
	runValue := "运行日志中找不到该 session 的 transition → 保留 JSONL 判定"
	if runState != "" {
		runValue = "最后一条 transition to=" + runState
	}
	d.Steps = append(d.Steps, DiagStep{Name: "runlog", Value: runValue, Detail: dash(runSource)})

	reconcileNote := "runState 未命中校正规则，保留 JSONL 判定"
	switch {
	case runState == "waiting_for_permission":
		reconcileNote = "runState=waiting_for_permission，精确命中 → 强制 waiting_for_approval"
	case runningRunStates[runState] && jsonlState == protocol.StateWaitingForApproval:
		reconcileNote = "runState 属「运行中」集合(" + runState + ")，把 JSONL 的 waiting_for_approval 纠正成 active ← 若你正在等审批，问题极可能出在这一步"
	}
	d.Steps = append(d.Steps, DiagStep{
		Name:   "reconcile",
		Value:  fmt.Sprintf("%s → %s", jsonlState, reconciled),
		Detail: reconcileNote,
	})

	// 5. subagent 检查（仅在 active 时生效）
	state := reconciled
	if state == protocol.StateActive {
		sub := checkSubagentsForApproval(jsonlPath)
		if sub {
			state = protocol.StateWaitingForApproval
		}
		d.Steps = append(d.Steps, DiagStep{
			Name:   "subagent",
			Value:  fmt.Sprintf("有 subagent 在等审批=%t", sub),
			Detail: strings.TrimSuffix(jsonlPath, ".jsonl") + "/subagents",
		})
	}

	d.State = state
	d.Steps = append(d.Steps, DiagStep{Name: "结论", Value: string(state)})

	if maxTransitions > 0 {
		d.Transitions = recentTransitionsIn(home.dir, pf.SessionID, maxTransitions)
	}
	return d
}

// recentTransitionsIn 返回该 session 最近 n 条状态机 transition 行（从旧到新，
// 已裁掉过长内容）。它比单个 lastRunState 有用得多：能看出状态机到底有没有
// 迁移到 waiting_for_permission，还是一直停在 tool_executing。
func recentTransitionsIn(baseDir, sessionID string, n int) []string {
	if sessionID == "" || n <= 0 {
		return nil
	}
	files, err := filepath.Glob(filepath.Join(baseDir, "logs", "*", "*.log"))
	if err != nil || len(files) == 0 {
		return nil
	}
	type fileMod struct {
		path string
		mod  int64
	}
	mods := make([]fileMod, 0, len(files))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			mods = append(mods, fileMod{path: f, mod: info.ModTime().UnixNano()})
		}
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].mod > mods[j].mod })

	needle := []byte(sessionID)
	for _, fm := range mods {
		if got := recentTransitionsInFile(fm.path, needle, n); len(got) > 0 {
			return got
		}
	}
	return nil
}

func recentTransitionsInFile(path string, sessionID []byte, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil
	}
	seekPos := info.Size() - runStateReadTail
	if seekPos < 0 {
		seekPos = 0
	}
	if _, err := f.Seek(seekPos, io.SeekStart); err != nil {
		return nil
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil
	}

	var hits []string
	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0 && len(hits) < n; i-- {
		line := bytes.TrimSpace(lines[i])
		if !bytes.Contains(line, sessionID) {
			continue
		}
		if transitionRe.FindSubmatch(line) == nil {
			continue
		}
		hits = append(hits, truncate(string(line), 220))
	}
	// hits 是从新到旧收集的，反转成时间正序更好读。
	for i, j := 0, len(hits)-1; i < j; i, j = i+1, j-1 {
		hits[i], hits[j] = hits[j], hits[i]
	}
	return hits
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func humanDur(d time.Duration) string {
	if d < 0 {
		d = -d
	}
	switch {
	case d < time.Second:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < time.Minute:
		return fmt.Sprintf("%.1fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}
