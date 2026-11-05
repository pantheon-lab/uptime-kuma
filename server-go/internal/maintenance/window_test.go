package maintenance

import (
	"strings"
	"testing"
	"time"
)

var hk = mustLoad("Asia/Hong_Kong")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// fakeHolidays treats the listed dates as holidays in "HK".
type fakeHolidays map[string]bool

func (f fakeHolidays) IsHoliday(date time.Time, countries []string) bool {
	for _, c := range countries {
		if c == "HK" && f[date.Format("2006-01-02")] {
			return true
		}
	}
	return false
}

func at(value string) time.Time {
	t, err := time.ParseInLocation("2006-01-02 15:04", value, hk)
	if err != nil {
		panic(err)
	}
	return t
}

func eval() Evaluator {
	// 2026-04-03 is a Friday, Good Friday; 2026-04-06 is a Monday.
	return Evaluator{Server: hk, Holidays: fakeHolidays{"2026-04-03": true, "2026-04-06": true, "2026-04-07": true}}
}

func weekly(days ...int) Window {
	return Window{ID: 1, Title: "w", Strategy: StrategyWeekday, Active: true, Timezone: SameAsServer,
		StartTime: "02:00", EndTime: "04:00", Weekdays: days}
}

func TestWeekdayWindow(t *testing.T) {
	e := eval()
	w := weekly(5) // Fridays
	for _, tc := range []struct {
		now  string
		want string
	}{
		{"2026-03-27 01:59", StatusScheduled},
		{"2026-03-27 02:00", StatusUnderMaintenance},
		{"2026-03-27 03:59", StatusUnderMaintenance},
		{"2026-03-27 04:00", StatusScheduled},
		{"2026-03-28 03:00", StatusScheduled},
	} {
		if got := e.Status(w, at(tc.now)); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.now, got, tc.want)
		}
	}
	next, ok := e.Next(w, at("2026-03-27 05:00"))
	if !ok || !next.Start.Equal(at("2026-04-03 02:00")) || !next.End.Equal(at("2026-04-03 04:00")) {
		t.Fatalf("next: %v %v", next, ok)
	}
}

func TestHolidaysExcluded(t *testing.T) {
	e := eval()
	w := weekly(5)
	w.HolidayMode, w.HolidayCountries = HolidaysExclude, []string{"HK"}
	if got := e.Status(w, at("2026-04-03 03:00")); got != StatusScheduled {
		t.Fatalf("Good Friday: %s", got)
	}
	next, ok := e.Next(w, at("2026-03-27 05:00"))
	if !ok || !next.Start.Equal(at("2026-04-10 02:00")) {
		t.Fatalf("next skips the holiday: %v %v", next, ok)
	}
	// A holiday in another country doesn't count.
	w.HolidayCountries = []string{"SG"}
	if got := e.Status(w, at("2026-04-03 03:00")); got != StatusUnderMaintenance {
		t.Fatalf("other country: %s", got)
	}
}

func TestHolidaysIncluded(t *testing.T) {
	e := eval()
	w := weekly(5)
	w.HolidayMode, w.HolidayCountries = HolidaysInclude, []string{"HK"}
	// Monday 6 April is a holiday, so the Friday window also runs then, at the same time.
	if got := e.Status(w, at("2026-04-06 02:30")); got != StatusUnderMaintenance {
		t.Fatalf("holiday Monday: %s", got)
	}
	if got := e.Status(w, at("2026-04-08 02:30")); got != StatusScheduled {
		t.Fatalf("ordinary Wednesday: %s", got)
	}
	next, ok := e.Next(w, at("2026-04-03 05:00"))
	if !ok || !next.Start.Equal(at("2026-04-06 02:00")) {
		t.Fatalf("next holiday: %v %v", next, ok)
	}
	// Holidays only, with no weekdays.
	w.Weekdays = nil
	if err := e.Validate(w); err != nil {
		t.Fatalf("holidays-only window: %v", err)
	}
	if got := e.Status(w, at("2026-04-07 03:00")); got != StatusUnderMaintenance {
		t.Fatalf("holidays only: %s", got)
	}
}

