package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
)

// ImportedAccount is the account kuma-import copies from Node.js.
type ImportedAccount struct {
	Username string
	// PasswordHash is better-auth's scrypt hash, or a bcrypt hash from the table before it.
	PasswordHash string
	// TOTPSecret is the authenticator key, when two-factor login was on.
	TOTPSecret []byte
}

// ImportAccount creates the account, unless there is one already, which it reports as false.
func (s *Store) ImportAccount(ctx context.Context, a ImportedAccount) (bool, error) {
	created := false
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, setupLockID); err != nil {
			return fmt.Errorf("lock setup: %w", err)
		}
		tag, err := tx.Exec(ctx, `
			INSERT INTO app_user (username, password_hash, totp_secret, totp_enabled)
			SELECT $1, $2, $3, $4 WHERE NOT EXISTS (SELECT 1 FROM app_user)`,
			a.Username, a.PasswordHash, a.TOTPSecret, a.TOTPSecret != nil)
		if err != nil {
			return fmt.Errorf("import account: %w", err)
		}
		created = tag.RowsAffected() == 1
		return nil
	})
	return created, err
}

// UpgradePasswordHash replaces an imported bcrypt hash with a new one after a login, unless the
// password changed in the meantime.
func (s *Store) UpgradePasswordHash(ctx context.Context, userID int32, old, hash string) error {
	if _, err := s.pool.Exec(ctx, `UPDATE app_user SET password_hash = $3 WHERE id = $1 AND password_hash = $2`,
		userID, old, hash); err != nil {
		return fmt.Errorf("upgrade password hash: %w", err)
	}
	return nil
}

// ImportedStatusPages maps the source's status page ids to this service's.
func (s *Store) ImportedStatusPages(ctx context.Context) (map[int32]int32, error) {
	return importMap(ctx, s, `SELECT source_id, status_page_id FROM status_page_import`)
}

// ImportedMaintenances maps the source's maintenance window ids to this service's.
func (s *Store) ImportedMaintenances(ctx context.Context) (map[int32]int32, error) {
	return importMap(ctx, s, `SELECT source_id, maintenance_id FROM maintenance_import`)
}

