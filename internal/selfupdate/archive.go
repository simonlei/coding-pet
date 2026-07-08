package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// ExtractBinary 从归档 archivePath 中取出名为 binName 的二进制，写入 destDir 下的
// 临时文件，置可执行位（Unix），返回临时文件路径。
// 依归档扩展名（.zip / .tar.gz）自动选择解压方式。
// binName 应为归档内条目的 basename，如 "coding-pet-agent" 或 "coding-pet-server.exe"。
func ExtractBinary(archivePath, binName, destDir string) (string, error) {
	switch {
	case strings.HasSuffix(archivePath, ".zip"):
		return extractFromZip(archivePath, binName, destDir)
	case strings.HasSuffix(archivePath, ".tar.gz") || strings.HasSuffix(archivePath, ".tgz"):
		return extractFromTarGz(archivePath, binName, destDir)
	default:
		return "", fmt.Errorf("unsupported archive extension: %s", archivePath)
	}
}

// writeBinaryTemp 把 r 的内容写入 destDir 下的临时文件并置可执行位，返回路径。
func writeBinaryTemp(r io.Reader, destDir string) (string, error) {
	tmp, err := os.CreateTemp(destDir, "cp-bin-*")
	if err != nil {
		return "", fmt.Errorf("create temp binary: %w", err)
	}
	tmpPath := tmp.Name()
	if _, err := io.Copy(tmp, r); err != nil {
		tmp.Close()
		os.Remove(tmpPath)
		return "", fmt.Errorf("write temp binary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpPath)
		return "", err
	}
	if err := os.Chmod(tmpPath, 0o755); err != nil {
		os.Remove(tmpPath)
		return "", fmt.Errorf("chmod temp binary: %w", err)
	}
	return tmpPath, nil
}

// matchEntry 判断归档条目的路径 name 是否指向目标二进制 binName。
// 兼容归档内二进制位于子目录（如 coding-pet-<ver>-<os>-<arch>/coding-pet-agent）。
func matchEntry(name, binName string) bool {
	return filepath.Base(filepath.FromSlash(name)) == binName
}

func extractFromTarGz(archivePath, binName, destDir string) (string, error) {
	f, err := os.Open(archivePath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return "", fmt.Errorf("open gzip: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		if matchEntry(hdr.Name, binName) {
			return writeBinaryTemp(tr, destDir)
		}
	}
	return "", fmt.Errorf("binary %q not found in archive %s", binName, archivePath)
}

func extractFromZip(archivePath, binName, destDir string) (string, error) {
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		return "", fmt.Errorf("open zip: %w", err)
	}
	defer zr.Close()
	for _, zf := range zr.File {
		if zf.FileInfo().IsDir() {
			continue
		}
		if matchEntry(zf.Name, binName) {
			rc, err := zf.Open()
			if err != nil {
				return "", err
			}
			path, err := writeBinaryTemp(rc, destDir)
			rc.Close()
			return path, err
		}
	}
	return "", fmt.Errorf("binary %q not found in archive %s", binName, archivePath)
}