func TestOvernightWindow(t *testing.T) {
	e := eval()
	w := weekly(5)
	w.StartTime, w.EndTime = "23:00", "01:00"
	if got := e.Status(w, at("2026-03-28 00:30")); got != StatusUnderMaintenance {
		t.Fatalf("after midnight: %s", got)
	}
	w.EndTime = "23:00"
	if s, ok := e.Current(w, at("2026-03-28 12:00")); !ok || s.End.Sub(s.Start) != 24*time.Hour {
		t.Fatalf("whole day: %v %v", s, ok)
	}
}

func TestDayOfMonth(t *testing.T) {
	e := eval()
	w := weekly()
	w.Strategy, w.Weekdays, w.DaysOfMonth = StrategyDayOfMonth, nil, []int{15, -1, -2}
	for now, want := range map[string]string{
		"2026-02-15 03:00": StatusUnderMaintenance,
		"2026-02-28 03:00": StatusUnderMaintenance, // last day
		"2026-02-27 03:00": StatusUnderMaintenance, // second-last
		"2026-02-26 03:00": StatusScheduled,
		"2026-03-31 03:00": StatusUnderMaintenance,
		"2026-03-30 03:00": StatusUnderMaintenance,
	} {
		if got := e.Status(w, at(now)); got != want {
			t.Errorf("%s: %s, want %s", now, got, want)
		}
	}
}

func TestInterval(t *testing.T) {
	e := eval()
	w := weekly()
	w.Strategy, w.Weekdays, w.IntervalDay, w.StartDate = StrategyInterval, nil, 3, "2026-03-01T00:00"
	for now, want := range map[string]string{
		"2026-03-01 03:00": StatusUnderMaintenance,
		"2026-03-02 03:00": StatusScheduled,
		"2026-03-04 03:00": StatusUnderMaintenance,
		"2026-02-28 03:00": StatusScheduled, // before the effective period
	} {
		if got := e.Status(w, at(now)); got != want {
			t.Errorf("%s: %s, want %s", now, got, want)
		}
	}
	next, ok := e.Next(w, at("2026-03-04 05:00"))
	if !ok || !next.Start.Equal(at("2026-03-07 02:00")) {
		t.Fatalf("next: %v %v", next, ok)
	}
}

func TestEffectiveRange(t *testing.T) {
	e := eval()
	w := weekly(5)
	w.StartDate, w.EndDate = "2026-04-01T00:00", "2026-04-10T03:00"
	if got := e.Status(w, at("2026-03-31 12:00")); got != StatusScheduled {
		t.Fatalf("before: %s", got)
	}
	// The last window is cut short at the end of the period.
	s, ok := e.Current(w, at("2026-04-10 02:30"))
	if !ok || !s.End.Equal(at("2026-04-10 03:00")) {
		t.Fatalf("clipped: %v %v", s, ok)
	}
	if got := e.Status(w, at("2026-04-10 03:00")); got != StatusEnded {
		t.Fatalf("after: %s", got)
	}
	if _, ok := e.Next(w, at("2026-04-10 05:00")); ok {
		t.Fatal("next after the period")
	}
}

func TestSingleAndManual(t *testing.T) {
	e := eval()
	w := Window{Title: "s", Strategy: StrategySingle, Active: true, StartDate: "2026-05-01T10:00", EndDate: "2026-05-01T12:00", Timezone: "UTC"}
	utc := func(v string) time.Time { t, _ := time.Parse("2006-01-02 15:04", v); return t }
	for now, want := range map[string]string{
		"2026-05-01 09:59": StatusScheduled,
		"2026-05-01 10:00": StatusUnderMaintenance,
		"2026-05-01 12:00": StatusEnded,
	} {
		if got := e.Status(w, utc(now)); got != want {
			t.Errorf("%s: %s, want %s", now, got, want)
		}
	}
	w.Active = false
	if got := e.Status(w, utc("2026-05-01 11:00")); got != StatusInactive {
		t.Fatalf("paused: %s", got)
	}
	m := Window{Title: "m", Strategy: StrategyManual, Active: true}
	if got := e.Status(m, time.Now()); got != StatusUnderMaintenance {
		t.Fatalf("manual: %s", got)
	}
}

