package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
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

const proxyColumns = `id, protocol, host, port, auth, username, password, active, is_default`

func scanProxy(row pgx.Row) (check.Proxy, error) {
	var p check.Proxy
	err := row.Scan(&p.ID, &p.Protocol, &p.Host, &p.Port, &p.Auth, &p.Username, &p.Password, &p.Active, &p.Default)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	return p, err
}

// ListProxies returns the proxies by id.
func (s *Store) ListProxies(ctx context.Context) ([]check.Proxy, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+proxyColumns+` FROM proxy ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list proxies: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (check.Proxy, error) { return scanProxy(r) })
}

// Proxy returns one proxy or ErrNotFound.
func (s *Store) Proxy(ctx context.Context, id int32) (check.Proxy, error) {
	return scanProxy(s.pool.QueryRow(ctx, `SELECT `+proxyColumns+` FROM proxy WHERE id = $1`, id))
}

// SaveProxy adds a proxy, or replaces the one with p.ID if it isn't zero. A default proxy stops
// another being the default. applyExisting puts it on every HTTP monitor, whose ids it returns.
func (s *Store) SaveProxy(ctx context.Context, p check.Proxy, applyExisting bool) (check.Proxy, []int32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return p, nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if p.Default {
		if _, err := tx.Exec(ctx, `UPDATE proxy SET is_default = false WHERE is_default AND id <> $1`, p.ID); err != nil {
			return p, nil, fmt.Errorf("clear default proxy: %w", err)
		}
	}
	if p.ID == 0 {
		p, err = scanProxy(tx.QueryRow(ctx, `INSERT INTO proxy (protocol, host, port, auth, username, password, active, is_default)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8) RETURNING `+proxyColumns,
			p.Protocol, p.Host, p.Port, p.Auth, p.Username, p.Password, p.Active, p.Default))
	} else {
		p, err = scanProxy(tx.QueryRow(ctx, `UPDATE proxy SET protocol = $2, host = $3, port = $4, auth = $5, username = $6,
			password = $7, active = $8, is_default = $9 WHERE id = $1 RETURNING `+proxyColumns,
			p.ID, p.Protocol, p.Host, p.Port, p.Auth, p.Username, p.Password, p.Active, p.Default))
	}
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return p, nil, err
		}
		return p, nil, fmt.Errorf("save proxy: %w", err)
	}
	var changed []int32
	if applyExisting {
		rows, err := tx.Query(ctx, `UPDATE monitor SET settings = settings || jsonb_build_object('proxyId', $1::int), updated_at = now()
			WHERE type IN ('http', 'keyword', 'json-query') AND settings->'proxyId' IS DISTINCT FROM to_jsonb($1::int)
			RETURNING id`, p.ID)
		if err != nil {
			return p, nil, fmt.Errorf("apply proxy: %w", err)
		}
		if changed, err = pgx.CollectRows(rows, pgx.RowTo[int32]); err != nil {
			return p, nil, fmt.Errorf("apply proxy: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return p, nil, fmt.Errorf("commit: %w", err)
	}
	return p, changed, nil
}

// DeleteProxy deletes a proxy, and takes it out of the monitors that used it, whose ids it returns.
func (s *Store) DeleteProxy(ctx context.Context, id int32) ([]int32, error) {
	return s.deleteReferenced(ctx, `DELETE FROM proxy WHERE id = $1`, "proxyId", id)
}

// ImportProxy returns the id of a proxy with the same settings, and adds one if there's none.
// An imported proxy is never the default, so that the one chosen here stays.
func (s *Store) ImportProxy(ctx context.Context, p check.Proxy) (int32, error) {
	var id int32
	err := s.pool.QueryRow(ctx, `SELECT id FROM proxy WHERE protocol = $1 AND host = $2 AND port = $3 AND auth = $4
		AND username = $5 AND password = $6 ORDER BY id LIMIT 1`, p.Protocol, p.Host, p.Port, p.Auth, p.Username, p.Password).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, fmt.Errorf("find proxy: %w", err)
	}
	p.ID, p.Default = 0, false
	saved, _, err := s.SaveProxy(ctx, p, false)
	return saved.ID, err
}

// DomainExpiry returns what's known about a domain. A domain never seen has an empty record.
func (s *Store) DomainExpiry(ctx context.Context, domain string) (domains.Record, error) {
	r := domains.Record{Domain: domain}
	var days *int32
	err := s.pool.QueryRow(ctx, `SELECT expiry, last_check, last_notified_days FROM domain_expiry WHERE domain = $1`, domain).
		Scan(&r.Expiry, &r.LastCheck, &days)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, nil
	}
	if err != nil {
		return r, fmt.Errorf("read domain expiry: %w", err)
	}
	if days != nil {
		value := int(*days)
		r.LastNotifiedDays = &value
	}
	return r, nil
}

// SaveDomainExpiry stores what's known about a domain.
func (s *Store) SaveDomainExpiry(ctx context.Context, r domains.Record) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO domain_expiry (domain, expiry, last_check, last_notified_days) VALUES ($1, $2, $3, $4)
		ON CONFLICT (domain) DO UPDATE SET expiry = excluded.expiry, last_check = excluded.last_check,
		last_notified_days = excluded.last_notified_days`, r.Domain, r.Expiry, r.LastCheck, r.LastNotifiedDays)
	if err != nil {
		return fmt.Errorf("save domain expiry: %w", err)
	}
	return nil
}
