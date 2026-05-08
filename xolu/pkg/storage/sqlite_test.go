// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

func setupSQLiteTest(t *testing.T) (storage.Store, func()) {
	t.Helper()

	// Create temp database file
	tmpFile, err := os.CreateTemp("", "olu-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	dbPath := tmpFile.Name()

	config := map[string]interface{}{
		"db_path": dbPath,
	}

	store, err := storage.NewStore("sqlite", config)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to create store: %v", err)
	}
	if store == nil {
		os.Remove(dbPath)
		t.Fatal("NewStore returned nil")
	}

	cleanup := func() {
		if store != nil {
			store.Close()
		}
		os.Remove(dbPath)
	}

	return store, cleanup
}

// setupSQLiteGraphTest creates a test SQLiteStore with GraphEnabled=true,
// so that graph_t0000 is created and syncGraphEdges runs for tenant 0.
// Use this helper for any test that queries graph_tXXXX tables directly.
func setupSQLiteGraphTest(t *testing.T) (storage.Store, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "olu-graph-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()
	dbPath := tmpFile.Name()

	store, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:         "sqlite",
		DBPath:       dbPath,
		GraphEnabled: true,
	})
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to create graph-enabled store: %v", err)
	}

	cleanup := func() {
		if store != nil {
			store.Close()
		}
		os.Remove(dbPath)
	}
	return store, cleanup
}

// Helper to create test user data
func testUserData(name string) map[string]interface{} {
	return map[string]interface{}{
		"name":   name,
		"email":  fmt.Sprintf("%s@example.com", name),
		"active": true,
	}
}

// =============================================================================
// Basic CRUD Tests
// =============================================================================

func TestSQLiteStore_Create(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	data := map[string]interface{}{
		"name":  "Alice",
		"email": "alice@example.com",
		"age":   30,
	}

	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if id != 1 {
		t.Errorf("Expected id 1, got %d", id)
	}

	// Verify data was stored
	retrieved, err := store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice" {
		t.Errorf("Expected name Alice, got %v", retrieved["name"])
	}
	if retrieved["email"] != "alice@example.com" {
		t.Errorf("Expected email alice@example.com, got %v", retrieved["email"])
	}
	if retrieved["age"] != float64(30) {
		t.Errorf("Expected age 30, got %v", retrieved["age"])
	}
	if retrieved["id"] != float64(1) {
		t.Errorf("Expected id 1, got %v", retrieved["id"])
	}
}

