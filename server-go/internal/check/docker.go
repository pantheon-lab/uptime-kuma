package check

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// DockerHost is a Docker daemon that docker monitors ask about containers. Type is socket, with
// Daemon a Unix socket's path, or tcp, with Daemon a tcp://, http:// or https:// URL.
type DockerHost struct {
	ID     int32  `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"dockerType"`
	Daemon string `json:"dockerDaemon"`
}

// DockerHostReader finds a docker monitor's host.
type DockerHostReader interface {
	DockerHost(ctx context.Context, id int32) (DockerHost, error)
}

// SetDockerHosts sets where Docker hosts come from. Call it before checks start.
func (c *Checker) SetDockerHosts(r DockerHostReader) { c.dockerHosts = r }

// DockerTLSDir is where client certificates for Docker hosts are, as in the Node.js backend: the
// files ca.pem, cert.pem and key.pem in a directory named after the host. Empty means none.
var DockerTLSDir = os.Getenv("DOCKER_TLS_DIR_PATH")

const dockerTestTimeout = 5 * time.Second

// ValidateDockerHost checks a host's settings before it's saved.
func ValidateDockerHost(h DockerHost) error {
	switch {
	case strings.TrimSpace(h.Name) == "":
		return errors.New("name is required")
	case h.Type == "socket":
		if !strings.HasPrefix(h.Daemon, "/") {
			return errors.New("the daemon of a socket host must be the socket's path, such as /var/run/docker.sock")
		}
	case h.Type == "tcp":
		if _, err := dockerBaseURL(h.Daemon); err != nil {
			return err
		}
	default:
		return errors.New("dockerType must be socket or tcp")
	}
	return nil
}

// dockerBaseURL reads a TCP host's URL. tcp:// means http://, as in Node.js.
func dockerBaseURL(daemon string) (*url.URL, error) {
	u, err := url.Parse(strings.Replace(strings.TrimSpace(daemon), "tcp://", "http://", 1))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("the daemon of a tcp host must be a URL, such as tcp://docker:2375 or https://docker:2376")
	}
	return u, nil
}

// dockerClient returns a client for the host, and the URL that API paths go under.
func dockerClient(h DockerHost) (*http.Client, string, error) {
	transport := &http.Transport{DisableKeepAlives: true}
	base := "http://docker"
	switch h.Type {
	case "socket":
		path := h.Daemon
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", path)
		}
	case "tcp":
		u, err := dockerBaseURL(h.Daemon)
		if err != nil {
			return nil, "", err
		}
		base = strings.TrimSuffix(u.String(), "/")
		if u.Scheme == "https" {
			config, err := dockerTLSConfig(u.Hostname())
			if err != nil {
				return nil, "", err
			}
			transport.TLSClientConfig = config
		}
	default:
		return nil, "", fmt.Errorf("unknown Docker host type %q", h.Type)
	}
	return &http.Client{Transport: transport}, base, nil
}

// dockerTLSConfig adds the host's client certificate, if all three files are there.
func dockerTLSConfig(hostname string) (*tls.Config, error) {
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if DockerTLSDir == "" {
		return config, nil
	}
	dir := filepath.Join(DockerTLSDir, filepath.Base(hostname))
	ca, errCA := os.ReadFile(filepath.Join(dir, "ca.pem"))
	cert, errCert := os.ReadFile(filepath.Join(dir, "cert.pem"))
	key, errKey := os.ReadFile(filepath.Join(dir, "key.pem"))
	if errCA != nil || errCert != nil || errKey != nil {
		return config, nil
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return nil, fmt.Errorf("read the client certificate in %s: %w", dir, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(ca) {
		return nil, fmt.Errorf("no certificate in %s", filepath.Join(dir, "ca.pem"))
	}
	config.RootCAs = pool
	config.Certificates = []tls.Certificate{pair}
	return config, nil
}

// dockerGet reads one API path into v.
func dockerGet(ctx context.Context, h DockerHost, path string, v any) error {
	client, base, err := dockerClient(h)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "*/*")
	res, err := client.Do(req)
	if err != nil {
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return err
	}
	defer func() { _ = res.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if res.StatusCode != http.StatusOK {
		// Docker explains errors, such as "No such container: web".
		var problem struct{ Message string }
		if json.Unmarshal(body, &problem) == nil && problem.Message != "" {
			return errors.New(problem.Message)
		}
		return fmt.Errorf("Request failed with status code %d", res.StatusCode) //nolint:staticcheck // Shown to users, as in Node.js.
	}
	if err := json.Unmarshal(body, v); err != nil {
		return errors.New("Invalid Docker response, is it Docker really a daemon?") //nolint:staticcheck // Shown to users, as in Node.js.
	}
	return nil
}

// TestDockerHost counts the host's containers, as the Docker host dialog's test does.
func TestDockerHost(ctx context.Context, h DockerHost) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, dockerTestTimeout)
	defer cancel()
	var containers []map[string]any
	if err := dockerGet(ctx, h, "/containers/json?all=true", &containers); err != nil {
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			return 0, errors.New("Connection to Docker daemon timed out.") //nolint:staticcheck // Shown to users, as in Node.js.
		}
		return 0, err
	}
	if len(containers) > 0 {
		if _, found := containers[0]["ImageID"]; !found {
			return 0, errors.New("Invalid Docker response, is it Docker really a daemon?") //nolint:staticcheck // Shown to users, as in Node.js.
		}
	}
	return len(containers), nil
}

// runDocker reads the container's state, with the Node.js backend's rules and messages.
func (c *Checker) runDocker(ctx context.Context, m monitor.Monitor) monitor.Result {
	if c.dockerHosts == nil {
		return monitor.Result{Msg: "Failed to load docker host config"}
	}
	host, err := c.dockerHosts.DockerHost(ctx, m.DockerHostID)
	if err != nil {
		return monitor.Result{Msg: "Failed to load docker host config"}
	}
	start := time.Now()
	var container struct {
		State *struct {
			Status     string
			Running    bool
			Paused     bool
			Restarting bool
			Health     *struct{ Status string }
		}
	}
	if err := dockerGet(ctx, host, "/containers/"+url.PathEscape(strings.TrimSpace(m.DockerContainer))+"/json", &container); err != nil {
		return monitor.Result{Msg: requestError(err, m.EffectiveTimeout())}
	}
	ping := elapsedMs(start)
	state := container.State
	switch {
	case state == nil:
		return monitor.Result{Msg: "Container state is not available"}
	case !state.Running:
		return monitor.Result{Msg: "Container State is " + state.Status}
	case state.Paused:
		return monitor.Result{Msg: "Container is in a paused state"}
	case state.Restarting:
		return monitor.Result{Pending: true, Msg: "Container is reporting it is currently restarting"}
	case state.Health != nil && state.Health.Status != "none" && state.Health.Status != "":
		switch state.Health.Status {
		case "healthy":
			return monitor.Result{OK: true, Ping: &ping, Msg: "healthy"}
		case "unhealthy":
			return monitor.Result{Msg: "Container State is unhealthy according to its healthcheck"}
		default:
			return monitor.Result{Pending: true, Msg: state.Health.Status}
		}
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: fmt.Sprintf("Container has not reported health and is currently %s. "+
		"As it is running, it is considered UP. Consider adding a health check for better service visibility", state.Status)}
}
