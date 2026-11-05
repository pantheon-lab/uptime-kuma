package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/notify"
	"github.com/pantheon-lab/uptime-kuma/server-go/internal/store"
)

const (
	maxNotificationBody = 1 << 20
	testTimeout         = 30 * time.Second
	hiddenValue         = "***"
)

type notificationInput struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	Config     map[string]any `json:"config"`
	Active     *bool          `json:"active"`
	IsDefault  bool           `json:"isDefault"`
	ApplyToAll bool           `json:"applyToAllMonitors"`
}

func notificationID(w http.ResponseWriter, r *http.Request) (int32, bool) {
	v, err := strconv.ParseInt(r.PathValue("id"), 10, 32)
	if err != nil || v <= 0 {
		writeError(w, http.StatusBadRequest, "notification id must be a positive integer")
		return 0, false
	}
	return int32(v), true
}

// hideConfig keeps the fields' names but not their values, which can be credentials.
func hideConfig(n store.Notification) store.Notification {
	hidden := make(map[string]any, len(n.Config))
	for field := range n.Config {
		hidden[field] = hiddenValue
	}
	n.Config = hidden
	return n
}

func (s *Server) listNotificationProviders(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, notify.Providers())
}

func (s *Server) listNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := s.store.ListNotifications(r.Context())
	if err != nil {
		s.serverError(w, "list notifications", err)
		return
	}
	if !principalFrom(r.Context()).has(ScopeNotificationsWrite) {
		for i := range list {
			list[i] = hideConfig(list[i])
		}
	}
	writeJSON(w, http.StatusOK, list)
}

func (s *Server) getNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	n, err := s.store.GetNotification(r.Context(), id)
	if err != nil {
		s.notificationError(w, "get notification", err)
		return
	}
	if !principalFrom(r.Context()).has(ScopeNotificationsWrite) {
		n = hideConfig(n)
	}
	writeJSON(w, http.StatusOK, n)
}

func decodeNotification(w http.ResponseWriter, r *http.Request) (store.Notification, bool, bool) {
	var input notificationInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid notification: "+err.Error())
		return store.Notification{}, false, false
	}
	input.Name = strings.TrimSpace(input.Name)
	switch {
	case input.Name == "" || len(input.Name) > 255:
		writeError(w, http.StatusBadRequest, "name must be 1 to 255 characters")
		return store.Notification{}, false, false
	case !notify.Supported(input.Type):
		writeError(w, http.StatusBadRequest, "unknown type, see GET /api/v1/notifications/providers")
		return store.Notification{}, false, false
	case input.Config == nil:
		writeError(w, http.StatusBadRequest, "config is required")
		return store.Notification{}, false, false
	}
	active := true
	if input.Active != nil {
		active = *input.Active
	}
	return store.Notification{Name: input.Name, Type: input.Type, Config: input.Config, Active: active, IsDefault: input.IsDefault}, input.ApplyToAll, true
}

func (s *Server) createNotification(w http.ResponseWriter, r *http.Request) {
	n, applyToAll, ok := decodeNotification(w, r)
	if !ok {
		return
	}
	created, err := s.store.CreateNotification(r.Context(), n, applyToAll)
	if err != nil {
		s.notificationError(w, "create notification", err)
		return
	}
	s.log.Info("notification created", "notification", created.ID, "type", created.Type, "by", principalFrom(r.Context()).String())
	w.Header().Set("Location", "/api/v1/notifications/"+strconv.Itoa(int(created.ID)))
	writeJSON(w, http.StatusCreated, created)
}

func (s *Server) updateNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	n, applyToAll, ok := decodeNotification(w, r)
	if !ok {
		return
	}
	n.ID = id
	updated, err := s.store.UpdateNotification(r.Context(), n, applyToAll)
	if err != nil {
		s.notificationError(w, "update notification", err)
		return
	}
	s.log.Info("notification updated", "notification", updated.ID, "by", principalFrom(r.Context()).String())
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) deleteNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	if err := s.store.DeleteNotification(r.Context(), id); err != nil {
		s.notificationError(w, "delete notification", err)
		return
	}
	s.log.Info("notification deleted", "notification", id, "by", principalFrom(r.Context()).String())
	w.WriteHeader(http.StatusNoContent)
}

// testNotification sends a real message and reports the provider's answer.
func (s *Server) testNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := notificationID(w, r)
	if !ok {
		return
	}
	n, err := s.store.GetNotification(r.Context(), id)
	if err != nil {
		s.notificationError(w, "test notification", err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), testTimeout)
	defer cancel()
	if err := notify.SendTest(ctx, s.notifyEnv, n.Type, n.Name, n.Config); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) getMonitorNotifications(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	ids, err := s.store.MonitorNotificationIDs(r.Context(), id)
	if err != nil {
		s.monitorError(w, "get monitor notifications", err)
		return
	}
	if ids == nil {
		ids = []int32{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"notificationIds": ids})
}

func (s *Server) setMonitorNotifications(w http.ResponseWriter, r *http.Request) {
	id, ok := monitorID(w, r)
	if !ok {
		return
	}
	var input struct {
		NotificationIDs []int32 `json:"notificationIds"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxNotificationBody))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request: "+err.Error())
		return
	}
	if err := s.store.SetMonitorNotifications(r.Context(), id, input.NotificationIDs); err != nil {
		if errors.Is(err, store.ErrInvalidNotification) {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		s.monitorError(w, "set monitor notifications", err)
		return
	}
	s.audit(r, "monitor notifications changed", id)
	writeJSON(w, http.StatusOK, map[string]any{"notificationIds": input.NotificationIDs})
}

func (s *Server) notificationError(w http.ResponseWriter, operation string, err error) {
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "notification not found")
		return
	}
	s.serverError(w, operation, err)
}
