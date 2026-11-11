package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

var quiet = slog.New(slog.NewTextHandler(io.Discard, nil))

type fakeSink struct {
	mu    sync.Mutex
	beats []store.Beat
}

func (f *fakeSink) Add(b store.Beat) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.beats = append(f.beats, b)
}
func (f *fakeSink) Flush(context.Context) error { return nil }
func (f *fakeSink) snapshot() []store.Beat {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]store.Beat(nil), f.beats...)
}

type checkFunc func(context.Context, monitor.Monitor) monitor.Result

func (c checkFunc) Run(ctx context.Context, m monitor.Monitor) monitor.Result { return c(ctx, m) }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestRetryIntervalAndStatusSequence(t *testing.T) {
	sink := &fakeSink{}
	var calls atomic.Int32
	checker := checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		if calls.Add(1) == 1 {
			return monitor.Result{OK: true, Msg: "ok"}
		}
		return monitor.Result{Msg: "boom"}
	})
	s := New(checker, sink, 10, quiet)
	defer s.Stop()

	// The long interval would time the test out if retries didn't use retryInterval.
	s.Upsert(monitor.Monitor{ID: 1, Active: true, Interval: 1, RetryInterval: 1, MaxRetries: 1})
	waitFor(t, "three beats", func() bool { return len(sink.snapshot()) >= 3 })

	beats := sink.snapshot()
	want := []int16{monitor.StatusUp, monitor.StatusPending, monitor.StatusDown}
	for i, status := range want {
		if beats[i].Status != status {
			t.Errorf("beat %d: status %d, want %d", i, beats[i].Status, status)
		}
	}
	if !beats[0].Important || beats[1].Important || !beats[2].Important {
		t.Errorf("important flags: %v %v %v", beats[0].Important, beats[1].Important, beats[2].Important)
	}
	if gap := beats[1].Time.Sub(beats[0].Time); gap < 900*time.Millisecond || gap > 1500*time.Millisecond {
		t.Errorf("interval drifted: %v", gap)
	}
}

func TestStartResumesFromPreviousState(t *testing.T) {
	sink := &fakeSink{}
	checker := checkFunc(func(context.Context, monitor.Monitor) monitor.Result { return monitor.Result{OK: true} })
	s := New(checker, sink, 10, quiet)
	defer s.Stop()
	s.Start(
		[]monitor.Monitor{{ID: 1, Active: true, Interval: 1}, {ID: 2, Active: false, Interval: 1}, {ID: 3, Active: true, Interval: 1}},
		map[int32]monitor.Previous{1: {Status: monitor.StatusUp}, 3: {Status: monitor.StatusDown, Retries: 4}},
	)
	waitFor(t, "beats from both active monitors", func() bool {
		seen := map[int32]bool{}
		for _, b := range sink.snapshot() {
			seen[b.MonitorID] = true
		}
		return seen[1] && seen[3]
	})
	for _, b := range sink.snapshot() {
		switch b.MonitorID {
		case 1:
			if b.Important {
				t.Error("UP to UP after a restart must not be an event")
			}
		case 2:
			t.Error("paused monitor was checked")
		case 3:
			if !b.Important {
				t.Error("recovery across a restart must be an event")
			}
			return
		}
	}
}

func TestUpsertKeepsStateAndAppliesChanges(t *testing.T) {
	sink := &fakeSink{}
	var seen atomic.Value
	checker := checkFunc(func(_ context.Context, m monitor.Monitor) monitor.Result {
		seen.Store(m.Name)
		return monitor.Result{OK: true}
	})
	s := New(checker, sink, 10, quiet)
	defer s.Stop()

	s.Upsert(monitor.Monitor{ID: 1, Name: "old", Active: true, Interval: 3600})
	waitFor(t, "first beat", func() bool { return len(sink.snapshot()) == 1 })

	s.Upsert(monitor.Monitor{ID: 1, Name: "new", Active: true, Interval: 3600})
	waitFor(t, "beat after update", func() bool { return len(sink.snapshot()) == 2 })
	if seen.Load() != "new" {
		t.Errorf("checked with stale config: %v", seen.Load())
	}
	if sink.snapshot()[1].Important {
		t.Error("editing a monitor must not look like a first beat")
	}

	s.Upsert(monitor.Monitor{ID: 1, Name: "new", Active: false, Interval: 1})
	time.Sleep(1200 * time.Millisecond)
	if got := len(sink.snapshot()); got != 2 {
		t.Errorf("paused monitor kept beating: %d beats", got)
	}
}

