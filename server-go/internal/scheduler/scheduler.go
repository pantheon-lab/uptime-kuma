// Package scheduler runs monitor checks on their intervals and turns the results into beats.
package scheduler

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
)

const (
	tracerName = "kuma-analytics/scheduler"
	// pushGrace allows for clock differences and network delay, as in the Node.js backend.
	pushGrace = time.Second
	// A push's token is its only login, and each push is a stored beat, so one sender that
	// loops or a leaked token mustn't be able to fill the database.
	pushRate  = 10
	pushBurst = 20
)

// Checker runs one check.
type Checker interface {
	Run(ctx context.Context, m monitor.Monitor) monitor.Result
}

// Notifier sends alerts. It must not block.
type Notifier interface {
	Notify(m monitor.Monitor, beat store.Beat)
}

// Sink receives finished beats.
type Sink interface {
	Add(beat store.Beat)
	Flush(ctx context.Context) error
}

type runner struct {
	cancel context.CancelFunc
	done   chan struct{}

	// Only for push monitors. Pushes go through the runner, which owns the monitor's state.
	pushToken string
	pushes    chan monitor.Push
	limiter   *rate.Limiter
}

// Scheduler owns one goroutine per active monitor. A semaphore bounds how many
// checks run at once, so a burst can't exhaust sockets or memory.
type Scheduler struct {
	checker Checker
	sink    Sink
	log     *slog.Logger
	slots   chan struct{}
	// notifier is optional, and set before Start.
	notifier Notifier
	// maintenance is optional, and set before Start.
	maintenance Maintenance
	// certificates is optional, and set before Start.
	certificates Certificates
	// domains is optional, and set before Start.
	domains Domains

	mu      sync.Mutex
	stopped bool
	runners map[int32]*runner
	// pushRunners finds the runner of an active push monitor by its token.
	pushRunners map[string]*runner

	// groupNames maps group ids to names for metric labels. It's read on every check, so
	// a renamed or deleted group takes effect without restarting its children.
	groupNames sync.Map

	stateMu sync.Mutex
	// last is each monitor's newest state as of its previous runner's exit. The
	// running runner keeps the live value, and Remove deletes the entry after the
	// runner has exited.
	last map[int32]*monitor.Previous
}

// New creates a Scheduler that runs at most concurrency checks at once.
func New(checker Checker, sink Sink, concurrency int, log *slog.Logger) *Scheduler {
	return &Scheduler{
		checker: checker,
		sink:    sink,
		log:     log,
		slots:   make(chan struct{}, concurrency),
		runners: make(map[int32]*runner),

		pushRunners: make(map[string]*runner),
		last:        make(map[int32]*monitor.Previous),
	}
}

// SetNotifier sets where alerts go. Call it before Start.
func (s *Scheduler) SetNotifier(n Notifier) { s.notifier = n }

// Maintenance says which monitors are under maintenance.
type Maintenance interface {
	UnderMaintenance(monitorID int32) bool
}

// SetMaintenance sets what decides maintenance. Call it before Start.
func (s *Scheduler) SetMaintenance(m Maintenance) { s.maintenance = m }

// Certificates takes the TLS connection of a check. It mustn't block.
type Certificates interface {
	Observe(m monitor.Monitor, state *tls.ConnectionState)
}

// SetCertificates sets what receives TLS connections. Call it before Start.
func (s *Scheduler) SetCertificates(c Certificates) { s.certificates = c }

// Domains takes monitors whose domain may need its expiry checked. It mustn't block.
type Domains interface {
	Observe(m monitor.Monitor)
}

// SetDomains sets what tracks domain expiry. Call it before Start.
func (s *Scheduler) SetDomains(d Domains) { s.domains = d }

// Start begins checking monitors, spreading first checks over each interval so a
// restart doesn't check everything in the same second. previous holds the last
// known state, which decides retries and important beats across restarts.
func (s *Scheduler) Start(monitors []monitor.Monitor, previous map[int32]monitor.Previous) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stateMu.Lock()
	for id, p := range previous {
		s.last[id] = &p
	}
	s.stateMu.Unlock()
	for _, m := range monitors {
		s.rememberGroup(m)
	}
	for _, m := range monitors {
		if m.Active {
			delay := time.Duration(rand.Int64N(int64(m.Interval)*int64(time.Second) + 1))
			s.startLocked(m, delay)
		}
	}
}

