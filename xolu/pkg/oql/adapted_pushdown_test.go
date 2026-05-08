// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/tsqlparser/ast"
)

// ---------------------------------------------------------------------------
// Full adapted push-down E2E tests
// ---------------------------------------------------------------------------
// These tests verify that full push-down queries produce identical results
// to the Go pipeline for adapted entities. Every query is executed both
// ways and the results compared.
//
// The full push-down translates the entire SELECT (including scalars,
// WHERE, GROUP BY, HAVING, ORDER BY, DISTINCT, LIMIT) into a single
// SQL query against native columns. This is Phase A1 of the query
// optimisation roadmap.
// ---------------------------------------------------------------------------

type adaptedEnv struct {
	store  *storage.SQLiteStore
	goExec *Executor // Go-path only (no push-down)
	pdExec *Executor // Full push-down enabled
	ctx    context.Context
	n      int // record count
}

func newAdaptedEnv(t *testing.T) *adaptedEnv {
	t.Helper()

	store := openGoldenStore(t)
	ctx := context.Background()

	// Go-path executor: nonAggStore hides AggregateQueryable
	goStore := &nonAggStore{Store: store, q: store}
	goExec := &Executor{
		store:      goStore,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}

	// Push-down executor: threshold=1 so push-down always triggers
	pdExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithDialectAndThreshold(&SQLiteDialect{}, 1),
		dialect:    &SQLiteDialect{},
	}

	return &adaptedEnv{
		store:  store,
		goExec: goExec,
		pdExec: pdExec,
		ctx:    ctx,
		n:      goldenN,
	}
}

func (env *adaptedEnv) run(t *testing.T, oql string) {
	t.Helper()

	stmt := parseOQL(t, oql)

	goResult, err := env.goExec.ExecuteWithStore(env.ctx, stmt, env.goExec.store)
	if err != nil {
		t.Fatalf("Go-path %q: %v", oql, err)
	}

	pdResult, err := env.pdExec.ExecuteWithStore(env.ctx, stmt, env.store)
	if err != nil {
		t.Fatalf("Push-down %q: %v", oql, err)
	}

	// Compare row counts
	if len(goResult.Rows) != len(pdResult.Rows) {
		t.Errorf("row count mismatch: Go=%d push-down=%d\nOQL: %s",
			len(goResult.Rows), len(pdResult.Rows), oql)
		for i, r := range goResult.Rows {
			if i >= 3 {
				break
			}
			t.Logf("  Go row %d: %v", i, r)
		}
		for i, r := range pdResult.Rows {
			if i >= 3 {
				break
			}
			t.Logf("  PD row %d: %v", i, r)
		}
		return
	}

	// Determine if query has ORDER BY — if not, use set-based comparison
	sel := stmt.(*ast.SelectStatement)
	hasOrderBy := len(sel.OrderBy) > 0

	if hasOrderBy {
		// Ordered comparison: row-by-row positional match
		for i, goRow := range goResult.Rows {
			pdRow := pdResult.Rows[i]
			for key, goVal := range goRow {
				pdVal, ok := pdRow[key]
				if !ok {
					t.Errorf("row %d: push-down missing key %q\nOQL: %s", i, key, oql)
					continue
				}
				if !adaptedValuesMatch(goVal, pdVal) {
					t.Errorf("row %d key %q: Go=%v (%T) push-down=%v (%T)\nOQL: %s",
						i, key, goVal, goVal, pdVal, pdVal, oql)
				}
			}
		}
	} else {
		// Unordered comparison: every Go row must have a matching PD row
		adaptedCompareUnordered(t, goResult.Rows, pdResult.Rows, oql)
	}
}

// adaptedCompareUnordered verifies that two row sets contain the same rows
// regardless of order. Uses a greedy matching approach.
func adaptedCompareUnordered(t *testing.T, goRows, pdRows []map[string]interface{}, oql string) {
	t.Helper()

	used := make([]bool, len(pdRows))
	for i, goRow := range goRows {
		found := false
		for j, pdRow := range pdRows {
			if used[j] {
				continue
			}
			if adaptedRowsMatch(goRow, pdRow) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Go row %d has no matching push-down row\n  Go: %v\nOQL: %s", i, goRow, oql)
		}
	}
}

// adaptedRowsMatch checks if two rows have the same keys and matching values.
func adaptedRowsMatch(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for key, aVal := range a {
		bVal, ok := b[key]
		if !ok {
			return false
		}
		if !adaptedValuesMatch(aVal, bVal) {
			return false
		}
	}
	return true
}

