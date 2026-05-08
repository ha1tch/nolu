// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ha1tch/tsqlparser"
)

// setupBenchmarkEngine creates an engine with N records for benchmarking
func setupBenchmarkEngine(b *testing.B, recordCount int) (*Engine, func()) {
	store := newMockStore()
	ctx := context.Background()

	// Create test data with realistic distribution
	statuses := []string{"active", "inactive", "maintenance", "error"}
	for i := 0; i < recordCount; i++ {
		store.Create(ctx, "items", map[string]interface{}{
			"category_id": float64(i % 100),                      // 100 categorys
			"status":  statuses[i%len(statuses)],             // 4 statuses
			"value": float64(20.0 + float64(i%100)/10.0),   // 20.0-29.9
			"name":    fmt.Sprintf("item-%d", i),
		})
	}

	tmpDir, err := os.MkdirTemp("", "oql-bench")
	if err != nil {
		b.Fatalf("Failed to create temp dir: %v", err)
	}

	itemsDir := filepath.Join(tmpDir, "items")
	if err := os.MkdirAll(itemsDir, 0755); err != nil {
		b.Fatalf("Failed to create items dir: %v", err)
	}

	engine := NewEngine(store, tmpDir)

	cleanup := func() {
		os.RemoveAll(tmpDir)
	}

	return engine, cleanup
}

// =============================================================================
// SELECT Benchmarks
// =============================================================================

func BenchmarkSelectAll_100(b *testing.B) {
	benchmarkSelectAll(b, 100)
}

func BenchmarkSelectAll_1000(b *testing.B) {
	benchmarkSelectAll(b, 1000)
}

func BenchmarkSelectAll_10000(b *testing.B) {
	benchmarkSelectAll(b, 10000)
}

func benchmarkSelectAll(b *testing.B, recordCount int) {
	engine, cleanup := setupBenchmarkEngine(b, recordCount)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT * FROM items")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// SELECT with WHERE Benchmarks
// =============================================================================

func BenchmarkSelectWhere_100(b *testing.B) {
	benchmarkSelectWhere(b, 100)
}

func BenchmarkSelectWhere_1000(b *testing.B) {
	benchmarkSelectWhere(b, 1000)
}

func BenchmarkSelectWhere_10000(b *testing.B) {
	benchmarkSelectWhere(b, 10000)
}

func benchmarkSelectWhere(b *testing.B, recordCount int) {
	engine, cleanup := setupBenchmarkEngine(b, recordCount)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT * FROM items WHERE status = 'active'")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkSelectWhereNumeric_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT * FROM items WHERE value > 25.0")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkSelectWhereCompound_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT * FROM items WHERE status = 'active' AND category_id = 1")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// SELECT with ORDER BY Benchmarks
// =============================================================================

func BenchmarkSelectOrderBy_100(b *testing.B) {
	benchmarkSelectOrderBy(b, 100)
}

func BenchmarkSelectOrderBy_1000(b *testing.B) {
	benchmarkSelectOrderBy(b, 1000)
}

func BenchmarkSelectOrderBy_10000(b *testing.B) {
	benchmarkSelectOrderBy(b, 10000)
}

func benchmarkSelectOrderBy(b *testing.B, recordCount int) {
	engine, cleanup := setupBenchmarkEngine(b, recordCount)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT * FROM items ORDER BY value DESC")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// GROUP BY Benchmarks
// =============================================================================

func BenchmarkGroupByCount_100(b *testing.B) {
	benchmarkGroupByCount(b, 100)
}

func BenchmarkGroupByCount_1000(b *testing.B) {
	benchmarkGroupByCount(b, 1000)
}

func BenchmarkGroupByCount_10000(b *testing.B) {
	benchmarkGroupByCount(b, 10000)
}

func benchmarkGroupByCount(b *testing.B, recordCount int) {
	engine, cleanup := setupBenchmarkEngine(b, recordCount)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT category_id, COUNT(*) FROM items GROUP BY category_id")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkGroupByAvg_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT category_id, AVG(value) FROM items GROUP BY category_id")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkGroupByMultipleAgg_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT category_id, COUNT(*), AVG(value), MIN(value), MAX(value) FROM items GROUP BY category_id")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkGroupByWithHaving_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT category_id, COUNT(*) FROM items GROUP BY category_id HAVING COUNT(*) > 5")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// DISTINCT Benchmarks
// =============================================================================

func BenchmarkDistinct_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT DISTINCT status FROM items")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkDistinctMultiColumn_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT DISTINCT category_id, status FROM items")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// INSERT Benchmarks
// =============================================================================

func BenchmarkInsertSingle(b *testing.B) {
	store := newMockStore()
	tmpDir, _ := os.MkdirTemp("", "oql-bench")
	defer os.RemoveAll(tmpDir)
	os.MkdirAll(filepath.Join(tmpDir, "items"), 0755)
	engine := NewEngine(store, tmpDir)

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, fmt.Sprintf(
			"INSERT INTO items (category_id, status, value) VALUES (%d, 'active', 25.5)", i%100))
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkInsertBatch10(b *testing.B) {
	store := newMockStore()
	tmpDir, _ := os.MkdirTemp("", "oql-bench")
	defer os.RemoveAll(tmpDir)
	os.MkdirAll(filepath.Join(tmpDir, "items"), 0755)
	engine := NewEngine(store, tmpDir)

	ctx := context.Background()

	// Build batch insert query
	values := ""
	for j := 0; j < 10; j++ {
		if j > 0 {
			values += ", "
		}
		values += fmt.Sprintf("(%d, 'active', 25.5)", j)
	}
	query := "INSERT INTO items (category_id, status, value) VALUES " + values

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, query)
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// UPDATE Benchmarks
// =============================================================================

func BenchmarkUpdate_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "UPDATE items SET status = 'updated' WHERE category_id = 1")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// Complex Query Benchmarks
// =============================================================================

func BenchmarkComplexQuery_1000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 1000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx,
			"SELECT category_id, COUNT(*), AVG(value) FROM items WHERE status = 'active' GROUP BY category_id HAVING COUNT(*) > 2 ORDER BY category_id")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

func BenchmarkTopN_10000(b *testing.B) {
	engine, cleanup := setupBenchmarkEngine(b, 10000)
	defer cleanup()

	ctx := context.Background()
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		_, err := engine.Execute(ctx, "SELECT TOP 10 * FROM items ORDER BY value DESC")
		if err != nil {
			b.Fatalf("Execute failed: %v", err)
		}
	}
}

// =============================================================================
// Parse-only Benchmark (measures parsing overhead)
// =============================================================================

func BenchmarkParseOnly(b *testing.B) {
	queries := []string{
		"SELECT * FROM items",
		"SELECT * FROM items WHERE status = 'active'",
		"SELECT category_id, COUNT(*) FROM items GROUP BY category_id",
		"INSERT INTO items (category_id, status) VALUES (1, 'active')",
		"UPDATE items SET status = 'inactive' WHERE category_id = 1",
	}

	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		query := queries[i%len(queries)]
		_, errs := tsqlparser.Parse(query)
		if len(errs) > 0 {
			b.Fatalf("Parse failed: %v", errs)
		}
	}
}
