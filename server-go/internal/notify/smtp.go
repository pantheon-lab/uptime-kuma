package notify

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/dkim"
)

// plainAuth sends credentials on any connection. net/smtp refuses to without TLS, but the
// configuration can explicitly ask for an unencrypted connection, as it could in the original.
type plainAuth struct{ username, password string }

func (a plainAuth) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "PLAIN", []byte("\x00" + a.username + "\x00" + a.password), nil
}

func (a plainAuth) Next([]byte, bool) ([]byte, error) { return nil, nil }

// loginAuth is for servers that only offer AUTH LOGIN, such as Office 365.
type loginAuth struct{ username, password string }

func (a loginAuth) Start(*smtp.ServerInfo) (string, []byte, error) { return "LOGIN", nil, nil }

func (a loginAuth) Next(prompt []byte, more bool) ([]byte, error) {
	if !more {
		return nil, nil
	}
	switch strings.ToLower(strings.TrimSpace(string(prompt))) {
	case "username:":
		return []byte(a.username), nil
	case "password:":
		return []byte(a.password), nil
	}
	return nil, errors.New("unexpected SMTP login prompt")
}

func addressList(value string) ([]*mail.Address, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	return mail.ParseAddressList(value)
}

func sendSMTP(c *call) error {
	headers := map[string]string{}
	if raw := c.s("smtpAdditionalHeaders"); raw != "" {
		var extra map[string]any
		if json.Unmarshal([]byte(raw), &extra) != nil {
			return errors.New("additional headers aren't valid JSON")
		}
		for name, value := range extra {
			if strings.ContainsAny(name, "\r\n:") || strings.ContainsAny(js(value), "\r\n") {
				return errors.New("additional headers can't contain line breaks")
			}
			headers[name] = js(value)
		}
	}
	subject, body, html, err := mailContent(c)
	if err != nil {
		return err
	}

	from, err := mail.ParseAddress(c.s("smtpFrom"))
	if err != nil {
		return errors.New("invalid sender address")
	}
	to, err := addressList(c.s("smtpTo"))
	if err != nil {
		return errors.New("invalid To address")
	}
	cc, err := addressList(c.s("smtpCC"))
	if err != nil {
		return errors.New("invalid CC address")
	}
	bcc, err := addressList(c.s("smtpBCC"))
	if err != nil {
		return errors.New("invalid BCC address")
	}
	if len(to)+len(cc)+len(bcc) == 0 {
		return errors.New("at least one recipient is required")
	}

	message, err := buildMail(c.env.Now(), from, to, cc, subject, body, html, headers)
	if err != nil {
		return err
	}
	if c.t("smtpDkimDomain") {
		if message, err = signDKIM(c, message); err != nil {
			return err
		}
	}

	recipients := make([]string, 0, len(to)+len(cc)+len(bcc))
	for _, list := range [][]*mail.Address{to, cc, bcc} {
		for _, address := range list {
			recipients = append(recipients, address.Address)
		}
	}
	return deliver(c.ctx, c, from.Address, recipients, message)
}

// buildMail encodes the message. BCC recipients only appear in the envelope.
func buildMail(now time.Time, from *mail.Address, to, cc []*mail.Address, subject, body string, html bool, extra map[string]string) ([]byte, error) {
	var buffer bytes.Buffer
	write := func(name, value string) { fmt.Fprintf(&buffer, "%s: %s\r\n", name, value) }
	list := func(addresses []*mail.Address) string {
		parts := make([]string, len(addresses))
		for i, address := range addresses {
			parts[i] = address.String()
		}
		return strings.Join(parts, ", ")
	}
	write("From", from.String())
	if len(to) > 0 {
		write("To", list(to))
	}
	if len(cc) > 0 {
		write("Cc", list(cc))
	}
	write("Subject", mime.QEncoding.Encode("utf-8", strings.NewReplacer("\r", " ", "\n", " ").Replace(subject)))
	write("Date", now.Format(time.RFC1123Z))
	write("MIME-Version", "1.0")
	contentType := "text/plain; charset=utf-8"
	if html {
		contentType = "text/html; charset=utf-8"
	}
	write("Content-Type", contentType)
	write("Content-Transfer-Encoding", "quoted-printable")
	names := make([]string, 0, len(extra))
	for name := range extra {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		write(name, mime.QEncoding.Encode("utf-8", extra[name]))
	}
	buffer.WriteString("\r\n")
	encoder := quotedprintable.NewWriter(&buffer)
	if _, err := encoder.Write([]byte(body)); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	return buffer.Bytes(), nil
}

