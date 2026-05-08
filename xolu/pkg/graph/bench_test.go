// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package graph

// Benchmarks for FlatGraph across core operations.
//
// Run with:
//
//	go test -run=^$ -bench=. -benchmem ./pkg/graph/
//
// To compare specific operations:
//
//	go test -run=^$ -bench=BenchmarkAddNode -benchmem ./pkg/graph/

import (
	"fmt"
	"testing"
)

// ── Builder ───────────────────────────────────────────────────────────────────

func buildFlatGraph(nodes, edgesPerNode int) *FlatGraph {
	g := NewFlatGraph()
	types := []string{"device", "gateway", "concentrator", "location", "zone"}
	rels  := []string{"REPORTS_TO", "LOCATED_IN", "MEMBER_OF", "MONITORS"}
	ng := nodes/50 + 1
	for i := 0; i < nodes; i++ {
		typ := types[i%len(types)]
		from := fmt.Sprintf("device:%d", i)
		_ = g.AddNode(from, typ)
		for e := 0; e < edgesPerNode; e++ {
			to := fmt.Sprintf("gateway:%d", (i*edgesPerNode+e)%ng)
			_ = g.AddEdge(from, to, rels[e%len(rels)])
		}
	}
	return g
}

// ── AddNode ───────────────────────────────────────────────────────────────────

func BenchmarkAddNode(b *testing.B) {
	g := NewFlatGraph()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.AddNode(fmt.Sprintf("device:%d", i), "device")
	}
}

// ── AddEdge ───────────────────────────────────────────────────────────────────

func BenchmarkAddEdge(b *testing.B) {
	g := buildFlatGraph(1000, 0)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		from := fmt.Sprintf("device:%d", i%1000)
		to   := fmt.Sprintf("device:%d", (i+1)%1000)
		_ = g.AddEdge(from, to, "LINK")
	}
}

// ── GetNeighbors ──────────────────────────────────────────────────────────────

func BenchmarkGetNeighbors(b *testing.B) {
	g := buildFlatGraph(10000, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.GetNeighbors(fmt.Sprintf("device:%d", i%10000))
	}
}

// ── GetNodesByType ────────────────────────────────────────────────────────────

func BenchmarkGetNodesByType(b *testing.B) {
	g := buildFlatGraph(10000, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.GetNodesByType("device")
	}
}

// ── RemoveNode ────────────────────────────────────────────────────────────────

// BenchmarkRemoveNode measures remove+re-add as an atomic unit on a
// pre-built graph, so there is no per-iteration construction cost.
func BenchmarkRemoveNode(b *testing.B) {
	g := buildFlatGraph(1000, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		nodeID := fmt.Sprintf("device:%d", i%1000)
		_ = g.RemoveNode(nodeID)
		_ = g.AddNode(nodeID, "device")
	}
}

// ── FindPath ──────────────────────────────────────────────────────────────────

func BenchmarkFindPath(b *testing.B) {
	g := buildFlatGraph(1000, 4)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _ = g.FindPath("device:0", "device:999", 20)
	}
}

// ── Memory: pre-populated graphs ─────────────────────────────────────────────
// Use -benchmem to observe allocations.

func BenchmarkMemory_1k(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = buildFlatGraph(1000, 4)
	}
}
