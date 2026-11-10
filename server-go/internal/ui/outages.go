package ui

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// maxOutagesShown bounds the outages one answer lists, newest first.
const maxOutagesShown = 100

// The status changes read to find outages, which tests lower.
var (
	// outageEventBatch is how many status changes are read at a time to find them.
	outageEventBatch = 5000
	// maxOutageEvents bounds the status changes read in all. Past it, outages are found from the
	// oldest read, and the answer says so, rather than joined across the changes left out.
	maxOutageEvents = 200_000
)

// outage is a time a monitor was down: from a status change to DOWN until the next change to
// UP or MAINTENANCE. An outage that hasn't ended has no end.
type outage struct {
	Start   time.Time
	End     *time.Time
	Msg     string
	TraceID string
}

// findOutages turns a monitor's status changes, oldest first, into its outages, oldest first.
// Changes to PENDING don't end one: a monitor that's down stays down until it's up again. An
// UP before the first DOWN ends an outage that began before the changes, and is left out.
func findOutages(changes []store.Beat) []outage {
	var out []outage
	var current *outage
	for _, b := range changes {
		switch b.Status {
		case monitor.StatusDown:
			if current == nil {
				current = &outage{Start: b.Time, Msg: b.Msg, TraceID: b.TraceID}
			}
		case monitor.StatusUp, monitor.StatusMaintenance:
			if current != nil {
				end := b.Time
				current.End = &end
				out = append(out, *current)
				current = nil
			}
		}
	}
	if current != nil {
		out = append(out, *current)
	}
	return out
}

// monitorOutages answers getMonitorOutages(monitorID, days): the monitor's outages in the last
// days, newest first, including one that began before them and lasted into them, how many there
// were and how long it was down within the days. Clearing the monitor's events doesn't hide them.
func (s *Server) monitorOutages(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	days := flexInt{value: 30, set: true}
	if err := arg(args, 1, &days); err != nil {
		return nil, userErr(err)
	}
	if days.value < 1 || days.value > 365 {
		return nil, userErr(errors.New("days must be from 1 to 365"))
	}
	now := s.now()
	since := now.Add(-time.Duration(days.value) * 24 * time.Hour)
	var newestFirst []store.Beat
	for {
		batch, err := s.store.ImportantBeats(ctx, &id, store.EventFilter{Since: &since, IncludeCleared: true},
			len(newestFirst), outageEventBatch)
		if err != nil {
			return nil, err
		}
		newestFirst = append(newestFirst, batch...)
		if len(batch) < outageEventBatch || len(newestFirst) >= maxOutageEvents {
			break
		}
	}
	// Too many to read: the days are taken to start at the oldest change read.
	partial := len(newestFirst) >= maxOutageEvents
	if partial {
		since = newestFirst[len(newestFirst)-1].Time
	}
	// The status the monitor had as the days began: down then means an outage that began before.
	before, err := s.store.ImportantBeats(ctx, &id, store.EventFilter{Before: &since, IncludeCleared: true}, 0, 1)
	if err != nil {
		return nil, err
	}
	var changes []store.Beat
	if len(before) == 1 && before[0].Status == monitor.StatusDown {
		changes = append(changes, before[0])
	}
	for i := len(newestFirst) - 1; i >= 0; i-- {
		changes = append(changes, newestFirst[i])
	}

	// An outage that ended as the days began, such as at the oldest change read, isn't in them.
	outages := slices.DeleteFunc(findOutages(changes), func(o outage) bool { return o.End != nil && !o.End.After(since) })
	var downtime time.Duration
	list := make([]map[string]any, 0, min(len(outages), maxOutagesShown))
	for i := len(outages) - 1; i >= 0; i-- {
		o := outages[i]
		end := now
		if o.End != nil {
			end = *o.End
		}
		// Only the part within the days counts toward their downtime.
		from := o.Start
		if from.Before(since) {
			from = since
		}
		downtime += end.Sub(from)
		if len(list) == maxOutagesShown {
			continue
		}
		item := map[string]any{
			"start":           o.Start.UTC().Format(beatTimeFormat),
			"end":             nil,
			"durationSeconds": int64(end.Sub(o.Start).Seconds()),
			"ongoing":         o.End == nil,
			"beganBefore":     o.Start.Before(since),
			"msg":             o.Msg,
		}
		if o.End != nil {
			item["end"] = o.End.UTC().Format(beatTimeFormat)
		}
		if o.TraceID != "" {
			item["traceId"] = o.TraceID
		}
		list = append(list, item)
	}
	var from any
	if partial {
		from = since.UTC().Format(beatTimeFormat)
	}
	return ok("outages", list, "count", len(outages), "downtimeSeconds", int64(downtime.Seconds()), "days", days.value,
		"partialFrom", from), nil
}

// chartRange answers getMonitorChartRange(monitorID, from, to), with times in RFC 3339: the
// chart between them, at the resolution getMonitorChartData uses for a window that long.
func (s *Server) chartRange(ctx context.Context, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	var fromText, toText string
	if err := arg(args, 1, &fromText); err != nil {
		return nil, userErr(err)
	}
	if err := arg(args, 2, &toText); err != nil {
		return nil, userErr(err)
	}
	from, err1 := time.Parse(time.RFC3339, fromText)
	to, err2 := time.Parse(time.RFC3339, toText)
	if err1 != nil || err2 != nil {
		return nil, userErr(errors.New("from and to must be times in RFC 3339"))
	}
	if !from.Before(to) || to.Sub(from) > maxPeriodHours*time.Hour {
		return nil, userErr(errors.New("the range must start before it ends, and be at most a year"))
	}
	points, err := s.store.ChartRange(ctx, id, from, to)
	if err != nil {
		return nil, err
	}
	return ok("data", chartJSON(points)), nil
}
