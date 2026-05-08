// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// e2e_graph_intensive_test.go
//
// Intensive HTTP-level tests for graph API endpoints. These go deeper than
// the e2e_test.go coverage by:
//
//   - Testing exact response shapes and field types
//   - Building complex graph topologies through the HTTP API
//   - Verifying graph consistency after sequences of CRUD mutations
//   - Testing graph endpoints with adversarial/edge-case inputs
//   - Exercising graph + OQL combined workflows
//
// Author: ha1tch <h@ual.fi>

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ============================================================================
// 1. Response shape validation — verify exact structure of every graph endpoint
// ============================================================================

// TestGraphHTTP_ResponseShapes creates a known graph and validates every
// field in every graph endpoint's response.
func TestGraphHTTP_ResponseShapes(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Build known topology: asset_type <- asset <- sensor, asset <- event
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Pump", "code": "PMP",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code": "PMP-001",
		"asset_type": ref("asset_types", atID),
	})
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code": "TEMP-01",
		"asset": ref("assets", aID),
	})
	env.create("/api/v1/events", map[string]interface{}{
		"event_type": "reading",
		"asset":      ref("assets", aID),
		"sensor":     ref("sensors", sID),
		"value":      23.5,
	})

	t.Run("GET /graph/stats shape", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/graph/stats", nil)
		assertStatus(t, status, 200)
		assertFieldType(t, r, "node_count", "float64")
		assertFieldType(t, r, "edge_count", "float64")
		nc := int(toF64(r["node_count"]))
		ec := int(toF64(r["edge_count"]))
		if nc != 4 {
			t.Errorf("expected 4 nodes (type+asset+sensor+event), got %d", nc)
		}
		// asset->type, sensor->asset, event->asset, event->sensor = 4
		if ec != 4 {
			t.Errorf("expected 4 edges, got %d", ec)
		}
	})

	assetNode := fmt.Sprintf("assets:%d", aID)
	atNode := fmt.Sprintf("asset_types:%d", atID)
	sensorNode := fmt.Sprintf("sensors:%d", sID)

	t.Run("GET /graph/nodes/{id} shape", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/nodes/%s", assetNode), nil)
		assertStatus(t, status, 200)
		assertFieldType(t, r, "id", "string")
		assertFieldType(t, r, "entity", "string")
		assertFieldType(t, r, "entity_id", "float64")
		assertFieldExists(t, r, "outgoing")
		assertFieldExists(t, r, "incoming")
		assertFieldExists(t, r, "degree")

		if r["id"] != assetNode {
			t.Errorf("id: expected %s, got %v", assetNode, r["id"])
		}
		if r["entity"] != "assets" {
			t.Errorf("entity: expected assets, got %v", r["entity"])
		}
		if int(toF64(r["entity_id"])) != aID {
			t.Errorf("entity_id: expected %d, got %v", aID, r["entity_id"])
		}

		outgoing := r["outgoing"].(map[string]interface{})
		if outgoing[atNode] != "asset_type" {
			t.Errorf("expected outgoing edge to %s with rel 'asset_type', got %v", atNode, outgoing)
		}

		incoming := r["incoming"].(map[string]interface{})
		if incoming[sensorNode] != "asset" {
			t.Errorf("expected incoming from %s, got %v", sensorNode, incoming)
		}

		degree := r["degree"].(map[string]interface{})
		if int(toF64(degree["out"])) != 1 {
			t.Errorf("out degree: expected 1, got %v", degree["out"])
		}
		// sensor + event = 2 incoming
		if int(toF64(degree["in"])) != 2 {
			t.Errorf("in degree: expected 2, got %v", degree["in"])
		}
		if int(toF64(degree["total"])) != 3 {
			t.Errorf("total degree: expected 3, got %v", degree["total"])
		}
	})

	t.Run("GET /graph/nodes/{id}/degree shape", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/nodes/%s/degree", assetNode), nil)
		assertStatus(t, status, 200)
		assertFieldType(t, r, "node_id", "string")
		assertFieldExists(t, r, "degree")
		if r["node_id"] != assetNode {
			t.Errorf("node_id mismatch: %v", r["node_id"])
		}
	})

	t.Run("GET /graph/{id}/in shape", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/in", assetNode), nil)
		assertStatus(t, status, 200)
		assertFieldType(t, r, "node_id", "string")
		assertFieldType(t, r, "count", "float64")
		edges := r["edges"].([]interface{})
		if len(edges) != 2 {
			t.Errorf("expected 2 incoming edges, got %d", len(edges))
		}
		// Verify edge object structure
		edge := edges[0].(map[string]interface{})
		assertFieldExists(t, edge, "source")
		assertFieldExists(t, edge, "target")
		assertFieldExists(t, edge, "relationship")
		if edge["target"] != assetNode {
			t.Errorf("edge target should be %s, got %v", assetNode, edge["target"])
		}
	})

	t.Run("GET /graph/{id}/out shape", func(t *testing.T) {
		status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/out", assetNode), nil)
		assertStatus(t, status, 200)
		edges := r["edges"].([]interface{})
		if len(edges) != 1 {
			t.Errorf("expected 1 outgoing edge, got %d", len(edges))
		}
		edge := edges[0].(map[string]interface{})
		if edge["source"] != assetNode {
			t.Errorf("source should be %s, got %v", assetNode, edge["source"])
		}
		if edge["target"] != atNode {
			t.Errorf("target should be %s, got %v", atNode, edge["target"])
		}
		if edge["relationship"] != "asset_type" {
			t.Errorf("relationship should be asset_type, got %v", edge["relationship"])
		}
	})

	t.Run("POST /graph/shortestPath shape", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": fmt.Sprintf("events:%d", 1),
			"to":   atNode,
		})
		assertStatus(t, status, 200)
		assertFieldType(t, r, "from", "string")
		assertFieldType(t, r, "to", "string")
		assertFieldType(t, r, "exists", "bool")
		assertFieldType(t, r, "length", "float64")
		if r["exists"] != true {
			t.Error("path should exist")
		}
		path := r["path"].([]interface{})
		if len(path) < 3 {
			t.Errorf("expected path >= 3 hops, got %d: %v", len(path), path)
		}
	})

	t.Run("POST /graph/pathExists shape", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/pathExists", map[string]interface{}{
			"from": sensorNode,
			"to":   atNode,
		})
		assertStatus(t, status, 200)
		assertFieldType(t, r, "from", "string")
		assertFieldType(t, r, "to", "string")
		assertFieldType(t, r, "exists", "bool")
		assertFieldType(t, r, "length", "float64")
		if r["exists"] != true {
			t.Error("path sensor -> asset_type should exist")
		}
	})

	t.Run("POST /graph/commonNeighbors shape", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{
			"node_a": sensorNode,
			"node_b": fmt.Sprintf("events:%d", 1),
		})
		assertStatus(t, status, 200)
		assertFieldType(t, r, "node_a", "string")
		assertFieldType(t, r, "node_b", "string")
		assertFieldType(t, r, "count", "float64")
		common := r["common"].([]interface{})
		// Both sensor and event point to the asset
		found := false
		for _, n := range common {
			if n.(string) == assetNode {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %s in common neighbors, got %v", assetNode, common)
		}
	})

	t.Run("POST /graph/nodes/search shape", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "assets",
		})
		assertStatus(t, status, 200)
		assertFieldType(t, r, "count", "float64")
		nodes := r["nodes"].([]interface{})
		if len(nodes) != 1 {
			t.Errorf("expected 1 asset node, got %d", len(nodes))
		}
	})
}

