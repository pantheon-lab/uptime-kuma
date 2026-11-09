package ui

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"golang.org/x/time/rate"
)

type fakeOIDCPerson struct {
	sub, email, name string
	groups           []string
	// verified is the email_verified claim: nil leaves it out.
	verified *bool
	// emailInUserinfo keeps the email and groups out of the ID token, for userinfo to say.
	emailInUserinfo bool
	revoked         bool
}

// fakeIdP is an OpenID Connect provider: discovery, keys, token (HTTP Basic client
// authentication, PKCE, nonce), userinfo and refresh.
type fakeIdP struct {
	mu        sync.Mutex
	srv       *httptest.Server
	rsaKey    *rsa.PrivateKey
	ecKey     *ecdsa.PrivateKey
	useEC     bool
	people    map[string]*fakeOIDCPerson // by code, access and refresh token
	pending   map[string][3]string       // code: challenge, nonce, redirect
	refreshes int
	// rotate gives a new refresh token at each refresh.
	rotate bool
	// issuerInDoc replaces the discovery document's issuer.
	issuerInDoc string
}

func b64int(i *big.Int) string { return base64.RawURLEncoding.EncodeToString(i.Bytes()) }

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	ek, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	f := &fakeIdP{rsaKey: rk, ecKey: ek, people: map[string]*fakeOIDCPerson{}, pending: map[string][3]string{}}
	f.srv = httptest.NewServer(f)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeIdP) sign(claims jwt.MapClaims) string {
	if f.useEC {
		tok := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		tok.Header["kid"] = "ec-1"
		s, _ := tok.SignedString(f.ecKey)
		return s
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, claims)
	tok.Header["kid"] = "rsa-1"
	s, _ := tok.SignedString(f.rsaKey)
	return s
}

func (f *fakeIdP) idToken(p *fakeOIDCPerson, nonce string) string {
	claims := jwt.MapClaims{"iss": f.srv.URL, "sub": p.sub, "aud": "kuma", "exp": time.Now().Add(5 * time.Minute).Unix(),
		"iat": time.Now().Unix(), "preferred_username": p.name}
	if nonce != "" {
		claims["nonce"] = nonce
	}
	if !p.emailInUserinfo {
		if p.email != "" {
			claims["email"] = p.email
		}
		if p.verified != nil {
			claims["email_verified"] = *p.verified
		}
		if p.groups != nil {
			claims["groups"] = p.groups
		}
	}
	return f.sign(claims)
}

