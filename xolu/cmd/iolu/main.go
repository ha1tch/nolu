// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// iolu is the administrative CLI for olu/xolu.
//
// It operates directly on the SQLite database file and must be run against
// a stopped server (or before the server starts). Never run iolu against a
// database that a running xolu server is actively writing to.
//
// Usage:
//
//	iolu db init     --db /path/to/data.db [--tenant name[:id]] [--graph] [--ts-dir /path]
//	iolu db status   --db /path/to/data.db [--base-dir /path]
//	iolu db upgrade  --db /path/to/data.db
//
//	iolu tenant create       --db /path/to/data.db --name <name> [--id <n>]
//	iolu tenant list         --db /path/to/data.db
//	iolu tenant info         --db /path/to/data.db --name <name> [--base-dir /path]
//	iolu tenant delete       --db /path/to/data.db --name <name> [--force]
//	iolu tenant provision-ts --db /path/to/data.db --name <name> --ts-dir /path/to/ts
//
//	iolu version
//	iolu help
package main

import (
	"context"
	"database/sql"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/ha1tch/xolu/pkg/version"
)

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version", "--version", "-v":
		fmt.Printf("iolu %s\n", version.Version)
	case "db":
		if len(os.Args) < 3 {
			printDBUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "init":
			cmdDBInit(os.Args[3:])
		case "status":
			cmdDBStatus(os.Args[3:])
		case "upgrade":
			cmdDBUpgrade(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "unknown db subcommand: %s\n", os.Args[2])
			printDBUsage()
			os.Exit(1)
		}
	case "tenant":
		if len(os.Args) < 3 {
			printTenantUsage()
			os.Exit(1)
		}
		switch os.Args[2] {
		case "create":
			cmdTenantCreate(os.Args[3:])
		case "list":
			cmdTenantList(os.Args[3:])
		case "info":
			cmdTenantInfo(os.Args[3:])
		case "delete":
			cmdTenantDelete(os.Args[3:])
		case "provision-ts":
			cmdTenantProvisionTS(os.Args[3:])
		default:
			fmt.Fprintf(os.Stderr, "unknown tenant subcommand: %s\n", os.Args[2])
			printTenantUsage()
			os.Exit(1)
		}
	case "help", "--help", "-h":
		printUsage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		printUsage()
		os.Exit(1)
	}
}

// ---------------------------------------------------------------------------
// db init
// ---------------------------------------------------------------------------

