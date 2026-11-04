package api

import (
	"sync"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
)

// subscriberBuffer is how many beats a stream can lag behind. It holds about a
// minute of traffic at 5,000 monitors on one-minute checks.
const subscriberBuffer = 8192

// Hub fans new beats out to SSE streams. A subscriber that falls behind is
// disconnected, so one slow browser can never hold up ingestion.
type Hub struct {
	mu     sync.Mutex
	subs   map[chan store.Beat]struct{}
	closed bool
}

// NewHub creates an empty hub.
func NewHub() *Hub {
	h := &Hub{subs: make(map[chan store.Beat]struct{})}
	telemetry.Metrics().ObserveStreams(func() int64 {
		h.mu.Lock()
		defer h.mu.Unlock()
		return int64(len(h.subs))
	})
	return h
}

// Subscribe registers a stream. The channel closes when the subscriber is
// dropped or the hub shuts down. The returned function unsubscribes.
func (h *Hub) Subscribe() (<-chan store.Beat, func()) {
	ch := make(chan store.Beat, subscriberBuffer)
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		close(ch)
		return ch, func() {}
	}
	h.subs[ch] = struct{}{}
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Publish delivers beats to all subscribers without blocking.
func (h *Hub) Publish(beats []store.Beat) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
	deliver:
		for _, beat := range beats {
			select {
			case ch <- beat:
			default:
				delete(h.subs, ch)
				close(ch)
				telemetry.Metrics().RecordDroppedStream()
				break deliver
			}
		}
	}
}

// Close ends all streams and rejects new subscribers.
func (h *Hub) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.closed = true
	for ch := range h.subs {
		delete(h.subs, ch)
		close(ch)
	}
}
