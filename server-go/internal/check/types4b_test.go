package check

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestConnectionStrings(t *testing.T) {
	for in, want := range map[string]string{
		"Server=db,1433;User Id=sa;Password=x":     "Server=db,1433;User Id=sa;Password=x;encrypt=true",
		"Server=db;Encrypt=false;":                 "Server=db;Encrypt=false;",
		"sqlserver://sa:x@db:1433?database=master": "sqlserver://sa:x@db:1433?database=master&encrypt=true",
		"sqlserver://sa:x@db":                      "sqlserver://sa:x@db?encrypt=true",
	} {
		if got := sqlServerConnString(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
	for in, want := range map[string]string{
		"db:1521/FREEPDB1":       "oracle://u:p@db:1521/FREEPDB1",
		"//db/ORCL":              "oracle://u:p@db:1521/ORCL",
		"db:1522/ORCL:dedicated": "oracle://u:p@db:1522/ORCL",
	} {
		if got, err := oracleURL(in, "u", "p"); err != nil || got != want {
			t.Errorf("%s: %s %v", in, got, err)
		}
	}
	if got, err := oracleURL("(DESCRIPTION=(ADDRESS=(PROTOCOL=TCP)(HOST=db)(PORT=1521))(CONNECT_DATA=(SERVICE_NAME=X)))", "u", "p"); err != nil || !strings.Contains(got, "connStr=") {
		t.Errorf("descriptor: %s %v", got, err)
	}
	for host, want := range map[string]string{
		"broker":         "tcp://broker:1883",
		"mqtts://broker": "ssl://broker:1883",
		"ws://broker":    "ws://broker:1883/mqtt",
	} {
		m := monitor.Monitor{Hostname: host, Port: 1883, Settings: monitor.Settings{MQTTWebsocketPath: "mqtt"}}
		if got := mqttBroker(m); got != want {
			t.Errorf("%s: %s", host, got)
		}
	}
}

func TestJudgeMQTT(t *testing.T) {
	keyword := monitor.Monitor{Settings: monitor.Settings{MQTTTopic: "t/#", MQTTSuccessMessage: "ok"}}
	if r := judgeMQTT(keyword, "t/a", "all ok"); !r.OK || r.Msg != "Topic: t/a; Message: all ok" {
		t.Errorf("keyword: %+v", r)
	}
	if r := judgeMQTT(keyword, "t/a", "bad"); r.OK || r.Msg != "Message Mismatch - Topic: t/#; Message: bad" {
		t.Errorf("mismatch: %+v", r)
	}
	query := monitor.Monitor{JSONPath: "temp", ExpectedValue: "21.5", Settings: monitor.Settings{MQTTCheckType: "json-query"}}
	if r := judgeMQTT(query, "t", `{"temp": 21.5}`); !r.OK {
		t.Errorf("json query: %+v", r)
	}
	if r := judgeMQTT(query, "t", `{"temp": 30}`); r.OK || r.Msg != "Message received but value is not equal to expected value, value was: [30]" {
		t.Errorf("json query mismatch: %+v", r)
	}
	if r := judgeMQTT(query, "t", `{"other": 1}`); r.OK || !strings.HasSuffix(r.Msg, "[undefined]") {
		t.Errorf("json query missing: %+v", r)
	}
	conditional := monitor.Monitor{JSONPath: "temp", Conditions: json.RawMessage(`[
		{"type": "expression", "variable": "topic", "operator": "starts_with", "value": "sensors/"},
		{"type": "expression", "andOr": "and", "variable": "json_value", "operator": "lt", "value": "25"}]`)}
	if r := judgeMQTT(conditional, "sensors/1", `{"temp": 21.5}`); !r.OK {
		t.Errorf("conditions: %+v", r)
	}
	if r := judgeMQTT(conditional, "sensors/1", `{"temp": 30}`); r.OK || !strings.HasPrefix(r.Msg, "Conditions not met") {
		t.Errorf("conditions not met: %+v", r)
	}
}

func TestJSToString(t *testing.T) {
	for _, c := range []struct {
		in   any
		want string
	}{{[]any{1.0, "a", nil}, "1,a,"}, {map[string]any{"a": 1.0}, "[object Object]"}, {true, "true"}, {nil, "null"}} {
		if got := jsToString(c.in); got != c.want {
			t.Errorf("%v: %q", c.in, got)
		}
	}
}

func TestRabbitMQ(t *testing.T) {
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, _ := r.BasicAuth(); user != "guest" || pass != "guest" || r.URL.Path != "/prefix/api/health/checks/alarms/" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}))
	defer healthy.Close()
	alarmed := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"failed","reason":"There are alarms in effect in the cluster"}`))
	}))
	defer alarmed.Close()
	run := func(nodes ...string) monitor.Result {
		m := monitor.New()
		m.Type, m.Timeout = monitor.TypeRabbitMQ, 5
		m.RabbitMQNodes, m.RabbitMQUsername, m.RabbitMQPassword = nodes, "guest", "guest"
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run(healthy.URL + "/prefix"); !r.OK || r.Msg != "Node is reachable and there are no alerts in the cluster" {
		t.Errorf("one node: %+v", r)
	}
	if r := run(alarmed.URL, healthy.URL+"/prefix/"); !r.OK || !strings.HasPrefix(r.Msg, "One of the 2 nodes") {
		t.Errorf("second node: %+v", r)
	}
	if r := run(alarmed.URL, healthy.URL); r.OK || r.Msg != "All 2 nodes failed because Node 1: There are alarms in effect in the cluster; Node 2: 401 - Unauthorized" {
		t.Errorf("both fail: %+v", r)
	}
}

// The tests below need real servers, given by these variables:
//
//	TEST_MSSQL_DSN      Server=127.0.0.1,51433;User Id=sa;Password=...;TrustServerCertificate=true
//	TEST_ORACLE         127.0.0.1:51521/FREEPDB1
//	TEST_ORACLE_USER, TEST_ORACLE_PASSWORD
//	TEST_MONGODB_URL    mongodb://127.0.0.1:57017
//	TEST_MQTT_HOST      127.0.0.1:51883
//	TEST_KAFKA_BROKER   127.0.0.1:59092

func need(t *testing.T, names ...string) []string {
	t.Helper()
	var values []string
	for _, name := range names {
		v := os.Getenv(name)
		if v == "" {
			t.Skip(name + " is not set")
		}
		values = append(values, v)
	}
	return values
}

const resultIs = `[{"type":"expression","andOr":"and","variable":"result","operator":"equals","value":"%s"}]`

func sqlMonitor(kind, conn, query, conditions string) monitor.Monitor {
	m := monitor.New()
	m.Type, m.DatabaseConnectionString, m.DatabaseQuery, m.Timeout = kind, conn, query, 20
	if conditions != "" {
		m.Conditions = json.RawMessage(strings.Replace(resultIs, "%s", conditions, 1))
	}
	return m
}

func TestSQLServer(t *testing.T) {
	dsn := need(t, "TEST_MSSQL_DSN")[0]
	run := func(m monitor.Monitor) monitor.Result { return newChecker(t).Run(context.Background(), m) }
	if r := run(sqlMonitor(monitor.TypeSQLServer, dsn, "", "")); !r.OK || r.Msg != "Rows: 1" {
		t.Errorf("SELECT 1: %+v", r)
	}
	if r := run(sqlMonitor(monitor.TypeSQLServer, dsn, "SELECT 42", "42")); !r.OK || r.Msg != "Query did meet specified conditions" {
		t.Errorf("conditions: %+v", r)
	}
	if r := run(sqlMonitor(monitor.TypeSQLServer, dsn, "SELECT 'x' UNION ALL SELECT 'y'", "x")); r.OK || !strings.Contains(r.Msg, "Multiple values") {
		t.Errorf("two rows: %+v", r)
	}
	bad := strings.Replace(dsn, "Password=", "Password=wrong-mssql-pw", 1)
	if r := run(sqlMonitor(monitor.TypeSQLServer, bad, "", "")); r.OK || strings.Contains(r.Msg, "wrong-mssql-pw") ||
		!strings.HasPrefix(r.Msg, "Database connection/query failed: ") {
		t.Errorf("wrong password: %+v", r)
	}
}

func TestOracle(t *testing.T) {
	values := need(t, "TEST_ORACLE", "TEST_ORACLE_USER", "TEST_ORACLE_PASSWORD")
	run := func(query, conditions, password string) monitor.Result {
		m := sqlMonitor(monitor.TypeOracle, values[0], query, conditions)
		m.DatabaseUsername, m.DatabasePassword = values[1], password
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run("", "", values[2]); !r.OK || r.Msg != "Rows: 1" {
		t.Errorf("SELECT 1 FROM DUAL: %+v", r)
	}
	if r := run("SELECT 'up' FROM DUAL", "up", values[2]); !r.OK {
		t.Errorf("conditions: %+v", r)
	}
	if r := run("SELECT 'down' FROM DUAL", "up", values[2]); r.OK || r.Msg != "Query result did not meet the specified conditions (down)" {
		t.Errorf("conditions not met: %+v", r)
	}
	if r := run("", "", "wrong-oracle-pw"); r.OK || strings.Contains(r.Msg, "wrong-oracle-pw") {
		t.Errorf("wrong password: %+v", r)
	}
}

func TestMongoDB(t *testing.T) {
	url := need(t, "TEST_MONGODB_URL")[0]
	run := func(query, path, expected string) monitor.Result {
		m := monitor.New()
		m.Type, m.DatabaseConnectionString, m.DatabaseQuery, m.JSONPath, m.ExpectedValue, m.Timeout = monitor.TypeMongoDB, url, query, path, expected, 10
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run("", "", ""); !r.OK || r.Msg != "Command executed successfully" {
		t.Errorf("ping: %+v", r)
	}
	if r := run(`{"serverStatus": 1}`, "ok", "1"); !r.OK || r.Msg != "Command executed successfully and expected value was found" {
		t.Errorf("expected value: %+v", r)
	}
	if r := run(`{"buildInfo": 1}`, "missingField", ""); r.OK || r.Msg != "Queried value not found." {
		t.Errorf("missing field: %+v", r)
	}
	if r := run(`{"noSuchCommand": 1}`, "", ""); r.OK {
		t.Errorf("unknown command: %+v", r)
	}
}

func TestMQTT(t *testing.T) {
	host := need(t, "TEST_MQTT_HOST")[0]
	hostname, port, _ := strings.Cut(host, ":")
	topic := "uptime-kuma-test/" + time.Now().Format("150405.000")
	publisher := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://" + host))
	if token := publisher.Connect(); token.Wait() && token.Error() != nil {
		t.Fatal(token.Error())
	}
	defer publisher.Disconnect(0)
	// A retained message reaches every new subscriber.
	publisher.Publish(topic, 1, true, `{"state": "running"}`).Wait()
	defer func() { publisher.Publish(topic, 1, true, "").Wait() }()

	m := monitor.New()
	m.Type, m.Hostname, m.Timeout = monitor.TypeMQTT, hostname, 5
	m.Port = int32(atoi(port))
	m.MQTTTopic, m.MQTTSuccessMessage = topic, "running"
	if r := newChecker(t).Run(context.Background(), m); !r.OK || !strings.HasPrefix(r.Msg, "Topic: "+topic) {
		t.Errorf("keyword: %+v", r)
	}
	m.MQTTTopic = topic + "/silent"
	m.Timeout = 1
	if r := newChecker(t).Run(context.Background(), m); r.OK || r.Msg != "Timeout, Message not received" {
		t.Errorf("silent topic: %+v", r)
	}
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		n = n*10 + int(c-'0')
	}
	return n
}

func TestKafka(t *testing.T) {
	broker := need(t, "TEST_KAFKA_BROKER")[0]
	topic := "uptime-kuma-test-" + time.Now().Format("150405")
	m := monitor.New()
	m.Type, m.Timeout = monitor.TypeKafka, 15
	m.KafkaBrokers, m.KafkaTopic, m.KafkaMessage, m.KafkaAllowAutoTopicCreation = []string{broker}, topic, "hello", true
	if r := newChecker(t).Run(context.Background(), m); !r.OK || r.Msg != "Message sent successfully" {
		t.Fatalf("send: %+v", r)
	}
	consumer, err := kgo.NewClient(kgo.SeedBrokers(broker), kgo.ConsumeTopics(topic), kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()))
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fetches := consumer.PollFetches(ctx)
	if records := fetches.Records(); len(records) == 0 || string(records[0].Value) != "hello" {
		t.Errorf("not received: %v %v", records, fetches.Errors())
	}

	m.KafkaTopic, m.KafkaAllowAutoTopicCreation = topic+"-missing", false
	if r := newChecker(t).Run(context.Background(), m); r.OK || !strings.HasPrefix(r.Msg, "Error sending message: ") {
		t.Errorf("missing topic: %+v", r)
	}
	m.KafkaBrokers = []string{"127.0.0.1:1"}
	m.Timeout = 3
	if r := newChecker(t).Run(context.Background(), m); r.OK || !strings.HasPrefix(r.Msg, "Error in producer connection: ") {
		t.Errorf("no broker: %+v", r)
	}
}
