package server

import (
	"encoding/json"
	"io"
	"net/http"

	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

// Handler 持有 Store 引用
type Handler struct {
	store   *Store
	token   string // 认证 token，为空时跳过认证
	version string // Server 版本号（由 main 注入，用于前端展示）
}

// NewHandler 创建 Handler
func NewHandler(store *Store, token string, version string) *Handler {
	return &Handler{store: store, token: token, version: version}
}

// RegisterRoutes 注册所有路由到 mux
func (h *Handler) RegisterRoutes(mux *http.ServeMux) {
	// POST /api/report — Agent 上报，需要 token 认证（token 为空时不认证）
	mux.HandleFunc("/api/report", h.handleReport)

	// GET /api/status — 前端轮询，无需认证
	mux.HandleFunc("/api/status", h.handleStatus)

	// GET /healthz — 健康检查
	mux.HandleFunc("/healthz", h.handleHealthz)

	// GET / — 返回 Web UI（由 embed.go 提供 indexHTML）
	mux.HandleFunc("/", h.handleIndex)
}

// handleReport 处理 Agent 上报
func (h *Handler) handleReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	// token 认证
	if h.token != "" {
		authHeader := r.Header.Get("Authorization")
		expected := "Bearer " + h.token
		if authHeader != expected {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "Bad Request", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	var report protocol.AgentReport
	if err := json.Unmarshal(body, &report); err != nil {
		http.Error(w, "Bad Request: invalid JSON", http.StatusBadRequest)
		return
	}

	if report.MachineID == "" {
		http.Error(w, "Bad Request: machine_id required", http.StatusBadRequest)
		return
	}

	h.store.UpdateMachine(report)
	w.WriteHeader(http.StatusOK)
}

// handleStatus 返回 Dashboard 数据
func (h *Handler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method Not Allowed", http.StatusMethodNotAllowed)
		return
	}

	dashboard := h.store.GetDashboard()
	dashboard.ServerVersion = h.version
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(dashboard)
}

// handleHealthz 健康检查
func (h *Handler) handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("OK"))
}

// handleIndex 返回 Web UI（indexHTML 由 embed.go 提供）
func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(indexHTML))
}
