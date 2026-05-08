// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package cache

import (
	"context"
	"testing"
	"time"
)

func TestNewMemoryCache(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	if cache == nil {
		t.Fatal("NewMemoryCache returned nil")
	}
}

func TestMemoryCache_SetGet(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	// Set a value
	err := cache.Set(ctx, "key1", "value1", time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	// Get the value
	val, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "value1" {
		t.Errorf("Expected 'value1', got '%v'", val)
	}
}

func TestMemoryCache_GetMiss(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	_, err := cache.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("Expected error for cache miss")
	}
}

func TestMemoryCache_Delete(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", time.Minute)
	
	err := cache.Delete(ctx, "key1")
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, err = cache.Get(ctx, "key1")
	if err == nil {
		t.Error("Expected error after delete")
	}
}

func TestMemoryCache_Expiration(t *testing.T) {
	// Create cache with short TTL for expiration testing
	cache := NewMemoryCache(100, time.Millisecond*50)
	ctx := context.Background()

	// Set a value (TTL param is ignored - uses cache's global TTL)
	cache.Set(ctx, "key1", "value1", time.Millisecond*50)

	// Should exist immediately
	_, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get failed immediately after set: %v", err)
	}

	// Wait for expiration (cache TTL is 50ms)
	time.Sleep(time.Millisecond * 100)

	// Should be expired now
	_, err = cache.Get(ctx, "key1")
	if err == nil {
		t.Error("Expected error for expired key")
	}
}

func TestMemoryCache_ComplexValues(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	// Store a map
	data := map[string]interface{}{
		"id":    1,
		"name":  "test",
		"tags":  []string{"a", "b"},
	}

	err := cache.Set(ctx, "complex", data, time.Minute)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	val, err := cache.Get(ctx, "complex")
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
}

func TestMemoryCache_Overwrite(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	cache.Set(ctx, "key1", "value1", time.Minute)
	cache.Set(ctx, "key1", "value2", time.Minute)

	val, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "value2" {
		t.Errorf("Expected 'value2', got '%v'", val)
	}
}

func TestMemoryCache_Concurrent(t *testing.T) {
	cache := NewMemoryCache(1000, time.Second*60)
	ctx := context.Background()

	done := make(chan bool)

	// Writer goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Set(ctx, "key", i, time.Minute)
		}
		done <- true
	}()

	// Reader goroutine
	go func() {
		for i := 0; i < 100; i++ {
			cache.Get(ctx, "key")
		}
		done <- true
	}()

	// Deleter goroutine
	go func() {
		for i := 0; i < 50; i++ {
			cache.Delete(ctx, "key")
			time.Sleep(time.Millisecond)
		}
		done <- true
	}()

	// Wait for all goroutines
	<-done
	<-done
	<-done
}

func TestMemoryCache_DeleteNonexistent(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	// Should not error on deleting nonexistent key
	err := cache.Delete(ctx, "nonexistent")
	if err != nil {
		t.Errorf("Delete nonexistent key should not error: %v", err)
	}
}

func TestMemoryCache_ZeroTTL(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	// Zero TTL should still work (immediate expiry or no expiry depending on implementation)
	err := cache.Set(ctx, "key1", "value1", 0)
	if err != nil {
		t.Fatalf("Set with zero TTL failed: %v", err)
	}
}

func TestMemoryCache_NilValue(t *testing.T) {
	cache := NewMemoryCache(100, time.Second*60)
	ctx := context.Background()

	err := cache.Set(ctx, "key1", nil, time.Minute)
	if err != nil {
		t.Fatalf("Set nil value failed: %v", err)
	}

	val, err := cache.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get nil value failed: %v", err)
	}
	if val != nil {
		t.Errorf("Expected nil, got %v", val)
	}
}
