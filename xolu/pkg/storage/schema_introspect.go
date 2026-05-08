// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

// ---------------------------------------------------------------------------
// Schema abstraction layer
// ---------------------------------------------------------------------------
// These interfaces define what the adapted table generator needs from a
// schema. They mirror the queryfy introspection API (v0.3.0) so that when
// queryfy is integrated, the concrete implementation can be swapped without
// changing any consumer code.
//
// Current implementation: jsonSchemaAdapter (walks map[string]interface{})
// Future implementation:  queryfySchemaAdapter (delegates to queryfy API)
// ---------------------------------------------------------------------------

// SchemaIntrospector provides read access to an object schema's structure.
// This is the primary interface consumed by DeriveAdaptedTableSpec.
type SchemaIntrospector interface {
	// FieldNames returns all declared field names, sorted alphabetically.
	FieldNames() []string

	// GetField returns the field descriptor for a named field.
	// Returns nil if the field does not exist.
	GetField(name string) FieldIntrospector

	// IsRequired reports whether a field is required.
	IsRequired(name string) bool

	// AllowsAdditional reports whether the schema accepts fields not
	// declared in its field list. Returns true if additionalProperties
	// is absent or true; false if explicitly set to false.
	AllowsAdditional() bool
}

// FieldIntrospector provides read access to a single field's type and
// constraints. This maps to queryfy's per-type schema introspection
// (StringSchema.FormatType, NumberSchema.RangeConstraints, etc.).
type FieldIntrospector interface {
	// JSONType returns the JSON Schema type string:
	// "string", "integer", "number", "boolean", "array", "object"
	JSONType() string

	// Format returns the declared format ("email", "decimal", "ref", "")
	Format() string

	// EnumValues returns the declared enum values, or nil if none.
	EnumValues() []string

	// Meta returns a metadata value by key (e.g., "decimalPrecision").
	// Returns (nil, false) if the key is not set.
	Meta(key string) (interface{}, bool)
}
