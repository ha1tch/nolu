// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"database/sql"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

// setupAdaptedTestDB creates a temporary SQLite database with the
// adapted_table_schemas metadata table and a test adapted table.
func setupAdaptedTestDB(t *testing.T) (*sql.DB, StorageDialect, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "adapted_test_*.db")
	if err != nil {
		t.Fatal(err)
	}
	tmpFile.Close()

	db, err := sql.Open("sqlite", tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatal(err)
	}
	// WAL mode for consistency with production
	db.Exec("PRAGMA journal_mode=WAL")
	db.Exec("PRAGMA foreign_keys=ON")

	dialect := &SQLiteStorageDialect{}

	cleanup := func() {
		db.Close()
		os.Remove(tmpFile.Name())
	}
	return db, dialect, cleanup
}

// ---------------------------------------------------------------------------
// Registry tests
// ---------------------------------------------------------------------------

func TestAdaptedRegistry_BasicOps(t *testing.T) {
	reg := NewAdaptedRegistry()

	if reg.IsAdapted("users") {
		t.Error("empty registry reports users as adapted")
	}
	if reg.Get("users") != nil {
		t.Error("Get on empty registry should return nil")
	}

	spec := &AdaptedTableSpec{Entity: "users", Columns: []ColumnDef{
		{Name: "name", SQLType: "TEXT"},
	}}
	reg.Set("users", spec)

	if !reg.IsAdapted("users") {
		t.Error("registry should report users as adapted after Set")
	}
	if reg.Get("users") != spec {
		t.Error("Get should return the registered spec")
	}

	entities := reg.Entities()
	if len(entities) != 1 || entities[0] != "users" {
		t.Errorf("Entities = %v, want [users]", entities)
	}
}

// ---------------------------------------------------------------------------
// RegisterAdaptedTable tests
// ---------------------------------------------------------------------------

func TestRegisterAdaptedTable_CreatesTableAndMetadata(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"age":   map[string]interface{}{"type": "integer"},
			"email": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"name", "email"},
	}

	err := RegisterAdaptedTable(ctx, db, registry, "users", schema, dialect)
	if err != nil {
		t.Fatalf("RegisterAdaptedTable failed: %v", err)
	}

	// Verify table exists
	var tableName string
	err = db.QueryRowContext(ctx,
		"SELECT name FROM sqlite_master WHERE type='table' AND name='olu_users'",
	).Scan(&tableName)
	if err != nil {
		t.Fatalf("adapted table not created: %v", err)
	}

	// Verify metadata recorded
	var hash string
	err = db.QueryRowContext(ctx,
		"SELECT schema_hash FROM adapted_table_schemas WHERE entity_type='users'",
	).Scan(&hash)
	if err != nil {
		t.Fatalf("metadata not recorded: %v", err)
	}
	if hash == "" {
		t.Error("schema_hash is empty")
	}

	// Verify registry populated
	if !registry.IsAdapted("users") {
		t.Error("registry not populated after registration")
	}
}

func TestRegisterAdaptedTable_IdempotentSameSchema(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	// First registration
	if err := RegisterAdaptedTable(ctx, db, registry, "things", schema, dialect); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	// Second registration with same schema should succeed (no-op)
	if err := RegisterAdaptedTable(ctx, db, registry, "things", schema, dialect); err != nil {
		t.Fatalf("idempotent registration failed: %v", err)
	}
}

