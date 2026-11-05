package store

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestMaintenanceCRUD(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	w, err := s.CreateMaintenance(ctx, maintenance.Window{
		Title: "Patch", Strategy: maintenance.StrategyWeekday, Active: true, StartTime: "02:00", EndTime: "04:00",
		Weekdays: []int{0, 5}, DaysOfMonth: []int{15, -1}, IntervalDay: 1, Timezone: maintenance.SameAsServer,
		HolidayMode: maintenance.HolidaysExclude, HolidayCountries: []string{"HK", "CN"},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMaintenance(ctx, w.ID)
	if err != nil || got.Title != "Patch" || !slices.Equal(got.Weekdays, []int{0, 5}) ||
		!slices.Equal(got.DaysOfMonth, []int{15, -1}) || !slices.Equal(got.HolidayCountries, []string{"HK", "CN"}) ||
		got.HolidayMode != maintenance.HolidaysExclude {
		t.Fatalf("get: %+v %v", got, err)
	}

	got.Title, got.Weekdays, got.HolidayCountries = "Patched", nil, nil
	updated, err := s.UpdateMaintenance(ctx, got)
	if err != nil || updated.Title != "Patched" || len(updated.Weekdays) != 0 {
		t.Fatalf("update: %+v %v", updated, err)
	}
	got.ID = 999
	if _, err := s.UpdateMaintenance(ctx, got); !errors.Is(err, ErrNotFound) {
		t.Fatalf("update missing: %v", err)
	}
	if err := s.SetMaintenanceActive(ctx, w.ID, false); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GetMaintenance(ctx, w.ID); got.Active {
		t.Fatal("still active")
	}
	if err := s.SetMaintenanceActive(ctx, 999, false); !errors.Is(err, ErrNotFound) {
		t.Fatalf("pause missing: %v", err)
	}

	m := monitor.New()
	m.Name, m.Type, m.URL = "a", monitor.TypeHTTP, "https://example.com"
	created, err := s.CreateMonitor(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMaintenanceMonitors(ctx, w.ID, []int32{created.ID, created.ID}); err != nil {
		t.Fatal(err)
	}
	links, err := s.MaintenanceMonitorLinks(ctx)
	if err != nil || !slices.Equal(links[w.ID], []int32{created.ID}) {
		t.Fatalf("links: %v %v", links, err)
	}
	if err := s.SetMaintenanceMonitors(ctx, w.ID, []int32{999}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing monitor: %v", err)
	}
	if err := s.SetMaintenanceMonitors(ctx, 999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing window: %v", err)
	}
	// A failed change leaves the old links.
	if links, _ := s.MaintenanceMonitorLinks(ctx); len(links[w.ID]) != 1 {
		t.Fatalf("links after failure: %v", links)
	}

	if err := s.DeleteMonitor(ctx, created.ID); err != nil {
		t.Fatal(err)
	}
	if links, _ := s.MaintenanceMonitorLinks(ctx); len(links[w.ID]) != 0 {
		t.Fatalf("links after deleting the monitor: %v", links)
	}
	if err := s.DeleteMaintenance(ctx, w.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteMaintenance(ctx, w.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
}

func TestHolidayOverrides(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	if err := s.SetHolidayOverride(ctx, holidays.Override{Country: "hk", Date: "2026-04-08", Name: "Closed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHolidayOverride(ctx, holidays.Override{Country: "HK", Date: "2026-04-08", Name: "Renamed"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetHolidayOverride(ctx, holidays.Override{Country: "HK", Date: "2026-04-03", Removed: true}); err != nil {
		t.Fatal(err)
	}
	got, err := s.HolidayOverrides(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("overrides: %v %v", got, err)
	}
	for _, o := range got {
		if o.Date == "2026-04-08" && (o.Name != "Renamed" || o.Country != "HK" || o.Removed) {
			t.Fatalf("override: %+v", o)
		}
	}
	if err := s.DeleteHolidayOverride(ctx, "hk", "2026-04-08"); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.HolidayOverrides(ctx); len(got) != 1 || !got[0].Removed {
		t.Fatalf("after delete: %v", got)
	}
}
