// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// e2e_test.go
//
// End-to-end tests exercising multi-step workflows through the HTTP API.
// Ordered by frequency of use in a typical multi-tenant deployment:
//
//   1. Multi-step workflows (CRUD → graph → OQL analytics)
//   2. Graph endpoint coverage (node info, degree, in/out, shortest path, etc.)
//   3. Error/edge-case hardening (malformed inputs, boundary conditions)
//
// These tests complement the existing server_test.go (unit-level HTTP),
// integration_test.go (OQL/mutation paths), and
// tier1_oql_test.go (analytics/scalar functions).
//
// Author: ha1tch <h@ual.fi>
// Repository: https://github.com/ha1tch/xolu/

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// ----------------------------------------------------------------------------
// Test harness
// ----------------------------------------------------------------------------

// e2eEnv holds a fully wired test server with graph, OQL, and Sulpher enabled.
type e2eEnv struct {
	ts     *httptest.Server
	tmpDir string
	t      *testing.T
}

// e2eEntities covers the test schema entities plus a few extras for graph testing.
var e2eEntities = []string{
	"assets", "events", "asset_types", "fsm_machines", "rules",
	"sensors", "sensor_bindings", "qr_codes", "qr_scans",
	"users", "webhooks", "locations", "gateways",
}

func newE2EEnv(t *testing.T) *e2eEnv {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "olu-e2e-*")
	if err != nil {
		t.Fatal(err)
	}

	for _, entity := range e2eEntities {
		dir := filepath.Join(tmpDir, "test_schema", entity)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:        "jsonfile",
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        true,
		GraphMode:           "flat",
		FullTextEnabled:     false,
		CascadingDelete:     false,
		RefEmbedDepth:       3,
		MaxEmbedDepth:       10,
		MaxEntitySize:       1048576,
		PatchNullBehavior:   "store",
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		GraphQueryTTL:       86400,
		MaxQueryDepth:       10,
		TenantMode:          "path",
		TenantAutoRegister:  true, // Tests rely on auto-registration
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}
	store, err := storage.NewStore("jsonfile", storeConfig)
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	return &e2eEnv{ts: ts, tmpDir: tmpDir, t: t}
}

func (e *e2eEnv) cleanup() {
	e.ts.Close()
	os.RemoveAll(e.tmpDir)
}

// do makes an HTTP request and returns status + raw bytes.
func (e *e2eEnv) do(method, path string, body interface{}) (int, []byte) {
	e.t.Helper()
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			e.t.Fatal(err)
		}
	}
	req, err := http.NewRequest(method, e.ts.URL+path, bytes.NewBuffer(bodyBytes))
	if err != nil {
		e.t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	buf := &bytes.Buffer{}
	buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

// doJSON does an HTTP request and unmarshals the response.
func (e *e2eEnv) doJSON(method, path string, body interface{}) (int, map[string]interface{}) {
	e.t.Helper()
	status, raw := e.do(method, path, body)
	var result map[string]interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			e.t.Fatalf("unmarshal (status %d): %s\nbody: %s", status, err, string(raw))
		}
	}
	return status, result
}

// create creates an entity and returns its ID.
func (e *e2eEnv) create(path string, data map[string]interface{}) int {
	e.t.Helper()
	status, result := e.doJSON("POST", path, data)
	if status != http.StatusCreated {
		e.t.Fatalf("create %s: expected 201, got %d: %v", path, status, result)
	}
	id, ok := result["id"].(float64)
	if !ok {
		e.t.Fatalf("create %s: no id in response: %v", path, result)
	}
	return int(id)
}

// oqlData executes OQL and returns data rows.
func (e *e2eEnv) oqlData(query string) []interface{} {
	e.t.Helper()
	status, result := e.doJSON("POST", "/api/v1/oql/query", map[string]interface{}{"query": query})
	if status != http.StatusOK {
		e.t.Fatalf("OQL failed (status %d): %v\nquery: %s", status, result, query)
	}
	data, ok := result["data"].([]interface{})
	if !ok {
		return []interface{}{}
	}
	return data
}

// oqlTenantData executes OQL in a tenant scope and returns data rows.
func (e *e2eEnv) oqlTenantData(tenantID, query string) []interface{} {
	e.t.Helper()
	path := fmt.Sprintf("/api/v1/tenant/%s/oql/query", tenantID)
	status, result := e.doJSON("POST", path, map[string]interface{}{"query": query})
	if status != http.StatusOK {
		e.t.Fatalf("OQL tenant failed (status %d): %v\nquery: %s", status, result, query)
	}
	data, ok := result["data"].([]interface{})
	if !ok {
		return []interface{}{}
	}
	return data
}

// toFloat64 safely extracts a float64 from an interface{}.
func toF64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	default:
		return 0
	}
}
// ref creates a structured REF object for graph-aware entity references.
func ref(entity string, id int) map[string]interface{} {
	return map[string]interface{}{
		"type":   "REF",
		"entity": entity,
		"id":     id,
	}
}


// =============================================================================
// 1. MULTI-STEP WORKFLOWS
//    These test realistic sequences a stateful application performs.
// =============================================================================

