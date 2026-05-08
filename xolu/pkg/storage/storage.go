// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"errors"
)

var (
	// ErrNotFound is returned when an entity is not found
	ErrNotFound = errors.New("entity not found")
	// ErrAlreadyExists is returned when an entity already exists
	ErrAlreadyExists = errors.New("entity already exists")
	// ErrInvalidEntity is returned when entity name is invalid
	ErrInvalidEntity = errors.New("invalid entity name")
	// ErrInvalidID is returned when ID is invalid
	ErrInvalidID = errors.New("invalid ID")
	// ErrConflict is returned when an optimistic concurrency check fails
	ErrConflict = errors.New("version conflict")
	// ErrNotSupported is returned when a storage backend does not implement
	// a particular operation. Handlers that receive this error should map it
	// to an appropriate HTTP 501 Not Implemented response.
	ErrNotSupported = errors.New("operation not supported by this storage backend")
)

// StoreConfig is the canonical configuration for all store backends.
// A store is constructed with a StoreConfig and scoped to that config
// for its entire lifetime. TenantID 0 means no tenant scoping.
type StoreConfig struct {
	Type            string // "sqlite", "jsonfile"
	DBPath          string // SQLite database file path
	BaseDir         string // JSONFile base directory
	Schema          string // JSONFile schema subdirectory
	FullTextEnabled bool   // controls FTS indexing in backend
	GraphEnabled    bool   // controls graph edge table maintenance
	TenantID        uint16 // 0 = no tenant scoping

	// Performance tuning (SQLite-specific; zero = use defaults)
	SQLiteCacheSize           int // Page cache size in KB
	SQLiteBusyTimeout         int // Milliseconds to wait on locked database
	SQLiteMaxOpenConns        int // Max open database connections
	SQLiteMaxIdleConns        int // Max idle database connections
	SQLiteReadPoolSize        int // Max open read connections (0 = auto)
	SQLiteContentionThreshold int // Adaptive lock threshold 0-100
}

// Store defines the core interface for entity storage backends
type Store interface {
	// Config returns the store's configuration.
	Config() StoreConfig

	// Entity CRUD operations
	Create(ctx context.Context, entity string, data map[string]interface{}) (int, error)
	Get(ctx context.Context, entity string, id int) (map[string]interface{}, error)
	Update(ctx context.Context, entity string, id int, data map[string]interface{}) error
	Patch(ctx context.Context, entity string, id int, data map[string]interface{}) error
	// PatchValidated is like Patch but runs a validation function against the
	// merged data inside the transaction. If the validator returns an error,
	// the transaction is rolled back and the error is returned to the caller.
	// This avoids TOCTOU races where a Get-merge-Update sequence can observe
	// stale data between the Get and the Update.
	PatchValidated(ctx context.Context, entity string, id int, data map[string]interface{}, validate func(merged map[string]interface{}) error) error
	Delete(ctx context.Context, entity string, id int) error
	// Save upserts an entity with the caller-specified ID: creates it if it
	// does not exist, overwrites it if it does. Returns (true, nil) when a
	// new record was created and (false, nil) when an existing record was
	// replaced. Never returns an error solely because the ID already exists.
	Save(ctx context.Context, entity string, id int, data map[string]interface{}) (bool, error)

	// Commit performs an atomic upsert + one or more inserts in a single
	// storage transaction. The upsert (req.Update) supports optional
	// optimistic concurrency via Version. Each entry in req.Append is an
	// unconditional insert; a duplicate explicit ID returns ErrAlreadyExists
	// and rolls back the entire commit. Returns ErrConflict when the Update
	// version check fails.
	Commit(ctx context.Context, req CommitRequest) (CommitResult, error)
	
	// Query operations
	List(ctx context.Context, entity string) ([]map[string]interface{}, error)
	Exists(ctx context.Context, entity string, id int) bool
	Search(ctx context.Context, entity string, field string, query string, matchType string) ([]map[string]interface{}, error)
	
	// Full-text search (optional - may return empty if not supported)
	FullTextSearch(ctx context.Context, query string, entity string) ([]map[string]interface{}, error)
	
	// Ping verifies that the storage backend is reachable. Returns nil on
	// success. Used by health and readiness probes.
	Ping(ctx context.Context) error

	// Lifecycle
	Close() error
}

// CommitRequest is the payload for the atomic commit endpoint.
// It performs one conditional upsert (Update) and one or more unconditional
// inserts (Append) in a single storage transaction.
type CommitRequest struct {
	Update CommitUpdate   `json:"update"`
	Append []CommitAppend `json:"append"`
}

// CommitUpdate describes the entity to upsert in a Commit operation.
// If Version is non-nil, the write is conditional: it proceeds only if the
// stored _version equals *Version. A mismatch returns ErrConflict.
type CommitUpdate struct {
	Entity  string                 `json:"entity"`
	ID      int                    `json:"id"`
	Version *int                   `json:"version,omitempty"`
	Data    map[string]interface{} `json:"data"`
}

