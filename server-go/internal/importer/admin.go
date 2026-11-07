package importer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base32"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/chacha20poly1305"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/maintenance"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// The account, status pages and maintenance windows. Status pages and windows are copied once:
// one already copied is left alone, so that changes made here are kept. Deleting it here lets
// the next import copy it again.

// AccountReport says what happened to the account. It holds no password or key.
type AccountReport struct {
	// Source is how many accounts the source has. Node.js used the first one.
	Source   int    `json:"source"`
	Username string `json:"username,omitempty"`
	// RenamedFrom is the source's username, when it started with @, which is kept for
	// accounts that log in with GitHub.
	RenamedFrom string `json:"renamedFrom,omitempty"`
	TwoFactor   bool   `json:"twoFactor"`
	Imported    bool   `json:"imported"`
	// Skipped says why the account wasn't imported.
	Skipped string `json:"skipped,omitempty"`
}

// StatusPageReport says what happened to status pages.
type StatusPageReport struct {
	Source          int       `json:"source"`
	Imported        int       `json:"imported"`
	AlreadyImported int       `json:"alreadyImported"`
	Skipped         []Skipped `json:"skipped"`
	Monitors        int       `json:"monitors"`
	// MonitorsLeftOut were on a page, but weren't imported.
	MonitorsLeftOut int `json:"monitorsLeftOut"`
	Incidents       int `json:"incidents"`
	Domains         int `json:"domains"`
	// DomainsTaken are custom domains another page here already shows.
	DomainsTaken int `json:"domainsTaken"`
	Logos        int `json:"logos"`
	// LogosMissing were uploaded logos that couldn't be read from SOURCE_DATA_DIR.
	LogosMissing int `json:"logosMissing"`
}

// MaintenanceReport says what happened to maintenance windows.
type MaintenanceReport struct {
	Source          int       `json:"source"`
	Imported        int       `json:"imported"`
	AlreadyImported int       `json:"alreadyImported"`
	Skipped         []Skipped `json:"skipped"`
	MonitorLinks    int       `json:"monitorLinks"`
	MonitorsLeftOut int       `json:"monitorsLeftOut"`
	StatusPageLinks int       `json:"statusPageLinks"`
	// ServerTimezone is the source's timezone, which windows set to the server's are given.
	ServerTimezone string `json:"serverTimezone,omitempty"`
}

// errNoAuthSecret is the reason an account with two-factor login on isn't imported without
// the key that decrypts its authenticator key.
const errNoAuthSecret = "two-factor login is on, and SOURCE_AUTH_SECRET isn't set to decrypt its key. " +
	"Importing the account without it would let the password alone log in"

// copyAccount copies the source's first account, unless there's one here.
func copyAccount(ctx context.Context, conn *sql.Conn, opts Options, report *Report) error {
	account, err := readAccount(ctx, conn, opts.AuthSecret, &report.Account)
	if err != nil || account == nil {
		return err
	}
	if strings.HasPrefix(account.Username, store.GitHubAccountPrefix) {
		report.Account.RenamedFrom = account.Username
		account.Username = strings.TrimLeft(account.Username, store.GitHubAccountPrefix)
		if strings.TrimSpace(account.Username) == "" {
			account.Username = "admin"
		}
	}
	report.Account.Username = account.Username
	report.Account.TwoFactor = account.TOTPSecret != nil
	if opts.Target == nil {
		return nil
	}
	created, err := opts.Target.ImportAccount(ctx, *account)
	if err != nil {
		return err
	}
	report.Account.Imported = created
	if !created {
		report.Account.Skipped = "an account exists here already"
	}
	return nil
}

