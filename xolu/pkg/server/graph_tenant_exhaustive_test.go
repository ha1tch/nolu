// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Exhaustive isolation tests for the tenant-scoped graph subsystem.
//
// Design principle: every test in this file is adversarial. It is not
// enough to show that tenant A gets correct results; each test also
// confirms that data from tenant B cannot appear in tenant A's results.
//
// Threat vectors covered:
//
//  1. Same entity type + same numeric ID in both tenants (maximum collision).
//     post:1 exists for alpha AND beta. After prefix stripping both look
//     identical to the client. The handler MUST add the prefix before any
//     graph lookup.
//
//  2. All 12 handler surfaces tested individually.
//
//  3. Multi-hop traversal: chain A→B→C→D in one tenant; traversal must not
//     escape to the other tenant's chain, even when both chains share the
//     same node type labels and numeric IDs.
//
//  4. Cross-tenant reachability: a node in tenant A must return 404 / empty
//     when queried from tenant B's routes.
//
//  5. commonNeighbors: two tenants each have a "diamond" graph; confirm no
//     cross-contamination of the neighbor sets.
//
//  6. Sulpher sync: MATCH (n) RETURN n returns only the submitting tenant's
//     nodes; node IDs in results carry no XXXX@ prefix.
//
//  7. Sulpher async: full submit→poll→result cycle confirms isolation.
//
//  8. NodeInfo Entity field: GetNodeInfo parses Entity from the raw prefixed
//     ID; the handler must strip the prefix from that field too.
//
//  9. stats, degree, shortestPath, pathExists, path, neighbors all return
//     correct counts/paths using only the requesting tenant's data.

package server

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Shared fixture
//
// We build one "dense" two-tenant world once per test (via
// newGraphTenantServer + seedGraphEntity, both defined in
// graph_tenant_isolation_test.go).
//
// Alpha layout (IDs intentionally overlap with beta):
//   author:1
//   author:2
//   post:1  --[author_ref]--> author:1
//   post:2  --[author_ref]--> author:2
//   post:3  --[author_ref]--> author:1   (author:1 has two incoming edges)
//   tag:1
//   post:1  --[tag_ref]-->    tag:1
//   comment:1  --[post_ref]--> post:1
//
// Alpha has 7 nodes, 5 edges.
//
// Beta layout (same type names and numeric IDs — maximum collision risk):
//   author:1
//   author:2
//   post:1  --[author_ref]--> author:1
//   post:2  --[author_ref]--> author:2
//   tag:1
//   post:1  --[tag_ref]-->    tag:1
//
// Beta has 5 nodes, 3 edges.
// ---------------------------------------------------------------------------

func seedBlogFixture(t *testing.T, s *Server) {
	t.Helper()

	// --- alpha ---
	seedGraphEntity(t, s, "alpha", "author", 1, map[string]interface{}{"name": "Alice"})
	seedGraphEntity(t, s, "alpha", "author", 2, map[string]interface{}{"name": "Bob"})
	seedGraphEntity(t, s, "alpha", "tag", 1, map[string]interface{}{"label": "go"})
	seedGraphEntity(t, s, "alpha", "post", 1, map[string]interface{}{
		"title":      "olu intro",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
		"tag_ref":    map[string]interface{}{"type": "REF", "entity": "tag", "id": 1},
	})
	seedGraphEntity(t, s, "alpha", "post", 2, map[string]interface{}{
		"title":      "graphs in olu",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 2},
	})
	seedGraphEntity(t, s, "alpha", "post", 3, map[string]interface{}{
		"title":      "tenancy deep dive",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
	})
	seedGraphEntity(t, s, "alpha", "comment", 1, map[string]interface{}{
		"body":     "great post",
		"post_ref": map[string]interface{}{"type": "REF", "entity": "post", "id": 1},
	})

	// --- beta (same types and IDs as alpha) ---
	seedGraphEntity(t, s, "beta", "author", 1, map[string]interface{}{"name": "Carol"})
	seedGraphEntity(t, s, "beta", "author", 2, map[string]interface{}{"name": "Dave"})
	seedGraphEntity(t, s, "beta", "tag", 1, map[string]interface{}{"label": "rust"})
	seedGraphEntity(t, s, "beta", "post", 1, map[string]interface{}{
		"title":      "beta post one",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 1},
		"tag_ref":    map[string]interface{}{"type": "REF", "entity": "tag", "id": 1},
	})
	seedGraphEntity(t, s, "beta", "post", 2, map[string]interface{}{
		"title":      "beta post two",
		"author_ref": map[string]interface{}{"type": "REF", "entity": "author", "id": 2},
	})
}

