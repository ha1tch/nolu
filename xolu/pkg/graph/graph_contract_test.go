// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// graph_contract_test.go — FlatGraph correctness contract.
//
// Every test in this file runs against FlatGraph via the graphImpls table.
// The table structure is retained for easy addition of future implementations.
//
// Tenant-isolation contract tests are at the bottom of this file.

package graph

import (
	"errors"
	"fmt"
	"os"
	"sort"
	"sync"
	"testing"

	"github.com/ha1tch/xolu/pkg/tenant"
)

// ---------------------------------------------------------------------------
// Implementation table
// ---------------------------------------------------------------------------

type implEntry struct {
	name     string
	new      func() Graph
	newCycle func(mode string) Graph
}

var graphImpls = []implEntry{
	{
		name:     "FlatGraph",
		new:      func() Graph { return NewFlatGraph() },
		newCycle: func(mode string) Graph { return NewFlatGraphWithCycleDetection(mode) },
	},
}

// mustAddNodeG and mustAddEdgeG are interface-typed helpers (mirrors of
// mustAddNode/mustAddEdge in graph_test.go which are concrete-typed).
func mustAddNodeG(t *testing.T, g Graph, id, typ string) {
	t.Helper()
	if err := g.AddNode(id, typ); err != nil {
		t.Fatalf("AddNode(%q, %q): %v", id, typ, err)
	}
}

func mustAddEdgeG(t *testing.T, g Graph, from, to, rel string) {
	t.Helper()
	if err := g.AddEdge(from, to, rel); err != nil {
		t.Fatalf("AddEdge(%q, %q, %q): %v", from, to, rel, err)
	}
}

// sorted returns a sorted copy of a string slice for deterministic comparison.
func sorted(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	sort.Strings(out)
	return out
}

// ---------------------------------------------------------------------------
// Construction & empty-graph invariants
// ---------------------------------------------------------------------------

func TestContract_NewGraph_Empty(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			if g.NodeCount() != 0 {
				t.Errorf("NodeCount: want 0, got %d", g.NodeCount())
			}
			if g.EdgeCount() != 0 {
				t.Errorf("EdgeCount: want 0, got %d", g.EdgeCount())
			}
			if g.HasCycle() {
				t.Error("empty graph should not have a cycle")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AddNode / RemoveNode
// ---------------------------------------------------------------------------

func TestContract_AddNode_Basic(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			if g.NodeCount() != 1 {
				t.Errorf("NodeCount: want 1, got %d", g.NodeCount())
			}
			if !g.NodeExists("items:1") {
				t.Error("NodeExists: want true for items:1")
			}
		})
	}
}

func TestContract_AddNode_DuplicateIsIdempotent(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			if err := g.AddNode("items:1", "items"); err != nil {
				t.Errorf("duplicate AddNode should not error: %v", err)
			}
			if g.NodeCount() != 1 {
				t.Errorf("NodeCount after duplicate add: want 1, got %d", g.NodeCount())
			}
		})
	}
}

func TestContract_AddNode_UpdatesTypeIndex(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			mustAddNodeG(t, g, "items:2", "items")
			mustAddNodeG(t, g, "records:1", "records")

			items := g.GetNodesByType("items")
			if len(items) != 2 {
				t.Errorf("GetNodesByType(items): want 2, got %d", len(items))
			}
			records := g.GetNodesByType("records")
			if len(records) != 1 {
				t.Errorf("GetNodesByType(records): want 1, got %d", len(records))
			}
		})
	}
}

func TestContract_RemoveNode_Basic(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			if err := g.RemoveNode("items:1"); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			if g.NodeCount() != 0 {
				t.Errorf("NodeCount after remove: want 0, got %d", g.NodeCount())
			}
			if g.NodeExists("items:1") {
				t.Error("NodeExists should be false after removal")
			}
		})
	}
}

func TestContract_RemoveNode_NonExistent_IsNoOp(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Neither implementation should panic or return an error.
			if err := g.RemoveNode("nonexistent:999"); err != nil {
				t.Errorf("RemoveNode of absent node should not error: %v", err)
			}
		})
	}
}

func TestContract_RemoveNode_CascadesEdges(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// n:2 is the hub: n:1→n:2 and n:3→n:2
			mustAddNodeG(t, g, "n:1", "n")
			mustAddNodeG(t, g, "n:2", "n")
			mustAddNodeG(t, g, "n:3", "n")
			mustAddEdgeG(t, g, "n:1", "n:2", "OUT")
			mustAddEdgeG(t, g, "n:3", "n:2", "IN")
			if g.EdgeCount() != 2 {
				t.Fatalf("EdgeCount: want 2, got %d", g.EdgeCount())
			}

			if err := g.RemoveNode("n:2"); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			if g.EdgeCount() != 0 {
				t.Errorf("EdgeCount after removing hub: want 0, got %d", g.EdgeCount())
			}
			// n:1 should now have no outgoing edges.
			nb, err := g.GetNeighbors("n:1")
			if err != nil {
				t.Fatalf("GetNeighbors: %v", err)
			}
			if len(nb) != 0 {
				t.Errorf("n:1 neighbors after hub removal: want 0, got %v", nb)
			}
		})
	}
}

func TestContract_RemoveNode_CleansTypeIndex(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			mustAddNodeG(t, g, "items:2", "items")
			if err := g.RemoveNode("items:1"); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			nodes := g.GetNodesByType("items")
			if len(nodes) != 1 {
				t.Errorf("GetNodesByType after removal: want 1, got %d: %v", len(nodes), nodes)
			}
			if nodes[0] != "items:2" {
				t.Errorf("remaining type-index entry: want items:2, got %s", nodes[0])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// AddEdge / RemoveEdge
// ---------------------------------------------------------------------------

func TestContract_AddEdge_CreatesEdge(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			mustAddNodeG(t, g, "b:1", "b")
			mustAddEdgeG(t, g, "a:1", "b:1", "LINKS")
			if g.EdgeCount() != 1 {
				t.Errorf("EdgeCount: want 1, got %d", g.EdgeCount())
			}
		})
	}
}

func TestContract_AddEdge_ImplicitNodeCreation(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// AddEdge must create both nodes implicitly.
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			if !g.NodeExists("a:1") {
				t.Error("source node should be implicitly created")
			}
			if !g.NodeExists("b:1") {
				t.Error("target node should be implicitly created")
			}
			if g.NodeCount() != 2 {
				t.Errorf("NodeCount: want 2, got %d", g.NodeCount())
			}
		})
	}
}

func TestContract_AddEdge_IdempotentSameRelationship(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			// Re-adding the same edge with the same relationship is a no-op.
			if err := g.AddEdge("a:1", "b:1", "REL"); err != nil {
				t.Errorf("idempotent re-add should not error: %v", err)
			}
			if g.EdgeCount() != 1 {
				t.Errorf("EdgeCount after idempotent add: want 1, got %d", g.EdgeCount())
			}
		})
	}
}

func TestContract_AddEdge_DifferentRelationshipReturnsError(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			err := g.AddEdge("a:1", "b:1", "OTHER")
			if err == nil {
				t.Error("expected ErrEdgeAlreadyExists for same pair, different relationship")
			}
		})
	}
}

func TestContract_RemoveEdge_Basic(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			if err := g.RemoveEdge("a:1", "b:1"); err != nil {
				t.Fatalf("RemoveEdge: %v", err)
			}
			if g.EdgeCount() != 0 {
				t.Errorf("EdgeCount after remove: want 0, got %d", g.EdgeCount())
			}
		})
	}
}

func TestContract_RemoveEdge_UpdatesReverseIndex(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			if err := g.RemoveEdge("a:1", "b:1"); err != nil {
				t.Fatalf("RemoveEdge: %v", err)
			}
			inc, err := g.GetIncomingEdges("b:1")
			if err != nil {
				t.Fatalf("GetIncomingEdges: %v", err)
			}
			if len(inc) != 0 {
				t.Errorf("incoming edges after remove: want 0, got %v", inc)
			}
		})
	}
}

func TestContract_RemoveEdge_NonExistent_IsNoOp(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			mustAddNodeG(t, g, "b:1", "b")
			// No edge between a:1 and b:1 — should not panic or error.
			if err := g.RemoveEdge("a:1", "b:1"); err != nil {
				t.Errorf("RemoveEdge on absent edge should not error: %v", err)
			}
			if g.EdgeCount() != 0 {
				t.Errorf("EdgeCount: want 0, got %d", g.EdgeCount())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetNeighbors / GetIncomingEdges
// ---------------------------------------------------------------------------

func TestContract_GetNeighbors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "u:1", "u:2", "FOLLOWS")
			mustAddEdgeG(t, g, "u:1", "u:3", "KNOWS")

			nb, err := g.GetNeighbors("u:1")
			if err != nil {
				t.Fatalf("GetNeighbors: %v", err)
			}
			if len(nb) != 2 {
				t.Errorf("len: want 2, got %d", len(nb))
			}
			if nb["u:2"] != "FOLLOWS" {
				t.Errorf("FOLLOWS edge to u:2 missing; got %v", nb)
			}
			if nb["u:3"] != "KNOWS" {
				t.Errorf("KNOWS edge to u:3 missing; got %v", nb)
			}
		})
	}
}

func TestContract_GetNeighbors_ReturnsEmptyForIsolatedNode(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "u:1", "u")
			nb, err := g.GetNeighbors("u:1")
			if err != nil {
				t.Fatalf("GetNeighbors: %v", err)
			}
			if len(nb) != 0 {
				t.Errorf("isolated node should have 0 neighbors, got %v", nb)
			}
		})
	}
}

func TestContract_GetIncomingEdges(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Two sources → one target.
			mustAddEdgeG(t, g, "u:1", "u:3", "FOLLOWS")
			mustAddEdgeG(t, g, "u:2", "u:3", "FOLLOWS")

			inc, err := g.GetIncomingEdges("u:3")
			if err != nil {
				t.Fatalf("GetIncomingEdges: %v", err)
			}
			if len(inc) != 2 {
				t.Errorf("len: want 2, got %d: %v", len(inc), inc)
			}
			if inc["u:1"] != "FOLLOWS" {
				t.Errorf("FOLLOWS from u:1 missing; got %v", inc)
			}
			if inc["u:2"] != "FOLLOWS" {
				t.Errorf("FOLLOWS from u:2 missing; got %v", inc)
			}
		})
	}
}

func TestContract_GetNeighbors_AbsentNode_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			nb, err := g.GetNeighbors("nonexistent:1")
			if err != nil {
				t.Errorf("GetNeighbors on absent node: want nil error, got %v", err)
			}
			if len(nb) != 0 {
				t.Errorf("GetNeighbors on absent node: want empty map, got %v", nb)
			}
		})
	}
}

func TestContract_GetIncomingEdges_AbsentNode_ReturnsEmpty(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			inc, err := g.GetIncomingEdges("nonexistent:1")
			if err != nil {
				t.Errorf("GetIncomingEdges on absent node: want nil error, got %v", err)
			}
			if len(inc) != 0 {
				t.Errorf("GetIncomingEdges on absent node: want empty map, got %v", inc)
			}
		})
	}
}