// readAccount reads better-auth's account, or the older user table's when nobody has logged
// in since better-auth arrived. A reason to leave it is put in the report.
func readAccount(ctx context.Context, conn *sql.Conn, authSecret string, report *AccountReport) (*store.ImportedAccount, error) {
	users, err := optionalTable(ctx, conn, "SELECT * FROM better_auth_user ORDER BY createdAt, id")
	if err != nil {
		return nil, fmt.Errorf("read accounts: %w", err)
	}
	if len(users) == 0 {
		return readLegacyAccount(ctx, conn, report)
	}
	report.Source = len(users)
	user := users[0]
	username := strings.TrimSpace(user["username"].String)
	if username == "" {
		username = strings.TrimSpace(user["name"].String)
	}
	accounts, _, err := readRows(ctx, conn,
		"SELECT password FROM better_auth_account WHERE userId = ? AND providerId = 'credential' AND password IS NOT NULL", user["id"].String)
	if err != nil {
		return nil, fmt.Errorf("read the account's password: %w", err)
	}
	if len(accounts) == 0 {
		report.Skipped = "the account has no password"
		return nil, nil
	}
	account := &store.ImportedAccount{Username: username, PasswordHash: accounts[0]["password"].String}
	if !strings.Contains(account.PasswordHash, ":") {
		report.Skipped = "the password hash isn't better-auth's"
		return nil, nil
	}
	if !boolOf(user["twoFactorEnabled"]) {
		return account, nil
	}
	rows, err := optionalTable(ctx, conn, "SELECT secret, verified FROM better_auth_twoFactor WHERE userId = ?", user["id"].String)
	if err != nil {
		return nil, fmt.Errorf("read the two-factor key: %w", err)
	}
	if len(rows) == 0 || !boolOf(rows[0]["verified"]) {
		// better-auth asks for a code only once the key is verified.
		return account, nil
	}
	if authSecret == "" {
		report.TwoFactor = true
		report.Skipped = errNoAuthSecret
		return nil, nil
	}
	secret, err := decryptTOTPSecret(authSecret, rows[0]["secret"].String)
	if err != nil {
		report.TwoFactor = true
		report.Skipped = "the two-factor key can't be decrypted: " + err.Error()
		return nil, nil
	}
	account.TOTPSecret = secret
	return account, nil
}

// readLegacyAccount reads the user table that came before better-auth. Its password is a bcrypt
// hash, which the first login here replaces, and its two-factor key isn't encrypted.
func readLegacyAccount(ctx context.Context, conn *sql.Conn, report *AccountReport) (*store.ImportedAccount, error) {
	users, err := optionalTable(ctx, conn, "SELECT * FROM `user` WHERE active = 1 ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read the older user table: %w", err)
	}
	report.Source = len(users)
	if len(users) == 0 {
		return nil, nil
	}
	user := users[0]
	account := &store.ImportedAccount{Username: strings.TrimSpace(user["username"].String), PasswordHash: user["password"].String}
	if !strings.HasPrefix(account.PasswordHash, "$2") {
		report.Skipped = "the password hash isn't bcrypt"
		return nil, nil
	}
	if boolOf(user["twofa_status"]) && user["twofa_secret"].String != "" {
		account.TOTPSecret = []byte(user["twofa_secret"].String)
	}
	return account, nil
}

// decryptTOTPSecret undoes better-auth's symmetricEncrypt: XChaCha20-Poly1305 under the SHA-256
// of the auth secret, with the nonce first, in hex, maybe inside a "$ba$<version>$" envelope.
// The key is the decrypted text's bytes, or base32 after a "base32:" prefix.
func decryptTOTPSecret(authSecret, stored string) ([]byte, error) {
	if rest, found := strings.CutPrefix(stored, "$ba$"); found {
		if _, ciphertext, ok := strings.Cut(rest, "$"); ok {
			stored = ciphertext
		}
	}
	data, err := hex.DecodeString(stored)
	if err != nil || len(data) < chacha20poly1305.NonceSizeX+chacha20poly1305.Overhead {
		return nil, errors.New("it isn't better-auth's format")
	}
	key := sha256.Sum256([]byte(authSecret))
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, err
	}
	plain, err := aead.Open(nil, data[:chacha20poly1305.NonceSizeX], data[chacha20poly1305.NonceSizeX:], nil)
	if err != nil {
		return nil, errors.New("SOURCE_AUTH_SECRET isn't the key it was encrypted with")
	}
	if encoded, found := bytes.CutPrefix(plain, []byte("base32:")); found {
		decoded, err := base32.StdEncoding.WithPadding(base32.NoPadding).DecodeString(strings.ToUpper(strings.TrimRight(string(encoded), "=")))
		if err != nil {
			return nil, errors.New("its base32 key isn't valid")
		}
		return decoded, nil
	}
	if len(plain) == 0 {
		return nil, errors.New("it's empty")
	}
	return plain, nil
}

