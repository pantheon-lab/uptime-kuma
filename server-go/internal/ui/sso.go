package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Single sign-on: how people log in, set in Settings > Single Sign-On, or by environment variables,
// which then win and make the page read-only. The settings take effect at once on this instance,
// and other instances read them within loginRefreshEvery.
//
// Two rules keep admins from locking themselves out: passwords can only be turned off by an admin
// logged in with GitHub, which shows that GitHub login works, and no change may leave no admin who
// can log in.

const (
	loginRefreshEvery = 30 * time.Second
	defaultGitHubWeb  = "https://github.com"
	defaultGitHubAPI  = "https://api.github.com"
	maxTeams          = 100
)

var (
	errLoginFromEnv = errors.New("Login is set by environment variables (KUMA_GITHUB_*, KUMA_PASSWORD_LOGIN), so it can't be changed here.")                                                                                             //nolint:staticcheck // Shown to users.
	errNoLogin      = errors.New("Turn on password login or GitHub login: with neither, nobody could log in.")                                                                                                                           //nolint:staticcheck // Shown to users.
	errPasswordOff  = errors.New("Password login can only be off with GitHub settings that an admin has logged in with. Save the GitHub settings with password login on, log in with GitHub as an admin, then turn password login off.") //nolint:staticcheck // Shown to users.
	errNoAdminLeft  = errors.New("No admin could log in after this change. Keep a way in for an admin: a password, or GitHub.")                                                                                                          //nolint:staticcheck // Shown to users.
)

// loginState is how people log in now.
type loginState struct {
	github   *GitHubOptions
	password bool
	// fromEnv means environment variables set it, and the UI can't change it.
	fromEnv bool
}

// github returns GitHub login's settings, or nil when it's off.
func (s *Server) github() *GitHubOptions { return s.login.Load().github }

// passwordLogin reports whether passwords log in.
func (s *Server) passwordLogin() bool { return s.login.Load().password }

func (s *Server) loginMethods() store.LoginMethods {
	l := s.login.Load()
	return store.LoginMethods{Password: l.password, GitHub: l.github != nil}
}

// githubFromConfig is GitHub login's options from saved settings, or nil when it's off.
func githubFromConfig(c store.LoginConfig) *GitHubOptions {
	if !c.GitHubEnabled {
		return nil
	}
	web, api := c.WebURL, c.APIURL
	if web == "" {
		web = defaultGitHubWeb
	}
	if api == "" {
		api = defaultGitHubAPI
	}
	return &GitHubOptions{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, Org: c.Org,
		AdminTeams: c.AdminTeams, ViewerTeams: c.ViewerTeams, WebURL: web, APIURL: api,
	}
}

// LoadLoginConfig takes the login settings saved in the UI, unless environment variables set them.
func (s *Server) LoadLoginConfig(ctx context.Context) error {
	if s.login.Load().fromEnv {
		return nil
	}
	c, err := s.store.LoginConfig(ctx)
	if err != nil {
		return err
	}
	s.login.Store(&loginState{github: githubFromConfig(c), password: c.PasswordLogin})
	return nil
}

// WatchLoginConfig reads the login settings again now and then, for changes made on another
// instance, until ctx ends.
func (s *Server) WatchLoginConfig(ctx context.Context) {
	ticker := time.NewTicker(loginRefreshEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.LoadLoginConfig(ctx); err != nil {
				s.log.Warn("read login settings", "error", err)
			}
		}
	}
}

// callbackURL is the address to register in the GitHub OAuth App.
func (s *Server) callbackURL(ctx context.Context) string {
	base := strings.TrimRight(s.baseURL(ctx), "/")
	if base == "" {
		return ""
	}
	return base + "/api/auth/github/callback"
}

func nonNilList(list []string) []string {
	if list == nil {
		return []string{}
	}
	return list
}