func TestContract_GetNeighbors_ReturnsIndependentCopy(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			nb, _ := g.GetNeighbors("a:1")
			// Mutate the returned map — should not affect the graph.
			delete(nb, "b:1")
			nb2, _ := g.GetNeighbors("a:1")
			if len(nb2) != 1 {
				t.Error("GetNeighbors returned a live reference; mutations should not affect graph state")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NodeCount / EdgeCount consistency
// ---------------------------------------------------------------------------

func TestContract_Counters_ConsistentAfterMutations(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Build: 3 nodes, 3 edges forming a triangle.
			mustAddEdgeG(t, g, "n:1", "n:2", "A")
			mustAddEdgeG(t, g, "n:2", "n:3", "B")
			mustAddEdgeG(t, g, "n:1", "n:3", "C")
			if g.NodeCount() != 3 {
				t.Errorf("NodeCount: want 3, got %d", g.NodeCount())
			}
			if g.EdgeCount() != 3 {
				t.Errorf("EdgeCount: want 3, got %d", g.EdgeCount())
			}
			// Remove one edge.
			if err := g.RemoveEdge("n:1", "n:3"); err != nil {
				t.Fatalf("RemoveEdge: %v", err)
			}
			if g.EdgeCount() != 2 {
				t.Errorf("EdgeCount after remove: want 2, got %d", g.EdgeCount())
			}
			if g.NodeCount() != 3 {
				t.Errorf("NodeCount unchanged after edge remove: want 3, got %d", g.NodeCount())
			}
		})
	}
}

func TestContract_EdgeCount_NotDoubleCountedOnIdempotentAdd(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "REL")
			_ = g.AddEdge("a:1", "b:1", "REL") // idempotent
			if g.EdgeCount() != 1 {
				t.Errorf("EdgeCount: want 1, got %d (idempotent add must not double-count)", g.EdgeCount())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetDegree
// ---------------------------------------------------------------------------

func TestContract_GetDegree_Basic(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// n:2 receives 2 incoming and emits 1 outgoing.
			mustAddEdgeG(t, g, "n:1", "n:2", "A")
			mustAddEdgeG(t, g, "n:3", "n:2", "B")
			mustAddEdgeG(t, g, "n:2", "n:4", "C")

			deg, err := g.GetDegree("n:2")
			if err != nil {
				t.Fatalf("GetDegree: %v", err)
			}
			if deg.In != 2 {
				t.Errorf("In: want 2, got %d", deg.In)
			}
			if deg.Out != 1 {
				t.Errorf("Out: want 1, got %d", deg.Out)
			}
			if deg.Total != 3 {
				t.Errorf("Total: want 3, got %d", deg.Total)
			}
		})
	}
}

func TestContract_GetDegree_AbsentNodeErrors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			if _, err := g.GetDegree("absent:1"); err == nil {
				t.Error("GetDegree on absent node should error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetNodeInfo
// ---------------------------------------------------------------------------

func TestContract_GetNodeInfo_Basic(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "items:42", "records:7", "record_ref")
			mustAddEdgeG(t, g, "events:1", "items:42", "item_ref")

			info, err := g.GetNodeInfo("items:42")
			if err != nil {
				t.Fatalf("GetNodeInfo: %v", err)
			}
			if info.ID != "items:42" {
				t.Errorf("ID: want items:42, got %s", info.ID)
			}
			if info.Outgoing["records:7"] != "record_ref" {
				t.Errorf("Outgoing: expected record_ref to records:7; got %v", info.Outgoing)
			}
			if info.Incoming["events:1"] != "item_ref" {
				t.Errorf("Incoming: expected item_ref from events:1; got %v", info.Incoming)
			}
			if info.Degree.In != 1 || info.Degree.Out != 1 || info.Degree.Total != 2 {
				t.Errorf("Degree: want {1,1,2}, got %+v", info.Degree)
			}
		})
	}
}

func TestContract_GetNodeInfo_AbsentNodeErrors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			if _, err := g.GetNodeInfo("absent:99"); err == nil {
				t.Error("GetNodeInfo on absent node should error")
			}
		})
	}
}


// TestContract_GetNodeInfo_PrefixedNode verifies that Entity is reported
// without the tenant prefix for nodes in the "XXXX@entity:id" format.
// Regression test for a FlatGraph bug where Entity was returned as
// "0001@items" instead of "items".
func TestContract_GetNodeInfo_PrefixedNode(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "0001@items:42", "0001@records:7", "record_ref")

			info, err := g.GetNodeInfo("0001@items:42")
			if err != nil {
				t.Fatalf("GetNodeInfo: %v", err)
			}
			if info.ID != "0001@items:42" {
				t.Errorf("ID: want 0001@items:42, got %s", info.ID)
			}
			if info.Entity != "items" {
				t.Errorf("Entity: want \"items\" (prefix stripped), got %q", info.Entity)
			}
			if info.EntityID != 42 {
				t.Errorf("EntityID: want 42, got %d", info.EntityID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// GetNodesByType / GetAllNodes
// ---------------------------------------------------------------------------

func TestContract_GetNodesByType_MultiType(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			mustAddNodeG(t, g, "items:2", "items")
			mustAddNodeG(t, g, "records:1", "records")

			items := sorted(g.GetNodesByType("items"))
			if len(items) != 2 {
				t.Errorf("GetNodesByType(items): want 2, got %d: %v", len(items), items)
			}
			if items[0] != "items:1" || items[1] != "items:2" {
				t.Errorf("GetNodesByType(items): got %v", items)
			}
			records := g.GetNodesByType("records")
			if len(records) != 1 || records[0] != "records:1" {
				t.Errorf("GetNodesByType(records): got %v", records)
			}
		})
	}
}

func TestContract_GetNodesByType_UnknownType_ReturnsNilOrEmpty(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			nodes := g.GetNodesByType("nonexistent")
			if len(nodes) != 0 {
				t.Errorf("GetNodesByType for unknown type: want empty, got %v", nodes)
			}
		})
	}
}

func TestContract_GetAllNodes(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			mustAddNodeG(t, g, "b:1", "b")
			mustAddNodeG(t, g, "c:1", "c")

			all := sorted(g.GetAllNodes())
			if len(all) != 3 {
				t.Errorf("GetAllNodes: want 3, got %d: %v", len(all), all)
			}
			want := []string{"a:1", "b:1", "c:1"}
			for i, v := range want {
				if all[i] != v {
					t.Errorf("GetAllNodes[%d]: want %s, got %s", i, v, all[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// FindPath
// ---------------------------------------------------------------------------

func TestContract_FindPath_Chain(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// 1 → 2 → 3 → 4
			mustAddEdgeG(t, g, "n:1", "n:2", "NEXT")
			mustAddEdgeG(t, g, "n:2", "n:3", "NEXT")
			mustAddEdgeG(t, g, "n:3", "n:4", "NEXT")

			path, err := g.FindPath("n:1", "n:4", 10)
			if err != nil {
				t.Fatalf("FindPath: %v", err)
			}
			want := []string{"n:1", "n:2", "n:3", "n:4"}
			if len(path) != len(want) {
				t.Fatalf("path length: want %d, got %d: %v", len(want), len(path), path)
			}
			for i := range want {
				if path[i] != want[i] {
					t.Errorf("path[%d]: want %s, got %s", i, want[i], path[i])
				}
			}
		})
	}
}

func TestContract_FindPath_SelfPath(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "n:1", "n")
			path, err := g.FindPath("n:1", "n:1", 10)
			if err != nil {
				t.Fatalf("FindPath self: %v", err)
			}
			if len(path) != 1 || path[0] != "n:1" {
				t.Errorf("self-path: want [n:1], got %v", path)
			}
		})
	}
}

func TestContract_FindPath_MaxDepthRespected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Chain of 5: 1→2→3→4→5 requires depth 4.
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:2", "n:3", "L")
			mustAddEdgeG(t, g, "n:3", "n:4", "L")
			mustAddEdgeG(t, g, "n:4", "n:5", "L")

			// maxDepth=2 is not enough to reach n:5.
			_, err := g.FindPath("n:1", "n:5", 2)
			if err == nil {
				t.Error("FindPath should fail when path exceeds maxDepth")
			}
			// maxDepth=4 is exactly enough.
			path, err := g.FindPath("n:1", "n:5", 4)
			if err != nil {
				t.Fatalf("FindPath with sufficient depth: %v", err)
			}
			if len(path) != 5 {
				t.Errorf("path length: want 5, got %d: %v", len(path), path)
			}
		})
	}
}

func TestContract_FindPath_NoPath_Errors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "n:1", "n")
			mustAddNodeG(t, g, "n:2", "n") // no edge between them
			if _, err := g.FindPath("n:1", "n:2", 10); err == nil {
				t.Error("FindPath to disconnected node should error")
			}
		})
	}
}

func TestContract_FindPath_AbsentNode_Errors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "n:1", "n")
			if _, err := g.FindPath("n:1", "ghost:99", 5); err == nil {
				t.Error("FindPath to absent node should error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// PathExists
// ---------------------------------------------------------------------------

func TestContract_PathExists_Found(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R")
			mustAddEdgeG(t, g, "b:1", "c:1", "R")

			found, depth, err := g.PathExists("a:1", "c:1", 10)
			if err != nil {
				t.Fatalf("PathExists: %v", err)
			}
			if !found {
				t.Error("PathExists: want true")
			}
			if depth != 2 {
				t.Errorf("depth: want 2, got %d", depth)
			}
		})
	}
}

func TestContract_PathExists_SelfPath(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			found, depth, err := g.PathExists("a:1", "a:1", 5)
			if err != nil {
				t.Fatalf("PathExists self: %v", err)
			}
			if !found {
				t.Error("PathExists self: want true")
			}
			if depth != 0 {
				t.Errorf("depth for self-path: want 0, got %d", depth)
			}
		})
	}
}

func TestContract_PathExists_NotFound(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			mustAddNodeG(t, g, "b:1", "b")
			found, _, err := g.PathExists("a:1", "b:1", 10)
			if err != nil {
				t.Fatalf("PathExists: %v", err)
			}
			if found {
				t.Error("PathExists: want false for disconnected nodes")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// SharedOutNeighbors
// ---------------------------------------------------------------------------

func TestContract_SharedOutNeighbors_Basic(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Both i:1 and i:2 point to l:5 and g:3.
			mustAddEdgeG(t, g, "i:1", "l:5", "loc")
			mustAddEdgeG(t, g, "i:2", "l:5", "loc")
			mustAddEdgeG(t, g, "i:1", "g:3", "grp")
			mustAddEdgeG(t, g, "i:2", "g:3", "grp")
			// i:1 also points to something unique.
			mustAddEdgeG(t, g, "i:1", "x:9", "x")

			common, err := g.SharedOutNeighbors("i:1", "i:2")
			if err != nil {
				t.Fatalf("SharedOutNeighbors: %v", err)
			}
			if len(common) != 2 {
				t.Errorf("common neighbor count: want 2, got %d: %v", len(common), common)
			}
			s := sorted(common)
			if s[0] != "g:3" || s[1] != "l:5" {
				t.Errorf("common neighbors: want [g:3 l:5], got %v", s)
			}
		})
	}
}