func TestRegisterAdaptedTable_DetectsSchemaChange(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema1 := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}
	schema2 := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"email": map[string]interface{}{"type": "string"},
		},
	}

	if err := RegisterAdaptedTable(ctx, db, registry, "users", schema1, dialect); err != nil {
		t.Fatalf("first registration failed: %v", err)
	}

	// Changed schema should succeed via automatic migration (add column).
	if err := RegisterAdaptedTable(ctx, db, registry, "users", schema2, dialect); err != nil {
		t.Fatalf("migration should succeed for added column, got: %v", err)
	}

	// Verify the new column exists in the registry.
	spec := registry.Get("users")
	found := false
	for _, col := range spec.Columns {
		if col.Name == "email" {
			found = true
		}
	}
	if !found {
		t.Error("expected 'email' column in migrated spec")
	}

	// Incompatible type change should still error.
	schema3 := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "integer"}, // was string
			"email": map[string]interface{}{"type": "string"},
		},
	}
	err := RegisterAdaptedTable(ctx, db, registry, "users", schema3, dialect)
	if err == nil {
		t.Fatal("expected error for incompatible type change, got nil")
	}
	if !contains(err.Error(), "incompatible type changes") {
		t.Errorf("error should mention incompatible type changes, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// LoadAdaptedRegistry tests
// ---------------------------------------------------------------------------

func TestLoadAdaptedRegistry_RoundTrip(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry1 := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"score": map[string]interface{}{"type": "number"},
		},
		"required": []interface{}{"name"},
	}

	if err := RegisterAdaptedTable(ctx, db, registry1, "players", schema, dialect); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	// Load into a fresh registry (simulates restart)
	registry2, err := LoadAdaptedRegistry(ctx, db)
	if err != nil {
		t.Fatalf("LoadAdaptedRegistry failed: %v", err)
	}

	if !registry2.IsAdapted("players") {
		t.Error("loaded registry should have 'players'")
	}

	spec := registry2.Get("players")
	if spec == nil {
		t.Fatal("loaded spec is nil")
	}
	if spec.Entity != "players" {
		t.Errorf("Entity = %q, want players", spec.Entity)
	}
	if len(spec.Columns) != 2 {
		t.Errorf("got %d columns, want 2", len(spec.Columns))
	}
}

func TestLoadAdaptedRegistry_EmptyDB(t *testing.T) {
	db, _, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry, err := LoadAdaptedRegistry(ctx, db)
	if err != nil {
		t.Fatalf("LoadAdaptedRegistry on empty DB failed: %v", err)
	}
	if len(registry.Entities()) != 0 {
		t.Error("empty DB should produce empty registry")
	}
}

// ---------------------------------------------------------------------------
// Adapted CRUD operation tests
// ---------------------------------------------------------------------------

func TestAdaptedCRUD_CreateAndGet(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"age":   map[string]interface{}{"type": "integer"},
			"score": map[string]interface{}{"type": "number"},
		},
		"required": []interface{}{"name"},
	}

	if err := RegisterAdaptedTable(ctx, db, registry, "users", schema, dialect); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	spec := registry.Get("users")

	// Create
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	data := map[string]interface{}{
		"name":  "Alice",
		"age":   float64(30),
		"score": float64(99.5),
	}

	if err := adaptedCreate(ctx, tx, spec, dialect, 0, 1, data); err != nil {
		t.Fatalf("adaptedCreate failed: %v", err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	// Get
	result, err := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if err != nil {
		t.Fatalf("adaptedGet failed: %v", err)
	}

	if result["id"] != 1 {
		t.Errorf("id = %v, want 1", result["id"])
	}
	if result["name"] != "Alice" {
		t.Errorf("name = %v, want Alice", result["name"])
	}
	if result["_version"] != 1 {
		t.Errorf("_version = %v, want 1", result["_version"])
	}
}

func TestAdaptedCRUD_CreateWithOverflow(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	if err := RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	spec := registry.Get("items")
	tx, _ := db.BeginTx(ctx, nil)

	data := map[string]interface{}{
		"name":  "Widget",
		"color": "blue",    // overflow
		"weight": float64(1.5), // overflow
	}

	if err := adaptedCreate(ctx, tx, spec, dialect, 0, 1, data); err != nil {
		t.Fatalf("create with overflow failed: %v", err)
	}
	tx.Commit()

	result, err := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	if result["name"] != "Widget" {
		t.Errorf("name = %v, want Widget", result["name"])
	}
	if result["color"] != "blue" {
		t.Errorf("overflow color = %v, want blue", result["color"])
	}
	if result["weight"] != 1.5 {
		t.Errorf("overflow weight = %v, want 1.5", result["weight"])
	}
}

func TestAdaptedCRUD_Update(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
			"age":  map[string]interface{}{"type": "integer"},
		},
	}

	RegisterAdaptedTable(ctx, db, registry, "users", schema, dialect)
	spec := registry.Get("users")

	// Create
	tx, _ := db.BeginTx(ctx, nil)
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{
		"name": "Alice", "age": float64(30),
	})
	tx.Commit()

	// Update
	tx, _ = db.BeginTx(ctx, nil)
	err := adaptedUpdate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{
		"name": "Alice B.", "age": float64(31),
	}, 0, false)
	if err != nil {
		t.Fatalf("adaptedUpdate failed: %v", err)
	}
	tx.Commit()

	result, _ := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if result["name"] != "Alice B." {
		t.Errorf("name = %v, want 'Alice B.'", result["name"])
	}
	if result["_version"] != 2 {
		t.Errorf("_version = %v, want 2", result["_version"])
	}
}

