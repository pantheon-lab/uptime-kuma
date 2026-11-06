// Package domains tracks when the domains that monitors check expire, as the Node.js backend's
// domain_expiry model did: it asks the registry's RDAP server once a day, and sends alerts on the
// days the UI's settings list.
package domains

import (
	"cmp"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/net/publicsuffix"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const (
	// NotifyDaysSetting is the setting the UI saves: the days before expiry to alert at.
	NotifyDaysSetting = "domainExpiryNotifyDays"
	// bootstrapSetting keeps the last copy of IANA's RDAP list, as the Node.js backend did.
	bootstrapSetting = "rdapDnsData"
	bootstrapURL     = "https://data.iana.org/rdap/dns.json"

	queueSize       = 1024
	checkInterval   = 24 * time.Hour
	monitorInterval = time.Hour
	workTimeout     = 2 * time.Minute
	requestTimeout  = 20 * time.Second
	maxRDAPBytes    = 1 << 20
)

var defaultNotifyDays = []int{7, 14, 21}

//go:embed rdap-dns.json
var embeddedBootstrap []byte

// targetField is the field whose host each type's domain comes from, as the UI's
// TYPES_WITH_DOMAIN_EXPIRY_SUPPORT_VIA_FIELD lists.
var targetField = map[string]string{
	monitor.TypeHTTP: "url", monitor.TypeKeyword: "url", monitor.TypeJSONQuery: "url", monitor.TypeRealBrowser: "url",
	monitor.TypeWebSocket: "url", monitor.TypePort: "hostname", monitor.TypePing: "hostname", monitor.TypeGRPC: "grpcUrl",
	monitor.TypeDNS: "hostname", monitor.TypeSMTP: "hostname", monitor.TypeSNMP: "hostname", monitor.TypeSteam: "hostname",
	monitor.TypeMQTT: "hostname", monitor.TypeRADIUS: "hostname", monitor.TypeSIP: "hostname",
}

// Supports reports whether a type's monitors can have their domain tracked.
func Supports(kind string) bool {
	_, found := targetField[kind]
	return found
}

// Target is the text a monitor's domain comes from.
func Target(kind, rawURL, hostname, grpcURL string) string {
	switch targetField[kind] {
	case "url":
		return rawURL
	case "grpcUrl":
		return grpcURL
	case "hostname":
		return hostname
	}
	return ""
}

// UnsupportedError says why a monitor's domain can't be tracked. Key is a message of the UI's,
// which Meta fills in.
type UnsupportedError struct {
	Key  string
	Meta map[string]string
}

func (e *UnsupportedError) Error() string { return e.Key }

// Domain is the registrable domain of a target, such as example.co.uk for
// https://www.example.co.uk/x, and its public suffix.
func Domain(kind, target string) (domain, suffix string, err error) {
	if !Supports(kind) {
		return "", "", &UnsupportedError{Key: "domain_expiry_unsupported_monitor_type"}
	}
	host := hostOf(target)
	if host == "" {
		return "", "", &UnsupportedError{Key: "domain_expiry_unsupported_missing_target"}
	}
	suffix, icann := publicsuffix.PublicSuffix(host)
	registrable, err := publicsuffix.EffectiveTLDPlusOne(host)
	if net.ParseIP(host) != nil || !icann || err != nil {
		return "", "", &UnsupportedError{Key: "domain_expiry_unsupported_is_icann",
			Meta: map[string]string{"domain": cmp.Or(registrable, host), "publicSuffix": suffix}}
	}
	return registrable, suffix, nil
}

// hostOf finds the host name in a URL, host:port, or bare host.
func hostOf(target string) string {
	target = strings.TrimSpace(target)
	if strings.Contains(target, "://") {
		u, err := url.Parse(target)
		if err != nil {
			return ""
		}
		target = u.Hostname()
	} else if host, _, err := net.SplitHostPort(target); err == nil {
		target = host
	}
	target = strings.Trim(strings.ToLower(target), "[].")
	if target == "" || strings.ContainsAny(target, " /@") || !strings.Contains(target, ".") && net.ParseIP(target) == nil {
		return ""
	}
	return target
}

// Record is what's known about one domain.
type Record struct {
	Domain    string
	Expiry    *time.Time
	LastCheck *time.Time
	// LastNotifiedDays is the deadline, in days, of the last alert sent, if one was.
	LastNotifiedDays *int
}

// DaysRemaining counts whole days left, as dayjs's diff did.
func DaysRemaining(expiry, now time.Time) int {
	return int(expiry.Sub(now).Hours() / 24)
}

// Store is the database access the service needs.
type Store interface {
	DomainExpiry(ctx context.Context, domain string) (Record, error)
	SaveDomainExpiry(ctx context.Context, r Record) error
	Settings(ctx context.Context) (map[string]json.RawMessage, error)
	SaveSettings(ctx context.Context, settings map[string]json.RawMessage) error
}

