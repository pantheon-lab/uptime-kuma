package ui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
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

// BulkEditMonitors applies a bulk edit to the fake store's monitors, notifications and tags, all
// or nothing: a missing monitor changes nothing.
func (f *fakeStore) BulkEditMonitors(_ context.Context, e store.BulkEdit) ([]monitor.Monitor, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range e.MonitorIDs {
		if _, found := f.monitors[id]; !found {
			return nil, store.ErrNotFound
		}
	}
	var updated []monitor.Monitor
	for _, set := range e.Settings {
		m := f.monitors[set.ID]
		m.Interval, m.RetryInterval, m.MaxRetries = set.Interval, set.RetryInterval, set.MaxRetries
		f.monitors[set.ID] = m
		updated = append(updated, m)
	}
	if f.monitorNotifications == nil {
		f.monitorNotifications = map[int32][]int32{}
	}
	if f.monitorTags == nil {
		f.monitorTags = map[int32][]store.MonitorTag{}
	}
	for _, id := range e.MonitorIDs {
		next := slices.DeleteFunc(slices.Clone(f.monitorNotifications[id]), func(n int32) bool {
			return slices.Contains(e.RemoveNotifications, n)
		})
		for _, n := range e.AddNotifications {
			if !slices.Contains(next, n) {
				next = append(next, n)
			}
		}
		f.monitorNotifications[id] = next
		tags := slices.DeleteFunc(slices.Clone(f.monitorTags[id]), func(t store.MonitorTag) bool {
			return slices.Contains(e.RemoveTags, t.TagID)
		})
		for _, t := range e.AddTags {
			if !slices.ContainsFunc(tags, func(x store.MonitorTag) bool { return x.TagID == t.TagID && x.Value == t.Value }) {
				tags = append(tags, store.MonitorTag{TagID: t.TagID, MonitorID: id, Value: t.Value})
			}
		}
		f.monitorTags[id] = tags
	}
	return updated, nil
}

func TestImportMonitorsGroupsInTheFile(t *testing.T) {
	s, fs, _ := newTestServer(t)
	call := caller(t, s, true)
	existing := addFakeMonitor(fs, "existing", monitor.TypeGroup)
	pusher := monitor.New()
	pusher.Name, pusher.Type, pusher.PushToken = "pusher", monitor.TypePush, "taken-token"
	if _, err := fs.CreateMonitor(context.Background(), pusher); err != nil {
		t.Fatal(err)
	}

	// Children before their groups, and ids that are taken on this server: the file's own ids win.
	file := []map[string]any{
		{"id": 30, "name": "api", "type": "http", "url": "https://api.example.com", "parentId": 20},
		{"id": 20, "name": "eu", "type": "group", "parentId": existing},
		{"id": existing, "name": "shop", "type": "group"},
		{"name": "web", "type": "http", "url": "https://web.example.com", "parentId": existing},
	}
	file[1]["parentId"] = existing
	if r := call("importMonitors", file, true); r["ok"] != true || len(fs.monitors) != 2 {
		t.Fatalf("dry run: %v", r)
	}
	r := call("importMonitors", file, false)
	if r["ok"] != true || r["count"] != 4 {
		t.Fatalf("import: %v", r)
	}
	rows := r["rows"].([]importRow)
	api, eu, shop, web := fs.monitors[rows[0].ID], fs.monitors[rows[1].ID], fs.monitors[rows[2].ID], fs.monitors[rows[3].ID]
	if shop.ParentID != nil || eu.ParentID == nil || *eu.ParentID != shop.ID ||
		api.ParentID == nil || *api.ParentID != eu.ID || web.ParentID == nil || *web.ParentID != shop.ID {
		t.Errorf("groups: shop %v, eu %v, api %v, web %v", shop.ParentID, eu.ParentID, api.ParentID, web.ParentID)
	}
	if shop.ID == existing {
		t.Error("the file's id was used as the new monitor's")
	}

	for name, tc := range map[string]struct {
		file []map[string]any
		want string
	}{
		"twice": {[]map[string]any{
			{"id": 1, "name": "a", "type": "group"},
			{"id": 1, "name": "b", "type": "group"},
		}, "also row 1's"},
		"not a group": {[]map[string]any{
			{"id": 1, "name": "a", "type": "http", "url": "https://a"},
			{"name": "b", "type": "http", "url": "https://b", "parentId": 1},
		}, "isn't a group"},
		"push token here": {[]map[string]any{
			{"name": "a", "type": "push", "pushToken": "taken-token"},
		}, "already monitor \"pusher\"'s"},
		"push token twice": {[]map[string]any{
			{"name": "a", "type": "push", "pushToken": "same-token"},
			{"name": "b", "type": "push", "pushToken": "same-token"},
		}, "already row 1's"},
		"loop": {[]map[string]any{
			{"id": 1, "name": "a", "type": "group", "parentId": 2},
			{"id": 2, "name": "b", "type": "group", "parentId": 1},
		}, "contain each other"},
	} {
		before := len(fs.monitors)
		r := call("importMonitors", tc.file, false)
		if r["ok"] != false || len(fs.monitors) != before {
			t.Errorf("%s: %v", name, r)
			continue
		}
		var errs []string
		for _, row := range r["rows"].([]importRow) {
			errs = append(errs, row.Error)
		}
		if !strings.Contains(strings.Join(errs, "|"), tc.want) {
			t.Errorf("%s: errors %q, want %q", name, errs, tc.want)
		}
	}
}

