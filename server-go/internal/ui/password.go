package ui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"golang.org/x/crypto/bcrypt"
	"golang.org/x/crypto/scrypt"
	"golang.org/x/text/unicode/norm"
)

// Password hashes use the format of better-auth, which the Node.js backend used:
// "<salt>:<key>", where salt is 16 random bytes in hex, used as text, and key is
// scrypt(NFKC(password), salt, N=16384, r=16, p=1, 64 bytes) in hex. Accounts from that
// backend could therefore be imported as they are.
const (
	scryptN      = 16384
	scryptR      = 16
	scryptP      = 1
	scryptKeyLen = 64

	minPasswordLength = 8
	maxPasswordLength = 128
)

func hashPassword(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}
	saltText := hex.EncodeToString(salt)
	key, err := deriveKey(password, saltText)
	if err != nil {
		return "", err
	}
	return saltText + ":" + hex.EncodeToString(key), nil
}

// isBcrypt reports a hash from the user table of Node.js before better-auth, which kuma-import
// copies when nobody logged in since. The first login replaces it.
func isBcrypt(hash string) bool {
	return strings.HasPrefix(hash, "$2")
}

func verifyPassword(hash, password string) bool {
	if isBcrypt(hash) {
		return bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) == nil
	}
	saltText, keyText, ok := strings.Cut(hash, ":")
	if !ok || saltText == "" {
		return false
	}
	want, err := hex.DecodeString(keyText)
	if err != nil || len(want) != scryptKeyLen {
		return false
	}
	got, err := deriveKey(password, saltText)
	if err != nil {
		return false
	}
	return subtle.ConstantTimeCompare(got, want) == 1
}

func deriveKey(password, salt string) ([]byte, error) {
	key, err := scrypt.Key([]byte(norm.NFKC.String(password)), []byte(salt), scryptN, scryptR, scryptP, scryptKeyLen)
	if err != nil {
		return nil, fmt.Errorf("derive key: %w", err)
	}
	return key, nil
}

// dummyHash is checked when a username doesn't exist, so that a login takes as long
// whether or not the account exists.
var dummyHash = func() string {
	hash, err := hashPassword("not a real password")
	if err != nil {
		panic(err)
	}
	return hash
}()

func checkPasswordRules(password string) error {
	n := utf8.RuneCountInString(password)
	if n < minPasswordLength {
		return fmt.Errorf("password must be at least %d characters", minPasswordLength)
	}
	if n > maxPasswordLength {
		return fmt.Errorf("password must be at most %d characters", maxPasswordLength)
	}
	return nil
}

func checkUsername(username string) error {
	if strings.TrimSpace(username) == "" {
		return errors.New("username is required")
	}
	if utf8.RuneCountInString(username) > 64 {
		return errors.New("username must be at most 64 characters")
	}
	return nil
}
