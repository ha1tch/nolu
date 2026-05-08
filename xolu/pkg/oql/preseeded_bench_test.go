// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"math"
	"os"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

const preseededDB = "/home/claude/bench_1m.db"

func openPreseeded(b *testing.B) *benchEnv {
	b.Helper()

	if _, err := os.Stat(preseededDB); err != nil {
		b.Skipf("Pre-seeded DB not found at %s — run seed_bench_fast.go first", preseededDB)
	}

	store, err := storage.NewSQLiteStore(preseededDB, storage.SQLiteConfig{})
	if err != nil {
		b.Fatalf("NewSQLiteStore: %v", err)
	}
	b.Cleanup(func() { store.Close() })

	// Verify count
	count, _ := store.CountEntities(context.Background(), "pulses")
	if count < 900000 {
		b.Fatalf("Expected ~1M records, got %d", count)
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

	return &benchEnv{store: store, goExec: goExec, pdExec: pdExec, ctx: context.Background()}
}

func benchPreseeded(b *testing.B, query string) {
	env := openPreseeded(b)

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

func BenchmarkPreseeded1M_Where(b *testing.B) {
	benchPreseeded(b, "SELECT * FROM pulses WHERE status = 'critical'")
}

func BenchmarkPreseeded1M_WhereOrderByTop(b *testing.B) {
	benchPreseeded(b, "SELECT TOP 10 * FROM pulses WHERE status = 'critical' ORDER BY bpm DESC")
}

func BenchmarkPreseeded1M_Selective(b *testing.B) {
	benchPreseeded(b, "SELECT * FROM pulses WHERE sensor_id = 'SENS-0042' AND status = 'critical'")
}

func BenchmarkPreseeded1M_Between(b *testing.B) {
	benchPreseeded(b, "SELECT * FROM pulses WHERE bpm BETWEEN 100.0 AND 120.0")
}

func BenchmarkPreseeded1M_Like(b *testing.B) {
	benchPreseeded(b, "SELECT * FROM pulses WHERE sensor_id LIKE 'SENS-00%'")
}
