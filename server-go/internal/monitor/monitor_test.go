package monitor

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func valid() Monitor {
	m := New()
	m.Name, m.Type, m.URL = "site", TypeHTTP, "https://example.com"
	return m
}

func TestValidate(t *testing.T) {
	cases := map[string]func(*Monitor){
		"empty name":      func(m *Monitor) { m.Name = " " },
		"interval":        func(m *Monitor) { m.Interval = 0 },
		"retry interval":  func(m *Monitor) { m.RetryInterval = -1 },
		"timeout":         func(m *Monitor) { m.Timeout = -1 },
		"max retries":     func(m *Monitor) { m.MaxRetries = -1 },
		"type":            func(m *Monitor) { m.Type = "dns" },
		"url":             func(m *Monitor) { m.URL = "not a url" },
		"url scheme":      func(m *Monitor) { m.URL = "file:///etc/passwd" },
		"method":          func(m *Monitor) { m.Method = "get" },
		"redirects":       func(m *Monitor) { m.MaxRedirects = -1 },
		"json body":       func(m *Monitor) { m.Body = "{" },
		"encoding":        func(m *Monitor) { m.BodyEncoding = "yaml" },
		"no status codes": func(m *Monitor) { m.AcceptedStatusCodes = nil },
		"status code":     func(m *Monitor) { m.AcceptedStatusCodes = []string{"2xx"} },
		"keyword":         func(m *Monitor) { m.Type = TypeKeyword },
		"hostname":        func(m *Monitor) { m.Type, m.Port = TypePort, 80 },
		"port":            func(m *Monitor) { m.Type, m.Hostname, m.Port = TypePort, "a", 70000 },
	}
	for name, mutate := range cases {
		m := valid()
		mutate(&m)
		if err := m.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
	m := valid()
	if err := m.Validate(); err != nil {
		t.Errorf("valid monitor rejected: %v", err)
	}
}

func TestAccepts(t *testing.T) {
	m := Monitor{AcceptedStatusCodes: []string{"200-299", "404"}}
	for status, want := range map[int]bool{199: false, 200: true, 299: true, 300: false, 404: true, 405: false} {
		if got := m.Accepts(status); got != want {
			t.Errorf("status %d: got %v", status, got)
		}
	}
}

func TestEffectiveTimeout(t *testing.T) {
	m := Monitor{Interval: 60}
	if got := m.EffectiveTimeout(); got != 48*time.Second {
		t.Errorf("default timeout: %v", got)
	}
	m.Timeout = 2.5
	if got := m.EffectiveTimeout(); got != 2500*time.Millisecond {
		t.Errorf("explicit timeout: %v", got)
	}
}

func TestNextRetries(t *testing.T) {
	ok, fail := Result{OK: true, Msg: "fine"}, Result{Msg: "boom"}
	steps := []struct {
		name      string
		result    Result
		status    int16
		retries   int32
		important bool
	}{
		{"first beat is always important", ok, StatusUp, 0, true},
		{"up stays quiet", ok, StatusUp, 0, false},
		{"first failure is pending, not an event", fail, StatusPending, 1, false},
		{"second failure still pending", fail, StatusPending, 2, false},
		{"retries exhausted is down and an event", fail, StatusDown, 3, true},
		{"down keeps counting quietly", fail, StatusDown, 4, false},
		{"recovery is an event and resets", ok, StatusUp, 0, true},
		{"new failure retries again", fail, StatusPending, 1, false},
		{"pending to up is not an event", ok, StatusUp, 0, false},
	}
	m := Monitor{MaxRetries: 2}
	var prev *Previous
	for _, s := range steps {
		out := m.Next(prev, s.result)
		if out.Status != s.status || out.Retries != s.retries || out.Important != s.important {
			t.Errorf("%s: got %+v", s.name, out)
		}
		prev = &Previous{Status: out.Status, Retries: out.Retries}
	}
}

func TestNextWithoutRetriesGoesStraightDown(t *testing.T) {
	m := Monitor{}
	out := m.Next(&Previous{Status: StatusUp}, Result{Msg: "boom"})
	if out.Status != StatusDown || !out.Important || out.Retries != 1 || out.Msg != "boom" {
		t.Errorf("got %+v", out)
	}
}

func TestNextUpsideDown(t *testing.T) {
	m := Monitor{UpsideDown: true, MaxRetries: 1}
	ping := 5.0

	out := m.Next(&Previous{Status: StatusUp}, Result{Msg: "refused"})
	if out.Status != StatusUp || out.Retries != 0 || out.Important || out.Msg != "refused" {
		t.Errorf("failed check should be UP: %+v", out)
	}

	out = m.Next(&Previous{Status: StatusUp}, Result{OK: true, Ping: &ping, Msg: "200 - OK"})
	if out.Status != StatusPending || out.Retries != 1 || out.Msg != "Flip UP to DOWN" || out.Ping == nil {
		t.Errorf("successful check should retry: %+v", out)
	}

	out = m.Next(&Previous{Status: StatusPending, Retries: 1}, Result{OK: true, Msg: "200 - OK"})
	if out.Status != StatusDown || !out.Important {
		t.Errorf("successful check after retries should be DOWN: %+v", out)
	}
}

func TestIsImportantMatchesNodeTable(t *testing.T) {
	const d, u, p, x = StatusDown, StatusUp, StatusPending, StatusMaintenance
	important := map[[2]int16]bool{{d, x}: true, {u, x}: true, {x, d}: true, {x, u}: true, {u, d}: true, {d, u}: true, {p, d}: true}
	for _, from := range []int16{d, u, p, x} {
		for _, to := range []int16{d, u, p, x} {
			if got := IsImportant(from, to); got != important[[2]int16{from, to}] {
				t.Errorf("%d -> %d: got %v", from, to, got)
			}
		}
	}
}

func TestSanitizeMsg(t *testing.T) {
	if got := SanitizeMsg("a\x00b\xffc"); got != "ab�c" {
		t.Errorf("got %q", got)
	}
	long := SanitizeMsg(strings.Repeat("é", maxMsgBytes))
	if len(long) > maxMsgBytes+3 || !utf8.ValidString(long) {
		t.Errorf("truncation produced %d bytes, valid=%v", len(long), utf8.ValidString(long))
	}
}

func TestValidateGroupAndParent(t *testing.T) {
	group := New()
	group.Name, group.Type = "Group", TypeGroup
	if err := group.Validate(); err != nil {
		t.Errorf("group: %v", err)
	}
	zero := int32(0)
	m := valid()
	m.ParentID = &zero
	if err := m.Validate(); err == nil {
		t.Error("parentId 0 should fail")
	}
}

func TestPendingResultUsesNoRetry(t *testing.T) {
	m := Monitor{Type: TypeGroup, MaxRetries: 3}
	out := m.Next(&Previous{Status: StatusUp, Retries: 2}, Result{Pending: true, Msg: "Group empty"})
	if out.Status != StatusPending || out.Retries != 0 || out.Important {
		t.Errorf("got %+v", out)
	}
	if first := m.Next(nil, Result{Pending: true}); !first.Important {
		t.Error("the first beat is always important")
	}
}

func TestRedactedRemovesCredentials(t *testing.T) {
	m := valid()
	m.URL = "https://user:urlsecret@example.com/path?token=querysecret#frag"
	m.Headers = map[string]string{"X-Api-Key": "headersecret"}
	m.Body = `{"password":"bodysecret"}`
	r := m.Redacted()
	dump := r.URL + r.Headers["X-Api-Key"] + r.Body
	for _, secret := range []string{"urlsecret", "querysecret", "headersecret", "bodysecret"} {
		if strings.Contains(dump, secret) {
			t.Errorf("%s survived: %s", secret, dump)
		}
	}
	if !strings.HasPrefix(r.URL, "https://example.com/path") || r.Headers["X-Api-Key"] != "***" {
		t.Errorf("redacted: %+v", r)
	}
	if m.Headers["X-Api-Key"] != "headersecret" {
		t.Error("Redacted changed the original")
	}
	if got := (Monitor{URL: "::not a url"}).Redacted(); got.URL != "***" {
		t.Errorf("unparsable url: %q", got.URL)
	}
}
