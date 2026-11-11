package ui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// fakeStore keeps accounts, sessions and monitors in memory. Other methods return nothing.
type fakeStore struct {
	mu sync.Mutex
	// loginChanged makes account saves at login refuse, as when the settings changed meanwhile.
	loginChanged bool
	loginConfig  *store.LoginConfig
	googleTokens map[int32]string
	oidcTokens   map[int32]string
	users        []store.User
	sessions     map[string]store.Session
	monitors     map[int32]monitor.Monitor
	nextID       int32
	settings     map[string]json.RawMessage
	// createErr is returned by CreateMonitor, to test how a failure is shown.
	createErr error
	// important is what ImportantBeats returns, newest first, and chartRanges the ranges asked for.
	important   []store.Beat
	chartRanges [][2]time.Time

	windows   map[int32]maintenance.Window
	links     map[int32][]int32
	overrides []holidays.Override

	tags        []store.Tag
	monitorTags map[int32][]store.MonitorTag
	tlsInfo     map[int32]string

	dockerHosts    []check.DockerHost
	remoteBrowsers []check.RemoteBrowser
	screenshots    map[int32][]byte
	proxies        []check.Proxy

	totpSteps    map[int32]int64
	totpFailures map[int32]int
	challenges   map[string]store.TwoFactorChallenge
	apiKeys      []store.APIKey

	cleared []int32
	// notifications, and each monitor's, for the bulk edit. defaultLinked is who got the defaults.
	notifications        []store.Notification
	monitorNotifications map[int32][]int32
	defaultLinked        []int32
	githubTokens         map[int32]string
	retention            []int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		sessions: map[string]store.Session{}, monitors: map[int32]monitor.Monitor{}, settings: map[string]json.RawMessage{},
		totpSteps: map[int32]int64{}, totpFailures: map[int32]int{}, challenges: map[string]store.TwoFactorChallenge{},
		githubTokens: map[int32]string{},
		googleTokens: map[int32]string{},
		oidcTokens:   map[int32]string{},
	}
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
	u := store.User{ID: 1, Username: username, PasswordHash: hash, CreatedAt: time.Now(), Role: store.RoleAdmin}
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
	for _, u := range f.users {
		if u.ID == s.UserID {
			return s, u, nil
		}
	}
	return store.Session{}, store.User{}, store.ErrNotFound
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
	if f.createErr != nil {
		return m, f.createErr
	}
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

func (f *fakeStore) SetMonitorsActive(_ context.Context, ids []int32, active bool) ([]monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var changed []monitor.Monitor
	for _, id := range ids {
		if m, ok := f.monitors[id]; ok {
			m.Active = active
			f.monitors[id] = m
			changed = append(changed, m)
		}
	}
	return changed, nil
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
func (f *fakeStore) ImportantBeats(_ context.Context, _ *int32, filter store.EventFilter, offset, limit int) ([]store.Beat, error) {
	var out []store.Beat
	for _, b := range f.important {
		if (filter.Since == nil || !b.Time.Before(*filter.Since)) && (filter.Before == nil || b.Time.Before(*filter.Before)) {
			if offset > 0 {
				offset--
			} else if len(out) < limit {
				out = append(out, b)
			}
		}
	}
	return out, nil
}
func (f *fakeStore) CountImportantBeats(context.Context, *int32, store.EventFilter) (int64, error) {
	return 0, nil
}
func (f *fakeStore) ClearEvents(context.Context, *int32) error { return nil }
func (f *fakeStore) Chart(context.Context, int32, int) ([]store.ChartPoint, error) {
	return nil, nil
}
func (f *fakeStore) ChartRange(_ context.Context, _ int32, from, to time.Time) ([]store.ChartPoint, error) {
	f.chartRanges = append(f.chartRanges, [2]time.Time{from, to})
	return []store.ChartPoint{{Time: from, Up: 1}}, nil
}
func (f *fakeStore) Stats(context.Context) ([]store.MonitorStats, error) { return nil, nil }
func (f *fakeStore) ListNotifications(context.Context) ([]store.Notification, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.notifications), nil
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
func (f *fakeStore) DeleteNotification(context.Context, int32) error { return nil }
func (f *fakeStore) SetMonitorNotifications(_ context.Context, monitorID int32, ids []int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.monitorNotifications == nil {
		f.monitorNotifications = map[int32][]int32{}
	}
	f.monitorNotifications[monitorID] = slices.Clone(ids)
	return nil
}
func (f *fakeStore) AllMonitorNotificationIDs(context.Context) (map[int32][]int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int32][]int32{}
	for id, list := range f.monitorNotifications {
		out[id] = slices.Clone(list)
	}
	return out, nil
}
func (f *fakeStore) LinkDefaultNotifications(_ context.Context, monitorID int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.defaultLinked = append(f.defaultLinked, monitorID)
	return nil
}

func (f *fakeStore) ListTags(context.Context) ([]store.Tag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.tags), nil
}

func (f *fakeStore) CreateTag(_ context.Context, name, color string) (store.Tag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	tag := store.Tag{ID: int32(len(f.tags) + 1), Name: name, Color: color}
	f.tags = append(f.tags, tag)
	return tag, nil
}

