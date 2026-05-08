// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/storage"
)

// ---------------------------------------------------------------------------
// Aggregate push-down E2E tests
// ---------------------------------------------------------------------------
// These tests verify that aggregate queries (SUM, AVG, COUNT, MIN, MAX,
// GROUP BY) produce identical results whether executed via the Go path
// or via SQL push-down on adapted tables.
//
// Setup: "sales" entity with adapted table (decimal fields for amount and
// unit_price). The same dataset is queried both ways and results compared.
// ---------------------------------------------------------------------------

type aggEnv struct {
	store  *storage.SQLiteStore
	goExec *Executor // Go-path only (no push-down)
	pdExec *Executor // Push-down with aggregate support
	ctx    context.Context
	n      int // record count
}

func newAggEnv(t *testing.T) *aggEnv {
	t.Helper()

	store := openGoldenStore(t)
	ctx := context.Background()

	// Go-path executor: nonAggStore hides AggregateQueryable and
	// FilterableStore, forcing the executor through the true Go pipeline
	// (fetch all rows, filter/aggregate in Go).
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

	return &aggEnv{
		store:  store,
		goExec: goExec,
		pdExec: pdExec,
		ctx:    ctx,
		n:      goldenN,
	}
}

func (env *aggEnv) run(t *testing.T, oql string) {
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
		t.Errorf("row count mismatch: Go=%d push-down=%d\nOQL: %s", len(goResult.Rows), len(pdResult.Rows), oql)
		return
	}

	// For queries without ORDER BY, GROUP BY results may arrive in any
	// order. Build a canonical key for each row so we can match them
	// independent of position.
	hasOrderBy := strings.Contains(strings.ToUpper(oql), "ORDER BY")

	if hasOrderBy {
		// Positional comparison (original logic)
		for i, goRow := range goResult.Rows {
			pdRow := pdResult.Rows[i]
			for key, goVal := range goRow {
				pdVal, ok := pdRow[key]
				if !ok {
					t.Errorf("row %d: push-down missing key %q\nOQL: %s", i, key, oql)
					continue
				}
				if !aggValuesMatch(goVal, pdVal) {
					t.Errorf("row %d key %q: Go=%v (%T) push-down=%v (%T)\nOQL: %s",
						i, key, goVal, goVal, pdVal, pdVal, oql)
				}
			}
		}
	} else {
		// Order-independent comparison: match rows by their group-by keys.
		aggMatchRowSets(t, goResult.Rows, pdResult.Rows, oql)
	}
}

// aggRowKey builds a canonical string key from the non-numeric (group-by)
// columns of a row. For rows that are pure aggregates (no group-by), it
// returns a fixed sentinel.
func aggRowKey(row map[string]interface{}) string {
	// Collect string-valued columns (group-by keys).
	var parts []string
	keys := make([]string, 0, len(row))
	for k := range row {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		// Skip aggregate columns (they contain numbers)
		if _, ok := aggToFloat(row[k]); ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s=%v", k, row[k]))
	}
	if len(parts) == 0 {
		return "__aggregate_only__"
	}
	return strings.Join(parts, "|")
}

// aggMatchRowSets verifies that two row sets contain the same rows
// regardless of order. Rows are matched by their group-by key, then
// aggregate values are compared with numeric tolerance.
func aggMatchRowSets(t *testing.T, goRows, pdRows []map[string]interface{}, oql string) {
	t.Helper()

	// Index PD rows by their group-by key.
	pdByKey := make(map[string]map[string]interface{}, len(pdRows))
	for _, row := range pdRows {
		key := aggRowKey(row)
		pdByKey[key] = row
	}

	for i, goRow := range goRows {
		key := aggRowKey(goRow)
		pdRow, ok := pdByKey[key]
		if !ok {
			t.Errorf("Go row %d (key %q) has no matching push-down row\n  Go: %v\nOQL: %s",
				i, key, goRow, oql)
			continue
		}
		for col, goVal := range goRow {
			pdVal, ok := pdRow[col]
			if !ok {
				t.Errorf("row key %q: push-down missing column %q\nOQL: %s", key, col, oql)
				continue
			}
			if !aggValuesMatch(goVal, pdVal) {
				t.Errorf("row key %q col %q: Go=%v (%T) push-down=%v (%T)\nOQL: %s",
					key, col, goVal, goVal, pdVal, pdVal, oql)
			}
		}
	}
}

