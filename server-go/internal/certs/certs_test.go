package certs

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

var (
	testNow = time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	quiet   = slog.New(slog.NewTextHandler(io.Discard, nil))
)

type issued struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
}

// issue makes a certificate that lasts the given days from testNow, signed by parent, or by itself.
func issue(t *testing.T, cn string, days int, isCA bool, parent *issued, names ...string) *issued {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: cn, Organization: []string{"Test Org"}, Country: []string{"HK"}},
		NotBefore:             testNow.Add(-24 * time.Hour),
		NotAfter:              testNow.Add(time.Duration(days)*24*time.Hour + time.Hour),
		DNSNames:              names,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		BasicConstraintsValid: true,
		IsCA:                  isCA,
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
	}
	signer, signerKey := template, key
	if parent != nil {
		signer, signerKey = parent.cert, parent.key
	}
	der, err := x509.CreateCertificate(rand.Reader, template, signer, &key.PublicKey, signerKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return &issued{cert: cert, key: key}
}

func TestReadChain(t *testing.T) {
	root := issue(t, "Root", 3000, true, nil)
	mid := issue(t, "Intermediate", 1000, true, root)
	leaf := issue(t, "example.com", 30, false, mid, "example.com", "www.example.com")

	info := Read(&tls.ConnectionState{
		PeerCertificates: []*x509.Certificate{leaf.cert, mid.cert},
		VerifiedChains:   [][]*x509.Certificate{{leaf.cert, mid.cert, root.cert}},
	}, "https://example.com/path?token=x", testNow)
	if info == nil || !info.Valid || !info.HostnameMatchMonitorURL {
		t.Fatalf("info: %+v", info)
	}
	c := info.CertInfo
	if c.CertType != "server" || c.DaysRemaining != 30 || c.Subject["CN"] != "example.com" || c.Issuer["CN"] != "Intermediate" ||
		c.Subject["O"] != "Test Org" || c.SubjectAltName != "DNS:example.com, DNS:www.example.com, IP Address:127.0.0.1" {
		t.Fatalf("leaf: %+v", c)
	}
	if len(c.Fingerprint) != 59 || len(c.Fingerprint256) != 95 || strings.ToUpper(c.Fingerprint) != c.Fingerprint {
		t.Fatalf("fingerprints: %s %s", c.Fingerprint, c.Fingerprint256)
	}
	if c.IssuerCertificate.CertType != "intermediate CA" || c.IssuerCertificate.IssuerCertificate.CertType != "root CA" ||
		!c.IssuerCertificate.IssuerCertificate.trustedRoot || c.IssuerCertificate.IssuerCertificate.IssuerCertificate != nil {
		t.Fatalf("chain: %+v", c.IssuerCertificate)
	}

	// The UI reads these names.
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"valid":true`, `"certInfo"`, `"hostnameMatchMonitorUrl":true`, `"validTo":"2026-12-05T13:00:00Z"`, `"daysRemaining":30`, `"fingerprint"`, `"issuerCertificate"`, `"subject":{`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("%s missing from %s", field, data)
		}
	}
	if strings.Contains(string(data), "trustedRoot") {
		t.Error("an internal field leaked")
	}

	if Read(nil, "https://a.b", testNow) != nil || Read(&tls.ConnectionState{}, "https://a.b", testNow) != nil {
		t.Fatal("no certificates should mean no info")
	}
}

func TestReadWithoutVerification(t *testing.T) {
	self := issue(t, "self", 10, false, nil, "self.example")
	info := Read(&tls.ConnectionState{PeerCertificates: []*x509.Certificate{self.cert}}, "https://other.example", testNow)
	if info.Valid || info.HostnameMatchMonitorURL || info.CertInfo.CertType != "self-signed" || info.CertInfo.trustedRoot {
		t.Fatalf("self-signed: %+v %+v", info, info.CertInfo)
	}
}

type fakeStore struct {
	fingerprint string
	saves       int
	cleared     int
	sent        map[int]bool
	settings    map[string]json.RawMessage
}

func (f *fakeStore) TLSFingerprint(context.Context, int32) (string, error) { return f.fingerprint, nil }
func (f *fakeStore) SaveTLSInfo(_ context.Context, _ int32, _ []byte, fingerprint string) error {
	f.saves++
	f.fingerprint = fingerprint
	return nil
}
func (f *fakeStore) ClearCertAlerts(context.Context, int32) error {
	f.cleared++
	f.sent = map[int]bool{}
	return nil
}
func (f *fakeStore) CertAlertSent(_ context.Context, _ int32, days int) (bool, error) {
	for d := range f.sent {
		if d <= days {
			return true, nil
		}
	}
	return false, nil
}
func (f *fakeStore) RecordCertAlert(_ context.Context, _ int32, days int) error {
	f.sent[days] = true
	return nil
}
func (f *fakeStore) Settings(context.Context) (map[string]json.RawMessage, error) {
	return f.settings, nil
}

type fakeSender struct {
	texts []string
	fail  bool
}

func (f *fakeSender) SendText(_ context.Context, _ monitor.Monitor, text string) bool {
	if f.fail {
		return false
	}
	f.texts = append(f.texts, text)
	return true
}

func newService() (*Service, *fakeStore, *fakeSender) {
	st := &fakeStore{sent: map[int]bool{}, settings: map[string]json.RawMessage{}}
	sender := &fakeSender{}
	s := NewService(st, sender, quiet)
	s.now = func() time.Time { return testNow }
	return s, st, sender
}

func state(chain ...*issued) *tls.ConnectionState {
	certs := make([]*x509.Certificate, len(chain))
	for i, c := range chain {
		certs[i] = c.cert
	}
	return &tls.ConnectionState{PeerCertificates: certs, VerifiedChains: [][]*x509.Certificate{certs}}
}

func site() monitor.Monitor {
	m := monitor.New()
	m.ID, m.Name, m.URL, m.ExpiryNotification = 1, "Site", "https://example.com/?token=secret", true
	return m
}

func TestExpiryAlerts(t *testing.T) {
	s, st, sender := newService()
	ctx := context.Background()
	root := issue(t, "Root", 5, true, nil)
	far := issue(t, "example.com", 60, false, root, "example.com")

	if err := s.handle(ctx, observation{site(), state(far, root)}); err != nil {
		t.Fatal(err)
	}
	if st.saves != 1 {
		t.Fatalf("saves: %d", st.saves)
	}
	// The root is close to expiry, but a trusted root isn't the site owner's to replace.
	if len(sender.texts) != 0 {
		t.Fatalf("alerted: %v", sender.texts)
	}
	// Nothing changed, so nothing is written.
	if err := s.handle(ctx, observation{site(), state(far, root)}); err != nil {
		t.Fatal(err)
	}
	if st.saves != 1 {
		t.Fatalf("unchanged certificate written: %d", st.saves)
	}

	// A new certificate, 13 days from expiry, passes the 14 and 21 day deadlines: one alert.
	near := issue(t, "example.com", 13, false, root, "example.com")
	st.settings[NotifyDaysSetting] = json.RawMessage(`[21, 7, 14]`)
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 || !st.sent[14] || st.cleared != 1 {
		t.Fatalf("texts %v, sent %v, cleared %d", sender.texts, st.sent, st.cleared)
	}
	want := "[Site][https://example.com/?***] server certificate example.com will expire in 13 days"
	if sender.texts[0] != want {
		t.Fatalf("text: %q, want %q", sender.texts[0], want)
	}
	if strings.Contains(sender.texts[0], "secret") {
		t.Fatal("the URL's query went to a third party")
	}

	// No repeat for the same deadline, but the next one alerts, even at zero days.
	s.now = func() time.Time { return testNow.Add(2 * time.Hour) }
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 {
		t.Fatalf("repeated: %v", sender.texts)
	}
	s.now = func() time.Time { return testNow.Add(13 * 24 * time.Hour) }
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 2 || !strings.Contains(sender.texts[1], "in 0 days") {
		t.Fatalf("zero days: %v", sender.texts)
	}
}

func TestExpiryAlertConditions(t *testing.T) {
	root := issue(t, "Root", 3000, true, nil)
	near := issue(t, "example.com", 3, false, root, "example.com")
	ctx := context.Background()

	// Off, or with verification ignored: no alert, though the certificate is still recorded.
	for name, change := range map[string]func(*monitor.Monitor){
		"off":       func(m *monitor.Monitor) { m.ExpiryNotification = false },
		"ignoreTLS": func(m *monitor.Monitor) { m.IgnoreTLS = true },
	} {
		s, st, sender := newService()
		m := site()
		change(&m)
		if err := s.handle(ctx, observation{m, state(near, root)}); err != nil {
			t.Fatal(err)
		}
		if len(sender.texts) != 0 || st.saves != 1 {
			t.Errorf("%s: texts %v, saves %d", name, sender.texts, st.saves)
		}
	}

	// Switched on later: no need to wait for the certificate to change.
	s, _, sender := newService()
	m := site()
	m.ExpiryNotification = false
	if err := s.handle(ctx, observation{m, state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 {
		t.Fatalf("switched on: %v", sender.texts)
	}

	// A provider that's down: nothing recorded, so a later check tries again.
	s, st, sender := newService()
	sender.fail = true
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if len(st.sent) != 0 {
		t.Fatalf("recorded a failed alert: %v", st.sent)
	}
	sender.fail = false
	s.now = func() time.Time { return testNow.Add(2 * time.Hour) }
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if len(sender.texts) != 1 {
		t.Fatalf("retry: %v", sender.texts)
	}

	// After a restart, the database says what the previous certificate was.
	s, st, _ = newService()
	st.fingerprint = "old"
	st.sent[7] = true
	if err := s.handle(ctx, observation{site(), state(near, root)}); err != nil {
		t.Fatal(err)
	}
	if st.cleared != 1 {
		t.Fatalf("cleared: %d", st.cleared)
	}
}

func TestOnChangeAndRealConnection(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer server.Close()
	res, err := server.Client().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()

	s, _, _ := newService()
	s.now = time.Now
	var changed []string
	s.OnChange(func(id int32, info string) { changed = append(changed, info) })
	m := site()
	m.URL = server.URL
	if err := s.handle(context.Background(), observation{m, res.TLS}); err != nil {
		t.Fatal(err)
	}
	if len(changed) != 1 || !strings.Contains(changed[0], `"certInfo"`) {
		t.Fatalf("changed: %v", changed)
	}
}

func TestObserveNeverBlocks(t *testing.T) {
	s, _, _ := newService()
	for range queueSize + 10 {
		s.Observe(site(), &tls.ConnectionState{})
	}
}
