package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	nethtml "golang.org/x/net/html"
	"golang.org/x/net/html/atom"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// descriptionRunes is how much of a page's description goes in its meta tags, as in Node.js.
const descriptionRunes = 155

// renderStatusPage sends the web UI's index.html with the page's title, description, icon and
// analytics in it, and its data preloaded, as the Node.js backend did. Search engines and link
// previews see the page without running its JavaScript.
func (s *Server) renderStatusPage(w http.ResponseWriter, r *http.Request, slug string) {
	if len(s.opts.IndexHTML) == 0 {
		http.Error(w, "This build has no web UI. The status page's data is at /api/status-page/"+url.PathEscape(slug), http.StatusNotFound)
		return
	}
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	s.serveCached(w, r, "html:"+slug, pageTTL, func(ctx context.Context) (cached, error) {
		page, err := s.findPage(ctx, slug)
		if err != nil {
			return cached{}, err
		}
		entry := cached{status: http.StatusNotFound, contentType: "text/html; charset=utf-8", body: s.opts.IndexHTML, cacheControl: "no-cache"}
		if page == nil {
			return entry, nil
		}
		data, err := s.statusPageData(ctx, *page)
		if err != nil {
			return cached{}, err
		}
		body, err := renderPageHTML(s.opts.IndexHTML, *page, data)
		if err != nil {
			return cached{}, err
		}
		entry.status, entry.body = http.StatusOK, body
		return entry, nil
	})
}

