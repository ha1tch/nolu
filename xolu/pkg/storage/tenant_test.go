// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newTenantSQLiteStore creates a SQLiteStore scoped to a tenant for testing.
func newTenantSQLiteStore(t *testing.T, dbPath string, tenantID uint16, graph, fts bool) *SQLiteStore {
	t.Helper()
	store, err := NewSQLiteStore(dbPath, SQLiteConfig{
		DBPath:            dbPath,
		EnableWAL:         true,
		EnableForeignKeys: true,
		CacheSize:         2000,
		BusyTimeout:       5000,
		FullTextEnabled:   fts,
		GraphEnabled:      graph,
		TenantID:          tenantID,
	})
	if err != nil {
		t.Fatalf("NewSQLiteStore(tenant=%d): %v", tenantID, err)
	}
	return store
}

// newTenantJSONFileStore creates a JSONFileStore scoped to a tenant for testing.
func newTenantJSONFileStore(t *testing.T, baseDir string, tenantID uint16) *JSONFileStore {
	t.Helper()
	store, err := NewJSONFileStore(baseDir, "default")
	if err != nil {
		t.Fatalf("NewJSONFileStore: %v", err)
	}
	store.storeConfig.TenantID = tenantID
	return store
}

// ---------------------------------------------------------------------------
// SQLite tenant isolation
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_CRUD(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_iso.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Both tenants create entities with the same type
	idA, err := storeA.Create(ctx, "users", map[string]interface{}{"name": "Alice"})
	if err != nil {
		t.Fatalf("storeA.Create: %v", err)
	}
	idB, err := storeB.Create(ctx, "users", map[string]interface{}{"name": "Bob"})
	if err != nil {
		t.Fatalf("storeB.Create: %v", err)
	}

	// Per-tenant sequences: both should start at 1
	if idA != 1 {
		t.Errorf("tenant A first ID = %d, want 1", idA)
	}
	if idB != 1 {
		t.Errorf("tenant B first ID = %d, want 1", idB)
	}

	// Tenant A sees its own data at ID 1, not tenant B's
	dataA, err := storeA.Get(ctx, "users", 1)
	if err != nil {
		t.Fatalf("storeA.Get(1): %v", err)
	}
	if name, _ := dataA["name"].(string); name != "Alice" {
		t.Errorf("storeA.Get(1).name = %q, want Alice (got tenant B's data?)", name)
	}

	// Tenant B sees its own data at ID 1, not tenant A's
	dataB, err := storeB.Get(ctx, "users", 1)
	if err != nil {
		t.Fatalf("storeB.Get(1): %v", err)
	}
	if name, _ := dataB["name"].(string); name != "Bob" {
		t.Errorf("storeB.Get(1).name = %q, want Bob (got tenant A's data?)", name)
	}

	// Create a second record in tenant A only — tenant B should not see it
	idA2, _ := storeA.Create(ctx, "users", map[string]interface{}{"name": "Charlie"})
	_, err = storeB.Get(ctx, "users", idA2)
	if err == nil {
		t.Error("storeB.Get(idA2) should fail — tenant B has no record at this ID")
	}

	// Each tenant sees only its own records in List
	listA, err := storeA.List(ctx, "users")
	if err != nil {
		t.Fatalf("storeA.List: %v", err)
	}
	if len(listA) != 2 {
		t.Errorf("storeA.List len = %d, want 2 (Alice + Charlie)", len(listA))
	}

	listB, err := storeB.List(ctx, "users")
	if err != nil {
		t.Fatalf("storeB.List: %v", err)
	}
	if len(listB) != 1 {
		t.Errorf("storeB.List len = %d, want 1", len(listB))
	}
	if len(listB) == 1 {
		if name, _ := listB[0]["name"].(string); name != "Bob" {
			t.Errorf("storeB.List[0].name = %q, want Bob", name)
		}
	}
}

