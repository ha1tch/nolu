// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

// jsonic_crossover_bench_test.go
//
// Diagnostic benchmarks to determine when jsonic field extraction (GoFields)
// and inline predicate filtering (B4) pay off versus full json.Unmarshal
// (GoFull). Measures individual cost components and crossover points.
//
// Findings to characterize:
//   1. Per-row tokenisation overhead (fixed cost of jsonic regardless of fields)
//   2. Field count impact: how many fields must be SKIPPED before GoFields wins?
//   3. B4 per-row allocation overhead (predSeen, predMatched, pending, outputLookup)
//   4. Selectivity impact: what rejection rate makes B4's map-skip worthwhile?
//   5. Row count threshold for each path to be net-positive
//
// Run:
//   go test ./pkg/oql/ -bench='BenchmarkJsonic_' -benchmem -count=3
//   go test ./pkg/oql/ -bench='BenchmarkJsonic_Crossover' -benchmem -count=5

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"

	"github.com/ha1tch/xolu/pkg/jsonic"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/rs/zerolog"
)

// =========================================================================
// Section A: Raw tokenisation vs json.Unmarshal
//
// Isolates the base cost of tokenising a blob without extracting anything.
// This is the floor cost that any jsonic path pays per row.
// =========================================================================

func BenchmarkJsonic_TokeniseOnly(b *testing.B) {
	// Build representative JSON blobs matching the golden sensors schema:
	// ~8 fields, mix of string/number/bool/null values.
	blobs := make([][]byte, 500)
	for i := 0; i < 500; i++ {
		blobs[i] = []byte(fmt.Sprintf(
			`{"id":%d,"code":"SENS-%04d","status":"%s","category":"%s","value":%.1f,"floor":%d,"tenant_id":"t%d","nullable":%s}`,
			i+1, i,
			[]string{"active", "inactive", "maintenance", "decommissioned"}[i%4],
			[]string{"temperature", "pressure", "humidity", "flow"}[i%4],
			float64(i)*1.5, i%10, i%3,
			func() string {
				if i%2 == 0 {
					return fmt.Sprintf(`"val_%d"`, i)
				}
				return "null"
			}(),
		))
	}

	b.Run("Tokenise", func(b *testing.B) {
		tok := jsonic.GetTokeniser()
		defer jsonic.PutTokeniser(tok)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tok.Tokenise(blobs[i%500])
		}
	})

	b.Run("JsonUnmarshal", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			var data map[string]interface{}
			json.Unmarshal(blobs[i%500], &data)
		}
	})
}

// =========================================================================
// Section B: Field extraction crossover
//
// GoFields (ListWithFields) tokenises + extracts N fields.
// GoFull (List via json.Unmarshal) deserialises everything.
//
// The question: how many fields must be SKIPPED (not extracted) before
// the tokenise+extract cost is lower than full deserialisation?
//
// The sensors entity has 8 fields. We test extracting 1, 2, 3, 5, 7, 8
// fields to find where the crossover disappears.
// =========================================================================

func BenchmarkJsonic_Crossover_FieldSkip(b *testing.B) {
	env := newPathBenchEnv(b)

	fieldSets := []struct {
		label  string
		fields string // SELECT list
		skip   int    // how many fields are NOT in the list
	}{
		{"skip7of8", "code", 7},
		{"skip6of8", "code, status", 6},
		{"skip5of8", "code, status, value", 5},
		{"skip3of8", "code, status, value, category, floor", 3},
		{"skip1of8", "code, status, category, value, floor, nullable, tenant_id", 1},
		{"skip0of8", "code, status, category, value, floor, nullable, tenant_id, id", 0},
	}

	for _, fs := range fieldSets {
		query := fmt.Sprintf("SELECT %s FROM sensors WHERE status = 'active'", fs.fields)

		b.Run(fs.label, func(b *testing.B) {
			b.Run("GoFields", func(b *testing.B) {
				e := &Engine{}
				stmt, _ := e.parse(query)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					env.goField.ExecuteWithTenant(env.ctx, stmt, "")
				}
			})
			b.Run("GoFull", func(b *testing.B) {
				e := &Engine{}
				stmt, _ := e.parse(query)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					env.goFull.ExecuteWithTenant(env.ctx, stmt, "")
				}
			})
		})
	}
}

// =========================================================================
// Section C: B4 selectivity crossover
//
// B4 pays 4 allocations per row (predSeen, predMatched, pending,
// outputLookup) but saves the output map allocation for rejected rows.
// The question: at what rejection rate does B4 break even?
//
// We test against sensors (500 rows) with predicates of varying
// selectivity:
//   ~0% match  = all rows rejected (B4 max advantage)
//   ~5% match  = most rows rejected
//   ~25% match = 1 in 4 statuses
//   ~50% match = half the rows
//   ~100% match = all rows pass (B4 max overhead)
// =========================================================================

