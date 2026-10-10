// Package collector 中 cursor_local.go 读取 Cursor 桌面版的 state.vscdb，
// 补上还没落到 ~/.cursor/projects transcript 里的本地 composer。
//
// 路径：
//
//	Windows  %APPDATA%\Cursor\User\globalStorage\state.vscdb
//	macOS    ~/Library/Application Support/Cursor/User/globalStorage/state.vscdb
//	Linux    ~/.config/Cursor/User/globalStorage/state.vscdb
//
// ItemTable 的 composer.composerData 给出 allComposers 摘要（含 lastUpdatedAt）。
// 只对 30 分钟内有更新的条目再读 cursorDiskKV 的 composerData:<id>，取 status。
// workspace URI 是 vscode-remote 的跳过：那条会话的执行端在 Remote 机器上。
package collector

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	_ "modernc.org/sqlite"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// cursorLocalDBPath 返回桌面版 state.vscdb。不存在时 ok=false。
func cursorLocalDBPath() (string, bool) {
	for _, p := range cursorLocalDBCandidates() {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
	}
	return "", false
}

func cursorLocalDBCandidates() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	switch runtime.GOOS {
	case "windows":
		appData := os.Getenv("APPDATA")
		if appData == "" {
			appData = filepath.Join(home, "AppData", "Roaming")
		}
		return []string{filepath.Join(appData, "Cursor", "User", "globalStorage", "state.vscdb")}
	case "darwin":
		return []string{filepath.Join(home, "Library", "Application Support", "Cursor", "User", "globalStorage", "state.vscdb")}
	default:
		candidates := []string{filepath.Join(home, ".config", "Cursor", "User", "globalStorage", "state.vscdb")}
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			candidates = append(candidates, filepath.Join(xdg, "Cursor", "User", "globalStorage", "state.vscdb"))
		}
		return candidates
	}
}

type cursorComposerHeader struct {
	ComposerID          string                `json:"composerId"`
	CreatedAt           int64                 `json:"createdAt"`
	LastUpdatedAt       int64                 `json:"lastUpdatedAt"`
	IsArchived          bool                  `json:"isArchived"`
	IsDraft             bool                  `json:"isDraft"`
	WorkspaceIdentifier *cursorWorkspaceIdent `json:"workspaceIdentifier"`
}

type cursorWorkspaceIdent struct {
	URI *cursorURI `json:"uri"`
}

type cursorURI struct {
	Scheme   string `json:"scheme"`
	FsPath   string `json:"fsPath"`
	Path     string `json:"path"`
	External string `json:"external"`
}

type cursorComposerBody struct {
	ComposerID       string `json:"composerId"`
	Status           string `json:"status"`
	CreatedAt        int64  `json:"createdAt"`
	LastUpdatedAt    int64  `json:"lastUpdatedAt"`
	IsArchived       bool   `json:"isArchived"`
	IsDraft          bool   `json:"isDraft"`
	AwaitingApproval bool   `json:"awaitingApproval"`
	// 部分版本把待确认放在嵌套字段里，下面两个名字都见过。
	PendingApproval     bool                  `json:"pendingApproval"`
	WorkspaceIdentifier *cursorWorkspaceIdent `json:"workspaceIdentifier"`
}

func readCursorLocalComposers(dbPath string, nowMs int64) []protocol.SessionInfo {
	if dbPath == "" {
		return nil
	}
	if _, err := os.Stat(dbPath); err != nil {
		return nil
	}
	dsn := "file:" + filepath.ToSlash(dbPath) + "?mode=ro&_pragma=busy_timeout(3000)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	return readCursorComposers(db, nowMs)
}

