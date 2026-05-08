// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

func setupTestStore(t *testing.T) (storage.Store, string) {
	t.Helper()
	
	tmpDir, err := os.MkdirTemp("", "olu-storage-test-*")
	if err != nil {
		t.Fatal(err)
	}

	storeConfig := map[string]interface{}{
		"base_dir": tmpDir,
		"schema":   "test",
	}

	store, err := storage.NewStore("jsonfile", storeConfig)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	return store, tmpDir
}

func TestStoreCreate(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	t.Run("Create entity", func(t *testing.T) {
		data := map[string]interface{}{
			"name":  "Test User",
			"email": "test@example.com",
		}

		id, err := store.Create(ctx, "users", data)
		if err != nil {
			t.Fatalf("Create failed: %v", err)
		}

		if id <= 0 {
			t.Errorf("Expected positive ID, got %d", id)
		}
	})

	t.Run("Create multiple entities", func(t *testing.T) {
		var ids []int
		for i := 0; i < 5; i++ {
			data := map[string]interface{}{
				"name": "User",
				"num":  i,
			}
			id, err := store.Create(ctx, "users", data)
			if err != nil {
				t.Fatalf("Create %d failed: %v", i, err)
			}
			ids = append(ids, id)
		}

		// Check IDs are unique and sequential
		for i := 1; i < len(ids); i++ {
			if ids[i] <= ids[i-1] {
				t.Errorf("IDs not sequential: %v", ids)
			}
		}
	})
}

func TestStoreGet(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// Create test data
	data := map[string]interface{}{
		"name":  "Test User",
		"email": "test@example.com",
		"age":   30,
	}

	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Get existing entity", func(t *testing.T) {
		retrieved, err := store.Get(ctx, "users", id)
		if err != nil {
			t.Fatalf("Get failed: %v", err)
		}

		if retrieved["name"] != "Test User" {
			t.Errorf("Expected name 'Test User', got %v", retrieved["name"])
		}
		if retrieved["email"] != "test@example.com" {
			t.Errorf("Expected email 'test@example.com', got %v", retrieved["email"])
		}
		if retrieved["age"].(float64) != 30 {
			t.Errorf("Expected age 30, got %v", retrieved["age"])
		}
	})

	t.Run("Get non-existent entity", func(t *testing.T) {
		_, err := store.Get(ctx, "users", 99999)
		if err == nil {
			t.Error("Expected error for non-existent entity")
		}
		if !errors.Is(err, storage.ErrNotFound) {
			t.Errorf("Expected ErrNotFound, got %v", err)
		}
	})
}

func TestStoreUpdate(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// Create test data
	data := map[string]interface{}{
		"name":  "Test User",
		"email": "test@example.com",
	}

	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Update existing entity", func(t *testing.T) {
		updateData := map[string]interface{}{
			"name":  "Updated User",
			"email": "updated@example.com",
		}

		err := store.Update(ctx, "users", id, updateData)
		if err != nil {
			t.Fatalf("Update failed: %v", err)
		}

		// Verify update
		retrieved, _ := store.Get(ctx, "users", id)
		if retrieved["name"] != "Updated User" {
			t.Errorf("Expected updated name, got %v", retrieved["name"])
		}
	})

	t.Run("Update non-existent entity", func(t *testing.T) {
		updateData := map[string]interface{}{
			"name": "Should Fail",
		}

		err := store.Update(ctx, "users", 99999, updateData)
		if err == nil {
			t.Error("Expected error for non-existent entity")
		}
	})
}

func TestStorePatch(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// Create test data
	data := map[string]interface{}{
		"name":  "Test User",
		"email": "test@example.com",
		"age":   30,
	}

	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Patch partial fields", func(t *testing.T) {
		patchData := map[string]interface{}{
			"age": 31,
		}

		err := store.Patch(ctx, "users", id, patchData)
		if err != nil {
			t.Fatalf("Patch failed: %v", err)
		}

		// Verify patch
		retrieved, _ := store.Get(ctx, "users", id)
		if retrieved["age"].(float64) != 31 {
			t.Errorf("Expected age 31, got %v", retrieved["age"])
		}
		// Other fields should remain
		if retrieved["name"] != "Test User" {
			t.Errorf("Expected name unchanged, got %v", retrieved["name"])
		}
	})

	t.Run("Patch non-existent entity", func(t *testing.T) {
		patchData := map[string]interface{}{
			"age": 40,
		}

		err := store.Patch(ctx, "users", 99999, patchData)
		if err == nil {
			t.Error("Expected error for non-existent entity")
		}
	})
}

func TestStoreDelete(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// Create test data
	data := map[string]interface{}{
		"name": "Test User",
	}

	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Delete existing entity", func(t *testing.T) {
		err := store.Delete(ctx, "users", id)
		if err != nil {
			t.Fatalf("Delete failed: %v", err)
		}

		// Verify deletion
		_, err = store.Get(ctx, "users", id)
		if err == nil {
			t.Error("Entity still exists after delete")
		}
	})

	t.Run("Delete non-existent entity", func(t *testing.T) {
		err := store.Delete(ctx, "users", 99999)
		if err == nil {
			t.Error("Expected error for non-existent entity")
		}
	})
}