// adaptedValuesMatch compares two values with tolerance for decimal
// and numeric representations.
func adaptedValuesMatch(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Try numeric comparison with tolerance
	af, aOk := adaptedToFloat(a)
	bf, bOk := adaptedToFloat(b)
	if aOk && bOk {
		if af == 0 && bf == 0 {
			return true
		}
		diff := math.Abs(af - bf)
		avg := (math.Abs(af) + math.Abs(bf)) / 2
		if avg == 0 {
			return diff < 0.01
		}
		return diff/avg < 0.001 // 0.1% tolerance
	}

	// String comparison
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

func adaptedToFloat(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case float64:
		return val, true
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case string:
		f, err := strconv.ParseFloat(val, 64)
		if err != nil {
			return 0, false
		}
		return f, true
	default:
		return 0, false
	}
}

// ---------------------------------------------------------------------------
// TestAdaptedPushDown: shared-environment subtests
//
// All subtests share a single env created by newAdaptedEnv.
// The golden database is copied once; all queries are read-only.
// ---------------------------------------------------------------------------

func TestAdaptedPushDown(t *testing.T) {
	env := newAdaptedEnv(t)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"SelectAll", "SELECT region, product, quantity FROM items"},
		{"WhereEquality", "SELECT region, product, quantity FROM items WHERE region = 'north'"},
		{"WhereComparison", "SELECT region, product, quantity FROM items WHERE quantity > 50"},
		{"WhereAnd", "SELECT region, product, amount FROM items WHERE region = 'north' AND quantity > 50"},
		{"WhereOr", "SELECT region, product FROM items WHERE region = 'north' OR region = 'south'"},
		{"WhereLike", "SELECT region, product FROM items WHERE product LIKE 'gad%'"},
		{"WhereBetween", "SELECT region, product, quantity FROM items WHERE quantity BETWEEN 20 AND 60"},
		{"WhereIn", "SELECT region, product FROM items WHERE region IN ('north', 'east')"},
		{"WhereIsNull", "SELECT region, product FROM items WHERE unit_price IS NOT NULL"},
		{"OrderByAsc", "SELECT region, product, quantity FROM items ORDER BY quantity"},
		{"OrderByDesc", "SELECT region, product, quantity FROM items ORDER BY quantity DESC"},
		{"OrderByMultiple", "SELECT region, product, quantity FROM items ORDER BY region, quantity DESC"},
		{"Top", "SELECT TOP 10 region, product, quantity FROM items"},
		{"TopWithOrderBy", "SELECT TOP 5 region, product, quantity FROM items ORDER BY quantity DESC"},
		{"TopWithWhere", "SELECT TOP 10 region, product, quantity FROM items WHERE region = 'north' ORDER BY quantity DESC"},
		{"Distinct", "SELECT DISTINCT region FROM items"},
		{"DistinctMultiColumn", "SELECT DISTINCT region, category FROM items"},
		{"DistinctWithOrderBy", "SELECT DISTINCT region FROM items ORDER BY region"},
		{"CountStar", "SELECT COUNT(*) FROM items"},
		{"CountGroupBy", "SELECT region, COUNT(*) FROM items GROUP BY region"},
		{"SumDecimal", "SELECT SUM(amount) FROM items"},
		{"SumGroupBy", "SELECT region, SUM(amount) FROM items GROUP BY region"},
		{"AvgGroupBy", "SELECT region, AVG(amount) FROM items GROUP BY region"},
		{"MinMaxGroupBy", "SELECT region, MIN(amount), MAX(amount) FROM items GROUP BY region"},
		{"MultiAggregates", "SELECT region, COUNT(*), SUM(amount), AVG(amount), MIN(amount), MAX(amount) FROM items GROUP BY region"},
		{"MultiGroupBy", "SELECT region, category, SUM(amount) FROM items GROUP BY region, category"},
		{"AggWithWhere", "SELECT region, SUM(amount) FROM items WHERE quantity > 50 GROUP BY region"},
		{"AggWithOrderBy", "SELECT region, SUM(amount) FROM items GROUP BY region ORDER BY region"},
		{"Having", "SELECT region, SUM(amount) FROM items GROUP BY region HAVING SUM(amount) > 10000"},
		{"HavingCount", "SELECT region, COUNT(*) FROM items GROUP BY region HAVING COUNT(*) > 100"},
		{"HavingWithWhere", "SELECT region, SUM(amount) FROM items WHERE quantity > 20 GROUP BY region HAVING SUM(amount) > 5000"},
		{"WhereGroupByOrderByLimit", "SELECT TOP 3 region, SUM(amount) FROM items WHERE quantity > 20 GROUP BY region ORDER BY SUM(amount) DESC"},
		{"FullAnalytical", "SELECT region, category, COUNT(*), SUM(amount), AVG(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY SUM(amount) DESC"},
		{"PaginationPattern", "SELECT TOP 20 region, product, quantity, amount FROM items WHERE region = 'north' ORDER BY quantity DESC"},
		{"SecondDecimalField", "SELECT region, SUM(unit_price), AVG(unit_price) FROM items GROUP BY region"},
		{"SumInteger", "SELECT region, SUM(quantity) FROM items GROUP BY region"},
		{"AvgInteger", "SELECT region, AVG(quantity) FROM items GROUP BY region"},
		{"ScalarUpper", "SELECT UPPER(region), product FROM items WHERE region = 'north'"},
		{"ScalarLower", "SELECT LOWER(product), quantity FROM items WHERE quantity > 90"},
		{"ScalarAbs", "SELECT region, ABS(quantity) FROM items WHERE region = 'south'"},
		{"EmptyResult", "SELECT region, product FROM items WHERE quantity > 999999"},
		{"SingleRow", "SELECT TOP 1 region, product, quantity FROM items ORDER BY quantity DESC"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.run(t, tc.query)
		})
	}

}