func (f *fakeStore) UpdateTag(_ context.Context, tag store.Tag) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.tags {
		if f.tags[i].ID == tag.ID {
			f.tags[i] = tag
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) DeleteTag(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	before := len(f.tags)
	f.tags = slices.DeleteFunc(f.tags, func(t store.Tag) bool { return t.ID == id })
	if len(f.tags) == before {
		return store.ErrNotFound
	}
	for monitorID, list := range f.monitorTags {
		f.monitorTags[monitorID] = slices.DeleteFunc(list, func(t store.MonitorTag) bool { return t.TagID == id })
	}
	return nil
}

func (f *fakeStore) AddMonitorTag(_ context.Context, tagID, monitorID int32, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, t := range f.tags {
		if t.ID == tagID {
			if f.monitorTags == nil {
				f.monitorTags = map[int32][]store.MonitorTag{}
			}
			f.monitorTags[monitorID] = append(f.monitorTags[monitorID],
				store.MonitorTag{TagID: tagID, MonitorID: monitorID, Value: value, Name: t.Name, Color: t.Color})
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) UpdateMonitorTag(_ context.Context, tagID, monitorID int32, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, t := range f.monitorTags[monitorID] {
		if t.TagID == tagID {
			f.monitorTags[monitorID][i].Value = value
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) DeleteMonitorTag(_ context.Context, tagID, monitorID int32, value string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.monitorTags[monitorID] = slices.DeleteFunc(f.monitorTags[monitorID], func(t store.MonitorTag) bool {
		return t.TagID == tagID && t.Value == value
	})
	return nil
}

func (f *fakeStore) AllMonitorTags(context.Context) (map[int32][]store.MonitorTag, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := map[int32][]store.MonitorTag{}
	for id, list := range f.monitorTags {
		out[id] = slices.Clone(list)
	}
	return out, nil
}

func (f *fakeStore) AllTLSInfo(context.Context) (map[int32]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.tlsInfo), nil
}

func (f *fakeStore) ListMaintenances(context.Context) ([]maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []maintenance.Window
	for _, w := range f.windows {
		out = append(out, w)
	}
	return out, nil
}

func (f *fakeStore) GetMaintenance(_ context.Context, id int32) (maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, found := f.windows[id]
	if !found {
		return w, store.ErrNotFound
	}
	return w, nil
}

func (f *fakeStore) CreateMaintenance(_ context.Context, w maintenance.Window) (maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.windows == nil {
		f.windows = map[int32]maintenance.Window{}
	}
	w.ID = int32(len(f.windows) + 1)
	f.windows[w.ID] = w
	return w, nil
}

func (f *fakeStore) UpdateMaintenance(_ context.Context, w maintenance.Window) (maintenance.Window, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, found := f.windows[w.ID]; !found {
		return w, store.ErrNotFound
	}
	f.windows[w.ID] = w
	return w, nil
}

func (f *fakeStore) SetMaintenanceActive(_ context.Context, id int32, active bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	w, found := f.windows[id]
	if !found {
		return store.ErrNotFound
	}
	w.Active = active
	f.windows[id] = w
	return nil
}

func (f *fakeStore) DeleteMaintenance(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.windows, id)
	return nil
}

func (f *fakeStore) SetMaintenanceMonitors(_ context.Context, id int32, monitors []int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.links == nil {
		f.links = map[int32][]int32{}
	}
	f.links[id] = monitors
	return nil
}

func (f *fakeStore) MaintenanceMonitorLinks(context.Context) (map[int32][]int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.links), nil
}

func (f *fakeStore) HolidayOverrides(context.Context) ([]holidays.Override, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.overrides), nil
}

func (f *fakeStore) SetHolidayOverride(_ context.Context, o holidays.Override) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides = append(f.overrides, o)
	return nil
}

func (f *fakeStore) DeleteHolidayOverride(_ context.Context, country, date string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.overrides = slices.DeleteFunc(f.overrides, func(o holidays.Override) bool {
		return o.Country == country && o.Date == date
	})
	return nil
}

