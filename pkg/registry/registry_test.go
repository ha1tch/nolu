// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package registry_test

import (
	"context"
	"testing"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
)

func newReg() *registry.MemoryRegistry {
	return registry.NewMemoryRegistry("registry.test.local", events.NewMemoryBus())
}

func localRef(instance, entity string, id int) identity.LocalRef {
	return identity.LocalRef{InstanceURL: instance, EntityType: entity, LocalID: id}
}

// ── Register ─────────────────────────────────────────────────────────────────

func TestRegister_Success(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	rec, err := reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	if err != nil {
		t.Fatalf("Register: unexpected error: %v", err)
	}
	if err := rec.GlobalID.Validate(); err != nil {
		t.Fatalf("GlobalID invalid: %v", err)
	}
	if rec.Status != registry.StatusActive {
		t.Errorf("expected status active, got %s", rec.Status)
	}
	if rec.Current.LocalID != 1 {
		t.Errorf("expected LocalID 1, got %d", rec.Current.LocalID)
	}
	if len(rec.History) != 0 {
		t.Errorf("expected empty history on registration, got %d entries", len(rec.History))
	}
}

func TestRegister_GlobalID_UniquePerCall(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	r1, _ := reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	r2, _ := reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 2))

	if r1.GlobalID == r2.GlobalID {
		t.Error("two Register calls produced the same GlobalID")
	}
}

func TestRegister_EntityTypeInGlobalID(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	rec, _ := reg.Register(ctx, "registry.test.local", "shelves", localRef("http://xolu-a", "shelves", 1))
	et, err := rec.GlobalID.EntityType()
	if err != nil {
		t.Fatalf("EntityType: %v", err)
	}
	if et != "shelves" {
		t.Errorf("expected entity type 'shelves', got %q", et)
	}
}

// ── Get / Resolve ─────────────────────────────────────────────────────────────

func TestGet_NotFound(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	_, err := reg.Get(ctx, identity.GlobalID("nolu://registry.test.local/devices/nonexistent"))
	if err != registry.ErrNotFound {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestResolve_ActiveEntity(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	ref := localRef("http://xolu-a", "devices", 42)
	rec, _ := reg.Register(ctx, "registry.test.local", "devices", ref)

	got, err := reg.Resolve(ctx, rec.GlobalID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got != ref {
		t.Errorf("expected %+v, got %+v", ref, got)
	}
}

// ── Transfer ──────────────────────────────────────────────────────────────────

func TestTransfer_Success(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	fromRef := localRef("http://xolu-a", "devices", 1)
	toRef := localRef("http://xolu-b", "devices", 100)

	rec, _ := reg.Register(ctx, "registry.test.local", "devices", fromRef)

	updated, err := reg.Transfer(ctx, registry.TransferRequest{
		GlobalID: rec.GlobalID,
		From:     fromRef,
		To:       toRef,
		Protocol: "PO-TEST-001",
	})
	if err != nil {
		t.Fatalf("Transfer: %v", err)
	}
	if updated.Current != toRef {
		t.Errorf("expected current owner %+v, got %+v", toRef, updated.Current)
	}
	if len(updated.History) != 1 {
		t.Errorf("expected 1 history entry, got %d", len(updated.History))
	}
	if updated.History[0].Protocol != "PO-TEST-001" {
		t.Errorf("expected protocol PO-TEST-001, got %q", updated.History[0].Protocol)
	}
}

func TestTransfer_WrongCurrentOwner(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	fromRef := localRef("http://xolu-a", "devices", 1)
	wrongRef := localRef("http://xolu-wrong", "devices", 99)
	toRef := localRef("http://xolu-b", "devices", 100)

	rec, _ := reg.Register(ctx, "registry.test.local", "devices", fromRef)

	_, err := reg.Transfer(ctx, registry.TransferRequest{
		GlobalID: rec.GlobalID,
		From:     wrongRef,
		To:       toRef,
	})
	if err == nil {
		t.Fatal("expected error for wrong current owner, got nil")
	}
}

func TestTransfer_MultipleHops(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	a := localRef("http://xolu-a", "devices", 1)
	b := localRef("http://xolu-b", "devices", 2)
	c := localRef("http://xolu-c", "devices", 3)

	rec, _ := reg.Register(ctx, "registry.test.local", "devices", a)
	rec, _ = reg.Transfer(ctx, registry.TransferRequest{GlobalID: rec.GlobalID, From: a, To: b})
	rec, _ = reg.Transfer(ctx, registry.TransferRequest{GlobalID: rec.GlobalID, From: b, To: c})
	rec, _ = reg.Transfer(ctx, registry.TransferRequest{GlobalID: rec.GlobalID, From: c, To: a})

	if rec.Current != a {
		t.Errorf("expected back at a, got %+v", rec.Current)
	}
	if len(rec.History) != 3 {
		t.Errorf("expected 3 history entries, got %d", len(rec.History))
	}
}

// ── Retire ────────────────────────────────────────────────────────────────────

func TestRetire_Success(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	rec, _ := reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))

	if err := reg.Retire(ctx, rec.GlobalID, "end of life"); err != nil {
		t.Fatalf("Retire: %v", err)
	}

	got, _ := reg.Get(ctx, rec.GlobalID)
	if got.Status != registry.StatusRetired {
		t.Errorf("expected status retired, got %s", got.Status)
	}
}

func TestRetire_ResolveFails(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	rec, _ := reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	_ = reg.Retire(ctx, rec.GlobalID, "end of life")

	_, err := reg.Resolve(ctx, rec.GlobalID)
	if err != registry.ErrRetired {
		t.Errorf("expected ErrRetired, got %v", err)
	}
}

func TestRetire_TransferFails(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	ref := localRef("http://xolu-a", "devices", 1)
	rec, _ := reg.Register(ctx, "registry.test.local", "devices", ref)
	_ = reg.Retire(ctx, rec.GlobalID, "end of life")

	_, err := reg.Transfer(ctx, registry.TransferRequest{
		GlobalID: rec.GlobalID,
		From:     ref,
		To:       localRef("http://xolu-b", "devices", 2),
	})
	if err != registry.ErrRetired {
		t.Errorf("expected ErrRetired on transfer, got %v", err)
	}
}

func TestRetire_Idempotent_Fails(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	rec, _ := reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	_ = reg.Retire(ctx, rec.GlobalID, "first")

	if err := reg.Retire(ctx, rec.GlobalID, "second"); err != registry.ErrRetired {
		t.Errorf("expected ErrRetired on double retire, got %v", err)
	}
}

// ── ListBy ────────────────────────────────────────────────────────────────────

func TestListByInstance(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 2))
	reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-b", "devices", 1))

	ids, err := reg.ListByInstance(ctx, "http://xolu-a")
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 entries for xolu-a, got %d", len(ids))
	}
}

