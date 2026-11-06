package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAccounts(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	first, err := s.CreateFirstUser(ctx, "admin", "salt:hash")
	if err != nil {
		t.Fatal(err)
	}
	gh, err := s.SaveGitHubUser(ctx, 42, "octocat", RoleAdmin, "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	for _, h := range []string{"a", "b"} {
		if err := s.CreateSession(ctx, []byte(h), Session{UserID: first.ID, ExpiresAt: future}); err != nil {
			t.Fatal(err)
		}
	}
	if err := s.CreateSession(ctx, []byte("c"), Session{UserID: gh.ID, ExpiresAt: future}); err != nil {
		t.Fatal(err)
	}

	accounts, err := s.ListAccounts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(accounts) != 2 || accounts[0].Username != "admin" || len(accounts[0].Sessions) != 2 ||
		accounts[0].LastLoginAt == nil || accounts[1].GitHubID == nil || len(accounts[1].Sessions) != 1 {
		t.Fatalf("accounts: %+v", accounts)
	}

	both := LoginMethods{Password: true, GitHub: true}
	passwordOnly := LoginMethods{Password: true}
	if _, err := s.SetUserRole(ctx, gh.ID, RoleViewer, both); !errors.Is(err, ErrRoleFromGitHub) {
		t.Fatalf("GitHub role: %v", err)
	}
	// Without GitHub login, the GitHub admin can't log in, so the password admin is the last.
	if _, err := s.SetUserRole(ctx, first.ID, RoleViewer, passwordOnly); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("demote the last admin: %v", err)
	}
	if _, err := s.DeleteUser(ctx, first.ID, passwordOnly); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete the last admin: %v", err)
	}
	// With it, either may go, but not both at once.
	results := make([]error, 2)
	var wg sync.WaitGroup
	for i, id := range []int32{first.ID, gh.ID} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i] = s.DeleteUser(ctx, id, both)
		}()
	}
	wg.Wait()
	if (results[0] == nil) == (results[1] == nil) {
		t.Fatalf("concurrent deletes of the two admins: %v", results)
	}
	accounts, err = s.ListAccounts(ctx)
	if err != nil || len(accounts) != 1 {
		t.Fatalf("after one delete: %+v %v", accounts, err)
	}
	if _, err := s.DeleteUser(ctx, 999, both); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete an unknown account: %v", err)
	}

	// A viewer can always go.
	viewer, err := s.SaveGitHubUser(ctx, 7, "viewer", RoleViewer, "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DeleteUser(ctx, viewer.ID, LoginMethods{}); err != nil {
		t.Fatal(err)
	}

	// Sessions: all but the one kept, and the sessions went with a deleted account.
	if first, err = s.UserByUsername(ctx, "admin"); err == nil {
		if err := s.EndOtherUserSessions(ctx, first.ID, []byte("a")); err != nil {
			t.Fatal(err)
		}
		accounts, _ = s.ListAccounts(ctx)
		if len(accounts[0].Sessions) != 1 || string(accounts[0].Sessions[0].TokenHash) != "a" {
			t.Fatalf("after ending the others: %+v", accounts[0].Sessions)
		}
	}
	var sessions int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM app_session WHERE token_hash = 'c'`).Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if results[1] == nil && sessions != 0 {
		t.Fatal("the deleted GitHub account's session remains")
	}
}