type fakeScheduler struct {
	mu        sync.Mutex
	upserted  []monitor.Monitor
	removed   []int32
	suspended []int32
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

func (f *fakeScheduler) Suspend(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.suspended = append(f.suspended, id)
	return nil
}

func newTestServer(t *testing.T) (*Server, *fakeStore, *httptest.Server) {
	t.Helper()
	return newTestServerWith(t, nil)
}

func newTestServerWith(t *testing.T, configure func(*Options)) (*Server, *fakeStore, *httptest.Server) {
	t.Helper()
	fs := newFakeStore()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calendar, err := holidays.New()
	if err != nil {
		t.Fatal(err)
	}
	service := maintenance.NewService(fs, calendar, time.UTC, log)
	opts := Options{Version: "1.2.3", Maintenance: service}
	if configure != nil {
		configure(&opts)
	}
	s := New(fs, &fakeScheduler{}, notify.DefaultEnv(""), log, opts)
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
	if m.Headers["X-A"] != "1" || m.Headers["X-N"] != "2" || m.Headers["Authorization"] != "" ||
		m.AuthMethod != "basic" || m.BasicAuthUser != "u" || m.BasicAuthPass != "p" || !m.DomainExpiryNotification {
		t.Fatalf("headers: %v %+v", m.Headers, m.Settings)
	}
	if len(ids) != 2 || ids[0] != 1 || ids[1] != 3 {
		t.Fatalf("notifications: %v", ids)
	}
	if note != "" {
		t.Fatalf("note: %s", note)
	}

	for name, raw := range map[string]string{
		"type":       `{"type": "ping", "name": "x"}`,
		"ip family":  `{"type": "http", "name": "x", "url": "https://a.b", "ipFamily": "ipv5"}`,
		"conditions": `{"type": "dns", "name": "x", "hostname": "a.b", "dns_resolve_server": "1.1.1.1", "conditions": [{"type": "expression", "variable": "x", "operator": "equals", "value": "1"}]}`,
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
	if _, found := b["traceId"]; found {
		t.Fatalf("a beat without a trace has a traceId: %v", b)
	}
	failed := beatJSON(store.Beat{MonitorID: 3, Status: 0, TraceID: "4bf92f3577b34da6a3ce929d0e0e4736"})
	if failed["traceId"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("failed beat: %v", failed)
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

func TestCloseEndsStreams(t *testing.T) {
	s, _, ts := newTestServer(t)
	c := newClient(t)
	_, stream, done := openStream(t, c, ts.URL)
	defer done()
	s.Close()
	finished := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, stream.r)
		finished <- err
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the stream stayed open")
	}
}

func TestParseWindow(t *testing.T) {
	s, _, _ := newTestServer(t)
	w, err := s.parseWindow(json.RawMessage(`{
		"title": " Patch ", "strategy": "recurring-day-of-month", "active": 1, "intervalDay": "1",
		"dateRange": ["2026-01-01T00:00", null], "timeRange": [{"hours": 2, "minutes": 5}, {"hours": 3, "minutes": 0}],
		"weekdays": [], "daysOfMonth": [15, "lastDay1"], "cron": "30 3 * * *", "durationMinutes": 60,
		"timezoneOption": "Asia/Hong_Kong", "holidayMode": "exclude", "holidayCountries": ["hk", "CN", "HK"]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if w.Title != "Patch" || !w.Active || w.StartTime != "02:05" || w.EndTime != "03:00" || w.EndDate != "" ||
		!slices.Equal(w.DaysOfMonth, []int{15, -1}) || !slices.Equal(w.HolidayCountries, []string{"HK", "CN"}) {
		t.Fatalf("window: %+v", w)
	}
	out := s.windowJSON(w, time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC))
	if out["holidayMode"] != "exclude" || out["timezone"] != "Asia/Hong_Kong" || out["timezoneOffset"] != "+08:00" ||
		out["daysOfMonth"].([]any)[1] != "lastDay1" {
		t.Fatalf("json: %v", out)
	}
	// Slots are on the window's own clock.
	slots := out["timeslotList"].([]map[string]any)
	if len(slots) != 1 || !strings.HasSuffix(slots[0]["startDate"].(string), "T02:05:00") {
		t.Fatalf("slots: %v", slots)
	}

	// Holidays don't apply to windows without recurring days.
	w, err = s.parseWindow(json.RawMessage(`{"title": "m", "strategy": "manual", "holidayMode": "include", "holidayCountries": ["HK"]}`))
	if err != nil || w.HolidayMode != "" || w.HolidayCountries != nil {
		t.Fatalf("manual: %+v %v", w, err)
	}
	for name, raw := range map[string]string{
		"country": `{"title": "x", "strategy": "recurring-interval", "intervalDay": 1, "timeRange": [{"hours": 1}, {"hours": 2}], "holidayMode": "exclude", "holidayCountries": ["XX"]}`,
		"none":    `{"title": "x", "strategy": "recurring-interval", "intervalDay": 1, "timeRange": [{"hours": 1}, {"hours": 2}], "holidayMode": "exclude"}`,
		"day":     `{"title": "x", "strategy": "recurring-day-of-month", "timeRange": [{"hours": 1}, {"hours": 2}], "daysOfMonth": ["lastDay9"]}`,
		"cron":    `{"title": "x", "strategy": "cron", "cron": "30 3 * * *", "durationMinutes": 5, "holidayMode": "include", "holidayCountries": ["HK"]}`,
	} {
		if _, err := s.parseWindow(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMaintenanceEvents(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}

	group := monitor.New()
	group.Name, group.Type = "G", monitor.TypeGroup
	group, _ = fs.CreateMonitor(ctx, group)
	child := monitor.New()
	child.Name, child.Type, child.URL, child.ParentID = "C", monitor.TypeHTTP, "https://a.b", &group.ID
	child, _ = fs.CreateMonitor(ctx, child)

	r := call("addMaintenance", map[string]any{"title": "M", "strategy": "manual", "active": 1})
	if r["ok"] != true {
		t.Fatalf("add: %v", r)
	}
	id := r["maintenanceID"].(int32)
	if r := call("addMaintenanceStatusPage", id, []any{}); r["ok"] != true {
		t.Fatalf("no status pages: %v", r)
	}
	if r := call("addMaintenanceStatusPage", id, []any{map[string]any{"id": 1}}); r["ok"] != false {
		t.Fatalf("status pages: %v", r)
	}

	// A window on a group covers what's below it.
	if s.maintenance.UnderMaintenance(child.ID) {
		t.Fatal("covered before linking")
	}
	if r := call("addMonitorMaintenance", id, []any{map[string]any{"id": group.ID}}); r["ok"] != true {
		t.Fatalf("link: %v", r)
	}
	if !s.maintenance.UnderMaintenance(group.ID) || !s.maintenance.UnderMaintenance(child.ID) {
		t.Fatal("group and child should be under maintenance")
	}
	if r := call("getMonitorMaintenance", id); len(r["monitors"].([]map[string]any)) != 1 {
		t.Fatalf("monitors: %v", r)
	}
	if r := call("getMonitor", child.ID); r["monitor"].(map[string]any)["maintenance"] != true {
		t.Fatalf("flag: %v", r)
	}

	if r := call("pauseMaintenance", id); r["ok"] != true || s.maintenance.UnderMaintenance(child.ID) {
		t.Fatalf("pause: %v", r)
	}
	if r := call("resumeMaintenance", id); r["ok"] != true || !s.maintenance.UnderMaintenance(child.ID) {
		t.Fatalf("resume: %v", r)
	}
	if r := call("getMaintenance", 99); r["ok"] != false {
		t.Fatalf("missing: %v", r)
	}
	if r := call("deleteMaintenance", id); r["ok"] != true || s.maintenance.UnderMaintenance(child.ID) {
		t.Fatalf("delete: %v", r)
	}
}

func TestHolidayEvents(t *testing.T) {
	s, _, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}
	list := func() []holidays.Holiday {
		t.Helper()
		return call("getHolidays", "HK", 2026)["holidays"].([]holidays.Holiday)
	}
	find := func(date string) *holidays.Holiday {
		for _, h := range list() {
			if h.Date == date {
				return &h
			}
		}
		return nil
	}

	if r := call("getHolidayCountries"); len(r["countries"].([]holidays.Country)) < 200 {
		t.Fatalf("countries: %v", r["source"])
	}
	if r := call("getHolidays", "XX", 2026); r["ok"] != false {
		t.Fatalf("unknown country: %v", r)
	}
	if len(list()) != 17 {
		t.Fatalf("HK 2026: %d", len(list()))
	}

	if r := call("addCustomHoliday", "hk", "2026-04-08", "Office closed"); r["ok"] != true {
		t.Fatalf("add: %v", r)
	}
	if h := find("2026-04-08"); h == nil || h.Source != "custom" || h.Name != "Office closed" {
		t.Fatalf("custom: %+v", h)
	}
	if r := call("addCustomHoliday", "HK", "not a date", "x"); r["ok"] != false {
		t.Fatalf("bad date: %v", r)
	}
	if r := call("removeHoliday", "HK", "2026-04-08"); r["ok"] != true || find("2026-04-08") != nil {
		t.Fatalf("remove custom: %v", r)
	}

	// A bundled date is hidden, not lost, and can come back.
	if r := call("removeHoliday", "HK", "2026-04-03"); r["ok"] != true {
		t.Fatalf("remove bundled: %v", r)
	}
	if h := find("2026-04-03"); h == nil || !h.Removed {
		t.Fatalf("removed: %+v", h)
	}
	day := time.Date(2026, 4, 3, 0, 0, 0, 0, time.UTC)
	if s.maintenance.Calendar().IsHoliday(day, []string{"HK"}) {
		t.Fatal("removed date still counts")
	}
	if r := call("restoreHoliday", "HK", "2026-04-03"); r["ok"] != true || !s.maintenance.Calendar().IsHoliday(day, []string{"HK"}) {
		t.Fatalf("restore: %v", r)
	}
	if r := call("removeHoliday", "HK", "2026-04-09"); r["ok"] != false {
		t.Fatalf("remove what isn't there: %v", r)
	}
	if r := call("getUpcomingHolidays", []string{"HK"}); r["ok"] != true {
		t.Fatalf("upcoming: %v", r)
	}
}

func TestTagEvents(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["test"] = c
	s.mu.Unlock()
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}
	pushed := func(event string) bool {
		found := false
		for {
			select {
			case frame := <-c.out:
				found = found || strings.Contains(string(frame), `["`+event+`"`)
			default:
				return found
			}
		}
	}
	m := monitor.New()
	m.Name, m.Type, m.URL = "Site", monitor.TypeHTTP, "https://a.b"
	m, _ = fs.CreateMonitor(ctx, m)

	if r := call("getTags"); len(r["tags"].([]store.Tag)) != 0 {
		t.Fatalf("no tags yet: %v", r)
	}
	r := call("addTag", map[string]any{"name": " prod ", "color": "#ff0000", "new": true})
	tag, isTag := r["tag"].(store.Tag)
	if r["ok"] != true || !isTag || tag.Name != "prod" {
		t.Fatalf("add: %v", r)
	}
	if r := call("addTag", map[string]any{"name": "", "color": "#ff0000"}); r["ok"] != false {
		t.Fatalf("empty name: %v", r)
	}

	if r := call("addMonitorTag", tag.ID, m.ID, nil); r["msg"] != "successAdded" {
		t.Fatalf("attach: %v", r)
	}
	if r := call("addMonitorTag", tag.ID, m.ID, "eu"); r["ok"] != true {
		t.Fatalf("attach with a value: %v", r)
	}
	if !pushed("updateMonitorIntoList") {
		t.Fatal("monitor update not pushed")
	}
	if r := call("addMonitorTag", 99, m.ID, ""); r["ok"] != false {
		t.Fatalf("missing tag: %v", r)
	}
	tags := call("getMonitor", m.ID)["monitor"].(map[string]any)["tags"].([]store.MonitorTag)
	if len(tags) != 2 || tags[0].TagID != tag.ID || tags[1].Value != "eu" || tags[0].Color != "#ff0000" {
		t.Fatalf("monitor tags: %+v", tags)
	}
	data, _ := json.Marshal(tags[1])
	for _, field := range []string{`"tag_id"`, `"monitor_id"`, `"value":"eu"`, `"name":"prod"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("%s missing from %s", field, data)
		}
	}

	// Renaming shows on the monitors that carry it.
	pushed("")
	if r := call("editTag", map[string]any{"id": tag.ID, "name": "production", "color": "#00ff00"}); r["msg"] != "Saved." {
		t.Fatalf("edit: %v", r)
	}
	if !pushed("updateMonitorIntoList") {
		t.Fatal("rename not pushed")
	}
	if r := call("editTag", map[string]any{"id": 99, "name": "x", "color": "y"}); r["msg"] != "tagNotFound" {
		t.Fatalf("edit missing: %v", r)
	}
	if r := call("deleteMonitorTag", tag.ID, m.ID, "eu"); r["ok"] != true {
		t.Fatalf("detach: %v", r)
	}
	if tags := call("getMonitor", m.ID)["monitor"].(map[string]any)["tags"].([]store.MonitorTag); len(tags) != 1 {
		t.Fatalf("after detach: %+v", tags)
	}
	pushed("")
	if r := call("deleteTag", tag.ID); r["ok"] != true {
		t.Fatalf("delete: %v", r)
	}
	if !pushed("updateMonitorIntoList") {
		t.Fatal("delete not pushed")
	}
	if tags := call("getMonitor", m.ID)["monitor"].(map[string]any)["tags"].([]store.MonitorTag); len(tags) != 0 {
		t.Fatalf("after delete: %+v", tags)
	}
}

func TestExpiryNotificationField(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "http", "name": "x", "url": "https://a.b", "expiryNotification": true}`))
	if err != nil || !m.ExpiryNotification {
		t.Fatalf("on: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "http", "name": "x", "url": "https://a.b", "expiryNotification": true, "ignoreTls": true}`))
	if err != nil || m.ExpiryNotification {
		t.Fatalf("with ignoreTls: %+v %v", m, err)
	}
	if out := newMonitorTree(nil, nil).monitorJSON(monitor.Monitor{ExpiryNotification: true}); out["expiryNotification"] != true {
		t.Fatalf("json: %v", out["expiryNotification"])
	}
}

