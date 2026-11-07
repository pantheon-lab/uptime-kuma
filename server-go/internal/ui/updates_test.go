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

func TestWebpushKeyEvents(t *testing.T) {
	s, fs, _ := newTestServer(t)
	s.notifyEnv.WebpushKeys = func(context.Context) (string, string, error) { return "the-public", "the-private", nil }
	fs.settings["webpushPublicVapidKey"] = json.RawMessage(`"the-public"`)
	fs.settings["webpushPrivateVapidKey"] = json.RawMessage(`"the-private"`)
	c := &conn{userID: 1, username: "admin", out: make(chan []byte, 256), done: make(chan struct{})}
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, _ := s.handle(context.Background(), c, event, raws)
		return r.(reply)
	}
	if r := call("getWebpushVapidPublicKey"); r["ok"] != true || r["msg"] != "the-public" {
		t.Fatalf("public key: %v", r)
	}
	data := call("getSettings")["data"].(map[string]any)
	if _, found := data["webpushPrivateVapidKey"]; found {
		t.Fatal("the private key is sent to the browser")
	}
	call("setSettings", map[string]any{"webpushPublicVapidKey": "replaced", "webpushPrivateVapidKey": "replaced"})
	if string(fs.settings["webpushPublicVapidKey"]) != `"the-public"` || string(fs.settings["webpushPrivateVapidKey"]) != `"the-private"` {
		t.Fatal("a page replaced the key pair")
	}
}
