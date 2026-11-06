package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
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

func TestTwoFactorStore(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	user, err := s.CreateFirstUser(ctx, "admin", "salt:hash")
	if err != nil {
		t.Fatal(err)
	}
	if user.TOTPEnabled || user.TOTPSecret != nil {
		t.Fatalf("new user: %+v", user)
	}
	future := time.Now().Add(time.Hour)
	for _, h := range []string{"keep", "other"} {
		if err := s.CreateSession(ctx, []byte(h), Session{UserID: user.ID, ExpiresAt: future}); err != nil {
			t.Fatal(err)
		}
	}

	first, second := []byte("first key"), []byte("second key")
	if err := s.StartTOTPSetup(ctx, user.ID, first); err != nil {
		t.Fatal(err)
	}
	// Set up again before confirming, as from another tab: the first key no longer works.
	if err := s.StartTOTPSetup(ctx, user.ID, second); err != nil {
		t.Fatal(err)
	}
	if err := s.EnableTOTP(ctx, user.ID, first, 100, []byte("keep")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("enable with a replaced key: %v", err)
	}
	if err := s.EnableTOTP(ctx, user.ID, second, 100, []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindSession(ctx, []byte("other")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("other session after enabling: %v", err)
	}
	_, u, err := s.FindSession(ctx, []byte("keep"))
	if err != nil || !u.TOTPEnabled || string(u.TOTPSecret) != "second key" {
		t.Fatalf("kept session: %+v %v", u, err)
	}
	if err := s.StartTOTPSetup(ctx, user.ID, first); !errors.Is(err, ErrTOTPEnabled) {
		t.Fatalf("setup while enabled: %v", err)
	}
	if err := s.StartTOTPSetup(ctx, 999, first); !errors.Is(err, ErrNotFound) {
		t.Fatalf("setup of a missing user: %v", err)
	}

	// A step is used once, and an older one never.
	for _, tc := range []struct {
		step int64
		want bool
	}{{100, false}, {99, false}, {101, true}, {101, false}, {103, true}} {
		if got, err := s.UseTOTPStep(ctx, user.ID, tc.step); err != nil || got != tc.want {
			t.Fatalf("step %d: %v %v", tc.step, got, err)
		}
	}
	// Two requests with the same code: only one wins.
	var wg sync.WaitGroup
	wins := make(chan bool, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			used, err := s.UseTOTPStep(ctx, user.ID, 200)
			if err != nil {
				t.Error(err)
			}
			wins <- used
		}()
	}
	wg.Wait()
	close(wins)
	n := 0
	for w := range wins {
		if w {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d requests used the same step", n)
	}

	for range 2 {
		if err := s.RecordTOTPFailure(ctx, user.ID, 3, time.Minute); err != nil {
			t.Fatal(err)
		}
	}
	if u, _ := s.UserByID(ctx, user.ID); u.TOTPLockedUntil != nil {
		t.Fatal("locked too early")
	}
	if err := s.RecordTOTPFailure(ctx, user.ID, 3, time.Minute); err != nil {
		t.Fatal(err)
	}
	u, _ = s.UserByID(ctx, user.ID)
	if u.TOTPLockedUntil == nil || time.Until(*u.TOTPLockedUntil) < 50*time.Second {
		t.Fatalf("not locked: %+v", u.TOTPLockedUntil)
	}
	if used, _ := s.UseTOTPStep(ctx, user.ID, 300); !used {
		t.Fatal("a right code after the lock")
	}
	if u, _ := s.UserByID(ctx, user.ID); u.TOTPLockedUntil != nil {
		t.Fatal("a right code didn't clear the lock")
	}

	// Challenges.
	if err := s.CreateTwoFactorChallenge(ctx, []byte("c1"), TwoFactorChallenge{UserID: user.ID, Remember: true, ExpiresAt: future}); err != nil {
		t.Fatal(err)
	}
	if err := s.CreateTwoFactorChallenge(ctx, []byte("old"), TwoFactorChallenge{UserID: user.ID, ExpiresAt: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindTwoFactorChallenge(ctx, []byte("old")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expired challenge: %v", err)
	}
	c, cu, err := s.FindTwoFactorChallenge(ctx, []byte("c1"))
	if err != nil || !c.Remember || c.Attempts != 0 || cu.Username != "admin" || !cu.TOTPEnabled {
		t.Fatalf("challenge: %+v %+v %v", c, cu, err)
	}
	for want := 1; want <= 3; want++ {
		if n, err := s.CountTwoFactorAttempt(ctx, []byte("c1")); err != nil || n != want {
			t.Fatalf("attempt %d: %d %v", want, n, err)
		}
	}
	if _, err := s.CountTwoFactorAttempt(ctx, []byte("missing")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("attempt at a missing challenge: %v", err)
	}
	if ok, err := s.DeleteTwoFactorChallenge(ctx, []byte("c1")); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if ok, _ := s.DeleteTwoFactorChallenge(ctx, []byte("c1")); ok {
		t.Fatal("deleted twice")
	}

	// Changing the password or turning two-factor login off ends the waiting challenges.
	_ = s.CreateTwoFactorChallenge(ctx, []byte("c2"), TwoFactorChallenge{UserID: user.ID, ExpiresAt: future})
	if err := s.SetPassword(ctx, user.ID, "salt:new", []byte("keep")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindTwoFactorChallenge(ctx, []byte("c2")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("challenge after a password change: %v", err)
	}
	_ = s.CreateTwoFactorChallenge(ctx, []byte("c3"), TwoFactorChallenge{UserID: user.ID, ExpiresAt: future})
	if err := s.DisableTOTP(ctx, user.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.FindTwoFactorChallenge(ctx, []byte("c3")); !errors.Is(err, ErrNotFound) {
		t.Fatalf("challenge after disabling: %v", err)
	}
	if u, _ := s.UserByID(ctx, user.ID); u.TOTPEnabled || u.TOTPSecret != nil {
		t.Fatalf("after disabling: %+v", u)
	}
	if err := s.DisableTOTP(ctx, 999); !errors.Is(err, ErrNotFound) {
		t.Fatalf("disable a missing user: %v", err)
	}
}

func TestWebpushKeys(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var made atomic.Int32
	generate := func() (string, string, error) {
		n := made.Add(1)
		return fmt.Sprintf("public-%d", n), fmt.Sprintf("private-%d", n), nil
	}
	// The first uses race, and all of them get the same pair.
	type pair struct{ public, private string }
	results := make(chan pair, 8)
	for range 8 {
		go func() {
			public, private, err := s.WebpushKeys(ctx, generate)
			if err != nil {
				t.Error(err)
			}
			results <- pair{public, private}
		}()
	}
	first := <-results
	for range 7 {
		if got := <-results; got != first {
			t.Fatalf("%+v and %+v", first, got)
		}
	}
	if first.public == "" || first.private[len("private-"):] != first.public[len("public-"):] {
		t.Fatalf("mismatched pair %+v", first)
	}
	if imported, err := s.ImportWebpushKeys(ctx, "other-public", "other-private"); err != nil || imported {
		t.Fatalf("an import replaced the pair: %v %v", imported, err)
	}
	if public, _, _ := s.WebpushKeys(ctx, generate); public != first.public {
		t.Fatalf("the pair changed to %s", public)
	}

	fresh := newTestStore(t)
	if imported, err := fresh.ImportWebpushKeys(ctx, "node-public", "node-private"); err != nil || !imported {
		t.Fatalf("import: %v %v", imported, err)
	}
	if public, private, err := fresh.WebpushKeys(ctx, generate); err != nil || public != "node-public" || private != "node-private" {
		t.Fatalf("imported pair: %s %s %v", public, private, err)
	}
}
