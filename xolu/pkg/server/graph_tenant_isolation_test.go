// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Integration tests for tenant-scoped graph operations.
//
// Verifies:
//   - GraphEnabled stays true in strict mode (no longer force-disabled).
//   - Tenant-scoped graph routes exist and respond.
//   - Each tenant sees only its own nodes, edges, and stats.
//   - Cross-tenant path traversal is blocked.
//   - Sulpher queries respect tenant boundaries.
//   - API responses never expose the internal XXXX@ node ID prefix.

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/rs/zerolog"
)

// newGraphTenantServer creates a test server in strict mode with graph enabled
// and two pre-registered tenants: "alpha" (ID 1) and "beta" (ID 2).
func newGraphTenantServer(t *testing.T) *Server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "graph_tenant.db")

	baseStore, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:         "sqlite",
		DBPath:       dbPath,
		GraphEnabled: false,
		TenantID:     0,
	})
	if err != nil {
		t.Fatalf("base store: %v", err)
	}
	t.Cleanup(func() { baseStore.Close() })

	cfg := config.Default()
	cfg.StorageType = "sqlite"
	cfg.DBPath = dbPath
	cfg.GraphEnabled = true
	cfg.TenantMode = "strict"
	cfg.TenantAutoRegister = false
	cfg.AuthType = "none"
	cfg.MaxEntitySize = 1 << 20
	cfg.DefaultPageSize = 100
	cfg.MaxEmbedDepth = 0
	cfg.RefEmbedDepth = 0
	cfg.MaxQueryDepth = 10
	cfg.GraphQueryTTL = 30
	cfg.GraphMaxVisitedNodes = 10000
	cfg.QueryTimeout = 30

	logger := zerolog.Nop()
	g := graph.NewFlatGraph()
	s := New(cfg, baseStore, &noopCache{}, g, nil, &noopValidator{}, logger)

	ctx := context.Background()
	if err := s.tenantRegistry.Register(ctx, "alpha", 1); err != nil {
		t.Fatalf("register alpha: %v", err)
	}
	if err := s.tenantRegistry.Register(ctx, "beta", 2); err != nil {
		t.Fatalf("register beta: %v", err)
	}

	return s
}

// tgDo sends a request to a tenant-scoped graph endpoint.
func tgDo(t *testing.T, s *Server, method, tenant, path string, body interface{}) *httptest.ResponseRecorder {
	t.Helper()
	var buf *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		buf = bytes.NewBuffer(b)
	} else {
		buf = bytes.NewBuffer(nil)
	}
	url := fmt.Sprintf("/api/v1/tenant/%s/graph%s", tenant, path)
	req := httptest.NewRequest(method, url, buf)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	return w
}

// seedGraphEntity creates an entity (possibly with REFs) under a given tenant.
func seedGraphEntity(t *testing.T, s *Server, tenant, entityType string, id int, data map[string]interface{}) {
	t.Helper()
	b, _ := json.Marshal(data)
	url := fmt.Sprintf("/api/v1/tenant/%s/%s/save/%d", tenant, entityType, id)
	req := httptest.NewRequest(http.MethodPost, url, bytes.NewBuffer(b))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	s.router.ServeHTTP(w, req)
	if w.Code != http.StatusCreated && w.Code != http.StatusOK {
		t.Fatalf("seedGraphEntity %s/%s/%d: status %d — %s", tenant, entityType, id, w.Code, w.Body.String())
	}
}

// decodeGraphJSON decodes a recorder body into a map.
func decodeGraphJSON(t *testing.T, w *httptest.ResponseRecorder) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatalf("JSON decode: %v — body: %s", err, w.Body.String())
	}
	return m
}

// containsPrefix checks whether the string contains a XXXX@ style prefix.
func containsPrefix(body string) bool {
	for i := 0; i < len(body)-5; i++ {
		if body[i+4] == '@' {
			isHex := true
			for j := i; j < i+4; j++ {
				c := body[j]
				if !((c >= '0' && c <= '9') || (c >= 'A' && c <= 'F') || (c >= 'a' && c <= 'f')) {
					isHex = false
					break
				}
			}
			if isHex {
				return true
			}
		}
	}
	return false
}

// ----------------------------------------------------------------------------

// TestTenantGraph_StrictModeDoesNotForceGraphOff confirms that GraphEnabled is
// no longer overridden to false when TenantMode="strict".
func TestTenantGraph_StrictModeDoesNotForceGraphOff(t *testing.T) {
	cfg := config.Default()
	cfg.StorageType = "sqlite"
	cfg.DBPath = ":memory:"
	cfg.TenantMode = "strict"
	cfg.GraphEnabled = true
	cfg.GraphQueryTTL = 30

	_, err := cfg.Validate()
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !cfg.GraphEnabled {
		t.Fatal("GraphEnabled must remain true in strict mode — isolation is now enforced at the API layer")
	}
}