func BenchmarkJsonic_Crossover_B4Selectivity(b *testing.B) {
	env := newPathBenchEnv(b)

	cases := []struct {
		label string
		query string
		pct   string // approximate match percentage
	}{
		{"match0pct", "SELECT code, status FROM sensors WHERE code = 'NONEXISTENT'", "~0%"},
		{"match5pct", "SELECT code, status FROM sensors WHERE code = 'SENS-0042' AND status = 'active'", "~1%"},
		{"match25pct", "SELECT code, status FROM sensors WHERE status = 'active'", "~25%"},
		{"match50pct", "SELECT code, status FROM sensors WHERE floor < 5", "~50%"},
		// No predicate: B4 still runs but with no rejections.
		// This isolates pure B4 overhead vs GoFields.
	}

	for _, tc := range cases {
		b.Run(tc.label, func(b *testing.B) {
			b.Run("B4", func(b *testing.B) {
				e := &Engine{}
				stmt, err := e.parse(tc.query)
				if err != nil {
					b.Fatalf("parse: %v", err)
				}
				// Warm up / verify it works.
				if _, err := env.b4.ExecuteWithTenant(env.ctx, stmt, ""); err != nil {
					b.Skipf("B4: %v", err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					env.b4.ExecuteWithTenant(env.ctx, stmt, "")
				}
			})
			b.Run("GoFields", func(b *testing.B) {
				e := &Engine{}
				stmt, _ := e.parse(tc.query)
				if _, err := env.goField.ExecuteWithTenant(env.ctx, stmt, ""); err != nil {
					b.Skipf("GoFields: %v", err)
				}
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					env.goField.ExecuteWithTenant(env.ctx, stmt, "")
				}
			})
			b.Run("GoFull", func(b *testing.B) {
				e := &Engine{}
				stmt, _ := e.parse(tc.query)
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					env.goFull.ExecuteWithTenant(env.ctx, stmt, "")
				}
			})
		})
	}
}

// =========================================================================
// Section D: Row count crossover (GoFields vs GoFull)
//
// At small row counts, the fixed overhead of jsonic setup (atom building,
// tokeniser pool checkout) might dominate. At large row counts, the
// per-row saving accumulates. Where's the crossover?
//
// These seed their own databases at specific sizes.
// =========================================================================

func benchJsonicRowCount(b *testing.B, n int) {
	zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	b.Cleanup(func() { zerolog.SetGlobalLevel(zerolog.DebugLevel) })

	dir := b.TempDir()
	dbPath := fmt.Sprintf("%s/bench_%d.db", dir, n)
	store, err := storage.NewSQLiteStore(dbPath, storage.SQLiteConfig{})
	if err != nil {
		b.Fatalf("NewSQLiteStore: %v", err)
	}
	b.Cleanup(func() { store.Close() })

	ctx := context.Background()
	statuses := []string{"active", "inactive", "maintenance", "decommissioned"}

	// Seed using raw SQL for speed.
	store.DB().Exec("PRAGMA synchronous=OFF")
	store.DB().Exec("PRAGMA journal_mode=MEMORY")
	tx, _ := store.DB().BeginTx(ctx, nil)
	ins, _ := tx.Prepare(`INSERT INTO entities (tenant_id, entity_type, id, data) VALUES (0, 'things', ?, ?)`)
	for i := 0; i < n; i++ {
		data := fmt.Sprintf(
			`{"id":%d,"code":"T-%04d","status":"%s","value":%.1f,"floor":%d,"zone":"z%d","tag1":"aaa","tag2":"bbb"}`,
			i+1, i, statuses[i%4], float64(i)*1.5, i%10, i%5,
		)
		ins.Exec(i+1, data)
	}
	ins.Close()
	tx.Exec(`INSERT INTO entity_sequences (tenant_id, entity_type, next_id) VALUES (0, 'things', ?)
		ON CONFLICT(tenant_id, entity_type) DO UPDATE SET next_id = ?`, n, n)
	tx.Commit()

	// GoFields executor: FieldQueryable only, no push-down
	fonly := &fieldOnlyStore{Store: store, fq: store}
	goFieldExec := &Executor{
		store:      fonly,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
	}

	// GoFull executor: bare Store
	plain := &plainStore{Store: store}
	goFullExec := &Executor{
		store:      plain,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
	}

	// B4 executor
	filt := &filterableOnlyStore{Store: store, fs: store}
	b4Exec := &Executor{
		store:      filt,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
	}

	// Query: extract 2 of 8 fields with WHERE (25% selectivity)
	query := "SELECT code, value FROM things WHERE status = 'active'"

	e := &Engine{}
	stmt, _ := e.parse(query)

	b.Run("GoFields", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			goFieldExec.ExecuteWithTenant(ctx, stmt, "")
		}
	})
	b.Run("B4", func(b *testing.B) {
		if _, err := b4Exec.ExecuteWithTenant(ctx, stmt, ""); err != nil {
			b.Skipf("B4: %v", err)
		}
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			b4Exec.ExecuteWithTenant(ctx, stmt, "")
		}
	})
	b.Run("GoFull", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			goFullExec.ExecuteWithTenant(ctx, stmt, "")
		}
	})
}

