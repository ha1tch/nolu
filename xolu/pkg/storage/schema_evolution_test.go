// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"testing"

	_ "modernc.org/sqlite"
)

// ---------------------------------------------------------------------------
// DiffAdaptedSpecs unit tests
// ---------------------------------------------------------------------------

func TestDiffAdaptedSpecs_NoDifference(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
			{Name: "age", JSONField: "age", Type: "integer", SQLType: "INTEGER"},
		},
		SchemaHash: "abc123",
	}
	diff := DiffAdaptedSpecs(spec, spec)
	if !diff.IsEmpty() {
		t.Error("expected empty diff for identical specs")
	}
}

func TestDiffAdaptedSpecs_AddedColumn(t *testing.T) {
	old := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
		},
	}
	new := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
			{Name: "email", JSONField: "email", Type: "string", SQLType: "TEXT"},
		},
	}
	diff := DiffAdaptedSpecs(old, new)
	if len(diff.Added) != 1 || diff.Added[0].Name != "email" {
		t.Errorf("expected 1 added column 'email', got %v", diff.Added)
	}
	if len(diff.Dropped) != 0 {
		t.Errorf("expected 0 dropped, got %d", len(diff.Dropped))
	}
}

func TestDiffAdaptedSpecs_DroppedColumn(t *testing.T) {
	old := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
			{Name: "age", JSONField: "age", Type: "integer", SQLType: "INTEGER"},
		},
	}
	new := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
		},
	}
	diff := DiffAdaptedSpecs(old, new)
	if len(diff.Dropped) != 1 || diff.Dropped[0].Name != "age" {
		t.Errorf("expected 1 dropped column 'age', got %v", diff.Dropped)
	}
}

func TestDiffAdaptedSpecs_TypeChange(t *testing.T) {
	old := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "score", JSONField: "score", Type: "integer", SQLType: "INTEGER"},
		},
	}
	new := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "score", JSONField: "score", Type: "string", SQLType: "TEXT"},
		},
	}
	diff := DiffAdaptedSpecs(old, new)
	if !diff.HasTypeConflicts() {
		t.Error("expected type conflict")
	}
	if len(diff.Changed) != 1 {
		t.Fatalf("expected 1 change, got %d", len(diff.Changed))
	}
	if diff.Changed[0].OldSQLType != "INTEGER" || diff.Changed[0].NewSQLType != "TEXT" {
		t.Errorf("change = %v, want INTEGER→TEXT", diff.Changed[0])
	}
}

func TestDiffAdaptedSpecs_MixedChanges(t *testing.T) {
	old := &AdaptedTableSpec{
		Entity: "products",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
			{Name: "price", JSONField: "price", Type: "number", SQLType: "REAL"},
			{Name: "sku", JSONField: "sku", Type: "string", SQLType: "TEXT"},
		},
	}
	new := &AdaptedTableSpec{
		Entity: "products",
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", SQLType: "TEXT"},
			{Name: "price", JSONField: "price", Type: "number", SQLType: "REAL"},
			{Name: "category", JSONField: "category", Type: "string", SQLType: "TEXT"},
		},
	}
	diff := DiffAdaptedSpecs(old, new)
	if len(diff.Added) != 1 || diff.Added[0].Name != "category" {
		t.Errorf("added = %v, want [category]", diff.Added)
	}
	if len(diff.Dropped) != 1 || diff.Dropped[0].Name != "sku" {
		t.Errorf("dropped = %v, want [sku]", diff.Dropped)
	}
	if len(diff.Changed) != 0 {
		t.Errorf("expected no type changes, got %v", diff.Changed)
	}
}

func TestDiffAdaptedSpecs_HasExtraChanged(t *testing.T) {
	old := &AdaptedTableSpec{Entity: "x", HasExtra: false}
	new := &AdaptedTableSpec{Entity: "x", HasExtra: true}
	diff := DiffAdaptedSpecs(old, new)
	if !diff.HasExtraChanged || !diff.NewHasExtra {
		t.Error("expected HasExtra change false→true")
	}
}