// getLoginSettings is Settings > Single Sign-On's form. The client secret is never sent.
func (s *Server) getLoginSettings(ctx context.Context, c *conn) (reply, error) {
	l := s.login.Load()
	settings := map[string]any{
		"githubEnabled": l.github != nil, "clientId": "", "hasClientSecret": false, "org": "",
		"adminTeams": []string{}, "viewerTeams": []string{}, "webURL": "", "apiURL": "", "passwordLogin": l.password,
	}
	if l.fromEnv {
		if g := l.github; g != nil {
			settings["clientId"], settings["hasClientSecret"], settings["org"] = g.ClientID, g.ClientSecret != "", g.Org
			settings["adminTeams"], settings["viewerTeams"] = nonNilList(g.AdminTeams), nonNilList(g.ViewerTeams)
			settings["webURL"], settings["apiURL"] = optionalURL(g.WebURL, defaultGitHubWeb), optionalURL(g.APIURL, defaultGitHubAPI)
		}
	} else {
		saved, err := s.store.LoginConfig(ctx)
		if err != nil {
			return nil, err
		}
		settings["githubEnabled"], settings["clientId"], settings["hasClientSecret"] = saved.GitHubEnabled, saved.ClientID, saved.ClientSecret != ""
		settings["org"], settings["adminTeams"], settings["viewerTeams"] = saved.Org, nonNilList(saved.AdminTeams), nonNilList(saved.ViewerTeams)
		settings["webURL"], settings["apiURL"], settings["passwordLogin"] = saved.WebURL, saved.APIURL, saved.PasswordLogin
	}
	user, err := s.store.UserByID(ctx, c.userID)
	if err != nil {
		return nil, err
	}
	lastName, lastAt, err := s.store.LastGitHubLogin(ctx)
	if err != nil {
		return nil, err
	}
	var last any
	if lastName != "" {
		last = map[string]any{"username": lastName, "at": lastAt.In(s.opts.Location).Format(uiTimeFormat)}
	}
	return ok("settings", settings, "managedByEnv", l.fromEnv, "callbackURL", s.callbackURL(ctx),
		"youUseGitHub", user.GitHubID != nil, "lastGitHubLogin", last), nil
}

// optionalURL is "" for the default address, as the form shows it.
func optionalURL(u, def string) string {
	if strings.TrimRight(u, "/") == def {
		return ""
	}
	return u
}

type loginSettingsInput struct {
	GitHubEnabled bool     `json:"githubEnabled"`
	ClientID      string   `json:"clientId"`
	ClientSecret  string   `json:"clientSecret"`
	Org           string   `json:"org"`
	AdminTeams    []string `json:"adminTeams"`
	ViewerTeams   []string `json:"viewerTeams"`
	WebURL        string   `json:"webURL"`
	APIURL        string   `json:"apiURL"`
	PasswordLogin bool     `json:"passwordLogin"`
}

// teamSlugs cleans a list of team slugs: lowercase, without blanks or repeats.
func teamSlugs(list []string) ([]string, error) {
	var out []string
	for _, team := range list {
		team = strings.ToLower(strings.TrimSpace(team))
		if team == "" || slices.Contains(out, team) {
			continue
		}
		if len(team) > 100 || strings.ContainsAny(team, " /,") {
			return nil, errors.New("A team is its slug, as in its address: letters, digits and dashes.") //nolint:staticcheck // Shown to users.
		}
		out = append(out, team)
	}
	if len(out) > maxTeams {
		return nil, errors.New("too many teams")
	}
	return out, nil
}

func githubURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
		return "", errors.New("GitHub Enterprise addresses start with https://") //nolint:staticcheck // Shown to users.
	}
	return raw, nil
}

