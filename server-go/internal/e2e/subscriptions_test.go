package e2e

import (
	"context"
	"encoding/json"
	"io"
	"mime/quotedprintable"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// inbox is an SMTP server that keeps what it's sent.
type inbox struct {
	mu   sync.Mutex
	mail []sentMail
	port int
}

type sentMail struct {
	to   string
	data string
}

func (b *inbox) NewSession(*smtp.Conn) (smtp.Session, error) { return &inboxSession{b: b}, nil }

type inboxSession struct {
	b  *inbox
	to string
}

func (s *inboxSession) Mail(string, *smtp.MailOptions) error { return nil }
func (s *inboxSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.to = to
	return nil
}

func (s *inboxSession) Data(r io.Reader) error {
	raw, _ := io.ReadAll(r)
	s.b.mu.Lock()
	defer s.b.mu.Unlock()
	s.b.mail = append(s.b.mail, sentMail{to: s.to, data: string(raw)})
	return nil
}
func (s *inboxSession) Reset()        {}
func (s *inboxSession) Logout() error { return nil }

func startInbox(t *testing.T) *inbox {
	t.Helper()
	b := &inbox{}
	server := smtp.NewServer(b)
	server.Domain = "localhost"
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	b.port = listener.Addr().(*net.TCPAddr).Port
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return b
}

func (b *inbox) all() []sentMail {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]sentMail(nil), b.mail...)
}

// body is a message's decoded text.
func (m sentMail) body(t *testing.T) string {
	t.Helper()
	_, body, _ := strings.Cut(strings.ReplaceAll(m.data, "\r\n", "\n"), "\n\n")
	text, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

// hooks is a webhook receiver that keeps what it's sent, and fails on /fail.
type hooks struct {
	mu   sync.Mutex
	got  []map[string]any
	srv  *httptest.Server
	seen []string
}

func startHooks(t *testing.T) *hooks {
	h := &hooks{}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		h.mu.Lock()
		h.seen = append(h.seen, r.URL.Path+" "+r.Header.Get("Content-Type"))
		if r.URL.Path != "/fail" {
			h.got = append(h.got, body)
		}
		h.mu.Unlock()
		if r.URL.Path == "/fail" {
			http.Error(w, "nope", http.StatusBadGateway)
		}
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *hooks) events() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	out := make([]string, len(h.got))
	for i, b := range h.got {
		out[i] = b["event"].(string) + " " + b["incident"].(map[string]any)["title"].(string)
	}
	return out
}