// ---------------------------------------------------------------------------
// 1. Stats — per-tenant counts only
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Stats(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	cases := []struct {
		tenant    string
		wantNodes float64
		wantEdges float64
	}{
		// alpha: 7 nodes (author×2, post×3, tag×1, comment×1), 5 edges
		{"alpha", 7, 5},
		// beta:  5 nodes (author×2, post×2, tag×1), 3 edges
		{"beta", 5, 3},
	}
	for _, c := range cases {
		c := c
		t.Run(c.tenant, func(t *testing.T) {
			w := tgDo(t, s, http.MethodGet, c.tenant, "/stats", nil)
			if w.Code != http.StatusOK {
				t.Fatalf("stats %s: %d — %s", c.tenant, w.Code, w.Body.String())
			}
			stats := decodeGraphJSON(t, w)
			if stats["node_count"].(float64) != c.wantNodes {
				t.Errorf("%s node_count: want %.0f got %v", c.tenant, c.wantNodes, stats["node_count"])
			}
			if stats["edge_count"].(float64) != c.wantEdges {
				t.Errorf("%s edge_count: want %.0f got %v", c.tenant, c.wantEdges, stats["edge_count"])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 2. NodeInfo — same ID in both tenants, verify correct data + no prefix leak
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_NodeInfo_SameIDCollision(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// Both tenants have post:1 and author:1.
	// Querying alpha's post:1 must yield alpha's outgoing edges, not beta's.

	checkInfo := func(tenant, nodeID string, wantOutCount int) {
		t.Helper()
		w := tgDo(t, s, http.MethodGet, tenant, "/nodes/"+nodeID, nil)
		if w.Code != http.StatusOK {
			t.Fatalf("nodeInfo %s/%s: %d — %s", tenant, nodeID, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)

		// id field must match exactly what we asked for
		if body["id"].(string) != nodeID {
			t.Errorf("%s nodeInfo id: want %q got %q", tenant, nodeID, body["id"])
		}

		// Entity must be stripped — e.g. "post", not "0001@post"
		entity := body["entity"].(string)
		if containsPrefix(entity) {
			t.Errorf("%s nodeInfo entity=%q contains XXXX@ prefix", tenant, entity)
		}
		// Entity must equal the type portion of nodeID
		typePart := strings.SplitN(nodeID, ":", 2)[0]
		if entity != typePart {
			t.Errorf("%s nodeInfo entity: want %q got %q", tenant, typePart, entity)
		}

		// Outgoing edge count must match wantOutCount
		outgoing := body["outgoing"].(map[string]interface{})
		if len(outgoing) != wantOutCount {
			t.Errorf("%s nodeInfo %s outgoing: want %d got %d: %v", tenant, nodeID, wantOutCount, len(outgoing), outgoing)
		}

		// None of the outgoing targets must contain a prefix
		for target := range outgoing {
			if containsPrefix(target) {
				t.Errorf("%s nodeInfo outgoing target %q contains XXXX@ prefix", tenant, target)
			}
		}

		// The full response body must not contain a raw prefix
		if containsPrefix(w.Body.String()) {
			t.Errorf("%s nodeInfo response contains XXXX@ prefix: %s", tenant, w.Body.String())
		}
	}

	// alpha post:1 has 2 outgoing edges (author_ref + tag_ref)
	checkInfo("alpha", "post:1", 2)
	// beta post:1 has 2 outgoing edges (author_ref + tag_ref)
	checkInfo("beta", "post:1", 2)

	// alpha post:2 has 1 outgoing edge (author_ref only)
	checkInfo("alpha", "post:2", 1)
	// beta post:2 has 1 outgoing edge (author_ref only)
	checkInfo("beta", "post:2", 1)

	// alpha has comment:1 which only exists in alpha — beta must 404
	w := tgDo(t, s, http.MethodGet, "beta", "/nodes/comment:1", nil)
	if w.Code != http.StatusNotFound {
		t.Errorf("beta requesting alpha-only comment:1: expected 404, got %d", w.Code)
	}
}

// ---------------------------------------------------------------------------
// 3. NodeDegree — same-ID collision, degree counts scoped to tenant
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_NodeDegree_SameIDCollision(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// alpha author:1 has 2 incoming edges (from post:1 and post:3)
	// beta  author:1 has 1 incoming edge  (from post:1 only)

	checkDegree := func(tenant, nodeID string, wantIn, wantOut int) {
		t.Helper()
		w := tgDo(t, s, http.MethodGet, tenant, "/nodes/"+nodeID+"/degree", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("degree %s/%s: %d — %s", tenant, nodeID, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		degree := body["degree"].(map[string]interface{})
		in := int(degree["in"].(float64))
		out := int(degree["out"].(float64))
		if in != wantIn {
			t.Errorf("%s degree %s in: want %d got %d", tenant, nodeID, wantIn, in)
		}
		if out != wantOut {
			t.Errorf("%s degree %s out: want %d got %d", tenant, nodeID, wantOut, out)
		}
		if containsPrefix(w.Body.String()) {
			t.Errorf("%s degree response contains XXXX@ prefix", tenant)
		}
	}

	checkDegree("alpha", "author:1", 2, 0)
	checkDegree("beta", "author:1", 1, 0)
	checkDegree("alpha", "post:1", 1, 2) // 1 incoming from comment:1, 2 outgoing
	checkDegree("beta", "post:1", 0, 2)  // beta has no comment entity
}

// ---------------------------------------------------------------------------
// 4. Outgoing and incoming edges — verify only tenant's edges, correct IDs
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_OutgoingEdges_SameIDCollision(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// alpha author:1 has NO outgoing edges; beta author:1 also has none.
	// alpha post:1 has edges to author:1 and tag:1 (within alpha).
	// beta  post:1 has edges to author:1 and tag:1 (within beta).
	// After stripping, the targets look the same — but they must be isolated.

	checkOut := func(tenant, nodeID string, wantTargets []string) {
		t.Helper()
		w := tgDo(t, s, http.MethodGet, tenant, "/"+nodeID+"/out", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("out %s/%s: %d — %s", tenant, nodeID, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		if body["node_id"].(string) != nodeID {
			t.Errorf("%s out node_id: want %q got %q", tenant, nodeID, body["node_id"])
		}
		edges := body["edges"].([]interface{})
		if len(edges) != len(wantTargets) {
			t.Errorf("%s out %s edge count: want %d got %d", tenant, nodeID, len(wantTargets), len(edges))
			return
		}
		seen := make(map[string]bool)
		for _, e := range edges {
			edge := e.(map[string]interface{})
			target := edge["target"].(string)
			seen[target] = true
			if containsPrefix(target) {
				t.Errorf("%s out target %q contains XXXX@ prefix", tenant, target)
			}
			if edge["source"].(string) != nodeID {
				t.Errorf("%s out source: want %q got %q", tenant, nodeID, edge["source"])
			}
		}
		for _, want := range wantTargets {
			if !seen[want] {
				t.Errorf("%s out %s: expected target %q not found in %v", tenant, nodeID, want, seen)
			}
		}
	}

	checkOut("alpha", "post:1", []string{"author:1", "tag:1"})
	checkOut("beta", "post:1", []string{"author:1", "tag:1"}) // same labels, different tenant data
	checkOut("alpha", "post:2", []string{"author:2"})
	checkOut("beta", "post:2", []string{"author:2"})

	// comment:1 only exists in alpha; requesting from beta must 404
	w := tgDo(t, s, http.MethodGet, "beta", "/comment:1/out", nil)
	// Graph returns empty or 404 — either is acceptable isolation; the key
	// thing is it must not return alpha's comment:1 data.
	if w.Code == http.StatusOK {
		body := decodeGraphJSON(t, w)
		if count := body["count"].(float64); count != 0 {
			t.Errorf("beta /comment:1/out returned %v edges — should be 0 or 404", count)
		}
	}
}

func TestTenantGraphExhaustive_IncomingEdges_SameIDCollision(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// alpha author:1 gets 2 incoming edges (from post:1 and post:3).
	// beta  author:1 gets 1 incoming edge  (from post:1 only).
	// If isolation is broken, beta would see alpha's extra edges.

	checkIn := func(tenant, nodeID string, wantCount int, wantSources []string) {
		t.Helper()
		w := tgDo(t, s, http.MethodGet, tenant, "/"+nodeID+"/in", nil)
		if w.Code != http.StatusOK {
			t.Fatalf("in %s/%s: %d — %s", tenant, nodeID, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		count := int(body["count"].(float64))
		if count != wantCount {
			t.Errorf("%s in %s count: want %d got %d", tenant, nodeID, wantCount, count)
			return
		}
		edges := body["edges"].([]interface{})
		seen := make(map[string]bool)
		for _, e := range edges {
			edge := e.(map[string]interface{})
			src := edge["source"].(string)
			seen[src] = true
			if containsPrefix(src) {
				t.Errorf("%s in source %q contains XXXX@ prefix", tenant, src)
			}
		}
		for _, want := range wantSources {
			if !seen[want] {
				t.Errorf("%s in %s: expected source %q not found in %v", tenant, nodeID, want, seen)
			}
		}
	}

	checkIn("alpha", "author:1", 2, []string{"post:1", "post:3"})
	checkIn("beta", "author:1", 1, []string{"post:1"})
	checkIn("alpha", "tag:1", 1, []string{"post:1"})
	checkIn("beta", "tag:1", 1, []string{"post:1"})
}

// ---------------------------------------------------------------------------
// 5. POST /neighbors — isolation of both directions
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Neighbors(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// direction is the request value ("in"/"out"); responseKey is the JSON key
	// the handler returns ("incoming"/"outgoing").
	checkNeighbors := func(tenant, nodeID, direction, responseKey string, wantKeys []string) {
		t.Helper()
		w := tgDo(t, s, http.MethodPost, tenant, "/neighbors",
			map[string]interface{}{"node_id": nodeID, "direction": direction})
		if w.Code != http.StatusOK {
			t.Fatalf("neighbors %s/%s/%s: %d — %s", tenant, nodeID, direction, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		neighbors := body["neighbors"].(map[string]interface{})
		sideRaw, ok := neighbors[responseKey]
		if !ok {
			t.Fatalf("%s neighbors %s: response key %q not present in %v", tenant, nodeID, responseKey, neighbors)
		}
		side := sideRaw.(map[string]interface{})
		for _, k := range wantKeys {
			if _, ok := side[k]; !ok {
				t.Errorf("%s neighbors %s %s: expected key %q, got %v", tenant, nodeID, direction, k, side)
			}
		}
		if len(side) != len(wantKeys) {
			t.Errorf("%s neighbors %s %s: want %d entries, got %d: %v", tenant, nodeID, direction, len(wantKeys), len(side), side)
		}
		for k := range side {
			if containsPrefix(k) {
				t.Errorf("%s neighbors key %q contains XXXX@ prefix", tenant, k)
			}
		}
		if containsPrefix(w.Body.String()) {
			t.Errorf("%s neighbors response contains XXXX@ prefix", tenant)
		}
	}

	// alpha author:1 has post:1 and post:3 as incoming; beta author:1 has only post:1.
	checkNeighbors("alpha", "author:1", "in", "incoming", []string{"post:1", "post:3"})
	checkNeighbors("beta", "author:1", "in", "incoming", []string{"post:1"})
	checkNeighbors("alpha", "post:1", "out", "outgoing", []string{"author:1", "tag:1"})
	checkNeighbors("beta", "post:1", "out", "outgoing", []string{"author:1", "tag:1"})
}

// ---------------------------------------------------------------------------
// 6. POST /path and /shortestPath — same endpoints, same collision risk
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Path_SameIDCollision(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// Path from comment:1 to author:1 exists in alpha (comment->post->author).
	// This path does NOT exist in beta because beta has no comment entity.

	// alpha: comment:1 → post:1 → author:1  (length 2)
	wAlpha := tgDo(t, s, http.MethodPost, "alpha", "/path",
		map[string]interface{}{"from": "comment:1", "to": "author:1", "max_depth": 5})
	if wAlpha.Code != http.StatusOK {
		t.Fatalf("alpha path: %d — %s", wAlpha.Code, wAlpha.Body.String())
	}
	alphaResult := decodeGraphJSON(t, wAlpha)
	if alphaResult["length"].(float64) != 2 {
		t.Errorf("alpha path length: want 2 got %v", alphaResult["length"])
	}
	pathNodes := alphaResult["path"].([]interface{})
	for _, n := range pathNodes {
		if containsPrefix(n.(string)) {
			t.Errorf("alpha path node %q contains XXXX@ prefix", n)
		}
	}

	// beta: same path request — must 404 or return no path
	wBeta := tgDo(t, s, http.MethodPost, "beta", "/path",
		map[string]interface{}{"from": "comment:1", "to": "author:1", "max_depth": 5})
	if wBeta.Code == http.StatusOK {
		// If it returned OK it must report no path — beta doesn't have comment:1
		betaResult := decodeGraphJSON(t, wBeta)
		if length, ok := betaResult["length"].(float64); ok && length > 0 {
			t.Errorf("beta found a path that doesn't exist in beta (length %v) — cross-tenant traversal", length)
		}
	}
}

func TestTenantGraphExhaustive_ShortestPath(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// Shortest path post:2 → author:2 should exist in both alpha and beta (length 1).
	for _, tenant := range []string{"alpha", "beta"} {
		tenant := tenant
		t.Run(tenant, func(t *testing.T) {
			w := tgDo(t, s, http.MethodPost, tenant, "/shortestPath",
				map[string]interface{}{"from": "post:2", "to": "author:2", "max_depth": 5})
			if w.Code != http.StatusOK {
				t.Fatalf("shortestPath %s: %d — %s", tenant, w.Code, w.Body.String())
			}
			body := decodeGraphJSON(t, w)
			if !body["exists"].(bool) {
				t.Errorf("%s shortestPath post:2→author:2: expected to exist", tenant)
			}
			if body["length"].(float64) != 1 {
				t.Errorf("%s shortestPath length: want 1 got %v", tenant, body["length"])
			}
			// Path nodes must be clean
			for _, n := range body["path"].([]interface{}) {
				if containsPrefix(n.(string)) {
					t.Errorf("%s shortestPath node %q contains XXXX@ prefix", tenant, n)
				}
			}
		})
	}

	// Cross-tenant: alpha asking for a path via beta-only topology should not exist.
	// post:3 only exists in alpha; asking beta for shortestPath post:3→author:1
	// should return exists=false.
	w := tgDo(t, s, http.MethodPost, "beta", "/shortestPath",
		map[string]interface{}{"from": "post:3", "to": "author:1", "max_depth": 5})
	if w.Code == http.StatusOK {
		body := decodeGraphJSON(t, w)
		if exists, ok := body["exists"].(bool); ok && exists {
			t.Error("beta found a path involving alpha's post:3 — cross-tenant traversal")
		}
	}
}

// ---------------------------------------------------------------------------
// 7. pathExists — same-ID stress
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_PathExists(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	cases := []struct {
		tenant    string
		from, to  string
		wantExist bool
	}{
		// alpha has comment:1 → post:1 → author:1
		{"alpha", "comment:1", "author:1", true},
		// beta has no comment entity at all
		{"beta", "comment:1", "author:1", false},
		// post:1 → author:1 works in both (both have this edge)
		{"alpha", "post:1", "author:1", true},
		{"beta", "post:1", "author:1", true},
		// post:3 only in alpha
		{"alpha", "post:3", "author:1", true},
		{"beta", "post:3", "author:1", false},
	}

	for _, c := range cases {
		c := c
		label := fmt.Sprintf("%s/%s→%s", c.tenant, c.from, c.to)
		t.Run(label, func(t *testing.T) {
			w := tgDo(t, s, http.MethodPost, c.tenant, "/pathExists",
				map[string]interface{}{"from": c.from, "to": c.to, "max_depth": 10})
			if w.Code != http.StatusOK && w.Code != http.StatusNotFound {
				t.Fatalf("%s pathExists: status %d — %s", label, w.Code, w.Body.String())
			}
			if w.Code == http.StatusOK {
				body := decodeGraphJSON(t, w)
				exists, _ := body["exists"].(bool)
				if exists != c.wantExist {
					t.Errorf("%s pathExists: want exists=%v got exists=%v — body: %s",
						label, c.wantExist, exists, w.Body.String())
				}
			} else if c.wantExist {
				t.Errorf("%s pathExists: expected exists=true but got %d", label, w.Code)
			}
			if containsPrefix(w.Body.String()) {
				t.Errorf("%s pathExists response contains XXXX@ prefix", label)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 8. commonNeighbors — diamond topology, no cross-tenant contamination
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_SharedOutNeighbors(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// alpha: post:1 and post:3 both point to author:1.
	// commonNeighbors(post:1, post:3) in alpha should return [author:1].
	w := tgDo(t, s, http.MethodPost, "alpha", "/commonNeighbors",
		map[string]interface{}{"node_a": "post:1", "node_b": "post:3"})
	if w.Code != http.StatusOK {
		t.Fatalf("alpha commonNeighbors: %d — %s", w.Code, w.Body.String())
	}
	body := decodeGraphJSON(t, w)
	common := body["common"].([]interface{})
	if len(common) != 1 {
		t.Errorf("alpha commonNeighbors post:1+post:3: want 1, got %d: %v", len(common), common)
	}
	if len(common) == 1 && common[0].(string) != "author:1" {
		t.Errorf("alpha commonNeighbors: want author:1 got %q", common[0])
	}
	for _, n := range common {
		if containsPrefix(n.(string)) {
			t.Errorf("alpha commonNeighbors result %q contains XXXX@ prefix", n)
		}
	}

	// beta has post:1 and post:2 pointing to author:1 and author:2 respectively —
	// no common neighbor. And beta has no post:3.
	wB := tgDo(t, s, http.MethodPost, "beta", "/commonNeighbors",
		map[string]interface{}{"node_a": "post:1", "node_b": "post:2"})
	if wB.Code != http.StatusOK {
		t.Fatalf("beta commonNeighbors: %d — %s", wB.Code, wB.Body.String())
	}
	bodyB := decodeGraphJSON(t, wB)
	commonB := bodyB["common"].([]interface{})
	if len(commonB) != 0 {
		t.Errorf("beta commonNeighbors post:1+post:2: want 0, got %d: %v", len(commonB), commonB)
	}

	// Requesting from beta with post:3 (alpha-only node) must not find alpha's data.
	wCross := tgDo(t, s, http.MethodPost, "beta", "/commonNeighbors",
		map[string]interface{}{"node_a": "post:1", "node_b": "post:3"})
	// Expected: 404 (post:3 not in beta) or empty common list.
	if wCross.Code == http.StatusOK {
		bodyCross := decodeGraphJSON(t, wCross)
		if count := bodyCross["count"].(float64); count != 0 {
			t.Errorf("beta commonNeighbors with alpha-only post:3: expected 0 common, got %v — isolation violated", count)
		}
	}
}

// ---------------------------------------------------------------------------
// 9. nodeSearch — entity-type filtering across tenants
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_NodeSearch_EntityFilter(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	cases := []struct {
		tenant    string
		entity    string
		wantCount int
	}{
		{"alpha", "post", 3},    // post:1, post:2, post:3
		{"beta", "post", 2},     // post:1, post:2
		{"alpha", "author", 2},  // author:1, author:2
		{"beta", "author", 2},   // author:1, author:2
		{"alpha", "comment", 1}, // comment:1
		{"beta", "comment", 0},  // beta has no comments
	}

	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%s/%s", c.tenant, c.entity), func(t *testing.T) {
			w := tgDo(t, s, http.MethodPost, c.tenant, "/nodes/search",
				map[string]interface{}{"entity": c.entity})
			if w.Code != http.StatusOK {
				t.Fatalf("search %s/%s: %d — %s", c.tenant, c.entity, w.Code, w.Body.String())
			}
			body := decodeGraphJSON(t, w)
			nodes := body["nodes"].([]interface{})
			if len(nodes) != c.wantCount {
				t.Errorf("%s search %s: want %d got %d: %v", c.tenant, c.entity, c.wantCount, len(nodes), nodes)
			}
			for _, n := range nodes {
				id := n.(string)
				if containsPrefix(id) {
					t.Errorf("%s search result %q contains XXXX@ prefix", c.tenant, id)
				}
				// Every returned ID must start with the requested entity type
				if !strings.HasPrefix(id, c.entity+":") {
					t.Errorf("%s search %s: result %q does not match entity type", c.tenant, c.entity, id)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 10. Multi-hop traversal — 4-node chain, must not escape tenant boundary
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_MultiHopTraversal(t *testing.T) {
	s := newGraphTenantServer(t)

	// Alpha: A→B→C→D (3 hops), using the same IDs as beta's chain.
	seedGraphEntity(t, s, "alpha", "hop", 1, map[string]interface{}{"v": "A"})
	seedGraphEntity(t, s, "alpha", "hop", 2, map[string]interface{}{
		"v": "B", "next_ref": map[string]interface{}{"type": "REF", "entity": "hop", "id": 1},
	})
	seedGraphEntity(t, s, "alpha", "hop", 3, map[string]interface{}{
		"v": "C", "next_ref": map[string]interface{}{"type": "REF", "entity": "hop", "id": 2},
	})
	seedGraphEntity(t, s, "alpha", "hop", 4, map[string]interface{}{
		"v": "D", "next_ref": map[string]interface{}{"type": "REF", "entity": "hop", "id": 3},
	})

	// Beta: completely independent chain with same node IDs.
	seedGraphEntity(t, s, "beta", "hop", 1, map[string]interface{}{"v": "W"})
	seedGraphEntity(t, s, "beta", "hop", 2, map[string]interface{}{
		"v": "X", "next_ref": map[string]interface{}{"type": "REF", "entity": "hop", "id": 1},
	})

	// Alpha: 4 hops node search
	w := tgDo(t, s, http.MethodPost, "alpha", "/nodes/search",
		map[string]interface{}{"entity": "hop"})
	if w.Code != http.StatusOK {
		t.Fatalf("alpha hop search: %d — %s", w.Code, w.Body.String())
	}
	body := decodeGraphJSON(t, w)
	nodes := body["nodes"].([]interface{})
	if len(nodes) != 4 {
		t.Errorf("alpha hop search: want 4, got %d: %v", len(nodes), nodes)
	}

	// Beta: 2 hop nodes only
	wB := tgDo(t, s, http.MethodPost, "beta", "/nodes/search",
		map[string]interface{}{"entity": "hop"})
	bodyB := decodeGraphJSON(t, wB)
	nodesB := bodyB["nodes"].([]interface{})
	if len(nodesB) != 2 {
		t.Errorf("beta hop search: want 2, got %d: %v", len(nodesB), nodesB)
	}

	// Alpha: path from hop:4 to hop:1 should exist (length 3)
	wPath := tgDo(t, s, http.MethodPost, "alpha", "/shortestPath",
		map[string]interface{}{"from": "hop:4", "to": "hop:1", "max_depth": 10})
	if wPath.Code != http.StatusOK {
		t.Fatalf("alpha multi-hop path: %d — %s", wPath.Code, wPath.Body.String())
	}
	pathBody := decodeGraphJSON(t, wPath)
	if !pathBody["exists"].(bool) {
		t.Fatal("alpha multi-hop path: expected to exist")
	}
	if pathBody["length"].(float64) != 3 {
		t.Errorf("alpha multi-hop path length: want 3 got %v", pathBody["length"])
	}
	pathNodes := pathBody["path"].([]interface{})
	for _, n := range pathNodes {
		if containsPrefix(n.(string)) {
			t.Errorf("multi-hop path node %q contains XXXX@ prefix", n)
		}
	}

	// Beta: path from hop:4 to hop:1 must NOT exist (hop:3 and hop:4 only in alpha)
	wCross := tgDo(t, s, http.MethodPost, "beta", "/shortestPath",
		map[string]interface{}{"from": "hop:4", "to": "hop:1", "max_depth": 10})
	if wCross.Code == http.StatusOK {
		crossBody := decodeGraphJSON(t, wCross)
		if exists, ok := crossBody["exists"].(bool); ok && exists {
			t.Error("beta found multi-hop path using alpha's nodes — cross-tenant traversal")
		}
	}
}

// ---------------------------------------------------------------------------
// 11. Sulpher sync — adversarial "match all" query
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Sulpher_MatchAll(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// "MATCH (n) RETURN n" should only return the requesting tenant's nodes.
	checkMatchAll := func(tenant string, wantCount int) {
		t.Helper()
		w := tgDo(t, s, http.MethodPost, tenant, "/query",
			map[string]interface{}{
				"query":     "MATCH (n) RETURN n",
				"max_depth": 1,
			})
		if w.Code != http.StatusOK {
			t.Fatalf("sulpher matchAll %s: %d — %s", tenant, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		var result []interface{}
		if raw := body["result"]; raw != nil {
			result = raw.([]interface{})
		}
		if len(result) != wantCount {
			t.Errorf("%s MATCH (n): want %d results, got %d", tenant, wantCount, len(result))
		}
		// Verify no prefixed IDs leak through
		if containsPrefix(w.Body.String()) {
			t.Errorf("%s sulpher matchAll response contains XXXX@ prefix: %s", tenant, w.Body.String())
		}
	}

	// alpha has 7 nodes, beta has 5. If isolation is broken, both would return
	// the full 12.
	checkMatchAll("alpha", 7)
	checkMatchAll("beta", 5)
}

// ---------------------------------------------------------------------------
// 12. Sulpher sync — typed query, confirm only tenant type nodes returned
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Sulpher_TypedQuery(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	cases := []struct {
		tenant    string
		query     string
		wantCount int
	}{
		{"alpha", "MATCH (p:post) RETURN p", 3},
		{"beta", "MATCH (p:post) RETURN p", 2},
		{"alpha", "MATCH (a:author) RETURN a", 2},
		{"beta", "MATCH (a:author) RETURN a", 2},
		// alpha has comment, beta does not
		{"alpha", "MATCH (c:comment) RETURN c", 1},
		{"beta", "MATCH (c:comment) RETURN c", 0},
	}

	for _, c := range cases {
		c := c
		t.Run(fmt.Sprintf("%s/%s", c.tenant, strings.Fields(c.query)[2]), func(t *testing.T) {
			w := tgDo(t, s, http.MethodPost, c.tenant, "/query",
				map[string]interface{}{"query": c.query, "max_depth": 3})
			if w.Code != http.StatusOK {
				t.Fatalf("sulpher %s %q: %d — %s", c.tenant, c.query, w.Code, w.Body.String())
			}
			body := decodeGraphJSON(t, w)
			var result []interface{}
			if raw := body["result"]; raw != nil {
				result = raw.([]interface{})
			}
			if len(result) != c.wantCount {
				t.Errorf("%s %q: want %d, got %d: %v", c.tenant, c.query, c.wantCount, len(result), result)
			}
			if containsPrefix(w.Body.String()) {
				t.Errorf("%s sulpher response contains XXXX@ prefix", c.tenant)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 13. Sulpher sync — traversal query, result _id fields are clean
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Sulpher_TraversalIDFields(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// Query: MATCH (p:post)-[:author_ref]->(a:author) RETURN p, a
	// This exercises the traversal path and returns full node objects
	// with _id fields — exactly where a prefix leak would surface.

	for _, tenant := range []string{"alpha", "beta"} {
		tenant := tenant
		t.Run(tenant, func(t *testing.T) {
			w := tgDo(t, s, http.MethodPost, tenant, "/query",
				map[string]interface{}{
					"query":     "MATCH (p:post)-[:author_ref]->(a:author) RETURN p, a",
					"max_depth": 3,
				})
			if w.Code != http.StatusOK {
				t.Fatalf("sulpher traversal %s: %d — %s", tenant, w.Code, w.Body.String())
			}
			body := decodeGraphJSON(t, w)
			var result []interface{}
			if raw := body["result"]; raw != nil {
				result = raw.([]interface{})
			}
			if len(result) == 0 {
				t.Errorf("%s sulpher traversal: expected results, got none", tenant)
			}
			for i, row := range result {
				m := row.(map[string]interface{})
				for varName, nodeRaw := range m {
					node, ok := nodeRaw.(map[string]interface{})
					if !ok {
						continue
					}
					if id, ok := node["_id"].(string); ok {
						if containsPrefix(id) {
							t.Errorf("%s row[%d] %s._id=%q contains XXXX@ prefix", tenant, i, varName, id)
						}
					}
				}
			}
			if containsPrefix(w.Body.String()) {
				t.Errorf("%s sulpher traversal body contains XXXX@ prefix", tenant)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// 14. Sulpher async — full submit→poll→result cycle, both tenants isolated
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_Sulpher_AsyncIsolation(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// Submit one async query per tenant, then retrieve both results.
	submitQuery := func(tenant, query string) string {
		t.Helper()
		w := tgDo(t, s, http.MethodPost, tenant, "/query/async",
			map[string]interface{}{"query": query, "max_depth": 3})
		if w.Code != http.StatusAccepted {
			t.Fatalf("async submit %s: %d — %s", tenant, w.Code, w.Body.String())
		}
		body := decodeGraphJSON(t, w)
		return body["query_id"].(string)
	}

	waitForResult := func(tenant, queryID string) map[string]interface{} {
		t.Helper()
		resultPath := fmt.Sprintf("/query/%s/result", queryID)
		deadline := time.Now().Add(10 * time.Second)
		for time.Now().Before(deadline) {
			w := tgDo(t, s, http.MethodGet, tenant, resultPath, nil)
			if w.Code == http.StatusOK {
				body := decodeGraphJSON(t, w)
				status := body["status"].(string)
				if status == "completed" || status == "failed" {
					return body
				}
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatalf("async query %s/%s did not complete within timeout", tenant, queryID)
		return nil
	}

	alphaID := submitQuery("alpha", "MATCH (p:post) RETURN p")
	betaID := submitQuery("beta", "MATCH (p:post) RETURN p")

	// Alpha job must not be visible to beta, and vice versa.
	// (Each tenant has its own JobManager.)
	wCross := tgDo(t, s, http.MethodGet, "beta",
		fmt.Sprintf("/query/%s/result", alphaID), nil)
	if wCross.Code == http.StatusOK {
		crossBody := decodeGraphJSON(t, wCross)
		if status, ok := crossBody["status"].(string); ok && (status == "completed" || status == "running") {
			t.Errorf("beta can see alpha's job %s — JobManagers are not isolated", alphaID)
		}
	}

	// Retrieve the correct results from each tenant.
	alphaResult := waitForResult("alpha", alphaID)
	betaResult := waitForResult("beta", betaID)

	if alphaResult["status"].(string) != "completed" {
		t.Fatalf("alpha async: expected completed, got %s", alphaResult["status"])
	}
	if betaResult["status"].(string) != "completed" {
		t.Fatalf("beta async: expected completed, got %s", betaResult["status"])
	}

	var alphaRows []interface{}
	if raw := alphaResult["result"]; raw != nil {
		alphaRows = raw.([]interface{})
	}
	var betaRows []interface{}
	if raw := betaResult["result"]; raw != nil {
		betaRows = raw.([]interface{})
	}

	if len(alphaRows) != 3 {
		t.Errorf("alpha async MATCH (p:post): want 3, got %d", len(alphaRows))
	}
	if len(betaRows) != 2 {
		t.Errorf("beta async MATCH (p:post): want 2, got %d", len(betaRows))
	}

	for i, row := range alphaRows {
		if m, ok := row.(map[string]interface{}); ok {
			for k, v := range m {
				if n, ok := v.(map[string]interface{}); ok {
					if id, _ := n["_id"].(string); containsPrefix(id) {
						t.Errorf("alpha async row[%d][%s]._id=%q has prefix", i, k, id)
					}
				}
			}
		}
	}
}

// ---------------------------------------------------------------------------
// 15. Query a node that belongs only to the other tenant — must not leak data
// ---------------------------------------------------------------------------

func TestTenantGraphExhaustive_ForeignNodeDoesNotLeak(t *testing.T) {
	s := newGraphTenantServer(t)
	seedBlogFixture(t, s)

	// comment:1 only exists in alpha.
	// Requesting it from beta through any handler must not return alpha's data.

	endpoints := []struct {
		method string
		path   string
		body   interface{}
	}{
		{http.MethodGet, "/nodes/comment:1", nil},
		{http.MethodGet, "/nodes/comment:1/degree", nil},
		{http.MethodGet, "/comment:1/out", nil},
		{http.MethodGet, "/comment:1/in", nil},
	}

	for _, ep := range endpoints {
		ep := ep
		t.Run(ep.method+" "+ep.path, func(t *testing.T) {
			w := tgDo(t, s, ep.method, "beta", ep.path, ep.body)
			// Must be 404 or return empty result — must NOT return alpha's data.
			if w.Code == http.StatusOK {
				body := decodeGraphJSON(t, w)
				// For /out and /in: count must be 0
				if count, ok := body["count"].(float64); ok && count > 0 {
					t.Errorf("beta %s %s returned %v — should be 0 or 404", ep.method, ep.path, count)
				}
				// Degree total must be 0 if node doesn't exist in beta
				if deg, ok := body["degree"].(map[string]interface{}); ok {
					if total := deg["total"].(float64); total > 0 {
						t.Errorf("beta degree for alpha-only comment:1: total=%v, should be 0", total)
					}
				}
			}
		})
	}
}
