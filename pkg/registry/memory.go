// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/identity"
)

// MemoryRegistry is an in-process, non-durable implementation of Registry.
// All records are lost when the process exits. Intended for development,
// testing, and the demo environment.
//
// Thread-safe: all methods acquire the internal mutex.
type MemoryRegistry struct {
	mu          sync.RWMutex
	records     map[identity.GlobalID]*Record
	bus         events.Bus
	subs        []memorySub
	host        string
}

type memorySub struct {
	filter SubscriptionFilter
	ch     chan<- Event
}

// NewMemoryRegistry creates a MemoryRegistry that publishes events to bus.
// host is this registry's hostname, used as the authority in minted GlobalIDs.
func NewMemoryRegistry(host string, bus events.Bus) *MemoryRegistry {
	return &MemoryRegistry{
		records: make(map[identity.GlobalID]*Record),
		bus:     bus,
		host:    host,
	}
}

func (r *MemoryRegistry) Register(ctx context.Context, registryHost, entityType string, owner identity.LocalRef) (*Record, error) {
	if registryHost == "" {
		registryHost = r.host
	}
	gid := identity.MintGlobalID(registryHost, entityType)

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.records[gid]; exists {
		return nil, ErrAlreadyExists
	}

	now := time.Now().UTC()
	rec := &Record{
		OwnershipRecord: identity.OwnershipRecord{
			GlobalID: gid,
			Current:  owner,
			Since:    now,
		},
		Status:    StatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	r.records[gid] = rec
	clone := r.clone(rec)

	r.publishLocked(ctx, Event{
		Kind:     EventRegistered,
		GlobalID: gid,
		Record:   clone,
		At:       now,
	})

	return clone, nil
}

func (r *MemoryRegistry) Get(ctx context.Context, id identity.GlobalID) (*Record, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.records[id]
	if !ok {
		return nil, ErrNotFound
	}
	return r.clone(rec), nil
}

func (r *MemoryRegistry) Resolve(ctx context.Context, id identity.GlobalID) (identity.LocalRef, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	rec, ok := r.records[id]
	if !ok {
		return identity.LocalRef{}, ErrNotFound
	}
	if rec.Status == StatusRetired {
		return identity.LocalRef{}, ErrRetired
	}
	return rec.Current, nil
}

func (r *MemoryRegistry) Transfer(ctx context.Context, req TransferRequest) (*Record, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[req.GlobalID]
	if !ok {
		return nil, ErrNotFound
	}
	if rec.Status == StatusRetired {
		return nil, ErrRetired
	}
	// Optimistic concurrency: From must match current owner.
	if rec.Current != req.From {
		return nil, fmt.Errorf("%w: expected current owner %s, got %s",
			ErrInvalidTransfer, rec.Current, req.From)
	}

	now := time.Now().UTC()
	xfer := identity.Transfer{
		From:        req.From,
		To:          req.To,
		At:          now,
		Protocol:    req.Protocol,
		HistoryFrom: req.HistoryFrom,
	}
	rec.History = append(rec.History, xfer)
	rec.Current = req.To
	rec.Since = now
	rec.UpdatedAt = now
	clone := r.clone(rec)

	r.publishLocked(ctx, Event{
		Kind:     EventTransferred,
		GlobalID: req.GlobalID,
		Record:   clone,
		At:       now,
	})

	return clone, nil
}

func (r *MemoryRegistry) Retire(ctx context.Context, id identity.GlobalID, reason string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	rec, ok := r.records[id]
	if !ok {
		return ErrNotFound
	}
	if rec.Status == StatusRetired {
		return ErrRetired
	}

	now := time.Now().UTC()
	rec.Status = StatusRetired
	rec.UpdatedAt = now
	clone := r.clone(rec)

	r.publishLocked(ctx, Event{
		Kind:     EventRetired,
		GlobalID: id,
		Record:   clone,
		At:       now,
	})

	return nil
}

func (r *MemoryRegistry) Subscribe(ctx context.Context, filter SubscriptionFilter, ch chan<- Event) (func(), error) {
	r.mu.Lock()
	sub := memorySub{filter: filter, ch: ch}
	r.subs = append(r.subs, sub)
	r.mu.Unlock()

	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		for i, s := range r.subs {
			if s.ch == ch {
				r.subs = append(r.subs[:i], r.subs[i+1:]...)
				return
			}
		}
	}, nil
}