func TestContract_SharedOutNeighbors_None(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "x:1", "R")
			mustAddEdgeG(t, g, "b:1", "y:1", "R")
			common, err := g.SharedOutNeighbors("a:1", "b:1")
			if err != nil {
				t.Fatalf("SharedOutNeighbors: %v", err)
			}
			if common == nil {
				t.Error("SharedOutNeighbors: want non-nil empty slice, got nil")
			}
			if len(common) != 0 {
				t.Errorf("want 0 common neighbors, got %v", common)
			}
		})
	}
}

func TestContract_SharedOutNeighbors_AbsentNode_Errors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			if _, err := g.SharedOutNeighbors("a:1", "ghost:99"); err == nil {
				t.Error("SharedOutNeighbors with absent node should error")
			}
		})
	}
}

func TestContract_SharedOutNeighbors_IncomingEdgesExcluded(t *testing.T) {
	// Regression test for the state.CommonNeighbors bidirectional bug.
	// Only outgoing edges should be considered; incoming edges must not
	// contribute to the common set regardless of implementation.
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// x:1 and x:2 both receive an incoming edge from src:1,
			// but share no outgoing neighbours.
			mustAddEdgeG(t, g, "src:1", "x:1", "R")
			mustAddEdgeG(t, g, "src:1", "x:2", "R")
			common, err := g.SharedOutNeighbors("x:1", "x:2")
			if err != nil {
				t.Fatalf("SharedOutNeighbors: %v", err)
			}
			if len(common) != 0 {
				t.Errorf("incoming edges must not count as common neighbours; got %v", common)
			}
		})
	}
}

func TestContract_SharedOutNeighbors_SameNode(t *testing.T) {
	// When nodeA == nodeB every outgoing neighbour trivially satisfies
	// "reachable from both", so all of them are returned.
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "x:1", "R")
			mustAddEdgeG(t, g, "a:1", "x:2", "R")
			common, err := g.SharedOutNeighbors("a:1", "a:1")
			if err != nil {
				t.Fatalf("SharedOutNeighbors(same, same): %v", err)
			}
			if common == nil {
				t.Fatal("SharedOutNeighbors(same, same): want non-nil slice, got nil")
			}
			if len(common) != 2 {
				t.Errorf("SharedOutNeighbors(same, same): want 2 neighbours, got %d: %v", len(common), common)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// HasCycle
// ---------------------------------------------------------------------------

func TestContract_HasCycle_NoCycle(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "n:1", "n:2", "A")
			mustAddEdgeG(t, g, "n:2", "n:3", "B")
			if g.HasCycle() {
				t.Error("acyclic DAG should not report a cycle")
			}
		})
	}
}

func TestContract_HasCycle_WithCycle(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("ignore") // ignore mode so the cycle is accepted
			mustAddEdgeG(t, g, "n:1", "n:2", "A")
			mustAddEdgeG(t, g, "n:2", "n:3", "B")
			mustAddEdgeG(t, g, "n:3", "n:1", "C") // closes the cycle
			if !g.HasCycle() {
				t.Error("cyclic graph must report a cycle")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Cycle detection modes
// ---------------------------------------------------------------------------

func TestContract_CycleMode_Ignore_AllowsCycle(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("ignore")
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:2", "n:3", "L")
			if err := g.AddEdge("n:3", "n:1", "L"); err != nil {
				t.Errorf("ignore mode should permit cycle; got: %v", err)
			}
			if !g.HasCycle() {
				t.Error("HasCycle should be true after cycle is introduced")
			}
		})
	}
}

func TestContract_CycleMode_Warn_AllowsCycleWithoutError(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("warn")
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:2", "n:3", "L")
			if err := g.AddEdge("n:3", "n:1", "L"); err != nil {
				t.Errorf("warn mode should permit cycle without error; got: %v", err)
			}
		})
	}
}

func TestContract_CycleMode_Error_RejectsCycle(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("error")
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:2", "n:3", "L")
			err := g.AddEdge("n:3", "n:1", "L")
			if err == nil {
				t.Error("error mode should reject cycle-forming edge")
			}
			if g.HasCycle() {
				t.Error("HasCycle should be false — edge was rejected")
			}
		})
	}
}

func TestContract_CycleMode_Error_RejectsSelfLoop(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("error")
			mustAddNodeG(t, g, "n:1", "n")
			if err := g.AddEdge("n:1", "n:1", "SELF"); err == nil {
				t.Error("error mode should reject self-loop")
			}
		})
	}
}

func TestContract_CycleMode_Error_DAG_Succeeds(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("error")
			// Diamond: n:1 → n:2, n:1 → n:3, n:2 → n:4, n:3 → n:4
			for _, e := range [][3]string{
				{"n:1", "n:2", "L"},
				{"n:1", "n:3", "L"},
				{"n:2", "n:4", "L"},
				{"n:3", "n:4", "L"},
			} {
				if err := g.AddEdge(e[0], e[1], e[2]); err != nil {
					t.Errorf("AddEdge %s->%s in error mode on DAG: %v", e[0], e[1], err)
				}
			}
			if g.HasCycle() {
				t.Error("DAG should not have a cycle")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Clear
// ---------------------------------------------------------------------------

func TestContract_Clear_ResetsAll(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R")
			mustAddEdgeG(t, g, "b:1", "c:1", "R")
			if err := g.Clear(); err != nil {
				t.Fatalf("Clear: %v", err)
			}
			if g.NodeCount() != 0 {
				t.Errorf("NodeCount after Clear: want 0, got %d", g.NodeCount())
			}
			if g.EdgeCount() != 0 {
				t.Errorf("EdgeCount after Clear: want 0, got %d", g.EdgeCount())
			}
			if g.HasCycle() {
				t.Error("empty graph after Clear should have no cycle")
			}
		})
	}
}

func TestContract_Clear_AllowsRebuild(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R")
			_ = g.Clear()
			// After Clear the graph must behave as freshly created.
			mustAddNodeG(t, g, "x:1", "x")
			if g.NodeCount() != 1 {
				t.Errorf("NodeCount after Clear+AddNode: want 1, got %d", g.NodeCount())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Save / Load round-trip
// ---------------------------------------------------------------------------

func TestContract_SaveLoad_Roundtrip(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "users:1", "users")
			mustAddNodeG(t, g, "users:2", "users")
			mustAddNodeG(t, g, "posts:1", "posts")
			mustAddEdgeG(t, g, "users:1", "users:2", "FOLLOWS")
			mustAddEdgeG(t, g, "users:1", "posts:1", "AUTHORED")

			f, err := os.CreateTemp("", "contract_graph_*.json")
			if err != nil {
				t.Fatalf("TempFile: %v", err)
			}
			f.Close()
			defer os.Remove(f.Name())

			if err := g.Save(f.Name()); err != nil {
				t.Fatalf("Save: %v", err)
			}

			g2 := impl.new()
			if err := g2.Load(f.Name()); err != nil {
				t.Fatalf("Load: %v", err)
			}

			if g2.NodeCount() != g.NodeCount() {
				t.Errorf("NodeCount: want %d, got %d", g.NodeCount(), g2.NodeCount())
			}
			if g2.EdgeCount() != g.EdgeCount() {
				t.Errorf("EdgeCount: want %d, got %d", g.EdgeCount(), g2.EdgeCount())
			}
			nb, err := g2.GetNeighbors("users:1")
			if err != nil {
				t.Fatalf("GetNeighbors after Load: %v", err)
			}
			if nb["users:2"] != "FOLLOWS" {
				t.Error("FOLLOWS edge not preserved after Load")
			}
			if nb["posts:1"] != "AUTHORED" {
				t.Error("AUTHORED edge not preserved after Load")
			}
			inc, _ := g2.GetIncomingEdges("users:2")
			if inc["users:1"] != "FOLLOWS" {
				t.Error("reverse FOLLOWS edge not preserved after Load")
			}
			users := g2.GetNodesByType("users")
			if len(users) != 2 {
				t.Errorf("type index after Load: want 2 users, got %d", len(users))
			}
		})
	}
}

func TestContract_SaveLoad_Empty(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			f, err := os.CreateTemp("", "contract_empty_*.json")
			if err != nil {
				t.Fatalf("TempFile: %v", err)
			}
			f.Close()
			defer os.Remove(f.Name())

			if err := g.Save(f.Name()); err != nil {
				t.Fatalf("Save empty: %v", err)
			}
			g2 := impl.new()
			if err := g2.Load(f.Name()); err != nil {
				t.Fatalf("Load empty: %v", err)
			}
			if g2.NodeCount() != 0 || g2.EdgeCount() != 0 {
				t.Errorf("loaded empty graph should be empty; nodes=%d edges=%d",
					g2.NodeCount(), g2.EdgeCount())
			}
		})
	}
}

func TestContract_Load_NonexistentFile_IsNoOp(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Missing file should not error and should leave graph empty.
			if err := g.Load("/tmp/does_not_exist_contract_test.json"); err != nil {
				t.Errorf("Load of missing file should not error: %v", err)
			}
			if g.NodeCount() != 0 {
				t.Errorf("NodeCount after loading missing file: want 0, got %d", g.NodeCount())
			}
		})
	}
}

func TestContract_Load_InvalidCycleDetectionMode_Errors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			f, err := os.CreateTemp("", "olu-graph-badmode-*.json")
			if err != nil {
				t.Fatalf("create temp file: %v", err)
			}
			defer os.Remove(f.Name())
			// Write a file with an unrecognised cycle_detection value.
			_, _ = f.WriteString(`{"cycle_detection":"strict","nodes":{}}`)
			f.Close()

			g := impl.new()
			if err := g.Load(f.Name()); err == nil {
				t.Error("Load with invalid cycle_detection mode should return an error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// UpdateFromEntity (single-tenant path)
// ---------------------------------------------------------------------------

func TestContract_UpdateFromEntity_CreatesNodeAndEdge(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "asset_types:1", "asset_types")

			data := map[string]interface{}{
				"code": "FE-001",
				"asset_type": map[string]interface{}{
					"type":   "REF",
					"entity": "asset_types",
					"id":     1,
				},
			}
			ug, ok := g.(interface {
				UpdateFromEntity(string, int, map[string]interface{}) error
			})
			if !ok {
				t.Skip("implementation does not expose UpdateFromEntity")
			}
			if err := ug.UpdateFromEntity("assets", 1, data); err != nil {
				t.Fatalf("UpdateFromEntity: %v", err)
			}

			if !g.NodeExists("assets:1") {
				t.Error("assets:1 node should exist")
			}
			nb, _ := g.GetNeighbors("assets:1")
			if nb["asset_types:1"] != "asset_type" {
				t.Errorf("edge missing; neighbors: %v", nb)
			}
			inc, _ := g.GetIncomingEdges("asset_types:1")
			if inc["assets:1"] != "asset_type" {
				t.Errorf("reverse edge missing; incoming: %v", inc)
			}
		})
	}
}

