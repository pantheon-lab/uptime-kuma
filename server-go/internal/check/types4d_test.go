package check

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"google.golang.org/grpc"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

type fakeDockerHosts map[int32]DockerHost

func (f fakeDockerHosts) DockerHost(_ context.Context, id int32) (DockerHost, error) {
	if h, found := f[id]; found {
		return h, nil
	}
	return DockerHost{}, errors.New("not found")
}

// dockerAPI serves a fake Docker API on a Unix socket.
func dockerAPI(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return path
}

func TestDocker(t *testing.T) {
	states := map[string]string{
		"healthy":    `{"State":{"Status":"running","Running":true,"Health":{"Status":"healthy"}}}`,
		"unhealthy":  `{"State":{"Status":"running","Running":true,"Health":{"Status":"unhealthy"}}}`,
		"starting":   `{"State":{"Status":"running","Running":true,"Health":{"Status":"starting"}}}`,
		"plain":      `{"State":{"Status":"running","Running":true}}`,
		"exited":     `{"State":{"Status":"exited","Running":false}}`,
		"paused":     `{"State":{"Status":"paused","Running":true,"Paused":true}}`,
		"restarting": `{"State":{"Status":"restarting","Running":true,"Restarting":true}}`,
	}
	socket := dockerAPI(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/containers/json" {
			_, _ = io.WriteString(w, `[{"Id":"a","ImageID":"sha256:b"}]`)
			return
		}
		name := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/containers/"), "/json")
		body, found := states[name]
		if !found {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"message":"No such container: `+name+`"}`)
			return
		}
		_, _ = io.WriteString(w, body)
	})
	host := DockerHost{ID: 1, Name: "local", Type: "socket", Daemon: socket}
	checker := New(nil)
	checker.SetDockerHosts(fakeDockerHosts{1: host})
	for container, want := range map[string]monitor.Result{
		"healthy":    {OK: true, Msg: "healthy"},
		"unhealthy":  {Msg: "Container State is unhealthy according to its healthcheck"},
		"starting":   {Pending: true, Msg: "starting"},
		"plain":      {OK: true, Msg: "Container has not reported health and is currently running. As it is running, it is considered UP. Consider adding a health check for better service visibility"},
		"exited":     {Msg: "Container State is exited"},
		"paused":     {Msg: "Container is in a paused state"},
		"restarting": {Pending: true, Msg: "Container is reporting it is currently restarting"},
		"missing":    {Msg: "No such container: missing"},
	} {
		m := monitor.Monitor{Type: monitor.TypeDocker, Interval: 60, Settings: monitor.Settings{DockerHostID: 1, DockerContainer: container}}
		got := checker.Run(context.Background(), m)
		if got.OK != want.OK || got.Pending != want.Pending || got.Msg != want.Msg {
			t.Errorf("%s: %+v", container, got)
		}
	}
	m := monitor.Monitor{Type: monitor.TypeDocker, Interval: 60, Settings: monitor.Settings{DockerHostID: 2, DockerContainer: "x"}}
	if got := checker.Run(context.Background(), m); got.Msg != "Failed to load docker host config" {
		t.Errorf("missing host: %+v", got)
	}
	if count, err := TestDockerHost(context.Background(), host); err != nil || count != 1 {
		t.Errorf("test host: %d %v", count, err)
	}
	if err := ValidateDockerHost(DockerHost{Name: "x", Type: "tcp", Daemon: "tcp://docker:2375"}); err != nil {
		t.Error(err)
	}
	if err := ValidateDockerHost(DockerHost{Name: "x", Type: "tcp", Daemon: "docker:2375"}); err == nil {
		t.Error("a tcp host without a scheme was accepted")
	}
}

// TestDockerDaemon asks the real daemon about a container, if TEST_DOCKER_CONTAINER names one.
func TestDockerDaemon(t *testing.T) {
	container := os.Getenv("TEST_DOCKER_CONTAINER")
	if container == "" {
		t.Skip("TEST_DOCKER_CONTAINER isn't set")
	}
	checker := New(nil)
	checker.SetDockerHosts(fakeDockerHosts{1: {Type: "socket", Daemon: "/var/run/docker.sock"}})
	got := checker.Run(context.Background(), monitor.Monitor{Type: monitor.TypeDocker, Interval: 60, Settings: monitor.Settings{DockerHostID: 1, DockerContainer: container}})
	if !got.OK {
		t.Errorf("%+v", got)
	}
}

const healthProto = `
syntax = "proto3";

package grpc.health.v1;

service Health {
  rpc Check(HealthCheckRequest) returns (HealthCheckResponse);
  rpc Watch(HealthCheckRequest) returns (stream HealthCheckResponse);
}

message HealthCheckRequest {
  string service = 1;
}

message HealthCheckResponse {
  enum ServingStatus {
    UNKNOWN = 0;
    SERVING = 1;
    NOT_SERVING = 2;
    SERVICE_UNKNOWN = 3;
  }
  ServingStatus status = 1;
}
`

func TestGRPC(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer()
	healthServer := health.NewServer()
	healthServer.SetServingStatus("down", healthpb.HealthCheckResponse_NOT_SERVING)
	healthpb.RegisterHealthServer(server, healthServer)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	grpcMonitor := func(keyword, body string) monitor.Monitor {
		return monitor.Monitor{Type: monitor.TypeGRPC, Interval: 60, Keyword: keyword, Settings: monitor.Settings{
			GRPCURL: listener.Addr().String(), GRPCProtobuf: healthProto, GRPCServiceName: "Health", GRPCMethod: "check", GRPCBody: body}}
	}
	checker := New(nil)
	if got := checker.Run(context.Background(), grpcMonitor("SERVING", `{"service":""}`)); !got.OK || got.Msg != `{"status":"SERVING"}, keyword [SERVING] is found` {
		t.Errorf("serving: %+v", got)
	}
	if got := checker.Run(context.Background(), grpcMonitor(`"SERVING"`, `{"service":"down"}`)); got.OK || got.Msg != `keyword ["SERVING"] is not in [{"status":"NOT_SERVING"}]` {
		t.Errorf("not serving: %+v", got)
	}
	inverted := grpcMonitor("NOT_SERVING", "")
	inverted.InvertKeyword = true
	inverted.GRPCServiceName, inverted.GRPCMethod = "grpc.health.v1.Health", "Check"
	if got := checker.Run(context.Background(), inverted); !got.OK || got.Msg != `{"status":"SERVING"}, keyword [NOT_SERVING] not found` {
		t.Errorf("inverted: %+v", got)
	}
	unknown := grpcMonitor("x", `{"service":"nope"}`)
	if got := checker.Run(context.Background(), unknown); got.OK || got.Msg != "5 NOT_FOUND: unknown service" {
		t.Errorf("unknown service: %+v", got)
	}
	streaming := grpcMonitor("x", "")
	streaming.GRPCMethod = "watch"
	if got := checker.Run(context.Background(), streaming); got.OK || !strings.Contains(got.Msg, "streaming") {
		t.Errorf("streaming: %+v", got)
	}
	broken := grpcMonitor("x", "")
	broken.GRPCProtobuf = "syntax = nonsense"
	if got := checker.Run(context.Background(), broken); got.OK || !strings.HasPrefix(got.Msg, "invalid protobuf") {
		t.Errorf("broken proto: %+v", got)
	}
	closed := grpcMonitor("x", "")
	closed.GRPCURL = "127.0.0.1:1"
	closed.Timeout = 2
	if got := checker.Run(context.Background(), closed); got.OK || !strings.HasPrefix(got.Msg, "14 UNAVAILABLE: ") {
		t.Errorf("closed port: %+v", got)
	}
}

// sftpServer serves SFTP over SSH, for one user with a password and a key, from a directory.
func sftpServer(t *testing.T, password string, key ssh.PublicKey) (int32, string) {
	t.Helper()
	_, hostKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(hostKey)
	if err != nil {
		t.Fatal(err)
	}
	config := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, given []byte) (*ssh.Permissions, error) {
			if c.User() == "kuma" && string(given) == password {
				return nil, nil
			}
			return nil, errors.New("wrong password")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, given ssh.PublicKey) (*ssh.Permissions, error) {
			if c.User() == "kuma" && bytes.Equal(given.Marshal(), key.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("wrong key")
		},
	}
	config.AddHostKey(signer)
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "upload"), 0o700); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(func() { _ = listener.Close(); wg.Wait() })
	wg.Go(func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			wg.Go(func() { serveSFTP(conn, config, root) })
		}
	})
	return int32(listener.Addr().(*net.TCPAddr).Port), root
}

func serveSFTP(conn net.Conn, config *ssh.ServerConfig, root string) {
	defer func() { _ = conn.Close() }()
	_, channels, requests, err := ssh.NewServerConn(conn, config)
	if err != nil {
		return
	}
	go ssh.DiscardRequests(requests)
	for newChannel := range channels {
		channel, channelRequests, err := newChannel.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range channelRequests {
				isSFTP := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				_ = req.Reply(isSFTP, nil)
				if isSFTP {
					server, err := sftp.NewServer(channel, sftp.WithServerWorkingDirectory(root))
					if err == nil {
						_ = server.Serve()
					}
					_ = channel.Close()
				}
			}
		}()
	}
}

func TestSFTP(t *testing.T) {
	_, clientKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	clientSigner, err := ssh.NewSignerFromKey(clientKey)
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKeyWithPassphrase(clientKey, "", []byte("secret phrase"))
	if err != nil {
		t.Fatal(err)
	}
	encryptedKey := string(pem.EncodeToMemory(block))
	port, root := sftpServer(t, "pw", clientSigner.PublicKey())

	base := monitor.Monitor{Type: monitor.TypeSFTP, Interval: 60, Timeout: 5, Hostname: "127.0.0.1", Port: port,
		Settings: monitor.Settings{SSHUsername: "kuma", SSHAuthMethod: "password", SSHPassword: "pw"}}
	checker := New(nil)
	cases := map[string]struct {
		edit func(*monitor.Monitor)
		ok   bool
		msg  string
	}{
		"password":       {func(*monitor.Monitor) {}, true, "SFTP connection successful"},
		"path":           {func(m *monitor.Monitor) { m.SFTPPath = filepath.Join(root, "upload") }, true, "SFTP connection successful"},
		"relative path":  {func(m *monitor.Monitor) { m.SFTPPath = "upload" }, true, "SFTP connection successful"},
		"missing path":   {func(m *monitor.Monitor) { m.SFTPPath = "/nope/x" }, false, `Path "/nope/x" does not exist on the SFTP server`},
		"wrong password": {func(m *monitor.Monitor) { m.SSHPassword = "no" }, false, "All configured authentication methods failed"},
		"key": {func(m *monitor.Monitor) {
			m.SSHAuthMethod, m.SSHPrivateKey, m.SSHPassphrase = "privateKey", encryptedKey, "secret phrase"
		}, true, "SFTP connection successful"},
		"wrong passphrase": {func(m *monitor.Monitor) {
			m.SSHAuthMethod, m.SSHPrivateKey, m.SSHPassphrase = "privateKey", encryptedKey, "nope"
		}, false, "Cannot parse privateKey: "},
		"refused": {func(m *monitor.Monitor) { m.Port = 1 }, false, "Connection refused — 127.0.0.1:1 actively rejected the connection"},
	}
	for name, c := range cases {
		m := base
		c.edit(&m)
		got := checker.Run(context.Background(), m)
		if got.OK != c.ok || (c.msg != "" && got.Msg != c.msg && !strings.HasPrefix(got.Msg, c.msg)) {
			t.Errorf("%s: %+v", name, got)
		}
	}
}

func TestPM2(t *testing.T) {
	saved := pm2Command
	t.Cleanup(func() { pm2Command = saved })
	pm2Command = func(context.Context) ([]byte, []byte, error) {
		return []byte(`[{"pm_id":0,"name":"api","pm2_env":{"status":"online"}},{"pm_id":1,"name":"worker","pm2_env":{"status":"stopped"}},{"pm_id":2}]`), nil, nil
	}
	list, err := PM2ProcessList(context.Background())
	if err != nil || len(list) != 3 || list[2] != (PM2Process{ID: "2", Name: "2", Status: "unknown"}) {
		t.Fatalf("%+v %v", list, err)
	}
	for name, want := range map[string]monitor.Result{
		"api":    {OK: true, Msg: "PM2 process 'api' is online."},
		"0":      {OK: true, Msg: "PM2 process '0' is online."},
		"worker": {Msg: "PM2 process 'worker' is stopped."},
		"nope":   {Msg: "PM2 process 'nope' was not found."},
	} {
		got := New(nil).Run(context.Background(), monitor.Monitor{Type: monitor.TypePM2, Interval: 60, Settings: monitor.Settings{SystemServiceName: name}})
		if got.OK != want.OK || got.Msg != want.Msg {
			t.Errorf("%s: %+v", name, got)
		}
	}
	pm2Command = func(context.Context) ([]byte, []byte, error) {
		return nil, []byte("pm2: daemon not running"), errors.New("exit status 1")
	}
	if _, err := PM2ProcessList(context.Background()); err == nil || err.Error() != "Unable to query PM2 process list (pm2: daemon not running)." {
		t.Errorf("failure: %v", err)
	}
}

// TestSystemService asks this machine's systemd, if it has one.
func TestSystemService(t *testing.T) {
	conn, err := systemBus(context.Background())
	if err != nil {
		t.Skipf("no system bus: %v", err)
	}
	_ = conn.Close()
	checker := New(nil)
	run := func(name string) monitor.Result {
		return checker.Run(context.Background(), monitor.Monitor{Type: monitor.TypeSystemService, Interval: 60, Settings: monitor.Settings{SystemServiceName: name}})
	}
	if got := run("dbus"); !got.OK || got.Msg != "Service 'dbus' is running." {
		t.Errorf("dbus: %+v", got)
	}
	if got := run("dbus.socket"); !got.OK {
		t.Errorf("dbus.socket: %+v", got)
	}
	if got := run("kuma-no-such-service"); got.OK || got.Msg != "inactive" {
		t.Errorf("missing service: %+v", got)
	}
}

type fakeBrowserStore struct {
	mu          sync.Mutex
	chrome      string
	remote      map[int32]RemoteBrowser
	screenshots map[int32][]byte
}

func (f *fakeBrowserStore) RemoteBrowser(_ context.Context, id int32) (RemoteBrowser, error) {
	if b, found := f.remote[id]; found {
		return b, nil
	}
	return RemoteBrowser{}, errors.New("not found")
}

func (f *fakeBrowserStore) ChromeExecutable(context.Context) (string, error) { return f.chrome, nil }

func (f *fakeBrowserStore) SaveScreenshot(_ context.Context, id int32, png []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.screenshots[id] = png
	return nil
}

// TestRealBrowser loads pages in the Chrome that TEST_CHROME names, locally and through its
// DevTools port.
func TestRealBrowser(t *testing.T) {
	chrome := os.Getenv("TEST_CHROME")
	if chrome == "" {
		t.Skip("TEST_CHROME isn't set")
	}
	t.Setenv("UPTIME_KUMA_ALLOW_ALL_CHROME_EXEC", "1")
	t.Cleanup(StopBrowser)
	page := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "<html><body><h1>Hello</h1></body></html>")
	}))
	t.Cleanup(page.Close)

	// A browser to use remotely, on a free port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	cmd := exec.Command(chrome, fmt.Sprintf("--remote-debugging-port=%d", port), "--headless", "--no-sandbox", "about:blank")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	remoteURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	for range 50 {
		if _, err := TestRemoteBrowser(context.Background(), remoteURL); err == nil {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	store := &fakeBrowserStore{chrome: chrome, remote: map[int32]RemoteBrowser{1: {ID: 1, URL: remoteURL}}, screenshots: map[int32][]byte{}}
	checker := New(nil)
	checker.SetBrowsers(store)
	pngHeader := []byte("\x89PNG")
	for _, remoteID := range []int32{0, 1} {
		m := monitor.Monitor{ID: 7 + remoteID, Type: monitor.TypeRealBrowser, Interval: 60, URL: page.URL,
			Settings: monitor.Settings{RemoteBrowserID: remoteID, ScreenshotDelay: 100}}
		got := checker.Run(context.Background(), m)
		if !got.OK || got.Msg != "200" || got.Ping == nil {
			t.Errorf("remote %d: %+v", remoteID, got)
		}
		if !bytes.HasPrefix(store.screenshots[m.ID], pngHeader) {
			t.Errorf("remote %d: no screenshot", remoteID)
		}
		m.URL = page.URL + "/missing"
		if got := checker.Run(context.Background(), m); got.OK || got.Msg != "404" {
			t.Errorf("remote %d, missing page: %+v", remoteID, got)
		}
	}
	// The remote browser is still there for the next check.
	if _, err := TestRemoteBrowser(context.Background(), remoteURL); err != nil {
		t.Errorf("the remote browser was closed: %v", err)
	}
	version, err := TestChrome(context.Background(), chrome)
	if err != nil || version == "" {
		t.Errorf("test chrome: %q %v", version, err)
	}
	t.Setenv("UPTIME_KUMA_ALLOW_ALL_CHROME_EXEC", "")
	if _, err := chromePath("/tmp/evil"); err == nil {
		t.Error("an executable outside the allowed list was accepted")
	}
}