// TestTenantGraph_StatsIsolation verifies that each tenant's stats only count
// their own nodes and edges.
func TestTenantGraph_StatsIsolation(t *testing.T) {
	s := newGraphTenantServer(t)

	// Alpha: post:1 -> author:1 (1 edge, 2 nodes)
	seedGraphEntity(t, s, "alpha", "author", 1, map[string]interface{}{"name": "Alice"})
	seedGraphEntity(t, s, "alpha", "post", 1, map[string]interface{}{
		"title":      "Hello",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})

	// Beta: product:1 -> category:1 (1 edge, 2 nodes)
	seedGraphEntity(t, s, "beta", "product", 1, map[string]interface{}{"name": "Widget"})
	seedGraphEntity(t, s, "beta", "category", 1, map[string]interface{}{
		"name":        "Electronics",
		"product_ref": map[string]interface{}{"type": "REF", "entity": "product", "id": 1},
	})

	wAlpha := tgDo(t, s, http.MethodGet, "alpha", "/stats", nil)
	if wAlpha.Code != http.StatusOK {
		t.Fatalf("alpha stats: %d — %s", wAlpha.Code, wAlpha.Body.String())
	}
	alphaStats := decodeGraphJSON(t, wAlpha)
	if alphaStats["node_count"].(float64) != 2 {
		t.Errorf("alpha node_count: expected 2, got %v", alphaStats["node_count"])
	}
	if alphaStats["edge_count"].(float64) != 1 {
		t.Errorf("alpha edge_count: expected 1, got %v", alphaStats["edge_count"])
	}

	wBeta := tgDo(t, s, http.MethodGet, "beta", "/stats", nil)
	if wBeta.Code != http.StatusOK {
		t.Fatalf("beta stats: %d — %s", wBeta.Code, wBeta.Body.String())
	}
	betaStats := decodeGraphJSON(t, wBeta)
	if betaStats["node_count"].(float64) != 2 {
		t.Errorf("beta node_count: expected 2, got %v", betaStats["node_count"])
	}
}

// TestTenantGraph_EdgeTraversal verifies that out/in edge queries return clean
// client-facing node IDs and only the tenant's own edges.
func TestTenantGraph_EdgeTraversal(t *testing.T) {
	s := newGraphTenantServer(t)

	seedGraphEntity(t, s, "alpha", "author", 1, map[string]interface{}{"name": "Alice"})
	seedGraphEntity(t, s, "alpha", "post", 1, map[string]interface{}{
		"title":      "olu intro",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})
	// Beta seed — must not bleed through.
	seedGraphEntity(t, s, "beta", "author", 1, map[string]interface{}{"name": "Bob"})
	seedGraphEntity(t, s, "beta", "post", 1, map[string]interface{}{
		"title":      "other post",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})

	// Outgoing from post:1 (alpha)
	wOut := tgDo(t, s, http.MethodGet, "alpha", "/post:1/out", nil)
	if wOut.Code != http.StatusOK {
		t.Fatalf("alpha out: %d — %s", wOut.Code, wOut.Body.String())
	}
	outBody := decodeGraphJSON(t, wOut)
	if outBody["node_id"].(string) != "post:1" {
		t.Errorf("node_id: expected 'post:1', got %q", outBody["node_id"])
	}
	if outBody["count"].(float64) != 1 {
		t.Errorf("edge count: expected 1, got %v", outBody["count"])
	}
	edges := outBody["edges"].([]interface{})
	edge := edges[0].(map[string]interface{})
	if edge["target"].(string) != "author:1" {
		t.Errorf("target: expected 'author:1', got %q", edge["target"])
	}
	if edge["relationship"].(string) != "author_ref" {
		t.Errorf("relationship: expected 'author_ref', got %q", edge["relationship"])
	}

	// Incoming to author:1 (alpha) — exactly 1 edge from post:1.
	wIn := tgDo(t, s, http.MethodGet, "alpha", "/author:1/in", nil)
	if wIn.Code != http.StatusOK {
		t.Fatalf("alpha in: %d — %s", wIn.Code, wIn.Body.String())
	}
	inBody := decodeGraphJSON(t, wIn)
	if inBody["count"].(float64) != 1 {
		t.Errorf("incoming edges: expected 1, got %v — possible cross-tenant bleed", inBody["count"])
	}
}