// Upsert applies a new or changed monitor. An active monitor is checked immediately.
func (s *Scheduler) Upsert(m monitor.Monitor) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopped {
		return
	}
	s.rememberGroup(m)
	s.stopRunnerLocked(m.ID)
	if m.Active {
		s.startLocked(m, 0)
	} else {
		telemetry.Metrics().ForgetMonitor(m.ID)
	}
}

func (s *Scheduler) rememberGroup(m monitor.Monitor) {
	if m.Type == monitor.TypeGroup {
		s.groupNames.Store(m.ID, m.Name)
	} else {
		s.groupNames.Delete(m.ID)
	}
}

func (s *Scheduler) groupName(m monitor.Monitor) string {
	if m.ParentID != nil {
		if name, ok := s.groupNames.Load(*m.ParentID); ok {
			return name.(string)
		}
	}
	return ""
}

// Remove stops a monitor and writes out its queued beats. Do this before deleting
// its data, or queued beats would be written after the delete.
func (s *Scheduler) Remove(ctx context.Context, id int32) error {
	s.mu.Lock()
	s.stopRunnerLocked(id)
	s.stateMu.Lock()
	delete(s.last, id)
	s.stateMu.Unlock()
	s.mu.Unlock()
	s.groupNames.Delete(id)
	telemetry.Metrics().ForgetMonitor(id)
	if err := s.sink.Flush(ctx); err != nil {
		return fmt.Errorf("flush beats: %w", err)
	}
	return nil
}

// Stop cancels all checks and waits for them to exit. Cancelled checks produce no beat.
func (s *Scheduler) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.stopped = true
	for _, r := range s.runners {
		r.cancel()
	}
	for id := range s.runners {
		s.stopRunnerLocked(id)
	}
}

func (s *Scheduler) stopRunnerLocked(id int32) {
	if r, ok := s.runners[id]; ok {
		r.cancel()
		<-r.done
		delete(s.runners, id)
		if r.pushToken != "" {
			delete(s.pushRunners, r.pushToken)
		}
	}
}

func (s *Scheduler) startLocked(m monitor.Monitor, delay time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{cancel: cancel, done: make(chan struct{})}
	s.runners[m.ID] = r
	if m.Type == monitor.TypePush {
		r.pushToken = m.PushToken
		r.pushes = make(chan monitor.Push)
		r.limiter = rate.NewLimiter(pushRate, pushBurst)
		s.pushRunners[m.PushToken] = r
	}

	s.stateMu.Lock()
	state := s.last[m.ID]
	s.stateMu.Unlock()
	go func() {
		defer close(r.done)
		var final *monitor.Previous
		if m.Type == monitor.TypePush {
			final = s.runPush(ctx, m, r.pushes, state)
		} else {
			final = s.run(ctx, m, delay, state)
		}
		// Taking s.mu here would deadlock with whoever waits on r.done while holding
		// it, so the state goes through its own lock.
		s.stateMu.Lock()
		defer s.stateMu.Unlock()
		if final != nil {
			s.last[m.ID] = final
		}
	}()
}

