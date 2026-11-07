package updates

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewest(t *testing.T) {
	tags := []string{"v1.2.3", "v1.10.0", "v1.11.0-beta.2", "v2", "v1.9", "release-5", "1.20.0", "v1.10.0+build", "vX"}
	if got := Newest(tags, false); got != "1.10.0" {
		t.Errorf("stable: %s", got)
	}
	if got := Newest(tags, true); got != "1.11.0-beta.2" {
		t.Errorf("beta: %s", got)
	}
	// A beta older than the newest stable release isn't offered.
	if got := Newest([]string{"v2.0.0", "v2.0.0-beta.1"}, true); got != "2.0.0" {
		t.Errorf("old beta: %s", got)
	}
	if got := Newest(nil, true); got != "" {
		t.Errorf("none: %s", got)
	}
}

func TestChecker(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("User-Agent") == "" {
			t.Error("no user agent")
		}
		_, _ = w.Write([]byte(`[{"name":"v3.1.0-beta.1"},{"name":"v3.0.0"}]`))
	}))
	defer server.Close()
	var check, beta atomic.Bool
	check.Store(true)
	c := New(server.URL, nil, func(context.Context) (bool, bool, error) { return check.Load(), beta.Load(), nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Run(ctx)

	wait := func(want string) {
		t.Helper()
		for i := 0; i < 100 && c.Latest() != want; i++ {
			time.Sleep(20 * time.Millisecond)
		}
		if got := c.Latest(); got != want {
			t.Fatalf("latest %q, want %q", got, want)
		}
	}
	wait("3.0.0")
	beta.Store(true)
	c.Recheck()
	wait("3.1.0-beta.1")
	check.Store(false)
	c.Recheck()
	wait("")
	asked := requests.Load()
	c.Recheck()
	time.Sleep(100 * time.Millisecond)
	if requests.Load() != asked {
		t.Error("asked GitHub with the check off")
	}
	var nilChecker *Checker
	if nilChecker.Latest() != "" {
		t.Error("a nil checker knows a version")
	}
}
