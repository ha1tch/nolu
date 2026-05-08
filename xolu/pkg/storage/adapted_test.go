// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// DeriveAdaptedTableSpec tests
// ---------------------------------------------------------------------------

func TestDeriveAdaptedTableSpec_BasicTypes(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":   map[string]interface{}{"type": "string"},
			"age":    map[string]interface{}{"type": "integer"},
			"score":  map[string]interface{}{"type": "number"},
			"active": map[string]interface{}{"type": "boolean"},
			"tags":   map[string]interface{}{"type": "array"},
			"meta":   map[string]interface{}{"type": "object"},
		},
		"required": []interface{}{"name", "age"},
	}

	spec, err := DeriveAdaptedTableSpec("users", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	if spec.Entity != "users" {
		t.Errorf("Entity = %q, want %q", spec.Entity, "users")
	}
	if spec.TableName() != "olu_users" {
		t.Errorf("TableName = %q, want %q", spec.TableName(), "olu_users")
	}
	if !spec.HasExtra {
		t.Error("HasExtra = false, want true (additionalProperties not set)")
	}

	// Verify column count (no id column — that's a system column)
	if len(spec.Columns) != 6 {
		t.Fatalf("got %d columns, want 6", len(spec.Columns))
	}

	// Columns should be sorted alphabetically
	names := spec.ColumnNames()
	for i := 1; i < len(names); i++ {
		if names[i] < names[i-1] {
			t.Errorf("columns not sorted: %v", names)
			break
		}
	}

	// Verify type mapping
	typeChecks := map[string]string{
		"name":   "TEXT",
		"age":    "INTEGER",
		"score":  "REAL",
		"active": "INTEGER",
		"tags":   "TEXT",
		"meta":   "TEXT",
	}
	for _, col := range spec.Columns {
		want, ok := typeChecks[col.Name]
		if !ok {
			t.Errorf("unexpected column %q", col.Name)
			continue
		}
		if col.SQLType != want {
			t.Errorf("column %q: SQLType = %q, want %q", col.Name, col.SQLType, want)
		}
	}

	// Verify required
	for _, col := range spec.Columns {
		if col.Name == "name" && !col.Required {
			t.Error("column 'name' should be required")
		}
		if col.Name == "age" && !col.Required {
			t.Error("column 'age' should be required")
		}
		if col.Name == "score" && col.Required {
			t.Error("column 'score' should not be required")
		}
	}

	// Verify schema hash is non-empty and deterministic
	if spec.SchemaHash == "" {
		t.Error("SchemaHash is empty")
	}
	spec2, _ := DeriveAdaptedTableSpec("users", schema, &SQLiteStorageDialect{})
	if spec.SchemaHash != spec2.SchemaHash {
		t.Error("SchemaHash not deterministic")
	}
}

func TestDeriveAdaptedTableSpec_DecimalType(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"amount": map[string]interface{}{
				"type":             "number",
				"format":           "decimal",
				"decimalPrecision": float64(18),
				"decimalScale":     float64(4),
			},
		},
	}

	spec, err := DeriveAdaptedTableSpec("transactions", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	if len(spec.Columns) != 1 {
		t.Fatalf("got %d columns, want 1", len(spec.Columns))
	}

	col := spec.Columns[0]
	if col.SQLType != "INTEGER" {
		t.Errorf("decimal SQLType = %q, want INTEGER", col.SQLType)
	}
	if col.Format != "decimal" {
		t.Errorf("decimal Format = %q, want decimal", col.Format)
	}
	if col.Precision != 18 {
		t.Errorf("decimal Precision = %d, want 18", col.Precision)
	}
	if col.Scale != 4 {
		t.Errorf("decimal Scale = %d, want 4", col.Scale)
	}

	// Decimal fields should be auto-indexed
	hasIndex := false
	for _, idx := range spec.Indexes {
		if len(idx.Columns) == 1 && idx.Columns[0] == "amount" {
			hasIndex = true
		}
	}
	if !hasIndex {
		t.Error("decimal field 'amount' should be auto-indexed")
	}
}