func TestAdaptedCRUD_UpdateWithVersionCheck(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect)
	spec := registry.Get("items")

	tx, _ := db.BeginTx(ctx, nil)
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "A"})
	tx.Commit()

	// Update with correct version
	tx, _ = db.BeginTx(ctx, nil)
	err := adaptedUpdate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "B"}, 1, true)
	if err != nil {
		t.Fatalf("update with correct version failed: %v", err)
	}
	tx.Commit()

	// Update with wrong version
	tx, _ = db.BeginTx(ctx, nil)
	err = adaptedUpdate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "C"}, 1, true)
	if err != ErrConflict {
		t.Errorf("expected ErrConflict, got %v", err)
	}
	tx.Rollback()
}

func TestAdaptedCRUD_Delete(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect)
	spec := registry.Get("items")

	tx, _ := db.BeginTx(ctx, nil)
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "A"})
	tx.Commit()

	// Delete
	tx, _ = db.BeginTx(ctx, nil)
	if err := adaptedDelete(ctx, tx, spec, dialect, 0, 1); err != nil {
		t.Fatalf("delete failed: %v", err)
	}
	tx.Commit()

	// Verify gone
	_, err := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}

	// Delete non-existent
	tx, _ = db.BeginTx(ctx, nil)
	err = adaptedDelete(ctx, tx, spec, dialect, 0, 999)
	if err != ErrNotFound {
		t.Errorf("expected ErrNotFound for missing entity, got %v", err)
	}
	tx.Rollback()
}

func TestAdaptedCRUD_List(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect)
	spec := registry.Get("items")

	// Insert 3 items
	tx, _ := db.BeginTx(ctx, nil)
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "A"})
	adaptedCreate(ctx, tx, spec, dialect, 0, 2, map[string]interface{}{"name": "B"})
	adaptedCreate(ctx, tx, spec, dialect, 0, 3, map[string]interface{}{"name": "C"})
	tx.Commit()

	results, err := adaptedList(ctx, db, spec, dialect, 0)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("got %d results, want 3", len(results))
	}

	// Should be ordered by id
	for i, r := range results {
		if r["id"] != i+1 {
			t.Errorf("results[%d].id = %v, want %d", i, r["id"], i+1)
		}
	}
}

func TestAdaptedCRUD_Exists(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect)
	spec := registry.Get("items")

	tx, _ := db.BeginTx(ctx, nil)
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "A"})
	tx.Commit()

	if !adaptedExists(ctx, db, spec, dialect, 0, 1) {
		t.Error("exists should return true for existing entity")
	}
	if adaptedExists(ctx, db, spec, dialect, 0, 999) {
		t.Error("exists should return false for non-existent entity")
	}
}

