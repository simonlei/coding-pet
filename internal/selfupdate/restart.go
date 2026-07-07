package selfupdate

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// Restart spawn 一个 detach 的子进程执行 path（透传 args），不等待其结束。
// 子进程 stdout/stderr 追加到 logFile（打不开则退回 os.DevNull），
// 并经平台钩子（Unix setsid / Windows DETACHED_PROCESS）脱离父进程存活。
// 成功返回后调用方应让当前进程退出。
func Restart(path string, args []string, logFile string) error {
	cmd := exec.Command(path, args...)
	cmd.Dir = filepath.Dir(path)

	var out *os.File
	if logFile != "" {
		if f, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644); err == nil {
			out = f
		}
	}
	if out == nil {
		out = devNull()
	}
	cmd.Stdout = out
	cmd.Stderr = out
	cmd.Stdin = nil

	applyPlatformProc(cmd)

	if err := cmd.Start(); err != nil {
		if out != nil {
			out.Close()
		}
		return fmt.Errorf("spawn child process: %w", err)
	}
	// 不 Wait —— 子进程已 detach，父进程即将退出。
	return nil
}
