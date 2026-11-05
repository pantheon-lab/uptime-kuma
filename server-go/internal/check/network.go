package check

import (
	"context"
	"crypto/hmac"
	"crypto/md5" //nolint:gosec // RADIUS defines Message-Authenticator with MD5.
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/smtp"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gosnmp/gosnmp"
	"layeh.com/radius"
	"layeh.com/radius/rfc2865"
	"layeh.com/radius/rfc2869"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// ntpEpochOffset is the time from 1900, NTP's epoch, to 1970, in milliseconds.
const ntpEpochOffset = 2208988800000

func ntpTimestamp(b []byte) float64 {
	seconds := float64(binary.BigEndian.Uint32(b))
	fraction := float64(binary.BigEndian.Uint32(b[4:]))
	return seconds*1000 + fraction*1000/0x100000000
}

func nowNTPMillis() float64 {
	return float64(time.Now().UnixMicro())/1000 + ntpEpochOffset
}

// fixed3 is JavaScript's toFixed(3).
func fixed3(v float64) string { return strconv.FormatFloat(v, 'f', 3, 64) }

// runNTP sends one NTP client request and checks the server's stratum, offset and root
// dispersion against the thresholds, as the Node.js backend did.
func runNTP(ctx context.Context, m monitor.Monitor) monitor.Result {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(m.Hostname, strconv.Itoa(int(m.Port))))
	if err != nil {
		return monitor.Result{Msg: fmt.Sprintf("DNS lookup failed for %s: %s", m.Hostname, unwrapNetError(err))}
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	request := make([]byte, 48)
	request[0] = 0x1b // Leap indicator 0, version 3, client mode.
	start := time.Now()
	t1 := nowNTPMillis()
	if _, err := conn.Write(request); err != nil {
		return monitor.Result{Msg: "Failed to send NTP request: " + unwrapNetError(err)}
	}
	reply := make([]byte, 512)
	n, err := conn.Read(reply)
	t4 := nowNTPMillis()
	if err != nil {
		if isTimeout(err) || ctx.Err() != nil {
			return monitor.Result{Msg: "NTP request timed out"}
		}
		return monitor.Result{Msg: "UDP socket error: " + unwrapNetError(err)}
	}
	ping := elapsedMs(start)
	if n < 48 {
		return monitor.Result{Msg: fmt.Sprintf("Invalid NTP response: expected 48+ bytes, got %d", n)}
	}
	stratum := int32(reply[1])
	rootDispersion := float64(binary.BigEndian.Uint32(reply[8:])) / 65536 * 1000
	refid := fmt.Sprintf("%d.%d.%d.%d", reply[12], reply[13], reply[14], reply[15])
	if stratum <= 1 {
		refid = strings.TrimSpace(strings.ReplaceAll(string(reply[12:16]), "\x00", ""))
	}
	t2, t3 := ntpTimestamp(reply[32:]), ntpTimestamp(reply[40:])
	offset := (t2 - t1 + (t3 - t4)) / 2
	delay := t4 - t1 - (t3 - t2)
	msg := fmt.Sprintf("Stratum: %d, RefID: %s, Offset: %sms, Delay: %sms, Dispersion: %sms",
		stratum, refid, fixed3(offset), fixed3(delay), fixed3(rootDispersion))

	stratumThreshold := m.NTPStratumThreshold
	if stratumThreshold == 0 {
		stratumThreshold = 5
	}
	offsetThreshold := m.NTPTimeOffsetThreshold
	if offsetThreshold == 0 {
		offsetThreshold = 1000
	}
	dispersionThreshold := m.NTPRootDispersionThreshold
	if dispersionThreshold == 0 {
		dispersionThreshold = 500
	}
	switch {
	case stratum == 16:
		return monitor.Result{Msg: "NTP server is unsynchronized (stratum 16)"}
	case stratum >= stratumThreshold:
		return monitor.Result{Msg: fmt.Sprintf("Stratum %d meets or exceeds threshold %d", stratum, stratumThreshold)}
	case math.Abs(offset) >= offsetThreshold:
		return monitor.Result{Msg: fmt.Sprintf("Time offset %sms exceeds threshold %sms", fixed3(offset), jsNumber(offsetThreshold))}
	case rootDispersion >= dispersionThreshold:
		return monitor.Result{Msg: fmt.Sprintf("Root dispersion %sms exceeds threshold %sms", fixed3(rootDispersion), jsNumber(dispersionThreshold))}
	}
	return monitor.Result{OK: true, Ping: &ping, Msg: msg}
}

func unwrapNetError(err error) string {
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr.Err != nil {
		return opErr.Err.Error()
	}
	return err.Error()
}

// runSNMP gets one OID, then compares its value as a JSON query does.
func runSNMP(ctx context.Context, m monitor.Monitor) monitor.Result {
	timeout := m.EffectiveTimeout()
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	client := &gosnmp.GoSNMP{
		Target:    m.Hostname,
		Port:      uint16(m.Port),
		Community: m.SNMPCommunity,
		Timeout:   timeout,
		Retries:   0,
		Context:   ctx,
		MaxOids:   1,
	}
	switch m.SNMPVersion {
	case "1":
		client.Version = gosnmp.Version1
	case "3":
		client.Version = gosnmp.Version3
		client.SecurityModel = gosnmp.UserSecurityModel
		client.MsgFlags = gosnmp.NoAuthNoPriv
		client.SecurityParameters = &gosnmp.UsmSecurityParameters{UserName: m.SNMPV3Username}
	default:
		client.Version = gosnmp.Version2c
	}
	start := time.Now()
	if err := client.Connect(); err != nil {
		return monitor.Result{Msg: "Error creating SNMP session: " + err.Error()}
	}
	defer func() { _ = client.Conn.Close() }()
	packet, err := client.Get([]string{m.SNMPOID})
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "timeout") {
			return monitor.Result{Msg: "Request timed out"}
		}
		return monitor.Result{Msg: err.Error()}
	}
	ping := elapsedMs(start)
	if len(packet.Variables) == 0 {
		return monitor.Result{Msg: fmt.Sprintf("No varbinds returned from SNMP session (OID: %s)", m.SNMPOID)}
	}
	variable := packet.Variables[0]
	switch variable.Type {
	case gosnmp.NoSuchInstance, gosnmp.NoSuchObject, gosnmp.EndOfMibView:
		return monitor.Result{Msg: fmt.Sprintf("The SNMP query returned that no instance exists for OID %s", m.SNMPOID)}
	}
	var value []byte
	switch v := variable.Value.(type) {
	case []byte:
		value = v
	case string:
		value = []byte(v)
	default:
		value = []byte(gosnmp.ToBigInt(v).String())
		if variable.Type == gosnmp.IPAddress || variable.Type == gosnmp.ObjectIdentifier {
			value, _ = json.Marshal(fmt.Sprint(v))
		}
	}
	result := evaluateJSONQuery(value, m)
	if result.OK {
		result.Ping = &ping
	}
	return result
}

