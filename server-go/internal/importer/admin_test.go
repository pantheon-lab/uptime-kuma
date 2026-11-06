package importer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Made with better-auth's own symmetricEncrypt, for the key abcdefghijklmnopqrstuvwxyzABCDEF.
const (
	testAuthSecret     = "test-auth-secret-from-db-config"
	encryptedTOTPKey   = "e898d698c64d6abab32d4fee19a84f588ab541998d86f99bddd39286bce455373ba3b290e2a4e9055341275bcb785b530ae417f0e1d6c481b163bd4be461ca4245aaeb87ce56db99"
	decryptedTOTPKey   = "abcdefghijklmnopqrstuvwxyzABCDEF"
	betterAuthPassword = "0123456789abcdef0123456789abcdef:abcd"
)

func (f *fixture) runWith(t *testing.T, apply bool, configure func(*Options)) *Report {
	t.Helper()
	options := Options{Source: f.source, Log: quiet, Now: func() time.Time { return f.now }}
	if apply {
		options.Target = f.target
	}
	if configure != nil {
		configure(&options)
	}
	report, err := Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func TestDecryptTOTPSecret(t *testing.T) {
	key, err := decryptTOTPSecret(testAuthSecret, encryptedTOTPKey)
	if err != nil || string(key) != decryptedTOTPKey {
		t.Fatalf("%q %v", key, err)
	}
	if key, err := decryptTOTPSecret(testAuthSecret, "$ba$1$"+encryptedTOTPKey); err != nil || string(key) != decryptedTOTPKey {
		t.Fatalf("in an envelope: %q %v", key, err)
	}
	if _, err := decryptTOTPSecret("another secret", encryptedTOTPKey); err == nil || !strings.Contains(err.Error(), "SOURCE_AUTH_SECRET") {
		t.Fatalf("a wrong secret: %v", err)
	}
	if _, err := decryptTOTPSecret(testAuthSecret, "not hex"); err == nil {
		t.Fatal("not hex was decrypted")
	}
}

func TestImportsTheAccount(t *testing.T) {
	f := newFixture(t)
	f.exec(t, "INSERT INTO better_auth_user (id, name, email, emailVerified, createdAt, updatedAt, username, twoFactorEnabled) "+
		"VALUES ('u1', 'admin', 'admin@noreply.uptime-kuma.internal', 0, '2026-01-01', '2026-01-01', 'admin', 1)")
	f.exec(t, "INSERT INTO better_auth_account (id, userId, accountId, providerId, password, createdAt, updatedAt) "+
		"VALUES ('a1', 'u1', 'u1', 'credential', ?, '2026-01-01', '2026-01-01')", betterAuthPassword)
	f.exec(t, "INSERT INTO better_auth_twoFactor (id, secret, backupCodes, verified, userId) VALUES ('t1', ?, '', 1, 'u1')", encryptedTOTPKey)

	// Without the secret, the account would lose its second factor, so it isn't imported.
	report := f.runWith(t, true, nil)
	if report.Account.Imported || !strings.Contains(report.Account.Skipped, "SOURCE_AUTH_SECRET") || !report.Account.TwoFactor {
		t.Fatalf("without the secret: %+v", report.Account)
	}
	report = f.runWith(t, true, func(o *Options) { o.AuthSecret = "wrong" })
	if report.Account.Imported || !strings.Contains(report.Account.Skipped, "can't be decrypted") {
		t.Fatalf("a wrong secret: %+v", report.Account)
	}
	if n := f.count(t, `SELECT count(*) FROM app_user`); n != 0 {
		t.Fatalf("%d accounts after refusals", n)
	}

	if report := f.runWith(t, false, func(o *Options) { o.AuthSecret = testAuthSecret }); report.Account.Imported || report.Account.Username != "admin" {
		t.Fatalf("dry run: %+v", report.Account)
	}
	report = f.runWith(t, true, func(o *Options) { o.AuthSecret = testAuthSecret })
	if !report.Account.Imported || report.Account.Source != 1 || !report.Account.TwoFactor {
		t.Fatalf("import: %+v", report.Account)
	}
	var hash string
	var secret []byte
	var enabled bool
	if err := f.targetSQL.QueryRow(context.Background(), `SELECT password_hash, totp_secret, totp_enabled FROM app_user WHERE username = 'admin'`).
		Scan(&hash, &secret, &enabled); err != nil {
		t.Fatal(err)
	}
	if hash != betterAuthPassword || string(secret) != decryptedTOTPKey || !enabled {
		t.Fatalf("stored %q %q %v", hash, secret, enabled)
	}
	if report := f.runWith(t, true, func(o *Options) { o.AuthSecret = testAuthSecret }); report.Account.Imported || report.Account.Skipped != "an account exists here already" {
		t.Fatalf("again: %+v", report.Account)
	}
}

func TestImportsTheAccountBeforeBetterAuth(t *testing.T) {
	f := newFixture(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("old password"), 10)
	if err != nil {
		t.Fatal(err)
	}
	f.exec(t, "INSERT INTO `user` (username, password, active, twofa_secret, twofa_status) VALUES ('old', ?, 1, 'legacysecret', 1)", string(hash))
	report := f.runWith(t, true, nil)
	if !report.Account.Imported || report.Account.Username != "old" || !report.Account.TwoFactor {
		t.Fatalf("import: %+v", report.Account)
	}
	var stored string
	var secret []byte
	if err := f.targetSQL.QueryRow(context.Background(), `SELECT password_hash, totp_secret FROM app_user`).Scan(&stored, &secret); err != nil {
		t.Fatal(err)
	}
	if stored != string(hash) || string(secret) != "legacysecret" {
		t.Fatalf("stored %q %q", stored, secret)
	}
}

var testPNG = []byte("\x89PNG\r\n\x1a\nnot really an image")

func TestImportsStatusPagesAndMaintenance(t *testing.T) {
	f := newFixture(t)
	f.seedMonitors(t)
	dataDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dataDir, "upload"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "upload", "logo1.png"), testPNG, 0o600); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "INSERT INTO setting (`key`, value, type) VALUES ('serverTimezone', '\"Asia/Hong_Kong\"', 'general')")
	f.exec(t, `INSERT INTO status_page (id, slug, title, description, icon, theme, footer_text, show_tags, show_powered_by,
		auto_refresh_interval, analytics_type, analytics_id, rss_title, created_date)
		VALUES (1, 'main', 'Main', 'All services', '/upload/logo1.png?t=123', 'dark', 'Footer', 1, 0, 60, 'umami', 'abc', 'Feed', '2025-03-01 10:00:00'),
		       (2, 'other', 'Other', NULL, 'https://example.com/logo.png', 'light', NULL, 0, 1, 300, NULL, NULL, NULL, '2025-03-02 10:00:00'),
		       (3, 'taken', 'Taken', NULL, '/icon.svg', 'auto', NULL, 0, 1, 300, NULL, NULL, NULL, '2025-03-03 10:00:00'),
		       (4, 'nologo', 'No logo', NULL, '/upload/logo4.png', 'auto', NULL, 0, 1, 300, NULL, NULL, NULL, '2025-03-03 10:00:00')`)
	f.exec(t, "INSERT INTO status_page_cname (status_page_id, domain) VALUES (1, 'Status.Example.com'), (2, 'used.example.com')")
	f.exec(t, "INSERT INTO `group` (id, name, status_page_id, public, active, weight) VALUES (1, 'Services', 1, 1, 1, 2), (2, 'Edge', 1, 1, 1, 1)")
	// Monitor 21 isn't imported, so it's left off the page.
	f.exec(t, "INSERT INTO monitor_group (monitor_id, group_id, weight, send_url, custom_url) VALUES (3, 1, 1, 1, 'https://status.example.com'), (21, 1, 2, 0, NULL), (4, 2, 1, 0, NULL)")
	f.exec(t, `INSERT INTO incident (title, content, style, created_date, last_updated_date, pin, active, status_page_id)
		VALUES ('Outage', 'It broke', 'danger', '2025-04-01 08:00:00', '2025-04-01 09:00:00', 1, 1, 1),
		       ('Old', 'Fixed', 'info', '2025-01-01 08:00:00', NULL, 0, 0, 1)`)
	f.exec(t, `INSERT INTO maintenance (id, title, description, active, strategy, start_date, end_date, start_time, end_time,
		weekdays, days_of_month, interval_day, cron, timezone, duration)
		VALUES (1, 'Upgrade', 'Database', 1, 'single', '2026-12-01 22:00:00', '2026-12-02 01:00:00', NULL, NULL, '[]', '[]', NULL, NULL, 'SAME_AS_SERVER', NULL),
		       (2, 'Weekly', '', 1, 'recurring-weekday', NULL, NULL, '02:00:00', '03:00:00', '[1,3]', '[]', 1, '0 2 * * 1,3', 'UTC', 3600),
		       (3, 'Monthly', '', 0, 'recurring-day-of-month', '2026-01-01 00:00:00', NULL, '23:30:00', '00:30:00', '[]', '["lastDay1",15]', 1, '30 23 L,15 * *', 'Europe/Berlin', 3600),
		       (4, 'Cron', '', 1, 'cron', NULL, NULL, NULL, NULL, '[]', '[]', 1, '0 4 * * *', 'UTC', 5400),
		       (5, 'Backwards', '', 1, 'single', '2026-12-02 00:00:00', '2026-12-01 00:00:00', NULL, NULL, '[]', '[]', NULL, NULL, 'UTC', NULL)`)
	f.exec(t, "INSERT INTO monitor_maintenance (monitor_id, maintenance_id) VALUES (3, 1), (21, 1), (4, 2)")
	f.exec(t, "INSERT INTO maintenance_status_page (status_page_id, maintenance_id) VALUES (1, 1), (3, 1)")

	ctx := context.Background()
	if _, err := f.target.CreateStatusPage(ctx, "Here already", "taken"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.targetSQL.Exec(ctx, `INSERT INTO status_page_domain (domain, status_page_id) SELECT 'used.example.com', id FROM status_page`); err != nil {
		t.Fatal(err)
	}

	dry := f.runWith(t, false, func(o *Options) { o.DataDir = dataDir })
	if dry.StatusPages.Imported != 4 || dry.Maintenance.Imported != 4 || f.count(t, `SELECT count(*) FROM maintenance`) != 0 {
		t.Fatalf("dry run: %+v %+v", dry.StatusPages, dry.Maintenance)
	}

	report := f.runWith(t, true, func(o *Options) { o.DataDir = dataDir })
	pages := report.StatusPages
	if pages.Source != 4 || pages.Imported != 3 || len(pages.Skipped) != 1 || pages.Skipped[0].SourceID != 3 ||
		pages.Monitors != 2 || pages.MonitorsLeftOut != 1 || pages.Incidents != 2 || pages.Domains != 2 ||
		pages.DomainsTaken != 1 || pages.Logos != 1 || pages.LogosMissing != 1 {
		t.Fatalf("status pages: %+v", pages)
	}
	main, err := f.target.StatusPageBySlug(ctx, "main")
	if err != nil {
		t.Fatal(err)
	}
	if main.Title != "Main" || main.Description != "All services" || main.Theme != "dark" || main.FooterText != "Footer" ||
		!main.ShowTags || main.ShowPoweredBy || main.AutoRefreshInterval != 60 || main.RSSTitle != "Feed" ||
		*main.AnalyticsType != "umami" || *main.AnalyticsID != "abc" || !strings.HasPrefix(main.Icon, "/upload/logo") ||
		len(main.Domains) != 1 || main.Domains[0] != "status.example.com" {
		t.Fatalf("main page: %+v", main)
	}
	logo, _, err := f.target.StatusPageLogo(ctx, main.ID)
	if err != nil || string(logo) != string(testPNG) {
		t.Fatalf("logo %q %v", logo, err)
	}
	groups, err := f.target.StatusPageGroups(ctx, main.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[0].Name != "Edge" || groups[1].Name != "Services" || len(groups[1].Monitors) != 1 ||
		groups[1].Monitors[0].ID != f.targetID(t, 3) || !groups[1].Monitors[0].SendURL ||
		*groups[1].Monitors[0].CustomURL != "https://status.example.com" || groups[0].Monitors[0].ID != f.targetID(t, 4) {
		t.Fatalf("groups: %+v", groups)
	}
	incidents, err := f.target.PinnedIncidents(ctx, main.ID)
	if err != nil || len(incidents) != 1 || incidents[0].Title != "Outage" || incidents[0].Style != "danger" ||
		!incidents[0].CreatedAt.Equal(time.Date(2025, 4, 1, 8, 0, 0, 0, time.UTC)) || incidents[0].UpdatedAt == nil {
		t.Fatalf("incidents %+v %v", incidents, err)
	}
	if other, err := f.target.StatusPageBySlug(ctx, "other"); err != nil || other.Icon != "https://example.com/logo.png" || len(other.Domains) != 0 {
		t.Fatalf("other page: %+v %v", other, err)
	}
	if nologo, err := f.target.StatusPageBySlug(ctx, "nologo"); err != nil || nologo.Icon != "" {
		t.Fatalf("page whose logo is missing: %+v %v", nologo, err)
	}

	m := report.Maintenance
	if m.Source != 5 || m.Imported != 4 || len(m.Skipped) != 1 || m.Skipped[0].SourceID != 5 ||
		m.MonitorLinks != 2 || m.MonitorsLeftOut != 1 || m.StatusPageLinks != 1 || m.ServerTimezone != "Asia/Hong_Kong" {
		t.Fatalf("maintenance: %+v", m)
	}
	windows, err := f.target.ListMaintenances(ctx)
	if err != nil {
		t.Fatal(err)
	}
	byTitle := map[string]int{}
	for i, w := range windows {
		byTitle[w.Title] = i
	}
	upgrade, weekly, monthly, cron := windows[byTitle["Upgrade"]], windows[byTitle["Weekly"]], windows[byTitle["Monthly"]], windows[byTitle["Cron"]]
	if upgrade.StartDate != "2026-12-01T22:00" || upgrade.EndDate != "2026-12-02T01:00" || upgrade.Timezone != "Asia/Hong_Kong" ||
		upgrade.Description != "Database" || !upgrade.Active {
		t.Fatalf("single: %+v", upgrade)
	}
	if weekly.StartTime != "02:00" || weekly.EndTime != "03:00" || len(weekly.Weekdays) != 2 || weekly.Weekdays[1] != 3 ||
		weekly.Cron != "" || weekly.Timezone != "UTC" {
		t.Fatalf("weekly: %+v", weekly)
	}
	if monthly.Active || len(monthly.DaysOfMonth) != 2 || monthly.DaysOfMonth[0] != -1 || monthly.DaysOfMonth[1] != 15 ||
		monthly.StartDate != "2026-01-01T00:00" || monthly.EndDate != "" {
		t.Fatalf("monthly: %+v", monthly)
	}
	if cron.Cron != "0 4 * * *" || cron.DurationMinutes != 90 {
		t.Fatalf("cron: %+v", cron)
	}
	links, err := f.target.MaintenanceMonitorLinks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := links[upgrade.ID]; len(got) != 1 || got[0] != f.targetID(t, 3) {
		t.Fatalf("upgrade's monitors: %v", got)
	}
	if onPages, err := f.target.MaintenanceStatusPages(ctx, upgrade.ID); err != nil || len(onPages) != 1 || onPages[0].ID != main.ID {
		t.Fatalf("upgrade's pages: %+v %v", onPages, err)
	}

	// Again: nothing is copied twice, and changes here are kept.
	if _, err := f.targetSQL.Exec(ctx, `UPDATE status_page SET title = 'Edited here' WHERE slug = 'other'`); err != nil {
		t.Fatal(err)
	}
	again := f.runWith(t, true, func(o *Options) { o.DataDir = dataDir })
	if again.StatusPages.Imported != 0 || again.StatusPages.AlreadyImported != 3 || again.Maintenance.Imported != 0 ||
		again.Maintenance.AlreadyImported != 4 {
		t.Fatalf("again: %+v %+v", again.StatusPages, again.Maintenance)
	}
	if other, _ := f.target.StatusPageBySlug(ctx, "other"); other.Title != "Edited here" {
		t.Fatal("an import overwrote a change made here")
	}
	// Deleting one here copies it again.
	if err := f.target.DeleteStatusPage(ctx, "other"); err != nil {
		t.Fatal(err)
	}
	if again := f.runWith(t, true, nil); again.StatusPages.Imported != 1 {
		t.Fatalf("after deleting: %+v", again.StatusPages)
	}
	if other, _ := f.target.StatusPageBySlug(ctx, "other"); other.Title != "Other" {
		t.Fatalf("copied again: %+v", other)
	}
}

func TestImportsWebpushKeys(t *testing.T) {
	f := newFixture(t)
	f.exec(t, "INSERT INTO setting (`key`, value, type) VALUES ('webpushPublicVapidKey', '\"node-public\"', NULL), ('webpushPrivateVapidKey', '\"node-private\"', NULL)")
	f.exec(t, `INSERT INTO notification (id, name, active, is_default, config) VALUES
		(1, 'Browser', 1, 0, '{"type":"Webpush","subscription":{"endpoint":"https://push.example/abc","keys":{"p256dh":"x","auth":"y"}}}')`)
	if dry := f.runWith(t, false, nil); dry.Notifications.WebpushKeys != "imported" || dry.Notifications.Imported != 1 {
		t.Fatalf("dry run: %+v", dry.Notifications)
	}
	report := f.runWith(t, true, nil)
	if report.Notifications.WebpushKeys != "imported" || report.Notifications.Imported != 1 || len(report.Notifications.Skipped) != 0 {
		t.Fatalf("import: %+v", report.Notifications)
	}
	public, private, err := f.target.WebpushKeys(context.Background(), func() (string, string, error) { return "new", "new", nil })
	if err != nil || public != "node-public" || private != "node-private" {
		t.Fatalf("keys %s %s %v", public, private, err)
	}
	if again := f.runWith(t, true, nil); again.Notifications.WebpushKeys != "kept" {
		t.Fatalf("again: %+v", again.Notifications)
	}
	encoded, _ := json.Marshal(report)
	if strings.Contains(string(encoded), "node-private") {
		t.Fatal("the report holds the private key")
	}
}
