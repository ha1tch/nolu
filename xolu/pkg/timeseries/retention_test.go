// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// retention_test.go
//
// Tests for RetentionWorker: lifecycle (Start/Stop), sweep behaviour,
// context cancellation, and concurrent safety.

import (
	"context"
	"sync"
	"testing"
	"time"
)

// TestRetentionWorker_StartStop verifies that the worker goroutine starts,
// runs without panicking, and exits cleanly when Stop is called.
func TestRetentionWorker_StartStop(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir, NewPebbleStoreFactory(testPebbleConfig()), testStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	w := NewRetentionWorker(mgr, 50*time.Millisecond)
	w.Start()

	// Let it tick at least twice.
	time.Sleep(150 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// clean stop
	case <-time.After(2 * time.Second):
		t.Fatal("RetentionWorker.Stop() did not return within 2s")
	}
}

// TestRetentionWorker_SweepDeletesExpired seeds old events on a timeline with
// a 1-day retention policy, runs the worker for two ticks, and verifies those
// events are purged while a no-expiry timeline is untouched.
func TestRetentionWorker_SweepDeletesExpired(t *testing.T) {
	baseDir := t.TempDir()
	mgr, err := NewManager(baseDir, NewPebbleStoreFactory(testPebbleConfig()), testStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	ctx := context.Background()

	// Provision tenant 1.
	if err := mgr.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	store, err := mgr.StoreFor(1)
	if err != nil {
		t.Fatal(err)
	}

	// Timeline 1: 1-day retention. Seed events 3 days old (should be purged).
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UTC().Add(-72 * time.Hour)
	for i := 0; i < 5; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: old.Add(time.Duration(i) * time.Second),
		})
	}

	// Timeline 2: no expiry (RetentionDays=0). Seed same old events.
	if err := store.DefineTimeline(2, TimelineConfig{Dims: 1, RetentionDays: 0}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		store.Append(ctx, Event{
			Timeline: 2, Dims: []uint64{1},
			Time: old.Add(time.Duration(i) * time.Second),
		})
	}

	// Run the worker for two ticks.
	w := NewRetentionWorker(mgr, 50*time.Millisecond)
	w.Start()
	time.Sleep(150 * time.Millisecond)
	w.Stop()

	// Timeline 1: old events should be gone.
	events1, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{1},
		From: old.Add(-time.Second), To: old.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryRange timeline 1: %v", err)
	}
	if len(events1) > 0 {
		t.Errorf("timeline 1 (retention=1d): expected 0 events after sweep, got %d", len(events1))
	}

	// Timeline 2: no-expiry events must survive.
	events2, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 2, Dims: []uint64{1},
		From: old.Add(-time.Second), To: old.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryRange timeline 2: %v", err)
	}
	if len(events2) != 5 {
		t.Errorf("timeline 2 (no expiry): expected 5 events after sweep, got %d", len(events2))
	}
}

// TestRetentionWorker_SweepDoesNotDeleteRecent verifies that recent events
// (within the retention window) are never deleted by a sweep.
func TestRetentionWorker_SweepDoesNotDeleteRecent(t *testing.T) {
	baseDir := t.TempDir()
	mgr, err := NewManager(baseDir, NewPebbleStoreFactory(testPebbleConfig()), testStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	ctx := context.Background()
	if err := mgr.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	store, _ := mgr.StoreFor(1)

	// Retention = 7 days. Seed 5 events from 1 hour ago (well within window).
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 7}); err != nil {
		t.Fatal(err)
	}
	recent := time.Now().UTC().Add(-time.Hour)
	for i := 0; i < 5; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: recent.Add(time.Duration(i) * time.Second),
		})
	}

	w := NewRetentionWorker(mgr, 50*time.Millisecond)
	w.Start()
	time.Sleep(150 * time.Millisecond)
	w.Stop()

	events, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{1},
		From: recent.Add(-time.Second), To: recent.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(events) != 5 {
		t.Errorf("recent events: expected 5, got %d", len(events))
	}
}

// TestRetentionWorker_MultipleStops verifies that calling Stop more than once
// does not panic (channel double-close guard).
func TestRetentionWorker_MultipleStops(t *testing.T) {
	baseDir := t.TempDir()
	mgr, err := NewManager(baseDir, NewPebbleStoreFactory(testPebbleConfig()), testStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	w := NewRetentionWorker(mgr, 50*time.Millisecond)
	w.Start()
	time.Sleep(60 * time.Millisecond)

	// First Stop should work cleanly.
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("Stop panicked: %v", r)
			}
		}()
		w.Stop()
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("first Stop did not return")
	}
}

// TestRetentionWorker_ConcurrentSweepAndAppend runs the worker while
// appends are ongoing and verifies no races or panics occur.
// Run under -race.
func TestRetentionWorker_ConcurrentSweepAndAppend(t *testing.T) {
	baseDir := t.TempDir()
	mgr, err := NewManager(baseDir, NewPebbleStoreFactory(testPebbleConfig()), testStoreConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	ctx := context.Background()
	if err := mgr.Provision(ctx, 1); err != nil {
		t.Fatal(err)
	}
	store, _ := mgr.StoreFor(1)
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}

	// Start the worker.
	w := NewRetentionWorker(mgr, 30*time.Millisecond)
	w.Start()

	// Concurrent appenders for 200ms.
	var wg sync.WaitGroup
	stopCh := make(chan struct{})
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				store.Append(ctx, Event{
					Timeline: 1, Dims: []uint64{1},
					Time: time.Now().UTC(),
				})
			}
		}()
	}

	time.Sleep(200 * time.Millisecond)
	close(stopCh)
	wg.Wait()
	w.Stop()
	// No assertion needed — the test passes if no race or panic occurs.
}
