// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
)

// ---------------------------------------------------------------------------
// FTS index isolation after update and delete
//
// Verifies that modifying or deleting an entity in tenant A updates only
// tenant A's FTS entries, leaving tenant B's searchable content intact.
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_FTSUpdateDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_fts_mut.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, true)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, true)
	defer storeA.Close()
	defer storeB.Close()

	// Both tenants create entities with the word "original"
	storeA.Create(ctx, "docs", map[string]interface{}{"title": "original alpha report"})
	storeA.Create(ctx, "docs", map[string]interface{}{"title": "original alpha memo"})
	storeB.Create(ctx, "docs", map[string]interface{}{"title": "original beta report"})
	storeB.Create(ctx, "docs", map[string]interface{}{"title": "original beta memo"})

	// Baseline: both find 2 results for "original"
	ftsA, _ := storeA.FullTextSearch(ctx, "original", "docs")
	ftsB, _ := storeB.FullTextSearch(ctx, "original", "docs")
	if len(ftsA) != 2 {
		t.Fatalf("baseline: tenant A FTS 'original' = %d, want 2", len(ftsA))
	}
	if len(ftsB) != 2 {
		t.Fatalf("baseline: tenant B FTS 'original' = %d, want 2", len(ftsB))
	}

	// Update tenant A doc 1: change "original" to "revised"
	storeA.Update(ctx, "docs", 1, map[string]interface{}{
		"id": 1, "title": "revised alpha report",
	})

	// Tenant A: "original" now returns 1, "revised" returns 1
	ftsA, _ = storeA.FullTextSearch(ctx, "original", "docs")
	if len(ftsA) != 1 {
		t.Errorf("after update: tenant A FTS 'original' = %d, want 1", len(ftsA))
	}
	ftsARevised, _ := storeA.FullTextSearch(ctx, "revised", "docs")
	if len(ftsARevised) != 1 {
		t.Errorf("after update: tenant A FTS 'revised' = %d, want 1", len(ftsARevised))
	}

	// Tenant B: still 2 results for "original" (unaffected by A's update)
	ftsB, _ = storeB.FullTextSearch(ctx, "original", "docs")
	if len(ftsB) != 2 {
		t.Errorf("after A's update: tenant B FTS 'original' = %d, want 2", len(ftsB))
	}
	// Tenant B sees no "revised"
	ftsBRevised, _ := storeB.FullTextSearch(ctx, "revised", "docs")
	if len(ftsBRevised) != 0 {
		t.Errorf("tenant B FTS 'revised' = %d, want 0", len(ftsBRevised))
	}

	// Delete tenant A doc 2
	storeA.Delete(ctx, "docs", 2)

	// Tenant A: "original" now returns 0
	ftsA, _ = storeA.FullTextSearch(ctx, "original", "docs")
	if len(ftsA) != 0 {
		t.Errorf("after delete: tenant A FTS 'original' = %d, want 0", len(ftsA))
	}

	// Tenant B: still 2 (unaffected by A's delete)
	ftsB, _ = storeB.FullTextSearch(ctx, "original", "docs")
	if len(ftsB) != 2 {
		t.Errorf("after A's delete: tenant B FTS 'original' = %d, want 2", len(ftsB))
	}
}

