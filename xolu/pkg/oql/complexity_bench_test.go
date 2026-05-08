// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

// complexity_bench_test.go
//
// Decomposes PushFull cost into three phases:
//   1. OQL parse
//   2. SQL generation (GenerateAdaptedSQL)
//   3. SQLite execution (AggregateQuery)
//
// This identifies whether the Complex query regression is caused by
// expensive SQL generation, an expensive SQLite query plan, or the
// denormalisation/post-processing step. The results inform what
// heuristics the planner needs to detect "query too complex for
// push-down."
//
// Run:
//   go test ./pkg/oql/ -bench='BenchmarkComplexity_' -benchmem -count=3

import (
	"context"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/tsqlparser/ast"
	"github.com/rs/zerolog"
)

type complexityEnv struct {
	store    *storage.SQLiteStore
	aggStore storage.AggregateQueryable
	dialect  SQLDialect
	ctx      context.Context
}

func newComplexityEnv(b *testing.B) *complexityEnv {
	b.Helper()
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(zerolog.DebugLevel) })

	store := openGoldenStore(b)
	return &complexityEnv{
		store:    store,
		aggStore: store,
		dialect:  &SQLiteDialect{},
		ctx:      context.Background(),
	}
}

// queries orders from simplest to most complex so the cost escalation
// is visible.
var complexityQueries = []struct {
	label string
	oql   string
	// Structural complexity markers for heuristic development:
	nWhere   int  // number of WHERE predicates
	nGroupBy int  // number of GROUP BY keys
	hasHaving bool
	hasOrder bool
	hasLimit bool
}{
	{
		label: "WhereEq",
		oql:   "SELECT region, product, amount FROM items WHERE category = 'electronics'",
		nWhere: 1,
	},
	{
		label: "WhereOrderLimit",
		oql:   "SELECT TOP 10 region, product, amount FROM items WHERE category = 'electronics' ORDER BY amount DESC",
		nWhere: 1, hasOrder: true, hasLimit: true,
	},
	{
		label: "GroupBy1",
		oql:   "SELECT region, COUNT(*) FROM items GROUP BY region",
		nGroupBy: 1,
	},
	{
		label: "GroupBy1_Having",
		oql:   "SELECT region, COUNT(*) FROM items GROUP BY region HAVING COUNT(*) > 50",
		nGroupBy: 1, hasHaving: true,
	},
	{
		label: "GroupBy1_MultiAgg",
		oql:   "SELECT category, COUNT(*), SUM(quantity), AVG(quantity) FROM items GROUP BY category",
		nGroupBy: 1,
	},
	{
		label:  "GroupBy2_Having_Order",
		oql:    "SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY region",
		nWhere: 1, nGroupBy: 2, hasHaving: true, hasOrder: true,
	},
}

// BenchmarkComplexity_Generate measures SQL generation time per query shape.
func BenchmarkComplexity_Generate(b *testing.B) {
	env := newComplexityEnv(b)
	engine := &Engine{}

	for _, q := range complexityQueries {
		stmt, err := engine.parse(q.oql)
		if err != nil {
			b.Fatalf("parse %q: %v", q.label, err)
		}
		sel := stmt.(*ast.SelectStatement)

		b.Run(q.label, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				_, err := GenerateAdaptedSQL(sel, "items", "", env.aggStore, env.dialect)
				if err != nil {
					b.Fatalf("generate: %v", err)
				}
			}
		})
	}
}

