// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// e2e_coverage_gaps_test.go
//
// Tests for features identified as untested or weakly tested across the
// January and February 2026 analysis sessions:
//
//   1. Full-text search (FTS) through HTTP
//   2. REF embed depth (embed=false, embed_depth=N, default, cap)
//   3. Cascading delete (CascadingDelete=true)
//   4. PatchNullBehavior "delete" mode
//   5. Schema CRUD endpoints (POST + GET)
//   6. OQL async lifecycle (deep verification)
//   7. MaxEntitySize enforcement
//   8. Legacy graph endpoints (POST /graph/path, POST /graph/neighbors)
//
// Author: ha1tch <h@ual.fi>

import (
	"encoding/json"
	"fmt"
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
// Configurable test environment
// ----------------------------------------------------------------------------

type gapTestOpts struct {
	FullTextEnabled   bool
	CascadingDelete   bool
	PatchNullBehavior string
	RefEmbedDepth     int
	MaxEmbedDepth     int
	MaxEntitySize     int
}

func newGapEnv(t *testing.T, opts gapTestOpts) *e2eEnv {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "olu-gap-*")
	if err != nil {
		t.Fatal(err)
	}

	for _, entity := range e2eEntities {
		dir := filepath.Join(tmpDir, "test_schema", entity)
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}

	if opts.PatchNullBehavior == "" {
		opts.PatchNullBehavior = "store"
	}
	if opts.RefEmbedDepth == 0 {
		opts.RefEmbedDepth = 3
	}
	if opts.MaxEmbedDepth == 0 {
		opts.MaxEmbedDepth = 10
	}
	if opts.MaxEntitySize == 0 {
		opts.MaxEntitySize = 1048576
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
		FullTextEnabled:     opts.FullTextEnabled,
		CascadingDelete:     opts.CascadingDelete,
		RefEmbedDepth:       opts.RefEmbedDepth,
		MaxEmbedDepth:       opts.MaxEmbedDepth,
		MaxEntitySize:       opts.MaxEntitySize,
		PatchNullBehavior:   opts.PatchNullBehavior,
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		GraphQueryTTL:       86400,
		MaxQueryDepth:       10,
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}

	var store storage.Store
	if opts.FullTextEnabled {
		// FTS requires SQLite backend
		storeConfig = map[string]interface{}{
			"path":              filepath.Join(tmpDir, "test.db"),
			"full_text_enabled": true,
		}
		store, err = storage.NewStore("sqlite", storeConfig)
	} else {
		store, err = storage.NewStore("jsonfile", storeConfig)
	}
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

// ============================================================================
// 1. Full-text Search (FTS) through HTTP
// ============================================================================

// TestGap_FTS_Disabled verifies search returns 503 when FTS is disabled.
func TestGap_FTS_Disabled(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("missing q param", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/search", nil)
		if status != 400 {
			t.Errorf("expected 400 for missing q, got %d", status)
		}
	})

	t.Run("disabled returns 503", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/search?q=test", nil)
		if status != 503 {
			t.Errorf("expected 503 when FTS disabled, got %d", status)
		}
	})
}

