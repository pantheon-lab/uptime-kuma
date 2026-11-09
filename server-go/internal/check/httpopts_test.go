package check

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/binary"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

func optsMonitor(url string) monitor.Monitor {
	m := monitor.New()
	m.ID, m.Name, m.Type, m.URL, m.Timeout = 1, "x", monitor.TypeHTTP, url, 5
	return m
}

type fakeProxies map[int32]Proxy

func (f fakeProxies) Proxy(_ context.Context, id int32) (Proxy, error) {
	if p, found := f[id]; found {
		return p, nil
	}
	return Proxy{}, errors.New("not found")
}

func TestHTTPLoginsAndOptions(t *testing.T) {
	var lastQuery, lastAuth atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		lastQuery.Store(r.URL.RawQuery)
		lastAuth.Store(r.Header.Get("Authorization"))
		if r.URL.Path == "/fail" {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = io.WriteString(w, strings.Repeat("e", 40))
			return
		}
		_, _ = io.WriteString(w, `{"status":"degraded"}`)
	}))
	t.Cleanup(server.Close)
	checker := New(nil)

	m := optsMonitor(server.URL + "/?a=1")
	m.CacheBust = true
	if got := checker.Run(context.Background(), m); !got.OK {
		t.Fatalf("cache bust: %+v", got)
	}
	if q := lastQuery.Load().(string); !strings.HasPrefix(q, "a=1&uptime_kuma_cachebuster=") || len(q) < 30 {
		t.Errorf("query: %s", q)
	}

	m = optsMonitor(server.URL)
	m.AuthMethod, m.BasicAuthUser, m.BasicAuthPass = "basic", "u", "p:w"
	checker.Run(context.Background(), m)
	if got := lastAuth.Load(); got != "Basic "+base64.StdEncoding.EncodeToString([]byte("u:p:w")) {
		t.Errorf("basic: %v", got)
	}
	m.AuthMethod, m.BearerToken = "bearer", "tok"
	checker.Run(context.Background(), m)
	if got := lastAuth.Load(); got != "Bearer tok" {
		t.Errorf("bearer: %v", got)
	}
	// The monitor's own header wins, as in Node.js.
	m.Headers = map[string]string{"Authorization": "Custom x"}
	checker.Run(context.Background(), m)
	if got := lastAuth.Load(); got != "Custom x" {
		t.Errorf("header: %v", got)
	}

	m = optsMonitor(server.URL + "/fail")
	m.SaveErrorResponse, m.ResponseMaxLength = true, 10
	if got := checker.Run(context.Background(), m); got.OK || got.Response == nil || *got.Response != "eeeeeeeeee... (truncated)" {
		t.Errorf("error response: %+v", got)
	}
	m = optsMonitor(server.URL)
	m.SaveResponse = true
	if got := checker.Run(context.Background(), m); got.Response != nil {
		t.Errorf("a success response was saved without error responses: %+v", got)
	}
	m.SaveErrorResponse = true
	if got := checker.Run(context.Background(), m); got.Response == nil || *got.Response != `{"status":"degraded"}` {
		t.Errorf("success response: %+v", got)
	}

	m = optsMonitor(server.URL)
	m.Type, m.JSONPath, m.JSONPathOperator, m.ExpectedValue = monitor.TypeJSONQuery, "status", "==", "ok"
	if got := checker.Run(context.Background(), m); got.OK || got.NoRetry {
		t.Errorf("json query: %+v", got)
	}
	m.RetryOnlyOnStatusCodeFailure = true
	if got := checker.Run(context.Background(), m); got.OK || !got.NoRetry {
		t.Errorf("json query without retries: %+v", got)
	}

	m = optsMonitor(server.URL)
	m.IPFamily = "ipv6"
	if got := checker.Run(context.Background(), m); got.OK {
		t.Errorf("an IPv4 server answered over IPv6: %+v", got)
	}
	m.IPFamily = "ipv4"
	if got := checker.Run(context.Background(), m); !got.OK {
		t.Errorf("ipv4: %+v", got)
	}
}

func TestMTLS(t *testing.T) {
	clientCert, clientKey := selfSigned(t, "client")
	pool := x509.NewCertPool()
	pool.AppendCertsFromPEM([]byte(clientCert))
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "hello "+r.TLS.PeerCertificates[0].Subject.CommonName)
	}))
	server.TLS = &tls.Config{ClientAuth: tls.RequireAndVerifyClientCert, ClientCAs: pool, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	serverCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))

	checker := New(nil)
	m := optsMonitor(server.URL)
	m.Type, m.Keyword = monitor.TypeKeyword, "hello client"
	m.AuthMethod, m.TLSCert, m.TLSKey, m.TLSCA = "mtls", clientCert, clientKey, serverCA
	if got := checker.Run(context.Background(), m); !got.OK {
		t.Fatalf("mtls: %+v", got)
	}
	m.AuthMethod = ""
	m.IgnoreTLS = true
	if got := checker.Run(context.Background(), m); got.OK {
		t.Errorf("no client certificate: %+v", got)
	}
}

