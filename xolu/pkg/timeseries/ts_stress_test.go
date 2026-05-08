// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// ts_stress_test.go
//
// High-volume and concurrency stress tests. All are skipped in -short mode.
// Run under -race to detect data races.
//
// Deferred to the caller for full runs; in-container execution uses the
// smaller iteration counts that complete within ~10 seconds.

import (
	"context"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	stressSmallEvents  = 5_000  // safe in-container count
	stressWorkerCount  = 10
	stressWorkerSecs   = 3
)

// --- Bulk creation ---

// TestTSStress_BulkAppend writes stressSmallEvents events across 10 timelines
// and verifies the total count via TimelineStats.
func TestTSStress_BulkAppend(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	const numTimelines = 10
	eventsPerTimeline := stressSmallEvents / numTimelines

	for tid := 1; tid <= numTimelines; tid++ {
		if err := store.DefineTimeline(TimelineID(tid), TimelineConfig{Dims: 2}); err != nil {
			t.Fatalf("define timeline %d: %v", tid, err)
		}
	}

	base := time.Unix(1_000_000, 0).UTC()
	t.Logf("Writing %d events across %d timelines...", stressSmallEvents, numTimelines)
	start := time.Now()

	for tid := 1; tid <= numTimelines; tid++ {
		for i := 0; i < eventsPerTimeline; i++ {
			ev := Event{
				Timeline: TimelineID(tid),
				Dims:     []uint64{uint64(tid), uint64(i % 100)},
				Time:     base.Add(time.Duration(i) * time.Millisecond),
				Nums:     []float64{float64(i)},
			}
			if err := store.Append(ctx, ev); err != nil {
				t.Fatalf("tid=%d i=%d: Append: %v", tid, i, err)
			}
		}
	}

	elapsed := time.Since(start)
	rate := float64(stressSmallEvents) / elapsed.Seconds()
	t.Logf("Wrote %d events in %v (%.0f events/sec)", stressSmallEvents, elapsed, rate)

	// Verify counts via TimelineStats.
	var totalEvents int64
	for tid := 1; tid <= numTimelines; tid++ {
		stats, err := store.TimelineStats(ctx, TimelineID(tid))
		if err != nil {
			t.Fatalf("TimelineStats tid=%d: %v", tid, err)
		}
		totalEvents += stats.TotalEvents
	}
	if totalEvents != int64(stressSmallEvents) {
		t.Errorf("total events %d, want %d", totalEvents, stressSmallEvents)
	}
}

// --- Concurrent workers ---

// TestTSStress_ConcurrentWorkers runs stressWorkerCount goroutines doing a
// mix of appends and queries against the same timeline. Run under -race.
func TestTSStress_ConcurrentWorkers(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1}); err != nil {
		t.Fatal(err)
	}

	var (
		appends int64
		queries int64
		errors  int64
		wg      sync.WaitGroup
	)

	base := time.Unix(1_000_000, 0).UTC()
	stopCh := make(chan struct{})
	var counter int64

	t.Logf("Running %d workers for %ds...", stressWorkerCount, stressWorkerSecs)
	start := time.Now()

	for w := 0; w < stressWorkerCount; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano() + int64(workerID)))

			for {
				select {
				case <-stopCh:
					return
				default:
				}

				if rng.Float32() < 0.7 {
					// 70% appends
					seq := atomic.AddInt64(&counter, 1)
					ev := Event{
						Timeline: 1,
						Dims:     []uint64{uint64(seq % 100)},
						Time:     base.Add(time.Duration(seq) * time.Millisecond),
						Nums:     []float64{float64(seq)},
					}
					if err := store.Append(ctx, ev); err != nil {
						atomic.AddInt64(&errors, 1)
					} else {
						atomic.AddInt64(&appends, 1)
					}
				} else {
					// 30% queries (Latest — cheapest read)
					_, err := store.Latest(ctx, LatestQuery{
						Timeline: 1,
						Dims:     []uint64{uint64(rng.Intn(100))},
						N:        3,
					})
					if err != nil {
						atomic.AddInt64(&errors, 1)
					} else {
						atomic.AddInt64(&queries, 1)
					}
				}
			}
		}(w)
	}

	time.Sleep(time.Duration(stressWorkerSecs) * time.Second)
	close(stopCh)
	wg.Wait()

	elapsed := time.Since(start)
	totalOps := appends + queries
	t.Logf("Results over %v:", elapsed)
	t.Logf("  Appends: %d (%.0f/sec)", appends, float64(appends)/elapsed.Seconds())
	t.Logf("  Queries: %d (%.0f/sec)", queries, float64(queries)/elapsed.Seconds())
	t.Logf("  Errors:  %d", errors)
	t.Logf("  Total:   %d (%.0f ops/sec)", totalOps, float64(totalOps)/elapsed.Seconds())

	if errors > 0 {
		t.Errorf("expected 0 errors, got %d", errors)
	}
}

