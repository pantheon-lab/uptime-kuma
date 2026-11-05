package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// API key scopes.
const (
	ScopeMonitorsRead       = "monitors:read"
	ScopeMonitorsWrite      = "monitors:write"
	ScopeBeatsRead          = "beats:read"
	ScopeBeatsWrite         = "beats:write"
	ScopeNotificationsRead  = "notifications:read"
	ScopeNotificationsWrite = "notifications:write"

	// scopeAdmin can't be granted to a key: only the admin token has it.
	scopeAdmin = "admin"

	keyPrefix       = "kuma_"
	keyRandomBytes  = 32
	shownPrefixLen  = len(keyPrefix) + 8
	maxKeyNameLen   = 100
	touchInterval   = time.Minute
	defaultKeyRate  = 50
	defaultKeyBurst = 200
)

var grantableScopes = []string{ScopeMonitorsRead, ScopeMonitorsWrite, ScopeBeatsRead, ScopeBeatsWrite, ScopeNotificationsRead, ScopeNotificationsWrite}

// principal is who is making a request. keyID is 0 for the admin token.
type principal struct {
	admin  bool
	keyID  int64
	scopes []string
}

func (p principal) has(scope string) bool {
	return p.admin || (scope != scopeAdmin && slices.Contains(p.scopes, scope))
}

func (p principal) String() string {
	if p.admin {
		return "admin"
	}
	return "key:" + strconv.FormatInt(p.keyID, 10)
}

type principalKey struct{}

func principalFrom(ctx context.Context) principal {
	p, _ := ctx.Value(principalKey{}).(principal)
	return p
}

// generateKey returns a new key, the hash to store, and the prefix that identifies it.
// The key has 256 bits of randomness, so a fast hash is safe here: there's nothing to
// guess, unlike a password, and every request has to compute it.
func generateKey() (plain string, hash []byte, prefix string, err error) {
	random := make([]byte, keyRandomBytes)
	if _, err := rand.Read(random); err != nil {
		return "", nil, "", fmt.Errorf("generate key: %w", err)
	}
	plain = keyPrefix + hex.EncodeToString(random)
	return plain, hashKey(plain), plain[:shownPrefixLen], nil
}

func hashKey(plain string) []byte {
	sum := sha256.Sum256([]byte(plain))
	return sum[:]
}

// require authenticates a request and checks that it has the scope.
func (s *Server) require(scope string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		presented, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			unauthorized(w)
			return
		}

		var who principal
		if subtle.ConstantTimeCompare([]byte(presented), s.token) == 1 {
			who = principal{admin: true}
		} else {
			if !strings.HasPrefix(presented, keyPrefix) {
				unauthorized(w)
				return
			}
			key, err := s.store.FindAPIKey(r.Context(), hashKey(presented))
			if errors.Is(err, store.ErrNotFound) {
				unauthorized(w)
				return
			}
			if err != nil {
				// A database failure isn't a bad key, and a client shouldn't discard its key.
				s.log.Error("look up API key", "error", err)
				writeError(w, http.StatusServiceUnavailable, "try again later")
				return
			}
			who = principal{keyID: key.ID, scopes: key.Scopes}
			s.touchKey(key.ID)

			if wait, allowed := s.limiter.allow(key.ID); !allowed {
				w.Header().Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
				writeError(w, http.StatusTooManyRequests, "rate limit exceeded")
				return
			}
		}

		if !who.has(scope) {
			writeError(w, http.StatusForbidden, "this credential doesn't have the "+scope+" scope")
			return
		}
		trace.SpanFromContext(r.Context()).SetAttributes(attribute.String("api.principal", who.String()))
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, who)))
	}
}

func unauthorized(w http.ResponseWriter) {
	w.Header().Set("WWW-Authenticate", "Bearer")
	writeError(w, http.StatusUnauthorized, "invalid or missing bearer token")
}