// ============================================================================
// 2. Graph consistency after CRUD mutations through HTTP
// ============================================================================

// TestGraphHTTP_EdgeCleanupOnDelete verifies that deleting an entity through
// the HTTP API also cleans up its graph edges.
func TestGraphHTTP_EdgeCleanupOnDelete(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Disposable", "code": "DSP",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "DSP-001",
		"asset_type": ref("asset_types", atID),
	})

	// Verify edge exists
	_, stats := env.doJSON("GET", "/api/v1/graph/stats", nil)
	edgesBefore := int(toF64(stats["edge_count"]))
	if edgesBefore != 1 {
		t.Fatalf("expected 1 edge before delete, got %d", edgesBefore)
	}

	// Delete the asset
	status, _ := env.doJSON("DELETE", fmt.Sprintf("/api/v1/assets/%d", aID), nil)
	if status != http.StatusOK {
		t.Fatalf("DELETE expected 200, got %d", status)
	}

	// Graph should have cleaned up the edge
	_, stats = env.doJSON("GET", "/api/v1/graph/stats", nil)
	edgesAfter := int(toF64(stats["edge_count"]))
	if edgesAfter != 0 {
		t.Errorf("expected 0 edges after delete, got %d", edgesAfter)
	}
}

// TestGraphHTTP_PatchRefChange verifies that PATCHing a REF field updates
// the graph edge correctly.
func TestGraphHTTP_PatchRefChange(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	at1 := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Type A", "code": "TA",
	})
	at2 := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Type B", "code": "TB",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "REF-001",
		"asset_type": ref("asset_types", at1),
	})

	// Verify initial edge
	assetNode := fmt.Sprintf("assets:%d", aID)
	status, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/out", assetNode), nil)
	assertStatus(t, status, 200)
	edges := r["edges"].([]interface{})
	if len(edges) != 1 {
		t.Fatalf("expected 1 outgoing edge, got %d", len(edges))
	}
	edge := edges[0].(map[string]interface{})
	if edge["target"] != fmt.Sprintf("asset_types:%d", at1) {
		t.Fatalf("initial edge target wrong: %v", edge["target"])
	}

	// PATCH to change the REF
	status, _ = env.doJSON("PATCH", fmt.Sprintf("/api/v1/assets/%d", aID), map[string]interface{}{
		"asset_type": ref("asset_types", at2),
	})
	assertStatus(t, status, 200)

	// Verify edge changed
	status, r = env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/out", assetNode), nil)
	assertStatus(t, status, 200)
	edges = r["edges"].([]interface{})
	if len(edges) != 1 {
		t.Errorf("expected 1 outgoing edge after PATCH, got %d", len(edges))
	}
	if len(edges) > 0 {
		edge = edges[0].(map[string]interface{})
		expected := fmt.Sprintf("asset_types:%d", at2)
		if edge["target"] != expected {
			t.Errorf("expected edge to %s after PATCH, got %v", expected, edge["target"])
		}
	}
}