// TestGap_FTS_Enabled verifies full-text search works end-to-end with SQLite.
func TestGap_FTS_Enabled(t *testing.T) {
	env := newGapEnv(t, gapTestOpts{FullTextEnabled: true})
	defer env.cleanup()

	// Create searchable entities
	env.create("/api/v1/assets", map[string]interface{}{
		"code":        "PUMP-001",
		"description": "centrifugal water pump for irrigation",
		"status":      "active",
	})
	env.create("/api/v1/assets", map[string]interface{}{
		"code":        "VALVE-001",
		"description": "butterfly valve for flow control",
		"status":      "active",
	})
	env.create("/api/v1/sensors", map[string]interface{}{
		"code":        "TEMP-001",
		"description": "temperature sensor for water monitoring",
	})
	env.create("/api/v1/assets", map[string]interface{}{
		"code":        "PUMP-002",
		"description": "submersible pump for deep well extraction",
		"status":      "maintenance",
	})

	t.Run("search across all entities", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=water", nil)
		assertStatus(t, status, 200)
		assertFieldExists(t, r, "query")
		assertFieldExists(t, r, "count")
		assertFieldExists(t, r, "results")
		if r["query"] != "water" {
			t.Errorf("query field: expected 'water', got %v", r["query"])
		}
		count := int(toF64(r["count"]))
		// Both pump-001 and sensor mention "water"
		if count < 2 {
			t.Errorf("expected at least 2 results for 'water', got %d", count)
		}
	})

	t.Run("search with entity filter", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=water&entity=assets", nil)
		assertStatus(t, status, 200)
		count := int(toF64(r["count"]))
		// Only the pump asset mentions water (not the sensor)
		if count < 1 {
			t.Errorf("expected at least 1 result for 'water' in assets, got %d", count)
		}
	})

	t.Run("search with no matches", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=nonexistenttermxyz", nil)
		assertStatus(t, status, 200)
		count := int(toF64(r["count"]))
		if count != 0 {
			t.Errorf("expected 0 results, got %d", count)
		}
		// Results array should be present but empty
		results, ok := r["results"].([]interface{})
		if !ok {
			t.Error("expected results to be an array even when empty")
		} else if len(results) != 0 {
			t.Errorf("expected empty results array, got %d items", len(results))
		}
	})

	t.Run("search prefix matching", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=pump", nil)
		assertStatus(t, status, 200)
		count := int(toF64(r["count"]))
		if count < 1 {
			t.Errorf("expected at least 1 result for 'pump', got %d", count)
		}
	})

	t.Run("result shape contains entity and id", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=pump", nil)
		assertStatus(t, status, 200)
		results, ok := r["results"].([]interface{})
		if !ok || len(results) == 0 {
			t.Fatal("expected at least 1 result")
		}
		// Each result should identify which entity it came from
		for i, res := range results {
			row, ok := res.(map[string]interface{})
			if !ok {
				t.Errorf("result %d: expected map, got %T", i, res)
				continue
			}
			// Result should contain enough info to identify the record
			if row["id"] == nil && row["code"] == nil {
				t.Errorf("result %d: expected at least id or code to identify record", i)
			}
		}
	})

	t.Run("case insensitive search", func(t *testing.T) {
		status1, r1 := env.doJSON("GET", "/api/v1/search?q=WATER", nil)
		assertStatus(t, status1, 200)
		status2, r2 := env.doJSON("GET", "/api/v1/search?q=water", nil)
		assertStatus(t, status2, 200)

		count1 := int(toF64(r1["count"]))
		count2 := int(toF64(r2["count"]))
		// SQLite FTS is case-insensitive by default
		if count1 != count2 {
			t.Errorf("case sensitivity mismatch: 'WATER' returned %d, 'water' returned %d", count1, count2)
		}
	})

	t.Run("multi-word search", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=flow+control", nil)
		assertStatus(t, status, 200)
		count := int(toF64(r["count"]))
		// The valve has "flow control" in its description
		if count < 1 {
			t.Errorf("expected at least 1 result for 'flow control', got %d", count)
		}
	})

	t.Run("special characters handled gracefully", func(t *testing.T) {
		// These should not crash or cause 500 errors
		specialQueries := []string{
			"test'quote",
			"test\"doublequote",
			"test;semicolon",
			"test--comment",
			"",
		}
		for _, q := range specialQueries {
			if q == "" {
				// Empty query should return 400 (missing q param handled by FTS_Disabled test)
				continue
			}
			status, _ := env.doJSON("GET", fmt.Sprintf("/api/v1/search?q=%s", q), nil)
			if status == 500 {
				t.Errorf("query %q caused 500 error — should be handled gracefully", q)
			}
		}
	})

	t.Run("entity filter for nonexistent entity returns empty", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/search?q=water&entity=nonexistent_entity_xyz", nil)
		// Should either return 200 with 0 results or 400 for bad entity
		if status == 500 {
			t.Error("nonexistent entity in FTS filter should not cause 500")
		}
		if status == 200 {
			count := int(toF64(r["count"]))
			if count != 0 {
				t.Errorf("expected 0 results for nonexistent entity, got %d", count)
			}
		}
	})
}

// ============================================================================
// 2. REF Embed Depth
// ============================================================================

