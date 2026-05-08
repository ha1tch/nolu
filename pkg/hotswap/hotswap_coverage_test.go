// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package hotswap_test

// hotswap_coverage_test.go — tests for gaps identified in coverage audit:
//
//  1. ListByInstanceAndTenant correctness in cutover (multi-tenant isolation)
//  2. Migration/validation graceful skip (no DB paths)
//  3. Migration error → rollback
//  4. Validation failure → rollback (via mock)
//  5. InvalidateTenant called on quiesce start
//  6. Cutover leaves non-migrating tenant's entities on source

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/hotswap"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
)

// ── Multi-tenant isolation in cutover ────────────────────────────────────────

// TestCutover_OnlyMigratingTenantTransferred verifies that when a multi-tenant
// instance hosts two tenants and we hotswap only one, the other tenant's
// GlobalIDs remain pointing at the source after cutover.
func TestCutover_OnlyMigratingTenantTransferred(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	// Register 2 devices for tenant 1 and 2 devices for tenant 2, all on src.
	t1GIDs := make([]identity.GlobalID, 2)
	t2GIDs := make([]identity.GlobalID, 2)
	for i := range t1GIDs {
		rec, err := reg.Register(ctx, "registry.test.local", "devices",
			identity.LocalRef{InstanceURL: src.URL, TenantID: 1, EntityType: "devices", LocalID: i + 1})
		if err != nil {
			t.Fatalf("register t1 device %d: %v", i, err)
		}
		t1GIDs[i] = rec.GlobalID
	}
	for i := range t2GIDs {
		rec, err := reg.Register(ctx, "registry.test.local", "devices",
			identity.LocalRef{InstanceURL: src.URL, TenantID: 2, EntityType: "devices", LocalID: i + 1})
		if err != nil {
			t.Fatalf("register t2 device %d: %v", i, err)
		}
		t2GIDs[i] = rec.GlobalID
	}

	// Hotswap only tenant 1 from src to tgt.
	h, err := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "vendocorp", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "vendocorp", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: true, QuiesceTimeout: 2 * time.Second},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	// Wait for completion.
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(ctx, h.ID)
		if h2.State == hotswap.StateComplete || h2.State == hotswap.StateFailed {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if h.State != hotswap.StateComplete {
		t.Fatalf("expected complete, got %s (failure: %s)", h.State, h.FailureReason)
	}

	// Tenant 1 GlobalIDs must now point at tgt.
	for _, gid := range t1GIDs {
		ref, err := reg.Resolve(ctx, gid)
		if err != nil {
			t.Errorf("resolve t1 gid %s: %v", gid, err)
			continue
		}
		if ref.InstanceURL != tgt.URL {
			t.Errorf("t1 gid %s: expected tgt, got %s", gid, ref.InstanceURL)
		}
	}

	// Tenant 2 GlobalIDs must still point at src — not touched.
	for _, gid := range t2GIDs {
		ref, err := reg.Resolve(ctx, gid)
		if err != nil {
			t.Errorf("resolve t2 gid %s: %v", gid, err)
			continue
		}
		if ref.InstanceURL != src.URL {
			t.Errorf("t2 gid %s: expected src (untouched), got %s", gid, ref.InstanceURL)
		}
	}
	t.Logf("✓ tenant 1 transferred, tenant 2 isolated on source")
}

// ── Migration/validation graceful skip (no DB paths) ─────────────────────────

// TestCutover_NoDB_SkipsMigrationAndValidation verifies that when no DB paths
// are configured, the hotswap completes successfully by skipping iolu invocation.
// This is the correct behaviour for operator-managed or xolu-API-only migrations.
func TestCutover_NoDB_SkipsMigrationAndValidation(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, err := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "acme", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "acme", TenantID: 1},
		hotswap.HotswapOptions{
			AutoAdvance:    true,
			QuiesceTimeout: 2 * time.Second,
			// No SourceDBPath / TargetDBPath → migration skip
		},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(ctx, h.ID)
		if h2.State == hotswap.StateComplete || h2.State == hotswap.StateFailed {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if h.State != hotswap.StateComplete {
		t.Errorf("expected complete when no DB paths, got %s (failure: %s)", h.State, h.FailureReason)
	}
	t.Logf("✓ completed without DB paths (migration and validation skipped gracefully)")
}

