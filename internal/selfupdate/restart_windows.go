//go:build windows

package selfupdate

import (
	"os"
	"os/exec"
	"syscall"
)

// Windows 进程创建标志（见 Win32 CreateProcess 文档）。
const (
	_DETACHED_PROCESS         = 0x00000008
	_CREATE_NEW_PROCESS_GROUP = 0x00000200
)

// applyPlatformProc 在 Windows 下设置 DETACHED_PROCESS，使子进程脱离父进程控制台。
func applyPlatformProc(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: _DETACHED_PROCESS | _CREATE_NEW_PROCESS_GROUP,
	}
}

// devNull 返回用于兜底的空设备（当日志文件打不开时）。
func devNull() *os.File {
	f, _ := os.OpenFile(os.DevNull, os.O_WRONLY, 0)
	return f
}
