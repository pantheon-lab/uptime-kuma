package check

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func httpMonitor(url string) monitor.Monitor {
	m := monitor.New()
	m.Name, m.Type, m.URL, m.Timeout = "t", monitor.TypeHTTP, url, 2
	return m
}

func serve(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	ts := httptest.NewServer(h)
	t.Cleanup(ts.Close)
	return ts
}

func TestHTTPStatusHandling(t *testing.T) {
	ts := serve(t, func(w http.ResponseWriter, r *http.Request) {
		code, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		w.WriteHeader(code)
	})
	c := New(nil)
	defer c.Close()

	r := c.Run(context.Background(), httpMonitor(ts.URL+"/204"))
	if !r.OK || r.Msg != "204 - No Content" || r.Ping == nil {
		t.Errorf("204: %+v", r)
	}
	r = c.Run(context.Background(), httpMonitor(ts.URL+"/500"))
	if r.OK || r.Msg != "Request failed with status code 500" || r.Ping != nil {
		t.Errorf("500: %+v", r)
	}
	m := httpMonitor(ts.URL + "/500")
	m.AcceptedStatusCodes = []string{"500"}
	if r := c.Run(context.Background(), m); !r.OK {
		t.Errorf("accepted 500: %+v", r)
	}
}

func TestHTTPSendsConfiguredRequest(t *testing.T) {
	var method, contentType, custom, body, agent string
	ts := serve(t, func(_ http.ResponseWriter, r *http.Request) {
		method, contentType, custom, agent = r.Method, r.Header.Get("Content-Type"), r.Header.Get("X-Custom"), r.Header.Get("User-Agent")
		data, _ := io.ReadAll(r.Body)
		body = string(data)
	})
	c := New(nil)
	defer c.Close()
	m := httpMonitor(ts.URL)
	m.Method, m.Body, m.Headers = "POST", `{"a":1}`, map[string]string{"X-Custom": "yes", "User-Agent": "mine"}
	if r := c.Run(context.Background(), m); !r.OK {
		t.Fatalf("%+v", r)
	}
	if method != "POST" || contentType != "application/json" || custom != "yes" || body != `{"a":1}` || agent != "mine" {
		t.Errorf("request: %s %s %s %s %s", method, contentType, custom, body, agent)
	}
	m.Body, m.BodyEncoding = "a=1", "form"
	c.Run(context.Background(), m)
	if contentType != "application/x-www-form-urlencoded" {
		t.Errorf("form content type: %s", contentType)
	}
}

func TestHTTPRedirectLimit(t *testing.T) {
	ts := serve(t, func(w http.ResponseWriter, r *http.Request) {
		n, _ := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/"))
		if n > 0 {
			http.Redirect(w, r, "/"+strconv.Itoa(n-1), http.StatusFound)
		}
	})
	c := New(nil)
	defer c.Close()
	m := httpMonitor(ts.URL + "/2")

	m.MaxRedirects = 2
	if r := c.Run(context.Background(), m); !r.OK {
		t.Errorf("2 redirects with limit 2: %+v", r)
	}
	m.MaxRedirects = 1
	if r := c.Run(context.Background(), m); r.OK || r.Msg != errTooManyRedirects.Error() {
		t.Errorf("2 redirects with limit 1: %+v", r)
	}
	m.MaxRedirects = 0
	if r := c.Run(context.Background(), m); r.OK || r.Msg != "Request failed with status code 302" {
		t.Errorf("limit 0: %+v", r)
	}
	m.AcceptedStatusCodes = []string{"302"}
	if r := c.Run(context.Background(), m); !r.OK {
		t.Errorf("limit 0 accepting 302: %+v", r)
	}
}

