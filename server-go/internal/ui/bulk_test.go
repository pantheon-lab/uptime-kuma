package ui

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// caller sends realtime events as an admin, or as a viewer.
func caller(t *testing.T, s *Server, admin bool) func(event string, args ...any) reply {
	c := &conn{userID: 1, admin: admin, username: "someone", out: make(chan []byte, 64), done: make(chan struct{})}
	return func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(context.Background(), c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}
}

func addFakeMonitor(fs *fakeStore, name, typ string) int32 {
	m := monitor.New()
	m.Name, m.Type, m.URL = name, typ, "https://example.com/"+name
	created, _ := fs.CreateMonitor(context.Background(), m)
	return created.ID
}

func TestBulkEditMonitors(t *testing.T) {
	s, fs, _ := newTestServer(t)
	call := caller(t, s, true)
	a, b := addFakeMonitor(fs, "a", monitor.TypeHTTP), addFakeMonitor(fs, "b", monitor.TypeHTTP)
	fs.notifications = []store.Notification{{ID: 1, Name: "old"}, {ID: 2, Name: "new"}}
	fs.monitorNotifications = map[int32][]int32{a: {1}, b: {1, 2}}
	fs.tags = []store.Tag{{ID: 5, Name: "region"}, {ID: 6, Name: "legacy"}}
	fs.monitorTags = map[int32][]store.MonitorTag{a: {{TagID: 6, MonitorID: a, Value: "x"}, {TagID: 6, MonitorID: a, Value: "y"}}}

	r := call("bulkEditMonitors", []int32{a, b, a}, map[string]any{
		"interval": 30, "maxRetries": 2,
		"addNotifications": []int32{2}, "removeNotifications": []int32{1},
		"addTags": []map[string]any{{"tagId": 5, "value": "eu"}}, "removeTags": []int32{6},
	})
	if r["ok"] != true || r["count"] != 2 {
		t.Fatalf("bulk edit: %v", r)
	}
	for _, id := range []int32{a, b} {
		m := fs.monitors[id]
		if m.Interval != 30 || m.MaxRetries != 2 {
			t.Errorf("monitor %d: interval %d, retries %d", id, m.Interval, m.MaxRetries)
		}
		if got := fs.monitorNotifications[id]; !slices.Equal(got, []int32{2}) {
			t.Errorf("monitor %d notifications: %v", id, got)
		}
		tags := fs.monitorTags[id]
		if len(tags) != 1 || tags[0].TagID != 5 || tags[0].Value != "eu" {
			t.Errorf("monitor %d tags: %+v", id, tags)
		}
	}

	// A value one monitor refuses changes none of them.
	before := fs.monitors[a]
	r = call("bulkEditMonitors", []int32{a, b}, map[string]any{"interval": 0, "addNotifications": []int32{1}})
	if r["ok"] != false || !strings.Contains(r["msg"].(string), "interval") {
		t.Fatalf("interval 0: %v", r)
	}
	if fs.monitors[a].Interval != before.Interval || !slices.Equal(fs.monitorNotifications[a], []int32{2}) {
		t.Error("a refused bulk edit changed something")
	}

	for name, changes := range map[string]map[string]any{
		"unknown notification": {"addNotifications": []int32{9}},
		"unknown tag":          {"addTags": []map[string]any{{"tagId": 9}}},
		"added and removed":    {"addNotifications": []int32{1}, "removeNotifications": []int32{1}},
		"nothing":              {},
	} {
		if r := call("bulkEditMonitors", []int32{a}, changes); r["ok"] != false {
			t.Errorf("%s: %v", name, r)
		}
	}
	if r := call("bulkEditMonitors", []int32{a, 99}, map[string]any{"interval": 45}); r["ok"] != false || fs.monitors[a].Interval != 30 {
		t.Errorf("a missing monitor: %v", r)
	}
	if r := caller(t, s, false)("bulkEditMonitors", []int32{a}, map[string]any{"interval": 45}); r["ok"] != false {
		t.Errorf("a viewer changed monitors: %v", r)
	}
}

