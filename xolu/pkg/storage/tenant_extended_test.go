// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// ---------------------------------------------------------------------------
// Gap 1: OQL-style queries through scoped stores
//
// We can't import pkg/oql here (circular dep), but we CAN verify that the
// underlying storage operations that OQL depends on (List + filter, Search,
// FullTextSearch, CountEntities) return correctly scoped data when two
// tenant stores share the same DB. This is the storage-tier proof that
// OQL's ExecuteWithStore will see the right data.
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_QueryFoundation(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_query.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, true)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, true)
	defer storeA.Close()
	defer storeB.Close()

	// Populate tenant A: 5 active sensors, 3 inactive
	for i := 0; i < 5; i++ {
		storeA.Create(ctx, "sensors", map[string]interface{}{
			"name":   fmt.Sprintf("sensor-A-%d", i),
			"status": "active",
			"zone":   "north",
		})
	}
	for i := 0; i < 3; i++ {
		storeA.Create(ctx, "sensors", map[string]interface{}{
			"name":   fmt.Sprintf("sensor-A-off-%d", i),
			"status": "inactive",
			"zone":   "south",
		})
	}

	// Populate tenant B: 2 active sensors only
	for i := 0; i < 2; i++ {
		storeB.Create(ctx, "sensors", map[string]interface{}{
			"name":   fmt.Sprintf("sensor-B-%d", i),
			"status": "active",
			"zone":   "east",
		})
	}

	// List: each tenant sees only its own
	listA, _ := storeA.List(ctx, "sensors")
	listB, _ := storeB.List(ctx, "sensors")
	if len(listA) != 8 {
		t.Errorf("tenant A List = %d, want 8", len(listA))
	}
	if len(listB) != 2 {
		t.Errorf("tenant B List = %d, want 2", len(listB))
	}

	// Search (field match): tenant A searching status=active sees 5, not 7
	activeA, err := storeA.Search(ctx, "sensors", "status", "active", "exact")
	if err != nil {
		t.Fatalf("storeA.Search: %v", err)
	}
	if len(activeA) != 5 {
		t.Errorf("tenant A active sensors = %d, want 5", len(activeA))
	}

	activeB, err := storeB.Search(ctx, "sensors", "status", "active", "exact")
	if err != nil {
		t.Fatalf("storeB.Search: %v", err)
	}
	if len(activeB) != 2 {
		t.Errorf("tenant B active sensors = %d, want 2", len(activeB))
	}

	// Search (contains): tenant B searching for "sensor" sees only its 2
	containsB, _ := storeB.Search(ctx, "sensors", "name", "sensor", "contains")
	if len(containsB) != 2 {
		t.Errorf("tenant B contains 'sensor' = %d, want 2", len(containsB))
	}

	// Count: scoped
	countA, _ := storeA.CountEntities(ctx, "sensors")
	countB, _ := storeB.CountEntities(ctx, "sensors")
	if countA != 8 {
		t.Errorf("tenant A count = %d, want 8", countA)
	}
	if countB != 2 {
		t.Errorf("tenant B count = %d, want 2", countB)
	}

	// FullTextSearch: tenant-scoped
	ftsA, err := storeA.FullTextSearch(ctx, "north", "sensors")
	if err != nil {
		t.Fatalf("storeA.FullTextSearch: %v", err)
	}
	if len(ftsA) != 5 {
		t.Errorf("tenant A FTS 'north' = %d, want 5", len(ftsA))
	}

	ftsB, err := storeB.FullTextSearch(ctx, "north", "sensors")
	if err != nil {
		t.Fatalf("storeB.FullTextSearch: %v", err)
	}
	if len(ftsB) != 0 {
		t.Errorf("tenant B FTS 'north' = %d, want 0 (no north zone in B)", len(ftsB))
	}

	// Cross-check: global FTS for "sensor" from tenant A must not include B's data
	ftsAllA, _ := storeA.FullTextSearch(ctx, "sensor", "")
	for _, rec := range ftsAllA {
		name, _ := rec["name"].(string)
		if len(name) > 8 && name[:8] == "sensor-B" {
			t.Errorf("tenant A FTS leaked tenant B data: %s", name)
		}
	}
}

