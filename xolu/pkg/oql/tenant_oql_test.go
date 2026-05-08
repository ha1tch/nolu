// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

// newScopedSQLiteStore creates a tenant-scoped SQLite store for testing.
func newScopedSQLiteStore(t *testing.T, dbPath string, tenantID uint16) storage.Store {
	t.Helper()
	store, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:            "sqlite",
		DBPath:          dbPath,
		FullTextEnabled: true,
		GraphEnabled:    false,
		TenantID:        tenantID,
	})
	if err != nil {
		t.Fatalf("NewStoreFromConfig(tenant=%d): %v", tenantID, err)
	}
	return store
}

// TestExecuteWithStore_TenantIsolation verifies that the full OQL pipeline
// (parse → validate → execute) returns correctly scoped results when given
// stores scoped to different tenants.
func TestExecuteWithStore_TenantIsolation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "oql_tenant.db")
	ctx := context.Background()

	storeA := newScopedSQLiteStore(t, dbPath, 0x0001)
	storeB := newScopedSQLiteStore(t, dbPath, 0x0002)
	defer storeA.Close()
	defer storeB.Close()

	// Populate tenant A: 5 orders, all "shipped"
	for i := 0; i < 5; i++ {
		storeA.Create(ctx, "orders", map[string]interface{}{
			"status": "shipped",
			"amount": float64(100 + i*10),
		})
	}

	// Populate tenant B: 3 orders, 2 "pending" + 1 "shipped"
	storeB.Create(ctx, "orders", map[string]interface{}{
		"status": "pending", "amount": float64(500),
	})
	storeB.Create(ctx, "orders", map[string]interface{}{
		"status": "pending", "amount": float64(600),
	})
	storeB.Create(ctx, "orders", map[string]interface{}{
		"status": "shipped", "amount": float64(700),
	})

	// Build an OQL engine from tenant A's store (the base store; any tenant works)
	engine := NewEngine(storeA, "")

	// --- SELECT * FROM orders ---

	resultA, err := engine.ExecuteWithStore(ctx, "SELECT * FROM orders", storeA)
	if err != nil {
		t.Fatalf("ExecuteWithStore(A, SELECT *): %v", err)
	}
	if len(resultA.Rows) != 5 {
		t.Errorf("tenant A SELECT * = %d rows, want 5", len(resultA.Rows))
	}

	resultB, err := engine.ExecuteWithStore(ctx, "SELECT * FROM orders", storeB)
	if err != nil {
		t.Fatalf("ExecuteWithStore(B, SELECT *): %v", err)
	}
	if len(resultB.Rows) != 3 {
		t.Errorf("tenant B SELECT * = %d rows, want 3", len(resultB.Rows))
	}

	// --- SELECT * FROM orders WHERE status = 'shipped' ---

	shippedA, err := engine.ExecuteWithStore(ctx, "SELECT * FROM orders WHERE status = 'shipped'", storeA)
	if err != nil {
		t.Fatalf("ExecuteWithStore(A, WHERE shipped): %v", err)
	}
	if len(shippedA.Rows) != 5 {
		t.Errorf("tenant A shipped = %d, want 5", len(shippedA.Rows))
	}

	shippedB, err := engine.ExecuteWithStore(ctx, "SELECT * FROM orders WHERE status = 'shipped'", storeB)
	if err != nil {
		t.Fatalf("ExecuteWithStore(B, WHERE shipped): %v", err)
	}
	if len(shippedB.Rows) != 1 {
		t.Errorf("tenant B shipped = %d, want 1", len(shippedB.Rows))
	}

	// --- SELECT COUNT(*) FROM orders ---

	countA, err := engine.ExecuteWithStore(ctx, "SELECT COUNT(*) FROM orders", storeA)
	if err != nil {
		t.Fatalf("ExecuteWithStore(A, COUNT): %v", err)
	}
	if len(countA.Rows) != 1 {
		t.Fatalf("COUNT should return 1 row, got %d", len(countA.Rows))
	}
	if cnt, ok := countA.Rows[0]["COUNT(*)"]; !ok {
		// Try alternate key name
		t.Logf("COUNT result keys: %v", countA.Rows[0])
	} else {
		if cntFloat, ok := cnt.(float64); ok && cntFloat != 5 {
			t.Errorf("tenant A COUNT = %v, want 5", cnt)
		} else if cntInt, ok := cnt.(int); ok && cntInt != 5 {
			t.Errorf("tenant A COUNT = %v, want 5", cnt)
		}
	}

	// --- Verify data content doesn't leak ---

	for _, row := range resultA.Rows {
		if amt, ok := row["amount"].(float64); ok {
			if amt >= 500 {
				t.Errorf("tenant A has amount %.0f — possible leak from B", amt)
			}
		}
	}

	for _, row := range resultB.Rows {
		if amt, ok := row["amount"].(float64); ok {
			if amt < 500 {
				t.Errorf("tenant B has amount %.0f — possible leak from A", amt)
			}
		}
	}
}