func BenchmarkJsonic_RowCount_10(b *testing.B) {
	benchJsonicRowCount(b, 10)
}

func BenchmarkJsonic_RowCount_50(b *testing.B) {
	benchJsonicRowCount(b, 50)
}

func BenchmarkJsonic_RowCount_100(b *testing.B) {
	benchJsonicRowCount(b, 100)
}

func BenchmarkJsonic_RowCount_500(b *testing.B) {
	benchJsonicRowCount(b, 500)
}

func BenchmarkJsonic_RowCount_1000(b *testing.B) {
	benchJsonicRowCount(b, 1000)
}

func BenchmarkJsonic_RowCount_5000(b *testing.B) {
	benchJsonicRowCount(b, 5000)
}

// =========================================================================
// Section E: Per-row allocation anatomy
//
// Isolate the allocation cost of B4's FilterExtractFromTokens vs
// extractFieldsFromBlob vs json.Unmarshal on a single representative
// blob. This strips away SQLite I/O to measure pure processing cost.
// =========================================================================

func BenchmarkJsonic_PerRow_Anatomy(b *testing.B) {
	blob := []byte(`{"id":42,"code":"SENS-0042","status":"active","category":"temperature","value":63.0,"floor":2,"tenant_id":"t0","nullable":"val_42"}`)

	// json.Unmarshal
	b.Run("JsonUnmarshal", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			var data map[string]interface{}
			json.Unmarshal(blob, &data)
		}
	})

	// Tokenise + FilterExtractFromTokens (simulating GoFields: no predicates, 2 fields)
	b.Run("FilterExtract_NoPred_2of8", func(b *testing.B) {
		outputFields := jsonic.MakeFilterFieldEntries([]string{"code", "value"})
		tok := jsonic.GetTokeniser()
		defer jsonic.PutTokeniser(tok)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tok.Tokenise(blob)
			jsonic.FilterExtractFromTokens(tok, outputFields, nil)
		}
	})

	// Tokenise + FilterExtractFromTokens (simulating GoFields: no predicates, 6 fields)
	b.Run("FilterExtract_NoPred_6of8", func(b *testing.B) {
		outputFields := jsonic.MakeFilterFieldEntries([]string{"code", "status", "category", "value", "floor", "nullable"})
		tok := jsonic.GetTokeniser()
		defer jsonic.PutTokeniser(tok)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tok.Tokenise(blob)
			jsonic.FilterExtractFromTokens(tok, outputFields, nil)
		}
	})

	// Tokenise + FilterExtractFromTokens (B4 path, predicate matches)
	b.Run("B4_Match_2of8", func(b *testing.B) {
		outputFields := jsonic.MakeFilterFieldEntries([]string{"code", "value"})
		preds := makeTestPreds("status", "active")
		tok := jsonic.GetTokeniser()
		defer jsonic.PutTokeniser(tok)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tok.Tokenise(blob)
			jsonic.FilterExtractFromTokens(tok, outputFields, preds)
		}
	})

	// Tokenise + FilterExtractFromTokens (B4 path, predicate REJECTS)
	b.Run("B4_Reject_2of8", func(b *testing.B) {
		outputFields := jsonic.MakeFilterFieldEntries([]string{"code", "value"})
		preds := makeTestPreds("status", "inactive") // blob has "active"
		tok := jsonic.GetTokeniser()
		defer jsonic.PutTokeniser(tok)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tok.Tokenise(blob)
			jsonic.FilterExtractFromTokens(tok, outputFields, preds)
		}
	})

	// Tokenise only (floor cost)
	b.Run("TokeniseOnly", func(b *testing.B) {
		tok := jsonic.GetTokeniser()
		defer jsonic.PutTokeniser(tok)
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			tok.Tokenise(blob)
		}
	})
}

// makeTestPreds builds a simple equality predicate for benchmarking.
func makeTestPreds(field, value string) *jsonic.PredicateSet {
	pred := jsonic.MakeFieldPredicate(field, jsonic.FieldString, jsonic.OpEq, value)
	return jsonic.NewPredicateSet([]jsonic.FieldPredicate{pred})
}
