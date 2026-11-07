package notify

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
)

const (
	workers       = 16
	queuePerShard = 1024
	sendTimeout   = 30 * time.Second
	maxAttempts   = 3
	closeTimeout  = 5 * time.Second
	sqlDateTime   = "2006-01-02 15:04:05"
	beatTime      = "2006-01-02 15:04:05.000"
)

// Store is what the dispatcher reads.
type Store interface {
	ActiveNotificationsFor(ctx context.Context, monitorID int32) ([]store.Notification, error)
	LastDownTime(ctx context.Context, monitorID int32) (*time.Time, error)
	MonitorPath(ctx context.Context, monitorID int32) ([]string, error)
}

type event struct {
	monitor monitor.Monitor
	beat    store.Beat
}

// Dispatcher sends notifications in the background, so that a slow provider can never hold up
// checks. Events for one monitor stay in order, because a provider may have to open an alert
// before it can close it. Whatever isn't sent within closeTimeout of stopping is lost.
type Dispatcher struct {
	store     Store
	env       *Env
	log       *slog.Logger
	location  *time.Location
	backoff   time.Duration
	closeWait time.Duration

	shards []chan event
	stop   chan struct{}
	wg     sync.WaitGroup
	once   sync.Once
}

// NewDispatcher creates a dispatcher and starts its workers. location is the timezone that
// messages show.
func NewDispatcher(s Store, env *Env, location *time.Location, log *slog.Logger) *Dispatcher {
	d := &Dispatcher{store: s, env: env, log: log, location: location, backoff: 2 * time.Second, closeWait: closeTimeout, shards: make([]chan event, workers), stop: make(chan struct{})}
	for i := range d.shards {
		d.shards[i] = make(chan event, queuePerShard)
		d.wg.Add(1)
		go d.work(d.shards[i])
	}
	return d
}

func (d *Dispatcher) work(queue <-chan event) {
	defer d.wg.Done()
	for {
		select {
		case e := <-queue:
			d.dispatch(e)
		case <-d.stop:
			for {
				select {
				case e := <-queue:
					d.dispatch(e)
				default:
					return
				}
			}
		}
	}
}

// Notify queues a notification for a beat, and never blocks.
func (d *Dispatcher) Notify(m monitor.Monitor, beat store.Beat) {
	select {
	case d.shards[int(uint32(m.ID))%workers] <- event{monitor: m, beat: beat}:
	default:
		d.log.Error("notification queue is full, dropping an alert", "monitor", m.ID, "trace_id", beat.TraceID)
		telemetry.Metrics().RecordNotification("all", "dropped")
	}
}

// Close sends what's queued, but only waits closeTimeout so that a provider that's down can't
// hold up a restart. Notify stays safe to call afterwards.
func (d *Dispatcher) Close() {
	d.once.Do(func() { close(d.stop) })
	done := make(chan struct{})
	go func() {
		d.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(d.closeWait):
		d.log.Warn("stopped before all notifications were sent")
	}
}

func (d *Dispatcher) dispatch(e event) {
	ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
	defer cancel()
	notifications, err := d.store.ActiveNotificationsFor(ctx, e.monitor.ID)
	if err != nil {
		d.log.Error("read notifications", "monitor", e.monitor.ID, "error", err)
		return
	}
	if len(notifications) == 0 {
		return
	}
	message := d.buildMessage(ctx, e)
	for _, n := range notifications {
		d.sendWithRetry(n, message, e)
	}
}

func (d *Dispatcher) sendWithRetry(n store.Notification, message Message, e event) {
	var err error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), sendTimeout)
		err = Send(ctx, d.env, n.Type, n.Config, message)
		cancel()
		if err == nil {
			telemetry.Metrics().RecordNotification(n.Type, "sent")
			return
		}
		if !retryable(err) {
			break
		}
		if attempt < maxAttempts {
			select {
			case <-time.After(d.backoff * time.Duration(attempt)):
			case <-d.stop:
				attempt = maxAttempts
			}
		}
	}
	telemetry.Metrics().RecordNotification(n.Type, "failed")
	d.log.Error("send notification", "notification", n.ID, "name", n.Name, "type", n.Type, "monitor", e.monitor.ID, "error", err, "trace_id", e.beat.TraceID)
}

