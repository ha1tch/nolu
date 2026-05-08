// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/storage"
)

// =============================================================================
// Stress Test: 10,000 Records
// =============================================================================
//
// Simulates a high-volume workload:
// - 10,000 records across 100 categories
// - Mixed read/write patterns (90% reads, 10% writes)
// - Concurrent operations from multiple workers
// - Periodic bulk queries
//
// Run with: go test -v -run TestStress ./pkg/storage/...
// Or with race detector: go test -v -race -run TestStress ./pkg/storage/...

const (
	stressRecordCount   = 10000
	stressCategoryCount = 100
	stressWorkerCount   = 10
)

func setupStressTest(t *testing.T) (storage.Store, func()) {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "olu-stress-*.db")
	if err != nil {
		t.Fatalf("Failed to create temp file: %v", err)
	}
	tmpFile.Close()

	dbPath := tmpFile.Name()

	store, err := storage.NewStore("sqlite", map[string]interface{}{
		"db_path": dbPath,
	})
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("Failed to create store: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.Remove(dbPath)
	}

	return store, cleanup
}

// TestStress_BulkCreation tests creating 10,000 records
func TestStress_BulkCreation(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	store, cleanup := setupStressTest(t)
	defer cleanup()

	ctx := context.Background()
	start := time.Now()

	// Create records across categories
	for i := 0; i < stressRecordCount; i++ {
		categoryID := i % stressCategoryCount
		recordType := []string{"alpha", "beta", "gamma", "delta"}[i%4]
		status := []string{"active", "inactive", "pending"}[i%3]

		_, err := store.Create(ctx, "items", map[string]interface{}{
			"code":        fmt.Sprintf("ITEM-%05d", i),
			"category_id": categoryID,
			"type":        recordType,
			"status":      status,
			"value":       20.0 + float64(i%100)/10.0,
			"updated_at":  time.Now().Unix(),
			"version":     "v1.2.3",
			"priority":    100 - (i % 100),
		})
		if err != nil {
			t.Fatalf("Failed to create record %d: %v", i, err)
		}
	}

	elapsed := time.Since(start)
	rate := float64(stressRecordCount) / elapsed.Seconds()

	t.Logf("Created %d records in %v (%.0f records/sec)", stressRecordCount, elapsed, rate)

	// Verify count
	items, err := store.List(ctx, "items")
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(items) != stressRecordCount {
		t.Errorf("Expected %d records, got %d", stressRecordCount, len(items))
	}
}

// TestStress_ConcurrentWorkers simulates concurrent worker traffic
func TestStress_ConcurrentWorkers(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	store, cleanup := setupStressTest(t)
	defer cleanup()

	ctx := context.Background()

	// Pre-populate records
	t.Log("Populating records...")
	for i := 0; i < stressRecordCount; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"code":        fmt.Sprintf("ITEM-%05d", i),
			"category_id": i % stressCategoryCount,
			"type":        "standard",
			"status":      "active",
			"value":       20.0,
		})
	}

	// Concurrent worker simulation
	var wg sync.WaitGroup
	var reads, writes, errors int64
	duration := 5 * time.Second

	t.Logf("Running %d concurrent workers for %v...", stressWorkerCount, duration)
	start := time.Now()
	stopCh := make(chan struct{})

	// Start workers
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

				recordID := rng.Intn(stressRecordCount) + 1

				// 90% reads, 10% writes
				if rng.Float32() < 0.9 {
					// Read operation
					_, err := store.Get(ctx, "items", recordID)
					if err != nil {
						atomic.AddInt64(&errors, 1)
					} else {
						atomic.AddInt64(&reads, 1)
					}
				} else {
					// Write operation
					err := store.Patch(ctx, "items", recordID, map[string]interface{}{
						"value":      20.0 + rng.Float64()*10.0,
						"updated_at": time.Now().Unix(),
					})
					if err != nil {
						atomic.AddInt64(&errors, 1)
					} else {
						atomic.AddInt64(&writes, 1)
					}
				}
			}
		}(w)
	}

	// Run for duration
	time.Sleep(duration)
	close(stopCh)
	wg.Wait()

	elapsed := time.Since(start)
	totalOps := reads + writes
	opsPerSec := float64(totalOps) / elapsed.Seconds()

	t.Logf("Results over %v:", elapsed)
	t.Logf("  Reads:      %d (%.0f/sec)", reads, float64(reads)/elapsed.Seconds())
	t.Logf("  Writes:     %d (%.0f/sec)", writes, float64(writes)/elapsed.Seconds())
	t.Logf("  Errors:     %d", errors)
	t.Logf("  Total ops:  %d (%.0f ops/sec)", totalOps, opsPerSec)

	if errors > 0 {
		t.Errorf("Expected 0 errors, got %d", errors)
	}
}