// TestTenantGraph_NoCrossTenantPath verifies that a path query cannot traverse
// into another tenant's subgraph.
func TestTenantGraph_NoCrossTenantPath(t *testing.T) {
	s := newGraphTenantServer(t)

	// Alpha: node:1 -> node:2
	seedGraphEntity(t, s, "alpha", "node", 1, map[string]interface{}{"v": "a"})
	seedGraphEntity(t, s, "alpha", "node", 2, map[string]interface{}{
		"v":        "b",
		"prev_ref": map[string]interface{}{"type": "REF", "entity": "node", "id": 1},
	})

	// Beta: node:3 -> node:4
	seedGraphEntity(t, s, "beta", "node", 3, map[string]interface{}{"v": "c"})
	seedGraphEntity(t, s, "beta", "node", 4, map[string]interface{}{
		"v":        "d",
		"prev_ref": map[string]interface{}{"type": "REF", "entity": "node", "id": 3},
	})

	// Alpha: path from node:1 to beta's node:4 — must not exist.
	w := tgDo(t, s, http.MethodPost, "alpha", "/pathExists",
		map[string]interface{}{"from": "node:1", "to": "node:4", "max_depth": 10})
	if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
		t.Fatalf("pathExists: %d — %s", w.Code, w.Body.String())
	}
	result := decodeGraphJSON(t, w)
	if exists, ok := result["exists"].(bool); ok && exists {
		t.Error("cross-tenant path found — isolation violated")
	}
}

// TestTenantGraph_NodeSearchReturnsCleanIDs verifies that the node search
// endpoint returns client-facing IDs with no XXXX@ prefix.
func TestTenantGraph_NodeSearchReturnsCleanIDs(t *testing.T) {
	s := newGraphTenantServer(t)

	seedGraphEntity(t, s, "alpha", "user", 1, map[string]interface{}{"name": "Alice"})
	seedGraphEntity(t, s, "alpha", "user", 2, map[string]interface{}{"name": "Bob"})

	w := tgDo(t, s, http.MethodPost, "alpha", "/nodes/search",
		map[string]interface{}{"entity": "user"})
	if w.Code != http.StatusOK {
		t.Fatalf("node search: %d — %s", w.Code, w.Body.String())
	}
	body := decodeGraphJSON(t, w)
	nodes := body["nodes"].([]interface{})

	if len(nodes) != 2 {
		t.Errorf("expected 2 user nodes, got %d: %v", len(nodes), nodes)
	}
	for _, n := range nodes {
		id := n.(string)
		if containsPrefix(id) {
			t.Errorf("node ID %q contains XXXX@ prefix — should be stripped", id)
		}
	}
}

// TestTenantGraph_SulpherIsolation verifies that a Sulpher query submitted
// under a tenant's route only traverses that tenant's nodes.
func TestTenantGraph_SulpherIsolation(t *testing.T) {
	s := newGraphTenantServer(t)

	seedGraphEntity(t, s, "alpha", "author", 1, map[string]interface{}{"name": "Alice"})
	seedGraphEntity(t, s, "alpha", "post", 1, map[string]interface{}{
		"title":      "olu intro",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})
	// Beta: two posts — must not appear in alpha's results.
	seedGraphEntity(t, s, "beta", "post", 1, map[string]interface{}{"title": "beta post"})
	seedGraphEntity(t, s, "beta", "post", 2, map[string]interface{}{"title": "beta post 2"})

	w := tgDo(t, s, http.MethodPost, "alpha", "/query",
		map[string]interface{}{
			"query":     "MATCH (p:post) RETURN p",
			"max_depth": 3,
		})
	if w.Code != http.StatusOK {
		t.Fatalf("sulpher query: %d — %s", w.Code, w.Body.String())
	}
	body := decodeGraphJSON(t, w)
	result, ok := body["result"].([]interface{})
	if !ok {
		t.Fatalf("expected result array, got: %T %v", body["result"], body["result"])
	}
	if len(result) != 1 {
		t.Errorf("expected 1 post from alpha Sulpher query, got %d", len(result))
	}
}

// TestTenantGraph_NoPrefixInAPIResponses verifies that the internal XXXX@
// node ID prefix never appears in any tenant-scoped graph API response.
func TestTenantGraph_NoPrefixInAPIResponses(t *testing.T) {
	s := newGraphTenantServer(t)

	seedGraphEntity(t, s, "alpha", "item", 1, map[string]interface{}{"x": 1})
	seedGraphEntity(t, s, "alpha", "item", 2, map[string]interface{}{
		"x":        2,
		"link_ref": map[string]interface{}{"type": "REF", "entity": "item", "id": 1},
	})

	checks := []struct {
		label  string
		method string
		path   string
		body   interface{}
	}{
		{"stats", http.MethodGet, "/stats", nil},
		{"out", http.MethodGet, "/item:2/out", nil},
		{"in", http.MethodGet, "/item:1/in", nil},
		{"search", http.MethodPost, "/nodes/search", map[string]interface{}{"entity": "item"}},
		{"shortestPath", http.MethodPost, "/shortestPath",
			map[string]interface{}{"from": "item:2", "to": "item:1"}},
		{"pathExists", http.MethodPost, "/pathExists",
			map[string]interface{}{"from": "item:2", "to": "item:1"}},
	}

	for _, c := range checks {
		c := c
		t.Run(c.label, func(t *testing.T) {
			w := tgDo(t, s, c.method, "alpha", c.path, c.body)
			if containsPrefix(w.Body.String()) {
				t.Errorf("%s: response contains XXXX@ prefix: %s", c.label, w.Body.String())
			}
		})
	}
}
