package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/conditions"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	historyBeats    = 100
	maxPeriodHours  = 24 * 365
	maxBeatsInReply = 20000
	maxEventsPage   = 1000
	testTimeout     = 30 * time.Second
)

type reply = map[string]any

func ok(fields ...any) reply {
	r := reply{"ok": true}
	for i := 0; i+1 < len(fields); i += 2 {
		r[fields[i].(string)] = fields[i+1]
	}
	return r
}

func fail(err error) reply { return reply{"ok": false, "msg": err.Error()} }

func okI18n(msg string, fields ...any) reply {
	r := ok(fields...)
	r["msg"], r["msgi18n"] = msg, true
	return r
}

func notSupported(event string) reply {
	return reply{"ok": false, "msg": fmt.Sprintf("This isn't supported by this server yet (%s).", event)}
}

var errNotLoggedIn = errors.New("You are not logged in.")

// emptyReplies answer events that the UI sends on its own when a page opens, for features
// this service doesn't have yet. An error would show a toast on every page load.
var emptyReplies = map[string]func() any{
	"getAPIKeyList":   func() any { return ok() },
	"getDatabaseSize": func() any { return ok("size", 0) },
	"checkApprise":    func() any { return false },
}

// arg decodes argument i into v. A missing argument leaves v as it is.
func arg(args []json.RawMessage, i int, v any) error {
	if i >= len(args) {
		return nil
	}
	if err := json.Unmarshal(args[i], v); err != nil {
		return fmt.Errorf("invalid argument %d: %w", i+1, err)
	}
	return nil
}

// idArg reads a monitor or notification id, which the UI sends as a number or a string.
func idArg(args []json.RawMessage, i int) (int32, error) {
	var id flexInt
	if err := arg(args, i, &id); err != nil {
		return 0, err
	}
	if !id.set || id.value <= 0 || id.value > 1<<31-1 {
		return 0, errors.New("invalid id")
	}
	return int32(id.value), nil
}

// onConnect sends what the UI expects when a stream opens: info, whether setup is needed,
// and either the logged-in user's data or loginRequired.
func (s *Server) onConnect(c *conn, login *userInfo) {
	ctx, cancel := context.WithTimeout(c.ctx, handlerTimeout)
	defer cancel()
	c.emit("info", s.info(ctx, false))
	if need, err := s.needSetup(ctx); err != nil {
		s.log.Warn("check setup", "error", err)
	} else if need {
		c.emit("setup")
	}
	if login == nil {
		c.emit("loginRequired")
		return
	}
	c.emit("session", login.username)
	s.setLogin(c, login)
	s.afterLogin(ctx, c)
}

func (s *Server) afterLogin(ctx context.Context, c *conn) {
	if err := s.sendMonitorList(ctx, c); err != nil {
		s.log.Error("send monitor list", "error", err)
	}
	c.emit("info", s.info(ctx, true))
	if list, err := s.maintenanceList(ctx); err != nil {
		s.log.Error("send maintenance list", "error", err)
	} else {
		c.emit("maintenanceList", list)
	}
	if err := s.sendNotificationList(ctx, c); err != nil {
		s.log.Error("send notification list", "error", err)
	}
	c.emit("proxyList", []any{})
	c.emit("dockerHostList", []any{})
	c.emit("remoteBrowserList", []any{})
	c.emit("monitorTypeList", monitorTypeList())
	if s.pages == nil {
		c.emit("statusPageList", map[string]any{})
		return
	}
	if list, err := s.statusPageList(ctx); err != nil {
		s.log.Error("send status page list", "error", err)
	} else {
		c.emit("statusPageList", list)
	}
}

// monitorTypeList tells the form which types take conditions, and on what.
func monitorTypeList() map[string]any {
	out := map[string]any{}
	for _, kind := range monitor.Types {
		variables, uses := monitor.ConditionVariables[kind]
		if variables == nil {
			variables = []conditions.Variable{}
		}
		out[kind] = map[string]any{"supportsConditions": uses, "conditionVariables": variables}
	}
	return out
}

