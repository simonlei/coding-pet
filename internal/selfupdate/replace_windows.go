//go:build windows

package selfupdate

import (
	"fmt"
	"log"
	"os"
)

// Replace 在 Windows 下替换当前二进制。
// 运行中的 .exe 无法被覆盖/删除，但可被 rename：
// 先把当前 exe 换名为 <target>.old，再把新二进制写到原名。
// 若第二步失败，尽量把 .old 换回原名回滚。
// 残留的 .old 由下次启动时的 CleanupOld 清理。
func Replace(newBinary, targetPath string) error {
	oldPath := targetPath + ".old"

	// 先清掉可能残留的上一次 .old，避免 rename 失败。
	_ = os.Remove(oldPath)

	if err := os.Rename(targetPath, oldPath); err != nil {
		return fmt.Errorf("rename current exe to .old: %w", err)
	}
	if err := os.Rename(newBinary, targetPath); err != nil {
		// 回滚：把 .old 换回原名，恢复旧二进制可用。
		if rbErr := os.Rename(oldPath, targetPath); rbErr != nil {
			return fmt.Errorf("rename new exe failed (%v) and rollback failed (%v)", err, rbErr)
		}
		return fmt.Errorf("rename new exe into place: %w", err)
	}
	return nil
}

// CleanupOld 删除 targetPath 旁残留的 .old 文件（best-effort，失败仅 log）。
// 在进程启动早期调用，此时旧 exe 已不再运行，文件锁已释放。
func CleanupOld(targetPath string) {
	oldPath := targetPath + ".old"
	if _, err := os.Stat(oldPath); err != nil {
		return // 不存在则无需清理
	}
	if err := os.Remove(oldPath); err != nil {
		log.Printf("selfupdate: cleanup leftover %s failed: %v", oldPath, err)
	}
}
