package store

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// importedChannel is the Postgres channel that kuma-import notifies once it has written, so that
// a running service picks up what it imported without a restart.
const importedChannel = "kuma_imported"

// listenRetry is how long ListenImported waits before connecting again after an error.
var listenRetry = 5 * time.Second

// NotifyImported tells running services that an import changed the data.
func (s *Store) NotifyImported(ctx context.Context) error {
	if _, err := s.pool.Exec(ctx, `SELECT pg_notify($1, '')`, importedChannel); err != nil {
		return fmt.Errorf("notify import: %w", err)
	}
	return nil
}

// ListenImported calls changed after each import, until ctx ends. A lost connection is opened
// again, and changed is called then too, in case an import finished meanwhile.
func (s *Store) ListenImported(ctx context.Context, log *slog.Logger, changed func(context.Context)) {
	reconnect := false
	for ctx.Err() == nil {
		err := s.listenImported(ctx, func() {
			if reconnect {
				changed(ctx)
			}
			reconnect = true
		}, changed)
		if ctx.Err() != nil {
			return
		}
		log.Warn("listen for imports, connecting again", "error", err, "in", listenRetry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetry):
		}
	}
}

// listenImported holds its own connection, which LISTEN ties up, and returns when it fails.
func (s *Store) listenImported(ctx context.Context, listening func(), changed func(context.Context)) error {
	pooled, err := s.pool.Acquire(ctx)
	if err != nil {
		return err
	}
	// Out of the pool for good: a connection that has listened mustn't serve queries.
	conn := pooled.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+importedChannel); err != nil {
		return err
	}
	listening()
	for {
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return err
		}
		changed(ctx)
	}
}