func (f *fakeIdP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	reply := func(status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}
	switch r.URL.Path {
	case "/.well-known/openid-configuration":
		iss := f.srv.URL
		if f.issuerInDoc != "" {
			iss = f.issuerInDoc
		}
		reply(200, map[string]any{"issuer": iss, "authorization_endpoint": f.srv.URL + "/authorize", "token_endpoint": f.srv.URL + "/token",
			"userinfo_endpoint": f.srv.URL + "/userinfo", "jwks_uri": f.srv.URL + "/jwks",
			"token_endpoint_auth_methods_supported": []string{"client_secret_basic"}})
	case "/jwks":
		reply(200, map[string]any{"keys": []map[string]any{
			{"kty": "RSA", "kid": "rsa-1", "use": "sig", "n": b64int(f.rsaKey.N), "e": b64int(big.NewInt(int64(f.rsaKey.E)))},
			{"kty": "EC", "kid": "ec-1", "crv": "P-256", "x": b64int(f.ecKey.X), "y": b64int(f.ecKey.Y)},
		}})
	case "/token":
		_ = r.ParseForm()
		id, secret, ok := r.BasicAuth()
		if !ok || id != "kuma" || secret != "s3cret" || r.Form.Get("client_secret") != "" {
			reply(401, map[string]string{"error": "invalid_client"})
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshes++
			p := f.people[r.Form.Get("refresh_token")]
			if p == nil || p.revoked {
				reply(400, map[string]string{"error": "invalid_grant"})
				return
			}
			out := map[string]any{"access_token": "a-" + p.sub, "id_token": f.idToken(p, "")}
			if f.rotate {
				next := "r2-" + p.sub
				f.people[next] = p
				out["refresh_token"] = next
			}
			reply(200, out)
			return
		}
		code := r.Form.Get("code")
		p, pend := f.people[code], f.pending[code]
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		if p == nil || pend[0] != base64.RawURLEncoding.EncodeToString(sum[:]) || pend[2] != r.Form.Get("redirect_uri") {
			reply(400, map[string]string{"error": "invalid_grant"})
			return
		}
		f.people["r-"+p.sub], f.people["a-"+p.sub] = p, p
		reply(200, map[string]any{"access_token": "a-" + p.sub, "refresh_token": "r-" + p.sub, "id_token": f.idToken(p, pend[1])})
	case "/userinfo":
		p := f.people[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if p == nil {
			reply(401, map[string]any{})
			return
		}
		info := map[string]any{"sub": p.sub}
		if p.email != "" {
			info["email"] = p.email
		}
		if p.verified != nil {
			info["email_verified"] = *p.verified
		}
		if p.groups != nil {
			info["groups"] = p.groups
		}
		reply(200, info)
	default:
		reply(404, map[string]any{})
	}
}

func TestOIDCLogin(t *testing.T) {
	idp := newFakeIdP(t)
	no := false
	people := map[string]*fakeOIDCPerson{
		"ops":     {sub: "u1", email: "Ops@Example.com", name: "ops", groups: []string{"Kuma-Admins"}},
		"support": {sub: "u2", email: "sup@example.com", name: "sup", groups: []string{"Support"}, emailInUserinfo: true},
		"listed":  {sub: "u3", email: "listed@example.com", name: "listed"},
		"spoof":   {sub: "u4", email: "listed2@example.com", name: "spoof", verified: &no},
		"nobody":  {sub: "u5", email: "nobody@example.com", name: "nobody", groups: []string{"Sales"}},
		"taken":   {sub: "u6", email: "admin@example.com", name: "taken", groups: []string{"Kuma-Admins"}},
	}
	s, fs, ts := newTestServer(t)
	s.limiter = &loginLimiter{global: rate.NewLimiter(rate.Inf, 0), users: map[string]*rate.Limiter{}, perUser: rate.Inf}
	var clock sync.Mutex
	now := time.Now()
	s.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }
	postJSON(t, newClient(t), ts.URL+"/api/setup", map[string]string{"username": "admin@example.com", "password": "long enough"})
	admin := eventCaller(t, s, 1)

	oidcForm := map[string]any{
		"enabled": true, "name": "Okta", "issuer": idp.srv.URL + "/", "clientId": "kuma", "clientSecret": "s3cret",
		"scopes": []string{"email", "profile", "groups", "offline_access"}, "groupsClaim": "",
		"adminGroups": []string{"Kuma-Admins"}, "viewerGroups": []string{"Support"},
		"adminEmails": []string{}, "viewerEmails": []string{"Listed@example.com", "listed2@example.com"},
	}
	form := map[string]any{"passwordLogin": true, "oidc": oidcForm}
	with := func(changes map[string]any) map[string]any {
		o := map[string]any{}
		for k, v := range oidcForm {
			o[k] = v
		}
		for k, v := range changes {
			o[k] = v
		}
		return map[string]any{"passwordLogin": true, "oidc": o}
	}
	for name, bad := range map[string]map[string]any{
		"no name":                 {"name": ""},
		"no issuer":               {"issuer": ""},
		"a plain http issuer":     {"issuer": "http://idp.example.com"},
		"no secret":               {"clientSecret": ""},
		"nobody may log in":       {"adminGroups": []string{}, "viewerGroups": []string{}, "viewerEmails": []string{}},
		"a scope with a space":    {"scopes": []string{"a b"}},
		"an email that isn't one": {"viewerEmails": []string{"listed"}},
	} {
		if r := admin("saveLoginSettings", with(bad)); r["ok"] != false {
			t.Fatalf("%s: saved: %v", name, r)
		}
	}
	if r := admin("checkOIDCIssuer", idp.srv.URL); r["ok"] != true || r["issuer"] != idp.srv.URL {
		t.Fatalf("check: %v", r)
	}
	if r := admin("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("save: %v", r)
	}
	if o := s.oidc(); o == nil || o.Issuer != idp.srv.URL || o.ViewerEmails[0] != "listed@example.com" || o.AdminGroups[0] != "Kuma-Admins" {
		t.Fatalf("applied: %+v", o)
	}
	if info := s.info(context.Background(), false); info["oidcLogin"] != "Okta" {
		t.Fatalf("info: %v", info)
	}

	login := func(p *fakeOIDCPerson, tamper func(url.Values)) (*http.Client, string) {
		t.Helper()
		c := newClient(t)
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := c.Get(ts.URL + "/api/auth/oidc/start")
		if err != nil || res.StatusCode != http.StatusFound {
			t.Fatalf("start: %v %v", res, err)
		}
		authorize, _ := url.Parse(res.Header.Get("Location"))
		q := authorize.Query()
		if authorize.Path != "/authorize" || q.Get("client_id") != "kuma" || q.Get("nonce") == "" ||
			q.Get("scope") != "openid email profile groups offline_access" || q.Get("code_challenge_method") != "S256" ||
			q.Get("redirect_uri") != ts.URL+"/api/auth/oidc/callback" {
			t.Fatalf("authorize: %s", authorize)
		}
		code := "code-" + p.sub
		idp.mu.Lock()
		idp.people[code] = p
		idp.pending[code] = [3]string{q.Get("code_challenge"), q.Get("nonce"), q.Get("redirect_uri")}
		idp.mu.Unlock()
		back := url.Values{"code": {code}, "state": {q.Get("state")}}
		if tamper != nil {
			tamper(back)
		}
		res, err = c.Get(ts.URL + "/api/auth/oidc/callback?" + back.Encode())
		if err != nil || res.StatusCode != http.StatusFound {
			t.Fatalf("callback: %v %v", res, err)
		}
		return c, res.Header.Get("Location")
	}
	session := func(c *http.Client) map[string]any {
		t.Helper()
		res, err := c.Get(ts.URL + "/api/auth/get-session")
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var body map[string]any
		_ = json.NewDecoder(res.Body).Decode(&body)
		user, _ := body["user"].(map[string]any)
		return user
	}

	ops, to := login(people["ops"], nil)
	if user := session(ops); to != "/dashboard" || user["username"] != "ops@example.com" || user["role"] != "admin" {
		t.Fatalf("ops: %s %v", to, user)
	}
	// Email and groups from userinfo, when the ID token leaves them out.
	sup, _ := login(people["support"], nil)
	if user := session(sup); user["username"] != "sup@example.com" || user["role"] != "viewer" {
		t.Fatalf("support: %v", user)
	}
	// An EC-signed ID token, and a viewer by email.
	idp.mu.Lock()
	idp.useEC = true
	idp.mu.Unlock()
	listed, _ := login(people["listed"], nil)
	if user := session(listed); user["role"] != "viewer" {
		t.Fatalf("listed: %v", user)
	}
	idp.mu.Lock()
	idp.useEC = false
	idp.mu.Unlock()
	for who, want := range map[string]string{
		"spoof":  "oidc-not-allowed", // email_verified false: the listed email doesn't count
		"nobody": "oidc-not-allowed",
		"taken":  "oidc-name-taken",
	} {
		if _, to := login(people[who], nil); to != "/dashboard?loginError="+want {
			t.Errorf("%s: %s", who, to)
		}
	}
	if _, to := login(people["ops"], func(q url.Values) { q.Set("state", "wrong") }); to != "/dashboard?loginError=oidc-expired" {
		t.Errorf("wrong state: %s", to)
	}

	// The Security page, and no password changes for the account.
	opsID := fs.users[1].ID
	asOps := eventCaller(t, s, opsID)
	if r := asOps("getOwnAccount"); r["method"] != "oidc" || r["provider"] != "Okta" || r["email"] != "ops@example.com" {
		t.Fatalf("own account: %v", r)
	}
	if res, body := postJSON(t, ops, ts.URL+"/api/auth/change-password", map[string]any{"currentPassword": "", "newPassword": "a long new password"}); res.StatusCode != http.StatusBadRequest || body["code"] != "OIDC_ACCOUNT" {
		t.Fatalf("change password: %d %v", res.StatusCode, body)
	}

	// Ops turns passwords off; the provider's settings are then held.
	off := map[string]any{"passwordLogin": false, "oidc": oidcForm}
	if r := admin("saveLoginSettings", off); r["ok"] != false {
		t.Fatalf("a password admin turned passwords off: %v", r)
	}
	if r := asOps("saveLoginSettings", off); r["ok"] != true {
		t.Fatalf("passwords off: %v", r)
	}
	held := with(map[string]any{"adminGroups": []string{"Other"}})
	held["passwordLogin"] = false
	if r := asOps("saveLoginSettings", held); r["ok"] != false {
		t.Fatalf("changed the provider with passwords off: %v", r)
	}
	if r := asOps("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("passwords on: %v", r)
	}

	// A day later the provider confirms the accounts: a revoked one is logged out, one whose
	// groups changed changes role, and a rotated refresh token is kept.
	clock.Lock()
	now = now.Add(25 * time.Hour)
	clock.Unlock()
	idp.mu.Lock()
	idp.rotate = true
	people["support"].revoked = true
	people["ops"].groups = []string{"Support"}
	idp.mu.Unlock()
	if user := session(sup); user != nil {
		t.Fatalf("a revoked account stayed: %v", user)
	}
	if user := session(ops); user["role"] != "viewer" {
		t.Fatalf("ops after leaving the admin group: %v", user)
	}
	if token, _ := fs.OIDCToken(context.Background(), opsID); token != "r2-u1" {
		t.Fatalf("rotated refresh token: %q", token)
	}

	// Another issuer logs the old one's accounts out.
	idp2 := newFakeIdP(t)
	if r := admin("saveLoginSettings", with(map[string]any{"issuer": idp2.srv.URL, "clientSecret": "s3cret"})); r["ok"] != true {
		t.Fatalf("new issuer: %v", r)
	}
	if user := session(listed); user != nil {
		t.Fatalf("an account of the old issuer stayed: %v", user)
	}
}

