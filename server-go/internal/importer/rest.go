package importer

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// The rest of the move (E4 in PORTING.md): settings, API keys, alert state, and the history of
// monitors that were skipped.

// importedSettings are the source's settings that take effect here.
var importedSettings = []string{
	"primaryBaseURL", "entryPage", "searchEngineIndex", "trustProxy", "tlsExpiryNotifyDays",
	"domainExpiryNotifyDays", store.RetentionSetting, "steamAPIKey", "globalpingApiToken", "chromeExecutable",
	"checkUpdate", "checkBeta",
}

// SettingReport says what happened to a setting. Never its value, which can be a token.
type SettingReport struct {
	Key    string `json:"key"`
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
}

// APIKeyReport counts the API keys copied.
type APIKeyReport struct {
	Source   int       `json:"source"`
	New      int       `json:"new"`
	Updated  int       `json:"updated"`
	Skipped  []Skipped `json:"skipped"`
	Disabled int       `json:"disabled"`
}

// AlertStateReport counts what was copied so that alerts aren't sent again.
type AlertStateReport struct {
	CertAlerts   int64 `json:"certificateAlerts"`
	Certificates int   `json:"certificates"`
	Domains      int   `json:"domains"`
}

// SkippedHistoryReport counts the history of skipped monitors kept apart, and adopted by
// monitors imported now.
type SkippedHistoryReport struct {
	Monitors     int   `json:"monitors"`
	Beats        int64 `json:"beats"`
	NewBeats     int64 `json:"newBeats"`
	HourlyStats  int64 `json:"hourlyStats"`
	DailyStats   int64 `json:"dailyStats"`
	AdoptedBeats int64 `json:"adoptedBeats"`
}

// sourceJSONValue reads a setting's value, which Node.js stored as JSON, or as plain text in
// older versions.
func sourceJSONValue(v sql.NullString) json.RawMessage {
	if json.Valid([]byte(v.String)) {
		return json.RawMessage(v.String)
	}
	quoted, _ := json.Marshal(v.String)
	return quoted
}

func copySettings(ctx context.Context, conn *sql.Conn, target Target, report *Report) error {
	rows, err := optionalTable(ctx, conn, "SELECT `key`, value FROM setting")
	if err != nil {
		return fmt.Errorf("read settings: %w", err)
	}
	values := map[string]sql.NullString{}
	for _, row := range rows {
		values[row["key"].String] = row["value"]
	}
	report.Settings = []SettingReport{}
	for _, key := range importedSettings {
		v, found := values[key]
		if !found || !v.Valid || v.String == "null" {
			report.Settings = append(report.Settings, SettingReport{Key: key, Result: "not set in the source"})
			continue
		}
		value := sourceJSONValue(v)
		if key == store.RetentionSetting {
			if _, err := store.ParseRetention(value); err != nil {
				report.Settings = append(report.Settings, SettingReport{Key: key, Result: "skipped", Reason: err.Error()})
				continue
			}
		}
		if target == nil {
			report.Settings = append(report.Settings, SettingReport{Key: key, Result: "would be copied, unless changed here"})
			continue
		}
		result, reason, err := target.ImportSetting(ctx, key, value)
		if err != nil {
			return err
		}
		report.Settings = append(report.Settings, SettingReport{Key: key, Result: result, Reason: reason})
	}
	return nil
}

// copyAPIKeys copies better-auth's keys and the older table's. Uptime Kuma gave neither any
// permissions, so they get every scope.
func copyAPIKeys(ctx context.Context, conn *sql.Conn, target Target, report *Report) error {
	r := &report.APIKeys
	r.Skipped = []Skipped{}
	scopes := api.GrantableScopes()
	var keys []store.ImportedAPIKey
	rows, err := optionalTable(ctx, conn, "SELECT * FROM better_auth_apikey ORDER BY createdAt, id")
	if err != nil {
		return fmt.Errorf("read API keys: %w", err)
	}
	for _, row := range rows {
		r.Source++
		hash, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(row["key"].String, "="))
		if err != nil || len(hash) != 32 {
			r.Skipped = append(r.Skipped, Skipped{Name: row["name"].String,
				Reason: "the stored key isn't better-auth's SHA-256 hash, so it was probably made with other settings"})
			continue
		}
		keys = append(keys, store.ImportedAPIKey{
			Source: "better-auth:" + row["id"].String, Name: apiKeyName(row["name"].String, row["id"].String),
			Prefix: row["start"].String, Hash: hash, Scopes: scopes,
			CreatedAt: sourceTime(row["createdAt"], time.Now()), ExpiresAt: optionalTime(row["expiresAt"]),
			Enabled: !row["enabled"].Valid || boolOf(row["enabled"]),
		})
	}
	rows, err = optionalTable(ctx, conn, "SELECT * FROM api_key ORDER BY id")
	if err != nil {
		return fmt.Errorf("read API keys: %w", err)
	}
	for _, row := range rows {
		r.Source++
		id := int32(intOf(row["id"]))
		hash := row["key"].String
		if !strings.HasPrefix(hash, "$2") {
			r.Skipped = append(r.Skipped, Skipped{SourceID: id, Name: row["name"].String, Reason: "the stored key isn't a bcrypt hash"})
			continue
		}
		keys = append(keys, store.ImportedAPIKey{
			Source: "legacy:" + strconv.Itoa(int(id)), Name: apiKeyName(row["name"].String, strconv.Itoa(int(id))),
			Prefix: "uk" + strconv.Itoa(int(id)) + "_", LegacyID: &id, LegacyHash: hash, Scopes: scopes,
			CreatedAt: sourceTime(row["created_date"], time.Now()), ExpiresAt: optionalTime(row["expires"]),
			Enabled: boolOf(row["active"]),
		})
	}
	for _, k := range keys {
		if !k.Enabled {
			r.Disabled++
		}
		if target == nil {
			r.New++
			continue
		}
		created, err := target.ImportAPIKey(ctx, k)
		if err != nil {
			return err
		}
		if created {
			r.New++
		} else {
			r.Updated++
		}
	}
	return nil
}