// TestE2E_AssetLifecycle tests: create asset type → create assets → attach
// sensors → generate events → query analytics → verify graph.
func TestE2E_AssetLifecycle(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// --- Step 1: Create an asset type ---
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name":     "Fire Extinguisher",
		"code":     "FE",
		"fields":   []string{"pressure_psi", "last_inspection"},
		"fsm_type": "fire_ext_lifecycle",
	})

	// --- Step 2: Create assets referencing the type ---
	a1ID := env.create("/api/v1/assets", map[string]interface{}{
		"code":         "FE-001",
		"name":         "Lobby Extinguisher",
		"asset_type":   ref("asset_types", atID),
		"status":       "active",
		"pressure_psi": 195.0,
		"location":     "Building A, Floor 1",
	})
	a2ID := env.create("/api/v1/assets", map[string]interface{}{
		"code":         "FE-002",
		"name":         "Kitchen Extinguisher",
		"asset_type":   ref("asset_types", atID),
		"status":       "active",
		"pressure_psi": 180.0,
		"location":     "Building A, Floor 2",
	})

	// --- Step 3: Create a sensor and bind to asset 1 ---
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code":        "PRES-001",
		"type":        "pressure",
		"protocol":    "lorawan",
		"asset":       ref("assets", a1ID),
		"status":      "active",
	})

	// --- Step 4: Generate events against the assets ---
	for i := 0; i < 5; i++ {
		env.create("/api/v1/events", map[string]interface{}{
			"asset":      ref("assets", a1ID),
			"event_type": "reading",
			"sensor":     ref("sensors", sID),
			"value":      190.0 + float64(i),
			"timestamp":  fmt.Sprintf("2025-06-0%dT10:00:00Z", i+1),
		})
	}
	for i := 0; i < 3; i++ {
		env.create("/api/v1/events", map[string]interface{}{
			"asset":      ref("assets", a2ID),
			"event_type": "inspection",
			"value":      0,
			"timestamp":  fmt.Sprintf("2025-07-0%dT14:00:00Z", i+1),
		})
	}

	// --- Step 5: OQL analytics ---
	t.Run("count events per type", func(t *testing.T) {
		data := env.oqlData("SELECT event_type, COUNT(*) as cnt FROM events GROUP BY event_type")
		if len(data) != 2 {
			t.Fatalf("expected 2 event types, got %d", len(data))
		}
		found := map[string]float64{}
		for _, row := range data {
			rec := row.(map[string]interface{})
			found[rec["event_type"].(string)] = toF64(rec["cnt"])
		}
		if found["reading"] != 5 {
			t.Errorf("expected 5 readings, got %v", found["reading"])
		}
		if found["inspection"] != 3 {
			t.Errorf("expected 3 inspections, got %v", found["inspection"])
		}
	})

	t.Run("count assets by status", func(t *testing.T) {
		data := env.oqlData("SELECT COUNT(*) as total FROM assets WHERE status = 'active'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		if toF64(data[0].(map[string]interface{})["total"]) != 2 {
			t.Errorf("expected 2 active assets")
		}
	})

	// --- Step 6: Verify graph structure ---
	t.Run("graph stats reflect entities", func(t *testing.T) {
		_, stats := env.doJSON("GET", "/api/v1/graph/stats", nil)
		nodeCount := int(toF64(stats["node_count"]))
		edgeCount := int(toF64(stats["edge_count"]))
		// We created: 1 asset_type + 2 assets + 1 sensor + 8 events = 12 nodes
		if nodeCount < 10 {
			t.Errorf("expected at least 10 graph nodes, got %d", nodeCount)
		}
		// Edges from REFs: 2 assets→type + 1 sensor→asset + 5 events→asset +
		// 5 events→sensor + 3 events→asset = at least 16
		if edgeCount < 10 {
			t.Errorf("expected at least 10 graph edges, got %d", edgeCount)
		}
	})

	// --- Step 7: PATCH an asset and verify ---
	t.Run("patch asset status", func(t *testing.T) {
		status, _ := env.doJSON("PATCH", fmt.Sprintf("/api/v1/assets/%d", a2ID),
			map[string]interface{}{"status": "maintenance"})
		if status != http.StatusOK {
			t.Fatalf("PATCH expected 200, got %d", status)
		}
		// Verify via GET
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d", a2ID), nil)
		if status != http.StatusOK {
			t.Fatalf("GET after PATCH expected 200, got %d", status)
		}
		if result["status"] != "maintenance" {
			t.Errorf("expected status=maintenance, got %v", result["status"])
		}
	})

	// --- Step 8: DELETE an event and verify count ---
	t.Run("delete event reduces count", func(t *testing.T) {
		beforeData := env.oqlData("SELECT COUNT(*) as cnt FROM events")
		beforeCount := toF64(beforeData[0].(map[string]interface{})["cnt"])

		// Delete the last event (ID 8 — 5 readings + 3 inspections)
		status, _ := env.doJSON("DELETE", "/api/v1/events/8", nil)
		if status != http.StatusOK && status != http.StatusNoContent {
			t.Fatalf("DELETE expected 200 or 204, got %d", status)
		}

		afterData := env.oqlData("SELECT COUNT(*) as cnt FROM events")
		afterCount := toF64(afterData[0].(map[string]interface{})["cnt"])
		if afterCount != beforeCount-1 {
			t.Errorf("expected count to drop by 1: before=%v after=%v", beforeCount, afterCount)
		}
	})
}

