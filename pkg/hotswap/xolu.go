// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package hotswap

import (
	"context"
	"encoding/json"
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

const (
	// xoluHotswapTenant is the xolu tenant that owns hotswap records.
	// Shares the same xolu instance as XoluRegistry.
	xoluHotswapTenant = "nolu_registry"

	// xoluHotswapsEntity is the entity type for Hotswap records.
	xoluHotswapsEntity = "nolu_hotswaps"

	// xoluHotswapEventsEntity is the append-only event log for state transitions.
	xoluHotswapEventsEntity = "nolu_hotswap_events"
)

// xoluHotswapRecord is the JSON shape stored in xolu for each Hotswap.
// It extends Hotswap with flat indexed fields for OQL queries.
type xoluHotswapRecord struct {
	XoluID       int     `json:"xolu_id,omitempty"`
	HotswapIDStr string  `json:"hotswap_id"`
	StateStr     string  `json:"state_str"`
	SourceURL    string  `json:"source_url"`
	TenantName   string  `json:"tenant_name"`
	Version      int     `json:"_version,omitempty"`
	Record       Hotswap `json:"record"`
}

// xoluHotswapEvent is a single state transition record, append-only.
type xoluHotswapEvent struct {
	HotswapIDStr string    `json:"hotswap_id"`
	FromState    string    `json:"from_state"`
	ToState      string    `json:"to_state"`
	Note         string    `json:"note,omitempty"`
	At           time.Time `json:"at"`
}

// XoluHotswapManager is a durable implementation of Manager backed by xolu.
//
// Durability guarantee: every state transition is persisted to xolu before
// the async driver for the next phase is started. On nolu restart,
// Resume() re-arms drivers for all non-terminal hotswaps found in xolu.
//
// Concurrency: per-hotswap optimistic concurrency via xolu's _version field.
// Two concurrent state transitions for the same hotswap will result in one
// winning and one receiving a 409, which is surfaced as an error.
type XoluHotswapManager struct {
	client *xoluclient.Client // tenant-scoped to nolu_registry
	reg    registry.Registry
	bus    events.Bus
	dir    TenantInvalidator // optional; nil if no tenant directory

	// in-memory index for fast Get/List without OQL round-trip.
	mu    sync.RWMutex
	index map[string]*hotswapMeta // id → meta (state + xolu id + version)
}

type hotswapMeta struct {
	xoluID  int
	version int
	state   State
}

// NewXoluHotswapManager creates an XoluHotswapManager and runs Resume()
// to re-arm any in-progress hotswaps found in xolu.
// dir may be nil; if non-nil, InvalidateTenant is called when a hotswap enters QUIESCING.
func NewXoluHotswapManager(ctx context.Context, xoluBaseURL string, reg registry.Registry, bus events.Bus, dir TenantInvalidator) (*XoluHotswapManager, error) {
	client := xoluclient.NewTenant(xoluBaseURL, xoluHotswapTenant)

	// Verify connectivity.
	root := xoluclient.New(xoluBaseURL, 0)
	if err := root.Healthy(ctx); err != nil {
		return nil, fmt.Errorf("xolu_hotswap: xolu not reachable: %w", err)
	}
	// Ensure tenant exists (path mode: auto-created on first access).
	if err := root.EnsureTenant(ctx, xoluHotswapTenant); err != nil {
		return nil, fmt.Errorf("xolu_hotswap: ensure tenant: %w", err)
	}

	m := &XoluHotswapManager{
		client: client,
		reg:    reg,
		bus:    bus,
		dir:    dir,
		index:  make(map[string]*hotswapMeta),
	}

	if err := m.Resume(ctx); err != nil {
		return nil, fmt.Errorf("xolu_hotswap: resume: %w", err)
	}

	return m, nil
}

// Resume loads all non-terminal hotswaps from xolu and re-arms their drivers.
// Called on startup to recover from a process restart mid-hotswap.
func (m *XoluHotswapManager) Resume(ctx context.Context) error {
	rows, err := m.client.OQL(ctx, fmt.Sprintf(
		`SELECT * FROM %s WHERE state_str NOT IN ('complete','failed') ORDER BY id`,
		xoluHotswapsEntity,
	))
	if err != nil {
		// No records yet — not an error.
		return nil
	}

	resumed := 0
	for _, row := range rows {
		xr, err := unmarshalHotswapRecord(row)
		if err != nil {
			log.Warn().Err(err).Msg("xolu_hotswap: skip malformed record during resume")
			continue
		}
		xoluID, _ := xoluclient.IntIDFromMap(row)
		version, _ := intFieldHS(row, "_version")

		m.mu.Lock()
		m.index[xr.HotswapIDStr] = &hotswapMeta{
			xoluID:  xoluID,
			version: version,
			state:   State(xr.StateStr),
		}
		m.mu.Unlock()

		// Re-arm the driver for this state.
		h := xr.Record
		switch h.State {
		case StatePreparing:
			if h.Options.AutoAdvance {
				go m.driveQuiesce(context.Background(), h.ID)
			}
			// If not AutoAdvance: wait for operator Confirm — no driver needed.
		case StateQuiescing:
			go m.driveQuiesce(context.Background(), h.ID)
		case StateMigrating:
			go m.driveMigration(context.Background(), h.ID)
		case StateValidating:
			go m.driveValidation(context.Background(), h.ID)
		case StateCuttingOver:
			go m.driveCutover(context.Background(), h.ID)
		case StateRollingBack:
			go m.driveRollback(context.Background(), h.ID)
		}
		resumed++
		log.Info().
			Str("id", h.ID).
			Str("state", string(h.State)).
			Msg("xolu_hotswap: resumed")
	}

	if resumed > 0 {
		log.Info().Int("count", resumed).Msg("xolu_hotswap: resumed in-progress hotswaps")
	}
	return nil
}

// ── Manager interface ─────────────────────────────────────────────────────────

func (m *XoluHotswapManager) Request(ctx context.Context, source, target InstanceRef, opts HotswapOptions) (*Hotswap, error) {
	// Check no active hotswap for this source tenant.
	active, err := m.client.OQL(ctx, fmt.Sprintf(
		`SELECT hotswap_id FROM %s WHERE source_url = '%s' AND tenant_name = '%s' AND state_str NOT IN ('complete','failed') LIMIT 1`,
		xoluHotswapsEntity,
		escapeSingleQuoteHS(source.InstanceURL),
		escapeSingleQuoteHS(source.TenantName),
	))
	if err == nil && len(active) > 0 {
		return nil, fmt.Errorf("%w: %s/%s", ErrAlreadyExists, source.InstanceURL, source.TenantName)
	}

	// Validate reachability.
	srcClient := xoluclient.New(source.InstanceURL, 0)
	if err := srcClient.Healthy(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSourceUnreachable, err)
	}
	tgtClient := xoluclient.New(target.InstanceURL, 0)
	if err := tgtClient.Healthy(ctx); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTargetUnreachable, err)
	}

	// Count affected GlobalIDs.
	tenantGIDs, _ := m.reg.ListByInstanceAndTenant(ctx, source.InstanceURL, source.TenantID)

	if opts.QuiesceTimeout == 0 {
		opts.QuiesceTimeout = 30 * time.Second
	}
	opts.TimestampedHistory = true

	now := time.Now().UTC()
	h := &Hotswap{
		ID:          uuid.New().String(),
		Source:      source,
		Target:      target,
		State:       StatePreparing,
		Options:     opts,
		EntityCount: len(tenantGIDs),
		RequestedAt: now,
		History: []TimestampedState{
			{State: StateRequested, At: now},
			{State: StatePreparing, At: now, Note: "bulk sync phase initiated"},
		},
	}

	// Persist to xolu.
	xoluID, version, err := m.persistCreate(ctx, h, source)
	if err != nil {
		return nil, fmt.Errorf("xolu_hotswap: persist: %w", err)
	}

	m.mu.Lock()
	m.index[h.ID] = &hotswapMeta{xoluID: xoluID, version: version, state: h.State}
	m.mu.Unlock()

	m.appendEvent(ctx, h.ID, StateRequested, StatePreparing, "requested and preparing")

	log.Info().Str("id", h.ID).Str("source", source.TenantName).Msg("xolu_hotswap: created")

	if opts.AutoAdvance {
		go m.driveQuiesce(context.Background(), h.ID)
	}

	return m.clone(h), nil
}

