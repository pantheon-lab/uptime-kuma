package check

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// testdata/jsonquery-golden.json is the Node.js backend's evaluateJsonQuery, run with jsonata
// 2.2.2 by the script in testdata/jsonquery-golden.js.
func TestJSONQueryMatchesNodeJS(t *testing.T) {
	raw, err := os.ReadFile("testdata/jsonquery-golden.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Body  string `json:"body"`
		Cases []struct {
			Body     string `json:"body"`
			JSONPath string `json:"jsonPath"`
			Op       string `json:"op"`
			Expected string `json:"expected"`
			OK       bool   `json:"ok"`
			Msg      string `json:"msg"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &golden); err != nil {
		t.Fatal(err)
	}
	for _, c := range golden.Cases {
		body := golden.Body
		if c.Body != "" {
			body = c.Body
		}
		m := monitor.Monitor{JSONPath: c.JSONPath, JSONPathOperator: c.Op, ExpectedValue: c.Expected}
		got := evaluateJSONQuery([]byte(body), m)
		want := c.Msg
		// Known differences, both only in error messages: objects list their keys in order of
		// name, because Go's maps have no order, and jsonata-go treats a null field as undefined.
		if cut, found := knownDifference[c.JSONPath]; found {
			got.Msg, want = prefix(got.Msg, cut), prefix(want, cut)
		}
		if got.OK != c.OK || got.Msg != want {
			t.Errorf("%q %s %q:\n got %v %s\nwant %v %s", c.JSONPath, c.Op, c.Expected, got.OK, got.Msg, c.OK, c.Msg)
		}
	}
}

var knownDifference = map[string]string{"empty": "was: ", "items": "array ", "": "was: "}

func prefix(s, until string) string {
	for i := 0; i+len(until) <= len(s); i++ {
		if s[i:i+len(until)] == until {
			return s[:i+len(until)]
		}
	}
	return s
}

func TestJSNumber(t *testing.T) {
	for v, want := range map[float64]string{1e21: "1e+21", 5e-7: "5e-7", 0.1: "0.1", 100: "100", -2.5: "-2.5", 123456789012: "123456789012"} {
		if got := jsNumber(v); got != want {
			t.Errorf("%v: %s, want %s", v, got, want)
		}
	}
}
