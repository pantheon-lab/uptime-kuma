package ui

import (
	"encoding/json"
	"testing"
	"time"
)

func TestEventFilterArg(t *testing.T) {
	args := func(s ...string) []json.RawMessage {
		out := make([]json.RawMessage, len(s))
		for i, a := range s {
			out[i] = json.RawMessage(a)
		}
		return out
	}

	for _, missing := range [][]json.RawMessage{nil, args("null"), args(`{}`)} {
		f, err := eventFilterArg(missing, 0)
		if err != nil || f.MonitorIDs != nil || f.Statuses != nil || f.Since != nil {
			t.Fatalf("no filter: %+v %v", f, err)
		}
	}

	f, err := eventFilterArg(args(`{"monitorIDs": [3, "7"], "statuses": [0, 3], "since": "2026-11-01T10:00:00Z"}`), 0)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	if len(f.MonitorIDs) != 2 || f.MonitorIDs[1] != 7 || len(f.Statuses) != 2 || f.Statuses[1] != 3 ||
		f.Since == nil || !f.Since.Equal(want) {
		t.Fatalf("filter: %+v", f)
	}

	for _, bad := range []string{
		`{"monitorIDs": [0]}`,
		`{"monitorIDs": [null]}`,
		`{"monitorIDs": [99999999999]}`,
		`{"statuses": [4]}`,
		`{"statuses": [-1]}`,
		`{"since": "yesterday"}`,
		`[1]`,
	} {
		if _, err := eventFilterArg(args(bad), 0); err == nil {
			t.Fatalf("%s: no error", bad)
		}
	}
}
