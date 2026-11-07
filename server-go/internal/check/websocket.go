package check

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/coder/websocket"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// wsCloseNames are the Node.js backend's names of WebSocket close codes.
var wsCloseNames = map[int]string{
	1002: "Protocol error", 1003: "Unsupported Data", 1005: "No Status Received", 1006: "Abnormal Closure",
	1007: "Invalid frame payload data", 1008: "Policy Violation", 1009: "Message Too Big",
	1010: "Mandatory Extension Missing", 1011: "Internal Error", 1012: "Service Restart", 1013: "Try Again Later",
	1014: "Bad Gateway", 1015: "TLS Handshake Failed", 3000: "Unauthorized", 3003: "Forbidden", 3008: "Timeout",
}

// runWebSocket opens a WebSocket and closes it at once. The close code it ends with must be one
// of the accepted codes, 1000 by default, as in the Node.js backend.
func (c *Checker) runWebSocket(ctx context.Context, m monitor.Monitor) monitor.Result {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("server.address", hostOf(m.URL)))
	header := http.Header{}
	for name, value := range m.Headers {
		header.Set(name, value)
	}
	var protocols []string
	for p := range strings.SplitSeq(strings.Join(strings.Fields(m.WSSubprotocol), ""), ",") {
		if p != "" {
			protocols = append(protocols, p)
		}
	}
	transport := c.verified.Clone()
	if m.IgnoreTLS {
		transport = c.insecure.Clone()
	}
	// The handshake needs HTTP/1.1.
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	client := &http.Client{Transport: transport}
	defer client.CloseIdleConnections()

	start := time.Now()
	conn, res, err := websocket.Dial(ctx, m.URL, &websocket.DialOptions{HTTPClient: client, HTTPHeader: header, Subprotocols: protocols})
	if err != nil {
		if m.WSIgnoreAcceptHeader && strings.Contains(err.Error(), "Sec-WebSocket-Accept") {
			return c.acceptedClose(m, 1000, "1000 - OK", start)
		}
		if res != nil && res.StatusCode != http.StatusSwitchingProtocols {
			return monitor.Result{Msg: fmt.Sprintf("Unexpected server response: %d", res.StatusCode)}
		}
		if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
			return monitor.Result{Msg: "Opening handshake has timed out"}
		}
		return monitor.Result{Msg: unwrapDialError(err)}
	}
	code := 1000
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		code = int(websocket.CloseStatus(err))
		if code == -1 {
			code = 1006
		}
	}
	return c.acceptedClose(m, code, strconv.Itoa(code)+" - OK", start)
}

func (c *Checker) acceptedClose(m monitor.Monitor, code int, msg string, start time.Time) monitor.Result {
	if !m.Accepts(code) {
		if name, found := wsCloseNames[code]; found {
			return monitor.Result{Msg: name}
		}
		return monitor.Result{Msg: fmt.Sprintf("Unexpected status code: %d", code)}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: msg}
}

func unwrapDialError(err error) string {
	msg := err.Error()
	// coder/websocket wraps errors as "failed to WebSocket dial: ...".
	msg = strings.TrimPrefix(msg, "failed to WebSocket dial: ")
	if i := strings.LastIndex(msg, "\": "); i >= 0 && strings.Contains(msg[:i], "://") {
		msg = msg[i+3:] // Leave out the method and URL that net/http adds, which can hold credentials.
	}
	return msg
}

func hostOf(raw string) string {
	if i := strings.Index(raw, "://"); i >= 0 {
		rest := raw[i+3:]
		if j := strings.IndexAny(rest, "/?#"); j >= 0 {
			rest = rest[:j]
		}
		if k := strings.LastIndex(rest, "@"); k >= 0 {
			rest = rest[k+1:]
		}
		return rest
	}
	return ""
}
