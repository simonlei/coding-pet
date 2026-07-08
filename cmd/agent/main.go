package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/applog"
	"github.com/simonlei/coding-pet-dashboard/internal/collector"
	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
	"github.com/simonlei/coding-pet-dashboard/internal/selfupdate"
)

// version 由 CI 通过 -ldflags "-X main.version=..." 注入，必须是 var（const 会使 -X 静默失效）。
// 默认值 "dev" 用于标识未经 CI 注入的本地构建。
var version = "dev"

func main() {
	// flag 解析（环境变量作为默认值）
	serverURL := flag.String("server", envOr("DASHBOARD_SERVER", ""), "Dashboard server URL (e.g. http://192.168.1.100:3000)")
	token := flag.String("token", envOr("DASHBOARD_TOKEN", ""), "Auth token (optional)")
	machineID := flag.String("id", envOr("DASHBOARD_ID", ""), "Unique machine ID (default: hostname)")
	hostname := flag.String("hostname", "", "Display hostname (default: os.Hostname())")
	interval := flag.Duration("interval", 1*time.Second, "Report interval")
	showVersion := flag.Bool("version", false, "Print version and exit")
	selfUpdate := flag.Bool("self-update", false, "Check GitHub for a newer release and update if available, then exit")
	autoUpdate := flag.Bool("auto-update", envAutoUpdate(), "Enable periodic auto-update (env: CODING_PET_AUTO_UPDATE=false to disable)")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		os.Exit(0)
	}

	// Windows: 清理上次升级留下的 .old 文件（Unix 下 no-op）
	if exePath, err := os.Executable(); err == nil {
		selfupdate.CleanupOld(exePath)
	}

	if *selfUpdate {
		runSelfUpdateOnce("agent", nil)
		return
	}

	if *serverURL == "" {
		log.Fatal("--server is required (or set DASHBOARD_SERVER)")
	}

	applog.Setup("agent")

	// 解析 hostname 和 machine_id
	actualHostname, err := os.Hostname()
	if err != nil {
		actualHostname = "unknown"
	}
	if *hostname != "" {
		actualHostname = *hostname
	}
	actualMachineID := *machineID
	if actualMachineID == "" {
		actualMachineID = actualHostname
	}

	log.Printf("Starting coding-pet-agent %s, machine_id=%s, server=%s, interval=%s",
		version, actualMachineID, *serverURL, *interval)

	// 自动更新（默认开启，agent 无端口，BeforeRestart=nil）
	if *autoUpdate {
		selfupdate.StartAuto(selfupdate.Options{
			Kind:           "agent",
			CurrentVersion: version,
		}, selfupdate.DefaultInterval)
	} else {
		log.Printf("selfupdate: auto-update disabled by flag/env")
	}

	c := collector.New()

	// 定时采集 + fire-and-forget 上报
	ticker := time.NewTicker(*interval)
	defer ticker.Stop()

	// 启动时立即上报一次
	report(c, *serverURL, *token, actualMachineID, actualHostname)

	for range ticker.C {
		// fire-and-forget：不阻塞下一轮采集
		go report(c, *serverURL, *token, actualMachineID, actualHostname)
	}
}

// report 采集并上报，失败只记录日志
func report(c *collector.Collector, serverURL, token, machineID, hostname string) {
	sessions := c.CollectSessions()

	// 过滤已终止的 session，只上报活跃 session
	var activeSessions []protocol.SessionInfo
	for _, s := range sessions {
		if s.State != protocol.StateTerminated {
			activeSessions = append(activeSessions, s)
		}
	}

	payload := protocol.AgentReport{
		MachineID: machineID,
		Hostname:  hostname,
		ReportAt:  time.Now().UnixMilli(),
		Sessions:  activeSessions,
		AgentVer:  version,
	}

	data, err := json.Marshal(payload)
	if err != nil {
		log.Printf("marshal error: %v", err)
		return
	}

	req, err := http.NewRequest(http.MethodPost, serverURL+"/api/report", bytes.NewReader(data))
	if err != nil {
		log.Printf("create request error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("report error: %v (will retry in %s)", err, "next interval")
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("report failed: HTTP %d", resp.StatusCode)
		return
	}

	fmt.Printf(".")
}

// envOr 读取环境变量，不存在时返回默认值
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// envAutoUpdate 读取 CODING_PET_AUTO_UPDATE 决定 --auto-update flag 的默认值。
// 显式设置 "false"/"0" 关闭；未设置或其他值默认开启。
func envAutoUpdate() bool {
	v := os.Getenv("CODING_PET_AUTO_UPDATE")
	if v == "false" || v == "0" {
		return false
	}
	return true
}

// runSelfUpdateOnce 手动触发一次检查更新；成功升级则 os.Exit（不返回），
// 无新版或失败时打印结果并 return。
func runSelfUpdateOnce(kind string, beforeRestart func() error) {
	log.Printf("selfupdate: manual check triggered (kind=%s, local=%s)", kind, version)
	updated, err := selfupdate.CheckAndUpdate(selfupdate.Options{
		Kind:           kind,
		CurrentVersion: version,
		BeforeRestart:  beforeRestart,
	})
	if err != nil {
		log.Printf("selfupdate: manual check failed: %v", err)
		os.Exit(1)
	}
	if !updated {
		log.Printf("selfupdate: no update needed")
	}
	// updated=true 时进程已被 os.Exit(0) 终止，不会走到这
}
