package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// ErrNotFound means that the monitor doesn't exist.
var ErrNotFound = errors.New("not found")

const monitorColumns = `id, name, type, active, interval_seconds, retry_interval_seconds, timeout_seconds,
	max_retries, upside_down, url, method, headers, body, body_encoding, accepted_status_codes,
	max_redirects, ignore_tls, keyword, invert_keyword, hostname, port`

// aggregateRefreshTimeout allows for rebuilding every bucket a deleted monitor touched.
const aggregateRefreshTimeout = 5 * time.Minute

func scanMonitor(row pgx.Row) (monitor.Monitor, error) {
	var m monitor.Monitor
	err := row.Scan(&m.ID, &m.Name, &m.Type, &m.Active, &m.Interval, &m.RetryInterval, &m.Timeout,
		&m.MaxRetries, &m.UpsideDown, &m.URL, &m.Method, &m.Headers, &m.Body, &m.BodyEncoding, &m.AcceptedStatusCodes,
		&m.MaxRedirects, &m.IgnoreTLS, &m.Keyword, &m.InvertKeyword, &m.Hostname, &m.Port)
	return m, err
}

// ListMonitors returns all monitors ordered by id.
func (s *Store) ListMonitors(ctx context.Context) ([]monitor.Monitor, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+monitorColumns+` FROM monitor ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list monitors: %w", err)
	}
	monitors, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (monitor.Monitor, error) { return scanMonitor(r) })
	if err != nil {
		return nil, fmt.Errorf("read monitors: %w", err)
	}
	return monitors, nil
}

// GetMonitor returns one monitor or ErrNotFound.
func (s *Store) GetMonitor(ctx context.Context, id int32) (monitor.Monitor, error) {
	m, err := scanMonitor(s.pool.QueryRow(ctx, `SELECT `+monitorColumns+` FROM monitor WHERE id = $1`, id))
	return m, monitorResult(err)
}

// CreateMonitor stores a new monitor and returns it with its id.
func (s *Store) CreateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	created, err := scanMonitor(s.pool.QueryRow(ctx, `
		INSERT INTO monitor (name, type, active, interval_seconds, retry_interval_seconds, timeout_seconds,
			max_retries, upside_down, url, method, headers, body, body_encoding, accepted_status_codes,
			max_redirects, ignore_tls, keyword, invert_keyword, hostname, port)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20)
		RETURNING `+monitorColumns,
		m.Name, m.Type, m.Active, m.Interval, m.RetryInterval, m.Timeout,
		m.MaxRetries, m.UpsideDown, m.URL, m.Method, m.Headers, m.Body, m.BodyEncoding, m.AcceptedStatusCodes,
		m.MaxRedirects, m.IgnoreTLS, m.Keyword, m.InvertKeyword, m.Hostname, m.Port))
	return created, monitorResult(err)
}

// UpdateMonitor replaces a monitor's configuration or returns ErrNotFound.
func (s *Store) UpdateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	updated, err := scanMonitor(s.pool.QueryRow(ctx, `
		UPDATE monitor SET name = $2, type = $3, active = $4, interval_seconds = $5, retry_interval_seconds = $6,
			timeout_seconds = $7, max_retries = $8, upside_down = $9, url = $10, method = $11, headers = $12,
			body = $13, body_encoding = $14, accepted_status_codes = $15, max_redirects = $16, ignore_tls = $17,
			keyword = $18, invert_keyword = $19, hostname = $20, port = $21, updated_at = now()
		WHERE id = $1
		RETURNING `+monitorColumns,
		m.ID, m.Name, m.Type, m.Active, m.Interval, m.RetryInterval, m.Timeout,
		m.MaxRetries, m.UpsideDown, m.URL, m.Method, m.Headers, m.Body, m.BodyEncoding, m.AcceptedStatusCodes,
		m.MaxRedirects, m.IgnoreTLS, m.Keyword, m.InvertKeyword, m.Hostname, m.Port))
	return updated, monitorResult(err)
}

// DeleteMonitor removes a monitor and its beats, then rebuilds the aggregates that
// contained them. The refresh policies only cover recent buckets, so older ones
// would otherwise keep reporting the deleted monitor.
func (s *Store) DeleteMonitor(ctx context.Context, id int32) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	tag, err := tx.Exec(ctx, `DELETE FROM monitor WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete monitor: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM heartbeat WHERE monitor_id = $1`, id); err != nil {
		return fmt.Errorf("delete beats: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete: %w", err)
	}

	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aggregateRefreshTimeout)
	defer cancel()
	for _, view := range []string{"stat_hourly", "stat_daily"} {
		if _, err := s.pool.Exec(refreshCtx, `CALL refresh_continuous_aggregate('`+view+`', NULL, NULL)`); err != nil {
			return fmt.Errorf("refresh %s: %w", view, err)
		}
	}
	return nil
}

// LatestStates returns each monitor's last beat state.
func (s *Store) LatestStates(ctx context.Context) (map[int32]monitor.Previous, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, l.status, l.retries
		FROM monitor m
		CROSS JOIN LATERAL (
			SELECT status, retries FROM heartbeat h
			WHERE h.monitor_id = m.id ORDER BY h.time DESC LIMIT 1
		) l`)
	if err != nil {
		return nil, fmt.Errorf("query latest states: %w", err)
	}
	defer rows.Close()
	states := make(map[int32]monitor.Previous)
	for rows.Next() {
		var id int32
		var p monitor.Previous
		if err := rows.Scan(&id, &p.Status, &p.Retries); err != nil {
			return nil, fmt.Errorf("read latest state: %w", err)
		}
		states[id] = p
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read latest states: %w", err)
	}
	return states, nil
}

func monitorResult(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("monitor query: %w", err)
	}
	return nil
}
