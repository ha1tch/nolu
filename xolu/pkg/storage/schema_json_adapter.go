// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import "sort"

// ---------------------------------------------------------------------------
// JSON Schema adapter (current implementation)
// ---------------------------------------------------------------------------
// Wraps a raw JSON Schema map[string]interface{} to satisfy the
// SchemaIntrospector/FieldIntrospector interfaces. This is the bridge
// until queryfy v0.3.0 is integrated, at which point a queryfyAdapter
// replaces this with direct calls to the queryfy introspection API.
// ---------------------------------------------------------------------------

// jsonSchemaAdapter implements SchemaIntrospector over a raw JSON Schema.
type jsonSchemaAdapter struct {
	properties map[string]interface{}
	required   map[string]bool
	allowExtra bool
}

// NewJSONSchemaIntrospector wraps a parsed JSON Schema document.
// Returns nil if the schema has no "properties" key.
func NewJSONSchemaIntrospector(schema map[string]interface{}) SchemaIntrospector {
	props, ok := schema["properties"].(map[string]interface{})
	if !ok {
		return nil
	}

	// Build required set
	reqSet := make(map[string]bool)
	if reqList, ok := schema["required"].([]interface{}); ok {
		for _, r := range reqList {
			if s, ok := r.(string); ok {
				reqSet[s] = true
			}
		}
	}

	// Additional properties policy
	allowExtra := true
	if ap, ok := schema["additionalProperties"].(bool); ok {
		allowExtra = ap
	}

	return &jsonSchemaAdapter{
		properties: props,
		required:   reqSet,
		allowExtra: allowExtra,
	}
}

func (a *jsonSchemaAdapter) FieldNames() []string {
	names := make([]string, 0, len(a.properties))
	for name := range a.properties {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func (a *jsonSchemaAdapter) GetField(name string) FieldIntrospector {
	prop, ok := a.properties[name]
	if !ok {
		return nil
	}
	propMap, ok := prop.(map[string]interface{})
	if !ok {
		return nil
	}
	return &jsonFieldAdapter{propMap: propMap}
}

func (a *jsonSchemaAdapter) IsRequired(name string) bool {
	return a.required[name]
}

func (a *jsonSchemaAdapter) AllowsAdditional() bool {
	return a.allowExtra
}

// jsonFieldAdapter implements FieldIntrospector over a JSON Schema property.
type jsonFieldAdapter struct {
	propMap map[string]interface{}
}

func (f *jsonFieldAdapter) JSONType() string {
	if t, ok := f.propMap["type"].(string); ok {
		return t
	}
	return "string"
}

func (f *jsonFieldAdapter) Format() string {
	if fmt, ok := f.propMap["format"].(string); ok {
		return fmt
	}
	return ""
}

func (f *jsonFieldAdapter) EnumValues() []string {
	enumRaw, ok := f.propMap["enum"].([]interface{})
	if !ok {
		return nil
	}
	result := make([]string, 0, len(enumRaw))
	for _, v := range enumRaw {
		if s, ok := v.(string); ok {
			result = append(result, s)
		}
	}
	return result
}

func (f *jsonFieldAdapter) Meta(key string) (interface{}, bool) {
	// JSON Schema custom extensions are top-level keys on the property.
	// e.g., "decimalPrecision": 18, "x-olu-index": true
	v, ok := f.propMap[key]
	return v, ok
}
