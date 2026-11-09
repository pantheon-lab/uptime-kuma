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
	if name, _, err := s.LastGitHubLogin(ctx); err != nil || name != "" {
		t.Fatalf("no login yet: %q %v", name, err)
	}

	// Without the key, the sealed secret can't be read.
	s.SetKeyring(nil)
	if _, err := s.LoginConfig(ctx); err == nil {
		t.Fatal("a sealed secret opened without a key")
	}
}