func TestExportMonitors(t *testing.T) {
	s, fs, _ := newTestServer(t)
	call := caller(t, s, true)
	ctx := context.Background()
	add := func(name, typ string, parent int32) int32 {
		m := monitor.New()
		m.Name, m.Type = name, typ
		if typ == monitor.TypeHTTP {
			m.URL, m.BasicAuthUser, m.BasicAuthPass = "https://"+name+".example.com", "kuma", "secret-"+name
		}
		if parent > 0 {
			m.ParentID = &parent
		}
		created, err := fs.CreateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		return created.ID
	}
	shop := add("shop", monitor.TypeGroup, 0)
	eu := add("eu", monitor.TypeGroup, shop)
	api := add("api", monitor.TypeHTTP, eu)
	add("web", monitor.TypeHTTP, shop)
	other := add("other", monitor.TypeHTTP, 0)

	names := func(r reply) []string {
		t.Helper()
		if r["ok"] != true {
			t.Fatalf("export: %v", r)
		}
		var out []string
		for _, m := range r["monitors"].([]monitor.Monitor) {
			out = append(out, m.Name)
		}
		return out
	}

	// Only what was chosen; a group that isn't exported isn't named.
	r := call("exportMonitors", []int32{api, other}, map[string]any{})
	if got := names(r); !slices.Equal(got, []string{"api", "other"}) {
		t.Errorf("alone: %v", got)
	}
	exported := r["monitors"].([]monitor.Monitor)
	if exported[0].ParentID != nil || exported[0].BasicAuthPass != "secret-api" {
		t.Errorf("api: parent %v, password %q", exported[0].ParentID, exported[0].BasicAuthPass)
	}
	if fs.monitors[api].ParentID == nil {
		t.Error("the export changed the stored monitor")
	}

	// With the groups above it, each group before what's in it.
	if got := names(call("exportMonitors", []int32{api}, map[string]any{"groups": true})); !slices.Equal(got, []string{"shop", "eu", "api"}) {
		t.Errorf("with groups: %v", got)
	}
	// A group with everything in it.
	r = call("exportMonitors", []int32{shop}, map[string]any{"children": true})
	if got := names(r); !slices.Equal(got, []string{"shop", "eu", "api", "web"}) {
		t.Errorf("with children: %v", got)
	}

	// The file imports again as it is, into the same groups' copies.
	raw, _ := json.Marshal(r["monitors"])
	var file []map[string]any
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	r = call("importMonitors", file, false)
	if r["ok"] != true || r["count"] != 4 {
		t.Fatalf("import the export: %v", r)
	}
	rows := r["rows"].([]importRow)
	newShop, newEU, newAPI, newWeb := fs.monitors[rows[0].ID], fs.monitors[rows[1].ID], fs.monitors[rows[2].ID], fs.monitors[rows[3].ID]
	if newShop.ParentID != nil || *newEU.ParentID != newShop.ID || *newAPI.ParentID != newEU.ID || *newWeb.ParentID != newShop.ID {
		t.Errorf("imported groups: %v %v %v %v", newShop.ParentID, newEU.ParentID, newAPI.ParentID, newWeb.ParentID)
	}
	if newAPI.BasicAuthPass != "secret-api" || newAPI.URL != "https://api.example.com" {
		t.Errorf("imported api: %+v", newAPI)
	}

	for name, args := range map[string][]any{
		"nothing": {[]int32{}},
		"missing": {[]int32{api, 999}},
	} {
		if r := call("exportMonitors", args...); r["ok"] != false {
			t.Errorf("%s: %v", name, r)
		}
	}
	if r := caller(t, s, false)("exportMonitors", []int32{api}); r["ok"] != false {
		t.Errorf("a viewer exported credentials: %v", r)
	}
}