func TestSQLiteTenantIsolation_UpdateDeletePatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_ud.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Tenant A creates 2 records (IDs 1 and 2), tenant B creates 1 (ID 1)
	storeA.Create(ctx, "items", map[string]interface{}{"title": "A-item-1"})
	storeA.Create(ctx, "items", map[string]interface{}{"title": "A-item-2"})
	storeB.Create(ctx, "items", map[string]interface{}{"title": "B-item-1"})

	// Tenant B cannot update tenant A's record at ID 2 (B has no ID 2)
	err := storeB.Update(ctx, "items", 2, map[string]interface{}{"title": "hacked"})
	if err == nil {
		t.Error("storeB.Update on ID 2 should fail — tenant B has no record at ID 2")
	}

	// Tenant B cannot patch tenant A's record at ID 2
	err = storeB.Patch(ctx, "items", 2, map[string]interface{}{"title": "hacked"})
	if err == nil {
		t.Error("storeB.Patch on ID 2 should fail — tenant B has no record at ID 2")
	}

	// Tenant B cannot delete tenant A's record at ID 2
	err = storeB.Delete(ctx, "items", 2)
	if err == nil {
		t.Error("storeB.Delete on ID 2 should fail — tenant B has no record at ID 2")
	}

	// Verify tenant A's record at ID 2 is untouched
	data, err := storeA.Get(ctx, "items", 2)
	if err != nil {
		t.Fatalf("storeA.Get(2): %v", err)
	}
	if title, _ := data["title"].(string); title != "A-item-2" {
		t.Errorf("tenant A record mutated: title = %q, want A-item-2", title)
	}

	// Also verify: tenant B updating its own ID 1 doesn't affect tenant A's ID 1
	storeB.Update(ctx, "items", 1, map[string]interface{}{"title": "B-updated"})
	dataA, _ := storeA.Get(ctx, "items", 1)
	if title, _ := dataA["title"].(string); title != "A-item-1" {
		t.Errorf("tenant A's ID 1 was corrupted by tenant B's update: title = %q", title)
	}
}

func TestSQLiteTenantIsolation_Search(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_search.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	storeA.Create(ctx, "docs", map[string]interface{}{"title": "secret-alpha"})
	storeB.Create(ctx, "docs", map[string]interface{}{"title": "secret-beta"})

	resultsA, err := storeA.Search(ctx, "docs", "title", "secret", "contains")
	if err != nil {
		t.Fatalf("storeA.Search: %v", err)
	}
	if len(resultsA) != 1 {
		t.Errorf("storeA.Search returned %d results, want 1", len(resultsA))
	}

	resultsB, err := storeB.Search(ctx, "docs", "title", "secret", "contains")
	if err != nil {
		t.Fatalf("storeB.Search: %v", err)
	}
	if len(resultsB) != 1 {
		t.Errorf("storeB.Search returned %d results, want 1", len(resultsB))
	}
}

func TestSQLiteTenantIsolation_Exists(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_exists.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	storeA.Create(ctx, "things", map[string]interface{}{"x": 1})

	if !storeA.Exists(ctx, "things", 1) {
		t.Error("storeA.Exists should be true for its own record")
	}
	if storeB.Exists(ctx, "things", 1) {
		t.Error("storeB.Exists should be false for tenant A's record")
	}
}

func TestSQLiteTenantIsolation_CountEntities(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_count.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	for i := 0; i < 5; i++ {
		storeA.Create(ctx, "widgets", map[string]interface{}{"n": i})
	}
	for i := 0; i < 3; i++ {
		storeB.Create(ctx, "widgets", map[string]interface{}{"n": i})
	}

	countA, _ := storeA.CountEntities(ctx, "widgets")
	countB, _ := storeB.CountEntities(ctx, "widgets")

	if countA != 5 {
		t.Errorf("tenant A count = %d, want 5", countA)
	}
	if countB != 3 {
		t.Errorf("tenant B count = %d, want 3", countB)
	}
}

func TestSQLiteTenantIsolation_ListEntities(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_list_ent.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	storeA.Create(ctx, "alpha", map[string]interface{}{"x": 1})
	storeA.Create(ctx, "beta", map[string]interface{}{"x": 1})
	storeB.Create(ctx, "gamma", map[string]interface{}{"x": 1})

	entA, _ := storeA.ListEntities(ctx)
	entB, _ := storeB.ListEntities(ctx)

	if len(entA) != 2 {
		t.Errorf("tenant A entity types = %v, want [alpha beta]", entA)
	}
	if len(entB) != 1 {
		t.Errorf("tenant B entity types = %v, want [gamma]", entB)
	}
}

func TestSQLiteTenantIsolation_Save(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_save.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, false)
	defer storeA.Close()
	defer storeB.Close()

	// Both tenants can Save with the same ID — no conflict
	_, err := storeA.Save(ctx, "records", 42, map[string]interface{}{"owner": "A"})
	if err != nil {
		t.Fatalf("storeA.Save: %v", err)
	}
	_, err = storeB.Save(ctx, "records", 42, map[string]interface{}{"owner": "B"})
	if err != nil {
		t.Fatalf("storeB.Save: %v", err)
	}

	dataA, _ := storeA.Get(ctx, "records", 42)
	dataB, _ := storeB.Get(ctx, "records", 42)

	if dataA["owner"] != "A" {
		t.Errorf("tenant A record owner = %v, want A", dataA["owner"])
	}
	if dataB["owner"] != "B" {
		t.Errorf("tenant B record owner = %v, want B", dataB["owner"])
	}
}

