package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// fakeStore keeps accounts, sessions and monitors in memory. Other methods return nothing.
type fakeStore struct {
	mu       sync.Mutex
	users    []store.User
	sessions map[string]store.Session
	monitors map[int32]monitor.Monitor
	nextID   int32
	settings map[string]json.RawMessage
}

func newFakeStore() *fakeStore {
	return &fakeStore{sessions: map[string]store.Session{}, monitors: map[int32]monitor.Monitor{}, settings: map[string]json.RawMessage{}}
}

func (f *fakeStore) HasUser(context.Context) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.users) > 0, nil
}

func (f *fakeStore) CreateFirstUser(_ context.Context, username, hash string) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.users) > 0 {
		return store.User{}, store.ErrSetupDone
	}
	u := store.User{ID: 1, Username: username, PasswordHash: hash, CreatedAt: time.Now()}
	f.users = append(f.users, u)
	return u, nil
}

func (f *fakeStore) UserByUsername(_ context.Context, username string) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if strings.EqualFold(u.Username, username) {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) SetPassword(_ context.Context, id int32, hash string, keep []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].PasswordHash = hash
		}
	}
	for k := range f.sessions {
		if k != string(keep) {
			delete(f.sessions, k)
		}
	}
	return nil
}

func (f *fakeStore) CreateSession(_ context.Context, h []byte, s store.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s.CreatedAt = time.Now()
	f.sessions[string(h)] = s
	return nil
}

func (f *fakeStore) FindSession(_ context.Context, h []byte) (store.Session, store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[string(h)]
	if !ok || s.ExpiresAt.Before(time.Now()) {
		return store.Session{}, store.User{}, store.ErrNotFound
	}
	return s, f.users[0], nil
}

func (f *fakeStore) ExtendSession(_ context.Context, h []byte, t time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.sessions[string(h)]
	s.ExpiresAt = t
	f.sessions[string(h)] = s
	return nil
}

func (f *fakeStore) DeleteSession(_ context.Context, h []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, string(h))
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

func (f *fakeStore) ListMonitors(context.Context) ([]monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []monitor.Monitor
	for _, m := range f.monitors {
		out = append(out, m)
	}
	return out, nil
}

func (f *fakeStore) GetMonitor(_ context.Context, id int32) (monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.monitors[id]
	if !ok {
		return m, store.ErrNotFound
	}
	return m, nil
}

func (f *fakeStore) CreateMonitor(_ context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	m.ID = f.nextID
	f.monitors[m.ID] = m
	return m, nil
}

func (f *fakeStore) UpdateMonitor(_ context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.monitors[m.ID]; !ok {
		return m, store.ErrNotFound
	}
	f.monitors[m.ID] = m
	return m, nil
}

func (f *fakeStore) SetMonitorActive(_ context.Context, id int32, active bool) (monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	m, ok := f.monitors[id]
	if !ok {
		return m, store.ErrNotFound
	}
	m.Active = active
	f.monitors[id] = m
	return m, nil
}

func (f *fakeStore) DeleteMonitor(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.monitors, id)
	return nil
}

