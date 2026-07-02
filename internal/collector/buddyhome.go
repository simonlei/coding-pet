package collector

import (
	"os"
	"path/filepath"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// buddyHome 描述一个 CodeBuddy 系列工具的 agent home 目录及其工具标签。
//
// CodeBuddy CLI 与 CodeBuddy IDE（含 CodeBuddy CN）共用 ~/.codebuddy；
// WorkBuddy IDE 使用 ~/.workbuddy。两者磁盘布局完全一致：
//   - sessions/<pid>.json      —— PID + lastHeartbeat（判活）
//   - projects/<name>/<sid>.jsonl —— 对话记录（判「等待选择/输入」）
//   - logs/<date>/*.log        —— SessionRunStateMachine（判「等待授权」）
//
// 因此复用同一套解析逻辑，仅 base 目录与工具标签不同。
//
// 注：IDE 侧的 codebuddy-sessions.vscdb / workbuddy.db 仅存「已完成」的历史会话
// 元数据（status 只有 completed），不含实时的「等待用户」状态——IDE 代码自身也把
// vscdb 标注为 legacy、以 projects/ sidecar 为准，故实时状态一律走上述文件布局判定。
type buddyHome struct {
	dir  string               // agent home 绝对路径
	tool protocol.SessionTool // 采集出的 session 打的工具标签
}

// buddyHomes 返回本机需要扫描的所有 CodeBuddy 系列 agent home。
// 目录不存在时仍会返回（上层扫描到空结果自然跳过），保持逻辑简单。
func buddyHomes() []buddyHome {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	return []buddyHome{
		{dir: filepath.Join(home, ".codebuddy"), tool: protocol.ToolCodeBuddy},
		{dir: filepath.Join(home, ".workbuddy"), tool: protocol.ToolWorkBuddy},
	}
}