func TestSQLiteTenantIsolation_GraphEdges(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_graph.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, true, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, true, false)
	defer storeA.Close()
	defer storeB.Close()

	// Tenant A: create target then source with REF
	storeA.Create(ctx, "projects", map[string]interface{}{"name": "Project-A"})
	storeA.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-A",
		"project": map[string]interface{}{
			"type":   "REF",
			"entity": "projects",
			"id":     1,
		},
	})

	// Tenant B: same structure, different data
	storeB.Create(ctx, "projects", map[string]interface{}{"name": "Project-B"})
	storeB.Create(ctx, "tasks", map[string]interface{}{
		"title": "Task-B",
		"project": map[string]interface{}{
			"type":   "REF",
			"entity": "projects",
			"id":     1,
		},
	})

	// Verify per-tenant graph tables exist
	var tableA, tableB string
	storeA.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tenant.GraphEdgesTableName(0x0001)).Scan(&tableA)
	storeB.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?",
		tenant.GraphEdgesTableName(0x0002)).Scan(&tableB)

	if tableA == "" {
		t.Error("tenant A graph edge table not created")
	}
	if tableB == "" {
		t.Error("tenant B graph edge table not created")
	}

	// Tenant A neighbours don't leak into tenant B.
	// Use ScanGraphEdges (the edge table) to count and inspect outgoing edges,
	// then Get the neighbor entity to verify data isolation.
	nA := outEdgesFor(t, storeA, "tasks", 1)
	if len(nA) != 1 {
		t.Errorf("tenant A neighbors = %d, want 1", len(nA))
	}
	if len(nA) == 1 {
		proj, err := storeA.Get(ctx, nA[0].TargetEntity, nA[0].TargetID)
		if err != nil {
			t.Fatalf("storeA.Get neighbor: %v", err)
		}
		if name, _ := proj["name"].(string); name != "Project-A" {
			t.Errorf("tenant A neighbor name = %q, want Project-A", name)
		}
	}

	nB := outEdgesFor(t, storeB, "tasks", 1)
	if len(nB) != 1 {
		t.Errorf("tenant B neighbors = %d, want 1", len(nB))
	}
	if len(nB) == 1 {
		proj, err := storeB.Get(ctx, nB[0].TargetEntity, nB[0].TargetID)
		if err != nil {
			t.Fatalf("storeB.Get neighbor: %v", err)
		}
		if name, _ := proj["name"].(string); name != "Project-B" {
			t.Errorf("tenant B neighbor name = %q, want Project-B", name)
		}
	}
}

func TestSQLiteTenantIsolation_FTS(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_fts.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, false, true)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, false, true)
	defer storeA.Close()
	defer storeB.Close()

	storeA.Create(ctx, "notes", map[string]interface{}{"body": "quantum entanglement research"})
	storeB.Create(ctx, "notes", map[string]interface{}{"body": "quantum computing hardware"})

	resultsA, err := storeA.FullTextSearch(ctx, "quantum", "")
	if err != nil {
		t.Fatalf("storeA.FullTextSearch: %v", err)
	}
	if len(resultsA) != 1 {
		t.Errorf("tenant A FTS results = %d, want 1", len(resultsA))
	}
	if len(resultsA) == 1 {
		if body, _ := resultsA[0]["body"].(string); body != "quantum entanglement research" {
			t.Errorf("tenant A FTS body = %q, want 'quantum entanglement research'", body)
		}
	}

	resultsB, err := storeB.FullTextSearch(ctx, "quantum", "")
	if err != nil {
		t.Fatalf("storeB.FullTextSearch: %v", err)
	}
	if len(resultsB) != 1 {
		t.Errorf("tenant B FTS results = %d, want 1", len(resultsB))
	}
}

