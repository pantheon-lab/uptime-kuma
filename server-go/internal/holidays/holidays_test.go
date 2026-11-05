package holidays

import (
	"testing"
	"time"
)

func day(value string) time.Time {
	t, err := time.Parse(DateFormat, value)
	if err != nil {
		panic(err)
	}
	return t
}

func TestBundledCalendars(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Countries()) < 200 {
		t.Fatalf("only %d countries", len(c.Countries()))
	}
	for _, code := range []string{"HK", "CN", "SG", "TW", "JP", "MY", "MO", "KR", "US", "GB"} {
		if !c.Known(code) {
			t.Errorf("%s missing", code)
		}
	}

	// Hong Kong's 2026 general holidays, as published at 1823.gov.hk.
	hk := c.List("HK", 2026)
	if len(hk) != 17 {
		t.Fatalf("HK 2026 has %d holidays, want 17: %v", len(hk), hk)
	}
	for _, date := range []string{"2026-02-17", "2026-04-03", "2026-04-04", "2026-07-01", "2026-12-26"} {
		if !c.IsHoliday(day(date), []string{"HK"}) {
			t.Errorf("HK %s isn't a holiday", date)
		}
	}
	if c.IsHoliday(day("2026-04-08"), []string{"HK"}) {
		t.Error("an ordinary day is a holiday")
	}
	// Mainland China's substituted days off are holidays; its make-up working days aren't.
	if !c.IsHoliday(day("2026-10-07"), []string{"cn"}) {
		t.Error("CN Golden Week day off")
	}
	if c.IsHoliday(day("2026-01-01"), nil) {
		t.Error("no countries means no holidays")
	}
}

func TestOverrides(t *testing.T) {
	c, err := New()
	if err != nil {
		t.Fatal(err)
	}
	c.SetOverrides([]Override{
		{Country: "hk", Date: "2026-04-08", Name: "Office closed"},
		{Country: "HK", Date: "2026-04-03", Removed: true},
	})
	if !c.IsHoliday(day("2026-04-08"), []string{"HK"}) {
		t.Error("custom date ignored")
	}
	if c.IsHoliday(day("2026-04-03"), []string{"HK"}) {
		t.Error("removed date still a holiday")
	}
	// Another country's calendar still has its own dates.
	if !c.IsHoliday(day("2026-04-03"), []string{"HK", "SG"}) {
		t.Error("Good Friday in Singapore")
	}

	var custom, removed int
	for _, h := range c.List("HK", 2026) {
		if h.Source == "custom" {
			custom++
		}
		if h.Removed {
			removed++
		}
	}
	if custom != 1 || removed != 1 {
		t.Fatalf("custom %d, removed %d", custom, removed)
	}

	up := c.Upcoming([]string{"HK", "SG"}, day("2026-12-20"), 3)
	if len(up) != 3 || up[0].Date != "2026-12-25" || up[0].Name[:3] != "HK:" {
		t.Fatalf("upcoming: %v", up)
	}
}
