package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// ErrSlugTaken means that another status page has the slug.
var ErrSlugTaken = errors.New("another status page already uses this slug")

// StatusPage is a status page's settings.
type StatusPage struct {
	ID          int32
	Slug        string
	Title       string
	Description string
	// Icon is the logo's URL, or empty for the default one.
	Icon                  string
	Theme                 string
	AutoRefreshInterval   int32
	ShowTags              bool
	FooterText            string
	CustomCSS             string
	ShowPoweredBy         bool
	ShowCertificateExpiry bool
	ShowOnlyLastHeartbeat bool
	RSSTitle              string
	AnalyticsType         *string
	AnalyticsID           *string
	AnalyticsScriptURL    *string
	// Domains are the custom domains that show this page.
	Domains   []string
	UpdatedAt time.Time
}

// StatusPageGroup is a group of monitors on a status page.
type StatusPageGroup struct {
	ID       int32
	Name     string
	Monitors []StatusPageMonitor
}

// StatusPageMonitor is a monitor on a status page. Name, Type and URL come from the monitor.
type StatusPageMonitor struct {
	ID        int32
	Name      string
	Type      string
	URL       string
	SendURL   bool
	CustomURL *string
}

// Incident is a message on a status page.
type Incident struct {
	ID           int32
	StatusPageID int32
	Title        string
	Content      string
	Style        string
	Pin          bool
	Active       bool
	CreatedAt    time.Time
	UpdatedAt    *time.Time
}

// IncidentCursor is where a page of incident history continues: before this incident.
type IncidentCursor struct {
	CreatedAt time.Time
	ID        int32
}

const statusPageColumns = `p.id, p.slug, p.title, p.description, p.icon, p.theme, p.auto_refresh_interval, p.show_tags,
	p.footer_text, p.custom_css, p.show_powered_by, p.show_certificate_expiry, p.show_only_last_heartbeat, p.rss_title,
	p.analytics_type, p.analytics_id, p.analytics_script_url,
	COALESCE((SELECT array_agg(d.domain ORDER BY d.domain) FROM status_page_domain d WHERE d.status_page_id = p.id), '{}'),
	p.updated_at`

func scanStatusPage(row pgx.Row) (StatusPage, error) {
	var p StatusPage
	err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Description, &p.Icon, &p.Theme, &p.AutoRefreshInterval, &p.ShowTags,
		&p.FooterText, &p.CustomCSS, &p.ShowPoweredBy, &p.ShowCertificateExpiry, &p.ShowOnlyLastHeartbeat, &p.RSSTitle,
		&p.AnalyticsType, &p.AnalyticsID, &p.AnalyticsScriptURL, &p.Domains, &p.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if err != nil {
		return p, fmt.Errorf("read status page: %w", err)
	}
	return p, nil
}

func statusPageError(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation && pgErr.ConstraintName == "status_page_slug_key" {
		return ErrSlugTaken
	}
	return err
}

