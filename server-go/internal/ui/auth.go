package ui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	sessionCookie = "kuma_session"
	// Like better-auth: a remembered login lasts 7 days and is renewed once a day while used.
	rememberFor    = 7 * 24 * time.Hour
	renewAfter     = 24 * time.Hour
	notRememberFor = 24 * time.Hour
	maxAuthBody    = 64 << 10
)

// The Node.js backend's message, which tells the admin how to recover.
const setupDoneMsg = "Uptime Kuma has been initialized. If you want to run setup again, please delete the database."

// authError is better-auth's error body, which its client turns into error.message.
func authError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]string{"code": code, "message": message})
}

// sameOrigin rejects cross-site requests to endpoints that act on the session cookie.
// The cookie is SameSite=Lax already; this also covers browsers that don't support it.
func (s *Server) sameOrigin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
			authError(w, http.StatusForbidden, "INVALID_ORIGIN", "Cross-site requests aren't allowed")
			return
		}
		if origin := r.Header.Get("Origin"); origin != "" {
			u, err := url.Parse(origin)
			if err != nil || !strings.EqualFold(u.Host, r.Host) {
				authError(w, http.StatusForbidden, "INVALID_ORIGIN", "Cross-site requests aren't allowed")
				return
			}
		}
		next(w, r)
	}
}

func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAuthBody)).Decode(v); err != nil {
		authError(w, http.StatusBadRequest, "INVALID_BODY", "The request body isn't valid JSON")
		return false
	}
	return true
}

// entryPage tells the UI's / route where to go: a custom domain's status page, or the entry page.
func (s *Server) entryPage(w http.ResponseWriter, r *http.Request) {
	route, err := s.routing(r.Context())
	if err != nil {
		s.log.Error("read routing", "error", err)
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "msg": "internal error"})
		return
	}
	if slug, found := route.domains[requestHost(r, route.trustProxy)]; found {
		writeJSON(w, http.StatusOK, map[string]string{"type": "statusPageMatchedDomain", "statusPageSlug": slug})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"type": "entryPage", "entryPage": route.entryPage})
}

// needSetup reports whether the first account may still be created: there's no account,
// and the server started less than SetupWindow ago. The window stops a stranger from
// claiming a server that someone forgot to set up.
func (s *Server) needSetup(ctx context.Context) (bool, error) {
	if s.now().Sub(s.started) > s.opts.SetupWindow {
		return false, nil
	}
	exists, err := s.store.HasUser(ctx)
	return !exists, err
}

