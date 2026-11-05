package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

type fakeStore struct {
	notifications []store.Notification
	err           error
	lastDown      *time.Time
}

func (f *fakeStore) ActiveNotificationsFor(context.Context, int32) ([]store.Notification, error) {
	return f.notifications, f.err
}
func (f *fakeStore) LastDownTime(context.Context, int32) (*time.Time, error) { return f.lastDown, nil }
func (f *fakeStore) MonitorPath(context.Context, int32) ([]string, error) {
	return []string{"Group", "Site"}, nil
}

var quietLog = slog.New(slog.NewTextHandler(io.Discard, nil))

type hook struct {
	mu       sync.Mutex
	received []M
	status   atomic.Int32
	calls    atomic.Int32
	server   *httptest.Server
}

func newHook(t *testing.T) *hook {
	t.Helper()
	h := &hook{}
	h.status.Store(200)
	h.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h.calls.Add(1)
		var body M
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.received = append(h.received, body)
		h.mu.Unlock()
		w.WriteHeader(int(h.status.Load()))
	}))
	t.Cleanup(h.server.Close)
	return h
}

func (h *hook) notification() store.Notification {
	return store.Notification{ID: 1, Name: "hook", Type: "webhook", Active: true, Config: M{"webhookURL": h.server.URL, "webhookContentType": "json"}}
}

func site() monitor.Monitor {
	m := monitor.New()
	m.ID, m.Name, m.Type = 7, "Site", monitor.TypeHTTP
	m.URL = "https://user:pass@example.com/health?token=secret"
	return m
}

func TestDispatcherSendsWhatProvidersExpect(t *testing.T) {
	h := newHook(t)
	down := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	location, err := time.LoadLocation("Asia/Hong_Kong")
	if err != nil {
		t.Fatal(err)
	}
	d := NewDispatcher(&fakeStore{notifications: []store.Notification{h.notification()}, lastDown: &down}, DefaultEnv(""), location, quietLog)
	ping := 12.5
	d.Notify(site(), store.Beat{MonitorID: 7, Time: down.Add(time.Hour), Status: monitor.StatusUp, Msg: "200 OK", Important: true, Ping: &ping, TraceID: "abc"})
	d.Close()

	if len(h.received) != 1 {
		t.Fatalf("received %d", len(h.received))
	}
	body := h.received[0]
	if body["msg"] != "[Site] [✅ Up] 200 OK" {
		t.Errorf("msg: %v", body["msg"])
	}
	beat := body["heartbeat"].(map[string]any)
	for field, want := range map[string]any{
		"time": "2026-01-02 04:00:00.000", "localDateTime": "2026-01-02 12:00:00", "timezone": "Asia/Hong_Kong",
		"timezoneOffset": "+08:00", "lastDownTime": "2026-01-02 03:00:00.000", "traceId": "abc", "ping": 12.5, "status": float64(1),
	} {
		if beat[field] != want {
			t.Errorf("heartbeat.%s = %v, want %v", field, beat[field], want)
		}
	}
	mon := body["monitor"].(map[string]any)
	if mon["pathName"] != "Group / Site" || mon["id"] != float64(7) {
		t.Errorf("monitor: %v", mon)
	}
	encoded, _ := json.Marshal(body)
	if strings.Contains(string(encoded), "pass") || strings.Contains(string(encoded), "secret") {
		t.Errorf("monitor credentials went to a third party: %s", encoded)
	}
}

