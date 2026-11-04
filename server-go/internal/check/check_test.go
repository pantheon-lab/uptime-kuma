package check

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

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
	c := New()
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
	c := New()
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
	c := New()
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
	c := New()
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
	c := New()
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
	c := New()
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
	c := New()
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
	c := New()
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
	c := New()
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