func TestCron(t *testing.T) {
	e := eval()
	w := Window{Title: "c", Strategy: StrategyCron, Active: true, Cron: "30 3 * * *", DurationMinutes: 60, Timezone: SameAsServer}
	if err := e.Validate(w); err != nil {
		t.Fatal(err)
	}
	for now, want := range map[string]string{
		"2026-04-02 03:29": StatusScheduled,
		"2026-04-02 03:30": StatusUnderMaintenance,
		"2026-04-02 04:29": StatusUnderMaintenance,
		"2026-04-02 04:30": StatusScheduled,
	} {
		if got := e.Status(w, at(now)); got != want {
			t.Errorf("%s: %s, want %s", now, got, want)
		}
	}
	w.HolidayMode, w.HolidayCountries = HolidaysExclude, []string{"HK"}
	if got := e.Status(w, at("2026-04-03 03:45")); got != StatusScheduled {
		t.Fatalf("cron on a holiday: %s", got)
	}
	next, ok := e.Next(w, at("2026-04-02 05:00"))
	if !ok || !next.Start.Equal(at("2026-04-04 03:30")) {
		t.Fatalf("cron next skips the holiday: %v %v", next, ok)
	}
	w.HolidayMode = HolidaysInclude
	if err := e.Validate(w); err == nil || !strings.Contains(err.Error(), "skip holidays") {
		t.Fatalf("include with cron: %v", err)
	}
	// Last day of the month, which the UI documents.
	last := Window{Title: "c", Strategy: StrategyCron, Active: true, Cron: "0 1 L * *", DurationMinutes: 30, Timezone: SameAsServer}
	if got := e.Status(last, at("2026-02-28 01:10")); got != StatusUnderMaintenance {
		t.Fatalf("L: %s", got)
	}
}

func TestDaylightSaving(t *testing.T) {
	ny := mustLoad("America/New_York")
	e := Evaluator{Server: ny}
	w := weekly(0) // Sundays, 02:00 to 04:00
	w.StartTime = "01:00"
	// Clocks went forward at 02:00 on 8 March 2026; the window is still 01:00 to 04:00 local.
	s, ok := e.Current(w, time.Date(2026, 3, 8, 3, 30, 0, 0, ny))
	if !ok || s.End.Sub(s.Start) != 2*time.Hour {
		t.Fatalf("DST: %v %v", s, ok)
	}
}

func TestValidate(t *testing.T) {
	e := eval()
	for name, w := range map[string]Window{
		"title":     {Strategy: StrategyManual},
		"strategy":  {Title: "x", Strategy: "sometimes"},
		"single":    {Title: "x", Strategy: StrategySingle, StartDate: "2026-01-01T00:00"},
		"order":     {Title: "x", Strategy: StrategySingle, StartDate: "2026-01-02T00:00", EndDate: "2026-01-01T00:00"},
		"cron":      {Title: "x", Strategy: StrategyCron, Cron: "nope", DurationMinutes: 5},
		"weekday":   {Title: "x", Strategy: StrategyWeekday, StartTime: "02:00", EndTime: "03:00"},
		"time":      {Title: "x", Strategy: StrategyWeekday, StartTime: "25:00", EndTime: "03:00", Weekdays: []int{1}},
		"timezone":  {Title: "x", Strategy: StrategyManual, Timezone: "Mars/Olympus"},
		"countries": {Title: "x", Strategy: StrategyManual, HolidayMode: HolidaysExclude},
		"interval":  {Title: "x", Strategy: StrategyInterval, StartTime: "02:00", EndTime: "03:00"},
	} {
		if err := e.Validate(w); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}