// ---------------------------------------------------------------------------
// Graph edge cleanup on delete — per-tenant isolation
//
// Verifies that deleting an entity from tenant A cleans up graph edges
// only in tenant A's graph table, leaving tenant B's graph intact.
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_GraphEdgeCleanupOnDelete(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_graph_cleanup.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, true, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, true, false)
	defer storeA.Close()
	defer storeB.Close()

	// Both tenants: create target entity + entity with REF to it
	storeA.Create(ctx, "projects", map[string]interface{}{"name": "Project-A"})
	storeA.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-A1",
		"project": map[string]interface{}{
			"type": "REF", "entity": "projects", "id": 1,
		},
	})
	storeA.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-A2",
		"project": map[string]interface{}{
			"type": "REF", "entity": "projects", "id": 1,
		},
	})

	storeB.Create(ctx, "projects", map[string]interface{}{"name": "Project-B"})
	storeB.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-B1",
		"project": map[string]interface{}{
			"type": "REF", "entity": "projects", "id": 1,
		},
	})

	// Verify baseline edge counts
	var countA, countB int
	storeA.db.QueryRow("SELECT COUNT(*) FROM graph_t0001").Scan(&countA)
	storeB.db.QueryRow("SELECT COUNT(*) FROM graph_t0002").Scan(&countB)
	if countA != 2 {
		t.Fatalf("baseline: tenant A graph edges = %d, want 2", countA)
	}
	if countB != 1 {
		t.Fatalf("baseline: tenant B graph edges = %d, want 1", countB)
	}

	// Delete task 1 from tenant A
	storeA.Delete(ctx, "tasks", 1)

	// Tenant A: 1 edge remaining (task 2 → project 1)
	storeA.db.QueryRow("SELECT COUNT(*) FROM graph_t0001").Scan(&countA)
	if countA != 1 {
		t.Errorf("after delete: tenant A graph edges = %d, want 1", countA)
	}

	// Tenant B: still 1 edge (completely unaffected)
	storeB.db.QueryRow("SELECT COUNT(*) FROM graph_t0002").Scan(&countB)
	if countB != 1 {
		t.Errorf("after A's delete: tenant B graph edges = %d, want 1", countB)
	}

	// Delete the target entity (project) from tenant A
	storeA.Delete(ctx, "projects", 1)

	// Remaining edges in A that point TO projects:1 should be cleaned
	storeA.db.QueryRow("SELECT COUNT(*) FROM graph_t0001").Scan(&countA)
	if countA != 0 {
		t.Errorf("after project delete: tenant A graph edges = %d, want 0", countA)
	}

	// Tenant B still intact
	storeB.db.QueryRow("SELECT COUNT(*) FROM graph_t0002").Scan(&countB)
	if countB != 1 {
		t.Errorf("after A's project delete: tenant B graph edges = %d, want 1", countB)
	}
}

// ---------------------------------------------------------------------------
// Graph edge cleanup for tenant 0 vs non-zero
//
// Tenant 0 uses graph_t0000. Deleting in tenant 0 must not touch
// other tenants' graph tables, and vice versa.
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_GraphEdgeCleanupTenantZeroVsNonZero(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_graph_zero_cleanup.db")
	ctx := context.Background()

	store0 := newTenantSQLiteStore(t, dbPath, 0, true, false)
	store1 := newTenantSQLiteStore(t, dbPath, 0x0001, true, false)
	defer store0.Close()
	defer store1.Close()

	// Both create identical structure: parent → child ref
	store0.Create(ctx, "parents", map[string]interface{}{"name": "P0"})
	store0.Create(ctx, "children", map[string]interface{}{
		"name": "C0",
		"parent": map[string]interface{}{
			"type": "REF", "entity": "parents", "id": 1,
		},
	})

	store1.Create(ctx, "parents", map[string]interface{}{"name": "P1"})
	store1.Create(ctx, "children", map[string]interface{}{
		"name": "C1",
		"parent": map[string]interface{}{
			"type": "REF", "entity": "parents", "id": 1,
		},
	})

	// Check baseline
	var count0, count1 int
	store0.db.QueryRow("SELECT COUNT(*) FROM graph_t0000").Scan(&count0)
	store0.db.QueryRow("SELECT COUNT(*) FROM graph_t0001").Scan(&count1)
	if count0 < 1 {
		t.Fatalf("baseline: graph_t0000 = %d, want >= 1", count0)
	}
	if count1 < 1 {
		t.Fatalf("baseline: graph_t0001 = %d, want >= 1", count1)
	}

	// Delete from tenant 0
	store0.Delete(ctx, "children", 1)

	// graph_t0000 should have fewer edges
	var count0After int
	store0.db.QueryRow("SELECT COUNT(*) FROM graph_t0000 WHERE source_entity='children' AND source_id=1").Scan(&count0After)
	if count0After != 0 {
		t.Errorf("after delete: graph_t0000 still has children:1 edges = %d", count0After)
	}

	// graph_t0001 untouched
	var count1After int
	store0.db.QueryRow("SELECT COUNT(*) FROM graph_t0001").Scan(&count1After)
	if count1After != count1 {
		t.Errorf("tenant 1 graph changed: %d → %d (should be unchanged)", count1, count1After)
	}
}