// BenchmarkComplexity_Execute measures SQLite execution time for the
// pre-generated SQL, isolating it from generation cost.
func BenchmarkComplexity_Execute(b *testing.B) {
	env := newComplexityEnv(b)
	engine := &Engine{}

	for _, q := range complexityQueries {
		stmt, err := engine.parse(q.oql)
		if err != nil {
			b.Fatalf("parse %q: %v", q.label, err)
		}
		sel := stmt.(*ast.SelectStatement)

		adaptedSQL, err := GenerateAdaptedSQL(sel, "items", "", env.aggStore, env.dialect)
		if err != nil {
			b.Fatalf("generate %q: %v", q.label, err)
		}

		b.Run(q.label, func(b *testing.B) {
			// Warm: ensure prepared statement is cached
			env.aggStore.AggregateQuery(env.ctx, adaptedSQL.SQL, adaptedSQL.Args, adaptedSQL.Aliases)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				env.aggStore.AggregateQuery(env.ctx, adaptedSQL.SQL, adaptedSQL.Args, adaptedSQL.Aliases)
			}
		})
	}
}

// BenchmarkComplexity_Full measures the full PushFull path (generate +
// execute + denormalise) per query shape, matching the production path.
func BenchmarkComplexity_Full(b *testing.B) {
	env := newComplexityEnv(b)
	engine := &Engine{}

	for _, q := range complexityQueries {
		stmt, err := engine.parse(q.oql)
		if err != nil {
			b.Fatalf("parse %q: %v", q.label, err)
		}
		sel := stmt.(*ast.SelectStatement)

		b.Run(q.label, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				adaptedSQL, _ := GenerateAdaptedSQL(sel, "items", "", env.aggStore, env.dialect)
				records, _ := env.aggStore.AggregateQuery(env.ctx, adaptedSQL.SQL, adaptedSQL.Args, adaptedSQL.Aliases)
				denormaliseAggregateDecimals(records, adaptedSQL.DecimalColumns, adaptedSQL.Aliases, sel.Columns)
			}
		})
	}
}

// BenchmarkComplexity_GoPath measures the Go-path cost for each query
// shape, so we can compute the exact crossover.
func BenchmarkComplexity_GoPath(b *testing.B) {
	env := newComplexityEnv(b)
	engine := &Engine{}

	// Build a Go-only executor (nonAggStore hides AggregateQueryable)
	goStore := &nonAggStore{Store: env.store, q: env.store}
	goExec := &Executor{
		store:      goStore,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(1<<31 - 1),
		dialect:    &SQLiteDialect{},
	}

	for _, q := range complexityQueries {
		stmt, err := engine.parse(q.oql)
		if err != nil {
			b.Fatalf("parse %q: %v", q.label, err)
		}

		b.Run(q.label, func(b *testing.B) {
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				goExec.ExecuteWithTenant(env.ctx, stmt, "")
			}
		})
	}
}

// BenchmarkComplexity_SQLPlan prints the generated SQL for each query shape
// so we can inspect what SQLite is being asked to do. This isn't a real
// benchmark — it runs once and logs the SQL.
func BenchmarkComplexity_SQLPlan(b *testing.B) {
	env := newComplexityEnv(b)
	engine := &Engine{}

	for _, q := range complexityQueries {
		stmt, err := engine.parse(q.oql)
		if err != nil {
			b.Fatalf("parse %q: %v", q.label, err)
		}
		sel := stmt.(*ast.SelectStatement)

		adaptedSQL, err := GenerateAdaptedSQL(sel, "items", "", env.aggStore, env.dialect)
		if err != nil {
			b.Fatalf("generate %q: %v", q.label, err)
		}

		b.Run(q.label, func(b *testing.B) {
			b.Logf("SQL: %s", adaptedSQL.SQL)
			b.Logf("Args: %v", adaptedSQL.Args)
			// Run EXPLAIN QUERY PLAN
			rows, err := env.store.DB().QueryContext(env.ctx, "EXPLAIN QUERY PLAN "+adaptedSQL.SQL, adaptedSQL.Args...)
			if err != nil {
				b.Logf("EXPLAIN failed: %v", err)
				return
			}
			defer rows.Close()
			for rows.Next() {
				var id, parent, notused int
				var detail string
				rows.Scan(&id, &parent, &notused, &detail)
				b.Logf("  PLAN: %s", detail)
			}
		})
	}
}
