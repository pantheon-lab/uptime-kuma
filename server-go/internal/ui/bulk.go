package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	// maxBulkMonitors is how many monitors one bulk edit or import may touch.
	maxBulkMonitors = 1000
	// maxTestRun is how long the form's test may take: less than the event's own time limit.
	maxTestRun = 25 * time.Second
)

// bulkChanges is what a bulk edit changes. Fields left out stay as each monitor has them.
type bulkChanges struct {
	Interval      *int32 `json:"interval"`
	RetryInterval *int32 `json:"retryInterval"`
	MaxRetries    *int32 `json:"maxRetries"`

	AddNotifications    []int32 `json:"addNotifications"`
	RemoveNotifications []int32 `json:"removeNotifications"`

	AddTags []struct {
		TagID int32  `json:"tagId"`
		Value string `json:"value"`
	} `json:"addTags"`
	// RemoveTags takes each tag off with all its values.
	RemoveTags []int32 `json:"removeTags"`
}

func (c bulkChanges) changesSettings() bool {
	return c.Interval != nil || c.RetryInterval != nil || c.MaxRetries != nil
}

func (c bulkChanges) empty() bool {
	return !c.changesSettings() && len(c.AddNotifications) == 0 && len(c.RemoveNotifications) == 0 &&
		len(c.AddTags) == 0 && len(c.RemoveTags) == 0
}

// bulkEditMonitors changes the interval, retries, notifications or tags of many monitors at once:
// bulkEditMonitors(ids, changes). Every monitor is checked first, so a value one of them refuses
// changes none of them.
func (s *Server) bulkEditMonitors(ctx context.Context, args []json.RawMessage) (reply, error) {
	var ids []int32
	if err := arg(args, 0, &ids); err != nil {
		return nil, userErr(err)
	}
	var changes bulkChanges
	if err := arg(args, 1, &changes); err != nil {
		return nil, userErr(err)
	}
	ids = uniqueIDs(ids)
	switch {
	case len(ids) == 0:
		return nil, userErr(errors.New("no monitors were chosen"))
	case len(ids) > maxBulkMonitors:
		return nil, userErr(fmt.Errorf("at most %d monitors can be changed at once", maxBulkMonitors))
	case changes.empty():
		return nil, userErr(errors.New("nothing to change"))
	}
	for _, id := range changes.AddNotifications {
		if slices.Contains(changes.RemoveNotifications, id) {
			return nil, userErr(errors.New("a notification can't be both added and removed"))
		}
	}
	for _, t := range changes.AddTags {
		if slices.Contains(changes.RemoveTags, t.TagID) {
			return nil, userErr(errors.New("a tag can't be both added and removed"))
		}
		if len(t.Value) > maxTagField {
			return nil, userErr(errors.New("the tag's value must be at most 200 characters"))
		}
	}
	if err := s.checkBulkReferences(ctx, changes); err != nil {
		return nil, err
	}

	// Each monitor with its new settings, all checked before anything is written.
	monitors := make([]monitor.Monitor, 0, len(ids))
	for _, id := range ids {
		m, err := s.store.GetMonitor(ctx, id)
		if errors.Is(err, store.ErrNotFound) {
			return nil, userErr(fmt.Errorf("monitor %d doesn't exist", id))
		}
		if err != nil {
			return nil, err
		}
		if changes.Interval != nil {
			m.Interval = *changes.Interval
		}
		if changes.RetryInterval != nil {
			m.RetryInterval = *changes.RetryInterval
		}
		if changes.MaxRetries != nil {
			m.MaxRetries = *changes.MaxRetries
		}
		// As the form: a retry interval equal to the interval means the same as none.
		if m.RetryInterval == m.Interval {
			m.RetryInterval = 0
		}
		if err := m.Validate(); err != nil {
			return nil, userErr(fmt.Errorf("%s: %w", m.Name, err))
		}
		monitors = append(monitors, m)
	}

	linked, err := s.store.AllMonitorNotificationIDs(ctx)
	if err != nil {
		return nil, err
	}
	tags, err := s.store.AllMonitorTags(ctx)
	if err != nil {
		return nil, err
	}
	for _, m := range monitors {
		if changes.changesSettings() {
			updated, err := s.store.UpdateMonitor(ctx, m)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
			s.scheduler.Upsert(updated)
		}
		if len(changes.AddNotifications) > 0 || len(changes.RemoveNotifications) > 0 {
			next := slices.DeleteFunc(slices.Clone(linked[m.ID]), func(id int32) bool {
				return slices.Contains(changes.RemoveNotifications, id)
			})
			for _, id := range changes.AddNotifications {
				if !slices.Contains(next, id) {
					next = append(next, id)
				}
			}
			if err := s.store.SetMonitorNotifications(ctx, m.ID, next); err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
		}
		for _, tag := range tags[m.ID] {
			if slices.Contains(changes.RemoveTags, tag.TagID) {
				if err := s.store.DeleteMonitorTag(ctx, tag.TagID, m.ID, tag.Value); err != nil {
					return nil, fmt.Errorf("%s: %w", m.Name, err)
				}
			}
		}
		for _, tag := range changes.AddTags {
			if err := s.store.AddMonitorTag(ctx, tag.TagID, m.ID, tag.Value); err != nil {
				return nil, fmt.Errorf("%s: %w", m.Name, err)
			}
		}
	}
	s.broadcastMonitors(ctx, ids...)
	s.log.Info("monitors changed together from the web UI", "monitors", len(ids))
	return ok("msg", "Changed "+monitorCount(len(ids))+".", "count", len(ids)), nil
}