func TestCancelledCheckMakesNoBeat(t *testing.T) {
	sink := &fakeSink{}
	started := make(chan struct{})
	checker := checkFunc(func(ctx context.Context, _ monitor.Monitor) monitor.Result {
		close(started)
		<-ctx.Done()
		return monitor.Result{Msg: "context canceled"}
	})
	s := New(checker, sink, 10, quiet)
	s.Upsert(monitor.Monitor{ID: 1, Active: true, Interval: 1})
	<-started
	if err := s.Remove(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	s.Stop()
	if got := sink.snapshot(); len(got) != 0 {
		t.Errorf("cancelled check produced beats: %+v", got)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	sink := &fakeSink{}
	var running, peak atomic.Int32
	checker := checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		n := running.Add(1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return monitor.Result{OK: true}
	})
	s := New(checker, sink, 3, quiet)
	defer s.Stop()
	for i := int32(1); i <= 30; i++ {
		s.Upsert(monitor.Monitor{ID: i, Active: true, Interval: 3600})
	}
	waitFor(t, "all checks", func() bool { return len(sink.snapshot()) == 30 })
	if p := peak.Load(); p > 3 || p < 2 {
		t.Errorf("peak concurrency %d, limit 3", p)
	}
}

func TestPanicBecomesFailedBeat(t *testing.T) {
	sink := &fakeSink{}
	checker := checkFunc(func(context.Context, monitor.Monitor) monitor.Result { panic("bug") })
	s := New(checker, sink, 10, quiet)
	defer s.Stop()
	s.Upsert(monitor.Monitor{ID: 1, Active: true, Interval: 3600})
	waitFor(t, "beat", func() bool { return len(sink.snapshot()) == 1 })
	if b := sink.snapshot()[0]; b.Status != monitor.StatusDown || b.Msg == "" {
		t.Errorf("%+v", b)
	}
}

type fakeInserter struct {
	mu      sync.Mutex
	fail    bool
	batches [][]store.Beat
}

func (f *fakeInserter) InsertBeats(_ context.Context, beats []store.Beat) ([]store.Beat, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return nil, errors.New("db down")
	}
	f.batches = append(f.batches, append([]store.Beat(nil), beats...))
	return beats, nil
}

func (f *fakeInserter) ids() []int32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []int32
	for _, batch := range f.batches {
		for _, b := range batch {
			ids = append(ids, b.MonitorID)
		}
	}
	return ids
}

func TestWriterBatchesAndPublishes(t *testing.T) {
	db := &fakeInserter{}
	var published atomic.Int32
	w := NewWriter(db, func(b []store.Beat) { published.Add(int32(len(b))) }, quiet)
	for i := int32(0); i < flushSize+5; i++ {
		w.Add(store.Beat{MonitorID: i})
	}
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(db.batches) != 2 || len(db.batches[0]) != flushSize || len(db.batches[1]) != 5 {
		t.Errorf("batches: %d", len(db.batches))
	}
	if published.Load() != flushSize+5 {
		t.Errorf("published %d", published.Load())
	}
}

func TestWriterKeepsBeatsInOrderWhileDatabaseIsDown(t *testing.T) {
	db := &fakeInserter{fail: true}
	w := NewWriter(db, func([]store.Beat) {}, quiet)
	w.Add(store.Beat{MonitorID: 1})
	w.Add(store.Beat{MonitorID: 2})
	if err := w.Flush(context.Background()); err == nil {
		t.Fatal("expected an error")
	}
	w.Add(store.Beat{MonitorID: 3})
	db.fail = false
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids := db.ids()
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Errorf("order after outage: %v", ids)
	}
}

func TestWriterDropsOldestWhenQueueIsFull(t *testing.T) {
	db := &fakeInserter{fail: true}
	w := NewWriter(db, func([]store.Beat) {}, quiet)
	w.maxQueue = 3
	for i := int32(1); i <= 5; i++ {
		w.Add(store.Beat{MonitorID: i})
	}
	_ = w.Flush(context.Background())
	w.Add(store.Beat{MonitorID: 6})
	db.fail = false
	if err := w.Flush(context.Background()); err != nil {
		t.Fatal(err)
	}
	ids := db.ids()
	if len(ids) != 3 || ids[0] != 4 || ids[2] != 6 {
		t.Errorf("kept %v, want the newest 3", ids)
	}
}

