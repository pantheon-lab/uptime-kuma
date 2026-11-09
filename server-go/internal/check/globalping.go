package check

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// GlobalpingAPI is the Globalping API's address. Tests replace it.
var GlobalpingAPI = "https://api.globalping.io/v1"

const (
	globalpingPollInterval = 500 * time.Millisecond
	globalpingCreditsLink  = "https://dash.globalping.io?view=add-credits"
)

// SetGlobalpingToken sets where the Globalping API token comes from, which is optional: without
// one, the API's anonymous limits apply.
func (c *Checker) SetGlobalpingToken(token SteamKey) { c.globalpingToken = token }

type globalpingError struct {
	Type    string         `json:"type"`
	Message string         `json:"message"`
	Params  map[string]any `json:"params"`
}

// String formats an API error as the Node.js backend did.
func (e globalpingError) String() string {
	text := fmt.Sprintf("%s %s.", e.Type, e.Message)
	keys := make([]string, 0, len(e.Params))
	for key := range e.Params {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		text += fmt.Sprintf("\n%s: %v", key, e.Params[key])
	}
	return text
}

type globalpingProbe struct {
	Continent string   `json:"continent"`
	Country   string   `json:"country"`
	State     string   `json:"state"`
	City      string   `json:"city"`
	ASN       int      `json:"asn"`
	Network   string   `json:"network"`
	Tags      []string `json:"tags"`
}

// location is how the Node.js backend named a probe, such as
// "Ashburn (VA), US, NA, Amazon.com (AS14618), (aws-us-east-1)".
func (p globalpingProbe) location() string {
	tag := ""
	for _, t := range p.Tags {
		if t != "" && t[len(t)-1] >= '0' && t[len(t)-1] <= '9' {
			tag = t
			break
		}
	}
	text := p.City
	if p.State != "" {
		text += " (" + p.State + ")"
	}
	text += fmt.Sprintf(", %s, %s, %s (AS%d)", p.Country, p.Continent, p.Network, p.ASN)
	if tag != "" {
		text += ", (" + tag + ")"
	}
	return text
}

type globalpingResult struct {
	Status         string          `json:"status"`
	RawOutput      string          `json:"rawOutput"`
	RawBody        *string         `json:"rawBody"`
	StatusCode     int             `json:"statusCode"`
	StatusCodeName string          `json:"statusCodeName"`
	Timings        json.RawMessage `json:"timings"`
	Stats          struct {
		Avg *float64 `json:"avg"`
	} `json:"stats"`
	TLS *struct {
		Authorized bool   `json:"authorized"`
		Error      string `json:"error"`
	} `json:"tls"`
	Answers []struct {
		Value string `json:"value"`
	} `json:"answers"`
}

// totalTime reads timings.total of HTTP and DNS results.
func (r globalpingResult) totalTime() float64 {
	var timings struct {
		Total *float64 `json:"total"`
	}
	if json.Unmarshal(r.Timings, &timings) == nil && timings.Total != nil {
		return *timings.Total
	}
	return 0
}

