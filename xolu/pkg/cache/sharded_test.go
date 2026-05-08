// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestShardedMemoryCache_Basic(t *testing.T) {
	c := NewShardedMemoryCache(100, time.Minute, 0)
	ctx := context.Background()

	// Set and Get
	if err := c.Set(ctx, "key1", "value1", 0); err != nil {
		t.Fatalf("Set failed: %v", err)
	}
	val, err := c.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}
	if val != "value1" {
		t.Errorf("Get = %v, want 'value1'", val)
	}

	// Miss
	_, err = c.Get(ctx, "nonexistent")
	if err == nil {
		t.Error("expected error for missing key")
	}
}

func TestShardedMemoryCache_Delete(t *testing.T) {
	c := NewShardedMemoryCache(100, time.Minute, 0)
	ctx := context.Background()

	c.Set(ctx, "k", "v", 0)
	c.Delete(ctx, "k")

	_, err := c.Get(ctx, "k")
	if err == nil {
		t.Error("expected error after delete")
	}
}

func TestShardedMemoryCache_Exists(t *testing.T) {
	c := NewShardedMemoryCache(100, time.Minute, 0)
	ctx := context.Background()

	c.Set(ctx, "present", 42, 0)

	ok, _ := c.Exists(ctx, "present")
	if !ok {
		t.Error("expected Exists=true for present key")
	}
	ok, _ = c.Exists(ctx, "absent")
	if ok {
		t.Error("expected Exists=false for absent key")
	}
}

func TestShardedMemoryCache_DeletePattern(t *testing.T) {
	c := NewShardedMemoryCache(256, time.Minute, 16)
	ctx := context.Background()

	// Scatter keys across shards
	for i := 0; i < 50; i++ {
		c.Set(ctx, fmt.Sprintf("tenant:1:entity:%d", i), i, 0)
		c.Set(ctx, fmt.Sprintf("tenant:2:entity:%d", i), i, 0)
	}

	// Delete only tenant:1 keys
	c.DeletePattern(ctx, "tenant:1:*")

	// All tenant:1 keys should be gone
	for i := 0; i < 50; i++ {
		_, err := c.Get(ctx, fmt.Sprintf("tenant:1:entity:%d", i))
		if err == nil {
			t.Errorf("tenant:1 key %d should have been deleted", i)
		}
	}

	// Tenant:2 keys should remain
	for i := 0; i < 50; i++ {
		_, err := c.Get(ctx, fmt.Sprintf("tenant:2:entity:%d", i))
		if err != nil {
			t.Errorf("tenant:2 key %d should still exist", i)
		}
	}
}

func TestShardedMemoryCache_Close(t *testing.T) {
	c := NewShardedMemoryCache(100, time.Minute, 4)
	ctx := context.Background()

	c.Set(ctx, "a", 1, 0)
	c.Set(ctx, "b", 2, 0)
	c.Close()

	_, err := c.Get(ctx, "a")
	if err == nil {
		t.Error("expected miss after Close")
	}
}

func TestShardedMemoryCache_ShardCount(t *testing.T) {
	tests := []struct {
		input int
		want  int
	}{
		{0, 16},  // default
		{-1, 16}, // default
		{1, 1},
		{2, 2},
		{3, 4},   // rounded up
		{5, 8},   // rounded up
		{16, 16},
		{17, 32}, // rounded up
	}
	for _, tt := range tests {
		c := NewShardedMemoryCache(100, time.Minute, tt.input)
		if c.ShardCount() != tt.want {
			t.Errorf("ShardCount(%d) = %d, want %d", tt.input, c.ShardCount(), tt.want)
		}
	}
}

func TestShardedMemoryCache_Distribution(t *testing.T) {
	// Verify keys distribute across shards (not all in one)
	c := NewShardedMemoryCache(1000, time.Minute, 16)
	ctx := context.Background()

	for i := 0; i < 100; i++ {
		c.Set(ctx, fmt.Sprintf("key-%d", i), i, 0)
	}

	// Count keys per shard by checking each shard's Len
	nonEmpty := 0
	for _, shard := range c.shards {
		shard.mu.RLock()
		n := shard.cache.Len()
		shard.mu.RUnlock()
		if n > 0 {
			nonEmpty++
		}
	}

	// With 100 keys and 16 shards, at least half should have keys
	// (FNV distributes well enough for this)
	if nonEmpty < 8 {
		t.Errorf("poor distribution: only %d/16 shards have keys", nonEmpty)
	}
}

func TestShardedMemoryCache_ConcurrentAccess(t *testing.T) {
	c := NewShardedMemoryCache(10000, time.Minute, 16)
	ctx := context.Background()
	var wg sync.WaitGroup
	const goroutines = 32
	const opsPerGoroutine = 500

	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for i := 0; i < opsPerGoroutine; i++ {
				key := fmt.Sprintf("g%d-k%d", id, i)
				c.Set(ctx, key, i, 0)
				c.Get(ctx, key)
				c.Exists(ctx, key)
				if i%10 == 0 {
					c.Delete(ctx, key)
				}
			}
		}(g)
	}
	wg.Wait()

	// No panics or races = pass. Run with -race to verify.
}

func TestShardedMemoryCache_ImplementsInterface(t *testing.T) {
	// Compile-time check that ShardedMemoryCache satisfies Cache
	var _ Cache = (*ShardedMemoryCache)(nil)
}

func TestNextPow2(t *testing.T) {
	tests := []struct {
		in, want int
	}{
		{0, 1}, {1, 1}, {2, 2}, {3, 4}, {4, 4}, {5, 8},
		{7, 8}, {8, 8}, {9, 16}, {15, 16}, {16, 16},
		{17, 32}, {31, 32}, {32, 32}, {33, 64},
	}
	for _, tt := range tests {
		got := nextPow2(tt.in)
		if got != tt.want {
			t.Errorf("nextPow2(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
}