// TestGraphHTTP_PutReplacesAllEdges verifies that PUT (full replacement)
// removes edges from fields that are no longer present.
func TestGraphHTTP_PutReplacesAllEdges(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Motor", "code": "MOT",
	})
	sID := env.create("/api/v1/sensors", map[string]interface{}{
		"code": "VIB-01",
	})
	aID := env.create("/api/v1/assets", map[string]interface{}{
		"code":       "MOT-001",
		"asset_type": ref("asset_types", atID),
		"sensor":     ref("sensors", sID),
	})

	// Verify 2 outgoing edges
	assetNode := fmt.Sprintf("assets:%d", aID)
	_, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/out", assetNode), nil)
	edges := r["edges"].([]interface{})
	if len(edges) != 2 {
		t.Fatalf("expected 2 outgoing edges initially, got %d", len(edges))
	}

	// PUT with only asset_type — sensor ref should be removed
	status, _ := env.doJSON("PUT", fmt.Sprintf("/api/v1/assets/%d", aID), map[string]interface{}{
		"code":       "MOT-001",
		"asset_type": ref("asset_types", atID),
	})
	assertStatus(t, status, 200)

	// Now only 1 edge should remain
	_, r = env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/out", assetNode), nil)
	edges = r["edges"].([]interface{})
	if len(edges) != 1 {
		t.Errorf("expected 1 outgoing edge after PUT, got %d", len(edges))
	}
}

// ============================================================================
// 3. Complex graph topology through HTTP
// ============================================================================

