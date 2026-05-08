// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/ha1tch/xolu/pkg/models"
	"github.com/ha1tch/xolu/pkg/tenant"
	_ "modernc.org/sqlite" // Pure Go SQLite driver
)

// SQLiteStore implements Store interface using SQLite database.
//
// It maintains two connection pools against the same WAL-mode database:
//   - db (writer): MaxOpenConns=1, serialises all writes.
//   - readDB (reader): MaxOpenConns=NumCPU, query_only=ON, parallel reads.
//
// Under WAL mode, readers never block the writer and vice-versa.
type SQLiteStore struct {
	db          *sql.DB // writer pool (1 conn, serialised)
	readDB      *sql.DB // reader pool (N conns, parallel, query_only)
	dbPath      string
	config      SQLiteConfig
	storeConfig StoreConfig
	alock       *AdaptiveLock
	adapted     *AdaptedRegistry // nil-safe: Get() returns nil for unknown entities
	dialect     StorageDialect   // backend-specific SQL generation
	stmtCache   *StmtCache       // prepared statement cache for reader pool
	logger      zerolog.Logger   // structured logger; zerolog.Nop() by default
}

// DB returns the underlying *sql.DB for advanced operations such as
// batch seeding or direct SQL execution. Use with care — callers must
// respect the store's locking and schema conventions.
func (s *SQLiteStore) DB() *sql.DB {
	return s.db
}

// ReaderDB returns the underlying reader connection pool.
// Used by the tenant persister for read-only queries.
func (s *SQLiteStore) ReaderDB() *sql.DB {
	return s.readDB
}

// ContentiontLock returns the store's adaptive lock, allowing runtime
// configuration of the contention threshold via SetThreshold().
func (s *SQLiteStore) ContentionLock() *AdaptiveLock {
	return s.alock
}

// Config returns the store's StoreConfig.
func (s *SQLiteStore) Config() StoreConfig {
	return s.storeConfig
}

// AdaptedRegistry returns the store's adapted table registry.
// Returns nil only if the store was not properly initialized.
func (s *SQLiteStore) AdaptedRegistry() *AdaptedRegistry {
	return s.adapted
}

// WithLogger attaches a zerolog.Logger to the store. Returns the store so it
// can be chained: store := NewSQLiteStore(...).WithLogger(logger).
// Until this is called the store uses zerolog.Nop() and logs nothing.
func (s *SQLiteStore) WithLogger(logger zerolog.Logger) *SQLiteStore {
	s.logger = logger
	return s
}

// RegisterAdaptedEntity derives an adapted table for the given entity type
// from its JSON Schema and creates the table if it doesn't exist.
// This is called by the server layer when a schema is loaded or registered.
func (s *SQLiteStore) RegisterAdaptedEntity(ctx context.Context, entity string, schema map[string]interface{}) error {
	return RegisterAdaptedTable(ctx, s.db, s.adapted, entity, schema, s.dialect)
}

// SQLiteConfig holds SQLite-specific configuration
type SQLiteConfig struct {
	DBPath            string
	EnableWAL         bool   // Write-Ahead Logging for better concurrency
	EnableForeignKeys bool
	CacheSize         int    // Page cache size in KB
	BusyTimeout       int    // Milliseconds to wait on locked database
	FullTextEnabled   bool   // Enable FTS5 full-text search indexing
	GraphEnabled      bool   // Enable graph edge table maintenance
	TenantID          uint16 // 0 = no tenant scoping

	// Performance tuning (zero = use backend defaults)
	//   SQLite defaults: MaxOpenConns=1 (WAL single-writer),
	//   MaxIdleConns=1, ReadPoolSize=NumCPU.
	MaxOpenConns        int // Max open write connections (0 = backend default)
	MaxIdleConns        int // Max idle write connections (0 = backend default)
	ReadPoolSize        int // Max open read connections (0 = backend default)
	ContentionThreshold int // Adaptive lock threshold 0-100 (default 95)
}

// sqliteBusyRetries is the number of times to retry an operation that fails
// with SQLITE_BUSY after the busy_timeout has already been exhausted.
// Each retry uses an exponential backoff starting at 25ms.
const sqliteBusyRetries = 7

// isSQLiteBusy returns true if the error is a SQLITE_BUSY error.
func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "SQLITE_BUSY") ||
		strings.Contains(err.Error(), "database is locked")
}

// withRetry executes fn, using the adaptive lock for serialisation under
// contention and retrying on SQLITE_BUSY with exponential backoff.
func (s *SQLiteStore) withRetry(fn func() error) error {
	if locked := s.alock.Lock(); locked {
		defer s.alock.Unlock()
	}
	err := fn()
	if err == nil {
		s.alock.RecordSuccess()
		return nil
	}
	if !isSQLiteBusy(err) {
		return err
	}
	s.alock.RecordFailure()
	backoff := 25 * time.Millisecond
	for attempt := 0; attempt < sqliteBusyRetries; attempt++ {
		jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
		time.Sleep(backoff + jitter)
		backoff *= 2
		err = fn()
		if err == nil {
			s.alock.RecordSuccess()
			return nil
		}
		if !isSQLiteBusy(err) {
			return err
		}
		s.alock.RecordFailure()
	}
	return err
}


// withRetryRead executes a read operation, using the adaptive lock's RLock
// when engaged. Reads don't retry — SQLITE_BUSY on reads is extremely rare
// with WAL mode, and when the lock is engaged reads are already protected.
func (s *SQLiteStore) withRetryRead(fn func() (map[string]interface{}, error)) (map[string]interface{}, error) {
	if locked := s.alock.RLock(); locked {
		defer s.alock.RUnlock()
	}
	val, err := fn()
	if err == nil {
		s.alock.RecordSuccess()
		return val, nil
	}
	if !isSQLiteBusy(err) {
		return val, err
	}
	// Read hit SQLITE_BUSY — record and retry once
	s.alock.RecordFailure()
	time.Sleep(25 * time.Millisecond)
	val, err = fn()
	if err == nil {
		s.alock.RecordSuccess()
	} else if isSQLiteBusy(err) {
		s.alock.RecordFailure()
	}
	return val, err
}

// withRetryCreateVal is like withRetry but for Create which returns (int, error).
func (s *SQLiteStore) withRetryCreateVal(fn func() (int, error)) (int, error) {
	if locked := s.alock.Lock(); locked {
		defer s.alock.Unlock()
	}
	val, err := fn()
	if err == nil {
		s.alock.RecordSuccess()
		return val, nil
	}
	if !isSQLiteBusy(err) {
		return val, err
	}
	s.alock.RecordFailure()
	backoff := 25 * time.Millisecond
	for attempt := 0; attempt < sqliteBusyRetries; attempt++ {
		jitter := time.Duration(rand.Int63n(int64(backoff) / 2))
		time.Sleep(backoff + jitter)
		backoff *= 2
		val, err = fn()
		if err == nil {
			s.alock.RecordSuccess()
			return val, nil
		}
		if !isSQLiteBusy(err) {
			return val, err
		}
		s.alock.RecordFailure()
	}
	return val, err
}

// NewSQLiteStore creates a new SQLite-based storage with separate reader and
// writer connection pools. Under WAL mode the writer never blocks readers and
// vice-versa, so splitting pools maximises concurrency.
func NewSQLiteStore(dbPath string, config SQLiteConfig) (*SQLiteStore, error) {
	if dbPath == "" {
		dbPath = "olu.db"
	}
	
	// Base DSN with pragmas inherited by every connection in both pools.
	baseDSN := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)&_pragma=cache_size(-%d)&_pragma=busy_timeout(%d)",
		dbPath, config.CacheSize, config.BusyTimeout)

	// --- Writer pool (single connection, serialised) ---
	db, err := sql.Open("sqlite", baseDSN)
	if err != nil {
		return nil, fmt.Errorf("failed to open writer database: %w", err)
	}
	// SQLite WAL allows exactly one writer at a time. Limiting the pool to
	// 1 connection means Go-side serialisation matches the database constraint
	// and avoids pointless SQLITE_BUSY retries between our own connections.
	maxOpen := config.MaxOpenConns
	if maxOpen <= 0 {
		maxOpen = 1
	}
	maxIdle := config.MaxIdleConns
	if maxIdle <= 0 {
		maxIdle = 1
	}
	db.SetMaxOpenConns(maxOpen)
	db.SetMaxIdleConns(maxIdle)

	// --- Reader pool (N connections, parallel) ---
	// query_only=ON prevents accidental writes through the reader pool.
	readDSN := baseDSN + "&_pragma=query_only(ON)"
	readDB, err := sql.Open("sqlite", readDSN)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to open reader database: %w", err)
	}
	readPoolSize := config.ReadPoolSize
	if readPoolSize <= 0 {
		readPoolSize = runtime.NumCPU()
		if readPoolSize < 2 {
			readPoolSize = 2
		}
	}
	readDB.SetMaxOpenConns(readPoolSize)
	readDB.SetMaxIdleConns(readPoolSize)

	contentionThreshold := config.ContentionThreshold
	if contentionThreshold == 0 {
		contentionThreshold = 95 // default when unset
	}

	store := &SQLiteStore{
		db:     db,
		readDB: readDB,
		dbPath: dbPath,
		config: config,
		storeConfig: StoreConfig{
			Type:                      "sqlite",
			DBPath:                    dbPath,
			FullTextEnabled:           config.FullTextEnabled,
			GraphEnabled:              config.GraphEnabled,
			TenantID:                  config.TenantID,
			SQLiteCacheSize:           config.CacheSize,
			SQLiteBusyTimeout:         config.BusyTimeout,
			SQLiteMaxOpenConns:        maxOpen,
			SQLiteMaxIdleConns:        maxIdle,
			SQLiteContentionThreshold: contentionThreshold,
		},
		alock:     NewAdaptiveLock(contentionThreshold),
		adapted:   NewAdaptedRegistry(),
		dialect:   &SQLiteStorageDialect{},
		stmtCache: NewStmtCache(readDB, 0), // default size; prepares against reader pool
		logger:    zerolog.Nop(),           // silent until WithLogger is called
	}
	
	// Initialize database schema
	initCtx, initCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer initCancel()
	if err := store.initialize(initCtx); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to initialize database: %w", err)
	}
	
	// Load adapted table registry from metadata
	loadCtx, loadCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer loadCancel()
	adapted, err := LoadAdaptedRegistry(loadCtx, db)
	if err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to load adapted table registry: %w", err)
	}
	store.adapted = adapted
	
	return store, nil
}

