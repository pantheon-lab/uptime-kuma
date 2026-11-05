// Package conditions evaluates the conditions some monitor types pass or fail on, such as "the
// DNS record contains 1.2.3.4". It follows the Node.js backend's server/monitor-conditions.
package conditions

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// The operators, by the ids the UI sends.
const (
	Equals        = "equals"
	NotEquals     = "not_equals"
	Contains      = "contains"
	NotContains   = "not_contains"
	StartsWith    = "starts_with"
	NotStartsWith = "not_starts_with"
	EndsWith      = "ends_with"
	NotEndsWith   = "not_ends_with"
	NumEquals     = "num_equals"
	NumNotEquals  = "num_not_equals"
	LessThan      = "lt"
	GreaterThan   = "gt"
	LessOrEqual   = "lte"
	GreaterOrEq   = "gte"
)

// Operator is an operator as the UI lists it.
type Operator struct {
	ID      string `json:"id"`
	Caption string `json:"caption"`
}

// StringOperators are the Node.js backend's defaultStringOperators.
var StringOperators = []Operator{
	{Equals, "equals"}, {NotEquals, "not equals"}, {Contains, "contains"}, {NotContains, "not contains"},
	{StartsWith, "starts with"}, {NotStartsWith, "not starts with"}, {EndsWith, "ends with"}, {NotEndsWith, "not ends with"},
}

// NumberOperators are the Node.js backend's defaultNumberOperators.
var NumberOperators = []Operator{
	{NumEquals, "equals"}, {NumNotEquals, "not equals"}, {LessThan, "less than"}, {GreaterThan, "greater than"},
	{LessOrEqual, "less than or equal to"}, {GreaterOrEq, "greater than or equal to"},
}

// Variable is something a monitor type's conditions can test, with the operators that apply.
type Variable struct {
	ID        string     `json:"id"`
	Operators []Operator `json:"operators"`
}

const maxDepth = 10

// Node is one condition, or a group of them. AndOr joins it to the one before it.
type Node struct {
	Type     string `json:"type"`
	AndOr    string `json:"andOr"`
	Variable string `json:"variable,omitempty"`
	Operator string `json:"operator,omitempty"`
	Value    string `json:"value,omitempty"`
	Children []Node `json:"children,omitempty"`
}

// Parse reads conditions as the UI sends them. Empty means none.
func Parse(raw json.RawMessage) ([]Node, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	var nodes []Node
	if err := json.Unmarshal(raw, &nodes); err != nil {
		// The UI sends a value typed into a number field as a number.
		var loose []map[string]any
		if json.Unmarshal(raw, &loose) != nil {
			return nil, errors.New("conditions must be a list")
		}
		return nil, fmt.Errorf("invalid conditions: %w", err)
	}
	return nodes, nil
}

// Validate checks that every condition uses a variable and an operator the type has, and that
// groups aren't empty or too deep.
func Validate(nodes []Node, variables []Variable) error {
	return validate(nodes, variables, 0)
}

func validate(nodes []Node, variables []Variable, depth int) error {
	if depth > maxDepth {
		return fmt.Errorf("conditions can be at most %d groups deep", maxDepth)
	}
	for _, n := range nodes {
		if n.AndOr != "" && n.AndOr != "and" && n.AndOr != "or" {
			return fmt.Errorf("a condition must be joined with and or or, not %q", n.AndOr)
		}
		switch n.Type {
		case "group":
			if len(n.Children) == 0 {
				return errors.New("a condition group must not be empty")
			}
			if err := validate(n.Children, variables, depth+1); err != nil {
				return err
			}
		case "expression":
			i := slices.IndexFunc(variables, func(v Variable) bool { return v.ID == n.Variable })
			if i < 0 {
				return fmt.Errorf("conditions can't test %q", n.Variable)
			}
			if !slices.ContainsFunc(variables[i].Operators, func(o Operator) bool { return o.ID == n.Operator }) {
				return fmt.Errorf("%q can't be tested with %q", n.Variable, n.Operator)
			}
		default:
			return fmt.Errorf("unknown condition type %q", n.Type)
		}
	}
	return nil
}

// Evaluate reports whether the values meet the conditions. Each child is joined to the result
// so far, in order, without precedence, as in Node.js.
func Evaluate(nodes []Node, values map[string]string) (bool, error) {
	if len(nodes) == 0 {
		return false, errors.New("ConditionExpressionGroup must contain at least one child.") //nolint:staticcheck // As in Node.js.
	}
	var result bool
	for i, n := range nodes {
		var child bool
		var err error
		switch n.Type {
		case "group":
			child, err = Evaluate(n.Children, values)
		case "expression":
			child, err = test(n, values)
		default:
			err = fmt.Errorf("unknown condition type %q", n.Type)
		}
		if err != nil {
			return false, err
		}
		switch {
		case i == 0:
			result = child
		case n.AndOr == "or":
			result = result || child
		default:
			result = result && child
		}
	}
	return result, nil
}

func test(n Node, values map[string]string) (bool, error) {
	value, found := values[n.Variable]
	if !found {
		return false, errors.New("Variable missing in context: " + n.Variable) //nolint:staticcheck // As in Node.js.
	}
	switch n.Operator {
	case Equals:
		return value == n.Value, nil
	case NotEquals:
		return value != n.Value, nil
	case Contains:
		return strings.Contains(value, n.Value), nil
	case NotContains:
		return !strings.Contains(value, n.Value), nil
	case StartsWith:
		return strings.HasPrefix(value, n.Value), nil
	case NotStartsWith:
		return !strings.HasPrefix(value, n.Value), nil
	case EndsWith:
		return strings.HasSuffix(value, n.Value), nil
	case NotEndsWith:
		return !strings.HasSuffix(value, n.Value), nil
	}
	left, errLeft := strconv.ParseFloat(strings.TrimSpace(value), 64)
	right, errRight := strconv.ParseFloat(strings.TrimSpace(n.Value), 64)
	if errLeft != nil || errRight != nil {
		// Like Number() in JavaScript: anything that isn't a number compares as false.
		return n.Operator == NumNotEquals, nil
	}
	switch n.Operator {
	case NumEquals:
		return left == right, nil
	case NumNotEquals:
		return left != right, nil
	case LessThan:
		return left < right, nil
	case GreaterThan:
		return left > right, nil
	case LessOrEqual:
		return left <= right, nil
	case GreaterOrEq:
		return left >= right, nil
	}
	return false, fmt.Errorf("Unexpected expression operator ID '%s'", n.Operator) //nolint:staticcheck // As in Node.js.
}

// Any reports whether any of the values meets the conditions, which is how monitors with several
// results, such as DNS records, pass.
func Any(nodes []Node, variable string, values []string) (bool, error) {
	for _, v := range values {
		ok, err := Evaluate(nodes, map[string]string{variable: v})
		if err != nil || ok {
			return ok, err
		}
	}
	return false, nil
}
