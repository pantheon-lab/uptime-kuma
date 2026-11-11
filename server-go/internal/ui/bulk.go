package ui

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	// maxBulkMonitors is how many monitors one bulk edit, import or export may touch.
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

	edit := store.BulkEdit{
		MonitorIDs: ids, AddNotifications: changes.AddNotifications,
		RemoveNotifications: changes.RemoveNotifications, RemoveTags: changes.RemoveTags,
	}
	if changes.changesSettings() {
		for _, m := range monitors {
			edit.Settings = append(edit.Settings, store.BulkSettings{ID: m.ID, Interval: m.Interval, RetryInterval: m.RetryInterval, MaxRetries: m.MaxRetries})
		}
	}
	for _, t := range changes.AddTags {
		edit.AddTags = append(edit.AddTags, store.TagValue{TagID: t.TagID, Value: t.Value})
	}
	// All of them or none: a monitor, notification or tag deleted meanwhile changes nothing.
	updated, err := s.store.BulkEditMonitors(ctx, edit)
	if errors.Is(err, store.ErrNotFound) || errors.Is(err, store.ErrInvalidNotification) {
		return nil, userErr(errors.New("A monitor, notification or tag was deleted meanwhile, so nothing was changed. Try again.")) //nolint:staticcheck // Shown to users.
	}
	if err != nil {
		return nil, err
	}
	for _, m := range updated {
		s.scheduler.Upsert(m)
	}
	s.broadcastMonitors(ctx, ids...)
	s.log.Info("monitors changed together from the web UI", "monitors", len(ids))
	return ok("msg", "Changed "+monitorCount(len(ids))+".", "count", len(ids)), nil
}