func (m *XoluHotswapManager) Get(ctx context.Context, id string) (*Hotswap, error) {
	return m.loadFromXolu(ctx, id)
}

func (m *XoluHotswapManager) List(ctx context.Context, state *State) ([]*Hotswap, error) {
	q := fmt.Sprintf(`SELECT * FROM %s ORDER BY id`, xoluHotswapsEntity)
	if state != nil {
		q = fmt.Sprintf(`SELECT * FROM %s WHERE state_str = '%s' ORDER BY id`,
			xoluHotswapsEntity, string(*state))
	}
	rows, err := m.client.OQL(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("xolu_hotswap list: %w", err)
	}
	out := make([]*Hotswap, 0, len(rows))
	for _, row := range rows {
		xr, err := unmarshalHotswapRecord(row)
		if err != nil {
			continue
		}
		h := xr.Record
		out = append(out, m.clone(&h))
	}
	return out, nil
}

func (m *XoluHotswapManager) Confirm(ctx context.Context, id string) (*Hotswap, error) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil {
		return nil, err
	}
	if h.State != StatePreparing {
		return nil, fmt.Errorf("%w: expected preparing, got %s", ErrWrongState, h.State)
	}
	if err := m.advanceState(ctx, h, StateQuiescing, "operator confirmed"); err != nil {
		return nil, err
	}
	go m.driveQuiesce(context.Background(), id)
	return m.clone(h), nil
}

