// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"fmt"
	"context"
	"testing"
)

func TestNodeID(t *testing.T) {
	tests := []struct {
		tenantID uint16
		entity   string
		id       int
		want     string
	}{
		{0, "users", 1, "users:1"},
		{0, "items", 42, "items:42"},
		{1, "users", 1, "0001@users:1"},
		{0xBEF0, "projects", 7, "BEF0@projects:7"},
		{0xFFFF, "a", 0, "FFFF@a:0"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := NodeID(tt.tenantID, tt.entity, tt.id)
			if got != tt.want {
				t.Errorf("NodeID(%d, %q, %d) = %q, want %q", tt.tenantID, tt.entity, tt.id, got, tt.want)
			}
		})
	}
}

func TestCacheKey(t *testing.T) {
	tests := []struct {
		tenantID uint16
		entity   string
		id       int
		want     string
	}{
		{0, "users", 1, "users:1"},
		{1, "users", 1, "0001:users:1"},
		{0xBEF0, "items", 99, "BEF0:items:99"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := CacheKey(tt.tenantID, tt.entity, tt.id)
			if got != tt.want {
				t.Errorf("CacheKey(%d, %q, %d) = %q, want %q", tt.tenantID, tt.entity, tt.id, got, tt.want)
			}
		})
	}
}

func TestCachePattern(t *testing.T) {
	if got := CachePattern(0, "users"); got != "users:*" {
		t.Errorf("CachePattern(0, users) = %q", got)
	}
	if got := CachePattern(0xBEF0, "users"); got != "BEF0:users:*" {
		t.Errorf("CachePattern(BEF0, users) = %q", got)
	}
}

func TestCacheTenantPattern(t *testing.T) {
	if got := CacheTenantPattern(0); got != "*" {
		t.Errorf("CacheTenantPattern(0) = %q", got)
	}
	if got := CacheTenantPattern(0xBEF0); got != "BEF0:*" {
		t.Errorf("CacheTenantPattern(BEF0) = %q", got)
	}
}

func TestRegistry_Register(t *testing.T) {
	r := NewRegistry()

	if err := r.Register(context.Background(), "acme", 0x0001); err != nil {
		t.Fatalf("Register acme: %v", err)
	}
	if err := r.Register(context.Background(), "globex", 0x0002); err != nil {
		t.Fatalf("Register globex: %v", err)
	}

	// Lookup
	id, ok := r.Lookup("acme")
	if !ok || id != 0x0001 {
		t.Errorf("Lookup(acme) = (%d, %v), want (1, true)", id, ok)
	}

	name, ok := r.Name(0x0002)
	if !ok || name != "globex" {
		t.Errorf("Name(2) = (%q, %v), want (globex, true)", name, ok)
	}

	// Not found
	_, ok = r.Lookup("nonexistent")
	if ok {
		t.Error("Lookup(nonexistent) should return false")
	}

	// Count
	if r.Count() != 2 {
		t.Errorf("Count = %d, want 2", r.Count())
	}
}

func TestRegistry_DuplicatePrevention(t *testing.T) {
	r := NewRegistry()
	r.Register(context.Background(), "acme", 0x0001)

	// Duplicate name
	if err := r.Register(context.Background(), "acme", 0x0099); err == nil {
		t.Error("duplicate name should fail")
	}

	// Duplicate ID
	if err := r.Register(context.Background(), "other", 0x0001); err == nil {
		t.Error("duplicate ID should fail")
	}

	// ID 0 reserved
	if err := r.Register(context.Background(), "zero", 0); err == nil {
		t.Error("ID 0 should be rejected")
	}

	// Empty name
	if err := r.Register(context.Background(), "", 0x0099); err == nil {
		t.Error("empty name should be rejected")
	}
}

func TestRegistry_List(t *testing.T) {
	r := NewRegistry()
	r.Register(context.Background(), "a", 1)
	r.Register(context.Background(), "b", 2)
	r.Register(context.Background(), "c", 3)

	list := r.List()
	if len(list) != 3 {
		t.Errorf("List len = %d, want 3", len(list))
	}
	for name, id := range list {
		switch name {
		case "a":
			if id != 1 {
				t.Errorf("a => %d", id)
			}
		case "b":
			if id != 2 {
				t.Errorf("b => %d", id)
			}
		case "c":
			if id != 3 {
				t.Errorf("c => %d", id)
			}
		default:
			t.Errorf("unexpected tenant %q", name)
		}
	}
}

func TestRegistry_ConcurrentAccess(t *testing.T) {
	r := NewRegistry()

	// Concurrent registration (different IDs)
	done := make(chan error, 100)
	for i := 1; i <= 100; i++ {
		go func(n int) {
			done <- r.Register(context.Background(), fmt.Sprintf("tenant-%d", n), uint16(n))
		}(i)
	}

	for i := 0; i < 100; i++ {
		if err := <-done; err != nil {
			t.Errorf("concurrent register: %v", err)
		}
	}

	if r.Count() != 100 {
		t.Errorf("Count = %d after 100 registrations", r.Count())
	}
}
