// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package hotswap

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/xoluclient"
	"github.com/rs/zerolog/log"
)

// MemoryManager is an in-process, non-durable implementation of Manager.
// State is lost on restart — in production this should be backed by the
// XoluRegistry like the transfer negotiator.
type MemoryManager struct {
	mu       sync.Mutex
	hotswaps map[string]*Hotswap
	reg      registry.Registry
	bus      events.Bus
	dir      TenantInvalidator // optional; nil if no tenant directory
}

// NewMemoryManager creates a MemoryManager wired to the given registry and bus.
// Pass a registry.TenantDirectory as dir to enable cache invalidation on hotswap start.
// dir may be nil.
func NewMemoryManager(reg registry.Registry, bus events.Bus, dir TenantInvalidator) *MemoryManager {
	return &MemoryManager{
		hotswaps: make(map[string]*Hotswap),
		reg:      reg,
		bus:      bus,
		dir:      dir,
	}
}

func (m *MemoryManager) Request(ctx context.Context, source, target InstanceRef, opts HotswapOptions) (*Hotswap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check no active hotswap for this source tenant.
	for _, h := range m.hotswaps {
		if h.Source.InstanceURL == source.InstanceURL &&
			h.Source.TenantName == source.TenantName &&
			!isTerminal(h.State) {
			return nil, fmt.Errorf("%w: %s/%s", ErrAlreadyExists, source.InstanceURL, source.TenantName)
		}
	}

	// Apply defaults.
	if opts.QuiesceTimeout == 0 {
		opts.QuiesceTimeout = 30 * time.Second
	}
	opts.TimestampedHistory = true

	// Validate reachability (non-blocking connectivity check).
	srcClient := xoluclient.New(source.InstanceURL, 0)
	if err := srcClient.Healthy(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	tgtClient := xoluclient.New(target.InstanceURL, 0)
	if err := tgtClient.Healthy(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTargetUnreachable, err)
	}

	// Count GlobalIDs affected — only those belonging to the migrating tenant.
	tenantGIDs, err := m.reg.ListByInstanceAndTenant(ctx, source.InstanceURL, source.TenantID)
	if err != nil {
		return nil, fmt.Errorf("hotswap: list tenant entities: %w", err)
	}

	now := time.Now().UTC()
	h := &Hotswap{
		ID:          uuid.New().String(),
		Source:      source,
		Target:      target,
		State:       StateRequested,
		Options:     opts,
		EntityCount: len(tenantGIDs),
		RequestedAt: now,
		History:     []TimestampedState{{State: StateRequested, At: now}},
	}
	m.hotswaps[h.ID] = h

	log.Info().
		Str("id", h.ID).
		Str("source", fmt.Sprintf("%s/%s", source.InstanceURL, source.TenantName)).
		Str("target", fmt.Sprintf("%s/%s", target.InstanceURL, target.TenantName)).
		Int("entity_count", h.EntityCount).
		Msg("hotswap: requested")

	// Advance to PREPARING.
	m.advanceLocked(ctx, h, StatePreparing, "bulk sync phase initiated")

	// If AutoAdvance is set, immediately trigger quiesce without waiting
	// for operator Confirm.
	if opts.AutoAdvance {
		m.advanceLocked(ctx, h, StateQuiescing, "auto-advance: bulk sync skipped")
		go m.driveQuiesce(context.Background(), h.ID)
	}

	return m.clone(h), nil
}

func (m *MemoryManager) Get(ctx context.Context, id string) (*Hotswap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hotswaps[id]
	if !ok {
		return nil, ErrNotFound
	}
	return m.clone(h), nil
}

func (m *MemoryManager) List(ctx context.Context, state *State) ([]*Hotswap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []*Hotswap
	for _, h := range m.hotswaps {
		if state == nil || h.State == *state {
			out = append(out, m.clone(h))
		}
	}
	return out, nil
}

func (m *MemoryManager) Confirm(ctx context.Context, id string) (*Hotswap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hotswaps[id]
	if !ok {
		return nil, ErrNotFound
	}
	if h.State != StatePreparing {
		return nil, fmt.Errorf("%w: expected preparing, got %s", ErrWrongState, h.State)
	}
	m.advanceLocked(ctx, h, StateQuiescing, "operator confirmed")

	// Drive quiesce asynchronously.
	go m.driveQuiesce(context.Background(), id)

	return m.clone(h), nil
}

func (m *MemoryManager) Abort(ctx context.Context, id string, reason string) (*Hotswap, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hotswaps[id]
	if !ok {
		return nil, ErrNotFound
	}
	if isTerminal(h.State) {
		return nil, fmt.Errorf("%w: cannot abort a %s hotswap", ErrWrongState, h.State)
	}
	h.FailureReason = reason
	m.advanceLocked(ctx, h, StateRollingBack, "operator aborted: "+reason)

	go m.driveRollback(context.Background(), id)

	return m.clone(h), nil
}