// run checks one monitor until cancelled and returns its last state.
func (s *Scheduler) run(ctx context.Context, m monitor.Monitor, delay time.Duration, state *monitor.Previous) *monitor.Previous {
	metrics := telemetry.Metrics()
	due := time.Now().Add(delay)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return state
		case <-timer.C:
		}
		scheduled := time.Now()

		// Under maintenance, the target isn't checked: the beat says so, and never alerts.
		if s.maintenance != nil && s.maintenance.UnderMaintenance(m.ID) {
			state = s.maintenanceBeat(m, state)
			wait := max(time.Millisecond, time.Duration(m.Interval)*time.Second-time.Since(scheduled))
			due = time.Now().Add(wait)
			timer.Reset(wait)
			continue
		}

		select {
		case <-ctx.Done():
			return state
		case s.slots <- struct{}{}:
		}
		metrics.RecordLag(time.Since(due).Seconds())
		metrics.CheckStarted()
		group := s.groupName(m)
		startedAt := time.Now().UTC().Truncate(time.Microsecond)
		spanCtx, span := otel.Tracer(tracerName).Start(ctx, "monitor.check", trace.WithNewRoot(), trace.WithAttributes(
			attribute.String("monitor.id", strconv.Itoa(int(m.ID))),
			attribute.String("monitor.name", m.Name),
			attribute.String("monitor.type", m.Type),
			attribute.String("monitor.group", group),
		))
		result := s.runCheck(spanCtx, m)
		elapsed := time.Since(startedAt).Seconds()
		<-s.slots
		metrics.CheckFinished()

		// A cancelled check says nothing about the target, so it makes no beat.
		if ctx.Err() != nil {
			span.SetAttributes(attribute.Bool("monitor.check.cancelled", true))
			span.End()
			return state
		}

		if result.TLS != nil && s.certificates != nil {
			s.certificates.Observe(m, result.TLS)
		}

		out := m.Next(state, result)
		state = &monitor.Previous{Status: out.Status, Retries: out.Retries, DownCount: out.DownCount}
		// As in Node.js, a monitor under maintenance doesn't alert about its domain.
		if s.domains != nil && out.Status != monitor.StatusMaintenance {
			s.domains.Observe(m)
		}
		beat := store.Beat{
			MonitorID: m.ID,
			Time:      startedAt,
			Status:    out.Status,
			Ping:      out.Ping,
			Msg:       out.Msg,
			Important: out.Important,
			Retries:   out.Retries,
			DownCount: out.DownCount,
			Response:  result.Response,
		}

		// The status comes from the beat rather than the check, so that upside-down
		// monitors count correctly. Only failed traces are certain to be exported, so only
		// they carry a trace id and an exemplar: both must lead to a trace that exists.
		exemplarCtx := context.Background()
		span.SetAttributes(attribute.Int("monitor.status", int(out.Status)), attribute.Int("monitor.retries", int(out.Retries)))
		if out.Status == monitor.StatusDown || out.Status == monitor.StatusPending {
			span.SetStatus(codes.Error, out.Msg)
			beat.TraceID = span.SpanContext().TraceID().String()
			exemplarCtx = spanCtx
			if out.Important && out.Status == monitor.StatusDown {
				s.log.Warn("monitor is down", "monitor", m.ID, "name", m.Name, "detail", out.Msg, "trace_id", beat.TraceID)
			}
		}
		metrics.RecordCheck(exemplarCtx, m.Type, group, outcomeName(out.Status), elapsed)
		metrics.SetMonitorUp(m.ID, m.Name, group, out.Status == monitor.StatusUp)
		span.End()
		s.sink.Add(beat)
		if out.Notify && s.notifier != nil {
			s.notifier.Notify(m, beat)
		}

		interval := m.Interval
		if out.Status == monitor.StatusPending && m.RetryInterval > 0 {
			interval = m.RetryInterval
		}
		wait := max(time.Millisecond, time.Duration(interval)*time.Second-time.Since(scheduled))
		due = time.Now().Add(wait)
		timer.Reset(wait)
	}
}

// maintenanceBeat records that a monitor is under maintenance, and returns its new state.
func (s *Scheduler) maintenanceBeat(m monitor.Monitor, state *monitor.Previous) *monitor.Previous {
	out := m.Maintenance(state)
	s.sink.Add(store.Beat{
		MonitorID: m.ID,
		Time:      time.Now().UTC().Truncate(time.Microsecond),
		Status:    out.Status,
		Msg:       out.Msg,
		Important: out.Important,
	})
	telemetry.Metrics().SetMonitorUp(m.ID, m.Name, s.groupName(m), false)
	return &monitor.Previous{Status: out.Status, Retries: out.Retries, DownCount: out.DownCount}
}

