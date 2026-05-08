// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/tsqlparser/ast"
)

// ---------------------------------------------------------------------------
// FieldQueryable E2E tests (Phase B2)
// ---------------------------------------------------------------------------
// These tests verify that the executor correctly routes blob-entity queries
// through FieldQueryable.ListWithFields / QueryWithFields when the SELECT
// list names specific fields. Results must match the Go-path (SELECT *)
// for the same data.
// ---------------------------------------------------------------------------

type fqEnv struct {
	store  *storage.SQLiteStore
	goExec *Executor // Go path, SELECT * (full deserialisation)
	fqExec *Executor // Go path, will use FieldQueryable for non-star
	pdExec *Executor // Push-down, will use QueryWithFields for non-star
	ctx    context.Context
}

func newFQEnv(t *testing.T) *fqEnv {
	t.Helper()

	dir := t.TempDir()
	dbPath := filepath.Join(dir, "field_query_e2e.db")
	store, err := storage.NewSQLiteStore(dbPath, storage.SQLiteConfig{})
	if err != nil {
		t.Fatalf("NewSQLiteStore: %v", err)
	}
	t.Cleanup(func() { store.Close() })

	ctx := context.Background()

	// Insert blob entities (not adapted) with varied types.
	records := []map[string]interface{}{
		{
			"name":   "Alice",
			"email":  "alice@example.com",
			"age":    float64(30),
			"active": true,
			"score":  float64(95.5),
			"region": "north",
			"tags":   []interface{}{"admin", "user"},
		},
		{
			"name":   "Bob",
			"email":  "bob@example.com",
			"age":    float64(25),
			"active": false,
			"score":  float64(82.3),
			"region": "south",
			"tags":   []interface{}{"user"},
		},
		{
			"name":   "Charlie",
			"email":  "charlie@example.com",
			"age":    float64(40),
			"active": true,
			"score":  float64(71.0),
			"region": "north",
			"tags":   []interface{}{"admin"},
		},
		{
			"name":   "Diana",
			"email":  "diana@example.com",
			"age":    float64(35),
			"active": true,
			"score":  float64(88.9),
			"region": "south",
			"tags":   []interface{}{"user", "moderator"},
		},
	}

	for _, rec := range records {
		if _, err := store.Create(ctx, "people", rec); err != nil {
			t.Fatalf("create: %v", err)
		}
	}

	// Go-path executor: threshold too high for push-down.
	goExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}

	// Same Go-path executor, but queries with specific fields will
	// route through FieldQueryable automatically.
	fqExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithThreshold(math.MaxInt32),
		dialect:    &SQLiteDialect{},
	}

	// Push-down executor: threshold=1.
	pdExec := &Executor{
		store:      store,
		aggregator: NewAggregator(),
		planner:    NewPlannerWithDialectAndThreshold(&SQLiteDialect{}, 1),
		dialect:    &SQLiteDialect{},
	}

	return &fqEnv{
		store:  store,
		goExec: goExec,
		fqExec: fqExec,
		pdExec: pdExec,
		ctx:    ctx,
	}
}

func parseFQOQL(t *testing.T, oql string) ast.Statement {
	t.Helper()
	engine := &Engine{}
	stmt, err := engine.parse(oql)
	if err != nil {
		t.Fatalf("parse %q: %v", oql, err)
	}
	return stmt
}

