package scheduler

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
)

const (
	flushInterval   = time.Second
	flushSize       = 5000
	writeTimeout    = 30 * time.Second
	defaultMaxQueue = 500_000 // Over an hour of beats at 5,000 monitors on one-minute checks.
)

// BeatInserter stores beats and returns those that were new.
type BeatInserter interface {
	InsertBeats(ctx context.Context, beats []store.Beat) ([]store.Beat, error)
}

// Writer batches beats into the database, so that thousands of monitors don't
// cost one commit per check. While the database is down, beats wait in memory and
// the oldest are dropped once the queue is full. Replays are safe: rows are unique.
type Writer struct {
	db      BeatInserter
	publish func([]store.Beat)
	log     *slog.Logger

	maxQueue int
	queueMu  sync.Mutex
	queue    []store.Beat
	dropped  int

	flushMu sync.Mutex // Keeps batches in order.
	wake    chan struct{}
	stop    chan struct{}
	done    chan struct{}
}

// NewWriter creates a Writer. publish receives every newly stored batch.
func NewWriter(db BeatInserter, publish func([]store.Beat), log *slog.Logger) *Writer {
	w := &Writer{
		db:       db,
		publish:  publish,
		log:      log,
		maxQueue: defaultMaxQueue,
		wake:     make(chan struct{}, 1),
		stop:     make(chan struct{}),
		done:     make(chan struct{}),
	}
	telemetry.Metrics().ObserveQueue(func() int64 {
		w.queueMu.Lock()
		defer w.queueMu.Unlock()
		return int64(len(w.queue))
	})
	return w
}

// Add queues one beat and never blocks on the database.
func (w *Writer) Add(beat store.Beat) {
	w.queueMu.Lock()
	if len(w.queue) >= w.maxQueue {
		w.queue = w.queue[1:]
		w.dropped++
	}
	w.queue = append(w.queue, beat)
	full := len(w.queue) >= flushSize
	w.queueMu.Unlock()
	if full {
		select {
		case w.wake <- struct{}{}:
		default:
		}
	}
}

// Run flushes until Close is called.
func (w *Writer) Run() {
	defer close(w.done)
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
		case <-w.wake:
		case <-w.stop:
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		if err := w.Flush(ctx); err != nil {
			w.log.Error("write beats failed, will retry", "error", err)
		}
		cancel()
	}
}

// Flush writes everything queued so far. Beats that couldn't be written go back
// to the front of the queue.
func (w *Writer) Flush(ctx context.Context) error {
	w.flushMu.Lock()
	defer w.flushMu.Unlock()

	w.queueMu.Lock()
	pending := w.queue
	w.queue = nil
	if w.dropped > 0 {
		w.log.Error("beat queue was full, dropped the oldest beats", "dropped", w.dropped)
		telemetry.Metrics().RecordDroppedBeats(w.dropped)
		w.dropped = 0
	}
	w.queueMu.Unlock()

	for len(pending) > 0 {
		n := min(flushSize, len(pending))
		spanCtx, span := otel.Tracer(tracerName).Start(ctx, "beats.flush", trace.WithAttributes(attribute.Int("beats.count", n)))
		start := time.Now()
		inserted, err := w.db.InsertBeats(spanCtx, pending[:n])
		telemetry.Metrics().RecordFlush(time.Since(start).Seconds(), n)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		}
		span.End()
		if err != nil {
			w.requeue(pending)
			return err
		}
		pending = pending[n:]
		if len(inserted) > 0 {
			w.publish(inserted)
		}
	}
	return nil
}

func (w *Writer) requeue(pending []store.Beat) {
	w.queueMu.Lock()
	defer w.queueMu.Unlock()
	merged := append(pending, w.queue...)
	if over := len(merged) - w.maxQueue; over > 0 {
		merged = merged[over:]
		w.dropped += over
	}
	w.queue = merged
}

// Close stops the writer after a final flush.
func (w *Writer) Close(ctx context.Context) error {
	close(w.stop)
	<-w.done
	return w.Flush(ctx)
}
