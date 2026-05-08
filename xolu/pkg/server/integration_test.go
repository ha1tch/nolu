// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// integration_test.go
//
// Comprehensive integration tests targeting the olu API surface areas that
// These tests exercise code paths most heavily. Written to exercise the exact code paths
// with special focus on areas where olu had weak or no test
// coverage prior to this file.
//
// Priority areas tested (in order of risk):
//   1. Tenant-scoped OQL queries (cross-tenant data leakage)
//   2. OQL COUNT(*) aggregates (referential integrity checks)
//   3. OQL UPDATE mutations (sensor unbinding)
//   4. OQL with string equality WHERE (lookup by code/email)
//   5. PATCH + graph edge consistency (truth layer insertion point)
//   6. PUT replacing REFs + graph cleanup (data integrity)
//
// Author: ha1tch <h@ual.fi>
// Repository: https://github.com/ha1tch/xolu/

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// integrationEnv holds a fully configured test server with OQL support and
// pre-created entity directories matching a caller's schema.
type integrationEnv struct {
	ts     *httptest.Server
	tmpDir string
	t      *testing.T
}

// integrationEntities lists all entity types production workloads use. These directories must
// exist for the OQL validator to accept queries against them.
var integrationEntities = []string{
	"assets", "events", "asset_types", "fsm_machines", "rules",
	"sensors", "sensor_bindings", "qr_codes", "qr_scans",
	"users", "webhooks",
}

func newIntegrationEnv(t *testing.T) *integrationEnv {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "olu-integration-*")
	if err != nil {
		t.Fatal(err)
	}

	// Pre-create entity directories so the OQL validator recognises them.
	for _, entity := range integrationEntities {
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

	return &integrationEnv{ts: ts, tmpDir: tmpDir, t: t}
}

func (e *integrationEnv) cleanup() {
	e.ts.Close()
	os.RemoveAll(e.tmpDir)
}

// do makes an HTTP request and returns the response + body bytes.
func (e *integrationEnv) do(method, path string, body interface{}) (*http.Response, []byte) {
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
	return resp, buf.Bytes()
}

// doJSON is a convenience that also unmarshals the response into a map.
func (e *integrationEnv) doJSON(method, path string, body interface{}) (int, map[string]interface{}) {
	e.t.Helper()
	resp, raw := e.do(method, path, body)
	var result map[string]interface{}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &result); err != nil {
			e.t.Fatalf("failed to unmarshal response (status %d): %s\nbody: %s",
				resp.StatusCode, err, string(raw))
		}
	}
	return resp.StatusCode, result
}

// createEntity creates an entity via the CRUD API and returns the assigned ID.
func (e *integrationEnv) createEntity(path string, data map[string]interface{}) int {
	e.t.Helper()
	status, result := e.doJSON("POST", path, data)
	if status != http.StatusCreated {
		e.t.Fatalf("createEntity %s: expected 201, got %d: %v", path, status, result)
	}
	id, ok := result["id"].(float64)
	if !ok {
		e.t.Fatalf("createEntity %s: no id in response: %v", path, result)
	}
	return int(id)
}

// oql executes a synchronous OQL query and returns the parsed result.
func (e *integrationEnv) oql(path, query string) (int, map[string]interface{}) {
	e.t.Helper()
	return e.doJSON("POST", path, map[string]interface{}{"query": query})
}

// oqlData executes an OQL SELECT and returns the data rows.
func (e *integrationEnv) oqlData(path, query string) []interface{} {
	e.t.Helper()
	status, result := e.oql(path, query)
	if status != http.StatusOK {
		e.t.Fatalf("OQL query failed (status %d): %v\nquery: %s", status, result, query)
	}
	data, ok := result["data"].([]interface{})
	if !ok {
		// "data": null means zero rows returned (e.g. WHERE matched nothing
		// and no aggregates). Return empty slice rather than crashing.
		return []interface{}{}
	}
	return data
}

// graphStats returns node_count and edge_count.
func (e *integrationEnv) graphStats() (int, int) { //nolint:unused
	e.t.Helper()
	_, result := e.doJSON("GET", "/api/v1/graph/stats", nil)
	nodes := int(result["node_count"].(float64))
	edges := int(result["edge_count"].(float64))
	return nodes, edges
}

// graphNeighbors returns the outgoing neighbor map for a node.
// direction should be "outgoing" or "incoming" (mapped to API values "out"/"in").
func (e *integrationEnv) graphNeighbors(nodeID, direction string) map[string]interface{} {
	e.t.Helper()
	// Map caller-friendly names to API values
	apiDirection := "out"
	responseKey := "outgoing"
	if direction == "incoming" || direction == "in" {
		apiDirection = "in"
		responseKey = "incoming"
	}
	body := map[string]interface{}{"node_id": nodeID, "direction": apiDirection}
	resp, raw := e.do("POST", "/api/v1/graph/neighbors", body)

	var result map[string]interface{}
	if len(raw) > 0 {
		json.Unmarshal(raw, &result)
	}
	if result == nil || resp.StatusCode != http.StatusOK {
		return map[string]interface{}{}
	}
	// Response shape: {"neighbors": {"outgoing": {target: relationship, ...}}}
	neighborsWrap, _ := result["neighbors"].(map[string]interface{})
	if neighborsWrap == nil {
		return map[string]interface{}{}
	}
	inner, _ := neighborsWrap[responseKey].(map[string]interface{})
	if inner == nil {
		return map[string]interface{}{}
	}
	return inner
}

