package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// slotTimeFormat is a slot's time on the window's own clock, without a zone. The UI's
// MaintenanceTime component shows the wall clock it's given and labels it with the window's
// timezone, so a time in any other zone would show wrongly to viewers elsewhere.
const slotTimeFormat = "2006-01-02T15:04:05"

const (
	holidayModeNone  = "none"
	upcomingHolidays = 8
)

// flexBool reads the form's active field, which is a number on new windows and a boolean after.
type flexBool bool

func (f *flexBool) UnmarshalJSON(data []byte) error {
	switch strings.TrimSpace(string(data)) {
	case "true", "1":
		*f = true
	case "false", "0", "null":
		*f = false
	default:
		return fmt.Errorf("not a boolean: %s", data)
	}
	return nil
}

type clockInput struct {
	Hours   flexInt `json:"hours"`
	Minutes flexInt `json:"minutes"`
}

func (c *clockInput) text() string {
	if c == nil {
		return ""
	}
	return fmt.Sprintf("%02d:%02d", c.Hours.value, c.Minutes.value)
}

// maintenanceInput is the UI's maintenance form.
type maintenanceInput struct {
	ID               flexInt           `json:"id"`
	Title            string            `json:"title"`
	Description      string            `json:"description"`
	Strategy         string            `json:"strategy"`
	Active           *flexBool         `json:"active"`
	DateRange        []*string         `json:"dateRange"`
	TimeRange        []*clockInput     `json:"timeRange"`
	Weekdays         []int             `json:"weekdays"`
	DaysOfMonth      []json.RawMessage `json:"daysOfMonth"`
	IntervalDay      flexInt           `json:"intervalDay"`
	Cron             string            `json:"cron"`
	DurationMinutes  flexInt           `json:"durationMinutes"`
	TimezoneOption   string            `json:"timezoneOption"`
	HolidayMode      string            `json:"holidayMode"`
	HolidayCountries []string          `json:"holidayCountries"`
}

// parseDayOfMonth reads a day (1 to 31) or "lastDay1", the last day, to "lastDay4".
func parseDayOfMonth(raw json.RawMessage) (int, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return 0, err
		}
		if rest, found := strings.CutPrefix(text, "lastDay"); found {
			n, err := strconv.Atoi(rest)
			if err != nil || n < 1 || n > 4 {
				return 0, fmt.Errorf("invalid day of the month %q", text)
			}
			return -n, nil
		}
		raw = []byte(text)
	}
	day, err := strconv.Atoi(string(raw))
	if err != nil {
		return 0, fmt.Errorf("invalid day of the month %s", raw)
	}
	return day, nil
}

func dayOfMonthJSON(day int) any {
	if day < 0 {
		return "lastDay" + strconv.Itoa(-day)
	}
	return day
}

// parseWindow converts the form to a window. Every field is kept whichever strategy is chosen,
// so that switching strategy back doesn't lose them, and only the chosen strategy's are checked.
func (s *Server) parseWindow(raw json.RawMessage) (maintenance.Window, error) {
	var in maintenanceInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return maintenance.Window{}, fmt.Errorf("invalid maintenance: %w", err)
	}
	w := maintenance.Window{
		ID:              int32(in.ID.value),
		Title:           strings.TrimSpace(in.Title),
		Description:     in.Description,
		Strategy:        in.Strategy,
		Active:          in.Active == nil || bool(*in.Active),
		Weekdays:        in.Weekdays,
		IntervalDay:     int(in.IntervalDay.value),
		Cron:            strings.TrimSpace(in.Cron),
		DurationMinutes: int(in.DurationMinutes.value),
		Timezone:        in.TimezoneOption,
	}
	if w.Timezone == "" {
		w.Timezone = maintenance.SameAsServer
	}
	if len(in.DateRange) > 0 && in.DateRange[0] != nil {
		w.StartDate = strings.TrimSpace(*in.DateRange[0])
	}
	if len(in.DateRange) > 1 && in.DateRange[1] != nil {
		w.EndDate = strings.TrimSpace(*in.DateRange[1])
	}
	if len(in.TimeRange) > 1 {
		w.StartTime, w.EndTime = in.TimeRange[0].text(), in.TimeRange[1].text()
	}
	for _, raw := range in.DaysOfMonth {
		day, err := parseDayOfMonth(raw)
		if err != nil {
			return w, err
		}
		w.DaysOfMonth = append(w.DaysOfMonth, day)
	}

	switch in.Strategy {
	case maintenance.StrategyManual, maintenance.StrategySingle:
		// These have no recurring days for holidays to change.
	default:
		if in.HolidayMode != holidayModeNone {
			w.HolidayMode = in.HolidayMode
		}
		for _, code := range in.HolidayCountries {
			code = strings.ToUpper(strings.TrimSpace(code))
			if !s.maintenance.Calendar().Known(code) {
				return w, fmt.Errorf("no holiday calendar for %q", code)
			}
			if !slices.Contains(w.HolidayCountries, code) {
				w.HolidayCountries = append(w.HolidayCountries, code)
			}
		}
	}
	if err := s.maintenance.Evaluator().Validate(w); err != nil {
		return w, err
	}
	return w, nil
}