// TestE2E_MultiTenantWorkflow tests complete tenant isolation end-to-end.
func TestE2E_MultiTenantWorkflow(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	tenantA := "tenant_alpha"
	tenantB := "tenant_beta"

	// Create assets in tenant A
	for i := 0; i < 3; i++ {
		env.create(fmt.Sprintf("/api/v1/tenant/%s/assets", tenantA), map[string]interface{}{
			"code":   fmt.Sprintf("A-%03d", i+1),
			"name":   fmt.Sprintf("Alpha Asset %d", i+1),
			"status": "active",
		})
	}

	// Create assets in tenant B
	for i := 0; i < 2; i++ {
		env.create(fmt.Sprintf("/api/v1/tenant/%s/assets", tenantB), map[string]interface{}{
			"code":   fmt.Sprintf("B-%03d", i+1),
			"name":   fmt.Sprintf("Beta Asset %d", i+1),
			"status": "active",
		})
	}

	t.Run("tenant A sees only its assets via OQL", func(t *testing.T) {
		data := env.oqlTenantData(tenantA, "SELECT COUNT(*) as cnt FROM assets")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		cnt := toF64(data[0].(map[string]interface{})["cnt"])
		if cnt != 3 {
			t.Errorf("tenant A expected 3 assets, got %v", cnt)
		}
	})

	t.Run("tenant B sees only its assets via OQL", func(t *testing.T) {
		data := env.oqlTenantData(tenantB, "SELECT COUNT(*) as cnt FROM assets")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		cnt := toF64(data[0].(map[string]interface{})["cnt"])
		if cnt != 2 {
			t.Errorf("tenant B expected 2 assets, got %v", cnt)
		}
	})

	t.Run("tenant A list endpoint is isolated", func(t *testing.T) {
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/tenant/%s/assets", tenantA), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		items, ok := result["data"].([]interface{})
		if !ok {
			t.Fatalf("expected data array, got %T", result["data"])
		}
		if len(items) != 3 {
			t.Errorf("tenant A list expected 3, got %d", len(items))
		}
	})

	t.Run("cross-tenant GET by ID is blocked", func(t *testing.T) {
		// Tenant A has 3 assets (id=1,2,3), tenant B has 2 (id=1,2).
		// Access an ID that only exists in tenant A (id=3) via tenant B scope.
		status, _ := env.doJSON("GET", fmt.Sprintf("/api/v1/tenant/%s/assets/3", tenantB), nil)
		if status != http.StatusNotFound {
			t.Errorf("cross-tenant GET expected 404, got %d", status)
		}
	})

	// NOTE: No "global OQL sees all assets" sub-test. Non-tenant routes use
	// tenant 0 (unscoped store) and cannot aggregate across tenant-scoped data.
}

// TestE2E_SaveEndpoint tests the POST /{entity}/save/{id} upsert endpoint.
func TestE2E_SaveEndpoint(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("save creates entity with specific ID", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/assets/save/100", map[string]interface{}{
			"code":   "SAVE-001",
			"name":   "Saved Asset",
			"status": "active",
		})
		if status != http.StatusCreated {
			t.Fatalf("save expected 201, got %d: %v", status, result)
		}

		// Verify via GET
		status, result = env.doJSON("GET", "/api/v1/assets/100", nil)
		if status != http.StatusOK {
			t.Fatalf("GET after save expected 200, got %d", status)
		}
		if result["code"] != "SAVE-001" {
			t.Errorf("expected code=SAVE-001, got %v", result["code"])
		}
	})

	t.Run("save with existing ID overwrites (upsert)", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/assets/save/100", map[string]interface{}{
			"code":   "SAVE-002",
			"name":   "Overwritten Asset",
			"status": "active",
		})
		if status != http.StatusOK {
			t.Errorf("save overwrite expected 200, got %d: %v", status, result)
		}

		// Verify the data was actually replaced.
		status, result = env.doJSON("GET", "/api/v1/assets/100", nil)
		if status != http.StatusOK {
			t.Fatalf("GET after overwrite expected 200, got %d", status)
		}
		if result["code"] != "SAVE-002" {
			t.Errorf("expected overwritten code=SAVE-002, got %v", result["code"])
		}
	})

	t.Run("save with invalid ID returns error", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/assets/save/-1", map[string]interface{}{
			"code": "BAD",
		})
		if status != http.StatusBadRequest {
			t.Errorf("save negative ID expected 400, got %d", status)
		}
	})

	t.Run("save updates graph", func(t *testing.T) {
		// Create a type then save an asset referencing it
		atID := env.create("/api/v1/asset_types", map[string]interface{}{
			"name": "Pump",
			"code": "PMP",
		})
		status, _ := env.doJSON("POST", "/api/v1/assets/save/200", map[string]interface{}{
			"code":       "PMP-001",
			"name":       "Main Pump",
			"asset_type": ref("asset_types", atID),
		})
		if status != http.StatusCreated {
			t.Fatalf("save with ref expected 201, got %d", status)
		}

		// Graph should reflect the edge
		_, stats := env.doJSON("GET", "/api/v1/graph/stats", nil)
		edges := int(toF64(stats["edge_count"]))
		if edges < 1 {
			t.Errorf("expected at least 1 graph edge after save with ref, got %d", edges)
		}
	})
}

