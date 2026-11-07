package monitor

import (
	"encoding/json"
	"slices"
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

func TestNotifyRules(t *testing.T) {
	const d, u, p, x = StatusDown, StatusUp, StatusPending, StatusMaintenance
	wanted := map[[2]int16]bool{{x, d}: true, {u, d}: true, {d, u}: true, {p, d}: true}
	for _, from := range []int16{d, u, p, x} {
		for _, to := range []int16{d, u, p, x} {
			if got := ImportantForNotification(from, to); got != wanted[[2]int16{from, to}] {
				t.Errorf("%d -> %d: got %v", from, to, got)
			}
		}
	}

	m := Monitor{}
	if out := m.Next(nil, Result{OK: true}); out.Notify {
		t.Error("a healthy first beat must not alert")
	}
	if out := m.Next(nil, Result{}); !out.Notify {
		t.Error("a failing first beat must alert")
	}
	if out := m.Next(&Previous{Status: StatusUp}, Result{}); !out.Notify {
		t.Error("going down must alert")
	}
	if out := m.Next(&Previous{Status: StatusDown}, Result{OK: true}); !out.Notify {
		t.Error("recovering must alert")
	}
	if out := m.Next(&Previous{Status: StatusDown}, Result{}); out.Notify {
		t.Error("staying down must not alert without resendInterval")
	}
	if out := m.Next(&Previous{Status: StatusUp}, Result{OK: true}); out.Notify {
		t.Error("staying up must not alert")
	}
	retrying := Monitor{MaxRetries: 1}
	if out := retrying.Next(&Previous{Status: StatusUp}, Result{}); out.Notify || out.Status != StatusPending {
		t.Errorf("a retry must not alert: %+v", out)
	}
}

func TestResendInterval(t *testing.T) {
	m := Monitor{ResendInterval: 3}
	prev := &Previous{Status: StatusUp}
	out := m.Next(prev, Result{})
	if !out.Notify || out.DownCount != 0 {
		t.Fatalf("going down: %+v", out)
	}
	var notified []bool
	prev = &Previous{Status: out.Status, Retries: out.Retries, DownCount: out.DownCount}
	for i := 0; i < 7; i++ {
		out = m.Next(prev, Result{})
		notified = append(notified, out.Notify)
		prev = &Previous{Status: out.Status, Retries: out.Retries, DownCount: out.DownCount}
	}
	if !slices.Equal(notified, []bool{false, false, true, false, false, true, false}) {
		t.Errorf("alerts: %v", notified)
	}
	out = m.Next(prev, Result{OK: true})
	if !out.Notify || out.DownCount != 0 {
		t.Errorf("recovery: %+v", out)
	}
}

func TestMaintenanceOutcome(t *testing.T) {
	m := New()
	for _, tc := range []struct {
		prev      *Previous
		important bool
	}{
		{nil, true},
		{&Previous{Status: StatusUp}, true},
		{&Previous{Status: StatusDown, Retries: 3, DownCount: 2}, true},
		{&Previous{Status: StatusMaintenance}, false},
	} {
		out := m.Maintenance(tc.prev)
		if out.Status != StatusMaintenance || out.Important != tc.important || out.Notify || out.Retries != 0 || out.DownCount != 0 {
			t.Errorf("prev %+v: %+v", tc.prev, out)
		}
	}
	// Leaving maintenance: a failure alerts, a recovery doesn't.
	if out := m.Next(&Previous{Status: StatusMaintenance}, Result{OK: false}); !out.Notify {
		t.Errorf("maintenance to down: %+v", out)
	}
	if out := m.Next(&Previous{Status: StatusMaintenance}, Result{OK: true}); out.Notify || !out.Important {
		t.Errorf("maintenance to up: %+v", out)
	}
}

func pushMonitor() Monitor {
	m := New()
	m.Name, m.Type = "sender", TypePush
	return m
}

func pingMonitor() Monitor {
	m := New()
	m.Name, m.Type, m.Hostname = "router", TypePing, "10.0.0.1"
	return m
}

func dnsMonitor() Monitor {
	m := New()
	m.Name, m.Type, m.Hostname = "records", TypeDNS, "example.com"
	m.Normalize()
	return m
}

func TestValidatePush(t *testing.T) {
	m := pushMonitor()
	if err := m.Validate(); err != nil {
		t.Errorf("push without a token: %v", err)
	}
	m.PushToken = "a-token_0123456789"
	if err := m.Validate(); err != nil {
		t.Errorf("push with a token: %v", err)
	}
	for name, mutate := range map[string]func(*Monitor){
		"too short":   func(m *Monitor) { m.PushToken = "short" },
		"space":       func(m *Monitor) { m.PushToken = "has space" },
		"punctuation": func(m *Monitor) { m.PushToken = "token!" },
	} {
		bad := pushMonitor()
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidatePing(t *testing.T) {
	m := pingMonitor()
	if err := m.Validate(); err != nil {
		t.Errorf("valid ping: %v", err)
	}
	// The whole window must fit in the check's deadline.
	m.Timeout = 1
	if err := m.Validate(); err == nil {
		t.Error("a timeout shorter than the per-request timeout should fail")
	}
	m.Timeout = 0
	for name, mutate := range map[string]func(*Monitor){
		"hostname":     func(m *Monitor) { m.Hostname = "  " },
		"count":        func(m *Monitor) { m.PingCount = maxPingCount + 1 },
		"count zero":   func(m *Monitor) { m.PingCount = 0 },
		"per request":  func(m *Monitor) { m.PingPerRequestTimeout = maxPingPerRequest + 1 },
		"packet size":  func(m *Monitor) { m.PacketSize = maxPacketSize + 1 },
		"timeout":      func(m *Monitor) { m.Timeout = maxPingTimeout + 1 },
		"timeout zero": func(m *Monitor) { m.Timeout = 0.4 },
	} {
		bad := pingMonitor()
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestValidateDNS(t *testing.T) {
	m := dnsMonitor()
	if err := m.Validate(); err != nil {
		t.Errorf("valid DNS: %v", err)
	}
	if m.Port != defaultDNSPort {
		t.Errorf("Normalize gave port %d, want %d", m.Port, defaultDNSPort)
	}
	ptr := dnsMonitor()
	ptr.Hostname, ptr.DNSResolveType = "10.0.0.1", "PTR"
	if err := ptr.Validate(); err != nil {
		t.Errorf("PTR of an address: %v", err)
	}
	for name, mutate := range map[string]func(*Monitor){
		"hostname":    func(m *Monitor) { m.Hostname = " " },
		"port":        func(m *Monitor) { m.Port = 65536 },
		"record type": func(m *Monitor) { m.DNSResolveType = "TXT2" },
		"PTR of name": func(m *Monitor) { m.Hostname, m.DNSResolveType = "example.com", "PTR" },
		"servers":     func(m *Monitor) { m.DNSResolveServer = " , " },
	} {
		bad := dnsMonitor()
		mutate(&bad)
		if err := bad.Validate(); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestNewPushToken(t *testing.T) {
	seen := map[string]bool{}
	for range 32 {
		token := NewPushToken()
		if len(token) != 32 {
			t.Fatalf("length %d: %q", len(token), token)
		}
		if !pushTokenFormat.MatchString(token) {
			t.Fatalf("not accepted by the validator: %q", token)
		}
		if seen[token] {
			t.Fatalf("repeated token: %q", token)
		}
		seen[token] = true
	}
}

func TestRedactedRemovesPushToken(t *testing.T) {
	m := pushMonitor()
	m.PushToken = "supersecrettoken"
	if got := m.Redacted().PushToken; got != redactedValue {
		t.Errorf("redacted token: %q", got)
	}
	if m.PushToken != "supersecrettoken" {
		t.Error("Redacted changed the original")
	}
}

func TestNextPush(t *testing.T) {
	m := pushMonitor()
	m.MaxRetries = 1
	// The sender's own words become the message, and the first beat is important.
	out := m.NextPush(nil, Push{Up: true, Msg: "all good"})
	if out.Status != StatusUp || !out.Important || out.Msg != "all good" || out.Ping != nil {
		t.Errorf("first push: %+v", out)
	}
	// A failure retries like any other check...
	out = m.NextPush(&Previous{Status: StatusUp}, Push{Up: false, Msg: "late"})
	if out.Status != StatusPending || out.Retries != 1 {
		t.Errorf("retried failure: %+v", out)
	}
	// ...unless the sender reported the down itself, which is final.
	out = m.NextPush(&Previous{Status: StatusUp}, Push{ExplicitDown: true, Msg: "stopped"})
	if out.Status != StatusDown || out.Retries != 0 || !out.Important {
		t.Errorf("explicit down: %+v", out)
	}
	// The message is shown to users, so it's cleaned first.
	out = m.NextPush(&Previous{Status: StatusUp}, Push{Up: true, Msg: "a\x00b"})
	if out.Msg != "ab" {
		t.Errorf("message: %q", out.Msg)
	}
	// A pushed ping reaches the beat.
	ping := 12.5
	out = m.NextPush(nil, Push{Up: true, Ping: &ping})
	if out.Ping == nil || *out.Ping != ping {
		t.Errorf("ping: %+v", out.Ping)
	}
	// Upside down senders: an idle monitor is up, a push is down.
	upside := pushMonitor()
	upside.UpsideDown = true
	if out := upside.NextPush(&Previous{Status: StatusDown}, Push{Up: true}); out.Status != StatusDown {
		t.Errorf("upside down push: %+v", out)
	}
	if out := upside.Next(&Previous{Status: StatusUp}, Result{Msg: NoHeartbeatMsg}); out.Status != StatusUp {
		t.Errorf("upside down window: %+v", out)
	}
}

func TestValidatePhase4aTypes(t *testing.T) {
	valid := func(kind string, set func(*Monitor)) Monitor {
		m := New()
		m.Name, m.Type = "x", kind
		set(&m)
		m.Normalize()
		return m
	}
	for name, c := range map[string]struct {
		m  Monitor
		ok bool
	}{
		"json query":          {valid(TypeJSONQuery, func(m *Monitor) { m.URL, m.JSONPath, m.ExpectedValue = "https://a.b", "data.status", "ok" }), true},
		"json query bad path": {valid(TypeJSONQuery, func(m *Monitor) { m.URL, m.JSONPath = "https://a.b", "data[" }), false},
		"json query bad op":   {valid(TypeJSONQuery, func(m *Monitor) { m.URL, m.JSONPathOperator = "https://a.b", "~=" }), false},
		"manual":              {valid(TypeManual, func(m *Monitor) {}), true},
		"manual bad status":   {valid(TypeManual, func(m *Monitor) { s := int16(3); m.ManualStatus = &s }), false},
		"websocket":           {valid(TypeWebSocket, func(m *Monitor) { m.URL = "wss://a.b/ws" }), true},
		"websocket http url":  {valid(TypeWebSocket, func(m *Monitor) { m.URL = "https://a.b/ws" }), false},
		"postgres":            {valid(TypePostgres, func(m *Monitor) { m.DatabaseConnectionString = "postgres://u:p@db:5432/x" }), true},
		"postgres wrong url":  {valid(TypePostgres, func(m *Monitor) { m.DatabaseConnectionString = "mysql://u:p@db/x" }), false},
		"mysql":               {valid(TypeMySQL, func(m *Monitor) { m.DatabaseConnectionString = "mysql://u@db/x" }), true},
		"redis":               {valid(TypeRedis, func(m *Monitor) { m.DatabaseConnectionString = "rediss://db:6380/0" }), true},
		"redis empty":         {valid(TypeRedis, func(m *Monitor) {}), false},
		"conditions on http": {valid(TypeHTTP, func(m *Monitor) {
			m.URL, m.Conditions = "https://a.b", json.RawMessage(`[{"type":"expression","variable":"record","operator":"equals","value":"x"}]`)
		}), false},
		"mysql conditions": {valid(TypeMySQL, func(m *Monitor) {
			m.DatabaseConnectionString, m.Conditions = "mysql://db/x", json.RawMessage(`[{"type":"expression","variable":"result","operator":"equals","value":"1"}]`)
		}), true},
		"dns wrong variable": {valid(TypeDNS, func(m *Monitor) {
			m.Hostname, m.Conditions = "a.b", json.RawMessage(`[{"type":"expression","variable":"result","operator":"equals","value":"1"}]`)
		}), false},
	} {
		if err := c.m.Validate(); (err == nil) != c.ok {
			t.Errorf("%s: %v", name, err)
		}
	}
}

func TestRedactedHidesDatabaseCredentials(t *testing.T) {
	m := Monitor{DatabaseConnectionString: "postgres://user:secret@db:5432/x?sslpassword=p", DatabasePassword: "hunter2"}
	r := m.Redacted()
	if strings.Contains(r.DatabaseConnectionString, "secret") || strings.Contains(r.DatabaseConnectionString, "sslpassword") || r.DatabasePassword != "***" {
		t.Fatalf("%+v", r)
	}
}
