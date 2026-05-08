// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package hotswap implements the nolu tenant hotswap protocol.
//
// A hotswap moves all entities belonging to a tenant from one xolu instance
// to another with the shortest possible write-outage window. nolu orchestrates
// the state machine; iolu performs the data migration; xolu provides the
// quiesce endpoint.
//
// State machine:
//
//	REQUESTED → PREPARING → QUIESCING → MIGRATING → VALIDATING → CUTTING_OVER → COMPLETE
//	                     ↘           ↘           ↘            ↘
//	                      ROLLING_BACK ──────────────────────────────────────────────────
//
// REQUESTED:    Operator initiates. nolu validates participants are reachable.
// PREPARING:    Bulk data sync in progress (live, best-effort). Source still
//               serving reads and writes. nolu tracks estimated lag.
// QUIESCING:    nolu signals source to stop accepting writes. Drains in-flight
//               requests. Advances automatically when quiesce is confirmed.
// MIGRATING:    Delta sync: only rows modified since bulk sync. Sequences last.
//               Graph edges. Optionally timeseries.
// VALIDATING:   iolu validate confirms row counts, sequences, graph edges match.
// CUTTING_OVER: nolu atomically updates all GlobalIDs for this tenant to point
//               at the target. Publishes hotswap event to NATS. Target goes live.
// COMPLETE:     Hotswap finished. Source enters redirect-quiesce (307 → target).
// ROLLING_BACK: Failure at any stage. Source quiesce lifted. State returns to
//               pre-hotswap. All GlobalIDs still point at source.
package hotswap

import (
	"context"
	"errors"
	"time"
)

// TenantInvalidator is satisfied by registry.TenantDirectory.
// Using an interface avoids a circular import between hotswap and registry.
type TenantInvalidator interface {
	InvalidateTenant(tenantName string)
}

// State is the lifecycle state of a hotswap operation.
type State string

const (
	StateRequested   State = "requested"
	StatePreparing   State = "preparing"
	StateQuiescing   State = "quiescing"
	StateMigrating   State = "migrating"
	StateValidating  State = "validating"
	StateCuttingOver State = "cutting_over"
	StateComplete    State = "complete"
	StateRollingBack State = "rolling_back"
	StateFailed      State = "failed" // terminal failure after rollback
)

var (
	ErrNotFound         = errors.New("hotswap: not found")
	ErrWrongState       = errors.New("hotswap: wrong state for this operation")
	ErrAlreadyExists    = errors.New("hotswap: a hotswap is already in progress for this tenant")
	ErrValidationFailed = errors.New("hotswap: validation failed — source and target are not consistent")
	ErrQuiesceTimeout   = errors.New("hotswap: source did not confirm quiesce within timeout")
	ErrSourceUnreachable = errors.New("hotswap: source xolu instance is not reachable")
	ErrTargetUnreachable = errors.New("hotswap: target xolu instance is not reachable")
)

// InstanceRef identifies a xolu instance and tenant within it.
// It is the hotswap-level equivalent of identity.LocalRef but operates
// at tenant granularity rather than entity granularity.
type InstanceRef struct {
	// InstanceURL is the xolu base URL.
	InstanceURL string `json:"instance_url"`
	// TenantName is the human-readable tenant name on this instance.
	TenantName string `json:"tenant_name"`
	// TenantID is the numeric tenant ID on this instance.
	TenantID uint16 `json:"tenant_id"`
}

// TimestampedState records a state transition with its time.
type TimestampedState struct {
	State State     `json:"state"`
	At    time.Time `json:"at"`
	Note  string    `json:"note,omitempty"`
}

// ValidationResult records the output of the VALIDATING phase.
type ValidationResult struct {
	EntityCounts   map[string]int `json:"entity_counts"`     // per entity_type: source count
	EntityMismatch map[string]int `json:"entity_mismatch"`   // per entity_type: delta (0 = ok)
	SequenceOK     bool           `json:"sequence_ok"`
	GraphEdgesOK   bool           `json:"graph_edges_ok"`
	DeepCompared   bool           `json:"deep_compared"`
	Valid          bool           `json:"valid"`
	Notes          []string       `json:"notes,omitempty"`
}

