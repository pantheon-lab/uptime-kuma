package notify

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"mime/quotedprintable"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type delivered struct {
	from string
	to   []string
	data string
	user string
	pass string
}

type mailServer struct {
	mu       sync.Mutex
	messages []delivered
	port     int
}

func (m *mailServer) NewSession(*smtp.Conn) (smtp.Session, error) {
	return &mailSession{server: m}, nil
}

type mailSession struct {
	server *mailServer
	cur    delivered
}

func (s *mailSession) AuthMechanisms() []string { return []string{sasl.Plain} }
func (s *mailSession) Auth(string) (sasl.Server, error) {
	return sasl.NewPlainServer(func(_, username, password string) error {
		if password != "right" {
			return errors.New("bad password")
		}
		s.cur.user, s.cur.pass = username, password
		return nil
	}), nil
}
func (s *mailSession) Mail(from string, _ *smtp.MailOptions) error { s.cur.from = from; return nil }
func (s *mailSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	s.cur.to = append(s.cur.to, to)
	return nil
}
func (s *mailSession) Data(r io.Reader) error {
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.cur.data = string(data)
	s.server.mu.Lock()
	s.server.messages = append(s.server.messages, s.cur)
	s.server.mu.Unlock()
	return nil
}
func (s *mailSession) Reset()        { s.cur = delivered{user: s.cur.user, pass: s.cur.pass} }
func (s *mailSession) Logout() error { return nil }

func selfSigned(t *testing.T) *tls.Config {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "127.0.0.1"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
}

// startMail runs a real SMTP server. mode is "starttls", "implicit" or "plain".
func startMail(t *testing.T, mode string) *mailServer {
	t.Helper()
	backend := &mailServer{}
	server := smtp.NewServer(backend)
	server.Domain = "localhost"
	server.ReadTimeout, server.WriteTimeout = 5*time.Second, 5*time.Second
	var listener net.Listener
	var err error
	switch mode {
	case "implicit":
		server.TLSConfig = selfSigned(t)
		listener, err = tls.Listen("tcp", "127.0.0.1:0", server.TLSConfig)
	case "starttls":
		server.TLSConfig = selfSigned(t)
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	default:
		server.AllowInsecureAuth = true
		listener, err = net.Listen("tcp", "127.0.0.1:0")
	}
	if err != nil {
		t.Fatal(err)
	}
	backend.port = listener.Addr().(*net.TCPAddr).Port
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return backend
}

func (m *mailServer) config(extra M) M {
	config := M{
		"smtpHost": "127.0.0.1", "smtpPort": m.port, "smtpFrom": "Kuma <kuma@example.com>", "smtpTo": "ops@example.com",
		"smtpIgnoreTLSError": true,
	}
	for k, v := range extra {
		config[k] = v
	}
	return config
}

func sendMail(config M, message Message) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return Send(ctx, DefaultEnv(""), "smtp", config, message)
}

func decoded(t *testing.T, data string) string {
	t.Helper()
	_, body, _ := strings.Cut(strings.ReplaceAll(data, "\r\n", "\n"), "\n\n")
	if !strings.Contains(data, "quoted-printable") {
		return body
	}
	text, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(body)))
	if err != nil {
		t.Fatal(err)
	}
	return string(text)
}

