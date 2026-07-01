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

	"github.com/simonlei/coding-pet-dashboard/internal/collector"
	"github.com/simonlei/coding-pet-dashboard/internal/protocol"
)

const version = "0.1.0"

func main() {
	// flag 解析（环境变量作为默认值）
	serverURL := flag.String("server", envOr("DASHBOARD_SERVER", ""), "Dashboard server URL (e.g. http://192.168.1.100:3000)")
	token := flag.String("token", envOr("DASHBOARD_TOKEN", ""), "Auth token (optional)")
	machineID := flag.String("id", envOr("DASHBOARD_ID", ""), "Unique machine ID (default: hostname)")
	hostname := flag.String("hostname", "", "Display hostname (default: os.Hostname())")
	interval := flag.Duration("interval", 1*time.Second, "Report interval")
	flag.Parse()

	if *serverURL == "" {
		log.Fatal("--server is required (or set DASHBOARD_SERVER)")
	}

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

	log.Printf("Starting coding-pet-agent v%s, machine_id=%s, server=%s, interval=%s",
		version, actualMachineID, *serverURL, *interval)

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
