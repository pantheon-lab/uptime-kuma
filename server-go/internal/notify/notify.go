// Package notify sends monitor alerts through notification providers. The providers are ported
// from the original Node.js backend and keep its configuration field names, so that existing
// configurations can be imported. testdata/golden records what the originals sent, and the
// parity test holds these providers to it.
package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata" // The container image has no timezone files.
	"unicode/utf16"

	"github.com/osteele/liquid"
)

// M is a JSON object.
type M = map[string]any

// H holds request headers.
type H = map[string]string

const (
	statusDown = 0
	statusUp   = 1

	iconURL        = "https://github.com/louislam/uptime-kuma/raw/master/public/icon.png"
	maxResponseLen = 1 << 20
	maxErrorBody   = 500
)

// Env is what providers need from the outside. Tests replace all of it.
type Env struct {
	Client    *http.Client
	BaseURL   string
	Now       func() time.Time
	RandBytes func(n int) []byte
	RandFloat func() float64
}

// DefaultEnv returns the production environment. baseURL is the public URL of this service.
func DefaultEnv(baseURL string) *Env {
	return &Env{
		Client:  &http.Client{},
		BaseURL: baseURL,
		Now:     time.Now,
		RandBytes: func(n int) []byte {
			b := make([]byte, n)
			_, _ = rand.Read(b)
			return b
		},
		RandFloat: func() float64 {
			b := make([]byte, 2)
			_, _ = rand.Read(b)
			return float64(b[0])/256 + float64(b[1])/65536
		},
	}
}

// Message is what to send. Monitor and Beat are nil for a test message.
type Message struct {
	Text    string
	Monitor M
	Beat    M
}

type provider func(c *call) error

