package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

var (
	// ErrLastAdmin means that the change would leave no admin who can log in.
	ErrLastAdmin = errors.New("this is the last admin who can log in")
	// ErrRoleFromGitHub means that the account's role follows its GitHub teams.
	ErrRoleFromGitHub = errors.New("a GitHub account's role follows its teams")
)

// Account is an account as Settings > Accounts lists it.
type Account struct {
	User
	LastLoginAt *time.Time
	// Sessions are the logins that haven't expired, oldest first.
	Sessions []AccountSession
}

// AccountSession is an open login. The hash only tells the page which session is its own.
type AccountSession struct {
	Session
	TokenHash []byte
}

// LoginMethods are the ways in that are turned on, which decide whether an admin can still log
// in: a password account needs password login and a password, a GitHub account GitHub login.
type LoginMethods struct {
	Password bool
	GitHub   bool
	Google   bool
}

// accountsLockID serializes changes that could leave no admin.
const accountsLockID = 7_331_003

// ListAccounts returns every account with its open sessions, by id.
func (s *Store) ListAccounts(ctx context.Context) ([]Account, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+userColumns+`, last_login_at FROM app_user ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	accounts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Account, error) {
		var a Account
		if err := row.Scan(append(a.fields(), &a.LastLoginAt)...); err != nil {
			return a, err
		}
		return a, s.openUser(&a.User)
	})
	if err != nil {
		return nil, fmt.Errorf("list accounts: %w", err)
	}
	byID := make(map[int32]*Account, len(accounts))
	for i := range accounts {
		byID[accounts[i].ID] = &accounts[i]
	}
	rows, err = s.pool.Query(ctx, `
		SELECT user_id, remember, created_at, expires_at, token_hash FROM app_session
		WHERE expires_at > now() ORDER BY created_at, token_hash`)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var session AccountSession
		if err := rows.Scan(&session.UserID, &session.Remember, &session.CreatedAt, &session.ExpiresAt, &session.TokenHash); err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		if a, found := byID[session.UserID]; found {
			a.Sessions = append(a.Sessions, session)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return accounts, nil
}

// otherAdminExists reports whether an admin other than userID can log in.
func otherAdminExists(ctx context.Context, tx pgx.Tx, userID int32, methods LoginMethods) (bool, error) {
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, accountsLockID); err != nil {
		return false, fmt.Errorf("lock accounts: %w", err)
	}
	var exists bool
	err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM app_user
			WHERE id <> $1 AND role = 'admin'
			  AND ((github_id IS NULL AND google_sub IS NULL AND $2 AND password_hash <> '')
			    OR (github_id IS NOT NULL AND $3) OR (google_sub IS NOT NULL AND $4)))`,
		userID, methods.Password, methods.GitHub, methods.Google).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check admins: %w", err)
	}
	return exists, nil
}

// DeleteUser removes an account, which ends its sessions. It returns the account as it was,
// ErrNotFound, or ErrLastAdmin when it's an admin and no other admin can log in.
func (s *Store) DeleteUser(ctx context.Context, userID int32, methods LoginMethods) (User, error) {
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		others, err := otherAdminExists(ctx, tx, userID, methods)
		if err != nil {
			return err
		}
		user, err = s.scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1 FOR UPDATE`, userID))
		if err != nil {
			return err
		}
		if user.IsAdmin() && !others {
			return ErrLastAdmin
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_user WHERE id = $1`, userID); err != nil {
			return fmt.Errorf("delete account: %w", err)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// SetUserRole changes a password account's role. It returns the account as it was,
// ErrNotFound, ErrRoleFromGitHub for a GitHub account, or ErrLastAdmin when it would make the
// last admin who can log in a viewer.
func (s *Store) SetUserRole(ctx context.Context, userID int32, role string, methods LoginMethods) (User, error) {
	if role != RoleAdmin && role != RoleViewer {
		return User{}, fmt.Errorf("unknown role %q", role)
	}
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		others, err := otherAdminExists(ctx, tx, userID, methods)
		if err != nil {
			return err
		}
		user, err = s.scanUser(tx.QueryRow(ctx, `SELECT `+userColumns+` FROM app_user WHERE id = $1 FOR UPDATE`, userID))
		if err != nil {
			return err
		}
		if user.SSO() {
			return ErrRoleFromGitHub
		}
		if user.IsAdmin() && role == RoleViewer && !others {
			return ErrLastAdmin
		}
		if _, err := tx.Exec(ctx, `UPDATE app_user SET role = $2 WHERE id = $1`, userID, role); err != nil {
			return fmt.Errorf("set role: %w", err)
		}
		return nil
	})
	if err != nil {
		return User{}, err
	}
	return user, nil
}

// EndOtherUserSessions logs an account out everywhere but the session keep, which may be nil.
func (s *Store) EndOtherUserSessions(ctx context.Context, userID int32, keep []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_session WHERE user_id = $1 AND token_hash IS DISTINCT FROM $2`,
		userID, keep); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	return nil
}
