package store

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

var (
	// ErrNotFound means that the monitor doesn't exist.
	ErrNotFound = errors.New("not found")
	// ErrInvalidParent means that a monitor can't go in the requested group.
	ErrInvalidParent = errors.New("invalid parent")
	// ErrInvalidNotification means that a monitor can't use the requested notification.
	ErrInvalidNotification = errors.New("invalid notification")
	// ErrPushTokenTaken means that another monitor has the requested push token.
	ErrPushTokenTaken = errors.New("another monitor already uses this push token")
)

const monitorColumns = `id, name, type, active, interval_seconds, retry_interval_seconds, timeout_seconds,
	max_retries, upside_down, url, method, headers, body, body_encoding, accepted_status_codes,
	max_redirects, ignore_tls, keyword, invert_keyword, hostname, port, parent_id, propagate_trace, resend_interval, expiry_notification,
	push_token, ping_count, ping_per_request_timeout, packet_size, dns_resolve_type, dns_resolve_server, dns_last_result`

// aggregateRefreshTimeout allows for rebuilding every bucket a deleted monitor touched.
const (
	aggregateRefreshTimeout = 5 * time.Minute
	refreshRetryMin         = 100 * time.Millisecond
	refreshRetryMax         = 2 * time.Second
	// pgLockNotAvailable is what TimescaleDB returns for a concurrent refresh.
	pgLockNotAvailable = "55P03"
	pgUniqueViolation  = "23505"
)