func (f *fakeStore) LatestBeats(context.Context, int32, int) ([]store.Beat, error) { return nil, nil }
func (f *fakeStore) LatestBeatPerMonitor(context.Context) ([]store.Beat, error)    { return nil, nil }
func (f *fakeStore) BeatsSince(context.Context, int32, time.Time, int) ([]store.Beat, error) {
	return nil, nil
}
func (f *fakeStore) ImportantBeats(context.Context, *int32, int, int) ([]store.Beat, error) {
	return nil, nil
}
func (f *fakeStore) CountImportantBeats(context.Context, *int32) (int64, error) { return 0, nil }
func (f *fakeStore) ClearEvents(context.Context, int32) error                   { return nil }
func (f *fakeStore) Chart(context.Context, int32, int) ([]store.ChartPoint, error) {
	return nil, nil
}
func (f *fakeStore) Stats(context.Context) ([]store.MonitorStats, error) { return nil, nil }
func (f *fakeStore) ListNotifications(context.Context) ([]store.Notification, error) {
	return nil, nil
}
func (f *fakeStore) GetNotification(context.Context, int32) (store.Notification, error) {
	return store.Notification{}, store.ErrNotFound
}
func (f *fakeStore) CreateNotification(_ context.Context, n store.Notification, _ bool) (store.Notification, error) {
	n.ID = 1
	return n, nil
}
func (f *fakeStore) UpdateNotification(_ context.Context, n store.Notification, _ bool) (store.Notification, error) {
	return n, nil
}
func (f *fakeStore) DeleteNotification(context.Context, int32) error               { return nil }
func (f *fakeStore) SetMonitorNotifications(context.Context, int32, []int32) error { return nil }
func (f *fakeStore) AllMonitorNotificationIDs(context.Context) (map[int32][]int32, error) {
	return map[int32][]int32{}, nil
}

type fakeScheduler struct {
	mu       sync.Mutex
	upserted []monitor.Monitor
	removed  []int32
}

func (f *fakeScheduler) Upsert(m monitor.Monitor) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserted = append(f.upserted, m)
}

func (f *fakeScheduler) Remove(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed = append(f.removed, id)
	return nil
}

func newTestServer(t *testing.T) (*Server, *fakeStore, *httptest.Server) {
	t.Helper()
	fs := newFakeStore()
	s := New(fs, &fakeScheduler{}, notify.DefaultEnv(""), slog.New(slog.NewTextHandler(io.Discard, nil)), Options{Version: "1.2.3"})
	mux := http.NewServeMux()
	s.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return s, fs, ts
}

func newClient(t *testing.T) *http.Client {
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Jar: jar, Timeout: 10 * time.Second}
}

func postJSON(t *testing.T, c *http.Client, url string, body any, headers ...string) (*http.Response, map[string]any) {
	t.Helper()
	data, _ := json.Marshal(body)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(string(data)))
	req.Header.Set("Content-Type", "application/json")
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res, out
}

func TestPasswordMatchesBetterAuth(t *testing.T) {
	// Made by @better-auth/utils hashPassword("correct horse 1").
	const fromBetterAuth = "d50810c13d25396d0ed1252bb90119fe:f40392fbe85d1c63f7af173035926beebc7b207bd84d98532b7fa68f583644361a9c5d3fccfefcc0712eae1d9a27a4d167b5cecc7fa2c54f722b944c9b10d37f"
	if !verifyPassword(fromBetterAuth, "correct horse 1") {
		t.Fatal("a better-auth hash didn't verify")
	}
	if verifyPassword(fromBetterAuth, "correct horse 2") {
		t.Fatal("the wrong password verified")
	}
	hash, err := hashPassword("ünïcödé pass")
	if err != nil {
		t.Fatal(err)
	}
	if !verifyPassword(hash, "ünïcödé pass") || verifyPassword(hash, "unicode pass") {
		t.Fatal("round trip failed")
	}
	for _, bad := range []string{"", "nocolon", "salt:nothex", "salt:abcd"} {
		if verifyPassword(bad, "x") {
			t.Fatalf("malformed hash %q verified", bad)
		}
	}
}

