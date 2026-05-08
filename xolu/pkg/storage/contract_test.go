// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

// contract_test.go
//
// Shared behavioural tests that verify both storage backends (JSONFile and
// SQLite) conform to the Store interface contract identically. Any divergence
// between backends will surface here.
//
// Author: ha1tch <h@ual.fi>

import (
	"context"
	"os"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

// storeFactory creates a Store and returns a cleanup function.
type storeFactory func(t *testing.T) (storage.Store, func())

func jsonfileFactory(t *testing.T) (storage.Store, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "olu-contract-jf-*")
	if err != nil {
		t.Fatal(err)
	}
	store, err := storage.NewStore("jsonfile", map[string]interface{}{
		"base_dir": tmpDir,
		"schema":   "test",
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}
	return store, func() { store.Close(); os.RemoveAll(tmpDir) }
}

func sqliteFactory(t *testing.T) (storage.Store, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "olu-contract-sq-*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()
	dbPath := tmpFile.Name()

	store, err := storage.NewStore("sqlite", map[string]interface{}{
		"db_path": dbPath,
	})
	if err != nil {
		os.Remove(dbPath)
		t.Fatal(err)
	}
	return store, func() { store.Close(); os.Remove(dbPath) }
}

// runContractSuite runs the full Store contract against a given backend.
func runContractSuite(t *testing.T, name string, factory storeFactory) {
	t.Run(name+"/Create_returns_incrementing_IDs", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		id1, err := store.Create(ctx, "items", map[string]interface{}{"name": "first"})
		if err != nil {
			t.Fatalf("Create 1: %v", err)
		}
		id2, err := store.Create(ctx, "items", map[string]interface{}{"name": "second"})
		if err != nil {
			t.Fatalf("Create 2: %v", err)
		}
		if id2 <= id1 {
			t.Errorf("IDs should increment: got id1=%d, id2=%d", id1, id2)
		}
	})

	t.Run(name+"/Create_sets_id_in_data", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		id, _ := store.Create(ctx, "items", map[string]interface{}{"name": "widget"})
		got, err := store.Get(ctx, "items", id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// ID should be present in data
		gotID, ok := got["id"]
		if !ok {
			t.Fatal("created entity should have 'id' field in data")
		}
		// JSON numbers may be float64 or int depending on backend
		switch v := gotID.(type) {
		case float64:
			if int(v) != id {
				t.Errorf("id in data = %v, want %d", v, id)
			}
		case int:
			if v != id {
				t.Errorf("id in data = %d, want %d", v, id)
			}
		default:
			t.Errorf("unexpected id type %T", gotID)
		}
	})

	t.Run(name+"/Get_returns_created_data", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		id, _ := store.Create(ctx, "items", map[string]interface{}{
			"name":   "pump",
			"status": "active",
		})
		got, err := store.Get(ctx, "items", id)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if got["name"] != "pump" {
			t.Errorf("name = %v, want %q", got["name"], "pump")
		}
		if got["status"] != "active" {
			t.Errorf("status = %v, want %q", got["status"], "active")
		}
	})

	t.Run(name+"/Get_not_found", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		_, err := store.Get(ctx, "items", 99999)
		if err == nil {
			t.Error("Get nonexistent should return error")
		}
	})

	t.Run(name+"/Update_replaces_data", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		id, _ := store.Create(ctx, "items", map[string]interface{}{
			"name": "original", "colour": "red",
		})
		err := store.Update(ctx, "items", id, map[string]interface{}{
			"name": "updated", "colour": "blue",
		})
		if err != nil {
			t.Fatalf("Update: %v", err)
		}
		got, _ := store.Get(ctx, "items", id)
		if got["name"] != "updated" {
			t.Errorf("name after update = %v, want %q", got["name"], "updated")
		}
		if got["colour"] != "blue" {
			t.Errorf("colour after update = %v, want %q", got["colour"], "blue")
		}
	})

	t.Run(name+"/Delete_removes_entity", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		id, _ := store.Create(ctx, "items", map[string]interface{}{"name": "doomed"})
		if !store.Exists(ctx, "items", id) {
			t.Fatal("should exist before delete")
		}
		err := store.Delete(ctx, "items", id)
		if err != nil {
			t.Fatalf("Delete: %v", err)
		}
		if store.Exists(ctx, "items", id) {
			t.Error("should not exist after delete")
		}
	})

	t.Run(name+"/Delete_not_found", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		err := store.Delete(ctx, "items", 99999)
		if err == nil {
			t.Error("Delete nonexistent should return error")
		}
	})

	t.Run(name+"/List_returns_all_entities", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		store.Create(ctx, "items", map[string]interface{}{"name": "a"})
		store.Create(ctx, "items", map[string]interface{}{"name": "b"})
		store.Create(ctx, "items", map[string]interface{}{"name": "c"})

		list, err := store.List(ctx, "items")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 3 {
			t.Errorf("List returned %d items, want 3", len(list))
		}
	})

	t.Run(name+"/List_empty_entity_type", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		list, err := store.List(ctx, "nonexistent")
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(list) != 0 {
			t.Errorf("List of empty entity should return 0, got %d", len(list))
		}
	})

	t.Run(name+"/Exists_true_and_false", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		id, _ := store.Create(ctx, "items", map[string]interface{}{"name": "present"})
		if !store.Exists(ctx, "items", id) {
			t.Error("Exists should return true for created entity")
		}
		if store.Exists(ctx, "items", 99999) {
			t.Error("Exists should return false for missing entity")
		}
	})

	t.Run(name+"/Save_at_specific_ID", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		_, err := store.Save(ctx, "items", 42, map[string]interface{}{
			"name": "saved-at-42",
		})
		if err != nil {
			t.Fatalf("Save: %v", err)
		}

		got, err := store.Get(ctx, "items", 42)
		if err != nil {
			t.Fatalf("Get after Save: %v", err)
		}
		if got["name"] != "saved-at-42" {
			t.Errorf("name = %v, want %q", got["name"], "saved-at-42")
		}
	})

	t.Run(name+"/Entity_isolation", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		store.Create(ctx, "dogs", map[string]interface{}{"breed": "labrador"})
		store.Create(ctx, "dogs", map[string]interface{}{"breed": "poodle"})
		store.Create(ctx, "cats", map[string]interface{}{"breed": "siamese"})

		dogs, _ := store.List(ctx, "dogs")
		cats, _ := store.List(ctx, "cats")

		if len(dogs) != 2 {
			t.Errorf("dogs: expected 2, got %d", len(dogs))
		}
		if len(cats) != 1 {
			t.Errorf("cats: expected 1, got %d", len(cats))
		}
	})

	t.Run(name+"/Search_exact_match", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		store.Create(ctx, "items", map[string]interface{}{"name": "Alpha"})
		store.Create(ctx, "items", map[string]interface{}{"name": "Beta"})
		store.Create(ctx, "items", map[string]interface{}{"name": "Alpha"})

		results, err := store.Search(ctx, "items", "name", "Alpha", "exact")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 2 {
			t.Errorf("exact search for 'Alpha': expected 2 results, got %d", len(results))
		}
	})

	t.Run(name+"/Search_contains", func(t *testing.T) {
		store, cleanup := factory(t)
		defer cleanup()
		ctx := context.Background()

		store.Create(ctx, "items", map[string]interface{}{"name": "Alphabet"})
		store.Create(ctx, "items", map[string]interface{}{"name": "Beta"})
		store.Create(ctx, "items", map[string]interface{}{"name": "Gamma"})

		results, err := store.Search(ctx, "items", "name", "pha", "contains")
		if err != nil {
			t.Fatalf("Search: %v", err)
		}
		if len(results) != 1 {
			t.Errorf("contains search for 'pha': expected 1 result, got %d", len(results))
		}
	})
}

// =============================================================================
// Run the contract suite against both backends
// =============================================================================

func TestContract_JSONFile(t *testing.T) {
	runContractSuite(t, "jsonfile", jsonfileFactory)
}

func TestContract_SQLite(t *testing.T) {
	runContractSuite(t, "sqlite", sqliteFactory)
}
