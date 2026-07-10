package selfupdate

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// setupFakeGitHub 构造一个含 agent+server 二进制的 tar.gz 归档 release，
// 返回测试服务器与用于注入的 Client。归档中的 agent/server 内容取自参数。
func setupFakeGitHub(t *testing.T, tag, agentContent, serverContent string) (*httptest.Server, *Client, *Release) {
	t.Helper()

	// 构造 tar.gz
	tmpDir := t.TempDir()
	archivePath := filepath.Join(tmpDir, AssetName(tag))
	f, _ := os.Create(archivePath)
	gw := gzip.NewWriter(f)
	tw := tar.NewWriter(gw)
	writeEntry := func(name, content string) {
		tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(content)), Typeflag: tar.TypeReg})
		tw.Write([]byte(content))
	}
	subdir := fmt.Sprintf("coding-pet-%s-%s-%s/", tag, runtime.GOOS, runtime.GOARCH)
	writeEntry(subdir+binaryName("agent"), agentContent)
	writeEntry(subdir+binaryName("server"), serverContent)
	tw.Close()
	gw.Close()
	f.Close()

	archiveBytes, _ := os.ReadFile(archivePath)
	shaLine := fmt.Sprintf("%s  %s\n", sha256hex(archiveBytes), AssetName(tag))

	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	mux.HandleFunc("/dl/archive", func(w http.ResponseWriter, r *http.Request) { w.Write(archiveBytes) })
	mux.HandleFunc("/dl/sha", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(shaLine)) })
	rel := &Release{
		TagName: tag,
		Assets: []Asset{
			{Name: AssetName(tag), DownloadURL: srv.URL + "/dl/archive"},
			{Name: shaSumsAsset, DownloadURL: srv.URL + "/dl/sha"},
		},
	}
	mux.HandleFunc(fmt.Sprintf("/repos/%s/releases", repoSlug), func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode([]Release{*rel})
	})
	return srv, &Client{APIBase: srv.URL, HTTP: srv.Client()}, rel
}