// initialize creates the necessary tables and triggers
func (s *SQLiteStore) initialize(ctx context.Context) error {
	// Apply pragmas for performance and consistency
	pragmas := []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
		fmt.Sprintf("PRAGMA cache_size = -%d", s.config.CacheSize),
		fmt.Sprintf("PRAGMA busy_timeout = %d", s.config.BusyTimeout),
	}
	
	for _, pragma := range pragmas {
		if _, err := s.db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("failed to set pragma: %w", err)
		}
	}
	
	// Create schema
	schema := `
		-- Main entities table (JSON blob approach)
		CREATE TABLE IF NOT EXISTS entities (
			tenant_id INTEGER NOT NULL DEFAULT 0,
			entity_type TEXT NOT NULL,
			id INTEGER NOT NULL,
			data TEXT NOT NULL, -- JSON stored as TEXT
			_version INTEGER NOT NULL DEFAULT 1,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (tenant_id, entity_type, id)
		);
		
		CREATE INDEX IF NOT EXISTS idx_entity_type ON entities(entity_type);
		CREATE INDEX IF NOT EXISTS idx_updated_at ON entities(updated_at);
		CREATE INDEX IF NOT EXISTS idx_tenant_entity ON entities(tenant_id, entity_type);
		
		-- Tenant-scoped ID sequences
		CREATE TABLE IF NOT EXISTS entity_sequences (
			tenant_id INTEGER NOT NULL DEFAULT 0,
			entity_type TEXT NOT NULL,
			next_id INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (tenant_id, entity_type)
		);
		
		-- Schema metadata table (optional schema storage)
		CREATE TABLE IF NOT EXISTS schemas (
			entity_type TEXT PRIMARY KEY,
			schema TEXT NOT NULL, -- JSON schema
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		
		-- Version tracking for migrations
		CREATE TABLE IF NOT EXISTS schema_version (
			version INTEGER PRIMARY KEY,
			applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);

		-- Tenant registry: stable name-to-ID mapping across restarts
		CREATE TABLE IF NOT EXISTS tenants (
			id INTEGER NOT NULL PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
		);
		
		-- Full-text search virtual table (FTS5) with tenant_id
		CREATE VIRTUAL TABLE IF NOT EXISTS entities_fts USING fts5(
			tenant_id UNINDEXED,
			entity_type UNINDEXED,
			entity_id UNINDEXED,
			content
		);
	`
	
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("failed to create schema: %w", err)
	}
	
	// Create per-tenant graph edge table when graph is enabled.
	// All tenants, including tenant 0, get their own graph_tXXXX table.
	if s.config.GraphEnabled {
		table := tenant.GraphEdgesTableName(s.config.TenantID)
		tenantGraphSchema := fmt.Sprintf(`
			CREATE TABLE IF NOT EXISTS %s (
				source_entity TEXT NOT NULL,
				source_id INTEGER NOT NULL,
				target_entity TEXT NOT NULL,
				target_id INTEGER NOT NULL,
				relationship_name TEXT NOT NULL,
				created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
				PRIMARY KEY (source_entity, source_id, target_entity, target_id, relationship_name)
			);
			CREATE INDEX IF NOT EXISTS idx_%s_source ON %s(source_entity, source_id);
			CREATE INDEX IF NOT EXISTS idx_%s_target ON %s(target_entity, target_id);
			CREATE INDEX IF NOT EXISTS idx_%s_rel    ON %s(relationship_name);
		`, table, table, table, table, table, table, table)
		if _, err := s.db.ExecContext(ctx, tenantGraphSchema); err != nil {
			return fmt.Errorf("failed to create tenant graph table %s: %w", table, err)
		}
	}
	
	// Create triggers for automatic graph synchronization
	if err := s.createGraphTriggers(ctx); err != nil {
		return fmt.Errorf("failed to create triggers: %w", err)
	}
	
	// Mark current schema version
	if _, err := s.db.ExecContext(ctx, 
		"INSERT OR IGNORE INTO schema_version (version) VALUES (2)"); err != nil {
		return fmt.Errorf("failed to set schema version: %w", err)
	}
	
	// Migration v3: add _version column for optimistic concurrency
	var hasV3 int
	_ = s.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM schema_version WHERE version = 3").Scan(&hasV3)
	if hasV3 == 0 {
		// ALTER TABLE ADD COLUMN is idempotent-safe: if the column already
		// exists (new database), the error is harmless and we just skip it.
		_, alterErr := s.db.ExecContext(ctx,
			"ALTER TABLE entities ADD COLUMN _version INTEGER NOT NULL DEFAULT 1")
		if alterErr != nil {
			// Column may already exist from the CREATE TABLE — that's fine
			errMsg := alterErr.Error()
			if !strings.Contains(errMsg, "duplicate column") {
				return fmt.Errorf("failed to add _version column: %w", alterErr)
			}
		}
		if _, err := s.db.ExecContext(ctx,
			"INSERT OR IGNORE INTO schema_version (version) VALUES (3)"); err != nil {
			return fmt.Errorf("failed to set schema version 3: %w", err)
		}
	}

	return nil
}

// createGraphTriggers creates triggers to automatically sync the tenant graph table with REF fields in JSON
func (s *SQLiteStore) createGraphTriggers(ctx context.Context) error {
	// NOTE: Graph synchronization strategy
	// =====================================
	// We use MANUAL graph synchronization instead of triggers for the following reasons:
	//
	// 1. Reliability: json_each() in triggers can cause "malformed JSON" errors in some
	//    SQLite builds, particularly with the pure-Go modernc.org/sqlite driver.
	//
	// 2. Integrity is maintained through transactions:
	//    - All CRUD operations (Create/Update/Patch/Delete/Save) use transactions
	//    - Graph sync happens within the SAME transaction as the document operation
	//    - If either operation fails, the entire transaction rolls back
	//    - This provides ACID guarantees equivalent to triggers
	//
	// 3. Explicit control: Manual sync makes the graph update logic visible and debuggable,
	//    and allows for easier testing and modification.
	//
	// The syncGraphEdges() method is called within every transaction that modifies documents,
	// ensuring document-graph consistency is always maintained atomically.
	
	return nil
}

// Info returns store information
func (s *SQLiteStore) Info() StoreInfo {
	return StoreInfo{
		Type:                "sqlite",
		Version:             "1.0.0",
		SupportsSearch:      true,
		SupportsBatch:       true,
		SupportsTransaction: true,
	}
}

// Create inserts a new entity with auto-generated ID
func (s *SQLiteStore) Create(ctx context.Context, entity string, data map[string]interface{}) (int, error) {
	return s.withRetryCreateVal(func() (int, error) {
		return s.createInner(ctx, entity, data)
	})
}

