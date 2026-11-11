// Command kuma-analytics runs monitor checks, stores beats in TimescaleDB and serves them.
package main

import (
	_ "time/tzdata" // The container image has no timezone files, and KUMA_TIMEZONE needs them.

	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/certs"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/mcp"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/scheduler"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/ui"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/updates"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/webui"
)

const minTokenLength = 32

// version is set at build time, to the web UI's version from package.json, which the UI
// compares with its own.
var version = "dev"

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if len(os.Args) > 1 && os.Args[1] == "rekey" {
		if err := rekey(log); err != nil {
			log.Error("fatal", "error", err)
			os.Exit(1)
		}
		return
	}
	if err := run(log); err != nil {
		log.Error("fatal", "error", err)
		os.Exit(1)
	}
}

// openSecrets gives the store KUMA_SECRET_KEY, and checks the stored secrets: plain ones are
// encrypted when there's a key, and with rekey, those encrypted with an older key are encrypted
// again with the current one. It fails when one is encrypted and the key to open it is missing.
func openSecrets(ctx context.Context, db *store.Store, rekey bool, log *slog.Logger) error {
	keys, err := secrets.FromEnv()
	if err != nil {
		return err
	}
	if rekey && !keys.Enabled() {
		return errors.New("rekey needs KUMA_SECRET_KEY, the new key, and KUMA_SECRET_KEY_OLD, the old ones")
	}
	db.SetKeyring(keys)
	report, err := db.SealSecrets(ctx, rekey)
	if err != nil {
		return fmt.Errorf("stored secrets: %w", err)
	}
	for what, n := range report.Sealed {
		log.Info("encrypted stored secrets", "what", what, "count", n, "key", keys.CurrentID())
	}
	for what, n := range report.Resealed {
		log.Info("encrypted stored secrets again with the current key", "what", what, "count", n, "key", keys.CurrentID())
	}
	if !keys.Enabled() {
		log.Warn("stored secrets aren't encrypted: set KUMA_SECRET_KEY to encrypt them")
	}
	return nil
}

