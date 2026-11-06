package ui

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec
	"crypto/sha256"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/time/rate"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

func (f *fakeStore) StartTOTPSetup(_ context.Context, id int32, secret []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			if f.users[i].TOTPEnabled {
				return store.ErrTOTPEnabled
			}
			f.users[i].TOTPSecret = secret
			delete(f.totpSteps, id)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) EnableTOTP(_ context.Context, id int32, secret []byte, step int64, keep []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		u := &f.users[i]
		if u.ID == id && !u.TOTPEnabled && bytes.Equal(u.TOTPSecret, secret) {
			u.TOTPEnabled = true
			f.totpSteps[id] = step
			for k := range f.sessions {
				if k != string(keep) {
					delete(f.sessions, k)
				}
			}
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) DisableTOTP(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].TOTPEnabled, f.users[i].TOTPSecret, f.users[i].TOTPLockedUntil = false, nil, nil
			delete(f.totpSteps, id)
			clear(f.challenges)
			return nil
		}
	}
	return store.ErrNotFound
}

func (f *fakeStore) UseTOTPStep(_ context.Context, id int32, step int64) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if last, found := f.totpSteps[id]; found && last >= step {
		return false, nil
	}
	f.totpSteps[id] = step
	f.totpFailures[id] = 0
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].TOTPLockedUntil = nil
		}
	}
	return true, nil
}

func (f *fakeStore) RecordTOTPFailure(_ context.Context, id int32, max int, lockFor time.Duration) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.totpFailures[id]++
	if f.totpFailures[id] >= max {
		until := time.Now().Add(lockFor)
		for i := range f.users {
			if f.users[i].ID == id {
				f.users[i].TOTPLockedUntil = &until
			}
		}
	}
	return nil
}

func (f *fakeStore) CreateTwoFactorChallenge(_ context.Context, h []byte, c store.TwoFactorChallenge) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.challenges[string(h)] = c
	return nil
}

func (f *fakeStore) FindTwoFactorChallenge(_ context.Context, h []byte) (store.TwoFactorChallenge, store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, found := f.challenges[string(h)]
	if !found || c.ExpiresAt.Before(time.Now()) {
		return store.TwoFactorChallenge{}, store.User{}, store.ErrNotFound
	}
	for _, u := range f.users {
		if u.ID == c.UserID {
			return c, u, nil
		}
	}
	return store.TwoFactorChallenge{}, store.User{}, store.ErrNotFound
}

func (f *fakeStore) CountTwoFactorAttempt(_ context.Context, h []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, found := f.challenges[string(h)]
	if !found {
		return 0, store.ErrNotFound
	}
	c.Attempts++
	f.challenges[string(h)] = c
	return c.Attempts, nil
}

func (f *fakeStore) DeleteTwoFactorChallenge(_ context.Context, h []byte) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, found := f.challenges[string(h)]
	delete(f.challenges, string(h))
	return found, nil
}

func (f *fakeStore) CreateAPIKey(_ context.Context, k store.APIKey, hash []byte) (store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k.ID = int64(len(f.apiKeys) + 1)
	k.CreatedAt = time.Now()
	f.apiKeys = append(f.apiKeys, k)
	f.settings[fmt.Sprintf("test-key-hash-%d", k.ID)] = json.RawMessage(`"` + hex.EncodeToString(hash) + `"`)
	return k, nil
}

func (f *fakeStore) ListAPIKeys(context.Context) ([]store.APIKey, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.apiKeys), nil
}

func (f *fakeStore) RevokeAPIKey(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.apiKeys {
		if f.apiKeys[i].ID == id && f.apiKeys[i].RevokedAt == nil {
			now := time.Now()
			f.apiKeys[i].RevokedAt = &now
			return nil
		}
	}
	return store.ErrNotFound
}