func TestSetupAndLogin(t *testing.T) {
	_, fs, ts := newTestServer(t)
	c := newClient(t)

	res, body := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "short"})
	if res.StatusCode != 400 || body["ok"] != false {
		t.Fatalf("short password: %d %v", res.StatusCode, body)
	}
	res, _ = postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"},
		"Origin", "https://evil.example")
	if res.StatusCode != 403 {
		t.Fatalf("cross-origin setup: %d", res.StatusCode)
	}
	res, body = postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	if res.StatusCode != 200 || body["ok"] != true {
		t.Fatalf("setup: %d %v", res.StatusCode, body)
	}
	res, body = postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "other", "password": "long enough"})
	if res.StatusCode != 400 || !strings.Contains(body["msg"].(string), "initialized") {
		t.Fatalf("second setup: %d %v", res.StatusCode, body)
	}

	res, body = postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "wrong one"})
	if res.StatusCode != 401 || body["code"] != "INVALID_USERNAME_OR_PASSWORD" {
		t.Fatalf("wrong password: %d %v", res.StatusCode, body)
	}
	res, body = postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "nobody", "password": "long enough"})
	if res.StatusCode != 401 {
		t.Fatalf("unknown user: %d %v", res.StatusCode, body)
	}
	res, body = postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "ADMIN", "password": "long enough", "rememberMe": false})
	if res.StatusCode != 200 || body["token"] != nil {
		t.Fatalf("sign in: %d %v", res.StatusCode, body)
	}
	var cookie *http.Cookie
	for _, ck := range res.Cookies() {
		if ck.Name == sessionCookie {
			cookie = ck
		}
	}
	if cookie == nil || !cookie.HttpOnly || cookie.SameSite != http.SameSiteLaxMode || cookie.MaxAge != 0 {
		t.Fatalf("cookie: %+v", cookie)
	}
	if len(fs.sessions) != 1 {
		t.Fatalf("sessions: %d", len(fs.sessions))
	}
	for k := range fs.sessions {
		if k == cookie.Value {
			t.Fatal("the raw token was stored")
		}
	}

	get, err := c.Get(ts.URL + "/api/auth/get-session")
	if err != nil {
		t.Fatal(err)
	}
	var session map[string]map[string]any
	_ = json.NewDecoder(get.Body).Decode(&session)
	get.Body.Close()
	if session["user"]["username"] != "admin" {
		t.Fatalf("get-session: %v", session)
	}

	res, body = postJSON(t, c, ts.URL+"/api/auth/sign-out", map[string]any{})
	if res.StatusCode != 200 || body["success"] != true || len(fs.sessions) != 0 {
		t.Fatalf("sign out: %d %v %d", res.StatusCode, body, len(fs.sessions))
	}
}

func TestSetupWindowExpires(t *testing.T) {
	s, _, ts := newTestServer(t)
	s.started = time.Now().Add(-11 * time.Minute)
	res, body := postJSON(t, newClient(t), ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	if res.StatusCode != 400 || !strings.Contains(body["msg"].(string), "expired") {
		t.Fatalf("expired setup: %d %v", res.StatusCode, body)
	}
	if need, _ := s.needSetup(context.Background()); need {
		t.Fatal("setup still offered after the window")
	}
}

func TestLoginRateLimit(t *testing.T) {
	_, _, ts := newTestServer(t)
	c := newClient(t)
	var last int
	for range 12 {
		res, _ := postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "wrong one"})
		last = res.StatusCode
	}
	if last != http.StatusTooManyRequests {
		t.Fatalf("12th attempt: %d", last)
	}
}

// sseReader reads frames from a realtime stream.
type sseReader struct {
	r *bufio.Reader
}

func (s *sseReader) next(t *testing.T) (string, json.RawMessage) {
	t.Helper()
	var event string
	var data string
	for {
		line, err := s.r.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "":
			if data != "" {
				return event, json.RawMessage(data)
			}
		case strings.HasPrefix(line, "event: "):
			event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data += line[6:]
		}
	}
}

// message reads until a "message" or "ack" frame and returns its decoded array.
func (s *sseReader) message(t *testing.T) (string, []json.RawMessage) {
	t.Helper()
	for {
		event, data := s.next(t)
		if event == "message" || event == "ack" {
			var parts []json.RawMessage
			if err := json.Unmarshal(data, &parts); err != nil {
				t.Fatal(err)
			}
			return event, parts
		}
	}
}

