package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/selfupdate"
	"github.com/simonlei/coding-pet-dashboard/internal/server"
)

// version 由 CI 通过 -ldflags "-X main.version=..." 注入，必须是 var（const 会使 -X 静默失效）。
// 默认值 "dev" 用于标识未经 CI 注入的本地构建。
var version = "dev"

func main() {
	port := flag.String("port", "3000", "HTTP listen port")
	token := flag.String("token", "", "Auth token for Agent reports (optional)")
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
		// 手动一次性触发；server 未启动时无需 BeforeRestart
		runSelfUpdateOnce(nil)
		return
	}

	addr := net.JoinHostPort("0.0.0.0", *port)

	store := server.NewStore()
	handler := server.NewHandler(store, *token, version)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
	}

	// 自动更新（默认开启，server 需在升级前关监听释放端口）
	if *autoUpdate {
		selfupdate.StartAuto(selfupdate.Options{
			Kind:           "server",
			CurrentVersion: version,
			BeforeRestart: func() error {
				log.Printf("selfupdate: releasing listener before restart")
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				return srv.Shutdown(ctx)
			},
		}, selfupdate.DefaultInterval)
	} else {
		log.Printf("selfupdate: auto-update disabled by flag/env")
	}

	// 后台 goroutine：每秒检测掉线
	go func() {
		ticker := time.NewTicker(1 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			store.CheckOffline()
		}
	}()

	// 优雅关闭
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		log.Printf("coding-pet-server %s listening on %s", version, addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server error: %v", err)
		}
	}()

	<-quit
	log.Println("Shutting down...")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("Server exited")
}

// envAutoUpdate 读取 CODING_PET_AUTO_UPDATE 决定 --auto-update flag 的默认值。
func envAutoUpdate() bool {
	v := os.Getenv("CODING_PET_AUTO_UPDATE")
	if v == "false" || v == "0" {
		return false
	}
	return true
}

// runSelfUpdateOnce 手动触发一次检查更新。
func runSelfUpdateOnce(beforeRestart func() error) {
	log.Printf("selfupdate: manual check triggered (kind=server, local=%s)", version)
	updated, err := selfupdate.CheckAndUpdate(selfupdate.Options{
		Kind:           "server",
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
}