// aggValuesMatch compares two values with tolerance for decimal representations.
func aggValuesMatch(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}

	// Try numeric comparison with tolerance
	af, aOk := aggToFloat(a)
	bf, bOk := aggToFloat(b)
	if aOk && bOk {
		if af == 0 && bf == 0 {
			return true
		}
		// Relative tolerance for AVG rounding differences
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

func aggToFloat(v interface{}) (float64, bool) {
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
// TestAggregatePushDown: shared-environment subtests
//
// All subtests share a single env created by newAggEnv.
// The golden database is copied once; all queries are read-only.
// ---------------------------------------------------------------------------

func TestAggregatePushDown(t *testing.T) {
	env := newAggEnv(t)

	for _, tc := range []struct {
		name  string
		query string
	}{
		{"CountStar", "SELECT COUNT(*) FROM sales"},
		{"CountStarGroupBy", "SELECT region, COUNT(*) FROM sales GROUP BY region"},
		{"SumDecimal", "SELECT SUM(amount) FROM sales"},
		{"SumGroupBy", "SELECT region, SUM(amount) FROM sales GROUP BY region"},
		{"AvgDecimal", "SELECT AVG(amount) FROM sales"},
		{"AvgGroupBy", "SELECT region, AVG(amount) FROM sales GROUP BY region"},
		{"MinMaxDecimal", "SELECT MIN(amount), MAX(amount) FROM sales"},
		{"MinMaxGroupBy", "SELECT region, MIN(amount), MAX(amount) FROM sales GROUP BY region"},
		{"SumInteger", "SELECT SUM(quantity) FROM sales"},
		{"SumIntegerGroupBy", "SELECT region, SUM(quantity) FROM sales GROUP BY region"},
		{"MultipleAggregates", "SELECT region, COUNT(*), SUM(amount), AVG(amount), MIN(amount), MAX(amount) FROM sales GROUP BY region"},
		{"WithWhere", "SELECT region, SUM(amount) FROM sales WHERE quantity > 50 GROUP BY region"},
		{"WithWhereEquality", "SELECT product, COUNT(*), SUM(amount) FROM sales WHERE region = 'north' GROUP BY product"},
		{"MultiGroupBy", "SELECT region, product, SUM(amount), COUNT(*) FROM sales GROUP BY region, product"},
		{"NoGroupByJustAgg", "SELECT COUNT(*), SUM(amount), AVG(quantity), MIN(unit_price), MAX(unit_price) FROM sales"},
		{"SecondDecimalField", "SELECT region, SUM(unit_price), AVG(unit_price) FROM sales GROUP BY region"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.run(t, tc.query)
		})
	}

}

func TestAggregatePushDown_Benchmark(t *testing.T) {
if testing.Short() {
		t.Skip("skipping benchmark in -short mode")
	}

	env := newAggEnv(t)

	// Wrap the store so the Go-path executor doesn't see AggregateQueryable
	goStore := &nonAggStore{Store: env.store, q: env.store}

	// Use threshold=MaxInt for both to eliminate planner CountEntities overhead.
	// The Go executor uses nonAggStore (no aggregate push-down, uses List+Go agg).
	// The PD executor uses raw store (aggregate push-down fires via AggregateQueryable).
	goExec := &Executor{
		store:      goStore,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}
	pdExec := &Executor{
		store:      env.store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}

	queries := []struct {
		name string
		oql  string
	}{
		{"count_star", "SELECT COUNT(*) FROM sales"},
		{"sum_group_by", "SELECT region, SUM(amount) FROM sales GROUP BY region"},
		{"multi_agg", "SELECT region, COUNT(*), SUM(amount), AVG(amount), MIN(amount), MAX(amount) FROM sales GROUP BY region"},
		{"with_where", "SELECT region, SUM(amount) FROM sales WHERE quantity > 50 GROUP BY region"},
		{"multi_group", "SELECT region, product, SUM(amount) FROM sales GROUP BY region, product"},
	}

	iters := 50

	for _, q := range queries {
		stmt := parseOQL(t, q.oql)

		// Go path (no aggregate push-down because store is wrapped)
		goStart := time.Now()
		for i := 0; i < iters; i++ {
			goExec.ExecuteWithStore(env.ctx, stmt, goStore)
		}
		goDur := time.Since(goStart)

		// Push-down path (aggregate push-down via adapted table)
		pdStart := time.Now()
		for i := 0; i < iters; i++ {
			pdExec.ExecuteWithStore(env.ctx, stmt, env.store)
		}
		pdDur := time.Since(pdStart)

		ratio := float64(goDur) / float64(pdDur)
		t.Logf("%-20s  Go: %8s  Push-down: %8s  Speedup: %.1fx",
			q.name, goDur, pdDur, ratio)
	}
}
