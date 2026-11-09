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
	errLoginFromEnv = errors.New("Login is set by environment variables (KUMA_GITHUB_*, KUMA_GOOGLE_*, KUMA_OIDC_*, KUMA_PASSWORD_LOGIN), so it can't be changed here.")                                                                                                     //nolint:staticcheck // Shown to users.
	errNoLogin      = errors.New("Turn on password login or GitHub login: with neither, nobody could log in.")                                                                                                                                                               //nolint:staticcheck // Shown to users.
	errPasswordOff  = errors.New("Password login can only be off while you're logged in as an admin with single sign-on, with that provider's settings as you logged in with them. Save its settings with password login on, log in with it, then turn password login off.") //nolint:staticcheck // Shown to users.
	errNoAdminLeft  = errors.New("No admin could log in after this change. Keep a way in for an admin: a password, or GitHub.")                                                                                                                                              //nolint:staticcheck // Shown to users.
)

// loginState is how people log in now.
type loginState struct {
	github   *GitHubOptions
	google   *GoogleOptions
	oidc     *OIDCOptions
	password bool
	// fromEnv means environment variables set it, and the UI can't change it.
	fromEnv bool
}

// github returns GitHub login's settings, or nil when it's off.
func (s *Server) github() *GitHubOptions { return s.login.Load().github }

// google returns Google login's settings, or nil when it's off.
func (s *Server) google() *GoogleOptions { return s.login.Load().google }

// oidc returns the OpenID Connect provider's settings, or nil when it's off.
func (s *Server) oidc() *OIDCOptions { return s.login.Load().oidc }

// oidcName is the OpenID Connect provider's name for the login page, or "" when it's off.
func (s *Server) oidcName() string {
	if o := s.oidc(); o != nil {
		return o.Name
	}
	return ""
}

// passwordLogin reports whether passwords log in.
func (s *Server) passwordLogin() bool { return s.login.Load().password }

