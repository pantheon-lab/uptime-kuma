package check

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/miekg/dns"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func TestDNSConditions(t *testing.T) {
	host, port := startResolver(t, "127.0.0.1", dnsHandler(func(w dns.ResponseWriter, r *dns.Msg) {
		reply(w, r, dns.RcodeSuccess, aRecord("example.com.", "1.2.3.4"), aRecord("example.com.", "5.6.7.8"))
	}))
	check := func(conditions string) monitor.Result {
		m := dnsMonitorType(host, port, "example.com", "A")
		m.Conditions = json.RawMessage(conditions)
		return newChecker(t).Run(context.Background(), m)
	}
	// Any record may meet them.
	if r := check(`[{"type":"expression","andOr":"and","variable":"record","operator":"equals","value":"5.6.7.8"}]`); !r.OK {
		t.Errorf("met: %+v", r)
	}
	r := check(`[{"type":"expression","andOr":"and","variable":"record","operator":"starts_with","value":"9."}]`)
	if r.OK || r.Msg != "Records: 1.2.3.4 | 5.6.7.8" {
		t.Errorf("not met: %+v", r)
	}
}

func TestManual(t *testing.T) {
	up, down := monitor.StatusUp, monitor.StatusDown
	for name, c := range map[string]struct {
		status *int16
		check  func(monitor.Result) bool
	}{
		"up":    {&up, func(r monitor.Result) bool { return r.OK && r.Msg == "Up" }},
		"down":  {&down, func(r monitor.Result) bool { return !r.OK && r.NoRetry && r.Msg == "Down" }},
		"unset": {nil, func(r monitor.Result) bool { return r.Pending && r.Msg == "Manual monitoring - No status set" }},
	} {
		if r := runManual(monitor.Monitor{ManualStatus: c.status}); !c.check(r) {
			t.Errorf("%s: %+v", name, r)
		}
	}
}

func TestWebSocket(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			if r.Header.Get("Authorization") != "Bearer t" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"chat"}})
			if err != nil {
				return
			}
			_, _, _ = conn.Read(context.Background())
		case "/policy":
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				return
			}
			_ = conn.Close(websocket.StatusPolicyViolation, "go away")
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	base := "ws" + strings.TrimPrefix(server.URL, "http")
	run := func(path string, headers map[string]string, codes ...string) monitor.Result {
		m := monitor.New()
		m.Type, m.URL, m.Timeout, m.Headers, m.WSSubprotocol = monitor.TypeWebSocket, base+path, 5, headers, "chat, other"
		m.AcceptedStatusCodes = []string{"1000"}
		if len(codes) > 0 {
			m.AcceptedStatusCodes = codes
		}
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run("/ok", map[string]string{"Authorization": "Bearer t"}); !r.OK || r.Msg != "1000 - OK" || r.Ping == nil {
		t.Errorf("open and close: %+v", r)
	}
	if r := run("/ok", nil); r.OK || r.Msg != "Unexpected server response: 401" {
		t.Errorf("refused: %+v", r)
	}
	if r := run("/policy", nil); r.OK || r.Msg != "Policy Violation" {
		t.Errorf("closed by the server: %+v", r)
	}
	if r := run("/policy", nil, "1008"); !r.OK {
		t.Errorf("accepted close code: %+v", r)
	}
	if r := run("/missing", nil); r.OK || r.Msg != "Unexpected server response: 404" {
		t.Errorf("missing: %+v", r)
	}
}

