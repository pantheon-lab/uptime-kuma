package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Status page subscriptions: people subscribe to a page's incidents by email, which they confirm
// from a link, and admins add webhooks. Incidents go out from a queue, in order, one subscriber at
// a time, through the SMTP notification the page names.
const (
	maxSubscribers       = 10000
	maxIncidentTemplates = 200
	// confirmInterval is how often an address can be sent a confirmation.
	confirmInterval = 10 * time.Minute
	// unconfirmedFor is how long an email subscription waits for its confirmation.
	unconfirmedFor   = 48 * time.Hour
	sendTimeout      = 30 * time.Second
	deliveryQueueLen = 256
	maxStoredError   = 300
	maxEmailLength   = 254
	maxWebhookLength = 2048
	// upcomingWithin is how far ahead a page shows maintenance windows.
	upcomingWithin = 7 * 24 * time.Hour
)

// What an incident's message says happened.
const (
	incidentCreated  = "incident.created"
	incidentUpdated  = "incident.updated"
	incidentResolved = "incident.resolved"
)

// delivery is one message for a page's subscribers: an incident, or a confirmation for one of them.
type delivery struct {
	page     store.StatusPage
	incident *store.Incident
	event    string
	// confirm is the subscriber to send a confirmation to, instead of an incident.
	confirm *store.Subscriber
}

// deliveries runs the queue of messages to subscribers, in order, on one goroutine.
type deliveries struct {
	once   sync.Once
	queue  chan delivery
	ctx    context.Context
	cancel context.CancelFunc
	done   chan struct{}
	// subscribe limits subscription requests per address, and overall.
	subscribe *loginLimiter
}

func newDeliveries() *deliveries {
	ctx, cancel := context.WithCancel(context.Background())
	return &deliveries{
		queue: make(chan delivery, deliveryQueueLen), ctx: ctx, cancel: cancel, done: make(chan struct{}),
		subscribe: &loginLimiter{
			global: rate.NewLimiter(rate.Every(2*time.Second), 30), users: map[string]*rate.Limiter{},
			perUser: rate.Every(time.Minute), burst: 3,
		},
	}
}

// enqueue adds a message, starting the queue's goroutine the first time. It drops the message,
// and logs it, when the queue is full.
func (s *Server) enqueue(d delivery) {
	s.subs.once.Do(func() {
		go func() {
			defer close(s.subs.done)
			for {
				select {
				case <-s.subs.ctx.Done():
					return
				case d := <-s.subs.queue:
					s.deliver(s.subs.ctx, d)
				}
			}
		}()
	})
	select {
	case s.subs.queue <- d:
	default:
		s.log.Error("subscriber queue full, message dropped", "page", d.page.Slug, "event", d.event)
	}
}