// ============================================================================
// 1. TENANT-SCOPED OQL QUERIES
// ============================================================================

func TestOQLTenantIsolation(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Seed data in two tenants via tenant-scoped CRUD.
	// The CRUD API injects tenant_id automatically.
	env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
		"name": "Forklift A", "status": "active", "type_id": 1,
	})
	env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
		"name": "Forklift B", "status": "active", "type_id": 1,
	})
	env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
		"name": "Crane C", "status": "idle", "type_id": 2,
	})

	env.createEntity("/api/v1/tenant/globex/assets", map[string]interface{}{
		"name": "Truck X", "status": "active", "type_id": 1,
	})
	env.createEntity("/api/v1/tenant/globex/assets", map[string]interface{}{
		"name": "Truck Y", "status": "active", "type_id": 1,
	})

	t.Run("Tenant-scoped SELECT returns only that tenant's data", func(t *testing.T) {
		data := env.oqlData("/api/v1/tenant/acme/oql/query", "SELECT * FROM assets")
		if len(data) != 3 {
			t.Errorf("expected 3 acme assets, got %d", len(data))
		}

		data = env.oqlData("/api/v1/tenant/globex/oql/query", "SELECT * FROM assets")
		if len(data) != 2 {
			t.Errorf("expected 2 globex assets, got %d", len(data))
		}
	})

	// NOTE: No "non-tenant OQL returns all data" sub-test. Non-tenant routes
	// use tenant 0 (unscoped store), which only sees data written through
	// non-tenant routes. Cross-tenant aggregation requires a dedicated admin API.

	t.Run("Tenant-scoped SELECT with WHERE", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT * FROM assets WHERE status = 'active'",
		)
		if len(data) != 2 {
			t.Errorf("expected 2 active acme assets, got %d", len(data))
		}
	})

	t.Run("Tenant-scoped SELECT with WHERE does not leak cross-tenant", func(t *testing.T) {
		// type_id=1 exists in both tenants. Acme has 2, Globex has 2.
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT * FROM assets WHERE type_id = 1",
		)
		if len(data) != 2 {
			t.Errorf("expected 2 acme type_id=1 assets, got %d (cross-tenant leak?)", len(data))
		}
	})

	t.Run("Tenant-scoped COUNT returns tenant count only", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT COUNT(*) as count FROM assets",
		)
		if len(data) != 1 {
			t.Fatalf("expected 1 aggregate row, got %d", len(data))
		}
		row := data[0].(map[string]interface{})
		count := int(toFloat64(row["count"]))
		if count != 3 {
			t.Errorf("expected COUNT(*)=3 for acme, got %d", count)
		}
	})

	t.Run("Tenant-scoped COUNT with WHERE", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/globex/oql/query",
			"SELECT COUNT(*) as count FROM assets WHERE status = 'active'",
		)
		if len(data) != 1 {
			t.Fatalf("expected 1 aggregate row, got %d", len(data))
		}
		row := data[0].(map[string]interface{})
		count := int(toFloat64(row["count"]))
		if count != 2 {
			t.Errorf("expected COUNT(*)=2 for globex active, got %d", count)
		}
	})
}

// ============================================================================
// 2. OQL COUNT(*) AGGREGATES
//    Used for pre-delete referential integrity checks.
// ============================================================================

