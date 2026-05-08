// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// newTestSQLiteStore creates a temporary SQLiteStore for testing.
func newTestSQLiteStore(t *testing.T) *SQLiteStore {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	store, err := NewSQLiteStore(dbPath, SQLiteConfig{
		FullTextEnabled: false,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSQLiteStore_ImplementsQueryable(t *testing.T) {
	store := newTestSQLiteStore(t)
	if _, ok := interface{}(store).(Queryable); !ok {
		t.Fatal("SQLiteStore should implement Queryable")
	}
}

func TestJSONFileStore_DoesNotImplementQueryable(t *testing.T) {
	// Create a minimal JSONFileStore to verify it does NOT satisfy Queryable
	dir := t.TempDir()
	store, err := NewJSONFileStore(dir, "test_schema")
	if err != nil {
		t.Fatalf("NewJSONFileStore: %v", err)
	}
	defer store.Close()

	if _, ok := interface{}(store).(Queryable); ok {
		t.Fatal("JSONFileStore should NOT implement Queryable")
	}
}

func TestSQLiteStore_Capabilities(t *testing.T) {
	store := newTestSQLiteStore(t)
	caps := store.Capabilities()

	if !caps.Where {
		t.Error("expected Where capability")
	}
	if !caps.OrderBy {
		t.Error("expected OrderBy capability")
	}
	if !caps.Limit {
		t.Error("expected Limit capability")
	}
	if !caps.Count {
		t.Error("expected Count capability")
	}
}

func TestSQLiteStore_CountEntities(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	// Empty entity type
	count, err := store.CountEntities(ctx, "widgets")
	if err != nil {
		t.Fatalf("CountEntities on empty: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for empty entity, got %d", count)
	}

	// Seed some records
	for i := 0; i < 50; i++ {
		if _, err := store.Create(ctx, "widgets", map[string]interface{}{
			"name": "widget",
			"seq":  i,
		}); err != nil {
			t.Fatalf("seed record %d: %v", i, err)
		}
	}
	// Different entity type
	for i := 0; i < 10; i++ {
		if _, err := store.Create(ctx, "gadgets", map[string]interface{}{
			"name": "gadget",
		}); err != nil {
			t.Fatalf("seed gadget %d: %v", i, err)
		}
	}

	count, err = store.CountEntities(ctx, "widgets")
	if err != nil {
		t.Fatalf("CountEntities widgets: %v", err)
	}
	if count != 50 {
		t.Errorf("expected 50 widgets, got %d", count)
	}

	count, err = store.CountEntities(ctx, "gadgets")
	if err != nil {
		t.Fatalf("CountEntities gadgets: %v", err)
	}
	if count != 10 {
		t.Errorf("expected 10 gadgets, got %d", count)
	}

	count, err = store.CountEntities(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("CountEntities nonexistent: %v", err)
	}
	if count != 0 {
		t.Errorf("expected 0 for nonexistent, got %d", count)
	}
}

func TestSQLiteStore_QueryWithPlan_MatchesList(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	// Seed data
	for i := 0; i < 20; i++ {
		if _, err := store.Create(ctx, "items", map[string]interface{}{
			"code":   "ITEM-" + string(rune('A'+i%26)),
			"value":  i * 10,
			"active": i%2 == 0,
		}); err != nil {
			t.Fatalf("seed item %d: %v", i, err)
		}
	}

	// QueryWithPlan with bare SELECT data should match List exactly
	listResults, err := store.List(ctx, "items")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	planResults, err := store.QueryWithPlan(ctx,
		"SELECT data, _version FROM entities WHERE entity_type = ? ORDER BY id",
		[]interface{}{"items"},
	)
	if err != nil {
		t.Fatalf("QueryWithPlan: %v", err)
	}

	if len(planResults) != len(listResults) {
		t.Fatalf("count mismatch: List=%d QueryWithPlan=%d", len(listResults), len(planResults))
	}

	// Compare each record
	for i := range listResults {
		listID := extractTestID(listResults[i])
		planID := extractTestID(planResults[i])
		if listID != planID {
			t.Errorf("record %d: List id=%v QueryWithPlan id=%v", i, listID, planID)
		}
	}
}

func TestSQLiteStore_QueryWithPlan_WithWhereClause(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	// Seed data
	for i := 0; i < 30; i++ {
		status := "active"
		if i%3 == 0 {
			status = "inactive"
		}
		if _, err := store.Create(ctx, "sensors", map[string]interface{}{
			"code":   i,
			"status": status,
			"value":  float64(i) * 1.5,
		}); err != nil {
			t.Fatalf("seed sensor %d: %v", i, err)
		}
	}

	// Push-down WHERE: status = 'active'
	results, err := store.QueryWithPlan(ctx,
		"SELECT data, _version FROM entities WHERE entity_type = ? AND json_extract(data, '$.status') = ?",
		[]interface{}{"sensors", "active"},
	)
	if err != nil {
		t.Fatalf("QueryWithPlan with WHERE: %v", err)
	}

	// Count expected: 30 total, 10 are inactive (0,3,6,...,27), so 20 active
	if len(results) != 20 {
		t.Errorf("expected 20 active sensors, got %d", len(results))
	}

	// Verify all returned records are active
	for i, rec := range results {
		if status, ok := rec["status"].(string); !ok || status != "active" {
			t.Errorf("record %d: expected status=active, got %v", i, rec["status"])
		}
	}
}

func TestSQLiteStore_QueryWithPlan_EmptyResult(t *testing.T) {
	store := newTestSQLiteStore(t)
	ctx := context.Background()

	results, err := store.QueryWithPlan(ctx,
		"SELECT data, _version FROM entities WHERE entity_type = ?",
		[]interface{}{"nonexistent"},
	)
	if err != nil {
		t.Fatalf("QueryWithPlan empty: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results, got %d", len(results))
	}
}

// extractTestID pulls the id from a record map (float64 from JSON unmarshal).
func extractTestID(rec map[string]interface{}) interface{} {
	if id, ok := rec["id"]; ok {
		return id
	}
	return nil
}

// Verify JSONFileStore exists by trying to create one.
// This test just ensures the type assertion test above is valid.
func TestJSONFileStore_Exists(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONFileStore(dir, "test_schema")
	if err != nil {
		t.Fatalf("NewJSONFileStore: %v", err)
	}
	defer store.Close()

	// Sanity: can create and list
	ctx := context.Background()
	id, err := store.Create(ctx, "test", map[string]interface{}{"hello": "world"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id == 0 {
		t.Error("expected non-zero id")
	}
}

// Check that JSONFileStore source file exists (sanity for the interface test)
func init() {
	// This will fail at compile time if JSONFileStore doesn't exist
	var _ Store = (*JSONFileStore)(nil)
}

// Benchmark CountEntities - plan says <100µs
func BenchmarkSQLiteStore_CountEntities(b *testing.B) {
	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench.db")
	store, err := NewSQLiteStore(dbPath, SQLiteConfig{})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	// Seed 1000 records
	for i := 0; i < 1000; i++ {
		store.Create(ctx, "items", map[string]interface{}{"i": i})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = store.CountEntities(ctx, "items")
	}
}

// Suppress unused import warning if os isn't needed
var _ = os.DevNull
