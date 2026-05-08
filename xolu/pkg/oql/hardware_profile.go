// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"strings"

	"github.com/ha1tch/tsqlparser/ast"
)

// HardwareProfile holds threshold values that control when the planner
// pushes queries to SQL versus processing in Go. The optimal thresholds
// depend on the relative speed of Go's JSON processing versus SQLite's
// C engine on the host hardware.
//
// Three named presets cover common deployment targets. The Calibrate()
// function can derive a custom profile from a startup micro-benchmark.
type HardwareProfile struct {
	Name string // "edge", "vps", "dedicated", "calibrated", or "custom"

	// BlobPushThreshold is the minimum row count at which blob entity
	// queries are pushed to SQL (json_extract). Below this count, the
	// Go path processes rows directly without a CountEntities round-trip.
	BlobPushThreshold int

	// ComplexityThresholds gate PushFull for adapted entities based on
	// estimated query complexity. When estimated complexity exceeds zero,
	// the planner checks row count against these thresholds before
	// committing to PushFull.
	//
	// A query's complexity is estimated from its AST:
	//   - Each multi-key GROUP BY adds 1 temp B-tree
	//   - Each ORDER BY misaligned with GROUP BY adds 1 temp B-tree
	//   - Non-COUNT aggregates on columns not in GROUP BY set nonCovering
	//
	// Simple queries (0 temp B-trees, covering) always use PushFull.
	NonCoveringThreshold int // min rows for push-down with non-covering aggregate scan
	TempBTree1Threshold  int // min rows for push-down with 1 temp B-tree
	TempBTree2Threshold  int // min rows for push-down with 2+ temp B-trees
}

// Predefined profiles for common deployment targets.
var (
	// ProfileEdge targets ARM single-board computers, industrial gateways,
	// and low-power edge devices (1-2 cores, 1-4 GB RAM, eMMC/SD storage).
	// Go's GC is proportionally expensive; SQLite's C engine has a larger
	// relative advantage, so push-down thresholds are lower.
	ProfileEdge = HardwareProfile{
		Name:                 "edge",
		BlobPushThreshold:    25,
		NonCoveringThreshold: 500,
		TempBTree1Threshold:  250,
		TempBTree2Threshold:  1000,
	}

	// ProfileVPS targets small cloud instances (1-2 vCPU, 2-8 GB RAM, SSD).
	// Shared CPU with noisy neighbours. This is the default and most common
	// deployment target for self-hosted olu instances.
	ProfileVPS = HardwareProfile{
		Name:                 "vps",
		BlobPushThreshold:    50,
		NonCoveringThreshold: 1000,
		TempBTree1Threshold:  500,
		TempBTree2Threshold:  2000,
	}

	// ProfileDedicated targets bare metal or large instances (4+ cores,
	// 16+ GB RAM). Go has GC headroom and the CPU is fast enough that
	// push-down overhead must be justified by larger datasets.
	ProfileDedicated = HardwareProfile{
		Name:                 "dedicated",
		BlobPushThreshold:    100,
		NonCoveringThreshold: 2000,
		TempBTree1Threshold:  1000,
		TempBTree2Threshold:  5000,
	}
)

// ProfileByName returns the named preset, or nil if not found.
// Accepts: "edge", "vps", "dedicated" (case-insensitive).
func ProfileByName(name string) *HardwareProfile {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "edge":
		p := ProfileEdge
		return &p
	case "vps":
		p := ProfileVPS
		return &p
	case "dedicated":
		p := ProfileDedicated
		return &p
	default:
		return nil
	}
}

// DefaultProfile returns the VPS profile, which is the safest middle
// ground when no profile is specified.
func DefaultProfile() HardwareProfile {
	return ProfileVPS
}

// ---------------------------------------------------------------------------
// Query complexity estimation
// ---------------------------------------------------------------------------

// QueryComplexity describes the estimated cost characteristics of a
// SELECT statement when executed via PushFull on an adapted entity.
// The planner uses this to decide whether push-down is worthwhile
// at the current row count.
type QueryComplexity struct {
	TempBTrees  int  // estimated number of temp B-tree materialisations
	NonCovering bool // aggregate scan requires data page access beyond the index
}

// IsSimple returns true if the query has no complexity factors that
// would penalise push-down. Simple queries always use PushFull
// regardless of row count.
func (qc QueryComplexity) IsSimple() bool {
	return qc.TempBTrees == 0 && !qc.NonCovering
}

