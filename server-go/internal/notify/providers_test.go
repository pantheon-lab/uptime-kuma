package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type capture struct {
	url  string
	body string
}

func captureEnv(status int, responseBody string) (*Env, *capture) {
	seen := &capture{}
	env := DefaultEnv("https://kuma.example")
	env.Now = func() time.Time { return time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC) }
	env.Client = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		seen.url = r.URL.String()
		if r.Body != nil {
			data, _ := io.ReadAll(r.Body)
			seen.body = string(data)
		}
		return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(responseBody)), Request: r}, nil
	})}
	return env, seen
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func beatMessage(status int) Message {
	return Message{
		Text:    "[Site] msg",
		Monitor: M{"id": 7, "name": "My Site", "type": "http"},
		Beat:    M{"monitorID": 7, "status": status, "msg": "msg", "time": "2026-01-02 03:04:05.000"},
	}
}

// The original crashes on an UP beat, so this is a deliberate difference.
func TestAlertNowHandlesRecovery(t *testing.T) {
	env, seen := captureEnv(200, "{}")
	if err := Send(context.Background(), env, "AlertNow", M{"alertNowWebhookURL": "https://alert.example/h"}, beatMessage(statusUp)); err != nil {
		t.Fatal(err)
	}
	var body M
	if err := json.Unmarshal([]byte(seen.body), &body); err != nil {
		t.Fatal(err)
	}
	if body["status"] != "close" || !strings.HasPrefix(body["summary"].(string), "[My Site] ✅") {
		t.Errorf("up: %v", body)
	}
	if body["event_id"] != "20260102_MySite" {
		t.Errorf("event id: %v", body["event_id"])
	}
}

// The original sends UptimeKuma-undefined, so that an alert can't be matched with its recovery.
func TestSignl4UsesAStableExternalID(t *testing.T) {
	ids := map[int]string{}
	for _, status := range []int{statusDown, statusUp} {
		env, seen := captureEnv(200, "{}")
		if err := Send(context.Background(), env, "SIGNL4", M{"webhookURL": "https://connect.signl4.com/x"}, beatMessage(status)); err != nil {
			t.Fatal(err)
		}
		var body M
		if err := json.Unmarshal([]byte(seen.body), &body); err != nil {
			t.Fatal(err)
		}
		ids[status], _ = body["X-S4-ExternalID"].(string)
	}
	if ids[statusDown] != ids[statusUp] || strings.Contains(ids[statusDown], "undefined") || ids[statusDown] == "" {
		t.Errorf("external ids: %v", ids)
	}
}

// The original reads a field that doesn't exist, and sends gateway=undefined.
func TestSMSManagerSendsTheGateway(t *testing.T) {
	env, seen := captureEnv(200, "{}")
	config := M{"smsmanagerApiKey": "k", "numbers": "420", "messageType": "high"}
	if err := Send(context.Background(), env, "SMSManager", config, beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(seen.url, "gateway=high") {
		t.Errorf("url: %s", seen.url)
	}
}

func TestProvidersAreRegistered(t *testing.T) {
	names := Providers()
	if len(names) < 100 {
		t.Errorf("only %d providers", len(names))
	}
	for _, name := range []string{"webhook", "smtp", "slack", "telegram", "discord", "PagerDuty"} {
		if !Supported(name) {
			t.Errorf("%s isn't supported", name)
		}
	}
	if Supported("nope") || Supported("") {
		t.Error("unknown types must not be supported")
	}
}

func TestUnknownProviderFails(t *testing.T) {
	if err := Send(context.Background(), DefaultEnv(""), "nope", M{}, Message{Text: "x"}); err == nil {
		t.Fatal("expected an error")
	}
}

// Webhook URLs and bot tokens are credentials, and errors end up in logs and API responses.
func TestErrorsDontLeakURLs(t *testing.T) {
	const secret = "s3cr3t-token"
	configs := map[string]M{
		"webhook":  {"webhookURL": "http://127.0.0.1:1/" + secret, "webhookContentType": "json"},
		"telegram": {"telegramBotToken": secret, "telegramChatID": "1"},
		"discord":  {"discordWebhookUrl": "http://127.0.0.1:1/" + secret},
		"slack":    {"slackwebhookURL": "http://127.0.0.1:1/" + secret},
	}
	for kind, config := range configs {
		t.Run(kind, func(t *testing.T) {
			env := DefaultEnv("")
			env.Client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("connection refused")
			})}
			err := Send(context.Background(), env, kind, config, beatMessage(statusDown))
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("leaked: %v", err)
			}
		})
	}
}

func TestWebhookEndToEnd(t *testing.T) {
	var received M
	var header string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header = r.Header.Get("X-Team")
		_ = json.NewDecoder(r.Body).Decode(&received)
	}))
	defer server.Close()
	config := M{"webhookURL": server.URL, "webhookContentType": "json", "webhookAdditionalHeaders": `{"X-Team":"ops"}`}
	if err := Send(context.Background(), DefaultEnv(""), "webhook", config, beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	if received["msg"] != "[Site] msg" || received["heartbeat"] == nil || received["monitor"] == nil || header != "ops" {
		t.Errorf("received %v, header %q", received, header)
	}
}

func TestWebhookReportsRejection(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()
	err := Send(context.Background(), DefaultEnv(""), "webhook", M{"webhookURL": server.URL, "webhookContentType": "json"}, Message{Text: "x"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != http.StatusForbidden {
		t.Fatalf("got %v", err)
	}
}