var uploadedLogo = regexp.MustCompile(`^/upload/(logo\d+\.png)(\?.*)?$`)

// copyStatusPages copies the status pages that weren't copied before. monitors maps the source's
// monitor ids to this service's, for every monitor that was imported. It returns the source's
// page ids mapped to this service's, including pages copied before.
func copyStatusPages(ctx context.Context, conn *sql.Conn, opts Options, monitors map[int32]int32, report *Report) (map[int32]int32, error) {
	r := &report.StatusPages
	pages, err := optionalTable(ctx, conn, "SELECT * FROM status_page ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read status pages: %w", err)
	}
	r.Source = len(pages)
	ids := map[int32]int32{}
	if opts.Target != nil {
		if ids, err = opts.Target.ImportedStatusPages(ctx); err != nil {
			return nil, err
		}
	}
	domains, err := sourceRowsBy(ctx, conn, "SELECT status_page_id, domain FROM status_page_cname ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read custom domains: %w", err)
	}
	groups, err := sourceRowsBy(ctx, conn, "SELECT * FROM `group` WHERE status_page_id IS NOT NULL ORDER BY weight, id")
	if err != nil {
		return nil, fmt.Errorf("read status page groups: %w", err)
	}
	groupMonitors, err := sourceRowsBy(ctx, conn, "SELECT g.status_page_id, mg.* FROM monitor_group mg JOIN `group` g ON g.id = mg.group_id ORDER BY mg.weight, mg.id")
	if err != nil {
		return nil, fmt.Errorf("read status page monitors: %w", err)
	}
	incidents, err := sourceRowsBy(ctx, conn, "SELECT * FROM incident WHERE status_page_id IS NOT NULL ORDER BY created_date, id")
	if err != nil {
		return nil, fmt.Errorf("read incidents: %w", err)
	}

	for _, row := range pages {
		sourceID := int32(intOf(row["id"]))
		if _, done := ids[sourceID]; done {
			r.AlreadyImported++
			continue
		}
		page := convertStatusPage(row)
		for _, d := range domains[sourceID] {
			if domain := strings.ToLower(strings.TrimSpace(d["domain"].String)); domain != "" {
				page.Page.Domains = append(page.Page.Domains, domain)
			}
		}
		var leftOut, onPage int
		for _, g := range groups[sourceID] {
			group := store.StatusPageGroup{Name: g["name"].String}
			for _, m := range groupMonitors[sourceID] {
				if m["group_id"].String != g["id"].String {
					continue
				}
				target, found := monitors[int32(intOf(m["monitor_id"]))]
				if !found {
					leftOut++
					continue
				}
				var customURL *string
				if u := m["custom_url"].String; u != "" {
					customURL = &u
				}
				group.Monitors = append(group.Monitors, store.StatusPageMonitor{ID: target, SendURL: boolOf(m["send_url"]), CustomURL: customURL})
				onPage++
			}
			page.Groups = append(page.Groups, group)
		}
		for _, i := range incidents[sourceID] {
			incident := store.Incident{
				Title: i["title"].String, Content: i["content"].String, Style: i["style"].String,
				Pin: boolOf(i["pin"]), Active: boolOf(i["active"]), CreatedAt: sourceTime(i["created_date"], time.Now()),
			}
			if i["last_updated_date"].Valid {
				at := sourceTime(i["last_updated_date"], incident.CreatedAt)
				incident.UpdatedAt = &at
			}
			page.Incidents = append(page.Incidents, incident)
		}
		if match := uploadedLogo.FindStringSubmatch(row["icon"].String); match != nil {
			page.Page.Icon = ""
			if logo, err := readLogo(opts.DataDir, match[1]); err == nil {
				page.Logo = logo
			} else {
				r.LogosMissing++
			}
		}

		if opts.Target != nil {
			id, taken, err := opts.Target.ImportStatusPage(ctx, sourceID, page)
			if errors.Is(err, store.ErrSlugTaken) {
				r.Skipped = append(r.Skipped, Skipped{SourceID: sourceID, Name: page.Page.Title,
					Reason: "another status page here has the slug " + page.Page.Slug})
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("status page %d: %w", sourceID, err)
			}
			ids[sourceID] = id
			r.DomainsTaken += taken
		} else {
			ids[sourceID] = 0
		}
		r.Imported++
		r.Monitors += onPage
		r.MonitorsLeftOut += leftOut
		r.Incidents += len(page.Incidents)
		r.Domains += len(page.Page.Domains)
		if page.Logo != nil {
			r.Logos++
		}
	}
	return ids, nil
}