func cmdDBInit(args []string) {
	fs := flag.NewFlagSet("iolu db init", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to the olu SQLite database to create (required)")
	graph  := fs.Bool("graph", true, "Create graph edge tables for tenant 0")
	tsDir  := fs.String("ts-dir", "", "Base directory for timeseries storage (optional)")
	var tenants tenantFlags
	fs.Var(&tenants, "tenant", "Register a tenant: --tenant name or --tenant name:id (repeatable)")
	_ = fs.Parse(args)

	if *dbPath == "" {
		fs.Usage()
		os.Exit(1)
	}
	if _, err := os.Stat(*dbPath); err == nil {
		fatal("database already exists: %s\n  use 'iolu db upgrade' to apply migrations", *dbPath)
	}
	if err := os.MkdirAll(filepath.Dir(*dbPath), 0755); err != nil {
		fatal("create parent directory: %v", err)
	}

	db := createDB(*dbPath)
	defer db.Close()
	ctx := context.Background()

	fmt.Printf("initialising %s\n", *dbPath)

	if err := initSchema(ctx, db, *graph); err != nil {
		fatal("init schema: %v", err)
	}
	fmt.Printf("  \u2713  core schema (entities, sequences, schemas, schema_version, tenants, FTS)\n")
	if *graph {
		fmt.Printf("  \u2713  graph edge table (graph_t0000 for unscoped tenant)\n")
	}

	for _, t := range tenants {
		id, err := registerTenant(ctx, db, t.name, t.id)
		if err != nil {
			fatal("register tenant %q: %v", t.name, err)
		}
		if *graph {
			if err := createGraphTable(ctx, db, id); err != nil {
				fatal("create graph table for tenant %q: %v", t.name, err)
			}
		}
		tsNote := ""
		if *tsDir != "" {
			if err := provisionTSDir(*tsDir, id); err != nil {
				fatal("provision timeseries for tenant %q: %v", t.name, err)
			}
			tsNote = "  ts=provisioned"
		}
		fmt.Printf("  \u2713  tenant %-20s  id=%-5d  graph=%v%s\n", fmt.Sprintf("%q", t.name), id, *graph, tsNote)
	}

	if *tsDir != "" {
		if err := provisionTSDir(*tsDir, 0); err != nil {
			fatal("provision timeseries base: %v", err)
		}
		fmt.Printf("  \u2713  timeseries base directory provisioned at %s\n", *tsDir)
	}

	fmt.Printf("\ndone — database ready for xolu in strict mode\n")
}

// ---------------------------------------------------------------------------
// db status
// ---------------------------------------------------------------------------

func cmdDBStatus(args []string) {
	fs := flag.NewFlagSet("iolu db status", flag.ExitOnError)
	dbPath  := fs.String("db", "", "Path to the olu SQLite database (required)")
	baseDir := fs.String("base-dir", "", "olu base directory (default: directory of --db)")
	_ = fs.Parse(args)

	if *dbPath == "" {
		fs.Usage()
		os.Exit(1)
	}

	db := openDB(*dbPath)
	defer db.Close()
	ctx := context.Background()

	info, _ := os.Stat(*dbPath)
	walPath := *dbPath + "-wal"
	walSize := int64(0)
	if wi, err := os.Stat(walPath); err == nil {
		walSize = wi.Size()
	}

	fmt.Printf("Database:    %s\n", *dbPath)
	fmt.Printf("Size:        %s  (WAL: %s)\n", formatBytes(info.Size()), formatBytes(walSize))
	fmt.Printf("Modified:    %s\n", info.ModTime().Format(time.RFC3339))
	fmt.Println()

	// Schema versions.
	var versions []string
	rows, _ := db.QueryContext(ctx, `SELECT version FROM schema_version ORDER BY version`)
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var v int
			rows.Scan(&v)
			versions = append(versions, fmt.Sprintf("%d", v))
		}
	}
	if len(versions) > 0 {
		fmt.Printf("Schema:      versions %s\n", strings.Join(versions, ", "))
	} else {
		fmt.Printf("Schema:      \u26a0  no schema_version table\n")
	}
	fmt.Println()

	// Table row counts.
	fmt.Printf("Tables:\n")
	for _, t := range []string{"tenants", "entities", "entity_sequences", "schemas", "entities_fts"} {
		fmt.Printf("  %-24s  %d rows\n", t, tableCount(ctx, db, t))
	}
	fmt.Println()

	// Graph tables.
	fmt.Printf("Graph tables:\n")
	gtRows, _ := db.QueryContext(ctx,
		`SELECT name FROM sqlite_master WHERE type='table' AND name LIKE 'graph_t%' ORDER BY name`)
	if gtRows != nil {
		defer gtRows.Close()
		any := false
		for gtRows.Next() {
			var name string
			gtRows.Scan(&name)
			fmt.Printf("  %-24s  %d edges\n", name, tableCount(ctx, db, name))
			any = true
		}
		if !any {
			fmt.Printf("  (none)\n")
		}
	}
	fmt.Println()

	// Tenants.
	fmt.Printf("Tenants:\n")
	tRows, _ := db.QueryContext(ctx, `SELECT id, name, created_at FROM tenants ORDER BY id`)
	if tRows != nil {
		defer tRows.Close()
		any := false
		for tRows.Next() {
			var id int
			var name string
			var createdAt sql.NullString
			tRows.Scan(&id, &name, &createdAt)
			created := "-"
			if createdAt.Valid {
				created = createdAt.String
			}
			fmt.Printf("  %-5d  %-24s  %-20s  %d entities\n",
				id, name, created, tenantEntityCount(ctx, db, id))
			any = true
		}
		if !any {
			fmt.Printf("  (no tenants registered)\n")
		}
	}
	fmt.Println()

	// Timeseries.
	base := *baseDir
	if base == "" {
		base = filepath.Dir(*dbPath)
	}
	tsBase := filepath.Join(base, "ts")
	if info, err := os.Stat(tsBase); err == nil && info.IsDir() {
		entries, _ := os.ReadDir(tsBase)
		fmt.Printf("Timeseries:  %s  (%d tenant dirs, %s)\n",
			tsBase, len(entries), formatBytes(dirSize(tsBase)))
	} else {
		fmt.Printf("Timeseries:  not provisioned\n")
	}
}

