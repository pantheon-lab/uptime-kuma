package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestDockerHostsAndRemoteBrowsers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()

	host, err := s.SaveDockerHost(ctx, check.DockerHost{Name: "local", Type: "socket", Daemon: "/var/run/docker.sock"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.SaveDockerHost(ctx, check.DockerHost{Name: "remote", Type: "tcp", Daemon: "tcp://docker:2375"})
	if err != nil {
		t.Fatal(err)
	}
	browser, err := s.SaveRemoteBrowser(ctx, check.RemoteBrowser{Name: "browserless", URL: "ws://browserless:3000"})
	if err != nil {
		t.Fatal(err)
	}
	newMonitor := func(name string, settings monitor.Settings, kind string) monitor.Monitor {
		m := monitor.New()
		m.Name, m.Type, m.Active, m.URL, m.Settings = name, kind, false, "https://example.com", settings
		created, err := s.CreateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	web := newMonitor("web", monitor.Settings{DockerHostID: host.ID, DockerContainer: "web"}, monitor.TypeDocker)
	db := newMonitor("db", monitor.Settings{DockerHostID: other.ID, DockerContainer: "db"}, monitor.TypeDocker)
	page := newMonitor("page", monitor.Settings{RemoteBrowserID: browser.ID, ScreenshotDelay: 100}, monitor.TypeRealBrowser)

	changed, err := s.DeleteDockerHost(ctx, host.ID)
	if err != nil || len(changed) != 1 || changed[0] != web.ID {
		t.Fatalf("delete host: %v %v", changed, err)
	}
	if got, _ := s.GetMonitor(ctx, web.ID); got.DockerHostID != 0 || got.DockerContainer != "web" {
		t.Fatalf("web after delete: %+v", got.Settings)
	}
	if got, _ := s.GetMonitor(ctx, db.ID); got.DockerHostID != other.ID {
		t.Fatalf("the other host's monitor changed: %+v", got.Settings)
	}
	if _, err := s.DeleteDockerHost(ctx, host.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := s.SaveDockerHost(ctx, check.DockerHost{ID: host.ID, Name: "x", Type: "socket", Daemon: "/x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("edit a deleted host: %v", err)
	}
	if changed, err := s.DeleteRemoteBrowser(ctx, browser.ID); err != nil || len(changed) != 1 || changed[0] != page.ID {
		t.Fatalf("delete browser: %v %v", changed, err)
	}
	if got, _ := s.GetMonitor(ctx, page.ID); got.RemoteBrowserID != 0 || got.ScreenshotDelay != 100 {
		t.Fatalf("page after delete: %+v", got.Settings)
	}

	// Importing the same host twice gives one host.
	first, err := s.ImportDockerHost(ctx, check.DockerHost{Name: "imported", Type: "socket", Daemon: "/a.sock"})
	if err != nil {
		t.Fatal(err)
	}
	if again, err := s.ImportDockerHost(ctx, check.DockerHost{Name: "imported", Type: "socket", Daemon: "/a.sock"}); err != nil || again != first {
		t.Fatalf("import again: %d %v, want %d", again, err, first)
	}
	if hosts, _ := s.ListDockerHosts(ctx); len(hosts) != 2 {
		t.Fatalf("hosts: %+v", hosts)
	}

	if err := s.SaveScreenshot(ctx, page.ID, []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := s.SaveScreenshot(ctx, page.ID, []byte("two")); err != nil {
		t.Fatal(err)
	}
	if png, _, err := s.Screenshot(ctx, page.ID); err != nil || !bytes.Equal(png, []byte("two")) {
		t.Fatalf("screenshot: %q %v", png, err)
	}
	if err := s.DeleteMonitor(ctx, page.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.Screenshot(ctx, page.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("screenshot of a deleted monitor: %v", err)
	}
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{"chromeExecutable": json.RawMessage(`"/usr/bin/chromium"`)}); err != nil {
		t.Fatal(err)
	}
	if path, err := s.ChromeExecutable(ctx); err != nil || path != "/usr/bin/chromium" {
		t.Fatalf("chrome: %q %v", path, err)
	}
}

func TestProxiesAndDomainExpiry(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, _, err := s.SaveProxy(ctx, check.Proxy{Protocol: "http", Host: "a", Port: 3128, Active: true, Default: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	newMonitor := func(name, kind string) monitor.Monitor {
		m := monitor.New()
		m.Name, m.Type, m.Active, m.URL, m.Hostname = name, kind, false, "https://example.com", "example.com"
		created, err := s.CreateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	web := newMonitor("web", monitor.TypeHTTP)
	ping := newMonitor("ping", monitor.TypePing)
	second, changed, err := s.SaveProxy(ctx, check.Proxy{Protocol: "socks5", Host: "b", Port: 1080, Auth: true, Username: "u",
		Password: "p", Active: true, Default: true}, true)
	if err != nil || len(changed) != 1 || changed[0] != web.ID {
		t.Fatalf("apply to existing: %v %v", changed, err)
	}
	proxies, _ := s.ListProxies(ctx)
	if len(proxies) != 2 || proxies[0].Default || !proxies[1].Default || proxies[1].Password != "p" {
		t.Fatalf("one default: %+v", proxies)
	}
	if got, _ := s.GetMonitor(ctx, web.ID); got.ProxyID != second.ID {
		t.Fatalf("web: %+v", got.Settings)
	}
	if got, _ := s.GetMonitor(ctx, ping.ID); got.ProxyID != 0 {
		t.Fatalf("a ping monitor got a proxy: %+v", got.Settings)
	}
	if changed, err := s.DeleteProxy(ctx, second.ID); err != nil || len(changed) != 1 {
		t.Fatalf("delete: %v %v", changed, err)
	}
	if id, err := s.ImportProxy(ctx, check.Proxy{Protocol: "http", Host: "a", Port: 3128, Active: true, Default: true}); err != nil || id != first.ID {
		t.Fatalf("import an existing proxy: %d %v", id, err)
	}

	empty, err := s.DomainExpiry(ctx, "example.com")
	if err != nil || empty.Expiry != nil || empty.Domain != "example.com" {
		t.Fatalf("unknown domain: %+v %v", empty, err)
	}
	expiry, checked, days := time.Now().UTC().Add(30*24*time.Hour).Truncate(time.Second), time.Now().UTC().Truncate(time.Second), 21
	if err := s.SaveDomainExpiry(ctx, domains.Record{Domain: "example.com", Expiry: &expiry, LastCheck: &checked, LastNotifiedDays: &days}); err != nil {
		t.Fatal(err)
	}
	got, err := s.DomainExpiry(ctx, "example.com")
	if err != nil || !got.Expiry.Equal(expiry) || !got.LastCheck.Equal(checked) || *got.LastNotifiedDays != 21 {
		t.Fatalf("saved domain: %+v %v", got, err)
	}
	if err := s.SaveDomainExpiry(ctx, domains.Record{Domain: "example.com", Expiry: &expiry, LastCheck: &checked}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.DomainExpiry(ctx, "example.com"); got.LastNotifiedDays != nil {
		t.Fatalf("cleared alert: %+v", got)
	}
}
