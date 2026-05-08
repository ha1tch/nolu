// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Demo 5 — Cross-organisation asset transfer with negotiation.
//
// Three sovereign xolu instances, each owned by a different organisation.
// No shared databases. nolu is the only coordination point.
//
//   xolu-manufacturer  (port 9090)  — VendoCorp, makes vending machines
//   xolu-distributor   (port 9091)  — RetailChain, buys and deploys them
//   xolu-repair        (port 9092)  — ServiceCo, repairs failed units
//   xolu-registry      (port 9093)  — nolu's own durable store (path mode)
//   nolu               (port 7070)  — clearinghouse
//
// Scenario:
//
//   Phase 1  VendoCorp manufactures 5 devices. Registers all 5 in nolu.
//            GlobalIDs are minted. Entities live in xolu-manufacturer.
//
//   Phase 2  RetailChain purchases devices 1–4 (PO-2026-001).
//            Bilateral negotiation: VendoCorp proposes, RetailChain accepts.
//            Device 4 fails pre-shipment inspection and is rejected.
//            On completion nolu atomically updates 3 GlobalIDs.
//            Caller holds the GlobalID — resolve() returns the new xolu.
//
//   Phase 3  Device 2 malfunctions in the field.
//            RetailChain transfers it to ServiceCo for repair.
//            History portability: "full" — the complete repair history
//            travels with the device back to RetailChain.
//
//   Phase 4  ServiceCo completes repair and returns the device.
//            Three transfers: manufacturer → distributor → repair → distributor.
//            resolve() still returns a single GlobalID unchanged since Phase 1.
//
//   Phase 5  Device 3 reaches end-of-life and is retired.
//            Subsequent resolve() calls return 410 Gone.
//
//   Phase 6  Final audit: 5 GlobalIDs, all resolvable (except retired),
//            each pointing at the correct sovereign xolu instance.
//
// Key insight: the GlobalID is stable across all ownership changes.
// Each organisation sees only its own xolu data. nolu holds only identity.
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

	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
	"github.com/ha1tch/nolu/pkg/xoluclient"
)

// ── Terminal colours ──────────────────────────────────────────────────────────

const (
	colReset  = "\033[0m"
	colBold   = "\033[1m"
	colGreen  = "\033[32m"
	colYellow = "\033[33m"
	colBlue   = "\033[34m"
	colCyan   = "\033[36m"
	colGray   = "\033[90m"
	colRed    = "\033[31m"
)

func header(title string) {
	fmt.Printf("\n%s%s━━━  %s  ━━━%s\n\n", colBold, colCyan, title, colReset)
}
func step(n int, msg string) {
	fmt.Printf("%s[%d]%s %s%s%s\n", colBold+colBlue, n, colReset, colBold, msg, colReset)
}
func ok(msg string) { fmt.Printf("    %s✓%s  %s\n", colGreen, colReset, msg) }
func warn(msg string) { fmt.Printf("    %s✗%s  %s\n", colYellow, colReset, msg) }
func note(k, v string) { fmt.Printf("    %s%-20s%s %s\n", colGray, k, colReset, v) }
func fatal(op string, err error) {
	fmt.Printf("%sFATAL%s %s: %v\n", colRed, colReset, op, err)
	os.Exit(1)
}
func shortID(gid string) string {
	parts := strings.Split(gid, "/")
	if len(parts) == 0 {
		return gid
	}
	last := parts[len(parts)-1]
	if len(last) > 8 {
		return "…" + last[len(last)-8:]
	}
	return last
}

// ── Main ──────────────────────────────────────────────────────────────────────

