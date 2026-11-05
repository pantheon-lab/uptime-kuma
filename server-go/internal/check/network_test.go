package check

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-smtp"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// udpServer answers each packet with reply(packet), on a loopback port it returns.
func udpServer(t *testing.T, reply func([]byte) []byte) int32 {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, from, err := conn.ReadFrom(buffer)
			if err != nil {
				return
			}
			if answer := reply(buffer[:n]); answer != nil {
				_, _ = conn.WriteTo(answer, from)
			}
		}
	}()
	return int32(conn.LocalAddr().(*net.UDPAddr).Port)
}

func networkMonitor(kind string, port int32) monitor.Monitor {
	m := monitor.New()
	m.Type, m.Hostname, m.Port, m.Timeout = kind, "127.0.0.1", port, 2
	return m
}

func putNTPTime(b []byte, at time.Time) {
	ms := float64(at.UnixMicro())/1000 + ntpEpochOffset
	seconds := uint32(ms / 1000)
	fraction := uint32((ms - float64(seconds)*1000) / 1000 * 0x100000000)
	binary.BigEndian.PutUint32(b, seconds)
	binary.BigEndian.PutUint32(b[4:], fraction)
}

func TestNTP(t *testing.T) {
	answer := func(stratum byte, skew time.Duration, dispersion uint32) int32 {
		return udpServer(t, func(request []byte) []byte {
			if len(request) != 48 || request[0] != 0x1b {
				return nil
			}
			reply := make([]byte, 48)
			reply[0], reply[1] = 0x1c, stratum
			binary.BigEndian.PutUint32(reply[8:], dispersion)
			copy(reply[12:], "GPS\x00")
			putNTPTime(reply[32:], time.Now().Add(skew))
			putNTPTime(reply[40:], time.Now().Add(skew))
			return reply
		})
	}
	r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeNTP, answer(1, 0, 655)))
	if !r.OK || !strings.HasPrefix(r.Msg, "Stratum: 1, RefID: GPS, Offset: ") || r.Ping == nil {
		t.Errorf("synced: %+v", r)
	}
	if r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeNTP, answer(16, 0, 0))); r.Msg != "NTP server is unsynchronized (stratum 16)" {
		t.Errorf("stratum 16: %+v", r)
	}
	if r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeNTP, answer(5, 0, 0))); r.Msg != "Stratum 5 meets or exceeds threshold 5" {
		t.Errorf("stratum 5: %+v", r)
	}
	if r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeNTP, answer(2, 3*time.Second, 0))); !strings.HasPrefix(r.Msg, "Time offset ") || !strings.HasSuffix(r.Msg, "exceeds threshold 1000ms") {
		t.Errorf("offset: %+v", r)
	}
	if r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeNTP, answer(2, 0, 65536))); r.Msg != "Root dispersion 1000.000ms exceeds threshold 500ms" {
		t.Errorf("dispersion: %+v", r)
	}
	silent := udpServer(t, func([]byte) []byte { return nil })
	m := networkMonitor(monitor.TypeNTP, silent)
	m.Timeout = 0.3
	if r := newChecker(t).Run(context.Background(), m); r.Msg != "NTP request timed out" {
		t.Errorf("silent: %+v", r)
	}
}

func TestSIP(t *testing.T) {
	answer := func(status string) int32 {
		return udpServer(t, func(request []byte) []byte {
			if !strings.HasPrefix(string(request), "OPTIONS sip:127.0.0.1:") || !strings.Contains(string(request), "CSeq: 1 OPTIONS") {
				return []byte("SIP/2.0 400 Bad Request\r\n\r\n")
			}
			return []byte(status + "\r\nVia: x\r\nContent-Length: 0\r\n\r\n")
		})
	}
	if r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeSIP, answer("SIP/2.0 200 OK"))); !r.OK || r.Msg != "SIP/2.0 200 OK" {
		t.Errorf("200: %+v", r)
	}
	if r := newChecker(t).Run(context.Background(), networkMonitor(monitor.TypeSIP, answer("SIP/2.0 404 Not Found"))); r.OK || r.Msg != "SIP/2.0 404 Not Found" {
		t.Errorf("404: %+v", r)
	}
}