// Push hands a received push to its monitor, and returns monitor.ErrPushNotFound if no active
// monitor has the token.
func (s *Scheduler) Push(ctx context.Context, token string, push monitor.Push) error {
	s.mu.Lock()
	r, ok := s.pushRunners[token]
	s.mu.Unlock()
	if !ok {
		return monitor.ErrPushNotFound
	}
	if !r.limiter.Allow() {
		return monitor.ErrPushTooFast
	}
	select {
	case r.pushes <- push:
		return nil
	case <-r.done:
		// Paused, deleted or edited just now. A sender tries again at its next beat.
		return monitor.ErrPushNotFound
	case <-ctx.Done():
		return ctx.Err()
	}
}

// runPush waits for a push monitor's sender until cancelled, and returns its last state. The
// window starts again here, so a restart or an edit can't make a healthy sender look late. It
// also starts again after maintenance, because the Node.js backend's DOWN right after a window
// would alert about nothing.
func (s *Scheduler) runPush(ctx context.Context, m monitor.Monitor, pushes <-chan monitor.Push, state *monitor.Previous) *monitor.Previous {
	timer := time.NewTimer(time.Duration(m.Interval)*time.Second + pushGrace)
	defer timer.Stop()
	for {
		var out monitor.Outcome
		select {
		case <-ctx.Done():
			return state
		case push := <-pushes:
			if s.maintenance != nil && s.maintenance.UnderMaintenance(m.ID) {
				state = s.maintenanceBeat(m, state)
				timer.Reset(time.Duration(m.Interval)*time.Second + pushGrace)
				continue
			}
			out = m.NextPush(state, push)
		case <-timer.C:
			if s.maintenance != nil && s.maintenance.UnderMaintenance(m.ID) {
				state = s.maintenanceBeat(m, state)
				timer.Reset(time.Duration(m.Interval)*time.Second + pushGrace)
				continue
			}
			// Upside down, this is what makes it UP.
			out = m.Next(state, monitor.Result{Msg: monitor.NoHeartbeatMsg})
		}
		state = &monitor.Previous{Status: out.Status, Retries: out.Retries, DownCount: out.DownCount}
		s.recordPushBeat(m, out)

		interval := m.Interval
		if out.Status == monitor.StatusPending && m.RetryInterval > 0 {
			interval = m.RetryInterval
		}
		timer.Reset(time.Duration(interval)*time.Second + pushGrace)
	}
}

// recordPushBeat is the end of a check, for a beat that no check made, so it has no trace.
func (s *Scheduler) recordPushBeat(m monitor.Monitor, out monitor.Outcome) {
	beat := store.Beat{
		MonitorID: m.ID,
		Time:      time.Now().UTC().Truncate(time.Microsecond),
		Status:    out.Status,
		Ping:      out.Ping,
		Msg:       out.Msg,
		Important: out.Important,
		Retries:   out.Retries,
		DownCount: out.DownCount,
	}
	group := s.groupName(m)
	if out.Important && out.Status == monitor.StatusDown {
		s.log.Warn("monitor is down", "monitor", m.ID, "name", m.Name, "detail", out.Msg)
	}
	metrics := telemetry.Metrics()
	metrics.RecordOutcome(context.Background(), m.Type, group, outcomeName(out.Status))
	metrics.SetMonitorUp(m.ID, m.Name, group, out.Status == monitor.StatusUp)
	s.sink.Add(beat)
	if out.Notify && s.notifier != nil {
		s.notifier.Notify(m, beat)
	}
}

func outcomeName(status int16) string {
	switch status {
	case monitor.StatusUp:
		return "up"
	case monitor.StatusDown:
		return "down"
	case monitor.StatusPending:
		return "pending"
	default:
		return "maintenance"
	}
}

// runCheck keeps a panic in one check from taking down every other monitor.
func (s *Scheduler) runCheck(ctx context.Context, m monitor.Monitor) (result monitor.Result) {
	defer func() {
		if p := recover(); p != nil {
			s.log.Error("check panicked", "monitor", m.ID, "panic", p)
			result = monitor.Result{Msg: "Internal error while checking"}
		}
	}()
	return s.checker.Run(ctx, m)
}
