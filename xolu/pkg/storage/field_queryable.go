// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"

	"github.com/ha1tch/xolu/pkg/jsonic"
)

// FieldQueryable is an optional interface for storage backends that
// support selective field extraction from blob entities.
//
// Instead of deserialising every JSON blob into a full map, a
// FieldQueryable backend can extract only the requested fields during
// the scan loop. For blob entities this avoids allocating maps with
// dozens of unused keys.
//
// The OQL executor checks for this interface via type assertion. If
// the store satisfies FieldQueryable and the query's SELECT list names
// specific fields (not SELECT *), the executor may call ListWithFields
// instead of List.
type FieldQueryable interface {
	// ListWithFields returns all records for an entity type, but each
	// record contains only the specified fields plus _version. Fields
	// not present in a particular record's JSON are omitted from the
	// returned map (no null padding).
	//
	// For adapted entities this falls through to the regular List path
	// (adapted tables already select native columns efficiently).
	ListWithFields(ctx context.Context, entity string, fields []string) ([]map[string]interface{}, error)

	// QueryWithFields executes a pre-built SQL query (WHERE push-down)
	// and returns results with only the specified fields extracted from
	// the data blob. Like QueryWithPlan but avoids full deserialisation.
	QueryWithFields(ctx context.Context, sqlQuery string, args []interface{}, fields []string) ([]map[string]interface{}, error)
}

// FilterableStore is an optional extension of FieldQueryable that
// supports predicate evaluation during JSON tokenisation (B4 push-down).
//
// Instead of extracting all rows and filtering in Go afterward, a
// FilterableStore evaluates simple predicates inline during the token
// walk, skipping map allocation for rows that don't match.
//
// The OQL executor checks for this interface via type assertion. If the
// store satisfies FilterableStore and the WHERE clause can be expressed
// as a jsonic.PredicateSet, the executor passes predicates down.
type FilterableStore interface {
	FieldQueryable

	// ListWithFieldsAndFilter returns records for an entity type,
	// extracting only the specified fields and applying the predicate
	// set during tokenisation. Rows that fail the predicates are never
	// materialised as maps.
	//
	// The caller must ensure that fields includes all columns needed
	// for the SELECT list. Predicate fields may or may not overlap
	// with output fields.
	ListWithFieldsAndFilter(ctx context.Context, entity string, fields []string, preds *jsonic.PredicateSet) ([]map[string]interface{}, error)
}
