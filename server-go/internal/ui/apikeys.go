package ui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// API keys made under Settings > API Keys are the REST API's keys, the same as those made
// with POST /api/v1/api-keys. The form doesn't ask for scopes, and the dialog sends them
// only when they're picked, so a key without any gets them all. Deleting a key revokes it,
// and revoked keys aren't listed.

const (
	maxAPIKeyNameLength = 100
	uiTimeFormat        = "2006-01-02 15:04:05"
)

func (s *Server) apiKeyJSON(k store.APIKey) map[string]any {
	format := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.In(s.opts.Location).Format(uiTimeFormat)
	}
	return map[string]any{
		"id":         k.ID,
		"name":       k.Name,
		"prefix":     k.Prefix,
		"scopes":     k.Scopes,
		"enabled":    k.RevokedAt == nil,
		"createdAt":  k.CreatedAt.In(s.opts.Location).Format(uiTimeFormat),
		"expiresAt":  format(k.ExpiresAt),
		"lastUsedAt": format(k.LastUsedAt),
	}
}

func (s *Server) getAPIKeyList(ctx context.Context) (reply, error) {
	keys, err := s.store.ListAPIKeys(ctx)
	if err != nil {
		return nil, err
	}
	list := []map[string]any{}
	for _, k := range keys {
		if k.RevokedAt == nil {
			list = append(list, s.apiKeyJSON(k))
		}
	}
	return ok("apiKeyList", list, "scopes", api.GrantableScopes()), nil
}

func (s *Server) addAPIKey(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	var input struct {
		Name    string   `json:"name"`
		Expires *string  `json:"expires"`
		Scopes  []string `json:"scopes"`
	}
	if err := arg(args, 0, &input); err != nil {
		return nil, userErr(err)
	}
	name := strings.TrimSpace(input.Name)
	if name == "" || len(name) > maxAPIKeyNameLength {
		return nil, userErr(fmt.Errorf("the name must be 1 to %d characters", maxAPIKeyNameLength))
	}
	scopes := input.Scopes
	grantable := api.GrantableScopes()
	if len(scopes) == 0 {
		scopes = grantable
	}
	for _, scope := range scopes {
		if !slices.Contains(grantable, scope) {
			return nil, userErr(fmt.Errorf("unknown scope %q", scope))
		}
	}
	scopes = slices.Clone(scopes)
	slices.Sort(scopes)
	scopes = slices.Compact(scopes)

	var expires *time.Time
	if input.Expires != nil && strings.TrimSpace(*input.Expires) != "" {
		t, err := parseUITime(strings.TrimSpace(*input.Expires), s.opts.Location)
		if err != nil {
			return nil, userErr(err)
		}
		if !t.After(s.now()) {
			return nil, userErr(errors.New("the expiry date must be in the future"))
		}
		expires = &t
	}

	plain, hash, prefix, err := api.NewKey()
	if err != nil {
		return nil, err
	}
	key, err := s.store.CreateAPIKey(ctx, store.APIKey{Name: name, Prefix: prefix, Scopes: scopes, ExpiresAt: expires}, hash)
	if err != nil {
		return nil, err
	}
	s.log.Info("API key created", "key", key.ID, "name", key.Name, "scopes", key.Scopes, "by", c.username)
	return okI18n("successAdded", "key", plain, "keyID", key.ID), nil
}

// parseUITime reads a date and time from the UI's date pickers, in the server's timezone.
func parseUITime(value string, location *time.Location) (time.Time, error) {
	for _, layout := range []string{"2006-01-02 15:04", "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.ParseInLocation(layout, value, location); err == nil {
			return t, nil
		}
	}
	if t, err := time.Parse(time.RFC3339, value); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("invalid date %q", value)
}

func (s *Server) deleteAPIKey(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	var raw any
	if err := arg(args, 0, &raw); err != nil {
		return nil, userErr(err)
	}
	var id int64
	switch v := raw.(type) {
	case float64:
		id = int64(v)
	case string:
		id, _ = strconv.ParseInt(v, 10, 64)
	}
	if id <= 0 {
		return nil, userErr(errors.New("invalid id"))
	}
	if err := s.store.RevokeAPIKey(ctx, id); err != nil {
		return nil, err
	}
	s.log.Info("API key revoked", "key", id, "by", c.username)
	return okI18n("successDeleted"), nil
}
