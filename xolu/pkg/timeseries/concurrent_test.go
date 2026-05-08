// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// concurrent_test.go
//
// Concurrency and race-detector tests for the timeseries store. All tests
// here are intended to be run with -race. They exercise paths that touch
// shared state: the registry mutex, the event counter atomics, and Pebble
// itself.
//
// Run: go test -race -run TestConcurrent ./pkg/timeseries/

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestConcurrent_ParallelAppends writes from many goroutines to a single
// timeline and verifies the event count matches total writes with no errors.
func TestConcurrent_ParallelAppends(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	const workers = 20
	const eventsPerWorker = 50

	var (
		wg      sync.WaitGroup
		errCount atomic.Int64
	)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < eventsPerWorker; i++ {
				ts := base.Add(time.Duration(workerID*eventsPerWorker+i) * time.Millisecond)
				err := store.Append(ctx, Event{
					Timeline: 1,
					Dims:     []uint64{uint64(workerID), uint64(i)},
					Time:     ts,
					Nums:     []float64{float64(workerID*1000 + i)},
				})
				if err != nil {
					errCount.Add(1)
				}
			}
		}(w)
	}
	wg.Wait()

	if errCount.Load() != 0 {
		t.Fatalf("parallel appends: %d errors", errCount.Load())
	}

	stats, err := store.TimelineStats(ctx, 1)
	if err != nil {
		t.Fatalf("TimelineStats: %v", err)
	}
	want := int64(workers * eventsPerWorker)
	if stats.TotalEvents != want {
		t.Errorf("TotalEvents = %d, want %d", stats.TotalEvents, want)
	}
}

// TestConcurrent_AppendAndQuery runs appenders and readers concurrently and
// verifies no races, panics, or errors occur.
func TestConcurrent_AppendAndQuery(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 1)
	ctx := context.Background()

	// Seed some initial events.
	for i := 0; i < 100; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: base.Add(time.Duration(i) * time.Second),
			Nums: []float64{float64(i)},
		})
	}

	const duration = 300 * time.Millisecond
	stop := make(chan struct{})

	var (
		wg         sync.WaitGroup
		writeErrors atomic.Int64
		readErrors  atomic.Int64
	)

	// Writers.
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				ts := base.Add(time.Duration(100+id*10000+i) * time.Second)
				if err := store.Append(ctx, Event{
					Timeline: 1, Dims: []uint64{1}, Time: ts,
				}); err != nil {
					writeErrors.Add(1)
				}
				i++
			}
		}(w)
	}

	// Readers.
	for r := 0; r < 5; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				_, err := store.Latest(ctx, LatestQuery{
					Timeline: 1, Dims: []uint64{1}, N: 10,
				})
				if err != nil {
					readErrors.Add(1)
				}
			}
		}()
	}

	time.Sleep(duration)
	close(stop)
	wg.Wait()

	if writeErrors.Load() != 0 {
		t.Errorf("concurrent write errors: %d", writeErrors.Load())
	}
	if readErrors.Load() != 0 {
		t.Errorf("concurrent read errors: %d", readErrors.Load())
	}
}

// TestConcurrent_DefineTimelineIdempotency verifies that concurrent calls to
// DefineTimeline with the same ID don't corrupt the registry or produce
// unexpected errors. At most one definition should win; all subsequent
// identical calls should succeed silently.
func TestConcurrent_DefineTimelineIdempotency(t *testing.T) {
	store := mustOpenStore(t)
	ctx := context.Background()
	_ = ctx

	const workers = 20
	var wg sync.WaitGroup
	errs := make([]error, workers)

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			errs[id] = store.DefineTimeline(1, TimelineConfig{
				Dims: 2, Name: "sensor-data",
			})
		}(w)
	}
	wg.Wait()

	// All errors must be nil (idempotent define) or a dims-mismatch error.
	// None may be an unexpected internal error.
	for i, err := range errs {
		if err != nil {
			// A dims mismatch would only occur if two goroutines raced with
			// different configs — we're using identical configs here, so any
			// error is unexpected.
			t.Errorf("worker %d: unexpected error: %v", i, err)
		}
	}

	// Registry must be consistent: exactly one timeline defined.
	ids := store.Timelines()
	if len(ids) != 1 || ids[0] != 1 {
		t.Errorf("after concurrent define: got timelines %v, want [1]", ids)
	}
}

