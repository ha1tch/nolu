// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package e2e

// TestE2E_XoluRegistry tests that XoluRegistry is durable across instantiations.
// It creates a registry, registers entities, restarts (creates a new instance
// pointing at the same xolu), and verifies the records survived.
//
// Requires docker-up (xolu-vendocorp on localhost:9090).

import (
	"context"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
	"github.com/ha1tch/nolu/pkg/xoluclient"
)

func TestE2E_XoluRegistry_Durability(t *testing.T) {
	ctx := context.Background()

	// Require xolu-vendocorp (used as both the nolu backing store and the
	// entity store in this test — in production these would be separate instances).
	vendoClient := requireXolu(t, "vendocorp", vendoURL)
	_ = vendoClient

	// ── Instantiate XoluRegistry backed by xolu-vendocorp ───────────────────
	t.Log("step 1: create XoluRegistry backed by xolu-vendocorp")

	reg1, err := registry.NewXoluRegistry(ctx, vendoURL, "registry.e2e-dur.local", events.NewMemoryBus())
	if err != nil {
		t.Fatalf("NewXoluRegistry: %v", err)
	}

	// Register three entities.
	refs := []identity.LocalRef{
		{InstanceURL: vendoURL, EntityType: "devices", LocalID: 9001},
		{InstanceURL: vendoURL, EntityType: "devices", LocalID: 9002},
		{InstanceURL: vendoURL, EntityType: "shelves", LocalID: 9003},
	}
	gids := make([]identity.GlobalID, len(refs))
	for i, ref := range refs {
		rec, err := reg1.Register(ctx, "registry.e2e-dur.local", ref.EntityType, ref)
		if err != nil {
			t.Fatalf("register %d: %v", i, err)
		}
		gids[i] = rec.GlobalID
		t.Logf("  registered %s → %s", shortGID(rec.GlobalID), ref.EntityType)
	}

	// Transfer gids[0] to retailchain.
	_, err = reg1.Transfer(ctx, registry.TransferRequest{
		GlobalID: gids[0],
		From:     refs[0],
		To:       identity.LocalRef{InstanceURL: retailURL, EntityType: "devices", LocalID: 8001},
		Protocol: "DUR-TEST-001",
	})
	if err != nil {
		t.Fatalf("transfer: %v", err)
	}
	t.Log("  transferred gids[0] to retailchain")

	// Retire gids[2].
	if err := reg1.Retire(ctx, gids[2], "durability test cleanup"); err != nil {
		t.Fatalf("retire: %v", err)
	}
	t.Log("  retired gids[2]")

	// ── Simulate restart: create a new XoluRegistry instance ────────────────
	t.Log("step 2: create new XoluRegistry instance (simulated restart)")
	time.Sleep(100 * time.Millisecond) // let async writes settle

	reg2, err := registry.NewXoluRegistry(ctx, vendoURL, "registry.e2e-dur.local", events.NewMemoryBus())
	if err != nil {
		t.Fatalf("NewXoluRegistry (restart): %v", err)
	}

	// ── Verify all records survived ──────────────────────────────────────────
	t.Log("step 3: verify records survived restart")

	// gids[0] — transferred to retailchain
	rec0, err := reg2.Get(ctx, gids[0])
	if err != nil {
		t.Fatalf("get gids[0]: %v", err)
	}
	if rec0.Current.InstanceURL != retailURL {
		t.Errorf("gids[0]: expected retailchain owner, got %s", rec0.Current.InstanceURL)
	}
	if len(rec0.History) != 1 {
		t.Errorf("gids[0]: expected 1 transfer, got %d", len(rec0.History))
	}
	t.Logf("  gids[0]: owner=%s transfers=%d ✓", rec0.Current.InstanceURL, len(rec0.History))

	// gids[1] — still on vendocorp, active
	rec1, err := reg2.Get(ctx, gids[1])
	if err != nil {
		t.Fatalf("get gids[1]: %v", err)
	}
	if rec1.Status != registry.StatusActive {
		t.Errorf("gids[1]: expected active, got %s", rec1.Status)
	}
	if rec1.Current.InstanceURL != vendoURL {
		t.Errorf("gids[1]: expected vendocorp, got %s", rec1.Current.InstanceURL)
	}
	t.Logf("  gids[1]: status=%s owner=%s ✓", rec1.Status, rec1.Current.InstanceURL)

	// gids[2] — retired
	rec2, err := reg2.Get(ctx, gids[2])
	if err != nil {
		t.Fatalf("get gids[2]: %v", err)
	}
	if rec2.Status != registry.StatusRetired {
		t.Errorf("gids[2]: expected retired, got %s", rec2.Status)
	}
	_, err = reg2.Resolve(ctx, gids[2])
	if err != registry.ErrRetired {
		t.Errorf("gids[2] resolve: expected ErrRetired, got %v", err)
	}
	t.Logf("  gids[2]: status=%s ✓", rec2.Status)

	// ListByInstance — gids[1] should appear under vendoURL, gids[0] should not
	byInstance, err := reg2.ListByInstance(ctx, vendoURL)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	found := false
	for _, id := range byInstance {
		if id == gids[1] {
			found = true
		}
		if id == gids[0] {
			t.Errorf("ListByInstance: gids[0] (transferred) should not appear under vendocorp")
		}
	}
	if !found {
		t.Errorf("ListByInstance: gids[1] not found under vendocorp")
	}
	t.Logf("  ListByInstance(vendocorp): %d active records ✓", len(byInstance))

	// ListByEntityType — devices
	byType, err := reg2.ListByEntityType(ctx, "devices")
	if err != nil {
		t.Fatalf("ListByEntityType: %v", err)
	}
	// gids[0] (transferred, active) and gids[1] (active) should appear;
	// gids[2] (shelves) should not.
	deviceCount := 0
	for _, id := range byType {
		et, _ := id.EntityType()
		if et != "devices" {
			t.Errorf("ListByEntityType(devices): got non-device entity %s", id)
		}
		deviceCount++
	}
	if deviceCount < 2 {
		t.Errorf("ListByEntityType(devices): expected at least 2, got %d", deviceCount)
	}
	t.Logf("  ListByEntityType(devices): %d records ✓", deviceCount)

	t.Log("TestE2E_XoluRegistry_Durability PASSED")
}