// runSMTP connects and greets the server, with TLS as the monitor asks, as nodemailer's verify did.
func runSMTP(ctx context.Context, m monitor.Monitor) monitor.Result {
	fail := func(err error) monitor.Result {
		if ctx.Err() != nil || isTimeout(err) {
			return monitor.Result{Msg: "SMTP connection doesn't verify: Error: Connection timeout"}
		}
		return monitor.Result{Msg: "SMTP connection doesn't verify: Error: " + unwrapNetError(err)}
	}
	address := net.JoinHostPort(m.Hostname, strconv.Itoa(int(m.Port)))
	tlsConfig := &tls.Config{ServerName: m.Hostname, MinVersion: tls.VersionTLS12, InsecureSkipVerify: m.IgnoreTLS} //nolint:gosec // Per-monitor opt-in.
	start := time.Now()
	var conn net.Conn
	var err error
	dialer := &net.Dialer{}
	if m.SMTPSecurity == "secure" {
		conn, err = (&tls.Dialer{NetDialer: dialer, Config: tlsConfig}).DialContext(ctx, "tcp", address)
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return fail(err)
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	client, err := smtp.NewClient(conn, m.Hostname)
	if err != nil {
		return fail(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.Hello("uptime-kuma"); err != nil {
		return fail(err)
	}
	if m.SMTPSecurity != "secure" && m.SMTPSecurity != "nostarttls" {
		if ok, _ := client.Extension("STARTTLS"); ok {
			if err := client.StartTLS(tlsConfig); err != nil {
				return fail(err)
			}
		} else if m.SMTPSecurity == "starttls" {
			return monitor.Result{Msg: "SMTP connection doesn't verify: Error: No STARTTLS support"}
		}
	}
	_ = client.Quit()
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: "SMTP connection verifies successfully"}
}

// runSIP sends a SIP OPTIONS request over UDP, as sipsak did for the Node.js backend, and is UP
// on a 200 OK.
func runSIP(ctx context.Context, m monitor.Monitor) monitor.Result {
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", net.JoinHostPort(m.Hostname, strconv.Itoa(int(m.Port))))
	if err != nil {
		return monitor.Result{Msg: unwrapNetError(err)}
	}
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	random := make([]byte, 8)
	_, _ = rand.Read(random)
	tag := hex.EncodeToString(random)
	local := conn.LocalAddr().(*net.UDPAddr)
	host := net.JoinHostPort(local.IP.String(), strconv.Itoa(local.Port))
	target := fmt.Sprintf("sip:%s", net.JoinHostPort(m.Hostname, strconv.Itoa(int(m.Port))))
	request := strings.Join([]string{
		"OPTIONS " + target + " SIP/2.0",
		"Via: SIP/2.0/UDP " + host + ";branch=z9hG4bK" + tag[:10] + ";rport",
		"Max-Forwards: 70",
		"To: <" + target + ">",
		"From: <sip:sipsak@" + m.Hostname + ">;tag=" + tag[:8],
		"Call-ID: " + tag + "@" + local.IP.String(),
		"CSeq: 1 OPTIONS",
		"Contact: <sip:sipsak@" + host + ">",
		"Accept: application/sdp",
		"User-Agent: Uptime-Kuma",
		"Content-Length: 0",
		"", "",
	}, "\r\n")
	start := time.Now()
	if _, err := conn.Write([]byte(request)); err != nil {
		return monitor.Result{Msg: unwrapNetError(err)}
	}
	buffer := make([]byte, 65535)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			if isTimeout(err) || ctx.Err() != nil {
				return monitor.Result{Msg: "No answer to SIP OPTIONS"}
			}
			return monitor.Result{Msg: unwrapNetError(err)}
		}
		status, _, _ := strings.Cut(string(buffer[:n]), "\r\n")
		code := strings.Fields(status)
		if len(code) >= 2 && strings.HasPrefix(code[1], "1") {
			continue // Provisional, such as 100 Trying.
		}
		if strings.Contains(status, "200 OK") {
			ping := elapsedMs(start)
			return monitor.Result{OK: true, Ping: &ping, Msg: status}
		}
		return monitor.Result{Msg: status}
	}
}