var (
	markdownLink   = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]*\)`)
	markdownMarks  = regexp.MustCompile("(?m)^\\s{0,3}(#{1,6}\\s+|>\\s?|[-*+]\\s+|\\d+[.)]\\s+)|[*_`~]+")
	htmlTag        = regexp.MustCompile(`<[^>]+>`)
	spaceRun       = regexp.MustCompile(`\s+`)
	analyticsIDFmt = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,200}$`)
)

// plainDescription is the start of a page's description without its Markdown, for meta tags.
func plainDescription(markdown string) string {
	text := markdownLink.ReplaceAllString(markdown, "$1")
	text = markdownMarks.ReplaceAllString(text, "")
	text = htmlTag.ReplaceAllString(text, "")
	text = html.UnescapeString(strings.TrimSpace(spaceRun.ReplaceAllString(text, " ")))
	runes := []rune(text)
	if len(runes) > descriptionRunes {
		runes = runes[:descriptionRunes]
	}
	return string(runes)
}

func findAll(n *nethtml.Node, match func(*nethtml.Node) bool) []*nethtml.Node {
	var out []*nethtml.Node
	var walk func(*nethtml.Node)
	walk = func(n *nethtml.Node) {
		if n.Type == nethtml.ElementNode && match(n) {
			out = append(out, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return out
}

func attr(n *nethtml.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *nethtml.Node, key, value string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = value
			return
		}
	}
	n.Attr = append(n.Attr, nethtml.Attribute{Key: key, Val: value})
}

func removeAttr(n *nethtml.Node, key string) {
	attrs := n.Attr[:0]
	for _, a := range n.Attr {
		if a.Key != key {
			attrs = append(attrs, a)
		}
	}
	n.Attr = attrs
}

func element(tag atom.Atom, attrs ...string) *nethtml.Node {
	n := &nethtml.Node{Type: nethtml.ElementNode, DataAtom: tag, Data: tag.String()}
	for i := 0; i+1 < len(attrs); i += 2 {
		n.Attr = append(n.Attr, nethtml.Attribute{Key: attrs[i], Val: attrs[i+1]})
	}
	return n
}

func script(body string, attrs ...string) *nethtml.Node {
	n := element(atom.Script, attrs...)
	if body != "" {
		n.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: body})
	}
	return n
}

// jsString is a value as a JavaScript literal that's safe inside a script element: encoding/json
// escapes <, > and &, so the text can't close the element.
func jsString(v any) string {
	data, _ := json.Marshal(v)
	return string(data)
}

func renderPageHTML(index []byte, page store.StatusPage, data map[string]any) ([]byte, error) {
	doc, err := nethtml.Parse(bytes.NewReader(index))
	if err != nil {
		return nil, fmt.Errorf("parse index.html: %w", err)
	}
	heads := findAll(doc, func(n *nethtml.Node) bool { return n.DataAtom == atom.Head })
	if len(heads) == 0 {
		return nil, fmt.Errorf("index.html has no head")
	}
	head := heads[0]
	description := plainDescription(page.Description)

	for _, title := range findAll(head, func(n *nethtml.Node) bool { return n.DataAtom == atom.Title }) {
		for c := title.FirstChild; c != nil; c = title.FirstChild {
			title.RemoveChild(c)
		}
		title.AppendChild(&nethtml.Node{Type: nethtml.TextNode, Data: page.Title})
	}
	for _, meta := range findAll(head, func(n *nethtml.Node) bool {
		return n.DataAtom == atom.Meta && attr(n, "name") == "description"
	}) {
		setAttr(meta, "content", description)
	}
	if page.Icon != "" {
		for _, link := range findAll(head, func(n *nethtml.Node) bool { return n.DataAtom == atom.Link }) {
			switch attr(link, "rel") {
			case "icon":
				setAttr(link, "href", page.Icon)
				removeAttr(link, "type")
			case "apple-touch-icon":
				link.Parent.RemoveChild(link)
			}
		}
	}
	for _, link := range findAll(head, func(n *nethtml.Node) bool {
		return n.DataAtom == atom.Link && attr(n, "rel") == "manifest"
	}) {
		setAttr(link, "href", "/api/status-page/"+url.PathEscape(page.Slug)+"/manifest.json")
	}

	for _, n := range analyticsNodes(page) {
		head.AppendChild(n)
	}
	head.AppendChild(element(atom.Meta, "property", "og:title", "content", page.Title))
	head.AppendChild(element(atom.Meta, "property", "og:description", "content", description))
	head.AppendChild(element(atom.Meta, "property", "og:type", "content", "website"))
	head.AppendChild(script("window.preloadData = "+jsString(data)+";", "id", "preload-data", "data-json", "{}"))

	var out bytes.Buffer
	if err := nethtml.Render(&out, doc); err != nil {
		return nil, fmt.Errorf("render status page: %w", err)
	}
	return out.Bytes(), nil
}

// analyticsNodes are the tracking scripts of a page's analytics service, as the Node.js backend
// made them. Values go into JavaScript as JSON and into attributes through the HTML renderer,
// so a setting can't add markup or code of its own.
func analyticsNodes(page store.StatusPage) []*nethtml.Node {
	if page.AnalyticsType == nil || page.AnalyticsID == nil {
		return nil
	}
	id := *page.AnalyticsID
	scriptURL := ""
	if page.AnalyticsScriptURL != nil {
		scriptURL = *page.AnalyticsScriptURL
	}
	switch *page.AnalyticsType {
	case "google":
		if !analyticsIDFmt.MatchString(id) {
			return nil
		}
		return []*nethtml.Node{
			script("", "async", "", "src", "https://www.googletagmanager.com/gtag/js?id="+url.QueryEscape(id)),
			script("window.dataLayer = window.dataLayer || []; function gtag(){dataLayer.push(arguments);} gtag('js', new Date());gtag('config', " + jsString(id) + ");"),
		}
	case "umami", "plausible", "rybbit":
		if !safeLink(scriptURL) {
			return nil
		}
		idAttr := map[string]string{"umami": "data-website-id", "plausible": "data-domain", "rybbit": "data-site-id"}[*page.AnalyticsType]
		return []*nethtml.Node{script("", "defer", "", "src", scriptURL, idAttr, id)}
	case "matomo":
		// The Matomo setting is a host and path, such as matomo.example.com, without a scheme.
		host := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(scriptURL, "https://"), "http://"), "/")
		if host == "" || !safeLink("https://"+host) {
			return nil
		}
		return []*nethtml.Node{script(`
            var _paq = window._paq = window._paq || [];
            _paq.push(['trackPageView']);
            _paq.push(['enableLinkTracking']);
            (function() {
                var u="//" + `+jsString(host)+` + "/";
                _paq.push(['setTrackerUrl', u+'matomo.php']);
                _paq.push(['setSiteId', `+jsString(id)+`]);
                var d=document, g=d.createElement('script'), s=d.getElementsByTagName('script')[0];
                g.type='text/javascript'; g.async=true; g.src=u+'matomo.js'; s.parentNode.insertBefore(g,s);
            })();
        `, "type", "text/javascript")}
	}
	return nil
}

// The Node.js backend's overall states of a page, for the feed's description.
func overallDescription(beats []store.Beat) string {
	if len(beats) == 0 {
		return "No Services"
	}
	hasUp, allUp := false, true
	for _, b := range beats {
		switch b.Status {
		case monitor.StatusMaintenance:
			return "Under maintenance"
		case monitor.StatusUp:
			hasUp = true
		default:
			allUp = false
		}
	}
	switch {
	case !hasUp:
		return "Degraded Service"
	case !allUp:
		return "Partially Degraded Service"
	}
	return "All Systems Operational"
}

// rssItem is written as the feed package that the Node.js backend used wrote it: text that
// comes from users is in CDATA sections.
type rssItem struct {
	Title       cdata  `xml:"title"`
	Link        string `xml:"link"`
	GUID        rssGUID
	PubDate     string `xml:"pubDate"`
	Description cdata  `xml:"description"`
}

// cdata is text in a CDATA section. encoding/xml splits a "]]>" in it, so it can't end early.
type cdata struct {
	Text string `xml:",cdata"`
}

type rssGUID struct {
	XMLName     xml.Name `xml:"guid"`
	IsPermaLink string   `xml:"isPermaLink,attr"`
	Value       string   `xml:",chardata"`
}

type rssFeed struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Channel struct {
		Title         string    `xml:"title"`
		Link          string    `xml:"link"`
		Description   string    `xml:"description"`
		LastBuildDate string    `xml:"lastBuildDate"`
		Docs          string    `xml:"docs"`
		Generator     string    `xml:"generator"`
		Language      string    `xml:"language"`
		Items         []rssItem `xml:"item"`
	} `xml:"channel"`
}

// statusPageRSS is a feed of a page's pinned incidents and the monitors that are down.
func (s *Server) statusPageRSS(w http.ResponseWriter, r *http.Request) {
	slug := pageSlug(r.PathValue("slug"))
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()
	route, err := s.routing(ctx)
	if err != nil {
		s.log.Error("read routing", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	feedURL := requestOrigin(r, route.trustProxy) + "/status/" + url.PathEscape(slug)
	s.serveCached(w, r, "rss:"+slug+"\x00"+feedURL, pageTTL, func(ctx context.Context) (cached, error) {
		page, err := s.findPage(ctx, slug)
		if err != nil {
			return cached{}, err
		}
		if page == nil {
			return cached{status: http.StatusNotFound, contentType: "text/html; charset=utf-8", body: s.opts.IndexHTML, cacheControl: "no-cache"}, nil
		}
		body, err := s.renderRSS(ctx, *page, feedURL)
		if err != nil {
			return cached{}, err
		}
		return cached{status: http.StatusOK, contentType: "application/rss+xml; charset=utf-8", body: body}, nil
	})
}

func (s *Server) renderRSS(ctx context.Context, page store.StatusPage, feedURL string) ([]byte, error) {
	incidents, err := s.pages.PinnedIncidents(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	groups, err := s.pages.StatusPageGroups(ctx, page.ID)
	if err != nil {
		return nil, err
	}
	var ids []int32
	names := map[int32]string{}
	for _, g := range groups {
		for _, m := range g.Monitors {
			if _, seen := names[m.ID]; !seen {
				ids = append(ids, m.ID)
			}
			names[m.ID] = m.Name
		}
	}
	latest, err := s.pages.RecentBeats(ctx, ids, 1)
	if err != nil {
		return nil, err
	}
	var beats []store.Beat
	for _, id := range ids {
		if list := latest[id]; len(list) > 0 {
			beats = append(beats, list[0])
		}
	}

	feed := rssFeed{Version: "2.0"}
	feed.Channel.Title = "Uptime Kuma RSS Feed"
	switch {
	case page.RSSTitle != "":
		feed.Channel.Title = page.RSSTitle
	case page.Title != "":
		feed.Channel.Title = page.Title + " RSS Feed"
	}
	feed.Channel.Link = feedURL
	feed.Channel.Description = "Current status: " + overallDescription(beats)
	feed.Channel.LastBuildDate = s.now().UTC().Format(time.RFC1123Z)
	feed.Channel.Docs = "https://validator.w3.org/feed/docs/rss2.html"
	feed.Channel.Generator = "Uptime Kuma"
	feed.Channel.Language = "en"
	for _, i := range incidents {
		updated := i.CreatedAt
		if i.UpdatedAt != nil {
			updated = *i.UpdatedAt
		}
		feed.Channel.Items = append(feed.Channel.Items, rssItem{
			Title: cdata{i.Title}, Link: feedURL, Description: cdata{i.Content}, PubDate: updated.UTC().Format(time.RFC1123Z),
			GUID: rssGUID{IsPermaLink: "false", Value: "i" + strconv.Itoa(int(i.ID)) + "-" + updated.UTC().Format(beatTimeFormat)},
		})
	}
	for _, b := range beats {
		if b.Status != monitor.StatusDown {
			continue
		}
		at := b.Time.UTC().Format(beatTimeFormat)
		feed.Channel.Items = append(feed.Channel.Items, rssItem{
			Title: cdata{names[b.MonitorID] + " is down"}, Link: feedURL, PubDate: b.Time.UTC().Format(time.RFC1123Z),
			Description: cdata{names[b.MonitorID] + " has been down since " + at + " UTC"},
			GUID:        rssGUID{IsPermaLink: "false", Value: strconv.Itoa(int(b.MonitorID)) + "-" + at},
		})
	}
	out, err := xml.MarshalIndent(feed, "", "    ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), out...), nil
}