// CommitAppend describes one record to insert in a Commit operation.
// If ID is nil, the backend auto-generates an ID. If ID is non-nil and a
// record with that ID already exists in the entity type, ErrAlreadyExists
// is returned and the entire commit is rolled back.
type CommitAppend struct {
	Entity string                 `json:"entity"`
	ID     *int                   `json:"id,omitempty"`
	Data   map[string]interface{} `json:"data"`
}

// CommitResult is returned on a successful Commit.
type CommitResult struct {
	Update   CommitUpdateResult   `json:"update"`
	Appended []CommitAppendResult `json:"appended"`
}

// CommitUpdateResult describes the outcome of the upsert in a Commit.
// Created is true when a new record was inserted; false when an existing
// record was overwritten. Version is the _version value after the commit.
type CommitUpdateResult struct {
	Entity  string `json:"entity"`
	ID      int    `json:"id"`
	Created bool   `json:"created"`
	Version int    `json:"version"`
}

// CommitAppendResult describes one inserted record in a Commit response.
// ID is always set; for auto-generated IDs it contains the assigned value.
type CommitAppendResult struct {
	Entity string `json:"entity"`
	ID     int    `json:"id"`
}

// IDGenerator defines interface for ID generation strategies
type IDGenerator interface {
	NextID(ctx context.Context, entity string) (int, error)
}

// Migrator defines optional schema migration support
// Useful for database backends
type Migrator interface {
	Migrate(ctx context.Context) error
	Version(ctx context.Context) (int, error)
}

// Searcher defines optional search capabilities
type Searcher interface {
	Search(ctx context.Context, entity string, field string, query string, matchType string) ([]map[string]interface{}, error)
}

// Batcher defines optional batch operation support
type Batcher interface {
	BatchCreate(ctx context.Context, entity string, items []map[string]interface{}) ([]int, error)
	BatchDelete(ctx context.Context, entity string, ids []int) error
}

// GraphNeighbors defines optional graph neighbor queries
type GraphNeighbors interface {

}

// GraphIntegrity defines optional graph integrity checking
type GraphIntegrity interface {
	VerifyGraphIntegrity(ctx context.Context) error
	RebuildGraph(ctx context.Context) error
}

// StoreInfo provides metadata about the store implementation
type StoreInfo struct {
	Type                string // "jsonfile", "sqlite", "postgres", etc.
	Version             string
	SupportsSearch      bool
	SupportsBatch       bool
	SupportsTransaction bool
}

// InfoProvider allows stores to provide metadata about their capabilities
type InfoProvider interface {
	Info() StoreInfo
}

// EntityLister defines optional entity type listing support
type EntityLister interface {
	ListEntities(ctx context.Context) ([]string, error)
}

// PagedResult holds a page of results plus the total count.
type PagedResult struct {
	Data       []map[string]interface{}
	TotalItems int
}

// PagedLister is an optional interface for storage backends that support
// server-side pagination. Backends that implement this avoid loading every
// record into memory for paginated list requests.
type PagedLister interface {
	// ListPaged returns a single page of entities, plus the total count.
	// limit and offset are applied at the storage layer (SQL LIMIT/OFFSET).
	ListPaged(ctx context.Context, entity string, limit, offset int) (*PagedResult, error)
}

// GraphEdge holds the five columns of one row from the graph edges table.
type GraphEdge struct {
	SourceEntity string
	SourceID     int
	TargetEntity string
	TargetID     int
	Relationship string
}

// TenantIDLister is an optional interface for storage backends that can
// enumerate all tenant IDs for which a graph_tXXXX edge table exists. The
// returned slice must always include tenant 0 (the implicit default). Used
// during startup graph hydration to restore graph state for all tenants.
//
// Backends that do not implement this interface fall back to scanning only
// tenant 0 via a direct ScanGraphEdges call.
type TenantIDLister interface {
	GraphTenantIDs(ctx context.Context) ([]uint16, error)
}

// GraphEdgeScanner is an optional interface for storage backends that can
// stream graph edges directly from their edge table without deserialising full
// entity JSON. Implementing this interface enables O(edges) startup graph
// hydration instead of O(entities × JSON size).
//
// ScanGraphEdges calls fn once per edge row. Iteration stops on the first
// non-nil error returned by fn. A nil error from ScanGraphEdges means all
// rows were scanned (or fn stopped iteration early with a sentinel — callers
// must define their own sentinel if needed).
//
// tenantID scopes the scan to a specific tenant's edge table. Pass 0 for the
// default (tenant-0) table. Future SQL backends may extend this to scan all
// tenant tables in a single call; the current SQLite implementation scans one
// tenant at a time, matching the existing startup scope.
type GraphEdgeScanner interface {
	ScanGraphEdges(ctx context.Context, tenantID uint16, fn func(GraphEdge) error) error
}
