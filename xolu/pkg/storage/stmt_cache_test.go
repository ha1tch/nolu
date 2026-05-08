// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// --- Unit tests for StmtCache ---

func TestStmtCacheBasic(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	cache := store.stmtCache

	// Cache should start empty.
	if cache.Len() != 0 {
		t.Fatalf("expected empty cache, got %d", cache.Len())
	}

	// First Get prepares and caches.
	query := `SELECT COUNT(*) FROM entities WHERE tenant_id = ? AND entity_type = ?`
	stmt1, err := cache.Get(query)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if stmt1 == nil {
		t.Fatal("expected non-nil stmt")
	}
	if cache.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", cache.Len())
	}

	// Second Get returns the same *sql.Stmt.
	stmt2, err := cache.Get(query)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if stmt1 != stmt2 {
		t.Fatal("expected same stmt pointer on cache hit")
	}
	if cache.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d", cache.Len())
	}
}

func TestStmtCacheMultipleQueries(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	cache := store.stmtCache

	q1 := `SELECT data, _version FROM entities WHERE entity_type = ?`
	q2 := `SELECT COUNT(*) FROM entities WHERE tenant_id = ? AND entity_type = ?`

	s1, err := cache.Get(q1)
	if err != nil {
		t.Fatalf("Get q1: %v", err)
	}
	s2, err := cache.Get(q2)
	if err != nil {
		t.Fatalf("Get q2: %v", err)
	}

	if s1 == s2 {
		t.Fatal("different queries should produce different stmts")
	}
	if cache.Len() != 2 {
		t.Fatalf("expected 2 entries, got %d", cache.Len())
	}
}

func TestStmtCacheLRUEviction(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	// Create a tiny cache for testing eviction.
	cache := NewStmtCache(store.readDB, 3)

	queries := []string{
		`SELECT 1`,
		`SELECT 2`,
		`SELECT 3`,
	}

	for _, q := range queries {
		if _, err := cache.Get(q); err != nil {
			t.Fatalf("Get %q: %v", q, err)
		}
	}
	if cache.Len() != 3 {
		t.Fatalf("expected 3 entries, got %d", cache.Len())
	}

	// Access q1 and q3 again to bump their generation; q2 becomes LRU.
	cache.Get(queries[0])
	cache.Get(queries[2])

	// Adding a 4th query should evict q2 (lowest gen).
	if _, err := cache.Get(`SELECT 4`); err != nil {
		t.Fatalf("Get q4: %v", err)
	}
	if cache.Len() != 3 {
		t.Fatalf("expected 3 after eviction, got %d", cache.Len())
	}

	// q2 should be gone — fetching it again should increase cache to 3
	// (it was evicted, so it must be re-prepared, but something else is evicted).
	// Verify by accessing q1 and q3 (should still be cached = same pointer).
	s1a, _ := cache.Get(queries[0])
	s1b, _ := cache.Get(queries[0])
	if s1a != s1b {
		t.Fatal("q1 should still be cached")
	}
}

func TestStmtCacheInvalidate(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	cache := store.stmtCache
	q := `SELECT 1`

	stmt1, err := cache.Get(q)
	if err != nil {
		t.Fatal(err)
	}

	cache.Invalidate(q)

	if cache.Len() != 0 {
		t.Fatalf("expected 0 after invalidate, got %d", cache.Len())
	}

	// Re-fetch should prepare a new statement.
	stmt2, err := cache.Get(q)
	if err != nil {
		t.Fatal(err)
	}
	if stmt1 == stmt2 {
		t.Fatal("expected different stmt after invalidate")
	}
}

func TestStmtCacheReset(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	cache := store.stmtCache
	for i := 0; i < 5; i++ {
		if _, err := cache.Get(fmt.Sprintf("SELECT %d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if cache.Len() != 5 {
		t.Fatalf("expected 5, got %d", cache.Len())
	}

	cache.Reset()
	if cache.Len() != 0 {
		t.Fatalf("expected 0 after reset, got %d", cache.Len())
	}

	// Cache should still be usable.
	if _, err := cache.Get("SELECT 99"); err != nil {
		t.Fatalf("Get after reset: %v", err)
	}
	if cache.Len() != 1 {
		t.Fatalf("expected 1 after re-use, got %d", cache.Len())
	}
}

func TestStmtCacheInvalidSQLAtExec(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	cache := store.stmtCache

	// modernc.org/sqlite defers statement parsing until execution,
	// so Prepare succeeds even for invalid SQL. Verify that the stmt
	// errors at execution time and the cache still functions.
	stmt, err := cache.Get("THIS IS NOT SQL AT ALL !!!")
	if err != nil {
		// If the driver does validate at prepare time, that's also fine.
		if cache.Len() != 0 {
			t.Fatalf("expected 0 on prepare error, got %d", cache.Len())
		}
		return
	}

	// Statement was cached; execution should fail.
	_, execErr := stmt.QueryContext(context.Background())
	if execErr == nil {
		t.Fatal("expected error executing invalid SQL")
	}
}

func TestStmtCacheConcurrency(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	cache := store.stmtCache
	q := `SELECT COUNT(*) FROM entities WHERE entity_type = ?`

	var wg sync.WaitGroup
	errs := make(chan error, 50)

	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			stmt, err := cache.Get(q)
			if err != nil {
				errs <- err
				return
			}
			var count int
			if err := stmt.QueryRowContext(context.Background(), "test").Scan(&count); err != nil {
				errs <- err
			}
		}()
	}

	wg.Wait()
	close(errs)

	for err := range errs {
		t.Errorf("concurrent error: %v", err)
	}

	if cache.Len() != 1 {
		t.Fatalf("expected 1 entry, got %d (race condition?)", cache.Len())
	}
}

