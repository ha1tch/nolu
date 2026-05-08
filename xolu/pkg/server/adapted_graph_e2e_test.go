// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// setupSQLiteGraphTestServer creates a SQLite-backed server with graph enabled.
func setupSQLiteGraphTestServer(t *testing.T) *TestServer {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &config.Config{
		Host:                  "localhost",
		Port:                  0,
		StorageType:           "sqlite",
		DBPath:                dbPath,
		BaseDir:               tmpDir,
		Schema:                "test_schema",
		SchemaDir:             filepath.Join(tmpDir, "test_schema"),
		CacheType:             "memory",
		CacheTTL:              300,
		GraphEnabled:          true,
		GraphMode:             "flat",
		FullTextEnabled:       false,
		MaxEmbedDepth:         10,
		RefEmbedDepth:         3,
		MaxEntitySize:         1048576,
		DefaultPageSize:       10,
		PatchNullBehavior:     "store",
		TenantMode:            "path",
		TenantAutoRegister:    true,
		MaxCascadeDeletions:   100,
		QueryTimeout:          30,
		QueryMaxRows:          10000,
		QueryMaxScanRows:      100000,
		QueryMaxResponseBytes: 10485760,
		GraphDataFile:         filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:        filepath.Join(tmpDir, "graph.index"),
		GraphQueryTTL:         86400,
		MaxQueryDepth:         10,
	}

	os.MkdirAll(cfg.SchemaDir, 0755)

	storeConfig := map[string]interface{}{
		"db_path": dbPath,
	}
	store, err := storage.NewStore("sqlite", storeConfig)
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	g := graph.NewFlatGraph()
	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	return &TestServer{
		server:      srv,
		ts:          ts,
		cfg:         cfg,
		t:           t,
		sqliteStore: store,
	}
}

// TestAdaptedGraph_NodeSearchIncludesEdgeFreeNodes is a regression test for
// item 4.2: GetNodesByType only returned nodes present in the in-memory graph
// index, which meant entities with no REF edges were silently absent from
// /graph/nodes/search results.
//
// After the fix, the handler falls back to SELECT id FROM olu_X for adapted
// entities, ensuring all entities appear regardless of edge presence.
func TestAdaptedGraph_NodeSearchIncludesEdgeFreeNodes(t *testing.T) {
	ts := setupSQLiteGraphTestServer(t)
	defer ts.ts.Close()
	if ts.sqliteStore != nil {
		defer ts.sqliteStore.Close()
	}

	// Register a schema for "products" — this triggers adapted table creation.
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"sku":   map[string]interface{}{"type": "string"},
			"price": map[string]interface{}{"type": "number"},
		},
		"required": []interface{}{"name"},
	}
	resp, body := ts.doRequest("POST", "/api/v1/schema/products", schema)
	if resp.StatusCode != 201 {
		t.Fatalf("POST schema: got %d, body: %s", resp.StatusCode, string(body))
	}

	// Create three products — none have REF fields so they will have no edges
	// in the graph and would be invisible under the old code path.
	for i, name := range []string{"Widget", "Gadget", "Doohickey"} {
		resp, body = ts.doRequest("POST", "/api/v1/products", map[string]interface{}{
			"name":  name,
			"sku":   "SKU-" + name,
			"price": float64(i+1) * 9.99,
		})
		if resp.StatusCode != 201 {
			t.Fatalf("POST products[%d]: got %d, body: %s", i, resp.StatusCode, string(body))
		}
	}

	// Verify node search returns all three products.
	resp, body = ts.doRequest("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
		"entity": "products",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("graph/nodes/search: got %d, body: %s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}

	nodes, ok := result["nodes"].([]interface{})
	if !ok {
		t.Fatalf("nodes field missing or wrong type: %T", result["nodes"])
	}
	if len(nodes) != 3 {
		t.Errorf("expected 3 product nodes (edge-free entities), got %d — adapted table fallback not working", len(nodes))
	}
	count := int(result["count"].(float64))
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

// TestAdaptedGraph_DegreeEdgeFreeNode is a regression test for item 4.1:
// GetDegree returned 404 for adapted entities with no edges because they
// were absent from the in-memory adjacency map. After the fix the handler
// falls back to COUNT queries against the edge table and returns {0,0,0}.
func TestAdaptedGraph_DegreeEdgeFreeNode(t *testing.T) {
	ts := setupSQLiteGraphTestServer(t)
	defer ts.ts.Close()
	if ts.sqliteStore != nil {
		defer ts.sqliteStore.Close()
	}

	// Register a schema to get an adapted table.
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"name": map[string]interface{}{"type": "string"},
		},
		"required": []interface{}{"name"},
	}
	resp, body := ts.doRequest("POST", "/api/v1/schema/widgets", schema)
	if resp.StatusCode != 201 {
		t.Fatalf("POST schema: %d %s", resp.StatusCode, body)
	}

	// Create a widget with no REF fields — it will have no graph edges.
	resp, body = ts.doRequest("POST", "/api/v1/widgets", map[string]interface{}{
		"name": "Sprocket",
	})
	if resp.StatusCode != 201 {
		t.Fatalf("POST widget: %d %s", resp.StatusCode, body)
	}
	var created map[string]interface{}
	json.Unmarshal(body, &created)
	id := int(created["id"].(float64))

	t.Run("degree of edge-free adapted entity returns 0,0,0 not 404", func(t *testing.T) {
		nodeID := "widgets:" + fmt.Sprintf("%d", id)
		resp, body = ts.doRequest("GET", "/api/v1/graph/nodes/"+nodeID+"/degree", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d: %s", resp.StatusCode, body)
		}
		var result map[string]interface{}
		json.Unmarshal(body, &result)
		deg := result["degree"].(map[string]interface{})
		if int(deg["in"].(float64)) != 0 || int(deg["out"].(float64)) != 0 || int(deg["total"].(float64)) != 0 {
			t.Errorf("expected degree {0,0,0}, got %v", deg)
		}
	})

	t.Run("degree of non-existent entity still returns 404", func(t *testing.T) {
		resp, _ = ts.doRequest("GET", "/api/v1/graph/nodes/widgets:99999/degree", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404 for missing entity, got %d", resp.StatusCode)
		}
	})
}

