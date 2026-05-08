// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

// ---------------------------------------------------------------------------
// Storage dialect abstraction
// ---------------------------------------------------------------------------
// StorageDialect defines how to emit backend-specific SQL for adapted table
// operations. This is separate from the OQL SQLDialect (in pkg/oql) to
// avoid circular dependencies. The OQL dialect handles query generation;
// the StorageDialect handles DDL and CRUD for adapted tables.
//
// Each backend (SQLite, PostgreSQL, etc.) provides an implementation.
// ---------------------------------------------------------------------------

// StorageDialect defines backend-specific SQL generation for adapted tables.
type StorageDialect interface {
	// Name returns the dialect identifier ("sqlite", "postgres", etc.).
	Name() string

	// Placeholder returns a parameter placeholder for the n-th argument
	// (1-based). SQLite: "?", PostgreSQL: "$1", "$2", etc.
	Placeholder(n int) string

	// ColumnType maps a JSON Schema type + format to the backend's native
	// SQL column type. Examples:
	//   ("string", "")        → "TEXT"
	//   ("integer", "")       → "INTEGER" (SQLite) / "BIGINT" (PostgreSQL)
	//   ("number", "")        → "REAL" (SQLite) / "DOUBLE PRECISION" (PostgreSQL)
	//   ("number", "decimal") → "TEXT" (SQLite) / "NUMERIC(p,s)" (PostgreSQL)
	//   ("boolean", "")       → "INTEGER" (SQLite) / "BOOLEAN" (PostgreSQL)
	//   ("array", "")         → "TEXT" (SQLite) / "JSONB" (PostgreSQL)
	//   ("object", "")        → "TEXT" (SQLite) / "JSONB" (PostgreSQL)
	//
	// For decimals, precision and scale are provided for backends that
	// support native fixed-point (PostgreSQL NUMERIC). Backends that store
	// decimals as text (SQLite) may ignore them.
	ColumnType(jsonType, format string, precision, scale int) string

	// CreateTableSQL generates the CREATE TABLE statement for an adapted
	// table. Implementations must include system columns (id, tenant_id,
	// _extra if hasExtra, _version, created_at, updated_at) and the
	// primary key.
	CreateTableSQL(spec *AdaptedTableSpec) string

	// CreateIndexSQL generates CREATE INDEX statements for the adapted
	// table's indexes.
	CreateIndexSQL(spec *AdaptedTableSpec) []string

	// InsertSQL generates an INSERT statement with the appropriate
	// placeholders. Returns the SQL string and the expected argument
	// order (column names). The caller provides the actual values.
	InsertSQL(spec *AdaptedTableSpec, hasExtra bool) (sql string, columns []string)

	// SelectSQL generates a SELECT statement for a single row by
	// tenant_id and id.
	SelectSQL(spec *AdaptedTableSpec) string

	// SelectAllSQL generates a SELECT statement for all rows in a tenant,
	// ordered by id.
	SelectAllSQL(spec *AdaptedTableSpec) string

	// UpdateSQL generates an UPDATE statement with the appropriate
	// placeholders. The versionCheck parameter controls whether a
	// _version = ? clause is appended to the WHERE.
	UpdateSQL(spec *AdaptedTableSpec, versionCheck bool) string

	// DeleteSQL generates a DELETE statement for a single row.
	DeleteSQL(spec *AdaptedTableSpec) string

	// ExistsSQL generates an EXISTS check for a single row.
	ExistsSQL(spec *AdaptedTableSpec) string

	// MetadataTableSQL generates the DDL for the adapted_table_schemas
	// metadata table.
	MetadataTableSQL() string

	// NormaliseDecimal transforms a validated decimal string into the
	// storage representation for this backend. SQLite scales to int64.
	// PostgreSQL returns the value unchanged.
	NormaliseDecimal(value string, precision, scale int) (string, error)

	// DenormaliseDecimal transforms the stored representation back to
	// a client-facing string. SQLite divides by 10^scale and formats.
	// PostgreSQL returns the value unchanged.
	DenormaliseDecimal(value string, precision, scale int) string

	// SupportsNativeDecimalAggregation reports whether the backend
	// handles SUM/AVG on decimal columns with exact arithmetic.
	// If false, OQL aggregates in Go using shopspring/decimal.
	SupportsNativeDecimalAggregation() bool
}
