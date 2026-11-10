package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"maps"
	"sync"
	"time"
)

// tokenKey hashes a token so raw tokens never sit in memory longer than the
// request that carried them, and never end up in a heap dump of the cache.
func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type cacheEntry[V any] struct {
	val V
	exp time.Time
}

// ttlCache is a small mutex-protected map with per-entry expiry. It is
// bounded so a flood of distinct (possibly bogus) tokens cannot grow memory
// without limit; failed verifications are never stored anyway.
type ttlCache[V any] struct {
	mu   sync.Mutex
	max  int
	now  func() time.Time
	data map[string]cacheEntry[V]
}

func newTTLCache[V any](max int, now func() time.Time) *ttlCache[V] {
	return &ttlCache[V]{max: max, now: now, data: make(map[string]cacheEntry[V])}
}

func (c *ttlCache[V]) get(key string) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.data[key]
	if !ok {
		var zero V
		return zero, false
	}
	if !c.now().Before(e.exp) {
		delete(c.data, key)
		var zero V
		return zero, false
	}
	return e.val, true
}

func (c *ttlCache[V]) put(key string, val V, exp time.Time) {
	now := c.now()
	if !now.Before(exp) {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.data[key]; !exists && len(c.data) >= c.max {
		maps.DeleteFunc(c.data, func(_ string, e cacheEntry[V]) bool { return !now.Before(e.exp) })
		// Still full of live entries: evict arbitrary ones. Losing a cache
		// entry only costs one extra round trip to the system manager.
		for k := range c.data {
			if len(c.data) < c.max {
				break
			}
			delete(c.data, k)
		}
	}
	c.data[key] = cacheEntry[V]{val: val, exp: exp}
}

func (c *ttlCache[V]) len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.data)
}
