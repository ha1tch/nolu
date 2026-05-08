// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package cache

import (
	"context"
	"hash/fnv"
	"time"
)

const defaultShardCount = 16

// ShardedMemoryCache distributes keys across multiple MemoryCache shards
// to reduce lock contention under concurrent access. Each shard has its
// own LRU and its own mutex, so operations on keys that hash to different
// shards never contend.
//
// The total capacity is size (spread across shards), and TTL is global.
type ShardedMemoryCache struct {
	shards []*MemoryCache
	count  uint32
}

// NewShardedMemoryCache creates a sharded in-memory cache.
// size is the total capacity (divided across shards). shardCount must be
// a power of two; if zero or invalid, defaults to 16.
func NewShardedMemoryCache(size int, ttl time.Duration, shardCount int) *ShardedMemoryCache {
	if shardCount <= 0 {
		shardCount = defaultShardCount
	}
	// Round up to next power of two for fast modulo via bitmask
	shardCount = nextPow2(shardCount)

	perShard := size / shardCount
	if perShard < 1 {
		perShard = 1
	}

	shards := make([]*MemoryCache, shardCount)
	for i := range shards {
		shards[i] = NewMemoryCache(perShard, ttl)
	}

	return &ShardedMemoryCache{
		shards: shards,
		count:  uint32(shardCount),
	}
}

// shard returns the MemoryCache responsible for the given key.
func (s *ShardedMemoryCache) shard(key string) *MemoryCache {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return s.shards[h.Sum32()&(s.count-1)]
}

// Get retrieves a value from the appropriate shard.
func (s *ShardedMemoryCache) Get(ctx context.Context, key string) (interface{}, error) {
	return s.shard(key).Get(ctx, key)
}

// Set stores a value in the appropriate shard.
func (s *ShardedMemoryCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	return s.shard(key).Set(ctx, key, value, ttl)
}

// Delete removes a key from the appropriate shard.
func (s *ShardedMemoryCache) Delete(ctx context.Context, key string) error {
	return s.shard(key).Delete(ctx, key)
}

// DeletePattern removes all keys matching a prefix pattern across all shards.
func (s *ShardedMemoryCache) DeletePattern(ctx context.Context, pattern string) error {
	for _, shard := range s.shards {
		if err := shard.DeletePattern(ctx, pattern); err != nil {
			return err
		}
	}
	return nil
}

// Exists checks if a key exists in the appropriate shard.
func (s *ShardedMemoryCache) Exists(ctx context.Context, key string) (bool, error) {
	return s.shard(key).Exists(ctx, key)
}

// Close purges all shards.
func (s *ShardedMemoryCache) Close() error {
	for _, shard := range s.shards {
		if err := shard.Close(); err != nil {
			return err
		}
	}
	return nil
}

// ShardCount returns the number of shards (useful for diagnostics).
func (s *ShardedMemoryCache) ShardCount() int {
	return int(s.count)
}

// nextPow2 returns the smallest power of two >= n.
func nextPow2(n int) int {
	if n <= 1 {
		return 1
	}
	n--
	n |= n >> 1
	n |= n >> 2
	n |= n >> 4
	n |= n >> 8
	n |= n >> 16
	return n + 1
}
