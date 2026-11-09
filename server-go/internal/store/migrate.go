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
	{
		// Only a hash is stored. Revoked keys stay, as a record of what existed.
		`CREATE TABLE IF NOT EXISTS api_key (
			id bigserial PRIMARY KEY,
			name text NOT NULL,
			key_prefix text NOT NULL,
			key_hash bytea NOT NULL UNIQUE,
			scopes text[] NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			expires_at timestamptz,
			revoked_at timestamptz,
			last_used_at timestamptz
		)`,
	},
	{
		// source_id is the monitor's id in the source database. Imported uptime applies before
		// cutover, and this service's own beats after it, so nothing counts twice.
		`CREATE TABLE IF NOT EXISTS monitor_import (
			source_id integer PRIMARY KEY,
			monitor_id integer NOT NULL UNIQUE REFERENCES monitor (id) ON DELETE CASCADE,
			source_active boolean NOT NULL,
			cutover timestamptz NOT NULL,
			imported_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS imported_stat_hourly (
			monitor_id integer NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
			bucket timestamptz NOT NULL,
			up bigint NOT NULL,
			down bigint NOT NULL,
			maintenance bigint NOT NULL,
			ping_sum double precision NOT NULL,
			ping_count bigint NOT NULL,
			min_ping double precision,
			max_ping double precision,
			PRIMARY KEY (monitor_id, bucket)
		)`,
		`CREATE TABLE IF NOT EXISTS imported_stat_daily (LIKE imported_stat_hourly INCLUDING ALL)`,
		`ALTER TABLE imported_stat_daily DROP CONSTRAINT IF EXISTS imported_stat_daily_monitor_id_fkey`,
		`ALTER TABLE imported_stat_daily ADD CONSTRAINT imported_stat_daily_monitor_id_fkey
			FOREIGN KEY (monitor_id) REFERENCES monitor (id) ON DELETE CASCADE`,
		`CREATE OR REPLACE VIEW uptime_hourly AS
			SELECT s.monitor_id, s.bucket, s.up::bigint AS up, s.down::bigint AS down, s.maintenance::bigint AS maintenance,
			       s.ping_sum, s.ping_count::bigint AS ping_count, s.min_ping, s.max_ping
			FROM stat_hourly s LEFT JOIN monitor_import i USING (monitor_id)
			WHERE i.cutover IS NULL OR s.bucket >= i.cutover
			UNION ALL
			SELECT monitor_id, bucket, up, down, maintenance, ping_sum, ping_count, min_ping, max_ping
			FROM imported_stat_hourly`,
		`CREATE OR REPLACE VIEW uptime_daily AS
			SELECT s.monitor_id, s.bucket, s.up::bigint AS up, s.down::bigint AS down, s.maintenance::bigint AS maintenance,
			       s.ping_sum, s.ping_count::bigint AS ping_count, s.min_ping, s.max_ping
			FROM stat_daily s LEFT JOIN monitor_import i USING (monitor_id)
			WHERE i.cutover IS NULL OR s.bucket >= date_trunc('day', i.cutover, 'UTC')
			UNION ALL
			SELECT monitor_id, bucket, up, down, maintenance, ping_sum, ping_count, min_ping, max_ping
			FROM imported_stat_daily`,
	},
	{
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS resend_interval integer NOT NULL DEFAULT 0`,
		`ALTER TABLE heartbeat ADD COLUMN IF NOT EXISTS down_count integer NOT NULL DEFAULT 0`,
		`CREATE TABLE IF NOT EXISTS notification (
			id serial PRIMARY KEY,
			name text NOT NULL,
			type text NOT NULL,
			config jsonb NOT NULL,
			active boolean NOT NULL DEFAULT true,
			is_default boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS monitor_notification (
			monitor_id integer NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
			notification_id integer NOT NULL REFERENCES notification (id) ON DELETE CASCADE,
			PRIMARY KEY (monitor_id, notification_id)
		)`,
		`CREATE TABLE IF NOT EXISTS notification_import (
			source_id integer PRIMARY KEY,
			notification_id integer NOT NULL UNIQUE REFERENCES notification (id) ON DELETE CASCADE,
			source_active boolean NOT NULL,
			imported_at timestamptz NOT NULL DEFAULT now()
		)`,
	},
	{
		// Web UI login. Passwords and session tokens are stored only as hashes. The Grafana
		// role gets no access to these tables.
		`CREATE TABLE IF NOT EXISTS app_user (
			id serial PRIMARY KEY,
			username text NOT NULL UNIQUE,
			password_hash text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS app_session (
			token_hash bytea PRIMARY KEY,
			user_id integer NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
			remember boolean NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now(),
			expires_at timestamptz NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS app_session_user_idx ON app_session (user_id)`,
		`CREATE INDEX IF NOT EXISTS app_session_expires_idx ON app_session (expires_at)`,
		// Settings the web UI saves. The backend reads none of them yet.
		`CREATE TABLE IF NOT EXISTS app_setting (
			key text PRIMARY KEY,
			value jsonb NOT NULL
		)`,
	},
	{
		// Maintenance windows. Dates and times are local to the window's timezone, as entered.
		`CREATE TABLE IF NOT EXISTS maintenance (
			id serial PRIMARY KEY,
			title text NOT NULL,
			description text NOT NULL DEFAULT '',
			strategy text NOT NULL,
			active boolean NOT NULL DEFAULT true,
			start_date text NOT NULL DEFAULT '',
			end_date text NOT NULL DEFAULT '',
			start_time text NOT NULL DEFAULT '',
			end_time text NOT NULL DEFAULT '',
			weekdays integer[] NOT NULL DEFAULT '{}',
			days_of_month integer[] NOT NULL DEFAULT '{}',
			interval_day integer NOT NULL DEFAULT 1,
			cron text NOT NULL DEFAULT '',
			duration_minutes integer NOT NULL DEFAULT 0,
			timezone text NOT NULL DEFAULT 'SAME_AS_SERVER',
			holiday_mode text NOT NULL DEFAULT '',
			holiday_countries text[] NOT NULL DEFAULT '{}',
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS monitor_maintenance (
			monitor_id integer NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
			maintenance_id integer NOT NULL REFERENCES maintenance (id) ON DELETE CASCADE,
			PRIMARY KEY (monitor_id, maintenance_id)
		)`,
		`CREATE INDEX IF NOT EXISTS monitor_maintenance_maintenance_idx ON monitor_maintenance (maintenance_id)`,
		// Admins' changes to the bundled holiday calendars: added dates, or removed ones.
		`CREATE TABLE IF NOT EXISTS holiday_override (
			country text NOT NULL,
			date date NOT NULL,
			name text NOT NULL DEFAULT '',
			removed boolean NOT NULL DEFAULT false,
			PRIMARY KEY (country, date)
		)`,
	},
	{
		`CREATE TABLE IF NOT EXISTS tag (
			id serial PRIMARY KEY,
			name text NOT NULL,
			color text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		// A monitor can carry one tag several times, with different values.
		`CREATE TABLE IF NOT EXISTS monitor_tag (
			monitor_id integer NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
			tag_id integer NOT NULL REFERENCES tag (id) ON DELETE CASCADE,
			value text NOT NULL DEFAULT '',
			PRIMARY KEY (monitor_id, tag_id, value)
		)`,
		`CREATE INDEX IF NOT EXISTS monitor_tag_tag_idx ON monitor_tag (tag_id)`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS expiry_notification boolean NOT NULL DEFAULT false`,
		`CREATE TABLE IF NOT EXISTS monitor_tls_info (
			monitor_id integer PRIMARY KEY REFERENCES monitor (id) ON DELETE CASCADE,
			info text NOT NULL,
			fingerprint text NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		// Which deadlines before a certificate's expiry have been alerted about.
		`CREATE TABLE IF NOT EXISTS cert_alert_sent (
			monitor_id integer NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
			days integer NOT NULL,
			PRIMARY KEY (monitor_id, days)
		)`,
	},
	{
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS push_token text NOT NULL DEFAULT ''`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS ping_count integer NOT NULL DEFAULT 1`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS ping_per_request_timeout integer NOT NULL DEFAULT 2`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS packet_size integer NOT NULL DEFAULT 56`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS dns_resolve_type text NOT NULL DEFAULT 'A'`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS dns_resolve_server text NOT NULL DEFAULT '1.1.1.1'`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS dns_last_result text NOT NULL DEFAULT ''`,
		`CREATE UNIQUE INDEX IF NOT EXISTS ` + pushTokenIndex + ` ON monitor (push_token) WHERE push_token <> ''`,
	},
	{
		// Status pages. Analytics settings are null when unset, as the UI sends them.
		`CREATE TABLE IF NOT EXISTS status_page (
			id serial PRIMARY KEY,
			slug text NOT NULL UNIQUE,
			title text NOT NULL,
			description text NOT NULL DEFAULT '',
			icon text NOT NULL DEFAULT '',
			theme text NOT NULL DEFAULT 'auto',
			auto_refresh_interval integer NOT NULL DEFAULT 300,
			show_tags boolean NOT NULL DEFAULT false,
			footer_text text NOT NULL DEFAULT '',
			custom_css text NOT NULL DEFAULT '',
			show_powered_by boolean NOT NULL DEFAULT true,
			show_certificate_expiry boolean NOT NULL DEFAULT false,
			show_only_last_heartbeat boolean NOT NULL DEFAULT false,
			rss_title text NOT NULL DEFAULT '',
			analytics_type text,
			analytics_id text,
			analytics_script_url text,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		// A custom domain shows one status page.
		`CREATE TABLE IF NOT EXISTS status_page_domain (
			domain text PRIMARY KEY,
			status_page_id integer NOT NULL REFERENCES status_page (id) ON DELETE CASCADE
		)`,
		`CREATE INDEX IF NOT EXISTS status_page_domain_page_idx ON status_page_domain (status_page_id)`,
		// The logo uploaded for a page. A logo given as a URL stays in status_page.icon.
		`CREATE TABLE IF NOT EXISTS status_page_logo (
			status_page_id integer PRIMARY KEY REFERENCES status_page (id) ON DELETE CASCADE,
			image bytea NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS status_page_group (
			id serial PRIMARY KEY,
			status_page_id integer NOT NULL REFERENCES status_page (id) ON DELETE CASCADE,
			name text NOT NULL,
			weight integer NOT NULL
		)`,
		`CREATE INDEX IF NOT EXISTS status_page_group_page_idx ON status_page_group (status_page_id, weight)`,
		`CREATE TABLE IF NOT EXISTS status_page_monitor (
			group_id integer NOT NULL REFERENCES status_page_group (id) ON DELETE CASCADE,
			monitor_id integer NOT NULL REFERENCES monitor (id) ON DELETE CASCADE,
			weight integer NOT NULL,
			send_url boolean NOT NULL DEFAULT false,
			custom_url text,
			PRIMARY KEY (group_id, monitor_id)
		)`,
		`CREATE INDEX IF NOT EXISTS status_page_monitor_monitor_idx ON status_page_monitor (monitor_id)`,
		`CREATE TABLE IF NOT EXISTS incident (
			id serial PRIMARY KEY,
			status_page_id integer NOT NULL REFERENCES status_page (id) ON DELETE CASCADE,
			title text NOT NULL,
			content text NOT NULL,
			style text NOT NULL,
			pin boolean NOT NULL DEFAULT true,
			active boolean NOT NULL DEFAULT true,
			created_at timestamptz NOT NULL DEFAULT now(),
			updated_at timestamptz
		)`,
		`CREATE INDEX IF NOT EXISTS incident_page_idx ON incident (status_page_id, created_at DESC, id DESC)`,
		`CREATE TABLE IF NOT EXISTS maintenance_status_page (
			maintenance_id integer NOT NULL REFERENCES maintenance (id) ON DELETE CASCADE,
			status_page_id integer NOT NULL REFERENCES status_page (id) ON DELETE CASCADE,
			PRIMARY KEY (maintenance_id, status_page_id)
		)`,
		`CREATE INDEX IF NOT EXISTS maintenance_status_page_page_idx ON maintenance_status_page (status_page_id)`,
	},
	{
		// Settings of the json-query, manual, websocket-upgrade, postgres, mysql and redis types,
		// and the conditions of DNS and MySQL monitors.
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS json_path text NOT NULL DEFAULT ''`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS json_path_operator text NOT NULL DEFAULT '=='`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS expected_value text NOT NULL DEFAULT ''`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS conditions jsonb NOT NULL DEFAULT '[]'`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS manual_status smallint`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS ws_subprotocol text NOT NULL DEFAULT ''`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS ws_ignore_accept_header boolean NOT NULL DEFAULT false`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS database_connection_string text NOT NULL DEFAULT ''`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS database_query text NOT NULL DEFAULT ''`,
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS database_password text NOT NULL DEFAULT ''`,
	},
	{
		// Settings of the types added since Phase 4b, together, so that adding a type adds no column.
		`ALTER TABLE monitor ADD COLUMN IF NOT EXISTS settings jsonb NOT NULL DEFAULT '{}'`,
	},
	{
		// Docker hosts and remote browsers, which monitors name in settings, and the last
		// screenshot of each real-browser monitor.
		`CREATE TABLE IF NOT EXISTS docker_host (
			id serial PRIMARY KEY,
			name text NOT NULL,
			docker_type text NOT NULL,
			docker_daemon text NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS remote_browser (
			id serial PRIMARY KEY,
			name text NOT NULL,
			url text NOT NULL
		)`,
		`CREATE TABLE IF NOT EXISTS monitor_screenshot (
			monitor_id integer PRIMARY KEY REFERENCES monitor (id) ON DELETE CASCADE,
			png bytea NOT NULL,
			taken_at timestamptz NOT NULL DEFAULT now()
		)`,
	},
	{
		// Proxies, which monitors name in settings, and the expiry dates of their domains.
		`CREATE TABLE IF NOT EXISTS proxy (
			id serial PRIMARY KEY,
			protocol text NOT NULL,
			host text NOT NULL,
			port integer NOT NULL,
			auth boolean NOT NULL DEFAULT false,
			username text NOT NULL DEFAULT '',
			password text NOT NULL DEFAULT '',
			active boolean NOT NULL DEFAULT true,
			is_default boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS proxy_one_default_idx ON proxy (is_default) WHERE is_default`,
		`CREATE TABLE IF NOT EXISTS domain_expiry (
			domain text PRIMARY KEY,
			expiry timestamptz,
			last_check timestamptz,
			last_notified_days integer
		)`,
	},
	{
		// Two-factor login with an authenticator app. totp_secret is set when it's being set
		// up, and totp_enabled once a code has confirmed it. totp_last_step is the time step of
		// the last code used, so that a code works only once.
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS totp_secret bytea`,
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS totp_enabled boolean NOT NULL DEFAULT false`,
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS totp_last_step bigint`,
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS totp_failures integer NOT NULL DEFAULT 0`,
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS totp_locked_until timestamptz`,
		// A password that was right, waiting for its code. Only the token's hash is stored.
		`CREATE TABLE IF NOT EXISTS app_two_factor_challenge (
			token_hash bytea PRIMARY KEY,
			user_id integer NOT NULL REFERENCES app_user (id) ON DELETE CASCADE,
			remember boolean NOT NULL,
			attempts integer NOT NULL DEFAULT 0,
			expires_at timestamptz NOT NULL
		)`,
	},
	{
		// Status pages and maintenance windows copied by kuma-import. Each is copied once, and
		// deleting it here deletes its row, so that the next import copies it again.
		`CREATE TABLE IF NOT EXISTS status_page_import (
			source_id integer PRIMARY KEY,
			status_page_id integer NOT NULL UNIQUE REFERENCES status_page (id) ON DELETE CASCADE,
			imported_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS maintenance_import (
			source_id integer PRIMARY KEY,
			maintenance_id integer NOT NULL UNIQUE REFERENCES maintenance (id) ON DELETE CASCADE,
			imported_at timestamptz NOT NULL DEFAULT now()
		)`,
	},
	{
		// Accounts' roles: the password account and accounts until now are admins.
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS role text NOT NULL DEFAULT 'admin'`,
		`ALTER TABLE app_user DROP CONSTRAINT IF EXISTS app_user_role_check`,
		`ALTER TABLE app_user ADD CONSTRAINT app_user_role_check CHECK (role IN ('admin', 'viewer'))`,
	},
	{
		// Accounts that log in with GitHub. The token is kept to check membership again later.
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS github_id bigint`,
		`CREATE UNIQUE INDEX IF NOT EXISTS app_user_github_id_idx ON app_user (github_id) WHERE github_id IS NOT NULL`,
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS github_token text`,
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS github_checked_at timestamptz`,
	},
	{
		// When each account last logged in, for Settings > Accounts.
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS last_login_at timestamptz`,
	},
	{
		// The organization's teams a GitHub account was in at its last check, for the Security page.
		`ALTER TABLE app_user ADD COLUMN IF NOT EXISTS github_teams text[]`,
	},
	{
		// Where a setting's value came from: 'default' (made at start), 'ui' (saved in the UI) or
		// 'import' (kuma-import). An import replaces only defaults and what it wrote before.
		`ALTER TABLE app_setting ADD COLUMN IF NOT EXISTS source text`,
		// API keys from Uptime Kuma. better-auth's keep their hash, the SHA-256 that this service
		// uses too. The older table's are "uk<id>_<secret>" with a bcrypt hash of the secret.
		`ALTER TABLE api_key ALTER COLUMN key_hash DROP NOT NULL`,
		`ALTER TABLE api_key ADD COLUMN IF NOT EXISTS legacy_id integer UNIQUE`,
		`ALTER TABLE api_key ADD COLUMN IF NOT EXISTS legacy_hash text`,
		`ALTER TABLE api_key ADD COLUMN IF NOT EXISTS import_source text UNIQUE`,
		`ALTER TABLE api_key ADD CONSTRAINT api_key_hash_check
			CHECK (key_hash IS NOT NULL OR (legacy_id IS NOT NULL AND legacy_hash IS NOT NULL))`,
		// History of monitors that kuma-import skipped, kept apart until a version that can
		// import them does, by their id in the source.
		`CREATE TABLE IF NOT EXISTS skipped_monitor (
			source_id integer PRIMARY KEY,
			name text NOT NULL,
			reason text NOT NULL,
			kept_at timestamptz NOT NULL DEFAULT now()
		)`,
		`CREATE TABLE IF NOT EXISTS skipped_heartbeat (
			source_id integer NOT NULL REFERENCES skipped_monitor (source_id) ON DELETE CASCADE,
			time timestamptz NOT NULL,
			status smallint NOT NULL,
			ping double precision,
			msg text NOT NULL DEFAULT '',
			important boolean NOT NULL DEFAULT false,
			duration integer NOT NULL DEFAULT 0,
			retries integer NOT NULL DEFAULT 0,
			PRIMARY KEY (source_id, time)
		)`,
		`CREATE TABLE IF NOT EXISTS skipped_stat (
			source_id integer NOT NULL REFERENCES skipped_monitor (source_id) ON DELETE CASCADE,
			daily boolean NOT NULL,
			bucket timestamptz NOT NULL,
			up bigint NOT NULL,
			down bigint NOT NULL,
			maintenance bigint NOT NULL,
			ping_sum double precision NOT NULL,
			ping_count bigint NOT NULL,
			min_ping double precision,
			max_ping double precision,
			PRIMARY KEY (source_id, daily, bucket)
		)`,
	},
	{
		// Status page subscriptions: the SMTP notification a page's emails are sent through, and
		// who gets them. A webhook's address can hold a token, so it's sealed like other secrets;
		// address_key keeps one subscription per address (a lowercase email, or the URL's SHA-256).
		`ALTER TABLE status_page ADD COLUMN IF NOT EXISTS subscribe_notification_id integer
			REFERENCES notification (id) ON DELETE SET NULL`,
		`CREATE TABLE IF NOT EXISTS status_page_subscriber (
			id serial PRIMARY KEY,
			status_page_id integer NOT NULL REFERENCES status_page (id) ON DELETE CASCADE,
			kind text NOT NULL CHECK (kind IN ('email', 'webhook')),
			address text NOT NULL,
			address_key text NOT NULL,
			token text NOT NULL UNIQUE,
			confirmed boolean NOT NULL DEFAULT false,
			created_at timestamptz NOT NULL DEFAULT now(),
			confirm_sent_at timestamptz,
			last_sent_at timestamptz,
			last_error text NOT NULL DEFAULT '',
			UNIQUE (status_page_id, kind, address_key)
		)`,
		// Whether an incident was sent to subscribers, so that only those get its resolution.
		`ALTER TABLE incident ADD COLUMN IF NOT EXISTS notified boolean NOT NULL DEFAULT false`,
		// Incident templates, shared by every page, one per title.
		`CREATE TABLE IF NOT EXISTS incident_template (
			id serial PRIMARY KEY,
			title text NOT NULL UNIQUE,
			content text NOT NULL,
			style text NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		)`,
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
		`GRANT SELECT ON heartbeat, stat_hourly, stat_daily, uptime_hourly, uptime_daily, monitor_group_member TO ` + quoted,
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
