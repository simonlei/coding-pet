package selfupdate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"testing"
)

// newTestServer 构造一个伪 GitHub：/repos/.../releases/latest 返回 rel，
// 其余路径按 assets map（path -> content）提供下载。
func newTestServer(t *testing.T, tag string, assets map[string][]byte) (*httptest.Server, *Release) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	var relAssets []Asset
	for name, content := range assets {
		name, content := name, content
		path := "/dl/" + name
		relAssets = append(relAssets, Asset{Name: name, DownloadURL: srv.URL + path})
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			w.Write(content)
		})
	}
	rel := &Release{TagName: tag, Assets: relAssets}

	mux.HandleFunc(fmt.Sprintf("/repos/%s/releases/latest", repoSlug), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(rel)
	})
	return srv, rel
}

func sha256hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func TestFetchLatest_Happy(t *testing.T) {
	tag := "v1.2.3"
	archiveName := AssetName(tag)
	archiveData := []byte("fake-archive-bytes")
	sums := fmt.Sprintf("%s  %s\n", sha256hex(archiveData), archiveName)
	assets := map[string][]byte{
		archiveName:  archiveData,
		shaSumsAsset: []byte(sums),
	}
	srv, _ := newTestServer(t, tag, assets)
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	rel, err := c.FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest error: %v", err)
	}
	if rel.TagName != tag {
		t.Errorf("tag = %q, want %q", rel.TagName, tag)
	}
	if len(rel.Assets) != 2 {
		t.Errorf("assets = %d, want 2", len(rel.Assets))
	}
}

func TestFetchLatest_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	if _, err := c.FetchLatest(); err == nil {
		t.Error("expected error on 404, got nil")
	}
}

func TestSelectAsset(t *testing.T) {
	tag := "v1.2.3"
	want := AssetName(tag)
	rel := &Release{
		TagName: tag,
		Assets: []Asset{
			{Name: "coding-pet-" + tag + "-otheros-otherarch.tar.gz"},
			{Name: want},
			{Name: shaSumsAsset},
		},
	}
	archive, sha, err := rel.SelectAsset()
	if err != nil {
		t.Fatalf("SelectAsset error: %v", err)
	}
	if archive.Name != want {
		t.Errorf("archive = %q, want %q", archive.Name, want)
	}
	if sha.Name != shaSumsAsset {
		t.Errorf("sha = %q, want %q", sha.Name, shaSumsAsset)
	}
}

func TestSelectAsset_WindowsUsesZip(t *testing.T) {
	if runtime.GOOS == "windows" {
		if got := archiveExt(); got != "zip" {
			t.Errorf("archiveExt on windows = %q, want zip", got)
		}
	} else {
		if got := archiveExt(); got != "tar.gz" {
			t.Errorf("archiveExt on %s = %q, want tar.gz", runtime.GOOS, got)
		}
	}
}

func TestSelectAsset_MissingSha(t *testing.T) {
	tag := "v1.2.3"
	rel := &Release{TagName: tag, Assets: []Asset{{Name: AssetName(tag)}}}
	if _, _, err := rel.SelectAsset(); err == nil {
		t.Error("expected error for missing SHA256SUMS.txt, got nil")
	}
}

func TestDownloadAndVerify_Happy(t *testing.T) {
	tag := "v1.2.3"
	archiveName := AssetName(tag)
	archiveData := []byte("real-archive-content-here")
	sums := fmt.Sprintf("%s  %s\n", sha256hex(archiveData), archiveName)
	assets := map[string][]byte{
		archiveName:  archiveData,
		shaSumsAsset: []byte(sums),
	}
	srv, rel := newTestServer(t, tag, assets)
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	archive, sha, err := rel.SelectAsset()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	path, err := c.DownloadAndVerify(archive, sha, dir)
	if err != nil {
		t.Fatalf("DownloadAndVerify error: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(archiveData) {
		t.Error("downloaded content mismatch")
	}
}

func TestDownloadAndVerify_ShaMismatch(t *testing.T) {
	tag := "v1.2.3"
	archiveName := AssetName(tag)
	archiveData := []byte("real-content")
	// 故意写错误的 sha
	sums := fmt.Sprintf("%s  %s\n", sha256hex([]byte("different")), archiveName)
	assets := map[string][]byte{
		archiveName:  archiveData,
		shaSumsAsset: []byte(sums),
	}
	srv, rel := newTestServer(t, tag, assets)
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	archive, sha, _ := rel.SelectAsset()
	dir := t.TempDir()
	_, err := c.DownloadAndVerify(archive, sha, dir)
	if err == nil {
		t.Fatal("expected sha256 mismatch error, got nil")
	}
	// 校验失败后临时文件应被清理
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		t.Errorf("temp files not cleaned up: %v", entries)
	}
}

func TestDownloadAndVerify_MissingShaEntry(t *testing.T) {
	tag := "v1.2.3"
	archiveName := AssetName(tag)
	assets := map[string][]byte{
		archiveName:  []byte("content"),
		shaSumsAsset: []byte("abc123  some-other-file.tar.gz\n"),
	}
	srv, rel := newTestServer(t, tag, assets)
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	archive, sha, _ := rel.SelectAsset()
	dir := t.TempDir()
	if _, err := c.DownloadAndVerify(archive, sha, dir); err == nil {
		t.Error("expected error for missing sha entry, got nil")
	}
}
