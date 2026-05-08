// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"testing"

	"github.com/ha1tch/queryfy/builders"
	"github.com/ha1tch/queryfy/builders/jsonschema"
)

// ---------------------------------------------------------------------------
// End-to-end: JSON Schema → queryfy → SchemaBrowser → AdaptedTableSpec
// ---------------------------------------------------------------------------
// This test proves the full pipeline: a JSON Schema document is parsed by
// queryfy's jsonschema.FromJSON, wrapped in SchemaBrowser, and fed into
// DeriveAdaptedTableSpecFrom to produce the same result as the raw-map path.
// ---------------------------------------------------------------------------

func TestSchemaBrowser_EndToEnd_DeriveSpec(t *testing.T) {
	jsonDoc := []byte(`{
		"type": "object",
		"properties": {
			"name":   {"type": "string"},
			"age":    {"type": "integer"},
			"score":  {"type": "number"},
			"active": {"type": "boolean"},
			"tags":   {"type": "array", "items": {"type": "string"}},
			"status": {"type": "string", "enum": ["active", "inactive"]}
		},
		"required": ["name", "age"]
	}`)

	schema, errs := jsonschema.FromJSON(jsonDoc, &jsonschema.Options{
		StoreUnknown: true,
	})
	for _, e := range errs {
		if !e.IsWarning {
			t.Fatalf("conversion error: %s: %s", e.Path, e.Message)
		}
	}

	obj, ok := schema.(*builders.ObjectSchema)
	if !ok {
		t.Fatalf("expected *ObjectSchema, got %T", schema)
	}

	browser := NewSchemaBrowser(obj)
	if browser == nil {
		t.Fatal("NewSchemaBrowser returned nil")
	}

	dialect := &SQLiteStorageDialect{}
	spec, err := DeriveAdaptedTableSpecFrom("products", browser, dialect, "test-hash")
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpecFrom failed: %v", err)
	}

	// Verify entity name
	if spec.Entity != "products" {
		t.Errorf("entity = %q, want 'products'", spec.Entity)
	}

	// Verify column count (6 fields, no id)
	if len(spec.Columns) != 6 {
		t.Errorf("expected 6 columns, got %d", len(spec.Columns))
		for _, col := range spec.Columns {
			t.Logf("  column: %s (%s)", col.Name, col.SQLType)
		}
	}

	// Verify column types by name
	colMap := make(map[string]ColumnDef)
	for _, col := range spec.Columns {
		colMap[col.Name] = col
	}

	checks := []struct {
		name    string
		sqlType string
		req     bool
	}{
		{"name", "TEXT", true},
		{"age", "INTEGER", true},
		{"score", "REAL", false},
		{"active", "INTEGER", false}, // SQLite: boolean → INTEGER
		{"tags", "TEXT", false},       // array → TEXT (JSON)
		{"status", "TEXT", false},
	}

	for _, c := range checks {
		col, ok := colMap[c.name]
		if !ok {
			t.Errorf("missing column %q", c.name)
			continue
		}
		if col.SQLType != c.sqlType {
			t.Errorf("column %q: SQLType = %q, want %q", c.name, col.SQLType, c.sqlType)
		}
		if col.Required != c.req {
			t.Errorf("column %q: Required = %v, want %v", c.name, col.Required, c.req)
		}
	}

	// Verify HasExtra (additionalProperties not set → true)
	if !spec.HasExtra {
		t.Error("expected HasExtra = true (default)")
	}

	// Verify schema hash passed through
	if spec.SchemaHash != "test-hash" {
		t.Errorf("SchemaHash = %q, want 'test-hash'", spec.SchemaHash)
	}
}

