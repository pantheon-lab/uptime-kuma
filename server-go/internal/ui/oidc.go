package ui

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Logging in with an OpenID Connect provider, such as Okta, Microsoft Entra ID, Keycloak,
// Authentik or Auth0: the authorization code flow with PKCE and a nonce. The provider's endpoints
// come from its discovery document, and its ID tokens are verified with its published keys: the
// signature, issuer, audience, expiry and nonce. Groups (from a configurable claim) and emails
// decide the role. A refresh token, when the provider sends one, has it confirm the account once a
// day while it's used.

// OIDCOptions configures logging in with an OpenID Connect provider.
type OIDCOptions struct {
	// Name is the provider's, as the login page's button says it.
	Name         string
	Issuer       string
	ClientID     string
	ClientSecret string
	// Scopes are asked for besides openid.
	Scopes []string
	// GroupsClaim names the claim with the person's groups, "groups" by default.
	GroupsClaim  string
	AdminGroups  []string
	ViewerGroups []string
	AdminEmails  []string
	ViewerEmails []string
	Client       *http.Client
}

const (
	oidcCookie       = "kuma_oidc"
	oidcCookiePath   = "/api/auth/oidc"
	oidcCallbackPath = "/api/auth/oidc/callback"
	// oidcCacheFor is how long the discovery document and keys are kept.
	oidcCacheFor = 10 * time.Minute
	oidcLeeway   = time.Minute
)

var (
	errOIDCRevoked   = errors.New("the provider refused the refresh token")
	oidcSigningAlgos = []string{"RS256", "RS384", "RS512", "PS256", "PS384", "PS512", "ES256", "ES384", "ES512"}
)

func (o *OIDCOptions) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: 15 * time.Second, CheckRedirect: oidcRedirect}
}

// oidcRedirect follows a provider's redirect only to https, or http on this machine, as the
// request can carry a client secret, code or token.
func oidcRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if req.URL.Scheme != "https" && !(req.URL.Scheme == "http" && localHost(req.URL.Hostname())) {
		return errors.New("the provider redirected to an address that isn't https")
	}
	return nil
}

func (o *OIDCOptions) groupsClaim() string {
	if o.GroupsClaim == "" {
		return "groups"
	}
	return o.GroupsClaim
}

func (o *OIDCOptions) scope() string {
	scopes := []string{"openid"}
	extra := o.Scopes
	if len(extra) == 0 {
		extra = []string{"email", "profile"}
	}
	for _, s := range extra {
		if s != "openid" && !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	return strings.Join(scopes, " ")
}

// oidcDiscovery is the part of a provider's discovery document this uses.
type oidcDiscovery struct {
	Issuer                string   `json:"issuer"`
	AuthorizationEndpoint string   `json:"authorization_endpoint"`
	TokenEndpoint         string   `json:"token_endpoint"`
	UserinfoEndpoint      string   `json:"userinfo_endpoint"`
	JWKSURI               string   `json:"jwks_uri"`
	TokenAuthMethods      []string `json:"token_endpoint_auth_methods_supported"`
}

// oidcProvider is a provider's discovery document and keys, fetched when needed and kept a while.
type oidcProvider struct {
	mu        sync.Mutex
	issuer    string
	discovery *oidcDiscovery
	fetched   time.Time
	keys      map[string]crypto.PublicKey
	keysAt    time.Time
}

// oidcProviderFor returns the cache for an issuer, made anew when the issuer changes.
func (s *Server) oidcProviderFor(issuer string) *oidcProvider {
	s.oidcMu.Lock()
	defer s.oidcMu.Unlock()
	if s.oidcCache == nil || s.oidcCache.issuer != issuer {
		s.oidcCache = &oidcProvider{issuer: issuer}
	}
	return s.oidcCache
}

func oidcGetJSON(ctx context.Context, client *http.Client, u string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: %v", errGitHubUnavailable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%w: %s answered %d", errGitHubUnavailable, u, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s answered %d", u, resp.StatusCode)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(into); err != nil {
		return fmt.Errorf("%s: %w", u, err)
	}
	return nil
}