// ListStatusPages returns every status page, by title.
func (s *Store) ListStatusPages(ctx context.Context) ([]StatusPage, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+statusPageColumns+` FROM status_page p ORDER BY p.title, p.id`)
	if err != nil {
		return nil, fmt.Errorf("list status pages: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatusPage, error) { return scanStatusPage(r) })
}

// StatusPageBySlug returns a status page or ErrNotFound.
func (s *Store) StatusPageBySlug(ctx context.Context, slug string) (StatusPage, error) {
	return scanStatusPage(s.pool.QueryRow(ctx, `SELECT `+statusPageColumns+` FROM status_page p WHERE p.slug = $1`, slug))
}

// CreateStatusPage adds a status page with the defaults of the Node.js backend.
func (s *Store) CreateStatusPage(ctx context.Context, title, slug string) (StatusPage, error) {
	var id int32
	err := s.pool.QueryRow(ctx, `INSERT INTO status_page (title, slug) VALUES ($1, $2) RETURNING id`, title, slug).Scan(&id)
	if err != nil {
		if errors.Is(statusPageError(err), ErrSlugTaken) {
			return StatusPage{}, ErrSlugTaken
		}
		return StatusPage{}, fmt.Errorf("create status page: %w", err)
	}
	return scanStatusPage(s.pool.QueryRow(ctx, `SELECT `+statusPageColumns+` FROM status_page p WHERE p.id = $1`, id))
}

// SaveStatusPage replaces the settings, domains and groups of the page that has slug, which
// page.Slug may change. A domain moves here from any other page that had it. Groups without an
// id, or with one that isn't this page's, are new. Monitors that no longer exist are left out.
// It returns the groups with their ids.
func (s *Store) SaveStatusPage(ctx context.Context, slug string, page StatusPage, groups []StatusPageGroup) ([]StatusPageGroup, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	var id int32
	err = tx.QueryRow(ctx, `
		UPDATE status_page SET slug = $2, title = $3, description = $4, icon = $5, theme = $6, auto_refresh_interval = $7,
			show_tags = $8, footer_text = $9, custom_css = $10, show_powered_by = $11, show_certificate_expiry = $12,
			show_only_last_heartbeat = $13, rss_title = $14, analytics_type = $15, analytics_id = $16,
			analytics_script_url = $17, updated_at = now()
		WHERE slug = $1 RETURNING id`,
		slug, page.Slug, page.Title, page.Description, page.Icon, page.Theme, page.AutoRefreshInterval, page.ShowTags,
		page.FooterText, page.CustomCSS, page.ShowPoweredBy, page.ShowCertificateExpiry, page.ShowOnlyLastHeartbeat,
		page.RSSTitle, page.AnalyticsType, page.AnalyticsID, page.AnalyticsScriptURL).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if errors.Is(statusPageError(err), ErrSlugTaken) {
			return nil, ErrSlugTaken
		}
		return nil, fmt.Errorf("save status page: %w", err)
	}

	domains := page.Domains
	if domains == nil {
		domains = []string{}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM status_page_domain WHERE status_page_id = $1 OR domain = ANY($2)`, id, domains); err != nil {
		return nil, fmt.Errorf("clear domains: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO status_page_domain (domain, status_page_id) SELECT DISTINCT d, $1::int FROM unnest($2::text[]) d`, id, domains); err != nil {
		return nil, fmt.Errorf("save domains: %w", err)
	}

	kept := []int32{}
	saved := make([]StatusPageGroup, len(groups))
	for i, group := range groups {
		var groupID int32
		err := tx.QueryRow(ctx, `
			UPDATE status_page_group SET name = $3, weight = $4 WHERE id = $1 AND status_page_id = $2 RETURNING id`,
			group.ID, id, group.Name, i+1).Scan(&groupID)
		if errors.Is(err, pgx.ErrNoRows) {
			err = tx.QueryRow(ctx, `INSERT INTO status_page_group (status_page_id, name, weight) VALUES ($1, $2, $3) RETURNING id`,
				id, group.Name, i+1).Scan(&groupID)
		}
		if err != nil {
			return nil, fmt.Errorf("save group: %w", err)
		}
		if _, err := tx.Exec(ctx, `DELETE FROM status_page_monitor WHERE group_id = $1`, groupID); err != nil {
			return nil, fmt.Errorf("clear group monitors: %w", err)
		}
		seen := map[int32]bool{}
		for j, m := range group.Monitors {
			if seen[m.ID] {
				continue
			}
			seen[m.ID] = true
			if _, err := tx.Exec(ctx, `
				INSERT INTO status_page_monitor (group_id, monitor_id, weight, send_url, custom_url)
				SELECT $1::int, $2::int, $3::int, $4::bool, $5::text WHERE EXISTS (SELECT 1 FROM monitor WHERE id = $2)`,
				groupID, m.ID, j+1, m.SendURL, m.CustomURL); err != nil {
				return nil, fmt.Errorf("save group monitor: %w", err)
			}
		}
		kept = append(kept, groupID)
		saved[i] = StatusPageGroup{ID: groupID, Name: group.Name}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM status_page_group WHERE status_page_id = $1 AND id <> ALL($2)`, id, kept); err != nil {
		return nil, fmt.Errorf("delete groups: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit: %w", err)
	}
	return s.StatusPageGroups(ctx, id)
}

// DeleteStatusPage removes a status page with its groups, incidents and domains.
func (s *Store) DeleteStatusPage(ctx context.Context, slug string) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM status_page WHERE slug = $1`, slug)
	if err != nil {
		return fmt.Errorf("delete status page: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// StatusPageDomains maps each custom domain to its page's slug.
func (s *Store) StatusPageDomains(ctx context.Context) (map[string]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT d.domain, p.slug FROM status_page_domain d JOIN status_page p ON p.id = d.status_page_id`)
	if err != nil {
		return nil, fmt.Errorf("read domains: %w", err)
	}
	defer rows.Close()
	domains := map[string]string{}
	for rows.Next() {
		var domain, slug string
		if err := rows.Scan(&domain, &slug); err != nil {
			return nil, fmt.Errorf("read domain: %w", err)
		}
		domains[domain] = slug
	}
	return domains, rows.Err()
}

// SetStatusPageLogo stores a page's uploaded logo.
func (s *Store) SetStatusPageLogo(ctx context.Context, pageID int32, image []byte) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO status_page_logo (status_page_id, image) VALUES ($1, $2)
		ON CONFLICT (status_page_id) DO UPDATE SET image = $2, updated_at = now()`, pageID, image)
	if err != nil {
		return fmt.Errorf("save logo: %w", err)
	}
	return nil
}

// DeleteStatusPageLogo removes a page's uploaded logo, if it has one.
func (s *Store) DeleteStatusPageLogo(ctx context.Context, pageID int32) error {
	if _, err := s.pool.Exec(ctx, `DELETE FROM status_page_logo WHERE status_page_id = $1`, pageID); err != nil {
		return fmt.Errorf("delete logo: %w", err)
	}
	return nil
}

// StatusPageLogo returns a page's uploaded logo or ErrNotFound.
func (s *Store) StatusPageLogo(ctx context.Context, pageID int32) ([]byte, time.Time, error) {
	var image []byte
	var updated time.Time
	err := s.pool.QueryRow(ctx, `SELECT image, updated_at FROM status_page_logo WHERE status_page_id = $1`, pageID).Scan(&image, &updated)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, time.Time{}, ErrNotFound
	}
	if err != nil {
		return nil, time.Time{}, fmt.Errorf("read logo: %w", err)
	}
	return image, updated, nil
}

// StatusPageGroups returns a page's groups in order, with their monitors in order.
func (s *Store) StatusPageGroups(ctx context.Context, pageID int32) ([]StatusPageGroup, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT g.id, g.name, m.id, m.name, m.type, m.url, spm.send_url, spm.custom_url
		FROM status_page_group g
		LEFT JOIN status_page_monitor spm ON spm.group_id = g.id
		LEFT JOIN monitor m ON m.id = spm.monitor_id
		WHERE g.status_page_id = $1
		ORDER BY g.weight, g.id, spm.weight, m.id`, pageID)
	if err != nil {
		return nil, fmt.Errorf("read groups: %w", err)
	}
	defer rows.Close()
	groups := []StatusPageGroup{}
	for rows.Next() {
		var groupID int32
		var groupName string
		var monitorID *int32
		var name, kind, url *string
		var sendURL *bool
		var customURL *string
		if err := rows.Scan(&groupID, &groupName, &monitorID, &name, &kind, &url, &sendURL, &customURL); err != nil {
			return nil, fmt.Errorf("read group: %w", err)
		}
		if len(groups) == 0 || groups[len(groups)-1].ID != groupID {
			groups = append(groups, StatusPageGroup{ID: groupID, Name: groupName, Monitors: []StatusPageMonitor{}})
		}
		if monitorID != nil {
			group := &groups[len(groups)-1]
			group.Monitors = append(group.Monitors, StatusPageMonitor{
				ID: *monitorID, Name: *name, Type: *kind, URL: *url, SendURL: *sendURL, CustomURL: customURL,
			})
		}
	}
	return groups, rows.Err()
}

// StatusPageMonitorIDs returns the monitors shown on a page.
func (s *Store) StatusPageMonitorIDs(ctx context.Context, pageID int32) ([]int32, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT spm.monitor_id FROM status_page_monitor spm
		JOIN status_page_group g ON g.id = spm.group_id WHERE g.status_page_id = $1 ORDER BY 1`, pageID)
	if err != nil {
		return nil, fmt.Errorf("read page monitors: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[int32])
}

// IsMonitorPublic reports whether a monitor is on any status page, which makes its badges public.
func (s *Store) IsMonitorPublic(ctx context.Context, monitorID int32) (bool, error) {
	var public bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM status_page_monitor WHERE monitor_id = $1)`, monitorID).Scan(&public)
	if err != nil {
		return false, fmt.Errorf("check public monitor: %w", err)
	}
	return public, nil
}

// RecentBeats returns up to limit of the newest beats of each monitor, oldest first.
func (s *Store) RecentBeats(ctx context.Context, monitorIDs []int32, limit int) (map[int32][]Beat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT b.monitor_id, b.time, b.status, b.ping, b.msg, b.important, b.duration, b.retries, COALESCE(b.trace_id, ''), b.down_count
		FROM unnest($1::int[]) AS ids(id)
		CROSS JOIN LATERAL (
			SELECT * FROM heartbeat h WHERE h.monitor_id = ids.id ORDER BY h.time DESC LIMIT $2
		) b
		ORDER BY b.monitor_id, b.time`, monitorIDs, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent beats: %w", err)
	}
	beats, err := collectBeats(rows)
	if err != nil {
		return nil, err
	}
	out := make(map[int32][]Beat, len(monitorIDs))
	for _, b := range beats {
		out[b.MonitorID] = append(out[b.MonitorID], b)
	}
	return out, nil
}