func (s *Server) info(ctx context.Context, full bool) map[string]any {
	baseURL := s.opts.BaseURL
	if settings, err := s.store.Settings(ctx); err == nil {
		var saved string
		if json.Unmarshal(settings["primaryBaseURL"], &saved) == nil && saved != "" {
			baseURL = saved
		}
	}
	_, offset := s.now().In(s.opts.Location).Zone()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	info := map[string]any{
		"primaryBaseURL":       baseURL,
		"serverTimezone":       s.opts.Location.String(),
		"serverTimezoneOffset": fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60),
	}
	if full {
		info["version"] = s.opts.Version
		info["isContainer"] = true
		info["dbType"] = "postgres"
		info["runtime"] = map[string]any{"platform": "linux", "arch": ""}
	}
	return info
}

func (s *Server) monitorTree(ctx context.Context) (*monitorTree, error) {
	monitors, err := s.store.ListMonitors(ctx)
	if err != nil {
		return nil, err
	}
	links, err := s.store.AllMonitorNotificationIDs(ctx)
	if err != nil {
		return nil, err
	}
	tree := newMonitorTree(monitors, links)
	if tree.tags, err = s.store.AllMonitorTags(ctx); err != nil {
		return nil, err
	}
	tree.underMaintenance = s.maintenance.UnderMaintenanceSet()
	return tree, nil
}

func (s *Server) sendMonitorList(ctx context.Context, c *conn) error {
	tree, err := s.monitorTree(ctx)
	if err != nil {
		return err
	}
	c.emit("monitorList", tree.listJSON())
	return nil
}

// broadcastMonitors sends the current state of the given monitors to every logged-in browser.
func (s *Server) broadcastMonitors(ctx context.Context, ids ...int32) {
	tree, err := s.monitorTree(ctx)
	if err != nil {
		s.log.Error("send monitor update", "error", err)
		return
	}
	update := map[string]any{}
	for _, id := range ids {
		if m, found := tree.byID[id]; found {
			update[strconv.Itoa(int(id))] = tree.monitorJSON(m)
		}
	}
	if len(update) > 0 {
		s.broadcast(encodeMessage("updateMonitorIntoList", update))
	}
}

func (s *Server) sendNotificationList(ctx context.Context, c *conn) error {
	list, err := s.notificationList(ctx)
	if err != nil {
		return err
	}
	c.emit("notificationList", list)
	return nil
}

func (s *Server) notificationList(ctx context.Context) ([]map[string]any, error) {
	notifications, err := s.store.ListNotifications(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, len(notifications))
	for i, n := range notifications {
		list[i] = notificationJSON(n)
	}
	return list, nil
}

func (s *Server) broadcastNotificationList(ctx context.Context) {
	list, err := s.notificationList(ctx)
	if err != nil {
		s.log.Error("send notification list", "error", err)
		return
	}
	s.broadcast(encodeMessage("notificationList", list))
}

func (s *Server) stats(ctx context.Context) (map[string]any, error) {
	rows, err := s.store.Stats(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(rows))
	for _, row := range rows {
		out[strconv.Itoa(int(row.MonitorID))] = map[string]any{
			"avgPing":   row.AvgPing,
			"uptime24":  row.Uptime24h,
			"uptime720": row.Uptime30d,
			"uptime1y":  row.Uptime1y,
		}
	}
	return out, nil
}

// sendDashboardLists sends the newest beat of each monitor and the uptime numbers. The UI asks
// for them once it has drawn the monitor list, so that the list appears first.
func (s *Server) sendDashboardLists(ctx context.Context, c *conn) {
	beats, err := s.store.LatestBeatPerMonitor(ctx)
	if err != nil {
		s.log.Error("send latest beats", "error", err)
	} else {
		lists := make(map[string]any, len(beats))
		for _, b := range beats {
			lists[strconv.Itoa(int(b.MonitorID))] = []any{beatJSON(b)}
		}
		c.emit("heartbeatLists", lists)
	}
	stats, err := s.stats(ctx)
	if err != nil {
		s.log.Error("send stats", "error", err)
	} else {
		c.emit("stats", stats)
	}
	certificates, err := s.store.AllTLSInfo(ctx)
	if err != nil {
		s.log.Error("send certificates", "error", err)
		return
	}
	for id, info := range certificates {
		c.emit("certInfo", id, info)
	}
}

// BroadcastCertInfo tells open pages about a monitor's new certificate information. The UI
// expects it as JSON text.
func (s *Server) BroadcastCertInfo(monitorID int32, info string) {
	s.broadcast(encodeMessage("certInfo", monitorID, info))
}

