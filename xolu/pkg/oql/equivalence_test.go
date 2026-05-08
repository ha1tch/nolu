// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"math"
	"sort"
	"testing"

	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/storage"
)

// ---------------------------------------------------------------------------
// Equivalence test infrastructure
//
// These tests verify that the push-down path and Go-side path produce
// identical results for every supported query shape. This is the primary
// defence against semantic divergence between the two execution paths.
// ---------------------------------------------------------------------------

// equivEnv holds a SQLite store seeded with test data and two engines:
// one that always uses the Go path, one that always uses push-down.
type equivEnv struct {
	store  *storage.SQLiteStore
	goExec *Executor     // planner with threshold=MaxInt (always Go path)
	pdExec *Executor     // planner with threshold=1 (always push-down)
	ctx    context.Context
}

func newEquivEnv(t *testing.T) *equivEnv {
	t.Helper()

	store := openGoldenStore(t)
	ctx := context.Background()

	// Go-path executor: threshold so high push-down never triggers
	goExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}

	// Push-down executor: threshold=1 so push-down always triggers
	pdExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(1),
		dialect:    &SQLiteDialect{},
	}

	return &equivEnv{
		store:  store,
		goExec: goExec,
		pdExec: pdExec,
		ctx:    ctx,
	}
}

// assertEquiv runs a query through both executors and compares results.
// If orderMatters is true, results must be in the same order.
func (env *equivEnv) assertEquiv(t *testing.T, query string, orderMatters bool) {
	t.Helper()

	// Parse once
	stmt, err := parseForEquiv(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}

	goResult, err := env.goExec.ExecuteWithTenant(env.ctx, stmt, "")
	if err != nil {
		t.Fatalf("Go-path %q: %v", query, err)
	}

	pdResult, err := env.pdExec.ExecuteWithTenant(env.ctx, stmt, "")
	if err != nil {
		t.Fatalf("push-down %q: %v", query, err)
	}

	if len(goResult.Rows) != len(pdResult.Rows) {
		t.Fatalf("row count mismatch: Go=%d push-down=%d\n  query: %s",
			len(goResult.Rows), len(pdResult.Rows), query)
	}

	if orderMatters {
		for i := range goResult.Rows {
			if !rowsEqual(goResult.Rows[i], pdResult.Rows[i]) {
				t.Errorf("row %d mismatch:\n  Go:  %v\n  PD:  %v\n  query: %s",
					i, goResult.Rows[i], pdResult.Rows[i], query)
			}
		}
	} else {
		// Order-independent: sort both by a canonical key and compare
		goSorted := canonicalSort(goResult.Rows)
		pdSorted := canonicalSort(pdResult.Rows)
		for i := range goSorted {
			if !rowsEqual(goSorted[i], pdSorted[i]) {
				t.Errorf("row %d mismatch (order-independent):\n  Go:  %v\n  PD:  %v\n  query: %s",
					i, goSorted[i], pdSorted[i], query)
			}
		}
	}
}

// assertEquivWithTenant runs with tenant scoping.
func (env *equivEnv) assertEquivWithTenant(t *testing.T, query, tenantID string, orderMatters bool) {
	t.Helper()

	stmt, err := parseForEquiv(query)
	if err != nil {
		t.Fatalf("parse %q: %v", query, err)
	}

	goResult, err := env.goExec.ExecuteWithTenant(env.ctx, stmt, tenantID)
	if err != nil {
		t.Fatalf("Go-path %q: %v", query, err)
	}

	pdResult, err := env.pdExec.ExecuteWithTenant(env.ctx, stmt, tenantID)
	if err != nil {
		t.Fatalf("push-down %q: %v", query, err)
	}

	if len(goResult.Rows) != len(pdResult.Rows) {
		t.Fatalf("row count mismatch (tenant=%s): Go=%d push-down=%d\n  query: %s",
			tenantID, len(goResult.Rows), len(pdResult.Rows), query)
	}

	if !orderMatters {
		goSorted := canonicalSort(goResult.Rows)
		pdSorted := canonicalSort(pdResult.Rows)
		for i := range goSorted {
			if !rowsEqual(goSorted[i], pdSorted[i]) {
				t.Errorf("row %d mismatch (tenant=%s):\n  Go:  %v\n  PD:  %v",
					i, tenantID, goSorted[i], pdSorted[i])
			}
		}
	}
}

func parseForEquiv(sql string) (*ast.SelectStatement, error) {
	engine := &Engine{}
	stmt, err := engine.parse(sql)
	if err != nil {
		return nil, err
	}
	s, ok := stmt.(*ast.SelectStatement)
	if !ok {
		return nil, fmt.Errorf("expected SELECT, got %T", stmt)
	}
	return s, nil
}

