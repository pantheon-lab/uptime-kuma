package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/badge"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// How long public answers are kept. The Node.js backend kept pages and badges for 5 minutes, and
// beats for 1; these are shorter, and changes in the UI apply at once.
const (
	pageTTL      = time.Minute
	heartbeatTTL = 30 * time.Second
	badgeTTL     = time.Minute
	routingTTL   = time.Minute
	publicBeats  = 100
)

// The colours and defaults of the Node.js backend's badges.
const (
	naColor            = "#999"
	upColor            = "#66c20a"
	warnColor          = "#eed202"
	downColor          = "#c2290a"
	pendingColor       = "#f8a306"
	maintenanceColor   = "#1747f5"
	pingColor          = "blue"
	partialColor       = "#F6BE00"
	pageMaintenance    = "#808080"
	defaultCertWarn    = 14
	defaultCertDown    = 7
	maxBadgeHours      = 720
	maxBadgeMinutes    = 1440
	maxBadgeDays       = 365
	maxAvgResponseHour = 720
)

// registerPublic adds the routes anyone may use: status pages, their data, and badges.
func (s *Server) registerPublic(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/status-page/{slug}", s.publicStatusPage)
	// One pattern, because /heartbeat/{slug} and /{slug}/badge would overlap.
	mux.HandleFunc("GET /api/status-page/{first}/{second}", s.publicStatusPageRoute)
	mux.HandleFunc("GET /api/badge/{id}/{kind}", s.monitorBadge)
	mux.HandleFunc("GET /api/badge/{id}/{kind}/{duration}", s.monitorBadge)
	mux.HandleFunc("GET /status", s.statusPageHTML)
	mux.HandleFunc("GET /status-page", s.statusPageHTML)
	mux.HandleFunc("GET /status/{slug}", s.statusPageHTML)
	mux.HandleFunc("GET /status/{slug}/rss", s.statusPageRSS)
	mux.HandleFunc("GET /upload/{file}", s.uploadedLogo)
	mux.HandleFunc("GET /{$}", s.root)
}

// serveCached answers from the cache, or from build, which it keeps for ttl.
func (s *Server) serveCached(w http.ResponseWriter, r *http.Request, key string, ttl time.Duration, build func(context.Context) (cached, error)) {
	entry, found := s.cache.get(key)
	if !found {
		ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
		defer cancel()
		var err error
		entry, err = build(ctx)
		if err != nil {
			s.log.Error("public route", "path", r.URL.Path, "error", err)
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "internal error"})
			return
		}
		s.cache.set(key, entry, ttl)
	}
	header := w.Header()
	header.Set("Content-Type", entry.contentType)
	header.Set("X-Content-Type-Options", "nosniff")
	switch {
	case entry.cacheControl != "":
		header.Set("Cache-Control", entry.cacheControl)
	case entry.status == http.StatusOK:
		header.Set("Cache-Control", fmt.Sprintf("public, max-age=%d", int(ttl.Seconds())))
	default:
		header.Set("Cache-Control", "no-store")
	}
	w.WriteHeader(entry.status)
	_, _ = w.Write(entry.body)
}

// jsonEntry is a JSON answer. Browsers must ask again each time, because the UI fetches the
// same addresses before and after an edit, and would otherwise show what it had before. The
// server's own cache still spares the database.
func jsonEntry(status int, v any) (cached, error) {
	body, err := json.Marshal(v)
	if err != nil {
		return cached{}, err
	}
	return cached{status: status, contentType: "application/json; charset=utf-8", body: body, cacheControl: "no-cache"}, nil
}

// notFoundEntry is the Node.js backend's sendHttpError answer for a page that doesn't exist.
func notFoundEntry(msg string) (cached, error) {
	return jsonEntry(http.StatusNotFound, map[string]any{"ok": false, "msg": msg})
}

func pageSlug(raw string) string { return strings.ToLower(raw) }

// findPage returns a page, or nil if there's none with the slug.
func (s *Server) findPage(ctx context.Context, slug string) (*store.StatusPage, error) {
	page, err := s.pages.StatusPageBySlug(ctx, slug)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &page, nil
}