// TestE2E_SaveCAS tests conditional (optimistic concurrency) writes on save/{id}.
func TestE2E_SaveCAS(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Create initial record.
	status, _ := env.doJSON("POST", "/api/v1/assets/save/500", map[string]interface{}{
		"code": "CAS-001", "name": "CAS Asset", "status": "active",
	})
	if status != http.StatusCreated {
		t.Fatalf("initial save expected 201, got %d", status)
	}

	// Read to get current _version.
	_, entity := env.doJSON("GET", "/api/v1/assets/500", nil)
	version := entity["_version"]
	if version == nil {
		t.Fatal("GET response must include _version field")
	}

	t.Run("conditional save with correct version succeeds", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/assets/save/500", map[string]interface{}{
			"code":     "CAS-001",
			"name":     "CAS Asset Updated",
			"status":   "active",
			"_version": version,
		})
		if status != http.StatusOK {
			t.Errorf("conditional save with correct version expected 200, got %d", status)
		}
	})

	t.Run("conditional save with stale version returns 409 with current_version", func(t *testing.T) {
		// Version is now stale (was incremented by previous write).
		status, body := env.doJSON("POST", "/api/v1/assets/save/500", map[string]interface{}{
			"code":     "CAS-001",
			"name":     "Stale write attempt",
			"status":   "active",
			"_version": version,
		})
		if status != http.StatusConflict {
			t.Errorf("stale conditional save expected 409, got %d", status)
		}
		if body["current_version"] == nil {
			t.Error("409 body must contain current_version")
		}
		if body["current_version"] == version {
			t.Errorf("current_version should be newer than stale version %v", version)
		}
	})

	t.Run("unconditional save (no _version) always succeeds", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/assets/save/500", map[string]interface{}{
			"code": "CAS-001", "name": "Unconditional write", "status": "active",
		})
		if status != http.StatusOK {
			t.Errorf("unconditional save expected 200, got %d", status)
		}
	})
}

// TestE2E_UpdateCAS tests that PUT /{entity}/{id} also returns current_version on 409.
func TestE2E_UpdateCAS(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	id := env.create("/api/v1/assets", map[string]interface{}{
		"code": "UPCAS-001", "name": "Update CAS Asset", "status": "active",
	})
	_, entity := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d", id), nil)
	version := entity["_version"]

	t.Run("PUT with correct version succeeds", func(t *testing.T) {
		status, _ := env.doJSON("PUT", fmt.Sprintf("/api/v1/assets/%d", id), map[string]interface{}{
			"code": "UPCAS-001", "name": "Updated", "status": "active",
			"_version": version,
		})
		if status != http.StatusOK {
			t.Errorf("PUT with correct version expected 200, got %d", status)
		}
	})

	t.Run("PUT with stale version returns 409 with current_version", func(t *testing.T) {
		status, body := env.doJSON("PUT", fmt.Sprintf("/api/v1/assets/%d", id), map[string]interface{}{
			"code": "UPCAS-001", "name": "Stale", "status": "active",
			"_version": version, // now stale
		})
		if status != http.StatusConflict {
			t.Errorf("stale PUT expected 409, got %d", status)
		}
		if body["current_version"] == nil {
			t.Error("409 body must contain current_version")
		}
	})
}

// =============================================================================
// 2. GRAPH ENDPOINT COVERAGE
//    Fill gaps: node info, degree, in/out edges, shortest path, path exists,
//    common neighbors, node search, Sulpher async lifecycle.
// =============================================================================

// TestE2E_GraphNodeInfo tests GET /api/v1/graph/nodes/{node_id}
func TestE2E_GraphNodeInfo(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Create entities to populate the graph
	env.create("/api/v1/assets", map[string]interface{}{
		"code": "GNI-001", "name": "Test Asset", "status": "active",
	})

	t.Run("existing node returns info", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/graph/nodes/assets:1", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		// NodeInfo returns "id" field (from json:"id" tag)
		if result["id"] != "assets:1" {
			t.Errorf("expected id=assets:1, got %v", result["id"])
		}
	})

	t.Run("nonexistent node returns 404", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/graph/nodes/nonexistent:999", nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}
	})
}

// TestE2E_GraphNodeDegree tests GET /api/v1/graph/nodes/{node_id}/degree
func TestE2E_GraphNodeDegree(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Asset type → referenced by 2 assets
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Valve", "code": "VLV",
	})
	env.create("/api/v1/assets", map[string]interface{}{
		"code": "VLV-001", "asset_type": ref("asset_types", atID),
	})
	env.create("/api/v1/assets", map[string]interface{}{
		"code": "VLV-002", "asset_type": ref("asset_types", atID),
	})

	t.Run("degree reflects edges", func(t *testing.T) {
		nodeID := fmt.Sprintf("asset_types:%d", atID)
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/nodes/%s/degree", nodeID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		degree, ok := result["degree"].(map[string]interface{})
		if !ok {
			t.Fatalf("expected degree object, got %T: %v", result["degree"], result)
		}
		// Asset type should have incoming edges from the 2 assets
		inDeg := toF64(degree["in"])
		if inDeg < 2 {
			t.Errorf("expected in-degree >= 2, got %v", inDeg)
		}
	})

	t.Run("nonexistent node returns 404", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/graph/nodes/fake:999/degree", nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}
	})
}

