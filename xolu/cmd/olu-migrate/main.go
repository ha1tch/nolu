// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// olu-migrate handles database migrations for olu.
//
// Subcommands:
//
//	olu-migrate schema -db /path/to/olu.db [-dry-run] [-verbose]
//	    Migrate SQLite schema to v2 (multi-tenant). Adds tenant_id column,
//	    per-tenant sequences, FTS rebuild, graph_t0000 table for tenant 0.
//
//	olu-migrate backfill -db /path/to/olu.db -tenant-field <field> [-dry-run] [-verbose]
//	    Backfill tenant_id from a JSON field in existing entities. Maps
//	    distinct string values to numeric tenant IDs.
//
//	olu-migrate jsonfile <source-dir> <target-db>
//	    Migrate data from JSONFile backend to SQLite (legacy command).
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/ha1tch/xolu/pkg/storage"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "schema":
		cmdSchema(os.Args[2:])
	case "backfill":
		cmdBackfill(os.Args[2:])
	case "jsonfile":
		cmdJSONFile(os.Args[2:])
	case "-h", "--help", "help":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

func printUsage() {
	fmt.Println(`Usage: olu-migrate <command> [options]

Commands:
  schema    Migrate SQLite schema to v2 (multi-tenant)
  backfill  Backfill tenant_id from JSON field in existing entities
  jsonfile  Migrate from JSONFile backend to SQLite (legacy)

Run 'olu-migrate <command> -h' for command-specific help.`)
}

// ---------------------------------------------------------------------------
// schema: v1 → v2 migration
// ---------------------------------------------------------------------------

