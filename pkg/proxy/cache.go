// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package proxy

import (
	"container/list"
	"sync"
	"time"
)

// locationCache is a thread-safe LRU cache for TenantLocation values with
// per-entry TTL. It is used by both RegistryResolver and HTTPResolver.
//
// Eviction policy: LRU. Entries are also expired on read if their TTL has
// elapsed. The cache does not run a background goroutine; expiry is lazy.
type locationCache struct {
	mu      sync.Mutex
	cap     int
	ttl     time.Duration
	entries map[string]*list.Element
	lru     *list.List
}

type cacheEntry struct {
	key     string
	value   TenantLocation
	expires time.Time
}

func newLocationCache(capacity int, ttl time.Duration) *locationCache {
	if capacity <= 0 {
		capacity = 1024
	}
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &locationCache{
		cap:     capacity,
		ttl:     ttl,
		entries: make(map[string]*list.Element, capacity),
		lru:     list.New(),
	}
}

// get returns a cached location and true if found and not expired.
func (c *locationCache) get(key string) (TenantLocation, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.entries[key]
	if !ok {
		return TenantLocation{}, false
	}

	entry := el.Value.(*cacheEntry)
	if time.Now().After(entry.expires) {
		c.lru.Remove(el)
		delete(c.entries, key)
		return TenantLocation{}, false
	}

	c.lru.MoveToFront(el)
	return entry.value, true
}

// set stores a location with the default TTL.
func (c *locationCache) set(key string, value TenantLocation) {
	c.setWithTTL(key, value, c.ttl)
}

// setWithTTL stores a location with a specific TTL.
func (c *locationCache) setWithTTL(key string, value TenantLocation, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		c.lru.MoveToFront(el)
		el.Value.(*cacheEntry).value = value
		el.Value.(*cacheEntry).expires = time.Now().Add(ttl)
		return
	}

	// Evict LRU entry if at capacity.
	if c.lru.Len() >= c.cap {
		oldest := c.lru.Back()
		if oldest != nil {
			c.lru.Remove(oldest)
			delete(c.entries, oldest.Value.(*cacheEntry).key)
		}
	}

	el := c.lru.PushFront(&cacheEntry{
		key:     key,
		value:   value,
		expires: time.Now().Add(ttl),
	})
	c.entries[key] = el
}

// delete removes a key from the cache (cache invalidation on 307).
func (c *locationCache) delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.entries[key]; ok {
		c.lru.Remove(el)
		delete(c.entries, key)
	}
}

// size returns the number of entries currently in the cache.
func (c *locationCache) size() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lru.Len()
}
