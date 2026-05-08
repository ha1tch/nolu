// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"database/sql"
	"sync"
)

// DefaultStmtCacheSize is the maximum number of prepared statements
// kept in the cache before LRU eviction. Each entry holds a *sql.Stmt
// against the reader pool. For a typical olu workload the number of
// distinct SQL shapes is bounded (tens, not thousands), so 256 is
// generous while keeping memory bounded.
const DefaultStmtCacheSize = 256

// stmtEntry is a single entry in the prepared statement cache.
// It records the prepared statement and tracks recency for LRU eviction.
type stmtEntry struct {
	stmt *sql.Stmt
	gen  uint64 // generation counter — incremented on each access
}

// StmtCache is a concurrency-safe LRU cache of prepared statements.
//
// The cache is keyed by the SQL string. Since the OQL planner produces
// parameterised queries with ? placeholders, two queries with different
// parameter values but the same shape share a single prepared statement.
//
// Lifecycle:
//   - Created during SQLiteStore initialisation.
//   - Get() returns a cached *sql.Stmt or prepares and caches a new one.
//   - Close() closes all cached statements (called from SQLiteStore.Close).
//   - Invalidate() removes a single entry (for schema evolution).
//   - Reset() closes and removes all entries.
type StmtCache struct {
	mu      sync.Mutex
	db      *sql.DB            // reader pool used to prepare statements
	stmts   map[string]*stmtEntry
	gen     uint64             // monotonic generation counter
	maxSize int
}

// NewStmtCache creates a statement cache that prepares against db.
// Pass maxSize=0 to use DefaultStmtCacheSize.
func NewStmtCache(db *sql.DB, maxSize int) *StmtCache {
	if maxSize <= 0 {
		maxSize = DefaultStmtCacheSize
	}
	return &StmtCache{
		db:      db,
		stmts:   make(map[string]*stmtEntry, 64),
		maxSize: maxSize,
	}
}

// Get returns a prepared statement for the given SQL. If the statement
// is not cached, it is prepared and added to the cache, evicting the
// least-recently-used entry if the cache is full.
//
// The returned *sql.Stmt must NOT be closed by the caller — the cache
// owns the statement's lifecycle.
func (c *StmtCache) Get(query string) (*sql.Stmt, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.gen++

	if entry, ok := c.stmts[query]; ok {
		entry.gen = c.gen
		return entry.stmt, nil
	}

	// Prepare a new statement.
	stmt, err := c.db.Prepare(query)
	if err != nil {
		return nil, err
	}

	// Evict if at capacity.
	if len(c.stmts) >= c.maxSize {
		c.evictLRU()
	}

	c.stmts[query] = &stmtEntry{stmt: stmt, gen: c.gen}
	return stmt, nil
}

// Invalidate removes and closes the prepared statement for the given
// SQL, if cached. Used when a schema change invalidates a statement
// (e.g. adapted table ALTER TABLE).
func (c *StmtCache) Invalidate(query string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if entry, ok := c.stmts[query]; ok {
		entry.stmt.Close()
		delete(c.stmts, query)
	}
}

// Reset closes all cached statements and empties the cache.
// The cache remains usable — new statements will be prepared on demand.
func (c *StmtCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k, entry := range c.stmts {
		entry.stmt.Close()
		delete(c.stmts, k)
	}
}

// Close closes all cached statements. After Close, the cache must not
// be used. Called from SQLiteStore.Close().
func (c *StmtCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	for k, entry := range c.stmts {
		entry.stmt.Close()
		delete(c.stmts, k)
	}
}

// Len returns the number of cached statements. Primarily for testing.
func (c *StmtCache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stmts)
}

// evictLRU removes the entry with the lowest generation counter.
// Must be called with c.mu held.
func (c *StmtCache) evictLRU() {
	var (
		minGen uint64
		minKey string
		first  = true
	)
	for k, entry := range c.stmts {
		if first || entry.gen < minGen {
			minGen = entry.gen
			minKey = k
			first = false
		}
	}
	if !first {
		c.stmts[minKey].stmt.Close()
		delete(c.stmts, minKey)
	}
}
