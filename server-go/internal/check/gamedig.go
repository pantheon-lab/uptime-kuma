package check

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// The game servers that GameDig monitors can query come from the gamedig library's list
// (https://github.com/gamedig/node-gamedig, MIT), for the protocols ported here.
// gamedig-games.mjs makes it from that list.
//
//go:embed gamedig-games.json
var gamedigGamesJSON []byte

// Game is one game GameDig monitors can query.
type Game struct {
	Key             string `json:"key"`
	Name            string `json:"name"`
	Protocol        string `json:"protocol"`
	Port            int    `json:"port"`
	PortQuery       int    `json:"portQuery,omitempty"`
	PortQueryOffset []int  `json:"portQueryOffset,omitempty"`
}

var (
	gamesOnce sync.Once
	games     []Game
	gameByKey map[string]Game
)

// Games lists the games, by name.
func Games() []Game {
	gamesOnce.Do(func() {
		if err := json.Unmarshal(gamedigGamesJSON, &games); err != nil {
			panic(fmt.Sprintf("gamedig-games.json: %v", err))
		}
		gameByKey = make(map[string]Game, len(games))
		for _, g := range games {
			gameByKey[g.Key] = g
		}
	})
	return games
}

func init() {
	monitor.GameExists = func(key string) bool {
		_, found := FindGame(key)
		return found
	}
}

// FindGame returns a game by its key.
func FindGame(key string) (Game, bool) {
	Games()
	g, found := gameByKey[key]
	return g, found
}

const (
	gamedigPacketWait    = 2 * time.Second
	gamedigAttemptLimit  = 10 * time.Second
	gamedigMaxPacketSize = 64 << 10
)

// gameState is what a query finds out.
type gameState struct {
	name string
	ping time.Duration
}

// runGameDig queries a game server, trying the ports gamedig would, and is UP with the
// server's name, as in Node.js.
func runGameDig(ctx context.Context, m monitor.Monitor) monitor.Result {
	game, found := FindGame(m.Game)
	if !found {
		return monitor.Result{Msg: fmt.Sprintf("Game %q isn't supported", m.Game)}
	}
	host := strings.TrimSpace(m.Hostname)
	ports := gamedigPorts(game, int(m.Port), m.GamedigGivenPortOnly)
	var errs []string
	for i, port := range ports {
		attemptCtx, cancel := context.WithTimeout(ctx, gamedigAttemptLimit)
		if deadline, ok := ctx.Deadline(); ok {
			// Leave the remaining ports a share of the time.
			share := time.Until(deadline) / time.Duration(len(ports)-i)
			attemptCtx, cancel = context.WithTimeout(ctx, min(share, gamedigAttemptLimit))
		}
		state, err := queryGame(attemptCtx, game.Protocol, host, port)
		cancel()
		if err == nil {
			ping := float64(state.ping.Microseconds()) / 1000
			return monitor.Result{OK: true, Ping: &ping, Msg: state.name}
		}
		errs = append(errs, fmt.Sprintf("port %d: %v", port, err))
		if ctx.Err() != nil {
			break
		}
	}
	return monitor.Result{Msg: fmt.Sprintf("Failed all %d attempts: %s", len(errs), strings.Join(errs, "; "))}
}

// gamedigPorts are the ports gamedig's QueryRunner tries, in order.
func gamedigPorts(game Game, port int, givenOnly bool) []int {
	if port == 0 {
		port = game.Port
		if game.PortQuery != 0 {
			return []int{game.PortQuery}
		}
	}
	var ports []int
	add := func(p int) {
		if p > 0 && p < 65536 && !slices.Contains(ports, p) {
			ports = append(ports, p)
		}
	}
	if !givenOnly {
		offsets := game.PortQueryOffset
		if len(offsets) == 0 {
			offsets = []int{0}
		}
		for _, offset := range offsets {
			add(port + offset)
		}
		if port == game.Port && game.PortQuery != 0 {
			add(game.PortQuery)
		}
	}
	add(port)
	return ports
}

