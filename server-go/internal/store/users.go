package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrSetupDone means that a user already exists, so the first one can't be created.
	ErrSetupDone = errors.New("a user already exists")
	// ErrUsernameTaken means that another user has the username.
	ErrUsernameTaken = errors.New("username is taken")
)

// User is a web UI account.
type User struct {
	ID           int32
	Username     string
	PasswordHash string
	CreatedAt    time.Time

	// TOTPSecret is the authenticator app's key, set while two-factor login is set up or on.
	TOTPSecret []byte
	// TOTPEnabled means that logging in needs a code as well as the password.
	TOTPEnabled bool
	// TOTPLockedUntil is set after too many wrong codes in a row.
	TOTPLockedUntil *time.Time

	// Role is RoleAdmin or RoleViewer.
	Role string

	// GitHubID is set for an account that logs in with GitHub, which has no password.
	GitHubID *int64
	// GitHubCheckedAt is when the account's GitHub teams were last checked.
	GitHubCheckedAt *time.Time
	// GitHubTeams are the organization's teams the account was in then.
	GitHubTeams []string
}

// Roles. An admin can do everything. A viewer can look at monitors, beats, maintenance and status
// pages, but changes nothing and is sent no credentials.
const (
	RoleAdmin  = "admin"
	RoleViewer = "viewer"
)

// IsAdmin reports whether the account may change things and see credentials.
func (u User) IsAdmin() bool { return u.Role == RoleAdmin }

const userColumns = `id, username, password_hash, created_at, totp_secret, totp_enabled, totp_locked_until, role,
	github_id, github_checked_at, github_teams`

// userColumnsOf is userColumns, each qualified with a table's alias.
func userColumnsOf(alias string) string {
	columns := strings.Split(userColumns, ",")
	for i, c := range columns {
		columns[i] = alias + "." + strings.TrimSpace(c)
	}
	return strings.Join(columns, ", ")
}

// Session is a web UI login. Only the token's hash is stored.
type Session struct {
	UserID    int32
	Remember  bool
	CreatedAt time.Time
	ExpiresAt time.Time
}

// setupLockID serializes creating the first user.
const setupLockID = 7_331_002

// HasUser reports whether any web UI account exists.
func (s *Store) HasUser(ctx context.Context) (bool, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user)`).Scan(&exists); err != nil {
		return false, fmt.Errorf("check users: %w", err)
	}
	return exists, nil
}

// CreateFirstUser creates the first account, or returns ErrSetupDone if any account exists.
func (s *Store) CreateFirstUser(ctx context.Context, username, passwordHash string) (User, error) {
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, setupLockID); err != nil {
			return fmt.Errorf("lock setup: %w", err)
		}
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM app_user)`).Scan(&exists); err != nil {
			return fmt.Errorf("check users: %w", err)
		}
		if exists {
			return ErrSetupDone
		}
		return tx.QueryRow(ctx, `
			INSERT INTO app_user (username, password_hash) VALUES ($1, $2)
			RETURNING `+userColumns, username, passwordHash).
			Scan(user.fields()...)
	})
	if err != nil {
		if errors.Is(err, ErrSetupDone) {
			return User{}, err
		}
		return User{}, fmt.Errorf("create first user: %w", err)
	}
	return user, nil
}

func (u *User) fields() []any {
	return []any{&u.ID, &u.Username, &u.PasswordHash, &u.CreatedAt, &u.TOTPSecret, &u.TOTPEnabled, &u.TOTPLockedUntil, &u.Role, &u.GitHubID, &u.GitHubCheckedAt, &u.GitHubTeams}
}