// TestE2E_GraphIncomingOutgoing tests GET /api/v1/graph/{node_id}/in and /out
func TestE2E_GraphIncomingOutgoing(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Motor", "code": "MOT",
	})
	a1ID := env.create("/api/v1/assets", map[string]interface{}{
		"code": "MOT-001", "asset_type": ref("asset_types", atID),
	})
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code": "TEMP-001", "type": "temperature",
		"asset": ref("assets", a1ID),
	})
	_ = sID

	atNodeID := fmt.Sprintf("asset_types:%d", atID)
	assetNodeID := fmt.Sprintf("assets:%d", a1ID)

	t.Run("incoming edges to asset type", func(t *testing.T) {
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/in", atNodeID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		edges, ok := result["edges"].([]interface{})
		if !ok {
			t.Fatalf("expected edges array, got %T", result["edges"])
		}
		if len(edges) < 1 {
			t.Errorf("expected at least 1 incoming edge, got %d", len(edges))
		}
		count := int(toF64(result["count"]))
		if count != len(edges) {
			t.Errorf("count mismatch: count=%d, edges=%d", count, len(edges))
		}
	})

	t.Run("outgoing edges from asset", func(t *testing.T) {
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/out", assetNodeID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		edges, ok := result["edges"].([]interface{})
		if !ok {
			t.Fatalf("expected edges array, got %T", result["edges"])
		}
		// Asset has outgoing edge to asset_type
		if len(edges) < 1 {
			t.Errorf("expected at least 1 outgoing edge from asset, got %d", len(edges))
		}
	})

	t.Run("incoming edges to asset from sensor", func(t *testing.T) {
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/in", assetNodeID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		// Sensor references this asset, so there should be an incoming edge
		edges := result["edges"].([]interface{})
		if len(edges) < 1 {
			t.Errorf("expected at least 1 incoming edge to asset from sensor, got %d", len(edges))
		}
	})

	t.Run("node with no edges returns empty array", func(t *testing.T) {
		// Create an isolated entity
		env.create("/api/v1/users", map[string]interface{}{
			"username": "loner", "email": "loner@test.com",
		})
		status, result := env.doJSON("GET", "/api/v1/graph/users:1/out", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		edges := result["edges"].([]interface{})
		if len(edges) != 0 {
			t.Errorf("expected 0 outgoing edges for isolated node, got %d", len(edges))
		}
	})
}

// TestE2E_GraphShortestPath tests POST /api/v1/graph/shortestPath
func TestE2E_GraphShortestPath(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Build a chain: asset_type ← asset ← sensor ← event
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Chain", "code": "CHN",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code": "CHN-001", "asset_type": ref("asset_types", atID),
	})
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code": "CHN-S1", "asset": ref("assets", aID),
	})
	env.create("/api/v1/events", map[string]interface{}{
		"event_type": "reading",
		"sensor":     ref("sensors", sID),
		"asset":      ref("assets", aID),
	})

	t.Run("path exists between connected nodes", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from":      fmt.Sprintf("events:%d", 1),
			"to":        fmt.Sprintf("asset_types:%d", atID),
			"max_depth": 5,
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		if result["exists"] != true {
			t.Error("expected path to exist")
		}
		path, ok := result["path"].([]interface{})
		if !ok || len(path) < 2 {
			t.Errorf("expected path with at least 2 nodes, got %v", result["path"])
		}
		length := toF64(result["length"])
		if length < 1 {
			t.Errorf("expected length >= 1, got %v", length)
		}
	})

	t.Run("no path between disconnected nodes", func(t *testing.T) {
		// Create an isolated node
		env.create("/api/v1/users", map[string]interface{}{
			"username": "island",
		})
		status, result := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from":      "users:1",
			"to":        fmt.Sprintf("asset_types:%d", atID),
			"max_depth": 5,
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		if result["exists"] != false {
			t.Error("expected no path between disconnected nodes")
		}
	})

	t.Run("missing fields returns 400", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": "assets:1",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", status)
		}
	})
}

// TestE2E_GraphPathExists tests POST /api/v1/graph/pathExists
func TestE2E_GraphPathExists(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Sensor Hub", "code": "SH",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code": "SH-001", "asset_type": ref("asset_types", atID),
	})

	t.Run("path exists returns true with length", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/pathExists", map[string]interface{}{
			"from": fmt.Sprintf("assets:%d", aID),
			"to":   fmt.Sprintf("asset_types:%d", atID),
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		if result["exists"] != true {
			t.Error("expected path to exist")
		}
		length := toF64(result["length"])
		if length != 1 {
			t.Errorf("expected length=1, got %v", length)
		}
	})

	t.Run("no path returns false", func(t *testing.T) {
		env.create("/api/v1/users", map[string]interface{}{"username": "solo"})
		status, result := env.doJSON("POST", "/api/v1/graph/pathExists", map[string]interface{}{
			"from": "users:1",
			"to":   fmt.Sprintf("asset_types:%d", atID),
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		if result["exists"] != false {
			t.Error("expected no path")
		}
	})
}

// TestE2E_GraphSharedOutNeighbors tests POST /api/v1/graph/commonNeighbors
func TestE2E_GraphSharedOutNeighbors(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Two assets sharing the same asset type
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Shared", "code": "SHR",
	})
	a1ID := env.create("/api/v1/assets", map[string]interface{}{
		"code": "SHR-001", "asset_type": ref("asset_types", atID),
	})
	a2ID := env.create("/api/v1/assets", map[string]interface{}{
		"code": "SHR-002", "asset_type": ref("asset_types", atID),
	})

	t.Run("finds shared neighbor", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{
			"node_a": fmt.Sprintf("assets:%d", a1ID),
			"node_b": fmt.Sprintf("assets:%d", a2ID),
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		common, ok := result["common"].([]interface{})
		if !ok {
			t.Fatalf("expected common array, got %T", result["common"])
		}
		// Both point to the same asset_type
		found := false
		target := fmt.Sprintf("asset_types:%d", atID)
		for _, n := range common {
			if n.(string) == target {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %s in common neighbors, got %v", target, common)
		}
		count := int(toF64(result["count"]))
		if count != len(common) {
			t.Errorf("count mismatch: count=%d, common=%d", count, len(common))
		}
	})

	t.Run("no common neighbors", func(t *testing.T) {
		env.create("/api/v1/users", map[string]interface{}{"username": "alice"})
		env.create("/api/v1/users", map[string]interface{}{"username": "bob"})
		status, result := env.doJSON("POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{
			"node_a": "users:1",
			"node_b": "users:2",
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		common := result["common"].([]interface{})
		if len(common) != 0 {
			t.Errorf("expected 0 common neighbors, got %d", len(common))
		}
	})

	t.Run("missing fields returns 400", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{
			"node_a": "assets:1",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", status)
		}
	})
}