func (s *Server) publicStatusPage(w http.ResponseWriter, r *http.Request) {
	slug := pageSlug(r.PathValue("slug"))
	s.serveCached(w, r, "page:"+slug, pageTTL, func(ctx context.Context) (cached, error) {
		page, err := s.findPage(ctx, slug)
		if err != nil || page == nil {
			if err != nil {
				return cached{}, err
			}
			return notFoundEntry("Status Page Not Found")
		}
		data, err := s.statusPageData(ctx, *page)
		if err != nil {
			return cached{}, err
		}
		return jsonEntry(http.StatusOK, data)
	})
}

func (s *Server) publicStatusPageRoute(w http.ResponseWriter, r *http.Request) {
	first, second := r.PathValue("first"), r.PathValue("second")
	switch {
	case first == "heartbeat":
		s.publicHeartbeats(w, r, pageSlug(second))
	case second == "manifest.json":
		s.publicManifest(w, r, pageSlug(first))
	case second == "incident-history":
		s.publicIncidentHistory(w, r, pageSlug(first))
	case second == "badge":
		s.statusPageBadge(w, r, pageSlug(first))
	default:
		http.NotFound(w, r)
	}
}

// publicHeartbeats is the newest beats and the 24 hour uptime of each monitor on a page, without
// their messages, which can hold details that aren't meant to be public.
func (s *Server) publicHeartbeats(w http.ResponseWriter, r *http.Request, slug string) {
	s.serveCached(w, r, "beats:"+slug, heartbeatTTL, func(ctx context.Context) (cached, error) {
		page, err := s.findPage(ctx, slug)
		if err != nil {
			return cached{}, err
		}
		heartbeats := map[string]any{}
		uptimes := map[string]any{}
		if page != nil {
			ids, err := s.pages.StatusPageMonitorIDs(ctx, page.ID)
			if err != nil {
				return cached{}, err
			}
			beats, err := s.pages.RecentBeats(ctx, ids, publicBeats)
			if err != nil {
				return cached{}, err
			}
			stats, err := s.pages.Stats(ctx)
			if err != nil {
				return cached{}, err
			}
			byID := map[int32]*float64{}
			for _, row := range stats {
				byID[row.MonitorID] = row.Uptime24h
			}
			for _, id := range ids {
				list := make([]map[string]any, len(beats[id]))
				for i, b := range beats[id] {
					list[i] = map[string]any{"status": b.Status, "time": b.Time.UTC().Format(beatTimeFormat), "msg": "", "ping": b.Ping}
				}
				key := strconv.Itoa(int(id))
				heartbeats[key] = list
				if uptime := byID[id]; uptime != nil {
					uptimes[key+"_24"] = *uptime
				}
			}
		}
		return jsonEntry(http.StatusOK, map[string]any{"heartbeatList": heartbeats, "uptimeList": uptimes})
	})
}

func (s *Server) publicManifest(w http.ResponseWriter, r *http.Request, slug string) {
	s.serveCached(w, r, "manifest:"+slug, pageTTL, func(ctx context.Context) (cached, error) {
		page, err := s.findPage(ctx, slug)
		if err != nil || page == nil {
			if err != nil {
				return cached{}, err
			}
			return notFoundEntry("Not Found")
		}
		icon := page.Icon
		if icon == "" {
			icon = defaultIcon
		}
		return jsonEntry(http.StatusOK, map[string]any{
			"name":      page.Title,
			"start_url": "/status/" + page.Slug,
			"display":   "standalone",
			"icons":     []any{map[string]any{"src": icon, "sizes": "128x128", "type": "image/png"}},
		})
	})
}