func scanMonitor(row pgx.Row) (monitor.Monitor, error) {
	var m monitor.Monitor
	err := row.Scan(&m.ID, &m.Name, &m.Type, &m.Active, &m.Interval, &m.RetryInterval, &m.Timeout,
		&m.MaxRetries, &m.UpsideDown, &m.URL, &m.Method, &m.Headers, &m.Body, &m.BodyEncoding, &m.AcceptedStatusCodes,
		&m.MaxRedirects, &m.IgnoreTLS, &m.Keyword, &m.InvertKeyword, &m.Hostname, &m.Port, &m.ParentID, &m.PropagateTrace, &m.ResendInterval, &m.ExpiryNotification,
		&m.PushToken, &m.PingCount, &m.PingPerRequestTimeout, &m.PacketSize, &m.DNSResolveType, &m.DNSResolveServer, &m.DNSLastResult)
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

// MonitorFilter narrows a monitor listing. Zero values don't filter.
type MonitorFilter struct {
	Type     string
	Active   *bool
	ParentID *int32
	Limit    int
	Offset   int
}

// FindMonitors returns one page of monitors ordered by id, and how many match in total.
func (s *Store) FindMonitors(ctx context.Context, f MonitorFilter) ([]monitor.Monitor, int, error) {
	const where = `($1 = '' OR type = $1) AND ($2::boolean IS NULL OR active = $2) AND ($3::int IS NULL OR parent_id = $3)`
	var total int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM monitor WHERE `+where, f.Type, f.Active, f.ParentID).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count monitors: %w", err)
	}
	rows, err := s.pool.Query(ctx, `SELECT `+monitorColumns+` FROM monitor WHERE `+where+` ORDER BY id LIMIT $4 OFFSET $5`,
		f.Type, f.Active, f.ParentID, f.Limit, f.Offset)
	if err != nil {
		return nil, 0, fmt.Errorf("find monitors: %w", err)
	}
	monitors, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (monitor.Monitor, error) { return scanMonitor(r) })
	if err != nil {
		return nil, 0, fmt.Errorf("read monitors: %w", err)
	}
	return monitors, total, nil
}

// SetMonitorActive pauses or resumes a monitor, or returns ErrNotFound.
func (s *Store) SetMonitorActive(ctx context.Context, id int32, active bool) (monitor.Monitor, error) {
	m, err := scanMonitor(s.pool.QueryRow(ctx,
		`UPDATE monitor SET active = $2, updated_at = now() WHERE id = $1 RETURNING `+monitorColumns, id, active))
	return m, monitorResult(err)
}

// GetMonitor returns one monitor or ErrNotFound.
func (s *Store) GetMonitor(ctx context.Context, id int32) (monitor.Monitor, error) {
	m, err := scanMonitor(s.pool.QueryRow(ctx, `SELECT `+monitorColumns+` FROM monitor WHERE id = $1`, id))
	return m, monitorResult(err)
}

// pushTokenIndex keeps push tokens unique, because a token decides which monitor a push is for.
const pushTokenIndex = "monitor_push_token_idx"

// groupTreeLock serializes changes to the group tree, so that two concurrent moves can't
// create a cycle that each alone wouldn't.
const groupTreeLock = 7236002

// validateParent checks that a monitor can go in its group. It runs inside the
// transaction that holds groupTreeLock.
func validateParent(ctx context.Context, tx pgx.Tx, m monitor.Monitor) error {
	if m.ParentID == nil {
		return nil
	}
	var parentType string
	err := tx.QueryRow(ctx, `SELECT type FROM monitor WHERE id = $1`, *m.ParentID).Scan(&parentType)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && parentType != monitor.TypeGroup) {
		return fmt.Errorf("%w: parentId must be an existing group", ErrInvalidParent)
	}
	if err != nil {
		return fmt.Errorf("read parent: %w", err)
	}
	if m.ID == 0 {
		return nil
	}
	var cycle bool
	err = tx.QueryRow(ctx, `
		WITH RECURSIVE ancestors (id) AS (
			SELECT $1::int
			UNION
			SELECT monitor.parent_id FROM monitor JOIN ancestors ON monitor.id = ancestors.id
			WHERE monitor.parent_id IS NOT NULL
		)
		SELECT EXISTS (SELECT 1 FROM ancestors WHERE id = $2)`, *m.ParentID, m.ID).Scan(&cycle)
	if err != nil {
		return fmt.Errorf("check for a cycle: %w", err)
	}
	if cycle {
		return fmt.Errorf("%w: a group can't be inside itself", ErrInvalidParent)
	}
	return nil
}

// inTreeTransaction runs fn in a transaction that holds groupTreeLock.
func (s *Store) inTreeTransaction(ctx context.Context, fn func(pgx.Tx) (monitor.Monitor, error)) (monitor.Monitor, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return monitor.Monitor{}, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, int64(groupTreeLock)); err != nil {
		return monitor.Monitor{}, fmt.Errorf("lock group tree: %w", err)
	}
	result, err := fn(tx)
	if err != nil {
		return monitor.Monitor{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return monitor.Monitor{}, fmt.Errorf("commit: %w", err)
	}
	return result, nil
}

// CreateMonitor stores a new monitor and returns it with its id.
func (s *Store) CreateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	m.ID = 0
	m.PushToken = cmp.Or(m.PushToken, generatedPushToken(m))
	return s.inTreeTransaction(ctx, func(tx pgx.Tx) (monitor.Monitor, error) {
		if err := validateParent(ctx, tx, m); err != nil {
			return m, err
		}
		created, err := scanMonitor(tx.QueryRow(ctx, `
			INSERT INTO monitor (name, type, active, interval_seconds, retry_interval_seconds, timeout_seconds,
				max_retries, upside_down, url, method, headers, body, body_encoding, accepted_status_codes,
				max_redirects, ignore_tls, keyword, invert_keyword, hostname, port, parent_id, propagate_trace, resend_interval, expiry_notification,
				push_token, ping_count, ping_per_request_timeout, packet_size, dns_resolve_type, dns_resolve_server)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23, $24,
				$25, $26, $27, $28, $29, $30)
			RETURNING `+monitorColumns,
			m.Name, m.Type, m.Active, m.Interval, m.RetryInterval, m.Timeout,
			m.MaxRetries, m.UpsideDown, m.URL, m.Method, m.Headers, m.Body, m.BodyEncoding, m.AcceptedStatusCodes,
			m.MaxRedirects, m.IgnoreTLS, m.Keyword, m.InvertKeyword, m.Hostname, m.Port, m.ParentID, m.PropagateTrace, m.ResendInterval, m.ExpiryNotification,
			m.PushToken, m.PingCount, m.PingPerRequestTimeout, m.PacketSize, m.DNSResolveType, m.DNSResolveServer))
		return created, monitorResult(err)
	})
}

// UpdateMonitor replaces a monitor's configuration or returns ErrNotFound. A group that
// becomes another type releases its children, the same as the Node.js backend. An empty push
// token keeps the current one, because senders that have it would break otherwise.
func (s *Store) UpdateMonitor(ctx context.Context, m monitor.Monitor) (monitor.Monitor, error) {
	return s.inTreeTransaction(ctx, func(tx pgx.Tx) (monitor.Monitor, error) {
		if err := validateParent(ctx, tx, m); err != nil {
			return m, err
		}
		updated, err := scanMonitor(tx.QueryRow(ctx, `
			UPDATE monitor SET name = $2, type = $3, active = $4, interval_seconds = $5, retry_interval_seconds = $6,
				timeout_seconds = $7, max_retries = $8, upside_down = $9, url = $10, method = $11, headers = $12,
				body = $13, body_encoding = $14, accepted_status_codes = $15, max_redirects = $16, ignore_tls = $17,
				keyword = $18, invert_keyword = $19, hostname = $20, port = $21, parent_id = $22, propagate_trace = $23,
				resend_interval = $24, expiry_notification = $25,
				push_token = COALESCE(NULLIF($26::text, ''), NULLIF(push_token, ''), $27::text), ping_count = $28,
				ping_per_request_timeout = $29, packet_size = $30, dns_resolve_type = $31, dns_resolve_server = $32,
				updated_at = now()
			WHERE id = $1
			RETURNING `+monitorColumns,
			m.ID, m.Name, m.Type, m.Active, m.Interval, m.RetryInterval, m.Timeout,
			m.MaxRetries, m.UpsideDown, m.URL, m.Method, m.Headers, m.Body, m.BodyEncoding, m.AcceptedStatusCodes,
			m.MaxRedirects, m.IgnoreTLS, m.Keyword, m.InvertKeyword, m.Hostname, m.Port, m.ParentID, m.PropagateTrace, m.ResendInterval, m.ExpiryNotification,
			m.PushToken, generatedPushToken(m), m.PingCount, m.PingPerRequestTimeout, m.PacketSize, m.DNSResolveType, m.DNSResolveServer))
		if err := monitorResult(err); err != nil {
			return m, err
		}
		if updated.Type != monitor.TypeGroup {
			if _, err := tx.Exec(ctx, `UPDATE monitor SET parent_id = NULL WHERE parent_id = $1`, updated.ID); err != nil {
				return m, fmt.Errorf("release children: %w", err)
			}
		}
		return updated, nil
	})
}

// generatedPushToken is a new token for a push monitor, and empty for other types.
func generatedPushToken(m monitor.Monitor) string {
	if m.Type != monitor.TypePush {
		return ""
	}
	return monitor.NewPushToken()
}

// SetDNSLastResult implements check.DNSResultWriter. An unchanged result writes nothing.
func (s *Store) SetDNSLastResult(ctx context.Context, id int32, result string) error {
	_, err := s.pool.Exec(ctx, `UPDATE monitor SET dns_last_result = $2 WHERE id = $1 AND dns_last_result <> $2`, id, result)
	if err != nil {
		return fmt.Errorf("set DNS result: %w", err)
	}
	return nil
}

// GroupChildren implements check.GroupReader.
func (s *Store) GroupChildren(ctx context.Context, groupID int32) ([]check.Child, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.name, m.active, l.status
		FROM monitor m
		LEFT JOIN LATERAL (
			SELECT status FROM heartbeat h WHERE h.monitor_id = m.id ORDER BY h.time DESC LIMIT 1
		) l ON true
		WHERE m.parent_id = $1
		ORDER BY m.id`, groupID)
	if err != nil {
		return nil, fmt.Errorf("query group children: %w", err)
	}
	children, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (check.Child, error) {
		var c check.Child
		err := r.Scan(&c.Name, &c.Active, &c.Status)
		return c, err
	})
	if err != nil {
		return nil, fmt.Errorf("read group children: %w", err)
	}
	return children, nil
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
	return s.RefreshAggregates(refreshCtx)
}