func TestWriterCloseFlushes(t *testing.T) {
	db := &fakeInserter{}
	w := NewWriter(db, func([]store.Beat) {}, quiet)
	go w.Run()
	w.Add(store.Beat{MonitorID: 1})
	if err := w.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ids := db.ids(); len(ids) != 1 {
		t.Errorf("flushed %v", ids)
	}
}

type switchableMaintenance struct{ on atomic.Bool }

func (m *switchableMaintenance) UnderMaintenance(int32) bool { return m.on.Load() }

type countingNotifier struct{ count atomic.Int32 }

func (n *countingNotifier) Notify(monitor.Monitor, store.Beat) { n.count.Add(1) }

func TestMaintenanceSkipsChecksAndAlerts(t *testing.T) {
	sink := &fakeSink{}
	var checks atomic.Int32
	checker := checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		checks.Add(1)
		return monitor.Result{Msg: "boom"}
	})
	window := &switchableMaintenance{}
	window.on.Store(true)
	notifier := &countingNotifier{}
	s := New(checker, sink, 10, quiet)
	s.SetNotifier(notifier)
	s.SetMaintenance(window)
	defer s.Stop()

	s.Upsert(monitor.Monitor{ID: 1, Active: true, Interval: 1})
	waitFor(t, "two maintenance beats", func() bool { return len(sink.snapshot()) >= 2 })
	beats := sink.snapshot()
	if beats[0].Status != monitor.StatusMaintenance || beats[0].Msg != monitor.MaintenanceMsg || !beats[0].Important || beats[1].Important {
		t.Fatalf("beats: %+v", beats[:2])
	}
	if checks.Load() != 0 || notifier.count.Load() != 0 {
		t.Fatalf("under maintenance: %d checks, %d alerts", checks.Load(), notifier.count.Load())
	}

	// When the window ends, checks resume, and a failure alerts.
	window.on.Store(false)
	waitFor(t, "a down beat", func() bool {
		beats := sink.snapshot()
		return beats[len(beats)-1].Status == monitor.StatusDown
	})
	waitFor(t, "the alert", func() bool { return notifier.count.Load() >= 1 })
	if checks.Load() == 0 {
		t.Fatal("no check after the window")
	}
}

func pushMonitor(id int32, token string, interval int32) monitor.Monitor {
	m := monitor.New()
	m.ID, m.Type, m.PushToken, m.Interval = id, monitor.TypePush, token, interval
	return m
}

func TestPushReachesItsMonitor(t *testing.T) {
	sink := &fakeSink{}
	checker := checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		t.Error("a push monitor is never checked")
		return monitor.Result{}
	})
	s := New(checker, sink, 10, quiet)
	defer s.Stop()

	s.Upsert(pushMonitor(1, "token-one", 60))
	if err := s.Push(context.Background(), "nope", monitor.Push{Up: true}); !errors.Is(err, monitor.ErrPushNotFound) {
		t.Errorf("unknown token: %v", err)
	}
	if err := s.Push(context.Background(), "", monitor.Push{Up: true}); !errors.Is(err, monitor.ErrPushNotFound) {
		t.Errorf("empty token: %v", err)
	}

	ping := 4.0
	if err := s.Push(context.Background(), "token-one", monitor.Push{Up: true, Msg: "beat", Ping: &ping}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the pushed beat", func() bool { return len(sink.snapshot()) >= 1 })
	beats := sink.snapshot()
	if len(beats) != 1 || beats[0].Status != monitor.StatusUp || beats[0].Msg != "beat" || beats[0].Ping == nil || *beats[0].Ping != 4 {
		t.Fatalf("beat: %+v", beats)
	}
	if !beats[0].Important {
		t.Error("the first beat is an event")
	}

	// Paused, the token leads nowhere, as in the Node.js backend.
	s.Upsert(monitor.Monitor{ID: 1, Active: false, Interval: 60})
	if err := s.Push(context.Background(), "token-one", monitor.Push{Up: true}); !errors.Is(err, monitor.ErrPushNotFound) {
		t.Errorf("paused: %v", err)
	}

	// An edit that replaced the token moves the sender's target with it.
	s.Upsert(pushMonitor(1, "token-two", 60))
	if err := s.Push(context.Background(), "token-one", monitor.Push{Up: true}); !errors.Is(err, monitor.ErrPushNotFound) {
		t.Errorf("old token after an edit: %v", err)
	}
	if err := s.Push(context.Background(), "token-two", monitor.Push{Up: true, Msg: "again"}); err != nil {
		t.Fatalf("new token: %v", err)
	}
	waitFor(t, "the beat of the edited monitor", func() bool { return len(sink.snapshot()) >= 2 })

	// After the scheduler stops, no runner is left to receive the push.
	s.Stop()
	if err := s.Push(context.Background(), "token-two", monitor.Push{Up: true}); !errors.Is(err, monitor.ErrPushNotFound) {
		t.Errorf("after Stop: %v", err)
	}
}

