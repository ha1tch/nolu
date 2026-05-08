// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// registry_persist_test.go
//
// Tests for registry.json persistence: close/reopen cycles, immutability
// enforcement across sessions, atomic-write correctness, and default
// retention durability.

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestRegistryPersist_SurvivesCloseReopen verifies that timeline definitions
// written in one session are readable after a close and reopen.
func TestRegistryPersist_SurvivesCloseReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Session 1: define three timelines, write one event to lock dims on #1.
	func() {
		store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
		if err != nil {
			t.Fatalf("open session 1: %v", err)
		}
		defer store.Close()

		timelines := []struct {
			id   TimelineID
			dims uint8
			name string
			ret  int
		}{
			{1, 2, "temperature", 30},
			{2, 1, "pressure", 7},
			{3, 3, "vibration", 0},
		}
		for _, tl := range timelines {
			if err := store.DefineTimeline(tl.id, TimelineConfig{
				Name:          tl.name,
				Dims:          tl.dims,
				RetentionDays: tl.ret,
			}); err != nil {
				t.Fatalf("define %d: %v", tl.id, err)
			}
		}

		// Write one event to timeline 1 to lock its dims.
		if err := store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{10, 20},
			Time: time.Unix(1_000_000, 0).UTC(),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}()

	// Session 2: reopen and verify everything survived.
	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("open session 2: %v", err)
	}
	defer store.Close()

	ids := store.Timelines()
	if len(ids) != 3 {
		t.Fatalf("session 2: got %d timelines, want 3", len(ids))
	}

	cases := []struct {
		id   TimelineID
		dims uint8
		name string
		ret  int
	}{
		{1, 2, "temperature", 30},
		{2, 1, "pressure", 7},
		{3, 3, "vibration", 0},
	}
	for _, want := range cases {
		cfg, ok := store.Timeline(want.id)
		if !ok {
			t.Errorf("timeline %d not found after reopen", want.id)
			continue
		}
		if cfg.Dims != want.dims {
			t.Errorf("timeline %d: Dims %d, want %d", want.id, cfg.Dims, want.dims)
		}
		if cfg.Name != want.name {
			t.Errorf("timeline %d: Name %q, want %q", want.id, cfg.Name, want.name)
		}
		if cfg.RetentionDays != want.ret {
			t.Errorf("timeline %d: RetentionDays %d, want %d", want.id, cfg.RetentionDays, want.ret)
		}
	}
}

// TestRegistryPersist_FirstWriteAtPersists verifies that FirstWriteAt is
// persisted and survives a close/reopen.
func TestRegistryPersist_FirstWriteAtPersists(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	writeTime := time.Unix(5_000_000, 0).UTC()

	func() {
		store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer store.Close()
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1}, Time: writeTime,
		})
	}()

	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	cfg, ok := store.Timeline(1)
	if !ok {
		t.Fatal("timeline 1 not found after reopen")
	}
	if cfg.FirstWriteAt.IsZero() {
		t.Error("FirstWriteAt is zero after reopen; should be persisted")
	}
}

// TestRegistryPersist_DimsImmutableAcrossSessions verifies that attempting
// to redefine a timeline with different Dims after it has received its first
// write is rejected even when done in a new process session.
func TestRegistryPersist_DimsImmutableAcrossSessions(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()

	// Session 1: define dims=2, write one event.
	func() {
		store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer store.Close()
		store.DefineTimeline(1, TimelineConfig{Dims: 2})
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1, 2}, Time: time.Unix(1_000_000, 0).UTC(),
		})
	}()

	// Session 2: attempt redefine with dims=3 — must fail.
	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	err = store.DefineTimeline(1, TimelineConfig{Dims: 3})
	if err == nil {
		t.Error("expected error when changing dims across sessions, got nil")
	}
}

// TestRegistryPersist_DefaultRetentionPersists verifies that the store-level
// default retention survives a close/reopen.
func TestRegistryPersist_DefaultRetentionPersists(t *testing.T) {
	dir := t.TempDir()

	func() {
		store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer store.Close()
		if err := store.SetDefaultRetentionDays(180); err != nil {
			t.Fatalf("SetDefaultRetentionDays: %v", err)
		}
	}()

	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	if got := store.DefaultRetentionDays(); got != 180 {
		t.Errorf("after reopen: DefaultRetentionDays = %d, want 180", got)
	}
}

// TestRegistryPersist_NoTmpFileAfterSave verifies that the registry write
// uses atomic rename (no .tmp file left behind after a successful save).
func TestRegistryPersist_NoTmpFileAfterSave(t *testing.T) {
	dir := t.TempDir()

	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1, Name: "test"}); err != nil {
		t.Fatalf("define: %v", err)
	}
	store.Close()

	tmpPath := filepath.Join(dir, "registry.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf("tmp file %s should not exist after close, but found it", tmpPath)
	}
	registryPath := filepath.Join(dir, "registry.json")
	if _, err := os.Stat(registryPath); err != nil {
		t.Errorf("registry.json should exist after define+close: %v", err)
	}
}

// TestRegistryPersist_EventDataSurvivesReopen verifies that events written
// in one session are queryable after a close/reopen (registry + Pebble data).
func TestRegistryPersist_EventDataSurvivesReopen(t *testing.T) {
	dir := t.TempDir()
	ctx := context.Background()
	ts := time.Unix(2_000_000, 0).UTC()

	func() {
		store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		defer store.Close()
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{42}, Time: ts, Nums: []float64{99.5},
		})
	}()

	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	defer store.Close()

	events, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{42},
		From: ts.Add(-time.Second), To: ts.Add(time.Second),
	})
	if err != nil {
		t.Fatalf("QueryRange after reopen: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("got %d events after reopen, want 1", len(events))
	}
	if events[0].Nums[0] != 99.5 {
		t.Errorf("num0 = %v, want 99.5", events[0].Nums[0])
	}
}