func (s *Server) publicIncidentHistory(w http.ResponseWriter, r *http.Request, slug string) {
	cursor := r.URL.Query().Get("cursor")
	s.serveCached(w, r, "incidents:"+slug+"\x00"+cursor, pageTTL, func(ctx context.Context) (cached, error) {
		page, err := s.findPage(ctx, slug)
		if err != nil || page == nil {
			if err != nil {
				return cached{}, err
			}
			return notFoundEntry("Status Page Not Found")
		}
		history, err := s.incidentHistory(ctx, page.ID, cursor)
		if isUserError(err) {
			return jsonEntry(http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
		}
		if err != nil {
			return cached{}, err
		}
		history["ok"] = true
		return jsonEntry(http.StatusOK, history)
	})
}

// badgeEntry draws a badge, or answers 400 for a style badge-maker doesn't have.
func badgeEntry(b badge.Badge) (cached, error) {
	svg, err := badge.Render(b)
	if err != nil {
		return jsonEntry(http.StatusBadRequest, map[string]any{"ok": false, "msg": err.Error()})
	}
	return cached{status: http.StatusOK, contentType: "image/svg+xml; charset=utf-8", body: []byte(svg)}, nil
}

func badgeError(msg string) (cached, error) {
	return jsonEntry(http.StatusBadRequest, map[string]any{"ok": false, "msg": msg})
}

// serveBadge answers a badge route. Anyone may embed badges, so any origin may read them.
func (s *Server) serveBadge(w http.ResponseWriter, r *http.Request, build func(context.Context) (cached, error)) {
	w.Header().Set("Access-Control-Allow-Origin", "*")
	s.serveCached(w, r, "badge:"+r.URL.Path+"?"+r.URL.RawQuery, badgeTTL, build)
}

// statusPageBadge is the overall state of a page's monitors.
func (s *Server) statusPageBadge(w http.ResponseWriter, r *http.Request, slug string) {
	q := r.URL.Query()
	s.serveBadge(w, r, func(ctx context.Context) (cached, error) {
		var hasUp, hasDown, hasMaintenance bool
		page, err := s.findPage(ctx, slug)
		if err != nil {
			return cached{}, err
		}
		if page != nil {
			ids, err := s.pages.StatusPageMonitorIDs(ctx, page.ID)
			if err != nil {
				return cached{}, err
			}
			beats, err := s.pages.RecentBeats(ctx, ids, 1)
			if err != nil {
				return cached{}, err
			}
			for _, list := range beats {
				switch list[len(list)-1].Status {
				case monitor.StatusMaintenance:
					hasMaintenance = true
				case monitor.StatusPending:
				case monitor.StatusUp:
					hasUp = true
				default:
					hasDown = true
				}
			}
		}
		b := badge.Badge{Style: queryOr(q, "style", "flat"), Label: q.Get("label")}
		switch {
		case !hasUp && !hasDown && !hasMaintenance:
			b.Label, b.Message, b.Color = "", "N/A", naColor
		case hasMaintenance:
			b.Message, b.Color = "Maintenance", queryOr(q, "maintenanceColor", pageMaintenance)
		case hasUp && !hasDown:
			b.Message, b.Color = "Up", queryOr(q, "upColor", upColor)
		case hasUp && hasDown:
			b.Message, b.Color = "Degraded", queryOr(q, "partialColor", partialColor)
		default:
			b.Message, b.Color = "Down", queryOr(q, "downColor", downColor)
		}
		return badgeEntry(b)
	})
}

// queryOr is a query parameter, or def if it's absent. A parameter that's present but empty is
// used as it is, as JavaScript's default values do.
func queryOr(q url.Values, key, def string) string {
	if values, found := q[key]; found && len(values) > 0 {
		return values[0]
	}
	return def
}

// join is the Node.js backend's filterAndJoin: the parts that aren't empty, together.
func join(parts ...string) string {
	return strings.Join(parts, "")
}

var badgeDurationFormat = regexp.MustCompile(`^([0-9]+)([mhdwMy])$`)

// parseBadgeDuration is the Node.js backend's getDataByDuration: a number of minutes, hours,
// days, weeks, months of 30 days or years of 365 days, within its limits.
func parseBadgeDuration(raw string) (unit string, n int, err error) {
	m := badgeDurationFormat.FindStringSubmatch(raw)
	if m == nil {
		return "", 0, fmt.Errorf("Invalid duration: %s", raw) //nolint:staticcheck // Shown to users, as in Node.js.
	}
	n, err = strconv.Atoi(m[1])
	if err != nil {
		return "", 0, fmt.Errorf("Invalid duration: %s", raw) //nolint:staticcheck // Shown to users, as in Node.js.
	}
	switch m[2] {
	case "m":
		unit = "minute"
	case "h":
		unit = "hour"
	case "d":
		unit = "day"
	case "w":
		unit, n = "day", n*7
	case "M":
		unit, n = "day", n*30
	case "y":
		unit, n = "day", n*365
	}
	switch {
	case n < 1:
		return "", 0, fmt.Errorf("Invalid duration: %s", raw) //nolint:staticcheck // Shown to users, as in Node.js.
	case unit == "hour" && n > maxBadgeHours:
		return "", 0, errors.New("The maximum number of hours is 720") //nolint:staticcheck // Shown to users, as in Node.js.
	case unit == "minute" && n > maxBadgeMinutes:
		return "", 0, errors.New("The maximum number of minutes is 1440") //nolint:staticcheck // Shown to users, as in Node.js.
	case unit == "day" && n > maxBadgeDays:
		return "", 0, errors.New("The maximum number of days is 365") //nolint:staticcheck // Shown to users, as in Node.js.
	}
	return unit, n, nil
}

// toPrecision4 is JavaScript's Number.prototype.toPrecision(4), which rounds an exact tie up,
// where strconv would round it to even.
func toPrecision4(v float64) string {
	const digits = 4
	if v == 0 {
		return "0.000"
	}
	sign := ""
	if v < 0 {
		sign, v = "-", -v
	}
	// The float's exact value in decimal, as digits and the position of the decimal point.
	exact := new(big.Float).SetFloat64(v).Text('e', 1100)
	mantissa, exp, _ := strings.Cut(exact, "e")
	e, _ := strconv.Atoi(exp)
	all := strings.Replace(mantissa, ".", "", 1)
	kept := []byte(all[:digits])
	if all[digits] >= '5' {
		i := digits - 1
		for ; i >= 0 && kept[i] == '9'; i-- {
			kept[i] = '0'
		}
		if i < 0 {
			kept = append([]byte{'1'}, kept[:digits-1]...)
			e++
		} else {
			kept[i]++
		}
	}
	text := string(kept)
	switch {
	case e < -6 || e >= digits:
		out := text[:1]
		if digits > 1 {
			out += "." + text[1:]
		}
		expSign := "+"
		if e < 0 {
			expSign, e = "-", -e
		}
		return sign + out + "e" + expSign + strconv.Itoa(e)
	case e < 0:
		return sign + "0." + strings.Repeat("0", -e-1) + text
	case e == digits-1:
		return sign + text
	default:
		return sign + text[:e+1] + "." + text[e+1:]
	}
}

// monitorBadge draws the badges of a monitor that's on a status page. Any other monitor gets an
// N/A badge, so that a badge says nothing about monitors that aren't public.
func (s *Server) monitorBadge(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	kind, duration := r.PathValue("kind"), r.PathValue("duration")
	if duration != "" && kind != "uptime" && kind != "ping" && kind != "avg-response" {
		http.NotFound(w, r)
		return
	}
	switch kind {
	case "status", "uptime", "ping", "avg-response", "cert-exp", "response":
	default:
		http.NotFound(w, r)
		return
	}
	s.serveBadge(w, r, func(ctx context.Context) (cached, error) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
		if err != nil {
			return badgeError("Invalid monitor ID")
		}
		monitorID := int32(id)
		style := queryOr(q, "style", "flat")
		public, err := s.pages.IsMonitorPublic(ctx, monitorID)
		if err != nil {
			return cached{}, err
		}
		notAvailable := badge.Badge{Style: style, Message: "N/A", Color: naColor}

		switch kind {
		case "status":
			if !public {
				return badgeEntry(notAvailable)
			}
			return s.statusBadge(ctx, monitorID, q, style)
		case "uptime", "ping":
			if !public {
				return badgeEntry(notAvailable)
			}
			if duration == "" {
				duration = "24h"
			}
			if _, err := strconv.Atoi(duration); err == nil {
				duration += "h"
			}
			unit, n, err := parseBadgeDuration(duration)
			if err != nil {
				return badgeError(err.Error())
			}
			totals, err := s.pages.UptimeOver(ctx, monitorID, unit, n)
			if err != nil {
				return cached{}, err
			}
			label := duration[:len(duration)-1]
			if kind == "uptime" {
				return uptimeBadge(q, style, label, totals)
			}
			return pingBadge(q, style, label, totals)
		case "avg-response":
			if !public {
				return badgeEntry(notAvailable)
			}
			hours := 24
			if duration != "" {
				v, err := strconv.Atoi(leadingDigits(duration))
				if err != nil {
					return badgeError("Invalid duration: " + duration)
				}
				hours = min(v, maxAvgResponseHour)
			}
			if hours < 1 {
				return badgeError("Invalid duration: " + duration)
			}
			totals, err := s.pages.UptimeOver(ctx, monitorID, "hour", hours)
			if err != nil {
				return cached{}, err
			}
			avg := averagePing(totals)
			if avg == nil || *avg == 0 {
				return badgeEntry(notAvailable)
			}
			value := truncatedValue(q, *avg)
			return badgeEntry(badge.Badge{
				Style: style, Color: queryOr(q, "color", pingColor), LabelColor: q.Get("labelColor"),
				Label:   join(q.Get("labelPrefix"), queryOr(q, "label", fmt.Sprintf("Avg. Response (%dh)", hours)), q.Get("labelSuffix")),
				Message: join(q.Get("prefix"), value, queryOr(q, "suffix", "ms")),
			})
		case "cert-exp":
			if !public {
				return badgeEntry(notAvailable)
			}
			return s.certBadge(ctx, monitorID, q, style)
		default: // response
			if !public {
				return badgeEntry(notAvailable)
			}
			beats, err := s.pages.LatestBeats(ctx, monitorID, 1)
			if err != nil {
				return cached{}, err
			}
			if len(beats) == 0 || beats[0].Ping == nil || *beats[0].Ping == 0 {
				return badgeEntry(notAvailable)
			}
			return badgeEntry(badge.Badge{
				Style: style, Color: queryOr(q, "color", pingColor), LabelColor: q.Get("labelColor"),
				Label:   join(q.Get("labelPrefix"), queryOr(q, "label", "Response"), q.Get("labelSuffix")),
				Message: join(q.Get("prefix"), truncatedValue(q, *beats[0].Ping), queryOr(q, "suffix", "ms")),
			})
		}
	})
}