// handle runs one event. hasReply is false for events this server doesn't know.
func (s *Server) handle(ctx context.Context, c *conn, event string, args []json.RawMessage) (any, bool) {
	switch event {
	case "needSetup":
		need, err := s.needSetup(ctx)
		if err != nil {
			s.log.Error("check setup", "error", err)
		}
		return need, true
	case "initServerTimezone":
		// The timezone comes from KUMA_TIMEZONE.
		return ok(), true
	}

	if !c.loggedIn() {
		return fail(errNotLoggedIn), true
	}
	if empty, found := emptyReplies[event]; found {
		return empty(), true
	}

	var r reply
	var err error
	switch event {
	case "dashboardRendered":
		c.rendered.Do(func() { s.sendDashboardLists(ctx, c) })
		return ok(), true
	case "getMonitorList":
		err = s.sendMonitorList(ctx, c)
		r = ok()
	case "getMonitor":
		r, err = s.getMonitor(ctx, args)
	case "add":
		r, err = s.addMonitor(ctx, args)
	case "editMonitor":
		r, err = s.editMonitor(ctx, args)
	case "pauseMonitor":
		r, err = s.setActive(ctx, args, false)
	case "resumeMonitor":
		r, err = s.setActive(ctx, args, true)
	case "deleteMonitor":
		r, err = s.deleteMonitor(ctx, args)
	case "getHeartbeatHistory":
		r, err = s.heartbeatHistory(ctx, args)
	case "getMonitorBeats":
		r, err = s.monitorBeats(ctx, args)
	case "getMonitorChartData":
		r, err = s.chartData(ctx, args)
	case "monitorImportantHeartbeatListCount":
		r, err = s.importantCount(ctx, args)
	case "monitorImportantHeartbeatListPaged":
		r, err = s.importantPage(ctx, args)
	case "clearEvents":
		r, err = s.clearEvents(ctx, args)
	case "getSettings":
		r, err = s.getSettings(ctx)
	case "setSettings":
		r, err = s.setSettings(ctx, c, args)
	case "addNotification":
		r, err = s.saveNotification(ctx, args)
	case "deleteNotification":
		r, err = s.deleteNotification(ctx, args)
	case "testNotification":
		r, err = s.testNotification(ctx, args)
	case "getTags":
		r, err = s.getTags(ctx)
	case "addTag":
		r, err = s.addTag(ctx, args)
	case "editTag":
		r, err = s.editTag(ctx, args)
	case "deleteTag":
		r, err = s.deleteTag(ctx, args)
	case "addMonitorTag":
		r, err = s.changeMonitorTag(ctx, args, s.store.AddMonitorTag, "successAdded")
	case "editMonitorTag":
		r, err = s.changeMonitorTag(ctx, args, s.store.UpdateMonitorTag, "successEdited")
	case "deleteMonitorTag":
		r, err = s.changeMonitorTag(ctx, args, s.store.DeleteMonitorTag, "successDeleted")
	case "getMaintenanceList":
		var list map[string]any
		if list, err = s.maintenanceList(ctx); err == nil {
			c.emit("maintenanceList", list)
			r = ok()
		}
	case "getMaintenance":
		r, err = s.getMaintenance(ctx, args)
	case "addMaintenance":
		r, err = s.saveMaintenance(ctx, args, false)
	case "editMaintenance":
		r, err = s.saveMaintenance(ctx, args, true)
	case "addMonitorMaintenance":
		r, err = s.setMaintenanceMonitors(ctx, args)
	case "addMaintenanceStatusPage":
		if s.pages == nil {
			r, err = s.noMaintenanceStatusPages(args)
		} else {
			r, err = s.setMaintenanceStatusPages(ctx, args)
		}
	case "getMaintenanceStatusPage":
		if s.pages == nil {
			r = ok("statusPages", []any{})
		} else {
			r, err = s.getMaintenanceStatusPages(ctx, args)
		}
	case "getMonitorMaintenance":
		r, err = s.getMaintenanceMonitors(ctx, args)
	case "pauseMaintenance":
		r, err = s.setMaintenanceActive(ctx, args, false)
	case "resumeMaintenance":
		r, err = s.setMaintenanceActive(ctx, args, true)
	case "deleteMaintenance":
		r, err = s.deleteMaintenance(ctx, args)
	case "getHolidayCountries":
		r = s.holidayCountries()
	case "getHolidays":
		r, err = s.listHolidays(args)
	case "getUpcomingHolidays":
		r, err = s.upcoming(args)
	case "addCustomHoliday":
		r, err = s.addCustomHoliday(ctx, args)
	case "removeHoliday":
		r, err = s.removeHoliday(ctx, args)
	case "restoreHoliday":
		r, err = s.restoreHoliday(ctx, args)
	default:
		if s.pages == nil {
			return nil, false
		}
		handler, found := statusPageEvents[event]
		if !found {
			return nil, false
		}
		r, err = handler(s, ctx, args)
	}
	if err != nil {
		if !errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrInvalidParent) &&
			!errors.Is(err, store.ErrPushTokenTaken) && !errors.Is(err, store.ErrSlugTaken) && !isUserError(err) {
			s.log.Error("realtime event failed", "event", event, "error", err)
			return fail(errors.New("internal error, see the server log")), true
		}
		return fail(err), true
	}
	return r, true
}

