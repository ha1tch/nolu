// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

// benchmark_storage_test.go
//
// Benchmarks for storage performance regression detection.
// Covers: concurrent mixed read/write, large entity handling,
// and cross-backend comparison (JSONFile vs SQLite).
//
// Run with: go test ./pkg/storage/... -bench=. -benchmem
//
// Author: ha1tch <h@ual.fi>

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

// =============================================================================
// Concurrent mixed workload
// =============================================================================

// BenchmarkSQLite_ConcurrentMixed simulates realistic concurrent access:
// 80% reads, 20% writes across multiple goroutines.
func BenchmarkSQLite_ConcurrentMixed(b *testing.B) {
	tmpFile, _ := os.CreateTemp("", "olu-bench-mixed-*.db")
	tmpFile.Close()
	defer os.Remove(tmpFile.Name())

	store, err := storage.NewStore("sqlite", map[string]interface{}{
		"db_path": tmpFile.Name(),
	})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Pre-populate 100 entities
	for i := 0; i < 100; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"name":   fmt.Sprintf("item-%d", i),
			"status": "active",
		})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			if i%5 == 0 {
				// 20% writes
				store.Create(ctx, "items", map[string]interface{}{
					"name":   fmt.Sprintf("bench-%d", i),
					"status": "active",
				})
			} else {
				// 80% reads
				id := (i % 100) + 1
				store.Get(ctx, "items", id)
			}
		}
	})
}

// BenchmarkJSONFile_ConcurrentMixed is the same workload against JSONFile
// for cross-backend comparison.
func BenchmarkJSONFile_ConcurrentMixed(b *testing.B) {
	tmpDir, _ := os.MkdirTemp("", "olu-bench-jf-mixed-*")
	defer os.RemoveAll(tmpDir)

	store, err := storage.NewStore("jsonfile", map[string]interface{}{
		"base_dir": tmpDir,
		"schema":   "bench",
	})
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()

	ctx := context.Background()

	// Pre-populate 100 entities
	for i := 0; i < 100; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"name":   fmt.Sprintf("item-%d", i),
			"status": "active",
		})
	}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		i := 0
		for pb.Next() {
			i++
			if i%5 == 0 {
				store.Create(ctx, "items", map[string]interface{}{
					"name":   fmt.Sprintf("bench-%d", i),
					"status": "active",
				})
			} else {
				id := (i % 100) + 1
				store.Get(ctx, "items", id)
			}
		}
	})
}

// =============================================================================
// Large entity benchmarks
// =============================================================================

// BenchmarkSQLite_LargeEntity measures Create/Get performance with entities
// of varying sizes to detect size-dependent performance cliffs.
func BenchmarkSQLite_LargeEntity(b *testing.B) {
	sizes := []struct {
		name string
		size int
	}{
		{"1KB", 1024},
		{"10KB", 10 * 1024},
		{"100KB", 100 * 1024},
		{"500KB", 500 * 1024},
	}

	for _, sz := range sizes {
		b.Run(fmt.Sprintf("Create_%s", sz.name), func(b *testing.B) {
			tmpFile, _ := os.CreateTemp("", "olu-bench-large-*.db")
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			store, err := storage.NewStore("sqlite", map[string]interface{}{
				"db_path": tmpFile.Name(),
			})
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()

			ctx := context.Background()
			payload := strings.Repeat("x", sz.size)

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				store.Create(ctx, "items", map[string]interface{}{
					"name":    fmt.Sprintf("large-%d", i),
					"payload": payload,
				})
			}
		})

		b.Run(fmt.Sprintf("Get_%s", sz.name), func(b *testing.B) {
			tmpFile, _ := os.CreateTemp("", "olu-bench-large-*.db")
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			store, err := storage.NewStore("sqlite", map[string]interface{}{
				"db_path": tmpFile.Name(),
			})
			if err != nil {
				b.Fatal(err)
			}
			defer store.Close()

			ctx := context.Background()
			payload := strings.Repeat("x", sz.size)

			// Create one entity to read
			id, _ := store.Create(ctx, "items", map[string]interface{}{
				"name":    "large-entity",
				"payload": payload,
			})

			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				store.Get(ctx, "items", id)
			}
		})
	}
}

