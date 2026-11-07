package conditions

import (
	"encoding/json"
	"testing"
)

var record = []Variable{{ID: "record", Operators: StringOperators}}

func parse(t *testing.T, raw string) []Node {
	t.Helper()
	nodes, err := Parse(json.RawMessage(raw))
	if err != nil {
		t.Fatal(err)
	}
	return nodes
}

func TestEvaluateJoinsInOrder(t *testing.T) {
	// (a or b) and c, in order, with a group, as the UI builds them.
	nodes := parse(t, `[
		{"type": "expression", "andOr": "and", "variable": "record", "operator": "contains", "value": "1.2"},
		{"type": "expression", "andOr": "or", "variable": "record", "operator": "starts_with", "value": "9."},
		{"type": "group", "andOr": "and", "children": [
			{"type": "expression", "andOr": "and", "variable": "record", "operator": "not_ends_with", "value": ".4"},
			{"type": "expression", "andOr": "or", "variable": "record", "operator": "equals", "value": "9.9.9.4"}
		]}
	]`)
	if err := Validate(nodes, record); err != nil {
		t.Fatal(err)
	}
	for value, want := range map[string]bool{"1.2.3.5": true, "1.2.3.4": false, "9.9.9.4": true, "8.8.8.8": false} {
		got, err := Evaluate(nodes, map[string]string{"record": value})
		if err != nil || got != want {
			t.Errorf("%s: %v %v", value, got, err)
		}
	}
	got, err := Any(nodes, "record", []string{"8.8.8.8", "1.2.3.5"})
	if err != nil || !got {
		t.Errorf("any: %v %v", got, err)
	}
}

func TestNumberOperators(t *testing.T) {
	for _, c := range []struct {
		op, value, against string
		want               bool
	}{
		{NumEquals, "5", "5.0", true}, {NumNotEquals, "5", "6", true}, {LessThan, "4", "5", true}, {GreaterThan, "4", "5", false},
		{LessOrEqual, "5", "5", true}, {GreaterOrEq, "6", "5", true}, {NumEquals, "x", "5", false}, {NumNotEquals, "x", "5", true},
	} {
		got, err := Evaluate([]Node{{Type: "expression", Variable: "v", Operator: c.op, Value: c.against}}, map[string]string{"v": c.value})
		if err != nil || got != c.want {
			t.Errorf("%s %s %s: %v %v", c.value, c.op, c.against, got, err)
		}
	}
}

func TestValidateRejects(t *testing.T) {
	for name, raw := range map[string]string{
		"variable":    `[{"type": "expression", "variable": "other", "operator": "equals", "value": "x"}]`,
		"operator":    `[{"type": "expression", "variable": "record", "operator": "lt", "value": "1"}]`,
		"empty group": `[{"type": "group", "children": []}]`,
		"join":        `[{"type": "expression", "andOr": "xor", "variable": "record", "operator": "equals", "value": "x"}]`,
		"type":        `[{"type": "script"}]`,
	} {
		if err := Validate(parse(t, raw), record); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := Parse(json.RawMessage(`{"not": "a list"}`)); err == nil {
		t.Error("an object was accepted")
	}
	if nodes, err := Parse(json.RawMessage(`[]`)); err != nil || len(nodes) != 0 {
		t.Errorf("empty: %v %v", nodes, err)
	}
}
