package store

import (
	"context"
	"fmt"
)

// migrateLockID serializes migrations between instances.
const migrateLockID = 7236001

// migrations run in order, one statement at a time and outside a transaction,
// because TimescaleDB rejects creating continuous aggregates inside one. Every
// statement is idempotent so that a failed migration can simply run again.
var migrations = [][]string{
	{
		`CREATE EXTENSION IF NOT EXISTS timescaledb`,
		`CREATE TABLE IF NOT EXISTS heartbeat (
			monitor_id integer NOT NULL,
			time timestamptz NOT NULL,
			status smallint NOT NULL,
			ping double precision,
			msg text NOT NULL DEFAULT '',
			important boolean NOT NULL DEFAULT false,
			duration integer NOT NULL DEFAULT 0,
			retries integer NOT NULL DEFAULT 0,
			PRIMARY KEY (monitor_id, time)
		)`,
		`SELECT create_hypertable('heartbeat', by_range('time', INTERVAL '1 day'),
			create_default_indexes => false, if_not_exists => true)`,
		`CREATE INDEX IF NOT EXISTS heartbeat_time_idx ON heartbeat (time DESC)`,
		`ALTER TABLE heartbeat SET (timescaledb.compress,
			timescaledb.compress_segmentby = 'monitor_id',
			timescaledb.compress_orderby = 'time DESC')`,
		`SELECT add_compression_policy('heartbeat', compress_after => INTERVAL '7 days', if_not_exists => true)`,
		`CREATE MATERIALIZED VIEW IF NOT EXISTS stat_hourly
			WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
			SELECT monitor_id, time_bucket(INTERVAL '1 hour', time) AS bucket,
			       count(*) FILTER (WHERE status = 1) AS up,
			       count(*) FILTER (WHERE status IN (0, 2)) AS down,
			       count(*) FILTER (WHERE status = 3) AS maintenance,
			       COALESCE(sum(ping) FILTER (WHERE status = 1), 0) AS ping_sum,
			       count(ping) FILTER (WHERE status = 1) AS ping_count,
			       min(ping) FILTER (WHERE status = 1) AS min_ping,
			       max(ping) FILTER (WHERE status = 1) AS max_ping
			FROM heartbeat GROUP BY monitor_id, bucket
			WITH NO DATA`,
		`SELECT add_continuous_aggregate_policy('stat_hourly',
			start_offset => INTERVAL '6 hours', end_offset => INTERVAL '1 hour',
			schedule_interval => INTERVAL '5 minutes', if_not_exists => true)`,
		// Built on stat_hourly: a daily view over raw beats has to rescan all of
		// today's rows on every query, which dominated dashboard time in benchmarks.
		`CREATE MATERIALIZED VIEW IF NOT EXISTS stat_daily
			WITH (timescaledb.continuous, timescaledb.materialized_only = false) AS
			SELECT monitor_id, time_bucket(INTERVAL '1 day', bucket) AS bucket,
			       sum(up) AS up, sum(down) AS down, sum(maintenance) AS maintenance,
			       sum(ping_sum) AS ping_sum, sum(ping_count) AS ping_count,
			       min(min_ping) AS min_ping, max(max_ping) AS max_ping
			FROM stat_hourly GROUP BY monitor_id, time_bucket(INTERVAL '1 day', bucket)
			WITH NO DATA`,
		`SELECT add_continuous_aggregate_policy('stat_daily',
			start_offset => INTERVAL '7 days', end_offset => INTERVAL '1 day',
			schedule_interval => INTERVAL '1 hour', if_not_exists => true)`,
	},
}

// Migrate brings the schema up to date.
func (s *Store) Migrate(ctx context.Context) error {
	conn, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1)`, int64(migrateLockID)); err != nil {
		return fmt.Errorf("lock migrations: %w", err)
	}
	defer func() {
		// Uses a fresh context so that the lock is released even after cancellation.
		_, _ = conn.Exec(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, int64(migrateLockID))
	}()

	if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY,
		applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}

	var current int
	if err := conn.QueryRow(ctx, `SELECT COALESCE(max(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	for i := current; i < len(migrations); i++ {
		for _, statement := range migrations[i] {
			if _, err := conn.Exec(ctx, statement); err != nil {
				return fmt.Errorf("migration %d: %w\nstatement: %s", i+1, err, statement)
			}
		}
		if _, err := conn.Exec(ctx, `INSERT INTO schema_migrations (version) VALUES ($1)`, i+1); err != nil {
			return fmt.Errorf("record migration %d: %w", i+1, err)
		}
	}
	return nil
}

// SetRetention deletes raw beats older than days. Zero keeps them forever.
// Continuous aggregates keep their history regardless.
func (s *Store) SetRetention(ctx context.Context, days int) error {
	if days < 0 {
		return fmt.Errorf("retention must not be negative: %d", days)
	}
	if _, err := s.pool.Exec(ctx, `SELECT remove_retention_policy('heartbeat', if_exists => true)`); err != nil {
		return fmt.Errorf("remove retention policy: %w", err)
	}
	if days == 0 {
		return nil
	}
	if _, err := s.pool.Exec(ctx, `SELECT add_retention_policy('heartbeat', drop_after => make_interval(days => $1::int))`, days); err != nil {
		return fmt.Errorf("add retention policy: %w", err)
	}
	return nil
}