func (s *SQLiteStore) createInner(ctx context.Context, entity string, data map[string]interface{}) (int, error) {
	
	tid := int(s.config.TenantID)
	
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	
	// Get next ID (tenant-scoped sequence)
	var nextID int
	err = tx.QueryRowContext(ctx, `
		INSERT INTO entity_sequences (tenant_id, entity_type, next_id) 
		VALUES (?, ?, 1)
		ON CONFLICT(tenant_id, entity_type) DO UPDATE SET next_id = next_id + 1
		RETURNING next_id
	`, tid, entity).Scan(&nextID)
	if err != nil {
		return 0, fmt.Errorf("failed to get next ID: %w", err)
	}
	
	// Create a copy of data to avoid mutating input
	dataCopy := make(map[string]interface{}, len(data)+1)
	for k, v := range data {
		dataCopy[k] = v
	}
	dataCopy["id"] = nextID
	
	// Insert entity: adapted table or blob
	if spec := s.adapted.Get(entity); spec != nil {
		if err := adaptedCreate(ctx, tx, spec, s.dialect, tid, nextID, dataCopy); err != nil {
			return 0, err
		}
	} else {
		// Marshal to JSON (only needed for blob storage)
		jsonData, err := json.Marshal(dataCopy)
		if err != nil {
			return 0, fmt.Errorf("failed to marshal data: %w", err)
		}
		_, err = tx.ExecContext(ctx, `
			INSERT INTO entities (tenant_id, entity_type, id, data) 
			VALUES (?, ?, ?, ?)
		`, tid, entity, nextID, string(jsonData))
		if err != nil {
			return 0, fmt.Errorf("failed to insert entity: %w", err)
		}
	}
	
	// Manually sync graph edges
	if err := s.syncGraphEdges(ctx, tx, entity, nextID, dataCopy); err != nil {
		return 0, fmt.Errorf("failed to sync graph: %w", err)
	}
	
	// Index for full-text search
	if err := s.indexForFTS(ctx, tx, entity, nextID, dataCopy); err != nil {
		return 0, fmt.Errorf("failed to index for FTS: %w", err)
	}
	
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}
	
	return nextID, nil
}

// syncGraphEdges extracts REF fields and creates graph edges.
//
// All REFs for the entity are collected first, then inserted via a single
// prepared statement — one ExecContext per edge rather than one
// fmt.Sprintf + ExecContext. When an entity has no REF fields the INSERT
// is skipped entirely.
func (s *SQLiteStore) syncGraphEdges(ctx context.Context, tx *sql.Tx, sourceEntity string, sourceID int, data map[string]interface{}) error {
	if !s.config.GraphEnabled {
		return nil
	}

	table := tenant.GraphEdgesTableName(s.config.TenantID)

	// Validate and collect edges before touching the database. Failing here
	// avoids issuing a DELETE that would then be rolled back on extraction error.
	rawEdges, err := models.ExtractEntityEdges(data)
	if err != nil {
		return fmt.Errorf("syncGraphEdges: %w", err)
	}

	// Delete old edges from this entity (always a single statement).
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(
		`DELETE FROM %s WHERE source_entity = ? AND source_id = ?`, table),
		sourceEntity, sourceID,
	); err != nil {
		return err
	}
	var edges []rebuildEdge
	for _, ee := range rawEdges {
		edges = append(edges, rebuildEdge{
			sourceEntity: sourceEntity,
			sourceID:     sourceID,
			targetEntity: ee.TargetEntity,
			targetID:     int64(ee.TargetID),
			relationship: ee.Relationship,
		})
	}

	// Nothing to insert — skip the prepare round-trip.
	if len(edges) == 0 {
		return nil
	}

	// Prepare once, execute once per edge.
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(
		`INSERT INTO %s (source_entity, source_id, target_entity, target_id, relationship_name)
		 VALUES (?, ?, ?, ?, ?)`, table))
	if err != nil {
		return fmt.Errorf("syncGraphEdges: prepare: %w", err)
	}
	defer stmt.Close()

	for _, e := range edges {
		if _, err := stmt.ExecContext(ctx, e.sourceEntity, e.sourceID, e.targetEntity, e.targetID, e.relationship); err != nil {
			return fmt.Errorf("syncGraphEdges: insert edge: %w", err)
		}
	}

	return nil
}

// Get retrieves an entity by ID
func (s *SQLiteStore) Get(ctx context.Context, entity string, id int) (map[string]interface{}, error) {
	return s.withRetryRead(func() (map[string]interface{}, error) {
		return s.getInner(ctx, entity, id)
	})
}

func (s *SQLiteStore) getInner(ctx context.Context, entity string, id int) (map[string]interface{}, error) {
	
	// Adapted table path
	if spec := s.adapted.Get(entity); spec != nil {
		return adaptedGet(ctx, s.readDB, spec, s.dialect, int(s.config.TenantID), id)
	}

	var jsonData string
	var version int
	err := s.readDB.QueryRowContext(ctx, `
		SELECT data, _version FROM entities 
		WHERE tenant_id = ? AND entity_type = ? AND id = ?
	`, int(s.config.TenantID), entity, id).Scan(&jsonData, &version)
	
	if err == sql.ErrNoRows {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query entity: %w", err)
	}
	
	var result map[string]interface{}
	if err := json.Unmarshal([]byte(jsonData), &result); err != nil {
		return nil, fmt.Errorf("failed to unmarshal data: %w", err)
	}
	
	result["_version"] = version
	return result, nil
}

// Update replaces an entity completely
func (s *SQLiteStore) Update(ctx context.Context, entity string, id int, data map[string]interface{}) error {
	return s.withRetry(func() error {
		return s.updateInner(ctx, entity, id, data)
	})
}

func (s *SQLiteStore) updateInner(ctx context.Context, entity string, id int, data map[string]interface{}) error {
	
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	
	// Extract _version for optimistic concurrency (opt-in: if absent, no check)
	var expectVersion int
	var hasVersion bool
	if v, ok := data["_version"]; ok {
		hasVersion = true
		switch tv := v.(type) {
		case float64:
			expectVersion = int(tv)
		case int:
			expectVersion = tv
		}
	}
	
	// Create a copy to avoid mutating input; strip _version from the JSON blob
	dataCopy := make(map[string]interface{}, len(data)+1)
	for k, v := range data {
		if k == "_version" {
			continue
		}
		dataCopy[k] = v
	}
	dataCopy["id"] = id
	
	// Update entity: adapted table or blob
	if spec := s.adapted.Get(entity); spec != nil {
		if err := adaptedUpdate(ctx, tx, spec, s.dialect, int(s.config.TenantID), id, dataCopy, expectVersion, hasVersion); err != nil {
			return err
		}
	} else {
		// Marshal to JSON (only needed for blob storage)
		jsonData, err := json.Marshal(dataCopy)
		if err != nil {
			return fmt.Errorf("failed to marshal data: %w", err)
		}
		// Update entity with optional version check
		var result sql.Result
		if hasVersion {
			result, err = tx.ExecContext(ctx, `
				UPDATE entities 
				SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP 
				WHERE tenant_id = ? AND entity_type = ? AND id = ? AND _version = ?
			`, string(jsonData), int(s.config.TenantID), entity, id, expectVersion)
		} else {
			result, err = tx.ExecContext(ctx, `
				UPDATE entities 
				SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP 
				WHERE tenant_id = ? AND entity_type = ? AND id = ?
			`, string(jsonData), int(s.config.TenantID), entity, id)
		}
		if err != nil {
			return fmt.Errorf("failed to update entity: %w", err)
		}
		
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			// Distinguish "not found" from "version mismatch"
			if hasVersion {
				var exists int
				_ = tx.QueryRowContext(ctx, `
					SELECT 1 FROM entities 
					WHERE tenant_id = ? AND entity_type = ? AND id = ?
				`, int(s.config.TenantID), entity, id).Scan(&exists)
				if exists == 1 {
					return ErrConflict
				}
			}
			return ErrNotFound
		}
	}
	
	// Manually sync graph edges
	if err := s.syncGraphEdges(ctx, tx, entity, id, dataCopy); err != nil {
		return fmt.Errorf("failed to sync graph: %w", err)
	}
	
	// Update FTS index
	if err := s.indexForFTS(ctx, tx, entity, id, dataCopy); err != nil {
		return fmt.Errorf("failed to update FTS index: %w", err)
	}
	
	return tx.Commit()
}

// Patch partially updates an entity
func (s *SQLiteStore) Patch(ctx context.Context, entity string, id int, updates map[string]interface{}) error {
	return s.withRetry(func() error {
		return s.patchInner(ctx, entity, id, updates, nil)
	})
}

// PatchValidated applies a partial update inside a transaction and runs
// the validator against the merged data before committing.
func (s *SQLiteStore) PatchValidated(ctx context.Context, entity string, id int, updates map[string]interface{}, validate func(merged map[string]interface{}) error) error {
	return s.withRetry(func() error {
		return s.patchInner(ctx, entity, id, updates, validate)
	})
}