// TestConcurrent_PurgeWithOngoingAppends verifies that a Purge call running
// concurrently with ongoing appends doesn't panic, corrupt data, or leave
// events that should be retained.
func TestConcurrent_PurgeWithOngoingAppends(t *testing.T) {
	cfg := testStoreConfig()
	cfg.DefaultRetentionDays = 0

	store, err := NewPebbleStore(t.TempDir(), cfg, testPebbleConfig())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()

	// 7-day retention.
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 7}); err != nil {
		t.Fatalf("define: %v", err)
	}

	ctx := context.Background()
	now := time.Now().UTC()

	// Pre-seed old events (should be purged).
	for i := 0; i < 50; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: now.Add(-10 * 24 * time.Hour).Add(time.Duration(i) * time.Second),
		})
	}
	// Pre-seed recent events (should survive).
	for i := 0; i < 50; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: now.Add(-1 * time.Hour).Add(time.Duration(i) * time.Second),
		})
	}

	stop := make(chan struct{})
	var (
		wg          sync.WaitGroup
		appendErrors atomic.Int64
	)

	// Concurrent appenders writing recent events.
	for w := 0; w < 5; w++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			i := 0
			for {
				select {
				case <-stop:
					return
				default:
				}
				ts := now.Add(time.Duration(id*100000+i) * time.Millisecond)
				if err := store.Append(ctx, Event{
					Timeline: 1, Dims: []uint64{1}, Time: ts,
				}); err != nil {
					appendErrors.Add(1)
				}
				i++
			}
		}(w)
	}

	// Run purge twice while appends are in flight.
	for i := 0; i < 2; i++ {
		if err := store.Purge(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Purge %d: %v", i, err)
		}
		time.Sleep(20 * time.Millisecond)
	}

	close(stop)
	wg.Wait()

	if appendErrors.Load() != 0 {
		t.Errorf("append errors during concurrent purge: %d", appendErrors.Load())
	}

	// All 50 pre-seeded old events should be gone.
	old, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{1},
		From: now.Add(-15 * 24 * time.Hour),
		To:   now.Add(-8 * 24 * time.Hour),
	})
	if err != nil {
		t.Fatalf("query old events: %v", err)
	}
	if len(old) != 0 {
		t.Errorf("found %d old events after purge, want 0", len(old))
	}
}

// TestConcurrent_BatchAndSingleAppendMix verifies that batch appends and
// single appends to the same timeline are safe when interleaved across
// goroutines.
func TestConcurrent_BatchAndSingleAppendMix(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 1)
	ctx := context.Background()

	const singles = 10
	const batches = 10
	const batchSize = 20

	var wg sync.WaitGroup
	var errCount atomic.Int64

	// Single appenders.
	for i := 0; i < singles; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			ts := base.Add(time.Duration(id) * time.Hour)
			if err := store.Append(ctx, Event{
				Timeline: 1, Dims: []uint64{uint64(id)}, Time: ts,
			}); err != nil {
				errCount.Add(1)
			}
		}(i)
	}

	// Batch appenders.
	for b := 0; b < batches; b++ {
		wg.Add(1)
		go func(batchID int) {
			defer wg.Done()
			events := make([]Event, batchSize)
			for i := range events {
				events[i] = Event{
					Timeline: 1,
					Dims:     []uint64{uint64(1000 + batchID*batchSize + i)},
					Time:     base.Add(time.Duration(batchID*batchSize+i) * time.Minute),
				}
			}
			if _, err := store.AppendBatch(ctx, events, 0); err != nil {
				errCount.Add(1)
			}
		}(b)
	}

	wg.Wait()

	if errCount.Load() != 0 {
		t.Errorf("mixed append: %d errors", errCount.Load())
	}

	want := int64(singles + batches*batchSize)
	stats, err := store.TimelineStats(ctx, 1)
	if err != nil {
		t.Fatalf("TimelineStats: %v", err)
	}
	if stats.TotalEvents != want {
		t.Errorf("TotalEvents = %d, want %d", stats.TotalEvents, want)
	}
}
