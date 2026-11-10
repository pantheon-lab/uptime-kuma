package ui

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/time/rate"
)

type fakeGooglePerson struct {
	sub, email, hd string
	verified       bool
	revoked        bool
}

// fakeGoogle is Google's token and userinfo endpoints, enough for logging in and daily checks.
type fakeGoogle struct {
	mu         sync.Mutex
	people     map[string]*fakeGooglePerson // by code, refresh token and access token
	challenges map[string]string            // code to PKCE challenge
	redirects  map[string]string            // code to redirect_uri
	refreshes  int
}

func (f *fakeGoogle) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch r.URL.Path {
	case "/token":
		_ = r.ParseForm()
		if r.Form.Get("client_id") != "gid" || r.Form.Get("client_secret") != "gsecret" {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_client"})
			return
		}
		if r.Form.Get("grant_type") == "refresh_token" {
			f.refreshes++
			p := f.people[r.Form.Get("refresh_token")]
			if p == nil || p.revoked {
				w.WriteHeader(http.StatusBadRequest)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "a-" + p.sub})
			return
		}
		code := r.Form.Get("code")
		sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		p := f.people[code]
		if p == nil || f.challenges[code] != base64.RawURLEncoding.EncodeToString(sum[:]) || f.redirects[code] != r.Form.Get("redirect_uri") {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "invalid_grant"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": "a-" + p.sub, "refresh_token": "r-" + p.sub, "id_token": "x"})
	case "/v1/userinfo":
		p := f.people[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
		if p == nil {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		info := map[string]any{"sub": p.sub, "email": p.email, "email_verified": p.verified}
		if p.hd != "" {
			info["hd"] = p.hd
		}
		_ = json.NewEncoder(w).Encode(info)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestGoogleLogin(t *testing.T) {
	people := []*fakeGooglePerson{
		{sub: "1", email: "Ann@Example.com", hd: "example.com", verified: true},   // an admin by email
		{sub: "2", email: "bob@example.com", hd: "example.com", verified: true},   // the domain's viewer
		{sub: "3", email: "eve@example.com", verified: true},                      // a consumer account using the domain's address
		{sub: "4", email: "zed@other.com", hd: "other.com", verified: true},       // another domain
		{sub: "5", email: "carl@example.com", hd: "example.com", verified: false}, // unverified
		{sub: "6", email: "admin@example.com", hd: "example.com", verified: true}, // a password account's name
		{sub: "7", email: "viewer@gmail.com", verified: true},                     // a viewer by email
	}
	google := &fakeGoogle{people: map[string]*fakeGooglePerson{}, challenges: map[string]string{}, redirects: map[string]string{}}
	for _, p := range people {
		google.people["r-"+p.sub], google.people["a-"+p.sub] = p, p
	}
	gs := httptest.NewServer(google)
	defer gs.Close()
	s, fs, ts := newTestServerWith(t, func(o *Options) { o.GoogleEndpoint = gs.URL })
	s.limiter = &loginLimiter{global: rate.NewLimiter(rate.Inf, 0), users: map[string]*rate.Limiter{}, perUser: rate.Inf}
	var clock sync.Mutex
	now := time.Now()
	s.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }
	postJSON(t, newClient(t), ts.URL+"/api/setup", map[string]string{"username": "admin@example.com", "password": "long enough"})
	admin := eventCaller(t, s, 1)

	form := map[string]any{"passwordLogin": true, "google": map[string]any{
		"enabled": true, "clientId": "gid", "clientSecret": "gsecret", "domains": []string{"Example.com"},
		"adminEmails": []string{"ann@example.com"}, "viewerEmails": []string{"*", "viewer@gmail.com"},
	}}
	for name, bad := range map[string]map[string]any{
		"no secret":         {"clientSecret": ""},
		"nobody":            {"adminEmails": []string{}, "viewerEmails": []string{}},
		"* without domains": {"domains": []string{}},
		"a bad email":       {"adminEmails": []string{"ann"}},
		"a bad domain":      {"domains": []string{"example com"}},
	} {
		g := map[string]any{}
		for k, v := range form["google"].(map[string]any) {
			g[k] = v
		}
		for k, v := range bad {
			g[k] = v
		}
		if r := admin("saveLoginSettings", map[string]any{"passwordLogin": true, "google": g}); r["ok"] != false {
			t.Fatalf("%s: saved: %v", name, r)
		}
	}
	if r := admin("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("save: %v", r)
	}
	if g := s.google(); g == nil || g.Domains[0] != "example.com" || g.TokenURL != gs.URL+"/token" {
		t.Fatalf("applied: %+v", g)
	}
	if info := s.info(t.Context(), false); info["googleLogin"] != true {
		t.Fatalf("info: %v", info)
	}

	// login goes through the flow as a browser does, with Google sending back a code.
	login := func(p *fakeGooglePerson) (*http.Client, string) {
		t.Helper()
		c := newClient(t)
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		res, err := c.Get(ts.URL + "/api/auth/google/start")
		if err != nil || res.StatusCode != http.StatusFound {
			t.Fatalf("start: %v %v", res, err)
		}
		authorize, _ := url.Parse(res.Header.Get("Location"))
		q := authorize.Query()
		if !strings.HasPrefix(authorize.String(), gs.URL+"/o/oauth2/v2/auth?") || q.Get("client_id") != "gid" ||
			q.Get("redirect_uri") != ts.URL+"/api/auth/google/callback" || q.Get("code_challenge_method") != "S256" ||
			q.Get("access_type") != "offline" || q.Get("hd") != "example.com" || !strings.Contains(q.Get("scope"), "email") {
			t.Fatalf("authorize: %s", authorize)
		}
		code := "code-" + p.sub
		google.mu.Lock()
		google.people[code], google.challenges[code], google.redirects[code] = p, q.Get("code_challenge"), q.Get("redirect_uri")
		google.mu.Unlock()
		res, err = c.Get(ts.URL + "/api/auth/google/callback?" + url.Values{"code": {code}, "state": {q.Get("state")}}.Encode())
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

	ann, to := login(people[0])
	if user := session(ann); to != "/dashboard" || user["username"] != "ann@example.com" || user["role"] != "admin" {
		t.Fatalf("ann: %s %v", to, user)
	}
	bob, _ := login(people[1])
	if user := session(bob); user["role"] != "viewer" {
		t.Fatalf("bob: %v", user)
	}
	viewer, _ := login(people[6])
	if user := session(viewer); user["role"] != "viewer" {
		t.Fatalf("a viewer by email: %v", user)
	}
	for i, want := range map[int]string{2: "google-not-allowed", 3: "google-not-allowed", 4: "google-not-allowed", 5: "google-name-taken"} {
		if _, to := login(people[i]); to != "/dashboard?loginError="+want {
			t.Errorf("%s: %s", people[i].email, to)
		}
	}
	// A wrong state fails.
	c := newClient(t)
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	_, _ = c.Get(ts.URL + "/api/auth/google/start")
	if res, _ := c.Get(ts.URL + "/api/auth/google/callback?code=code-1&state=wrong"); res.Header.Get("Location") != "/dashboard?loginError=google-expired" {
		t.Fatalf("wrong state: %s", res.Header.Get("Location"))
	}

	// Google accounts have no password or two-factor login here, and the Security page says so.
	if res, body := postJSON(t, ann, ts.URL+"/api/auth/change-password", map[string]any{"currentPassword": "", "newPassword": "a long new password"}); res.StatusCode != http.StatusBadRequest || body["code"] != "GOOGLE_ACCOUNT" {
		t.Fatalf("change password: %d %v", res.StatusCode, body)
	}
	annID := fs.users[1].ID
	asAnn := eventCaller(t, s, annID)
	if r := asAnn("getOwnAccount"); r["method"] != "google" || r["email"] != "ann@example.com" || r["domain"] != "example.com" {
		t.Fatalf("own account: %v", r)
	}

	// Ann, an admin logged in with Google, turns passwords off; the password admin couldn't.
	off := map[string]any{"passwordLogin": false, "google": form["google"]}
	if r := admin("saveLoginSettings", off); r["ok"] != false {
		t.Fatalf("a password admin turned passwords off: %v", r)
	}
	if r := asAnn("saveLoginSettings", off); r["ok"] != true {
		t.Fatalf("passwords off from Google: %v", r)
	}
	if r := asAnn("saveLoginSettings", map[string]any{"passwordLogin": false, "google": map[string]any{
		"enabled": true, "clientId": "gid", "clientSecret": "", "domains": []string{"example.com"}, "adminEmails": []string{"ann@example.com", "new@example.com"},
		"viewerEmails": []string{"*", "viewer@gmail.com"}}}); r["ok"] != false {
		t.Fatalf("changed Google with passwords off: %v", r)
	}
	if r := asAnn("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("passwords on: %v", r)
	}

	// A day later, Google confirms the accounts again: a revoked one is logged out.
	clock.Lock()
	now = now.Add(25 * time.Hour)
	clock.Unlock()
	google.mu.Lock()
	people[1].revoked = true
	google.mu.Unlock()
	if user := session(bob); user != nil {
		t.Fatalf("a revoked account stayed logged in: %v", user)
	}
	if user := session(ann); user["role"] != "admin" {
		t.Fatalf("ann after the check: %v", user)
	}
	google.mu.Lock()
	refreshes := google.refreshes
	google.mu.Unlock()
	if refreshes != 2 {
		t.Fatalf("refreshes: %d", refreshes)
	}
	// Within the day, nobody asks Google again. (The fake store stamps checks with the real clock.)
	fs.mu.Lock()
	for i := range fs.users {
		if fs.users[i].GoogleCheckedAt != nil {
			at := now
			fs.users[i].GoogleCheckedAt = &at
		}
	}
	fs.mu.Unlock()
	if session(ann); google.refreshes != 2 {
		t.Fatalf("asked Google again within the day: %d", google.refreshes)
	}

	// Changing the lists applies at the next request: the viewer becomes an admin.
	g := form["google"].(map[string]any)
	g["adminEmails"] = []string{"ann@example.com", "viewer@gmail.com"}
	if r := admin("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("lists: %v", r)
	}
	if user := session(viewer); user["role"] != "admin" {
		t.Fatalf("the viewer after the lists changed: %v", user)
	}

	// Turning Google off logs its accounts out, at their next request.
	g["enabled"] = false
	if r := admin("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("Google off: %v", r)
	}
	if user := session(ann); user != nil {
		t.Fatalf("a Google account stayed with Google off: %v", user)
	}
	if res, _ := http.Get(ts.URL + "/api/auth/google/start"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("start with Google off: %d", res.StatusCode)
	}
}

func TestGoogleRole(t *testing.T) {
	g := &GoogleOptions{Domains: []string{"example.com"}, AdminEmails: []string{"ann@example.com"}, ViewerEmails: []string{"*"}}
	for _, c := range []struct {
		email, domain string
		verified      bool
		want          string
	}{
		{"ANN@example.com", "example.com", true, "admin"},
		{"ann@example.com", "", true, "admin"},
		{"bob@example.com", "example.com", true, "viewer"},
		{"bob@example.com", "", true, ""},
		{"bob@example.com", "EXAMPLE.COM", true, "viewer"},
		{"zed@other.com", "other.com", true, ""},
		{"ann@example.com", "example.com", false, ""},
	} {
		if got := googleRole(g, c.email, c.verified, c.domain); got != c.want {
			t.Errorf("%s %s %v: %q, want %q", c.email, c.domain, c.verified, got, c.want)
		}
	}
}
