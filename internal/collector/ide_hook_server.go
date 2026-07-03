// Package collector 中 ide_hook_server.go 提供本地回环 HTTP 服务，
// 用于接收 CodeBuddy / WorkBuddy IDE 的 Hook 上报。
//
// 设计要点：
//   - 只监听 127.0.0.1，避免暴露到局域网。
//   - 端口默认 38765，可通过 env DASHBOARD_IDE_HOOK_ADDR 覆盖（形如 "127.0.0.1:38765"）。
//   - endpoint POST /ide/hook：请求体是 IDEHookEvent JSON。
//   - endpoint GET  /ide/sessions：调试用，返回当前内存中的所有 IDE session。
//   - endpoint GET  /healthz：探活。
//   - 服务失败不影响 agent 主流程，只写日志。
package collector

import (
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

// DefaultIDEHookAddr 默认监听地址。
const DefaultIDEHookAddr = "127.0.0.1:38765"

// StartIDEHookServer 在后台启动 hook HTTP 服务。addr 为空时读取
// env DASHBOARD_IDE_HOOK_ADDR，仍为空时使用 DefaultIDEHookAddr。
// 返回实际监听地址（便于日志/测试），启动失败返回错误但不 panic。
func StartIDEHookServer(addr string) (string, error) {
	if addr == "" {
		addr = os.Getenv("DASHBOARD_IDE_HOOK_ADDR")
	}
	if addr == "" {
		addr = DefaultIDEHookAddr
	}
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return "", err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ide/hook", handleIDEHook)
	mux.HandleFunc("/ide/sessions", handleIDESessions)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok"))
	})

	srv := &http.Server{
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 5 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("ide hook server exited: %v", err)
		}
	}()
	return ln.Addr().String(), nil
}

// hookAckResponse 是给 IDE Hook 脚本的固定 ACK。
// hook 脚本自身负责按官方规范向 stdout 输出 {"continue":true,...} 决策 JSON；
// 本 endpoint 只回执 ACK，不参与 IDE 的允许/拒绝决策。
var hookAckResponse = map[string]any{"ok": true}

func handleIDEHook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	// 限制 body 大小，防呆
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MiB

	var ev IDEHookEvent
	if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if ev.HookEventName == "" || ev.SessionID == "" {
		http.Error(w, "missing hook_event_name or session_id", http.StatusBadRequest)
		return
	}
	globalIDEStore.Apply(ev)

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(hookAckResponse)
}

func handleIDESessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(globalIDEStore.Snapshot())
}