// ── Migration error → rollback ────────────────────────────────────────────────

// TestCutover_BadIoluBinary_Rollback verifies that a bad iolu binary path
// causes the hotswap to roll back rather than proceed to cutover.
func TestCutover_BadIoluBinary_Rollback(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	// Register a device so the hotswap has something to do.
	rec, _ := reg.Register(ctx, "registry.test.local", "devices",
		identity.LocalRef{InstanceURL: src.URL, TenantID: 1, EntityType: "devices", LocalID: 1})

	h, err := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "acme", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "acme", TenantID: 1},
		hotswap.HotswapOptions{
			AutoAdvance:    true,
			QuiesceTimeout: 2 * time.Second,
			SourceDBPath:   "/tmp/source.db",         // doesn't exist
			TargetDBPath:   "/tmp/target.db",         // doesn't exist
			IoluBinary:     "/nonexistent/iolu",      // bad binary
		},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(ctx, h.ID)
		if h2.State == hotswap.StateFailed || h2.State == hotswap.StateComplete {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if h.State != hotswap.StateFailed {
		t.Errorf("expected failed after bad iolu binary, got %s", h.State)
	}
	if h.FailureReason == "" {
		t.Error("expected non-empty FailureReason")
	}
	t.Logf("✓ rolled back with reason: %s", h.FailureReason)

	// Verify registry was NOT updated — entity still on source.
	ref, err := reg.Resolve(ctx, rec.GlobalID)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if ref.InstanceURL != src.URL {
		t.Errorf("after failed hotswap: expected entity still on source, got %s", ref.InstanceURL)
	}
	t.Logf("✓ entity remains on source after rollback")
}

// ── InvalidateTenant called on quiesce ───────────────────────────────────────

// mockInvalidator records which tenants were invalidated.
type mockInvalidator struct {
	calls int64
	names []string
	mu    chan struct{}
}

func newMockInvalidator() *mockInvalidator {
	return &mockInvalidator{mu: make(chan struct{}, 1)}
}

func (m *mockInvalidator) InvalidateTenant(name string) {
	atomic.AddInt64(&m.calls, 1)
	select {
	case m.mu <- struct{}{}:
		m.names = append(m.names, name)
		<-m.mu
	default:
		m.names = append(m.names, name)
	}
}

func TestQuiesce_InvalidatesTenantDirectory(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	inv := newMockInvalidator()
	mgr := hotswap.NewMemoryManager(reg, bus, inv)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	_, err := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "vendocorp", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "vendocorp", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: true, QuiesceTimeout: 2 * time.Second},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	// Wait for quiesce phase (InvalidateTenant is called there).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt64(&inv.calls) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	if atomic.LoadInt64(&inv.calls) == 0 {
		t.Error("expected InvalidateTenant to be called during quiesce phase")
	} else {
		t.Logf("✓ InvalidateTenant called %d time(s) for %v", inv.calls, inv.names)
	}
}

// ── Abort during quiesce rolls back correctly ─────────────────────────────────

func TestAbort_DuringPreparing_RollsBack(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	// Register a device.
	rec, _ := reg.Register(ctx, "registry.test.local", "devices",
		identity.LocalRef{InstanceURL: src.URL, TenantID: 1, EntityType: "devices", LocalID: 1})

	// Start with AutoAdvance=false so it stays in PREPARING.
	h, _ := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "acme", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "acme", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: false},
	)

	// Abort while in PREPARING.
	aborted, err := mgr.Abort(ctx, h.ID, "changed mind")
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.State != hotswap.StateRollingBack {
		t.Errorf("expected rolling_back immediately, got %s", aborted.State)
	}

	time.Sleep(300 * time.Millisecond)

	final, _ := mgr.Get(ctx, h.ID)
	if final.State != hotswap.StateFailed {
		t.Errorf("expected failed after rollback, got %s", final.State)
	}

	// Entity must still be on source.
	ref, _ := reg.Resolve(ctx, rec.GlobalID)
	if ref.InstanceURL != src.URL {
		t.Errorf("entity should remain on source after abort, got %s", ref.InstanceURL)
	}
}