// runRADIUS sends an Access-Request, and is UP on Access-Accept.
func runRADIUS(ctx context.Context, m monitor.Monitor) monitor.Result {
	packet := radius.New(radius.CodeAccessRequest, []byte(m.RadiusSecret))
	if err := rfc2865.UserName_SetString(packet, m.RadiusUsername); err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	if err := rfc2865.UserPassword_SetString(packet, m.RadiusPassword); err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	if m.RadiusCallingStationID != "" {
		_ = rfc2865.CallingStationID_SetString(packet, m.RadiusCallingStationID)
	}
	if m.RadiusCalledStationID != "" {
		_ = rfc2865.CalledStationID_SetString(packet, m.RadiusCalledStationID)
	}
	if err := signRADIUS(packet); err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	address := net.JoinHostPort(m.Hostname, strconv.Itoa(int(m.Port)))
	client := radius.Client{Retry: time.Second}
	start := time.Now()
	response, err := client.Exchange(ctx, packet, address)
	if err != nil {
		if ctx.Err() != nil {
			return monitor.Result{Msg: fmt.Sprintf("RADIUS authentication failed for %s: Timed out", address)}
		}
		return monitor.Result{Msg: fmt.Sprintf("RADIUS authentication failed for %s: %s", address, unwrapNetError(err))}
	}
	if response.Code != radius.CodeAccessAccept {
		return monitor.Result{Msg: fmt.Sprintf("RADIUS %s from %s", response.Code, address)}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: response.Code.String()}
}