// runGlobalping runs one measurement from a probe at the monitor's location, as the Node.js
// backend did with the globalping library.
func (c *Checker) runGlobalping(ctx context.Context, m monitor.Monitor) monitor.Result {
	token := ""
	if c.globalpingToken != nil {
		if t, err := c.globalpingToken(ctx); err == nil {
			token = t
		}
	}
	options := map[string]any{}
	request := map[string]any{
		"type": m.Subtype, "inProgressUpdates": false, "limit": 1,
		"locations":          []map[string]string{{"magic": cmpString(m.Location, "world")}},
		"measurementOptions": options,
	}
	switch m.IPFamily {
	case "ipv4":
		options["ipVersion"] = 4
	case "ipv6":
		options["ipVersion"] = 6
	}
	var target *url.URL
	switch m.Subtype {
	case "ping":
		request["target"] = strings.TrimSpace(m.Hostname)
		options["packets"] = m.PingCount
		options["protocol"] = cmpString(m.Protocol, "ICMP")
		if m.Protocol == "TCP" && m.Port > 0 {
			options["port"] = m.Port
		}
	case "http":
		raw := m.URL
		if m.CacheBust {
			raw = cacheBustURL(raw)
		}
		var err error
		if target, err = url.Parse(raw); err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		headers, err := c.globalpingHeaders(ctx, m)
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		protocol := strings.ToUpper(target.Scheme)
		if m.Protocol == "HTTP2" {
			protocol = "HTTP2"
		}
		httpRequest := map[string]any{"host": target.Hostname(), "path": cmpString(target.EscapedPath(), "/"), "method": m.Method, "headers": headers}
		if target.RawQuery != "" {
			httpRequest["query"] = target.RawQuery
		}
		request["target"] = target.Hostname()
		options["request"] = httpRequest
		options["protocol"] = protocol
		if port := target.Port(); port != "" {
			options["port"], _ = strconv.Atoi(port)
		}
		if m.DNSResolveServer != "" {
			options["resolver"] = m.DNSResolveServer
		}
	case "dns":
		request["target"] = strings.TrimSpace(m.Hostname)
		options["query"] = map[string]string{"type": m.DNSResolveType}
		options["port"] = m.Port
		options["protocol"] = cmpString(m.Protocol, "UDP")
		if m.DNSResolveServer != "" {
			options["resolver"] = m.DNSResolveServer
		}
	default:
		return monitor.Result{Msg: fmt.Sprintf("unknown Globalping type %q", m.Subtype)}
	}

	probe, result, err := globalpingMeasure(ctx, token, request)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	say := func(text string) string { return probe.location() + " : " + text }
	if result.Status == "failed" {
		return monitor.Result{Msg: say("Failed: " + result.RawOutput)}
	}

	switch m.Subtype {
	case "ping":
		var timings []json.RawMessage
		if json.Unmarshal(result.Timings, &timings) != nil || len(timings) == 0 {
			return monitor.Result{Msg: say("Failed: " + result.RawOutput)}
		}
		ping := 0.0
		if result.Stats.Avg != nil {
			ping = *result.Stats.Avg
		}
		return monitor.Result{OK: true, Ping: &ping, Msg: say("OK")}
	case "http":
		return globalpingHTTPResult(m, probe, result, target)
	}
	return c.globalpingDNSResult(ctx, m, probe, result)
}