// fqRun executes the same query on goExec (SELECT * path) and fqExec
// (specific fields path) and compares the results.
func (env *fqEnv) fqRun(t *testing.T, starOQL, selectiveOQL string) {
	t.Helper()

	starStmt := parseFQOQL(t, starOQL)
	selStmt := parseFQOQL(t, selectiveOQL)

	starResult, err := env.goExec.ExecuteWithStore(env.ctx, starStmt, env.store)
	if err != nil {
		t.Fatalf("star query %q: %v", starOQL, err)
	}

	selResult, err := env.fqExec.ExecuteWithStore(env.ctx, selStmt, env.store)
	if err != nil {
		t.Fatalf("selective query %q: %v", selectiveOQL, err)
	}

	if len(starResult.Rows) != len(selResult.Rows) {
		t.Fatalf("row count: star=%d selective=%d\n  star: %s\n  sel:  %s",
			len(starResult.Rows), len(selResult.Rows), starOQL, selectiveOQL)
	}

	// Selective result should contain only the fields in the SELECT list,
	// and those values should match the star result.
	for i, selRow := range selResult.Rows {
		starRow := starResult.Rows[i]
		for key, selVal := range selRow {
			starVal, ok := starRow[key]
			if !ok {
				t.Errorf("row %d: selective has key %q not in star result", i, key)
				continue
			}
			sJSON, _ := json.Marshal(selVal)
			fJSON, _ := json.Marshal(starVal)
			if string(sJSON) != string(fJSON) {
				t.Errorf("row %d key %q: selective=%s star=%s\n  query: %s",
					i, key, sJSON, fJSON, selectiveOQL)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestFieldQuery_E2E_BasicSelect(t *testing.T) {
	env := newFQEnv(t)
	env.fqRun(t,
		"SELECT * FROM people",
		"SELECT name, email FROM people",
	)
}

func TestFieldQuery_E2E_SelectWithWhere(t *testing.T) {
	env := newFQEnv(t)
	env.fqRun(t,
		"SELECT * FROM people WHERE active = true",
		"SELECT name, score FROM people WHERE active = true",
	)
}

func TestFieldQuery_E2E_SelectWithOrderBy(t *testing.T) {
	env := newFQEnv(t)
	env.fqRun(t,
		"SELECT * FROM people ORDER BY age",
		"SELECT name, age FROM people ORDER BY age",
	)
}

func TestFieldQuery_E2E_SelectWithWhereAndOrderBy(t *testing.T) {
	env := newFQEnv(t)
	env.fqRun(t,
		"SELECT * FROM people WHERE region = 'north' ORDER BY score DESC",
		"SELECT name, score FROM people WHERE region = 'north' ORDER BY score DESC",
	)
}

func TestFieldQuery_E2E_SelectWithTop(t *testing.T) {
	env := newFQEnv(t)
	env.fqRun(t,
		"SELECT TOP 2 * FROM people ORDER BY age",
		"SELECT TOP 2 name, age FROM people ORDER BY age",
	)
}

func TestFieldQuery_E2E_PushDownVsGoPath(t *testing.T) {
	env := newFQEnv(t)

	oql := "SELECT name, score FROM people WHERE active = true ORDER BY score DESC"
	stmt := parseFQOQL(t, oql)

	goResult, err := env.fqExec.ExecuteWithStore(env.ctx, stmt, env.store)
	if err != nil {
		t.Fatalf("Go-path: %v", err)
	}

	pdResult, err := env.pdExec.ExecuteWithStore(env.ctx, stmt, env.store)
	if err != nil {
		t.Fatalf("Push-down: %v", err)
	}

	if len(goResult.Rows) != len(pdResult.Rows) {
		t.Fatalf("row count: Go=%d PD=%d", len(goResult.Rows), len(pdResult.Rows))
	}

	for i := range goResult.Rows {
		for key, goVal := range goResult.Rows[i] {
			pdVal := pdResult.Rows[i][key]
			gJSON, _ := json.Marshal(goVal)
			pJSON, _ := json.Marshal(pdVal)
			if string(gJSON) != string(pJSON) {
				t.Errorf("row %d key %q: Go=%s PD=%s", i, key, gJSON, pJSON)
			}
		}
	}
}

func TestFieldQuery_E2E_SelectStarBypassesFieldQuery(t *testing.T) {
	env := newFQEnv(t)

	// SELECT * should return all fields including those not in any selective list.
	oql := "SELECT * FROM people WHERE name = 'Alice'"
	stmt := parseFQOQL(t, oql)

	result, err := env.fqExec.ExecuteWithStore(env.ctx, stmt, env.store)
	if err != nil {
		t.Fatalf("SELECT *: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(result.Rows))
	}

	row := result.Rows[0]
	// Must have all fields from the original record.
	for _, field := range []string{"name", "email", "age", "active", "score", "region", "tags"} {
		if _, ok := row[field]; !ok {
			t.Errorf("SELECT * missing field %q", field)
		}
	}
}
