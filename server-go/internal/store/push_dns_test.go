package store

import (
	"context"
	"errors"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func pushMonitor(name, token string) monitor.Monitor {
	m := monitor.New()
	m.Name, m.Type, m.PushToken, m.Active = name, monitor.TypePush, token, false
	return m
}

// TestPushTokenLifecycle covers what senders depend on: a token appears on
// creation, survives edits that say nothing about it, and can never collide.
func TestPushTokenLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	created, err := s.CreateMonitor(ctx, pushMonitor("heartbeat", ""))
	if err != nil {
		t.Fatal(err)
	}
	if created.PushToken == "" {
		t.Fatal("CreateMonitor generated no push token")
	}
	// The token must round-trip through the store unchanged.
	got, err := s.GetMonitor(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.PushToken != created.PushToken {
		t.Fatalf("stored token = %q, want %q", got.PushToken, created.PushToken)
	}

	// An edit that leaves the token empty keeps the one senders already hold.
	edited := created
	edited.Name = "renamed"
	edited.PushToken = ""
	updated, err := s.UpdateMonitor(ctx, edited)
	if err != nil {
		t.Fatal(err)
	}
	if updated.PushToken != created.PushToken {
		t.Fatalf("token after edit = %q, want %q", updated.PushToken, created.PushToken)
	}

	// A second monitor may not take a token that is already in use.
	conflict := pushMonitor("impostor", created.PushToken)
	if _, err := s.CreateMonitor(ctx, conflict); !errors.Is(err, ErrPushTokenTaken) {
		t.Fatalf("duplicate token: err = %v, want ErrPushTokenTaken", err)
	}
	// Editing another monitor onto that token fails the same way.
	other, err := s.CreateMonitor(ctx, pushMonitor("other", ""))
	if err != nil {
		t.Fatal(err)
	}
	other.PushToken = created.PushToken
	if _, err := s.UpdateMonitor(ctx, other); !errors.Is(err, ErrPushTokenTaken) {
		t.Fatalf("duplicate token on update: err = %v, want ErrPushTokenTaken", err)
	}

	// A deliberate token change is honored.
	other.PushToken = "a-brand-new-token"
	changed, err := s.UpdateMonitor(ctx, other)
	if err != nil {
		t.Fatal(err)
	}
	if changed.PushToken != "a-brand-new-token" {
		t.Fatalf("token after change = %q", changed.PushToken)
	}

	// Non-push monitors never get a token, so the column stays free for them.
	httpM := monitor.New()
	httpM.Name, httpM.Type, httpM.URL = "web", monitor.TypeHTTP, "https://example.com"
	httpM.Active = false
	web, err := s.CreateMonitor(ctx, httpM)
	if err != nil {
		t.Fatal(err)
	}
	if web.PushToken != "" {
		t.Fatalf("http monitor got a push token %q", web.PushToken)
	}
}

func TestSetDNSLastResultWritesOnlyChanges(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	m := monitor.New()
	m.Name, m.Type, m.Active = "resolver", monitor.TypeDNS, false
	m.Hostname, m.Port, m.DNSResolveType, m.DNSResolveServer = "example.com", 53, "A", "1.1.1.1"
	created, err := s.CreateMonitor(ctx, m)
	if err != nil {
		t.Fatal(err)
	}

	xmin := func() string {
		t.Helper()
		var x string
		if err := s.pool.QueryRow(ctx, `SELECT xmin::text FROM monitor WHERE id = $1`, created.ID).Scan(&x); err != nil {
			t.Fatal(err)
		}
		return x
	}

	if err := s.SetDNSLastResult(ctx, created.ID, "Records: 93.184.216.34"); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMonitor(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DNSLastResult != "Records: 93.184.216.34" {
		t.Fatalf("DNSLastResult = %q", got.DNSLastResult)
	}
	first := xmin()

	// The same answer again must not touch the row.
	if err := s.SetDNSLastResult(ctx, created.ID, "Records: 93.184.216.34"); err != nil {
		t.Fatal(err)
	}
	if xmin() != first {
		t.Fatal("unchanged DNS result still wrote the row")
	}

	// A changed answer writes again.
	if err := s.SetDNSLastResult(ctx, created.ID, "Records: 2001:db8::1"); err != nil {
		t.Fatal(err)
	}
	if xmin() == first {
		t.Fatal("changed DNS result did not write the row")
	}
	got, err = s.GetMonitor(ctx, created.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.DNSLastResult != "Records: 2001:db8::1" {
		t.Fatalf("DNSLastResult = %q", got.DNSLastResult)
	}
}
