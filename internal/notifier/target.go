package notifier

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Target 是配置文件中单条推送目标。Kind 决定 Dispatcher 分派；未知 Kind
// 保留在内存但分发时会被跳过（R10）。
type Target struct {
	Kind string `json:"kind"`
	URL  string `json:"url"`
}

// TargetStore 抽象 target 列表的读写。fileTargetStore 是默认实现，测试可用
// fake 覆盖。
type TargetStore interface {
	// List 返回当前 target 列表的快照（可安全遍历，不会被内部修改）。
	List() []Target
}

// fileTargetStore 把 target 列表持久化到 JSON 文件，并在后台 goroutine 里每 10s
// 重读一次以感知子命令的写入。
type fileTargetStore struct {
	path string

	mu       sync.RWMutex
	cached   []Target
	lastHash [32]byte
}

// NewFileTargetStore 构造 fileTargetStore；path 为空时走默认位置
// $CODING_PET_TARGETS_PATH 或 $HOME/.coding-pet/targets.json。
func NewFileTargetStore(path string) *fileTargetStore {
	if path == "" {
		path = resolveDefaultTargetsPath()
	}
	return &fileTargetStore{path: path}
}

// Path 返回底层配置文件的绝对路径（供子命令与日志用）。
func (s *fileTargetStore) Path() string { return s.path }

// resolveDefaultTargetsPath 决议默认配置文件路径。优先 env（测试用），否则 home 下。
// 无 home 时退回当前目录。
func resolveDefaultTargetsPath() string {
	if p := os.Getenv("CODING_PET_TARGETS_PATH"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "coding-pet-targets.json"
	}
	return filepath.Join(home, ".coding-pet", "targets.json")
}

// List 返回当前内存缓存的 target 快照。
func (s *fileTargetStore) List() []Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Target, len(s.cached))
	copy(out, s.cached)
	return out
}

// Load 同步从文件读取并替换内存缓存。文件不存在视为空数组不报错（R11）。
// 解析失败保留旧缓存（R12），返回 error 供调用者写日志。
func (s *fileTargetStore) Load() error {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.mu.Lock()
			s.cached = nil
			s.lastHash = [32]byte{}
			s.mu.Unlock()
			return nil
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	if len(b) == 0 {
		s.mu.Lock()
		s.cached = nil
		s.lastHash = sha256.Sum256(b)
		s.mu.Unlock()
		return nil
	}
	var raw []Target
	if err := json.Unmarshal(b, &raw); err != nil {
		return fmt.Errorf("parse %s: %w", s.path, err)
	}
	s.mu.Lock()
	s.cached = raw
	s.lastHash = sha256.Sum256(b)
	s.mu.Unlock()
	return nil
}

// Add 追加一条 target 并原子写回。同一 URL 已存在时视为 no-op（R13 幂等）。
func (s *fileTargetStore) Add(t Target) error {
	if t.URL == "" {
		return fmt.Errorf("target URL is empty")
	}
	if t.Kind == "" {
		t.Kind = "wechat_work"
	}
	if err := s.Load(); err != nil {
		// 文件损坏时 Load 返回 error；Add 不应在损坏文件上追加，让用户先修。
		return err
	}
	s.mu.Lock()
	for _, existing := range s.cached {
		if existing.URL == t.URL {
			s.mu.Unlock()
			return nil
		}
	}
	next := append([]Target(nil), s.cached...)
	next = append(next, t)
	s.mu.Unlock()
	return s.write(next)
}

// Remove 按完整 URL 或 1-based 序号删除一条 target。未命中返回 error。
func (s *fileTargetStore) Remove(key string) error {
	if err := s.Load(); err != nil {
		return err
	}
	s.mu.RLock()
	current := append([]Target(nil), s.cached...)
	s.mu.RUnlock()

	if idx, err := strconv.Atoi(key); err == nil {
		if idx < 1 || idx > len(current) {
			return fmt.Errorf("index %d out of range (have %d)", idx, len(current))
		}
		current = append(current[:idx-1], current[idx:]...)
		return s.write(current)
	}
	// 按 URL
	for i, t := range current {
		if t.URL == key {
			current = append(current[:i], current[i+1:]...)
			return s.write(current)
		}
	}
	return fmt.Errorf("target not found: %s", key)
}

// write 原子写回 targets 列表到文件（tmp + rename）。同时更新内存缓存与 hash。
func (s *fileTargetStore) write(targets []Target) error {
	if targets == nil {
		targets = []Target{}
	}
	b, err := json.MarshalIndent(targets, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	s.mu.Lock()
	s.cached = targets
	s.lastHash = sha256.Sum256(b)
	s.mu.Unlock()
	return nil
}

// StartWatch 起一个后台 goroutine，每 interval 检查一次文件；内容 hash 变化
// 才重读并替换缓存。ctx cancel 退出。
func (s *fileTargetStore) StartWatch(ctx context.Context, interval time.Duration) {
	s.startWatchWithNotify(ctx, interval, nil)
}

// startWatchWithNotify 供测试用：额外传入 onChange 回调，每次成功重读后调用。
func (s *fileTargetStore) startWatchWithNotify(ctx context.Context, interval time.Duration, onChange func(n int)) {
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				s.reloadIfChanged(onChange)
			}
		}
	}()
}

// reloadIfChanged 读文件；hash 变化则更新缓存并回调。文件损坏保留旧缓存，
// 写一行错误日志（R12）。
func (s *fileTargetStore) reloadIfChanged(onChange func(n int)) {
	b, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			// 文件被删除：清空缓存（若之前非空）。
			s.mu.Lock()
			had := len(s.cached) > 0
			if had {
				s.cached = nil
				s.lastHash = [32]byte{}
			}
			s.mu.Unlock()
			if had && onChange != nil {
				onChange(0)
			}
			return
		}
		log.Printf("notifier: watch read %s: %v", s.path, err)
		return
	}
	hash := sha256.Sum256(b)
	s.mu.RLock()
	same := hash == s.lastHash
	s.mu.RUnlock()
	if same {
		return
	}
	var raw []Target
	if len(b) > 0 {
		if err := json.Unmarshal(b, &raw); err != nil {
			log.Printf("notifier: watch parse %s: %v (keeping previous list)", s.path, err)
			return
		}
	}
	s.mu.Lock()
	prev := len(s.cached)
	s.cached = raw
	s.lastHash = hash
	s.mu.Unlock()
	log.Printf("notifier: targets updated: %d -> %d", prev, len(raw))
	if onChange != nil {
		onChange(len(raw))
	}
}

// SummarizeKinds 返回如 "wechat_work=2,dingtalk=1" 的 kind 分布字符串，
// 供启动日志用。空列表返回空字符串。
func SummarizeKinds(targets []Target) string {
	if len(targets) == 0 {
		return ""
	}
	counts := map[string]int{}
	order := []string{}
	for _, t := range targets {
		if _, ok := counts[t.Kind]; !ok {
			order = append(order, t.Kind)
		}
		counts[t.Kind]++
	}
	var parts []string
	for _, k := range order {
		parts = append(parts, fmt.Sprintf("%s=%d", k, counts[k]))
	}
	return strings.Join(parts, ",")
}
