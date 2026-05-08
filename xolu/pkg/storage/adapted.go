// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Adapted table metadata
// ---------------------------------------------------------------------------
// This file implements the schema-to-DDL layer for adapted tables.
// It derives native SQLite column definitions from JSON Schema documents,
// manages a metadata table (adapted_table_schemas) that tracks which entity
// types have adapted tables and what their column layout is, and provides
// the schema change detection logic used at startup and schema registration.
//
// The design is backend-portable: all SQL generation is delegated to the
// SQLDialect interface (defined in pkg/oql/sqlgen.go and extended here).
// ---------------------------------------------------------------------------

// ColumnDef describes a single column in an adapted table.
type ColumnDef struct {
	Name      string `json:"name"`       // Column name (e.g., "age", "REF_author_entity")
	JSONField string `json:"json_field"` // Original JSON field name (e.g., "age", "author")
	Type      string `json:"type"`       // JSON Schema type: string, integer, number, boolean, array, object
	Format    string `json:"format"`     // JSON Schema format: "", "decimal", "ref", "email", etc.
	SQLType   string `json:"sql_type"`   // Backend-specific SQL type (e.g., "TEXT", "INTEGER", "REAL")
	Required  bool   `json:"required"`   // Whether the field is in the schema's required array
	Precision int    `json:"precision"`  // For decimal: total significant digits
	Scale     int    `json:"scale"`      // For decimal: digits after decimal point
	IsREF     bool   `json:"is_ref"`     // True if this column is part of a REF decomposition
}

// AdaptedTableSpec describes the full column layout of an adapted table.
type AdaptedTableSpec struct {
	Entity     string      `json:"entity"`      // Entity type name
	Columns    []ColumnDef `json:"columns"`      // Ordered column definitions
	SchemaHash string      `json:"schema_hash"`  // SHA-256 of canonical schema JSON
	HasExtra   bool        `json:"has_extra"`    // Whether _extra overflow column is present
	Indexes    []IndexDef  `json:"indexes"`      // Indexes to create
}

// IndexDef describes an index on an adapted table.
type IndexDef struct {
	Name    string   `json:"name"`    // Index name
	Columns []string `json:"columns"` // Column names
	Unique  bool     `json:"unique"`  // Whether the index is unique
}

// TableName returns the SQL table name for this adapted table.
func (s *AdaptedTableSpec) TableName() string {
	return "olu_" + s.Entity
}

// ColumnNames returns all column names in order (excluding system columns).
func (s *AdaptedTableSpec) ColumnNames() []string {
	names := make([]string, len(s.Columns))
	for i, col := range s.Columns {
		names[i] = col.Name
	}
	return names
}

// FieldToColumn maps a JSON field name to its column name(s).
// For REF fields, this returns two names: REF_{field}_entity, REF_{field}_id.
// For all other fields, it returns a single name equal to the field name.
func (s *AdaptedTableSpec) FieldToColumn(jsonField string) []string {
	var cols []string
	for _, col := range s.Columns {
		if col.JSONField == jsonField {
			cols = append(cols, col.Name)
		}
	}
	return cols
}

// IsSchemaField reports whether a JSON field name is a declared schema field.
func (s *AdaptedTableSpec) IsSchemaField(jsonField string) bool {
	for _, col := range s.Columns {
		if col.JSONField == jsonField {
			return true
		}
	}
	return false
}

// ColumnByName returns the ColumnDef for a given SQL column name.
func (s *AdaptedTableSpec) ColumnByName(name string) (ColumnDef, bool) {
	for _, col := range s.Columns {
		if col.Name == name {
			return col, true
		}
	}
	return ColumnDef{}, false
}

// ---------------------------------------------------------------------------
// Schema-to-column derivation
// ---------------------------------------------------------------------------

// DeriveAdaptedTableSpec examines a JSON Schema document and produces a
// complete AdaptedTableSpec describing the adapted table layout.
//
// The dialect parameter determines backend-specific column types.
//
// This is a convenience wrapper that creates a SchemaIntrospector from
// the raw JSON Schema map. For direct use with queryfy (future), call
// DeriveAdaptedTableSpecFrom with a queryfy-backed introspector.
func DeriveAdaptedTableSpec(entity string, schema map[string]interface{}, dialect StorageDialect) (*AdaptedTableSpec, error) {
	introspector := NewJSONSchemaIntrospector(schema)
	if introspector == nil {
		return nil, fmt.Errorf("schema for %q has no properties", entity)
	}

	// Schema hash still computed from the raw map for determinism
	schemaHash, err := canonicalSchemaHash(schema)
	if err != nil {
		return nil, fmt.Errorf("failed to hash schema for %q: %w", entity, err)
	}

	return DeriveAdaptedTableSpecFrom(entity, introspector, dialect, schemaHash)
}

