package ui

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Settings > Accounts, for admins: every account, its role and open sessions, and ending
// sessions, removing an account and changing a password account's role. A GitHub account's role
// follows its teams, and removing one lasts only until its next login while it's still in them.
// No change may leave no admin who can log in, and an admin can't remove or demote themselves,
// so that nobody locks themselves out by mistake.

var (
	errOwnAccount = errors.New("You can't remove your own account or change its role. Ask another admin.")     //nolint:staticcheck // Shown to users.
	errLastAdmin  = errors.New("This is the last admin who can log in, so it must stay an admin.")             //nolint:staticcheck // Shown to users.
	errGitHubRole = errors.New("A GitHub account's role follows its GitHub teams, and can't be changed here.") //nolint:staticcheck // Shown to users.
)

func (s *Server) loginMethods() store.LoginMethods {
	return store.LoginMethods{Password: !s.opts.DisablePasswordLogin, GitHub: s.opts.GitHub != nil}
}

func (s *Server) accountJSON(a store.Account, c *conn) map[string]any {
	format := func(t *time.Time) any {
		if t == nil {
			return nil
		}
		return t.In(s.opts.Location).Format(uiTimeFormat)
	}
	sessions := make([]map[string]any, 0, len(a.Sessions))
	for _, session := range a.Sessions {
		sessions = append(sessions, map[string]any{
			"createdAt": format(&session.CreatedAt),
			"expiresAt": format(&session.ExpiresAt),
			"remember":  session.Remember,
			"current":   c.tokenHash != nil && bytes.Equal(session.TokenHash, c.tokenHash),
		})
	}
	method := "password"
	if a.GitHubID != nil {
		method = "github"
	}
	return map[string]any{
		"id":               a.ID,
		"username":         a.Username,
		"role":             a.Role,
		"method":           method,
		"twoFactorEnabled": a.TOTPEnabled,
		"createdAt":        format(&a.CreatedAt),
		"lastLoginAt":      format(a.LastLoginAt),
		"githubCheckedAt":  format(a.GitHubCheckedAt),
		"sessions":         sessions,
		"you":              a.ID == c.userID,
	}
}

func (s *Server) getAccountList(ctx context.Context, c *conn) (reply, error) {
	accounts, err := s.store.ListAccounts(ctx)
	if err != nil {
		return nil, err
	}
	list := make([]map[string]any, 0, len(accounts))
	for _, a := range accounts {
		list = append(list, s.accountJSON(a, c))
	}
	methods := s.loginMethods()
	return ok("accounts", list, "passwordLogin", methods.Password, "githubLogin", methods.GitHub), nil
}

func accountErr(err error) error {
	switch {
	case errors.Is(err, store.ErrLastAdmin):
		return userErr(errLastAdmin)
	case errors.Is(err, store.ErrRoleFromGitHub):
		return userErr(errGitHubRole)
	}
	return err
}

func (s *Server) deleteAccount(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if id == c.userID {
		return nil, userErr(errOwnAccount)
	}
	user, err := s.store.DeleteUser(ctx, id, s.loginMethods())
	if err != nil {
		return nil, accountErr(err)
	}
	s.closeOtherSessions(id, nil)
	s.log.Info("account removed", "username", user.Username, "role", user.Role, "by", c.username)
	return okI18n("successDeleted"), nil
}

func (s *Server) setAccountRole(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var role string
	if err := arg(args, 1, &role); err != nil {
		return nil, userErr(err)
	}
	if role != store.RoleAdmin && role != store.RoleViewer {
		return nil, userErr(errors.New("the role must be admin or viewer"))
	}
	if id == c.userID {
		return nil, userErr(errOwnAccount)
	}
	user, err := s.store.SetUserRole(ctx, id, role, s.loginMethods())
	if err != nil {
		return nil, accountErr(err)
	}
	if user.Role != role {
		// Open pages reconnect, and get what the new role allows.
		s.closeOtherSessions(id, nil)
		s.log.Info("account's role changed", "username", user.Username, "from", user.Role, "to", role, "by", c.username)
	}
	return okI18n("Saved."), nil
}

// endAccountSessions logs an account out everywhere. An admin's own page stays logged in.
func (s *Server) endAccountSessions(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var keep []byte
	if id == c.userID {
		keep = c.tokenHash
	}
	if err := s.store.EndOtherUserSessions(ctx, id, keep); err != nil {
		return nil, err
	}
	s.closeOtherSessions(id, keep)
	s.log.Info("account's sessions ended", "user", id, "by", c.username)
	return okI18n("Saved."), nil
}
