package notify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/coder/websocket"
	"github.com/nbd-wtf/go-nostr"
	"github.com/nbd-wtf/go-nostr/keyer"
	"github.com/nbd-wtf/go-nostr/nip19"
	"github.com/nbd-wtf/go-nostr/nip59"
)

// The nostr and Webpush providers, which Phase 6b added.

func init() {
	providers["nostr"] = nostrDM
	providers["Webpush"] = webPush
}

// webpushSubject identifies the sender to push services, which may contact it about abuse.
const webpushSubject = "https://github.com/pantheon-lab/uptime-kuma"

// webpushTTL is how long a push service keeps a message for a browser that's offline: four
// weeks, web-push's default, which Node.js used.
const webpushTTL = 4 * 7 * 24 * 60 * 60

// webPush sends a message to the browser subscribed in the notification form, signed with this
// server's VAPID key, as Node.js's web-push did.
func webPush(c *call) error {
	if c.env.WebpushKeys == nil {
		return errors.New("web push isn't set up on this server")
	}
	public, private, err := c.env.WebpushKeys(c.ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(c.cfg["subscription"])
	if err != nil {
		return err
	}
	var sub webpush.Subscription
	if err := json.Unmarshal(raw, &sub); err != nil || sub.Endpoint == "" || sub.Keys.P256dh == "" || sub.Keys.Auth == "" {
		return errors.New("this browser isn't subscribed: allow notifications in the form, then save")
	}
	payload, err := json.Marshal(M{"title": "Uptime Kuma", "body": c.msg})
	if err != nil {
		return err
	}
	resp, err := webpush.SendNotificationWithContext(c.ctx, payload, &sub, &webpush.Options{
		HTTPClient:      c.env.Client,
		Subscriber:      webpushSubject,
		VAPIDPublicKey:  public,
		VAPIDPrivateKey: private,
		TTL:             webpushTTL,
		Urgency:         webpush.UrgencyHigh,
	})
	if err != nil {
		return fmt.Errorf("web push: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		if resp.StatusCode == 404 || resp.StatusCode == 410 {
			return fmt.Errorf("the browser's subscription has ended (%s): subscribe again in the notification form", resp.Status)
		}
		return fmt.Errorf("web push: %s %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// nostrTimeout bounds each relay.
const nostrTimeout = 20 * time.Second

// nostrDM sends a NIP-17 private message to each recipient, gift-wrapped with NIP-59 so that
// relays don't see who wrote to whom, to every relay, as Node.js's nostr-tools did.
func nostrDM(c *call) error {
	prefix, value, err := nip19.Decode(strings.TrimSpace(c.s("sender")))
	if err != nil || prefix != "nsec" {
		return errors.New("the sender must be a private key starting with nsec")
	}
	signer, err := keyer.NewPlainKeySigner(value.(string))
	if err != nil {
		return fmt.Errorf("the sender's key: %w", err)
	}
	ctx := c.ctx
	sender, err := signer.GetPublicKey(ctx)
	if err != nil {
		return err
	}
	var recipients []string
	for _, npub := range splitLines(c.s("recipients")) {
		prefix, value, err := nip19.Decode(npub)
		if err != nil || prefix != "npub" {
			return fmt.Errorf("recipient %s isn't an npub", npub)
		}
		recipients = append(recipients, value.(string))
	}
	if len(recipients) == 0 {
		return errors.New("add at least one recipient")
	}
	relays := splitLines(c.s("relays"))
	if len(relays) == 0 {
		return errors.New("add at least one relay")
	}

	var wraps []nostr.Event
	for _, recipient := range recipients {
		rumor := nostr.Event{
			Kind:      nostr.KindDirectMessage,
			Content:   c.msg,
			Tags:      nostr.Tags{{"p", recipient}},
			CreatedAt: nostr.Timestamp(c.env.Now().Unix()),
			PubKey:    sender,
		}
		rumor.ID = rumor.GetID()
		wrap, err := nip59.GiftWrap(rumor, recipient,
			func(plain string) (string, error) { return signer.Encrypt(ctx, plain, recipient) },
			func(e *nostr.Event) error { return signer.SignEvent(ctx, e) }, nil)
		if err != nil {
			return fmt.Errorf("wrap the message: %w", err)
		}
		wraps = append(wraps, wrap)
	}

	delivered := 0
	var failures []string
	for _, url := range relays {
		if err := publishToRelay(ctx, url, wraps, signer); err != nil {
			failures = append(failures, url+": "+err.Error())
			continue
		}
		delivered++
	}
	if delivered == 0 {
		return fmt.Errorf("no relay took the message: %s", strings.Join(failures, "; "))
	}
	return nil
}

// publishToRelay sends events to a relay over NIP-01, and authenticates with NIP-42 when the
// relay asks, signing as the sender, as nostr-tools did.
func publishToRelay(ctx context.Context, url string, events []nostr.Event, signer keyer.KeySigner) error {
	ctx, cancel := context.WithTimeout(ctx, nostrTimeout)
	defer cancel()
	conn, _, err := websocket.Dial(ctx, url, nil)
	if err != nil {
		return err
	}
	defer conn.CloseNow()
	conn.SetReadLimit(1 << 20)
	r := &relayConn{conn: conn, url: url}

	authed := false
	for _, event := range events {
		accepted, reason, err := r.send(ctx, "EVENT", event)
		if err != nil {
			return err
		}
		if !accepted && !authed && strings.HasPrefix(reason, "auth-required:") {
			if err := r.auth(ctx, signer); err != nil {
				return fmt.Errorf("authenticate: %w", err)
			}
			authed = true
			if accepted, reason, err = r.send(ctx, "EVENT", event); err != nil {
				return err
			}
		}
		if !accepted {
			return fmt.Errorf("refused: %s", reason)
		}
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
	return nil
}

type relayConn struct {
	conn      *websocket.Conn
	url       string
	challenge string
}

// send sends an EVENT or AUTH message and waits for the relay's OK for it.
func (r *relayConn) send(ctx context.Context, kind string, event nostr.Event) (bool, string, error) {
	data, err := json.Marshal([]any{kind, event})
	if err != nil {
		return false, "", err
	}
	if err := r.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return false, "", err
	}
	for {
		_, data, err := r.conn.Read(ctx)
		if err != nil {
			return false, "", err
		}
		var msg []json.RawMessage
		if json.Unmarshal(data, &msg) != nil || len(msg) < 2 {
			continue
		}
		var label string
		_ = json.Unmarshal(msg[0], &label)
		switch label {
		case "AUTH":
			_ = json.Unmarshal(msg[1], &r.challenge)
		case "OK":
			var id, reason string
			var accepted bool
			_ = json.Unmarshal(msg[1], &id)
			if len(msg) > 2 {
				_ = json.Unmarshal(msg[2], &accepted)
			}
			if len(msg) > 3 {
				_ = json.Unmarshal(msg[3], &reason)
			}
			if id == event.ID {
				return accepted, reason, nil
			}
		}
	}
}

func (r *relayConn) auth(ctx context.Context, signer keyer.KeySigner) error {
	if r.challenge == "" {
		return errors.New("the relay asked to authenticate, but sent no challenge")
	}
	event := nostr.Event{
		Kind:      nostr.KindClientAuthentication,
		CreatedAt: nostr.Now(),
		Tags:      nostr.Tags{{"relay", r.url}, {"challenge", r.challenge}},
	}
	if err := signer.SignEvent(ctx, &event); err != nil {
		return err
	}
	accepted, reason, err := r.send(ctx, "AUTH", event)
	if err != nil {
		return err
	}
	if !accepted {
		return errors.New(reason)
	}
	return nil
}

func splitLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			out = append(out, line)
		}
	}
	return out
}