func TestSMTPStartTLSWithAuth(t *testing.T) {
	server := startMail(t, "starttls")
	config := server.config(M{"smtpUsername": "me", "smtpPassword": "right"})
	if err := sendMail(config, beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	if len(server.messages) != 1 {
		t.Fatalf("delivered %d", len(server.messages))
	}
	got := server.messages[0]
	if got.user != "me" || got.from != "kuma@example.com" || len(got.to) != 1 || got.to[0] != "ops@example.com" {
		t.Errorf("%+v", got)
	}
	if !strings.Contains(decoded(t, got.data), "[Site] msg") {
		t.Errorf("body: %s", got.data)
	}
}

func TestSMTPImplicitTLS(t *testing.T) {
	server := startMail(t, "implicit")
	if err := sendMail(server.config(M{"smtpSecure": true, "smtpUsername": "me", "smtpPassword": "right"}), beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	if len(server.messages) != 1 {
		t.Fatalf("delivered %d", len(server.messages))
	}
}

func TestSMTPWrongPasswordFails(t *testing.T) {
	server := startMail(t, "starttls")
	err := sendMail(server.config(M{"smtpUsername": "me", "smtpPassword": "wrong"}), beatMessage(statusDown))
	if err == nil || len(server.messages) != 0 {
		t.Fatalf("err %v, delivered %d", err, len(server.messages))
	}
	if strings.Contains(err.Error(), "wrong") {
		t.Errorf("the password is in the error: %v", err)
	}
}

func TestSMTPRefusesPasswordWithoutTLS(t *testing.T) {
	server := startMail(t, "plain")
	err := sendMail(server.config(M{"smtpUsername": "me", "smtpPassword": "right"}), beatMessage(statusDown))
	if err == nil || len(server.messages) != 0 {
		t.Fatalf("err %v, delivered %d", err, len(server.messages))
	}
	// Unless the configuration explicitly asks for it.
	if err := sendMail(server.config(M{"smtpUsername": "me", "smtpPassword": "right", "smtpIgnoreSTARTTLS": true}), beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	if len(server.messages) != 1 {
		t.Fatalf("delivered %d", len(server.messages))
	}
}

func TestSMTPUntrustedCertificateFails(t *testing.T) {
	server := startMail(t, "starttls")
	config := server.config(M{"smtpIgnoreTLSError": false})
	if err := sendMail(config, beatMessage(statusDown)); err == nil || len(server.messages) != 0 {
		t.Fatalf("err %v, delivered %d", err, len(server.messages))
	}
}

func TestSMTPBccStaysOutOfHeaders(t *testing.T) {
	server := startMail(t, "plain")
	config := server.config(M{"smtpCC": "cc@example.com", "smtpBCC": "hidden@example.com", "smtpIgnoreSTARTTLS": true})
	if err := sendMail(config, beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	got := server.messages[0]
	if len(got.to) != 3 {
		t.Errorf("recipients: %v", got.to)
	}
	if strings.Contains(got.data, "hidden@example.com") {
		t.Errorf("BCC is visible: %s", got.data)
	}
	if !strings.Contains(got.data, "cc@example.com") {
		t.Errorf("CC is missing: %s", got.data)
	}
}

func TestSMTPCustomTemplatesAndHeaderInjection(t *testing.T) {
	server := startMail(t, "plain")
	config := server.config(M{"smtpIgnoreSTARTTLS": true, "customSubject": "ALERT {{NAME}}", "customBody": "<b>{{STATUS}}</b>", "htmlBody": true})
	if err := sendMail(config, beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	data := server.messages[0].data
	if !strings.Contains(data, "ALERT My Site") {
		t.Errorf("subject: %s", data)
	}
	if !strings.Contains(data, "text/html") {
		t.Errorf("type: %s", data)
	}

	// A monitor name is user data, and mustn't be able to add headers.
	message := beatMessage(statusDown)
	message.Monitor["name"] = "x\r\nBcc: evil@example.com"
	config = server.config(M{"smtpIgnoreSTARTTLS": true, "customSubject": "{{NAME}}"})
	if err := sendMail(config, message); err == nil {
		for _, m := range server.messages[1:] {
			for _, to := range m.to {
				if strings.Contains(to, "evil") {
					t.Fatalf("injected a recipient: %v", m.to)
				}
			}
			for _, line := range strings.Split(m.data, "\r\n") {
				if strings.HasPrefix(line, "Bcc:") {
					t.Fatalf("injected a header: %s", m.data)
				}
			}
		}
	}

	if err := sendMail(server.config(M{"smtpIgnoreSTARTTLS": true, "smtpAdditionalHeaders": `{"X-A":"1\r\nBcc: evil@example.com"}`}), beatMessage(statusDown)); err == nil {
		t.Error("line breaks in additional headers must be refused")
	}
}

func TestSMTPDKIMSignatureVerifies(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	private := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	public, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	server := startMail(t, "plain")
	config := server.config(M{"smtpIgnoreSTARTTLS": true, "smtpDkimDomain": "example.com", "smtpDkimKeySelector": "sel", "smtpDkimPrivateKey": string(private)})
	if err := sendMail(config, beatMessage(statusDown)); err != nil {
		t.Fatal(err)
	}
	verifications, err := dkim.VerifyWithOptions(bytes.NewReader([]byte(server.messages[0].data)), &dkim.VerifyOptions{
		LookupTXT: func(string) ([]string, error) {
			return []string{"v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(public)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(verifications) != 1 || verifications[0].Err != nil || verifications[0].Domain != "example.com" {
		t.Fatalf("verifications: %+v", verifications)
	}
}

func TestSMTPRefusesPort(t *testing.T) {
	if err := sendMail(M{"smtpHost": "127.0.0.1", "smtpPort": strconv.Itoa(1), "smtpFrom": "a@example.com", "smtpTo": "b@example.com"}, beatMessage(statusDown)); err == nil {
		t.Fatal("expected a connection error")
	}
}