func TestParsePushPingDNSMonitor(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{
		"type": "ping", "name": " Router ", "hostname": " 10.0.0.1 ",
		"ping_count": "4", "ping_per_request_timeout": 3, "packetSize": 64, "timeout": 30
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Type != monitor.TypePing || m.Hostname != "10.0.0.1" || m.Name != "Router" ||
		m.PingCount != 4 || m.PingPerRequestTimeout != 3 || m.PacketSize != 64 || m.Timeout != 30 {
		t.Fatalf("ping: %+v", m)
	}

	m, _, _, err = parseMonitor(json.RawMessage(`{
		"type": "dns", "name": "records", "hostname": " example.com ", "port": 5353,
		"dns_resolve_type": "AAAA", "dns_resolve_server": " 8.8.8.8 "
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Hostname != "example.com" || m.Port != 5353 || m.DNSResolveType != "AAAA" || m.DNSResolveServer != "8.8.8.8" {
		t.Fatalf("dns: %+v", m)
	}
	// The form sends nothing for a monitor that has just been created.
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "dns", "name": "records", "hostname": "example.com"}`))
	if err != nil {
		t.Fatal(err)
	}
	if m.Port != 53 || m.DNSResolveType != "A" || m.DNSResolveServer != "1.1.1.1" || m.DNSLastResult != "" {
		t.Fatalf("dns defaults: %+v", m)
	}

	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "push", "name": "sender", "pushToken": " abc12345 "}`))
	if err != nil || m.PushToken != "abc12345" {
		t.Fatalf("push: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "push", "name": "sender"}`))
	if err != nil || m.PushToken != "" {
		t.Fatalf("push without a token: %+v %v", m, err)
	}

	for name, raw := range map[string]string{
		"ping count":         `{"type": "ping", "name": "x", "hostname": "h", "ping_count": 0}`,
		"ping packet size":   `{"type": "ping", "name": "x", "hostname": "h", "packetSize": 65501}`,
		"ping deadline":      `{"type": "ping", "name": "x", "hostname": "h", "ping_per_request_timeout": 30, "timeout": 10}`,
		"ping hostname":      `{"type": "ping", "name": "x", "hostname": " "}`,
		"dns record type":    `{"type": "dns", "name": "x", "hostname": "example.com", "dns_resolve_type": "BOGUS"}`,
		"dns PTR of a name":  `{"type": "dns", "name": "x", "hostname": "example.com", "dns_resolve_type": "PTR"}`,
		"dns servers":        `{"type": "dns", "name": "x", "hostname": "example.com", "dns_resolve_server": " , "}`,
		"push token":         `{"type": "push", "name": "x", "pushToken": "not a token"}`,
		"a type still to do": `{"type": "docker", "name": "x"}`,
	} {
		if _, _, _, err := parseMonitor(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestMonitorJSONShowsPushPingDNSSettings(t *testing.T) {
	m := monitor.New()
	m.ID, m.Name, m.Type = 7, "sender", monitor.TypePush
	m.PushToken, m.PingCount, m.PingPerRequestTimeout, m.PacketSize = "tok", 4, 3, 64
	m.DNSResolveType, m.DNSResolveServer, m.DNSLastResult = "AAAA", "8.8.8.8", "Records: 2001:db8::1"

	out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m)
	for key, want := range map[string]any{
		"pushToken":                "tok",
		"ping_count":               int32(4),
		"ping_per_request_timeout": int32(3),
		"packetSize":               int32(64),
		"dns_resolve_type":         "AAAA",
		"dns_resolve_server":       "8.8.8.8",
		"dns_last_result":          "Records: 2001:db8::1",
	} {
		if got := out[key]; got != want {
			t.Errorf("%s: got %v, want %v", key, got, want)
		}
	}
}

func TestPushTokenConflictIsShown(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["test"] = c
	s.mu.Unlock()
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}

	// A token that another monitor already uses is the user's problem to solve, so it
	// is shown as it is rather than as an internal error.
	fs.createErr = store.ErrPushTokenTaken
	r := call("add", map[string]any{"type": "push", "name": "sender", "url": "https://"})
	if r["ok"] != false || r["msg"] != store.ErrPushTokenTaken.Error() {
		t.Fatalf("add: %v", r)
	}
}

