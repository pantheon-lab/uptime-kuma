package check

import (
	"context"
	"crypto/tls"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/conditions"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// Each check opens its own connection and closes it, as the Node.js backend did, so that a
// check tells whether a new client can connect.

func databaseQuery(m monitor.Monitor) string {
	if strings.TrimSpace(m.DatabaseQuery) == "" {
		return "SELECT 1"
	}
	return m.DatabaseQuery
}

func databaseSpan(ctx context.Context, m monitor.Monitor, system string) (context.Context, trace.Span) {
	host := ""
	if u, err := url.Parse(m.DatabaseConnectionString); err == nil {
		host = u.Hostname()
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("server.address", host), attribute.String("db.system", system))
	return otel.Tracer(tracerName).Start(ctx, "db.query")
}

// postgresConfig reads a connection string as node-postgres did: ssl=true asks for verified TLS,
// and ssl=false for none. Other settings are pgx's, which are libpq's.
func postgresConfig(raw string) (*pgx.ConnConfig, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, errors.New("invalid connection string")
	}
	q := u.Query()
	if ssl := q.Get("ssl"); ssl != "" {
		q.Del("ssl")
		if q.Get("sslmode") == "" {
			if ssl == "true" || ssl == "1" {
				q.Set("sslmode", "verify-full")
			} else {
				q.Set("sslmode", "disable")
			}
		}
	}
	u.RawQuery = q.Encode()
	config, err := pgx.ParseConfig(u.String())
	if err != nil {
		// pgx repeats the connection string, password included, in some errors.
		return nil, errors.New("invalid connection string")
	}
	return config, nil
}

func runPostgres(ctx context.Context, m monitor.Monitor) monitor.Result {
	ctx, span := databaseSpan(ctx, m, "postgresql")
	defer span.End()
	config, err := postgresConfig(m.DatabaseConnectionString)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	// node-postgres refused a password that's there but empty, as in user:@host.
	if u, err := url.Parse(m.DatabaseConnectionString); err == nil && u.User != nil {
		if password, set := u.User.Password(); set && password == "" {
			return monitor.Result{Msg: "Password is undefined."}
		}
	}
	start := time.Now()
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		return monitor.Result{Msg: databaseError(ctx, err, m)}
	}
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	rows, err := conn.Query(ctx, databaseQuery(m), pgx.QueryExecModeSimpleProtocol)
	if err == nil {
		rows.Close()
		err = rows.Err()
	}
	if err != nil {
		return monitor.Result{Msg: databaseError(ctx, err, m)}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping}
}

// mysqlConfig reads a mysql:// URL, as mysql2 did, into the driver's settings. password replaces
// the one in the URL, if it's set.
func mysqlConfig(raw, password string) (*mysql.Config, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return nil, errors.New("invalid connection string")
	}
	config := mysql.NewConfig()
	config.Net = "tcp"
	config.Addr = u.Host
	if u.Port() == "" {
		config.Addr = net.JoinHostPort(u.Hostname(), "3306")
	}
	if u.User != nil {
		config.User = u.User.Username()
		config.Passwd, _ = u.User.Password()
	}
	if password != "" {
		config.Passwd = password
	}
	config.DBName = strings.TrimPrefix(u.Path, "/")
	q := u.Query()
	if ssl := q.Get("ssl"); ssl != "" && ssl != "false" && ssl != "0" {
		config.TLSConfig = "true"
	}
	if q.Get("ssl-mode") != "" || q.Get("tls") != "" {
		config.TLSConfig = q.Get("tls")
		if config.TLSConfig == "" {
			config.TLSConfig = "preferred"
		}
	}
	return config, nil
}

func runMySQL(ctx context.Context, m monitor.Monitor) monitor.Result {
	ctx, span := databaseSpan(ctx, m, "mysql")
	defer span.End()
	config, err := mysqlConfig(m.DatabaseConnectionString, m.DatabasePassword)
	if err != nil {
		return monitor.Result{Msg: "Database connection/query failed: " + err.Error()}
	}
	connector, err := mysql.NewConnector(config)
	if err != nil {
		return monitor.Result{Msg: "Database connection/query failed: " + err.Error()}
	}
	return runSQL(ctx, m, connector, "No Error, but the result is not an array. Type: object", false)
}

