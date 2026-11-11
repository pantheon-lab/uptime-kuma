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

// ListenImported calls changed after each import, until ctx ends, and once each time it starts
// listening, the first time too, in case an import finished while it wasn't. When changed fails,
// or the connection is lost, it waits and listens again, which calls changed again.
func (s *Store) ListenImported(ctx context.Context, log *slog.Logger, changed func(context.Context) error) {
	for ctx.Err() == nil {
		err := s.listenImported(ctx, changed)
		if ctx.Err() != nil {
			return
		}
		log.Warn("reload after an import, trying again", "error", err, "in", listenRetry)
		select {
		case <-ctx.Done():
			return
		case <-time.After(listenRetry):
		}
	}
}

// listenImported holds its own connection, which LISTEN ties up, and returns when it fails.
func (s *Store) listenImported(ctx context.Context, changed func(context.Context) error) error {
	pooled, err := s.pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire a connection to listen on: %w", err)
	}
	// Out of the pool for good: a connection that has listened mustn't serve queries.
	conn := pooled.Hijack()
	defer func() { _ = conn.Close(context.WithoutCancel(ctx)) }()
	if _, err := conn.Exec(ctx, "LISTEN "+importedChannel); err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	for {
		if err := changed(ctx); err != nil {
			return fmt.Errorf("reload: %w", err)
		}
		if _, err := conn.WaitForNotification(ctx); err != nil {
			return fmt.Errorf("wait for an import: %w", err)
		}
	}
}
