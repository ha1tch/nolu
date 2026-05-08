// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

// execution_path_bench_test.go
//
// Comparative benchmarks for all six OQL execution paths. Uses the golden
// database (500 rows per entity, seeded once by TestMain) to keep setup
// costs near zero.
//
// Execution paths under test:
//
//   Path 1: PushFull      — entire SELECT as single SQL (adapted entities)
//   Path 2: PushAggregate — GROUP BY + aggregates in SQL, HAVING in Go (adapted)
//   Path 3: PushWhere+    — WHERE/ORDER BY/LIMIT via json_extract (blob entities)
//   Path 4: B4 predicate  — inline JSON tokenisation with predicate filtering
//   Path 5: Go + fields   — selective field extraction, filter/sort in Go
//   Path 6: Go full       — List all, deserialise all, filter/sort in Go
//
// Naming convention:
//   BenchmarkPath_<category>/<path_label>/<query_shape>
//
// Run examples:
//   go test ./pkg/oql/ -bench='BenchmarkPath_' -benchmem -count=3
//   go test ./pkg/oql/ -bench='BenchmarkPath_Adapted' -benchmem
//   go test ./pkg/oql/ -bench='BenchmarkPath_Blob' -benchmem
//   go test ./pkg/oql/ -bench='BenchmarkPath_Threshold' -benchmem

import (
	"context"
	"math"
	"testing"

	"github.com/ha1tch/xolu/pkg/jsonic"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/tsqlparser/ast"
	"github.com/rs/zerolog"
)

// =========================================================================
// Store wrappers that selectively expose/hide interfaces to force the
// executor down a specific path. Each wrapper documents which path it
// targets and why.
// =========================================================================

// plainStore hides ALL optional interfaces (FieldQueryable, FilterableStore,
// AggregateQueryable). The executor has no choice but to call List() and
// process everything in Go.
//
// Forces: Path 6 (Go full)
type plainStore struct {
	storage.Store
}

// fieldOnlyStore exposes FieldQueryable but hides FilterableStore and
// AggregateQueryable. The executor can call ListWithFields for selective
// extraction, but cannot push predicates into tokenisation.
//
// Forces: Path 5 (Go + fields) when threshold is high enough to prevent
// SQL push-down.
type fieldOnlyStore struct {
	storage.Store
	fq storage.FieldQueryable
}

func (s *fieldOnlyStore) ListWithFields(ctx context.Context, entity string, fields []string) ([]map[string]interface{}, error) {
	return s.fq.ListWithFields(ctx, entity, fields)
}

func (s *fieldOnlyStore) QueryWithFields(ctx context.Context, sqlQuery string, args []interface{}, fields []string) ([]map[string]interface{}, error) {
	return s.fq.QueryWithFields(ctx, sqlQuery, args, fields)
}

// filterableOnlyStore exposes FilterableStore (and therefore FieldQueryable)
// but hides AggregateQueryable. The executor can push predicates into JSON
// tokenisation but cannot push aggregates or full queries to SQL.
//
// Forces: Path 4 (B4 predicate) when threshold is high enough to prevent
// SQL push-down and the query has a compilable WHERE and specific fields.
type filterableOnlyStore struct {
	storage.Store
	fs storage.FilterableStore
}

func (s *filterableOnlyStore) ListWithFields(ctx context.Context, entity string, fields []string) ([]map[string]interface{}, error) {
	return s.fs.ListWithFields(ctx, entity, fields)
}

func (s *filterableOnlyStore) QueryWithFields(ctx context.Context, sqlQuery string, args []interface{}, fields []string) ([]map[string]interface{}, error) {
	return s.fs.QueryWithFields(ctx, sqlQuery, args, fields)
}

func (s *filterableOnlyStore) ListWithFieldsAndFilter(ctx context.Context, entity string, fields []string, preds *jsonic.PredicateSet) ([]map[string]interface{}, error) {
	return s.fs.ListWithFieldsAndFilter(ctx, entity, fields, preds)
}

// blobPushStore exposes Queryable and FieldQueryable but hides
// AggregateQueryable and FilterableStore. The executor can push WHERE
// to SQL via json_extract but cannot push aggregates or use B4.
//
// Forces: Path 3 (PushWhere+) when threshold is low enough.
type blobPushStore struct {
	storage.Store
	q  storage.Queryable
	fq storage.FieldQueryable
}