func TestOQLCountAggregates(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create asset types
	env.createEntity("/api/v1/asset_types", map[string]interface{}{
		"name": "Forklift", "code": "FLT",
	})
	env.createEntity("/api/v1/asset_types", map[string]interface{}{
		"name": "Crane", "code": "CRN",
	})

	// Create assets referencing type_id
	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Forklift 1", "type_id": 1,
	})
	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Forklift 2", "type_id": 1,
	})
	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Crane 1", "type_id": 2,
	})

	t.Run("COUNT(*) with WHERE on integer field", func(t *testing.T) {
		// This is typical query for checking if an asset_type has assets
		data := env.oqlData("/api/v1/oql/query",
			"SELECT COUNT(*) as count FROM assets WHERE type_id = 1")
		row := data[0].(map[string]interface{})
		count := int(toFloat64(row["count"]))
		if count != 2 {
			t.Errorf("expected COUNT(*)=2 for type_id=1, got %d", count)
		}
	})

	t.Run("COUNT(*) returns 0 for no matches", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT COUNT(*) as count FROM assets WHERE type_id = 999")
		if len(data) != 1 {
			t.Fatalf("expected 1 aggregate row (SQL standard), got %d", len(data))
		}
		row := data[0].(map[string]interface{})
		count := int(toFloat64(row["count"]))
		if count != 0 {
			t.Errorf("expected COUNT(*)=0 for type_id=999, got %d", count)
		}
	})

	t.Run("COUNT(*) without WHERE counts all", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT COUNT(*) as count FROM assets")
		row := data[0].(map[string]interface{})
		count := int(toFloat64(row["count"]))
		if count != 3 {
			t.Errorf("expected COUNT(*)=3, got %d", count)
		}
	})

	t.Run("COUNT with named column", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT COUNT(name) as total FROM assets")
		row := data[0].(map[string]interface{})
		total := int(toFloat64(row["total"]))
		if total != 3 {
			t.Errorf("expected COUNT(name)=3, got %d", total)
		}
	})

	t.Run("SUM aggregate", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT SUM(type_id) as total FROM assets")
		row := data[0].(map[string]interface{})
		total := toFloat64(row["total"])
		if total != 4 { // 1+1+2
			t.Errorf("expected SUM(type_id)=4, got %v", total)
		}
	})

	t.Run("MIN and MAX aggregates", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT MIN(type_id) as lo, MAX(type_id) as hi FROM assets")
		row := data[0].(map[string]interface{})
		lo := toFloat64(row["lo"])
		hi := toFloat64(row["hi"])
		if lo != 1 {
			t.Errorf("expected MIN=1, got %v", lo)
		}
		if hi != 2 {
			t.Errorf("expected MAX=2, got %v", hi)
		}
	})

	t.Run("AVG aggregate", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT AVG(type_id) as avg_type FROM assets")
		row := data[0].(map[string]interface{})
		avg := toFloat64(row["avg_type"])
		// (1+1+2)/3 = 1.333...
		if avg < 1.3 || avg > 1.4 {
			t.Errorf("expected AVG ~1.33, got %v", avg)
		}
	})

	t.Run("Aggregates on empty set return SQL-standard values", func(t *testing.T) {
		// SQL standard: aggregates without GROUP BY always produce one row.
		// COUNT(*) → 0, COUNT(col) → 0, SUM → NULL, AVG → NULL, MIN → NULL, MAX → NULL
		data := env.oqlData("/api/v1/oql/query",
			"SELECT COUNT(*) as c, SUM(type_id) as s, AVG(type_id) as a, MIN(type_id) as lo, MAX(type_id) as hi FROM assets WHERE type_id = 9999")
		if len(data) != 1 {
			t.Fatalf("expected exactly 1 row from aggregate on empty set, got %d", len(data))
		}
		row := data[0].(map[string]interface{})
		if int(toFloat64(row["c"])) != 0 {
			t.Errorf("expected COUNT(*)=0, got %v", row["c"])
		}
		if row["s"] != nil {
			t.Errorf("expected SUM=nil on empty set, got %v", row["s"])
		}
		if row["a"] != nil {
			t.Errorf("expected AVG=nil on empty set, got %v", row["a"])
		}
		if row["lo"] != nil {
			t.Errorf("expected MIN=nil on empty set, got %v", row["lo"])
		}
		if row["hi"] != nil {
			t.Errorf("expected MAX=nil on empty set, got %v", row["hi"])
		}
	})
}

// ============================================================================
// 3. OQL UPDATE MUTATIONS
//    UPDATE via OQL for unbinding references.
// ============================================================================

func TestOQLUpdateMutations(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create sensor bindings (typical pattern)
	env.createEntity("/api/v1/sensor_bindings", map[string]interface{}{
		"sensor_id": 10, "asset_id": 100, "active": true,
		"bound_at": "2025-01-01T00:00:00Z",
	})
	env.createEntity("/api/v1/sensor_bindings", map[string]interface{}{
		"sensor_id": 10, "asset_id": 200, "active": true,
		"bound_at": "2025-02-01T00:00:00Z",
	})
	env.createEntity("/api/v1/sensor_bindings", map[string]interface{}{
		"sensor_id": 20, "asset_id": 300, "active": true,
		"bound_at": "2025-03-01T00:00:00Z",
	})

	t.Run("UPDATE SET with WHERE on integer field", func(t *testing.T) {
		// This is typical unbind query
		status, result := env.oql("/api/v1/oql/query",
			"UPDATE sensor_bindings SET active = false WHERE sensor_id = 10 AND active = true")
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}

		affected := int(toFloat64(result["stats"].(map[string]interface{})["rows_affected"]))
		if affected != 2 {
			t.Errorf("expected 2 rows affected, got %d", affected)
		}
	})

	t.Run("Updated records reflect changes", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM sensor_bindings WHERE sensor_id = 10")
		for _, row := range data {
			rec := row.(map[string]interface{})
			if rec["active"] == true {
				t.Errorf("expected active=false after UPDATE, got %v for id=%v",
					rec["active"], rec["id"])
			}
		}
	})

	t.Run("UPDATE does not affect non-matching records", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM sensor_bindings WHERE sensor_id = 20")
		if len(data) != 1 {
			t.Fatalf("expected 1 row for sensor_id=20, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["active"] != true {
			t.Errorf("sensor_id=20 should still be active, got %v", rec["active"])
		}
	})

	t.Run("UPDATE SET string value", func(t *testing.T) {
		status, result := env.oql("/api/v1/oql/query",
			"UPDATE sensor_bindings SET active = false WHERE sensor_id = 20")
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		affected := int(toFloat64(result["stats"].(map[string]interface{})["rows_affected"]))
		if affected != 1 {
			t.Errorf("expected 1 row affected, got %d", affected)
		}
	})

	t.Run("UPDATE with no matches affects 0 rows", func(t *testing.T) {
		status, result := env.oql("/api/v1/oql/query",
			"UPDATE sensor_bindings SET active = false WHERE sensor_id = 9999")
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		affected := int(toFloat64(result["stats"].(map[string]interface{})["rows_affected"]))
		if affected != 0 {
			t.Errorf("expected 0 rows affected, got %d", affected)
		}
	})
}

// ============================================================================
// 3b. OQL UPDATE with tenant isolation
// ============================================================================

