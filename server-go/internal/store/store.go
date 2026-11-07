// Package store persists heartbeats in TimescaleDB and answers analytics queries.
package store

import (
	"context"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Beat is one monitor check result. Status uses the Uptime Kuma values:
// 0 down, 1 up, 2 pending, 3 maintenance.
type Beat struct {
	MonitorID int32     `json:"monitorId"`
	Time      time.Time `json:"time"`
	Status    int16     `json:"status"`
	Ping      *float64  `json:"ping"`
	Msg       string    `json:"msg"`
	Important bool      `json:"important"`
	Duration  int32     `json:"duration"`
	Retries   int32     `json:"retries"`
	DownCount int32     `json:"downCount"`
	// TraceID is set on failed beats, and links to the check's trace.
	TraceID string `json:"traceId,omitempty"`
	// Response is the body an HTTP monitor saved for notification templates. It isn't stored.
	Response *string `json:"-"`
}

// ChartPoint is one aggregated chart bucket.
type ChartPoint struct {
	Time        time.Time `json:"time"`
	Up          int64     `json:"up"`
	Down        int64     `json:"down"`
	Maintenance int64     `json:"maintenance"`
	AvgPing     *float64  `json:"avgPing"`
	MinPing     *float64  `json:"minPing"`
	MaxPing     *float64  `json:"maxPing"`
}

// MonitorStats holds the dashboard numbers of one monitor. A nil uptime means
// that the window has no beats.
type MonitorStats struct {
	MonitorID int32    `json:"monitorId"`
	Uptime24h *float64 `json:"uptime24h"`
	Uptime30d *float64 `json:"uptime30d"`
	Uptime1y  *float64 `json:"uptime1y"`
	AvgPing   *float64 `json:"avgPing24h"`
}

// Store is a TimescaleDB-backed heartbeat store.
type Store struct {
	pool *pgxpool.Pool
}

// Open connects to the database. Call Migrate before serving traffic.
func Open(ctx context.Context, databaseURL string) (*Store, error) {
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	// Query parameters stay off the spans, which only get the SQL text.
	config.ConnConfig.Tracer = otelpgx.NewTracer()
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return nil, fmt.Errorf("create pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("connect: %w", err)
	}
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("record pool stats: %w", err)
	}
	return &Store{pool: pool}, nil
}

// Close releases all connections.
func (s *Store) Close() { s.pool.Close() }

// Ping checks database connectivity.
func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

// InsertBeats stores beats in one statement and returns those that were new.
// Replaying a batch is safe: rows are unique per (monitor, time).
func (s *Store) InsertBeats(ctx context.Context, beats []Beat) ([]Beat, error) {
	if len(beats) == 0 {
		return nil, nil
	}
	n := len(beats)
	ids, times, statuses := make([]int32, n), make([]time.Time, n), make([]int16, n)
	pings, msgs, important := make([]*float64, n), make([]string, n), make([]bool, n)
	durations, retries, traceIDs, downCounts := make([]int32, n), make([]int32, n), make([]string, n), make([]int32, n)
	for i, b := range beats {
		ids[i], times[i], statuses[i] = b.MonitorID, b.Time, b.Status
		pings[i], msgs[i], important[i] = b.Ping, b.Msg, b.Important
		durations[i], retries[i], traceIDs[i], downCounts[i] = b.Duration, b.Retries, b.TraceID, b.DownCount
	}

	rows, err := s.pool.Query(ctx, `
		INSERT INTO heartbeat (monitor_id, time, status, ping, msg, important, duration, retries, trace_id, down_count)
		SELECT b.m, b.t, b.s, b.p, b.msg, b.i, b.d, b.r, NULLIF(b.tr, ''), b.dc
		FROM unnest($1::int[], $2::timestamptz[], $3::smallint[], $4::float8[],
		            $5::text[], $6::bool[], $7::int[], $8::int[], $9::text[], $10::int[])
		     AS b(m, t, s, p, msg, i, d, r, tr, dc)
		ON CONFLICT (monitor_id, time) DO NOTHING
		RETURNING monitor_id, time, status, ping, msg, important, duration, retries, COALESCE(trace_id, ''), down_count`,
		ids, times, statuses, pings, msgs, important, durations, retries, traceIDs, downCounts)
	if err != nil {
		return nil, fmt.Errorf("insert beats: %w", err)
	}
	return collectBeats(rows)
}