func clockJSON(value string, defaultHour int) map[string]any {
	hour, minute := defaultHour, 0
	if _, err := fmt.Sscanf(value, "%d:%d", &hour, &minute); err != nil {
		hour, minute = defaultHour, 0
	}
	return map[string]any{"hours": hour, "minutes": minute}
}

func optionalText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func slotJSON(slot maintenance.Slot, loc *time.Location) map[string]any {
	return map[string]any{
		"startDate": slot.Start.In(loc).Format(slotTimeFormat),
		"endDate":   slot.End.In(loc).Format(slotTimeFormat),
	}
}

// windowJSON is a window as the UI's list and form read it.
func (s *Server) windowJSON(w maintenance.Window, now time.Time) map[string]any {
	eval := s.maintenance.Evaluator()
	loc, err := eval.Location(w)
	if err != nil {
		loc = time.UTC
	}
	slots := []map[string]any{}
	if current, ok := eval.Current(w, now); ok {
		slots = append(slots, slotJSON(current, loc))
	}
	if next, ok := eval.Next(w, now); ok {
		slots = append(slots, slotJSON(next, loc))
	}
	days := make([]any, len(w.DaysOfMonth))
	for i, day := range w.DaysOfMonth {
		days[i] = dayOfMonthJSON(day)
	}
	weekdays, countries := w.Weekdays, w.HolidayCountries
	if weekdays == nil {
		weekdays = []int{}
	}
	if countries == nil {
		countries = []string{}
	}
	mode := w.HolidayMode
	if mode == "" {
		mode = holidayModeNone
	}
	minutes := w.DurationMinutes
	if minutes == 0 {
		minutes = 60
	}
	interval := w.IntervalDay
	if interval == 0 {
		interval = 1
	}
	return map[string]any{
		"id":               w.ID,
		"title":            w.Title,
		"description":      w.Description,
		"strategy":         w.Strategy,
		"intervalDay":      interval,
		"active":           w.Active,
		"dateRange":        []any{optionalText(w.StartDate), optionalText(w.EndDate)},
		"timeRange":        []any{clockJSON(w.StartTime, 2), clockJSON(w.EndTime, 3)},
		"weekdays":         weekdays,
		"daysOfMonth":      days,
		"timeslotList":     slots,
		"cron":             w.Cron,
		"duration":         minutes * 60,
		"durationMinutes":  minutes,
		"timezone":         loc.String(),
		"timezoneOption":   w.Timezone,
		"timezoneOffset":   zoneOffset(now.In(loc)),
		"status":           eval.Status(w, now),
		"holidayMode":      mode,
		"holidayCountries": countries,
	}
}

func zoneOffset(t time.Time) string {
	_, offset := t.Zone()
	sign := "+"
	if offset < 0 {
		sign, offset = "-", -offset
	}
	return fmt.Sprintf("%s%02d:%02d", sign, offset/3600, offset%3600/60)
}

