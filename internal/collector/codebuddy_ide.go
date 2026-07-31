// Package collector 中 codebuddy_ide.go 采集 CodeBuddy IDE 的会话状态。
//
// CodeBuddy IDE 将会话数据存储在两个位置：
//   1. SQLite 数据库 (%APPDATA%\CodeBuddy CN\codebuddy-sessions.vscdb)
//      - ItemTable: key="session:<conversationId>", value=JSON{conversationId, cwd, title, status, updatedAt, ...}
//      - status 字段: Working / Completed
//   2. 消息队列 JSON (%APPDATA%\CodeBuddy CN\User\globalStorage\...\message-queue\<wsHash>.json)
//      - conversations[conversationId].runtime.{activated, paused}
//      - 提供实时运行时状态
//
// 状态映射:
//   - Working + activated + !paused + age < 3min       → active
//   - Working + activated + !paused + 3min ≤ age < 30min → waiting_for_input
//   - Working + paused                                  → waiting_for_input
//   - Working + !activated                              → waiting_for_input
//   - Completed + age < 30min                           → waiting_for_input
//   - 任意    + age ≥ 30min                              → 不上报
//
// 上下文占用 token 从 history 目录读取（usage.lastTokens），SQLite / message-queue 不含此信息。

package collector

import (
	"crypto/md5"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	_ "modernc.org/sqlite"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// ---- 常量 ----

const (
	// cbIDEActiveWindow: Working 会话最近一次写盘在此窗口内 → 仍算「活跃」。
	cbIDEActiveWindow = 3 * time.Minute
	// cbIDEStaleWindow: 任何会话超过此窗口无写盘 → 视为已终止，不再上报。
	cbIDEStaleWindow = 30 * time.Minute
)

// ---- SQLite 会话结构 ----

// cbIDESessionValue 对应 SQLite ItemTable 中 key="session:<id>" 的 value JSON。
type cbIDESessionValue struct {
	ConversationId string `json:"conversationId"`
	Cwd            string `json:"cwd"`
	UserId         string `json:"userId"`
	Title          string `json:"title"`
	Status         string `json:"status"`    // Working / Completed
	CreatedAt      int64  `json:"createdAt"` // Unix ms
	UpdatedAt      int64  `json:"updatedAt"` // Unix ms
	Revision       int    `json:"revision"`
	UpdatedBy      string `json:"updatedBy"` // "ide" or agent name
	DeletedAt      *int64 `json:"deletedAt"`
}

// ---- 消息队列结构 ----

// cbIDEMessageQueueFile 对应 message-queue/<workspaceHash>.json。
type cbIDEMessageQueueFile struct {
	Version       int                            `json:"version"`
	LastUpdated   int64                          `json:"lastUpdated"`
	Conversations map[string]cbIDEMQConversation `json:"conversations"`
}

// cbIDEMQConversation 对应单个 conversation 的队列条目。
type cbIDEMQConversation struct {
	Version        int            `json:"version"`
	ConversationId string         `json:"conversationId"`
	UpdatedAt      int64          `json:"updatedAt"`
	Runtime        cbIDEMQRuntime `json:"runtime"`
}

// cbIDEMQRuntime 运行时状态。
type cbIDEMQRuntime struct {
	Activated           bool  `json:"activated"`
	Paused              bool  `json:"paused"`
	AwaitingSessionIdle bool  `json:"awaitingSessionIdle"`
	UpdatedAt           int64 `json:"updatedAt"`
}

// ---- history 目录结构（仅用于 context token 回退读取） ----

// cbIDEConversationIndex 对应 history/<workspaceHash>/<convId>/index.json。
type cbIDEConversationIndex struct {
	Messages []struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	} `json:"messages"`
	Requests []struct {
		State string `json:"state"`
		Usage *struct {
			LastTokens int64 `json:"lastTokens"`
		} `json:"usage"`
	} `json:"requests"`
}

// ---- SQLite 连接缓存 ----

var (
	cbIDEDBOnce sync.Mutex
	cbIDEDBConn *sql.DB
)

// ---- 平台路径 ----

// codebuddyIDEAppDataRoot 返回 CodeBuddy IDE 的 AppData 根目录。
// Windows: %APPDATA%\CodeBuddy CN 或 %APPDATA%\CodeBuddy
// macOS:   ~/Library/Application Support/CodeBuddy CN 或 ~/Library/Application Support/CodeBuddy
func codebuddyIDEAppDataRoot() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	var base string
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		base = appData
	case "darwin":
		base = filepath.Join(home, "Library", "Application Support")
	default:
		return "", false
	}
	for _, name := range []string{"CodeBuddy CN", "CodeBuddy"} {
		dir := filepath.Join(base, name)
		if fi, err := os.Stat(dir); err == nil && fi.IsDir() {
			return dir, true
		}
	}
	return "", false
}