func (s *blobPushStore) Capabilities() storage.QueryCapabilities { return s.q.Capabilities() }
func (s *blobPushStore) CountEntities(ctx context.Context, entity string) (int, error) {
	return s.q.CountEntities(ctx, entity)
}
func (s *blobPushStore) QueryWithPlan(ctx context.Context, sql string, args []interface{}) ([]map[string]interface{}, error) {
	return s.q.QueryWithPlan(ctx, sql, args)
}
func (s *blobPushStore) ListWithFields(ctx context.Context, entity string, fields []string) ([]map[string]interface{}, error) {
	return s.fq.ListWithFields(ctx, entity, fields)
}
func (s *blobPushStore) QueryWithFields(ctx context.Context, sqlQuery string, args []interface{}, fields []string) ([]map[string]interface{}, error) {
	return s.fq.QueryWithFields(ctx, sqlQuery, args, fields)
}

// =========================================================================
// Benchmark environment builder
// =========================================================================

type pathBenchEnv struct {
	store *storage.SQLiteStore
	ctx   context.Context

	// Pre-built executors, one per path. Nil entries mean "not applicable
	// for this entity type" (e.g. PushFull only works with adapted).
	pushFull *Executor // Path 1
	pushAgg  *Executor // Path 2
	pushSQL  *Executor // Path 3
	b4       *Executor // Path 4
	goField  *Executor // Path 5
	goFull   *Executor // Path 6
}

func newPathBenchEnv(b *testing.B) *pathBenchEnv {
	b.Helper()

	// Suppress planner debug logs — they interleave with benchmark output
	// and swallow sub-benchmark labels.
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(zerolog.DebugLevel) })

	store := openGoldenStore(b)
	ctx := context.Background()

	env := &pathBenchEnv{store: store, ctx: ctx}

	// --- Path 1: PushFull (adapted entities only) ---
	env.pushFull = &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithDialectAndThreshold(&SQLiteDialect{}, 1),
		dialect:    &SQLiteDialect{},
	}

	// --- Path 2: PushAggregate (adapted entities only) ---
	// Same executor as PushFull — the planner decides PushAggregate vs
	// PushFull based on query translatability, not executor config.
	env.pushAgg = env.pushFull

	// --- Path 3: PushWhere+ (blob entities, SQL json_extract) ---
	bp := &blobPushStore{Store: store, q: store, fq: store}
	env.pushSQL = &Executor{
		store:      bp,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(1), // no dialect → blob path only
		dialect:    &SQLiteDialect{},
	}

	// --- Path 4: B4 predicate (inline tokenisation) ---
	fo := &filterableOnlyStore{Store: store, fs: store}
	env.b4 = &Executor{
		store:      fo,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32), // force Go fallback
	}

	// --- Path 5: Go + fields (selective extraction, no predicate push) ---
	fonly := &fieldOnlyStore{Store: store, fq: store}
	env.goField = &Executor{
		store:      fonly,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
	}

	// --- Path 6: Go full (bare Store, no optimisations) ---
	plain := &plainStore{Store: store}
	env.goFull = &Executor{
		store:      plain,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
	}

	return env
}

// run parses and executes a query through the given executor.
func (env *pathBenchEnv) run(b *testing.B, exec *Executor, oql string) { //nolint:unused
	b.Helper()
	e := &Engine{}
	stmt, err := e.parse(oql)
	if err != nil {
		b.Fatalf("parse %q: %v", oql, err)
	}
	_, err = exec.ExecuteWithTenant(env.ctx, stmt, "")
	if err != nil {
		b.Fatalf("execute %q: %v", oql, err)
	}
}

// runAll runs all applicable paths as sub-benchmarks for a given query.
// The paths slice selects which executors to exercise.
type pathEntry struct {
	name string
	exec *Executor
}