// readLogo reads an uploaded logo from the Node.js data directory, which only has PNGs.
func readLogo(dataDir, name string) ([]byte, error) {
	if dataDir == "" {
		return nil, errors.New("no data directory")
	}
	data, err := os.ReadFile(filepath.Join(dataDir, "upload", name))
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(data, []byte("\x89PNG\r\n\x1a\n")) || len(data) > 3<<20 {
		return nil, errors.New("not a PNG of at most 3 MB")
	}
	return data, nil
}

func convertStatusPage(row map[string]sql.NullString) store.ImportedPage {
	optional := func(key string) *string {
		if v := strings.TrimSpace(row[key].String); v != "" {
			return &v
		}
		return nil
	}
	page := store.StatusPage{
		Slug:                  strings.ToLower(strings.TrimSpace(row["slug"].String)),
		Title:                 row["title"].String,
		Description:           row["description"].String,
		Icon:                  sourceIcon(row["icon"].String),
		Theme:                 row["theme"].String,
		AutoRefreshInterval:   300,
		ShowTags:              boolOf(row["show_tags"]),
		FooterText:            row["footer_text"].String,
		CustomCSS:             row["custom_css"].String,
		ShowPoweredBy:         !row["show_powered_by"].Valid || boolOf(row["show_powered_by"]),
		ShowCertificateExpiry: boolOf(row["show_certificate_expiry"]),
		ShowOnlyLastHeartbeat: boolOf(row["show_only_last_heartbeat"]),
		RSSTitle:              row["rss_title"].String,
		AnalyticsType:         optional("analytics_type"),
		AnalyticsID:           optional("analytics_id"),
		AnalyticsScriptURL:    optional("analytics_script_url"),
	}
	if page.Theme == "" {
		page.Theme = "auto"
	}
	if row["auto_refresh_interval"].Valid {
		page.AutoRefreshInterval = int32(intOf(row["auto_refresh_interval"]))
	}
	// Before other analytics, the ID was Google's.
	if id := optional("google_analytics_tag_id"); id != nil && page.AnalyticsID == nil {
		google := "google"
		page.AnalyticsType, page.AnalyticsID = &google, id
	}
	return store.ImportedPage{Page: page, CreatedAt: sourceTime(row["created_date"], time.Now())}
}

// sourceIcon keeps a logo given as an address. The default icon and uploaded logos aren't kept
// as addresses: the default is empty here, and uploads are copied.
func sourceIcon(icon string) string {
	icon = strings.TrimSpace(icon)
	if icon == "" || icon == "/icon.svg" || uploadedLogo.MatchString(icon) {
		return ""
	}
	u, err := url.Parse(icon)
	if err != nil || strings.ContainsAny(icon, "\"'<>\\ \t\r\n") {
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" || (u.Scheme == "" && strings.HasPrefix(icon, "/") && !strings.HasPrefix(icon, "//")) {
		return icon
	}
	return ""
}

