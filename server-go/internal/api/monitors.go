package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	maxMonitorBodyBytes = 2 << 20
	defaultMonitorPage  = 100
	maxMonitorPage      = 1000
)

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", defaultMonitorPage, maxMonitorPage)
	if !ok {
		return
	}
	filter := store.MonitorFilter{Type: r.URL.Query().Get("type"), Limit: limit}
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err := strconv.Atoi(raw)
		if err != nil || offset < 0 {
			writeError(w, http.StatusBadRequest, "offset must be a non-negative integer")
			return
		}
		filter.Offset = offset
	}
	if raw := r.URL.Query().Get("active"); raw != "" {
		active, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "active must be true or false")
			return
		}
		filter.Active = &active
	}
	if raw := r.URL.Query().Get("parentId"); raw != "" {
		parent, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || parent <= 0 {
			writeError(w, http.StatusBadRequest, "parentId must be a positive integer")
			return
		}
		id := int32(parent)
		filter.ParentID = &id
	}

	monitors, total, err := s.store.FindMonitors(r.Context(), filter)
	if err != nil {
		s.serverError(w, "list monitors", err)
		return
	}
	canSeeSecrets := principalFrom(r.Context()).has(ScopeMonitorsWrite)
	if !canSeeSecrets {
		for i := range monitors {
			monitors[i] = monitors[i].Redacted()
		}
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))
	writeJSON(w, http.StatusOK, monitors)
}

func (s *Server) getMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	m, err := s.store.GetMonitor(r.Context(), id)
	if err != nil {
		s.monitorError(w, "get monitor", err)
		return
	}
	if !principalFrom(r.Context()).has(ScopeMonitorsWrite) {
		m = m.Redacted()
	}
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) createMonitor(w http.ResponseWriter, r *http.Request) {
	m, ok := decodeMonitor(w, r)
	if !ok {
		return
	}
	created, err := s.store.CreateMonitor(r.Context(), m)
	if err != nil {
		s.monitorError(w, "create monitor", err)
		return
	}
	if err := s.store.LinkDefaultNotifications(r.Context(), created.ID); err != nil {
		s.log.Error("link default notifications", "monitor", created.ID, "error", err)
	}
	s.scheduler.Upsert(created)
	s.audit(r, "monitor created", created.ID)
	w.Header().Set("Location", "/api/v1/monitors/"+strconv.Itoa(int(created.ID)))
	writeJSON(w, http.StatusCreated, created)
}

// updateMonitor replaces the whole configuration. Fields left out return to their defaults.
func (s *Server) updateMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	m, ok := decodeMonitor(w, r)
	if !ok {
		return
	}
	m.ID = id
	updated, err := s.store.UpdateMonitor(r.Context(), m)
	if err != nil {
		s.monitorError(w, "update monitor", err)
		return
	}
	s.scheduler.Upsert(updated)
	s.audit(r, "monitor replaced", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

// patchMonitor changes only the fields in the request. Headers and status codes are
// replaced as a whole when present, and "parentId": null removes a monitor from its group.
func (s *Server) patchMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxMonitorBodyBytes))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid monitor: "+err.Error())
		return
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		writeError(w, http.StatusBadRequest, "invalid monitor: "+err.Error())
		return
	}
	m, err := s.store.GetMonitor(r.Context(), id)
	if err != nil {
		s.monitorError(w, "update monitor", err)
		return
	}
	// Decoding into an existing map would merge it.
	if _, present := fields["headers"]; present {
		m.Headers = nil
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		writeError(w, http.StatusBadRequest, "invalid monitor: "+err.Error())
		return
	}
	m.ID = id
	m.Normalize()
	if err := m.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	updated, err := s.store.UpdateMonitor(r.Context(), m)
	if err != nil {
		s.monitorError(w, "update monitor", err)
		return
	}
	s.scheduler.Upsert(updated)
	s.audit(r, "monitor updated", updated.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) pauseMonitor(w http.ResponseWriter, r *http.Request) {
	s.setMonitorActive(w, r, false)
}

func (s *Server) resumeMonitor(w http.ResponseWriter, r *http.Request) {
	s.setMonitorActive(w, r, true)
}

func (s *Server) setMonitorActive(w http.ResponseWriter, r *http.Request, active bool) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	m, err := s.store.SetMonitorActive(r.Context(), id, active)
	if err != nil {
		s.monitorError(w, "set monitor active", err)
		return
	}
	s.scheduler.Upsert(m)
	s.audit(r, "monitor active changed", m.ID, "active", active)
	writeJSON(w, http.StatusOK, m)
}

func (s *Server) deleteMonitor(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	if err := s.scheduler.Remove(r.Context(), id); err != nil {
		s.serverError(w, "delete monitor", err)
		return
	}
	if err := s.store.DeleteMonitor(r.Context(), id); err != nil {
		s.monitorError(w, "delete monitor", err)
		return
	}
	s.audit(r, "monitor deleted", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) monitorError(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "monitor not found")
		return
	}
	if errors.Is(err, store.ErrInvalidParent) || errors.Is(err, store.ErrPushTokenTaken) {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	s.serverError(w, operation, err)
}

func decodeMonitor(w http.ResponseWriter, r *http.Request) (monitor.Monitor, bool) {
	m := monitor.New()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxMonitorBodyBytes))
	// A misspelled option must fail, not silently become its default.
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&m); err != nil {
		writeError(w, http.StatusBadRequest, "invalid monitor: "+err.Error())
		return m, false
	}
	m.Normalize()
	if err := m.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return m, false
	}
	return m, true
}
