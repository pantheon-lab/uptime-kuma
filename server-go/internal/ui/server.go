// Package ui implements the backend contract of the Vue web UI in src/: login under /api/auth
// (compatible with the better-auth client the UI uses), first-run setup, and the realtime
// protocol under /api/realtime that replaced Socket.IO.
//
// It also serves what visitors see without logging in: status pages, rendered with their data,
// their feeds and badges. Events for features this service doesn't have yet get an empty answer
// when the UI asks for them on page load, and a "not supported yet" error otherwise.
package ui

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Store is the database access the UI needs.
type Store interface {
	HasUser(ctx context.Context) (bool, error)
	CreateFirstUser(ctx context.Context, username, passwordHash string) (store.User, error)
	UserByUsername(ctx context.Context, username string) (store.User, error)
	UserByID(ctx context.Context, id int32) (store.User, error)
	SetPassword(ctx context.Context, userID int32, passwordHash string, keep []byte) error
	UpgradePasswordHash(ctx context.Context, userID int32, old, hash string) error
	SaveGitHubUser(ctx context.Context, githubID int64, login, role, token string, teams []string) (store.User, error)
	GitHubToken(ctx context.Context, userID int32) (string, error)
	SetGitHubChecked(ctx context.Context, userID int32, role string, teams []string) error
	EndUserSessions(ctx context.Context, userID int32) error
	EndOtherUserSessions(ctx context.Context, userID int32, keep []byte) error
	ListAccounts(ctx context.Context) ([]store.Account, error)
	DeleteUser(ctx context.Context, userID int32, methods store.LoginMethods) (store.User, error)
	SetUserRole(ctx context.Context, userID int32, role string, methods store.LoginMethods) (store.User, error)
	StartTOTPSetup(ctx context.Context, userID int32, secret []byte) error
	EnableTOTP(ctx context.Context, userID int32, secret []byte, step int64, keep []byte) error
	DisableTOTP(ctx context.Context, userID int32) error
	UseTOTPStep(ctx context.Context, userID int32, step int64) (bool, error)
	RecordTOTPFailure(ctx context.Context, userID int32, max int, lockFor time.Duration) error
	CreateTwoFactorChallenge(ctx context.Context, tokenHash []byte, c store.TwoFactorChallenge) error
	FindTwoFactorChallenge(ctx context.Context, tokenHash []byte) (store.TwoFactorChallenge, store.User, error)
	CountTwoFactorAttempt(ctx context.Context, tokenHash []byte) (int, error)
	DeleteTwoFactorChallenge(ctx context.Context, tokenHash []byte) (bool, error)
	CreateAPIKey(ctx context.Context, key store.APIKey, hash []byte) (store.APIKey, error)
	ListAPIKeys(ctx context.Context) ([]store.APIKey, error)
	RevokeAPIKey(ctx context.Context, id int64) error
	CreateSession(ctx context.Context, tokenHash []byte, session store.Session) error
	FindSession(ctx context.Context, tokenHash []byte) (store.Session, store.User, error)
	ExtendSession(ctx context.Context, tokenHash []byte, expiresAt time.Time) error
	DeleteSession(ctx context.Context, tokenHash []byte) error
	Settings(ctx context.Context) (map[string]json.RawMessage, error)
	SaveSettings(ctx context.Context, settings map[string]json.RawMessage) error

	ListMonitors(ctx context.Context) ([]monitor.Monitor, error)
	GetMonitor(ctx context.Context, id int32) (monitor.Monitor, error)
	CreateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error)
	UpdateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error)
	SetMonitorActive(ctx context.Context, id int32, active bool) (monitor.Monitor, error)
	DeleteMonitor(ctx context.Context, id int32) error

	LatestBeats(ctx context.Context, monitorID int32, limit int) ([]store.Beat, error)
	LatestBeatPerMonitor(ctx context.Context) ([]store.Beat, error)
	BeatsSince(ctx context.Context, monitorID int32, since time.Time, limit int) ([]store.Beat, error)
	ImportantBeats(ctx context.Context, monitorID *int32, filter store.EventFilter, offset, limit int) ([]store.Beat, error)
	CountImportantBeats(ctx context.Context, monitorID *int32, filter store.EventFilter) (int64, error)
	ClearEvents(ctx context.Context, monitorID *int32) error
	ClearMonitorStats(ctx context.Context, monitorID int32) error
	ClearAllStats(ctx context.Context) error
	DatabaseSize(ctx context.Context) (int64, error)
	SetRetention(ctx context.Context, days int) error
	Chart(ctx context.Context, monitorID int32, hours int) ([]store.ChartPoint, error)
	ChartRange(ctx context.Context, monitorID int32, from, to time.Time) ([]store.ChartPoint, error)
	Stats(ctx context.Context) ([]store.MonitorStats, error)

	ListNotifications(ctx context.Context) ([]store.Notification, error)
	GetNotification(ctx context.Context, id int32) (store.Notification, error)
	CreateNotification(ctx context.Context, n store.Notification, applyToAll bool) (store.Notification, error)
	UpdateNotification(ctx context.Context, n store.Notification, applyToAll bool) (store.Notification, error)
	DeleteNotification(ctx context.Context, id int32) error
	SetMonitorNotifications(ctx context.Context, monitorID int32, ids []int32) error
	AllMonitorNotificationIDs(ctx context.Context) (map[int32][]int32, error)
	LinkDefaultNotifications(ctx context.Context, monitorID int32) error

	ListTags(ctx context.Context) ([]store.Tag, error)
	CreateTag(ctx context.Context, name, color string) (store.Tag, error)
	UpdateTag(ctx context.Context, tag store.Tag) error
	DeleteTag(ctx context.Context, id int32) error
	AddMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error
	UpdateMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error
	DeleteMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error
	AllMonitorTags(ctx context.Context) (map[int32][]store.MonitorTag, error)
	AllTLSInfo(ctx context.Context) (map[int32]string, error)

	ListMaintenances(ctx context.Context) ([]maintenance.Window, error)
	GetMaintenance(ctx context.Context, id int32) (maintenance.Window, error)
	CreateMaintenance(ctx context.Context, w maintenance.Window) (maintenance.Window, error)
	UpdateMaintenance(ctx context.Context, w maintenance.Window) (maintenance.Window, error)
	SetMaintenanceActive(ctx context.Context, id int32, active bool) error
	DeleteMaintenance(ctx context.Context, id int32) error
	SetMaintenanceMonitors(ctx context.Context, maintenanceID int32, monitorIDs []int32) error
	MaintenanceMonitorLinks(ctx context.Context) (map[int32][]int32, error)
	SetHolidayOverride(ctx context.Context, o holidays.Override) error
	DeleteHolidayOverride(ctx context.Context, country, date string) error

	ResourceStore
}

