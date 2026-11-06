package ui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func (f *fakeStore) ClearMonitorStats(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, id)
	return nil
}

func (f *fakeStore) ClearAllStats(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cleared = append(f.cleared, 0)
	return nil
}

func (f *fakeStore) DatabaseSize(context.Context) (int64, error) { return 42 << 20, nil }

func (f *fakeStore) SetRetention(_ context.Context, days int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.retention = append(f.retention, days)
	return nil
}

func TestMonitorHistoryEvents(t *testing.T) {
	s, fs, ts := newTestServer(t)
	sched := s.scheduler.(*fakeScheduler)
	ctx := context.Background()
	c := &conn{userID: 1, username: "admin", out: make(chan []byte, 256), done: make(chan struct{})}
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
	for _, m := range []monitor.Monitor{{Name: "a", Type: "http", Active: true}, {Name: "b", Type: "http"}} {
		if _, err := fs.CreateMonitor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("retention", func(t *testing.T) {
		if r := call("setSettings", map[string]any{"keepDataPeriodDays": "30", "entryPage": "dashboard"}); r["ok"] != true {
			t.Fatalf("save: %v", r)
		}
		if string(fs.settings["keepDataPeriodDays"]) != "30" || !slices.Equal(fs.retention, []int{30}) {
			t.Fatalf("saved %s, applied %v", fs.settings["keepDataPeriodDays"], fs.retention)
		}
		// Saving another page sends the same value again, which changes nothing.
		if r := call("setSettings", map[string]any{"keepDataPeriodDays": 30}); r["ok"] != true || len(fs.retention) != 1 {
			t.Fatalf("unchanged: %v, applied %v", r, fs.retention)
		}
		for _, bad := range []any{-1, "1.5", "x", 1e9, nil} {
			if r := call("setSettings", map[string]any{"keepDataPeriodDays": bad, "entryPage": "statusPage-x"}); r["ok"] != false {
				t.Fatalf("%v: %v", bad, r)
			}
		}
		if string(fs.settings["entryPage"]) != `"dashboard"` || len(fs.retention) != 1 {
			t.Fatal("a refused save changed something")
		}
		if r := call("setSettings", map[string]any{"keepDataPeriodDays": 0}); r["ok"] != true || !slices.Equal(fs.retention, []int{30, 0}) {
			t.Fatalf("forever: %v, applied %v", r, fs.retention)
		}
	})

	t.Run("clear a monitor", func(t *testing.T) {
		sched.removed, sched.upserted = nil, nil
		if r := call("clearHeartbeats", 1); r["ok"] != true {
			t.Fatalf("clear: %v", r)
		}
		if !slices.Equal(fs.cleared, []int32{1}) || !slices.Equal(sched.removed, []int32{1}) ||
			len(sched.upserted) != 1 || sched.upserted[0].ID != 1 {
			t.Fatalf("cleared %v, removed %v, upserted %v", fs.cleared, sched.removed, sched.upserted)
		}
		if r := call("clearHeartbeats", 99); r["ok"] != false || len(fs.cleared) != 1 {
			t.Fatalf("unknown monitor: %v", r)
		}
	})

	t.Run("clear everything", func(t *testing.T) {
		sched.removed, sched.upserted, fs.cleared = nil, nil, nil
		if r := call("clearStatistics"); r["ok"] != true {
			t.Fatalf("clear: %v", r)
		}
		slices.Sort(sched.removed)
		if !slices.Equal(fs.cleared, []int32{0}) || !slices.Equal(sched.removed, []int32{1, 2}) || len(sched.upserted) != 2 {
			t.Fatalf("cleared %v, removed %v, upserted %d", fs.cleared, sched.removed, len(sched.upserted))
		}
	})

	t.Run("database size", func(t *testing.T) {
		if r := call("getDatabaseSize"); r["ok"] != true || r["size"] != int64(42<<20) {
			t.Fatalf("size: %v", r)
		}
		if r := call("shrinkDatabase"); r["ok"] != false {
			t.Fatalf("shrink: %v", r)
		}
	})

	t.Run("robots.txt", func(t *testing.T) {
		get := func() string {
			t.Helper()
			resp, err := http.Get(ts.URL + "/robots.txt")
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return string(body)
		}
		if body := get(); body != "User-agent: *\nDisallow: /" {
			t.Fatalf("by default: %q", body)
		}
		call("setSettings", map[string]any{"searchEngineIndex": true})
		if body := get(); body != "User-agent: *\nDisallow:" {
			t.Fatalf("allowed: %q", body)
		}
	})
}
