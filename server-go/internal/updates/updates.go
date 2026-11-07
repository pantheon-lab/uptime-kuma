// Package updates finds out whether a newer version of this service has been released, for the
// UI's update notice. The Node.js backend asked uptime.kuma.pet, which knows upstream's
// releases. This fork is released as v1.2.3 tags in its own repository, so it reads those.
package updates

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/semver"
)

// DefaultURL lists this fork's tags.
const DefaultURL = "https://api.github.com/repos/pantheon-lab/uptime-kuma/tags?per_page=100"

// Interval is how often to check, as in Node.js.
const Interval = 48 * time.Hour

// Settings reports the UI's "Show update if available" and "Also check beta release".
type Settings func(ctx context.Context) (check, beta bool, err error)

// Checker knows the newest released version.
type Checker struct {
	url      string
	client   *http.Client
	settings Settings
	log      *slog.Logger

	mu     sync.Mutex
	latest string
	wake   chan struct{}
}

// New returns a checker that reads tags from url.
func New(url string, client *http.Client, settings Settings, log *slog.Logger) *Checker {
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	return &Checker{url: url, client: client, settings: settings, log: log, wake: make(chan struct{}, 1)}
}

// Latest is the newest version, without the "v", or empty when it isn't known or checking is off.
func (c *Checker) Latest() string {
	if c == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.latest
}

// Recheck checks again soon, such as after the settings changed.
func (c *Checker) Recheck() {
	if c == nil {
		return
	}
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// Run checks now, then every Interval, until ctx ends.
func (c *Checker) Run(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		case <-c.wake:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
		}
		c.check(ctx)
		timer.Reset(Interval)
	}
}

func (c *Checker) check(ctx context.Context) {
	check, beta, err := c.settings(ctx)
	if err != nil {
		c.log.Warn("update check: read settings", "error", err)
		return
	}
	if !check {
		c.set("")
		return
	}
	latest, err := c.fetch(ctx, beta)
	if err != nil {
		// As in Node.js, a failed check keeps what was known.
		c.log.Info("update check failed", "error", err)
		return
	}
	c.set(latest)
}

func (c *Checker) set(latest string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.latest = latest
}

func (c *Checker) fetch(ctx context.Context, beta bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "Uptime Kuma update check")
	resp, err := c.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tags: %s", resp.Status)
	}
	var tags []struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(&tags); err != nil {
		return "", fmt.Errorf("tags: %w", err)
	}
	names := make([]string, len(tags))
	for i, t := range tags {
		names[i] = t.Name
	}
	return Newest(names, beta), nil
}

// Newest is the highest release among tags such as v1.2.3, without the "v". Pre-releases such
// as v1.3.0-beta.1 count only with beta. Other tags are ignored.
func Newest(tags []string, beta bool) string {
	best := ""
	for _, tag := range tags {
		if !strings.HasPrefix(tag, "v") || !semver.IsValid(tag) || semver.Build(tag) != "" {
			continue
		}
		// v1 and v1.2 are valid semver here, but aren't releases.
		if strings.Count(strings.SplitN(tag, "-", 2)[0], ".") != 2 {
			continue
		}
		if semver.Prerelease(tag) != "" && !beta {
			continue
		}
		if best == "" || semver.Compare(tag, best) > 0 {
			best = tag
		}
	}
	return strings.TrimPrefix(best, "v")
}