func (r *MemoryRegistry) ListByInstance(ctx context.Context, instanceURL string) ([]identity.GlobalID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var ids []identity.GlobalID
	for id, rec := range r.records {
		if rec.Status == StatusActive && rec.Current.InstanceURL == instanceURL {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (r *MemoryRegistry) ListByEntityType(ctx context.Context, entityType string) ([]identity.GlobalID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var ids []identity.GlobalID
	for id, rec := range r.records {
		if rec.Status != StatusActive {
			continue
		}
		et, err := id.EntityType()
		if err != nil {
			continue
		}
		if et == entityType {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

// publishLocked publishes an event to the bus and to in-process subscribers.
// Must be called with r.mu held (write lock).
func (r *MemoryRegistry) publishLocked(ctx context.Context, ev Event) {
	// In-process subscribers (non-blocking drop if channel full).
	for _, sub := range r.subs {
		if r.matchFilter(sub.filter, ev) {
			select {
			case sub.ch <- ev:
			default:
			}
		}
	}

	// Bus publication (best-effort; log on error).
	if r.bus != nil {
		payload, _ := json.Marshal(ev.Record)
		entityType := ""
		if ev.Record != nil {
			entityType, _ = ev.GlobalID.EntityType()
		}
		env := events.Envelope{
			ID:         uuid.New().String(),
			Subject:    events.SubjectFor(string(ev.Kind), entityType),
			GlobalID:   ev.GlobalID,
			Kind:       string(ev.Kind),
			EntityType: entityType,
			At:         ev.At,
			Payload:    payload,
		}
		// Publish asynchronously to avoid holding the lock while doing I/O.
		go func() {
			if err := r.bus.Publish(context.Background(), env); err != nil {
				// Log but don't panic — bus errors must not corrupt registry state.
				_ = err
			}
		}()
	}
}

func (r *MemoryRegistry) matchFilter(f SubscriptionFilter, ev Event) bool {
	if len(f.GlobalIDs) > 0 {
		found := false
		for _, id := range f.GlobalIDs {
			if id == ev.GlobalID {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if len(f.EntityTypes) > 0 {
		et, _ := ev.GlobalID.EntityType()
		found := false
		for _, t := range f.EntityTypes {
			if strings.EqualFold(t, et) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

// clone deep-copies a Record so callers cannot mutate internal state.
func (r *MemoryRegistry) clone(rec *Record) *Record {
	c := *rec
	c.History = make([]identity.Transfer, len(rec.History))
	copy(c.History, rec.History)
	return &c
}

// ListByInstanceAndTenant returns all active GlobalIDs on the given instance
// scoped to the given tenant ID. If tenantID is 0, returns all unscoped entities.
func (r *MemoryRegistry) ListByInstanceAndTenant(ctx context.Context, instanceURL string, tenantID uint16) ([]identity.GlobalID, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var ids []identity.GlobalID
	for id, rec := range r.records {
		if rec.Status != StatusActive {
			continue
		}
		if rec.Current.InstanceURL != instanceURL {
			continue
		}
		if rec.Current.TenantID != tenantID {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// SeedDirectory satisfies DirectorySeeder, allowing TenantDirectory to
// bootstrap from the current registry state on startup.
func (r *MemoryRegistry) SeedDirectory(ctx context.Context, dir *TenantDirectory) error {
	r.mu.RLock()
	defer r.mu.RUnlock()

	type key struct{ url, name string }
	counts := map[key]int{}
	ids    := map[key]uint16{}

	for _, rec := range r.records {
		if rec.Status == StatusRetired {
			continue
		}
		ref := rec.Current
		name := tenantNameFromRef(ref)
		if name == "" {
			continue
		}
		k := key{url: ref.InstanceURL, name: name}
		counts[k]++
		ids[k] = ref.TenantID
	}
	for k, count := range counts {
		dir.Upsert(k.name, k.url, ids[k], count)
	}
	return nil
}

// Compile-time assertions.
var _ Registry = (*MemoryRegistry)(nil)
var _ DirectorySeeder = (*MemoryRegistry)(nil)