// rekey encrypts every stored secret with KUMA_SECRET_KEY, opening those encrypted with an older
// key with KUMA_SECRET_KEY_OLD. Afterwards the old key isn't needed.
func rekey(log *slog.Logger) error {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := db.Migrate(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	if err := openSecrets(ctx, db, true, log); err != nil {
		return err
	}
	log.Info("every stored secret is encrypted with the current key; KUMA_SECRET_KEY_OLD can be removed")
	return nil
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
		if err != nil || v < 0 || v > store.MaxRetentionDays {
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
	if err := openSecrets(ctx, db, false, log); err != nil {
		return err
	}
	// Settings > Monitor History decides the retention. RETENTION_DAYS is only its first value.
	days, saved, err := db.RetentionDays(ctx, retentionDays)
	if err != nil {
		return err
	}
	if saved && os.Getenv("RETENTION_DAYS") != "" && days != retentionDays {
		log.Warn("RETENTION_DAYS is ignored, as the retention was saved under Settings > Monitor History",
			"RETENTION_DAYS", retentionDays, "setting", days)
	}
	if err := db.SetRetention(ctx, days); err != nil {
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
	// Optional: without one, Globalping's anonymous limits apply. Saved under Settings > General.
	checker.SetGlobalpingToken(func(ctx context.Context) (string, error) {
		settings, err := db.Settings(ctx)
		if err != nil {
			return "", err
		}
		var token string
		_ = json.Unmarshal(settings["globalpingApiToken"], &token)
		return token, nil
	})
	checker.SetDockerHosts(db)
	checker.SetBrowsers(db)
	checker.SetProxies(db)
	defer checker.Close()
	defer check.StopBrowser()
	if err := check.PingAllowed(); err != nil {
		log.Warn("ping monitors will fail", "detail", err)
	}
	location, err := loadLocation()
	if err != nil {
		return err
	}
	notifyEnv := notify.DefaultEnv(strings.TrimRight(os.Getenv("KUMA_BASE_URL"), "/"))
	notifyEnv.WebpushKeys = func(ctx context.Context) (string, string, error) {
		return db.WebpushKeys(ctx, func() (string, string, error) {
			private, public, err := webpush.GenerateVAPIDKeys()
			return public, private, err
		})
	}
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
	domainService := domains.NewService(db, dispatcher, log)
	sched.SetDomains(domainService)
	go domainService.Run(ctx)
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
	var passwordLeaked func(context.Context, string) (bool, error)
	if envTrue(os.Getenv("KUMA_DISABLE_LEAK_PASSWORD_CHECK")) {
		log.Warn("the leaked password check is off (KUMA_DISABLE_LEAK_PASSWORD_CHECK)")
	} else {
		passwordLeaked = ui.PwnedPasswords(nil, ui.PwnedPasswordsURL)
	}
	var updateChecker *updates.Checker
	if url := os.Getenv("KUMA_UPDATE_CHECK_URL"); url != "off" {
		if url == "" {
			url = updates.DefaultURL
		}
		updateChecker = updates.New(url, nil, func(ctx context.Context) (bool, bool, error) {
			settings, err := db.Settings(ctx)
			if err != nil {
				return false, false, err
			}
			// As in the UI, checking is on until it's turned off, and betas are off.
			check, beta := true, false
			_ = json.Unmarshal(settings["checkUpdate"], &check)
			_ = json.Unmarshal(settings["checkBeta"], &beta)
			return check, beta, nil
		}, log)
		go updateChecker.Run(ctx)
	}
	githubLogin, err := githubOptions()
	if err != nil {
		return err
	}
	googleLogin, err := googleOptions()
	if err != nil {
		return err
	}
	oidcLogin, err := oidcOptions()
	if err != nil {
		return err
	}
	passwordLogin := os.Getenv("KUMA_PASSWORD_LOGIN") == "" || envTrue(os.Getenv("KUMA_PASSWORD_LOGIN"))
	loginFromEnv := os.Getenv("KUMA_PASSWORD_LOGIN") != "" || githubLogin != nil || googleLogin != nil || oidcLogin != nil
	if !passwordLogin && githubLogin == nil && googleLogin == nil && oidcLogin == nil {
		return errors.New("KUMA_PASSWORD_LOGIN=false needs GitHub, Google or OpenID Connect login, or nobody could log in")
	}
	uiServer := ui.New(db, sched, notifyEnv, log, ui.Options{
		GitHub:               githubLogin,
		DisablePasswordLogin: !passwordLogin,
		Google:               googleLogin,
		OIDC:                 oidcLogin,
		GoogleEndpoint:       os.Getenv("KUMA_GOOGLE_ENDPOINT"),
		LoginFromEnv:         loginFromEnv,
		Updates:              updateChecker,
		PasswordLeaked:       passwordLeaked,
		Version:              version,
		BaseURL:              strings.TrimRight(os.Getenv("KUMA_BASE_URL"), "/"),
		Location:             location,
		Maintenance:          maintenanceService,
		StatusPages:          db,
		IndexHTML:            webui.IndexHTML(),
		Domains:              domainService,
		Checker:              checker,
	})
	if err := createInitialAdmin(ctx, uiServer, log); err != nil {
		return err
	}
	// Login settings saved in Settings > Single Sign-On, when no environment variable sets them.
	if err := uiServer.LoadLoginConfig(ctx); err != nil {
		return err
	}
	go uiServer.WatchLoginConfig(ctx)
	// kuma-import writes to the database directly: apply what it wrote, and show it on open pages.
	go db.ListenImported(ctx, log, func(ctx context.Context) error {
		// A stalled query fails the attempt, which is then tried again, rather than holding up the listener.
		ctx, cancel := context.WithTimeout(ctx, reloadTimeout)
		defer cancel()
		if err := maintenanceService.Refresh(ctx); err != nil {
			return fmt.Errorf("reload maintenance: %w", err)
		}
		// Before reading: a monitor changed here meanwhile is newer than what's read.
		since := sched.Generation()
		monitors, err := db.ListMonitors(ctx)
		if err != nil {
			return fmt.Errorf("reload monitors: %w", err)
		}
		sched.Sync(monitors, since)
		return uiServer.Reload(ctx)
	})
	go uiServer.Run(ctx, hub)
	certService.OnChange(uiServer.BroadcastCertInfo)
	domainService.OnChange(uiServer.BroadcastDomainInfo)

	// The REST API, and the web UI's own API. webui.Mount adds the UI's files.
	routes := http.NewServeMux()
	restAPI := apiServer.Handler()
	routes.Handle("/api/v1/", restAPI)
	routes.Handle("/healthz", restAPI)
	routes.Handle("/api/push/", restAPI)
	mcpHandler, err := mcp.Handler(mcp.Options{
		API: restAPI, OpenAPI: api.OpenAPI(), Version: version, Authenticate: apiServer.Authenticate,
	})
	if err != nil {
		return err
	}
	routes.Handle("/mcp", mcpHandler)
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
		log.Info("listening", "addr", addr, "retentionDays", days, "webUI", webui.Built())
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

func envTrue(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	return value != "" && value != "false" && value != "0" && value != "no"
}

// createInitialAdmin creates the first account from KUMA_INITIAL_ADMIN_USERNAME and
// KUMA_INITIAL_ADMIN_PASSWORD, which are ignored once an account exists.
// reloadTimeout bounds one reload after an import.
const reloadTimeout = 2 * time.Minute

func createInitialAdmin(ctx context.Context, server *ui.Server, log *slog.Logger) error {
	username := strings.TrimSpace(os.Getenv("KUMA_INITIAL_ADMIN_USERNAME"))
	password := os.Getenv("KUMA_INITIAL_ADMIN_PASSWORD")
	if username == "" && password == "" {
		return nil
	}
	if username == "" || password == "" {
		return errors.New("KUMA_INITIAL_ADMIN_USERNAME and KUMA_INITIAL_ADMIN_PASSWORD must be set together")
	}
	created, err := server.CreateInitialAdmin(ctx, username, password)
	if err != nil {
		return fmt.Errorf("KUMA_INITIAL_ADMIN_USERNAME: %w", err)
	}
	if created {
		log.Info("created the first account from KUMA_INITIAL_ADMIN_USERNAME", "username", username)
	} else {
		log.Info("an account exists, so KUMA_INITIAL_ADMIN_USERNAME and KUMA_INITIAL_ADMIN_PASSWORD are ignored")
	}
	return nil
}

// envList reads a comma-separated list, lowercase, without blanks.
func envList(name string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(name), ",") {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

// googleOptions reads the Google login settings, or returns nil when KUMA_GOOGLE_CLIENT_ID isn't set.
func googleOptions() (*ui.GoogleOptions, error) {
	id := strings.TrimSpace(os.Getenv("KUMA_GOOGLE_CLIENT_ID"))
	if id == "" {
		return nil, nil
	}
	g := &ui.GoogleOptions{
		ClientID:     id,
		ClientSecret: os.Getenv("KUMA_GOOGLE_CLIENT_SECRET"),
		Domains:      envList("KUMA_GOOGLE_DOMAINS"),
		AdminEmails:  envList("KUMA_GOOGLE_ADMIN_EMAILS"),
		ViewerEmails: envList("KUMA_GOOGLE_VIEWER_EMAILS"),
	}
	switch {
	case g.ClientSecret == "":
		return nil, errors.New("KUMA_GOOGLE_CLIENT_SECRET is required with KUMA_GOOGLE_CLIENT_ID")
	case len(g.AdminEmails) == 0 && len(g.ViewerEmails) == 0:
		return nil, errors.New("set KUMA_GOOGLE_ADMIN_EMAILS or KUMA_GOOGLE_VIEWER_EMAILS, the addresses that may log in with Google")
	case slices.Contains(g.ViewerEmails, "*") && len(g.Domains) == 0:
		return nil, errors.New("KUMA_GOOGLE_VIEWER_EMAILS=* needs KUMA_GOOGLE_DOMAINS, or anyone with a Google account could log in")
	}
	return g, nil
}

// oidcOptions reads the OpenID Connect login settings, or returns nil when KUMA_OIDC_ISSUER isn't set.
func oidcOptions() (*ui.OIDCOptions, error) {
	issuer := strings.TrimRight(strings.TrimSpace(os.Getenv("KUMA_OIDC_ISSUER")), "/")
	if issuer == "" {
		return nil, nil
	}
	// Groups keep their case: some providers' are ids.
	groups := func(name string) []string {
		var out []string
		for _, v := range strings.Split(os.Getenv(name), ",") {
			if v = strings.TrimSpace(v); v != "" {
				out = append(out, v)
			}
		}
		return out
	}
	o := &ui.OIDCOptions{
		Name:         cmp.Or(strings.TrimSpace(os.Getenv("KUMA_OIDC_NAME")), "single sign-on"),
		Issuer:       issuer,
		ClientID:     strings.TrimSpace(os.Getenv("KUMA_OIDC_CLIENT_ID")),
		ClientSecret: os.Getenv("KUMA_OIDC_CLIENT_SECRET"),
		Scopes:       groups("KUMA_OIDC_SCOPES"),
		GroupsClaim:  strings.TrimSpace(os.Getenv("KUMA_OIDC_GROUPS_CLAIM")),
		AdminGroups:  groups("KUMA_OIDC_ADMIN_GROUPS"),
		ViewerGroups: groups("KUMA_OIDC_VIEWER_GROUPS"),
		AdminEmails:  envList("KUMA_OIDC_ADMIN_EMAILS"),
		ViewerEmails: envList("KUMA_OIDC_VIEWER_EMAILS"),
	}
	switch {
	case o.ClientID == "" || o.ClientSecret == "":
		return nil, errors.New("KUMA_OIDC_CLIENT_ID and KUMA_OIDC_CLIENT_SECRET are required with KUMA_OIDC_ISSUER")
	case len(o.AdminGroups)+len(o.ViewerGroups)+len(o.AdminEmails)+len(o.ViewerEmails) == 0:
		return nil, errors.New("set KUMA_OIDC_ADMIN_GROUPS, KUMA_OIDC_VIEWER_GROUPS, KUMA_OIDC_ADMIN_EMAILS or KUMA_OIDC_VIEWER_EMAILS: who may log in")
	}
	return o, nil
}

// githubOptions reads the GitHub login settings, or returns nil when KUMA_GITHUB_CLIENT_ID isn't set.
func githubOptions() (*ui.GitHubOptions, error) {
	id := strings.TrimSpace(os.Getenv("KUMA_GITHUB_CLIENT_ID"))
	if id == "" {
		return nil, nil
	}
	teams := func(name string) []string {
		var out []string
		for _, team := range strings.Split(os.Getenv(name), ",") {
			if team = strings.ToLower(strings.TrimSpace(team)); team != "" {
				out = append(out, team)
			}
		}
		return out
	}
	g := &ui.GitHubOptions{
		ClientID:     id,
		ClientSecret: os.Getenv("KUMA_GITHUB_CLIENT_SECRET"),
		Org:          strings.TrimSpace(os.Getenv("KUMA_GITHUB_ORG")),
		AdminTeams:   teams("KUMA_GITHUB_ADMIN_TEAMS"),
		ViewerTeams:  teams("KUMA_GITHUB_VIEWER_TEAMS"),
		WebURL:       strings.TrimRight(cmp.Or(os.Getenv("KUMA_GITHUB_URL"), "https://github.com"), "/"),
		APIURL:       strings.TrimRight(cmp.Or(os.Getenv("KUMA_GITHUB_API_URL"), "https://api.github.com"), "/"),
	}
	switch {
	case g.ClientSecret == "":
		return nil, errors.New("KUMA_GITHUB_CLIENT_SECRET is required with KUMA_GITHUB_CLIENT_ID")
	case g.Org == "":
		return nil, errors.New("KUMA_GITHUB_ORG is required with KUMA_GITHUB_CLIENT_ID: its teams decide who may log in")
	case len(g.AdminTeams) == 0 && len(g.ViewerTeams) == 0:
		return nil, errors.New("set KUMA_GITHUB_ADMIN_TEAMS or KUMA_GITHUB_VIEWER_TEAMS, the team slugs whose members may log in")
	}
	return g, nil
}
