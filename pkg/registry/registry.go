// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package registry implements nolu's federated entity registry.
//
// The registry is the single service that knows where every global entity
// currently lives. It does not own the entity data — that stays in xolu —
// but it owns the mapping from GlobalID to LocalRef and the complete
// ownership history.
//
// Design invariants:
//
//  1. A GlobalID, once minted, is never reused or deleted. Entities that are
//     retired receive a "retired" status but their records remain.
//
//  2. At any point in time, exactly one LocalRef is the current owner of a
//     GlobalID. There is no ambiguity about where an entity lives.
//
//  3. Every ownership transition is recorded atomically with the new LocalRef.
//     The registry is the clearinghouse: both parties trust it.
//
//  4. The registry does not communicate with xolu instances. It is told about
//     transfers by the application layer; it does not initiate them. This keeps
//     the registry stateless with respect to xolu availability.
//
// Persistence:
//
// The Registry interface is intentionally backend-agnostic. The canonical
// implementation uses xolu itself as its storage backend — the registry's
// OwnershipRecords are entities in a dedicated xolu instance, giving nolu's
// own data the same graph, OQL, and timeseries capabilities as any other
// xolu deployment.
//
// A future implementation may use aulsql as the substrate when that project
// matures sufficiently.
package registry

import (
	"context"
	"errors"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
)

var (
	// ErrNotFound is returned when a GlobalID has no record in the registry.
	ErrNotFound = errors.New("registry: entity not found")

	// ErrAlreadyExists is returned when trying to register a GlobalID that
	// already exists.
	ErrAlreadyExists = errors.New("registry: entity already registered")

	// ErrRetired is returned when an operation is attempted on a retired entity.
	ErrRetired = errors.New("registry: entity has been retired")

	// ErrInvalidTransfer is returned when a transfer request is malformed —
	// for example, when the From ref does not match the current owner.
	ErrInvalidTransfer = errors.New("registry: invalid transfer request")
)

// Status represents the lifecycle state of a registered entity.
type Status string

const (
	// StatusActive means the entity exists and has a current owner.
	StatusActive Status = "active"

	// StatusRetired means the entity has been permanently decommissioned.
	// No further transfers are permitted.
	StatusRetired Status = "retired"
)

// Record is the registry's full record for a global entity.
type Record struct {
	identity.OwnershipRecord
	Status    Status    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TransferRequest is the input to Registry.Transfer.
type TransferRequest struct {
	GlobalID    identity.GlobalID `json:"global_id"`
	// From must match the current owner's LocalRef. This acts as an
	// optimistic concurrency guard: if the registry's current record does
	// not match From, the transfer is rejected.
	From        identity.LocalRef  `json:"from"`
	To          identity.LocalRef  `json:"to"`
	Protocol    string             `json:"protocol,omitempty"`
	HistoryFrom string             `json:"history_from,omitempty"`
}

// SubscriptionFilter controls which GlobalIDs a subscriber receives events for.
type SubscriptionFilter struct {
	// EntityTypes, if non-empty, restricts events to these entity types.
	EntityTypes []string `json:"entity_types,omitempty"`
	// InstanceURLs, if non-empty, restricts events to entities currently
	// owned by these xolu instances.
	InstanceURLs []string `json:"instance_urls,omitempty"`
	// GlobalIDs, if non-empty, restricts to exactly these entities.
	GlobalIDs []identity.GlobalID `json:"global_ids,omitempty"`
}

// Event is emitted by the registry when an ownership record changes.
type Event struct {
	Kind      EventKind         `json:"kind"`
	GlobalID  identity.GlobalID `json:"global_id"`
	Record    *Record           `json:"record,omitempty"`
	At        time.Time         `json:"at"`
}

// EventKind classifies registry events.
type EventKind string

const (
	// EventRegistered fires when a new GlobalID is minted and registered.
	EventRegistered EventKind = "registered"
	// EventTransferred fires when ownership changes.
	EventTransferred EventKind = "transferred"
	// EventRetired fires when an entity is retired.
	EventRetired EventKind = "retired"
)

// Registry is the core interface for nolu's entity registry.
//
// All methods are safe for concurrent use. Implementations must be durable:
// a committed Register or Transfer must survive a process restart.
type Registry interface {
	// Register mints a new GlobalID for the given entity type and records
	// its initial owner. Returns ErrAlreadyExists if the GlobalID is already
	// registered (which should not happen with UUID-based minting, but is
	// checked for safety).
	//
	// The registryHost is used to construct the GlobalID. In a single-node
	// deployment this is the nolu instance's own hostname.
	Register(ctx context.Context, registryHost, entityType string, owner identity.LocalRef) (*Record, error)

	// Get returns the current record for a GlobalID.
	// Returns ErrNotFound if not registered.
	Get(ctx context.Context, id identity.GlobalID) (*Record, error)

	// Resolve returns the current LocalRef for a GlobalID — the short path
	// for callers that only need to know where to find the entity right now.
	// Returns ErrNotFound or ErrRetired as appropriate.
	Resolve(ctx context.Context, id identity.GlobalID) (identity.LocalRef, error)

	// Transfer atomically moves ownership of a GlobalID from one LocalRef
	// to another. Returns ErrInvalidTransfer if req.From does not match the
	// current owner. Returns ErrRetired if the entity has been retired.
	//
	// This is the clearinghouse operation: once Transfer returns nil, the
	// registry's record is the authoritative statement of current ownership.
	// Both the outgoing and incoming xolu instances can be updated
	// asynchronously; the registry record is the ground truth.
	Transfer(ctx context.Context, req TransferRequest) (*Record, error)

	// Retire permanently decommissions a GlobalID. No further transfers are
	// permitted. The entity's history remains in the registry.
	// Returns ErrNotFound or ErrRetired.
	Retire(ctx context.Context, id identity.GlobalID, reason string) error

	// Subscribe registers a channel to receive events matching filter.
	// The channel must be drained promptly; the registry will drop events
	// if the channel is full. The caller should close the returned cancel
	// function when done.
	//
	// The subscription is in-process only. For durable cross-process event
	// delivery, use the event bus (see package events).
	Subscribe(ctx context.Context, filter SubscriptionFilter, ch chan<- Event) (cancel func(), err error)

	// ListByInstance returns all active GlobalIDs currently owned by the
	// given xolu instance URL. Useful for instance health checks and
	// migration planning.
	ListByInstance(ctx context.Context, instanceURL string) ([]identity.GlobalID, error)

	// ListByEntityType returns all active GlobalIDs of the given entity type
	// across all instances.
	ListByEntityType(ctx context.Context, entityType string) ([]identity.GlobalID, error)

	// ListByInstanceAndTenant returns all active GlobalIDs currently owned by
	// the given xolu instance AND scoped to the given tenant ID.
	// TenantID 0 returns all unscoped (single-tenant) entities on the instance.
	// Used by the hotswap manager at cutover to transfer only the entities
	// belonging to the migrating tenant, not all entities on the instance.
	ListByInstanceAndTenant(ctx context.Context, instanceURL string, tenantID uint16) ([]identity.GlobalID, error)
}