func (env *pathBenchEnv) runPaths(b *testing.B, query string, paths []pathEntry) {
	b.Helper()
	// Pre-parse once to validate the query and avoid repeated parse cost.
	e := &Engine{}
	stmt, err := e.parse(query)
	if err != nil {
		b.Fatalf("parse %q: %v", query, err)
	}

	for _, p := range paths {
		if p.exec == nil {
			continue
		}
		b.Run(p.name, func(b *testing.B) {
			// Validate the query works before timing.
			if _, err := p.exec.ExecuteWithTenant(env.ctx, stmt, ""); err != nil {
				b.Skipf("%s: %v", p.name, err)
			}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				p.exec.ExecuteWithTenant(env.ctx, stmt, "")
			}
		})
	}
}

// =========================================================================
// Section 1: Adapted entity benchmarks (items — 500 rows)
//
// Compare PushFull vs PushAggregate vs Go-path for the same queries.
// The adapted "items" entity has native columns: region, product,
// category, amount (decimal), unit_price (decimal), quantity, active.
// =========================================================================

func BenchmarkPath_Adapted_WhereEq(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT region, product, amount FROM items WHERE category = 'electronics'`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_WhereRange(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT region, product, quantity FROM items WHERE quantity > 50`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_WhereOrderByLimit(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT TOP 10 region, product, amount FROM items WHERE category = 'electronics' ORDER BY amount DESC`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_GroupByCount(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT region, COUNT(*) FROM items GROUP BY region`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_GroupByMultiAgg(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT category, COUNT(*), SUM(quantity), AVG(quantity) FROM items GROUP BY category`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_GroupByHaving(b *testing.B) {
	env := newPathBenchEnv(b)
	// Note: HAVING pushes down with PushFull only if fully translatable.
	// Otherwise it falls back to PushAggregate (SQL aggregates, Go HAVING).
	query := `SELECT region, COUNT(*) FROM items GROUP BY region HAVING COUNT(*) > 50`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_Distinct(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT DISTINCT region, category FROM items`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_Complex(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category HAVING COUNT(*) > 5 ORDER BY region`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

func BenchmarkPath_Adapted_SelectStar(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT * FROM items`

	env.runPaths(b, query, []pathEntry{
		{"PushFull", env.pushFull},
		{"GoPath", env.goFull},
	})
}

// =========================================================================
// Section 2: Blob entity benchmarks (sensors — 500 rows)
//
// Compare all four blob-applicable paths: PushWhere (SQL json_extract),
// B4 (inline tokenisation), Go+fields (selective extraction), Go full.
// =========================================================================

func BenchmarkPath_Blob_WhereEq(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT code, status, value FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_WhereCompound(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT code, status FROM sensors WHERE status = 'active' AND floor = 3`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_WhereRange(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT code, value FROM sensors WHERE value > 500.0`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_WhereLike(b *testing.B) {
	env := newPathBenchEnv(b)
	// LIKE is pushable to SQL but NOT compilable by B4.
	// B4 should fall through to Go+fields for this query.
	query := `SELECT code, status FROM sensors WHERE code LIKE 'SENS-00%'`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_WhereOrderByTop(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT TOP 10 code, value FROM sensors WHERE status = 'active' ORDER BY value DESC`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_SelectStar(b *testing.B) {
	env := newPathBenchEnv(b)
	// SELECT * disables B4 and Go+fields (isSelectStar=true).
	// Only PushSQL (for WHERE) and GoFull apply.
	query := `SELECT * FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_SelectStarNoWhere(b *testing.B) {
	env := newPathBenchEnv(b)
	// No WHERE, SELECT * — pure full scan. Only GoFull is applicable.
	query := `SELECT * FROM sensors`

	env.runPaths(b, query, []pathEntry{
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_GroupByCount(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT status, COUNT(*) FROM sensors GROUP BY status`

	env.runPaths(b, query, []pathEntry{
		// Blob entities can't push aggregates — only Go paths.
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Blob_OrderByNoWhere(b *testing.B) {
	env := newPathBenchEnv(b)
	// ORDER BY without WHERE: push-down won't help (ORDER BY alone not pushed).
	query := `SELECT code, value FROM sensors ORDER BY value DESC`

	env.runPaths(b, query, []pathEntry{
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

// =========================================================================
// Section 3: Threshold boundary benchmarks (blob entities)
//
// The SQLite blob push-down threshold is 50 rows. These benchmarks test
// cardinalities around the boundary to validate the crossover point.
//
// These seed their own small databases (not golden) because we need
// precise control over row counts.
// =========================================================================

func benchThreshold(b *testing.B, n int, query string) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(zerolog.DebugLevel) })

	env := newBenchEnv(b, n)

	b.Run("Go", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.runQuery(b, env.goExec, query)
		}
	})

	b.Run("PushDown", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.runQuery(b, env.pdExec, query)
		}
	})
}

func BenchmarkPath_Threshold_20(b *testing.B) {
	benchThreshold(b, 20, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPath_Threshold_35(b *testing.B) {
	benchThreshold(b, 35, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPath_Threshold_50(b *testing.B) {
	benchThreshold(b, 50, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPath_Threshold_75(b *testing.B) {
	benchThreshold(b, 75, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPath_Threshold_100(b *testing.B) {
	benchThreshold(b, 100, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPath_Threshold_200(b *testing.B) {
	benchThreshold(b, 200, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPath_Threshold_500(b *testing.B) {
	benchThreshold(b, 500, "SELECT * FROM pulses WHERE status = 'critical'")
}

// Same shape with ORDER BY + TOP to show compound push-down crossover.
func BenchmarkPath_Threshold_WhereOrderByTop_50(b *testing.B) {
	benchThreshold(b, 50, "SELECT TOP 5 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkPath_Threshold_WhereOrderByTop_100(b *testing.B) {
	benchThreshold(b, 100, "SELECT TOP 5 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkPath_Threshold_WhereOrderByTop_500(b *testing.B) {
	benchThreshold(b, 500, "SELECT TOP 5 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

// =========================================================================
// Section 4: Selectivity benchmarks (blob entities)
//
// Same cardinality (500 rows from golden), different selectivities.
// Measures how result set size affects each path.
// =========================================================================

func BenchmarkPath_Selectivity_Broad(b *testing.B) {
	// ~50% selectivity: half the rows match (status has 4 values, so ~25% each,
	// but we OR two to get ~50%).
	env := newPathBenchEnv(b)
	query := `SELECT code, status, value FROM sensors WHERE status = 'active' OR status = 'inactive'`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Selectivity_Medium(b *testing.B) {
	// ~25% selectivity: 1 of 4 status values.
	env := newPathBenchEnv(b)
	query := `SELECT code, status, value FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_Selectivity_Narrow(b *testing.B) {
	// ~1% selectivity: specific sensor + status.
	env := newPathBenchEnv(b)
	query := `SELECT code, status, value FROM sensors WHERE code = 'SENS-0042' AND status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"PushSQL", env.pushSQL},
		{"B4", env.b4},
		{"GoFull", env.goFull},
	})
}

// =========================================================================
// Section 5: Field count impact benchmarks
//
// Same query shape, different number of projected fields. Measures the
// overhead of deserialising unused fields.
// =========================================================================

func BenchmarkPath_FieldCount_1(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT code FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_FieldCount_3(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT code, status, value FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_FieldCount_All(b *testing.B) {
	env := newPathBenchEnv(b)
	query := `SELECT code, status, category, value, floor, nullable FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"B4", env.b4},
		{"GoFields", env.goField},
		{"GoFull", env.goFull},
	})
}

func BenchmarkPath_FieldCount_Star(b *testing.B) {
	env := newPathBenchEnv(b)
	// SELECT * disables B4 and GoFields.
	query := `SELECT * FROM sensors WHERE status = 'active'`

	env.runPaths(b, query, []pathEntry{
		{"GoFull", env.goFull},
	})
}

// =========================================================================
// Section 6: Parse overhead isolation
//
// Measures the cost of parsing alone vs parse+execute, so that benchmark
// consumers can subtract parsing overhead from execution numbers.
// =========================================================================

func BenchmarkPath_ParseOverhead(b *testing.B) {
	queries := map[string]string{
		"SimpleSelect":      `SELECT * FROM sensors`,
		"WhereEq":           `SELECT code FROM sensors WHERE status = 'active'`,
		"WhereCompound":     `SELECT code FROM sensors WHERE status = 'active' AND floor = 3`,
		"GroupByHaving":     `SELECT region, COUNT(*) FROM items GROUP BY region HAVING COUNT(*) > 50`,
		"ComplexAdapted":    `SELECT region, category, COUNT(*), SUM(quantity) FROM items WHERE active = true GROUP BY region, category ORDER BY region`,
		"WhereOrderByLimit": `SELECT TOP 10 code, value FROM sensors WHERE status = 'active' ORDER BY value DESC`,
	}

	for name, query := range queries {
		b.Run(name, func(b *testing.B) {
			e := &Engine{}
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				stmt, err := e.parse(query)
				if err != nil {
					b.Fatalf("parse: %v", err)
				}
				_ = stmt
			}
		})
	}
}

// =========================================================================
// Section 7: Adapted vs Blob head-to-head
//
// Same logical query shape, same data distribution, but one against the
// adapted "items" entity (native columns) and one against the blob
// "assets" entity (json_extract). Isolates the storage layout benefit.
// =========================================================================

func BenchmarkPath_HeadToHead_WhereEq(b *testing.B) {
	env := newPathBenchEnv(b)

	// Adapted: native column WHERE
	b.Run("Adapted_PushFull", func(b *testing.B) {
		e := &Engine{}
		stmt, _ := e.parse(`SELECT region, product FROM items WHERE category = 'electronics'`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.pushFull.ExecuteWithTenant(env.ctx, stmt, "")
		}
	})

	// Blob: json_extract WHERE
	b.Run("Blob_PushSQL", func(b *testing.B) {
		e := &Engine{}
		stmt, _ := e.parse(`SELECT code, category FROM sensors WHERE status = 'active'`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.pushSQL.ExecuteWithTenant(env.ctx, stmt, "")
		}
	})

	// Blob: Go full scan
	b.Run("Blob_GoFull", func(b *testing.B) {
		e := &Engine{}
		stmt, _ := e.parse(`SELECT code, category FROM sensors WHERE status = 'active'`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.goFull.ExecuteWithTenant(env.ctx, stmt, "")
		}
	})
}

func BenchmarkPath_HeadToHead_GroupBy(b *testing.B) {
	env := newPathBenchEnv(b)

	b.Run("Adapted_PushFull", func(b *testing.B) {
		e := &Engine{}
		stmt, _ := e.parse(`SELECT category, COUNT(*), SUM(quantity) FROM items GROUP BY category`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.pushFull.ExecuteWithTenant(env.ctx, stmt, "")
		}
	})

	b.Run("Blob_GoFull", func(b *testing.B) {
		e := &Engine{}
		stmt, _ := e.parse(`SELECT category, COUNT(*), SUM(value) FROM sensors GROUP BY category`)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			env.goFull.ExecuteWithTenant(env.ctx, stmt, "")
		}
	})
}

// Verify interface assertions at compile time.
var _ storage.FieldQueryable = (*fieldOnlyStore)(nil)
var _ storage.FilterableStore = (*filterableOnlyStore)(nil)
var _ storage.Queryable = (*blobPushStore)(nil)
var _ storage.FieldQueryable = (*blobPushStore)(nil)

// Ensure none of the wrapper types accidentally satisfy interfaces they
// shouldn't. These are runtime checks because Go doesn't have "not
// implements" compile-time assertions.
func init() {
	var ps interface{} = &plainStore{}
	if _, ok := ps.(storage.FieldQueryable); ok {
		panic("plainStore must NOT satisfy FieldQueryable")
	}
	if _, ok := ps.(storage.FilterableStore); ok {
		panic("plainStore must NOT satisfy FilterableStore")
	}

	var fos interface{} = &fieldOnlyStore{}
	if _, ok := fos.(storage.FilterableStore); ok {
		panic("fieldOnlyStore must NOT satisfy FilterableStore")
	}

	var fls interface{} = &filterableOnlyStore{}
	if _, ok := fls.(storage.Queryable); ok {
		panic("filterableOnlyStore must NOT satisfy Queryable")
	}
}

// Suppress unused import warnings for ast — used by compile-time checks
// in runPaths which calls ExecuteWithTenant with a parsed statement.
var _ ast.Statement
