// Package monitor defines monitor configuration and the rules that turn a check
// result into a beat. The rules mirror the Node.js backend's server/model/monitor.js.
package monitor

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	jsonata "github.com/blues/jsonata-go"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/conditions"
)

// Supported monitor types, using the Node.js backend's names.
const (
	TypeHTTP    = "http"
	TypeKeyword = "keyword"
	TypePort    = "port"
	TypeGroup   = "group"
	TypePush    = "push"
	TypePing    = "ping"
	TypeDNS     = "dns"

	TypeJSONQuery = "json-query"
	TypeManual    = "manual"
	TypeWebSocket = "websocket-upgrade"
	TypePostgres  = "postgres"
	TypeMySQL     = "mysql"
	TypeRedis     = "redis"

	TypeSQLServer = "sqlserver"
	TypeOracle    = "oracledb"
	TypeMongoDB   = "mongodb"
	TypeMQTT      = "mqtt"
	TypeRabbitMQ  = "rabbitmq"
	TypeKafka     = "kafka-producer"

	TypeNTP    = "ntp"
	TypeSNMP   = "snmp"
	TypeSMTP   = "smtp"
	TypeSIP    = "sip-options"
	TypeRADIUS = "radius"
	TypeSteam  = "steam"
)

// Types are every supported type, in the order error messages list them.
var Types = []string{TypeHTTP, TypeKeyword, TypeJSONQuery, TypePort, TypePing, TypeDNS, TypePush, TypeGroup,
	TypeManual, TypeWebSocket, TypePostgres, TypeMySQL, TypeRedis, TypeSQLServer, TypeOracle, TypeMongoDB, TypeMQTT,
	TypeRabbitMQ, TypeKafka, TypeNTP, TypeSNMP, TypeSMTP, TypeSIP, TypeRADIUS, TypeSteam}

// SNMPVersions and SMTPSecurities are the choices of the SNMP and SMTP types.
var (
	SNMPVersions   = []string{"1", "2c", "3"}
	SMTPSecurities = []string{"", "secure", "nostarttls", "starttls"}
	defaultPortOf  = map[string]int32{TypeNTP: 123, TypeSNMP: 161, TypeSMTP: 25, TypeSIP: 5060, TypeRADIUS: 1812}
	hostnameTypes  = []string{TypeNTP, TypeSNMP, TypeSMTP, TypeSIP, TypeRADIUS, TypeSteam}
)

// KafkaSASLMechanisms are the SASL mechanisms Kafka producer monitors can log in with.
var KafkaSASLMechanisms = []string{"None", "plain", "scram-sha-256", "scram-sha-512"}

// JSONQueryOperators compare the result of a JSON query with the expected value.
var JSONQueryOperators = []string{"==", "!=", "contains", ">", ">=", "<", "<="}

// ConditionVariables are what each type's conditions can test. Types without any don't take
// conditions.
var ConditionVariables = map[string][]conditions.Variable{
	TypeDNS:       {{ID: "record", Operators: conditions.StringOperators}},
	TypeMySQL:     {{ID: "result", Operators: conditions.StringOperators}},
	TypeSQLServer: {{ID: "result", Operators: conditions.StringOperators}},
	TypeOracle:    {{ID: "result", Operators: conditions.StringOperators}},
	TypeMQTT: {
		{ID: "topic", Operators: conditions.StringOperators},
		{ID: "message", Operators: conditions.StringOperators},
		{ID: "json_value", Operators: append(slices.Clone(conditions.StringOperators), conditions.NumberOperators...)},
	},
}

// Beat statuses, using the Node.js backend's values.
const (
	StatusDown        int16 = 0
	StatusUp          int16 = 1
	StatusPending     int16 = 2
	StatusMaintenance int16 = 3
)

const (
	minIntervalSeconds = 1
	maxIntervalSeconds = 2073600 // Same upper bound as the Node.js UI: 24 days.
	maxRetries         = 1000
	maxRedirects       = 100
	maxBodyLen         = 1 << 20
	redactedValue      = "***"

	// The limits and defaults of ping settings, the same as the Node.js backend's.
	minPingCount, maxPingCount           = 1, 100
	minPingPerRequest, maxPingPerRequest = 1, 60
	minPacketSize, maxPacketSize         = 1, 65500
	minPingTimeout, maxPingTimeout       = 1, 300
	defaultPingCount                     = 1
	defaultPingPerRequest                = 2
	defaultPacketSize                    = 56

	defaultDNSServer = "1.1.1.1"
	defaultDNSPort   = 53
	defaultDNSType   = "A"
)