// ---------------------------------------------------------------------------
// db upgrade
// ---------------------------------------------------------------------------

func cmdDBUpgrade(args []string) {
	fs := flag.NewFlagSet("iolu db upgrade", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to the olu SQLite database (required)")
	_ = fs.Parse(args)

	if *dbPath == "" {
		fs.Usage()
		os.Exit(1)
	}

	db := openDB(*dbPath)
	defer db.Close()
	ctx := context.Background()

	fmt.Printf("upgrading %s\n", *dbPath)

	if err := ensureCoreTables(ctx, db); err != nil {
		fatal("ensure core tables: %v", err)
	}
	fmt.Printf("  \u2713  core tables present\n")

	applied, err := applyMigration(ctx, db, 2,
		"INSERT OR IGNORE INTO schema_version (version) VALUES (2)")
	if err != nil {
		fatal("migration v2: %v", err)
	}
	if applied {
		fmt.Printf("  \u2713  migration v2 applied\n")
	} else {
		fmt.Printf("  \u2014  migration v2 already applied\n")
	}

	applied, err = applyMigration(ctx, db, 3,
		"ALTER TABLE entities ADD COLUMN _version INTEGER NOT NULL DEFAULT 1")
	if err != nil && !strings.Contains(err.Error(), "duplicate column") {
		fatal("migration v3: %v", err)
	}
	if applied {
		fmt.Printf("  \u2713  migration v3 applied (_version column)\n")
	} else {
		fmt.Printf("  \u2014  migration v3 already applied\n")
	}

	fmt.Printf("\ndone — database at latest schema\n")
}

// ---------------------------------------------------------------------------
// tenant create
// ---------------------------------------------------------------------------

func cmdTenantCreate(args []string) {
	fs := flag.NewFlagSet("iolu tenant create", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to the olu SQLite database (required)")
	name   := fs.String("name", "", "Tenant name (required)")
	id     := fs.Int("id", 0, "Tenant ID (optional; auto-assigns if omitted)")
	_ = fs.Parse(args)

	if *dbPath == "" || *name == "" {
		fs.Usage()
		os.Exit(1)
	}
	if *id < 0 || *id > 65535 {
		fatal("tenant ID must be between 1 and 65535")
	}

	db := openDB(*dbPath)
	defer db.Close()

	assigned, err := registerTenant(context.Background(), db, *name, uint16(*id))
	if err != nil {
		fatal("%v", err)
	}
	fmt.Printf("created tenant %q with ID %d\n", *name, assigned)
}

// ---------------------------------------------------------------------------
// tenant list
// ---------------------------------------------------------------------------

func cmdTenantList(args []string) {
	fs := flag.NewFlagSet("iolu tenant list", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to the olu SQLite database (required)")
	_ = fs.Parse(args)

	if *dbPath == "" {
		fs.Usage()
		os.Exit(1)
	}

	db := openDB(*dbPath)
	defer db.Close()
	ctx := context.Background()

	rows, err := db.QueryContext(ctx, `
		SELECT t.id, t.name, t.created_at,
		       COALESCE(e.cnt, 0) AS entity_count
		FROM tenants t
		LEFT JOIN (
			SELECT tenant_id, COUNT(*) AS cnt
			FROM entities GROUP BY tenant_id
		) e ON e.tenant_id = t.id
		ORDER BY t.id`)
	if err != nil {
		fatal("query tenants: %v", err)
	}
	defer rows.Close()

	fmt.Printf("%-6s  %-24s  %-20s  %s\n", "ID", "NAME", "CREATED", "ENTITIES")
	fmt.Printf("%-6s  %-24s  %-20s  %s\n",
		"------", "------------------------", "--------------------", "--------")

	count := 0
	for rows.Next() {
		var id int
		var name string
		var createdAt sql.NullString
		var entityCount int
		if err := rows.Scan(&id, &name, &createdAt, &entityCount); err != nil {
			fatal("scan: %v", err)
		}
		created := "-"
		if createdAt.Valid {
			created = createdAt.String
		}
		fmt.Printf("%-6d  %-24s  %-20s  %d\n", id, name, created, entityCount)
		count++
	}
	if err := rows.Err(); err != nil {
		fatal("iterate: %v", err)
	}
	fmt.Printf("\n%d tenant(s)\n", count)
}

// ---------------------------------------------------------------------------
// tenant info
// ---------------------------------------------------------------------------

func cmdTenantInfo(args []string) {
	fs := flag.NewFlagSet("iolu tenant info", flag.ExitOnError)
	dbPath  := fs.String("db", "", "Path to the olu SQLite database (required)")
	name    := fs.String("name", "", "Tenant name (required)")
	baseDir := fs.String("base-dir", "", "olu base directory (default: directory of --db)")
	_ = fs.Parse(args)

	if *dbPath == "" || *name == "" {
		fs.Usage()
		os.Exit(1)
	}

	db := openDB(*dbPath)
	defer db.Close()
	ctx := context.Background()

	var id int
	var createdAt sql.NullString
	err := db.QueryRowContext(ctx,
		`SELECT id, created_at FROM tenants WHERE name = ?`, *name).Scan(&id, &createdAt)
	if err == sql.ErrNoRows {
		fatal("tenant %q not found", *name)
	} else if err != nil {
		fatal("query tenant: %v", err)
	}

	created := "-"
	if createdAt.Valid {
		created = createdAt.String
	}
	fmt.Printf("Tenant:      %s\n", *name)
	fmt.Printf("ID:          %d  (hex: 0x%04X)\n", id, id)
	fmt.Printf("Created:     %s\n", created)

	// Entity breakdown.
	rows, _ := db.QueryContext(ctx, `
		SELECT entity_type, COUNT(*) AS cnt
		FROM entities WHERE tenant_id = ?
		GROUP BY entity_type ORDER BY entity_type`, id)
	fmt.Printf("\nEntities:\n")
	total, any := 0, false
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var entityType string
			var cnt int
			rows.Scan(&entityType, &cnt)
			fmt.Printf("  %-20s  %d\n", entityType, cnt)
			total += cnt
			any = true
		}
	}
	if !any {
		fmt.Printf("  (none)\n")
	} else {
		fmt.Printf("  %-20s  %d\n", "TOTAL", total)
	}

	// Graph table.
	graphTable := fmt.Sprintf("graph_t%04X", id)
	var graphCount int
	err = db.QueryRowContext(ctx,
		fmt.Sprintf("SELECT COUNT(*) FROM %s", graphTable)).Scan(&graphCount)
	fmt.Printf("\nGraph:\n")
	if err != nil {
		fmt.Printf("  table %s not found (graph not enabled or not created)\n", graphTable)
	} else {
		fmt.Printf("  table %-20s  %d edges\n", graphTable, graphCount)
	}

	// Timeseries.
	base := *baseDir
	if base == "" {
		base = filepath.Dir(*dbPath)
	}
	tsDir := filepath.Join(base, "ts", fmt.Sprintf("t%04x", id))
	fmt.Printf("\nTimeseries:\n")
	if info, err := os.Stat(tsDir); err == nil && info.IsDir() {
		fmt.Printf("  provisioned at %s  (%s)\n", tsDir, formatBytes(dirSize(tsDir)))
	} else {
		fmt.Printf("  not provisioned\n")
		fmt.Printf("  hint: iolu tenant provision-ts --db %s --name %s --ts-dir %s/ts\n",
			*dbPath, *name, base)
	}
}

// ---------------------------------------------------------------------------
// tenant delete
// ---------------------------------------------------------------------------

func cmdTenantDelete(args []string) {
	fs := flag.NewFlagSet("iolu tenant delete", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to the olu SQLite database (required)")
	name   := fs.String("name", "", "Tenant name (required)")
	force  := fs.Bool("force", false, "Delete even if entity data exists")
	_ = fs.Parse(args)

	if *dbPath == "" || *name == "" {
		fs.Usage()
		os.Exit(1)
	}

	db := openDB(*dbPath)
	defer db.Close()
	ctx := context.Background()

	var id int
	err := db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE name = ?`, *name).Scan(&id)
	if err == sql.ErrNoRows {
		fatal("tenant %q not found", *name)
	} else if err != nil {
		fatal("query tenant: %v", err)
	}

	var entityCount int
	db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM entities WHERE tenant_id = ?`, id).Scan(&entityCount)

	if entityCount > 0 && !*force {
		fatal("tenant %q (ID %d) has %d entities. Use --force to delete anyway.", *name, id, entityCount)
	}
	if entityCount > 0 {
		fmt.Fprintf(os.Stderr, "WARNING: %d entities will be orphaned.\n", entityCount)
	}

	result, err := db.ExecContext(ctx, `DELETE FROM tenants WHERE id = ?`, id)
	if err != nil {
		fatal("delete tenant: %v", err)
	}
	affected, _ := result.RowsAffected()
	if affected == 0 {
		fatal("tenant disappeared during delete")
	}

	fmt.Printf("deleted tenant %q (ID %d)\n", *name, id)
	if entityCount > 0 {
		fmt.Printf("NOTE: %d orphaned entities remain with tenant_id=%d\n", entityCount, id)
	}
}

// ---------------------------------------------------------------------------
// tenant provision-ts
// ---------------------------------------------------------------------------

func cmdTenantProvisionTS(args []string) {
	fs := flag.NewFlagSet("iolu tenant provision-ts", flag.ExitOnError)
	dbPath := fs.String("db", "", "Path to the olu SQLite database (required)")
	name   := fs.String("name", "", "Tenant name (required)")
	tsDir  := fs.String("ts-dir", "", "Base timeseries directory (required)")
	_ = fs.Parse(args)

	if *dbPath == "" || *name == "" || *tsDir == "" {
		fs.Usage()
		os.Exit(1)
	}

	db := openDB(*dbPath)
	defer db.Close()

	var id int
	err := db.QueryRowContext(context.Background(),
		`SELECT id FROM tenants WHERE name = ?`, *name).Scan(&id)
	if err == sql.ErrNoRows {
		fatal("tenant %q not found", *name)
	} else if err != nil {
		fatal("query tenant: %v", err)
	}

	if err := provisionTSDir(*tsDir, uint16(id)); err != nil {
		fatal("provision timeseries: %v", err)
	}
	tsPath := filepath.Join(*tsDir, fmt.Sprintf("t%04x", id))
	fmt.Printf("provisioned timeseries for tenant %q (ID %d) at %s\n", *name, id, tsPath)
}

// ---------------------------------------------------------------------------
// Schema helpers
// ---------------------------------------------------------------------------

const coreSchema = `
	CREATE TABLE IF NOT EXISTS entities (
		tenant_id   INTEGER NOT NULL DEFAULT 0,
		entity_type TEXT NOT NULL,
		id          INTEGER NOT NULL,
		data        TEXT NOT NULL,
		_version    INTEGER NOT NULL DEFAULT 1,
		created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (tenant_id, entity_type, id)
	);
	CREATE INDEX IF NOT EXISTS idx_entity_type   ON entities(entity_type);
	CREATE INDEX IF NOT EXISTS idx_updated_at    ON entities(updated_at);
	CREATE INDEX IF NOT EXISTS idx_tenant_entity ON entities(tenant_id, entity_type);

	CREATE TABLE IF NOT EXISTS entity_sequences (
		tenant_id   INTEGER NOT NULL DEFAULT 0,
		entity_type TEXT NOT NULL,
		next_id     INTEGER NOT NULL DEFAULT 1,
		PRIMARY KEY (tenant_id, entity_type)
	);

	CREATE TABLE IF NOT EXISTS schemas (
		entity_type TEXT PRIMARY KEY,
		schema      TEXT NOT NULL,
		created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS schema_version (
		version    INTEGER PRIMARY KEY,
		applied_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS tenants (
		id         INTEGER NOT NULL PRIMARY KEY,
		name       TEXT NOT NULL UNIQUE,
		created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP
	);

	CREATE VIRTUAL TABLE IF NOT EXISTS entities_fts USING fts5(
		tenant_id   UNINDEXED,
		entity_type UNINDEXED,
		entity_id   UNINDEXED,
		content
	);
`

const graphTableSQL = `
	CREATE TABLE IF NOT EXISTS %s (
		source_entity     TEXT NOT NULL,
		source_id         INTEGER NOT NULL,
		target_entity     TEXT NOT NULL,
		target_id         INTEGER NOT NULL,
		relationship_name TEXT NOT NULL,
		created_at        TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
		PRIMARY KEY (source_entity, source_id, target_entity, target_id, relationship_name)
	);
	CREATE INDEX IF NOT EXISTS idx_%s_source ON %s(source_entity, source_id);
	CREATE INDEX IF NOT EXISTS idx_%s_target ON %s(target_entity, target_id);
	CREATE INDEX IF NOT EXISTS idx_%s_rel    ON %s(relationship_name);
`

func initSchema(ctx context.Context, db *sql.DB, graph bool) error {
	for _, pragma := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA synchronous = NORMAL",
		"PRAGMA foreign_keys = ON",
	} {
		if _, err := db.ExecContext(ctx, pragma); err != nil {
			return fmt.Errorf("%s: %w", pragma, err)
		}
	}
	if _, err := db.ExecContext(ctx, coreSchema); err != nil {
		return fmt.Errorf("core schema: %w", err)
	}
	for _, v := range []int{2, 3} {
		db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_version (version) VALUES (?)", v)
	}
	if graph {
		if err := createGraphTable(ctx, db, 0); err != nil {
			return fmt.Errorf("graph table tenant 0: %w", err)
		}
	}
	return nil
}

