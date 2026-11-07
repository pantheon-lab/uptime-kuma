package ui

import (
	"sync"
	"time"
)

// maxCacheEntries bounds memory. Badge URLs can carry any query, so a client could otherwise
// fill it; when it's full, it starts again.
const maxCacheEntries = 10000

// publicCache keeps answers of public routes for a short time, as the Node.js backend's
// response cache did, so that a busy status page or badge doesn't query the database for every
// visitor. Changes made in the UI clear it.
type publicCache struct {
	mu      sync.Mutex
	entries map[string]cached
	now     func() time.Time

	routing        *routing
	routingExpires time.Time
}

type cached struct {
	status      int
	contentType string
	body        []byte
	// cacheControl replaces the header that serveCached would send.
	cacheControl string
	expires      time.Time
}

func newPublicCache() *publicCache {
	return &publicCache{entries: map[string]cached{}, now: time.Now}
}

func (c *publicCache) get(key string) (cached, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries[key]
	if !found || c.now().After(entry.expires) {
		return cached{}, false
	}
	return entry, true
}

func (c *publicCache) set(key string, entry cached, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.entries) >= maxCacheEntries {
		c.entries = map[string]cached{}
	}
	entry.expires = c.now().Add(ttl)
	c.entries[key] = entry
}

func (c *publicCache) getRouting() (routing, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.routing == nil || c.now().After(c.routingExpires) {
		return routing{}, false
	}
	return *c.routing, true
}

func (c *publicCache) setRouting(r routing, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.routing, c.routingExpires = &r, c.now().Add(ttl)
}

func (c *publicCache) clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = map[string]cached{}
	c.routing = nil
}
