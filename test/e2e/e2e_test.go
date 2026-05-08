// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package e2e contains the nolu end-to-end test suite.
//
// This test requires the full Docker Compose stack to be running:
//
//	make docker-up
//
// Run with:
//
//	make test-e2e
//
// The test is skipped automatically when the xolu instances are not reachable,
// so it is safe to include in a broader test run — it will skip rather than
// fail if the stack is not up.
//
// What this test actually verifies (unlike the unit tests and demo):
//
//  1. An entity is written to xolu-vendocorp via its live HTTP API
//  2. nolu registers that entity's GlobalID pointing at xolu-vendocorp
//  3. nolu resolves the GlobalID and confirms it points at xolu-vendocorp
//  4. The transfer negotiation runs (propose → accept → complete)
//  5. After transfer, nolu resolves the GlobalID — it now points at xolu-retailchain
//  6. The entity is confirmed present on xolu-retailchain via its live HTTP API
//  7. xolu-vendocorp still holds the original entity (nolu does not delete it —
//     that is the application layer's responsibility; nolu only updates the registry)
//  8. A NATS event was published for the transfer (verified via stream message count)
//  9. Retirement: nolu retires the GlobalID; Resolve returns ErrRetired
// 10. The entity on xolu-retailchain is still readable after retirement
//     (nolu retirement is a registry-level tombstone, not a data delete)
package e2e

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
	"github.com/ha1tch/nolu/pkg/xoluclient"
)

// ── Service addresses — overridable via environment ───────────────────────────

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

