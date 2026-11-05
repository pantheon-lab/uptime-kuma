// Package maintenance decides when maintenance windows are running, and which monitors they
// cover. A monitor under maintenance makes MAINTENANCE beats instead of being checked, and
// doesn't alert.
//
// Unlike the Node.js backend, which kept each window's state in timers, a window's state is
// worked out from its rules and the current time whenever it's needed, so it survives restarts.
// That also lets a recurring window skip public holidays, or run on them too.
package maintenance

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/adhocore/gronx"
)

// Strategies, with the UI's names.
const (
	StrategyManual      = "manual"
	StrategySingle      = "single"
	StrategyCron        = "cron"
	StrategyInterval    = "recurring-interval"
	StrategyWeekday     = "recurring-weekday"
	StrategyDayOfMonth  = "recurring-day-of-month"
	SameAsServer        = "SAME_AS_SERVER"
	HolidaysNone        = ""
	HolidaysInclude     = "include"
	HolidaysExclude     = "exclude"
	LocalDateTimeFormat = "2006-01-02T15:04"
)

// Statuses, with the UI's names.
const (
	StatusInactive         = "inactive"
	StatusScheduled        = "scheduled"
	StatusUnderMaintenance = "under-maintenance"
	StatusEnded            = "ended"
)

// Window is a maintenance window's rules. Dates and times are local to the window's timezone.
type Window struct {
	ID          int32
	Title       string
	Description string
	Strategy    string
	Active      bool
	// StartDate and EndDate are "2006-01-02T15:04" or empty. For a single window they're the
	// window; for recurring ones the period in which it recurs.
	StartDate string
	EndDate   string
	// StartTime and EndTime are "15:04", the daily window of recurring strategies. An end at or
	// before the start means the next day.
	StartTime string
	EndTime   string
	// Weekdays are 0 (Sunday) to 6.
	Weekdays []int
	// DaysOfMonth are 1 to 31, or -1 to -4 for the last to fourth-last day.
	DaysOfMonth []int
	IntervalDay int
	Cron        string
	// DurationMinutes is how long a cron window lasts.
	DurationMinutes int
	// Timezone is an IANA name, "UTC", or SameAsServer.
	Timezone string
	// HolidayMode is HolidaysNone, HolidaysInclude or HolidaysExclude.
	HolidayMode      string
	HolidayCountries []string
}

// Slot is one occurrence of a window.
type Slot struct {
	Start time.Time
	End   time.Time
}

// Holidays answers whether a calendar date is a holiday in any of the given countries.
type Holidays interface {
	IsHoliday(date time.Time, countries []string) bool
}

// Evaluator works out windows' states.
type Evaluator struct {
	// Server is the timezone of windows set to SameAsServer.
	Server   *time.Location
	Holidays Holidays
}

// maxLookahead bounds the search for a recurring window's next occurrence. The UI allows
// intervals of up to 3650 days.
const maxLookahead = 3660

// Location returns the window's timezone.
func (e Evaluator) Location(w Window) (*time.Location, error) {
	switch w.Timezone {
	case "", SameAsServer:
		if e.Server == nil {
			return time.UTC, nil
		}
		return e.Server, nil
	default:
		loc, err := time.LoadLocation(w.Timezone)
		if err != nil {
			return nil, fmt.Errorf("unknown timezone %q", w.Timezone)
		}
		return loc, nil
	}
}

func parseLocal(value string, loc *time.Location) (time.Time, bool, error) {
	if value == "" {
		return time.Time{}, false, nil
	}
	for _, layout := range []string{LocalDateTimeFormat, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, loc); err == nil {
			return t, true, nil
		}
	}
	return time.Time{}, false, fmt.Errorf("invalid date %q", value)
}

func parseClock(value string) (hour, minute int, err error) {
	if _, err := fmt.Sscanf(value, "%d:%d", &hour, &minute); err != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("invalid time %q", value)
	}
	return hour, minute, nil
}

