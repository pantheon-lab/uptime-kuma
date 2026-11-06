package ui

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Two-factor login with an authenticator app, answering like better-auth's twoFactor plugin,
// whose client the UI uses:
//
//   - /two-factor/enable checks the password and makes a key, which isn't used until
//     /two-factor/verify-totp, called while logged in, confirms a code from it.
//   - With two-factor login on, a right password gives no session, but a challenge cookie
//     for 10 minutes, and {twoFactorRedirect: true}. /two-factor/verify-totp with the
//     challenge and a code then logs in.
//   - /two-factor/disable checks the password and turns it off.
//
// Unlike better-auth, a code can't be used twice, and there are no backup codes, which the UI
// never showed. As in better-auth, a challenge allows 5 tries, and 10 wrong codes in a row
// refuse the account's codes for 15 minutes.
const (
	twoFactorCookie     = "kuma_two_factor"
	challengeFor        = 10 * time.Minute
	challengeAttempts   = 5
	totpMaxFailures     = 10
	totpLockFor         = 15 * time.Minute
	defaultTOTPIssuer   = "Uptime Kuma"
	maxTOTPIssuerLength = 64
)

func (s *Server) startChallenge(ctx context.Context, w http.ResponseWriter, r *http.Request, userID int32, remember bool) error {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	token := base64.RawURLEncoding.EncodeToString(raw)
	expires := s.now().Add(challengeFor)
	if err := s.store.CreateTwoFactorChallenge(ctx, hashToken(token), store.TwoFactorChallenge{
		UserID: userID, Remember: remember, ExpiresAt: expires,
	}); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: twoFactorCookie, Value: token, Path: "/api/auth", MaxAge: int(challengeFor.Seconds()),
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
	return nil
}

func clearChallengeCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name: twoFactorCookie, Value: "", Path: "/api/auth", MaxAge: -1,
		HttpOnly: true, Secure: secureRequest(r), SameSite: http.SameSiteLaxMode,
	})
}

func internalAuthError(w http.ResponseWriter) {
	authError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "Internal error")
}

// loggedInUser returns the request's user, or answers 401 and returns ok=false.
func (s *Server) loggedInUser(w http.ResponseWriter, r *http.Request, action string) (store.User, bool) {
	_, user, ok, err := s.currentSession(r.Context(), w, r)
	if err != nil {
		s.log.Error(action, "error", err)
		internalAuthError(w)
		return store.User{}, false
	}
	if !ok {
		authError(w, http.StatusUnauthorized, "UNAUTHORIZED", "You are not logged in")
		return store.User{}, false
	}
	return user, true
}

func (s *Server) enableTwoFactor(w http.ResponseWriter, r *http.Request) {
	user, ok := s.loggedInUser(w, r, "enable two-factor login")
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
		Issuer   string `json:"issuer"`
		Method   string `json:"method"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if refuseGitHubAccount(w, user) {
		return
	}
	if body.Method != "" && body.Method != "totp" {
		authError(w, http.StatusBadRequest, "OTP_NOT_CONFIGURED", "OTP is not available")
		return
	}
	if !s.limiter.allow(user.Username) {
		authError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Too many requests. Please try again later.")
		return
	}
	if !verifyPassword(user.PasswordHash, body.Password) {
		authError(w, http.StatusBadRequest, "INVALID_PASSWORD", "Invalid password")
		return
	}
	if user.TOTPEnabled {
		authError(w, http.StatusBadRequest, "TOTP_ALREADY_ENABLED", "TOTP is already enabled")
		return
	}
	secret, err := newTOTPSecret()
	if err != nil {
		s.log.Error("enable two-factor login", "error", err)
		internalAuthError(w)
		return
	}
	if err := s.store.StartTOTPSetup(r.Context(), user.ID, secret); err != nil {
		if errors.Is(err, store.ErrTOTPEnabled) {
			authError(w, http.StatusBadRequest, "TOTP_ALREADY_ENABLED", "TOTP is already enabled")
			return
		}
		s.log.Error("enable two-factor login", "error", err)
		internalAuthError(w)
		return
	}
	issuer := strings.TrimSpace(body.Issuer)
	if issuer == "" || len(issuer) > maxTOTPIssuerLength {
		issuer = defaultTOTPIssuer
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{
		"method":      "totp",
		"totpURI":     totpURI(issuer, user.Username, secret),
		"backupCodes": []string{},
	})
}

func (s *Server) disableTwoFactor(w http.ResponseWriter, r *http.Request) {
	user, ok := s.loggedInUser(w, r, "disable two-factor login")
	if !ok {
		return
	}
	var body struct {
		Password string `json:"password"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	if !s.limiter.allow(user.Username) {
		authError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Too many requests. Please try again later.")
		return
	}
	if !verifyPassword(user.PasswordHash, body.Password) {
		authError(w, http.StatusBadRequest, "INVALID_PASSWORD", "Invalid password")
		return
	}
	if err := s.store.DisableTOTP(r.Context(), user.ID); err != nil {
		s.log.Error("disable two-factor login", "error", err)
		internalAuthError(w)
		return
	}
	s.log.Info("two-factor login disabled", "username", user.Username)
	writeJSON(w, http.StatusOK, map[string]bool{"status": true})
}

func (s *Server) verifyTOTP(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code        string `json:"code"`
		TrustDevice bool   `json:"trustDevice"`
	}
	if !decodeBody(w, r, &body) {
		return
	}
	_, user, loggedIn, err := s.currentSession(r.Context(), w, r)
	if err != nil {
		s.log.Error("verify two-factor code", "error", err)
		internalAuthError(w)
		return
	}
	if loggedIn {
		if refuseGitHubAccount(w, user) {
			return
		}
		s.confirmTOTP(w, r, user, body.Code)
		return
	}
	s.completeTwoFactorLogin(w, r, body.Code)
}