func cmpString(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

// globalpingHeaders are the monitor's headers after its login's, as in Node.js.
func (c *Checker) globalpingHeaders(ctx context.Context, m monitor.Monitor) (map[string]string, error) {
	login, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://login.invalid", nil)
	if err != nil {
		return nil, err
	}
	if m.AuthMethod == "basic" || m.AuthMethod == "bearer" || m.AuthMethod == "oauth2-cc" {
		if err := c.authorize(ctx, login, m, false); err != nil {
			return nil, err
		}
	}
	headers := map[string]string{}
	if auth := login.Header.Get("Authorization"); auth != "" {
		headers["Authorization"] = auth
	}
	for name, value := range m.Headers {
		headers[name] = value
	}
	return headers, nil
}

func globalpingHTTPResult(m monitor.Monitor, probe globalpingProbe, result globalpingResult, target *url.URL) monitor.Result {
	say := func(text string) string { return probe.location() + " : " + text }
	ping := result.totalTime()
	if !m.Accepts(result.StatusCode) {
		return monitor.Result{Msg: say(fmt.Sprintf("Status code %d not accepted. Output: %s", result.StatusCode, result.RawOutput))}
	}
	msg := say(fmt.Sprintf("%d - %s", result.StatusCode, result.StatusCodeName))
	if m.Keyword != "" {
		found := strings.Contains(result.RawOutput, m.Keyword)
		if found == m.InvertKeyword {
			state := "not"
			if found {
				state = "present"
			}
			return monitor.Result{Msg: fmt.Sprintf("%s, but keyword is %s in [%s]", msg, state, snippet([]byte(result.RawOutput)))}
		}
		state := "not"
		if found {
			state = "is"
		}
		return monitor.Result{OK: true, Ping: &ping, Msg: fmt.Sprintf("%s, keyword %s found", msg, state)}
	}
	if m.ExpectedValue != "" {
		// The body alone, where the probe sent it. Node.js used the whole output, headers too.
		body := result.RawOutput
		if result.RawBody != nil {
			body = *result.RawBody
		}
		query := evaluateJSONQuery([]byte(body), m)
		query.Msg = say(query.Msg)
		if query.OK {
			query.Ping = &ping
		}
		return query
	}
	if result.TLS != nil && !m.IgnoreTLS && target.Scheme == "https" && m.Protocol != "HTTP2" && !result.TLS.Authorized {
		return monitor.Result{Msg: say("TLS certificate is not authorized: " + result.TLS.Error)}
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: say("OK")}
}

func (c *Checker) globalpingDNSResult(ctx context.Context, m monitor.Monitor, probe globalpingProbe, result globalpingResult) monitor.Result {
	say := func(text string) string { return probe.location() + " : " + text }
	values := make([]string, len(result.Answers))
	for i, answer := range result.Answers {
		values[i] = answer.Value
	}
	message := strings.Join(values, " | ")
	if message != "" && c.dnsResults != nil && !isDryRun(ctx) {
		if err := c.dnsResults.SetDNSLastResult(ctx, m.ID, monitor.SanitizeMsg(message)); err != nil {
			slog.Warn("store DNS result", "monitor", m.ID, "error", err)
		}
	}
	ping := result.totalTime()
	if message == "" {
		message = "No records found. " + result.StatusCodeName
	}
	if m.Keyword != "" {
		// A case-insensitive regular expression, as in Node.js.
		pattern, err := regexp.Compile("(?i)" + m.Keyword)
		if err != nil {
			return monitor.Result{Msg: say("invalid keyword: " + err.Error())}
		}
		if !slices.ContainsFunc(values, pattern.MatchString) {
			return monitor.Result{Msg: say("No record matched. " + message)}
		}
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: say(message)}
}

// globalpingMeasure creates a measurement, waits for it, and returns its one result.
func globalpingMeasure(ctx context.Context, token string, request map[string]any) (globalpingProbe, globalpingResult, error) {
	body, err := json.Marshal(request)
	if err != nil {
		return globalpingProbe{}, globalpingResult{}, err
	}
	var created struct {
		ID    string          `json:"id"`
		Error globalpingError `json:"error"`
	}
	status, err := globalpingCall(ctx, http.MethodPost, "/measurements", token, body, &created)
	if err == nil && status >= 500 {
		// Retried once, as in Node.js.
		status, err = globalpingCall(ctx, http.MethodPost, "/measurements", token, body, &created)
	}
	switch {
	case err != nil:
		return globalpingProbe{}, globalpingResult{}, err
	case status == http.StatusTooManyRequests:
		help := "creating an account. Sign up at"
		if token != "" {
			help = "sponsoring us or hosting probes. Learn more at"
		}
		return globalpingProbe{}, globalpingResult{}, fmt.Errorf("Failed to create measurement: You have run out of credits. Get higher limits by %s %s.", help, globalpingCreditsLink) //nolint:staticcheck // As in Node.js.
	case status >= 300 || created.ID == "":
		return globalpingProbe{}, globalpingResult{}, fmt.Errorf("Failed to create measurement: %s", created.Error) //nolint:staticcheck // As in Node.js.
	}

	for {
		var measurement struct {
			Status  string `json:"status"`
			Results []struct {
				Probe  globalpingProbe  `json:"probe"`
				Result globalpingResult `json:"result"`
			} `json:"results"`
			Error globalpingError `json:"error"`
		}
		status, err := globalpingCall(ctx, http.MethodGet, "/measurements/"+url.PathEscape(created.ID), token, nil, &measurement)
		if err != nil {
			return globalpingProbe{}, globalpingResult{}, err
		}
		if status >= 300 {
			return globalpingProbe{}, globalpingResult{}, fmt.Errorf("Failed to fetch measurement (%s): %s", created.ID, measurement.Error) //nolint:staticcheck // As in Node.js.
		}
		if measurement.Status != "in-progress" {
			if len(measurement.Results) == 0 {
				return globalpingProbe{}, globalpingResult{}, errors.New("no probe matched the location")
			}
			return measurement.Results[0].Probe, measurement.Results[0].Result, nil
		}
		select {
		case <-ctx.Done():
			return globalpingProbe{}, globalpingResult{}, errors.New("the measurement didn't finish in time")
		case <-time.After(globalpingPollInterval):
		}
	}
}

func globalpingCall(ctx context.Context, method, path, token string, body []byte, v any) (int, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, GlobalpingAPI+path, reader)
	if err != nil {
		return 0, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return 0, fmt.Errorf("Globalping API: %w", err) //nolint:staticcheck // Shown to users.
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return 0, err
	}
	if len(data) > 0 {
		_ = json.Unmarshal(data, v)
	}
	return res.StatusCode, nil
}