// Providers lists the supported provider types.
func Providers() []string {
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Supported reports whether a provider type exists.
func Supported(name string) bool {
	_, ok := providers[name]
	return ok
}

// Send delivers one message using a provider's configuration.
func Send(ctx context.Context, env *Env, kind string, config M, message Message) error {
	send, ok := providers[kind]
	if !ok {
		return fmt.Errorf("notification type %q is not supported", kind)
	}
	return send(&call{ctx: ctx, env: env, cfg: config, msg: message.Text, mon: message.Monitor, beat: message.Beat})
}

type call struct {
	ctx  context.Context
	env  *Env
	cfg  M
	msg  string
	mon  M
	beat M
}

// undefined marks a value that JSON.stringify would leave out.
type undefined struct{}

var undef = undefined{}

// s returns a config field as text, and "" if it's missing.
func (c *call) s(field string) string {
	v, ok := c.cfg[field]
	if !ok || v == nil {
		return ""
	}
	return js(v)
}

// v returns a config field for a JSON body, left out if it's missing.
func (c *call) v(field string) any {
	if v, ok := c.cfg[field]; ok {
		return v
	}
	return undef
}

// t reports whether a config field is truthy in JavaScript's sense.
func (c *call) t(field string) bool { return truthy(c.cfg[field]) }

// or returns a config field, or a default if the field isn't truthy.
func (c *call) or(field string, fallback any) any {
	if c.t(field) {
		return c.cfg[field]
	}
	return fallback
}

func (c *call) hasBeat() bool { return c.beat != nil }

func (c *call) status() int {
	if n, ok := number(c.beat["status"]); ok {
		return int(n)
	}
	return -1
}

func (c *call) isUp() bool   { return c.hasBeat() && c.status() == statusUp }
func (c *call) isDown() bool { return c.hasBeat() && c.status() == statusDown }

func (c *call) ms(key string) string { return js(c.mon[key]) }
func (c *call) bs(key string) string { return js(c.beat[key]) }

func (c *call) timeLine() string {
	return fmt.Sprintf("Time (%s): %s", c.bs("timezone"), c.bs("localDateTime"))
}

// address follows extractAddress in the original.
func (c *call) address() string {
	if c.mon == nil {
		return ""
	}
	switch js(c.mon["type"]) {
	case "push":
		return "Heartbeat"
	case "ping", "dns":
		return c.ms("hostname")
	case "port", "gamedig", "steam":
		if truthy(c.mon["port"]) {
			return c.ms("hostname") + ":" + c.ms("port")
		}
		return c.ms("hostname")
	}
	u := ""
	if c.mon["url"] != nil {
		u = c.ms("url")
	}
	if u == "https://" || u == "http://" {
		return ""
	}
	return u
}

func (c *call) monitorURL() string { return c.env.BaseURL + "/dashboard/" + c.ms("id") }

// render fills in a Liquid template, with the same variables as the original.
func (c *call) render(template string) (string, error) {
	return renderWith(template, c.msg, c.mon, c.beat, c.address())
}

func renderWith(template, msg string, monitor, beat M, address string) (string, error) {
	name, host, status := any("Monitor Name not available"), any("testing.hostname"), "⚠️ Test"
	if monitor != nil {
		name, host = monitor["name"], address
	}
	if beat != nil {
		status = "✅ Up"
		if n, _ := number(beat["status"]); n == statusDown {
			status = "🔴 Down"
		}
	}
	var monitorValue, beatValue any
	if monitor != nil {
		monitorValue = monitor
	}
	if beat != nil {
		beatValue = beat
	}
	out, err := liquid.NewEngine().ParseAndRenderString(template, map[string]any{
		"STATUS": status, "NAME": name, "HOSTNAME_OR_URL": host,
		"status": status, "name": name, "hostnameOrURL": host,
		"monitorJSON": monitorValue, "heartbeatJSON": beatValue, "msg": msg,
	})
	if err != nil {
		return "", fmt.Errorf("render template: %w", err)
	}
	return out, nil
}

// form is a URL-encoded body, keeping the order of its fields.
type form [][2]string

// multipartForm is a multipart body.
type multipartForm [][2]string

type response struct {
	Status int
	Data   any
	Text   string
}

func (r *response) m() M {
	m, _ := r.Data.(M)
	return m
}

// HTTPError is a response outside 2xx.
type HTTPError struct {
	Status int
	Body   string
}

func (e *HTTPError) Error() string {
	if e.Body == "" {
		return fmt.Sprintf("HTTP %d", e.Status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.Status, e.Body)
}

func (c *call) post(target string, body any, headers H) (*response, error) {
	return c.request(http.MethodPost, target, body, headers)
}

func (c *call) get(target string, headers H) (*response, error) {
	return c.request(http.MethodGet, target, nil, headers)
}

func (c *call) postOnly(target string, body any, headers H) error {
	_, err := c.post(target, body, headers)
	return err
}

func (c *call) request(method, target string, body any, headers H) (*response, error) {
	var reader io.Reader
	contentType := ""
	switch b := body.(type) {
	case nil:
	case string:
		reader = strings.NewReader(b)
		contentType = "application/x-www-form-urlencoded"
	case form:
		reader = strings.NewReader(encodeForm(b))
		contentType = "application/x-www-form-urlencoded;charset=utf-8"
	case multipartForm:
		var buffer bytes.Buffer
		writer := multipart.NewWriter(&buffer)
		for _, field := range b {
			if err := writer.WriteField(field[0], field[1]); err != nil {
				return nil, err
			}
		}
		if err := writer.Close(); err != nil {
			return nil, err
		}
		reader = &buffer
		contentType = writer.FormDataContentType()
	default:
		data, err := marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
		contentType = "application/json"
	}

	req, err := http.NewRequestWithContext(c.ctx, method, target, reader)
	if err != nil {
		return nil, errors.New("invalid notification URL")
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", "Uptime-Kuma-Go/0")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	for name, value := range headers {
		// A multipart boundary has to stay, whatever the configured header says.
		if _, isMultipart := body.(multipartForm); isMultipart && strings.EqualFold(name, "Content-Type") {
			continue
		}
		req.Header.Set(name, value)
	}

	res, err := c.env.Client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			// The URL often contains a token, so it must not reach logs or API responses.
			return nil, urlErr.Err
		}
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseLen))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		text := strings.ToValidUTF8(string(data), "")
		if len(text) > maxErrorBody {
			text = text[:maxErrorBody]
		}
		return nil, &HTTPError{Status: res.StatusCode, Body: text}
	}
	result := &response{Status: res.StatusCode, Text: string(data)}
	var parsed any
	if json.Unmarshal(data, &parsed) == nil {
		result.Data = parsed
	}
	return result, nil
}

