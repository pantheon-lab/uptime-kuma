package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
)

// What kuma-import copies besides monitors: settings, API keys, alert state, and the history of
// monitors it skipped. Each is safe to import again.

// What ImportSetting did.
const (
	SettingCopied = "copied"
	SettingSame   = "same"
	SettingKept   = "kept"
)

// ImportSetting copies a setting's value, unless it was changed here. A value here that's a
// default, or what an earlier import wrote, is replaced. It returns SettingCopied, SettingSame
// or SettingKept, with why for SettingKept.
func (s *Store) ImportSetting(ctx context.Context, key string, value json.RawMessage) (string, string, error) {
	result, reason := SettingCopied, ""
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		var same bool
		var source *string
		err := tx.QueryRow(ctx, `SELECT value = $2::jsonb, source FROM app_setting WHERE key = $1 FOR UPDATE`, key, []byte(value)).
			Scan(&same, &source)
		switch {
		case errors.Is(err, pgx.ErrNoRows):
		case err != nil:
			return err
		case same:
			result = SettingSame
			return nil
		case source == nil:
			result, reason = SettingKept, "set here before this version recorded where settings come from"
			return nil
		case *source == "ui":
			result, reason = SettingKept, "changed here"
			return nil
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO app_setting (key, value, source) VALUES ($1, $2, 'import')
			ON CONFLICT (key) DO UPDATE SET value = excluded.value, source = 'import'`, key, []byte(value))
		return err
	})
	if err != nil {
		return "", "", fmt.Errorf("import setting %s: %w", key, err)
	}
	return result, reason, nil
}

// ImportedAPIKey is an API key from Uptime Kuma.
type ImportedAPIKey struct {
	// Source names the key in the source, such as "better-auth:<id>", so that importing it again
	// updates it.
	Source string
	Name   string
	Prefix string
	// Hash is the SHA-256 of the key, for better-auth's keys. LegacyID and LegacyHash are the
	// older table's id and bcrypt hash, for its "uk<id>_<secret>" keys.
	Hash       []byte
	LegacyID   *int32
	LegacyHash string
	Scopes     []string
	CreatedAt  time.Time
	ExpiresAt  *time.Time
	Enabled    bool
}

// ImportAPIKey adds a key from the source, or updates its name, expiry and whether it's on. A
// key revoked here stays revoked. It returns whether the key is new.
func (s *Store) ImportAPIKey(ctx context.Context, k ImportedAPIKey) (bool, error) {
	var revoked *time.Time
	if !k.Enabled {
		now := time.Now()
		revoked = &now
	}
	var hash any
	if k.Hash != nil {
		hash = k.Hash
	}
	var legacyHash any
	if k.LegacyHash != "" {
		legacyHash = k.LegacyHash
	}
	var inserted bool
	err := s.pool.QueryRow(ctx, `
		INSERT INTO api_key (name, key_prefix, key_hash, scopes, created_at, expires_at, revoked_at, legacy_id, legacy_hash, import_source)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
		ON CONFLICT (import_source) DO UPDATE SET name = excluded.name, expires_at = excluded.expires_at,
			revoked_at = COALESCE(api_key.revoked_at, excluded.revoked_at)
		RETURNING xmax = 0`,
		k.Name, k.Prefix, hash, k.Scopes, k.CreatedAt, k.ExpiresAt, revoked, k.LegacyID, legacyHash, k.Source).Scan(&inserted)
	if err != nil {
		return false, fmt.Errorf("import API key: %w", err)
	}
	return inserted, nil
}

// FindLegacyAPIKey returns the usable key imported from the older table with this id, and its
// bcrypt hash, or ErrNotFound.
func (s *Store) FindLegacyAPIKey(ctx context.Context, legacyID int32) (APIKey, string, error) {
	var hash string
	var k APIKey
	err := s.pool.QueryRow(ctx, `
		SELECT `+apiKeyColumns+`, legacy_hash FROM api_key
		WHERE legacy_id = $1 AND revoked_at IS NULL AND (expires_at IS NULL OR expires_at > now())`, legacyID).
		Scan(&k.ID, &k.Name, &k.Prefix, &k.Scopes, &k.CreatedAt, &k.ExpiresAt, &k.RevokedAt, &k.LastUsedAt, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return APIKey{}, "", ErrNotFound
	}
	if err != nil {
		return APIKey{}, "", fmt.Errorf("find API key: %w", err)
	}
	return k, hash, nil
}

// ImportCertAlerts records certificate expiry alerts the source sent, so that they aren't sent
// again. It returns how many were new.
func (s *Store) ImportCertAlerts(ctx context.Context, monitorID int32, days []int) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO cert_alert_sent (monitor_id, days) SELECT $1, d FROM unnest($2::int[]) AS d ON CONFLICT DO NOTHING`,
		monitorID, days)
	if err != nil {
		return 0, fmt.Errorf("import certificate alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ImportTLSInfo stores the certificate the source last saw for a monitor, unless one was seen
// here. It returns whether it did.
func (s *Store) ImportTLSInfo(ctx context.Context, monitorID int32, info, fingerprint string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO monitor_tls_info (monitor_id, info, fingerprint) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		monitorID, info, fingerprint)
	if err != nil {
		return false, fmt.Errorf("import certificate: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// ImportDomainExpiry stores what the source knew of a domain's expiry and the last alert it sent,
// unless it was checked here since. It returns whether it did.
func (s *Store) ImportDomainExpiry(ctx context.Context, r domains.Record) (bool, error) {
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO domain_expiry (domain, expiry, last_check, last_notified_days) VALUES ($1, $2, $3, $4)
		ON CONFLICT (domain) DO UPDATE SET expiry = excluded.expiry, last_check = excluded.last_check,
			last_notified_days = excluded.last_notified_days
		WHERE domain_expiry.last_check IS NULL OR domain_expiry.last_check < excluded.last_check`,
		r.Domain, r.Expiry, r.LastCheck, r.LastNotifiedDays)
	if err != nil {
		return false, fmt.Errorf("import domain expiry: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

// KeepSkippedMonitor records a monitor the import skipped, whose history is kept apart.
func (s *Store) KeepSkippedMonitor(ctx context.Context, sourceID int32, name, reason string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO skipped_monitor (source_id, name, reason) VALUES ($1, $2, $3)
		ON CONFLICT (source_id) DO UPDATE SET name = excluded.name, reason = excluded.reason, kept_at = now()`,
		sourceID, name, reason)
	if err != nil {
		return fmt.Errorf("keep skipped monitor: %w", err)
	}
	return nil
}

// KeepSkippedBeats keeps beats of skipped monitors, whose MonitorID is the source's id. It
// returns how many were new.
func (s *Store) KeepSkippedBeats(ctx context.Context, beats []Beat) (int64, error) {
	if len(beats) == 0 {
		return 0, nil
	}
	n := len(beats)
	ids, times, statuses := make([]int32, n), make([]time.Time, n), make([]int16, n)
	pings, msgs, important := make([]*float64, n), make([]string, n), make([]bool, n)
	durations, retries := make([]int32, n), make([]int32, n)
	for i, b := range beats {
		ids[i], times[i], statuses[i], pings[i], msgs[i] = b.MonitorID, b.Time, b.Status, b.Ping, b.Msg
		important[i], durations[i], retries[i] = b.Important, b.Duration, b.Retries
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO skipped_heartbeat (source_id, time, status, ping, msg, important, duration, retries)
		SELECT * FROM unnest($1::int[], $2::timestamptz[], $3::smallint[], $4::float8[], $5::text[], $6::bool[], $7::int[], $8::int[])
		ON CONFLICT DO NOTHING`, ids, times, statuses, pings, msgs, important, durations, retries)
	if err != nil {
		return 0, fmt.Errorf("keep skipped beats: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ReplaceSkippedStats keeps a skipped monitor's hourly or daily statistics, by its source id.
func (s *Store) ReplaceSkippedStats(ctx context.Context, daily bool, sourceID int32, stats []ImportedStat) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM skipped_stat WHERE source_id = $1 AND daily = $2`, sourceID, daily); err != nil {
			return fmt.Errorf("clear skipped statistics: %w", err)
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"skipped_stat"},
			[]string{"source_id", "daily", "bucket", "up", "down", "maintenance", "ping_sum", "ping_count", "min_ping", "max_ping"},
			pgx.CopyFromSlice(len(stats), func(i int) ([]any, error) {
				r := stats[i]
				return []any{sourceID, daily, r.Bucket, r.Up, r.Down, r.Maintenance, r.PingSum, r.PingCount, r.MinPing, r.MaxPing}, nil
			}))
		if err != nil {
			return fmt.Errorf("keep skipped statistics: %w", err)
		}
		return nil
	})
}

// AdoptSkipped gives a monitor that's imported now the history kept while it was skipped: beats
// and statistics that aren't here already. The kept history is then dropped. It returns how
// many beats were added.
func (s *Store) AdoptSkipped(ctx context.Context, sourceID, monitorID int32) (int64, error) {
	var beats int64
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			INSERT INTO heartbeat (monitor_id, time, status, ping, msg, important, duration, retries)
			SELECT $2, time, status, ping, msg, important, duration, retries FROM skipped_heartbeat WHERE source_id = $1
			ON CONFLICT (monitor_id, time) DO NOTHING`, sourceID, monitorID)
		if err != nil {
			return fmt.Errorf("adopt skipped beats: %w", err)
		}
		beats = tag.RowsAffected()
		for _, table := range []struct {
			name  string
			daily bool
		}{{"imported_stat_hourly", false}, {"imported_stat_daily", true}} {
			if _, err := tx.Exec(ctx, `
				INSERT INTO `+table.name+` (monitor_id, bucket, up, down, maintenance, ping_sum, ping_count, min_ping, max_ping)
				SELECT $2, bucket, up, down, maintenance, ping_sum, ping_count, min_ping, max_ping
				FROM skipped_stat WHERE source_id = $1 AND daily = $3
				ON CONFLICT (monitor_id, bucket) DO NOTHING`, sourceID, monitorID, table.daily); err != nil {
				return fmt.Errorf("adopt skipped statistics: %w", err)
			}
		}
		_, err = tx.Exec(ctx, `DELETE FROM skipped_monitor WHERE source_id = $1`, sourceID)
		return err
	})
	return beats, err
}

// MissingBeats counts the times that have no beat of the monitor.
func (s *Store) MissingBeats(ctx context.Context, monitorID int32, times []time.Time) (int64, error) {
	if len(times) == 0 {
		return 0, nil
	}
	var n int64
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM unnest($2::timestamptz[]) AS t(at)
		WHERE NOT EXISTS (SELECT 1 FROM heartbeat h WHERE h.monitor_id = $1 AND h.time = t.at)`,
		monitorID, times).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("look for beats: %w", err)
	}
	return n, nil
}

// UptimeCounts adds up a monitor's up and down checks in the hourly or daily buckets from one
// time, included, to another, excluded, as the dashboards do.
func (s *Store) UptimeCounts(ctx context.Context, daily bool, monitorID int32, from, to time.Time) (up, down int64, err error) {
	view := "uptime_hourly"
	if daily {
		view = "uptime_daily"
	}
	err = s.pool.QueryRow(ctx, `SELECT COALESCE(sum(up), 0)::bigint, COALESCE(sum(down), 0)::bigint FROM `+view+`
		WHERE monitor_id = $1 AND bucket >= $2 AND bucket < $3`, monitorID, from, to).Scan(&up, &down)
	if err != nil {
		return 0, 0, fmt.Errorf("read uptime: %w", err)
	}
	return up, down, nil
}

// BeatStatusAt returns the status of a monitor's beat at a time, if there's one.
func (s *Store) BeatStatusAt(ctx context.Context, monitorID int32, at time.Time) (int16, bool, error) {
	var status int16
	err := s.pool.QueryRow(ctx, `SELECT status FROM heartbeat WHERE monitor_id = $1 AND time = $2`, monitorID, at).Scan(&status)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read beat: %w", err)
	}
	return status, true, nil
}
