// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package transfer_test

import (
	"context"
	"testing"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/transfer"
)

func setup() (context.Context, *registry.MemoryRegistry, *transfer.MemoryNegotiator) {
	ctx := context.Background()
	reg := registry.NewMemoryRegistry("registry.test.local", events.NewMemoryBus())
	neg := transfer.NewMemoryNegotiator(reg)
	return ctx, reg, neg
}

func localRef(instance, entity string, id int) identity.LocalRef {
	return identity.LocalRef{InstanceURL: instance, EntityType: entity, LocalID: id}
}

func registerDevice(ctx context.Context, t *testing.T, reg *registry.MemoryRegistry, instanceURL string, localID int) identity.GlobalID {
	t.Helper()
	rec, err := reg.Register(ctx, "registry.test.local", "devices",
		localRef(instanceURL, "devices", localID))
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	return rec.GlobalID
}

// ── Propose ───────────────────────────────────────────────────────────────────

func TestPropose_CreatesProposal(t *testing.T) {
	ctx, reg, neg := setup()

	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)
	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)

	p, err := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		Protocol:     "PO-001",
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	if err != nil {
		t.Fatalf("Propose: %v", err)
	}
	if p.State != transfer.StateProposed {
		t.Errorf("expected state proposed, got %s", p.State)
	}
	if p.ID == "" {
		t.Error("expected non-empty proposal ID")
	}
}

// ── Accept ────────────────────────────────────────────────────────────────────

func TestAccept_UpdatesRegistryAndProposal(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	p, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})

	accepted, err := neg.Accept(ctx, p.ID, transfer.HistorySpec{Mode: "full"})
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	if accepted.State != transfer.StateAccepted {
		t.Errorf("expected state accepted, got %s", accepted.State)
	}

	// Registry must reflect new owner.
	ref, err := reg.Resolve(ctx, gid)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if ref != to {
		t.Errorf("expected registry owner %+v, got %+v", to, ref)
	}
}

func TestAccept_WrongState(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	p, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	_, _ = neg.Accept(ctx, p.ID, transfer.HistorySpec{Mode: "full"})

	// Accept again should fail.
	_, err := neg.Accept(ctx, p.ID, transfer.HistorySpec{Mode: "full"})
	if err == nil {
		t.Fatal("expected error on double accept, got nil")
	}
}

// ── Reject ────────────────────────────────────────────────────────────────────

func TestReject_LeavesRegistryUnchanged(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	p, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})

	rejected, err := neg.Reject(ctx, p.ID, "failed inspection")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.State != transfer.StateRejected {
		t.Errorf("expected state rejected, got %s", rejected.State)
	}
	if rejected.RejectionReason != "failed inspection" {
		t.Errorf("expected rejection reason, got %q", rejected.RejectionReason)
	}

	// Registry must still point to original owner.
	ref, _ := reg.Resolve(ctx, gid)
	if ref != from {
		t.Errorf("registry changed after rejection: expected %+v, got %+v", from, ref)
	}
}

// ── Cancel ────────────────────────────────────────────────────────────────────

func TestCancel_LeavesRegistryUnchanged(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	p, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})

	cancelled, err := neg.Cancel(ctx, p.ID)
	if err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	if cancelled.State != transfer.StateCancelled {
		t.Errorf("expected state cancelled, got %s", cancelled.State)
	}

	ref, _ := reg.Resolve(ctx, gid)
	if ref != from {
		t.Errorf("registry changed after cancellation: expected %+v, got %+v", from, ref)
	}
}

// ── Complete ──────────────────────────────────────────────────────────────────

func TestComplete_RequiresAccepted(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	p, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})

	// Complete on a PROPOSED proposal must fail.
	if _, err := neg.Complete(ctx, p.ID); err == nil {
		t.Fatal("expected error completing a proposed (not accepted) proposal")
	}
}

func TestComplete_Success(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	p, _ := neg.Propose(ctx, transfer.Proposal{
		GlobalID:     gid,
		From:         from,
		To:           to,
		HistoryOffer: transfer.HistoryOffer{Mode: "full"},
	})
	accepted, _ := neg.Accept(ctx, p.ID, transfer.HistorySpec{Mode: "full"})
	completed, err := neg.Complete(ctx, accepted.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if completed.State != transfer.StateCompleted {
		t.Errorf("expected state completed, got %s", completed.State)
	}
}

// ── List ──────────────────────────────────────────────────────────────────────

func TestListByGlobalID(t *testing.T) {
	ctx, reg, neg := setup()

	from := localRef("http://xolu-a", "devices", 1)
	to := localRef("http://xolu-b", "devices", 100)
	gid := registerDevice(ctx, t, reg, "http://xolu-a", 1)

	neg.Propose(ctx, transfer.Proposal{GlobalID: gid, From: from, To: to, HistoryOffer: transfer.HistoryOffer{Mode: "full"}})
	neg.Propose(ctx, transfer.Proposal{GlobalID: gid, From: from, To: to, HistoryOffer: transfer.HistoryOffer{Mode: "none"}})

	// Register and propose for a different device — must not appear.
	gid2 := registerDevice(ctx, t, reg, "http://xolu-a", 2)
	neg.Propose(ctx, transfer.Proposal{GlobalID: gid2, From: from, To: to, HistoryOffer: transfer.HistoryOffer{Mode: "full"}})

	proposals, err := neg.ListByGlobalID(ctx, gid)
	if err != nil {
		t.Fatalf("ListByGlobalID: %v", err)
	}
	if len(proposals) != 2 {
		t.Errorf("expected 2 proposals for gid, got %d", len(proposals))
	}
}
