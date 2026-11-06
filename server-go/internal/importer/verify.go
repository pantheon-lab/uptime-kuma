package importer

import (
	"context"
	"database/sql"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// Checking the move: each imported monitor's beats, uptime and newest beat here against the
// source's, so that a cutover can be scripted on the result.

// uptimeTolerance is how far uptimes may differ, as a fraction: 0.01 percentage points, below
// what the pages show.
const uptimeTolerance = 0.0001

// VerifyTarget is what Verify reads here.
type VerifyTarget interface {
	ImportedMonitors(ctx context.Context) (map[int32]store.ImportedMonitor, error)
	RefreshAggregates(ctx context.Context) error
	BeatCount(ctx context.Context, monitorID int32, from, to time.Time) (int64, error)
	UptimeCounts(ctx context.Context, daily bool, monitorID int32, from, to time.Time) (up, down int64, err error)
	BeatStatusAt(ctx context.Context, monitorID int32, at time.Time) (int16, bool, error)
}

// Uptime is a window's uptime in the source and here, as fractions, or nil without checks.
type Uptime struct {
	Source *float64 `json:"source"`
	Here   *float64 `json:"here"`
}

// MonitorCheck is one monitor's comparison.
type MonitorCheck struct {
	SourceID    int32             `json:"sourceId"`
	MonitorID   int32             `json:"monitorId"`
	SourceBeats int64             `json:"sourceBeats"`
	Beats       int64             `json:"beats"`
	Uptime      map[string]Uptime `json:"uptime"`
	Differences []string          `json:"differences,omitempty"`
}

// VerifyReport is the result of Verify. OK is false when any monitor differs.
type VerifyReport struct {
	// AsOf is the source's newest beat. Uptime is compared over whole hours and days before it.
	AsOf      time.Time      `json:"asOf"`
	Monitors  int            `json:"monitors"`
	Matching  int            `json:"matching"`
	Differing []MonitorCheck `json:"differing"`
	// NotImported are source monitors that aren't here, such as skipped ones.
	NotImported int  `json:"notImported"`
	OK          bool `json:"ok"`
}

type window struct {
	name     string
	daily    bool
	from, to time.Time
}

// Verify compares every imported monitor with the source.
func Verify(ctx context.Context, source *sql.DB, target VerifyTarget) (*VerifyReport, error) {
	conn, err := source.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("connect to the source: %w", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := conn.ExecContext(ctx, "START TRANSACTION WITH CONSISTENT SNAPSHOT, READ ONLY"); err != nil {
		return nil, fmt.Errorf("start a read-only transaction: %w", err)
	}
	defer func() { _, _ = conn.ExecContext(context.WithoutCancel(ctx), "ROLLBACK") }()

	report := &VerifyReport{Differing: []MonitorCheck{}}
	rows, _, err := readRows(ctx, conn, "SELECT max(`time`) AS newest FROM heartbeat")
	if err != nil {
		return nil, fmt.Errorf("read the source's newest beat: %w", err)
	}
	if len(rows) == 0 || !rows[0]["newest"].Valid {
		report.OK = true
		return report, nil
	}
	report.AsOf = sourceTime(rows[0]["newest"], time.Time{})
	hour := report.AsOf.Truncate(time.Hour)
	day := report.AsOf.Truncate(24 * time.Hour)
	windows := []window{
		{"24h", false, hour.Add(-24 * time.Hour), hour},
		{"30d", true, day.AddDate(0, 0, -30), day},
		{"365d", true, day.AddDate(0, 0, -365), day},
	}

	imported, err := target.ImportedMonitors(ctx)
	if err != nil {
		return nil, err
	}
	if err := target.RefreshAggregates(ctx); err != nil {
		return nil, err
	}
	sourceMonitors, _, err := readRows(ctx, conn, "SELECT id FROM monitor")
	if err != nil {
		return nil, fmt.Errorf("read monitors: %w", err)
	}
	ids := make([]int32, 0, len(imported))
	for _, row := range sourceMonitors {
		id := int32(intOf(row["id"]))
		if _, found := imported[id]; found {
			ids = append(ids, id)
		} else {
			report.NotImported++
		}
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })

	for _, sourceID := range ids {
		check, err := verifyMonitor(ctx, conn, target, sourceID, imported[sourceID].MonitorID, windows)
		if err != nil {
			return nil, fmt.Errorf("monitor %d: %w", sourceID, err)
		}
		report.Monitors++
		if len(check.Differences) == 0 {
			report.Matching++
		} else {
			report.Differing = append(report.Differing, check)
		}
	}
	report.OK = len(report.Differing) == 0
	return report, nil
}

func verifyMonitor(ctx context.Context, conn *sql.Conn, target VerifyTarget, sourceID, monitorID int32, windows []window) (MonitorCheck, error) {
	check := MonitorCheck{SourceID: sourceID, MonitorID: monitorID, Uptime: map[string]Uptime{}}
	// Beats with a status the import refuses aren't counted.
	rows, _, err := readRows(ctx, conn, "SELECT count(*) AS n, min(`time`) AS first, max(`time`) AS last FROM heartbeat "+
		"WHERE monitor_id = ? AND status BETWEEN 0 AND 3", sourceID)
	if err != nil {
		return check, err
	}
	check.SourceBeats = intOf(rows[0]["n"])
	if check.SourceBeats > 0 {
		first, last := sourceTime(rows[0]["first"], time.Time{}), sourceTime(rows[0]["last"], time.Time{})
		if check.Beats, err = target.BeatCount(ctx, monitorID, first, last); err != nil {
			return check, err
		}
		if check.Beats != check.SourceBeats {
			check.Differences = append(check.Differences, fmt.Sprintf("beats: %d in the source, %d here", check.SourceBeats, check.Beats))
		}
		newest, _, err := readRows(ctx, conn, "SELECT status FROM heartbeat WHERE monitor_id = ? AND `time` = ? LIMIT 1",
			sourceID, rows[0]["last"].String)
		if err != nil {
			return check, err
		}
		status, found, err := target.BeatStatusAt(ctx, monitorID, last)
		if err != nil {
			return check, err
		}
		switch {
		case !found:
			check.Differences = append(check.Differences, "the newest beat, at "+last.Format(time.RFC3339)+", isn't here")
		case len(newest) > 0 && int64(status) != intOf(newest[0]["status"]):
			check.Differences = append(check.Differences, fmt.Sprintf("the newest beat is %s in the source, %d here",
				newest[0]["status"].String, status))
		}
	}

	for _, w := range windows {
		table := "stat_hourly"
		if w.daily {
			table = "stat_daily"
		}
		stats, err := optionalTable(ctx, conn, "SELECT COALESCE(sum(up), 0) AS up, COALESCE(sum(down), 0) AS down FROM "+table+
			" WHERE monitor_id = ? AND `timestamp` >= ? AND `timestamp` < ?", sourceID, w.from.Unix(), w.to.Unix())
		if err != nil {
			return check, err
		}
		var sourceUp, sourceDown int64
		if len(stats) > 0 {
			sourceUp, sourceDown = intOf(stats[0]["up"]), intOf(stats[0]["down"])
		}
		up, down, err := target.UptimeCounts(ctx, w.daily, monitorID, w.from, w.to)
		if err != nil {
			return check, err
		}
		u := Uptime{Source: ratio(sourceUp, sourceDown), Here: ratio(up, down)}
		check.Uptime[w.name] = u
		if !sameUptime(u) {
			check.Differences = append(check.Differences, fmt.Sprintf("uptime over %s: %s in the source, %s here",
				w.name, percent(u.Source), percent(u.Here)))
		}
	}
	return check, nil
}

func ratio(up, down int64) *float64 {
	if up+down == 0 {
		return nil
	}
	r := float64(up) / float64(up+down)
	return &r
}

func sameUptime(u Uptime) bool {
	if u.Source == nil || u.Here == nil {
		return u.Source == nil && u.Here == nil
	}
	return math.Abs(*u.Source-*u.Here) <= uptimeTolerance
}

func percent(r *float64) string {
	if r == nil {
		return "no checks"
	}
	return fmt.Sprintf("%.4f%%", *r*100)
}