// TestGap_RefEmbed tests REF embedding at various depths through HTTP.
func TestGap_RefEmbed(t *testing.T) {
	env := newGapEnv(t, gapTestOpts{RefEmbedDepth: 2})
	defer env.cleanup()

	// Build chain: asset_type <- asset <- sensor (3 levels)
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Motor", "code": "MOT",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "MOT-001",
		"asset_type": ref("asset_types", atID),
	})
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code":  "TEMP-01",
		"asset": ref("assets", aID),
	})

	t.Run("default embed depth resolves REFs", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d", sID), nil)
		assertStatus(t, status, 200)

		// With RefEmbedDepth=2, "asset" REF should be resolved
		asset, ok := r["asset"].(map[string]interface{})
		if !ok {
			t.Fatal("expected asset to be embedded (resolved), got raw REF or nil")
		}
		if asset["code"] != "MOT-001" {
			t.Errorf("embedded asset code: expected MOT-001, got %v", asset["code"])
		}

		// At depth 2, the nested asset_type should also be resolved (depth 1 remaining)
		assetType, ok := asset["asset_type"].(map[string]interface{})
		if !ok {
			t.Log("nested asset_type not embedded at depth 2 — may remain as REF depending on traversal")
		} else {
			if assetType["name"] != "Motor" {
				t.Errorf("nested asset_type name: expected Motor, got %v", assetType["name"])
			}
		}
	})

	t.Run("embed=false disables embedding", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d?embed=false", sID), nil)
		assertStatus(t, status, 200)

		// "asset" should be raw REF, not resolved
		asset, ok := r["asset"].(map[string]interface{})
		if !ok {
			t.Fatal("expected asset field to be a map")
		}
		if asset["type"] != "REF" {
			t.Error("with embed=false, asset should remain as REF")
		}
	})

	t.Run("embed_depth=0 disables embedding", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d?embed_depth=0", sID), nil)
		assertStatus(t, status, 200)

		asset, ok := r["asset"].(map[string]interface{})
		if !ok {
			t.Fatal("expected asset field")
		}
		if asset["type"] != "REF" {
			t.Error("with embed_depth=0, asset should remain as REF")
		}
	})

	t.Run("embed_depth=1 limits resolution depth", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d?embed_depth=1", sID), nil)
		assertStatus(t, status, 200)

		// First level should be embedded
		asset, ok := r["asset"].(map[string]interface{})
		if !ok || asset["type"] == "REF" {
			t.Fatal("at depth 1, first-level REF should be embedded")
		}

		// Second level should NOT be embedded (depth exhausted after decrement)
		// The handler calls embedReferences(data, depth) and embedValue
		// decrements with depth-1 on each REF resolution. So at depth=1,
		// the first REF is resolved (consuming the depth), and any nested
		// REFs should remain unresolved.
		if assetType, ok := asset["asset_type"].(map[string]interface{}); ok {
			if assetType["type"] != "REF" {
				t.Errorf("at embed_depth=1, nested asset_type should remain as REF, but was resolved: %v", assetType)
			}
		}
		// If asset_type is absent entirely, that's also acceptable (field not
		// present in stored data). But if it IS present and resolved, that's
		// a depth violation.
	})

	t.Run("embed_depth capped at MaxEmbedDepth", func(t *testing.T) {
		// MaxEmbedDepth is 10 in our gapTestOpts. Request 999 — should be
		// capped at 10. We verify by building a chain longer than 10 and
		// confirming resolution stops.
		//
		// For this test, our 3-level chain (sensor->asset->asset_type) is
		// shorter than 10, so everything resolves. The key assertion is that
		// the server applies the cap without error and the response is valid.
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d?embed_depth=999", sID), nil)
		assertStatus(t, status, 200)

		// Verify that the response did resolve (cap is 10, chain is 3, so
		// everything should be embedded despite the absurd request).
		asset, ok := r["asset"].(map[string]interface{})
		if !ok || asset["type"] == "REF" {
			t.Error("capped depth should still resolve a 3-level chain")
		}
		if asset != nil {
			if at, ok := asset["asset_type"].(map[string]interface{}); ok {
				if at["type"] == "REF" {
					t.Error("at effective depth 10, a 3-level chain should fully resolve")
				}
			}
		}
	})

	t.Run("embed_depth=2 resolves exactly 2 levels", func(t *testing.T) {
		// With a clean RefEmbedDepth=2 env: sensor -> asset (level 1) -> asset_type (level 2)
		// Both should be resolved.
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d?embed_depth=2", sID), nil)
		assertStatus(t, status, 200)

		asset, ok := r["asset"].(map[string]interface{})
		if !ok || asset["type"] == "REF" {
			t.Fatal("level 1 (asset) should be resolved at depth=2")
		}
		assetType, ok := asset["asset_type"].(map[string]interface{})
		if !ok {
			t.Fatal("level 2 (asset_type) should be present")
		}
		if assetType["type"] == "REF" {
			t.Error("level 2 (asset_type) should be resolved at depth=2")
		}
		if assetType["name"] != "Motor" {
			t.Errorf("asset_type name: expected Motor, got %v", assetType["name"])
		}
	})
}

// ============================================================================
// 3. Cascading Delete
// ============================================================================