// Validate reports the first problem with a window's rules.
func (e Evaluator) Validate(w Window) error {
	if strings.TrimSpace(w.Title) == "" {
		return errors.New("title is required")
	}
	loc, err := e.Location(w)
	if err != nil {
		return err
	}
	start, hasStart, err := parseLocal(w.StartDate, loc)
	if err != nil {
		return err
	}
	end, hasEnd, err := parseLocal(w.EndDate, loc)
	if err != nil {
		return err
	}
	if hasStart && hasEnd && !end.After(start) {
		return errors.New("the end date must be after the start date")
	}
	switch w.HolidayMode {
	case HolidaysNone, HolidaysInclude, HolidaysExclude:
	default:
		return fmt.Errorf("invalid holiday mode %q", w.HolidayMode)
	}
	if w.HolidayMode != HolidaysNone && len(w.HolidayCountries) == 0 {
		return errors.New("choose at least one country for holidays")
	}

	switch w.Strategy {
	case StrategyManual:
		return nil
	case StrategySingle:
		if !hasStart || !hasEnd {
			return errors.New("a single window needs a start and an end")
		}
		return nil
	case StrategyCron:
		if !gronx.IsValid(w.Cron) {
			return fmt.Errorf("invalid cron expression %q", w.Cron)
		}
		if w.DurationMinutes < 1 {
			return errors.New("duration must be at least 1 minute")
		}
		if w.HolidayMode == HolidaysInclude {
			return errors.New("a cron window can skip holidays, but not add them, because it has no fixed time of day")
		}
		return nil
	case StrategyInterval, StrategyWeekday, StrategyDayOfMonth:
		if _, _, err := parseClock(w.StartTime); err != nil {
			return err
		}
		if _, _, err := parseClock(w.EndTime); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown strategy %q", w.Strategy)
	}
	switch w.Strategy {
	case StrategyInterval:
		if w.IntervalDay < 1 || w.IntervalDay > 3650 {
			return errors.New("the interval must be 1 to 3650 days")
		}
	case StrategyWeekday:
		if len(w.Weekdays) == 0 && w.HolidayMode != HolidaysInclude {
			return errors.New("choose at least one day of the week")
		}
		for _, d := range w.Weekdays {
			if d < 0 || d > 6 {
				return fmt.Errorf("invalid day of the week %d", d)
			}
		}
	case StrategyDayOfMonth:
		if len(w.DaysOfMonth) == 0 && w.HolidayMode != HolidaysInclude {
			return errors.New("choose at least one day of the month")
		}
		for _, d := range w.DaysOfMonth {
			if d == 0 || d < -4 || d > 31 {
				return fmt.Errorf("invalid day of the month %d", d)
			}
		}
	}
	return nil
}

// Status is the window's state at now.
func (e Evaluator) Status(w Window, now time.Time) string {
	if !w.Active {
		return StatusInactive
	}
	if w.Strategy == StrategyManual {
		return StatusUnderMaintenance
	}
	loc, err := e.Location(w)
	if err != nil {
		return StatusInactive
	}
	if start, ok, _ := parseLocal(w.StartDate, loc); ok && now.Before(start) {
		return StatusScheduled
	}
	if end, ok, _ := parseLocal(w.EndDate, loc); ok && !now.Before(end) {
		return StatusEnded
	}
	if _, ok := e.Current(w, now); ok {
		return StatusUnderMaintenance
	}
	return StatusScheduled
}

// Current returns the occurrence running at now, if any. Manual windows have no occurrences.
func (e Evaluator) Current(w Window, now time.Time) (Slot, bool) {
	if !w.Active {
		return Slot{}, false
	}
	loc, err := e.Location(w)
	if err != nil {
		return Slot{}, false
	}
	rangeStart, hasStart, _ := parseLocal(w.StartDate, loc)
	rangeEnd, hasEnd, _ := parseLocal(w.EndDate, loc)
	clip := func(s Slot) (Slot, bool) {
		if hasStart && s.Start.Before(rangeStart) {
			return Slot{}, false
		}
		if hasEnd && s.End.After(rangeEnd) {
			s.End = rangeEnd
		}
		return s, !now.Before(s.Start) && now.Before(s.End)
	}

	switch w.Strategy {
	case StrategySingle:
		if !hasStart || !hasEnd {
			return Slot{}, false
		}
		s := Slot{Start: rangeStart, End: rangeEnd}
		return s, !now.Before(s.Start) && now.Before(s.End)
	case StrategyCron:
		duration := time.Duration(w.DurationMinutes) * time.Minute
		ref := now.In(loc)
		// Walk back through the ticks that could still be running.
		for range 1000 {
			tick, err := gronx.PrevTickBefore(w.Cron, ref, true)
			if err != nil || !tick.Add(duration).After(now) {
				return Slot{}, false
			}
			if !e.skipped(w, tick, loc) {
				if s, ok := clip(Slot{Start: tick, End: tick.Add(duration)}); ok {
					return s, true
				}
			}
			ref = tick.Add(-time.Second)
		}
		return Slot{}, false
	case StrategyInterval, StrategyWeekday, StrategyDayOfMonth:
		// A daily window lasts at most a day, so it started today or yesterday.
		today := dateOf(now.In(loc))
		for _, day := range []time.Time{today.AddDate(0, 0, -1), today} {
			s, ok := e.dailySlot(w, day, loc)
			if !ok {
				continue
			}
			if s, ok := clip(s); ok {
				return s, true
			}
		}
	}
	return Slot{}, false
}

