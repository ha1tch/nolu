// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package tenant provides tenant-scoping helpers for the middle and handler tiers.
//
// The storage tier enforces isolation by construction (see storage.StoreConfig).
// This package provides the supporting utilities that other tiers need:
//
//   - NodeID / CacheKey: construct globally-unique identifiers when a shared
//     in-memory structure (graph, cache) spans multiple tenants.
//   - Registry: maps human-readable tenant names to uint16 identifiers.
//     When a Persister is attached, mappings are durable across restarts.
package tenant

import (
	"context"
	"fmt"
	"sync"
)

// ---------------------------------------------------------------------------
// Node ID and cache key helpers
// ---------------------------------------------------------------------------

// NodeID returns a graph node identifier scoped to a tenant.
// For tenant 0 (unscoped): "entity:id"
// For non-zero tenants:    "XXXX@entity:id"
func NodeID(tenantID uint16, entity string, id int) string {
	if tenantID == 0 {
		return fmt.Sprintf("%s:%d", entity, id)
	}
	return fmt.Sprintf("%04X@%s:%d", tenantID, entity, id)
}

// GraphNodePrefix returns the XXXX@ prefix used to namespace graph node IDs
// in a shared in-memory graph that spans multiple tenants.
// For tenant 0 (unscoped): "" (no prefix; node IDs are bare "entity:id")
// For non-zero tenants:    "XXXX@" (e.g. "0001@")
//
// Uses uppercase hex. NodeIDPrefix is the corresponding parser; both must
// agree on case. If this format ever changes, NodeIDPrefix must be updated.
func GraphNodePrefix(tenantID uint16) string {
	if tenantID == 0 {
		return ""
	}
	return fmt.Sprintf("%04X@", tenantID)
}

// NodeIDPrefix extracts the XXXX@ tenant prefix from a graph node ID,
// returning "" if the node ID carries no prefix.
//
// Only uppercase hex digits (0-9, A-F) are recognised, matching the output
// of GraphNodePrefix. Lowercase hex will cause this function to return "".
// This intentional strictness prevents ambiguous or malformed prefixes from
// being silently accepted.
func NodeIDPrefix(nodeID string) string {
	if len(nodeID) < 5 || nodeID[4] != '@' {
		return ""
	}
	for i := 0; i < 4; i++ {
		c := nodeID[i]
		if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F')) {
			return ""
		}
	}
	return nodeID[:5]
}

// NodeIDStripped returns nodeID with its XXXX@ tenant prefix removed, if any.
// For bare node IDs (no prefix) the input is returned unchanged.
// Intended for use in error messages that must not leak internal prefixes.
func NodeIDStripped(nodeID string) string {
	if p := NodeIDPrefix(nodeID); p != "" {
		return nodeID[len(p):]
	}
	return nodeID
}

// StorageDirSegment returns the directory name used for tenant-scoped file
// storage (timeseries data, JSON file store). The segment is intended to be
// joined with a base directory using filepath.Join.
// For tenant 0 (unscoped): "" (data lives directly in the base directory)
// For non-zero tenants:    "tXXXX" (e.g. "t0001")
//
// Uses uppercase hex, consistent with GraphNodePrefix.
func StorageDirSegment(tenantID uint16) string {
	if tenantID == 0 {
		return ""
	}
	return fmt.Sprintf("t%04X", tenantID)
}

// ScopeKey prepends the tenant's hex ID to an arbitrary cache or lookup key,
// producing a tenant-scoped key of the form "XXXX:key".
// For tenant 0 (unscoped): the key is returned unchanged.
// For non-zero tenants:    "XXXX:key" (e.g. "0001:post:list:1:10")
//
// This is the generic scoping primitive; use CacheKey, CachePattern, etc.
// for the specialised cache key formats.
func ScopeKey(tenantID uint16, key string) string {
	if tenantID == 0 {
		return key
	}
	return fmt.Sprintf("%04X:%s", tenantID, key)
}

// GraphEdgesTableName returns the SQLite table name used to store graph edges
// for a given tenant. All tenants, including tenant 0, get their own table.
// The name is "graph_tXXXX" where XXXX is the zero-padded hex tenant ID,
// e.g. "graph_t0000" for tenant 0, "graph_t0001" for tenant 1.
func GraphEdgesTableName(tenantID uint16) string {
	return fmt.Sprintf("graph_t%04X", tenantID)
}

