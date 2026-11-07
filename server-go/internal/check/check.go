// Package check runs monitor checks against their targets.
package check

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/httptrace/otelhttptrace"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const (
	// maxResponseBytes bounds memory per check. A keyword past this point is not found.
	maxResponseBytes = 10 << 20
	acceptHeader     = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9"
	userAgent        = "Uptime-Kuma-Go/0"
	tracerName       = "kuma-analytics/check"
	snippetRunes     = 50
)

var markupOrSpace = regexp.MustCompile(`<[^>]*>?|[\n\r]|\s+`)

var errTooManyRedirects = errors.New("Maximum number of redirects exceeded") //nolint:staticcheck // Shown to users, same text as Node.js.

// Checker runs checks. Connections are reused between checks, because opening a
// new socket per check exhausts local ports with thousands of monitors.
type Checker struct {
	verified *http.Transport
	insecure *http.Transport
	dialer   net.Dialer
	groups   GroupReader
	// dnsResults is optional, and set before checks start.
	dnsResults DNSResultWriter
	// steamKey is optional, and set before checks start.
	steamKey SteamKey
	// dockerHosts and browsers are optional, and set before checks start.
	dockerHosts DockerHostReader
	browsers    BrowserStore
	proxies     ProxyReader
	// globalpingToken is optional, and set before checks start.
	globalpingToken SteamKey
	// extra holds the transports of monitors with options, and tokens their OAuth2 tokens.
	extra  transports
	tokens oauthTokens
}

// Child is what a group needs to know about one of its direct children.
type Child struct {
	Name   string
	Active bool
	// Status is the last beat's status, nil if there are no beats yet.
	Status *int16
}

// GroupReader reads a group's direct children.
type GroupReader interface {
	GroupChildren(ctx context.Context, groupID int32) ([]Child, error)
}

// New creates a Checker. groups is only needed for group monitors.
func New(groups GroupReader) *Checker {
	transport := func(skipVerify bool) *http.Transport {
		return &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			ForceAttemptHTTP2:   true,
			MaxIdleConns:        0,
			MaxIdleConnsPerHost: 4,
			IdleConnTimeout:     90 * time.Second,
			TLSClientConfig:     &tls.Config{InsecureSkipVerify: skipVerify, MinVersion: tls.VersionTLS12}, //nolint:gosec // Per-monitor opt-in.
		}
	}
	return &Checker{verified: transport(false), insecure: transport(true), groups: groups}
}

// Close releases idle connections.
func (c *Checker) Close() {
	c.verified.CloseIdleConnections()
	c.insecure.CloseIdleConnections()
	c.extra.mu.Lock()
	for _, t := range c.extra.byID {
		t.CloseIdleConnections()
	}
	c.extra.mu.Unlock()
}

// Run performs one check, bounded by the monitor's timeout.
func (c *Checker) Run(ctx context.Context, m monitor.Monitor) monitor.Result {
	timeout := m.EffectiveTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch m.Type {
	case monitor.TypeHTTP, monitor.TypeKeyword, monitor.TypeJSONQuery:
		return c.runHTTP(ctx, m, timeout)
	case monitor.TypeWebSocket:
		return c.runWebSocket(ctx, m)
	case monitor.TypePostgres:
		return runPostgres(ctx, m)
	case monitor.TypeMySQL:
		return runMySQL(ctx, m)
	case monitor.TypeRedis:
		return runRedis(ctx, m)
	case monitor.TypeManual:
		return runManual(m)
	case monitor.TypeSQLServer:
		return runSQLServer(ctx, m)
	case monitor.TypeOracle:
		return runOracle(ctx, m)
	case monitor.TypeMongoDB:
		return runMongoDB(ctx, m)
	case monitor.TypeMQTT:
		return runMQTT(ctx, m)
	case monitor.TypeRabbitMQ:
		return c.runRabbitMQ(ctx, m)
	case monitor.TypeKafka:
		return runKafka(ctx, m)
	case monitor.TypeNTP:
		return runNTP(ctx, m)
	case monitor.TypeSNMP:
		return runSNMP(ctx, m)
	case monitor.TypeSMTP:
		return runSMTP(ctx, m)
	case monitor.TypeSIP:
		return runSIP(ctx, m)
	case monitor.TypeRADIUS:
		return runRADIUS(ctx, m)
	case monitor.TypeSteam:
		return c.runSteam(ctx, m)
	case monitor.TypeDocker:
		return c.runDocker(ctx, m)
	case monitor.TypeSystemService:
		return runSystemService(ctx, m)
	case monitor.TypePM2:
		return runPM2(ctx, m)
	case monitor.TypeSFTP:
		return runSFTP(ctx, m)
	case monitor.TypeGRPC:
		return runGRPC(ctx, m)
	case monitor.TypeRealBrowser:
		return c.runRealBrowser(ctx, m)
	case monitor.TypeGlobalping:
		return c.runGlobalping(ctx, m)
	case monitor.TypeGameDig:
		return runGameDig(ctx, m)
	case monitor.TypeTailscale:
		return runTailscalePing(ctx, m)
	case monitor.TypePort:
		return c.runPort(ctx, m)
	case monitor.TypeGroup:
		return c.runGroup(ctx, m)
	case monitor.TypePing:
		return c.runPing(ctx, m)
	case monitor.TypeDNS:
		return c.runDNS(ctx, m)
	default:
		return monitor.Result{Msg: "Unknown Monitor Type"}
	}
}

