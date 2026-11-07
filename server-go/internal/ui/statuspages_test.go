package ui

import (
	"crypto/tls"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

func TestToPrecision4MatchesJavaScript(t *testing.T) {
	// From node -p "(x).toPrecision(4)".
	for v, want := range map[float64]string{
		100: "100.0", 99.95: "99.95", 99.99999: "100.0", 5.5: "5.500", 0.123: "0.1230", 0: "0.000",
		33.33333333: "33.33", 0.0001234: "0.0001234", 12345: "1.235e+4", 99.125: "99.13", 1234: "1234",
		9.9995: "9.999", 99.995: "100.0", 1e-7: "1.000e-7", 0.5: "0.5000",
	} {
		if got := toPrecision4(v); got != want {
			t.Errorf("%v: got %s, want %s", v, got, want)
		}
	}
}

func TestParseBadgeDuration(t *testing.T) {
	for raw, want := range map[string]struct {
		unit string
		n    int
	}{"24h": {"hour", 24}, "720h": {"hour", 720}, "90m": {"minute", 90}, "30d": {"day", 30}, "2w": {"day", 14}, "6M": {"day", 180}, "1y": {"day", 365}} {
		unit, n, err := parseBadgeDuration(raw)
		if err != nil || unit != want.unit || n != want.n {
			t.Errorf("%s: %s %d %v", raw, unit, n, err)
		}
	}
	for _, raw := range []string{"721h", "1441m", "2y", "0h", "h", "24", "24x", "-1h"} {
		if _, _, err := parseBadgeDuration(raw); err == nil {
			t.Errorf("%s: accepted", raw)
		}
	}
}

func TestPlainDescription(t *testing.T) {
	for in, want := range map[string]string{
		"# Title\n\n**Bold** and _it_ with [a link](https://x) ![img](y.png)": "Title Bold and it with a link img",
		"> quote\n- item\n1. one":         "quote item one",
		"<b>html</b> &amp; text":          "html & text",
		string(make([]rune, 0)) + "plain": "plain",
	} {
		if got := plainDescription(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
	long := ""
	for range 200 {
		long += "é"
	}
	if got := []rune(plainDescription(long)); len(got) != 155 {
		t.Errorf("long: %d runes", len(got))
	}
}

func TestRequestHostAndOrigin(t *testing.T) {
	r := httptest.NewRequest("GET", "http://Status.Example.com:8080/", nil)
	r.Header.Set("X-Forwarded-Host", "proxied.example.com, other")
	r.Header.Set("X-Forwarded-Proto", "https")
	if got := requestHost(r, false); got != "status.example.com" {
		t.Errorf("host: %s", got)
	}
	if got := requestHost(r, true); got != "proxied.example.com" {
		t.Errorf("proxied host: %s", got)
	}
	if got := requestOrigin(r, false); got != "http://Status.Example.com:8080" {
		t.Errorf("origin: %s", got)
	}
	if got := requestOrigin(r, true); got != "https://proxied.example.com" {
		t.Errorf("proxied origin: %s", got)
	}
	r.TLS = &tls.ConnectionState{}
	r.Header.Set("X-Forwarded-Proto", "javascript")
	if got := requestOrigin(r, true); got != "http://proxied.example.com" {
		t.Errorf("odd proto: %s", got)
	}
	v6 := httptest.NewRequest("GET", "http://[::1]:3002/", nil)
	if got := requestHost(v6, false); got != "::1" {
		t.Errorf("ipv6: %s", got)
	}
}

func TestIncidentCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 5, 1, 2, 3, 456789000, time.UTC)
	cursor, err := decodeCursor(encodeCursor(store.Incident{ID: 7, CreatedAt: at}))
	if err != nil || !cursor.CreatedAt.Equal(at) || cursor.ID != 7 {
		t.Fatalf("%+v %v", cursor, err)
	}
	for _, bad := range []string{"x", "2026-01-01_x", "nope_1"} {
		if _, err := decodeCursor(bad); err == nil {
			t.Errorf("%s: accepted", bad)
		}
	}
	if c, err := decodeCursor(""); c != nil || err != nil {
		t.Errorf("empty: %v %v", c, err)
	}
}

func TestSafeLink(t *testing.T) {
	for link, want := range map[string]bool{
		"https://example.com/logo.png": true, "http://x.y/a": true, "/upload/logo1.png?t=1": true, "/icon.svg": true,
		"javascript:alert(1)": false, "data:image/png;base64,AA": false, "https://": false, "logo.png": false,
		`/a"onerror="x`: false, "ftp://x/y": false,
	} {
		if got := safeLink(link); got != want {
			t.Errorf("%q: %v", link, got)
		}
	}
}

func TestParseStatusPageRejects(t *testing.T) {
	current := store.StatusPage{ID: 3, Slug: "s"}
	valid := map[string]any{"slug": "s", "title": "T"}
	for name, change := range map[string]map[string]any{
		"slug":      {"slug": "a b"},
		"title":     {"title": " "},
		"theme":     {"theme": "neon"},
		"analytics": {"analyticsType": "tracker"},
		"domain":    {"domainNameList": []string{"https://status.example.com"}},
		"refresh":   {"autoRefreshInterval": -1},
	} {
		config := map[string]any{}
		for k, v := range valid {
			config[k] = v
		}
		for k, v := range change {
			config[k] = v
		}
		raw, _ := json.Marshal(config)
		if _, _, err := parseStatusPage(current, raw, ""); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	raw, _ := json.Marshal(map[string]any{"slug": " S ", "title": "T", "analyticsType": "", "domainNameList": []string{"A.example.com.", "a.example.com"}})
	page, logo, err := parseStatusPage(current, raw, "/icon.svg")
	if err != nil || page.Slug != "s" || page.AnalyticsType != nil || len(page.Domains) != 1 || page.Icon != "" || logo != nil || page.Theme != "auto" {
		t.Fatalf("valid: %+v %v", page, err)
	}
}
