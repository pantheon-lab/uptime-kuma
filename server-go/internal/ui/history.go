package ui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

// The Monitor History page and the Clear Data menu of a monitor: the retention setting,
// clearing beats and statistics, and the database size.

// clearTimeout covers stopping the monitors, which writes out their queued beats, deleting
// their beats and rebuilding the aggregates.
const clearTimeout = 5 * time.Minute

// errShrinkNotNeeded answers the Shrink Database button, which the UI shows for SQLite only.
// PostgreSQL's autovacuum already does what a VACUUM would, and beats older than the
// retention are dropped a day at a time, which frees their space at once.
var errShrinkNotNeeded = errors.New("PostgreSQL reclaims space on its own, so there's nothing to shrink")

// retentionChange checks the retention setting being saved, and says whether it changes.
// It's saved as a number, however the UI sent it.
func (s *Server) retentionChange(ctx context.Context, toSave map[string]json.RawMessage) (int, bool, error) {
	raw, found := toSave[store.RetentionSetting]
	if !found {
		return 0, false, nil
	}
	days, err := store.ParseRetention(raw)
	if err != nil {
		return 0, false, err
	}
	toSave[store.RetentionSetting], _ = json.Marshal(days)
	settings, err := s.store.Settings(ctx)
	if err != nil {
		return 0, false, err
	}
	if current, err := store.ParseRetention(settings[store.RetentionSetting]); err == nil && current == days {
		return days, false, nil
	}
	return days, true, nil
}

// stopForClear stops monitors before their data is deleted, so that queued beats aren't
// written after it, and starts the active ones again when it's done, as Node.js restarted
// them. Their first beat then starts a new history, and counts as important.
func (s *Server) stopForClear(ctx context.Context, ids []int32) error {
	for _, id := range ids {
		if err := s.scheduler.Suspend(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) startAfterClear(ctx context.Context, ids []int32) {
	for _, id := range ids {
		m, err := s.store.GetMonitor(ctx, id)
		if err != nil {
			s.log.Error("restart monitor after clearing its data", "monitor", id, "error", err)
			continue
		}
		s.scheduler.Upsert(m)
	}
}

func (s *Server) clearHeartbeats(ctx context.Context, c *conn, args []json.RawMessage) (reply, error) {
	id, err := idArg(args, 0)
	if err != nil {
		return nil, userErr(err)
	}
	if _, err := s.store.GetMonitor(ctx, id); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clearTimeout)
	defer cancel()
	if err := s.stopForClear(ctx, []int32{id}); err != nil {
		return nil, err
	}
	clearErr := s.store.ClearMonitorStats(ctx, id)
	s.startAfterClear(ctx, []int32{id})
	if clearErr != nil {
		return nil, clearErr
	}
	s.log.Info("monitor's beats and statistics cleared", "monitor", id, "by", c.username)
	s.cache.clear()
	s.broadcast(encodeMessage("heartbeatList", id, []any{}, true))
	s.broadcastStats(ctx)
	return ok(), nil
}

func (s *Server) clearStatistics(ctx context.Context, c *conn) (reply, error) {
	monitors, err := s.store.ListMonitors(ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int32, 0, len(monitors))
	for _, m := range monitors {
		ids = append(ids, m.ID)
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clearTimeout)
	defer cancel()
	if err := s.stopForClear(ctx, ids); err != nil {
		s.startAfterClear(ctx, ids)
		return nil, err
	}
	clearErr := s.store.ClearAllStats(ctx)
	s.startAfterClear(ctx, ids)
	if clearErr != nil {
		return nil, clearErr
	}
	s.log.Info("all beats and statistics cleared", "monitors", len(ids), "by", c.username)
	s.cache.clear()
	s.broadcastStats(ctx)
	return ok(), nil
}

func (s *Server) databaseSize(ctx context.Context) (reply, error) {
	size, err := s.store.DatabaseSize(ctx)
	if err != nil {
		return nil, err
	}
	return ok("size", size), nil
}

// robots answers search engines as the Search Engine Visibility setting says. Indexing is
// discouraged until it's allowed, as in Node.js.
func (s *Server) robots(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), handlerTimeout)
	defer cancel()
	allow := false
	if settings, err := s.store.Settings(ctx); err != nil {
		s.log.Warn("read search engine visibility", "error", err)
	} else {
		_ = json.Unmarshal(settings["searchEngineIndex"], &allow)
	}
	body := "User-agent: *\nDisallow:"
	if !allow {
		body += " /"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	_, _ = w.Write([]byte(body))
}
