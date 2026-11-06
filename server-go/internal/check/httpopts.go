package check

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	ntlmssp "github.com/Azure/go-ntlmssp"
	"golang.org/x/net/proxy"
	"golang.org/x/oauth2"
	"golang.org/x/oauth2/clientcredentials"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// Proxy is a proxy that HTTP monitors can connect through, with the Node.js backend's fields.
type Proxy struct {
	ID       int32  `json:"id"`
	Protocol string `json:"protocol"`
	Host     string `json:"host"`
	Port     int32  `json:"port"`
	Auth     bool   `json:"auth"`
	Username string `json:"username"`
	Password string `json:"password"`
	Active   bool   `json:"active"`
	Default  bool   `json:"default"`
}

// ProxyProtocols are the protocols a proxy can speak, as in Node.js. socks and socks5h resolve
// names on the proxy, and socks5 and socks4 here.
var ProxyProtocols = []string{"http", "https", "socks", "socks5", "socks5h", "socks4"}

// ValidateProxy checks a proxy's settings before they're saved.
func ValidateProxy(p Proxy) error {
	switch {
	case !slices.Contains(ProxyProtocols, p.Protocol):
		return fmt.Errorf("unsupported proxy protocol %q. Supported protocols are %s", p.Protocol, strings.Join(ProxyProtocols, ", "))
	case strings.TrimSpace(p.Host) == "" || strings.ContainsAny(p.Host, " /@"):
		return errors.New("the proxy's host must be a host name or an IP address")
	case p.Port < 1 || p.Port > 65535:
		return errors.New("the proxy's port must be from 1 to 65535")
	}
	return nil
}

// ProxyReader finds an HTTP monitor's proxy.
type ProxyReader interface {
	Proxy(ctx context.Context, id int32) (Proxy, error)
}

// SetProxies sets where proxies come from. Call it before checks start.
func (c *Checker) SetProxies(r ProxyReader) { c.proxies = r }

// transports keeps one transport for each combination of options, so that connections are
// reused between checks as they are without options.
type transports struct {
	mu   sync.Mutex
	byID map[string]*http.Transport
}

// transportFor returns the transport for a monitor's TLS, IP family, client certificate and proxy.
func (c *Checker) transportFor(ctx context.Context, m monitor.Monitor) (*http.Transport, error) {
	var p *Proxy
	if m.ProxyID > 0 {
		if c.proxies == nil {
			return nil, errors.New("proxies aren't available")
		}
		found, err := c.proxies.Proxy(ctx, m.ProxyID)
		if err != nil {
			return nil, errors.New("the monitor's proxy doesn't exist")
		}
		// As in Node.js, a proxy that's switched off isn't used.
		if found.Active {
			p = &found
		}
	}
	mtls := m.AuthMethod == "mtls"
	if !mtls && m.IPFamily == "" && p == nil {
		if m.IgnoreTLS {
			return c.insecure, nil
		}
		return c.verified, nil
	}

	key := sha256.New()
	_, _ = fmt.Fprintf(key, "%t\x00%s\x00", m.IgnoreTLS, m.IPFamily)
	if mtls {
		_, _ = fmt.Fprintf(key, "%s\x00%s\x00%s\x00", m.TLSCert, m.TLSKey, m.TLSCA)
	}
	if p != nil {
		_, _ = fmt.Fprintf(key, "%s\x00%s\x00%d\x00%t\x00%s\x00%s", p.Protocol, p.Host, p.Port, p.Auth, p.Username, p.Password)
	}
	id := string(key.Sum(nil))

	c.extra.mu.Lock()
	defer c.extra.mu.Unlock()
	if t, found := c.extra.byID[id]; found {
		return t, nil
	}
	base := c.verified
	if m.IgnoreTLS {
		base = c.insecure
	}
	t := base.Clone()
	dialer := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	network := "tcp"
	switch m.IPFamily {
	case "ipv4":
		network = "tcp4"
	case "ipv6":
		network = "tcp6"
	}
	t.DialContext = func(ctx context.Context, _, address string) (net.Conn, error) {
		return dialer.DialContext(ctx, network, address)
	}
	if mtls {
		pair, err := tls.X509KeyPair([]byte(m.TLSCert), []byte(m.TLSKey))
		if err != nil {
			return nil, fmt.Errorf("the client certificate is invalid: %w", err)
		}
		t.TLSClientConfig.Certificates = []tls.Certificate{pair}
		if strings.TrimSpace(m.TLSCA) != "" {
			pool := x509.NewCertPool()
			if !pool.AppendCertsFromPEM([]byte(m.TLSCA)) {
				return nil, errors.New("the CA certificate is invalid")
			}
			t.TLSClientConfig.RootCAs = pool
		}
	}
	if p != nil {
		if err := useProxy(t, *p, network, dialer); err != nil {
			return nil, err
		}
	}
	if c.extra.byID == nil {
		c.extra.byID = map[string]*http.Transport{}
	}
	c.extra.byID[id] = t
	return t, nil
}

