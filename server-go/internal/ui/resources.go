package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// ResourceStore keeps Docker hosts, remote browsers and screenshots.
type ResourceStore interface {
	ListDockerHosts(ctx context.Context) ([]check.DockerHost, error)
	SaveDockerHost(ctx context.Context, h check.DockerHost) (check.DockerHost, error)
	DeleteDockerHost(ctx context.Context, id int32) ([]int32, error)
	ListRemoteBrowsers(ctx context.Context) ([]check.RemoteBrowser, error)
	SaveRemoteBrowser(ctx context.Context, b check.RemoteBrowser) (check.RemoteBrowser, error)
	DeleteRemoteBrowser(ctx context.Context, id int32) ([]int32, error)
	Screenshot(ctx context.Context, monitorID int32) ([]byte, time.Time, error)
	ListProxies(ctx context.Context) ([]check.Proxy, error)
	SaveProxy(ctx context.Context, p check.Proxy, applyExisting bool) (check.Proxy, []int32, error)
	DeleteProxy(ctx context.Context, id int32) ([]int32, error)
}

// The tests replace these, so that they don't need Docker, Chrome or PM2.
var (
	testDockerHost    = check.TestDockerHost
	testRemoteBrowser = check.TestRemoteBrowser
	testChrome        = check.TestChrome
	pm2ProcessList    = check.PM2ProcessList
)

// optionalID reads an id that may be null, for a new item.
func optionalID(args []json.RawMessage, i int) (int32, error) {
	var id flexInt
	if err := arg(args, i, &id); err != nil {
		return 0, err
	}
	if !id.set {
		return 0, nil
	}
	if id.value <= 0 || id.value > 1<<31-1 {
		return 0, errors.New("invalid id")
	}
	return int32(id.value), nil
}

func (s *Server) sendDockerHostList(ctx context.Context, c *conn) {
	hosts, err := s.store.ListDockerHosts(ctx)
	if err != nil {
		s.log.Error("send docker host list", "error", err)
		return
	}
	frame := encodeMessage("dockerHostList", orEmpty(hosts))
	if c != nil {
		c.push(frame)
		return
	}
	s.broadcast(frame)
}

func (s *Server) sendRemoteBrowserList(ctx context.Context, c *conn) {
	browsers, err := s.store.ListRemoteBrowsers(ctx)
	if err != nil {
		s.log.Error("send remote browser list", "error", err)
		return
	}
	frame := encodeMessage("remoteBrowserList", orEmpty(browsers))
	if c != nil {
		c.push(frame)
		return
	}
	s.broadcast(frame)
}

func dockerHostArg(args []json.RawMessage) (check.DockerHost, error) {
	var h check.DockerHost
	if err := arg(args, 0, &h); err != nil {
		return h, err
	}
	h.Name, h.Daemon = strings.TrimSpace(h.Name), strings.TrimSpace(h.Daemon)
	return h, nil
}

func (s *Server) addDockerHost(ctx context.Context, args []json.RawMessage) (reply, error) {
	h, err := dockerHostArg(args)
	if err != nil {
		return nil, userErr(err)
	}
	if h.ID, err = optionalID(args, 1); err != nil {
		return nil, userErr(err)
	}
	if err := check.ValidateDockerHost(h); err != nil {
		return nil, userErr(err)
	}
	saved, err := s.store.SaveDockerHost(ctx, h)
	if err != nil {
		return nil, err
	}
	s.sendDockerHostList(ctx, nil)
	return okI18n("Saved.", "id", saved.ID), nil
}

func (s *Server) deleteDockerHost(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	changed, err := s.store.DeleteDockerHost(ctx, id)
	if err != nil {
		return nil, err
	}
	s.refreshMonitors(ctx, changed)
	s.sendDockerHostList(ctx, nil)
	return okI18n("successDeleted"), nil
}

func (s *Server) testDockerHostEvent(ctx context.Context, args []json.RawMessage) (reply, error) {
	h, err := dockerHostArg(args)
	if err != nil {
		return nil, userErr(err)
	}
	if err := check.ValidateDockerHost(h); err != nil {
		return nil, userErr(err)
	}
	count, err := testDockerHost(ctx, h)
	if err != nil {
		return nil, userErr(err)
	}
	if count == 0 {
		return ok("msg", "Connected Successfully, but there are no containers?"), nil
	}
	return ok("msg", "Connected Successfully. Amount of containers: "+strconv.Itoa(count)), nil
}

func remoteBrowserArg(args []json.RawMessage) (check.RemoteBrowser, error) {
	var b check.RemoteBrowser
	if err := arg(args, 0, &b); err != nil {
		return b, err
	}
	b.Name, b.URL = strings.TrimSpace(b.Name), strings.TrimSpace(b.URL)
	return b, nil
}