// touchKey records use at most once a minute per key, so that reads don't turn into writes.
func (s *Server) touchKey(id int64) {
	now := s.now()
	if last, ok := s.lastTouch.Load(id); ok && now.Sub(last.(time.Time)) < touchInterval {
		return
	}
	s.lastTouch.Store(id, now)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := s.store.TouchAPIKey(ctx, id); err != nil {
			s.log.Warn("record API key use", "key", id, "error", err)
		}
	}()
}

// audit logs a change along with who made it.
func (s *Server) audit(r *http.Request, action string, monitorID int32, extra ...any) {
	args := append([]any{"monitor", monitorID, "by", principalFrom(r.Context()).String()}, extra...)
	s.log.Info(action, args...)
}

type keyLimiter struct {
	mu        sync.Mutex
	perSecond rate.Limit
	burst     int
	limiters  map[int64]*rate.Limiter
}

func newKeyLimiter(perSecond float64, burst int) *keyLimiter {
	return &keyLimiter{perSecond: rate.Limit(perSecond), burst: burst, limiters: make(map[int64]*rate.Limiter)}
}

func (l *keyLimiter) allow(id int64) (time.Duration, bool) {
	l.mu.Lock()
	limiter := l.limiters[id]
	if limiter == nil {
		limiter = rate.NewLimiter(l.perSecond, l.burst)
		l.limiters[id] = limiter
	}
	l.mu.Unlock()

	reservation := limiter.Reserve()
	wait := reservation.Delay()
	if wait > 0 {
		reservation.Cancel()
		return wait, false
	}
	return 0, true
}

// createdKey is the only response that ever contains the key itself.
type createdKey struct {
	store.APIKey
	Key string `json:"key"`
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Name      string     `json:"name"`
		Scopes    []string   `json:"scopes"`
		ExpiresAt *time.Time `json:"expiresAt"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid API key: "+err.Error())
		return
	}
	input.Name = strings.TrimSpace(input.Name)
	if input.Name == "" || len(input.Name) > maxKeyNameLen {
		writeError(w, http.StatusBadRequest, fmt.Sprintf("name must be 1 to %d characters", maxKeyNameLen))
		return
	}
	if len(input.Scopes) == 0 {
		writeError(w, http.StatusBadRequest, "scopes must not be empty, choose from "+strings.Join(grantableScopes, ", "))
		return
	}
	for _, scope := range input.Scopes {
		if !slices.Contains(grantableScopes, scope) {
			writeError(w, http.StatusBadRequest, "unknown scope "+strconv.Quote(scope)+", choose from "+strings.Join(grantableScopes, ", "))
			return
		}
	}
	slices.Sort(input.Scopes)
	input.Scopes = slices.Compact(input.Scopes)
	if input.ExpiresAt != nil && !input.ExpiresAt.After(s.now()) {
		writeError(w, http.StatusBadRequest, "expiresAt must be in the future")
		return
	}

	plain, hash, prefix, err := generateKey()
	if err != nil {
		s.serverError(w, "generate API key", err)
		return
	}
	key, err := s.store.CreateAPIKey(r.Context(), store.APIKey{
		Name: input.Name, Prefix: prefix, Scopes: input.Scopes, ExpiresAt: input.ExpiresAt,
	}, hash)
	if err != nil {
		s.serverError(w, "create API key", err)
		return
	}
	s.log.Info("API key created", "key", key.ID, "name", key.Name, "scopes", key.Scopes, "by", principalFrom(r.Context()).String())
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, createdKey{APIKey: key, Key: plain})
}

func (s *Server) listAPIKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		s.serverError(w, "list API keys", err)
		return
	}
	writeJSON(w, http.StatusOK, keys)
}

func (s *Server) revokeAPIKey(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "API key id must be a positive integer")
		return
	}
	if err := s.store.RevokeAPIKey(r.Context(), id); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "API key not found")
			return
		}
		s.serverError(w, "revoke API key", err)
		return
	}
	s.log.Info("API key revoked", "key", id, "by", principalFrom(r.Context()).String())
	w.WriteHeader(http.StatusNoContent)
}
