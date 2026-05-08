// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import "testing"

// ---------------------------------------------------------------------------
// SchemaIntrospector / FieldIntrospector tests
// ---------------------------------------------------------------------------
// These tests verify the jsonSchemaAdapter implementation. When the queryfy
// adapter is written, an identical test suite should pass against it.
// ---------------------------------------------------------------------------

func TestJSONSchemaIntrospector_FieldNames(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"age":   map[string]interface{}{"type": "integer"},
			"email": map[string]interface{}{"type": "string", "format": "email"},
		},
	}

	intr := NewJSONSchemaIntrospector(schema)
	if intr == nil {
		t.Fatal("expected non-nil introspector")
	}

	names := intr.FieldNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(names))
	}
	// Should be sorted
	if names[0] != "age" || names[1] != "email" || names[2] != "name" {
		t.Errorf("expected sorted [age email name], got %v", names)
	}
}

func TestJSONSchemaIntrospector_GetField(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"price": map[string]interface{}{
				"type":             "number",
				"format":           "decimal",
				"decimalPrecision": float64(12),
				"decimalScale":     float64(2),
			},
		},
	}

	intr := NewJSONSchemaIntrospector(schema)
	field := intr.GetField("price")
	if field == nil {
		t.Fatal("expected non-nil field")
	}

	if field.JSONType() != "number" {
		t.Errorf("expected type 'number', got %q", field.JSONType())
	}
	if field.Format() != "decimal" {
		t.Errorf("expected format 'decimal', got %q", field.Format())
	}

	p, ok := field.Meta("decimalPrecision")
	if !ok || p.(float64) != 12 {
		t.Errorf("expected decimalPrecision=12, got %v (ok=%v)", p, ok)
	}

	s, ok := field.Meta("decimalScale")
	if !ok || s.(float64) != 2 {
		t.Errorf("expected decimalScale=2, got %v (ok=%v)", s, ok)
	}
}

func TestJSONSchemaIntrospector_GetFieldMissing(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
	}

	intr := NewJSONSchemaIntrospector(schema)
	if intr.GetField("nonexistent") != nil {
		t.Error("expected nil for missing field")
	}
}

func TestJSONSchemaIntrospector_IsRequired(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"email": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"name"},
	}

	intr := NewJSONSchemaIntrospector(schema)
	if !intr.IsRequired("name") {
		t.Error("expected name to be required")
	}
	if intr.IsRequired("email") {
		t.Error("expected email to not be required")
	}
	if intr.IsRequired("nonexistent") {
		t.Error("expected nonexistent to not be required")
	}
}

func TestJSONSchemaIntrospector_AllowsAdditional(t *testing.T) {
	tests := []struct {
		name   string
		schema map[string]interface{}
		want   bool
	}{
		{
			name: "default (absent)",
			schema: map[string]interface{}{
				"properties": map[string]interface{}{
					"x": map[string]interface{}{"type": "string"},
				},
			},
			want: true,
		},
		{
			name: "explicit true",
			schema: map[string]interface{}{
				"properties":           map[string]interface{}{"x": map[string]interface{}{"type": "string"}},
				"additionalProperties": true,
			},
			want: true,
		},
		{
			name: "explicit false",
			schema: map[string]interface{}{
				"properties":           map[string]interface{}{"x": map[string]interface{}{"type": "string"}},
				"additionalProperties": false,
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			intr := NewJSONSchemaIntrospector(tt.schema)
			if got := intr.AllowsAdditional(); got != tt.want {
				t.Errorf("AllowsAdditional() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestJSONSchemaIntrospector_EnumValues(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"status": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"active", "inactive", "pending"},
			},
			"name": map[string]interface{}{
				"type": "string",
			},
		},
	}

	intr := NewJSONSchemaIntrospector(schema)

	status := intr.GetField("status")
	vals := status.EnumValues()
	if len(vals) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(vals))
	}
	if vals[0] != "active" || vals[1] != "inactive" || vals[2] != "pending" {
		t.Errorf("unexpected enum values: %v", vals)
	}

	name := intr.GetField("name")
	if name.EnumValues() != nil {
		t.Error("expected nil enum values for non-enum field")
	}
}

func TestJSONSchemaIntrospector_MetaMissing(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"x": map[string]interface{}{"type": "string"},
		},
	}

	intr := NewJSONSchemaIntrospector(schema)
	field := intr.GetField("x")
	_, ok := field.Meta("nonexistent-key")
	if ok {
		t.Error("expected Meta to return false for missing key")
	}
}

func TestJSONSchemaIntrospector_NilForNoProperties(t *testing.T) {
	schema := map[string]interface{}{
		"type": "object",
	}
	if NewJSONSchemaIntrospector(schema) != nil {
		t.Error("expected nil for schema with no properties")
	}
}

func TestJSONSchemaIntrospector_REFFormat(t *testing.T) {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"author": map[string]interface{}{
				"type":   "object",
				"format": "ref",
			},
		},
	}

	intr := NewJSONSchemaIntrospector(schema)
	field := intr.GetField("author")
	if field.Format() != "ref" {
		t.Errorf("expected format 'ref', got %q", field.Format())
	}
	if field.JSONType() != "object" {
		t.Errorf("expected type 'object', got %q", field.JSONType())
	}
}
