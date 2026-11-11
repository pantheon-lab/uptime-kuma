package store

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

// listenForTest runs ListenImported until the test ends, counting its calls into changes.
func listenForTest(t *testing.T, s *Store, changed func(context.Context) error) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ListenImported(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), changed)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

func waitForCalls(t *testing.T, calls *atomic.Int32, want int32, what string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for calls.Load() < want {
		if time.Now().After(deadline) {
			t.Fatalf("%s: %d calls, want %d", what, calls.Load(), want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListenImportedHearsEachImport(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	var calls atomic.Int32
	cancel, done := listenForTest(t, s, func(context.Context) error { calls.Add(1); return nil })

	// Once on listening, in case an import finished before.
	waitForCalls(t, &calls, 1, "on listening")
	for i := int32(2); i <= 3; i++ {
		if err := s.NotifyImported(ctx); err != nil {
			t.Fatal(err)
		}
		waitForCalls(t, &calls, i, "after an import")
	}

	// The pool still serves queries: listening doesn't keep one of its connections.
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener didn't stop with its context")
	}
}

func TestListenImportedRecovers(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	retry := listenRetry
	listenRetry = 50 * time.Millisecond
	t.Cleanup(func() { listenRetry = retry })

	// A reload that fails is tried again, without another import.
	var calls atomic.Int32
	failOnce := atomic.Bool{}
	failOnce.Store(true)
	listenForTest(t, s, func(context.Context) error {
		calls.Add(1)
		if failOnce.Swap(false) {
			return errors.New("the database is busy")
		}
		return nil
	})
	waitForCalls(t, &calls, 2, "a failed reload tried again")

	// A lost connection is opened again, and reloads, in case an import finished meanwhile.
	before := calls.Load()
	var killed int64
	err := s.pool.QueryRow(ctx, `SELECT count(pg_terminate_backend(pid)) FROM pg_stat_activity
		WHERE datname = current_database() AND query LIKE 'LISTEN%' AND pid <> pg_backend_pid()`).Scan(&killed)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if killed != 1 {
		t.Fatalf("closed %d listening connections, want 1", killed)
	}
	waitForCalls(t, &calls, before+1, "after the connection was lost")

	// And it hears imports again.
	before = calls.Load()
	if err := s.NotifyImported(ctx); err != nil {
		t.Fatal(err)
	}
	waitForCalls(t, &calls, before+1, "an import after reconnecting")
}
