package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// TailscaleSocket is tailscaled's socket, which TAILSCALE_SOCKET can move.
var TailscaleSocket = func() string {
	if path := os.Getenv("TAILSCALE_SOCKET"); path != "" {
		return path
	}
	return "/var/run/tailscale/tailscaled.sock"
}()

// tailscaleClient talks to tailscaled's local API over its socket, as the tailscale command does.
func tailscaleClient() *http.Client {
	return &http.Client{Transport: &http.Transport{
		DisableKeepAlives: true,
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", TailscaleSocket)
		},
	}}
}

func tailscaleRequest(ctx context.Context, method, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, method, "http://local-tailscaled.sock/localapi/v0/"+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Sec-Tailscale", "localapi")
	res, err := tailscaleClient().Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return fmt.Errorf("can't reach tailscaled (%w). In a container, mount its socket, %s", err, TailscaleSocket)
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("tailscaled answered %d: %s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	return json.Unmarshal(body, v)
}

// tailscaleIP finds a peer's Tailscale IP from its host name or MagicDNS name, as `tailscale
// ping` does. An IP address is used as it is.
func tailscaleIP(ctx context.Context, target string) (string, error) {
	if ip := net.ParseIP(target); ip != nil {
		return target, nil
	}
	var status struct {
		Peer map[string]struct {
			HostName     string
			DNSName      string
			TailscaleIPs []string
		}
	}
	if err := tailscaleRequest(ctx, http.MethodGet, "status", &status); err != nil {
		return "", err
	}
	want := strings.ToLower(strings.TrimSuffix(target, "."))
	for _, peer := range status.Peer {
		dnsName := strings.ToLower(strings.TrimSuffix(peer.DNSName, "."))
		short, _, _ := strings.Cut(dnsName, ".")
		if (dnsName == want || short == want || strings.EqualFold(peer.HostName, want)) && len(peer.TailscaleIPs) > 0 {
			return peer.TailscaleIPs[0], nil
		}
	}
	return "", fmt.Errorf("no matching peer for %q", target)
}

// runTailscalePing sends one disco ping, as `tailscale ping --c 1` did in Node.js.
func runTailscalePing(ctx context.Context, m monitor.Monitor) monitor.Result {
	fail := func(format string, args ...any) monitor.Result {
		return monitor.Result{Msg: "Error checking Tailscale ping: " + fmt.Sprintf(format, args...)}
	}
	ip, err := tailscaleIP(ctx, strings.TrimSpace(m.Hostname))
	if err != nil {
		if strings.HasPrefix(err.Error(), "no matching peer") {
			return fail("Nonexistent or inaccessible due to ACLs: %v", err)
		}
		return fail("%v", err)
	}
	var result struct {
		Err            string
		LatencySeconds float64
		NodeName       string
		IsLocalIP      bool
	}
	err = tailscaleRequest(ctx, http.MethodPost, "ping?type=disco&ip="+url.QueryEscape(ip), &result)
	switch {
	case ctx.Err() != nil:
		return fail("Ping timed out after %s", m.EffectiveTimeout().Round(time.Millisecond))
	case err != nil:
		return fail("%v", err)
	case result.IsLocalIP:
		return fail("Tailscale only works if used on other machines: %s", result.Err)
	case strings.Contains(result.Err, "no matching peer"):
		return fail("Nonexistent or inaccessible due to ACLs: %s", result.Err)
	case strings.Contains(result.Err, "timeout") || strings.Contains(result.Err, "timed out"):
		return fail("Ping timed out: %s", result.Err)
	case result.Err != "":
		return fail("%s", result.Err)
	}
	ping := result.LatencySeconds * 1000
	return monitor.Result{OK: true, Ping: &ping, Msg: "OK"}
}