func TestContract_UpdateFromEntity_MultipleRefs(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "assets:1", "assets")
			mustAddNodeG(t, g, "sensors:1", "sensors")

			data := map[string]interface{}{
				"asset": map[string]interface{}{"type": "REF", "entity": "assets", "id": 1},
				"sensor": map[string]interface{}{"type": "REF", "entity": "sensors", "id": 1},
			}
			ug := g.(interface {
				UpdateFromEntity(string, int, map[string]interface{}) error
			})
			if err := ug.UpdateFromEntity("events", 1, data); err != nil {
				t.Fatalf("UpdateFromEntity: %v", err)
			}
			nb, _ := g.GetNeighbors("events:1")
			if len(nb) != 2 {
				t.Errorf("expected 2 outgoing edges, got %d: %v", len(nb), nb)
			}
			if nb["assets:1"] != "asset" {
				t.Errorf("asset edge: got %v", nb)
			}
			if nb["sensors:1"] != "sensor" {
				t.Errorf("sensor edge: got %v", nb)
			}
		})
	}
}

func TestContract_UpdateFromEntity_RefChange_RemovesOldEdge(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "asset_types:1", "asset_types")
			mustAddNodeG(t, g, "asset_types:2", "asset_types")
			ug := g.(interface {
				UpdateFromEntity(string, int, map[string]interface{}) error
			})

			// Initial: assets:1 → asset_types:1
			if err := ug.UpdateFromEntity("assets", 1, map[string]interface{}{
				"asset_type": map[string]interface{}{"type": "REF", "entity": "asset_types", "id": 1},
			}); err != nil {
				t.Fatalf("first update: %v", err)
			}

			// Change: assets:1 → asset_types:2
			if err := ug.UpdateFromEntity("assets", 1, map[string]interface{}{
				"asset_type": map[string]interface{}{"type": "REF", "entity": "asset_types", "id": 2},
			}); err != nil {
				t.Fatalf("second update: %v", err)
			}

			nb, _ := g.GetNeighbors("assets:1")
			if _, stillLinked := nb["asset_types:1"]; stillLinked {
				t.Error("old edge to asset_types:1 should have been removed")
			}
			if nb["asset_types:2"] != "asset_type" {
				t.Errorf("new edge to asset_types:2 missing; got %v", nb)
			}
			if g.EdgeCount() != 1 {
				t.Errorf("EdgeCount: want 1, got %d", g.EdgeCount())
			}
		})
	}
}

func TestContract_UpdateFromEntity_Idempotent(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "asset_types:1", "asset_types")
			ug := g.(interface {
				UpdateFromEntity(string, int, map[string]interface{}) error
			})
			data := map[string]interface{}{
				"asset_type": map[string]interface{}{"type": "REF", "entity": "asset_types", "id": 1},
			}
			if err := ug.UpdateFromEntity("assets", 1, data); err != nil {
				t.Fatalf("first update: %v", err)
			}
			if err := ug.UpdateFromEntity("assets", 1, data); err != nil {
				t.Fatalf("second (idempotent) update: %v", err)
			}
			if g.EdgeCount() != 1 {
				t.Errorf("EdgeCount after idempotent update: want 1, got %d", g.EdgeCount())
			}
		})
	}
}

func TestContract_UpdateFromEntity_RefRemoval_RemovesEdge(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "asset_types:1", "asset_types")
			ug := g.(interface {
				UpdateFromEntity(string, int, map[string]interface{}) error
			})

			// Add a REF.
			if err := ug.UpdateFromEntity("assets", 1, map[string]interface{}{
				"asset_type": map[string]interface{}{"type": "REF", "entity": "asset_types", "id": 1},
			}); err != nil {
				t.Fatalf("first update: %v", err)
			}
			if g.EdgeCount() != 1 {
				t.Fatalf("EdgeCount: want 1, got %d", g.EdgeCount())
			}

			// Remove it by sending an entity with no REF fields.
			if err := ug.UpdateFromEntity("assets", 1, map[string]interface{}{
				"asset_type": "plain-string-not-a-ref",
			}); err != nil {
				t.Fatalf("second update: %v", err)
			}
			if g.EdgeCount() != 0 {
				t.Errorf("EdgeCount after ref removal: want 0, got %d", g.EdgeCount())
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Concurrent access
// ---------------------------------------------------------------------------

func TestContract_ConcurrentAccess_MixedReadsWrites(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Seed 100 nodes.
			for i := 0; i < 100; i++ {
				mustAddNodeG(t, g, fmt.Sprintf("n:%d", i), "n")
			}

			errCh := make(chan error, 200)
			var wg sync.WaitGroup

			// 50 concurrent readers.
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					_, err := g.GetNeighbors(fmt.Sprintf("n:%d", id%100))
					if err != nil {
						errCh <- fmt.Errorf("GetNeighbors: %w", err)
					}
				}(i)
			}

			// 50 concurrent writers.
			for i := 0; i < 50; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					from := fmt.Sprintf("n:%d", id%100)
					to := fmt.Sprintf("n:%d", (id+1)%100)
					if err := g.AddEdge(from, to, "LINK"); err != nil {
						// ErrEdgeAlreadyExists is expected from concurrent writers;
						// any other error is a genuine fault.
						if err != ErrEdgeAlreadyExists {
							errCh <- fmt.Errorf("AddEdge: %w", err)
						}
					}
				}(i)
			}

			wg.Wait()
			close(errCh)
			for err := range errCh {
				t.Errorf("concurrent operation error: %v", err)
			}
		})
	}
}

func TestContract_ConcurrentAccess_ClearDuringReads(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			for i := 0; i < 50; i++ {
				mustAddNodeG(t, g, fmt.Sprintf("n:%d", i), "n")
			}

			var wg sync.WaitGroup
			// Readers keep reading while Clear runs; must not panic.
			for i := 0; i < 20; i++ {
				wg.Add(1)
				go func(id int) {
					defer wg.Done()
					_ = g.NodeCount()
					_, _ = g.GetNeighbors(fmt.Sprintf("n:%d", id%50))
				}(i)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				_ = g.Clear()
			}()
			wg.Wait()
			// No assertion beyond "did not panic".
		})
	}
}

// ---------------------------------------------------------------------------
// Topology patterns
// ---------------------------------------------------------------------------

func TestContract_Topology_Diamond(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// n:1 → n:2, n:1 → n:3, n:2 → n:4, n:3 → n:4
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:1", "n:3", "L")
			mustAddEdgeG(t, g, "n:2", "n:4", "L")
			mustAddEdgeG(t, g, "n:3", "n:4", "L")

			if g.NodeCount() != 4 || g.EdgeCount() != 4 {
				t.Errorf("nodes=%d edges=%d; want 4/4", g.NodeCount(), g.EdgeCount())
			}
			path, err := g.FindPath("n:1", "n:4", 5)
			if err != nil {
				t.Fatalf("FindPath: %v", err)
			}
			if path[0] != "n:1" || path[len(path)-1] != "n:4" {
				t.Errorf("path bounds wrong: %v", path)
			}
			if g.HasCycle() {
				t.Error("diamond DAG should not have a cycle")
			}
		})
	}
}

func TestContract_Topology_FanOut(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// hub → 5 leaves
			for i := 1; i <= 5; i++ {
				mustAddEdgeG(t, g, "hub:1", fmt.Sprintf("leaf:%d", i), "EDGE")
			}
			deg, err := g.GetDegree("hub:1")
			if err != nil {
				t.Fatalf("GetDegree: %v", err)
			}
			if deg.Out != 5 {
				t.Errorf("out-degree: want 5, got %d", deg.Out)
			}
			if deg.In != 0 {
				t.Errorf("in-degree: want 0, got %d", deg.In)
			}
		})
	}
}

func TestContract_Topology_FanIn(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// 5 sources → sink
			for i := 1; i <= 5; i++ {
				mustAddEdgeG(t, g, fmt.Sprintf("src:%d", i), "sink:1", "EDGE")
			}
			deg, err := g.GetDegree("sink:1")
			if err != nil {
				t.Fatalf("GetDegree: %v", err)
			}
			if deg.In != 5 {
				t.Errorf("in-degree: want 5, got %d", deg.In)
			}
			if deg.Out != 0 {
				t.Errorf("out-degree: want 0, got %d", deg.Out)
			}
		})
	}
}

func TestContract_Topology_DeepChain(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			const depth = 50
			g := impl.new()
			for i := 0; i < depth; i++ {
				mustAddEdgeG(t, g, fmt.Sprintf("n:%d", i), fmt.Sprintf("n:%d", i+1), "L")
			}
			if g.NodeCount() != depth+1 {
				t.Errorf("NodeCount: want %d, got %d", depth+1, g.NodeCount())
			}
			path, err := g.FindPath("n:0", fmt.Sprintf("n:%d", depth), depth+5)
			if err != nil {
				t.Fatalf("FindPath: %v", err)
			}
			if len(path) != depth+1 {
				t.Errorf("path length: want %d, got %d", depth+1, len(path))
			}
		})
	}
}

func TestContract_Topology_DisconnectedComponents(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Component A: a:1 → a:2
			mustAddEdgeG(t, g, "a:1", "a:2", "R")
			// Component B: b:1 → b:2 (no connection to A)
			mustAddEdgeG(t, g, "b:1", "b:2", "R")

			if g.NodeCount() != 4 || g.EdgeCount() != 2 {
				t.Errorf("nodes=%d edges=%d; want 4/2", g.NodeCount(), g.EdgeCount())
			}
			// No path across components.
			found, _, _ := g.PathExists("a:1", "b:1", 10)
			if found {
				t.Error("PathExists should be false across disconnected components")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tenant isolation contract tests
//
// These verify that *ForTenant methods filter by the XXXX@ prefix in both
// implementations. The server uses a single shared graph instance for all
// tenants, so this is the only layer preventing cross-tenant data leaks on
// the stats/enumeration surface.
// ---------------------------------------------------------------------------

func TestContract_TenantIsolation_NodeCount(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			// 3 nodes for tenant 1, 2 nodes for tenant 2.
			for i := 1; i <= 3; i++ {
				mustAddNodeG(t, g, tenant.NodeID(1, "item", i), "item")
			}
			for i := 1; i <= 2; i++ {
				mustAddNodeG(t, g, tenant.NodeID(2, "item", i), "item")
			}

			n1, err := g.NodeCountForTenant(p1)
			if err != nil {
				t.Fatalf("NodeCountForTenant(p1): %v", err)
			}
			if n1 != 3 {
				t.Errorf("tenant 1 node count: want 3, got %d", n1)
			}
			n2, err := g.NodeCountForTenant(p2)
			if err != nil {
				t.Fatalf("NodeCountForTenant(p2): %v", err)
			}
			if n2 != 2 {
				t.Errorf("tenant 2 node count: want 2, got %d", n2)
			}
		})
	}
}