// HotswapOptions controls optional behaviour of a hotswap operation.
type HotswapOptions struct {
	// AutoAdvance: if true nolu will advance from PREPARING → QUIESCING
	// automatically when lag falls below LagThreshold. If false the operator
	// must call Confirm to advance.
	AutoAdvance bool `json:"auto_advance"`

	// LagThreshold is the maximum number of entity rows that may differ
	// between source and target before AutoAdvance triggers.
	// Default: 0 (advance immediately after bulk sync).
	LagThreshold int `json:"lag_threshold"`

	// QuiesceTimeout is how long nolu waits for the source to confirm
	// quiesce before aborting. Default: 30s.
	QuiesceTimeout time.Duration `json:"quiesce_timeout"`

	// IncludeTimeseries: if true the MIGRATING phase includes timeseries
	// file migration. Requires shared storage between source and target.
	// Default: false.
	IncludeTimeseries bool `json:"include_timeseries"`

	// TimestampedHistory: if true all state transitions are stored in
	// the history slice for audit purposes. Default: true.
	TimestampedHistory bool `json:"timestamped_history"`

	// SourceDBPath is the filesystem path to the source xolu SQLite database.
	// Required for iolu-based migration and validation phases.
	// Example: "/data/hub-a.db"
	// If empty, migration and validation phases are simulated (no-op).
	SourceDBPath string `json:"source_db_path,omitempty"`

	// TargetDBPath is the filesystem path to the target xolu SQLite database.
	// Required for iolu-based migration and validation phases.
	// Example: "/data/hub-b.db"
	TargetDBPath string `json:"target_db_path,omitempty"`

	// ArchivePath is the directory used for intermediate export archives
	// during the MIGRATING phase. Must be accessible from both source and
	// target hosts (shared mount, NFS, etc.).
	// Default: OS temp directory.
	ArchivePath string `json:"archive_path,omitempty"`

	// IoluBinary is the path to the iolu binary.
	// Default: "iolu" (resolved via PATH).
	IoluBinary string `json:"iolu_binary,omitempty"`
}

// Hotswap is the record of a single hotswap operation.
type Hotswap struct {
	ID      string      `json:"id"`
	Source  InstanceRef `json:"source"`
	Target  InstanceRef `json:"target"`
	State   State       `json:"state"`
	Options HotswapOptions `json:"options"`

	// EntityCount is the number of GlobalIDs that will be updated at cutover.
	EntityCount int `json:"entity_count"`

	// Lag is the estimated number of entity rows that differ between
	// source and target at the time of last lag check.
	Lag int `json:"lag"`

	// Validation holds the result of the VALIDATING phase, if reached.
	Validation *ValidationResult `json:"validation,omitempty"`

	// FailureReason holds the error message if State is RollingBack or Failed.
	FailureReason string `json:"failure_reason,omitempty"`

	// History records all state transitions in order.
	History []TimestampedState `json:"history"`

	RequestedAt  time.Time  `json:"requested_at"`
	CompletedAt  *time.Time `json:"completed_at,omitempty"`
}

// Manager orchestrates hotswap operations.
// All methods are safe for concurrent use.
type Manager interface {
	// Request initiates a new hotswap from source to target.
	// Returns ErrAlreadyExists if a hotswap is already in progress for
	// the source tenant on this nolu instance.
	Request(ctx context.Context, source, target InstanceRef, opts HotswapOptions) (*Hotswap, error)

	// Get returns the current hotswap record by ID.
	Get(ctx context.Context, id string) (*Hotswap, error)

	// List returns all hotswap records, optionally filtered by state.
	List(ctx context.Context, state *State) ([]*Hotswap, error)

	// Confirm advances a hotswap from PREPARING to QUIESCING.
	// Only valid when State == StatePreparing and AutoAdvance is false.
	// Returns ErrWrongState if the hotswap is not in PREPARING state.
	Confirm(ctx context.Context, id string) (*Hotswap, error)

	// Abort cancels a hotswap and initiates rollback.
	// Valid from any state except COMPLETE and FAILED.
	// Returns ErrWrongState if the hotswap is already terminal.
	Abort(ctx context.Context, id string, reason string) (*Hotswap, error)

	// Status returns a poll-friendly summary of a hotswap.
	// Equivalent to Get but may include additional computed fields
	// (e.g. estimated time remaining, real-time lag).
	Status(ctx context.Context, id string) (*HotswapStatus, error)
}

// HotswapStatus is the poll-friendly view of a hotswap, with additional
// computed fields not stored in the Hotswap record itself.
type HotswapStatus struct {
	Hotswap

	// CurrentLag is the most recently measured row delta (recomputed on poll).
	CurrentLag int `json:"current_lag"`

	// PhaseElapsed is how long the hotswap has been in the current state.
	PhaseElapsed time.Duration `json:"phase_elapsed"`

	// EstimatedRemaining is a rough estimate of time to completion.
	// Zero if unknown (e.g. before MIGRATING phase begins).
	EstimatedRemaining time.Duration `json:"estimated_remaining"`
}