var (
	statusCodeRange = regexp.MustCompile(`^\d{3}(-\d{3})?$`)
	httpMethods     = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
	// Tokens from older Node.js versions are shorter than the 32 characters the UI makes now.
	pushTokenFormat = regexp.MustCompile(`^[A-Za-z0-9_-]{8,64}$`)
	dnsTypes        = []string{"A", "AAAA", "CAA", "CNAME", "MX", "NS", "PTR", "SOA", "SRV", "TXT"}
)

// Monitor is one monitor's configuration. Timeout is in seconds and 0 means 80%
// of the interval. Times are in seconds.
type Monitor struct {
	ID            int32   `json:"id"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Active        bool    `json:"active"`
	Interval      int32   `json:"interval"`
	RetryInterval int32   `json:"retryInterval"`
	Timeout       float64 `json:"timeout"`
	MaxRetries    int32   `json:"maxRetries"`
	// ResendInterval repeats the alert every this many DOWN beats. 0 alerts once.
	ResendInterval int32 `json:"resendInterval"`
	UpsideDown     bool  `json:"upsideDown"`
	// ParentID is the group that this monitor belongs to, if any.
	ParentID *int32 `json:"parentId"`

	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	Headers             map[string]string `json:"headers"`
	Body                string            `json:"body"`
	BodyEncoding        string            `json:"bodyEncoding"`
	AcceptedStatusCodes []string          `json:"acceptedStatusCodes"`
	MaxRedirects        int32             `json:"maxRedirects"`
	IgnoreTLS           bool              `json:"ignoreTls"`
	// ExpiryNotification alerts before the certificate expires. It has no effect with IgnoreTLS.
	ExpiryNotification bool   `json:"expiryNotification"`
	Keyword            string `json:"keyword"`
	InvertKeyword      bool   `json:"invertKeyword"`

	Hostname string `json:"hostname"`
	Port     int32  `json:"port"`

	// PushToken is the secret in a push monitor's URL. Empty means make one when saving.
	PushToken string `json:"pushToken"`

	// PingCount is how many replies a ping waits for. PingPerRequestTimeout is in seconds.
	PingCount             int32 `json:"pingCount"`
	PingPerRequestTimeout int32 `json:"pingPerRequestTimeout"`
	PacketSize            int32 `json:"packetSize"`

	DNSResolveType string `json:"dnsResolveType"`
	// DNSResolveServer is a comma-separated list of IP addresses or hostnames.
	DNSResolveServer string `json:"dnsResolveServer"`
	// DNSLastResult is set by the checker, and ignored in input.
	DNSLastResult string `json:"dnsLastResult"`

	// JSONPath is a JSONata expression, evaluated on the response of a json-query monitor.
	JSONPath         string `json:"jsonPath"`
	JSONPathOperator string `json:"jsonPathOperator"`
	ExpectedValue    string `json:"expectedValue"`

	// Conditions decide whether a DNS or MySQL monitor passes, as the UI sends them.
	Conditions json.RawMessage `json:"conditions"`

	// ManualStatus is the status a manual monitor reports: UP, DOWN, or nil for PENDING.
	ManualStatus *int16 `json:"manualStatus"`

	// WSSubprotocol is a comma-separated list of WebSocket subprotocols to ask for.
	WSSubprotocol string `json:"wsSubprotocol"`
	// WSIgnoreAcceptHeader accepts a handshake with a wrong Sec-WebSocket-Accept header.
	WSIgnoreAcceptHeader bool `json:"wsIgnoreSecWebsocketAcceptHeader"`

	// DatabaseConnectionString is a URL, such as postgres://user:password@host/db.
	DatabaseConnectionString string `json:"databaseConnectionString"`
	DatabaseQuery            string `json:"databaseQuery"`
	// DatabasePassword replaces the password in a MySQL connection string, if it's set, and is
	// the password of an Oracle monitor.
	DatabasePassword string `json:"databasePassword"`

	Settings

	// PropagateTrace sends a W3C traceparent header to the target, so that the target's
	// traces join this one. It's off by default, to not share trace ids with third parties.
	PropagateTrace bool `json:"propagateTrace"`
}

// Settings are the settings of the types added since Phase 4b, which are stored together.
type Settings struct {
	// DatabaseUsername is an Oracle monitor's user.
	DatabaseUsername string `json:"databaseUsername,omitempty"`

	MQTTTopic          string `json:"mqttTopic,omitempty"`
	MQTTUsername       string `json:"mqttUsername,omitempty"`
	MQTTPassword       string `json:"mqttPassword,omitempty"`
	MQTTWebsocketPath  string `json:"mqttWebsocketPath,omitempty"`
	MQTTCheckType      string `json:"mqttCheckType,omitempty"`
	MQTTSuccessMessage string `json:"mqttSuccessMessage,omitempty"`

	// RabbitMQNodes are the base URLs of the management API of each node.
	RabbitMQNodes    []string `json:"rabbitmqNodes,omitempty"`
	RabbitMQUsername string   `json:"rabbitmqUsername,omitempty"`
	RabbitMQPassword string   `json:"rabbitmqPassword,omitempty"`

	// KafkaBrokers are host:port addresses.
	KafkaBrokers                []string   `json:"kafkaProducerBrokers,omitempty"`
	KafkaTopic                  string     `json:"kafkaProducerTopic,omitempty"`
	KafkaMessage                string     `json:"kafkaProducerMessage,omitempty"`
	KafkaSSL                    bool       `json:"kafkaProducerSsl,omitempty"`
	KafkaAllowAutoTopicCreation bool       `json:"kafkaProducerAllowAutoTopicCreation,omitempty"`
	KafkaSASL                   *KafkaSASL `json:"kafkaProducerSaslOptions,omitempty"`

	// The thresholds an NTP server must stay under: its stratum, and its offset and root
	// dispersion in milliseconds. Zero means the Node.js backend's defaults, 5, 1000 and 500.
	NTPStratumThreshold        int32   `json:"ntpStratumThreshold,omitempty"`
	NTPTimeOffsetThreshold     float64 `json:"ntpTimeOffsetThreshold,omitempty"`
	NTPRootDispersionThreshold float64 `json:"ntpRootDispersionThreshold,omitempty"`

	SNMPOID        string `json:"snmpOid,omitempty"`
	SNMPVersion    string `json:"snmpVersion,omitempty"`
	SNMPCommunity  string `json:"snmpCommunity,omitempty"`
	SNMPV3Username string `json:"snmpV3Username,omitempty"`

	// SMTPSecurity is secure (SMTPS), starttls (required), nostarttls, or empty for STARTTLS when
	// the server offers it.
	SMTPSecurity string `json:"smtpSecurity,omitempty"`

	RadiusUsername         string `json:"radiusUsername,omitempty"`
	RadiusPassword         string `json:"radiusPassword,omitempty"`
	RadiusSecret           string `json:"radiusSecret,omitempty"`
	RadiusCalledStationID  string `json:"radiusCalledStationId,omitempty"`
	RadiusCallingStationID string `json:"radiusCallingStationId,omitempty"`
}

// KafkaSASL is how a Kafka producer monitor logs in.
type KafkaSASL struct {
	Mechanism             string `json:"mechanism"`
	Username              string `json:"username,omitempty"`
	Password              string `json:"password,omitempty"`
	AuthorizationIdentity string `json:"authorizationIdentity,omitempty"`
}

// New returns a monitor with the Node.js backend's defaults, for decoding input into.
func New() Monitor {
	return Monitor{
		Active:              true,
		Interval:            60,
		Method:              "GET",
		BodyEncoding:        "json",
		AcceptedStatusCodes: []string{"200-299"},
		MaxRedirects:        10,
		Headers:             map[string]string{},

		PingCount:             defaultPingCount,
		PingPerRequestTimeout: defaultPingPerRequest,
		PacketSize:            defaultPacketSize,
		DNSResolveType:        defaultDNSType,
		DNSResolveServer:      defaultDNSServer,
		JSONPathOperator:      "==",
		Conditions:            json.RawMessage(`[]`),
	}
}

// Normalize replaces empty collections, so that explicit JSON nulls can't reach the database,
// and gives a DNS monitor the standard port.
func (m *Monitor) Normalize() {
	if m.Type == TypeDNS && m.Port == 0 {
		m.Port = defaultDNSPort
	}
	if port, found := defaultPortOf[m.Type]; found && m.Port == 0 {
		m.Port = port
	}
	if m.Type == TypeSNMP && m.SNMPVersion == "" {
		m.SNMPVersion = "2c"
	}
	if m.Headers == nil {
		m.Headers = map[string]string{}
	}
	if m.AcceptedStatusCodes == nil {
		m.AcceptedStatusCodes = []string{}
	}
	if len(m.Conditions) == 0 || string(m.Conditions) == "null" {
		m.Conditions = json.RawMessage(`[]`)
	}
	if m.Type == TypeWebSocket && len(m.AcceptedStatusCodes) == 0 {
		m.AcceptedStatusCodes = []string{"1000"}
	}
}

// Redacted returns a copy without the fields that can hold credentials, for clients
// that may only read: header values, the body, the push token, the database password, and the
// user info and query of the URL and the database connection string.
func (m Monitor) Redacted() Monitor {
	if m.DatabasePassword != "" {
		m.DatabasePassword = redactedValue
	}
	if m.DatabaseConnectionString != "" {
		if strings.Contains(m.DatabaseConnectionString, "://") {
			m.DatabaseConnectionString = redactURL(m.DatabaseConnectionString)
		} else {
			// SQL Server's and Oracle's strings aren't URLs, and can hold a password anywhere.
			m.DatabaseConnectionString = redactedValue
		}
	}
	for _, secret := range []*string{&m.MQTTPassword, &m.RabbitMQPassword, &m.SNMPCommunity, &m.RadiusPassword, &m.RadiusSecret} {
		if *secret != "" {
			*secret = redactedValue
		}
	}
	if m.KafkaSASL != nil && m.KafkaSASL.Password != "" {
		sasl := *m.KafkaSASL
		sasl.Password = redactedValue
		m.KafkaSASL = &sasl
	}
	headers := make(map[string]string, len(m.Headers))
	for name := range m.Headers {
		headers[name] = redactedValue
	}
	m.Headers = headers
	if m.Body != "" {
		m.Body = redactedValue
	}
	if m.PushToken != "" {
		m.PushToken = redactedValue
	}
	if m.URL != "" {
		m.URL = redactURL(m.URL)
	}
	return m
}

// redactURL leaves out a URL's user info, query and fragment, which can hold credentials.
func redactURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return redactedValue
	}
	u.User = nil
	if u.RawQuery != "" {
		u.RawQuery = redactedValue
	}
	u.Fragment = ""
	return u.String()
}

// Validate reports the first problem with the configuration.
func (m *Monitor) Validate() error {
	name := strings.TrimSpace(m.Name)
	switch {
	case name == "" || len(name) > 150:
		return errors.New("name must be 1 to 150 characters")
	case m.Interval < minIntervalSeconds || m.Interval > maxIntervalSeconds:
		return fmt.Errorf("interval must be from %d to %d seconds", minIntervalSeconds, maxIntervalSeconds)
	case m.RetryInterval < 0 || m.RetryInterval > maxIntervalSeconds:
		return fmt.Errorf("retryInterval must be from 0 to %d seconds", maxIntervalSeconds)
	case m.Timeout < 0 || m.Timeout > maxIntervalSeconds:
		return fmt.Errorf("timeout must be from 0 to %d seconds", maxIntervalSeconds)
	case m.MaxRetries < 0 || m.MaxRetries > maxRetries:
		return fmt.Errorf("maxRetries must be from 0 to %d", maxRetries)
	case m.ResendInterval < 0:
		return errors.New("resendInterval must not be negative")
	}

	if m.ParentID != nil && *m.ParentID <= 0 {
		return errors.New("parentId must be a positive integer")
	}

	if err := m.validateConditions(); err != nil {
		return err
	}

	switch m.Type {
	case TypeGroup:
		return nil
	case TypeHTTP:
		return m.validateHTTP()
	case TypeJSONQuery:
		if !slices.Contains(JSONQueryOperators, m.JSONPathOperator) {
			return fmt.Errorf("jsonPathOperator must be one of %s", strings.Join(JSONQueryOperators, ", "))
		}
		if len(m.JSONPath) > maxBodyLen || len(m.ExpectedValue) > maxBodyLen {
			return errors.New("jsonPath and expectedValue must be at most 1 MB")
		}
		if strings.TrimSpace(m.JSONPath) != "" {
			if _, err := jsonata.Compile(m.JSONPath); err != nil {
				return fmt.Errorf("jsonPath isn't a valid JSONata expression: %w", err)
			}
		}
		return m.validateHTTP()
	case TypeManual:
		if m.ManualStatus != nil && *m.ManualStatus != StatusDown && *m.ManualStatus != StatusUp && *m.ManualStatus != StatusPending {
			return errors.New("manualStatus must be 0 (down), 1 (up), 2 (pending) or null")
		}
		return nil
	case TypeWebSocket:
		return m.validateWebSocket()
	case TypePostgres, TypeMySQL, TypeRedis, TypeMongoDB:
		return m.validateDatabase()
	case TypeSQLServer, TypeOracle:
		if strings.TrimSpace(m.DatabaseConnectionString) == "" || len(m.DatabaseConnectionString) > 4096 {
			return errors.New("databaseConnectionString is required, and at most 4096 characters")
		}
		if len(m.DatabaseQuery) > maxBodyLen {
			return fmt.Errorf("databaseQuery must be at most %d bytes", maxBodyLen)
		}
		return nil
	case TypeMQTT:
		return m.validateMQTT()
	case TypeRabbitMQ:
		return m.validateRabbitMQ()
	case TypeKafka:
		return m.validateKafka()
	case TypeNTP, TypeSNMP, TypeSMTP, TypeSIP, TypeRADIUS, TypeSteam:
		return m.validateNetwork()
	case TypeKeyword:
		if m.Keyword == "" {
			return errors.New("keyword is required")
		}
		return m.validateHTTP()
	case TypePort:
		if strings.TrimSpace(m.Hostname) == "" {
			return errors.New("hostname is required")
		}
		if m.Port < 1 || m.Port > 65535 {
			return errors.New("port must be from 1 to 65535")
		}
		return nil
	case TypePush:
		if m.PushToken != "" && !pushTokenFormat.MatchString(m.PushToken) {
			return errors.New("pushToken must be 8 to 64 letters, digits, dashes or underscores")
		}
		return nil
	case TypePing:
		return m.validatePing()
	case TypeDNS:
		return m.validateDNS()
	default:
		return fmt.Errorf("type must be one of %s", strings.Join(Types, ", "))
	}
}

var (
	closeCodeRange  = regexp.MustCompile(`^\d{3,4}(-\d{3,4})?$`)
	databaseSchemes = map[string][]string{
		TypePostgres: {"postgres", "postgresql"},
		TypeMySQL:    {"mysql", "mariadb"},
		TypeRedis:    {"redis", "rediss"},
		TypeMongoDB:  {"mongodb", "mongodb+srv"},
	}
	snmpOID  = regexp.MustCompile(`^\.?[0-9]+(\.[0-9]+)+$`)
	mqttHost = regexp.MustCompile(`^((mqtt|mqtts|ws|wss|tcp|ssl|tls)://)?[A-Za-z0-9.\-_\[\]:]+$`)
)

func (m *Monitor) validateConditions() error {
	nodes, err := conditions.Parse(m.Conditions)
	if err != nil {
		return err
	}
	if len(nodes) == 0 {
		return nil
	}
	variables, found := ConditionVariables[m.Type]
	if !found {
		return fmt.Errorf("a %s monitor can't have conditions", m.Type)
	}
	return conditions.Validate(nodes, variables)
}

func (m *Monitor) validateWebSocket() error {
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "ws" && u.Scheme != "wss") || u.Host == "" {
		return errors.New("url must be a valid ws or wss URL")
	}
	if len(m.AcceptedStatusCodes) == 0 {
		return errors.New("acceptedStatusCodes must not be empty")
	}
	for _, code := range m.AcceptedStatusCodes {
		if !closeCodeRange.MatchString(code) {
			return fmt.Errorf("invalid status code range %q", code)
		}
	}
	return nil
}

