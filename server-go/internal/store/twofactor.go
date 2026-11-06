package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrTOTPEnabled means that two-factor login is already on, so it can't be set up again.
var ErrTOTPEnabled = errors.New("two-factor login is already enabled")

// TwoFactorChallenge is a login whose password was right, waiting for its code. Only the
// token's hash is stored.
type TwoFactorChallenge struct {
	UserID    int32
	Remember  bool
	Attempts  int
	ExpiresAt time.Time
}

// StartTOTPSetup stores a new authenticator key that isn't in use yet, replacing any earlier
// one that wasn't confirmed. It returns ErrTOTPEnabled if two-factor login is already on.
func (s *Store) StartTOTPSetup(ctx context.Context, userID int32, secret []byte) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE app_user SET totp_secret = $2, totp_last_step = NULL, totp_failures = 0, totp_locked_until = NULL
		WHERE id = $1 AND NOT totp_enabled`, userID, secret)
	if err != nil {
		return fmt.Errorf("start two-factor setup: %w", err)
	}
	if tag.RowsAffected() == 0 {
		if _, err := s.UserByID(ctx, userID); err != nil {
			return err
		}
		return ErrTOTPEnabled
	}
	return nil
}

// EnableTOTP turns two-factor login on with the key being set up, if it's still the same
// key, and records step as used. It ends the account's other sessions, which didn't need a
// code: keep is the hash of the session to keep. It returns ErrNotFound if the key changed.
func (s *Store) EnableTOTP(ctx context.Context, userID int32, secret []byte, step int64, keep []byte) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE app_user SET totp_enabled = true, totp_last_step = $3, totp_failures = 0, totp_locked_until = NULL
			WHERE id = $1 AND totp_secret = $2 AND NOT totp_enabled`, userID, secret, step)
		if err != nil {
			return fmt.Errorf("enable two-factor login: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_session WHERE user_id = $1 AND token_hash IS DISTINCT FROM $2`,
			userID, keep); err != nil {
			return fmt.Errorf("end sessions: %w", err)
		}
		return nil
	})
}

// DisableTOTP turns two-factor login off and forgets the key, and ends logins waiting for a code.
func (s *Store) DisableTOTP(ctx context.Context, userID int32) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE app_user SET totp_secret = NULL, totp_enabled = false, totp_last_step = NULL,
				totp_failures = 0, totp_locked_until = NULL
			WHERE id = $1`, userID)
		if err != nil {
			return fmt.Errorf("disable two-factor login: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_two_factor_challenge WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("end two-factor challenges: %w", err)
		}
		return nil
	})
}

// UseTOTPStep records that a code of time step step was accepted, and clears the count of
// wrong codes. It returns false if a code of this step or a later one was already used, so
// that a code someone saw can't be used again.
func (s *Store) UseTOTPStep(ctx context.Context, userID int32, step int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE app_user SET totp_last_step = $2, totp_failures = 0, totp_locked_until = NULL
		WHERE id = $1 AND (totp_last_step IS NULL OR totp_last_step < $2)`, userID, step)
	if err != nil {
		return false, fmt.Errorf("record two-factor code: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// RecordTOTPFailure counts a wrong code. After max wrong codes in a row, the account's
// codes are refused until lockFor has passed.
func (s *Store) RecordTOTPFailure(ctx context.Context, userID int32, max int, lockFor time.Duration) error {
	if _, err := s.pool.Exec(ctx, `
		UPDATE app_user SET totp_failures = totp_failures + 1,
			totp_locked_until = CASE WHEN totp_failures + 1 >= $2 THEN now() + make_interval(secs => $3) ELSE totp_locked_until END
		WHERE id = $1`, userID, max, lockFor.Seconds()); err != nil {
		return fmt.Errorf("record wrong two-factor code: %w", err)
	}
	return nil
}

// CreateTwoFactorChallenge stores a login waiting for its code. Expired ones are cleared on the way.
func (s *Store) CreateTwoFactorChallenge(ctx context.Context, tokenHash []byte, c TwoFactorChallenge) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_two_factor_challenge WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("clear expired two-factor challenges: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO app_two_factor_challenge (token_hash, user_id, remember, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, c.UserID, c.Remember, c.ExpiresAt); err != nil {
		return fmt.Errorf("create two-factor challenge: %w", err)
	}
	return nil
}

// FindTwoFactorChallenge returns a challenge that hasn't expired, with its user, or ErrNotFound.
func (s *Store) FindTwoFactorChallenge(ctx context.Context, tokenHash []byte) (TwoFactorChallenge, User, error) {
	var c TwoFactorChallenge
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT c.user_id, c.remember, c.attempts, c.expires_at,
		       `+userColumnsOf("u")+`
		FROM app_two_factor_challenge c JOIN app_user u ON u.id = c.user_id
		WHERE c.token_hash = $1 AND c.expires_at > now()`, tokenHash).
		Scan(append([]any{&c.UserID, &c.Remember, &c.Attempts, &c.ExpiresAt}, u.fields()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return TwoFactorChallenge{}, User{}, ErrNotFound
	}
	if err != nil {
		return TwoFactorChallenge{}, User{}, fmt.Errorf("find two-factor challenge: %w", err)
	}
	return c, u, nil
}

// CountTwoFactorAttempt counts a try at a challenge, and returns the number of tries so far.
// It returns ErrNotFound if the challenge is gone.
func (s *Store) CountTwoFactorAttempt(ctx context.Context, tokenHash []byte) (int, error) {
	var attempts int
	err := s.pool.QueryRow(ctx, `
		UPDATE app_two_factor_challenge SET attempts = attempts + 1
		WHERE token_hash = $1 AND expires_at > now() RETURNING attempts`, tokenHash).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("count two-factor attempt: %w", err)
	}
	return attempts, nil
}

// DeleteTwoFactorChallenge ends a challenge. It returns false if it was already gone, so
// that only one of two requests with the same challenge logs in.
func (s *Store) DeleteTwoFactorChallenge(ctx context.Context, tokenHash []byte) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM app_two_factor_challenge WHERE token_hash = $1`, tokenHash)
	if err != nil {
		return false, fmt.Errorf("delete two-factor challenge: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}