// userError is a problem with what the user sent, shown to them as it is.
type userError struct{ error }

func isUserError(err error) bool {
	var u userError
	return errors.As(err, &u)
}

func userErr(err error) error {
	if err == nil {
		return nil
	}
	return userError{err}
}

func (s *Server) getMonitor(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	tree, err := s.monitorTree(ctx)
	if err != nil {
		return nil, err
	}
	m, found := tree.byID[id]
	if !found {
		return nil, store.ErrNotFound
	}
	return ok("monitor", tree.monitorJSON(m)), nil
}

func (s *Server) addMonitor(ctx context.Context, args []json.RawMessage) (reply, error) {
	if len(args) == 0 {
		return nil, userErr(errors.New("missing monitor"))
	}
	m, notificationIDs, note, err := parseMonitor(args[0])
	if err != nil {
		return nil, userErr(err)
	}
	m.ID = 0
	created, err := s.store.CreateMonitor(ctx, m)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetMonitorNotifications(ctx, created.ID, notificationIDs); err != nil {
		return nil, userErr(fmt.Errorf("the monitor was added, but its notifications weren't: %w", err))
	}
	s.maintenance.Changed() // A new monitor may be in a group under maintenance.
	s.scheduler.Upsert(created)
	s.broadcastMonitors(ctx, created.ID)
	s.log.Info("monitor added from the web UI", "monitor", created.ID)
	r := okI18n("successAdded", "monitorID", created.ID)
	if note != "" {
		r["msg"], r["msgi18n"] = "Added. "+note, false
	}
	return r, nil
}

func (s *Server) editMonitor(ctx context.Context, args []json.RawMessage) (reply, error) {
	if len(args) == 0 {
		return nil, userErr(errors.New("missing monitor"))
	}
	m, notificationIDs, note, err := parseMonitor(args[0])
	if err != nil {
		return nil, userErr(err)
	}
	if m.ID <= 0 {
		return nil, userErr(errors.New("invalid id"))
	}
	current, err := s.store.GetMonitor(ctx, m.ID)
	if err != nil {
		return nil, err
	}
	// Pausing and resuming have their own events, so editing never changes it.
	m.Active = current.Active
	updated, err := s.store.UpdateMonitor(ctx, m)
	if err != nil {
		return nil, err
	}
	if err := s.store.SetMonitorNotifications(ctx, updated.ID, notificationIDs); err != nil {
		return nil, userErr(fmt.Errorf("the monitor was saved, but its notifications weren't: %w", err))
	}
	s.maintenance.Changed() // It may have moved to another group.
	s.scheduler.Upsert(updated)
	s.broadcastMonitors(ctx, updated.ID)
	s.log.Info("monitor changed from the web UI", "monitor", updated.ID)
	r := okI18n("Saved.", "monitorID", updated.ID)
	if note != "" {
		r["msg"], r["msgi18n"] = "Saved. "+note, false
	}
	return r, nil
}

func (s *Server) setActive(ctx context.Context, args []json.RawMessage, active bool) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	m, err := s.store.SetMonitorActive(ctx, id, active)
	if err != nil {
		return nil, err
	}
	s.scheduler.Upsert(m)
	// Children show as paused while their group is, so they're sent too.
	ids := []int32{id}
	if tree, err := s.monitorTree(ctx); err == nil {
		ids = append(ids, tree.descendants(id)...)
	}
	s.broadcastMonitors(ctx, ids...)
	if active {
		return okI18n("successResumed"), nil
	}
	return okI18n("successPaused"), nil
}