// Updates knows the newest release.
type Updates interface {
	Latest() string
	Recheck()
}

// Scheduler runs checks. Upsert starts, restarts or stops a monitor as its Active field says.
type Scheduler interface {
	Upsert(m monitor.Monitor)
	Remove(ctx context.Context, id int32) error
}

// Beats delivers new beats as they are stored.
type Beats interface {
	Subscribe() (<-chan store.Beat, func())
}

// Options configures the server.
type Options struct {
	// Version is shown in the UI's footer and settings.
	Version string
	// BaseURL is the public address of the UI, used when the UI hasn't saved its own.
	BaseURL string
	// Location is the server's timezone.
	Location *time.Location
	// Maintenance tracks maintenance windows and holds the holiday calendars. Required.
	Maintenance *maintenance.Service
	// SetupWindow is how long after start the first account may be created. Zero means 10 minutes.
	SetupWindow time.Duration
	// StatusPages stores status pages. Without it, they aren't supported.
	StatusPages StatusPageStore
	// Domains tracks domain expiry. Without it, the form says nothing about domains.
	Domains Domains
	// PasswordLeaked reports whether a new password is a known leaked one, which is then
	// refused. Without it, passwords aren't checked. See PwnedPasswords.
	PasswordLeaked func(ctx context.Context, password string) (bool, error)
	// Updates knows the newest release, for the UI's update notice. Without it there's none.
	Updates Updates
	// GitHub turns on logging in with GitHub.
	GitHub *GitHubOptions
	// DisablePasswordLogin refuses logging in with a password, once GitHub login works.
	DisablePasswordLogin bool
	// IndexHTML is the web UI's index.html, which status pages are rendered into. Without it,
	// /status/ answers 404, and the data is still at /api/status-page/.
	IndexHTML []byte
	// Checker runs the Edit Monitor form's test. Without it, the test isn't supported.
	Checker Checker
}

// Checker runs one check.
type Checker interface {
	Run(ctx context.Context, m monitor.Monitor) monitor.Result
}