// sourceRowsBy reads rows grouped by their first column, a status page's id. A missing table
// gives none.
func sourceRowsBy(ctx context.Context, conn *sql.Conn, query string) (map[int32][]map[string]sql.NullString, error) {
	rows, err := optionalTable(ctx, conn, query)
	if err != nil {
		return nil, err
	}
	by := map[int32][]map[string]sql.NullString{}
	for _, row := range rows {
		id := int32(intOf(row["status_page_id"]))
		by[id] = append(by[id], row)
	}
	return by, nil
}

// sourceTime reads a datetime that Node.js stored in UTC.
func sourceTime(v sql.NullString, fallback time.Time) time.Time {
	if !v.Valid {
		return fallback
	}
	t, err := time.ParseInLocation(datetimeLayout, v.String, time.UTC)
	if err != nil {
		return fallback
	}
	return t
}

// copyMaintenance copies the maintenance windows that weren't copied before.
func copyMaintenance(ctx context.Context, conn *sql.Conn, opts Options, monitors, pages map[int32]int32, report *Report) error {
	r := &report.Maintenance
	windows, err := optionalTable(ctx, conn, "SELECT * FROM maintenance ORDER BY id")
	if err != nil {
		return fmt.Errorf("read maintenance windows: %w", err)
	}
	r.Source = len(windows)
	done := map[int32]int32{}
	if opts.Target != nil {
		if done, err = opts.Target.ImportedMaintenances(ctx); err != nil {
			return err
		}
	}
	serverZone, err := sourceServerTimezone(ctx, conn)
	if err != nil {
		return err
	}
	r.ServerTimezone = serverZone
	monitorLinks, err := linksBy(ctx, conn, "SELECT maintenance_id, monitor_id AS id FROM monitor_maintenance ORDER BY id")
	if err != nil {
		return fmt.Errorf("read maintenance monitors: %w", err)
	}
	pageLinks, err := linksBy(ctx, conn, "SELECT maintenance_id, status_page_id AS id FROM maintenance_status_page ORDER BY id")
	if err != nil {
		return fmt.Errorf("read maintenance status pages: %w", err)
	}
	eval := maintenance.Evaluator{Server: time.UTC}

	for _, row := range windows {
		sourceID := int32(intOf(row["id"]))
		if _, ok := done[sourceID]; ok {
			r.AlreadyImported++
			continue
		}
		w, reason := convertWindow(row, serverZone)
		if reason == "" {
			if err := eval.Validate(w); err != nil {
				reason = "invalid window: " + err.Error()
			}
		}
		if reason != "" {
			r.Skipped = append(r.Skipped, Skipped{SourceID: sourceID, Name: w.Title, Reason: reason})
			continue
		}
		var monitorIDs, pageIDs []int32
		leftOut := 0
		for _, id := range monitorLinks[sourceID] {
			if target, ok := monitors[id]; ok {
				monitorIDs = append(monitorIDs, target)
			} else {
				leftOut++
			}
		}
		for _, id := range pageLinks[sourceID] {
			if target, ok := pages[id]; ok {
				pageIDs = append(pageIDs, target)
			}
		}
		if opts.Target != nil {
			if _, err := opts.Target.ImportMaintenance(ctx, sourceID, w, monitorIDs, pageIDs); err != nil {
				return fmt.Errorf("maintenance window %d: %w", sourceID, err)
			}
		}
		r.Imported++
		r.MonitorLinks += len(monitorIDs)
		r.MonitorsLeftOut += leftOut
		r.StatusPageLinks += len(pageIDs)
	}
	return nil
}

func linksBy(ctx context.Context, conn *sql.Conn, query string) (map[int32][]int32, error) {
	rows, err := optionalTable(ctx, conn, query)
	if err != nil {
		return nil, err
	}
	links := map[int32][]int32{}
	for _, row := range rows {
		id := int32(intOf(row["maintenance_id"]))
		links[id] = append(links[id], int32(intOf(row["id"])))
	}
	return links, nil
}