func readCursorComposers(db *sql.DB, nowMs int64) []protocol.SessionInfo {
	headers := loadCursorComposerHeaders(db)
	if len(headers) == 0 {
		return nil
	}
	staleMs := cursorStaleWindow.Milliseconds()
	var out []protocol.SessionInfo
	for _, h := range headers {
		if h.ComposerID == "" || h.IsArchived || h.IsDraft {
			continue
		}
		activity := h.LastUpdatedAt
		if activity == 0 {
			activity = h.CreatedAt
		}
		if activity == 0 || nowMs-activity >= staleMs {
			continue
		}
		cwd, remote := cursorWorkspacePath(h.WorkspaceIdentifier)
		if remote {
			continue
		}
		body, ok := loadCursorComposerBody(db, h.ComposerID)
		state := protocol.StateWaitingForInput
		started := h.CreatedAt
		if ok {
			if body.IsArchived || body.IsDraft {
				continue
			}
			if body.LastUpdatedAt > activity {
				activity = body.LastUpdatedAt
			}
			if cwd == "" {
				var bodyRemote bool
				cwd, bodyRemote = cursorWorkspacePath(body.WorkspaceIdentifier)
				if bodyRemote {
					continue
				}
			}
			if body.CreatedAt > 0 {
				started = body.CreatedAt
			}
			state = mapCursorComposerStatus(body, nowMs, activity)
		} else if nowMs-activity < cursorApprovalQuiet.Milliseconds() {
			// 摘要很新但还没有正文：刚开的会话，先当工作中。
			state = protocol.StateActive
		}
		if cwd == "" {
			cwd = h.ComposerID
		}
		if started == 0 {
			started = activity
		}
		if nowMs-activity >= staleMs {
			continue
		}
		out = append(out, protocol.SessionInfo{
			SessionID:     h.ComposerID,
			Kind:          protocol.KindInteractive,
			Tool:          protocol.ToolCursor,
			CWD:           cwd,
			StartedAt:     started,
			LastHeartbeat: activity,
			State:         state,
			LastActivity:  activity,
		})
	}
	return out
}

func mapCursorComposerStatus(body cursorComposerBody, nowMs, activity int64) protocol.SessionState {
	if body.AwaitingApproval || body.PendingApproval {
		return protocol.StateWaitingForApproval
	}
	switch strings.ToLower(body.Status) {
	case "generating", "applying", "running", "generating_response":
		return protocol.StateActive
	default:
		if activity > 0 && nowMs-activity < cursorApprovalQuiet.Milliseconds() && body.Status == "" {
			return protocol.StateActive
		}
		return protocol.StateWaitingForInput
	}
}

func loadCursorComposerHeaders(db *sql.DB) []cursorComposerHeader {
	var raw string
	err := db.QueryRow(`SELECT value FROM ItemTable WHERE key = ?`, "composer.composerData").Scan(&raw)
	if err != nil || raw == "" {
		return nil
	}
	var wrapped struct {
		AllComposers []cursorComposerHeader `json:"allComposers"`
	}
	if json.Unmarshal([]byte(raw), &wrapped) == nil && len(wrapped.AllComposers) > 0 {
		return wrapped.AllComposers
	}
	var list []cursorComposerHeader
	if json.Unmarshal([]byte(raw), &list) == nil {
		return list
	}
	return nil
}

func loadCursorComposerBody(db *sql.DB, id string) (cursorComposerBody, bool) {
	var raw string
	err := db.QueryRow(`SELECT value FROM cursorDiskKV WHERE key = ?`, "composerData:"+id).Scan(&raw)
	if err != nil || raw == "" {
		return cursorComposerBody{}, false
	}
	var body cursorComposerBody
	if json.Unmarshal([]byte(raw), &body) != nil {
		return cursorComposerBody{}, false
	}
	if body.ComposerID == "" {
		body.ComposerID = id
	}
	return body, true
}

// cursorWorkspacePath 返回本地路径。第二个返回值为 true 表示这是 Remote URI，应跳过。
func cursorWorkspacePath(id *cursorWorkspaceIdent) (string, bool) {
	if id == nil || id.URI == nil {
		return "", false
	}
	u := id.URI
	if isCursorRemoteURI(u.Scheme, u.External) {
		return "", true
	}
	if u.FsPath != "" {
		return u.FsPath, false
	}
	if u.Path != "" && u.Scheme != "vscode-remote" {
		return u.Path, false
	}
	return "", false
}

func isCursorRemoteURI(scheme, external string) bool {
	if scheme == "vscode-remote" || scheme == "vscode-remote-resource" {
		return true
	}
	return strings.Contains(external, "vscode-remote://")
}