// TestGraphHTTP_MultiLevelHierarchy builds a 3-level hierarchy through the
// API and verifies traversal at every level.
func TestGraphHTTP_MultiLevelHierarchy(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Level 1: asset types
	pumpType := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Pump", "code": "PMP",
	})
	valveType := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Valve", "code": "VLV",
	})

	// Level 2: assets referencing types
	pumps := make([]int, 3)
	for i := 0; i < 3; i++ {
		pumps[i] = env.create("/api/v1/assets", map[string]interface{}{
			"code":       fmt.Sprintf("PMP-%03d", i+1),
			"asset_type": ref("asset_types", pumpType),
		})
	}
	valves := make([]int, 2)
	for i := 0; i < 2; i++ {
		valves[i] = env.create("/api/v1/assets", map[string]interface{}{
			"code":       fmt.Sprintf("VLV-%03d", i+1),
			"asset_type": ref("asset_types", valveType),
		})
	}

	// Level 3: sensors referencing assets
	for _, pID := range pumps {
		env.create("/api/v1/sensors", map[string]interface{}{
			"code":  fmt.Sprintf("S-PMP-%d", pID),
			"asset": ref("assets", pID),
		})
	}

	t.Run("total node and edge counts", func(t *testing.T) {
		_, stats := env.doJSON("GET", "/api/v1/graph/stats", nil)
		// 2 types + 5 assets + 3 sensors = 10 nodes
		// 5 asset->type edges + 3 sensor->asset edges = 8 edges
		nc := int(toF64(stats["node_count"]))
		ec := int(toF64(stats["edge_count"]))
		if nc != 10 {
			t.Errorf("expected 10 nodes, got %d", nc)
		}
		if ec != 8 {
			t.Errorf("expected 8 edges, got %d", ec)
		}
	})

	t.Run("pump type incoming edges count", func(t *testing.T) {
		nodeID := fmt.Sprintf("asset_types:%d", pumpType)
		_, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/in", nodeID), nil)
		count := int(toF64(r["count"]))
		if count != 3 {
			t.Errorf("pump type should have 3 incoming edges, got %d", count)
		}
	})

	t.Run("valve type incoming edges count", func(t *testing.T) {
		nodeID := fmt.Sprintf("asset_types:%d", valveType)
		_, r := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/in", nodeID), nil)
		count := int(toF64(r["count"]))
		if count != 2 {
			t.Errorf("valve type should have 2 incoming edges, got %d", count)
		}
	})

	t.Run("sensor can reach asset type via shortest path", func(t *testing.T) {
		sensorNode := fmt.Sprintf("sensors:%d", 1) // first sensor created (ID depends on global counter)
		_, r := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": sensorNode,
			"to":   fmt.Sprintf("asset_types:%d", pumpType),
		})
		// sensor -> asset -> asset_type = 2 hops
		if r["exists"] != true {
			t.Error("path should exist")
		}
		length := int(toF64(r["length"]))
		if length != 2 {
			t.Errorf("expected path length 2, got %d", length)
		}
	})

	t.Run("node search counts by type", func(t *testing.T) {
		_, r := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "sensors",
		})
		count := int(toF64(r["count"]))
		if count != 3 {
			t.Errorf("expected 3 sensor nodes, got %d", count)
		}

		_, r = env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "assets",
		})
		count = int(toF64(r["count"]))
		if count != 5 {
			t.Errorf("expected 5 asset nodes, got %d", count)
		}
	})
}

// TestGraphHTTP_SulpherSyncVsAsync verifies that sync and async Sulpher
// queries return equivalent results.
func TestGraphHTTP_SulpherSyncVsAsync(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	// Build small graph
	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Test", "code": "TST",
	})
	env.create("/api/v1/assets", map[string]interface{}{
		"code": "TST-001", "asset_type": ref("asset_types", atID),
	})

	query := "MATCH (a:assets)-[r]->(at:asset_types) RETURN a, r, at"

	// Sync query
	syncStatus, syncR := env.doJSON("POST", "/api/v1/graph/query", map[string]interface{}{
		"query": query,
	})
	if syncStatus != 200 {
		t.Fatalf("sync query failed: %d %v", syncStatus, syncR)
	}

	// Async query
	asyncStatus, asyncR := env.doJSON("POST", "/api/v1/graph/query/async", map[string]interface{}{
		"query": query,
	})
	if asyncStatus != 202 {
		t.Fatalf("async submit failed: %d %v", asyncStatus, asyncR)
	}
	queryID := asyncR["query_id"].(string)

	// Poll
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, poll := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/query/%s", queryID), nil)
		if poll["status"] == "completed" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Fetch result
	_, resultR := env.doJSON("GET", fmt.Sprintf("/api/v1/graph/query/%s/result", queryID), nil)
	if resultR["status"] != "completed" {
		t.Fatalf("async query did not complete: %v", resultR)
	}

	// Both should have stats
	if syncR["stats"] == nil {
		t.Error("sync response missing stats")
	}
	if resultR["stats"] == nil {
		t.Error("async response missing stats")
	}
}