// Threshold returns the minimum row count at which PushFull is
// expected to be faster than the Go path for this complexity level.
// Returns 0 for simple queries (always push).
func (qc QueryComplexity) Threshold(profile *HardwareProfile) int {
	if qc.IsSimple() {
		return 0
	}

	threshold := 0

	// Non-covering aggregate scans: must read data pages for every row,
	// negating the index advantage for GROUP BY ordering.
	if qc.NonCovering && profile.NonCoveringThreshold > threshold {
		threshold = profile.NonCoveringThreshold
	}

	// Temp B-trees: each materialisation costs O(n log n) in SQLite's
	// allocator. At small n, this exceeds Go's O(n) brute force.
	switch {
	case qc.TempBTrees >= 2:
		if profile.TempBTree2Threshold > threshold {
			threshold = profile.TempBTree2Threshold
		}
	case qc.TempBTrees == 1:
		if profile.TempBTree1Threshold > threshold {
			threshold = profile.TempBTree1Threshold
		}
	}

	return threshold
}

// EstimateComplexity examines a SELECT statement's AST and predicts
// the cost characteristics of executing it via PushFull on SQLite.
//
// This is a static analysis — no database access required. The
// estimation assumes single-column indexes on each adapted column
// (which is what olu creates by default).
//
// The signals detected:
//
//  1. Multi-key GROUP BY: SQLite cannot satisfy a two-key GROUP BY
//     from any single-column index. It will scan one index and
//     materialise a temp B-tree for the grouping.
//
//  2. ORDER BY misaligned with GROUP BY: if the query has both GROUP
//     BY and ORDER BY, and the ORDER BY columns are not a prefix of
//     the GROUP BY output, SQLite materialises a second temp B-tree.
//
//  3. Non-covering aggregates: when non-COUNT aggregates (SUM, AVG,
//     MIN, MAX) reference columns not in the GROUP BY key, the index
//     scan is non-covering — SQLite must fetch every data page to
//     read the aggregated values.
func EstimateComplexity(stmt *ast.SelectStatement) QueryComplexity {
	var qc QueryComplexity

	// Signal 1: multi-key GROUP BY → temp B-tree for grouping
	if len(stmt.GroupBy) >= 2 {
		qc.TempBTrees++
	}

	// Signal 2: ORDER BY misaligned with GROUP BY → temp B-tree for sort
	if len(stmt.OrderBy) > 0 && len(stmt.GroupBy) > 0 {
		if !isOrderByPrefixOfGroupBy(stmt) {
			qc.TempBTrees++
		}
	}

	// Signal 3: non-covering aggregate scan
	if len(stmt.GroupBy) > 0 && hasNonCountAggregates(stmt) {
		groupByFields := groupByFieldSet(stmt)
		if hasAggregatesOutsideGroupBy(stmt, groupByFields) {
			qc.NonCovering = true
		}
	}

	return qc
}

// isOrderByPrefixOfGroupBy checks whether the ORDER BY columns are
// a prefix of the GROUP BY columns (in order). When they are, SQLite
// can produce ordered output from the GROUP BY operation without an
// additional sort step.
func isOrderByPrefixOfGroupBy(stmt *ast.SelectStatement) bool {
	if len(stmt.OrderBy) > len(stmt.GroupBy) {
		return false
	}
	for i, ob := range stmt.OrderBy {
		if exprToString(ob.Expression) != exprToString(stmt.GroupBy[i]) {
			return false
		}
	}
	return true
}

// hasNonCountAggregates checks whether the SELECT list contains
// aggregate functions other than COUNT. COUNT(*) and COUNT(col) are
// "free" on a covering index scan — they don't require reading data
// pages. SUM, AVG, MIN, MAX do.
func hasNonCountAggregates(stmt *ast.SelectStatement) bool {
	for _, col := range stmt.Columns {
		fc, ok := col.Expression.(*ast.FunctionCall)
		if !ok {
			continue
		}
		name := strings.ToUpper(exprToString(fc.Function))
		switch name {
		case "SUM", "AVG", "MIN", "MAX":
			return true
		}
	}
	return false
}

// groupByFieldSet extracts the set of field names from the GROUP BY
// clause for quick membership testing.
func groupByFieldSet(stmt *ast.SelectStatement) map[string]bool {
	fields := make(map[string]bool, len(stmt.GroupBy))
	for _, gb := range stmt.GroupBy {
		fields[exprToString(gb)] = true
	}
	return fields
}

// hasAggregatesOutsideGroupBy checks whether any non-COUNT aggregate
// in the SELECT list references a column that is NOT in the GROUP BY
// set. Such aggregates require data page access beyond the covering
// index.
func hasAggregatesOutsideGroupBy(stmt *ast.SelectStatement, groupByFields map[string]bool) bool {
	for _, col := range stmt.Columns {
		fc, ok := col.Expression.(*ast.FunctionCall)
		if !ok {
			continue
		}
		name := strings.ToUpper(exprToString(fc.Function))
		switch name {
		case "SUM", "AVG", "MIN", "MAX":
			if len(fc.Arguments) > 0 {
				argName := exprToString(fc.Arguments[0])
				if !groupByFields[argName] {
					return true
				}
			}
		}
	}
	return false
}
