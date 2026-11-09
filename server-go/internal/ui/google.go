package ui

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Logging in with Google, by OpenID Connect with PKCE. Google says who someone is: their verified
// email and, for Google Workspace, their domain (the hd claim, which Google sets, unlike an email's
// suffix). The settings' email lists decide the role. A refresh token, kept sealed, has Google
// confirm the account once a day while it's used, so that someone suspended or removed in
// Workspace, or who revoked access, is logged out within a day.

// GoogleOptions configures logging in with Google.
type GoogleOptions struct {
	ClientID     string
	ClientSecret string
	// Domains are Google Workspace domains: a viewer email of "*" lets in anyone of theirs.
	Domains []string
	// AdminEmails and ViewerEmails are lowercase addresses; ViewerEmails may hold "*".
	AdminEmails  []string
	ViewerEmails []string
	// AuthURL, TokenURL and UserInfoURL are Google's; tests replace them.
	AuthURL     string
	TokenURL    string
	UserInfoURL string
	Client      *http.Client
}

const (
	googleCookie       = "kuma_google"
	googleCookiePath   = "/api/auth/google"
	googleCallbackPath = "/api/auth/google/callback"
	googleScope        = "openid email profile"
	defaultGoogleAuth  = "https://accounts.google.com/o/oauth2/v2/auth"
	defaultGoogleToken = "https://oauth2.googleapis.com/token"
	defaultGoogleInfo  = "https://openidconnect.googleapis.com/v1/userinfo"
)

// errGoogleRevoked means Google no longer accepts the account's refresh token: access was revoked,
// or the account suspended or deleted.
var errGoogleRevoked = errors.New("Google refused the refresh token")

// googleEndpoints sets Google's addresses: its own, or under base, for tests.
func googleEndpoints(g *GoogleOptions, base string) {
	if base == "" {
		g.AuthURL, g.TokenURL, g.UserInfoURL = defaultGoogleAuth, defaultGoogleToken, defaultGoogleInfo
		return
	}
	base = strings.TrimRight(base, "/")
	g.AuthURL, g.TokenURL, g.UserInfoURL = base+"/o/oauth2/v2/auth", base+"/token", base+"/v1/userinfo"
}

func (g *GoogleOptions) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// googleRole is the role an account gets: admin or viewer by its email, a viewer of "*" for anyone
// of the Workspace domains, or "" for someone who may not log in. Unverified emails get "".
func googleRole(g *GoogleOptions, email string, verified bool, domain string) string {
	email = strings.ToLower(email)
	if !verified || email == "" {
		return ""
	}
	if slices.Contains(g.AdminEmails, email) {
		return store.RoleAdmin
	}
	if slices.Contains(g.ViewerEmails, email) {
		return store.RoleViewer
	}
	if domain != "" && slices.Contains(g.ViewerEmails, "*") && slices.Contains(g.Domains, strings.ToLower(domain)) {
		return store.RoleViewer
	}
	return ""
}

// googleRedirect is the address Google sends the browser back to, which must be registered in the
// OAuth client: under the Primary Base URL, or else the address the browser used.
func (s *Server) googleRedirect(ctx context.Context, r *http.Request) string {
	if base := strings.TrimRight(s.baseURL(ctx), "/"); base != "" {
		return base + googleCallbackPath
	}
	trust := false
	if route, err := s.routing(ctx); err == nil {
		trust = route.trustProxy
	}
	return requestOrigin(r, trust) + googleCallbackPath
}

// googleStart sends the browser to Google to log in. The state, PKCE verifier and redirect address
// are kept in a short-lived cookie only this flow's paths receive.
func (s *Server) googleStart(w http.ResponseWriter, r *http.Request) {
	g := s.google()
	if g == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()
	redirect := s.googleRedirect(ctx, r)
	state, verifier := randomToken(), randomToken()
	remember := "0"
	if r.URL.Query().Get("remember") != "0" {
		remember = "1"
	}
	http.SetCookie(w, &http.Cookie{
		Name: googleCookie, Value: state + "." + verifier + "." + remember + "." + base64.RawURLEncoding.EncodeToString([]byte(redirect)),
		Path: googleCookiePath, MaxAge: int(githubLoginFor.Seconds()), HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {g.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {googleScope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		// A refresh token, for the daily check. Google sends it at the first consent.
		"access_type": {"offline"},
		"prompt":      {"select_account"},
	}
	if len(g.Domains) == 1 {
		// Only a hint for Google's account chooser: the domain is checked at the callback.
		query.Set("hd", g.Domains[0])
	}
	http.Redirect(w, r, g.AuthURL+"?"+query.Encode(), http.StatusFound)
}

// googleCallback finishes the login Google sent the browser back from.
func (s *Server) googleCallback(w http.ResponseWriter, r *http.Request) {
	g := s.google()
	if g == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(code string) {
		http.Redirect(w, r, "/dashboard?loginError="+code, http.StatusFound)
	}
	cookie, err := r.Cookie(googleCookie)
	http.SetCookie(w, &http.Cookie{Name: googleCookie, Value: "", Path: googleCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode})
	if err != nil {
		fail("google-expired")
		return
	}
	parts := strings.Split(cookie.Value, ".")
	state := r.URL.Query().Get("state")
	if len(parts) != 4 || state == "" || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(state)) != 1 {
		fail("google-expired")
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail("google-denied")
		return
	}
	redirect, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		fail("google-expired")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	tokens, err := s.googleToken(ctx, g, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.URL.Query().Get("code")},
		"redirect_uri":  {string(redirect)},
		"code_verifier": {parts[1]},
	})
	if err != nil {
		s.log.Warn("Google login failed", "error", err)
		fail("google-failed")
		return
	}
	who, verified, err := s.googleUserInfo(ctx, g, tokens.AccessToken)
	if err != nil {
		s.log.Warn("Google login failed", "error", err)
		fail("google-failed")
		return
	}
	role := googleRole(g, who.Email, verified, who.Domain)
	if role == "" {
		s.log.Warn("Google login refused: not in the lists", "email", who.Email, "domain", who.Domain, "verified", verified)
		fail("google-not-allowed")
		return
	}
	user, err := s.store.SaveGoogleUser(ctx, who, role, tokens.RefreshToken)
	if errors.Is(err, store.ErrUsernameTakenByPassword) {
		s.log.Warn("Google login refused: a password account has the email as its name", "email", who.Email)
		fail("google-name-taken")
		return
	}
	if err != nil {
		s.log.Error("Google login", "error", err)
		fail("google-failed")
		return
	}
	s.closeOtherSessions(user.ID, nil)
	if err := s.startSession(ctx, w, r, user.ID, parts[2] == "1"); err != nil {
		s.log.Error("Google login", "error", err)
		fail("google-failed")
		return
	}
	s.log.Info("login", "username", user.Username, "role", user.Role, "with", "Google")
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

type googleTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	Error        string `json:"error"`
}