func TestOAuthClientCredentials(t *testing.T) {
	var issued, revoked atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			_ = r.ParseForm()
			if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_id") != "c" ||
				r.Form.Get("client_secret") != "s" || r.Form.Get("audience") != "api" || r.Form.Get("scope") != "read write" {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = io.WriteString(w, `{"error":"invalid_client"}`)
				return
			}
			n := issued.Add(1)
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"access_token":"t`+strconv.Itoa(int(n))+`","token_type":"Bearer","expires_in":3600}`)
		default:
			if r.Header.Get("Authorization") != "Bearer t"+strconv.Itoa(int(issued.Load())) || revoked.Load() == issued.Load() {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, "ok")
		}
	}))
	t.Cleanup(server.Close)
	checker := New(nil)
	m := optsMonitor(server.URL + "/api")
	m.AuthMethod, m.OAuthAuthMethod, m.OAuthTokenURL = "oauth2-cc", "client_secret_post", server.URL+"/token"
	m.OAuthClientID, m.OAuthClientSecret, m.OAuthScopes, m.OAuthAudience = "c", "s", "read write", "api"
	for range 2 {
		if got := checker.Run(context.Background(), m); !got.OK {
			t.Fatalf("oauth: %+v", got)
		}
	}
	if issued.Load() != 1 {
		t.Errorf("the token wasn't reused: %d issued", issued.Load())
	}
	// A revoked token is replaced once.
	revoked.Store(1)
	if got := checker.Run(context.Background(), m); !got.OK || issued.Load() != 2 {
		t.Errorf("after revoking: %+v, %d issued", got, issued.Load())
	}
	m.OAuthClientSecret = "wrong"
	if got := checker.Run(context.Background(), m); got.OK || !strings.HasPrefix(got.Msg, "The oauth config is invalid. ") {
		t.Errorf("wrong secret: %+v", got)
	}
}

func TestNTLMStartsTheHandshake(t *testing.T) {
	var negotiated atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if strings.HasPrefix(auth, "NTLM ") {
			message, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(auth, "NTLM "))
			if err == nil && strings.HasPrefix(string(message), "NTLMSSP\x00") {
				negotiated.Store(true)
				_, _ = io.WriteString(w, "ok")
				return
			}
		}
		w.Header().Set("WWW-Authenticate", "NTLM")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	m := optsMonitor(server.URL)
	m.AuthMethod, m.BasicAuthUser, m.BasicAuthPass, m.AuthDomain = "ntlm", "u", "p", "CORP"
	if got := New(nil).Run(context.Background(), m); !got.OK || !negotiated.Load() {
		t.Errorf("ntlm: %+v", got)
	}
}

func TestProxies(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "target")
	}))
	t.Cleanup(target.Close)
	var viaHTTP atomic.Int32
	httpProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Proxy-Authorization") != "Basic "+base64.StdEncoding.EncodeToString([]byte("u:p")) {
			w.WriteHeader(http.StatusProxyAuthRequired)
			return
		}
		viaHTTP.Add(1)
		res, err := http.Get(r.URL.String())
		if err != nil {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		defer res.Body.Close()
		_, _ = io.Copy(w, res.Body)
	}))
	t.Cleanup(httpProxy.Close)
	var viaSOCKS5, viaSOCKS4 atomic.Int32
	socks5 := socksServer(t, &viaSOCKS5, false)
	socks4 := socksServer(t, &viaSOCKS4, true)
	proxyPort := func(s string) int32 {
		_, port, _ := net.SplitHostPort(strings.TrimPrefix(s, "http://"))
		n, _ := strconv.Atoi(port)
		return int32(n)
	}

	checker := New(nil)
	checker.SetProxies(fakeProxies{
		1: {ID: 1, Protocol: "http", Host: "127.0.0.1", Port: proxyPort(httpProxy.URL), Auth: true, Username: "u", Password: "p", Active: true},
		2: {ID: 2, Protocol: "socks5h", Host: "127.0.0.1", Port: proxyPort(socks5), Auth: true, Username: "u", Password: "p", Active: true},
		3: {ID: 3, Protocol: "socks4", Host: "127.0.0.1", Port: proxyPort(socks4), Active: true},
		4: {ID: 4, Protocol: "http", Host: "127.0.0.1", Port: 1, Active: false},
		5: {ID: 5, Protocol: "http", Host: "127.0.0.1", Port: proxyPort(httpProxy.URL), Active: true},
	})
	for id, counter := range map[int32]*atomic.Int32{1: &viaHTTP, 2: &viaSOCKS5, 3: &viaSOCKS4} {
		m := optsMonitor(target.URL)
		m.ProxyID = id
		before := counter.Load()
		if got := checker.Run(context.Background(), m); !got.OK || counter.Load() != before+1 {
			t.Errorf("proxy %d: %+v", id, got)
		}
	}
	m := optsMonitor(target.URL)
	m.ProxyID = 4
	if got := checker.Run(context.Background(), m); !got.OK {
		t.Errorf("a proxy that's off was used: %+v", got)
	}
	m.ProxyID = 5
	if got := checker.Run(context.Background(), m); got.OK || got.Msg != "Request failed with status code 407" {
		t.Errorf("a proxy without its login: %+v", got)
	}
	m.ProxyID = 9
	if got := checker.Run(context.Background(), m); got.OK {
		t.Errorf("a missing proxy: %+v", got)
	}
}

// socksServer is a SOCKS5 server with the login u:p, or a SOCKS4 server, that counts connections.
func socksServer(t *testing.T, count *atomic.Int32, v4 bool) string {
	t.Helper()
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
				var target string
				if v4 {
					header := make([]byte, 8)
					if _, err := io.ReadFull(conn, header); err != nil || header[0] != 4 {
						return
					}
					user := make([]byte, 1)
					for user[0] = 1; user[0] != 0; {
						if _, err := conn.Read(user); err != nil {
							return
						}
					}
					target = net.JoinHostPort(net.IP(header[4:8]).String(), strconv.Itoa(int(binary.BigEndian.Uint16(header[2:4]))))
				} else {
					greeting := make([]byte, 2)
					if _, err := io.ReadFull(conn, greeting); err != nil {
						return
					}
					_, _ = io.ReadFull(conn, make([]byte, greeting[1]))
					_, _ = conn.Write([]byte{5, 2})
					// RFC 1929: version, user, password.
					head := make([]byte, 2)
					_, _ = io.ReadFull(conn, head)
					user := make([]byte, head[1])
					_, _ = io.ReadFull(conn, user)
					length := make([]byte, 1)
					_, _ = io.ReadFull(conn, length)
					password := make([]byte, length[0])
					_, _ = io.ReadFull(conn, password)
					if string(user) != "u" || string(password) != "p" {
						_, _ = conn.Write([]byte{1, 1})
						return
					}
					_, _ = conn.Write([]byte{1, 0})
					request := make([]byte, 4)
					if _, err := io.ReadFull(conn, request); err != nil {
						return
					}
					var host string
					switch request[3] {
					case 1:
						ip := make([]byte, 4)
						_, _ = io.ReadFull(conn, ip)
						host = net.IP(ip).String()
					case 3:
						_, _ = io.ReadFull(conn, length)
						name := make([]byte, length[0])
						_, _ = io.ReadFull(conn, name)
						host = string(name)
					default:
						return
					}
					port := make([]byte, 2)
					_, _ = io.ReadFull(conn, port)
					target = net.JoinHostPort(host, strconv.Itoa(int(binary.BigEndian.Uint16(port))))
				}
				upstream, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer upstream.Close()
				count.Add(1)
				if v4 {
					_, _ = conn.Write([]byte{0, 0x5a, 0, 0, 0, 0, 0, 0})
				} else {
					_, _ = conn.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
				}
				go func() { _, _ = io.Copy(upstream, conn) }()
				_, _ = io.Copy(conn, upstream)
			}()
		}
	}()
	return "http://" + listener.Addr().String()
}

// selfSigned makes a client certificate and its key, as PEM.
func selfSigned(t *testing.T, name string) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
}

func TestTrialTransportsAreNotKept(t *testing.T) {
	testCert, testKey := selfSigned(t, "client")
	withSettings := func(settings monitor.Settings) monitor.Monitor {
		return monitor.Monitor{Type: "http", Settings: settings}
	}
	checker := New(nil)
	ctx := context.Background()
	kept := func() int {
		checker.extra.mu.Lock()
		defer checker.extra.mu.Unlock()
		return len(checker.extra.byID)
	}
	// Settings that need a transport of their own: a trial's isn't kept, and holds no connections.
	for i := range 20 {
		m := withSettings(monitor.Settings{IPFamily: "ipv4", AuthMethod: "mtls", TLSCert: testCert, TLSKey: testKey, TLSCA: strings.Repeat(" ", i)})
		tr, err := checker.transportFor(DryRun(ctx), m)
		if err != nil {
			t.Fatal(err)
		}
		if !tr.DisableKeepAlives {
			t.Fatal("a trial's transport keeps connections")
		}
	}
	if n := kept(); n != 0 {
		t.Fatalf("trial transports kept: %d", n)
	}
	// A saved monitor's is kept and reused.
	m := withSettings(monitor.Settings{IPFamily: "ipv4"})
	first, _ := checker.transportFor(ctx, m)
	second, _ := checker.transportFor(ctx, m)
	if first != second || kept() != 1 || first.DisableKeepAlives {
		t.Fatalf("saved monitor's transport: kept %d, same %v", kept(), first == second)
	}
	// Settings that keep changing don't grow the cache past its bound.
	for i := range maxTransports + 10 {
		m := withSettings(monitor.Settings{IPFamily: "ipv4", AuthMethod: "mtls", TLSCert: testCert, TLSKey: testKey, TLSCA: strings.Repeat(" ", i)})
		if _, err := checker.transportFor(ctx, m); err != nil {
			t.Fatal(err)
		}
	}
	if n := kept(); n > maxTransports {
		t.Fatalf("transports kept: %d", n)
	}
}
