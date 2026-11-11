package store

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

func TestListenImportedHearsEachImport(t *testing.T) {
	s := newTestStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.ListenImported(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), func(context.Context) { changes <- struct{}{} })
	}()

	// LISTEN takes a moment; a notification before it would be missed, so keep notifying until one is heard.
	deadline := time.Now().Add(10 * time.Second)
	heard := false
	for !heard && time.Now().Before(deadline) {
		if err := s.NotifyImported(ctx); err != nil {
			t.Fatal(err)
		}
		select {
		case <-changes:
			heard = true
		case <-time.After(100 * time.Millisecond):
		}
	}
	if !heard {
		t.Fatal("the listener didn't hear the import")
	}
	for len(changes) > 0 {
		<-changes
	}

	if err := s.NotifyImported(ctx); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("the listener didn't hear a second import")
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