func TestListByEntityType(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 2))
	reg.Register(ctx, "registry.test.local", "shelves", localRef("http://xolu-a", "shelves", 1))

	ids, err := reg.ListByEntityType(ctx, "devices")
	if err != nil {
		t.Fatalf("ListByEntityType: %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 devices, got %d", len(ids))
	}
}

// ── Subscribe ─────────────────────────────────────────────────────────────────

func TestSubscribe_ReceivesEvents(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	ch := make(chan registry.Event, 10)
	cancel, err := reg.Subscribe(ctx, registry.SubscriptionFilter{}, ch)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer cancel()

	ref := localRef("http://xolu-a", "devices", 1)
	rec, _ := reg.Register(ctx, "registry.test.local", "devices", ref)
	_ = reg.Retire(ctx, rec.GlobalID, "test")

	if len(ch) != 2 {
		t.Errorf("expected 2 events (registered + retired), got %d", len(ch))
	}
}

func TestSubscribe_FilterByEntityType(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	ch := make(chan registry.Event, 10)
	cancel, _ := reg.Subscribe(ctx, registry.SubscriptionFilter{
		EntityTypes: []string{"shelves"},
	}, ch)
	defer cancel()

	reg.Register(ctx, "registry.test.local", "devices", localRef("http://xolu-a", "devices", 1))
	rec, _ := reg.Register(ctx, "registry.test.local", "shelves", localRef("http://xolu-a", "shelves", 1))
	_ = rec

	if len(ch) != 1 {
		t.Errorf("expected 1 event (shelves only), got %d", len(ch))
	}
}

// ── Demo scenario assertions ──────────────────────────────────────────────────
// These mirror the Phase 8 snapshot that the demo prints, asserting the exact
// expected states programmatically rather than relying on human inspection.