func TestAdaptedPushDown_AllRows(t *testing.T) {
env := newAdaptedEnv(t)
	// No WHERE, no LIMIT — should return all 500 rows
	stmt := parseOQL(t, "SELECT region, quantity FROM items")

	goResult, _ := env.goExec.ExecuteWithStore(env.ctx, stmt, env.goExec.store)
	pdResult, _ := env.pdExec.ExecuteWithStore(env.ctx, stmt, env.store)

	if len(goResult.Rows) != env.n || len(pdResult.Rows) != env.n {
		t.Errorf("expected %d rows from both paths, got Go=%d PD=%d",
			env.n, len(goResult.Rows), len(pdResult.Rows))
	}
}

func TestAdaptedPushDown_Benchmark(t *testing.T) {
if testing.Short() {
		t.Skip("skipping benchmark in -short mode")
	}

	env := newAdaptedEnv(t)

	queries := []struct {
		name string
		oql  string
	}{
		{"select_where_order_limit",
			"SELECT TOP 20 region, product, quantity, amount FROM items WHERE region = 'north' ORDER BY quantity DESC"},
		{"count_group",
			"SELECT region, COUNT(*) FROM items GROUP BY region"},
		{"sum_group_where",
			"SELECT region, SUM(amount) FROM items WHERE quantity > 50 GROUP BY region"},
		{"multi_agg_group",
			"SELECT region, COUNT(*), SUM(amount), AVG(amount), MIN(amount), MAX(amount) FROM items GROUP BY region"},
		{"multi_group",
			"SELECT region, category, SUM(amount) FROM items GROUP BY region, category"},
		{"having",
			"SELECT region, SUM(amount) FROM items GROUP BY region HAVING SUM(amount) > 10000"},
		{"full_analytical",
			"SELECT region, category, COUNT(*), SUM(amount) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY SUM(amount) DESC"},
		{"distinct",
			"SELECT DISTINCT region, category FROM items"},
		{"scalar_upper_where",
			"SELECT UPPER(region), product FROM items WHERE quantity > 80"},
	}

	iters := 50

	for _, q := range queries {
		stmt := parseOQL(t, q.oql)

		// Go path
		goStart := time.Now()
		for i := 0; i < iters; i++ {
			env.goExec.ExecuteWithStore(env.ctx, stmt, env.goExec.store)
		}
		goDur := time.Since(goStart)

		// Full push-down path
		pdStart := time.Now()
		for i := 0; i < iters; i++ {
			env.pdExec.ExecuteWithStore(env.ctx, stmt, env.store)
		}
		pdDur := time.Since(pdStart)

		ratio := float64(goDur) / float64(pdDur)
		t.Logf("%-30s  Go: %8s  Push-down: %8s  Speedup: %.1fx",
			q.name, goDur, pdDur, ratio)
	}
}
