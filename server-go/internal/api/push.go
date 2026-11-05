package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// maxPushPingMs is the Node.js backend's limit on a pushed ping, about 3 years.
const maxPushPingMs = 100_000_000_000

// push receives a heartbeat from a push monitor's sender. It answers as the Node.js backend
// does, which scripts may rely on: {"ok":true}, or a 404 with {"ok":false,"msg":...}.
func (s *Server) push(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	status := query.Get("status")
	push := monitor.Push{
		Up:           status == "" || status == "up",
		ExplicitDown: status == "down",
		Msg:          query.Get("msg"),
	}
	if push.Msg == "" {
		push.Msg = "OK"
	}
	// Anything that isn't a number means no ping, including the empty value in the UI's example URL.
	if ping, err := strconv.ParseFloat(query.Get("ping"), 64); err == nil && ping == ping {
		if ping < 0 || ping > maxPushPingMs {
			writePushResult(w, http.StatusNotFound, "Invalid ping value. Must be between 0 and "+strconv.Itoa(maxPushPingMs)+" ms.")
			return
		}
		push.Ping = &ping
	}

	switch err := s.scheduler.Push(r.Context(), r.PathValue("token"), push); {
	case err == nil:
		writePushResult(w, http.StatusOK, "")
	case errors.Is(err, monitor.ErrPushTooFast):
		writePushResult(w, http.StatusTooManyRequests, "Too many pushes")
	case errors.Is(err, monitor.ErrPushNotFound):
		writePushResult(w, http.StatusNotFound, "Monitor not found or not active.")
	default:
		// The client went away, or the service is stopping.
		writePushResult(w, http.StatusServiceUnavailable, "Push not received")
	}
}

func writePushResult(w http.ResponseWriter, status int, msg string) {
	body := map[string]any{"ok": msg == ""}
	if msg != "" {
		body["msg"] = msg
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
