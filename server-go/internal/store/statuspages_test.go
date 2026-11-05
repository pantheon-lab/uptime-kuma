package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestStatusPageLifecycle(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	page, err := s.CreateStatusPage(ctx, "Public", "public")
	if err != nil {
		t.Fatal(err)
	}
	if page.Theme != "auto" || page.AutoRefreshInterval != 300 || !page.ShowPoweredBy || len(page.Domains) != 0 {
		t.Fatalf("defaults: %+v", page)
	}
	if _, err := s.CreateStatusPage(ctx, "Again", "public"); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("duplicate slug: %v", err)
	}
	other, err := s.CreateStatusPage(ctx, "Other", "other")
	if err != nil {
		t.Fatal(err)
	}

	a, err := s.CreateMonitor(ctx, member("a", nil))
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.CreateMonitor(ctx, member("b", nil))
	if err != nil {
		t.Fatal(err)
	}

	// The other page has a domain, which moves to this page when it claims it.
	other.Domains = []string{"status.example.com"}
	if _, err := s.SaveStatusPage(ctx, "other", other, nil); err != nil {
		t.Fatal(err)
	}
	custom := "https://custom.example.com"
	page.Slug, page.Title, page.Domains = "renamed", "Renamed", []string{"status.example.com", "b.example.com", "b.example.com"}
	kind := "plausible"
	page.AnalyticsType = &kind
	groups, err := s.SaveStatusPage(ctx, "public", page, []StatusPageGroup{
		{Name: "First", Monitors: []StatusPageMonitor{{ID: b.ID, SendURL: true, CustomURL: &custom}, {ID: a.ID}, {ID: a.ID}, {ID: 9999}}},
		{Name: "Empty"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "First" || len(groups[0].Monitors) != 2 || groups[1].Name != "Empty" || len(groups[1].Monitors) != 0 {
		t.Fatalf("groups: %+v", groups)
	}
	if first := groups[0].Monitors[0]; first.ID != b.ID || first.Name != "b" || !first.SendURL || *first.CustomURL != custom {
		t.Fatalf("first monitor: %+v", first)
	}
	if _, err := s.StatusPageBySlug(ctx, "public"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("old slug: %v", err)
	}
	saved, err := s.StatusPageBySlug(ctx, "renamed")
	if err != nil || saved.Title != "Renamed" || *saved.AnalyticsType != "plausible" || len(saved.Domains) != 2 {
		t.Fatalf("saved: %+v %v", saved, err)
	}
	domains, err := s.StatusPageDomains(ctx)
	if err != nil || len(domains) != 2 || domains["status.example.com"] != "renamed" {
		t.Fatalf("domains: %v %v", domains, err)
	}
	if _, err := s.SaveStatusPage(ctx, "renamed", StatusPage{Slug: "other", Title: "x"}, nil); !errors.Is(err, ErrSlugTaken) {
		t.Fatalf("renaming onto another slug: %v", err)
	}

	// Saving again keeps a group by its id, and drops groups that aren't sent.
	groups, err = s.SaveStatusPage(ctx, "renamed", saved, []StatusPageGroup{{ID: groups[1].ID, Name: "Now first", Monitors: []StatusPageMonitor{{ID: a.ID}}}})
	if err != nil || len(groups) != 1 || groups[0].Name != "Now first" {
		t.Fatalf("resave: %+v %v", groups, err)
	}
	ids, err := s.StatusPageMonitorIDs(ctx, saved.ID)
	if err != nil || len(ids) != 1 || ids[0] != a.ID {
		t.Fatalf("monitor ids: %v %v", ids, err)
	}
	for id, want := range map[int32]bool{a.ID: true, b.ID: false} {
		if public, err := s.IsMonitorPublic(ctx, id); err != nil || public != want {
			t.Errorf("monitor %d public: %v %v", id, public, err)
		}
	}

	if err := s.SetStatusPageLogo(ctx, saved.ID, []byte("png")); err != nil {
		t.Fatal(err)
	}
	if image, _, err := s.StatusPageLogo(ctx, saved.ID); err != nil || string(image) != "png" {
		t.Fatalf("logo: %q %v", image, err)
	}

	// Deleting a monitor takes it off the page.
	if err := s.DeleteMonitor(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if groups, _ := s.StatusPageGroups(ctx, saved.ID); len(groups) != 1 || len(groups[0].Monitors) != 0 {
		t.Fatalf("after deleting the monitor: %+v", groups)
	}

	if err := s.DeleteStatusPage(ctx, "renamed"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStatusPage(ctx, "renamed"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
	if _, _, err := s.StatusPageLogo(ctx, saved.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("logo after delete: %v", err)
	}
	if domains, _ := s.StatusPageDomains(ctx); len(domains) != 0 {
		t.Fatalf("domains after delete: %v", domains)
	}
}

func TestIncidents(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	page, err := s.CreateStatusPage(ctx, "P", "p")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateStatusPage(ctx, "O", "o")
	if err != nil {
		t.Fatal(err)
	}

	var created []Incident
	for i := range 5 {
		incident, err := s.PostIncident(ctx, Incident{StatusPageID: page.ID, Title: "t", Content: "c", Style: "info"})
		if err != nil {
			t.Fatal(err)
		}
		if !incident.Pin || !incident.Active || incident.UpdatedAt != nil {
			t.Fatalf("new incident %d: %+v", i, incident)
		}
		created = append(created, incident)
	}
	// Posting with an id replaces that incident, but only on its own page.
	updated, err := s.PostIncident(ctx, Incident{ID: created[0].ID, StatusPageID: page.ID, Title: "new", Content: "c", Style: "danger"})
	if err != nil || updated.ID != created[0].ID || updated.Title != "new" || updated.UpdatedAt == nil {
		t.Fatalf("repost: %+v %v", updated, err)
	}
	elsewhere, err := s.PostIncident(ctx, Incident{ID: created[1].ID, StatusPageID: other.ID, Title: "x", Content: "c", Style: "info"})
	if err != nil || elsewhere.ID == created[1].ID || elsewhere.StatusPageID != other.ID {
		t.Fatalf("repost on another page made: %+v %v", elsewhere, err)
	}

	// Pages of two, newest first, which must not skip incidents created in the same instant.
	var seen []int32
	var cursor *IncidentCursor
	for {
		list, total, more, err := s.IncidentHistory(ctx, page.ID, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		if total != 5 {
			t.Fatalf("total %d", total)
		}
		for _, i := range list {
			seen = append(seen, i.ID)
		}
		if !more {
			break
		}
		last := list[len(list)-1]
		cursor = &IncidentCursor{CreatedAt: last.CreatedAt, ID: last.ID}
	}
	if len(seen) != 5 || seen[0] != created[4].ID || seen[4] != created[0].ID {
		t.Fatalf("history order: %v", seen)
	}

	if _, err := s.EditIncident(ctx, Incident{ID: created[1].ID, StatusPageID: other.ID, Title: "x", Content: "y", Style: "info"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit on another page: %v", err)
	}
	if edited, err := s.EditIncident(ctx, Incident{ID: created[1].ID, StatusPageID: page.ID, Title: "x", Content: "y", Style: "warning"}); err != nil || edited.Pin {
		t.Fatalf("edit: %+v %v", edited, err)
	}
	if resolved, err := s.ResolveIncident(ctx, page.ID, created[2].ID); err != nil || resolved.Active || resolved.Pin {
		t.Fatalf("resolve: %+v %v", resolved, err)
	}
	if err := s.DeleteIncident(ctx, page.ID, created[3].ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIncident(ctx, other.ID, created[4].ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete on another page: %v", err)
	}
	pinned, err := s.PinnedIncidents(ctx, page.ID)
	if err != nil || len(pinned) != 2 || pinned[0].ID != created[4].ID || pinned[1].ID != created[0].ID {
		t.Fatalf("pinned: %+v %v", pinned, err)
	}
	if err := s.UnpinIncidents(ctx, page.ID); err != nil {
		t.Fatal(err)
	}
	if pinned, _ := s.PinnedIncidents(ctx, page.ID); len(pinned) != 0 {
		t.Fatalf("pinned after unpin: %+v", pinned)
	}
	if pinned, _ := s.PinnedIncidents(ctx, other.ID); len(pinned) != 1 {
		t.Fatalf("the other page's: %+v", pinned)
	}
}

func TestMaintenanceStatusPages(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	w, err := s.CreateMaintenance(ctx, maintenance.Window{Title: "W", Strategy: maintenance.StrategyManual, Active: true, Timezone: maintenance.SameAsServer})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := s.CreateStatusPage(ctx, "B page", "a")
	b, _ := s.CreateStatusPage(ctx, "A page", "b")
	if err := s.SetMaintenanceStatusPages(ctx, w.ID, []int32{a.ID, b.ID, b.ID, 9999}); err != nil {
		t.Fatal(err)
	}
	pages, err := s.MaintenanceStatusPages(ctx, w.ID)
	if err != nil || len(pages) != 2 || pages[0].Title != "A page" {
		t.Fatalf("pages: %+v %v", pages, err)
	}
	if ids, err := s.StatusPageMaintenanceIDs(ctx, a.ID); err != nil || len(ids) != 1 || ids[0] != w.ID {
		t.Fatalf("page's windows: %v %v", ids, err)
	}
	if err := s.SetMaintenanceStatusPages(ctx, 9999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing window: %v", err)
	}
	if err := s.DeleteStatusPage(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if pages, _ := s.MaintenanceStatusPages(ctx, w.ID); len(pages) != 1 {
		t.Fatalf("after deleting a page: %+v", pages)
	}
}

func TestRecentBeatsAndUptimeOver(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m, err := s.CreateMonitor(ctx, member("m", nil))
	if err != nil {
		t.Fatal(err)
	}
	quiet, err := s.CreateMonitor(ctx, member("quiet", nil))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	var beats []Beat
	for i := range 6 {
		status := monitor.StatusUp
		if i == 5 {
			status = monitor.StatusDown
		}
		beats = append(beats, Beat{MonitorID: m.ID, Time: now.Add(time.Duration(i-6) * time.Second), Status: status, Ping: ptr(float64(10 * (i + 1)))})
	}
	// Two days ago, which only the daily numbers and the fallback see.
	beats = append(beats, Beat{MonitorID: quiet.ID, Time: now.Add(-49 * time.Hour), Status: monitor.StatusUp, Ping: ptr(7)})
	if _, err := s.InsertBeats(ctx, beats); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}

	recent, err := s.RecentBeats(ctx, []int32{m.ID, quiet.ID, 9999}, 3)
	if err != nil || len(recent[m.ID]) != 3 || len(recent[quiet.ID]) != 1 || recent[m.ID][2].Status != monitor.StatusDown {
		t.Fatalf("recent: %+v %v", recent, err)
	}

	for _, unit := range []string{"minute", "hour", "day"} {
		got, err := s.UptimeOver(ctx, m.ID, unit, 2)
		if err != nil {
			t.Fatal(unit, err)
		}
		if got.Up != 5 || got.Down != 1 || got.PingCount != 5 || got.PingSum != 150 {
			t.Errorf("%s: %+v", unit, got)
		}
	}
	// Nothing in the last hour, so the newest hour with beats counts.
	if got, err := s.UptimeOver(ctx, quiet.ID, "hour", 1); err != nil || got.Up != 1 || got.PingSum != 7 {
		t.Errorf("fallback: %+v %v", got, err)
	}
	if got, err := s.UptimeOver(ctx, 9999, "day", 30); err != nil || got.Up+got.Down != 0 {
		t.Errorf("no beats: %+v %v", got, err)
	}
}

func TestMonitorSettingsRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := monitor.New()
	m.Name, m.Type = "k", monitor.TypeKafka
	m.KafkaBrokers, m.KafkaTopic, m.KafkaSASL = []string{"k:9092"}, "t", &monitor.KafkaSASL{Mechanism: "plain", Username: "u", Password: "p"}
	created, err := s.CreateMonitor(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMonitor(ctx, created.ID)
	if err != nil || got.KafkaTopic != "t" || len(got.KafkaBrokers) != 1 || got.KafkaSASL == nil || got.KafkaSASL.Password != "p" {
		t.Fatalf("read back: %+v %v", got.Settings, err)
	}
	got.Type, got.KafkaBrokers, got.KafkaSASL = monitor.TypeMQTT, nil, nil
	got.Hostname, got.Port, got.MQTTTopic = "b", 1883, "x/#"
	updated, err := s.UpdateMonitor(ctx, got)
	if err != nil || updated.MQTTTopic != "x/#" || updated.KafkaSASL != nil {
		t.Fatalf("update: %+v %v", updated.Settings, err)
	}
}