// useProxy sends a transport's connections through the proxy.
func useProxy(t *http.Transport, p Proxy, network string, dialer *net.Dialer) error {
	address := net.JoinHostPort(p.Host, strconv.Itoa(int(p.Port)))
	switch p.Protocol {
	case "http", "https":
		u := &url.URL{Scheme: p.Protocol, Host: address}
		if p.Auth {
			u.User = url.UserPassword(p.Username, p.Password)
		}
		t.Proxy = http.ProxyURL(u)
		return nil
	case "socks", "socks5", "socks5h":
		var auth *proxy.Auth
		if p.Auth {
			auth = &proxy.Auth{User: p.Username, Password: p.Password}
		}
		socks, err := proxy.SOCKS5(network, address, auth, dialer)
		if err != nil {
			return err
		}
		contextDialer := socks.(proxy.ContextDialer)
		localLookup := p.Protocol == "socks5"
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, _, target string) (net.Conn, error) {
			if localLookup {
				resolved, err := resolveTarget(ctx, target, false)
				if err != nil {
					return nil, err
				}
				target = resolved
			}
			return contextDialer.DialContext(ctx, "tcp", target)
		}
		return nil
	case "socks4":
		t.Proxy = nil
		t.DialContext = func(ctx context.Context, _, target string) (net.Conn, error) {
			return dialSOCKS4(ctx, dialer, network, address, p.Username, target)
		}
		return nil
	}
	return fmt.Errorf("unsupported proxy protocol %q", p.Protocol)
}

// resolveTarget replaces the host of host:port with one of its addresses.
func resolveTarget(ctx context.Context, target string, ipv4Only bool) (string, error) {
	host, port, err := net.SplitHostPort(target)
	if err != nil {
		return "", err
	}
	if ip := net.ParseIP(host); ip != nil {
		if ipv4Only && ip.To4() == nil {
			return "", errors.New("a SOCKS4 proxy can't reach an IPv6 address")
		}
		return target, nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return "", err
	}
	for _, a := range addresses {
		if !ipv4Only || a.IP.To4() != nil {
			return net.JoinHostPort(a.IP.String(), port), nil
		}
	}
	return "", fmt.Errorf("%s has no IPv4 address for a SOCKS4 proxy", host)
}