func (m *Monitor) validateDatabase() error {
	raw := strings.TrimSpace(m.DatabaseConnectionString)
	u, err := url.Parse(raw)
	if raw == "" || err != nil || u.Host == "" || !slices.Contains(databaseSchemes[m.Type], u.Scheme) {
		return fmt.Errorf("databaseConnectionString must be a URL starting with %s://", databaseSchemes[m.Type][0])
	}
	if len(m.DatabaseQuery) > maxBodyLen {
		return fmt.Errorf("databaseQuery must be at most %d bytes", maxBodyLen)
	}
	if m.Type == TypeMongoDB {
		if q := strings.TrimSpace(m.DatabaseQuery); q != "" {
			var command map[string]any
			if json.Unmarshal([]byte(q), &command) != nil || len(command) == 0 {
				return errors.New("databaseQuery of a MongoDB monitor must be a JSON object, such as {\"ping\": 1}")
			}
		}
		if err := validJSONPath(m.JSONPath); err != nil {
			return err
		}
	}
	return nil
}

func validJSONPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if len(path) > maxBodyLen {
		return errors.New("jsonPath must be at most 1 MB")
	}
	if _, err := jsonata.Compile(path); err != nil {
		return fmt.Errorf("jsonPath isn't a valid JSONata expression: %w", err)
	}
	return nil
}