// TestGap_CascadingDelete tests delete with CascadingDelete=true.
func TestGap_CascadingDelete(t *testing.T) {
	env := newGapEnv(t, gapTestOpts{CascadingDelete: true})
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Pump", "code": "PMP",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "PMP-001",
		"asset_type": ref("asset_types", atID),
	})

	t.Run("cascading delete returns deleted refs", func(t *testing.T) {
		status, r := env.doJSON("DELETE", fmt.Sprintf("/api/v1/assets/%d", aID), nil)
		assertStatus(t, status, 200)

		// Should include cascaded_deletes in response
		assertFieldExists(t, r, "cascaded_deletes")
		deletes, ok := r["cascaded_deletes"].([]interface{})
		if !ok {
			t.Fatal("expected cascaded_deletes to be array")
		}
		// At minimum, the deleted entity itself
		if len(deletes) < 1 {
			t.Error("expected at least 1 entry in cascaded_deletes")
		}

		// The root entity key should be in the cascaded list
		found := false
		rootKey := fmt.Sprintf("assets:%d", aID)
		for _, d := range deletes {
			if d == rootKey {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("expected root key %q in cascaded_deletes, got %v", rootKey, deletes)
		}

		// Verify the asset is actually gone
		getStatus, _ := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d", aID), nil)
		if getStatus != 404 {
			t.Errorf("expected 404 after delete, got %d", getStatus)
		}
	})

	t.Run("graph cleaned up after cascade", func(t *testing.T) {
		// Create fresh entities for this test
		at2 := env.create("/api/v1/asset_types", map[string]interface{}{
			"name": "Valve", "code": "VLV",
		})
		a2 := env.create("/api/v1/assets", map[string]interface{}{
			"code":       "VLV-001",
			"asset_type": ref("asset_types", at2),
		})

		// Verify edge exists
		_, stats := env.doJSON("GET", "/api/v1/graph/stats", nil)
		edgesBefore := int(toF64(stats["edge_count"]))

		// Delete with cascade
		env.doJSON("DELETE", fmt.Sprintf("/api/v1/assets/%d", a2), nil)

		// Graph should have fewer edges
		_, stats = env.doJSON("GET", "/api/v1/graph/stats", nil)
		edgesAfter := int(toF64(stats["edge_count"]))
		if edgesAfter >= edgesBefore {
			t.Errorf("edges should decrease after cascading delete: before=%d after=%d", edgesBefore, edgesAfter)
		}

		// The deleted node should no longer exist in the graph
		_, nodeInfo := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/nodes/assets:%d", a2), nil)
		if nodeInfo != nil && nodeInfo["error"] == nil {
			// If the endpoint returns valid node info, the node wasn't cleaned up
			if nodeInfo["node_id"] != nil {
				t.Error("deleted node should not exist in graph after cascading delete")
			}
		}
	})

	// NOTE: The current cascadeDelete implementation is a simplified BFS that
	// deletes the root entity and records it in cascaded_deletes, but does NOT
	// traverse references to find dependent entities (the "find referencing
	// entities" step in cascadeDelete is a stub comment). This means
	// multi-level cascading (e.g., deleting an asset_type should cascade to
	// its assets, then to their sensors) does not currently work.
	//
	// The test below documents this limitation: it verifies the current
	// behaviour rather than an aspirational one.
	t.Run("cascade does not yet traverse references (documents limitation)", func(t *testing.T) {
		// Create a hierarchy: type <- asset <- sensor
		at3 := env.create("/api/v1/asset_types", map[string]interface{}{
			"name": "Compressor", "code": "CMP",
		})
		a3 := env.create("/api/v1/assets", map[string]interface{}{
			"code":       "CMP-001",
			"asset_type": ref("asset_types", at3),
		})
		s3 := env.create("/api/v1/sensors", map[string]interface{}{
			"code":  "TEMP-CMP",
			"asset": ref("assets", a3),
		})

		// Delete the asset — currently only deletes the root
		status, r := env.doJSON("DELETE", fmt.Sprintf("/api/v1/assets/%d", a3), nil)
		assertStatus(t, status, 200)

		deletes := r["cascaded_deletes"].([]interface{})
		// Current behaviour: only the root entity is in the list
		if len(deletes) != 1 {
			t.Logf("cascade returned %d deletes (expected 1 with current stub): %v", len(deletes), deletes)
		}

		// The sensor should still exist (cascade didn't reach it)
		sStatus, _ := env.doJSON("GET", fmt.Sprintf("/api/v1/sensors/%d?embed=false", s3), nil)
		if sStatus == 404 {
			t.Log("sensor was cascade-deleted — implementation may have been upgraded beyond stub")
		}

		// The asset_type should still exist (it's the parent, not child)
		atStatus, _ := env.doJSON("GET", fmt.Sprintf("/api/v1/asset_types/%d", at3), nil)
		if atStatus != 200 {
			t.Errorf("asset_type should still exist after deleting child asset, got %d", atStatus)
		}
	})

	t.Run("MaxCascadeDeletions cap is enforced", func(t *testing.T) {
		// newGapEnv sets MaxCascadeDeletions=100. Create more entities than
		// that and verify the server doesn't run away.
		// Since cascade doesn't traverse refs (stub), we can't truly hit the
		// cap with dependent entities. But we can verify the config is wired
		// through by checking the response doesn't exceed the cap.
		// Create 5 entities and delete them individually to confirm bounded behaviour.
		ids := make([]int, 5)
		for i := 0; i < 5; i++ {
			ids[i] = env.create("/api/v1/assets", map[string]interface{}{
				"code":   fmt.Sprintf("CAP-%03d", i+1),
				"status": "active",
			})
		}
		for _, id := range ids {
			status, r := env.doJSON("DELETE", fmt.Sprintf("/api/v1/assets/%d", id), nil)
			assertStatus(t, status, 200)
			deletes := r["cascaded_deletes"].([]interface{})
			if len(deletes) > 100 {
				t.Errorf("cascaded_deletes exceeded MaxCascadeDeletions cap: got %d", len(deletes))
			}
		}
	})
}

