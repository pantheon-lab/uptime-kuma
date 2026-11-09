package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// LoginConfig is how people log in, as set in the UI.
type LoginConfig struct {
	GitHubEnabled bool
	ClientID      string
	ClientSecret  string
	Org           string
	AdminTeams    []string
	ViewerTeams   []string
	// WebURL and APIURL are a GitHub Enterprise server's, or empty for github.com.
	WebURL        string
	APIURL        string
	PasswordLogin bool

	Google GoogleConfig
	// UpdatedAt is nil until the first save.
	UpdatedAt *time.Time
}

// GoogleConfig is logging in with Google.
type GoogleConfig struct {
	Enabled      bool
	ClientID     string
	ClientSecret string
	// Domains are the Google Workspace domains whose people a viewer email of "*" lets in.
	Domains      []string
	AdminEmails  []string
	ViewerEmails []string
}

var (
	loginSecretAAD  = secretAAD("login_config", "github_client_secret", 1)
	googleSecretAAD = secretAAD("login_config", "google_client_secret", 1)
)

// LoginConfig returns the login settings saved in the UI, or the defaults: passwords only.
func (s *Store) LoginConfig(ctx context.Context) (LoginConfig, error) {
	var c LoginConfig
	var updated time.Time
	err := s.pool.QueryRow(ctx, `SELECT github_enabled, github_client_id, github_client_secret, github_org,
		github_admin_teams, github_viewer_teams, github_web_url, github_api_url, password_login, updated_at,
		google_enabled, google_client_id, google_client_secret, google_domains, google_admin_emails, google_viewer_emails
		FROM login_config WHERE id = 1`).Scan(&c.GitHubEnabled, &c.ClientID, &c.ClientSecret, &c.Org,
		&c.AdminTeams, &c.ViewerTeams, &c.WebURL, &c.APIURL, &c.PasswordLogin, &updated,
		&c.Google.Enabled, &c.Google.ClientID, &c.Google.ClientSecret, &c.Google.Domains, &c.Google.AdminEmails,
		&c.Google.ViewerEmails)
	if errors.Is(err, pgx.ErrNoRows) {
		return LoginConfig{PasswordLogin: true}, nil
	}
	if err != nil {
		return c, fmt.Errorf("read login settings: %w", err)
	}
	c.UpdatedAt = &updated
	if c.ClientSecret != "" {
		if c.ClientSecret, err = s.openText(c.ClientSecret, loginSecretAAD); err != nil {
			return c, fmt.Errorf("login settings: %w", err)
		}
	}
	if c.Google.ClientSecret != "" {
		if c.Google.ClientSecret, err = s.openText(c.Google.ClientSecret, googleSecretAAD); err != nil {
			return c, fmt.Errorf("login settings: %w", err)
		}
	}
	return c, nil
}

// SaveLoginConfig replaces the login settings.
func (s *Store) SaveLoginConfig(ctx context.Context, c LoginConfig) error {
	secret := c.ClientSecret
	if secret != "" {
		var err error
		if secret, err = s.sealText(secret, loginSecretAAD); err != nil {
			return err
		}
	}
	googleSecret := c.Google.ClientSecret
	if googleSecret != "" {
		var err error
		if googleSecret, err = s.sealText(googleSecret, googleSecretAAD); err != nil {
			return err
		}
	}
	nonNil := func(list []string) []string {
		if list == nil {
			return []string{}
		}
		return list
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO login_config (id, github_enabled, github_client_id, github_client_secret,
			github_org, github_admin_teams, github_viewer_teams, github_web_url, github_api_url, password_login,
			google_enabled, google_client_id, google_client_secret, google_domains, google_admin_emails, google_viewer_emails)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15)
		ON CONFLICT (id) DO UPDATE SET github_enabled = EXCLUDED.github_enabled,
			github_client_id = EXCLUDED.github_client_id, github_client_secret = EXCLUDED.github_client_secret,
			github_org = EXCLUDED.github_org, github_admin_teams = EXCLUDED.github_admin_teams,
			github_viewer_teams = EXCLUDED.github_viewer_teams, github_web_url = EXCLUDED.github_web_url,
			github_api_url = EXCLUDED.github_api_url, password_login = EXCLUDED.password_login,
			google_enabled = EXCLUDED.google_enabled, google_client_id = EXCLUDED.google_client_id,
			google_client_secret = EXCLUDED.google_client_secret, google_domains = EXCLUDED.google_domains,
			google_admin_emails = EXCLUDED.google_admin_emails, google_viewer_emails = EXCLUDED.google_viewer_emails,
			updated_at = now()`,
		c.GitHubEnabled, c.ClientID, secret, c.Org, nonNil(c.AdminTeams), nonNil(c.ViewerTeams), c.WebURL, c.APIURL,
		c.PasswordLogin, c.Google.Enabled, c.Google.ClientID, googleSecret, nonNil(c.Google.Domains),
		nonNil(c.Google.AdminEmails), nonNil(c.Google.ViewerEmails))
	if err != nil {
		return fmt.Errorf("save login settings: %w", err)
	}
	return nil
}

// AdminCanLogIn reports whether an admin could log in with the methods.
func (s *Store) AdminCanLogIn(ctx context.Context, methods LoginMethods) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE role = 'admin'
		AND ((github_id IS NULL AND google_sub IS NULL AND $1 AND password_hash <> '')
		  OR (github_id IS NOT NULL AND $2) OR (google_sub IS NOT NULL AND $3)))`,
		methods.Password, methods.GitHub, methods.Google).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check admins: %w", err)
	}
	return exists, nil
}

// ExpireGitHubChecks makes every GitHub account's teams due for a check at its next request.
func (s *Store) ExpireGitHubChecks(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `UPDATE app_user SET github_checked_at = NULL WHERE github_id IS NOT NULL`); err != nil {
		return fmt.Errorf("expire GitHub checks: %w", err)
	}
	return nil
}

// LastSSOLogin returns the account of a kind ("github" or "google") that logged in last, and when,
// or "" if none has.
func (s *Store) LastSSOLogin(ctx context.Context, kind string) (string, *time.Time, error) {
	column := "github_id"
	if kind == "google" {
		column = "google_sub"
	}
	var name string
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT username, last_login_at FROM app_user
		WHERE `+column+` IS NOT NULL AND last_login_at IS NOT NULL ORDER BY last_login_at DESC LIMIT 1`).Scan(&name, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("last %s login: %w", kind, err)
	}
	return name, at, nil
}

// ExpireGoogleChecks makes every Google account due for a check at its next request.
func (s *Store) ExpireGoogleChecks(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `UPDATE app_user SET google_checked_at = NULL WHERE google_sub IS NOT NULL`); err != nil {
		return fmt.Errorf("expire Google checks: %w", err)
	}
	return nil
}