// sourceServerTimezone is the timezone the Node.js server was set to, which its windows set to
// "same as server" used. Empty when it wasn't set, or isn't a known timezone.
func sourceServerTimezone(ctx context.Context, conn *sql.Conn) (string, error) {
	rows, err := optionalTable(ctx, conn, "SELECT value FROM setting WHERE `key` = 'serverTimezone'")
	if err != nil || len(rows) == 0 {
		return "", err
	}
	value := rows[0]["value"].String
	var decoded string
	if json.Unmarshal([]byte(value), &decoded) == nil {
		value = decoded
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if _, err := time.LoadLocation(value); err != nil {
		return "", nil
	}
	return value, nil
}

// convertWindow reads a window. Node.js stored the dates as the form gave them, in the window's
// timezone, and the times of day with seconds.
func convertWindow(row map[string]sql.NullString, serverZone string) (maintenance.Window, string) {
	w := maintenance.Window{
		Title:       strings.TrimSpace(row["title"].String),
		Description: row["description"].String,
		Strategy:    row["strategy"].String,
		Active:      boolOf(row["active"]),
		StartDate:   windowDate(row["start_date"]),
		EndDate:     windowDate(row["end_date"]),
		IntervalDay: int(intOf(row["interval_day"])),
		Cron:        strings.TrimSpace(row["cron"].String),
		Timezone:    strings.TrimSpace(row["timezone"].String),
		HolidayMode: maintenance.HolidaysNone,
	}
	if w.IntervalDay < 1 {
		w.IntervalDay = 1
	}
	if w.Timezone == "" || w.Timezone == maintenance.SameAsServer {
		w.Timezone = maintenance.SameAsServer
		if serverZone != "" {
			w.Timezone = serverZone
		}
	}
	// Node.js generated a cron expression for recurring windows, which this service doesn't use.
	if w.Strategy != maintenance.StrategyCron {
		w.Cron = ""
	}
	if w.Strategy == maintenance.StrategyCron {
		w.DurationMinutes = int(intOf(row["duration"]) / 60)
	}
	if strings.HasPrefix(w.Strategy, "recurring-") {
		w.StartTime, w.EndTime = windowClock(row["start_time"]), windowClock(row["end_time"])
	}
	if raw := strings.TrimSpace(row["weekdays"].String); raw != "" {
		if err := json.Unmarshal([]byte(raw), &w.Weekdays); err != nil {
			return w, "invalid setting: weekdays"
		}
	}
	if raw := strings.TrimSpace(row["days_of_month"].String); raw != "" {
		var days []json.RawMessage
		if err := json.Unmarshal([]byte(raw), &days); err != nil {
			return w, "invalid setting: days of the month"
		}
		for _, d := range days {
			day, err := dayOfMonth(d)
			if err != nil {
				return w, "invalid setting: " + err.Error()
			}
			w.DaysOfMonth = append(w.DaysOfMonth, day)
		}
	}
	return w, ""
}

// windowDate turns "2006-01-02 15:04:05" into "2006-01-02T15:04".
func windowDate(v sql.NullString) string {
	if !v.Valid || v.String == "" {
		return ""
	}
	t, err := time.Parse(datetimeLayout, v.String)
	if err != nil {
		return v.String // Validate rejects it.
	}
	return t.Format("2006-01-02T15:04")
}

// windowClock turns "15:04:05" into "15:04".
func windowClock(v sql.NullString) string {
	parts := strings.Split(v.String, ":")
	if len(parts) < 2 {
		return v.String
	}
	return parts[0] + ":" + parts[1]
}

// dayOfMonth reads a day, 1 to 31, or "lastDay1" to "lastDay4" as -1 to -4.
func dayOfMonth(raw json.RawMessage) (int, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, err
	}
	switch v := value.(type) {
	case float64:
		return int(v), nil
	case string:
		if rest, found := strings.CutPrefix(v, "lastDay"); found {
			n, err := strconv.Atoi(rest)
			if err == nil && n >= 1 && n <= 4 {
				return -n, nil
			}
		}
		if n, err := strconv.Atoi(v); err == nil {
			return n, nil
		}
		return 0, fmt.Errorf("day of the month %q", v)
	}
	return 0, fmt.Errorf("day of the month %s", raw)
}