// signRADIUS adds a Message-Authenticator, an HMAC-MD5 of the packet with the secret, which
// servers fixed for BlastRADIUS (CVE-2024-3596), such as FreeRADIUS 3.2.5 and later, require.
func signRADIUS(packet *radius.Packet) error {
	if err := rfc2869.MessageAuthenticator_Set(packet, make([]byte, md5.Size)); err != nil {
		return err
	}
	raw, err := packet.Encode()
	if err != nil {
		return err
	}
	mac := hmac.New(md5.New, packet.Secret)
	mac.Write(raw)
	return rfc2869.MessageAuthenticator_Set(packet, mac.Sum(nil))
}

// steamAPI is the Steam Web API's server list. Tests point it elsewhere.
var steamAPI = "https://api.steampowered.com/IGameServersService/GetServerList/v1/"

// SteamKey returns the Steam Web API key from the settings.
type SteamKey func(ctx context.Context) (string, error)

// SetSteamKey sets where Steam monitors find their API key. Call it before checks start.
func (c *Checker) SetSteamKey(key SteamKey) { c.steamKey = key }

// runSteam asks the Steam Web API whether it lists the game server, as the Node.js backend did.
func (c *Checker) runSteam(ctx context.Context, m monitor.Monitor) monitor.Result {
	key := ""
	if c.steamKey != nil {
		var err error
		if key, err = c.steamKey(ctx); err != nil {
			return monitor.Result{Msg: "Steam API Key not found"}
		}
	}
	if key == "" {
		return monitor.Result{Msg: "Steam API Key not found"}
	}
	address, err := pingTarget(ctx, m.Hostname)
	if err != nil {
		return monitor.Result{Msg: fmt.Sprintf("Unable to resolve Steam server hostname %q: %s", m.Hostname, unwrapNetError(err))}
	}
	query := url.Values{"filter": {fmt.Sprintf(`addr\%s:%d`, address.IP, m.Port)}, "key": {key}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, steamAPI+"?"+query.Encode(), nil)
	if err != nil {
		return monitor.Result{Msg: err.Error()}
	}
	req.Header.Set("Accept", "*/*")
	transport := c.verified
	if m.IgnoreTLS {
		transport = c.insecure
	}
	start := time.Now()
	res, err := (&http.Client{Transport: transport}).Do(req)
	if err != nil {
		// The URL has the key in it, so only the cause is shown.
		return monitor.Result{Msg: requestError(err, m.EffectiveTimeout())}
	}
	defer func() { _ = res.Body.Close() }()
	if !m.Accepts(res.StatusCode) {
		return monitor.Result{Msg: fmt.Sprintf("Request failed with status code %d", res.StatusCode)}
	}
	var body struct {
		Response struct {
			Servers []struct {
				Name string `json:"name"`
			} `json:"servers"`
		} `json:"response"`
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxResponseBytes))
	if err != nil || json.Unmarshal(data, &body) != nil || len(body.Response.Servers) == 0 {
		return monitor.Result{Msg: "Server not found on Steam"}
	}
	ping := elapsedMs(start)
	return monitor.Result{OK: true, Ping: &ping, Msg: body.Response.Servers[0].Name}
}