// Next returns the first occurrence that starts after now, if any.
func (e Evaluator) Next(w Window, now time.Time) (Slot, bool) {
	if !w.Active {
		return Slot{}, false
	}
	loc, err := e.Location(w)
	if err != nil {
		return Slot{}, false
	}
	rangeStart, hasStart, _ := parseLocal(w.StartDate, loc)
	rangeEnd, hasEnd, _ := parseLocal(w.EndDate, loc)
	inRange := func(s Slot) (Slot, bool) {
		if hasStart && s.Start.Before(rangeStart) {
			return Slot{}, false
		}
		if hasEnd && !s.Start.Before(rangeEnd) {
			return Slot{}, false
		}
		if hasEnd && s.End.After(rangeEnd) {
			s.End = rangeEnd
		}
		return s, true
	}

	switch w.Strategy {
	case StrategySingle:
		if hasStart && hasEnd && rangeStart.After(now) {
			return Slot{Start: rangeStart, End: rangeEnd}, true
		}
	case StrategyCron:
		duration := time.Duration(w.DurationMinutes) * time.Minute
		ref := now.In(loc)
		if hasStart && rangeStart.After(ref) {
			ref = rangeStart.Add(-time.Second)
		}
		for range 1000 {
			tick, err := gronx.NextTickAfter(w.Cron, ref, false)
			if err != nil || (hasEnd && !tick.Before(rangeEnd)) {
				return Slot{}, false
			}
			if !e.skipped(w, tick, loc) {
				if s, ok := inRange(Slot{Start: tick, End: tick.Add(duration)}); ok {
					return s, true
				}
			}
			ref = tick
		}
	case StrategyInterval, StrategyWeekday, StrategyDayOfMonth:
		day := dateOf(now.In(loc))
		if hasStart && rangeStart.After(now) {
			day = dateOf(rangeStart)
		}
		for range maxLookahead {
			if hasEnd && day.After(rangeEnd) {
				return Slot{}, false
			}
			if s, ok := e.dailySlot(w, day, loc); ok && s.Start.After(now) {
				if s, ok := inRange(s); ok {
					return s, true
				}
			}
			day = day.AddDate(0, 0, 1)
		}
	}
	return Slot{}, false
}

// skipped reports whether a cron tick falls on a holiday that the window skips.
func (e Evaluator) skipped(w Window, tick time.Time, loc *time.Location) bool {
	return w.HolidayMode == HolidaysExclude && e.Holidays != nil &&
		e.Holidays.IsHoliday(dateOf(tick.In(loc)), w.HolidayCountries)
}

// dateOf returns midnight of t's calendar date, in t's location.
func dateOf(t time.Time) time.Time {
	y, m, d := t.Date()
	return time.Date(y, m, d, 0, 0, 0, 0, t.Location())
}

// dailySlot returns the occurrence of a recurring window that starts on day, if it runs that day.
func (e Evaluator) dailySlot(w Window, day time.Time, loc *time.Location) (Slot, bool) {
	runs := e.matchesDay(w, day, loc)
	if w.HolidayMode != HolidaysNone && e.Holidays != nil {
		holiday := e.Holidays.IsHoliday(day, w.HolidayCountries)
		switch {
		case w.HolidayMode == HolidaysExclude && holiday:
			runs = false
		case w.HolidayMode == HolidaysInclude && holiday:
			runs = true
		}
	}
	if !runs {
		return Slot{}, false
	}
	sh, sm, err := parseClock(w.StartTime)
	if err != nil {
		return Slot{}, false
	}
	eh, em, err := parseClock(w.EndTime)
	if err != nil {
		return Slot{}, false
	}
	y, m, d := day.Date()
	start := time.Date(y, m, d, sh, sm, 0, 0, loc)
	end := time.Date(y, m, d, eh, em, 0, 0, loc)
	if !end.After(start) {
		// Ends the next day. An end equal to the start means a whole day.
		end = time.Date(y, m, d+1, eh, em, 0, 0, loc)
	}
	return Slot{Start: start, End: end}, true
}

// matchesDay applies the strategy's own rule, before holidays.
func (e Evaluator) matchesDay(w Window, day time.Time, loc *time.Location) bool {
	switch w.Strategy {
	case StrategyWeekday:
		return slices.Contains(w.Weekdays, int(day.Weekday()))
	case StrategyDayOfMonth:
		lastDay := time.Date(day.Year(), day.Month()+1, 0, 0, 0, 0, 0, loc).Day()
		for _, d := range w.DaysOfMonth {
			if d == day.Day() || (d < 0 && day.Day() == lastDay+d+1) {
				return true
			}
		}
		return false
	case StrategyInterval:
		if w.IntervalDay <= 1 {
			return true
		}
		// Days are counted from the start of the effective period, or from 1 January 1970.
		anchor := time.Date(1970, 1, 1, 0, 0, 0, 0, loc)
		if start, ok, _ := parseLocal(w.StartDate, loc); ok {
			anchor = dateOf(start)
		}
		days := daysBetween(anchor, day)
		return days >= 0 && days%w.IntervalDay == 0
	}
	return false
}

// daysBetween counts calendar days, which isn't the same as 24-hour periods across a DST change.
func daysBetween(from, to time.Time) int {
	a := time.Date(from.Year(), from.Month(), from.Day(), 12, 0, 0, 0, time.UTC)
	b := time.Date(to.Year(), to.Month(), to.Day(), 12, 0, 0, 0, time.UTC)
	return int(b.Sub(a).Hours() / 24)
}