// The test vectors of RFC 6238, appendix B, for SHA-1, cut to 6 digits.
func TestTOTPMatchesRFC6238(t *testing.T) {
	secret := []byte("12345678901234567890")
	for _, tc := range []struct {
		unix int64
		code string
	}{{59, "287082"}, {1111111109, "081804"}, {1111111111, "050471"}, {1234567890, "005924"}, {2000000000, "279037"}} {
		if got := hotp(secret, tc.unix/totpPeriod); got != tc.code {
			t.Errorf("at %d: %s, want %s", tc.unix, got, tc.code)
		}
		step, ok := checkTOTP(secret, tc.code, time.Unix(tc.unix, 0))
		if !ok || step != tc.unix/totpPeriod {
			t.Errorf("at %d: check %d %v", tc.unix, step, ok)
		}
		// A step either side is allowed, but not two.
		if _, ok := checkTOTP(secret, tc.code, time.Unix(tc.unix+totpPeriod, 0)); !ok {
			t.Errorf("at %d: the previous step's code was refused", tc.unix)
		}
		if _, ok := checkTOTP(secret, tc.code, time.Unix(tc.unix+2*totpPeriod, 0)); ok {
			t.Errorf("at %d: a code two steps old was accepted", tc.unix)
		}
	}
	for _, bad := range []string{"", "12345", "1234567", "abcdef"} {
		if _, ok := checkTOTP(secret, bad, time.Unix(59, 0)); ok {
			t.Errorf("%q was accepted", bad)
		}
	}
	if _, ok := checkTOTP(secret, " 287 082 ", time.Unix(59, 0)); !ok {
		t.Error("spaces in the code weren't ignored")
	}

	uri, err := url.Parse(totpURI("Uptime Kuma", "admin", secret))
	if err != nil {
		t.Fatal(err)
	}
	if uri.Scheme != "otpauth" || uri.Host != "totp" || uri.Path != "/Uptime Kuma:admin" ||
		uri.Query().Get("secret") != "GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ" || uri.Query().Get("issuer") != "Uptime Kuma" ||
		uri.Query().Get("digits") != "6" || uri.Query().Get("period") != "30" {
		t.Fatalf("URI: %s", uri)
	}
}

