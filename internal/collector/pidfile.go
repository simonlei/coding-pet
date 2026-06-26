package collector

import (
	"encoding/json"
	"os"
	"path/filepath"
)

// PIDFile 对应 ~/.codebuddy/sessions/<pid>.json 的结构
// JSON 字段名是驼峰命名（CodeBuddy 原始格式）
type PIDFile struct {
    PID           int    `json:"pid"`
    LastHeartbeat int64  `json:"lastHeartbeat"` // Unix ms
    SessionID     string `json:"sessionId"`
    CWD           string `json:"cwd"`
    StartedAt     int64  `json:"startedAt"` // Unix ms
    Kind          string `json:"kind"`
    URL           string `json:"url"`
    Version       string `json:"version"`
    Hostname      string `json:"hostname"`
    UpdatedAt     int64  `json:"updatedAt"`
}

// ReadPIDFiles 扫描 ~/.codebuddy/sessions/*.json，返回所有 PIDFile
// 单文件解析失败时跳过，不返回错误
func ReadPIDFiles() ([]PIDFile, error) {
    homeDir, err := os.UserHomeDir()
    if err != nil {
        return nil, err
    }
    sessionsDir := filepath.Join(homeDir, ".codebuddy", "sessions")
    pattern := filepath.Join(sessionsDir, "*.json")
    files, err := filepath.Glob(pattern)
    if err != nil {
        return nil, err
    }
    var result []PIDFile
    for _, f := range files {
        data, err := os.ReadFile(f)
        if err != nil {
            continue // 跳过读取失败的文件
        }
        var pf PIDFile
        if err := json.Unmarshal(data, &pf); err != nil {
            continue // 跳过解析失败的文件
        }
        result = append(result, pf)
    }
    return result, nil
}

// isProcessAlive 在平台特定文件中实现:
//   - pidfile_windows.go: Windows 实现
//   - pidfile_unix.go:   Unix/Linux 实现
