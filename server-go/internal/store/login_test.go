package store

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
)

func TestLoginConfig(t *testing.T) {
	s := newTestStore(t)
	ring, _ := secrets.New(bytes.Repeat([]byte{4}, 32))
	s.SetKeyring(ring)
	ctx := context.Background()

	c, err := s.LoginConfig(ctx)
	if err != nil || !c.PasswordLogin || c.GitHubEnabled || c.UpdatedAt != nil {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	want := LoginConfig{GitHubEnabled: true, ClientID: "client", ClientSecret: "very-secret", Org: "pantheon-lab",
		AdminTeams: []string{"sre"}, PasswordLogin: false}
	if err := s.SaveLoginConfig(ctx, want); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoginConfig(ctx)
	if err != nil || got.ClientSecret != "very-secret" || got.Org != "pantheon-lab" || len(got.ViewerTeams) != 0 ||
		got.AdminTeams[0] != "sre" || got.PasswordLogin || got.UpdatedAt == nil {
		t.Fatalf("saved: %+v %v", got, err)
	}
	var stored string
	_ = s.pool.QueryRow(ctx, `SELECT github_client_secret FROM login_config`).Scan(&stored)
	if strings.Contains(stored, "very-secret") || !secrets.IsSealed(stored) {
		t.Fatalf("stored secret: %q", stored)
	}
	// A second save replaces the row.
	want.PasswordLogin = true
	if err := s.SaveLoginConfig(ctx, want); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.LoginConfig(ctx); !got.PasswordLogin {
		t.Fatal("not replaced")
	}

	// Admins who can log in, by method.
	if _, err := s.CreateFirstUser(ctx, "admin", "salt:hash"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{Password: false, GitHub: true}); ok {
		t.Fatal("a password admin counted for GitHub")
	}
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{Password: true}); !ok {
		t.Fatal("the password admin didn't count")
	}
	gh, err := s.SaveGitHubUser(ctx, 7, "alice", RoleAdmin, "token", []string{"sre"})
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{GitHub: true}); !ok {
		t.Fatal("the GitHub admin didn't count")
	}
	if err := s.ExpireGitHubChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, gh.ID); u.GitHubCheckedAt != nil {
		t.Fatal("still checked")
	}
	if name, _, err := s.LastSSOLogin(ctx, "github"); err != nil || name != "" {
		t.Fatalf("no login yet: %q %v", name, err)
	}

	// Without the key, the sealed secret can't be read.
	s.SetKeyring(nil)
	if _, err := s.LoginConfig(ctx); err == nil {
		t.Fatal("a sealed secret opened without a key")
	}
}

func TestGoogleAccounts(t *testing.T) {
	s := newTestStore(t)
	ring, _ := secrets.New(bytes.Repeat([]byte{5}, 32))
	s.SetKeyring(ring)
	ctx := context.Background()

	// A password account named by the email isn't taken over, whatever the case.
	if _, err := s.CreateFirstUser(ctx, "Ann@Example.com", "salt:hash"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-ann", Email: "ann@example.com"}, RoleAdmin, "r1"); err != ErrUsernameTakenByPassword {
		t.Fatalf("took over a password account: %v", err)
	}

	bob, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-bob", Email: "Bob@Example.com", Domain: "example.com"}, RoleViewer, "refresh-1")
	if err != nil || bob.Username != "bob@example.com" || *bob.GoogleSub != "s-bob" || *bob.GoogleDomain != "example.com" || !bob.SSO() {
		t.Fatalf("bob: %+v %v", bob, err)
	}
	if token, err := s.GoogleToken(ctx, bob.ID); err != nil || token != "refresh-1" {
		t.Fatalf("token: %q %v", token, err)
	}
	var stored string
	_ = s.pool.QueryRow(ctx, `SELECT google_token FROM app_user WHERE id = $1`, bob.ID).Scan(&stored)
	if strings.Contains(stored, "refresh-1") || !secrets.IsSealed(stored) {
		t.Fatalf("stored token: %q", stored)
	}
	// A later login without a refresh token keeps the one there is, and updates the role.
	again, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-bob", Email: "bob@example.com", Domain: "example.com"}, RoleAdmin, "")
	if err != nil || again.ID != bob.ID || again.Role != RoleAdmin {
		t.Fatalf("again: %+v %v", again, err)
	}
	if token, _ := s.GoogleToken(ctx, bob.ID); token != "refresh-1" {
		t.Fatalf("token after a login without one: %q", token)
	}
	// Google accounts count as Google's admins, not password ones; their role isn't set here.
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{Google: true}); !ok {
		t.Fatal("the Google admin didn't count")
	}
	if _, err := s.SetUserRole(ctx, bob.ID, RoleViewer, LoginMethods{Password: true, Google: true}); err != ErrRoleFromGitHub {
		t.Fatalf("set a Google account's role: %v", err)
	}
	if err := s.SetGoogleChecked(ctx, bob.ID, GoogleUser{Sub: "s-bob", Email: "bob@example.com"}, RoleViewer); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, bob.ID); u.Role != RoleViewer || u.GoogleCheckedAt == nil || *u.GoogleDomain != "" {
		t.Fatalf("checked: %+v", u)
	}
	if err := s.ExpireGoogleChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, bob.ID); u.GoogleCheckedAt != nil {
		t.Fatal("still checked")
	}
	// A Google account without a token has none.
	carol, _ := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-carol", Email: "carol@example.com"}, RoleViewer, "")
	if _, err := s.GoogleToken(ctx, carol.ID); err != ErrNotFound {
		t.Fatalf("carol's token: %v", err)
	}
	// The email moving to someone else renames the old account.
	dave, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-dave", Email: "carol@example.com"}, RoleViewer, "")
	if err != nil || dave.ID == carol.ID {
		t.Fatalf("dave: %+v %v", dave, err)
	}
	if u, _ := s.UserByID(ctx, carol.ID); !strings.HasPrefix(u.Username, "carol@example.com#") {
		t.Fatalf("carol renamed: %q", u.Username)
	}

	// Settings round trip, with the Google secret sealed.
	c := LoginConfig{PasswordLogin: true, Google: GoogleConfig{Enabled: true, ClientID: "gid", ClientSecret: "gsecret",
		Domains: []string{"example.com"}, ViewerEmails: []string{"*"}}}
	if err := s.SaveLoginConfig(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoginConfig(ctx)
	if err != nil || !got.Google.Enabled || got.Google.ClientSecret != "gsecret" || got.Google.Domains[0] != "example.com" ||
		len(got.Google.AdminEmails) != 0 {
		t.Fatalf("google settings: %+v %v", got.Google, err)
	}
	_ = s.pool.QueryRow(ctx, `SELECT google_client_secret FROM login_config`).Scan(&stored)
	if !secrets.IsSealed(stored) {
		t.Fatalf("google secret stored as %q", stored)
	}
}
