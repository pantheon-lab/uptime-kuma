// Package monitor defines monitor configuration and the rules that turn a check
// result into a beat. The rules mirror the Node.js backend's server/model/monitor.js.
package monitor

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Supported monitor types, using the Node.js backend's names.
const (
	TypeHTTP    = "http"
	TypeKeyword = "keyword"
	TypePort    = "port"
	TypeGroup   = "group"
)

// Beat statuses, using the Node.js backend's values.
const (
	StatusDown        int16 = 0
	StatusUp          int16 = 1
	StatusPending     int16 = 2
	StatusMaintenance int16 = 3
)

const (
	minIntervalSeconds = 1
	maxIntervalSeconds = 2073600 // Same upper bound as the Node.js UI: 24 days.
	maxRetries         = 1000
	maxRedirects       = 100
	maxBodyLen         = 1 << 20
)

var (
	statusCodeRange = regexp.MustCompile(`^\d{3}(-\d{3})?$`)
	httpMethods     = []string{"GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"}
)

// Monitor is one monitor's configuration. Timeout is in seconds and 0 means 80%
// of the interval. Times are in seconds.
type Monitor struct {
	ID            int32   `json:"id"`
	Name          string  `json:"name"`
	Type          string  `json:"type"`
	Active        bool    `json:"active"`
	Interval      int32   `json:"interval"`
	RetryInterval int32   `json:"retryInterval"`
	Timeout       float64 `json:"timeout"`
	MaxRetries    int32   `json:"maxRetries"`
	UpsideDown    bool    `json:"upsideDown"`
	// ParentID is the group that this monitor belongs to, if any.
	ParentID *int32 `json:"parentId"`

	URL                 string            `json:"url"`
	Method              string            `json:"method"`
	Headers             map[string]string `json:"headers"`
	Body                string            `json:"body"`
	BodyEncoding        string            `json:"bodyEncoding"`
	AcceptedStatusCodes []string          `json:"acceptedStatusCodes"`
	MaxRedirects        int32             `json:"maxRedirects"`
	IgnoreTLS           bool              `json:"ignoreTls"`
	Keyword             string            `json:"keyword"`
	InvertKeyword       bool              `json:"invertKeyword"`

	Hostname string `json:"hostname"`
	Port     int32  `json:"port"`

	// PropagateTrace sends a W3C traceparent header to the target, so that the target's
	// traces join this one. It's off by default, to not share trace ids with third parties.
	PropagateTrace bool `json:"propagateTrace"`
}

// New returns a monitor with the Node.js backend's defaults, for decoding input into.
func New() Monitor {
	return Monitor{
		Active:              true,
		Interval:            60,
		Method:              "GET",
		BodyEncoding:        "json",
		AcceptedStatusCodes: []string{"200-299"},
		MaxRedirects:        10,
		Headers:             map[string]string{},
	}
}

// Validate reports the first problem with the configuration.
func (m *Monitor) Validate() error {
	name := strings.TrimSpace(m.Name)
	switch {
	case name == "" || len(name) > 150:
		return errors.New("name must be 1 to 150 characters")
	case m.Interval < minIntervalSeconds || m.Interval > maxIntervalSeconds:
		return fmt.Errorf("interval must be from %d to %d seconds", minIntervalSeconds, maxIntervalSeconds)
	case m.RetryInterval < 0 || m.RetryInterval > maxIntervalSeconds:
		return fmt.Errorf("retryInterval must be from 0 to %d seconds", maxIntervalSeconds)
	case m.Timeout < 0 || m.Timeout > maxIntervalSeconds:
		return fmt.Errorf("timeout must be from 0 to %d seconds", maxIntervalSeconds)
	case m.MaxRetries < 0 || m.MaxRetries > maxRetries:
		return fmt.Errorf("maxRetries must be from 0 to %d", maxRetries)
	}

	if m.ParentID != nil && *m.ParentID <= 0 {
		return errors.New("parentId must be a positive integer")
	}

	switch m.Type {
	case TypeGroup:
		return nil
	case TypeHTTP:
		return m.validateHTTP()
	case TypeKeyword:
		if m.Keyword == "" {
			return errors.New("keyword is required")
		}
		return m.validateHTTP()
	case TypePort:
		if strings.TrimSpace(m.Hostname) == "" {
			return errors.New("hostname is required")
		}
		if m.Port < 1 || m.Port > 65535 {
			return errors.New("port must be from 1 to 65535")
		}
		return nil
	default:
		return fmt.Errorf("type must be one of %s, %s, %s or %s", TypeHTTP, TypeKeyword, TypePort, TypeGroup)
	}
}

func (m *Monitor) validateHTTP() error {
	u, err := url.Parse(m.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return errors.New("url must be a valid http or https URL")
	}
	if !slices.Contains(httpMethods, m.Method) {
		return fmt.Errorf("method must be one of %s", strings.Join(httpMethods, ", "))
	}
	if m.MaxRedirects < 0 || m.MaxRedirects > maxRedirects {
		return fmt.Errorf("maxRedirects must be from 0 to %d", maxRedirects)
	}
	if len(m.Body) > maxBodyLen {
		return fmt.Errorf("body must be at most %d bytes", maxBodyLen)
	}
	switch m.BodyEncoding {
	case "json":
		if strings.TrimSpace(m.Body) != "" && !json.Valid([]byte(m.Body)) {
			return errors.New("body is not valid JSON")
		}
	case "form", "xml":
	default:
		return errors.New("bodyEncoding must be json, form or xml")
	}
	if len(m.AcceptedStatusCodes) == 0 {
		return errors.New("acceptedStatusCodes must not be empty")
	}
	for _, code := range m.AcceptedStatusCodes {
		if !statusCodeRange.MatchString(code) {
			return fmt.Errorf("invalid status code range %q", code)
		}
	}
	return nil
}

// EffectiveTimeout is how long one check may take.
func (m *Monitor) EffectiveTimeout() time.Duration {
	seconds := m.Timeout
	if seconds <= 0 {
		seconds = float64(m.Interval) * 0.8
	}
	return time.Duration(seconds * float64(time.Second))
}

// Accepts reports whether an HTTP status code counts as success.
func (m *Monitor) Accepts(status int) bool {
	for _, code := range m.AcceptedStatusCodes {
		low, high, isRange := strings.Cut(code, "-")
		first, err := strconv.Atoi(low)
		if err != nil {
			continue
		}
		last := first
		if isRange {
			if last, err = strconv.Atoi(high); err != nil {
				continue
			}
		}
		if status >= first && status <= last {
			return true
		}
	}
	return false
}
