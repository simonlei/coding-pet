package collector

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// transitionRe 匹配一条状态机日志行的 to= 状态值。
// 形如：... [SessionRunStateMachine]  transition | sessionId=<id> | ... | to=<state> | ...
var transitionRe = regexp.MustCompile(`SessionRunStateMachine\].*?transition.*?\bto=([a-z_]+)`)

// runStateReadTail 从每个日志文件尾部读取的字节数（状态机行较短，末尾若干行足够）。
const runStateReadTail = 65536

// lastRunState 返回指定 session 在 CodeBuddy 运行日志中最后一条
// SessionRunStateMachine transition 的目标状态（to=<state>）。
//
// CodeBuddy 的 pidfile 不含状态字段，但运行日志会记录状态机迁移，
// 是判定「等待授权」最精确的信号：授权时迁移到 waiting_for_permission，
// 获批后迁移到 tool_executing。找不到任何匹配时返回空串。
//
// 日志位于 ~/.codebuddy/logs/<date>/*.log，同一 session 的行可能分布在
// 多个文件中，取含该 session 且 mtime 最新的文件的最后一条 transition。
func lastRunState(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	logsRoot := filepath.Join(home, ".codebuddy", "logs")

	files, err := filepath.Glob(filepath.Join(logsRoot, "*", "*.log"))
	if err != nil || len(files) == 0 {
		return ""
	}

	// 按 mtime 从新到旧排序，优先扫最新文件；命中即返回。
	type fileMod struct {
		path string
		mod  int64
	}
	mods := make([]fileMod, 0, len(files))
	for _, f := range files {
		info, err := os.Stat(f)
		if err != nil {
			continue
		}
		mods = append(mods, fileMod{path: f, mod: info.ModTime().UnixNano()})
	}
	sort.Slice(mods, func(i, j int) bool { return mods[i].mod > mods[j].mod })

	needle := []byte(sessionID)
	for _, fm := range mods {
		if state := lastRunStateInFile(fm.path, needle); state != "" {
			return state
		}
	}
	return ""
}

// runningRunStates 是运行日志中表示「session 正在执行」的状态机状态集合。
// 这些状态出现在最后一条 transition 时，说明工具实际在跑，而非阻塞等待。
var runningRunStates = map[string]bool{
	"tool_executing":   true,
	"model_streaming":  true,
	"model_requesting": true,
	"model_done":       true,
	"agent_running":    true,
	"preparing":        true,
	"pending":          true,
}

// reconcileWithRunState 用运行日志状态机的最后状态（runState）校正 JSONL 判定：
//   - runState=waiting_for_permission：精确命中权限等待 → waiting_for_approval
//   - runState 属运行中集合：纠正 JSONL 因悬空 function_call 产生的 approval 误判
//     （刚发起工具调用、实际在执行）→ active；但不覆盖真正的 waiting_for_input
//   - runState 为空或其它：保留 JSONL 判定
func reconcileWithRunState(jsonlState protocol.SessionState, runState string) protocol.SessionState {
	switch {
	case runState == "waiting_for_permission":
		return protocol.StateWaitingForApproval
	case runningRunStates[runState] && jsonlState == protocol.StateWaitingForApproval:
		return protocol.StateActive
	default:
		return jsonlState
	}
}

// transition 的 to= 状态；文件中无该 session 的 transition 时返回空串。
func lastRunStateInFile(path string, sessionID []byte) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return ""
	}
	seekPos := info.Size() - runStateReadTail
	if seekPos < 0 {
		seekPos = 0
	}
	if _, err := f.Seek(seekPos, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}

	lines := bytes.Split(data, []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := lines[i]
		if !bytes.Contains(line, sessionID) {
			continue
		}
		m := transitionRe.FindSubmatch(line)
		if m == nil {
			continue
		}
		return string(m[1])
	}
	return ""
}
