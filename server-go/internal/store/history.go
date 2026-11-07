package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// RetentionSetting is the setting from Settings > Monitor History that decides how many
// days of raw beats are kept. Zero keeps them forever.
const RetentionSetting = "keepDataPeriodDays"

// MaxRetentionDays bounds the setting, so that it fits an interval.
const MaxRetentionDays = 100 * 366

// ParseRetention reads the retention setting, which the UI sends as a number, or as text
// when the field was edited.
func ParseRetention(raw json.RawMessage) (int, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, fmt.Errorf("invalid number of days: %w", err)
	}
	var days float64
	switch v := value.(type) {
	case float64:
		days = v
	case string:
		parsed, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err != nil {
			return 0, fmt.Errorf("invalid number of days %q", v)
		}
		days = parsed
	default:
		return 0, errors.New("invalid number of days")
	}
	if days != math.Trunc(days) || days < 0 || days > MaxRetentionDays {
		return 0, fmt.Errorf("the number of days must be a whole number from 0 to %d", MaxRetentionDays)
	}
	return int(days), nil
}

// aggregateTables returns the hypertables that hold the continuous aggregates' rows. Rows
// whose beats were already dropped by retention can only be deleted there: a refresh of the
// view recomputes buckets from beats, and would leave them.
func aggregateTables(ctx context.Context, q interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}) ([]string, error) {
	rows, err := q.Query(ctx, `
		SELECT format('%I.%I', materialization_hypertable_schema, materialization_hypertable_name)
		FROM timescaledb_information.continuous_aggregates
		WHERE view_schema = current_schema() AND view_name IN ('stat_hourly', 'stat_daily')`)
	if err != nil {
		return nil, fmt.Errorf("find aggregate tables: %w", err)
	}
	tables, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read aggregate tables: %w", err)
	}
	if len(tables) != 2 {
		return nil, fmt.Errorf("found %d of the 2 aggregate tables", len(tables))
	}
	return tables, nil
}

// ClearMonitorStats deletes a monitor's beats and statistics: its beats, its rows in the
// aggregates, including those older than its beats, and the history imported from Node.js.
// Stop the monitor first, so that queued beats aren't written after.
func (s *Store) ClearMonitorStats(ctx context.Context, id int32) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin clear: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tables, err := aggregateTables(ctx, tx)
	if err != nil {
		return err
	}
	for _, table := range append([]string{"heartbeat", "imported_stat_hourly", "imported_stat_daily"}, tables...) {
		if _, err := tx.Exec(ctx, `DELETE FROM `+table+` WHERE monitor_id = $1`, id); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit clear: %w", err)
	}
	// The deletes marked the beats' buckets as changed. Refreshing now, while the other
	// monitors' beats in them are still kept, stops a later refresh from recomputing them
	// after retention has dropped those beats.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aggregateRefreshTimeout)
	defer cancel()
	return s.RefreshAggregates(refreshCtx)
}

// ClearAllStats deletes every monitor's beats and statistics. Stop the monitors first.
func (s *Store) ClearAllStats(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin clear: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	// TimescaleDB doesn't truncate a hypertable that an aggregate is built on, but it drops
	// its chunks, as retention does, which frees their space at once.
	for _, table := range []string{"heartbeat", "stat_hourly", "stat_daily"} {
		if _, err := tx.Exec(ctx, `SELECT drop_chunks($1::regclass, older_than => 'infinity'::timestamptz)`, table); err != nil {
			return fmt.Errorf("clear %s: %w", table, err)
		}
	}
	if _, err := tx.Exec(ctx, `TRUNCATE imported_stat_hourly, imported_stat_daily`); err != nil {
		return fmt.Errorf("clear imported statistics: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit clear: %w", err)
	}
	// As above, for anything the drops marked as changed.
	refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), aggregateRefreshTimeout)
	defer cancel()
	return s.RefreshAggregates(refreshCtx)
}

// DatabaseSize returns the size of the database on disk, in bytes.
func (s *Store) DatabaseSize(ctx context.Context) (int64, error) {
	var size int64
	if err := s.pool.QueryRow(ctx, `SELECT pg_database_size(current_database())`).Scan(&size); err != nil {
		return 0, fmt.Errorf("database size: %w", err)
	}
	return size, nil
}

// RetentionDays returns the retention setting. When it was never saved, or isn't valid, it
// saves fallback as the setting first, so that the UI shows the days that apply.
func (s *Store) RetentionDays(ctx context.Context, fallback int) (days int, saved bool, err error) {
	var raw []byte
	err = s.pool.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = $1`, RetentionSetting).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return 0, false, fmt.Errorf("read %s: %w", RetentionSetting, err)
	}
	if err == nil {
		if days, err := ParseRetention(raw); err == nil {
			return days, true, nil
		}
	}
	value, _ := json.Marshal(fallback)
	if err := s.SaveSettings(ctx, map[string]json.RawMessage{RetentionSetting: value}); err != nil {
		return 0, false, err
	}
	return fallback, false, nil
}
