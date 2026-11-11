package ui

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// beatTimeFormat is how the Node.js backend sent times: UTC, without a zone. The UI parses
// them with dayjs.utc.
const beatTimeFormat = "2006-01-02 15:04:05.000"

// beatJSON is a beat as the UI reads it. Pushed beats use monitorID and listed ones
// monitor_id, so both are set.
func beatJSON(b store.Beat) map[string]any {
	out := map[string]any{
		"monitorID":  b.MonitorID,
		"monitor_id": b.MonitorID,
		"status":     b.Status,
		"time":       b.Time.UTC().Format(beatTimeFormat),
		"msg":        b.Msg,
		"ping":       b.Ping,
		"important":  b.Important,
		"duration":   b.Duration,
		"retries":    b.Retries,
		"down_count": b.DownCount,
	}
	// A failed check's trace, which the prompt for an AI agent points to
	if b.TraceID != "" {
		out["traceId"] = b.TraceID
	}
	return out
}

func beatsJSON(beats []store.Beat) []map[string]any {
	out := make([]map[string]any, len(beats))
	for i, b := range beats {
		out[i] = beatJSON(b)
	}
	return out
}

// Supported monitor types, by the UI's names, which match this service's.
var supportedTypes = map[string]bool{
	monitor.TypeHTTP: true, monitor.TypeKeyword: true, monitor.TypePort: true, monitor.TypeGroup: true,
	monitor.TypePush: true, monitor.TypePing: true, monitor.TypeDNS: true, monitor.TypeJSONQuery: true,
	monitor.TypeManual: true, monitor.TypeWebSocket: true, monitor.TypePostgres: true, monitor.TypeMySQL: true,
	monitor.TypeRedis: true, monitor.TypeSQLServer: true, monitor.TypeOracle: true, monitor.TypeMongoDB: true,
	monitor.TypeMQTT: true, monitor.TypeRabbitMQ: true, monitor.TypeKafka: true, monitor.TypeNTP: true,
	monitor.TypeSNMP: true, monitor.TypeSMTP: true, monitor.TypeSIP: true, monitor.TypeRADIUS: true, monitor.TypeSteam: true,
	monitor.TypeDocker: true, monitor.TypeSystemService: true, monitor.TypePM2: true, monitor.TypeSFTP: true,
	monitor.TypeGRPC: true, monitor.TypeRealBrowser: true, monitor.TypeGlobalping: true, monitor.TypeGameDig: true,
	monitor.TypeTailscale: true,
}

// uiDefaults are fields of the UI's monitor form that this service doesn't have. They're sent
// with every monitor so that the form finds every field it reads.
var uiDefaults = map[string]any{
	"description": "", "wsSubprotocol": "", "protocol": nil, "location": "world", "ipFamily": nil,
	"retryOnlyOnStatusCodeFailure": false, "expiryNotification": false, "domainExpiryNotification": false,
	"saveResponse": false, "saveErrorResponse": false, "responseMaxLength": 1024,
	"dns_resolve_type": "A", "dns_resolve_server": "", "dns_last_result": nil,
	"docker_container": "", "docker_host": nil, "proxyId": nil,
	"basic_auth_user": "", "basic_auth_pass": "", "bearer_token": "", "authMethod": nil,
	"oauth_auth_method": "client_secret_basic", "oauth_client_id": "", "oauth_client_secret": "",
	"oauth_token_url": "", "oauth_scopes": "", "oauth_audience": "", "authWorkstation": "", "authDomain": "",
	"tlsCa": "", "tlsCert": "", "tlsKey": "",
	"mqttUsername": "", "mqttPassword": "", "mqttTopic": "", "mqttWebsocketPath": "", "mqttSuccessMessage": "",
	"mqttCheckType": "keyword", "databaseConnectionString": "", "databaseQuery": nil,
	"grpcUrl": nil, "grpcProtobuf": nil, "grpcMethod": nil, "grpcServiceName": nil, "grpcEnableTls": false,
	"grpcBody": nil, "grpcMetadata": nil, "radiusUsername": nil, "radiusPassword": nil, "radiusSecret": nil,
	"radiusCalledStationId": nil, "radiusCallingStationId": nil, "game": nil, "gamedigGivenPortOnly": true,
	"gamedigToken": "", "jsonPath": nil, "expectedValue": nil, "jsonPathOperator": nil,
	"kafkaProducerTopic": nil, "kafkaProducerBrokers": []any{}, "kafkaProducerSsl": false,
	"kafkaProducerAllowAutoTopicCreation": false, "kafkaProducerMessage": nil,
	"kafkaProducerSaslOptions": map[string]any{"mechanism": "None"},
	"screenshot":               nil, "cacheBust": false, "remote_browser": nil, "screenshot_delay": 0,
	"snmpOid": nil, "snmpVersion": nil, "smtpSecurity": nil,
	"rabbitmqNodes": []any{}, "rabbitmqUsername": "", "rabbitmqPassword": "", "conditions": []any{},
	"ntpStratumThreshold": 5, "ntpTimeOffsetThreshold": 1000, "ntpRootDispersionThreshold": 500,
	"sftpPath": nil, "sshAuthMethod": "password", "sshUsername": "", "sshPassword": "", "sshPrivateKey": "",
	"sshPassphrase": "", "ping_numeric": true, "ping_count": 3, "ping_per_request_timeout": 2, "packetSize": 56,
	"subtype": nil, "weight": 2000, "pushToken": nil, "config": map[string]any{}, "tags": []any{},
	"maintenance": false, "includeSensitiveData": true,
}

// monitorTree holds what monitorJSON needs about other monitors.
type monitorTree struct {
	byID          map[int32]monitor.Monitor
	children      map[int32][]int32
	notifications map[int32][]int32
	// underMaintenance is set by the server. Nil means nothing is.
	underMaintenance map[int32]bool
	// tags is set by the server. Nil means no monitor has any.
	tags map[int32][]store.MonitorTag
}

