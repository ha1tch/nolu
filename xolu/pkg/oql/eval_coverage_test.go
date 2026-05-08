// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// seedProducts populates a mock store with product records that have
// numeric, string, boolean-ish, null, and float fields for exercising
// the evaluator's type-switch branches.
func seedProducts(store *mockStore) {
	ctx := context.Background()
	store.Create(ctx, "products", map[string]interface{}{
		"name": "Widget", "price": 10.0, "quantity": 100,
		"category": "hardware", "discount": 0.15, "active": "TRUE",
	})
	store.Create(ctx, "products", map[string]interface{}{
		"name": "Gadget", "price": 25.5, "quantity": 50,
		"category": "electronics", "discount": 0.0, "active": "FALSE",
	})
	store.Create(ctx, "products", map[string]interface{}{
		"name": "Gizmo", "price": 7.25, "quantity": 200,
		"category": "hardware", "discount": 0.10, "active": "TRUE",
	})
	store.Create(ctx, "products", map[string]interface{}{
		"name": "Doohickey", "price": 100.0, "quantity": 5,
		"category": "electronics", "discount": nil, "active": "TRUE",
	})
	store.Create(ctx, "products", map[string]interface{}{
		"name": "Thingamajig", "price": 0.0, "quantity": 0,
		"category": "misc", "discount": 0.50, "active": "FALSE",
	})
}

// setupProductsEngine creates a mock store with product data and an engine
// with the necessary schema directory for the "products" entity.
func setupProductsEngine(t *testing.T) *Engine {
	t.Helper()
	store := newMockStore()
	seedProducts(store)
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "products"), 0755)
	return NewEngine(store, tmpDir)
}

// setupItemsEngine creates a mock store with custom data and an engine.
func setupItemsEngine(t *testing.T, store *mockStore) *Engine {
	t.Helper()
	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "items"), 0755)
	os.MkdirAll(filepath.Join(tmpDir, "products"), 0755)
	return NewEngine(store, tmpDir)
}

func TestEvalArithmetic_AllOperators(t *testing.T) {
	engine := setupProductsEngine(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		query string
		check func(*Result) error
	}{
		{
			name:  "addition",
			query: "SELECT name, price + 5 as adjusted FROM products WHERE name = 'Widget'",
			check: func(r *Result) error {
				val := r.Rows[0]["adjusted"]
				if toFloat(val) != 15.0 {
					t.Errorf("price + 5 = %v, want 15", val)
				}
				return nil
			},
		},
		{
			name:  "subtraction",
			query: "SELECT name, quantity - 10 as remaining FROM products WHERE name = 'Widget'",
			check: func(r *Result) error {
				val := r.Rows[0]["remaining"]
				if toFloat(val) != 90.0 {
					t.Errorf("quantity - 10 = %v, want 90", val)
				}
				return nil
			},
		},
		{
			name:  "multiplication",
			query: "SELECT name, price * quantity as revenue FROM products WHERE name = 'Gadget'",
			check: func(r *Result) error {
				val := r.Rows[0]["revenue"]
				if toFloat(val) != 1275.0 {
					t.Errorf("25.5 * 50 = %v, want 1275", val)
				}
				return nil
			},
		},
		{
			name:  "division",
			query: "SELECT name, quantity / 10 as packs FROM products WHERE name = 'Gizmo'",
			check: func(r *Result) error {
				val := r.Rows[0]["packs"]
				if toFloat(val) != 20.0 {
					t.Errorf("200 / 10 = %v, want 20", val)
				}
				return nil
			},
		},
		{
			name:  "modulo",
			query: "SELECT name, quantity % 30 as leftover FROM products WHERE name = 'Gadget'",
			check: func(r *Result) error {
				val := r.Rows[0]["leftover"]
				if toFloat(val) != 20.0 {
					t.Errorf("50 %% 30 = %v, want 20", val)
				}
				return nil
			},
		},
		{
			name:  "division by zero",
			query: "SELECT name, price / 0 as boom FROM products WHERE name = 'Widget'",
			check: func(r *Result) error {
				val := r.Rows[0]["boom"]
				if val != nil {
					t.Errorf("division by zero = %v, want nil", val)
				}
				return nil
			},
		},
		{
			name:  "modulo by zero",
			query: "SELECT name, quantity % 0 as boom FROM products WHERE name = 'Widget'",
			check: func(r *Result) error {
				val := r.Rows[0]["boom"]
				if val != nil {
					t.Errorf("modulo by zero = %v, want nil", val)
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Execute(ctx, tt.query)
			if err != nil {
				t.Fatalf("query %q: %v", tt.query, err)
			}
			if len(result.Rows) == 0 {
				t.Fatalf("query %q: no rows", tt.query)
			}
			tt.check(result)
		})
	}
}

// --- Boolean literals in WHERE (evalExpr TRUE/FALSE branches) ---

func TestEvalExpr_BooleanLiterals(t *testing.T) {
	store := newMockStore()
	ctx := context.Background()
	// Store actual boolean values (not strings) to test the TRUE/FALSE branches
	store.Create(ctx, "flags", map[string]interface{}{"name": "on", "enabled": true})
	store.Create(ctx, "flags", map[string]interface{}{"name": "off", "enabled": false})
	store.Create(ctx, "flags", map[string]interface{}{"name": "also_on", "enabled": true})

	tmpDir := t.TempDir()
	os.MkdirAll(filepath.Join(tmpDir, "flags"), 0755)
	engine := NewEngine(store, tmpDir)

	// WHERE enabled = TRUE should match 2 flags
	result, err := engine.Execute(ctx, "SELECT name FROM flags WHERE enabled = TRUE")
	if err != nil {
		t.Fatalf("WHERE TRUE: %v", err)
	}
	if len(result.Rows) != 2 {
		t.Errorf("WHERE enabled = TRUE: got %d rows, want 2", len(result.Rows))
	}

	// WHERE enabled = FALSE should match 1 flag
	result, err = engine.Execute(ctx, "SELECT name FROM flags WHERE enabled = FALSE")
	if err != nil {
		t.Fatalf("WHERE FALSE: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("WHERE enabled = FALSE: got %d rows, want 1", len(result.Rows))
	}
}

// --- Float comparisons (evalComparison with float values) ---

func TestEvalComparison_FloatOperators(t *testing.T) {
	engine := setupProductsEngine(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		query string
		want  int // expected row count
	}{
		{"greater than float", "SELECT name FROM products WHERE price > 10.0", 2},   // 25.5, 100
		{"less than float", "SELECT name FROM products WHERE price < 10.0", 2},      // 7.25, 0
		{"greater or equal", "SELECT name FROM products WHERE price >= 10.0", 3},    // 10, 25.5, 100
		{"less or equal", "SELECT name FROM products WHERE price <= 10.0", 3},       // 10, 7.25, 0
		{"not equal", "SELECT name FROM products WHERE category <> 'hardware'", 3},  // electronics x2, misc
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Execute(ctx, tt.query)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if len(result.Rows) != tt.want {
				names := make([]interface{}, len(result.Rows))
				for i, r := range result.Rows {
					names[i] = r["name"]
				}
				t.Errorf("got %d rows %v, want %d", len(result.Rows), names, tt.want)
			}
		})
	}
}