func cookieNamed(res *http.Response, name string) *http.Cookie {
	for _, c := range res.Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func getSessionUser(t *testing.T, c *http.Client, base string) map[string]any {
	t.Helper()
	res, err := c.Get(base + "/api/auth/get-session")
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var session map[string]map[string]any
	_ = json.NewDecoder(res.Body).Decode(&session)
	return session["user"]
}

func TestTwoFactorLogin(t *testing.T) {
	s, fs, ts := newTestServer(t)
	// This test logs in more often than the limit allows; TestLoginRateLimit tests it.
	s.limiter = &loginLimiter{global: rate.NewLimiter(rate.Inf, 0), users: map[string]*rate.Limiter{}, perUser: rate.Inf}
	var mu sync.Mutex
	now := time.Now()
	s.now = func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	advance := func(d time.Duration) { mu.Lock(); now = now.Add(d); mu.Unlock() }
	codeAt := func(secret []byte, offset int64) string { return hotp(secret, s.now().Unix()/totpPeriod+offset) }
	signIn := func(c *http.Client) (*http.Response, map[string]any) {
		return postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "long enough"})
	}

	c := newClient(t)
	postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	signIn(c)
	other := newClient(t)
	signIn(other)

	if res, body := postJSON(t, newClient(t), ts.URL+"/api/auth/two-factor/enable", map[string]any{"password": "long enough"}); res.StatusCode != 401 {
		t.Fatalf("enable logged out: %d %v", res.StatusCode, body)
	}
	if res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/enable", map[string]any{"password": "wrong one"}); res.StatusCode != 400 || body["code"] != "INVALID_PASSWORD" {
		t.Fatalf("enable with a wrong password: %d %v", res.StatusCode, body)
	}
	res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/enable", map[string]any{"password": "long enough", "issuer": "Uptime Kuma"})
	if res.StatusCode != 200 || body["method"] != "totp" {
		t.Fatalf("enable: %d %v", res.StatusCode, body)
	}
	uri, _ := url.Parse(body["totpURI"].(string))
	secret, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(uri.Query().Get("secret"))
	if err != nil || len(secret) != totpSecretSize || !bytes.Equal(secret, fs.users[0].TOTPSecret) {
		t.Fatalf("secret: %x %v", secret, err)
	}
	// Not on until a code confirms it.
	if getSessionUser(t, c, ts.URL)["twoFactorEnabled"] != false {
		t.Fatal("enabled before a code was confirmed")
	}
	if res, body := signIn(newClient(t)); res.StatusCode != 200 || body["twoFactorRedirect"] != nil {
		t.Fatalf("sign in before confirming: %d %v", res.StatusCode, body)
	}
	if res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": "000000"}); res.StatusCode != 401 || body["code"] != "INVALID_CODE" {
		t.Fatalf("wrong code: %d %v", res.StatusCode, body)
	}
	if res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)}); res.StatusCode != 200 {
		t.Fatalf("confirm: %d %v", res.StatusCode, body)
	}
	if getSessionUser(t, c, ts.URL)["twoFactorEnabled"] != true {
		t.Fatal("not enabled after the code")
	}
	// The other login didn't give a code, so it ended.
	if getSessionUser(t, other, ts.URL) != nil {
		t.Fatal("another session survived enabling two-factor login")
	}
	if res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/enable", map[string]any{"password": "long enough"}); res.StatusCode != 400 || body["code"] != "TOTP_ALREADY_ENABLED" {
		t.Fatalf("enable again: %d %v", res.StatusCode, body)
	}

	// A right password now gives a challenge, not a session.
	login := newClient(t)
	res, body = signIn(login)
	if res.StatusCode != 200 || body["twoFactorRedirect"] != true || cookieNamed(res, sessionCookie) != nil {
		t.Fatalf("sign in: %d %v", res.StatusCode, body)
	}
	challenge := cookieNamed(res, twoFactorCookie)
	if challenge == nil || !challenge.HttpOnly || challenge.Path != "/api/auth" {
		t.Fatalf("challenge cookie: %+v", challenge)
	}
	if getSessionUser(t, login, ts.URL) != nil {
		t.Fatal("logged in without a code")
	}
	if res, body := postJSON(t, newClient(t), ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)}); res.StatusCode != 401 || body["code"] != "INVALID_TWO_FACTOR_COOKIE" {
		t.Fatalf("code without a challenge: %d %v", res.StatusCode, body)
	}
	// The code that turned it on can't be used again.
	if res, body := postJSON(t, login, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)}); res.StatusCode != 401 || body["code"] != "INVALID_CODE" {
		t.Fatalf("replayed code: %d %v", res.StatusCode, body)
	}
	advance(totpPeriod * time.Second)
	res, body = postJSON(t, login, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)})
	if res.StatusCode != 200 || cookieNamed(res, sessionCookie) == nil {
		t.Fatalf("verify: %d %v", res.StatusCode, body)
	}
	if user := getSessionUser(t, login, ts.URL); user == nil || user["twoFactorEnabled"] != true {
		t.Fatalf("after the code: %v", user)
	}
	if len(fs.challenges) != 0 {
		t.Fatal("the challenge was kept")
	}

	// 5 tries per challenge.
	login = newClient(t)
	signIn(login)
	for i := range challengeAttempts {
		if res, body := postJSON(t, login, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": "000000"}); res.StatusCode != 401 {
			t.Fatalf("try %d: %d %v", i+1, res.StatusCode, body)
		}
	}
	advance(totpPeriod * time.Second)
	if res, body := postJSON(t, login, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)}); res.StatusCode != 400 || body["code"] != "TOO_MANY_ATTEMPTS_REQUEST_NEW_CODE" {
		t.Fatalf("6th try: %d %v", res.StatusCode, body)
	}

	// 10 wrong codes in a row lock the account's codes, whatever the challenge.
	for range 2 {
		login = newClient(t)
		signIn(login)
		for range challengeAttempts {
			postJSON(t, login, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": "000000"})
		}
	}
	login = newClient(t)
	signIn(login)
	if res, body := postJSON(t, login, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)}); res.StatusCode != 429 || body["code"] != "ACCOUNT_TEMPORARILY_LOCKED" {
		t.Fatalf("locked: %d %v", res.StatusCode, body)
	}
	fs.users[0].TOTPLockedUntil = nil

	// Turning it off needs the password, and ends logins waiting for a code.
	waiting := newClient(t)
	signIn(waiting)
	if res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/disable", map[string]any{"password": "wrong one"}); res.StatusCode != 400 {
		t.Fatalf("disable with a wrong password: %d %v", res.StatusCode, body)
	}
	if res, body := postJSON(t, c, ts.URL+"/api/auth/two-factor/disable", map[string]any{"password": "long enough"}); res.StatusCode != 200 || body["status"] != true {
		t.Fatalf("disable: %d %v", res.StatusCode, body)
	}
	advance(totpPeriod * time.Second)
	if res, body := postJSON(t, waiting, ts.URL+"/api/auth/two-factor/verify-totp", map[string]any{"code": codeAt(secret, 0)}); res.StatusCode != 401 {
		t.Fatalf("challenge after disabling: %d %v", res.StatusCode, body)
	}
	if res, body := signIn(newClient(t)); res.StatusCode != 200 || body["twoFactorRedirect"] != nil {
		t.Fatalf("sign in after disabling: %d %v", res.StatusCode, body)
	}
}

