// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// contract_test.go
//
// Backend-agnostic contract suite for the Store interface.
// Any implementation of Store must pass runContractSuite.
// Currently exercised against PebbleStore only; a second backend
// adds a single driver function calling runContractSuite.

import (
	"context"
	"testing"
	"time"
)

// storeFactory creates a fresh Store in dir for contract testing.
type storeFactory func(dir string) (Store, error)

// pebbleFactory is the driver for the current production backend.
func pebbleFactory(dir string) (Store, error) {
	return NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
}

// TestContract_Pebble runs the full contract suite against PebbleStore.
func TestContract_Pebble(t *testing.T) {
	runContractSuite(t, pebbleFactory)
}

// runContractSuite executes all contract behaviours against a Store
// created by factory.
func runContractSuite(t *testing.T, factory storeFactory) {
	t.Helper()

	t.Run("DefineGet_Roundtrip", func(t *testing.T) {
		store := newContractStore(t, factory)
		err := store.DefineTimeline(1, TimelineConfig{
			Dims: 2, Name: "sensors", RetentionDays: 30,
		})
		if err != nil {
			t.Fatalf("DefineTimeline: %v", err)
		}
		cfg, ok := store.Timeline(1)
		if !ok {
			t.Fatal("Timeline(1) not found after Define")
		}
		if cfg.Dims != 2 {
			t.Errorf("Dims: got %d want 2", cfg.Dims)
		}
		if cfg.Name != "sensors" {
			t.Errorf("Name: got %q want %q", cfg.Name, "sensors")
		}
		if cfg.RetentionDays != 30 {
			t.Errorf("RetentionDays: got %d want 30", cfg.RetentionDays)
		}
	})

	t.Run("DimsImmutableAfterFirstWrite", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		ctx := context.Background()
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: time.Unix(1_000_000, 0).UTC(),
		})
		// Attempt to re-define with different dims.
		err := store.DefineTimeline(1, TimelineConfig{Dims: 2})
		if err == nil {
			t.Error("expected error redefining dims after first write, got nil")
		}
	})

	t.Run("AppendQueryRange_FullPrefix", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 2})
		ctx := context.Background()
		ts := time.Unix(1_000_000, 0).UTC()
		payload := []byte("hello")
		store.Append(ctx, Event{
			Timeline: 1,
			Dims:     []uint64{42, 7},
			Time:     ts,
			Nums:     []float64{3.14},
			Payload:  payload,
		})
		events, err := store.QueryRange(ctx, RangeQuery{
			Timeline: 1, Dims: []uint64{42, 7},
			From: ts.Add(-time.Second), To: ts.Add(time.Second),
		})
		if err != nil {
			t.Fatalf("QueryRange: %v", err)
		}
		if len(events) != 1 {
			t.Fatalf("got %d events, want 1", len(events))
		}
		e := events[0]
		if !e.Time.Equal(ts) {
			t.Errorf("Time: got %v want %v", e.Time, ts)
		}
		if len(e.Nums) != 1 || e.Nums[0] != 3.14 {
			t.Errorf("Nums: got %v want [3.14]", e.Nums)
		}
		if string(e.Payload) != "hello" {
			t.Errorf("Payload: got %q want %q", e.Payload, "hello")
		}
	})

	t.Run("PartialPrefix_TimeFilter_Regression", func(t *testing.T) {
		// Regression: partial-prefix scans must not leak events outside From/To.
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 2})
		ctx := context.Background()
		base := time.Unix(1_000_000, 0).UTC()

		// Three events: early, in-window, late.
		for i, ts := range []time.Time{
			base.Add(-2 * time.Hour),
			base,
			base.Add(2 * time.Hour),
		} {
			store.Append(ctx, Event{
				Timeline: 1,
				Dims:     []uint64{1, uint64(i)},
				Time:     ts,
			})
		}

		from := base.Add(-30 * time.Minute)
		to := base.Add(30 * time.Minute)
		events, err := store.QueryRange(ctx, RangeQuery{
			Timeline: 1, Dims: []uint64{1}, // partial prefix
			From: from, To: to,
		})
		if err != nil {
			t.Fatalf("QueryRange: %v", err)
		}
		if len(events) != 1 {
			t.Errorf("partial prefix time filter: got %d events, want 1", len(events))
		}
	})

	t.Run("Purge_RemovesOldEvents", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 1})
		ctx := context.Background()

		old := time.Now().UTC().Add(-72 * time.Hour)
		recent := time.Now().UTC().Add(-1 * time.Hour)

		// 3 old events (should be purged).
		for i := 0; i < 3; i++ {
			store.Append(ctx, Event{
				Timeline: 1, Dims: []uint64{1},
				Time: old.Add(time.Duration(i) * time.Second),
			})
		}
		// 3 recent events (must survive).
		for i := 0; i < 3; i++ {
			store.Append(ctx, Event{
				Timeline: 1, Dims: []uint64{1},
				Time: recent.Add(time.Duration(i) * time.Second),
			})
		}

		if err := store.Purge(ctx); err != nil {
			t.Fatalf("Purge: %v", err)
		}

		oldEvents, _ := store.QueryRange(ctx, RangeQuery{
			Timeline: 1, Dims: []uint64{1},
			From: old.Add(-time.Second), To: old.Add(time.Hour),
		})
		if len(oldEvents) > 0 {
			t.Errorf("old events not purged: %d remain", len(oldEvents))
		}

		recentEvents, _ := store.QueryRange(ctx, RangeQuery{
			Timeline: 1, Dims: []uint64{1},
			From: recent.Add(-time.Second), To: recent.Add(time.Hour),
		})
		if len(recentEvents) != 3 {
			t.Errorf("recent events: got %d, want 3", len(recentEvents))
		}
	})

	t.Run("Purge_RespectsNoExpiry", func(t *testing.T) {
		// RetentionDays=0 and store default=0 means no expiry.
		cfg := testStoreConfig()
		cfg.DefaultRetentionDays = 0
		dir := t.TempDir()
		store2, err := NewPebbleStore(dir, cfg, testPebbleConfig())
		if err != nil {
			t.Fatal(err)
		}
		defer store2.Close()

		store2.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 0})
		ctx := context.Background()
		old := time.Now().UTC().Add(-720 * time.Hour)
		for i := 0; i < 5; i++ {
			store2.Append(ctx, Event{
				Timeline: 1, Dims: []uint64{1},
				Time: old.Add(time.Duration(i) * time.Second),
			})
		}
		if err := store2.Purge(ctx); err != nil {
			t.Fatalf("Purge: %v", err)
		}
		events, _ := store2.QueryRange(ctx, RangeQuery{
			Timeline: 1, Dims: []uint64{1},
			From: old.Add(-time.Second), To: old.Add(time.Hour),
		})
		if len(events) != 5 {
			t.Errorf("no-expiry timeline: got %d events after Purge, want 5", len(events))
		}
	})

	t.Run("AppendBatch_Atomicity", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		ctx := context.Background()
		base := time.Unix(2_000_000, 0).UTC()

		// Batch: two valid events + one invalid (timeline 99 not defined).
		events := []Event{
			{Timeline: 1, Dims: []uint64{1}, Time: base},
			{Timeline: 1, Dims: []uint64{1}, Time: base.Add(time.Second)},
			{Timeline: 99, Dims: []uint64{1}, Time: base.Add(2 * time.Second)},
		}
		n, err := store.AppendBatch(ctx, events, 0)
		if err == nil {
			t.Errorf("expected error from invalid event, got nil (accepted %d)", n)
		}

		// Verify nothing was written.
		result, _ := store.QueryRange(ctx, RangeQuery{
			Timeline: 1, Dims: []uint64{1},
			From: base.Add(-time.Second), To: base.Add(time.Minute),
		})
		if len(result) != 0 {
			t.Errorf("batch atomicity violated: %d events written despite error", len(result))
		}
	})

	t.Run("Latest_ReverseChronological", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		ctx := context.Background()
		base := time.Unix(3_000_000, 0).UTC()
		for i := 0; i < 5; i++ {
			store.Append(ctx, Event{
				Timeline: 1, Dims: []uint64{1},
				Time: base.Add(time.Duration(i) * time.Second),
			})
		}
		events, err := store.Latest(ctx, LatestQuery{Timeline: 1, Dims: []uint64{1}, N: 3})
		if err != nil {
			t.Fatalf("Latest: %v", err)
		}
		if len(events) != 3 {
			t.Fatalf("got %d events, want 3", len(events))
		}
		// Latest returns newest first.
		if !events[0].Time.After(events[1].Time) {
			t.Errorf("events[0] (%v) should be after events[1] (%v)", events[0].Time, events[1].Time)
		}
		// The 3 newest of 5: indices 4, 3, 2.
		want := base.Add(4 * time.Second)
		if !events[0].Time.Equal(want) {
			t.Errorf("newest event: got %v want %v", events[0].Time, want)
		}
	})

	t.Run("Aggregate_Count", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		ctx := context.Background()
		base := time.Unix(4_000_000, 0).UTC()
		for i := 0; i < 4; i++ {
			store.Append(ctx, Event{
				Timeline: 1, Dims: []uint64{1},
				Time: base.Add(time.Duration(i) * time.Second),
				Nums: []float64{float64(i)},
			})
		}
		buckets, err := store.Aggregate(ctx, AggregateQuery{
			Timeline: 1, Dims: []uint64{1},
			From: base.Add(-time.Second), To: base.Add(time.Minute),
			Function: "count", NumField: 0,
		})
		if err != nil {
			t.Fatalf("Aggregate: %v", err)
		}
		if len(buckets) != 1 {
			t.Fatalf("got %d buckets, want 1", len(buckets))
		}
		if buckets[0].Count != 4 {
			t.Errorf("count: got %d want 4", buckets[0].Count)
		}
	})

	t.Run("DefaultRetentionDays_Persists", func(t *testing.T) {
		dir := t.TempDir()
		s1, err := factory(dir)
		if err != nil {
			t.Fatal(err)
		}
		if err := s1.SetDefaultRetentionDays(42); err != nil {
			t.Fatalf("SetDefaultRetentionDays: %v", err)
		}
		s1.Close()

		s2, err := factory(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer s2.Close()
		if s2.DefaultRetentionDays() != 42 {
			t.Errorf("DefaultRetentionDays after reopen: got %d want 42", s2.DefaultRetentionDays())
		}
	})

	t.Run("TimelineStats_TotalEventsApproximate", func(t *testing.T) {
		store := newContractStore(t, factory)
		store.DefineTimeline(1, TimelineConfig{Dims: 1})
		ctx := context.Background()
		ts := time.Unix(5_000_000, 0).UTC()
		store.Append(ctx, Event{Timeline: 1, Dims: []uint64{1}, Time: ts})

		stats, err := store.TimelineStats(ctx, 1)
		if err != nil {
			t.Fatalf("TimelineStats: %v", err)
		}
		// TotalEventsApproximate must always be true per current spec.
		if !stats.TotalEventsApproximate {
			t.Error("TotalEventsApproximate should be true")
		}
		if stats.TotalEvents < 1 {
			t.Errorf("TotalEvents: got %d want >= 1", stats.TotalEvents)
		}
	})
}

// newContractStore creates a fresh PebbleStore in a temp dir via factory.
func newContractStore(t *testing.T, factory storeFactory) Store {
	t.Helper()
	dir := t.TempDir()
	store, err := factory(dir)
	if err != nil {
		t.Fatalf("factory: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}
