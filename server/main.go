package main

import (
	"context"
	"errors"
	"log"
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
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.Printf("karaver %s", version)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Open(filepath.Join(cfg.DataDir, "karaver.db"))
	if err != nil {
		log.Fatalf("database: %v", err)
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
