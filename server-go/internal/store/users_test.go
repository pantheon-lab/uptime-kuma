package store

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestFirstUserAndSessions(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	if has, err := s.HasUser(ctx); err != nil || has {
		t.Fatalf("HasUser on empty: %v %v", has, err)
	}

	// Only one of several concurrent setups may win.
	var wg sync.WaitGroup
	results := make([]error, 5)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = s.CreateFirstUser(ctx, "admin", "salt:hash")
		}(i)
	}
	wg.Wait()
	wins := 0
	for _, err := range results {
		switch {
		case err == nil:
			wins++
		case !errors.Is(err, ErrSetupDone):
			t.Fatalf("unexpected error: %v", err)
		}
	}
	if wins != 1 {
		t.Fatalf("%d setups succeeded", wins)
	}

	user, err := s.UserByUsername(ctx, "ADMIN")
	if err != nil || user.Username != "admin" {
		t.Fatalf("UserByUsername: %+v %v", user, err)
	}
	if _, err := s.UserByUsername(ctx, "nobody"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown user: %v", err)
	}

	keep, other, expired := []byte("keep"), []byte("other"), []byte("expired")
	future := time.Now().Add(time.Hour)
	for _, h := range [][]byte{keep, other} {
		if err := s.CreateSession(ctx, h, Session{UserID: user.ID, Remember: true, ExpiresAt: future}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(ctx, expired, Session{UserID: user.ID, ExpiresAt: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateSession(ctx, []byte("orphan"), Session{UserID: 999, ExpiresAt: future}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("session for a missing user: %v", err)
	}

	session, found, err := s.FindSession(ctx, keep)
	if err != nil || found.ID != user.ID || !session.Remember {
		t.Fatalf("FindSession: %+v %+v %v", session, found, err)
	}
	if _, _, err := s.FindSession(ctx, expired); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired session found: %v", err)
	}

	later := time.Now().Add(48 * time.Hour).Truncate(time.Microsecond)
	if err := s.ExtendSession(ctx, keep, later); err != nil {
		t.Fatal(err)
	}
	if session, _, _ := s.FindSession(ctx, keep); !session.ExpiresAt.Equal(later) {
		t.Fatalf("extended to %v, want %v", session.ExpiresAt, later)
	}

	if err := s.SetPassword(ctx, user.ID, "new:hash", keep); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindSession(ctx, other); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other session survived a password change: %v", err)
	}
	if _, u, err := s.FindSession(ctx, keep); err != nil || u.PasswordHash != "new:hash" {
		t.Fatalf("kept session: %+v %v", u, err)
	}
	if err := s.DeleteSession(ctx, keep); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindSession(ctx, keep); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted session found: %v", err)
	}
}

func TestSettings(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{"a": json.RawMessage(`"x"`), "b": json.RawMessage(`{"n": 1}`)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{"a": json.RawMessage(`null`)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Settings(ctx)
	if err != nil || string(got["a"]) != "null" || string(got["b"]) != `{"n": 1}` {
		t.Fatalf("settings: %s %s %v", got["a"], got["b"], err)
	}
}

func TestUIQueries(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	newMonitor := func(name string) monitor.Monitor {
		m := monitor.New()
		m.Name, m.Type, m.URL = name, monitor.TypeHTTP, "https://example.com"
		created, err := s.CreateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	a, b, quiet := newMonitor("a"), newMonitor("b"), newMonitor("quiet")

	now := time.Now().UTC().Truncate(time.Millisecond)
	_, err := s.InsertBeats(ctx, []Beat{
		{MonitorID: a.ID, Time: now.Add(-3 * time.Hour), Status: 0, Important: true},
		{MonitorID: a.ID, Time: now.Add(-2 * time.Minute), Status: 1, Important: true},
		{MonitorID: a.ID, Time: now.Add(-time.Minute), Status: 1},
		{MonitorID: b.ID, Time: now.Add(-30 * time.Second), Status: 0, Important: true},
	})
	if err != nil {
		t.Fatal(err)
	}

	latest, err := s.LatestBeatPerMonitor(ctx)
	if err != nil || len(latest) != 2 {
		t.Fatalf("latest: %v %v", latest, err)
	}
	for _, beat := range latest {
		if beat.MonitorID == quiet.ID || (beat.MonitorID == a.ID && !beat.Time.Equal(now.Add(-time.Minute))) {
			t.Fatalf("latest beat: %+v", beat)
		}
	}

	since, err := s.BeatsSince(ctx, a.ID, now.Add(-time.Hour), 10)
	if err != nil || len(since) != 2 || !since[0].Time.Before(since[1].Time) {
		t.Fatalf("since: %v %v", since, err)
	}

	if n, err := s.CountImportantBeats(ctx, nil); err != nil || n != 3 {
		t.Fatalf("count all: %d %v", n, err)
	}
	if n, err := s.CountImportantBeats(ctx, &a.ID); err != nil || n != 2 {
		t.Fatalf("count a: %d %v", n, err)
	}
	page, err := s.ImportantBeats(ctx, nil, 1, 1)
	if err != nil || len(page) != 1 || page[0].MonitorID != a.ID || page[0].Status != 1 {
		t.Fatalf("page: %+v %v", page, err)
	}

	if err := s.ClearEvents(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountImportantBeats(ctx, nil); n != 1 {
		t.Fatalf("after clearing a: %d", n)
	}

	n, err := s.CreateNotification(ctx, Notification{Name: "n", Type: "webhook", Config: map[string]any{}, Active: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMonitorNotifications(ctx, b.ID, []int32{n.ID}); err != nil {
		t.Fatal(err)
	}
	links, err := s.AllMonitorNotificationIDs(ctx)
	if err != nil || len(links) != 1 || len(links[b.ID]) != 1 || links[b.ID][0] != n.ID {
		t.Fatalf("links: %v %v", links, err)
	}
}
