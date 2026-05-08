// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import "context"

// AggregateQueryable is an optional interface for storage backends that
// support native GROUP BY + aggregate push-down for adapted tables.
//
// When an entity has an adapted table, aggregate queries can run entirely
// in SQL against native columns instead of fetching all rows into Go.
// The OQL executor checks for this interface via type assertion.
type AggregateQueryable interface {
	// AggregateQuery executes a pre-built aggregate SQL query and returns
	// the grouped results as maps. Unlike QueryWithPlan (which scans
	// data+_version blobs), this scans arbitrary columns/expressions
	// and returns them by alias.
	AggregateQuery(ctx context.Context, sql string, args []interface{}, aliases []string) ([]map[string]interface{}, error)

	// IsAdaptedEntity reports whether the given entity uses an adapted
	// table (column-per-field storage). The OQL planner uses this to
	// decide whether aggregate push-down is possible.
	IsAdaptedEntity(entity string) bool

	// AdaptedColumnInfo returns the SQL column name for a JSON field in
	// an adapted entity. Returns ("", false) if the entity is not adapted
	// or the field is not a known column. For decimal columns, also returns
	// the scale so the caller can denormalise aggregated values.
	AdaptedColumnInfo(entity, jsonField string) (colName string, scale int, isDecimal bool, ok bool)

	// AdaptedTableName returns the SQL table name for an adapted entity.
	// Returns ("", false) if the entity is not adapted.
	AdaptedTableName(entity string) (string, bool)

	// StorageDialectFor returns the StorageDialect for the given entity,
	// or nil if the entity is not adapted. This allows SQL generators to
	// access dialect methods without importing backend-specific types.
	StorageDialectFor(entity string) StorageDialect
}