// leadingDigits is what JavaScript's parseInt reads.
func leadingDigits(s string) string {
	end := 0
	for end < len(s) && s[end] >= '0' && s[end] <= '9' {
		end++
	}
	return s[:end]
}

// overrideValue is the value query parameter, which the badge generator uses for previews.
func overrideValue(q url.Values) (float64, bool) {
	raw := queryOr(q, "value", "")
	if raw == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(raw, 64)
	return v, err == nil
}

// truncatedValue is parseInt of a number, as the Node.js backend shows pings.
func truncatedValue(q url.Values, v float64) string {
	if override, found := overrideValue(q); found {
		v = override
	}
	return strconv.FormatInt(int64(math.Trunc(v)), 10)
}

func averagePing(t store.UptimeTotals) *float64 {
	if t.PingCount == 0 {
		return nil
	}
	avg := t.PingSum / float64(t.PingCount)
	return &avg
}

func (s *Server) statusBadge(ctx context.Context, monitorID int32, q url.Values, style string) (cached, error) {
	beats, err := s.pages.LatestBeats(ctx, monitorID, 1)
	if err != nil {
		return cached{}, err
	}
	state := int64(-1)
	if len(beats) > 0 {
		state = int64(beats[0].Status)
	}
	if raw := queryOr(q, "value", ""); raw != "" {
		if v, err := strconv.ParseInt(leadingDigits(raw), 10, 64); err == nil {
			state = v
		}
	}
	b := badge.Badge{Style: style, Label: queryOr(q, "label", "Status")}
	switch state {
	case int64(monitor.StatusDown):
		b.Color, b.Message = queryOr(q, "downColor", downColor), queryOr(q, "downLabel", "Down")
	case int64(monitor.StatusUp):
		b.Color, b.Message = queryOr(q, "upColor", upColor), queryOr(q, "upLabel", "Up")
	case int64(monitor.StatusPending):
		b.Color, b.Message = queryOr(q, "pendingColor", pendingColor), queryOr(q, "pendingLabel", "Pending")
	case int64(monitor.StatusMaintenance):
		b.Color, b.Message = queryOr(q, "maintenanceColor", maintenanceColor), queryOr(q, "maintenanceLabel", "Maintenance")
	default:
		b.Color, b.Message = naColor, "N/A"
	}
	return badgeEntry(b)
}

