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
	// UpdatedAt is nil until the first save.
	UpdatedAt *time.Time
}

var loginSecretAAD = secretAAD("login_config", "github_client_secret", 1)

// LoginConfig returns the login settings saved in the UI, or the defaults: passwords only.
func (s *Store) LoginConfig(ctx context.Context) (LoginConfig, error) {
	var c LoginConfig
	var updated time.Time
	err := s.pool.QueryRow(ctx, `SELECT github_enabled, github_client_id, github_client_secret, github_org,
		github_admin_teams, github_viewer_teams, github_web_url, github_api_url, password_login, updated_at
		FROM login_config WHERE id = 1`).Scan(&c.GitHubEnabled, &c.ClientID, &c.ClientSecret, &c.Org,
		&c.AdminTeams, &c.ViewerTeams, &c.WebURL, &c.APIURL, &c.PasswordLogin, &updated)
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
	nonNil := func(list []string) []string {
		if list == nil {
			return []string{}
		}
		return list
	}
	_, err := s.pool.Exec(ctx, `INSERT INTO login_config (id, github_enabled, github_client_id, github_client_secret,
			github_org, github_admin_teams, github_viewer_teams, github_web_url, github_api_url, password_login)
		VALUES (1, $1, $2, $3, $4, $5, $6, $7, $8, $9)
		ON CONFLICT (id) DO UPDATE SET github_enabled = EXCLUDED.github_enabled,
			github_client_id = EXCLUDED.github_client_id, github_client_secret = EXCLUDED.github_client_secret,
			github_org = EXCLUDED.github_org, github_admin_teams = EXCLUDED.github_admin_teams,
			github_viewer_teams = EXCLUDED.github_viewer_teams, github_web_url = EXCLUDED.github_web_url,
			github_api_url = EXCLUDED.github_api_url, password_login = EXCLUDED.password_login, updated_at = now()`,
		c.GitHubEnabled, c.ClientID, secret, c.Org, nonNil(c.AdminTeams), nonNil(c.ViewerTeams), c.WebURL, c.APIURL,
		c.PasswordLogin)
	if err != nil {
		return fmt.Errorf("save login settings: %w", err)
	}
	return nil
}

// AdminCanLogIn reports whether an admin could log in with the methods.
func (s *Store) AdminCanLogIn(ctx context.Context, methods LoginMethods) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE role = 'admin'
		AND ((github_id IS NULL AND $1 AND password_hash <> '') OR (github_id IS NOT NULL AND $2)))`,
		methods.Password, methods.GitHub).Scan(&exists)
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

// LastGitHubLogin returns the GitHub account that logged in last, and when, or "" if none has.
func (s *Store) LastGitHubLogin(ctx context.Context) (string, *time.Time, error) {
	var name string
	var at *time.Time
	err := s.pool.QueryRow(ctx, `SELECT username, last_login_at FROM app_user
		WHERE github_id IS NOT NULL AND last_login_at IS NOT NULL ORDER BY last_login_at DESC LIMIT 1`).Scan(&name, &at)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil, nil
	}
	if err != nil {
		return "", nil, fmt.Errorf("last GitHub login: %w", err)
	}
	return name, at, nil
}
