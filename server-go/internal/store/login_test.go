package store

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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
	gh, err := s.SaveGitHubUser(ctx, 7, "alice", RoleAdmin, "token", []string{"sre"}, LoginVersion{Env: true})
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
	if _, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-ann", Email: "ann@example.com"}, RoleAdmin, "r1", LoginVersion{Env: true}); err != ErrUsernameTakenByPassword {
		t.Fatalf("took over a password account: %v", err)
	}

	bob, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-bob", Email: "Bob@Example.com", Domain: "example.com"}, RoleViewer, "refresh-1", LoginVersion{Env: true})
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
	again, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-bob", Email: "bob@example.com", Domain: "example.com"}, RoleAdmin, "", LoginVersion{Env: true})
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
	if err := s.SetGoogleChecked(ctx, bob.ID, GoogleUser{Sub: "s-bob", Email: "bob@example.com"}, RoleViewer, LoginVersion{Env: true}); err != nil {
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
	carol, _ := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-carol", Email: "carol@example.com"}, RoleViewer, "", LoginVersion{Env: true})
	if _, err := s.GoogleToken(ctx, carol.ID); err != ErrNotFound {
		t.Fatalf("carol's token: %v", err)
	}
	// The email moving to someone else renames the old account.
	dave, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "s-dave", Email: "carol@example.com"}, RoleViewer, "", LoginVersion{Env: true})
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