func (s *Store) scanUser(row pgx.Row) (User, error) {
	var u User
	err := row.Scan(u.fields()...)
	if errors.Is(err, pgx.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	if err := s.openUser(&u); err != nil {
		return User{}, fmt.Errorf("read user: %w", err)
	}
	return u, nil
}

// UserByUsername returns an account or ErrNotFound. Usernames match case-insensitively.
func (s *Store) UserByUsername(ctx context.Context, username string) (User, error) {
	return s.scanUser(s.pool.QueryRow(ctx, `
		SELECT `+userColumns+` FROM app_user
		WHERE lower(username) = lower($1) ORDER BY id LIMIT 1`, username))
}

// UserByID returns an account or ErrNotFound.
func (s *Store) UserByID(ctx context.Context, id int32) (User, error) {
	return s.scanUser(s.pool.QueryRow(ctx, `
		SELECT `+userColumns+` FROM app_user WHERE id = $1`, id))
}

// SetPassword replaces an account's password and ends its other sessions. keep is the hash
// of the session to keep, or nil to end them all.
func (s *Store) SetPassword(ctx context.Context, userID int32, passwordHash string, keep []byte) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `UPDATE app_user SET password_hash = $2 WHERE id = $1`, userID, passwordHash)
		if err != nil {
			return fmt.Errorf("set password: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `DELETE FROM app_session WHERE user_id = $1 AND token_hash IS DISTINCT FROM $2`,
			userID, keep); err != nil {
			return fmt.Errorf("end sessions: %w", err)
		}
		// A login waiting for its code had the old password.
		if _, err := tx.Exec(ctx, `DELETE FROM app_two_factor_challenge WHERE user_id = $1`, userID); err != nil {
			return fmt.Errorf("end two-factor challenges: %w", err)
		}
		return nil
	})
}

// CreateSession stores a login. Expired sessions are cleared on the way.
func (s *Store) CreateSession(ctx context.Context, tokenHash []byte, session Session) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_session WHERE expires_at < now()`); err != nil {
		return fmt.Errorf("clear expired sessions: %w", err)
	}
	// Each session is a login, which Settings > Accounts shows.
	_, err := s.pool.Exec(ctx, `
		WITH login AS (UPDATE app_user SET last_login_at = now() WHERE id = $2)
		INSERT INTO app_session (token_hash, user_id, remember, expires_at) VALUES ($1, $2, $3, $4)`,
		tokenHash, session.UserID, session.Remember, session.ExpiresAt)
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23503" {
			return ErrNotFound
		}
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// FindSession returns a session that hasn't expired, with its user, or ErrNotFound.
func (s *Store) FindSession(ctx context.Context, tokenHash []byte) (Session, User, error) {
	var session Session
	var user User
	err := s.pool.QueryRow(ctx, `
		SELECT s.user_id, s.remember, s.created_at, s.expires_at,
		       `+userColumnsOf("u")+`
		FROM app_session s JOIN app_user u ON u.id = s.user_id
		WHERE s.token_hash = $1 AND s.expires_at > now()`, tokenHash).
		Scan(append([]any{&session.UserID, &session.Remember, &session.CreatedAt, &session.ExpiresAt}, user.fields()...)...)
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, User{}, ErrNotFound
	}
	if err != nil {
		return Session{}, User{}, fmt.Errorf("find session: %w", err)
	}
	if err := s.openUser(&user); err != nil {
		return Session{}, User{}, fmt.Errorf("find session: %w", err)
	}
	return session, user, nil
}

// ExtendSession moves a session's expiry.
func (s *Store) ExtendSession(ctx context.Context, tokenHash []byte, expiresAt time.Time) error {
	if _, err := s.pool.Exec(ctx, `UPDATE app_session SET expires_at = $2 WHERE token_hash = $1`,
		tokenHash, expiresAt); err != nil {
		return fmt.Errorf("extend session: %w", err)
	}
	return nil
}

// DeleteSession ends a login. Ending one that doesn't exist isn't an error.
func (s *Store) DeleteSession(ctx context.Context, tokenHash []byte) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_session WHERE token_hash = $1`, tokenHash); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// Settings returns every setting the web UI has saved.
func (s *Store) Settings(ctx context.Context) (map[string]json.RawMessage, error) {
	rows, err := s.pool.Query(ctx, `SELECT key, value FROM app_setting`)
	if err != nil {
		return nil, fmt.Errorf("query settings: %w", err)
	}
	defer rows.Close()
	settings := map[string]json.RawMessage{}
	for rows.Next() {
		var key string
		var value []byte
		if err := rows.Scan(&key, &value); err != nil {
			return nil, fmt.Errorf("read settings: %w", err)
		}
		settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read settings: %w", err)
	}
	return settings, nil
}