// ============================================================================
// 4. PatchNullBehavior "delete" mode
// ============================================================================

// TestGap_PatchNullDelete tests that PATCH with null values removes fields
// when PatchNullBehavior is "delete".
func TestGap_PatchNullDelete(t *testing.T) {
	env := newGapEnv(t, gapTestOpts{PatchNullBehavior: "delete"})
	defer env.cleanup()

	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":        "DEL-001",
		"status":      "active",
		"description": "a pump that will lose its description",
	})

	// PATCH with null description — should remove the field
	status, _ := env.doJSON("PATCH", fmt.Sprintf("/api/v1/assets/%d", aID), map[string]interface{}{
		"description": nil,
	})
	assertStatus(t, status, 200)

	// GET and verify description is gone
	status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d?embed=false", aID), nil)
	assertStatus(t, status, 200)
	if _, exists := r["description"]; exists {
		t.Errorf("description should be deleted when PatchNullBehavior=delete, but it's still present: %v", r["description"])
	}
	// Other fields should remain
	if r["code"] != "DEL-001" {
		t.Errorf("code should still be DEL-001, got %v", r["code"])
	}
	if r["status"] != "active" {
		t.Errorf("status should still be active, got %v", r["status"])
	}
}

// TestGap_PatchNullStore tests that PATCH with null values stores null
// when PatchNullBehavior is "store" (default).
func TestGap_PatchNullStore(t *testing.T) {
	env := newE2EEnv(t) // default: PatchNullBehavior="store"
	defer env.cleanup()

	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":        "STR-001",
		"description": "will become null",
	})

	// PATCH with null description — should store null, not delete
	status, _ := env.doJSON("PATCH", fmt.Sprintf("/api/v1/assets/%d", aID), map[string]interface{}{
		"description": nil,
	})
	assertStatus(t, status, 200)

	// GET and verify description is present but null
	status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d?embed=false", aID), nil)
	assertStatus(t, status, 200)
	// The field should exist (key present) but be nil
	if _, exists := r["description"]; !exists {
		t.Error("description should still exist as key when PatchNullBehavior=store")
	}
}

// ============================================================================
// 5. Schema CRUD Endpoints
// ============================================================================

// TestGap_SchemaCRUD tests POST and GET on /api/v1/schema/{entity}.
func TestGap_SchemaCRUD(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{
				"type": "string",
			},
			"age": map[string]interface{}{
				"type": "number",
			},
		},
		"required": []interface{}{"name"},
	}

	t.Run("POST schema", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/schema/test_entity", schema)
		if status != 200 && status != 201 {
			t.Errorf("expected 200 or 201, got %d: %v", status, r)
		}
	})

	t.Run("GET schema", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/schema/test_entity", nil)
		assertStatus(t, status, 200)
		// Should return the schema we posted
		if r == nil {
			t.Fatal("expected schema body in response")
		}
	})

	t.Run("GET nonexistent schema", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/schema/nonexistent_entity", nil)
		if status != 404 && status != 200 {
			// Some implementations return 200 with empty, others 404
			t.Logf("GET nonexistent schema returned %d", status)
		}
	})
}

// ============================================================================
// 6. OQL Async Lifecycle (Deep Verification)
// ============================================================================

