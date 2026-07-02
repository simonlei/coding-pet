package collector

import (
	"os"
	"path/filepath"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// buddyHome 描述一个 CodeBuddy 系列工具的 agent home 目录及其工具标签。
//
// CodeBuddy CLI 使用 ~/.codebuddy，磁盘布局：
//   - sessions/<pid>.json      —— PID + lastHeartbeat（判活）
//   - projects/<name>/<sid>.jsonl —— 对话记录（判「等待选择/输入」）
//   - logs/<date>/*.log        —— SessionRunStateMachine（判「等待授权」）
//
// 注：CodeBuddy IDE 与 WorkBuddy IDE 的实时状态已改由官方 Hook 主动上报
// （见 ide_hook_store.go），不再扫描磁盘，故此处只保留 CLI 的 agent home。
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
	}
}
