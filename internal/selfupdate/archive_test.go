package selfupdate

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

// makeTarGz 构造含给定 name->content 条目的 .tar.gz，写到 dir 下，返回路径。
func makeTarGz(t *testing.T, dir string, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, "test.tar.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	for name, content := range entries {
		hdr := &tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gw.Close()
	return path
}

// makeZip 构造含给定 name->content 条目的 .zip，写到 dir 下，返回路径。
func makeZip(t *testing.T, dir string, entries map[string]string) string {
	t.Helper()
	path := filepath.Join(dir, "test.zip")
	buf := new(bytes.Buffer)
	zw := zip.NewWriter(buf)
	for name, content := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	zw.Close()
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestExtractBinary_TarGz(t *testing.T) {
	dir := t.TempDir()
	// 归档内二进制位于子目录，模拟真实 build.yml 布局
	entries := map[string]string{
		"coding-pet-v1.2.3-linux-amd64/coding-pet-agent":  "AGENT-BINARY",
		"coding-pet-v1.2.3-linux-amd64/coding-pet-server": "SERVER-BINARY",
		"coding-pet-v1.2.3-linux-amd64/README.md":         "readme",
	}
	archivePath := makeTarGz(t, dir, entries)

	path, err := ExtractBinary(archivePath, "coding-pet-agent", dir)
	if err != nil {
		t.Fatalf("ExtractBinary agent: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "AGENT-BINARY" {
		t.Errorf("agent content = %q", got)
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm()&0o100 == 0 {
		t.Error("extracted binary is not executable")
	}

	spath, err := ExtractBinary(archivePath, "coding-pet-server", dir)
	if err != nil {
		t.Fatalf("ExtractBinary server: %v", err)
	}
	sgot, _ := os.ReadFile(spath)
	if string(sgot) != "SERVER-BINARY" {
		t.Errorf("server content = %q", sgot)
	}
}

func TestExtractBinary_Zip(t *testing.T) {
	dir := t.TempDir()
	entries := map[string]string{
		"coding-pet-v1.2.3-windows-amd64/coding-pet-agent.exe":  "WIN-AGENT",
		"coding-pet-v1.2.3-windows-amd64/coding-pet-server.exe": "WIN-SERVER",
	}
	archivePath := makeZip(t, dir, entries)

	path, err := ExtractBinary(archivePath, "coding-pet-agent.exe", dir)
	if err != nil {
		t.Fatalf("ExtractBinary from zip: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "WIN-AGENT" {
		t.Errorf("content = %q", got)
	}
}

func TestExtractBinary_MissingEntry(t *testing.T) {
	dir := t.TempDir()
	archivePath := makeTarGz(t, dir, map[string]string{"coding-pet-server": "x"})
	if _, err := ExtractBinary(archivePath, "coding-pet-agent", dir); err == nil {
		t.Error("expected error for missing binary, got nil")
	}
}

func TestExtractBinary_CorruptArchive(t *testing.T) {
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.tar.gz")
	os.WriteFile(bad, []byte("not-a-gzip"), 0o644)
	if _, err := ExtractBinary(bad, "coding-pet-agent", dir); err == nil {
		t.Error("expected error for corrupt archive, got nil")
	}
}

func TestExtractBinary_UnsupportedExt(t *testing.T) {
	dir := t.TempDir()
	f := filepath.Join(dir, "x.rar")
	os.WriteFile(f, []byte("x"), 0o644)
	if _, err := ExtractBinary(f, "coding-pet-agent", dir); err == nil {
		t.Error("expected error for unsupported extension, got nil")
	}
}