func TestEditingAGroupSendsTheMonitorsInIt(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["test"] = c
	s.mu.Unlock()
	group := addFakeMonitor(fs, "Shop", monitor.TypeGroup)
	child := monitor.New()
	child.Name, child.Type, child.URL, child.ParentID = "API", monitor.TypeHTTP, "https://api.example.com", &group
	child, _ = fs.CreateMonitor(ctx, child)

	tree, err := s.monitorTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	form := tree.monitorJSON(tree.byID[group])
	form["name"] = "Store"
	raw, _ := json.Marshal(form)
	r, _ := s.handle(ctx, c, "editMonitor", []json.RawMessage{raw})
	if r.(reply)["ok"] != true {
		t.Fatalf("edit: %v", r)
	}

	// The update is an SSE frame of ["updateMonitorIntoList", {id: monitor}].
	sent := ""
	for len(c.out) > 0 {
		if frame := string(<-c.out); strings.Contains(frame, `"updateMonitorIntoList"`) {
			sent += frame
		}
	}
	if !strings.Contains(sent, `"`+strconv.Itoa(int(child.ID))+`":{`) || !strings.Contains(sent, `"pathName":"Store / API"`) {
		t.Fatalf("the monitor in the renamed group wasn't sent with its new path: %s", sent)
	}
}

