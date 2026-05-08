// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

//go:build redis

package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

// ============================================================================
// Redis Cache Tests
// ============================================================================

// These tests require a running Redis instance.
// Run with: go test -tags=redis ./pkg/cache/...

func TestRedisCache_SetGet(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	// Clean up any existing test key
	cache.Delete(ctx, "test:key1")

	// Set a value
	err = cache.Set(ctx, "test:key1", "value1", time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get the value
	val, err := cache.Get(ctx, "test:key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "value1" {
		t.Errorf("Expected 'value1', got '%v'", val)
	}

	// Clean up
	cache.Delete(ctx, "test:key1")
}

func TestRedisCache_PerItemTTL(t *testing.T) {
	// Create cache with long default TTL
	cache, err := NewRedisCache("localhost", 6379, time.Hour, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	// Clean up
	cache.Delete(ctx, "test:shortttl")

	// Set with short per-item TTL (Redis honours this)
	err = cache.Set(ctx, "test:shortttl", "expires-soon", time.Millisecond*100)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Should exist immediately
	_, err = cache.Get(ctx, "test:shortttl")
	if err != nil {
		t.Fatalf("Get failed immediately after set: %v", err)
	}

	// Wait for expiration
	time.Sleep(time.Millisecond * 200)

	// Should be expired (Redis per-item TTL works)
	_, err = cache.Get(ctx, "test:shortttl")
	if err == nil {
		t.Error("Expected error for expired key - Redis should honour per-item TTL")
	}
}

func TestRedisCache_Delete(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	cache.Set(ctx, "test:delete", "value", time.Minute)

	err = cache.Delete(ctx, "test:delete")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = cache.Get(ctx, "test:delete")
	if err == nil {
		t.Error("Expected error after delete")
	}
}

func TestRedisCache_Exists(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	// Clean up
	cache.Delete(ctx, "test:exists")

	// Should not exist
	exists, err := cache.Exists(ctx, "test:exists")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if exists {
		t.Error("Key should not exist")
	}

	// Set it
	cache.Set(ctx, "test:exists", "value", time.Minute)

	// Should exist now
	exists, err = cache.Exists(ctx, "test:exists")
	if err != nil {
		t.Fatalf("Exists failed: %v", err)
	}
	if !exists {
		t.Error("Key should exist")
	}

	// Clean up
	cache.Delete(ctx, "test:exists")
}

func TestRedisCache_ComplexValues(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	// Clean up
	cache.Delete(ctx, "test:complex")

	// Store a map (Redis cache serialises to JSON)
	data := map[string]interface{}{
		"id":   float64(1), // JSON numbers become float64
		"name": "test",
	}

	err = cache.Set(ctx, "test:complex", data, time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	val, err := cache.Get(ctx, "test:complex")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	retrieved, ok := val.(map[string]interface{})
	if !ok {
		t.Fatalf("Expected map, got %T", val)
	}
	if retrieved["name"] != "test" {
		t.Errorf("Expected name 'test', got '%v'", retrieved["name"])
	}

	// Clean up
	cache.Delete(ctx, "test:complex")
}

// ============================================================================
// Redis Stress Tests
// ============================================================================

func TestRedisStress_ConcurrentAccess(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()
	numWorkers := 50
	opsPerWorker := 100
	var wg sync.WaitGroup
	errors := make(chan error, numWorkers*opsPerWorker)

	start := time.Now()

	for w := 0; w < numWorkers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < opsPerWorker; i++ {
				key := fmt.Sprintf("stress:%d:%d", workerID, i)
				value := fmt.Sprintf("value-%d-%d", workerID, i)

				// Set
				if err := cache.Set(ctx, key, value, time.Minute); err != nil {
					errors <- fmt.Errorf("Set failed: %v", err)
					continue
				}

				// Get
				got, err := cache.Get(ctx, key)
				if err != nil {
					errors <- fmt.Errorf("Get failed: %v", err)
					continue
				}
				if got != value {
					errors <- fmt.Errorf("Value mismatch: expected %s, got %v", value, got)
				}

				// Delete
				if err := cache.Delete(ctx, key); err != nil {
					errors <- fmt.Errorf("Delete failed: %v", err)
				}
			}
		}(w)
	}

	wg.Wait()
	close(errors)

	elapsed := time.Since(start)
	totalOps := numWorkers * opsPerWorker * 3 // Set + Get + Delete
	opsPerSec := float64(totalOps) / elapsed.Seconds()

	var errCount int
	for err := range errors {
		errCount++
		if errCount <= 5 {
			t.Logf("Error: %v", err)
		}
	}

	t.Logf("Concurrent stress: %d workers x %d ops = %d total ops", numWorkers, opsPerWorker, totalOps)
	t.Logf("Completed in %v (%.0f ops/sec)", elapsed, opsPerSec)
	t.Logf("Errors: %d", errCount)

	if errCount > 0 {
		t.Errorf("Had %d errors during stress test", errCount)
	}
}

func TestRedisStress_LargeValues(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()

	// Test various payload sizes
	sizes := []int{1024, 10 * 1024, 100 * 1024, 1024 * 1024} // 1KB, 10KB, 100KB, 1MB

	for _, size := range sizes {
		key := fmt.Sprintf("stress:large:%d", size)

		// Create payload of specified size using printable ASCII
		// (avoids JSON escaping issues with binary data)
		payload := make([]byte, size)
		for i := range payload {
			payload[i] = byte('A' + (i % 26))
		}
		value := string(payload)

		start := time.Now()

		// Set
		if err := cache.Set(ctx, key, value, time.Minute); err != nil {
			t.Errorf("Set %d bytes failed: %v", size, err)
			continue
		}
		setTime := time.Since(start)

		// Get
		start = time.Now()
		got, err := cache.Get(ctx, key)
		if err != nil {
			t.Errorf("Get %d bytes failed: %v", size, err)
			continue
		}
		getTime := time.Since(start)

		gotStr, ok := got.(string)
		if !ok {
			t.Errorf("Expected string, got %T", got)
			continue
		}
		if len(gotStr) != size {
			t.Errorf("Size mismatch: expected %d, got %d", size, len(gotStr))
		}

		// Clean up
		cache.Delete(ctx, key)

		t.Logf("Payload %7d bytes: Set %v, Get %v", size, setTime, getTime)
	}
}

func TestRedisStress_PatternDelete(t *testing.T) {
	cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
	if err != nil {
		t.Fatalf("Failed to create Redis cache: %v", err)
	}
	defer cache.Close()

	ctx := context.Background()
	numKeys := 500

	// Create many keys with a common prefix
	start := time.Now()
	for i := 0; i < numKeys; i++ {
		key := fmt.Sprintf("stress:pattern:%d", i)
		if err := cache.Set(ctx, key, i, time.Minute); err != nil {
			t.Fatalf("Set failed: %v", err)
		}
	}
	setTime := time.Since(start)

	// Verify a few exist
	for _, i := range []int{0, 100, 499} {
		key := fmt.Sprintf("stress:pattern:%d", i)
		exists, err := cache.Exists(ctx, key)
		if err != nil {
			t.Fatalf("Exists failed: %v", err)
		}
		if !exists {
			t.Errorf("Key %s should exist", key)
		}
	}

	// Delete by pattern
	start = time.Now()
	if err := cache.DeletePattern(ctx, "stress:pattern:*"); err != nil {
		t.Fatalf("DeletePattern failed: %v", err)
	}
	deleteTime := time.Since(start)

	// Verify all are gone
	for _, i := range []int{0, 100, 499} {
		key := fmt.Sprintf("stress:pattern:%d", i)
		exists, err := cache.Exists(ctx, key)
		if err != nil {
			t.Fatalf("Exists failed: %v", err)
		}
		if exists {
			t.Errorf("Key %s should be deleted", key)
		}
	}

	t.Logf("Pattern delete: Created %d keys in %v, deleted in %v", numKeys, setTime, deleteTime)
}

func TestRedisStress_RapidReconnect(t *testing.T) {
	ctx := context.Background()
	numConnections := 50

	start := time.Now()
	var wg sync.WaitGroup
	errors := make(chan error, numConnections)

	for i := 0; i < numConnections; i++ {
		wg.Add(1)
		go func(connID int) {
			defer wg.Done()

			cache, err := NewRedisCache("localhost", 6379, time.Second*60, 0, 0)
			if err != nil {
				errors <- fmt.Errorf("Connection %d failed: %v", connID, err)
				return
			}

			// Do a quick operation
			key := fmt.Sprintf("reconnect:%d", connID)
			if err := cache.Set(ctx, key, connID, time.Second*10); err != nil {
				errors <- fmt.Errorf("Set %d failed: %v", connID, err)
			}

			cache.Delete(ctx, key)
			cache.Close()
		}(i)
	}

	wg.Wait()
	close(errors)

	elapsed := time.Since(start)

	var errCount int
	for err := range errors {
		errCount++
		if errCount <= 5 {
			t.Logf("Error: %v", err)
		}
	}

	t.Logf("Rapid reconnect: %d connections in %v (%.0f conn/sec)", 
		numConnections, elapsed, float64(numConnections)/elapsed.Seconds())

	if errCount > 0 {
		t.Errorf("Had %d connection errors", errCount)
	}
}