// TestStress_BulkQueries simulates periodic bulk query patterns
func TestStress_BulkQueries(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	store, cleanup := setupStressTest(t)
	defer cleanup()

	ctx := context.Background()
	searcher := store.(storage.Searcher)

	// Pre-populate records
	t.Log("Populating records...")
	for i := 0; i < stressRecordCount; i++ {
		status := []string{"active", "inactive", "pending"}[i%3]
		store.Create(ctx, "items", map[string]interface{}{
			"code":        fmt.Sprintf("ITEM-%05d", i),
			"category_id": i % stressCategoryCount,
			"type":        []string{"alpha", "beta", "gamma"}[i%3],
			"status":      status,
			"value":       20.0 + float64(i%100)/10.0,
		})
	}

	// Simulate bulk queries
	queries := []struct {
		name      string
		field     string
		value     string
		matchType string
	}{
		{"Active records", "status", "active", "exact"},
		{"Alpha type", "type", "alpha", "exact"},
		{"Code prefix search", "code", "ITEM-001", "starts"},
		{"Pending records", "status", "pending", "exact"},
	}

	t.Log("Running bulk queries...")
	for _, q := range queries {
		start := time.Now()
		results, err := searcher.Search(ctx, "items", q.field, q.value, q.matchType)
		elapsed := time.Since(start)

		if err != nil {
			t.Errorf("%s: search failed: %v", q.name, err)
			continue
		}

		t.Logf("  %s: %d results in %v", q.name, len(results), elapsed)
	}

	// Full list timing
	start := time.Now()
	all, _ := store.List(ctx, "items")
	t.Logf("  Full list: %d records in %v", len(all), time.Since(start))
}

// TestStress_MixedWorkload combines all patterns
func TestStress_MixedWorkload(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping stress test in short mode")
	}

	store, cleanup := setupStressTest(t)
	defer cleanup()

	ctx := context.Background()
	searcher := store.(storage.Searcher)

	// Pre-populate
	t.Log("Populating records...")
	for i := 0; i < stressRecordCount; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"code":        fmt.Sprintf("ITEM-%05d", i),
			"category_id": i % stressCategoryCount,
			"status":      "active",
			"value":       20.0,
		})
	}

	var wg sync.WaitGroup
	var pointReads, updates, searches, listOps int64
	duration := 5 * time.Second
	stopCh := make(chan struct{})

	t.Logf("Running mixed workload for %v...", duration)
	start := time.Now()

	// Point read workers (60%)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			for {
				select {
				case <-stopCh:
					return
				default:
					store.Get(ctx, "items", rng.Intn(stressRecordCount)+1)
					atomic.AddInt64(&pointReads, 1)
				}
			}
		}()
	}

	// Update workers (20%)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rng := rand.New(rand.NewSource(time.Now().UnixNano()))
			for {
				select {
				case <-stopCh:
					return
				default:
					store.Patch(ctx, "items", rng.Intn(stressRecordCount)+1, map[string]interface{}{
						"value": 20.0 + rng.Float64()*10.0,
					})
					atomic.AddInt64(&updates, 1)
				}
			}
		}()
	}

	// Search workers (15%)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				searcher.Search(ctx, "items", "status", "active", "exact")
				atomic.AddInt64(&searches, 1)
			}
		}
	}()

	// List workers (5%)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stopCh:
				return
			default:
				store.List(ctx, "items")
				atomic.AddInt64(&listOps, 1)
				time.Sleep(100 * time.Millisecond) // Throttle expensive ops
			}
		}
	}()

	time.Sleep(duration)
	close(stopCh)
	wg.Wait()

	elapsed := time.Since(start)

	t.Logf("Results over %v:", elapsed)
	t.Logf("  Point reads: %d (%.0f/sec)", pointReads, float64(pointReads)/elapsed.Seconds())
	t.Logf("  Updates:     %d (%.0f/sec)", updates, float64(updates)/elapsed.Seconds())
	t.Logf("  Searches:    %d (%.0f/sec)", searches, float64(searches)/elapsed.Seconds())
	t.Logf("  List ops:    %d (%.0f/sec)", listOps, float64(listOps)/elapsed.Seconds())

	total := pointReads + updates + searches + listOps
	t.Logf("  Total ops:   %d (%.0f ops/sec)", total, float64(total)/elapsed.Seconds())
}

// BenchmarkStress provides benchmark versions for CI integration
func BenchmarkStress_PointRead_10k(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Populate
	for i := 0; i < stressRecordCount; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"code":        fmt.Sprintf("ITEM-%05d", i),
			"category_id": i % stressCategoryCount,
			"status":      "active",
		})
	}

	rng := rand.New(rand.NewSource(42))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		store.Get(ctx, "items", rng.Intn(stressRecordCount)+1)
	}
}

func BenchmarkStress_Update_10k(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Populate
	for i := 0; i < stressRecordCount; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"code":  fmt.Sprintf("ITEM-%05d", i),
			"value": 20.0,
		})
	}

	rng := rand.New(rand.NewSource(42))
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		store.Patch(ctx, "items", rng.Intn(stressRecordCount)+1, map[string]interface{}{
			"value": 20.0 + rng.Float64()*10.0,
		})
	}
}

func BenchmarkStress_Search_10k(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-*.db")
	tmpFile.Close()
	dbPath := tmpFile.Name()
	defer os.Remove(dbPath)

	store, _ := storage.NewStore("sqlite", map[string]interface{}{"db_path": dbPath})
	defer store.Close()

	ctx := context.Background()

	// Populate
	for i := 0; i < stressRecordCount; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"code":   fmt.Sprintf("ITEM-%05d", i),
			"status": []string{"active", "inactive"}[i%2],
		})
	}

	searcher := store.(storage.Searcher)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		searcher.Search(ctx, "items", "status", "active", "exact")
	}
}
