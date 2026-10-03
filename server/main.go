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

	"zkaraver/internal/api"
	"zkaraver/internal/config"
	"zkaraver/internal/db"
	"zkaraver/internal/keys"
	"zkaraver/internal/library"
	"zkaraver/internal/room"
	"zkaraver/web"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

func main() {
	// The image has no shell or curl, so the container HEALTHCHECK runs the binary itself.
	if len(os.Args) > 1 && os.Args[1] == "healthcheck" {
		os.Exit(healthcheck())
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.Printf("zkaraver %s", version)
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	dbPath := filepath.Join(cfg.DataDir, "zkaraver.db")
	if err := migrateLegacyDB(cfg.DataDir, dbPath); err != nil {
		log.Fatalf("database: %v", err)
	}
	database, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("database: %v (is %s writable by this container's user?)", err, cfg.DataDir)
	}
	defer database.Close()

	lib := library.New(database, cfg)
	keyR := keys.New(cfg.DataDir, cfg.MediaDir, lib)
	lib.OnScan = keyR.Cleanup // drops renders of changed videos
	lib.StartScan()

	rooms, err := room.NewManager(database, lib)
	if err != nil {
		log.Fatalf("load rooms: %v", err)
	}

	rooms.SetKeys(keyR)
	srv, err := api.New(cfg, database, lib, rooms, keyR, web.FS())
	if err != nil {
		log.Fatalf("api: %v", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go rooms.RunIdleSweeper(ctx)
	keyR.Start(ctx, rooms.QueuedSongs, rooms.KeysReady)

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
	rooms.Flush()
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

// migrateLegacyDB renames the database from before the zKaraver rename
// (karaver.db, plus its WAL files) when no new database exists yet.
func migrateLegacyDB(dir, newPath string) error {
	if _, err := os.Stat(newPath); err == nil {
		return nil
	}
	old := filepath.Join(dir, "karaver.db")
	if _, err := os.Stat(old); err != nil {
		return nil
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Rename(old+suffix, newPath+suffix); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("migrate %s: %w", old+suffix, err)
		}
	}
	log.Printf("migrated %s to %s", old, newPath)
	return nil
}
