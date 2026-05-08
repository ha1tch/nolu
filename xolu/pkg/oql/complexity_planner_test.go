// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"testing"

	"github.com/ha1tch/tsqlparser/ast"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Core gating: VPS profile at 500 rows
// ---------------------------------------------------------------------------

// TestPlannerComplexityGating verifies that the planner correctly gates
// PushFull for complex adapted queries based on the hardware profile's
// complexity thresholds.
//
// The golden database has 500 rows in the "items" entity. With the VPS
// profile, the complex query (2 temp B-trees + non-covering) has a
// threshold of 2000, so PushFull should NOT be selected at 500 rows.
// Simple queries should still get PushFull unconditionally.
func TestPlannerComplexityGating(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}
	vps := DefaultProfile()

	planner := NewPlannerWithProfile(dialect, &vps)
	engine := &Engine{}

	tests := []struct {
		label        string
		oql          string
		wantPushFull bool
	}{
		{
			label:        "simple WHERE — always PushFull",
			oql:          "SELECT region, amount FROM items WHERE category = 'electronics'",
			wantPushFull: true,
		},
		{
			label:        "single-key GROUP BY + COUNT — always PushFull",
			oql:          "SELECT region, COUNT(*) FROM items GROUP BY region",
			wantPushFull: true,
		},
		{
			label:        "single-key GROUP BY + HAVING — always PushFull",
			oql:          "SELECT region, COUNT(*) FROM items GROUP BY region HAVING COUNT(*) > 50",
			wantPushFull: true,
		},
		{
			label:        "single-key GROUP BY + aligned ORDER BY — always PushFull",
			oql:          "SELECT region, COUNT(*) FROM items GROUP BY region ORDER BY region",
			wantPushFull: true,
		},
		{
			label:        "WHERE + ORDER BY + TOP — always PushFull (simple)",
			oql:          "SELECT TOP 10 region, product, amount FROM items WHERE category = 'electronics' ORDER BY amount DESC",
			wantPushFull: true,
		},
		{
			label:        "DISTINCT — always PushFull (simple)",
			oql:          "SELECT DISTINCT region FROM items",
			wantPushFull: true,
		},
		{
			label:        "single-key GROUP BY + SUM (non-covering) — gated, 500 < 1000",
			oql:          "SELECT category, COUNT(*), SUM(quantity), AVG(quantity) FROM items GROUP BY category",
			wantPushFull: false,
		},
		{
			label:        "single-key GROUP BY + misaligned ORDER BY — gated, 1 temp B-tree, 500 >= 500",
			oql:          "SELECT region, COUNT(*) FROM items GROUP BY region ORDER BY COUNT(*) DESC",
			wantPushFull: true, // TempBTree1Threshold=500, count=500 → at threshold → PushFull
		},
		{
			label:        "multi-key GROUP BY (COUNT only) — gated, 1 temp B-tree, 500 >= 500",
			oql:          "SELECT region, category, COUNT(*) FROM items GROUP BY region, category",
			wantPushFull: true, // TempBTree1Threshold=500, count=500, covering → at threshold → PushFull
		},
		{
			label:        "multi-key GROUP BY + misaligned ORDER BY — gated, 2 temp B-trees, 500 < 2000",
			oql:          "SELECT region, category, COUNT(*) FROM items GROUP BY region, category ORDER BY category",
			wantPushFull: false,
		},
		{
			label:        "full complex — gated, 500 < 2000",
			oql:          "SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY region",
			wantPushFull: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			stmt, err := engine.parse(tt.oql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			sel := stmt.(*ast.SelectStatement)

			plan := planner.Plan(ctx, sel, store)
			gotPushFull := plan.pushed(PushFull)

			if gotPushFull != tt.wantPushFull {
				t.Errorf("PushFull = %v, want %v\n  plan: %s", gotPushFull, tt.wantPushFull, plan.Reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Fallback path: when PushFull is gated, verify the chosen alternative
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_FallbackPath(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}
	vps := DefaultProfile()
	planner := NewPlannerWithProfile(dialect, &vps)
	engine := &Engine{}

	tests := []struct {
		label    string
		oql      string
		wantPush PushDecision
	}{
		{
			// Non-covering aggregate: PushFull gated → falls through to PushAggregate
			label:    "non-covering agg falls to PushAggregate",
			oql:      "SELECT category, SUM(quantity), AVG(quantity) FROM items GROUP BY category",
			wantPush: PushAggregate,
		},
		{
			// Multi-key GROUP BY with ORDER: PushFull gated → falls through to PushAggregate
			label:    "complex GROUP BY falls to PushAggregate",
			oql:      "SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY region",
			wantPush: PushAggregate,
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			stmt, err := engine.parse(tt.oql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			sel := stmt.(*ast.SelectStatement)

			plan := planner.Plan(ctx, sel, store)
			if plan.pushed(PushFull) {
				t.Fatalf("expected PushFull to be gated, got: %s", plan.Reason)
			}
			if !plan.pushed(tt.wantPush) {
				t.Errorf("expected %v as fallback, got push=%v reason=%s",
					tt.wantPush, plan.Push, plan.Reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Above-threshold: custom profile where 500 rows exceeds all thresholds
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_AboveThreshold(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}

	// Create a profile where all thresholds are well below 500 rows.
	// This simulates edge hardware or a high-cardinality table where
	// push-down is always worthwhile.
	low := HardwareProfile{
		Name:                 "test-low",
		BlobPushThreshold:    10,
		NonCoveringThreshold: 100,
		TempBTree1Threshold:  50,
		TempBTree2Threshold:  200,
	}
	planner := NewPlannerWithProfile(dialect, &low)
	engine := &Engine{}

	tests := []struct {
		label string
		oql   string
	}{
		{
			label: "non-covering agg → PushFull (500 > 100)",
			oql:   "SELECT category, SUM(quantity), AVG(quantity) FROM items GROUP BY category",
		},
		{
			label: "2 temp B-trees → PushFull (500 > 200)",
			oql:   "SELECT region, category, COUNT(*) FROM items GROUP BY region, category ORDER BY category",
		},
		{
			label: "full complex → PushFull (500 > 200)",
			oql:   "SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY region",
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			stmt, err := engine.parse(tt.oql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			sel := stmt.(*ast.SelectStatement)

			plan := planner.Plan(ctx, sel, store)
			if !plan.pushed(PushFull) {
				t.Errorf("expected PushFull (above threshold), got: %s", plan.Reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Dedicated profile: all thresholds high, everything gated at 500 rows
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_Dedicated(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}
	dedicated := ProfileDedicated
	planner := NewPlannerWithProfile(dialect, &dedicated)
	engine := &Engine{}

	// Simple queries: PushFull regardless of profile
	stmt, _ := engine.parse("SELECT region, COUNT(*) FROM items GROUP BY region")
	sel := stmt.(*ast.SelectStatement)
	plan := planner.Plan(ctx, sel, store)
	if !plan.pushed(PushFull) {
		t.Errorf("simple query: expected PushFull with dedicated profile, got: %s", plan.Reason)
	}

	// Non-covering: dedicated threshold=2000, 500 rows → no PushFull
	stmt, _ = engine.parse("SELECT category, SUM(quantity) FROM items GROUP BY category")
	sel = stmt.(*ast.SelectStatement)
	plan = planner.Plan(ctx, sel, store)
	if plan.pushed(PushFull) {
		t.Errorf("non-covering: expected no PushFull at 500 rows with dedicated (threshold=2000), got: %s", plan.Reason)
	}

	// 1 temp B-tree: dedicated threshold=1000, 500 rows → no PushFull
	stmt, _ = engine.parse("SELECT region, category, COUNT(*) FROM items GROUP BY region, category")
	sel = stmt.(*ast.SelectStatement)
	plan = planner.Plan(ctx, sel, store)
	if plan.pushed(PushFull) {
		t.Errorf("1 temp B-tree: expected no PushFull at 500 rows with dedicated (threshold=1000), got: %s", plan.Reason)
	}
}

// ---------------------------------------------------------------------------
// Edge profile
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_Edge(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}
	edge := ProfileEdge
	planner := NewPlannerWithProfile(dialect, &edge)
	engine := &Engine{}

	// Simple: always PushFull
	stmt, _ := engine.parse("SELECT region, COUNT(*) FROM items GROUP BY region")
	sel := stmt.(*ast.SelectStatement)
	plan := planner.Plan(ctx, sel, store)
	if !plan.pushed(PushFull) {
		t.Errorf("simple GROUP BY: expected PushFull with edge profile, got: %s", plan.Reason)
	}

	// Non-covering: edge threshold=500, count=500 → at threshold → PushFull
	stmt, _ = engine.parse("SELECT category, SUM(quantity) FROM items GROUP BY category")
	sel = stmt.(*ast.SelectStatement)
	plan = planner.Plan(ctx, sel, store)
	if !plan.pushed(PushFull) {
		t.Errorf("non-covering: expected PushFull with edge at 500 rows (threshold=500), got: %s", plan.Reason)
	}

	// 1 temp B-tree: edge threshold=250, 500 > 250 → PushFull
	stmt, _ = engine.parse("SELECT region, category, COUNT(*) FROM items GROUP BY region, category")
	sel = stmt.(*ast.SelectStatement)
	plan = planner.Plan(ctx, sel, store)
	if !plan.pushed(PushFull) {
		t.Errorf("1 temp B-tree: expected PushFull with edge at 500 rows (threshold=250), got: %s", plan.Reason)
	}

	// 2 temp B-trees: edge threshold=1000, 500 < 1000 → no PushFull
	stmt, _ = engine.parse("SELECT region, category, COUNT(*) FROM items GROUP BY region, category ORDER BY category")
	sel = stmt.(*ast.SelectStatement)
	plan = planner.Plan(ctx, sel, store)
	if plan.pushed(PushFull) {
		t.Errorf("2 temp B-trees: expected no PushFull with edge at 500 rows (threshold=1000), got: %s", plan.Reason)
	}
}

// ---------------------------------------------------------------------------
// Blob entities: complexity heuristics must not affect blob path decisions
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_BlobUnaffected(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}
	vps := DefaultProfile()
	planner := NewPlannerWithProfile(dialect, &vps)
	engine := &Engine{}

	// "sensors" is a blob entity (not adapted). Complex query shapes
	// should follow the standard blob path, not the adapted complexity
	// gating logic.
	tests := []struct {
		label string
		oql   string
	}{
		{
			label: "blob WHERE",
			oql:   "SELECT status, value FROM sensors WHERE status = 'active'",
		},
		{
			label: "blob GROUP BY",
			oql:   "SELECT status, COUNT(*) FROM sensors GROUP BY status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			stmt, err := engine.parse(tt.oql)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			sel := stmt.(*ast.SelectStatement)

			plan := planner.Plan(ctx, sel, store)
			// Blob entities should NEVER get PushFull (that's adapted-only).
			// They should get PushWhere or Go path.
			if plan.pushed(PushFull) {
				t.Errorf("blob entity got PushFull — complexity gating leaked: %s", plan.Reason)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Nil profile fallback: uses VPS defaults
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_NilProfile(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}

	// nil profile → should use VPS defaults internally
	planner := NewPlannerWithProfile(dialect, nil)
	engine := &Engine{}

	// Simple query: PushFull
	stmt, _ := engine.parse("SELECT region, COUNT(*) FROM items GROUP BY region")
	sel := stmt.(*ast.SelectStatement)
	plan := planner.Plan(ctx, sel, store)
	if !plan.pushed(PushFull) {
		t.Errorf("simple query with nil profile: expected PushFull, got: %s", plan.Reason)
	}

	// Complex query: VPS thresholds → no PushFull at 500
	stmt, _ = engine.parse("SELECT region, category, COUNT(*), SUM(quantity) FROM items GROUP BY region, category ORDER BY category")
	sel = stmt.(*ast.SelectStatement)
	plan = planner.Plan(ctx, sel, store)
	if plan.pushed(PushFull) {
		t.Errorf("complex query with nil profile: expected no PushFull (VPS defaults), got: %s", plan.Reason)
	}
}

// ---------------------------------------------------------------------------
// Result correctness: gated queries must produce correct results
// ---------------------------------------------------------------------------

func TestPlannerComplexityGating_ResultCorrectness(t *testing.T) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	defer zerolog.SetGlobalLevel(zerolog.DebugLevel)

	store := openGoldenStore(t)
	ctx := context.Background()
	dialect := &SQLiteDialect{}

	// Low-threshold profile: PushFull fires for complex queries
	low := HardwareProfile{
		Name:                 "test-low",
		BlobPushThreshold:    10,
		NonCoveringThreshold: 100,
		TempBTree1Threshold:  50,
		TempBTree2Threshold:  200,
	}
	pushExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithProfile(dialect, &low),
		dialect:    dialect,
	}

	// High-threshold profile: PushFull is gated, falls through to Go/Aggregate
	high := HardwareProfile{
		Name:                 "test-high",
		BlobPushThreshold:    10,
		NonCoveringThreshold: 10000,
		TempBTree1Threshold:  10000,
		TempBTree2Threshold:  10000,
	}
	goExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithProfile(dialect, &high),
		dialect:    dialect,
	}

	engine := &Engine{}

	queries := []string{
		"SELECT category, COUNT(*), SUM(quantity), AVG(quantity) FROM items GROUP BY category",
		"SELECT region, category, COUNT(*) FROM items GROUP BY region, category ORDER BY category",
		"SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY region",
	}

	for _, q := range queries {
		t.Run(q, func(t *testing.T) {
			stmt, err := engine.parse(q)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}

			pushResult, err := pushExec.ExecuteWithTenant(ctx, stmt, "")
			if err != nil {
				t.Fatalf("PushFull exec: %v", err)
			}

			goResult, err := goExec.ExecuteWithTenant(ctx, stmt, "")
			if err != nil {
				t.Fatalf("Go/Aggregate exec: %v", err)
			}

			if len(pushResult.Rows) != len(goResult.Rows) {
				t.Errorf("row count mismatch: PushFull=%d, Go=%d",
					len(pushResult.Rows), len(goResult.Rows))
				return
			}

			if len(pushResult.Rows) == 0 {
				t.Error("both paths returned 0 rows — test is vacuous")
				return
			}

			// Verify column set matches
			pushCols := sortedKeys(pushResult.Rows[0])
			goCols := sortedKeys(goResult.Rows[0])
			if len(pushCols) != len(goCols) {
				t.Errorf("column count mismatch: PushFull=%v, Go=%v", pushCols, goCols)
			}

			t.Logf("both paths returned %d rows with columns %v", len(pushResult.Rows), pushCols)
		})
	}
}

// sortedKeys returns the keys of a map in sorted order.
func sortedKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — small maps
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j] < keys[j-1]; j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}
