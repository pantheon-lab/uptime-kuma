package ui

import (
	"bufio"
	"context"
	"crypto/sha1" //nolint:gosec // The Pwned Passwords range API is keyed by SHA-1.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// PwnedPasswordsURL is the Have I Been Pwned range API, which the Node.js backend also asked.
const PwnedPasswordsURL = "https://api.pwnedpasswords.com/range/"

// errLeakCheckFailed is shown when the leaked password check can't be made. New passwords
// are refused then, as in the Node.js backend, so the message says how to turn it off.
var errLeakCheckFailed = errors.New("the password couldn't be checked against known leaks: " +
	"try again later, or set KUMA_DISABLE_LEAK_PASSWORD_CHECK=true if this server can't reach api.pwnedpasswords.com")

// errPasswordLeaked is better-auth's message.
var errPasswordLeaked = errors.New("The password you entered has been compromised. Please choose a different password.")

// PwnedPasswords returns a check of whether a password is in the Pwned Passwords list. Only
// the first 5 characters of the password's SHA-1 hash are sent (k-anonymity), and the answer
// is padded, so that its size doesn't tell which prefix was asked.
func PwnedPasswords(client *http.Client, baseURL string) func(ctx context.Context, password string) (bool, error) {
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context, password string) (bool, error) {
		sum := sha1.Sum([]byte(password)) //nolint:gosec
		hash := strings.ToUpper(hex.EncodeToString(sum[:]))
		prefix, suffix := hash[:5], hash[5:]
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, baseURL+prefix, nil)
		if err != nil {
			return false, err
		}
		req.Header.Set("Add-Padding", "true")
		req.Header.Set("User-Agent", "Uptime Kuma password checker")
		resp, err := client.Do(req)
		if err != nil {
			return false, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false, fmt.Errorf("pwned passwords: %s", resp.Status)
		}
		scanner := bufio.NewScanner(io.LimitReader(resp.Body, 4<<20))
		for scanner.Scan() {
			candidate, count, found := strings.Cut(strings.TrimSpace(scanner.Text()), ":")
			if !found || !strings.EqualFold(candidate, suffix) {
				continue
			}
			// Padding lines have a count of 0.
			n, err := strconv.ParseUint(count, 10, 64)
			if err != nil {
				return false, fmt.Errorf("pwned passwords: invalid count %q", count)
			}
			return n > 0, nil
		}
		return false, scanner.Err()
	}
}

// checkNewPassword applies the rules for a password being set: its length, and that it isn't
// a known leaked password, when that check is on.
func (s *Server) checkNewPassword(ctx context.Context, password string) error {
	if err := checkPasswordRules(password); err != nil {
		return err
	}
	if s.opts.PasswordLeaked == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	leaked, err := s.opts.PasswordLeaked(ctx, password)
	if err != nil {
		s.log.Warn("leaked password check failed", "error", err)
		return errLeakCheckFailed
	}
	if leaked {
		return errPasswordLeaked
	}
	return nil
}