// stopDeliveries ends the queue, abandoning what's left in it, and waits for a send under way.
func (s *Server) stopDeliveries() {
	s.subs.cancel()
	s.subs.once.Do(func() { close(s.subs.done) }) // It never started.
	select {
	case <-s.subs.done:
	case <-time.After(sendTimeout):
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if len(text) > maxStoredError {
		text = text[:maxStoredError]
	}
	return text
}

// pageURL is the address of a page, with a query if one is given.
func pageURL(base, slug string, query url.Values) string {
	u := strings.TrimRight(base, "/") + "/status/" + url.PathEscape(slug)
	if len(query) > 0 {
		u += "?" + query.Encode()
	}
	return u
}

// deliver sends one message from the queue.
func (s *Server) deliver(ctx context.Context, d delivery) {
	base := s.baseURL(ctx)
	if d.confirm != nil {
		err := s.sendConfirmation(ctx, d.page, *d.confirm, base)
		if err != nil {
			s.log.Warn("subscription confirmation not sent", "page", d.page.Slug, "error", err)
		}
		if rerr := s.pages.RecordDelivery(ctx, d.confirm.ID, errText(err)); rerr != nil {
			s.log.Error("record delivery", "error", rerr)
		}
		return
	}
	subscribers, err := s.pages.Subscribers(ctx, d.page.ID)
	if err != nil {
		s.log.Error("read subscribers", "page", d.page.Slug, "error", err)
		return
	}
	config, mailErr := s.pageMailConfig(ctx, d.page)
	for _, sub := range subscribers {
		if !sub.Confirmed {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		var err error
		switch sub.Kind {
		case store.SubscriberEmail:
			if mailErr != nil {
				err = mailErr
			} else {
				err = s.sendIncidentMail(ctx, config, d, sub, base)
			}
		case store.SubscriberWebhook:
			err = s.sendIncidentWebhook(ctx, d, sub, base)
		}
		if err != nil {
			s.log.Warn("subscriber not sent to", "page", d.page.Slug, "subscriber", sub.ID, "error", err)
		}
		if rerr := s.pages.RecordDelivery(ctx, sub.ID, errText(err)); rerr != nil {
			s.log.Error("record delivery", "error", rerr)
		}
	}
}

// pageMailConfig is the SMTP settings that a page's emails go through.
func (s *Server) pageMailConfig(ctx context.Context, page store.StatusPage) (notify.M, error) {
	if page.SubscribeNotificationID == nil {
		return nil, errors.New("the page has no email notification to send through")
	}
	n, err := s.store.GetNotification(ctx, *page.SubscribeNotificationID)
	if err != nil {
		return nil, fmt.Errorf("read the page's email notification: %w", err)
	}
	if n.Type != "smtp" {
		return nil, errors.New("the page's notification isn't an email (SMTP) one")
	}
	return n.Config, nil
}

func (s *Server) sendConfirmation(ctx context.Context, page store.StatusPage, sub store.Subscriber, base string) error {
	config, err := s.pageMailConfig(ctx, page)
	if err != nil {
		return err
	}
	link := pageURL(base, page.Slug, url.Values{"subscription": {"confirm"}, "token": {sub.Token}})
	body := fmt.Sprintf("Someone, hopefully you, asked for updates from the %s status page at this address.\n\n"+
		"To confirm, open this link:\n%s\n\n"+
		"If it wasn't you, ignore this email. Nothing is sent unless the address is confirmed, and the request "+
		"expires in two days.\n", page.Title, link)
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return notify.SendMail(sendCtx, s.notifyEnv, config, sub.Address, "Confirm your subscription to "+page.Title, body, nil)
}

func (s *Server) sendIncidentMail(ctx context.Context, config notify.M, d delivery, sub store.Subscriber, base string) error {
	i := d.incident
	var subject, heading string
	switch d.event {
	case incidentCreated:
		subject, heading = fmt.Sprintf("[%s] %s", d.page.Title, i.Title), "New incident"
	case incidentUpdated:
		subject, heading = fmt.Sprintf("[%s] Update: %s", d.page.Title, i.Title), "Incident update"
	default:
		subject, heading = fmt.Sprintf("[%s] Resolved: %s", d.page.Title, i.Title), "Incident resolved"
	}
	unsubscribe := pageURL(base, d.page.Slug, url.Values{"subscription": {"unsubscribe"}, "token": {sub.Token}})
	var body strings.Builder
	fmt.Fprintf(&body, "%s: %s\n\n", heading, i.Title)
	if d.event == incidentResolved {
		body.WriteString("This incident is resolved. What it said:\n\n")
	}
	body.WriteString(strings.TrimSpace(i.Content))
	fmt.Fprintf(&body, "\n\nThe status page: %s\n\n", pageURL(base, d.page.Slug, nil))
	fmt.Fprintf(&body, "You get this email because you subscribed to the %s status page.\nUnsubscribe: %s\n", d.page.Title, unsubscribe)
	oneClick := strings.TrimRight(base, "/") + "/api/status-page/" + url.PathEscape(d.page.Slug) + "/unsubscribe?" +
		url.Values{"token": {sub.Token}}.Encode()
	headers := map[string]string{
		"List-Unsubscribe":      "<" + oneClick + ">",
		"List-Unsubscribe-Post": "List-Unsubscribe=One-Click",
		"Auto-Submitted":        "auto-generated",
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	return notify.SendMail(sendCtx, s.notifyEnv, config, sub.Address, subject, body.String(), headers)
}

func (s *Server) sendIncidentWebhook(ctx context.Context, d delivery, sub store.Subscriber, base string) error {
	payload, err := json.Marshal(map[string]any{
		"event": d.event,
		"statusPage": map[string]any{
			"slug": d.page.Slug, "title": d.page.Title, "url": pageURL(base, d.page.Slug, nil),
		},
		"incident": incidentJSON(*d.incident),
	})
	if err != nil {
		return err
	}
	sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(sendCtx, http.MethodPost, sub.Address, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Uptime-Kuma/"+s.opts.Version)
	client := s.notifyEnv.Client
	if client == nil {
		client = http.DefaultClient
	}
	resp, err := client.Do(req)
	if err != nil {
		// The URL can hold a token, which the error would repeat.
		var uerr *url.Error
		if errors.As(err, &uerr) {
			err = uerr.Err
		}
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("the webhook answered %s", resp.Status)
	}
	return nil
}

// notifySubscribers queues an incident for a page's subscribers, and marks it as sent to them.
func (s *Server) notifySubscribers(ctx context.Context, page store.StatusPage, incident store.Incident, event string) {
	if event != incidentResolved && !incident.Notified {
		if err := s.pages.MarkIncidentNotified(ctx, incident.ID); err != nil {
			s.log.Error("mark incident notified", "error", err)
		}
	}
	s.enqueue(delivery{page: page, incident: &incident, event: event})
}

// validEmail returns the address, if it's a plain one.
func validEmail(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxEmailLength {
		return "", false
	}
	addr, err := mail.ParseAddress(raw)
	if err != nil || addr.Name != "" || addr.Address != raw || !strings.Contains(raw[strings.LastIndexByte(raw, '@')+1:], ".") {
		return "", false
	}
	return addr.Address, true
}

func subscriberJSON(sub store.Subscriber) map[string]any {
	var lastSent any
	if sub.LastSentAt != nil {
		lastSent = sub.LastSentAt.UTC().Format(beatTimeFormat)
	}
	return map[string]any{
		"id":           sub.ID,
		"kind":         sub.Kind,
		"address":      sub.Address,
		"confirmed":    sub.Confirmed,
		"createdDate":  sub.CreatedAt.UTC().Format(beatTimeFormat),
		"lastSentDate": lastSent,
		"lastError":    sub.LastError,
	}
}

func templateJSON(t store.IncidentTemplate) map[string]any {
	return map[string]any{"id": t.ID, "title": t.Title, "content": t.Content, "style": t.Style}
}

// getStatusPageSubscribers lists a page's subscribers and its email sender, for admins.
func (s *Server) getStatusPageSubscribers(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	subscribers, err := s.pages.Subscribers(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, len(subscribers))
	for i, sub := range subscribers {
		list[i] = subscriberJSON(sub)
	}
	return ok("notificationId", page.SubscribeNotificationID, "baseURL", s.baseURL(ctx), "subscribers", list), nil
}

// setStatusPageSubscriptions sets the SMTP notification that sends a page's emails, or none.
func (s *Server) setStatusPageSubscriptions(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	var in struct {
		NotificationID *flexInt `json:"notificationId"`
	}
	if err := arg(args, 1, &in); err != nil {
		return nil, userErr(err)
	}
	var id *int32
	if in.NotificationID != nil && in.NotificationID.set {
		if in.NotificationID.value <= 0 || in.NotificationID.value > 1<<31-1 {
			return nil, userErr(errors.New("invalid id"))
		}
		n, err := s.store.GetNotification(ctx, int32(in.NotificationID.value))
		if errors.Is(err, store.ErrNotFound) || (err == nil && n.Type != "smtp") {
			return nil, userErr(errors.New("Choose an email (SMTP) notification.")) //nolint:staticcheck // Shown to users.
		}
		if err != nil {
			return nil, err
		}
		if s.baseURL(ctx) == "" {
			return nil, userErr(errors.New("Set the Primary Base URL in Settings > General first, so that emails can link to the page.")) //nolint:staticcheck // Shown to users.
		}
		id = &n.ID
	}
	if err := s.pages.SetStatusPageSubscriptions(ctx, page.ID, id); err != nil {
		return nil, err
	}
	s.cache.clear()
	return okI18n("Saved."), nil
}

// addStatusPageWebhook adds a webhook that gets the page's incidents.
func (s *Server) addStatusPageWebhook(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	var raw string
	if err := arg(args, 1, &raw); err != nil {
		return nil, userErr(err)
	}
	raw = strings.TrimSpace(raw)
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || len(raw) > maxWebhookLength {
		return nil, userErr(errors.New("Enter the webhook's http:// or https:// address.")) //nolint:staticcheck // Shown to users.
	}
	n, err := s.pages.CountSubscribers(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	if n >= maxSubscribers {
		return nil, userErr(errors.New("This page has too many subscribers.")) //nolint:staticcheck // Shown to users.
	}
	sub, err := s.pages.AddWebhookSubscriber(ctx, page.ID, raw, randomToken())
	if errors.Is(err, store.ErrSubscriberExists) {
		return nil, userErr(errors.New("This webhook is already added.")) //nolint:staticcheck // Shown to users.
	}
	if err != nil {
		return nil, err
	}
	return okI18n("successAdded", "subscriber", subscriberJSON(sub)), nil
}

func (s *Server) removeStatusPageSubscriber(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	id, err := idArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.pages.RemoveSubscriber(ctx, page.ID, id); err != nil {
		return nil, err
	}
	return okI18n("successDeleted"), nil
}

func (s *Server) getIncidentTemplates(ctx context.Context, _ []json.RawMessage) (reply, error) {
	templates, err := s.pages.IncidentTemplates(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, len(templates))
	for i, t := range templates {
		list[i] = templateJSON(t)
	}
	return ok("templates", list), nil
}

// saveIncidentTemplate saves an incident form as a template, replacing the one with its title.
func (s *Server) saveIncidentTemplate(ctx context.Context, args []json.RawMessage) (reply, error) {
	incident, _, err := parseIncident(args, 0, 0)
	if err != nil {
		return nil, userErr(err)
	}
	templates, err := s.pages.IncidentTemplates(ctx)
	if err != nil {
		return nil, err
	}
	exists := slices.ContainsFunc(templates, func(t store.IncidentTemplate) bool { return t.Title == incident.Title })
	if !exists && len(templates) >= maxIncidentTemplates {
		return nil, userErr(fmt.Errorf("there can be up to %d templates", maxIncidentTemplates))
	}
	saved, err := s.pages.SaveIncidentTemplate(ctx, store.IncidentTemplate{Title: incident.Title, Content: incident.Content, Style: incident.Style})
	if err != nil {
		return nil, err
	}
	return okI18n("Saved.", "template", templateJSON(saved)), nil
}

func (s *Server) deleteIncidentTemplate(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.pages.DeleteIncidentTemplate(ctx, id); err != nil {
		return nil, err
	}
	return okI18n("successDeleted"), nil
}

// registerSubscriptions adds the public routes that subscribe to a page, confirm and unsubscribe.
func (s *Server) registerSubscriptions(mux *http.ServeMux) {
	mux.HandleFunc("POST /api/status-page/{slug}/subscribe", s.publicSubscribe)
	mux.HandleFunc("POST /api/status-page/{slug}/confirm", s.publicConfirm)
	mux.HandleFunc("POST /api/status-page/{slug}/unsubscribe", s.publicUnsubscribe)
}

func publicFail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"ok": false, "msg": msg})
}

// subscriptionRequest reads a public route's page and JSON body.
func (s *Server) subscriptionRequest(w http.ResponseWriter, r *http.Request, body any) (*store.StatusPage, bool) {
	if body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(body); err != nil &&
			strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
			publicFail(w, http.StatusBadRequest, "invalid request")
			return nil, false
		}
	}
	page, err := s.findPage(r.Context(), pageSlug(r.PathValue("slug")))
	if err != nil {
		s.log.Error("public route", "path", r.URL.Path, "error", err)
		publicFail(w, http.StatusInternalServerError, "internal error")
		return nil, false
	}
	if page == nil {
		publicFail(w, http.StatusNotFound, "Not Found")
		return nil, false
	}
	return page, true
}

