package importer

import (
	"database/sql"
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

	t.Run("dns with conditions is skipped", func(t *testing.T) {
		_, reason := convertMonitor(row(map[string]string{
			"name":       "conditional",
			"type":       monitor.TypeDNS,
			"hostname":   "example.com",
			"conditions": `[{"type":"header"}]`,
		}))
		if reason != "unsupported setting: conditions" {
			t.Fatalf("reason = %q", reason)
		}
	})

	t.Run("unknown type is skipped", func(t *testing.T) {
		_, reason := convertMonitor(row(map[string]string{
			"name": "steam",
			"type": "steam",
		}))
		if reason != "unsupported monitor type: steam" {
			t.Fatalf("reason = %q", reason)
		}
	})
}