// discover reads the provider's discovery document, which must be for the configured issuer.
func discover(ctx context.Context, client *http.Client, issuer string) (*oidcDiscovery, error) {
	var d oidcDiscovery
	if err := oidcGetJSON(ctx, client, strings.TrimRight(issuer, "/")+"/.well-known/openid-configuration", &d); err != nil {
		return nil, err
	}
	if strings.TrimRight(d.Issuer, "/") != strings.TrimRight(issuer, "/") {
		return nil, fmt.Errorf("the discovery document is for the issuer %q, not %q", d.Issuer, issuer)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.JWKSURI == "" {
		return nil, errors.New("the discovery document has no authorization, token or keys endpoint")
	}
	// The token and userinfo endpoints get the client secret, codes and tokens: https only, as
	// for the issuer, but for a provider on this machine.
	for _, u := range []string{d.AuthorizationEndpoint, d.TokenEndpoint, d.JWKSURI, d.UserinfoEndpoint} {
		if u == "" {
			continue
		}
		parsed, err := url.Parse(u)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && localHost(parsed.Hostname()))) {
			return nil, fmt.Errorf("the discovery document has an invalid address: %q", u)
		}
	}
	return &d, nil
}

// document returns the provider's discovery document, fetched again after oidcCacheFor.
func (p *oidcProvider) document(ctx context.Context, o *OIDCOptions) (*oidcDiscovery, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.discovery != nil && time.Since(p.fetched) < oidcCacheFor {
		return p.discovery, nil
	}
	d, err := discover(ctx, o.client(), o.Issuer)
	if err != nil {
		if p.discovery != nil {
			// Keep the last good one while the provider is away.
			return p.discovery, nil
		}
		return nil, err
	}
	p.discovery, p.fetched = d, time.Now()
	return d, nil
}

// key returns the provider's key with the id, fetching its keys again when they're old or don't
// have it, at most once a minute for an unknown id.
func (p *oidcProvider) key(ctx context.Context, o *OIDCOptions, jwksURI, kid string) (crypto.PublicKey, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	pick := func() (crypto.PublicKey, bool) {
		if kid == "" && len(p.keys) == 1 {
			for _, k := range p.keys {
				return k, true
			}
		}
		k, ok := p.keys[kid]
		return k, ok
	}
	if k, ok := pick(); ok && time.Since(p.keysAt) < oidcCacheFor {
		return k, nil
	}
	if p.keys == nil || time.Since(p.keysAt) > time.Minute {
		keys, err := fetchJWKS(ctx, o.client(), jwksURI)
		if err != nil {
			return nil, err
		}
		p.keys, p.keysAt = keys, time.Now()
	}
	if k, ok := pick(); ok {
		return k, nil
	}
	return nil, fmt.Errorf("the provider has no signing key %q", kid)
}

// fetchJWKS reads a JSON Web Key Set's RSA and EC signing keys, by key id.
func fetchJWKS(ctx context.Context, client *http.Client, u string) (map[string]crypto.PublicKey, error) {
	var set struct {
		Keys []struct {
			Kty string `json:"kty"`
			Kid string `json:"kid"`
			Use string `json:"use"`
			N   string `json:"n"`
			E   string `json:"e"`
			Crv string `json:"crv"`
			X   string `json:"x"`
			Y   string `json:"y"`
		} `json:"keys"`
	}
	if err := oidcGetJSON(ctx, client, u, &set); err != nil {
		return nil, err
	}
	keys := map[string]crypto.PublicKey{}
	b64 := func(v string) *big.Int {
		raw, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(v, "="))
		if err != nil || len(raw) == 0 {
			return nil
		}
		return new(big.Int).SetBytes(raw)
	}
	for _, k := range set.Keys {
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		switch k.Kty {
		case "RSA":
			n, e := b64(k.N), b64(k.E)
			if n == nil || e == nil || !e.IsInt64() || n.BitLen() < 2048 {
				continue
			}
			keys[k.Kid] = &rsa.PublicKey{N: n, E: int(e.Int64())}
		case "EC":
			var curve elliptic.Curve
			switch k.Crv {
			case "P-256":
				curve = elliptic.P256()
			case "P-384":
				curve = elliptic.P384()
			case "P-521":
				curve = elliptic.P521()
			default:
				continue
			}
			x, y := b64(k.X), b64(k.Y)
			if x == nil || y == nil || !curve.IsOnCurve(x, y) {
				continue
			}
			keys[k.Kid] = &ecdsa.PublicKey{Curve: curve, X: x, Y: y}
		}
	}
	if len(keys) == 0 {
		return nil, errors.New("the provider's key set has no RSA or EC signing keys")
	}
	return keys, nil
}

