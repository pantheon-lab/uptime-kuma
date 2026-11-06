package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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
	keys      map[string]store.APIKey
	// legacy are keys imported from Uptime Kuma's older table, with their bcrypt hash.
	legacy     map[int32]legacyKeyEntry
	findKeyErr error

	notifications  map[int32]store.Notification
	appliedToAll   bool
	linkedDefaults []int32
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

func (f *fakeStore) FindMonitors(context.Context, store.MonitorFilter) ([]monitor.Monitor, int, error) {
	return []monitor.Monitor{}, 0, nil
}
func (f *fakeStore) SetMonitorActive(context.Context, int32, bool) (monitor.Monitor, error) {
	return monitor.Monitor{}, store.ErrNotFound
}
func (f *fakeStore) CreateAPIKey(_ context.Context, key store.APIKey, _ []byte) (store.APIKey, error) {
	return key, nil
}
func (f *fakeStore) ListAPIKeys(context.Context) ([]store.APIKey, error) {
	return []store.APIKey{}, nil
}
func (f *fakeStore) RevokeAPIKey(context.Context, int64) error { return store.ErrNotFound }
func (f *fakeStore) FindAPIKey(_ context.Context, hash []byte) (store.APIKey, error) {
	if f.keys != nil {
		if key, ok := f.keys[string(hash)]; ok {
			return key, nil
		}
	}
	if f.findKeyErr != nil {
		return store.APIKey{}, f.findKeyErr
	}
	return store.APIKey{}, store.ErrNotFound
}

type legacyKeyEntry struct {
	key  store.APIKey
	hash string
}

func (f *fakeStore) FindLegacyAPIKey(_ context.Context, id int32) (store.APIKey, string, error) {
	if e, ok := f.legacy[id]; ok {
		return e.key, e.hash, nil
	}
	return store.APIKey{}, "", store.ErrNotFound
}

