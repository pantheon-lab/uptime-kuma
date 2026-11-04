// Package e2e runs the real API, scheduler, checker and database together.
package e2e

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/api"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/check"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/scheduler"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/telemetry"
)

const token = "0123456789abcdef0123456789abcdef"

type app struct {
	url    string
	stop   func()
	client *http.Client
}

func start(t *testing.T, databaseURL string) *app {
	t.Helper()
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	db, err := store.Open(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	monitors, err := db.ListMonitors(ctx)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := db.LatestStates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	hub := api.NewHub()
	writer := scheduler.NewWriter(db, hub.Publish, log)
	go writer.Run()
	checker := check.New(db)
	sched := scheduler.New(checker, writer, 100, log)
	sched.Start(monitors, previous)
	ts := httptest.NewServer(api.New(db, sched, hub, token, log).Handler())

	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		hub.Close()
		ts.Close()
		sched.Stop()
		if err := writer.Close(ctx); err != nil {
			t.Error(err)
		}
		checker.Close()
		db.Close()
	}
	t.Cleanup(stop)
	return &app{url: ts.URL, stop: stop, client: ts.Client()}
}

func newDatabase(t *testing.T) string {
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
	name := fmt.Sprintf("kuma_e2e_%d", time.Now().UnixNano())
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = conn.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)")
		_ = conn.Close(ctx)
	})
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