func (m *XoluHotswapManager) Abort(ctx context.Context, id string, reason string) (*Hotswap, error) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil {
		return nil, err
	}
	if isTerminal(h.State) {
		return nil, fmt.Errorf("%w: cannot abort a %s hotswap", ErrWrongState, h.State)
	}
	h.FailureReason = reason
	if err := m.advanceState(ctx, h, StateRollingBack, "operator aborted: "+reason); err != nil {
		return nil, err
	}
	go m.driveRollback(context.Background(), id)
	return m.clone(h), nil
}

func (m *XoluHotswapManager) Status(ctx context.Context, id string) (*HotswapStatus, error) {
	h, err := m.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	elapsed := time.Duration(0)
	if len(h.History) > 0 {
		elapsed = time.Since(h.History[len(h.History)-1].At)
	}
	return &HotswapStatus{
		Hotswap:      *h,
		CurrentLag:   h.Lag,
		PhaseElapsed: elapsed,
	}, nil
}

// ── State persistence ─────────────────────────────────────────────────────────

func (m *XoluHotswapManager) advanceState(ctx context.Context, h *Hotswap, next State, note string) error {
	prev := h.State
	h.State = next
	h.History = append(h.History, TimestampedState{
		State: next,
		At:    time.Now().UTC(),
		Note:  note,
	})

	m.mu.RLock()
	meta, ok := m.index[h.ID]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("xolu_hotswap: no index entry for %s", h.ID)
	}

	newVersion, err := m.persistUpdate(ctx, h, meta.xoluID, meta.version)
	if err != nil {
		return fmt.Errorf("xolu_hotswap: persist state %s→%s: %w", prev, next, err)
	}

	m.mu.Lock()
	meta.version = newVersion
	meta.state = next
	m.mu.Unlock()

	m.appendEvent(ctx, h.ID, prev, next, note)

	log.Info().
		Str("id", h.ID).
		Str("state", string(next)).
		Str("note", note).
		Msg("xolu_hotswap: state persisted")
	return nil
}

func (m *XoluHotswapManager) persistCreate(ctx context.Context, h *Hotswap, source InstanceRef) (xoluID, version int, err error) {
	xr := xoluHotswapRecord{
		HotswapIDStr: h.ID,
		StateStr:     string(h.State),
		SourceURL:    source.InstanceURL,
		TenantName:   source.TenantName,
		Record:       *h,
	}
	data, err := marshalHS(xr)
	if err != nil {
		return 0, 0, err
	}
	result, err := m.client.Create(ctx, xoluHotswapsEntity, data)
	if err != nil {
		return 0, 0, err
	}
	xoluID, _ = xoluclient.IntIDFromMap(result)
	version, _ = intFieldHS(result, "_version")
	return xoluID, version, nil
}

func (m *XoluHotswapManager) persistUpdate(ctx context.Context, h *Hotswap, xoluID, version int) (newVersion int, err error) {
	xr := xoluHotswapRecord{
		XoluID:       xoluID,
		HotswapIDStr: h.ID,
		StateStr:     string(h.State),
		SourceURL:    h.Source.InstanceURL,
		TenantName:   h.Source.TenantName,
		Version:      version,
		Record:       *h,
	}
	data, err := marshalHS(xr)
	if err != nil {
		return 0, err
	}
	result, err := m.client.Save(ctx, xoluHotswapsEntity, xoluID, data)
	if err != nil {
		return 0, err
	}
	newVersion, _ = intFieldHS(result, "_version")
	return newVersion, nil
}