// CacheKey returns a cache key scoped to a tenant.
// For tenant 0 (unscoped): "entity:id"
// For non-zero tenants:    "XXXX:entity:id"
func CacheKey(tenantID uint16, entity string, id int) string {
	if tenantID == 0 {
		return fmt.Sprintf("%s:%d", entity, id)
	}
	return fmt.Sprintf("%04X:%s:%d", tenantID, entity, id)
}

// CachePattern returns a pattern for cache invalidation scoped to a tenant.
// For tenant 0 (unscoped): "entity:*"
// For non-zero tenants:    "XXXX:entity:*"
func CachePattern(tenantID uint16, entity string) string {
	if tenantID == 0 {
		return fmt.Sprintf("%s:*", entity)
	}
	return fmt.Sprintf("%04X:%s:*", tenantID, entity)
}

// CacheTenantPattern returns a pattern matching all keys for a tenant.
// For tenant 0: "*" (everything)
// For non-zero: "XXXX:*"
func CacheTenantPattern(tenantID uint16) string {
	if tenantID == 0 {
		return "*"
	}
	return fmt.Sprintf("%04X:*", tenantID)
}

// CacheListPattern returns a pattern matching only list cache keys for an
// entity type, scoped to a tenant. This leaves individual GET cache entries
// intact, improving cache hit rate when a single entity is modified.
// For tenant 0 (unscoped): "entity:list:*"
// For non-zero tenants:    "XXXX:entity:list:*"
func CacheListPattern(tenantID uint16, entity string) string {
	if tenantID == 0 {
		return fmt.Sprintf("%s:list:*", entity)
	}
	return fmt.Sprintf("%04X:%s:list:*", tenantID, entity)
}

// ---------------------------------------------------------------------------
// Persistence interface
// ---------------------------------------------------------------------------

// Persister stores and retrieves tenant name-to-ID mappings durably.
// Implementations must be safe for concurrent use.
type Persister interface {
	// LoadAll returns all persisted tenant mappings.
	LoadAll(ctx context.Context) (map[string]uint16, error)
	// Save persists a single tenant mapping. It must be idempotent:
	// saving an already-persisted (name, id) pair is not an error.
	Save(ctx context.Context, name string, id uint16) error
}

// ---------------------------------------------------------------------------
// Tenant registry
// ---------------------------------------------------------------------------

// Registry maps human-readable tenant names (e.g. "acme") to uint16 IDs.
// It is safe for concurrent use. When a Persister is attached, all mutations
// are durably stored, ensuring stable name-to-ID mappings across restarts.
type Registry struct {
	mu        sync.RWMutex
	byName    map[string]uint16
	byID      map[uint16]string
	nextAuto  uint16 // for auto-assignment; starts at 1
	persister Persister
}

// NewRegistry creates an empty tenant registry with no persistence.
// Mappings will be lost on restart. Use SetPersister or LoadFrom to
// attach durable storage.
func NewRegistry() *Registry {
	return &Registry{
		byName:   make(map[string]uint16),
		byID:     make(map[uint16]string),
		nextAuto: 1,
	}
}

// SetPersister attaches a persistence backend to the registry.
// Must be called before any Register/GetOrRegister calls.
func (r *Registry) SetPersister(p Persister) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.persister = p
}

// LoadFrom loads all tenant mappings from the attached persister into
// the in-memory registry. Existing in-memory mappings are preserved;
// conflicts (same name with different ID) return an error.
// This should be called once at startup, after SetPersister.
func (r *Registry) LoadFrom(ctx context.Context) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.persister == nil {
		return nil // no persister, nothing to load
	}

	mappings, err := r.persister.LoadAll(ctx)
	if err != nil {
		return fmt.Errorf("load tenant registry: %w", err)
	}

	for name, id := range mappings {
		if id == 0 {
			continue // skip reserved ID
		}
		if existing, ok := r.byName[name]; ok && existing != id {
			return fmt.Errorf("tenant %q: persisted ID %d conflicts with in-memory ID %d", name, id, existing)
		}
		if existing, ok := r.byID[id]; ok && existing != name {
			return fmt.Errorf("tenant ID %d: persisted name %q conflicts with in-memory name %q", id, name, existing)
		}
		r.byName[name] = id
		r.byID[id] = name
		if id >= r.nextAuto && id < 65535 {
			r.nextAuto = id + 1
		}
	}

	return nil
}