// SendText sends a message that isn't about a beat, such as a certificate about to expire, to a
// monitor's active notifications. It waits for the result, and reports whether any accepted it.
func (d *Dispatcher) SendText(ctx context.Context, m monitor.Monitor, text string) bool {
	notifications, err := d.store.ActiveNotificationsFor(ctx, m.ID)
	if err != nil {
		d.log.Error("read notifications", "monitor", m.ID, "error", err)
		return false
	}
	sent := false
	for _, n := range notifications {
		sendCtx, cancel := context.WithTimeout(ctx, sendTimeout)
		err := Send(sendCtx, d.env, n.Type, n.Config, Message{Text: text})
		cancel()
		if err != nil {
			telemetry.Metrics().RecordNotification(n.Type, "failed")
			d.log.Error("send notification", "notification", n.ID, "name", n.Name, "type", n.Type, "monitor", m.ID, "error", err)
			continue
		}
		telemetry.Metrics().RecordNotification(n.Type, "sent")
		sent = true
	}
	return sent
}

// retryable is true for failures that may pass by themselves. A rejected request wouldn't.
func retryable(err error) bool {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return httpErr.Status == 429 || httpErr.Status >= 500
	}
	return true
}

func (d *Dispatcher) buildMessage(ctx context.Context, e event) Message {
	label := "🔴 Down"
	if e.beat.Status == monitor.StatusUp {
		label = "✅ Up"
	}
	text := "[" + e.monitor.Name + "] [" + label + "] " + e.beat.Msg

	beatMsg := e.beat.Msg
	if beatMsg == "" {
		beatMsg = "N/A"
	}
	at := e.beat.Time.UTC()
	local := at.In(d.location)
	_, offset := local.Zone()
	beat := M{
		"monitorID": e.beat.MonitorID, "status": e.beat.Status, "time": at.Format(beatTime), "msg": beatMsg,
		"important": e.beat.Important, "duration": e.beat.Duration, "retries": e.beat.Retries, "ping": nil,
		"timezone": d.location.String(), "timezoneOffset": formatOffset(offset), "localDateTime": local.Format(sqlDateTime),
	}
	if e.beat.Ping != nil {
		beat["ping"] = *e.beat.Ping
	}
	if e.beat.TraceID != "" {
		beat["traceId"] = e.beat.TraceID
	}
	if e.beat.Response != nil {
		beat["response"] = *e.beat.Response
	}
	if e.beat.Status == monitor.StatusUp {
		if down, err := d.store.LastDownTime(ctx, e.monitor.ID); err == nil && down != nil {
			beat["lastDownTime"] = down.UTC().Format(beatTime)
		}
	}
	return Message{Text: text, Monitor: d.monitorJSON(ctx, e.monitor), Beat: beat}
}

func (d *Dispatcher) monitorJSON(ctx context.Context, m monitor.Monitor) M {
	path := []any{m.Name}
	pathName := m.Name
	if names, err := d.store.MonitorPath(ctx, m.ID); err == nil && len(names) > 0 {
		path = anyList(names)
		pathName = strings.Join(names, " / ")
	}
	result := M{
		"id": m.ID, "name": m.Name, "type": m.Type, "active": m.Active, "interval": m.Interval, "maxretries": m.MaxRetries,
		"parent": nil, "path": path, "pathName": pathName, "description": nil, "tags": []any{},
		"url": nil, "hostname": nil, "port": nil, "keyword": nil,
	}
	if m.ParentID != nil {
		result["parent"] = *m.ParentID
	}
	switch m.Type {
	case monitor.TypeHTTP, monitor.TypeKeyword:
		result["url"] = redactURL(m)
		if m.Type == monitor.TypeKeyword {
			result["keyword"] = m.Keyword
		}
	case monitor.TypePort:
		result["hostname"], result["port"] = m.Hostname, m.Port
	}
	return result
}

// redactURL leaves credentials out of what goes to third parties.
func redactURL(m monitor.Monitor) string {
	return m.Redacted().URL
}

func formatOffset(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	pad := func(n int) string {
		text := strconv.Itoa(n)
		if n < 10 {
			return "0" + text
		}
		return text
	}
	return sign + pad(seconds/3600) + ":" + pad(seconds%3600/60)
}

// SendTest sends a test message using a configuration, and reports what happened.
func SendTest(ctx context.Context, env *Env, kind, name string, config M) error {
	return Send(ctx, env, kind, config, Message{Text: name + " Testing"})
}