// TestExecuteWithStore_Insert verifies that INSERT through ExecuteWithStore
// writes to the correct tenant's store.
func TestExecuteWithStore_Insert(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "oql_tenant_insert.db")
	ctx := context.Background()

	storeA := newScopedSQLiteStore(t, dbPath, 0x0001)
	storeB := newScopedSQLiteStore(t, dbPath, 0x0002)
	defer storeA.Close()
	defer storeB.Close()

	// Seed both tenants with one record so the entity type exists
	// (OQL validates entity existence before INSERT)
	storeA.Create(ctx, "products", map[string]interface{}{"name": "seed-A", "price": float64(0)})
	storeB.Create(ctx, "products", map[string]interface{}{"name": "seed-B", "price": float64(0)})

	engine := NewEngine(storeA, "")

	// INSERT into tenant A via OQL
	_, err := engine.ExecuteWithStore(ctx,
		"INSERT INTO products (name, price) VALUES ('Widget', 9.99)", storeA)
	if err != nil {
		t.Fatalf("INSERT into A: %v", err)
	}

	// INSERT into tenant B via OQL
	_, err = engine.ExecuteWithStore(ctx,
		"INSERT INTO products (name, price) VALUES ('Gadget', 19.99)", storeB)
	if err != nil {
		t.Fatalf("INSERT into B: %v", err)
	}

	// Verify isolation via storage layer (seed + 1 OQL insert = 2 each)
	listA, _ := storeA.List(ctx, "products")
	listB, _ := storeB.List(ctx, "products")

	if len(listA) != 2 {
		t.Errorf("tenant A products = %d, want 2", len(listA))
	}
	if len(listB) != 2 {
		t.Errorf("tenant B products = %d, want 2", len(listB))
	}

	// Verify the OQL-inserted records exist in the right tenant
	foundWidget := false
	for _, rec := range listA {
		if name, _ := rec["name"].(string); name == "Widget" {
			foundWidget = true
		}
		if name, _ := rec["name"].(string); name == "Gadget" {
			t.Error("tenant A contains 'Gadget' — leak from B")
		}
	}
	if !foundWidget {
		t.Error("tenant A missing 'Widget' after OQL INSERT")
	}

	foundGadget := false
	for _, rec := range listB {
		if name, _ := rec["name"].(string); name == "Gadget" {
			foundGadget = true
		}
		if name, _ := rec["name"].(string); name == "Widget" {
			t.Error("tenant B contains 'Widget' — leak from A")
		}
	}
	if !foundGadget {
		t.Error("tenant B missing 'Gadget' after OQL INSERT")
	}

	// Cross-verify via OQL SELECT
	selA, _ := engine.ExecuteWithStore(ctx, "SELECT * FROM products", storeA)
	selB, _ := engine.ExecuteWithStore(ctx, "SELECT * FROM products", storeB)

	if len(selA.Rows) != 2 || len(selB.Rows) != 2 {
		t.Errorf("OQL SELECT: A=%d B=%d, want 2 each", len(selA.Rows), len(selB.Rows))
	}
}

// TestExecuteWithStore_UpdateDelete verifies that UPDATE and DELETE through
// ExecuteWithStore affect only the target tenant's data.
func TestExecuteWithStore_UpdateDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "oql_tenant_mutate.db")
	ctx := context.Background()

	storeA := newScopedSQLiteStore(t, dbPath, 0x0001)
	storeB := newScopedSQLiteStore(t, dbPath, 0x0002)
	defer storeA.Close()
	defer storeB.Close()

	// Seed both tenants with identical data
	for i := 0; i < 3; i++ {
		storeA.Create(ctx, "items", map[string]interface{}{
			"name": "item", "status": "active",
		})
		storeB.Create(ctx, "items", map[string]interface{}{
			"name": "item", "status": "active",
		})
	}

	engine := NewEngine(storeA, "")

	// UPDATE tenant A only
	updResult, err := engine.ExecuteWithStore(ctx,
		"UPDATE items SET status = 'archived' WHERE status = 'active'", storeA)
	if err != nil {
		t.Fatalf("UPDATE in A: %v", err)
	}
	if updResult.Stats.RowsAffected != 3 {
		t.Errorf("UPDATE affected %d rows, want 3", updResult.Stats.RowsAffected)
	}

	// Tenant B should still have all active
	activeB, _ := engine.ExecuteWithStore(ctx,
		"SELECT * FROM items WHERE status = 'active'", storeB)
	if len(activeB.Rows) != 3 {
		t.Errorf("tenant B active after A's update = %d, want 3", len(activeB.Rows))
	}

	// Tenant A should have 0 active
	activeA, _ := engine.ExecuteWithStore(ctx,
		"SELECT * FROM items WHERE status = 'active'", storeA)
	if len(activeA.Rows) != 0 {
		t.Errorf("tenant A active after update = %d, want 0", len(activeA.Rows))
	}

	// DELETE from tenant B
	delResult, err := engine.ExecuteWithStore(ctx,
		"DELETE FROM items WHERE status = 'active'", storeB)
	if err != nil {
		t.Fatalf("DELETE in B: %v", err)
	}
	if delResult.Stats.RowsAffected != 3 {
		t.Errorf("DELETE affected %d rows, want 3", delResult.Stats.RowsAffected)
	}

	// Tenant A should still have its 3 archived records
	allA, _ := engine.ExecuteWithStore(ctx, "SELECT * FROM items", storeA)
	if len(allA.Rows) != 3 {
		t.Errorf("tenant A after B's delete = %d, want 3", len(allA.Rows))
	}

	// Tenant B should be empty
	allB, _ := engine.ExecuteWithStore(ctx, "SELECT * FROM items", storeB)
	if len(allB.Rows) != 0 {
		t.Errorf("tenant B after delete = %d, want 0", len(allB.Rows))
	}
}
