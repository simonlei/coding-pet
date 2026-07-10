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

// newTestServer 构造一个伪 GitHub：/repos/.../releases 返回单元素列表 [rel]，
// 其余路径按 assets map（path -> content）提供下载。
func newTestServer(t *testing.T, tag string, assets map[string][]byte) (*httptest.Server, *Release) {
	t.Helper()
	return newTestServerReleases(t, []testRelease{{Tag: tag, Assets: assets}})
}

// testRelease 描述一条测试 release，用于构造 /releases 列表。
type testRelease struct {
	Tag        string
	Assets     map[string][]byte
	Prerelease bool
	Draft      bool
}

// newTestServerReleases 构造 mock GitHub /releases 列表端点，按传入顺序返回
// （模拟真实 API 按发布时间倒序）。第一条对应「最新」release。
func newTestServerReleases(t *testing.T, rels []testRelease) (*httptest.Server, *Release) {
	t.Helper()
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)

	var releases []Release
	for _, r := range rels {
		var relAssets []Asset
		for name, content := range r.Assets {
			path := "/dl/" + r.Tag + "/" + name
			relAssets = append(relAssets, Asset{Name: name, DownloadURL: srv.URL + path})
			mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
				w.Write(content)
			})
		}
		releases = append(releases, Release{
			TagName:    r.Tag,
			Assets:     relAssets,
			Prerelease: r.Prerelease,
			Draft:      r.Draft,
		})
	}

	mux.HandleFunc(fmt.Sprintf("/repos/%s/releases", repoSlug), func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		json.NewEncoder(w).Encode(releases)
	})

	var first *Release
	if len(releases) > 0 {
		first = &releases[0]
	}
	return srv, first
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

// TestFetchLatest_SkipsAndroidTag 验证：当仓库最新一条 release 是安卓 tag
// （如 android-v0.1.26，无法解析为 semver）时，FetchLatest 会跳过它，
// 返回下一条能解析为 vX.Y.Z 的服务端 release。这是修复主 bug 的场景。
func TestFetchLatest_SkipsAndroidTag(t *testing.T) {
	srv, _ := newTestServerReleases(t, []testRelease{
		{Tag: "android-v0.1.26", Assets: map[string][]byte{"app.apk": []byte("apk")}},
		{Tag: "v0.1.24", Assets: map[string][]byte{"whatever": []byte("bin")}},
	})
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	rel, err := c.FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest error: %v", err)
	}
	if rel.TagName != "v0.1.24" {
		t.Errorf("tag = %q, want v0.1.24 (android tag must be skipped)", rel.TagName)
	}
}

// TestFetchLatest_SkipsPrereleaseAndDraft 验证：正式发布之前的 draft/prerelease
// 都被跳过，选中的是第一条 published 正式 release。
func TestFetchLatest_SkipsPrereleaseAndDraft(t *testing.T) {
	srv, _ := newTestServerReleases(t, []testRelease{
		{Tag: "v2.0.0", Assets: map[string][]byte{"a": []byte("x")}, Draft: true},
		{Tag: "v1.9.0", Assets: map[string][]byte{"a": []byte("x")}, Prerelease: true},
		{Tag: "v1.8.0", Assets: map[string][]byte{"a": []byte("x")}},
	})
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	rel, err := c.FetchLatest()
	if err != nil {
		t.Fatalf("FetchLatest error: %v", err)
	}
	if rel.TagName != "v1.8.0" {
		t.Errorf("tag = %q, want v1.8.0", rel.TagName)
	}
}

// TestFetchLatest_NoMatchingRelease 验证：如果整个列表里没有任何 semver 正式 release
// （例如仓库只发过 android tag），返回明确错误。
func TestFetchLatest_NoMatchingRelease(t *testing.T) {
	srv, _ := newTestServerReleases(t, []testRelease{
		{Tag: "android-v0.1.26", Assets: map[string][]byte{"a": []byte("x")}},
		{Tag: "android-v0.1.25", Assets: map[string][]byte{"a": []byte("x")}},
	})
	defer srv.Close()

	c := &Client{APIBase: srv.URL, HTTP: srv.Client()}
	if _, err := c.FetchLatest(); err == nil {
		t.Error("expected error when no semver release exists, got nil")
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