func TestSQLiteTenantIsolation_GraphIntegrity(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_integrity.db")
	ctx := context.Background()

	storeA := newTenantSQLiteStore(t, dbPath, 0x0001, true, false)
	storeB := newTenantSQLiteStore(t, dbPath, 0x0002, true, false)
	defer storeA.Close()
	defer storeB.Close()

	storeA.Create(ctx, "nodes", map[string]interface{}{"label": "A1"})
	storeA.Create(ctx, "edges", map[string]interface{}{
		"from": map[string]interface{}{"type": "REF", "entity": "nodes", "id": 1},
	})

	storeB.Create(ctx, "nodes", map[string]interface{}{"label": "B1"})

	// Each tenant's integrity check should pass independently
	if err := storeA.VerifyGraphIntegrity(ctx); err != nil {
		t.Errorf("tenant A graph integrity: %v", err)
	}
	if err := storeB.VerifyGraphIntegrity(ctx); err != nil {
		t.Errorf("tenant B graph integrity: %v", err)
	}
}

// ---------------------------------------------------------------------------
// Unscoped (tenant 0) backward compatibility
// ---------------------------------------------------------------------------

func TestSQLiteTenantZero_BackwardCompatible(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "tenant_zero.db")
	ctx := context.Background()

	store := newTenantSQLiteStore(t, dbPath, 0, false, false)
	defer store.Close()

	id, err := store.Create(ctx, "users", map[string]interface{}{"name": "Legacy"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if id != 1 {
		t.Errorf("first ID = %d, want 1", id)
	}

	data, err := store.Get(ctx, "users", 1)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if data["name"] != "Legacy" {
		t.Errorf("name = %v, want Legacy", data["name"])
	}

	list, _ := store.List(ctx, "users")
	if len(list) != 1 {
		t.Errorf("List len = %d, want 1", len(list))
	}

	if cfg := store.Config(); cfg.TenantID != 0 {
		t.Errorf("Config().TenantID = %d, want 0", cfg.TenantID)
	}
}

// ---------------------------------------------------------------------------
// JSONFile tenant isolation
// ---------------------------------------------------------------------------

func TestJSONFileTenantIsolation_Paths(t *testing.T) {
	baseDir := t.TempDir()

	storeA := newTenantJSONFileStore(t, baseDir, 0x000A)
	storeB := newTenantJSONFileStore(t, baseDir, 0x000B)

	dirA := storeA.GetEntityDir("widgets")
	dirB := storeB.GetEntityDir("widgets")

	expectedA := filepath.Join(baseDir, "default", "t000A", "widgets")
	expectedB := filepath.Join(baseDir, "default", "t000B", "widgets")

	if dirA != expectedA {
		t.Errorf("tenant A dir = %s, want %s", dirA, expectedA)
	}
	if dirB != expectedB {
		t.Errorf("tenant B dir = %s, want %s", dirB, expectedB)
	}
}

func TestJSONFileTenantIsolation_CRUD(t *testing.T) {
	baseDir := t.TempDir()
	ctx := context.Background()

	storeA := newTenantJSONFileStore(t, baseDir, 0x000A)
	storeB := newTenantJSONFileStore(t, baseDir, 0x000B)

	idA, err := storeA.Create(ctx, "items", map[string]interface{}{"name": "alpha"})
	if err != nil {
		t.Fatalf("storeA.Create: %v", err)
	}
	idB, err := storeB.Create(ctx, "items", map[string]interface{}{"name": "beta"})
	if err != nil {
		t.Fatalf("storeB.Create: %v", err)
	}

	// Both start at 1 (separate _next_id.json files in separate dirs)
	if idA != 1 || idB != 1 {
		t.Errorf("IDs = (%d, %d), want (1, 1)", idA, idB)
	}

	// Tenant A cannot see tenant B's data
	_, err = storeA.Get(ctx, "items", idB)
	// For JSONFile, this may or may not error depending on file existence
	// but the physical files are in different directories, so it should 404
	if err == nil {
		// Check it's not returning B's data
		dataA, _ := storeA.Get(ctx, "items", 1)
		if name, _ := dataA["name"].(string); name == "beta" {
			t.Error("tenant A retrieved tenant B's data — isolation violated")
		}
	}

	listA, _ := storeA.List(ctx, "items")
	listB, _ := storeB.List(ctx, "items")

	if len(listA) != 1 {
		t.Errorf("storeA.List len = %d, want 1", len(listA))
	}
	if len(listB) != 1 {
		t.Errorf("storeB.List len = %d, want 1", len(listB))
	}
}

func TestJSONFileTenantZero_DefaultPaths(t *testing.T) {
	baseDir := t.TempDir()

	store := newTenantJSONFileStore(t, baseDir, 0)

	dir := store.GetEntityDir("widgets")
	expected := filepath.Join(baseDir, "default", "widgets")

	if dir != expected {
		t.Errorf("tenant 0 dir = %s, want %s", dir, expected)
	}
}

// ---------------------------------------------------------------------------
// Config() correctness
// ---------------------------------------------------------------------------

func TestStoreConfig_SQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "cfg.db")
	store := newTenantSQLiteStore(t, dbPath, 0xBEF0, true, true)
	defer store.Close()

	cfg := store.Config()
	if cfg.Type != "sqlite" {
		t.Errorf("Type = %q, want sqlite", cfg.Type)
	}
	if cfg.TenantID != 0xBEF0 {
		t.Errorf("TenantID = %d, want %d", cfg.TenantID, 0xBEF0)
	}
	if !cfg.GraphEnabled {
		t.Error("GraphEnabled should be true")
	}
	if !cfg.FullTextEnabled {
		t.Error("FullTextEnabled should be true")
	}
}