func (s *Server) deleteMonitor(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var deleteChildren bool
	if err := arg(args, 1, &deleteChildren); err != nil {
		return nil, userErr(err)
	}
	tree, err := s.monitorTree(ctx)
	if err != nil {
		return nil, err
	}
	if _, found := tree.byID[id]; !found {
		return nil, store.ErrNotFound
	}
	deleted := []int32{}
	if deleteChildren {
		for _, child := range tree.descendants(id) {
			if err := s.removeMonitor(ctx, child); err != nil {
				return nil, err
			}
			deleted = append(deleted, child)
		}
	}
	if err := s.removeMonitor(ctx, id); err != nil {
		return nil, err
	}
	deleted = append(deleted, id)
	for _, d := range deleted {
		s.broadcast(encodeMessage("deleteMonitorFromList", d))
	}
	if !deleteChildren {
		// The children stay, outside the group now.
		s.broadcastMonitors(ctx, tree.children[id]...)
	}
	s.log.Info("monitor deleted from the web UI", "monitor", id, "withChildren", deleteChildren)
	return okI18n("successDeleted"), nil
}

func (s *Server) removeMonitor(ctx context.Context, id int32) error {
	if err := s.scheduler.Remove(ctx, id); err != nil {
		return err
	}
	return s.store.DeleteMonitor(ctx, id)
}

func (s *Server) heartbeatHistory(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	beats, err := s.store.LatestBeats(ctx, id, historyBeats)
	if err != nil {
		return nil, err
	}
	// Oldest first, as the UI draws them.
	for i, j := 0, len(beats)-1; i < j; i, j = i+1, j-1 {
		beats[i], beats[j] = beats[j], beats[i]
	}
	return ok("data", beatsJSON(beats)), nil
}

func periodArg(args []json.RawMessage, i int) (int, error) {
	var period flexInt
	if err := arg(args, i, &period); err != nil {
		return 0, err
	}
	if !period.set || period.value <= 0 || period.value > maxPeriodHours {
		return 0, errors.New("Invalid period.")
	}
	return int(period.value), nil
}

func (s *Server) monitorBeats(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	hours, err := periodArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	beats, err := s.store.BeatsSince(ctx, id, s.now().Add(-time.Duration(hours)*time.Hour), maxBeatsInReply)
	if err != nil {
		return nil, err
	}
	return ok("data", beatsJSON(beats)), nil
}

func (s *Server) chartData(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	hours, err := periodArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	points, err := s.store.Chart(ctx, id, hours)
	if err != nil {
		return nil, err
	}
	return ok("data", chartJSON(points)), nil
}

// optionalMonitorID reads a monitor id that may be null, meaning every monitor.
func optionalMonitorID(args []json.RawMessage, i int) (*int32, error) {
	if i >= len(args) || strings.TrimSpace(string(args[i])) == "null" {
		return nil, nil
	}
	id, err := idArg(args, i)
	if err != nil {
		return nil, err
	}
	return &id, nil
}

func (s *Server) importantCount(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := optionalMonitorID(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	count, err := s.store.CountImportantBeats(ctx, id)
	if err != nil {
		return nil, err
	}
	return ok("count", count), nil
}

func (s *Server) importantPage(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := optionalMonitorID(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var offset, count flexInt
	if err := arg(args, 1, &offset); err != nil {
		return nil, userErr(err)
	}
	if err := arg(args, 2, &count); err != nil {
		return nil, userErr(err)
	}
	if offset.value < 0 || count.value < 0 || count.value > maxEventsPage {
		return nil, userErr(errors.New("invalid page"))
	}
	beats, err := s.store.ImportantBeats(ctx, id, int(offset.value), int(count.value))
	if err != nil {
		return nil, err
	}
	return ok("data", beatsJSON(beats)), nil
}

func (s *Server) clearEvents(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.store.ClearEvents(ctx, id); err != nil {
		return nil, err
	}
	return ok(), nil
}

func (s *Server) getSettings(ctx context.Context) (reply, error) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return nil, err
	}
	data := map[string]any{}
	for key, value := range settings {
		data[key] = value
	}
	data["serverTimezone"] = s.opts.Location.String()
	if _, found := data["primaryBaseURL"]; !found && s.opts.BaseURL != "" {
		data["primaryBaseURL"] = s.opts.BaseURL
	}
	return ok("data", data), nil
}

