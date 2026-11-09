package ui

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// The realtime protocol, as src/realtime-client.ts speaks it:
//
//   - GET /api/realtime/stream is a Server-Sent Events stream. It starts with an "open"
//     event holding the connection id. "message" events hold [event, ...args], and "ack"
//     events hold [ackId, ...reply]. A comment line keeps the connection alive.
//   - POST /api/realtime/send takes {id, messages: [{event, args, ack?}]} and answers 202,
//     or 410 when the stream is gone, which makes the client reconnect.
//
// A connection's events run one at a time, in the order they arrived.
const (
	keepAliveInterval = 25 * time.Second
	writeTimeout      = 30 * time.Second
	handlerTimeout    = 30 * time.Second
	// A client this far behind is dropped. It reloads everything when it reconnects.
	outBuffer     = 4096
	inboxBuffer   = 256
	maxSendBody   = 32 << 20
	maxBatchCount = 1000
	// sessionCheckInterval is how often an open stream checks that its login is still valid:
	// that the session hasn't expired or ended, and, for a GitHub account, its teams when due.
	sessionCheckInterval = 5 * time.Minute
)

type inMessage struct {
	Event string            `json:"event"`
	Args  []json.RawMessage `json:"args"`
	Ack   *int64            `json:"ack"`
}

type conn struct {
	id     string
	out    chan []byte
	inbox  chan []inMessage
	done   chan struct{}
	once   sync.Once
	ctx    context.Context
	cancel context.CancelFunc

	// Set at login. Read under Server.mu by broadcasts.
	userID    int32
	username  string
	tokenHash []byte
	// admin is false for a viewer, who may only read and is sent no credentials.
	admin bool

	rendered sync.Once
}

func (c *conn) close() {
	c.once.Do(func() {
		close(c.done)
		c.cancel()
	})
}

// push queues an encoded frame, or drops a connection that has fallen too far behind.
func (c *conn) push(frame []byte) {
	select {
	case <-c.done:
	case c.out <- frame:
	default:
		c.close()
	}
}

func (c *conn) emit(event string, args ...any) {
	c.push(encodeMessage(event, args...))
}

// loggedIn reports whether the connection has logged in. Call it with Server.mu held, or
// from the connection's own goroutine, which is the only writer.
func (c *conn) loggedIn() bool { return c.userID != 0 }

func encodeFrame(event string, payload []byte) []byte {
	var b bytes.Buffer
	b.Grow(len(payload) + len(event) + 16)
	b.WriteString("event: ")
	b.WriteString(event)
	b.WriteString("\ndata: ")
	b.Write(payload)
	b.WriteString("\n\n")
	return b.Bytes()
}

func encodeMessage(event string, args ...any) []byte {
	payload, err := json.Marshal(append([]any{event}, args...))
	if err != nil {
		// Only values built in this package are sent, so this is a bug.
		panic("encode realtime message " + event + ": " + err.Error())
	}
	return encodeFrame("message", payload)
}

func newConnID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Server) stream(w http.ResponseWriter, r *http.Request) {
	// Before any output, so that a renewed session cookie can still be set.
	_, user, loggedIn, err := s.currentSession(r.Context(), w, r)
	if err != nil {
		s.log.Error("realtime session", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	var login *userInfo
	if loggedIn {
		cookie, _ := r.Cookie(sessionCookie)
		login = &userInfo{id: user.ID, username: user.Username, tokenHash: hashToken(cookie.Value), admin: user.IsAdmin()}
	}

	rc := http.NewResponseController(w)
	// The server's read and write timeouts are for ordinary requests. A stream sets a
	// deadline per write instead.
	_ = rc.SetReadDeadline(time.Time{})
	header := w.Header()
	header.Set("Content-Type", "text/event-stream")
	header.Set("Cache-Control", "no-store, no-transform")
	header.Set("X-Accel-Buffering", "no")
	var out io.Writer = w
	var gz *gzip.Writer
	// The monitor list can be megabytes of JSON, and compresses well.
	if acceptsGzip(r.Header.Get("Accept-Encoding")) {
		header.Set("Content-Encoding", "gzip")
		header.Add("Vary", "Accept-Encoding")
		gz, _ = gzip.NewWriterLevel(w, gzip.BestSpeed)
		out = gz
	}

	ctx, cancel := context.WithCancel(context.WithoutCancel(r.Context()))
	c := &conn{
		id:     newConnID(),
		out:    make(chan []byte, outBuffer),
		inbox:  make(chan []inMessage, inboxBuffer),
		done:   make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
	}
	defer c.close()

	write := func(frame []byte) error {
		_ = rc.SetWriteDeadline(time.Now().Add(writeTimeout))
		if _, err := out.Write(frame); err != nil {
			return err
		}
		if gz != nil {
			if err := gz.Flush(); err != nil {
				return err
			}
		}
		return rc.Flush()
	}

	// Registered before the client hears of it, because its first send can follow at once.
	s.mu.Lock()
	s.conns[c.id] = c
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.conns, c.id)
		s.mu.Unlock()
	}()

	open, _ := json.Marshal(map[string]string{"id": c.id})
	w.WriteHeader(http.StatusOK)
	if err := write(encodeFrame("open", open)); err != nil {
		return
	}

	go s.process(c, login)
	if login != nil {
		go s.watchSession(c, login)
	}

	keepAlive := time.NewTicker(keepAliveInterval)
	defer keepAlive.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case <-c.done:
			return
		case <-keepAlive.C:
			if write([]byte(": keep-alive\n\n")) != nil {
				return
			}
		case frame := <-c.out:
			if write(frame) != nil {
				return
			}
		}
	}
}