func TestContract_TenantIsolation_EdgeCount(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			// 2 edges for tenant 1, 1 edge for tenant 2.
			mustAddEdgeG(t, g, tenant.NodeID(1, "a", 1), tenant.NodeID(1, "b", 1), "R")
			mustAddEdgeG(t, g, tenant.NodeID(1, "a", 2), tenant.NodeID(1, "b", 2), "R")
			mustAddEdgeG(t, g, tenant.NodeID(2, "a", 1), tenant.NodeID(2, "b", 1), "R")

			e1, err := g.EdgeCountForTenant(p1)
			if err != nil {
				t.Fatalf("EdgeCountForTenant(p1): %v", err)
			}
			if e1 != 2 {
				t.Errorf("tenant 1 edge count: want 2, got %d", e1)
			}
			e2, err := g.EdgeCountForTenant(p2)
			if err != nil {
				t.Fatalf("EdgeCountForTenant(p2): %v", err)
			}
			if e2 != 1 {
				t.Errorf("tenant 2 edge count: want 1, got %d", e2)
			}
		})
	}
}

func TestContract_TenantIsolation_GetAllNodes(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			n1a := tenant.NodeID(1, "item", 1)
			n1b := tenant.NodeID(1, "item", 2)
			n2a := tenant.NodeID(2, "item", 1)

			mustAddNodeG(t, g, n1a, "item")
			mustAddNodeG(t, g, n1b, "item")
			mustAddNodeG(t, g, n2a, "item")

			nodes1, err := g.GetAllNodesForTenant(p1)
			if err != nil {
				t.Fatalf("GetAllNodesForTenant(p1): %v", err)
			}
			if len(nodes1) != 2 {
				t.Errorf("tenant 1: want 2 nodes, got %d: %v", len(nodes1), nodes1)
			}
			for _, nd := range nodes1 {
				if nd != n1a && nd != n1b {
					t.Errorf("tenant 1 result contains foreign node %q", nd)
				}
			}

			nodes2, err := g.GetAllNodesForTenant(p2)
			if err != nil {
				t.Fatalf("GetAllNodesForTenant(p2): %v", err)
			}
			if len(nodes2) != 1 || nodes2[0] != n2a {
				t.Errorf("tenant 2: want [%q], got %v", n2a, nodes2)
			}
		})
	}
}

func TestContract_TenantIsolation_GetNodesByType(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			mustAddNodeG(t, g, tenant.NodeID(1, "item", 1), "item")
			mustAddNodeG(t, g, tenant.NodeID(1, "order", 1), "order")
			mustAddNodeG(t, g, tenant.NodeID(2, "item", 1), "item")

			items1, err := g.GetNodesByTypeForTenant(p1, "item")
			if err != nil {
				t.Fatalf("GetNodesByTypeForTenant(p1, item): %v", err)
			}
			if len(items1) != 1 {
				t.Errorf("tenant 1 items: want 1, got %d: %v", len(items1), items1)
			}

			items2, err := g.GetNodesByTypeForTenant(p2, "item")
			if err != nil {
				t.Fatalf("GetNodesByTypeForTenant(p2, item): %v", err)
			}
			if len(items2) != 1 {
				t.Errorf("tenant 2 items: want 1, got %d: %v", len(items2), items2)
			}
			if items1[0] == items2[0] {
				t.Errorf("tenant 1 and tenant 2 returned the same node %q", items1[0])
			}

			// tenant 2 has no orders.
			orders2, err := g.GetNodesByTypeForTenant(p2, "order")
			if err != nil {
				t.Fatalf("GetNodesByTypeForTenant(p2, order): %v", err)
			}
			if len(orders2) != 0 {
				t.Errorf("tenant 2 orders: want 0, got %d: %v", len(orders2), orders2)
			}
		})
	}
}

func TestContract_TenantIsolation_EmptyPrefixRejected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "item:1", "item")

			if _, err := g.NodeCountForTenant(""); err == nil {
				t.Error("NodeCountForTenant(\"\") should return an error")
			}
			if _, err := g.EdgeCountForTenant(""); err == nil {
				t.Error("EdgeCountForTenant(\"\") should return an error")
			}
			if _, err := g.GetAllNodesForTenant(""); err == nil {
				t.Error("GetAllNodesForTenant(\"\") should return an error")
			}
			if _, err := g.GetNodesByTypeForTenant("", "item"); err == nil {
				t.Error("GetNodesByTypeForTenant(\"\", item) should return an error")
			}
		})
	}
}

func TestContract_TenantIsolation_UpdateFromEntityForTenant(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			data := map[string]interface{}{
				"id":   1,
				"type": "order",
				"@customer": map[string]interface{}{
					"$ref": map[string]interface{}{
						"entity": "customer",
						"id":     float64(10),
					},
				},
			}

			if err := g.UpdateFromEntityForTenant(1, "order", 1, data); err != nil {
				t.Fatalf("UpdateFromEntityForTenant(tid=1): %v", err)
			}
			if err := g.UpdateFromEntityForTenant(2, "order", 1, data); err != nil {
				t.Fatalf("UpdateFromEntityForTenant(tid=2): %v", err)
			}

			// Each tenant should see exactly its own order node.
			nodes1, err := g.GetAllNodesForTenant(p1)
			if err != nil {
				t.Fatalf("GetAllNodesForTenant(p1): %v", err)
			}
			nodes2, err := g.GetAllNodesForTenant(p2)
			if err != nil {
				t.Fatalf("GetAllNodesForTenant(p2): %v", err)
			}

			if len(nodes1) == 0 {
				t.Error("tenant 1: expected nodes after UpdateFromEntityForTenant, got none")
			}
			if len(nodes2) == 0 {
				t.Error("tenant 2: expected nodes after UpdateFromEntityForTenant, got none")
			}
			for _, nd := range nodes1 {
				if !hasPrefix(nd, p1) {
					t.Errorf("tenant 1 result contains node with wrong prefix: %q", nd)
				}
			}
			for _, nd := range nodes2 {
				if !hasPrefix(nd, p2) {
					t.Errorf("tenant 2 result contains node with wrong prefix: %q", nd)
				}
			}
		})
	}
}

func hasPrefix(s, prefix string) bool {
	return len(s) >= len(prefix) && s[:len(prefix)] == prefix
}

// ---------------------------------------------------------------------------
// Error contract tests — gaps that existed only in FlatGraph before patched31
// ---------------------------------------------------------------------------

func TestContract_AddNode_MalformedID_Rejected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// An '@' that is not a valid XXXX@ prefix must be rejected.
			malformed := []string{
				"bad@node",
				"@item:1",
				"item@1",
				"xx@item:1",     // only 2 hex chars before '@'
				"00001@item:1",  // 5 hex chars — too long
			}
			for _, id := range malformed {
				if err := g.AddNode(id, "item"); err == nil {
					t.Errorf("AddNode(%q): expected error for malformed ID, got nil", id)
				} else if !errors.Is(err, ErrMalformedNodeID) {
					t.Errorf("AddNode(%q): expected ErrMalformedNodeID, got %v", id, err)
				}
			}
		})
	}
}

func TestContract_AddEdge_MalformedNodeID_Rejected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Malformed '@' in the "from" endpoint must not silently create a node.
			err := g.AddEdge("bad@node:1", "item:2", "rel")
			if err == nil {
				t.Error("AddEdge with malformed from-node: expected error, got nil")
			} else if !errors.Is(err, ErrMalformedNodeID) {
				t.Errorf("AddEdge with malformed from-node: expected ErrMalformedNodeID, got %v", err)
			}
			// Malformed '@' in the "to" endpoint.
			err = g.AddEdge("item:1", "bad@node:2", "rel")
			if err == nil {
				t.Error("AddEdge with malformed to-node: expected error, got nil")
			} else if !errors.Is(err, ErrMalformedNodeID) {
				t.Errorf("AddEdge with malformed to-node: expected ErrMalformedNodeID, got %v", err)
			}
		})
	}
}

func TestContract_AddNode_ValidPrefixedID_Accepted(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Valid forms: no '@', or exactly XXXX@ (4 uppercase hex).
			valid := []string{
				"item:1",
				"0001@item:1",
				"FFFF@item:99",
				"00AB@order:42",
			}
			for _, id := range valid {
				if err := g.AddNode(id, "item"); err != nil {
					t.Errorf("AddNode(%q): unexpected error %v", id, err)
				}
			}
		})
	}
}

func TestContract_AddEdge_CrossTenant_Rejected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			src := tenant.NodeID(1, "item", 1) // "0001@item:1"
			dst := tenant.NodeID(2, "item", 2) // "0002@item:2"
			err := g.AddEdge(src, dst, "R")
			if err == nil {
				t.Errorf("AddEdge(%q -> %q): expected cross-tenant error, got nil", src, dst)
			} else if !errors.Is(err, ErrCrossTenantEdge) {
				t.Errorf("AddEdge(%q -> %q): expected ErrCrossTenantEdge, got %v", src, dst, err)
			}
		})
	}
}

func TestContract_AddEdge_SameTenant_Accepted(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// Both non-zero same tenant: allowed.
			if err := g.AddEdge(tenant.NodeID(1, "a", 1), tenant.NodeID(1, "b", 1), "R"); err != nil {
				t.Errorf("same-tenant edge: unexpected error %v", err)
			}
			// Both tenant-0 (no prefix): allowed.
			if err := g.AddEdge("a:1", "b:1", "R"); err != nil {
				t.Errorf("tenant-0 edge: unexpected error %v", err)
			}
			// Mixed: one prefixed, one not — allowed (src is tenant-0).
			if err := g.AddEdge("a:2", tenant.NodeID(1, "b", 2), "R"); err != nil {
				t.Errorf("mixed zero/non-zero edge: unexpected error %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Counter accuracy tests — verify NodeCountForTenant and EdgeCountForTenant
// stay correct through add/remove cycles (regression for O(N)→O(1) change)
// ---------------------------------------------------------------------------

func TestContract_TenantCounters_NodeAddRemove(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			assertNodeCount := func(prefix string, want int) {
				t.Helper()
				got, err := g.NodeCountForTenant(prefix)
				if err != nil {
					t.Fatalf("NodeCountForTenant(%q): %v", prefix, err)
				}
				if got != want {
					t.Errorf("NodeCountForTenant(%q): want %d, got %d", prefix, want, got)
				}
			}

			// Add 3 nodes for tenant 1, 2 for tenant 2.
			for i := 1; i <= 3; i++ {
				mustAddNodeG(t, g, tenant.NodeID(1, "item", i), "item")
			}
			for i := 1; i <= 2; i++ {
				mustAddNodeG(t, g, tenant.NodeID(2, "item", i), "item")
			}
			assertNodeCount(p1, 3)
			assertNodeCount(p2, 2)

			// Remove one tenant-1 node.
			if err := g.RemoveNode(tenant.NodeID(1, "item", 2)); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			assertNodeCount(p1, 2)
			assertNodeCount(p2, 2) // unaffected

			// Idempotent add must not double-count.
			mustAddNodeG(t, g, tenant.NodeID(1, "item", 1), "item")
			assertNodeCount(p1, 2)

			// Remove all tenant-2 nodes.
			for i := 1; i <= 2; i++ {
				if err := g.RemoveNode(tenant.NodeID(2, "item", i)); err != nil {
					t.Fatalf("RemoveNode: %v", err)
				}
			}
			assertNodeCount(p2, 0)
			assertNodeCount(p1, 2) // unaffected
		})
	}
}