// --- HAVING with various operators (aggregator evalCondition branches) ---

func TestAggregator_HavingOperators(t *testing.T) {
	engine := setupProductsEngine(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		query string
		want  int
	}{
		{
			name:  "HAVING greater than",
			query: "SELECT category, COUNT(*) as cnt FROM products GROUP BY category HAVING COUNT(*) > 1",
			want:  2, // hardware(2), electronics(2)
		},
		{
			name:  "HAVING less than",
			query: "SELECT category, COUNT(*) as cnt FROM products GROUP BY category HAVING COUNT(*) < 2",
			want:  1, // misc(1)
		},
		{
			name:  "HAVING not equal",
			query: "SELECT category, COUNT(*) as cnt FROM products GROUP BY category HAVING COUNT(*) != 1",
			want:  2, // hardware(2), electronics(2)
		},
		{
			name:  "HAVING greater or equal",
			query: "SELECT category, COUNT(*) as cnt FROM products GROUP BY category HAVING COUNT(*) >= 2",
			want:  2,
		},
		{
			name:  "HAVING less or equal",
			query: "SELECT category, COUNT(*) as cnt FROM products GROUP BY category HAVING COUNT(*) <= 1",
			want:  1,
		},
		{
			name:  "HAVING with AND",
			query: "SELECT category, COUNT(*) as cnt, AVG(price) as avg_price FROM products GROUP BY category HAVING COUNT(*) >= 1 AND AVG(price) > 5",
			want:  2, // hardware(avg 8.625), electronics(avg 62.75); misc avg is 0
		},
		{
			name:  "HAVING with OR",
			query: "SELECT category, COUNT(*) as cnt FROM products GROUP BY category HAVING COUNT(*) > 2 OR category = 'misc'",
			want:  1, // misc (no category has >2)
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Execute(ctx, tt.query)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if len(result.Rows) != tt.want {
				t.Errorf("got %d rows, want %d; rows: %v", len(result.Rows), tt.want, result.Rows)
			}
		})
	}
}

// --- Aggregate functions with float data (aggSum/aggAvg/aggMin/aggMax untested type paths) ---