// parseLoginSettings checks the form, keeping the saved secret when the form leaves it empty.
func parseLoginSettings(in loginSettingsInput, saved store.LoginConfig) (store.LoginConfig, error) {
	c := store.LoginConfig{
		GitHubEnabled: in.GitHubEnabled,
		ClientID:      strings.TrimSpace(in.ClientID),
		ClientSecret:  strings.TrimSpace(in.ClientSecret),
		Org:           strings.TrimSpace(in.Org),
		PasswordLogin: in.PasswordLogin,
	}
	if c.ClientSecret == "" && c.ClientID == saved.ClientID {
		c.ClientSecret = saved.ClientSecret
	}
	var err error
	if c.AdminTeams, err = teamSlugs(in.AdminTeams); err != nil {
		return c, err
	}
	if c.ViewerTeams, err = teamSlugs(in.ViewerTeams); err != nil {
		return c, err
	}
	if c.WebURL, err = githubURL(in.WebURL); err != nil {
		return c, err
	}
	if c.APIURL, err = githubURL(in.APIURL); err != nil {
		return c, err
	}
	if len(c.ClientID) > 255 || len(c.ClientSecret) > 255 || len(c.Org) > 100 {
		return c, errors.New("a field is too long")
	}
	if !c.GitHubEnabled {
		if !c.PasswordLogin {
			return c, errNoLogin
		}
		return c, nil
	}
	switch {
	case c.ClientID == "":
		return c, errors.New("Enter the OAuth App's client ID.") //nolint:staticcheck // Shown to users.
	case c.ClientSecret == "":
		return c, errors.New("Enter the OAuth App's client secret.") //nolint:staticcheck // Shown to users.
	case c.Org == "":
		return c, errors.New("Enter the organization whose teams decide who may log in.") //nolint:staticcheck // Shown to users.
	case len(c.AdminTeams) == 0 && len(c.ViewerTeams) == 0:
		return c, errors.New("Enter at least one team, whose members may log in.") //nolint:staticcheck // Shown to users.
	}
	return c, nil
}

// sameGitHub reports whether two GitHub settings let in the same people the same way.
func sameGitHub(a *GitHubOptions, b *GitHubOptions) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ClientID == b.ClientID && a.ClientSecret == b.ClientSecret && strings.EqualFold(a.Org, b.Org) &&
		slices.Equal(a.AdminTeams, b.AdminTeams) && slices.Equal(a.ViewerTeams, b.ViewerTeams) &&
		a.WebURL == b.WebURL && a.APIURL == b.APIURL
}

// saveLoginSettings saves Settings > Single Sign-On, and applies it at once.
func (s *Server) saveLoginSettings(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	current := s.login.Load()
	if current.fromEnv {
		return nil, userErr(errLoginFromEnv)
	}
	var in loginSettingsInput
	if err := arg(args, 0, &in); err != nil {
		return nil, userErr(err)
	}
	saved, err := s.store.LoginConfig(ctx)
	if err != nil {
		return nil, err
	}
	config, err := parseLoginSettings(in, saved)
	if err != nil {
		return nil, userErr(err)
	}
	next := &loginState{github: githubFromConfig(config), password: config.PasswordLogin}

	// Turning passwords off needs proof that GitHub login works: the admin doing it is logged in
	// with GitHub, as the settings are now. While they're off, GitHub's settings stay as they are,
	// since a wrong one could leave nobody able to log in: turn passwords on to change them.
	if !next.password {
		user, err := s.store.UserByID(ctx, c.userID)
		if err != nil {
			return nil, err
		}
		if user.GitHubID == nil || current.github == nil || !sameGitHub(current.github, next.github) {
			return nil, userErr(errPasswordOff)
		}
	}
	methods := store.LoginMethods{Password: next.password, GitHub: next.github != nil}
	if canLogIn, err := s.store.AdminCanLogIn(ctx, methods); err != nil {
		return nil, err
	} else if !canLogIn {
		return nil, userErr(errNoAdminLeft)
	}

	if err := s.store.SaveLoginConfig(ctx, config); err != nil {
		return nil, err
	}
	// Who may log in, and as what, may have changed: GitHub accounts are checked at their next request.
	if !sameGitHub(current.github, next.github) {
		if err := s.store.ExpireGitHubChecks(ctx); err != nil {
			return nil, err
		}
	}
	s.login.Store(next)
	s.log.Info("login settings saved", "github", next.github != nil, "password", next.password)
	return okI18n("Saved."), nil
}