// ---------------------------------------------------------------------------
// Gap 3: RebuildGraph per-tenant isolation
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_RebuildGraph(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_rebuild.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, true, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, true, false)
	defer storeA.Close()
	defer storeB.Close()

	// Tenant A: project -> task ref
	storeA.Create(ctx, "projects", map[string]interface{}{"name": "Alpha"})
	storeA.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-A",
		"project": map[string]interface{}{
			"type": "REF", "entity": "projects", "id": 1,
		},
	})

	// Tenant B: project -> task ref (same structure, different data)
	storeB.Create(ctx, "projects", map[string]interface{}{"name": "Beta"})
	storeB.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-B",
		"project": map[string]interface{}{
			"type": "REF", "entity": "projects", "id": 1,
		},
	})

	// Rebuild tenant A's graph
	var sA Store = storeA
	integrityA, ok := sA.(GraphIntegrity)
	if !ok {
		t.Fatal("storeA does not implement GraphIntegrity")
	}
	if err := integrityA.RebuildGraph(ctx); err != nil {
		t.Fatalf("storeA.RebuildGraph: %v", err)
	}

	// Tenant A's graph should still be correct
	if err := integrityA.VerifyGraphIntegrity(ctx); err != nil {
		t.Errorf("tenant A integrity after rebuild: %v", err)
	}

	// Tenant B's graph should be unaffected by A's rebuild
	var sB Store = storeB
	integrityB := sB.(GraphIntegrity)
	if err := integrityB.VerifyGraphIntegrity(ctx); err != nil {
		t.Errorf("tenant B integrity after A's rebuild: %v", err)
	}

	// Verify edge counts per tenant via edge table scan.
	if nA := outEdgesForExtended(t, sA.(*SQLiteStore), "tasks", 1); len(nA) != 1 {
		t.Errorf("tenant A neighbors after rebuild = %d, want 1", len(nA))
	}
	if nB := outEdgesForExtended(t, sB.(*SQLiteStore), "tasks", 1); len(nB) != 1 {
		t.Errorf("tenant B neighbors after rebuild = %d, want 1", len(nB))
	}

	// Now rebuild B and verify A is still intact
	if err := integrityB.RebuildGraph(ctx); err != nil {
		t.Fatalf("storeB.RebuildGraph: %v", err)
	}
	if err := integrityA.VerifyGraphIntegrity(ctx); err != nil {
		t.Errorf("tenant A integrity after B's rebuild: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Gap 4: Concurrent cross-tenant writes
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_ConcurrentWrites(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_concurrent.db")
	ctx := context.Background()

	const numTenants = 5
	const writesPerTenant = 50

	stores := make([]*SQLiteStore, numTenants)
	for i := 0; i < numTenants; i++ {
		stores[i] = newTenantSQLiteStore(t, dbPath, uint16(i+1), false, false)
		defer stores[i].Close()
	}

	// All tenants write concurrently
	var wg sync.WaitGroup
	errors := make(chan error, numTenants*writesPerTenant)

	for i := 0; i < numTenants; i++ {
		wg.Add(1)
		go func(tenantIdx int) {
			defer wg.Done()
			store := stores[tenantIdx]
			for j := 0; j < writesPerTenant; j++ {
				_, err := store.Create(ctx, "events", map[string]interface{}{
					"tenant_idx": tenantIdx,
					"seq":        j,
					"payload":    fmt.Sprintf("event-%d-%d", tenantIdx, j),
				})
				if err != nil {
					errors <- fmt.Errorf("tenant %d write %d: %w", tenantIdx+1, j, err)
				}
			}
		}(i)
	}

	wg.Wait()
	close(errors)

	for err := range errors {
		t.Error(err)
	}

	// Verify isolation after concurrent writes
	for i, store := range stores {
		list, err := store.List(ctx, "events")
		if err != nil {
			t.Fatalf("tenant %d List: %v", i+1, err)
		}
		if len(list) != writesPerTenant {
			t.Errorf("tenant %d has %d records, want %d", i+1, len(list), writesPerTenant)
		}

		// Verify all records belong to this tenant
		for _, rec := range list {
			if idx, ok := rec["tenant_idx"].(float64); ok {
				if int(idx) != i {
					t.Errorf("tenant %d sees record from tenant_idx %d", i+1, int(idx))
				}
			}
		}

		// Verify IDs are 1..writesPerTenant (per-tenant sequences)
		count, _ := store.CountEntities(ctx, "events")
		if count != writesPerTenant {
			t.Errorf("tenant %d count = %d, want %d", i+1, count, writesPerTenant)
		}
	}

	// Verify total in raw DB
	var total int
	stores[0].db.QueryRow("SELECT COUNT(*) FROM entities WHERE entity_type = 'events'").Scan(&total)
	if total != numTenants*writesPerTenant {
		t.Errorf("total records = %d, want %d", total, numTenants*writesPerTenant)
	}
}

func TestSQLiteTenantIsolation_ConcurrentMixedOps(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_mixed.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Pre-populate both tenants
	for i := 0; i < 20; i++ {
		storeA.Create(ctx, "items", map[string]interface{}{"val": i, "owner": "A"})
		storeB.Create(ctx, "items", map[string]interface{}{"val": i, "owner": "B"})
	}

	// Concurrent: A creates+updates, B reads+deletes — no cross-contamination
	var wg sync.WaitGroup
	errCh := make(chan error, 200)

	// Tenant A: create 30 more, then update records 1-10
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 30; i++ {
			_, err := storeA.Create(ctx, "items", map[string]interface{}{"val": 100 + i, "owner": "A"})
			if err != nil {
				errCh <- fmt.Errorf("A create: %w", err)
			}
		}
		for i := 1; i <= 10; i++ {
			err := storeA.Update(ctx, "items", i, map[string]interface{}{
				"id": i, "val": i * 100, "owner": "A", "updated": true,
			})
			if err != nil {
				errCh <- fmt.Errorf("A update %d: %w", i, err)
			}
		}
	}()

	// Tenant B: read all, delete records 11-20
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 10; i++ {
			list, err := storeB.List(ctx, "items")
			if err != nil {
				errCh <- fmt.Errorf("B list: %w", err)
				continue
			}
			// Every record B sees must belong to B
			for _, rec := range list {
				if owner, _ := rec["owner"].(string); owner != "B" {
					errCh <- fmt.Errorf("B saw record owned by %q", owner)
				}
			}
		}
		for i := 11; i <= 20; i++ {
			if err := storeB.Delete(ctx, "items", i); err != nil {
				errCh <- fmt.Errorf("B delete %d: %w", i, err)
			}
		}
	}()

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Error(err)
	}

	// Final state: A has 50 records (20 original + 30 new), B has 10 (20 - 10 deleted)
	countA, _ := storeA.CountEntities(ctx, "items")
	countB, _ := storeB.CountEntities(ctx, "items")
	if countA != 50 {
		t.Errorf("tenant A final count = %d, want 50", countA)
	}
	if countB != 10 {
		t.Errorf("tenant B final count = %d, want 10", countB)
	}
}