func TestDiffAdaptedSpecs_IndexChanges(t *testing.T) {
	old := &AdaptedTableSpec{
		Entity:  "items",
		Indexes: []IndexDef{{Name: "idx_old", Columns: []string{"a"}}},
	}
	new := &AdaptedTableSpec{
		Entity:  "items",
		Indexes: []IndexDef{{Name: "idx_new", Columns: []string{"b"}}},
	}
	diff := DiffAdaptedSpecs(old, new)
	if len(diff.IndexesAdded) != 1 || diff.IndexesAdded[0].Name != "idx_new" {
		t.Errorf("indexes added = %v, want [idx_new]", diff.IndexesAdded)
	}
	if len(diff.IndexesDropped) != 1 || diff.IndexesDropped[0].Name != "idx_old" {
		t.Errorf("indexes dropped = %v, want [idx_old]", diff.IndexesDropped)
	}
}

// ---------------------------------------------------------------------------
// MigrateAdaptedTable integration tests
// ---------------------------------------------------------------------------

func setupMigrationDB(t *testing.T) (*sql.DB, *AdaptedRegistry) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}

	// Create metadata table.
	if _, err := db.ExecContext(ctx, GenerateAdaptedSchemasTableSQL(dialect)); err != nil {
		t.Fatal(err)
	}

	// Create entities table (needed for blob path but also base infrastructure).
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS entities (
		entity_type TEXT NOT NULL,
		entity_id INTEGER NOT NULL,
		data TEXT,
		_version INTEGER DEFAULT 1,
		tenant_id TEXT DEFAULT '',
		PRIMARY KEY (entity_type, entity_id, tenant_id)
	)`); err != nil {
		t.Fatal(err)
	}

	registry := NewAdaptedRegistry()
	return db, registry
}

func registerWithSchema(t *testing.T, db *sql.DB, registry *AdaptedRegistry, entity string, schema map[string]interface{}) {
	t.Helper()
	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}
	if err := RegisterAdaptedTable(ctx, db, registry, entity, schema, dialect); err != nil {
		t.Fatalf("RegisterAdaptedTable failed: %v", err)
	}
}

func TestMigrate_AddColumn(t *testing.T) {
	db, registry := setupMigrationDB(t)
	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}

	// V1: name only.
	schemaV1 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}
	registerWithSchema(t, db, registry, "users", schemaV1)

	// Insert a row.
	spec := registry.Get("users")
	insertSQL, _ := dialect.InsertSQL(spec, spec.HasExtra)
	// InsertSQL column order: id, tenant_id, name, _extra (additionalProperties defaults true)
	_, err := db.ExecContext(ctx, insertSQL, 1, "", "Alice", nil)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	// V2: add email.
	schemaV2 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"email": map[string]interface{}{"type": "string"},
		},
	}
	registerWithSchema(t, db, registry, "users", schemaV2)

	// Verify new column exists and old data preserved.
	newSpec := registry.Get("users")
	if len(newSpec.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(newSpec.Columns))
	}

	// Read the row back — email should be NULL.
	var name, emailPtr *string
	err = db.QueryRowContext(ctx,
		"SELECT name, email FROM olu_users WHERE id = 1").Scan(&name, &emailPtr)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}
	if *name != "Alice" {
		t.Errorf("name = %v, want Alice", *name)
	}
	if emailPtr != nil {
		t.Errorf("email = %v, want nil", *emailPtr)
	}
}

func TestMigrate_DropColumn_WithExtra(t *testing.T) {
	db, registry := setupMigrationDB(t)
	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}

	// V1: name + age, with additionalProperties.
	schemaV1 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
			"age":  map[string]interface{}{"type": "integer"},
		},
		"additionalProperties": true,
	}
	registerWithSchema(t, db, registry, "people", schemaV1)

	// Insert a row with both fields.
	spec := registry.Get("people")
	insertSQL, _ := dialect.InsertSQL(spec, spec.HasExtra)
	// InsertSQL column order: id, tenant_id, age, name, _extra (fields sorted alphabetically)
	_, err := db.ExecContext(ctx, insertSQL, 1, "", 30, "Alice", nil)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	// V2: drop age, keep name, still has additionalProperties.
	schemaV2 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
		"additionalProperties": true,
	}
	registerWithSchema(t, db, registry, "people", schemaV2)

	// Verify age column is gone but data migrated to _extra.
	newSpec := registry.Get("people")
	if len(newSpec.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(newSpec.Columns))
	}

	var name string
	var extra *string
	err = db.QueryRowContext(ctx,
		"SELECT name, _extra FROM olu_people WHERE id = 1").Scan(&name, &extra)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}
	if name != "Alice" {
		t.Errorf("name = %s, want Alice", name)
	}
	if extra == nil {
		t.Fatal("expected _extra to contain migrated age data")
	}
	var extraMap map[string]interface{}
	if err := json.Unmarshal([]byte(*extra), &extraMap); err != nil {
		t.Fatalf("failed to parse _extra: %v", err)
	}
	if _, ok := extraMap["age"]; !ok {
		t.Errorf("_extra = %v, want key 'age'", extraMap)
	}
}

func TestMigrate_TypeChange_Rejected(t *testing.T) {
	db, registry := setupMigrationDB(t)
	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}

	schemaV1 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"score": map[string]interface{}{"type": "integer"},
		},
	}
	registerWithSchema(t, db, registry, "scores", schemaV1)

	// V2: change score from integer to string — should fail.
	schemaV2 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"score": map[string]interface{}{"type": "string"},
		},
	}
	err := RegisterAdaptedTable(ctx, db, registry, "scores", schemaV2, dialect)
	if err == nil {
		t.Fatal("expected error for type change, got nil")
	}
	if !contains(err.Error(), "incompatible type changes") {
		t.Errorf("error = %v, want 'incompatible type changes'", err)
	}
}

func TestMigrate_SameSchema_NoOp(t *testing.T) {
	db, registry := setupMigrationDB(t)
	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}
	registerWithSchema(t, db, registry, "items", schema)

	// Register again with same schema — should be a no-op.
	err := RegisterAdaptedTable(ctx, db, registry, "items", schema, dialect)
	if err != nil {
		t.Fatalf("re-registration of same schema should be no-op, got: %v", err)
	}
}

func TestMigrate_AddAndDrop_Simultaneously(t *testing.T) {
	db, registry := setupMigrationDB(t)
	ctx := context.Background()
	dialect := &SQLiteStorageDialect{}

	schemaV1 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
			"old":  map[string]interface{}{"type": "string"},
		},
		"additionalProperties": true,
	}
	registerWithSchema(t, db, registry, "things", schemaV1)

	// Insert data.
	spec := registry.Get("things")
	insertSQL, _ := dialect.InsertSQL(spec, spec.HasExtra)
	// InsertSQL column order: id, tenant_id, name, old, _extra (fields sorted alphabetically)
	_, err := db.ExecContext(ctx, insertSQL, 1, "", "hello", "world", nil)
	if err != nil {
		t.Fatalf("insert failed: %v", err)
	}

	// V2: drop "old", add "fresh".
	schemaV2 := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"fresh": map[string]interface{}{"type": "string"},
		},
		"additionalProperties": true,
	}
	registerWithSchema(t, db, registry, "things", schemaV2)

	newSpec := registry.Get("things")
	if len(newSpec.Columns) != 2 {
		t.Fatalf("expected 2 columns, got %d", len(newSpec.Columns))
	}

	// Verify: name preserved, fresh is NULL, old migrated to _extra.
	var name string
	var fresh, extra *string
	err = db.QueryRowContext(ctx,
		"SELECT name, fresh, _extra FROM olu_things WHERE id = 1").Scan(&name, &fresh, &extra)
	if err != nil {
		t.Fatalf("select failed: %v", err)
	}
	if name != "hello" {
		t.Errorf("name = %s, want hello", name)
	}
	if fresh != nil {
		t.Errorf("fresh = %v, want nil", *fresh)
	}
	if extra != nil {
		var m map[string]interface{}
		json.Unmarshal([]byte(*extra), &m)
		if m["old"] != "world" {
			t.Errorf("_extra[old] = %v, want world", m["old"])
		}
	}
}