// verifyIDToken checks an ID token's signature with the provider's keys, its issuer, audience,
// authorized party, expiry and, at login, nonce, and returns its claims.
func (s *Server) verifyIDToken(ctx context.Context, o *OIDCOptions, d *oidcDiscovery, raw, nonce string) (jwt.MapClaims, error) {
	p := s.oidcProviderFor(o.Issuer)
	claims := jwt.MapClaims{}
	_, err := jwt.ParseWithClaims(raw, claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return p.key(ctx, o, d.JWKSURI, kid)
	}, jwt.WithValidMethods(oidcSigningAlgos), jwt.WithIssuer(d.Issuer), jwt.WithAudience(o.ClientID),
		jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithLeeway(oidcLeeway))
	if err != nil {
		return nil, fmt.Errorf("ID token: %w", err)
	}
	if aud, _ := claims.GetAudience(); len(aud) > 1 {
		if azp, _ := claims["azp"].(string); azp != o.ClientID {
			return nil, errors.New("ID token: issued to another party")
		}
	}
	if nonce != "" {
		if got, _ := claims["nonce"].(string); subtle.ConstantTimeCompare([]byte(got), []byte(nonce)) != 1 {
			return nil, errors.New("ID token: the nonce doesn't match")
		}
	}
	if sub, _ := claims["sub"].(string); sub == "" {
		return nil, errors.New("ID token: no subject")
	}
	return claims, nil
}

type oidcTokens struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	Error        string `json:"error"`
}

// oidcToken asks the provider's token endpoint for tokens, authenticating the client as the
// provider supports: HTTP Basic, the default, or in the form.
func (s *Server) oidcToken(ctx context.Context, o *OIDCOptions, d *oidcDiscovery, form url.Values) (oidcTokens, error) {
	basic := len(d.TokenAuthMethods) == 0 || slices.Contains(d.TokenAuthMethods, "client_secret_basic")
	if !basic {
		form.Set("client_id", o.ClientID)
		form.Set("client_secret", o.ClientSecret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return oidcTokens{}, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if basic {
		req.SetBasicAuth(url.QueryEscape(o.ClientID), url.QueryEscape(o.ClientSecret))
	}
	resp, err := o.client().Do(req)
	if err != nil {
		return oidcTokens{}, fmt.Errorf("%w: %v", errGitHubUnavailable, err)
	}
	defer resp.Body.Close()
	var body oidcTokens
	_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&body)
	switch {
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return body, fmt.Errorf("%w: the token endpoint answered %d", errGitHubUnavailable, resp.StatusCode)
	case body.Error == "invalid_grant" && form.Get("grant_type") == "refresh_token":
		return body, errOIDCRevoked
	case resp.StatusCode != http.StatusOK || body.AccessToken == "":
		return body, fmt.Errorf("token: HTTP %d %s", resp.StatusCode, body.Error)
	}
	return body, nil
}

// oidcIdentity is who the provider says someone is: from the ID token's claims, and the userinfo
// endpoint's for an email or groups the ID token doesn't have. emailSaid reports whether the
// provider sent an email at all, verified or not, and infoSaid whether the userinfo endpoint
// answered for this person, so that the claims it left out are ones the person doesn't have.
func (s *Server) oidcIdentity(ctx context.Context, o *OIDCOptions, d *oidcDiscovery, claims jwt.MapClaims, accessToken string) (who store.OIDCUser, emailSaid, infoSaid bool) {
	sub, _ := claims["sub"].(string)
	_, hasEmail := claims["email"]
	_, hasGroups := claims[o.groupsClaim()]
	if (!hasEmail || !hasGroups) && d.UserinfoEndpoint != "" && accessToken != "" {
		info := jwt.MapClaims{}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.UserinfoEndpoint, nil)
		if err == nil {
			req.Header.Set("Authorization", "Bearer "+accessToken)
			req.Header.Set("Accept", "application/json")
			if resp, err := o.client().Do(req); err == nil {
				if resp.StatusCode == http.StatusOK {
					_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info)
				}
				resp.Body.Close()
			}
		}
		// Only the same person's claims count.
		if infoSub, _ := info["sub"].(string); infoSub != "" && infoSub == sub {
			infoSaid = true
			for k, v := range info {
				if _, found := claims[k]; !found {
					claims[k] = v
				}
			}
		}
	}
	email, _ := claims["email"].(string)
	_, emailSaid = claims["email"]
	// Only an email the provider says is verified counts, for the email lists and as the account's
	// name: with some providers (Microsoft Entra ID among them) a user or tenant can set any email,
	// and leave email_verified out, so that one address could be anyone's.
	verified := false
	switch v := claims["email_verified"].(type) {
	case bool:
		verified = v
	case string:
		verified = v == "true"
	}
	if !verified {
		email = ""
	}
	name := email
	if name == "" {
		name, _ = claims["preferred_username"].(string)
	}
	if name == "" {
		name = sub
	}
	return store.OIDCUser{
		Key:    strings.TrimRight(d.Issuer, "/") + " " + sub,
		Name:   name,
		Email:  strings.ToLower(email),
		Groups: claimStrings(claims[o.groupsClaim()]),
	}, emailSaid, infoSaid
}

