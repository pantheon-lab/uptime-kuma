package ui

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1" //nolint:gosec // TOTP (RFC 6238) uses HMAC-SHA1, which authenticator apps expect.
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"fmt"
	"net/url"
	"strings"
	"time"
)

// Codes are those of authenticator apps, and of better-auth, which the Node.js backend used:
// RFC 6238 with HMAC-SHA1, 6 digits and 30-second steps. A code of the step before or after
// is also accepted, for clocks that are a little apart.
const (
	totpDigits     = 6
	totpPeriod     = 30
	totpWindow     = 1
	totpSecretSize = 20
)

func newTOTPSecret() ([]byte, error) {
	secret := make([]byte, totpSecretSize)
	if _, err := rand.Read(secret); err != nil {
		return nil, fmt.Errorf("generate two-factor key: %w", err)
	}
	return secret, nil
}

// totpURI is what authenticator apps read from the QR code.
func totpURI(issuer, account string, secret []byte) string {
	params := url.Values{}
	params.Set("secret", base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret))
	params.Set("issuer", issuer)
	params.Set("digits", fmt.Sprint(totpDigits))
	params.Set("period", fmt.Sprint(totpPeriod))
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) + "?" + params.Encode()
}

func hotp(secret []byte, counter int64) string {
	mac := hmac.New(sha1.New, secret)
	var msg [8]byte
	binary.BigEndian.PutUint64(msg[:], uint64(counter))
	mac.Write(msg[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	mod := uint32(1)
	for range totpDigits {
		mod *= 10
	}
	return fmt.Sprintf("%0*d", totpDigits, value%mod)
}

// checkTOTP returns the time step that code is for, or ok=false if it isn't right at now.
// Every step of the window is compared, so that the time taken doesn't tell which one matched.
func checkTOTP(secret []byte, code string, now time.Time) (step int64, ok bool) {
	code = strings.ReplaceAll(strings.TrimSpace(code), " ", "")
	if len(code) != totpDigits || len(secret) == 0 {
		return 0, false
	}
	current := now.Unix() / totpPeriod
	for i := int64(-totpWindow); i <= totpWindow; i++ {
		if subtle.ConstantTimeCompare([]byte(hotp(secret, current+i)), []byte(code)) == 1 && !ok {
			step, ok = current+i, true
		}
	}
	return step, ok
}