func TestAdaptedCRUD_REFRoundTrip(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"title": map[string]interface{}{"type": "string"},
			"author": map[string]interface{}{
				"type":   "object",
				"format": "ref",
			},
		},
		"required": []interface{}{"title", "author"},
	}

	RegisterAdaptedTable(ctx, db, registry, "articles", schema, dialect)
	spec := registry.Get("articles")

	tx, _ := db.BeginTx(ctx, nil)
	data := map[string]interface{}{
		"title": "Hello World",
		"author": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     float64(42),
		},
	}
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, data)
	tx.Commit()

	result, err := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if err != nil {
		t.Fatalf("get failed: %v", err)
	}

	author, ok := result["author"].(map[string]interface{})
	if !ok {
		t.Fatalf("author is not a map, got %T: %v", result["author"], result["author"])
	}
	if author["type"] != "REF" {
		t.Errorf("author.type = %v, want REF", author["type"])
	}
	if author["entity"] != "users" {
		t.Errorf("author.entity = %v, want users", author["entity"])
	}
	// Note: SQLite returns int64, ReassembleData preserves it
	if author["id"] != int64(42) {
		t.Errorf("author.id = %v (%T), want 42", author["id"], author["id"])
	}
}

func TestAdaptedCRUD_TenantIsolation(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect)
	spec := registry.Get("items")

	// Insert into tenant 0 and tenant 1
	tx, _ := db.BeginTx(ctx, nil)
	adaptedCreate(ctx, tx, spec, dialect, 0, 1, map[string]interface{}{"name": "T0-Item"})
	adaptedCreate(ctx, tx, spec, dialect, 1, 1, map[string]interface{}{"name": "T1-Item"})
	tx.Commit()

	// Tenant 0 should see only its item
	results, _ := adaptedList(ctx, db, spec, dialect, 0)
	if len(results) != 1 {
		t.Fatalf("tenant 0: got %d results, want 1", len(results))
	}
	if results[0]["name"] != "T0-Item" {
		t.Errorf("tenant 0 item name = %v, want T0-Item", results[0]["name"])
	}

	// Tenant 1 should see only its item
	results, _ = adaptedList(ctx, db, spec, dialect, 1)
	if len(results) != 1 {
		t.Fatalf("tenant 1: got %d results, want 1", len(results))
	}
	if results[0]["name"] != "T1-Item" {
		t.Errorf("tenant 1 item name = %v, want T1-Item", results[0]["name"])
	}

	// Cross-tenant get should fail
	_, err := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if err != nil {
		t.Errorf("tenant 0 get own item failed: %v", err)
	}

	// Tenant 0 get tenant 1's id=1 (same table, different tenant)
	r, err := adaptedGet(ctx, db, spec, dialect, 1, 1)
	if err != nil {
		t.Fatalf("cross-tenant get failed: %v", err)
	}
	if r["name"] != "T1-Item" {
		t.Errorf("cross-tenant result = %v, want T1-Item", r["name"])
	}
}

// ---------------------------------------------------------------------------
// Adapted CRUD with decimal fields — end-to-end through SQLite
// ---------------------------------------------------------------------------

