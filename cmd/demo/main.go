// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// demo runs a simulated nolu clearinghouse scenario involving three
// organisations and a set of vending machine assets moving through their
// real-world lifecycle:
//
//   VendoCorp      — manufacturer, creates devices and sells them
//   RetailChain    — operator, buys devices and deploys them in stores
//   ServiceCo      — maintenance provider, temporarily holds devices for repair
//
// The scenario exercises:
//
//   1. Asset registration (VendoCorp registers newly manufactured devices)
//   2. Transfer proposal + acceptance (sale from VendoCorp to RetailChain)
//   3. Transfer rejection (RetailChain rejects a damaged unit)
//   4. Transfer cancellation (VendoCorp cancels a pending proposal)
//   5. Sub-transfer for repair (RetailChain → ServiceCo → RetailChain)
//   6. Asset retirement (end-of-life decommission)
//   7. NATS event observation (a fourth process watches the event stream)
//
// When run with -bus=nats, all events flow through NATS JetStream and the
// observer receives them as durable push-consumer messages.
package main

import (
	"context"
	"encoding/json"
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
)

// ── Simulated xolu instance endpoints ───────────────────────────────────────

const (
	vendoCorpURL   = "http://xolu-vendocorp:9090"
	retailChainURL = "http://xolu-retailchain:9091"
	serviceCoURL   = "http://xolu-serviceco:9092"
)

// ── Colour helpers for terminal output ──────────────────────────────────────

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

func ok(msg string) {
	fmt.Printf("    %s✓%s  %s\n", colGreen, colReset, msg)
}

func warn(msg string) {
	fmt.Printf("    %s✗%s  %s\n", colYellow, colReset, msg)
}

func detail(k, v string) {
	fmt.Printf("    %s%-16s%s %s\n", colGray, k, colReset, v)
}

func event(kind, gid, detail string) {
	fmt.Printf("    %s▶ EVENT%s %-14s %s%s%s  %s\n",
		colCyan, colReset, kind,
		colGray, shortID(gid), colReset,
		detail)
}