// checkBulkReferences refuses notifications and tags that don't exist.
func (s *Server) checkBulkReferences(ctx context.Context, changes bulkChanges) error {
	if len(changes.AddNotifications) > 0 {
		notifications, err := s.store.ListNotifications(ctx)
		if err != nil {
			return err
		}
		for _, id := range changes.AddNotifications {
			if !slices.ContainsFunc(notifications, func(n store.Notification) bool { return n.ID == id }) {
				return userErr(fmt.Errorf("notification %d doesn't exist", id))
			}
		}
	}
	if len(changes.AddTags) > 0 {
		all, err := s.store.ListTags(ctx)
		if err != nil {
			return err
		}
		for _, t := range changes.AddTags {
			if !slices.ContainsFunc(all, func(tag store.Tag) bool { return tag.ID == t.TagID }) {
				return userErr(fmt.Errorf("tag %d doesn't exist", t.TagID))
			}
		}
	}
	return nil
}

// monitorCount says "1 monitor" or "3 monitors".
func monitorCount(n int) string {
	if n == 1 {
		return "1 monitor"
	}
	return fmt.Sprintf("%d monitors", n)
}

func uniqueIDs(ids []int32) []int32 {
	out := make([]int32, 0, len(ids))
	for _, id := range ids {
		if id > 0 && !slices.Contains(out, id) {
			out = append(out, id)
		}
	}
	return out
}

// importRow is what an import says about one monitor of the file.
type importRow struct {
	Row   int    `json:"row"`
	Name  string `json:"name"`
	Type  string `json:"type"`
	Error string `json:"error,omitempty"`
	ID    int32  `json:"id,omitempty"`
}

