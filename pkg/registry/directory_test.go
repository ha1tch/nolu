// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package registry_test

// directory_test.go — tests for TenantDirectory and the locate endpoint.

import (
	"context"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
)

func newDirTestRegistry(t *testing.T) (registry.Registry, *registry.TenantDirectory, context.CancelFunc) {
	t.Helper()
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	dir := registry.NewTenantDirectory(reg, 30*time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	if err := dir.Start(ctx); err != nil {
		cancel()
		t.Fatalf("dir.Start: %v", err)
	}
	return reg, dir, cancel
}

func namedRef(instanceURL, tenantName string, tenantID uint16, localID int) identity.LocalRef {
	return identity.LocalRef{
		InstanceURL: instanceURL,
		TenantName:  tenantName,
		TenantID:    tenantID,
		EntityType:  "devices",
		LocalID:     localID,
	}
}

// ── Bootstrap (SeedDirectory) ─────────────────────────────────────────────────

func TestDirectory_Bootstrap(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)

	// Register entities before starting the directory.
	ref := namedRef("http://xolu-a:9090", "vendocorp", 1, 1)
	if _, err := reg.Register(context.Background(), "registry.test.local", "devices", ref); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Start directory after registration — must bootstrap from existing records.
	dir := registry.NewTenantDirectory(reg, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := dir.Start(ctx); err != nil {
		t.Fatalf("dir.Start: %v", err)
	}

	entry, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("expected vendocorp in directory after bootstrap")
	}
	if entry.InstanceURL != "http://xolu-a:9090" {
		t.Errorf("expected xolu-a, got %s", entry.InstanceURL)
	}
	if entry.EntityCount < 1 {
		t.Errorf("expected entity count >= 1, got %d", entry.EntityCount)
	}
}

// ── Live event tracking ───────────────────────────────────────────────────────

func TestDirectory_TrackRegistration(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	ref := namedRef("http://xolu-a:9090", "vendocorp", 1, 1)
	if _, err := reg.Register(context.Background(), "registry.test.local", "devices", ref); err != nil {
		t.Fatalf("register: %v", err)
	}

	// Give event a moment to propagate.
	time.Sleep(20 * time.Millisecond)

	entry, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("expected vendocorp in directory after registration event")
	}
	if entry.InstanceURL != "http://xolu-a:9090" {
		t.Errorf("instance: expected xolu-a, got %s", entry.InstanceURL)
	}
	if entry.TenantID != 1 {
		t.Errorf("tenant_id: expected 1, got %d", entry.TenantID)
	}
}

func TestDirectory_TrackMultipleEntities(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	for i := 1; i <= 5; i++ {
		ref := namedRef("http://xolu-a:9090", "vendocorp", 1, i)
		if _, err := reg.Register(context.Background(), "registry.test.local", "devices", ref); err != nil {
			t.Fatalf("register device %d: %v", i, err)
		}
	}
	time.Sleep(20 * time.Millisecond)

	entry, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("vendocorp not found")
	}
	if entry.EntityCount != 5 {
		t.Errorf("expected entity count=5, got %d", entry.EntityCount)
	}
}