func TestHTTPTimeoutAndCredentialsInErrors(t *testing.T) {
	release := make(chan struct{})
	ts := serve(t, func(http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	c := New(nil)
	defer c.Close()

	m := httpMonitor(ts.URL)
	m.Timeout = 0.1
	start := time.Now()
	r := c.Run(context.Background(), m)
	if r.OK || r.Msg != "timeout of 100ms exceeded" {
		t.Errorf("timeout: %+v", r)
	}
	if time.Since(start) > time.Second {
		t.Errorf("timeout took %v", time.Since(start))
	}

	// Port 1 refuses. The message must not repeat the URL and its password.
	m = httpMonitor("http://user:secret@127.0.0.1:1/")
	if r := c.Run(context.Background(), m); r.OK || strings.Contains(r.Msg, "secret") {
		t.Errorf("refused: %+v", r)
	}
}

func TestHTTPTLSVerification(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	ts.Config.ErrorLog = nil
	defer ts.Close()
	c := New(nil)
	defer c.Close()
	m := httpMonitor(ts.URL)
	if r := c.Run(context.Background(), m); r.OK {
		t.Errorf("self-signed certificate accepted: %+v", r)
	}
	m.IgnoreTLS = true
	if r := c.Run(context.Background(), m); !r.OK {
		t.Errorf("ignoreTls: %+v", r)
	}
}

func TestKeyword(t *testing.T) {
	page := "<html>\n<b>hello</b> " + strings.Repeat("x", 100) + "</html>"
	ts := serve(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, page) })
	c := New(nil)
	defer c.Close()
	m := httpMonitor(ts.URL)
	m.Type, m.Keyword = monitor.TypeKeyword, "hello"
	if r := c.Run(context.Background(), m); !r.OK || r.Msg != "200 - OK, keyword is found" {
		t.Errorf("found: %+v", r)
	}
	m.InvertKeyword = true
	if r := c.Run(context.Background(), m); r.OK || !strings.Contains(r.Msg, "but keyword is present in [") {
		t.Errorf("inverted found: %+v", r)
	}
	m.Keyword = "absent"
	if r := c.Run(context.Background(), m); !r.OK || r.Msg != "200 - OK, keyword not found" {
		t.Errorf("inverted missing: %+v", r)
	}
	m.InvertKeyword = false
	r := c.Run(context.Background(), m)
	if r.OK || !strings.HasPrefix(r.Msg, "200 - OK, but keyword is not in [") || !strings.Contains(r.Msg, "...]") {
		t.Errorf("missing: %+v", r)
	}
	if strings.Contains(r.Msg, "<b>") {
		t.Errorf("markup was not removed: %s", r.Msg)
	}
}

func TestResponseSizeIsBounded(t *testing.T) {
	ts := serve(t, func(w http.ResponseWriter, _ *http.Request) {
		chunk := make([]byte, 1<<20)
		for i := 0; i < 50; i++ {
			if _, err := w.Write(chunk); err != nil {
				return
			}
		}
	})
	c := New(nil)
	defer c.Close()
	if r := c.Run(context.Background(), httpMonitor(ts.URL)); !r.OK {
		t.Errorf("%+v", r)
	}
}

func TestPort(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	host, portText, _ := net.SplitHostPort(l.Addr().String())
	port, _ := strconv.Atoi(portText)
	c := New(nil)
	defer c.Close()

	m := monitor.New()
	m.Type, m.Hostname, m.Port, m.Timeout = monitor.TypePort, host, int32(port), 2
	r := c.Run(context.Background(), m)
	if !r.OK || r.Ping == nil || !strings.HasSuffix(r.Msg, " ms") {
		t.Errorf("open: %+v", r)
	}
	_ = l.Close()
	if r := c.Run(context.Background(), m); r.OK || r.Msg != "Connection failed" {
		t.Errorf("closed: %+v", r)
	}
}

func TestRunStopsWhenCancelled(t *testing.T) {
	release := make(chan struct{})
	ts := serve(t, func(http.ResponseWriter, *http.Request) { <-release })
	defer close(release)
	c := New(nil)
	defer c.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan monitor.Result, 1)
	m := httpMonitor(ts.URL)
	m.Timeout = 30
	go func() { done <- c.Run(ctx, m) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("check kept running after cancellation")
	}
}

type fakeGroups struct {
	children []Child
	err      error
}

func (f fakeGroups) GroupChildren(context.Context, int32) ([]Child, error) { return f.children, f.err }