func signDKIM(c *call, message []byte) ([]byte, error) {
	block, _ := pem.Decode([]byte(c.s("smtpDkimPrivateKey")))
	if block == nil {
		return nil, errors.New("the DKIM private key isn't valid PEM")
	}
	var signer crypto.Signer
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		signer = key
	} else if parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		key, ok := parsed.(crypto.Signer)
		if !ok {
			return nil, errors.New("the DKIM private key type isn't supported")
		}
		signer = key
	} else {
		return nil, errors.New("the DKIM private key can't be parsed")
	}
	if _, isRSA := signer.(*rsa.PrivateKey); !isRSA && c.t("smtpDkimHashAlgo") && strings.Contains(strings.ToLower(c.s("smtpDkimHashAlgo")), "sha1") {
		return nil, errors.New("this DKIM key type needs SHA-256")
	}
	options := &dkim.SignOptions{Domain: c.s("smtpDkimDomain"), Selector: c.s("smtpDkimKeySelector"), Signer: signer, Hash: crypto.SHA256}
	if fields := splitTrim(c.s("smtpDkimheaderFieldNames"), ":,"); len(fields) > 0 {
		options.HeaderKeys = fields
		if !containsFold(fields, "from") {
			options.HeaderKeys = append(options.HeaderKeys, "From")
		}
	}
	var signed bytes.Buffer
	if err := dkim.Sign(&signed, bytes.NewReader(message), options); err != nil {
		return nil, fmt.Errorf("sign with DKIM: %w", err)
	}
	return signed.Bytes(), nil
}

func containsFold(list []string, value string) bool {
	for _, item := range list {
		if strings.EqualFold(item, value) {
			return true
		}
	}
	return false
}

func deliver(ctx context.Context, c *call, from string, recipients []string, message []byte) error {
	host := c.s("smtpHost")
	port := c.s("smtpPort")
	if port == "" {
		port = "587"
		if c.t("smtpSecure") {
			port = "465"
		}
	}
	if _, err := strconv.Atoi(port); err != nil {
		return errors.New("invalid SMTP port")
	}
	address := net.JoinHostPort(host, port)
	tlsConfig := &tls.Config{ServerName: host, InsecureSkipVerify: c.t("smtpIgnoreTLSError"), MinVersion: tls.VersionTLS12} //nolint:gosec // Per-notification opt-in.

	dialer := &net.Dialer{}
	var conn net.Conn
	var err error
	if c.t("smtpSecure") {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fmt.Errorf("connect to the SMTP server: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}

	client, err := smtp.NewClient(conn, host)
	if err != nil {
		return fmt.Errorf("start SMTP: %w", err)
	}
	defer func() { _ = client.Close() }()

	encrypted := c.t("smtpSecure")
	if !encrypted && !c.t("smtpIgnoreSTARTTLS") {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fmt.Errorf("start TLS: %w", err)
			}
			encrypted = true
		}
	}

	if c.t("smtpUsername") || c.t("smtpPassword") {
		if !encrypted && !c.t("smtpIgnoreSTARTTLS") {
			return errors.New("the SMTP server doesn't offer TLS, so the password wasn't sent. Set smtpIgnoreSTARTTLS to allow this")
		}
		username, password := c.s("smtpUsername"), c.s("smtpPassword")
		var auth smtp.Auth = plainAuth{username, password}
		if ok, mechanisms := client.Extension("AUTH"); ok && !strings.Contains(mechanisms, "PLAIN") && strings.Contains(mechanisms, "LOGIN") {
			auth = loginAuth{username, password}
		}
		if err := client.Auth(auth); err != nil {
			return fmt.Errorf("SMTP authentication failed: %w", err)
		}
	}

	if err := client.Mail(from); err != nil {
		return fmt.Errorf("SMTP MAIL: %w", err)
	}
	for _, recipient := range recipients {
		if err := client.Rcpt(recipient); err != nil {
			return fmt.Errorf("SMTP RCPT: %w", err)
		}
	}
	writer, err := client.Data()
	if err != nil {
		return fmt.Errorf("SMTP DATA: %w", err)
	}
	if _, err := writer.Write(message); err != nil {
		return fmt.Errorf("write message: %w", err)
	}
	if err := writer.Close(); err != nil {
		return fmt.Errorf("finish message: %w", err)
	}
	return client.Quit()
}