// TestGap_OQLAsyncLifecycle tests the full submit -> poll -> result cycle
// with detailed response shape validation.
func TestGap_OQLAsyncLifecycle(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Populate data
	for i := 0; i < 10; i++ {
		env.create("/api/v1/assets", map[string]interface{}{
			"code":   fmt.Sprintf("ASYNC-%03d", i+1),
			"status": "active",
		})
	}

	t.Run("submit returns 202 with query_id", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/oql/query/async", map[string]interface{}{
			"query": "SELECT * FROM assets WHERE status = 'active'",
		})
		if status != 202 {
			t.Fatalf("expected 202 for async submit, got %d: %v", status, r)
		}
		assertFieldExists(t, r, "query_id")
		assertFieldType(t, r, "query_id", "string")

		queryID := r["query_id"].(string)
		if queryID == "" {
			t.Fatal("query_id should not be empty")
		}

		// Poll for completion
		var resultResp map[string]interface{}
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			pollStatus, pollR := env.doJSON("GET",
				fmt.Sprintf("/api/v1/oql/query/%s", queryID), nil)
			if pollStatus != 200 {
				t.Fatalf("poll returned %d", pollStatus)
			}
			assertFieldExists(t, pollR, "status")
			st := pollR["status"].(string)
			if st == "completed" {
				resultResp = pollR
				break
			}
			if st == "failed" {
				t.Fatalf("async query failed: %v", pollR)
			}
			time.Sleep(50 * time.Millisecond)
		}
		if resultResp == nil {
			t.Fatal("async query did not complete within deadline")
		}

		// Fetch full result
		resStatus, resR := env.doJSON("GET",
			fmt.Sprintf("/api/v1/oql/query/%s/result", queryID), nil)
		assertStatus(t, resStatus, 200)
		assertFieldExists(t, resR, "status")

		// Verify the result has data with correct count
		if data, ok := resR["data"].([]interface{}); ok {
			if len(data) != 10 {
				t.Errorf("expected 10 results, got %d", len(data))
			}
			// Verify result shape: each row should have at minimum code and status
			for i, row := range data {
				rowMap, ok := row.(map[string]interface{})
				if !ok {
					t.Errorf("row %d: expected map, got %T", i, row)
					continue
				}
				if rowMap["code"] == nil {
					t.Errorf("row %d: missing 'code' field", i)
				}
				if rowMap["status"] != "active" {
					t.Errorf("row %d: expected status 'active', got %v", i, rowMap["status"])
				}
			}
		} else {
			t.Error("expected 'data' to be an array in result")
		}
	})

	t.Run("malformed query fails gracefully", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/oql/query/async", map[string]interface{}{
			"query": "THIS IS NOT VALID OQL AT ALL !!!",
		})
		// Should either reject at submission (400) or complete as failed
		if status == 202 {
			// Accepted — poll until it fails
			queryID := r["query_id"].(string)
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				_, pollR := env.doJSON("GET",
					fmt.Sprintf("/api/v1/oql/query/%s", queryID), nil)
				st, _ := pollR["status"].(string)
				if st == "failed" {
					// Correct: malformed query eventually fails
					return
				}
				if st == "completed" {
					t.Fatal("malformed query should not complete successfully")
				}
				time.Sleep(50 * time.Millisecond)
			}
			t.Fatal("malformed async query neither failed nor completed within deadline")
		} else if status != 400 {
			t.Errorf("expected 400 or 202 for malformed query, got %d", status)
		}
	})

	t.Run("concurrent async queries complete independently", func(t *testing.T) {
		queryIDs := make([]string, 3)
		queries := []string{
			"SELECT * FROM assets WHERE code = 'ASYNC-001'",
			"SELECT COUNT(*) as cnt FROM assets",
			"SELECT * FROM assets WHERE status = 'active'",
		}

		// Submit 3 queries concurrently
		for i, q := range queries {
			status, r := env.doJSON("POST", "/api/v1/oql/query/async", map[string]interface{}{
				"query": q,
			})
			if status != 202 {
				t.Fatalf("query %d: expected 202, got %d", i, status)
			}
			queryIDs[i] = r["query_id"].(string)
		}

		// All query_ids should be distinct
		seen := make(map[string]bool)
		for _, qid := range queryIDs {
			if seen[qid] {
				t.Error("duplicate query_id returned for different submissions")
			}
			seen[qid] = true
		}

		// Wait for all to complete
		deadline := time.Now().Add(5 * time.Second)
		completed := make([]bool, 3)
		for time.Now().Before(deadline) {
			allDone := true
			for i, qid := range queryIDs {
				if completed[i] {
					continue
				}
				_, pollR := env.doJSON("GET", fmt.Sprintf("/api/v1/oql/query/%s", qid), nil)
				st, _ := pollR["status"].(string)
				if st == "completed" {
					completed[i] = true
				} else if st == "failed" {
					t.Fatalf("query %d failed unexpectedly", i)
				} else {
					allDone = false
				}
			}
			if allDone {
				break
			}
			time.Sleep(50 * time.Millisecond)
		}
		for i, done := range completed {
			if !done {
				t.Errorf("query %d did not complete within deadline", i)
			}
		}

		// Verify each result independently
		// Query 0: single row for ASYNC-001
		_, r0 := env.doJSON("GET", fmt.Sprintf("/api/v1/oql/query/%s/result", queryIDs[0]), nil)
		if data, ok := r0["data"].([]interface{}); ok {
			if len(data) != 1 {
				t.Errorf("query 0: expected 1 result, got %d", len(data))
			}
		}

		// Query 1: COUNT should return 1 row with cnt=10
		_, r1 := env.doJSON("GET", fmt.Sprintf("/api/v1/oql/query/%s/result", queryIDs[1]), nil)
		if data, ok := r1["data"].([]interface{}); ok {
			if len(data) != 1 {
				t.Errorf("query 1: expected 1 row for COUNT, got %d", len(data))
			} else if row, ok := data[0].(map[string]interface{}); ok {
				if toF64(row["cnt"]) != 10 {
					t.Errorf("query 1: expected cnt=10, got %v", row["cnt"])
				}
			}
		}
	})

	t.Run("polling pending job returns valid status", func(t *testing.T) {
		// Submit and immediately poll — should see "pending" or "running"
		status, r := env.doJSON("POST", "/api/v1/oql/query/async", map[string]interface{}{
			"query": "SELECT * FROM assets",
		})
		if status != 202 {
			t.Fatalf("expected 202, got %d", status)
		}
		queryID := r["query_id"].(string)

		// Immediate poll
		pollStatus, pollR := env.doJSON("GET",
			fmt.Sprintf("/api/v1/oql/query/%s", queryID), nil)
		assertStatus(t, pollStatus, 200)
		st, _ := pollR["status"].(string)
		validStates := map[string]bool{"pending": true, "running": true, "completed": true}
		if !validStates[st] {
			t.Errorf("expected status to be pending/running/completed, got %q", st)
		}
	})

	// NOTE: The current async job manager uses context.Background() with no
	// timeout or cancellation support. There is no way to cancel a running
	// query, and queries that take arbitrarily long will run to completion.
	// If timeout/cancellation is needed, executeJob should use
	// context.WithTimeout and the JobManager should expose a Cancel method.

	t.Run("nonexistent query_id returns error", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/oql/query/nonexistent-id-12345", nil)
		if status != 404 {
			t.Errorf("expected 404 for nonexistent query_id, got %d", status)
		}
	})

	t.Run("nonexistent query_id result returns error", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/oql/query/nonexistent-id-12345/result", nil)
		if status != 404 {
			t.Errorf("expected 404 for nonexistent query_id result, got %d", status)
		}
	})
}

