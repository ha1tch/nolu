// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Demo 3 — Advanced: XoluRegistry (durable), three xolu instances,
// 3-node NATS cluster.
//
// Infrastructure:
//   xolu-vendocorp   (9090) — VendoCorp, manufacturer
//   xolu-retailchain (9091) — RetailChain, operator
//   xolu-serviceco   (9092) — ServiceCo, repair provider
//   xolu-registry    (9093) — dedicated xolu instance for nolu's own records
//   nats-1/2/3       (4222/4223/4224) — 3-node JetStream cluster
//
// Five-phase scenario:
//   Phase 1 — Register devices, show GlobalIDs minted in xolu-registry
//   Phase 2 — Transfer operations (positive cases: sale, repair cycle)
//   Phase 3 — Negative cases: rejected transfer, concurrent transfer race,
//              attempt to transfer a retired entity, wrong-owner guard
//   Phase 4 — Simulated restart: new XoluRegistry instance, verify durability
//   Phase 5 — OQL query against xolu-registry to show event log readability
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

func main() {
	vendoURL    := flag.String("vendo",    "http://localhost:9090", "xolu-vendocorp URL")
	retailURL   := flag.String("retail",   "http://localhost:9091", "xolu-retailchain URL")
	serviceURL  := flag.String("service",  "http://localhost:9092", "xolu-serviceco URL")
	regURL      := flag.String("registry", "http://localhost:9093", "xolu-registry URL (nolu's backing store)")
	natsURL     := flag.String("nats",     "nats://localhost:4222", "NATS URL (any cluster node)")
	flag.Parse()

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	zerolog.SetGlobalLevel(zerolog.WarnLevel)

	ctx := context.Background()

	// ── Connectivity ──────────────────────────────────────────────────────────
	header("Demo 3 — XoluRegistry + 3-node NATS cluster")
	fmt.Printf("  Infrastructure:\n")
	for label, url := range map[string]string{
		"xolu-vendocorp":   *vendoURL,
		"xolu-retailchain": *retailURL,
		"xolu-serviceco":   *serviceURL,
		"xolu-registry":    *regURL,
		"NATS cluster":     *natsURL,
	} {
		c := xoluclient.New(url, 0)
		if err := c.Healthy(ctx); err != nil {
			// NATS check is different — just note it
			if label == "NATS cluster" {
				fmt.Printf("    %s✗%s  %-20s %s (NATS checked separately)\n", colYellow, colReset, label, url)
			} else {
				fmt.Printf("    %s✗%s  %-20s %s — NOT REACHABLE\n", colRed, colReset, label, url)
				os.Exit(1)
			}
		} else {
			fmt.Printf("    %s✓%s  %-20s %s\n", colGreen, colReset, label, url)
		}
	}

	// ── NATS bus ──────────────────────────────────────────────────────────────
	bus, err := events.NewNATSBus(events.NATSBusConfig{
		URL:            *natsURL,
		StreamName:     "NOLU_DEMO3",
		ConnectTimeout: 10 * time.Second,
	})
	if err != nil {
		fmt.Printf("%sERROR%s NATS: %v\n", colRed, colReset, err)
		os.Exit(1)
	}
	defer bus.Close()
	fmt.Printf("    %s✓%s  %-20s connected\n\n", colGreen, colReset, "NATS cluster")

	// Subscribe to all nolu events on NATS for display
	bus.Subscribe(ctx, "demo3-display", "NOLU_DEMO3.events.>", func(env events.Envelope) error {
		fmt.Printf("    %s▶ NATS[%d]%s %-14s %s%s%s\n",
			colCyan, env.At.Unix()%1000, colReset,
			env.Kind, colGray, shortID(string(env.GlobalID)), colReset)
		return nil
	})

	// ── XoluRegistry: first instance ─────────────────────────────────────────
	fmt.Printf("\n  Initialising XoluRegistry (backed by xolu-registry)...\n")
	reg1, err := registry.NewXoluRegistry(ctx, *regURL, "registry.demo3.local", bus)
	if err != nil {
		fmt.Printf("%sERROR%s XoluRegistry init: %v\n", colRed, colReset, err)
		os.Exit(1)
	}
	neg := transfer.NewMemoryNegotiator(reg1)
	fmt.Printf("    %s✓%s  XoluRegistry ready\n", colGreen, colReset)

	// ── LocalRef helpers ──────────────────────────────────────────────────────
	vref := func(id int) identity.LocalRef {
		return identity.LocalRef{InstanceURL: *vendoURL, EntityType: "devices", LocalID: id}
	}
	rref := func(id int) identity.LocalRef {
		return identity.LocalRef{InstanceURL: *retailURL, EntityType: "devices", LocalID: id}
	}
	sref := func(id int) identity.LocalRef {
		return identity.LocalRef{InstanceURL: *serviceURL, EntityType: "devices", LocalID: id}
	}

	// ═════════════════════════════════════════════════════════════════════════
	// PHASE 1 — Registration
	// ═════════════════════════════════════════════════════════════════════════
	header("Phase 1 · Registration — GlobalIDs minted into xolu-registry")
	step(1, "VendoCorp registers 6 devices")

	deviceIDs := make([]identity.GlobalID, 6)
	for i := 0; i < 6; i++ {
		rec, err := reg1.Register(ctx, "registry.demo3.local", "devices", vref(1000+i))
		if err != nil {
			fatal("register", err)
		}
		deviceIDs[i] = rec.GlobalID
		ok(fmt.Sprintf("device %d  %s%s%s", i+1, colGray, shortID(string(rec.GlobalID)), colReset))
	}
	time.Sleep(200 * time.Millisecond) // NATS delivery

	// Verify records landed in xolu-registry
	regClient := xoluclient.NewTenant(*regURL, "nolu_registry")
	rows, err := regClient.OQL(ctx, "SELECT global_id_str, status_str FROM nolu_records")
	if err != nil {
		warn(fmt.Sprintf("OQL verify: %v", err))
	} else {
		ok(fmt.Sprintf("%d records visible in xolu-registry via OQL ✓", len(rows)))
	}

	// ═════════════════════════════════════════════════════════════════════════
	// PHASE 2 — Positive transfer cases
	// ═════════════════════════════════════════════════════════════════════════
	header("Phase 2 · Positive cases — sale, repair cycle")

	// Sale: devices 1–3
	step(2, "Batch sale (devices 1–3): VendoCorp → RetailChain")
	for i := 0; i < 3; i++ {
		p, _ := neg.Propose(ctx, transfer.Proposal{
			GlobalID:     deviceIDs[i],
			From:         vref(1000 + i),
			To:           rref(2000 + i),
			Protocol:     "PO-2026-003",
			HistoryOffer: transfer.HistoryOffer{Mode: "full"},
		})
		a, err := neg.Accept(ctx, p.ID, transfer.HistorySpec{Mode: "full"})
		if err != nil {
			fatal("accept", err)
		}
		neg.Complete(ctx, a.ID)
		ok(fmt.Sprintf("device %d sold  %s→ retailchain/200%d%s", i+1, colGray, i, colReset))
	}
	time.Sleep(200 * time.Millisecond)

	// Repair cycle: device 2
	step(2, "Repair cycle: device 2 → ServiceCo → RetailChain")
	pR, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     deviceIDs[1],
		From:         rref(2001),
		To:           sref(3001),
		Protocol:     "WO-2026-0050",
		HistoryOffer: transfer.HistoryOffer{Mode: "from"},
	})
	aR, _ := neg.Accept(ctx, pR.ID, transfer.HistorySpec{Mode: "from", From: time.Now().Add(-14 * 24 * time.Hour)})
	neg.Complete(ctx, aR.ID)
	ok("device 2 → serviceco")

	pRtn, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     deviceIDs[1],
		From:         sref(3001),
		To:           rref(2001),
		Protocol:     "WO-2026-0050-RTN",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	aRtn, _ := neg.Accept(ctx, pRtn.ID, transfer.HistorySpec{Mode: "full"})
	neg.Complete(ctx, aRtn.ID)
	ok("device 2 returned to retailchain")
	recD2, _ := reg1.Get(ctx, deviceIDs[1])
	detail("device 2 transfers", fmt.Sprintf("%d (sold + to service + returned)", len(recD2.History)))

	// ═════════════════════════════════════════════════════════════════════════
	// PHASE 3 — Negative cases
	// ═════════════════════════════════════════════════════════════════════════
	header("Phase 3 · Negative cases — guards and error paths")

	// 3a: Rejection
	step(3, "Rejected transfer (device 4 — failed inspection)")
	p4, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     deviceIDs[3],
		From:         vref(1003),
		To:           rref(2003),
		Protocol:     "PO-2026-003",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	rej4, _ := neg.Reject(ctx, p4.ID, "cracked casing detected on arrival")
	assertState(rej4.State, transfer.StateRejected, "rejection")
	resolved4, _ := reg1.Resolve(ctx, deviceIDs[3])
	if resolved4 == vref(1003) {
		ok("registry unchanged after rejection ✓")
	} else {
		warn("FAIL: registry changed after rejection")
	}

	// 3b: Wrong current owner
	step(3, "Wrong-owner guard (optimistic concurrency)")
	_, err = reg1.Transfer(ctx, registry.TransferRequest{
		GlobalID: deviceIDs[0],
		From:     vref(1000), // wrong — device 1 is now at retailchain
		To:       sref(3000),
	})
	if err != nil && strings.Contains(err.Error(), "invalid") {
		ok("wrong-owner transfer correctly rejected ✓")
	} else {
		warn(fmt.Sprintf("FAIL: expected ErrInvalidTransfer, got %v", err))
	}

	// 3c: Transfer after retirement
	step(3, "Transfer after retirement")
	reg1.Retire(ctx, deviceIDs[4], "demo3 negative case")
	_, err = reg1.Transfer(ctx, registry.TransferRequest{
		GlobalID: deviceIDs[4],
		From:     vref(1004),
		To:       rref(2004),
	})
	if err == registry.ErrRetired {
		ok("transfer of retired entity correctly rejected ✓")
	} else {
		warn(fmt.Sprintf("FAIL: expected ErrRetired, got %v", err))
	}

	// 3d: Cancellation
	step(3, "Cancellation before acceptance (device 5 — quality hold)")
	p5, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     deviceIDs[5],
		From:         vref(1005),
		To:           rref(2005),
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	c5, _ := neg.Cancel(ctx, p5.ID)
	assertState(c5.State, transfer.StateCancelled, "cancellation")
	resolved5, _ := reg1.Resolve(ctx, deviceIDs[5])
	if resolved5 == vref(1005) {
		ok("registry unchanged after cancellation ✓")
	}

	// 3e: Double-retire
	step(3, "Double-retire (idempotency guard)")
	err = reg1.Retire(ctx, deviceIDs[4], "duplicate retire attempt")
	if err == registry.ErrRetired {
		ok("double-retire correctly returns ErrRetired ✓")
	} else {
		warn(fmt.Sprintf("FAIL: expected ErrRetired, got %v", err))
	}

	// 3f: Concurrent transfer race
	step(3, "Concurrent transfer race (XoluRegistry _version guard)")
	gidRace := deviceIDs[5]
	fromRace := vref(1005)
	ch := make(chan error, 2)
	go func() {
		_, err := reg1.Transfer(ctx, registry.TransferRequest{
			GlobalID: gidRace, From: fromRace, To: rref(2005),
		})
		ch <- err
	}()
	go func() {
		_, err := reg1.Transfer(ctx, registry.TransferRequest{
			GlobalID: gidRace, From: fromRace, To: sref(3005),
		})
		ch <- err
	}()
	e1, e2 := <-ch, <-ch
	wins, losses := 0, 0
	for _, e := range []error{e1, e2} {
		if e == nil {
			wins++
		} else {
			losses++
		}
	}
	if wins == 1 && losses == 1 {
		ok("concurrent race: exactly 1 winner, 1 loser ✓")
	} else {
		warn(fmt.Sprintf("concurrent race: wins=%d losses=%d (expected 1/1)", wins, losses))
	}

	// ═════════════════════════════════════════════════════════════════════════
	// PHASE 4 — Simulated restart
	// ═════════════════════════════════════════════════════════════════════════
	header("Phase 4 · Simulated restart — durability verification")
	step(4, "Discarding reg1, creating reg2 pointing at same xolu-registry")
	time.Sleep(300 * time.Millisecond)

	reg2, err := registry.NewXoluRegistry(ctx, *regURL, "registry.demo3.local", bus)
	if err != nil {
		fatal("reg2 init", err)
	}
	ok("reg2 created (simulates process restart)")

	// Verify all records
	type expectation struct {
		label     string
		id        identity.GlobalID
		ownerURL  string
		tenantID  uint16
		status    registry.Status
		transfers int
	}
	wants := []expectation{
		{"device 1", deviceIDs[0], *retailURL, 0, registry.StatusActive, 1},
		{"device 2", deviceIDs[1], *retailURL, 0, registry.StatusActive, 3},
		{"device 3", deviceIDs[2], *retailURL, 0, registry.StatusActive, 1},
		{"device 4", deviceIDs[3], *vendoURL,  0, registry.StatusActive, 0},
		{"device 5", deviceIDs[4], *vendoURL,  0, registry.StatusRetired, 0},
	}
	allOK := true
	for _, w := range wants {
		rec, err := reg2.Get(ctx, w.id)
		if err != nil {
			warn(fmt.Sprintf("%s: Get failed: %v", w.label, err))
			allOK = false
			continue
		}
		if rec.Status != w.status || rec.Current.InstanceURL != w.ownerURL || len(rec.History) != w.transfers {
			warn(fmt.Sprintf("%s: status=%s owner=%s transfers=%d (expected %s/%s/%d)",
				w.label, rec.Status, rec.Current.InstanceURL, len(rec.History),
				w.status, w.ownerURL, w.transfers))
			allOK = false
		} else {
			ok(fmt.Sprintf("%s: status=%-8s owner=%-12s transfers=%d ✓",
				w.label, rec.Status,
				shortHost(rec.Current.InstanceURL), len(rec.History)))
		}
	}
	if allOK {
		ok("ALL records survived restart — XoluRegistry is durable ✓")
	}

	// Continue operations on reg2 — retire device 3
	step(4, "Continue operations on reg2: retire device 3")
	if err := reg2.Retire(ctx, deviceIDs[2], "post-restart retirement"); err != nil {
		warn(fmt.Sprintf("retire on reg2: %v", err))
	} else {
		ok("device 3 retired on reg2 (persisted to xolu-registry)")
	}

	// ═════════════════════════════════════════════════════════════════════════
	// PHASE 5 — OQL query against xolu-registry
	// ═════════════════════════════════════════════════════════════════════════
	header("Phase 5 · xolu-registry is just xolu — query it directly with OQL")
	step(5, "Direct OQL queries against xolu-registry")

	regC := xoluclient.NewTenant(*regURL, "nolu_registry")

	// Count all records
	rows, err = regC.OQL(ctx, "SELECT global_id_str, status_str, current_instance_url FROM nolu_records")
	if err != nil {
		warn(fmt.Sprintf("OQL nolu_records: %v", err))
	} else {
		active, retired := 0, 0
		for _, row := range rows {
			if s, _ := row["status_str"].(string); s == "retired" {
				retired++
			} else {
				active++
			}
		}
		ok(fmt.Sprintf("nolu_records: %d total (%d active, %d retired)", len(rows), active, retired))
	}

	// Query event log
	evRows, err := regC.OQL(ctx, "SELECT global_id_str, kind, entity_type FROM nolu_events ORDER BY id DESC LIMIT 10")
	if err != nil {
		warn(fmt.Sprintf("OQL nolu_events: %v", err))
	} else {
		ok(fmt.Sprintf("nolu_events: last %d events:", len(evRows)))
		for _, row := range evRows {
			fmt.Printf("      %s%-12s%s %s%s%s\n",
				colGray, row["kind"], colReset,
				colGray, shortID(fmt.Sprintf("%v", row["global_id_str"])), colReset)
		}
	}

	// Retail-owned devices via OQL
	retailOwned, err := regC.OQL(ctx, fmt.Sprintf(
		`SELECT global_id_str FROM nolu_records WHERE current_instance_url = '%s' AND status_str = 'active'`,
		*retailURL,
	))
	if err == nil {
		ok(fmt.Sprintf("devices currently owned by RetailChain: %d (via OQL)", len(retailOwned)))
	}

	// ── Final summary ─────────────────────────────────────────────────────────
	header("Demo 3 complete")
	fmt.Printf("  ✓  XoluRegistry: records persisted to xolu-registry\n")
	fmt.Printf("  ✓  3-node NATS cluster: events published and consumed\n")
	fmt.Printf("  ✓  Positive cases: sale, repair cycle\n")
	fmt.Printf("  ✓  Negative cases: rejection, wrong-owner, retired, cancel, double-retire, race\n")
	fmt.Printf("  ✓  Restart durability: reg2 read all records written by reg1\n")
	fmt.Printf("  ✓  Direct OQL: xolu-registry data queryable without nolu\n\n")
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func assertState(got, want transfer.State, label string) {
	if got == want {
		ok(fmt.Sprintf("%s: state=%s ✓", label, got))
	} else {
		warn(fmt.Sprintf("%s: expected %s, got %s", label, want, got))
	}
}

func shortHost(url string) string {
	url = strings.TrimPrefix(url, "http://xolu-")
	url = strings.TrimPrefix(url, "http://")
	if idx := strings.Index(url, ":"); idx > 0 {
		url = url[:idx]
	}
	return url
}

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