func (s *SQLiteStore) patchInner(ctx context.Context, entity string, id int, updates map[string]interface{}, validate func(merged map[string]interface{}) error) error {
	
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	
	// Extract _version for optimistic concurrency (opt-in: if absent, no check)
	var expectVersion int
	var hasVersion bool
	if v, ok := updates["_version"]; ok {
		hasVersion = true
		switch tv := v.(type) {
		case float64:
			expectVersion = int(tv)
		case int:
			expectVersion = tv
		}
	}
	
	tid := int(s.config.TenantID)
	spec := s.adapted.Get(entity)

	// Get existing data (adapted or blob path)
	var existing map[string]interface{}
	if spec != nil {
		var currentVersion int
		existing, currentVersion, err = adaptedGetInTx(ctx, tx, spec, s.dialect, tid, id)
		if err != nil {
			return err
		}
		// Version check for adapted path
		if hasVersion && currentVersion != expectVersion {
			return ErrConflict
		}
	} else {
		var jsonData string
		err = tx.QueryRowContext(ctx, `
			SELECT data FROM entities 
			WHERE tenant_id = ? AND entity_type = ? AND id = ?
		`, tid, entity, id).Scan(&jsonData)
		
		if err == sql.ErrNoRows {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("failed to query entity: %w", err)
		}
		
		if err := json.Unmarshal([]byte(jsonData), &existing); err != nil {
			return fmt.Errorf("failed to unmarshal data: %w", err)
		}
	}
	
	// Merge updates into existing data; skip _version (it's metadata, not document content).
	// nil values are stored as-is (JSON null). The handler is responsible for
	// removing keys from the patch map when PatchNullBehavior is "delete".
	for key, value := range updates {
		if key == "id" || key == "_version" {
			continue
		}
		existing[key] = value
	}
	
	// Ensure ID is set
	existing["id"] = id
	
	// Run validation against the merged data (inside the transaction)
	if validate != nil {
		if err := validate(existing); err != nil {
			return err
		}
	}

	// Strip _version from the data (it lives in the column, not the document)
	delete(existing, "_version")
	
	// Write back: adapted or blob path
	if spec != nil {
		// For adapted path, use adaptedUpdate with version already checked above
		if err := adaptedUpdate(ctx, tx, spec, s.dialect, tid, id, existing, 0, false); err != nil {
			return err
		}
	} else {
		// Marshal back to JSON
		updatedJSON, err := json.Marshal(existing)
		if err != nil {
			return fmt.Errorf("failed to marshal data: %w", err)
		}
		
		// Update with optional version check
		var result sql.Result
		if hasVersion {
			result, err = tx.ExecContext(ctx, `
				UPDATE entities 
				SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP 
				WHERE tenant_id = ? AND entity_type = ? AND id = ? AND _version = ?
			`, string(updatedJSON), tid, entity, id, expectVersion)
		} else {
			result, err = tx.ExecContext(ctx, `
				UPDATE entities 
				SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP 
				WHERE tenant_id = ? AND entity_type = ? AND id = ?
			`, string(updatedJSON), tid, entity, id)
		}
		if err != nil {
			return fmt.Errorf("failed to update entity: %w", err)
		}
		
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			if hasVersion {
				// Entity exists but version didn't match
				return ErrConflict
			}
			return ErrNotFound
		}
	}
	
	// Manually sync graph edges
	if err := s.syncGraphEdges(ctx, tx, entity, id, existing); err != nil {
		return fmt.Errorf("failed to sync graph: %w", err)
	}
	
	// Update FTS index
	if err := s.indexForFTS(ctx, tx, entity, id, existing); err != nil {
		return fmt.Errorf("failed to update FTS index: %w", err)
	}
	
	return tx.Commit()
}

// Delete removes an entity
func (s *SQLiteStore) Delete(ctx context.Context, entity string, id int) error {
	return s.withRetry(func() error {
		return s.deleteInner(ctx, entity, id)
	})
}

func (s *SQLiteStore) deleteInner(ctx context.Context, entity string, id int) error {
	
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	
	// Delete entity: adapted table or blob
	if spec := s.adapted.Get(entity); spec != nil {
		if err := adaptedDelete(ctx, tx, spec, s.dialect, int(s.config.TenantID), id); err != nil {
			return err
		}
	} else {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM entities 
			WHERE tenant_id = ? AND entity_type = ? AND id = ?
		`, int(s.config.TenantID), entity, id)
		if err != nil {
			return fmt.Errorf("failed to delete entity: %w", err)
		}
		
		rows, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return ErrNotFound
		}
	}
	
	// Clean up graph edges
	if s.config.GraphEnabled {
		edgeTable := tenant.GraphEdgesTableName(s.config.TenantID)
		_, err = tx.ExecContext(ctx, fmt.Sprintf(`
			DELETE FROM %s 
			WHERE (source_entity = ? AND source_id = ?)
			   OR (target_entity = ? AND target_id = ?)
		`, edgeTable), entity, id, entity, id)
		if err != nil {
			return fmt.Errorf("failed to delete graph edges: %w", err)
		}
	}
	
	// Remove from FTS index
	_, err = tx.ExecContext(ctx, `
		DELETE FROM entities_fts WHERE tenant_id = ? AND entity_type = ? AND entity_id = ?
	`, fmt.Sprintf("%d", int(s.config.TenantID)), entity, fmt.Sprintf("%d", id))
	if err != nil {
		return fmt.Errorf("failed to delete from FTS index: %w", err)
	}
	
	return tx.Commit()
}

// Save creates an entity with a specific ID (fails if exists)
func (s *SQLiteStore) Save(ctx context.Context, entity string, id int, data map[string]interface{}) (bool, error) {
	var created bool
	err := s.withRetry(func() error {
		var innerErr error
		created, innerErr = s.saveInner(ctx, entity, id, data)
		return innerErr
	})
	return created, err
}

func (s *SQLiteStore) saveInner(ctx context.Context, entity string, id int, data map[string]interface{}) (bool, error) {

	// Extract _version for optional optimistic concurrency check.
	// If present in the request, the overwrite path becomes a conditional write.
	var expectVersion int
	var hasVersion bool
	if v, ok := data["_version"]; ok {
		hasVersion = true
		switch tv := v.(type) {
		case float64:
			expectVersion = int(tv)
		case int:
			expectVersion = tv
		}
	}

	// Create a copy to avoid mutating input; strip _version (column, not document content).
	dataCopy := make(map[string]interface{}, len(data)+1)
	for k, v := range data {
		if k == "_version" {
			continue
		}
		dataCopy[k] = v
	}
	dataCopy["id"] = id

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()

	// Check existence inside the transaction to prevent TOCTOU races.
	tid := int(s.config.TenantID)
	spec := s.adapted.Get(entity)

	var exists bool
	if spec != nil {
		err = tx.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT EXISTS(SELECT 1 FROM %s WHERE tenant_id = ? AND id = ?)",
			spec.TableName()), tid, id).Scan(&exists)
	} else {
		err = tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM entities WHERE tenant_id = ? AND entity_type = ? AND id = ?)
		`, tid, entity, id).Scan(&exists)
	}
	if err != nil {
		return false, fmt.Errorf("failed to check existence: %w", err)
	}

	if exists {
		// Overwrite path: conditional or unconditional update in place.
		if spec != nil {
			if err := adaptedUpdate(ctx, tx, spec, s.dialect, tid, id, dataCopy, expectVersion, hasVersion); err != nil {
				return false, err
			}
		} else {
			jsonData, err := json.Marshal(dataCopy)
			if err != nil {
				return false, fmt.Errorf("failed to marshal data: %w", err)
			}
			var result sql.Result
			if hasVersion {
				result, err = tx.ExecContext(ctx, `
					UPDATE entities
					SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP
					WHERE tenant_id = ? AND entity_type = ? AND id = ? AND _version = ?
				`, string(jsonData), tid, entity, id, expectVersion)
			} else {
				result, err = tx.ExecContext(ctx, `
					UPDATE entities
					SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP
					WHERE tenant_id = ? AND entity_type = ? AND id = ?
				`, string(jsonData), tid, entity, id)
			}
			if err != nil {
				return false, fmt.Errorf("failed to overwrite entity: %w", err)
			}
			if hasVersion {
				rows, err := result.RowsAffected()
				if err != nil {
					return false, err
				}
				if rows == 0 {
					return false, ErrConflict
				}
			}
		}
	} else {
		// Create path: insert new record.

		// Update sequence so future auto-IDs stay above this one.
		_, err = tx.ExecContext(ctx, `
			INSERT INTO entity_sequences (tenant_id, entity_type, next_id)
			VALUES (?, ?, ?)
			ON CONFLICT(tenant_id, entity_type) DO UPDATE
			SET next_id = MAX(next_id, excluded.next_id + 1)
		`, tid, entity, id+1)
		if err != nil {
			return false, fmt.Errorf("failed to update sequence: %w", err)
		}

		if spec != nil {
			if err := adaptedCreate(ctx, tx, spec, s.dialect, tid, id, dataCopy); err != nil {
				return false, err
			}
		} else {
			jsonData, err := json.Marshal(dataCopy)
			if err != nil {
				return false, fmt.Errorf("failed to marshal data: %w", err)
			}
			_, err = tx.ExecContext(ctx, `
				INSERT INTO entities (tenant_id, entity_type, id, data)
				VALUES (?, ?, ?, ?)
			`, tid, entity, id, string(jsonData))
			if err != nil {
				return false, fmt.Errorf("failed to save entity: %w", err)
			}
		}
	}

	// Sync graph edges and FTS index regardless of create/update.
	if err := s.syncGraphEdges(ctx, tx, entity, id, dataCopy); err != nil {
		return false, fmt.Errorf("failed to sync graph: %w", err)
	}
	if !exists {
		// FTS indexing on create — update path already covered by updateInner convention.
		if err := s.indexForFTS(ctx, tx, entity, id, dataCopy); err != nil {
			return false, fmt.Errorf("failed to update FTS index: %w", err)
		}
	}

	return !exists, tx.Commit()
}

