// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/go-redis/redis/v8"
	lru "github.com/hashicorp/golang-lru/v2/expirable"
)

// Cache interface defines caching operations.
//
// Two implementations are provided:
//   - MemoryCache: Simple in-process LRU cache with global TTL
//   - RedisCache: Distributed cache with per-item TTL support
//
// Use MemoryCache for development and single-instance deployments.
// Use RedisCache for horizontal scaling or when per-item TTL is needed.
type Cache interface {
	Get(ctx context.Context, key string) (interface{}, error)
	Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error
	Delete(ctx context.Context, key string) error
	DeletePattern(ctx context.Context, pattern string) error
	Exists(ctx context.Context, key string) (bool, error)
	Close() error
}

// MemoryCache implements an in-memory LRU cache.
// TTL is global, set at construction time via NewMemoryCache.
// For per-item TTL, use RedisCache instead.
type MemoryCache struct {
	cache *lru.LRU[string, interface{}]
	mu    sync.RWMutex
}

// NewMemoryCache creates a new in-memory cache
func NewMemoryCache(size int, ttl time.Duration) *MemoryCache {
	return &MemoryCache{
		cache: lru.NewLRU[string, interface{}](size, nil, ttl),
	}
}

// Get retrieves a value from the cache
func (m *MemoryCache) Get(ctx context.Context, key string) (interface{}, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	val, ok := m.cache.Get(key)
	if !ok {
		return nil, fmt.Errorf("key not found")
	}
	return val, nil
}

// Set stores a value in the cache.
// Note: ttl parameter is ignored; global TTL from NewMemoryCache is used.
// For per-item TTL, use RedisCache.
func (m *MemoryCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.cache.Add(key, value)
	return nil
}

// Delete removes a key from the cache
func (m *MemoryCache) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.cache.Remove(key)
	return nil
}

// DeletePattern removes all keys matching a pattern
func (m *MemoryCache) DeletePattern(ctx context.Context, pattern string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	prefix := strings.TrimSuffix(pattern, "*")
	keys := m.cache.Keys()
	for _, key := range keys {
		if strings.HasPrefix(key, prefix) {
			m.cache.Remove(key)
		}
	}
	return nil
}

// Exists checks if a key exists in the cache
func (m *MemoryCache) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	
	return m.cache.Contains(key), nil
}

// Close closes the cache (no-op for memory cache)
func (m *MemoryCache) Close() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	
	m.cache.Purge()
	return nil
}

// RedisCache implements a Redis-backed cache
type RedisCache struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisCache creates a new Redis cache.
// poolSize and minIdleConns control the connection pool; zero values use
// defaults of 50 and 10 respectively.
func NewRedisCache(host string, port int, ttl time.Duration, poolSize int, minIdleConns int) (*RedisCache, error) {
	if poolSize <= 0 {
		poolSize = 50
	}
	if minIdleConns <= 0 {
		minIdleConns = 10
	}
	client := redis.NewClient(&redis.Options{
		Addr:         fmt.Sprintf("%s:%d", host, port),
		PoolSize:     poolSize,
		MinIdleConns: minIdleConns,
	})
	
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("failed to connect to Redis: %w", err)
	}
	
	return &RedisCache{
		client: client,
		ttl:    ttl,
	}, nil
}

// Get retrieves a value from Redis
func (r *RedisCache) Get(ctx context.Context, key string) (interface{}, error) {
	val, err := r.client.Get(ctx, key).Result()
	if err == redis.Nil {
		return nil, fmt.Errorf("key not found")
	}
	if err != nil {
		return nil, err
	}
	
	var result interface{}
	if err := json.Unmarshal([]byte(val), &result); err != nil {
		return nil, err
	}
	return result, nil
}

// Set stores a value in Redis
func (r *RedisCache) Set(ctx context.Context, key string, value interface{}, ttl time.Duration) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	
	if ttl == 0 {
		ttl = r.ttl
	}
	
	return r.client.Set(ctx, key, data, ttl).Err()
}

// Delete removes a key from Redis
func (r *RedisCache) Delete(ctx context.Context, key string) error {
	return r.client.Del(ctx, key).Err()
}

// DeletePattern removes all keys matching a pattern
func (r *RedisCache) DeletePattern(ctx context.Context, pattern string) error {
	var cursor uint64
	for {
		keys, nextCursor, err := r.client.Scan(ctx, cursor, pattern+"*", 100).Result()
		if err != nil {
			return err
		}
		
		if len(keys) > 0 {
			if err := r.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}

// Exists checks if a key exists in Redis
func (r *RedisCache) Exists(ctx context.Context, key string) (bool, error) {
	count, err := r.client.Exists(ctx, key).Result()
	return count > 0, err
}

// Close closes the Redis connection
func (r *RedisCache) Close() error {
	return r.client.Close()
}
