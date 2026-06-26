//go:build windows

package collector

import "syscall"

// isProcessAlive 检测进程是否存活
// Windows 下使用 OpenProcess + GetExitCodeProcess 检测
func isProcessAlive(pid int) bool {
	handle, err := syscall.OpenProcess(syscall.PROCESS_QUERY_INFORMATION, false, uint32(pid))
	if err != nil {
		return false
	}
	var exitCode uint32
	err = syscall.GetExitCodeProcess(handle, &exitCode)
	syscall.CloseHandle(handle)
	// STILL_ACTIVE = 259，进程仍在运行
	return err == nil && exitCode == 259
}