func (a *app) call(t *testing.T, method, path string, body any, want int, out any) {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func TestMonitorLifecycle(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer target.Close()

	databaseURL := newDatabase(t)
	a := start(t, databaseURL)

	var m monitor.Monitor
	a.call(t, "POST", "/api/v1/monitors", map[string]any{
		"name": "site", "type": "http", "url": target.URL, "interval": 1, "maxRetries": 1, "retryInterval": 1,
	}, http.StatusCreated, &m)
	if m.ID == 0 || m.Timeout != 0 || m.MaxRedirects != 10 {
		t.Fatalf("created: %+v", m)
	}
	path := fmt.Sprintf("/api/v1/monitors/%d", m.ID)

	var got monitor.Monitor
	a.call(t, "GET", path, nil, http.StatusOK, &got)
	if got.URL != target.URL || got.Headers == nil || len(got.AcceptedStatusCodes) != 1 {
		t.Errorf("read back: %+v", got)
	}
	var list []monitor.Monitor
	a.call(t, "GET", "/api/v1/monitors", nil, http.StatusOK, &list)
	if len(list) != 1 {
		t.Errorf("list: %+v", list)
	}

	beats := func() []store.Beat {
		var out []store.Beat
		a.call(t, "GET", path+"/beats?limit=100", nil, http.StatusOK, &out)
		return out
	}
	waitFor(t, "first UP beat", func() bool { return len(beats()) > 0 })
	first := beats()
	first0 := first[len(first)-1]
	if first0.Status != monitor.StatusUp || !first0.Important || first0.Ping == nil || first0.Msg != "200 - OK" {
		t.Errorf("first beat: %+v", first0)
	}

	req, _ := http.NewRequest("GET", a.url+"/api/v1/events?monitorId="+fmt.Sprint(m.ID), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	stream, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Body.Close() }()
	streamed := make(chan store.Beat, 100)
	go func() {
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
				var b store.Beat
				if json.Unmarshal([]byte(data), &b) == nil {
					streamed <- b
				}
			}
		}
	}()
	select {
	case b := <-streamed:
		if b.MonitorID != m.ID {
			t.Errorf("streamed beat: %+v", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no beat arrived over SSE")
	}

	healthy.Store(false)
	waitFor(t, "DOWN beat", func() bool {
		b := beats()
		return b[0].Status == monitor.StatusDown
	})
	healthy.Store(true)
	waitFor(t, "recovery", func() bool { return beats()[0].Status == monitor.StatusUp })

	// Newest first: UP (event), DOWN (event), PENDING, UP...
	var sequence []int16
	for _, b := range beats() {
		if len(sequence) == 0 || sequence[len(sequence)-1] != b.Status {
			sequence = append(sequence, b.Status)
		}
	}
	if len(sequence) < 4 || sequence[0] != monitor.StatusUp || sequence[1] != monitor.StatusDown ||
		sequence[2] != monitor.StatusPending || sequence[3] != monitor.StatusUp {
		t.Fatalf("status sequence: %v", sequence)
	}
	all := beats()
	for i, b := range all {
		if b.Status == monitor.StatusDown && i+1 < len(all) && all[i+1].Status == monitor.StatusPending && !b.Important {
			t.Error("PENDING to DOWN must be an event")
		}
		if b.Status == monitor.StatusPending && b.Important {
			t.Error("PENDING must not be an event")
		}
	}
	if b := firstWith(all, monitor.StatusDown); b == nil || b.Msg != "Request failed with status code 503" || b.Ping != nil {
		t.Errorf("down beat: %+v", b)
	}

	var stats []store.MonitorStats
	a.call(t, "GET", "/api/v1/stats", nil, http.StatusOK, &stats)
	if len(stats) != 1 || stats[0].Uptime24h == nil || *stats[0].Uptime24h >= 1 || *stats[0].Uptime24h <= 0 {
		t.Errorf("stats: %+v", stats)
	}

	// A restart resumes from stored state instead of treating the next beat as the first.
	a.stop()
	b := start(t, databaseURL)
	path2 := path
	count := len(all)
	waitFor(t, "beats after restart", func() bool {
		var out []store.Beat
		b.call(t, "GET", path2+"/beats?limit=1000", nil, http.StatusOK, &out)
		return len(out) > count+1
	})
	var after []store.Beat
	b.call(t, "GET", path2+"/beats?limit=1000", nil, http.StatusOK, &after)
	if after[0].Important || after[1].Important {
		t.Errorf("beats after restart should not be events: %+v %+v", after[0], after[1])
	}

	// Update applies to the running monitor.
	b.call(t, "PUT", path2, map[string]any{
		"name": "renamed", "type": "keyword", "url": target.URL, "interval": 1, "keyword": "absent",
	}, http.StatusOK, &got)
	if got.Name != "renamed" || got.Keyword != "absent" || got.MaxRetries != 0 {
		t.Errorf("updated: %+v", got)
	}
	waitFor(t, "failure from the new config", func() bool {
		var out []store.Beat
		b.call(t, "GET", path2+"/beats?limit=1", nil, http.StatusOK, &out)
		return out[0].Status == monitor.StatusDown && strings.Contains(out[0].Msg, "but keyword is not")
	})

	// Delete removes beats and history, including materialized aggregates.
	b.call(t, "DELETE", path2, nil, http.StatusNoContent, nil)
	b.call(t, "GET", path2, nil, http.StatusNotFound, nil)
	b.call(t, "DELETE", path2, nil, http.StatusNotFound, nil)
	var empty []store.Beat
	b.call(t, "GET", path2+"/beats", nil, http.StatusOK, &empty)
	if len(empty) != 0 {
		t.Errorf("beats survived delete: %d", len(empty))
	}
	time.Sleep(1500 * time.Millisecond)
	b.call(t, "GET", path2+"/beats", nil, http.StatusOK, &empty)
	if len(empty) != 0 {
		t.Errorf("deleted monitor kept beating: %d", len(empty))
	}
	b.call(t, "GET", "/api/v1/stats", nil, http.StatusOK, &stats)
	if len(stats) != 0 {
		t.Errorf("stats still report the deleted monitor: %+v", stats)
	}
}

func TestDeleteRemovesMaterializedAndCompressedHistory(t *testing.T) {
	databaseURL := newDatabase(t)
	ctx := context.Background()
	a := start(t, databaseURL)
	conn, err := pgx.Connect(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()

	create := func(name string) monitor.Monitor {
		var m monitor.Monitor
		a.call(t, "POST", "/api/v1/monitors", map[string]any{
			"name": name, "type": "port", "hostname": "127.0.0.1", "port": 1, "active": false,
		}, http.StatusCreated, &m)
		return m
	}
	gone, kept := create("gone"), create("kept")

	var old []store.Beat
	base := time.Now().UTC().Add(-72 * time.Hour).Truncate(time.Hour)
	for i := 0; i < 48; i++ {
		at := base.Add(time.Duration(i) * time.Hour)
		old = append(old, store.Beat{MonitorID: gone.ID, Time: at, Status: 1}, store.Beat{MonitorID: kept.ID, Time: at, Status: 1})
	}
	a.call(t, "POST", "/api/v1/beats", old, http.StatusOK, nil)

	for _, statement := range []string{
		`CALL refresh_continuous_aggregate('stat_hourly', NULL, NULL)`,
		`CALL refresh_continuous_aggregate('stat_daily', NULL, NULL)`,
		`SELECT compress_chunk(c) FROM show_chunks('heartbeat', older_than => INTERVAL '1 day') c`,
	} {
		if _, err := conn.Exec(ctx, statement); err != nil {
			t.Fatal(err)
		}
	}

	var stats []store.MonitorStats
	a.call(t, "GET", "/api/v1/stats", nil, http.StatusOK, &stats)
	if len(stats) != 2 {
		t.Fatalf("before delete: %+v", stats)
	}
	a.call(t, "DELETE", fmt.Sprintf("/api/v1/monitors/%d", gone.ID), nil, http.StatusNoContent, nil)

	a.call(t, "GET", "/api/v1/stats", nil, http.StatusOK, &stats)
	if len(stats) != 1 || stats[0].MonitorID != kept.ID || stats[0].Uptime30d == nil {
		t.Errorf("after delete: %+v", stats)
	}
	var rows int
	if err := conn.QueryRow(ctx, `SELECT count(*) FROM heartbeat WHERE monitor_id = $1`, gone.ID).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 0 {
		t.Errorf("%d raw beats survived in compressed chunks", rows)
	}
	var chart []store.ChartPoint
	a.call(t, "GET", fmt.Sprintf("/api/v1/monitors/%d/chart?hours=168", gone.ID), nil, http.StatusOK, &chart)
	if len(chart) != 0 {
		t.Errorf("chart of deleted monitor: %+v", chart)
	}
	a.call(t, "GET", fmt.Sprintf("/api/v1/monitors/%d/chart?hours=168", kept.ID), nil, http.StatusOK, &chart)
	if len(chart) == 0 {
		t.Error("deleting one monitor removed another monitor's chart")
	}
}

func firstWith(beats []store.Beat, status int16) *store.Beat {
	for i := range beats {
		if beats[i].Status == status {
			return &beats[i]
		}
	}
	return nil
}

func TestFailedBeatLinksToAnExportedTrace(t *testing.T) {
	exporter := tracetest.NewInMemoryExporter()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	// Ratio 0: only failures may be exported.
	tp := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(
		telemetry.NewOutcomeProcessor(sdktrace.NewSimpleSpanProcessor(exporter), 0, log)))
	previous := otel.GetTracerProvider()
	otel.SetTracerProvider(tp)
	t.Cleanup(func() {
		otel.SetTracerProvider(previous)
		_ = tp.Shutdown(context.Background())
	})

	var healthy atomic.Bool
	healthy.Store(true)
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	defer target.Close()

	a := start(t, newDatabase(t))
	var grp, m monitor.Monitor
	a.call(t, "POST", "/api/v1/monitors", map[string]any{"name": "Team", "type": "group", "interval": 1}, http.StatusCreated, &grp)
	a.call(t, "POST", "/api/v1/monitors", map[string]any{
		"name": "site", "type": "http", "url": target.URL, "interval": 1, "parentId": grp.ID,
	}, http.StatusCreated, &m)
	path := fmt.Sprintf("/api/v1/monitors/%d/beats?limit=100", m.ID)

	beats := func() []store.Beat {
		var out []store.Beat
		a.call(t, "GET", path, nil, http.StatusOK, &out)
		return out
	}
	waitFor(t, "an UP beat", func() bool { return len(beats()) > 0 })
	if got := beats()[0]; got.Status != monitor.StatusUp || got.TraceID != "" {
		t.Errorf("an UP beat should have no trace id: %+v", got)
	}
	if spans := exporter.GetSpans(); len(spans) != 0 {
		// The group's beat can be pending or up, and pending counts as a failure.
		for _, span := range spans {
			for _, attr := range span.Attributes {
				if attr.Key == "monitor.name" && attr.Value.AsString() == "site" {
					t.Errorf("a successful check was exported at ratio 0: %+v", span.Name)
				}
			}
		}
	}

	healthy.Store(false)
	var failed store.Beat
	waitFor(t, "a DOWN beat", func() bool {
		failed = beats()[0]
		return failed.Status == monitor.StatusDown
	})
	if len(failed.TraceID) != 32 {
		t.Fatalf("failed beat has trace id %q", failed.TraceID)
	}

	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatal(err)
	}
	var root *tracetest.SpanStub
	names := map[string]bool{}
	spans := exporter.GetSpans()
	for i := range spans {
		if spans[i].SpanContext.TraceID().String() == failed.TraceID {
			names[spans[i].Name] = true
			if spans[i].Name == "monitor.check" {
				root = &spans[i]
			}
		}
	}
	if root == nil {
		t.Fatalf("trace %s wasn't exported, got %v", failed.TraceID, names)
	}
	if root.Status.Code != codes.Error || root.Status.Description != "Request failed with status code 502" {
		t.Errorf("root status: %+v", root.Status)
	}
	if len(names) < 2 {
		t.Errorf("expected child spans, got %v", names)
	}
	attrs := map[string]string{}
	for _, attr := range root.Attributes {
		attrs[string(attr.Key)] = attr.Value.String()
	}
	if attrs["monitor.name"] != "site" || attrs["monitor.group"] != "Team" || attrs["http.response.status_code"] != "502" {
		t.Errorf("attributes: %+v", attrs)
	}

	// The group goes down because of its child, with its own trace.
	var groupBeats []store.Beat
	waitFor(t, "the group to go down", func() bool {
		a.call(t, "GET", fmt.Sprintf("/api/v1/monitors/%d/beats?limit=1", grp.ID), nil, http.StatusOK, &groupBeats)
		return len(groupBeats) > 0 && groupBeats[0].Status == monitor.StatusDown
	})
	if groupBeats[0].Msg != "Child monitors down: site" || groupBeats[0].TraceID == "" {
		t.Errorf("group beat: %+v", groupBeats[0])
	}

	a.call(t, "POST", "/api/v1/monitors", map[string]any{
		"name": "x", "type": "port", "hostname": "a", "port": 1, "parentId": m.ID,
	}, http.StatusBadRequest, nil)
}

