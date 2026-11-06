package check

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// runSFTP logs in over SSH, opens SFTP, and checks that the path exists, if one is set.
func runSFTP(ctx context.Context, m monitor.Monitor) monitor.Result {
	host := strings.TrimSpace(m.Hostname)
	address := net.JoinHostPort(host, strconv.Itoa(int(m.Port)))
	var auth []ssh.AuthMethod
	if m.SSHAuthMethod == "privateKey" {
		if strings.TrimSpace(m.SSHPrivateKey) == "" {
			return monitor.Result{Msg: "SSH private key is required for key-based authentication"}
		}
		var signer ssh.Signer
		var err error
		if m.SSHPassphrase != "" {
			signer, err = ssh.ParsePrivateKeyWithPassphrase([]byte(m.SSHPrivateKey), []byte(m.SSHPassphrase))
		} else {
			signer, err = ssh.ParsePrivateKey([]byte(m.SSHPrivateKey))
		}
		if err != nil {
			return monitor.Result{Msg: "Cannot parse privateKey: " + strings.TrimPrefix(err.Error(), "ssh: ")}
		}
		auth = append(auth, ssh.PublicKeys(signer))
	} else {
		auth = append(auth, ssh.Password(m.SSHPassword),
			// Servers that only take keyboard-interactive logins ask for the password that way.
			ssh.KeyboardInteractive(func(_, _ string, questions []string, _ []bool) ([]string, error) {
				answers := make([]string, len(questions))
				for i := range answers {
					answers[i] = m.SSHPassword
				}
				return answers, nil
			}))
	}
	config := &ssh.ClientConfig{
		User: m.SSHUsername,
		Auth: auth,
		// As in Node.js, the server's key isn't checked: there's nowhere to configure it.
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // Same as the Node.js backend.
		Timeout:         m.EffectiveTimeout(),
	}

	start := time.Now()
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return monitor.Result{Msg: sftpError(ctx, err, host, m.Port)}
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	sshConn, channels, requests, err := ssh.NewClientConn(conn, address, config)
	if err != nil {
		return monitor.Result{Msg: sftpError(ctx, err, host, m.Port)}
	}
	client := ssh.NewClient(sshConn, channels, requests)
	defer func() { _ = client.Close() }()
	session, err := sftp.NewClient(client)
	if err != nil {
		return monitor.Result{Msg: sftpError(ctx, err, host, m.Port)}
	}
	defer func() { _ = session.Close() }()
	if path := m.SFTPPath; path != "" {
		if _, err := session.Stat(path); err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return monitor.Result{Msg: fmt.Sprintf("Path %q does not exist on the SFTP server", path)}
			}
			return monitor.Result{Msg: sftpError(ctx, err, host, m.Port)}
		}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: "SFTP connection successful"}
}

// sftpError has the Node.js backend's messages for the common failures.
func sftpError(ctx context.Context, err error, host string, port int32) string {
	var dnsErr *net.DNSError
	switch {
	case errors.Is(err, syscall.ECONNREFUSED):
		return fmt.Sprintf("Connection refused — %s:%d actively rejected the connection", host, port)
	case errors.As(err, &dnsErr) && dnsErr.IsNotFound:
		return fmt.Sprintf("Host not found — cannot resolve hostname %q", host)
	case errors.Is(err, syscall.ECONNRESET):
		return fmt.Sprintf("Connection reset by remote host %s:%d", host, port)
	case errors.Is(err, syscall.ENETUNREACH), errors.Is(err, syscall.EHOSTUNREACH):
		return fmt.Sprintf("Network unreachable — cannot reach %s:%d", host, port)
	case ctx.Err() != nil || isTimeout(err):
		return fmt.Sprintf("Connection timed out — %s:%d did not respond in time", host, port)
	case strings.Contains(err.Error(), "unable to authenticate"):
		return "All configured authentication methods failed"
	case errors.Is(err, io.EOF):
		return fmt.Sprintf("Connection failed — %s:%d is unreachable or offline", host, port)
	}
	return strings.TrimPrefix(err.Error(), "ssh: ")
}