// dialSOCKS4 connects through a SOCKS4 proxy, which only takes IPv4 addresses.
func dialSOCKS4(ctx context.Context, dialer *net.Dialer, network, proxyAddress, user, target string) (net.Conn, error) {
	resolved, err := resolveTarget(ctx, target, true)
	if err != nil {
		return nil, err
	}
	host, portText, _ := net.SplitHostPort(resolved)
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	conn, err := dialer.DialContext(ctx, network, proxyAddress)
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := []byte{4, 1, 0, 0}
	binary.BigEndian.PutUint16(request[2:], uint16(port))
	request = append(request, net.ParseIP(host).To4()...)
	request = append(request, user...)
	request = append(request, 0)
	reply := make([]byte, 8)
	if _, err := conn.Write(request); err == nil {
		_, err = io.ReadFull(conn, reply)
	}
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("SOCKS4 proxy: %w", err)
	}
	if reply[1] != 0x5a {
		_ = conn.Close()
		return nil, fmt.Errorf("the SOCKS4 proxy refused the connection (code %d)", reply[1])
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

// oauthTokens keeps each monitor's access token until it expires.
type oauthTokens struct {
	mu     sync.Mutex
	byID   map[int32]*oauth2.Token
	config map[int32]string
}

// oauthToken returns a token for an oauth2-cc login, asking for a new one if there's none, it
// expired, the settings changed, or fresh is set.
func (c *Checker) oauthToken(ctx context.Context, m monitor.Monitor, fresh bool) (*oauth2.Token, error) {
	settings := strings.Join([]string{m.OAuthTokenURL, m.OAuthClientID, m.OAuthClientSecret, m.OAuthScopes, m.OAuthAudience, m.OAuthAuthMethod}, "\x00")
	c.tokens.mu.Lock()
	defer c.tokens.mu.Unlock()
	if c.tokens.byID == nil {
		c.tokens.byID, c.tokens.config = map[int32]*oauth2.Token{}, map[int32]string{}
	}
	if token := c.tokens.byID[m.ID]; !fresh && token != nil && token.Valid() && c.tokens.config[m.ID] == settings {
		return token, nil
	}
	config := clientcredentials.Config{
		ClientID:     m.OAuthClientID,
		ClientSecret: m.OAuthClientSecret,
		TokenURL:     m.OAuthTokenURL,
		Scopes:       strings.Fields(strings.ReplaceAll(m.OAuthScopes, ",", " ")),
		AuthStyle:    oauth2.AuthStyleInHeader,
	}
	if m.OAuthAuthMethod == "client_secret_post" {
		config.AuthStyle = oauth2.AuthStyleInParams
	}
	if m.OAuthAudience != "" {
		config.EndpointParams = url.Values{"audience": {m.OAuthAudience}}
	}
	ctx = context.WithValue(ctx, oauth2.HTTPClient, &http.Client{Transport: c.verified})
	token, err := config.Token(ctx)
	if err != nil {
		return nil, fmt.Errorf("The oauth config is invalid. %w", err) //nolint:staticcheck // Shown to users, as in Node.js.
	}
	c.tokens.byID[m.ID], c.tokens.config[m.ID] = token, settings
	return token, nil
}

// authorize adds a monitor's login to a request.
func (c *Checker) authorize(ctx context.Context, req *http.Request, m monitor.Monitor, freshToken bool) error {
	switch m.AuthMethod {
	case "basic", "ntlm":
		user := m.BasicAuthUser
		if m.AuthMethod == "ntlm" && m.AuthDomain != "" {
			user = m.AuthDomain + `\` + user
		}
		req.Header.Set("Authorization", "Basic "+base64.StdEncoding.EncodeToString([]byte(user+":"+m.BasicAuthPass)))
	case "bearer":
		req.Header.Set("Authorization", "Bearer "+m.BearerToken)
	case "oauth2-cc":
		token, err := c.oauthToken(ctx, m, freshToken)
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", token.Type()+" "+token.AccessToken)
	}
	return nil
}

// roundTripper adds NTLM's handshake to a transport, for an ntlm login.
func roundTripper(t *http.Transport, m monitor.Monitor) http.RoundTripper {
	if m.AuthMethod == "ntlm" {
		return ntlmssp.Negotiator{RoundTripper: t}
	}
	return t
}

// cacheBustURL adds the uptime_kuma_cachebuster parameter, with a random value as Node.js did.
func cacheBustURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	value := make([]byte, 11)
	for i := range value {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			return raw
		}
		value[i] = letters[n.Int64()]
	}
	query := u.Query()
	query.Set("uptime_kuma_cachebuster", string(value))
	u.RawQuery = query.Encode()
	return u.String()
}

// savedResponse is a response body as notification templates see it, cut to the monitor's limit.
func savedResponse(m monitor.Monitor, data []byte) *string {
	limit := int(m.ResponseMaxLength)
	if limit <= 0 {
		limit = monitor.DefaultResponseMaxLength
	}
	text := strings.ToValidUTF8(string(data), "�")
	if utf8.RuneCountInString(text) > limit {
		text = string([]rune(text)[:limit]) + "... (truncated)"
	}
	return &text
}