// googleToken asks Google's token endpoint for tokens: for a login's code, or with a refresh token.
// A refresh token Google refuses is errGoogleRevoked; Google not answering is errGitHubUnavailable's
// kind, which isn't a reason to log someone out.
func (s *Server) googleToken(ctx context.Context, g *GoogleOptions, form url.Values) (googleTokens, error) {
	form.Set("client_id", g.ClientID)
	form.Set("client_secret", g.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return googleTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.client().Do(req)
	if err != nil {
		return googleTokens{}, fmt.Errorf("%w: %v", errGitHubUnavailable, err)
	}
	defer resp.Body.Close()
	var body googleTokens
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return body, fmt.Errorf("%w: Google answered %d", errGitHubUnavailable, resp.StatusCode)
	case body.Error == "invalid_grant" && form.Get("grant_type") == "refresh_token":
		return body, errGoogleRevoked
	case resp.StatusCode != http.StatusOK || body.AccessToken == "":
		return body, fmt.Errorf("token: HTTP %d %s", resp.StatusCode, body.Error)
	}
	return body, nil
}

// googleUserInfo asks Google who the access token's person is.
func (s *Server) googleUserInfo(ctx context.Context, g *GoogleOptions, accessToken string) (store.GoogleUser, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.UserInfoURL, nil)
	if err != nil {
		return store.GoogleUser{}, false, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := g.client().Do(req)
	if err != nil {
		return store.GoogleUser{}, false, fmt.Errorf("%w: %v", errGitHubUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return store.GoogleUser{}, false, fmt.Errorf("%w: Google answered %d", errGitHubUnavailable, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return store.GoogleUser{}, false, fmt.Errorf("userinfo: HTTP %d", resp.StatusCode)
	}
	var info struct {
		Sub           string `json:"sub"`
		Email         string `json:"email"`
		EmailVerified any    `json:"email_verified"`
		HD            string `json:"hd"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return store.GoogleUser{}, false, fmt.Errorf("userinfo: %w", err)
	}
	if info.Sub == "" {
		return store.GoogleUser{}, false, errors.New("userinfo: no subject")
	}
	// Google sends a boolean; some proxies of it send "true".
	verified := info.EmailVerified == true || info.EmailVerified == "true"
	return store.GoogleUser{Sub: info.Sub, Email: strings.ToLower(info.Email), Domain: strings.ToLower(info.HD)}, verified, nil
}

// recheckGoogle has Google confirm an account once a day while it's used, and returns its role
// now, or "" when it may no longer log in. Without a refresh token, only the settings decide.
// When Google can't be reached, the account keeps its role until the next try.
func (s *Server) recheckGoogle(ctx context.Context, user store.User) string {
	g := s.google()
	if g == nil {
		// Google login was turned off: its accounts have no other way in.
		return ""
	}
	if user.GoogleCheckedAt != nil && s.now().Sub(*user.GoogleCheckedAt) < githubRecheckEvery {
		return user.Role
	}
	if !s.startGitHubCheck(user.ID) {
		return user.Role
	}
	failed := false
	defer func() { s.endGitHubCheck(user.ID, failed) }()
	who := store.GoogleUser{Sub: *user.GoogleSub, Email: deref(user.GoogleEmail), Domain: deref(user.GoogleDomain)}
	token, err := s.store.GoogleToken(ctx, user.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No refresh token: the settings decide, from what Google said at login.
	case err != nil:
		s.log.Warn("check Google account", "error", err)
		failed = true
		return user.Role
	default:
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		tokens, err := s.googleToken(ctx, g, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}})
		if errors.Is(err, errGoogleRevoked) {
			s.log.Info("Google account logged out: Google refused its refresh token", "username", user.Username)
			return ""
		}
		var verified bool
		if err == nil {
			who, verified, err = s.googleUserInfo(ctx, g, tokens.AccessToken)
		}
		if err != nil {
			s.log.Warn("check Google account", "username", user.Username, "error", err)
			failed = true
			return user.Role
		}
		if !verified {
			who.Email = ""
		}
	}
	role := googleRole(g, who.Email, who.Email != "", who.Domain)
	if role == "" {
		s.log.Info("Google account logged out: no longer allowed", "username", user.Username)
		return ""
	}
	if err := s.store.SetGoogleChecked(ctx, user.ID, who, role); err != nil {
		s.log.Warn("check Google account", "error", err)
		failed = true
		return user.Role
	}
	if role != user.Role {
		s.log.Info("Google account's role changed", "username", user.Username, "role", role)
	}
	return role
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