func TestPushWithoutHeartbeatGoesDown(t *testing.T) {
	sink := &fakeSink{}
	s := New(checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		return monitor.Result{OK: true}
	}), sink, 10, quiet)
	defer s.Stop()

	// With no retries, the first missed window is DOWN at once. The window is the
	// interval plus the one second of grace, so this takes about two seconds.
	s.Upsert(monitor.Monitor{ID: 2, Active: true, Interval: 1, Type: monitor.TypePush, PushToken: "token", MaxRetries: 0})
	waitFor(t, "the missed heartbeat", func() bool { return len(sink.snapshot()) >= 1 })
	beats := sink.snapshot()
	if beats[0].Status != monitor.StatusDown || beats[0].Msg != monitor.NoHeartbeatMsg || !beats[0].Important {
		t.Fatalf("first beat: %+v", beats[0])
	}

	// A push brings it back up, and starts the window again.
	if err := s.Push(context.Background(), "token", monitor.Push{Up: true, Msg: "here"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the recovery", func() bool {
		beats := sink.snapshot()
		return len(beats) >= 2 && beats[len(beats)-1].Status == monitor.StatusUp
	})
	if last := sink.snapshot()[1]; last.Msg != "here" || !last.Important {
		t.Errorf("recovery beat: %+v", last)
	}

	// Without another push, the window expires again.
	waitFor(t, "the second missed heartbeat", func() bool {
		beats := sink.snapshot()
		return len(beats) >= 3 && beats[len(beats)-1].Status == monitor.StatusDown
	})
	if !sink.snapshot()[2].Important {
		t.Error("losing the sender is an event")
	}
}

func TestPushIsRateLimited(t *testing.T) {
	sink := &fakeSink{}
	s := New(checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		return monitor.Result{OK: true}
	}), sink, 10, quiet)
	defer s.Stop()
	s.Upsert(pushMonitor(3, "token", 60))

	// The burst of twenty is spent at once, and a loop can't fill the queue behind it.
	for range pushBurst {
		if err := s.Push(context.Background(), "token", monitor.Push{Up: true}); err != nil {
			t.Fatalf("within the burst: %v", err)
		}
	}
	if err := s.Push(context.Background(), "token", monitor.Push{Up: true}); !errors.Is(err, monitor.ErrPushTooFast) {
		t.Errorf("past the burst: %v", err)
	}
}

func TestPushUnderMaintenanceIsNotAnAlert(t *testing.T) {
	sink := &fakeSink{}
	window := &switchableMaintenance{}
	window.on.Store(true)
	notifier := &countingNotifier{}
	s := New(checkFunc(func(context.Context, monitor.Monitor) monitor.Result {
		return monitor.Result{OK: true}
	}), sink, 10, quiet)
	s.SetNotifier(notifier)
	s.SetMaintenance(window)
	defer s.Stop()

	s.Upsert(pushMonitor(4, "token", 60))
	if err := s.Push(context.Background(), "token", monitor.Push{Up: true}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the maintenance beat", func() bool { return len(sink.snapshot()) >= 1 })
	if beats := sink.snapshot(); beats[0].Status != monitor.StatusMaintenance || beats[0].Msg != monitor.MaintenanceMsg {
		t.Fatalf("first beat: %+v", beats[0])
	}
	if notifier.count.Load() != 0 {
		t.Fatalf("%d alerts under maintenance", notifier.count.Load())
	}

	window.on.Store(false)
	if err := s.Push(context.Background(), "token", monitor.Push{Up: true, Msg: "back"}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "an up beat after the window", func() bool {
		beats := sink.snapshot()
		return len(beats) >= 2 && beats[len(beats)-1].Status == monitor.StatusUp
	})
	if notifier.count.Load() != 0 {
		t.Errorf("a recovery alerted: %d", notifier.count.Load())
	}
}

// countingChecker counts checks by monitor.
type countingChecker struct {
	mu     sync.Mutex
	checks map[int32]int
}

// Run counts the check and passes it.
func (c *countingChecker) Run(_ context.Context, m monitor.Monitor) monitor.Result {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.checks == nil {
		c.checks = map[int32]int{}
	}
	c.checks[m.ID]++
	return monitor.Result{OK: true}
}

// count is how many checks a monitor has had.
func (c *countingChecker) count(id int32) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.checks[id]
}

