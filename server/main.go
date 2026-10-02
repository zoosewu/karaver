package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"karaver/internal/api"
	"karaver/internal/config"
	"karaver/internal/db"
	"karaver/internal/library"
	"karaver/internal/room"
	"karaver/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// The image has no shell or curl, so the container HEALTHCHECK runs the binary itself.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.Printf("karaver %s", version)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Open(filepath.Join(cfg.DataDir, "karaver.db"))
	if err != nil {
		log.Fatalf("database: %v (is %s writable by this container's user?)", err, cfg.DataDir)
	}
	defer database.Close()

	lib := library.New(database, cfg)
	lib.StartScan()

	rooms, err := room.NewManager(database)
	if err != nil {
		log.Fatalf("load rooms: %v", err)
	}

	srv, err := api.New(cfg, database, lib, rooms, web.FS())
	if err != nil {
		log.Fatalf("api: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go rooms.RunIdleSweeper(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Listen,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Printf("listening on %s (public URL %s)", cfg.Listen, cfg.PublicURL)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	httpServer.Shutdown(shutdownCtx)
}

// healthcheck probes /healthz on the local listener; exit code 0 means healthy.
func healthcheck() int {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	client := http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/healthz")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "status", resp.StatusCode)
		return 1
	}
	return 0
}
