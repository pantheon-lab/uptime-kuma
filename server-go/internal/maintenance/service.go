package maintenance

import (
	"context"
	"log/slog"
	"maps"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// Store is the database access the service needs.
type Store interface {
	ListMaintenances(ctx context.Context) ([]Window, error)
	MaintenanceMonitorLinks(ctx context.Context) (map[int32][]int32, error)
	ListMonitors(ctx context.Context) ([]monitor.Monitor, error)
	HolidayOverrides(ctx context.Context) ([]holidays.Override, error)
}

// refreshInterval is how often window states are recomputed, which bounds how late a window
// starts or ends. Changes made through the UI apply at once.
const refreshInterval = 10 * time.Second

// Service tracks which monitors are under maintenance. It's safe for concurrent use.
type Service struct {
	store    Store
	calendar *holidays.Calendar
	eval     Evaluator
	log      *slog.Logger
	now      func() time.Time

	under    atomic.Pointer[map[int32]bool]
	trigger  chan struct{}
	onChange atomic.Pointer[func()]

	mu       sync.Mutex
	windows  []Window
	statuses map[int32]string
}

// NewService creates the service. Call Refresh before relying on it, then Run.
func NewService(s Store, calendar *holidays.Calendar, server *time.Location, log *slog.Logger) *Service {
	svc := &Service{
		store:    s,
		calendar: calendar,
		eval:     Evaluator{Server: server, Holidays: calendar},
		log:      log,
		now:      time.Now,
		trigger:  make(chan struct{}, 1),
		statuses: map[int32]string{},
	}
	empty := map[int32]bool{}
	svc.under.Store(&empty)
	return svc
}

// Evaluator returns the evaluator the service uses.
func (s *Service) Evaluator() Evaluator { return s.eval }

// Calendar returns the holiday calendar.
func (s *Service) Calendar() *holidays.Calendar { return s.calendar }

// OnChange sets a function called after a refresh in which any window's status changed.
func (s *Service) OnChange(fn func()) { s.onChange.Store(&fn) }

// UnderMaintenance reports whether a monitor is covered by a running window, directly or
// through a group above it.
func (s *Service) UnderMaintenance(monitorID int32) bool {
	return (*s.under.Load())[monitorID]
}

// UnderMaintenanceSet returns every monitor under maintenance.
func (s *Service) UnderMaintenanceSet() map[int32]bool {
	return maps.Clone(*s.under.Load())
}

// Windows returns the windows as of the last refresh.
func (s *Service) Windows() []Window {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Window(nil), s.windows...)
}

// Changed asks for a refresh soon, after windows, links, monitors or holidays changed.
func (s *Service) Changed() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Run refreshes until ctx ends.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(refreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-s.trigger:
		}
		if err := s.Refresh(ctx); err != nil && ctx.Err() == nil {
			s.log.Error("refresh maintenance", "error", err)
		}
	}
}

// Refresh reloads windows and recomputes which monitors are under maintenance.
func (s *Service) Refresh(ctx context.Context) error {
	windows, err := s.store.ListMaintenances(ctx)
	if err != nil {
		return err
	}
	links, err := s.store.MaintenanceMonitorLinks(ctx)
	if err != nil {
		return err
	}
	monitors, err := s.store.ListMonitors(ctx)
	if err != nil {
		return err
	}
	overrides, err := s.store.HolidayOverrides(ctx)
	if err != nil {
		return err
	}
	s.calendar.SetOverrides(overrides)

	children := map[int32][]int32{}
	for _, m := range monitors {
		if m.ParentID != nil {
			children[*m.ParentID] = append(children[*m.ParentID], m.ID)
		}
	}
	now := s.now()
	under := map[int32]bool{}
	statuses := make(map[int32]string, len(windows))
	for _, w := range windows {
		status := s.eval.Status(w, now)
		statuses[w.ID] = status
		if status != StatusUnderMaintenance {
			continue
		}
		// A window on a group covers everything below it.
		pending := append([]int32(nil), links[w.ID]...)
		for len(pending) > 0 {
			id := pending[len(pending)-1]
			pending = pending[:len(pending)-1]
			if under[id] {
				continue
			}
			under[id] = true
			pending = append(pending, children[id]...)
		}
	}

	s.mu.Lock()
	changed := !maps.Equal(statuses, s.statuses)
	s.windows, s.statuses = windows, statuses
	s.mu.Unlock()
	previous := s.under.Swap(&under)
	if changed || !maps.Equal(*previous, under) {
		if fn := s.onChange.Load(); fn != nil {
			(*fn)()
		}
	}
	return nil
}
