package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// ImportedMonitor is what an earlier import left behind for a source monitor.
type ImportedMonitor struct {
	MonitorID int32
	Active    bool
}

// ImportedStat is one aggregated uptime bucket from the source database.
type ImportedStat struct {
	MonitorID   int32
	Bucket      time.Time
	Up          int64
	Down        int64
	Maintenance int64
	PingSum     float64
	PingCount   int64
	MinPing     *float64
	MaxPing     *float64
}

// ImportedMonitors maps source ids to earlier imports.
func (s *Store) ImportedMonitors(ctx context.Context) (map[int32]ImportedMonitor, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.source_id, i.monitor_id, m.active FROM monitor_import i JOIN monitor m ON m.id = i.monitor_id`)
	if err != nil {
		return nil, fmt.Errorf("read imported monitors: %w", err)
	}
	defer rows.Close()
	result := make(map[int32]ImportedMonitor)
	for rows.Next() {
		var id int32
		var m ImportedMonitor
		if err := rows.Scan(&id, &m.MonitorID, &m.Active); err != nil {
			return nil, fmt.Errorf("read imported monitor: %w", err)
		}
		result[id] = m
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read imported monitors: %w", err)
	}
	return result, nil
}

// ImportMonitor creates or refreshes a monitor from a source monitor, always paused. Its
// parent is set later, once all monitors exist. Returns its id here.
func (s *Store) ImportMonitor(ctx context.Context, sourceID int32, m monitor.Monitor, sourceActive bool, cutover time.Time) (int32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id int32
	err = tx.QueryRow(ctx, `SELECT monitor_id FROM monitor_import WHERE source_id = $1`, sourceID).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		err = tx.QueryRow(ctx, `
			INSERT INTO monitor (name, type, active, interval_seconds, retry_interval_seconds, timeout_seconds,
				max_retries, upside_down, url, method, headers, body, body_encoding, accepted_status_codes,
				max_redirects, ignore_tls, keyword, invert_keyword, hostname, port, resend_interval, expiry_notification)
			VALUES ($1, $2, false, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21)
			RETURNING id`,
			m.Name, m.Type, m.Interval, m.RetryInterval, m.Timeout, m.MaxRetries, m.UpsideDown, m.URL, m.Method, m.Headers,
			m.Body, m.BodyEncoding, m.AcceptedStatusCodes, m.MaxRedirects, m.IgnoreTLS, m.Keyword, m.InvertKeyword,
			m.Hostname, m.Port, m.ResendInterval, m.ExpiryNotification).Scan(&id)
		if err != nil {
			return 0, fmt.Errorf("create monitor: %w", err)
		}
	case err != nil:
		return 0, fmt.Errorf("read import: %w", err)
	default:
		_, err = tx.Exec(ctx, `
			UPDATE monitor SET name = $2, type = $3, interval_seconds = $4, retry_interval_seconds = $5, timeout_seconds = $6,
				max_retries = $7, upside_down = $8, url = $9, method = $10, headers = $11, body = $12, body_encoding = $13,
				accepted_status_codes = $14, max_redirects = $15, ignore_tls = $16, keyword = $17, invert_keyword = $18,
				hostname = $19, port = $20, resend_interval = $21, expiry_notification = $22, updated_at = now()
			WHERE id = $1`,
			id, m.Name, m.Type, m.Interval, m.RetryInterval, m.Timeout, m.MaxRetries, m.UpsideDown, m.URL, m.Method, m.Headers,
			m.Body, m.BodyEncoding, m.AcceptedStatusCodes, m.MaxRedirects, m.IgnoreTLS, m.Keyword, m.InvertKeyword,
			m.Hostname, m.Port, m.ResendInterval, m.ExpiryNotification)
		if err != nil {
			return 0, fmt.Errorf("update monitor: %w", err)
		}
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO monitor_import (source_id, monitor_id, source_active, cutover) VALUES ($1, $2, $3, $4)
		ON CONFLICT (source_id) DO UPDATE SET source_active = $3, cutover = $4, imported_at = now()`,
		sourceID, id, sourceActive, cutover)
	if err != nil {
		return 0, fmt.Errorf("record import: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// SetImportedParent puts an imported monitor in its group, or takes it out with nil.
func (s *Store) SetImportedParent(ctx context.Context, id int32, parent *int32) error {
	if _, err := s.pool.Exec(ctx, `UPDATE monitor SET parent_id = $2 WHERE id = $1`, id, parent); err != nil {
		return fmt.Errorf("set parent: %w", err)
	}
	return nil
}

// ReplaceImportedStats replaces a monitor's imported buckets in one table.
func (s *Store) ReplaceImportedStats(ctx context.Context, daily bool, monitorID int32, stats []ImportedStat) error {
	table := "imported_stat_hourly"
	if daily {
		table = "imported_stat_daily"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE monitor_id = $1`, monitorID); err != nil {
		return fmt.Errorf("clear %s: %w", table, err)
	}
	_, err = tx.CopyFrom(ctx, pgx.Identifier{table},
		[]string{"monitor_id", "bucket", "up", "down", "maintenance", "ping_sum", "ping_count", "min_ping", "max_ping"},
		pgx.CopyFromSlice(len(stats), func(i int) ([]any, error) {
			r := stats[i]
			return []any{r.MonitorID, r.Bucket, r.Up, r.Down, r.Maintenance, r.PingSum, r.PingCount, r.MinPing, r.MaxPing}, nil
		}))
	if err != nil {
		return fmt.Errorf("write %s: %w", table, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// ActivateImported resumes imported monitors that were active in the source.
func (s *Store) ActivateImported(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE monitor m SET active = true, updated_at = now()
		FROM monitor_import i WHERE i.monitor_id = m.id AND i.source_active AND NOT m.active`)
	if err != nil {
		return 0, fmt.Errorf("activate imported monitors: %w", err)
	}
	return tag.RowsAffected(), nil
}