func (s *Server) loginMethods() store.LoginMethods {
	l := s.login.Load()
	return store.LoginMethods{Password: l.password, GitHub: l.github != nil, Google: l.google != nil, OIDC: l.oidc != nil}
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

// oidcFromConfig is the OpenID Connect provider's options from saved settings, or nil when it's off.
func oidcFromConfig(c store.OIDCConfig) *OIDCOptions {
	if !c.Enabled {
		return nil
	}
	return &OIDCOptions{
		Name: c.Name, Issuer: c.Issuer, ClientID: c.ClientID, ClientSecret: c.ClientSecret, Scopes: c.Scopes,
		GroupsClaim: c.GroupsClaim, AdminGroups: c.AdminGroups, ViewerGroups: c.ViewerGroups,
		AdminEmails: c.AdminEmails, ViewerEmails: c.ViewerEmails,
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
	s.login.Store(&loginState{github: githubFromConfig(c), google: googleFromConfig(c.Google, s.opts.GoogleEndpoint),
		oidc: oidcFromConfig(c.OIDC), password: c.PasswordLogin})
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
	settings["oidc"] = oidcSettingsJSON(store.OIDCConfig{})
	if l.fromEnv {
		if o := l.oidc; o != nil {
			settings["oidc"] = oidcSettingsJSON(store.OIDCConfig{Enabled: true, Name: o.Name, Issuer: o.Issuer,
				ClientID: o.ClientID, ClientSecret: o.ClientSecret, Scopes: o.Scopes, GroupsClaim: o.GroupsClaim,
				AdminGroups: o.AdminGroups, ViewerGroups: o.ViewerGroups, AdminEmails: o.AdminEmails, ViewerEmails: o.ViewerEmails})
		}
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
		settings["oidc"] = oidcSettingsJSON(saved.OIDC)
		settings["githubEnabled"], settings["clientId"], settings["hasClientSecret"] = saved.GitHubEnabled, saved.ClientID, saved.ClientSecret != ""
		settings["org"], settings["adminTeams"], settings["viewerTeams"] = saved.Org, nonNilList(saved.AdminTeams), nonNilList(saved.ViewerTeams)
		settings["webURL"], settings["apiURL"], settings["passwordLogin"] = saved.WebURL, saved.APIURL, saved.PasswordLogin
	}
	user, err := s.store.UserByID(ctx, c.userID)
	if err != nil {
		return nil, err
	}
	lastLogins := map[string]any{}
	for _, kind := range []string{"github", "google", "oidc"} {
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
	case user.OIDCSub != nil:
		you = "oidc"
	}
	return ok("settings", settings, "managedByEnv", l.fromEnv, "callbackURL", s.callbackURL(ctx, "github"),
		"googleCallbackURL", s.callbackURL(ctx, "google"), "oidcCallbackURL", s.callbackURL(ctx, "oidc"),
		"youUse", you, "lastLogins", lastLogins), nil
}

// oidcSettingsJSON is the form's OpenID Connect part, without the secret.
func oidcSettingsJSON(c store.OIDCConfig) map[string]any {
	return map[string]any{
		"enabled": c.Enabled, "name": c.Name, "issuer": c.Issuer, "clientId": c.ClientID, "hasClientSecret": c.ClientSecret != "",
		"scopes": nonNilList(c.Scopes), "groupsClaim": c.GroupsClaim, "adminGroups": nonNilList(c.AdminGroups),
		"viewerGroups": nonNilList(c.ViewerGroups), "adminEmails": nonNilList(c.AdminEmails), "viewerEmails": nonNilList(c.ViewerEmails),
	}
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
	OIDC struct {
		Enabled      bool     `json:"enabled"`
		Name         string   `json:"name"`
		Issuer       string   `json:"issuer"`
		ClientID     string   `json:"clientId"`
		ClientSecret string   `json:"clientSecret"`
		Scopes       []string `json:"scopes"`
		GroupsClaim  string   `json:"groupsClaim"`
		AdminGroups  []string `json:"adminGroups"`
		ViewerGroups []string `json:"viewerGroups"`
		AdminEmails  []string `json:"adminEmails"`
		ViewerEmails []string `json:"viewerEmails"`
	} `json:"oidc"`
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
	if c.OIDC, err = parseOIDCSettings(in, saved.OIDC); err != nil {
		return c, err
	}
	if !c.GitHubEnabled && !c.Google.Enabled && !c.OIDC.Enabled && !c.PasswordLogin {
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

// parseOIDCSettings checks the form's OpenID Connect part, keeping the saved secret when it's
// empty and the issuer and client are the same.
func parseOIDCSettings(in loginSettingsInput, saved store.OIDCConfig) (store.OIDCConfig, error) {
	f := in.OIDC
	o := store.OIDCConfig{
		Enabled: f.Enabled, Name: strings.TrimSpace(f.Name), ClientID: strings.TrimSpace(f.ClientID),
		ClientSecret: strings.TrimSpace(f.ClientSecret), GroupsClaim: strings.TrimSpace(f.GroupsClaim),
	}
	var err error
	if o.Issuer, err = oidcIssuerURL(f.Issuer); err != nil {
		return o, err
	}
	if o.ClientSecret == "" && o.ClientID == saved.ClientID && o.Issuer == saved.Issuer {
		o.ClientSecret = saved.ClientSecret
	}
	// Groups compare as the provider writes them: some are ids, and case can matter.
	keep := func(list []string) []string {
		var out []string
		for _, v := range list {
			if v = strings.TrimSpace(v); v != "" && !slices.Contains(out, v) {
				out = append(out, v)
			}
		}
		return out
	}
	o.AdminGroups, o.ViewerGroups = keep(f.AdminGroups), keep(f.ViewerGroups)
	// Scopes keep their case too.
	o.Scopes = keep(f.Scopes)
	for _, scope := range o.Scopes {
		if strings.ContainsAny(scope, " \"\\") || len(scope) > 200 {
			return o, errors.New("A scope is one word: " + scope)
		}
	}
	if o.AdminEmails, err = cleanList(f.AdminEmails, validEmailEntry(false), "Admins are email addresses."); err != nil {
		return o, err
	}
	if o.ViewerEmails, err = cleanList(f.ViewerEmails, validEmailEntry(false), "Viewers are email addresses."); err != nil {
		return o, err
	}
	if len(o.Name) > 40 || len(o.ClientID) > 255 || len(o.ClientSecret) > 512 || len(o.GroupsClaim) > 100 ||
		len(o.AdminGroups)+len(o.ViewerGroups) > 1000 {
		return o, errors.New("a field is too long")
	}
	if !o.Enabled {
		return o, nil
	}
	switch {
	case o.Name == "":
		return o, errors.New("Enter the provider's name, for the login page's button.") //nolint:staticcheck // Shown to users.
	case o.Issuer == "":
		return o, errors.New("Enter the provider's issuer address.") //nolint:staticcheck // Shown to users.
	case o.ClientID == "":
		return o, errors.New("Enter the client ID the provider gave this server.") //nolint:staticcheck // Shown to users.
	case o.ClientSecret == "":
		return o, errors.New("Enter the client secret the provider gave this server.") //nolint:staticcheck // Shown to users.
	case len(o.AdminGroups)+len(o.ViewerGroups)+len(o.AdminEmails)+len(o.ViewerEmails) == 0:
		return o, errors.New("Enter at least one group or email, who may log in.") //nolint:staticcheck // Shown to users.
	}
	return o, nil
}

// sameOIDC reports whether two OpenID Connect settings let in the same people the same way.
func sameOIDC(a, b *OIDCOptions) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Issuer == b.Issuer && a.ClientID == b.ClientID && a.ClientSecret == b.ClientSecret &&
		slices.Equal(a.Scopes, b.Scopes) && a.GroupsClaim == b.GroupsClaim &&
		slices.Equal(a.AdminGroups, b.AdminGroups) && slices.Equal(a.ViewerGroups, b.ViewerGroups) &&
		slices.Equal(a.AdminEmails, b.AdminEmails) && slices.Equal(a.ViewerEmails, b.ViewerEmails)
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
		oidc: oidcFromConfig(config.OIDC), password: config.PasswordLogin}

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
		viaOIDC := user.OIDCSub != nil && current.oidc != nil && sameOIDC(current.oidc, next.oidc)
		if !user.IsAdmin() || (!viaGitHub && !viaGoogle && !viaOIDC) {
			return nil, userErr(errPasswordOff)
		}
	}
	methods := store.LoginMethods{Password: next.password, GitHub: next.github != nil, Google: next.google != nil, OIDC: next.oidc != nil}
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
	if !sameOIDC(current.oidc, next.oidc) {
		if err := s.store.ExpireOIDCChecks(ctx); err != nil {
			return nil, err
		}
	}
	s.login.Store(next)
	s.log.Info("login settings saved", "github", next.github != nil, "google", next.google != nil, "oidc", next.oidc != nil, "password", next.password)
	return okI18n("Saved."), nil
}