func (m *Monitor) validateMQTT() error {
	if !mqttHost.MatchString(strings.TrimSpace(m.Hostname)) {
		return errors.New("hostname must be a host name, optionally with mqtt://, mqtts://, ws:// or wss://")
	}
	if m.Port < 1 || m.Port > 65535 {
		return errors.New("port must be from 1 to 65535")
	}
	if strings.TrimSpace(m.MQTTTopic) == "" {
		return errors.New("mqttTopic is required")
	}
	switch m.MQTTCheckType {
	case "", "keyword":
	case "json-query":
		if strings.TrimSpace(m.JSONPath) == "" {
			return errors.New("jsonPath is required for a json-query check")
		}
	default:
		return errors.New("mqttCheckType must be keyword or json-query")
	}
	return validJSONPath(m.JSONPath)
}

func (m *Monitor) validateNetwork() error {
	if h := strings.TrimSpace(m.Hostname); h == "" || strings.ContainsAny(h, " /\\@") {
		return errors.New("hostname must be a host name or an IP address")
	}
	if m.Port < 1 || m.Port > 65535 {
		return errors.New("port must be from 1 to 65535")
	}
	switch m.Type {
	case TypeNTP:
		if m.NTPStratumThreshold < 0 || m.NTPStratumThreshold > 16 || m.NTPTimeOffsetThreshold < 0 || m.NTPRootDispersionThreshold < 0 {
			return errors.New("ntpStratumThreshold must be from 0 to 16, and the other thresholds positive")
		}
	case TypeSNMP:
		if !slices.Contains(SNMPVersions, m.SNMPVersion) {
			return fmt.Errorf("snmpVersion must be one of %s", strings.Join(SNMPVersions, ", "))
		}
		if !snmpOID.MatchString(m.SNMPOID) {
			return errors.New("snmpOid must be a numeric OID, such as 1.3.6.1.2.1.1.3.0")
		}
		if m.SNMPVersion == "3" && strings.TrimSpace(m.SNMPV3Username) == "" {
			return errors.New("SNMPv3 username is required")
		}
		if !slices.Contains(JSONQueryOperators, m.JSONPathOperator) {
			return fmt.Errorf("jsonPathOperator must be one of %s", strings.Join(JSONQueryOperators, ", "))
		}
		return validJSONPath(m.JSONPath)
	case TypeSMTP:
		if !slices.Contains(SMTPSecurities, m.SMTPSecurity) {
			return errors.New("smtpSecurity must be secure, starttls or nostarttls")
		}
	case TypeRADIUS:
		if m.RadiusSecret == "" {
			return errors.New("radiusSecret is required")
		}
	}
	return nil
}

