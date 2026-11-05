package check

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
	"golang.org/x/net/ipv6"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const (
	// pingSpacing is the time between requests, the same as the ping command's.
	pingSpacing = time.Second
	// pingHeaderRoom is more than an ICMP header, so that a read never cuts off a reply.
	pingHeaderRoom = 64

	protocolICMP   = 1
	protocolICMPv6 = 58

	pingDeniedMsg = "Ping isn't allowed: the group of this process must be in the net.ipv4.ping_group_range sysctl"
)

// PingAllowed reports whether this process can open an unprivileged ICMP socket, which
// the host's net.ipv4.ping_group_range decides. It applies to IPv4 and IPv6 alike.
func PingAllowed() error {
	conn, err := icmp.ListenPacket("udp4", "0.0.0.0")
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return errors.New(pingDeniedMsg)
		}
		return err
	}
	return conn.Close()
}

// runPing follows the Node.js backend's use of the ping command with a deadline: it sends a
// request a second until PingCount replies have arrived or the check's time is up. Any reply
// makes it UP, and the first reply's time is the beat's ping.
func (c *Checker) runPing(ctx context.Context, m monitor.Monitor) monitor.Result {
	host := strings.Trim(strings.TrimSpace(m.Hostname), "[]")
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("server.address", host))
	ctx, span := otel.Tracer(tracerName).Start(ctx, "icmp.echo")
	defer span.End()

	target, err := pingTarget(ctx, host)
	if err != nil {
		return monitor.Result{Msg: requestError(err, m.EffectiveTimeout())}
	}
	echo, err := newPinger(target)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return monitor.Result{Msg: pingDeniedMsg}
		}
		return monitor.Result{Msg: err.Error()}
	}
	defer func() { _ = echo.conn.Close() }()
	// Closing the socket ends a read straight away, on the deadline or at shutdown.
	defer context.AfterFunc(ctx, func() { _ = echo.conn.Close() })()

	payload := make([]byte, m.PacketSize)
	buffer := make([]byte, int(m.PacketSize)+pingHeaderRoom)
	perRequest := time.Duration(m.PingPerRequestTimeout) * time.Second
	var first time.Duration
	var replies, sent int
	var lastErr error
	for replies < int(m.PingCount) && ctx.Err() == nil {
		sent++
		started := time.Now()
		rtt, err := echo.exchange(sent, payload, buffer, perRequest)
		switch {
		case err == nil:
			if replies == 0 {
				first = rtt
			}
			replies++
		case ctx.Err() == nil:
			lastErr = err
		}
		if replies == int(m.PingCount) {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(pingSpacing - time.Since(started)):
		}
	}

	if replies == 0 {
		if lastErr != nil && !isTimeout(lastErr) {
			return monitor.Result{Msg: lastErr.Error()}
		}
		return monitor.Result{Msg: fmt.Sprintf("No reply from %s to %d requests", target.IP, sent)}
	}
	ping := float64(first.Microseconds()) / 1000
	return monitor.Result{OK: true, Ping: &ping, Msg: fmt.Sprintf("%d of %d requests answered", replies, sent)}
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// pingTarget resolves a host to one address, preferring IPv4, as the Node.js backend does.
func pingTarget(ctx context.Context, host string) (*net.IPAddr, error) {
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, address := range addresses {
		if address.IP.To4() != nil {
			return &address, nil
		}
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("no addresses for %s", host)
	}
	return &addresses[0], nil
}

// pinger is one check's ICMP socket. The kernel gives each socket its own identifier, and only
// passes it the replies with that identifier, so checks running at once can't see each other's.
type pinger struct {
	conn     *icmp.PacketConn
	target   net.Addr
	protocol int
	request  icmp.Type
	reply    icmp.Type
}

func newPinger(target *net.IPAddr) (*pinger, error) {
	p := &pinger{target: &net.UDPAddr{IP: target.IP, Zone: target.Zone}}
	var err error
	if target.IP.To4() != nil {
		p.protocol, p.request, p.reply = protocolICMP, ipv4.ICMPTypeEcho, ipv4.ICMPTypeEchoReply
		p.conn, err = icmp.ListenPacket("udp4", "0.0.0.0")
	} else {
		p.protocol, p.request, p.reply = protocolICMPv6, ipv6.ICMPTypeEchoRequest, ipv6.ICMPTypeEchoReply
		p.conn, err = icmp.ListenPacket("udp6", "::")
	}
	return p, err
}

// exchange sends one request and waits for its reply, ignoring late replies to earlier ones.
func (p *pinger) exchange(sequence int, payload, buffer []byte, timeout time.Duration) (time.Duration, error) {
	sequence &= 0xffff
	request := icmp.Message{Type: p.request, Body: &icmp.Echo{Seq: sequence, Data: payload}}
	data, err := request.Marshal(nil)
	if err != nil {
		return 0, err
	}
	started := time.Now()
	if err := p.conn.SetReadDeadline(started.Add(timeout)); err != nil {
		return 0, err
	}
	if _, err := p.conn.WriteTo(data, p.target); err != nil {
		return 0, err
	}
	for {
		n, _, err := p.conn.ReadFrom(buffer)
		if err != nil {
			return 0, err
		}
		rtt := time.Since(started)
		reply, err := icmp.ParseMessage(p.protocol, buffer[:n])
		if err != nil || reply.Type != p.reply {
			continue
		}
		if echo, ok := reply.Body.(*icmp.Echo); ok && echo.Seq == sequence {
			return rtt, nil
		}
	}
}
