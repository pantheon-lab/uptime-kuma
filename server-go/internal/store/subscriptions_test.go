package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
)

func TestSubscribers(t *testing.T) {
	s := newTestStore(t)
	ring, _ := secrets.New(bytes.Repeat([]byte{3}, 32))
	s.SetKeyring(ring)
	ctx := context.Background()
	page, err := s.CreateStatusPage(ctx, "Public", "public")
	if err != nil {
		t.Fatal(err)
	}
	other, err := s.CreateStatusPage(ctx, "Other", "other")
	if err != nil {
		t.Fatal(err)
	}

	// One subscription per address, whatever its case; the same address on another page is another.
	sub, added, err := s.AddEmailSubscriber(ctx, page.ID, "Ann@Example.com", "token-a")
	if err != nil || !added || sub.Confirmed || sub.Token != "token-a" {
		t.Fatalf("add: %+v %v %v", sub, added, err)
	}
	again, added, err := s.AddEmailSubscriber(ctx, page.ID, "ann@example.com", "token-b")
	if err != nil || added || again.ID != sub.ID || again.Token != "token-a" {
		t.Fatalf("again: %+v %v %v", again, added, err)
	}
	if _, added, err := s.AddEmailSubscriber(ctx, other.ID, "ann@example.com", "token-c"); err != nil || !added {
		t.Fatalf("other page: %v %v", added, err)
	}

	// A confirmation goes once in the interval.
	if ok, err := s.ClaimConfirmation(ctx, sub.ID, time.Hour); err != nil || !ok {
		t.Fatalf("first claim: %v %v", ok, err)
	}
	if ok, _ := s.ClaimConfirmation(ctx, sub.ID, time.Hour); ok {
		t.Fatal("claimed twice within the interval")
	}
	if ok, _ := s.ClaimConfirmation(ctx, sub.ID, 0); !ok {
		t.Fatal("not claimed after the interval")
	}

	// Tokens only work on their page.
	if _, err := s.ConfirmSubscriber(ctx, other.ID, "token-a", time.Hour); !errors.Is(err, ErrNotFound) {
		t.Fatalf("confirmed on another page: %v", err)
	}
	// An expired link doesn't confirm, even before the subscriber is deleted.
	if _, err := s.ConfirmSubscriber(ctx, page.ID, "token-a", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("confirmed with an expired link: %v", err)
	}
	confirmed, err := s.ConfirmSubscriber(ctx, page.ID, "token-a", time.Hour)
	if err != nil || !confirmed.Confirmed {
		t.Fatalf("confirm: %+v %v", confirmed, err)
	}
	if ok, _ := s.ClaimConfirmation(ctx, sub.ID, 0); ok {
		t.Fatal("a confirmed subscriber was sent a confirmation")
	}

	// Webhooks are sealed, confirmed, and one per URL.
	hook, err := s.AddWebhookSubscriber(ctx, page.ID, "https://hooks.example/secret-token", "token-w")
	if err != nil || !hook.Confirmed {
		t.Fatalf("webhook: %+v %v", hook, err)
	}
	if _, err := s.AddWebhookSubscriber(ctx, page.ID, "https://hooks.example/secret-token", "token-x"); !errors.Is(err, ErrSubscriberExists) {
		t.Fatalf("duplicate webhook: %v", err)
	}
	var stored string
	if err := s.pool.QueryRow(ctx, `SELECT address FROM status_page_subscriber WHERE id = $1`, hook.ID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "secret-token") || !secrets.IsSealed(stored) {
		t.Fatalf("webhook stored as %q", stored)
	}

	list, err := s.Subscribers(ctx, page.ID)
	if err != nil || len(list) != 2 || list[0].Kind != SubscriberEmail || list[1].Address != "https://hooks.example/secret-token" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if n, _ := s.CountSubscribers(ctx, page.ID); n != 2 {
		t.Fatalf("count: %d", n)
	}

	if err := s.RecordDelivery(ctx, hook.ID, "502 Bad Gateway"); err != nil {
		t.Fatal(err)
	}
	list, _ = s.Subscribers(ctx, page.ID)
	if list[1].LastSentAt == nil || list[1].LastError != "502 Bad Gateway" {
		t.Fatalf("delivery: %+v", list[1])
	}

	// Unconfirmed ones go after a while; confirmed ones stay.
	if err := s.DeleteUnconfirmedSubscribers(ctx, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n, _ := s.CountSubscribers(ctx, other.ID); n != 0 {
		t.Fatalf("unconfirmed left: %d", n)
	}
	if n, _ := s.CountSubscribers(ctx, page.ID); n != 2 {
		t.Fatalf("confirmed removed: %d", n)
	}

	if _, err := s.RemoveSubscriberByToken(ctx, other.ID, "token-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unsubscribed on another page: %v", err)
	}
	if removed, err := s.RemoveSubscriberByToken(ctx, page.ID, "token-a"); err != nil || removed.Address != "Ann@Example.com" {
		t.Fatalf("unsubscribe: %+v %v", removed, err)
	}
	if err := s.RemoveSubscriber(ctx, other.ID, hook.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("removed from another page: %v", err)
	}
	if err := s.RemoveSubscriber(ctx, page.ID, hook.ID); err != nil {
		t.Fatal(err)
	}

	// The page's sender, which goes when its notification does.
	n, err := s.CreateNotification(ctx, Notification{Name: "Mail", Type: "smtp", Config: map[string]any{"smtpHost": "mail"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetStatusPageSubscriptions(ctx, page.ID, &n.ID); err != nil {
		t.Fatal(err)
	}
	got, _ := s.StatusPageBySlug(ctx, "public")
	if got.SubscribeNotificationID == nil || *got.SubscribeNotificationID != n.ID {
		t.Fatalf("sender: %v", got.SubscribeNotificationID)
	}
	// Saving the page's settings keeps it.
	if _, err := s.SaveStatusPage(ctx, "public", got, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.StatusPageBySlug(ctx, "public"); got.SubscribeNotificationID == nil {
		t.Fatal("saving the page dropped the sender")
	}
	if err := s.DeleteNotification(ctx, n.ID); err != nil {
		t.Fatal(err)
	}
	if got, _ = s.StatusPageBySlug(ctx, "public"); got.SubscribeNotificationID != nil {
		t.Fatalf("sender kept after its notification went: %v", *got.SubscribeNotificationID)
	}

	// Deleting the page deletes its subscribers.
	if _, _, err := s.AddEmailSubscriber(ctx, page.ID, "bob@example.com", "token-d"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteStatusPage(ctx, "public"); err != nil {
		t.Fatal(err)
	}
	var left int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM status_page_subscriber`).Scan(&left)
	if left != 0 {
		t.Fatalf("subscribers left: %d", left)
	}
}

func TestIncidentTemplates(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	a, err := s.SaveIncidentTemplate(ctx, IncidentTemplate{Title: "Investigating", Content: "Looking", Style: "warning"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveIncidentTemplate(ctx, IncidentTemplate{Title: "database down", Content: "DB", Style: "danger"}); err != nil {
		t.Fatal(err)
	}
	// The same title replaces it.
	b, err := s.SaveIncidentTemplate(ctx, IncidentTemplate{Title: "Investigating", Content: "Still looking", Style: "info"})
	if err != nil || b.ID != a.ID {
		t.Fatalf("replace: %+v %v", b, err)
	}
	list, err := s.IncidentTemplates(ctx)
	if err != nil || len(list) != 2 || list[0].Title != "database down" || list[1].Content != "Still looking" || list[1].Style != "info" {
		t.Fatalf("list: %+v %v", list, err)
	}
	if n, _ := s.CountIncidentTemplates(ctx); n != 2 {
		t.Fatalf("count: %d", n)
	}
	if err := s.DeleteIncidentTemplate(ctx, a.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteIncidentTemplate(ctx, a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted twice: %v", err)
	}
}
