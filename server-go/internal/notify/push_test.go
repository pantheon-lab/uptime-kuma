package notify

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip59"
)

var b64 = base64.RawURLEncoding

// decryptWebPush undoes RFC 8291's aes128gcm encryption, as a browser does.
func decryptWebPush(t *testing.T, body []byte, ua *ecdh.PrivateKey, authSecret []byte) []byte {
	t.Helper()
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	serverKey, ciphertext := body[21:21+idlen], body[21+idlen:]
	if rs < 18 {
		t.Fatalf("record size %d", rs)
	}
	as, err := ecdh.P256().NewPublicKey(serverKey)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := ua.ECDH(as)
	if err != nil {
		t.Fatal(err)
	}
	info := append(append([]byte("WebPush: info\x00"), ua.PublicKey().Bytes()...), serverKey...)
	prkKey, _ := hkdf.Extract(sha256.New, secret, authSecret)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(info), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	block, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(block)
	plain, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	plain = []byte(strings.TrimRight(string(plain), "\x00"))
	if len(plain) == 0 || plain[len(plain)-1] != 2 {
		t.Fatalf("no last-record delimiter in %q", plain)
	}
	return plain[:len(plain)-1]
}

// checkVAPID verifies the VAPID token, as a push service does.
func checkVAPID(t *testing.T, header, public, audience string) {
	t.Helper()
	token, key, ok := strings.Cut(strings.TrimPrefix(header, "vapid t="), ", k=")
	if !ok || key != public {
		t.Fatalf("authorization %q", header)
	}
	parts := strings.Split(token, ".")
	raw, _ := b64.DecodeString(public)
	pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), raw)
	if err != nil {
		t.Fatal(err)
	}
	sig, _ := b64.DecodeString(parts[2])
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if len(sig) != 64 || !ecdsa.Verify(pub, digest[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		t.Fatal("the VAPID signature doesn't verify")
	}
	claims, _ := b64.DecodeString(parts[1])
	var c struct{ Aud, Sub string }
	_ = json.Unmarshal(claims, &c)
	if c.Aud != audience || c.Sub != webpushSubject {
		t.Fatalf("claims %s", claims)
	}
}

func TestWebpush(t *testing.T) {
	private, public, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		t.Fatal(err)
	}
	ua, _ := ecdh.P256().GenerateKey(rand.Reader)
	authSecret := make([]byte, 16)
	_, _ = rand.Read(authSecret)

	var got struct {
		sync.Mutex
		body          []byte
		auth, ttl     string
		encoding, urg string
	}
	status := http.StatusCreated
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got.Lock()
		got.body, got.auth, got.ttl, got.encoding, got.urg = body, r.Header.Get("Authorization"), r.Header.Get("TTL"),
			r.Header.Get("Content-Encoding"), r.Header.Get("Urgency")
		got.Unlock()
		w.WriteHeader(status)
	}))
	defer service.Close()

	env := DefaultEnv("")
	env.WebpushKeys = func(context.Context) (string, string, error) { return public, private, nil }
	config := M{"type": "Webpush", "subscription": M{
		"endpoint": service.URL + "/push/abc", "expirationTime": nil,
		"keys": M{"p256dh": b64.EncodeToString(ua.PublicKey().Bytes()), "auth": b64.EncodeToString(authSecret)},
	}}
	if err := Send(context.Background(), env, "Webpush", config, Message{Text: "Site is down"}); err != nil {
		t.Fatal(err)
	}
	var message struct{ Title, Body string }
	if err := json.Unmarshal(decryptWebPush(t, got.body, ua, authSecret), &message); err != nil {
		t.Fatal(err)
	}
	if message.Title != "Uptime Kuma" || message.Body != "Site is down" {
		t.Fatalf("message %+v", message)
	}
	if got.encoding != "aes128gcm" || got.ttl != "2419200" || got.urg != "high" {
		t.Fatalf("headers: encoding %q, TTL %q, urgency %q", got.encoding, got.ttl, got.urg)
	}
	checkVAPID(t, got.auth, public, service.URL)

	status = http.StatusGone
	if err := Send(context.Background(), env, "Webpush", config, Message{Text: "x"}); err == nil || !strings.Contains(err.Error(), "subscription has ended") {
		t.Fatalf("a gone subscription: %v", err)
	}
	if err := Send(context.Background(), env, "Webpush", M{"type": "Webpush"}, Message{Text: "x"}); err == nil || !strings.Contains(err.Error(), "isn't subscribed") {
		t.Fatalf("no subscription: %v", err)
	}
	if err := Send(context.Background(), DefaultEnv(""), "Webpush", config, Message{Text: "x"}); err == nil {
		t.Fatal("sent without keys")
	}
}