// Sender sends a message to a monitor's notifications, and reports whether any accepted it.
type Sender interface {
	SendText(ctx context.Context, m monitor.Monitor, text string) bool
}

// Service looks up expiry dates and sends alerts.
type Service struct {
	store    Store
	sender   Sender
	log      *slog.Logger
	client   *http.Client
	now      func() time.Time
	queue    chan monitor.Monitor
	onChange atomic.Pointer[func(monitorID int32, daysRemaining int, expiry time.Time)]
	// lastSeen is when each monitor was last handled. Only the worker uses it.
	lastSeen map[int32]time.Time

	bootstrapMu   sync.Mutex
	bootstrap     *bootstrap
	nextBootstrap time.Time
	// BootstrapURL is IANA's list of RDAP servers. Tests replace it.
	BootstrapURL string
}

type bootstrap struct {
	Services [][][]string `json:"services"`
}

// NewService creates the service. Call Run to start it.
func NewService(s Store, sender Sender, log *slog.Logger) *Service {
	return &Service{
		store:        s,
		sender:       sender,
		log:          log,
		client:       &http.Client{Timeout: requestTimeout},
		now:          time.Now,
		queue:        make(chan monitor.Monitor, queueSize),
		lastSeen:     map[int32]time.Time{},
		BootstrapURL: bootstrapURL,
	}
}

// OnChange sets a function called when a monitor's domain has a new expiry date.
func (s *Service) OnChange(fn func(monitorID int32, daysRemaining int, expiry time.Time)) {
	s.onChange.Store(&fn)
}

// Observe takes a monitor that was just checked. It never blocks.
func (s *Service) Observe(m monitor.Monitor) {
	if !m.DomainExpiryNotification || !Supports(m.Type) {
		return
	}
	select {
	case s.queue <- m:
	default:
	}
}

// Run handles monitors until ctx ends.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case m := <-s.queue:
			if last, found := s.lastSeen[m.ID]; found && s.now().Sub(last) < monitorInterval {
				continue
			}
			s.lastSeen[m.ID] = s.now()
			workCtx, cancel := context.WithTimeout(ctx, workTimeout)
			if err := s.handle(workCtx, m); err != nil && ctx.Err() == nil {
				var unsupported *UnsupportedError
				if errors.As(err, &unsupported) {
					s.log.Debug("domain expiry unsupported", "monitor", m.ID, "reason", unsupported.Key)
				} else {
					s.log.Warn("domain expiry", "monitor", m.ID, "error", err)
				}
			}
			cancel()
		}
	}
}

func (s *Service) handle(ctx context.Context, m monitor.Monitor) error {
	domain, _, err := s.Check(ctx, m.Type, Target(m.Type, m.URL, m.Hostname, m.GRPCURL))
	if err != nil {
		return err
	}
	record, err := s.refresh(ctx, domain)
	if err != nil || record.Expiry == nil {
		return err
	}
	if fn := s.onChange.Load(); fn != nil {
		(*fn)(m.ID, DaysRemaining(*record.Expiry, s.now()), *record.Expiry)
	}
	return s.notify(ctx, m, record)
}

// Check finds a target's domain and makes sure its registry has an RDAP server, for the form's
// check of whether a monitor's domain can be tracked.
func (s *Service) Check(ctx context.Context, kind, target string) (domain, tld string, err error) {
	domain, suffix, err := Domain(kind, target)
	if err != nil {
		return "", "", err
	}
	if server := s.rdapServer(ctx, suffix); server == "" {
		return "", "", &UnsupportedError{Key: "domain_expiry_unsupported_unsupported_tld_no_rdap_endpoint",
			Meta: map[string]string{"publicSuffix": suffix}}
	}
	parts := strings.Split(suffix, ".")
	return domain, parts[len(parts)-1], nil
}

// Expiry returns what's known about a target's domain, without asking its registry.
func (s *Service) Expiry(ctx context.Context, kind, target string) (*time.Time, error) {
	domain, _, err := Domain(kind, target)
	if err != nil {
		return nil, err
	}
	record, err := s.store.DomainExpiry(ctx, domain)
	if err != nil {
		return nil, err
	}
	return record.Expiry, nil
}

// refresh asks the registry for the domain's expiry, unless that was done in the last day.
func (s *Service) refresh(ctx context.Context, domain string) (Record, error) {
	record, err := s.store.DomainExpiry(ctx, domain)
	if err != nil {
		return record, err
	}
	record.Domain = domain
	now := s.now()
	if record.LastCheck != nil && now.Sub(*record.LastCheck) < checkInterval {
		return record, nil
	}
	expiry, err := s.lookup(ctx, domain)
	if err != nil {
		s.log.Debug("rdap lookup", "domain", domain, "error", err)
	}
	// A later expiry is a renewal, so its deadlines may alert again.
	if expiry != nil && (record.Expiry == nil || expiry.After(*record.Expiry)) {
		record.LastNotifiedDays = nil
	}
	if expiry != nil || record.Expiry == nil {
		record.Expiry = expiry
	}
	record.LastCheck = &now
	return record, s.store.SaveDomainExpiry(ctx, record)
}