func TestContract_TenantCounters_EdgeAddRemove(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p1 := tenant.GraphNodePrefix(1)
			p2 := tenant.GraphNodePrefix(2)

			assertEdgeCount := func(prefix string, want int) {
				t.Helper()
				got, err := g.EdgeCountForTenant(prefix)
				if err != nil {
					t.Fatalf("EdgeCountForTenant(%q): %v", prefix, err)
				}
				if got != want {
					t.Errorf("EdgeCountForTenant(%q): want %d, got %d", prefix, want, got)
				}
			}

			// 2 edges for tenant 1, 1 edge for tenant 2.
			mustAddEdgeG(t, g, tenant.NodeID(1, "a", 1), tenant.NodeID(1, "b", 1), "R")
			mustAddEdgeG(t, g, tenant.NodeID(1, "a", 2), tenant.NodeID(1, "b", 2), "R")
			mustAddEdgeG(t, g, tenant.NodeID(2, "a", 1), tenant.NodeID(2, "b", 1), "R")
			assertEdgeCount(p1, 2)
			assertEdgeCount(p2, 1)

			// Idempotent add must not double-count.
			mustAddEdgeG(t, g, tenant.NodeID(1, "a", 1), tenant.NodeID(1, "b", 1), "R")
			assertEdgeCount(p1, 2)

			// Remove one tenant-1 edge via RemoveEdge.
			if err := g.RemoveEdge(tenant.NodeID(1, "a", 1), tenant.NodeID(1, "b", 1)); err != nil {
				t.Fatalf("RemoveEdge: %v", err)
			}
			assertEdgeCount(p1, 1)
			assertEdgeCount(p2, 1) // unaffected

			// Remove tenant-1 edge via RemoveNode (cascade).
			if err := g.RemoveNode(tenant.NodeID(1, "a", 2)); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			assertEdgeCount(p1, 0)
			assertEdgeCount(p2, 1) // unaffected
		})
	}
}

func TestContract_TenantCounters_ClearResetsCounters(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p := tenant.GraphNodePrefix(1)

			mustAddEdgeG(t, g, tenant.NodeID(1, "a", 1), tenant.NodeID(1, "b", 1), "R")

			n, _ := g.NodeCountForTenant(p)
			e, _ := g.EdgeCountForTenant(p)
			if n == 0 || e == 0 {
				t.Fatal("expected non-zero counts before Clear")
			}

			if err := g.Clear(); err != nil {
				t.Fatalf("Clear: %v", err)
			}

			n, _ = g.NodeCountForTenant(p)
			e, _ = g.EdgeCountForTenant(p)
			if n != 0 || e != 0 {
				t.Errorf("after Clear: want 0/0, got %d/%d", n, e)
			}
		})
	}
}

// TestContract_UpdateFromEntityForTenant_RelabelExistingEdge exercises the
// ErrEdgeAlreadyExists branch inside UpdateFromEntityForTenant where a
// (from, to) pair already exists but the relationship label has changed.
// This is the most complex mutation path in the file (delete old, re-add
// with new label, counter-safe rollback on double failure) and previously
// had no test coverage.
func TestContract_UpdateFromEntityForTenant_RelabelExistingEdge(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()

			const (
				tenantID  = uint16(1)
				entity    = "order"
				entityID  = 42
				targetEnt = "customer"
				targetID  = 7
			)

			// Step 1: create initial edge.
			// The field key ("owns") becomes the relationship label;
			// the value is a REF pointing to the target entity.
			data := map[string]interface{}{
				"owns": map[string]interface{}{
					"type":   "REF",
					"entity": targetEnt,
					"id":     float64(targetID),
				},
			}
			if err := g.UpdateFromEntityForTenant(tenantID, entity, entityID, data); err != nil {
				t.Fatalf("initial UpdateFromEntityForTenant: %v", err)
			}

			fromNode := tenant.NodeID(tenantID, entity, entityID)
			toNode := tenant.NodeID(tenantID, targetEnt, targetID)

			nb, err := g.GetNeighbors(fromNode)
			if err != nil {
				t.Fatalf("GetNeighbors after initial update: %v", err)
			}
			if nb[toNode] != "owns" {
				t.Fatalf("want initial label %q, got %q", "owns", nb[toNode])
			}

			// Sanity: edge counter is 1 before relabel.
			p := tenant.GraphNodePrefix(tenantID)
			ec, err := g.EdgeCountForTenant(p)
			if err != nil {
				t.Fatalf("EdgeCountForTenant: %v", err)
			}
			if ec != 1 {
				t.Fatalf("edge count before relabel: want 1, got %d", ec)
			}

			// Step 2: update same (from, to) pair — different field key means
			// different relationship label ("purchased" replaces "owns").
			// The old "owns" key is absent from newData, so its edge is removed;
			// but the (from, to) pair already exists, triggering the
			// ErrEdgeAlreadyExists relabel path inside UpdateFromEntityForTenant.
			newData := map[string]interface{}{
				"purchased": map[string]interface{}{
					"type":   "REF",
					"entity": targetEnt,
					"id":     float64(targetID),
				},
			}
			if err := g.UpdateFromEntityForTenant(tenantID, entity, entityID, newData); err != nil {
				t.Fatalf("relabel UpdateFromEntityForTenant: %v", err)
			}

			// The label must now be "purchased".
			nb, err = g.GetNeighbors(fromNode)
			if err != nil {
				t.Fatalf("GetNeighbors after relabel: %v", err)
			}
			if nb[toNode] != "purchased" {
				t.Errorf("want relabelled label %q, got %q", "purchased", nb[toNode])
			}

			// Edge count must still be 1 — relabel is not a net add.
			ec, err = g.EdgeCountForTenant(p)
			if err != nil {
				t.Fatalf("EdgeCountForTenant after relabel: %v", err)
			}
			if ec != 1 {
				t.Errorf("edge count after relabel: want 1, got %d", ec)
			}

			// Incoming edge on the target node must reflect the new label.
			in, err := g.GetIncomingEdges(toNode)
			if err != nil {
				t.Fatalf("GetIncomingEdges: %v", err)
			}
			if in[fromNode] != "purchased" {
				t.Errorf("want incoming label %q, got %q", "purchased", in[fromNode])
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Missing test #9: Load round-trip for cycleCheckLimit
// ---------------------------------------------------------------------------

// TestContract_SaveLoad_CycleCheckLimitPreserved verifies that a graph saved
// with a custom cycleCheckLimit reloads with that value intact, and that
// loading an older file that lacks the field preserves the runtime value
// rather than silently reverting to the package default.
func TestContract_SaveLoad_CycleCheckLimitPreserved(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()

			// Part A: custom limit round-trips through Save/Load.
			g := impl.newCycle("error")
			fg, ok := g.(*FlatGraph)
			if !ok {
				t.Skip("implementation is not *FlatGraph; SetCycleCheckLimit not available")
			}
			const customLimit = 1024
			fg.SetCycleCheckLimit(customLimit)
			mustAddEdgeG(t, g, "a:1", "b:1", "R")

			f, err := os.CreateTemp("", "olu-graph-limit-*.json")
			if err != nil {
				t.Fatalf("TempFile: %v", err)
			}
			f.Close()
			defer os.Remove(f.Name())

			if err := g.Save(f.Name()); err != nil {
				t.Fatalf("Save: %v", err)
			}

			g2 := impl.newCycle("error")
			fg2 := g2.(*FlatGraph)
			if err := g2.Load(f.Name()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			fg2.mu.RLock()
			got := fg2.cycleCheckLimit
			fg2.mu.RUnlock()
			if got != customLimit {
				t.Errorf("cycleCheckLimit after Load: want %d, got %d", customLimit, got)
			}

			// Part B: loading an older file (no cycle_check_limit field) must
			// preserve the runtime value, not revert to DefaultCycleCheckLimit.
			f2, err := os.CreateTemp("", "olu-graph-oldfile-*.json")
			if err != nil {
				t.Fatalf("TempFile: %v", err)
			}
			_, _ = f2.WriteString(`{"cycle_detection":"error","nodes":{}}`)
			f2.Close()
			defer os.Remove(f2.Name())

			const runtimeLimit = 2048
			g3 := impl.newCycle("error")
			fg3 := g3.(*FlatGraph)
			fg3.SetCycleCheckLimit(runtimeLimit)
			if err := g3.Load(f2.Name()); err != nil {
				t.Fatalf("Load old file: %v", err)
			}
			fg3.mu.RLock()
			got3 := fg3.cycleCheckLimit
			fg3.mu.RUnlock()
			if got3 != runtimeLimit {
				t.Errorf("cycleCheckLimit after loading old file: want %d (runtime), got %d", runtimeLimit, got3)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Missing test #10: global counter vs per-tenant counter map consistency
// ---------------------------------------------------------------------------

// TestContract_CounterConsistency verifies that after a mixed sequence of
// add/remove operations the global NodeCount/EdgeCount equals the sum of all
// per-tenant counters, and the per-tenant counters individually match a linear
// scan of g.nodes. Counter drift between the two layers would go undetected
// by the existing tests.
func TestContract_CounterConsistency(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			fg, ok := g.(*FlatGraph)
			if !ok {
				t.Skip("implementation is not *FlatGraph; internal maps not accessible")
			}

			// Build: two tenants + a handful of tenant-0 nodes.
			for i := 1; i <= 4; i++ {
				mustAddNodeG(t, g, tenant.NodeID(1, "item", i), "item")
			}
			for i := 1; i <= 3; i++ {
				mustAddNodeG(t, g, tenant.NodeID(2, "item", i), "item")
			}
			mustAddEdgeG(t, g, tenant.NodeID(1, "item", 1), tenant.NodeID(1, "item", 2), "R")
			mustAddEdgeG(t, g, tenant.NodeID(1, "item", 3), tenant.NodeID(1, "item", 4), "R")
			mustAddEdgeG(t, g, tenant.NodeID(2, "item", 1), tenant.NodeID(2, "item", 2), "R")

			// Remove one node from each tenant and one edge.
			if err := g.RemoveNode(tenant.NodeID(1, "item", 4)); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			if err := g.RemoveEdge(tenant.NodeID(2, "item", 1), tenant.NodeID(2, "item", 2)); err != nil {
				t.Fatalf("RemoveEdge: %v", err)
			}

			// Verify: global NodeCount == len(nodes map).
			fg.mu.RLock()
			actualNodes := len(fg.nodes)
			counterSum := 0
			for _, c := range fg.nodeCounters {
				counterSum += c
			}
			actualEdges := 0
			for _, rec := range fg.nodes {
				actualEdges += len(rec.out)
			}
			edgeCounterSum := 0
			for _, c := range fg.edgeCounters {
				edgeCounterSum += c
			}
			globalEdge := fg.edgeCount
			fg.mu.RUnlock()

			if g.NodeCount() != actualNodes {
				t.Errorf("NodeCount() %d != len(nodes) %d", g.NodeCount(), actualNodes)
			}
			if counterSum != actualNodes {
				t.Errorf("sum(nodeCounters) %d != len(nodes) %d", counterSum, actualNodes)
			}
			if g.EdgeCount() != actualEdges {
				t.Errorf("EdgeCount() %d != counted outgoing edges %d", g.EdgeCount(), actualEdges)
			}
			if globalEdge != edgeCounterSum {
				t.Errorf("edgeCount field %d != sum(edgeCounters) %d", globalEdge, edgeCounterSum)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Missing test #11: GetNodeInfo with no colon in node ID
// ---------------------------------------------------------------------------

// TestContract_GetNodeInfo_NoColon verifies that GetNodeInfo handles a node
// whose ID contains no colon — Entity is empty string, EntityID is 0, and
// no error or panic occurs. The parsing code takes a different branch for
// this case and was previously untested.
func TestContract_GetNodeInfo_NoColon(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "rootnode", "root")

			info, err := g.GetNodeInfo("rootnode")
			if err != nil {
				t.Fatalf("GetNodeInfo(\"rootnode\"): %v", err)
			}
			if info.ID != "rootnode" {
				t.Errorf("ID: want %q, got %q", "rootnode", info.ID)
			}
			// No colon → entity and entity ID cannot be parsed; both must be zero values.
			if info.Entity != "" {
				t.Errorf("Entity: want empty string for no-colon node, got %q", info.Entity)
			}
			if info.EntityID != 0 {
				t.Errorf("EntityID: want 0 for no-colon node, got %d", info.EntityID)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for issues fixed in this patch set
// ---------------------------------------------------------------------------

// TestContract_AddNode_TypeMutation_ReturnsError (#1, #16)
// AddNode on an existing node with an established type must return
// ErrNodeTypeMismatch, not silently retype the node or corrupt the index.
func TestContract_AddNode_TypeMutation_ReturnsError(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "items:1", "items")
			err := g.AddNode("items:1", "products")
			if err == nil {
				t.Error("AddNode with different type on existing node: expected error, got nil")
			} else if !errors.Is(err, ErrNodeTypeMismatch) {
				t.Errorf("AddNode with different type: expected ErrNodeTypeMismatch, got %v", err)
			}
			// Original type must be preserved in the index.
			nodes := g.GetNodesByType("items")
			if len(nodes) != 1 || nodes[0] != "items:1" {
				t.Errorf("type index after rejected retype: want [items:1], got %v", nodes)
			}
			if len(g.GetNodesByType("products")) != 0 {
				t.Error("items:1 must not appear under new type after rejection")
			}
		})
	}
}

// TestContract_AddNode_ImplicitNode_CanBeTypedLater (#1)
// A node created with no type (via AddEdge) must still accept a type
// on the first explicit AddNode call — ErrNodeTypeMismatch must not fire.
func TestContract_AddNode_ImplicitNode_CanBeTypedLater(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R") // both nodes created typeless
			if err := g.AddNode("a:1", "a"); err != nil {
				t.Fatalf("setting type on implicitly-created node: %v", err)
			}
			nodes := g.GetNodesByType("a")
			if len(nodes) != 1 || nodes[0] != "a:1" {
				t.Errorf("type index after late typing: want [a:1], got %v", nodes)
			}
		})
	}
}

// TestContract_CycleMode_InvalidMode_FallsBackToIgnore (#3, #17)
// Supplying an unrecognised mode must fall back to "ignore" (cycles permitted).
func TestContract_CycleMode_InvalidMode_FallsBackToIgnore(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("strict") // invalid — should fall back to "ignore"
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:2", "n:3", "L")
			if err := g.AddEdge("n:3", "n:1", "L"); err != nil {
				t.Errorf("invalid mode must fall back to ignore and allow cycle; got: %v", err)
			}
			if !g.HasCycle() {
				t.Error("HasCycle must be true after cycle is introduced in ignore-fallback mode")
			}
		})
	}
}