// Commit performs an atomic upsert + one or more inserts in a single
// SQLite transaction. The upsert supports optional CAS via Update.Version.
// All operations share one BEGIN/COMMIT boundary; any failure rolls back
// the entire set.
func (s *SQLiteStore) Commit(ctx context.Context, req CommitRequest) (CommitResult, error) {
	var result CommitResult
	// withRetry is intentional here. It retries only on SQLITE_BUSY, which
	// means another writer held the WAL write lock. On a BUSY error the
	// transaction was never committed, so retrying commitInner from scratch
	// is safe. CAS semantics are preserved across retries: if a concurrent
	// writer advances the version between two retry attempts, the subsequent
	// attempt reads the new version and returns ErrConflict (not BUSY), which
	// exits the retry loop immediately and propagates 409 to the caller.
	// A retry cannot silently double-write or mask a conflict.
	err := s.withRetry(func() error {
		var innerErr error
		result, innerErr = s.commitInner(ctx, req)
		return innerErr
	})
	return result, err
}

func (s *SQLiteStore) commitInner(ctx context.Context, req CommitRequest) (CommitResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return CommitResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	updateResult, err := s.saveInTx(ctx, tx, req.Update)
	if err != nil {
		return CommitResult{}, err
	}

	appended := make([]CommitAppendResult, 0, len(req.Append))
	for _, a := range req.Append {
		id, err := s.createInTx(ctx, tx, a)
		if err != nil {
			return CommitResult{}, err
		}
		appended = append(appended, CommitAppendResult{Entity: a.Entity, ID: id})
	}

	if err := tx.Commit(); err != nil {
		return CommitResult{}, fmt.Errorf("commit transaction failed: %w", err)
	}

	return CommitResult{Update: updateResult, Appended: appended}, nil
}

