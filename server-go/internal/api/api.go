// Package api exposes the analytics service over REST and Server-Sent Events.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	maxBeatsPerRequest = 5000
	maxBodyBytes       = 32 << 20
	maxMsgBytes        = 64 << 10
	maxFutureSkew      = time.Hour
	maxHours           = 24 * 365
	maxBeatLimit       = 1000
	maxFractions       = 10
	queryTimeout       = 30 * time.Second
	streamKeepAlive    = 25 * time.Second
)

var traceIDFormat = regexp.MustCompile(`^[0-9a-f]{32}$`)

// Store is the persistence the API depends on.
type Store interface {
	Ping(ctx context.Context) error
	InsertBeats(ctx context.Context, beats []store.Beat) ([]store.Beat, error)
	LatestBeats(ctx context.Context, monitorID int32, limit int) ([]store.Beat, error)
	Chart(ctx context.Context, monitorID int32, hours int) ([]store.ChartPoint, error)
	Percentiles(ctx context.Context, monitorID int32, hours int, fractions []float64) ([]*float64, error)
	Stats(ctx context.Context) ([]store.MonitorStats, error)
	FindMonitors(ctx context.Context, f store.MonitorFilter) ([]monitor.Monitor, int, error)
	SetMonitorActive(ctx context.Context, id int32, active bool) (monitor.Monitor, error)
	CreateAPIKey(ctx context.Context, key store.APIKey, hash []byte) (store.APIKey, error)
	ListAPIKeys(ctx context.Context) ([]store.APIKey, error)
	RevokeAPIKey(ctx context.Context, id int64) error
	FindAPIKey(ctx context.Context, hash []byte) (store.APIKey, error)
	TouchAPIKey(ctx context.Context, id int64) error
	ListNotifications(ctx context.Context) ([]store.Notification, error)
	GetNotification(ctx context.Context, id int32) (store.Notification, error)
	CreateNotification(ctx context.Context, n store.Notification, applyToAll bool) (store.Notification, error)
	UpdateNotification(ctx context.Context, n store.Notification, applyToAll bool) (store.Notification, error)
	DeleteNotification(ctx context.Context, id int32) error
	MonitorNotificationIDs(ctx context.Context, monitorID int32) ([]int32, error)
	SetMonitorNotifications(ctx context.Context, monitorID int32, ids []int32) error
	LinkDefaultNotifications(ctx context.Context, monitorID int32) error
	GetMonitor(ctx context.Context, id int32) (monitor.Monitor, error)
	CreateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error)
	UpdateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error)
	DeleteMonitor(ctx context.Context, id int32) error
}

// Scheduler is told about monitor changes.
type Scheduler interface {
	Upsert(m monitor.Monitor)
	Remove(ctx context.Context, id int32) error
}

// Server holds the HTTP handlers.
type Server struct {
	store     Store
	scheduler Scheduler
	hub       *Hub
	token     []byte
	log       *slog.Logger
	now       func() time.Time
	limiter   *keyLimiter
	notifyEnv *notify.Env
	lastTouch sync.Map // Key id to the time last_used_at was last written.
	routes    []string
}

// New creates the API. Every route except /healthz requires token as a bearer token.
func New(s Store, scheduler Scheduler, hub *Hub, token string, log *slog.Logger) *Server {
	return &Server{
		store:     s,
		scheduler: scheduler,
		hub:       hub,
		token:     []byte(token),
		log:       log,
		now:       time.Now,
		limiter:   newKeyLimiter(defaultKeyRate, defaultKeyBurst),
		notifyEnv: notify.DefaultEnv(""),
	}
}

// SetNotifyEnv sets how test notifications are sent.
func (s *Server) SetNotifyEnv(env *notify.Env) { s.notifyEnv = env }

// SetKeyRateLimit sets the requests per second and burst allowed per API key.
func (s *Server) SetKeyRateLimit(perSecond float64, burst int) {
	s.limiter = newKeyLimiter(perSecond, burst)
}

