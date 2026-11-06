package importer

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestImportsSettings(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for key, value := range map[string]string{
		"primaryBaseURL": `"https://kuma.example.com"`, "searchEngineIndex": "true", "trustProxy": "true",
		"tlsExpiryNotifyDays": "[7,14]", "keepDataPeriodDays": "90", "steamAPIKey": `"steam-s3cret"`,
		"entryPage": "statusPage-main", // An older version stored text, not JSON.
		"title":     `"Not a setting that takes effect"`,
	} {
		f.exec(t, "INSERT INTO setting (`key`, value) VALUES (?, ?)", key, value)
	}
	// Here: a default retention, which may be replaced, and a base URL changed in the UI.
	if _, _, err := f.target.RetentionDays(ctx, 365); err != nil {
		t.Fatal(err)
	}
	if err := f.target.SaveSettings(ctx, map[string]json.RawMessage{"primaryBaseURL": json.RawMessage(`"https://new.example.com"`)}); err != nil {
		t.Fatal(err)
	}

	report := f.run(t, true, false)
	results := map[string]string{}
	for _, s := range report.Settings {
		results[s.Key] = s.Result
	}
	for key, want := range map[string]string{
		"primaryBaseURL": "kept", "searchEngineIndex": "copied", "trustProxy": "copied", "tlsExpiryNotifyDays": "copied",
		"keepDataPeriodDays": "copied", "steamAPIKey": "copied", "entryPage": "copied", "checkUpdate": "not set in the source",
	} {
		if results[key] != want {
			t.Errorf("%s: %q, want %q", key, results[key], want)
		}
	}
	data, _ := json.Marshal(report)
	if strings.Contains(string(data), "steam-s3cret") {
		t.Error("the report holds a setting's value")
	}
	settings, err := f.target.Settings(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if string(settings["primaryBaseURL"]) != `"https://new.example.com"` || string(settings["entryPage"]) != `"statusPage-main"` ||
		string(settings["keepDataPeriodDays"]) != "90" || settings["title"] != nil {
		t.Errorf("settings: %s %s %s %s", settings["primaryBaseURL"], settings["entryPage"], settings["keepDataPeriodDays"], settings["title"])
	}

	// Again: what the import wrote may be replaced, and once changed here it's kept.
	f.exec(t, "UPDATE setting SET value = '180' WHERE `key` = 'keepDataPeriodDays'")
	if err := f.target.SaveSettings(ctx, map[string]json.RawMessage{"trustProxy": json.RawMessage("false")}); err != nil {
		t.Fatal(err)
	}
	report = f.run(t, true, false)
	results = map[string]string{}
	for _, s := range report.Settings {
		results[s.Key] = s.Result
	}
	if results["keepDataPeriodDays"] != "copied" || results["trustProxy"] != "kept" || results["searchEngineIndex"] != "same" {
		t.Errorf("second import: %v", results)
	}
}

func TestImportsAPIKeys(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	betterAuthKey := strings.Repeat("Zx9", 21) + "q"
	sum := sha256.Sum256([]byte(betterAuthKey))
	f.exec(t, "INSERT INTO better_auth_apikey (id, name, start, `key`, referenceId, enabled, createdAt, updatedAt) VALUES ('ba1', 'CI', 'Zx9Zx9', ?, 'u1', 1, '2026-01-02 03:04:05', '2026-01-02 03:04:05')",
		base64.RawURLEncoding.EncodeToString(sum[:]))
	f.exec(t, "INSERT INTO better_auth_apikey (id, name, `key`, referenceId, enabled, createdAt, updatedAt) VALUES ('ba2', 'Off', 'not-a-hash', 'u1', 0, now(), now())")
	legacy, _ := bcrypt.GenerateFromPassword([]byte("old-secret"), bcrypt.MinCost)
	f.exec(t, "INSERT INTO api_key (id, `key`, name, user_id, active, expires) VALUES (4, ?, 'Old script', 1, 1, NULL), (5, ?, 'Revoked', 1, 0, '2030-01-01 00:00:00')",
		string(legacy), string(legacy))

	report := f.run(t, true, false)
	if r := report.APIKeys; r.Source != 4 || r.New != 3 || len(r.Skipped) != 1 || r.Disabled != 1 {
		t.Fatalf("report: %+v", r)
	}
	key, err := f.target.FindAPIKey(ctx, sum[:])
	if err != nil || key.Name != "CI" || key.Prefix != "Zx9Zx9" || len(key.Scopes) == 0 || key.CreatedAt.Year() != 2026 {
		t.Fatalf("better-auth key: %+v %v", key, err)
	}
	old, hash, err := f.target.FindLegacyAPIKey(ctx, 4)
	if err != nil || old.Name != "Old script" || bcrypt.CompareHashAndPassword([]byte(hash), []byte("old-secret")) != nil {
		t.Fatalf("legacy key: %+v %v", old, err)
	}
	if _, _, err := f.target.FindLegacyAPIKey(ctx, 5); err == nil {
		t.Error("a key that was off works")
	}
	// Again: nothing new, and a key revoked here stays revoked.
	if err := f.target.RevokeAPIKey(ctx, old.ID); err != nil {
		t.Fatal(err)
	}
	report = f.run(t, true, false)
	if r := report.APIKeys; r.New != 0 || r.Updated != 3 {
		t.Fatalf("second import: %+v", r)
	}
	if _, _, err := f.target.FindLegacyAPIKey(ctx, 4); err == nil {
		t.Error("a key revoked here came back")
	}
}

func TestImportsAlertState(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.monitor(t, 1, "Site", "http", map[string]any{"url": "https://example.com", "expiry_notification": 1})
	f.exec(t, "INSERT INTO notification_sent_history (type, monitor_id, days) VALUES ('certificate', 1, 14), ('certificate', 1, 7), ('certificate', 99, 7)")
	info := `{"valid":true,"certInfo":{"fingerprint256":"AA:BB","daysRemaining":6}}`
	f.exec(t, "INSERT INTO monitor_tls_info (monitor_id, info_json) VALUES (1, ?)", info)
	f.exec(t, "INSERT INTO domain_expiry (domain, expiry, last_check, last_expiry_notification_sent) VALUES ('example.com', '2027-01-01 00:00:00', '2026-11-01 00:00:00', 14)")

	report := f.run(t, true, false)
	if r := report.AlertState; r.CertAlerts != 2 || r.Certificates != 1 || r.Domains != 1 {
		t.Fatalf("report: %+v", r)
	}
	id := f.targetID(t, 1)
	if sent, err := f.target.CertAlertSent(ctx, id, 7); err != nil || !sent {
		t.Errorf("the 7-day alert: %v %v", sent, err)
	}
	if fp, err := f.target.TLSFingerprint(ctx, id); err != nil || fp != "AA:BB" {
		t.Errorf("fingerprint: %q %v", fp, err)
	}
	record, err := f.target.DomainExpiry(ctx, "example.com")
	if err != nil || record.LastNotifiedDays == nil || *record.LastNotifiedDays != 14 || record.Expiry == nil {
		t.Fatalf("domain: %+v %v", record, err)
	}
	// A domain checked here since keeps what this server knows.
	later := time.Date(2026, 11, 5, 0, 0, 0, 0, time.UTC)
	record.LastCheck, record.LastNotifiedDays = &later, nil
	if err := f.target.SaveDomainExpiry(ctx, record); err != nil {
		t.Fatal(err)
	}
	if report := f.run(t, true, false); report.AlertState.Domains != 0 || report.AlertState.CertAlerts != 0 {
		t.Errorf("second import: %+v", report.AlertState)
	}
}

func TestKeepsSkippedHistoryUntilImported(t *testing.T) {
	f := newFixture(t)
	f.monitor(t, 1, "Broken", "http", map[string]any{"url": "https://example.com", "headers": "not json"})
	now := time.Now().UTC().Truncate(time.Hour)
	for i := 0; i < 3; i++ {
		f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (1, 1, ?, 'ok')", now.Add(-time.Duration(i+1)*time.Hour).Format("2006-01-02 15:04:05"))
	}
	f.exec(t, "INSERT INTO stat_hourly (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (1, ?, 10, 5, 20, 60, 0)", now.Add(-2*time.Hour).Unix())
	f.exec(t, "INSERT INTO stat_daily (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (1, ?, 10, 5, 20, 900, 10)", now.Truncate(24*time.Hour).Add(-24*time.Hour).Unix())

	report := f.run(t, true, false)
	if r := report.SkippedHistory; r.Monitors != 1 || r.Beats != 3 || r.NewBeats != 3 || r.HourlyStats != 1 || r.DailyStats != 1 {
		t.Fatalf("kept: %+v", r)
	}
	if n := f.count(t, `SELECT count(*) FROM heartbeat`); n != 0 {
		t.Fatalf("%d beats arrived for a skipped monitor", n)
	}
	// Uptime Kuma pruned the beats since, and a later version can import the monitor.
	f.exec(t, "DELETE FROM heartbeat")
	f.exec(t, "DELETE FROM stat_daily")
	f.exec(t, "UPDATE monitor SET headers = NULL WHERE id = 1")
	report = f.run(t, true, false)
	if report.Imported != 1 || report.SkippedHistory.AdoptedBeats != 3 {
		t.Fatalf("adopted: imported %d, %+v", report.Imported, report.SkippedHistory)
	}
	id := f.targetID(t, 1)
	if n := f.count(t, `SELECT count(*) FROM heartbeat WHERE monitor_id = $1`, id); n != 3 {
		t.Errorf("%d beats after adopting", n)
	}
	if n := f.count(t, `SELECT up FROM imported_stat_daily WHERE monitor_id = $1`, id); n != 900 {
		t.Errorf("daily statistics after adopting: %d", n)
	}
	if n := f.count(t, `SELECT count(*) FROM skipped_monitor`); n != 0 {
		t.Errorf("%d skipped monitors left", n)
	}
}

func TestVerify(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.monitor(t, 1, "Site", "http", map[string]any{"url": "https://example.com"})
	f.monitor(t, 2, "Broken", "http", map[string]any{"url": "https://example.com", "headers": "not json"})
	now := time.Now().UTC().Truncate(time.Minute)
	f.now = now
	// Two days of beats every 10 minutes, a few down, with Uptime Kuma's statistics for them.
	hourly, daily := map[int64][2]int{}, map[int64][2]int{}
	for at := now.Add(-48 * time.Hour); !at.After(now); at = at.Add(10 * time.Minute) {
		status := 1
		if at.Minute() == 30 && at.Hour()%5 == 0 {
			status = 0
		}
		f.exec(t, "INSERT INTO heartbeat (monitor_id, status, time, msg) VALUES (1, ?, ?, '')", status, at.Format("2006-01-02 15:04:05"))
		for _, b := range []struct {
			m   map[int64][2]int
			key int64
		}{{hourly, at.Truncate(time.Hour).Unix()}, {daily, at.Truncate(24 * time.Hour).Unix()}} {
			c := b.m[b.key]
			c[1-status]++
			b.m[b.key] = c
		}
	}
	for table, m := range map[string]map[int64][2]int{"stat_hourly": hourly, "stat_daily": daily} {
		for ts, c := range m {
			f.exec(t, "INSERT INTO "+table+" (monitor_id, timestamp, ping, ping_min, ping_max, up, down) VALUES (1, ?, 1, 1, 1, ?, ?)", ts, c[0], c[1])
		}
	}
	f.run(t, true, false)

	report, err := Verify(ctx, f.source, f.target)
	if err != nil {
		t.Fatal(err)
	}
	if !report.OK || report.Monitors != 1 || report.Matching != 1 || report.NotImported != 1 {
		t.Fatalf("verify: %+v", report)
	}
	if u := report.Differing; len(u) != 0 {
		t.Fatalf("differing: %+v", u)
	}

	// A beat lost here, and a down check that only the source has, are reported.
	id := f.targetID(t, 1)
	if _, err := f.targetSQL.Exec(ctx, `DELETE FROM heartbeat WHERE monitor_id = $1 AND time = $2`, id, now.Add(-3*time.Hour)); err != nil {
		t.Fatal(err)
	}
	f.exec(t, "UPDATE stat_daily SET down = down + 50 WHERE monitor_id = 1 AND timestamp = ?", now.Truncate(24*time.Hour).Add(-24*time.Hour).Unix())
	report, err = Verify(ctx, f.source, f.target)
	if err != nil {
		t.Fatal(err)
	}
	if report.OK || len(report.Differing) != 1 {
		t.Fatalf("verify after a change: %+v", report)
	}
	text := strings.Join(report.Differing[0].Differences, "; ")
	// The lost beat is in an hour before the cutover, whose uptime comes from the source's statistics.
	for _, want := range []string{"beats: 289 in the source, 288 here", "uptime over 30d"} {
		if !strings.Contains(text, want) {
			t.Errorf("no %q in: %s", want, text)
		}
	}
}
