//go:build !windows

package collector

import (
	"os"
	"syscall"
)

// isProcessAlive 检测进程是否存活
// Unix 下使用 FindProcess + Signal(0) 检测
func isProcessAlive(pid int) bool {
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}