func (s *Server) addRemoteBrowser(ctx context.Context, args []json.RawMessage) (reply, error) {
	b, err := remoteBrowserArg(args)
	if err != nil {
		return nil, userErr(err)
	}
	if b.ID, err = optionalID(args, 1); err != nil {
		return nil, userErr(err)
	}
	if err := check.ValidateRemoteBrowser(b); err != nil {
		return nil, userErr(err)
	}
	saved, err := s.store.SaveRemoteBrowser(ctx, b)
	if err != nil {
		return nil, err
	}
	s.sendRemoteBrowserList(ctx, nil)
	return okI18n("Saved.", "id", saved.ID), nil
}

func (s *Server) deleteRemoteBrowser(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	changed, err := s.store.DeleteRemoteBrowser(ctx, id)
	if err != nil {
		return nil, err
	}
	s.refreshMonitors(ctx, changed)
	s.sendRemoteBrowserList(ctx, nil)
	return okI18n("successDeleted"), nil
}

func (s *Server) testRemoteBrowserEvent(ctx context.Context, args []json.RawMessage) (reply, error) {
	b, err := remoteBrowserArg(args)
	if err != nil {
		return nil, userErr(err)
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	if _, err := testRemoteBrowser(ctx, b.URL); err != nil {
		return nil, userErr(err)
	}
	return ok("msg", "Connected Successfully."), nil
}

func (s *Server) testChromeEvent(ctx context.Context, args []json.RawMessage) (reply, error) {
	var executable string
	if err := arg(args, 0, &executable); err != nil {
		return nil, userErr(err)
	}
	ctx, cancel := context.WithTimeout(ctx, testTimeout)
	defer cancel()
	version, err := testChrome(ctx, executable)
	if err != nil {
		return nil, userErr(err)
	}
	return reply{"ok": true, "msgi18n": true, "msg": map[string]any{"key": "foundChromiumVersion", "values": []string{version}}}, nil
}

func (s *Server) getPM2ProcessList(ctx context.Context) (reply, error) {
	list, err := pm2ProcessList(ctx)
	if err != nil {
		return nil, userErr(err)
	}
	return ok("processList", list), nil
}

// refreshMonitors gives the scheduler and open pages monitors that changed in the database.
func (s *Server) refreshMonitors(ctx context.Context, ids []int32) {
	for _, id := range ids {
		m, err := s.store.GetMonitor(ctx, id)
		if err != nil {
			s.log.Error("reload monitor", "monitor", id, "error", err)
			continue
		}
		s.scheduler.Upsert(m)
	}
	if len(ids) > 0 {
		s.broadcastMonitors(ctx, ids...)
	}
}

// screenshot serves a real-browser monitor's last screenshot to logged-in users.
func (s *Server) screenshot(w http.ResponseWriter, r *http.Request) {
	_, _, loggedIn, err := s.currentSession(r.Context(), w, r)
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if !loggedIn {
		http.Error(w, "not logged in", http.StatusUnauthorized)
		return
	}
	name, found := strings.CutSuffix(r.PathValue("file"), ".png")
	id, err := strconv.ParseInt(name, 10, 32)
	if !found || err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	png, taken, err := s.store.Screenshot(r.Context(), int32(id))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("read screenshot", "monitor", id, "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "private, no-cache")
	w.Header().Set("Last-Modified", taken.UTC().Format(http.TimeFormat))
	_, _ = w.Write(png)
}

func (s *Server) sendProxyList(ctx context.Context, c *conn) {
	proxies, err := s.store.ListProxies(ctx)
	if err != nil {
		s.log.Error("send proxy list", "error", err)
		return
	}
	frame := encodeMessage("proxyList", orEmpty(proxies))
	if c != nil {
		c.push(frame)
		return
	}
	s.broadcast(frame)
}

func (s *Server) addProxy(ctx context.Context, args []json.RawMessage) (reply, error) {
	var in struct {
		check.Proxy
		Port          flexInt `json:"port"`
		ApplyExisting bool    `json:"applyExisting"`
	}
	if err := arg(args, 0, &in); err != nil {
		return nil, userErr(err)
	}
	p := in.Proxy
	p.Port = int32(in.Port.value)
	p.Host, p.Protocol = strings.TrimSpace(p.Host), strings.TrimSpace(p.Protocol)
	if !p.Auth {
		p.Username, p.Password = "", ""
	}
	var err error
	if p.ID, err = optionalID(args, 1); err != nil {
		return nil, userErr(err)
	}
	if err := check.ValidateProxy(p); err != nil {
		return nil, userErr(err)
	}
	saved, changed, err := s.store.SaveProxy(ctx, p, in.ApplyExisting)
	if err != nil {
		return nil, err
	}
	s.refreshMonitors(ctx, changed)
	s.sendProxyList(ctx, nil)
	return okI18n("Saved.", "id", saved.ID), nil
}

func (s *Server) deleteProxy(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	changed, err := s.store.DeleteProxy(ctx, id)
	if err != nil {
		return nil, err
	}
	s.refreshMonitors(ctx, changed)
	s.sendProxyList(ctx, nil)
	return okI18n("successDeleted"), nil
}
