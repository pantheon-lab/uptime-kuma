package monitor

import (
	"crypto/tls"
	"errors"
	"strings"
)

const maxMsgBytes = 4096

// Result is the outcome of running a check.
type Result struct {
	OK   bool
	Ping *float64
	Msg  string
	// Pending reports PENDING directly, without using up a retry. Groups use it when they
	// are empty or a child is pending.
	Pending bool
	// NoRetry makes a failure DOWN at once, because the target itself reported it.
	NoRetry bool
	// TLS is the connection's state, if the check made an HTTPS request.
	TLS *tls.ConnectionState
}

// Previous is what the next beat needs from the last one. A nil *Previous means
// that this monitor has never had a beat.
type Previous struct {
	Status    int16
	Retries   int32
	DownCount int32
}

// Outcome is the beat derived from a check result.
type Outcome struct {
	Status    int16
	Retries   int32
	Important bool
	Ping      *float64
	Msg       string
	// Notify says whether this beat should send an alert.
	Notify bool
	// DownCount counts DOWN beats since the last alert, for ResendInterval.
	DownCount int32
}

// Next applies the retry, upside-down and important-beat rules to a check result.
func (m *Monitor) Next(prev *Previous, result Result) Outcome {
	var retries int32
	if prev != nil {
		retries = prev.Retries
	}

	out := Outcome{Ping: result.Ping, Msg: SanitizeMsg(result.Msg)}
	if result.Pending {
		out.Status = StatusPending
		out.Important = prev == nil || IsImportant(prev.Status, out.Status)
		m.applyNotify(prev, &out)
		return out
	}
	failed := !result.OK

	if m.UpsideDown {
		// A failing check means UP. A succeeding check is treated as a failure, so
		// it goes through the same retries before the monitor counts as DOWN.
		out.Status = StatusUp
		if result.OK {
			out.Status = StatusDown
			out.Msg = "Flip UP to DOWN"
			failed = true
		}
	} else if result.OK {
		out.Status = StatusUp
	} else {
		out.Status = StatusDown
	}

	switch {
	case !failed:
		retries = 0
	case result.NoRetry && !m.UpsideDown:
		// The target itself reported this, so there is nothing to retry. (An
		// upside-down monitor reports the opposite, and never reaches this.)
		retries = 0
	case m.UpsideDown && out.Status == StatusUp:
		retries = 0
	case m.MaxRetries > 0 && retries < m.MaxRetries && !result.NoRetry:
		retries++
		out.Status = StatusPending
	default:
		// Keeps counting while DOWN.
		retries++
	}
	out.Retries = retries

	if prev == nil {
		out.Important = true
	} else {
		out.Important = IsImportant(prev.Status, out.Status)
	}
	m.applyNotify(prev, &out)
	return out
}

// Push is what a push monitor's sender reports.
type Push struct {
	Up bool
	// ExplicitDown means that the sender said status=down, rather than another status.
	ExplicitDown bool
	Msg          string
	Ping         *float64
}

var (
	// ErrPushNotFound means that no active monitor has the token.
	ErrPushNotFound = errors.New("no active monitor has this push token")
	// ErrPushTooFast means that a monitor is getting more pushes than it can keep.
	ErrPushTooFast = errors.New("too many pushes")
)

// NoHeartbeatMsg is the message of a beat made when a push is late, as in the Node.js backend.
const NoHeartbeatMsg = "No heartbeat in the time window"

// NextPush is Next for a received push. The beat keeps the sender's message, even upside down.
func (m *Monitor) NextPush(prev *Previous, push Push) Outcome {
	out := m.Next(prev, Result{OK: push.Up, Ping: push.Ping, Msg: push.Msg, NoRetry: push.ExplicitDown})
	out.Msg = SanitizeMsg(push.Msg)
	return out
}

// MaintenanceMsg is the message of a beat made under maintenance, as in the Node.js backend.
const MaintenanceMsg = "Monitor under maintenance"

// Maintenance is the beat for a monitor under maintenance, which isn't checked. Entering or
// leaving maintenance is an event, but never an alert, and retries start again afterwards.
func (m *Monitor) Maintenance(prev *Previous) Outcome {
	out := Outcome{Status: StatusMaintenance, Msg: MaintenanceMsg}
	out.Important = prev == nil || IsImportant(prev.Status, out.Status)
	return out
}

// applyNotify decides on an alert. A monitor's very first beat only alerts when it's DOWN,
// so that adding a healthy monitor doesn't send anything.
func (m *Monitor) applyNotify(prev *Previous, out *Outcome) {
	if out.Important {
		if prev == nil {
			out.Notify = out.Status == StatusDown
		} else {
			out.Notify = ImportantForNotification(prev.Status, out.Status)
		}
		return
	}
	if prev != nil {
		out.DownCount = prev.DownCount
	}
	if out.Status == StatusDown && m.ResendInterval > 0 {
		out.DownCount++
		if out.DownCount >= m.ResendInterval {
			out.Notify = true
			out.DownCount = 0
		}
	}
}

// ImportantForNotification reports whether a status change should alert. Going into or out
// of maintenance to UP shouldn't.
func ImportantForNotification(previous, current int16) bool {
	switch {
	case previous == StatusMaintenance && current == StatusDown,
		previous == StatusUp && current == StatusDown,
		previous == StatusDown && current == StatusUp,
		previous == StatusPending && current == StatusDown:
		return true
	}
	return false
}

// IsImportant reports whether a status change between two beats is an event.
func IsImportant(previous, current int16) bool {
	switch {
	case previous == StatusDown && current == StatusMaintenance,
		previous == StatusUp && current == StatusMaintenance,
		previous == StatusMaintenance && current == StatusDown,
		previous == StatusMaintenance && current == StatusUp,
		previous == StatusUp && current == StatusDown,
		previous == StatusDown && current == StatusUp,
		previous == StatusPending && current == StatusDown:
		return true
	}
	return false
}

// SanitizeMsg makes remote text safe to store. PostgreSQL rejects NUL and invalid
// UTF-8, and one bad message must not fail a whole batch of beats.
func SanitizeMsg(msg string) string {
	if len(msg) > maxMsgBytes {
		msg = msg[:maxMsgBytes]
	}
	return strings.ReplaceAll(strings.ToValidUTF8(msg, "�"), "\x00", "")
}
