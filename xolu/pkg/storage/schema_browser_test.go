// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"testing"

	"github.com/ha1tch/queryfy/builders"
)

// ---------------------------------------------------------------------------
// SchemaBrowser tests
// ---------------------------------------------------------------------------
// These mirror the jsonSchemaAdapter tests to confirm both implementations
// satisfy SchemaIntrospector identically.
// ---------------------------------------------------------------------------

func TestSchemaBrowser_FieldNames(t *testing.T) {
	obj := builders.Object().
		Field("name", builders.String()).
		Field("age", builders.Number()).
		Field("email", builders.String().Email())

	browser := NewSchemaBrowser(obj)
	if browser == nil {
		t.Fatal("expected non-nil browser")
	}

	names := browser.FieldNames()
	if len(names) != 3 {
		t.Fatalf("expected 3 fields, got %d", len(names))
	}
	if names[0] != "age" || names[1] != "email" || names[2] != "name" {
		t.Errorf("expected sorted [age email name], got %v", names)
	}
}

func TestSchemaBrowser_GetField_Types(t *testing.T) {
	obj := builders.Object().
		Field("title", builders.String()).
		Field("count", builders.Number().Integer()).
		Field("price", builders.Number()).
		Field("active", builders.Bool()).
		Field("tags", builders.Array())

	browser := NewSchemaBrowser(obj)

	tests := []struct {
		name     string
		wantType string
	}{
		{"title", "string"},
		{"count", "integer"},
		{"price", "number"},
		{"active", "boolean"},
		{"tags", "array"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := browser.GetField(tt.name)
			if field == nil {
				t.Fatalf("GetField(%q) returned nil", tt.name)
			}
			if got := field.JSONType(); got != tt.wantType {
				t.Errorf("JSONType() = %q, want %q", got, tt.wantType)
			}
		})
	}
}

func TestSchemaBrowser_GetFieldMissing(t *testing.T) {
	obj := builders.Object().
		Field("name", builders.String())

	browser := NewSchemaBrowser(obj)
	if browser.GetField("nonexistent") != nil {
		t.Error("expected nil for missing field")
	}
}

func TestSchemaBrowser_IsRequired(t *testing.T) {
	obj := builders.Object().
		Field("name", builders.String().Required()).
		Field("email", builders.String())

	browser := NewSchemaBrowser(obj)

	if !browser.IsRequired("name") {
		t.Error("expected name to be required")
	}
	if browser.IsRequired("email") {
		t.Error("expected email to not be required")
	}
	if browser.IsRequired("nonexistent") {
		t.Error("expected nonexistent to not be required")
	}
}

func TestSchemaBrowser_AllowsAdditional(t *testing.T) {
	t.Run("default (no policy)", func(t *testing.T) {
		obj := builders.Object().
			Field("x", builders.String())
		browser := NewSchemaBrowser(obj)
		if !browser.AllowsAdditional() {
			t.Error("expected true when no policy set")
		}
	})

	t.Run("explicit true", func(t *testing.T) {
		obj := builders.Object().
			Field("x", builders.String()).
			AllowAdditional(true)
		browser := NewSchemaBrowser(obj)
		if !browser.AllowsAdditional() {
			t.Error("expected true")
		}
	})

	t.Run("explicit false", func(t *testing.T) {
		obj := builders.Object().
			Field("x", builders.String()).
			AllowAdditional(false)
		browser := NewSchemaBrowser(obj)
		if browser.AllowsAdditional() {
			t.Error("expected false")
		}
	})
}

func TestSchemaBrowser_FormatType(t *testing.T) {
	obj := builders.Object().
		Field("email_field", builders.String().Email()).
		Field("url_field", builders.String().URL()).
		Field("uuid_field", builders.String().UUID()).
		Field("plain_field", builders.String())

	browser := NewSchemaBrowser(obj)

	tests := []struct {
		name       string
		wantFormat string
	}{
		{"email_field", "email"},
		{"url_field", "url"},
		{"uuid_field", "uuid"},
		{"plain_field", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			field := browser.GetField(tt.name)
			if got := field.Format(); got != tt.wantFormat {
				t.Errorf("Format() = %q, want %q", got, tt.wantFormat)
			}
		})
	}
}

func TestSchemaBrowser_MetaFormat(t *testing.T) {
	// Custom formats (decimal, ref) are stored as Meta("format", ...)
	obj := builders.Object().
		Field("amount", builders.String().Meta("format", "decimal")).
		Field("author", builders.String().Meta("format", "ref"))

	browser := NewSchemaBrowser(obj)

	amount := browser.GetField("amount")
	if got := amount.Format(); got != "decimal" {
		t.Errorf("expected format 'decimal', got %q", got)
	}

	author := browser.GetField("author")
	if got := author.Format(); got != "ref" {
		t.Errorf("expected format 'ref', got %q", got)
	}
}

func TestSchemaBrowser_Meta(t *testing.T) {
	obj := builders.Object().
		Field("price", builders.Number().
			Meta("decimalPrecision", float64(12)).
			Meta("decimalScale", float64(2)))

	browser := NewSchemaBrowser(obj)
	field := browser.GetField("price")

	p, ok := field.Meta("decimalPrecision")
	if !ok || p.(float64) != 12 {
		t.Errorf("expected decimalPrecision=12, got %v (ok=%v)", p, ok)
	}

	s, ok := field.Meta("decimalScale")
	if !ok || s.(float64) != 2 {
		t.Errorf("expected decimalScale=2, got %v (ok=%v)", s, ok)
	}

	_, ok = field.Meta("nonexistent")
	if ok {
		t.Error("expected false for missing meta key")
	}
}

func TestSchemaBrowser_EnumValues(t *testing.T) {
	obj := builders.Object().
		Field("status", builders.String().Enum("active", "inactive", "pending")).
		Field("name", builders.String())

	browser := NewSchemaBrowser(obj)

	status := browser.GetField("status")
	vals := status.EnumValues()
	if len(vals) != 3 {
		t.Fatalf("expected 3 enum values, got %d", len(vals))
	}

	name := browser.GetField("name")
	if name.EnumValues() != nil {
		t.Error("expected nil enum values for non-enum field")
	}
}

func TestSchemaBrowser_NilInput(t *testing.T) {
	if NewSchemaBrowser(nil) != nil {
		t.Error("expected nil for nil input")
	}
}

func TestSchemaBrowser_NestedObject(t *testing.T) {
	obj := builders.Object().
		Field("address", builders.Object().
			Field("city", builders.String().Required()).
			Field("zip", builders.String()))

	browser := NewSchemaBrowser(obj)
	addr := browser.GetField("address")

	if addr.JSONType() != "object" {
		t.Errorf("expected type 'object', got %q", addr.JSONType())
	}
}