func TestGroup(t *testing.T) {
	status := func(s int16) *int16 { return &s }
	up, down, pending := status(monitor.StatusUp), status(monitor.StatusDown), status(monitor.StatusPending)
	cases := []struct {
		name     string
		children []Child
		ok       bool
		pending  bool
		msg      string
	}{
		{"empty", nil, false, true, "Group empty"},
		{"all up", []Child{{"a", true, up}, {"b", true, up}}, true, false, "All children up and running"},
		{"paused down child ignored", []Child{{"a", true, up}, {"b", false, down}}, true, false, "All children up and running"},
		{"no beats yet", []Child{{"a", true, nil}}, false, true, "Pending child monitors: a"},
		{"pending", []Child{{"a", true, up}, {"b", true, pending}}, false, true, "Pending child monitors: b"},
		{"down wins", []Child{{"a", true, pending}, {"b", true, down}}, false, false, "Child monitors down: b; pending: a"},
		{"all paused", []Child{{"a", false, up}}, true, false, "All children up and running"},
	}
	for _, tc := range cases {
		c := New(fakeGroups{children: tc.children})
		r := c.Run(context.Background(), monitor.Monitor{Type: monitor.TypeGroup, Interval: 60})
		if r.OK != tc.ok || r.Pending != tc.pending || r.Msg != tc.msg {
			t.Errorf("%s: %+v", tc.name, r)
		}
	}
	c := New(fakeGroups{err: context.DeadlineExceeded})
	if r := c.Run(context.Background(), monitor.Monitor{Type: monitor.TypeGroup, Interval: 60}); r.OK || r.Pending {
		t.Errorf("read failure: %+v", r)
	}
}

func TestPropagateTraceIsOptIn(t *testing.T) {
	var header string
	ts := serve(t, func(_ http.ResponseWriter, r *http.Request) { header = r.Header.Get("traceparent") })
	tp := sdktrace.NewTracerProvider()
	defer func() { _ = tp.Shutdown(context.Background()) }()
	ctx, span := tp.Tracer("test").Start(context.Background(), "check")
	defer span.End()

	c := New(nil)
	defer c.Close()
	m := httpMonitor(ts.URL)
	c.Run(ctx, m)
	if header != "" {
		t.Errorf("traceparent sent by default: %q", header)
	}
	m.PropagateTrace = true
	c.Run(ctx, m)
	if !strings.Contains(header, span.SpanContext().TraceID().String()) {
		t.Errorf("traceparent %q doesn't carry the trace id", header)
	}
}

func TestSpansDontCarrySecrets(t *testing.T) {
	ts := serve(t, func(http.ResponseWriter, *http.Request) {})
	exporter := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	defer func() { _ = tp.Shutdown(context.Background()) }()
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	defer otel.SetTracerProvider(previous)

	ctx, span := tp.Tracer("test").Start(context.Background(), "check")
	c := New(nil)
	defer c.Close()
	m := httpMonitor(strings.Replace(ts.URL, "http://", "http://user:urlsecret@", 1) + "/path?token=querysecret")
	m.Headers = map[string]string{"X-Api-Key": "headersecret", "Authorization": "Bearer authsecret"}
	if r := c.Run(ctx, m); !r.OK {
		t.Fatalf("%+v", r)
	}
	span.End()

	spans := exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("no spans recorded")
	}
	dump := fmt.Sprintf("%+v", spans)
	for _, secret := range []string{"urlsecret", "querysecret", "headersecret", "authsecret"} {
		if strings.Contains(dump, secret) {
			t.Errorf("%s leaked into a span", secret)
		}
	}
}

func TestHTTPSReportsTLS(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()
	c := New(nil)
	defer c.Close()
	m := monitor.New()
	m.Type, m.URL, m.IgnoreTLS, m.Interval = monitor.TypeHTTP, server.URL, true, 10
	// A failed status still has a certificate to report.
	result := c.Run(context.Background(), m)
	if result.OK || result.TLS == nil || len(result.TLS.PeerCertificates) == 0 {
		t.Fatalf("result: %+v", result)
	}
	plain := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer plain.Close()
	m.URL = plain.URL
	if result := c.Run(context.Background(), m); !result.OK || result.TLS != nil {
		t.Fatalf("plain HTTP: %+v", result)
	}
}