func (f *fakeStore) TouchAPIKey(context.Context, int64) error { return nil }
func (f *fakeStore) ListNotifications(context.Context) ([]store.Notification, error) {
	list := []store.Notification{}
	for _, n := range f.notifications {
		list = append(list, n)
	}
	return list, nil
}
func (f *fakeStore) GetNotification(_ context.Context, id int32) (store.Notification, error) {
	n, ok := f.notifications[id]
	if !ok {
		return n, store.ErrNotFound
	}
	return n, nil
}
func (f *fakeStore) CreateNotification(_ context.Context, n store.Notification, applyToAll bool) (store.Notification, error) {
	if f.notifications == nil {
		f.notifications = map[int32]store.Notification{}
	}
	n.ID = int32(len(f.notifications) + 1)
	f.notifications[n.ID] = n
	f.appliedToAll = applyToAll
	return n, nil
}
func (f *fakeStore) UpdateNotification(_ context.Context, n store.Notification, _ bool) (store.Notification, error) {
	if _, ok := f.notifications[n.ID]; !ok {
		return n, store.ErrNotFound
	}
	f.notifications[n.ID] = n
	return n, nil
}
func (f *fakeStore) DeleteNotification(_ context.Context, id int32) error {
	if _, ok := f.notifications[id]; !ok {
		return store.ErrNotFound
	}
	delete(f.notifications, id)
	return nil
}
func (f *fakeStore) MonitorNotificationIDs(context.Context, int32) ([]int32, error) {
	return nil, store.ErrNotFound
}
func (f *fakeStore) SetMonitorNotifications(_ context.Context, _ int32, ids []int32) error {
	for _, id := range ids {
		if _, ok := f.notifications[id]; !ok {
			return fmt.Errorf("%w: notification %d doesn't exist", store.ErrInvalidNotification, id)
		}
	}
	return nil
}
func (f *fakeStore) LinkDefaultNotifications(_ context.Context, id int32) error {
	f.linkedDefaults = append(f.linkedDefaults, id)
	return nil
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

func (nopScheduler) Upsert(monitor.Monitor)                           {}
func (nopScheduler) Remove(context.Context, int32) error              { return nil }
func (nopScheduler) Push(context.Context, string, monitor.Push) error { return nil }

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

func TestRoutesMatchTheOpenAPISpec(t *testing.T) {
	s := New(&fakeStore{}, nopScheduler{}, NewHub(), testToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.Handler()
	var spec struct {
		Paths map[string]map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(openAPISpec, &spec); err != nil {
		t.Fatalf("invalid spec: %v", err)
	}
	documented := map[string]bool{}
	for path, operations := range spec.Paths {
		for method := range operations {
			documented[strings.ToUpper(method)+" "+path] = true
		}
	}
	for _, route := range s.routes {
		if !documented[route] {
			t.Errorf("%s is not in openapi.json", route)
		}
		delete(documented, route)
	}
	delete(documented, "GET /healthz")
	for route := range documented {
		t.Errorf("%s is documented but doesn't exist", route)
	}
}

func keyedServer(t *testing.T, scopes ...string) (*httptest.Server, string, *fakeStore) {
	t.Helper()
	plain, hash, _, err := generateKey()
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeStore{keys: map[string]store.APIKey{string(hash): {ID: 7, Name: "ci", Scopes: scopes}}}
	ts, _ := newTestServer(t, f)
	return ts, plain, f
}

func TestScopes(t *testing.T) {
	ts, key, _ := keyedServer(t, ScopeMonitorsRead)
	if res := do(t, "GET", ts.URL+"/api/v1/monitors", key, ""); res.StatusCode != http.StatusOK {
		t.Errorf("read with read scope: %d", res.StatusCode)
	}
	for _, r := range [][2]string{
		{"POST", "/api/v1/monitors"},
		{"DELETE", "/api/v1/monitors/1"},
		{"POST", "/api/v1/monitors/1/pause"},
		{"PATCH", "/api/v1/monitors/1"},
		{"GET", "/api/v1/stats"},
		{"POST", "/api/v1/beats"},
		{"GET", "/api/v1/api-keys"},
		{"POST", "/api/v1/api-keys"},
		{"DELETE", "/api/v1/api-keys/1"},
		{"GET", "/api/v1/notifications"},
		{"POST", "/api/v1/notifications"},
		{"POST", "/api/v1/notifications/1/test"},
		{"GET", "/api/v1/monitors/1/notifications"},
		{"PUT", "/api/v1/monitors/1/notifications"},
	} {
		if res := do(t, r[0], ts.URL+r[1], key, "{}"); res.StatusCode != http.StatusForbidden {
			t.Errorf("%s %s without scope: %d", r[0], r[1], res.StatusCode)
		}
	}
}

func TestDatabaseFailureDuringAuthIsNotUnauthorized(t *testing.T) {
	f := &fakeStore{findKeyErr: errors.New("db down")}
	ts, _ := newTestServer(t, f)
	if res := do(t, "GET", ts.URL+"/api/v1/monitors", "kuma_abc", ""); res.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("got %d", res.StatusCode)
	}
	if res := do(t, "GET", ts.URL+"/api/v1/monitors", "not-a-kuma-key", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("malformed key: %d", res.StatusCode)
	}
}

func TestRateLimitPerKey(t *testing.T) {
	plain, hash, _, _ := generateKey()
	other, otherHash, _, _ := generateKey()
	f := &fakeStore{keys: map[string]store.APIKey{
		string(hash):      {ID: 1, Scopes: []string{ScopeMonitorsRead}},
		string(otherHash): {ID: 2, Scopes: []string{ScopeMonitorsRead}},
	}}
	hub := NewHub()
	s := New(f, nopScheduler{}, hub, testToken, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s.SetKeyRateLimit(0.001, 2)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(func() { hub.Close(); ts.Close() })

	url := ts.URL + "/api/v1/monitors"
	for i := 0; i < 2; i++ {
		if res := do(t, "GET", url, plain, ""); res.StatusCode != http.StatusOK {
			t.Fatalf("request %d: %d", i, res.StatusCode)
		}
	}
	res := do(t, "GET", url, plain, "")
	if res.StatusCode != http.StatusTooManyRequests || res.Header.Get("Retry-After") == "" {
		t.Errorf("over the limit: %d, Retry-After %q", res.StatusCode, res.Header.Get("Retry-After"))
	}
	if res := do(t, "GET", url, other, ""); res.StatusCode != http.StatusOK {
		t.Errorf("another key shares the limit: %d", res.StatusCode)
	}
	for i := 0; i < 5; i++ {
		if res := do(t, "GET", url, testToken, ""); res.StatusCode != http.StatusOK {
			t.Errorf("the admin token is rate limited: %d", res.StatusCode)
		}
	}
}

func TestCreateKeyValidation(t *testing.T) {
	ts, _ := newTestServer(t, &fakeStore{})
	url := ts.URL + "/api/v1/api-keys"
	for name, body := range map[string]string{
		"no name":       `{"scopes":["monitors:read"]}`,
		"long name":     `{"name":"` + strings.Repeat("x", 101) + `","scopes":["monitors:read"]}`,
		"no scopes":     `{"name":"a"}`,
		"unknown scope": `{"name":"a","scopes":["admin"]}`,
		"expired":       `{"name":"a","scopes":["monitors:read"],"expiresAt":"2000-01-01T00:00:00Z"}`,
		"unknown field": `{"name":"a","scopes":["monitors:read"],"x":1}`,
	} {
		if res := do(t, "POST", url, testToken, body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	res := do(t, "POST", url, testToken, `{"name":"a","scopes":["monitors:read"]}`)
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != http.StatusCreated || !strings.Contains(string(data), `"key":"kuma_`) {
		t.Errorf("create: %d %s", res.StatusCode, data)
	}
}

func notificationBody(url string) string {
	return `{"name":"Ops","type":"webhook","config":{"webhookURL":"` + url + `","webhookContentType":"json"}}`
}

func TestNotificationConfigIsHiddenFromReadOnlyKeys(t *testing.T) {
	ts, key, f := keyedServer(t, ScopeNotificationsRead)
	f.notifications = map[int32]store.Notification{1: {ID: 1, Name: "Ops", Type: "webhook", Config: map[string]any{"webhookURL": "https://hooks.example/s3cret"}}}
	for _, path := range []string{"/api/v1/notifications", "/api/v1/notifications/1"} {
		res := do(t, "GET", ts.URL+path, key, "")
		body, _ := io.ReadAll(res.Body)
		if res.StatusCode != http.StatusOK || strings.Contains(string(body), "s3cret") || !strings.Contains(string(body), "webhookURL") {
			t.Errorf("%s: %d %s", path, res.StatusCode, body)
		}
	}
	res := do(t, "GET", ts.URL+"/api/v1/notifications/1", testToken, "")
	body, _ := io.ReadAll(res.Body)
	if !strings.Contains(string(body), "s3cret") {
		t.Errorf("the admin token should see config: %s", body)
	}
}

func TestNotificationValidation(t *testing.T) {
	ts, _ := newTestServer(t, &fakeStore{})
	for name, body := range map[string]string{
		"no name":      `{"type":"webhook","config":{}}`,
		"unknown type": `{"name":"x","type":"nope","config":{}}`,
		"no config":    `{"name":"x","type":"webhook"}`,
		"extra field":  `{"name":"x","type":"webhook","config":{},"nope":1}`,
		"not json":     `{`,
	} {
		if res := do(t, "POST", ts.URL+"/api/v1/notifications", testToken, body); res.StatusCode != http.StatusBadRequest {
			t.Errorf("%s: %d", name, res.StatusCode)
		}
	}
	for path, want := range map[string]int{"/api/v1/notifications/abc": 400, "/api/v1/notifications/0": 400, "/api/v1/notifications/9": 404} {
		if res := do(t, "GET", ts.URL+path, testToken, ""); res.StatusCode != want {
			t.Errorf("%s: %d", path, res.StatusCode)
		}
	}
	res := do(t, "GET", ts.URL+"/api/v1/notifications/providers", testToken, "")
	var names []string
	if err := json.NewDecoder(res.Body).Decode(&names); err != nil || !slices.Contains(names, "smtp") {
		t.Errorf("providers: %v %v", names, err)
	}
}

func TestNotificationLifecycleAndTestSend(t *testing.T) {
	f := &fakeStore{}
	ts, _ := newTestServer(t, f)
	var received map[string]any
	status := http.StatusOK
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(status)
	}))
	defer hook.Close()

	body := strings.TrimSuffix(notificationBody(hook.URL), "}") + `,"applyToAllMonitors":true}`
	res := do(t, "POST", ts.URL+"/api/v1/notifications", testToken, body)
	if res.StatusCode != http.StatusCreated || res.Header.Get("Location") != "/api/v1/notifications/1" || !f.appliedToAll {
		t.Fatalf("create: %d %v", res.StatusCode, f.appliedToAll)
	}
	if !f.notifications[1].Active {
		t.Error("a new notification should default to active")
	}
	res = do(t, "POST", ts.URL+"/api/v1/notifications/1/test", testToken, "")
	if res.StatusCode != http.StatusOK || received["msg"] != "Ops Testing" {
		t.Errorf("test: %d %v", res.StatusCode, received)
	}
	status = http.StatusForbidden
	res = do(t, "POST", ts.URL+"/api/v1/notifications/1/test", testToken, "")
	if res.StatusCode != http.StatusBadGateway {
		t.Errorf("a refused test: %d", res.StatusCode)
	}
	if res := do(t, "PUT", ts.URL+"/api/v1/notifications/1", testToken, notificationBody(hook.URL)); res.StatusCode != http.StatusOK {
		t.Errorf("update: %d", res.StatusCode)
	}
	if res := do(t, "PUT", ts.URL+"/api/v1/notifications/9", testToken, notificationBody(hook.URL)); res.StatusCode != http.StatusNotFound {
		t.Errorf("update missing: %d", res.StatusCode)
	}
	if res := do(t, "DELETE", ts.URL+"/api/v1/notifications/1", testToken, ""); res.StatusCode != http.StatusNoContent {
		t.Errorf("delete: %d", res.StatusCode)
	}
}

func TestNewMonitorGetsDefaultNotifications(t *testing.T) {
	f := &fakeStore{}
	ts, _ := newTestServer(t, f)
	res := do(t, "POST", ts.URL+"/api/v1/monitors", testToken, `{"name":"x","type":"http","url":"https://example.com"}`)
	if res.StatusCode != http.StatusCreated || len(f.linkedDefaults) != 1 {
		t.Fatalf("%d, linked %v", res.StatusCode, f.linkedDefaults)
	}
}

func TestMonitorNotificationsValidation(t *testing.T) {
	ts, _ := newTestServer(t, &fakeStore{})
	if res := do(t, "PUT", ts.URL+"/api/v1/monitors/1/notifications", testToken, `{"notificationIds":[99]}`); res.StatusCode != http.StatusBadRequest {
		t.Errorf("unknown notification: %d", res.StatusCode)
	}
	if res := do(t, "PUT", ts.URL+"/api/v1/monitors/1/notifications", testToken, `{"nope":1}`); res.StatusCode != http.StatusBadRequest {
		t.Errorf("extra field: %d", res.StatusCode)
	}
	if res := do(t, "GET", ts.URL+"/api/v1/monitors/1/notifications", testToken, ""); res.StatusCode != http.StatusNotFound {
		t.Errorf("unknown monitor: %d", res.StatusCode)
	}
}
