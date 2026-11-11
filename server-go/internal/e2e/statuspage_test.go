package e2e

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/holidays"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/ui"
)

const testIndex = `<!DOCTYPE html><html lang="en"><head><meta charset="UTF-8" />
<link rel="apple-touch-icon" sizes="180x180" href="/apple-touch-icon.png">
<link rel="icon" type="image/svg+xml" href="/icon.svg" />
<link rel="manifest" href="/manifest.json" />
<meta name="description" content="Uptime Kuma monitoring tool" />
<title>Uptime Kuma</title></head><body><div id="app"></div></body></html>`

// A 1x1 PNG.
const testPNG = "iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="

type nopScheduler struct{}

func (nopScheduler) Upsert(monitor.Monitor)               {}
func (nopScheduler) Remove(context.Context, int32) error  { return nil }
func (nopScheduler) Suspend(context.Context, int32) error { return nil }

// uiApp is the web UI's backend on a real database, logged in.
type uiApp struct {
	url    string
	client *http.Client
	db     *store.Store
	maint  *maintenance.Service

	connID string
	stream *bufio.Reader
	nextID int
}

func startUI(t *testing.T) *uiApp {
	t.Helper()
	ctx := context.Background()
	db, err := store.Open(ctx, newDatabase(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	calendar, err := holidays.New()
	if err != nil {
		t.Fatal(err)
	}
	service := maintenance.NewService(db, calendar, time.UTC, log)
	if err := service.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	server := ui.New(db, nopScheduler{}, notify.DefaultEnv(""), log, ui.Options{
		Maintenance: service, StatusPages: db, IndexHTML: []byte(testIndex),
	})
	mux := http.NewServeMux()
	server.Register(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(func() { server.Close(); ts.Close() })

	jar, _ := cookiejar.New(nil)
	a := &uiApp{url: ts.URL, client: &http.Client{Jar: jar, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}, db: db, maint: service}
	for _, path := range []string{"/api/setup", "/api/auth/sign-in/username"} {
		res, err := a.client.Post(ts.URL+path, "application/json", strings.NewReader(`{"username":"admin","password":"long enough"}`))
		if err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("%s: %v %v", path, res, err)
		}
		_ = res.Body.Close()
	}

	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/realtime/stream", nil)
	req.Header.Set("Accept-Encoding", "identity")
	streamClient := &http.Client{Jar: jar}
	res, err := streamClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = res.Body.Close() })
	a.stream = bufio.NewReader(res.Body)
	event, data := a.frame(t)
	if event != "open" {
		t.Fatalf("first frame %q", event)
	}
	var open struct{ ID string }
	_ = json.Unmarshal(data, &open)
	a.connID = open.ID
	return a
}

func (a *uiApp) frame(t *testing.T) (string, json.RawMessage) {
	t.Helper()
	var event, data string
	for {
		line, err := a.stream.ReadString('\n')
		if err != nil {
			t.Fatalf("read stream: %v", err)
		}
		line = strings.TrimRight(line, "\n")
		switch {
		case line == "" && data != "":
			return event, json.RawMessage(data)
		case strings.HasPrefix(line, "event: "):
			event = line[7:]
		case strings.HasPrefix(line, "data: "):
			data += line[6:]
		}
	}
}

// emit sends an event and returns its reply, and the names of the messages that came first.
func (a *uiApp) emit(t *testing.T, event string, args ...any) (map[string]any, []string) {
	t.Helper()
	a.nextID++
	ack := a.nextID
	body, _ := json.Marshal(map[string]any{"id": a.connID, "messages": []any{map[string]any{"event": event, "args": args, "ack": ack}}})
	res, err := a.client.Post(a.url+"/api/realtime/send", "application/json", strings.NewReader(string(body)))
	if err != nil || res.StatusCode != http.StatusAccepted {
		t.Fatalf("send %s: %v %v", event, res, err)
	}
	_ = res.Body.Close()
	var before []string
	for {
		kind, data := a.frame(t)
		var parts []json.RawMessage
		_ = json.Unmarshal(data, &parts)
		if kind == "message" {
			var name string
			_ = json.Unmarshal(parts[0], &name)
			before = append(before, name)
			continue
		}
		if kind == "ack" && string(parts[0]) == fmt.Sprint(ack) {
			var reply map[string]any
			_ = json.Unmarshal(parts[1], &reply)
			return reply, before
		}
	}
}

