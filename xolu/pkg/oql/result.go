// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import "time"

// ResultType indicates the type of SQL statement executed
type ResultType int

const (
	ResultSelect ResultType = iota
	ResultInsert
	ResultUpdate
	ResultDelete
)

func (rt ResultType) String() string {
	switch rt {
	case ResultSelect:
		return "SELECT"
	case ResultInsert:
		return "INSERT"
	case ResultUpdate:
		return "UPDATE"
	case ResultDelete:
		return "DELETE"
	default:
		return "UNKNOWN"
	}
}

// QueryStats contains execution statistics
type QueryStats struct {
	RowsScanned   int           `json:"rows_scanned"`
	RowsReturned  int           `json:"rows_returned"`
	RowsAffected  int           `json:"rows_affected,omitempty"`
	ExecutionTime time.Duration `json:"execution_time_ms"`
}

// Result represents the result of an OQL query execution
type Result struct {
	Type  ResultType               `json:"type"`
	Rows  []map[string]interface{} `json:"data,omitempty"`
	Stats QueryStats               `json:"stats"`
}

// NewSelectResult creates a result for SELECT queries
func NewSelectResult(rows []map[string]interface{}, scanned int, duration time.Duration) *Result {
	return &Result{
		Type: ResultSelect,
		Rows: rows,
		Stats: QueryStats{
			RowsScanned:   scanned,
			RowsReturned:  len(rows),
			ExecutionTime: duration,
		},
	}
}

// NewMutationResult creates a result for INSERT/UPDATE/DELETE queries
func NewMutationResult(resultType ResultType, affected int, duration time.Duration) *Result {
	return &Result{
		Type: resultType,
		Stats: QueryStats{
			RowsAffected:  affected,
			ExecutionTime: duration,
		},
	}
}
