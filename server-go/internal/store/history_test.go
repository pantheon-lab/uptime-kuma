package store

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// The aggregates keep statistics after retention drops the beats. Clearing a monitor must
// remove its own, keep the others', and leave nothing that a later refresh would recompute
// from the beats that are gone.
func TestClearStatistics(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.CreateMonitor(ctx, member("a", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateMonitor(ctx, member("b", nil))
	if err != nil {
		t.Fatal(err)
	}
	day := time.Now().UTC().Truncate(24 * time.Hour)
	var beats []Beat
	for d := 1; d <= 40; d++ {
		for _, id := range []int32{a.ID, b.ID} {
			beats = append(beats, Beat{MonitorID: id, Time: day.Add(time.Duration(-d)*24*time.Hour + time.Hour), Status: monitor.StatusUp})
		}
	}
	if _, err := s.InsertBeats(ctx, beats); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int32{a.ID, b.ID} {
		for _, table := range []string{"imported_stat_hourly", "imported_stat_daily"} {
			if _, err := s.pool.Exec(ctx, `INSERT INTO `+table+` VALUES ($1, $2, 5, 0, 0, 0, 0, NULL, NULL)`,
				id, day.Add(-400*24*time.Hour)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	// What retention does to beats older than 10 days.
	if _, err := s.pool.Exec(ctx, `SELECT drop_chunks('heartbeat', older_than => now() - INTERVAL '10 days')`); err != nil {
		t.Fatal(err)
	}

	count := func(table string, id int32) int64 {
		t.Helper()
		column := "count(*)"
		if table != "heartbeat" {
			column = "COALESCE(sum(up), 0)"
		}
		var n int64
		if err := s.pool.QueryRow(ctx, `SELECT `+column+`::bigint FROM `+table+` WHERE monitor_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	want := func(id int32, beats, aggregated int64) {
		t.Helper()
		if n := count("heartbeat", id); n != beats {
			t.Errorf("monitor %d has %d beats, want %d", id, n, beats)
		}
		for _, view := range []string{"stat_hourly", "stat_daily"} {
			if n := count(view, id); n != aggregated {
				t.Errorf("monitor %d has %d in %s, want %d", id, n, view, aggregated)
			}
		}
		for _, view := range []string{"uptime_hourly", "uptime_daily"} {
			imported := int64(5)
			if aggregated == 0 {
				imported = 0
			}
			if n := count(view, id); n != aggregated+imported {
				t.Errorf("monitor %d has %d in %s, want %d", id, n, view, aggregated+imported)
			}
		}
	}
	keptBeats := count("heartbeat", b.ID)
	if keptBeats < 8 || keptBeats > 10 {
		t.Fatalf("%d beats kept after dropping chunks", keptBeats)
	}
	want(b.ID, keptBeats, 40)
	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	want(b.ID, keptBeats, 40)

	if err := s.ClearMonitorStats(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	want(a.ID, 0, 0)
	want(b.ID, keptBeats, 40)
	// Deleting a monitor refreshes the aggregates, which used to delete the statistics of
	// every monitor from before the beats kept.
	if err := s.DeleteMonitor(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	want(b.ID, keptBeats, 40)

	if err := s.ClearAllStats(ctx); err != nil {
		t.Fatal(err)
	}
	want(b.ID, 0, 0)
	// Monitoring goes on after.
	if _, err := s.InsertBeats(ctx, []Beat{{MonitorID: b.ID, Time: time.Now().Add(-2 * time.Hour), Status: monitor.StatusUp}}); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count("uptime_hourly", b.ID); n != 1 {
		t.Errorf("a new beat counts %d times after clearing", n)
	}

	size, err := s.DatabaseSize(ctx)
	if err != nil || size < 1<<20 {
		t.Errorf("database size %d, %v", size, err)
	}
}

func TestRetentionDays(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if days, saved, err := s.RetentionDays(ctx, 365); err != nil || days != 365 || saved {
		t.Fatalf("first start: %d %v %v", days, saved, err)
	}
	if days, saved, err := s.RetentionDays(ctx, 30); err != nil || days != 365 || !saved {
		t.Fatalf("saved setting: %d %v %v", days, saved, err)
	}
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{RetentionSetting: json.RawMessage(`"90"`)}); err != nil {
		t.Fatal(err)
	}
	if days, _, err := s.RetentionDays(ctx, 30); err != nil || days != 90 {
		t.Fatalf("text: %d %v", days, err)
	}
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{RetentionSetting: json.RawMessage(`-4`)}); err != nil {
		t.Fatal(err)
	}
	if days, saved, err := s.RetentionDays(ctx, 30); err != nil || days != 30 || saved {
		t.Fatalf("invalid: %d %v %v", days, saved, err)
	}
	settings, err := s.Settings(ctx)
	if err != nil || string(settings[RetentionSetting]) != "30" {
		t.Fatalf("reset to %s, %v", settings[RetentionSetting], err)
	}
}