func TestPausedGroupStopsTheMonitorsInIt(t *testing.T) {
	checker := &countingChecker{}
	s := New(checker, &fakeSink{}, 10, quiet)
	defer s.Stop()
	parent := func(id int32) *int32 { return &id }
	group := monitor.Monitor{ID: 1, Type: monitor.TypeGroup, Active: true, Interval: 3600}
	sub := monitor.Monitor{ID: 2, Type: monitor.TypeGroup, Active: true, Interval: 3600, ParentID: parent(1)}
	child := monitor.Monitor{ID: 3, Active: true, Interval: 1, ParentID: parent(2)}
	pausedChild := monitor.Monitor{ID: 4, Active: false, Interval: 1, ParentID: parent(1)}
	other := monitor.Monitor{ID: 5, Active: true, Interval: 1}

	// After a restart, the monitors of a paused group aren't checked.
	group.Active = false
	s.Start([]monitor.Monitor{child, sub, group, pausedChild, other}, nil)
	waitFor(t, "the monitor outside the group", func() bool { return checker.count(5) >= 2 })
	if n := checker.count(3); n != 0 {
		t.Fatalf("a monitor in a paused group was checked %d times after a restart", n)
	}

	// Resuming the group starts what's active in it, however deep, at once.
	group.Active = true
	s.Upsert(group)
	waitFor(t, "the monitor in the resumed group", func() bool { return checker.count(3) >= 1 })
	if n := checker.count(4); n != 0 {
		t.Errorf("a paused monitor started with its group: %d checks", n)
	}

	// Pausing the group stops them.
	group.Active = false
	s.Upsert(group)
	before := checker.count(3)
	time.Sleep(1300 * time.Millisecond)
	if n := checker.count(3); n != before {
		t.Errorf("a monitor in a paused group kept running: %d checks, then %d", before, n)
	}

	// Moving the monitor out of the paused group starts it.
	child.ParentID = nil
	s.Upsert(child)
	waitFor(t, "the monitor out of the group", func() bool { return checker.count(3) > before })

	// Back in, it stops; a paused group's data being cleared doesn't start it; and
	// deleting the paused group lets it run again, outside any group.
	child.ParentID = parent(2)
	s.Upsert(child)
	if err := s.Suspend(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	s.Upsert(group)
	before = checker.count(3)
	time.Sleep(1300 * time.Millisecond)
	if n := checker.count(3); n != before {
		t.Errorf("a monitor in a paused group ran after the group was suspended: %d checks, then %d", before, n)
	}
	if err := s.Remove(context.Background(), 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the monitor once its group is deleted", func() bool { return checker.count(3) > before })
	if n := checker.count(4); n != 0 {
		t.Errorf("a paused monitor started when its group was deleted: %d checks", n)
	}
}

func TestSuspendedMonitorStaysStoppedWhileItsGroupChanges(t *testing.T) {
	checker := &countingChecker{}
	s := New(checker, &fakeSink{}, 10, quiet)
	defer s.Stop()
	groupID := int32(1)
	group := monitor.Monitor{ID: groupID, Type: monitor.TypeGroup, Active: true, Interval: 3600}
	child := monitor.Monitor{ID: 2, Active: true, Interval: 1, ParentID: &groupID}
	s.Start([]monitor.Monitor{group, child}, nil)
	waitFor(t, "the child's first check", func() bool { return checker.count(2) >= 1 })

	// Suspended while its data is cleared: pausing and resuming its group doesn't start it.
	if err := s.Suspend(context.Background(), 2); err != nil {
		t.Fatal(err)
	}
	before := checker.count(2)
	running := func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		_, ok := s.runners[2]
		return ok
	}
	group.Active = false
	s.Upsert(group)
	if running() {
		t.Fatal("a suspended monitor has a runner after its group was paused")
	}
	group.Active = true
	s.Upsert(group)
	if running() {
		t.Fatal("a suspended monitor has a runner after its group was resumed")
	}
	time.Sleep(1300 * time.Millisecond)
	if n := checker.count(2); n != before {
		t.Fatalf("a suspended monitor started with its group: %d checks, then %d", before, n)
	}

	// Its own Upsert, once the data is cleared, starts it again.
	s.Upsert(child)
	waitFor(t, "the child after its Upsert", func() bool { return checker.count(2) > before })
}