func newMonitorTree(monitors []monitor.Monitor, notifications map[int32][]int32) *monitorTree {
	t := &monitorTree{byID: map[int32]monitor.Monitor{}, children: map[int32][]int32{}, notifications: notifications}
	for _, m := range monitors {
		t.byID[m.ID] = m
	}
	for _, m := range monitors {
		if m.ParentID != nil {
			t.children[*m.ParentID] = append(t.children[*m.ParentID], m.ID)
		}
	}
	return t
}

// path returns the names from the top group down to m, and whether a group above m is paused.
func (t *monitorTree) path(m monitor.Monitor) ([]string, bool) {
	names := []string{m.Name}
	parentPaused := false
	seen := map[int32]bool{m.ID: true}
	for p := m.ParentID; p != nil && !seen[*p]; {
		parent, ok := t.byID[*p]
		if !ok {
			break
		}
		seen[parent.ID] = true
		names = append([]string{parent.Name}, names...)
		parentPaused = parentPaused || !parent.Active
		p = parent.ParentID
	}
	return names, parentPaused
}

// descendants returns every monitor below id, deepest first.
func (t *monitorTree) descendants(id int32) []int32 {
	var out []int32
	seen := map[int32]bool{id: true}
	var walk func(int32)
	walk = func(parent int32) {
		for _, child := range t.children[parent] {
			if !seen[child] {
				seen[child] = true
				walk(child)
				out = append(out, child)
			}
		}
	}
	walk(id)
	return out
}

// monitorJSON is a monitor as the UI's list, detail and edit pages read it.
func (t *monitorTree) monitorJSON(m monitor.Monitor) map[string]any {
	out := maps.Clone(uiDefaults)
	path, parentPaused := t.path(m)
	notificationIDs := map[string]bool{}
	for _, id := range t.notifications[m.ID] {
		notificationIDs[strconv.Itoa(int(id))] = true
	}
	childrenIDs := t.children[m.ID]
	if childrenIDs == nil {
		childrenIDs = []int32{}
	}
	headers := ""
	if len(m.Headers) > 0 {
		data, _ := json.MarshalIndent(m.Headers, "", "    ")
		headers = string(data)
	}
	var port any
	if m.Port != 0 {
		port = m.Port
	}
	codes := m.AcceptedStatusCodes
	if len(codes) == 0 {
		codes = []string{"200-299"}
	}
	url := m.URL
	if url == "" {
		url = "https://"
	}
	tags := t.tags[m.ID]
	if tags == nil {
		tags = []store.MonitorTag{}
	}
	maps.Copy(out, map[string]any{
		"tags":                 tags,
		"expiryNotification":   m.ExpiryNotification,
		"id":                   m.ID,
		"name":                 m.Name,
		"type":                 m.Type,
		"path":                 path,
		"pathName":             strings.Join(path, " / "),
		"parent":               m.ParentID,
		"sortOrder":            m.SortOrder,
		"childrenIDs":          childrenIDs,
		"active":               m.Active,
		"forceInactive":        parentPaused,
		"url":                  url,
		"method":               m.Method,
		"hostname":             m.Hostname,
		"port":                 port,
		"interval":             m.Interval,
		"retryInterval":        m.RetryInterval,
		"resendInterval":       m.ResendInterval,
		"timeout":              m.Timeout,
		"maxretries":           m.MaxRetries,
		"upsideDown":           m.UpsideDown,
		"keyword":              m.Keyword,
		"invertKeyword":        m.InvertKeyword,
		"ignoreTls":            m.IgnoreTLS,
		"maxredirects":         m.MaxRedirects,
		"accepted_statuscodes": codes,
		"httpBodyEncoding":     m.BodyEncoding,
		"headers":              headers,
		"body":                 m.Body,
		"notificationIDList":   notificationIDs,
		"propagateTrace":       m.PropagateTrace,
		"maintenance":          t.underMaintenance[m.ID],

		"pushToken":                m.PushToken,
		"ping_count":               m.PingCount,
		"ping_per_request_timeout": m.PingPerRequestTimeout,
		"packetSize":               m.PacketSize,
		"dns_resolve_type":         m.DNSResolveType,
		"dns_resolve_server":       m.DNSResolveServer,
		"dns_last_result":          m.DNSLastResult,

		"jsonPath":                            m.JSONPath,
		"jsonPathOperator":                    m.JSONPathOperator,
		"expectedValue":                       m.ExpectedValue,
		"conditions":                          conditionsJSON(m.Conditions),
		"manual_status":                       m.ManualStatus,
		"wsSubprotocol":                       m.WSSubprotocol,
		"wsIgnoreSecWebsocketAcceptHeader":    m.WSIgnoreAcceptHeader,
		"databaseConnectionString":            m.DatabaseConnectionString,
		"databaseQuery":                       m.DatabaseQuery,
		"radiusPassword":                      radiusPasswordField(m),
		"radiusUsername":                      m.RadiusUsername,
		"radiusSecret":                        m.RadiusSecret,
		"radiusCalledStationId":               m.RadiusCalledStationID,
		"radiusCallingStationId":              m.RadiusCallingStationID,
		"snmpOid":                             m.SNMPOID,
		"snmpVersion":                         cmp.Or(m.SNMPVersion, "2c"),
		"snmpV3Username":                      m.SNMPV3Username,
		"smtpSecurity":                        nilIfEmpty(m.SMTPSecurity),
		"ntpStratumThreshold":                 cmp.Or(m.NTPStratumThreshold, 5),
		"ntpTimeOffsetThreshold":              cmp.Or(m.NTPTimeOffsetThreshold, 1000),
		"ntpRootDispersionThreshold":          cmp.Or(m.NTPRootDispersionThreshold, 500),
		"basic_auth_user":                     basicAuthUser(m),
		"basic_auth_pass":                     basicAuthPass(m),
		"mqttTopic":                           m.MQTTTopic,
		"mqttUsername":                        m.MQTTUsername,
		"mqttPassword":                        m.MQTTPassword,
		"mqttWebsocketPath":                   m.MQTTWebsocketPath,
		"mqttCheckType":                       cmp.Or(m.MQTTCheckType, "keyword"),
		"mqttSuccessMessage":                  m.MQTTSuccessMessage,
		"rabbitmqNodes":                       orEmpty(m.RabbitMQNodes),
		"rabbitmqUsername":                    m.RabbitMQUsername,
		"rabbitmqPassword":                    m.RabbitMQPassword,
		"kafkaProducerBrokers":                orEmpty(m.KafkaBrokers),
		"kafkaProducerTopic":                  m.KafkaTopic,
		"kafkaProducerMessage":                m.KafkaMessage,
		"kafkaProducerSsl":                    m.KafkaSSL,
		"kafkaProducerAllowAutoTopicCreation": m.KafkaAllowAutoTopicCreation,
		"kafkaProducerSaslOptions":            kafkaSASLJSON(m.KafkaSASL),

		"docker_container": m.DockerContainer,
		"docker_host":      nilIfZero(m.DockerHostID),
		"config":           map[string]any{"systemServiceName": m.SystemServiceName},
		"sshAuthMethod":    cmp.Or(m.SSHAuthMethod, "password"),
		"sshUsername":      m.SSHUsername,
		"sshPassword":      m.SSHPassword,
		"sshPrivateKey":    m.SSHPrivateKey,
		"sshPassphrase":    m.SSHPassphrase,
		"sftpPath":         nilIfEmpty(m.SFTPPath),
		"grpcUrl":          nilIfEmpty(m.GRPCURL),
		"grpcProtobuf":     nilIfEmpty(m.GRPCProtobuf),
		"grpcServiceName":  nilIfEmpty(m.GRPCServiceName),
		"grpcMethod":       nilIfEmpty(m.GRPCMethod),
		"grpcBody":         nilIfEmpty(m.GRPCBody),
		"grpcMetadata":     nilIfEmpty(m.GRPCMetadata),
		"grpcEnableTls":    m.GRPCEnableTLS,
		"remote_browser":   nilIfZero(m.RemoteBrowserID),
		"screenshot_delay": m.ScreenshotDelay,

		"description":                  m.Description,
		"cacheBust":                    m.CacheBust,
		"ipFamily":                     nilIfEmpty(m.IPFamily),
		"retryOnlyOnStatusCodeFailure": m.RetryOnlyOnStatusCodeFailure,
		"saveResponse":                 m.SaveResponse,
		"saveErrorResponse":            m.SaveErrorResponse,
		"responseMaxLength":            cmp.Or(m.ResponseMaxLength, monitor.DefaultResponseMaxLength),
		"authMethod":                   nilIfEmpty(m.AuthMethod),
		"bearer_token":                 m.BearerToken,
		"authDomain":                   m.AuthDomain,
		"authWorkstation":              m.AuthWorkstation,
		"tlsCert":                      m.TLSCert,
		"tlsKey":                       m.TLSKey,
		"tlsCa":                        m.TLSCA,
		"oauth_auth_method":            cmp.Or(m.OAuthAuthMethod, "client_secret_basic"),
		"oauth_token_url":              m.OAuthTokenURL,
		"oauth_client_id":              m.OAuthClientID,
		"oauth_client_secret":          m.OAuthClientSecret,
		"oauth_scopes":                 m.OAuthScopes,
		"oauth_audience":               m.OAuthAudience,
		"proxyId":                      nilIfZero(m.ProxyID),
		"domainExpiryNotification":     m.DomainExpiryNotification,

		"game":                 nilIfEmpty(m.Game),
		"gamedigGivenPortOnly": m.GamedigGivenPortOnly || m.Type != monitor.TypeGameDig,
		"gamedigToken":         m.GamedigToken,
		"subtype":              nilIfEmpty(m.Subtype),
		"location":             cmp.Or(m.Location, "world"),
		"protocol":             nilIfEmpty(m.Protocol),
	})
	if m.Type == monitor.TypeRealBrowser {
		out["screenshot"] = fmt.Sprintf("/screenshots/%d.png", m.ID)
	}
	return out
}

