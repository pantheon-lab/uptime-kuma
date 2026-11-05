// Package holidays provides public holiday calendars by country, for maintenance windows that
// skip or add holidays.
//
// The calendars are bundled in holidays.json.gz, generated from the python-holidays library by
// generate/generate.py. Admins can add dates the bundle doesn't have and remove dates from it,
// per country; those overrides are stored in the database and applied on top.
package holidays

import (
	"bytes"
	"compress/gzip"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

//go:embed holidays.json.gz
var bundled []byte

// DateFormat is how dates are written: a calendar date with no time or zone.
const DateFormat = "2006-01-02"

// Country is a calendar that can be chosen.
type Country struct {
	Code string `json:"code"`
	Name string `json:"name"`
}

// Holiday is one date in a country's calendar.
type Holiday struct {
	Date string `json:"date"`
	Name string `json:"name"`
	// Source is "bundled" or "custom".
	Source string `json:"source"`
	// Removed marks a bundled date that an admin removed. It doesn't count as a holiday.
	Removed bool `json:"removed"`
}

// Override is an admin's change to a country's calendar: an added date, or a removed one.
type Override struct {
	Country string
	Date    string
	Name    string
	Removed bool
}

type bundle struct {
	Source    string `json:"source"`
	Generated string `json:"generated"`
	FirstYear int    `json:"firstYear"`
	LastYear  int    `json:"lastYear"`
	Countries map[string]struct {
		Name     string            `json:"name"`
		Holidays map[string]string `json:"holidays"`
	} `json:"countries"`
}

// Calendar answers which dates are holidays. It's safe for concurrent use.
type Calendar struct {
	data *bundle

	mu        sync.RWMutex
	overrides map[string]map[string]Override // country -> date -> override
}

var (
	loadOnce sync.Once
	loaded   *bundle
	loadErr  error
)

func loadBundle() (*bundle, error) {
	loadOnce.Do(func() {
		r, err := gzip.NewReader(bytes.NewReader(bundled))
		if err != nil {
			loadErr = fmt.Errorf("open holiday data: %w", err)
			return
		}
		var b bundle
		if err := json.NewDecoder(r).Decode(&b); err != nil {
			loadErr = fmt.Errorf("read holiday data: %w", err)
			return
		}
		loaded = &b
	})
	return loaded, loadErr
}

// New returns a calendar with the bundled data and no overrides.
func New() (*Calendar, error) {
	b, err := loadBundle()
	if err != nil {
		return nil, err
	}
	return &Calendar{data: b, overrides: map[string]map[string]Override{}}, nil
}

// Source describes the bundled data, such as "python-holidays 0.108, 2024-2035".
func (c *Calendar) Source() string {
	return fmt.Sprintf("%s, %d-%d", c.data.Source, c.data.FirstYear, c.data.LastYear)
}

// Countries lists the calendars, by name.
func (c *Calendar) Countries() []Country {
	out := make([]Country, 0, len(c.data.Countries))
	for code, country := range c.data.Countries {
		out = append(out, Country{Code: code, Name: country.Name})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Known reports whether a country code has a calendar.
func (c *Calendar) Known(code string) bool {
	_, ok := c.data.Countries[strings.ToUpper(code)]
	return ok
}

// SetOverrides replaces all admin changes.
func (c *Calendar) SetOverrides(overrides []Override) {
	byCountry := map[string]map[string]Override{}
	for _, o := range overrides {
		country := strings.ToUpper(o.Country)
		if byCountry[country] == nil {
			byCountry[country] = map[string]Override{}
		}
		byCountry[country][o.Date] = o
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.overrides = byCountry
}

// IsHoliday reports whether date, a calendar date, is a holiday in any of the countries.
func (c *Calendar) IsHoliday(date time.Time, countries []string) bool {
	key := date.Format(DateFormat)
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, code := range countries {
		code = strings.ToUpper(code)
		if o, ok := c.overrides[code][key]; ok {
			if !o.Removed {
				return true
			}
			continue
		}
		if _, ok := c.data.Countries[code].Holidays[key]; ok {
			return true
		}
	}
	return false
}

// List returns a country's holidays in a year, with admins' changes, by date. Removed bundled
// dates are included and marked, so that they can be restored.
func (c *Calendar) List(code string, year int) []Holiday {
	code = strings.ToUpper(code)
	prefix := fmt.Sprintf("%04d-", year)
	byDate := map[string]Holiday{}
	for date, name := range c.data.Countries[code].Holidays {
		if strings.HasPrefix(date, prefix) {
			byDate[date] = Holiday{Date: date, Name: name, Source: "bundled"}
		}
	}
	c.mu.RLock()
	for date, o := range c.overrides[code] {
		if !strings.HasPrefix(date, prefix) {
			continue
		}
		if o.Removed {
			if h, ok := byDate[date]; ok {
				h.Removed = true
				byDate[date] = h
			}
			continue
		}
		byDate[date] = Holiday{Date: date, Name: o.Name, Source: "custom"}
	}
	c.mu.RUnlock()
	out := make([]Holiday, 0, len(byDate))
	for _, h := range byDate {
		out = append(out, h)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	return out
}

// Upcoming returns the holidays from today on in the given countries, at most limit of them,
// with the country code in front of each name when there are several countries.
func (c *Calendar) Upcoming(countries []string, from time.Time, limit int) []Holiday {
	var out []Holiday
	for year := from.Year(); year <= from.Year()+1; year++ {
		for _, code := range countries {
			for _, h := range c.List(code, year) {
				if h.Removed || h.Date < from.Format(DateFormat) {
					continue
				}
				if len(countries) > 1 {
					h.Name = strings.ToUpper(code) + ": " + h.Name
				}
				out = append(out, h)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date < out[j].Date })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// IsBundled reports whether a date is in the bundled calendar of a country.
func (c *Calendar) IsBundled(code, date string) bool {
	_, ok := c.data.Countries[strings.ToUpper(code)].Holidays[date]
	return ok
}
