// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package e2e

// TestE2E_XoluHotswapManager_Durability verifies that XoluHotswapManager
// survives a simulated restart: hotswap records written by the first instance
// are visible and in the correct state when a second instance starts.
//
// Requires docker-up (xolu-vendocorp on localhost:9090).

import (
	"context"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/hotswap"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
)

func TestE2E_XoluHotswapManager_Durability(t *testing.T) {
	ctx := context.Background()
	requireXolu(t, "vendocorp", vendoURL)
	requireXolu(t, "retailchain", retailURL)

	// Use vendocorp as the xolu backing store for nolu's registry and hotswap.
	bus := events.NewMemoryBus()
	reg, err := registry.NewXoluRegistry(ctx, vendoURL, "registry.e2e-hs.local", bus)
	if err != nil {
		t.Fatalf("NewXoluRegistry: %v", err)
	}

	// ── Instance 1: create a hotswap ─────────────────────────────────────────
	t.Log("step 1: create XoluHotswapManager (instance 1)")
	mgr1, err := hotswap.NewXoluHotswapManager(ctx, vendoURL, reg, bus, nil)
	if err != nil {
		t.Fatalf("NewXoluHotswapManager: %v", err)
	}

	// Register a device on vendocorp so the hotswap has something to transfer.
	deviceRef := identity.LocalRef{
		InstanceURL: vendoURL,
		EntityType:  "devices",
		LocalID:     7777,
	}
	recReg, err := reg.Register(ctx, "registry.e2e-hs.local", "devices", deviceRef)
	if err != nil {
		t.Fatalf("register device: %v", err)
	}
	t.Logf("  registered device: %s", recReg.GlobalID)

	// Request a hotswap from vendocorp → retailchain.
	// AutoAdvance=false so it stays in PREPARING until we Confirm.
	h, err := mgr1.Request(ctx,
		hotswap.InstanceRef{
			InstanceURL: vendoURL,
			TenantName:  "vendocorp",
			TenantID:    0,
		},
		hotswap.InstanceRef{
			InstanceURL: retailURL,
			TenantName:  "vendocorp",
			TenantID:    0,
		},
		hotswap.HotswapOptions{
			AutoAdvance:        false,
			QuiesceTimeout:     5 * time.Second,
			TimestampedHistory: true,
		},
	)
	if err != nil {
		t.Fatalf("hotswap request: %v", err)
	}
	t.Logf("  hotswap %s: state=%s entities=%d", h.ID, h.State, h.EntityCount)

	if h.State != hotswap.StatePreparing {
		t.Errorf("expected preparing, got %s", h.State)
	}

	hotswapID := h.ID

	// ── Instance 2: simulate restart ─────────────────────────────────────────
	t.Log("step 2: create new XoluHotswapManager (simulated restart)")
	time.Sleep(200 * time.Millisecond) // let async writes settle

	mgr2, err := hotswap.NewXoluHotswapManager(ctx, vendoURL, reg, bus, nil)
	if err != nil {
		t.Fatalf("NewXoluHotswapManager (restart): %v", err)
	}

	// The hotswap should be visible from mgr2.
	h2, err := mgr2.Get(ctx, hotswapID)
	if err != nil {
		t.Fatalf("mgr2.Get: %v", err)
	}
	if h2.State != hotswap.StatePreparing {
		t.Errorf("after restart: expected preparing, got %s", h2.State)
	}
	if h2.Source.InstanceURL != vendoURL {
		t.Errorf("after restart: source URL mismatch: %s", h2.Source.InstanceURL)
	}
	t.Logf("  after restart: hotswap %s state=%s ✓", h2.ID, h2.State)

	// ── List by state ─────────────────────────────────────────────────────────
	t.Log("step 3: list by state from mgr2")
	state := hotswap.StatePreparing
	list, err := mgr2.List(ctx, &state)
	if err != nil {
		t.Fatalf("mgr2.List: %v", err)
	}
	found := false
	for _, h := range list {
		if h.ID == hotswapID {
			found = true
		}
	}
	if !found {
		t.Errorf("hotswap %s not found in List(preparing)", hotswapID)
	}
	t.Logf("  List(preparing): %d records, target found ✓", len(list))

	// ── Abort from mgr2 ───────────────────────────────────────────────────────
	t.Log("step 4: abort hotswap from mgr2 (operator action on restarted instance)")
	aborted, err := mgr2.Abort(ctx, hotswapID, "e2e test cleanup")
	if err != nil {
		t.Fatalf("mgr2.Abort: %v", err)
	}
	if aborted.State != hotswap.StateRollingBack && aborted.State != hotswap.StateFailed {
		t.Errorf("expected rolling_back or failed, got %s", aborted.State)
	}
	t.Logf("  aborted: state=%s ✓", aborted.State)

	// Allow rollback goroutine to complete.
	time.Sleep(500 * time.Millisecond)

	// Verify final state from a third Get.
	final, err := mgr2.Get(ctx, hotswapID)
	if err != nil {
		t.Fatalf("final Get: %v", err)
	}
	if final.State != hotswap.StateFailed {
		t.Errorf("expected failed after rollback, got %s", final.State)
	}
	t.Logf("  final state: %s ✓", final.State)

	t.Log("TestE2E_XoluHotswapManager_Durability PASSED")
}
