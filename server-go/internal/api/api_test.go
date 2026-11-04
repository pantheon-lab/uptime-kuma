package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const testToken = "0123456789abcdef0123456789abcdef"

type fakeStore struct {
	insertErr error
	inserted  []store.Beat
	created   []monitor.Monitor
}

func (f *fakeStore) Ping(context.Context) error { return nil }
func (f *fakeStore) InsertBeats(_ context.Context, b []store.Beat) ([]store.Beat, error) {
	if f.insertErr != nil {
		return nil, f.insertErr
	}
	f.inserted = append(f.inserted, b...)
	return b, nil
}
func (f *fakeStore) LatestBeats(context.Context, int32, int) ([]store.Beat, error) {
	return []store.Beat{}, nil
}
func (f *fakeStore) Chart(context.Context, int32, int) ([]store.ChartPoint, error) {
	return []store.ChartPoint{}, nil
}
func (f *fakeStore) Percentiles(_ context.Context, _ int32, _ int, p []float64) ([]*float64, error) {
	return make([]*float64, len(p)), nil
}
func (f *fakeStore) Stats(context.Context) ([]store.MonitorStats, error) {
	return []store.MonitorStats{}, nil
}

func (f *fakeStore) ListMonitors(context.Context) ([]monitor.Monitor, error) {
	return []monitor.Monitor{}, nil
}
func (f *fakeStore) GetMonitor(context.Context, int32) (monitor.Monitor, error) {
	return monitor.Monitor{}, store.ErrNotFound
}
func (f *fakeStore) CreateMonitor(_ context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	m.ID = 1
	f.created = append(f.created, m)
	return m, nil
}
func (f *fakeStore) UpdateMonitor(_ context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	return m, store.ErrNotFound
}
func (f *fakeStore) DeleteMonitor(context.Context, int32) error { return store.ErrNotFound }

type nopScheduler struct{}

func (nopScheduler) Upsert(monitor.Monitor)              {}
func (nopScheduler) Remove(context.Context, int32) error { return nil }

func newTestServer(t *testing.T, s Store) (*httptest.Server, *Hub) {
	t.Helper()
	hub := NewHub()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(s, nopScheduler{}, hub, testToken, log).Handler())
	t.Cleanup(func() { hub.Close(); ts.Close() })
	return ts, hub
}

func do(t *testing.T, method, url, token, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	return res
}

func TestAuthRequiredExceptHealth(t *testing.T) {
	ts, _ := newTestServer(t, &fakeStore{})
	for _, path := range []string{"/api/v1/stats", "/api/v1/events", "/api/v1/monitors", "/api/v1/monitors/1", "/api/v1/monitors/1/beats", "/api/v1/monitors/1/chart", "/api/v1/monitors/1/percentiles"} {
		for _, token := range []string{"", "wrong"} {
			if res := do(t, "GET", ts.URL+path, token, ""); res.StatusCode != http.StatusUnauthorized {
				t.Errorf("%s with token %q: got %d", path, token, res.StatusCode)
			}
		}
	}
	if res := do(t, "POST", ts.URL+"/api/v1/beats", "", "[]"); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("POST beats without token: got %d", res.StatusCode)
	}
	if res := do(t, "GET", ts.URL+"/healthz", "", ""); res.StatusCode != http.StatusOK {
		t.Errorf("healthz: got %d", res.StatusCode)
	}
}

func TestValidateBeats(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	neg := -1.0
	good := func() store.Beat { return store.Beat{MonitorID: 1, Time: now, Status: 1} }
	cases := map[string]func(*store.Beat){
		"monitor id":  func(b *store.Beat) { b.MonitorID = 0 },
		"zero time":   func(b *store.Beat) { b.Time = time.Time{} },
		"future":      func(b *store.Beat) { b.Time = now.Add(2 * time.Hour) },
		"low status":  func(b *store.Beat) { b.Status = -1 },
		"high status": func(b *store.Beat) { b.Status = 4 },
		"ping":        func(b *store.Beat) { b.Ping = &neg },
		"duration":    func(b *store.Beat) { b.Duration = -1 },
		"retries":     func(b *store.Beat) { b.Retries = -1 },
		"long msg":    func(b *store.Beat) { b.Msg = strings.Repeat("x", maxMsgBytes+1) },
		"nul msg":     func(b *store.Beat) { b.Msg = "a\x00b" },
	}
	for name, mutate := range cases {
		b := good()
		mutate(&b)
		if err := validateBeats([]store.Beat{good(), b}, now); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	if err := validateBeats(make([]store.Beat, maxBeatsPerRequest+1), now); err == nil {
		t.Error("oversized batch: expected an error")
	}

	precise := []store.Beat{{MonitorID: 1, Status: 1, Time: time.Date(2026, 1, 1, 12, 0, 0, 1999, time.FixedZone("x", 3600))}}
	if err := validateBeats(precise, now); err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 1, 1, 11, 0, 0, 1000, time.UTC); !precise[0].Time.Equal(want) || precise[0].Time.Location() != time.UTC {
		t.Errorf("time not normalized: %v", precise[0].Time)
	}
}

func TestPostBeatsErrors(t *testing.T) {
	f := &fakeStore{}
	ts, _ := newTestServer(t, f)
	if res := do(t, "POST", ts.URL+"/api/v1/beats", testToken, "{"); res.StatusCode != http.StatusBadRequest {
		t.Errorf("bad JSON: got %d", res.StatusCode)
	}
	if res := do(t, "POST", ts.URL+"/api/v1/beats", testToken, `[{"monitorId":0}]`); res.StatusCode != http.StatusBadRequest {
		t.Errorf("invalid beat: got %d", res.StatusCode)
	}
	if len(f.inserted) != 0 {
		t.Errorf("invalid requests reached the store: %v", f.inserted)
	}

	f.insertErr = errors.New("connection string with secret")
	body := `[{"monitorId":1,"time":"2026-01-01T00:00:00Z","status":1}]`
	res := do(t, "POST", ts.URL+"/api/v1/beats", testToken, body)
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusInternalServerError || strings.Contains(string(data), "secret") {
		t.Errorf("store failure: status %d body %s", res.StatusCode, data)
	}
}

