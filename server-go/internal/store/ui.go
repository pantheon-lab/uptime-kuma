package store

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Queries for the web UI.

// LatestBeatPerMonitor returns each monitor's newest beat, for monitors that have one.
func (s *Store) LatestBeatPerMonitor(ctx context.Context) ([]Beat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.monitor_id, b.time, b.status, b.ping, b.msg, b.important, b.duration, b.retries,
		       COALESCE(b.trace_id, ''), b.down_count
		FROM monitor m
		CROSS JOIN LATERAL (
			SELECT * FROM heartbeat h WHERE h.monitor_id = m.id ORDER BY h.time DESC LIMIT 1
		) b`)
	if err != nil {
		return nil, fmt.Errorf("query latest beats: %w", err)
	}
	return collectBeats(rows)
}

// BeatsSince returns a monitor's beats after since, oldest first, up to limit.
func (s *Store) BeatsSince(ctx context.Context, monitorID int32, since time.Time, limit int) ([]Beat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, time, status, ping, msg, important, duration, retries, COALESCE(trace_id, ''), down_count
		FROM heartbeat WHERE monitor_id = $1 AND time > $2 ORDER BY time LIMIT $3`, monitorID, since, limit)
	if err != nil {
		return nil, fmt.Errorf("query beats: %w", err)
	}
	return collectBeats(rows)
}

// EventFilter narrows the important beats listed. An empty field doesn't narrow them.
type EventFilter struct {
	MonitorIDs []int32
	Statuses   []int16
	Since      *time.Time
}

// importantWhere selects important beats for one monitor or, with a nil monitorID, all of them,
// narrowed by the filter. Its arguments are $1 to $4.
const importantWhere = `important AND ($1::int IS NULL OR monitor_id = $1)
	AND ($2::int[] IS NULL OR monitor_id = ANY($2)) AND ($3::smallint[] IS NULL OR status = ANY($3))
	AND ($4::timestamptz IS NULL OR time >= $4)`

func (f EventFilter) args(monitorID *int32) []any {
	var ids []int32
	if len(f.MonitorIDs) > 0 {
		ids = f.MonitorIDs
	}
	var statuses []int16
	if len(f.Statuses) > 0 {
		statuses = f.Statuses
	}
	return []any{monitorID, ids, statuses, f.Since}
}

// ImportantBeats returns important beats, newest first, for one monitor or, with a nil
// monitorID, for all of them.
func (s *Store) ImportantBeats(ctx context.Context, monitorID *int32, filter EventFilter, offset, limit int) ([]Beat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT monitor_id, time, status, ping, msg, important, duration, retries, COALESCE(trace_id, ''), down_count
		FROM heartbeat WHERE `+importantWhere+`
		ORDER BY time DESC OFFSET $5 LIMIT $6`, append(filter.args(monitorID), offset, limit)...)
	if err != nil {
		return nil, fmt.Errorf("query important beats: %w", err)
	}
	return collectBeats(rows)
}

// CountImportantBeats counts important beats for one monitor or, with a nil monitorID, all.
func (s *Store) CountImportantBeats(ctx context.Context, monitorID *int32, filter EventFilter) (int64, error) {
	var count int64
	err := s.pool.QueryRow(ctx, `SELECT count(*) FROM heartbeat WHERE `+importantWhere,
		filter.args(monitorID)...).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count important beats: %w", err)
	}
	return count, nil
}

// ClearEvents marks a monitor's beats, or with a nil monitorID every monitor's, as not
// important, which empties the event list.
func (s *Store) ClearEvents(ctx context.Context, monitorID *int32) error {
	if _, err := s.pool.Exec(ctx,
		`UPDATE heartbeat SET important = false WHERE important AND ($1::int IS NULL OR monitor_id = $1)`,
		monitorID); err != nil {
		return fmt.Errorf("clear events: %w", err)
	}
	return nil
}

// AllMonitorNotificationIDs returns the notifications linked to each monitor.
func (s *Store) AllMonitorNotificationIDs(ctx context.Context) (map[int32][]int32, error) {
	rows, err := s.pool.Query(ctx, `SELECT monitor_id, notification_id FROM monitor_notification ORDER BY 1, 2`)
	if err != nil {
		return nil, fmt.Errorf("query monitor notifications: %w", err)
	}
	links := map[int32][]int32{}
	var monitorID, notificationID int32
	_, err = pgx.ForEachRow(rows, []any{&monitorID, &notificationID}, func() error {
		links[monitorID] = append(links[monitorID], notificationID)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read monitor notifications: %w", err)
	}
	return links, nil
}
