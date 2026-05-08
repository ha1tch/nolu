// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"os"
	"testing"
)

// ---------------------------------------------------------------------------
// Factory: ListStores, NewStore
// ---------------------------------------------------------------------------

func TestListStores(t *testing.T) {
	stores := ListStores()
	if len(stores) < 2 {
		t.Errorf("Expected at least 2 registered stores, got %d", len(stores))
	}

	found := map[string]bool{}
	for _, s := range stores {
		found[s] = true
	}
	if !found["sqlite"] {
		t.Error("Expected 'sqlite' in registered stores")
	}
	if !found["jsonfile"] {
		t.Error("Expected 'jsonfile' in registered stores")
	}
}

func TestNewStore_Unknown(t *testing.T) {
	_, err := NewStore("nonexistent", nil)
	if err == nil {
		t.Error("Expected error for unknown store type")
	}
}

func TestNewStore_JSONFileDefaults(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "storage-cov")
	defer os.RemoveAll(tmpDir)

	// Empty config — should use "data" and "default" defaults
	store, err := NewStore("jsonfile", map[string]interface{}{
		"base_dir": tmpDir,
	})
	if err != nil {
		t.Fatalf("NewStore(jsonfile) with defaults failed: %v", err)
	}
	store.Close()
}

func TestNewStoreFromConfig_UnknownType(t *testing.T) {
	_, err := NewStoreFromConfig(StoreConfig{Type: "badtype"})
	if err == nil {
		t.Error("Expected error for unknown store type in NewStoreFromConfig")
	}
}

// ---------------------------------------------------------------------------
// AdaptiveLock accessors
// ---------------------------------------------------------------------------

func TestAdaptiveLock_Accessors(t *testing.T) {
	al := NewAdaptiveLock(95)

	if al.Threshold() != 95 {
		t.Errorf("Threshold() = %d, want 95", al.Threshold())
	}

	// Initially should not be engaged (95 < 100)
	if al.Engaged() {
		t.Error("Expected lock to not be engaged initially at 95%")
	}

	// SetThreshold
	al.SetThreshold(50)
	if al.Threshold() != 50 {
		t.Errorf("After SetThreshold(50), Threshold() = %d", al.Threshold())
	}

	// Edge: 100 = always engaged
	al.SetThreshold(100)
	if !al.Engaged() {
		t.Error("Expected lock to be engaged at 100%")
	}

	// Edge: 0 = never engaged
	al.SetThreshold(0)
	if al.Engaged() {
		t.Error("Expected lock to not be engaged at 0%")
	}

	// Clamping: negative
	al.SetThreshold(-10)
	if al.Threshold() != 0 {
		t.Errorf("Negative threshold should clamp to 0, got %d", al.Threshold())
	}

	// Clamping: over 100
	al.SetThreshold(200)
	if al.Threshold() != 100 {
		t.Errorf("Over-100 threshold should clamp to 100, got %d", al.Threshold())
	}
}

func TestAdaptiveLock_EdgeCases(t *testing.T) {
	// threshold = 0: disabled
	al := NewAdaptiveLock(0)
	if al.Engaged() {
		t.Error("Lock with threshold 0 should never engage")
	}

	// threshold = 100: always engaged
	al = NewAdaptiveLock(100)
	if !al.Engaged() {
		t.Error("Lock with threshold 100 should always be engaged")
	}
}

// ---------------------------------------------------------------------------
// SQLite DB() and ContentionLock() accessors
// ---------------------------------------------------------------------------

func TestSQLiteStore_Accessors(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "storage-acc")
	defer os.RemoveAll(tmpDir)

	store, err := NewSQLiteStore(tmpDir+"/acc.db", SQLiteConfig{
		DBPath:    tmpDir + "/acc.db",
		EnableWAL: true,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore failed: %v", err)
	}
	defer store.Close()

	if store.DB() == nil {
		t.Error("DB() should return non-nil database")
	}

	if store.ContentionLock() == nil {
		t.Error("ContentionLock() should return non-nil lock")
	}
}

// ---------------------------------------------------------------------------
// JSONFile: ListEntities
// ---------------------------------------------------------------------------

func TestJSONFileStore_ListEntities(t *testing.T) {
	tmpDir, _ := os.MkdirTemp("", "storage-le")
	defer os.RemoveAll(tmpDir)

	store, err := NewJSONFileStore(tmpDir, "test")
	if err != nil {
		t.Fatalf("NewJSONFileStore failed: %v", err)
	}
	defer store.Close()

	ctx := context.Background()

	// Create entities of different types
	store.Create(ctx, "users", map[string]interface{}{"name": "alice"})
	store.Create(ctx, "items", map[string]interface{}{"name": "widget"})

	entities, err := store.ListEntities(ctx)
	if err != nil {
		t.Fatalf("ListEntities failed: %v", err)
	}

	if len(entities) < 2 {
		t.Errorf("Expected at least 2 entity types, got %d: %v", len(entities), entities)
	}
}