func queryGame(ctx context.Context, protocol, host string, port int) (gameState, error) {
	address := net.JoinHostPort(host, strconv.Itoa(port))
	switch protocol {
	case "valve":
		return queryValve(ctx, address)
	case "gamespy1":
		return queryGameSpy1(ctx, address)
	case "gamespy2":
		return queryGameSpy2(ctx, address)
	case "gamespy3":
		return queryGameSpy3(ctx, address)
	case "quake2":
		return queryQuake(ctx, address, "status", "print", false)
	case "quake3":
		return queryQuake(ctx, address, "getstatus", "statusResponse", true)
	case "ase":
		return queryASE(ctx, address)
	case "minecraft":
		return queryMinecraft(ctx, host, port)
	}
	return gameState{}, fmt.Errorf("protocol %s isn't supported", protocol)
}

// udpExchange sends request, and calls accept with each packet until it returns a result. It
// sends the request again if no packet comes for a while, and returns the time to the first
// packet.
func udpExchange[T any](ctx context.Context, address string, request []byte, accept func(packet []byte) (T, bool, error)) (T, time.Duration, error) {
	var zero T
	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "udp", address)
	if err != nil {
		return zero, 0, err
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Now()) })
	defer stop()

	buffer := make([]byte, gamedigMaxPacketSize)
	start := time.Now()
	var rtt time.Duration
	send := func(payload []byte) error {
		_, err := conn.Write(payload)
		return err
	}
	if err := send(request); err != nil {
		return zero, 0, err
	}
	for {
		_ = conn.SetReadDeadline(time.Now().Add(gamedigPacketWait))
		if ctx.Err() != nil {
			return zero, 0, errors.New("timed out")
		}
		n, err := conn.Read(buffer)
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() && ctx.Err() == nil {
			if rtt == 0 {
				// Nothing came back; UDP can lose the request.
				start = time.Now()
				if err := send(request); err != nil {
					return zero, 0, err
				}
			}
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return zero, 0, errors.New("timed out")
			}
			return zero, 0, err
		}
		if rtt == 0 {
			rtt = time.Since(start)
		}
		result, done, err := accept(slices.Clone(buffer[:n]))
		if err != nil {
			return zero, 0, err
		}
		if done {
			return result, rtt, nil
		}
	}
}

// cString reads a NUL-terminated string.
func cString(b []byte) (string, []byte) {
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return string(b), nil
	}
	return string(b[:i]), b[i+1:]
}

// latin1 decodes Latin-1 text, which the older protocols use.
func latin1(s string) string {
	runes := make([]rune, len(s))
	for i := 0; i < len(s); i++ {
		runes[i] = rune(s[i])
	}
	return string(runes)
}

var valvePrefix = []byte{0xff, 0xff, 0xff, 0xff}

// queryValve asks for A2S_INFO, answering a challenge if the server sends one.
func queryValve(ctx context.Context, address string) (gameState, error) {
	request := append(slices.Clone(valvePrefix), append([]byte{0x54}, "Source Engine Query\x00"...)...)
	for range 3 {
		type answer struct {
			info      []byte
			challenge []byte
		}
		got, rtt, err := udpExchange(ctx, address, request, func(p []byte) (answer, bool, error) {
			if len(p) < 5 || !bytes.Equal(p[:4], valvePrefix) {
				if len(p) >= 4 && binary.LittleEndian.Uint32(p) == 0xfffffffe {
					return answer{}, false, errors.New("the server sent a split answer, which isn't supported")
				}
				return answer{}, false, nil
			}
			switch p[4] {
			case 0x41:
				if len(p) < 9 {
					return answer{}, false, nil
				}
				return answer{challenge: p[5:9]}, true, nil
			case 0x49, 0x6d:
				return answer{info: p[4:]}, true, nil
			}
			return answer{}, false, nil
		})
		if err != nil {
			return gameState{}, err
		}
		if got.challenge != nil {
			request = append(request[:25], got.challenge...)
			continue
		}
		return gameState{name: valveName(got.info), ping: rtt}, nil
	}
	return gameState{}, errors.New("Received too many challenge key responses") //nolint:staticcheck // As in Node.js.
}

// valveName reads the server name of an info answer: after the protocol byte for Source, and
// after the address for the old GoldSrc answer.
func valveName(info []byte) string {
	rest := info[1:]
	if info[0] == 0x49 {
		if len(rest) == 0 {
			return ""
		}
		rest = rest[1:]
	} else {
		_, rest = cString(rest)
	}
	name, _ := cString(rest)
	return name
}

