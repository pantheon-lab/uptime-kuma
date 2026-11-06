package importer

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	_ "embed"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"log/slog"
	"math"
	"math/big"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

//go:embed testdata/uptime-kuma-schema.sql
var sourceSchema string

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fixture struct {
	source    *sql.DB
	target    *store.Store
	targetSQL *pgx.Conn
	now       time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	sourceDSN, targetURL := os.Getenv("TEST_MARIADB_DSN"), os.Getenv("TEST_DATABASE_URL")
	if sourceDSN == "" || targetURL == "" {
		t.Skip("TEST_MARIADB_DSN and TEST_DATABASE_URL are not set")
	}
	ctx := context.Background()
	name := fmt.Sprintf("kuma_import_%d", time.Now().UnixNano())

	config, err := mysql.ParseDSN(sourceDSN)
	if err != nil {
		t.Fatal(err)
	}
	admin, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	config.DBName = name
	source, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	for _, statement := range strings.Split(sourceSchema, ";\n") {
		if strings.Contains(statement, "CREATE TABLE") {
			if _, err := source.ExecContext(ctx, statement); err != nil {
				t.Fatalf("%v\n%s", err, statement)
			}
		}
	}

	pgAdmin, err := pgx.Connect(ctx, targetURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pgAdmin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(targetURL)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	target, err := store.Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	if err := target.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	targetSQL, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = targetSQL.Close(ctx)
		target.Close()
		_, _ = pgAdmin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = pgAdmin.Close(ctx)
		_ = source.Close()
		_, _ = admin.ExecContext(ctx, "DROP DATABASE "+name)
		_ = admin.Close()
	})
	return &fixture{source: source, target: target, targetSQL: targetSQL, now: time.Now().UTC()}
}