// confirmTOTP turns two-factor login on, once a code shows that the app has the key.
func (s *Server) confirmTOTP(w http.ResponseWriter, r *http.Request, user store.User, code string) {
	if !s.limiter.allow(user.Username) {
		authError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Too many requests. Please try again later.")
		return
	}
	if len(user.TOTPSecret) == 0 {
		authError(w, http.StatusBadRequest, "TOTP_NOT_ENABLED", "TOTP not enabled")
		return
	}
	step, valid := checkTOTP(user.TOTPSecret, code, s.now())
	if !valid {
		authError(w, http.StatusUnauthorized, "INVALID_CODE", "Invalid code")
		return
	}
	if user.TOTPEnabled {
		// Already on: better-auth answers a right code all the same.
		if used, err := s.store.UseTOTPStep(r.Context(), user.ID, step); err != nil {
			s.log.Error("verify two-factor code", "error", err)
			internalAuthError(w)
			return
		} else if !used {
			authError(w, http.StatusUnauthorized, "INVALID_CODE", "Invalid code")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"token": nil, "user": userJSON(user)})
		return
	}
	cookie, _ := r.Cookie(sessionCookie)
	keep := hashToken(cookie.Value)
	// Other logins end, because they didn't need a code.
	if err := s.store.EnableTOTP(r.Context(), user.ID, user.TOTPSecret, step, keep); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			// Set up again in the meantime, from another tab.
			authError(w, http.StatusBadRequest, "INVALID_CODE", "Invalid code")
			return
		}
		s.log.Error("enable two-factor login", "error", err)
		internalAuthError(w)
		return
	}
	s.closeOtherSessions(user.ID, keep)
	user.TOTPEnabled = true
	s.log.Info("two-factor login enabled", "username", user.Username)
	writeJSON(w, http.StatusOK, map[string]any{"token": nil, "user": userJSON(user)})
}

// completeTwoFactorLogin logs in a challenge whose code is right.
func (s *Server) completeTwoFactorLogin(w http.ResponseWriter, r *http.Request, code string) {
	ctx := r.Context()
	invalidChallenge := func() {
		clearChallengeCookie(w, r)
		authError(w, http.StatusUnauthorized, "INVALID_TWO_FACTOR_COOKIE", "Invalid two factor cookie")
	}
	cookie, err := r.Cookie(twoFactorCookie)
	if err != nil || cookie.Value == "" {
		invalidChallenge()
		return
	}
	tokenHash := hashToken(cookie.Value)
	challenge, user, err := s.store.FindTwoFactorChallenge(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		invalidChallenge()
		return
	}
	if err != nil {
		s.log.Error("verify two-factor code", "error", err)
		internalAuthError(w)
		return
	}
	if !s.limiter.allow(user.Username) {
		authError(w, http.StatusTooManyRequests, "TOO_MANY_REQUESTS", "Too many requests. Please try again later.")
		return
	}
	if !user.TOTPEnabled || len(user.TOTPSecret) == 0 {
		// Turned off since the password was checked.
		invalidChallenge()
		return
	}
	if user.TOTPLockedUntil != nil && user.TOTPLockedUntil.After(s.now()) {
		authError(w, http.StatusTooManyRequests, "ACCOUNT_TEMPORARILY_LOCKED",
			"Too many failed verification attempts. Your account is temporarily locked. Please try again later.")
		return
	}
	attempts, err := s.store.CountTwoFactorAttempt(ctx, tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		invalidChallenge()
		return
	}
	if err != nil {
		s.log.Error("verify two-factor code", "error", err)
		internalAuthError(w)
		return
	}
	if attempts > challengeAttempts {
		if _, err := s.store.DeleteTwoFactorChallenge(ctx, tokenHash); err != nil {
			s.log.Error("verify two-factor code", "error", err)
		}
		clearChallengeCookie(w, r)
		authError(w, http.StatusBadRequest, "TOO_MANY_ATTEMPTS_REQUEST_NEW_CODE", "Too many attempts. Please request a new code.")
		return
	}
	step, valid := checkTOTP(user.TOTPSecret, code, s.now())
	if !valid {
		if err := s.store.RecordTOTPFailure(ctx, user.ID, totpMaxFailures, totpLockFor); err != nil {
			s.log.Error("verify two-factor code", "error", err)
		}
		s.log.Warn("failed two-factor login", "username", user.Username)
		authError(w, http.StatusUnauthorized, "INVALID_CODE", "Invalid code")
		return
	}
	used, err := s.store.UseTOTPStep(ctx, user.ID, step)
	if err != nil {
		s.log.Error("verify two-factor code", "error", err)
		internalAuthError(w)
		return
	}
	if !used {
		s.log.Warn("two-factor code used again", "username", user.Username)
		authError(w, http.StatusUnauthorized, "INVALID_CODE", "Invalid code")
		return
	}
	// Only one of two requests with the same challenge may log in.
	if consumed, err := s.store.DeleteTwoFactorChallenge(ctx, tokenHash); err != nil {
		s.log.Error("verify two-factor code", "error", err)
		internalAuthError(w)
		return
	} else if !consumed {
		invalidChallenge()
		return
	}
	if err := s.startSession(ctx, w, r, user.ID, challenge.Remember); err != nil {
		s.log.Error("verify two-factor code", "error", err)
		internalAuthError(w)
		return
	}
	clearChallengeCookie(w, r)
	s.log.Info("login", "username", user.Username, "twoFactor", true)
	writeJSON(w, http.StatusOK, map[string]any{"token": nil, "user": userJSON(user)})
}
