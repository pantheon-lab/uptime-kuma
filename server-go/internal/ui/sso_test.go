package ui

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/time/rate"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// eventCaller sends realtime events as an account.
func eventCaller(t *testing.T, s *Server, userID int32) func(event string, args ...any) reply {
	c := &conn{userID: userID, admin: true, out: make(chan []byte, 256), done: make(chan struct{})}
	return func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(context.Background(), c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}
}

func TestLoginSettingsFromTheUI(t *testing.T) {
	github := &fakeGitHub{
		users: map[string]*fakeGitHubUser{
			"t-alice": {id: 1, login: "alice", member: true, teams: []string{"sre"}},
		},
		codes:      map[string]string{},
		challenges: map[string]string{},
	}
	gh := httptest.NewServer(github)
	defer gh.Close()
	s, fs, ts := newTestServer(t)
	s.limiter = &loginLimiter{global: rate.NewLimiter(rate.Inf, 0), users: map[string]*rate.Limiter{}, perUser: rate.Inf}
	c := newClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	admin := eventCaller(t, s, 1)

	r := admin("getLoginSettings")
	settings := r["settings"].(map[string]any)
	if r["ok"] != true || r["managedByEnv"] != false || settings["githubEnabled"] != false || settings["passwordLogin"] != true {
		t.Fatalf("defaults: %v", r)
	}
	if res, _ := http.Get(ts.URL + "/api/auth/github/start"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("GitHub login before it's set up: %d", res.StatusCode)
	}

	form := map[string]any{
		"githubEnabled": true, "clientId": "client", "clientSecret": "secret", "org": "pantheon-lab",
		"adminTeams": []string{" SRE ", "sre", ""}, "viewerTeams": []string{}, "webURL": gh.URL + "/", "apiURL": gh.URL,
		"passwordLogin": true,
	}
	with := func(changes map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range form {
			out[k] = v
		}
		for k, v := range changes {
			out[k] = v
		}
		return out
	}
	for name, bad := range map[string]map[string]any{
		"nothing on":   {"githubEnabled": false, "passwordLogin": false},
		"no secret":    {"clientSecret": ""},
		"no org":       {"org": " "},
		"no teams":     {"adminTeams": []string{}},
		"a bad team":   {"adminTeams": []string{"site reliability"}},
		"a bad server": {"webURL": "ftp://github.example"},
		// The admin logs in with a password, which can't show that GitHub login works.
		"passwords off": {"passwordLogin": false},
	} {
		if r := admin("saveLoginSettings", with(bad)); r["ok"] != false {
			t.Fatalf("%s: saved: %v", name, r)
		}
	}
	if r := admin("saveLoginSettings", form); r["ok"] != true {
		t.Fatalf("save: %v", r)
	}
	if g := s.github(); g == nil || g.ClientSecret != "secret" || strings.Join(g.AdminTeams, ",") != "sre" || g.WebURL != gh.URL {
		t.Fatalf("applied: %+v", g)
	}
	if info := s.info(context.Background(), false); info["githubLogin"] != true || info["passwordLogin"] != true {
		t.Fatalf("info: %v", info)
	}
	r = admin("getLoginSettings")
	if raw, _ := json.Marshal(r); strings.Contains(string(raw), `"secret"`) || r["settings"].(map[string]any)["hasClientSecret"] != true {
		t.Fatalf("the secret: %s", raw)
	}
	// An empty secret keeps the saved one.
	if r := admin("saveLoginSettings", with(map[string]any{"clientSecret": ""})); r["ok"] != true || s.github().ClientSecret != "secret" {
		t.Fatalf("keep the secret: %v", r)
	}

	// Alice logs in with GitHub, at once.
	alice := newClient(t)
	alice.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := alice.Get(ts.URL + "/api/auth/github/start")
	if err != nil || res.StatusCode != http.StatusFound {
		t.Fatalf("start: %v %v", res, err)
	}
	authorize, _ := url.Parse(res.Header.Get("Location"))
	if !strings.HasPrefix(authorize.String(), gh.URL+"/login/oauth/authorize") {
		t.Fatalf("authorize: %s", authorize)
	}
	github.mu.Lock()
	github.codes["code-a"] = "t-alice"
	github.challenges[authorize.Query().Get("state")] = authorize.Query().Get("code_challenge")
	github.mu.Unlock()
	res, err = alice.Get(ts.URL + "/api/auth/github/callback?" + url.Values{"code": {"code-a"}, "state": {authorize.Query().Get("state")}}.Encode())
	if err != nil || res.Header.Get("Location") != "/dashboard" {
		t.Fatalf("callback: %v %v", res.Header.Get("Location"), err)
	}
	aliceID := fs.users[len(fs.users)-1].ID

	// Alice, an admin logged in with GitHub, turns passwords off; GitHub's settings then stay.
	asAlice := eventCaller(t, s, aliceID)
	if r := asAlice("saveLoginSettings", with(map[string]any{"passwordLogin": false, "adminTeams": []string{"ops"}})); r["ok"] != false {
		t.Fatalf("changed GitHub while turning passwords off: %v", r)
	}
	if r := asAlice("saveLoginSettings", with(map[string]any{"passwordLogin": false})); r["ok"] != true {
		t.Fatalf("passwords off: %v", r)
	}
	if res, body := postJSON(t, newClient(t), ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "long enough"}); res.StatusCode != http.StatusForbidden || body["code"] != "PASSWORD_LOGIN_DISABLED" {
		t.Fatalf("a password login with passwords off: %d %v", res.StatusCode, body)
	}
	if r := asAlice("saveLoginSettings", with(map[string]any{"passwordLogin": false, "org": "elsewhere"})); r["ok"] != false {
		t.Fatalf("changed the org with passwords off: %v", r)
	}

	// With passwords back on, changing the teams has every GitHub account checked again.
	if r := asAlice("saveLoginSettings", with(map[string]any{"adminTeams": []string{"sre", "ops"}})); r["ok"] != true {
		t.Fatalf("teams: %v", r)
	}
	fs.mu.Lock()
	checked := fs.users[len(fs.users)-1].GitHubCheckedAt
	fs.mu.Unlock()
	if checked != nil {
		t.Fatal("GitHub accounts weren't made due for a check")
	}

	// Turning GitHub off is fine while the password admin can log in, and stops its login.
	if r := admin("saveLoginSettings", with(map[string]any{"githubEnabled": false})); r["ok"] != true {
		t.Fatalf("GitHub off: %v", r)
	}
	if res, _ := http.Get(ts.URL + "/api/auth/github/start"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("GitHub login after it's off: %d", res.StatusCode)
	}
	// The settings are kept, secret included, to turn it on again.
	if r := admin("getLoginSettings"); r["settings"].(map[string]any)["clientId"] != "client" || r["settings"].(map[string]any)["hasClientSecret"] != true {
		t.Fatalf("kept: %v", r)
	}

	// No change may leave no admin who can log in.
	fs.mu.Lock()
	fs.users[0].PasswordHash = ""
	fs.mu.Unlock()
	if r := admin("saveLoginSettings", with(map[string]any{"githubEnabled": false, "passwordLogin": true})); r["ok"] != false ||
		!strings.Contains(r["msg"].(string), "No admin") {
		t.Fatalf("no admin left: %v", r)
	}
}

