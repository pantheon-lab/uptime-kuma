package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
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
	{
		`CREATE TABLE IF NOT EXISTS monitor (
			id serial PRIMARY KEY,
			name text NOT NULL,
			type text NOT NULL,
			active boolean NOT NULL,
			interval_seconds integer NOT NULL,
			retry_interval_seconds integer NOT NULL,
			timeout_seconds double precision NOT NULL,
			max_retries integer NOT NULL,
			upside_down boolean NOT NULL,
			url text NOT NULL,
			method text NOT NULL,
			headers jsonb NOT NULL,
			body text NOT NULL,
			body_encoding text NOT NULL,
			accepted_status_codes text[] NOT NULL,
			max_redirects integer NOT NULL,
			ignore_tls boolean NOT NULL,
			keyword text NOT NULL,
			invert_keyword boolean NOT NULL,
			hostname text NOT NULL,
			port integer NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
	},
	{
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS parent_id integer REFERENCES monitor (id) ON DELETE SET NULL`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS propagate_trace boolean NOT NULL DEFAULT false`,
		`CREATE INDEX IF NOT EXISTS monitor_parent_idx ON monitor (parent_id)`,
		`ALTER TABLE heartbeat ADD COLUMN IF NOT EXISTS trace_id text`,
		// Every group with all monitors below it, through nested groups. Groups themselves
		// aren't members, so their status beats don't count towards uptime twice. UNION
		// removes duplicates, which also stops the recursion if a cycle ever appears.
		`CREATE OR REPLACE VIEW monitor_group_member AS
			WITH RECURSIVE tree (group_id, monitor_id) AS (
				SELECT parent_id, id FROM monitor WHERE parent_id IS NOT NULL
				UNION
				SELECT tree.group_id, child.id
				FROM tree JOIN monitor child ON child.parent_id = tree.monitor_id
			)
			SELECT tree.group_id, grp.name AS group_name, tree.monitor_id, member.name AS monitor_name
			FROM tree
			JOIN monitor grp ON grp.id = tree.group_id
			JOIN monitor member ON member.id = tree.monitor_id
			WHERE member.type <> 'group'`,
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

// EnsureReadOnlyRole creates or updates a login role for dashboards. It can read beats
// and aggregates, but of monitors only the columns below: URLs, headers and bodies can
// contain credentials.
func (s *Store) EnsureReadOnlyRole(ctx context.Context, role, password string) error {
	quoted := pgx.Identifier{role}.Sanitize()
	var literal string
	if err := s.pool.QueryRow(ctx, `SELECT quote_literal($1)`, password).Scan(&literal); err != nil {
		return fmt.Errorf("quote password: %w", err)
	}
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)`, role).Scan(&exists); err != nil {
		return fmt.Errorf("check role: %w", err)
	}
	verb := "CREATE"
	if exists {
		verb = "ALTER"
	}
	statements := []string{
		verb + ` ROLE ` + quoted + ` LOGIN PASSWORD ` + literal,
		`GRANT USAGE ON SCHEMA public TO ` + quoted,
		`GRANT SELECT ON heartbeat, stat_hourly, stat_daily, monitor_group_member TO ` + quoted,
		`GRANT SELECT (id, name, type, active, parent_id, interval_seconds) ON monitor TO ` + quoted,
	}
	for _, statement := range statements {
		if _, err := s.pool.Exec(ctx, statement); err != nil {
			return fmt.Errorf("set up role %s: %w", role, err)
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