// watchSession closes a logged-in stream once its login is no longer valid, or its role
// changed: the client reconnects, and gets what its login allows now. A stream doesn't
// otherwise make requests that would check, as its events only name the connection.
func (s *Server) watchSession(c *conn, login *userInfo) {
	ticker := time.NewTicker(sessionCheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.done:
			return
		case <-ticker.C:
			if !s.loginStillValid(c.ctx, login) {
				c.close()
				return
			}
		}
	}
}

func (s *Server) loginStillValid(ctx context.Context, login *userInfo) bool {
	ctx, cancel := context.WithTimeout(ctx, handlerTimeout)
	defer cancel()
	_, user, err := s.store.FindSession(ctx, login.tokenHash)
	if errors.Is(err, store.ErrNotFound) {
		return false
	}
	if err != nil {
		// Keep the stream; the next check tries again.
		s.log.Warn("check realtime session", "error", err)
		return true
	}
	user, allowed, err := s.checkGitHubAccount(ctx, user)
	if err != nil {
		s.log.Warn("check realtime session", "error", err)
		return true
	}
	return allowed && user.IsAdmin() == login.admin
}

// acceptsGzip reports whether an Accept-Encoding header allows gzip, ignoring q=0.
func acceptsGzip(header string) bool {
	for _, part := range strings.Split(header, ",") {
		token, params, _ := strings.Cut(part, ";")
		if !strings.EqualFold(strings.TrimSpace(token), "gzip") {
			continue
		}
		for _, param := range strings.Split(params, ";") {
			key, value, _ := strings.Cut(strings.TrimSpace(param), "=")
			if strings.EqualFold(key, "q") {
				q, err := strconv.ParseFloat(value, 64)
				return err == nil && q > 0
			}
		}
		return true
	}
	return false
}

func (s *Server) send(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ID       string      `json:"id"`
		Messages []inMessage `json:"messages"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSendBody)).Decode(&body); err != nil ||
		body.ID == "" || body.Messages == nil || len(body.Messages) > maxBatchCount {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	for _, m := range body.Messages {
		if m.Event == "" || m.Args == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
	}
	s.mu.Lock()
	c := s.conns[body.ID]
	s.mu.Unlock()
	if c == nil {
		w.WriteHeader(http.StatusGone)
		return
	}
	select {
	case c.inbox <- body.Messages:
		w.WriteHeader(http.StatusAccepted)
	case <-c.done:
		w.WriteHeader(http.StatusGone)
	default:
		// The connection can't keep up; it's dropped and the client reconnects.
		c.close()
		w.WriteHeader(http.StatusGone)
	}
}

// userInfo is the login a stream was opened with.
type userInfo struct {
	id        int32
	username  string
	tokenHash []byte
	admin     bool
}

// setLogin marks a connection as logged in, so that broadcasts reach it.
func (s *Server) setLogin(c *conn, login *userInfo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c.userID, c.username, c.tokenHash, c.admin = login.id, login.username, login.tokenHash, login.admin
}

// process runs a connection's start-up, then its events in order.
func (s *Server) process(c *conn, login *userInfo) {
	defer func() {
		if r := recover(); r != nil {
			s.log.Error("realtime connection panicked", "panic", r)
			c.close()
		}
	}()
	s.onConnect(c, login)
	for {
		select {
		case <-c.done:
			return
		case batch := <-c.inbox:
			for _, m := range batch {
				s.dispatch(c, m)
			}
		}
	}
}

func (s *Server) dispatch(c *conn, m inMessage) {
	ctx, cancel := context.WithTimeout(c.ctx, handlerTimeout)
	defer cancel()
	reply, hasReply := s.handle(ctx, c, m.Event, m.Args)
	if m.Ack == nil {
		return
	}
	if !hasReply {
		reply = notSupported(m.Event)
	}
	payload, err := json.Marshal([]any{*m.Ack, reply})
	if err != nil {
		s.log.Error("encode reply", "event", m.Event, "error", err)
		payload, _ = json.Marshal([]any{*m.Ack, fail(errors.New("internal error"))})
	}
	c.push(encodeFrame("ack", payload))
}

// Close ends every stream, which graceful shutdown would otherwise wait for.
func (s *Server) Close() {
	s.stopDeliveries()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		c.close()
	}
}

// broadcast sends a frame to every logged-in connection.
func (s *Server) broadcast(frame []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.loggedIn() {
			c.push(frame)
		}
	}
}

// closeSession disconnects the streams of a login that ended.
func (s *Server) closeSession(tokenHash []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.tokenHash != nil && bytes.Equal(c.tokenHash, tokenHash) {
			c.close()
		}
	}
}

// closeOtherSessions disconnects a user's streams except those of the session keep.
func (s *Server) closeOtherSessions(userID int32, keep []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.userID == userID && !bytes.Equal(c.tokenHash, keep) {
			c.close()
		}
	}
}
