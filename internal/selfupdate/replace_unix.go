//go:build !windows

package selfupdate

import (
	"fmt"
	"os"
)

// Replace 用 newBinary 原子替换 targetPath 处的当前二进制。
// Unix 下运行中的二进制文件可被 rename 覆盖（进程持有旧 inode，新文件供下次 exec）。
// newBinary 与 targetPath 需在同一分区（调用方保证临时文件建在目标目录）。
func Replace(newBinary, targetPath string) error {
	if err := os.Chmod(newBinary, 0o755); err != nil {
		return fmt.Errorf("chmod new binary: %w", err)
	}
	if err := os.Rename(newBinary, targetPath); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", newBinary, targetPath, err)
	}
	return nil
}

// CleanupOld 在 Unix 下为 no-op（无 .old 残留机制）。
func CleanupOld(targetPath string) {}