// =============================================================================
// Cross-backend CRUD comparison
// =============================================================================

// BenchmarkCrossBackend_Create provides side-by-side Create throughput
// for direct comparison between backends.
func BenchmarkCrossBackend_Create(b *testing.B) {
	b.Run("SQLite", func(b *testing.B) {
		tmpFile, _ := os.CreateTemp("", "olu-bench-xb-*.db")
		tmpFile.Close()
		defer os.Remove(tmpFile.Name())

		store, _ := storage.NewStore("sqlite", map[string]interface{}{
			"db_path": tmpFile.Name(),
		})
		defer store.Close()
		ctx := context.Background()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			store.Create(ctx, "items", map[string]interface{}{
				"name": fmt.Sprintf("item-%d", i),
			})
		}
	})

	b.Run("JSONFile", func(b *testing.B) {
		tmpDir, _ := os.MkdirTemp("", "olu-bench-xb-jf-*")
		defer os.RemoveAll(tmpDir)

		store, _ := storage.NewStore("jsonfile", map[string]interface{}{
			"base_dir": tmpDir,
			"schema":   "bench",
		})
		defer store.Close()
		ctx := context.Background()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			store.Create(ctx, "items", map[string]interface{}{
				"name": fmt.Sprintf("item-%d", i),
			})
		}
	})
}

// BenchmarkCrossBackend_Search provides side-by-side Search throughput.
func BenchmarkCrossBackend_Search(b *testing.B) {
	prepopulate := func(store storage.Store) {
		ctx := context.Background()
		for i := 0; i < 500; i++ {
			store.Create(ctx, "items", map[string]interface{}{
				"name":   fmt.Sprintf("item-%d", i),
				"status": []string{"active", "inactive", "pending"}[i%3],
			})
		}
	}

	b.Run("SQLite", func(b *testing.B) {
		tmpFile, _ := os.CreateTemp("", "olu-bench-xbs-*.db")
		tmpFile.Close()
		defer os.Remove(tmpFile.Name())

		store, _ := storage.NewStore("sqlite", map[string]interface{}{
			"db_path": tmpFile.Name(),
		})
		defer store.Close()
		prepopulate(store)
		ctx := context.Background()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			store.Search(ctx, "items", "status", "active", "exact")
		}
	})

	b.Run("JSONFile", func(b *testing.B) {
		tmpDir, _ := os.MkdirTemp("", "olu-bench-xbs-jf-*")
		defer os.RemoveAll(tmpDir)

		store, _ := storage.NewStore("jsonfile", map[string]interface{}{
			"base_dir": tmpDir,
			"schema":   "bench",
		})
		defer store.Close()
		prepopulate(store)
		ctx := context.Background()

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			store.Search(ctx, "items", "name", "item-42", "exact")
		}
	})
}

// =============================================================================
// Concurrent write contention
// =============================================================================

// BenchmarkSQLite_WriteContention measures throughput under heavy concurrent
// write load — useful for detecting lock contention regressions.
func BenchmarkSQLite_WriteContention(b *testing.B) {
	workers := []int{1, 2, 4, 8}

	for _, w := range workers {
		b.Run(fmt.Sprintf("%d_writers", w), func(b *testing.B) {
			tmpFile, _ := os.CreateTemp("", "olu-bench-wc-*.db")
			tmpFile.Close()
			defer os.Remove(tmpFile.Name())

			store, _ := storage.NewStore("sqlite", map[string]interface{}{
				"db_path": tmpFile.Name(),
			})
			defer store.Close()
			ctx := context.Background()

			b.ResetTimer()

			var wg sync.WaitGroup
			opsPerWorker := b.N / w
			if opsPerWorker == 0 {
				opsPerWorker = 1
			}

			for g := 0; g < w; g++ {
				wg.Add(1)
				go func(workerID int) {
					defer wg.Done()
					for i := 0; i < opsPerWorker; i++ {
						store.Create(ctx, "items", map[string]interface{}{
							"name":   fmt.Sprintf("w%d-%d", workerID, i),
							"worker": workerID,
						})
					}
				}(g)
			}
			wg.Wait()
		})
	}
}