// UptimeTotals are the counts of a period, as the uptime numbers use them.
type UptimeTotals struct {
	Up, Down  int64
	PingSum   float64
	PingCount int64
}

// UptimeOver adds up the last n minutes, hours or days of a monitor, including the current one.
// With nothing in that time, it uses the newest bucket before it, as the Node.js backend does,
// so that a paused monitor keeps its last numbers.
func (s *Store) UptimeOver(ctx context.Context, monitorID int32, unit string, n int) (UptimeTotals, error) {
	var query string
	switch unit {
	case "minute":
		query = `
			WITH buckets AS (
				SELECT date_trunc('minute', time) AS bucket,
				       count(*) FILTER (WHERE status = 1) AS up, count(*) FILTER (WHERE status IN (0, 2)) AS down,
				       COALESCE(sum(ping) FILTER (WHERE status = 1), 0) AS ping_sum, count(ping) FILTER (WHERE status = 1) AS ping_count
				FROM heartbeat WHERE monitor_id = $1 AND time >= now() - INTERVAL '1 day' GROUP BY 1
			)`
	case "hour":
		query = `WITH buckets AS (SELECT bucket, up, down, ping_sum, ping_count FROM uptime_hourly WHERE monitor_id = $1)`
	case "day":
		query = `WITH buckets AS (SELECT bucket, up, down, ping_sum, ping_count FROM uptime_daily WHERE monitor_id = $1)`
	default:
		return UptimeTotals{}, fmt.Errorf("unknown unit %q", unit)
	}
	query += `,
		period AS (
			SELECT sum(up)::bigint AS up, sum(down)::bigint AS down, sum(ping_sum) AS ping_sum, sum(ping_count)::bigint AS ping_count
			FROM buckets WHERE bucket >= date_trunc($2, now(), 'UTC') - ($3::int - 1) * ('1 ' || $2)::interval
			HAVING sum(up) + sum(down) > 0
		), last AS (
			SELECT up::bigint, down::bigint, ping_sum, ping_count::bigint FROM buckets
			WHERE up + down > 0 ORDER BY bucket DESC LIMIT 1
		)
		SELECT up, down, COALESCE(ping_sum, 0), ping_count FROM period
		UNION ALL
		SELECT up, down, COALESCE(ping_sum, 0), ping_count FROM last WHERE NOT EXISTS (SELECT 1 FROM period)`
	var t UptimeTotals
	err := s.pool.QueryRow(ctx, query, monitorID, unit, n).Scan(&t.Up, &t.Down, &t.PingSum, &t.PingCount)
	if errors.Is(err, pgx.ErrNoRows) {
		return UptimeTotals{}, nil
	}
	if err != nil {
		return t, fmt.Errorf("query uptime: %w", err)
	}
	return t, nil
}

