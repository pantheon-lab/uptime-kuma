package store

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/secrets"
)

// newTestStore creates a throwaway database. It needs TEST_DATABASE_URL to point
// at a TimescaleDB server, otherwise the test is skipped.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	admin := os.Getenv("TEST_DATABASE_URL")
	if admin == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("kuma_test_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		s.Close()
		_, _ = conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = conn.Close(ctx)
	})
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	// TEST_SECRET_KEY runs the tests with stored secrets encrypted.
	if text := os.Getenv("TEST_SECRET_KEY"); text != "" {
		key, err := secrets.ParseKey(text)
		if err != nil {
			t.Fatal(err)
		}
		keys, _ := secrets.New(key)
		s.SetKeyring(keys)
	}
	return s
}

func ptr(v float64) *float64 { return &v }

func TestMigrateIsRepeatable(t *testing.T) {
	s := newTestStore(t)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("second migration: %v", err)
	}
}

func TestRetentionCanChange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	for _, days := range []int{30, 90, 0} {
		if err := s.SetRetention(ctx, days); err != nil {
			t.Fatalf("retention %d: %v", days, err)
		}
		var jobs int
		err := s.pool.QueryRow(ctx, `SELECT count(*) FROM timescaledb_information.jobs WHERE proc_name = 'policy_retention'`).Scan(&jobs)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if days == 0 {
			want = 0
		}
		if jobs != want {
			t.Errorf("retention %d: %d jobs, want %d", days, jobs, want)
		}
	}
	if err := s.SetRetention(ctx, -1); err == nil {
		t.Error("negative retention should fail")
	}
}

func TestInsertIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Microsecond)
	batch := []Beat{
		{MonitorID: 1, Time: at, Status: 1, Ping: ptr(10), Msg: "ok", Important: true, Duration: 60, Retries: 2},
		{MonitorID: 1, Time: at, Status: 0},
		{MonitorID: 2, Time: at, Status: 0},
	}
	inserted, err := s.InsertBeats(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 2 {
		t.Fatalf("first insert returned %d beats, want 2", len(inserted))
	}
	inserted, err = s.InsertBeats(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}
	if len(inserted) != 0 {
		t.Fatalf("replay inserted %d beats, want 0", len(inserted))
	}

	beats, err := s.LatestBeats(ctx, 1, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(beats) != 1 {
		t.Fatalf("got %d beats, want 1", len(beats))
	}
	b := beats[0]
	if !b.Time.Equal(at) || b.Status != 1 || *b.Ping != 10 || b.Msg != "ok" || !b.Important || b.Duration != 60 || b.Retries != 2 {
		t.Errorf("beat did not round-trip: %+v", b)
	}
	down, err := s.LatestBeats(ctx, 2, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(down) != 1 || down[0].Ping != nil {
		t.Errorf("null ping did not round-trip: %+v", down)
	}
}

// seed writes beats for monitor 1 at 1-minute spacing, ending now: status cycles
// through up, up, up, down, pending, maintenance. Pings are 10..., only on up beats.
func seed(t *testing.T, s *Store, start time.Time, minutes int) {
	t.Helper()
	beats := make([]Beat, 0, minutes)
	cycle := []int16{1, 1, 1, 0, 2, 3}
	for i := 0; i < minutes; i++ {
		status := cycle[i%len(cycle)]
		b := Beat{MonitorID: 1, Time: start.Add(time.Duration(i) * time.Minute), Status: status}
		if status == 1 {
			b.Ping = ptr(float64(10 + i%3*10))
		}
		beats = append(beats, b)
		if len(beats) == 5000 || i == minutes-1 {
			if _, err := s.InsertBeats(context.Background(), beats); err != nil {
				t.Fatal(err)
			}
			beats = beats[:0]
		}
	}
}

func sumChart(points []ChartPoint) (up, down, maintenance int64) {
	for _, p := range points {
		up += p.Up
		down += p.Down
		maintenance += p.Maintenance
	}
	return
}

func TestChartAndStatsMatchRawData(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const days = 3
	minutes := days * 24 * 60
	seed(t, s, time.Now().UTC().Add(-time.Duration(minutes)*time.Minute).Truncate(time.Minute), minutes)

	check := func(t *testing.T) {
		t.Helper()
		// Whole cycles of 6 beats: 3 up, 2 down, 1 maintenance.
		for _, hours := range []int{1, 6, 24, 48, 24 * 31} {
			points, err := s.Chart(ctx, 1, hours)
			if err != nil {
				t.Fatalf("chart %dh: %v", hours, err)
			}
			if len(points) == 0 {
				t.Fatalf("chart %dh is empty", hours)
			}
			up, down, maintenance := sumChart(points)
			if up == 0 || down == 0 || maintenance == 0 {
				t.Errorf("chart %dh lost a status: up=%d down=%d maintenance=%d", hours, up, down, maintenance)
			}
			if got := float64(up) / float64(down); math.Abs(got-1.5) > 0.1 {
				t.Errorf("chart %dh: up/down = %.3f, want about 1.5", hours, got)
			}
			if got := float64(up) / float64(maintenance); math.Abs(got-3) > 0.2 {
				t.Errorf("chart %dh: up/maintenance = %.3f, want about 3", hours, got)
			}
			for _, p := range points {
				if p.AvgPing != nil && (*p.AvgPing < 10 || *p.AvgPing > 30 || *p.MinPing < 10 || *p.MaxPing > 30) {
					t.Errorf("chart %dh: ping out of range: %+v", hours, p)
				}
			}
			if hours == 24*31 {
				up, down, _ := sumChart(points)
				if total := up + down; total < int64(minutes)*5/6-10 || total > int64(minutes)*5/6+10 {
					t.Errorf("all-time chart counts %d beats, want about %d", total, minutes*5/6)
				}
			}
		}

		stats, err := s.Stats(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if len(stats) != 1 || stats[0].MonitorID != 1 {
			t.Fatalf("stats: %+v", stats)
		}
		m := stats[0]
		for name, v := range map[string]*float64{"24h": m.Uptime24h, "30d": m.Uptime30d, "1y": m.Uptime1y} {
			if v == nil || math.Abs(*v-0.6) > 0.01 {
				t.Errorf("uptime %s = %v, want about 0.6", name, v)
			}
		}
		if m.AvgPing == nil || math.Abs(*m.AvgPing-20) > 1 {
			t.Errorf("avg ping = %v, want about 20", m.AvgPing)
		}
	}

	t.Run("real-time, nothing materialized", check)

	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	t.Run("after materializing", check)

	if _, err := s.pool.Exec(ctx, `SELECT compress_chunk(c) FROM show_chunks('heartbeat', older_than => INTERVAL '1 day') c`); err != nil {
		t.Fatal(err)
	}
	t.Run("after compressing raw chunks", check)

	// A replayed beat that lands in a compressed chunk must still be ignored.
	old := time.Now().UTC().Add(-time.Duration(minutes) * time.Minute).Truncate(time.Minute)
	inserted, err := s.InsertBeats(ctx, []Beat{{MonitorID: 1, Time: old, Status: 1}})
	if err != nil {
		t.Fatalf("insert into compressed chunk: %v", err)
	}
	if len(inserted) != 0 {
		t.Errorf("replay into compressed chunk inserted %d beats", len(inserted))
	}
}

func TestPercentiles(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	beats := []Beat{{MonitorID: 1, Time: now.Add(-time.Hour), Status: 0, Ping: ptr(100000)}}
	for i := 1; i <= 100; i++ {
		beats = append(beats, Beat{MonitorID: 1, Time: now.Add(-time.Duration(i) * time.Second), Status: 1, Ping: ptr(float64(i))})
	}
	if _, err := s.InsertBeats(ctx, beats); err != nil {
		t.Fatal(err)
	}
	values, err := s.Percentiles(ctx, 1, 24, []float64{0.5, 0.95, 1})
	if err != nil {
		t.Fatal(err)
	}
	for i, want := range []float64{50.5, 95.05, 100} {
		if values[i] == nil || math.Abs(*values[i]-want) > 0.01 {
			t.Errorf("percentile %d = %v, want %v (down beats must not count)", i, values[i], want)
		}
	}
	empty, err := s.Percentiles(ctx, 999, 24, []float64{0.5, 0.95})
	if err != nil {
		t.Fatal(err)
	}
	if len(empty) != 2 || empty[0] != nil || empty[1] != nil {
		t.Errorf("empty window: %v", empty)
	}
}

func group(name string, parent *int32) monitor.Monitor {
	m := monitor.New()
	m.Name, m.Type, m.ParentID, m.Active = name, monitor.TypeGroup, parent, false
	return m
}

func member(name string, parent *int32) monitor.Monitor {
	m := monitor.New()
	m.Name, m.Type, m.Hostname, m.Port, m.ParentID, m.Active = name, monitor.TypePort, "127.0.0.1", 1, parent, false
	return m
}

func TestGroupTreeRules(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	top, err := s.CreateMonitor(ctx, group("top", nil))
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateMonitor(ctx, group("inner", &top.ID))
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := s.CreateMonitor(ctx, member("leaf", &inner.ID))
	if err != nil {
		t.Fatal(err)
	}

	missing := int32(99999)
	if _, err := s.CreateMonitor(ctx, member("x", &missing)); !errors.Is(err, ErrInvalidParent) {
		t.Errorf("missing parent: %v", err)
	}
	if _, err := s.CreateMonitor(ctx, member("x", &leaf.ID)); !errors.Is(err, ErrInvalidParent) {
		t.Errorf("parent that isn't a group: %v", err)
	}
	top.ParentID = &inner.ID
	if _, err := s.UpdateMonitor(ctx, top); !errors.Is(err, ErrInvalidParent) {
		t.Errorf("cycle through a child: %v", err)
	}
	top.ParentID = &top.ID
	if _, err := s.UpdateMonitor(ctx, top); !errors.Is(err, ErrInvalidParent) {
		t.Errorf("group inside itself: %v", err)
	}

	children, err := s.GroupChildren(ctx, inner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(children) != 1 || children[0].Name != "leaf" || children[0].Status != nil {
		t.Errorf("children: %+v", children)
	}
	if _, err := s.InsertBeats(ctx, []Beat{{MonitorID: leaf.ID, Time: time.Now().UTC().Truncate(time.Microsecond), Status: monitor.StatusDown}}); err != nil {
		t.Fatal(err)
	}
	children, err = s.GroupChildren(ctx, inner.ID)
	if err != nil {
		t.Fatal(err)
	}
	if children[0].Status == nil || *children[0].Status != monitor.StatusDown {
		t.Errorf("child status: %+v", children[0])
	}

	// Deleting a group keeps its monitors.
	if err := s.DeleteMonitor(ctx, inner.ID); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetMonitor(ctx, leaf.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.ParentID != nil {
		t.Errorf("parent after deleting the group: %v", *got.ParentID)
	}

	// A group that changes type releases its children.
	other, err := s.CreateMonitor(ctx, group("other", nil))
	if err != nil {
		t.Fatal(err)
	}
	got.ParentID = &other.ID
	if _, err := s.UpdateMonitor(ctx, got); err != nil {
		t.Fatal(err)
	}
	other.Type, other.Hostname, other.Port = monitor.TypePort, "127.0.0.1", 1
	if _, err := s.UpdateMonitor(ctx, other); err != nil {
		t.Fatal(err)
	}
	if got, err = s.GetMonitor(ctx, leaf.ID); err != nil || got.ParentID != nil {
		t.Errorf("after the group changed type: %+v, %v", got.ParentID, err)
	}
}

func TestGroupUptimeIsShareOfSuccessfulChecks(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	top, err := s.CreateMonitor(ctx, group("top", nil))
	if err != nil {
		t.Fatal(err)
	}
	inner, err := s.CreateMonitor(ctx, group("inner", &top.ID))
	if err != nil {
		t.Fatal(err)
	}
	direct, err := s.CreateMonitor(ctx, member("direct", &top.ID))
	if err != nil {
		t.Fatal(err)
	}
	nested, err := s.CreateMonitor(ctx, member("nested", &inner.ID))
	if err != nil {
		t.Fatal(err)
	}
	outside, err := s.CreateMonitor(ctx, member("outside", nil))
	if err != nil {
		t.Fatal(err)
	}

	// direct: 3 up, 1 down. nested: 1 up, 1 pending, 2 maintenance. outside: 5 down.
	// The group's own status beat must not count either.
	now := time.Now().UTC().Truncate(time.Minute).Add(-30 * time.Minute)
	statuses := map[int32][]int16{
		direct.ID:  {1, 1, 1, 0},
		nested.ID:  {1, 2, 3, 3},
		outside.ID: {0, 0, 0, 0, 0},
		top.ID:     {0, 0, 0, 0, 0, 0, 0},
	}
	var beats []Beat
	for id, list := range statuses {
		for i, status := range list {
			beats = append(beats, Beat{MonitorID: id, Time: now.Add(time.Duration(i) * time.Second), Status: status})
		}
	}
	if _, err := s.InsertBeats(ctx, beats); err != nil {
		t.Fatal(err)
	}

	uptime := func(name string) float64 {
		t.Helper()
		var value float64
		err := s.pool.QueryRow(ctx, `
			SELECT sum(h.up)::float8 / NULLIF(sum(h.up + h.down), 0)
			FROM uptime_hourly h JOIN monitor_group_member g USING (monitor_id)
			WHERE g.group_name = $1 AND h.bucket >= now() - INTERVAL '2 hours'`, name).Scan(&value)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return value
	}
	// top: (3 + 1) up of (3 + 1 + 1 + 1) counted, inner: 1 of 2.
	if got := uptime("top"); math.Abs(got-4.0/6.0) > 1e-9 {
		t.Errorf("top = %v, want %v", got, 4.0/6.0)
	}
	if got := uptime("inner"); math.Abs(got-0.5) > 1e-9 {
		t.Errorf("inner = %v, want 0.5", got)
	}
}

func TestReadOnlyRoleCantSeeSecrets(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	password := "it's a \"tricky\" password"
	role := fmt.Sprintf("kuma_grafana_%d", time.Now().UnixNano())
	if err := s.EnsureReadOnlyRole(ctx, role, password); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = s.pool.Exec(ctx, `DROP OWNED BY "`+role+`"`)
		_, _ = s.pool.Exec(ctx, `DROP ROLE "`+role+`"`)
	})
	if err := s.EnsureReadOnlyRole(ctx, role, password); err != nil {
		t.Fatalf("second run: %v", err)
	}

	config := s.pool.Config().ConnConfig.Copy()
	config.User, config.Password = role, password
	conn, err := pgx.ConnectConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	for _, allowed := range []string{
		`SELECT count(*) FROM heartbeat`,
		`SELECT count(*) FROM stat_hourly`,
		`SELECT count(*) FROM stat_daily`,
		`SELECT count(*) FROM uptime_hourly`,
		`SELECT count(*) FROM uptime_daily`,
		`SELECT count(*) FROM monitor_group_member`,
		`SELECT id, name, type, active, parent_id FROM monitor`,
	} {
		rows, err := conn.Query(ctx, allowed)
		if err == nil {
			rows.Close()
			err = rows.Err()
		}
		if err != nil {
			t.Errorf("%s: %v", allowed, err)
		}
	}
	for _, denied := range []string{
		`SELECT url FROM monitor`,
		`SELECT headers FROM monitor`,
		`SELECT body FROM monitor`,
		`SELECT * FROM monitor`,
		`SELECT config FROM notification`,
		`SELECT * FROM notification_import`,
		`SELECT * FROM monitor_notification`,
		`DELETE FROM heartbeat`,
		`UPDATE monitor SET name = 'x'`,
		`INSERT INTO heartbeat (monitor_id, time, status) VALUES (1, now(), 1)`,
	} {
		rows, err := conn.Query(ctx, denied)
		if err == nil {
			rows.Close()
			err = rows.Err()
		}
		if err == nil {
			t.Errorf("%s should be denied", denied)
		}
	}
}

func TestRetryConcurrentRefresh(t *testing.T) {
	concurrent := &pgconn.PgError{Code: pgLockNotAvailable}
	other := errors.New("other failure")

	attempts := 0
	err := retryConcurrentRefresh(context.Background(), func() error {
		attempts++
		if attempts < 3 {
			return fmt.Errorf("wrapped: %w", concurrent)
		}
		return nil
	})
	if err != nil || attempts != 3 {
		t.Errorf("should retry until it works: %v after %d attempts", err, attempts)
	}

	attempts = 0
	err = retryConcurrentRefresh(context.Background(), func() error {
		attempts++
		return other
	})
	if !errors.Is(err, other) || attempts != 1 {
		t.Errorf("other errors must not retry: %v after %d attempts", err, attempts)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err = retryConcurrentRefresh(ctx, func() error { return concurrent })
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		t.Errorf("should give up with the last error when the context ends: %v", err)
	}
}

func TestOverlappingDeletesSucceed(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	at := time.Now().UTC().Truncate(time.Hour).Add(-48 * time.Hour)
	var ids []int32
	var beats []Beat
	for i := 0; i < 8; i++ {
		m, err := s.CreateMonitor(ctx, member(fmt.Sprintf("m%d", i), nil))
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, m.ID)
		for h := 0; h < 48; h++ {
			beats = append(beats, Beat{MonitorID: m.ID, Time: at.Add(time.Duration(h) * time.Hour), Status: 1})
		}
	}
	if _, err := s.InsertBeats(ctx, beats); err != nil {
		t.Fatal(err)
	}
	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}

	errs := make(chan error, len(ids))
	for _, id := range ids {
		go func() { errs <- s.DeleteMonitor(ctx, id) }()
	}
	for range ids {
		if err := <-errs; err != nil {
			t.Errorf("overlapping delete: %v", err)
		}
	}
	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 0 {
		t.Errorf("stats still report deleted monitors: %+v", stats)
	}
}

func TestBeatsAfterARefreshStillCount(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m, err := s.CreateMonitor(ctx, member("site", nil))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	beat := func(at time.Time) {
		t.Helper()
		if _, err := s.InsertBeats(ctx, []Beat{{MonitorID: m.ID, Time: at, Status: monitor.StatusUp}}); err != nil {
			t.Fatal(err)
		}
	}
	beat(now.Add(-2 * time.Hour))
	beat(now.Add(-2 * time.Second))
	if err := s.RefreshAggregates(ctx); err != nil {
		t.Fatal(err)
	}
	beat(now.Add(-time.Second))

	for _, view := range []string{"uptime_hourly", "uptime_daily", "stat_hourly", "stat_daily"} {
		var up int64
		err := s.pool.QueryRow(ctx, `SELECT COALESCE(sum(up), 0)::bigint FROM `+view+` WHERE monitor_id = $1`, m.ID).Scan(&up)
		if err != nil {
			t.Fatal(err)
		}
		if up != 3 {
			t.Errorf("%s has %d beats, want 3: a beat after a refresh went missing", view, up)
		}
	}
}

func TestNotifications(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	newMonitor := func(name string) monitor.Monitor {
		m := monitor.New()
		m.Name, m.Type, m.URL, m.ResendInterval = name, monitor.TypeHTTP, "https://example.com", 4
		created, err := s.CreateMonitor(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		return created
	}
	first := newMonitor("first")
	if first.ResendInterval != 4 {
		t.Errorf("resend interval wasn't stored: %d", first.ResendInterval)
	}

	config := map[string]any{"webhookURL": "https://hooks.example", "smtpPort": float64(587), "nested": map[string]any{"a": true}}
	on, err := s.CreateNotification(ctx, Notification{Name: "on", Type: "webhook", Config: config, Active: true, IsDefault: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(on.Config, config) {
		t.Errorf("config changed in storage: %v", on.Config)
	}
	off, err := s.CreateNotification(ctx, Notification{Name: "off", Type: "webhook", Config: config}, false)
	if err != nil {
		t.Fatal(err)
	}

	if ids, err := s.MonitorNotificationIDs(ctx, first.ID); err != nil || !slices.Equal(ids, []int32{on.ID}) {
		t.Fatalf("applying to all: %v %v", ids, err)
	}
	if err := s.SetMonitorNotifications(ctx, first.ID, []int32{on.ID, off.ID, on.ID}); err != nil {
		t.Fatal(err)
	}
	active, err := s.ActiveNotificationsFor(ctx, first.ID)
	if err != nil || len(active) != 1 || active[0].ID != on.ID {
		t.Fatalf("an inactive notification would be sent: %v %v", active, err)
	}
	if err := s.SetMonitorNotifications(ctx, first.ID, []int32{on.ID, 9999}); !errors.Is(err, ErrInvalidNotification) {
		t.Fatalf("unknown notification: %v", err)
	}
	// That failure must not have changed anything.
	if ids, _ := s.MonitorNotificationIDs(ctx, first.ID); len(ids) != 2 {
		t.Fatalf("a failed change left %v", ids)
	}
	if err := s.SetMonitorNotifications(ctx, 9999, nil); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown monitor: %v", err)
	}

	second := newMonitor("second")
	if err := s.LinkDefaultNotifications(ctx, second.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.MonitorNotificationIDs(ctx, second.ID); !slices.Equal(ids, []int32{on.ID}) {
		t.Fatalf("defaults: %v", ids)
	}

	on.Name = "renamed"
	if updated, err := s.UpdateNotification(ctx, on, false); err != nil || updated.Name != "renamed" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if err := s.DeleteNotification(ctx, on.ID); err != nil {
		t.Fatal(err)
	}
	if ids, _ := s.MonitorNotificationIDs(ctx, second.ID); len(ids) != 0 {
		t.Fatalf("delete didn't remove links: %v", ids)
	}
	if err := s.DeleteNotification(ctx, on.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("delete twice: %v", err)
	}
	if _, err := s.GetNotification(ctx, on.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get deleted: %v", err)
	}
}

func TestDownCountSurvivesARestart(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	m := monitor.New()
	m.Name, m.Type, m.URL = "m", monitor.TypeHTTP, "https://example.com"
	m.Active = true
	created, err := s.CreateMonitor(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().UTC().Add(-time.Minute).Truncate(time.Millisecond)
	beats := []Beat{
		{MonitorID: created.ID, Time: at, Status: monitor.StatusDown, Important: true},
		{MonitorID: created.ID, Time: at.Add(time.Second), Status: monitor.StatusDown, DownCount: 3},
	}
	if _, err := s.InsertBeats(ctx, beats); err != nil {
		t.Fatal(err)
	}
	states, err := s.LatestStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if states[created.ID].DownCount != 3 {
		t.Fatalf("state: %+v", states[created.ID])
	}
	down, err := s.LastDownTime(ctx, created.ID)
	if err != nil || down == nil || !down.Equal(at) {
		t.Fatalf("last down: %v %v", down, err)
	}
	path, err := s.MonitorPath(ctx, created.ID)
	if err != nil || !slices.Equal(path, []string{"m"}) {
		t.Fatalf("path: %v %v", path, err)
	}
}

func TestChartRange(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	const minutes = 2 * 24 * 60
	start := time.Now().UTC().Add(-minutes * time.Minute).Truncate(time.Minute)
	seed(t, s, start, minutes)

	// Two hours from raw beats, minute by minute
	from := start.Add(24 * time.Hour)
	points, err := s.ChartRange(ctx, 1, from, from.Add(2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(points) < 118 || len(points) > 121 {
		t.Fatalf("2h range: %d points, want about 120", len(points))
	}
	if points[0].Time.Before(from) || points[len(points)-1].Time.After(from.Add(2*time.Hour)) {
		t.Errorf("points outside the range: %v to %v", points[0].Time, points[len(points)-1].Time)
	}
	up, down, maintenance := sumChart(points)
	if up+down+maintenance < 118 {
		t.Errorf("2h range counts %d beats", up+down+maintenance)
	}

	// The same window as Chart's
	byHours, err := s.Chart(ctx, 1, 6)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	byRange, err := s.ChartRange(ctx, 1, now.Add(-6*time.Hour), now)
	if err != nil {
		t.Fatal(err)
	}
	if d := len(byHours) - len(byRange); d < -1 || d > 1 {
		t.Errorf("Chart(6h) has %d points, ChartRange(6h) %d", len(byHours), len(byRange))
	}

	// A range before any beat is empty
	if points, err := s.ChartRange(ctx, 1, start.Add(-48*time.Hour), start.Add(-47*time.Hour)); err != nil || len(points) != 0 {
		t.Errorf("empty range: %d points, %v", len(points), err)
	}
}
