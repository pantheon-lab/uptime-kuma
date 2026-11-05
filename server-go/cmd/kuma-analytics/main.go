// Command kuma-analytics runs monitor checks, stores beats in TimescaleDB and serves them.
package main

import (
	_ "time/tzdata" // The container image has no timezone files, and KUMA_TIMEZONE needs them.

	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/certs"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/scheduler"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/ui"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/webui"
)

const minTokenLength = 32

// version is set at build time, to the web UI's version from package.json, which the UI
// compares with its own.
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// loadLocation returns the timezone that alerts show, from KUMA_TIMEZONE.
func loadLocation() (*time.Location, error) {
	name := os.Getenv("KUMA_TIMEZONE")
	if name == "" {
		return time.UTC, nil
	}
	location, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("KUMA_TIMEZONE: %w", err)
	}
	return location, nil
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

	concurrency := 2000
	if raw := os.Getenv("CHECK_CONCURRENCY"); raw != "" {
		v, err := strconv.Atoi(raw)
		if err != nil || v < 1 {
			return errors.New("CHECK_CONCURRENCY must be a positive integer")
		}
		concurrency = v
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	shutdownTelemetry, err := telemetry.Setup(ctx, log)
	if err != nil {
		return err
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := shutdownTelemetry(flushCtx); err != nil {
			log.Warn("flush telemetry", "error", err)
		}
	}()

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
	if password := os.Getenv("GRAFANA_DB_PASSWORD"); password != "" {
		if err := db.EnsureReadOnlyRole(ctx, "kuma_grafana", password); err != nil {
			return err
		}
	}

	monitors, err := db.ListMonitors(ctx)
	if err != nil {
		return err
	}
	previous, err := db.LatestStates(ctx)
	if err != nil {
		return err
	}

	hub := api.NewHub()
	writer := scheduler.NewWriter(db, hub.Publish, log)
	go writer.Run()
	checker := check.New(db)
	checker.SetDNSResults(db)
	// The key is saved under Settings > General in the web UI.
	checker.SetSteamKey(func(ctx context.Context) (string, error) {
		settings, err := db.Settings(ctx)
		if err != nil {
			return "", err
		}
		var key string
		_ = json.Unmarshal(settings["steamAPIKey"], &key)
		return key, nil
	})
	defer checker.Close()
	if err := check.PingAllowed(); err != nil {
		log.Warn("ping monitors will fail", "detail", err)
	}
	location, err := loadLocation()
	if err != nil {
		return err
	}
	notifyEnv := notify.DefaultEnv(strings.TrimRight(os.Getenv("KUMA_BASE_URL"), "/"))
	dispatcher := notify.NewDispatcher(db, notifyEnv, location, log)
	defer dispatcher.Close()
	sched := scheduler.New(checker, writer, concurrency, log)
	sched.SetNotifier(dispatcher)
	calendar, err := holidays.New()
	if err != nil {
		return err
	}
	maintenanceService := maintenance.NewService(db, calendar, location, log)
	// Before the scheduler starts, so that no check runs during a window that's already running.
	if err := maintenanceService.Refresh(ctx); err != nil {
		return fmt.Errorf("load maintenance: %w", err)
	}
	sched.SetMaintenance(maintenanceService)
	certService := certs.NewService(db, dispatcher, log)
	sched.SetCertificates(certService)
	go certService.Run(ctx)
	go maintenanceService.Run(ctx)
	sched.Start(monitors, previous)
	apiServer := api.New(db, sched, hub, token, log)
	apiServer.SetNotifyEnv(notifyEnv)
	if raw := os.Getenv("API_KEY_RATE_LIMIT"); raw != "" {
		perSecond, err := strconv.ParseFloat(raw, 64)
		if err != nil || perSecond <= 0 {
			return errors.New("API_KEY_RATE_LIMIT must be a positive number of requests per second")
		}
		apiServer.SetKeyRateLimit(perSecond, max(1, int(perSecond*4)))
	}
	uiServer := ui.New(db, sched, notifyEnv, log, ui.Options{
		Version:     version,
		BaseURL:     strings.TrimRight(os.Getenv("KUMA_BASE_URL"), "/"),
		Location:    location,
		Maintenance: maintenanceService,
		StatusPages: db,
		IndexHTML:   webui.IndexHTML(),
	})
	go uiServer.Run(ctx, hub)
	certService.OnChange(uiServer.BroadcastCertInfo)

	// The REST API, and the web UI's own API. webui.Mount adds the UI's files.
	routes := http.NewServeMux()
	restAPI := apiServer.Handler()
	routes.Handle("/api/v1/", restAPI)
	routes.Handle("/healthz", restAPI)
	routes.Handle("/api/push/", restAPI)
	uiServer.Register(routes)

	server := &http.Server{
		Addr:              addr,
		Handler:           webui.Mount(routes),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
	}
	server.RegisterOnShutdown(hub.Close)
	server.RegisterOnShutdown(uiServer.Close)

	serveErr := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", addr, "retentionDays", retentionDays, "webUI", webui.Built())
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		sched.Stop()
		_ = writer.Close(context.WithoutCancel(ctx))
		return err
	case <-ctx.Done():
	}

	log.Info("shutting down")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	shutdownErr := server.Shutdown(shutdownCtx)
	sched.Stop()
	if err := writer.Close(shutdownCtx); err != nil {
		log.Error("final beat write failed", "error", err)
	}
	if shutdownErr != nil {
		return fmt.Errorf("shutdown: %w", shutdownErr)
	}
	return nil
}
