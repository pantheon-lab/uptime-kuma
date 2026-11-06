package importer

import (
	"database/sql"
	"strings"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// row builds a monitor row where every column is set unless overridden with a
// zero sql.NullString (unset columns read as "").
func row(values map[string]string) map[string]sql.NullString {
	out := map[string]sql.NullString{}
	for k, v := range values {
		out[k] = sql.NullString{String: v, Valid: true}
	}
	return out
}

func TestConvertMonitorPushPingDNS(t *testing.T) {
	t.Run("push keeps its token", func(t *testing.T) {
		m, reason := convertMonitor(row(map[string]string{
			"name":       " heartbeat ",
			"type":       monitor.TypePush,
			"push_token": "tok-123456",
			"interval":   "60",
		}))
		if reason != "" {
			t.Fatalf("reason = %q, want none", reason)
		}
		if m.Type != monitor.TypePush || m.PushToken != "tok-123456" {
			t.Fatalf("got type %q token %q", m.Type, m.PushToken)
		}
		if m.Name != "heartbeat" {
			t.Fatalf("name = %q, want trimmed", m.Name)
		}
		if m.Active {
			t.Fatal("imported monitors must start paused")
		}
	})

	t.Run("push without a token is rejected", func(t *testing.T) {
		_, reason := convertMonitor(row(map[string]string{
			"name": "no token",
			"type": monitor.TypePush,
		}))
		if reason == "" {
			t.Fatal("want a reason for a push monitor with no token")
		}
	})

	t.Run("ping carries its packet settings", func(t *testing.T) {
		m, reason := convertMonitor(row(map[string]string{
			"name":                     "gateway",
			"type":                     monitor.TypePing,
			"hostname":                 "10.0.0.1",
			"interval":                 "60",
			"ping_count":               "10",
			"ping_per_request_timeout": "5",
			"packet_size":              "56",
			"maxretries":               "2",
		}))
		if reason != "" {
			t.Fatalf("reason = %q, want none", reason)
		}
		if m.Hostname != "10.0.0.1" || m.PingCount != 10 ||
			m.PingPerRequestTimeout != 5 || m.PacketSize != 56 || m.MaxRetries != 2 {
			t.Fatalf("got %+v", m)
		}
	})

	t.Run("dns keeps resolver settings", func(t *testing.T) {
		m, reason := convertMonitor(row(map[string]string{
			"name":               "example.com",
			"type":               monitor.TypeDNS,
			"hostname":           "example.com",
			"interval":           "60",
			"port":               "53",
			"dns_resolve_type":   "AAAA",
			"dns_resolve_server": "1.1.1.1",
		}))
		if reason != "" {
			t.Fatalf("reason = %q, want none", reason)
		}
		if m.DNSResolveType != "AAAA" || m.DNSResolveServer != "1.1.1.1" || m.Port != 53 {
			t.Fatalf("got %+v", m)
		}
	})

	t.Run("dns conditions are imported, and checked", func(t *testing.T) {
		m, reason := convertMonitor(row(map[string]string{
			"name": "conditional", "type": monitor.TypeDNS, "hostname": "example.com", "interval": "60",
			"conditions": `[{"type":"expression","andOr":"and","variable":"record","operator":"contains","value":"1.2"}]`,
		}))
		if reason != "" || !strings.Contains(string(m.Conditions), `"record"`) {
			t.Fatalf("imported: %q %s", reason, m.Conditions)
		}
		_, reason = convertMonitor(row(map[string]string{
			"name": "bad", "type": monitor.TypeDNS, "hostname": "example.com", "interval": "60",
			"conditions": `[{"type":"header"}]`,
		}))
		if !strings.HasPrefix(reason, "invalid setting: unknown condition type") {
			t.Fatalf("reason = %q", reason)
		}
	})

	t.Run("json query, websocket, manual and databases", func(t *testing.T) {
		manual := "1"
		for _, c := range []struct {
			fields map[string]string
			check  func(monitor.Monitor) bool
		}{
			{map[string]string{"type": monitor.TypeJSONQuery, "url": "https://x.example", "json_path": "status", "json_path_operator": "==", "expected_value": "ok", "accepted_statuscodes_json": `["200-299"]`},
				func(m monitor.Monitor) bool { return m.JSONPath == "status" && m.ExpectedValue == "ok" }},
			{map[string]string{"type": monitor.TypeWebSocket, "url": "wss://x.example/ws", "accepted_statuscodes_json": `["1000"]`, "ws_subprotocol": "chat", "ws_ignore_sec_websocket_accept_header": "1"},
				func(m monitor.Monitor) bool { return m.WSSubprotocol == "chat" && m.WSIgnoreAcceptHeader }},
			{map[string]string{"type": monitor.TypeManual, "manual_status": manual},
				func(m monitor.Monitor) bool { return m.ManualStatus != nil && *m.ManualStatus == 1 }},
			{map[string]string{"type": monitor.TypePostgres, "database_connection_string": "postgres://u:p@db/x", "database_query": "SELECT 2"},
				func(m monitor.Monitor) bool { return m.DatabaseQuery == "SELECT 2" }},
			{map[string]string{"type": monitor.TypeMySQL, "database_connection_string": "mysql://u@db/x", "radius_password": "secret"},
				func(m monitor.Monitor) bool { return m.DatabasePassword == "secret" }},
			{map[string]string{"type": monitor.TypeRedis, "database_connection_string": "rediss://db:6380", "ignore_tls": "1"},
				func(m monitor.Monitor) bool { return m.IgnoreTLS }},
		} {
			c.fields["name"], c.fields["interval"] = "x", "60"
			m, reason := convertMonitor(row(c.fields))
			if reason != "" || !c.check(m) {
				t.Errorf("%s: %q %+v", c.fields["type"], reason, m)
			}
		}
	})

	t.Run("unknown type is skipped", func(t *testing.T) {
		_, reason := convertMonitor(row(map[string]string{
			"name": "game",
			"type": "lighthouse",
		}))
		if reason != "unsupported monitor type: lighthouse" {
			t.Fatalf("reason = %q", reason)
		}
	})
}