// DeriveAdaptedTableSpecFrom derives an AdaptedTableSpec from a
// SchemaIntrospector. This is the backend-agnostic core that works
// with any schema representation (JSON Schema maps, queryfy objects,
// or anything else that implements SchemaIntrospector).
func DeriveAdaptedTableSpecFrom(entity string, schema SchemaIntrospector, dialect StorageDialect, schemaHash string) (*AdaptedTableSpec, error) {
	hasExtra := schema.AllowsAdditional()

	// Get field names (already sorted), excluding system columns
	allFields := schema.FieldNames()
	fieldNames := make([]string, 0, len(allFields))
	for _, name := range allFields {
		if name == "id" {
			continue // id is a system column, not a schema column
		}
		fieldNames = append(fieldNames, name)
	}

	var columns []ColumnDef
	var indexes []IndexDef

	for _, fieldName := range fieldNames {
		field := schema.GetField(fieldName)
		if field == nil {
			continue
		}

		jsonType := field.JSONType()
		format := field.Format()
		required := schema.IsRequired(fieldName)

		precision := 18 // default decimal precision
		scale := 4      // default decimal scale
		if p, ok := field.Meta("decimalPrecision"); ok {
			if pf, ok := p.(float64); ok {
				precision = int(pf)
			}
		}
		if s, ok := field.Meta("decimalScale"); ok {
			if sf, ok := s.(float64); ok {
				scale = int(sf)
			}
		}

		if format == "ref" {
			// REF fields decompose into two columns
			columns = append(columns, ColumnDef{
				Name:      "REF_" + fieldName + "_entity",
				JSONField: fieldName,
				Type:      "string",
				Format:    "ref",
				SQLType:   dialect.ColumnType("string", "", 0, 0),
				Required:  required,
				IsREF:     true,
			})
			columns = append(columns, ColumnDef{
				Name:      "REF_" + fieldName + "_id",
				JSONField: fieldName,
				Type:      "integer",
				Format:    "ref",
				SQLType:   dialect.ColumnType("integer", "", 0, 0),
				Required:  required,
				IsREF:     true,
			})
			// Index on _id column for join lookups
			indexes = append(indexes, IndexDef{
				Name:    fmt.Sprintf("idx_olu_%s_ref_%s", entity, fieldName),
				Columns: []string{"REF_" + fieldName + "_id"},
			})
		} else {
			sqlType := dialect.ColumnType(jsonType, format, precision, scale)
			columns = append(columns, ColumnDef{
				Name:      fieldName,
				JSONField: fieldName,
				Type:      jsonType,
				Format:    format,
				SQLType:   sqlType,
				Required:  required,
				Precision: precision,
				Scale:     scale,
			})

			// Auto-index heuristics
			enumVals := field.EnumValues()
			if shouldAutoIndexField(fieldName, jsonType, format, required, enumVals) {
				indexes = append(indexes, IndexDef{
					Name:    fmt.Sprintf("idx_olu_%s_%s", entity, fieldName),
					Columns: []string{fieldName},
				})
			}
		}

		// Check for explicit index override
		if idx, ok := field.Meta("x-olu-index"); ok {
			if b, ok := idx.(bool); ok && b {
				indexes = append(indexes, IndexDef{
					Name:    fmt.Sprintf("idx_olu_%s_%s", entity, fieldName),
					Columns: []string{fieldName},
				})
			}
		}
	}

	return &AdaptedTableSpec{
		Entity:     entity,
		Columns:    columns,
		SchemaHash: schemaHash,
		HasExtra:   hasExtra,
		Indexes:    deduplicateIndexes(indexes),
	}, nil
}

// shouldAutoIndex determines whether a field should receive an automatic
// index based on schema heuristics.
// shouldAutoIndexField determines whether a field should be automatically
// indexed based on its type, format, and constraints. This is the
// interface-friendly version used by DeriveAdaptedTableSpecFrom.
func shouldAutoIndexField(name, jsonType, format string, required bool, enumVals []string) bool {
	// Decimal fields: likely used in range queries
	if format == "decimal" {
		return true
	}
	// Enum fields: low cardinality, often filtered on
	if len(enumVals) > 0 {
		return true
	}
	// Required string fields: likely identifiers
	// (Pattern check removed — not available via EnumValues alone.
	//  When queryfy introspection lands, we can add PatternString() check.)
	return false
}


// canonicalSchemaHash produces a deterministic SHA-256 hash of a JSON Schema.
// The schema is re-serialised with sorted keys to ensure stability.
func canonicalSchemaHash(schema map[string]interface{}) (string, error) {
	// json.Marshal sorts map keys deterministically in Go
	canonical, err := json.Marshal(schema)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(canonical)
	return fmt.Sprintf("%x", hash), nil
}

// deduplicateIndexes removes duplicate index definitions (same columns).
func deduplicateIndexes(indexes []IndexDef) []IndexDef {
	seen := make(map[string]bool)
	var result []IndexDef
	for _, idx := range indexes {
		key := strings.Join(idx.Columns, ",")
		if !seen[key] {
			seen[key] = true
			result = append(result, idx)
		}
	}
	return result
}