func (m *XoluHotswapManager) loadFromXolu(ctx context.Context, id string) (*Hotswap, error) {
	rows, err := m.client.OQL(ctx, fmt.Sprintf(
		`SELECT * FROM %s WHERE hotswap_id = '%s' LIMIT 1`,
		xoluHotswapsEntity, escapeSingleQuoteHS(id),
	))
	if err != nil || len(rows) == 0 {
		return nil, ErrNotFound
	}
	xr, err := unmarshalHotswapRecord(rows[0])
	if err != nil {
		return nil, fmt.Errorf("xolu_hotswap: unmarshal: %w", err)
	}
	// Update index with fresh version.
	xoluID, _ := xoluclient.IntIDFromMap(rows[0])
	version, _ := intFieldHS(rows[0], "_version")
	m.mu.Lock()
	m.index[id] = &hotswapMeta{xoluID: xoluID, version: version, state: State(xr.StateStr)}
	m.mu.Unlock()

	h := xr.Record
	return m.clone(&h), nil
}

func (m *XoluHotswapManager) appendEvent(ctx context.Context, id string, from, to State, note string) {
	ev := xoluHotswapEvent{
		HotswapIDStr: id,
		FromState:    string(from),
		ToState:      string(to),
		Note:         note,
		At:           time.Now().UTC(),
	}
	data, err := marshalHS(ev)
	if err != nil {
		return
	}
	go func() {
		_, _ = m.client.Create(context.Background(), xoluHotswapEventsEntity, data)
	}()
}

// ── Async phase drivers (mirrors MemoryManager) ───────────────────────────────

func (m *XoluHotswapManager) driveQuiesce(ctx context.Context, id string) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateQuiescing {
		return
	}

	log.Info().Str("id", id).Str("source", h.Source.InstanceURL).Str("tenant", h.Source.TenantName).
		Msg("xolu_hotswap: signalling quiesce")

	// Notify directory: tenant location is unstable during migration.
	if m.dir != nil {
		m.dir.InvalidateTenant(h.Source.TenantName)
	}

	srcClient := xoluclient.New(h.Source.InstanceURL, 0)
	if _, err := srcClient.QuiesceTenant(ctx, h.Source.TenantName, ""); err != nil {
		log.Warn().Err(err).Str("id", id).
			Msg("xolu_hotswap: quiesce call failed — proceeding (xolu may not support quiesce)")
	}
	select {
	case <-time.After(100 * time.Millisecond):
		// Brief pause to allow in-flight requests to drain.
	case <-ctx.Done():
		m.fail(ctx, id, "context cancelled during quiesce")
		return
	}

	h, err = m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateQuiescing {
		return
	}
	if err := m.advanceState(ctx, h, StateMigrating, "source quiesce confirmed"); err != nil {
		log.Error().Err(err).Str("id", id).Msg("xolu_hotswap: advance to migrating failed")
		return
	}
	go m.driveMigration(context.Background(), id)
}

func (m *XoluHotswapManager) driveMigration(ctx context.Context, id string) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateMigrating {
		return
	}

	log.Info().Str("id", id).Msg("xolu_hotswap: migration phase — invoking iolu")
	bulkSyncAt := h.RequestedAt.UTC().Format(time.RFC3339)
	if err := runMigration(ctx, h.Options, h.Source.TenantName, true, bulkSyncAt); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("xolu_hotswap: migration error")
		m.fail(ctx, id, fmt.Sprintf("migration: %v", err))
		return
	}

	h, err = m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateMigrating {
		return
	}
	if err := m.advanceState(ctx, h, StateValidating, "delta migration complete"); err != nil {
		log.Error().Err(err).Str("id", id).Msg("xolu_hotswap: advance to validating failed")
		return
	}
	go m.driveValidation(context.Background(), id)
}

func (m *XoluHotswapManager) driveValidation(ctx context.Context, id string) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateValidating {
		return
	}

	log.Info().Str("id", id).Msg("xolu_hotswap: validation phase — invoking iolu")
	result := runValidation(ctx, h.Options, h.Source.TenantName)

	h, err = m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateValidating {
		return
	}
	h.Validation = result
	if !result.Valid {
		h.FailureReason = "validation failed"
		if err := m.advanceState(ctx, h, StateRollingBack, "validation failed"); err != nil {
			log.Error().Err(err).Str("id", id).Msg("xolu_hotswap: advance to rolling_back failed")
		}
		go m.driveRollback(context.Background(), id)
		return
	}
	if err := m.advanceState(ctx, h, StateCuttingOver, "validation passed"); err != nil {
		log.Error().Err(err).Str("id", id).Msg("xolu_hotswap: advance to cutting_over failed")
		return
	}
	go m.driveCutover(context.Background(), id)
}

