package ui

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// secretsInMonitor are values that a viewer must never receive.
var secretsInMonitor = []string{"pass-word-1", "hdr-token-2", "body-secret-3", "query-token-4", "user-info-5", "push-token-6"}

func TestViewerRole(t *testing.T) {
	s, fs, ts := newTestServer(t)
	ctx := context.Background()
	admin := newClient(t)
	postJSON(t, admin, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	hash, err := hashPassword("viewer password")
	if err != nil {
		t.Fatal(err)
	}
	fs.users = append(fs.users, store.User{ID: 2, Username: "viewer", PasswordHash: hash, Role: store.RoleViewer})
	if _, err := fs.CreateMonitor(ctx, monitor.Monitor{
		Name: "Secret site", Type: "http", Active: true, Interval: 60,
		URL:                      "https://user-info-5:x@example.com/health?token=query-token-4",
		DatabaseConnectionString: "postgres://u:pass-word-1@db/x",
		Headers:                  map[string]string{"Authorization": "hdr-token-2"},
		Body:                     "body-secret-3",
		PushToken:                "push-token-6",
	}); err != nil {
		t.Fatal(err)
	}

	viewer := newClient(t)
	res, body := postJSON(t, viewer, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "viewer", "password": "viewer password"})
	if res.StatusCode != 200 {
		t.Fatalf("viewer login: %d %v", res.StatusCode, body)
	}
	if user, _ := body["user"].(map[string]any); user["role"] != "viewer" {
		t.Fatalf("session role: %v", body)
	}

	id, stream, done := openStream(t, viewer, ts.URL)
	defer done()
	received := map[string]string{}
	for received["statusPageList"] == "" {
		_, parts := stream.message(t)
		var name string
		_ = json.Unmarshal(parts[0], &name)
		received[name] = string(parts[len(parts)-1])
	}
	for name, data := range received {
		for _, secret := range secretsInMonitor {
			if strings.Contains(data, secret) {
				t.Errorf("%s sent the viewer %s: %s", name, secret, data)
			}
		}
	}
	if received["session"] != `"viewer"` {
		t.Errorf("session: %s", received["session"])
	}
	if !strings.Contains(received["monitorList"], "Secret site") || !strings.Contains(received["monitorList"], "https://example.com/health") {
		t.Fatalf("the viewer's monitor list: %s", received["monitorList"])
	}
	for _, list := range []string{"notificationList", "proxyList", "dockerHostList", "remoteBrowserList"} {
		if received[list] != "[]" {
			t.Errorf("%s: %s", list, received[list])
		}
	}

	// Reading works, and changing doesn't.
	sendEvent(t, viewer, ts.URL, id, "getMonitorBeats", 1, 1, 24)
	if _, parts := stream.message(t); !strings.Contains(string(parts[1]), `"ok":true`) {
		t.Fatalf("read: %s", parts[1])
	}
	sendEvent(t, viewer, ts.URL, id, "getMonitor", 2, 1)
	if _, parts := stream.message(t); !strings.Contains(string(parts[1]), "Secret site") ||
		strings.Contains(string(parts[1]), "pass-word-1") || strings.Contains(string(parts[1]), "push-token-6") {
		t.Fatalf("getMonitor: %s", parts[1])
	}
	for i, event := range []string{"add", "deleteMonitor", "getSettings", "setSettings", "getAPIKeyList",
		"addNotification", "saveStatusPage", "clearStatistics", "getWebpushVapidPublicKey", "somethingNew"} {
		sendEvent(t, viewer, ts.URL, id, event, 10+i, map[string]any{})
		kind, parts := stream.message(t)
		for kind != "ack" {
			kind, parts = stream.message(t)
		}
		if !strings.Contains(string(parts[1]), "can only view") {
			t.Errorf("%s: %s", event, parts[1])
		}
	}
	if len(fs.monitors) != 1 {
		t.Fatal("a viewer changed the monitors")
	}

	// Broadcasts: monitors without credentials, and no notification settings.
	s.broadcastNotificationList(ctx)
	tree, err := s.monitorTree(ctx)
	if err != nil {
		t.Fatal(err)
	}
	full, _ := json.Marshal(tree.listJSON())
	for _, secret := range []string{"pass-word-1", "hdr-token-2", "body-secret-3", "query-token-4", "push-token-6"} {
		if !strings.Contains(string(full), secret) {
			t.Fatalf("an admin's monitor list has no %s, so this test checks nothing", secret)
		}
	}
	s.broadcastMonitorUpdate(tree.listJSON())
	_, parts := stream.message(t)
	var name string
	_ = json.Unmarshal(parts[0], &name)
	if name != "updateMonitorIntoList" {
		t.Fatalf("the viewer got %s first", name)
	}
	for _, secret := range secretsInMonitor {
		if strings.Contains(string(parts[1]), secret) {
			t.Errorf("the update sent the viewer %s", secret)
		}
	}

	// Saving a proxy, Docker host or remote browser sends the lists to admins only. The update
	// after them shows that the viewer got nothing before it.
	if _, _, err := fs.SaveProxy(ctx, check.Proxy{Protocol: "http", Host: "proxy", Port: 8080, Auth: true,
		Username: "proxy-user", Password: "proxy-pass-7"}, false); err != nil {
		t.Fatal(err)
	}
	s.sendProxyList(ctx, nil)
	s.sendDockerHostList(ctx, nil)
	s.sendRemoteBrowserList(ctx, nil)
	s.broadcastMonitorUpdate(tree.listJSON())
	_, parts = stream.message(t)
	_ = json.Unmarshal(parts[0], &name)
	if name != "updateMonitorIntoList" || strings.Contains(string(parts[1]), "proxy-pass-7") {
		t.Fatalf("the viewer got %s: %s", name, parts[1])
	}

	// The Security page reads the viewer's own account.
	sendEvent(t, viewer, ts.URL, id, "getOwnAccount", 30)
	kind, parts := stream.message(t)
	for kind != "ack" {
		kind, parts = stream.message(t)
	}
	if !strings.Contains(string(parts[1]), `"method":"password"`) {
		t.Fatalf("getOwnAccount for a viewer: %s", parts[1])
	}
}

func TestViewerMonitorJSON(t *testing.T) {
	full := map[string]any{
		"id": 1, "name": "A", "url": "https://u:p@example.com/a?k=v#f", "basic_auth_pass": "x",
		"databaseConnectionString": "postgres://u:p@db/x", "pushToken": "t", "headers": "{}", "someNewField": "y",
		"notificationIDList": map[string]bool{"3": true},
	}
	got := viewerMonitorJSON(full)
	if got["url"] != "https://example.com/a" || got["name"] != "A" {
		t.Fatalf("kept: %v", got)
	}
	for _, key := range []string{"basic_auth_pass", "databaseConnectionString", "pushToken", "headers", "someNewField"} {
		if _, found := got[key]; found {
			t.Errorf("%s was kept", key)
		}
	}
	if len(got["notificationIDList"].(map[string]bool)) != 0 {
		t.Error("the notifications were kept")
	}
	if safeURL("not a url") != "" || safeURL("mongodb://u:p@host/db?x=1") != "mongodb://host/db" {
		t.Errorf("safeURL: %q %q", safeURL("not a url"), safeURL("mongodb://u:p@host/db?x=1"))
	}
}