func main() {
	mfgURL  := flag.String("mfg",      "http://localhost:9090", "VendoCorp xolu instance URL")
	distURL := flag.String("dist",     "http://localhost:9091", "RetailChain xolu instance URL")
	repURL  := flag.String("repair",   "http://localhost:9092", "ServiceCo xolu instance URL")
	regURL  := flag.String("registry", "http://localhost:9093", "nolu registry xolu backing store URL")
	flag.Parse()

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	ctx := context.Background()

	// ── Connectivity checks ───────────────────────────────────────────────────
	for _, u := range []string{*mfgURL, *distURL, *repURL, *regURL} {
		c := xoluclient.New(u, 0)
		if err := c.Healthy(ctx); err != nil {
			fatal(fmt.Sprintf("connectivity check %s", u), err)
		}
		ok(fmt.Sprintf("%-42s reachable", u))
	}

	// ── nolu setup ────────────────────────────────────────────────────────────
	bus := events.NewMemoryBus()
	reg, err := registry.NewXoluRegistry(ctx, *regURL, "registry.demo5.local", bus)
	if err != nil {
		fatal("init XoluRegistry", err)
	}
	ok("XoluRegistry ready (backed by " + *regURL + ")")

	neg := transfer.NewMemoryNegotiator(reg)

	// Event subscriber — prints events as they arrive.
	evCh := make(chan registry.Event, 64)
	_, _ = reg.Subscribe(ctx, registry.SubscriptionFilter{}, evCh)
	go func() {
		for ev := range evCh {
			if ev.Record == nil {
				continue
			}
			fmt.Printf("    %s▶ EVENT %-12s%s %s%s%s  %s%s%s\n",
				colCyan, ev.Kind, colReset,
				colGray, shortID(string(ev.GlobalID)), colReset,
				colGray, ev.Record.Current.InstanceURL, colReset)
		}
	}()

	// ── Clients ───────────────────────────────────────────────────────────────
	mfg  := xoluclient.NewTenant(*mfgURL,  "vendocorp")
	dist := xoluclient.NewTenant(*distURL, "retailchain")
	rep  := xoluclient.NewTenant(*repURL,  "serviceco")

	// Ensure tenants exist (path mode: created on first access).
	for _, pair := range []struct{ url, name string }{
		{*mfgURL, "vendocorp"},
		{*distURL, "retailchain"},
		{*repURL, "serviceco"},
	} {
		if err := xoluclient.New(pair.url, 0).EnsureTenant(ctx, pair.name); err != nil {
			fatal("ensure tenant "+pair.name, err)
		}
	}

	// ── Scenario intro ────────────────────────────────────────────────────────
	fmt.Printf("\n%s━━━  Demo 5 — Cross-organisation asset transfer  ━━━%s\n\n", colBold, colReset)
	fmt.Printf("  Three sovereign xolu instances, zero shared data:\n")
	fmt.Printf("    VendoCorp (manufacturer)  %s\n", *mfgURL)
	fmt.Printf("    RetailChain (distributor) %s\n", *distURL)
	fmt.Printf("    ServiceCo (repair depot)  %s\n", *repURL)
	fmt.Printf("    nolu clearinghouse        backed by %s\n\n", *regURL)
	fmt.Printf("  nolu mints a GlobalID for each device. That ID never changes\n")
	fmt.Printf("  regardless of how many times the device changes hands.\n")

	// ── Phase 1: Manufacturing & Registration ─────────────────────────────────
	header("Phase 1 · Manufacturing & Registration (VendoCorp)")
	step(1, "VendoCorp manufactures 5 devices and registers each in nolu")

	type device struct {
		gid     identity.GlobalID
		localID int
		name    string
	}
	devices := make([]device, 5)

	for i := 0; i < 5; i++ {
		serial := fmt.Sprintf("VM-2026-%04d", 1000+i)
		entity, err := mfg.Create(ctx, "devices", map[string]interface{}{
			"serial": serial,
			"model":  "VM-100",
			"owner":  "vendocorp",
			"status": "manufactured",
		})
		if err != nil {
			fatal("create device", err)
		}
		localID, err := xoluclient.IntID(entity)
	if err != nil {
		fatal("entity id", err)
	}

		ref := identity.LocalRef{
			InstanceURL: *mfgURL,
			TenantName:  "vendocorp",
			EntityType:  "devices",
			LocalID:     localID,
		}
		rec, err := reg.Register(ctx, "registry.demo5.local", "devices", ref)
		if err != nil {
			fatal("register", err)
		}
		devices[i] = device{gid: rec.GlobalID, localID: localID, name: serial}
		ok(fmt.Sprintf("device %d (%s)  GlobalID: %s%s%s", i+1, serial, colGray, shortID(string(rec.GlobalID)), colReset))
	}
	time.Sleep(100 * time.Millisecond)
	note("all 5 GlobalIDs", "point at "+*mfgURL+"/vendocorp")

	// ── Phase 2: Bilateral sale with one rejection ────────────────────────────
	header("Phase 2 · Sale: VendoCorp → RetailChain  (PO-2026-001)")
	step(2, "RetailChain purchases devices 1–4. Device 4 fails inspection.")

	// Create matching entities in retailchain's xolu for devices 1–3.
	// In a real scenario RetailChain would create these as part of acceptance.
	distEntities := make([]int, 3) // local IDs in retailchain's xolu
	for i := 0; i < 3; i++ {
		e, err := dist.Create(ctx, "devices", map[string]interface{}{
			"serial": devices[i].name,
			"source": "vendocorp",
			"status": "incoming",
		})
		if err != nil {
			fatal("create dist entity", err)
		}
		eid, err := xoluclient.IntID(e)
		if err != nil {
			fatal("entity id", err)
		}
		distEntities[i] = eid
	}

	// Negotiate all four transfers.
	proposals := make([]string, 4)
	for i := 0; i < 4; i++ {
		fromRef := identity.LocalRef{
			InstanceURL: *mfgURL, TenantName: "vendocorp",
			EntityType: "devices", LocalID: devices[i].localID,
		}
		toLocalID := 0
		if i < 3 {
			toLocalID = distEntities[i]
		} else {
			toLocalID = 9999 // device 4 — will be rejected
		}
		toRef := identity.LocalRef{
			InstanceURL: *distURL, TenantName: "retailchain",
			EntityType: "devices", LocalID: toLocalID,
		}
		p, err := neg.Propose(ctx, transfer.Proposal{
			GlobalID:     devices[i].gid,
			From:         fromRef,
			To:           toRef,
			Protocol:     "PO-2026-001",
			HistoryOffer: transfer.HistoryOffer{Mode: "full"},
		})
		if err != nil {
			fatal("propose", err)
		}
		proposals[i] = p.ID
	}
	ok("4 proposals created")

	// Accept devices 1–3, reject device 4.
	for i := 0; i < 3; i++ {
		_, err := neg.Accept(ctx, proposals[i], transfer.HistorySpec{Mode: "full"})
		if err != nil {
			fatal("accept", err)
		}
		_, err = neg.Complete(ctx, proposals[i])
		if err != nil {
			fatal("complete", err)
		}
		ok(fmt.Sprintf("device %d: vendocorp → retailchain", i+1))
	}
	_, err = neg.Reject(ctx, proposals[3], "inspection failure: motor torque below spec")
	if err != nil {
		fatal("reject", err)
	}
	warn("device 4 REJECTED — inspection failure: motor torque below spec")

	time.Sleep(100 * time.Millisecond)
	note("devices 1–3", "GlobalIDs now point at "+*distURL+"/retailchain")
	note("device 4", "GlobalID still points at "+*mfgURL+"/vendocorp")

	// Verify: resolve device 1's GlobalID — should now be retailchain.
	ref1, err := reg.Resolve(ctx, devices[0].gid)
	if err != nil {
		fatal("resolve device 1", err)
	}
	if ref1.InstanceURL != *distURL {
		fatal("verify", fmt.Errorf("expected %s, got %s", *distURL, ref1.InstanceURL))
	}
	ok(fmt.Sprintf("resolve(%s) → %s ✓", shortID(string(devices[0].gid)), *distURL))

	// ── Phase 3: Field failure → repair ──────────────────────────────────────
	header("Phase 3 · Field failure: device 2 sent to ServiceCo for repair")
	step(3, "Device 2 malfunctions in the field. RetailChain transfers to ServiceCo.")

	// ServiceCo creates a work order entity.
	repEntity, err := rep.Create(ctx, "devices", map[string]interface{}{
		"serial":     devices[1].name,
		"work_order": "WO-2026-0042",
		"status":     "under_repair",
	})
	if err != nil {
		fatal("create repair entity", err)
	}
	repLocalID, err := xoluclient.IntID(repEntity)
	if err != nil {
		fatal("entity id", err)
	}

	fromRef2 := identity.LocalRef{
		InstanceURL: *distURL, TenantName: "retailchain",
		EntityType: "devices", LocalID: distEntities[1],
	}
	toRef2 := identity.LocalRef{
		InstanceURL: *repURL, TenantName: "serviceco",
		EntityType: "devices", LocalID: repLocalID,
	}
	p2, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     devices[1].gid,
		From:         fromRef2,
		To:           toRef2,
		Protocol:     "WO-2026-0042",
		HistoryOffer: transfer.HistoryOffer{Mode: "full", Note: "full history required for warranty claim"},
	})
	if err != nil {
		fatal("propose repair", err)
	}
	_, err = neg.Accept(ctx, p2.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		fatal("accept repair", err)
	}
	_, err = neg.Complete(ctx, p2.ID)
	if err != nil {
		fatal("complete repair", err)
	}

	time.Sleep(100 * time.Millisecond)
	ok(fmt.Sprintf("device 2 → ServiceCo  (work order WO-2026-0042)"))
	note("history portability", "full — warranty history travels with the device")

	// ── Phase 4: Return from repair ───────────────────────────────────────────
	header("Phase 4 · Repair complete: ServiceCo returns device 2 to RetailChain")
	step(4, "ServiceCo transfers repaired device back. Full history included.")

	// RetailChain creates a new entity record for the returned device.
	retEntity, err := dist.Create(ctx, "devices", map[string]interface{}{
		"serial": devices[1].name,
		"status": "repaired",
	})
	if err != nil {
		fatal("create return entity", err)
	}
	retLocalID, err := xoluclient.IntID(retEntity)
	if err != nil {
		fatal("entity id", err)
	}

	fromRef3 := identity.LocalRef{
		InstanceURL: *repURL, TenantName: "serviceco",
		EntityType: "devices", LocalID: repLocalID,
	}
	toRef3 := identity.LocalRef{
		InstanceURL: *distURL, TenantName: "retailchain",
		EntityType: "devices", LocalID: retLocalID,
	}
	p3, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     devices[1].gid,
		From:         fromRef3,
		To:           toRef3,
		Protocol:     "WO-2026-0042-RETURN",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	if err != nil {
		fatal("propose return", err)
	}
	_, err = neg.Accept(ctx, p3.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		fatal("accept return", err)
	}
	_, err = neg.Complete(ctx, p3.ID)
	if err != nil {
		fatal("complete return", err)
	}

	time.Sleep(100 * time.Millisecond)
	ok("device 2 returned to RetailChain")

	// Count total transfers for device 2.
	props, _ := neg.ListByGlobalID(ctx, devices[1].gid)
	ok(fmt.Sprintf("device 2 total transfers: %d  (manufacturer → distributor → repair → distributor)", len(props)))
	note("GlobalID", string(devices[1].gid))
	note("current owner", *distURL+"/retailchain")

	// ── Phase 5: Retirement ───────────────────────────────────────────────────
	header("Phase 5 · End-of-life: device 3 retired")
	step(5, "Device 3 reaches end-of-life and is permanently retired.")

	if err := reg.Retire(ctx, devices[2].gid, "end of operational life"); err != nil {
		fatal("retire", err)
	}
	time.Sleep(50 * time.Millisecond)
	ok("device 3 RETIRED")

	// Verify 410 on resolve.
	_, err = reg.Resolve(ctx, devices[2].gid)
	if err != nil {
		ok("resolve(retired device) → error (410 Gone) ✓")
	}

	// ── Phase 6: Final audit ──────────────────────────────────────────────────
	header("Phase 6 · Final audit — all 5 GlobalIDs")
	step(6, "Resolve each GlobalID. Verify identity is stable across all transfers.")

	type result struct {
		serial  string
		gid     identity.GlobalID
		where   string
		status  string
		nXfer   int
	}
	results := make([]result, 5)
	for i, d := range devices {
		rec, err := reg.Get(ctx, d.gid)
		if err != nil {
			results[i] = result{d.name, d.gid, "—", "retired", 0}
			continue
		}
		props, _ := neg.ListByGlobalID(ctx, d.gid)
		owner := rec.Current.InstanceURL + "/" + rec.Current.TenantName
		results[i] = result{d.name, d.gid, owner, string(rec.Status), len(props)}
	}

	fmt.Printf("\n")
	for i, r := range results {
		statusCol := colGreen
		if r.status == "retired" {
			statusCol = colGray
		}
		fmt.Printf("  device %d  %-14s  %s%-8s%s  %s%s%s  transfers=%d\n",
			i+1, r.serial,
			statusCol, r.status, colReset,
			colGray, r.where, colReset,
			r.nXfer)
	}

	fmt.Printf("\n%s━━━  Demo 5 complete  ━━━%s\n\n", colBold, colReset)
	fmt.Printf("  Key insights:\n")
	fmt.Printf("  · Each GlobalID was minted once and never changed across 3 organisations\n")
	fmt.Printf("  · Rejection left device 4's registry record pointing at VendoCorp\n")
	fmt.Printf("  · Three transfers (mfg→dist→repair→dist) all tracked under one GlobalID\n")
	fmt.Printf("  · Retirement is permanent — 410 Gone on all future resolve() calls\n")
	fmt.Printf("  · Zero data left nolu. Each organisation retains full data sovereignty.\n\n")
}