func apiKeyName(name, id string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "Imported key " + id
	}
	if len(name) > 100 {
		name = name[:100]
	}
	return name
}

func optionalTime(v sql.NullString) *time.Time {
	if !v.Valid || v.String == "" {
		return nil
	}
	t := sourceTime(v, time.Time{})
	if t.IsZero() {
		return nil
	}
	return &t
}

// copyAlertState copies which certificate expiry alerts were sent, the certificates last seen,
// and what was known of domains' expiry, for every monitor that's here.
func copyAlertState(ctx context.Context, conn *sql.Conn, target Target, monitors map[int32]int32, report *Report) error {
	r := &report.AlertState
	rows, err := optionalTable(ctx, conn, "SELECT monitor_id, days FROM notification_sent_history WHERE type = 'certificate'")
	if err != nil {
		return fmt.Errorf("read sent alerts: %w", err)
	}
	days := map[int32][]int{}
	for _, row := range rows {
		// In a dry run, monitors have no id here yet: they're counted under 0.
		if id, ok := monitors[int32(intOf(row["monitor_id"]))]; ok && (id != 0 || target == nil) {
			days[id] = append(days[id], int(intOf(row["days"])))
		}
	}
	for id, list := range days {
		if target == nil {
			r.CertAlerts += int64(len(list))
			continue
		}
		n, err := target.ImportCertAlerts(ctx, id, list)
		if err != nil {
			return err
		}
		r.CertAlerts += n
	}

	rows, err = optionalTable(ctx, conn, "SELECT monitor_id, info_json FROM monitor_tls_info ORDER BY id")
	if err != nil {
		return fmt.Errorf("read certificates: %w", err)
	}
	for _, row := range rows {
		id, ok := monitors[int32(intOf(row["monitor_id"]))]
		if !ok || (id == 0 && target != nil) || !row["info_json"].Valid {
			continue
		}
		var info struct {
			CertInfo struct {
				Fingerprint256 string `json:"fingerprint256"`
			} `json:"certInfo"`
		}
		if json.Unmarshal([]byte(row["info_json"].String), &info) != nil || info.CertInfo.Fingerprint256 == "" {
			continue
		}
		if target == nil {
			r.Certificates++
			continue
		}
		saved, err := target.ImportTLSInfo(ctx, id, row["info_json"].String, info.CertInfo.Fingerprint256)
		if err != nil {
			return err
		}
		if saved {
			r.Certificates++
		}
	}

	rows, err = optionalTable(ctx, conn, "SELECT * FROM domain_expiry")
	if err != nil {
		return fmt.Errorf("read domain expiry: %w", err)
	}
	for _, row := range rows {
		domain := strings.ToLower(strings.TrimSpace(row["domain"].String))
		if domain == "" {
			continue
		}
		record := domains.Record{Domain: domain, Expiry: optionalTime(row["expiry"]), LastCheck: optionalTime(row["last_check"])}
		if row["last_expiry_notification_sent"].Valid {
			n := int(intOf(row["last_expiry_notification_sent"]))
			record.LastNotifiedDays = &n
		}
		if target == nil {
			r.Domains++
			continue
		}
		saved, err := target.ImportDomainExpiry(ctx, record)
		if err != nil {
			return err
		}
		if saved {
			r.Domains++
		}
	}
	return nil
}

// keepSkipped records the skipped monitors and keeps their statistics. Their beats are kept by
// copyBeats.
func keepSkipped(ctx context.Context, conn *sql.Conn, target Target, report *Report) (map[int32]bool, error) {
	ids := map[int32]bool{}
	for _, s := range report.Skipped {
		ids[s.SourceID] = true
		if target != nil {
			if err := target.KeepSkippedMonitor(ctx, s.SourceID, s.Name, s.Reason); err != nil {
				return nil, err
			}
		}
	}
	report.SkippedHistory.Monitors = len(ids)
	for _, daily := range []bool{false, true} {
		table, count := "stat_hourly", &report.SkippedHistory.HourlyStats
		if daily {
			table, count = "stat_daily", &report.SkippedHistory.DailyStats
		}
		for id := range ids {
			rows, err := optionalTable(ctx, conn, "SELECT * FROM "+table+" WHERE monitor_id = ? ORDER BY `timestamp`", id)
			if err != nil {
				return nil, fmt.Errorf("read %s: %w", table, err)
			}
			stats := make([]store.ImportedStat, 0, len(rows))
			for _, row := range rows {
				stats = append(stats, convertStat(row, id))
			}
			*count += int64(len(stats))
			if target != nil {
				if err := target.ReplaceSkippedStats(ctx, daily, id, stats); err != nil {
					return nil, err
				}
			}
		}
	}
	return ids, nil
}
