package ui

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeUpdates struct {
	latest   string
	rechecks int
}

func (f *fakeUpdates) Latest() string { return f.latest }
func (f *fakeUpdates) Recheck()       { f.rechecks++ }

func TestUpdateNotice(t *testing.T) {
	u := &fakeUpdates{latest: "3.1.0"}
	s, _, _ := newTestServerWith(t, func(o *Options) { o.Updates = u })
	if got := s.info(context.Background(), true)["latestVersion"]; got != "3.1.0" {
		t.Fatalf("info: %v", got)
	}
	c := &conn{userID: 1, username: "admin", out: make(chan []byte, 256), done: make(chan struct{})}
	save := func(settings map[string]any) {
		t.Helper()
		raw, _ := json.Marshal(settings)
		if r, _ := s.handle(context.Background(), c, "setSettings", []json.RawMessage{raw}); r.(reply)["ok"] != true {
			t.Fatalf("save: %v", r)
		}
	}
	save(map[string]any{"checkUpdate": true, "entryPage": "dashboard"})
	save(map[string]any{"checkUpdate": true, "entryPage": "dashboard"})
	if u.rechecks != 1 {
		t.Fatalf("%d rechecks after saving the same setting twice", u.rechecks)
	}
	save(map[string]any{"checkUpdate": true, "checkBeta": true})
	if u.rechecks != 2 {
		t.Fatalf("%d rechecks after turning betas on", u.rechecks)
	}
	u.latest = ""
	if _, found := s.info(context.Background(), true)["latestVersion"]; found {
		t.Fatal("an unknown version is sent")
	}
}
