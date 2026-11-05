package telemetry

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func newTracer(ratio float64) (*tracetest.InMemoryExporter, *OutcomeProcessor, *sdktrace.TracerProvider) {
	exporter := tracetest.NewInMemoryExporter()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	processor := NewOutcomeProcessor(sdktrace.NewSimpleSpanProcessor(exporter), ratio, log)
	return exporter, processor, sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(processor))
}

func runTrace(tp *sdktrace.TracerProvider, failed bool) {
	tracer := tp.Tracer("test")
	ctx, root := tracer.Start(context.Background(), "root")
	_, child := tracer.Start(ctx, "child")
	child.End()
	if failed {
		root.SetStatus(codes.Error, "boom")
	}
	root.End()
}

func TestKeepsEveryFailureWithItsChildren(t *testing.T) {
	exporter, processor, tp := newTracer(0)
	for i := 0; i < 50; i++ {
		runTrace(tp, true)
	}
	if got := len(exporter.GetSpans()); got != 100 {
		t.Errorf("exported %d spans, want 100", got)
	}
	if len(processor.traces) != 0 {
		t.Errorf("%d traces still buffered", len(processor.traces))
	}
}

func TestDropsSuccessesAtRatioZeroWithoutLeaking(t *testing.T) {
	exporter, processor, tp := newTracer(0)
	for i := 0; i < 50; i++ {
		runTrace(tp, false)
	}
	if got := len(exporter.GetSpans()); got != 0 {
		t.Errorf("exported %d spans, want 0", got)
	}
	if len(processor.traces) != 0 {
		t.Errorf("%d traces still buffered", len(processor.traces))
	}
}

func TestKeepsSuccessesAtRatioOne(t *testing.T) {
	exporter, _, tp := newTracer(1)
	runTrace(tp, false)
	if got := len(exporter.GetSpans()); got != 2 {
		t.Errorf("exported %d spans, want 2", got)
	}
}

func TestSampleIsCloseToRatio(t *testing.T) {
	exporter, _, tp := newTracer(0.1)
	for i := 0; i < 5000; i++ {
		runTrace(tp, false)
	}
	traces := len(exporter.GetSpans()) / 2
	if traces < 350 || traces > 650 {
		t.Errorf("kept %d of 5000 traces at ratio 0.1", traces)
	}
}

func TestSuccessRatioValidation(t *testing.T) {
	for raw, valid := range map[string]bool{"": true, "0": true, "0.5": true, "1": true, "2": false, "-1": false, "x": false} {
		t.Setenv("KUMA_TRACE_SUCCESS_RATIO", raw)
		if _, err := successRatio(); (err == nil) != valid {
			t.Errorf("%q: err = %v", raw, err)
		}
	}
}