func TestStoreSave(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	t.Run("Save with specific ID", func(t *testing.T) {
		data := map[string]interface{}{
			"name": "Fixed ID User",
		}

		created, err := store.Save(ctx, "users", 100, data)
		if err != nil {
			t.Fatalf("Save failed: %v", err)
		}
		if !created {
			t.Error("Expected created=true for first save")
		}

		// Verify save
		retrieved, err := store.Get(ctx, "users", 100)
		if err != nil {
			t.Fatalf("Get after save failed: %v", err)
		}

		if retrieved["id"].(float64) != 100 {
			t.Errorf("Expected ID 100, got %v", retrieved["id"])
		}
	})

	t.Run("Save duplicate ID overwrites (upsert)", func(t *testing.T) {
		data := map[string]interface{}{
			"name": "Overwritten",
		}

		created, err := store.Save(ctx, "users", 100, data)
		if err != nil {
			t.Errorf("Save overwrite should succeed, got error: %v", err)
		}
		if created {
			t.Error("Expected created=false for overwrite")
		}
	})
}

func TestStoreSaveOptimisticConcurrency(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// First save: create at ID 200.
	_, err := store.Save(ctx, "things", 200, map[string]interface{}{"val": "a"})
	if err != nil {
		t.Fatalf("initial save: %v", err)
	}

	// Read back to get current _version.
	retrieved, err := store.Get(ctx, "things", 200)
	if err != nil {
		t.Fatalf("get: %v", err)
	}

	t.Run("conditional overwrite with correct version succeeds", func(t *testing.T) {
		created, err := store.Save(ctx, "things", 200, map[string]interface{}{
			"val":      "b",
			"_version": retrieved["_version"],
		})
		if err != nil {
			t.Errorf("conditional save with correct version should succeed: %v", err)
		}
		if created {
			t.Error("expected created=false for overwrite")
		}
		// Verify data updated.
		got, _ := store.Get(ctx, "things", 200)
		if got["val"] != "b" {
			t.Errorf("expected val=b, got %v", got["val"])
		}
	})

	t.Run("conditional overwrite with stale version returns conflict", func(t *testing.T) {
		// Use the original version, which is now stale (incremented by the previous write).
		_, err := store.Save(ctx, "things", 200, map[string]interface{}{
			"val":      "c",
			"_version": retrieved["_version"], // stale
		})
		if !errors.Is(err, storage.ErrConflict) {
			t.Errorf("expected ErrConflict for stale version, got: %v", err)
		}
		// Data should be unchanged.
		got, _ := store.Get(ctx, "things", 200)
		if got["val"] != "b" {
			t.Errorf("data should not have changed on conflict, got val=%v", got["val"])
		}
	})

	t.Run("unconditional overwrite (no _version) always succeeds", func(t *testing.T) {
		_, err := store.Save(ctx, "things", 200, map[string]interface{}{"val": "d"})
		if err != nil {
			t.Errorf("unconditional save should succeed regardless of version: %v", err)
		}
		got, _ := store.Get(ctx, "things", 200)
		if got["val"] != "d" {
			t.Errorf("expected val=d, got %v", got["val"])
		}
	})
}

func TestStoreList(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	t.Run("List empty entity type", func(t *testing.T) {
		list, err := store.List(ctx, "users")
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}

		if len(list) != 0 {
			t.Errorf("Expected empty list, got %d items", len(list))
		}
	})

	t.Run("List with entities", func(t *testing.T) {
		// Create test data
		for i := 0; i < 5; i++ {
			data := map[string]interface{}{
				"name": "User",
				"num":  i,
			}
			store.Create(ctx, "users", data)
		}

		list, err := store.List(ctx, "users")
		if err != nil {
			t.Fatalf("List failed: %v", err)
		}

		if len(list) != 5 {
			t.Errorf("Expected 5 items, got %d", len(list))
		}
	})
}

func TestStoreExists(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// Create test data
	data := map[string]interface{}{
		"name": "Test User",
	}

	id, err := store.Create(ctx, "users", data)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	t.Run("Exists returns true for existing entity", func(t *testing.T) {
		exists := store.Exists(ctx, "users", id)
		if !exists {
			t.Error("Expected entity to exist")
		}
	})

	t.Run("Exists returns false for non-existent entity", func(t *testing.T) {
		exists := store.Exists(ctx, "users", 99999)
		if exists {
			t.Error("Expected entity to not exist")
		}
	})
}