func (m *Monitor) validateRabbitMQ() error {
	if len(m.RabbitMQNodes) == 0 {
		return errors.New("rabbitmqNodes must list at least one node")
	}
	for _, node := range m.RabbitMQNodes {
		u, err := url.Parse(node)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("%q isn't an http(s) URL of a node's management API", node)
		}
	}
	return nil
}

func (m *Monitor) validateKafka() error {
	if len(m.KafkaBrokers) == 0 {
		return errors.New("kafkaProducerBrokers must list at least one broker")
	}
	for _, broker := range m.KafkaBrokers {
		if _, port, err := net.SplitHostPort(broker); err != nil || port == "" {
			return fmt.Errorf("broker %q must be host:port", broker)
		}
	}
	if strings.TrimSpace(m.KafkaTopic) == "" {
		return errors.New("kafkaProducerTopic is required")
	}
	if m.KafkaSASL != nil && !slices.Contains(KafkaSASLMechanisms, m.KafkaSASL.Mechanism) {
		return fmt.Errorf("the SASL mechanism must be one of %s; AWS IAM isn't supported yet", strings.Join(KafkaSASLMechanisms, ", "))
	}
	return nil
}

func (m *Monitor) validatePing() error {
	if strings.TrimSpace(m.Hostname) == "" {
		return errors.New("hostname is required")
	}
	if m.PingCount < minPingCount || m.PingCount > maxPingCount {
		return fmt.Errorf("pingCount must be from %d to %d", minPingCount, maxPingCount)
	}
	if m.PingPerRequestTimeout < minPingPerRequest || m.PingPerRequestTimeout > maxPingPerRequest {
		return fmt.Errorf("pingPerRequestTimeout must be from %d to %d seconds", minPingPerRequest, maxPingPerRequest)
	}
	if m.PacketSize < minPacketSize || m.PacketSize > maxPacketSize {
		return fmt.Errorf("packetSize must be from %d to %d", minPacketSize, maxPacketSize)
	}
	if m.Timeout > 0 {
		timeout := int32(math.Round(m.Timeout))
		if timeout < minPingTimeout || timeout > maxPingTimeout {
			return fmt.Errorf("timeout of a ping must be from %d to %d seconds", minPingTimeout, maxPingTimeout)
		}
		if timeout < m.PingPerRequestTimeout {
			return errors.New("timeout must not be shorter than pingPerRequestTimeout")
		}
	}
	return nil
}

