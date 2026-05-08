// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"fmt"
	"strconv"
)

// NormaliseDecimalColumns applies dialect-specific decimal normalisation
// to column values produced by PartitionData. This transforms validated
// decimal strings into scaled integers for storage. Called on the write
// path (create, update) before SQL execution.
func NormaliseDecimalColumns(spec *AdaptedTableSpec, dialect StorageDialect, colVals []interface{}) error {
	for i, col := range spec.Columns {
		if col.Format != "decimal" || colVals[i] == nil {
			continue
		}
		s, ok := colVals[i].(string)
		if !ok {
			return fmt.Errorf("decimal column %q: expected string, got %T", col.Name, colVals[i])
		}
		normalised, err := dialect.NormaliseDecimal(s, col.Precision, col.Scale)
		if err != nil {
			return fmt.Errorf("decimal column %q: %w", col.JSONField, err)
		}
		// Convert the string representation to int64 for INTEGER column storage
		n, err := strconv.ParseInt(normalised, 10, 64)
		if err != nil {
			return fmt.Errorf("decimal column %q: failed to parse scaled integer %q: %w", col.JSONField, normalised, err)
		}
		colVals[i] = n
	}
	return nil
}

// DenormaliseDecimalColumns applies dialect-specific decimal denormalisation
// to column values read from the database. This converts scaled integers
// back to client-facing decimal strings. Called on the read path (get, list)
// after SQL scan.
func DenormaliseDecimalColumns(spec *AdaptedTableSpec, dialect StorageDialect, colVals []interface{}) {
	for i, col := range spec.Columns {
		if col.Format != "decimal" || colVals[i] == nil {
			continue
		}
		// SQLite returns integers as int64; convert to string for DenormaliseDecimal
		var s string
		switch v := colVals[i].(type) {
		case int64:
			s = strconv.FormatInt(v, 10)
		case int:
			s = strconv.Itoa(v)
		case float64:
			s = strconv.FormatInt(int64(v), 10)
		case string:
			s = v
		default:
			continue
		}
		colVals[i] = dialect.DenormaliseDecimal(s, col.Precision, col.Scale)
	}
}
