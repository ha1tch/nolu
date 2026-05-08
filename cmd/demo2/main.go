// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Demo 2 — Basic: one multi-tenant xolu instance, three tenants.
//
// No persistent registry. Uses MemoryRegistry (state lost on exit).
// All three organisations share a single xolu instance but are isolated
// as tenants within it:
//
//   xolu-hub (port 9090, tenant_mode=strict)
//     tenant "vendocorp"   — VendoCorp, manufacturer
//     tenant "retailchain" — RetailChain, vending operator
//     tenant "serviceco"   — ServiceCo, repair provider
//
// This demonstrates that the nolu identity model is agnostic to whether
// organisations are separate xolu instances or tenants in a shared one.
// The clearinghouse operations are identical; only the LocalRef changes.
//
// Scenario: same operations as Demo 1.
// Key difference: LocalRefs all point at the same instance_url but
// different tenant_ids.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
	"github.com/ha1tch/nolu/pkg/xoluclient"
)

// ── Tenant IDs within the shared xolu instance ───────────────────────────────
// In xolu strict mode, tenants are pre-registered with integer IDs.
// We use fixed small IDs here for demo clarity.
const (
	tenantVendo   = uint16(1)
	tenantRetail  = uint16(2)
	tenantService = uint16(3)
)

func main() {
	xoluURL := flag.String("xolu", "http://localhost:9090", "Shared xolu instance URL")
	flag.Parse()

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	ctx := context.Background()

	// ── Connectivity check ────────────────────────────────────────────────────
	root := xoluclient.New(*xoluURL, 0)
	if err := root.Healthy(ctx); err != nil {
		fmt.Printf("%sERROR%s xolu-hub not reachable at %s: %v\n", colRed, colReset, *xoluURL, err)
		os.Exit(1)
	}
	fmt.Printf("%s✓%s  Connected to xolu-hub at %s\n", colGreen, colReset, *xoluURL)

	// ── Provision tenants ─────────────────────────────────────────────────────
	for _, name := range []string{"vendocorp", "retailchain", "serviceco"} {
		if err := root.EnsureTenant(ctx, name); err != nil {
			fmt.Printf("%sERROR%s ensure tenant %q: %v\n", colRed, colReset, name, err)
			os.Exit(1)
		}
	}
	fmt.Printf("%s✓%s  Tenants provisioned: vendocorp, retailchain, serviceco\n", colGreen, colReset)

	// ── LocalRefs — same instance_url, different tenants ─────────────────────
	// tenant_id 0 means "use tenantName path" in our client, but for the
	// nolu identity layer we use numeric tenant IDs to distinguish orgs.
	vendoRef := func(localID int) identity.LocalRef {
		return identity.LocalRef{InstanceURL: *xoluURL, TenantID: tenantVendo, EntityType: "devices", LocalID: localID}
	}
	retailRef := func(localID int) identity.LocalRef {
		return identity.LocalRef{InstanceURL: *xoluURL, TenantID: tenantRetail, EntityType: "devices", LocalID: localID}
	}
	serviceRef := func(localID int) identity.LocalRef {
		return identity.LocalRef{InstanceURL: *xoluURL, TenantID: tenantService, EntityType: "devices", LocalID: localID}
	}

	// ── Registry and negotiator ───────────────────────────────────────────────
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.demo2.local", bus)
	neg := transfer.NewMemoryNegotiator(reg)

	evCh := make(chan registry.Event, 64)
	cancelSub, _ := reg.Subscribe(ctx, registry.SubscriptionFilter{}, evCh)
	defer cancelSub()
	go func() {
		for ev := range evCh {
			var owner string
			if ev.Record != nil {
				owner = formatRef(ev.Record.Current, *xoluURL)
			}
			event(string(ev.Kind), string(ev.GlobalID), owner)
		}
	}()
	time.Sleep(50 * time.Millisecond)

	// ═════════════════════════════════════════════════════════════════════════
	// SCENARIO
	// ═════════════════════════════════════════════════════════════════════════

	header("Demo 2 — Multi-tenant xolu instance")
	fmt.Printf("  All three organisations share xolu-hub (%s)\n", *xoluURL)
	fmt.Printf("  Tenant isolation ensures cross-org data access is impossible\n\n")

	// ── Phase 1: Register devices (VendoCorp tenant) ──────────────────────────
	header("Phase 1 · Registration (tenant: vendocorp)")
	step(1, "VendoCorp registers 5 devices in its tenant")

	deviceIDs := make([]identity.GlobalID, 5)
	for i := 0; i < 5; i++ {
		rec, err := reg.Register(ctx, "registry.demo2.local", "devices", vendoRef(1000+i))
		if err != nil {
			fatal("register", err)
		}
		deviceIDs[i] = rec.GlobalID
		ok(fmt.Sprintf("device %d registered  %s%s%s", i+1, colGray, shortID(string(rec.GlobalID)), colReset))
	}
	time.Sleep(80 * time.Millisecond)

	detail("xolu path", fmt.Sprintf("%s/api/v1/tenant/vendocorp/devices/100x", *xoluURL))

	// ── Phase 2: Batch sale ───────────────────────────────────────────────────
	header("Phase 2 · Sale: vendocorp → retailchain tenants")
	step(2, "Batch sale of devices 1–4 (PO-2026-002)")

	proposals := make([]*transfer.Proposal, 4)
	for i := 0; i < 4; i++ {
		p, err := neg.Propose(ctx, transfer.Proposal{
			GlobalID:     deviceIDs[i],
			From:         vendoRef(1000 + i),
			To:           retailRef(2000 + i),
			Protocol:     "PO-2026-002",
			HistoryOffer: transfer.HistoryOffer{Mode: "full"},
		})
		if err != nil {
			fatal("propose", err)
		}
		proposals[i] = p
	}
	ok("4 proposals created")

	// Accept 1–3, reject 4
	for i := 0; i < 3; i++ {
		accepted, err := neg.Accept(ctx, proposals[i].ID, transfer.HistorySpec{Mode: "full"})
		if err != nil {
			fatal("accept", err)
		}
		neg.Complete(ctx, accepted.ID)
		ok(fmt.Sprintf("device %d: vendocorp/1%03d → retailchain/2%03d", i+1, i, i))
	}
	rejected, _ := neg.Reject(ctx, proposals[3].ID, "inspection failure: motor torque below spec")
	warn(fmt.Sprintf("device 4 REJECTED (%s)", rejected.State))
	detail("reason", rejected.RejectionReason)
	time.Sleep(80 * time.Millisecond)

	// ── Phase 3: Key difference — show tenant isolation ───────────────────────
	header("Phase 3 · Tenant isolation demonstration")
	step(3, "Verify tenant isolation: retailchain cannot see vendocorp data")

	vendoClient := xoluclient.NewTenant(*xoluURL, "vendocorp")
	retailClient := xoluclient.NewTenant(*xoluURL, "retailchain")

	// Create a test entity in vendocorp's tenant
	vendoEntity, err := vendoClient.Create(ctx, "devices", map[string]interface{}{
		"serial": "DEMO2-ISOLATION-TEST", "status": "new",
	})
	if err != nil {
		warn(fmt.Sprintf("create vendocorp entity: %v", err))
	} else {
		vendoID, _ := xoluclient.IntID(vendoEntity)
		ok(fmt.Sprintf("created devices/%d in vendocorp tenant", vendoID))

		// Attempt to read it from retailchain tenant — should not be visible
		exists, _ := retailClient.Exists(ctx, "devices", vendoID)
		if !exists {
			ok("retailchain cannot see vendocorp's device ✓ isolation confirmed")
		} else {
			warn("isolation BREACH: retailchain can see vendocorp's device")
		}

		// Cleanup
		vendoClient.Delete(ctx, "devices", vendoID)
	}

	// ── Phase 4: Repair cycle ─────────────────────────────────────────────────
	header("Phase 4 · Repair cycle: retailchain → serviceco → retailchain")
	step(4, "Device 2 sent for repair (tenant transfer)")

	pRepair, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     deviceIDs[1],
		From:         retailRef(2001),
		To:           serviceRef(3001),
		Protocol:     "WO-2026-0042",
		HistoryOffer: transfer.HistoryOffer{Mode: "from"},
	})
	aRepair, err := neg.Accept(ctx, pRepair.ID, transfer.HistorySpec{Mode: "from", From: time.Now().Add(-30 * 24 * time.Hour)})
	if err != nil {
		fatal("repair accept", err)
	}
	neg.Complete(ctx, aRepair.ID)
	ok(fmt.Sprintf("device 2 → serviceco tenant  (state: %s)", aRepair.State))

	pReturn, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     deviceIDs[1],
		From:         serviceRef(3001),
		To:           retailRef(2001),
		Protocol:     "WO-2026-0042-RTN",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	aReturn, err := neg.Accept(ctx, pReturn.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		fatal("return accept", err)
	}
	neg.Complete(ctx, aReturn.ID)
	ok(fmt.Sprintf("device 2 returned to retailchain tenant  (state: %s)", aReturn.State))

	recBack, _ := reg.Get(ctx, deviceIDs[1])
	detail("current owner", fmt.Sprintf("xolu-hub tenant=%d device=%d", recBack.Current.TenantID, recBack.Current.LocalID))
	detail("total transfers", fmt.Sprintf("%d", len(recBack.History)))

	// ── Phase 5: Retirement ───────────────────────────────────────────────────
	header("Phase 5 · Retirement")
	step(5, "Device 3 end-of-life")
	reg.Retire(ctx, deviceIDs[2], "exceeded service life")
	ok("device 3 RETIRED")

	// ── Phase 6: Final state ──────────────────────────────────────────────────
	header("Phase 6 · Final state")
	time.Sleep(80 * time.Millisecond)
	for i, id := range deviceIDs {
		rec, _ := reg.Get(ctx, id)
		fmt.Printf("    %sdevice %d%s  status=%-8s  tenant=%-12s  localID=%d  transfers=%d\n",
			colBold, i+1, colReset,
			string(rec.Status),
			tenantName(rec.Current.TenantID),
			rec.Current.LocalID,
			len(rec.History))
	}

	header("Demo 2 complete")
	fmt.Printf("  Key insight: nolu's identity model is instance-agnostic.\n")
	fmt.Printf("  Whether orgs are separate xolu instances or tenants in one,\n")
	fmt.Printf("  the clearinghouse operations are identical.\n\n")
}