func (m *XoluHotswapManager) driveCutover(ctx context.Context, id string) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateCuttingOver {
		return
	}

	// Transfer all affected GlobalIDs in the registry.
	tenantGIDs, _ := m.reg.ListByInstanceAndTenant(ctx, h.Source.InstanceURL, h.Source.TenantID)

	transferred := 0
	for _, gid := range tenantGIDs {
		rec, err := m.reg.Get(ctx, gid)
		if err != nil {
			continue
		}
		newRef := identity.LocalRef{
			InstanceURL: h.Target.InstanceURL,
			TenantID:    h.Target.TenantID,
			EntityType:  rec.Current.EntityType,
			LocalID:     rec.Current.LocalID,
		}
		if _, err := m.reg.Transfer(ctx, registry.TransferRequest{
			GlobalID: gid, From: rec.Current, To: newRef,
			Protocol: fmt.Sprintf("hotswap:%s", id),
		}); err != nil {
			log.Warn().Err(err).Str("gid", string(gid)).Msg("xolu_hotswap: transfer during cutover")
			continue
		}
		transferred++
	}

	now := time.Now().UTC()
	h, err = m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateCuttingOver {
		return
	}
	h.CompletedAt = &now
	note := fmt.Sprintf("cutover: %d/%d GlobalIDs transferred", transferred, len(tenantGIDs))
	if err := m.advanceState(ctx, h, StateComplete, note); err != nil {
		log.Error().Err(err).Str("id", id).Msg("xolu_hotswap: advance to complete failed")
		return
	}

	if m.bus != nil {
		env := events.Envelope{
			Kind:    "hotswap.complete",
			Subject: fmt.Sprintf("nolu.events.hotswap.complete.%s", h.Source.TenantName),
			At:      now,
		}
		_ = m.bus.Publish(ctx, env)
	}

	log.Info().Str("id", id).Int("transferred", transferred).Msg("xolu_hotswap: complete")
}

func (m *XoluHotswapManager) driveRollback(ctx context.Context, id string) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateRollingBack {
		return
	}

	log.Info().Str("id", id).Str("source", h.Source.InstanceURL).Msg("xolu_hotswap: rollback — lifting quiesce")
	rcClient := xoluclient.New(h.Source.InstanceURL, 0)
	if err := rcClient.UnquiesceTenant(ctx, h.Source.TenantName); err != nil {
		log.Warn().Err(err).Str("id", id).Msg("xolu_hotswap: unquiesce failed (may need manual intervention)")
	}

	h, err = m.loadFromXolu(ctx, id)
	if err != nil || h.State != StateRollingBack {
		return
	}
	if err := m.advanceState(ctx, h, StateFailed, "rollback complete: source quiesce lifted"); err != nil {
		log.Error().Err(err).Str("id", id).Msg("xolu_hotswap: advance to failed")
	}
}

func (m *XoluHotswapManager) fail(ctx context.Context, id, reason string) {
	h, err := m.loadFromXolu(ctx, id)
	if err != nil {
		return
	}
	h.FailureReason = reason
	_ = m.advanceState(ctx, h, StateRollingBack, reason)
	go m.driveRollback(context.Background(), id)
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (m *XoluHotswapManager) clone(h *Hotswap) *Hotswap {
	c := *h
	c.History = make([]TimestampedState, len(h.History))
	copy(c.History, h.History)
	if h.Validation != nil {
		v := *h.Validation
		c.Validation = &v
	}
	return &c
}

func marshalHS(v interface{}) (map[string]interface{}, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func unmarshalHotswapRecord(raw map[string]interface{}) (*xoluHotswapRecord, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var xr xoluHotswapRecord
	if err := json.Unmarshal(b, &xr); err != nil {
		return nil, err
	}
	return &xr, nil
}

func intFieldHS(m map[string]interface{}, key string) (int, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	}
	return 0, false
}

func escapeSingleQuoteHS(s string) string {
	return escapeOQL(s)
}

func escapeOQL(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '\'' {
			out = append(out, '\'', '\'')
		} else {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// Compile-time assertion.
var _ Manager = (*XoluHotswapManager)(nil)