func (m *Monitor) validateDNS() error {
	hostname := strings.TrimSpace(m.Hostname)
	if hostname == "" {
		return errors.New("hostname is required")
	}
	if m.Port < 1 || m.Port > 65535 {
		return errors.New("port must be from 1 to 65535")
	}
	if !slices.Contains(dnsTypes, m.DNSResolveType) {
		return fmt.Errorf("dnsResolveType must be one of %s", strings.Join(dnsTypes, ", "))
	}
	if m.DNSResolveType == "PTR" && net.ParseIP(hostname) == nil {
		return errors.New("hostname of a PTR lookup must be an IP address")
	}
	if strings.Trim(m.DNSResolveServer, " ,") == "" {
		return errors.New("dnsResolveServer is required")
	}
	return nil
}

// NewPushToken makes a token in the format of the UI's: 32 letters and digits.
func NewPushToken() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	token := make([]byte, 32)
	for i := range token {
		// rand.Int only fails if the system has no randomness, where nothing else would work.
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			panic(err)
		}
		token[i] = letters[n.Int64()]
	}
	return string(token)
}

func (m *Monitor) validateHTTP() error {
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be a valid http or https URL")
	}
	if !slices.Contains(httpMethods, m.Method) {
		return fmt.Errorf("method must be one of %s", strings.Join(httpMethods, ", "))
	}
	if m.MaxRedirects < 0 || m.MaxRedirects > maxRedirects {
		return fmt.Errorf("maxRedirects must be from 0 to %d", maxRedirects)
	}
	if len(m.Body) > maxBodyLen {
		return fmt.Errorf("body must be at most %d bytes", maxBodyLen)
	}
	switch m.BodyEncoding {
	case "json":
		if strings.TrimSpace(m.Body) != "" && !json.Valid([]byte(m.Body)) {
			return errors.New("body is not valid JSON")
		}
	case "form", "xml":
	default:
		return errors.New("bodyEncoding must be json, form or xml")
	}
	if len(m.AcceptedStatusCodes) == 0 {
		return errors.New("acceptedStatusCodes must not be empty")
	}
	for _, code := range m.AcceptedStatusCodes {
		if !statusCodeRange.MatchString(code) {
			return fmt.Errorf("invalid status code range %q", code)
		}
	}
	return nil
}

// EffectiveTimeout is how long one check may take.
func (m *Monitor) EffectiveTimeout() time.Duration {
	seconds := m.Timeout
	if seconds <= 0 {
		seconds = float64(m.Interval) * 0.8
	}
	return time.Duration(seconds * float64(time.Second))
}

// Accepts reports whether an HTTP status code counts as success.
func (m *Monitor) Accepts(status int) bool {
	for _, code := range m.AcceptedStatusCodes {
		low, high, isRange := strings.Cut(code, "-")
		first, err := strconv.Atoi(low)
		if err != nil {
			continue
		}
		last := first
		if isRange {
			if last, err = strconv.Atoi(high); err != nil {
				continue
			}
		}
		if status >= first && status <= last {
			return true
		}
	}
	return false
}
