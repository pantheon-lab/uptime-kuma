package telemetry

import (
	"context"
	"strconv"
	"sync"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Instruments holds the service's metrics. Latency stays per type and group rather than
// per monitor: histograms for thousands of monitors would create tens of thousands of series.
type Instruments struct {
	checks        metric.Int64Counter
	checkDuration metric.Float64Histogram
	schedulerLag  metric.Float64Histogram
	running       metric.Int64UpDownCounter
	flushDuration metric.Float64Histogram
	flushedBeats  metric.Int64Counter
	droppedBeats  metric.Int64Counter
	droppedSSE    metric.Int64Counter
	notifications metric.Int64Counter

	mu       sync.Mutex
	monitors map[int32]monitorUp
	queue    func() int64
	streams  func() int64
}

type monitorUp struct {
	attrs metric.MeasurementOption
	up    int64
}

var (
	instruments     *Instruments
	instrumentsOnce sync.Once
)

// Metrics returns the shared instruments. They report through the global meter
// provider, including when Setup runs after this call.
func Metrics() *Instruments {
	instrumentsOnce.Do(func() { instruments = newInstruments() })
	return instruments
}

func newInstruments() *Instruments {
	meter := otel.Meter("kuma-analytics")
	seconds := metric.WithExplicitBucketBoundaries(0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60)

	// These only fail on an invalid name or unit, and return a no-op instrument then.
	i := &Instruments{monitors: make(map[int32]monitorUp)}
	i.checks, _ = meter.Int64Counter("kuma.checks", metric.WithDescription("Checks run"), metric.WithUnit("{check}"))
	i.checkDuration, _ = meter.Float64Histogram("kuma.check.duration", metric.WithDescription("Check duration"), metric.WithUnit("s"), seconds)
	i.schedulerLag, _ = meter.Float64Histogram("kuma.scheduler.lag", metric.WithDescription("How late checks start"), metric.WithUnit("s"), seconds)
	i.running, _ = meter.Int64UpDownCounter("kuma.checks.running", metric.WithDescription("Checks running now"), metric.WithUnit("{check}"))
	i.flushDuration, _ = meter.Float64Histogram("kuma.beats.flush.duration", metric.WithDescription("Time to write a batch of beats"), metric.WithUnit("s"), seconds)
	i.flushedBeats, _ = meter.Int64Counter("kuma.beats.written", metric.WithDescription("Beats written to the database"), metric.WithUnit("{beat}"))
	i.droppedBeats, _ = meter.Int64Counter("kuma.beats.dropped", metric.WithDescription("Beats dropped because the queue was full"), metric.WithUnit("{beat}"))
	i.droppedSSE, _ = meter.Int64Counter("kuma.sse.dropped_clients", metric.WithDescription("SSE clients dropped for falling behind"), metric.WithUnit("{client}"))

	i.notifications, _ = meter.Int64Counter("kuma.notifications", metric.WithDescription("Notifications by provider and outcome"), metric.WithUnit("{notification}"))

	up, _ := meter.Int64ObservableGauge("kuma.monitor.up", metric.WithDescription("1 if the monitor's last beat was UP, otherwise 0"))
	queue, _ := meter.Int64ObservableGauge("kuma.beats.queue", metric.WithDescription("Beats waiting to be written"), metric.WithUnit("{beat}"))
	streams, _ := meter.Int64ObservableGauge("kuma.sse.clients", metric.WithDescription("Open SSE streams"), metric.WithUnit("{client}"))
	_, _ = meter.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		i.mu.Lock()
		defer i.mu.Unlock()
		for _, m := range i.monitors {
			o.ObserveInt64(up, m.up, m.attrs)
		}
		if i.queue != nil {
			o.ObserveInt64(queue, i.queue())
		}
		if i.streams != nil {
			o.ObserveInt64(streams, i.streams())
		}
		return nil
	}, up, queue, streams)
	return i
}

// RecordCheck records one finished check. Pass a context with the check's span only for
// traces that are certain to be kept, because the histogram stores it as an exemplar.
func (i *Instruments) RecordCheck(ctx context.Context, monitorType, group, outcome string, seconds float64) {
	attrs := metric.WithAttributes(
		attribute.String("monitor.type", monitorType),
		attribute.String("monitor.group", group),
		attribute.String("outcome", outcome),
	)
	i.checks.Add(ctx, 1, attrs)
	i.checkDuration.Record(ctx, seconds, attrs)
}

// RecordLag records how late a check started.
func (i *Instruments) RecordLag(seconds float64) {
	i.schedulerLag.Record(context.Background(), seconds)
}

// CheckStarted and CheckFinished track running checks.
func (i *Instruments) CheckStarted() { i.running.Add(context.Background(), 1) }

// CheckFinished is the counterpart of CheckStarted.
func (i *Instruments) CheckFinished() { i.running.Add(context.Background(), -1) }

// SetMonitorUp stores a monitor's current state. Its series exists until ForgetMonitor.
func (i *Instruments) SetMonitorUp(id int32, name, group string, up bool) {
	value := int64(0)
	if up {
		value = 1
	}
	attrs := metric.WithAttributes(
		attribute.String("monitor.id", strconv.Itoa(int(id))),
		attribute.String("monitor.name", name),
		attribute.String("monitor.group", group),
	)
	i.mu.Lock()
	defer i.mu.Unlock()
	i.monitors[id] = monitorUp{attrs: attrs, up: value}
}

// ForgetMonitor removes a paused or deleted monitor's series.
func (i *Instruments) ForgetMonitor(id int32) {
	i.mu.Lock()
	defer i.mu.Unlock()
	delete(i.monitors, id)
}

// RecordFlush records a written batch of beats.
func (i *Instruments) RecordFlush(seconds float64, beats int) {
	i.flushDuration.Record(context.Background(), seconds)
	i.flushedBeats.Add(context.Background(), int64(beats))
}

// RecordDroppedBeats counts beats lost to a full queue.
func (i *Instruments) RecordDroppedBeats(n int) { i.droppedBeats.Add(context.Background(), int64(n)) }

// RecordDroppedStream counts an SSE client dropped for falling behind.
func (i *Instruments) RecordDroppedStream() { i.droppedSSE.Add(context.Background(), 1) }

// RecordNotification counts a notification. Outcome is sent, failed or dropped.
func (i *Instruments) RecordNotification(provider, outcome string) {
	i.notifications.Add(context.Background(), 1, metric.WithAttributes(attribute.String("provider", provider), attribute.String("outcome", outcome)))
}

// ObserveQueue sets the function that reports the beat queue length.
func (i *Instruments) ObserveQueue(fn func() int64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.queue = fn
}

// ObserveStreams sets the function that reports the number of SSE streams.
func (i *Instruments) ObserveStreams(fn func() int64) {
	i.mu.Lock()
	defer i.mu.Unlock()
	i.streams = fn
}