// RefreshAggregates rebuilds every finished aggregate bucket that has changed. It leaves out
// the current bucket: a materialized bucket stops following new beats until the next scheduled
// refresh that covers it, so an unfinished one would hide them.
func (s *Store) RefreshAggregates(ctx context.Context) error {
	for _, view := range []struct{ name, unit string }{{"stat_hourly", "hour"}, {"stat_daily", "day"}} {
		err := retryConcurrentRefresh(ctx, func() error {
			_, err := s.pool.Exec(ctx,
				`CALL refresh_continuous_aggregate('`+view.name+`', NULL, date_trunc('`+view.unit+`', now(), 'UTC'))`)
			return err
		})
		if err != nil {
			return fmt.Errorf("refresh %s: %w", view.name, err)
		}
	}
	return nil
}

// retryConcurrentRefresh retries while another refresh of the same aggregate is running,
// such as the scheduled policy, or the refresh from deleting another monitor. TimescaleDB
// rejects overlapping refreshes rather than waiting for them.
func retryConcurrentRefresh(ctx context.Context, refresh func() error) error {
	wait := refreshRetryMin
	for {
		err := refresh()
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != pgLockNotAvailable {
			return err
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(wait):
		}
		wait = min(wait*2, refreshRetryMax)
	}
}

// LatestStates returns each monitor's last beat state.
func (s *Store) LatestStates(ctx context.Context) (map[int32]monitor.Previous, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT m.id, l.status, l.retries, l.down_count
		FROM monitor m
		CROSS JOIN LATERAL (
			SELECT status, retries, down_count FROM heartbeat h
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
		if err := rows.Scan(&id, &p.Status, &p.Retries, &p.DownCount); err != nil {
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
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == pushTokenIndex {
		return ErrPushTokenTaken
	}
	if err != nil {
		return fmt.Errorf("monitor query: %w", err)
	}
	return nil
}