func (a *uiApp) post(t *testing.T, path, contentType, body string) (int, map[string]any) {
	t.Helper()
	res, err := a.client.Post(a.url+path, contentType, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

var tokenInLink = regexp.MustCompile(`subscription=confirm&token=([A-Za-z0-9_-]+)`)

func TestStatusPageSubscriptions(t *testing.T) {
	a := startUI(t)
	ctx := context.Background()
	mail := startInbox(t)
	hook := startHooks(t)

	mustOK(t, "add page", func() map[string]any { r, _ := a.emit(t, "addStatusPage", "Main", "main"); return r }())
	page, err := a.db.StatusPageBySlug(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	smtpN, err := a.db.CreateNotification(ctx, store.Notification{Name: "Mail", Type: "smtp", Active: true, Config: map[string]any{
		"smtpHost": "127.0.0.1", "smtpPort": mail.port, "smtpFrom": "Status <status@example.com>", "smtpTo": "ops@example.com",
		"smtpIgnoreSTARTTLS": true,
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	slack, err := a.db.CreateNotification(ctx, store.Notification{Name: "Slack", Type: "slack", Active: true, Config: map[string]any{}}, false)
	if err != nil {
		t.Fatal(err)
	}

	// The page needs an SMTP notification, and the server's address for its links.
	if r, _ := a.emit(t, "setStatusPageSubscriptions", "main", map[string]any{"notificationId": slack.ID}); r["ok"] != false {
		t.Fatalf("a Slack sender: %v", r)
	}
	if r, _ := a.emit(t, "setStatusPageSubscriptions", "main", map[string]any{"notificationId": smtpN.ID}); r["ok"] != false ||
		!strings.Contains(r["msg"].(string), "Primary Base URL") {
		t.Fatalf("no base URL: %v", r)
	}
	if code, _ := a.post(t, "/api/status-page/main/subscribe", "application/json", `{"email":"ann@example.com"}`); code != http.StatusBadRequest {
		t.Fatalf("subscribed to a page without email: %d", code)
	}
	if err := a.db.SaveSettings(ctx, map[string]json.RawMessage{"primaryBaseURL": json.RawMessage(`"https://kuma.example/"`)}); err != nil {
		t.Fatal(err)
	}
	mustOK(t, "set sender", func() map[string]any {
		r, _ := a.emit(t, "setStatusPageSubscriptions", "main", map[string]any{"notificationId": smtpN.ID})
		return r
	}())
	var public struct {
		Config map[string]any `json:"config"`
	}
	a.getJSON(t, "/api/status-page/main", http.StatusOK, &public)
	if public.Config["subscribable"] != true {
		t.Fatalf("public config: %v", public.Config)
	}

	// Subscribing sends one confirmation, however often it's asked for.
	for _, bad := range []string{`{"email":"not-an-address"}`, `{"email":"Ann <ann@example.com>"}`, `{"email":"ann@localhost"}`, `{}`} {
		if code, _ := a.post(t, "/api/status-page/main/subscribe", "application/json", bad); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", bad, code)
		}
	}
	if code, _ := a.post(t, "/api/status-page/nope/subscribe", "application/json", `{"email":"ann@example.com"}`); code != http.StatusNotFound {
		t.Fatalf("unknown page: %d", code)
	}
	for range 2 {
		if code, out := a.post(t, "/api/status-page/main/subscribe", "application/json", `{"email":"ann@example.com"}`); code != http.StatusOK {
			t.Fatalf("subscribe: %d %v", code, out)
		}
	}
	waitFor(t, "the confirmation", func() bool { return len(mail.all()) >= 1 })
	time.Sleep(300 * time.Millisecond)
	if got := mail.all(); len(got) != 1 || got[0].to != "ann@example.com" || !strings.Contains(got[0].data, "Subject: Confirm your subscription to Main") {
		t.Fatalf("confirmations: %+v", got)
	}
	confirmation := mail.all()[0].body(t)
	if !strings.Contains(confirmation, "https://kuma.example/status/main?subscription=confirm&token=") {
		t.Fatalf("confirmation: %s", confirmation)
	}
	token := tokenInLink.FindStringSubmatch(confirmation)[1]

	// A webhook, and one that fails.
	mustOK(t, "add webhook", func() map[string]any { r, _ := a.emit(t, "addStatusPageWebhook", "main", hook.srv.URL+"/ok"); return r }())
	mustOK(t, "add failing webhook", func() map[string]any {
		r, _ := a.emit(t, "addStatusPageWebhook", "main", hook.srv.URL+"/fail")
		return r
	}())
	if r, _ := a.emit(t, "addStatusPageWebhook", "main", hook.srv.URL+"/ok"); r["ok"] != false {
		t.Fatalf("the same webhook twice: %v", r)
	}
	if r, _ := a.emit(t, "addStatusPageWebhook", "main", "ftp://example.com/x"); r["ok"] != false {
		t.Fatalf("an ftp webhook: %v", r)
	}

	// An unconfirmed address isn't sent incidents.
	reply, _ := a.emit(t, "postIncident", "main", map[string]any{"title": "Database slow", "content": "Looking into it", "style": "warning", "notify": true})
	mustOK(t, "post", reply)
	incidentID := reply["incident"].(map[string]any)["id"]
	waitFor(t, "the first webhook", func() bool { return len(hook.events()) == 1 })
	if got := hook.events(); got[0] != "incident.created Database slow" {
		t.Fatalf("webhook: %v", got)
	}
	if len(mail.all()) != 1 {
		t.Fatalf("an unconfirmed address got an incident: %d", len(mail.all()))
	}

	// Confirmed, it gets the update and the resolution, with a way to unsubscribe.
	if code, _ := a.post(t, "/api/status-page/main/confirm", "application/json", `{"token":"wrong"}`); code != http.StatusNotFound {
		t.Fatalf("wrong token: %d", code)
	}
	if code, _ := a.post(t, "/api/status-page/main/confirm", "application/json", `{"token":"`+token+`"}`); code != http.StatusOK {
		t.Fatalf("confirm: %d", code)
	}
	mustOK(t, "update", func() map[string]any {
		r, _ := a.emit(t, "postIncident", "main", map[string]any{"id": incidentID, "title": "Database slow", "content": "Fixed the index", "style": "info", "notify": true})
		return r
	}())
	mustOK(t, "resolve", func() map[string]any { r, _ := a.emit(t, "resolveIncident", "main", incidentID); return r }())
	waitFor(t, "two emails", func() bool { return len(mail.all()) == 3 })
	waitFor(t, "three webhooks", func() bool { return len(hook.events()) == 3 })
	sent := mail.all()
	if !strings.Contains(sent[1].data, "Subject: [Main] Update: Database slow") || !strings.Contains(sent[1].body(t), "Fixed the index") {
		t.Fatalf("update mail: %s", sent[1].data)
	}
	if !strings.Contains(sent[2].data, "Subject: [Main] Resolved: Database slow") {
		t.Fatalf("resolved mail: %s", sent[2].data)
	}
	for _, want := range []string{"List-Unsubscribe: <https://kuma.example/api/status-page/main/unsubscribe?token=" + token + ">",
		"List-Unsubscribe-Post: List-Unsubscribe=One-Click"} {
		if !strings.Contains(sent[1].data, want) {
			t.Fatalf("missing %q: %s", want, sent[1].data)
		}
	}
	if body := sent[1].body(t); !strings.Contains(body, "https://kuma.example/status/main?subscription=unsubscribe&token="+token) {
		t.Fatalf("unsubscribe link: %s", body)
	}
	if got := hook.events(); got[1] != "incident.updated Database slow" || got[2] != "incident.resolved Database slow" {
		t.Fatalf("webhook order: %v", got)
	}

	// An incident not sent to subscribers isn't when it's resolved either.
	reply, _ = a.emit(t, "postIncident", "main", map[string]any{"title": "Quiet", "content": "Typo fix", "style": "info"})
	mustOK(t, "quiet post", reply)
	mustOK(t, "quiet resolve", func() map[string]any {
		r, _ := a.emit(t, "resolveIncident", "main", reply["incident"].(map[string]any)["id"])
		return r
	}())
	time.Sleep(500 * time.Millisecond)
	if len(mail.all()) != 3 || len(hook.events()) != 3 {
		t.Fatalf("a quiet incident was sent: %d mails, %v", len(mail.all()), hook.events())
	}

	// Admins see who's subscribed, and what failed.
	reply, _ = a.emit(t, "getStatusPageSubscribers", "main")
	mustOK(t, "subscribers", reply)
	subs := reply["subscribers"].([]any)
	if len(subs) != 3 || reply["notificationId"].(float64) != float64(smtpN.ID) {
		t.Fatalf("subscribers: %v", reply)
	}
	var failing map[string]any
	for _, s := range subs {
		m := s.(map[string]any)
		if strings.HasSuffix(m["address"].(string), "/fail") {
			failing = m
		} else if m["lastError"] != "" || m["lastSentDate"] == nil {
			t.Fatalf("delivered subscriber: %v", m)
		}
	}
	if !strings.Contains(failing["lastError"].(string), "502") {
		t.Fatalf("failing webhook: %v", failing)
	}
	mustOK(t, "remove", func() map[string]any {
		r, _ := a.emit(t, "removeStatusPageSubscriber", "main", failing["id"])
		return r
	}())

	// A mail program's one-click unsubscribe.
	code, _ := a.post(t, "/api/status-page/main/unsubscribe?token="+url.QueryEscape(token), "application/x-www-form-urlencoded", "List-Unsubscribe=One-Click")
	if code != http.StatusOK {
		t.Fatalf("unsubscribe: %d", code)
	}
	if n, _ := a.db.CountSubscribers(ctx, page.ID); n != 1 {
		t.Fatalf("subscribers after unsubscribing: %d", n)
	}

	// Turning email off stops the page taking subscriptions.
	mustOK(t, "email off", func() map[string]any {
		r, _ := a.emit(t, "setStatusPageSubscriptions", "main", map[string]any{"notificationId": nil})
		return r
	}())
	a.getJSON(t, "/api/status-page/main", http.StatusOK, &public)
	if public.Config["subscribable"] != false {
		t.Fatalf("still subscribable: %v", public.Config)
	}
}

func TestIncidentTemplatesAndUpcomingMaintenance(t *testing.T) {
	a := startUI(t)
	ctx := context.Background()
	mustOK(t, "add page", func() map[string]any { r, _ := a.emit(t, "addStatusPage", "Main", "main"); return r }())

	reply, _ := a.emit(t, "saveIncidentTemplate", map[string]any{"title": "Investigating", "content": "We're looking into it.", "style": "warning"})
	mustOK(t, "save template", reply)
	mustOK(t, "replace template", func() map[string]any {
		r, _ := a.emit(t, "saveIncidentTemplate", map[string]any{"title": "Investigating", "content": "Still looking.", "style": "bogus"})
		return r
	}())
	if r, _ := a.emit(t, "saveIncidentTemplate", map[string]any{"title": "", "content": "x"}); r["ok"] != false {
		t.Fatalf("a template without a title: %v", r)
	}
	reply, _ = a.emit(t, "getIncidentTemplates", nil)
	templates := reply["templates"].([]any)
	if len(templates) != 1 {
		t.Fatalf("templates: %v", reply)
	}
	tpl := templates[0].(map[string]any)
	if tpl["content"] != "Still looking." || tpl["style"] != "warning" {
		t.Fatalf("template: %v", tpl)
	}
	mustOK(t, "delete template", func() map[string]any { r, _ := a.emit(t, "deleteIncidentTemplate", tpl["id"]); return r }())

	// Windows linked to the page that start within a week show as coming; later ones and
	// unlinked ones don't.
	page, _ := a.db.StatusPageBySlug(ctx, "main")
	at := func(d time.Duration) string { return time.Now().UTC().Add(d).Format(maintenance.LocalDateTimeFormat) }
	window := func(title string, start time.Duration) maintenance.Window {
		w, err := a.db.CreateMaintenance(ctx, maintenance.Window{Title: title, Description: "**Planned**", Strategy: maintenance.StrategySingle,
			Active: true, Timezone: "UTC", StartDate: at(start), EndDate: at(start + 2*time.Hour)})
		if err != nil {
			t.Fatal(err)
		}
		return w
	}
	soon, later, unlinked := window("Database upgrade", 50*time.Hour), window("Move", 10*24*time.Hour), window("Other", time.Hour)
	_ = unlinked
	sooner := window("Network", 3*time.Hour)
	for _, w := range []maintenance.Window{soon, later, sooner} {
		if err := a.db.SetMaintenanceStatusPages(ctx, w.ID, []int32{page.ID}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.maint.Refresh(ctx); err != nil {
		t.Fatal(err)
	}
	var data struct {
		Upcoming []map[string]any `json:"upcomingMaintenanceList"`
		Current  []map[string]any `json:"maintenanceList"`
	}
	a.getJSON(t, "/api/status-page/main", http.StatusOK, &data)
	if len(data.Upcoming) != 2 || data.Upcoming[0]["title"] != "Network" || data.Upcoming[1]["title"] != "Database upgrade" || len(data.Current) != 0 {
		t.Fatalf("upcoming: %v current: %v", data.Upcoming, data.Current)
	}
	start, err := time.Parse(time.RFC3339, data.Upcoming[0]["start"].(string))
	if err != nil || start.Sub(time.Now()) < 2*time.Hour || start.Sub(time.Now()) > 3*time.Hour {
		t.Fatalf("start: %v %v", data.Upcoming[0]["start"], err)
	}
	if data.Upcoming[1]["description"] != "**Planned**" {
		t.Fatalf("description: %v", data.Upcoming[1])
	}
}
