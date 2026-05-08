// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// parseTenantDirName
// ---------------------------------------------------------------------------

func TestParseTenantDirName_Valid(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		wantID   uint16
		wantOK   bool
	}{
		{"tenant 1",    "t0001", 1,      true},
		{"tenant 255",  "t00ff", 255,    true},
		{"tenant 0",    "t0000", 0,      true},
		{"max uint16",  "tffff", 65535,  true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			id, ok := parseTenantDirName(c.input)
			if ok != c.wantOK {
				t.Fatalf("ok: got %v, want %v", ok, c.wantOK)
			}
			if ok && id != c.wantID {
				t.Errorf("id: got %d, want %d", id, c.wantID)
			}
		})
	}
}

func TestParseTenantDirName_Invalid(t *testing.T) {
	cases := []string{
		"",           // empty
		"t",          // too short
		"x0001",      // wrong prefix
		"0001",       // no prefix
		"txyz",       // non-hex after t
		"data",       // random dirname
	}
	for _, s := range cases {
		t.Run(s, func(t *testing.T) {
			_, ok := parseTenantDirName(s)
			if ok {
				t.Errorf("expected false for %q, got true", s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DefaultManager — IsProvisioned, StoreFor lazy open
// ---------------------------------------------------------------------------

func newTestManager(t *testing.T) (*DefaultManager, string) {
	t.Helper()
	dir := t.TempDir()
	m, err := NewManager(dir, NewPebbleStoreFactory(testPebbleConfig()), StoreConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	t.Cleanup(func() { _ = m.Close() })
	return m, dir
}

func TestIsProvisioned_FalseBeforeProvision(t *testing.T) {
	m, _ := newTestManager(t)
	if m.IsProvisioned(99) {
		t.Error("expected false for unprovisioned tenant")
	}
}

func TestIsProvisioned_TrueAfterProvision(t *testing.T) {
	m, _ := newTestManager(t)
	if err := m.Provision(context.Background(), 1); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	if !m.IsProvisioned(1) {
		t.Error("expected true after provision")
	}
}

func TestStoreFor_ErrorWhenNotProvisioned(t *testing.T) {
	m, _ := newTestManager(t)
	_, err := m.StoreFor(42)
	if err == nil {
		t.Fatal("expected error for unprovisioned tenant")
	}
}

func TestStoreFor_LazyOpen(t *testing.T) {
	dir := t.TempDir()

	// First manager: provision tenant 7 and write a timeline.
	m1, err := NewManager(dir, NewPebbleStoreFactory(testPebbleConfig()), StoreConfig{})
	if err != nil {
		t.Fatalf("NewManager m1: %v", err)
	}
	ctx := context.Background()
	if err := m1.Provision(ctx, 7); err != nil {
		t.Fatalf("Provision: %v", err)
	}
	s1, err := m1.StoreFor(7)
	if err != nil {
		t.Fatalf("StoreFor m1: %v", err)
	}
	if err := s1.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 30}); err != nil {
		t.Fatalf("DefineTimeline: %v", err)
	}
	_ = m1.Close()

	// Second manager: scan discovers t0007, lazy-opens on first StoreFor.
	m2, err := NewManager(dir, NewPebbleStoreFactory(testPebbleConfig()), StoreConfig{})
	if err != nil {
		t.Fatalf("NewManager m2: %v", err)
	}
	defer m2.Close()

	if !m2.IsProvisioned(7) {
		t.Fatal("m2 should see tenant 7 from dir scan")
	}

	s2, err := m2.StoreFor(7)
	if err != nil {
		t.Fatalf("StoreFor lazy open: %v", err)
	}
	if s2 == nil {
		t.Fatal("expected non-nil store")
	}

	// Second call returns the cached store (no double-open).
	s3, err := m2.StoreFor(7)
	if err != nil {
		t.Fatalf("StoreFor second call: %v", err)
	}
	if s2 != s3 {
		t.Error("expected same store instance on second call")
	}
}

// ---------------------------------------------------------------------------
// PebbleStore — UpdateTimeline and Stats
// ---------------------------------------------------------------------------

func newTestStore(t *testing.T) Store {
	t.Helper()
	dir := t.TempDir()
	s, err := NewPebbleStoreFactory(testPebbleConfig())(dir, StoreConfig{})
	if err != nil {
		t.Fatalf("NewPebbleStore: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestUpdateTimeline_UpdatesNameAndRetention(t *testing.T) {
	s := newTestStore(t)
	id := TimelineID(1)

	if err := s.DefineTimeline(id, TimelineConfig{Name: "original", Dims: 1, RetentionDays: 7}); err != nil {
		t.Fatalf("DefineTimeline: %v", err)
	}

	if err := s.UpdateTimeline(id, TimelineConfig{Name: "updated", RetentionDays: 30}); err != nil {
		t.Fatalf("UpdateTimeline: %v", err)
	}

	cfg, ok := s.Timeline(id)
	if !ok {
		t.Fatal("Timeline not found after update")
	}
	if cfg.Name != "updated" {
		t.Errorf("Name: got %q, want %q", cfg.Name, "updated")
	}
	if cfg.RetentionDays != 30 {
		t.Errorf("RetentionDays: got %d, want 30", cfg.RetentionDays)
	}
}

func TestUpdateTimeline_ErrorOnMissing(t *testing.T) {
	s := newTestStore(t)
	err := s.UpdateTimeline(TimelineID(999), TimelineConfig{Name: "ghost"})
	if err == nil {
		t.Fatal("expected error updating non-existent timeline")
	}
}

func TestStats_EmptyStore(t *testing.T) {
	s := newTestStore(t)
	stats, err := s.Stats(context.Background())
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats == nil {
		t.Fatal("expected non-nil stats")
	}
	if stats.Timelines != 0 {
		t.Errorf("Timelines: got %d, want 0", stats.Timelines)
	}
	if stats.DiskBytes < 0 {
		t.Errorf("DiskBytes should be >= 0, got %d", stats.DiskBytes)
	}
}

func TestStats_WithTimelines(t *testing.T) {
	s := newTestStore(t)
	ctx := context.Background()
	now := time.Now()

	for _, id := range []TimelineID{1, 2, 3} {
		if err := s.DefineTimeline(id, TimelineConfig{Dims: 1, RetentionDays: 30}); err != nil {
			t.Fatalf("DefineTimeline %d: %v", id, err)
		}
		_ = s.Append(ctx, Event{Timeline: id, Dims: []uint64{0}, Time: now, Nums: []float64{1}})
	}

	stats, err := s.Stats(ctx)
	if err != nil {
		t.Fatalf("Stats: %v", err)
	}
	if stats.Timelines != 3 {
		t.Errorf("Timelines: got %d, want 3", stats.Timelines)
	}
}

// ---------------------------------------------------------------------------
// NewManager — scan existing dirs
// ---------------------------------------------------------------------------

func TestNewManager_ScansExistingTenantDirs(t *testing.T) {
	dir := t.TempDir()

	// Create valid tenant dirs manually.
	for _, sub := range []string{"t0001", "t0002", "notenant", "randomdir"} {
		_ = os.MkdirAll(filepath.Join(dir, sub), 0755)
	}

	m, err := NewManager(dir, NewPebbleStoreFactory(testPebbleConfig()), StoreConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	if !m.IsProvisioned(1) {
		t.Error("expected tenant 1 to be known from scan")
	}
	if !m.IsProvisioned(2) {
		t.Error("expected tenant 2 to be known from scan")
	}
	// "notenant" and "t00gg" should not parse as valid tenant dirs;
	// those would only ever resolve to tenant ID 0 if they happened to
	// parse at all, and tenant 0 was not created here.
	if m.IsProvisioned(0) {
		t.Error("non-hex dir should not cause tenant 0 to appear provisioned")
	}
}

func TestNewManager_IgnoresFiles(t *testing.T) {
	dir := t.TempDir()

	// Create a file (not a dir) that looks like a tenant dir.
	f, err := os.Create(filepath.Join(dir, "t0003"))
	if err != nil {
		t.Fatalf("create file: %v", err)
	}
	f.Close()

	m, err := NewManager(dir, NewPebbleStoreFactory(testPebbleConfig()), StoreConfig{})
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	defer m.Close()

	if m.IsProvisioned(3) {
		t.Error("file t0003 should not be provisioned (not a directory)")
	}
}
