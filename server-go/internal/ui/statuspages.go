package ui

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// StatusPageStore is the database access status pages need. It's separate from Store, so that a
// server can run without status pages, which then answer "not supported".
type StatusPageStore interface {
	ListStatusPages(ctx context.Context) ([]store.StatusPage, error)
	StatusPageBySlug(ctx context.Context, slug string) (store.StatusPage, error)
	CreateStatusPage(ctx context.Context, title, slug string) (store.StatusPage, error)
	SaveStatusPage(ctx context.Context, slug string, page store.StatusPage, groups []store.StatusPageGroup) ([]store.StatusPageGroup, error)
	DeleteStatusPage(ctx context.Context, slug string) error
	StatusPageDomains(ctx context.Context) (map[string]string, error)
	SetStatusPageLogo(ctx context.Context, pageID int32, image []byte) error
	DeleteStatusPageLogo(ctx context.Context, pageID int32) error
	StatusPageLogo(ctx context.Context, pageID int32) ([]byte, time.Time, error)
	StatusPageGroups(ctx context.Context, pageID int32) ([]store.StatusPageGroup, error)
	StatusPageMonitorIDs(ctx context.Context, pageID int32) ([]int32, error)
	IsMonitorPublic(ctx context.Context, monitorID int32) (bool, error)

	PinnedIncidents(ctx context.Context, pageID int32) ([]store.Incident, error)
	IncidentHistory(ctx context.Context, pageID int32, cursor *store.IncidentCursor, limit int) ([]store.Incident, int64, bool, error)
	PostIncident(ctx context.Context, i store.Incident) (store.Incident, error)
	EditIncident(ctx context.Context, i store.Incident) (store.Incident, error)
	ResolveIncident(ctx context.Context, pageID, id int32) (store.Incident, error)
	DeleteIncident(ctx context.Context, pageID, id int32) error
	UnpinIncidents(ctx context.Context, pageID int32) error
	MarkIncidentNotified(ctx context.Context, id int32) error

	Subscribers(ctx context.Context, pageID int32) ([]store.Subscriber, error)
	CountSubscribers(ctx context.Context, pageID int32) (int, error)
	AddEmailSubscriber(ctx context.Context, pageID int32, email, token string) (store.Subscriber, bool, error)
	AddWebhookSubscriber(ctx context.Context, pageID int32, url, token string) (store.Subscriber, error)
	ClaimConfirmation(ctx context.Context, id int32, interval time.Duration) (bool, error)
	ConfirmSubscriber(ctx context.Context, pageID int32, token string, maxAge time.Duration) (store.Subscriber, error)
	RemoveSubscriberByToken(ctx context.Context, pageID int32, token string) (store.Subscriber, error)
	RemoveSubscriber(ctx context.Context, pageID, id int32) error
	DeleteUnconfirmedSubscribers(ctx context.Context, before time.Time) error
	RecordDelivery(ctx context.Context, id int32, sendErr string) error
	SetStatusPageSubscriptions(ctx context.Context, pageID int32, notificationID *int32) error
	IncidentTemplates(ctx context.Context) ([]store.IncidentTemplate, error)
	SaveIncidentTemplate(ctx context.Context, t store.IncidentTemplate) (store.IncidentTemplate, error)
	DeleteIncidentTemplate(ctx context.Context, id int32) error

	SetMaintenanceStatusPages(ctx context.Context, maintenanceID int32, pageIDs []int32) error
	MaintenanceStatusPages(ctx context.Context, maintenanceID int32) ([]store.StatusPage, error)
	StatusPageMaintenanceIDs(ctx context.Context, pageID int32) ([]int32, error)

	RecentBeats(ctx context.Context, monitorIDs []int32, limit int) (map[int32][]store.Beat, error)
	LatestBeats(ctx context.Context, monitorID int32, limit int) ([]store.Beat, error)
	UptimeOver(ctx context.Context, monitorID int32, unit string, n int) (store.UptimeTotals, error)
	Stats(ctx context.Context) ([]store.MonitorStats, error)
	AllMonitorTags(ctx context.Context) (map[int32][]store.MonitorTag, error)
	AllTLSInfo(ctx context.Context) (map[int32]string, error)
}