// TestUpdateGraph_REFSEdgesPopulatedInMemory is a regression test for the bug
// where updateGraph used an inline type-switch that only matched single-REF
// maps, silently dropping all @REFS slice edges from the in-memory graph.
//
// After the fix, updateGraph delegates to models.ExtractRefs, which handles
// both single REF maps and []interface{} @REFS slices.
func TestUpdateGraph_REFSEdgesPopulatedInMemory(t *testing.T) {
	ts := setupSQLiteGraphTestServer(t)
	defer ts.ts.Close()
	if ts.sqliteStore != nil {
		defer ts.sqliteStore.Close()
	}

	// Create three tags and one post that references all three via @REFS.
	var tagIDs [3]int
	for i, name := range []string{"go", "db", "graph"} {
		resp, body := ts.doRequest("POST", "/api/v1/tags", map[string]interface{}{"name": name})
		if resp.StatusCode != 201 {
			t.Fatalf("POST tag %s: %d %s", name, resp.StatusCode, body)
		}
		var r map[string]interface{}
		json.Unmarshal(body, &r)
		tagIDs[i] = int(r["id"].(float64))
	}

	resp, body := ts.doRequest("POST", "/api/v1/posts", map[string]interface{}{
		"title": "Multi-tag post",
		"tags": []interface{}{
			map[string]interface{}{"type": "REF", "entity": "tags", "id": tagIDs[0]},
			map[string]interface{}{"type": "REF", "entity": "tags", "id": tagIDs[1]},
			map[string]interface{}{"type": "REF", "entity": "tags", "id": tagIDs[2]},
		},
	})
	if resp.StatusCode != 201 {
		t.Fatalf("POST post: %d %s", resp.StatusCode, body)
	}
	var postResult map[string]interface{}
	json.Unmarshal(body, &postResult)
	postID := int(postResult["id"].(float64))

	// Query the in-memory graph for the post's outgoing edges.
	// Before the fix this returned 0; after the fix it must return 3.
	nodeID := fmt.Sprintf("posts:%d", postID)
	resp, body = ts.doRequest("GET", "/api/v1/graph/"+nodeID+"/out", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /graph/%s/out: %d %s", nodeID, resp.StatusCode, body)
	}
	var edgeResult map[string]interface{}
	json.Unmarshal(body, &edgeResult)

	edges, _ := edgeResult["edges"].([]interface{})
	if len(edges) != 3 {
		t.Errorf("expected 3 outgoing edges for @REFS post, got %d — updateGraph @REFS fix not applied", len(edges))
	}
}