// Register adds a tenant with an explicit ID.
// Returns an error if the name or ID is already registered.
// If a persister is attached, the mapping is durably stored.
func (r *Registry) Register(ctx context.Context, name string, id uint16) error {
	if id == 0 {
		return fmt.Errorf("tenant ID 0 is reserved for unscoped operation")
	}
	if name == "" {
		return fmt.Errorf("tenant name must not be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	// Guard against exceeding the usable ID range (1..65535).
	if len(r.byName) >= 65535 {
		return fmt.Errorf("tenant registry full: maximum 65535 tenants reached")
	}

	if existing, ok := r.byName[name]; ok {
		if existing == id {
			return nil // idempotent: same mapping already exists
		}
		return fmt.Errorf("tenant name %q already registered with ID %d", name, existing)
	}
	if existing, ok := r.byID[id]; ok {
		return fmt.Errorf("tenant ID %d already registered as %q", id, existing)
	}

	// Persist before committing to memory
	if r.persister != nil {
		if err := r.persister.Save(ctx, name, id); err != nil {
			return fmt.Errorf("persist tenant %q (ID %d): %w", name, id, err)
		}
	}

	r.byName[name] = id
	r.byID[id] = name

	// Keep nextAuto above the highest registered ID, but cap at 65535
	// to avoid uint16 wrap-around.
	if id >= r.nextAuto && id < 65535 {
		r.nextAuto = id + 1
	}

	return nil
}

// Lookup returns the tenant ID for a name, or 0 and false if not found.
func (r *Registry) Lookup(name string) (uint16, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	id, ok := r.byName[name]
	return id, ok
}

// GetOrRegister returns the tenant ID for a name, auto-registering with
// the next available ID if the name is not yet known. This is intended for
// non-strict tenant modes where tenants are created on first access.
// If a persister is attached, new mappings are durably stored.
func (r *Registry) GetOrRegister(ctx context.Context, name string) (uint16, error) {
	// Fast path: read lock
	r.mu.RLock()
	if id, ok := r.byName[name]; ok {
		r.mu.RUnlock()
		return id, nil
	}
	r.mu.RUnlock()

	// Slow path: write lock, double-check, register
	r.mu.Lock()
	defer r.mu.Unlock()

	if id, ok := r.byName[name]; ok {
		return id, nil
	}

	if name == "" {
		return 0, fmt.Errorf("tenant name must not be empty")
	}

	// Guard against uint16 overflow. ID 0 is reserved, so the usable
	// range is 1..65535 — at most 65535 tenants.
	if len(r.byName) >= 65535 {
		return 0, fmt.Errorf("tenant registry full: maximum 65535 tenants reached")
	}

	id := r.nextAuto
	if id == 0 {
		id = 1 // skip reserved 0
	}
	// Skip past IDs that were explicitly registered via Register().
	// This prevents collisions when auto-assign and explicit-assign coexist.
	for {
		if _, taken := r.byID[id]; !taken {
			break
		}
		if id == 65535 {
			return 0, fmt.Errorf("tenant registry full: no available auto-assign IDs")
		}
		id++
	}

	// Persist before committing to memory
	if r.persister != nil {
		if err := r.persister.Save(ctx, name, id); err != nil {
			return 0, fmt.Errorf("persist tenant %q (ID %d): %w", name, id, err)
		}
	}

	r.byName[name] = id
	r.byID[id] = name
	if id < 65535 {
		r.nextAuto = id + 1
	}

	return id, nil
}

// Name returns the tenant name for an ID, or "" and false if not found.
func (r *Registry) Name(id uint16) (string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	name, ok := r.byID[id]
	return name, ok
}

// List returns all registered tenant name-ID pairs.
func (r *Registry) List() map[string]uint16 {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]uint16, len(r.byName))
	for k, v := range r.byName {
		result[k] = v
	}
	return result
}

// Count returns the number of registered tenants.
func (r *Registry) Count() int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return len(r.byName)
}
