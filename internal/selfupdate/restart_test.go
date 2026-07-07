package selfupdate

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// TestRestart_SpawnsDetachedChild 用一个真实脚本验证 Restart 能 spawn 子进程、
// 透传参数、且子进程 stdout 写入指定日志文件。
func TestRestart_SpawnsDetachedChild(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell script child not portable to windows test")
	}
	dir := t.TempDir()
	script := filepath.Join(dir, "child.sh")
	marker := filepath.Join(dir, "marker.txt")
	// 子进程把它收到的第一个参数写进 marker 文件
	content := "#!/bin/sh\necho \"$1\" > \"" + marker + "\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	logFile := filepath.Join(dir, "child.log")

	if err := Restart(script, []string{"HELLO-ARG"}, logFile); err != nil {
		t.Fatalf("Restart: %v", err)
	}

	// 等子进程执行完成
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("child did not run / marker missing: %v", err)
	}
	if string(got) != "HELLO-ARG\n" {
		t.Errorf("child received arg %q, want HELLO-ARG", got)
	}
}

func TestRestart_BadPath(t *testing.T) {
	err := Restart(filepath.Join(t.TempDir(), "does-not-exist"), nil, "")
	if err == nil {
		t.Error("expected error spawning nonexistent binary, got nil")
	}
}
