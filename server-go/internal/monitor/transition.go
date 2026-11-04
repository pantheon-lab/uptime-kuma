package monitor

import "strings"

const maxMsgBytes = 4096

// Result is the outcome of running a check.
type Result struct {
	OK   bool
	Ping *float64
	Msg  string
	// Pending reports PENDING directly, without using up a retry. Groups use it when they
	// are empty or a child is pending.
	Pending bool
}

// Previous is what the next beat needs from the last one. A nil *Previous means
// that this monitor has never had a beat.
type Previous struct {
	Status  int16
	Retries int32
}

// Outcome is the beat derived from a check result.
type Outcome struct {
	Status    int16
	Retries   int32
	Important bool
	Ping      *float64
	Msg       string
}

// Next applies the retry, upside-down and important-beat rules to a check result.
func (m *Monitor) Next(prev *Previous, result Result) Outcome {
	var retries int32
	if prev != nil {
		retries = prev.Retries
	}

	out := Outcome{Ping: result.Ping, Msg: sanitizeMsg(result.Msg)}
	if result.Pending {
		out.Status = StatusPending
		out.Important = prev == nil || IsImportant(prev.Status, out.Status)
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
	case m.UpsideDown && out.Status == StatusUp:
		retries = 0
	case m.MaxRetries > 0 && retries < m.MaxRetries:
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
	return out
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

// sanitizeMsg makes remote text safe to store. PostgreSQL rejects NUL and invalid
// UTF-8, and one bad message must not fail a whole batch of beats.
func sanitizeMsg(msg string) string {
	if len(msg) > maxMsgBytes {
		msg = msg[:maxMsgBytes]
	}
	return strings.ReplaceAll(strings.ToValidUTF8(msg, "�"), "\x00", "")
}