func TestStoreSearch(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	// Check if store supports search
	searcher, ok := store.(storage.Searcher)
	if !ok {
		t.Skip("Store does not support search")
	}

	// Create test data
	users := []map[string]interface{}{
		{"name": "Alice Smith", "email": "alice@example.com"},
		{"name": "Bob Smith", "email": "bob@example.com"},
		{"name": "Charlie Brown", "email": "charlie@example.com"},
		{"name": "Alice Johnson", "email": "alice.j@example.com"},
	}

	for _, user := range users {
		store.Create(ctx, "users", user)
	}

	t.Run("Search contains", func(t *testing.T) {
		results, err := searcher.Search(ctx, "users", "name", "Alice", "contains")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 2 {
			t.Errorf("Expected 2 results, got %d", len(results))
		}
	})

	t.Run("Search starts", func(t *testing.T) {
		results, err := searcher.Search(ctx, "users", "name", "Alice", "starts")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 2 {
			t.Errorf("Expected 2 results, got %d", len(results))
		}
	})

	t.Run("Search ends", func(t *testing.T) {
		results, err := searcher.Search(ctx, "users", "name", "Smith", "ends")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 2 {
			t.Errorf("Expected 2 results, got %d", len(results))
		}
	})

	t.Run("Search exact", func(t *testing.T) {
		results, err := searcher.Search(ctx, "users", "name", "alice smith", "exact")
		if err != nil {
			t.Fatalf("Search failed: %v", err)
		}

		if len(results) != 1 {
			t.Errorf("Expected 1 result, got %d", len(results))
		}
	})
}

func TestStoreConcurrency(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	t.Run("Concurrent creates", func(t *testing.T) {
		const numGoroutines = 10

		done := make(chan int, numGoroutines)

		for i := 0; i < numGoroutines; i++ {
			go func(n int) {
				data := map[string]interface{}{
					"name": "Concurrent User",
					"num":  n,
				}
				id, err := store.Create(ctx, "users", data)
				if err != nil {
					t.Errorf("Concurrent create failed: %v", err)
				}
				done <- id
			}(i)
		}

		ids := make(map[int]bool)
		for i := 0; i < numGoroutines; i++ {
			id := <-done
			if ids[id] {
				t.Errorf("Duplicate ID generated: %d", id)
			}
			ids[id] = true
		}

		if len(ids) != numGoroutines {
			t.Errorf("Expected %d unique IDs, got %d", numGoroutines, len(ids))
		}
	})
}

func TestStoreInfo(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	t.Run("Store provides info", func(t *testing.T) {
		infoProvider, ok := store.(storage.InfoProvider)
		if !ok {
			t.Skip("Store does not provide info")
		}

		info := infoProvider.Info()

		if info.Type == "" {
			t.Error("Expected store type")
		}
		if info.Version == "" {
			t.Error("Expected store version")
		}

		t.Logf("Store: %s v%s", info.Type, info.Version)
		t.Logf("  Supports Search: %v", info.SupportsSearch)
		t.Logf("  Supports Batch: %v", info.SupportsBatch)
		t.Logf("  Supports Transaction: %v", info.SupportsTransaction)
	})
}

func TestStoreFilePersistence(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "olu-storage-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	t.Run("Data persists across store instances", func(t *testing.T) {
		// Create first store instance
		storeConfig := map[string]interface{}{
			"base_dir": tmpDir,
			"schema":   "test",
		}

		store1, err := storage.NewStore("jsonfile", storeConfig)
		if err != nil {
			t.Fatal(err)
		}

		// Create data
		data := map[string]interface{}{
			"name": "Persistent User",
		}
		id, err := store1.Create(ctx, "users", data)
		if err != nil {
			t.Fatal(err)
		}

		store1.Close()

		// Create second store instance
		store2, err := storage.NewStore("jsonfile", storeConfig)
		if err != nil {
			t.Fatal(err)
		}
		defer store2.Close()

		// Verify data persists
		retrieved, err := store2.Get(ctx, "users", id)
		if err != nil {
			t.Fatalf("Data not persisted: %v", err)
		}

		if retrieved["name"] != "Persistent User" {
			t.Errorf("Expected persisted data, got %v", retrieved["name"])
		}
	})
}

func TestStoreFileStructure(t *testing.T) {
	store, tmpDir := setupTestStore(t)
	defer os.RemoveAll(tmpDir)
	defer store.Close()

	ctx := context.Background()

	t.Run("Files created in correct structure", func(t *testing.T) {
		data := map[string]interface{}{
			"name": "Test User",
		}
		id, err := store.Create(ctx, "users", data)
		if err != nil {
			t.Fatal(err)
		}

		// Check file exists
		expectedPath := filepath.Join(tmpDir, "test", "users", fmt.Sprintf("%d.json", id))
		if _, err := os.Stat(expectedPath); os.IsNotExist(err) {
			t.Errorf("Expected file at %s", expectedPath)
		}

		// Check next_id file exists
		nextIDPath := filepath.Join(tmpDir, "test", "users", "_next_id.json")
		if _, err := os.Stat(nextIDPath); os.IsNotExist(err) {
			t.Errorf("Expected next_id file at %s", nextIDPath)
		}
	})
}