func uptimeBadge(q url.Values, style, durationLabel string, totals store.UptimeTotals) (cached, error) {
	uptime := 0.0
	if totals.Up+totals.Down > 0 {
		uptime = float64(totals.Up) / float64(totals.Up+totals.Down)
	}
	if override, found := overrideValue(q); found {
		uptime = override
	}
	return badgeEntry(badge.Badge{
		Style:      style,
		Color:      queryOr(q, "color", badge.PercentageToColor(uptime)),
		LabelColor: q.Get("labelColor"),
		Label:      join(q.Get("labelPrefix"), queryOr(q, "label", "Uptime ("+durationLabel+queryOr(q, "labelSuffix", "h")+")")),
		Message:    join(q.Get("prefix"), toPrecision4(uptime*100), queryOr(q, "suffix", "%")),
	})
}

func pingBadge(q url.Values, style, durationLabel string, totals store.UptimeTotals) (cached, error) {
	avg := averagePing(totals)
	_, overridden := overrideValue(q)
	if avg == nil && !overridden {
		return badgeEntry(badge.Badge{Style: style, Message: "N/A", Color: naColor})
	}
	value := 0.0
	if avg != nil {
		value = *avg
	}
	return badgeEntry(badge.Badge{
		Style:      style,
		Color:      queryOr(q, "color", pingColor),
		LabelColor: q.Get("labelColor"),
		Label:      join(q.Get("labelPrefix"), queryOr(q, "label", "Avg. Ping ("+durationLabel+queryOr(q, "labelSuffix", "h")+")")),
		Message:    join(q.Get("prefix"), truncatedValue(q, value), queryOr(q, "suffix", "ms")),
	})
}