func nilIfZero(id int32) any {
	if id == 0 {
		return nil
	}
	return id
}

func (t *monitorTree) listJSON() map[string]any {
	out := make(map[string]any, len(t.byID))
	for id, m := range t.byID {
		out[strconv.Itoa(int(id))] = t.monitorJSON(m)
	}
	return out
}

// flexInt reads a JSON number, a numeric string or null, as the UI's form fields can send any.
type flexInt struct {
	value int64
	set   bool
}

func (f *flexInt) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if string(data) == "null" || string(data) == `""` {
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		data = []byte(strings.TrimSpace(s))
	}
	v, err := strconv.ParseFloat(string(data), 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", data)
	}
	f.value, f.set = int64(v), true
	return nil
}

type flexFloat struct {
	value float64
	set   bool
}

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	var i flexInt
	data = bytes.TrimSpace(data)
	if string(data) == "null" || string(data) == `""` {
		return nil
	}
	if len(data) > 0 && data[0] == '"' {
		if err := i.UnmarshalJSON(data); err != nil {
			return err
		}
		f.value, f.set = float64(i.value), true
		return nil
	}
	if err := json.Unmarshal(data, &f.value); err != nil {
		return err
	}
	f.set = true
	return nil
}

// monitorInput is the part of the UI's monitor form that this service uses.
type monitorInput struct {
	ID                  flexInt         `json:"id"`
	Name                string          `json:"name"`
	Type                string          `json:"type"`
	Parent              flexInt         `json:"parent"`
	Active              *bool           `json:"active"`
	URL                 string          `json:"url"`
	Method              string          `json:"method"`
	Headers             json.RawMessage `json:"headers"`
	Body                *string         `json:"body"`
	HTTPBodyEncoding    string          `json:"httpBodyEncoding"`
	AcceptedStatusCodes []string        `json:"accepted_statuscodes"`
	MaxRedirects        flexInt         `json:"maxredirects"`
	IgnoreTLS           bool            `json:"ignoreTls"`
	Keyword             *string         `json:"keyword"`
	InvertKeyword       bool            `json:"invertKeyword"`
	Hostname            *string         `json:"hostname"`
	Port                flexInt         `json:"port"`
	Interval            flexInt         `json:"interval"`
	RetryInterval       flexInt         `json:"retryInterval"`
	ResendInterval      flexInt         `json:"resendInterval"`
	Timeout             flexFloat       `json:"timeout"`
	MaxRetries          flexInt         `json:"maxretries"`
	UpsideDown          bool            `json:"upsideDown"`
	ExpiryNotification  bool            `json:"expiryNotification"`
	PropagateTrace      bool            `json:"propagateTrace"`
	ProxyID             flexInt         `json:"proxyId"`
	AuthMethod          *string         `json:"authMethod"`
	BasicAuthUser       string          `json:"basic_auth_user"`
	BasicAuthPass       string          `json:"basic_auth_pass"`
	BearerToken         string          `json:"bearer_token"`
	Conditions          json.RawMessage `json:"conditions"`
	NotificationIDList  map[string]bool `json:"notificationIDList"`

	PushToken             string  `json:"pushToken"`
	PingCount             flexInt `json:"ping_count"`
	PingPerRequestTimeout flexInt `json:"ping_per_request_timeout"`
	PacketSize            flexInt `json:"packetSize"`
	DNSResolveType        string  `json:"dns_resolve_type"`
	DNSResolveServer      string  `json:"dns_resolve_server"`

	JSONPath                 *string         `json:"jsonPath"`
	JSONPathOperator         *string         `json:"jsonPathOperator"`
	ExpectedValue            json.RawMessage `json:"expectedValue"`
	ManualStatus             flexInt         `json:"manual_status"`
	WSSubprotocol            *string         `json:"wsSubprotocol"`
	WSIgnoreAcceptHeader     bool            `json:"wsIgnoreSecWebsocketAcceptHeader"`
	DatabaseConnectionString *string         `json:"databaseConnectionString"`
	DatabaseQuery            *string         `json:"databaseQuery"`
	RadiusPassword           *string         `json:"radiusPassword"`

	MQTTTopic                   *string            `json:"mqttTopic"`
	MQTTUsername                *string            `json:"mqttUsername"`
	MQTTPassword                *string            `json:"mqttPassword"`
	MQTTWebsocketPath           *string            `json:"mqttWebsocketPath"`
	MQTTCheckType               *string            `json:"mqttCheckType"`
	MQTTSuccessMessage          *string            `json:"mqttSuccessMessage"`
	RabbitMQNodes               []string           `json:"rabbitmqNodes"`
	RabbitMQUsername            *string            `json:"rabbitmqUsername"`
	RabbitMQPassword            *string            `json:"rabbitmqPassword"`
	KafkaBrokers                []string           `json:"kafkaProducerBrokers"`
	KafkaTopic                  *string            `json:"kafkaProducerTopic"`
	KafkaMessage                *string            `json:"kafkaProducerMessage"`
	KafkaSSL                    bool               `json:"kafkaProducerSsl"`
	KafkaAllowAutoTopicCreation bool               `json:"kafkaProducerAllowAutoTopicCreation"`
	KafkaSASL                   *monitor.KafkaSASL `json:"kafkaProducerSaslOptions"`

	NTPStratumThreshold        flexInt   `json:"ntpStratumThreshold"`
	NTPTimeOffsetThreshold     flexFloat `json:"ntpTimeOffsetThreshold"`
	NTPRootDispersionThreshold flexFloat `json:"ntpRootDispersionThreshold"`
	SNMPOID                    *string   `json:"snmpOid"`
	SNMPVersion                *string   `json:"snmpVersion"`
	SNMPV3Username             *string   `json:"snmpV3Username"`
	SMTPSecurity               *string   `json:"smtpSecurity"`
	RadiusUsername             *string   `json:"radiusUsername"`
	RadiusSecret               *string   `json:"radiusSecret"`
	RadiusCalledStationID      *string   `json:"radiusCalledStationId"`
	RadiusCallingStationID     *string   `json:"radiusCallingStationId"`

	DockerContainer *string `json:"docker_container"`
	DockerHost      flexInt `json:"docker_host"`
	Config          *struct {
		SystemServiceName *string `json:"systemServiceName"`
	} `json:"config"`
	SSHAuthMethod   *string `json:"sshAuthMethod"`
	SSHUsername     *string `json:"sshUsername"`
	SSHPassword     *string `json:"sshPassword"`
	SSHPrivateKey   *string `json:"sshPrivateKey"`
	SSHPassphrase   *string `json:"sshPassphrase"`
	SFTPPath        *string `json:"sftpPath"`
	GRPCURL         *string `json:"grpcUrl"`
	GRPCProtobuf    *string `json:"grpcProtobuf"`
	GRPCServiceName *string `json:"grpcServiceName"`
	GRPCMethod      *string `json:"grpcMethod"`
	GRPCBody        *string `json:"grpcBody"`
	GRPCMetadata    *string `json:"grpcMetadata"`
	GRPCEnableTLS   bool    `json:"grpcEnableTls"`
	RemoteBrowser   flexInt `json:"remote_browser"`
	ScreenshotDelay flexInt `json:"screenshot_delay"`

	Description                  *string `json:"description"`
	CacheBust                    bool    `json:"cacheBust"`
	IPFamily                     *string `json:"ipFamily"`
	RetryOnlyOnStatusCodeFailure bool    `json:"retryOnlyOnStatusCodeFailure"`
	SaveResponse                 bool    `json:"saveResponse"`
	SaveErrorResponse            bool    `json:"saveErrorResponse"`
	ResponseMaxLength            flexInt `json:"responseMaxLength"`
	AuthDomain                   *string `json:"authDomain"`
	AuthWorkstation              *string `json:"authWorkstation"`
	TLSCert                      *string `json:"tlsCert"`
	TLSKey                       *string `json:"tlsKey"`
	TLSCA                        *string `json:"tlsCa"`
	OAuthAuthMethod              *string `json:"oauth_auth_method"`
	OAuthTokenURL                *string `json:"oauth_token_url"`
	OAuthClientID                *string `json:"oauth_client_id"`
	OAuthClientSecret            *string `json:"oauth_client_secret"`
	OAuthScopes                  *string `json:"oauth_scopes"`
	OAuthAudience                *string `json:"oauth_audience"`
	DomainExpiryNotification     bool    `json:"domainExpiryNotification"`

	Game                 *string `json:"game"`
	GamedigGivenPortOnly bool    `json:"gamedigGivenPortOnly"`
	GamedigToken         *string `json:"gamedigToken"`
	Subtype              *string `json:"subtype"`
	Location             *string `json:"location"`
	Protocol             *string `json:"protocol"`
}

