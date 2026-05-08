// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/storage"
)

// ---------------------------------------------------------------------------
// Full adapted push-down E2E tests
// ---------------------------------------------------------------------------
// These tests verify that queries produce identical results whether
// executed via the Go path or via full SQL push-down on adapted tables.
//
// Unlike aggregate_pushdown_test.go (which tests GROUP BY + aggregates),
// these cover the full push-down path: non-aggregate queries, ORDER BY,
// LIMIT, DISTINCT, HAVING, and compound queries.
// ---------------------------------------------------------------------------

type fullPDEnv struct {
	store  *storage.SQLiteStore
	goExec *Executor // Go-path only (no push-down)
	pdExec *Executor // Full push-down
	ctx    context.Context
	n      int
}

func newFullPDEnv(t *testing.T) *fullPDEnv {
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

	return &fullPDEnv{
		store:  store,
		goExec: goExec,
		pdExec: pdExec,
		ctx:    ctx,
		n:      goldenN,
	}
}

func (env *fullPDEnv) run(t *testing.T, oql string) {
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
		// Show first few rows for debugging
		showN := 5
		if showN > len(goResult.Rows) {
			showN = len(goResult.Rows)
		}
		for i := 0; i < showN; i++ {
			t.Logf("  Go row %d: %v", i, goResult.Rows[i])
		}
		showN = 5
		if showN > len(pdResult.Rows) {
			showN = len(pdResult.Rows)
		}
		for i := 0; i < showN; i++ {
			t.Logf("  PD row %d: %v", i, pdResult.Rows[i])
		}
		return
	}

	// Compare values
	for i, goRow := range goResult.Rows {
		pdRow := pdResult.Rows[i]
		for key, goVal := range goRow {
			pdVal, ok := pdRow[key]
			if !ok {
				t.Errorf("row %d: push-down missing key %q", i, key)
				continue
			}
			if !aggValuesMatch(goVal, pdVal) {
				t.Errorf("row %d key %q: Go=%v (%T) push-down=%v (%T)\nOQL: %s",
					i, key, goVal, goVal, pdVal, pdVal, oql)
			}
		}
	}
}

// runUnordered compares results without caring about row order.
// Useful for queries without ORDER BY where SQLite and Go may return
// rows in different order. Uses tolerance-aware value matching.
func (env *fullPDEnv) runUnordered(t *testing.T, oql string) {
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

	if len(goResult.Rows) != len(pdResult.Rows) {
		t.Errorf("row count mismatch: Go=%d push-down=%d\nOQL: %s",
			len(goResult.Rows), len(pdResult.Rows), oql)
		return
	}

	// For each Go row, find a matching push-down row using aggValuesMatch.
	// This is O(n^2) but n is small in tests.
	used := make([]bool, len(pdResult.Rows))
	for i, goRow := range goResult.Rows {
		found := false
		for j, pdRow := range pdResult.Rows {
			if used[j] {
				continue
			}
			if rowsMatch(goRow, pdRow) {
				used[j] = true
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Go row %d has no matching push-down row: %v\nOQL: %s",
				i, goRow, oql)
		}
	}
}

// rowsMatch checks if two row maps have the same keys with matching values.
func rowsMatch(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, av := range a {
		bv, ok := b[k]
		if !ok {
			return false
		}
		if !aggValuesMatch(av, bv) {
			return false
		}
	}
	return true
}

// ---------------------------------------------------------------------------
// TestFullPD: shared-environment subtests
//
// All subtests share a single env created by newFullPDEnv.
// The golden database is copied once; all queries are read-only.
// ---------------------------------------------------------------------------