func TestDeriveAdaptedTableSpec_DecimalDefaults(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"price": map[string]interface{}{
				"type":   "number",
				"format": "decimal",
				// no explicit precision/scale
			},
		},
	}

	spec, err := DeriveAdaptedTableSpec("products", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	col := spec.Columns[0]
	if col.Precision != 18 {
		t.Errorf("default Precision = %d, want 18", col.Precision)
	}
	if col.Scale != 4 {
		t.Errorf("default Scale = %d, want 4", col.Scale)
	}
}

func TestDeriveAdaptedTableSpec_REFFields(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"author": map[string]interface{}{
				"type":   "object",
				"format": "ref",
			},
			"name": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"author"},
	}

	spec, err := DeriveAdaptedTableSpec("articles", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	// author decomposes into REF_author_entity + REF_author_id, plus name
	if len(spec.Columns) != 3 {
		t.Fatalf("got %d columns, want 3 (REF_author_entity, REF_author_id, name)", len(spec.Columns))
	}

	// Find REF columns
	var refEntity, refID *ColumnDef
	for i := range spec.Columns {
		col := &spec.Columns[i]
		if col.Name == "REF_author_entity" {
			refEntity = col
		}
		if col.Name == "REF_author_id" {
			refID = col
		}
	}

	if refEntity == nil {
		t.Fatal("missing REF_author_entity column")
	}
	if refID == nil {
		t.Fatal("missing REF_author_id column")
	}

	if refEntity.SQLType != "TEXT" {
		t.Errorf("REF_author_entity SQLType = %q, want TEXT", refEntity.SQLType)
	}
	if refID.SQLType != "INTEGER" {
		t.Errorf("REF_author_id SQLType = %q, want INTEGER", refID.SQLType)
	}
	if !refEntity.IsREF || !refID.IsREF {
		t.Error("REF columns should have IsREF=true")
	}
	if refEntity.JSONField != "author" || refID.JSONField != "author" {
		t.Error("REF columns should reference JSON field 'author'")
	}
	if !refEntity.Required || !refID.Required {
		t.Error("REF columns from required field should be required")
	}

	// REF _id should be auto-indexed
	hasRefIndex := false
	for _, idx := range spec.Indexes {
		if len(idx.Columns) == 1 && idx.Columns[0] == "REF_author_id" {
			hasRefIndex = true
		}
	}
	if !hasRefIndex {
		t.Error("REF_author_id should be auto-indexed")
	}
}

func TestDeriveAdaptedTableSpec_NoAdditionalProperties(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
		"additionalProperties": false,
	}

	spec, err := DeriveAdaptedTableSpec("strict_entity", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	if spec.HasExtra {
		t.Error("HasExtra = true, want false (additionalProperties is false)")
	}
}

func TestDeriveAdaptedTableSpec_IDFieldExcluded(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"id":   map[string]interface{}{"type": "integer"},
			"name": map[string]interface{}{"type": "string"},
		},
	}

	spec, err := DeriveAdaptedTableSpec("things", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	for _, col := range spec.Columns {
		if col.Name == "id" {
			t.Error("id should not appear as a schema column (it's a system column)")
		}
	}
	if len(spec.Columns) != 1 {
		t.Errorf("got %d columns, want 1 (name only)", len(spec.Columns))
	}
}

func TestDeriveAdaptedTableSpec_NoProperties(t *testing.T) {
	schema := map[string]interface{}{}

	_, err := DeriveAdaptedTableSpec("empty", schema, &SQLiteStorageDialect{})
	if err == nil {
		t.Error("expected error for schema with no properties")
	}
}

