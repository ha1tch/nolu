// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

// commit_test.go
//
// Storage-layer contract tests for the Commit operation. Runs against both
// the SQLite and jsonfile backends via the shared storeFactory type defined
// in contract_test.go.

import (
	"context"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

func TestCommit_CreateAndAppend(t *testing.T) {
	runCommitTest(t, sqliteFactory)
}

func runCommitTest(t *testing.T, factory storeFactory) {
	t.Helper()
	store, cleanup := factory(t)
	defer cleanup()

	ctx := context.Background()
	version := 0 // new entity; no version check on create
	req := storage.CommitRequest{
		Update: storage.CommitUpdate{
			Entity:  "asset",
			ID:      1001,
			Version: nil, // unconditional create
			Data:    map[string]interface{}{"state": "on-shelf"},
		},
		Append: []storage.CommitAppend{
			{
				Entity: "log",
				Data:   map[string]interface{}{"asset_id": 1001, "to_state": "on-shelf"},
			},
		},
	}
	_ = version

	result, err := store.Commit(ctx, req)
	if err != nil {
		t.Fatalf("Commit: unexpected error: %v", err)
	}
	if !result.Update.Created {
		t.Errorf("expected Created=true, got false")
	}
	if result.Update.Version != 1 {
		t.Errorf("expected Version=1, got %d", result.Update.Version)
	}
	if len(result.Appended) != 1 {
		t.Fatalf("expected 1 appended, got %d", len(result.Appended))
	}
	if result.Appended[0].Entity != "log" {
		t.Errorf("expected appended entity 'log', got %q", result.Appended[0].Entity)
	}
	if result.Appended[0].ID <= 0 {
		t.Errorf("expected positive appended ID, got %d", result.Appended[0].ID)
	}

	// Verify the asset landed in the store.
	asset, err := store.Get(ctx, "asset", 1001)
	if err != nil {
		t.Fatalf("Get asset: %v", err)
	}
	if asset["state"] != "on-shelf" {
		t.Errorf("asset state: want 'on-shelf', got %v", asset["state"])
	}
}

func TestCommit_UpsertOverwrite(t *testing.T) {
	runCommitOverwriteTest(t, sqliteFactory)
}

func runCommitOverwriteTest(t *testing.T, factory storeFactory) {
	t.Helper()
	store, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	// First commit: create.
	req1 := storage.CommitRequest{
		Update: storage.CommitUpdate{Entity: "asset", ID: 42, Data: map[string]interface{}{"state": "idle"}},
		Append: []storage.CommitAppend{
			{Entity: "log", Data: map[string]interface{}{"ev": "created"}},
		},
	}
	r1, err := store.Commit(ctx, req1)
	if err != nil {
		t.Fatalf("first commit: %v", err)
	}
	if !r1.Update.Created {
		t.Errorf("first commit: expected Created=true")
	}

	// Second commit: overwrite without CAS.
	req2 := storage.CommitRequest{
		Update: storage.CommitUpdate{Entity: "asset", ID: 42, Data: map[string]interface{}{"state": "active"}},
		Append: []storage.CommitAppend{
			{Entity: "log", Data: map[string]interface{}{"ev": "activated"}},
		},
	}
	r2, err := store.Commit(ctx, req2)
	if err != nil {
		t.Fatalf("second commit: %v", err)
	}
	if r2.Update.Created {
		t.Errorf("second commit: expected Created=false")
	}
	if r2.Update.Version != 2 {
		t.Errorf("second commit: expected Version=2, got %d", r2.Update.Version)
	}

	// Verify final state.
	asset, _ := store.Get(ctx, "asset", 42)
	if asset["state"] != "active" {
		t.Errorf("expected state 'active', got %v", asset["state"])
	}
}

func TestCommit_CAS_Success(t *testing.T) {
	runCASSuccessTest(t, sqliteFactory)
}

func runCASSuccessTest(t *testing.T, factory storeFactory) {
	t.Helper()
	store, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	// Create via plain Save.
	if _, err := store.Save(ctx, "asset", 7, map[string]interface{}{"state": "A"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, _ := store.Get(ctx, "asset", 7)
	v := versionOf(t, got)

	one := v
	req := storage.CommitRequest{
		Update: storage.CommitUpdate{
			Entity:  "asset",
			ID:      7,
			Version: &one,
			Data:    map[string]interface{}{"state": "B"},
		},
		Append: []storage.CommitAppend{
			{Entity: "log", Data: map[string]interface{}{"transition": "A->B"}},
		},
	}
	result, err := store.Commit(ctx, req)
	if err != nil {
		t.Fatalf("CAS commit: unexpected error: %v", err)
	}
	if result.Update.Version != v+1 {
		t.Errorf("expected version %d, got %d", v+1, result.Update.Version)
	}
}

func TestCommit_CAS_Conflict(t *testing.T) {
	runCASConflictTest(t, sqliteFactory)
}

func runCASConflictTest(t *testing.T, factory storeFactory) {
	t.Helper()
	store, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	if _, err := store.Save(ctx, "asset", 9, map[string]interface{}{"state": "A"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	// Advance the version so our CAS will use a stale value.
	if _, err := store.Save(ctx, "asset", 9, map[string]interface{}{"state": "A2"}); err != nil {
		t.Fatalf("second Save: %v", err)
	}

	stale := 1 // original version, now stale
	req := storage.CommitRequest{
		Update: storage.CommitUpdate{
			Entity:  "asset",
			ID:      9,
			Version: &stale,
			Data:    map[string]interface{}{"state": "B"},
		},
		Append: []storage.CommitAppend{
			{Entity: "log", Data: map[string]interface{}{"ev": "should-not-land"}},
		},
	}
	_, err := store.Commit(ctx, req)
	if err == nil {
		t.Fatal("expected ErrConflict, got nil")
	}
	if err != storage.ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}

	// Verify nothing was written: state should still be A2.
	asset, _ := store.Get(ctx, "asset", 9)
	if asset["state"] != "A2" {
		t.Errorf("state should be unchanged 'A2', got %v", asset["state"])
	}
}

func TestCommit_AppendDuplicateIDRejected(t *testing.T) {
	runAppendDuplicateTest(t, sqliteFactory)
}

func runAppendDuplicateTest(t *testing.T, factory storeFactory) {
	t.Helper()
	store, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	// Pre-create a log entry with ID 55.
	if _, err := store.Save(ctx, "log", 55, map[string]interface{}{"ev": "existing"}); err != nil {
		t.Fatalf("pre-create log: %v", err)
	}

	dupID := 55
	req := storage.CommitRequest{
		Update: storage.CommitUpdate{Entity: "asset", ID: 1, Data: map[string]interface{}{"state": "X"}},
		Append: []storage.CommitAppend{
			{Entity: "log", ID: &dupID, Data: map[string]interface{}{"ev": "duplicate"}},
		},
	}
	_, err := store.Commit(ctx, req)
	if err == nil {
		t.Fatal("expected ErrAlreadyExists, got nil")
	}
	if err != storage.ErrAlreadyExists {
		t.Errorf("expected ErrAlreadyExists, got %v", err)
	}

	// The asset update must have been rolled back too.
	if store.Exists(ctx, "asset", 1) {
		t.Error("asset 1 should not exist after rolled-back commit")
	}
}

func TestCommit_MultipleAppends(t *testing.T) {
	runMultiAppendTest(t, sqliteFactory)
}

func runMultiAppendTest(t *testing.T, factory storeFactory) {
	t.Helper()
	store, cleanup := factory(t)
	defer cleanup()
	ctx := context.Background()

	req := storage.CommitRequest{
		Update: storage.CommitUpdate{Entity: "asset", ID: 100, Data: map[string]interface{}{"state": "ok"}},
		Append: []storage.CommitAppend{
			{Entity: "log", Data: map[string]interface{}{"n": 1}},
			{Entity: "log", Data: map[string]interface{}{"n": 2}},
			{Entity: "audit", Data: map[string]interface{}{"action": "create"}},
		},
	}
	result, err := store.Commit(ctx, req)
	if err != nil {
		t.Fatalf("multi-append commit: %v", err)
	}
	if len(result.Appended) != 3 {
		t.Errorf("expected 3 appended, got %d", len(result.Appended))
	}
	// All IDs should be positive and distinct.
	seen := map[string]map[int]bool{}
	for _, a := range result.Appended {
		if _, ok := seen[a.Entity]; !ok {
			seen[a.Entity] = map[int]bool{}
		}
		if seen[a.Entity][a.ID] {
			t.Errorf("duplicate ID %d in entity %s", a.ID, a.Entity)
		}
		seen[a.Entity][a.ID] = true
	}
}

// versionOf extracts _version from a Get result and fails the test if absent.
func versionOf(t *testing.T, data map[string]interface{}) int {
	t.Helper()
	v, ok := data["_version"]
	if !ok {
		t.Fatal("_version field missing from Get result")
	}
	switch tv := v.(type) {
	case float64:
		return int(tv)
	case int:
		return tv
	}
	t.Fatalf("_version has unexpected type %T", v)
	return 0
}

// TestCommit_JSONFileReturnsErrNotSupported verifies that the jsonfile backend
// returns ErrNotSupported from Commit, which is what the HTTP handler maps to
// 501 Not Implemented (OLU-CM009). This is the canonical contract test for
// the stub: the method must exist (interface compliance) and must return the
// correct sentinel.
func TestCommit_JSONFileReturnsErrNotSupported(t *testing.T) {
	store, cleanup := jsonfileFactory(t)
	defer cleanup()

	req := storage.CommitRequest{
		Update: storage.CommitUpdate{
			Entity: "asset",
			ID:     1,
			Data:   map[string]interface{}{"state": "x"},
		},
		Append: []storage.CommitAppend{
			{Entity: "log", Data: map[string]interface{}{"ev": "test"}},
		},
	}

	_, err := store.Commit(context.Background(), req)
	if err == nil {
		t.Fatal("expected ErrNotSupported, got nil")
	}
	if err != storage.ErrNotSupported {
		t.Errorf("expected ErrNotSupported, got %v", err)
	}
}