// parseMonitor converts the UI's form to a monitor, and returns the notifications to link.
// Settings this service can't honour are refused, so that a check never runs differently from
// what the form shows. note is set when a setting was stored in another form.
func parseMonitor(raw json.RawMessage) (m monitor.Monitor, notificationIDs []int32, note string, err error) {
	var in monitorInput
	if err := json.Unmarshal(raw, &in); err != nil {
		return m, nil, "", fmt.Errorf("invalid monitor: %w", err)
	}
	if !supportedTypes[in.Type] {
		return m, nil, "", fmt.Errorf("monitor type %q isn't supported yet. Supported types: %s", in.Type, strings.Join(monitor.Types, ", "))
	}
	// The form keeps conditions while the type changes, so only the types that use them keep them.
	conditionsRaw := json.RawMessage(`[]`)
	if _, uses := monitor.ConditionVariables[in.Type]; uses && len(in.Conditions) > 0 && string(in.Conditions) != "null" {
		conditionsRaw = in.Conditions
	}

	m = monitor.New()
	m.ID = int32(in.ID.value)
	m.Name = strings.TrimSpace(in.Name)
	m.Type = in.Type
	if in.Active != nil {
		m.Active = *in.Active
	}
	if in.Parent.set && in.Parent.value > 0 {
		parent := int32(in.Parent.value)
		m.ParentID = &parent
	}
	if in.Interval.set {
		m.Interval = int32(in.Interval.value)
	}
	m.RetryInterval = int32(in.RetryInterval.value)
	m.ResendInterval = int32(in.ResendInterval.value)
	m.Timeout = in.Timeout.value
	m.MaxRetries = int32(in.MaxRetries.value)
	m.UpsideDown = in.UpsideDown
	m.PropagateTrace = in.PropagateTrace
	m.Description = strings.TrimSpace(text(in.Description))
	if domains.Supports(m.Type) {
		m.DomainExpiryNotification = in.DomainExpiryNotification
	}
	// The UI sends a retry interval equal to the interval by default, which means the same here.
	if m.RetryInterval == m.Interval {
		m.RetryInterval = 0
	}

	switch m.Type {
	case monitor.TypePort:
		if in.Hostname != nil {
			m.Hostname = strings.TrimSpace(*in.Hostname)
		}
		m.Port = int32(in.Port.value)
	case monitor.TypePush:
		m.PushToken = strings.TrimSpace(in.PushToken)
	case monitor.TypePing:
		if in.Hostname != nil {
			m.Hostname = strings.TrimSpace(*in.Hostname)
		}
		if in.PingCount.set {
			m.PingCount = int32(in.PingCount.value)
		}
		if in.PingPerRequestTimeout.set {
			m.PingPerRequestTimeout = int32(in.PingPerRequestTimeout.value)
		}
		if in.PacketSize.set {
			m.PacketSize = int32(in.PacketSize.value)
		}
	case monitor.TypeDNS:
		if in.Hostname != nil {
			m.Hostname = strings.TrimSpace(*in.Hostname)
		}
		m.Port = int32(in.Port.value)
		if in.DNSResolveType != "" {
			m.DNSResolveType = strings.TrimSpace(in.DNSResolveType)
		}
		if in.DNSResolveServer != "" {
			m.DNSResolveServer = strings.TrimSpace(in.DNSResolveServer)
		}
	case monitor.TypeManual:
		if in.ManualStatus.set {
			status := int16(in.ManualStatus.value)
			m.ManualStatus = &status
		}
	case monitor.TypeSQLServer, monitor.TypeOracle, monitor.TypeMongoDB:
		m.DatabaseConnectionString = strings.TrimSpace(text(in.DatabaseConnectionString))
		m.DatabaseQuery = text(in.DatabaseQuery)
		if m.Type == monitor.TypeOracle {
			m.DatabaseUsername = strings.TrimSpace(in.BasicAuthUser)
			m.DatabasePassword = in.BasicAuthPass
		}
		if m.Type == monitor.TypeMongoDB {
			m.JSONPath = text(in.JSONPath)
			m.ExpectedValue = rawText(in.ExpectedValue)
		}
	case monitor.TypeMQTT:
		if in.Hostname != nil {
			m.Hostname = strings.TrimSpace(*in.Hostname)
		}
		m.Port = int32(in.Port.value)
		m.MQTTTopic = strings.TrimSpace(text(in.MQTTTopic))
		m.MQTTUsername = text(in.MQTTUsername)
		m.MQTTPassword = text(in.MQTTPassword)
		m.MQTTWebsocketPath = strings.TrimSpace(text(in.MQTTWebsocketPath))
		m.MQTTCheckType = cmp.Or(text(in.MQTTCheckType), "keyword")
		m.MQTTSuccessMessage = text(in.MQTTSuccessMessage)
		m.JSONPath = text(in.JSONPath)
		m.ExpectedValue = rawText(in.ExpectedValue)
	case monitor.TypeRabbitMQ:
		for _, node := range in.RabbitMQNodes {
			if node = strings.TrimSpace(node); node != "" {
				m.RabbitMQNodes = append(m.RabbitMQNodes, node)
			}
		}
		m.RabbitMQUsername = text(in.RabbitMQUsername)
		m.RabbitMQPassword = text(in.RabbitMQPassword)
		m.IgnoreTLS = in.IgnoreTLS
	case monitor.TypeKafka:
		for _, broker := range in.KafkaBrokers {
			if broker = strings.TrimSpace(broker); broker != "" {
				m.KafkaBrokers = append(m.KafkaBrokers, broker)
			}
		}
		m.KafkaTopic = strings.TrimSpace(text(in.KafkaTopic))
		m.KafkaMessage = text(in.KafkaMessage)
		m.KafkaSSL = in.KafkaSSL
		m.KafkaAllowAutoTopicCreation = in.KafkaAllowAutoTopicCreation
		if in.KafkaSASL != nil && in.KafkaSASL.Mechanism != "" && in.KafkaSASL.Mechanism != "None" {
			m.KafkaSASL = in.KafkaSASL
		}
	case monitor.TypeNTP, monitor.TypeSNMP, monitor.TypeSMTP, monitor.TypeSIP, monitor.TypeRADIUS, monitor.TypeSteam:
		if in.Hostname != nil {
			m.Hostname = strings.TrimSpace(*in.Hostname)
		}
		m.Port = int32(in.Port.value)
		m.IgnoreTLS = in.IgnoreTLS
		switch m.Type {
		case monitor.TypeNTP:
			m.NTPStratumThreshold = int32(in.NTPStratumThreshold.value)
			m.NTPTimeOffsetThreshold = in.NTPTimeOffsetThreshold.value
			m.NTPRootDispersionThreshold = in.NTPRootDispersionThreshold.value
		case monitor.TypeSNMP:
			m.SNMPOID = strings.TrimSpace(text(in.SNMPOID))
			m.SNMPVersion = text(in.SNMPVersion)
			m.SNMPV3Username = strings.TrimSpace(text(in.SNMPV3Username))
			m.SNMPCommunity = text(in.RadiusPassword)
			m.JSONPath = text(in.JSONPath)
			if in.JSONPathOperator != nil && *in.JSONPathOperator != "" {
				m.JSONPathOperator = *in.JSONPathOperator
			}
			m.ExpectedValue = rawText(in.ExpectedValue)
		case monitor.TypeSMTP:
			m.SMTPSecurity = text(in.SMTPSecurity)
		case monitor.TypeRADIUS:
			m.RadiusUsername = text(in.RadiusUsername)
			m.RadiusPassword = text(in.RadiusPassword)
			m.RadiusSecret = text(in.RadiusSecret)
			m.RadiusCalledStationID = text(in.RadiusCalledStationID)
			m.RadiusCallingStationID = text(in.RadiusCallingStationID)
		}
	case monitor.TypeDocker:
		m.DockerContainer = strings.TrimSpace(text(in.DockerContainer))
		m.DockerHostID = int32(in.DockerHost.value)
	case monitor.TypeSystemService, monitor.TypePM2:
		if in.Config != nil {
			m.SystemServiceName = strings.TrimSpace(text(in.Config.SystemServiceName))
		}
	case monitor.TypeSFTP:
		if in.Hostname != nil {
			m.Hostname = strings.TrimSpace(*in.Hostname)
		}
		m.Port = int32(in.Port.value)
		m.SSHAuthMethod = cmp.Or(text(in.SSHAuthMethod), "password")
		m.SSHUsername = strings.TrimSpace(text(in.SSHUsername))
		// Only the secrets of the chosen method are kept.
		if m.SSHAuthMethod == "privateKey" {
			m.SSHPrivateKey = text(in.SSHPrivateKey)
			m.SSHPassphrase = text(in.SSHPassphrase)
		} else {
			m.SSHPassword = text(in.SSHPassword)
		}
		m.SFTPPath = strings.TrimSpace(text(in.SFTPPath))
	case monitor.TypeGRPC:
		m.GRPCURL = strings.TrimSpace(text(in.GRPCURL))
		m.GRPCProtobuf = text(in.GRPCProtobuf)
		m.GRPCServiceName = strings.TrimSpace(text(in.GRPCServiceName))
		m.GRPCMethod = strings.TrimSpace(text(in.GRPCMethod))
		m.GRPCBody = text(in.GRPCBody)
		m.GRPCMetadata = text(in.GRPCMetadata)
		m.GRPCEnableTLS = in.GRPCEnableTLS
		if in.Keyword != nil {
			m.Keyword = *in.Keyword
			m.InvertKeyword = in.InvertKeyword
		}
	case monitor.TypeGameDig:
		m.Hostname = strings.TrimSpace(text(in.Hostname))
		m.Port = int32(in.Port.value)
		m.Game = strings.TrimSpace(text(in.Game))
		m.GamedigGivenPortOnly = in.GamedigGivenPortOnly
		m.GamedigToken = text(in.GamedigToken)
	case monitor.TypeTailscale:
		m.Hostname = strings.TrimSpace(text(in.Hostname))
	case monitor.TypeGlobalping:
		if err := parseGlobalping(&m, in); err != nil {
			return m, nil, "", err
		}
	case monitor.TypeRealBrowser:
		m.URL = strings.TrimSpace(in.URL)
		m.RemoteBrowserID = int32(in.RemoteBrowser.value)
		m.ScreenshotDelay = int32(in.ScreenshotDelay.value)
	case monitor.TypePostgres, monitor.TypeMySQL, monitor.TypeRedis:
		m.DatabaseConnectionString = strings.TrimSpace(text(in.DatabaseConnectionString))
		if m.Type != monitor.TypeRedis {
			m.DatabaseQuery = text(in.DatabaseQuery)
		}
		if m.Type == monitor.TypeMySQL {
			m.DatabasePassword = text(in.RadiusPassword)
		}
		m.IgnoreTLS = in.IgnoreTLS
	case monitor.TypeWebSocket:
		m.URL = strings.TrimSpace(in.URL)
		m.WSSubprotocol = strings.TrimSpace(text(in.WSSubprotocol))
		m.WSIgnoreAcceptHeader = in.WSIgnoreAcceptHeader
		m.IgnoreTLS = in.IgnoreTLS
		m.AcceptedStatusCodes = []string{"1000"}
		if len(in.AcceptedStatusCodes) > 0 {
			m.AcceptedStatusCodes = in.AcceptedStatusCodes
		}
		headers, err := parseHeaders(in.Headers)
		if err != nil {
			return m, nil, "", err
		}
		m.Headers = headers
		applyRequestOptions(&m, in)
	case monitor.TypeHTTP, monitor.TypeKeyword, monitor.TypeJSONQuery:
		m.URL = strings.TrimSpace(in.URL)
		if m.Type == monitor.TypeJSONQuery {
			m.JSONPath = text(in.JSONPath)
			if in.JSONPathOperator != nil && *in.JSONPathOperator != "" {
				m.JSONPathOperator = *in.JSONPathOperator
			}
			m.ExpectedValue = rawText(in.ExpectedValue)
		}
		if method := strings.ToUpper(strings.TrimSpace(in.Method)); method != "" {
			m.Method = method
		}
		if in.Body != nil {
			m.Body = *in.Body
		}
		if encoding := strings.ToLower(in.HTTPBodyEncoding); encoding != "" {
			m.BodyEncoding = encoding
		}
		if in.AcceptedStatusCodes != nil {
			m.AcceptedStatusCodes = in.AcceptedStatusCodes
		}
		if in.MaxRedirects.set {
			m.MaxRedirects = int32(in.MaxRedirects.value)
		}
		m.IgnoreTLS = in.IgnoreTLS
		// Without verification there's nothing to warn about, as the form also says.
		m.ExpiryNotification = in.ExpiryNotification && !in.IgnoreTLS
		if m.Type == monitor.TypeKeyword && in.Keyword != nil {
			m.Keyword = *in.Keyword
			m.InvertKeyword = in.InvertKeyword
		}
		headers, err := parseHeaders(in.Headers)
		if err != nil {
			return m, nil, "", err
		}
		m.Headers = headers
		applyRequestOptions(&m, in)
	}

	m.Conditions = conditionsRaw
	m.Normalize()
	if err := m.Validate(); err != nil {
		return m, nil, "", err
	}

	for key, on := range in.NotificationIDList {
		if !on {
			continue
		}
		id, err := strconv.ParseInt(key, 10, 32)
		if err != nil || id <= 0 {
			return m, nil, "", fmt.Errorf("invalid notification id %q", key)
		}
		notificationIDs = append(notificationIDs, int32(id))
	}
	sort.Slice(notificationIDs, func(i, j int) bool { return notificationIDs[i] < notificationIDs[j] })
	return m, notificationIDs, note, nil
}