func TestDemoScenario(t *testing.T) {
	ctx := context.Background()
	reg := newReg()

	const (
		vendoURL   = "http://xolu-vendocorp:9090"
		retailURL  = "http://xolu-retailchain:9091"
		serviceURL = "http://xolu-serviceco:9092"
	)

	// Phase 1: register 5 devices
	deviceIDs := make([]identity.GlobalID, 5)
	for i := 0; i < 5; i++ {
		rec, err := reg.Register(ctx, "registry.test.local", "devices",
			localRef(vendoURL, "devices", 1000+i))
		if err != nil {
			t.Fatalf("register device %d: %v", i, err)
		}
		deviceIDs[i] = rec.GlobalID
	}

	// Phase 3+4: transfer devices 0–2 to RetailChain; device 3 rejected (no transfer)
	for i := 0; i < 3; i++ {
		_, err := reg.Transfer(ctx, registry.TransferRequest{
			GlobalID: deviceIDs[i],
			From:     localRef(vendoURL, "devices", 1000+i),
			To:       localRef(retailURL, "devices", 2000+i),
			Protocol: "PO-2026-001",
		})
		if err != nil {
			t.Fatalf("transfer device %d: %v", i, err)
		}
	}

	// Phase 6: device 1 repair cycle (→ ServiceCo → back to RetailChain)
	_, err := reg.Transfer(ctx, registry.TransferRequest{
		GlobalID: deviceIDs[1],
		From:     localRef(retailURL, "devices", 2001),
		To:       localRef(serviceURL, "devices", 3001),
		Protocol: "WO-2026-0042",
	})
	if err != nil {
		t.Fatalf("repair transfer: %v", err)
	}
	_, err = reg.Transfer(ctx, registry.TransferRequest{
		GlobalID: deviceIDs[1],
		From:     localRef(serviceURL, "devices", 3001),
		To:       localRef(retailURL, "devices", 2001),
		Protocol: "WO-2026-0042-RTN",
	})
	if err != nil {
		t.Fatalf("return transfer: %v", err)
	}

	// Phase 7: retire device 2
	if err := reg.Retire(ctx, deviceIDs[2], "exceeded service life"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	// ── Phase 8 assertions ────────────────────────────────────────────────────

	type expectation struct {
		label     string
		id        identity.GlobalID
		status    registry.Status
		ownerURL  string
		transfers int
	}

	wants := []expectation{
		{"device 0", deviceIDs[0], registry.StatusActive, retailURL, 1},
		{"device 1", deviceIDs[1], registry.StatusActive, retailURL, 3},
		{"device 2", deviceIDs[2], registry.StatusRetired, retailURL, 1},
		{"device 3", deviceIDs[3], registry.StatusActive, vendoURL, 0},
		{"device 4", deviceIDs[4], registry.StatusActive, vendoURL, 0},
	}

	for _, w := range wants {
		rec, err := reg.Get(ctx, w.id)
		if err != nil {
			t.Errorf("%s: Get failed: %v", w.label, err)
			continue
		}
		if rec.Status != w.status {
			t.Errorf("%s: expected status %s, got %s", w.label, w.status, rec.Status)
		}
		if rec.Current.InstanceURL != w.ownerURL {
			t.Errorf("%s: expected owner %s, got %s", w.label, w.ownerURL, rec.Current.InstanceURL)
		}
		if len(rec.History) != w.transfers {
			t.Errorf("%s: expected %d transfers, got %d", w.label, w.transfers, len(rec.History))
		}
	}

	// Device 2 must return ErrRetired on Resolve.
	if _, err := reg.Resolve(ctx, deviceIDs[2]); err != registry.ErrRetired {
		t.Errorf("device 2: expected ErrRetired on Resolve, got %v", err)
	}
}

func TestMemoryRegistry_ListByInstanceAndTenant(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	ctx := context.Background()

	// Register three devices — two for tenant 1 on xolu-a, one for tenant 2 on xolu-a,
	// one for tenant 1 on xolu-b.
	for i, ref := range []identity.LocalRef{
		{InstanceURL: "http://xolu-a:9090", TenantID: 1, EntityType: "devices", LocalID: 1},
		{InstanceURL: "http://xolu-a:9090", TenantID: 1, EntityType: "devices", LocalID: 2},
		{InstanceURL: "http://xolu-a:9090", TenantID: 2, EntityType: "devices", LocalID: 1},
		{InstanceURL: "http://xolu-b:9091", TenantID: 1, EntityType: "devices", LocalID: 1},
	} {
		if _, err := reg.Register(ctx, "registry.test.local", "devices", ref); err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
	}

	// xolu-a + tenant 1 → 2 entities.
	gids, err := reg.ListByInstanceAndTenant(ctx, "http://xolu-a:9090", 1)
	if err != nil {
		t.Fatalf("ListByInstanceAndTenant: %v", err)
	}
	if len(gids) != 2 {
		t.Errorf("xolu-a tenant 1: expected 2, got %d", len(gids))
	}

	// xolu-a + tenant 2 → 1 entity.
	gids, _ = reg.ListByInstanceAndTenant(ctx, "http://xolu-a:9090", 2)
	if len(gids) != 1 {
		t.Errorf("xolu-a tenant 2: expected 1, got %d", len(gids))
	}

	// xolu-b + tenant 1 → 1 entity.
	gids, _ = reg.ListByInstanceAndTenant(ctx, "http://xolu-b:9091", 1)
	if len(gids) != 1 {
		t.Errorf("xolu-b tenant 1: expected 1, got %d", len(gids))
	}

	// xolu-a + tenant 99 → 0 entities.
	gids, _ = reg.ListByInstanceAndTenant(ctx, "http://xolu-a:9090", 99)
	if len(gids) != 0 {
		t.Errorf("xolu-a tenant 99: expected 0, got %d", len(gids))
	}
}
