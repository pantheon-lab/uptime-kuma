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

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

type fakeGitHubUser struct {
	id     int64
	login  string
	member bool
	teams  []string
}

// fakeGitHub is GitHub's OAuth and API, enough for logging in.
type fakeGitHub struct {
	mu         sync.Mutex
	users      map[string]*fakeGitHubUser // by token
	codes      map[string]string          // code to token
	challenges map[string]string          // state to PKCE challenge
	down       bool
}

func (f *fakeGitHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.down {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	if r.URL.Path == "/login/oauth/access_token" {
		_ = r.ParseForm()
		token, found := f.codes[r.Form.Get("code")]
		verifier := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
		challenge := base64.RawURLEncoding.EncodeToString(verifier[:])
		ok := found && r.Form.Get("client_id") == "client" && r.Form.Get("client_secret") == "secret" && r.Header.Get("Accept") == "application/json"
		var pkce bool
		for _, c := range f.challenges {
			pkce = pkce || c == challenge
		}
		if !ok || !pkce {
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "bad_verification_code"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"access_token": token, "scope": "read:org", "token_type": "bearer"})
		return
	}
	user := f.users[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/user":
		_ = json.NewEncoder(w).Encode(map[string]any{"id": user.id, "login": user.login})
	case "/user/memberships/orgs/pantheon-lab":
		if !user.member {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"state": "active"})
	case "/user/teams":
		list := []map[string]any{{"slug": "Elsewhere", "organization": map[string]any{"login": "other-org"}}}
		for _, team := range user.teams {
			list = append(list, map[string]any{"slug": team, "organization": map[string]any{"login": "Pantheon-Lab"}})
		}
		_ = json.NewEncoder(w).Encode(list)
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestGitHubLogin(t *testing.T) {
	github := &fakeGitHub{
		users: map[string]*fakeGitHubUser{
			"t-alice": {id: 1, login: "alice", member: true, teams: []string{"sre"}},
			"t-bob":   {id: 2, login: "bob", member: true, teams: []string{"support"}},
			"t-carol": {id: 3, login: "carol", member: true, teams: []string{"marketing"}},
			"t-dave":  {id: 4, login: "dave", member: false, teams: []string{"sre"}},
			// Elsewhere is a team of another organization, which counts for nothing.
			"t-erin": {id: 5, login: "erin", member: true},
		},
		codes:      map[string]string{},
		challenges: map[string]string{},
	}
	gh := httptest.NewServer(github)
	defer gh.Close()
	s, fs, ts := newTestServerWith(t, func(o *Options) {
		o.GitHub = &GitHubOptions{
			ClientID: "client", ClientSecret: "secret", Org: "pantheon-lab",
			AdminTeams: []string{"sre"}, ViewerTeams: []string{"support", "elsewhere"},
			WebURL: gh.URL, APIURL: gh.URL,
		}
	})
	s.limiter = &loginLimiter{global: rate.NewLimiter(rate.Inf, 0), users: map[string]*rate.Limiter{}, perUser: rate.Inf}
	var clock sync.Mutex
	now := time.Now()
	s.now = func() time.Time { clock.Lock(); defer clock.Unlock(); return now }

	noRedirect := func(c *http.Client) *http.Client {
		c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		return c
	}
	// login goes through the flow as a browser does, with GitHub sending back code.
	login := func(token string, tamper func(q url.Values)) (*http.Client, string) {
		t.Helper()
		c := noRedirect(newClient(t))
		res, err := c.Get(ts.URL + "/api/auth/github/start?remember=1")
		if err != nil {
			t.Fatal(err)
		}
		authorize, _ := url.Parse(res.Header.Get("Location"))
		q := authorize.Query()
		if res.StatusCode != http.StatusFound || authorize.Path != "/login/oauth/authorize" || q.Get("client_id") != "client" ||
			q.Get("scope") != "read:org" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
			t.Fatalf("start: %d %s", res.StatusCode, authorize)
		}
		code := "code-" + token
		github.mu.Lock()
		github.codes[code] = token
		github.challenges[q.Get("state")] = q.Get("code_challenge")
		github.mu.Unlock()
		back := url.Values{"code": {code}, "state": {q.Get("state")}}
		if tamper != nil {
			tamper(back)
		}
		res, err = c.Get(ts.URL + "/api/auth/github/callback?" + back.Encode())
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != http.StatusFound {
			t.Fatalf("callback: %d", res.StatusCode)
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

	alice, to := login("t-alice", nil)
	if user := session(alice); to != "/dashboard" || user["username"] != "@alice" || user["role"] != "admin" {
		t.Fatalf("alice: %s %v", to, user)
	}
	bob, _ := login("t-bob", nil)
	if user := session(bob); user["role"] != "viewer" {
		t.Fatalf("bob: %v", user)
	}
	for token, want := range map[string]string{"t-carol": "github-not-allowed", "t-dave": "github-not-allowed", "t-erin": "github-not-allowed"} {
		c, to := login(token, nil)
		if !strings.HasSuffix(to, "loginError="+want) || session(c) != nil {
			t.Errorf("%s: %s", token, to)
		}
	}
	if c, to := login("t-alice", func(q url.Values) { q.Set("state", "forged") }); !strings.HasSuffix(to, "github-expired") || session(c) != nil {
		t.Errorf("forged state: %s", to)
	}
	if c, to := login("t-alice", func(q url.Values) { q.Set("error", "access_denied"); q.Del("code") }); !strings.HasSuffix(to, "github-denied") || session(c) != nil {
		t.Errorf("denied: %s", to)
	}
	if c, to := login("t-alice", func(q url.Values) { q.Set("code", "made-up") }); !strings.HasSuffix(to, "github-failed") || session(c) != nil {
		t.Errorf("bad code: %s", to)
	}
	// A callback without the start's cookie, such as one sent to someone else.
	res, _ := noRedirect(newClient(t)).Get(ts.URL + "/api/auth/github/callback?code=code-t-alice&state=x")
	if !strings.HasSuffix(res.Header.Get("Location"), "github-expired") {
		t.Errorf("no cookie: %s", res.Header.Get("Location"))
	}

	// A day later: Alice moved to the support team, Bob left the organization, and the
	// sessions are checked again when used.
	github.mu.Lock()
	github.users["t-alice"].teams = []string{"support"}
	github.users["t-bob"].member = false
	github.mu.Unlock()
	clock.Lock()
	now = now.Add(25 * time.Hour)
	clock.Unlock()
	if user := session(alice); user["role"] != "viewer" {
		t.Fatalf("alice after moving: %v", user)
	}
	if user := session(bob); user != nil {
		t.Fatalf("bob after leaving: %v", user)
	}
	for _, sess := range fs.sessions {
		if sess.UserID == 2 {
			t.Fatal("bob's session wasn't ended")
		}
	}

	// GitHub down at the next check keeps the session until the check after.
	github.mu.Lock()
	github.down = true
	github.mu.Unlock()
	clock.Lock()
	now = now.Add(25 * time.Hour)
	clock.Unlock()
	if user := session(alice); user == nil || user["role"] != "viewer" {
		t.Fatalf("alice with GitHub down: %v", user)
	}

	// A password can't make an account that looks like a GitHub one.
	if err := checkUsername("@alice"); err == nil {
		t.Error("a password account may start with @")
	}
}

func TestPasswordLoginOff(t *testing.T) {
	_, _, ts := newTestServerWith(t, func(o *Options) { o.DisablePasswordLogin = true })
	c := newClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	res, body := postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "long enough"})
	if res.StatusCode != http.StatusForbidden || body["code"] != "PASSWORD_LOGIN_DISABLED" {
		t.Fatalf("%d %v", res.StatusCode, body)
	}
}