func TestDispatcherRetries(t *testing.T) {
	cases := []struct {
		name   string
		status int32
		calls  int32
	}{{"a server error is retried", 503, maxAttempts}, {"rate limiting is retried", 429, maxAttempts}, {"a rejection isn't", 401, 1}, {"success stops", 200, 1}}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHook(t)
			h.status.Store(c.status)
			d := NewDispatcher(&fakeStore{notifications: []store.Notification{h.notification()}}, DefaultEnv(""), time.UTC, quietLog)
			d.backoff = time.Millisecond
			d.Notify(site(), store.Beat{MonitorID: 7, Status: monitor.StatusDown})
			// Stopping gives up on retries, so let them happen first.
			for deadline := time.Now().Add(5 * time.Second); h.calls.Load() < c.calls && time.Now().Before(deadline); {
				time.Sleep(time.Millisecond)
			}
			time.Sleep(20 * time.Millisecond)
			d.Close()
			if got := h.calls.Load(); got != c.calls {
				t.Errorf("calls: %d, want %d", got, c.calls)
			}
		})
	}
}

func TestOneFailingNotificationDoesntStopTheOthers(t *testing.T) {
	bad, good := newHook(t), newHook(t)
	bad.status.Store(401)
	d := NewDispatcher(&fakeStore{notifications: []store.Notification{bad.notification(), good.notification()}}, DefaultEnv(""), time.UTC, quietLog)
	d.Notify(site(), store.Beat{MonitorID: 7, Status: monitor.StatusDown})
	d.Close()
	if good.calls.Load() != 1 {
		t.Error("the second notification wasn't sent")
	}
}

func TestEventsForAMonitorStayInOrder(t *testing.T) {
	h := newHook(t)
	d := NewDispatcher(&fakeStore{notifications: []store.Notification{h.notification()}}, DefaultEnv(""), time.UTC, quietLog)
	statuses := []int16{monitor.StatusDown, monitor.StatusUp, monitor.StatusDown, monitor.StatusUp, monitor.StatusDown}
	for _, status := range statuses {
		d.Notify(site(), store.Beat{MonitorID: 7, Status: status})
	}
	d.Close()
	if len(h.received) != len(statuses) {
		t.Fatalf("received %d", len(h.received))
	}
	for i, status := range statuses {
		if h.received[i]["heartbeat"].(map[string]any)["status"] != float64(status) {
			t.Fatalf("event %d out of order", i)
		}
	}
}

func TestNotifyNeverBlocksAndCloseIsBounded(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { <-release }))
	defer server.Close()
	defer close(release)
	n := store.Notification{ID: 1, Type: "webhook", Config: M{"webhookURL": server.URL, "webhookContentType": "json"}}
	d := NewDispatcher(&fakeStore{notifications: []store.Notification{n}}, DefaultEnv(""), time.UTC, quietLog)

	start := time.Now()
	for range queuePerShard * 2 {
		d.Notify(site(), store.Beat{MonitorID: 7, Status: monitor.StatusDown})
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("Notify blocked for %s", elapsed)
	}
	d.closeWait = 100 * time.Millisecond
	start = time.Now()
	d.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Close took %s", elapsed)
	}
	// The scheduler may still be finishing a check.
	d.Notify(site(), store.Beat{MonitorID: 7})
	d.Close()
}

func TestStoppingGivesUpOnRetries(t *testing.T) {
	h := newHook(t)
	h.status.Store(503)
	d := NewDispatcher(&fakeStore{notifications: []store.Notification{h.notification()}}, DefaultEnv(""), time.UTC, quietLog)
	d.backoff = time.Hour
	d.Notify(site(), store.Beat{MonitorID: 7, Status: monitor.StatusDown})
	for deadline := time.Now().Add(5 * time.Second); h.calls.Load() == 0 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	start := time.Now()
	d.Close()
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("Close waited for a backoff: %s", elapsed)
	}
}

func TestReadFailureSendsNothing(t *testing.T) {
	d := NewDispatcher(&fakeStore{err: errors.New("db down")}, DefaultEnv(""), time.UTC, quietLog)
	d.Notify(site(), store.Beat{MonitorID: 7})
	d.Close()
}

func TestFormatOffset(t *testing.T) {
	for seconds, want := range map[int]string{0: "+00:00", 28800: "+08:00", -12600: "-03:30", 20700: "+05:45"} {
		if got := formatOffset(seconds); got != want {
			t.Errorf("%d: %s", seconds, got)
		}
	}
}