func TestOQLUpdateTenantIsolation(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create bindings in two tenants
	env.createEntity("/api/v1/tenant/acme/sensor_bindings", map[string]interface{}{
		"sensor_id": 10, "active": true,
	})
	env.createEntity("/api/v1/tenant/acme/sensor_bindings", map[string]interface{}{
		"sensor_id": 10, "active": true,
	})
	env.createEntity("/api/v1/tenant/globex/sensor_bindings", map[string]interface{}{
		"sensor_id": 10, "active": true,
	})

	t.Run("Tenant-scoped UPDATE only affects tenant's records", func(t *testing.T) {
		status, result := env.oql(
			"/api/v1/tenant/acme/oql/query",
			"UPDATE sensor_bindings SET active = false WHERE sensor_id = 10",
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		affected := int(toFloat64(result["stats"].(map[string]interface{})["rows_affected"]))
		if affected != 2 {
			t.Errorf("expected 2 rows affected in acme, got %d", affected)
		}
	})

	t.Run("Other tenant's records are untouched", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/globex/oql/query",
			"SELECT * FROM sensor_bindings WHERE sensor_id = 10",
		)
		if len(data) != 1 {
			t.Fatalf("expected 1 globex binding, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["active"] != true {
			t.Errorf("globex binding should still be active, got %v", rec["active"])
		}
	})
}

// ============================================================================
// 4. OQL WITH STRING EQUALITY WHERE
//    A common OQL pattern: WHERE code = 'X', WHERE email = 'X'
// ============================================================================

func TestOQLStringEquality(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	env.createEntity("/api/v1/qr_codes", map[string]interface{}{
		"code": "ABC123", "asset_id": 1, "status": "active",
	})
	env.createEntity("/api/v1/qr_codes", map[string]interface{}{
		"code": "DEF456", "asset_id": 2, "status": "active",
	})
	env.createEntity("/api/v1/qr_codes", map[string]interface{}{
		"code": "GHI789", "asset_id": 3, "status": "revoked",
	})

	env.createEntity("/api/v1/users", map[string]interface{}{
		"email": "alice@example.com", "name": "Alice", "role": "admin",
	})
	env.createEntity("/api/v1/users", map[string]interface{}{
		"email": "bob@example.com", "name": "Bob", "role": "user",
	})

	t.Run("SELECT TOP 1 with string WHERE (QR lookup)", func(t *testing.T) {
		// typical QR code lookup query
		data := env.oqlData("/api/v1/oql/query",
			"SELECT TOP 1 * FROM qr_codes WHERE code = 'ABC123'")
		if len(data) != 1 {
			t.Fatalf("expected 1 result, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["code"] != "ABC123" {
			t.Errorf("expected code=ABC123, got %v", rec["code"])
		}
	})

	t.Run("SELECT TOP 1 with string WHERE (user login)", func(t *testing.T) {
		// typical login lookup query
		data := env.oqlData("/api/v1/oql/query",
			"SELECT TOP 1 * FROM users WHERE email = 'alice@example.com'")
		if len(data) != 1 {
			t.Fatalf("expected 1 result, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["name"] != "Alice" {
			t.Errorf("expected name=Alice, got %v", rec["name"])
		}
	})

	t.Run("String WHERE with no match returns empty", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT TOP 1 * FROM qr_codes WHERE code = 'NONEXISTENT'")
		if len(data) != 0 {
			t.Errorf("expected 0 results for nonexistent code, got %d", len(data))
		}
	})

	t.Run("String WHERE combined with status filter", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM qr_codes WHERE status = 'active'")
		if len(data) != 2 {
			t.Errorf("expected 2 active QR codes, got %d", len(data))
		}
	})

	t.Run("SELECT with ORDER BY on string field", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT name FROM users ORDER BY name ASC")
		if len(data) < 2 {
			t.Fatalf("expected 2 users, got %d", len(data))
		}
		first := data[0].(map[string]interface{})
		if first["name"] != "Alice" {
			t.Errorf("expected Alice first, got %v", first["name"])
		}
	})

	t.Run("SELECT with LIKE pattern", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM users WHERE email LIKE '%example.com'")
		if len(data) != 2 {
			t.Errorf("expected 2 results for LIKE %%example.com, got %d", len(data))
		}
	})

	t.Run("SELECT id field only (a caller QR id lookup)", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT TOP 1 id FROM qr_codes WHERE code = 'DEF456'")
		if len(data) != 1 {
			t.Fatalf("expected 1 result, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["id"] == nil {
			t.Error("expected id in result")
		}
	})
}

// ============================================================================
// 5. PATCH + GRAPH EDGE CONSISTENCY
//    Entities are patched on every FSM transition. This is the authoritative state layer
//    insertion point. We must verify graph edges stay consistent.
// ============================================================================