// radiusPasswordField is the form's radiusPassword field, which the form uses for the password of
// MySQL and RADIUS, and the community of SNMP.
func radiusPasswordField(m monitor.Monitor) string {
	switch m.Type {
	case monitor.TypeSNMP:
		return m.SNMPCommunity
	case monitor.TypeRADIUS:
		return m.RadiusPassword
	}
	return m.DatabasePassword
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func orEmpty[T any](list []T) []T {
	if list == nil {
		return []T{}
	}
	return list
}

// oraclePassword is shown in the Oracle form's password field, which is basic_auth_pass.
// basicAuthUser and basicAuthPass are the form's basic_auth fields, which hold an Oracle
// monitor's database login, and the login of other types.
func basicAuthUser(m monitor.Monitor) string {
	if m.Type == monitor.TypeOracle {
		return m.DatabaseUsername
	}
	return m.BasicAuthUser
}

func basicAuthPass(m monitor.Monitor) string {
	if m.Type == monitor.TypeOracle {
		return m.DatabasePassword
	}
	return m.BasicAuthPass
}

func kafkaSASLJSON(s *monitor.KafkaSASL) any {
	if s == nil {
		return map[string]any{"mechanism": "None"}
	}
	return s
}

// rawText reads a field the form sends as text, or as a number from a number input.
func rawText(raw json.RawMessage) string {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

// conditionsJSON is a monitor's conditions as the form reads them, a list.
func conditionsJSON(raw json.RawMessage) any {
	var out any
	if len(raw) == 0 || json.Unmarshal(raw, &out) != nil || out == nil {
		return []any{}
	}
	return out
}

// parseHeaders reads the headers field: a JSON object as text, which is how the form's text
// box sends it, or an object.
func parseHeaders(raw json.RawMessage) (map[string]string, error) {
	headers := map[string]string{}
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || string(raw) == "null" {
		return headers, nil
	}
	if raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return nil, errors.New("headers must be a JSON object")
		}
		if strings.TrimSpace(text) == "" {
			return headers, nil
		}
		raw = []byte(text)
	}
	var values map[string]any
	if err := json.Unmarshal(raw, &values); err != nil {
		return nil, errors.New("headers must be a JSON object")
	}
	for name, value := range values {
		if text, ok := value.(string); ok {
			headers[name] = text
		} else {
			headers[name] = fmt.Sprint(value)
		}
	}
	return headers, nil
}

