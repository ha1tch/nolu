// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"testing"
)

func TestCacheListPattern(t *testing.T) {
	tests := []struct {
		tenantID uint16
		entity   string
		want     string
	}{
		{0, "users", "users:list:*"},
		{1, "items", "0001:items:list:*"},
		{0xBEF0, "projects", "BEF0:projects:list:*"},
		{0xFFFF, "assets", "FFFF:assets:list:*"},
	}
	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := CacheListPattern(tt.tenantID, tt.entity)
			if got != tt.want {
				t.Errorf("CacheListPattern(%d, %q) = %q, want %q", tt.tenantID, tt.entity, got, tt.want)
			}
		})
	}
}

func TestRegistry_GetOrRegister(t *testing.T) {
	t.Run("auto-registers new tenant", func(t *testing.T) {
		r := NewRegistry()

		id, err := r.GetOrRegister(context.Background(), "acme")
		if err != nil {
			t.Fatalf("GetOrRegister(acme) error: %v", err)
		}
		if id == 0 {
			t.Error("Expected non-zero ID")
		}

		// Should now be findable via Lookup
		lookupID, ok := r.Lookup("acme")
		if !ok || lookupID != id {
			t.Errorf("Lookup(acme) = (%d, %v), want (%d, true)", lookupID, ok, id)
		}
	})

	t.Run("returns existing tenant", func(t *testing.T) {
		r := NewRegistry()
		r.Register(context.Background(), "acme", 42)

		id, err := r.GetOrRegister(context.Background(), "acme")
		if err != nil {
			t.Fatalf("GetOrRegister error: %v", err)
		}
		if id != 42 {
			t.Errorf("Expected ID 42, got %d", id)
		}
	})

	t.Run("rejects empty name", func(t *testing.T) {
		r := NewRegistry()

		_, err := r.GetOrRegister(context.Background(), "")
		if err == nil {
			t.Error("Expected error for empty name")
		}
	})

	t.Run("sequential auto-assignment", func(t *testing.T) {
		r := NewRegistry()

		id1, _ := r.GetOrRegister(context.Background(), "alpha")
		id2, _ := r.GetOrRegister(context.Background(), "beta")
		id3, _ := r.GetOrRegister(context.Background(), "gamma")

		if id2 != id1+1 || id3 != id2+1 {
			t.Errorf("Expected sequential IDs, got %d, %d, %d", id1, id2, id3)
		}
	})

	t.Run("auto-assignment skips manually registered IDs", func(t *testing.T) {
		r := NewRegistry()

		// Manually register at ID 5
		r.Register(context.Background(), "manual", 5)

		// Auto-register should start above 5
		id, _ := r.GetOrRegister(context.Background(), "auto1")
		if id <= 5 {
			t.Errorf("Expected auto ID > 5, got %d", id)
		}
	})

	t.Run("idempotent on repeated calls", func(t *testing.T) {
		r := NewRegistry()

		id1, _ := r.GetOrRegister(context.Background(), "acme")
		id2, _ := r.GetOrRegister(context.Background(), "acme")
		id3, _ := r.GetOrRegister(context.Background(), "acme")

		if id1 != id2 || id2 != id3 {
			t.Errorf("Expected same ID, got %d, %d, %d", id1, id2, id3)
		}
	})
}
