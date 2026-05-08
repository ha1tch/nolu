// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package registry

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
)

// TenantEntry is a single entry in the tenant directory.
// It records the current location of a named tenant on a specific instance.
type TenantEntry struct {
	// TenantName is the human-readable tenant name (e.g. "vendocorp").
	// Derived from xolu path-mode URLs or set explicitly at registration.
	TenantName string `json:"tenant_name"`

	// InstanceURL is the xolu base URL currently serving this tenant.
	InstanceURL string `json:"instance_url"`

	// TenantID is the numeric tenant ID on that instance.
	TenantID uint16 `json:"tenant_id"`

	// EntityCount is the number of active GlobalIDs currently registered
	// to this tenant on this instance. Updated on every registry event.
	EntityCount int `json:"entity_count"`

	// FirstSeen is when this tenant/instance combination was first recorded.
	FirstSeen time.Time `json:"first_seen"`

	// LastSeen is the most recent registry event touching this tenant.
	LastSeen time.Time `json:"last_seen"`

	// StableUntil is the proxy cache hint. During a hotswap this is set
	// to now (or the near future) to force rapid revalidation.
	StableUntil time.Time `json:"stable_until"`
}

// directoryKey is the composite key for the tenant directory.
// A tenant name may exist on multiple instances simultaneously
// (e.g. during a hotswap transition). The key is instance+tenant.
type directoryKey struct {
	instanceURL string
	tenantName  string
}

// TenantDirectory maintains a real-time mapping from (instanceURL, tenantName)
// to TenantEntry. It subscribes to registry events and updates incrementally
// so that locate queries are O(1) without scanning all records.
//
// The directory is eventually consistent: it reflects the registry state as of
// the last processed event. Under normal operation this lag is microseconds.
//
// Usage:
//
//	dir := NewTenantDirectory(reg, defaultCacheTTL)
//	dir.Start(ctx)
//	entry, ok := dir.Locate("vendocorp")
type TenantDirectory struct {
	reg        Registry
	cacheTTL   time.Duration
	cancelSub  func()

	mu      sync.RWMutex
	byName  map[string]*TenantEntry    // tenantName → entry (best match: highest entity count)
	byKey   map[directoryKey]*TenantEntry // (instanceURL, tenantName) → entry
}

// NewTenantDirectory creates a TenantDirectory that subscribes to reg's events.
// cacheTTL controls the StableUntil field in locate responses.
// Call Start to begin processing events.
func NewTenantDirectory(reg Registry, cacheTTL time.Duration) *TenantDirectory {
	if cacheTTL == 0 {
		cacheTTL = 30 * time.Second
	}
	return &TenantDirectory{
		reg:      reg,
		cacheTTL: cacheTTL,
		byName:   make(map[string]*TenantEntry),
		byKey:    make(map[directoryKey]*TenantEntry),
	}
}

// Start subscribes to registry events and initialises the directory by
// scanning all currently registered entities. Blocks until ctx is done.
// Run in a goroutine.
func (d *TenantDirectory) Start(ctx context.Context) error {
	ch := make(chan Event, 256)
	cancel, err := d.reg.Subscribe(ctx, SubscriptionFilter{}, ch)
	if err != nil {
		return fmt.Errorf("tenant directory: subscribe: %w", err)
	}
	d.cancelSub = cancel

	// Bootstrap: scan all active records to build initial state.
	if err := d.bootstrap(ctx); err != nil {
		cancel()
		return fmt.Errorf("tenant directory: bootstrap: %w", err)
	}

	// Process events.
	go func() {
		defer cancel()
		for {
			select {
			case ev, ok := <-ch:
				if !ok {
					return
				}
				d.handleEvent(ev)
			case <-ctx.Done():
				return
			}
		}
	}()

	return nil
}