// readOnlySettings are decided by the server's configuration, so they aren't saved.
var readOnlySettings = map[string]bool{"serverTimezone": true}

func (s *Server) setSettings(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	var data map[string]json.RawMessage
	if err := arg(args, 0, &data); err != nil || data == nil {
		return nil, userErr(errors.New("invalid settings"))
	}
	var disableAuth bool
	_ = json.Unmarshal(data["disableAuth"], &disableAuth)
	if disableAuth {
		return nil, userErr(errors.New("turning off authentication isn't supported"))
	}
	toSave := maps.Clone(data)
	for key := range readOnlySettings {
		delete(toSave, key)
	}
	if err := s.store.SaveSettings(ctx, toSave); err != nil {
		return nil, err
	}
	s.cache.clear() // The entry page or trusting a proxy may have changed.
	c.emit("info", s.info(ctx, true))
	return okI18n("Saved."), nil
}

// notificationInput is the UI's notification form. Every field is also kept in config, as
// the Node.js backend did, because providers read their settings from there.
type notificationInput struct {
	Name          string `json:"name"`
	Type          string `json:"type"`
	IsDefault     bool   `json:"isDefault"`
	ApplyExisting bool   `json:"applyExisting"`
}

func parseNotification(raw json.RawMessage) (store.Notification, bool, error) {
	var in notificationInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return store.Notification{}, false, fmt.Errorf("invalid notification: %w", err)
	}
	var config map[string]any
	if err := json.Unmarshal(raw, &config); err != nil {
		return store.Notification{}, false, fmt.Errorf("invalid notification: %w", err)
	}
	if !notify.Supported(in.Type) {
		return store.Notification{}, false, fmt.Errorf("notification type %q isn't supported", in.Type)
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		return store.Notification{}, false, errors.New("name is required")
	}
	// One-time options, and fields that have their own columns.
	for _, key := range []string{"applyExisting", "id", "isDefault", "name", "type", "active"} {
		delete(config, key)
	}
	config["type"] = in.Type
	config["name"] = name
	return store.Notification{Name: name, Type: in.Type, Config: config, IsDefault: in.IsDefault, Active: true}, in.ApplyExisting, nil
}

func (s *Server) saveNotification(ctx context.Context, args []json.RawMessage) (reply, error) {
	if len(args) == 0 {
		return nil, userErr(errors.New("missing notification"))
	}
	n, applyToAll, err := parseNotification(args[0])
	if err != nil {
		return nil, userErr(err)
	}
	id, err := optionalMonitorID(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	var saved store.Notification
	if id == nil {
		saved, err = s.store.CreateNotification(ctx, n, applyToAll)
	} else {
		current, getErr := s.store.GetNotification(ctx, *id)
		if getErr != nil {
			return nil, getErr
		}
		n.ID, n.Active = *id, current.Active
		saved, err = s.store.UpdateNotification(ctx, n, applyToAll)
	}
	if err != nil {
		return nil, err
	}
	s.broadcastNotificationList(ctx)
	if applyToAll {
		if tree, err := s.monitorTree(ctx); err == nil {
			s.broadcast(encodeMessage("updateMonitorIntoList", tree.listJSON()))
		}
	}
	return okI18n("Saved.", "id", saved.ID), nil
}

func (s *Server) deleteNotification(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.store.DeleteNotification(ctx, id); err != nil {
		return nil, err
	}
	s.broadcastNotificationList(ctx)
	return okI18n("successDeleted"), nil
}

func (s *Server) testNotification(ctx context.Context, args []json.RawMessage) (reply, error) {
	if len(args) == 0 {
		return nil, userErr(errors.New("missing notification"))
	}
	n, _, err := parseNotification(args[0])
	if err != nil {
		return nil, userErr(err)
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	if err := notify.SendTest(ctx, s.notifyEnv, n.Type, n.Name, n.Config); err != nil {
		return nil, userErr(err)
	}
	return ok("msg", "Sent Successfully."), nil
}