// TestE2E_GraphNodeSearch tests POST /api/v1/graph/nodes/search
func TestE2E_GraphNodeSearch(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Create a mix of entity types
	for i := 0; i < 3; i++ {
		env.create("/api/v1/assets", map[string]interface{}{
			"code": fmt.Sprintf("NS-%03d", i+1), "status": "active",
		})
	}
	for i := 0; i < 2; i++ {
		env.create("/api/v1/sensors", map[string]interface{}{
			"code": fmt.Sprintf("SNS-%03d", i+1),
		})
	}

	t.Run("search by entity type", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "assets",
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		nodes := result["nodes"].([]interface{})
		if len(nodes) != 3 {
			t.Errorf("expected 3 asset nodes, got %d", len(nodes))
		}
		count := int(toF64(result["count"]))
		if count != 3 {
			t.Errorf("count expected 3, got %d", count)
		}
	})

	t.Run("search all nodes", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		nodes := result["nodes"].([]interface{})
		// 3 assets + 2 sensors = 5
		if len(nodes) < 5 {
			t.Errorf("expected at least 5 nodes, got %d", len(nodes))
		}
	})

	t.Run("search with limit", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "assets",
			"limit":  2,
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		nodes := result["nodes"].([]interface{})
		if len(nodes) != 2 {
			t.Errorf("expected 2 nodes with limit, got %d", len(nodes))
		}
	})
}

// TestE2E_SulpherAsyncLifecycle tests the full submit → poll → result cycle
// for Sulpher graph queries.
func TestE2E_SulpherAsyncLifecycle(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Build a small graph
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Async Test", "code": "AST",
	})
	env.create("/api/v1/assets", map[string]interface{}{
		"code": "AST-001", "asset_type": ref("asset_types", atID),
	})

	t.Run("full async lifecycle", func(t *testing.T) {
		// Submit
		status, result := env.doJSON("POST", "/api/v1/graph/query/async", map[string]interface{}{
			"query": "MATCH (a:assets)-[r]->(at:asset_types) RETURN a, r, at",
		})
		if status != http.StatusAccepted {
			t.Fatalf("submit expected 202, got %d: %v", status, result)
		}
		queryID, ok := result["query_id"].(string)
		if !ok || queryID == "" {
			t.Fatalf("expected query_id, got %v", result)
		}

		// Poll until complete (with timeout)
		deadline := time.Now().Add(5 * time.Second)
		var jobStatus string
		for time.Now().Before(deadline) {
			status, result = env.doJSON("GET", fmt.Sprintf("/api/v1/graph/query/%s", queryID), nil)
			if status != http.StatusOK {
				t.Fatalf("poll expected 200, got %d", status)
			}
			jobStatus = result["status"].(string)
			if jobStatus == "completed" || jobStatus == "failed" {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		if jobStatus != "completed" {
			t.Fatalf("expected completed, got %s: %v", jobStatus, result)
		}

		// Fetch result
		status, result = env.doJSON("GET", fmt.Sprintf("/api/v1/graph/query/%s/result", queryID), nil)
		if status != http.StatusOK {
			t.Fatalf("result expected 200, got %d: %v", status, result)
		}
		if result["status"] != "completed" {
			t.Errorf("result status expected completed, got %v", result["status"])
		}
		if result["stats"] == nil {
			t.Error("expected stats in result")
		}
	})

	t.Run("nonexistent query returns 404", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/graph/query/nonexistent-id", nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}

		status, _ = env.doJSON("GET", "/api/v1/graph/query/nonexistent-id/result", nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404 for result, got %d", status)
		}
	})

	t.Run("empty query returns 400", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/query/async", map[string]interface{}{
			"query": "",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", status)
		}
	})
}

// =============================================================================
// 3. ERROR / EDGE-CASE HARDENING
// =============================================================================