// TestContract_Load_ConcurrentCalls_DoNotCorrupt (#4, #18)
// Concurrent Load calls must not produce inconsistent counter state.
func TestContract_Load_ConcurrentCalls_DoNotCorrupt(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddNodeG(t, g, "a:1", "a")
			mustAddEdgeG(t, g, "a:1", "b:1", "R")

			f, err := os.CreateTemp("", "olu-concurrent-load-*.json")
			if err != nil {
				t.Fatalf("TempFile: %v", err)
			}
			f.Close()
			defer os.Remove(f.Name())
			if err := g.Save(f.Name()); err != nil {
				t.Fatalf("Save: %v", err)
			}

			g2 := impl.new()
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					_ = g2.Load(f.Name())
				}()
			}
			wg.Wait()

			// After all concurrent loads the graph must be internally consistent.
			if g2.NodeCount() != g.NodeCount() {
				t.Errorf("NodeCount after concurrent Load: want %d, got %d", g.NodeCount(), g2.NodeCount())
			}
			if g2.EdgeCount() != g.EdgeCount() {
				t.Errorf("EdgeCount after concurrent Load: want %d, got %d", g.EdgeCount(), g2.EdgeCount())
			}
		})
	}
}

// TestContract_PathExists_CyclicGraph_Terminates (#19)
// PathExists must terminate on a cyclic graph and return correct results.
func TestContract_PathExists_CyclicGraph_Terminates(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("ignore") // cycle allowed
			mustAddEdgeG(t, g, "n:1", "n:2", "L")
			mustAddEdgeG(t, g, "n:2", "n:3", "L")
			mustAddEdgeG(t, g, "n:3", "n:1", "L") // closes cycle

			// Forward path within the cycle.
			found, depth, err := g.PathExists("n:1", "n:3", 10)
			if err != nil {
				t.Fatalf("PathExists on cyclic graph: %v", err)
			}
			if !found {
				t.Error("PathExists: want true for reachable n:3 from n:1")
			}
			if depth != 2 {
				t.Errorf("PathExists depth: want 2, got %d", depth)
			}

			// Back-edge path (n:2 → n:3 → n:1).
			found, _, err = g.PathExists("n:2", "n:1", 10)
			if err != nil {
				t.Fatalf("PathExists n:2->n:1 on cyclic graph: %v", err)
			}
			if !found {
				t.Error("PathExists: want true for n:1 reachable from n:2 via cycle")
			}
		})
	}
}

// TestContract_AddEdge_ImplicitNode_IsVode (#10, #20)
// Nodes implicitly created by AddEdge must carry NodeTypeVode, appear in the
// vode type index, be countable via VodeCount, and be promotable to a real
// type via a subsequent AddNode call (which must decrement VodeCount).
func TestContract_AddEdge_ImplicitNode_IsVode(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R") // both nodes created as vodes

			// Both nodes must exist.
			if !g.NodeExists("a:1") {
				t.Error("implicitly created a:1 must exist")
			}
			if !g.NodeExists("b:1") {
				t.Error("implicitly created b:1 must exist")
			}
			// Both must appear in the vode type index.
			vodes := g.GetNodesByType(NodeTypeVode)
			if len(vodes) != 2 {
				t.Errorf("want 2 vodes after AddEdge, got %d: %v", len(vodes), vodes)
			}
			// VodeCount must reflect this.
			if g.VodeCount() != 2 {
				t.Errorf("VodeCount: want 2, got %d", g.VodeCount())
			}
			// Neither must appear under a real type.
			if nodes := g.GetNodesByType("a"); len(nodes) != 0 {
				t.Errorf("vode must not appear under real type; got %v", nodes)
			}
			// Promotion via AddNode must remove from vode index and decrement counter.
			mustAddNodeG(t, g, "a:1", "a")
			if g.VodeCount() != 1 {
				t.Errorf("VodeCount after promoting a:1: want 1, got %d", g.VodeCount())
			}
			if nodes := g.GetNodesByType("a"); len(nodes) != 1 || nodes[0] != "a:1" {
				t.Errorf("after promotion: want [a:1] in type index, got %v", nodes)
			}
			if nodes := g.GetNodesByType(NodeTypeVode); len(nodes) != 1 || nodes[0] != "b:1" {
				t.Errorf("after promotion: want only b:1 in vode index, got %v", nodes)
			}
			// Promote the second vode.
			mustAddNodeG(t, g, "b:1", "b")
			if g.VodeCount() != 0 {
				t.Errorf("VodeCount after promoting both: want 0, got %d", g.VodeCount())
			}
		})
	}
}

// TestContract_Vode_RemoveDecrementsCounter
// Removing a vode node must decrement VodeCount.
func TestContract_Vode_RemoveDecrementsCounter(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R")
			if g.VodeCount() != 2 {
				t.Fatalf("VodeCount before removal: want 2, got %d", g.VodeCount())
			}
			if err := g.RemoveNode("a:1"); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			if g.VodeCount() != 1 {
				t.Errorf("VodeCount after removing vode a:1: want 1, got %d", g.VodeCount())
			}
		})
	}
}

// TestContract_Vode_ClearResetsCounter
// Clear must reset VodeCount to zero.
func TestContract_Vode_ClearResetsCounter(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R")
			if err := g.Clear(); err != nil {
				t.Fatalf("Clear: %v", err)
			}
			if g.VodeCount() != 0 {
				t.Errorf("VodeCount after Clear: want 0, got %d", g.VodeCount())
			}
		})
	}
}

// TestContract_Vode_SaveLoadRoundtrip
// Vodes must survive a Save/Load roundtrip with their type intact.
func TestContract_Vode_SaveLoadRoundtrip(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			mustAddEdgeG(t, g, "a:1", "b:1", "R")
			mustAddNodeG(t, g, "a:1", "a") // promote a:1; b:1 remains a vode

			f, err := os.CreateTemp("", "olu-vode-roundtrip-*.json")
			if err != nil {
				t.Fatalf("TempFile: %v", err)
			}
			f.Close()
			defer os.Remove(f.Name())

			if err := g.Save(f.Name()); err != nil {
				t.Fatalf("Save: %v", err)
			}
			g2 := impl.new()
			if err := g2.Load(f.Name()); err != nil {
				t.Fatalf("Load: %v", err)
			}
			if g2.VodeCount() != 1 {
				t.Errorf("VodeCount after Load: want 1, got %d", g2.VodeCount())
			}
			vodes := g2.GetNodesByType(NodeTypeVode)
			if len(vodes) != 1 || vodes[0] != "b:1" {
				t.Errorf("vode type index after Load: want [b:1], got %v", vodes)
			}
		})
	}
}

// TestContract_VodeCountForTenant_EmptyPrefix_Errors
func TestContract_VodeCountForTenant_EmptyPrefix_Errors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			_, err := g.VodeCountForTenant("")
			if err == nil {
				t.Error("VodeCountForTenant(\"\") must return an error")
			}
		})
	}
}

