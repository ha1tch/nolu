// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Supplementary tenant graph isolation tests covering scenarios not addressed
// by graph_tenant_isolation_test.go or graph_tenant_exhaustive_test.go:
//
//  1. Tenant 0 (empty prefix / single-tenant fallback): all routes must work
//     correctly when the tenant prefix is "" and must not interfere with
//     named tenants.
//
//  2. Three-tenant isolation at the HTTP layer: a third tenant's data must
//     not appear in either of the first two tenants' responses.
//
//  3. Neighbors "both" direction: the "both" option on POST /neighbors must
//     return in- and out-edges scoped to the requesting tenant only.
//
//  4. Relationship type strings in edge responses must never carry the
//     XXXX@ prefix (they are opaque labels, not node IDs, but this is an
//     explicit assertion to guard against future refactors).
//
//  5. Concurrent cross-tenant requests: two goroutines simultaneously
//     querying stats and paths from different tenants must each receive
//     only their own data.  Run with -race to catch data races.

package server

import (
	"context"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// newThreeTenantServer creates a strict-mode server with three pre-registered
// tenants: alpha (1), beta (2), gamma (3).
func newThreeTenantServer(t *testing.T) *Server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "three_tenant.db")
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
	for _, reg := range []struct {
		name string
		id   uint16
	}{
		{"alpha", 1}, {"beta", 2}, {"gamma", 3},
	} {
		if err := s.tenantRegistry.Register(ctx, reg.name, reg.id); err != nil {
			t.Fatalf("register %s: %v", reg.name, err)
		}
	}
	return s
}

// ---------------------------------------------------------------------------
// 1. Tenant 0 (empty-prefix / single-tenant fallback)
// ---------------------------------------------------------------------------

// newSingleTenantServer creates a server in "path" mode (not strict),
// allowing access to the default tenant-0 graph routes.
// In single-tenant deployments the graph routes are at
//   GET /api/v1/graph/stats  (not /tenant/…)
// rather than the tenant-scoped ones.  This test exercises what happens
// when a zero-ID tenant is used via the registry (which can happen when
// TenantMode is not strict and tenant ID 0 is the implicit default).
//
// We confirm that the empty-prefix helpers work correctly end-to-end by
// directly building a graph with un-prefixed node IDs and querying the
// ForTenant methods with an empty prefix.
// TestEmptyPrefix_GraphMethodsRejectCrossTenant verifies that all four
// tenant-scoped graph methods reject an empty prefix with ErrTenantRequired.
// An empty prefix previously returned cross-tenant data; that behaviour is
// now a security violation. This test is intentionally adversarial.
func TestEmptyPrefix_GraphMethodsRejectCrossTenant(t *testing.T) {
	g := graph.NewFlatGraph()

	for _, id := range []string{"post:1", "post:2", "author:1"} {
		nodeType := id[:strings.Index(id, ":")]
		if err := g.AddNode(id, nodeType); err != nil {
			t.Fatalf("AddNode %q: %v", id, err)
		}
	}
	_ = g.AddEdge("post:1", "author:1", "author_ref")
	_ = g.AddEdge("post:2", "author:1", "author_ref")

	t.Run("NodeCountForTenant", func(t *testing.T) {
		n, err := g.NodeCountForTenant("")
		if err == nil {
			t.Errorf("expected error for empty prefix, got nil (n=%d)", n)
		}
		if n != 0 {
			t.Errorf("expected zero count on error, got %d", n)
		}
	})

	t.Run("EdgeCountForTenant", func(t *testing.T) {
		e, err := g.EdgeCountForTenant("")
		if err == nil {
			t.Errorf("expected error for empty prefix, got nil (e=%d)", e)
		}
		if e != 0 {
			t.Errorf("expected zero count on error, got %d", e)
		}
	})

	t.Run("GetAllNodesForTenant", func(t *testing.T) {
		nodes, err := g.GetAllNodesForTenant("")
		if err == nil {
			t.Errorf("expected error for empty prefix, got nil (nodes=%v)", nodes)
		}
		if len(nodes) != 0 {
			t.Errorf("expected nil/empty nodes on error, got %v", nodes)
		}
	})

	t.Run("GetNodesByTypeForTenant", func(t *testing.T) {
		nodes, err := g.GetNodesByTypeForTenant("", "post")
		if err == nil {
			t.Errorf("expected error for empty prefix, got nil (nodes=%v)", nodes)
		}
		if len(nodes) != 0 {
			t.Errorf("expected nil/empty nodes on error, got %v", nodes)
		}
	})
}
// ---------------------------------------------------------------------------
// 2. Three-tenant isolation at the HTTP layer
// ---------------------------------------------------------------------------