// TestE2E_MalformedInputs tests that bad requests are handled gracefully.
func TestE2E_MalformedInputs(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("invalid JSON body", func(t *testing.T) {
		req, _ := http.NewRequest("POST", env.ts.URL+"/api/v1/assets",
			strings.NewReader("{invalid json"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid JSON, got %d", resp.StatusCode)
		}
	})

	t.Run("empty body on POST", func(t *testing.T) {
		req, _ := http.NewRequest("POST", env.ts.URL+"/api/v1/assets",
			strings.NewReader(""))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for empty body, got %d", resp.StatusCode)
		}
	})

	t.Run("GET nonexistent entity type", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/nonexistent/1", nil)
		// Should be 404 or similar — not 500
		if status == http.StatusInternalServerError {
			t.Error("expected graceful handling of unknown entity, got 500")
		}
	})

	t.Run("GET nonexistent ID", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/assets/999999", nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}
	})

	t.Run("DELETE nonexistent ID", func(t *testing.T) {
		status, _ := env.doJSON("DELETE", "/api/v1/assets/999999", nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}
	})

	t.Run("PATCH nonexistent ID", func(t *testing.T) {
		status, _ := env.doJSON("PATCH", "/api/v1/assets/999999", map[string]interface{}{
			"status": "broken",
		})
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}
	})

	t.Run("PUT nonexistent ID", func(t *testing.T) {
		status, _ := env.doJSON("PUT", "/api/v1/assets/999999", map[string]interface{}{
			"code": "GHOST", "status": "active",
		})
		if status != http.StatusNotFound {
			t.Errorf("expected 404, got %d", status)
		}
	})

	t.Run("invalid OQL query", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "SELECT * FROM",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400 for malformed OQL, got %d: %v", status, result)
		}
	})

	t.Run("OQL against nonexistent entity", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "SELECT * FROM unicorns",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400 for unknown entity in OQL, got %d", status)
		}
	})

	t.Run("empty OQL query", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400 for empty OQL, got %d", status)
		}
	})

	t.Run("graph endpoint with missing required fields", func(t *testing.T) {
		// shortestPath without 'to'
		status, _ := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": "assets:1",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", status)
		}

		// commonNeighbors without node_b
		status, _ = env.doJSON("POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{
			"node_a": "assets:1",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", status)
		}

		// pathExists without to
		status, _ = env.doJSON("POST", "/api/v1/graph/pathExists", map[string]interface{}{
			"from": "assets:1",
		})
		if status != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", status)
		}
	})

	t.Run("invalid JSON on graph endpoints", func(t *testing.T) {
		endpoints := []string{
			"/api/v1/graph/shortestPath",
			"/api/v1/graph/pathExists",
			"/api/v1/graph/commonNeighbors",
			"/api/v1/graph/nodes/search",
			"/api/v1/graph/query",
			"/api/v1/graph/query/async",
			"/api/v1/oql/query",
			"/api/v1/oql/query/async",
		}
		for _, ep := range endpoints {
			req, _ := http.NewRequest("POST", env.ts.URL+ep,
				strings.NewReader("{bad json"))
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("%s: expected 400 for invalid JSON, got %d", ep, resp.StatusCode)
			}
		}
	})
}

// TestE2E_BoundaryConditions tests edge cases around limits and empty states.
func TestE2E_BoundaryConditions(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("list on empty entity returns empty array", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/gateways", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200 for empty list, got %d", status)
		}
		data, ok := result["data"].([]interface{})
		if !ok {
			// Might be nil for truly empty — acceptable
			if result["data"] != nil {
				t.Errorf("expected nil or empty array, got %T: %v", result["data"], result["data"])
			}
		} else if len(data) != 0 {
			t.Errorf("expected 0 items, got %d", len(data))
		}
	})

	t.Run("graph stats on empty graph", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/graph/stats", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		if toF64(result["node_count"]) != 0 {
			t.Errorf("expected 0 nodes on fresh graph, got %v", result["node_count"])
		}
	})

	t.Run("node search on empty graph", func(t *testing.T) {
		status, result := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "assets",
		})
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		nodes, _ := result["nodes"].([]interface{})
		if len(nodes) != 0 {
			t.Errorf("expected 0 nodes, got %d", len(nodes))
		}
	})

	t.Run("create and immediately GET", func(t *testing.T) {
		id := env.create("/api/v1/assets", map[string]interface{}{
			"code": "IMMED-001", "status": "active",
		})
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d", id), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		if result["code"] != "IMMED-001" {
			t.Errorf("expected code=IMMED-001, got %v", result["code"])
		}
	})

	t.Run("PUT replaces all fields", func(t *testing.T) {
		id := env.create("/api/v1/assets", map[string]interface{}{
			"code": "REPL-001", "status": "active", "notes": "original",
		})
		status, _ := env.doJSON("PUT", fmt.Sprintf("/api/v1/assets/%d", id), map[string]interface{}{
			"code": "REPL-001", "status": "replaced",
		})
		if status != http.StatusOK {
			t.Fatalf("PUT expected 200, got %d", status)
		}
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d", id), nil)
		if status != http.StatusOK {
			t.Fatalf("GET after PUT expected 200, got %d", status)
		}
		if result["status"] != "replaced" {
			t.Errorf("expected status=replaced, got %v", result["status"])
		}
		// The "notes" field should be gone after PUT (full replacement)
		if result["notes"] != nil {
			t.Errorf("expected notes to be gone after PUT, got %v", result["notes"])
		}
	})

	t.Run("PATCH preserves unmentioned fields", func(t *testing.T) {
		id := env.create("/api/v1/assets", map[string]interface{}{
			"code": "PRES-001", "status": "active", "notes": "keep me",
		})
		status, _ := env.doJSON("PATCH", fmt.Sprintf("/api/v1/assets/%d", id), map[string]interface{}{
			"status": "updated",
		})
		if status != http.StatusOK {
			t.Fatalf("PATCH expected 200, got %d", status)
		}
		status, result := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d", id), nil)
		if status != http.StatusOK {
			t.Fatalf("GET after PATCH expected 200, got %d", status)
		}
		if result["status"] != "updated" {
			t.Errorf("expected status=updated, got %v", result["status"])
		}
		if result["notes"] != "keep me" {
			t.Errorf("expected notes preserved, got %v", result["notes"])
		}
	})

	t.Run("version and health endpoints", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/health", nil)
		if status != http.StatusOK {
			t.Errorf("health expected 200, got %d", status)
		}
		status, _ = env.doJSON("GET", "/version", nil)
		if status != http.StatusOK {
			t.Errorf("version expected 200, got %d", status)
		}
	})
}