func TestFullPD(t *testing.T) {
	env := newFullPDEnv(t)

	// runUnordered tests
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"SelectAll", "SELECT region, product, quantity FROM items"},
		{"WhereEquality", "SELECT region, product, quantity FROM items WHERE region = 'north'"},
		{"WhereComparison", "SELECT region, quantity FROM items WHERE quantity > 50"},
		{"WhereAnd", "SELECT region, product, quantity FROM items WHERE region = 'north' AND quantity > 50"},
		{"WhereOr", "SELECT region, quantity FROM items WHERE region = 'north' OR region = 'south'"},
		{"WhereLike", "SELECT region, product FROM items WHERE product LIKE 'gad%'"},
		{"WhereBetween", "SELECT region, quantity FROM items WHERE quantity BETWEEN 20 AND 40"},
		{"WhereIn", "SELECT region, product, quantity FROM items WHERE region IN ('north', 'east')"},
		{"WhereIsNull", "SELECT region, product FROM items WHERE category IS NOT NULL"},
		{"Distinct", "SELECT DISTINCT region FROM items"},
		{"DistinctMultiColumn", "SELECT DISTINCT region, product FROM items"},
		{"DistinctWithWhere", "SELECT DISTINCT region FROM items WHERE quantity > 50"},
		{"GroupByHaving", "SELECT region, SUM(amount) FROM items GROUP BY region HAVING SUM(amount) > 10000"},
		{"GroupByHavingCount", "SELECT region, COUNT(*) FROM items GROUP BY region HAVING COUNT(*) > 100"},
		{"GroupByMultiAgg", "SELECT region, COUNT(*), SUM(amount), AVG(amount), MIN(quantity), MAX(quantity) FROM items GROUP BY region"},
		{"DecimalSum", "SELECT region, SUM(amount) FROM items GROUP BY region"},
		{"DecimalAvg", "SELECT region, AVG(unit_price) FROM items GROUP BY region"},
		{"NoMatchingRows", "SELECT region, quantity FROM items WHERE quantity > 99999"},
		{"CountAll", "SELECT COUNT(*) FROM items"},
		{"SumAll", "SELECT SUM(quantity), AVG(quantity), MIN(quantity), MAX(quantity), COUNT(*) FROM items"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.runUnordered(t, tc.query)
		})
	}

	// run tests
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"OrderByAsc", "SELECT region, quantity FROM items ORDER BY quantity"},
		{"OrderByDesc", "SELECT region, quantity FROM items ORDER BY quantity DESC"},
		{"OrderByMultiple", "SELECT region, product, quantity FROM items ORDER BY region, quantity DESC"},
		{"WhereOrderBy", "SELECT region, quantity FROM items WHERE region = 'north' ORDER BY quantity DESC"},
		{"Limit", "SELECT TOP 10 region, quantity FROM items ORDER BY quantity DESC"},
		{"LimitSmall", "SELECT TOP 5 region, product, quantity FROM items WHERE region = 'east' ORDER BY quantity"},
		{"LimitOne", "SELECT TOP 1 region, quantity FROM items ORDER BY quantity DESC"},
		{"GroupByOrderBy", "SELECT region, SUM(amount) FROM items GROUP BY region ORDER BY SUM(amount) DESC"},
		{"GroupByOrderByLimit", "SELECT TOP 3 region, SUM(amount) FROM items GROUP BY region ORDER BY SUM(amount) DESC"},
		{"MultiGroupByOrderBy", "SELECT region, product, SUM(quantity) FROM items GROUP BY region, product ORDER BY region, SUM(quantity) DESC"},
		{"SingleRow", "SELECT TOP 1 region, product, quantity FROM items ORDER BY quantity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.run(t, tc.query)
		})
	}

}

func TestFullPD_GroupByWhereHavingOrderLimit(t *testing.T) {
env := newFullPDEnv(t)
	// Full compound query: WHERE + GROUP BY + HAVING + ORDER BY + LIMIT
	env.run(t, "SELECT TOP 2 region, SUM(amount), COUNT(*) FROM items WHERE quantity > 20 GROUP BY region HAVING COUNT(*) > 50 ORDER BY SUM(amount) DESC")
}

func TestFullPD_DecimalMinMax(t *testing.T) {
// Note: the Go path compares decimal values as strings, which gives
	// lexicographic ordering ("9.99" > "45.00"). The push-down path
	// operates on scaled int64 columns and produces numerically correct
	// results. This test verifies the push-down path returns reasonable
	// values (not comparing against the Go path).
	env := newFullPDEnv(t)
	stmt := parseOQL(t, "SELECT region, MIN(amount), MAX(amount), MIN(unit_price), MAX(unit_price) FROM items GROUP BY region")

	pdResult, err := env.pdExec.ExecuteWithStore(env.ctx, stmt, env.store)
	if err != nil {
		t.Fatalf("Push-down: %v", err)
	}

	if len(pdResult.Rows) != 4 {
		t.Fatalf("expected 4 regions, got %d", len(pdResult.Rows))
	}

	for _, row := range pdResult.Rows {
		// MIN should be less than MAX for each group
		minAmt, _ := aggToFloat(row["MIN(amount)"])
		maxAmt, _ := aggToFloat(row["MAX(amount)"])
		if minAmt >= maxAmt {
			t.Errorf("region %v: MIN(amount) %.4f >= MAX(amount) %.4f",
				row["region"], minAmt, maxAmt)
		}
		minPrice, _ := aggToFloat(row["MIN(unit_price)"])
		maxPrice, _ := aggToFloat(row["MAX(unit_price)"])
		if minPrice >= maxPrice {
			t.Errorf("region %v: MIN(unit_price) %.4f >= MAX(unit_price) %.4f",
				row["region"], minPrice, maxPrice)
		}
	}
}

func TestFullPD_Benchmark(t *testing.T) {
if testing.Short() {
		t.Skip("skipping benchmark in -short mode")
	}

	env := newFullPDEnv(t)

	queries := []struct {
		name string
		oql  string
	}{
		{"select_where", "SELECT region, quantity FROM items WHERE quantity > 50"},
		{"where_order_limit", "SELECT TOP 10 region, quantity FROM items WHERE region = 'north' ORDER BY quantity DESC"},
		{"distinct_region", "SELECT DISTINCT region FROM items"},
		{"group_having_order", "SELECT TOP 3 region, SUM(amount) FROM items GROUP BY region HAVING SUM(amount) > 5000 ORDER BY SUM(amount) DESC"},
		{"compound", "SELECT TOP 5 region, COUNT(*), SUM(amount) FROM items WHERE quantity > 20 GROUP BY region ORDER BY SUM(amount) DESC"},
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

		// Push-down path
		pdStart := time.Now()
		for i := 0; i < iters; i++ {
			env.pdExec.ExecuteWithStore(env.ctx, stmt, env.store)
		}
		pdDur := time.Since(pdStart)

		ratio := float64(goDur) / float64(pdDur)
		t.Logf("%-25s  Go: %8s  Push-down: %8s  Speedup: %.1fx",
			q.name, goDur, pdDur, ratio)
	}
}