// codebuddyIDEDBPath 返回 codebuddy-sessions.vscdb 的路径。测试可注入替代实现。
var codebuddyIDEDBPath = func() (string, bool) {
	root, ok := codebuddyIDEAppDataRoot()
	if !ok {
		return "", false
	}
	p := filepath.Join(root, "codebuddy-sessions.vscdb")
	if _, err := os.Stat(p); err != nil {
		return "", false
	}
	return p, true
}

// codebuddyIDEMessageQueueDir 返回 message-queue 目录。测试可注入替代实现。
var codebuddyIDEMessageQueueDir = func() (string, bool) {
	root, ok := codebuddyIDEAppDataRoot()
	if !ok {
		return "", false
	}
	p := filepath.Join(root, "User", "globalStorage", "tencent-cloud.coding-copilot", "message-queue")
	if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
		return "", false
	}
	return p, true
}

// ---- database connection ----

// openCodeBuddyIDEDB 惰性打开只读连接并缓存。
func openCodeBuddyIDEDB() (*sql.DB, bool) {
	cbIDEDBOnce.Lock()
	defer cbIDEDBOnce.Unlock()

	if cbIDEDBConn != nil {
		return cbIDEDBConn, true
	}

	path, ok := codebuddyIDEDBPath()
	if !ok {
		return nil, false
	}

	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, false
	}
	db.SetMaxOpenConns(1)
	cbIDEDBConn = db
	return db, true
}

// resetCodeBuddyIDEDB 关闭并清空缓存句柄。
func resetCodeBuddyIDEDB() {
	cbIDEDBOnce.Lock()
	defer cbIDEDBOnce.Unlock()
	if cbIDEDBConn != nil {
		_ = cbIDEDBConn.Close()
		cbIDEDBConn = nil
	}
}

// ---- 主入口 ----

// CollectCodeBuddyIDESessions 从 SQLite + message-queue 采集 IDE 会话。
// 库不存在或查询失败时返回 nil（不阻塞其他采集源）。
func CollectCodeBuddyIDESessions() []protocol.SessionInfo {
	db, ok := openCodeBuddyIDEDB()
	if !ok {
		return nil
	}

	sessions, err := readCodeBuddyIDESessions(db)
	if err != nil {
		resetCodeBuddyIDEDB()
		return nil
	}
	if len(sessions) == 0 {
		return nil
	}

	runtimeMap := buildCodeBuddyIDERuntimeMap()

	nowMs := time.Now().UnixMilli()
	var out []protocol.SessionInfo

	for _, s := range sessions {
		rt, hasRuntime := runtimeMap[s.ConversationId]
		state, include := mapCodeBuddyIDEStateFromDB(s, rt, hasRuntime, nowMs)
		if !include {
			continue
		}

		contextTokens := codebuddyIDEContextTokensFromHistory(s.Cwd, s.ConversationId)

		out = append(out, protocol.SessionInfo{
			SessionID:     s.ConversationId,
			PID:           0,
			Kind:          protocol.KindInteractive,
			Tool:          protocol.ToolCodeBuddyIDE,
			CWD:           s.Cwd,
			StartedAt:     s.CreatedAt,
			LastHeartbeat: s.UpdatedAt,
			State:         state,
			LastActivity:  s.UpdatedAt,
			ContextTokens: contextTokens,
		})
	}
	return out
}