// queryGameSpy1 sends \status\ and puts together the answer's parts.
func queryGameSpy1(ctx context.Context, address string) (gameState, error) {
	values := map[string]string{}
	parts := map[int]bool{}
	finalPart := 0
	queryID := ""
	_, rtt, err := udpExchange(ctx, address, []byte(`\status\xserverquery`), func(p []byte) (struct{}, bool, error) {
		fields := strings.Split(latin1(string(p)), `\`)
		if len(fields) > 0 {
			fields = fields[1:]
		}
		data := map[string]string{}
		for i := 0; i < len(fields); i += 2 {
			value := ""
			if i+1 < len(fields) {
				value = fields[i+1]
			}
			data[fields[i]] = value
		}
		_, final := data["final"]
		partNum := 0
		if id, found := data["queryid"]; found {
			id, part, _ := strings.Cut(id, ".")
			if queryID != "" && queryID != id {
				return struct{}{}, false, nil
			}
			queryID = id
			partNum, _ = strconv.Atoi(part)
		}
		delete(data, "final")
		delete(data, "queryid")
		if partNum == 0 {
			partNum = len(parts)
		}
		if parts[partNum] {
			return struct{}{}, false, nil
		}
		parts[partNum] = true
		if final {
			finalPart = partNum
		}
		for k, v := range data {
			values[strings.ToLower(k)] = v
		}
		return struct{}{}, finalPart != 0 && len(parts) == finalPart || final && finalPart == 0, nil
	})
	if err != nil {
		return gameState{}, err
	}
	return gameState{name: values["hostname"], ping: rtt}, nil
}

// queryGameSpy2 asks for the server's rules, which hold its name.
func queryGameSpy2(ctx context.Context, address string) (gameState, error) {
	request := []byte{0xfe, 0xfd, 0x00, 0x00, 0x00, 0x00, 0x01, 0xff, 0x00, 0x00}
	body, rtt, err := udpExchange(ctx, address, request, func(p []byte) ([]byte, bool, error) {
		if len(p) < 5 || p[0] != 0 || binary.BigEndian.Uint32(p[1:5]) != 1 {
			return nil, false, nil
		}
		return p[5:], true, nil
	})
	if err != nil {
		return gameState{}, err
	}
	values := keyValues(body)
	return gameState{name: latin1(values["hostname"]), ping: rtt}, nil
}

// keyValues reads NUL-separated keys and values until an empty key.
func keyValues(b []byte) map[string]string {
	values := map[string]string{}
	for len(b) > 0 {
		var key, value string
		key, b = cString(b)
		if key == "" {
			break
		}
		value, b = cString(b)
		values[key] = value
	}
	return values
}

// queryGameSpy3 gets a challenge, then the server's information, which may come in parts.
func queryGameSpy3(ctx context.Context, address string) (gameState, error) {
	const session = 1
	header := func(kind byte) []byte {
		b := []byte{0xfe, 0xfd, kind, 0, 0, 0, 0}
		binary.BigEndian.PutUint32(b[3:], session)
		return b
	}
	matches := func(p []byte, kind byte) bool {
		return len(p) >= 5 && p[0] == kind && binary.BigEndian.Uint32(p[1:5]) == session
	}
	challengeText, rtt, err := udpExchange(ctx, address, header(9), func(p []byte) (string, bool, error) {
		if !matches(p, 9) {
			return "", false, nil
		}
		text, _ := cString(p[5:])
		return text, true, nil
	})
	if err != nil {
		return gameState{}, err
	}
	request := header(0)
	if challenge, _ := strconv.ParseInt(strings.TrimSpace(challengeText), 10, 64); challenge != 0 {
		request = binary.BigEndian.AppendUint32(request, uint32(int32(challenge)))
	}
	request = append(request, 0xff, 0xff, 0xff, 0x01)
	parts := map[int][]byte{}
	count := 0
	body, _, err := udpExchange(ctx, address, request, func(p []byte) ([]byte, bool, error) {
		if !matches(p, 0) || len(p) < 16 {
			return nil, false, nil
		}
		// "splitnum\0", then the part's number, with the high bit on the last one.
		id := int(p[14] & 0x7f)
		if p[14]&0x80 != 0 {
			count = id + 1
		}
		parts[id] = p[16:]
		if count == 0 || len(parts) != count {
			return nil, false, nil
		}
		return parts[0], true, nil
	})
	if err != nil {
		return gameState{}, err
	}
	values := keyValues(body)
	name := values["hostname"]
	if name == "" {
		name = values["servername"]
	}
	return gameState{name: latin1(name), ping: rtt}, nil
}

var quake3Colors = regexp.MustCompile(`\^(X.{6}|.)`)

// queryQuake asks for the status of a Quake 2 or Quake 3 server.
func queryQuake(ctx context.Context, address, send, expect string, stripColors bool) (gameState, error) {
	request := append(slices.Clone(valvePrefix), send+"\x00"...)
	body, rtt, err := udpExchange(ctx, address, request, func(p []byte) ([]byte, bool, error) {
		if len(p) < 4 || !bytes.Equal(p[:4], valvePrefix) {
			return nil, false, nil
		}
		rest := p[4:]
		i := bytes.IndexAny(rest, "\n\x00")
		if i < 0 || string(rest[:i]) != expect {
			return nil, false, nil
		}
		return rest[i+1:], true, nil
	})
	if err != nil {
		return gameState{}, err
	}
	line, _, _ := strings.Cut(latin1(string(body)), "\n")
	fields := strings.Split(line, `\`)
	if len(fields) > 0 && fields[0] == "" {
		fields = fields[1:]
	}
	values := map[string]string{}
	for i := 0; i+1 < len(fields); i += 2 {
		values[fields[i]] = fields[i+1]
	}
	name := values["sv_hostname"]
	if values["hostname"] != "" {
		name = values["hostname"]
	}
	if stripColors {
		name = quake3Colors.ReplaceAllString(name, "")
	}
	return gameState{name: name, ping: rtt}, nil
}

// queryASE asks an All-Seeing Eye server for its information.
func queryASE(ctx context.Context, address string) (gameState, error) {
	body, rtt, err := udpExchange(ctx, address, []byte("s"), func(p []byte) ([]byte, bool, error) {
		if len(p) < 4 || string(p[:4]) != "EYE1" {
			return nil, false, nil
		}
		return p[4:], true, nil
	})
	if err != nil {
		return gameState{}, err
	}
	// Strings carry their length plus one in front: the game, the port, then the name.
	var fields []string
	for len(fields) < 3 && len(body) > 0 {
		n := int(body[0]) - 1
		if n < 0 || n+1 > len(body) {
			break
		}
		fields = append(fields, string(body[1:1+n]))
		body = body[1+n:]
	}
	if len(fields) < 3 {
		return gameState{}, errors.New("the server's answer is too short")
	}
	return gameState{name: latin1(fields[2]), ping: rtt}, nil
}

// queryMinecraft asks a Java server for its status, and tries Bedrock's ping and GameSpy 3 at
// the same time, as gamedig does. The Java answer's name wins.
func queryMinecraft(ctx context.Context, host string, port int) (gameState, error) {
	type answer struct {
		state gameState
		err   error
	}
	java, bedrock, gamespy := make(chan answer, 1), make(chan answer, 1), make(chan answer, 1)
	go func() { s, err := queryMinecraftJava(ctx, host, port); java <- answer{s, err} }()
	go func() { s, err := queryMinecraftBedrock(ctx, host, port); bedrock <- answer{s, err} }()
	go func() {
		s, err := queryGameSpy3(ctx, net.JoinHostPort(host, strconv.Itoa(port)))
		gamespy <- answer{s, err}
	}()
	// The Java answer wins, so the others are only used once Java has failed.
	var javaErr, bedrockErr, gamespyErr error
	var fallback *gameState
	for pending := 3; pending > 0; pending-- {
		select {
		case r := <-java:
			if r.err == nil {
				return r.state, nil
			}
			javaErr = r.err
		case r := <-bedrock:
			if bedrockErr = r.err; r.err == nil && fallback == nil {
				fallback = &r.state
			}
		case r := <-gamespy:
			if gamespyErr = r.err; r.err == nil && fallback == nil {
				fallback = &r.state
			}
		}
		if javaErr != nil && fallback != nil {
			return *fallback, nil
		}
	}
	return gameState{}, fmt.Errorf("No protocols succeeded (Java: %v; Bedrock: %v; GameSpy 3: %v)", //nolint:staticcheck // As in Node.js.
		javaErr, bedrockErr, gamespyErr)
}

func appendVarInt(b []byte, v int) []byte {
	u := uint32(v)
	for u >= 0x80 {
		b = append(b, byte(u)|0x80)
		u >>= 7
	}
	return append(b, byte(u))
}

func readVarInt(r io.ByteReader) (int, error) {
	var value uint32
	for shift := uint(0); shift < 35; shift += 7 {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		value |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return int(int32(value)), nil
		}
	}
	return 0, errors.New("varint too long")
}

func minecraftPacket(id int, data []byte) []byte {
	body := appendVarInt(nil, id)
	body = append(body, data...)
	return append(appendVarInt(nil, len(body)), body...)
}

// queryMinecraftJava uses the Server List Ping of Java servers.
func queryMinecraftJava(ctx context.Context, host string, port int) (gameState, error) {
	var dialer net.Dialer
	start := time.Now()
	conn, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(host, strconv.Itoa(port)))
	if err != nil {
		return gameState{}, err
	}
	rtt := time.Since(start)
	defer func() { _ = conn.Close() }()
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	handshake := appendVarInt(nil, 47)
	handshake = appendVarInt(handshake, len(host))
	handshake = append(handshake, host...)
	handshake = binary.BigEndian.AppendUint16(handshake, uint16(port))
	handshake = appendVarInt(handshake, 1)
	if _, err := conn.Write(append(minecraftPacket(0, handshake), minecraftPacket(0, nil)...)); err != nil {
		return gameState{}, err
	}
	reader := &byteReader{r: conn}
	length, err := readVarInt(reader)
	if err != nil {
		return gameState{}, err
	}
	if length <= 0 || length > 1<<21 {
		return gameState{}, errors.New("invalid status packet")
	}
	packet := make([]byte, length)
	if _, err := io.ReadFull(conn, packet); err != nil {
		return gameState{}, err
	}
	body := bytes.NewReader(packet)
	if _, err := readVarInt(body); err != nil {
		return gameState{}, err
	}
	size, err := readVarInt(body)
	if err != nil || size < 0 || size > body.Len() {
		return gameState{}, errors.New("invalid status JSON")
	}
	text := make([]byte, size)
	_, _ = io.ReadFull(body, text)
	var status struct {
		Description json.RawMessage `json:"description"`
	}
	if err := json.Unmarshal(text, &status); err != nil {
		return gameState{}, fmt.Errorf("invalid status JSON: %w", err)
	}
	return gameState{name: minecraftText(status.Description), ping: rtt}, nil
}

// byteReader reads one byte at a time from a connection, for the length in front of a packet.
type byteReader struct{ r io.Reader }

func (b *byteReader) ReadByte() (byte, error) {
	var one [1]byte
	_, err := io.ReadFull(b.r, one[:])
	return one[0], err
}

// minecraftText flattens a description, which is text or a chat component, as gamedig does.
func minecraftText(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var component struct {
		Text  string            `json:"text"`
		Extra []json.RawMessage `json:"extra"`
	}
	if json.Unmarshal(raw, &component) != nil {
		return ""
	}
	var out strings.Builder
	out.WriteString(component.Text)
	for _, extra := range component.Extra {
		out.WriteString(minecraftText(extra))
	}
	return out.String()
}

var raknetMagic = []byte{0x00, 0xff, 0xff, 0x00, 0xfe, 0xfe, 0xfe, 0xfe, 0xfd, 0xfd, 0xfd, 0xfd, 0x12, 0x34, 0x56, 0x78}

// queryMinecraftBedrock sends RakNet's unconnected ping, whose answer names the server.
func queryMinecraftBedrock(ctx context.Context, host string, port int) (gameState, error) {
	request := []byte{0x01}
	request = binary.BigEndian.AppendUint64(request, uint64(time.Now().UnixMilli()))
	request = append(request, raknetMagic...)
	request = binary.BigEndian.AppendUint64(request, 0x4b756d61) // "Kuma"
	motd, rtt, err := udpExchange(ctx, net.JoinHostPort(host, strconv.Itoa(port)), request, func(p []byte) (string, bool, error) {
		if len(p) < 35 || p[0] != 0x1c || !bytes.Equal(p[17:33], raknetMagic) {
			return "", false, nil
		}
		n := int(binary.BigEndian.Uint16(p[33:35]))
		if 35+n > len(p) {
			return "", false, nil
		}
		return string(p[35 : 35+n]), true, nil
	})
	if err != nil {
		return gameState{}, err
	}
	// MCPE;name;protocol;version;players;max players;...
	fields := strings.Split(motd, ";")
	if len(fields) < 2 {
		return gameState{}, errors.New("invalid Bedrock answer")
	}
	return gameState{name: fields[1], ping: rtt}, nil
}
