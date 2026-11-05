package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

type goldenRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Body    *string           `json:"body"`
}

type goldenScenario struct {
	Msg      string          `json:"msg"`
	Requests []goldenRequest `json:"requests"`
	Error    *string         `json:"error"`
}

type goldenVariant struct {
	Config    M                         `json:"config"`
	Scenarios map[string]goldenScenario `json:"scenarios"`
}

type goldenFile struct {
	Provider string                   `json:"provider"`
	Variants map[string]goldenVariant `json:"variants"`
}

type goldenInputs struct {
	Now       float64 `json:"now"`
	Monitor   M       `json:"monitor"`
	Scenarios map[string]M
}

// stubResponse is the same response the recorder gave the original providers.
const stubResponse = `{"ok":true,"success":true,"status":"success","result":{"id":1},"code":0,"errcode":0,"errorCode":0,"ErrorCode":0,"Code":"OK","error":0,"errors":[],"access_token":"stub-access-token","token":"stub-token","accessToken":"stub-access-token","tenant_access_token":"stub-tenant-token","expires_in":3600,"id":"stub-id","data":{"id":"stub-id","token":"stub-token","ok":true},"message":"ok","msg":"ok","ret":0,"event_id":"stub","sid":"stub"}`

type recorder struct {
	mu       sync.Mutex
	requests []goldenRequest
}

func (r *recorder) RoundTrip(req *http.Request) (*http.Response, error) {
	captured := goldenRequest{Method: req.Method, URL: req.URL.String(), Headers: map[string]string{}}
	for name, values := range req.Header {
		captured.Headers[name] = values[0]
	}
	if req.Body != nil {
		data, _ := io.ReadAll(req.Body)
		text := string(data)
		captured.Body = &text
	}
	r.mu.Lock()
	r.requests = append(r.requests, captured)
	r.mu.Unlock()
	return &http.Response{
		StatusCode: 200, Status: "200 OK", Header: http.Header{},
		Body: io.NopCloser(strings.NewReader(stubResponse)),
	}, nil
}

// deviations are places where the original was broken and this port deliberately differs.
// Each of those has its own test in providers_test.go.
var deviations = map[string]string{
	"AlertNow":   "the original crashes on every recovery, and sends \"[undefined]\" as the name",
	"SIGNL4":     "the original sends \"UptimeKuma-undefined\" as the alert id",
	"SMSManager": "the original sends gateway=undefined",
}

// notPorted is what has no golden file for a good reason.
var notPorted = map[string]string{
	"nostr":   "needs a NIP-59 implementation, and a real relay to test it against",
	"Webpush": "needs browser subscriptions, which the web UI would have to set up against this service",
}

func loadInputs(t *testing.T) goldenInputs {
	t.Helper()
	data, err := os.ReadFile("testdata/golden/_inputs.json")
	if err != nil {
		t.Fatal(err)
	}
	var inputs goldenInputs
	if err := json.Unmarshal(data, &inputs); err != nil {
		t.Fatal(err)
	}
	return inputs
}

func testEnv(rec *recorder, now float64) *Env {
	return &Env{
		Client:  &http.Client{Transport: rec},
		BaseURL: "https://kuma.example",
		Now:     func() time.Time { return time.UnixMilli(int64(now)).UTC() },
		RandBytes: func(n int) []byte {
			return bytes.Repeat([]byte{0xab}, n)
		},
		RandFloat: func() float64 { return 0.5 },
	}
}

func clone(m M) M {
	if m == nil {
		return nil
	}
	data, _ := json.Marshal(m)
	var out M
	_ = json.Unmarshal(data, &out)
	return out
}