func TestSchemaBrowser_EndToEnd_DecimalWithMeta(t *testing.T) {
	// Decimal fields use type:"string" format:"decimal" in JSON Schema.
	// The string representation preserves exact precision; JSON number
	// would lose it to IEEE 754 float approximation.
	jsonDoc := []byte(`{
		"type": "object",
		"properties": {
			"amount": {
				"type": "string",
				"format": "decimal",
				"decimalPrecision": 12,
				"decimalScale": 2
			}
		},
		"required": ["amount"]
	}`)

	schema, errs := jsonschema.FromJSON(jsonDoc, &jsonschema.Options{
		StoreUnknown: true,
	})
	for _, e := range errs {
		if !e.IsWarning {
			t.Fatalf("conversion error: %s: %s", e.Path, e.Message)
		}
	}

	obj, ok := schema.(*builders.ObjectSchema)
	if !ok {
		t.Fatalf("expected *ObjectSchema, got %T", schema)
	}

	browser := NewSchemaBrowser(obj)
	dialect := &SQLiteStorageDialect{}

	spec, err := DeriveAdaptedTableSpecFrom("transactions", browser, dialect, "test-hash")
	if err != nil {
		t.Fatalf("DeriveAdaptedTableSpecFrom failed: %v", err)
	}

	if len(spec.Columns) != 1 {
		t.Fatalf("expected 1 column, got %d", len(spec.Columns))
	}

	col := spec.Columns[0]
	if col.Name != "amount" {
		t.Errorf("column name = %q, want 'amount'", col.Name)
	}
	if col.SQLType != "INTEGER" {
		t.Errorf("SQLType = %q, want INTEGER (decimal stored as scaled integer in SQLite)", col.SQLType)
	}
	if col.Precision != 12 {
		t.Errorf("Precision = %d, want 12", col.Precision)
	}
	if col.Scale != 2 {
		t.Errorf("Scale = %d, want 2", col.Scale)
	}
	if !col.Required {
		t.Error("expected Required = true")
	}
}

func TestSchemaBrowser_EndToEnd_NoAdditionalProperties(t *testing.T) {
	jsonDoc := []byte(`{
		"type": "object",
		"properties": {
			"x": {"type": "string"}
		},
		"additionalProperties": false
	}`)

	schema, _ := jsonschema.FromJSON(jsonDoc, &jsonschema.Options{
		StoreUnknown: true,
	})

	obj := schema.(*builders.ObjectSchema)
	browser := NewSchemaBrowser(obj)
	dialect := &SQLiteStorageDialect{}

	spec, err := DeriveAdaptedTableSpecFrom("strict", browser, dialect, "test-hash")
	if err != nil {
		t.Fatalf("failed: %v", err)
	}

	if spec.HasExtra {
		t.Error("expected HasExtra = false when additionalProperties = false")
	}
}

func TestSchemaBrowser_EndToEnd_MatchesRawAdapter(t *testing.T) {
	// The same JSON Schema should produce identical specs whether parsed
	// through the raw-map adapter or through queryfy → SchemaBrowser.
	rawSchema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"count": map[string]interface{}{"type": "integer"},
			"price": map[string]interface{}{"type": "number"},
		},
		"required": []interface{}{"name"},
	}

	dialect := &SQLiteStorageDialect{}

	// Path 1: raw map adapter
	specRaw, err := DeriveAdaptedTableSpec("items", rawSchema, dialect)
	if err != nil {
		t.Fatalf("raw path failed: %v", err)
	}

	// Path 2: queryfy → SchemaBrowser
	jsonDoc := []byte(`{
		"type": "object",
		"properties": {
			"name":  {"type": "string"},
			"count": {"type": "integer"},
			"price": {"type": "number"}
		},
		"required": ["name"]
	}`)

	schema, _ := jsonschema.FromJSON(jsonDoc, &jsonschema.Options{StoreUnknown: true})
	obj := schema.(*builders.ObjectSchema)
	browser := NewSchemaBrowser(obj)

	specBrowser, err := DeriveAdaptedTableSpecFrom("items", browser, dialect, specRaw.SchemaHash)
	if err != nil {
		t.Fatalf("browser path failed: %v", err)
	}

	// Compare column count
	if len(specRaw.Columns) != len(specBrowser.Columns) {
		t.Fatalf("column count mismatch: raw=%d, browser=%d",
			len(specRaw.Columns), len(specBrowser.Columns))
	}

	// Compare each column
	for i, rawCol := range specRaw.Columns {
		browCol := specBrowser.Columns[i]
		if rawCol.Name != browCol.Name {
			t.Errorf("column[%d] name: raw=%q, browser=%q", i, rawCol.Name, browCol.Name)
		}
		if rawCol.SQLType != browCol.SQLType {
			t.Errorf("column[%d] %q SQLType: raw=%q, browser=%q", i, rawCol.Name, rawCol.SQLType, browCol.SQLType)
		}
		if rawCol.Required != browCol.Required {
			t.Errorf("column[%d] %q Required: raw=%v, browser=%v", i, rawCol.Name, rawCol.Required, browCol.Required)
		}
		if rawCol.Type != browCol.Type {
			t.Errorf("column[%d] %q Type: raw=%q, browser=%q", i, rawCol.Name, rawCol.Type, browCol.Type)
		}
	}

	// Compare HasExtra
	if specRaw.HasExtra != specBrowser.HasExtra {
		t.Errorf("HasExtra: raw=%v, browser=%v", specRaw.HasExtra, specBrowser.HasExtra)
	}
}