func TestGitHubLoginOffEndsItsSessions(t *testing.T) {
	_, fs, ts := newTestServer(t)
	id := int64(9)
	fs.users = append(fs.users, storeUserWithGitHub(1, id))
	c := newClient(t)
	cookieFor(t, fs, c, ts.URL, 1)
	res, err := c.Get(ts.URL + "/api/auth/get-session")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var body map[string]any
	_ = json.NewDecoder(res.Body).Decode(&body)
	if body != nil && body["user"] != nil {
		t.Fatalf("a GitHub account stayed logged in with GitHub login off: %v", body)
	}
}

func storeUserWithGitHub(id int32, githubID int64) store.User {
	checked := time.Now()
	return store.User{ID: id, Username: "@someone", Role: store.RoleAdmin, GitHubID: &githubID, GitHubCheckedAt: &checked}
}

// cookieFor logs a client in as an account, with a session made in the store.
func cookieFor(t *testing.T, fs *fakeStore, c *http.Client, base string, userID int32) {
	t.Helper()
	token := "session-token-for-test"
	fs.mu.Lock()
	fs.sessions[string(hashToken(token))] = store.Session{UserID: userID, ExpiresAt: time.Now().Add(time.Hour)}
	fs.mu.Unlock()
	u, _ := url.Parse(base)
	c.Jar.SetCookies(u, []*http.Cookie{{Name: sessionCookie, Value: token, Path: "/"}})
}