func (s *Server) maintenanceList(ctx context.Context) (map[string]any, error) {
	windows, err := s.store.ListMaintenances(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	list := make(map[string]any, len(windows))
	for _, w := range windows {
		list[strconv.Itoa(int(w.ID))] = s.windowJSON(w, now)
	}
	return list, nil
}

func (s *Server) broadcastMaintenanceList(ctx context.Context) {
	list, err := s.maintenanceList(ctx)
	if err != nil {
		s.log.Error("send maintenance list", "error", err)
		return
	}
	s.broadcast(encodeMessage("maintenanceList", list))
}

// maintenanceChanged applies a change at once, rather than at the next periodic refresh.
func (s *Server) maintenanceChanged(ctx context.Context) {
	if err := s.maintenance.Refresh(ctx); err != nil {
		s.log.Error("refresh maintenance", "error", err)
	}
	s.broadcastMaintenanceList(ctx)
}

func (s *Server) saveMaintenance(ctx context.Context, args []json.RawMessage, editing bool) (reply, error) {
	if len(args) == 0 {
		return nil, userErr(errors.New("missing maintenance"))
	}
	w, err := s.parseWindow(args[0])
	if err != nil {
		return nil, userErr(err)
	}
	var saved maintenance.Window
	if editing {
		if w.ID <= 0 {
			return nil, userErr(errors.New("invalid id"))
		}
		saved, err = s.store.UpdateMaintenance(ctx, w)
	} else {
		saved, err = s.store.CreateMaintenance(ctx, w)
	}
	if err != nil {
		return nil, err
	}
	s.maintenanceChanged(ctx)
	s.log.Info("maintenance saved from the web UI", "maintenance", saved.ID)
	if editing {
		return okI18n("Saved.", "maintenanceID", saved.ID), nil
	}
	return okI18n("successAdded", "maintenanceID", saved.ID), nil
}

func (s *Server) setMaintenanceMonitors(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var monitors []struct {
		ID flexInt `json:"id"`
	}
	if err := arg(args, 1, &monitors); err != nil {
		return nil, userErr(err)
	}
	ids := make([]int32, 0, len(monitors))
	for _, m := range monitors {
		if !m.ID.set || m.ID.value <= 0 {
			return nil, userErr(errors.New("invalid monitor id"))
		}
		ids = append(ids, int32(m.ID.value))
	}
	if err := s.store.SetMaintenanceMonitors(ctx, id, ids); err != nil {
		return nil, err
	}
	s.maintenanceChanged(ctx)
	return okI18n("successAdded"), nil
}

// noMaintenanceStatusPages succeeds when there's nothing to link, because the form always sends
// this after saving, on a server without status pages.
func (s *Server) noMaintenanceStatusPages(args []json.RawMessage) (reply, error) {
	var pages []json.RawMessage
	if err := arg(args, 1, &pages); err != nil {
		return nil, userErr(err)
	}
	if len(pages) > 0 {
		return notSupported("addMaintenanceStatusPage"), nil
	}
	return ok(), nil
}

func (s *Server) getMaintenance(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	w, err := s.store.GetMaintenance(ctx, id)
	if err != nil {
		return nil, err
	}
	return ok("maintenance", s.windowJSON(w, s.now())), nil
}

func (s *Server) getMaintenanceMonitors(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	links, err := s.store.MaintenanceMonitorLinks(ctx)
	if err != nil {
		return nil, err
	}
	monitors := make([]map[string]any, 0, len(links[id]))
	for _, monitorID := range links[id] {
		monitors = append(monitors, map[string]any{"id": monitorID})
	}
	return ok("monitors", monitors), nil
}

func (s *Server) setMaintenanceActive(ctx context.Context, args []json.RawMessage, active bool) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.store.SetMaintenanceActive(ctx, id, active); err != nil {
		return nil, err
	}
	s.maintenanceChanged(ctx)
	if active {
		return okI18n("successResumed"), nil
	}
	return okI18n("successPaused"), nil
}

