package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Notification is one configured notification target. Config keeps the field names of the
// provider, and can hold credentials.
type Notification struct {
	ID        int32          `json:"id"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	Config    map[string]any `json:"config"`
	Active    bool           `json:"active"`
	IsDefault bool           `json:"isDefault"`
}

// ImportedNotification is what an earlier import left behind for a source notification.
type ImportedNotification struct {
	NotificationID int32
	Active         bool
}

const notificationColumns = `id, name, type, config, active, is_default`

func (s *Store) scanNotification(row pgx.Row) (Notification, error) {
	var n Notification
	var config []byte
	if err := row.Scan(&n.ID, &n.Name, &n.Type, &config, &n.Active, &n.IsDefault); err != nil {
		return n, err
	}
	if err := s.openJSON(config, notificationAAD(n.ID), &n.Config); err != nil {
		return n, fmt.Errorf("notification %d: %w", n.ID, err)
	}
	return n, nil
}

func notificationAAD(id int32) string { return secretAAD("notification", "config", id) }

// nextNotificationID takes the id a new notification will have, so that its settings can be
// sealed for its row before it's inserted.
func nextNotificationID(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}) (int32, error) {
	var id int32
	err := q.QueryRow(ctx, `SELECT nextval(pg_get_serial_sequence('notification', 'id'))::int`).Scan(&id)
	return id, err
}

func notificationResult(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("notification query: %w", err)
	}
	return nil
}

func (s *Store) collectNotifications(rows pgx.Rows) ([]Notification, error) {
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (Notification, error) { return s.scanNotification(r) })
	if err != nil {
		return nil, fmt.Errorf("read notifications: %w", err)
	}
	return list, nil
}

// ListNotifications returns all notifications ordered by id.
func (s *Store) ListNotifications(ctx context.Context) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+notificationColumns+` FROM notification ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list notifications: %w", err)
	}
	return s.collectNotifications(rows)
}

// GetNotification returns one notification or ErrNotFound.
func (s *Store) GetNotification(ctx context.Context, id int32) (Notification, error) {
	n, err := s.scanNotification(s.pool.QueryRow(ctx, `SELECT `+notificationColumns+` FROM notification WHERE id = $1`, id))
	return n, notificationResult(err)
}

// CreateNotification stores a new notification. With applyToAll, every monitor starts using it.
func (s *Store) CreateNotification(ctx context.Context, n Notification, applyToAll bool) (Notification, error) {
	id, err := nextNotificationID(ctx, s.pool)
	if err != nil {
		return Notification{}, fmt.Errorf("create notification: %w", err)
	}
	config, err := s.sealJSON(n.Config, notificationAAD(id))
	if err != nil {
		return Notification{}, err
	}
	created, err := s.scanNotification(s.pool.QueryRow(ctx, `
		INSERT INTO notification (id, name, type, config, active, is_default) VALUES ($1, $2, $3, $4, $5, $6)
		RETURNING `+notificationColumns, id, n.Name, n.Type, config, n.Active, n.IsDefault))
	if err := notificationResult(err); err != nil {
		return created, err
	}
	if applyToAll {
		return created, s.applyToAllMonitors(ctx, created.ID)
	}
	return created, nil
}

// UpdateNotification replaces a notification or returns ErrNotFound.
func (s *Store) UpdateNotification(ctx context.Context, n Notification, applyToAll bool) (Notification, error) {
	config, err := s.sealJSON(n.Config, notificationAAD(n.ID))
	if err != nil {
		return Notification{}, err
	}
	updated, err := s.scanNotification(s.pool.QueryRow(ctx, `
		UPDATE notification SET name = $2, type = $3, config = $4, active = $5, is_default = $6, updated_at = now()
		WHERE id = $1 RETURNING `+notificationColumns, n.ID, n.Name, n.Type, config, n.Active, n.IsDefault))
	if err := notificationResult(err); err != nil {
		return updated, err
	}
	if applyToAll {
		return updated, s.applyToAllMonitors(ctx, updated.ID)
	}
	return updated, nil
}

