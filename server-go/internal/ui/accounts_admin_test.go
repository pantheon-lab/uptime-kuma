package ui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

func (f *fakeStore) EndOtherUserSessions(_ context.Context, id int32, keep []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.sessions {
		if s.UserID == id && k != string(keep) {
			delete(f.sessions, k)
		}
	}
	return nil
}

func (f *fakeStore) UserByID(_ context.Context, id int32) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.ID == id {
			return u, nil
		}
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) ListAccounts(_ context.Context) ([]store.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []store.Account
	for _, u := range f.users {
		a := store.Account{User: u}
		for k, s := range f.sessions {
			if s.UserID == u.ID && s.ExpiresAt.After(time.Now()) {
				a.Sessions = append(a.Sessions, store.AccountSession{Session: s, TokenHash: []byte(k)})
			}
		}
		out = append(out, a)
	}
	return out, nil
}

// otherAdmin is the store's rule: another admin who can log in.
func (f *fakeStore) otherAdmin(id int32, methods store.LoginMethods) bool {
	for _, u := range f.users {
		if u.ID != id && u.IsAdmin() &&
			((u.GitHubID == nil && methods.Password && u.PasswordHash != "") || (u.GitHubID != nil && methods.GitHub)) {
			return true
		}
	}
	return false
}

func (f *fakeStore) DeleteUser(_ context.Context, id int32, methods store.LoginMethods) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, u := range f.users {
		if u.ID != id {
			continue
		}
		if u.IsAdmin() && !f.otherAdmin(id, methods) {
			return store.User{}, store.ErrLastAdmin
		}
		f.users = append(f.users[:i], f.users[i+1:]...)
		for k, s := range f.sessions {
			if s.UserID == id {
				delete(f.sessions, k)
			}
		}
		return u, nil
	}
	return store.User{}, store.ErrNotFound
}

func (f *fakeStore) SetUserRole(_ context.Context, id int32, role string, methods store.LoginMethods) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, u := range f.users {
		if u.ID != id {
			continue
		}
		if u.GitHubID != nil {
			return store.User{}, store.ErrRoleFromGitHub
		}
		if u.IsAdmin() && role == store.RoleViewer && !f.otherAdmin(id, methods) {
			return store.User{}, store.ErrLastAdmin
		}
		f.users[i].Role = role
		return u, nil
	}
	return store.User{}, store.ErrNotFound
}

func TestManagingAccounts(t *testing.T) {
	_, fs, ts := newTestServer(t)
	admin := newClient(t)
	postJSON(t, admin, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	if res, body := postJSON(t, admin, ts.URL+"/api/auth/sign-in/username",
		map[string]any{"username": "admin", "password": "long enough"}); res.StatusCode != 200 {
		t.Fatalf("login: %d %v", res.StatusCode, body)
	}
	hash, err := hashPassword("other password")
	if err != nil {
		t.Fatal(err)
	}
	githubID := int64(42)
	future := time.Now().Add(time.Hour)
	fs.mu.Lock()
	fs.users = append(fs.users,
		store.User{ID: 2, Username: "second", PasswordHash: hash, Role: store.RoleAdmin},
		store.User{ID: 3, Username: "viewer", PasswordHash: hash, Role: store.RoleViewer},
		store.User{ID: 4, Username: "@octocat", Role: store.RoleAdmin, GitHubID: &githubID})
	fs.sessions["viewer-1"] = store.Session{UserID: 3, ExpiresAt: future}
	fs.sessions["viewer-2"] = store.Session{UserID: 3, ExpiresAt: future}
	fs.sessions["admin-other"] = store.Session{UserID: 1, ExpiresAt: future}
	fs.mu.Unlock()

	id, stream, done := openStream(t, admin, ts.URL)
	defer done()
	ack := 100
	call := func(event string, args ...any) string {
		t.Helper()
		ack++
		sendEvent(t, admin, ts.URL, id, event, ack, args...)
		for {
			kind, parts := stream.message(t)
			if kind == "ack" {
				return string(parts[1])
			}
		}
	}

	list := call("getAccountList")
	for _, want := range []string{`"username":"admin"`, `"username":"@octocat"`, `"method":"github"`, `"you":true`,
		`"current":true`, `"githubLogin":false`, `"passwordLogin":true`} {
		if !strings.Contains(list, want) {
			t.Errorf("the list has no %s: %s", want, list)
		}
	}
	if strings.Contains(list, "viewer-1") || strings.Contains(list, hash) {
		t.Errorf("the list has a session token or password hash: %s", list)
	}

	for _, c := range []struct {
		event string
		args  []any
		want  string
	}{
		{"deleteAccount", []any{1}, "your own account"},
		{"setAccountRole", []any{1, "viewer"}, "your own account"},
		{"setAccountRole", []any{4, "viewer"}, "follows its GitHub teams"},
		{"setAccountRole", []any{2, "owner"}, "admin or viewer"},
		{"deleteAccount", []any{99}, "not found"},
		{"setAccountRole", []any{3, "admin"}, `"ok":true`},
		{"setAccountRole", []any{2, "viewer"}, `"ok":true`},
		{"endAccountSessions", []any{3}, `"ok":true`},
		{"endAccountSessions", []any{1}, `"ok":true`},
		{"deleteAccount", []any{4}, `"ok":true`},
	} {
		if got := call(c.event, c.args...); !strings.Contains(got, c.want) {
			t.Errorf("%s %v: %s", c.event, c.args, got)
		}
	}

	fs.mu.Lock()
	roles := map[string]string{}
	for _, u := range fs.users {
		roles[u.Username] = u.Role
	}
	_, viewerSession := fs.sessions["viewer-1"]
	_, otherSession := fs.sessions["admin-other"]
	ownSessions := 0
	for _, s := range fs.sessions {
		if s.UserID == 1 {
			ownSessions++
		}
	}
	fs.mu.Unlock()
	if roles["viewer"] != "admin" || roles["second"] != "viewer" || roles["@octocat"] != "" {
		t.Errorf("roles after the changes: %v", roles)
	}
	if viewerSession || otherSession || ownSessions != 1 {
		t.Errorf("sessions: viewer's %v, admin's other %v, admin's %d", viewerSession, otherSession, ownSessions)
	}

	// The last-admin rule is the store's, and its error is shown as it is.
	if got := accountErr(store.ErrLastAdmin); !isUserError(got) || got.Error() != errLastAdmin.Error() {
		t.Errorf("last admin: %v", got)
	}

	// A viewer may not list or change accounts.
	viewer := newClient(t)
	postJSON(t, viewer, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "second", "password": "other password"})
	vid, vstream, vdone := openStream(t, viewer, ts.URL)
	defer vdone()
	for i, event := range []string{"getAccountList", "deleteAccount", "setAccountRole", "endAccountSessions"} {
		sendEvent(t, viewer, ts.URL, vid, event, 10+i, 1)
		kind, parts := vstream.message(t)
		for kind != "ack" {
			kind, parts = vstream.message(t)
		}
		if !strings.Contains(string(parts[1]), "can only view") {
			t.Errorf("viewer %s: %s", event, parts[1])
		}
	}
}
