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
	errLoginFromEnv = errors.New("Login is set by environment variables (KUMA_GITHUB_*, KUMA_PASSWORD_LOGIN), so it can't be changed here.")                                                                                                                                   //nolint:staticcheck // Shown to users.
	errNoLogin      = errors.New("Turn on password login or GitHub login: with neither, nobody could log in.")                                                                                                                                                                 //nolint:staticcheck // Shown to users.
	errPasswordOff  = errors.New("Password login can only be off while you're logged in as an admin with GitHub or Google, with that provider's settings as you logged in with them. Save its settings with password login on, log in with it, then turn password login off.") //nolint:staticcheck // Shown to users.
	errNoAdminLeft  = errors.New("No admin could log in after this change. Keep a way in for an admin: a password, or GitHub.")                                                                                                                                                //nolint:staticcheck // Shown to users.
)

// loginState is how people log in now.
type loginState struct {
	github   *GitHubOptions
	google   *GoogleOptions
	password bool
	// fromEnv means environment variables set it, and the UI can't change it.
	fromEnv bool
}

// github returns GitHub login's settings, or nil when it's off.
func (s *Server) github() *GitHubOptions { return s.login.Load().github }

// google returns Google login's settings, or nil when it's off.
func (s *Server) google() *GoogleOptions { return s.login.Load().google }

// passwordLogin reports whether passwords log in.
func (s *Server) passwordLogin() bool { return s.login.Load().password }