func (s *Server) deleteMaintenance(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.store.DeleteMaintenance(ctx, id); err != nil {
		return nil, err
	}
	s.maintenanceChanged(ctx)
	s.log.Info("maintenance deleted from the web UI", "maintenance", id)
	return okI18n("successDeleted"), nil
}

func (s *Server) holidayCountries() reply {
	calendar := s.maintenance.Calendar()
	return ok("countries", calendar.Countries(), "source", calendar.Source())
}

func (s *Server) countryArg(args []json.RawMessage, i int) (string, error) {
	var code string
	if err := arg(args, i, &code); err != nil {
		return "", err
	}
	code = strings.ToUpper(strings.TrimSpace(code))
	if !s.maintenance.Calendar().Known(code) {
		return "", fmt.Errorf("no holiday calendar for %q", code)
	}
	return code, nil
}

func dateArg(args []json.RawMessage, i int) (string, time.Time, error) {
	var date string
	if err := arg(args, i, &date); err != nil {
		return "", time.Time{}, err
	}
	parsed, err := time.Parse(holidays.DateFormat, date)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("invalid date %q", date)
	}
	return date, parsed, nil
}

func (s *Server) listHolidays(args []json.RawMessage) (reply, error) {
	code, err := s.countryArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var year flexInt
	if err := arg(args, 1, &year); err != nil {
		return nil, userErr(err)
	}
	if year.value < 1970 || year.value > 9999 {
		return nil, userErr(errors.New("invalid year"))
	}
	return ok("holidays", s.maintenance.Calendar().List(code, int(year.value))), nil
}

// upcoming previews the holidays that a window's chosen countries will affect.
func (s *Server) upcoming(args []json.RawMessage) (reply, error) {
	var codes []string
	if err := arg(args, 0, &codes); err != nil {
		return nil, userErr(err)
	}
	calendar := s.maintenance.Calendar()
	for _, code := range codes {
		if !calendar.Known(code) {
			return nil, userErr(fmt.Errorf("no holiday calendar for %q", code))
		}
	}
	sort.Strings(codes)
	today := s.now().In(s.opts.Location)
	return ok("holidays", calendar.Upcoming(codes, today, upcomingHolidays)), nil
}

func (s *Server) addCustomHoliday(ctx context.Context, args []json.RawMessage) (reply, error) {
	code, err := s.countryArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	date, _, err := dateArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	var name string
	if err := arg(args, 2, &name); err != nil {
		return nil, userErr(err)
	}
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 200 {
		return nil, userErr(errors.New("the name must be 1 to 200 characters"))
	}
	if err := s.store.SetHolidayOverride(ctx, holidays.Override{Country: code, Date: date, Name: name}); err != nil {
		return nil, err
	}
	s.maintenanceChanged(ctx)
	return okI18n("Saved."), nil
}

// removeHoliday deletes a custom date, or marks a bundled one as removed, which can be undone.
func (s *Server) removeHoliday(ctx context.Context, args []json.RawMessage) (reply, error) {
	code, err := s.countryArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	date, parsed, err := dateArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	found := false
	for _, h := range s.maintenance.Calendar().List(code, parsed.Year()) {
		if h.Date != date {
			continue
		}
		found = true
		if h.Source == "custom" {
			err = s.store.DeleteHolidayOverride(ctx, code, date)
		} else {
			err = s.store.SetHolidayOverride(ctx, holidays.Override{Country: code, Date: date, Removed: true})
		}
	}
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, store.ErrNotFound
	}
	s.maintenanceChanged(ctx)
	return okI18n("successDeleted"), nil
}

func (s *Server) restoreHoliday(ctx context.Context, args []json.RawMessage) (reply, error) {
	code, err := s.countryArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	date, _, err := dateArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.store.DeleteHolidayOverride(ctx, code, date); err != nil {
		return nil, err
	}
	s.maintenanceChanged(ctx)
	return okI18n("Saved."), nil
}