// TestE2E_ExportEndpoint tests GET /api/v1/export.
func TestE2E_ExportEndpoint(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Populate some data
	env.create("/api/v1/assets", map[string]interface{}{"code": "EXP-001", "status": "active"})
	env.create("/api/v1/assets", map[string]interface{}{"code": "EXP-002", "status": "active"})
	env.create("/api/v1/sensors", map[string]interface{}{"code": "EXP-S1"})

	t.Run("export returns valid ZIP", func(t *testing.T) {
		status, raw := env.do("GET", "/api/v1/export", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		if len(raw) == 0 {
			t.Fatal("expected non-empty export response")
		}
		// ZIP files start with PK signature (0x50, 0x4B)
		if len(raw) < 2 || raw[0] != 0x50 || raw[1] != 0x4B {
			t.Fatalf("expected ZIP file (PK signature), got first bytes: %x", raw[:min(4, len(raw))])
		}
	})

	t.Run("export contains manifest", func(t *testing.T) {
		status, raw := env.do("GET", "/api/v1/export", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("failed to open ZIP: %v", err)
		}

		var manifest map[string]interface{}
		for _, f := range zr.File {
			if f.Name == "manifest.json" {
				rc, err := f.Open()
				if err != nil {
					t.Fatalf("failed to open manifest.json: %v", err)
				}
				data, _ := io.ReadAll(rc)
				rc.Close()
				if err := json.Unmarshal(data, &manifest); err != nil {
					t.Fatalf("manifest is not valid JSON: %v", err)
				}
				break
			}
		}
		if manifest == nil {
			t.Fatal("ZIP does not contain manifest.json")
		}
		if _, ok := manifest["version"]; !ok {
			t.Error("manifest missing 'version' field")
		}
		if _, ok := manifest["exported_at"]; !ok {
			t.Error("manifest missing 'exported_at' field")
		}
		if manifest["storage_type"] != "jsonfile" {
			t.Errorf("manifest storage_type = %v, want %q", manifest["storage_type"], "jsonfile")
		}
	})

	t.Run("export contains entity data files", func(t *testing.T) {
		status, raw := env.do("GET", "/api/v1/export", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("failed to open ZIP: %v", err)
		}

		fileNames := make(map[string]bool)
		for _, f := range zr.File {
			fileNames[f.Name] = true
		}

		// The jsonfile backend stores entities in data/{entity}/{id}.json
		// We created 2 assets and 1 sensor, so we expect those paths
		if !fileNames["manifest.json"] {
			t.Error("missing manifest.json")
		}

		// Check that at least some data files exist
		hasDataFiles := false
		for name := range fileNames {
			if strings.HasPrefix(name, "data/") && strings.HasSuffix(name, ".json") {
				hasDataFiles = true
				break
			}
		}
		if !hasDataFiles {
			t.Errorf("no data/*.json files found in export; files present: %v", fileNames)
		}
	})

	t.Run("exported entity data is valid JSON matching created records", func(t *testing.T) {
		status, raw := env.do("GET", "/api/v1/export", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			t.Fatalf("failed to open ZIP: %v", err)
		}

		// Collect all entity data from the ZIP
		exportedCodes := map[string]bool{}
		for _, f := range zr.File {
			if !strings.HasPrefix(f.Name, "data/") || !strings.HasSuffix(f.Name, ".json") {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				t.Errorf("failed to open %s: %v", f.Name, err)
				continue
			}
			data, _ := io.ReadAll(rc)
			rc.Close()

			var entity map[string]interface{}
			if err := json.Unmarshal(data, &entity); err != nil {
				t.Errorf("%s: not valid JSON: %v", f.Name, err)
				continue
			}

			if code, ok := entity["code"].(string); ok {
				exportedCodes[code] = true
			}
		}

		// Verify the codes we created are present in the export
		expectedCodes := []string{"EXP-001", "EXP-002", "EXP-S1"}
		for _, code := range expectedCodes {
			if !exportedCodes[code] {
				t.Errorf("expected code %q in export, but not found; exported codes: %v",
					code, exportedCodes)
			}
		}
	})
}

// TestE2E_ConcurrentCreates verifies no data corruption under parallel writes.
func TestE2E_ConcurrentCreates(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	const workers = 5
	const perWorker = 10
	errs := make(chan error, workers*perWorker)

	for w := 0; w < workers; w++ {
		go func(workerID int) {
			for i := 0; i < perWorker; i++ {
				status, result := env.doJSON("POST", "/api/v1/assets", map[string]interface{}{
					"code":   fmt.Sprintf("W%d-%03d", workerID, i),
					"status": "active",
				})
				if status != http.StatusCreated {
					errs <- fmt.Errorf("worker %d, item %d: expected 201, got %d: %v",
						workerID, i, status, result)
				} else {
					errs <- nil
				}
			}
		}(w)
	}

	var failures []string
	for i := 0; i < workers*perWorker; i++ {
		if err := <-errs; err != nil {
			failures = append(failures, err.Error())
		}
	}
	if len(failures) > 0 {
		t.Errorf("%d/%d concurrent creates failed:\n%s",
			len(failures), workers*perWorker, strings.Join(failures[:min(5, len(failures))], "\n"))
	}

	// Verify total count
	data := env.oqlData("SELECT COUNT(*) as cnt FROM assets")
	cnt := toF64(data[0].(map[string]interface{})["cnt"])
	if int(cnt) != workers*perWorker {
		t.Errorf("expected %d assets, got %v", workers*perWorker, cnt)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