// setMonitorsActive pauses or resumes many monitors at once: setMonitorsActive(ids, active).
// Open pages get one update with all of them, and the monitors in the groups among them, rather
// than one per monitor, which made the list re-sort itself monitor by monitor.
func (s *Server) setMonitorsActive(ctx context.Context, args []json.RawMessage) (reply, error) {
	var ids []int32
	if err := arg(args, 0, &ids); err != nil {
		return nil, userErr(err)
	}
	var active bool
	if err := arg(args, 1, &active); err != nil {
		return nil, userErr(err)
	}
	ids = uniqueIDs(ids)
	switch {
	case len(ids) == 0:
		return nil, userErr(errors.New("no monitors were chosen"))
	case len(ids) > maxBulkMonitors:
		return nil, userErr(fmt.Errorf("at most %d monitors can be changed at once", maxBulkMonitors))
	}
	changed, err := s.store.SetMonitorsActive(ctx, ids, active)
	if err != nil {
		return nil, err
	}
	for _, m := range changed {
		s.scheduler.Upsert(m)
	}
	// Monitors show as paused while a group above them is, so those in the groups change too.
	send := make([]int32, 0, len(changed))
	seen := make(map[int32]bool, len(changed))
	for _, m := range changed {
		seen[m.ID] = true
		send = append(send, m.ID)
	}
	if tree, err := s.monitorTree(ctx); err != nil {
		s.log.Warn("find the monitors in the changed groups", "error", err)
	} else {
		for _, m := range changed {
			for _, id := range tree.descendants(m.ID) {
				if !seen[id] {
					seen[id] = true
					send = append(send, id)
				}
			}
		}
	}
	s.broadcastMonitors(ctx, send...)
	verb := "Paused"
	if active {
		verb = "Resumed"
	}
	s.log.Info("monitors "+strings.ToLower(verb)+" together from the web UI", "monitors", len(changed))
	return ok("msg", verb+" "+monitorCount(len(changed))+".", "count", len(changed)), nil
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
//
// A parentId names a group in the file by its id, as an export writes them, or else a group on
// this server. Groups in the file are added before the monitors in them.
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
	errs := make([]error, len(raws))
	// The rows by the id the file gives them
	byFileID := make(map[int32]int, len(raws))
	for i, raw := range raws {
		m, fileID, err := decodeImportedMonitor(raw)
		rows[i] = importRow{Row: i + 1, Name: m.Name, Type: m.Type}
		monitors[i], errs[i] = m, err
		if fileID > 0 {
			if first, taken := byFileID[fileID]; taken {
				errs[i] = firstErr(errs[i], fmt.Errorf("id %d is also row %d's", fileID, first+1))
			} else {
				byFileID[fileID] = i
			}
		}
	}

	// Push tokens are unique, so a token in use here, or twice in the file, would stop the import
	// part way.
	tokens := make(map[string]string, len(tree.byID))
	for _, m := range tree.byID {
		if m.PushToken != "" {
			tokens[m.PushToken] = fmt.Sprintf("monitor %q", m.Name)
		}
	}
	for i, m := range monitors {
		if errs[i] != nil || m.Type != monitor.TypePush || m.PushToken == "" {
			continue
		}
		if owner, taken := tokens[m.PushToken]; taken {
			errs[i] = fmt.Errorf("its pushToken is already %s's: remove it to get a new one", owner)
		} else {
			tokens[m.PushToken] = fmt.Sprintf("row %d", i+1)
		}
	}

	// parentRow is the row of each monitor's group when the group is in the file, or -1.
	parentRow := make([]int, len(raws))
	for i, m := range monitors {
		parentRow[i] = -1
		if errs[i] != nil || m.ParentID == nil {
			continue
		}
		if p, inFile := byFileID[*m.ParentID]; inFile {
			if monitors[p].Type != monitor.TypeGroup {
				errs[i] = fmt.Errorf("parentId %d is row %d, which isn't a group", *m.ParentID, p+1)
			} else {
				parentRow[i] = p
			}
		} else if parent, found := tree.byID[*m.ParentID]; !found || parent.Type != monitor.TypeGroup {
			errs[i] = fmt.Errorf("parentId %d isn't a group", *m.ParentID)
		}
	}
	depth := make([]int, len(raws))
	for i := range monitors {
		for p := parentRow[i]; p >= 0; p = parentRow[p] {
			depth[i]++
			if depth[i] > len(raws) {
				errs[i] = errors.New("its groups contain each other")
				break
			}
		}
	}

	invalid := 0
	for i, err := range errs {
		if err != nil {
			rows[i].Error = err.Error()
			invalid++
		}
	}
	if invalid > 0 {
		return reply{"ok": false, "msg": fmt.Sprintf("%d of %s can't be imported. Nothing was imported.", invalid, monitorCount(len(raws))), "rows": rows}, nil
	}
	if dryRun {
		return ok("rows", rows, "count", len(rows)), nil
	}

	// Groups first, so that each monitor's group exists when it's added.
	order := make([]int, len(raws))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return depth[a] - depth[b] })

	created := make([]int32, 0, len(monitors))
	defer func() {
		if len(created) > 0 {
			s.maintenance.Changed() // New monitors may be in a group under maintenance.
			s.broadcastMonitors(ctx, created...)
			s.log.Info("monitors imported from the web UI", "monitors", len(created))
		}
	}()
	for _, i := range order {
		m := monitors[i]
		if p := parentRow[i]; p >= 0 {
			id := rows[p].ID
			m.ParentID = &id
		}
		c, err := s.store.CreateMonitor(ctx, m)
		if errors.Is(err, store.ErrInvalidParent) || errors.Is(err, store.ErrPushTokenTaken) {
			rows[i].Error = err.Error()
			return reply{"ok": false, "msg": fmt.Sprintf("Row %d: %v. %s were imported before it.", i+1, err, capitalize(monitorCount(len(created)))), "rows": rows, "count": len(created)}, nil
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

// firstErr returns the first error that isn't nil.
func firstErr(first, second error) error {
	if first != nil {
		return first
	}
	return second
}

// capitalize makes "3 monitors" read as the start of a sentence; numbers are left as they are.
func capitalize(text string) string {
	if text == "" || text[0] < 'a' || text[0] > 'z' {
		return text
	}
	return string(text[0]-'a'+'A') + text[1:]
}

// decodeImportedMonitor reads a monitor as the REST API does: a misspelled field is an error,
// not a default. It returns the id the file gives the monitor, which only links monitors of
// the same file.
func decodeImportedMonitor(raw json.RawMessage) (monitor.Monitor, int32, error) {
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
		return m, 0, err
	}
	fileID := m.ID
	m.ID = 0
	m.Normalize()
	return m, fileID, m.Validate()
}

// exportOptions says what an export adds to the chosen monitors.
type exportOptions struct {
	// Groups adds the groups the monitors are in, up to the top. Without it, a monitor whose
	// group isn't exported loses its parentId.
	Groups bool `json:"groups"`
	// Children adds every monitor in the chosen groups, however deep.
	Children bool `json:"children"`
}

// exportMonitors returns monitors in the REST API's format, credentials included, for a file
// that importMonitors reads back: exportMonitors(ids, options). Groups come before the monitors
// in them, and each parentId is a monitor in the file, so the file is complete on its own.
func (s *Server) exportMonitors(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	var ids []int32
	if err := arg(args, 0, &ids); err != nil {
		return nil, userErr(err)
	}
	var options exportOptions
	if len(args) > 1 {
		if err := arg(args, 1, &options); err != nil {
			return nil, userErr(err)
		}
	}
	ids = uniqueIDs(ids)
	if len(ids) == 0 {
		return nil, userErr(errors.New("no monitors were chosen"))
	}
	tree, err := s.monitorTree(ctx)
	if err != nil {
		return nil, fmt.Errorf("load monitors to export: %w", err)
	}

	chosen := make(map[int32]bool, len(ids))
	for _, id := range ids {
		if _, found := tree.byID[id]; !found {
			return nil, userErr(fmt.Errorf("monitor %d doesn't exist", id))
		}
		chosen[id] = true
	}
	if options.Children {
		for _, id := range ids {
			if tree.byID[id].Type == monitor.TypeGroup {
				for _, child := range tree.descendants(id) {
					chosen[child] = true
				}
			}
		}
	}
	if options.Groups {
		for id := range maps.Clone(chosen) {
			seen := map[int32]bool{id: true}
			for p := tree.byID[id].ParentID; p != nil && !seen[*p]; p = tree.byID[*p].ParentID {
				if _, found := tree.byID[*p]; !found {
					break
				}
				seen[*p] = true
				chosen[*p] = true
			}
		}
	}
	if len(chosen) > maxBulkMonitors {
		return nil, userErr(fmt.Errorf("at most %d monitors can be exported at once", maxBulkMonitors))
	}

	// In the list's order: each group, then what's in it.
	out := make([]monitor.Monitor, 0, len(chosen))
	var walk func(parent *int32, seen map[int32]bool)
	walk = func(parent *int32, seen map[int32]bool) {
		var level []monitor.Monitor
		for id := range chosen {
			m := tree.byID[id]
			inLevel := (parent == nil && (m.ParentID == nil || !chosen[*m.ParentID])) ||
				(parent != nil && m.ParentID != nil && *m.ParentID == *parent)
			if inLevel && !seen[id] {
				level = append(level, m)
			}
		}
		slices.SortFunc(level, func(a, b monitor.Monitor) int {
			return cmp.Or(strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)), cmp.Compare(a.ID, b.ID))
		})
		for _, m := range level {
			seen[m.ID] = true
			if m.ParentID != nil && !chosen[*m.ParentID] {
				m.ParentID = nil
			}
			out = append(out, m)
			walk(&m.ID, seen)
		}
	}
	walk(nil, map[int32]bool{})

	s.log.Info("monitors exported from the web UI", "monitors", len(out), "user", c.username)
	return ok("monitors", out, "count", len(out)), nil
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