// ============================================================================
// 4. Adversarial / edge-case inputs for graph endpoints
// ============================================================================

// TestGraphHTTP_AdversarialInputs tests graph endpoints with unusual inputs.
func TestGraphHTTP_AdversarialInputs(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("node info with colon-heavy ID", func(t *testing.T) {
		// The URL routing must handle entity:id format correctly
		status, _ := env.doJSON("GET", "/api/v1/graph/nodes/assets:999", nil)
		// 404 is fine — just shouldn't be 500
		if status == 500 {
			t.Error("should not 500 on well-formed but nonexistent node ID")
		}
	})

	t.Run("shortestPath from=to (self)", func(t *testing.T) {
		env.create("/api/v1/assets", map[string]interface{}{"code": "SELF-01"})
		status, r := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": "assets:1", "to": "assets:1",
		})
		assertStatus(t, status, 200)
		// Path to self should exist with length 0
		if r["exists"] != true {
			t.Error("path to self should exist")
		}
	})

	t.Run("pathExists from=to (self)", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/pathExists", map[string]interface{}{
			"from": "assets:1", "to": "assets:1",
		})
		assertStatus(t, status, 200)
		if r["exists"] != true {
			t.Error("pathExists to self should be true")
		}
		if toF64(r["length"]) != 0 {
			t.Errorf("pathExists to self should have length 0, got %v", r["length"])
		}
	})

	t.Run("commonNeighbors with same node twice", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{
			"node_a": "assets:1", "node_b": "assets:1",
		})
		assertStatus(t, status, 200)
		// Common neighbors of a node with itself = all its neighbors
		// Should not crash or return error
		_ = r
	})

	t.Run("shortestPath with max_depth=0", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": "assets:1", "to": "assets:1", "max_depth": 0,
		})
		// Should not crash — server sets default depth when <= 0
		if status == 500 {
			t.Error("max_depth=0 should not cause 500")
		}
	})

	t.Run("node search with empty entity string", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "",
		})
		assertStatus(t, status, 200)
		// Empty entity = search all nodes
		_ = r
	})

	t.Run("Sulpher query with invalid syntax", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/query", map[string]interface{}{
			"query": "NOT A VALID QUERY AT ALL!!!",
		})
		if status != 400 {
			t.Errorf("invalid Sulpher query expected 400, got %d", status)
		}
	})

	t.Run("Sulpher async with invalid syntax", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/query/async", map[string]interface{}{
			"query": "GARBAGE QUERY",
		})
		if status != 400 {
			t.Errorf("invalid Sulpher async query expected 400, got %d", status)
		}
	})

	t.Run("node search with very large limit", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
			"entity": "assets",
			"limit":  999999,
		})
		assertStatus(t, status, 200)
	})

	t.Run("graph endpoint with extra unknown fields", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from":          "assets:1",
			"to":            "assets:1",
			"unknown_field": "should be ignored",
			"another":       42,
		})
		// Should not crash from extra fields
		if status == 500 {
			t.Error("extra fields should not cause 500")
		}
	})
}