// bootstrap scans all active GlobalIDs to build the initial directory state.
// This catches entities registered before Start was called.
func (d *TenantDirectory) bootstrap(ctx context.Context) error {
	// We don't have a ListAll method; scan via ListByInstance would require
	// knowing all instances. Instead we trigger a rescan by listing all
	// entity types — but we also don't have that. The practical approach
	// for bootstrap: scan via a registry walk if the registry supports it,
	// otherwise accept that the directory builds up from live events only.
	//
	// MemoryRegistry: we access it directly via type assertion.
	// XoluRegistry: events catch all future changes; historical state is
	// loaded on startup via XoluRegistry.seedDirectory if implemented.
	//
	// For now: walk via the registry's internal list if available.
	if seeder, ok := d.reg.(DirectorySeeder); ok {
		return seeder.SeedDirectory(ctx, d)
	}
	return nil
}

// DirectorySeeder is an optional interface that Registry implementations can
// satisfy to provide initial tenant directory state on startup.
type DirectorySeeder interface {
	SeedDirectory(ctx context.Context, dir *TenantDirectory) error
}

// handleEvent updates the directory in response to a registry event.
func (d *TenantDirectory) handleEvent(ev Event) {
	if ev.Record == nil {
		return
	}

	ref := ev.Record.Current
	tenantName := tenantNameFromRef(ref)
	if tenantName == "" {
		return
	}

	key := directoryKey{instanceURL: ref.InstanceURL, tenantName: tenantName}
	now := time.Now().UTC()

	d.mu.Lock()
	defer d.mu.Unlock()

	switch ev.Kind {
	case EventRegistered:
		entry := d.getOrCreateLocked(key, ref, now)
		entry.EntityCount++
		entry.LastSeen = now
		entry.StableUntil = now.Add(d.cacheTTL)
		d.updateByNameLocked(tenantName, entry)

	case EventTransferred:
		// Remove from old location if different from current.
		// The Record.History has the From ref; we derive it from the
		// penultimate history entry.
		if len(ev.Record.History) > 0 {
			fromRef := ev.Record.History[len(ev.Record.History)-1].From
			fromName := tenantNameFromRef(fromRef)
			if fromName != "" {
				fromKey := directoryKey{instanceURL: fromRef.InstanceURL, tenantName: fromName}
				if fromKey != key {
					if fromEntry := d.byKey[fromKey]; fromEntry != nil {
						fromEntry.EntityCount--
						fromEntry.LastSeen = now
						// Short StableUntil during transition.
						fromEntry.StableUntil = now.Add(2 * time.Second)
						if fromEntry.EntityCount <= 0 {
							delete(d.byKey, fromKey)
							d.rebuildByNameLocked()
						} else {
							d.updateByNameLocked(fromName, fromEntry)
						}
					}
				}
			}
		}
		// Add to new location.
		entry := d.getOrCreateLocked(key, ref, now)
		entry.EntityCount++
		entry.LastSeen = now
		entry.StableUntil = now.Add(d.cacheTTL)
		d.updateByNameLocked(tenantName, entry)

	case EventRetired:
		if entry := d.byKey[key]; entry != nil {
			entry.EntityCount--
			entry.LastSeen = now
			if entry.EntityCount <= 0 {
				delete(d.byKey, key)
				d.rebuildByNameLocked()
			} else {
				d.updateByNameLocked(tenantName, entry)
			}
		}
	}
}

// Locate returns the TenantEntry for the named tenant, or false if unknown.
// When multiple instances host entities for the same tenant name (possible
// during a hotswap transition), returns the entry with the most entities.
func (d *TenantDirectory) Locate(tenantName string) (TenantEntry, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	entry, ok := d.byName[tenantName]
	if !ok || entry == nil {
		return TenantEntry{}, false
	}
	return *entry, true
}

// LocateOnInstance returns the TenantEntry for a specific (instance, tenant)
// combination. Used by ListByInstanceAndTenant.
func (d *TenantDirectory) LocateOnInstance(instanceURL, tenantName string) (TenantEntry, bool) {
	d.mu.RLock()
	defer d.mu.RUnlock()
	entry, ok := d.byKey[directoryKey{instanceURL: instanceURL, tenantName: tenantName}]
	if !ok {
		return TenantEntry{}, false
	}
	return *entry, true
}