func (a *app) callAs(t *testing.T, credential, method, path string, body any, want int, out any) *http.Response {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequest(method, a.url+path, reader)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+credential)
	res, err := a.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	data, _ := io.ReadAll(res.Body)
	if res.StatusCode != want {
		t.Fatalf("%s %s: status %d, want %d: %s", method, path, res.StatusCode, want, data)
	}
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			t.Fatalf("%s %s: %v: %s", method, path, err, data)
		}
	}
	return res
}

func TestExternalAPIWithKeys(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer target.Close()
	databaseURL := newDatabase(t)
	a := start(t, databaseURL)

	type created struct {
		ID     int64    `json:"id"`
		Key    string   `json:"key"`
		Prefix string   `json:"prefix"`
		Scopes []string `json:"scopes"`
	}
	newKey := func(scopes ...string) created {
		var k created
		a.callAs(t, token, "POST", "/api/v1/api-keys", map[string]any{"name": "ci", "scopes": scopes}, http.StatusCreated, &k)
		return k
	}
	writer := newKey("monitors:read", "monitors:write", "beats:read")
	reader := newKey("monitors:read")

	// The plain key is only returned once and never stored.
	if !strings.HasPrefix(writer.Key, "kuma_") || len(writer.Key) != 69 || !strings.HasPrefix(writer.Key, writer.Prefix) {
		t.Fatalf("key: %+v", writer)
	}
	listed := []map[string]any{}
	a.callAs(t, token, "GET", "/api/v1/api-keys", nil, http.StatusOK, &listed)
	dump, _ := json.Marshal(listed)
	if len(listed) != 2 || strings.Contains(string(dump), writer.Key) || strings.Contains(string(dump), "hash") {
		t.Errorf("listing exposes too much: %s", dump)
	}
	conn, err := pgx.Connect(context.Background(), databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	var stored int
	if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM api_key WHERE encode(key_hash, 'escape') LIKE '%' || $1 || '%' OR key_prefix = $1`, writer.Key).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 0 {
		t.Error("the plain key is in the database")
	}

	// Keys can't manage keys.
	a.callAs(t, writer.Key, "GET", "/api/v1/api-keys", nil, http.StatusForbidden, nil)
	a.callAs(t, writer.Key, "POST", "/api/v1/api-keys", map[string]any{"name": "x", "scopes": []string{"beats:write"}}, http.StatusForbidden, nil)

	// Create with a key, including a secret in several places.
	var m monitor.Monitor
	res := a.callAs(t, writer.Key, "POST", "/api/v1/monitors", map[string]any{
		"name": "site", "type": "http", "url": target.URL + "/?token=querysecret", "interval": 1,
		"headers": map[string]string{"X-Api-Key": "headersecret", "X-Other": "keep"},
	}, http.StatusCreated, &m)
	path := fmt.Sprintf("/api/v1/monitors/%d", m.ID)
	if res.Header.Get("Location") != path {
		t.Errorf("Location: %q", res.Header.Get("Location"))
	}

	// A read-only key doesn't get credentials, a writer does.
	var seen monitor.Monitor
	a.callAs(t, reader.Key, "GET", path, nil, http.StatusOK, &seen)
	if strings.Contains(seen.URL, "querysecret") || seen.Headers["X-Api-Key"] != "***" {
		t.Errorf("read-only key sees secrets: %+v", seen)
	}
	var page []monitor.Monitor
	a.callAs(t, reader.Key, "GET", "/api/v1/monitors", nil, http.StatusOK, &page)
	if len(page) != 1 || page[0].Headers["X-Api-Key"] != "***" {
		t.Errorf("read-only list sees secrets: %+v", page)
	}
	a.callAs(t, writer.Key, "GET", path, nil, http.StatusOK, &seen)
	if seen.Headers["X-Api-Key"] != "headersecret" {
		t.Errorf("writer: %+v", seen)
	}
	a.callAs(t, reader.Key, "POST", "/api/v1/monitors", map[string]any{"name": "x", "type": "group"}, http.StatusForbidden, nil)
	a.callAs(t, reader.Key, "GET", path+"/beats", nil, http.StatusForbidden, nil)

	// PATCH changes only what's sent.
	var patched monitor.Monitor
	a.callAs(t, writer.Key, "PATCH", path, map[string]any{"name": "renamed", "headers": map[string]string{"X-New": "1"}}, http.StatusOK, &patched)
	if patched.Name != "renamed" || patched.Interval != 1 || patched.URL != m.URL || len(patched.Headers) != 1 || patched.Headers["X-New"] != "1" {
		t.Errorf("patched: %+v", patched)
	}
	a.callAs(t, writer.Key, "PATCH", path, map[string]any{"intervall": 5}, http.StatusBadRequest, nil)
	a.callAs(t, writer.Key, "PATCH", path, map[string]any{"interval": 0}, http.StatusBadRequest, nil)
	a.callAs(t, writer.Key, "PATCH", path, map[string]any{"id": 999, "maxRetries": 2}, http.StatusOK, &patched)
	if patched.ID != m.ID || patched.MaxRetries != 2 {
		t.Errorf("body id must be ignored: %+v", patched)
	}
	a.callAs(t, writer.Key, "PATCH", "/api/v1/monitors/99999", map[string]any{"name": "x"}, http.StatusNotFound, nil)

	beatCount := func() int {
		var beats []store.Beat
		a.callAs(t, writer.Key, "GET", path+"/beats?limit=1000", nil, http.StatusOK, &beats)
		return len(beats)
	}
	waitFor(t, "beats", func() bool { return beatCount() > 0 })

	// Pause really stops checks, resume starts them.
	a.callAs(t, writer.Key, "POST", path+"/pause", nil, http.StatusOK, &patched)
	if patched.Active {
		t.Error("still active after pause")
	}
	time.Sleep(1200 * time.Millisecond)
	paused := beatCount()
	time.Sleep(1500 * time.Millisecond)
	if got := beatCount(); got != paused {
		t.Errorf("paused monitor kept beating: %d to %d", paused, got)
	}
	a.callAs(t, writer.Key, "POST", path+"/resume", nil, http.StatusOK, &patched)
	waitFor(t, "beats after resume", func() bool { return beatCount() > paused })
	a.callAs(t, writer.Key, "POST", "/api/v1/monitors/99999/pause", nil, http.StatusNotFound, nil)

	// Pagination and filters.
	for i := 0; i < 5; i++ {
		a.callAs(t, writer.Key, "POST", "/api/v1/monitors", map[string]any{"name": fmt.Sprintf("g%d", i), "type": "group", "active": false}, http.StatusCreated, nil)
	}
	res = a.callAs(t, writer.Key, "GET", "/api/v1/monitors?type=group&limit=2&offset=1", nil, http.StatusOK, &page)
	if len(page) != 2 || page[0].Name != "g1" || res.Header.Get("X-Total-Count") != "5" {
		t.Errorf("page: %+v, total %q", page, res.Header.Get("X-Total-Count"))
	}
	a.callAs(t, writer.Key, "GET", "/api/v1/monitors?active=false", nil, http.StatusOK, &page)
	if len(page) != 5 {
		t.Errorf("active filter: %d", len(page))
	}
	a.callAs(t, writer.Key, "GET", "/api/v1/monitors?limit=1001", nil, http.StatusBadRequest, nil)
	a.callAs(t, writer.Key, "GET", "/api/v1/monitors?offset=-1", nil, http.StatusBadRequest, nil)
	a.callAs(t, writer.Key, "GET", "/api/v1/monitors?active=maybe", nil, http.StatusBadRequest, nil)

	// Delete with a key.
	a.callAs(t, writer.Key, "DELETE", path, nil, http.StatusNoContent, nil)
	a.callAs(t, writer.Key, "GET", path, nil, http.StatusNotFound, nil)

	// last_used_at gets recorded.
	waitFor(t, "last used", func() bool {
		var keys []struct {
			ID         int64      `json:"id"`
			LastUsedAt *time.Time `json:"lastUsedAt"`
		}
		a.callAs(t, token, "GET", "/api/v1/api-keys", nil, http.StatusOK, &keys)
		return keys[0].LastUsedAt != nil
	})

	// Revoking is immediate, including with the same server and no restart.
	a.callAs(t, token, "DELETE", fmt.Sprintf("/api/v1/api-keys/%d", writer.ID), nil, http.StatusNoContent, nil)
	a.callAs(t, writer.Key, "GET", "/api/v1/monitors", nil, http.StatusUnauthorized, nil)
	a.callAs(t, token, "DELETE", fmt.Sprintf("/api/v1/api-keys/%d", writer.ID), nil, http.StatusNotFound, nil)
	a.callAs(t, reader.Key, "GET", "/api/v1/monitors", nil, http.StatusOK, nil)

	// An expired key stops working.
	var soon created
	a.callAs(t, token, "POST", "/api/v1/api-keys", map[string]any{
		"name": "short", "scopes": []string{"monitors:read"}, "expiresAt": time.Now().Add(1500 * time.Millisecond),
	}, http.StatusCreated, &soon)
	a.callAs(t, soon.Key, "GET", "/api/v1/monitors", nil, http.StatusOK, nil)
	time.Sleep(1700 * time.Millisecond)
	a.callAs(t, soon.Key, "GET", "/api/v1/monitors", nil, http.StatusUnauthorized, nil)
}
