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
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

const (
	// maxResponseBytes bounds memory per check. A keyword past this point is not found.
	maxResponseBytes = 10 << 20
	acceptHeader     = "text/html,application/xhtml+xml,application/xml;q=0.9,image/avif,image/webp,image/apng,*/*;q=0.8,application/signed-exchange;v=b3;q=0.9"
	userAgent        = "Uptime-Kuma-Go/0"
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
}

// New creates a Checker.
func New() *Checker {
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
	return &Checker{verified: transport(false), insecure: transport(true)}
}

// Close releases idle connections.
func (c *Checker) Close() {
	c.verified.CloseIdleConnections()
	c.insecure.CloseIdleConnections()
}

// Run performs one check, bounded by the monitor's timeout.
func (c *Checker) Run(ctx context.Context, m monitor.Monitor) monitor.Result {
	timeout := m.EffectiveTimeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	switch m.Type {
	case monitor.TypeHTTP, monitor.TypeKeyword:
		return c.runHTTP(ctx, m, timeout)
	case monitor.TypePort:
		return c.runPort(ctx, m)
	default:
		return monitor.Result{Msg: "Unknown Monitor Type"}
	}
}

func (c *Checker) runPort(ctx context.Context, m monitor.Monitor) monitor.Result {
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
	var body io.Reader
	contentType := ""
	if strings.TrimSpace(m.Body) != "" {
		body = strings.NewReader(m.Body)
		switch m.BodyEncoding {
		case "form":
			contentType = "application/x-www-form-urlencoded"
		case "xml":
			contentType = "text/xml; charset=utf-8"
		default:
			contentType = "application/json"
		}
	}

	req, err := http.NewRequestWithContext(ctx, m.Method, m.URL, body)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	req.Header.Set("Accept", acceptHeader)
	req.Header.Set("User-Agent", userAgent)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range m.Headers {
		req.Header.Set(name, value)
	}

	transport := c.verified
	if m.IgnoreTLS {
		transport = c.insecure
	}
	client := http.Client{Transport: transport, CheckRedirect: redirectPolicy(m.MaxRedirects)}

	start := time.Now()
	res, err := client.Do(req)
	if err != nil {
		return monitor.Result{Msg: requestError(err, timeout)}
	}
	defer func() { _ = res.Body.Close() }()

	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return monitor.Result{Msg: requestError(err, timeout)}
	}
	ping := elapsedMs(start)

	if !m.Accepts(res.StatusCode) {
		return monitor.Result{Msg: fmt.Sprintf("Request failed with status code %d", res.StatusCode)}
	}

	reason := strings.TrimSpace(strings.TrimPrefix(res.Status, strconv.Itoa(res.StatusCode)))
	if reason == "" {
		reason = http.StatusText(res.StatusCode)
	}
	msg := fmt.Sprintf("%d - %s", res.StatusCode, reason)

	if m.Type == monitor.TypeHTTP {
		return monitor.Result{OK: true, Ping: &ping, Msg: msg}
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
