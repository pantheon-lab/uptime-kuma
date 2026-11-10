package store

import (
	"context"
	"errors"
	"slices"
	"testing"
)

func TestBulkEditMonitors(t *testing.T) {
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
	n, err := s.CreateNotification(ctx, Notification{Name: "n", Type: "webhook", Config: map[string]any{}, Active: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	tag, err := s.CreateTag(ctx, "region", "#fff")
	if err != nil {
		t.Fatal(err)
	}
	ids := []int32{a.ID, b.ID}

	// A notification deleted meanwhile changes nothing: not the settings either.
	_, err = s.BulkEditMonitors(ctx, BulkEdit{MonitorIDs: ids, Settings: []BulkSettings{{ID: a.ID, Interval: 99, MaxRetries: 1}},
		AddNotifications: []int32{n.ID, 9999}})
	if !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("missing notification: %v", err)
	}
	if got, _ := s.GetMonitor(ctx, a.ID); got.Interval == 99 {
		t.Fatal("settings changed though the edit failed")
	}
	// A monitor deleted meanwhile, likewise.
	if _, err := s.BulkEditMonitors(ctx, BulkEdit{MonitorIDs: []int32{a.ID, 9999}, AddTags: []TagValue{{TagID: tag.ID, Value: "eu"}}}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing monitor: %v", err)
	}
	if tags, _ := s.AllMonitorTags(ctx); len(tags[a.ID]) != 0 {
		t.Fatal("a tag was added though the edit failed")
	}

	updated, err := s.BulkEditMonitors(ctx, BulkEdit{MonitorIDs: ids,
		Settings:         []BulkSettings{{ID: a.ID, Interval: 45, RetryInterval: 15, MaxRetries: 2}, {ID: b.ID, Interval: 45, MaxRetries: 2}},
		AddNotifications: []int32{n.ID}, AddTags: []TagValue{{TagID: tag.ID, Value: "eu"}}})
	if err != nil || len(updated) != 2 || updated[0].Interval != 45 || updated[0].RetryInterval != 15 {
		t.Fatalf("edit: %+v %v", updated, err)
	}
	if got, _ := s.GetMonitor(ctx, b.ID); got.Name != "b" || got.Interval != 45 || got.MaxRetries != 2 {
		t.Fatalf("b: %+v", got)
	}
	links, _ := s.AllMonitorNotificationIDs(ctx)
	tags, _ := s.AllMonitorTags(ctx)
	for _, id := range ids {
		if !slices.Equal(links[id], []int32{n.ID}) || len(tags[id]) != 1 || tags[id][0].Value != "eu" {
			t.Fatalf("monitor %d: %v %+v", id, links[id], tags[id])
		}
	}
	// Removing takes them off; doing it twice changes nothing more.
	for range 2 {
		if _, err := s.BulkEditMonitors(ctx, BulkEdit{MonitorIDs: ids, RemoveNotifications: []int32{n.ID}, RemoveTags: []int32{tag.ID}}); err != nil {
			t.Fatal(err)
		}
	}
	links, _ = s.AllMonitorNotificationIDs(ctx)
	tags, _ = s.AllMonitorTags(ctx)
	if len(links) != 0 || len(tags) != 0 {
		t.Fatalf("left: %v %v", links, tags)
	}
}