// ListAll returns all known tenant entries sorted by name then instance URL.
func (d *TenantDirectory) ListAll() []TenantEntry {
	d.mu.RLock()
	defer d.mu.RUnlock()
	out := make([]TenantEntry, 0, len(d.byKey))
	for _, e := range d.byKey {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantName != out[j].TenantName {
			return out[i].TenantName < out[j].TenantName
		}
		return out[i].InstanceURL < out[j].InstanceURL
	})
	return out
}

// InvalidateTenant marks a tenant's location as unstable (short StableUntil).
// Called by the hotswap manager when a hotswap begins for this tenant,
// forcing proxy clients to revalidate quickly.
func (d *TenantDirectory) InvalidateTenant(tenantName string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if entry, ok := d.byName[tenantName]; ok {
		entry.StableUntil = time.Now().UTC().Add(2 * time.Second)
	}
	// Also invalidate all keys for this tenant name.
	for key, entry := range d.byKey {
		if key.tenantName == tenantName {
			entry.StableUntil = time.Now().UTC().Add(2 * time.Second)
		}
	}
}

// Upsert allows external code (e.g. DirectorySeeder) to populate entries.
func (d *TenantDirectory) Upsert(tenantName, instanceURL string, tenantID uint16, entityCount int) {
	key := directoryKey{instanceURL: instanceURL, tenantName: tenantName}
	now := time.Now().UTC()

	d.mu.Lock()
	defer d.mu.Unlock()

	entry := d.byKey[key]
	if entry == nil {
		entry = &TenantEntry{
			TenantName:  tenantName,
			InstanceURL: instanceURL,
			TenantID:    tenantID,
			FirstSeen:   now,
		}
		d.byKey[key] = entry
	}
	entry.EntityCount = entityCount
	entry.TenantID = tenantID
	entry.LastSeen = now
	entry.StableUntil = now.Add(d.cacheTTL)
	d.updateByNameLocked(tenantName, entry)
}

// ── Internal helpers ──────────────────────────────────────────────────────────

func (d *TenantDirectory) getOrCreateLocked(key directoryKey, ref identity.LocalRef, now time.Time) *TenantEntry {
	if entry, ok := d.byKey[key]; ok {
		return entry
	}
	entry := &TenantEntry{
		TenantName:  key.tenantName,
		InstanceURL: key.instanceURL,
		TenantID:    ref.TenantID,
		FirstSeen:   now,
		StableUntil: now.Add(d.cacheTTL),
	}
	d.byKey[key] = entry
	return entry
}

// updateByNameLocked updates byName[tenantName] to point to the entry with
// the highest entity count for that tenant name.
// Must be called with mu held (write lock).
func (d *TenantDirectory) updateByNameLocked(tenantName string, candidate *TenantEntry) {
	current := d.byName[tenantName]
	if current == nil || candidate.EntityCount >= current.EntityCount {
		d.byName[tenantName] = candidate
		return
	}
	// current has more entities — keep it, but update if candidate overtakes later.
}

// rebuildByNameLocked reconstructs byName from scratch after a deletion.
// Must be called with mu held (write lock).
func (d *TenantDirectory) rebuildByNameLocked() {
	d.byName = make(map[string]*TenantEntry)
	for _, entry := range d.byKey {
		d.updateByNameLocked(entry.TenantName, entry)
	}
}

// tenantNameFromRef returns the tenant name from a LocalRef.
// Returns empty string for unscoped entities (TenantID==0), which are
// not tracked in the tenant directory.
func tenantNameFromRef(ref identity.LocalRef) string {
	if ref.TenantID == 0 {
		return ""
	}
	if ref.TenantName != "" {
		return ref.TenantName
	}
	// TenantID known but name not set — fall back to numeric string.
	// The directory will track this entry by ID until a named registration
	// updates it.
	return fmt.Sprintf("tenant-%d", ref.TenantID)
}