func TestLeakedPasswordCheck(t *testing.T) {
	leaked := sha1.Sum([]byte("password123")) //nolint:gosec
	leakedHash := strings.ToUpper(hex.EncodeToString(leaked[:]))
	var mu sync.Mutex
	var asked []string
	down := false
	hibp := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		asked = append(asked, r.URL.Path+" "+r.Header.Get("Add-Padding"))
		isDown := down
		mu.Unlock()
		if isDown {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		prefix := strings.TrimPrefix(r.URL.Path, "/range/")
		fmt.Fprintf(w, "0018A45C4D1DEF81644B54AB7F969B88D65:3\r\n")
		if prefix == leakedHash[:5] {
			fmt.Fprintf(w, "%s:250000\r\n", strings.ToLower(leakedHash[5:]))
		}
		// A padding line, with a count of 0.
		fmt.Fprintf(w, "%s:0\r\n", "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF")
	}))
	defer hibp.Close()

	_, fs, ts := newTestServerWith(t, func(o *Options) { o.PasswordLeaked = PwnedPasswords(nil, hibp.URL+"/range/") })
	c := newClient(t)
	res, body := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "password123"})
	if res.StatusCode != 400 || !strings.Contains(fmt.Sprint(body["msg"]), "compromised") || len(fs.users) != 0 {
		t.Fatalf("leaked password at setup: %d %v", res.StatusCode, body)
	}
	mu.Lock()
	if len(asked) != 1 || asked[0] != "/range/"+leakedHash[:5]+" true" {
		t.Fatalf("asked: %v", asked)
	}
	down = true
	mu.Unlock()
	res, body = postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"})
	if res.StatusCode != 400 || !strings.Contains(fmt.Sprint(body["msg"]), "KUMA_DISABLE_LEAK_PASSWORD_CHECK") {
		t.Fatalf("check failing at setup: %d %v", res.StatusCode, body)
	}
	mu.Lock()
	down = false
	mu.Unlock()
	if res, body := postJSON(t, c, ts.URL+"/api/setup", map[string]string{"username": "admin", "password": "long enough"}); res.StatusCode != 200 {
		t.Fatalf("setup: %d %v", res.StatusCode, body)
	}
	postJSON(t, c, ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": "long enough"})
	res, body = postJSON(t, c, ts.URL+"/api/auth/change-password", map[string]any{"currentPassword": "long enough", "newPassword": "password123"})
	if res.StatusCode != 400 || body["code"] != "PASSWORD_COMPROMISED" {
		t.Fatalf("change to a leaked password: %d %v", res.StatusCode, body)
	}
	if res, body := postJSON(t, c, ts.URL+"/api/auth/change-password", map[string]any{"currentPassword": "long enough", "newPassword": "another long one"}); res.StatusCode != 200 {
		t.Fatalf("change password: %d %v", res.StatusCode, body)
	}
}