func openStream(t *testing.T, c *http.Client, url string) (string, *sseReader, func()) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url+"/api/realtime/stream", nil)
	// Without this, the Go client would ask for gzip and decode it for us; the test reads plain.
	req.Header.Set("Accept-Encoding", "identity")
	res, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := &sseReader{r: bufio.NewReader(res.Body)}
	event, data := reader.next(t)
	if event != "open" {
		t.Fatalf("first event %q", event)
	}
	var open struct{ ID string }
	_ = json.Unmarshal(data, &open)
	return open.ID, reader, func() { res.Body.Close() }
}

func sendEvent(t *testing.T, c *http.Client, url, id, event string, ack int, args ...any) {
	t.Helper()
	if args == nil {
		args = []any{}
	}
	res, _ := postJSON(t, c, url+"/api/realtime/send", map[string]any{
		"id": id, "messages": []any{map[string]any{"event": event, "args": args, "ack": ack}},
	})
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s: %d", event, res.StatusCode)
	}
}

func TestRealtimeAnonymous(t *testing.T) {
	_, _, ts := newTestServer(t)
	c := newClient(t)
	id, stream, done := openStream(t, c, ts.URL)
	defer done()

	var names []string
	for range 3 {
		_, parts := stream.message(t)
		var name string
		_ = json.Unmarshal(parts[0], &name)
		names = append(names, name)
	}
	if strings.Join(names, ",") != "info,setup,loginRequired" {
		t.Fatalf("events: %v", names)
	}

	sendEvent(t, c, ts.URL, id, "getMonitorList", 7)
	kind, parts := stream.message(t)
	if kind != "ack" || string(parts[0]) != "7" || !strings.Contains(string(parts[1]), "not logged in") {
		t.Fatalf("ack: %s %s", kind, parts)
	}

	res, _ := postJSON(t, c, ts.URL+"/api/realtime/send", map[string]any{"id": "missing", "messages": []any{}})
	if res.StatusCode != http.StatusGone {
		t.Fatalf("unknown stream: %d", res.StatusCode)
	}
	res, _ = postJSON(t, c, ts.URL+"/api/realtime/send", map[string]any{"id": id, "messages": []any{map[string]any{"event": ""}}})
	if res.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid message: %d", res.StatusCode)
	}
}

func TestRealtimeLoggedIn(t *testing.T) {
	_, fs, ts := newTestServer(t)
	c := newClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "long enough"})

	id, stream, done := openStream(t, c, ts.URL)
	defer done()
	seen := map[string]bool{}
	for !seen["statusPageList"] {
		_, parts := stream.message(t)
		var name string
		_ = json.Unmarshal(parts[0], &name)
		seen[name] = true
	}
	for _, want := range []string{"info", "session", "monitorList", "notificationList", "monitorTypeList"} {
		if !seen[want] {
			t.Fatalf("missing %s in %v", want, seen)
		}
	}
	if seen["loginRequired"] {
		t.Fatal("loginRequired sent to a logged-in stream")
	}

	sendEvent(t, c, ts.URL, id, "add", 1, map[string]any{
		"type": "http", "name": "Site", "url": "https://example.com", "interval": "30", "retryInterval": 30,
		"accepted_statuscodes": []string{"200-299"}, "notificationIDList": map[string]bool{},
	})
	for {
		kind, parts := stream.message(t)
		if kind != "ack" {
			continue
		}
		var r map[string]any
		_ = json.Unmarshal(parts[1], &r)
		if r["ok"] != true || r["msg"] != "successAdded" {
			t.Fatalf("add: %v", r)
		}
		break
	}
	if len(fs.monitors) != 1 || fs.monitors[1].Interval != 30 || fs.monitors[1].RetryInterval != 0 {
		t.Fatalf("stored: %+v", fs.monitors)
	}

	sendEvent(t, c, ts.URL, id, "addStatusPage", 2, "Title", "slug")
	for {
		kind, parts := stream.message(t)
		if kind == "ack" && string(parts[0]) == "2" {
			if !strings.Contains(string(parts[1]), "isn't supported") {
				t.Fatalf("unsupported: %s", parts[1])
			}
			break
		}
	}
	sendEvent(t, c, ts.URL, id, "getTags", 3)
	for {
		kind, parts := stream.message(t)
		if kind == "ack" && string(parts[0]) == "3" {
			if string(parts[1]) != `{"ok":true,"tags":[]}` {
				t.Fatalf("getTags: %s", parts[1])
			}
			break
		}
	}
}