func tenantName(id uint16) string {
	switch id {
	case tenantVendo:
		return "vendocorp"
	case tenantRetail:
		return "retailchain"
	case tenantService:
		return "serviceco"
	default:
		return fmt.Sprintf("tenant-%d", id)
	}
}

func formatRef(ref identity.LocalRef, hubURL string) string {
	if ref.InstanceURL == hubURL {
		return fmt.Sprintf("hub/%s/%d", tenantName(ref.TenantID), ref.LocalID)
	}
	inst := strings.TrimPrefix(ref.InstanceURL, "http://xolu-")
	return fmt.Sprintf("%s/%s:%d", inst, ref.EntityType, ref.LocalID)
}

// ── Shared display helpers (copied from demo1 to keep binaries self-contained)

const (
	colReset  = "\033[0m"
	colBold   = "\033[1m"
	colGreen  = "\033[32m"
	colYellow = "\033[33m"
	colBlue   = "\033[34m"
	colCyan   = "\033[36m"
	colRed    = "\033[31m"
	colGray   = "\033[90m"
)

func header(title string) {
	fmt.Printf("\n%s%s━━━  %s  ━━━%s\n\n", colBold, colCyan, title, colReset)
}
func step(n int, msg string) {
	fmt.Printf("%s[%d]%s %s%s%s\n", colBold+colBlue, n, colReset, colBold, msg, colReset)
}
func ok(msg string)   { fmt.Printf("    %s✓%s  %s\n", colGreen, colReset, msg) }
func warn(msg string) { fmt.Printf("    %s✗%s  %s\n", colYellow, colReset, msg) }
func detail(k, v string) {
	fmt.Printf("    %s%-18s%s %s\n", colGray, k, colReset, v)
}
func event(kind, gid, detail string) {
	fmt.Printf("    %s▶ EVENT%s %-14s %s%s%s  %s\n",
		colCyan, colReset, kind, colGray, shortID(gid), colReset, detail)
}
func shortID(gid string) string {
	parts := strings.Split(gid, "/")
	if len(parts) < 1 {
		return gid
	}
	uid := parts[len(parts)-1]
	if len(uid) > 8 {
		return "…" + uid[len(uid)-8:]
	}
	return uid
}
func fatal(op string, err error) {
	fmt.Printf("%sFATAL%s %s: %v\n", colRed, colReset, op, err)
	os.Exit(1)
}