// runGroup follows the Node.js backend: a group is down if any direct child is down,
// pending if one is pending or has no beats yet, and pending if it has no children.
// Paused children don't count.
func (c *Checker) runGroup(ctx context.Context, m monitor.Monitor) monitor.Result {
	if c.groups == nil {
		return monitor.Result{Msg: "Groups are not available"}
	}
	children, err := c.groups.GroupChildren(ctx, m.ID)
	if err != nil {
		return monitor.Result{Msg: "Failed to read child monitors"}
	}
	if len(children) == 0 {
		return monitor.Result{Pending: true, Msg: "Group empty"}
	}

	var down, pending []string
	for _, child := range children {
		switch {
		case !child.Active:
		case child.Status == nil || *child.Status == monitor.StatusPending:
			pending = append(pending, child.Name)
		case *child.Status == monitor.StatusDown:
			down = append(down, child.Name)
		}
	}
	switch {
	case len(down) > 0:
		msg := "Child monitors down: " + strings.Join(down, ", ")
		if len(pending) > 0 {
			msg += "; pending: " + strings.Join(pending, ", ")
		}
		return monitor.Result{Msg: msg}
	case len(pending) > 0:
		return monitor.Result{Pending: true, Msg: "Pending child monitors: " + strings.Join(pending, ", ")}
	}
	return monitor.Result{OK: true, Msg: "All children up and running"}
}

func (c *Checker) runPort(ctx context.Context, m monitor.Monitor) monitor.Result {
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("server.address", m.Hostname), attribute.Int("server.port", int(m.Port)))
	_, span := otel.Tracer(tracerName).Start(ctx, "tcp.connect")
	defer span.End()
	start := time.Now()
	conn, err := c.dialer.DialContext(ctx, "tcp", net.JoinHostPort(m.Hostname, strconv.Itoa(int(m.Port))))
	if err != nil {
		return monitor.Result{Msg: "Connection failed"}
	}
	ping := elapsedMs(start)
	_ = conn.Close()
	ping = math.Round(ping)
	return monitor.Result{OK: true, Ping: &ping, Msg: fmt.Sprintf("%.0f ms", ping)}
}

