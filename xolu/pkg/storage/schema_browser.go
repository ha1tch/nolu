// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"github.com/ha1tch/queryfy"
	"github.com/ha1tch/queryfy/builders"
)

// ---------------------------------------------------------------------------
// SchemaBrowser — queryfy-backed schema introspection
// ---------------------------------------------------------------------------
// Implements SchemaIntrospector and FieldIntrospector by delegating to
// queryfy v0.3.0's introspection API. This is the preferred implementation
// when schemas have been converted to queryfy objects (via jsonschema.FromJSON
// or built programmatically with the builder API).
//
// The fallback implementation (jsonSchemaAdapter in schema_json_adapter.go)
// walks raw map[string]interface{} and requires no external dependency.
// ---------------------------------------------------------------------------

// SchemaBrowser implements SchemaIntrospector over a queryfy ObjectSchema.
type SchemaBrowser struct {
	obj *builders.ObjectSchema
}

// NewSchemaBrowser wraps a queryfy ObjectSchema for introspection.
// Returns nil if obj is nil.
func NewSchemaBrowser(obj *builders.ObjectSchema) SchemaIntrospector {
	if obj == nil {
		return nil
	}
	return &SchemaBrowser{obj: obj}
}

func (b *SchemaBrowser) FieldNames() []string {
	names := b.obj.FieldNames()
	// FieldNames() already returns sorted names
	return names
}

func (b *SchemaBrowser) GetField(name string) FieldIntrospector {
	schema, ok := b.obj.GetField(name)
	if !ok {
		return nil
	}
	return &fieldBrowser{schema: schema}
}

func (b *SchemaBrowser) IsRequired(name string) bool {
	for _, req := range b.obj.RequiredFieldNames() {
		if req == name {
			return true
		}
	}
	return false
}

func (b *SchemaBrowser) AllowsAdditional() bool {
	allow, explicit := b.obj.AllowsAdditional()
	if !explicit {
		return true // default: allow extra fields
	}
	return allow
}

// fieldBrowser implements FieldIntrospector over a queryfy Schema.
type fieldBrowser struct {
	schema queryfy.Schema
}

func (f *fieldBrowser) JSONType() string {
	st := f.schema.Type()

	switch st {
	case queryfy.TypeString:
		return "string"
	case queryfy.TypeNumber:
		// Distinguish integer from number via IsInteger()
		if ns, ok := f.schema.(*builders.NumberSchema); ok && ns.IsInteger() {
			return "integer"
		}
		return "number"
	case queryfy.TypeBool:
		return "boolean"
	case queryfy.TypeObject:
		return "object"
	case queryfy.TypeArray:
		return "array"
	default:
		return string(st)
	}
}

func (f *fieldBrowser) Format() string {
	// Check StringSchema.FormatType() first (email, url, uuid)
	if ss, ok := f.schema.(*builders.StringSchema); ok {
		ft := ss.FormatType()
		if ft != "" {
			return ft
		}
	}

	// Fall back to Meta("format") for custom formats (decimal, ref, etc.)
	if meta, ok := f.getMeta("format"); ok {
		if s, ok := meta.(string); ok {
			return s
		}
	}

	return ""
}

func (f *fieldBrowser) EnumValues() []string {
	if ss, ok := f.schema.(*builders.StringSchema); ok {
		return ss.EnumValues()
	}
	return nil
}

func (f *fieldBrowser) Meta(key string) (interface{}, bool) {
	return f.getMeta(key)
}

// getMeta retrieves metadata from the schema's BaseSchema.
func (f *fieldBrowser) getMeta(key string) (interface{}, bool) {
	// All queryfy schema types embed BaseSchema which has GetMeta()
	type metaGetter interface {
		GetMeta(string) (interface{}, bool)
	}
	if mg, ok := f.schema.(metaGetter); ok {
		return mg.GetMeta(key)
	}
	return nil, false
}