func TestParsePhase4aMonitors(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "json-query", "name": "API", "url": "https://a.b/health",
		"jsonPath": "data.status", "jsonPathOperator": ">=", "expectedValue": 3, "accepted_statuscodes": ["200-299"],
		"conditions": [{"type": "expression", "variable": "record", "operator": "equals", "value": "x"}]}`))
	if err != nil || m.JSONPath != "data.status" || m.JSONPathOperator != ">=" || m.ExpectedValue != "3" || string(m.Conditions) != "[]" {
		t.Fatalf("json-query: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "mysql", "name": "DB", "databaseConnectionString": " mysql://u@db/x ",
		"databaseQuery": "SELECT 1", "radiusPassword": "pw",
		"conditions": [{"type": "expression", "andOr": "and", "variable": "result", "operator": "equals", "value": "1"}]}`))
	if err != nil || m.DatabaseConnectionString != "mysql://u@db/x" || m.DatabasePassword != "pw" || !strings.Contains(string(m.Conditions), "result") {
		t.Fatalf("mysql: %+v %v", m, err)
	}
	m, _, note, err := parseMonitor(json.RawMessage(`{"type": "websocket-upgrade", "name": "WS", "url": "wss://a.b/ws",
		"wsSubprotocol": "chat", "wsIgnoreSecWebsocketAcceptHeader": true, "accepted_statuscodes": ["1000"],
		"authMethod": "bearer", "bearer_token": "t"}`))
	if err != nil || m.WSSubprotocol != "chat" || !m.WSIgnoreAcceptHeader || m.AuthMethod != "bearer" || m.BearerToken != "t" || note != "" {
		t.Fatalf("websocket: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "manual", "name": "M", "manual_status": 0}`))
	if err != nil || m.ManualStatus == nil || *m.ManualStatus != 0 {
		t.Fatalf("manual: %+v %v", m, err)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "redis", "name": "R", "databaseConnectionString": "http://x"}`)); err == nil {
		t.Fatal("redis with an http URL was accepted")
	}

	tree := newMonitorTree([]monitor.Monitor{m}, nil)
	out := tree.monitorJSON(m)
	if out["manual_status"] == nil || out["conditions"] == nil {
		t.Fatalf("monitor JSON: %v", out)
	}
	types := monitorTypeList()
	dns := types[monitor.TypeDNS].(map[string]any)
	if dns["supportsConditions"] != true || types[monitor.TypeHTTP].(map[string]any)["supportsConditions"] != false {
		t.Fatalf("monitorTypeList: %v", types)
	}
}

func TestParsePhase4bMonitors(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "oracledb", "name": "O", "databaseConnectionString": "db:1521/X",
		"basic_auth_user": " kuma ", "basic_auth_pass": "pw", "databaseQuery": "SELECT 1 FROM DUAL"}`))
	if err != nil || m.DatabaseUsername != "kuma" || m.DatabasePassword != "pw" {
		t.Fatalf("oracle: %+v %v", m, err)
	}
	out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m)
	if out["basic_auth_user"] != "kuma" || out["basic_auth_pass"] != "pw" {
		t.Fatalf("oracle JSON: %v", out)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "mqtt", "name": "Q", "hostname": "broker", "port": "1883",
		"mqttTopic": "a/#", "mqttCheckType": "json-query", "jsonPath": "temp", "expectedValue": "1", "mqttPassword": "s"}`))
	if err != nil || m.MQTTTopic != "a/#" || m.MQTTCheckType != "json-query" || m.JSONPath != "temp" || m.MQTTPassword != "s" {
		t.Fatalf("mqtt: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "rabbitmq", "name": "R", "rabbitmqNodes": ["https://mq:15672", " "],
		"rabbitmqUsername": "guest", "rabbitmqPassword": "guest"}`))
	if err != nil || len(m.RabbitMQNodes) != 1 || m.RabbitMQUsername != "guest" {
		t.Fatalf("rabbitmq: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "kafka-producer", "name": "K", "kafkaProducerBrokers": ["k:9092"],
		"kafkaProducerTopic": "t", "kafkaProducerMessage": "hi", "kafkaProducerSsl": true,
		"kafkaProducerSaslOptions": {"mechanism": "None"}}`))
	if err != nil || m.KafkaSASL != nil || !m.KafkaSSL || m.KafkaMessage != "hi" {
		t.Fatalf("kafka: %+v %v", m, err)
	}
	out = newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m)
	if sasl := out["kafkaProducerSaslOptions"].(map[string]any); sasl["mechanism"] != "None" {
		t.Fatalf("kafka JSON: %v", out)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "kafka-producer", "name": "K", "kafkaProducerBrokers": ["k:9092"],
		"kafkaProducerTopic": "t", "kafkaProducerSaslOptions": {"mechanism": "aws"}}`)); err == nil {
		t.Fatal("AWS IAM was accepted")
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "mongodb", "name": "M", "databaseConnectionString": "mongodb://db",
		"databaseQuery": "{\"ping\": 1}", "jsonPath": "ok", "expectedValue": "1"}`))
	if err != nil || m.JSONPath != "ok" || m.ExpectedValue != "1" {
		t.Fatalf("mongodb: %+v %v", m, err)
	}
	if !monitorTypeList()[monitor.TypeMQTT].(map[string]any)["supportsConditions"].(bool) {
		t.Fatal("MQTT conditions")
	}
}

func TestParsePhase4cMonitors(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "snmp", "name": "S", "hostname": "sw1", "port": 161,
		"snmpOid": "1.3.6.1.2.1.1.3.0", "snmpVersion": "2c", "radiusPassword": "public", "jsonPathOperator": ">", "expectedValue": 0}`))
	if err != nil || m.SNMPCommunity != "public" || m.ExpectedValue != "0" || m.JSONPathOperator != ">" {
		t.Fatalf("snmp: %+v %v", m, err)
	}
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["radiusPassword"] != "public" || out["snmpVersion"] != "2c" {
		t.Fatalf("snmp JSON: %v", out)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "radius", "name": "R", "hostname": "radius", "port": 1812,
		"radiusUsername": "u", "radiusPassword": "p", "radiusSecret": "s", "radiusCalledStationId": "AP"}`))
	if err != nil || m.RadiusPassword != "p" || m.RadiusSecret != "s" || m.RadiusCalledStationID != "AP" || m.DatabasePassword != "" {
		t.Fatalf("radius: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "ntp", "name": "N", "hostname": "pool.ntp.org", "port": 123,
		"ntpStratumThreshold": 4, "ntpTimeOffsetThreshold": 250, "ntpRootDispersionThreshold": "100"}`))
	if err != nil || m.NTPStratumThreshold != 4 || m.NTPTimeOffsetThreshold != 250 || m.NTPRootDispersionThreshold != 100 {
		t.Fatalf("ntp: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "smtp", "name": "M", "hostname": "mail", "port": 587, "smtpSecurity": "starttls"}`))
	if err != nil || m.SMTPSecurity != "starttls" {
		t.Fatalf("smtp: %+v %v", m, err)
	}
}

func (f *fakeStore) ListDockerHosts(context.Context) ([]check.DockerHost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.dockerHosts), nil
}