func TestDeriveAdaptedTableSpec_AutoIndexEnum(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"status": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"active", "inactive", "pending"},
			},
		},
	}

	spec, err := DeriveAdaptedTableSpec("items", schema, &SQLiteStorageDialect{})
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpec failed: %v", err)
	}

	hasIndex := false
	for _, idx := range spec.Indexes {
		if len(idx.Columns) == 1 && idx.Columns[0] == "status" {
			hasIndex = true
		}
	}
	if !hasIndex {
		t.Error("enum field 'status' should be auto-indexed")
	}
}

// ---------------------------------------------------------------------------
// DDL generation tests
// ---------------------------------------------------------------------------

func TestGenerateCreateTableSQL(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "age", SQLType: "INTEGER", Required: true},
			{Name: "email", SQLType: "TEXT", Required: true},
			{Name: "name", SQLType: "TEXT", Required: true},
			{Name: "score", SQLType: "REAL"},
		},
		HasExtra: true,
	}

	ddl := GenerateCreateTableSQL(spec, &SQLiteStorageDialect{})

	// Verify table name
	if !strings.Contains(ddl, "CREATE TABLE IF NOT EXISTS olu_users") {
		t.Error("DDL missing correct table name")
	}

	// Verify system columns
	for _, sysCol := range []string{"id INTEGER", "tenant_id INTEGER", "_extra TEXT", "_version INTEGER"} {
		if !strings.Contains(ddl, sysCol) {
			t.Errorf("DDL missing system column %q", sysCol)
		}
	}

	// Verify required columns have NOT NULL
	if !strings.Contains(ddl, "age INTEGER NOT NULL") {
		t.Error("DDL: required column 'age' should be NOT NULL")
	}
	if !strings.Contains(ddl, "email TEXT NOT NULL") {
		t.Error("DDL: required column 'email' should be NOT NULL")
	}

	// Verify optional columns don't have NOT NULL
	// score is optional, so it should just be "score REAL,"
	lines := strings.Split(ddl, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "score") {
			if strings.Contains(trimmed, "NOT NULL") {
				t.Error("DDL: optional column 'score' should not be NOT NULL")
			}
		}
	}

	// Verify primary key
	if !strings.Contains(ddl, "PRIMARY KEY (tenant_id, id)") {
		t.Error("DDL missing primary key")
	}
}

func TestGenerateCreateTableSQL_NoExtra(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "strict",
		Columns: []ColumnDef{
			{Name: "name", SQLType: "TEXT", Required: true},
		},
		HasExtra: false,
	}

	ddl := GenerateCreateTableSQL(spec, &SQLiteStorageDialect{})

	if strings.Contains(ddl, "_extra") {
		t.Error("DDL should not contain _extra when HasExtra is false")
	}
}

func TestGenerateIndexSQL(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "users",
		Indexes: []IndexDef{
			{Name: "idx_olu_users_email", Columns: []string{"email"}},
			{Name: "idx_olu_users_status", Columns: []string{"status"}, Unique: true},
		},
	}

	stmts := GenerateIndexSQL(spec, &SQLiteStorageDialect{})

	// Should have tenant index + 2 custom indexes
	if len(stmts) != 3 {
		t.Fatalf("got %d index statements, want 3", len(stmts))
	}

	// Check tenant index
	if !strings.Contains(stmts[0], "idx_olu_users_tenant") {
		t.Error("first index should be tenant index")
	}

	// Check unique index
	found := false
	for _, stmt := range stmts {
		if strings.Contains(stmt, "UNIQUE") && strings.Contains(stmt, "status") {
			found = true
		}
	}
	if !found {
		t.Error("missing UNIQUE index on status")
	}
}

// ---------------------------------------------------------------------------
// PartitionData / ReassembleData tests
// ---------------------------------------------------------------------------