func TestReloadSendsOpenPagesTheirLists(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	admin := &conn{userID: 1, admin: true, username: "a", out: make(chan []byte, 256), done: make(chan struct{})}
	viewer := &conn{userID: 2, username: "v", out: make(chan []byte, 256), done: make(chan struct{})}
	anonymous := &conn{out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["admin"], s.conns["viewer"], s.conns["anonymous"] = admin, viewer, anonymous
	s.mu.Unlock()

	// Written to the database by something else, such as kuma-import.
	m := monitor.New()
	m.Name, m.Type, m.URL, m.BasicAuthPass = "Imported", monitor.TypeHTTP, "https://imported.example.com", "s3cret"
	if _, err := fs.CreateMonitor(ctx, m); err != nil {
		t.Fatal(err)
	}
	if err := s.Reload(ctx); err != nil {
		t.Fatal(err)
	}

	read := func(c *conn) string {
		out := ""
		for len(c.out) > 0 {
			out += string(<-c.out)
		}
		return out
	}
	if got := read(admin); !strings.Contains(got, `"monitorList"`) || !strings.Contains(got, "Imported") || !strings.Contains(got, `"notificationList"`) {
		t.Errorf("the admin's page: %.300s", got)
	}
	got := read(viewer)
	if !strings.Contains(got, `"monitorList"`) || !strings.Contains(got, "Imported") {
		t.Errorf("the viewer's page: %.300s", got)
	}
	if strings.Contains(got, "s3cret") {
		t.Error("the viewer was sent a monitor's password")
	}
	if got := read(anonymous); got != "" {
		t.Errorf("a page that isn't logged in was sent: %.300s", got)
	}
	// The latest beats or the uptime that can't be read fail it too, so that the listener tries again.
	for name, set := range map[string]func(error){
		"latest beats": func(err error) { fs.latestBeatsErr = err },
		"uptime":       func(err error) { fs.statsErr = err },
	} {
		failure := errors.New("the database is gone")
		set(failure)
		if err := s.Reload(ctx); !errors.Is(err, failure) {
			t.Errorf("%s failing: reload returned %v", name, err)
		}
		set(nil)
		for len(admin.out) > 0 {
			<-admin.out
		}
	}
	if err := s.Reload(ctx); err != nil {
		t.Errorf("a reload with nothing failing: %v", err)
	}

	// Out of time: it fails, so that the listener tries again.
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if err := s.Reload(cancelled); err == nil {
		t.Error("a reload that ran out of time succeeded")
	}
}

func TestSetMonitorsActiveSendsOneUpdate(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, username: "a", out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["admin"] = c
	s.mu.Unlock()
	sched := s.scheduler.(*fakeScheduler)
	group := addFakeMonitor(fs, "Shop", monitor.TypeGroup)
	child := monitor.New()
	child.Name, child.Type, child.URL, child.ParentID = "API", monitor.TypeHTTP, "https://api.example.com", &group
	child, _ = fs.CreateMonitor(ctx, child)
	other := addFakeMonitor(fs, "Site", monitor.TypeHTTP)
	for _, id := range []int32{group, child.ID, other} {
		m := fs.monitors[id]
		m.Active = false
		fs.monitors[id] = m
	}
	call := func(args ...any) reply {
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, _ := s.handle(ctx, c, "setMonitorsActive", raws)
		return r.(reply)
	}

	r := call([]int32{group, other, group}, true)
	if r["ok"] != true || r["count"] != 2 {
		t.Fatalf("resume: %v", r)
	}
	if !fs.monitors[group].Active || !fs.monitors[other].Active || fs.monitors[child.ID].Active {
		t.Errorf("active: group %v, other %v, child %v", fs.monitors[group].Active, fs.monitors[other].Active, fs.monitors[child.ID].Active)
	}
	if len(sched.upserted) != 2 {
		t.Errorf("the scheduler got %d monitors, want 2", len(sched.upserted))
	}

	// One update, with the group's monitor too: it shows as paused while its group is.
	var updates []string
	for len(c.out) > 0 {
		if frame := string(<-c.out); strings.Contains(frame, `"updateMonitorIntoList"`) {
			updates = append(updates, frame)
		}
	}
	if len(updates) != 1 {
		t.Fatalf("%d list updates, want 1", len(updates))
	}
	for _, id := range []int32{group, child.ID, other} {
		if !strings.Contains(updates[0], `"`+strconv.Itoa(int(id))+`":{`) {
			t.Errorf("monitor %d isn't in the update", id)
		}
	}

	if r := call([]int32{}, true); r["ok"] != false {
		t.Errorf("nothing chosen: %v", r)
	}
	if r, _ := s.handle(ctx, &conn{userID: 2, out: make(chan []byte, 8), done: make(chan struct{})}, "setMonitorsActive",
		[]json.RawMessage{json.RawMessage(`[1]`), json.RawMessage(`false`)}); r.(reply)["ok"] != false {
		t.Errorf("a viewer paused monitors: %v", r)
	}
}

func TestSetMonitorOrder(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, username: "a", out: make(chan []byte, 256), done: make(chan struct{})}
	s.mu.Lock()
	s.conns["admin"] = c
	s.mu.Unlock()
	sched := s.scheduler.(*fakeScheduler)
	group := addFakeMonitor(fs, "Shop", monitor.TypeGroup)
	child := func(name string) int32 {
		m := monitor.New()
		m.Name, m.Type, m.URL, m.ParentID = name, monitor.TypeHTTP, "https://"+name, &group
		created, _ := fs.CreateMonitor(ctx, m)
		return created.ID
	}
	a, b := child("a"), child("b")
	top := addFakeMonitor(fs, "Site", monitor.TypeHTTP)
	call := func(by *conn, args ...any) reply {
		raws := make([]json.RawMessage, len(args))
		for i, x := range args {
			raws[i], _ = json.Marshal(x)
		}
		r, _ := s.handle(ctx, by, "setMonitorOrder", raws)
		return r.(reply)
	}

	upserts := len(sched.upserted)
	if r := call(c, group, []int32{b, a}); r["ok"] != true || r["count"] != 2 {
		t.Fatalf("order: %v", r)
	}
	if fs.monitors[b].SortOrder != 1 || fs.monitors[a].SortOrder != 2 {
		t.Errorf("orders: b %d, a %d", fs.monitors[b].SortOrder, fs.monitors[a].SortOrder)
	}
	if len(sched.upserted) != upserts {
		t.Error("ordering monitors restarted their checks")
	}
	sent := ""
	for len(c.out) > 0 {
		sent += string(<-c.out)
	}
	if !strings.Contains(sent, `"updateMonitorIntoList"`) || !strings.Contains(sent, `"sortOrder":1`) {
		t.Errorf("open pages weren't sent the order: %.300s", sent)
	}

	// The top of the list, with a null parent.
	if r := call(c, nil, []int32{top, group}); r["ok"] != true || fs.monitors[top].SortOrder != 1 {
		t.Errorf("order the top: %v", r)
	}
	// A monitor from another group changes nothing.
	if r := call(c, group, []int32{a, top}); r["ok"] != false || fs.monitors[a].SortOrder != 2 {
		t.Errorf("mixed groups: %v", r)
	}
	if r := call(c, group, []int32{}); r["ok"] != false {
		t.Errorf("nothing to order: %v", r)
	}
	tooMany := make([]int32, maxOrderedMonitors+1)
	for i := range tooMany {
		tooMany[i] = a // Duplicates too: the size is checked before they're removed
	}
	if r := call(c, group, tooMany); r["ok"] != false {
		t.Errorf("too many: %v", r)
	}
	viewer := &conn{userID: 2, out: make(chan []byte, 8), done: make(chan struct{})}
	if r := call(viewer, group, []int32{a, b}); r["ok"] != false {
		t.Errorf("a viewer ordered monitors: %v", r)
	}
}