const (
	// incidentPageSize is the Node.js backend's INCIDENT_PAGE_SIZE.
	incidentPageSize = 10
	maxLogoBytes     = 2 << 20
	maxCustomCSS     = 1 << 20
	maxTextField     = 64 << 10
	maxDomains       = 50
	maxGroups        = 200
	maxGroupMonitors = 5000
	defaultIcon      = "/icon.svg"
	pngDataURLPrefix = "data:image/png;base64,"
	entryPageKey     = "entryPage"
	statusPagePrefix = "statusPage-"
)

var (
	// slugFormat is the Node.js backend's: letters and digits in words joined by dashes.
	slugFormat     = regexp.MustCompile(`^[A-Za-z0-9]+(?:-[A-Za-z0-9]+)*$`)
	domainFormat   = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)*$`)
	incidentStyles = []string{"info", "warning", "danger", "primary", "light", "dark"}
	themes         = []string{"auto", "light", "dark"}
	analyticsTypes = []string{"google", "umami", "plausible", "matomo", "rybbit"}
	pngSignature   = []byte("\x89PNG\r\n\x1a\n")
)

var errNoStatusPage = errors.New("slug is not found")

// statusPageEvents are the realtime events of status pages, for logged-in users.
var statusPageEvents = map[string]func(*Server, context.Context, []json.RawMessage) (reply, error){
	"addStatusPage":      (*Server).addStatusPage,
	"getStatusPage":      (*Server).getStatusPage,
	"saveStatusPage":     (*Server).saveStatusPage,
	"deleteStatusPage":   (*Server).deleteStatusPage,
	"postIncident":       (*Server).postIncident,
	"editIncident":       (*Server).editIncident,
	"resolveIncident":    (*Server).resolveIncident,
	"deleteIncident":     (*Server).deleteIncident,
	"unpinIncident":      (*Server).unpinIncident,
	"getIncidentHistory": (*Server).getIncidentHistory,

	"getIncidentTemplates":       (*Server).getIncidentTemplates,
	"saveIncidentTemplate":       (*Server).saveIncidentTemplate,
	"deleteIncidentTemplate":     (*Server).deleteIncidentTemplate,
	"getStatusPageSubscribers":   (*Server).getStatusPageSubscribers,
	"setStatusPageSubscriptions": (*Server).setStatusPageSubscriptions,
	"addStatusPageWebhook":       (*Server).addStatusPageWebhook,
	"removeStatusPageSubscriber": (*Server).removeStatusPageSubscriber,
}

func checkSlug(slug string) error {
	if slug == "" {
		return errors.New("Slug cannot be empty") //nolint:staticcheck // Shown to users, as in Node.js.
	}
	if len(slug) > 255 || !slugFormat.MatchString(slug) {
		return errors.New("Invalid Slug") //nolint:staticcheck // Shown to users, as in Node.js.
	}
	return nil
}

// statusPageJSON is a page's settings as the editor reads them. public leaves out what only
// the editor needs.
func statusPageJSON(p store.StatusPage, public bool) map[string]any {
	icon := p.Icon
	if icon == "" {
		icon = defaultIcon
	}
	out := map[string]any{
		"slug":                  p.Slug,
		"title":                 p.Title,
		"description":           p.Description,
		"icon":                  icon,
		"autoRefreshInterval":   p.AutoRefreshInterval,
		"theme":                 p.Theme,
		"published":             true,
		"showTags":              p.ShowTags,
		"customCSS":             p.CustomCSS,
		"footerText":            p.FooterText,
		"showPoweredBy":         p.ShowPoweredBy,
		"analyticsId":           p.AnalyticsID,
		"analyticsScriptUrl":    p.AnalyticsScriptURL,
		"analyticsType":         p.AnalyticsType,
		"showCertificateExpiry": p.ShowCertificateExpiry,
		"showOnlyLastHeartbeat": p.ShowOnlyLastHeartbeat,
		"rssTitle":              p.RSSTitle,
		// Whether people can subscribe to the page by email.
		"subscribable": p.SubscribeNotificationID != nil,
	}
	if !public {
		domains := p.Domains
		if domains == nil {
			domains = []string{}
		}
		out["id"] = p.ID
		out["domainNameList"] = domains
	}
	return out
}

func incidentJSON(i store.Incident) map[string]any {
	var updated any
	if i.UpdatedAt != nil {
		updated = i.UpdatedAt.UTC().Format(beatTimeFormat)
	}
	return map[string]any{
		"id":              i.ID,
		"style":           i.Style,
		"title":           i.Title,
		"content":         i.Content,
		"pin":             i.Pin,
		"active":          i.Active,
		"createdDate":     i.CreatedAt.UTC().Format(beatTimeFormat),
		"lastUpdatedDate": updated,
		"status_page_id":  i.StatusPageID,
		"notified":        i.Notified,
	}
}

func incidentsJSON(incidents []store.Incident) []map[string]any {
	out := make([]map[string]any, len(incidents))
	for i, incident := range incidents {
		out[i] = incidentJSON(incident)
	}
	return out
}

// encodeCursor makes the opaque cursor of incident history, which the UI sends back as it is.
func encodeCursor(i store.Incident) string {
	return i.CreatedAt.UTC().Format(time.RFC3339Nano) + "_" + strconv.Itoa(int(i.ID))
}

func decodeCursor(raw string) (*store.IncidentCursor, error) {
	if raw == "" {
		return nil, nil
	}
	at, id, found := strings.Cut(raw, "_")
	if !found {
		return nil, errors.New("invalid cursor")
	}
	created, err := time.Parse(time.RFC3339Nano, at)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	n, err := strconv.ParseInt(id, 10, 32)
	if err != nil {
		return nil, errors.New("invalid cursor")
	}
	return &store.IncidentCursor{CreatedAt: created, ID: int32(n)}, nil
}

func (s *Server) incidentHistory(ctx context.Context, pageID int32, rawCursor string) (map[string]any, error) {
	cursor, err := decodeCursor(rawCursor)
	if err != nil {
		return nil, userErr(err)
	}
	incidents, total, more, err := s.pages.IncidentHistory(ctx, pageID, cursor, incidentPageSize)
	if err != nil {
		return nil, err
	}
	var next any
	if more && len(incidents) > 0 {
		next = encodeCursor(incidents[len(incidents)-1])
	}
	return map[string]any{"incidents": incidentsJSON(incidents), "total": total, "nextCursor": next, "hasMore": more}, nil
}

// publicMonitorURL is the address a page links a monitor to. Credentials in it are left out.
func publicMonitorURL(m store.StatusPageMonitor) string {
	raw := m.URL
	if m.CustomURL != nil {
		raw = *m.CustomURL
	}
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		u.User = nil
		return u.String()
	}
	return raw
}

// groupsJSON is a page's groups as the page reads them.
func (s *Server) groupsJSON(ctx context.Context, page store.StatusPage, groups []store.StatusPageGroup) ([]map[string]any, error) {
	var tags map[int32][]store.MonitorTag
	var certificates map[int32]string
	var err error
	if page.ShowTags {
		if tags, err = s.pages.AllMonitorTags(ctx); err != nil {
			return nil, err
		}
	}
	if page.ShowCertificateExpiry {
		if certificates, err = s.pages.AllTLSInfo(ctx); err != nil {
			return nil, err
		}
	}
	out := make([]map[string]any, len(groups))
	for i, g := range groups {
		monitors := make([]map[string]any, len(g.Monitors))
		for j, m := range g.Monitors {
			item := map[string]any{"id": m.ID, "name": m.Name, "sendUrl": m.SendURL, "type": m.Type}
			if m.SendURL {
				item["url"] = publicMonitorURL(m)
			}
			if page.ShowTags {
				list := tags[m.ID]
				if list == nil {
					list = []store.MonitorTag{}
				}
				item["tags"] = list
			}
			if page.ShowCertificateExpiry {
				days, valid := certExpiry(certificates[m.ID])
				item["certExpiryDaysRemaining"], item["validCert"] = days, valid
			}
			monitors[j] = item
		}
		out[i] = map[string]any{"id": g.ID, "name": g.Name, "weight": i + 1, "monitorList": monitors}
	}
	return out, nil
}

// tlsSummary is the part of a stored certificate chain that pages and badges read.
type tlsSummary struct {
	Valid    bool `json:"valid"`
	CertInfo *struct {
		DaysRemaining int             `json:"daysRemaining"`
		ValidTo       json.RawMessage `json:"validTo"`
	} `json:"certInfo"`
}

// certExpiry is the Node.js backend's getCertExpiry: the days left, or "" and false.
func certExpiry(info string) (any, bool) {
	var summary tlsSummary
	if info == "" || json.Unmarshal([]byte(info), &summary) != nil {
		return "", false
	}
	if summary.Valid && summary.CertInfo != nil && summary.CertInfo.DaysRemaining != 0 {
		return summary.CertInfo.DaysRemaining, true
	}
	return "", false
}

// pageMaintenanceList is the windows linked to a page that are running now.
func (s *Server) pageMaintenanceList(ctx context.Context, pageID int32) ([]map[string]any, error) {
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
	for _, w := range s.maintenance.Windows() {
		if slices.Contains(ids, w.ID) && eval.Status(w, now) == maintenance.StatusUnderMaintenance {
			out = append(out, s.windowJSON(w, now))
		}
	}
	return out, nil
}

// statusPageData is everything a page shows, apart from beats: what /api/status-page/:slug
// answers, and what the page's HTML carries.
func (s *Server) statusPageData(ctx context.Context, page store.StatusPage) (map[string]any, error) {
	incidents, err := s.pages.PinnedIncidents(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	groups, err := s.pages.StatusPageGroups(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	groupList, err := s.groupsJSON(ctx, page, groups)
	if err != nil {
		return nil, err
	}
	windows, err := s.pageMaintenanceList(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	upcoming, err := s.pageUpcomingMaintenance(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"config":                  statusPageJSON(page, true),
		"incidents":               incidentsJSON(incidents),
		"publicGroupList":         groupList,
		"maintenanceList":         windows,
		"upcomingMaintenanceList": upcoming,
	}, nil
}

func (s *Server) statusPageList(ctx context.Context) (map[string]any, error) {
	pages, err := s.pages.ListStatusPages(ctx)
	if err != nil {
		return nil, err
	}
	out := make(map[string]any, len(pages))
	for _, p := range pages {
		out[strconv.Itoa(int(p.ID))] = statusPageJSON(p, false)
	}
	return out, nil
}

// statusPagesChanged clears cached public answers, and sends the list to open pages.
func (s *Server) statusPagesChanged(ctx context.Context) {
	s.cache.clear()
	list, err := s.statusPageList(ctx)
	if err != nil {
		s.log.Error("send status page list", "error", err)
		return
	}
	s.broadcast(encodeMessage("statusPageList", list))
}

// pageBySlug finds the page an event names, which the UI sends as its first argument.
func (s *Server) pageBySlug(ctx context.Context, args []json.RawMessage) (store.StatusPage, error) {
	var slug string
	if err := arg(args, 0, &slug); err != nil {
		return store.StatusPage{}, userErr(err)
	}
	page, err := s.pages.StatusPageBySlug(ctx, strings.ToLower(strings.TrimSpace(slug)))
	if errors.Is(err, store.ErrNotFound) {
		return page, userErr(errNoStatusPage)
	}
	return page, err
}

func (s *Server) addStatusPage(ctx context.Context, args []json.RawMessage) (reply, error) {
	var title, slug string
	if err := arg(args, 0, &title); err != nil {
		return nil, userErr(err)
	}
	if err := arg(args, 1, &slug); err != nil {
		return nil, userErr(err)
	}
	title, slug = strings.TrimSpace(title), strings.ToLower(strings.TrimSpace(slug))
	if title == "" || slug == "" {
		return nil, userErr(errors.New("Please input all fields")) //nolint:staticcheck // Shown to users.
	}
	if len(title) > 255 {
		return nil, userErr(errors.New("the title must be at most 255 characters"))
	}
	if err := checkSlug(slug); err != nil {
		return nil, userErr(err)
	}
	if _, err := s.pages.CreateStatusPage(ctx, title, slug); err != nil {
		if errors.Is(err, store.ErrSlugTaken) {
			return nil, userErr(err)
		}
		return nil, err
	}
	s.statusPagesChanged(ctx)
	s.log.Info("status page added from the web UI", "slug", slug)
	return okI18n("successAdded", "slug", slug), nil
}

func (s *Server) getStatusPage(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	return ok("config", statusPageJSON(page, false)), nil
}

// statusPageInput is the editor's config object.
type statusPageInput struct {
	Slug                  string   `json:"slug"`
	Title                 string   `json:"title"`
	Description           *string  `json:"description"`
	Theme                 string   `json:"theme"`
	AutoRefreshInterval   flexInt  `json:"autoRefreshInterval"`
	ShowTags              bool     `json:"showTags"`
	DomainNameList        []string `json:"domainNameList"`
	CustomCSS             *string  `json:"customCSS"`
	FooterText            *string  `json:"footerText"`
	ShowPoweredBy         bool     `json:"showPoweredBy"`
	AnalyticsID           *string  `json:"analyticsId"`
	AnalyticsScriptURL    *string  `json:"analyticsScriptUrl"`
	AnalyticsType         *string  `json:"analyticsType"`
	ShowCertificateExpiry bool     `json:"showCertificateExpiry"`
	ShowOnlyLastHeartbeat bool     `json:"showOnlyLastHeartbeat"`
	RSSTitle              *string  `json:"rssTitle"`
}

type groupInput struct {
	ID          flexInt `json:"id"`
	Name        string  `json:"name"`
	MonitorList []struct {
		ID      flexInt `json:"id"`
		SendURL *bool   `json:"sendUrl"`
		URL     *string `json:"url"`
	} `json:"monitorList"`
}

func text(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// optionalText is nil for a missing or blank value, as the page stores unset analytics.
func optionalSetting(p *string) *string {
	if p == nil || strings.TrimSpace(*p) == "" {
		return nil
	}
	v := strings.TrimSpace(*p)
	return &v
}

// parseStatusPage checks the editor's config and returns the page to store, with its id, and
// the logo to upload if there's a new one.
func parseStatusPage(current store.StatusPage, raw json.RawMessage, imgDataURL string) (store.StatusPage, []byte, error) {
	var in statusPageInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return store.StatusPage{}, nil, fmt.Errorf("invalid status page: %w", err)
	}
	page := store.StatusPage{
		ID:                    current.ID,
		Slug:                  strings.ToLower(strings.TrimSpace(in.Slug)),
		Title:                 strings.TrimSpace(in.Title),
		Description:           text(in.Description),
		Theme:                 in.Theme,
		AutoRefreshInterval:   300,
		ShowTags:              in.ShowTags,
		CustomCSS:             text(in.CustomCSS),
		FooterText:            text(in.FooterText),
		ShowPoweredBy:         in.ShowPoweredBy,
		ShowCertificateExpiry: in.ShowCertificateExpiry,
		ShowOnlyLastHeartbeat: in.ShowOnlyLastHeartbeat,
		RSSTitle:              strings.TrimSpace(text(in.RSSTitle)),
		AnalyticsType:         optionalSetting(in.AnalyticsType),
		AnalyticsID:           optionalSetting(in.AnalyticsID),
		AnalyticsScriptURL:    optionalSetting(in.AnalyticsScriptURL),
	}
	if err := checkSlug(page.Slug); err != nil {
		return page, nil, err
	}
	switch {
	case page.Title == "" || len(page.Title) > 255:
		return page, nil, errors.New("the title must be 1 to 255 characters")
	case len(page.Description) > maxTextField || len(page.FooterText) > maxTextField || len(page.RSSTitle) > 255:
		return page, nil, errors.New("the description, footer or RSS title is too long")
	case len(page.CustomCSS) > maxCustomCSS:
		return page, nil, errors.New("the custom CSS is too long")
	}
	if page.Theme == "" {
		page.Theme = "auto"
	}
	if !slices.Contains(themes, page.Theme) {
		return page, nil, fmt.Errorf("the theme must be one of %s", strings.Join(themes, ", "))
	}
	if in.AutoRefreshInterval.set {
		if in.AutoRefreshInterval.value < 0 || in.AutoRefreshInterval.value > 86400 {
			return page, nil, errors.New("the refresh interval must be from 0 to 86400 seconds")
		}
		page.AutoRefreshInterval = int32(in.AutoRefreshInterval.value)
	}
	if page.AnalyticsType != nil && !slices.Contains(analyticsTypes, *page.AnalyticsType) {
		return page, nil, errors.New("Invalid analytics type") //nolint:staticcheck // Shown to users, as in Node.js.
	}
	for _, field := range []*string{page.AnalyticsID, page.AnalyticsScriptURL} {
		if field != nil && len(*field) > 2048 {
			return page, nil, errors.New("the analytics settings are too long")
		}
	}

	seen := map[string]bool{}
	page.Domains = []string{}
	for _, domain := range in.DomainNameList {
		domain = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(domain)), ".")
		if domain == "" || seen[domain] {
			continue
		}
		if len(domain) > 253 || !domainFormat.MatchString(domain) {
			return page, nil, fmt.Errorf("%q isn't a domain name. Enter it without http:// or a port", domain)
		}
		seen[domain] = true
		page.Domains = append(page.Domains, domain)
	}
	if len(page.Domains) > maxDomains {
		return page, nil, fmt.Errorf("a page can have at most %d domains", maxDomains)
	}

	var logo []byte
	switch {
	case strings.HasPrefix(imgDataURL, "data:"):
		encoded, found := strings.CutPrefix(imgDataURL, pngDataURLPrefix)
		if !found {
			return page, nil, errors.New("Only allowed PNG logo.") //nolint:staticcheck // Shown to users, as in Node.js.
		}
		if len(encoded) > maxLogoBytes*4/3+4 {
			return page, nil, fmt.Errorf("the logo must be at most %d MB", maxLogoBytes>>20)
		}
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) < len(pngSignature) || string(data[:len(pngSignature)]) != string(pngSignature) {
			return page, nil, errors.New("Only allowed PNG logo.") //nolint:staticcheck // Shown to users, as in Node.js.
		}
		logo = data
		page.Icon = logoPath(page.ID, time.Now())
	case imgDataURL == "" || imgDataURL == defaultIcon:
		page.Icon = ""
	default:
		if !safeLink(imgDataURL) {
			return page, nil, errors.New("the logo must be a PNG, or an http(s) address or a path")
		}
		page.Icon = imgDataURL
	}
	return page, logo, nil
}

// safeLink accepts http and https addresses and paths on this server, which is what a logo can
// be. Anything else, such as javascript: or data:, could run in a visitor's browser.
func safeLink(raw string) bool {
	if len(raw) > 2048 || strings.ContainsAny(raw, "\"'<>\\ \t\r\n") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme == "" {
		return strings.HasPrefix(raw, "/")
	}
	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// logoPath is where an uploaded logo is served. The time makes browsers fetch a changed one.
func logoPath(pageID int32, at time.Time) string {
	return fmt.Sprintf("/upload/logo%d.png?t=%d", pageID, at.UnixMilli())
}

func isUploadedLogo(icon string, pageID int32) bool {
	return strings.HasPrefix(icon, fmt.Sprintf("/upload/logo%d.png", pageID))
}

func parseGroups(raw json.RawMessage) ([]store.StatusPageGroup, error) {
	var in []groupInput
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &in); err != nil {
			return nil, fmt.Errorf("invalid group list: %w", err)
		}
	}
	if len(in) > maxGroups {
		return nil, fmt.Errorf("a page can have at most %d groups", maxGroups)
	}
	groups := make([]store.StatusPageGroup, len(in))
	total := 0
	for i, g := range in {
		name := strings.TrimSpace(g.Name)
		if len(name) > 255 {
			return nil, errors.New("a group name must be at most 255 characters")
		}
		groups[i] = store.StatusPageGroup{ID: int32(g.ID.value), Name: name}
		for _, m := range g.MonitorList {
			if !m.ID.set || m.ID.value <= 0 || m.ID.value > 1<<31-1 {
				return nil, errors.New("invalid monitor id")
			}
			monitor := store.StatusPageMonitor{ID: int32(m.ID.value), SendURL: m.SendURL != nil && *m.SendURL}
			if m.URL != nil {
				link := strings.TrimSpace(*m.URL)
				if link != "" && link != "https://" && !safeLink(link) {
					return nil, fmt.Errorf("%q isn't an http(s) address", link)
				}
				monitor.CustomURL = &link
			}
			groups[i].Monitors = append(groups[i].Monitors, monitor)
			total++
		}
	}
	if total > maxGroupMonitors {
		return nil, fmt.Errorf("a page can show at most %d monitors", maxGroupMonitors)
	}
	return groups, nil
}

func (s *Server) saveStatusPage(ctx context.Context, args []json.RawMessage) (reply, error) {
	current, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	var imgDataURL string
	if err := arg(args, 2, &imgDataURL); err != nil {
		return nil, userErr(err)
	}
	var config json.RawMessage
	if len(args) > 1 {
		config = args[1]
	}
	page, logo, err := parseStatusPage(current, config, imgDataURL)
	if err != nil {
		return nil, userErr(err)
	}
	var groupsRaw json.RawMessage
	if len(args) > 3 {
		groupsRaw = args[3]
	}
	groups, err := parseGroups(groupsRaw)
	if err != nil {
		return nil, userErr(err)
	}
	if !isUploadedLogo(page.Icon, page.ID) {
		if err := s.pages.DeleteStatusPageLogo(ctx, page.ID); err != nil {
			return nil, err
		}
	}
	saved, err := s.pages.SaveStatusPage(ctx, current.Slug, page, groups)
	if err != nil {
		if errors.Is(err, store.ErrSlugTaken) {
			return nil, userErr(err)
		}
		return nil, err
	}
	if logo != nil {
		if err := s.pages.SetStatusPageLogo(ctx, page.ID, logo); err != nil {
			return nil, err
		}
	}
	if page.Slug != current.Slug {
		s.moveEntryPage(ctx, current.Slug, statusPagePrefix+page.Slug)
	}
	groupList, err := s.groupsJSON(ctx, page, saved)
	if err != nil {
		return nil, err
	}
	s.statusPagesChanged(ctx)
	s.log.Info("status page saved from the web UI", "slug", page.Slug)
	return ok("publicGroupList", groupList), nil
}

// moveEntryPage changes the entry page setting if it pointed at a page that was renamed or
// deleted.
func (s *Server) moveEntryPage(ctx context.Context, oldSlug, entryPage string) {
	settings, err := s.store.Settings(ctx)
	if err != nil {
		s.log.Error("read entry page", "error", err)
		return
	}
	var current string
	if json.Unmarshal(settings[entryPageKey], &current) != nil || current != statusPagePrefix+oldSlug {
		return
	}
	value, _ := json.Marshal(entryPage)
	if err := s.store.SaveSettings(ctx, map[string]json.RawMessage{entryPageKey: value}); err != nil {
		s.log.Error("save entry page", "error", err)
	}
}

func (s *Server) deleteStatusPage(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := s.pages.DeleteStatusPage(ctx, page.Slug); err != nil {
		return nil, err
	}
	s.moveEntryPage(ctx, page.Slug, "dashboard")
	s.statusPagesChanged(ctx)
	s.log.Info("status page deleted from the web UI", "slug", page.Slug)
	return ok(), nil
}

type incidentInput struct {
	ID      flexInt `json:"id"`
	Title   string  `json:"title"`
	Content string  `json:"content"`
	Style   string  `json:"style"`
	Pin     *bool   `json:"pin"`
	// Notify sends a posted incident to the page's subscribers.
	Notify bool `json:"notify"`
}

// parseIncident checks an incident form. An unknown style becomes warning, as in Node.js.
func parseIncident(args []json.RawMessage, i int, pageID int32) (store.Incident, *bool, error) {
	var in incidentInput
	if err := arg(args, i, &in); err != nil {
		return store.Incident{}, nil, err
	}
	incident := store.Incident{ID: int32(in.ID.value), StatusPageID: pageID, Title: strings.TrimSpace(in.Title), Content: in.Content, Style: in.Style}
	switch {
	case incident.Title == "":
		return incident, nil, errors.New("Please input title") //nolint:staticcheck // Shown to users, as in Node.js.
	case strings.TrimSpace(incident.Content) == "":
		return incident, nil, errors.New("Please input content") //nolint:staticcheck // Shown to users, as in Node.js.
	case len(incident.Title) > 255 || len(incident.Content) > maxTextField:
		return incident, nil, errors.New("the title or content is too long")
	}
	if !slices.Contains(incidentStyles, incident.Style) {
		incident.Style = "warning"
	}
	return incident, in.Pin, nil
}

func (s *Server) postIncident(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	incident, _, err := parseIncident(args, 1, page.ID)
	if err != nil {
		return nil, userErr(err)
	}
	var in incidentInput
	_ = arg(args, 1, &in)
	saved, err := s.pages.PostIncident(ctx, incident)
	if err != nil {
		return nil, err
	}
	s.cache.clear()
	if in.Notify {
		// An update to one that subscribers weren't told of is news to them.
		event := incidentCreated
		if incident.ID != 0 && saved.Notified {
			event = incidentUpdated
		}
		s.notifySubscribers(ctx, page, saved, event)
		saved.Notified = true
	}
	return ok("incident", incidentJSON(saved)), nil
}

func (s *Server) editIncident(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	id, err := idArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	incident, pin, err := parseIncident(args, 2, page.ID)
	if err != nil {
		return nil, userErr(err)
	}
	incident.ID = id
	incident.Pin = pin == nil || *pin
	saved, err := s.pages.EditIncident(ctx, incident)
	if errors.Is(err, store.ErrNotFound) {
		return nil, userErr(errors.New("Incident not found or access denied")) //nolint:staticcheck // Shown to users.
	}
	if err != nil {
		return nil, err
	}
	s.cache.clear()
	return okI18n("Saved.", "incident", incidentJSON(saved)), nil
}

func (s *Server) resolveIncident(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	id, err := idArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	saved, err := s.pages.ResolveIncident(ctx, page.ID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, userErr(errors.New("Incident not found or access denied")) //nolint:staticcheck // Shown to users.
	}
	if err != nil {
		return nil, err
	}
	s.cache.clear()
	// Those who were told of it hear that it's over.
	if saved.Notified {
		s.notifySubscribers(ctx, page, saved, incidentResolved)
	}
	return okI18n("Resolved", "incident", incidentJSON(saved)), nil
}

func (s *Server) deleteIncident(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	id, err := idArg(args, 1)
	if err != nil {
		return nil, userErr(err)
	}
	if err := s.pages.DeleteIncident(ctx, page.ID, id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, userErr(errors.New("Incident not found or access denied")) //nolint:staticcheck // Shown to users.
		}
		return nil, err
	}
	s.cache.clear()
	return okI18n("successDeleted"), nil
}

func (s *Server) unpinIncident(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := s.pages.UnpinIncidents(ctx, page.ID); err != nil {
		return nil, err
	}
	s.cache.clear()
	return ok(), nil
}

func (s *Server) getIncidentHistory(ctx context.Context, args []json.RawMessage) (reply, error) {
	page, err := s.pageBySlug(ctx, args)
	if err != nil {
		return nil, err
	}
	var cursor *string
	if err := arg(args, 1, &cursor); err != nil {
		return nil, userErr(err)
	}
	history, err := s.incidentHistory(ctx, page.ID, text(cursor))
	if err != nil {
		return nil, err
	}
	r := ok()
	for k, v := range history {
		r[k] = v
	}
	return r, nil
}

// setMaintenanceStatusPages links a window to the pages that show it while it runs.
func (s *Server) setMaintenanceStatusPages(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var pages []struct {
		ID flexInt `json:"id"`
	}
	if err := arg(args, 1, &pages); err != nil {
		return nil, userErr(err)
	}
	ids := make([]int32, 0, len(pages))
	for _, p := range pages {
		if p.ID.set && p.ID.value > 0 && p.ID.value <= 1<<31-1 {
			ids = append(ids, int32(p.ID.value))
		}
	}
	if err := s.pages.SetMaintenanceStatusPages(ctx, id, ids); err != nil {
		return nil, err
	}
	s.cache.clear()
	return okI18n("successAdded"), nil
}

func (s *Server) getMaintenanceStatusPages(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	pages, err := s.pages.MaintenanceStatusPages(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, len(pages))
	for i, p := range pages {
		out[i] = map[string]any{"id": p.ID, "title": p.Title}
	}
	return ok("statusPages", out), nil
}