// Handler returns the routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	s.routes = nil
	handle := func(pattern, scope string, handler http.HandlerFunc) {
		s.routes = append(s.routes, pattern)
		mux.Handle(pattern, otelhttp.NewHandler(s.require(scope, handler), pattern))
	}
	// Not traced: a health check is noise, and a stream's span would last for hours.
	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /api/v1/openapi.json", s.openAPI)
	s.routes = append(s.routes, "GET /api/v1/openapi.json")
	s.routes = append(s.routes, "GET /api/v1/events")
	mux.Handle("GET /api/v1/events", s.require(ScopeBeatsRead, s.stream))

	handle("GET /api/v1/monitors", ScopeMonitorsRead, s.listMonitors)
	handle("POST /api/v1/monitors", ScopeMonitorsWrite, s.createMonitor)
	handle("GET /api/v1/monitors/{id}", ScopeMonitorsRead, s.getMonitor)
	handle("PUT /api/v1/monitors/{id}", ScopeMonitorsWrite, s.updateMonitor)
	handle("PATCH /api/v1/monitors/{id}", ScopeMonitorsWrite, s.patchMonitor)
	handle("DELETE /api/v1/monitors/{id}", ScopeMonitorsWrite, s.deleteMonitor)
	handle("POST /api/v1/monitors/{id}/pause", ScopeMonitorsWrite, s.pauseMonitor)
	handle("POST /api/v1/monitors/{id}/resume", ScopeMonitorsWrite, s.resumeMonitor)
	handle("GET /api/v1/monitors/{id}/beats", ScopeBeatsRead, s.getBeats)
	handle("GET /api/v1/monitors/{id}/chart", ScopeBeatsRead, s.getChart)
	handle("GET /api/v1/monitors/{id}/percentiles", ScopeBeatsRead, s.getPercentiles)
	handle("GET /api/v1/stats", ScopeBeatsRead, s.getStats)
	handle("POST /api/v1/beats", ScopeBeatsWrite, s.postBeats)

	handle("GET /api/v1/monitors/{id}/notifications", ScopeNotificationsRead, s.getMonitorNotifications)
	handle("PUT /api/v1/monitors/{id}/notifications", ScopeNotificationsWrite, s.setMonitorNotifications)
	handle("GET /api/v1/notifications/providers", ScopeNotificationsRead, s.listNotificationProviders)
	handle("GET /api/v1/notifications", ScopeNotificationsRead, s.listNotifications)
	handle("POST /api/v1/notifications", ScopeNotificationsWrite, s.createNotification)
	handle("GET /api/v1/notifications/{id}", ScopeNotificationsRead, s.getNotification)
	handle("PUT /api/v1/notifications/{id}", ScopeNotificationsWrite, s.updateNotification)
	handle("DELETE /api/v1/notifications/{id}", ScopeNotificationsWrite, s.deleteNotification)
	handle("POST /api/v1/notifications/{id}/test", ScopeNotificationsWrite, s.testNotification)

	handle("GET /api/v1/api-keys", scopeAdmin, s.listAPIKeys)
	handle("POST /api/v1/api-keys", scopeAdmin, s.createAPIKey)
	handle("DELETE /api/v1/api-keys/{id}", scopeAdmin, s.revokeAPIKey)
	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.log.Warn("health check failed", "error", err)
		writeError(w, http.StatusServiceUnavailable, "database unavailable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) postBeats(w http.ResponseWriter, r *http.Request) {
	var beats []store.Beat
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(&beats); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "body must be a JSON array of beats")
		return
	}
	if err := validateBeats(beats, s.now()); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	inserted, err := s.store.InsertBeats(ctx, beats)
	if err != nil {
		s.serverError(w, "insert beats", err)
		return
	}
	s.hub.Publish(inserted)
	writeJSON(w, http.StatusOK, map[string]int{"received": len(beats), "inserted": len(inserted)})
}

// validateBeats rejects the whole batch on the first bad beat and normalizes
// times to the microsecond precision that PostgreSQL stores.
func validateBeats(beats []store.Beat, now time.Time) error {
	if len(beats) > maxBeatsPerRequest {
		return fmt.Errorf("too many beats: %d, maximum is %d", len(beats), maxBeatsPerRequest)
	}
	for i := range beats {
		b := &beats[i]
		switch {
		case b.MonitorID <= 0:
			return fmt.Errorf("beat %d: monitorId must be positive", i)
		case b.Time.IsZero():
			return fmt.Errorf("beat %d: time is required", i)
		case b.Time.After(now.Add(maxFutureSkew)):
			return fmt.Errorf("beat %d: time is too far in the future", i)
		case b.Status < 0 || b.Status > 3:
			return fmt.Errorf("beat %d: status must be between 0 and 3", i)
		case b.Ping != nil && *b.Ping < 0:
			return fmt.Errorf("beat %d: ping must not be negative", i)
		case b.Duration < 0 || b.Retries < 0:
			return fmt.Errorf("beat %d: duration and retries must not be negative", i)
		case len(b.Msg) > maxMsgBytes:
			return fmt.Errorf("beat %d: msg is longer than %d bytes", i, maxMsgBytes)
		case strings.ContainsRune(b.Msg, 0):
			return fmt.Errorf("beat %d: msg must not contain NUL", i)
		case b.TraceID != "" && !traceIDFormat.MatchString(b.TraceID):
			return fmt.Errorf("beat %d: traceId must be 32 lowercase hexadecimal characters", i)
		}
		b.Time = b.Time.UTC().Truncate(time.Microsecond)
	}
	return nil
}

