// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"fmt"
)

// AggregateQuery executes an aggregate SQL query against native columns
// and returns results keyed by alias names.
func (s *SQLiteStore) AggregateQuery(ctx context.Context, sql string, args []interface{}, aliases []string) ([]map[string]interface{}, error) {
	stmt, err := s.stmtCache.Get(sql)
	if err != nil {
		return nil, fmt.Errorf("aggregate prepare failed: %w", err)
	}

	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("aggregate query failed: %w", err)
	}
	defer rows.Close()

	// Build scan targets
	ncols := len(aliases)
	var results []map[string]interface{}

	for rows.Next() {
		vals := make([]interface{}, ncols)
		ptrs := make([]interface{}, ncols)
		for i := range vals {
			ptrs[i] = &vals[i]
		}

		if err := rows.Scan(ptrs...); err != nil {
			return nil, fmt.Errorf("aggregate scan: %w", err)
		}

		row := make(map[string]interface{}, ncols)
		for i, alias := range aliases {
			row[alias] = vals[i]
		}
		results = append(results, row)
	}

	if results == nil {
		results = []map[string]interface{}{}
	}
	return results, rows.Err()
}

// IsAdaptedEntity reports whether the entity uses an adapted table.
func (s *SQLiteStore) IsAdaptedEntity(entity string) bool {
	return s.adapted.IsAdapted(entity)
}

// AdaptedColumnInfo returns column metadata for a JSON field in an adapted entity.
func (s *SQLiteStore) AdaptedColumnInfo(entity, jsonField string) (colName string, scale int, isDecimal bool, ok bool) {
	spec := s.adapted.Get(entity)
	if spec == nil {
		return "", 0, false, false
	}

	for _, col := range spec.Columns {
		if col.JSONField == jsonField {
			return col.Name, col.Scale, col.Format == "decimal", true
		}
	}
	return "", 0, false, false
}

// AdaptedTableName returns the SQL table name for an adapted entity.
func (s *SQLiteStore) AdaptedTableName(entity string) (string, bool) {
	spec := s.adapted.Get(entity)
	if spec == nil {
		return "", false
	}
	return spec.TableName(), true
}

// StorageDialectFor returns the StorageDialect for the given entity,
// or nil if the entity is not adapted.
func (s *SQLiteStore) StorageDialectFor(entity string) StorageDialect {
	if !s.adapted.IsAdapted(entity) {
		return nil
	}
	return s.dialect
}
