package main

import (
	"context"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/simonlei/coding-pet-dashboard/internal/server"
)

const version = "0.1.0"

func main() {
	port := flag.String("port", "3000", "HTTP listen port")
	token := flag.String("token", "", "Auth token for Agent reports (optional)")
	flag.Parse()

	addr := net.JoinHostPort("0.0.0.0", *port)

	store := server.NewStore()
	handler := server.NewHandler(store, *token)

	mux := http.NewServeMux()
	handler.RegisterRoutes(mux)

	srv := &http.Server{
		Addr:    addr,
		Handler: mux,
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
		log.Printf("coding-pet-server v%s listening on %s", version, addr)
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