func TestPartitionData_BasicFields(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "age", JSONField: "age", Type: "integer"},
			{Name: "email", JSONField: "email", Type: "string"},
			{Name: "name", JSONField: "name", Type: "string"},
		},
		HasExtra: true,
	}

	data := map[string]interface{}{
		"id":    1,
		"name":  "Alice",
		"email": "alice@example.com",
		"age":   float64(30),
		"bio":   "Hello world", // overflow
	}

	colVals, extra := PartitionData(spec, data)

	if len(colVals) != 3 {
		t.Fatalf("got %d column values, want 3", len(colVals))
	}

	// Values should be in column order: age, email, name
	if colVals[0] != float64(30) {
		t.Errorf("age = %v, want 30", colVals[0])
	}
	if colVals[1] != "alice@example.com" {
		t.Errorf("email = %v, want alice@example.com", colVals[1])
	}
	if colVals[2] != "Alice" {
		t.Errorf("name = %v, want Alice", colVals[2])
	}

	// Overflow
	if extra == nil {
		t.Fatal("extra should not be nil")
	}
	if extra["bio"] != "Hello world" {
		t.Errorf("extra[bio] = %v, want 'Hello world'", extra["bio"])
	}
	if _, hasID := extra["id"]; hasID {
		t.Error("extra should not contain 'id'")
	}
}

func TestPartitionData_REFField(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "articles",
		Columns: []ColumnDef{
			{Name: "REF_author_entity", JSONField: "author", Type: "string", Format: "ref", IsREF: true},
			{Name: "REF_author_id", JSONField: "author", Type: "integer", Format: "ref", IsREF: true},
			{Name: "title", JSONField: "title", Type: "string"},
		},
		HasExtra: false,
	}

	data := map[string]interface{}{
		"id":    1,
		"title": "Test Article",
		"author": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     float64(42),
		},
	}

	colVals, extra := PartitionData(spec, data)

	if colVals[0] != "users" {
		t.Errorf("REF_author_entity = %v, want 'users'", colVals[0])
	}
	if colVals[1] != 42 {
		t.Errorf("REF_author_id = %v, want 42", colVals[1])
	}
	if colVals[2] != "Test Article" {
		t.Errorf("title = %v, want 'Test Article'", colVals[2])
	}
	if extra != nil {
		t.Errorf("extra should be nil (no HasExtra), got %v", extra)
	}
}

func TestReassembleData_BasicFields(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "users",
		Columns: []ColumnDef{
			{Name: "age", JSONField: "age", Type: "integer"},
			{Name: "email", JSONField: "email", Type: "string"},
			{Name: "name", JSONField: "name", Type: "string"},
		},
	}

	colVals := []interface{}{int64(30), "alice@example.com", "Alice"}
	extra := map[string]interface{}{"bio": "Hello world"}

	result := ReassembleData(spec, colVals, extra, 1, 3)

	if result["id"] != 1 {
		t.Errorf("id = %v, want 1", result["id"])
	}
	if result["_version"] != 3 {
		t.Errorf("_version = %v, want 3", result["_version"])
	}
	if result["name"] != "Alice" {
		t.Errorf("name = %v, want Alice", result["name"])
	}
	if result["email"] != "alice@example.com" {
		t.Errorf("email = %v, want alice@example.com", result["email"])
	}
	if result["age"] != int64(30) {
		t.Errorf("age = %v, want 30", result["age"])
	}
	if result["bio"] != "Hello world" {
		t.Errorf("bio = %v, want 'Hello world'", result["bio"])
	}
}

func TestReassembleData_REFField(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "articles",
		Columns: []ColumnDef{
			{Name: "REF_author_entity", JSONField: "author", Type: "string", Format: "ref", IsREF: true},
			{Name: "REF_author_id", JSONField: "author", Type: "integer", Format: "ref", IsREF: true},
			{Name: "title", JSONField: "title", Type: "string"},
		},
	}

	colVals := []interface{}{"users", int64(42), "Test Article"}

	result := ReassembleData(spec, colVals, nil, 1, 1)

	author, ok := result["author"].(map[string]interface{})
	if !ok {
		t.Fatalf("author is not a map, got %T", result["author"])
	}
	if author["type"] != "REF" {
		t.Errorf("author.type = %v, want REF", author["type"])
	}
	if author["entity"] != "users" {
		t.Errorf("author.entity = %v, want users", author["entity"])
	}
	if author["id"] != int64(42) {
		t.Errorf("author.id = %v, want 42", author["id"])
	}
}