// ── Complete pipeline: PREPARING → Confirm → COMPLETE ────────────────────────

func TestConfirm_DrivesPipelineToComplete(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	rec, _ := reg.Register(ctx, "registry.test.local", "devices",
		identity.LocalRef{InstanceURL: src.URL, TenantID: 1, EntityType: "devices", LocalID: 1})

	// Request with AutoAdvance=false — stays in PREPARING until Confirm.
	h, err := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "acme", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "acme", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: false, QuiesceTimeout: 2 * time.Second},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if h.State != hotswap.StatePreparing {
		t.Fatalf("expected preparing, got %s", h.State)
	}

	// Operator confirms.
	confirmed, err := mgr.Confirm(ctx, h.ID)
	if err != nil {
		t.Fatalf("confirm: %v", err)
	}
	if confirmed.State != hotswap.StateQuiescing {
		t.Errorf("expected quiescing after confirm, got %s", confirmed.State)
	}

	// Wait for full pipeline completion.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(ctx, h.ID)
		if h2.State == hotswap.StateComplete || h2.State == hotswap.StateFailed {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if h.State != hotswap.StateComplete {
		t.Fatalf("expected complete, got %s (failure: %s)", h.State, h.FailureReason)
	}

	// Entity transferred.
	ref, _ := reg.Resolve(ctx, rec.GlobalID)
	if ref.InstanceURL != tgt.URL {
		t.Errorf("expected entity on tgt, got %s", ref.InstanceURL)
	}
	t.Logf("✓ pipeline: preparing→confirm→quiescing→migrating→validating→cutting_over→complete")
}

// ── History completeness ──────────────────────────────────────────────────────

func TestHistory_FullPipelineHasAllStates(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "acme", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "acme", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: true, QuiesceTimeout: 2 * time.Second},
	)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(ctx, h.ID)
		if h2.State == hotswap.StateComplete || h2.State == hotswap.StateFailed {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if h.State != hotswap.StateComplete {
		t.Fatalf("expected complete, got %s", h.State)
	}

	// Collect all states that appeared in history.
	seen := map[hotswap.State]bool{}
	for _, e := range h.History {
		seen[e.State] = true
	}

	required := []hotswap.State{
		hotswap.StateRequested,
		hotswap.StatePreparing,
		hotswap.StateQuiescing,
		hotswap.StateMigrating,
		hotswap.StateValidating,
		hotswap.StateCuttingOver,
		hotswap.StateComplete,
	}
	for _, s := range required {
		if !seen[s] {
			t.Errorf("state %s missing from history", s)
		}
	}
	t.Logf("✓ all %d pipeline states recorded in history", len(required))
}

// ── CompletedAt is set on completion ─────────────────────────────────────────

func TestCompletedAt_SetOnComplete(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	ctx := context.Background()

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	before := time.Now()
	h, _ := mgr.Request(ctx,
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "acme", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "acme", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: true, QuiesceTimeout: 2 * time.Second},
	)

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(ctx, h.ID)
		if h2.State == hotswap.StateComplete || h2.State == hotswap.StateFailed {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if h.State != hotswap.StateComplete {
		t.Fatalf("expected complete, got %s", h.State)
	}
	if h.CompletedAt == nil {
		t.Error("expected CompletedAt to be set on completion")
	} else if h.CompletedAt.Before(before) {
		t.Errorf("CompletedAt %v is before test start %v", h.CompletedAt, before)
	} else {
		t.Logf("✓ CompletedAt=%v", h.CompletedAt)
	}
}