// fakeRelay requires NIP-42 authentication before it takes events, as many DM relays do.
type fakeRelay struct {
	mu       sync.Mutex
	events   []nostr.Event
	authedAs string
}

func (f *fakeRelay) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	ctx := r.Context()
	send := func(v ...any) {
		data, _ := json.Marshal(v)
		_ = conn.Write(ctx, websocket.MessageText, data)
	}
	send("AUTH", "challenge-123")
	authed := false
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
			continue
		}
		var kind string
		_ = json.Unmarshal(msg[0], &kind)
		var event nostr.Event
		if err := json.Unmarshal(msg[1], &event); err != nil {
			continue
		}
		valid, _ := event.CheckSignature()
		switch kind {
		case "AUTH":
			if valid && event.Kind == nostr.KindClientAuthentication && event.Tags.GetFirst([]string{"challenge", "challenge-123"}) != nil {
				authed = true
				f.mu.Lock()
				f.authedAs = event.PubKey
				f.mu.Unlock()
				send("OK", event.ID, true, "")
			} else {
				send("OK", event.ID, false, "invalid: bad auth")
			}
		case "EVENT":
			switch {
			case !authed:
				send("OK", event.ID, false, "auth-required: this relay needs you to log in")
			case !valid:
				send("OK", event.ID, false, "invalid: bad signature")
			default:
				f.mu.Lock()
				f.events = append(f.events, event)
				f.mu.Unlock()
				send("OK", event.ID, true, "")
			}
		}
	}
}

func TestNostr(t *testing.T) {
	relay := &fakeRelay{}
	server := httptest.NewServer(relay)
	defer server.Close()
	relayURL := "ws" + strings.TrimPrefix(server.URL, "http")

	senderKey := nostr.GeneratePrivateKey()
	senderPub, _ := nostr.GetPublicKey(senderKey)
	nsec, _ := nip19.EncodePrivateKey(senderKey)
	var recipientKeys []string
	var npubs []string
	for range 2 {
		key := nostr.GeneratePrivateKey()
		pub, _ := nostr.GetPublicKey(key)
		npub, _ := nip19.EncodePublicKey(pub)
		recipientKeys = append(recipientKeys, key)
		npubs = append(npubs, npub)
	}
	config := M{
		"type": "nostr", "sender": nsec,
		"recipients": strings.Join(npubs, "\n") + "\n",
		// One relay is down, which a message survives.
		"relays": relayURL + "\nws://127.0.0.1:1\n",
	}
	if err := Send(context.Background(), DefaultEnv(""), "nostr", config, Message{Text: "Site is down"}); err != nil {
		t.Fatal(err)
	}
	relay.mu.Lock()
	events, authedAs := relay.events, relay.authedAs
	relay.mu.Unlock()
	if len(events) != 2 || authedAs != senderPub {
		t.Fatalf("%d events, authenticated as %s", len(events), authedAs)
	}
	for i, key := range recipientKeys {
		recipient, _ := keyer.NewPlainKeySigner(key)
		recipientPub, _ := recipient.GetPublicKey(context.Background())
		wrap := events[i]
		if wrap.Kind != nostr.KindGiftWrap || wrap.PubKey == senderPub || wrap.Tags.GetFirst([]string{"p", recipientPub}) == nil {
			t.Fatalf("gift wrap %d: kind %d, pubkey %s, tags %v", i, wrap.Kind, wrap.PubKey, wrap.Tags)
		}
		rumor, err := nip59.GiftUnwrap(wrap, func(other, ciphertext string) (string, error) {
			return recipient.Decrypt(context.Background(), ciphertext, other)
		})
		if err != nil {
			t.Fatalf("unwrap %d: %v", i, err)
		}
		if rumor.Kind != nostr.KindDirectMessage || rumor.Content != "Site is down" || rumor.PubKey != senderPub ||
			rumor.Tags.GetFirst([]string{"p", recipientPub}) == nil {
			t.Fatalf("rumor %d: %+v", i, rumor)
		}
	}

	for name, bad := range map[string]M{
		"npub as sender":   {"sender": npubs[0], "recipients": npubs[0], "relays": relayURL},
		"nsec as receiver": {"sender": nsec, "recipients": nsec, "relays": relayURL},
		"no relays":        {"sender": nsec, "recipients": npubs[0], "relays": ""},
		"relays all down":  {"sender": nsec, "recipients": npubs[0], "relays": "ws://127.0.0.1:1"},
	} {
		if err := Send(context.Background(), DefaultEnv(""), "nostr", bad, Message{Text: "x"}); err == nil {
			t.Errorf("%s: sent", name)
		}
	}
}