func TestQueryParamValidation(t *testing.T) {
	ts, _ := newTestServer(t, &fakeStore{})
	for _, path := range []string{
		"/api/v1/monitors/abc/beats",
		"/api/v1/monitors/-1/beats",
		"/api/v1/monitors/99999999999/beats",
		"/api/v1/monitors/1/beats?limit=0",
		"/api/v1/monitors/1/beats?limit=1001",
		"/api/v1/monitors/1/chart?hours=0",
		"/api/v1/monitors/1/chart?hours=8761",
		"/api/v1/monitors/1/percentiles?p=101",
		"/api/v1/monitors/1/percentiles?p=NaN",
		"/api/v1/monitors/1/percentiles?p=1,2,3,4,5,6,7,8,9,10,11",
		"/api/v1/events?monitorId=x",
	} {
		if res := do(t, "GET", ts.URL+path, testToken, ""); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: got %d", path, res.StatusCode)
		}
	}
}

func TestStreamFiltersAndEnds(t *testing.T) {
	f := &fakeStore{}
	ts, hub := newTestServer(t, f)
	res := do(t, "GET", ts.URL+"/api/v1/events?monitorId=2", testToken, "")
	if got := res.Header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type %q", got)
	}
	reader := bufio.NewReader(res.Body)
	if line, err := reader.ReadString('\n'); err != nil || line != ": connected\n" {
		t.Fatalf("first line %q, err %v", line, err)
	}

	body := `[{"monitorId":1,"time":"2026-01-01T00:00:00Z","status":1},{"monitorId":2,"time":"2026-01-01T00:00:00Z","status":0}]`
	if post := do(t, "POST", ts.URL+"/api/v1/beats", testToken, body); post.StatusCode != http.StatusOK {
		t.Fatalf("post: %d", post.StatusCode)
	}

	var beat store.Beat
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if data, ok := strings.CutPrefix(line, "data: "); ok {
			if err := json.Unmarshal([]byte(data), &beat); err != nil {
				t.Fatal(err)
			}
			break
		}
	}
	if beat.MonitorID != 2 || beat.Status != 0 {
		t.Errorf("wrong beat: %+v", beat)
	}

	hub.Close()
	if _, err := io.ReadAll(reader); err != nil {
		t.Errorf("stream did not end cleanly: %v", err)
	}
}

func TestStreamOutlivesServerTimeouts(t *testing.T) {
	hub := NewHub()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewUnstartedServer(New(&fakeStore{}, nopScheduler{}, hub, testToken, log).Handler())
	ts.Config.ReadTimeout = 100 * time.Millisecond
	ts.Config.WriteTimeout = 100 * time.Millisecond
	ts.Start()
	t.Cleanup(func() { hub.Close(); ts.Close() })

	res := do(t, "GET", ts.URL+"/api/v1/events", testToken, "")
	reader := bufio.NewReader(res.Body)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	hub.Publish([]store.Beat{{MonitorID: 9, Status: 1, Time: time.Now()}})
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatalf("stream died after the server timeouts: %v", err)
		}
		if strings.HasPrefix(line, "data: ") {
			return
		}
	}
}

func TestHubDropsSlowSubscriberWithoutBlocking(t *testing.T) {
	hub := NewHub()
	slow, unsubscribe := hub.Subscribe()
	defer unsubscribe()

	done := make(chan struct{})
	go func() {
		hub.Publish(make([]store.Beat, subscriberBuffer+1))
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Publish blocked on a slow subscriber")
	}
	count := 0
	for range slow {
		count++
	}
	if count != subscriberBuffer {
		t.Errorf("drained %d beats, want %d", count, subscriberBuffer)
	}
	unsubscribe() // Must not panic after the hub already closed the channel.

	hub.Close()
	closed, _ := hub.Subscribe()
	if _, open := <-closed; open {
		t.Error("subscribing to a closed hub should return a closed channel")
	}
}

func TestMonitorRoutesValidateInput(t *testing.T) {
	f := &fakeStore{}
	ts, _ := newTestServer(t, f)
	url := ts.URL + "/api/v1/monitors"
	for name, body := range map[string]string{
		"bad JSON":      "{",
		"unknown field": `{"name":"a","type":"http","url":"http://a","intervall":5}`,
		"invalid":       `{"name":"a","type":"dns"}`,
	} {
		if res := do(t, "POST", url, testToken, body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: got %d", name, res.StatusCode)
		}
	}
	if len(f.created) != 0 {
		t.Errorf("invalid monitors reached the store: %v", f.created)
	}

	res := do(t, "POST", url, testToken, `{"name":"a","type":"http","url":"http://a"}`)
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("create: got %d", res.StatusCode)
	}
	if len(f.created) != 1 || f.created[0].Interval != 60 || !f.created[0].Active || f.created[0].Method != "GET" {
		t.Errorf("defaults not applied: %+v", f.created)
	}

	for _, r := range [][3]string{
		{"GET", url + "/7", ""},
		{"DELETE", url + "/7", ""},
		{"PUT", url + "/7", `{"name":"a","type":"http","url":"http://a"}`},
	} {
		if res := do(t, r[0], r[1], testToken, r[2]); res.StatusCode != http.StatusNotFound {
			t.Errorf("%s missing monitor: got %d", r[0], res.StatusCode)
		}
	}
}