func (s *Server) setup(w http.ResponseWriter, r *http.Request) {
	fail := func(msg string) { writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "msg": msg}) }
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !s.setupMu.TryLock() {
		fail("Setup is already in progress. Please wait.")
		return
	}
	defer s.setupMu.Unlock()

	exists, err := s.store.HasUser(r.Context())
	if err != nil {
		s.log.Error("setup", "error", err)
		fail("Internal error")
		return
	}
	if exists {
		fail(setupDoneMsg)
		return
	}
	if s.now().Sub(s.started) > s.opts.SetupWindow {
		fail("Setup has expired. Please restart the server to try again.")
		return
	}
	body.Username = strings.TrimSpace(body.Username)
	if err := checkUsername(body.Username); err != nil {
		fail(err.Error())
		return
	}
	if err := s.checkNewPassword(r.Context(), body.Password); err != nil {
		fail(err.Error())
		return
	}
	hash, err := hashPassword(body.Password)
	if err != nil {
		s.log.Error("setup", "error", err)
		fail("Internal error")
		return
	}
	if _, err := s.store.CreateFirstUser(r.Context(), body.Username, hash); err != nil {
		if errors.Is(err, store.ErrSetupDone) {
			fail(setupDoneMsg)
			return
		}
		s.log.Error("setup", "error", err)
		fail("Internal error")
		return
	}
	s.log.Info("first account created", "username", body.Username)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) signIn(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Username   string `json:"username"`
		Password   string `json:"password"`
		RememberMe *bool  `json:"rememberMe"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if s.opts.DisablePasswordLogin {
		authError(w, http.StatusForbidden, "PASSWORD_LOGIN_DISABLED", "Logging in with a password is turned off. Log in with GitHub.")
		return
	}
	if !s.limiter.allow(body.Username) {
		authError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Too many requests. Please try again later.")
		return
	}
	user, err := s.store.UserByUsername(r.Context(), strings.TrimSpace(body.Username))
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.log.Error("sign in", "error", err)
		authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
		return
	}
	hash := user.PasswordHash
	if err != nil {
		hash = dummyHash
	}
	if !verifyPassword(hash, body.Password) || err != nil {
		s.log.Warn("failed login", "username", body.Username)
		authError(w, http.StatusUnauthorized, "INVALID_USERNAME_OR_PASSWORD", "Invalid username or password")
		return
	}

	if isBcrypt(user.PasswordHash) {
		if upgraded, err := hashPassword(body.Password); err == nil {
			err = s.store.UpgradePasswordHash(r.Context(), user.ID, user.PasswordHash, upgraded)
			if err != nil {
				s.log.Warn("replace the imported password hash", "error", err)
			}
		}
	}

	remember := body.RememberMe == nil || *body.RememberMe
	if user.TOTPEnabled {
		if err := s.startChallenge(r.Context(), w, r, user.ID, remember); err != nil {
			s.log.Error("sign in", "error", err)
			authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
			return
		}
		// better-auth's answer, which makes the UI ask for the code.
		writeJSON(w, http.StatusOK, map[string]any{"twoFactorRedirect": true, "twoFactorMethods": []string{"totp"}})
		return
	}
	if err := s.startSession(r.Context(), w, r, user.ID, remember); err != nil {
		s.log.Error("sign in", "error", err)
		authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
		return
	}
	s.log.Info("login", "username", user.Username)
	// better-auth also returns the token here. It's left out, so that only the HttpOnly
	// cookie holds it, out of reach of scripts.
	writeJSON(w, http.StatusOK, map[string]any{"redirect": false, "token": nil, "user": userJSON(user)})
}

func (s *Server) startSession(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int32, remember bool) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	lifetime := notRememberFor
	if remember {
		lifetime = rememberFor
	}
	expires := s.now().Add(lifetime)
	if err := s.store.CreateSession(ctx, hashToken(token), store.Session{UserID: userID, Remember: remember, ExpiresAt: expires}); err != nil {
		return err
	}
	setSessionCookie(w, r, token, remember, expires)
	return nil
}

func hashToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, remember bool, expires time.Time) {
	cookie := &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   secureRequest(r),
		SameSite: http.SameSiteLaxMode,
	}
	if remember {
		cookie.Expires = expires
		cookie.MaxAge = int(time.Until(expires).Seconds())
	}
	http.SetCookie(w, cookie)
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: "", Path: "/", MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
}

// currentSession returns the request's login, renewing a remembered one that is over a day
// old. It returns ok=false without an error when there's no valid session.
func (s *Server) currentSession(ctx context.Context, w http.ResponseWriter, r *http.Request) (store.Session, store.User, bool, error) {
	cookie, err := r.Cookie(sessionCookie)
	if err != nil || cookie.Value == "" {
		return store.Session{}, store.User{}, false, nil
	}
	tokenHash := hashToken(cookie.Value)
	session, user, err := s.store.FindSession(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return store.Session{}, store.User{}, false, nil
	}
	if err != nil {
		return store.Session{}, store.User{}, false, err
	}
	user, allowed, err := s.checkGitHubAccount(ctx, user)
	if err != nil {
		return store.Session{}, store.User{}, false, err
	}
	if !allowed {
		clearSessionCookie(w, r)
		return store.Session{}, store.User{}, false, nil
	}
	if session.Remember && session.ExpiresAt.Sub(s.now()) < rememberFor-renewAfter {
		expires := s.now().Add(rememberFor)
		if err := s.store.ExtendSession(ctx, tokenHash, expires); err != nil {
			s.log.Warn("renew session", "error", err)
		} else {
			session.ExpiresAt = expires
			setSessionCookie(w, r, cookie.Value, true, expires)
		}
	}
	return session, user, true, nil
}

// checkGitHubAccount rechecks a GitHub account's teams when they are due, and returns the
// user with its role now. allowed is false when the account may no longer log in: its
// sessions are then ended. When the role changed, the user's streams are closed, so that
// open pages reconnect and get what the new role allows. Other accounts are returned as
// they are.
func (s *Server) checkGitHubAccount(ctx context.Context, user store.User) (store.User, bool, error) {
	if user.GitHubID == nil {
		return user, true, nil
	}
	role := s.recheckGitHub(ctx, user)
	if role == "" {
		if err := s.store.EndUserSessions(ctx, user.ID); err != nil {
			return store.User{}, false, err
		}
		s.closeOtherSessions(user.ID, nil)
		return store.User{}, false, nil
	}
	if role != user.Role {
		user.Role = role
		s.closeOtherSessions(user.ID, nil)
	}
	return user, true, nil
}

func userJSON(u store.User) map[string]any {
	return map[string]any{
		"id":               u.ID,
		"name":             u.Username,
		"username":         u.Username,
		"displayUsername":  u.Username,
		"email":            u.Username + "@noreply.uptime-kuma.internal",
		"emailVerified":    false,
		"image":            nil,
		"role":             u.Role,
		"twoFactorEnabled": u.TOTPEnabled,
		"createdAt":        u.CreatedAt,
		"updatedAt":        u.CreatedAt,
	}
}

// getSession answers like better-auth: the session and user, or null.
func (s *Server) getSession(w http.ResponseWriter, r *http.Request) {
	session, user, ok, err := s.currentSession(r.Context(), w, r)
	if err != nil {
		s.log.Error("get session", "error", err)
		authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
		return
	}
	if !ok {
		writeJSON(w, http.StatusOK, nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"session": map[string]any{
			"userId":    user.ID,
			"createdAt": session.CreatedAt,
			"updatedAt": session.CreatedAt,
			"expiresAt": session.ExpiresAt,
		},
		"user": userJSON(user),
	})
}

func (s *Server) signOut(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil && cookie.Value != "" {
		if err := s.store.DeleteSession(r.Context(), hashToken(cookie.Value)); err != nil {
			s.log.Error("sign out", "error", err)
			authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
			return
		}
		s.closeSession(hashToken(cookie.Value))
	}
	clearSessionCookie(w, r)
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (s *Server) changePassword(w http.ResponseWriter, r *http.Request) {
	_, user, ok, err := s.currentSession(r.Context(), w, r)
	if err != nil {
		s.log.Error("change password", "error", err)
		authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
		return
	}
	if !ok {
		authError(w, http.StatusUnauthorized, "UNAUTHORIZED", "You are not logged in")
		return
	}
	var body struct {
		CurrentPassword string `json:"currentPassword"`
		NewPassword     string `json:"newPassword"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if refuseGitHubAccount(w, user) {
		return
	}
	if !s.limiter.allow(user.Username) {
		authError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Too many requests. Please try again later.")
		return
	}
	if !verifyPassword(user.PasswordHash, body.CurrentPassword) {
		authError(w, http.StatusBadRequest, "INVALID_PASSWORD", "Invalid password")
		return
	}
	if err := s.checkNewPassword(r.Context(), body.NewPassword); err != nil {
		switch {
		case errors.Is(err, errPasswordLeaked):
			authError(w, http.StatusBadRequest, "PASSWORD_COMPROMISED", err.Error())
		case errors.Is(err, errLeakCheckFailed):
			authError(w, http.StatusServiceUnavailable, "PASSWORD_CHECK_FAILED", err.Error())
		default:
			authError(w, http.StatusBadRequest, "INVALID_PASSWORD_LENGTH", err.Error())
		}
		return
	}
	hash, err := hashPassword(body.NewPassword)
	if err != nil {
		s.log.Error("change password", "error", err)
		authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
		return
	}
	cookie, _ := r.Cookie(sessionCookie)
	keep := hashToken(cookie.Value)
	// Other logins end, as after any password change, so a stolen session stops working.
	if err := s.store.SetPassword(r.Context(), user.ID, hash, keep); err != nil {
		s.log.Error("change password", "error", err)
		authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
		return
	}
	s.closeOtherSessions(user.ID, keep)
	s.log.Info("password changed", "username", user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"token": nil, "user": userJSON(user)})
}