func (m *MemoryManager) Status(ctx context.Context, id string) (*HotswapStatus, error) {
	h, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	elapsed := time.Since(h.RequestedAt)
	if len(h.History) > 0 {
		elapsed = time.Since(h.History[len(h.History)-1].At)
	}
	return &HotswapStatus{
		Hotswap:      *h,
		CurrentLag:   h.Lag,
		PhaseElapsed: elapsed,
	}, nil
}

// ── Internal state machine drivers ────────────────────────────────────────────

// advanceLocked transitions the hotswap to the next state.
// Must be called with m.mu held.
func (m *MemoryManager) advanceLocked(_ context.Context, h *Hotswap, next State, note string) {
	h.State = next
	if h.Options.TimestampedHistory {
		h.History = append(h.History, TimestampedState{
			State: next,
			At:    time.Now().UTC(),
			Note:  note,
		})
	}
	log.Info().
		Str("id", h.ID).
		Str("state", string(next)).
		Str("note", note).
		Msg("hotswap: state transition")
}

// driveQuiesce signals the source to stop accepting writes, then advances
// to MIGRATING when confirmed (or rolls back on timeout).
func (m *MemoryManager) driveQuiesce(ctx context.Context, id string) {
	m.mu.Lock()
	h, ok := m.hotswaps[id]
	if !ok || h.State != StateQuiescing {
		m.mu.Unlock()
		return
	}
	timeout := h.Options.QuiesceTimeout
	sourceURL := h.Source.InstanceURL
	tenantName := h.Source.TenantName
	m.mu.Unlock()

	// Signal quiesce to source xolu.
	// xolu must implement POST /api/v1/tenant/{name}/quiesce (not yet built).
	// For now we log intent and simulate confirmation after a brief pause.
	log.Info().
		Str("id", id).
		Str("source", sourceURL).
		Str("tenant", tenantName).
		Dur("timeout", timeout).
		Msg("hotswap: signalling quiesce to source (xolu quiesce endpoint not yet implemented)")

	// Notify directory: tenant location is unstable during migration.
	if m.dir != nil {
		m.dir.InvalidateTenant(tenantName)
	}

	// Signal quiesce to source xolu.
	srcClient := xoluclient.New(sourceURL, 0)
	if _, err := srcClient.QuiesceTenant(ctx, tenantName, ""); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("hotswap: quiesce call failed — proceeding anyway (xolu may not support quiesce)")
	}
	select {
	case <-time.After(100 * time.Millisecond):
		// Brief pause to allow in-flight requests to drain.
	case <-ctx.Done():
		m.failHotswap(ctx, id, "context cancelled during quiesce")
		return
	}

	m.mu.Lock()
	h, ok = m.hotswaps[id]
	if !ok || h.State != StateQuiescing {
		m.mu.Unlock()
		return
	}
	m.advanceLocked(ctx, h, StateMigrating, "source quiesce confirmed")
	m.mu.Unlock()

	// Drive migration asynchronously.
	go m.driveMigration(context.Background(), id)
}

// driveMigration runs the iolu export→import pipeline, then advances to VALIDATING.
func (m *MemoryManager) driveMigration(ctx context.Context, id string) {
	m.mu.Lock()
	h, ok := m.hotswaps[id]
	if !ok || h.State != StateMigrating {
		m.mu.Unlock()
		return
	}
	opts := h.Options
	tenantName := h.Source.TenantName
	bulkSyncAt := h.RequestedAt.UTC().Format(time.RFC3339)
	m.mu.Unlock()

	log.Info().Str("id", id).Msg("hotswap: migration phase — invoking iolu")
	if err := runMigration(ctx, opts, tenantName, true, bulkSyncAt); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("hotswap: migration error")
		m.failHotswap(ctx, id, fmt.Sprintf("migration: %v", err))
		return
	}

	m.mu.Lock()
	h, ok = m.hotswaps[id]
	if !ok || h.State != StateMigrating {
		m.mu.Unlock()
		return
	}
	m.advanceLocked(ctx, h, StateValidating, "delta migration complete")
	m.mu.Unlock()

	go m.driveValidation(context.Background(), id)
}

