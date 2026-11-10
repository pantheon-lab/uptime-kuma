package ui

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// A webhook's redirect can repeat its token, and the error mustn't.
func TestWebhookRedirectErrorHidesTheToken(t *testing.T) {
	s, _, _ := newTestServer(t)
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://[bad/secret-token")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(hook.Close)
	d := delivery{page: store.StatusPage{Slug: "main", Title: "Main"}, incident: &store.Incident{}, event: incidentCreated}
	err := s.sendIncidentWebhook(context.Background(), d, store.Subscriber{Kind: store.SubscriberWebhook, Address: hook.URL + "/secret-token"}, "http://kuma")
	if err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatalf("error: %v", err)
	}
}