// readCodeBuddyIDESessions 从 SQLite 读取所有未删除的会话。
func readCodeBuddyIDESessions(db *sql.DB) ([]cbIDESessionValue, error) {
	rows, err := db.Query(`SELECT key, value FROM ItemTable`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sessions []cbIDESessionValue
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			continue
		}
		if len(key) < 9 || key[:8] != "session:" {
			continue
		}
		var s cbIDESessionValue
		if err := json.Unmarshal([]byte(value), &s); err != nil {
			continue
		}
		if s.DeletedAt != nil {
			continue
		}
		if s.ConversationId == "" {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// ---- 消息队列 ----

// buildCodeBuddyIDERuntimeMap 读取所有 message-queue/*.json，构建 conversationId → runtime 映射。
func buildCodeBuddyIDERuntimeMap() map[string]cbIDEMQRuntime {
	mqDir, ok := codebuddyIDEMessageQueueDir()
	if !ok {
		return nil
	}

	entries, err := os.ReadDir(mqDir)
	if err != nil {
		return nil
	}

	runtimeMap := make(map[string]cbIDEMQRuntime)
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		var mq cbIDEMessageQueueFile
		if !readJSONFile(filepath.Join(mqDir, e.Name()), &mq) {
			continue
		}
		for convID, conv := range mq.Conversations {
			runtimeMap[convID] = conv.Runtime
		}
	}
	return runtimeMap
}

// ---- 状态映射 ----

// mapCodeBuddyIDEStateFromDB 将 SQLite 会话记录 + 消息队列运行时状态映射为 SessionState。
// 第二个返回值为 false 表示该会话应从结果中剔除。
func mapCodeBuddyIDEStateFromDB(s cbIDESessionValue, rt cbIDEMQRuntime, hasRuntime bool, nowMs int64) (protocol.SessionState, bool) {
	ageMs := nowMs - s.UpdatedAt

	if ageMs >= cbIDEStaleWindow.Milliseconds() {
		return "", false
	}

	switch lowerASCII(s.Status) {
	case "working":
		if hasRuntime {
			if rt.Paused {
				return protocol.StateWaitingForInput, true
			}
			if !rt.Activated {
				return protocol.StateWaitingForInput, true
			}
			if ageMs < cbIDEActiveWindow.Milliseconds() {
				return protocol.StateActive, true
			}
			return protocol.StateWaitingForInput, true
		}
		// 无消息队列数据：退化为 freshness 猜测
		if ageMs < cbIDEActiveWindow.Milliseconds() {
			return protocol.StateActive, true
		}
		return protocol.StateWaitingForInput, true

	case "completed":
		return protocol.StateWaitingForInput, true

	default:
		return protocol.StateWaitingForInput, true
	}
}

// ---- 上下文 token（history 目录回退） ----

// codebuddyIDEContextTokensFromHistory 尝试从 history 目录读取上下文占用 token。
// history 目录不可达或无数据时返回 0（优雅降级）。
func codebuddyIDEContextTokensFromHistory(cwd, convID string) int64 {
	extRoot, ok := codebuddyExtensionRoot()
	if !ok {
		return 0
	}

	wsHash := fmt.Sprintf("%x", md5.Sum([]byte(filepath.ToSlash(cwd))))

	// 尝试两种 profile 模式
	patterns := []string{
		filepath.Join(extRoot, "Data", "*", "CodeBuddyIDE", "*", "history", wsHash, convID, "index.json"),
		filepath.Join(extRoot, "Data", "*", "CodeBuddyIDE", "history", wsHash, convID, "index.json"),
	}

	for _, pattern := range patterns {
		matches, err := filepath.Glob(pattern)
		if err != nil || len(matches) == 0 {
			continue
		}
		var convIdx cbIDEConversationIndex
		if readJSONFile(matches[0], &convIdx) {
			return codebuddyIDEContextTokensFromIndex(convIdx)
		}
	}
	return 0
}

// codebuddyIDEContextTokensFromIndex 从 conversation index 提取 context token。
func codebuddyIDEContextTokensFromIndex(convIdx cbIDEConversationIndex) int64 {
	for i := len(convIdx.Requests) - 1; i >= 0; i-- {
		if u := convIdx.Requests[i].Usage; u != nil && u.LastTokens > 0 {
			return u.LastTokens
		}
	}
	return 0
}

// codebuddyExtensionRoot 返回 CodeBuddyExtension 数据根目录。
func codebuddyExtensionRoot() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	var root string
	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LOCALAPPDATA")
		if local == "" {
			local = filepath.Join(home, "AppData", "Local")
		}
		root = filepath.Join(local, "CodeBuddyExtension")
	case "darwin":
		root = filepath.Join(home, "Library", "Application Support", "CodeBuddyExtension")
	default:
		return "", false
	}
	if _, err := os.Stat(root); err != nil {
		return "", false
	}
	return root, true
}

// ---- 工具函数 ----

// workspaceHash16 返回 CWD 的 16 字符 workspace hash（与 message-queue 文件名一致）。
func workspaceHash16(cwd string) string {
	h := md5.Sum([]byte(filepath.ToSlash(cwd)))
	return fmt.Sprintf("%x", h)[:16]
}

// readJSONFile 读取并解码一个 JSON 文件到 v，成功返回 true。
func readJSONFile(path string, v any) bool {
	data, err := os.ReadFile(path)
	if err != nil {
		return false
	}
	return json.Unmarshal(data, v) == nil
}
