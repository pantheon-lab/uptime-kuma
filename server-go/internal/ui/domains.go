package ui

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/domains"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// Domains tells the UI about the domains of monitors.
type Domains interface {
	Check(ctx context.Context, kind, target string) (domain, tld string, err error)
	Expiry(ctx context.Context, kind, target string) (*time.Time, error)
}

// BroadcastDomainInfo tells open pages a monitor's domain expiry.
func (s *Server) BroadcastDomainInfo(monitorID int32, daysRemaining int, expiry time.Time) {
	s.broadcast(encodeMessage("domainInfo", monitorID, daysRemaining, expiry.UTC().Format(time.RFC3339)))
}

// sendDomainInfo sends what's known about the domains of monitors that track them.
func (s *Server) sendDomainInfo(ctx context.Context, c *conn, monitors map[int32]monitor.Monitor) {
	if s.opts.Domains == nil {
		return
	}
	now := s.now()
	for id, m := range monitors {
		if !m.DomainExpiryNotification || !domains.Supports(m.Type) {
			continue
		}
		expiry, err := s.opts.Domains.Expiry(ctx, m.Type, domains.Target(m.Type, m.URL, m.Hostname, m.GRPCURL))
		if err != nil || expiry == nil {
			continue
		}
		c.emit("domainInfo", id, domains.DaysRemaining(*expiry, now), expiry.UTC().Format(time.RFC3339))
	}
}

// checkDomain says whether a monitor's domain can be tracked, as the form asks while it's edited.
func (s *Server) checkDomain(ctx context.Context, args []json.RawMessage) (reply, error) {
	var in struct {
		Type     string  `json:"type"`
		URL      *string `json:"url"`
		Hostname *string `json:"hostname"`
		GRPCURL  *string `json:"grpcUrl"`
	}
	if err := arg(args, 0, &in); err != nil {
		return nil, userErr(err)
	}
	if s.opts.Domains == nil {
		return nil, userErr(errors.New("domain expiry isn't available"))
	}
	domain, tld, err := s.opts.Domains.Check(ctx, in.Type, domains.Target(in.Type, text(in.URL), text(in.Hostname), text(in.GRPCURL)))
	var unsupported *domains.UnsupportedError
	if errors.As(err, &unsupported) {
		meta := unsupported.Meta
		if meta == nil {
			meta = map[string]string{}
		}
		return reply{"ok": false, "msg": unsupported.Key, "msgi18n": true, "meta": meta}, nil
	}
	if err != nil {
		return nil, err
	}
	return ok("domain", domain, "tld", tld), nil
}