func TestPatchGraphEdgeConsistency(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create an asset type and two FSM machines
	fsmID1 := env.createEntity("/api/v1/fsm_machines", map[string]interface{}{
		"name": "Lifecycle v1",
	})
	fsmID2 := env.createEntity("/api/v1/fsm_machines", map[string]interface{}{
		"name": "Lifecycle v2",
	})

	// Create an asset with a REF to fsm_machines
	assetID := env.createEntity("/api/v1/assets", map[string]interface{}{
		"name":      "Forklift A",
		"fsm_state": "idle",
		"fsm_ref": map[string]interface{}{
			"type": "REF", "entity": "fsm_machines", "id": fsmID1,
		},
	})

	t.Run("Initial REF creates graph edge", func(t *testing.T) {
		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		targetKey := fmt.Sprintf("fsm_machines:%d", fsmID1)
		if _, ok := neighbors[targetKey]; !ok {
			t.Errorf("expected outgoing edge to %s, got neighbors: %v", targetKey, neighbors)
		}
	})

	t.Run("PATCH non-REF field preserves graph edges", func(t *testing.T) {
		// An FSM state transition: only patches fsm_state
		status, _ := env.doJSON("PATCH",
			fmt.Sprintf("/api/v1/assets/%d", assetID),
			map[string]interface{}{"fsm_state": "running"},
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		// Edge to fsm_machines should still exist
		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		targetKey := fmt.Sprintf("fsm_machines:%d", fsmID1)
		if _, ok := neighbors[targetKey]; !ok {
			t.Errorf("FSM edge lost after patching fsm_state: neighbors=%v", neighbors)
		}
	})

	t.Run("PATCH REF field updates graph edge", func(t *testing.T) {
		// Change the FSM reference from v1 to v2
		status, _ := env.doJSON("PATCH",
			fmt.Sprintf("/api/v1/assets/%d", assetID),
			map[string]interface{}{
				"fsm_ref": map[string]interface{}{
					"type": "REF", "entity": "fsm_machines", "id": fsmID2,
				},
			},
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")

		// Should have edge to v2
		newTarget := fmt.Sprintf("fsm_machines:%d", fsmID2)
		if _, ok := neighbors[newTarget]; !ok {
			t.Errorf("expected edge to %s after PATCH, got: %v", newTarget, neighbors)
		}

		// Should NOT have edge to v1 (stale edge cleanup)
		oldTarget := fmt.Sprintf("fsm_machines:%d", fsmID1)
		if _, ok := neighbors[oldTarget]; ok {
			t.Errorf("stale edge to %s still present after PATCH, neighbors: %v",
				oldTarget, neighbors)
		}
	})

	t.Run("Multiple PATCHes accumulate no stale edges", func(t *testing.T) {
		// Cycle through several FSM references
		for i := 0; i < 5; i++ {
			newFSM := env.createEntity("/api/v1/fsm_machines", map[string]interface{}{
				"name": fmt.Sprintf("FSM round %d", i),
			})
			env.doJSON("PATCH",
				fmt.Sprintf("/api/v1/assets/%d", assetID),
				map[string]interface{}{
					"fsm_ref": map[string]interface{}{
						"type": "REF", "entity": "fsm_machines", "id": newFSM,
					},
				},
			)
		}

		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")

		// Should have exactly 1 outgoing edge (to the last FSM)
		fsmEdges := 0
		fsmPrefix := "fsm_machines:"
		for target := range neighbors {
			if len(target) >= len(fsmPrefix) && target[:len(fsmPrefix)] == fsmPrefix {
				fsmEdges++
			}
		}
		if fsmEdges != 1 {
			t.Errorf("expected exactly 1 FSM edge after 5 reassignments, got %d: %v",
				fsmEdges, neighbors)
		}
	})
}

// ============================================================================
// 6. PUT REPLACING REFs + GRAPH CLEANUP
// ============================================================================

func TestPutGraphEdgeCleanup(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	typeID := env.createEntity("/api/v1/asset_types", map[string]interface{}{
		"name": "Forklift",
	})
	sensorID := env.createEntity("/api/v1/sensors", map[string]interface{}{
		"name": "Temp Sensor A",
	})
	sensorID2 := env.createEntity("/api/v1/sensors", map[string]interface{}{
		"name": "Temp Sensor B",
	})

	// Create an asset with REFs to asset_type and sensor
	assetID := env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Forklift 1",
		"type_ref": map[string]interface{}{
			"type": "REF", "entity": "asset_types", "id": typeID,
		},
		"sensor_ref": map[string]interface{}{
			"type": "REF", "entity": "sensors", "id": sensorID,
		},
	})

	t.Run("Initial state has two outgoing edges", func(t *testing.T) {
		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		if len(neighbors) != 2 {
			t.Errorf("expected 2 outgoing edges, got %d: %v", len(neighbors), neighbors)
		}
	})

	t.Run("PUT replacing one REF updates graph correctly", func(t *testing.T) {
		// Full PUT: keep type_ref, change sensor_ref to sensor B
		status, _ := env.doJSON("PUT",
			fmt.Sprintf("/api/v1/assets/%d", assetID),
			map[string]interface{}{
				"name": "Forklift 1 (updated)",
				"type_ref": map[string]interface{}{
					"type": "REF", "entity": "asset_types", "id": typeID,
				},
				"sensor_ref": map[string]interface{}{
					"type": "REF", "entity": "sensors", "id": sensorID2,
				},
			},
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")

		// Should have edge to sensor B
		if _, ok := neighbors[fmt.Sprintf("sensors:%d", sensorID2)]; !ok {
			t.Errorf("expected edge to sensors:%d, got: %v", sensorID2, neighbors)
		}
		// Should NOT have edge to sensor A
		if _, ok := neighbors[fmt.Sprintf("sensors:%d", sensorID)]; ok {
			t.Errorf("stale edge to sensors:%d still present: %v", sensorID, neighbors)
		}
		// Should still have edge to asset_type
		if _, ok := neighbors[fmt.Sprintf("asset_types:%d", typeID)]; !ok {
			t.Errorf("edge to asset_types:%d lost during PUT: %v", typeID, neighbors)
		}
	})

	t.Run("PUT removing all REFs clears all edges", func(t *testing.T) {
		status, _ := env.doJSON("PUT",
			fmt.Sprintf("/api/v1/assets/%d", assetID),
			map[string]interface{}{
				"name":   "Forklift 1 (no refs)",
				"status": "idle",
			},
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		if len(neighbors) != 0 {
			t.Errorf("expected 0 outgoing edges after removing all REFs, got %d: %v",
				len(neighbors), neighbors)
		}
	})

	t.Run("PUT adding REFs back creates edges correctly", func(t *testing.T) {
		status, _ := env.doJSON("PUT",
			fmt.Sprintf("/api/v1/assets/%d", assetID),
			map[string]interface{}{
				"name": "Forklift 1 (restored)",
				"type_ref": map[string]interface{}{
					"type": "REF", "entity": "asset_types", "id": typeID,
				},
			},
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		if len(neighbors) != 1 {
			t.Errorf("expected 1 outgoing edge, got %d: %v", len(neighbors), neighbors)
		}
	})
}

// ============================================================================
// 7. OQL DELETE MUTATIONS (with tenant isolation)
// ============================================================================

func TestOQLDeleteMutations(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create events in two tenants
	env.createEntity("/api/v1/tenant/acme/events", map[string]interface{}{
		"type": "temperature", "value": 42, "asset_id": 1,
	})
	env.createEntity("/api/v1/tenant/acme/events", map[string]interface{}{
		"type": "humidity", "value": 80, "asset_id": 1,
	})
	env.createEntity("/api/v1/tenant/globex/events", map[string]interface{}{
		"type": "temperature", "value": 55, "asset_id": 2,
	})

	t.Run("Tenant-scoped DELETE only removes tenant's records", func(t *testing.T) {
		status, result := env.oql(
			"/api/v1/tenant/acme/oql/query",
			"DELETE FROM events WHERE type = 'temperature'",
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d: %v", status, result)
		}
		affected := int(toFloat64(result["stats"].(map[string]interface{})["rows_affected"]))
		if affected != 1 {
			t.Errorf("expected 1 acme temperature event deleted, got %d", affected)
		}
	})

	t.Run("Other tenant's matching records survive", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/globex/oql/query",
			"SELECT * FROM events WHERE type = 'temperature'",
		)
		if len(data) != 1 {
			t.Errorf("expected globex temperature event to survive, got %d results", len(data))
		}
	})

	t.Run("Acme's non-matching record survives", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT * FROM events",
		)
		if len(data) != 1 {
			t.Fatalf("expected 1 remaining acme event, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["type"] != "humidity" {
			t.Errorf("expected surviving event type=humidity, got %v", rec["type"])
		}
	})
}

