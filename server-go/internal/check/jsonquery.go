package check

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	jsonata "github.com/blues/jsonata-go"

	"github.com/pantheon-lab/uptime-kuma/server-go/internal/monitor"
)

// jsonNumber is the syntax JSONata's $number accepts in a string.
var jsonNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]*)(\.[0-9]+)?([Ee][-+]?[0-9]+)?$`)

// evaluateJSONQuery is the Node.js backend's evaluateJsonQuery: the JSONata expression picks a
// value out of the response, which is then compared with the expected value. The message is
// the same as Node.js's.
func evaluateJSONQuery(body []byte, m monitor.Monitor) monitor.Result {
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		data = string(body)
	}
	value, err := jsonQueryValue(data, m.JSONPath)
	if err != nil {
		return monitor.Result{Msg: fmt.Sprintf("Error evaluating JSON query: %s. Response from server was: %s", err, truncatedJSON(value))}
	}
	text := jsString(value)
	passed, err := compareJSONQuery(text, m.JSONPathOperator, m.ExpectedValue)
	if err != nil {
		return monitor.Result{Msg: fmt.Sprintf("Error evaluating JSON query: %s. Response from server was: %s", err, truncatedJSON(value))}
	}
	comparison := fmt.Sprintf("(comparing %s %s %s)", text, m.JSONPathOperator, m.ExpectedValue)
	if !passed {
		return monitor.Result{Msg: "JSON query does not pass " + comparison}
	}
	return monitor.Result{OK: true, Msg: "JSON query passes " + comparison}
}

func jsonQueryValue(data any, path string) (any, error) {
	value := data
	if strings.TrimSpace(path) != "" {
		expr, err := jsonata.Compile(path)
		if err != nil {
			return data, err
		}
		value, err = expr.Eval(data)
		if errors.Is(err, jsonata.ErrUndefined) {
			value, err = undefined{}, nil
		}
		if err != nil {
			return data, err
		}
	}
	switch v := value.(type) {
	case nil, undefined:
		return value, errors.New("Empty or undefined response. Check query syntax and response structure") //nolint:staticcheck // As in Node.js.
	case string, bool, float64, int, int64, json.Number:
		return value, nil
	case []any:
		text := jsonText(v)
		if len(text) > 25 {
			text = text[:25] + "...]"
		}
		return value, fmt.Errorf("JSON query returned the array %s, but a primitive value is required. Modify your query to return a single value via [0] to get the first element or use an aggregation like $count(), $sum() or $boolean().", text) //nolint:staticcheck // As in Node.js.
	default:
		if reflect.ValueOf(value).Kind() == reflect.Slice {
			return value, errors.New("JSON query returned an array, but a primitive value is required")
		}
		return value, errors.New("The post-JSON query evaluated response from the server is of type object, which cannot be directly compared to the expected value") //nolint:staticcheck // As in Node.js.
	}
}

func compareJSONQuery(value, operator, expected string) (bool, error) {
	switch operator {
	case "==":
		return value == expected, nil
	case "!=":
		return value != expected, nil
	case "contains":
		return strings.Contains(value, expected), nil
	case ">", ">=", "<", "<=":
		left, err := jsonataNumber(value)
		if err != nil {
			return false, err
		}
		right, err := jsonataNumber(expected)
		if err != nil {
			return false, err
		}
		switch operator {
		case ">":
			return left > right, nil
		case ">=":
			return left >= right, nil
		case "<":
			return left < right, nil
		default:
			return left <= right, nil
		}
	}
	return false, fmt.Errorf("Invalid condition %s", operator) //nolint:staticcheck // As in Node.js.
}

// undefined is JSONata's undefined, which Node.js shows differently from null.
type undefined struct{}

func jsonataNumber(s string) (float64, error) {
	if !jsonNumber.MatchString(s) {
		return 0, fmt.Errorf("Unable to cast value to a number: %s", jsonText(s)) //nolint:staticcheck // JSONata's message.
	}
	return strconv.ParseFloat(s, 64)
}

// jsString is a value as JavaScript's toString writes it.
func jsString(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case bool:
		return strconv.FormatBool(v)
	case float64:
		return jsNumber(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return v.String()
	}
	return jsonText(v)
}

// jsNumber is a number as JavaScript prints it.
func jsNumber(v float64) string {
	switch {
	case math.IsNaN(v):
		return "NaN"
	case math.IsInf(v, 1):
		return "Infinity"
	case math.IsInf(v, -1):
		return "-Infinity"
	case v == 0:
		return "0"
	}
	abs := math.Abs(v)
	if abs >= 1e21 || abs < 1e-6 {
		s := strconv.FormatFloat(v, 'e', -1, 64)
		mantissa, exp, _ := strings.Cut(s, "e")
		sign := exp[:1]
		exp = strings.TrimLeft(exp[1:], "0")
		return mantissa + "e" + sign + exp
	}
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func jsonText(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprint(v)
	}
	return string(data)
}

// truncatedJSON is how Node.js shows the response in an error: as JSON, cut at 100 characters
// when it's longer than 50.
func truncatedJSON(v any) string {
	if _, isUndefined := v.(undefined); isUndefined {
		return "undefined"
	}
	text := jsonText(v)
	units := utf16.Encode([]rune(text))
	if len(units) > 50 {
		if len(units) > 100 {
			units = units[:100]
		}
		return string(utf16.Decode(units)) + "… (truncated)"
	}
	return text
}
