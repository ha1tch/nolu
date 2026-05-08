// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

// ---------------------------------------------------------------------------
// Benchmark infrastructure
//
// Each benchmark seeds a SQLite store with N records, then runs a query
// through two executors:
//   - goExec:  threshold=MaxInt32 (forces Go path always)
//   - pdExec:  threshold=1 (forces push-down always)
//
// The benchmark name encodes: path/cardinality/query_shape
// e.g., BenchmarkPushDown/Go/10000/Where
// ---------------------------------------------------------------------------

type benchEnv struct {
	store  *storage.SQLiteStore
	goExec *Executor
	pdExec *Executor
	ctx    context.Context
}

func newBenchEnv(b *testing.B, n int) *benchEnv {
	b.Helper()

	dir := b.TempDir()
	dbPath := filepath.Join(dir, "bench.db")
	store, err := storage.NewSQLiteStore(dbPath, storage.SQLiteConfig{})
	if err != nil {
		b.Fatalf("NewSQLiteStore: %v", err)
	}
	b.Cleanup(func() { store.Close() })

	ctx := context.Background()

	// Seed N pulse-beat records (simulating a sensor with millions of readings)
	statuses := []string{"normal", "elevated", "critical", "warning"}
	for i := 0; i < n; i++ {
		_, err := store.Create(ctx, "pulses", map[string]interface{}{
			"sensor_id": fmt.Sprintf("SENS-%04d", i%100),
			"bpm":       60.0 + float64(i%80),
			"status":    statuses[i%len(statuses)],
			"timestamp": fmt.Sprintf("2026-01-%02dT%02d:%02d:%02dZ", (i%28)+1, (i/3600)%24, (i/60)%60, i%60),
			"quality":   i % 10,
			"zone":      fmt.Sprintf("zone_%d", i%5),
		})
		if err != nil {
			b.Fatalf("seed %d: %v", i, err)
		}
		// Log progress for large seeds
		// Progress logging omitted in benchmarks (no-op).
	}

	goExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}

	pdExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(1),
		dialect:    &SQLiteDialect{},
	}

	return &benchEnv{store: store, goExec: goExec, pdExec: pdExec, ctx: ctx}
}

// runQuery parses and executes a query, failing the benchmark on error.
func (env *benchEnv) runQuery(b *testing.B, exec *Executor, query string) {
	b.Helper()
	e := &Engine{}
	stmt, err := e.parse(query)
	if err != nil {
		b.Fatalf("parse: %v", err)
	}
	_, err = exec.ExecuteWithTenant(env.ctx, stmt, "")
	if err != nil {
		b.Fatalf("execute: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Parameterised benchmark
// ---------------------------------------------------------------------------

func benchmarkQuery(b *testing.B, n int, label string, query string) {
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

// ---------------------------------------------------------------------------
// WHERE-only benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWhere_100(b *testing.B) {
	benchmarkQuery(b, 100, "Where", "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkWhere_1K(b *testing.B) {
	benchmarkQuery(b, 1000, "Where", "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkWhere_10K(b *testing.B) {
	benchmarkQuery(b, 10000, "Where", "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkWhere_100K(b *testing.B) {
	benchmarkQuery(b, 100000, "Where", "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkWhere_1M(b *testing.B) {
	benchmarkQuery(b, 1000000, "Where", "SELECT * FROM pulses WHERE status = 'critical'")
}

// ---------------------------------------------------------------------------
// WHERE + ORDER BY benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWhereOrderBy_100(b *testing.B) {
	benchmarkQuery(b, 100, "WhereOrderBy", "SELECT * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderBy_1K(b *testing.B) {
	benchmarkQuery(b, 1000, "WhereOrderBy", "SELECT * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderBy_10K(b *testing.B) {
	benchmarkQuery(b, 10000, "WhereOrderBy", "SELECT * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderBy_100K(b *testing.B) {
	benchmarkQuery(b, 100000, "WhereOrderBy", "SELECT * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderBy_1M(b *testing.B) {
	benchmarkQuery(b, 1000000, "WhereOrderBy", "SELECT * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

// ---------------------------------------------------------------------------
// WHERE + ORDER BY + TOP (LIMIT) benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWhereOrderByTop_100(b *testing.B) {
	benchmarkQuery(b, 100, "WhereOrderByTop", "SELECT TOP 10 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderByTop_1K(b *testing.B) {
	benchmarkQuery(b, 1000, "WhereOrderByTop", "SELECT TOP 10 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderByTop_10K(b *testing.B) {
	benchmarkQuery(b, 10000, "WhereOrderByTop", "SELECT TOP 10 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderByTop_100K(b *testing.B) {
	benchmarkQuery(b, 100000, "WhereOrderByTop", "SELECT TOP 10 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkWhereOrderByTop_1M(b *testing.B) {
	benchmarkQuery(b, 1000000, "WhereOrderByTop", "SELECT TOP 10 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

// ---------------------------------------------------------------------------
// Selective WHERE (narrow result set) benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWhereSelective_10K(b *testing.B) {
	benchmarkQuery(b, 10000, "WhereSelective",
		"SELECT * FROM pulses WHERE sensor_id = 'SENS-0042' AND status = 'critical'")
}

func BenchmarkWhereSelective_100K(b *testing.B) {
	benchmarkQuery(b, 100000, "WhereSelective",
		"SELECT * FROM pulses WHERE sensor_id = 'SENS-0042' AND status = 'critical'")
}

func BenchmarkWhereSelective_1M(b *testing.B) {
	benchmarkQuery(b, 1000000, "WhereSelective",
		"SELECT * FROM pulses WHERE sensor_id = 'SENS-0042' AND status = 'critical'")
}

// ---------------------------------------------------------------------------
// Numeric range benchmarks
// ---------------------------------------------------------------------------

func BenchmarkWhereBetween_10K(b *testing.B) {
	benchmarkQuery(b, 10000, "WhereBetween",
		"SELECT * FROM pulses WHERE bpm BETWEEN 100.0 AND 120.0")
}

func BenchmarkWhereBetween_100K(b *testing.B) {
	benchmarkQuery(b, 100000, "WhereBetween",
		"SELECT * FROM pulses WHERE bpm BETWEEN 100.0 AND 120.0")
}

func BenchmarkWhereBetween_1M(b *testing.B) {
	benchmarkQuery(b, 1000000, "WhereBetween",
		"SELECT * FROM pulses WHERE bpm BETWEEN 100.0 AND 120.0")
}
