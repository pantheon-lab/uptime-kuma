package ui

import (
	"context"
	"crypto/rand"
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

// Logging in with GitHub, through an OAuth App. Membership of the organization's teams decides
// who may log in and with which role, at each login and again every day while a session is used,
// so that someone who leaves a team loses access within a day. Two-factor login is left to
// GitHub, where the organization can require it.

// GitHubOptions configure logging in with GitHub.
type GitHubOptions struct {
	ClientID     string
	ClientSecret string
	// Org is the organization whose teams decide access.
	Org string
	// AdminTeams' members are admins. ViewerTeams' members are viewers; "*" is any member.
	AdminTeams  []string
	ViewerTeams []string
	// WebURL and APIURL are GitHub's, or a GitHub Enterprise server's.
	WebURL string
	APIURL string
	Client *http.Client
}

const (
	githubCookie       = "kuma_github"
	githubCookiePath   = "/api/auth/github"
	githubLoginFor     = 10 * time.Minute
	githubRecheckEvery = 24 * time.Hour
	// githubRetryAfter is how long a GitHub account's check waits after one that failed.
	githubRetryAfter = 10 * time.Minute
	githubScope      = "read:org"
)

// errGitHubUnavailable is a failure to ask GitHub, which isn't a reason to log someone out.
var errGitHubUnavailable = errors.New("GitHub can't be reached")

func (g *GitHubOptions) client() *http.Client {
	if g.Client != nil {
		return g.Client
	}
	return &http.Client{Timeout: 15 * time.Second}
}

// githubStart sends the browser to GitHub to log in. The state and the PKCE verifier are kept in
// a short-lived cookie only this flow's paths receive.
func (s *Server) githubStart(w http.ResponseWriter, r *http.Request) {
	g := s.github()
	if g == nil {
		http.NotFound(w, r)
		return
	}
	state, verifier := randomToken(), randomToken()
	remember := "0"
	if r.URL.Query().Get("remember") != "0" {
		remember = "1"
	}
	http.SetCookie(w, &http.Cookie{
		Name: githubCookie, Value: state + "." + verifier + "." + remember, Path: githubCookiePath,
		MaxAge: int(githubLoginFor.Seconds()), HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {g.ClientID},
		"scope":                 {githubScope},
		"state":                 {state},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
		"allow_signup":          {"false"},
	}
	http.Redirect(w, r, strings.TrimRight(g.WebURL, "/")+"/login/oauth/authorize?"+query.Encode(), http.StatusFound)
}

// githubCallback finishes the login GitHub sent the browser back from, with one reading of the
// login settings throughout, which the account's save refuses if they changed meanwhile.
func (s *Server) githubCallback(w http.ResponseWriter, r *http.Request) {
	l := s.currentLogin(r.Context())
	g := l.github
	if g == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(code string) {
		http.Redirect(w, r, "/dashboard?loginError="+code, http.StatusFound)
	}
	cookie, err := r.Cookie(githubCookie)
	http.SetCookie(w, &http.Cookie{Name: githubCookie, Value: "", Path: githubCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode})
	if err != nil {
		fail("github-expired")
		return
	}
	parts := strings.Split(cookie.Value, ".")
	state := r.URL.Query().Get("state")
	if len(parts) != 3 || state == "" || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(state)) != 1 {
		fail("github-expired")
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail("github-denied")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	token, err := s.githubExchange(ctx, g, r.URL.Query().Get("code"), parts[1])
	if err != nil {
		s.log.Warn("GitHub login failed", "error", err)
		fail("github-failed")
		return
	}
	id, login, role, teams, err := s.githubRole(ctx, g, token)
	if err != nil {
		s.log.Warn("GitHub login failed", "error", err)
		fail("github-failed")
		return
	}
	if role == "" {
		s.log.Warn("GitHub login refused: not in the teams", "github", login)
		fail("github-not-allowed")
		return
	}
	user, err := s.store.SaveGitHubUser(ctx, id, login, role, token, teams, l.version)
	if errors.Is(err, store.ErrLoginChanged) {
		// An admin changed the login settings during the login: it's tried again with the new ones.
		s.loginStale(ctx, l)
		s.log.Warn("GitHub login refused: the login settings changed during it", "github", login)
		fail("login-changed")
		return
	}
	if err != nil {
		s.log.Error("GitHub login", "error", err)
		fail("github-failed")
		return
	}
	// Someone whose role changed shouldn't keep a page open with the old one.
	s.closeOtherSessions(user.ID, nil)
	if err := s.startSession(ctx, w, r, user.ID, parts[2] == "1"); err != nil {
		s.log.Error("GitHub login", "error", err)
		fail("github-failed")
		return
	}
	s.log.Info("login", "username", user.Username, "role", user.Role, "with", "GitHub")
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

func randomToken() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// githubExchange trades the code GitHub gave the browser for a token.
func (s *Server) githubExchange(ctx context.Context, g *GitHubOptions, code, verifier string) (string, error) {
	if code == "" {
		return "", errors.New("no code")
	}
	form := url.Values{"client_id": {g.ClientID}, "client_secret": {g.ClientSecret}, "code": {code}, "code_verifier": {verifier}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.WebURL, "/")+"/login/oauth/access_token",
		strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := g.client().Do(req)
	if err != nil {
		return "", fmt.Errorf("%w: %v", errGitHubUnavailable, err)
	}
	defer resp.Body.Close()
	var body struct {
		AccessToken string `json:"access_token"`
		Scope       string `json:"scope"`
		Error       string `json:"error"`
		Description string `json:"error_description"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 64<<10)).Decode(&body); err != nil {
		return "", fmt.Errorf("token: %s: %w", resp.Status, err)
	}
	if body.AccessToken == "" {
		return "", fmt.Errorf("token: %s %s", body.Error, body.Description)
	}
	return body.AccessToken, nil
}

// githubRole asks GitHub who the token's user is, which of the organization's teams they're in,
// and what those make them: RoleAdmin, RoleViewer, or "" for someone who may not log in. A token
// GitHub refuses is "", and a GitHub that can't be reached is errGitHubUnavailable.
func (s *Server) githubRole(ctx context.Context, g *GitHubOptions, token string) (int64, string, string, []string, error) {
	var user struct {
		ID    int64  `json:"id"`
		Login string `json:"login"`
	}
	status, err := s.githubGet(ctx, g, token, "/user", &user)
	if err != nil {
		return 0, "", "", nil, err
	}
	if status == http.StatusUnauthorized {
		return 0, "", "", nil, nil
	}
	if status != http.StatusOK || user.ID == 0 || user.Login == "" {
		return 0, "", "", nil, fmt.Errorf("user: HTTP %d", status)
	}

	var membership struct {
		State string `json:"state"`
	}
	status, err = s.githubGet(ctx, g, token, "/user/memberships/orgs/"+url.PathEscape(g.Org), &membership)
	if err != nil {
		return 0, "", "", nil, err
	}
	switch {
	case status == http.StatusOK && membership.State == "active":
	case status == http.StatusOK, status == http.StatusNotFound, status == http.StatusForbidden, status == http.StatusUnauthorized:
		return user.ID, user.Login, "", nil, nil
	default:
		return 0, "", "", nil, fmt.Errorf("membership: HTTP %d", status)
	}

	var teams []string
	for page := 1; page <= 10; page++ {
		var list []struct {
			Slug         string `json:"slug"`
			Organization struct {
				Login string `json:"login"`
			} `json:"organization"`
		}
		status, err := s.githubGet(ctx, g, token, fmt.Sprintf("/user/teams?per_page=100&page=%d", page), &list)
		if err != nil {
			return 0, "", "", nil, err
		}
		if status != http.StatusOK {
			return 0, "", "", nil, fmt.Errorf("teams: HTTP %d", status)
		}
		for _, team := range list {
			if strings.EqualFold(team.Organization.Login, g.Org) {
				teams = append(teams, strings.ToLower(team.Slug))
			}
		}
		if len(list) < 100 {
			break
		}
	}
	slices.Sort(teams)
	return user.ID, user.Login, roleForTeams(teams, g.AdminTeams, g.ViewerTeams), teams, nil
}

// roleForTeams is the role an organization member's teams give.
func roleForTeams(teams, adminTeams, viewerTeams []string) string {
	for _, team := range teams {
		if slices.Contains(adminTeams, team) {
			return store.RoleAdmin
		}
	}
	if slices.Contains(viewerTeams, "*") {
		return store.RoleViewer
	}
	for _, team := range teams {
		if slices.Contains(viewerTeams, team) {
			return store.RoleViewer
		}
	}
	return ""
}

func (s *Server) githubGet(ctx context.Context, g *GitHubOptions, token, path string, into any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(g.APIURL, "/")+path, nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	resp, err := g.client().Do(req)
	if err != nil {
		return 0, fmt.Errorf("%w: %v", errGitHubUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return 0, fmt.Errorf("%w: HTTP %d", errGitHubUnavailable, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusOK && into != nil {
		if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(into); err != nil {
			return 0, fmt.Errorf("%s: %w", path, err)
		}
	}
	return resp.StatusCode, nil
}

// recheckGitHub checks a GitHub account's teams again once a day while it's used. It returns
// the account's role now, or "" when it may no longer log in, which ends its sessions. When
// GitHub can't be reached, the account keeps its role until the next try.
func (s *Server) recheckGitHub(ctx context.Context, user store.User) string {
	if s.github() == nil {
		// GitHub login was turned off: its accounts have no other way in.
		return ""
	}
	if user.GitHubCheckedAt != nil && s.now().Sub(*user.GitHubCheckedAt) < githubRecheckEvery {
		return user.Role
	}
	// One check at a time per account, and after one that failed, none for a while: during a
	// GitHub outage or rate limit, every request would otherwise wait on GitHub again.
	if !s.startGitHubCheck(user.ID) {
		return user.Role
	}
	failed := false
	defer func() { s.endGitHubCheck(user.ID, failed) }()
	l := s.currentLogin(ctx)
	if l.github == nil {
		return ""
	}
	token, err := s.store.GitHubToken(ctx, user.ID)
	if errors.Is(err, store.ErrNotFound) {
		return ""
	}
	if err != nil {
		s.log.Warn("check GitHub teams", "error", err)
		failed = true
		return user.Role
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	_, _, role, teams, err := s.githubRole(ctx, l.github, token)
	if err != nil {
		s.log.Warn("check GitHub teams", "username", user.Username, "error", err)
		failed = true
		return user.Role
	}
	if role == "" {
		if s.loginStale(ctx, l) {
			return user.Role
		}
		s.log.Info("GitHub account logged out: no longer in the teams", "username", user.Username)
		return ""
	}
	if err := s.store.SetGitHubChecked(ctx, user.ID, role, teams, l.version); err != nil {
		if errors.Is(err, store.ErrLoginChanged) {
			// Checked with settings another instance replaced: read them, and check again next time.
			s.loginStale(ctx, l)
			return user.Role
		}
		// The stored role stays as it was, so keep it until the next try: returning the new
		// one would look like a change on every request, and close the user's streams each time.
		s.log.Warn("check GitHub teams", "error", err)
		failed = true
		return user.Role
	}
	if role != user.Role {
		s.log.Info("GitHub account's role changed", "username", user.Username, "role", role)
	}
	return role
}

// startGitHubCheck reports whether a check of the account's teams may start now: none is
// running, and none failed in the last githubRetryAfter. It then marks one as running.
func (s *Server) startGitHubCheck(userID int32) bool {
	s.githubMu.Lock()
	defer s.githubMu.Unlock()
	if s.githubChecking[userID] {
		return false
	}
	if failed, ok := s.githubFailed[userID]; ok && s.now().Sub(failed) < githubRetryAfter {
		return false
	}
	s.githubChecking[userID] = true
	return true
}

// endGitHubCheck marks the account's check as done, and when it failed, when.
func (s *Server) endGitHubCheck(userID int32, failed bool) {
	s.githubMu.Lock()
	defer s.githubMu.Unlock()
	delete(s.githubChecking, userID)
	if failed {
		s.githubFailed[userID] = s.now()
	} else {
		delete(s.githubFailed, userID)
	}
}
