package server

import (
	"sort"
	"sync"
	"time"

	"github.com/simonlei/codebuddy-dashboard/internal/protocol"
)

const (
	// offlineThreshold：超过此时间无上报视为离线（90s，给 Agent 5s 上报周期 18 次机会）
	offlineThreshold = 90 * time.Second
	// offlineTTL：离线超过此时间后从 Store 清理
	offlineTTL = 24 * time.Hour
)

// Store 线程安全的内存状态存储
type Store struct {
	mu       sync.RWMutex
	machines map[string]*protocol.MachineStatus // key: machine_id（非 hostname）
}

// NewStore 创建新的 Store
func NewStore() *Store {
	return &Store{
		machines: make(map[string]*protocol.MachineStatus),
	}
}

// UpdateMachine 更新机器状态（使用 Server 本地时间作为 LastReport，避免时钟偏移）
func (s *Store) UpdateMachine(report protocol.AgentReport) {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UnixMilli() // Server 本地时间，非 Agent 时间
	existing, ok := s.machines[report.MachineID]
	if !ok {
		existing = &protocol.MachineStatus{MachineID: report.MachineID}
		s.machines[report.MachineID] = existing
	}
	existing.Hostname = report.Hostname
	existing.LastReport = now
	existing.Online = true
	existing.OfflineSince = 0
	existing.Sessions = report.Sessions
}

// CheckOffline 每秒调用：标记掉线机器，清理过期条目，离线机器 session 状态覆盖为 unknown
func (s *Store) CheckOffline() {
	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now()
	nowMs := now.UnixMilli()
	for id, m := range s.machines {
		if nowMs-m.LastReport > int64(offlineThreshold/time.Millisecond) {
			if m.Online {
				m.Online = false
				m.OfflineSince = nowMs
			}
			// 离线机器的所有 session 状态覆盖为 unknown，避免显示过期的"等待输入"
			for i := range m.Sessions {
				m.Sessions[i].State = protocol.StateUnknown
			}
			// 超过 24 小时未上报 → 从 Store 清理
			if m.OfflineSince > 0 && nowMs-m.OfflineSince > int64(offlineTTL/time.Millisecond) {
				delete(s.machines, id)
			}
		}
	}
}

// GetDashboard 汇总统计，返回前端所需完整数据
func (s *Store) GetDashboard() protocol.DashboardResponse {
	s.mu.RLock()
	defer s.mu.RUnlock()

	now := time.Now().UnixMilli()
	resp := protocol.DashboardResponse{
		Timestamp: now,
		Machines:  make([]protocol.MachineStatus, 0, len(s.machines)),
	}

	for _, m := range s.machines {
		// 深拷贝，避免返回引用
		mc := *m
		mc.Sessions = make([]protocol.SessionInfo, len(m.Sessions))
		copy(mc.Sessions, m.Sessions)
		resp.Machines = append(resp.Machines, mc)

		for _, sess := range m.Sessions {
			resp.TotalSessions++
			if m.Online {
				switch sess.State {
				case protocol.StateActive:
					resp.ActiveCount++
				case protocol.StateWaitingForInput:
					resp.WaitingCount++
				case protocol.StateWaitingForApproval:
					resp.ApprovalCount++
					resp.WaitingCount++ // approval 也计入等待总数
				}
			}
		}
		if !m.Online {
			resp.OfflineCount++
		}
	}

	// 排序：1. 等待输入优先 2. 在线优先 3. MachineID 升序（稳定排序）
	sort.Slice(resp.Machines, func(i, j int) bool {
		wi := haswWaiting(resp.Machines[i])
		wj := haswWaiting(resp.Machines[j])
		if wi != wj {
			return wi
		}
		oi := resp.Machines[i].Online
		oj := resp.Machines[j].Online
		if oi != oj {
			return oi
		}
		return resp.Machines[i].MachineID < resp.Machines[j].MachineID
	})

	return resp
}

func haswWaiting(m protocol.MachineStatus) bool {
	for _, s := range m.Sessions {
		if s.State == protocol.StateWaitingForInput ||
			s.State == protocol.StateWaitingForApproval {
			return true
		}
	}
	return false
}