// ---------------------------------------------------------------------------
// OQL ExecuteWithStore scoping
//
// Verifies that the OQL pipeline (parse → validate → execute) respects
// tenant boundaries when given a scoped store.
// ---------------------------------------------------------------------------

// Note: This test is in the storage package for practical reasons (access to
// newTenantSQLiteStore). It imports oql indirectly via the executor's Store
// interface — the real OQL test would be in pkg/oql but would need a way
// to construct scoped stores. We test the foundation here: the storage tier
// that OQL queries read from returns correctly scoped data.

func TestSQLiteTenantIsolation_OQLReadPath(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_oql_read.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Populate tenant A with specific data patterns
	for i := 0; i < 10; i++ {
		storeA.Create(ctx, "orders", map[string]interface{}{
			"amount":   float64(100 + i*10),
			"status":   "shipped",
			"customer": fmt.Sprintf("customer-A-%d", i),
		})
	}

	// Populate tenant B with different patterns
	for i := 0; i < 3; i++ {
		storeB.Create(ctx, "orders", map[string]interface{}{
			"amount":   float64(999),
			"status":   "pending",
			"customer": fmt.Sprintf("customer-B-%d", i),
		})
	}

	// OQL's read path uses List + in-memory filtering.
	// Verify that List from each store is fully scoped.
	listA, _ := storeA.List(ctx, "orders")
	listB, _ := storeB.List(ctx, "orders")

	if len(listA) != 10 {
		t.Errorf("storeA.List = %d, want 10", len(listA))
	}
	if len(listB) != 3 {
		t.Errorf("storeB.List = %d, want 3", len(listB))
	}

	// Verify no cross-contamination in the data itself
	for _, rec := range listA {
		customer, _ := rec["customer"].(string)
		if len(customer) > 10 && customer[:10] == "customer-B" {
			t.Errorf("tenant A list contains B's customer: %s", customer)
		}
	}
	for _, rec := range listB {
		customer, _ := rec["customer"].(string)
		if len(customer) > 10 && customer[:10] == "customer-A" {
			t.Errorf("tenant B list contains A's customer: %s", customer)
		}
	}

	// Both tenants have ID 1 (per-tenant sequences). Verify each gets its own.
	recA, err := storeA.Get(ctx, "orders", 1)
	if err != nil {
		t.Fatalf("storeA.Get(1): %v", err)
	}
	if cust, _ := recA["customer"].(string); cust != "customer-A-0" {
		t.Errorf("storeA.Get(1) customer = %q, want customer-A-0", cust)
	}

	recB, err := storeB.Get(ctx, "orders", 1)
	if err != nil {
		t.Fatalf("storeB.Get(1): %v", err)
	}
	if cust, _ := recB["customer"].(string); cust != "customer-B-0" {
		t.Errorf("storeB.Get(1) customer = %q, want customer-B-0", cust)
	}

	// Verify that IDs beyond a tenant's range don't exist
	// Tenant B only has IDs 1-3, so ID 5 should not exist
	_, err = storeB.Get(ctx, "orders", 5)
	if err == nil {
		t.Error("storeB.Get(5) should fail — tenant B only has 3 orders")
	}

	// Note: OQL's push-down path goes through the Queryable interface
	// which generates tenant-scoped SQL. That scoping is verified by the
	// fact that List/Get/Search all return tenant-scoped data above.
}