func TestAggregates_FloatFields(t *testing.T) {
	engine := setupProductsEngine(t)
	ctx := context.Background()

	tests := []struct {
		name  string
		query string
		field string
		check func(interface{}) bool
	}{
		{
			name:  "SUM of floats",
			query: "SELECT SUM(price) as total FROM products",
			field: "total",
			check: func(v interface{}) bool { return toFloat(v) > 142.0 && toFloat(v) < 143.0 }, // 10+25.5+7.25+100+0 = 142.75
		},
		{
			name:  "AVG of floats",
			query: "SELECT AVG(price) as avg_price FROM products",
			field: "avg_price",
			check: func(v interface{}) bool { return toFloat(v) > 28.0 && toFloat(v) < 29.0 }, // 142.75/5 = 28.55
		},
		{
			name:  "MIN of floats",
			query: "SELECT MIN(price) as cheapest FROM products",
			field: "cheapest",
			check: func(v interface{}) bool { return toFloat(v) == 0.0 },
		},
		{
			name:  "MAX of floats",
			query: "SELECT MAX(price) as priciest FROM products",
			field: "priciest",
			check: func(v interface{}) bool { return toFloat(v) == 100.0 },
		},
		{
			name:  "SUM grouped",
			query: "SELECT category, SUM(quantity) as total_qty FROM products GROUP BY category HAVING category = 'hardware'",
			field: "total_qty",
			check: func(v interface{}) bool { return toFloat(v) == 300.0 }, // 100+200
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := engine.Execute(ctx, tt.query)
			if err != nil {
				t.Fatalf("%v", err)
			}
			if len(result.Rows) == 0 {
				t.Fatal("no rows returned")
			}
			val := result.Rows[0][tt.field]
			if !tt.check(val) {
				t.Errorf("%s = %v (type %T)", tt.field, val, val)
			}
		})
	}
}

// --- NULL literal in WHERE (evalLiteral NullLiteral branch) ---

func TestEvalLiteral_NullComparison(t *testing.T) {
	engine := setupProductsEngine(t)
	ctx := context.Background()

	// Products with discount = nil: Doohickey
	result, err := engine.Execute(ctx, "SELECT name FROM products WHERE discount = NULL")
	if err != nil {
		t.Fatalf("WHERE NULL: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("WHERE discount = NULL: got %d rows, want 1", len(result.Rows))
	} else if result.Rows[0]["name"] != "Doohickey" {
		t.Errorf("got %v, want Doohickey", result.Rows[0]["name"])
	}
}

// --- UPDATE with SET using field references (evalLiteralWithRecord) ---

func TestExecuteUpdate_SetFromField(t *testing.T) {
	store := newMockStore()
	ctx := context.Background()
	store.Create(ctx, "items", map[string]interface{}{"name": "alpha", "status": "draft", "backup": "old"})
	store.Create(ctx, "items", map[string]interface{}{"name": "beta", "status": "active", "backup": "old"})

	engine := setupItemsEngine(t, store)

	// Update backup to match the current status value
	result, err := engine.Execute(ctx, "UPDATE items SET backup = status WHERE name = 'alpha'")
	if err != nil {
		t.Fatalf("UPDATE: %v", err)
	}
	if result.Stats.RowsAffected != 1 {
		t.Errorf("affected = %d, want 1", result.Stats.RowsAffected)
	}
}

// --- DELETE with various WHERE operators (executeDelete untested branches) ---

func TestExecuteDelete_ComparisonOperators(t *testing.T) {
	store := newMockStore()
	seedProducts(store)
	engine := setupItemsEngine(t, store)
	ctx := context.Background()

	// Delete products with price > 50 (should delete Doohickey at 100)
	result, err := engine.Execute(ctx, "DELETE FROM products WHERE price > 50")
	if err != nil {
		t.Fatalf("DELETE: %v", err)
	}
	if result.Stats.RowsAffected != 1 {
		t.Errorf("affected = %d, want 1", result.Stats.RowsAffected)
	}

	// Verify remaining
	remaining, _ := store.List(ctx, "products")
	if len(remaining) != 4 {
		t.Errorf("remaining = %d, want 4", len(remaining))
	}
}

// --- SELECT with ORDER BY on float field (OrderBy with numeric comparison) ---

func TestOrderBy_FloatField(t *testing.T) {
	engine := setupProductsEngine(t)
	ctx := context.Background()

	result, err := engine.Execute(ctx, "SELECT name, price FROM products ORDER BY price DESC")
	if err != nil {
		t.Fatalf("%v", err)
	}
	if len(result.Rows) < 2 {
		t.Fatal("too few rows")
	}
	// First should be most expensive (100.0)
	if result.Rows[0]["name"] != "Doohickey" {
		t.Errorf("first by price DESC = %v, want Doohickey", result.Rows[0]["name"])
	}
	// Last should be cheapest (0.0)
	last := result.Rows[len(result.Rows)-1]
	if last["name"] != "Thingamajig" {
		t.Errorf("last by price DESC = %v, want Thingamajig", last["name"])
	}
}
