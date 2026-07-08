package selfupdate

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Options 配置一次自更新检查。
type Options struct {
	// Kind 标识当前进程类型："agent" 或 "server"，决定从归档中取哪个二进制。
	Kind string
	// CurrentVersion 是本地运行版本（main.version 注入值）。
	CurrentVersion string
	// BeforeRestart 在替换成功后、spawn 子进程前调用。
	// server 传入包装 srv.Shutdown 的闭包以释放端口；agent 传 nil。
	BeforeRestart func() error

	// 以下字段用于测试注入，生产环境留零值走默认实现。
	client  *Client                                            // nil 时用 NewClient()
	exePath string                                             // 空时用 os.Executable()
	restart func(path string, args []string, log string) error // nil 时用 Restart
	exit    func(code int)                                     // nil 时用 os.Exit
	logFile string                                             // 空时按 exe 目录下 logs/ 推断
}

// binaryName 依 Kind 与当前平台返回归档内应取出的二进制条目名。
func binaryName(kind string) string {
	name := "coding-pet-" + kind
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return name
}

// CheckAndUpdate 执行一次完整自更新：检查最新版 → 若更新则下载/校验/解压/替换 → 重启。
//
// 返回 (updated, err)：
//   - 已是最新：返回 (false, nil)。
//   - 检查/下载/校验/替换任一步失败：返回 (false, err)，不重启、不退出（安全回退，R5）。
//   - 成功替换并 spawn 子进程后：调用 exit(0)（生产环境不返回；测试注入时返回 (true, nil)）。
func CheckAndUpdate(opts Options) (bool, error) {
	client := opts.client
	if client == nil {
		client = NewClient()
	}

	log.Printf("selfupdate: checking latest release from GitHub (kind=%s, local=%s)", opts.Kind, opts.CurrentVersion)
	rel, err := client.FetchLatest()
	if err != nil {
		log.Printf("selfupdate: check failed: %v", err)
		return false, err
	}
	log.Printf("selfupdate: latest release fetched: tag=%s (assets=%d)", rel.TagName, len(rel.Assets))

	newer, err := IsNewer(rel.TagName, opts.CurrentVersion)
	if err != nil {
		log.Printf("selfupdate: version compare failed (remote=%s local=%s): %v", rel.TagName, opts.CurrentVersion, err)
		return false, err
	}
	if !newer {
		log.Printf("selfupdate: already up to date (local=%s, latest=%s)", opts.CurrentVersion, rel.TagName)
		return false, nil
	}
	log.Printf("selfupdate: new version available: %s (local=%s)", rel.TagName, opts.CurrentVersion)

	exePath := opts.exePath
	if exePath == "" {
		exePath, err = os.Executable()
		if err != nil {
			log.Printf("selfupdate: cannot resolve executable path: %v", err)
			return false, err
		}
		// 解析符号链接，取真实路径以便原子替换。
		if resolved, rErr := filepath.EvalSymlinks(exePath); rErr == nil {
			exePath = resolved
		}
	}
	destDir := filepath.Dir(exePath)

	archive, shaSums, err := rel.SelectAsset()
	if err != nil {
		log.Printf("selfupdate: asset selection failed: %v", err)
		return false, err
	}
	log.Printf("selfupdate: selected asset %s, downloading to %s", archive.Name, destDir)

	archivePath, err := client.DownloadAndVerify(archive, shaSums, destDir)
	if err != nil {
		log.Printf("selfupdate: download/verify failed: %v", err)
		return false, err
	}
	log.Printf("selfupdate: download & sha256 verified: %s", archivePath)
	defer os.Remove(archivePath) // 解压后清理归档

	log.Printf("selfupdate: extracting %s from archive", binaryName(opts.Kind))
	newBin, err := ExtractBinary(archivePath, binaryName(opts.Kind), destDir)
	if err != nil {
		log.Printf("selfupdate: extract failed: %v", err)
		return false, err
	}
	log.Printf("selfupdate: extracted new binary to %s", newBin)

	log.Printf("selfupdate: replacing binary at %s", exePath)
	if err := Replace(newBin, exePath); err != nil {
		os.Remove(newBin)
		log.Printf("selfupdate: replace failed (old process kept running): %v", err)
		return false, err
	}
	log.Printf("selfupdate: binary replaced at %s, restarting to %s", exePath, rel.TagName)

	// server 在此释放端口。
	if opts.BeforeRestart != nil {
		if err := opts.BeforeRestart(); err != nil {
			log.Printf("selfupdate: before-restart hook failed: %v", err)
			// 已替换二进制，仍尝试重启——新进程会用新二进制。
		}
	}

	restart := opts.restart
	if restart == nil {
		restart = Restart
	}
	logFile := opts.logFile
	if logFile == "" {
		logFile = defaultLogFile(destDir, opts.Kind)
	}
	if err := restart(exePath, os.Args[1:], logFile); err != nil {
		log.Printf("selfupdate: spawn new process failed: %v", err)
		return false, err
	}

	log.Printf("selfupdate: new process spawned, exiting old process")
	exit := opts.exit
	if exit == nil {
		exit = os.Exit
	}
	exit(0)
	return true, nil // 生产环境走不到（os.Exit 已终止）；测试注入 exit 时返回。
}

// defaultLogFile 返回 <exeDir>/coding-pet-<kind>.log，与 applog.Setup 落点一致。
func defaultLogFile(exeDir, kind string) string {
	return filepath.Join(exeDir, "coding-pet-"+kind+".log")
}

// InferKind 依可执行文件名推断 Kind（"agent"/"server"），无法判定时返回空串。
func InferKind(exePath string) string {
	base := strings.ToLower(filepath.Base(exePath))
	switch {
	case strings.Contains(base, "agent"):
		return "agent"
	case strings.Contains(base, "server"):
		return "server"
	default:
		return ""
	}
}