// publicSubscribe takes an email address for a page's incidents, and sends it a confirmation. It
// answers the same whether the address was subscribed already or not.
func (s *Server) publicSubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Email string `json:"email"`
	}
	page, found := s.subscriptionRequest(w, r, &in)
	if !found {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()
	if page.SubscribeNotificationID == nil || s.baseURL(ctx) == "" {
		publicFail(w, http.StatusBadRequest, "This page doesn't take subscriptions.")
		return
	}
	email, valid := validEmail(in.Email)
	if !valid {
		publicFail(w, http.StatusBadRequest, "Enter a valid email address.")
		return
	}
	if !s.subs.subscribe.allow(email) {
		publicFail(w, http.StatusTooManyRequests, "Too many requests. Try again in a few minutes.")
		return
	}
	if err := s.pages.DeleteUnconfirmedSubscribers(ctx, s.now().Add(-unconfirmedFor)); err != nil {
		s.log.Error("delete unconfirmed subscribers", "error", err)
	}
	n, err := s.pages.CountSubscribers(ctx, page.ID)
	if err != nil {
		s.log.Error("count subscribers", "error", err)
		publicFail(w, http.StatusInternalServerError, "internal error")
		return
	}
	if n >= maxSubscribers {
		publicFail(w, http.StatusServiceUnavailable, "This page can't take more subscriptions.")
		return
	}
	sub, _, err := s.pages.AddEmailSubscriber(ctx, page.ID, email, randomToken())
	if err != nil {
		s.log.Error("add subscriber", "error", err)
		publicFail(w, http.StatusInternalServerError, "internal error")
		return
	}
	if !sub.Confirmed {
		claimed, err := s.pages.ClaimConfirmation(ctx, sub.ID, confirmInterval)
		if err != nil {
			s.log.Error("claim confirmation", "error", err)
		}
		if claimed {
			s.enqueue(delivery{page: *page, confirm: &sub, event: "confirm"})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) publicConfirm(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	page, found := s.subscriptionRequest(w, r, &in)
	if !found {
		return
	}
	if in.Token == "" {
		publicFail(w, http.StatusBadRequest, "invalid request")
		return
	}
	_, err := s.pages.ConfirmSubscriber(r.Context(), page.ID, in.Token, unconfirmedFor)
	if errors.Is(err, store.ErrNotFound) {
		publicFail(w, http.StatusNotFound, "This link has expired. Subscribe again to get a new one.")
		return
	}
	if err != nil {
		s.log.Error("confirm subscriber", "error", err)
		publicFail(w, http.StatusInternalServerError, "internal error")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// publicUnsubscribe removes a subscriber by its token: from the page, which sends it in JSON, or
// from a mail program's one-click unsubscribe (RFC 8058), which posts a form to the link.
func (s *Server) publicUnsubscribe(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	var body any
	if strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		body = &in
	}
	page, found := s.subscriptionRequest(w, r, body)
	if !found {
		return
	}
	token := in.Token
	if token == "" {
		token = r.URL.Query().Get("token")
	}
	if token == "" {
		publicFail(w, http.StatusBadRequest, "invalid request")
		return
	}
	if _, err := s.pages.RemoveSubscriberByToken(r.Context(), page.ID, token); err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("unsubscribe", "error", err)
		publicFail(w, http.StatusInternalServerError, "internal error")
		return
	}
	// Already gone is the same as done.
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

// pageUpcomingMaintenance is the windows linked to a page that start within upcomingWithin, and
// aren't running now, soonest first.
func (s *Server) pageUpcomingMaintenance(ctx context.Context, pageID int32) ([]map[string]any, error) {
	ids, err := s.pages.StatusPageMaintenanceIDs(ctx, pageID)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	if len(ids) == 0 {
		return out, nil
	}
	now := s.now()
	eval := s.maintenance.Evaluator()
	type upcoming struct {
		w    maintenance.Window
		slot maintenance.Slot
	}
	var list []upcoming
	for _, w := range s.maintenance.Windows() {
		if !slices.Contains(ids, w.ID) || eval.Status(w, now) != maintenance.StatusScheduled {
			continue
		}
		if slot, found := eval.Next(w, now); found && slot.Start.After(now) && slot.Start.Sub(now) <= upcomingWithin {
			list = append(list, upcoming{w, slot})
		}
	}
	slices.SortFunc(list, func(a, b upcoming) int { return a.slot.Start.Compare(b.slot.Start) })
	for _, u := range list {
		out = append(out, map[string]any{
			"id":          u.w.ID,
			"title":       u.w.Title,
			"description": u.w.Description,
			"start":       u.slot.Start.UTC().Format(time.RFC3339),
			"end":         u.slot.End.UTC().Format(time.RFC3339),
		})
	}
	return out, nil
}
