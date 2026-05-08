// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"

	_ "modernc.org/sqlite"
)

// createV1Database builds a synthetic v1-schema SQLite database with test data.
func createV1Database(t *testing.T, dbPath string, entityCount int) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE entities (
			entity_type TEXT NOT NULL,
			id INTEGER NOT NULL,
			data TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (entity_type, id)
		)`,
		`CREATE TABLE entity_sequences (
			entity_type TEXT NOT NULL PRIMARY KEY,
			next_id INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_version VALUES (1)`,
	}

	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	for i := 1; i <= entityCount; i++ {
		data, _ := json.Marshal(map[string]interface{}{
			"name":     fmt.Sprintf("widget-%d", i),
			"category": "tools",
		})
		db.Exec("INSERT INTO entities VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
			"widgets", i, string(data))
	}
	db.Exec("INSERT INTO entity_sequences VALUES ('widgets', ?)", entityCount+1)
}

// createV1DatabaseWithTenantField builds a v1 database where entities have
// a tenant identifier stored as a JSON field (prototype tenancy pattern).
func createV1DatabaseWithTenantField(t *testing.T, dbPath string) {
	t.Helper()

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open DB: %v", err)
	}
	defer db.Close()

	stmts := []string{
		`CREATE TABLE entities (
			entity_type TEXT NOT NULL,
			id INTEGER NOT NULL,
			data TEXT NOT NULL,
			created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
			PRIMARY KEY (entity_type, id)
		)`,
		`CREATE TABLE entity_sequences (
			entity_type TEXT NOT NULL PRIMARY KEY,
			next_id INTEGER NOT NULL DEFAULT 1
		)`,
		`CREATE TABLE schema_version (version INTEGER PRIMARY KEY)`,
		`INSERT INTO schema_version VALUES (1)`,
	}

	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("create schema: %v", err)
		}
	}

	// 3 records for "acme", 2 for "globex", 1 with no tenant field
	records := []struct {
		id     int
		entity string
		data   map[string]interface{}
	}{
		{1, "orders", map[string]interface{}{"org": "acme", "amount": 100}},
		{2, "orders", map[string]interface{}{"org": "acme", "amount": 200}},
		{3, "orders", map[string]interface{}{"org": "acme", "amount": 300}},
		{4, "orders", map[string]interface{}{"org": "globex", "amount": 400}},
		{5, "orders", map[string]interface{}{"org": "globex", "amount": 500}},
		{6, "orders", map[string]interface{}{"amount": 600}}, // no org field
	}

	for _, r := range records {
		data, _ := json.Marshal(r.data)
		db.Exec("INSERT INTO entities VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
			r.entity, r.id, string(data))
	}
	db.Exec("INSERT INTO entity_sequences VALUES ('orders', 7)")
}

// ---------------------------------------------------------------------------
// Schema migration tests
// ---------------------------------------------------------------------------

func TestMigrateV1ToV2(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate_v1v2.db")
	createV1Database(t, dbPath, 10)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Pre-check: version 1, no tenant_id column
	version := detectSchemaVersion(db)
	if version != 1 {
		t.Fatalf("pre-migration version = %d, want 1", version)
	}
	if columnExists(db, "entities", "tenant_id") {
		t.Fatal("tenant_id column should not exist pre-migration")
	}

	// Run migration
	if err := migrateV1ToV2(db, false, false); err != nil {
		t.Fatalf("migrateV1ToV2: %v", err)
	}

	// Post-check: version 2
	version = detectSchemaVersion(db)
	if version != 2 {
		t.Errorf("post-migration version = %d, want 2", version)
	}

	// tenant_id column exists, all rows have default 0
	if !columnExists(db, "entities", "tenant_id") {
		t.Error("tenant_id column should exist post-migration")
	}

	var countZero int
	db.QueryRow("SELECT COUNT(*) FROM entities WHERE tenant_id = 0").Scan(&countZero)
	if countZero != 10 {
		t.Errorf("records with tenant_id=0: %d, want 10", countZero)
	}

	// entity_sequences has tenant_id column
	if !columnExists2FromDB(db, "entity_sequences", "tenant_id") {
		t.Error("entity_sequences should have tenant_id column")
	}

	// Sequence data preserved
	var nextID int
	db.QueryRow("SELECT next_id FROM entity_sequences WHERE tenant_id = 0 AND entity_type = 'widgets'").Scan(&nextID)
	if nextID != 11 {
		t.Errorf("widgets next_id = %d, want 11", nextID)
	}

	// graph_t0000 table exists for tenant 0
	var graphExists int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='graph_t0000'").Scan(&graphExists)
	if graphExists != 1 {
		t.Error("graph_t0000 table should exist")
	}

	// FTS table exists with tenant_id
	var ftsExists int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='entities_fts'").Scan(&ftsExists)
	if ftsExists != 1 {
		t.Error("entities_fts table should exist")
	}

	// Index exists
	var idxExists int
	db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='idx_tenant_entity'").Scan(&idxExists)
	if idxExists != 1 {
		t.Error("idx_tenant_entity index should exist")
	}
}

func TestMigrateV1ToV2_Idempotent(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate_idem.db")
	createV1Database(t, dbPath, 5)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Migrate once
	if err := migrateV1ToV2(db, false, false); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// Migrate again — should not error
	// (detectSchemaVersion returns 2, so cmdSchema would skip,
	// but we test the internal function directly for robustness)
	version := detectSchemaVersion(db)
	if version != 2 {
		t.Fatalf("version after first migration = %d", version)
	}

	// Calling migrateV1ToV2 again shouldn't break things
	// (it checks for existing columns/tables)
	if err := migrateV1ToV2(db, false, false); err != nil {
		t.Fatalf("second migration should not error: %v", err)
	}

	// Data still intact
	var count int
	db.QueryRow("SELECT COUNT(*) FROM entities").Scan(&count)
	if count != 5 {
		t.Errorf("entity count after double migration = %d, want 5", count)
	}
}

func TestMigrateV1ToV2_DryRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate_dry.db")
	createV1Database(t, dbPath, 5)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Dry run — should not change the database
	if err := migrateV1ToV2(db, true, false); err != nil {
		t.Fatalf("dry run: %v", err)
	}

	// Version should still be 1
	version := detectSchemaVersion(db)
	if version != 1 {
		t.Errorf("version after dry run = %d, want 1 (unchanged)", version)
	}

	// tenant_id should not exist
	if columnExists(db, "entities", "tenant_id") {
		t.Error("tenant_id should not exist after dry run")
	}
}

func TestMigrateV1ToV2_WithExistingFTS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate_fts.db")
	createV1Database(t, dbPath, 3)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// Add a v1-style FTS table (no tenant_id column)
	db.Exec(`CREATE VIRTUAL TABLE entities_fts USING fts5(
		entity_type UNINDEXED,
		entity_id UNINDEXED,
		content
	)`)
	db.Exec("INSERT INTO entities_fts (entity_type, entity_id, content) VALUES ('widgets', '1', 'widget-1 tools')")

	// Migrate — should rebuild FTS with tenant_id
	if err := migrateV1ToV2(db, false, false); err != nil {
		t.Fatalf("migration with existing FTS: %v", err)
	}

	// FTS should now have tenant_id column
	_, err = db.Exec("SELECT tenant_id FROM entities_fts LIMIT 0")
	if err != nil {
		t.Error("FTS table should have tenant_id column after migration")
	}

	// Re-indexed content should be present
	var ftsCount int
	db.QueryRow("SELECT COUNT(*) FROM entities_fts").Scan(&ftsCount)
	if ftsCount < 3 {
		t.Errorf("FTS re-indexed %d rows, want >= 3", ftsCount)
	}
}

// ---------------------------------------------------------------------------
// Backfill tests
// ---------------------------------------------------------------------------

func TestBackfillTenantIDs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backfill.db")
	createV1DatabaseWithTenantField(t, dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	// First migrate schema to v2
	if err := migrateV1ToV2(db, false, false); err != nil {
		t.Fatalf("schema migration: %v", err)
	}

	// All records start with tenant_id = 0
	var allZero int
	db.QueryRow("SELECT COUNT(*) FROM entities WHERE tenant_id = 0").Scan(&allZero)
	if allZero != 6 {
		t.Fatalf("pre-backfill: %d records with tenant_id=0, want 6", allZero)
	}

	// Backfill from the "org" field
	if err := backfillTenantIDs(db, "org", false, false); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// Post-backfill: 5 records should have non-zero tenant_id
	// (record 6 has no "org" field, stays at 0)
	var nonZero int
	db.QueryRow("SELECT COUNT(*) FROM entities WHERE tenant_id != 0").Scan(&nonZero)
	if nonZero != 5 {
		t.Errorf("post-backfill: %d records with non-zero tenant_id, want 5", nonZero)
	}

	// The 2 distinct org values should map to 2 different tenant IDs
	var distinctTenants int
	db.QueryRow("SELECT COUNT(DISTINCT tenant_id) FROM entities WHERE tenant_id != 0").Scan(&distinctTenants)
	if distinctTenants != 2 {
		t.Errorf("distinct tenant IDs = %d, want 2", distinctTenants)
	}

	// Records with same org should share the same tenant_id
	var acmeID, globexID int
	rows, _ := db.Query("SELECT tenant_id, data FROM entities WHERE tenant_id != 0")
	tenantOrg := make(map[int]string) // tenant_id -> org
	for rows.Next() {
		var tid int
		var data string
		rows.Scan(&tid, &data)
		var parsed map[string]interface{}
		json.Unmarshal([]byte(data), &parsed)
		org, _ := parsed["org"].(string)
		if existing, ok := tenantOrg[tid]; ok {
			if existing != org {
				t.Errorf("tenant_id %d maps to both %q and %q", tid, existing, org)
			}
		} else {
			tenantOrg[tid] = org
		}
		if org == "acme" {
			acmeID = tid
		} else {
			globexID = tid
		}
	}
	rows.Close()

	if acmeID == globexID {
		t.Error("acme and globex should have different tenant IDs")
	}

	// Record 6 (no org) should still be tenant_id = 0
	var rec6Tenant int
	db.QueryRow("SELECT tenant_id FROM entities WHERE entity_type = 'orders' AND id = 6").Scan(&rec6Tenant)
	if rec6Tenant != 0 {
		t.Errorf("record 6 tenant_id = %d, want 0 (no org field)", rec6Tenant)
	}
}

func TestBackfillTenantIDs_DryRun(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backfill_dry.db")
	createV1DatabaseWithTenantField(t, dbPath)

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migrateV1ToV2(db, false, false)

	// Dry run
	if err := backfillTenantIDs(db, "org", true, false); err != nil {
		t.Fatalf("backfill dry run: %v", err)
	}

	// All records should still be tenant_id = 0
	var allZero int
	db.QueryRow("SELECT COUNT(*) FROM entities WHERE tenant_id = 0").Scan(&allZero)
	if allZero != 6 {
		t.Errorf("after dry run: %d records with tenant_id=0, want 6", allZero)
	}
}

func TestBackfillTenantIDs_NoMatchingField(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "backfill_nofield.db")
	createV1Database(t, dbPath, 5) // v1 data has no "org" field

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	migrateV1ToV2(db, false, false)

	// Backfill with a field that doesn't exist — should complete without error
	if err := backfillTenantIDs(db, "org", false, false); err != nil {
		t.Fatalf("backfill with nonexistent field: %v", err)
	}

	// All records still at 0
	var allZero int
	db.QueryRow("SELECT COUNT(*) FROM entities WHERE tenant_id = 0").Scan(&allZero)
	if allZero != 5 {
		t.Errorf("%d records at tenant_id=0, want 5", allZero)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func columnExists2FromDB(db *sql.DB, table, column string) bool {
	rows, err := db.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notnull, pk int
		var dflt sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notnull, &dflt, &pk); err != nil {
			continue
		}
		if name == column {
			return true
		}
	}
	return false
}