func importMap(ctx context.Context, s *Store, query string) (map[int32]int32, error) {
	rows, err := s.pool.Query(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("read imports: %w", err)
	}
	ids := map[int32]int32{}
	var source, target int32
	_, err = pgx.ForEachRow(rows, []any{&source, &target}, func() error {
		ids[source] = target
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read imports: %w", err)
	}
	return ids, nil
}

// ImportedPage is a status page as kuma-import copies it, with everything on it.
type ImportedPage struct {
	Page   StatusPage
	Groups []StatusPageGroup
	// Logo is the uploaded PNG, which replaces Page.Icon.
	Logo      []byte
	Incidents []Incident
	CreatedAt time.Time
}

// ImportStatusPage creates a status page from the source, with its groups, incidents, logo and
// custom domains, and remembers where it came from. A domain that another page here already
// shows stays there, and is counted in the result. Monitors that don't exist are left out.
func (s *Store) ImportStatusPage(ctx context.Context, sourceID int32, p ImportedPage) (id int32, domainsTaken int, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	page := p.Page
	err = tx.QueryRow(ctx, `
		INSERT INTO status_page (slug, title, description, icon, theme, auto_refresh_interval, show_tags, footer_text,
			custom_css, show_powered_by, show_certificate_expiry, show_only_last_heartbeat, rss_title, analytics_type,
			analytics_id, analytics_script_url, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17)
		RETURNING id`,
		page.Slug, page.Title, page.Description, page.Icon, page.Theme, page.AutoRefreshInterval, page.ShowTags,
		page.FooterText, page.CustomCSS, page.ShowPoweredBy, page.ShowCertificateExpiry, page.ShowOnlyLastHeartbeat,
		page.RSSTitle, page.AnalyticsType, page.AnalyticsID, page.AnalyticsScriptURL, p.CreatedAt).Scan(&id)
	if err != nil {
		if errors.Is(statusPageError(err), ErrSlugTaken) {
			return 0, 0, ErrSlugTaken
		}
		return 0, 0, fmt.Errorf("import status page: %w", err)
	}
	if p.Logo != nil {
		icon := fmt.Sprintf("/upload/logo%d.png?t=%d", id, time.Now().UnixMilli())
		if _, err := tx.Exec(ctx, `UPDATE status_page SET icon = $2 WHERE id = $1`, id, icon); err != nil {
			return 0, 0, fmt.Errorf("set logo: %w", err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO status_page_logo (status_page_id, image) VALUES ($1, $2)`, id, p.Logo); err != nil {
			return 0, 0, fmt.Errorf("save logo: %w", err)
		}
	}
	for _, domain := range page.Domains {
		tag, err := tx.Exec(ctx, `INSERT INTO status_page_domain (domain, status_page_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`,
			domain, id)
		if err != nil {
			return 0, 0, fmt.Errorf("save domain: %w", err)
		}
		if tag.RowsAffected() == 0 {
			domainsTaken++
		}
	}
	for i, group := range p.Groups {
		var groupID int32
		if err := tx.QueryRow(ctx, `INSERT INTO status_page_group (status_page_id, name, weight) VALUES ($1, $2, $3) RETURNING id`,
			id, group.Name, i+1).Scan(&groupID); err != nil {
			return 0, 0, fmt.Errorf("save group: %w", err)
		}
		for j, m := range group.Monitors {
			if _, err := tx.Exec(ctx, `
				INSERT INTO status_page_monitor (group_id, monitor_id, weight, send_url, custom_url)
				SELECT $1::int, $2::int, $3::int, $4::bool, $5::text WHERE EXISTS (SELECT 1 FROM monitor WHERE id = $2)
				ON CONFLICT DO NOTHING`,
				groupID, m.ID, j+1, m.SendURL, m.CustomURL); err != nil {
				return 0, 0, fmt.Errorf("save group monitor: %w", err)
			}
		}
	}
	for _, incident := range p.Incidents {
		if _, err := tx.Exec(ctx, `
			INSERT INTO incident (status_page_id, title, content, style, pin, active, created_at, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`,
			id, incident.Title, incident.Content, incident.Style, incident.Pin, incident.Active,
			incident.CreatedAt, incident.UpdatedAt); err != nil {
			return 0, 0, fmt.Errorf("save incident: %w", err)
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO status_page_import (source_id, status_page_id) VALUES ($1, $2)`, sourceID, id); err != nil {
		return 0, 0, fmt.Errorf("record import: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit: %w", err)
	}
	return id, domainsTaken, nil
}

// ImportMaintenance creates a maintenance window from the source, with its monitors and status
// pages, and remembers where it came from.
func (s *Store) ImportMaintenance(ctx context.Context, sourceID int32, w maintenance.Window, monitorIDs, pageIDs []int32) (int32, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var id int32
	if err := tx.QueryRow(ctx, `
		INSERT INTO maintenance (title, description, strategy, active, start_date, end_date, start_time, end_time,
			weekdays, days_of_month, interval_day, cron, duration_minutes, timezone, holiday_mode, holiday_countries)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		RETURNING id`, maintenanceArgs(w)...).Scan(&id); err != nil {
		return 0, fmt.Errorf("import maintenance: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO monitor_maintenance (monitor_id, maintenance_id)
		SELECT DISTINCT m.id, $1::int FROM monitor m WHERE m.id = ANY($2)`, id, monitorIDs); err != nil {
		return 0, fmt.Errorf("link monitors: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO maintenance_status_page (maintenance_id, status_page_id)
		SELECT DISTINCT $1::int, p.id FROM status_page p WHERE p.id = ANY($2)`, id, pageIDs); err != nil {
		return 0, fmt.Errorf("link status pages: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO maintenance_import (source_id, maintenance_id) VALUES ($1, $2)`, sourceID, id); err != nil {
		return 0, fmt.Errorf("record import: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit: %w", err)
	}
	return id, nil
}
