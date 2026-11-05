// Package importer copies monitors and history from an Uptime Kuma MariaDB database.
//
// It reads the source inside a read-only transaction, so the database itself refuses any
// write. A monitor that this service can't check the same way is skipped and reported, because
// importing it without the setting would show false results. Imports are repeatable: monitors
// that have since been resumed here are left alone, because they now produce their own data.
package importer

import (
	"cmp"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	beatBatch      = 5000
	datetimeLayout = "2006-01-02 15:04:05.999999"
)

// requiredMonitorColumns have been in the source schema for a long time. Newer columns are
// optional, so that an older installation can still be read.
var requiredMonitorColumns = []string{
	"id", "name", "active", "interval", "url", "type", "hostname", "port", "keyword",
	"maxretries", "ignore_tls", "upside_down", "maxredirects", "accepted_statuscodes_json",
	"retry_interval", "method", "body", "headers", "parent",
}

// Target is where imported data goes.
type Target interface {
	ImportedMonitors(ctx context.Context) (map[int32]store.ImportedMonitor, error)
	ImportMonitor(ctx context.Context, sourceID int32, m monitor.Monitor, sourceActive bool, cutover time.Time) (int32, error)
	SetImportedParent(ctx context.Context, id int32, parent *int32) error
	ReplaceImportedStats(ctx context.Context, daily bool, monitorID int32, stats []store.ImportedStat) error
	InsertBeats(ctx context.Context, beats []store.Beat) ([]store.Beat, error)
	RefreshAggregates(ctx context.Context) error
	ActivateImported(ctx context.Context) (int64, error)
	ImportedNotifications(ctx context.Context) (map[int32]store.ImportedNotification, error)
	ImportNotification(ctx context.Context, sourceID int32, n store.Notification, sourceActive bool) (int32, error)
	SetMonitorNotifications(ctx context.Context, monitorID int32, ids []int32) error
	ImportTag(ctx context.Context, name, color string) (int32, error)
	AddMonitorTag(ctx context.Context, tagID, monitorID int32, value string) error
	ActivateImportedNotifications(ctx context.Context) (int64, error)
}

// Options controls an import. A nil Target means a dry run, which writes nothing.
type Options struct {
	Source   *sql.DB
	Target   Target
	Activate bool
	Now      func() time.Time
	Log      *slog.Logger
}

