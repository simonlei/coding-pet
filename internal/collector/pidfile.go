package collector

import (
    "encoding/json"
    "os"
    "path/filepath"
    "syscall"
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
    sessionsDir := filepath.Join(os.Getenv("HOME"), ".codebuddy", "sessions")
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

// isProcessAlive 通过发送 Signal(0) 检测进程是否存活
func isProcessAlive(pid int) bool {
    proc, err := os.FindProcess(pid)
    if err != nil {
        return false
    }
    err = proc.Signal(syscall.Signal(0))
    return err == nil
}
