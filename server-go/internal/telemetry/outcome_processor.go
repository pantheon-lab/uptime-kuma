package telemetry

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

const (
	maxBufferedTraces = 100_000
	// Child spans that end after their root, or whose root never ends, are dropped after this.
	bufferedTraceTTL = 5 * time.Minute
	sweepEvery       = 10_000
)

type bufferedTrace struct {
	spans   []sdktrace.ReadOnlySpan
	touched time.Time
}

// OutcomeProcessor decides which traces to export once their outcome is known: all
// failures, and a sample of the rest. A sampler can't do this, because it has to decide
// when a trace starts. It means that a trace id shown on a failed beat always has a trace.
//
// Child spans end before their root, so they wait here until the root ends.
type OutcomeProcessor struct {
	next  sdktrace.SpanProcessor
	ratio float64
	log   *slog.Logger

	mu     sync.Mutex
	traces map[trace.TraceID]*bufferedTrace
	ended  int
}

// NewOutcomeProcessor forwards the chosen traces to next. ratio is the share of
// successful traces to keep.
func NewOutcomeProcessor(next sdktrace.SpanProcessor, ratio float64, log *slog.Logger) *OutcomeProcessor {
	return &OutcomeProcessor{next: next, ratio: ratio, log: log, traces: make(map[trace.TraceID]*bufferedTrace)}
}

// OnStart implements sdktrace.SpanProcessor.
func (p *OutcomeProcessor) OnStart(context.Context, sdktrace.ReadWriteSpan) {}

// OnEnd implements sdktrace.SpanProcessor.
func (p *OutcomeProcessor) OnEnd(span sdktrace.ReadOnlySpan) {
	id := span.SpanContext().TraceID()
	parent := span.Parent()
	isRoot := !parent.IsValid() || parent.IsRemote()

	p.mu.Lock()
	if !isRoot {
		p.sweepLocked()
		buffered := p.traces[id]
		if buffered == nil {
			if len(p.traces) >= maxBufferedTraces {
				p.mu.Unlock()
				p.log.Warn("too many unfinished traces, dropping a span")
				return
			}
			buffered = &bufferedTrace{}
			p.traces[id] = buffered
		}
		buffered.spans = append(buffered.spans, span)
		buffered.touched = time.Now()
		p.mu.Unlock()
		return
	}
	buffered := p.traces[id]
	delete(p.traces, id)
	p.mu.Unlock()

	if span.Status().Code != codes.Error && rand.Float64() >= p.ratio {
		return
	}
	if buffered != nil {
		for _, child := range buffered.spans {
			p.next.OnEnd(child)
		}
	}
	p.next.OnEnd(span)
}

func (p *OutcomeProcessor) sweepLocked() {
	p.ended++
	if p.ended < sweepEvery {
		return
	}
	p.ended = 0
	cutoff := time.Now().Add(-bufferedTraceTTL)
	for id, buffered := range p.traces {
		if buffered.touched.Before(cutoff) {
			delete(p.traces, id)
		}
	}
}

// Shutdown implements sdktrace.SpanProcessor.
func (p *OutcomeProcessor) Shutdown(ctx context.Context) error { return p.next.Shutdown(ctx) }

// ForceFlush implements sdktrace.SpanProcessor.
func (p *OutcomeProcessor) ForceFlush(ctx context.Context) error { return p.next.ForceFlush(ctx) }
