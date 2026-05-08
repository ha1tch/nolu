// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package cache

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

// startMiniRedis starts an in-process Redis server and returns a *RedisCache
// connected to it plus a cleanup function.
func startMiniRedis(t *testing.T) (*RedisCache, *miniredis.Miniredis) {
	t.Helper()
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis.Run: %v", err)
	}
	t.Cleanup(mr.Close)

	rc, err := NewRedisCache(mr.Host(), mr.Server().Addr().Port, time.Minute, 5, 1)
	if err != nil {
		t.Fatalf("NewRedisCache: %v", err)
	}
	t.Cleanup(func() { _ = rc.Close() })
	return rc, mr
}

// ---------------------------------------------------------------------------
// Get and Set
// ---------------------------------------------------------------------------

func TestRedisCache_SetGet_String(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	if err := rc.Set(ctx, "key1", "hello", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	val, err := rc.Get(ctx, "key1")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != "hello" {
		t.Errorf("Get: got %v, want %q", val, "hello")
	}
}

func TestRedisCache_SetGet_Map(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	m := map[string]interface{}{"a": float64(1), "b": "two"}
	if err := rc.Set(ctx, "mapkey", m, time.Minute); err != nil {
		t.Fatalf("Set map: %v", err)
	}
	val, err := rc.Get(ctx, "mapkey")
	if err != nil {
		t.Fatalf("Get map: %v", err)
	}
	got, ok := val.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", val)
	}
	if got["b"] != "two" {
		t.Errorf("map field b: got %v", got["b"])
	}
}

func TestRedisCache_Get_MissingKey(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	_, err := rc.Get(ctx, "nosuchkey")
	if err == nil {
		t.Fatal("expected error for missing key")
	}
}

func TestRedisCache_Set_UsesCacheTTL_WhenZero(t *testing.T) {
	rc, mr := startMiniRedis(t)
	ctx := context.Background()

	// TTL=0 means "use the cache's default TTL" (1 minute in this test).
	if err := rc.Set(ctx, "ttlkey", "val", 0); err != nil {
		t.Fatalf("Set: %v", err)
	}
	ttl := mr.TTL("ttlkey")
	if ttl <= 0 {
		t.Errorf("TTL should be positive when 0 passed, got %v", ttl)
	}
}

// ---------------------------------------------------------------------------
// Delete
// ---------------------------------------------------------------------------

func TestRedisCache_Delete(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	_ = rc.Set(ctx, "delkey", "value", time.Minute)
	if err := rc.Delete(ctx, "delkey"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	_, err := rc.Get(ctx, "delkey")
	if err == nil {
		t.Fatal("expected error after delete")
	}
}

func TestRedisCache_Delete_NonExistent(t *testing.T) {
	rc, _ := startMiniRedis(t)
	// Deleting a non-existent key should not error.
	if err := rc.Delete(context.Background(), "ghost"); err != nil {
		t.Errorf("Delete non-existent: got error %v", err)
	}
}

// ---------------------------------------------------------------------------
// DeletePattern
// ---------------------------------------------------------------------------

func TestRedisCache_DeletePattern_Mini(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	for _, k := range []string{"ns:a", "ns:b", "ns:c", "other:x"} {
		_ = rc.Set(ctx, k, k, time.Minute)
	}

	if err := rc.DeletePattern(ctx, "ns:"); err != nil {
		t.Fatalf("DeletePattern: %v", err)
	}

	// ns: keys should be gone.
	for _, k := range []string{"ns:a", "ns:b", "ns:c"} {
		if _, err := rc.Get(ctx, k); err == nil {
			t.Errorf("key %q should have been deleted", k)
		}
	}
	// other:x should remain.
	if _, err := rc.Get(ctx, "other:x"); err != nil {
		t.Errorf("key other:x should not have been deleted: %v", err)
	}
}

func TestRedisCache_DeletePattern_NoMatches(t *testing.T) {
	rc, _ := startMiniRedis(t)
	// Should not error when no keys match.
	if err := rc.DeletePattern(context.Background(), "nothing:"); err != nil {
		t.Errorf("DeletePattern no-match: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Exists
// ---------------------------------------------------------------------------

func TestRedisCache_Exists_True(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	_ = rc.Set(ctx, "present", "yes", time.Minute)
	ok, err := rc.Exists(ctx, "present")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if !ok {
		t.Error("Exists should return true for existing key")
	}
}

func TestRedisCache_Exists_False(t *testing.T) {
	rc, _ := startMiniRedis(t)
	ctx := context.Background()

	ok, err := rc.Exists(ctx, "absent")
	if err != nil {
		t.Fatalf("Exists: %v", err)
	}
	if ok {
		t.Error("Exists should return false for missing key")
	}
}

// ---------------------------------------------------------------------------
// Close
// ---------------------------------------------------------------------------

func TestRedisCache_Close_Mini(t *testing.T) {
	mr, err := miniredis.Run()
	if err != nil {
		t.Fatalf("miniredis: %v", err)
	}
	defer mr.Close()

	rc, err := NewRedisCache(mr.Host(), mr.Server().Addr().Port, time.Minute, 0, 0)
	if err != nil {
		t.Fatalf("NewRedisCache: %v", err)
	}
	// Close should not error.
	if err := rc.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

// ---------------------------------------------------------------------------
// NewRedisCache — connection failure
// ---------------------------------------------------------------------------

func TestNewRedisCache_ConnectionFailure(t *testing.T) {
	// Port 1 is almost certainly not listening.
	_, err := NewRedisCache("127.0.0.1", 1, time.Minute, 1, 1)
	if err == nil {
		t.Fatal("expected error connecting to non-listening port")
	}
}