func cmdSchema(args []string) {
	fs := flag.NewFlagSet("schema", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to olu SQLite database (required)")
	dryRun := fs.Bool("dry-run", false, "Show what would be done without making changes")
	verbose := fs.Bool("verbose", false, "Verbose output")
	_ = fs.Parse(args)

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: olu-migrate schema -db /path/to/olu.db [-dry-run] [-verbose]")
		os.Exit(1)
	}

	if _, err := os.Stat(*dbPath); os.IsNotExist(err) {
		log.Fatalf("Database not found: %s", *dbPath)
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	version := detectSchemaVersion(db)
	if *verbose {
		log.Printf("Current schema version: %d", version)
	}

	if version >= 2 {
		log.Println("Database is already at schema version 2. Nothing to do.")
		return
	}

	if err := migrateV1ToV2(db, *dryRun, *verbose); err != nil {
		log.Fatalf("Migration failed: %v", err)
	}

	log.Println("Schema migration complete.")
}

// ---------------------------------------------------------------------------
// backfill: tenant_id from JSON field
// ---------------------------------------------------------------------------

func cmdBackfill(args []string) {
	fs := flag.NewFlagSet("backfill", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to olu SQLite database (required)")
	tenantField := fs.String("tenant-field", "tenant_id", "JSON field containing tenant identifier")
	dryRun := fs.Bool("dry-run", false, "Show what would be done without making changes")
	verbose := fs.Bool("verbose", false, "Verbose output")
	_ = fs.Parse(args)

	if *dbPath == "" {
		fmt.Fprintln(os.Stderr, "Usage: olu-migrate backfill -db /path/to/olu.db -tenant-field <field> [-dry-run]")
		os.Exit(1)
	}

	db, err := sql.Open("sqlite", *dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer db.Close()

	version := detectSchemaVersion(db)
	if version < 2 {
		log.Fatal("Database must be at schema version 2 before backfill. Run 'olu-migrate schema' first.")
	}

	if err := backfillTenantIDs(db, *tenantField, *dryRun, *verbose); err != nil {
		log.Fatalf("Backfill failed: %v", err)
	}

	log.Println("Backfill complete.")
}

// ---------------------------------------------------------------------------
// jsonfile: legacy JSONFile → SQLite migration
// ---------------------------------------------------------------------------

func cmdJSONFile(args []string) {
	if len(args) < 2 {
		fmt.Println("Usage: olu-migrate jsonfile <source-dir> <target-db>")
		fmt.Println("Example: olu-migrate jsonfile ./data/default ./olu.db")
		os.Exit(1)
	}

	sourceDir := args[0]
	targetDB := args[1]

	if err := migrateJSONFile(sourceDir, targetDB); err != nil {
		log.Fatal(err)
	}

	fmt.Println("Migration completed successfully!")
}

// ---------------------------------------------------------------------------
// Schema migration implementation
// ---------------------------------------------------------------------------

func detectSchemaVersion(db *sql.DB) int {
	var version int
	err := db.QueryRow("SELECT COALESCE(MAX(version), 0) FROM schema_version").Scan(&version)
	if err != nil {
		return 0
	}
	return version
}

func migrateV1ToV2(db *sql.DB, dryRun, verbose bool) error {
	log.Println("Migrating schema v1 → v2 (multi-tenant)...")

	hasTenantCol := columnExists(db, "entities", "tenant_id")
	if hasTenantCol && verbose {
		log.Println("  tenant_id column already exists on entities, skipping ALTER")
	}

	if dryRun {
		log.Println("[DRY RUN] Would apply the following changes:")
		if !hasTenantCol {
			log.Println("  - Add tenant_id column to entities (default 0)")
			log.Println("  - Create index idx_tenant_entity")
		}
		log.Println("  - Rebuild entity_sequences with (tenant_id, entity_type) PK")
		log.Println("  - Rebuild FTS index with tenant_id column")
		log.Println("  - Create graph_t0000 table for tenant 0 (if missing)")
		log.Println("  - Set schema_version = 2")
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 1. Add tenant_id column
	if !hasTenantCol {
		if verbose {
			log.Println("  Adding tenant_id column to entities...")
		}
		if _, err := tx.Exec("ALTER TABLE entities ADD COLUMN tenant_id INTEGER NOT NULL DEFAULT 0"); err != nil {
			return fmt.Errorf("add tenant_id column: %w", err)
		}
		if _, err := tx.Exec("CREATE INDEX IF NOT EXISTS idx_tenant_entity ON entities(tenant_id, entity_type)"); err != nil {
			return fmt.Errorf("create index: %w", err)
		}
	}

	// 2. Rebuild entity_sequences
	if err := migrateEntitySequences(tx, verbose); err != nil {
		return err
	}

	// 3. Rebuild FTS
	if err := migrateFTS(tx, verbose); err != nil {
		return err
	}

	// 4. Ensure graph_t0000 table exists for tenant 0.
	if verbose {
		log.Println("  Ensuring graph_t0000 table exists for tenant 0...")
	}
	_, err = tx.Exec(`CREATE TABLE IF NOT EXISTS graph_t0000 (
		source_entity TEXT NOT NULL,
		source_id INTEGER NOT NULL,
		target_entity TEXT NOT NULL,
		target_id INTEGER NOT NULL,
		relationship_name TEXT NOT NULL,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (source_entity, source_id, target_entity, target_id, relationship_name)
	)`)
	if err != nil {
		return fmt.Errorf("create graph_t0000: %w", err)
	}

	// 5. Update schema version
	if _, err := tx.Exec("INSERT OR REPLACE INTO schema_version (version) VALUES (2)"); err != nil {
		return fmt.Errorf("update schema_version: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}

	log.Println("Schema migration v1 → v2 complete.")
	return nil
}

func migrateEntitySequences(tx *sql.Tx, verbose bool) error {
	if columnExists2(tx, "entity_sequences", "tenant_id") {
		if verbose {
			log.Println("  entity_sequences already has tenant_id, skipping")
		}
		return nil
	}

	if verbose {
		log.Println("  Rebuilding entity_sequences with (tenant_id, entity_type) PK...")
	}

	stmts := []string{
		"ALTER TABLE entity_sequences RENAME TO entity_sequences_old",
		`CREATE TABLE entity_sequences (
			tenant_id INTEGER NOT NULL DEFAULT 0,
			entity_type TEXT NOT NULL,
			next_id INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (tenant_id, entity_type)
		)`,
		"INSERT INTO entity_sequences (tenant_id, entity_type, next_id) SELECT 0, entity_type, next_id FROM entity_sequences_old",
		"DROP TABLE entity_sequences_old",
	}

	for _, s := range stmts {
		if _, err := tx.Exec(s); err != nil {
			return fmt.Errorf("entity_sequences rebuild: %w", err)
		}
	}
	return nil
}

func migrateFTS(tx *sql.Tx, verbose bool) error {
	var ftsExists int
	_ = tx.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='entities_fts'").Scan(&ftsExists)

	if ftsExists == 0 {
		if verbose {
			log.Println("  No FTS table found, creating fresh with tenant_id...")
		}
		_, err := tx.Exec(`CREATE VIRTUAL TABLE IF NOT EXISTS entities_fts USING fts5(
			tenant_id UNINDEXED,
			entity_type UNINDEXED,
			entity_id UNINDEXED,
			content
		)`)
		return err
	}

	// Check if FTS already has tenant_id
	_, err := tx.Exec("SELECT tenant_id FROM entities_fts LIMIT 0")
	if err == nil {
		if verbose {
			log.Println("  FTS table already has tenant_id, skipping")
		}
		return nil
	}

	if verbose {
		log.Println("  Rebuilding FTS table with tenant_id column...")
	}

	if _, err := tx.Exec("DROP TABLE IF EXISTS entities_fts"); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE VIRTUAL TABLE entities_fts USING fts5(
		tenant_id UNINDEXED,
		entity_type UNINDEXED,
		entity_id UNINDEXED,
		content
	)`); err != nil {
		return err
	}

	// Re-index all entities
	rows, err := tx.Query("SELECT entity_type, id, data FROM entities")
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var entityType, data string
		var id int
		if err := rows.Scan(&entityType, &id, &data); err != nil {
			continue
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}

		content := extractTextContent(parsed)
		if content == "" {
			continue
		}

		_, _ = tx.Exec(
			"INSERT INTO entities_fts (tenant_id, entity_type, entity_id, content) VALUES (?, ?, ?, ?)",
			"0", entityType, fmt.Sprintf("%d", id), content,
		)
		count++
	}

	if verbose {
		log.Printf("  Re-indexed %d entities for FTS", count)
	}
	return nil
}

// ---------------------------------------------------------------------------
// Backfill implementation
// ---------------------------------------------------------------------------

func backfillTenantIDs(db *sql.DB, field string, dryRun, verbose bool) error {
	log.Printf("Backfilling tenant_id from JSON field '%s'...", field)

	rows, err := db.Query("SELECT entity_type, id, data FROM entities WHERE tenant_id = 0")
	if err != nil {
		return err
	}

	type record struct {
		entityType string
		id         int
		tenantVal  string
	}

	var records []record
	tenantSet := make(map[string]bool)

	for rows.Next() {
		var et, data string
		var eid int
		if err := rows.Scan(&et, &eid, &data); err != nil {
			continue
		}

		var parsed map[string]interface{}
		if err := json.Unmarshal([]byte(data), &parsed); err != nil {
			continue
		}

		if val, ok := parsed[field]; ok {
			tv := fmt.Sprintf("%v", val)
			if tv != "" && tv != "0" {
				records = append(records, record{et, eid, tv})
				tenantSet[tv] = true
			}
		}
	}
	rows.Close()

	if len(tenantSet) == 0 {
		log.Println("No tenant values found to backfill.")
		return nil
	}

	// Assign numeric IDs (1, 2, 3, ...)
	tenantMap := make(map[string]int)
	nextID := 1
	for tv := range tenantSet {
		tenantMap[tv] = nextID
		nextID++
	}

	log.Printf("Found %d distinct tenant values, %d records to update:", len(tenantMap), len(records))
	for tv, tid := range tenantMap {
		log.Printf("  %q -> tenant_id %d (0x%04X)", tv, tid, tid)
	}

	if dryRun {
		log.Println("[DRY RUN] No changes made.")
		return nil
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	updated := 0
	for _, r := range records {
		tid := tenantMap[r.tenantVal]
		if _, err := tx.Exec(
			"UPDATE entities SET tenant_id = ? WHERE entity_type = ? AND id = ? AND tenant_id = 0",
			tid, r.entityType, r.id,
		); err != nil {
			log.Printf("  Warning: %s:%d: %v", r.entityType, r.id, err)
			continue
		}
		updated++
	}

	// Fix sequences: for each (tenant_id, entity_type), set next_id = max(id) + 1
	for _, tid := range tenantMap {
		_, _ = tx.Exec(`INSERT OR IGNORE INTO entity_sequences (tenant_id, entity_type, next_id)
			SELECT ?, entity_type, MAX(id) + 1
			FROM entities WHERE tenant_id = ?
			GROUP BY entity_type`, tid, tid)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit backfill: %w", err)
	}

	log.Printf("Updated %d records.", updated)
	return nil
}

// ---------------------------------------------------------------------------
// Legacy JSONFile → SQLite migration
// ---------------------------------------------------------------------------

func migrateJSONFile(sourceDir, targetDB string) error {
	ctx := context.Background()

	if _, err := os.Stat(sourceDir); os.IsNotExist(err) {
		return fmt.Errorf("source directory does not exist: %s", sourceDir)
	}
	if _, err := os.Stat(targetDB); err == nil {
		return fmt.Errorf("target database already exists: %s (delete it first)", targetDB)
	}

	fmt.Println("Opening source (JSONFile)...")
	baseDir := filepath.Dir(sourceDir)
	schema := filepath.Base(sourceDir)

	sourceStore, err := storage.NewStore("jsonfile", map[string]interface{}{
		"base_dir": baseDir,
		"schema":   schema,
	})
	if err != nil {
		return fmt.Errorf("failed to open source: %w", err)
	}
	defer sourceStore.Close()

	fmt.Println("Creating target (SQLite)...")
	targetStore, err := storage.NewStore("sqlite", map[string]interface{}{
		"db_path": targetDB,
	})
	if err != nil {
		return fmt.Errorf("failed to create target: %w", err)
	}
	defer targetStore.Close()

	entries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("failed to read source: %w", err)
	}

	totalEntities := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		entityType := entry.Name()
		fmt.Printf("Migrating %s...\n", entityType)

		entities, err := sourceStore.List(ctx, entityType)
		if err != nil {
			fmt.Printf("  Warning: failed to list %s: %v\n", entityType, err)
			continue
		}

		for _, entity := range entities {
			id, ok := entity["id"].(int)
			if !ok {
				if idFloat, ok := entity["id"].(float64); ok {
					id = int(idFloat)
				} else {
					continue
				}
			}
			if _, err := targetStore.Save(ctx, entityType, id, entity); err != nil {
				return fmt.Errorf("failed to migrate %s:%d: %w", entityType, id, err)
			}
			totalEntities++
		}

		fmt.Printf("  Migrated %d entities\n", len(entities))
	}

	fmt.Printf("\nTotal entities migrated: %d\n", totalEntities)
	return nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func columnExists(db *sql.DB, table, column string) bool {
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

func columnExists2(tx *sql.Tx, table, column string) bool {
	rows, err := tx.Query(fmt.Sprintf("PRAGMA table_info(%s)", table))
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

func extractTextContent(data map[string]interface{}) string {
	var parts []string
	for key, value := range data {
		if key == "id" {
			continue
		}
		switch v := value.(type) {
		case string:
			parts = append(parts, v)
		case float64, int, bool:
			parts = append(parts, fmt.Sprintf("%v", v))
		}
	}
	return strings.Join(parts, " ")
}