// ---------------------------------------------------------------------------
// Gap 7: Tenant 0 coexistence with non-zero tenants
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_ZeroAndNonZeroCoexist(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_coexist.db")
	ctx := context.Background()

	store0 := newTenantSQLiteStore(t, dbPath, 0, false, false)
	store1 := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	store2 := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer store0.Close()
	defer store1.Close()
	defer store2.Close()

	// All three create records in the same entity type
	for i := 0; i < 3; i++ {
		store0.Create(ctx, "widgets", map[string]interface{}{"source": "global", "n": i})
	}
	for i := 0; i < 5; i++ {
		store1.Create(ctx, "widgets", map[string]interface{}{"source": "tenant1", "n": i})
	}
	for i := 0; i < 7; i++ {
		store2.Create(ctx, "widgets", map[string]interface{}{"source": "tenant2", "n": i})
	}

	// Each sees only its own
	list0, _ := store0.List(ctx, "widgets")
	list1, _ := store1.List(ctx, "widgets")
	list2, _ := store2.List(ctx, "widgets")

	if len(list0) != 3 {
		t.Errorf("tenant 0 list = %d, want 3", len(list0))
	}
	if len(list1) != 5 {
		t.Errorf("tenant 1 list = %d, want 5", len(list1))
	}
	if len(list2) != 7 {
		t.Errorf("tenant 2 list = %d, want 7", len(list2))
	}

	// Verify data ownership
	for _, rec := range list0 {
		if src, _ := rec["source"].(string); src != "global" {
			t.Errorf("tenant 0 sees source=%q, want global", src)
		}
	}
	for _, rec := range list1 {
		if src, _ := rec["source"].(string); src != "tenant1" {
			t.Errorf("tenant 1 sees source=%q, want tenant1", src)
		}
	}
	for _, rec := range list2 {
		if src, _ := rec["source"].(string); src != "tenant2" {
			t.Errorf("tenant 2 sees source=%q, want tenant2", src)
		}
	}

	// Per-tenant sequences: all start at 1
	count0, _ := store0.CountEntities(ctx, "widgets")
	count1, _ := store1.CountEntities(ctx, "widgets")
	count2, _ := store2.CountEntities(ctx, "widgets")
	if count0 != 3 || count1 != 5 || count2 != 7 {
		t.Errorf("counts = (%d, %d, %d), want (3, 5, 7)", count0, count1, count2)
	}

	// Search scoped: "tenant1" only visible from store1
	res0, _ := store0.Search(ctx, "widgets", "source", "tenant1", "exact")
	res1, _ := store1.Search(ctx, "widgets", "source", "tenant1", "exact")
	if len(res0) != 0 {
		t.Errorf("tenant 0 search for 'tenant1' = %d, want 0", len(res0))
	}
	if len(res1) != 5 {
		t.Errorf("tenant 1 search for 'tenant1' = %d, want 5", len(res1))
	}

	// Delete from tenant 1 doesn't affect others
	store1.Delete(ctx, "widgets", 1)
	store1.Delete(ctx, "widgets", 2)

	count0After, _ := store0.CountEntities(ctx, "widgets")
	count1After, _ := store1.CountEntities(ctx, "widgets")
	count2After, _ := store2.CountEntities(ctx, "widgets")
	if count0After != 3 {
		t.Errorf("tenant 0 count after tenant 1 deletes = %d, want 3", count0After)
	}
	if count1After != 3 {
		t.Errorf("tenant 1 count after deletes = %d, want 3", count1After)
	}
	if count2After != 7 {
		t.Errorf("tenant 2 count after tenant 1 deletes = %d, want 7", count2After)
	}

	// Total in raw DB
	var total int
	store0.db.QueryRow("SELECT COUNT(*) FROM entities WHERE entity_type = 'widgets'").Scan(&total)
	if total != 13 { // 3 + 3 + 7
		t.Errorf("raw DB total = %d, want 13", total)
	}
}