func TestImportMonitors(t *testing.T) {
	s, fs, _ := newTestServer(t)
	call := caller(t, s, true)
	group := addFakeMonitor(fs, "shop", monitor.TypeGroup)
	site := addFakeMonitor(fs, "site", monitor.TypeHTTP)

	good := []map[string]any{
		{"name": "api", "type": "http", "url": "https://api.example.com", "interval": 30, "parentId": group},
		{"name": "db", "type": "port", "hostname": "db.internal", "port": 5432},
	}
	r := call("importMonitors", good, true)
	if r["ok"] != true || r["count"] != 2 || len(fs.monitors) != 2 {
		t.Fatalf("dry run: %v, %d monitors", r, len(fs.monitors))
	}

	bad := append(slices.Clone(good),
		map[string]any{"name": "typo", "type": "http", "url": "https://x", "intervl": 30},
		map[string]any{"name": "no url", "type": "http"},
		map[string]any{"name": "not a group", "type": "http", "url": "https://y", "parentId": site},
	)
	r = call("importMonitors", bad, false)
	if r["ok"] != false || len(fs.monitors) != 2 {
		t.Fatalf("bad rows imported: %v", r)
	}
	rows := r["rows"].([]importRow)
	if rows[0].Error != "" || rows[1].Error != "" {
		t.Errorf("good rows have errors: %+v", rows[:2])
	}
	if !strings.Contains(rows[2].Error, "intervl") || rows[2].Name != "typo" {
		t.Errorf("the misspelled field: %+v", rows[2])
	}
	if rows[3].Error == "" || !strings.Contains(rows[4].Error, "isn't a group") {
		t.Errorf("rows 4 and 5: %+v", rows[3:])
	}

	r = call("importMonitors", good, false)
	if r["ok"] != true || r["count"] != 2 || len(fs.monitors) != 4 {
		t.Fatalf("import: %v", r)
	}
	rows = r["rows"].([]importRow)
	api := fs.monitors[rows[0].ID]
	if api.Name != "api" || api.Interval != 30 || api.ParentID == nil || *api.ParentID != group || !api.Active {
		t.Errorf("imported: %+v", api)
	}
	if db := fs.monitors[rows[1].ID]; db.Port != 5432 || db.Interval != 60 {
		t.Errorf("defaults: %+v", db)
	}
	if !slices.Equal(fs.defaultLinked, []int32{rows[0].ID, rows[1].ID}) {
		t.Errorf("default notifications: %v", fs.defaultLinked)
	}
	if r := call("importMonitors", []map[string]any{}, false); r["ok"] != false {
		t.Errorf("empty file: %v", r)
	}
	if r := caller(t, s, false)("importMonitors", good, true); r["ok"] != false {
		t.Errorf("a viewer imported: %v", r)
	}
}

type fakeChecker struct {
	mu     sync.Mutex
	result monitor.Result
	got    []monitor.Monitor
}

func (f *fakeChecker) Run(_ context.Context, m monitor.Monitor) monitor.Result {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.got = append(f.got, m)
	return f.result
}

func TestTestMonitor(t *testing.T) {
	checker := &fakeChecker{}
	s, fs, _ := newTestServerWith(t, func(o *Options) { o.Checker = checker })
	call := caller(t, s, true)
	ping := 12.5
	checker.result = monitor.Result{OK: true, Ping: &ping, Msg: "200 - OK"}
	form := map[string]any{"name": "site", "type": "http", "url": "https://example.com", "interval": 60, "maxretries": 3, "timeout": 120}

	r := call("testMonitor", form)
	if r["ok"] != true || r["status"] != monitor.StatusUp || r["ping"] != 12.5 || r["msg"] != "200 - OK" {
		t.Fatalf("up: %v", r)
	}
	if got := checker.got[0]; got.Timeout != maxTestRun.Seconds() || got.MaxRetries != 0 {
		t.Errorf("the check ran with timeout %v and %d retries", got.Timeout, got.MaxRetries)
	}
	if len(fs.monitors) != 0 {
		t.Error("the test saved a monitor")
	}

	// A failure is down at once, whatever the retries, and upside-down mode flips it.
	checker.result = monitor.Result{Msg: "connection refused"}
	if r := call("testMonitor", form); r["status"] != monitor.StatusDown || r["msg"] != "connection refused" {
		t.Errorf("down: %v", r)
	}
	form["upsideDown"] = true
	if r := call("testMonitor", form); r["status"] != monitor.StatusUp {
		t.Errorf("upside down: %v", r)
	}

	if r := call("testMonitor", map[string]any{"name": "p", "type": "push", "pushToken": "abc"}); r["ok"] != false {
		t.Errorf("push: %v", r)
	}
	if r := call("testMonitor", map[string]any{"name": "bad", "type": "http"}); r["ok"] != false {
		t.Errorf("no URL: %v", r)
	}
	if r := caller(t, s, false)("testMonitor", form); r["ok"] != false {
		t.Errorf("a viewer ran a check: %v", r)
	}

	// Without a checker, the server says so.
	plain, _, _ := newTestServer(t)
	if r := caller(t, plain, true)("testMonitor", form); r["ok"] != false || !strings.Contains(r["msg"].(string), "isn't supported") {
		t.Errorf("no checker: %v", r)
	}
}
