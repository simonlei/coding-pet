// Package applog 统一初始化 coding-pet 二进制的日志输出。
//
// 目的：即便不通过 restart.sh 启动（nohup 重定向），直接跑 ./coding-pet-server
// 或 ./coding-pet-agent 时，所有输出（log 包 + fmt 打印）都会追加到
// 与二进制同目录下的 coding-pet-<kind>.log。
package applog

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// Setup 把 log 包与进程的 stdout/stderr 全部切到 <exeDir>/coding-pet-<kind>.log。
// 任何一步失败都静默回退到原有 stderr，不影响进程启动。成功时先在原 stderr
// 打印一行 "logging to ..." 再切换，方便前台运行时快速定位日志文件。
func Setup(kind string) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, rErr := filepath.EvalSymlinks(exe); rErr == nil {
		exe = resolved
	}
	path := filepath.Join(filepath.Dir(exe), "coding-pet-"+kind+".log")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	fmt.Fprintf(os.Stderr, "logging to %s\n", path)
	log.SetOutput(f)
	os.Stdout = f
	os.Stderr = f
}