func TestLoginSettingsFromTheEnvironment(t *testing.T) {
	s, _, _ := newTestServerWith(t, func(o *Options) {
		o.GitHub = &GitHubOptions{ClientID: "client", ClientSecret: "secret", Org: "pantheon-lab",
			AdminTeams: []string{"sre"}, WebURL: defaultGitHubWeb, APIURL: defaultGitHubAPI}
	})
	call := eventCaller(t, s, 1)
	s.store.(*fakeStore).users = append(s.store.(*fakeStore).users, store.User{ID: 1, Username: "admin", Role: store.RoleAdmin})
	r := call("getLoginSettings")
	settings := r["settings"].(map[string]any)
	if r["managedByEnv"] != true || settings["clientId"] != "client" || settings["webURL"] != "" || settings["hasClientSecret"] != true {
		t.Fatalf("from the environment: %v", r)
	}
	if raw, _ := json.Marshal(r); strings.Contains(string(raw), `"secret"`) {
		t.Fatalf("the secret: %s", raw)
	}
	if r := call("saveLoginSettings", map[string]any{"passwordLogin": true}); r["ok"] != false {
		t.Fatalf("saved over the environment: %v", r)
	}
	// Saved settings don't replace the environment's.
	_ = s.store.SaveLoginConfig(context.Background(), store.LoginConfig{PasswordLogin: true})
	if err := s.LoadLoginConfig(context.Background()); err != nil || s.github() == nil {
		t.Fatalf("the environment's settings were replaced: %v", err)
	}
}

func TestLoadLoginConfig(t *testing.T) {
	s, fs, _ := newTestServer(t)
	fs.loginConfig = &store.LoginConfig{GitHubEnabled: true, ClientID: "c", ClientSecret: "s", Org: "o",
		ViewerTeams: []string{"*"}, PasswordLogin: false}
	if err := s.LoadLoginConfig(context.Background()); err != nil {
		t.Fatal(err)
	}
	g := s.github()
	if g == nil || g.WebURL != defaultGitHubWeb || g.APIURL != defaultGitHubAPI || s.passwordLogin() {
		t.Fatalf("loaded: %+v %v", g, s.passwordLogin())
	}
}