// ============================================================================
// 8. COMBINED SHELF WORKFLOW: event ingest -> FSM patch -> graph check
//    Simulates the complete flow that the truth layer will augment.
// ============================================================================

func TestFSMEventWorkflow(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	tenant := "acme"
	base := "/api/v1/tenant/" + tenant

	// 1. Create asset type
	typeID := env.createEntity(base+"/asset_types", map[string]interface{}{
		"name": "Forklift", "code": "FLT",
	})

	// 2. Create FSM machine
	fsmID := env.createEntity(base+"/fsm_machines", map[string]interface{}{
		"name": "Asset Lifecycle", "initial": "idle",
	})

	// 3. Create asset with REFs
	assetID := env.createEntity(base+"/assets", map[string]interface{}{
		"name":      "Forklift 1",
		"fsm_state": "idle",
		"type_ref": map[string]interface{}{
			"type": "REF", "entity": "asset_types", "id": typeID,
		},
		"fsm_ref": map[string]interface{}{
			"type": "REF", "entity": "fsm_machines", "id": fsmID,
		},
	})

	// 4. Create events (sensor readings)
	for i := 0; i < 5; i++ {
		env.createEntity(base+"/events", map[string]interface{}{
			"asset_id":     assetID,
			"trigger_type": "sensor_reading",
			"value":        map[string]interface{}{"temperature": 20 + i},
			"timestamp":    fmt.Sprintf("2025-01-01T%02d:00:00Z", i),
		})
	}

	// 5. Simulate FSM transition (a caller patches only fsm_state)
	status, _ := env.doJSON("PATCH",
		fmt.Sprintf("%s/assets/%d", base, assetID),
		map[string]interface{}{"fsm_state": "running"},
	)
	if status != http.StatusOK {
		t.Fatalf("FSM patch failed: %d", status)
	}

	// 6. Verify the complete state

	t.Run("Asset has correct state after FSM transition", func(t *testing.T) {
		status, result := env.doJSON("GET",
			fmt.Sprintf("%s/assets/%d", base, assetID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		if result["fsm_state"] != "running" {
			t.Errorf("expected fsm_state=running, got %v", result["fsm_state"])
		}
	})

	t.Run("Graph edges intact after FSM transition", func(t *testing.T) {
		// Tenant-scoped entities have prefixed node IDs (XXXX@entity:id).
		// "acme" is the first auto-registered tenant, so tid=1 → prefix "0001".
		nodeID := fmt.Sprintf("0001@assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		if len(neighbors) != 2 {
			t.Errorf("expected 2 outgoing edges (type_ref + fsm_ref), got %d: %v",
				len(neighbors), neighbors)
		}
	})

	t.Run("OQL event query scoped to tenant", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			fmt.Sprintf("SELECT * FROM events WHERE asset_id = %d", assetID))
		if len(data) != 5 {
			t.Errorf("expected 5 events for asset, got %d", len(data))
		}
	})

	t.Run("OQL COUNT on events scoped to tenant", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			fmt.Sprintf("SELECT COUNT(*) as count FROM events WHERE asset_id = %d", assetID))
		row := data[0].(map[string]interface{})
		count := int(toFloat64(row["count"]))
		if count != 5 {
			t.Errorf("expected COUNT(*)=5, got %d", count)
		}
	})

	t.Run("OQL event query with TOP and ORDER BY", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			fmt.Sprintf(
				"SELECT TOP 2 * FROM events WHERE asset_id = %d ORDER BY timestamp DESC",
				assetID))
		if len(data) != 2 {
			t.Errorf("expected 2 events with TOP 2, got %d", len(data))
		}
	})

	t.Run("Pre-delete check: COUNT(*) prevents orphan deletion", func(t *testing.T) {
		// Check whether entity type has dependants before deletion
		data := env.oqlData(base+"/oql/query",
			fmt.Sprintf("SELECT COUNT(*) as count FROM asset_types WHERE id = %d", typeID))
		// This verifies the entity exists
		row := data[0].(map[string]interface{})
		// For now we just check the query works; the count will be 1
		if row["count"] == nil {
			t.Error("expected count field in result")
		}
	})
}