func TestAPIKeyEvents(t *testing.T) {
	s, fs, _ := newTestServer(t)
	ctx := context.Background()
	c := &conn{userID: 1, admin: true, username: "admin", out: make(chan []byte, 256), done: make(chan struct{})}
	call := func(event string, args ...any) reply {
		t.Helper()
		raws := make([]json.RawMessage, len(args))
		for i, a := range args {
			raws[i], _ = json.Marshal(a)
		}
		r, known := s.handle(ctx, c, event, raws)
		if !known {
			t.Fatalf("%s: unknown event", event)
		}
		return r.(reply)
	}

	if r := call("getAPIKeyList"); r["ok"] != true || len(r["apiKeyList"].([]map[string]any)) != 0 {
		t.Fatalf("empty list: %v", r)
	}
	r := call("addAPIKey", map[string]any{"name": " CI ", "expires": nil, "active": 1})
	if r["ok"] != true || !strings.HasPrefix(r["key"].(string), "kuma_") {
		t.Fatalf("add: %v", r)
	}
	plain := r["key"].(string)
	sum := sha256.Sum256([]byte(plain))
	if string(fs.settings["test-key-hash-1"]) != `"`+hex.EncodeToString(sum[:])+`"` {
		t.Fatal("the stored hash isn't the key's")
	}
	all := api.GrantableScopes()
	slices.Sort(all)
	if k := fs.apiKeys[0]; k.Name != "CI" || !slices.Equal(k.Scopes, all) || k.ExpiresAt != nil || k.Prefix != plain[:13] {
		t.Fatalf("stored: %+v", k)
	}

	future := time.Now().Add(48 * time.Hour).Format("2006-01-02 15:04")
	r = call("addAPIKey", map[string]any{"name": "read", "expires": future, "scopes": []string{"monitors:read", "monitors:read"}})
	if r["ok"] != true || !slices.Equal(fs.apiKeys[1].Scopes, []string{"monitors:read"}) || fs.apiKeys[1].ExpiresAt == nil {
		t.Fatalf("add with scopes: %v %+v", r, fs.apiKeys[1])
	}
	for _, bad := range []map[string]any{
		{"name": ""},
		{"name": "x", "scopes": []string{"admin"}},
		{"name": "x", "expires": "2001-01-01 00:00"},
		{"name": "x", "expires": "tomorrow"},
	} {
		if r := call("addAPIKey", bad); r["ok"] != false {
			t.Fatalf("%v: %v", bad, r)
		}
	}

	r = call("getAPIKeyList")
	list := r["apiKeyList"].([]map[string]any)
	if len(list) != 2 || list[0]["enabled"] != true || list[1]["expiresAt"] != future+":00" || list[0]["expiresAt"] != nil {
		t.Fatalf("list: %v", list)
	}
	if _, leaked := list[0]["key"]; leaked {
		t.Fatal("the list has the key")
	}
	if r := call("deleteAPIKey", 1); r["ok"] != true {
		t.Fatalf("delete: %v", r)
	}
	if r := call("deleteAPIKey", "1"); r["ok"] != false {
		t.Fatalf("delete again: %v", r)
	}
	if r := call("deleteAPIKey", "legacy-3"); r["ok"] != false {
		t.Fatalf("delete an invalid id: %v", r)
	}
	if list := call("getAPIKeyList")["apiKeyList"].([]map[string]any); len(list) != 1 || list[0]["name"] != "read" {
		t.Fatalf("after delete: %v", list)
	}
	if fs.apiKeys[0].RevokedAt == nil {
		t.Fatal("not revoked")
	}
}