func (f *fakeStore) SaveDockerHost(_ context.Context, h check.DockerHost) (check.DockerHost, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if h.ID == 0 {
		h.ID = int32(len(f.dockerHosts) + 1)
		f.dockerHosts = append(f.dockerHosts, h)
		return h, nil
	}
	for i := range f.dockerHosts {
		if f.dockerHosts[i].ID == h.ID {
			f.dockerHosts[i] = h
			return h, nil
		}
	}
	return h, store.ErrNotFound
}

func (f *fakeStore) DeleteDockerHost(_ context.Context, id int32) ([]int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, h := range f.dockerHosts {
		if h.ID == id {
			f.dockerHosts = slices.Delete(f.dockerHosts, i, i+1)
			var changed []int32
			for mid, m := range f.monitors {
				if m.DockerHostID == id {
					m.DockerHostID = 0
					f.monitors[mid] = m
					changed = append(changed, mid)
				}
			}
			return changed, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) ListRemoteBrowsers(context.Context) ([]check.RemoteBrowser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.remoteBrowsers), nil
}

func (f *fakeStore) SaveRemoteBrowser(_ context.Context, b check.RemoteBrowser) (check.RemoteBrowser, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b.ID == 0 {
		b.ID = int32(len(f.remoteBrowsers) + 1)
	}
	f.remoteBrowsers = append(f.remoteBrowsers, b)
	return b, nil
}

func (f *fakeStore) DeleteRemoteBrowser(_ context.Context, id int32) ([]int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, b := range f.remoteBrowsers {
		if b.ID == id {
			f.remoteBrowsers = slices.Delete(f.remoteBrowsers, i, i+1)
			return nil, nil
		}
	}
	return nil, store.ErrNotFound
}

func (f *fakeStore) Screenshot(_ context.Context, id int32) ([]byte, time.Time, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if png, found := f.screenshots[id]; found {
		return png, time.Unix(0, 0), nil
	}
	return nil, time.Time{}, store.ErrNotFound
}

func TestParsePhase4dMonitors(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "docker", "name": "D", "docker_container": " web ", "docker_host": "3"}`))
	if err != nil || m.DockerContainer != "web" || m.DockerHostID != 3 {
		t.Fatalf("docker: %+v %v", m, err)
	}
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["docker_host"] != int32(3) || out["docker_container"] != "web" {
		t.Fatalf("docker JSON: %v", out)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "docker", "name": "D", "docker_container": "web", "docker_host": null}`)); err == nil {
		t.Fatal("a docker monitor without a host was accepted")
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "system-service", "name": "S", "config": {"systemServiceName": " nginx "}}`))
	if err != nil || m.SystemServiceName != "nginx" {
		t.Fatalf("system service: %+v %v", m, err)
	}
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["config"].(map[string]any)["systemServiceName"] != "nginx" {
		t.Fatalf("system service JSON: %v", out)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "system-service", "name": "S", "config": {"systemServiceName": "nginx; rm -rf"}}`)); err == nil {
		t.Fatal("an invalid service name was accepted")
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "pm2", "name": "P", "config": {"systemServiceName": "api worker"}}`)); err != nil {
		t.Fatalf("pm2: %v", err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "sftp", "name": "F", "hostname": "files", "port": null,
		"sshAuthMethod": "privateKey", "sshUsername": "kuma", "sshPassword": "left over", "sshPrivateKey": "KEY", "sftpPath": "/upload"}`))
	if err != nil || m.Port != 22 || m.SSHPrivateKey != "KEY" || m.SSHPassword != "" || m.SFTPPath != "/upload" {
		t.Fatalf("sftp: %+v %v", m, err)
	}
	if redacted := m.Redacted(); redacted.SSHPrivateKey != "***" {
		t.Fatalf("sftp redacted: %+v", redacted)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "grpc-keyword", "name": "G", "grpcUrl": "api:443", "grpcProtobuf": "syntax = \"proto3\";",
		"grpcServiceName": "Health", "grpcMethod": "check", "grpcBody": "{}", "grpcEnableTls": true, "keyword": "SERVING"}`))
	if err != nil || m.GRPCURL != "api:443" || !m.GRPCEnableTLS || m.Keyword != "SERVING" {
		t.Fatalf("grpc: %+v %v", m, err)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "grpc-keyword", "name": "G", "grpcUrl": "api", "grpcProtobuf": "x",
		"grpcServiceName": "Health", "grpcMethod": "check", "keyword": "x"}`)); err == nil {
		t.Fatal("a gRPC URL without a port was accepted")
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "real-browser", "name": "B", "url": "https://example.com", "interval": 60,
		"remote_browser": 2, "screenshot_delay": "500"}`))
	if err != nil || m.RemoteBrowserID != 2 || m.ScreenshotDelay != 500 {
		t.Fatalf("real browser: %+v %v", m, err)
	}
	m.ID = 9
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["screenshot"] != "/screenshots/9.png" || out["remote_browser"] != int32(2) {
		t.Fatalf("real browser JSON: %v", out)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "real-browser", "name": "B", "url": "https://example.com", "interval": 20,
		"screenshot_delay": 10000}`)); err == nil {
		t.Fatal("a screenshot delay over half the interval was accepted")
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "real-browser", "name": "B", "url": "file:///etc/passwd"}`)); err == nil {
		t.Fatal("a file URL was accepted")
	}
}

func TestResourceEvents(t *testing.T) {
	s, fs, ts := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["test"] = c
	s.mu.Unlock()
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}

	r := call("addDockerHost", map[string]any{"name": "local", "dockerType": "socket", "dockerDaemon": "/var/run/docker.sock"}, nil)
	if r["ok"] != true || r["id"] != int32(1) {
		t.Fatalf("add docker host: %v", r)
	}
	if r := call("addDockerHost", map[string]any{"name": "bad", "dockerType": "tcp", "dockerDaemon": "docker"}, nil); r["ok"] != false {
		t.Fatalf("invalid docker host: %v", r)
	}
	r = call("addDockerHost", map[string]any{"name": "renamed", "dockerType": "socket", "dockerDaemon": "/run/docker.sock"}, 1)
	if r["ok"] != true || fs.dockerHosts[0].Name != "renamed" || len(fs.dockerHosts) != 1 {
		t.Fatalf("edit docker host: %v %+v", r, fs.dockerHosts)
	}
	m := monitor.New()
	m.Name, m.Type, m.DockerHostID, m.DockerContainer = "web", monitor.TypeDocker, 1, "web"
	m, _ = fs.CreateMonitor(ctx, m)

	saved := testDockerHost
	t.Cleanup(func() { testDockerHost = saved })
	testDockerHost = func(context.Context, check.DockerHost) (int, error) { return 4, nil }
	if r := call("testDockerHost", map[string]any{"name": "x", "dockerType": "socket", "dockerDaemon": "/x.sock"}); r["msg"] != "Connected Successfully. Amount of containers: 4" {
		t.Fatalf("test docker host: %v", r)
	}
	if r := call("deleteDockerHost", 1); r["ok"] != true || len(fs.dockerHosts) != 0 || fs.monitors[m.ID].DockerHostID != 0 {
		t.Fatalf("delete docker host: %v %+v", r, fs.monitors[m.ID])
	}

	if r := call("addRemoteBrowser", map[string]any{"name": "browserless", "url": "ws://browserless:3000?token=x"}, nil); r["ok"] != true {
		t.Fatalf("add remote browser: %v", r)
	}
	if r := call("addRemoteBrowser", map[string]any{"name": "bad", "url": "chrome:3000"}, nil); r["ok"] != false {
		t.Fatalf("invalid remote browser: %v", r)
	}
	savedChrome := testChrome
	t.Cleanup(func() { testChrome = savedChrome })
	testChrome = func(context.Context, string) (string, error) { return "HeadlessChrome/140", nil }
	r = call("testChrome", "/usr/bin/chromium")
	if msg, _ := r["msg"].(map[string]any); r["ok"] != true || msg["key"] != "foundChromiumVersion" {
		t.Fatalf("test chrome: %v", r)
	}
	savedPM2 := pm2ProcessList
	t.Cleanup(func() { pm2ProcessList = savedPM2 })
	pm2ProcessList = func(context.Context) ([]check.PM2Process, error) {
		return []check.PM2Process{{ID: "0", Name: "api", Status: "online"}}, nil
	}
	if r := call("getPM2ProcessList"); r["ok"] != true || len(r["processList"].([]check.PM2Process)) != 1 {
		t.Fatalf("pm2 list: %v", r)
	}

	// Screenshots are for logged-in users only.
	fs.screenshots = map[int32][]byte{5: []byte("\x89PNG")}
	res, err := http.Get(ts.URL + "/screenshots/5.png")
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anonymous screenshot: %d", res.StatusCode)
	}
}

func (f *fakeStore) ListProxies(context.Context) ([]check.Proxy, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.proxies), nil
}

func (f *fakeStore) SaveProxy(_ context.Context, p check.Proxy, applyExisting bool) (check.Proxy, []int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p.Default {
		for i := range f.proxies {
			f.proxies[i].Default = false
		}
	}
	if p.ID == 0 {
		p.ID = int32(len(f.proxies) + 1)
		f.proxies = append(f.proxies, p)
	} else {
		found := false
		for i := range f.proxies {
			if f.proxies[i].ID == p.ID {
				f.proxies[i], found = p, true
			}
		}
		if !found {
			return p, nil, store.ErrNotFound
		}
	}
	var changed []int32
	if applyExisting {
		for id, m := range f.monitors {
			if m.Type == monitor.TypeHTTP && m.ProxyID != p.ID {
				m.ProxyID = p.ID
				f.monitors[id] = m
				changed = append(changed, id)
			}
		}
	}
	return p, changed, nil
}

func (f *fakeStore) DeleteProxy(_ context.Context, id int32) ([]int32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.proxies {
		if p.ID == id {
			f.proxies = slices.Delete(f.proxies, i, i+1)
			var changed []int32
			for mid, m := range f.monitors {
				if m.ProxyID == id {
					m.ProxyID = 0
					f.monitors[mid] = m
					changed = append(changed, mid)
				}
			}
			return changed, nil
		}
	}
	return nil, store.ErrNotFound
}

func TestParsePhase4eOptions(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "json-query", "name": "J", "url": "https://example.com",
		"jsonPath": "status", "expectedValue": "ok", "description": " the API ", "cacheBust": true, "ipFamily": "ipv6",
		"retryOnlyOnStatusCodeFailure": true, "saveResponse": true, "saveErrorResponse": true, "responseMaxLength": "64",
		"authMethod": "oauth2-cc", "oauth_auth_method": "client_secret_post", "oauth_token_url": "https://id.example.com/t",
		"oauth_client_id": "c", "oauth_client_secret": "s", "oauth_scopes": "read", "oauth_audience": "api",
		"basic_auth_pass": "not kept", "proxyId": 3, "domainExpiryNotification": true}`))
	if err != nil || m.Description != "the API" || !m.CacheBust || m.IPFamily != "ipv6" || !m.RetryOnlyOnStatusCodeFailure ||
		!m.SaveResponse || m.ResponseMaxLength != 64 || m.OAuthClientSecret != "s" || m.BasicAuthPass != "" || m.ProxyID != 3 ||
		!m.DomainExpiryNotification {
		t.Fatalf("json query: %+v %v", m.Settings, err)
	}
	out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m)
	if out["authMethod"] != "oauth2-cc" || out["proxyId"] != int32(3) || out["ipFamily"] != "ipv6" || out["oauth_client_secret"] != "s" ||
		out["description"] != "the API" || out["responseMaxLength"] != int32(64) {
		t.Fatalf("json query JSON: %v", out)
	}
	if r := m.Redacted(); r.OAuthClientSecret != "***" {
		t.Fatalf("redacted: %+v", r.Settings)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "http", "name": "N", "url": "https://example.com", "authMethod": "ntlm",
		"basic_auth_user": "u", "basic_auth_pass": "p", "authDomain": "CORP", "authWorkstation": "WS"}`))
	if err != nil || m.AuthDomain != "CORP" || m.BasicAuthPass != "p" {
		t.Fatalf("ntlm: %+v %v", m.Settings, err)
	}
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["basic_auth_user"] != "u" || out["basic_auth_pass"] != "p" {
		t.Fatalf("ntlm JSON: %v", out)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "http", "name": "M", "url": "https://example.com", "authMethod": "mtls",
		"tlsCert": "not a certificate", "tlsKey": "x"}`)); err == nil {
		t.Fatal("an invalid client certificate was accepted")
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "websocket-upgrade", "name": "W", "url": "wss://a.b", "authMethod": "ntlm",
		"basic_auth_user": "u", "accepted_statuscodes": ["1000"]}`)); err == nil {
		t.Fatal("a WebSocket monitor with NTLM was accepted")
	}
	// A push monitor can't track a domain, whatever the form sends.
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "push", "name": "P", "domainExpiryNotification": true}`))
	if err != nil || m.DomainExpiryNotification {
		t.Fatalf("push: %+v %v", m.Settings, err)
	}
}

