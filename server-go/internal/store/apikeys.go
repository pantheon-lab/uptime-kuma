package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// APIKey describes a key. The key itself is never stored, only its hash.
type APIKey struct {
	ID         int64      `json:"id"`
	Name       string     `json:"name"`
	Prefix     string     `json:"prefix"`
	Scopes     []string   `json:"scopes"`
	CreatedAt  time.Time  `json:"createdAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	RevokedAt  *time.Time `json:"revokedAt"`
	LastUsedAt *time.Time `json:"lastUsedAt"`
}

const apiKeyColumns = `id, name, key_prefix, scopes, created_at, expires_at, revoked_at, last_used_at`

func scanAPIKey(row pgx.Row) (APIKey, error) {
	var k APIKey
	err := row.Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt)
	return k, err
}

// CreateAPIKey stores a new key by its hash.
func (s *Store) CreateAPIKey(ctx context.Context, key APIKey, hash []byte) (APIKey, error) {
	created, err := scanAPIKey(s.pool.QueryRow(ctx, `
		INSERT INTO api_key (name, key_prefix, key_hash, scopes, expires_at)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING `+apiKeyColumns, key.Name, key.Prefix, hash, key.Scopes, key.ExpiresAt))
	if err != nil {
		return APIKey{}, fmt.Errorf("create API key: %w", err)
	}
	return created, nil
}

// ListAPIKeys returns all keys, including expired and revoked ones.
func (s *Store) ListAPIKeys(ctx context.Context) ([]APIKey, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+apiKeyColumns+` FROM api_key ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list API keys: %w", err)
	}
	keys, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (APIKey, error) { return scanAPIKey(r) })
	if err != nil {
		return nil, fmt.Errorf("read API keys: %w", err)
	}
	return keys, nil
}

// FindAPIKey returns the usable key with this hash, or ErrNotFound if there is none, or
// if it has expired or been revoked. It isn't cached, so revoking takes effect immediately.
func (s *Store) FindAPIKey(ctx context.Context, hash []byte) (APIKey, error) {
	key, err := scanAPIKey(s.pool.QueryRow(ctx, `
		SELECT `+apiKeyColumns+` FROM api_key
		WHERE key_hash = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, hash))
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, ErrNotFound
	}
	if err != nil {
		return APIKey{}, fmt.Errorf("find API key: %w", err)
	}
	return key, nil
}

// RevokeAPIKey disables a key, or returns ErrNotFound if it doesn't exist or is already revoked.
func (s *Store) RevokeAPIKey(ctx context.Context, id int64) error {
	tag, err := s.pool.Exec(ctx, `UPDATE api_key SET revoked_at = now() WHERE id = $1 AND revoked_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("revoke API key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// TouchAPIKey records that a key was just used.
func (s *Store) TouchAPIKey(ctx context.Context, id int64) error {
	if _, err := s.pool.Exec(ctx, `UPDATE api_key SET last_used_at = now() WHERE id = $1`, id); err != nil {
		return fmt.Errorf("touch API key: %w", err)
	}
	return nil
}