// saveInTx performs the upsert half of a Commit inside an existing transaction.
// It mirrors saveInner but accepts a caller-owned *sql.Tx and returns the
// resulting _version rather than committing.
func (s *SQLiteStore) saveInTx(ctx context.Context, tx *sql.Tx, u CommitUpdate) (CommitUpdateResult, error) {
	tid := int(s.config.TenantID)

	dataCopy := make(map[string]interface{}, len(u.Data)+1)
	for k, v := range u.Data {
		if k == "_version" {
			continue
		}
		dataCopy[k] = v
	}
	dataCopy["id"] = u.ID

	spec := s.adapted.Get(u.Entity)

	var exists bool
	var existsErr error
	if spec != nil {
		existsErr = tx.QueryRowContext(ctx, fmt.Sprintf(
			"SELECT EXISTS(SELECT 1 FROM %s WHERE tenant_id = ? AND id = ?)",
			spec.TableName()), tid, u.ID).Scan(&exists)
	} else {
		existsErr = tx.QueryRowContext(ctx, `
			SELECT EXISTS(SELECT 1 FROM entities WHERE tenant_id = ? AND entity_type = ? AND id = ?)
		`, tid, u.Entity, u.ID).Scan(&exists)
	}
	if existsErr != nil {
		return CommitUpdateResult{}, fmt.Errorf("saveInTx: existence check: %w", existsErr)
	}

	var newVersion int
	var created bool

	if exists {
		// Overwrite path — conditional or unconditional.
		if spec != nil {
			var expectVersion int
			var hasVersion bool
			if u.Version != nil {
				hasVersion = true
				expectVersion = *u.Version
			}
			if err := adaptedUpdate(ctx, tx, spec, s.dialect, tid, u.ID, dataCopy, expectVersion, hasVersion); err != nil {
				return CommitUpdateResult{}, err
			}
			// Retrieve the new version from the adapted table column.
			if err := tx.QueryRowContext(ctx, fmt.Sprintf(
				"SELECT _version FROM %s WHERE tenant_id = ? AND id = ?",
				spec.TableName()), tid, u.ID).Scan(&newVersion); err != nil {
				return CommitUpdateResult{}, fmt.Errorf("saveInTx: read adapted version: %w", err)
			}
		} else {
			jsonData, err := json.Marshal(dataCopy)
			if err != nil {
				return CommitUpdateResult{}, fmt.Errorf("saveInTx: marshal: %w", err)
			}
			if u.Version != nil {
				err = tx.QueryRowContext(ctx, `
					UPDATE entities
					SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP
					WHERE tenant_id = ? AND entity_type = ? AND id = ? AND _version = ?
					RETURNING _version
				`, string(jsonData), tid, u.Entity, u.ID, *u.Version).Scan(&newVersion)
			} else {
				err = tx.QueryRowContext(ctx, `
					UPDATE entities
					SET data = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP
					WHERE tenant_id = ? AND entity_type = ? AND id = ?
					RETURNING _version
				`, string(jsonData), tid, u.Entity, u.ID).Scan(&newVersion)
			}
			if err == sql.ErrNoRows && u.Version != nil {
				return CommitUpdateResult{}, ErrConflict
			}
			if err != nil {
				return CommitUpdateResult{}, fmt.Errorf("saveInTx: update: %w", err)
			}
		}
		created = false
	} else {
		// Create path — update sequence, then insert.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_sequences (tenant_id, entity_type, next_id)
			VALUES (?, ?, ?)
			ON CONFLICT(tenant_id, entity_type) DO UPDATE
			SET next_id = MAX(next_id, excluded.next_id + 1)
		`, tid, u.Entity, u.ID+1); err != nil {
			return CommitUpdateResult{}, fmt.Errorf("saveInTx: sequence: %w", err)
		}
		if spec != nil {
			if err := adaptedCreate(ctx, tx, spec, s.dialect, tid, u.ID, dataCopy); err != nil {
				return CommitUpdateResult{}, err
			}
		} else {
			jsonData, err := json.Marshal(dataCopy)
			if err != nil {
				return CommitUpdateResult{}, fmt.Errorf("saveInTx: marshal: %w", err)
			}
			if _, err := tx.ExecContext(ctx, `
				INSERT INTO entities (tenant_id, entity_type, id, data) VALUES (?, ?, ?, ?)
			`, tid, u.Entity, u.ID, string(jsonData)); err != nil {
				return CommitUpdateResult{}, fmt.Errorf("saveInTx: insert: %w", err)
			}
		}
		newVersion = 1
		created = true
	}

	if err := s.syncGraphEdges(ctx, tx, u.Entity, u.ID, dataCopy); err != nil {
		return CommitUpdateResult{}, fmt.Errorf("saveInTx: graph: %w", err)
	}
	if !exists {
		if err := s.indexForFTS(ctx, tx, u.Entity, u.ID, dataCopy); err != nil {
			return CommitUpdateResult{}, fmt.Errorf("saveInTx: fts: %w", err)
		}
	}

	return CommitUpdateResult{Entity: u.Entity, ID: u.ID, Created: created, Version: newVersion}, nil
}

// createInTx performs one append insert inside an existing transaction.
// If a.ID is nil, an ID is generated from the tenant sequence. If a.ID is
// set and that ID already exists, ErrAlreadyExists is returned.
func (s *SQLiteStore) createInTx(ctx context.Context, tx *sql.Tx, a CommitAppend) (int, error) {
	tid := int(s.config.TenantID)
	spec := s.adapted.Get(a.Entity)

	dataCopy := make(map[string]interface{}, len(a.Data)+1)
	for k, v := range a.Data {
		if k == "_version" {
			continue
		}
		dataCopy[k] = v
	}

	var id int
	if a.ID == nil {
		// Auto-generate via sequence.
		if err := tx.QueryRowContext(ctx, `
			INSERT INTO entity_sequences (tenant_id, entity_type, next_id)
			VALUES (?, ?, 1)
			ON CONFLICT(tenant_id, entity_type) DO UPDATE SET next_id = next_id + 1
			RETURNING next_id
		`, tid, a.Entity).Scan(&id); err != nil {
			return 0, fmt.Errorf("createInTx: sequence: %w", err)
		}
	} else {
		id = *a.ID
		// Keep sequence ahead of explicit IDs.
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entity_sequences (tenant_id, entity_type, next_id)
			VALUES (?, ?, ?)
			ON CONFLICT(tenant_id, entity_type) DO UPDATE
			SET next_id = MAX(next_id, excluded.next_id + 1)
		`, tid, a.Entity, id+1); err != nil {
			return 0, fmt.Errorf("createInTx: sequence bump: %w", err)
		}
	}

	dataCopy["id"] = id

	if spec != nil {
		if err := adaptedCreate(ctx, tx, spec, s.dialect, tid, id, dataCopy); err != nil {
			return 0, err
		}
	} else {
		jsonData, err := json.Marshal(dataCopy)
		if err != nil {
			return 0, fmt.Errorf("createInTx: marshal: %w", err)
		}
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO entities (tenant_id, entity_type, id, data) VALUES (?, ?, ?, ?)
		`, tid, a.Entity, id, string(jsonData)); err != nil {
			if strings.Contains(err.Error(), "UNIQUE constraint failed") {
				return 0, ErrAlreadyExists
			}
			return 0, fmt.Errorf("createInTx: insert: %w", err)
		}
	}

	if err := s.syncGraphEdges(ctx, tx, a.Entity, id, dataCopy); err != nil {
		return 0, fmt.Errorf("createInTx: graph: %w", err)
	}
	if err := s.indexForFTS(ctx, tx, a.Entity, id, dataCopy); err != nil {
		return 0, fmt.Errorf("createInTx: fts: %w", err)
	}

	return id, nil
}

// List returns all entities of a given type
func (s *SQLiteStore) List(ctx context.Context, entity string) ([]map[string]interface{}, error) {
	
	// Adapted table path
	if spec := s.adapted.Get(entity); spec != nil {
		return adaptedList(ctx, s.readDB, spec, s.dialect, int(s.config.TenantID))
	}

	rows, err := s.readDB.QueryContext(ctx, `
		SELECT data, _version FROM entities 
		WHERE tenant_id = ? AND entity_type = ?
		ORDER BY id
	`, int(s.config.TenantID), entity)
	if err != nil {
		return nil, fmt.Errorf("failed to list entities: %w", err)
	}
	defer rows.Close()
	
	var results []map[string]interface{}
	for rows.Next() {
		var jsonData string
		var version int
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}
		
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal data: %w", err)
		}
		
		data["_version"] = version
		results = append(results, data)
	}
	
	return results, rows.Err()
}

// Exists checks if an entity exists
func (s *SQLiteStore) Exists(ctx context.Context, entity string, id int) bool {

	// Adapted table path
	if spec := s.adapted.Get(entity); spec != nil {
		return adaptedExists(ctx, s.readDB, spec, s.dialect, int(s.config.TenantID), id)
	}

	var exists bool
	err := s.readDB.QueryRowContext(ctx, `
		SELECT EXISTS(SELECT 1 FROM entities WHERE tenant_id = ? AND entity_type = ? AND id = ?)
	`, int(s.config.TenantID), entity, id).Scan(&exists)

	return err == nil && exists
}

// Ping verifies that the database connection is alive.
func (s *SQLiteStore) Ping(ctx context.Context) error {
	var one int
	return s.readDB.QueryRowContext(ctx, "SELECT 1").Scan(&one)
}

// Close closes the database connection
func (s *SQLiteStore) Close() error {
	if s.alock != nil {
		s.alock.Stop()
	}
	// Close cached prepared statements before the pools they reference.
	if s.stmtCache != nil {
		s.stmtCache.Close()
	}
	// Close reader pool first (drains in-flight queries), then writer.
	if s.readDB != nil {
		s.readDB.Close()
	}
	return s.db.Close()
}

// Search implements field-based search using JSON extraction
func (s *SQLiteStore) Search(ctx context.Context, entity string, field string, query string, matchType string) ([]map[string]interface{}, error) {
	
	var sqlQuery string
	var args []interface{}
	
	switch matchType {
	case "exact":
		sqlQuery = `
			SELECT data, _version FROM entities 
			WHERE tenant_id = ? AND entity_type = ? 
			  AND json_extract(data, '$.' || ?) = ?
			ORDER BY id
		`
		args = []interface{}{int(s.config.TenantID), entity, field, query}
		
	case "contains":
		sqlQuery = `
			SELECT data, _version FROM entities 
			WHERE tenant_id = ? AND entity_type = ? 
			  AND json_extract(data, '$.' || ?) LIKE ?
			ORDER BY id
		`
		args = []interface{}{int(s.config.TenantID), entity, field, "%" + query + "%"}
		
	case "starts":
		sqlQuery = `
			SELECT data, _version FROM entities 
			WHERE tenant_id = ? AND entity_type = ? 
			  AND json_extract(data, '$.' || ?) LIKE ?
			ORDER BY id
		`
		args = []interface{}{int(s.config.TenantID), entity, field, query + "%"}
		
	case "ends":
		sqlQuery = `
			SELECT data, _version FROM entities 
			WHERE tenant_id = ? AND entity_type = ? 
			  AND json_extract(data, '$.' || ?) LIKE ?
			ORDER BY id
		`
		args = []interface{}{int(s.config.TenantID), entity, field, "%" + query}
		
	default:
		return nil, fmt.Errorf("invalid match type: %s", matchType)
	}
	
	rows, err := s.readDB.QueryContext(ctx, sqlQuery, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to search: %w", err)
	}
	defer rows.Close()
	
	var results []map[string]interface{}
	for rows.Next() {
		var jsonData string
		var version int64
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, err
		}
		
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			return nil, err
		}
		
		data["_version"] = version
		results = append(results, data)
	}
	
	return results, rows.Err()
}

// VerifyGraphIntegrity checks whether the tenant edge table matches the REF
// fields in stored entity JSON. Returns a joined error listing every
// discrepancy found (missing edges + unexpected edges); does not stop at the
// first violation.
//
// Both reads (entities and edge table) are issued inside a single read
// transaction so that concurrent writes cannot produce false violations.
//
// Memory: only one map is materialised (expected edges derived from entity
// JSON). Actual edges from the edge table are streamed and checked against
// that map rather than accumulated into a second map.
func (s *SQLiteStore) VerifyGraphIntegrity(ctx context.Context) error {
	tx, err := s.readDB.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("VerifyGraphIntegrity: begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Phase 1: build expected edge set from entity JSON.
	expectedEdges := make(map[string]bool)

	entityRows, err := tx.QueryContext(ctx,
		"SELECT entity_type, id, data FROM entities WHERE tenant_id = ?",
		int(s.config.TenantID))
	if err != nil {
		return fmt.Errorf("VerifyGraphIntegrity: query entities: %w", err)
	}
	defer entityRows.Close()

	for entityRows.Next() {
		var entity string
		var id int
		var jsonData string
		if err := entityRows.Scan(&entity, &id, &jsonData); err != nil {
			return fmt.Errorf("VerifyGraphIntegrity: scan entity: %w", err)
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			// Unparseable entity: skip — not a graph integrity violation.
			continue
		}
		ees, err := models.ExtractEntityEdges(data)
		if err != nil {
			// Duplicate REF target in stored JSON: this IS a data integrity
			// issue but it belongs in the ExtractEntityEdges error, not here.
			// Collect it as a violation rather than aborting.
			expectedEdges[fmt.Sprintf("CORRUPT[%s:%d]: %v", entity, id, err)] = false
			continue
		}
		for _, ee := range ees {
			key := fmt.Sprintf("%s:%d:%s:%d:%s",
				entity, id, ee.TargetEntity, ee.TargetID, ee.Relationship)
			expectedEdges[key] = false // false = not yet seen in actual edges
		}
	}
	if err := entityRows.Err(); err != nil {
		return fmt.Errorf("VerifyGraphIntegrity: iterate entities: %w", err)
	}

	// Phase 2: stream the actual edge table; mark expected edges as seen and
	// collect any edges that have no expected counterpart.
	edgeTable := tenant.GraphEdgesTableName(s.config.TenantID)
	edgeRows, err := tx.QueryContext(ctx,
		fmt.Sprintf("SELECT source_entity, source_id, target_entity, target_id, relationship_name FROM %s", edgeTable))
	if err != nil {
		return fmt.Errorf("VerifyGraphIntegrity: query edge table: %w", err)
	}
	defer edgeRows.Close()

	var violations []string
	for edgeRows.Next() {
		var source, target, rel string
		var sourceID, targetID int
		if err := edgeRows.Scan(&source, &sourceID, &target, &targetID, &rel); err != nil {
			return fmt.Errorf("VerifyGraphIntegrity: scan edge: %w", err)
		}
		key := fmt.Sprintf("%s:%d:%s:%d:%s", source, sourceID, target, targetID, rel)
		if _, expected := expectedEdges[key]; expected {
			expectedEdges[key] = true // mark as seen
		} else {
			violations = append(violations, fmt.Sprintf("unexpected edge: %s", key))
		}
	}
	if err := edgeRows.Err(); err != nil {
		return fmt.Errorf("VerifyGraphIntegrity: iterate edge table: %w", err)
	}

	// Phase 3: any expected edge not marked as seen is missing from the table.
	for key, seen := range expectedEdges {
		if !seen {
			violations = append(violations, fmt.Sprintf("missing edge: %s", key))
		}
	}

	if len(violations) == 0 {
		return nil
	}

	// Sort for deterministic output, then join.
	sortStrings(violations)
	return fmt.Errorf("graph integrity: %d violation(s):\n%s",
		len(violations), strings.Join(violations, "\n"))
}

// sortStrings sorts a string slice in place. Defined here to avoid importing
// sort just for this one use; inlined so the compiler can inline if needed.
func sortStrings(ss []string) { sort.Strings(ss) }

// rebuildBatchSize is the number of edges flushed per INSERT during RebuildGraph.
// SQLite's variable-binding limit is 32766; at 5 columns per row the ceiling is
// ~6500 rows. 500 keeps memory bounded while staying well under that limit.
const rebuildBatchSize = 500


// GraphTenantIDs implements TenantIDLister. It returns all tenant IDs for which
// a graph_tXXXX edge table should be hydrated at startup. Tenant 0 is always
// included first (it is implicit and never appears in the tenants registry
// table). Registered non-zero tenants follow in ascending order.
func (s *SQLiteStore) GraphTenantIDs(ctx context.Context) ([]uint16, error) {
	// Tenant 0 is the implicit default; it is never inserted into the
	// tenants table, so we always prepend it manually.
	ids := []uint16{0}

	rows, err := s.readDB.QueryContext(ctx, "SELECT id FROM tenants WHERE id > 0 ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("GraphTenantIDs: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("GraphTenantIDs: scan: %w", err)
		}
		if id > 0 && id <= 65535 {
			ids = append(ids, uint16(id))
		}
	}
	return ids, rows.Err()
}

// ScanGraphEdges implements GraphEdgeScanner. It streams every row from the
// tenant-scoped graph_tXXXX edge table, calling fn once per row. Iteration stops
// on the first non-nil error returned by fn. Rows are read via the reader pool
// (query_only, parallel-safe). All tenants, including tenant 0, use graph_tXXXX.
func (s *SQLiteStore) ScanGraphEdges(ctx context.Context, tenantID uint16, fn func(GraphEdge) error) error {
	table := tenant.GraphEdgesTableName(tenantID)
	rows, err := s.readDB.QueryContext(ctx,
		"SELECT source_entity, source_id, target_entity, target_id, relationship_name FROM "+table)
	if err != nil {
		return fmt.Errorf("ScanGraphEdges: query %s: %w", table, err)
	}
	defer rows.Close()

	for rows.Next() {
		var e GraphEdge
		if err := rows.Scan(&e.SourceEntity, &e.SourceID, &e.TargetEntity, &e.TargetID, &e.Relationship); err != nil {
			return fmt.Errorf("ScanGraphEdges: scan: %w", err)
		}
		if err := fn(e); err != nil {
			return err
		}
	}
	return rows.Err()
}

// rebuildEdge holds the five columns of one graph edge row collected during rebuild.
type rebuildEdge struct {
	sourceEntity string
	sourceID     int
	targetEntity string
	targetID     int64
	relationship string
}

// RebuildGraph rebuilds the tenant edge table from stored entity JSON.
//
// Correctness: uses models.ExtractEntityEdges for REF extraction so that @REFS
// ([]interface{} of REF maps) and TSREF exclusion are handled identically to
// the live syncGraphEdges path.
//
// Performance: one PrepareContext call outside the row loop; edges are
// accumulated and flushed in batches of rebuildBatchSize rather than one
// ExecContext per edge.
func (s *SQLiteStore) RebuildGraph(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	rebuildTable := tenant.GraphEdgesTableName(s.config.TenantID)

	// Clear existing edges for this tenant.
	if _, err := tx.ExecContext(ctx, fmt.Sprintf("DELETE FROM %s", rebuildTable)); err != nil {
		return err
	}

	// Prepare a single-row INSERT statement reused for every flush.
	stmt, err := tx.PrepareContext(ctx, fmt.Sprintf(`
		INSERT INTO %s (source_entity, source_id, target_entity, target_id, relationship_name)
		VALUES (?, ?, ?, ?, ?)
	`, rebuildTable))
	if err != nil {
		return fmt.Errorf("RebuildGraph: prepare insert: %w", err)
	}
	defer stmt.Close()

	flushEdges := func(batch []rebuildEdge) error {
		for _, e := range batch {
			if _, err := stmt.ExecContext(ctx, e.sourceEntity, e.sourceID, e.targetEntity, e.targetID, e.relationship); err != nil {
				return fmt.Errorf("RebuildGraph: insert edge: %w", err)
			}
		}
		return nil
	}

	rows, err := tx.QueryContext(ctx,
		"SELECT entity_type, id, data FROM entities WHERE tenant_id = ?",
		int(s.config.TenantID))
	if err != nil {
		return err
	}
	defer rows.Close()

	var batch []rebuildEdge

	for rows.Next() {
		var entity string
		var id int
		var jsonData string

		if err := rows.Scan(&entity, &id, &jsonData); err != nil {
			return err
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			// Unparseable entity — skip rather than aborting the whole rebuild.
			continue
		}

		ees, edgeErr := models.ExtractEntityEdges(data)
		if edgeErr != nil {
			// Corrupt entity: duplicate REF targets in stored JSON. Skip the
			// entity rather than aborting the entire rebuild so that the rest
			// of the graph can be salvaged. The integrity checker will report
			// this entity as a violation.
			s.logger.Warn().
				Str("entity", entity).Int("id", id).
				Err(edgeErr).
				Msg("RebuildGraph: skipping entity with duplicate edge target")
			continue
		}
		for _, ee := range ees {
			batch = append(batch, rebuildEdge{
				sourceEntity: entity,
				sourceID:     id,
				targetEntity: ee.TargetEntity,
				targetID:     int64(ee.TargetID),
				relationship: ee.Relationship,
			})
			if len(batch) >= rebuildBatchSize {
				if err := flushEdges(batch); err != nil {
					return err
				}
				batch = batch[:0]
			}
		}
	}

	if err := rows.Err(); err != nil {
		return err
	}

	// Flush any remaining edges below the batch threshold.
	if len(batch) > 0 {
		if err := flushEdges(batch); err != nil {
			return err
		}
	}

	return tx.Commit()
}


// indexForFTS extracts text content from entity and indexes it for full-text search
func (s *SQLiteStore) indexForFTS(ctx context.Context, tx *sql.Tx, entity string, id int, data map[string]interface{}) error {
	// Skip if FTS is not enabled
	if !s.config.FullTextEnabled {
		return nil
	}

	// Convert id to string for FTS storage
	idStr := fmt.Sprintf("%d", id)

	// First, delete any existing FTS entry for this entity
	tidStr := fmt.Sprintf("%d", int(s.config.TenantID))
	_, err := tx.ExecContext(ctx, `
		DELETE FROM entities_fts WHERE tenant_id = ? AND entity_type = ? AND entity_id = ?
	`, tidStr, entity, idStr)
	if err != nil {
		return err
	}
	
	// Extract searchable text content from the entity
	content := extractTextContent(data)
	if content == "" {
		return nil // Nothing to index
	}
	
	// Insert into FTS index
	_, err = tx.ExecContext(ctx, `
		INSERT INTO entities_fts (tenant_id, entity_type, entity_id, content)
		VALUES (?, ?, ?, ?)
	`, tidStr, entity, idStr, content)
	
	return err
}

// extractTextContent recursively extracts all string values from a map
func extractTextContent(data map[string]interface{}) string {
	var parts []string
	
	for key, value := range data {
		// Skip internal fields
		if key == "id" || key == "created_at" || key == "updated_at" {
			continue
		}
		
		switch v := value.(type) {
		case string:
			if v != "" {
				parts = append(parts, v)
			}
		case map[string]interface{}:
			// Check if it is a REF (skip references)
			if _, isRef := v["type"]; !isRef || v["type"] != "REF" {
				if nested := extractTextContent(v); nested != "" {
					parts = append(parts, nested)
				}
			}
		case []interface{}:
			for _, item := range v {
				if str, ok := item.(string); ok && str != "" {
					parts = append(parts, str)
				} else if m, ok := item.(map[string]interface{}); ok {
					if nested := extractTextContent(m); nested != "" {
						parts = append(parts, nested)
					}
				}
			}
		}
	}
	
	return strings.Join(parts, " ")
}

// FullTextSearch performs a full-text search across entities
func (s *SQLiteStore) FullTextSearch(ctx context.Context, query string, entity string) ([]map[string]interface{}, error) {
	
	if query == "" {
		return []map[string]interface{}{}, nil
	}
	
	// Sanitise query for FTS5 MATCH syntax.
	// FTS5 treats quotes, dashes, semicolons, and other punctuation as
	// syntax operators. Unescaped, they cause parse errors that surface
	// as 500s. We strip everything that isn't alphanumeric, whitespace,
	// or underscore, then collapse runs of whitespace.
	sanitised := sanitiseFTSQuery(query)
	if sanitised == "" {
		// Query was entirely special characters — return empty results
		return []map[string]interface{}{}, nil
	}
	
	// Add prefix matching with * for partial word matches
	ftsQuery := sanitised + "*"
	
	var rows *sql.Rows
	var err error
	
	if entity != "" {
		// Search within specific entity type
		rows, err = s.readDB.QueryContext(ctx, `
			SELECT e.entity_type, e.id, e.data
			FROM entities_fts
			JOIN entities e ON e.tenant_id = ? AND entities_fts.entity_type = e.entity_type AND CAST(entities_fts.entity_id AS INTEGER) = e.id
			WHERE entities_fts.tenant_id = ? AND entities_fts.entity_type = ? AND entities_fts MATCH ?
			ORDER BY rank
			LIMIT 100
		`, int(s.config.TenantID), fmt.Sprintf("%d", int(s.config.TenantID)), entity, ftsQuery)
	} else {
		// Search across all entities
		rows, err = s.readDB.QueryContext(ctx, `
			SELECT e.entity_type, e.id, e.data
			FROM entities_fts
			JOIN entities e ON e.tenant_id = ? AND entities_fts.entity_type = e.entity_type AND CAST(entities_fts.entity_id AS INTEGER) = e.id
			WHERE entities_fts.tenant_id = ? AND entities_fts MATCH ?
			ORDER BY rank
			LIMIT 100
		`, int(s.config.TenantID), fmt.Sprintf("%d", int(s.config.TenantID)), ftsQuery)
	}
	
	if err != nil {
		return nil, fmt.Errorf("full-text search failed: %w", err)
	}
	defer rows.Close()
	
	var results []map[string]interface{}
	for rows.Next() {
		var entityType string
		var id int
		var jsonData string
		
		if err := rows.Scan(&entityType, &id, &jsonData); err != nil {
			return nil, err
		}
		
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			continue
		}
		
		// Add metadata
		data["_entity"] = entityType
		results = append(results, data)
	}
	
	if results == nil {
		results = []map[string]interface{}{}
	}
	
	return results, rows.Err()
}

// sanitiseFTSQuery strips FTS5 syntax characters from user input.
// FTS5 interprets quotes, colons, parentheses, dashes, asterisks, carets,
// and other punctuation as query operators. Passing them raw causes parse
// errors. We keep only alphanumeric characters, whitespace, and underscores,
// then collapse multiple spaces into one and trim.
func sanitiseFTSQuery(query string) string {
	var b strings.Builder
	b.Grow(len(query))
	prevSpace := false
	for _, r := range query {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
			prevSpace = false
		} else if r == ' ' || r == '\t' || r == '\n' {
			if !prevSpace && b.Len() > 0 {
				b.WriteByte(' ')
				prevSpace = true
			}
		}
		// All other characters (quotes, semicolons, dashes, etc.) are dropped
	}
	return strings.TrimSpace(b.String())
}

// ---------------------------------------------------------------------------
// Queryable interface — predicate push-down support for the OQL planner
// ---------------------------------------------------------------------------

// Capabilities reports that the SQLite backend can handle WHERE, ORDER BY,
// LIMIT, and COUNT natively via json_extract() push-down.
func (s *SQLiteStore) Capabilities() QueryCapabilities {
	return QueryCapabilities{
		Where:   true,
		OrderBy: true,
		Limit:   true,
		Count:   true,
	}
}

// CountEntities returns the number of records for an entity type without
// fetching the data. This is a single indexed COUNT(*) — typically <10µs.
func (s *SQLiteStore) CountEntities(ctx context.Context, entity string) (int, error) {

	// Adapted table path
	if spec := s.adapted.Get(entity); spec != nil {
		countSQL := fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE tenant_id = ?`, spec.TableName())
		stmt, err := s.stmtCache.Get(countSQL)
		if err != nil {
			return 0, fmt.Errorf("count adapted prepare: %w", err)
		}
		var count int
		err = stmt.QueryRowContext(ctx, int(s.config.TenantID)).Scan(&count)
		if err != nil {
			return 0, fmt.Errorf("count adapted entities: %w", err)
		}
		return count, nil
	}

	const countSQL = `SELECT COUNT(*) FROM entities WHERE tenant_id = ? AND entity_type = ?`
	stmt, err := s.stmtCache.Get(countSQL)
	if err != nil {
		return 0, fmt.Errorf("count prepare: %w", err)
	}
	var count int
	err = stmt.QueryRowContext(ctx, int(s.config.TenantID), entity).Scan(&count)
	if err != nil {
		return 0, fmt.Errorf("count entities: %w", err)
	}
	return count, nil
}

