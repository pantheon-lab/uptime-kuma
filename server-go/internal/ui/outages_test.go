package ui

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

func TestFindOutages(t *testing.T) {
	at := func(m int) time.Time { return time.Date(2026, 11, 1, 0, m, 0, 0, time.UTC) }
	beats := []store.Beat{
		{Time: at(0), Status: 1},                                 // up, before any outage: no outage
		{Time: at(10), Status: 0, Msg: "refused", TraceID: "t1"}, // down
		{Time: at(12), Status: 2},                                // pending doesn't end it
		{Time: at(13), Status: 0, Msg: "again"},                  // still the same outage
		{Time: at(20), Status: 1},                                // up: ends it
		{Time: at(30), Status: 0, Msg: "timeout"},                // down
		{Time: at(35), Status: 3},                                // maintenance ends it
		{Time: at(50), Status: 0, Msg: "dns"},                    // down, not over
	}
	got := findOutages(beats)
	if len(got) != 3 {
		t.Fatalf("outages: %+v", got)
	}
	if !got[0].Start.Equal(at(10)) || !got[0].End.Equal(at(20)) || got[0].Msg != "refused" || got[0].TraceID != "t1" {
		t.Errorf("first: %+v", got[0])
	}
	if !got[1].Start.Equal(at(30)) || !got[1].End.Equal(at(35)) {
		t.Errorf("second: %+v", got[1])
	}
	if !got[2].Start.Equal(at(50)) || got[2].End != nil || got[2].Msg != "dns" {
		t.Errorf("ongoing: %+v", got[2])
	}
	// Leading up, no downs
	if got := findOutages([]store.Beat{{Time: at(0), Status: 1}, {Time: at(1), Status: 2}}); len(got) != 0 {
		t.Errorf("no outage expected: %+v", got)
	}
}

func TestOutageAndChartRangeEvents(t *testing.T) {
	s, fs, _ := newTestServer(t)
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return now }
	ctx := context.Background()
	c := &conn{userID: 2, admin: false, username: "viewer", out: make(chan []byte, 16), done: make(chan struct{})}
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

	// Newest first, as the store returns them: an outage of an hour ten days ago, one of
	// 30 minutes 40 days ago (outside 30 days), and one going on for two hours.
	fs.important = []store.Beat{
		{Time: now.Add(-2 * time.Hour), Status: 0, Msg: "now down", TraceID: "abc"},
		{Time: now.Add(-10*24*time.Hour + time.Hour), Status: 1},
		{Time: now.Add(-10 * 24 * time.Hour), Status: 0, Msg: "refused"},
		{Time: now.Add(-40*24*time.Hour + 30*time.Minute), Status: 1},
		{Time: now.Add(-40 * 24 * time.Hour), Status: 0, Msg: "old"},
	}

	t.Run("outages in the last 30 days, newest first, for a viewer too", func(t *testing.T) {
		r := call("getMonitorOutages", 1, 30)
		if r["ok"] != true || r["count"] != 2 || r["downtimeSeconds"] != int64(3*3600) {
			t.Fatalf("reply: %v", r)
		}
		list := r["outages"].([]map[string]any)
		if list[0]["ongoing"] != true || list[0]["end"] != nil || list[0]["durationSeconds"] != int64(7200) || list[0]["traceId"] != "abc" {
			t.Errorf("ongoing: %v", list[0])
		}
		if list[1]["ongoing"] != false || list[1]["msg"] != "refused" || list[1]["durationSeconds"] != int64(3600) {
			t.Errorf("ended: %v", list[1])
		}
	})

	t.Run("a longer window, and a default", func(t *testing.T) {
		if r := call("getMonitorOutages", 1, 60); r["count"] != 3 {
			t.Errorf("60 days: %v", r["count"])
		}
		if r := call("getMonitorOutages", 1); r["count"] != 2 || r["days"] != int64(30) {
			t.Errorf("default: %v", r)
		}
		for _, bad := range []any{0, 366, "x"} {
			if r := call("getMonitorOutages", 1, bad); r["ok"] != false {
				t.Errorf("%v: %v", bad, r)
			}
		}
	})

	t.Run("a chart range", func(t *testing.T) {
		r := call("getMonitorChartRange", 1, "2026-11-01T10:00:00Z", "2026-11-01T16:00:00+02:00")
		if r["ok"] != true || len(r["data"].([]map[string]any)) != 1 {
			t.Fatalf("reply: %v", r)
		}
		got := fs.chartRanges[len(fs.chartRanges)-1]
		if !got[0].Equal(time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)) || !got[1].Equal(time.Date(2026, 11, 1, 14, 0, 0, 0, time.UTC)) {
			t.Errorf("asked for %v", got)
		}
		for _, bad := range [][2]any{
			{"yesterday", "2026-11-01T16:00:00Z"},
			{"2026-11-01T16:00:00Z", "2026-11-01T10:00:00Z"},
			{"2024-01-01T00:00:00Z", "2026-01-01T00:00:00Z"},
		} {
			if r := call("getMonitorChartRange", 1, bad[0], bad[1]); r["ok"] != false {
				t.Errorf("%v: %v", bad, r)
			}
		}
	})
}
