// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package transfer implements nolu's asset transfer protocol.
//
// An asset transfer is the sequence of operations that moves ownership of
// a GlobalID from one xolu instance to another. It is a first-class event
// in nolu — not just a field update in the registry — because it has
// real-world consequences: data portability, subscription rerouting,
// compliance records, and bilateral acknowledgement.
//
// Transfer lifecycle:
//
//  1. PROPOSED — the outgoing owner initiates a transfer proposal.
//     The registry records the proposal; the incoming owner is notified
//     via the event bus.
//
//  2. ACCEPTED — the incoming owner accepts the transfer proposal.
//     At this point the registry atomically updates the ownership record.
//     The outgoing owner is notified.
//
//  3. COMPLETED — the outgoing owner confirms that it has completed any
//     local obligations (e.g. flushing the event stream, updating its
//     own records). The transfer is now fully settled.
//
//  4. REJECTED — the incoming owner declines the proposal. The registry
//     record is unchanged.
//
//  5. CANCELLED — the outgoing owner withdraws a PROPOSED transfer before
//     it is accepted.
//
// The history portability negotiation happens between PROPOSED and ACCEPTED.
// The proposal carries a HistoryOffer; the acceptance carries a HistorySpec
// that the outgoing owner must honour when it flushes its event stream.
//
// This protocol is intentionally bilateral and explicit. There is no
// "push" transfer where an owner can force an entity onto another instance
// without acceptance.
package transfer

import (
	"context"
	"errors"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
)

var (
	ErrNotFound         = errors.New("transfer: proposal not found")
	ErrWrongState       = errors.New("transfer: proposal is not in the expected state")
	ErrNotAuthorised    = errors.New("transfer: caller is not authorised for this operation")
	ErrAlreadySettled   = errors.New("transfer: transfer is already settled")
)

// State is the lifecycle state of a transfer proposal.
type State string

const (
	StateProposed   State = "proposed"
	StateAccepted   State = "accepted"
	StateCompleted  State = "completed"
	StateRejected   State = "rejected"
	StateCancelled  State = "cancelled"
)

// HistoryOffer describes what history the outgoing owner is willing to provide.
type HistoryOffer struct {
	// Mode is "none", "full", or "from".
	Mode    string    `json:"mode"`
	// From is the earliest event timestamp available, when Mode is "from".
	From    time.Time `json:"from,omitempty"`
	// Note is a human-readable explanation of any constraints.
	Note    string    `json:"note,omitempty"`
}

// HistorySpec is the incoming owner's selection from the HistoryOffer.
type HistorySpec struct {
	// Mode is "none", "full", or "from". Must be compatible with the offer.
	Mode    string    `json:"mode"`
	// From is the requested start timestamp, when Mode is "from".
	From    time.Time `json:"from,omitempty"`
}

// Proposal is the core record of a transfer negotiation.
type Proposal struct {
	ID           string            `json:"id"`
	GlobalID     identity.GlobalID `json:"global_id"`
	From         identity.LocalRef `json:"from"`
	To           identity.LocalRef `json:"to"`
	State        State             `json:"state"`
	// Protocol is an application-layer transaction identifier.
	// Examples: purchase order number, contract ID, maintenance work order.
	Protocol     string            `json:"protocol,omitempty"`
	HistoryOffer HistoryOffer      `json:"history_offer"`
	// HistorySpec is set when the incoming owner accepts and specifies
	// how much history it wants.
	HistorySpec  *HistorySpec      `json:"history_spec,omitempty"`
	// RejectionReason is set when the incoming owner rejects.
	RejectionReason string         `json:"rejection_reason,omitempty"`
	ProposedAt   time.Time         `json:"proposed_at"`
	UpdatedAt    time.Time         `json:"updated_at"`
}

// Negotiator manages the transfer proposal lifecycle.
type Negotiator interface {
	// Propose creates a new transfer proposal. The outgoing owner (From)
	// initiates this. Returns the created Proposal.
	Propose(ctx context.Context, p Proposal) (*Proposal, error)

	// Accept moves a proposal from PROPOSED to ACCEPTED.
	// Must be called by the incoming owner (To).
	// spec describes how much history the incoming owner wants.
	Accept(ctx context.Context, proposalID string, spec HistorySpec) (*Proposal, error)

	// Reject moves a proposal from PROPOSED to REJECTED.
	// Must be called by the incoming owner (To).
	Reject(ctx context.Context, proposalID string, reason string) (*Proposal, error)

	// Cancel moves a proposal from PROPOSED to CANCELLED.
	// Must be called by the outgoing owner (From).
	Cancel(ctx context.Context, proposalID string) (*Proposal, error)

	// Complete moves a proposal from ACCEPTED to COMPLETED.
	// Must be called by the outgoing owner (From) after it has fulfilled
	// its history delivery obligations.
	Complete(ctx context.Context, proposalID string) (*Proposal, error)

	// Get returns a proposal by ID.
	Get(ctx context.Context, proposalID string) (*Proposal, error)

	// ListByGlobalID returns all proposals for a given GlobalID,
	// ordered by ProposedAt descending.
	ListByGlobalID(ctx context.Context, id identity.GlobalID) ([]Proposal, error)

	// ListByInstance returns all proposals involving a given xolu instance
	// (either as From or To), optionally filtered by state.
	ListByInstance(ctx context.Context, instanceURL string, state *State) ([]Proposal, error)
}