func TestOIDCVerifyIDToken(t *testing.T) {
	idp := newFakeIdP(t)
	s, _, _ := newTestServer(t)
	o := &OIDCOptions{Issuer: idp.srv.URL, ClientID: "kuma", ClientSecret: "s3cret"}
	d, err := discover(context.Background(), o.client(), o.Issuer)
	if err != nil {
		t.Fatal(err)
	}
	base := func() jwt.MapClaims {
		return jwt.MapClaims{"iss": idp.srv.URL, "sub": "u1", "aud": "kuma", "exp": time.Now().Add(time.Minute).Unix(),
			"iat": time.Now().Unix(), "nonce": "n1"}
	}
	verify := func(raw string) error {
		_, err := s.verifyIDToken(context.Background(), o, d, raw, "n1")
		return err
	}
	if err := verify(idp.sign(base())); err != nil {
		t.Fatalf("a good token: %v", err)
	}
	idp.useEC = true
	if err := verify(idp.sign(base())); err != nil {
		t.Fatalf("a good EC token: %v", err)
	}
	idp.useEC = false
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	forged := jwt.NewWithClaims(jwt.SigningMethodRS256, base())
	forged.Header["kid"] = "rsa-1"
	forgedRaw, _ := forged.SignedString(other)
	hs := jwt.NewWithClaims(jwt.SigningMethodHS256, base())
	hsRaw, _ := hs.SignedString([]byte("s3cret"))
	none := jwt.NewWithClaims(jwt.SigningMethodNone, base())
	noneRaw, _ := none.SignedString(jwt.UnsafeAllowNoneSignatureType)
	cases := map[string]string{
		"another key's signature": forgedRaw,
		"HS256 with the secret":   hsRaw,
		"no signature":            noneRaw,
		"an unknown key id": func() string {
			tk := jwt.NewWithClaims(jwt.SigningMethodRS256, base())
			tk.Header["kid"] = "nope"
			r, _ := tk.SignedString(idp.rsaKey)
			return r
		}(),
	}
	for name, change := range map[string]func(jwt.MapClaims){
		"another issuer":          func(c jwt.MapClaims) { c["iss"] = "https://evil.example" },
		"another audience":        func(c jwt.MapClaims) { c["aud"] = "other" },
		"expired":                 func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-5 * time.Minute).Unix() },
		"no expiry":               func(c jwt.MapClaims) { delete(c, "exp") },
		"another nonce":           func(c jwt.MapClaims) { c["nonce"] = "n2" },
		"no subject":              func(c jwt.MapClaims) { delete(c, "sub") },
		"issued to another party": func(c jwt.MapClaims) { c["aud"] = []string{"kuma", "other"}; c["azp"] = "other" },
	} {
		c := base()
		change(c)
		cases[name] = idp.sign(c)
	}
	for name, raw := range cases {
		if err := verify(raw); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}

	// A discovery document for another issuer is refused.
	idp.issuerInDoc = "https://evil.example"
	if _, err := discover(context.Background(), o.client(), o.Issuer); err == nil {
		t.Error("another issuer's discovery document was accepted")
	}
}

func TestOIDCRole(t *testing.T) {
	o := &OIDCOptions{AdminGroups: []string{"Admins"}, ViewerGroups: []string{"*"}, AdminEmails: []string{"boss@example.com"}}
	for _, c := range []struct {
		email  string
		groups []string
		want   string
	}{
		{"boss@example.com", nil, "admin"},
		{"BOSS@example.com", nil, "admin"},
		{"x@example.com", []string{"Admins"}, "admin"},
		{"x@example.com", []string{"admins"}, "viewer"}, // groups compare as written
		{"", nil, "viewer"},
	} {
		if got := oidcRole(o, c.email, c.groups); got != c.want {
			t.Errorf("%s %v: %q, want %q", c.email, c.groups, got, c.want)
		}
	}
	if got := oidcRole(&OIDCOptions{ViewerGroups: []string{"Support"}}, "x@example.com", []string{"Sales"}); got != "" {
		t.Errorf("someone in no group: %q", got)
	}
	if got := claimStrings([]any{"a", "b", "a", 3}); strings.Join(got, ",") != "a,b" {
		t.Errorf("claim list: %v", got)
	}
	if got := claimStrings("solo"); len(got) != 1 || got[0] != "solo" {
		t.Errorf("claim string: %v", got)
	}
}