func shortID(gid string) string {
	// nolu://host/type/uuid → last 8 chars of uuid
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

func prettyRef(ref identity.LocalRef) string {
	org := strings.TrimPrefix(ref.InstanceURL, "http://xolu-")
	org = strings.TrimSuffix(org, ":9090")
	org = strings.TrimSuffix(org, ":9091")
	org = strings.TrimSuffix(org, ":9092")
	return fmt.Sprintf("%s / %s:%d", org, ref.EntityType, ref.LocalID)
}

// ── Main ─────────────────────────────────────────────────────────────────────

func main() {
	busType := flag.String("bus", "memory", "Event bus type: memory|nats")
	natsURL := flag.String("nats", "nats://nats:4222", "NATS server URL")
	flag.Parse()

	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	zerolog.SetGlobalLevel(zerolog.WarnLevel) // suppress library noise during demo

	ctx := context.Background()

	// ── Bus setup ────────────────────────────────────────────────────────────
	var bus events.Bus
	switch *busType {
	case "nats":
		nb, err := events.NewNATSBus(events.NATSBusConfig{
			URL:            *natsURL,
			StreamName:     "NOLU_EVENTS",
			ConnectTimeout: 15 * time.Second,
		})
		if err != nil {
			fmt.Printf("%sERROR%s connecting to NATS at %s: %v\n", colRed, colReset, *natsURL, err)
			fmt.Printf("Falling back to memory bus.\n")
			bus = events.NewMemoryBus()
		} else {
			defer nb.Close()
			bus = nb
			fmt.Printf("%s✓%s  Connected to NATS at %s\n", colGreen, colReset, *natsURL)
		}
	default:
		bus = events.NewMemoryBus()
		fmt.Printf("%s✓%s  Using in-process memory bus\n", colGreen, colReset)
	}

	// ── Registry and negotiator setup ────────────────────────────────────────
	reg := registry.NewMemoryRegistry("registry.nolu.local", bus)
	neg := transfer.NewMemoryNegotiator(reg)

	// ── Event observer ───────────────────────────────────────────────────────
	// Subscribe to all events and print them as they arrive.
	evCh := make(chan registry.Event, 64)
	cancelSub, _ := reg.Subscribe(ctx, registry.SubscriptionFilter{}, evCh)
	defer cancelSub()

	go func() {
		for ev := range evCh {
			var owner string
			if ev.Record != nil {
				owner = prettyRef(ev.Record.Current)
			}
			event(string(ev.Kind), string(ev.GlobalID), owner)
		}
	}()

	// ── NATS observer (separate consumer, mirrors what a remote process sees) ─
	if *busType == "nats" {
		_, _ = bus.Subscribe(ctx, "demo-observer", events.SubjectAll, func(env events.Envelope) error {
			fmt.Printf("    %s▶ NATS  %s %-14s %s%s%s\n",
				colYellow, colReset, env.Kind,
				colGray, shortID(string(env.GlobalID)), colReset)
			return nil
		})
	}

	// Small pause so the observer goroutine is ready before events start flowing.
	time.Sleep(50 * time.Millisecond)

	// ════════════════════════════════════════════════════════════════════════
	// SCENARIO
	// ════════════════════════════════════════════════════════════════════════

	header("nolu clearinghouse demo")
	fmt.Printf("  Participants:\n")
	fmt.Printf("    VendoCorp    — vending machine manufacturer\n")
	fmt.Printf("    RetailChain  — vending operator (supermarket chain)\n")
	fmt.Printf("    ServiceCo    — authorised repair provider\n\n")

	// ── 1. VendoCorp registers five newly manufactured devices ───────────────
	header("Phase 1 · Manufacturing & Registration")
	step(1, "VendoCorp registers 5 freshly manufactured devices")

	deviceIDs := make([]identity.GlobalID, 5)
	for i := 0; i < 5; i++ {
		rec, err := reg.Register(ctx, "registry.nolu.local", "devices", identity.LocalRef{
			InstanceURL: vendoCorpURL,
			TenantID:    0,
			EntityType:  "devices",
			LocalID:     1000 + i,
		})
		if err != nil {
			fatal("register device", err)
		}
		deviceIDs[i] = rec.GlobalID
		ok(fmt.Sprintf("device %d registered  %s%s%s", i+1, colGray, shortID(string(rec.GlobalID)), colReset))
	}
	time.Sleep(80 * time.Millisecond) // let observer print

	// ── 2. RetailChain subscribes to device events ───────────────────────────
	header("Phase 2 · RetailChain subscribes to devices of interest")
	step(2, "RetailChain watches devices 1–4 (not device 5 — suspected fault)")

	watchCh := make(chan registry.Event, 32)
	cancelWatch, _ := reg.Subscribe(ctx, registry.SubscriptionFilter{
		GlobalIDs: deviceIDs[:4],
	}, watchCh)
	defer cancelWatch()
	ok("RetailChain subscribed to 4 devices")

	// ── 3. VendoCorp proposes sale of 4 devices to RetailChain ──────────────
	header("Phase 3 · Sale: VendoCorp → RetailChain")
	step(3, "VendoCorp proposes transfer of devices 1–4 (batch sale, PO-2026-001)")

	proposals := make([]*transfer.Proposal, 4)
	for i := 0; i < 4; i++ {
		p, err := neg.Propose(ctx, transfer.Proposal{
			GlobalID: deviceIDs[i],
			From: identity.LocalRef{
				InstanceURL: vendoCorpURL, EntityType: "devices", LocalID: 1000 + i,
			},
			To: identity.LocalRef{
				InstanceURL: retailChainURL, EntityType: "devices", LocalID: 2000 + i,
			},
			Protocol: "PO-2026-001",
			HistoryOffer: transfer.HistoryOffer{
				Mode: "full",
				Note: "Full manufacturing and QA history included",
			},
		})
		if err != nil {
			fatal("propose", err)
		}
		proposals[i] = p
	}
	ok("4 proposals created  (state: PROPOSED)")
	detail("protocol", "PO-2026-001")
	detail("history offer", "full manufacturing + QA history")

	// ── 4. RetailChain accepts devices 1–3, rejects device 4 ─────────────────
	header("Phase 4 · RetailChain acceptance")
	step(4, "RetailChain accepts devices 1–3; rejects device 4 (failed inspection)")

	for i := 0; i < 3; i++ {
		accepted, err := neg.Accept(ctx, proposals[i].ID, transfer.HistorySpec{Mode: "full"})
		if err != nil {
			fatal("accept", err)
		}
		ok(fmt.Sprintf("device %d accepted  (state: %s)", i+1, accepted.State))
		// Confirm settlement.
		_, _ = neg.Complete(ctx, accepted.ID)
	}

	rejected, err := neg.Reject(ctx, proposals[3].ID, "failed pre-delivery inspection: sensor calibration out of range")
	if err != nil {
		fatal("reject", err)
	}
	warn(fmt.Sprintf("device 4 REJECTED  (state: %s)", rejected.State))
	detail("reason", rejected.RejectionReason)
	time.Sleep(80 * time.Millisecond)

	// Verify registry state for device 1.
	rec1, _ := reg.Get(ctx, deviceIDs[0])
	detail("device 1 current owner", prettyRef(rec1.Current))
	detail("device 1 transfer count", fmt.Sprintf("%d", len(rec1.History)))

	// ── 5. VendoCorp cancels pending proposal for device 5 ───────────────────
	header("Phase 5 · Cancellation")
	step(5, "VendoCorp proposes then cancels transfer of device 5 (quality hold)")

	p5, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID: deviceIDs[4],
		From:     identity.LocalRef{InstanceURL: vendoCorpURL, EntityType: "devices", LocalID: 1004},
		To:       identity.LocalRef{InstanceURL: retailChainURL, EntityType: "devices", LocalID: 2004},
		Protocol: "PO-2026-001",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	cancelled, err := neg.Cancel(ctx, p5.ID)
	if err != nil {
		fatal("cancel", err)
	}
	warn(fmt.Sprintf("device 5 proposal CANCELLED  (state: %s)", cancelled.State))
	detail("reason", "quality hold — sensor batch recall")

	// ── 6. Device 2 goes for repair: RetailChain → ServiceCo → RetailChain ──
	header("Phase 6 · Repair cycle: RetailChain ↔ ServiceCo")
	step(6, "Device 2 sent to ServiceCo for repair")

	// RetailChain → ServiceCo
	pRepair, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID: deviceIDs[1],
		From:     identity.LocalRef{InstanceURL: retailChainURL, EntityType: "devices", LocalID: 2001},
		To:       identity.LocalRef{InstanceURL: serviceCoURL, EntityType: "devices", LocalID: 3001},
		Protocol: "WO-2026-0042",
		HistoryOffer: transfer.HistoryOffer{
			Mode: "from",
			From: time.Now().Add(-30 * 24 * time.Hour), // last 30 days
			Note: "Operational history only — manufacturing data stays with RetailChain",
		},
	})
	if err != nil {
		fatal("repair propose", err)
	}

	aRepair, err := neg.Accept(ctx, pRepair.ID, transfer.HistorySpec{
		Mode: "from",
		From: time.Now().Add(-30 * 24 * time.Hour),
	})
	if err != nil {
		fatal("repair accept", err)
	}
	_, _ = neg.Complete(ctx, aRepair.ID)
	ok(fmt.Sprintf("device 2 → ServiceCo  (state: %s)", aRepair.State))
	detail("work order", "WO-2026-0042")
	detail("history", "last 30 days operational")

	recRepair, _ := reg.Get(ctx, deviceIDs[1])
	detail("current owner", prettyRef(recRepair.Current))
	detail("transfer count", fmt.Sprintf("%d", len(recRepair.History)))

	// ServiceCo → RetailChain (repair complete)
	step(6, "Repair complete — device 2 returned to RetailChain")
	pReturn, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID: deviceIDs[1],
		From:     identity.LocalRef{InstanceURL: serviceCoURL, EntityType: "devices", LocalID: 3001},
		To:       identity.LocalRef{InstanceURL: retailChainURL, EntityType: "devices", LocalID: 2001},
		Protocol: "WO-2026-0042-RTN",
		HistoryOffer: transfer.HistoryOffer{Mode: "full", Note: "Full repair record included"},
	})
	if err != nil {
		fatal("return propose", err)
	}
	aReturn, err := neg.Accept(ctx, pReturn.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		fatal("return accept", err)
	}
	_, _ = neg.Complete(ctx, aReturn.ID)
	ok(fmt.Sprintf("device 2 returned to RetailChain  (state: %s)", aReturn.State))

	recBack, _ := reg.Get(ctx, deviceIDs[1])
	detail("current owner", prettyRef(recBack.Current))
	detail("total transfers", fmt.Sprintf("%d", len(recBack.History)))

	// ── 7. End-of-life: retire device 3 ─────────────────────────────────────
	header("Phase 7 · End-of-life retirement")
	step(7, "Device 3 decommissioned (exceeded service life)")

	err = reg.Retire(ctx, deviceIDs[2], "exceeded 7-year service life")
	if err != nil {
		fatal("retire", err)
	}
	ok(fmt.Sprintf("device 3 RETIRED  %s%s%s", colGray, shortID(string(deviceIDs[2])), colReset))

	_, err = reg.Resolve(ctx, deviceIDs[2])
	if err == registry.ErrRetired {
		ok("Resolve correctly returns ErrRetired — GlobalID preserved, no new owner possible")
	}

	// ── 8. Final registry state ───────────────────────────────────────────────
	header("Phase 8 · Final state")
	step(8, "Registry snapshot")
	time.Sleep(80 * time.Millisecond) // flush observer

	states := []struct {
		label string
		id    identity.GlobalID
	}{
		{"device 1", deviceIDs[0]},
		{"device 2", deviceIDs[1]},
		{"device 3", deviceIDs[2]},
		{"device 4", deviceIDs[3]},
		{"device 5", deviceIDs[4]},
	}

	for _, s := range states {
		rec, err := reg.Get(ctx, s.id)
		if err != nil {
			detail(s.label, fmt.Sprintf("ERROR: %v", err))
			continue
		}
		status := string(rec.Status)
		owner := prettyRef(rec.Current)
		xfers := len(rec.History)
		fmt.Printf("    %s%-10s%s  status=%-8s  owner=%-28s  transfers=%d\n",
			colBold, s.label, colReset, status, owner, xfers)
	}

	// ── 9. Subscription drain ─────────────────────────────────────────────────
	header("Phase 9 · RetailChain subscription drain")
	step(9, "Events RetailChain received for its 4 watched devices")
	close(watchCh)
	count := 0
	for range watchCh {
		count++
	}
	// Count may be 0 since watchCh is non-blocking and events may have been dropped.
	// In a real system the router delivers reliably; here it's best-effort.
	ok(fmt.Sprintf("RetailChain watch channel received %d events", count))

	// ── Summary ───────────────────────────────────────────────────────────────
	header("Demo complete")
	fmt.Printf("  Demonstrated:\n")
	for _, line := range []string{
		"Asset registration (5 devices minted with stable GlobalIDs)",
		"Batch transfer proposal with protocol reference (PO-2026-001)",
		"Acceptance with negotiated history portability",
		"Rejection with reason (failed inspection)",
		"Cancellation (quality hold)",
		"Repair sub-transfer cycle (partial history negotiation)",
		"Return transfer (full history)",
		"End-of-life retirement (GlobalID preserved, ErrRetired on resolve)",
		"In-process subscription (RetailChain watches 4 specific entities)",
		"Bus publication (all events → NATS JetStream when -bus=nats)",
	} {
		ok(line)
	}
	fmt.Println()
}

// ── NATS event summary (called from observer consumer) ───────────────────────

func printEnvelope(env events.Envelope) {
	var rec map[string]interface{}
	_ = json.Unmarshal(env.Payload, &rec)
	fmt.Printf("    %s▶ NATS%s  %-14s  %s%s%s\n",
		colYellow, colReset,
		env.Kind,
		colGray, shortID(string(env.GlobalID)), colReset)
}

func fatal(op string, err error) {
	fmt.Printf("%sFATAL%s %s: %v\n", colRed, colReset, op, err)
	os.Exit(1)
}