// parseGlobalping reads a Globalping monitor, whose fields depend on its measurement.
func parseGlobalping(m *monitor.Monitor, in monitorInput) error {
	m.Subtype = strings.TrimSpace(text(in.Subtype))
	m.Location = strings.TrimSpace(text(in.Location))
	m.Protocol = strings.TrimSpace(text(in.Protocol))
	if m.Protocol == "null" {
		m.Protocol = ""
	}
	if family := text(in.IPFamily); family != "" && family != "null" {
		m.IPFamily = family
	}
	switch m.Subtype {
	case "ping":
		m.Hostname = strings.TrimSpace(text(in.Hostname))
		m.Port = int32(in.Port.value)
		if in.PingCount.set {
			m.PingCount = int32(in.PingCount.value)
		}
	case "dns":
		m.Hostname = strings.TrimSpace(text(in.Hostname))
		m.Port = int32(in.Port.value)
		if m.Port == 0 {
			m.Port = 53
		}
		m.DNSResolveType = cmp.Or(strings.TrimSpace(in.DNSResolveType), "A")
		m.DNSResolveServer = strings.TrimSpace(in.DNSResolveServer)
		m.Keyword = text(in.Keyword)
	case "http":
		m.URL = strings.TrimSpace(in.URL)
		if method := strings.ToUpper(strings.TrimSpace(in.Method)); method != "" {
			m.Method = method
		}
		if in.AcceptedStatusCodes != nil {
			m.AcceptedStatusCodes = in.AcceptedStatusCodes
		}
		m.IgnoreTLS = in.IgnoreTLS
		m.CacheBust = in.CacheBust
		m.DNSResolveServer = strings.TrimSpace(in.DNSResolveServer)
		m.Keyword = text(in.Keyword)
		m.InvertKeyword = in.InvertKeyword
		m.JSONPath = text(in.JSONPath)
		if in.JSONPathOperator != nil && *in.JSONPathOperator != "" {
			m.JSONPathOperator = *in.JSONPathOperator
		}
		m.ExpectedValue = rawText(in.ExpectedValue)
		headers, err := parseHeaders(in.Headers)
		if err != nil {
			return err
		}
		m.Headers = headers
		// The probe gets only the headers of a login, so NTLM and mTLS don't apply.
		if method := text(in.AuthMethod); method == "basic" || method == "bearer" || method == "oauth2-cc" {
			applyRequestOptions(m, in)
			m.ProxyID, m.SaveResponse, m.SaveErrorResponse, m.ResponseMaxLength = 0, false, false, 0
			m.IPFamily = text(in.IPFamily)
			if m.IPFamily == "null" {
				m.IPFamily = ""
			}
		}
	}
	return nil
}