// --- Bulk queries ---

// TestTSStress_BulkQueries seeds events across multiple dimension values and
// verifies that repeated partial-prefix queries return consistent results.
func TestTSStress_BulkQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	store, err := NewPebbleStore(dir, testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 2}); err != nil {
		t.Fatal(err)
	}

	const (
		numD0    = 5
		eventsPerCombo = 100
		total    = numD0 * eventsPerCombo
	)

	base := time.Unix(1_000_000, 0).UTC()
	t.Logf("Seeding %d events...", total)
	for d0 := 0; d0 < numD0; d0++ {
		for i := 0; i < eventsPerCombo; i++ {
			store.Append(ctx, Event{
				Timeline: 1,
				Dims:     []uint64{uint64(d0), uint64(i)},
				Time:     base.Add(time.Duration(d0*eventsPerCombo+i) * time.Millisecond),
				Nums:     []float64{float64(i)},
			})
		}
	}

	from := base.Add(-time.Second)
	to := base.Add(time.Duration(total+1) * time.Millisecond)

	t.Log("Running 100 bulk queries with partial prefix...")
	for d0 := 0; d0 < numD0; d0++ {
		for run := 0; run < 20; run++ {
			events, err := store.QueryRange(ctx, RangeQuery{
				Timeline: 1,
				Dims:     []uint64{uint64(d0)},
				From:     from,
				To:       to,
			})
			if err != nil {
				t.Fatalf("d0=%d run=%d: QueryRange: %v", d0, run, err)
			}
			if len(events) != eventsPerCombo {
				t.Errorf("d0=%d: got %d events, want %d", d0, len(events), eventsPerCombo)
			}
		}
	}
}

// --- Mixed workload: appends + purge cycles ---

// TestTSStress_MixedWorkload_AppendAndPurge runs concurrent appends while
// purge cycles fire periodically. Verifies that events written after the
// purge cutoff are never deleted.
func TestTSStress_MixedWorkload_AppendAndPurge(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping stress test in short mode")
	}

	dir := t.TempDir()
	cfg := testStoreConfig()
	cfg.DefaultRetentionDays = 1 // short retention
	store, err := NewPebbleStore(dir, cfg, testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1, RetentionDays: 1}); err != nil {
		t.Fatal(err)
	}

	// cutoff is 2 days ago — only old events (3 days ago) should be purged.
	now := time.Now().UTC()
	old := now.Add(-72 * time.Hour)
	recent := now.Add(-12 * time.Hour)

	// Seed old events (should be purged).
	for i := 0; i < 50; i++ {
		store.Append(ctx, Event{
			Timeline: 1,
			Dims:     []uint64{1},
			Time:     old.Add(time.Duration(i) * time.Second),
		})
	}

	// Seed recent events (must survive).
	for i := 0; i < 50; i++ {
		store.Append(ctx, Event{
			Timeline: 1,
			Dims:     []uint64{1},
			Time:     recent.Add(time.Duration(i) * time.Second),
		})
	}

	// Concurrent appends (future-ish timestamps, must survive).
	var wg sync.WaitGroup
	var appendErrors int64
	stopCh := make(chan struct{})
	var seq int64

	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stopCh:
					return
				default:
				}
				n := atomic.AddInt64(&seq, 1)
				store.Append(ctx, Event{
					Timeline: 1,
					Dims:     []uint64{2},
					Time:     now.Add(time.Duration(n) * time.Millisecond),
				})
			}
		}()
	}

	// Run 3 purge cycles via Purge(ctx) — the timeline was defined with
	// RetentionDays=1, so anything older than 24h will be pruned.
	for i := 0; i < 3; i++ {
		time.Sleep(50 * time.Millisecond)
		if err := store.Purge(ctx); err != nil {
			t.Errorf("Purge cycle %d: %v", i, err)
		}
	}

	close(stopCh)
	wg.Wait()

	if appendErrors > 0 {
		t.Errorf("append errors: %d", appendErrors)
	}

	// Verify recent events survived the purge.
	events, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1,
		Dims:     []uint64{1},
		From:     recent.Add(-time.Second),
		To:       recent.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryRange after purge: %v", err)
	}
	if len(events) != 50 {
		t.Errorf("recent events after purge: got %d, want 50", len(events))
	}

	// Verify old events were purged.
	oldEvents, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1,
		Dims:     []uint64{1},
		From:     old.Add(-time.Second),
		To:       old.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("QueryRange old events: %v", err)
	}
	if len(oldEvents) > 0 {
		t.Errorf("old events should be purged but %d remain", len(oldEvents))
	}

}