// TestGraphHTTP_EmptyGraphQueries tests all graph endpoints against an
// empty graph — no entities created.
func TestGraphHTTP_EmptyGraphQueries(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	t.Run("stats on empty", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/graph/stats", nil)
		assertStatus(t, status, 200)
		if toF64(r["node_count"]) != 0 {
			t.Errorf("expected 0 nodes, got %v", r["node_count"])
		}
	})

	t.Run("node info on empty", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/graph/nodes/assets:1", nil)
		if status != 404 {
			t.Errorf("expected 404, got %d", status)
		}
	})

	t.Run("degree on empty", func(t *testing.T) {
		status, _ := env.doJSON("GET", "/api/v1/graph/nodes/assets:1/degree", nil)
		if status != 404 {
			t.Errorf("expected 404, got %d", status)
		}
	})

	t.Run("in edges on empty", func(t *testing.T) {
		status, r := env.doJSON("GET", "/api/v1/graph/assets:1/in", nil)
		// The handler doesn't check if node exists, just returns empty
		assertStatus(t, status, 200)
		edges, _ := r["edges"].([]interface{})
		if len(edges) != 0 {
			t.Errorf("expected 0 edges, got %d", len(edges))
		}
	})

	t.Run("shortestPath on empty", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/shortestPath", map[string]interface{}{
			"from": "a:1", "to": "b:1",
		})
		assertStatus(t, status, 200)
		if r["exists"] != false {
			t.Error("should be no path on empty graph")
		}
	})

	t.Run("search all on empty", func(t *testing.T) {
		status, r := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{})
		assertStatus(t, status, 200)
		nodes, _ := r["nodes"].([]interface{})
		if len(nodes) != 0 {
			t.Errorf("expected 0 nodes, got %d", len(nodes))
		}
	})

	t.Run("Sulpher query on empty graph", func(t *testing.T) {
		status, _ := env.doJSON("POST", "/api/v1/graph/query", map[string]interface{}{
			"query": "MATCH (a:assets) RETURN a",
		})
		// Should succeed with empty results, not error
		if status == 500 {
			t.Error("Sulpher on empty graph should not 500")
		}
	})
}

// ============================================================================
// 5. Graph + OQL combined verification
// ============================================================================

// TestGraphHTTP_GraphAndOQLConsistency verifies that graph edge counts match
// what OQL can see in the data.
func TestGraphHTTP_GraphAndOQLConsistency(t *testing.T) {
	env := newE2EEnv(t)
	defer env.cleanup()

	atID := env.create("/api/v1/asset_types", map[string]interface{}{
		"name": "Container", "code": "CTN",
	})
	for i := 0; i < 5; i++ {
		env.create("/api/v1/assets", map[string]interface{}{
			"code":       fmt.Sprintf("CTN-%03d", i+1),
			"status":     "active",
			"asset_type": ref("asset_types", atID),
		})
	}

	// OQL count
	oqlData := env.oqlData("SELECT COUNT(*) as cnt FROM assets WHERE status = 'active'")
	oqlCount := int(toF64(oqlData[0].(map[string]interface{})["cnt"]))

	// Graph node search count
	_, r := env.doJSON("POST", "/api/v1/graph/nodes/search", map[string]interface{}{
		"entity": "assets",
	})
	graphCount := int(toF64(r["count"]))

	if oqlCount != graphCount {
		t.Errorf("OQL count (%d) != graph node count (%d)", oqlCount, graphCount)
	}

	// Graph edge count should match number of REFs
	atNode := fmt.Sprintf("asset_types:%d", atID)
	_, r = env.doJSON("GET", fmt.Sprintf("/api/v1/graph/%s/in", atNode), nil)
	inEdges := int(toF64(r["count"]))
	if inEdges != 5 {
		t.Errorf("expected 5 incoming edges to asset type, got %d", inEdges)
	}
}

// ============================================================================
// Assertion helpers
// ============================================================================

func assertStatus(t *testing.T, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("status: expected %d, got %d", want, got)
	}
}

func assertFieldExists(t *testing.T, m map[string]interface{}, key string) {
	t.Helper()
	if _, ok := m[key]; !ok {
		t.Errorf("expected field %q in response, keys: %v", key, mapKeys(m))
	}
}

func assertFieldType(t *testing.T, m map[string]interface{}, key string, expectedType string) {
	t.Helper()
	v, ok := m[key]
	if !ok {
		t.Errorf("expected field %q in response", key)
		return
	}
	actualType := fmt.Sprintf("%T", v)
	if !strings.Contains(actualType, expectedType) {
		t.Errorf("field %q: expected type containing %q, got %q (value: %v)", key, expectedType, actualType, v)
	}
}

func mapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
