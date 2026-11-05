package notify_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/scheduler"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

type alert struct {
	Msg       string         `json:"msg"`
	Heartbeat map[string]any `json:"heartbeat"`
}

type alerts struct {
	mu   sync.Mutex
	list []alert
}

func (a *alerts) add(item alert) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.list = append(a.list, item)
}

func (a *alerts) snapshot() []alert {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]alert(nil), a.list...)
}

func (a *alerts) waitFor(t *testing.T, n int, within time.Duration) []alert {
	t.Helper()
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if list := a.snapshot(); len(list) >= n {
			return list
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("got %d alerts, wanted %d: %+v", len(a.snapshot()), n, a.snapshot())
	return nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("kuma_notify_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	db, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		db.Close()
		_, _ = conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = conn.Close(ctx)
	})
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	return db
}

type service struct {
	stop func()
}

func start(t *testing.T, db *store.Store, log *slog.Logger) *service {
	t.Helper()
	ctx := context.Background()
	monitors, err := db.ListMonitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := db.LatestStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	writer := scheduler.NewWriter(db, func([]store.Beat) {}, log)
	go writer.Run()
	checker := check.New(db)
	dispatcher := notify.NewDispatcher(db, notify.DefaultEnv(""), time.UTC, log)
	sched := scheduler.New(checker, writer, 10, log)
	sched.SetNotifier(dispatcher)
	sched.Start(monitors, previous)
	return &service{stop: func() {
		sched.Stop()
		if err := writer.Close(ctx); err != nil {
			t.Error(err)
		}
		dispatcher.Close()
		checker.Close()
	}}
}

// A site goes down and comes back, with the real scheduler, checker, database and HTTP sends.
func TestAlertsFromCheckToWebhook(t *testing.T) {
	db := openStore(t)
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	var healthy atomic.Bool
	healthy.Store(true)
	site := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer site.Close()

	received := &alerts{}
	hook := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		var item alert
		if err := json.NewDecoder(r.Body).Decode(&item); err == nil {
			received.add(item)
		}
	}))
	defer hook.Close()

	m := monitor.New()
	m.Name, m.Type, m.URL, m.Active, m.Interval, m.RetryInterval, m.ResendInterval = "Site", monitor.TypeHTTP, site.URL, true, 1, 1, 3
	created, err := db.CreateMonitor(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	config := map[string]any{"webhookURL": hook.URL, "webhookContentType": "json"}
	if _, err := db.CreateNotification(ctx, store.Notification{Name: "hook", Type: "webhook", Config: config, Active: true}, true); err != nil {
		t.Fatal(err)
	}

	svc := start(t, db, log)
	// A healthy new monitor says nothing.
	time.Sleep(2500 * time.Millisecond)
	if list := received.snapshot(); len(list) != 0 {
		t.Fatalf("alerted about a healthy monitor: %+v", list)
	}

	healthy.Store(false)
	list := received.waitFor(t, 1, 5*time.Second)
	if list[0].Msg == "" || list[0].Heartbeat["status"] != float64(monitor.StatusDown) || list[0].Heartbeat["important"] != true {
		t.Fatalf("down: %+v", list[0])
	}

	// Stopping part way through a resend interval mustn't lose count of the beats so far.
	time.Sleep(1500 * time.Millisecond)
	svc.stop()
	beforeRestart := len(received.snapshot())
	states, err := db.LatestStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if states[created.ID].Status != monitor.StatusDown {
		t.Fatalf("state: %+v", states[created.ID])
	}
	svc = start(t, db, log)
	defer func() { svc.stop() }()
	list = received.waitFor(t, beforeRestart+1, 8*time.Second)
	resend := list[beforeRestart]
	if resend.Heartbeat["status"] != float64(monitor.StatusDown) || resend.Heartbeat["important"] != false {
		t.Fatalf("resend: %+v", resend)
	}

	healthy.Store(true)
	deadline := time.Now().Add(6 * time.Second)
	for {
		list = received.snapshot()
		last := list[len(list)-1]
		if last.Heartbeat["status"] == float64(monitor.StatusUp) {
			if last.Heartbeat["lastDownTime"] == nil {
				t.Fatalf("recovery has no lastDownTime: %+v", last)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no recovery alert: %+v", list)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Recovered, and quiet from then on.
	settled := len(received.snapshot())
	time.Sleep(2500 * time.Millisecond)
	if extra := len(received.snapshot()) - settled; extra != 0 {
		t.Fatalf("%d alerts after recovery", extra)
	}
}