func TestStoreConfig_JSONFile(t *testing.T) {
	store := newTenantJSONFileStore(t, t.TempDir(), 0x0042)

	cfg := store.Config()
	if cfg.Type != "jsonfile" {
		t.Errorf("Type = %q, want jsonfile", cfg.Type)
	}
	if cfg.TenantID != 0x0042 {
		t.Errorf("TenantID = %d, want %d", cfg.TenantID, 0x0042)
	}
}

// ---------------------------------------------------------------------------
// graphEdgesTable helper
// ---------------------------------------------------------------------------

func TestGraphEdgesTable(t *testing.T) {
	tests := []struct {
		tenantID uint16
		want     string
	}{
		{0, "graph_t0000"},
		{1, "graph_t0001"},
		{0xBEF0, "graph_tBEF0"},
		{0xFFFF, "graph_tFFFF"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("tenant_%04X", tt.tenantID), func(t *testing.T) {
			got := tenant.GraphEdgesTableName(tt.tenantID)
			if got != tt.want {
				t.Errorf("tenant.GraphEdgesTableName(%d) = %q, want %q", tt.tenantID, got, tt.want)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Stress: many tenants, same DB
// ---------------------------------------------------------------------------

func TestSQLiteTenantIsolation_ManyTenants(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "many_tenants.db")
	ctx := context.Background()

	const numTenants = 20
	const recordsPerTenant = 10

	stores := make([]*SQLiteStore, numTenants)
	for i := 0; i < numTenants; i++ {
		stores[i] = newTenantSQLiteStore(t, dbPath, uint16(i+1), false, false)
		defer stores[i].Close()
	}

	// Each tenant creates records
	for i, store := range stores {
		for j := 0; j < recordsPerTenant; j++ {
			_, err := store.Create(ctx, "data", map[string]interface{}{
				"tenant": i + 1,
				"seq":    j,
			})
			if err != nil {
				t.Fatalf("tenant %d, record %d: %v", i+1, j, err)
			}
		}
	}

	// Verify isolation
	for i, store := range stores {
		list, err := store.List(ctx, "data")
		if err != nil {
			t.Fatalf("tenant %d List: %v", i+1, err)
		}
		if len(list) != recordsPerTenant {
			t.Errorf("tenant %d sees %d records, want %d", i+1, len(list), recordsPerTenant)
		}
		// Verify all records belong to this tenant
		for _, rec := range list {
			if tenantVal, ok := rec["tenant"].(float64); ok {
				if int(tenantVal) != i+1 {
					t.Errorf("tenant %d sees record from tenant %d", i+1, int(tenantVal))
				}
			}
		}

		count, _ := store.CountEntities(ctx, "data")
		if count != recordsPerTenant {
			t.Errorf("tenant %d count = %d, want %d", i+1, count, recordsPerTenant)
		}
	}

	// Verify raw DB has all records
	var total int
	stores[0].db.QueryRow("SELECT COUNT(*) FROM entities WHERE entity_type = 'data'").Scan(&total)
	if total != numTenants*recordsPerTenant {
		t.Errorf("total records in DB = %d, want %d", total, numTenants*recordsPerTenant)
	}
}

// Ensure tests don't leave temp files
func TestMain(m *testing.M) {
	os.Exit(m.Run())
}

// outEdgesFor returns all outgoing GraphEdge rows for a given (entity, id)
// pair by scanning the store's edge table.
func outEdgesFor(t *testing.T, store *SQLiteStore, entity string, id int) []GraphEdge {
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
		t.Fatalf("outEdgesFor(%s:%d): %v", entity, id, err)
	}
	return edges
}
