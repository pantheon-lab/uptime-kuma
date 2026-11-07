package api

import (
	"crypto/sha256"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Keys imported from Uptime Kuma keep working: better-auth's, whose stored hash is the
// base64url of the key's SHA-256, and the older table's "uk<id>_<secret>", with a bcrypt hash.
func TestImportedKeys(t *testing.T) {
	betterAuth := strings.Repeat("aB3", 21) + "x" // 64 characters, as better-auth makes them
	// What better-auth stored, decoded as kuma-import does.
	stored := sha256.Sum256([]byte(betterAuth))
	decoded, err := base64.RawURLEncoding.DecodeString(base64.RawURLEncoding.EncodeToString(stored[:]))
	if err != nil {
		t.Fatal(err)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte("legacy-secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	all := GrantableScopes()
	f := &fakeStore{
		keys:   map[string]store.APIKey{string(decoded): {ID: 8, Name: "better-auth", Scopes: all}},
		legacy: map[int32]legacyKeyEntry{3: {key: store.APIKey{ID: 9, Name: "old", Scopes: all}, hash: string(hash)}},
	}
	ts, _ := newTestServer(t, f)
	for key, want := range map[string]int{
		betterAuth:          http.StatusOK,
		"uk3_legacy-secret": http.StatusOK,
		"uk3_wrong-secret":  http.StatusUnauthorized,
		"uk4_legacy-secret": http.StatusUnauthorized,
		betterAuth[:63]:     http.StatusUnauthorized,
		"short":             http.StatusUnauthorized,
	} {
		if res := do(t, "GET", ts.URL+"/api/v1/monitors", key, ""); res.StatusCode != want {
			t.Errorf("%.12s…: %d, want %d", key, res.StatusCode, want)
		}
	}
	// The bcrypt check is remembered, but the key is still looked up: revoking it works at once.
	if res := do(t, "GET", ts.URL+"/api/v1/monitors", "uk3_legacy-secret", ""); res.StatusCode != http.StatusOK {
		t.Fatalf("again: %d", res.StatusCode)
	}
	delete(f.legacy, 3)
	if res := do(t, "GET", ts.URL+"/api/v1/monitors", "uk3_legacy-secret", ""); res.StatusCode != http.StatusUnauthorized {
		t.Errorf("after revoking: %d", res.StatusCode)
	}
}