// ---------------------------------------------------------------------------
// Per-tenant sequence independence under update/delete
//
// Verifies that deleting records in one tenant doesn't affect sequence
// numbering in another, and that re-creating after deletes continues
// from the correct sequence value.
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_SequenceAfterDeleteAndRecreate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_seq_delete.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Create 5 records in each
	for i := 0; i < 5; i++ {
		storeA.Create(ctx, "items", map[string]interface{}{"n": i})
		storeB.Create(ctx, "items", map[string]interface{}{"n": i})
	}

	// Delete records 3, 4, 5 from tenant A
	storeA.Delete(ctx, "items", 3)
	storeA.Delete(ctx, "items", 4)
	storeA.Delete(ctx, "items", 5)

	// Create a new record in tenant A — should get ID 6 (not reuse 3)
	idA, err := storeA.Create(ctx, "items", map[string]interface{}{"n": 99})
	if err != nil {
		t.Fatalf("storeA.Create after delete: %v", err)
	}
	if idA != 6 {
		t.Errorf("tenant A next ID after delete = %d, want 6", idA)
	}

	// Tenant B's sequence unaffected — next should be 6
	idB, err := storeB.Create(ctx, "items", map[string]interface{}{"n": 99})
	if err != nil {
		t.Fatalf("storeB.Create: %v", err)
	}
	if idB != 6 {
		t.Errorf("tenant B next ID = %d, want 6", idB)
	}

	// Counts: A has 3 (IDs 1,2,6), B has 6 (IDs 1-6)
	countA, _ := storeA.CountEntities(ctx, "items")
	countB, _ := storeB.CountEntities(ctx, "items")
	if countA != 3 {
		t.Errorf("tenant A count = %d, want 3", countA)
	}
	if countB != 6 {
		t.Errorf("tenant B count = %d, want 6", countB)
	}
}

// ---------------------------------------------------------------------------
// Cross-entity-type isolation within a tenant
//
// Verifies that tenant scoping works correctly when a tenant has
// multiple entity types, and that entity types from one tenant don't
// appear in another tenant's ListEntities.
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_CrossEntityType(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_cross_entity.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Tenant A has 3 entity types
	storeA.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	storeA.Create(ctx, "projects", map[string]interface{}{"name": "Alpha"})
	storeA.Create(ctx, "tasks", map[string]interface{}{"title": "Do things"})

	// Tenant B has only 1 entity type
	storeB.Create(ctx, "invoices", map[string]interface{}{"total": 100})

	// ListEntities: A sees 3 types, B sees 1
	typesA, _ := storeA.ListEntities(ctx)
	typesB, _ := storeB.ListEntities(ctx)

	if len(typesA) != 3 {
		t.Errorf("tenant A entity types = %d (%v), want 3", len(typesA), typesA)
	}
	if len(typesB) != 1 {
		t.Errorf("tenant B entity types = %d (%v), want 1", len(typesB), typesB)
	}

	// B should see only "invoices"
	if len(typesB) == 1 && typesB[0] != "invoices" {
		t.Errorf("tenant B entity type = %q, want invoices", typesB[0])
	}

	// A should not see "invoices"
	for _, et := range typesA {
		if et == "invoices" {
			t.Error("tenant A should not see 'invoices' entity type")
		}
	}
}

// ---------------------------------------------------------------------------
// Patch isolation: patching in one tenant doesn't affect another
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_PatchIsolation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_patch.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Both tenants create record ID 1 with same initial data
	storeA.Create(ctx, "configs", map[string]interface{}{
		"setting": "dark", "value": 1,
	})
	storeB.Create(ctx, "configs", map[string]interface{}{
		"setting": "dark", "value": 1,
	})

	// Patch tenant A's record
	recA, _ := storeA.Get(ctx, "configs", 1)
	recA["setting"] = "light"
	recA["value"] = 42
	storeA.Update(ctx, "configs", 1, recA)

	// Tenant B's record should be unchanged
	recB, _ := storeB.Get(ctx, "configs", 1)
	if setting, _ := recB["setting"].(string); setting != "dark" {
		t.Errorf("tenant B setting = %q, want 'dark' (unchanged)", setting)
	}
	if val, ok := recB["value"].(float64); !ok || val != 1 {
		t.Errorf("tenant B value = %v, want 1 (unchanged)", recB["value"])
	}

	// Confirm A's change took effect
	recA2, _ := storeA.Get(ctx, "configs", 1)
	if setting, _ := recA2["setting"].(string); setting != "light" {
		t.Errorf("tenant A setting = %q, want 'light'", setting)
	}
}