// QueryWithPlan executes a pre-built SQL query (generated by the OQL planner)
// and returns the results as maps, in the same format as List().
func (s *SQLiteStore) QueryWithPlan(ctx context.Context, sqlQuery string, args []interface{}) ([]map[string]interface{}, error) {

	stmt, err := s.stmtCache.Get(sqlQuery)
	if err != nil {
		return nil, fmt.Errorf("push-down prepare failed: %w", err)
	}

	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("push-down query failed: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var jsonData string
		var version int64
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, fmt.Errorf("scan push-down row: %w", err)
		}

		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			return nil, fmt.Errorf("unmarshal push-down data: %w", err)
		}

		data["_version"] = version
		results = append(results, data)
	}

	return results, rows.Err()
}

// Compile-time interface check
var _ Queryable = (*SQLiteStore)(nil)

// Compile-time check: SQLiteStore implements PagedLister
var _ PagedLister = (*SQLiteStore)(nil)

// ListPaged returns a single page of entities plus total count, using
// SQL LIMIT/OFFSET so only the requested page is deserialised.
func (s *SQLiteStore) ListPaged(ctx context.Context, entity string, limit, offset int) (*PagedResult, error) {

	// Adapted table path: delegate to adaptedList and paginate in Go.
	// The adapted table doesn't store data in the entities table, so the
	// blob-based SQL below would return zero rows.
	if spec := s.adapted.Get(entity); spec != nil {
		all, err := adaptedList(ctx, s.readDB, spec, s.dialect, int(s.config.TenantID))
		if err != nil {
			return nil, err
		}
		total := len(all)
		if offset >= total {
			return &PagedResult{Data: []map[string]interface{}{}, TotalItems: total}, nil
		}
		end := offset + limit
		if end > total {
			end = total
		}
		return &PagedResult{Data: all[offset:end], TotalItems: total}, nil
	}

	// Count total
	var total int
	err := s.readDB.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entities WHERE tenant_id = ? AND entity_type = ?`,
		int(s.config.TenantID), entity,
	).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("count entities: %w", err)
	}

	if total == 0 {
		return &PagedResult{Data: []map[string]interface{}{}, TotalItems: 0}, nil
	}

	// Fetch page
	rows, err := s.readDB.QueryContext(ctx,
		`SELECT data, _version FROM entities WHERE tenant_id = ? AND entity_type = ? ORDER BY id LIMIT ? OFFSET ?`,
		int(s.config.TenantID), entity, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list paged: %w", err)
	}
	defer rows.Close()

	var results []map[string]interface{}
	for rows.Next() {
		var jsonData string
		var version int64
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, fmt.Errorf("scan paged row: %w", err)
		}
		var data map[string]interface{}
		if err := json.Unmarshal([]byte(jsonData), &data); err != nil {
			return nil, fmt.Errorf("unmarshal paged data: %w", err)
		}
		data["_version"] = version
		results = append(results, data)
	}
	if results == nil {
		results = []map[string]interface{}{}
	}

	return &PagedResult{Data: results, TotalItems: total}, nil
}

// ListEntities returns all distinct entity types in the database
func (s *SQLiteStore) ListEntities(ctx context.Context) ([]string, error) {

	rows, err := s.readDB.QueryContext(ctx, "SELECT DISTINCT entity_type FROM entities WHERE tenant_id = ? ORDER BY entity_type", int(s.config.TenantID))
	if err != nil {
		return nil, fmt.Errorf("query entity types: %w", err)
	}
	defer rows.Close()

	var entities []string
	for rows.Next() {
		var entityType string
		if err := rows.Scan(&entityType); err != nil {
			return nil, fmt.Errorf("scan entity type: %w", err)
		}
		entities = append(entities, entityType)
	}

	return entities, rows.Err()
}