func (s *Server) certBadge(ctx context.Context, monitorID int32, q url.Values, style string) (cached, error) {
	certificates, err := s.pages.AllTLSInfo(ctx)
	if err != nil {
		return cached{}, err
	}
	info, found := certificates[monitorID]
	var summary tlsSummary
	if !found || json.Unmarshal([]byte(info), &summary) != nil {
		return badgeEntry(badge.Badge{Style: style, Message: "No/Bad Cert", Color: naColor})
	}
	if !summary.Valid || summary.CertInfo == nil {
		return badgeEntry(badge.Badge{Style: style, Message: "Bad Cert", Color: queryOr(q, "downColor", downColor)})
	}
	days := float64(summary.CertInfo.DaysRemaining)
	if override, found := overrideValue(q); found {
		days = math.Trunc(override)
	}
	warnDays, downDays := float64(defaultCertWarn), float64(defaultCertDown)
	if v, err := strconv.ParseFloat(q.Get("warnDays"), 64); err == nil {
		warnDays = v
	}
	if v, err := strconv.ParseFloat(q.Get("downDays"), 64); err == nil {
		downDays = v
	}
	color := queryOr(q, "downColor", downColor)
	switch {
	case days > warnDays:
		color = queryOr(q, "upColor", upColor)
	case days > downDays:
		color = queryOr(q, "warnColor", warnColor)
	}
	_, byDate := q["date"]
	value := strconv.FormatFloat(days, 'f', -1, 64)
	suffix := queryOr(q, "suffix", " days")
	if byDate && q.Get("date") != "" {
		var validTo string
		if json.Unmarshal(summary.CertInfo.ValidTo, &validTo) == nil {
			value = validTo
		}
		suffix = queryOr(q, "suffix", "")
	}
	return badgeEntry(badge.Badge{
		Style: style, Color: color, LabelColor: q.Get("labelColor"),
		Label:   join(q.Get("labelPrefix"), queryOr(q, "label", "Cert Exp."), q.Get("labelSuffix")),
		Message: join(q.Get("prefix"), value, suffix),
	})
}