func TestDirectory_TrackTransfer(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	// Register on xolu-a.
	ref := namedRef("http://xolu-a:9090", "vendocorp", 1, 1)
	rec, err := reg.Register(context.Background(), "registry.test.local", "devices", ref)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	// Transfer to xolu-b.
	toRef := namedRef("http://xolu-b:9091", "vendocorp", 2, 100)
	if _, err := reg.Transfer(context.Background(), registry.TransferRequest{
		GlobalID: rec.GlobalID,
		From:     ref,
		To:       toRef,
		Protocol: "TEST",
	}); err != nil {
		t.Fatalf("transfer: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	// Directory should now show xolu-b as primary.
	entry, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("vendocorp not found after transfer")
	}
	if entry.InstanceURL != "http://xolu-b:9091" {
		t.Errorf("after transfer: expected xolu-b, got %s", entry.InstanceURL)
	}
}

func TestDirectory_TrackRetire(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	ref := namedRef("http://xolu-a:9090", "vendocorp", 1, 1)
	rec, err := reg.Register(context.Background(), "registry.test.local", "devices", ref)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	if err := reg.Retire(context.Background(), rec.GlobalID, "end of life"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	entry, ok := dir.Locate("vendocorp")
	if ok && entry.EntityCount > 0 {
		t.Errorf("after retire: expected 0 entities, got %d", entry.EntityCount)
	}
}

func TestDirectory_MultipleTenants(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	tenants := []struct {
		name     string
		instance string
		id       uint16
	}{
		{"vendocorp", "http://xolu-a:9090", 1},
		{"retailchain", "http://xolu-a:9090", 2},
		{"serviceco", "http://xolu-b:9091", 1},
	}

	for i, tc := range tenants {
		ref := namedRef(tc.instance, tc.name, tc.id, i+1)
		if _, err := reg.Register(context.Background(), "registry.test.local", "devices", ref); err != nil {
			t.Fatalf("register %s: %v", tc.name, err)
		}
	}
	time.Sleep(30 * time.Millisecond)

	for _, tc := range tenants {
		entry, ok := dir.Locate(tc.name)
		if !ok {
			t.Errorf("tenant %s not found", tc.name)
			continue
		}
		if entry.InstanceURL != tc.instance {
			t.Errorf("tenant %s: expected %s, got %s", tc.name, tc.instance, entry.InstanceURL)
		}
	}
}

func TestDirectory_Locate_NotFound(t *testing.T) {
	_, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	_, ok := dir.Locate("nonexistent-tenant")
	if ok {
		t.Error("expected false for unknown tenant")
	}
}

func TestDirectory_InvalidateTenant(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	ref := namedRef("http://xolu-a:9090", "vendocorp", 1, 1)
	if _, err := reg.Register(context.Background(), "registry.test.local", "devices", ref); err != nil {
		t.Fatalf("register: %v", err)
	}
	time.Sleep(20 * time.Millisecond)

	before, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("vendocorp not found")
	}

	dir.InvalidateTenant("vendocorp")

	after, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("vendocorp not found after invalidation")
	}

	// StableUntil should be much sooner after invalidation.
	if !after.StableUntil.Before(before.StableUntil) {
		t.Errorf("expected StableUntil to shrink after invalidation: before=%v after=%v",
			before.StableUntil, after.StableUntil)
	}
}

func TestDirectory_ListAll(t *testing.T) {
	reg, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	for i, name := range []string{"acme", "beta", "gamma"} {
		ref := namedRef("http://xolu:9090", name, uint16(i+1), i+1)
		if _, err := reg.Register(context.Background(), "registry.test.local", "items", ref); err != nil {
			t.Fatalf("register %s: %v", name, err)
		}
	}
	time.Sleep(30 * time.Millisecond)

	all := dir.ListAll()
	if len(all) < 3 {
		t.Errorf("expected >= 3 entries, got %d", len(all))
	}
}

func TestDirectory_UpsertAndLocateOnInstance(t *testing.T) {
	_, dir, cancel := newDirTestRegistry(t)
	defer cancel()

	dir.Upsert("vendocorp", "http://xolu-a:9090", 1, 10)
	dir.Upsert("vendocorp", "http://xolu-b:9091", 1, 5)

	// Locate returns highest entity count.
	entry, ok := dir.Locate("vendocorp")
	if !ok {
		t.Fatal("vendocorp not found")
	}
	if entry.InstanceURL != "http://xolu-a:9090" {
		t.Errorf("expected xolu-a (more entities), got %s", entry.InstanceURL)
	}

	// LocateOnInstance returns specific instance.
	specific, ok := dir.LocateOnInstance("http://xolu-b:9091", "vendocorp")
	if !ok {
		t.Fatal("vendocorp not found on xolu-b")
	}
	if specific.EntityCount != 5 {
		t.Errorf("expected 5 entities on xolu-b, got %d", specific.EntityCount)
	}
}