func TestParityWithNodeProviders(t *testing.T) {
	inputs := loadInputs(t)
	files, err := filepath.Glob("testdata/golden/*.json")
	if err != nil {
		t.Fatal(err)
	}
	covered, requests := 0, 0
	for _, path := range files {
		if strings.HasSuffix(path, "_inputs.json") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var golden goldenFile
		if err := json.Unmarshal(data, &golden); err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		t.Run(golden.Provider, func(t *testing.T) {
			if !Supported(golden.Provider) {
				t.Fatalf("not ported")
			}
			if _, deviates := deviations[golden.Provider]; deviates {
				t.Skip(deviations[golden.Provider])
			}
			covered++
			for variantName, variant := range golden.Variants {
				for scenarioName, scenario := range variant.Scenarios {
					message := Message{Text: scenario.Msg}
					if scenarioName != "test" {
						message.Monitor = clone(inputs.Monitor)
						message.Beat = clone(inputs.Scenarios[scenarioName])
					}
					rec := &recorder{}
					err := Send(context.Background(), testEnv(rec, inputs.Now), golden.Provider, clone(variant.Config), message)
					requests += len(rec.requests)
					label := variantName + "/" + scenarioName
					if len(rec.requests) != len(scenario.Requests) {
						t.Errorf("%s: sent %d requests, the original sent %d (err: %v, original err: %v)", label, len(rec.requests), len(scenario.Requests), err, deref(scenario.Error))
						continue
					}
					for i := range rec.requests {
						compareRequest(t, label, scenario.Requests[i], rec.requests[i])
					}
				}
			}
		})
	}
	t.Logf("%d providers and %d requests compared", covered, requests)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

var ignoredHeaders = map[string]bool{"accept": true, "user-agent": true, "content-length": true, "accept-encoding": true}

func compareRequest(t *testing.T, label string, want, got goldenRequest) {
	t.Helper()
	if want.Method != got.Method {
		t.Errorf("%s: method %s, want %s", label, got.Method, want.Method)
	}
	if a, b := normalURL(want.URL), normalURL(got.URL); a != b {
		t.Errorf("%s: url\n got %s\nwant %s", label, b, a)
	}
	wantHeaders, gotHeaders := normalHeaders(want.Headers), normalHeaders(got.Headers)
	if !reflect.DeepEqual(wantHeaders, gotHeaders) {
		t.Errorf("%s: headers\n got %v\nwant %v", label, gotHeaders, wantHeaders)
	}
	a, b := normalBody(want), normalBody(got)
	if !reflect.DeepEqual(a, b) {
		wantJSON, _ := json.Marshal(a)
		gotJSON, _ := json.Marshal(b)
		t.Errorf("%s: body\n got %s\nwant %s", label, gotJSON, wantJSON)
	}
}

func normalHeaders(headers map[string]string) map[string]string {
	out := map[string]string{}
	for name, value := range headers {
		lower := strings.ToLower(name)
		if ignoredHeaders[lower] {
			continue
		}
		if lower == "content-type" {
			value = strings.ToLower(strings.TrimSpace(strings.SplitN(value, ";", 2)[0]))
		}
		out[lower] = value
	}
	return out
}

func normalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := u.Query()
	keys := make([]string, 0, len(query))
	for key := range query {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var parts []string
	for _, key := range keys {
		for _, value := range query[key] {
			parts = append(parts, key+"="+normalJSONString(value))
		}
	}
	return u.Scheme + "://" + u.Host + u.Path + "?" + strings.Join(parts, "&")
}

// normalJSONString makes JSON inside a string comparable, because key order differs.
func normalJSONString(value string) string {
	if strings.HasPrefix(value, "{") {
		var parsed any
		if json.Unmarshal([]byte(value), &parsed) == nil {
			data, _ := json.Marshal(parsed)
			return string(data)
		}
	}
	return value
}

var boundaryLine = regexp.MustCompile(`^--(\S+)`)

func normalBody(req goldenRequest) any {
	if req.Body == nil {
		return nil
	}
	body := *req.Body
	var parsed any
	if json.Unmarshal([]byte(body), &parsed) == nil {
		return parsed
	}
	if m := boundaryLine.FindStringSubmatch(body); m != nil {
		fields := map[string]any{}
		reader := multipart.NewReader(strings.NewReader(body), m[1])
		for {
			part, err := reader.NextPart()
			if err != nil {
				break
			}
			data, _ := io.ReadAll(part)
			_, params, _ := mime.ParseMediaType(part.Header.Get("Content-Disposition"))
			fields[params["name"]] = normalJSONString(string(data))
		}
		return fields
	}
	if values, err := url.ParseQuery(body); err == nil && strings.Contains(body, "=") {
		return values
	}
	return body
}

func TestNotPortedIsAccurate(t *testing.T) {
	for name := range notPorted {
		if Supported(name) {
			t.Errorf("%s is listed as not ported, but is registered", name)
		}
		if _, err := os.Stat(filepath.Join("testdata/golden", name+".json")); err == nil {
			t.Errorf("%s has a golden file, so it should be ported", name)
		}
	}
}
