package store

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
)

func TestStoredSecrets(t *testing.T) {
	s := newTestStore(t)
	s.SetKeyring(nil) // It starts without a key, whatever TEST_SECRET_KEY says.
	ctx := context.Background()
	key1, key2 := bytes.Repeat([]byte{1}, 32), bytes.Repeat([]byte{2}, 32)
	ring1, _ := secrets.New(key1)

	// Secrets stored before there was a key.
	user, err := s.CreateFirstUser(ctx, "admin", "salt:hash")
	if err != nil {
		t.Fatal(err)
	}
	if err := s.StartTOTPSetup(ctx, user.ID, []byte("totp-plain-key")); err != nil {
		t.Fatal(err)
	}
	gh, err := s.SaveGitHubUser(ctx, 5, "octocat", RoleAdmin, "gho-plain-token", nil)
	if err != nil {
		t.Fatal(err)
	}
	n, err := s.CreateNotification(ctx, Notification{Name: "Slack", Type: "slack",
		Config: map[string]any{"slackwebhookURL": "https://hooks.example/plain-hook"}}, false)
	if err != nil {
		t.Fatal(err)
	}
	proxy, _, err := s.SaveProxy(ctx, check.Proxy{Protocol: "http", Host: "proxy", Port: 3128, Auth: true,
		Username: "u", Password: "proxy-plain-pass", Active: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.WebpushKeys(ctx, func() (string, string, error) { return "pub", "vapid-plain-private", nil }); err != nil {
		t.Fatal(err)
	}
	plains := []string{"totp-plain-key", "gho-plain-token", "plain-hook", "proxy-plain-pass", "vapid-plain-private"}

	// The first start with a key encrypts them all.
	s.SetKeyring(ring1)
	report, err := s.SealSecrets(ctx, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Sealed) != 5 {
		t.Fatalf("sealed: %v", report.Sealed)
	}
	if again, err := s.SealSecrets(ctx, false); err != nil || len(again.Sealed)+len(again.Resealed) != 0 {
		t.Fatalf("a second start changed something: %+v %v", again, err)
	}
	dump := func() string {
		var out string
		err := s.pool.QueryRow(ctx, `SELECT concat_ws(' ',
			(SELECT string_agg(encode(totp_secret, 'escape') || ' ' || coalesce(github_token, ''), ' ') FROM app_user),
			(SELECT string_agg(config::text, ' ') FROM notification),
			(SELECT string_agg(password, ' ') FROM proxy),
			(SELECT string_agg(value::text, ' ') FROM app_setting))`).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, plain := range plains {
		if strings.Contains(dump(), plain) {
			t.Errorf("%s is still stored as it is", plain)
		}
	}

	// Reading gives the values back.
	wantToken := "gho-plain-token"
	checkReads := func(stage string) {
		t.Helper()
		if u, err := s.UserByID(ctx, user.ID); err != nil || string(u.TOTPSecret) != "totp-plain-key" {
			t.Errorf("%s: authenticator key: %q %v", stage, u.TOTPSecret, err)
		}
		if token, err := s.GitHubToken(ctx, gh.ID); err != nil || token != wantToken {
			t.Errorf("%s: GitHub token: %q %v", stage, token, err)
		}
		if got, err := s.GetNotification(ctx, n.ID); err != nil || got.Config["slackwebhookURL"] != "https://hooks.example/plain-hook" {
			t.Errorf("%s: notification: %v %v", stage, got.Config, err)
		}
		if got, err := s.Proxy(ctx, proxy.ID); err != nil || got.Password != "proxy-plain-pass" {
			t.Errorf("%s: proxy: %q %v", stage, got.Password, err)
		}
		if _, private, err := s.WebpushKeys(ctx, nil); err != nil || private != "vapid-plain-private" {
			t.Errorf("%s: VAPID key: %q %v", stage, private, err)
		}
	}
	checkReads("sealed")

	// What's written with a key is sealed, and still works.
	if err := s.EnableTOTP(ctx, user.ID, []byte("wrong-key"), 1, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("enable with another key: %v", err)
	}
	if err := s.EnableTOTP(ctx, user.ID, []byte("totp-plain-key"), 1, nil); err != nil {
		t.Errorf("enable two-factor login: %v", err)
	}
	n2, err := s.CreateNotification(ctx, Notification{Name: "Other", Type: "slack",
		Config: map[string]any{"slackwebhookURL": "https://hooks.example/new-hook"}}, false)
	if err != nil || n2.Config["slackwebhookURL"] != "https://hooks.example/new-hook" {
		t.Fatalf("new notification: %v %v", n2, err)
	}
	if _, err := s.UpdateNotification(ctx, n2, false); err != nil {
		t.Fatal(err)
	}
	if id, err := s.ImportProxy(ctx, check.Proxy{Protocol: "http", Host: "proxy", Port: 3128, Auth: true, Username: "u", Password: "proxy-plain-pass"}); err != nil || id != proxy.ID {
		t.Errorf("import the same proxy: %d %v", id, err)
	}
	if id, err := s.ImportProxy(ctx, check.Proxy{Protocol: "http", Host: "proxy", Port: 3128, Auth: true, Username: "u", Password: "other"}); err != nil || id == proxy.ID {
		t.Errorf("import a proxy with another password: %d %v", id, err)
	}
	if _, err := s.SaveGitHubUser(ctx, 5, "octocat", RoleAdmin, "gho-new-token", nil); err != nil {
		t.Fatal(err)
	}
	wantToken = "gho-new-token"
	if token, err := s.GitHubToken(ctx, gh.ID); err != nil || token != "gho-new-token" {
		t.Errorf("new GitHub token: %q %v", token, err)
	}
	for _, plain := range []string{"new-hook", "gho-new-token", "other"} {
		if strings.Contains(dump(), plain) {
			t.Errorf("%s was stored as it is", plain)
		}
	}
	if err := s.StartTOTPSetup(ctx, gh.ID, []byte("x")); err != nil {
		t.Fatal(err)
	}

	// A value copied to another row doesn't open there.
	if _, err := s.pool.Exec(ctx, `UPDATE notification SET config = (SELECT config FROM notification WHERE id = $1) WHERE id = $2`, n.ID, n2.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetNotification(ctx, n2.ID); err == nil {
		t.Error("a notification's settings opened in another row")
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM notification WHERE id = $1`, n2.ID); err != nil {
		t.Fatal(err)
	}

	// Without the key, the server refuses to start.
	s.SetKeyring(nil)
	if _, err := s.SealSecrets(ctx, false); !errors.Is(err, secrets.ErrNoKey) {
		t.Fatalf("no key: %v", err)
	}

	// Rotation: the new key with the old one, rekey, then the new key alone.
	ring2, _ := secrets.New(key2, key1)
	s.SetKeyring(ring2)
	if report, err := s.SealSecrets(ctx, false); err != nil || len(report.Resealed) != 0 {
		t.Fatalf("a start with a new key resealed: %+v %v", report, err)
	}
	report, err = s.SealSecrets(ctx, true)
	if err != nil || report.Resealed["notification settings"] != 1 || report.Resealed["authenticator keys"] != 2 {
		t.Fatalf("rekey: %+v %v", report, err)
	}
	only2, _ := secrets.New(key2)
	s.SetKeyring(only2)
	if _, err := s.SealSecrets(ctx, false); err != nil {
		t.Fatalf("after rekey, the old key is still needed: %v", err)
	}
	checkReads("rekeyed")
	only1, _ := secrets.New(key1)
	s.SetKeyring(only1)
	if _, err := s.SealSecrets(ctx, false); !errors.Is(err, secrets.ErrUnknownKey) {
		t.Fatalf("the old key alone: %v", err)
	}
}
