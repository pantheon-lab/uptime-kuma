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