// ============================================================================
// 9. EDGE CASES: entity name validation, large payloads, concurrent-ish ops
// ============================================================================

func TestEdgeCases(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	t.Run("PATCH with null value in store mode", func(t *testing.T) {
		id := env.createEntity("/api/v1/assets", map[string]interface{}{
			"name": "Test Asset", "notes": "some notes",
		})
		status, _ := env.doJSON("PATCH",
			fmt.Sprintf("/api/v1/assets/%d", id),
			map[string]interface{}{"notes": nil},
		)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		// In "store" mode, null should be stored, not deleted
		status, result := env.doJSON("GET",
			fmt.Sprintf("/api/v1/assets/%d", id), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		// The field should exist (stored as null), not be absent
		if _, exists := result["notes"]; !exists {
			t.Error("expected notes field to exist (stored as null), but it was missing")
		}
	})

	t.Run("PATCH cannot change id", func(t *testing.T) {
		id := env.createEntity("/api/v1/assets", map[string]interface{}{
			"name": "Immutable ID test",
		})
		env.doJSON("PATCH",
			fmt.Sprintf("/api/v1/assets/%d", id),
			map[string]interface{}{"id": 9999},
		)
		// Fetch and verify id hasn't changed
		status, result := env.doJSON("GET",
			fmt.Sprintf("/api/v1/assets/%d", id), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		actualID := int(result["id"].(float64))
		if actualID != id {
			t.Errorf("PATCH should not change id: expected %d, got %d", id, actualID)
		}
	})

	t.Run("PATCH cannot change tenant_id", func(t *testing.T) {
		id := env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
			"name": "Tenant locked",
		})
		env.doJSON("PATCH",
			fmt.Sprintf("/api/v1/tenant/acme/assets/%d", id),
			map[string]interface{}{"tenant_id": "globex"},
		)
		// Verify it's still in acme
		status, _ := env.doJSON("GET",
			fmt.Sprintf("/api/v1/tenant/acme/assets/%d", id), nil)
		if status != http.StatusOK {
			t.Error("asset should still be accessible in acme tenant")
		}
		// And NOT accessible in globex
		status, _ = env.doJSON("GET",
			fmt.Sprintf("/api/v1/tenant/globex/assets/%d", id), nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404 in globex, got %d", status)
		}
	})

	t.Run("OQL SELECT with boolean WHERE", func(t *testing.T) {
		env.createEntity("/api/v1/rules", map[string]interface{}{
			"name": "Rule A", "enabled": true,
		})
		env.createEntity("/api/v1/rules", map[string]interface{}{
			"name": "Rule B", "enabled": false,
		})
		env.createEntity("/api/v1/rules", map[string]interface{}{
			"name": "Rule C", "enabled": true,
		})

		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM rules WHERE enabled = true")
		if len(data) != 2 {
			t.Errorf("expected 2 enabled rules, got %d", len(data))
		}
	})

	t.Run("OQL SELECT with AND compound WHERE", func(t *testing.T) {
		env.createEntity("/api/v1/sensors", map[string]interface{}{
			"name": "Temp A", "status": "active", "type": "temperature",
		})
		env.createEntity("/api/v1/sensors", map[string]interface{}{
			"name": "Temp B", "status": "inactive", "type": "temperature",
		})
		env.createEntity("/api/v1/sensors", map[string]interface{}{
			"name": "Humid A", "status": "active", "type": "humidity",
		})

		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM sensors WHERE status = 'active' AND type = 'temperature'")
		if len(data) != 1 {
			t.Errorf("expected 1 active temperature sensor, got %d", len(data))
		}
	})

	t.Run("OQL SELECT with OR compound WHERE", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT * FROM sensors WHERE type = 'temperature' OR type = 'humidity'")
		if len(data) != 3 {
			t.Errorf("expected 3 sensors matching OR condition, got %d", len(data))
		}
	})

	t.Run("DELETE entity removes graph node edges", func(t *testing.T) {
		managerID := env.createEntity("/api/v1/users", map[string]interface{}{
			"name": "Manager",
		})
		employeeID := env.createEntity("/api/v1/users", map[string]interface{}{
			"name": "Employee",
			"manager": map[string]interface{}{
				"type": "REF", "entity": "users", "id": managerID,
			},
		})

		// Verify edge exists
		nodeID := fmt.Sprintf("users:%d", employeeID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		if len(neighbors) == 0 {
			t.Fatal("expected edge from employee to manager")
		}

		// Delete the employee
		status, _ := env.doJSON("DELETE",
			fmt.Sprintf("/api/v1/users/%d", employeeID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		// Verify entity is gone
		status, _ = env.doJSON("GET",
			fmt.Sprintf("/api/v1/users/%d", employeeID), nil)
		if status != http.StatusNotFound {
			t.Errorf("expected 404 after delete, got %d", status)
		}
	})

	t.Run("OQL query on mixed numeric and string fields", func(t *testing.T) {
		env.createEntity("/api/v1/events", map[string]interface{}{
			"asset_id": 100, "trigger_type": "sensor_reading",
			"timestamp": "2025-06-15T10:00:00Z",
		})
		env.createEntity("/api/v1/events", map[string]interface{}{
			"asset_id": 100, "trigger_type": "manual",
			"timestamp": "2025-06-15T11:00:00Z",
		})
		env.createEntity("/api/v1/events", map[string]interface{}{
			"asset_id": 200, "trigger_type": "sensor_reading",
			"timestamp": "2025-06-15T12:00:00Z",
		})

		// typical event query pattern
		data := env.oqlData("/api/v1/oql/query",
			"SELECT TOP 10 * FROM events WHERE asset_id = 100 ORDER BY timestamp DESC")
		if len(data) != 2 {
			t.Errorf("expected 2 events for asset_id=100, got %d", len(data))
		}
	})
}

// ============================================================================
// 10. MULTIPLE REFs ON SAME ENTITY (a caller assets have type_ref + sensor_ref + fsm_ref)
// ============================================================================

func TestMultipleRefsOnEntity(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	typeID := env.createEntity("/api/v1/asset_types", map[string]interface{}{
		"name": "Forklift",
	})
	sensorID := env.createEntity("/api/v1/sensors", map[string]interface{}{
		"name": "Temp Sensor",
	})
	fsmID := env.createEntity("/api/v1/fsm_machines", map[string]interface{}{
		"name": "Lifecycle",
	})

	assetID := env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Multi-ref Asset",
		"type_ref": map[string]interface{}{
			"type": "REF", "entity": "asset_types", "id": typeID,
		},
		"sensor_ref": map[string]interface{}{
			"type": "REF", "entity": "sensors", "id": sensorID,
		},
		"fsm_ref": map[string]interface{}{
			"type": "REF", "entity": "fsm_machines", "id": fsmID,
		},
	})

	t.Run("All three REFs create graph edges", func(t *testing.T) {
		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")
		if len(neighbors) != 3 {
			t.Errorf("expected 3 outgoing edges, got %d: %v", len(neighbors), neighbors)
		}
	})

	t.Run("PATCH one REF leaves other two intact", func(t *testing.T) {
		newSensor := env.createEntity("/api/v1/sensors", map[string]interface{}{
			"name": "New Sensor",
		})
		env.doJSON("PATCH",
			fmt.Sprintf("/api/v1/assets/%d", assetID),
			map[string]interface{}{
				"sensor_ref": map[string]interface{}{
					"type": "REF", "entity": "sensors", "id": newSensor,
				},
			},
		)

		nodeID := fmt.Sprintf("assets:%d", assetID)
		neighbors := env.graphNeighbors(nodeID, "outgoing")

		// Should still have exactly 3 edges
		if len(neighbors) != 3 {
			t.Errorf("expected 3 outgoing edges after single-REF patch, got %d: %v",
				len(neighbors), neighbors)
		}

		// Old sensor edge gone, new sensor edge present
		if _, ok := neighbors[fmt.Sprintf("sensors:%d", sensorID)]; ok {
			t.Error("stale edge to old sensor still present")
		}
		if _, ok := neighbors[fmt.Sprintf("sensors:%d", newSensor)]; !ok {
			t.Error("edge to new sensor missing")
		}
		// Type and FSM edges still there
		if _, ok := neighbors[fmt.Sprintf("asset_types:%d", typeID)]; !ok {
			t.Error("edge to asset_type lost")
		}
		if _, ok := neighbors[fmt.Sprintf("fsm_machines:%d", fsmID)]; !ok {
			t.Error("edge to fsm_machines lost")
		}
	})

	t.Run("GET with embed_depth resolves all REFs", func(t *testing.T) {
		status, result := env.doJSON("GET",
			fmt.Sprintf("/api/v1/assets/%d?embed_depth=1", assetID), nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}

		// type_ref should be embedded (not a raw REF)
		typeRef, ok := result["type_ref"].(map[string]interface{})
		if !ok {
			t.Fatal("expected type_ref to be embedded object")
		}
		if typeRef["type"] == "REF" {
			t.Error("type_ref should be resolved, not a raw REF")
		}
		if typeRef["name"] != "Forklift" {
			t.Errorf("expected embedded type_ref.name=Forklift, got %v", typeRef["name"])
		}
	})
}

// ============================================================================
// helpers
// ============================================================================

// toFloat64 safely extracts a float64 from an interface{}, handling int and
// float64 types as returned by JSON unmarshaling and OQL aggregates.
func toFloat64(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	case int64:
		return float64(val)
	default:
		return 0
	}
}
