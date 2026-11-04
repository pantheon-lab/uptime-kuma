// Command kuma-analytics ingests monitor beats and serves analytics from TimescaleDB.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const minTokenLength = 32

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	token := os.Getenv("ANALYTICS_API_TOKEN")
	if len(token) < minTokenLength {
		return fmt.Errorf("ANALYTICS_API_TOKEN must be at least %d characters", minTokenLength)
	}
	addr := os.Getenv("LISTEN_ADDR")
	if addr == "" {
		addr = "127.0.0.1:3002"
	}
	retentionDays := 365
	if raw := os.Getenv("RETENTION_DAYS"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 0 {
			return errors.New("RETENTION_DAYS must be a non-negative integer, 0 keeps raw beats forever")
		}
		retentionDays = v
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := db.SetRetention(ctx, retentionDays); err != nil {
		return err
	}

	hub := api.NewHub()
	server := &http.Server{
		Addr:              addr,
		Handler:           api.New(db, hub, token, log).Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	server.RegisterOnShutdown(hub.Close)

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "retentionDays", retentionDays)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}
