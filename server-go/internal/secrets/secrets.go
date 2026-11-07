// Package secrets encrypts values stored in the database with a key kept outside it, so that a
// copy of the database, such as a backup, doesn't give them away.
//
// A sealed value is text: "kenc:v1:<key id>:<base64 of nonce and ciphertext>", AES-256-GCM with
// associated data that names where the value is stored, such as "proxy.password:3", so that a
// value moved to another column or row doesn't open. The key id is the start of the key's
// SHA-256, so that a keyring with the new key and the old ones opens values sealed with any of
// them, and can tell which need sealing again.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"
)

// Prefix starts every sealed value.
const Prefix = "kenc:v1:"

var (
	// ErrNoKey means that a value is sealed, and no key was given.
	ErrNoKey = errors.New("the value is encrypted, and KUMA_SECRET_KEY isn't set")
	// ErrUnknownKey means that a value was sealed with a key that isn't in the keyring.
	ErrUnknownKey = errors.New("the value was encrypted with a key that isn't in KUMA_SECRET_KEY or KUMA_SECRET_KEY_OLD")
)

type key struct {
	id   string
	aead cipher.AEAD
}

// Keyring seals with its current key, and opens with any of its keys. A nil Keyring seals
// nothing, and opens only plain values.
type Keyring struct {
	current *key
	byID    map[string]*key
}

// New makes a keyring that seals with current and also opens values sealed with old.
func New(current []byte, old ...[]byte) (*Keyring, error) {
	k := &Keyring{byID: map[string]*key{}}
	for i, raw := range append([][]byte{current}, old...) {
		if len(raw) != 32 {
			return nil, fmt.Errorf("a key must be 32 bytes, not %d", len(raw))
		}
		block, err := aes.NewCipher(raw)
		if err != nil {
			return nil, err
		}
		aead, err := cipher.NewGCM(block)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(raw)
		entry := &key{id: hex.EncodeToString(sum[:4]), aead: aead}
		if i == 0 {
			k.current = entry
		}
		if _, found := k.byID[entry.id]; !found {
			k.byID[entry.id] = entry
		}
	}
	return k, nil
}

// ParseKey reads a key: 32 bytes in base64 (standard or URL-safe, padded or not) or in hex.
func ParseKey(text string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if b, err := hex.DecodeString(text); err == nil && len(b) == 32 {
		return b, nil
	}
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(text); err == nil && len(b) == 32 {
			return b, nil
		}
	}
	return nil, errors.New("a key must be 32 bytes in base64 or hex, such as from `openssl rand -base64 32`")
}

// FromEnv makes the keyring from KUMA_SECRET_KEY or KUMA_SECRET_KEY_FILE, and the keys it
// replaced from KUMA_SECRET_KEY_OLD or KUMA_SECRET_KEY_OLD_FILE, comma- or line-separated. It
// returns nil without a key.
func FromEnv() (*Keyring, error) {
	current, err := envValue("KUMA_SECRET_KEY")
	if err != nil || current == "" {
		return nil, err
	}
	key, err := ParseKey(current)
	if err != nil {
		return nil, fmt.Errorf("KUMA_SECRET_KEY: %w", err)
	}
	oldText, err := envValue("KUMA_SECRET_KEY_OLD")
	if err != nil {
		return nil, err
	}
	var old [][]byte
	for _, part := range strings.FieldsFunc(oldText, func(r rune) bool { return r == ',' || r == '\n' || r == '\r' }) {
		if strings.TrimSpace(part) == "" {
			continue
		}
		b, err := ParseKey(part)
		if err != nil {
			return nil, fmt.Errorf("KUMA_SECRET_KEY_OLD: %w", err)
		}
		old = append(old, b)
	}
	return New(key, old...)
}

func envValue(name string) (string, error) {
	if path := os.Getenv(name + "_FILE"); path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("%s_FILE: %w", name, err)
		}
		return strings.TrimSpace(string(b)), nil
	}
	return strings.TrimSpace(os.Getenv(name)), nil
}

// Enabled reports whether the keyring seals values.
func (k *Keyring) Enabled() bool { return k != nil }

// CurrentID is the id of the key that seals.
func (k *Keyring) CurrentID() string {
	if k == nil {
		return ""
	}
	return k.current.id
}

// IsSealed reports whether a stored value is sealed.
func IsSealed(value string) bool { return strings.HasPrefix(value, Prefix) }

// KeyID returns the id of the key that sealed a value, or "" for a plain value.
func KeyID(value string) string {
	if !IsSealed(value) {
		return ""
	}
	id, _, _ := strings.Cut(strings.TrimPrefix(value, Prefix), ":")
	return id
}

// Seal encrypts plain for the place aad names. A nil keyring returns it as it is.
func (k *Keyring) Seal(plain []byte, aad string) (string, error) {
	if k == nil {
		return string(plain), nil
	}
	nonce := make([]byte, k.current.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := k.current.aead.Seal(nonce, nonce, plain, []byte(aad))
	return Prefix + k.current.id + ":" + base64.RawStdEncoding.EncodeToString(sealed), nil
}

// Open decrypts a value sealed for the place aad names. A plain value is returned as it is.
func (k *Keyring) Open(value, aad string) ([]byte, error) {
	if !IsSealed(value) {
		return []byte(value), nil
	}
	if k == nil {
		return nil, ErrNoKey
	}
	id, data, found := strings.Cut(strings.TrimPrefix(value, Prefix), ":")
	entry := k.byID[id]
	if !found || entry == nil {
		return nil, ErrUnknownKey
	}
	raw, err := base64.RawStdEncoding.DecodeString(data)
	if err != nil || len(raw) < entry.aead.NonceSize() {
		return nil, errors.New("the encrypted value is damaged")
	}
	plain, err := entry.aead.Open(nil, raw[:entry.aead.NonceSize()], raw[entry.aead.NonceSize():], []byte(aad))
	if err != nil {
		return nil, fmt.Errorf("the encrypted value doesn't open for %s: it was changed or moved", aad)
	}
	return plain, nil
}