func (c *Checker) runHTTP(ctx context.Context, m monitor.Monitor, timeout time.Duration) monitor.Result {
	contentType := ""
	if strings.TrimSpace(m.Body) != "" {
		switch m.BodyEncoding {
		case "form":
			contentType = "application/x-www-form-urlencoded"
		case "xml":
			contentType = "text/xml; charset=utf-8"
		default:
			contentType = "application/json"
		}
	}
	target := m.URL
	if m.CacheBust {
		target = cacheBustURL(target)
	}
	transport, err := c.transportFor(ctx, m)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	client := http.Client{Transport: roundTripper(transport, m), CheckRedirect: redirectPolicy(m.MaxRedirects)}

	// Only the host goes on the span: URLs can carry credentials, and the query string
	// and headers can carry tokens.
	span := trace.SpanFromContext(ctx)
	newRequest := func(freshToken bool) (*http.Request, error) {
		var body io.Reader
		if contentType != "" {
			body = strings.NewReader(m.Body)
		}
		req, err := http.NewRequestWithContext(ctx, m.Method, target, body)
		if err != nil {
			return nil, err
		}
		req.Header.Set("Accept", acceptHeader)
		req.Header.Set("User-Agent", userAgent)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		// Headers the monitor sets replace the login's, as in Node.js.
		if err := c.authorize(ctx, req, m, freshToken); err != nil {
			return nil, err
		}
		for name, value := range m.Headers {
			req.Header.Set(name, value)
		}
		if m.PropagateTrace {
			propagation.TraceContext{}.Inject(ctx, propagation.HeaderCarrier(req.Header))
		}
		return req.WithContext(httptrace.WithClientTrace(ctx, otelhttptrace.NewClientTrace(ctx, otelhttptrace.WithoutHeaders()))), nil
	}
	req, err := newRequest(false)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	span.SetAttributes(attribute.String("server.address", req.URL.Hostname()), attribute.String("http.request.method", m.Method))

	start := time.Now()
	res, err := client.Do(req)
	if err == nil && res.StatusCode == http.StatusUnauthorized && m.AuthMethod == "oauth2-cc" {
		// The token may have been revoked, so a new one gets one more try, as in Node.js.
		_ = res.Body.Close()
		if req, err = newRequest(true); err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		res, err = client.Do(req)
	}
	if err != nil {
		return monitor.Result{Msg: requestError(err, timeout)}
	}
	defer func() { _ = res.Body.Close() }()
	span.SetAttributes(attribute.Int("http.response.status_code", res.StatusCode))
	tlsState := res.TLS

	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return monitor.Result{Msg: requestError(err, timeout), TLS: tlsState}
	}
	ping := elapsedMs(start)

	if !m.Accepts(res.StatusCode) {
		result := monitor.Result{TLS: tlsState, Msg: fmt.Sprintf("Request failed with status code %d", res.StatusCode)}
		if m.SaveErrorResponse {
			result.Response = savedResponse(m, data)
		}
		return result
	}
	result := c.httpResult(m, res, data, ping)
	result.TLS = tlsState
	// As in Node.js, an accepted response is only kept if error responses are too.
	if m.SaveResponse && m.SaveErrorResponse {
		result.Response = savedResponse(m, data)
	}
	return result
}

// httpResult decides an accepted response's result by the monitor's type.
func (c *Checker) httpResult(m monitor.Monitor, res *http.Response, data []byte, ping float64) monitor.Result {
	reason := strings.TrimSpace(strings.TrimPrefix(res.Status, strconv.Itoa(res.StatusCode)))
	if reason == "" {
		reason = http.StatusText(res.StatusCode)
	}
	msg := fmt.Sprintf("%d - %s", res.StatusCode, reason)

	switch m.Type {
	case monitor.TypeHTTP:
		return monitor.Result{OK: true, Ping: &ping, Msg: msg}
	case monitor.TypeJSONQuery:
		result := evaluateJSONQuery(data, m)
		if result.OK {
			result.Ping = &ping
		} else if m.RetryOnlyOnStatusCodeFailure && strings.Contains(result.Msg, "JSON query does not pass") {
			// The status was accepted, so retrying wouldn't help.
			result.NoRetry = true
		}
		return result
	}

	found := bytes.Contains(data, []byte(m.Keyword))
	if found == !m.InvertKeyword {
		state := "not"
		if found {
			state = "is"
		}
		return monitor.Result{OK: true, Ping: &ping, Msg: fmt.Sprintf("%s, keyword %s found", msg, state)}
	}
	state := "not"
	if found {
		state = "present"
	}
	return monitor.Result{Msg: fmt.Sprintf("%s, but keyword is %s in [%s]", msg, state, snippet(data))}
}

func redirectPolicy(limit int32) func(*http.Request, []*http.Request) error {
	return func(_ *http.Request, via []*http.Request) error {
		if limit == 0 {
			// Report the 3xx itself, so that accepted status codes decide the outcome.
			return http.ErrUseLastResponse
		}
		if len(via) > int(limit) {
			return errTooManyRedirects
		}
		return nil
	}
}

// requestError removes the method and URL that net/http wraps errors with, because
// the URL can carry credentials and the monitor already identifies the target.
func requestError(err error, timeout time.Duration) string {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, http.ErrHandlerTimeout) {
		return fmt.Sprintf("timeout of %dms exceeded", timeout.Milliseconds())
	}
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		if urlErr.Timeout() {
			return fmt.Sprintf("timeout of %dms exceeded", timeout.Milliseconds())
		}
		return urlErr.Err.Error()
	}
	return err.Error()
}

func snippet(data []byte) string {
	runes := []rune(strings.TrimSpace(markupOrSpace.ReplaceAllString(string(data), " ")))
	if len(runes) > snippetRunes {
		return string(runes[:snippetRunes-3]) + "..."
	}
	return string(runes)
}

func elapsedMs(start time.Time) float64 {
	return float64(time.Since(start).Microseconds()) / 1000
}