func ensureCoreTables(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, coreSchema)
	return err
}

func createGraphTable(ctx context.Context, db *sql.DB, tenantID uint16) error {
	t := fmt.Sprintf("graph_t%04X", tenantID)
	_, err := db.ExecContext(ctx, fmt.Sprintf(graphTableSQL, t, t, t, t, t, t, t))
	return err
}

func applyMigration(ctx context.Context, db *sql.DB, v int, stmt string) (bool, error) {
	var count int
	db.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_version WHERE version = ?", v).Scan(&count)
	if count > 0 {
		return false, nil
	}
	_, err := db.ExecContext(ctx, stmt)
	if err != nil {
		return false, err
	}
	db.ExecContext(ctx, "INSERT OR IGNORE INTO schema_version (version) VALUES (?)", v)
	return true, nil
}

func registerTenant(ctx context.Context, db *sql.DB, name string, id uint16) (uint16, error) {
	var existingID int
	err := db.QueryRowContext(ctx, `SELECT id FROM tenants WHERE name = ?`, name).Scan(&existingID)
	if err == nil {
		return 0, fmt.Errorf("tenant %q already exists with ID %d", name, existingID)
	} else if err != sql.ErrNoRows {
		return 0, fmt.Errorf("query tenant name: %w", err)
	}
	if id == 0 {
		var maxID sql.NullInt64
		db.QueryRowContext(ctx, `SELECT MAX(id) FROM tenants`).Scan(&maxID)
		if maxID.Valid {
			next := maxID.Int64 + 1
			if next > 65535 {
				return 0, fmt.Errorf("tenant registry full")
			}
			id = uint16(next)
		} else {
			id = 1
		}
	} else {
		var existingName string
		err := db.QueryRowContext(ctx, `SELECT name FROM tenants WHERE id = ?`, int(id)).Scan(&existingName)
		if err == nil {
			return 0, fmt.Errorf("tenant ID %d already assigned to %q", id, existingName)
		} else if err != sql.ErrNoRows {
			return 0, fmt.Errorf("query tenant ID: %w", err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO tenants (id, name) VALUES (?, ?)`, int(id), name); err != nil {
		return 0, fmt.Errorf("insert tenant: %w", err)
	}
	return id, nil
}

func provisionTSDir(base string, tenantID uint16) error {
	return os.MkdirAll(filepath.Join(base, fmt.Sprintf("t%04x", tenantID)), 0755)
}

func tableCount(ctx context.Context, db *sql.DB, table string) int {
	var count int
	db.QueryRowContext(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", table)).Scan(&count)
	return count
}

func tenantEntityCount(ctx context.Context, db *sql.DB, tenantID int) int {
	var count int
	db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM entities WHERE tenant_id = ?", tenantID).Scan(&count)
	return count
}

// ---------------------------------------------------------------------------
// Database helpers
// ---------------------------------------------------------------------------

func createDB(path string) *sql.DB {
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		fatal("create database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fatal("ping database: %v", err)
	}
	return db
}

func openDB(path string) *sql.DB {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fatal("database not found: %s\n  hint: iolu db init --db %s", path, path)
	}
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		fatal("open database: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		fatal("ping database: %v", err)
	}
	return db
}

// ---------------------------------------------------------------------------
// tenantFlags: repeatable --tenant name[:id] flag
// ---------------------------------------------------------------------------

type tenantEntry struct {
	name string
	id   uint16
}

type tenantFlags []tenantEntry

func (t *tenantFlags) String() string { return "" }
func (t *tenantFlags) Set(s string) error {
	parts := strings.SplitN(s, ":", 2)
	name := strings.TrimSpace(parts[0])
	if name == "" {
		return fmt.Errorf("tenant name cannot be empty")
	}
	var id uint16
	if len(parts) == 2 {
		var n int
		if _, err := fmt.Sscanf(parts[1], "%d", &n); err != nil {
			return fmt.Errorf("invalid tenant ID %q: %w", parts[1], err)
		}
		if n < 1 || n > 65535 {
			return fmt.Errorf("tenant ID must be between 1 and 65535")
		}
		id = uint16(n)
	}
	*t = append(*t, tenantEntry{name: name, id: id})
	return nil
}

// ---------------------------------------------------------------------------
// Utilities
// ---------------------------------------------------------------------------

func dirSize(path string) int64 {
	var size int64
	_ = filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		size += info.Size()
		return nil
	})
	return size
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "iolu: "+format+"\n", args...)
	os.Exit(1)
}

// ---------------------------------------------------------------------------
// Usage
// ---------------------------------------------------------------------------

func printUsage() {
	w := os.Stderr
	fmt.Fprintf(w, "iolu %s \u2014 olu/xolu administrative CLI\n\n", version.Version)
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  iolu db <command>      [flags]   Database lifecycle")
	fmt.Fprintln(w, "  iolu tenant <command>  [flags]   Tenant management")
	fmt.Fprintln(w, "  iolu version                     Print version")
	fmt.Fprintln(w, "  iolu help                        Show this help")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "DB commands:")
	for _, l := range []struct{ cmd, desc string }{
		{"init",    "Create and initialise a new database (run before first xolu start)"},
		{"status",  "Show schema version, table counts, tenants, graph, timeseries"},
		{"upgrade", "Apply pending schema migrations to an existing database"},
	} {
		fmt.Fprintf(w, "  %-16s  %s\n", l.cmd, l.desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Tenant commands:")
	for _, l := range []struct{ cmd, desc string }{
		{"create",       "Register a new tenant"},
		{"list",         "List all tenants with entity counts"},
		{"info",         "Show tenant details, entity breakdown, graph and timeseries status"},
		{"delete",       "Remove a tenant from the registry"},
		{"provision-ts", "Provision timeseries storage directory for a tenant"},
	} {
		fmt.Fprintf(w, "  %-16s  %s\n", l.cmd, l.desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Examples:")
	fmt.Fprintln(w, "  # Initialise a new strict-mode database with two tenants:")
	fmt.Fprintln(w, "  iolu db init --db /data/hub.db --graph \\")
	fmt.Fprintln(w, "               --tenant vendocorp:1 --tenant retailchain:2")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Check database health before starting xolu:")
	fmt.Fprintln(w, "  iolu db status --db /data/hub.db")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Add a tenant to an existing database:")
	fmt.Fprintln(w, "  iolu tenant create --db /data/hub.db --name newpartner")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Provision timeseries for a tenant:")
	fmt.Fprintln(w, "  iolu tenant provision-ts --db /data/hub.db --name vendocorp --ts-dir /data/ts")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "  # Apply migrations after upgrading xolu:")
	fmt.Fprintln(w, "  iolu db upgrade --db /data/hub.db")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "WARNING: iolu writes directly to the SQLite file. Do not run db init or")
	fmt.Fprintln(w, "         db upgrade against a database xolu is actively writing to.")
}

func printDBUsage() {
	w := os.Stderr
	fmt.Fprintln(w, "Usage: iolu db <command> [flags]")
	fmt.Fprintln(w)
	for _, l := range []struct{ cmd, desc string }{
		{"init",    "Create a new database with schema and optional tenants"},
		{"status",  "Show database health and contents"},
		{"upgrade", "Apply pending schema migrations"},
	} {
		fmt.Fprintf(w, "  %-10s  %s\n", l.cmd, l.desc)
	}
	fmt.Fprintln(w, "\nRun 'iolu db <command> --help' for flags.")
}

func printTenantUsage() {
	w := os.Stderr
	fmt.Fprintln(w, "Usage: iolu tenant <command> [flags]")
	fmt.Fprintln(w)
	for _, l := range []struct{ cmd, desc string }{
		{"create",       "Register a new tenant"},
		{"list",         "List all tenants"},
		{"info",         "Show tenant details"},
		{"delete",       "Remove a tenant"},
		{"provision-ts", "Provision timeseries storage for a tenant"},
	} {
		fmt.Fprintf(w, "  %-14s  %s\n", l.cmd, l.desc)
	}
	fmt.Fprintln(w, "\nRun 'iolu tenant <command> --help' for flags.")
}