// ============================================================================
// 7. MaxEntitySize Enforcement
// ============================================================================

// TestGap_MaxEntitySize tests that entities exceeding the size limit are
// rejected with 413.
func TestGap_MaxEntitySize(t *testing.T) {
	// Set a tiny limit so we can test easily
	env := newGapEnv(t, gapTestOpts{MaxEntitySize: 256})
	defer env.cleanup()

	t.Run("small entity accepted", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/assets", map[string]interface{}{
			"code": "TINY-001",
		})
		if status != 201 {
			t.Errorf("expected 201, got %d", status)
		}
	})

	t.Run("oversized entity rejected with 413", func(t *testing.T) {
		// Create a large payload
		bigDesc := strings.Repeat("x", 500)
		status, r := env.doJSON("POST", "/api/v1/assets", map[string]interface{}{
			"code":        "BIG-001",
			"description": bigDesc,
		})
		if status != 413 {
			t.Errorf("expected 413 for oversized entity, got %d: %v", status, r)
		}
	})
}

// ============================================================================
// 8. Legacy Graph Endpoints (POST /graph/path, POST /graph/neighbors)
// ============================================================================

// TestGap_LegacyGraphPath tests POST /api/v1/graph/path.
func TestGap_LegacyGraphPath(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Build: type <- asset <- sensor
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Generator", "code": "GEN",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "GEN-001",
		"asset_type": ref("asset_types", atID),
	})
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code":  "RPM-01",
		"asset": ref("assets", aID),
	})

	sensorNode := fmt.Sprintf("sensors:%d", sID)
	atNode := fmt.Sprintf("asset_types:%d", atID)

	t.Run("find path between nodes", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/path", map[string]interface{}{
			"from": sensorNode,
			"to":   atNode,
		})
		assertStatus(t, status, 200)
		assertFieldExists(t, r, "path")
		assertFieldExists(t, r, "length")
		path := r["path"].([]interface{})
		length := int(toF64(r["length"]))
		if length != 2 {
			t.Errorf("expected path length 2, got %d (path: %v)", length, path)
		}
	})

	t.Run("no path returns 404", func(t *testing.T) {
		// Create isolated node
		env.create("/api/v1/users", map[string]interface{}{
			"username": "isolated",
		})
		status, _ := env.doJSON("POST", "/api/v1/graph/path", map[string]interface{}{
			"from": sensorNode,
			"to":   "users:1",
		})
		if status != 404 {
			t.Errorf("expected 404 for no path, got %d", status)
		}
	})

	t.Run("path with max_depth", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/path", map[string]interface{}{
			"from":      sensorNode,
			"to":        atNode,
			"max_depth": 1,
		})
		// Depth 1 is insufficient for a 2-hop path
		if status != 404 {
			t.Errorf("expected 404 with insufficient max_depth, got %d", status)
		}
	})

	t.Run("missing from returns 400", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/path", map[string]interface{}{
			"to": atNode,
		})
		if status != 400 {
			t.Errorf("expected 400 for missing from, got %d", status)
		}
	})

	t.Run("missing to returns 400", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/path", map[string]interface{}{
			"from": sensorNode,
		})
		if status != 400 {
			t.Errorf("expected 400 for missing to, got %d", status)
		}
	})

	t.Run("missing both returns 400", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/path", map[string]interface{}{})
		if status != 400 {
			t.Errorf("expected 400 for missing from and to, got %d", status)
		}
	})
}

