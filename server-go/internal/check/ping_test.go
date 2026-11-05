package check

import (
	"context"
	"strings"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// requirePing skips the test where the host doesn't allow unprivileged ICMP, which the
// net.ipv4.ping_group_range sysctl decides. That is what the image relies on instead of
// the NET_RAW capability, so most of the test runs only where it's configured.
func requirePing(t *testing.T) {
	t.Helper()
	if err := PingAllowed(); err != nil {
		t.Skipf("unprivileged ICMP isn't available here: %v", err)
	}
}

func pingMonitorType(hostname string) monitor.Monitor {
	m := monitor.New()
	m.Name, m.Type, m.Hostname = "router", monitor.TypePing, hostname
	m.PingCount, m.PacketSize, m.Timeout = 2, 56, 5
	return m
}

func TestPingAnswers(t *testing.T) {
	requirePing(t)
	r := newChecker(t).Run(context.Background(), pingMonitorType("127.0.0.1"))
	if !r.OK || r.Ping == nil || *r.Ping < 0 {
		t.Fatalf("result: %+v", r)
	}
	if r.Msg != "2 of 2 requests answered" {
		t.Errorf("message: %q", r.Msg)
	}
}

func TestPingAnUnknownHost(t *testing.T) {
	requirePing(t)
	r := newChecker(t).Run(context.Background(), pingMonitorType("no-such-host.invalid"))
	if r.OK || r.Msg == "" {
		t.Errorf("result: %+v", r)
	}
	if r.Ping != nil {
		t.Errorf("a failed check has no ping: %+v", r.Ping)
	}
}

func TestPingAllowedMessage(t *testing.T) {
	// Where ICMP is permitted, the message a denied process would get is the one the
	// startup warning uses, and it's the only part that can't be exercised here.
	if err := PingAllowed(); err != nil {
		if !strings.Contains(err.Error(), "ping_group_range") {
			t.Errorf("denied message: %v", err)
		}
	}
}
