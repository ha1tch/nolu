// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/tsqlparser/ast"
)

// goldenCopy copies the golden database file into t.TempDir and returns
// the path to the copy. Each test gets its own file, so write-tests
// cannot corrupt the seed. For read-only tests the copy overhead is
// negligible (~1 MB file).
func goldenCopy(t testing.TB) string {
	t.Helper()

	if goldenPath == "" {
		t.Fatal("goldenPath not set — TestMain did not run")
	}

	src, err := os.Open(goldenPath)
	if err != nil {
		t.Fatalf("open golden: %v", err)
	}
	defer src.Close()

	dst := filepath.Join(t.TempDir(), "olu.golden")
	out, err := os.Create(dst)
	if err != nil {
		t.Fatalf("create golden copy: %v", err)
	}
	defer out.Close()

	if _, err := io.Copy(out, src); err != nil {
		t.Fatalf("copy golden: %v", err)
	}

	// Also copy the WAL and SHM files if they exist (they usually do
	// after the seeder closes with WAL mode). Missing files are fine.
	for _, suffix := range []string{"-wal", "-shm"} {
		if sf, err := os.Open(goldenPath + suffix); err == nil {
			df, _ := os.Create(dst + suffix)
			io.Copy(df, sf)
			sf.Close()
			df.Close()
		}
	}

	return dst
}

// openGoldenStore copies the golden database and opens it as a
// *storage.SQLiteStore. The store is registered for cleanup via t.Cleanup.
func openGoldenStore(t testing.TB) *storage.SQLiteStore {
	t.Helper()

	dbPath := goldenCopy(t)
	store, err := storage.NewSQLiteStore(dbPath, storage.SQLiteConfig{})
	if err != nil {
		t.Fatalf("NewSQLiteStore (golden): %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

// ---------------------------------------------------------------------------
// Shared test types
// ---------------------------------------------------------------------------

// nonAggStore wraps a Store so that it satisfies Queryable but not
// AggregateQueryable. This forces the executor down the Go-path
// (fetch all rows, aggregate in Go) rather than pushing aggregates
// to SQL. Used by adapted_pushdown, adapted_full_pushdown, and
// aggregate_pushdown tests.
type nonAggStore struct {
	storage.Store
	q storage.Queryable
}

func (s *nonAggStore) Capabilities() storage.QueryCapabilities {
	return s.q.Capabilities()
}

func (s *nonAggStore) CountEntities(ctx context.Context, entity string) (int, error) {
	return s.q.CountEntities(ctx, entity)
}

func (s *nonAggStore) QueryWithPlan(ctx context.Context, sql string, args []interface{}) ([]map[string]interface{}, error) {
	return s.q.QueryWithPlan(ctx, sql, args)
}

// parseOQL is a shared helper to parse an OQL statement, failing the
// test on error.
func parseOQL(t testing.TB, oql string) ast.Statement {
	t.Helper()
	engine := &Engine{}
	stmt, err := engine.parse(oql)
	if err != nil {
		t.Fatalf("parse %q: %v", oql, err)
	}
	return stmt
}