// refuseGitHubAccount answers a password or two-factor change for an account that logs in with
// GitHub, which has neither, and returns whether it did.
func refuseGitHubAccount(w http.ResponseWriter, user store.User) bool {
	if user.GitHubID == nil {
		return false
	}
	authError(w, http.StatusBadRequest, "GITHUB_ACCOUNT",
		"This account logs in with GitHub, so it has no password or two-factor login here. Manage them on GitHub.")
	return true
}

func (s *Server) authNotSupported(w http.ResponseWriter, r *http.Request) {
	authError(w, http.StatusNotFound, "NOT_SUPPORTED",
		"This isn't supported by this server yet: "+strings.TrimPrefix(r.URL.Path, "/api/auth"))
}

// loginLimiter allows 10 login attempts a minute per username and 60 overall. The service
// usually runs behind a proxy, so the client's address isn't known reliably.
type loginLimiter struct {
	mu      sync.Mutex
	global  *rate.Limiter
	users   map[string]*rate.Limiter
	perUser rate.Limit
	burst   int
}

func newLoginLimiter() *loginLimiter {
	return &loginLimiter{
		global: rate.NewLimiter(rate.Every(time.Second), 60), users: map[string]*rate.Limiter{},
		perUser: rate.Every(6 * time.Second), burst: 10,
	}
}

func (l *loginLimiter) allow(username string) bool {
	key := strings.ToLower(strings.TrimSpace(username))
	l.mu.Lock()
	defer l.mu.Unlock()
	limiter, ok := l.users[key]
	if !ok {
		if len(l.users) > 10000 {
			clear(l.users)
		}
		limiter = rate.NewLimiter(l.perUser, l.burst)
		l.users[key] = limiter
	}
	return limiter.Allow() && l.global.Allow()
}

// CreateInitialAdmin creates the first account, so that a deployment needs no setup in the
// browser. It does nothing if an account exists, and returns whether it created one. The
// password must meet the same rules as at setup.
func (s *Server) CreateInitialAdmin(ctx context.Context, username, password string) (bool, error) {
	exists, err := s.store.HasUser(ctx)
	if err != nil {
		return false, err
	}
	if exists {
		return false, nil
	}
	username = strings.TrimSpace(username)
	if err := checkUsername(username); err != nil {
		return false, err
	}
	if err := s.checkNewPassword(ctx, password); err != nil {
		return false, err
	}
	hash, err := hashPassword(password)
	if err != nil {
		return false, err
	}
	if _, err := s.store.CreateFirstUser(ctx, username, hash); err != nil {
		if errors.Is(err, store.ErrSetupDone) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}
