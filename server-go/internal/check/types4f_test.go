package check

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// gameServer answers UDP packets with reply, which returns the packets to send back.
func gameServer(t *testing.T, reply func(request []byte) [][]byte) int32 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buffer := make([]byte, 65536)
		for {
			n, from, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			for _, packet := range reply(bytes.Clone(buffer[:n])) {
				_, _ = conn.WriteTo(packet, from)
			}
		}
	}()
	return int32(conn.LocalAddr().(*net.UDPAddr).Port)
}

func gameMonitor(game string, port int32) monitor.Monitor {
	return monitor.Monitor{Type: monitor.TypeGameDig, Interval: 60, Timeout: 5, Hostname: "127.0.0.1", Port: port,
		Settings: monitor.Settings{Game: game, GamedigGivenPortOnly: true}}
}

func TestGameDigProtocols(t *testing.T) {
	prefix := []byte{0xff, 0xff, 0xff, 0xff}
	cases := map[string]struct {
		game  string
		reply func([]byte) [][]byte
		want  string
	}{
		"valve with a challenge": {"teamfortress2", func(r []byte) [][]byte {
			if !bytes.HasPrefix(r, append(bytes.Clone(prefix), 0x54)) {
				return nil
			}
			if !bytes.HasSuffix(r, []byte{1, 2, 3, 4}) {
				return [][]byte{append(append(bytes.Clone(prefix), 0x41), 1, 2, 3, 4)}
			}
			info := append(append(bytes.Clone(prefix), 0x49, 17), "Kuma TF2\x00ctf_2fort\x00tf\x00Team Fortress\x00"...)
			return [][]byte{info}
		}, "Kuma TF2"},
		"gamespy1 in two parts": {"battlefield1942", func(r []byte) [][]byte {
			if !strings.HasPrefix(string(r), `\status\`) {
				return nil
			}
			return [][]byte{[]byte(`\mapname\berlin\queryid\7.2\final\`), []byte("\\hostname\\Caf\xe9 Kuma\\queryid\\7.1")}
		}, "Café Kuma"},
		"gamespy2": {"battlefieldvietnam", func(r []byte) [][]byte {
			return [][]byte{append([]byte{0, 0, 0, 0, 1}, "hostname\x00Kuma BFV\x00mapname\x00x\x00\x00"...)}
		}, "Kuma BFV"},
		"gamespy3 with a challenge and parts": {"battlefield2", func(r []byte) [][]byte {
			if len(r) < 7 || r[0] != 0xfe {
				return nil
			}
			session := r[3:7]
			if r[2] == 9 {
				return [][]byte{append(append([]byte{9}, session...), "123456\x00"...)}
			}
			if binary.BigEndian.Uint32(r[7:11]) != 123456 {
				return nil
			}
			part := func(id byte, body string) []byte {
				p := append(append([]byte{0}, session...), "splitnum\x00"...)
				return append(append(p, id, 0), body...)
			}
			return [][]byte{part(0x81, "\x00\x00player_\x00\x00"), part(0, "hostname\x00Kuma BF2\x00\x00")}
		}, "Kuma BF2"},
		"quake3 with colours": {"q3a", func(r []byte) [][]byte {
			if !bytes.Contains(r, []byte("getstatus")) {
				return nil
			}
			return [][]byte{append(bytes.Clone(prefix), "statusResponse\n\\sv_hostname\\^1Kuma ^7Arena\\mapname\\q3dm17\n"...)}
		}, "Kuma Arena"},
		"quake2": {"quake2", func(r []byte) [][]byte {
			return [][]byte{append(bytes.Clone(prefix), "print\n\\hostname\\Kuma Q2\\maxclients\\8\n"...)}
		}, "Kuma Q2"},
		"ase": {"chaser", func(r []byte) [][]byte {
			if string(r) != "s" {
				return nil
			}
			field := func(s string) []byte { return append([]byte{byte(len(s) + 1)}, s...) }
			body := []byte("EYE1")
			for _, s := range []string{"mta", "22003", "Kuma MTA", "race"} {
				body = append(body, field(s)...)
			}
			return [][]byte{body}
		}, "Kuma MTA"},
	}
	for name, c := range cases {
		if _, found := FindGame(c.game); !found {
			t.Fatalf("%s: no game %s", name, c.game)
		}
		port := gameServer(t, c.reply)
		got := runGameDig(context.Background(), gameMonitor(c.game, port))
		if !got.OK || got.Msg != c.want || got.Ping == nil {
			t.Errorf("%s: %+v", name, got)
		}
	}

	// Nothing answers: each port is tried, and the message says so.
	silent := gameServer(t, func([]byte) [][]byte { return nil })
	m := gameMonitor("teamfortress2", silent)
	m.Timeout = 3
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if got := runGameDig(ctx, m); got.OK || !strings.HasPrefix(got.Msg, "Failed all 1 attempts: port ") {
		t.Errorf("silent: %+v", got)
	}
	if got := runGameDig(context.Background(), gameMonitor("no-such-game", 1)); got.OK {
		t.Errorf("unknown game: %+v", got)
	}
}

func TestGameDigPorts(t *testing.T) {
	game := Game{Port: 2302, PortQueryOffset: []int{1, 15}}
	if got := fmt.Sprint(gamedigPorts(game, 2302, false)); got != "[2303 2317 2302]" {
		t.Errorf("offsets: %s", got)
	}
	if got := fmt.Sprint(gamedigPorts(game, 2302, true)); got != "[2302]" {
		t.Errorf("given port only: %s", got)
	}
	withQuery := Game{Port: 7777, PortQuery: 27015}
	if got := fmt.Sprint(gamedigPorts(withQuery, 7777, false)); got != "[7777 27015]" {
		t.Errorf("query port: %s", got)
	}
	if got := fmt.Sprint(gamedigPorts(withQuery, 0, false)); got != "[27015]" {
		t.Errorf("no port: %s", got)
	}
	if len(Games()) < 250 {
		t.Errorf("only %d games", len(Games()))
	}
}

func TestMinecraftJavaAndBedrock(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = conn.Read(make([]byte, 512))
				status := `{"description":{"text":"Kuma ","extra":[{"text":"Craft"}]},"players":{"max":20,"online":1}}`
				body := appendVarInt(nil, len(status))
				body = append(body, status...)
				_, _ = conn.Write(minecraftPacket(0, body))
			}()
		}
	}()
	port := listener.Addr().(*net.TCPAddr).Port
	state, err := queryMinecraftJava(context.Background(), "127.0.0.1", port)
	if err != nil || state.name != "Kuma Craft" {
		t.Fatalf("java: %+v %v", state, err)
	}

	bedrock := gameServer(t, func(r []byte) [][]byte {
		if len(r) < 25 || r[0] != 0x01 {
			return nil
		}
		motd := "MCPE;Kuma Bedrock;594;1.20.0;0;10;123;world;Survival;1;19132;19133;"
		p := append([]byte{0x1c}, r[1:9]...)
		p = append(p, 0, 0, 0, 0, 0, 0, 0, 1)
		p = append(p, raknetMagic...)
		p = binary.BigEndian.AppendUint16(p, uint16(len(motd)))
		return [][]byte{append(p, motd...)}
	})
	got := runGameDig(context.Background(), gameMonitor("minecraft", bedrock))
	if !got.OK || got.Msg != "Kuma Bedrock" {
		t.Errorf("bedrock through the minecraft game: %+v", got)
	}
}

// TestMinecraftServer queries a real server, if TEST_MINECRAFT is its host:port.
func TestMinecraftServer(t *testing.T) {
	address := os.Getenv("TEST_MINECRAFT")
	if address == "" {
		t.Skip("TEST_MINECRAFT isn't set")
	}
	host, port, _ := net.SplitHostPort(address)
	var n int
	_, _ = fmt.Sscan(port, &n)
	m := gameMonitor("minecraft", int32(n))
	m.Hostname, m.Timeout = host, 10
	if got := runGameDig(context.Background(), m); !got.OK || got.Msg == "" {
		t.Errorf("%+v", got)
	}
}

// tailscaleAPI serves a fake tailscaled local API on a socket.
func tailscaleAPI(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tailscaled.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	saved := TailscaleSocket
	TailscaleSocket = path
	t.Cleanup(func() { TailscaleSocket = saved })
}

func TestTailscalePing(t *testing.T) {
	tailscaleAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Sec-Tailscale") != "localapi" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/localapi/v0/status":
			_, _ = io.WriteString(w, `{"Peer":{"k":{"HostName":"Cider","DNSName":"cider.tail.ts.net.","TailscaleIPs":["100.1.2.3"]}}}`)
		case "/localapi/v0/ping":
			switch r.URL.Query().Get("ip") {
			case "100.1.2.3":
				_, _ = io.WriteString(w, `{"IP":"100.1.2.3","LatencySeconds":0.012}`)
			case "100.9.9.9":
				_, _ = io.WriteString(w, `{"Err":"100.9.9.9 is local Tailscale IP","IsLocalIP":true}`)
			default:
				_, _ = io.WriteString(w, `{"Err":"timeout"}`)
			}
		}
	})
	run := func(host string) monitor.Result {
		return New(nil).Run(context.Background(), monitor.Monitor{Type: monitor.TypeTailscale, Interval: 60, Timeout: 5, Hostname: host})
	}
	for _, host := range []string{"cider", "cider.tail.ts.net", "100.1.2.3"} {
		if got := run(host); !got.OK || got.Msg != "OK" || got.Ping == nil || *got.Ping != 12 {
			t.Errorf("%s: %+v", host, got)
		}
	}
	for host, want := range map[string]string{
		"beer":      "Error checking Tailscale ping: Nonexistent or inaccessible due to ACLs: ",
		"100.9.9.9": "Error checking Tailscale ping: Tailscale only works if used on other machines: ",
		"100.4.4.4": "Error checking Tailscale ping: Ping timed out: timeout",
	} {
		if got := run(host); got.OK || !strings.HasPrefix(got.Msg, want) {
			t.Errorf("%s: %+v", host, got)
		}
	}
	TailscaleSocket = filepath.Join(t.TempDir(), "missing.sock")
	if got := run("cider"); got.OK || !strings.Contains(got.Msg, "can't reach tailscaled") {
		t.Errorf("no tailscaled: %+v", got)
	}
}

// TestTailscaleDaemon pings TEST_TAILSCALE_PEER through this machine's tailscaled.
func TestTailscaleDaemon(t *testing.T) {
	peer := os.Getenv("TEST_TAILSCALE_PEER")
	if peer == "" {
		t.Skip("TEST_TAILSCALE_PEER isn't set")
	}
	got := New(nil).Run(context.Background(), monitor.Monitor{Type: monitor.TypeTailscale, Interval: 60, Timeout: 10, Hostname: peer})
	if !got.OK {
		t.Errorf("%+v", got)
	}
}

func TestGlobalping(t *testing.T) {
	var created []map[string]any
	var polls atomic.Int32
	results := map[string]string{
		"ping": `{"status":"finished","rawOutput":"PING","timings":[{"rtt":10}],"stats":{"avg":10.5}}`,
		"http": `{"status":"finished","rawOutput":"HTTP/1.1 200\n\n{\"status\":\"ok\"}","rawBody":"{\"status\":\"ok\"}","statusCode":200,"statusCodeName":"OK","timings":{"total":120},"tls":{"authorized":false,"error":"self signed"}}`,
		"dns":  `{"status":"finished","statusCodeName":"NOERROR","answers":[{"value":"93.184.215.14"}],"timings":{"total":20}}`,
	}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"type":"too_many_requests","message":"Too many requests"}}`)
			return
		}
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body["target"] == "bad" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":{"type":"validation_error","message":"Parameter validation failed","params":{"target":"\"target\" must be a valid host"}}}`)
				return
			}
			created = append(created, body)
			_, _ = fmt.Fprintf(w, `{"id":"%s","probesCount":1}`, body["type"])
			return
		}
		kind := strings.TrimPrefix(r.URL.Path, "/measurements/")
		if polls.Add(1)%2 == 1 {
			_, _ = io.WriteString(w, `{"status":"in-progress","results":[]}`)
			return
		}
		_, _ = fmt.Fprintf(w, `{"status":"finished","results":[{"probe":{"continent":"EU","country":"DE","city":"Frankfurt","asn":24940,"network":"Hetzner","tags":["datacenter-network","hetzner-fsn1"]},"result":%s}]}`, results[kind])
	}))
	t.Cleanup(api.Close)
	saved := GlobalpingAPI
	GlobalpingAPI = api.URL
	t.Cleanup(func() { GlobalpingAPI = saved })

	checker := New(nil)
	token := "tok"
	checker.SetGlobalpingToken(func(context.Context) (string, error) { return token, nil })
	run := func(m monitor.Monitor) monitor.Result {
		m.Type, m.Interval, m.Timeout = monitor.TypeGlobalping, 60, 10
		return checker.Run(context.Background(), m)
	}
	where := "Frankfurt, DE, EU, Hetzner (AS24940), (hetzner-fsn1) : "

	ping := monitor.New()
	ping.Hostname, ping.Subtype, ping.Location, ping.PingCount, ping.Protocol, ping.Port = "example.com", "ping", "Germany", 3, "TCP", 443
	if got := run(ping); !got.OK || got.Msg != where+"OK" || *got.Ping != 10.5 {
		t.Errorf("ping: %+v", got)
	}
	options := created[0]["measurementOptions"].(map[string]any)
	if created[0]["locations"].([]any)[0].(map[string]any)["magic"] != "Germany" || options["packets"] != float64(3) || options["port"] != float64(443) {
		t.Errorf("ping request: %v", created[0])
	}

	web := monitor.New()
	web.Subtype, web.URL, web.Keyword = "http", "https://example.com/health?x=1", "ok"
	web.AuthMethod, web.BearerToken = "bearer", "secret"
	if got := run(web); !got.OK || got.Msg != where+"200 - OK, keyword is found" {
		t.Errorf("keyword: %+v", got)
	}
	request := created[1]["measurementOptions"].(map[string]any)["request"].(map[string]any)
	if request["path"] != "/health" || request["query"] != "x=1" || request["headers"].(map[string]any)["Authorization"] != "Bearer secret" {
		t.Errorf("http request: %v", request)
	}
	web.Keyword, web.JSONPath, web.JSONPathOperator, web.ExpectedValue = "", "status", "==", "ok"
	if got := run(web); !got.OK || !strings.HasPrefix(got.Msg, where+"JSON query passes") {
		t.Errorf("json query: %+v", got)
	}
	web.JSONPath, web.ExpectedValue = "", ""
	if got := run(web); got.OK || got.Msg != where+"TLS certificate is not authorized: self signed" {
		t.Errorf("tls: %+v", got)
	}

	dns := monitor.New()
	dns.Subtype, dns.Hostname, dns.Port, dns.DNSResolveType, dns.Keyword = "dns", "example.com", 53, "A", `^93\.`
	if got := run(dns); !got.OK || got.Msg != where+"93.184.215.14" {
		t.Errorf("dns: %+v", got)
	}
	dns.Keyword = "^10\\."
	if got := run(dns); got.OK || got.Msg != where+"No record matched. 93.184.215.14" {
		t.Errorf("dns without a match: %+v", got)
	}

	ping.Hostname = "bad"
	if got := run(ping); got.OK || got.Msg != "Failed to create measurement: validation_error Parameter validation failed.\ntarget: \"target\" must be a valid host" {
		t.Errorf("invalid: %+v", got)
	}
	token = ""
	if got := run(dns); got.OK || !strings.Contains(got.Msg, "Get higher limits by creating an account") {
		t.Errorf("out of credits: %+v", got)
	}
}

// TestGlobalpingAPI runs one real ping from the API, if TEST_GLOBALPING is set.
func TestGlobalpingAPI(t *testing.T) {
	if os.Getenv("TEST_GLOBALPING") == "" {
		t.Skip("TEST_GLOBALPING isn't set")
	}
	m := monitor.New()
	m.Type, m.Interval, m.Timeout, m.Subtype, m.Hostname, m.Location, m.PingCount = monitor.TypeGlobalping, 60, 30, "ping", "example.com", "Europe", 2
	got := New(nil).Run(context.Background(), m)
	if !got.OK || !strings.HasSuffix(got.Msg, " : OK") {
		t.Errorf("%+v", got)
	}
	t.Log(got.Msg)
}