// SaveSettings stores the given settings, leaving others as they are.
func (s *Store) SaveSettings(ctx context.Context, settings map[string]json.RawMessage) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		for key, value := range settings {
			// A value that changes is the UI's from then on, so that an import leaves it.
			if _, err := tx.Exec(ctx, `
				INSERT INTO app_setting (key, value, source) VALUES ($1, $2, 'ui')
				ON CONFLICT (key) DO UPDATE SET value = excluded.value,
					source = CASE WHEN app_setting.value IS DISTINCT FROM excluded.value THEN 'ui' ELSE app_setting.source END`,
				key, []byte(value)); err != nil {
				return fmt.Errorf("save setting %s: %w", key, err)
			}
		}
		return nil
	})
}

// Settings that hold this server's VAPID key pair for web push.
const (
	WebpushPublicKeySetting  = "webpushPublicVapidKey"
	WebpushPrivateKeySetting = "webpushPrivateVapidKey"
)

// vapidAAD is where the VAPID private key is sealed for.
var vapidAAD = secretAAD("app_setting", "value", WebpushPrivateKeySetting)

// WebpushKeys returns the VAPID key pair, made with generate the first time. Browsers subscribe
// with the public key, so it never changes once made: when two requests make one at once, the
// first to be saved wins.
func (s *Store) WebpushKeys(ctx context.Context, generate func() (public, private string, err error)) (string, string, error) {
	read := func() (string, string, error) {
		var public, private *string
		err := s.pool.QueryRow(ctx, `
			SELECT (SELECT value #>> '{}' FROM app_setting WHERE key = $1), (SELECT value #>> '{}' FROM app_setting WHERE key = $2)`,
			WebpushPublicKeySetting, WebpushPrivateKeySetting).Scan(&public, &private)
		if err != nil || public == nil || private == nil {
			return "", "", err
		}
		plain, err := s.openText(*private, vapidAAD)
		return *public, plain, err
	}
	public, private, err := read()
	if err != nil {
		return "", "", fmt.Errorf("read web push keys: %w", err)
	}
	if public != "" && private != "" {
		return public, private, nil
	}
	newPublic, newPrivate, err := generate()
	if err != nil {
		return "", "", fmt.Errorf("make web push keys: %w", err)
	}
	sealedPrivate, err := s.sealText(newPrivate, vapidAAD)
	if err != nil {
		return "", "", err
	}
	pub, _ := json.Marshal(newPublic)
	priv, _ := json.Marshal(sealedPrivate)
	// Both rows, or neither: a concurrent insert of the same keys makes this one do nothing.
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO app_setting (key, value) VALUES ($1, $2), ($3, $4) ON CONFLICT (key) DO NOTHING`,
		WebpushPublicKeySetting, pub, WebpushPrivateKeySetting, priv); err != nil {
		return "", "", fmt.Errorf("save web push keys: %w", err)
	}
	if public, private, err = read(); err != nil {
		return "", "", fmt.Errorf("read web push keys: %w", err)
	}
	if public == "" || private == "" {
		return "", "", errors.New("the web push keys are incomplete")
	}
	return public, private, nil
}

// ImportWebpushKeys saves a VAPID key pair from Node.js, so that browsers subscribed there keep
// receiving messages. It does nothing, and says so, when this server has a pair already.
func (s *Store) ImportWebpushKeys(ctx context.Context, public, private string) (bool, error) {
	sealed, err := s.sealText(private, vapidAAD)
	if err != nil {
		return false, err
	}
	pub, _ := json.Marshal(public)
	priv, _ := json.Marshal(sealed)
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO app_setting (key, value)
		SELECT k, v FROM (VALUES ($1, $2::jsonb), ($3, $4::jsonb)) AS pair (k, v)
		WHERE NOT EXISTS (SELECT 1 FROM app_setting WHERE key IN ($1, $3))
		ON CONFLICT (key) DO NOTHING`,
		WebpushPublicKeySetting, pub, WebpushPrivateKeySetting, priv)
	if err != nil {
		return false, fmt.Errorf("import web push keys: %w", err)
	}
	return tag.RowsAffected() == 2, nil
}