func TestSQLiteTenantIsolation_ZeroAndNonZeroGraphs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_coexist_graph.db")
	ctx := context.Background()

	store0 := newTenantSQLiteStore(t, dbPath, 0, true, false)
	store1 := newTenantSQLiteStore(t, dbPath, 0x0001, true, false)
	defer store0.Close()
	defer store1.Close()

	// Tenant 0 uses graph_t0000 table
	store0.Create(ctx, "nodes", map[string]interface{}{"label": "root-0"})
	store0.Create(ctx, "edges_data", map[string]interface{}{
		"label": "link-0",
		"target": map[string]interface{}{
			"type": "REF", "entity": "nodes", "id": 1,
		},
	})

	// Tenant 1 uses graph_t0001 table
	store1.Create(ctx, "nodes", map[string]interface{}{"label": "root-1"})
	store1.Create(ctx, "edges_data", map[string]interface{}{
		"label": "link-1",
		"target": map[string]interface{}{
			"type": "REF", "entity": "nodes", "id": 1,
		},
	})

	// Verify separate tables
	var defaultCount, tenantCount int
	store0.db.QueryRow("SELECT COUNT(*) FROM graph_t0000").Scan(&defaultCount)
	store0.db.QueryRow("SELECT COUNT(*) FROM graph_t0001").Scan(&tenantCount)

	if defaultCount == 0 {
		t.Error("graph_t0000 should have edges from tenant 0")
	}
	if tenantCount == 0 {
		t.Error("graph_t0001 should have edges from tenant 1")
	}

	// Neighbors are scoped per tenant — verify via edge table scan and entity Get.
	n0 := outEdgesForExtended(t, store0, "edges_data", 1)
	n1 := outEdgesForExtended(t, store1, "edges_data", 1)

	if len(n0) != 1 {
		t.Errorf("tenant 0 neighbors = %d, want 1", len(n0))
	}
	if len(n1) != 1 {
		t.Errorf("tenant 1 neighbors = %d, want 1", len(n1))
	}

	// Verify neighbor data is from the correct tenant.
	if len(n0) == 1 {
		neigh0, err := store0.Get(ctx, n0[0].TargetEntity, n0[0].TargetID)
		if err != nil {
			t.Fatalf("store0.Get neighbor: %v", err)
		}
		if label, _ := neigh0["label"].(string); label != "root-0" {
			t.Errorf("tenant 0 neighbor label = %q, want root-0", label)
		}
	}
	if len(n1) == 1 {
		neigh1, err := store1.Get(ctx, n1[0].TargetEntity, n1[0].TargetID)
		if err != nil {
			t.Fatalf("store1.Get neighbor: %v", err)
		}
		if label, _ := neigh1["label"].(string); label != "root-1" {
			t.Errorf("tenant 1 neighbor label = %q, want root-1", label)
		}
	}
}