// Server serves the UI's API.
type Server struct {
	store     Store
	scheduler Scheduler
	notifyEnv *notify.Env
	log       *slog.Logger
	opts      Options
	now       func() time.Time
	started   time.Time
	limiter   *loginLimiter
	// maintenance tracks maintenance windows.
	maintenance *maintenance.Service
	// pages is nil when status pages aren't supported.
	pages StatusPageStore
	cache *publicCache
	// subs sends incidents to status pages' subscribers.
	subs *deliveries

	setupMu sync.Mutex

	mu    sync.Mutex
	conns map[string]*conn
	// beatsSeen is set by new beats and cleared when stats are sent.
	beatsSeen bool

	// githubMu guards the GitHub team checks running, and when each account's last one failed.
	githubMu       sync.Mutex
	githubChecking map[int32]bool
	githubFailed   map[int32]time.Time
}

// New creates the server.
func New(s Store, scheduler Scheduler, notifyEnv *notify.Env, log *slog.Logger, opts Options) *Server {
	if opts.Location == nil {
		opts.Location = time.UTC
	}
	if opts.SetupWindow == 0 {
		opts.SetupWindow = 10 * time.Minute
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	server := &Server{
		maintenance: opts.Maintenance,
		store:       s,
		scheduler:   scheduler,
		notifyEnv:   notifyEnv,
		log:         log,
		opts:        opts,
		now:         time.Now,
		started:     time.Now(),
		limiter:     newLoginLimiter(),
		conns:       map[string]*conn{},
		pages:       opts.StatusPages,
		cache:       newPublicCache(),
		subs:        newDeliveries(),

		githubChecking: map[int32]bool{},
		githubFailed:   map[int32]time.Time{},
	}
	// A window started or ended, so open pages show its new status.
	opts.Maintenance.OnChange(func() {
		ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
		defer cancel()
		server.cache.clear()
		server.broadcastMaintenanceList(ctx)
	})
	return server
}

// Register adds the UI's routes to mux.
func (s *Server) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/entry-page", s.entryPage)
	mux.HandleFunc("POST /api/setup", s.sameOrigin(s.setup))
	mux.HandleFunc("POST /api/auth/sign-in/username", s.sameOrigin(s.signIn))
	mux.HandleFunc("GET /api/auth/get-session", s.getSession)
	mux.HandleFunc("POST /api/auth/sign-out", s.sameOrigin(s.signOut))
	mux.HandleFunc("POST /api/auth/change-password", s.sameOrigin(s.changePassword))
	mux.HandleFunc("POST /api/auth/two-factor/enable", s.sameOrigin(s.enableTwoFactor))
	mux.HandleFunc("POST /api/auth/two-factor/disable", s.sameOrigin(s.disableTwoFactor))
	mux.HandleFunc("POST /api/auth/two-factor/verify-totp", s.sameOrigin(s.verifyTOTP))
	mux.HandleFunc("GET /api/auth/github/start", s.githubStart)
	mux.HandleFunc("GET /api/auth/github/callback", s.githubCallback)
	mux.HandleFunc("/api/auth/", s.authNotSupported)
	mux.HandleFunc("GET /api/realtime/stream", s.stream)
	mux.HandleFunc("POST /api/realtime/send", s.sameOrigin(s.send))
	mux.HandleFunc("GET /screenshots/{file}", s.screenshot)
	mux.HandleFunc("GET /robots.txt", s.robots)
	if s.pages != nil {
		s.registerPublic(mux)
		s.registerSubscriptions(mux)
	} else {
		mux.HandleFunc("GET /{$}", s.root)
	}
}

// Run sends new beats and refreshed stats to logged-in browsers until ctx ends.
func (s *Server) Run(ctx context.Context, beats Beats) {
	ch, unsubscribe := beats.Subscribe()
	defer func() { unsubscribe() }()
	ticker := time.NewTicker(statsInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case beat, ok := <-ch:
			if !ok {
				// The source drops a subscriber that falls behind, and closes at shutdown.
				// Browsers miss the dropped beats until they reload; the stats catch up.
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
				}
				ch, unsubscribe = beats.Subscribe()
				continue
			}
			s.broadcast(encodeMessage("heartbeat", beatJSON(beat)))
			s.mu.Lock()
			s.beatsSeen = true
			s.mu.Unlock()
		case <-ticker.C:
			s.mu.Lock()
			seen := s.beatsSeen
			s.beatsSeen = false
			s.mu.Unlock()
			if seen {
				s.broadcastStats(ctx)
			}
		}
	}
}

const statsInterval = 30 * time.Second

func (s *Server) broadcastStats(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()
	stats, err := s.stats(ctx)
	if err != nil {
		s.log.Warn("ui stats", "error", err)
		return
	}
	s.broadcast(encodeMessage("stats", stats))
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