func TestSQLiteStore_CreateMultiple(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple entities
	id1, err := store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if id1 != 1 {
		t.Errorf("Expected id1=1, got %d", id1)
	}

	id2, err := store.Create(ctx, "users", map[string]interface{}{"name": "Bob"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if id2 != 2 {
		t.Errorf("Expected id2=2, got %d", id2)
	}

	id3, err := store.Create(ctx, "users", map[string]interface{}{"name": "Charlie"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if id3 != 3 {
		t.Errorf("Expected id3=3, got %d", id3)
	}

	// IDs should be unique and sequential
	if id1 == id2 || id2 == id3 {
		t.Error("IDs should be unique")
	}
}

func TestSQLiteStore_CreateDifferentEntityTypes(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entities of different types - IDs should be independent
	userId, err := store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if userId != 1 {
		t.Errorf("Expected userId=1, got %d", userId)
	}

	postId, err := store.Create(ctx, "posts", map[string]interface{}{"title": "Post 1"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	if postId != 1 {
		t.Errorf("Expected postId=1, got %d", postId)
	}

	// Both should have ID 1 since they're different entity types
	if userId != postId {
		t.Errorf("Expected userId == postId for different entity types")
	}
}

func TestSQLiteStore_Get(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	data := testUserData("Alice")
	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Get entity
	retrieved, err := store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice" {
		t.Errorf("Expected name Alice, got %v", retrieved["name"])
	}
	if retrieved["email"] != "Alice@example.com" {
		t.Errorf("Expected email Alice@example.com, got %v", retrieved["email"])
	}
	if retrieved["active"] != true {
		t.Errorf("Expected active true, got %v", retrieved["active"])
	}
}

func TestSQLiteStore_GetNotFound(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	_, err := store.Get(ctx, "users", 999)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteStore_Update(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, err := store.Create(ctx, "users", map[string]interface{}{
		"name": "Alice",
		"age":  30,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Update entity
	err = store.Update(ctx, "users", id, map[string]interface{}{
		"name": "Alice Smith",
		"age":  31,
	})
	if err != nil {
		t.Fatalf("Update failed: %v", err)
	}

	// Verify update
	retrieved, err := store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice Smith" {
		t.Errorf("Expected name 'Alice Smith', got %v", retrieved["name"])
	}
	if retrieved["age"] != float64(31) {
		t.Errorf("Expected age 31, got %v", retrieved["age"])
	}
}

func TestSQLiteStore_UpdateNotFound(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	err := store.Update(ctx, "users", 999, map[string]interface{}{"name": "Nobody"})
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteStore_Patch(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, err := store.Create(ctx, "users", map[string]interface{}{
		"name":  "Alice",
		"email": "alice@example.com",
		"age":   30,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Patch only age
	err = store.Patch(ctx, "users", id, map[string]interface{}{
		"age": 31,
	})
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}

	// Verify only age changed
	retrieved, err := store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice" {
		t.Errorf("Expected name Alice, got %v", retrieved["name"])
	}
	if retrieved["email"] != "alice@example.com" {
		t.Errorf("Expected email alice@example.com, got %v", retrieved["email"])
	}
	if retrieved["age"] != float64(31) {
		t.Errorf("Expected age 31, got %v", retrieved["age"])
	}
}

func TestSQLiteStore_PatchAddField(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, err := store.Create(ctx, "users", map[string]interface{}{
		"name": "Alice",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Add new field
	err = store.Patch(ctx, "users", id, map[string]interface{}{
		"email": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}

	// Verify field was added
	retrieved, err := store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice" {
		t.Errorf("Expected name Alice, got %v", retrieved["name"])
	}
	if retrieved["email"] != "alice@example.com" {
		t.Errorf("Expected email alice@example.com, got %v", retrieved["email"])
	}
}

func TestSQLiteStore_PatchRemoveField(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, err := store.Create(ctx, "users", map[string]interface{}{
		"name":  "Alice",
		"email": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Patch with nil sets the field to null (JSON null).
	// Key deletion is handled at the handler level via PatchValidated
	// with a validate callback that removes nil keys.
	err = store.Patch(ctx, "users", id, map[string]interface{}{
		"email": nil,
	})
	if err != nil {
		t.Fatalf("Patch failed: %v", err)
	}

	// Verify field is set to nil (not removed)
	retrieved, err := store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice" {
		t.Errorf("Expected name Alice, got %v", retrieved["name"])
	}
	// email key should still exist but be nil (JSON null)
	if _, hasEmail := retrieved["email"]; !hasEmail {
		t.Error("Expected email field to be present (as null)")
	}
	if retrieved["email"] != nil {
		t.Errorf("Expected email to be nil, got %v", retrieved["email"])
	}

	// Test PatchValidated with key deletion callback
	err = store.PatchValidated(ctx, "users", id, map[string]interface{}{
		"name": "Bob",
	}, func(merged map[string]interface{}) error {
		// Simulate PatchNullBehavior=delete by removing the nil email
		delete(merged, "email")
		return nil
	})
	if err != nil {
		t.Fatalf("PatchValidated failed: %v", err)
	}

	retrieved, err = store.Get(ctx, "users", id)
	if err != nil {
		t.Fatalf("Get after PatchValidated failed: %v", err)
	}
	if retrieved["name"] != "Bob" {
		t.Errorf("Expected name Bob, got %v", retrieved["name"])
	}
	if _, hasEmail := retrieved["email"]; hasEmail {
		t.Error("Expected email field to be removed by PatchValidated callback")
	}
}

func TestSQLiteStore_Delete(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, err := store.Create(ctx, "users", map[string]interface{}{
		"name": "Alice",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify exists
	if !store.Exists(ctx, "users", id) {
		t.Error("Entity should exist before delete")
	}

	// Delete entity
	err = store.Delete(ctx, "users", id)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	// Verify deleted
	if store.Exists(ctx, "users", id) {
		t.Error("Entity should not exist after delete")
	}
	_, err = store.Get(ctx, "users", id)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Expected ErrNotFound after delete, got %v", err)
	}
}

func TestSQLiteStore_DeleteNotFound(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	err := store.Delete(ctx, "users", 999)
	if !errors.Is(err, storage.ErrNotFound) {
		t.Errorf("Expected ErrNotFound, got %v", err)
	}
}

func TestSQLiteStore_List(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create multiple entities
	store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	store.Create(ctx, "users", map[string]interface{}{"name": "Bob"})
	store.Create(ctx, "users", map[string]interface{}{"name": "Charlie"})

	// List all
	results, err := store.List(ctx, "users")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}

	// Verify names
	names := make(map[string]bool)
	for _, result := range results {
		names[result["name"].(string)] = true
	}
	for _, expected := range []string{"Alice", "Bob", "Charlie"} {
		if !names[expected] {
			t.Errorf("Expected %s in results", expected)
		}
	}
}

func TestSQLiteStore_ListEmpty(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	results, err := store.List(ctx, "users")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected empty list, got %d results", len(results))
	}
}

func TestSQLiteStore_Exists(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Non-existent
	if store.Exists(ctx, "users", 1) {
		t.Error("Should not exist before creation")
	}

	// Create
	id, err := store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Exists
	if !store.Exists(ctx, "users", id) {
		t.Error("Should exist after creation")
	}

	// Delete
	store.Delete(ctx, "users", id)

	// No longer exists
	if store.Exists(ctx, "users", id) {
		t.Error("Should not exist after deletion")
	}
}

func TestSQLiteStore_Save(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Save creates a new entity with a specific ID
	created, err := store.Save(ctx, "users", 42, map[string]interface{}{
		"name":  "Alice",
		"email": "alice@example.com",
	})
	if err != nil {
		t.Fatalf("Save failed: %v", err)
	}
	if !created {
		t.Error("Expected created=true for first save")
	}

	// Verify entity was created with specified ID
	retrieved, err := store.Get(ctx, "users", 42)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if retrieved["name"] != "Alice" {
		t.Errorf("Expected name 'Alice', got %v", retrieved["name"])
	}
	if retrieved["email"] != "alice@example.com" {
		t.Errorf("Expected email alice@example.com, got %v", retrieved["email"])
	}

	// Save on existing ID should overwrite (upsert), not error
	created, err = store.Save(ctx, "users", 42, map[string]interface{}{
		"name":  "Bob",
		"email": "bob@example.com",
	})
	if err != nil {
		t.Errorf("Save on existing ID should succeed (upsert), got error: %v", err)
	}
	if created {
		t.Error("Expected created=false for overwrite")
	}

	// Verify data was overwritten
	retrieved, err = store.Get(ctx, "users", 42)
	if err != nil {
		t.Fatalf("Get after overwrite failed: %v", err)
	}
	if retrieved["name"] != "Bob" {
		t.Errorf("Expected overwritten name 'Bob', got %v", retrieved["name"])
	}
}

// =============================================================================
// Data Type Tests
// =============================================================================

func TestSQLiteStore_DataTypes(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	data := map[string]interface{}{
		"string": "hello",
		"int":    42,
		"float":  3.14,
		"bool":   true,
		"null":   nil,
		"array":  []interface{}{1, 2, 3},
		"object": map[string]interface{}{"nested": "value"},
	}

	id, err := store.Create(ctx, "test", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	retrieved, err := store.Get(ctx, "test", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if retrieved["string"] != "hello" {
		t.Errorf("String mismatch: %v", retrieved["string"])
	}
	if retrieved["float"] != 3.14 {
		t.Errorf("Float mismatch: %v", retrieved["float"])
	}
	if retrieved["bool"] != true {
		t.Errorf("Bool mismatch: %v", retrieved["bool"])
	}

	// Array should be preserved
	arr, ok := retrieved["array"].([]interface{})
	if !ok {
		t.Errorf("Array type mismatch: %T", retrieved["array"])
	} else if len(arr) != 3 {
		t.Errorf("Array length mismatch: %d", len(arr))
	}

	// Object should be preserved
	obj, ok := retrieved["object"].(map[string]interface{})
	if !ok {
		t.Errorf("Object type mismatch: %T", retrieved["object"])
	} else if obj["nested"] != "value" {
		t.Errorf("Nested value mismatch: %v", obj["nested"])
	}
}

// =============================================================================
// Search Tests
// =============================================================================

func TestSQLiteStore_Search(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create test data
	store.Create(ctx, "users", map[string]interface{}{"name": "Alice", "age": 30})
	store.Create(ctx, "users", map[string]interface{}{"name": "Bob", "age": 25})
	store.Create(ctx, "users", map[string]interface{}{"name": "Charlie", "age": 35})
	store.Create(ctx, "users", map[string]interface{}{"name": "Alicia", "age": 28})

	searcher := store.(storage.Searcher) // compile-time: SQLiteStore implements Searcher

	// Exact match
	results, err := searcher.Search(ctx, "users", "name", "Alice", "exact")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("Expected 1 result for exact match, got %d", len(results))
	}

	// Contains
	results, err = searcher.Search(ctx, "users", "name", "li", "contains")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("Expected 3 results for contains 'li' (Alice, Alicia, Charlie), got %d", len(results))
	}
}

func TestSQLiteStore_SearchStarts(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	store.Create(ctx, "users", map[string]interface{}{"name": "Alfred"})
	store.Create(ctx, "users", map[string]interface{}{"name": "Bob"})

	searcher := store.(storage.Searcher)

	results, err := searcher.Search(ctx, "users", "name", "Al", "starts")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results for starts 'Al', got %d", len(results))
	}
}

func TestSQLiteStore_SearchEnds(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	store.Create(ctx, "users", map[string]interface{}{"email": "alice@example.com"})
	store.Create(ctx, "users", map[string]interface{}{"email": "bob@example.com"})
	store.Create(ctx, "users", map[string]interface{}{"email": "charlie@other.com"})

	searcher := store.(storage.Searcher)

	results, err := searcher.Search(ctx, "users", "email", "example.com", "ends")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("Expected 2 results for ends 'example.com', got %d", len(results))
	}
}

// =============================================================================
// Graph/Reference Tests
// =============================================================================

func TestSQLiteStore_References(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create manager
	managerId, err := store.Create(ctx, "users", map[string]interface{}{
		"name": "Manager",
		"role": "manager",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Create employee with REF to manager
	employeeId, err := store.Create(ctx, "users", map[string]interface{}{
		"name": "Employee",
		"role": "employee",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     managerId,
		},
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Get employee and verify REF is stored
	employee, err := store.Get(ctx, "users", employeeId)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	managerRef, ok := employee["manager"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected manager to be a map, got %T", employee["manager"])
	}
	if managerRef["type"] != "REF" {
		t.Errorf("Expected REF type, got %v", managerRef["type"])
	}
	if managerRef["entity"] != "users" {
		t.Errorf("Expected entity 'users', got %v", managerRef["entity"])
	}
}

func TestSQLiteStore_GetNeighbors(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entities with references
	managerId, _ := store.Create(ctx, "users", map[string]interface{}{"name": "Manager"})
	store.Create(ctx, "users", map[string]interface{}{
		"name": "Employee1",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     managerId,
		},
	})
	store.Create(ctx, "users", map[string]interface{}{
		"name": "Employee2",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     managerId,
		},
	})

	// Get incoming edges (employees who report to this manager) via edge table scan.
	// Count edges where target = managerId (i.e. edges pointing TO the manager).
	sqlStore := store.(*storage.SQLiteStore)
	inCount := 0
	if err := sqlStore.ScanGraphEdges(ctx, sqlStore.Config().TenantID, func(e storage.GraphEdge) error {
		if e.TargetEntity == "users" && e.TargetID == managerId {
			inCount++
		}
		return nil
	}); err != nil {
		t.Fatalf("ScanGraphEdges failed: %v", err)
	}
	if inCount != 2 {
		t.Errorf("Expected 2 incoming edges, got %d", inCount)
	}
}

// =============================================================================
// Graph Integrity Tests
// =============================================================================

func TestSQLiteStore_VerifyGraphIntegrity(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entities with REFs
	managerId, _ := store.Create(ctx, "users", map[string]interface{}{"name": "Manager"})
	store.Create(ctx, "users", map[string]interface{}{
		"name": "Employee",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     managerId,
		},
	})

	// Test GraphIntegrity interface
	integrityStore := store.(storage.GraphIntegrity) // compile-time: SQLiteStore implements GraphIntegrity

	// Verify integrity
	err := integrityStore.VerifyGraphIntegrity(ctx)
	if err != nil {
		t.Errorf("VerifyGraphIntegrity failed: %v", err)
	}
}

func TestSQLiteStore_RebuildGraph(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entities with REFs
	managerId, _ := store.Create(ctx, "users", map[string]interface{}{"name": "Manager"})
	employeeId, _ := store.Create(ctx, "users", map[string]interface{}{
		"name": "Employee",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     managerId,
		},
	})

	sqlStore := store.(*storage.SQLiteStore)
	integrityStore := store.(storage.GraphIntegrity) // compile-time: SQLiteStore implements GraphIntegrity

	// Verify edge exists before rebuild via edge table scan.
	outEdgesBefore := outEdgesForSQLite(t, sqlStore, "users", employeeId)
	if len(outEdgesBefore) != 1 {
		t.Errorf("Expected 1 outgoing edge before rebuild, got %d", len(outEdgesBefore))
	}

	// Rebuild graph
	err := integrityStore.RebuildGraph(ctx)
	if err != nil {
		t.Fatalf("RebuildGraph failed: %v", err)
	}

	// Verify edge still present after rebuild.
	outEdgesAfter := outEdgesForSQLite(t, sqlStore, "users", employeeId)
	if len(outEdgesAfter) != 1 {
		t.Errorf("Expected 1 outgoing edge after rebuild, got %d", len(outEdgesAfter))
	}
	if len(outEdgesAfter) == 1 {
		neigh, err := sqlStore.Get(ctx, outEdgesAfter[0].TargetEntity, outEdgesAfter[0].TargetID)
		if err != nil {
			t.Fatalf("Get neighbor: %v", err)
		}
		if neigh["name"] != "Manager" {
			t.Errorf("Expected neighbor name 'Manager', got %v", neigh["name"])
		}
	}
}

// TestSQLiteStore_RebuildGraph_REFS is a regression test for the bug where
// RebuildGraph used an inline type-switch that recognised only single REF maps
// and silently dropped @REFS ([]interface{} of REF maps) edges entirely.
// After RebuildGraph the edge count must equal the pre-rebuild count.
func TestSQLiteStore_RebuildGraph_REFS(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()

	sqls, ok := store.(*storage.SQLiteStore)
	if !ok {
		t.Skip("store is not *SQLiteStore — skipping graph edge verification")
	}

	ctx := context.Background()

	makeREF := func(entity string, id int64) map[string]interface{} {
		return map[string]interface{}{
			"type":   "REF",
			"entity": entity,
			"id":     id,
		}
	}

	// One entity with a single REF, one with @REFS carrying three elements.
	singleID, err := store.Create(ctx, "post", map[string]interface{}{
		"title":  "Single REF post",
		"author": makeREF("user", 1),
	})
	if err != nil {
		t.Fatalf("Create single-REF entity: %v", err)
	}

	multiID, err := store.Create(ctx, "post", map[string]interface{}{
		"title": "Multi-REF post",
		"tags": []interface{}{
			makeREF("tag", 10),
			makeREF("tag", 20),
			makeREF("tag", 30),
		},
	})
	if err != nil {
		t.Fatalf("Create @REFS entity: %v", err)
	}

	db := sqls.DB()
	countEdges := func(sourceID int) int {
		var n int
		if err := db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM graph_t0000 WHERE source_entity = ? AND source_id = ?`,
			"post", sourceID).Scan(&n); err != nil {
			t.Fatalf("COUNT graph_t0000 for source_id=%d: %v", sourceID, err)
		}
		return n
	}

	// Verify pre-rebuild state: syncGraphEdges already handled both correctly.
	if got := countEdges(singleID); got != 1 {
		t.Errorf("pre-rebuild: single-REF entity: expected 1 edge, got %d", got)
	}
	if got := countEdges(multiID); got != 3 {
		t.Errorf("pre-rebuild: @REFS entity: expected 3 edges, got %d", got)
	}

	// Rebuild.
	integrityStore := store.(storage.GraphIntegrity)
	if err := integrityStore.RebuildGraph(ctx); err != nil {
		t.Fatalf("RebuildGraph: %v", err)
	}

	// Post-rebuild counts must be identical.
	if got := countEdges(singleID); got != 1 {
		t.Errorf("post-rebuild: single-REF entity: expected 1 edge, got %d (regression)", got)
	}
	if got := countEdges(multiID); got != 3 {
		t.Errorf("post-rebuild: @REFS entity: expected 3 edges, got %d (regression: @REFS edges dropped by RebuildGraph)", got)
	}
}

// =============================================================================
// Concurrency Tests
// =============================================================================

func TestSQLiteStore_ConcurrentCreates(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entities concurrently
	count := 20
	var wg sync.WaitGroup
	errCh := make(chan error, count)
	ids := make(chan int, count)

	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id, err := store.Create(ctx, "users", map[string]interface{}{
				"name": fmt.Sprintf("User%d", n),
			})
			if err != nil {
				errCh <- err
			} else {
				ids <- id
			}
		}(i)
	}

	wg.Wait()
	close(errCh)
	close(ids)

	// Check for errors
	for err := range errCh {
		t.Errorf("Concurrent create error: %v", err)
	}

	// Verify all created
	results, err := store.List(ctx, "users")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(results) != count {
		t.Errorf("Expected %d users, got %d", count, len(results))
	}

	// Verify IDs are unique
	idSet := make(map[int]bool)
	for id := range ids {
		if idSet[id] {
			t.Errorf("Duplicate ID: %d", id)
		}
		idSet[id] = true
	}
}

func TestSQLiteStore_ConcurrentReadWrite(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create initial entities
	for i := 1; i <= 10; i++ {
		store.Create(ctx, "users", map[string]interface{}{"name": fmt.Sprintf("User%d", i)})
	}

	var wg sync.WaitGroup
	errCh := make(chan error, 100)

	// Concurrent readers
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 1; j <= 5; j++ {
				_, err := store.Get(ctx, "users", j)
				if err != nil {
					errCh <- err
				}
			}
		}()
	}

	// Concurrent writers
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			id := n + 1
			err := store.Update(ctx, "users", id, map[string]interface{}{
				"name": fmt.Sprintf("UpdatedUser%d", n),
			})
			if err != nil {
				errCh <- err
			}
		}(i)
	}

	wg.Wait()
	close(errCh)

	// Check for errors
	for err := range errCh {
		t.Errorf("Concurrent operation error: %v", err)
	}
}

// =============================================================================
// Info Tests
// =============================================================================

func TestSQLiteStore_Info(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	infoProvider := store.(storage.InfoProvider) // compile-time: SQLiteStore implements InfoProvider

	info := infoProvider.Info()
	if info.Type != "sqlite" {
		t.Errorf("Expected type 'sqlite', got %s", info.Type)
	}
	if info.Version == "" {
		t.Error("Expected non-empty version")
	}
	if !info.SupportsSearch {
		t.Error("Expected SupportsSearch to be true")
	}
	if !info.SupportsTransaction {
		t.Error("Expected SupportsTransaction to be true")
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSQLiteStore_Create(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Create(ctx, "users", map[string]interface{}{
			"name":  "User",
			"email": "user@example.com",
		})
	}
}

func BenchmarkSQLiteStore_Get(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Create test data
	id, _ := store.Create(ctx, "users", map[string]interface{}{
		"name": "User",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Get(ctx, "users", id)
	}
}

func BenchmarkSQLiteStore_Update(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Create test data
	id, _ := store.Create(ctx, "users", map[string]interface{}{
		"name": "User",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Update(ctx, "users", id, map[string]interface{}{
			"name": "Updated",
		})
	}
}

func BenchmarkSQLiteStore_Search(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Create test data
	for i := 0; i < 100; i++ {
		store.Create(ctx, "users", map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
		})
	}

	searcher := store.(storage.Searcher)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		searcher.Search(ctx, "users", "name", "User", "contains")
	}
}

func BenchmarkSQLiteStore_List(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Create test data
	for i := 0; i < 100; i++ {
		store.Create(ctx, "users", map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.List(ctx, "users")
	}
}

// ============================================================================
// Full-Text Search Tests
// ============================================================================

func setupSQLiteTestWithFTS(t *testing.T) (storage.Store, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "olu-fts-test-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	dbPath := tmpFile.Name()

	config := map[string]interface{}{
		"db_path":           dbPath,
		"full_text_enabled": true,
	}

	store, err := storage.NewStore("sqlite", config)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to create store: %v", err)
	}

	cleanup := func() {
		if store != nil {
			store.Close()
		}
		os.Remove(dbPath)
	}

	return store, cleanup
}

func TestSQLiteStore_FullTextSearch_Basic(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	// Create test entities
	store.Create(ctx, "users", map[string]interface{}{
		"name":  "Alice Smith",
		"email": "alice@example.com",
		"bio":   "Software engineer who loves coding",
	})
	store.Create(ctx, "users", map[string]interface{}{
		"name":  "Bob Johnson",
		"email": "bob@example.com",
		"bio":   "Product manager with engineering background",
	})
	store.Create(ctx, "users", map[string]interface{}{
		"name":  "Charlie Brown",
		"email": "charlie@example.com",
		"bio":   "Designer focused on user experience",
	})

	// Search for "engineer"
	results, err := store.FullTextSearch(ctx, "engineer", "")
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}

	if len(results) != 2 {
		t.Errorf("Expected 2 results for 'engineer', got %d", len(results))
	}
}

func TestSQLiteStore_FullTextSearch_EntityFilter(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	// Create entities in different types
	store.Create(ctx, "users", map[string]interface{}{
		"name": "Alice Developer",
	})
	store.Create(ctx, "posts", map[string]interface{}{
		"title":   "Developer Guide",
		"content": "How to become a developer",
	})

	// Search across all entities
	allResults, err := store.FullTextSearch(ctx, "developer", "")
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	if len(allResults) != 2 {
		t.Errorf("Expected 2 results across all entities, got %d", len(allResults))
	}

	// Search only in users
	userResults, err := store.FullTextSearch(ctx, "developer", "users")
	if err != nil {
		t.Fatalf("FullTextSearch with entity filter failed: %v", err)
	}
	if len(userResults) != 1 {
		t.Errorf("Expected 1 result in users, got %d", len(userResults))
	}
}

func TestSQLiteStore_FullTextSearch_UpdateReindex(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, _ := store.Create(ctx, "users", map[string]interface{}{
		"name": "Original Name",
		"bio":  "Original bio content",
	})

	// Verify initial search works
	results, _ := store.FullTextSearch(ctx, "Original", "")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'Original', got %d", len(results))
	}

	// Update entity
	store.Update(ctx, "users", id, map[string]interface{}{
		"name": "Updated Name",
		"bio":  "Completely different content",
	})

	// Old content should not be found
	oldResults, _ := store.FullTextSearch(ctx, "Original", "")
	if len(oldResults) != 0 {
		t.Errorf("Expected 0 results for 'Original' after update, got %d", len(oldResults))
	}

	// New content should be found
	newResults, _ := store.FullTextSearch(ctx, "Updated", "")
	if len(newResults) != 1 {
		t.Errorf("Expected 1 result for 'Updated', got %d", len(newResults))
	}
}

func TestSQLiteStore_FullTextSearch_DeleteRemovesIndex(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, _ := store.Create(ctx, "users", map[string]interface{}{
		"name": "DeleteMe User",
	})

	// Verify search works
	results, _ := store.FullTextSearch(ctx, "DeleteMe", "")
	if len(results) != 1 {
		t.Errorf("Expected 1 result before delete, got %d", len(results))
	}

	// Delete entity
	store.Delete(ctx, "users", id)

	// Should not be found after delete
	afterResults, _ := store.FullTextSearch(ctx, "DeleteMe", "")
	if len(afterResults) != 0 {
		t.Errorf("Expected 0 results after delete, got %d", len(afterResults))
	}
}

func TestSQLiteStore_FullTextSearch_EmptyQuery(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	results, err := store.FullTextSearch(ctx, "", "")
	if err != nil {
		t.Fatalf("FullTextSearch with empty query failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("Expected 0 results for empty query, got %d", len(results))
	}
}

func TestSQLiteStore_FullTextSearch_NestedContent(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity with nested content
	store.Create(ctx, "articles", map[string]interface{}{
		"title": "Main Title",
		"metadata": map[string]interface{}{
			"author":   "Nested Author Name",
			"category": "Technology",
		},
		"tags": []interface{}{"golang", "programming", "tutorial"},
	})

	// Search for nested content
	authorResults, _ := store.FullTextSearch(ctx, "Nested", "")
	if len(authorResults) != 1 {
		t.Errorf("Expected 1 result for nested 'Nested', got %d", len(authorResults))
	}

	// Search for array content
	tagResults, _ := store.FullTextSearch(ctx, "golang", "")
	if len(tagResults) != 1 {
		t.Errorf("Expected 1 result for tag 'golang', got %d", len(tagResults))
	}
}

func TestSQLiteStore_FullTextSearch_DisabledByDefault(t *testing.T) {
	// Use regular setup (FTS disabled)
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	store.Create(ctx, "users", map[string]interface{}{
		"name": "Test User",
	})

	// Search should return empty (FTS not enabled)
	results, err := store.FullTextSearch(ctx, "Test", "")
	if err != nil {
		t.Fatalf("FullTextSearch failed: %v", err)
	}
	// With FTS disabled, no content is indexed
	if len(results) != 0 {
		t.Errorf("Expected 0 results with FTS disabled, got %d", len(results))
	}
}

func TestSQLiteStore_FullTextSearch_PatchReindex(t *testing.T) {
	store, cleanup := setupSQLiteTestWithFTS(t)
	defer cleanup()

	ctx := context.Background()

	// Create entity
	id, _ := store.Create(ctx, "users", map[string]interface{}{
		"name": "Original",
		"bio":  "Original bio",
	})

	// Patch only the bio
	store.Patch(ctx, "users", id, map[string]interface{}{
		"bio": "Patched content here",
	})

	// Should find patched content
	results, _ := store.FullTextSearch(ctx, "Patched", "")
	if len(results) != 1 {
		t.Errorf("Expected 1 result for 'Patched', got %d", len(results))
	}

	// Original name should still be findable
	nameResults, _ := store.FullTextSearch(ctx, "Original", "")
	if len(nameResults) != 1 {
		t.Errorf("Expected 1 result for 'Original' (name unchanged), got %d", len(nameResults))
	}
}

// TestSQLiteStore_REFSGraphEdges verifies that storing an entity with a
// @REFS-style field ([]interface{} of REF maps) causes one graph_t0000 row to
// be created per element, and that the stored field round-trips correctly.
func TestSQLiteStore_REFSGraphEdges(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()

	sqls, ok := store.(*storage.SQLiteStore)
	if !ok {
		t.Skip("store is not *SQLiteStore — skipping graph edge verification")
	}

	ctx := context.Background()

	// Build a []interface{} of three REF maps, as @REFS produces.
	makeREF := func(entity string, id int64) map[string]interface{} {
		return map[string]interface{}{
			"type":   "REF",
			"entity": entity,
			"id":     id,
		}
	}
	refs := []interface{}{
		makeREF("tag", 1),
		makeREF("tag", 2),
		makeREF("tag", 3),
	}

	id, err := store.Create(ctx, "post", map[string]interface{}{
		"title":     "Hello REFS",
		"post_tags": refs,
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// --- Verify stored field round-trips as []interface{} of REF maps ---
	doc, err := store.Get(ctx, "post", id)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	storedTags, ok := doc["post_tags"].([]interface{})
	if !ok {
		t.Fatalf("post_tags: expected []interface{}, got %T: %v", doc["post_tags"], doc["post_tags"])
	}
	if len(storedTags) != 3 {
		t.Errorf("post_tags: expected 3 elements, got %d", len(storedTags))
	}
	for i, elem := range storedTags {
		refMap, ok := elem.(map[string]interface{})
		if !ok {
			t.Errorf("post_tags[%d]: expected map, got %T", i, elem)
			continue
		}
		if refMap["type"] != "REF" {
			t.Errorf("post_tags[%d]: expected type=REF, got %v", i, refMap["type"])
		}
		if refMap["entity"] != "tag" {
			t.Errorf("post_tags[%d]: expected entity=tag, got %v", i, refMap["entity"])
		}
	}

	// --- Verify graph_t0000 has exactly 3 rows for this source ---
	db := sqls.DB()
	var edgeCount int
	row := db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM graph_t0000 WHERE source_entity = ? AND source_id = ?`,
		"post", id)
	if err := row.Scan(&edgeCount); err != nil {
		t.Fatalf("COUNT graph_t0000 failed: %v", err)
	}
	if edgeCount != 3 {
		t.Errorf("expected 3 graph edges, got %d", edgeCount)
	}

	// --- Verify edge targets and relationship name ---
	rows, err := db.QueryContext(ctx,
		`SELECT target_entity, target_id, relationship_name
		 FROM graph_t0000
		 WHERE source_entity = ? AND source_id = ?
		 ORDER BY target_id`,
		"post", id)
	if err != nil {
		t.Fatalf("SELECT graph_t0000 failed: %v", err)
	}
	defer rows.Close()

	expectedTargetIDs := []int64{1, 2, 3}
	i := 0
	for rows.Next() {
		var targetEntity, relName string
		var targetID int64
		if err := rows.Scan(&targetEntity, &targetID, &relName); err != nil {
			t.Fatalf("Scan row %d: %v", i, err)
		}
		if targetEntity != "tag" {
			t.Errorf("row %d: expected target_entity=tag, got %v", i, targetEntity)
		}
		if i < len(expectedTargetIDs) && targetID != expectedTargetIDs[i] {
			t.Errorf("row %d: expected target_id=%d, got %d", i, expectedTargetIDs[i], targetID)
		}
		if relName != "post_tags" {
			t.Errorf("row %d: expected relationship_name=post_tags, got %v", i, relName)
		}
		i++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if i != 3 {
		t.Errorf("expected 3 edge rows, iterated %d", i)
	}
}

// TestSQLiteStore_ScanGraphEdges verifies the GraphEdgeScanner implementation:
// edges are returned correctly, node types are preserved, and the early-exit
// path (fn returning an error) stops iteration cleanly.
func TestSQLiteStore_ScanGraphEdges(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()
	ctx := context.Background()

	// Create a small graph: post -> author, post -> tag (×2)
	authorID, _ := store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	tag1ID, _ := store.Create(ctx, "tags", map[string]interface{}{"name": "go"})
	tag2ID, _ := store.Create(ctx, "tags", map[string]interface{}{"name": "db"})
	store.Create(ctx, "posts", map[string]interface{}{
		"title":  "Hello",
		"author": map[string]interface{}{"type": "REF", "entity": "users", "id": authorID},
		"tag1":   map[string]interface{}{"type": "REF", "entity": "tags", "id": tag1ID},
		"tag2":   map[string]interface{}{"type": "REF", "entity": "tags", "id": tag2ID},
	})

	scanner, ok := store.(storage.GraphEdgeScanner)
	if !ok {
		t.Fatal("SQLiteStore does not implement GraphEdgeScanner")
	}

	// Collect all edges.
	var edges []storage.GraphEdge
	if err := scanner.ScanGraphEdges(ctx, 0, func(e storage.GraphEdge) error {
		edges = append(edges, e)
		return nil
	}); err != nil {
		t.Fatalf("ScanGraphEdges: %v", err)
	}

	// The post has three outgoing edges.
	if len(edges) != 3 {
		t.Fatalf("expected 3 edges, got %d: %v", len(edges), edges)
	}
	for _, e := range edges {
		if e.SourceEntity != "posts" {
			t.Errorf("expected source_entity=posts, got %q", e.SourceEntity)
		}
		if e.Relationship == "" {
			t.Errorf("relationship_name must not be empty")
		}
	}

	// Early-exit: fn returns an error on the first row; ScanGraphEdges must
	// propagate it and stop — not swallow it.
	sentinel := errors.New("stop")
	count := 0
	err := scanner.ScanGraphEdges(ctx, 0, func(e storage.GraphEdge) error {
		count++
		return sentinel
	})
	if err != sentinel {
		t.Errorf("expected sentinel error, got %v", err)
	}
	if count != 1 {
		t.Errorf("fn should have been called exactly once before early exit, called %d times", count)
	}
}

// TestSQLiteStore_ScanGraphEdges_MatchesRebuild verifies that ScanGraphEdges
// returns the same edge set as the graph_t0000 table after a full RebuildGraph.
// This guards against the fast hydration path producing a different result than
// what RebuildGraph (the authoritative reconstruction) would produce.
func TestSQLiteStore_ScanGraphEdges_MatchesRebuild(t *testing.T) {
	store, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()
	ctx := context.Background()

	// Build a richer graph: two entities referencing a third.
	a1, _ := store.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	a2, _ := store.Create(ctx, "users", map[string]interface{}{"name": "Bob"})
	store.Create(ctx, "posts", map[string]interface{}{
		"title":  "Post A",
		"author": map[string]interface{}{"type": "REF", "entity": "users", "id": a1},
	})
	store.Create(ctx, "posts", map[string]interface{}{
		"title":  "Post B",
		"author": map[string]interface{}{"type": "REF", "entity": "users", "id": a2},
		"coauthor": map[string]interface{}{"type": "REF", "entity": "users", "id": a1},
	})

	scanner := store.(storage.GraphEdgeScanner)

	// Collect edges via ScanGraphEdges.
	type edgeKey struct{ src, tgt, rel string }
	scanEdges := make(map[edgeKey]bool)
	if err := scanner.ScanGraphEdges(ctx, 0, func(e storage.GraphEdge) error {
		scanEdges[edgeKey{
			fmt.Sprintf("%s:%d", e.SourceEntity, e.SourceID),
			fmt.Sprintf("%s:%d", e.TargetEntity, e.TargetID),
			e.Relationship,
		}] = true
		return nil
	}); err != nil {
		t.Fatalf("ScanGraphEdges: %v", err)
	}

	// Rebuild and re-scan (rebuild rewrites graph_t0000 from entity JSON).
	integ := store.(storage.GraphIntegrity)
	if err := integ.RebuildGraph(ctx); err != nil {
		t.Fatalf("RebuildGraph: %v", err)
	}

	rebuildEdges := make(map[edgeKey]bool)
	if err := scanner.ScanGraphEdges(ctx, 0, func(e storage.GraphEdge) error {
		rebuildEdges[edgeKey{
			fmt.Sprintf("%s:%d", e.SourceEntity, e.SourceID),
			fmt.Sprintf("%s:%d", e.TargetEntity, e.TargetID),
			e.Relationship,
		}] = true
		return nil
	}); err != nil {
		t.Fatalf("ScanGraphEdges post-rebuild: %v", err)
	}

	// Both sets must be identical.
	for k := range scanEdges {
		if !rebuildEdges[k] {
			t.Errorf("edge present before rebuild but missing after: %+v", k)
		}
	}
	for k := range rebuildEdges {
		if !scanEdges[k] {
			t.Errorf("edge present after rebuild but was missing before: %+v", k)
		}
	}
	if len(scanEdges) != 3 {
		t.Errorf("expected 3 edges total, got %d", len(scanEdges))
	}
}

// TestSQLiteStore_GraphTenantIDs verifies that GraphTenantIDs returns all
// registered tenant IDs (including tenant 0) and that ScanGraphEdges correctly
// scopes results to each tenant's own graph_tXXXX edge table.
//
// This covers the multi-tenant startup hydration path: after a restart,
// loadEntitiesFromEdgeTable calls GraphTenantIDs to discover all tenants, then
// ScanGraphEdges once per tenant ID to restore each graph partition.
func TestSQLiteStore_GraphTenantIDs(t *testing.T) {
	store, cleanup := setupSQLiteTest(t)
	defer cleanup()
	ctx := context.Background()

	lister, ok := store.(storage.TenantIDLister)
	if !ok {
		t.Fatal("SQLiteStore does not implement TenantIDLister")
	}

	// On a fresh store only tenant 0 is registered (added during initSchema).
	ids, err := lister.GraphTenantIDs(ctx)
	if err != nil {
		t.Fatalf("GraphTenantIDs: %v", err)
	}
	if len(ids) != 1 || ids[0] != 0 {
		t.Fatalf("expected [0] on fresh store, got %v", ids)
	}

	// Register two tenants via the persister.
	persister := storage.NewSQLiteTenantPersister(store.(*storage.SQLiteStore).DB(), store.(*storage.SQLiteStore).ReaderDB())
	if err := persister.Save(ctx, "alpha", 1); err != nil {
		t.Fatalf("Save alpha: %v", err)
	}
	if err := persister.Save(ctx, "beta", 2); err != nil {
		t.Fatalf("Save beta: %v", err)
	}

	ids, err = lister.GraphTenantIDs(ctx)
	if err != nil {
		t.Fatalf("GraphTenantIDs after registration: %v", err)
	}
	if len(ids) != 3 {
		t.Fatalf("expected 3 tenant IDs, got %d: %v", len(ids), ids)
	}

	// IDs must be sorted: tenant 0 first, then the registered non-zero tenants.
	if ids[0] != 0 || ids[1] != 1 || ids[2] != 2 {
		t.Errorf("expected [0 1 2], got %v", ids)
	}
}

// TestSQLiteStore_MultiTenantHydration verifies the full multi-tenant graph
// hydration contract: edges written for tenant N are only visible when
// ScanGraphEdges is called with tenant ID N, not with 0 or a different ID.
//
// This guards the property that loadEntitiesFromEdgeTable would produce
// correctly isolated graph partitions for each tenant after a restart.
func TestSQLiteStore_MultiTenantHydration(t *testing.T) {
	// We need two stores — one scoped to tenant 1 for writing, and the base
	// store (tenant 0) for scanning, since ScanGraphEdges is a method on the
	// base store and accepts a tenantID argument.
	baseStore, cleanup := setupSQLiteGraphTest(t)
	defer cleanup()
	ctx := context.Background()

	sqlBase := baseStore.(*storage.SQLiteStore)

	// Create a tenant-1 scoped store sharing the same DB, with GraphEnabled
	// so the graph_t0001 table is created and syncGraphEdges routes correctly.
	t1Store, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:         "sqlite",
		DBPath:       sqlBase.Config().DBPath,
		TenantID:     1,
		GraphEnabled: true,
	})
	if err != nil {
		t.Fatalf("create tenant-1 store: %v", err)
	}
	defer t1Store.Close()

	// Write an edge for tenant 1.
	authorID, _ := t1Store.Create(ctx, "users", map[string]interface{}{"name": "T1 Alice"})
	t1Store.Create(ctx, "posts", map[string]interface{}{
		"title":  "T1 Post",
		"author": map[string]interface{}{"type": "REF", "entity": "users", "id": authorID},
	})

	// Write an edge for tenant 0.
	a0, _ := baseStore.Create(ctx, "users", map[string]interface{}{"name": "T0 Bob"})
	baseStore.Create(ctx, "posts", map[string]interface{}{
		"title":  "T0 Post",
		"author": map[string]interface{}{"type": "REF", "entity": "users", "id": a0},
	})

	scanner := baseStore.(storage.GraphEdgeScanner)

	// Tenant 0 scan must return exactly 1 edge.
	var t0Edges []storage.GraphEdge
	scanner.ScanGraphEdges(ctx, 0, func(e storage.GraphEdge) error {
		t0Edges = append(t0Edges, e)
		return nil
	})
	if len(t0Edges) != 1 {
		t.Errorf("tenant-0 scan: expected 1 edge, got %d", len(t0Edges))
	}

	// Tenant 1 scan must return exactly 1 edge.
	var t1Edges []storage.GraphEdge
	scanner.ScanGraphEdges(ctx, 1, func(e storage.GraphEdge) error {
		t1Edges = append(t1Edges, e)
		return nil
	})
	if len(t1Edges) != 1 {
		t.Errorf("tenant-1 scan: expected 1 edge, got %d", len(t1Edges))
	}

	// Edges must belong to their respective tenants.
	if len(t0Edges) == 1 && t0Edges[0].SourceEntity != "posts" {
		t.Errorf("tenant-0 edge source wrong: %q", t0Edges[0].SourceEntity)
	}
	if len(t1Edges) == 1 && t1Edges[0].SourceEntity != "posts" {
		t.Errorf("tenant-1 edge source wrong: %q", t1Edges[0].SourceEntity)
	}
}

// outEdgesForSQLite returns all outgoing GraphEdge rows for a given
// (entity, id) pair by scanning the store's edge table.
func outEdgesForSQLite(t *testing.T, store *storage.SQLiteStore, entity string, id int) []storage.GraphEdge {
	t.Helper()
	ctx := context.Background()
	var edges []storage.GraphEdge
	err := store.ScanGraphEdges(ctx, store.Config().TenantID, func(e storage.GraphEdge) error {
		if e.SourceEntity == entity && e.SourceID == id {
			edges = append(edges, e)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outEdgesForSQLite(%s:%d): %v", entity, id, err)
	}
	return edges
}
