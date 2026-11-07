package store

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Tag is a label that monitors can carry, with a value per monitor.
type Tag struct {
	ID    int32  `json:"id"`
	Name  string `json:"name"`
	Color string `json:"color"`
}

// MonitorTag is a tag on a monitor. The JSON names are what the web UI reads.
type MonitorTag struct {
	TagID     int32  `json:"tag_id"`
	MonitorID int32  `json:"monitor_id"`
	Value     string `json:"value"`
	Name      string `json:"name"`
	Color     string `json:"color"`
}

// ListTags returns every tag by name.
func (s *Store) ListTags(ctx context.Context) ([]Tag, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, color FROM tag ORDER BY name, id`)
	if err != nil {
		return nil, fmt.Errorf("query tags: %w", err)
	}
	tags, err := pgx.CollectRows(rows, pgx.RowToStructByPos[Tag])
	if err != nil {
		return nil, fmt.Errorf("read tags: %w", err)
	}
	return tags, nil
}

// CreateTag stores a new tag.
func (s *Store) CreateTag(ctx context.Context, name, color string) (Tag, error) {
	tag := Tag{Name: name, Color: color}
	err := s.pool.QueryRow(ctx, `INSERT INTO tag (name, color) VALUES ($1, $2) RETURNING id`, name, color).Scan(&tag.ID)
	if err != nil {
		return Tag{}, fmt.Errorf("create tag: %w", err)
	}
	return tag, nil
}

// UpdateTag changes a tag, or returns ErrNotFound.
func (s *Store) UpdateTag(ctx context.Context, tag Tag) error {
	result, err := s.pool.Exec(ctx, `UPDATE tag SET name = $2, color = $3 WHERE id = $1`, tag.ID, tag.Name, tag.Color)
	if err != nil {
		return fmt.Errorf("update tag: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// DeleteTag deletes a tag and takes it off every monitor, or returns ErrNotFound.
func (s *Store) DeleteTag(ctx context.Context, id int32) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM tag WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("delete tag: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AddMonitorTag puts a tag on a monitor with a value. Doing it twice changes nothing.
func (s *Store) AddMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO monitor_tag (tag_id, monitor_id, value) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`,
		tagID, monitorID, value)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23503" {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("add monitor tag: %w", err)
	}
	return nil
}

// UpdateMonitorTag changes the value of a tag on a monitor, as the Node.js backend did: every
// value that tag has on that monitor. It returns ErrNotFound if the monitor doesn't have the tag.
func (s *Store) UpdateMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error {
	return pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		result, err := tx.Exec(ctx, `DELETE FROM monitor_tag WHERE tag_id = $1 AND monitor_id = $2`, tagID, monitorID)
		if err != nil {
			return fmt.Errorf("update monitor tag: %w", err)
		}
		if result.RowsAffected() == 0 {
			return ErrNotFound
		}
		if _, err := tx.Exec(ctx, `INSERT INTO monitor_tag (tag_id, monitor_id, value) VALUES ($1, $2, $3)`,
			tagID, monitorID, value); err != nil {
			return fmt.Errorf("update monitor tag: %w", err)
		}
		return nil
	})
}

// DeleteMonitorTag takes one value of a tag off a monitor.
func (s *Store) DeleteMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM monitor_tag WHERE tag_id = $1 AND monitor_id = $2 AND value = $3`,
		tagID, monitorID, value); err != nil {
		return fmt.Errorf("delete monitor tag: %w", err)
	}
	return nil
}

// AllMonitorTags returns the tags on each monitor.
func (s *Store) AllMonitorTags(ctx context.Context) (map[int32][]MonitorTag, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT mt.tag_id, mt.monitor_id, mt.value, t.name, t.color
		FROM monitor_tag mt JOIN tag t ON t.id = mt.tag_id
		ORDER BY mt.monitor_id, t.name, mt.value`)
	if err != nil {
		return nil, fmt.Errorf("query monitor tags: %w", err)
	}
	tags, err := pgx.CollectRows(rows, pgx.RowToStructByPos[MonitorTag])
	if err != nil {
		return nil, fmt.Errorf("read monitor tags: %w", err)
	}
	out := map[int32][]MonitorTag{}
	for _, tag := range tags {
		out[tag.MonitorID] = append(out[tag.MonitorID], tag)
	}
	return out, nil
}

// ImportTag finds a tag with this name and colour, or creates it, so that importing again
// doesn't duplicate tags.
func (s *Store) ImportTag(ctx context.Context, name, color string) (int32, error) {
	var id int32
	err := s.pool.QueryRow(ctx, `SELECT id FROM tag WHERE name = $1 AND color = $2 ORDER BY id LIMIT 1`, name, color).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find tag: %w", err)
	}
	tag, err := s.CreateTag(ctx, name, color)
	return tag.ID, err
}