func TestPostgres(t *testing.T) {
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	run := func(conn, query string) monitor.Result {
		m := monitor.New()
		m.Type, m.DatabaseConnectionString, m.DatabaseQuery, m.Timeout = monitor.TypePostgres, conn, query, 5
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run(admin, ""); !r.OK || r.Ping == nil {
		t.Errorf("SELECT 1: %+v", r)
	}
	if r := run(admin, "SELECT * FROM no_such_table"); r.OK || !strings.Contains(r.Msg, "no_such_table") {
		t.Errorf("bad query: %+v", r)
	}
	u, _ := url.Parse(admin)
	u.User = url.UserPassword(u.User.Username(), "wrong-password-123")
	if r := run(u.String(), ""); r.OK || strings.Contains(r.Msg, "wrong-password-123") {
		t.Errorf("wrong password: %+v", r)
	}
	u.User = url.UserPassword(u.User.Username(), "")
	if r := run(u.String(), ""); r.Msg != "Password is undefined." {
		t.Errorf("empty password: %+v", r)
	}
}

func TestMySQL(t *testing.T) {
	dsn := os.Getenv("TEST_MARIADB_DSN")
	if dsn == "" {
		t.Skip("TEST_MARIADB_DSN is not set")
	}
	// The test DSN is the driver's form, user:password@tcp(host:port)/; the monitor takes a URL.
	userinfo, rest, _ := strings.Cut(dsn, "@tcp(")
	addr, _, _ := strings.Cut(rest, ")")
	user, password, _ := strings.Cut(userinfo, ":")
	run := func(conn, override, query, conditions string) monitor.Result {
		m := monitor.New()
		m.Type, m.DatabaseConnectionString, m.DatabasePassword, m.DatabaseQuery, m.Timeout = monitor.TypeMySQL, conn, override, query, 5
		if conditions != "" {
			m.Conditions = json.RawMessage(conditions)
		}
		return newChecker(t).Run(context.Background(), m)
	}
	conn := fmt.Sprintf("mysql://%s:%s@%s/mysql", user, url.PathEscape(password), addr)
	if r := run(conn, "", "SELECT 1 UNION SELECT 2", ""); !r.OK || r.Msg != "Rows: 2" {
		t.Errorf("rows: %+v", r)
	}
	if r := run(fmt.Sprintf("mysql://%s@%s/mysql", user, addr), password, "", ""); !r.OK || r.Msg != "Rows: 1" {
		t.Errorf("password from its own field: %+v", r)
	}
	met := `[{"type":"expression","andOr":"and","variable":"result","operator":"equals","value":"42"}]`
	if r := run(conn, "", "SELECT 42", met); !r.OK || r.Msg != "Query did meet specified conditions" {
		t.Errorf("conditions met: %+v", r)
	}
	if r := run(conn, "", "SELECT 41", met); r.OK || r.Msg != "Query result did not meet the specified conditions (41)" {
		t.Errorf("conditions not met: %+v", r)
	}
	if r := run(conn, "", "SELECT 1, 2", met); r.OK || !strings.Contains(r.Msg, "Multiple columns") {
		t.Errorf("two columns: %+v", r)
	}
	if r := run(conn, "", "SELECT NULL", strings.Replace(met, "42", "null", 1)); !r.OK {
		t.Errorf("null: %+v", r)
	}
	if r := run(fmt.Sprintf("mysql://%s:wrong-secret-99@%s/mysql", user, addr), "", "", ""); r.OK ||
		!strings.HasPrefix(r.Msg, "Database connection/query failed: ") || strings.Contains(r.Msg, "wrong-secret-99") {
		t.Errorf("wrong password: %+v", r)
	}
}

func TestRedis(t *testing.T) {
	addr := os.Getenv("TEST_REDIS_URL")
	if addr == "" {
		t.Skip("TEST_REDIS_URL is not set")
	}
	run := func(conn string) monitor.Result {
		m := monitor.New()
		m.Type, m.DatabaseConnectionString, m.Timeout = monitor.TypeRedis, conn, 5
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run(addr); !r.OK || r.Msg != "PONG" {
		t.Errorf("ping: %+v", r)
	}
	u, _ := url.Parse(addr)
	u.User = url.UserPassword("default", "wrong-redis-pw")
	if r := run(u.String()); r.OK || strings.Contains(r.Msg, "wrong-redis-pw") {
		t.Errorf("wrong password: %+v", r)
	}
}