// runSQL runs a SQL monitor's query on a new connection, as MySQL, SQL Server and Oracle do in
// the Node.js backend: without conditions, the message counts the rows; with them, the result
// must be a single value that meets them. noRows is the message for a statement without rows.
// exec runs a statement that isn't a query with Exec, to count the rows it changed.
func runSQL(ctx context.Context, m monitor.Monitor, connector driver.Connector, noRows string, exec bool) monitor.Result {
	db := sql.OpenDB(connector)
	defer func() { _ = db.Close() }()
	db.SetMaxOpenConns(1)
	fail := func(err error) monitor.Result {
		return monitor.Result{Msg: "Database connection/query failed: " + databaseError(ctx, err, m)}
	}

	nodes, err := conditions.Parse(m.Conditions)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	query := databaseQuery(m)
	start := time.Now()
	if exec && len(nodes) == 0 && !returnsRows(query) {
		result, err := db.ExecContext(ctx, query)
		if err != nil {
			return fail(err)
		}
		ping := elapsedMs(start)
		msg := "Query executed successfully"
		if affected, err := result.RowsAffected(); err == nil {
			msg = fmt.Sprintf("Rows affected: %d", affected)
		}
		return monitor.Result{OK: true, Ping: &ping, Msg: msg}
	}
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return fail(err)
	}

	if len(nodes) == 0 {
		count := 0
		for rows.Next() {
			count++
		}
		if err := rows.Err(); err != nil {
			return fail(err)
		}
		ping := elapsedMs(start)
		msg := fmt.Sprintf("Rows: %d", count)
		if len(columns) == 0 {
			msg = noRows
		}
		return monitor.Result{OK: true, Ping: &ping, Msg: msg}
	}

	value, err := singleValue(rows, columns)
	if err != nil {
		return fail(err)
	}
	ping := elapsedMs(start)
	passed, err := conditions.Evaluate(nodes, map[string]string{"result": value})
	if err != nil {
		return monitor.Result{Msg: "Database connection/query failed: " + err.Error()}
	}
	if !passed {
		return monitor.Result{Msg: fmt.Sprintf("Query result did not meet the specified conditions (%s)", value)}
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: "Query did meet specified conditions"}
}

// returnsRows guesses whether a statement is a query, by its first word.
func returnsRows(query string) bool {
	word := strings.ToUpper(strings.TrimLeft(strings.Fields(query + " x")[0], "("))
	switch word {
	case "SELECT", "WITH", "SHOW", "DESCRIBE", "DESC", "EXPLAIN", "VALUES":
		return true
	}
	return false
}

// singleValue reads a result that must be one row with one column, as text, as the Node.js
// backend compared it. NULL is "null".
func singleValue(rows *sql.Rows, columns []string) (string, error) {
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", errors.New("Query returned no results") //nolint:staticcheck // As in Node.js.
	}
	if len(columns) > 1 {
		return "", errors.New("Multiple columns were found, expected only one value") //nolint:staticcheck // As in Node.js.
	}
	var value sql.NullString
	if err := rows.Scan(&value); err != nil {
		return "", err
	}
	if rows.Next() {
		return "", errors.New("Multiple values were found, expected only one value") //nolint:staticcheck // As in Node.js.
	}
	if !value.Valid {
		return "null", rows.Err()
	}
	return value.String, rows.Err()
}

func runRedis(ctx context.Context, m monitor.Monitor) monitor.Result {
	ctx, span := databaseSpan(ctx, m, "redis")
	defer span.End()
	options, err := redis.ParseURL(m.DatabaseConnectionString)
	if err != nil {
		return monitor.Result{Msg: "invalid connection string"}
	}
	if options.TLSConfig != nil {
		options.TLSConfig.InsecureSkipVerify = m.IgnoreTLS //nolint:gosec // Per-monitor opt-in.
		options.TLSConfig.MinVersion = tls.VersionTLS12
	}
	options.MaxRetries = -1
	options.PoolSize = 1
	options.DisableIdentity = true
	start := time.Now()
	client := redis.NewClient(options)
	defer func() { _ = client.Close() }()
	reply, err := client.Ping(ctx).Result()
	if err != nil {
		return monitor.Result{Msg: databaseError(ctx, err, m)}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: reply}
}

// databaseError is an error without the connection string's password, which some drivers
// repeat, and a timeout as the other checks say it.
func databaseError(ctx context.Context, err error, m monitor.Monitor) string {
	if errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil {
		return fmt.Sprintf("timeout of %dms exceeded", m.EffectiveTimeout().Milliseconds())
	}
	msg := err.Error()
	if u, perr := url.Parse(m.DatabaseConnectionString); perr == nil && u.User != nil {
		if password, set := u.User.Password(); set && password != "" {
			msg = strings.ReplaceAll(msg, password, "***")
		}
	}
	if m.DatabasePassword != "" {
		msg = strings.ReplaceAll(msg, m.DatabasePassword, "***")
	}
	return msg
}

// runManual reports the status set in the form, as the Node.js backend did. A DOWN status is
// what the user says, so it doesn't wait for retries.
func runManual(m monitor.Monitor) monitor.Result {
	if m.ManualStatus == nil {
		return monitor.Result{Pending: true, Msg: "Manual monitoring - No status set"}
	}
	switch *m.ManualStatus {
	case monitor.StatusUp:
		return monitor.Result{OK: true, Msg: "Up"}
	case monitor.StatusDown:
		return monitor.Result{NoRetry: true, Msg: "Down"}
	}
	return monitor.Result{Pending: true, Msg: "Pending"}
}
