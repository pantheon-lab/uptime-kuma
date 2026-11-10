package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// OIDCUser is what an OpenID Connect provider says about someone at login or a check.
type OIDCUser struct {
	// Key is "<issuer> <sub>": a subject is only unique within its issuer.
	Key string
	// Name is the account's username: the email, or else the preferred username or subject.
	Name   string
	Email  string
	Groups []string
}

func oidcTokenAAD(userID int32) string { return secretAAD("app_user", "oidc_token", userID) }

// SaveOIDCUser makes or updates the account of a provider's user at login, with a role. A new
// refresh token replaces the kept one; an empty one keeps it.
func (s *Store) SaveOIDCUser(ctx context.Context, o OIDCUser, role, refreshToken string) (User, error) {
	name := strings.ToLower(o.Name)
	groups := o.Groups
	if groups == nil {
		groups = []string{}
	}
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// A verified email can move to someone else: the account that had it keeps its id, renamed.
		// Any other name, such as a preferred username that a user can set, can't take one.
		if o.Email != "" && name == strings.ToLower(o.Email) {
			if _, err := tx.Exec(ctx, `UPDATE app_user SET username = username || '#' || id
				WHERE lower(username) = $1 AND oidc_sub IS NOT NULL AND oidc_sub <> $2`, name, o.Key); err != nil {
				return err
			}
		}
		var taken bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user WHERE lower(username) = $1
			AND (oidc_sub IS NULL OR oidc_sub <> $2))`, name, o.Key).Scan(&taken); err != nil {
			return err
		}
		if taken {
			return ErrUsernameTakenByPassword
		}
		err := tx.QueryRow(ctx, `
		INSERT INTO app_user (username, password_hash, role, oidc_sub, oidc_email, oidc_groups, oidc_checked_at)
		VALUES ($1, '', $2, $3, $4, $5, now())
		ON CONFLICT (oidc_sub) WHERE oidc_sub IS NOT NULL DO UPDATE
		SET username = excluded.username, role = excluded.role, oidc_email = excluded.oidc_email,
			oidc_groups = excluded.oidc_groups, oidc_checked_at = now()
		RETURNING `+userColumns, name, role, o.Key, strings.ToLower(o.Email), groups).Scan(user.fields()...)
		// Another account took the name meanwhile, or differs from it only in case.
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
		sealed, err := s.sealText(refreshToken, oidcTokenAAD(user.ID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE app_user SET oidc_token = $2 WHERE id = $1`, user.ID, sealed)
		return err
	})
	if errors.Is(err, ErrUsernameTakenByPassword) {
		return User{}, err
	}
	if err != nil {
		return User{}, fmt.Errorf("save OpenID Connect account: %w", err)
	}
	return user, nil
}

// OIDCToken returns an account's refresh token, or ErrNotFound when it has none.
func (s *Store) OIDCToken(ctx context.Context, userID int32) (string, error) {
	var token *string
	err := s.pool.QueryRow(ctx, `SELECT oidc_token FROM app_user WHERE id = $1`, userID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (token == nil || *token == "")) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read OpenID Connect token: %w", err)
	}
	plain, err := s.openText(*token, oidcTokenAAD(userID))
	if err != nil {
		return "", fmt.Errorf("read OpenID Connect token: %w", err)
	}
	return plain, nil
}

// SetOIDCChecked records a check of an account, with what the provider said, the role now, and a
// refresh token that replaces the kept one when it isn't empty, as providers that rotate them send,
// made with the login settings of the version. It returns ErrLoginChanged, recording nothing, when
// the settings changed since.
func (s *Store) SetOIDCChecked(ctx context.Context, userID int32, o OIDCUser, role, refreshToken string, v LoginVersion) error {
	groups := o.Groups
	if groups == nil {
		groups = []string{}
	}
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE app_user SET role = $2, oidc_email = $3, oidc_groups = $4, oidc_checked_at = now()
			WHERE id = $1 AND `+loginCurrent(5), userID, role, strings.ToLower(o.Email), groups, v.Env, v.SavedAt)
		if err != nil {
			return fmt.Errorf("record OpenID Connect check: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrLoginChanged
		}
		if refreshToken == "" {
			return nil
		}
		sealed, err := s.sealText(refreshToken, oidcTokenAAD(userID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE app_user SET oidc_token = $2 WHERE id = $1`, userID, sealed)
		return err
	})
}