func TestReassembleData_BooleanConversion(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "items",
		Columns: []ColumnDef{
			{Name: "active", JSONField: "active", Type: "boolean"},
		},
	}

	// SQLite returns booleans as int64
	result := ReassembleData(spec, []interface{}{int64(1)}, nil, 1, 1)
	if result["active"] != true {
		t.Errorf("active = %v (%T), want true", result["active"], result["active"])
	}

	result = ReassembleData(spec, []interface{}{int64(0)}, nil, 2, 1)
	if result["active"] != false {
		t.Errorf("active = %v (%T), want false", result["active"], result["active"])
	}
}

func TestReassembleData_JSONArrayColumn(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "items",
		Columns: []ColumnDef{
			{Name: "tags", JSONField: "tags", Type: "array"},
		},
	}

	result := ReassembleData(spec, []interface{}{`["go","rust"]`}, nil, 1, 1)

	tags, ok := result["tags"].([]interface{})
	if !ok {
		t.Fatalf("tags is not []interface{}, got %T: %v", result["tags"], result["tags"])
	}
	if len(tags) != 2 || tags[0] != "go" || tags[1] != "rust" {
		t.Errorf("tags = %v, want [go, rust]", tags)
	}
}

func TestPartitionReassembleRoundTrip(t *testing.T) {
	spec := &AdaptedTableSpec{
		Entity: "full",
		Columns: []ColumnDef{
			{Name: "REF_owner_entity", JSONField: "owner", Type: "string", Format: "ref", IsREF: true},
			{Name: "REF_owner_id", JSONField: "owner", Type: "integer", Format: "ref", IsREF: true},
			{Name: "active", JSONField: "active", Type: "boolean"},
			{Name: "name", JSONField: "name", Type: "string"},
			{Name: "score", JSONField: "score", Type: "number"},
		},
		HasExtra: true,
	}

	original := map[string]interface{}{
		"id":    1,
		"name":  "Test",
		"score": float64(99.5),
		"active": true,
		"owner": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     float64(7),
		},
		"extra_field": "bonus",
	}

	colVals, extra := PartitionData(spec, original)

	// Simulate what SQLite would return (booleans as int64, ints as int64)
	sqlColVals := make([]interface{}, len(colVals))
	for i, v := range colVals {
		switch tv := v.(type) {
		case bool:
			if tv {
				sqlColVals[i] = int64(1)
			} else {
				sqlColVals[i] = int64(0)
			}
		case int:
			sqlColVals[i] = int64(tv)
		case float64:
			sqlColVals[i] = tv
		default:
			sqlColVals[i] = v
		}
	}

	result := ReassembleData(spec, sqlColVals, extra, 1, 1)

	// Check scalar fields
	if result["name"] != "Test" {
		t.Errorf("name = %v, want Test", result["name"])
	}
	if result["score"] != float64(99.5) {
		t.Errorf("score = %v, want 99.5", result["score"])
	}
	if result["active"] != true {
		t.Errorf("active = %v, want true", result["active"])
	}

	// Check REF field
	owner, ok := result["owner"].(map[string]interface{})
	if !ok {
		t.Fatalf("owner is not a map, got %T", result["owner"])
	}
	if owner["entity"] != "users" {
		t.Errorf("owner.entity = %v, want users", owner["entity"])
	}

	// Check overflow
	if result["extra_field"] != "bonus" {
		t.Errorf("extra_field = %v, want bonus", result["extra_field"])
	}
}