func TestThreeTenants_HTTPIsolation(t *testing.T) {
	s := newThreeTenantServer(t)

	// Seed distinct data per tenant, again using colliding IDs.
	// gamma gets its own unique set plus a colliding post:1.
	seedGraphEntity(t, s, "alpha", "post", 1, map[string]interface{}{"t": "alpha"})
	seedGraphEntity(t, s, "alpha", "post", 2, map[string]interface{}{"t": "alpha"})
	seedGraphEntity(t, s, "beta", "post", 1, map[string]interface{}{"t": "beta"})
	seedGraphEntity(t, s, "gamma", "post", 1, map[string]interface{}{"t": "gamma"})
	seedGraphEntity(t, s, "gamma", "post", 2, map[string]interface{}{"t": "gamma"})
	seedGraphEntity(t, s, "gamma", "post", 3, map[string]interface{}{"t": "gamma"})

	cases := []struct {
		tenant    string
		wantNodes float64
	}{
		{"alpha", 2},
		{"beta", 1},
		{"gamma", 3},
	}

	for _, c := range cases {
		c := c
		t.Run(c.tenant, func(t *testing.T) {
			w := tgDo(t, s, http.MethodGet, c.tenant, "/stats", nil)
			if w.Code != http.StatusOK {
				t.Fatalf("stats %s: %d — %s", c.tenant, w.Code, w.Body.String())
			}
			body := decodeGraphJSON(t, w)
			if body["node_count"].(float64) != c.wantNodes {
				t.Errorf("%s node_count: want %.0f got %v", c.tenant, c.wantNodes, body["node_count"])
			}
			if containsPrefix(w.Body.String()) {
				t.Errorf("%s stats contains XXXX@ prefix", c.tenant)
			}
		})
	}

	// nodeSearch: each tenant must only return its own post nodes.
	for tenant, wantCount := range map[string]int{"alpha": 2, "beta": 1, "gamma": 3} {
		tenant, wantCount := tenant, wantCount
		t.Run("search/"+tenant, func(t *testing.T) {
			w := tgDo(t, s, http.MethodPost, tenant, "/nodes/search",
				map[string]interface{}{"entity": "post"})
			if w.Code != http.StatusOK {
				t.Fatalf("search %s: %d", tenant, w.Code)
			}
			body := decodeGraphJSON(t, w)
			nodes := body["nodes"].([]interface{})
			if len(nodes) != wantCount {
				t.Errorf("%s post search: want %d, got %d: %v", tenant, wantCount, len(nodes), nodes)
			}
		})
	}

	// gamma must not see alpha's or beta's post:2 when looking up its own.
	// (alpha has post:2, beta does not, gamma has its own post:2.)
	wGamma := tgDo(t, s, http.MethodGet, "gamma", "/nodes/post:2", nil)
	if wGamma.Code != http.StatusOK {
		t.Fatalf("gamma post:2 nodeInfo: %d — %s", wGamma.Code, wGamma.Body.String())
	}
	// The "entity" field must be "post", not "0003@post" or "0001@post".
	body := decodeGraphJSON(t, wGamma)
	if body["entity"].(string) != "post" {
		t.Errorf("gamma post:2 entity: want \"post\", got %q", body["entity"])
	}
}

// ---------------------------------------------------------------------------
// 3. Neighbors "both" direction
// ---------------------------------------------------------------------------