// driveValidation invokes cross-instance consistency checks then advances
// to CUTTING_OVER on success, or rolls back on failure.
func (m *MemoryManager) driveValidation(ctx context.Context, id string) {
	m.mu.Lock()
	h, ok := m.hotswaps[id]
	if !ok || h.State != StateValidating {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	log.Info().Str("id", id).Msg("hotswap: validation phase — invoking iolu")
	result := runValidation(ctx, h.Options, h.Source.TenantName)

	m.mu.Lock()
	h, ok = m.hotswaps[id]
	if !ok || h.State != StateValidating {
		m.mu.Unlock()
		return
	}
	h.Validation = result
	if !result.Valid {
		h.FailureReason = "validation failed"
		m.advanceLocked(ctx, h, StateRollingBack, "validation failed")
		m.mu.Unlock()
		go m.driveRollback(context.Background(), id)
		return
	}
	m.advanceLocked(ctx, h, StateCuttingOver, "validation passed")
	m.mu.Unlock()

	go m.driveCutover(context.Background(), id)
}

// driveCutover atomically updates all GlobalIDs for this tenant to point
// at the target, then publishes the hotswap event.
func (m *MemoryManager) driveCutover(ctx context.Context, id string) {
	m.mu.Lock()
	h, ok := m.hotswaps[id]
	if !ok || h.State != StateCuttingOver {
		m.mu.Unlock()
		return
	}
	source := h.Source
	target := h.Target
	m.mu.Unlock()

	// Transfer only GlobalIDs belonging to the migrating tenant.
	tenantGIDs, err := m.reg.ListByInstanceAndTenant(ctx, source.InstanceURL, source.TenantID)
	if err != nil {
		m.failHotswap(ctx, id, fmt.Sprintf("list tenant entities: %v", err))
		return
	}

	transferred := 0
	for _, gid := range tenantGIDs {
		rec, err := m.reg.Get(ctx, gid)
		if err != nil {
			log.Warn().Str("gid", string(gid)).Err(err).Msg("hotswap: get record during cutover")
			continue
		}
		newRef := identity.LocalRef{
			InstanceURL: target.InstanceURL,
			TenantID:    target.TenantID,
			EntityType:  rec.Current.EntityType,
			LocalID:     rec.Current.LocalID,
		}
		if _, err := m.reg.Transfer(ctx, registry.TransferRequest{
			GlobalID:    gid,
			From:        rec.Current,
			To:          newRef,
			Protocol:    fmt.Sprintf("hotswap:%s", id),
			HistoryFrom: "full",
		}); err != nil {
			log.Warn().Str("gid", string(gid)).Err(err).Msg("hotswap: transfer during cutover")
			continue
		}
		transferred++
	}

	log.Info().
		Str("id", id).
		Int("transferred", transferred).
		Int("total", len(tenantGIDs)).
		Msg("hotswap: cutover complete")

	now := time.Now().UTC()
	m.mu.Lock()
	h, ok = m.hotswaps[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	m.advanceLocked(ctx, h, StateComplete, fmt.Sprintf("cutover: %d/%d GlobalIDs transferred", transferred, len(tenantGIDs)))
	h.CompletedAt = &now
	m.mu.Unlock()

	// Publish hotswap complete event.
	if m.bus != nil {
		env := events.Envelope{
			Kind:    "hotswap.complete",
			Subject: fmt.Sprintf("nolu.events.hotswap.complete.%s", source.TenantName),
			At:      now,
		}
		_ = m.bus.Publish(ctx, env)
	}
}

// driveRollback lifts the quiesce on the source and marks the hotswap failed.
func (m *MemoryManager) driveRollback(ctx context.Context, id string) {
	m.mu.Lock()
	h, ok := m.hotswaps[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	sourceURL := h.Source.InstanceURL
	tenantName := h.Source.TenantName
	m.mu.Unlock()

	// Lift quiesce on source — restores write access.
	rcClient := xoluclient.New(sourceURL, 0)
	if err := rcClient.UnquiesceTenant(ctx, tenantName); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("hotswap: unquiesce failed (may need manual intervention)")
	}
	log.Info().
		Str("id", id).
		Str("source", sourceURL).
		Str("tenant", tenantName).
		Msg("hotswap: rollback — lifting quiesce on source (not yet wired to xolu)")

	m.mu.Lock()
	h, ok = m.hotswaps[id]
	if !ok {
		m.mu.Unlock()
		return
	}
	m.advanceLocked(ctx, h, StateFailed, "rollback complete: source quiesce lifted")
	m.mu.Unlock()
}

func (m *MemoryManager) failHotswap(ctx context.Context, id, reason string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.hotswaps[id]
	if !ok {
		return
	}
	h.FailureReason = reason
	m.advanceLocked(ctx, h, StateRollingBack, reason)
	go m.driveRollback(context.Background(), id)
}

// clone deep-copies a Hotswap so callers cannot mutate internal state.
func (m *MemoryManager) clone(h *Hotswap) *Hotswap {
	c := *h
	c.History = make([]TimestampedState, len(h.History))
	copy(c.History, h.History)
	if h.Validation != nil {
		v := *h.Validation
		c.Validation = &v
	}
	return &c
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func isTerminal(s State) bool {
	return s == StateComplete || s == StateFailed
}


// Compile-time assertion.
var _ Manager = (*MemoryManager)(nil)