func (a *uiApp) get(t *testing.T, path string, headers ...string) (*http.Response, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, a.url+path, nil)
	for i := 0; i+1 < len(headers); i += 2 {
		if headers[i] == "Host" {
			req.Host = headers[i+1]
		} else {
			req.Header.Set(headers[i], headers[i+1])
		}
	}
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(res.Body)
	return res, string(body)
}

func (a *uiApp) getJSON(t *testing.T, path string, want int, out any) {
	t.Helper()
	res, body := a.get(t, path)
	if res.StatusCode != want {
		t.Fatalf("GET %s: %d, want %d: %s", path, res.StatusCode, want, body)
	}
	if err := json.Unmarshal([]byte(body), out); err != nil {
		t.Fatalf("GET %s: %v: %s", path, err, body)
	}
}

func mustOK(t *testing.T, what string, reply map[string]any) {
	t.Helper()
	if reply["ok"] != true {
		t.Fatalf("%s: %v", what, reply)
	}
}

func TestStatusPages(t *testing.T) {
	a := startUI(t)
	ctx := context.Background()

	newMonitor := func(name, url string) monitor.Monitor {
		m := monitor.New()
		m.Name, m.Type, m.URL, m.Active = name, monitor.TypeHTTP, url, false
		created, err := a.db.CreateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	web := newMonitor("Website", "https://user:secret@example.com/health?token=1")
	api := newMonitor("API", "https://api.example.com")
	private := newMonitor("Private", "https://private.example.com")
	now := time.Now().UTC().Add(-time.Second)
	ping := 42.0
	if _, err := a.db.InsertBeats(ctx, []store.Beat{
		{MonitorID: web.ID, Time: now, Status: monitor.StatusUp, Ping: &ping, Msg: "secret detail"},
		{MonitorID: api.ID, Time: now, Status: monitor.StatusDown, Msg: "connection refused"},
		{MonitorID: private.ID, Time: now, Status: monitor.StatusUp, Ping: &ping},
	}); err != nil {
		t.Fatal(err)
	}
	if err := a.db.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}

	reply, _ := a.emit(t, "addStatusPage", "Main", "Main")
	mustOK(t, "add", reply)
	if reply["slug"] != "main" {
		t.Fatalf("slug: %v", reply)
	}
	if reply, _ := a.emit(t, "addStatusPage", "Again", "main"); reply["ok"] != false || !strings.Contains(reply["msg"].(string), "slug") {
		t.Fatalf("duplicate: %v", reply)
	}
	if reply, _ := a.emit(t, "addStatusPage", "Bad", "no spaces"); reply["msg"] != "Invalid Slug" {
		t.Fatalf("bad slug: %v", reply)
	}

	hostile := `Main </script><script>alert(1)</script>`
	config := map[string]any{
		"slug": "main", "title": hostile, "description": "**All** systems [run](https://x.example) here",
		"theme": "dark", "autoRefreshInterval": 60, "showTags": true, "showCertificateExpiry": true,
		"domainNameList": []string{"Status.Example.com", ""}, "footerText": "Footer", "customCSS": "body {}",
		"showPoweredBy": false, "analyticsType": "plausible", "analyticsId": `example.com" onload="alert(1)`,
		"analyticsScriptUrl": "https://plausible.io/js/script.js", "rssTitle": "",
	}
	groups := []map[string]any{{"name": "Services", "monitorList": []map[string]any{
		{"id": web.ID, "sendUrl": true}, {"id": api.ID, "sendUrl": false},
	}}}
	reply, before := a.emit(t, "saveStatusPage", "main", config, "data:image/png;base64,"+testPNG, groups)
	mustOK(t, "save", reply)
	if !strings.Contains(strings.Join(before, ","), "statusPageList") {
		t.Fatalf("no statusPageList after saving: %v", before)
	}
	if reply, _ := a.emit(t, "saveStatusPage", "main", config, "data:image/gif;base64,R0lG", groups); reply["msg"] != "Only allowed PNG logo." {
		t.Fatalf("gif logo: %v", reply)
	}
	if reply, _ := a.emit(t, "saveStatusPage", "main", config, "javascript:alert(1)", groups); reply["ok"] != false {
		t.Fatalf("javascript logo: %v", reply)
	}

	var page struct {
		Config          map[string]any   `json:"config"`
		Incidents       []map[string]any `json:"incidents"`
		PublicGroupList []struct {
			Name        string           `json:"name"`
			MonitorList []map[string]any `json:"monitorList"`
		} `json:"publicGroupList"`
		MaintenanceList []map[string]any `json:"maintenanceList"`
	}
	a.getJSON(t, "/api/status-page/MAIN", http.StatusOK, &page)
	if page.Config["title"] != hostile || page.Config["theme"] != "dark" || page.Config["domainNameList"] != nil {
		t.Fatalf("config: %v", page.Config)
	}
	icon, _ := page.Config["icon"].(string)
	if !strings.HasPrefix(icon, "/upload/logo") {
		t.Fatalf("icon: %v", icon)
	}
	monitors := page.PublicGroupList[0].MonitorList
	if len(monitors) != 2 || monitors[0]["url"] != "https://example.com/health?token=1" || monitors[1]["url"] != nil {
		t.Fatalf("monitors: %v", monitors)
	}
	if _, hasTags := monitors[0]["tags"]; !hasTags || monitors[0]["validCert"] != false {
		t.Fatalf("tags and certificates: %v", monitors[0])
	}

	var beats struct {
		HeartbeatList map[string][]map[string]any `json:"heartbeatList"`
		UptimeList    map[string]float64          `json:"uptimeList"`
	}
	a.getJSON(t, "/api/status-page/heartbeat/main", http.StatusOK, &beats)
	webBeats := beats.HeartbeatList[fmt.Sprint(web.ID)]
	if len(beats.HeartbeatList) != 2 || len(webBeats) != 1 || webBeats[0]["msg"] != "" || webBeats[0]["ping"] != 42.0 {
		t.Fatalf("heartbeats: %v", beats.HeartbeatList)
	}
	if beats.UptimeList[fmt.Sprint(web.ID)+"_24"] != 1 || beats.UptimeList[fmt.Sprint(api.ID)+"_24"] != 0 {
		t.Fatalf("uptime: %v", beats.UptimeList)
	}

	if res, _ := a.get(t, "/api/status-page/main"); res.Header.Get("Cache-Control") != "no-cache" {
		t.Fatalf("page data may be kept by browsers: %q", res.Header.Get("Cache-Control"))
	}
	if res, _ := a.get(t, fmt.Sprintf("/api/badge/%d/status", web.ID)); res.Header.Get("Cache-Control") != "public, max-age=60" ||
		res.Header.Get("Access-Control-Allow-Origin") != "*" {
		t.Fatalf("badge headers: %v", res.Header)
	}

	var manifest map[string]any
	a.getJSON(t, "/api/status-page/main/manifest.json", http.StatusOK, &manifest)
	if manifest["start_url"] != "/status/main" {
		t.Fatalf("manifest: %v", manifest)
	}

	res, html := a.get(t, "/status/main")
	if res.StatusCode != http.StatusOK || !strings.Contains(html, "window.preloadData = ") ||
		!strings.Contains(html, `<meta property="og:title" content="Main &lt;/script&gt;`) ||
		!strings.Contains(html, `<meta name="description" content="All systems run here"/>`) ||
		!strings.Contains(html, `href="/api/status-page/main/manifest.json"`) ||
		!strings.Contains(html, `<script defer="" src="https://plausible.io/js/script.js" data-domain="example.com&#34; onload=&#34;alert(1)">`) ||
		strings.Contains(html, "apple-touch-icon") || !strings.Contains(html, `<link rel="icon" href="/upload/logo`) {
		t.Fatalf("page HTML: %s", html)
	}
	if strings.Contains(html, "</script><script>alert(1)") {
		t.Fatalf("the title closed a script element: %s", html)
	}
	if res, _ := a.get(t, "/status/missing"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("missing page: %d", res.StatusCode)
	}
	res, logo := a.get(t, strings.Split(icon, "?")[0])
	if want, _ := base64.StdEncoding.DecodeString(testPNG); res.StatusCode != http.StatusOK || logo != string(want) || res.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("logo: %d %q", res.StatusCode, logo)
	}

	// The custom domain shows the page at its root, and the UI's entry route finds it too.
	if res, body := a.get(t, "/", "Host", "status.example.com:8080"); res.StatusCode != http.StatusOK || !strings.Contains(body, "preloadData") {
		t.Fatalf("custom domain: %d", res.StatusCode)
	}
	var entry map[string]string
	if _, body := a.get(t, "/api/entry-page", "Host", "status.example.com"); json.Unmarshal([]byte(body), &entry) != nil || entry["statusPageSlug"] != "main" {
		t.Fatalf("entry page for the domain: %s", body)
	}
	if res, _ := a.get(t, "/"); res.StatusCode != http.StatusFound || res.Header.Get("Location") != "/dashboard" {
		t.Fatalf("root: %d %s", res.StatusCode, res.Header.Get("Location"))
	}

	// Incidents.
	reply, _ = a.emit(t, "postIncident", "main", map[string]any{"title": "Outage", "content": "We're on it", "style": "bogus"})
	mustOK(t, "post incident", reply)
	incident := reply["incident"].(map[string]any)
	if incident["style"] != "warning" || incident["pin"] != true {
		t.Fatalf("incident: %v", incident)
	}
	if reply, _ := a.emit(t, "postIncident", "main", map[string]any{"title": "", "content": "x"}); reply["msg"] != "Please input title" {
		t.Fatalf("empty title: %v", reply)
	}
	a.getJSON(t, "/api/status-page/main", http.StatusOK, &page)
	if len(page.Incidents) != 1 || page.Incidents[0]["title"] != "Outage" {
		t.Fatalf("pinned incidents: %v", page.Incidents)
	}
	res, rss := a.get(t, "/status/main/rss")
	if res.StatusCode != http.StatusOK || !strings.Contains(rss, "<title><![CDATA[Outage]]></title>") || !strings.Contains(rss, "<title><![CDATA[API is down]]></title>") ||
		!strings.Contains(rss, "Current status: Partially Degraded Service") || strings.Contains(rss, "Private") {
		t.Fatalf("rss: %d %s", res.StatusCode, rss)
	}
	id := incident["id"]
	reply, _ = a.emit(t, "editIncident", "main", id, map[string]any{"title": "Outage", "content": "Fixed soon", "style": "danger", "pin": true})
	mustOK(t, "edit incident", reply)
	reply, _ = a.emit(t, "resolveIncident", "main", id)
	mustOK(t, "resolve", reply)
	if resolved := reply["incident"].(map[string]any); resolved["active"] != false || resolved["lastUpdatedDate"] == nil {
		t.Fatalf("resolved: %v", resolved)
	}
	var history map[string]any
	a.getJSON(t, "/api/status-page/main/incident-history", http.StatusOK, &history)
	if history["total"] != 1.0 || history["hasMore"] != false {
		t.Fatalf("history: %v", history)
	}
	if reply, _ := a.emit(t, "deleteIncident", "main", 99999); reply["ok"] != false {
		t.Fatalf("delete missing incident: %v", reply)
	}
	mustOK(t, "delete incident", func() map[string]any { r, _ := a.emit(t, "deleteIncident", "main", id); return r }())

	// Badges: public monitors only, and the same SVG as badge-maker's.
	badge := func(path string) (int, string) {
		res, body := a.get(t, path)
		return res.StatusCode, body
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/status", web.ID)); code != 200 || !strings.Contains(svg, `aria-label="Status: Up"`) {
		t.Fatalf("status badge: %d %s", code, svg)
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/status", private.ID)); code != 200 || !strings.Contains(svg, `aria-label="N/A"`) {
		t.Fatalf("private badge: %d %s", code, svg)
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/uptime/24?style=for-the-badge", web.ID)); code != 200 || !strings.Contains(svg, "UPTIME (24H): 100.0%") {
		t.Fatalf("uptime badge: %d %s", code, svg)
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/ping/1d", web.ID)); code != 200 || !strings.Contains(svg, "Avg. Ping (1h): 42ms") {
		t.Fatalf("ping badge: %d %s", code, svg)
	}
	if code, _ := badge(fmt.Sprintf("/api/badge/%d/uptime/721h", web.ID)); code != http.StatusBadRequest {
		t.Fatalf("too long: %d", code)
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/response", web.ID)); code != 200 || !strings.Contains(svg, "Response: 42ms") {
		t.Fatalf("response badge: %d %s", code, svg)
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/cert-exp", web.ID)); code != 200 || !strings.Contains(svg, "No/Bad Cert") {
		t.Fatalf("cert badge: %d %s", code, svg)
	}
	if code, svg := badge("/api/status-page/main/badge?label=Services"); code != 200 || !strings.Contains(svg, "Services: Degraded") {
		t.Fatalf("page badge: %d %s", code, svg)
	}
	if code, _ := badge(fmt.Sprintf("/api/badge/%d/status?style=round", web.ID)); code != http.StatusBadRequest {
		t.Fatalf("unknown style: %d", code)
	}

	// A running window linked to the page shows on it.
	window, err := a.db.CreateMaintenance(ctx, maintenance.Window{Title: "Upgrade", Strategy: maintenance.StrategyManual, Active: true, Timezone: maintenance.SameAsServer})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.maint.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	pageID := int32(0)
	{
		p, err := a.db.StatusPageBySlug(ctx, "main")
		if err != nil {
			t.Fatal(err)
		}
		pageID = p.ID
	}
	mustOK(t, "link window", func() map[string]any {
		r, _ := a.emit(t, "addMaintenanceStatusPage", window.ID, []map[string]any{{"id": pageID, "title": "x"}})
		return r
	}())
	reply, _ = a.emit(t, "getMaintenanceStatusPage", window.ID)
	if pages := reply["statusPages"].([]any); len(pages) != 1 {
		t.Fatalf("window's pages: %v", reply)
	}
	a.getJSON(t, "/api/status-page/main", http.StatusOK, &page)
	if len(page.MaintenanceList) != 1 || page.MaintenanceList[0]["title"] != "Upgrade" {
		t.Fatalf("maintenance on the page: %v", page.MaintenanceList)
	}

	// Renaming a page that's the entry page moves the entry page along.
	if err := a.db.SaveSettings(ctx, map[string]json.RawMessage{"entryPage": json.RawMessage(`"statusPage-main"`)}); err != nil {
		t.Fatal(err)
	}
	config["slug"] = "renamed"
	reply, _ = a.emit(t, "saveStatusPage", "main", config, icon, groups)
	mustOK(t, "rename", reply)
	if res, _ := a.get(t, "/"); res.Header.Get("Location") != "/status/renamed" {
		t.Fatalf("entry page after renaming: %s", res.Header.Get("Location"))
	}
	if res, _ := a.get(t, "/api/status-page/main"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("old slug: %d", res.StatusCode)
	}
	if res, _ := a.get(t, strings.Split(icon, "?")[0]); res.StatusCode != http.StatusOK {
		t.Fatalf("logo kept: %d", res.StatusCode)
	}

	mustOK(t, "delete page", func() map[string]any { r, _ := a.emit(t, "deleteStatusPage", "renamed"); return r }())
	if res, _ := a.get(t, "/api/status-page/renamed"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("deleted page: %d", res.StatusCode)
	}
	if res, _ := a.get(t, "/"); res.Header.Get("Location") != "/dashboard" {
		t.Fatalf("entry page after deleting: %s", res.Header.Get("Location"))
	}
	if code, svg := badge(fmt.Sprintf("/api/badge/%d/status?x=1", web.ID)); code != 200 || !strings.Contains(svg, "N/A") {
		t.Fatalf("badge after deleting the page: %d %s", code, svg)
	}
}