func TestTenantNeighborsBothDirection(t *testing.T) {
	s := newGraphTenantServer(t)

	// Build a node that has both incoming and outgoing edges.
	// left:1 --[middle_ref]--> middle:1 --[right_ref]--> right:1
	seedGraphEntity(t, s, "alpha", "right", 1, map[string]interface{}{"v": "R"})
	seedGraphEntity(t, s, "alpha", "middle", 1, map[string]interface{}{
		"right_ref": map[string]interface{}{"type": "REF", "entity": "right", "id": 1},
	})
	seedGraphEntity(t, s, "alpha", "left", 1, map[string]interface{}{
		"v":          "L",
		"middle_ref": map[string]interface{}{"type": "REF", "entity": "middle", "id": 1},
	})

	// Same topology in beta.
	seedGraphEntity(t, s, "beta", "right", 1, map[string]interface{}{"v": "BR"})
	seedGraphEntity(t, s, "beta", "middle", 1, map[string]interface{}{
		"right_ref": map[string]interface{}{"type": "REF", "entity": "right", "id": 1},
	})
	seedGraphEntity(t, s, "beta", "left", 1, map[string]interface{}{
		"v":          "BL",
		"middle_ref": map[string]interface{}{"type": "REF", "entity": "middle", "id": 1},
	})

	checkBoth := func(tenant, nodeID string, wantTotal int) {
		t.Helper()
		w := tgDo(t, s, http.MethodPost, tenant, "/neighbors",
			map[string]interface{}{"node_id": nodeID, "direction": "both"})
		if w.Code != http.StatusOK {
			t.Fatalf("neighbors both %s/%s: %d — %s", tenant, nodeID, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		neighbors := body["neighbors"].(map[string]interface{})

		total := 0
		for _, side := range []string{"incoming", "outgoing"} {
			if sideMap, ok := neighbors[side].(map[string]interface{}); ok {
				total += len(sideMap)
				for k := range sideMap {
					if containsPrefix(k) {
						t.Errorf("%s neighbors both %s: key %q contains XXXX@ prefix", tenant, nodeID, k)
					}
				}
			}
		}
		if total != wantTotal {
			t.Errorf("%s neighbors both %s: want %d total, got %d — body: %s",
				tenant, nodeID, wantTotal, total, body)
		}
		if containsPrefix(w.Body.String()) {
			t.Errorf("%s neighbors both response contains XXXX@ prefix", tenant)
		}
	}

	// middle:1 has 1 incoming (from left:1) and 1 outgoing (to right:1) = 2 total.
	checkBoth("alpha", "middle:1", 2)
	checkBoth("beta", "middle:1", 2)

	// left:1 has 0 incoming and 1 outgoing (to middle:1).
	checkBoth("alpha", "left:1", 1)
	checkBoth("beta", "left:1", 1)
}

// ---------------------------------------------------------------------------
// 4. Relationship type strings must never carry the XXXX@ prefix
// ---------------------------------------------------------------------------

func TestRelationshipTypeIsNeverPrefixed(t *testing.T) {
	s := newGraphTenantServer(t)

	// Seed two tenants with the same edge relationship labels.
	seedGraphEntity(t, s, "alpha", "author", 1, map[string]interface{}{"name": "Alice"})
	seedGraphEntity(t, s, "alpha", "post", 1, map[string]interface{}{
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})
	seedGraphEntity(t, s, "beta", "author", 1, map[string]interface{}{"name": "Bob"})
	seedGraphEntity(t, s, "beta", "post", 1, map[string]interface{}{
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})

	checkRelationships := func(tenant string) {
		t.Helper()

		// /out returns edges with a "relationship" or "rel" field per edge.
		w := tgDo(t, s, http.MethodGet, tenant, "/post:1/out", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("out %s/post:1: %d", tenant, w.Code)
		}
		body := decodeGraphJSON(t, w)
		edges := body["edges"].([]interface{})
		for _, e := range edges {
			edge := e.(map[string]interface{})
			// The relationship field may be called "relationship" or "rel".
			for _, key := range []string{"relationship", "rel", "type"} {
				if rel, ok := edge[key].(string); ok {
					if containsPrefix(rel) {
						t.Errorf("%s edge relationship field %q = %q contains XXXX@ prefix",
							tenant, key, rel)
					}
				}
			}
		}

		// /nodes/{id} includes outgoing/incoming as maps: nodeID -> relationship.
		wInfo := tgDo(t, s, http.MethodGet, tenant, "/nodes/post:1", nil)
		if wInfo.Code != http.StatusOK {
			t.Fatalf("nodeInfo %s/post:1: %d", tenant, wInfo.Code)
		}
		infoBody := decodeGraphJSON(t, wInfo)
		for _, dirKey := range []string{"outgoing", "incoming"} {
			if m, ok := infoBody[dirKey].(map[string]interface{}); ok {
				for nodeID, relRaw := range m {
					rel := relRaw.(string)
					if containsPrefix(rel) {
						t.Errorf("%s nodeInfo %s[%q] = %q contains XXXX@ prefix",
							tenant, dirKey, nodeID, rel)
					}
				}
			}
		}
	}

	checkRelationships("alpha")
	checkRelationships("beta")
}

// ---------------------------------------------------------------------------
// 5. Concurrent cross-tenant requests
// ---------------------------------------------------------------------------

func TestConcurrentCrossTenantRequests(t *testing.T) {
	s := newGraphTenantServer(t)

	// Seed distinct data in each tenant.
	for i := 1; i <= 5; i++ {
		seedGraphEntity(t, s, "alpha", "post", i, map[string]interface{}{"n": i})
	}
	for i := 1; i <= 3; i++ {
		seedGraphEntity(t, s, "beta", "post", i, map[string]interface{}{"n": i})
	}

	const goroutines = 20
	const iterations = 10

	errs := make(chan string, goroutines*iterations)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		tenant := "alpha"
		wantNodes := float64(5)
		if g%2 == 1 {
			tenant = "beta"
			wantNodes = 3
		}
		wg.Add(1)
		go func(tenant string, wantNodes float64) {
			defer wg.Done()
			for i := 0; i < iterations; i++ {
				w := tgDo(t, s, http.MethodGet, tenant, "/stats", nil)
				if w.Code != http.StatusOK {
					errs <- fmt.Sprintf("%s stats: %d", tenant, w.Code)
					continue
				}
				body := decodeGraphJSON(t, w)
				if body["node_count"].(float64) != wantNodes {
					errs <- fmt.Sprintf("%s node_count: want %.0f got %v",
						tenant, wantNodes, body["node_count"])
				}
			}
		}(tenant, wantNodes)
	}

	wg.Wait()
	close(errs)

	for msg := range errs {
		t.Error(msg)
	}
}

// ---------------------------------------------------------------------------
// 6. Sulpher concurrent cross-tenant queries
// ---------------------------------------------------------------------------

func TestConcurrentSulpherCrossTenant(t *testing.T) {
	s := newGraphTenantServer(t)

	for i := 1; i <= 4; i++ {
		seedGraphEntity(t, s, "alpha", "item", i, map[string]interface{}{"n": i})
	}
	for i := 1; i <= 2; i++ {
		seedGraphEntity(t, s, "beta", "item", i, map[string]interface{}{"n": i})
	}

	const goroutines = 16
	errs := make(chan string, goroutines)
	var wg sync.WaitGroup

	for g := 0; g < goroutines; g++ {
		tenant := "alpha"
		wantCount := 4
		if g%2 == 1 {
			tenant = "beta"
			wantCount = 2
		}
		wg.Add(1)
		go func(tenant string, wantCount int) {
			defer wg.Done()
			w := tgDo(t, s, http.MethodPost, tenant, "/query",
				map[string]interface{}{"query": "MATCH (n:item) RETURN n", "max_depth": 1})
			if w.Code != http.StatusOK {
				errs <- fmt.Sprintf("%s sulpher: %d", tenant, w.Code)
				return
			}
			body := decodeGraphJSON(t, w)
			var result []interface{}
			if raw := body["result"]; raw != nil {
				result = raw.([]interface{})
			}
			if len(result) != wantCount {
				errs <- fmt.Sprintf("%s MATCH (n:item): want %d, got %d",
					tenant, wantCount, len(result))
			}
			if containsPrefix(w.Body.String()) {
				errs <- fmt.Sprintf("%s sulpher response contains XXXX@ prefix", tenant)
			}
		}(tenant, wantCount)
	}

	wg.Wait()
	close(errs)

	for msg := range errs {
		t.Error(msg)
	}
}
