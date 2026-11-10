package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// BulkEdit is a change to several monitors at once, which applies to all of them or none.
type BulkEdit struct {
	MonitorIDs []int32
	// Settings are each monitor's new checking settings, by monitor; empty changes none.
	Settings []BulkSettings

	AddNotifications    []int32
	RemoveNotifications []int32
	AddTags             []TagValue
	// RemoveTags takes each tag off with all its values.
	RemoveTags []int32
}

// BulkSettings are the checking settings a bulk edit gives one monitor.
type BulkSettings struct {
	ID            int32
	Interval      int32
	RetryInterval int32
	MaxRetries    int32
}

// TagValue is a tag with a value, to put on monitors.
type TagValue struct {
	TagID int32
	Value string
}

// BulkEditMonitors applies a bulk edit in one transaction: if any part fails, such as a monitor,
// notification or tag deleted meanwhile, nothing changes. Only the fields it sets are written, so
// that other changes to the monitors made meanwhile stay. It returns the monitors whose settings
// changed, as they are now, or ErrNotFound when a monitor or tag no longer exists.
func (s *Store) BulkEditMonitors(ctx context.Context, e BulkEdit) ([]monitor.Monitor, error) {
	var updated []monitor.Monitor
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		// Locks the monitors, and finds any that went.
		var found int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM (SELECT 1 FROM monitor WHERE id = ANY($1) FOR UPDATE) m`,
			e.MonitorIDs).Scan(&found); err != nil {
			return fmt.Errorf("lock monitors: %w", err)
		}
		if found != len(e.MonitorIDs) {
			return ErrNotFound
		}
		for _, set := range e.Settings {
			m, err := scanMonitor(tx.QueryRow(ctx, `UPDATE monitor SET interval_seconds = $2, retry_interval_seconds = $3,
				max_retries = $4, updated_at = now() WHERE id = $1 RETURNING `+monitorColumns,
				set.ID, set.Interval, set.RetryInterval, set.MaxRetries))
			if err := monitorResult(err); err != nil {
				return err
			}
			updated = append(updated, m)
		}
		if len(e.RemoveNotifications) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM monitor_notification WHERE monitor_id = ANY($1) AND notification_id = ANY($2)`,
				e.MonitorIDs, e.RemoveNotifications); err != nil {
				return fmt.Errorf("remove notifications: %w", err)
			}
		}
		if len(e.AddNotifications) > 0 {
			_, err := tx.Exec(ctx, `INSERT INTO monitor_notification (monitor_id, notification_id)
				SELECT m, n FROM unnest($1::int[]) m, unnest($2::int[]) n ON CONFLICT DO NOTHING`,
				e.MonitorIDs, e.AddNotifications)
			if isForeignKeyError(err) {
				return fmt.Errorf("%w: a notification no longer exists", ErrInvalidNotification)
			}
			if err != nil {
				return fmt.Errorf("add notifications: %w", err)
			}
		}
		if len(e.RemoveTags) > 0 {
			if _, err := tx.Exec(ctx, `DELETE FROM monitor_tag WHERE monitor_id = ANY($1) AND tag_id = ANY($2)`,
				e.MonitorIDs, e.RemoveTags); err != nil {
				return fmt.Errorf("remove tags: %w", err)
			}
		}
		for _, t := range e.AddTags {
			_, err := tx.Exec(ctx, `INSERT INTO monitor_tag (monitor_id, tag_id, value)
				SELECT m, $2, $3 FROM unnest($1::int[]) m ON CONFLICT DO NOTHING`, e.MonitorIDs, t.TagID, t.Value)
			if isForeignKeyError(err) {
				return ErrNotFound
			}
			if err != nil {
				return fmt.Errorf("add tags: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return updated, nil
}

func isForeignKeyError(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