func (f *fakeStore) UpgradePasswordHash(_ context.Context, id int32, old, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.users {
		if f.users[i].ID == id && f.users[i].PasswordHash == old {
			f.users[i].PasswordHash = hash
		}
	}
	return nil
}

// An account imported from the user table before better-auth has a bcrypt hash, which the
// first login replaces with scrypt.
func TestImportedBcryptPassword(t *testing.T) {
	_, fs, ts := newTestServer(t)
	legacy, err := bcrypt.GenerateFromPassword([]byte("old password"), 10)
	if err != nil {
		t.Fatal(err)
	}
	fs.users = append(fs.users, store.User{ID: 1, Username: "admin", PasswordHash: string(legacy), Role: store.RoleAdmin})
	signIn := func(password string) int {
		res, _ := postJSON(t, newClient(t), ts.URL+"/api/auth/sign-in/username", map[string]any{"username": "admin", "password": password})
		return res.StatusCode
	}
	if code := signIn("wrong password"); code != 401 || fs.users[0].PasswordHash != string(legacy) {
		t.Fatalf("a wrong password: %d", code)
	}
	if code := signIn("old password"); code != 200 {
		t.Fatalf("the imported password: %d", code)
	}
	if hash := fs.users[0].PasswordHash; isBcrypt(hash) || !verifyPassword(hash, "old password") {
		t.Fatalf("the hash wasn't replaced: %s", hash)
	}
	if code := signIn("old password"); code != 200 {
		t.Fatalf("after the upgrade: %d", code)
	}
}

// better-auth used the text of its generated key as the HMAC key, which kuma-import stores as
// it is. These codes came from better-auth's createOTP("abcdefghijklmnopqrstuvwxyzABCDEF").hotp.
func TestTOTPMatchesBetterAuthForImportedKeys(t *testing.T) {
	key := []byte("abcdefghijklmnopqrstuvwxyzABCDEF")
	if got := hotp(key, 1); got != "530764" {
		t.Errorf("counter 1: %s", got)
	}
	if got := hotp(key, 55555555); got != "285745" {
		t.Errorf("counter 55555555: %s", got)
	}
}

func (f *fakeStore) SaveGitHubUser(_ context.Context, githubID int64, login, role, token string) (store.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for i, u := range f.users {
		if u.GitHubID != nil && *u.GitHubID == githubID {
			f.users[i].Username, f.users[i].Role, f.users[i].GitHubCheckedAt = "@"+login, role, &now
			f.githubTokens[u.ID] = token
			return f.users[i], nil
		}
	}
	id := int32(len(f.users) + 1)
	u := store.User{ID: id, Username: "@" + login, Role: role, GitHubID: &githubID, GitHubCheckedAt: &now, CreatedAt: now}
	f.users = append(f.users, u)
	f.githubTokens[id] = token
	return u, nil
}

func (f *fakeStore) GitHubToken(_ context.Context, id int32) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	token, found := f.githubTokens[id]
	if !found {
		return "", store.ErrNotFound
	}
	return token, nil
}

func (f *fakeStore) SetGitHubChecked(_ context.Context, id int32, role string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	now := time.Now()
	for i := range f.users {
		if f.users[i].ID == id {
			f.users[i].Role, f.users[i].GitHubCheckedAt = role, &now
		}
	}
	return nil
}

func (f *fakeStore) EndUserSessions(_ context.Context, id int32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for k, s := range f.sessions {
		if s.UserID == id {
			delete(f.sessions, k)
		}
	}
	return nil
}
