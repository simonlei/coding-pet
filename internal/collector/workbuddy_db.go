// Package collector 中 workbuddy_db.go 采集 WorkBuddy 桌面版的会话状态。
//
// 与 CodeBuddy CLI（~/.codebuddy，PID 文件 + JSONL）不同，WorkBuddy 桌面版把
// 会话状态写进 SQLite 库 ~/.workbuddy/workbuddy.db 的 sessions 表，其中 status
// 字段直接给出「working / completed」，比解析 JSONL 末条记录反推更可靠。
//
// 库处于 WAL 模式且被 WorkBuddy 进程持有写句柄；我们以只读方式打开，WAL 允许
// reader 与 writer 并发共存，不会互相加锁，也不会破坏正在写入的库。
//
// 状态映射（DB 只有 working / completed 两态，无心跳字段，故用 last_activity
// 新鲜度兜底，与 CodeBuddy IDE Hook 的分层展示保持一致）：
//   - working   且 age < 3min          → active（顶部绿色活跃）
//   - working   且 3min ≤ age < 30min  → waiting_for_input（掉出高亮，淡黄列表）
//   - completed 且 age < 30min          → waiting_for_input（答完等回复）
//   - 任意       且 age ≥ 30min          → 不上报（视为已终止）
//
// 前端 HOOK_ONLY_TOOLS = { codebuddy_ide, workbuddy } 已有 3 分钟降级逻辑，
// 采集端只要打上 Tool=workbuddy 并给对 state，即自动复用该展示分层。
//
// 当前上下文占用 token 取自同库的 session_usage 表（used 列），LEFT JOIN 得到；
// 无 usage 行时降级为 0，前端不展示。
package collector

import (
	"database/sql"
	"os"
	"path/filepath"
	"sync"
	"time"

	// 纯 Go（无 cgo）SQLite 驱动，便于 agent 跨平台交叉编译。
	_ "modernc.org/sqlite"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

const (
	// wbActiveWindow：working 会话最近一次活动在此窗口内 → 仍算「活跃」。
	wbActiveWindow = 3 * time.Minute
	// wbStaleWindow：任何会话超过此窗口无活动 → 视为已终止，不再上报。
	wbStaleWindow = 30 * time.Minute
)

// wbDB 缓存只读连接句柄，跨采集轮复用（采集循环约每 5 秒调用一次）。
var (
	wbDBOnce sync.Mutex
	wbDBConn *sql.DB
)

// workbuddyDBPath 返回 ~/.workbuddy/workbuddy.db 的绝对路径。
func workbuddyDBPath() (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	p := filepath.Join(home, ".workbuddy", "workbuddy.db")
	if _, err := os.Stat(p); err != nil {
		return "", false // WorkBuddy 未安装或未产生过会话，安静跳过
	}
	return p, true
}

// openWorkBuddyDB 惰性打开只读连接并缓存。库不存在时返回 (nil, false)。
func openWorkBuddyDB() (*sql.DB, bool) {
	wbDBOnce.Lock()
	defer wbDBOnce.Unlock()

	if wbDBConn != nil {
		return wbDBConn, true
	}

	path, ok := workbuddyDBPath()
	if !ok {
		return nil, false
	}

	// mode=ro 只读；busy_timeout 避免偶发写事务瞬间的 SQLITE_BUSY。
	// Windows 路径用正斜杠以符合 file: URI。
	dsn := "file:" + filepath.ToSlash(path) + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, false
	}
	// 单连接足够（只读、低频），也避免多连接各自持有 WAL 读锁。
	db.SetMaxOpenConns(1)
	wbDBConn = db
	return db, true
}

// CollectWorkBuddyDBSessions 读取 workbuddy.db 的 sessions 表，返回活跃/等待中的
// 会话列表。库不存在或查询失败时返回 nil（不阻塞其他采集源）。
func CollectWorkBuddyDBSessions() []protocol.SessionInfo {
	db, ok := openWorkBuddyDB()
	if !ok {
		return nil
	}

	// LEFT JOIN session_usage：该表 used 列即当前上下文占用 token（size 为窗口上限，
	// 如 168000）。无 usage 行的会话 used 为 NULL，COALESCE 兜底为 0（前端不展示）。
	rows, err := db.Query(`
		SELECT s.id, s.cwd, s.status, s.created_at,
		       COALESCE(s.last_activity_at, s.updated_at),
		       COALESCE(u.used, 0)
		FROM sessions s
		LEFT JOIN session_usage u ON u.session_id = s.id
		WHERE s.deleted_at IS NULL`)
	if err != nil {
		// 查询失败（如库瞬时不可读）：丢弃缓存句柄，下轮重开。
		resetWorkBuddyDB()
		return nil
	}
	defer rows.Close()

	nowMs := time.Now().UnixMilli()
	var out []protocol.SessionInfo
	for rows.Next() {
		var (
			id, cwd, status string
			createdAt       int64
			lastActivity    int64
			contextTokens   int64
		)
		if err := rows.Scan(&id, &cwd, &status, &createdAt, &lastActivity, &contextTokens); err != nil {
			continue
		}
		state, include := mapWorkBuddyState(status, nowMs-lastActivity)
		if !include {
			continue
		}
		out = append(out, protocol.SessionInfo{
			SessionID:     id,
			PID:           0, // 桌面版无独立 per-session PID
			Kind:          protocol.KindInteractive,
			Tool:          protocol.ToolWorkBuddy,
			CWD:           cwd,
			StartedAt:     createdAt,
			LastHeartbeat: lastActivity,
			State:         state,
			LastActivity:  lastActivity,
			ContextTokens: contextTokens,
		})
	}
	if err := rows.Err(); err != nil {
		resetWorkBuddyDB()
	}
	return out
}

// resetWorkBuddyDB 关闭并清空缓存句柄，使下一轮采集重新打开（容错用）。
func resetWorkBuddyDB() {
	wbDBOnce.Lock()
	defer wbDBOnce.Unlock()
	if wbDBConn != nil {
		_ = wbDBConn.Close()
		wbDBConn = nil
	}
}

// mapWorkBuddyState 把 DB 的 status + 距最后活动的毫秒数映射为 SessionState。
// 第二个返回值为 false 表示该会话应从结果中剔除（已终止，不上报）。
//
// status 大小写混用（DB 中同时存在 "completed" 与 "Completed"），统一小写比较。
func mapWorkBuddyState(status string, ageMs int64) (protocol.SessionState, bool) {
	// 超过 30 分钟无活动 → 视为已终止，不上报。
	if ageMs >= wbStaleWindow.Milliseconds() {
		return "", false
	}

	switch lowerASCII(status) {
	case "working":
		if ageMs < wbActiveWindow.Milliseconds() {
			return protocol.StateActive, true
		}
		// 3~30 分钟无更新：多半 IDE 已关或崩溃，掉出高亮，淡黄列表保留。
		return protocol.StateWaitingForInput, true
	case "completed":
		// 一轮答完、等用户下条指令。
		return protocol.StateWaitingForInput, true
	default:
		// 未知 status（协议扩展）：窗口内保守显示为等待，不丢失可见性。
		return protocol.StateWaitingForInput, true
	}
}

// lowerASCII 小写化（仅处理 ASCII，避免引入 strings 依赖以外的开销）。
func lowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if c >= 'A' && c <= 'Z' {
			b[i] = c + ('a' - 'A')
		}
	}
	return string(b)
}