const incidentColumns = `id, status_page_id, title, content, style, pin, active, created_at, updated_at`

func collectIncidents(rows pgx.Rows) ([]Incident, error) {
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (Incident, error) {
		var i Incident
		err := r.Scan(&i.ID, &i.StatusPageID, &i.Title, &i.Content, &i.Style, &i.Pin, &i.Active, &i.CreatedAt, &i.UpdatedAt)
		return i, err
	})
}

// PinnedIncidents returns a page's pinned, active incidents, newest first.
func (s *Store) PinnedIncidents(ctx context.Context, pageID int32) ([]Incident, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+` FROM incident
		WHERE status_page_id = $1 AND pin AND active ORDER BY created_at DESC, id DESC`, pageID)
	if err != nil {
		return nil, fmt.Errorf("read incidents: %w", err)
	}
	return collectIncidents(rows)
}

// IncidentHistory returns up to limit of a page's incidents, newest first, from after the
// cursor, with the total and whether there are more.
func (s *Store) IncidentHistory(ctx context.Context, pageID int32, cursor *IncidentCursor, limit int) ([]Incident, int64, bool, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM incident WHERE status_page_id = $1`, pageID).Scan(&total); err != nil {
		return nil, 0, false, fmt.Errorf("count incidents: %w", err)
	}
	var before *time.Time
	var beforeID int32
	if cursor != nil {
		before, beforeID = &cursor.CreatedAt, cursor.ID
	}
	rows, err := s.pool.Query(ctx, `SELECT `+incidentColumns+` FROM incident
		WHERE status_page_id = $1 AND ($2::timestamptz IS NULL OR (created_at, id) < ($2, $3))
		ORDER BY created_at DESC, id DESC LIMIT $4`, pageID, before, beforeID, limit+1)
	if err != nil {
		return nil, 0, false, fmt.Errorf("read incident history: %w", err)
	}
	incidents, err := collectIncidents(rows)
	if err != nil {
		return nil, 0, false, err
	}
	more := len(incidents) > limit
	if more {
		incidents = incidents[:limit]
	}
	return incidents, total, more, nil
}

// PostIncident creates an incident, or replaces one of the page's by id, pinned and active.
func (s *Store) PostIncident(ctx context.Context, i Incident) (Incident, error) {
	if i.ID != 0 {
		rows, err := s.pool.Query(ctx, `UPDATE incident SET title = $3, content = $4, style = $5, pin = true, active = true,
			updated_at = now() WHERE id = $1 AND status_page_id = $2 RETURNING `+incidentColumns,
			i.ID, i.StatusPageID, i.Title, i.Content, i.Style)
		if err != nil {
			return i, fmt.Errorf("update incident: %w", err)
		}
		updated, err := collectIncidents(rows)
		if err != nil {
			return i, err
		}
		if len(updated) == 1 {
			return updated[0], nil
		}
	}
	rows, err := s.pool.Query(ctx, `INSERT INTO incident (status_page_id, title, content, style) VALUES ($1, $2, $3, $4)
		RETURNING `+incidentColumns, i.StatusPageID, i.Title, i.Content, i.Style)
	if err != nil {
		return i, fmt.Errorf("create incident: %w", err)
	}
	created, err := collectIncidents(rows)
	if err != nil || len(created) != 1 {
		return i, fmt.Errorf("create incident: %w", err)
	}
	return created[0], nil
}

// EditIncident changes one of a page's incidents, or returns ErrNotFound.
func (s *Store) EditIncident(ctx context.Context, i Incident) (Incident, error) {
	return s.oneIncident(ctx, `UPDATE incident SET title = $3, content = $4, style = $5, pin = $6, updated_at = now()
		WHERE id = $1 AND status_page_id = $2 RETURNING `+incidentColumns, i.ID, i.StatusPageID, i.Title, i.Content, i.Style, i.Pin)
}

// ResolveIncident makes one of a page's incidents inactive and unpinned, or returns ErrNotFound.
func (s *Store) ResolveIncident(ctx context.Context, pageID, id int32) (Incident, error) {
	return s.oneIncident(ctx, `UPDATE incident SET active = false, pin = false, updated_at = now()
		WHERE id = $1 AND status_page_id = $2 RETURNING `+incidentColumns, id, pageID)
}

func (s *Store) oneIncident(ctx context.Context, query string, args ...any) (Incident, error) {
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return Incident{}, fmt.Errorf("update incident: %w", err)
	}
	incidents, err := collectIncidents(rows)
	if err != nil {
		return Incident{}, fmt.Errorf("update incident: %w", err)
	}
	if len(incidents) == 0 {
		return Incident{}, ErrNotFound
	}
	return incidents[0], nil
}

// DeleteIncident removes one of a page's incidents, or returns ErrNotFound.
func (s *Store) DeleteIncident(ctx context.Context, pageID, id int32) error {
	tag, err := s.pool.Exec(ctx, `DELETE FROM incident WHERE id = $1 AND status_page_id = $2`, id, pageID)
	if err != nil {
		return fmt.Errorf("delete incident: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// UnpinIncidents unpins every incident of a page.
func (s *Store) UnpinIncidents(ctx context.Context, pageID int32) error {
	if _, err := s.pool.Exec(ctx, `UPDATE incident SET pin = false WHERE status_page_id = $1 AND pin`, pageID); err != nil {
		return fmt.Errorf("unpin incidents: %w", err)
	}
	return nil
}

// SetMaintenanceStatusPages replaces the status pages that show a maintenance window. Pages that
// don't exist are left out.
func (s *Store) SetMaintenanceStatusPages(ctx context.Context, maintenanceID int32, pageIDs []int32) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM maintenance WHERE id = $1)`, maintenanceID).Scan(&exists); err != nil {
		return fmt.Errorf("read maintenance: %w", err)
	}
	if !exists {
		return ErrNotFound
	}
	if _, err := tx.Exec(ctx, `DELETE FROM maintenance_status_page WHERE maintenance_id = $1`, maintenanceID); err != nil {
		return fmt.Errorf("clear status pages: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO maintenance_status_page (maintenance_id, status_page_id)
		SELECT DISTINCT $1::int, p.id FROM status_page p WHERE p.id = ANY($2)`, maintenanceID, pageIDs); err != nil {
		return fmt.Errorf("link status pages: %w", err)
	}
	return tx.Commit(ctx)
}

// MaintenanceStatusPages returns the ids and titles of the pages that show a window.
func (s *Store) MaintenanceStatusPages(ctx context.Context, maintenanceID int32) ([]StatusPage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.title FROM maintenance_status_page msp JOIN status_page p ON p.id = msp.status_page_id
		WHERE msp.maintenance_id = $1 ORDER BY p.title, p.id`, maintenanceID)
	if err != nil {
		return nil, fmt.Errorf("read maintenance status pages: %w", err)
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (StatusPage, error) {
		var p StatusPage
		err := r.Scan(&p.ID, &p.Title)
		return p, err
	})
}

// StatusPageMaintenanceIDs returns the maintenance windows linked to a page.
func (s *Store) StatusPageMaintenanceIDs(ctx context.Context, pageID int32) ([]int32, error) {
	rows, err := s.pool.Query(ctx, `SELECT maintenance_id FROM maintenance_status_page WHERE status_page_id = $1 ORDER BY 1`, pageID)
	if err != nil {
		return nil, fmt.Errorf("read page maintenance: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowTo[int32])
}
