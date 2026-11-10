package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrUsernameTakenByPassword means that another account (a password one, or another provider's)
// has the single sign-on account's name as its username, so the login can't take it over.
var ErrUsernameTakenByPassword = errors.New("another account has this name as its username")

// GoogleUser is what Google says about someone at login or a check.
type GoogleUser struct {
	Sub    string
	Email  string
	Domain string
}

func googleTokenAAD(userID int32) string { return secretAAD("app_user", "google_token", userID) }

// SaveGoogleUser makes or updates the account of a Google user at login, named by their email,
// with a role. A new refresh token replaces the kept one; an empty one keeps it, since Google sends
// one only when asked to.
// It returns ErrLoginChanged, saving nothing, when the login settings of the version changed since.
func (s *Store) SaveGoogleUser(ctx context.Context, g GoogleUser, role, refreshToken string, v LoginVersion) (User, error) {
	email := strings.ToLower(g.Email)
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if err := loginHeld(ctx, tx, v); err != nil {
			return err
		}
		// An email can move to someone else: the Google account that had it keeps its id, under a
		// name that says so. A password account with the name isn't taken over.
		if _, err := tx.Exec(ctx, `
			UPDATE app_user SET username = username || '#' || id
			WHERE lower(username) = $1 AND google_sub IS NOT NULL AND google_sub <> $2`, email, g.Sub); err != nil {
			return err
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE lower(username) = $1 AND google_sub IS NULL)`,
			email).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrUsernameTakenByPassword
		}
		err := tx.QueryRow(ctx, `
		INSERT INTO app_user (username, password_hash, role, google_sub, google_email, google_domain, google_checked_at)
		VALUES ($1, '', $2, $3, $1, $4, now())
		ON CONFLICT (google_sub) WHERE google_sub IS NOT NULL DO UPDATE
		SET username = excluded.username, role = excluded.role, google_email = excluded.google_email,
			google_domain = excluded.google_domain, google_checked_at = now()
		RETURNING `+userColumns, email, role, g.Sub, g.Domain).Scan(user.fields()...)
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
			return ErrUsernameTakenByPassword
		}
		if err != nil {
			return err
		}
		if err := s.openUser(&user); err != nil {
			return err
		}
		if refreshToken == "" {
			return nil
		}
		sealed, err := s.sealText(refreshToken, googleTokenAAD(user.ID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE app_user SET google_token = $2 WHERE id = $1`, user.ID, sealed)
		return err
	})
	if errors.Is(err, ErrUsernameTakenByPassword) {
		return User{}, err
	}
	if err != nil {
		return User{}, fmt.Errorf("save Google account: %w", err)
	}
	return user, nil
}

// GoogleToken returns an account's refresh token, or ErrNotFound when it has none.
func (s *Store) GoogleToken(ctx context.Context, userID int32) (string, error) {
	var token *string
	err := s.pool.QueryRow(ctx, `SELECT google_token FROM app_user WHERE id = $1`, userID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (token == nil || *token == "")) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read Google token: %w", err)
	}
	plain, err := s.openText(*token, googleTokenAAD(userID))
	if err != nil {
		return "", fmt.Errorf("read Google token: %w", err)
	}
	return plain, nil
}

// SetGoogleChecked records a check of a Google account, with what Google said and the role now,
// made with the login settings of the version. It returns ErrLoginChanged, recording nothing, when
// the settings changed since.
func (s *Store) SetGoogleChecked(ctx context.Context, userID int32, g GoogleUser, role string, v LoginVersion) error {
	tag, err := s.pool.Exec(ctx, `UPDATE app_user SET role = $2, google_email = $3, google_domain = $4,
		google_checked_at = now() WHERE id = $1 AND `+loginCurrent(5), userID, role, strings.ToLower(g.Email), g.Domain, v.Env, v.SavedAt)
	if err != nil {
		return fmt.Errorf("record Google check: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrLoginChanged
	}
	return nil
}