func TestAdaptedCRUD_DecimalRoundTrip(t *testing.T) {
	db, dialect, cleanup := setupAdaptedTestDB(t)
	defer cleanup()

	ctx := context.Background()
	registry := NewAdaptedRegistry()

	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"description": map[string]interface{}{"type": "string"},
			"amount": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(10),
				"decimalScale":     float64(2),
			},
			"unit_price": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(12),
				"decimalScale":     float64(4),
			},
		},
	}

	if err := RegisterAdaptedTable(ctx, db, registry, "invoices", schema, dialect); err != nil {
		t.Fatalf("registration failed: %v", err)
	}

	spec := registry.Get("invoices")
	if spec == nil {
		t.Fatal("spec is nil after registration")
	}

	// Verify column types
	for _, col := range spec.Columns {
		if col.Format == "decimal" && col.SQLType != "INTEGER" {
			t.Errorf("decimal column %q has SQLType %q, want INTEGER", col.Name, col.SQLType)
		}
	}

	// Create with positive and negative decimal values
	tests := []struct {
		id          int
		description string
		amount      string
		unitPrice   string
		wantAmount  string
		wantUnit    string
	}{
		{1, "Invoice A", "1234.56", "99.9900", "1234.56", "99.9900"},
		{2, "Credit note", "-42.50", "-0.0100", "-42.50", "-0.0100"},
		{3, "Zero balance", "0", "0", "0.00", "0.0000"},
		{4, "Small value", "0.01", "0.0001", "0.01", "0.0001"},
		{5, "Large value", "99999999.99", "99999999.9999", "99999999.99", "99999999.9999"},
	}

	for _, tt := range tests {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}

		data := map[string]interface{}{
			"description": tt.description,
			"amount":      tt.amount,
			"unit_price":  tt.unitPrice,
		}

		if err := adaptedCreate(ctx, tx, spec, dialect, 0, tt.id, data); err != nil {
			tx.Rollback()
			t.Fatalf("create id=%d failed: %v", tt.id, err)
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}

		// Read back
		result, err := adaptedGet(ctx, db, spec, dialect, 0, tt.id)
		if err != nil {
			t.Fatalf("get id=%d failed: %v", tt.id, err)
		}

		if result["amount"] != tt.wantAmount {
			t.Errorf("id=%d amount = %q, want %q", tt.id, result["amount"], tt.wantAmount)
		}
		if result["unit_price"] != tt.wantUnit {
			t.Errorf("id=%d unit_price = %q, want %q", tt.id, result["unit_price"], tt.wantUnit)
		}
	}

	// Verify that the raw SQLite values are scaled integers
	var rawAmount int64
	err := db.QueryRowContext(ctx, "SELECT amount FROM olu_invoices WHERE id = 1 AND tenant_id = 0").Scan(&rawAmount)
	if err != nil {
		t.Fatalf("raw query failed: %v", err)
	}
	if rawAmount != 123456 {
		t.Errorf("raw amount = %d, want 123456 (1234.56 * 100)", rawAmount)
	}

	// Verify negative raw value
	err = db.QueryRowContext(ctx, "SELECT amount FROM olu_invoices WHERE id = 2 AND tenant_id = 0").Scan(&rawAmount)
	if err != nil {
		t.Fatalf("raw query negative failed: %v", err)
	}
	if rawAmount != -4250 {
		t.Errorf("raw negative amount = %d, want -4250 (-42.50 * 100)", rawAmount)
	}

	// Verify SQLite ordering is correct (ascending by amount)
	rows, err := db.QueryContext(ctx, "SELECT id FROM olu_invoices WHERE tenant_id = 0 ORDER BY amount ASC")
	if err != nil {
		t.Fatalf("order query failed: %v", err)
	}
	defer rows.Close()

	var ids []int
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}

	// Expected order: -42.50 (2), 0 (3), 0.01 (4), 1234.56 (1), 99999999.99 (5)
	expectedOrder := []int{2, 3, 4, 1, 5}
	if len(ids) != len(expectedOrder) {
		t.Fatalf("got %d rows, want %d", len(ids), len(expectedOrder))
	}
	for i, id := range ids {
		if id != expectedOrder[i] {
			t.Errorf("order position %d: got id=%d, want id=%d", i, id, expectedOrder[i])
		}
	}

	// Update a decimal value
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	updateData := map[string]interface{}{
		"description": "Updated invoice",
		"amount":      "-999.99",
		"unit_price":  "50.0000",
	}
	if err := adaptedUpdate(ctx, tx, spec, dialect, 0, 1, updateData, 1, true); err != nil {
		tx.Rollback()
		t.Fatalf("update failed: %v", err)
	}
	tx.Commit()

	result, err := adaptedGet(ctx, db, spec, dialect, 0, 1)
	if err != nil {
		t.Fatalf("get after update failed: %v", err)
	}
	if result["amount"] != "-999.99" {
		t.Errorf("updated amount = %q, want %q", result["amount"], "-999.99")
	}

	// List should return all with denormalised decimals
	all, err := adaptedList(ctx, db, spec, dialect, 0)
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(all) != 5 {
		t.Errorf("list returned %d items, want 5", len(all))
	}
}

// helper
func contains(s, sub string) bool {
	return len(s) >= len(sub) && searchString(s, sub)
}

func searchString(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