type fakeDomains struct{}

func (fakeDomains) Check(_ context.Context, kind, target string) (string, string, error) {
	return domains.Domain(kind, target)
}

func (fakeDomains) Expiry(context.Context, string, string) (*time.Time, error) {
	at := time.Now().Add(30 * 24 * time.Hour)
	return &at, nil
}

func TestProxyAndDomainEvents(t *testing.T) {
	s, fs, _ := newTestServer(t)
	s.opts.Domains = fakeDomains{}
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["test"] = c
	s.mu.Unlock()
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}
	m := monitor.New()
	m.Name, m.Type, m.URL = "Site", monitor.TypeHTTP, "https://a.b"
	m, _ = fs.CreateMonitor(ctx, m)

	r := call("addProxy", map[string]any{"protocol": "socks5", "host": " proxy ", "port": "1080", "auth": false,
		"username": "dropped", "active": true, "default": true, "applyExisting": true}, nil)
	if r["ok"] != true || fs.proxies[0].Host != "proxy" || fs.proxies[0].Port != 1080 || fs.proxies[0].Username != "" ||
		fs.monitors[m.ID].ProxyID != 1 {
		t.Fatalf("add proxy: %v %+v %+v", r, fs.proxies, fs.monitors[m.ID].Settings)
	}
	if r := call("addProxy", map[string]any{"protocol": "ftp", "host": "p", "port": 21}, nil); r["ok"] != false {
		t.Fatalf("an ftp proxy: %v", r)
	}
	if r := call("deleteProxy", 1); r["ok"] != true || fs.monitors[m.ID].ProxyID != 0 {
		t.Fatalf("delete proxy: %v", r)
	}

	if r := call("checkDomain", map[string]any{"type": "http", "url": "https://www.example.com/x"}); r["ok"] != true || r["domain"] != "example.com" {
		t.Fatalf("check domain: %v", r)
	}
	r = call("checkDomain", map[string]any{"type": "ping", "hostname": "10.0.0.1"})
	if r["ok"] != false || r["msgi18n"] != true || r["msg"] != "domain_expiry_unsupported_is_icann" {
		t.Fatalf("an IP address: %v", r)
	}

	// The dashboard gets the expiry of monitors that track their domain.
	m.DomainExpiryNotification = true
	fs.monitors[m.ID] = m
	for len(c.out) > 0 {
		<-c.out
	}
	s.sendDashboardLists(ctx, c)
	found := false
	for len(c.out) > 0 {
		found = found || strings.Contains(string(<-c.out), `"domainInfo"`)
	}
	if !found {
		t.Fatal("no domainInfo")
	}
}

