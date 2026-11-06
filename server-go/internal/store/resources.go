package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
)

// ListDockerHosts returns the Docker hosts by id.
func (s *Store) ListDockerHosts(ctx context.Context) ([]check.DockerHost, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, docker_type, docker_daemon FROM docker_host ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list docker hosts: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (check.DockerHost, error) {
		var h check.DockerHost
		err := r.Scan(&h.ID, &h.Name, &h.Type, &h.Daemon)
		return h, err
	})
}

// DockerHost returns one Docker host or ErrNotFound.
func (s *Store) DockerHost(ctx context.Context, id int32) (check.DockerHost, error) {
	var h check.DockerHost
	err := s.pool.QueryRow(ctx, `SELECT id, name, docker_type, docker_daemon FROM docker_host WHERE id = $1`, id).
		Scan(&h.ID, &h.Name, &h.Type, &h.Daemon)
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrNotFound
	}
	return h, err
}

// SaveDockerHost adds a host, or replaces the one with h.ID if it isn't zero.
func (s *Store) SaveDockerHost(ctx context.Context, h check.DockerHost) (check.DockerHost, error) {
	var err error
	if h.ID == 0 {
		err = s.pool.QueryRow(ctx, `INSERT INTO docker_host (name, docker_type, docker_daemon) VALUES ($1, $2, $3) RETURNING id`,
			h.Name, h.Type, h.Daemon).Scan(&h.ID)
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE docker_host SET name = $2, docker_type = $3, docker_daemon = $4 WHERE id = $1 RETURNING id`,
			h.ID, h.Name, h.Type, h.Daemon).Scan(&h.ID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return h, ErrNotFound
	}
	if err != nil {
		return h, fmt.Errorf("save docker host: %w", err)
	}
	return h, nil
}

// DeleteDockerHost deletes a host, and takes it out of the monitors that used it, whose ids it
// returns.
func (s *Store) DeleteDockerHost(ctx context.Context, id int32) ([]int32, error) {
	return s.deleteReferenced(ctx, `DELETE FROM docker_host WHERE id = $1`, "dockerHost", id)
}

// ListRemoteBrowsers returns the remote browsers by id.
func (s *Store) ListRemoteBrowsers(ctx context.Context) ([]check.RemoteBrowser, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, name, url FROM remote_browser ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list remote browsers: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (check.RemoteBrowser, error) {
		var b check.RemoteBrowser
		err := r.Scan(&b.ID, &b.Name, &b.URL)
		return b, err
	})
}

// RemoteBrowser returns one remote browser or ErrNotFound.
func (s *Store) RemoteBrowser(ctx context.Context, id int32) (check.RemoteBrowser, error) {
	var b check.RemoteBrowser
	err := s.pool.QueryRow(ctx, `SELECT id, name, url FROM remote_browser WHERE id = $1`, id).Scan(&b.ID, &b.Name, &b.URL)
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	return b, err
}

// SaveRemoteBrowser adds a remote browser, or replaces the one with b.ID if it isn't zero.
func (s *Store) SaveRemoteBrowser(ctx context.Context, b check.RemoteBrowser) (check.RemoteBrowser, error) {
	var err error
	if b.ID == 0 {
		err = s.pool.QueryRow(ctx, `INSERT INTO remote_browser (name, url) VALUES ($1, $2) RETURNING id`, b.Name, b.URL).Scan(&b.ID)
	} else {
		err = s.pool.QueryRow(ctx, `UPDATE remote_browser SET name = $2, url = $3 WHERE id = $1 RETURNING id`, b.ID, b.Name, b.URL).Scan(&b.ID)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return b, ErrNotFound
	}
	if err != nil {
		return b, fmt.Errorf("save remote browser: %w", err)
	}
	return b, nil
}

// DeleteRemoteBrowser deletes a remote browser, and takes it out of the monitors that used it,
// which then use a local browser, as in Node.js. It returns their ids.
func (s *Store) DeleteRemoteBrowser(ctx context.Context, id int32) ([]int32, error) {
	return s.deleteReferenced(ctx, `DELETE FROM remote_browser WHERE id = $1`, "remoteBrowser", id)
}

// deleteReferenced deletes a row that monitors name by id in a settings key.
func (s *Store) deleteReferenced(ctx context.Context, deletion, key string, id int32) ([]int32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	tag, err := tx.Exec(ctx, deletion, id)
	if err != nil {
		return nil, fmt.Errorf("delete: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	rows, err := tx.Query(ctx, `UPDATE monitor SET settings = settings - $1::text, updated_at = now()
		WHERE settings->$1::text = to_jsonb($2::int) RETURNING id`, key, id)
	if err != nil {
		return nil, fmt.Errorf("update monitors: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int32])
	if err != nil {
		return nil, fmt.Errorf("update monitors: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return ids, nil
}

// ChromeExecutable is the chromeExecutable setting, from Settings > General.
func (s *Store) ChromeExecutable(ctx context.Context) (string, error) {
	var raw []byte
	err := s.pool.QueryRow(ctx, `SELECT value FROM app_setting WHERE key = 'chromeExecutable'`).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("read chromeExecutable: %w", err)
	}
	var path string
	_ = json.Unmarshal(raw, &path)
	return path, nil
}

// SaveScreenshot keeps the last screenshot of a real-browser monitor.
func (s *Store) SaveScreenshot(ctx context.Context, monitorID int32, png []byte) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO monitor_screenshot (monitor_id, png) VALUES ($1, $2)
		ON CONFLICT (monitor_id) DO UPDATE SET png = excluded.png, taken_at = now()`, monitorID, png)
	if err != nil {
		return fmt.Errorf("save screenshot: %w", err)
	}
	return nil
}

// Screenshot returns a monitor's last screenshot, or ErrNotFound.
func (s *Store) Screenshot(ctx context.Context, monitorID int32) ([]byte, time.Time, error) {
	var png []byte
	var taken time.Time
	err := s.pool.QueryRow(ctx, `SELECT png, taken_at FROM monitor_screenshot WHERE monitor_id = $1`, monitorID).Scan(&png, &taken)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, taken, ErrNotFound
	}
	return png, taken, err
}

// ImportDockerHost returns the id of a host with the same settings, and adds one if there's none.
func (s *Store) ImportDockerHost(ctx context.Context, h check.DockerHost) (int32, error) {
	var id int32
	err := s.pool.QueryRow(ctx, `SELECT id FROM docker_host WHERE name = $1 AND docker_type = $2 AND docker_daemon = $3
		ORDER BY id LIMIT 1`, h.Name, h.Type, h.Daemon).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find docker host: %w", err)
	}
	h.ID = 0
	saved, err := s.SaveDockerHost(ctx, h)
	return saved.ID, err
}

// ImportRemoteBrowser returns the id of a remote browser with the same settings, and adds one if
// there's none.
func (s *Store) ImportRemoteBrowser(ctx context.Context, b check.RemoteBrowser) (int32, error) {
	var id int32
	err := s.pool.QueryRow(ctx, `SELECT id FROM remote_browser WHERE name = $1 AND url = $2 ORDER BY id LIMIT 1`, b.Name, b.URL).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find remote browser: %w", err)
	}
	b.ID = 0
	saved, err := s.SaveRemoteBrowser(ctx, b)
	return saved.ID, err
}