// TestNostrRealRelay sends to real relay software, such as nostr-rs-relay in Docker, then reads
// the message back as the recipient. TEST_NOSTR_RELAY is its ws:// address.
func TestNostrRealRelay(t *testing.T) {
	relayURL := os.Getenv("TEST_NOSTR_RELAY")
	if relayURL == "" {
		t.Skip("TEST_NOSTR_RELAY is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	senderKey, recipientKey := nostr.GeneratePrivateKey(), nostr.GeneratePrivateKey()
	senderPub, _ := nostr.GetPublicKey(senderKey)
	recipientPub, _ := nostr.GetPublicKey(recipientKey)
	nsec, _ := nip19.EncodePrivateKey(senderKey)
	npub, _ := nip19.EncodePublicKey(recipientPub)
	text := "Real relay test " + nostr.GeneratePrivateKey()[:8]
	if err := Send(ctx, DefaultEnv(""), "nostr", M{"sender": nsec, "recipients": npub, "relays": relayURL}, Message{Text: text}); err != nil {
		t.Fatal(err)
	}

	conn, _, err := websocket.Dial(ctx, relayURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	req, _ := json.Marshal([]any{"REQ", "check", M{"kinds": []int{nostr.KindGiftWrap}, "#p": []string{recipientPub}}})
	if err := conn.Write(ctx, websocket.MessageText, req); err != nil {
		t.Fatal(err)
	}
	recipient, _ := keyer.NewPlainKeySigner(recipientKey)
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("no message came back: %v", err)
		}
		var msg []json.RawMessage
		_ = json.Unmarshal(data, &msg)
		var label string
		_ = json.Unmarshal(msg[0], &label)
		if label == "EOSE" {
			t.Fatal("the relay has no message for the recipient")
		}
		if label != "EVENT" || len(msg) < 3 {
			continue
		}
		var wrap nostr.Event
		if err := json.Unmarshal(msg[2], &wrap); err != nil {
			t.Fatal(err)
		}
		rumor, err := nip59.GiftUnwrap(wrap, func(other, ciphertext string) (string, error) {
			return recipient.Decrypt(ctx, ciphertext, other)
		})
		if err != nil {
			t.Fatal(err)
		}
		if rumor.Content != text || rumor.PubKey != senderPub || rumor.Kind != nostr.KindDirectMessage {
			t.Fatalf("rumor %+v", rumor)
		}
		t.Logf("read back from %s: %q from %s", relayURL, rumor.Content, senderPub[:12])
		return
	}
}
