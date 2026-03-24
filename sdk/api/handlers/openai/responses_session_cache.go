package openai

import (
	"sync"
	"time"
)

// ResponseSessionCache maps Responses API response IDs to the auth (account)
// that produced them, enabling session-sticky routing for HTTP requests.
//
// When a request carries previous_response_id, the handler looks up the
// originating auth and pins the new request to the same account.  This
// mirrors the WebSocket path's PinnedAuthMetadataKey behaviour.
type ResponseSessionCache struct {
	mu  sync.RWMutex
	ttl time.Duration
	cap int
	m   map[string]*sessionEntry
}

type sessionEntry struct {
	authID    string
	expiresAt time.Time
}

// NewResponseSessionCache creates a cache with the given TTL and max capacity.
func NewResponseSessionCache(ttl time.Duration, capacity int) *ResponseSessionCache {
	if capacity <= 0 {
		capacity = 50000
	}
	c := &ResponseSessionCache{
		ttl: ttl,
		cap: capacity,
		m:   make(map[string]*sessionEntry, 1024),
	}
	go c.reapLoop()
	return c
}

// Get returns the auth ID for a response ID, or ("", false) on miss/expiry.
func (c *ResponseSessionCache) Get(responseID string) (string, bool) {
	c.mu.RLock()
	e, ok := c.m[responseID]
	c.mu.RUnlock()
	if !ok {
		return "", false
	}
	if time.Now().After(e.expiresAt) {
		c.mu.Lock()
		delete(c.m, responseID)
		c.mu.Unlock()
		return "", false
	}
	return e.authID, true
}

// Set stores the mapping responseID → authID with the configured TTL.
func (c *ResponseSessionCache) Set(responseID, authID string) {
	if responseID == "" || authID == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.m) >= c.cap {
		c.evictOldest()
	}
	c.m[responseID] = &sessionEntry{
		authID:    authID,
		expiresAt: time.Now().Add(c.ttl),
	}
}

func (c *ResponseSessionCache) evictOldest() {
	oldest := ""
	var oldestTime time.Time
	for k, v := range c.m {
		if oldest == "" || v.expiresAt.Before(oldestTime) {
			oldest = k
			oldestTime = v.expiresAt
		}
	}
	if oldest != "" {
		delete(c.m, oldest)
	}
}

func (c *ResponseSessionCache) reapLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		now := time.Now()
		c.mu.Lock()
		for k, v := range c.m {
			if now.After(v.expiresAt) {
				delete(c.m, k)
			}
		}
		c.mu.Unlock()
	}
}