func (s *Server) loginMethods() store.LoginMethods {
	l := s.login.Load()
	return store.LoginMethods{Password: l.password, GitHub: l.github != nil, Google: l.google != nil}
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

// googleFromConfig is Google login's options from saved settings, or nil when it's off.
func googleFromConfig(c store.GoogleConfig, endpoint string) *GoogleOptions {
	if !c.Enabled {
		return nil
	}
	g := &GoogleOptions{
		ClientID: c.ClientID, ClientSecret: c.ClientSecret, Domains: c.Domains,
		AdminEmails: c.AdminEmails, ViewerEmails: c.ViewerEmails,
	}
	googleEndpoints(g, endpoint)
	return g
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
	s.login.Store(&loginState{github: githubFromConfig(c), google: googleFromConfig(c.Google, s.opts.GoogleEndpoint),
		password: c.PasswordLogin})
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

// callbackURL is the address to register with a provider ("github" or "google"), or "" when the
// server has no Primary Base URL, and the page uses its own address.
func (s *Server) callbackURL(ctx context.Context, kind string) string {
	base := strings.TrimRight(s.baseURL(ctx), "/")
	if base == "" {
		return ""
	}
	return base + "/api/auth/" + kind + "/callback"
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
	settings["google"] = googleSettingsJSON(false, "", false, nil, nil, nil)
	if l.fromEnv {
		if g := l.google; g != nil {
			settings["google"] = googleSettingsJSON(true, g.ClientID, g.ClientSecret != "", g.Domains, g.AdminEmails, g.ViewerEmails)
		}
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
		settings["google"] = googleSettingsJSON(saved.Google.Enabled, saved.Google.ClientID, saved.Google.ClientSecret != "",
			saved.Google.Domains, saved.Google.AdminEmails, saved.Google.ViewerEmails)
		settings["githubEnabled"], settings["clientId"], settings["hasClientSecret"] = saved.GitHubEnabled, saved.ClientID, saved.ClientSecret != ""
		settings["org"], settings["adminTeams"], settings["viewerTeams"] = saved.Org, nonNilList(saved.AdminTeams), nonNilList(saved.ViewerTeams)
		settings["webURL"], settings["apiURL"], settings["passwordLogin"] = saved.WebURL, saved.APIURL, saved.PasswordLogin
	}
	user, err := s.store.UserByID(ctx, c.userID)
	if err != nil {
		return nil, err
	}
	lastLogins := map[string]any{}
	for _, kind := range []string{"github", "google"} {
		name, at, err := s.store.LastSSOLogin(ctx, kind)
		if err != nil {
			return nil, err
		}
		if name != "" {
			lastLogins[kind] = map[string]any{"username": name, "at": at.In(s.opts.Location).Format(uiTimeFormat)}
		}
	}
	you := "password"
	switch {
	case user.GitHubID != nil:
		you = "github"
	case user.GoogleSub != nil:
		you = "google"
	}
	return ok("settings", settings, "managedByEnv", l.fromEnv, "callbackURL", s.callbackURL(ctx, "github"),
		"googleCallbackURL", s.callbackURL(ctx, "google"), "youUse", you, "lastLogins", lastLogins), nil
}

func googleSettingsJSON(enabled bool, clientID string, hasSecret bool, domains, admins, viewers []string) map[string]any {
	return map[string]any{
		"enabled": enabled, "clientId": clientID, "hasClientSecret": hasSecret, "domains": nonNilList(domains),
		"adminEmails": nonNilList(admins), "viewerEmails": nonNilList(viewers),
	}
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
	Google        struct {
		Enabled      bool     `json:"enabled"`
		ClientID     string   `json:"clientId"`
		ClientSecret string   `json:"clientSecret"`
		Domains      []string `json:"domains"`
		AdminEmails  []string `json:"adminEmails"`
		ViewerEmails []string `json:"viewerEmails"`
	} `json:"google"`
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
	if c.GitHubEnabled {
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
	}
	if c.Google, err = parseGoogleSettings(in, saved.Google); err != nil {
		return c, err
	}
	if !c.GitHubEnabled && !c.Google.Enabled && !c.PasswordLogin {
		return c, errNoLogin
	}
	return c, nil
}

// parseGoogleSettings checks the form's Google part, keeping the saved secret when it's empty.
func parseGoogleSettings(in loginSettingsInput, saved store.GoogleConfig) (store.GoogleConfig, error) {
	f := in.Google
	g := store.GoogleConfig{Enabled: f.Enabled, ClientID: strings.TrimSpace(f.ClientID), ClientSecret: strings.TrimSpace(f.ClientSecret)}
	if g.ClientSecret == "" && g.ClientID == saved.ClientID {
		g.ClientSecret = saved.ClientSecret
	}
	var err error
	if g.Domains, err = cleanList(f.Domains, validDomain, "A domain is like example.com."); err != nil { //nolint:staticcheck // Shown to users.
		return g, err
	}
	if g.AdminEmails, err = cleanList(f.AdminEmails, validEmailEntry(false), "Admins are email addresses."); err != nil {
		return g, err
	}
	if g.ViewerEmails, err = cleanList(f.ViewerEmails, validEmailEntry(true), "Viewers are email addresses, or * for everyone in the domains."); err != nil {
		return g, err
	}
	if len(g.ClientID) > 255 || len(g.ClientSecret) > 255 {
		return g, errors.New("a field is too long")
	}
	if !g.Enabled {
		return g, nil
	}
	switch {
	case g.ClientID == "":
		return g, errors.New("Enter the Google OAuth client's ID.") //nolint:staticcheck // Shown to users.
	case g.ClientSecret == "":
		return g, errors.New("Enter the Google OAuth client's secret.") //nolint:staticcheck // Shown to users.
	case len(g.AdminEmails) == 0 && len(g.ViewerEmails) == 0:
		return g, errors.New("Enter at least one admin or viewer email, who may log in with Google.") //nolint:staticcheck // Shown to users.
	case slices.Contains(g.ViewerEmails, "*") && len(g.Domains) == 0:
		// Without a domain, "*" would let in anyone with a Google account.
		return g, errors.New("A viewer of * needs at least one Google Workspace domain, or anyone with a Google account could log in.") //nolint:staticcheck // Shown to users.
	}
	return g, nil
}

// cleanList lowercases and trims a list, without blanks or repeats, and checks each entry.
func cleanList(list []string, valid func(string) bool, msg string) ([]string, error) {
	var out []string
	for _, v := range list {
		v = strings.ToLower(strings.TrimSpace(v))
		if v == "" || slices.Contains(out, v) {
			continue
		}
		if !valid(v) {
			return nil, errors.New(msg + " " + v)
		}
		out = append(out, v)
	}
	if len(out) > 1000 {
		return nil, errors.New("too many entries")
	}
	return out, nil
}

func validDomain(d string) bool {
	return len(d) <= 253 && strings.Contains(d, ".") && domainFormat.MatchString(d)
}

func validEmailEntry(star bool) func(string) bool {
	return func(v string) bool {
		if star && v == "*" {
			return true
		}
		_, ok := validEmail(v)
		return ok
	}
}

// sameGoogle reports whether two Google settings let in the same people the same way.
func sameGoogle(a, b *GoogleOptions) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.ClientID == b.ClientID && a.ClientSecret == b.ClientSecret && slices.Equal(a.Domains, b.Domains) &&
		slices.Equal(a.AdminEmails, b.AdminEmails) && slices.Equal(a.ViewerEmails, b.ViewerEmails)
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
	next := &loginState{github: githubFromConfig(config), google: googleFromConfig(config.Google, s.opts.GoogleEndpoint),
		password: config.PasswordLogin}

	// Turning passwords off needs proof that single sign-on works: the admin doing it is logged in
	// with GitHub or Google, as that provider's settings are now. While they're off, that provider's
	// settings stay as they are, since a wrong one could leave nobody able to log in: turn
	// passwords on to change them.
	if !next.password {
		user, err := s.store.UserByID(ctx, c.userID)
		if err != nil {
			return nil, err
		}
		viaGitHub := user.GitHubID != nil && current.github != nil && sameGitHub(current.github, next.github)
		viaGoogle := user.GoogleSub != nil && current.google != nil && sameGoogle(current.google, next.google)
		if !user.IsAdmin() || (!viaGitHub && !viaGoogle) {
			return nil, userErr(errPasswordOff)
		}
	}
	methods := store.LoginMethods{Password: next.password, GitHub: next.github != nil, Google: next.google != nil}
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
	if !sameGoogle(current.google, next.google) {
		if err := s.store.ExpireGoogleChecks(ctx); err != nil {
			return nil, err
		}
	}
	s.login.Store(next)
	s.log.Info("login settings saved", "github", next.github != nil, "google", next.google != nil, "password", next.password)
	return okI18n("Saved."), nil
}
