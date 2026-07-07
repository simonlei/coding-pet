//go:build !windows

package selfupdate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReplace_Unix(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "coding-pet-agent")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	newBin := filepath.Join(dir, "cp-bin-new")
	if err := os.WriteFile(newBin, []byte("NEW"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := Replace(newBin, target); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != "NEW" {
		t.Errorf("target content = %q, want NEW", got)
	}
	info, _ := os.Stat(target)
	if info.Mode().Perm()&0o100 == 0 {
		t.Error("replaced binary not executable")
	}
	// 源临时文件应已被 rename 移走
	if _, err := os.Stat(newBin); !os.IsNotExist(err) {
		t.Error("source temp file should be gone after rename")
	}
}

func TestCleanupOld_Unix_NoOp(t *testing.T) {
	// Unix 下 CleanupOld 为 no-op，不应 panic
	CleanupOld(filepath.Join(t.TempDir(), "coding-pet-agent"))
}