func marshal(body any) ([]byte, error) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(strip(body)); err != nil {
		return nil, fmt.Errorf("encode body: %w", err)
	}
	return bytes.TrimRight(buffer.Bytes(), "\n"), nil
}

// strip removes undefined values, the way JSON.stringify does.
func strip(value any) any {
	switch v := value.(type) {
	case M:
		out := make(M, len(v))
		for key, item := range v {
			if _, skip := item.(undefined); !skip {
				out[key] = strip(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			if _, skip := item.(undefined); skip {
				out[i] = nil
			} else {
				out[i] = strip(item)
			}
		}
		return out
	}
	return value
}

func encodeForm(fields form) string {
	var parts []string
	for _, field := range fields {
		parts = append(parts, url.QueryEscape(field[0])+"="+url.QueryEscape(field[1]))
	}
	return strings.Join(parts, "&")
}

// js formats a value the way JavaScript's string conversion does.
func js(v any) string {
	switch value := v.(type) {
	case nil:
		return "null"
	case string:
		return value
	case bool:
		return strconv.FormatBool(value)
	case undefined:
		return "undefined"
	}
	if n, ok := number(v); ok {
		return strconv.FormatFloat(n, 'f', -1, 64)
	}
	data, err := marshal(v)
	if err != nil {
		return ""
	}
	return string(data)
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int16:
		return float64(n), true
	case int32:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	}
	return 0, false
}

func truthy(v any) bool {
	switch value := v.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case undefined:
		return false
	}
	if n, ok := number(v); ok {
		return n != 0 && !math.IsNaN(n)
	}
	return true
}

// encodeURIComponent matches JavaScript's function exactly, which signatures depend on.
func encodeURIComponent(s string) string {
	var out strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		switch {
		case ch >= 'A' && ch <= 'Z', ch >= 'a' && ch <= 'z', ch >= '0' && ch <= '9',
			ch == '-', ch == '_', ch == '.', ch == '!', ch == '~', ch == '*', ch == '\'', ch == '(', ch == ')':
			out.WriteByte(ch)
		default:
			fmt.Fprintf(&out, "%%%02X", ch)
		}
	}
	return out.String()
}

var nonASCII = regexp.MustCompile(`[^\x00-\x7F]`)

func ascii(s string) string { return nonASCII.ReplaceAllString(s, "") }

// jsSubstring cuts by UTF-16 units, as JavaScript does.
func jsSubstring(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

// jsNumber follows Number(): text that isn't a number becomes NaN, which JSON sends as null.
func jsNumber(s string) any {
	if n, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
		return n
	}
	return nil
}

func splitTrim(s, separators string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool { return strings.ContainsRune(separators, r) })
	out := []string{}
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func anyList(items []string) []any {
	out := make([]any, len(items))
	for i, item := range items {
		out[i] = item
	}
	return out
}

func parseLocalUTC(value string) (time.Time, bool) {
	for _, layout := range []string{"2006-01-02 15:04:05.999", time.RFC3339Nano} {
		if at, err := time.ParseInLocation(layout, value, time.UTC); err == nil {
			return at, true
		}
	}
	return time.Time{}, false
}

func isoNow(c *call) string { return c.env.Now().UTC().Format("2006-01-02T15:04:05.000Z") }

func basicAuth(user, password string) string { return "Basic " + encodeBase64(user+":"+password) }