// importMonitors adds monitors, each in the REST API's format: importMonitors(monitors, dryRun).
// Nothing is added unless every one of them is valid, and a dry run only checks them. Monitors
// get the default notifications, as from the REST API.
func (s *Server) importMonitors(ctx context.Context, args []json.RawMessage) (reply, error) {
	var raws []json.RawMessage
	if err := arg(args, 0, &raws); err != nil {
		return nil, userErr(err)
	}
	var dryRun bool
	if err := arg(args, 1, &dryRun); err != nil {
		return nil, userErr(err)
	}
	switch {
	case len(raws) == 0:
		return nil, userErr(errors.New("the file has no monitors"))
	case len(raws) > maxBulkMonitors:
		return nil, userErr(fmt.Errorf("at most %d monitors can be imported at once", maxBulkMonitors))
	}
	tree, err := s.monitorTree(ctx)
	if err != nil {
		return nil, err
	}

	rows := make([]importRow, len(raws))
	monitors := make([]monitor.Monitor, len(raws))
	invalid := 0
	for i, raw := range raws {
		m, err := decodeImportedMonitor(raw)
		rows[i] = importRow{Row: i + 1, Name: m.Name, Type: m.Type}
		if err == nil && m.ParentID != nil {
			if parent, found := tree.byID[*m.ParentID]; !found || parent.Type != monitor.TypeGroup {
				err = fmt.Errorf("parentId %d isn't a group", *m.ParentID)
			}
		}
		if err != nil {
			rows[i].Error = err.Error()
			invalid++
		}
		monitors[i] = m
	}
	if invalid > 0 {
		return reply{"ok": false, "msg": fmt.Sprintf("%d of %s can't be imported. Nothing was imported.", invalid, monitorCount(len(raws))), "rows": rows}, nil
	}
	if dryRun {
		return ok("rows", rows, "count", len(rows)), nil
	}

	created := make([]int32, 0, len(monitors))
	defer func() {
		if len(created) > 0 {
			s.maintenance.Changed() // New monitors may be in a group under maintenance.
			s.broadcastMonitors(ctx, created...)
			s.log.Info("monitors imported from the web UI", "monitors", len(created))
		}
	}()
	for i, m := range monitors {
		c, err := s.store.CreateMonitor(ctx, m)
		if errors.Is(err, store.ErrInvalidParent) || errors.Is(err, store.ErrPushTokenTaken) {
			rows[i].Error = err.Error()
			return reply{"ok": false, "msg": fmt.Sprintf("Row %d: %v. The %s before it were imported.", i+1, err, monitorCount(len(created))), "rows": rows, "count": len(created)}, nil
		}
		if err != nil {
			return nil, err
		}
		if err := s.store.LinkDefaultNotifications(ctx, c.ID); err != nil {
			s.log.Error("link default notifications", "monitor", c.ID, "error", err)
		}
		s.scheduler.Upsert(c)
		rows[i].ID = c.ID
		created = append(created, c.ID)
	}
	return ok("msg", "Imported "+monitorCount(len(created))+".", "rows", rows, "count", len(created)), nil
}

// decodeImportedMonitor reads a monitor as the REST API does: a misspelled field is an error,
// not a default.
func decodeImportedMonitor(raw json.RawMessage) (monitor.Monitor, error) {
	m := monitor.New()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		// The name, if there is one, says which monitor this is.
		var named struct {
			Name string `json:"name"`
			Type string `json:"type"`
		}
		_ = json.Unmarshal(raw, &named)
		m.Name, m.Type = named.Name, named.Type
		return m, err
	}
	m.ID = 0
	m.Normalize()
	return m, m.Validate()
}

// testMonitor runs one check of the Edit Monitor form's monitor, saved or not, and stores nothing:
// testMonitor(monitor). It answers the status the check gives, ignoring retries.
func (s *Server) testMonitor(ctx context.Context, args []json.RawMessage) (reply, error) {
	if s.opts.Checker == nil {
		return notSupported("testMonitor"), nil
	}
	if len(args) == 0 {
		return nil, userErr(errors.New("missing monitor"))
	}
	m, _, _, err := parseMonitor(args[0])
	if err != nil {
		return nil, userErr(err)
	}
	if m.Type == monitor.TypePush {
		return nil, userErr(errors.New("a push monitor waits for pushes, so there is nothing to check"))
	}
	if m.EffectiveTimeout() > maxTestRun {
		m.Timeout = maxTestRun.Seconds()
	}
	// The result as the first check of a new monitor, which has no retries left to use.
	m.MaxRetries = 0
	started := s.now()
	result := s.opts.Checker.Run(check.DryRun(ctx), m)
	out := m.Next(nil, result)
	r := ok("status", out.Status, "msg", out.Msg, "durationMs", s.now().Sub(started).Milliseconds())
	if out.Ping != nil {
		r["ping"] = *out.Ping
	}
	return r, nil
}