// applyRequestOptions reads what HTTP and WebSocket monitors share: the login, IP family and
// proxy, and for HTTP, cache busting and saved responses.
func applyRequestOptions(m *monitor.Monitor, in monitorInput) {
	m.AuthMethod = strings.TrimSpace(text(in.AuthMethod))
	if m.AuthMethod == "null" || m.AuthMethod == "none" {
		m.AuthMethod = ""
	}
	// Only the chosen login's settings are kept, so that secrets of another aren't stored.
	switch m.AuthMethod {
	case "basic", "ntlm":
		m.BasicAuthUser, m.BasicAuthPass = in.BasicAuthUser, in.BasicAuthPass
		if m.AuthMethod == "ntlm" {
			m.AuthDomain = strings.TrimSpace(text(in.AuthDomain))
			m.AuthWorkstation = strings.TrimSpace(text(in.AuthWorkstation))
		}
	case "bearer":
		m.BearerToken = in.BearerToken
	case "mtls":
		m.TLSCert, m.TLSKey, m.TLSCA = text(in.TLSCert), text(in.TLSKey), text(in.TLSCA)
	case "oauth2-cc":
		m.OAuthAuthMethod = strings.TrimSpace(text(in.OAuthAuthMethod))
		m.OAuthTokenURL = strings.TrimSpace(text(in.OAuthTokenURL))
		m.OAuthClientID = strings.TrimSpace(text(in.OAuthClientID))
		m.OAuthClientSecret = text(in.OAuthClientSecret)
		m.OAuthScopes = strings.TrimSpace(text(in.OAuthScopes))
		m.OAuthAudience = strings.TrimSpace(text(in.OAuthAudience))
	}
	if family := text(in.IPFamily); family == "ipv4" || family == "ipv6" {
		m.IPFamily = family
	} else if family != "" && family != "null" {
		m.IPFamily = family // Validate refuses it.
	}
	if m.Type == monitor.TypeWebSocket {
		return
	}
	m.ProxyID = int32(in.ProxyID.value)
	m.CacheBust = in.CacheBust
	m.SaveResponse, m.SaveErrorResponse = in.SaveResponse, in.SaveErrorResponse
	if m.SaveResponse || m.SaveErrorResponse {
		m.ResponseMaxLength = int32(in.ResponseMaxLength.value)
	}
	if m.Type == monitor.TypeJSONQuery {
		m.RetryOnlyOnStatusCodeFailure = in.RetryOnlyOnStatusCodeFailure
	}
}

// notificationJSON is a notification as the UI's list and dialog read it: config holds the
// whole form as JSON text.
func notificationJSON(n store.Notification) map[string]any {
	config := maps.Clone(n.Config)
	if config == nil {
		config = map[string]any{}
	}
	config["id"] = n.ID
	config["name"] = n.Name
	config["type"] = n.Type
	config["isDefault"] = n.IsDefault
	data, _ := json.Marshal(config)
	return map[string]any{
		"id":         n.ID,
		"name":       n.Name,
		"config":     string(data),
		"active":     n.Active,
		"isDefault":  n.IsDefault,
		"is_default": n.IsDefault,
		"user_id":    1,
	}
}

func chartJSON(points []store.ChartPoint) []map[string]any {
	out := make([]map[string]any, len(points))
	for i, p := range points {
		point := map[string]any{
			"timestamp": p.Time.Unix(),
			"up":        p.Up,
			"down":      p.Down,
			"avgPing":   p.AvgPing,
			"minPing":   p.MinPing,
			"maxPing":   p.MaxPing,
		}
		// The chart colours any point with this field as maintenance, so it's left out when zero.
		if p.Maintenance > 0 {
			point["maintenance"] = p.Maintenance
		}
		out[i] = point
	}
	return out
}