// ---------------------------------------------------------------------------
// DDL generation
// ---------------------------------------------------------------------------

// GenerateCreateTableSQL produces the CREATE TABLE statement for an adapted
// table using the given dialect.
func GenerateCreateTableSQL(spec *AdaptedTableSpec, dialect StorageDialect) string {
	return dialect.CreateTableSQL(spec)
}

// GenerateIndexSQL produces CREATE INDEX statements for the adapted table
// using the given dialect.
func GenerateIndexSQL(spec *AdaptedTableSpec, dialect StorageDialect) []string {
	return dialect.CreateIndexSQL(spec)
}

// GenerateAdaptedSchemasTableSQL returns the DDL for the metadata table
// that tracks adapted table schemas, using the given dialect.
func GenerateAdaptedSchemasTableSQL(dialect StorageDialect) string {
	return dialect.MetadataTableSQL()
}

// ---------------------------------------------------------------------------
// Data partitioning (decompose map into columns + overflow)
// ---------------------------------------------------------------------------

// PartitionData separates a data map into schema-column values and overflow.
// Returns:
//   - columnValues: ordered values matching spec.Columns, ready for INSERT
//   - extra: map of fields not in the schema (nil if none or !hasExtra)
//
// REF fields are decomposed: {"type":"REF","entity":"users","id":42} becomes
// two column values: "users" (for REF_{field}_entity) and 42 (for REF_{field}_id).
func PartitionData(spec *AdaptedTableSpec, data map[string]interface{}) (columnValues []interface{}, extra map[string]interface{}) {
	// Build a set of known JSON field names for fast lookup
	knownFields := make(map[string]bool, len(spec.Columns))
	for _, col := range spec.Columns {
		knownFields[col.JSONField] = true
	}

	// Collect column values in order
	columnValues = make([]interface{}, len(spec.Columns))
	for i, col := range spec.Columns {
		if col.IsREF {
			// REF decomposition: extract entity/id from the REF object
			refObj, _ := data[col.JSONField].(map[string]interface{})
			if refObj == nil {
				columnValues[i] = nil
				continue
			}
			if strings.HasSuffix(col.Name, "_entity") {
				columnValues[i] = refObj["entity"]
			} else if strings.HasSuffix(col.Name, "_id") {
				switch v := refObj["id"].(type) {
				case float64:
					columnValues[i] = int(v)
				case int:
					columnValues[i] = v
				default:
					columnValues[i] = nil
				}
			}
		} else {
			columnValues[i] = data[col.JSONField]
		}
	}

	// Collect overflow fields
	if spec.HasExtra {
		for key, val := range data {
			if key == "id" || key == "_version" {
				continue
			}
			if !knownFields[key] {
				if extra == nil {
					extra = make(map[string]interface{})
				}
				extra[key] = val
			}
		}
	}

	return columnValues, extra
}

// ReassembleData reconstructs a map[string]interface{} from column values
// and an optional overflow map. This is the inverse of PartitionData.
func ReassembleData(spec *AdaptedTableSpec, columnValues []interface{}, extra map[string]interface{}, id int, version int) map[string]interface{} {
	result := make(map[string]interface{}, len(spec.Columns)+4)
	result["id"] = id
	result["_version"] = version

	// Track which JSON fields have been set (for REF reconstruction)
	refFields := make(map[string]map[string]interface{})

	for i, col := range spec.Columns {
		val := columnValues[i]
		if val == nil {
			continue
		}

		if col.IsREF {
			// Accumulate REF parts
			if refFields[col.JSONField] == nil {
				refFields[col.JSONField] = map[string]interface{}{
					"type": "REF",
				}
			}
			if strings.HasSuffix(col.Name, "_entity") {
				refFields[col.JSONField]["entity"] = val
			} else if strings.HasSuffix(col.Name, "_id") {
				refFields[col.JSONField]["id"] = val
			}
		} else if col.Type == "array" || (col.Type == "object" && col.Format != "ref") {
			// Deserialise JSON-stored columns
			if s, ok := val.(string); ok && s != "" {
				var parsed interface{}
				if err := json.Unmarshal([]byte(s), &parsed); err == nil {
					result[col.JSONField] = parsed
				} else {
					result[col.JSONField] = val
				}
			}
		} else if col.Type == "boolean" {
			// SQLite stores booleans as integers
			switch v := val.(type) {
			case int64:
				result[col.JSONField] = v != 0
			case int:
				result[col.JSONField] = v != 0
			case float64:
				result[col.JSONField] = v != 0
			default:
				result[col.JSONField] = val
			}
		} else {
			result[col.JSONField] = val
		}
	}

	// Merge assembled REF objects
	for field, refObj := range refFields {
		result[field] = refObj
	}

	// Merge overflow
	for k, v := range extra {
		result[k] = v
	}

	return result
}
