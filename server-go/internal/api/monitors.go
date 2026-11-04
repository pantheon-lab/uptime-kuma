package api

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const maxMonitorBodyBytes = 2 << 20

func (s *Server) listMonitors(w http.ResponseWriter, r *http.Request) {
	monitors, err := s.store.ListMonitors(r.Context())
	if err != nil {
		s.serverError(w, "list monitors", err)
		return
	}
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
	s.scheduler.Upsert(created)
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
	writeJSON(w, http.StatusOK, updated)
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
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) monitorError(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "monitor not found")
		return
	}
	if errors.Is(err, store.ErrInvalidParent) {
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
	if err := m.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return m, false
	}
	return m, true
}