// claimStrings reads a claim that's a list of strings, or one string.
func claimStrings(v any) []string {
	var out []string
	switch v := v.(type) {
	case string:
		if v != "" {
			out = append(out, v)
		}
	case []any:
		for _, item := range v {
			if s, ok := item.(string); ok && s != "" && !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	if len(out) > 1000 {
		out = out[:1000]
	}
	return out
}

// oidcRole is the role an account gets: admin or viewer by its groups or email, or a viewer group of
// "*" for anyone the provider logs in; "" means it may not log in. Groups compare as written,
// since some providers' are ids.
func oidcRole(o *OIDCOptions, email string, groups []string) string {
	email = strings.ToLower(email)
	inAny := func(list []string) bool {
		for _, g := range groups {
			if slices.Contains(list, g) {
				return true
			}
		}
		return false
	}
	if (email != "" && slices.Contains(o.AdminEmails, email)) || inAny(o.AdminGroups) {
		return store.RoleAdmin
	}
	if (email != "" && slices.Contains(o.ViewerEmails, email)) || inAny(o.ViewerGroups) || slices.Contains(o.ViewerGroups, "*") {
		return store.RoleViewer
	}
	return ""
}

// oidcRedirect is the address the provider sends the browser back to, which must be registered.
func (s *Server) oidcRedirect(ctx context.Context, r *http.Request) string {
	if base := strings.TrimRight(s.baseURL(ctx), "/"); base != "" {
		return base + oidcCallbackPath
	}
	trust := false
	if route, err := s.routing(ctx); err == nil {
		trust = route.trustProxy
	}
	return requestOrigin(r, trust) + oidcCallbackPath
}

// oidcStart sends the browser to the provider to log in. The state, nonce, PKCE verifier and
// redirect address are kept in a short-lived cookie only this flow's paths receive.
func (s *Server) oidcStart(w http.ResponseWriter, r *http.Request) {
	o := s.oidc()
	if o == nil {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	d, err := s.oidcProviderFor(o.Issuer).document(ctx, o)
	if err != nil {
		s.log.Warn("OpenID Connect login failed", "error", err)
		http.Redirect(w, r, "/dashboard?loginError=oidc-failed", http.StatusFound)
		return
	}
	redirect := s.oidcRedirect(ctx, r)
	state, nonce, verifier := randomToken(), randomToken(), randomToken()
	remember := "0"
	if r.URL.Query().Get("remember") != "0" {
		remember = "1"
	}
	http.SetCookie(w, &http.Cookie{
		Name:  oidcCookie,
		Value: strings.Join([]string{state, verifier, remember, nonce, base64.RawURLEncoding.EncodeToString([]byte(redirect))}, "."),
		Path:  oidcCookiePath, MaxAge: int(githubLoginFor.Seconds()), HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	challenge := sha256.Sum256([]byte(verifier))
	query := url.Values{
		"client_id":             {o.ClientID},
		"redirect_uri":          {redirect},
		"response_type":         {"code"},
		"scope":                 {o.scope()},
		"state":                 {state},
		"nonce":                 {nonce},
		"code_challenge":        {base64.RawURLEncoding.EncodeToString(challenge[:])},
		"code_challenge_method": {"S256"},
	}
	sep := "?"
	if strings.Contains(d.AuthorizationEndpoint, "?") {
		sep = "&"
	}
	http.Redirect(w, r, d.AuthorizationEndpoint+sep+query.Encode(), http.StatusFound)
}

// oidcCallback finishes the login the provider sent the browser back from.
func (s *Server) oidcCallback(w http.ResponseWriter, r *http.Request) {
	// One reading of the login settings throughout, which the account's save refuses if they
	// changed meanwhile.
	l := s.currentLogin(r.Context())
	o := l.oidc
	if o == nil {
		http.NotFound(w, r)
		return
	}
	fail := func(code string) {
		http.Redirect(w, r, "/dashboard?loginError="+code, http.StatusFound)
	}
	cookie, err := r.Cookie(oidcCookie)
	http.SetCookie(w, &http.Cookie{Name: oidcCookie, Value: "", Path: oidcCookiePath, MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode})
	if err != nil {
		fail("oidc-expired")
		return
	}
	parts := strings.Split(cookie.Value, ".")
	state := r.URL.Query().Get("state")
	if len(parts) != 5 || state == "" || subtle.ConstantTimeCompare([]byte(parts[0]), []byte(state)) != 1 {
		fail("oidc-expired")
		return
	}
	if r.URL.Query().Get("error") != "" {
		fail("oidc-denied")
		return
	}
	redirect, err := base64.RawURLEncoding.DecodeString(parts[4])
	if err != nil {
		fail("oidc-expired")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	d, err := s.oidcProviderFor(o.Issuer).document(ctx, o)
	if err != nil {
		s.log.Warn("OpenID Connect login failed", "error", err)
		fail("oidc-failed")
		return
	}
	tokens, err := s.oidcToken(ctx, o, d, url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {r.URL.Query().Get("code")},
		"redirect_uri":  {string(redirect)},
		"code_verifier": {parts[1]},
	})
	if err == nil && tokens.IDToken == "" {
		err = errors.New("the provider sent no ID token")
	}
	var claims jwt.MapClaims
	if err == nil {
		claims, err = s.verifyIDToken(ctx, o, d, tokens.IDToken, parts[3])
	}
	if err != nil {
		s.log.Warn("OpenID Connect login failed", "error", err)
		fail("oidc-failed")
		return
	}
	who, _, _ := s.oidcIdentity(ctx, o, d, claims, tokens.AccessToken)
	role := oidcRole(o, who.Email, who.Groups)
	if role == "" {
		s.log.Warn("OpenID Connect login refused: not in the groups or emails", "name", who.Name, "groups", who.Groups)
		fail("oidc-not-allowed")
		return
	}
	user, err := s.store.SaveOIDCUser(ctx, who, role, tokens.RefreshToken, l.version)
	if errors.Is(err, store.ErrLoginChanged) {
		s.loginStale(ctx, l)
		s.log.Warn("OpenID Connect login refused: the login settings changed during it", "name", who.Name)
		fail("login-changed")
		return
	}
	if errors.Is(err, store.ErrUsernameTakenByPassword) {
		s.log.Warn("OpenID Connect login refused: another account has the name", "name", who.Name)
		fail("oidc-name-taken")
		return
	}
	if err != nil {
		s.log.Error("OpenID Connect login", "error", err)
		fail("oidc-failed")
		return
	}
	s.closeOtherSessions(user.ID, nil)
	if err := s.startSession(ctx, w, r, user.ID, parts[2] == "1"); err != nil {
		s.log.Error("OpenID Connect login", "error", err)
		fail("oidc-failed")
		return
	}
	s.log.Info("login", "username", user.Username, "role", user.Role, "with", o.Name)
	http.Redirect(w, r, "/dashboard", http.StatusFound)
}

// recheckOIDC has the provider confirm an account once a day while it's used, with its refresh
// token, and returns its role now, or "" when it may no longer log in. Without a refresh token,
// the settings decide from what the provider said last. When the provider can't be reached, the
// account keeps its role until the next try.
func (s *Server) recheckOIDC(ctx context.Context, user store.User) string {
	o := s.oidc()
	if o == nil {
		return ""
	}
	// An account of another issuer than the one set now has no way in.
	if !strings.HasPrefix(*user.OIDCSub, strings.TrimRight(o.Issuer, "/")+" ") {
		return ""
	}
	if user.OIDCCheckedAt != nil && s.now().Sub(*user.OIDCCheckedAt) < githubRecheckEvery {
		return user.Role
	}
	if !s.startGitHubCheck(user.ID) {
		return user.Role
	}
	failed := false
	defer func() { s.endGitHubCheck(user.ID, failed) }()
	l := s.currentLogin(ctx)
	if o = l.oidc; o == nil || !strings.HasPrefix(*user.OIDCSub, strings.TrimRight(o.Issuer, "/")+" ") {
		return ""
	}
	who := store.OIDCUser{Key: *user.OIDCSub, Name: user.Username, Email: deref(user.OIDCEmail), Groups: user.OIDCGroups}
	newRefresh := ""
	token, err := s.store.OIDCToken(ctx, user.ID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		// No refresh token: the settings decide, from what the provider said at login.
	case err != nil:
		s.log.Warn("check OpenID Connect account", "error", err)
		failed = true
		return user.Role
	default:
		ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		d, err := s.oidcProviderFor(o.Issuer).document(ctx, o)
		var tokens oidcTokens
		if err == nil {
			tokens, err = s.oidcToken(ctx, o, d, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {token}})
		}
		if errors.Is(err, errOIDCRevoked) {
			s.log.Info("OpenID Connect account logged out: the provider refused its refresh token", "username", user.Username)
			return ""
		}
		claims := jwt.MapClaims{"sub": strings.TrimPrefix(*user.OIDCSub, strings.TrimRight(o.Issuer, "/")+" ")}
		if err == nil && tokens.IDToken != "" {
			claims, err = s.verifyIDToken(ctx, o, d, tokens.IDToken, "")
		}
		if err != nil {
			s.log.Warn("check OpenID Connect account", "username", user.Username, "error", err)
			failed = true
			return user.Role
		}
		if fresh, emailSaid, infoSaid := s.oidcIdentity(ctx, o, d, claims, tokens.AccessToken); fresh.Key == *user.OIDCSub {
			// Keep what the provider left out of the answer. An email it sent but no longer says
			// is verified is dropped, so that a role from the email lists ends with it.
			if !emailSaid {
				fresh.Email = who.Email
			}
			// Groups left out of the ID token and of a userinfo answer are none: some providers,
			// Keycloak among them, leave the claim out for someone in no group. Without a userinfo
			// answer they're kept, as the ID token may not carry them at all.
			if _, has := claims[o.groupsClaim()]; !has && !infoSaid {
				fresh.Groups = who.Groups
			}
			who = fresh
		}
		newRefresh = tokens.RefreshToken
	}
	role := oidcRole(o, who.Email, who.Groups)
	if role == "" {
		if s.loginStale(ctx, l) {
			return user.Role
		}
		s.log.Info("OpenID Connect account logged out: no longer allowed", "username", user.Username)
		return ""
	}
	if err := s.store.SetOIDCChecked(ctx, user.ID, who, role, newRefresh, l.version); err != nil {
		if errors.Is(err, store.ErrLoginChanged) {
			s.loginStale(ctx, l)
			return user.Role
		}
		s.log.Warn("check OpenID Connect account", "error", err)
		failed = true
		return user.Role
	}
	if role != user.Role {
		s.log.Info("OpenID Connect account's role changed", "username", user.Username, "role", role)
	}
	return role
}

// checkOIDCIssuer reads an issuer's discovery document, for Settings > Single Sign-On's check.
func (s *Server) checkOIDCIssuer(ctx context.Context, args []json.RawMessage) (reply, error) {
	var issuer string
	if err := arg(args, 0, &issuer); err != nil {
		return nil, userErr(err)
	}
	issuer, err := oidcIssuerURL(issuer)
	if err != nil || issuer == "" {
		return nil, userErr(errors.New("Enter the issuer's https:// address.")) //nolint:staticcheck // Shown to users.
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	d, err := discover(ctx, (&OIDCOptions{}).client(), issuer)
	if err != nil {
		return nil, userErr(fmt.Errorf("the issuer's discovery document can't be read: %w", err))
	}
	return ok("issuer", d.Issuer, "authorizationEndpoint", d.AuthorizationEndpoint, "userinfo", d.UserinfoEndpoint != ""), nil
}

// oidcIssuerURL checks an issuer's address: https, or http on this machine, for trying a provider
// locally.
// localHost is whether a host is this machine, where a provider may use plain http for testing.
func localHost(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func oidcIssuerURL(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("Enter the issuer's https:// address.") //nolint:staticcheck // Shown to users.
	}
	if u.Scheme != "https" && !(u.Scheme == "http" && localHost(u.Hostname())) {
		return "", errors.New("The issuer's address must start with https://.") //nolint:staticcheck // Shown to users.
	}
	return raw, nil
}