// LatestBeats returns the newest beats of a monitor, newest first.
func (s *Store) LatestBeats(ctx context.Context, monitorID int32, limit int) ([]Beat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, time, status, ping, msg, important, duration, retries, COALESCE(trace_id, ''), down_count
		FROM heartbeat WHERE monitor_id = $1 ORDER BY time DESC LIMIT $2`, monitorID, limit)
	if err != nil {
		return nil, fmt.Errorf("query beats: %w", err)
	}
	return collectBeats(rows)
}

func collectBeats(rows pgx.Rows) ([]Beat, error) {
	beats, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Beat, error) {
		var b Beat
		err := r.Scan(&b.MonitorID, &b.Time, &b.Status, &b.Ping, &b.Msg, &b.Important, &b.Duration, &b.Retries, &b.TraceID, &b.DownCount)
		return b, err
	})
	if err != nil {
		return nil, fmt.Errorf("read beats: %w", err)
	}
	return beats, nil
}

// Chart returns buckets for the last `hours` hours, oldest first. Resolution
// follows the window: 1 minute up to 6h, 5 minutes up to 24h, hourly up to 30
// days, then daily.
func (s *Store) Chart(ctx context.Context, monitorID int32, hours int) ([]ChartPoint, error) {
	const aggregated = `
		SELECT bucket, up::bigint, down::bigint, maintenance::bigint,
		       ping_sum / NULLIF(ping_count, 0), min_ping, max_ping
		FROM %s WHERE monitor_id = $1 AND bucket >= now() - make_interval(hours => $2)
		ORDER BY bucket`
	const fromRaw = `
		SELECT time_bucket(INTERVAL '%s', time) AS bucket,
		       count(*) FILTER (WHERE status = 1),
		       count(*) FILTER (WHERE status IN (0, 2)),
		       count(*) FILTER (WHERE status = 3),
		       avg(ping) FILTER (WHERE status = 1),
		       min(ping) FILTER (WHERE status = 1),
		       max(ping) FILTER (WHERE status = 1)
		FROM heartbeat WHERE monitor_id = $1 AND time >= now() - make_interval(hours => $2)
		GROUP BY bucket ORDER BY bucket`

	var query string
	switch {
	case hours <= 6:
		query = fmt.Sprintf(fromRaw, "1 minute")
	case hours <= 24:
		query = fmt.Sprintf(fromRaw, "5 minutes")
	case hours <= 24*30:
		query = fmt.Sprintf(aggregated, "uptime_hourly")
	default:
		query = fmt.Sprintf(aggregated, "uptime_daily")
	}

	rows, err := s.pool.Query(ctx, query, monitorID, hours)
	if err != nil {
		return nil, fmt.Errorf("query chart: %w", err)
	}
	points, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (ChartPoint, error) {
		var p ChartPoint
		err := r.Scan(&p.Time, &p.Up, &p.Down, &p.Maintenance, &p.AvgPing, &p.MinPing, &p.MaxPing)
		return p, err
	})
	if err != nil {
		return nil, fmt.Errorf("read chart: %w", err)
	}
	return points, nil
}

// Percentiles computes ping percentiles of successful beats from raw data. The
// result has one entry per fraction, each nil when the window has no pings.
func (s *Store) Percentiles(ctx context.Context, monitorID int32, hours int, fractions []float64) ([]*float64, error) {
	var values []*float64
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont($3::float8[]) WITHIN GROUP (ORDER BY ping)
		FROM heartbeat
		WHERE monitor_id = $1 AND status = 1 AND ping IS NOT NULL
		  AND time >= now() - make_interval(hours => $2)`,
		monitorID, hours, fractions).Scan(&values)
	if err != nil {
		return nil, fmt.Errorf("query percentiles: %w", err)
	}
	if values == nil {
		values = make([]*float64, len(fractions))
	}
	return values, nil
}

// Stats returns dashboard numbers for every monitor with data. It reads only
// the continuous aggregates, so its cost does not grow with raw history.
func (s *Store) Stats(ctx context.Context) ([]MonitorStats, error) {
	rows, err := s.pool.Query(ctx, `
		WITH hourly AS (
			SELECT monitor_id, sum(up)::bigint AS up, sum(down)::bigint AS down,
			       sum(ping_sum) AS ping_sum, sum(ping_count)::bigint AS ping_count
			FROM uptime_hourly WHERE bucket >= now() - INTERVAL '24 hours'
			GROUP BY monitor_id
		), daily AS (
			SELECT monitor_id,
			       (sum(up) FILTER (WHERE bucket >= now() - INTERVAL '30 days'))::bigint AS up30,
			       (sum(down) FILTER (WHERE bucket >= now() - INTERVAL '30 days'))::bigint AS down30,
			       sum(up)::bigint AS up1y, sum(down)::bigint AS down1y
			FROM uptime_daily WHERE bucket >= now() - INTERVAL '365 days'
			GROUP BY monitor_id
		)
		SELECT COALESCE(h.monitor_id, d.monitor_id),
		       COALESCE(h.up, 0), COALESCE(h.down, 0), h.ping_sum, COALESCE(h.ping_count, 0),
		       COALESCE(d.up30, 0), COALESCE(d.down30, 0), COALESCE(d.up1y, 0), COALESCE(d.down1y, 0)
		FROM hourly h FULL JOIN daily d ON h.monitor_id = d.monitor_id
		ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("query stats: %w", err)
	}
	stats, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (MonitorStats, error) {
		var m MonitorStats
		var up, down, pingCount, up30, down30, up1y, down1y int64
		var pingSum *float64
		if err := r.Scan(&m.MonitorID, &up, &down, &pingSum, &pingCount, &up30, &down30, &up1y, &down1y); err != nil {
			return m, err
		}
		m.Uptime24h, m.Uptime30d, m.Uptime1y = ratio(up, down), ratio(up30, down30), ratio(up1y, down1y)
		if pingSum != nil && pingCount > 0 {
			avg := *pingSum / float64(pingCount)
			m.AvgPing = &avg
		}
		return m, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read stats: %w", err)
	}
	return stats, nil
}

func ratio(up, down int64) *float64 {
	if up+down == 0 {
		return nil
	}
	v := float64(up) / float64(up+down)
	return &v
}
