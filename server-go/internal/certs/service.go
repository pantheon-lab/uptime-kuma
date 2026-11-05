package certs

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const (
	// NotifyDaysSetting is the setting the UI saves, a list of days before expiry to alert at.
	NotifyDaysSetting = "tlsExpiryNotifyDays"

	queueSize = 1024
	// recheckInterval is how often an unchanged certificate's deadlines are checked again, so
	// that a new notification or changed setting takes effect without waiting for a new day.
	recheckInterval = time.Hour
	workTimeout     = 2 * time.Minute
)

// defaultNotifyDays is what the UI shows when nothing has been saved.
var defaultNotifyDays = []int{7, 14, 21}

// Store is the database access the service needs.
type Store interface {
	TLSFingerprint(ctx context.Context, monitorID int32) (string, error)
	SaveTLSInfo(ctx context.Context, monitorID int32, info []byte, fingerprint string) error
	ClearCertAlerts(ctx context.Context, monitorID int32) error
	CertAlertSent(ctx context.Context, monitorID int32, days int) (bool, error)
	RecordCertAlert(ctx context.Context, monitorID int32, days int) error
	Settings(ctx context.Context) (map[string]json.RawMessage, error)
}

// Sender sends a message to a monitor's notifications, and reports whether any accepted it.
type Sender interface {
	SendText(ctx context.Context, m monitor.Monitor, text string) bool
}

type observation struct {
	monitor monitor.Monitor
	state   *tls.ConnectionState
}

// seen is what's known about a monitor's certificate. Only the worker uses it.
type seen struct {
	fingerprint string
	days        int
	valid       bool
	match       bool
	checked     time.Time
	alerting    bool
}

// Service stores certificates and sends expiry alerts.
type Service struct {
	store    Store
	sender   Sender
	log      *slog.Logger
	now      func() time.Time
	queue    chan observation
	onChange atomic.Pointer[func(monitorID int32, info string)]
	last     map[int32]*seen
}

// NewService creates the service. Call Run to start it.
func NewService(s Store, sender Sender, log *slog.Logger) *Service {
	return &Service{
		store:  s,
		sender: sender,
		log:    log,
		now:    time.Now,
		queue:  make(chan observation, queueSize),
		last:   map[int32]*seen{},
	}
}

// OnChange sets a function called with a monitor's new certificate information, as JSON.
func (s *Service) OnChange(fn func(monitorID int32, info string)) { s.onChange.Store(&fn) }

// Observe takes the certificates of a check's connection. It never blocks: if the worker is
// behind, the next check of the monitor will bring the same information.
func (s *Service) Observe(m monitor.Monitor, state *tls.ConnectionState) {
	select {
	case s.queue <- observation{monitor: m, state: state}:
	default:
	}
}

// Run handles observations until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case o := <-s.queue:
			workCtx, cancel := context.WithTimeout(ctx, workTimeout)
			if err := s.handle(workCtx, o); err != nil && ctx.Err() == nil {
				s.log.Error("handle certificate", "monitor", o.monitor.ID, "error", err)
			}
			cancel()
		}
	}
}

func (s *Service) handle(ctx context.Context, o observation) error {
	info := Read(o.state, o.monitor.URL, s.now())
	if info == nil {
		return nil
	}
	cert := info.CertInfo
	last := s.last[o.monitor.ID]
	changed := last == nil || last.fingerprint != cert.Fingerprint256 || last.days != cert.DaysRemaining ||
		last.valid != info.Valid || last.match != info.HostnameMatchMonitorURL
	if changed {
		if err := s.save(ctx, o.monitor.ID, info, last); err != nil {
			return err
		}
	}
	if last == nil {
		last = &seen{}
		s.last[o.monitor.ID] = last
	}
	replaced := last.fingerprint != "" && last.fingerprint != cert.Fingerprint256
	last.fingerprint, last.days = cert.Fingerprint256, cert.DaysRemaining
	last.valid, last.match = info.Valid, info.HostnameMatchMonitorURL

	alerting := o.monitor.ExpiryNotification && !o.monitor.IgnoreTLS
	due := alerting && (changed || replaced || !last.alerting || s.now().Sub(last.checked) >= recheckInterval)
	last.alerting = alerting
	if !due {
		return nil
	}
	last.checked = s.now()
	return s.checkExpiry(ctx, o.monitor, cert)
}

func (s *Service) save(ctx context.Context, monitorID int32, info *Info, last *seen) error {
	data, err := json.Marshal(info)
	if err != nil {
		return fmt.Errorf("encode certificate: %w", err)
	}
	// After a restart the previous certificate is only in the database.
	previous := ""
	if last != nil {
		previous = last.fingerprint
	} else if previous, err = s.store.TLSFingerprint(ctx, monitorID); err != nil {
		return err
	}
	// A new certificate starts its deadlines again.
	if previous != "" && previous != info.CertInfo.Fingerprint256 {
		if err := s.store.ClearCertAlerts(ctx, monitorID); err != nil {
			return err
		}
	}
	if err := s.store.SaveTLSInfo(ctx, monitorID, data, info.CertInfo.Fingerprint256); err != nil {
		return err
	}
	if fn := s.onChange.Load(); fn != nil {
		(*fn)(monitorID, string(data))
	}
	return nil
}

func (s *Service) notifyDays(ctx context.Context) ([]int, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	raw, found := settings[NotifyDaysSetting]
	if !found {
		return defaultNotifyDays, nil
	}
	var days []int
	if err := json.Unmarshal(raw, &days); err != nil {
		s.log.Warn("invalid setting, using the default", "setting", NotifyDaysSetting, "error", err)
		return defaultNotifyDays, nil
	}
	return days, nil
}

// checkExpiry alerts for each deadline a certificate in the chain has passed. Deadlines go in
// ascending order and an alert counts for every later deadline, so one certificate at 5 days sends
// one alert rather than one per deadline, whatever order the setting is in.
func (s *Service) checkExpiry(ctx context.Context, m monitor.Monitor, chain *Cert) error {
	days, err := s.notifyDays(ctx)
	if err != nil {
		return err
	}
	days = slices.Sorted(slices.Values(days))
	for _, target := range days {
		for cert := chain; cert != nil && !cert.trustedRoot; cert = cert.IssuerCertificate {
			if cert.DaysRemaining > target {
				continue
			}
			sent, err := s.store.CertAlertSent(ctx, m.ID, target)
			if err != nil {
				return err
			}
			if sent {
				continue
			}
			text := fmt.Sprintf("[%s][%s] %s certificate %s will expire in %d days",
				m.Name, m.Redacted().URL, cert.CertType, cert.Subject["CN"], cert.DaysRemaining)
			// Nothing is recorded if no provider took it, so the next check tries again.
			if s.sender.SendText(ctx, m, text) {
				if err := s.store.RecordCertAlert(ctx, m.ID, target); err != nil {
					return err
				}
			}
		}
	}
	return nil
}