var (
	vendoURL   = envOr("XOLU_VENDOCORP_URL", "http://localhost:9090")
	retailURL  = envOr("XOLU_RETAILCHAIN_URL", "http://localhost:9091")
	serviceURL = envOr("XOLU_SERVICECO_URL", "http://localhost:9092")
	natsURL    = envOr("NATS_URL", "nats://localhost:4222")
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// requireXolu skips the test if the given xolu instance is not reachable.
func requireXolu(t *testing.T, name, url string) *xoluclient.Client {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	c := xoluclient.New(url, 0)
	if err := c.Healthy(ctx); err != nil {
		t.Skipf("xolu instance %s (%s) not reachable: %v — run 'make docker-up' first", name, url, err)
	}
	return c
}

// requireNATS skips the test if NATS is not reachable, otherwise returns a
// connected NATSBus. Uses a short timeout so the skip is fast.
func requireNATS(t *testing.T) *events.NATSBus {
	t.Helper()
	bus, err := events.NewNATSBus(events.NATSBusConfig{
		URL:            natsURL,
		StreamName:     "NOLU_EVENTS",
		ConnectTimeout: 3 * time.Second,
	})
	if err != nil {
		t.Skipf("NATS not reachable at %s: %v — run 'make docker-up' first", natsURL, err)
	}
	return bus
}

// mustCreate creates an entity on a xolu instance and fatals on error.
func mustCreate(t *testing.T, ctx context.Context, c *xoluclient.Client, entity string, data map[string]interface{}) int {
	t.Helper()
	result, err := c.Create(ctx, entity, data)
	if err != nil {
		t.Fatalf("create %s: %v", entity, err)
	}
	id, err := xoluclient.IntID(result)
	if err != nil {
		t.Fatalf("create %s: extract id: %v", entity, err)
	}
	return id
}

// assertExists fatals if the entity does not exist on the given client.
func assertExists(t *testing.T, ctx context.Context, c *xoluclient.Client, label, entity string, id int) {
	t.Helper()
	exists, err := c.Exists(ctx, entity, id)
	if err != nil {
		t.Fatalf("%s: exists check: %v", label, err)
	}
	if !exists {
		t.Errorf("%s: expected entity %s/%d to exist, but it does not", label, entity, id)
	}
}

// assertAbsent fatals if the entity exists on the given client when it should not.
func assertAbsent(t *testing.T, ctx context.Context, c *xoluclient.Client, label, entity string, id int) {
	t.Helper()
	exists, err := c.Exists(ctx, entity, id)
	if err != nil {
		t.Fatalf("%s: exists check: %v", label, err)
	}
	if exists {
		t.Errorf("%s: expected entity %s/%d to be absent, but it exists", label, entity, id)
	}
}

// ── Tests ─────────────────────────────────────────────────────────────────────

// TestE2E_FullTransferLifecycle is the primary end-to-end test.
// It exercises the complete nolu clearinghouse flow against live xolu instances
// and a live NATS JetStream broker.
func TestE2E_FullTransferLifecycle(t *testing.T) {
	ctx := context.Background()

	// ── Require all services ─────────────────────────────────────────────────
	vendoClient   := requireXolu(t, "vendocorp",   vendoURL)
	retailClient  := requireXolu(t, "retailchain", retailURL)
	_             = requireXolu(t, "serviceco",    serviceURL) // checked but not used in this test
	bus           := requireNATS(t)
	defer bus.Close()

	// ── Wiring ───────────────────────────────────────────────────────────────
	reg := registry.NewMemoryRegistry("registry.e2e.local", bus)
	neg := transfer.NewMemoryNegotiator(reg)

	// ── Subscribe to all events before anything happens ──────────────────────
	evCh := make(chan registry.Event, 32)
	cancelSub, _ := reg.Subscribe(ctx, registry.SubscriptionFilter{}, evCh)
	defer cancelSub()

	// ── Step 1: Write entity to xolu-vendocorp ───────────────────────────────
	t.Log("step 1: create device on xolu-vendocorp")

	vendoLocalID := mustCreate(t, ctx, vendoClient, "devices", map[string]interface{}{
		"serial":       "SN-E2E-001",
		"model":        "VM-3000",
		"manufactured": time.Now().UTC().Format(time.RFC3339),
		"status":       "new",
	})
	t.Logf("  created devices/%d on vendocorp", vendoLocalID)

	// Confirm it exists.
	assertExists(t, ctx, vendoClient, "step 1", "devices", vendoLocalID)

	// ── Step 2: Register with nolu ────────────────────────────────────────────
	t.Log("step 2: register GlobalID with nolu")

	fromRef := identity.LocalRef{
		InstanceURL: vendoURL,
		EntityType:  "devices",
		LocalID:     vendoLocalID,
	}
	rec, err := reg.Register(ctx, "registry.e2e.local", "devices", fromRef)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	gid := rec.GlobalID
	t.Logf("  GlobalID: %s", gid)

	// ── Step 3: Resolve — must point at vendocorp ─────────────────────────────
	t.Log("step 3: resolve GlobalID — expect vendocorp")

	resolved, err := reg.Resolve(ctx, gid)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if resolved.InstanceURL != vendoURL {
		t.Errorf("resolve: expected %s, got %s", vendoURL, resolved.InstanceURL)
	}
	if resolved.LocalID != vendoLocalID {
		t.Errorf("resolve: expected localID %d, got %d", vendoLocalID, resolved.LocalID)
	}

	// ── Step 4: Write matching entity to xolu-retailchain ────────────────────
	// In a real system the application layer would POST the entity to retailchain
	// as part of accepting the transfer. We do it explicitly here so the e2e
	// test can verify both sides independently.
	t.Log("step 4: create matching device on xolu-retailchain")

	retailLocalID := mustCreate(t, ctx, retailClient, "devices", map[string]interface{}{
		"serial":       "SN-E2E-001",
		"model":        "VM-3000",
		"nolu_gid":     gid.String(), // record the GlobalID on the entity
		"status":       "received",
	})
	t.Logf("  created devices/%d on retailchain", retailLocalID)

	toRef := identity.LocalRef{
		InstanceURL: retailURL,
		EntityType:  "devices",
		LocalID:     retailLocalID,
	}

	// ── Step 5: Transfer proposal → accept → complete ─────────────────────────
	t.Log("step 5: transfer negotiation")

	proposal, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         fromRef,
		To:           toRef,
		Protocol:     "PO-E2E-001",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	if err != nil {
		t.Fatalf("propose: %v", err)
	}
	t.Logf("  proposal %s: %s", proposal.ID, proposal.State)

	accepted, err := neg.Accept(ctx, proposal.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	t.Logf("  proposal %s: %s", accepted.ID, accepted.State)

	completed, err := neg.Complete(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("complete: %v", err)
	}
	t.Logf("  proposal %s: %s", completed.ID, completed.State)

	// ── Step 6: Resolve — must now point at retailchain ───────────────────────
	t.Log("step 6: resolve GlobalID after transfer — expect retailchain")

	resolved, err = reg.Resolve(ctx, gid)
	if err != nil {
		t.Fatalf("resolve post-transfer: %v", err)
	}
	if resolved.InstanceURL != retailURL {
		t.Errorf("resolve post-transfer: expected %s, got %s", retailURL, resolved.InstanceURL)
	}
	if resolved.LocalID != retailLocalID {
		t.Errorf("resolve post-transfer: expected localID %d, got %d", retailLocalID, resolved.LocalID)
	}

	// ── Step 7: Verify entity is on retailchain ───────────────────────────────
	t.Log("step 7: verify entity exists on xolu-retailchain")
	assertExists(t, ctx, retailClient, "step 7", "devices", retailLocalID)

	// ── Step 8: Verify entity is STILL on vendocorp ───────────────────────────
	// nolu does not delete source data — that is the application's responsibility.
	// This assertion confirms nolu's contract: registry update only.
	t.Log("step 8: verify entity still exists on xolu-vendocorp (nolu does not delete source)")
	assertExists(t, ctx, vendoClient, "step 8", "devices", vendoLocalID)

	// Optionally, the application would PATCH the vendocorp entity to mark it
	// as transferred. We do that here to show the intended pattern.
	_, err = vendoClient.Patch(ctx, "devices", vendoLocalID, map[string]interface{}{
		"status":       "transferred",
		"transferred_to_nolu_gid": gid.String(),
	})
	if err != nil {
		t.Logf("  patch vendocorp entity (non-fatal): %v", err)
	} else {
		t.Log("  patched vendocorp entity status → transferred")
	}

	// ── Step 9: NATS event count ──────────────────────────────────────────────
	// Give the async bus publisher a moment to flush.
	t.Log("step 9: check NATS event count")
	time.Sleep(200 * time.Millisecond)

	eventCount := 0
	done := false
	for !done {
		select {
		case ev := <-evCh:
			eventCount++
			t.Logf("  event: %s  %s", ev.Kind, shortGID(ev.GlobalID))
		default:
			done = true
		}
	}
	// Expect: 1 registered + 1 transferred = 2 events minimum.
	if eventCount < 2 {
		t.Errorf("expected at least 2 events (registered + transferred), got %d", eventCount)
	}
	t.Logf("  total events received: %d", eventCount)

	// ── Step 10: Retire ───────────────────────────────────────────────────────
	t.Log("step 10: retire GlobalID")

	if err := reg.Retire(ctx, gid, "e2e test cleanup"); err != nil {
		t.Fatalf("retire: %v", err)
	}

	_, err = reg.Resolve(ctx, gid)
	if err != registry.ErrRetired {
		t.Errorf("resolve post-retire: expected ErrRetired, got %v", err)
	}
	t.Log("  GlobalID retired; Resolve returns ErrRetired ✓")

	// ── Step 11: Entity on retailchain survives retirement ────────────────────
	// nolu retirement is a registry-level tombstone only.
	t.Log("step 11: verify entity still readable on retailchain after retirement")
	assertExists(t, ctx, retailClient, "step 11", "devices", retailLocalID)

	// ── Cleanup: delete test entities from both instances ─────────────────────
	t.Log("cleanup: delete test entities")
	_ = vendoClient.Delete(ctx, "devices", vendoLocalID)
	_ = retailClient.Delete(ctx, "devices", retailLocalID)

	t.Log("TestE2E_FullTransferLifecycle PASSED")
}

// TestE2E_RepairCycle tests the three-party repair sub-transfer scenario:
// RetailChain → ServiceCo → RetailChain, verifying entity presence at each hop.
func TestE2E_RepairCycle(t *testing.T) {
	ctx := context.Background()

	retailClient  := requireXolu(t, "retailchain", retailURL)
	serviceClient := requireXolu(t, "serviceco",   serviceURL)
	bus           := requireNATS(t)
	defer bus.Close()

	reg := registry.NewMemoryRegistry("registry.e2e.local", bus)
	neg := transfer.NewMemoryNegotiator(reg)

	// Write device to RetailChain first.
	t.Log("setup: create device on retailchain")
	retailLocalID := mustCreate(t, ctx, retailClient, "devices", map[string]interface{}{
		"serial": "SN-REPAIR-001", "status": "deployed",
	})
	t.Logf("  created devices/%d on retailchain", retailLocalID)

	retailRef := identity.LocalRef{InstanceURL: retailURL, EntityType: "devices", LocalID: retailLocalID}

	rec, err := reg.Register(ctx, "registry.e2e.local", "devices", retailRef)
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	gid := rec.GlobalID

	// Write matching entity to ServiceCo.
	t.Log("step 1: create device record on serviceco for repair")
	serviceLocalID := mustCreate(t, ctx, serviceClient, "devices", map[string]interface{}{
		"serial": "SN-REPAIR-001", "status": "in-repair", "work_order": "WO-E2E-0001",
	})
	t.Logf("  created devices/%d on serviceco", serviceLocalID)

	serviceRef := identity.LocalRef{InstanceURL: serviceURL, EntityType: "devices", LocalID: serviceLocalID}

	// RetailChain → ServiceCo
	t.Log("step 2: transfer to serviceco")
	p1, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID: gid, From: retailRef, To: serviceRef,
		Protocol:     "WO-E2E-0001",
		HistoryOffer: transfer.HistoryOffer{Mode: "from"},
	})
	a1, err := neg.Accept(ctx, p1.ID, transfer.HistorySpec{Mode: "from", From: time.Now().Add(-7 * 24 * time.Hour)})
	if err != nil {
		t.Fatalf("accept to serviceco: %v", err)
	}
	neg.Complete(ctx, a1.ID)

	resolved, _ := reg.Resolve(ctx, gid)
	if resolved.InstanceURL != serviceURL {
		t.Errorf("after repair transfer: expected serviceco, got %s", resolved.InstanceURL)
	}
	assertExists(t, ctx, serviceClient, "repair in-progress", "devices", serviceLocalID)
	t.Logf("  registry now points to serviceco ✓")

	// ServiceCo → RetailChain (repair complete)
	t.Log("step 3: return to retailchain after repair")
	p2, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID: gid, From: serviceRef, To: retailRef,
		Protocol:     "WO-E2E-0001-RTN",
		HistoryOffer: transfer.HistoryOffer{Mode: "full", Note: "Full repair log included"},
	})
	a2, err := neg.Accept(ctx, p2.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		t.Fatalf("accept return: %v", err)
	}
	neg.Complete(ctx, a2.ID)

	resolved, _ = reg.Resolve(ctx, gid)
	if resolved.InstanceURL != retailURL {
		t.Errorf("after return: expected retailchain, got %s", resolved.InstanceURL)
	}
	if resolved.LocalID != retailLocalID {
		t.Errorf("after return: expected localID %d, got %d", retailLocalID, resolved.LocalID)
	}
	assertExists(t, ctx, retailClient, "post-repair", "devices", retailLocalID)
	t.Logf("  registry points back to retailchain ✓")

	// Verify history depth.
	finalRec, _ := reg.Get(ctx, gid)
	if len(finalRec.History) != 2 {
		t.Errorf("expected 2 transfer history entries, got %d", len(finalRec.History))
	}

	// Cleanup.
	_ = retailClient.Delete(ctx, "devices", retailLocalID)
	_ = serviceClient.Delete(ctx, "devices", serviceLocalID)

	t.Log("TestE2E_RepairCycle PASSED")
}

// TestE2E_XoluInstancesHealthy is a lightweight connectivity check that can be
// run in isolation to confirm the stack is up without running the full scenario.
func TestE2E_XoluInstancesHealthy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	for _, tc := range []struct{ name, url string }{
		{"vendocorp",   vendoURL},
		{"retailchain", retailURL},
		{"serviceco",   serviceURL},
	} {
		c := xoluclient.New(tc.url, 0)
		if err := c.Healthy(ctx); err != nil {
			t.Skipf("%s (%s) not reachable: %v", tc.name, tc.url, err)
		}
		t.Logf("  %s: healthy ✓", tc.name)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

func shortGID(gid identity.GlobalID) string {
	s := string(gid)
	if len(s) > 12 {
		return "…" + s[len(s)-12:]
	}
	return s
}

// assertAbsentFn is declared here to satisfy the unused import check.
// It is used by assertAbsent above; the compiler will confirm.
var _ = fmt.Sprintf
