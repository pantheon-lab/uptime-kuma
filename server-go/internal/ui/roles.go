package ui

import (
	"context"
	"errors"
	"net/url"
)

// Roles. An admin can do everything. A viewer can look at monitors, beats, maintenance windows
// and status pages, but changes nothing, and is sent no credentials: monitors come without
// them, and the lists of notifications, proxies, Docker hosts and remote browsers, which are
// mostly credentials, come empty.

// errReadOnly answers a viewer's attempt to change something.
var errReadOnly = errors.New("Your account can only view. Ask an admin to make this change.") //nolint:staticcheck // Shown to users.

// viewerEvents are the realtime events a viewer may send: those that only read, and send no
// credentials. Anything else is refused, so that a new event is closed to viewers until it's
// added here.
var viewerEvents = map[string]bool{
	"dashboardRendered":                  true,
	"getMonitorList":                     true,
	"getMonitor":                         true,
	"getHeartbeatHistory":                true,
	"getMonitorBeats":                    true,
	"getMonitorChartData":                true,
	"getMonitorChartRange":               true,
	"getMonitorOutages":                  true,
	"monitorImportantHeartbeatListCount": true,
	"monitorImportantHeartbeatListPaged": true,
	"getTags":                            true,
	"getMaintenanceList":                 true,
	"getMonitorMaintenance":              true,
	"getHolidayCountries":                true,
	"getHolidays":                        true,
	"getUpcomingHolidays":                true,
	"getStatusPage":                      true,
	"getIncidentHistory":                 true,
	"checkApprise":                       true,
	// The viewer's own account, for the Security page: how it logs in, and its teams.
	"getOwnAccount": true,
}

// viewerMonitorFields are what a viewer is sent of a monitor: what the list and its page show,
// and nothing that can hold a credential. It's a list of what's allowed, so that a field added
// later stays hidden from viewers until it's added here.
var viewerMonitorFields = []string{
	"id", "name", "description", "type", "path", "pathName", "parent", "childrenIDs", "active", "forceInactive",
	"tags", "maintenance", "interval", "retryInterval", "maxretries", "upsideDown", "expiryNotification",
	"domainExpiryNotification", "hostname", "port", "method", "keyword", "invertKeyword", "dns_resolve_type",
	"dns_resolve_server", "dns_last_result", "docker_container", "game", "weight", "includeSensitiveData",
	"location", "protocol", "ipFamily",
}

// viewerMonitorJSON keeps the fields a viewer may see. The URL loses its credentials, query and
// fragment, where tokens often are.
func viewerMonitorJSON(full map[string]any) map[string]any {
	out := make(map[string]any, len(viewerMonitorFields)+1)
	for _, key := range viewerMonitorFields {
		if v, found := full[key]; found {
			out[key] = v
		}
	}
	if raw, ok := full["url"].(string); ok {
		out["url"] = safeURL(raw)
	}
	// The form's notification list would show which notifications exist.
	out["notificationIDList"] = map[string]bool{}
	return out
}

func safeURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" {
		return ""
	}
	u.User, u.RawQuery, u.ForceQuery, u.Fragment, u.RawFragment = nil, "", false, "", ""
	return u.String()
}

func viewerMonitorList(full map[string]any) map[string]any {
	out := make(map[string]any, len(full))
	for id, m := range full {
		if monitor, ok := m.(map[string]any); ok {
			out[id] = viewerMonitorJSON(monitor)
		}
	}
	return out
}

// broadcastAdmins sends a frame to logged-in admins only.
func (s *Server) broadcastAdmins(frame []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if c.loggedIn() && c.admin {
			c.push(frame)
		}
	}
}

// broadcastMonitorUpdate sends changed monitors, keyed by id: in full to admins, and without
// credentials to viewers.
func (s *Server) broadcastMonitorUpdate(update map[string]any) {
	adminFrame := encodeMessage("updateMonitorIntoList", update)
	viewerFrame := encodeMessage("updateMonitorIntoList", viewerMonitorList(update))
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		if !c.loggedIn() {
			continue
		}
		if c.admin {
			c.push(adminFrame)
		} else {
			c.push(viewerFrame)
		}
	}
}

// afterViewerLogin sends a viewer what the UI waits for at login, without credentials.
func (s *Server) afterViewerLogin(ctx context.Context, c *conn) {
	if tree, err := s.monitorTree(ctx); err != nil {
		s.log.Error("send monitor list", "error", err)
	} else {
		c.emit("monitorList", viewerMonitorList(tree.listJSON()))
	}
	c.emit("info", s.info(ctx, true))
	if list, err := s.maintenanceList(ctx); err != nil {
		s.log.Error("send maintenance list", "error", err)
	} else {
		c.emit("maintenanceList", list)
	}
	for _, event := range []string{"notificationList", "proxyList", "dockerHostList", "remoteBrowserList"} {
		c.emit(event, []any{})
	}
	c.emit("monitorTypeList", monitorTypeList())
	if s.pages == nil {
		c.emit("statusPageList", map[string]any{})
		return
	}
	if list, err := s.statusPageList(ctx); err != nil {
		s.log.Error("send status page list", "error", err)
	} else {
		c.emit("statusPageList", list)
	}
}