func TestParseMonitor(t *testing.T) {
	m, ids, note, err := parseMonitor(json.RawMessage(`{
		"type": "keyword", "name": " Site ", "url": " https://example.com/x ", "method": "post",
		"interval": "45", "retryInterval": 45, "timeout": "36", "maxretries": 2, "parent": 4,
		"keyword": "ok", "invertKeyword": true, "headers": "{\"X-A\": \"1\", \"X-N\": 2}",
		"body": "{}", "httpBodyEncoding": "JSON", "accepted_statuscodes": ["200-299", "301"],
		"maxredirects": 3, "authMethod": "basic", "basic_auth_user": "u", "basic_auth_pass": "p",
		"proxyId": null, "conditions": [], "notificationIDList": {"3": true, "1": true, "2": false},
		"kafkaProducerSaslOptions": {"mechanism": "None"}, "domainExpiryNotification": true
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Site" || m.URL != "https://example.com/x" || m.Method != "POST" || m.Interval != 45 ||
		m.RetryInterval != 0 || m.Timeout != 36 || m.MaxRetries != 2 || *m.ParentID != 4 || m.Keyword != "ok" ||
		!m.InvertKeyword || m.BodyEncoding != "json" || m.MaxRedirects != 3 || len(m.AcceptedStatusCodes) != 2 {
		t.Fatalf("monitor: %+v", m)
	}
	if m.Headers["X-A"] != "1" || m.Headers["X-N"] != "2" || m.Headers["Authorization"] != "Basic dTpw" {
		t.Fatalf("headers: %v", m.Headers)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Fatalf("notifications: %v", ids)
	}
	if note == "" {
		t.Fatal("no note about the Authorization header")
	}

	for name, raw := range map[string]string{
		"type":       `{"type": "ping", "name": "x"}`,
		"proxy":      `{"type": "http", "name": "x", "url": "https://a.b", "proxyId": 1}`,
		"conditions": `{"type": "http", "name": "x", "url": "https://a.b", "conditions": [{"a": 1}]}`,
		"auth":       `{"type": "http", "name": "x", "url": "https://a.b", "authMethod": "ntlm"}`,
		"headers":    `{"type": "http", "name": "x", "url": "https://a.b", "headers": "not json"}`,
		"invalid":    `{"type": "http", "name": "", "url": "https://a.b"}`,
		"port":       `{"type": "port", "name": "x", "hostname": "h", "port": 0}`,
	} {
		if _, _, _, err := parseMonitor(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "port", "name": "DB", "hostname": " db ", "port": "5432", "url": "https://"}`))
	if err != nil || m.Hostname != "db" || m.Port != 5432 || m.URL != "" {
		t.Fatalf("port: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "group", "name": "G", "url": "https://", "active": false}`))
	if err != nil || m.Active {
		t.Fatalf("group: %+v %v", m, err)
	}
}

func TestMonitorJSON(t *testing.T) {
	group := monitor.New()
	group.ID, group.Name, group.Type, group.Active = 1, "Web", monitor.TypeGroup, false
	site := monitor.New()
	site.ID, site.Name, site.Type, site.URL, site.ParentID = 2, "Site", monitor.TypeHTTP, "https://a.b", &group.ID
	site.Headers = map[string]string{"X-A": "1"}
	tree := newMonitorTree([]monitor.Monitor{group, site}, map[int32][]int32{2: {5}})

	out := tree.monitorJSON(site)
	if out["pathName"] != "Web / Site" || out["forceInactive"] != true || out["parent"].(*int32) == nil {
		t.Fatalf("path: %v %v %v", out["pathName"], out["forceInactive"], out["parent"])
	}
	if out["headers"] != "{\n    \"X-A\": \"1\"\n}" {
		t.Fatalf("headers: %q", out["headers"])
	}
	if ids := out["notificationIDList"].(map[string]bool); !ids["5"] {
		t.Fatalf("notifications: %v", ids)
	}
	if out["kafkaProducerSaslOptions"] == nil || out["accepted_statuscodes"] == nil || out["tags"] == nil {
		t.Fatal("form defaults missing")
	}
	if children := tree.monitorJSON(group)["childrenIDs"].([]int32); len(children) != 1 || children[0] != 2 {
		t.Fatalf("children: %v", children)
	}
	// The defaults are shared, so building one monitor must not change another's.
	out["tags"] = "changed"
	if tree.monitorJSON(group)["tags"] == "changed" {
		t.Fatal("defaults were modified")
	}
	data, err := json.Marshal(tree.listJSON())
	if err != nil || !strings.Contains(string(data), `"2":{`) {
		t.Fatalf("list: %s %v", data, err)
	}
}

func TestChartOmitsZeroMaintenance(t *testing.T) {
	points := chartJSON([]store.ChartPoint{{Time: time.Unix(60, 0), Up: 1}, {Time: time.Unix(120, 0), Maintenance: 2}})
	if _, found := points[0]["maintenance"]; found {
		t.Fatal("zero maintenance sent")
	}
	if points[1]["maintenance"] != int64(2) || points[0]["timestamp"] != int64(60) {
		t.Fatalf("points: %v", points)
	}
}

func TestBeatJSON(t *testing.T) {
	ping := 12.5
	b := beatJSON(store.Beat{MonitorID: 3, Time: time.Date(2026, 1, 2, 3, 4, 5, 6_000_000, time.FixedZone("x", 3600)), Status: 1, Ping: &ping})
	if b["time"] != "2026-01-02 02:04:05.006" || b["monitorID"] != int32(3) || b["monitor_id"] != int32(3) {
		t.Fatalf("beat: %v", b)
	}
}

func TestAcceptsGzip(t *testing.T) {
	for header, want := range map[string]bool{
		"gzip, deflate, br": true, "br": false, "gzip;q=0": false, "gzip;q=0.5": true, "": false, "GZIP": true,
	} {
		if got := acceptsGzip(header); got != want {
			t.Errorf("%q: %v", header, got)
		}
	}
}

func TestParseNotification(t *testing.T) {
	n, applyAll, err := parseNotification(json.RawMessage(`{"name": " Hook ", "type": "webhook", "isDefault": true,
		"applyExisting": true, "webhookURL": "https://h", "id": 9, "active": false}`))
	if err != nil || n.Name != "Hook" || !n.IsDefault || !applyAll || n.Config["webhookURL"] != "https://h" {
		t.Fatalf("notification: %+v %v %v", n, applyAll, err)
	}
	for _, key := range []string{"applyExisting", "id", "isDefault", "active"} {
		if _, found := n.Config[key]; found {
			t.Errorf("config kept %s", key)
		}
	}
	if _, _, err := parseNotification(json.RawMessage(`{"name": "x", "type": "carrier-pigeon"}`)); err == nil {
		t.Fatal("unknown type accepted")
	}
	out := notificationJSON(n)
	var config map[string]any
	if err := json.Unmarshal([]byte(out["config"].(string)), &config); err != nil || config["webhookURL"] != "https://h" || config["isDefault"] != true {
		t.Fatalf("list item: %v %v", config, err)
	}
}

func TestUserErrorsAreShown(t *testing.T) {
	if !isUserError(userErr(errors.New("x"))) || isUserError(errors.New("x")) || userErr(nil) != nil {
		t.Fatal("userErr")
	}
}