// ---------------------------------------------------------------------------
// FieldToColumn / IsSchemaField tests
// ---------------------------------------------------------------------------

func TestFieldToColumn(t *testing.T) {
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "REF_author_entity", JSONField: "author", IsREF: true},
			{Name: "REF_author_id", JSONField: "author", IsREF: true},
			{Name: "name", JSONField: "name"},
		},
	}

	cols := spec.FieldToColumn("author")
	if len(cols) != 2 {
		t.Fatalf("FieldToColumn(author) returned %d columns, want 2", len(cols))
	}

	cols = spec.FieldToColumn("name")
	if len(cols) != 1 || cols[0] != "name" {
		t.Errorf("FieldToColumn(name) = %v, want [name]", cols)
	}

	cols = spec.FieldToColumn("nonexistent")
	if len(cols) != 0 {
		t.Errorf("FieldToColumn(nonexistent) = %v, want []", cols)
	}
}

func TestIsSchemaField(t *testing.T) {
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name"},
			{Name: "REF_author_entity", JSONField: "author", IsREF: true},
		},
	}

	if !spec.IsSchemaField("name") {
		t.Error("IsSchemaField(name) = false, want true")
	}
	if !spec.IsSchemaField("author") {
		t.Error("IsSchemaField(author) = false, want true")
	}
	if spec.IsSchemaField("unknown") {
		t.Error("IsSchemaField(unknown) = true, want false")
	}
}

// ---------------------------------------------------------------------------
// Schema hash stability test
// ---------------------------------------------------------------------------

func TestCanonicalSchemaHash_Deterministic(t *testing.T) {
	// Go's json.Marshal sorts map keys, so insertion order shouldn't matter
	schema1 := map[string]interface{}{
		"properties": map[string]interface{}{
			"b": map[string]interface{}{"type": "string"},
			"a": map[string]interface{}{"type": "integer"},
		},
	}

	schema2 := map[string]interface{}{
		"properties": map[string]interface{}{
			"a": map[string]interface{}{"type": "integer"},
			"b": map[string]interface{}{"type": "string"},
		},
	}

	h1, err := canonicalSchemaHash(schema1)
	if err != nil {
		t.Fatal(err)
	}
	h2, err := canonicalSchemaHash(schema2)
	if err != nil {
		t.Fatal(err)
	}

	if h1 != h2 {
		t.Errorf("hashes differ for equivalent schemas: %s vs %s", h1, h2)
	}
}

// ---------------------------------------------------------------------------
// Metadata table DDL test
// ---------------------------------------------------------------------------

func TestGenerateAdaptedSchemasTableSQL(t *testing.T) {
	ddl := GenerateAdaptedSchemasTableSQL(&SQLiteStorageDialect{})
	if !strings.Contains(ddl, "adapted_table_schemas") {
		t.Error("DDL missing table name 'adapted_table_schemas'")
	}
	if !strings.Contains(ddl, "entity_type TEXT PRIMARY KEY") {
		t.Error("DDL missing primary key on entity_type")
	}
	if !strings.Contains(ddl, "schema_hash") {
		t.Error("DDL missing schema_hash column")
	}
	if !strings.Contains(ddl, "column_spec") {
		t.Error("DDL missing column_spec column")
	}
}

// ---------------------------------------------------------------------------
// JSON round-trip for ColumnDef (serialisation stability)
// ---------------------------------------------------------------------------

func TestColumnDef_JSONRoundTrip(t *testing.T) {
	original := ColumnDef{
		Name:      "amount",
		JSONField: "amount",
		Type:      "number",
		Format:    "decimal",
		SQLType:   "TEXT",
		Required:  true,
		Precision: 18,
		Scale:     4,
	}

	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}

	var restored ColumnDef
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}

	if restored != original {
		t.Errorf("round-trip mismatch:\n  got  %+v\n  want %+v", restored, original)
	}
}