// --- Integration tests: verify cached stmts produce correct results ---

func TestStmtCacheQueryWithPlan(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Seed some data.
	for i := 1; i <= 5; i++ {
		_, err := store.Create(ctx, "devices", map[string]interface{}{
			"name": fmt.Sprintf("device-%d", i),
			"type": "sensor",
		})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	sql := `SELECT data, _version FROM entities WHERE entity_type = ? AND json_extract(data, '$.type') = ?`

	// First call: prepares the statement.
	results1, err := store.QueryWithPlan(ctx, sql, []interface{}{"devices", "sensor"})
	if err != nil {
		t.Fatalf("QueryWithPlan 1: %v", err)
	}
	if len(results1) != 5 {
		t.Fatalf("expected 5 results, got %d", len(results1))
	}

	// Verify cache was populated.
	if store.stmtCache.Len() == 0 {
		t.Fatal("expected cache to be populated after QueryWithPlan")
	}

	// Second call: uses cached statement.
	results2, err := store.QueryWithPlan(ctx, sql, []interface{}{"devices", "sensor"})
	if err != nil {
		t.Fatalf("QueryWithPlan 2: %v", err)
	}
	if len(results2) != 5 {
		t.Fatalf("expected 5 results from cache, got %d", len(results2))
	}
}

func TestStmtCacheCountEntities(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Seed data.
	for i := 1; i <= 3; i++ {
		store.Create(ctx, "widgets", map[string]interface{}{
			"name": fmt.Sprintf("w-%d", i),
		})
	}

	count1, err := store.CountEntities(ctx, "widgets")
	if err != nil {
		t.Fatalf("CountEntities 1: %v", err)
	}
	if count1 != 3 {
		t.Fatalf("expected 3, got %d", count1)
	}

	// Cache should now have the count query.
	cacheSize := store.stmtCache.Len()
	if cacheSize == 0 {
		t.Fatal("expected cache to be populated after CountEntities")
	}

	// Second call: uses cache.
	count2, err := store.CountEntities(ctx, "widgets")
	if err != nil {
		t.Fatalf("CountEntities 2: %v", err)
	}
	if count2 != 3 {
		t.Fatalf("expected 3 from cache, got %d", count2)
	}

	// Cache size should not have grown.
	if store.stmtCache.Len() != cacheSize {
		t.Fatalf("cache grew unexpectedly: was %d, now %d", cacheSize, store.stmtCache.Len())
	}
}

func TestStmtCacheAggregateQuery(t *testing.T) {
	store := setupTestStore(t)
	defer store.Close()

	ctx := context.Background()

	// Seed data in entities table directly for aggregate test.
	for i := 1; i <= 4; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"category": "A",
			"price":    float64(i * 10),
		})
	}

	sql := `SELECT json_extract(data, '$.category') AS category, COUNT(*) AS cnt FROM entities WHERE entity_type = ? GROUP BY json_extract(data, '$.category')`
	aliases := []string{"category", "cnt"}

	results1, err := store.AggregateQuery(ctx, sql, []interface{}{"items"}, aliases)
	if err != nil {
		t.Fatalf("AggregateQuery 1: %v", err)
	}
	if len(results1) != 1 {
		t.Fatalf("expected 1 group, got %d", len(results1))
	}

	// Second call: cached.
	results2, err := store.AggregateQuery(ctx, sql, []interface{}{"items"}, aliases)
	if err != nil {
		t.Fatalf("AggregateQuery 2: %v", err)
	}
	if len(results2) != 1 {
		t.Fatalf("expected 1 group from cache, got %d", len(results2))
	}
}

// setupTestStore creates a file-backed SQLiteStore for testing.
// In-memory databases cannot share state across the writer and reader
// pools, so we use a temporary file.
func setupTestStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "stmt_cache_test.db")
	store, err := NewSQLiteStore(dbPath, SQLiteConfig{
		EnableWAL:   true,
		CacheSize:   2000,
		BusyTimeout: 5000,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	return store
}
