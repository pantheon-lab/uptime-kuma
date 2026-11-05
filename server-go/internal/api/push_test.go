package api

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// pushScheduler answers pushes the way the scheduler does, and keeps what it was asked.
type pushScheduler struct {
	err      error
	tokens   []string
	received []monitor.Push
}

func (f *pushScheduler) Upsert(monitor.Monitor)              {}
func (f *pushScheduler) Remove(context.Context, int32) error { return nil }

func (f *pushScheduler) Push(_ context.Context, token string, push monitor.Push) error {
	f.tokens = append(f.tokens, token)
	if f.err != nil {
		return f.err
	}
	f.received = append(f.received, push)
	return nil
}

// newPushServer serves the push route with a scheduler the test controls.
func newPushServer(t *testing.T, sched *pushScheduler) *httptest.Server {
	t.Helper()
	hub := NewHub()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ts := httptest.NewServer(New(&fakeStore{}, sched, hub, testToken, log).Handler())
	t.Cleanup(func() { hub.Close(); ts.Close() })
	return ts
}

func pushBody(t *testing.T, res *http.Response) map[string]any {
	t.Helper()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("body %q: %v", body, err)
	}
	return decoded
}

func TestPushReportsABeat(t *testing.T) {
	sched := &pushScheduler{}
	ts := newPushServer(t, sched)

	// The token in the path is the only login, and any method reaches it, as in the
	// Node.js backend that scripts already call.
	res := do(t, "POST", ts.URL+"/api/push/the-token?status=down&msg=stopped&ping=12.5", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", res.StatusCode)
	}
	if body := pushBody(t, res); len(body) != 1 || body["ok"] != true {
		t.Errorf("body: %v", body)
	}
	if len(sched.tokens) != 1 || sched.tokens[0] != "the-token" {
		t.Fatalf("tokens: %v", sched.tokens)
	}
	got := sched.received[0]
	if got.Up || !got.ExplicitDown || got.Msg != "stopped" || got.Ping == nil || *got.Ping != 12.5 {
		t.Errorf("push: %+v", got)
	}

	// Without parameters, the beat is up and says OK, like the UI's example URL.
	res = do(t, "GET", ts.URL+"/api/push/the-token?ping=", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", res.StatusCode)
	}
	got = sched.received[1]
	if !got.Up || got.ExplicitDown || got.Msg != "OK" || got.Ping != nil {
		t.Errorf("push: %+v", got)
	}

	// A ping that isn't a number means no ping, rather than a failed push.
	res = do(t, "GET", ts.URL+"/api/push/the-token?ping=soon", "", "")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status: %d", res.StatusCode)
	}
	if sched.received[2].Ping != nil {
		t.Errorf("ping: %+v", sched.received[2].Ping)
	}
}

func TestPushRejectsABadPing(t *testing.T) {
	sched := &pushScheduler{}
	ts := newPushServer(t, sched)

	for _, value := range []string{"-1", "100000000001"} {
		res := do(t, "GET", ts.URL+"/api/push/the-token?ping="+value, "", "")
		if res.StatusCode != http.StatusNotFound {
			t.Errorf("ping %s: status %d", value, res.StatusCode)
		}
		body := pushBody(t, res)
		if body["ok"] != false {
			t.Errorf("ping %s: body %v", value, body)
		}
		if msg, _ := body["msg"].(string); msg != "Invalid ping value. Must be between 0 and 100000000000 ms." {
			t.Errorf("ping %s: %q", value, msg)
		}
	}
	if len(sched.tokens) != 0 {
		t.Error("an invalid push still reached the scheduler")
	}
}

func TestPushAnswersForTheScheduler(t *testing.T) {
	cases := map[string]struct {
		err    error
		status int
		msg    string
	}{
		"unknown or paused": {err: monitor.ErrPushNotFound, status: http.StatusNotFound, msg: "Monitor not found or not active."},
		"too fast":          {err: monitor.ErrPushTooFast, status: http.StatusTooManyRequests, msg: "Too many pushes"},
		"stopping":          {err: context.Canceled, status: http.StatusServiceUnavailable, msg: "Push not received"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sched := &pushScheduler{err: tc.err}
			ts := newPushServer(t, sched)
			res := do(t, "GET", ts.URL+"/api/push/the-token", "", "")
			if res.StatusCode != tc.status {
				t.Errorf("status: %d, want %d", res.StatusCode, tc.status)
			}
			body := pushBody(t, res)
			if body["ok"] != false || body["msg"] != tc.msg {
				t.Errorf("body: %v", body)
			}
		})
	}
}