// makeFakeExe 在临时目录创建"当前二进制"占位文件并返回其路径。
func makeFakeExe(t *testing.T, kind string) string {
	t.Helper()
	dir := t.TempDir()
	name := "coding-pet-" + kind
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("OLD-BINARY"), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestCheckAndUpdate_HappyPath(t *testing.T) {
	srv, client, _ := setupFakeGitHub(t, "v9.9.9", "NEW-AGENT", "NEW-SERVER")
	defer srv.Close()

	exePath := makeFakeExe(t, "agent")

	var restartCalled, exitCalled bool
	var restartArgs []string
	beforeCalled := false

	updated, err := CheckAndUpdate(Options{
		Kind:           "agent",
		CurrentVersion: "v1.0.0",
		BeforeRestart:  func() error { beforeCalled = true; return nil },
		client:         client,
		exePath:        exePath,
		restart: func(path string, args []string, log string) error {
			restartCalled = true
			restartArgs = args
			return nil
		},
		exit: func(code int) { exitCalled = true },
	})
	if err != nil {
		t.Fatalf("CheckAndUpdate: %v", err)
	}
	if !updated {
		t.Error("updated = false, want true")
	}
	if !restartCalled {
		t.Error("restart hook not called")
	}
	if !exitCalled {
		t.Error("exit hook not called")
	}
	// 目标二进制应已被替换
	got, _ := os.ReadFile(exePath)
	if string(got) != "NEW-AGENT" {
		t.Errorf("target binary content = %q, want NEW-AGENT", got)
	}
	// BeforeRestart 应被调用
	if !beforeCalled {
		t.Error("BeforeRestart not called")
	}
	// os.Args[1:] 透传
	_ = restartArgs // 只断言被调用，参数由 os.Args 决定
}

func TestCheckAndUpdate_AlreadyUpToDate(t *testing.T) {
	srv, client, _ := setupFakeGitHub(t, "v1.0.0", "NEW-AGENT", "NEW-SERVER")
	defer srv.Close()

	exePath := makeFakeExe(t, "agent")

	restartCalled := false
	exitCalled := false
	updated, err := CheckAndUpdate(Options{
		Kind:           "agent",
		CurrentVersion: "v1.0.0", // 与远端相同
		client:         client,
		exePath:        exePath,
		restart:        func(path string, args []string, log string) error { restartCalled = true; return nil },
		exit:           func(code int) { exitCalled = true },
	})
	if err != nil {
		t.Fatalf("CheckAndUpdate: %v", err)
	}
	if updated {
		t.Error("updated = true, want false")
	}
	if restartCalled {
		t.Error("restart should not be called when already up-to-date")
	}
	if exitCalled {
		t.Error("exit should not be called when already up-to-date")
	}
	got, _ := os.ReadFile(exePath)
	if string(got) != "OLD-BINARY" {
		t.Errorf("target should be untouched, got %q", got)
	}
}

func TestCheckAndUpdate_DowngradeBlocked(t *testing.T) {
	srv, client, _ := setupFakeGitHub(t, "v1.0.0", "NEW", "NEW")
	defer srv.Close()

	exePath := makeFakeExe(t, "agent")
	restartCalled := false

	updated, err := CheckAndUpdate(Options{
		Kind:           "agent",
		CurrentVersion: "v2.0.0", // 本地更新
		client:         client,
		exePath:        exePath,
		restart:        func(path string, args []string, log string) error { restartCalled = true; return nil },
		exit:           func(code int) {},
	})
	if err != nil {
		t.Fatalf("CheckAndUpdate: %v", err)
	}
	if updated {
		t.Error("should not downgrade")
	}
	if restartCalled {
		t.Error("restart should not fire for older remote (R3 defense)")
	}
}

func TestCheckAndUpdate_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	client := &Client{APIBase: srv.URL, HTTP: srv.Client()}

	exePath := makeFakeExe(t, "agent")
	restartCalled := false
	exitCalled := false
	updated, err := CheckAndUpdate(Options{
		Kind:           "agent",
		CurrentVersion: "v1.0.0",
		client:         client,
		exePath:        exePath,
		restart:        func(path string, args []string, log string) error { restartCalled = true; return nil },
		exit:           func(code int) { exitCalled = true },
	})
	if err == nil {
		t.Error("expected API error, got nil")
	}
	if updated {
		t.Error("updated should be false on error")
	}
	if restartCalled || exitCalled {
		t.Error("restart/exit must not fire on error (R5)")
	}
	// 目标二进制未被触碰
	got, _ := os.ReadFile(exePath)
	if string(got) != "OLD-BINARY" {
		t.Errorf("binary should be untouched, got %q", got)
	}
}

func TestCheckAndUpdate_ServerBeforeRestartOrder(t *testing.T) {
	srv, client, _ := setupFakeGitHub(t, "v9.9.9", "NEW", "NEW")
	defer srv.Close()

	exePath := makeFakeExe(t, "server")

	var events []string
	_, err := CheckAndUpdate(Options{
		Kind:           "server",
		CurrentVersion: "v1.0.0",
		BeforeRestart:  func() error { events = append(events, "before"); return nil },
		client:         client,
		exePath:        exePath,
		restart: func(path string, args []string, log string) error {
			events = append(events, "restart")
			return nil
		},
		exit: func(code int) { events = append(events, "exit") },
	})
	if err != nil {
		t.Fatal(err)
	}
	// 顺序：BeforeRestart 必须在 restart 之前调用（R1：端口释放先于新进程 bind）
	if len(events) < 3 || events[0] != "before" || events[1] != "restart" || events[2] != "exit" {
		t.Errorf("event order = %v, want [before restart exit]", events)
	}
}

func TestInferKind(t *testing.T) {
	cases := map[string]string{
		"/opt/coding-pet-agent":      "agent",
		"/opt/coding-pet-server.exe": "server",
		"C:\\bin\\coding-pet-agent":  "agent",
		"/random":                    "",
	}
	for path, want := range cases {
		if got := InferKind(path); got != want {
			t.Errorf("InferKind(%q) = %q, want %q", path, got, want)
		}
	}
}