// TestE2E_XoluRegistry_ConcurrentTransfer verifies that the _version-based
// optimistic concurrency prevents two simultaneous transfers from both
// succeeding — exactly one must win, the other must get ErrInvalidTransfer.
func TestE2E_XoluRegistry_ConcurrentTransfer(t *testing.T) {
	ctx := context.Background()
	requireXolu(t, "vendocorp", vendoURL)
	requireXolu(t, "retailchain", retailURL)
	requireXolu(t, "serviceco", serviceURL)

	reg, err := registry.NewXoluRegistry(ctx, vendoURL, "registry.e2e-conc.local", events.NewMemoryBus())
	if err != nil {
		t.Fatalf("NewXoluRegistry: %v", err)
	}

	fromRef := identity.LocalRef{InstanceURL: vendoURL, EntityType: "devices", LocalID: 9900}
	rec, err := reg.Register(ctx, "registry.e2e-conc.local", "devices", fromRef)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	gid := rec.GlobalID
	t.Logf("registered %s", shortGID(gid))

	// Launch two concurrent transfers: one to retail, one to service.
	type result struct {
		err error
	}
	ch := make(chan result, 2)

	toRetail := identity.LocalRef{InstanceURL: retailURL, EntityType: "devices", LocalID: 8900}
	toService := identity.LocalRef{InstanceURL: serviceURL, EntityType: "devices", LocalID: 7900}

	go func() {
		_, err := reg.Transfer(ctx, registry.TransferRequest{
			GlobalID: gid, From: fromRef, To: toRetail,
		})
		ch <- result{err}
	}()
	go func() {
		_, err := reg.Transfer(ctx, registry.TransferRequest{
			GlobalID: gid, From: fromRef, To: toService,
		})
		ch <- result{err}
	}()

	r1, r2 := <-ch, <-ch

	wins := 0
	losses := 0
	for _, r := range []result{r1, r2} {
		if r.err == nil {
			wins++
		} else {
			losses++
			t.Logf("  losing transfer got: %v", r.err)
		}
	}

	if wins != 1 {
		t.Errorf("expected exactly 1 winning transfer, got %d", wins)
	}
	if losses != 1 {
		t.Errorf("expected exactly 1 losing transfer, got %d", losses)
	}

	// The registry must point to exactly one owner.
	final, err := reg.Get(ctx, gid)
	if err != nil {
		t.Fatalf("get after concurrent transfer: %v", err)
	}
	if len(final.History) != 1 {
		t.Errorf("expected exactly 1 history entry, got %d", len(final.History))
	}
	t.Logf("  final owner: %s ✓", final.Current.InstanceURL)
	t.Log("TestE2E_XoluRegistry_ConcurrentTransfer PASSED")
}

// TestE2E_XoluRegistry_NegotiatedTransfer tests the full negotiation protocol
// driven by XoluRegistry (not MemoryRegistry) as the backing store.
func TestE2E_XoluRegistry_NegotiatedTransfer(t *testing.T) {
	ctx := context.Background()
	requireXolu(t, "vendocorp", vendoURL)
	requireXolu(t, "retailchain", retailURL)

	reg, err := registry.NewXoluRegistry(ctx, vendoURL, "registry.e2e-neg.local", events.NewMemoryBus())
	if err != nil {
		t.Fatalf("NewXoluRegistry: %v", err)
	}
	neg := transfer.NewMemoryNegotiator(reg)

	fromRef := identity.LocalRef{InstanceURL: vendoURL, EntityType: "devices", LocalID: 9800}
	toRef := identity.LocalRef{InstanceURL: retailURL, EntityType: "devices", LocalID: 8800}

	rec, err := reg.Register(ctx, "registry.e2e-neg.local", "devices", fromRef)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	gid := rec.GlobalID

	// Propose → Accept → Complete
	p, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         fromRef,
		To:           toRef,
		Protocol:     "NEG-E2E-001",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}

	a, err := neg.Accept(ctx, p.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	_, err = neg.Complete(ctx, a.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}

	// Verify via the durable registry.
	resolved, err := reg.Resolve(ctx, gid)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.InstanceURL != retailURL {
		t.Errorf("expected retailchain after negotiated transfer, got %s", resolved.InstanceURL)
	}
	t.Logf("  resolved to %s ✓", resolved.InstanceURL)
	t.Log("TestE2E_XoluRegistry_NegotiatedTransfer PASSED")
}

// helper re-declared here to avoid import cycle — same as in e2e_test.go
func requireXoluForReg(t *testing.T, name, url string) *xoluclient.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := xoluclient.New(url, 0)
	if err := c.Healthy(ctx); err != nil {
		t.Skipf("xolu instance %s (%s) not reachable: %v", name, url, err)
	}
	return c
}