func (s *Store) applyToAllMonitors(ctx context.Context, id int32) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO monitor_notification (monitor_id, notification_id)
		SELECT id, $1 FROM monitor ON CONFLICT DO NOTHING`, id)
	if err != nil {
		return fmt.Errorf("apply notification: %w", err)
	}
	return nil
}

// DeleteNotification removes a notification or returns ErrNotFound.
func (s *Store) DeleteNotification(ctx context.Context, id int32) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM notification WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete notification: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// MonitorNotificationIDs returns the notifications a monitor uses, or ErrNotFound.
func (s *Store) MonitorNotificationIDs(ctx context.Context, monitorID int32) ([]int32, error) {
	if _, err := s.GetMonitor(ctx, monitorID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `SELECT notification_id FROM monitor_notification WHERE monitor_id = $1 ORDER BY 1`, monitorID)
	if err != nil {
		return nil, fmt.Errorf("read monitor notifications: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return nil, fmt.Errorf("read monitor notifications: %w", err)
	}
	return ids, nil
}

// SetMonitorNotifications replaces the notifications a monitor uses.
func (s *Store) SetMonitorNotifications(ctx context.Context, monitorID int32, ids []int32) error {
	if _, err := s.GetMonitor(ctx, monitorID); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if _, err := tx.Exec(ctx, `DELETE FROM monitor_notification WHERE monitor_id = $1`, monitorID); err != nil {
		return fmt.Errorf("clear monitor notifications: %w", err)
	}
	for _, id := range ids {
		tag, err := tx.Exec(ctx, `
			INSERT INTO monitor_notification (monitor_id, notification_id)
			SELECT $1, id FROM notification WHERE id = $2 ON CONFLICT DO NOTHING`, monitorID, id)
		if err != nil {
			return fmt.Errorf("set monitor notification: %w", err)
		}
		if tag.RowsAffected() == 0 {
			var exists bool
			if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notification WHERE id = $1)`, id).Scan(&exists); err != nil {
				return fmt.Errorf("check notification: %w", err)
			}
			if !exists {
				return fmt.Errorf("%w: notification %d doesn't exist", ErrInvalidNotification, id)
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

// LinkDefaultNotifications attaches the default notifications to a new monitor.
func (s *Store) LinkDefaultNotifications(ctx context.Context, monitorID int32) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO monitor_notification (monitor_id, notification_id)
		SELECT $1, id FROM notification WHERE is_default ON CONFLICT DO NOTHING`, monitorID)
	if err != nil {
		return fmt.Errorf("link default notifications: %w", err)
	}
	return nil
}

// ActiveNotificationsFor returns the notifications to send for a monitor.
func (s *Store) ActiveNotificationsFor(ctx context.Context, monitorID int32) ([]Notification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT n.id, n.name, n.type, n.config, n.active, n.is_default
		FROM notification n JOIN monitor_notification l ON l.notification_id = n.id
		WHERE l.monitor_id = $1 AND n.active ORDER BY n.id`, monitorID)
	if err != nil {
		return nil, fmt.Errorf("read notifications: %w", err)
	}
	return s.collectNotifications(rows)
}

// LastDownTime returns when a monitor last went down, if it has.
func (s *Store) LastDownTime(ctx context.Context, monitorID int32) (*time.Time, error) {
	var at time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT time FROM heartbeat WHERE monitor_id = $1 AND status = 0 AND important
		ORDER BY time DESC LIMIT 1`, monitorID).Scan(&at)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read last down time: %w", err)
	}
	return &at, nil
}

// MonitorPath returns a monitor's names from its top group down to itself.
func (s *Store) MonitorPath(ctx context.Context, monitorID int32) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		WITH RECURSIVE up (id, name, parent_id, depth) AS (
			SELECT id, name, parent_id, 0 FROM monitor WHERE id = $1
			UNION ALL
			SELECT m.id, m.name, m.parent_id, up.depth + 1
			FROM monitor m JOIN up ON m.id = up.parent_id WHERE up.depth < 32
		)
		SELECT name FROM up ORDER BY depth DESC`, monitorID)
	if err != nil {
		return nil, fmt.Errorf("read monitor path: %w", err)
	}
	path, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("read monitor path: %w", err)
	}
	return path, nil
}

// ImportedNotifications maps source ids to earlier imports.
func (s *Store) ImportedNotifications(ctx context.Context) (map[int32]ImportedNotification, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT i.source_id, i.notification_id, n.active
		FROM notification_import i JOIN notification n ON n.id = i.notification_id`)
	if err != nil {
		return nil, fmt.Errorf("read imported notifications: %w", err)
	}
	defer rows.Close()
	result := make(map[int32]ImportedNotification)
	for rows.Next() {
		var id int32
		var n ImportedNotification
		if err := rows.Scan(&id, &n.NotificationID, &n.Active); err != nil {
			return nil, fmt.Errorf("read imported notification: %w", err)
		}
		result[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read imported notifications: %w", err)
	}
	return result, nil
}

// ImportNotification creates or refreshes a notification, always switched off, because
// Uptime Kuma is still sending these alerts until the cutover.
func (s *Store) ImportNotification(ctx context.Context, sourceID int32, n Notification, sourceActive bool) (int32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var id int32
	err = tx.QueryRow(ctx, `SELECT notification_id FROM notification_import WHERE source_id = $1`, sourceID).Scan(&id)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if id, err = nextNotificationID(ctx, tx); err != nil {
			return 0, fmt.Errorf("create notification: %w", err)
		}
		config, err := s.sealJSON(n.Config, notificationAAD(id))
		if err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, `
			INSERT INTO notification (id, name, type, config, active, is_default) VALUES ($1, $2, $3, $4, false, $5)`,
			id, n.Name, n.Type, config, n.IsDefault)
		if err != nil {
			return 0, fmt.Errorf("create notification: %w", err)
		}
	case err != nil:
		return 0, fmt.Errorf("read import: %w", err)
	default:
		config, err := s.sealJSON(n.Config, notificationAAD(id))
		if err != nil {
			return 0, err
		}
		_, err = tx.Exec(ctx, `UPDATE notification SET name = $2, type = $3, config = $4, is_default = $5, updated_at = now() WHERE id = $1`,
			id, n.Name, n.Type, config, n.IsDefault)
		if err != nil {
			return 0, fmt.Errorf("update notification: %w", err)
		}
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO notification_import (source_id, notification_id, source_active) VALUES ($1, $2, $3)
		ON CONFLICT (source_id) DO UPDATE SET source_active = $3, imported_at = now()`, sourceID, id, sourceActive)
	if err != nil {
		return 0, fmt.Errorf("record import: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}

// ActivateImportedNotifications switches on notifications that were active in the source.
func (s *Store) ActivateImportedNotifications(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE notification n SET active = true, updated_at = now()
		FROM notification_import i WHERE i.notification_id = n.id AND i.source_active AND NOT n.active`)
	if err != nil {
		return 0, fmt.Errorf("activate imported notifications: %w", err)
	}
	return tag.RowsAffected(), nil
}