func TestParsePhase4fMonitors(t *testing.T) {
	m, _, _, err := parseMonitor(json.RawMessage(`{"type": "gamedig", "name": "G", "hostname": "mc.example.com", "port": 25565,
		"game": "minecraft", "gamedigGivenPortOnly": false, "gamedigToken": "tok"}`))
	if err != nil || m.Game != "minecraft" || m.GamedigGivenPortOnly || m.GamedigToken != "tok" {
		t.Fatalf("gamedig: %+v %v", m, err)
	}
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["game"] != "minecraft" || out["gamedigGivenPortOnly"] != false {
		t.Fatalf("gamedig JSON: %v", out)
	}
	if _, _, _, err := parseMonitor(json.RawMessage(`{"type": "gamedig", "name": "G", "hostname": "h", "port": 1, "game": "solitaire"}`)); err == nil {
		t.Fatal("an unknown game was accepted")
	}
	if m, _, _, err := parseMonitor(json.RawMessage(`{"type": "tailscale-ping", "name": "T", "hostname": " cider "}`)); err != nil || m.Hostname != "cider" {
		t.Fatalf("tailscale: %+v %v", m, err)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "globalping", "name": "P", "subtype": "ping", "hostname": "example.com",
		"location": "Germany", "protocol": "TCP", "port": "443", "ping_count": 3, "ipFamily": "ipv6"}`))
	if err != nil || m.Subtype != "ping" || m.Protocol != "TCP" || m.Port != 443 || m.PingCount != 3 || m.IPFamily != "ipv6" {
		t.Fatalf("globalping ping: %+v %v", m, err)
	}
	if out := newMonitorTree([]monitor.Monitor{m}, nil).monitorJSON(m); out["subtype"] != "ping" || out["location"] != "Germany" || out["protocol"] != "TCP" {
		t.Fatalf("globalping JSON: %v", out)
	}
	m, _, _, err = parseMonitor(json.RawMessage(`{"type": "globalping", "name": "H", "subtype": "http", "url": "https://example.com",
		"method": "get", "location": "Europe", "protocol": null, "keyword": "Example", "accepted_statuscodes": ["200-299"],
		"authMethod": "bearer", "bearer_token": "t", "headers": "{\"X-A\": \"1\"}"}`))
	if err != nil || m.Method != "GET" || m.BearerToken != "t" || m.Headers["X-A"] != "1" || m.Keyword != "Example" {
		t.Fatalf("globalping http: %+v %v", m, err)
	}
	for name, raw := range map[string]string{
		"two locations": `{"type": "globalping", "name": "x", "subtype": "ping", "hostname": "a.b", "location": "DE,FR", "ping_count": 1}`,
		"bad dns type":  `{"type": "globalping", "name": "x", "subtype": "dns", "hostname": "a.b", "dns_resolve_type": "AXFR"}`,
		"bad subtype":   `{"type": "globalping", "name": "x", "subtype": "mtr", "hostname": "a.b"}`,
	} {
		if _, _, _, err := parseMonitor(json.RawMessage(raw)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	list := gameList()["gameList"].([]map[string]any)
	if len(list) < 250 || list[0]["keys"].([]string)[0] == "" || list[0]["options"].(map[string]any)["port"] == nil {
		t.Fatalf("game list: %d %v", len(list), list[0])
	}
}