func TestOIDCAccounts(t *testing.T) {
	s := newTestStore(t)
	ring, _ := secrets.New(bytes.Repeat([]byte{6}, 32))
	s.SetKeyring(ring)
	ctx := context.Background()
	if _, err := s.CreateFirstUser(ctx, "admin@example.com", "salt:hash"); err != nil {
		t.Fatal(err)
	}
	// A Google account's name isn't taken over either.
	if _, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "g1", Email: "sam@example.com"}, RoleViewer, "", LoginVersion{Env: true}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Admin@example.com", "sam@example.com"} {
		if _, err := s.SaveOIDCUser(ctx, OIDCUser{Key: "https://idp x" + name, Name: name}, RoleAdmin, "", LoginVersion{Env: true}); err != ErrUsernameTakenByPassword {
			t.Fatalf("%s taken over: %v", name, err)
		}
	}
	o := OIDCUser{Key: "https://idp u1", Name: "Ops@Example.com", Email: "ops@example.com", Groups: []string{"Admins"}}
	ops, err := s.SaveOIDCUser(ctx, o, RoleAdmin, "refresh-1", LoginVersion{Env: true})
	if err != nil || ops.Username != "ops@example.com" || *ops.OIDCSub != "https://idp u1" || ops.OIDCGroups[0] != "Admins" || !ops.SSO() {
		t.Fatalf("ops: %+v %v", ops, err)
	}
	var stored string
	_ = s.pool.QueryRow(ctx, `SELECT oidc_token FROM app_user WHERE id = $1`, ops.ID).Scan(&stored)
	if !secrets.IsSealed(stored) || strings.Contains(stored, "refresh-1") {
		t.Fatalf("stored token: %q", stored)
	}
	// A check that rotates the token keeps the new one; one without keeps the old.
	o.Groups = []string{"Support"}
	if err := s.SetOIDCChecked(ctx, ops.ID, o, RoleViewer, "refresh-2", LoginVersion{Env: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetOIDCChecked(ctx, ops.ID, o, RoleViewer, "", LoginVersion{Env: true}); err != nil {
		t.Fatal(err)
	}
	if token, _ := s.OIDCToken(ctx, ops.ID); token != "refresh-2" {
		t.Fatalf("token: %q", token)
	}
	if u, _ := s.UserByID(ctx, ops.ID); u.Role != RoleViewer || u.OIDCGroups[0] != "Support" || u.OIDCCheckedAt == nil {
		t.Fatalf("checked: %+v", u)
	}
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{OIDC: true}); ok {
		t.Fatal("a viewer counted as an admin")
	}
	if _, err := s.SaveOIDCUser(ctx, o, RoleAdmin, "", LoginVersion{Env: true}); err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{OIDC: true}); !ok {
		t.Fatal("the OpenID Connect admin didn't count")
	}
	if ok, _ := s.AdminCanLogIn(ctx, LoginMethods{Google: true}); ok {
		t.Fatal("an OpenID Connect admin counted for Google")
	}
	if err := s.ExpireOIDCChecks(ctx); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, ops.ID); u.OIDCCheckedAt != nil {
		t.Fatal("still checked")
	}
	if name, _, err := s.LastSSOLogin(ctx, "oidc"); err != nil || name != "" {
		t.Fatalf("last login: %q %v", name, err)
	}

	c := LoginConfig{PasswordLogin: true, OIDC: OIDCConfig{Enabled: true, Name: "Okta", Issuer: "https://idp", ClientID: "kuma",
		ClientSecret: "s3cret", Scopes: []string{"groups"}, GroupsClaim: "roles", AdminGroups: []string{"Admins"}}}
	if err := s.SaveLoginConfig(ctx, c); err != nil {
		t.Fatal(err)
	}
	got, err := s.LoginConfig(ctx)
	if err != nil || got.OIDC.Name != "Okta" || got.OIDC.ClientSecret != "s3cret" || got.OIDC.GroupsClaim != "roles" ||
		got.OIDC.AdminGroups[0] != "Admins" || len(got.OIDC.ViewerEmails) != 0 {
		t.Fatalf("settings: %+v %v", got.OIDC, err)
	}
	_ = s.pool.QueryRow(ctx, `SELECT oidc_client_secret FROM login_config`).Scan(&stored)
	if !secrets.IsSealed(stored) {
		t.Fatalf("secret stored as %q", stored)
	}

	// A name that isn't a verified email, such as a preferred username a user can set, can't take
	// another account's.
	pat, err := s.SaveOIDCUser(ctx, OIDCUser{Key: "https://idp u2", Name: "pat"}, RoleViewer, "", LoginVersion{Env: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveOIDCUser(ctx, OIDCUser{Key: "https://idp u3", Name: "Pat"}, RoleViewer, "", LoginVersion{Env: true}); err != ErrUsernameTakenByPassword {
		t.Fatalf("a preferred username took another account's: %v", err)
	}
	if u, _ := s.UserByID(ctx, pat.ID); u.Username != "pat" {
		t.Fatalf("renamed to %q", u.Username)
	}
	// A verified email moves to its new owner, and the account that had it is renamed.
	lee, err := s.SaveOIDCUser(ctx, OIDCUser{Key: "https://idp u4", Name: "lee@example.com", Email: "lee@example.com"}, RoleViewer, "", LoginVersion{Env: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SaveOIDCUser(ctx, OIDCUser{Key: "https://idp u5", Name: "lee@example.com", Email: "lee@example.com"}, RoleViewer, "", LoginVersion{Env: true}); err != nil {
		t.Fatal(err)
	}
	if u, _ := s.UserByID(ctx, lee.ID); u.Username != fmt.Sprintf("lee@example.com#%d", lee.ID) {
		t.Fatalf("the old owner is %q", u.Username)
	}
}

func TestLoginVersion(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	gh, err := s.SaveGitHubUser(ctx, 9, "alice", RoleAdmin, "token", []string{"sre"}, LoginVersion{Env: true})
	if err != nil {
		t.Fatal(err)
	}
	// Before any save, the version without a time is current, and environment settings always are.
	none := LoginVersion{}
	if changed, err := s.LoginChanged(ctx, none); err != nil || changed {
		t.Fatalf("before a save: %v %v", changed, err)
	}
	if err := s.SaveLoginConfig(ctx, LoginConfig{PasswordLogin: true}); err != nil {
		t.Fatal(err)
	}
	saved, _ := s.LoginConfig(ctx)
	v1 := LoginVersion{SavedAt: saved.UpdatedAt}
	for name, v := range map[string]LoginVersion{"no save": none, "env": {Env: true}, "v1": v1} {
		changed, err := s.LoginChanged(ctx, v)
		if err != nil || changed != (name == "no save") {
			t.Fatalf("%s: changed %v %v", name, changed, err)
		}
	}
	// A check made with replaced settings isn't recorded; one with the current settings is.
	time.Sleep(10 * time.Millisecond)
	if err := s.SaveLoginConfig(ctx, LoginConfig{PasswordLogin: true, GitHubEnabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGitHubChecked(ctx, gh.ID, RoleViewer, nil, v1); !errors.Is(err, ErrLoginChanged) {
		t.Fatalf("a stale check was recorded: %v", err)
	}
	if u, _ := s.UserByID(ctx, gh.ID); u.Role != RoleAdmin {
		t.Fatalf("role after a stale check: %s", u.Role)
	}
	saved, _ = s.LoginConfig(ctx)
	if err := s.SetGitHubChecked(ctx, gh.ID, RoleViewer, nil, LoginVersion{SavedAt: saved.UpdatedAt}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetGitHubChecked(ctx, gh.ID, RoleViewer, nil, LoginVersion{Env: true}); err != nil {
		t.Fatal(err)
	}

	// A login whose settings were replaced while it ran saves no account, for each provider.
	if _, err := s.SaveGitHubUser(ctx, 10, "bob", RoleAdmin, "token", nil, v1); !errors.Is(err, ErrLoginChanged) {
		t.Fatalf("GitHub login with replaced settings: %v", err)
	}
	if _, err := s.SaveGoogleUser(ctx, GoogleUser{Sub: "g-bob", Email: "bob@example.com"}, RoleAdmin, "", v1); !errors.Is(err, ErrLoginChanged) {
		t.Fatalf("Google login with replaced settings: %v", err)
	}
	if _, err := s.SaveOIDCUser(ctx, OIDCUser{Key: "https://idp bob", Name: "bob"}, RoleAdmin, "", v1); !errors.Is(err, ErrLoginChanged) {
		t.Fatalf("OpenID Connect login with replaced settings: %v", err)
	}
	var accounts int
	_ = s.pool.QueryRow(ctx, `SELECT count(*) FROM app_user`).Scan(&accounts)
	if accounts != 1 {
		t.Fatalf("accounts saved with replaced settings: %d", accounts)
	}

	// A save of the settings waits for a login saving its account, so that the expiry after the
	// save reaches the account: the login holds the lock until it commits.
	current := LoginVersion{SavedAt: saved.UpdatedAt}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := loginHeld(ctx, tx, current); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.SaveLoginConfig(ctx, LoginConfig{PasswordLogin: true}) }()
	select {
	case err := <-done:
		t.Fatalf("the settings were saved during a login's save: %v", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// And a login that then saves with the settings it read before is refused.
	if _, err := s.SaveGitHubUser(ctx, 10, "bob", RoleAdmin, "token", nil, current); !errors.Is(err, ErrLoginChanged) {
		t.Fatalf("a login after the save: %v", err)
	}
}