// rowsEqual compares two row maps, handling float64 comparison.
func rowsEqual(a, b map[string]interface{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if !valuesEqual(va, vb) {
			return false
		}
	}
	return true
}

func valuesEqual(a, b interface{}) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	// Handle float64 comparison with tolerance
	af, aOk := toFloatSafe(a)
	bf, bOk := toFloatSafe(b)
	if aOk && bOk {
		return math.Abs(af-bf) < 1e-9
	}
	return fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b)
}

// canonicalSort produces a deterministic ordering of rows by their string
// representation. Used for order-independent comparison.
func canonicalSort(rows []map[string]interface{}) []map[string]interface{} {
	sorted := make([]map[string]interface{}, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool {
		return fmt.Sprintf("%v", sorted[i]) < fmt.Sprintf("%v", sorted[j])
	})
	return sorted
}

// ---------------------------------------------------------------------------
// TestEquivalence: shared-environment subtests
//
// All subtests share a single env created by newEquivEnv.
// The golden database is copied once; all queries are read-only.
// ---------------------------------------------------------------------------

func TestEquivalence(t *testing.T) {
	env := newEquivEnv(t)

	// assertEquiv tests
	for _, tc := range []struct {
		name    string
		query   string
		ordered bool
	}{
		{"WhereStringEquals", "SELECT * FROM sensors WHERE status = 'active'", false},
		{"WhereNumericGreater", "SELECT * FROM sensors WHERE value > 500.0", false},
		{"WhereNumericLessEqual", "SELECT * FROM sensors WHERE floor <= 3", false},
		{"WhereNumericEquals", "SELECT * FROM sensors WHERE floor = 5", false},
		{"WhereCompound", "SELECT * FROM sensors WHERE status = 'active' AND value > 100.0", false},
		{"WhereCompoundOr", "SELECT * FROM sensors WHERE status = 'active' OR status = 'maintenance'", false},
		{"WhereLike", "SELECT * FROM events WHERE message LIKE '%sensor SENS-00%'", false},
		{"WhereIsNull", "SELECT * FROM sensors WHERE nullable IS NULL", false},
		{"WhereIsNotNull", "SELECT * FROM sensors WHERE nullable IS NOT NULL", false},
		{"WhereBetween", "SELECT * FROM sensors WHERE value BETWEEN 100.0 AND 200.0", false},
		{"WhereIn", "SELECT * FROM sensors WHERE status IN ('active', 'maintenance')", false},
		{"WhereNotEqual", "SELECT * FROM sensors WHERE status != 'decommissioned'", false},
		{"OrderBy", "SELECT * FROM sensors WHERE status = 'active' ORDER BY code", true},
		{"OrderByDesc", "SELECT * FROM sensors WHERE status = 'active' ORDER BY value DESC", true},
		{"OrderByMultiple", "SELECT * FROM sensors WHERE value > 100.0 ORDER BY category, code", true},
		{"TopWithWhere", "SELECT TOP 10 * FROM sensors WHERE status = 'active' ORDER BY code", true},
		{"TopWithWhereAndOrderByDesc", "SELECT TOP 5 * FROM readings WHERE quality = 0 ORDER BY value DESC", true},
		{"EmptyResult", "SELECT * FROM sensors WHERE status = 'nonexistent_status'", false},
		{"SelectSpecificColumns", "SELECT code, status, value FROM sensors WHERE status = 'active'", false},
		{"NotBetween", "SELECT * FROM sensors WHERE value NOT BETWEEN 200.0 AND 400.0", false},
		{"NotIn", "SELECT * FROM sensors WHERE status NOT IN ('inactive', 'decommissioned')", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env.assertEquiv(t, tc.query, tc.ordered)
		})
	}

}

func TestEquivalence_WhereWithTenant(t *testing.T) {
env := newEquivEnv(t)
	env.assertEquivWithTenant(t, "SELECT * FROM sensors WHERE status = 'active'", "t0", false)
}

func TestEquivalence_WhereWithTenantAndOrderBy(t *testing.T) {
env := newEquivEnv(t)
	env.assertEquivWithTenant(t, "SELECT * FROM sensors WHERE value > 200.0 ORDER BY code", "t1", true)
}

func TestEquivalence_WhereWithGroupBy(t *testing.T) {
// GROUP BY stays on Go path regardless. The WHERE push narrows the input,
	// then Go-side aggregation runs on the filtered results.
	env := newEquivEnv(t)
	env.assertEquiv(t,
		"SELECT status, COUNT(*) as cnt FROM sensors WHERE value > 200.0 GROUP BY status", false)
}

func TestEquivalence_CompoundDeep(t *testing.T) {
env := newEquivEnv(t)
	env.assertEquiv(t,
		"SELECT * FROM sensors WHERE (status = 'active' AND value > 100.0) OR (status = 'maintenance' AND floor <= 3)",
		false)
}