// GitHubAccountPrefix starts the username of an account that logs in with GitHub, such as
// "@octocat", so that it can't be taken by a password account.
const GitHubAccountPrefix = "@"

// SaveGitHubUser makes or updates the account of a GitHub user at login, with the role their
// teams give. The token is kept to check the teams again later.
func (s *Store) SaveGitHubUser(ctx context.Context, githubID int64, login, role, token string, teams []string) (User, error) {
	if teams == nil {
		teams = []string{}
	}
	var user User
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// GitHub logins can be renamed and taken by someone else: the account that had the name
		// before keeps its id, under a name that says so.
		if _, err := tx.Exec(ctx, `
			UPDATE app_user SET username = username || '#' || id
			WHERE lower(username) = lower($1) AND github_id IS NOT NULL AND github_id <> $2`, GitHubAccountPrefix+login, githubID); err != nil {
			return err
		}
		err := tx.QueryRow(ctx, `
		INSERT INTO app_user (username, password_hash, role, github_id, github_token, github_checked_at, github_teams)
		VALUES ($1, '', $2, $3, NULL, now(), $4)
		ON CONFLICT (github_id) WHERE github_id IS NOT NULL DO UPDATE
		SET username = excluded.username, role = excluded.role, github_checked_at = now(), github_teams = excluded.github_teams
		RETURNING `+userColumns, GitHubAccountPrefix+login, role, githubID, teams).Scan(user.fields()...)
		if err != nil {
			return err
		}
		if err := s.openUser(&user); err != nil {
			return err
		}
		// The token is sealed for the account's row, whose id is known now.
		sealed, err := s.sealText(token, secretAAD("app_user", "github_token", user.ID))
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `UPDATE app_user SET github_token = $2 WHERE id = $1`, user.ID, sealed)
		return err
	})
	if err != nil {
		return User{}, fmt.Errorf("save GitHub account: %w", err)
	}
	return user, nil
}

// GitHubToken returns the token an account last logged in with.
func (s *Store) GitHubToken(ctx context.Context, userID int32) (string, error) {
	var token *string
	err := s.pool.QueryRow(ctx, `SELECT github_token FROM app_user WHERE id = $1`, userID).Scan(&token)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && token == nil) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read GitHub token: %w", err)
	}
	plain, err := s.openText(*token, secretAAD("app_user", "github_token", userID))
	if err != nil {
		return "", fmt.Errorf("read GitHub token: %w", err)
	}
	return plain, nil
}

// SetGitHubChecked records a check of a GitHub account's teams, and the role they give now.
func (s *Store) SetGitHubChecked(ctx context.Context, userID int32, role string, teams []string) error {
	if teams == nil {
		teams = []string{}
	}
	if _, err := s.pool.Exec(ctx, `UPDATE app_user SET role = $2, github_checked_at = now(), github_teams = $3 WHERE id = $1`,
		userID, role, teams); err != nil {
		return fmt.Errorf("record GitHub check: %w", err)
	}
	return nil
}

// EndUserSessions logs an account out everywhere, such as when it has left the GitHub teams.
func (s *Store) EndUserSessions(ctx context.Context, userID int32) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM app_session WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("end sessions: %w", err)
	}
	return nil
}