func (s *Server) getBeats(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	limit, ok := intParam(w, r, "limit", 100, maxBeatLimit)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	beats, err := s.store.LatestBeats(ctx, id, limit)
	if err != nil {
		s.serverError(w, "latest beats", err)
		return
	}
	writeJSON(w, http.StatusOK, beats)
}

func (s *Server) getChart(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	hours, ok := intParam(w, r, "hours", 24, maxHours)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	points, err := s.store.Chart(ctx, id, hours)
	if err != nil {
		s.serverError(w, "chart", err)
		return
	}
	writeJSON(w, http.StatusOK, points)
}

func (s *Server) getPercentiles(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	hours, ok := intParam(w, r, "hours", 24*7, maxHours)
	if !ok {
		return
	}
	raw := r.URL.Query().Get("p")
	if raw == "" {
		raw = "50,95,99"
	}
	parts := strings.Split(raw, ",")
	if len(parts) > maxFractions {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("at most %d percentiles", maxFractions))
		return
	}
	fractions := make([]float64, len(parts))
	for i, part := range parts {
		p, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || !(p >= 0 && p <= 100) {
			writeError(w, http.StatusBadRequest, "p must be a comma-separated list of numbers from 0 to 100")
			return
		}
		fractions[i] = p / 100
	}

	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	values, err := s.store.Percentiles(ctx, id, hours, fractions)
	if err != nil {
		s.serverError(w, "percentiles", err)
		return
	}
	result := make(map[string]*float64, len(parts))
	for i, part := range parts {
		result["p"+strings.TrimSpace(part)] = values[i]
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) getStats(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), queryTimeout)
	defer cancel()
	stats, err := s.store.Stats(ctx)
	if err != nil {
		s.serverError(w, "stats", err)
		return
	}
	writeJSON(w, http.StatusOK, stats)
}

// stream sends new beats as Server-Sent Events. There is no replay: a client
// that reconnects should refetch current state over REST.
func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	var filter int64
	if raw := r.URL.Query().Get("monitorId"); raw != "" {
		v, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || v <= 0 {
			writeError(w, http.StatusBadRequest, "monitorId must be a positive integer")
			return
		}
		filter = v
	}

	// Streams outlive the server's read and write timeouts.
	rc := http.NewResponseController(w)
	if err := rc.SetReadDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.log.Warn("clear stream read deadline", "error", err)
	}
	if err := rc.SetWriteDeadline(time.Time{}); err != nil && !errors.Is(err, http.ErrNotSupported) {
		s.log.Warn("clear stream write deadline", "error", err)
	}

	beats, unsubscribe := s.hub.Subscribe()
	defer unsubscribe()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Without this first flush a quiet stream would not reach the client at all.
	if _, err := fmt.Fprint(w, ": connected\n\n"); err != nil {
		return
	}
	if err := rc.Flush(); err != nil {
		return
	}

	keepAlive := time.NewTicker(streamKeepAlive)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-keepAlive.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return
			}
		case beat, open := <-beats:
			if !open {
				return
			}
			if filter != 0 && int64(beat.MonitorID) != filter {
				continue
			}
			data, err := json.Marshal(beat)
			if err != nil {
				s.log.Error("encode beat", "error", err)
				continue
			}
			if _, err := fmt.Fprintf(w, "event: beat\ndata: %s\n\n", data); err != nil {
				return
			}
		}
		if err := rc.Flush(); err != nil {
			return
		}
	}
}

func monitorID(w http.ResponseWriter, r *http.Request) (int32, bool) {
	v, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil || v <= 0 {
		writeError(w, http.StatusBadRequest, "monitor id must be a positive integer")
		return 0, false
	}
	return int32(v), true
}

func intParam(w http.ResponseWriter, r *http.Request, name string, fallback, limit int) (int, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return fallback, true
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v < 1 || v > limit {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("%s must be an integer from 1 to %d", name, limit))
		return 0, false
	}
	return v, true
}

func (s *Server) serverError(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, context.Canceled) {
		return // The client went away.
	}
	s.log.Error("request failed", "operation", operation, "error", err)
	writeError(w, http.StatusInternalServerError, "internal error")
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
