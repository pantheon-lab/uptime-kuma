package store

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
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
