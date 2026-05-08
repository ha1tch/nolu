// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package transfer

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
)

// MemoryNegotiator is an in-process, non-durable implementation of Negotiator.
// On Accept, it drives the registry Transfer so that the registry record and
// the proposal state stay consistent.
type MemoryNegotiator struct {
	mu        sync.Mutex
	proposals map[string]*Proposal
	reg       registry.Registry
}

// NewMemoryNegotiator creates a MemoryNegotiator backed by the given registry.
func NewMemoryNegotiator(reg registry.Registry) *MemoryNegotiator {
	return &MemoryNegotiator{
		proposals: make(map[string]*Proposal),
		reg:       reg,
	}
}

func (n *MemoryNegotiator) Propose(ctx context.Context, p Proposal) (*Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	if p.ID == "" {
		p.ID = uuid.New().String()
	}
	p.State = StateProposed
	p.ProposedAt = time.Now().UTC()
	p.UpdatedAt = p.ProposedAt

	clone := p
	n.proposals[p.ID] = &clone
	return &clone, nil
}

func (n *MemoryNegotiator) Accept(ctx context.Context, proposalID string, spec HistorySpec) (*Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	p, err := n.getProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if p.State != StateProposed {
		return nil, fmt.Errorf("%w: proposal %s is %s", ErrWrongState, proposalID, p.State)
	}

	// Drive the registry transfer atomically with the state change.
	_, err = n.reg.Transfer(ctx, registry.TransferRequest{
		GlobalID:    p.GlobalID,
		From:        p.From,
		To:          p.To,
		Protocol:    p.Protocol,
		HistoryFrom: spec.From.Format(time.RFC3339),
	})
	if err != nil {
		return nil, fmt.Errorf("transfer: registry refused: %w", err)
	}

	p.State = StateAccepted
	p.HistorySpec = &spec
	p.UpdatedAt = time.Now().UTC()
	clone := *p
	return &clone, nil
}

func (n *MemoryNegotiator) Reject(ctx context.Context, proposalID string, reason string) (*Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	p, err := n.getProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if p.State != StateProposed {
		return nil, fmt.Errorf("%w: proposal %s is %s", ErrWrongState, proposalID, p.State)
	}
	p.State = StateRejected
	p.RejectionReason = reason
	p.UpdatedAt = time.Now().UTC()
	clone := *p
	return &clone, nil
}

func (n *MemoryNegotiator) Cancel(ctx context.Context, proposalID string) (*Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	p, err := n.getProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if p.State != StateProposed {
		return nil, fmt.Errorf("%w: proposal %s is %s", ErrWrongState, proposalID, p.State)
	}
	p.State = StateCancelled
	p.UpdatedAt = time.Now().UTC()
	clone := *p
	return &clone, nil
}

func (n *MemoryNegotiator) Complete(ctx context.Context, proposalID string) (*Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()

	p, err := n.getProposal(proposalID)
	if err != nil {
		return nil, err
	}
	if p.State != StateAccepted {
		return nil, fmt.Errorf("%w: proposal %s is %s, expected accepted", ErrWrongState, proposalID, p.State)
	}
	p.State = StateCompleted
	p.UpdatedAt = time.Now().UTC()
	clone := *p
	return &clone, nil
}

func (n *MemoryNegotiator) Get(ctx context.Context, proposalID string) (*Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	p, err := n.getProposal(proposalID)
	if err != nil {
		return nil, err
	}
	clone := *p
	return &clone, nil
}

func (n *MemoryNegotiator) ListByGlobalID(ctx context.Context, id identity.GlobalID) ([]Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []Proposal
	for _, p := range n.proposals {
		if p.GlobalID == id {
			out = append(out, *p)
		}
	}
	return out, nil
}

func (n *MemoryNegotiator) ListByInstance(ctx context.Context, instanceURL string, state *State) ([]Proposal, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	var out []Proposal
	for _, p := range n.proposals {
		if p.From.InstanceURL != instanceURL && p.To.InstanceURL != instanceURL {
			continue
		}
		if state != nil && p.State != *state {
			continue
		}
		out = append(out, *p)
	}
	return out, nil
}

// getProposal retrieves a proposal by ID without locking (caller must hold lock).
func (n *MemoryNegotiator) getProposal(id string) (*Proposal, error) {
	p, ok := n.proposals[id]
	if !ok {
		return nil, fmt.Errorf("%w: %s", ErrNotFound, id)
	}
	return p, nil
}

// Compile-time assertion.
var _ Negotiator = (*MemoryNegotiator)(nil)