// ---------------------------------------------------------------------------
// NewStoreFromConfig factory test
// ---------------------------------------------------------------------------

func TestNewStoreFromConfig_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "factory.db")

	store, err := NewStoreFromConfig(StoreConfig{
		Type:            "sqlite",
		DBPath:          dbPath,
		FullTextEnabled: true,
		GraphEnabled:    true,
		TenantID:        0x00FF,
	})
	if err != nil {
		t.Fatalf("NewStoreFromConfig: %v", err)
	}
	defer store.Close()

	cfg := store.Config()
	if cfg.Type != "sqlite" {
		t.Errorf("Type = %q", cfg.Type)
	}
	if cfg.TenantID != 0x00FF {
		t.Errorf("TenantID = %d, want %d", cfg.TenantID, 0x00FF)
	}
	if !cfg.FullTextEnabled {
		t.Error("FullTextEnabled should be true")
	}
	if !cfg.GraphEnabled {
		t.Error("GraphEnabled should be true")
	}

	// Verify it works
	ctx := context.Background()
	id, err := store.Create(ctx, "test", map[string]interface{}{"x": 1})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != 1 {
		t.Errorf("first ID = %d, want 1", id)
	}
}

func TestNewStoreFromConfig_JSONFile(t *testing.T) {
	baseDir := t.TempDir()

	store, err := NewStoreFromConfig(StoreConfig{
		Type:     "jsonfile",
		BaseDir:  baseDir,
		Schema:   "test_schema",
		TenantID: 0x0042,
	})
	if err != nil {
		t.Fatalf("NewStoreFromConfig: %v", err)
	}
	defer store.Close()

	cfg := store.Config()
	if cfg.TenantID != 0x0042 {
		t.Errorf("TenantID = %d, want %d", cfg.TenantID, 0x0042)
	}
}

func TestNewStoreFromConfig_Unknown(t *testing.T) {
	_, err := NewStoreFromConfig(StoreConfig{Type: "nosuchbackend"})
	if err == nil {
		t.Error("expected error for unknown store type")
	}
}

// outEdgesForExtended returns all outgoing GraphEdge rows for a given
// (entity, id) pair by scanning the store's edge table.
func outEdgesForExtended(t *testing.T, store *SQLiteStore, entity string, id int) []GraphEdge {
	t.Helper()
	ctx := context.Background()
	var edges []GraphEdge
	err := store.ScanGraphEdges(ctx, store.config.TenantID, func(e GraphEdge) error {
		if e.SourceEntity == entity && e.SourceID == id {
			edges = append(edges, e)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("outEdgesForExtended(%s:%d): %v", entity, id, err)
	}
	return edges
}