func (f *fixture) exec(t *testing.T, query string, args ...any) {
	t.Helper()
	if _, err := f.source.Exec(query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

func (f *fixture) monitor(t *testing.T, id int, name, kind string, extra map[string]any) {
	t.Helper()
	columns := []string{"id", "name", "type", "`interval`"}
	values := []any{id, name, kind, 60}
	if _, set := extra["active"]; !set {
		columns = append(columns, "active")
		values = append(values, 1)
	}
	for column, value := range extra {
		columns = append(columns, "`"+column+"`")
		values = append(values, value)
	}
	marks := strings.TrimSuffix(strings.Repeat("?,", len(values)), ",")
	f.exec(t, "INSERT INTO monitor ("+strings.Join(columns, ",")+") VALUES ("+marks+")", values...)
}

func (f *fixture) run(t *testing.T, apply, activate bool) *Report {
	t.Helper()
	options := Options{Source: f.source, Activate: activate, Log: quiet, Now: func() time.Time { return f.now }}
	if apply {
		options.Target = f.target
	}
	report, err := Run(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	return report
}

func (f *fixture) count(t *testing.T, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := f.targetSQL.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
	return n
}

func (f *fixture) targetID(t *testing.T, sourceID int) int32 {
	t.Helper()
	return int32(f.count(t, `SELECT monitor_id FROM monitor_import WHERE source_id = $1`, sourceID))
}

func (f *fixture) seedMonitors(t *testing.T) {
	t.Helper()
	f.monitor(t, 1, "Top", "group", nil)
	f.monitor(t, 2, "Inner", "group", map[string]any{"parent": 1})
	f.monitor(t, 3, "Site", "http", map[string]any{
		"parent": 2, "url": "https://example.com/health", "method": "post", "body": `{"a":1}`, "http_body_encoding": "json",
		"headers": `{"X-Key":"abc","X-Number":5}`, "accepted_statuscodes_json": `["200-299","404"]`, "maxredirects": 3,
		"ignore_tls": 1, "upside_down": 0, "retry_interval": 20, "maxretries": 2, "timeout": 12.5,
	})
	f.monitor(t, 4, "Basic", "http", map[string]any{"url": "https://example.com", "auth_method": "basic", "basic_auth_user": "u", "basic_auth_pass": "p:w"})
	f.monitor(t, 5, "Bearer", "http", map[string]any{"url": "https://example.com", "auth_method": "bearer", "bearer_token": "tok", "headers": `{"X-A":"b"}`})
	f.monitor(t, 6, "Keyword", "keyword", map[string]any{"url": "https://example.com", "keyword": "ok", "invert_keyword": 1, "active": 0})
	f.monitor(t, 7, "Port", "port", map[string]any{"hostname": "db.internal", "port": 5432, "url": "https://", "upside_down": 1, "parent": 1})
	// Dns and Ping import now; Steam has no Go implementation.
	f.monitor(t, 10, "Dns", "dns", map[string]any{"hostname": "example.com"})
	f.monitor(t, 11, "Ping", "ping", map[string]any{"hostname": "example.com"})
	f.monitor(t, 21, "Game", "lighthouse", nil)
	f.monitor(t, 12, "Mtls", "http", map[string]any{"url": "https://example.com", "auth_method": "mtls"})
	f.monitor(t, 13, "Oauth", "http", map[string]any{"url": "https://example.com", "auth_method": "oauth2-cc"})
	f.monitor(t, 14, "Ntlm", "http", map[string]any{"url": "https://example.com", "auth_method": "ntlm"})
	f.monitor(t, 15, "BadHeaders", "http", map[string]any{"url": "https://example.com", "headers": "not json"})
	f.monitor(t, 16, "BadUrl", "http", map[string]any{"url": "ftp://example.com"})
	f.monitor(t, 17, "BadBody", "http", map[string]any{"url": "https://example.com", "body": "{"})
	f.monitor(t, 19, "Proxied", "http", map[string]any{"url": "https://example.com", "proxy_id": 1})
	// These lose something that doesn't change a check's result, which gets reported.
	f.monitor(t, 18, "Dropped", "http", map[string]any{"url": "https://example.com", "cache_bust": 1, "expiry_notification": 1, "resend_interval": 5, "description": "x"})
	f.exec(t, "INSERT INTO monitor_notification (monitor_id, notification_id) VALUES (3, 1), (3, 2), (4, 1)")
}

func TestDryRunWritesNothing(t *testing.T) {
	f := newFixture(t)
	f.seedMonitors(t)
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (3, 1, UTC_TIMESTAMP(), 'ok')")
	report := f.run(t, false, false)
	if !report.DryRun || report.Imported != 10 || report.Beats != 1 || report.NewBeats != 0 {
		t.Errorf("report: %+v", report)
	}
	if n := f.count(t, `SELECT count(*) FROM monitor`); n != 0 {
		t.Errorf("a dry run created %d monitors", n)
	}
}

func TestImportsMonitorsFaithfully(t *testing.T) {
	f := newFixture(t)
	f.seedMonitors(t)
	report := f.run(t, true, false)

	if report.SourceMonitors != 18 || report.Imported != 10 || len(report.Skipped) != 8 {
		t.Errorf("counts: source %d imported %d skipped %d: %+v", report.SourceMonitors, report.Imported, len(report.Skipped), report.Skipped)
	}
	reasons := map[string]string{}
	for _, s := range report.Skipped {
		reasons[s.Name] = s.Reason
	}
	for name, want := range map[string]string{
		"Game": "unsupported monitor type: lighthouse",
		"Mtls": "invalid setting: an mTLS login needs tlsCert and tlsKey", "Oauth": "invalid setting: oauthTokenUrl must be an http or https URL",
		"Ntlm": "invalid setting: an NTLM login needs basicAuthUser", "BadHeaders": "invalid setting: headers aren't a valid JSON object",
		"BadUrl": "invalid setting: url must be a valid http or https URL", "BadBody": "invalid setting: body is not valid JSON",
		"Proxied": "invalid setting: the proxy doesn't exist",
	} {
		if reasons[name] != want {
			t.Errorf("%s: %q, want %q", name, reasons[name], want)
		}
	}
	if report.SkippedByReason["unsupported monitor type"] != 1 {
		t.Errorf("by reason: %+v", report.SkippedByReason)
	}
	if len(report.DroppedSettings) != 0 || report.WithAlerts != 2 {
		t.Errorf("reported losses: %+v, alerts %d", report.DroppedSettings, report.WithAlerts)
	}

	ctx := context.Background()
	get := func(sourceID int) monitor.Monitor {
		t.Helper()
		m, err := f.target.GetMonitor(ctx, f.targetID(t, sourceID))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	site := get(3)
	if site.Active || site.Type != "http" || site.URL != "https://example.com/health" || site.Method != "POST" || site.Body != `{"a":1}` ||
		site.Headers["X-Key"] != "abc" || site.Headers["X-Number"] != "5" || len(site.AcceptedStatusCodes) != 2 || site.MaxRedirects != 3 ||
		!site.IgnoreTLS || site.RetryInterval != 20 || site.MaxRetries != 2 || site.Timeout != 12.5 || site.Interval != 60 {
		t.Errorf("site: %+v", site)
	}
	inner := f.targetID(t, 2)
	if site.ParentID == nil || *site.ParentID != inner {
		t.Errorf("site's parent: %v, want %d", site.ParentID, inner)
	}
	if p := get(2).ParentID; p == nil || *p != f.targetID(t, 1) {
		t.Errorf("nested group's parent: %v", p)
	}
	if b := get(4); b.AuthMethod != "basic" || b.BasicAuthUser != "u" || b.BasicAuthPass != "p:w" || b.Headers["Authorization"] != "" {
		t.Errorf("basic: %+v", b.Settings)
	}
	if b := get(5); b.AuthMethod != "bearer" || b.BearerToken != "tok" || b.Headers["X-A"] != "b" {
		t.Errorf("bearer: %+v %+v", b.Headers, b.Settings)
	}
	if d := get(18); !d.CacheBust || d.Description != "x" {
		t.Errorf("options: %+v", d.Settings)
	}
	if k := get(6); k.Type != "keyword" || k.Keyword != "ok" || !k.InvertKeyword {
		t.Errorf("keyword: %+v", k)
	}
	if p := get(7); p.Hostname != "db.internal" || p.Port != 5432 || !p.UpsideDown || p.URL != "" {
		t.Errorf("port: %+v", p)
	}
	// The source row says nothing about the resolver, so the Go defaults fill in.
	if d := get(10); d.Type != "dns" || d.Hostname != "example.com" || d.Port != 53 ||
		d.DNSResolveType != "A" || d.DNSResolveServer != "1.1.1.1" {
		t.Errorf("dns: %+v", d)
	}
	if p := get(11); p.Type != "ping" || p.Hostname != "example.com" || p.PingCount != 1 ||
		p.PingPerRequestTimeout != 2 || p.PacketSize != 56 {
		t.Errorf("ping: %+v", p)
	}
	members := f.count(t, `SELECT count(*) FROM monitor_group_member WHERE group_name = 'Top'`)
	if members != 2 {
		t.Errorf("Top has %d members, want Site through Inner, and Port", members)
	}
}

func TestSourceIsNeverWritten(t *testing.T) {
	f := newFixture(t)
	conn, err := f.source.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(context.Background(), "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = conn.ExecContext(context.Background(), "ROLLBACK") }()
	if _, err := conn.ExecContext(context.Background(), "INSERT INTO monitor (name) VALUES ('x')"); err == nil {
		t.Error("a write inside the import's transaction mode should fail")
	}
}

func TestHistoryWithoutDoubleCounting(t *testing.T) {
	f := newFixture(t)
	f.monitor(t, 3, "Site", "http", map[string]any{"url": "https://example.com"})
	// Conditions aren't ported, so this one still can't be imported.
	f.monitor(t, 20, "Skipped", "dns", map[string]any{"hostname": "x", "conditions": `[{"type":"header"}]`})

	f.now = time.Now().UTC().Truncate(time.Hour).Add(10 * time.Minute)
	cutover := f.now.Truncate(time.Hour)
	today := f.now.Truncate(24 * time.Hour)

	// Aggregates: 3 hours before the cutover, with some down, in the same days as raw beats.
	for i, row := range []struct{ up, down int }{{50, 10}, {60, 0}, {30, 30}} {
		at := cutover.Add(-time.Duration(i+1) * time.Hour)
		extras := any(nil)
		if i == 0 {
			extras = `{"maintenance":5}`
		}
		f.exec(t, "INSERT INTO stat_hourly (monitor_id, timestamp, ping, ping_min, ping_max, up, down, extras) VALUES (3, ?, 100, 50, 200, ?, ?, ?)", at.Unix(), row.up, row.down, extras)
	}
	f.exec(t, "INSERT INTO stat_hourly (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (20, ?, 1, 1, 1, 99, 99)", cutover.Add(-time.Hour).Unix())
	// The current hour is in the source too, but raw beats must supply it.
	f.exec(t, "INSERT INTO stat_hourly (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (3, ?, 1, 1, 1, 1000, 1000)", cutover.Unix())
	for i, row := range []struct{ up, down int }{{1000, 100}, {2000, 0}} {
		at := today.Add(-time.Duration(i+1) * 24 * time.Hour)
		f.exec(t, "INSERT INTO stat_daily (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (3, ?, 80, 40, 90, ?, ?)", at.Unix(), row.up, row.down)
	}
	f.exec(t, "INSERT INTO stat_daily (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (3, ?, 1, 1, 1, 7777, 7777)", today.Unix())

	// Raw beats: in the imported hours, which must not count again, and in the current hour.
	insert := func(at time.Time, status int, important int, ping any) {
		f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg, important, ping, duration, retries) VALUES (3, ?, ?, 'beat', ?, ?, 60, 1)", status, at.Format("2006-01-02 15:04:05"), important, ping)
	}
	for m := 0; m < 20; m++ {
		insert(cutover.Add(-time.Hour).Add(time.Duration(m)*time.Minute), 0, 0, nil)
	}
	insert(cutover.Add(time.Minute), 1, 0, 42)
	insert(cutover.Add(2*time.Minute), 1, 0, 44)
	insert(cutover.Add(3*time.Minute), 0, 1, nil)
	insert(cutover.Add(4*time.Minute), 2, 0, nil)
	insert(cutover.Add(5*time.Minute), 3, 1, nil)
	// A beat from a skipped monitor, one with an impossible status, and one with a duplicate time.
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (20, 1, ?, 'x')", cutover.Format("2006-01-02 15:04:05"))
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (3, 9, ?, 'x')", cutover.Add(6*time.Minute).Format("2006-01-02 15:04:05"))
	insert(cutover.Add(time.Minute), 1, 0, 42)

	report := f.run(t, true, false)
	if report.HourlyStats != 3 || report.DailyStats != 2 {
		t.Errorf("stats: hourly %d daily %d", report.HourlyStats, report.DailyStats)
	}
	if report.Beats != 26 || report.NewBeats != 25 || report.InvalidBeats != 1 {
		t.Errorf("beats: read %d new %d invalid %d", report.Beats, report.NewBeats, report.InvalidBeats)
	}

	id := f.targetID(t, 3)
	var up, down, maintenance int64
	sum := func(view string, since time.Time) {
		t.Helper()
		err := f.targetSQL.QueryRow(context.Background(), `
			SELECT COALESCE(sum(up), 0)::bigint, COALESCE(sum(down), 0)::bigint, COALESCE(sum(maintenance), 0)::bigint
			FROM `+view+` WHERE monitor_id = $1 AND bucket >= $2`, id, since).Scan(&up, &down, &maintenance)
		if err != nil {
			t.Fatal(err)
		}
	}
	// Imported hours: 50+60+30 up and 10+0+30 down. Current hour from raw: 2 up, 1 down + 1 pending.
	sum("uptime_hourly", cutover.Add(-3*time.Hour))
	if up != 142 || down != 42 || maintenance != 6 {
		t.Errorf("hourly: up %d down %d maintenance %d, want 142, 42 and 6", up, down, maintenance)
	}
	// Imported days: 1000+2000 up and 100 down. Today only has beats after the day starts.
	sum("uptime_daily", today.Add(-2*24*time.Hour))
	if up < 3002 || down < 102 {
		t.Errorf("daily: up %d down %d", up, down)
	}
	if up >= 7777 || down >= 7777 {
		t.Errorf("today's source row counted: up %d down %d", up, down)
	}

	stats, err := f.target.Stats(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 1 || stats[0].Uptime30d == nil || stats[0].Uptime24h == nil {
		t.Fatalf("stats: %+v", stats)
	}
	if got := *stats[0].Uptime24h; math.Abs(got-142.0/184.0) > 1e-9 {
		t.Errorf("24h uptime %v, want %v", got, 142.0/184.0)
	}

	var ping float64
	if err := f.targetSQL.QueryRow(context.Background(), `SELECT ping_sum / ping_count FROM imported_stat_hourly WHERE monitor_id = $1 AND bucket = $2`, id, cutover.Add(-time.Hour)).Scan(&ping); err != nil {
		t.Fatal(err)
	}
	if ping != 100 {
		t.Errorf("average ping %v", ping)
	}

	// Beats keep their values and times, read as UTC.
	beats, err := f.target.LatestBeats(context.Background(), id, 100)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, b := range beats {
		if b.Time.Equal(cutover.Add(3 * time.Minute)) {
			found = true
			if b.Status != 0 || !b.Important || b.Ping != nil || b.Msg != "beat" || b.Duration != 60 || b.Retries != 1 {
				t.Errorf("beat: %+v", b)
			}
		}
	}
	if !found {
		t.Error("the beat's time didn't survive as UTC")
	}

	// A skipped monitor's data doesn't arrive.
	if n := f.count(t, `SELECT count(*) FROM imported_stat_hourly WHERE up = 99`); n != 0 {
		t.Errorf("%d rows from a skipped monitor", n)
	}

	// Deleting the monitor removes the imported history too.
	if err := f.target.DeleteMonitor(context.Background(), id); err != nil {
		t.Fatal(err)
	}
	if n := f.count(t, `SELECT count(*) FROM imported_stat_hourly`) + f.count(t, `SELECT count(*) FROM imported_stat_daily`) + f.count(t, `SELECT count(*) FROM monitor_import`); n != 0 {
		t.Errorf("%d imported rows survived the delete", n)
	}
}

func TestImportIsRepeatableAndRespectsActiveMonitors(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.monitor(t, 3, "Site", "http", map[string]any{"url": "https://example.com"})
	f.monitor(t, 4, "Paused in source", "http", map[string]any{"url": "https://example.com", "active": 0})
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (3, 1, UTC_TIMESTAMP() - INTERVAL 1 MINUTE, 'ok')")
	hour := f.now.Truncate(time.Hour).Add(-time.Hour).Unix()
	f.exec(t, "INSERT INTO stat_hourly (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (3, ?, 10, 10, 10, 5, 5)", hour)

	first := f.run(t, true, false)
	if first.Imported != 2 || first.NewBeats != 1 {
		t.Fatalf("first: %+v", first)
	}

	// A second run duplicates nothing, and picks up changes.
	f.exec(t, "UPDATE monitor SET name = 'Renamed' WHERE id = 3")
	f.exec(t, "UPDATE stat_hourly SET up = 8 WHERE monitor_id = 3")
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (3, 0, UTC_TIMESTAMP(), 'new')")
	second := f.run(t, true, false)
	if second.Imported != 2 || second.NewBeats != 1 || second.Beats != 2 {
		t.Fatalf("second: %+v", second)
	}
	if n := f.count(t, `SELECT count(*) FROM monitor`); n != 2 {
		t.Errorf("%d monitors after a repeat", n)
	}
	if n := f.count(t, `SELECT sum(up)::bigint FROM imported_stat_hourly`); n != 8 {
		t.Errorf("imported up = %d", n)
	}
	id := f.targetID(t, 3)
	m, err := f.target.GetMonitor(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "Renamed" {
		t.Errorf("name: %q", m.Name)
	}

	// Activating only resumes what was running in the source.
	third := f.run(t, true, true)
	if third.Activated != 1 {
		t.Errorf("activated: %d", third.Activated)
	}
	if m, _ := f.target.GetMonitor(ctx, id); !m.Active {
		t.Error("the source's active monitor should be active")
	}
	if m, _ := f.target.GetMonitor(ctx, f.targetID(t, 4)); m.Active {
		t.Error("the source's paused monitor should stay paused")
	}

	// From now on this service owns the monitor: no config or history changes.
	if _, err := f.target.SetMonitorActive(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if _, err := f.targetSQL.Exec(ctx, `UPDATE monitor SET name = 'Edited here' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE stat_hourly SET up = 500 WHERE monitor_id = 3")
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (3, 1, UTC_TIMESTAMP() + INTERVAL 1 SECOND, 'late')")
	before := f.count(t, `SELECT count(*) FROM heartbeat WHERE monitor_id = $1`, id)
	fourth := f.run(t, true, false)
	if fourth.AlreadyActive != 1 {
		t.Errorf("fourth: %+v", fourth)
	}
	if m, _ := f.target.GetMonitor(ctx, id); m.Name != "Edited here" {
		t.Errorf("an active monitor was overwritten: %q", m.Name)
	}
	if n := f.count(t, `SELECT count(*) FROM heartbeat WHERE monitor_id = $1`, id); n != before {
		t.Errorf("an active monitor got beats: %d to %d", before, n)
	}
	if n := f.count(t, `SELECT sum(up)::bigint FROM imported_stat_hourly WHERE monitor_id = $1`, id); n != 8 {
		t.Errorf("an active monitor's history changed: %d", n)
	}
}

func TestRejectsADatabaseThatIsntUptimeKuma(t *testing.T) {
	f := newFixture(t)
	f.exec(t, "ALTER TABLE monitor DROP COLUMN parent")
	_, err := Run(context.Background(), Options{Source: f.source, Log: quiet})
	if err == nil || !strings.Contains(err.Error(), `"parent"`) {
		t.Errorf("got %v", err)
	}
}

func TestToleratesOlderSchema(t *testing.T) {
	f := newFixture(t)
	// Columns added in later versions.
	for _, column := range []string{"bearer_token", "cache_bust", "ip_family", "domain_expiry_notification", "save_response", "save_error_response", "description"} {
		f.exec(t, "ALTER TABLE monitor DROP COLUMN "+column)
	}
	f.exec(t, "ALTER TABLE heartbeat DROP COLUMN retries")
	f.monitor(t, 3, "Site", "http", map[string]any{"url": "https://example.com"})
	f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (3, 1, UTC_TIMESTAMP(), 'ok')")
	report := f.run(t, true, false)
	if report.Imported != 1 || report.NewBeats != 1 {
		t.Errorf("report: %+v", report)
	}
}

func TestConvertBeatRejectsBadTime(t *testing.T) {
	row := map[string]sql.NullString{"time": {String: "0000-00-00 00:00:00", Valid: true}, "status": {String: "1", Valid: true}}
	if _, err := convertBeat(row, 1); err == nil {
		t.Error("expected an error")
	}
}

func TestImportsNotifications(t *testing.T) {
	f := newFixture(t)
	f.seedMonitors(t)
	f.exec(t, `INSERT INTO notification (id, name, active, is_default, config) VALUES
		(1, 'Ops webhook', 1, 1, '{"type":"webhook","webhookURL":"https://hooks.example/secret-token","webhookContentType":"json"}'),
		(2, 'Mail', 0, 0, '{"type":"smtp","smtpHost":"mail.example","smtpPort":587,"smtpPassword":"secret-pass"}'),
		(5, 'Push', 1, 0, '{"type":"LineNotify","lineNotifyAccessToken":"secret-line"}'),
		(6, 'Broken', 1, 0, 'not json')`)
	f.exec(t, "INSERT INTO monitor_notification (monitor_id, notification_id) VALUES (4, 5)")

	dry := f.run(t, false, false)
	if dry.Notifications.Source != 4 || dry.Notifications.Imported != 2 || len(dry.Notifications.Skipped) != 2 || dry.Notifications.Links != 3 || dry.Notifications.MonitorsLosingAlerts != 1 {
		t.Fatalf("dry run: %+v", dry.Notifications)
	}
	if n := f.count(t, "SELECT count(*) FROM notification"); n != 0 {
		t.Fatalf("a dry run wrote %d notifications", n)
	}
	encoded, err := json.Marshal(dry)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "secret") {
		t.Fatalf("the report leaks credentials: %s", encoded)
	}

	report := f.run(t, true, false)
	if report.Notifications.Imported != 2 || report.Notifications.Links != 3 {
		t.Fatalf("apply: %+v", report.Notifications)
	}
	// Uptime Kuma still sends these alerts during a parallel run, so nothing may be on yet.
	if n := f.count(t, "SELECT count(*) FROM notification WHERE active"); n != 0 {
		t.Fatalf("%d notifications are on before the cutover", n)
	}
	if n := f.count(t, "SELECT count(*) FROM monitor_notification WHERE monitor_id = $1", f.targetID(t, 3)); n != 2 {
		t.Fatalf("monitor 3 has %d notifications", n)
	}
	if n := f.count(t, "SELECT count(*) FROM monitor_notification WHERE monitor_id = $1", f.targetID(t, 4)); n != 1 {
		t.Fatalf("monitor 4 has %d notifications", n)
	}

	// A link removed in the source goes away, and nothing is duplicated.
	f.exec(t, "DELETE FROM monitor_notification WHERE monitor_id = 3 AND notification_id = 2")
	f.run(t, true, false)
	if n := f.count(t, "SELECT count(*) FROM monitor_notification WHERE monitor_id = $1", f.targetID(t, 3)); n != 1 {
		t.Fatalf("after re-import, monitor 3 has %d notifications", n)
	}
	if n := f.count(t, "SELECT count(*) FROM notification"); n != 2 {
		t.Fatalf("re-import made %d notifications", n)
	}

	// Activating only switches on what was on in the source.
	activated := f.run(t, true, true)
	if activated.Notifications.Activated != 1 {
		t.Fatalf("activated: %+v", activated.Notifications)
	}
	if n := f.count(t, "SELECT count(*) FROM notification WHERE active AND name = 'Ops webhook'"); n != 1 {
		t.Fatal("the active notification wasn't switched on")
	}
	if n := f.count(t, "SELECT count(*) FROM notification WHERE active AND name = 'Mail'"); n != 0 {
		t.Fatal("an inactive notification was switched on")
	}

	// Once on, later imports leave it alone.
	f.exec(t, "UPDATE notification SET config = '{\"type\":\"webhook\",\"webhookURL\":\"https://changed.example\"}' WHERE id = 1")
	again := f.run(t, true, false)
	if again.Notifications.AlreadyActive != 1 {
		t.Fatalf("again: %+v", again.Notifications)
	}
	if n := f.count(t, "SELECT count(*) FROM notification WHERE config->>'webhookURL' = 'https://changed.example'"); n != 0 {
		t.Fatal("an active notification was overwritten")
	}
}

// TestImportsPushMonitors keeps a sender's URL working: the token the source
// database holds is the one that arrives, and a token used twice is skipped.
func TestImportsPushMonitors(t *testing.T) {
	f := newFixture(t)
	f.monitor(t, 1, "Heartbeat", "push", map[string]any{"push_token": "sharedToken123456789012345678x"})
	f.monitor(t, 2, "Twin", "push", map[string]any{"push_token": "sharedToken123456789012345678x"})
	f.monitor(t, 3, "Own", "push", map[string]any{"push_token": "anotherToken123456789012345678y"})

	report := f.run(t, true, false)
	if report.Imported != 2 || len(report.Skipped) != 1 {
		t.Fatalf("imported %d skipped %+v", report.Imported, report.Skipped)
	}
	if s := report.Skipped[0]; s.SourceID != 2 || s.Reason != "invalid setting: another monitor has the same push token" {
		t.Fatalf("skipped: %+v", s)
	}
	if got := f.count(t, "SELECT count(*) FROM monitor"); got != 2 {
		t.Fatalf("%d monitors", got)
	}
	for sourceID, token := range map[int]string{
		1: "sharedToken123456789012345678x",
		3: "anotherToken123456789012345678y",
	} {
		var stored string
		err := f.targetSQL.QueryRow(context.Background(),
			`SELECT push_token FROM monitor WHERE id = $1`, f.targetID(t, sourceID)).Scan(&stored)
		if err != nil {
			t.Fatal(err)
		}
		if stored != token {
			t.Errorf("source %d: token %q, want %q", sourceID, stored, token)
		}
	}
}

func TestImportsResendInterval(t *testing.T) {
	f := newFixture(t)
	f.monitor(t, 1, "Resends", "http", map[string]any{"url": "https://example.com", "resend_interval": 5})
	f.run(t, true, false)
	if n := f.count(t, "SELECT resend_interval FROM monitor WHERE id = $1", f.targetID(t, 1)); n != 5 {
		t.Fatalf("resend interval: %d", n)
	}
}

func TestImportsTagsAndCertificateAlerts(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.monitor(t, 1, "Secure", "http", map[string]any{"url": "https://example.com", "expiry_notification": 1})
	f.monitor(t, 2, "Ignored", "http", map[string]any{"url": "https://example.com", "expiry_notification": 1, "ignore_tls": 1})
	f.monitor(t, 3, "Unsupported", "lighthouse", map[string]any{"hostname": "example.com"})
	f.exec(t, "INSERT INTO tag (id, name, color) VALUES (7, 'prod', '#ff0000'), (8, 'eu', '#00ff00')")
	f.exec(t, "INSERT INTO monitor_tag (monitor_id, tag_id, value) VALUES (1, 7, ''), (1, 8, 'west'), (3, 7, '')")

	dry := f.run(t, false, false)
	if dry.Tags != 2 || dry.TagLinks != 2 {
		t.Fatalf("dry run: %d tags, %d links", dry.Tags, dry.TagLinks)
	}
	if n := f.count(t, "SELECT count(*) FROM tag"); n != 0 {
		t.Fatalf("dry run wrote %d tags", n)
	}

	// Twice, because importing again must duplicate nothing.
	f.run(t, true, false)
	report := f.run(t, true, false)
	if report.TagLinks != 2 {
		t.Fatalf("links: %d", report.TagLinks)
	}
	if n := f.count(t, "SELECT count(*) FROM tag"); n != 2 {
		t.Fatalf("%d tags", n)
	}
	secure := f.targetID(t, 1)
	all, err := f.target.AllMonitorTags(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if tags := all[secure]; len(tags) != 2 || tags[0].Name != "eu" || tags[0].Value != "west" || tags[1].Color != "#ff0000" {
		t.Fatalf("tags: %+v", tags)
	}

	m, err := f.target.GetMonitor(ctx, secure)
	if err != nil || !m.ExpiryNotification {
		t.Fatalf("secure: %+v %v", m, err)
	}
	m, err = f.target.GetMonitor(ctx, f.targetID(t, 2))
	if err != nil || m.ExpiryNotification {
		t.Fatalf("with ignored TLS: %+v %v", m, err)
	}
}

func TestImportsPhase4dMonitors(t *testing.T) {
	f := newFixture(t)
	f.exec(t, "INSERT INTO docker_host (id, user_id, docker_daemon, docker_type, name) VALUES (5, 1, '/var/run/docker.sock', 'socket', 'local')")
	f.exec(t, "INSERT INTO remote_browser (id, name, url) VALUES (3, 'browserless', 'ws://browserless:3000')")
	f.monitor(t, 1, "Container", "docker", map[string]any{"docker_host": 5, "docker_container": "web"})
	f.monitor(t, 2, "Lost container", "docker", map[string]any{"docker_host": 99, "docker_container": "db"})
	f.monitor(t, 3, "Nginx", "system-service", map[string]any{"config": `{"systemServiceName":"nginx"}`})
	f.monitor(t, 4, "API", "pm2", map[string]any{"config": `{"systemServiceName":"api"}`})
	f.monitor(t, 5, "Files", "sftp", map[string]any{"hostname": "files", "port": 2222, "ssh_username": "kuma",
		"ssh_password": "pw", "ssh_private_key": "unused", "sftp_path": "/upload"})
	f.monitor(t, 6, "Health", "grpc-keyword", map[string]any{"grpc_url": "api:443", "grpc_protobuf": "syntax = \"proto3\";",
		"grpc_service_name": "Health", "grpc_method": "check", "grpc_body": "{}", "grpc_enable_tls": 1, "keyword": "SERVING"})
	f.monitor(t, 7, "Page", "real-browser", map[string]any{"url": "https://example.com", "remote_browser": 3, "screenshot_delay": 200})

	report := f.run(t, true, false)
	if report.Imported != 6 || report.DockerHosts != 1 || report.RemoteBrowsers != 1 || len(report.Skipped) != 1 ||
		report.Skipped[0].Reason != "invalid setting: the Docker host doesn't exist" {
		t.Fatalf("report: %+v", report)
	}
	ctx := context.Background()
	get := func(sourceID int) monitor.Monitor {
		m, err := f.target.GetMonitor(ctx, f.targetID(t, sourceID))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	hosts, _ := f.target.ListDockerHosts(ctx)
	browsers, _ := f.target.ListRemoteBrowsers(ctx)
	if len(hosts) != 1 || len(browsers) != 1 {
		t.Fatalf("hosts %+v browsers %+v", hosts, browsers)
	}
	if m := get(1); m.DockerHostID != hosts[0].ID || m.DockerContainer != "web" {
		t.Errorf("docker: %+v", m.Settings)
	}
	if m := get(3); m.SystemServiceName != "nginx" {
		t.Errorf("system service: %+v", m.Settings)
	}
	if m := get(4); m.SystemServiceName != "api" {
		t.Errorf("pm2: %+v", m.Settings)
	}
	if m := get(5); m.Port != 2222 || m.SSHPassword != "pw" || m.SSHPrivateKey != "" || m.SFTPPath != "/upload" {
		t.Errorf("sftp: %+v", m)
	}
	if m := get(6); m.GRPCURL != "api:443" || !m.GRPCEnableTLS || m.Keyword != "SERVING" {
		t.Errorf("grpc: %+v", m)
	}
	if m := get(7); m.RemoteBrowserID != browsers[0].ID || m.ScreenshotDelay != 200 {
		t.Errorf("real browser: %+v", m.Settings)
	}

	// A second import uses the same hosts.
	f.run(t, true, false)
	if hosts, _ := f.target.ListDockerHosts(ctx); len(hosts) != 1 {
		t.Fatalf("hosts after a second import: %+v", hosts)
	}
}

func TestImportsHTTPOptions(t *testing.T) {
	f := newFixture(t)
	f.exec(t, "INSERT INTO proxy (id, user_id, protocol, host, port, auth, username, password, active, `default`) VALUES (4, 1, 'socks5', 'proxy.internal', 1080, 1, 'u', 'p', 1, 1)")
	cert, key := testCertificate(t)
	f.monitor(t, 1, "Proxied", "http", map[string]any{"url": "https://example.com", "proxy_id": 4, "ip_family": "ipv4",
		"save_response": 1, "save_error_response": 1, "response_max_length": 50, "domain_expiry_notification": 1})
	f.monitor(t, 2, "Mtls", "keyword", map[string]any{"url": "https://example.com", "keyword": "ok", "auth_method": "mtls", "tls_cert": cert, "tls_key": key})
	f.monitor(t, 3, "Oauth", "json-query", map[string]any{"url": "https://example.com", "json_path": "status", "expected_value": "ok",
		"auth_method": "oauth2-cc", "oauth_token_url": "https://id.example.com/token", "oauth_client_id": "c", "oauth_client_secret": "s",
		"oauth_auth_method": "client_secret_post", "oauth_scopes": "read", "oauth_audience": "api", "retry_only_on_status_code_failure": 1})
	f.monitor(t, 4, "Ntlm", "http", map[string]any{"url": "https://example.com", "auth_method": "ntlm", "basic_auth_user": "u",
		"basic_auth_pass": "p", "auth_domain": "CORP", "auth_workstation": "WS1"})
	f.monitor(t, 5, "Ping", "ping", map[string]any{"hostname": "example.com", "domain_expiry_notification": 1, "description": "the gateway"})

	report := f.run(t, true, false)
	if report.Imported != 5 || report.Proxies != 1 || len(report.Skipped) != 0 {
		t.Fatalf("report: %+v", report)
	}
	ctx := context.Background()
	get := func(sourceID int) monitor.Monitor {
		t.Helper()
		m, err := f.target.GetMonitor(ctx, f.targetID(t, sourceID))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	proxies, _ := f.target.ListProxies(ctx)
	if len(proxies) != 1 || proxies[0].Protocol != "socks5" || proxies[0].Username != "u" || proxies[0].Default {
		t.Fatalf("proxies: %+v", proxies)
	}
	if m := get(1); m.ProxyID != proxies[0].ID || m.IPFamily != "ipv4" || !m.SaveResponse || !m.SaveErrorResponse ||
		m.ResponseMaxLength != 50 || !m.DomainExpiryNotification {
		t.Errorf("proxied: %+v", m.Settings)
	}
	if m := get(2); m.AuthMethod != "mtls" || m.TLSCert != cert || m.TLSKey != key {
		t.Errorf("mtls: %+v", m.Settings)
	}
	if m := get(3); m.AuthMethod != "oauth2-cc" || m.OAuthAuthMethod != "client_secret_post" || m.OAuthAudience != "api" ||
		!m.RetryOnlyOnStatusCodeFailure {
		t.Errorf("oauth: %+v", m.Settings)
	}
	if m := get(4); m.AuthMethod != "ntlm" || m.AuthDomain != "CORP" || m.AuthWorkstation != "WS1" {
		t.Errorf("ntlm: %+v", m.Settings)
	}
	if m := get(5); !m.DomainExpiryNotification || m.Description != "the gateway" {
		t.Errorf("ping: %+v", m.Settings)
	}
	f.run(t, true, false)
	if proxies, _ := f.target.ListProxies(ctx); len(proxies) != 1 {
		t.Fatalf("proxies after a second import: %+v", proxies)
	}
}

// testCertificate makes a self-signed certificate and its key, as PEM.
func testCertificate(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "kuma"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestImportsPhase4fMonitors(t *testing.T) {
	f := newFixture(t)
	f.monitor(t, 1, "TF2", "gamedig", map[string]any{"hostname": "tf2.example.com", "port": 27015, "game": "teamfortress2", "gamedig_given_port_only": 0})
	f.monitor(t, 2, "Unknown game", "gamedig", map[string]any{"hostname": "x.example.com", "port": 1, "game": "solitaire"})
	f.monitor(t, 3, "Peer", "tailscale-ping", map[string]any{"hostname": "cider"})
	f.monitor(t, 4, "Ping from Germany", "globalping", map[string]any{"subtype": "ping", "hostname": "example.com", "location": "Germany",
		"protocol": "ICMP", "ping_count": 3})
	f.monitor(t, 5, "HTTP from Europe", "globalping", map[string]any{"subtype": "http", "url": "https://example.com", "location": "Europe",
		"keyword": "Example", "accepted_statuscodes_json": `["200-299"]`, "auth_method": "bearer", "bearer_token": "t"})
	f.monitor(t, 6, "DNS from Asia", "globalping", map[string]any{"subtype": "dns", "hostname": "example.com", "location": "Asia",
		"protocol": "UDP", "port": 53, "dns_resolve_type": "MX", "keyword": "mail"})

	report := f.run(t, true, false)
	if report.Imported != 5 || len(report.Skipped) != 1 || report.Skipped[0].Reason != `invalid setting: game "solitaire" isn't supported` {
		t.Fatalf("report: %+v", report)
	}
	ctx := context.Background()
	get := func(sourceID int) monitor.Monitor {
		t.Helper()
		m, err := f.target.GetMonitor(ctx, f.targetID(t, sourceID))
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	if m := get(1); m.Game != "teamfortress2" || m.GamedigGivenPortOnly || m.Port != 27015 {
		t.Errorf("gamedig: %+v", m)
	}
	if m := get(3); m.Hostname != "cider" {
		t.Errorf("tailscale: %+v", m)
	}
	if m := get(4); m.Subtype != "ping" || m.Location != "Germany" || m.PingCount != 3 {
		t.Errorf("globalping ping: %+v", m)
	}
	if m := get(5); m.URL != "https://example.com" || m.Keyword != "Example" || m.AuthMethod != "bearer" {
		t.Errorf("globalping http: %+v", m)
	}
	if m := get(6); m.DNSResolveType != "MX" || m.Keyword != "mail" || m.Port != 53 {
		t.Errorf("globalping dns: %+v", m)
	}
}
