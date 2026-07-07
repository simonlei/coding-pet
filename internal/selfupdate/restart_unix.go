//go:build !windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// sysProcAttr 返回 Unix 下让子进程脱离父进程会话的属性（setsid）。
func sysProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setsid: true}
}

// applyPlatformProc 在 Unix 下设置 setsid，使子进程在父进程退出后继续存活。
func applyPlatformProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = sysProcAttr()
}

// devNull 返回用于兜底的空设备（当日志文件打不开时）。
func devNull() *os.File {
	f, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	return f
}