// notify sends an alert for the nearest deadline that has passed and wasn't alerted yet.
func (s *Service) notify(ctx context.Context, m monitor.Monitor, record Record) error {
	days, err := s.notifyDays(ctx)
	if err != nil {
		return err
	}
	remaining := DaysRemaining(*record.Expiry, s.now())
	for _, target := range days {
		if remaining > target || (record.LastNotifiedDays != nil && *record.LastNotifiedDays <= target) {
			continue
		}
		if !s.sender.SendText(ctx, m, fmt.Sprintf("Domain name %s will expire in %d days", record.Domain, remaining)) {
			return nil
		}
		record.LastNotifiedDays = &target
		return s.store.SaveDomainExpiry(ctx, record)
	}
	return nil
}

func (s *Service) notifyDays(ctx context.Context) ([]int, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	var days []int
	if raw, found := settings[NotifyDaysSetting]; !found || json.Unmarshal(raw, &days) != nil {
		days = slices.Clone(defaultNotifyDays)
	}
	slices.Sort(days)
	return days, nil
}

// lookup asks the domain's RDAP server for its expiration event.
func (s *Service) lookup(ctx context.Context, domain string) (*time.Time, error) {
	suffix, _ := publicsuffix.PublicSuffix(domain)
	server := s.rdapServer(ctx, suffix)
	if server == "" {
		return nil, fmt.Errorf("no RDAP server for .%s", suffix)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(server, "/")+"/domain/"+url.PathEscape(domain), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/rdap+json, application/json")
	res, err := s.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("RDAP server answered %d", res.StatusCode)
	}
	var answer struct {
		Events []struct {
			Action string `json:"eventAction"`
			Date   string `json:"eventDate"`
		} `json:"events"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, maxRDAPBytes)).Decode(&answer); err != nil {
		return nil, fmt.Errorf("read RDAP answer: %w", err)
	}
	for _, event := range answer.Events {
		if event.Action != "expiration" {
			continue
		}
		at, err := time.Parse(time.RFC3339, event.Date)
		if err != nil {
			return nil, fmt.Errorf("RDAP expiration date %q: %w", event.Date, err)
		}
		return &at, nil
	}
	return nil, errors.New("the RDAP answer has no expiration date")
}

// rdapServer finds the RDAP server of a suffix's top-level domain, or "" if there's none.
func (s *Service) rdapServer(ctx context.Context, suffix string) string {
	parts := strings.Split(suffix, ".")
	tld := parts[len(parts)-1]
	for _, service := range s.services(ctx).Services {
		if len(service) == 2 && len(service[1]) > 0 && slices.Contains(service[0], tld) {
			return service[1][0]
		}
	}
	return ""
}

// services returns IANA's list, fetched once a week, or once a day after a failure. Without a
// copy from IANA, the last saved one or the one built in is used.
func (s *Service) services(ctx context.Context) *bootstrap {
	s.bootstrapMu.Lock()
	defer s.bootstrapMu.Unlock()
	if s.bootstrap != nil && s.now().Before(s.nextBootstrap) {
		return s.bootstrap
	}
	fresh, raw, err := s.fetchBootstrap(ctx)
	if err == nil {
		s.bootstrap, s.nextBootstrap = fresh, s.now().Add(7*24*time.Hour)
		if err := s.store.SaveSettings(ctx, map[string]json.RawMessage{bootstrapSetting: raw}); err != nil {
			s.log.Warn("save RDAP list", "error", err)
		}
		return s.bootstrap
	}
	s.log.Info("update RDAP list from IANA", "error", err)
	s.nextBootstrap = s.now().Add(24 * time.Hour)
	if s.bootstrap != nil {
		return s.bootstrap
	}
	if settings, err := s.store.Settings(ctx); err == nil {
		var saved bootstrap
		if json.Unmarshal(settings[bootstrapSetting], &saved) == nil && len(saved.Services) > 0 {
			s.bootstrap = &saved
			return s.bootstrap
		}
	}
	var builtIn bootstrap
	_ = json.Unmarshal(embeddedBootstrap, &builtIn)
	s.bootstrap = &builtIn
	return s.bootstrap
}

func (s *Service) fetchBootstrap(ctx context.Context) (*bootstrap, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.BootstrapURL, nil)
	if err != nil {
		return nil, nil, err
	}
	res, err := s.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d", res.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}
	var parsed bootstrap
	if err := json.Unmarshal(raw, &parsed); err != nil || len(parsed.Services) == 0 {
		return nil, nil, errors.New("invalid RDAP list")
	}
	return &parsed, raw, nil
}