// routing is what decides where / leads: the custom domains, and whether to trust a proxy's
// X-Forwarded-Host.
type routing struct {
	domains    map[string]string
	trustProxy bool
	entryPage  string
}

func (s *Server) routing(ctx context.Context) (routing, error) {
	if cachedRouting, found := s.cache.getRouting(); found {
		return cachedRouting, nil
	}
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return routing{}, err
	}
	out := routing{entryPage: "dashboard", domains: map[string]string{}}
	_ = json.Unmarshal(settings["trustProxy"], &out.trustProxy)
	var entry string
	if json.Unmarshal(settings[entryPageKey], &entry) == nil && entry != "" {
		out.entryPage = entry
	}
	if s.pages != nil {
		if out.domains, err = s.pages.StatusPageDomains(ctx); err != nil {
			return routing{}, err
		}
	}
	s.cache.setRouting(out, routingTTL)
	return out, nil
}

// requestHost is the host the browser asked for, without a port: the proxy's X-Forwarded-Host
// when proxies are trusted.
func requestHost(r *http.Request, trustProxy bool) string {
	host := r.Host
	if trustProxy {
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			host, _, _ = strings.Cut(forwarded, ",")
		}
	}
	host = strings.TrimSpace(host)
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	return strings.ToLower(strings.Trim(host, "[]"))
}

// requestOrigin is the scheme and host the browser used, for links in the RSS feed.
func requestOrigin(r *http.Request, trustProxy bool) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	host := r.Host
	if trustProxy {
		if proto := r.Header.Get("X-Forwarded-Proto"); proto != "" {
			scheme, _, _ = strings.Cut(proto, ",")
			scheme = strings.TrimSpace(scheme)
		}
		if forwarded := r.Header.Get("X-Forwarded-Host"); forwarded != "" {
			host, _, _ = strings.Cut(forwarded, ",")
			host = strings.TrimSpace(host)
		}
	}
	if scheme != "http" && scheme != "https" {
		scheme = "http"
	}
	return scheme + "://" + host
}

// root sends a custom domain's visitors to its status page, and everyone else to the entry
// page, as the Node.js backend did.
func (s *Server) root(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()
	route, err := s.routing(ctx)
	if err != nil {
		s.log.Error("read routing", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if slug, found := route.domains[requestHost(r, route.trustProxy)]; found && s.pages != nil {
		s.renderStatusPage(w, r, slug)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	if slug, found := strings.CutPrefix(route.entryPage, statusPagePrefix); found && s.pages != nil {
		http.Redirect(w, r, "/status/"+slug, http.StatusFound)
		return
	}
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func (s *Server) statusPageHTML(w http.ResponseWriter, r *http.Request) {
	slug := pageSlug(r.PathValue("slug"))
	if slug == "" || slug == "index.html" {
		slug = "default"
	}
	s.renderStatusPage(w, r, slug)
}

var logoFile = regexp.MustCompile(`^logo([0-9]{1,10})\.png$`)

// uploadedLogo serves a status page's logo from the database. The Node.js backend kept them in
// its data directory.
func (s *Server) uploadedLogo(w http.ResponseWriter, r *http.Request) {
	m := logoFile.FindStringSubmatch(r.PathValue("file"))
	if m == nil {
		http.NotFound(w, r)
		return
	}
	id, err := strconv.ParseInt(m[1], 10, 32)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	image, updated, err := s.pages.StatusPageLogo(r.Context(), int32(id))
	if errors.Is(err, store.ErrNotFound) {
		http.NotFound(w, r)
		return
	}
	if err != nil {
		s.log.Error("read logo", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	header := w.Header()
	header.Set("Content-Type", "image/png")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Content-Security-Policy", "default-src 'none'")
	header.Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "logo.png", updated, strings.NewReader(string(image)))
}
