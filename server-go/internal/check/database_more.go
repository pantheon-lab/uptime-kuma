package check

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"

	mssql "github.com/microsoft/go-mssqldb"
	"github.com/microsoft/go-mssqldb/msdsn"
	goora "github.com/sijms/go-ora/v2"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	jsonata "github.com/blues/jsonata-go"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// sqlServerConnString encrypts the connection unless the string says otherwise, because the
// mssql package of Node.js did. An ADO string or a sqlserver:// URL both work.
func sqlServerConnString(raw string) string {
	raw = strings.TrimSpace(raw)
	if strings.Contains(strings.ToLower(raw), "encrypt") {
		return raw
	}
	if strings.HasPrefix(strings.ToLower(raw), "sqlserver://") {
		if strings.Contains(raw, "?") {
			return raw + "&encrypt=true"
		}
		return raw + "?encrypt=true"
	}
	return strings.TrimSuffix(raw, ";") + ";encrypt=true"
}

func runSQLServer(ctx context.Context, m monitor.Monitor) monitor.Result {
	ctx, span := databaseSpan(ctx, m, "mssql")
	defer span.End()
	config, err := msdsn.Parse(sqlServerConnString(m.DatabaseConnectionString))
	if err != nil {
		// The parser's errors can repeat the string, password included.
		return monitor.Result{Msg: "Database connection/query failed: invalid connection string"}
	}
	if m.DatabasePassword == "" {
		m.DatabasePassword = config.Password
	}
	connector := mssql.NewConnectorConfig(config)
	return runSQL(ctx, m, connector, "No Error, but the result is not an array. Type: undefined", false)
}

// oracleURL turns the Node.js backend's connect string, Easy Connect such as db:1521/FREEPDB1 or
// a (DESCRIPTION=...) descriptor, into go-ora's URL, with the user and password from their fields.
func oracleURL(connect, user, password string) (string, error) {
	connect = strings.TrimSpace(connect)
	if strings.HasPrefix(connect, "(") {
		return goora.BuildJDBC(user, password, connect, nil), nil
	}
	connect = strings.TrimPrefix(connect, "//")
	hostPort, service, _ := strings.Cut(connect, "/")
	service, _, _ = strings.Cut(service, ":") // A server type, such as :dedicated, isn't needed.
	host, portText, err := net.SplitHostPort(hostPort)
	if err != nil {
		host, portText = hostPort, "1521"
	}
	port, err := strconv.Atoi(portText)
	if err != nil || host == "" {
		return "", errors.New("the connect string must be host[:port][/service] or a (DESCRIPTION=...)")
	}
	return goora.BuildUrl(host, port, service, user, password, nil), nil
}

func runOracle(ctx context.Context, m monitor.Monitor) monitor.Result {
	ctx, span := databaseSpan(ctx, m, "oracle")
	defer span.End()
	address, err := oracleURL(m.DatabaseConnectionString, strings.TrimSpace(m.DatabaseUsername), strings.TrimSpace(m.DatabasePassword))
	if err != nil {
		return monitor.Result{Msg: "Database connection/query failed: " + err.Error()}
	}
	if strings.TrimSpace(m.DatabaseQuery) == "" {
		m.DatabaseQuery = "SELECT 1 FROM DUAL"
	}
	return runSQL(ctx, m, goora.NewConnector(address), "Query executed successfully", true)
}

// runMongoDB runs a command, ping by default, and passes if it's ok. A JSONata expression can
// then pick a value out of the reply, which must be truthy, or equal the expected value.
func runMongoDB(ctx context.Context, m monitor.Monitor) monitor.Result {
	ctx, span := databaseSpan(ctx, m, "mongodb")
	defer span.End()
	command := bson.D{{Key: "ping", Value: 1}}
	if q := strings.TrimSpace(m.DatabaseQuery); q != "" {
		if err := bson.UnmarshalExtJSON([]byte(q), false, &command); err != nil {
			return monitor.Result{Msg: "invalid command: " + err.Error()}
		}
	}
	start := time.Now()
	client, err := mongo.Connect(options.Client().ApplyURI(m.DatabaseConnectionString).SetServerSelectionTimeout(m.EffectiveTimeout()))
	if err != nil {
		return monitor.Result{Msg: databaseError(ctx, err, m)}
	}
	defer func() { _ = client.Disconnect(context.WithoutCancel(ctx)) }()
	database := "test"
	if u, err := url.Parse(m.DatabaseConnectionString); err == nil && strings.Trim(u.Path, "/") != "" {
		database = strings.Trim(u.Path, "/")
	}
	raw, err := client.Database(database).RunCommand(ctx, command).Raw()
	if err != nil {
		return monitor.Result{Msg: databaseError(ctx, err, m)}
	}
	ping := elapsedMs(start)
	text, err := bson.MarshalExtJSON(raw, false, false)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	var result any
	if err := json.Unmarshal(text, &result); err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	if reply, _ := result.(map[string]any); reply == nil || reply["ok"] != float64(1) {
		return monitor.Result{Msg: "MongoDB command failed"}
	}
	msg := "Command executed successfully"
	if strings.TrimSpace(m.JSONPath) != "" {
		expr, err := jsonata.Compile(m.JSONPath)
		if err != nil {
			return monitor.Result{Msg: err.Error()}
		}
		result, err = expr.Eval(result)
		if err != nil && !errors.Is(err, jsonata.ErrUndefined) {
			return monitor.Result{Msg: err.Error()}
		}
		if err != nil || !jsTruthy(result) {
			return monitor.Result{Msg: "Queried value not found."}
		}
		msg = "Command executed successfully and the jsonata expression produces a result."
	}
	if m.ExpectedValue != "" {
		if jsToString(result) != m.ExpectedValue {
			return monitor.Result{Msg: fmt.Sprintf("Query executed, but value is not equal to expected value, value was: [%s]", jsonText(result))}
		}
		msg = "Command executed successfully and expected value was found"
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: msg}
}

// jsTruthy is JavaScript's truthiness.
func jsTruthy(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case bool:
		return v
	case float64:
		return v != 0 && v == v
	case string:
		return v != ""
	}
	return true
}

// jsToString is JavaScript's String() of a JSON value: arrays are joined with commas, and objects
// are "[object Object]".
func jsToString(v any) string {
	switch v := v.(type) {
	case nil:
		return "null"
	case undefined:
		return "undefined"
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = jsToString(item)
			}
		}
		return strings.Join(parts, ",")
	case map[string]any:
		return "[object Object]"
	}
	return jsString(v)
}
