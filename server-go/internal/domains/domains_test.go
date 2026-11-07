package domains

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestDomain(t *testing.T) {
	for _, c := range []struct {
		kind, target, domain, suffix, err string
	}{
		{monitor.TypeHTTP, "https://www.example.co.uk/x?y", "example.co.uk", "co.uk", ""},
		{monitor.TypeGRPC, "api.example.com:443", "example.com", "com", ""},
		{monitor.TypeMQTT, "mqtt://broker.example.org", "example.org", "org", ""},
		{monitor.TypePing, "Example.COM.", "example.com", "com", ""},
		{monitor.TypePing, "10.0.0.1", "", "", "domain_expiry_unsupported_is_icann"},
		{monitor.TypeHTTP, "https://user.github.io", "", "", "domain_expiry_unsupported_is_icann"},
		{monitor.TypePush, "https://example.com", "", "", "domain_expiry_unsupported_monitor_type"},
		{monitor.TypePort, "", "", "", "domain_expiry_unsupported_missing_target"},
		{monitor.TypePort, "localhost", "", "", "domain_expiry_unsupported_missing_target"},
	} {
		domain, suffix, err := Domain(c.kind, c.target)
		var unsupported *UnsupportedError
		if c.err != "" {
			if !errors.As(err, &unsupported) || unsupported.Key != c.err {
				t.Errorf("%s: %v, want %s", c.target, err, c.err)
			}
			continue
		}
		if err != nil || domain != c.domain || suffix != c.suffix {
			t.Errorf("%s: %s %s %v", c.target, domain, suffix, err)
		}
	}
}

type fakeStore struct {
	mu       sync.Mutex
	records  map[string]Record
	settings map[string]json.RawMessage
}

func (f *fakeStore) DomainExpiry(_ context.Context, domain string) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, found := f.records[domain]; found {
		return r, nil
	}
	return Record{Domain: domain}, nil
}

func (f *fakeStore) SaveDomainExpiry(_ context.Context, r Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records[r.Domain] = r
	return nil
}

func (f *fakeStore) Settings(context.Context) (map[string]json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[string]json.RawMessage{}
	for k, v := range f.settings {
		out[k] = v
	}
	return out, nil
}

func (f *fakeStore) SaveSettings(_ context.Context, s map[string]json.RawMessage) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, v := range s {
		f.settings[k] = v
	}
	return nil
}

type fakeSender struct {
	mu   sync.Mutex
	sent []string
}

func (f *fakeSender) SendText(_ context.Context, _ monitor.Monitor, text string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sent = append(f.sent, text)
	return true
}

func TestExpiryAlerts(t *testing.T) {
	var expiry atomic.Value
	var lookups atomic.Int32
	rdap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/bootstrap":
			_, _ = fmt.Fprintf(w, `{"services": [[["com", "net"], ["http://%s/rdap/"]]]}`, r.Host)
		case "/rdap/domain/example.com":
			lookups.Add(1)
			_, _ = fmt.Fprintf(w, `{"events": [{"eventAction": "registration", "eventDate": "1995-08-14T04:00:00Z"},
				{"eventAction": "expiration", "eventDate": "%s"}]}`, expiry.Load().(time.Time).Format(time.RFC3339))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(rdap.Close)

	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	expiry.Store(now.Add(20*24*time.Hour + time.Hour))
	store := &fakeStore{records: map[string]Record{}, settings: map[string]json.RawMessage{}}
	sender := &fakeSender{}
	service := NewService(store, sender, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.BootstrapURL = rdap.URL + "/bootstrap"
	service.now = func() time.Time { return now }
	var changed atomic.Int32
	service.OnChange(func(id int32, days int, _ time.Time) {
		if id == 1 && days == 20 {
			changed.Add(1)
		}
	})
	m := monitor.Monitor{ID: 1, Type: monitor.TypeHTTP, URL: "https://www.example.com/", Settings: monitor.Settings{DomainExpiryNotification: true}}
	ctx := context.Background()

	if domain, tld, err := service.Check(ctx, m.Type, m.URL); err != nil || domain != "example.com" || tld != "com" {
		t.Fatalf("check: %s %s %v", domain, tld, err)
	}
	var unsupported *UnsupportedError
	if _, _, err := service.Check(ctx, monitor.TypeHTTP, "https://example.org"); !errors.As(err, &unsupported) ||
		unsupported.Key != "domain_expiry_unsupported_unsupported_tld_no_rdap_endpoint" || unsupported.Meta["publicSuffix"] != "org" {
		t.Fatalf("no RDAP server: %v", err)
	}

	// 20 days left: the 21-day deadline alerts, once.
	for range 2 {
		if err := service.handle(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if len(sender.sent) != 1 || sender.sent[0] != "Domain name example.com will expire in 20 days" || lookups.Load() != 1 || changed.Load() != 2 {
		t.Fatalf("first alert: %v, %d lookups, %d changes", sender.sent, lookups.Load(), changed.Load())
	}
	// A week later the 14-day deadline alerts, after another lookup.
	now = now.Add(7 * 24 * time.Hour)
	if err := service.handle(ctx, m); err != nil {
		t.Fatal(err)
	}
	if len(sender.sent) != 2 || sender.sent[1] != "Domain name example.com will expire in 13 days" || lookups.Load() != 2 {
		t.Fatalf("second alert: %v, %d lookups", sender.sent, lookups.Load())
	}
	// A renewal starts the deadlines again.
	expiry.Store(now.Add(400 * 24 * time.Hour))
	now = now.Add(25 * time.Hour)
	if err := service.handle(ctx, m); err != nil {
		t.Fatal(err)
	}
	if r := store.records["example.com"]; r.LastNotifiedDays != nil || len(sender.sent) != 2 {
		t.Fatalf("after renewal: %+v %v", r, sender.sent)
	}
	if expires, err := service.Expiry(ctx, m.Type, m.URL); err != nil || expires == nil || !expires.Equal(expiry.Load().(time.Time)) {
		t.Fatalf("expiry: %v %v", expires, err)
	}
	if _, found := store.settings[bootstrapSetting]; !found {
		t.Error("IANA's list wasn't saved")
	}
}

func TestBuiltInBootstrap(t *testing.T) {
	store := &fakeStore{records: map[string]Record{}, settings: map[string]json.RawMessage{}}
	service := NewService(store, &fakeSender{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	service.BootstrapURL = "http://127.0.0.1:1/unreachable"
	if server := service.rdapServer(context.Background(), "com"); server == "" {
		t.Fatal("the built-in list has no server for .com")
	}
}