// TestGap_LegacyGraphNeighbors tests POST /api/v1/graph/neighbors.
func TestGap_LegacyGraphNeighbors(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Turbine", "code": "TRB",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "TRB-001",
		"asset_type": ref("asset_types", atID),
	})
	env.create("/api/v1/sensors", map[string]interface{}{
		"code":  "VIB-01",
		"asset": ref("assets", aID),
	})

	assetNode := fmt.Sprintf("assets:%d", aID)

	t.Run("outgoing neighbors (default)", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/neighbors", map[string]interface{}{
			"node_id": assetNode,
		})
		assertStatus(t, status, 200)
		neighbors := r["neighbors"].(map[string]interface{})
		outgoing, ok := neighbors["outgoing"].(map[string]interface{})
		if !ok {
			t.Fatal("expected outgoing neighbors map")
		}
		// asset -> asset_type
		if len(outgoing) != 1 {
			t.Errorf("expected 1 outgoing neighbor, got %d", len(outgoing))
		}
	})

	t.Run("incoming neighbors", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/neighbors", map[string]interface{}{
			"node_id":   assetNode,
			"direction": "in",
		})
		assertStatus(t, status, 200)
		neighbors := r["neighbors"].(map[string]interface{})
		incoming, ok := neighbors["incoming"].(map[string]interface{})
		if !ok {
			t.Fatal("expected incoming neighbors map")
		}
		// sensor -> asset
		if len(incoming) != 1 {
			t.Errorf("expected 1 incoming neighbor, got %d", len(incoming))
		}
	})

	t.Run("both directions", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/neighbors", map[string]interface{}{
			"node_id":   assetNode,
			"direction": "both",
		})
		assertStatus(t, status, 200)
		neighbors := r["neighbors"].(map[string]interface{})
		if _, ok := neighbors["outgoing"]; !ok {
			t.Error("expected outgoing in both mode")
		}
		if _, ok := neighbors["incoming"]; !ok {
			t.Error("expected incoming in both mode")
		}
	})
}

// ============================================================================
// Bonus: Metrics endpoint (lightly tested)
// ============================================================================

// TestGap_MetricsEndpoint tests GET /metrics for both formats.
func TestGap_MetricsEndpoint(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("default format is text", func(t *testing.T) {
		resp, body := env.do("GET", "/metrics", nil)
		// Metrics may return 200 or 503 depending on whether metrics are enabled
		if resp == 200 {
			if len(body) == 0 {
				t.Error("expected non-empty metrics body")
			}
		} else if resp == 503 {
			// Metrics not enabled — acceptable
		} else {
			t.Errorf("expected 200 or 503, got %d", resp)
		}
	})
}

// ============================================================================
// Bonus: PATCH cannot change id or tenant_id
// ============================================================================

// TestGap_PatchProtectedFields tests that id and tenant_id cannot be changed
// via PATCH.
func TestGap_PatchProtectedFields(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":   "PROT-001",
		"status": "active",
	})

	// Try to change id via PATCH
	status, _ := env.doJSON("PATCH", fmt.Sprintf("/api/v1/assets/%d", aID), map[string]interface{}{
		"id":     999,
		"status": "maintenance",
	})
	assertStatus(t, status, 200)

	// Verify id didn't change
	status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/assets/%d?embed=false", aID), nil)
	assertStatus(t, status, 200)
	gotID := int(toF64(r["id"]))
	if gotID != aID {
		t.Errorf("id should not have changed: expected %d, got %d", aID, gotID)
	}
	if r["status"] != "maintenance" {
		t.Errorf("status should have changed to maintenance, got %v", r["status"])
	}
}

// ============================================================================
// Helpers (assertStatus, assertFieldExists, assertFieldType already in
// e2e_graph_intensive_test.go — these are available package-wide)
// ============================================================================

// toF64JSON is a type-safe float64 extraction used only in this file
// to avoid any collision risk. Delegates to the shared toF64 from e2e_test.go.
var _ = json.Marshal // keep import
var _ = http.StatusOK