// Skipped is a monitor that wasn't imported.
type Skipped struct {
	SourceID int32  `json:"sourceId"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
}

// Report says what an import did, or what a dry run would do. It holds no credentials.
type Report struct {
	DryRun          bool               `json:"dryRun"`
	SourceMonitors  int                `json:"sourceMonitors"`
	Imported        int                `json:"imported"`
	AlreadyActive   int                `json:"alreadyActive"`
	Skipped         []Skipped          `json:"skipped"`
	SkippedByReason map[string]int     `json:"skippedByReason"`
	DroppedSettings map[string]int     `json:"droppedSettings"`
	Tags            int                `json:"tags"`
	TagLinks        int                `json:"tagLinks"`
	WithAlerts      int                `json:"importedWithNotifications"`
	Beats           int64              `json:"beats"`
	NewBeats        int64              `json:"newBeats"`
	InvalidBeats    int64              `json:"invalidBeats"`
	HourlyStats     int64              `json:"hourlyStats"`
	DailyStats      int64              `json:"dailyStats"`
	Activated       int64              `json:"activated"`
	Notifications   NotificationReport `json:"notifications"`
	Cutover         time.Time          `json:"cutover"`
}

// NotificationReport says what happened to notification settings. Never their config.
type NotificationReport struct {
	Source        int       `json:"source"`
	Imported      int       `json:"imported"`
	AlreadyActive int       `json:"alreadyActive"`
	Skipped       []Skipped `json:"skipped"`
	Links         int       `json:"links"`
	// MonitorsLosingAlerts are imported monitors using a notification that wasn't imported.
	MonitorsLosingAlerts int   `json:"monitorsLosingAlerts"`
	Activated            int64 `json:"activated"`
}

type sourceNotification struct {
	id           int32
	active       bool
	notification store.Notification
}

type sourceMonitor struct {
	id       int32
	parent   *int32
	active   bool
	monitor  monitor.Monitor
	targetID int32
}

// Run performs the import.
func Run(ctx context.Context, opts Options) (*Report, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	// Whole hours before this come from the source's aggregates, and everything after from beats.
	cutover := now().UTC().Truncate(time.Hour)
	report := &Report{
		DryRun:          opts.Target == nil,
		Skipped:         []Skipped{},
		SkippedByReason: map[string]int{},
		DroppedSettings: map[string]int{},
		Cutover:         cutover,
		Notifications:   NotificationReport{Skipped: []Skipped{}},
	}

	conn, err := opts.Source.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to the source: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
		return nil, fmt.Errorf("start a read-only transaction: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()

	monitors, err := readMonitors(ctx, conn, report)
	if err != nil {
		return nil, err
	}
	withAlerts, err := monitorsWithNotifications(ctx, conn)
	if err != nil {
		return nil, err
	}

	existing := map[int32]store.ImportedMonitor{}
	if opts.Target != nil {
		if existing, err = opts.Target.ImportedMonitors(ctx); err != nil {
			return nil, err
		}
	}

	// Monitors, then parents, because a parent's id may not exist yet.
	active := make([]*sourceMonitor, 0, len(monitors))
	for _, m := range monitors {
		if withAlerts[m.id] {
			report.WithAlerts++
		}
		if previous, ok := existing[m.id]; ok && previous.Active {
			report.AlreadyActive++
			m.targetID = previous.MonitorID
			continue
		}
		if opts.Target != nil {
			id, err := opts.Target.ImportMonitor(ctx, m.id, m.monitor, m.active, cutover)
			if err != nil {
				return nil, fmt.Errorf("monitor %d: %w", m.id, err)
			}
			m.targetID = id
		}
		report.Imported++
		active = append(active, m)
	}
	byID := make(map[int32]*sourceMonitor, len(monitors))
	for _, m := range monitors {
		byID[m.id] = m
	}
	if opts.Target != nil {
		for _, m := range active {
			var parent *int32
			if m.parent != nil && !wouldCycle(byID, m) {
				if p := byID[*m.parent]; p != nil {
					parent = &p.targetID
				}
			}
			if err := opts.Target.SetImportedParent(ctx, m.targetID, parent); err != nil {
				return nil, fmt.Errorf("monitor %d: %w", m.id, err)
			}
		}
	}

	targets := make(map[int32]int32, len(active))
	for _, m := range active {
		targets[m.id] = m.targetID
	}
	log.Info("monitors done", "imported", report.Imported, "skipped", len(report.Skipped), "alreadyActive", report.AlreadyActive)

	if err := copyNotifications(ctx, conn, opts.Target, active, report); err != nil {
		return nil, err
	}
	log.Info("notifications done", "imported", report.Notifications.Imported, "skipped", len(report.Notifications.Skipped), "links", report.Notifications.Links)

	if err := copyTags(ctx, conn, opts.Target, targets, report); err != nil {
		return nil, err
	}
	log.Info("tags done", "tags", report.Tags, "links", report.TagLinks)

	if err := copyStats(ctx, conn, opts.Target, false, targets, cutover, &report.HourlyStats); err != nil {
		return nil, err
	}
	if err := copyStats(ctx, conn, opts.Target, true, targets, cutover.Truncate(24*time.Hour), &report.DailyStats); err != nil {
		return nil, err
	}
	log.Info("aggregates done", "hourly", report.HourlyStats, "daily", report.DailyStats)

	if err := copyBeats(ctx, conn, opts.Target, targets, report, log); err != nil {
		return nil, err
	}

	if opts.Target != nil {
		if err := opts.Target.RefreshAggregates(ctx); err != nil {
			return nil, err
		}
		if opts.Activate {
			if report.Activated, err = opts.Target.ActivateImported(ctx); err != nil {
				return nil, err
			}
			if report.Notifications.Activated, err = opts.Target.ActivateImportedNotifications(ctx); err != nil {
				return nil, err
			}
		}
	}
	return report, nil
}

func wouldCycle(byID map[int32]*sourceMonitor, m *sourceMonitor) bool {
	seen := map[int32]bool{m.id: true}
	for current := m; current != nil && current.parent != nil; current = byID[*current.parent] {
		if seen[*current.parent] {
			return true
		}
		seen[*current.parent] = true
	}
	return false
}

// readRows returns the result as maps of column name to text, so that columns that an older
// schema doesn't have simply aren't there.
func readRows(ctx context.Context, conn *sql.Conn, query string, args ...any) ([]map[string]sql.NullString, []string, error) {
	rows, err := conn.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, nil, err
	}
	var result []map[string]sql.NullString
	values := make([]sql.NullString, len(columns))
	pointers := make([]any, len(columns))
	for i := range values {
		pointers[i] = &values[i]
	}
	for rows.Next() {
		if err := rows.Scan(pointers...); err != nil {
			return nil, nil, err
		}
		row := make(map[string]sql.NullString, len(columns))
		for i, column := range columns {
			row[column] = values[i]
		}
		result = append(result, row)
	}
	return result, columns, rows.Err()
}

func readMonitors(ctx context.Context, conn *sql.Conn, report *Report) ([]*sourceMonitor, error) {
	rows, columns, err := readRows(ctx, conn, "SELECT * FROM monitor ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read monitors: %w", err)
	}
	for _, column := range requiredMonitorColumns {
		if !slices.Contains(columns, column) {
			return nil, fmt.Errorf("the source's monitor table has no %q column, is this an Uptime Kuma database?", column)
		}
	}

	report.SourceMonitors = len(rows)
	var result []*sourceMonitor
	pushTokens := map[string]bool{}
	for _, row := range rows {
		id := int32(intOf(row["id"]))
		name := row["name"].String
		m, reason := convertMonitor(row)
		if reason != "" {
			report.Skipped = append(report.Skipped, Skipped{SourceID: id, Name: name, Reason: reason})
			report.SkippedByReason[skipCategory(reason)]++
			continue
		}
		for _, setting := range droppedSettings(row) {
			report.DroppedSettings[setting]++
		}
		if m.PushToken != "" {
			if pushTokens[m.PushToken] {
				reason := "invalid setting: another monitor has the same push token"
				report.Skipped = append(report.Skipped, Skipped{SourceID: id, Name: name, Reason: reason})
				report.SkippedByReason[skipCategory(reason)]++
				continue
			}
			pushTokens[m.PushToken] = true
		}
		source := &sourceMonitor{id: id, active: boolOf(row["active"]), monitor: m}
		if row["parent"].Valid {
			parent := int32(intOf(row["parent"]))
			source.parent = &parent
		}
		result = append(result, source)
	}
	return result, nil
}

func skipCategory(reason string) string {
	category, _, _ := strings.Cut(reason, ":")
	return category
}

// convertMonitor returns the monitor, or why it can't be imported.
func convertMonitor(row map[string]sql.NullString) (monitor.Monitor, string) {
	m := monitor.New()
	m.Name = strings.TrimSpace(row["name"].String)
	m.Type = row["type"].String
	m.Active = false
	m.Interval = int32(intOf(row["interval"]))
	m.RetryInterval = int32(intOf(row["retry_interval"]))
	m.Timeout = floatOf(row["timeout"])
	m.MaxRetries = int32(intOf(row["maxretries"]))
	m.UpsideDown = boolOf(row["upside_down"])
	m.ResendInterval = int32(max(intOf(row["resend_interval"]), 0))

	switch m.Type {
	case monitor.TypeGroup:
		return checked(m)
	case monitor.TypePort:
		m.Hostname = row["hostname"].String
		m.Port = int32(intOf(row["port"]))
		return checked(m)
	case monitor.TypePush:
		m.PushToken = row["push_token"].String
		return checked(m)
	case monitor.TypePing:
		m.Hostname = row["hostname"].String
		if row["ping_count"].Valid {
			m.PingCount = int32(intOf(row["ping_count"]))
		}
		if row["ping_per_request_timeout"].Valid {
			m.PingPerRequestTimeout = int32(intOf(row["ping_per_request_timeout"]))
		}
		if row["packet_size"].Valid {
			m.PacketSize = int32(intOf(row["packet_size"]))
		}
		return checked(m)
	case monitor.TypeDNS:
		m.Conditions = sourceConditions(row)
		m.Hostname = row["hostname"].String
		m.Port = int32(intOf(row["port"]))
		if row["dns_resolve_type"].String != "" {
			m.DNSResolveType = row["dns_resolve_type"].String
		}
		if row["dns_resolve_server"].String != "" {
			m.DNSResolveServer = row["dns_resolve_server"].String
		}
		return checked(m)
	case monitor.TypeManual:
		if row["manual_status"].Valid {
			status := int16(intOf(row["manual_status"]))
			m.ManualStatus = &status
		}
		return checked(m)
	case monitor.TypePostgres, monitor.TypeMySQL, monitor.TypeRedis:
		m.DatabaseConnectionString = strings.TrimSpace(row["database_connection_string"].String)
		if m.Type != monitor.TypeRedis {
			m.DatabaseQuery = row["database_query"].String
		}
		if m.Type == monitor.TypeMySQL {
			m.DatabasePassword = row["radius_password"].String
			m.Conditions = sourceConditions(row)
		}
		m.IgnoreTLS = boolOf(row["ignore_tls"])
		return checked(m)
	case monitor.TypeSQLServer, monitor.TypeOracle, monitor.TypeMongoDB:
		m.DatabaseConnectionString = strings.TrimSpace(row["database_connection_string"].String)
		m.DatabaseQuery = row["database_query"].String
		switch m.Type {
		case monitor.TypeOracle:
			m.DatabaseUsername = strings.TrimSpace(row["basic_auth_user"].String)
			m.DatabasePassword = row["basic_auth_pass"].String
			m.Conditions = sourceConditions(row)
		case monitor.TypeSQLServer:
			m.Conditions = sourceConditions(row)
		case monitor.TypeMongoDB:
			m.JSONPath = row["json_path"].String
			m.ExpectedValue = row["expected_value"].String
		}
		return checked(m)
	case monitor.TypeMQTT:
		m.Hostname = strings.TrimSpace(row["hostname"].String)
		m.Port = int32(intOf(row["port"]))
		m.MQTTTopic = row["mqtt_topic"].String
		m.MQTTUsername = row["mqtt_username"].String
		m.MQTTPassword = row["mqtt_password"].String
		m.MQTTWebsocketPath = row["mqtt_websocket_path"].String
		m.MQTTCheckType = cmp.Or(row["mqtt_check_type"].String, "keyword")
		m.MQTTSuccessMessage = row["mqtt_success_message"].String
		m.JSONPath = row["json_path"].String
		m.ExpectedValue = row["expected_value"].String
		m.Conditions = sourceConditions(row)
		return checked(m)
	case monitor.TypeRabbitMQ:
		if err := jsonList(row["rabbitmq_nodes"].String, &m.RabbitMQNodes); err != nil {
			return m, "invalid setting: RabbitMQ nodes aren't a JSON list"
		}
		m.RabbitMQUsername = row["rabbitmq_username"].String
		m.RabbitMQPassword = row["rabbitmq_password"].String
		return checked(m)
	case monitor.TypeKafka:
		if err := jsonList(row["kafka_producer_brokers"].String, &m.KafkaBrokers); err != nil {
			return m, "invalid setting: Kafka brokers aren't a JSON list"
		}
		m.KafkaTopic = row["kafka_producer_topic"].String
		m.KafkaMessage = row["kafka_producer_message"].String
		m.KafkaSSL = boolOf(row["kafka_producer_ssl"])
		m.KafkaAllowAutoTopicCreation = boolOf(row["kafka_producer_allow_auto_topic_creation"])
		if raw := strings.TrimSpace(row["kafka_producer_sasl_options"].String); raw != "" {
			var options monitor.KafkaSASL
			if err := json.Unmarshal([]byte(raw), &options); err != nil {
				return m, "invalid setting: Kafka SASL options aren't JSON"
			}
			if options.Mechanism == "aws" {
				return m, "unsupported setting: Kafka SASL mechanism aws"
			}
			if options.Mechanism != "" && options.Mechanism != "None" {
				m.KafkaSASL = &options
			}
		}
		return checked(m)
	case monitor.TypeNTP, monitor.TypeSNMP, monitor.TypeSMTP, monitor.TypeSIP, monitor.TypeRADIUS, monitor.TypeSteam:
		m.Hostname = strings.TrimSpace(row["hostname"].String)
		m.Port = int32(intOf(row["port"]))
		m.IgnoreTLS = boolOf(row["ignore_tls"])
		switch m.Type {
		case monitor.TypeNTP:
			m.NTPStratumThreshold = int32(intOf(row["ntp_stratum_threshold"]))
			m.NTPTimeOffsetThreshold = floatOf(row["ntp_time_offset_threshold"])
			m.NTPRootDispersionThreshold = floatOf(row["ntp_root_dispersion_threshold"])
		case monitor.TypeSNMP:
			m.SNMPOID = strings.TrimSpace(row["snmp_oid"].String)
			m.SNMPVersion = row["snmp_version"].String
			m.SNMPV3Username = row["snmp_v3_username"].String
			m.SNMPCommunity = row["radius_password"].String
			m.JSONPath = row["json_path"].String
			if op := row["json_path_operator"].String; op != "" {
				m.JSONPathOperator = op
			}
			m.ExpectedValue = row["expected_value"].String
		case monitor.TypeSMTP:
			m.SMTPSecurity = row["smtp_security"].String
		case monitor.TypeRADIUS:
			m.RadiusUsername = row["radius_username"].String
			m.RadiusPassword = row["radius_password"].String
			m.RadiusSecret = row["radius_secret"].String
			m.RadiusCalledStationID = row["radius_called_station_id"].String
			m.RadiusCallingStationID = row["radius_calling_station_id"].String
		}
		return checked(m)
	case monitor.TypeHTTP, monitor.TypeKeyword, monitor.TypeJSONQuery, monitor.TypeWebSocket:
	default:
		return m, "unsupported monitor type: " + m.Type
	}

	if row["proxy_id"].Valid {
		return m, "unsupported setting: proxy"
	}
	m.URL = row["url"].String
	if method := strings.ToUpper(strings.TrimSpace(row["method"].String)); method != "" {
		m.Method = method
	}
	m.Body = row["body"].String
	if encoding := strings.ToLower(row["http_body_encoding"].String); encoding != "" {
		m.BodyEncoding = encoding
	}
	m.MaxRedirects = int32(intOf(row["maxredirects"]))
	m.IgnoreTLS = boolOf(row["ignore_tls"])
	m.ExpiryNotification = boolOf(row["expiry_notification"]) && !m.IgnoreTLS
	switch m.Type {
	case monitor.TypeKeyword:
		m.Keyword = row["keyword"].String
		m.InvertKeyword = boolOf(row["invert_keyword"])
	case monitor.TypeJSONQuery:
		m.JSONPath = row["json_path"].String
		if op := row["json_path_operator"].String; op != "" {
			m.JSONPathOperator = op
		}
		m.ExpectedValue = row["expected_value"].String
	case monitor.TypeWebSocket:
		m.WSSubprotocol = row["ws_subprotocol"].String
		m.WSIgnoreAcceptHeader = boolOf(row["ws_ignore_sec_websocket_accept_header"])
		m.ExpiryNotification = false
	}

	if raw := strings.TrimSpace(row["accepted_statuscodes_json"].String); raw != "" {
		if err := json.Unmarshal([]byte(raw), &m.AcceptedStatusCodes); err != nil {
			return m, "invalid setting: accepted status codes aren't valid JSON"
		}
	}
	headers, reason := convertHeaders(row["headers"].String)
	if reason != "" {
		return m, reason
	}
	m.Headers = headers

	switch authMethod := row["auth_method"].String; authMethod {
	case "", "null":
	case "basic":
		value := row["basic_auth_user"].String + ":" + row["basic_auth_pass"].String
		m.Headers["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(value))
	case "bearer":
		m.Headers["Authorization"] = "Bearer " + row["bearer_token"].String
	default:
		return m, "unsupported setting: authentication method " + authMethod
	}
	return checked(m)
}

// jsonList reads a list the Node.js backend kept as JSON text. Empty is none.
func jsonList(raw string, into *[]string) error {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}

// sourceConditions are a monitor's conditions, which the Node.js backend kept as JSON text.
func sourceConditions(row map[string]sql.NullString) json.RawMessage {
	raw := strings.TrimSpace(row["conditions"].String)
	if raw == "" || !json.Valid([]byte(raw)) {
		return json.RawMessage(`[]`)
	}
	return json.RawMessage(raw)
}

func checked(m monitor.Monitor) (monitor.Monitor, string) {
	m.Normalize()
	if err := m.Validate(); err != nil {
		return m, "invalid setting: " + err.Error()
	}
	return m, ""
}

func convertHeaders(raw string) (map[string]string, string) {
	headers := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return headers, ""
	}
	var values map[string]any
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, "invalid setting: headers aren't a valid JSON object"
	}
	for name, value := range values {
		if text, ok := value.(string); ok {
			headers[name] = text
		} else {
			headers[name] = fmt.Sprint(value)
		}
	}
	return headers, ""
}

// droppedSettings lists settings that this service doesn't have yet but that don't change
// whether a check passes. They're reported, not silently lost.
func droppedSettings(row map[string]sql.NullString) []string {
	var dropped []string
	if row["type"].String != monitor.TypeHTTP && row["type"].String != monitor.TypeKeyword {
		return dropped
	}
	if boolOf(row["cache_bust"]) {
		dropped = append(dropped, "cacheBust")
	}
	if boolOf(row["domain_expiry_notification"]) {
		dropped = append(dropped, "domainExpiryNotification")
	}
	if v := row["ip_family"].String; v != "" && v != "null" {
		dropped = append(dropped, "ipFamily")
	}
	if boolOf(row["save_response"]) {
		dropped = append(dropped, "saveResponse")
	}
	if row["description"].String != "" {
		dropped = append(dropped, "description")
	}
	return dropped
}

// copyNotifications imports notification settings, switched off, and the links of the monitors
// being imported. Settings that were switched on here are left alone, as monitors are.
func copyNotifications(ctx context.Context, conn *sql.Conn, target Target, monitors []*sourceMonitor, report *Report) error {
	source, err := readNotifications(ctx, conn, report)
	if err != nil {
		return err
	}
	existing := map[int32]store.ImportedNotification{}
	if target != nil {
		if existing, err = target.ImportedNotifications(ctx); err != nil {
			return err
		}
	}
	// Dry runs have no target ids, so any non-zero value stands in for "was imported".
	ids := make(map[int32]int32, len(source))
	for _, n := range source {
		if previous, ok := existing[n.id]; ok && previous.Active {
			report.Notifications.AlreadyActive++
			ids[n.id] = previous.NotificationID
			continue
		}
		ids[n.id] = n.id
		if target != nil {
			id, err := target.ImportNotification(ctx, n.id, n.notification, n.active)
			if err != nil {
				return fmt.Errorf("notification %d: %w", n.id, err)
			}
			ids[n.id] = id
		}
		report.Notifications.Imported++
	}

	rows, _, err := readRows(ctx, conn, "SELECT monitor_id, notification_id FROM monitor_notification ORDER BY monitor_id, notification_id")
	if err != nil {
		return fmt.Errorf("read monitor notifications: %w", err)
	}
	links := map[int32][]int32{}
	for _, row := range rows {
		id := int32(intOf(row["monitor_id"]))
		links[id] = append(links[id], int32(intOf(row["notification_id"])))
	}
	for _, m := range monitors {
		var linked []int32
		losing := false
		for _, sourceID := range links[m.id] {
			id, ok := ids[sourceID]
			if !ok {
				losing = true
				continue
			}
			if !slices.Contains(linked, id) {
				linked = append(linked, id)
			}
		}
		if losing {
			report.Notifications.MonitorsLosingAlerts++
		}
		report.Notifications.Links += len(linked)
		if target != nil {
			if err := target.SetMonitorNotifications(ctx, m.targetID, linked); err != nil {
				return fmt.Errorf("monitor %d: %w", m.id, err)
			}
		}
	}
	return nil
}

// copyTags copies the tags of the monitors being imported. A dry run has no target ids, so it
// counts what would be copied. Monitors already resumed here are left alone, like their settings.
func copyTags(ctx context.Context, conn *sql.Conn, target Target, monitors map[int32]int32, report *Report) error {
	rows, _, err := readRows(ctx, conn, "SELECT id, name, color FROM tag ORDER BY id")
	if err != nil {
		return fmt.Errorf("read tags: %w", err)
	}
	ids := make(map[int32]int32, len(rows))
	for _, row := range rows {
		sourceID := int32(intOf(row["id"]))
		ids[sourceID] = sourceID
		if target != nil {
			id, err := target.ImportTag(ctx, row["name"].String, row["color"].String)
			if err != nil {
				return fmt.Errorf("tag %d: %w", sourceID, err)
			}
			ids[sourceID] = id
		}
		report.Tags++
	}
	rows, _, err = readRows(ctx, conn, "SELECT tag_id, monitor_id, value FROM monitor_tag ORDER BY id")
	if err != nil {
		return fmt.Errorf("read monitor tags: %w", err)
	}
	for _, row := range rows {
		monitorID, ok := monitors[int32(intOf(row["monitor_id"]))]
		if !ok {
			continue
		}
		tagID, ok := ids[int32(intOf(row["tag_id"]))]
		if !ok {
			continue
		}
		if target != nil {
			if err := target.AddMonitorTag(ctx, tagID, monitorID, row["value"].String); err != nil {
				return fmt.Errorf("tag on monitor %d: %w", monitorID, err)
			}
		}
		report.TagLinks++
	}
	return nil
}

func readNotifications(ctx context.Context, conn *sql.Conn, report *Report) ([]sourceNotification, error) {
	rows, _, err := readRows(ctx, conn, "SELECT * FROM notification ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("read notifications: %w", err)
	}
	report.Notifications.Source = len(rows)
	list := make([]sourceNotification, 0, len(rows))
	for _, row := range rows {
		id, name := int32(intOf(row["id"])), strings.TrimSpace(row["name"].String)
		n, reason := convertNotification(row)
		if reason != "" {
			report.Notifications.Skipped = append(report.Notifications.Skipped, Skipped{SourceID: id, Name: name, Reason: reason})
			continue
		}
		list = append(list, sourceNotification{id: id, active: boolOf(row["active"]), notification: n})
	}
	return list, nil
}

func convertNotification(row map[string]sql.NullString) (store.Notification, string) {
	raw := row["config"].String
	// PostgreSQL can't store this character in JSON.
	if strings.Contains(raw, `\u0000`) {
		return store.Notification{}, "invalid setting: config contains a null character"
	}
	var config map[string]any
	if err := json.Unmarshal([]byte(raw), &config); err != nil || config == nil {
		return store.Notification{}, "invalid setting: config isn't a JSON object"
	}
	kind, _ := config["type"].(string)
	if !notify.Supported(kind) {
		return store.Notification{}, "unsupported notification type: " + kind
	}
	name := strings.TrimSpace(row["name"].String)
	if name == "" {
		name = kind
	}
	return store.Notification{Name: name, Type: kind, Config: config, IsDefault: boolOf(row["is_default"])}, ""
}

func monitorsWithNotifications(ctx context.Context, conn *sql.Conn) (map[int32]bool, error) {
	rows, _, err := readRows(ctx, conn, "SELECT DISTINCT monitor_id FROM monitor_notification")
	if err != nil {
		// Only used for the report, and not needed to import anything.
		return map[int32]bool{}, nil
	}
	result := make(map[int32]bool, len(rows))
	for _, row := range rows {
		result[int32(intOf(row["monitor_id"]))] = true
	}
	return result, nil
}

func copyStats(ctx context.Context, conn *sql.Conn, target Target, daily bool, targets map[int32]int32, before time.Time, count *int64) error {
	table := "stat_hourly"
	if daily {
		table = "stat_daily"
	}
	for sourceID, targetID := range targets {
		rows, _, err := readRows(ctx, conn, "SELECT * FROM "+table+" WHERE monitor_id = ? AND `timestamp` < ? ORDER BY `timestamp`", sourceID, before.Unix())
		if err != nil {
			return fmt.Errorf("read %s: %w", table, err)
		}
		stats := make([]store.ImportedStat, 0, len(rows))
		for _, row := range rows {
			stats = append(stats, convertStat(row, targetID))
		}
		*count += int64(len(stats))
		if target != nil {
			if err := target.ReplaceImportedStats(ctx, daily, targetID, stats); err != nil {
				return err
			}
		}
	}
	return nil
}

func convertStat(row map[string]sql.NullString, targetID int32) store.ImportedStat {
	up := intOf(row["up"])
	ping := floatOf(row["ping"])
	stat := store.ImportedStat{
		MonitorID: targetID,
		Bucket:    time.Unix(intOf(row["timestamp"]), 0).UTC(),
		Up:        up,
		Down:      intOf(row["down"]),
		PingSum:   ping * float64(up),
		PingCount: up,
	}
	if up > 0 {
		minPing, maxPing := floatOf(row["ping_min"]), floatOf(row["ping_max"])
		stat.MinPing, stat.MaxPing = &minPing, &maxPing
	}
	if row["extras"].Valid {
		var extras struct {
			Maintenance int64 `json:"maintenance"`
		}
		if json.Unmarshal([]byte(row["extras"].String), &extras) == nil {
			stat.Maintenance = extras.Maintenance
		}
	}
	return stat
}

func copyBeats(ctx context.Context, conn *sql.Conn, target Target, targets map[int32]int32, report *Report, log *slog.Logger) error {
	var lastID int64
	for {
		rows, _, err := readRows(ctx, conn, "SELECT * FROM heartbeat WHERE id > ? ORDER BY id LIMIT ?", lastID, beatBatch)
		if err != nil {
			return fmt.Errorf("read beats: %w", err)
		}
		if len(rows) == 0 {
			return nil
		}
		beats := make([]store.Beat, 0, len(rows))
		for _, row := range rows {
			lastID = intOf(row["id"])
			targetID, ok := targets[int32(intOf(row["monitor_id"]))]
			if !ok {
				continue
			}
			beat, err := convertBeat(row, targetID)
			if err != nil {
				report.InvalidBeats++
				continue
			}
			beats = append(beats, beat)
		}
		report.Beats += int64(len(beats))
		if target != nil && len(beats) > 0 {
			inserted, err := target.InsertBeats(ctx, beats)
			if err != nil {
				return err
			}
			report.NewBeats += int64(len(inserted))
		}
		if report.Beats%(beatBatch*200) < beatBatch {
			log.Info("beats progress", "read", report.Beats, "new", report.NewBeats)
		}
	}
}

func convertBeat(row map[string]sql.NullString, targetID int32) (store.Beat, error) {
	at, err := time.ParseInLocation(datetimeLayout, row["time"].String, time.UTC)
	if err != nil {
		return store.Beat{}, fmt.Errorf("time: %w", err)
	}
	status := intOf(row["status"])
	if status < 0 || status > 3 {
		return store.Beat{}, errors.New("status out of range")
	}
	beat := store.Beat{
		MonitorID: targetID,
		Time:      at,
		Status:    int16(status),
		Msg:       monitor.SanitizeMsg(row["msg"].String),
		Important: boolOf(row["important"]),
		Duration:  int32(max(0, min(intOf(row["duration"]), math.MaxInt32))),
		Retries:   int32(max(0, min(intOf(row["retries"]), math.MaxInt32))),
	}
	if row["ping"].Valid {
		ping := floatOf(row["ping"])
		if ping >= 0 {
			beat.Ping = &ping
		}
	}
	return beat, nil
}

func intOf(v sql.NullString) int64 {
	n, err := strconv.ParseInt(v.String, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

func floatOf(v sql.NullString) float64 {
	n, err := strconv.ParseFloat(v.String, 64)
	if err != nil {
		return 0
	}
	return n
}

func boolOf(v sql.NullString) bool {
	return v.Valid && v.String != "" && v.String != "0"
}