func TestRADIUS(t *testing.T) {
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := radius.PacketServer{
		SecretSource: radius.StaticSecretSource([]byte("s3cret")),
		Handler: radius.HandlerFunc(func(w radius.ResponseWriter, r *radius.Request) {
			code := radius.CodeAccessReject
			if rfc2865.UserName_GetString(r.Packet) == "kuma" && rfc2865.UserPassword_GetString(r.Packet) == "pw" &&
				rfc2865.CalledStationID_GetString(r.Packet) == "AP-1" {
				code = radius.CodeAccessAccept
			}
			_ = w.Write(r.Response(code))
		}),
	}
	go func() { _ = server.Serve(conn) }()
	t.Cleanup(func() { _ = server.Shutdown(context.Background()) })
	port := int32(conn.LocalAddr().(*net.UDPAddr).Port)
	run := func(password string) monitor.Result {
		m := networkMonitor(monitor.TypeRADIUS, port)
		m.RadiusUsername, m.RadiusPassword, m.RadiusSecret, m.RadiusCalledStationID = "kuma", password, "s3cret", "AP-1"
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run("pw"); !r.OK || r.Msg != "Access-Accept" {
		t.Errorf("accept: %+v", r)
	}
	if r := run("wrong"); r.OK || r.Msg != "RADIUS Access-Reject from 127.0.0.1:"+strconv.Itoa(int(port)) {
		t.Errorf("reject: %+v", r)
	}
}

type smtpBackend struct{}

func (smtpBackend) NewSession(*smtp.Conn) (smtp.Session, error) { return smtpSession{}, nil }

// smtpSession refuses mail, which the check never sends.
type smtpSession struct{}

func (smtpSession) Mail(string, *smtp.MailOptions) error { return errors.New("no mail") }
func (smtpSession) Rcpt(string, *smtp.RcptOptions) error { return errors.New("no mail") }
func (smtpSession) Data(io.Reader) error                 { return errors.New("no mail") }
func (smtpSession) Reset()                               {}
func (smtpSession) Logout() error                        { return nil }

func testCertificate(t *testing.T) *tls.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

func smtpServer(t *testing.T, starttls bool) int32 {
	t.Helper()
	server := smtp.NewServer(smtpBackend{})
	server.Domain = "localhost"
	if starttls {
		server.TLSConfig = testCertificate(t)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return int32(listener.Addr().(*net.TCPAddr).Port)
}

func TestSMTP(t *testing.T) {
	plain, withTLS := smtpServer(t, false), smtpServer(t, true)
	run := func(port int32, security string, ignoreTLS bool) monitor.Result {
		m := networkMonitor(monitor.TypeSMTP, port)
		m.SMTPSecurity, m.IgnoreTLS = security, ignoreTLS
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run(plain, "", false); !r.OK || r.Msg != "SMTP connection verifies successfully" {
		t.Errorf("plain: %+v", r)
	}
	if r := run(plain, "starttls", false); r.OK || r.Msg != "SMTP connection doesn't verify: Error: No STARTTLS support" {
		t.Errorf("required STARTTLS: %+v", r)
	}
	if r := run(withTLS, "", false); r.OK || !strings.Contains(r.Msg, "certificate") {
		t.Errorf("untrusted certificate: %+v", r)
	}
	if r := run(withTLS, "starttls", true); !r.OK {
		t.Errorf("STARTTLS, ignoring the certificate: %+v", r)
	}
	if r := run(withTLS, "nostarttls", false); !r.OK {
		t.Errorf("without STARTTLS: %+v", r)
	}
}

func TestSteam(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("key") != "k3y" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.URL.Query().Get("filter") == `addr\127.0.0.1:27015` {
			_, _ = io.WriteString(w, `{"response":{"servers":[{"name":"My Server"}]}}`)
			return
		}
		_, _ = io.WriteString(w, `{"response":{}}`)
	}))
	defer api.Close()
	original := steamAPI
	steamAPI = api.URL
	defer func() { steamAPI = original }()

	run := func(key string, port int32) monitor.Result {
		checker := newChecker(t)
		checker.SetSteamKey(func(context.Context) (string, error) { return key, nil })
		m := networkMonitor(monitor.TypeSteam, port)
		m.Hostname = "localhost"
		return checker.Run(context.Background(), m)
	}
	if r := run("k3y", 27015); !r.OK || r.Msg != "My Server" {
		t.Errorf("listed: %+v", r)
	}
	if r := run("k3y", 27016); r.OK || r.Msg != "Server not found on Steam" {
		t.Errorf("not listed: %+v", r)
	}
	if r := run("", 27015); r.Msg != "Steam API Key not found" {
		t.Errorf("no key: %+v", r)
	}
	if r := run("bad", 27015); r.OK || r.Msg != "Request failed with status code 403" {
		t.Errorf("bad key: %+v", r)
	}
}

// TEST_SNMP_HOST is an SNMP agent with the community public, such as polinux/snmpd.
func TestSNMP(t *testing.T) {
	host := os.Getenv("TEST_SNMP_HOST")
	if host == "" {
		t.Skip("TEST_SNMP_HOST is not set")
	}
	hostname, port, _ := strings.Cut(host, ":")
	run := func(version, oid, path, op, expected string) monitor.Result {
		m := networkMonitor(monitor.TypeSNMP, int32(atoi(port)))
		m.Hostname, m.SNMPVersion, m.SNMPOID, m.SNMPCommunity = hostname, version, oid, "public"
		m.JSONPath, m.JSONPathOperator, m.ExpectedValue = path, op, expected
		return newChecker(t).Run(context.Background(), m)
	}
	// sysUpTime is a number, and sysDescr text.
	if r := run("2c", "1.3.6.1.2.1.1.3.0", "", ">", "0"); !r.OK {
		t.Errorf("uptime: %+v", r)
	}
	if r := run("1", "1.3.6.1.2.1.1.1.0", "", "contains", "Linux"); !r.OK || !strings.HasPrefix(r.Msg, "JSON query passes") {
		t.Errorf("sysDescr: %+v", r)
	}
	if r := run("2c", "1.3.6.1.2.1.1.99.0", "", "==", "x"); r.OK || !strings.Contains(r.Msg, "no instance exists") {
		t.Errorf("missing OID: %+v", r)
	}
}

// TEST_RADIUS is a RADIUS server as host:port,secret,user,password, such as FreeRADIUS.
func TestRADIUSServer(t *testing.T) {
	setting := os.Getenv("TEST_RADIUS")
	if setting == "" {
		t.Skip("TEST_RADIUS is not set")
	}
	parts := strings.Split(setting, ",")
	hostname, port, _ := strings.Cut(parts[0], ":")
	run := func(password string) monitor.Result {
		m := networkMonitor(monitor.TypeRADIUS, int32(atoi(port)))
		m.Hostname, m.RadiusSecret, m.RadiusUsername, m.RadiusPassword = hostname, parts[1], parts[2], password
		return newChecker(t).Run(context.Background(), m)
	}
	if r := run(parts[3]); !r.OK || r.Msg != "Access-Accept" {
		t.Errorf("accept: %+v", r)
	}
	if r := run("wrong"); r.OK || !strings.HasPrefix(r.Msg, "RADIUS Access-Reject from ") {
		t.Errorf("reject: %+v", r)
	}
}