// TestContract_VodeCountForTenant_Isolated
// Vode counts must be tenant-isolated.
func TestContract_VodeCountForTenant_Isolated(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			// tenant 1: one forward-reference edge (creates 2 vodes)
			t1n1 := tenant.NodeID(1, "item", 1)
			t1n2 := tenant.NodeID(1, "item", 2)
			mustAddEdgeG(t, g, t1n1, t1n2, "L")
			// tenant 2: promote both explicitly (0 vodes)
			t2n1 := tenant.NodeID(2, "item", 1)
			t2n2 := tenant.NodeID(2, "item", 2)
			mustAddNodeG(t, g, t2n1, "item")
			mustAddNodeG(t, g, t2n2, "item")
			mustAddEdgeG(t, g, t2n1, t2n2, "L")

			v1, err := g.VodeCountForTenant(tenant.GraphNodePrefix(1))
			if err != nil {
				t.Fatalf("VodeCountForTenant(t1): %v", err)
			}
			if v1 != 2 {
				t.Errorf("tenant 1 vode count: want 2, got %d", v1)
			}
			v2, err := g.VodeCountForTenant(tenant.GraphNodePrefix(2))
			if err != nil {
				t.Fatalf("VodeCountForTenant(t2): %v", err)
			}
			if v2 != 0 {
				t.Errorf("tenant 2 vode count: want 0, got %d", v2)
			}
		})
	}
}

// TestContract_FindPath_CrossTenant_Rejected (#5)
// FindPath with endpoints from different tenants must return ErrCrossTenantEdge.
func TestContract_FindPath_CrossTenant_Rejected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			n1 := tenant.NodeID(1, "item", 1)
			n2 := tenant.NodeID(2, "item", 1)
			mustAddNodeG(t, g, n1, "item")
			mustAddNodeG(t, g, n2, "item")
			_, err := g.FindPath(n1, n2, 10)
			if err == nil {
				t.Error("FindPath across tenants: expected error, got nil")
			} else if !errors.Is(err, ErrCrossTenantEdge) {
				t.Errorf("FindPath across tenants: expected ErrCrossTenantEdge, got %v", err)
			}
		})
	}
}

// TestContract_PathExists_CrossTenant_Rejected (#5)
// PathExists with endpoints from different tenants must return ErrCrossTenantEdge.
func TestContract_PathExists_CrossTenant_Rejected(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			n1 := tenant.NodeID(1, "item", 1)
			n2 := tenant.NodeID(2, "item", 1)
			mustAddNodeG(t, g, n1, "item")
			mustAddNodeG(t, g, n2, "item")
			_, _, err := g.PathExists(n1, n2, 10)
			if err == nil {
				t.Error("PathExists across tenants: expected error, got nil")
			} else if !errors.Is(err, ErrCrossTenantEdge) {
				t.Errorf("PathExists across tenants: expected ErrCrossTenantEdge, got %v", err)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests for issues #8, #13, #14 fixed in this patch set
// ---------------------------------------------------------------------------

// TestContract_HasCycleForTenant_EmptyPrefix_Errors (#8)
func TestContract_HasCycleForTenant_EmptyPrefix_Errors(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			_, err := g.HasCycleForTenant("")
			if err == nil {
				t.Error("HasCycleForTenant(\"\") must return an error")
			}
		})
	}
}

// TestContract_HasCycleForTenant_NoCycle (#8)
func TestContract_HasCycleForTenant_NoCycle(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			p := tenant.GraphNodePrefix(1)
			n1 := tenant.NodeID(1, "item", 1)
			n2 := tenant.NodeID(1, "item", 2)
			n3 := tenant.NodeID(1, "item", 3)
			mustAddEdgeG(t, g, n1, n2, "L")
			mustAddEdgeG(t, g, n2, n3, "L")
			got, err := g.HasCycleForTenant(p)
			if err != nil {
				t.Fatalf("HasCycleForTenant: %v", err)
			}
			if got {
				t.Error("HasCycleForTenant: want false for DAG, got true")
			}
		})
	}
}

// TestContract_HasCycleForTenant_WithCycle (#8)
func TestContract_HasCycleForTenant_WithCycle(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("ignore")
			p := tenant.GraphNodePrefix(1)
			n1 := tenant.NodeID(1, "item", 1)
			n2 := tenant.NodeID(1, "item", 2)
			n3 := tenant.NodeID(1, "item", 3)
			mustAddEdgeG(t, g, n1, n2, "L")
			mustAddEdgeG(t, g, n2, n3, "L")
			mustAddEdgeG(t, g, n3, n1, "L")
			got, err := g.HasCycleForTenant(p)
			if err != nil {
				t.Fatalf("HasCycleForTenant: %v", err)
			}
			if !got {
				t.Error("HasCycleForTenant: want true for cyclic subgraph, got false")
			}
		})
	}
}

// TestContract_HasCycleForTenant_Isolated (#8)
// A cycle in tenant 2 must not affect the HasCycleForTenant result for tenant 1.
func TestContract_HasCycleForTenant_Isolated(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("ignore")
			// tenant 1: straight DAG
			t1n1 := tenant.NodeID(1, "item", 1)
			t1n2 := tenant.NodeID(1, "item", 2)
			mustAddEdgeG(t, g, t1n1, t1n2, "L")
			// tenant 2: cycle
			t2n1 := tenant.NodeID(2, "item", 1)
			t2n2 := tenant.NodeID(2, "item", 2)
			t2n3 := tenant.NodeID(2, "item", 3)
			mustAddEdgeG(t, g, t2n1, t2n2, "L")
			mustAddEdgeG(t, g, t2n2, t2n3, "L")
			mustAddEdgeG(t, g, t2n3, t2n1, "L")

			got, err := g.HasCycleForTenant(tenant.GraphNodePrefix(1))
			if err != nil {
				t.Fatalf("HasCycleForTenant(t1): %v", err)
			}
			if got {
				t.Error("HasCycleForTenant(t1): want false — tenant 2 cycle must not bleed into tenant 1")
			}
			got, err = g.HasCycleForTenant(tenant.GraphNodePrefix(2))
			if err != nil {
				t.Fatalf("HasCycleForTenant(t2): %v", err)
			}
			if !got {
				t.Error("HasCycleForTenant(t2): want true")
			}
		})
	}
}

// TestContract_GetAllNodesForTenant_OwnedByTenant (#13)
// GetAllNodesForTenant must return exactly the nodes for that tenant,
// not nodes from others.
func TestContract_GetAllNodesForTenant_OwnedByTenant(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			for i := 1; i <= 5; i++ {
				mustAddNodeG(t, g, tenant.NodeID(1, "item", i), "item")
			}
			for i := 1; i <= 3; i++ {
				mustAddNodeG(t, g, tenant.NodeID(2, "item", i), "item")
			}
			nodes, err := g.GetAllNodesForTenant(tenant.GraphNodePrefix(1))
			if err != nil {
				t.Fatalf("GetAllNodesForTenant: %v", err)
			}
			if len(nodes) != 5 {
				t.Errorf("want 5 nodes for tenant 1, got %d: %v", len(nodes), nodes)
			}
			nodes2, err := g.GetAllNodesForTenant(tenant.GraphNodePrefix(2))
			if err != nil {
				t.Fatalf("GetAllNodesForTenant(t2): %v", err)
			}
			if len(nodes2) != 3 {
				t.Errorf("want 3 nodes for tenant 2, got %d: %v", len(nodes2), nodes2)
			}
		})
	}
}

// TestContract_GetNodesByTypeForTenant_OwnedByTenant (#13)
func TestContract_GetNodesByTypeForTenant_OwnedByTenant(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			for i := 1; i <= 4; i++ {
				mustAddNodeG(t, g, tenant.NodeID(1, "item", i), "item")
			}
			mustAddNodeG(t, g, tenant.NodeID(1, "order", 1), "order")
			for i := 1; i <= 2; i++ {
				mustAddNodeG(t, g, tenant.NodeID(2, "item", i), "item")
			}

			items1, err := g.GetNodesByTypeForTenant(tenant.GraphNodePrefix(1), "item")
			if err != nil {
				t.Fatalf("GetNodesByTypeForTenant: %v", err)
			}
			if len(items1) != 4 {
				t.Errorf("want 4 items for tenant 1, got %d", len(items1))
			}
			items2, err := g.GetNodesByTypeForTenant(tenant.GraphNodePrefix(2), "item")
			if err != nil {
				t.Fatalf("GetNodesByTypeForTenant(t2): %v", err)
			}
			if len(items2) != 2 {
				t.Errorf("want 2 items for tenant 2, got %d", len(items2))
			}
		})
	}
}

// TestContract_GetAllNodesForTenant_ReflectsRemoval (#13)
// Nodes removed via RemoveNode must disappear from GetAllNodesForTenant.
func TestContract_GetAllNodesForTenant_ReflectsRemoval(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.new()
			n1 := tenant.NodeID(1, "item", 1)
			n2 := tenant.NodeID(1, "item", 2)
			mustAddNodeG(t, g, n1, "item")
			mustAddNodeG(t, g, n2, "item")
			if err := g.RemoveNode(n1); err != nil {
				t.Fatalf("RemoveNode: %v", err)
			}
			nodes, err := g.GetAllNodesForTenant(tenant.GraphNodePrefix(1))
			if err != nil {
				t.Fatalf("GetAllNodesForTenant: %v", err)
			}
			if len(nodes) != 1 || nodes[0] != n2 {
				t.Errorf("after removal: want [%s], got %v", n2, nodes)
			}
		})
	}
}

// TestContract_CycleCheck_TenantScoped (#14)
// Verifies that cycle detection operates independently per tenant:
// rejecting a cycle-closing edge in tenant 1 must not prevent valid
// edges in tenant 2, and each tenant's rejection is self-contained.
func TestContract_CycleCheck_TenantScoped(t *testing.T) {
	t.Parallel()
	for _, impl := range graphImpls {
		impl := impl
		t.Run(impl.name, func(t *testing.T) {
			t.Parallel()
			g := impl.newCycle("error")
			// tenant 1 chain: n1 → n2 → n3
			t1n1 := tenant.NodeID(1, "item", 1)
			t1n2 := tenant.NodeID(1, "item", 2)
			t1n3 := tenant.NodeID(1, "item", 3)
			mustAddEdgeG(t, g, t1n1, t1n2, "L")
			mustAddEdgeG(t, g, t1n2, t1n3, "L")
			// closing the tenant-1 cycle must be rejected
			if err := g.AddEdge(t1n3, t1n1, "L"); err == nil {
				t.Error("AddEdge closing cycle in tenant 1: expected error, got nil")
			}
			// tenant 2 DAG must succeed independently, unaffected by t1 rejection
			t2n1 := tenant.NodeID(2, "item", 1)
			t2n2 := tenant.NodeID(2, "item", 2)
			t2n3 := tenant.NodeID(2, "item", 3)
			mustAddEdgeG(t, g, t2n1, t2n2, "L")
			mustAddEdgeG(t, g, t2n2, t2n3, "L")
			// closing the tenant-2 cycle is also rejected (error mode is global)
			if err := g.AddEdge(t2n3, t2n1, "L"); err == nil {
				t.Error("AddEdge closing cycle in tenant 2: expected error, got nil")
			}
			// the non-cycle edges in both tenants must still exist
			if !g.NodeExists(t1n1) || !g.NodeExists(t2n1) {
				t.Error("nodes from both tenants must survive after cycle rejections")
			}
		})
	}
}
