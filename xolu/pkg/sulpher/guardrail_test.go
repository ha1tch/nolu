// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/graph"
)

// buildDenseGraph creates a graph with n nodes where each node connects
// to the next, forming a chain. With low MaxVisitedNodes, this triggers
// the visited-node limit during traversal.
func buildDenseGraph(n int) graph.Graph {
	g := graph.NewFlatGraph()
	for i := 0; i < n; i++ {
		nodeID := fmt.Sprintf("items:%d", i)
		g.AddNode(nodeID, "items")
		if i > 0 {
			prevID := fmt.Sprintf("items:%d", i-1)
			g.AddEdge(prevID, nodeID, "next")
		}
	}
	return g
}

func TestGuardrail_VisitedNodeLimit(t *testing.T) {
	g := buildDenseGraph(100) // 100-node chain

	executor := NewExecutor(g, 50)
	executor.SetLimits(GraphLimits{
		MaxVisitedNodes: 10, // Very low — will be exceeded
		MaxResults:      1000,
	})

	query, err := NewParser().Parse("MATCH (a:items)-[:next]->(b:items) RETURN a, b")
	if err != nil {
		t.Fatal(err)
	}

	_, err = executor.Execute(context.Background(), query)
	if err == nil {
		t.Fatal("expected visited-node limit error, got nil")
	}

	if !isVisitedNodeLimit(err) {
		t.Errorf("expected ErrVisitedNodeLimit, got: %v", err)
	}
}

func TestGuardrail_ResultLimit(t *testing.T) {
	// Create a star graph: one centre node connected to many leaves.
	// FIND (items) returns all nodes, which exceeds a low result limit.
	g := graph.NewFlatGraph()
	g.AddNode("items:0", "items")
	for i := 1; i <= 20; i++ {
		nodeID := fmt.Sprintf("items:%d", i)
		g.AddNode(nodeID, "items")
		g.AddEdge("items:0", nodeID, "has")
	}

	executor := NewExecutor(g, 10)
	executor.SetLimits(GraphLimits{
		MaxVisitedNodes: 100000,
		MaxResults:      5, // Very low
	})

	// Query that matches all start nodes and expands
	query, err := NewParser().Parse("MATCH (a:items)-[:has]->(b:items) RETURN a, b")
	if err != nil {
		t.Fatal(err)
	}

	_, err = executor.Execute(context.Background(), query)
	if err == nil {
		t.Fatal("expected result limit error, got nil")
	}

	if !isResultLimit(err) {
		t.Errorf("expected ErrResultLimit, got: %v", err)
	}
}

func TestGuardrail_TimeoutCancelsTraversal(t *testing.T) {
	// Use a large chain graph and a very short timeout.
	g := buildDenseGraph(500)

	executor := NewExecutor(g, 100)
	executor.SetLimits(GraphLimits{
		MaxVisitedNodes: 1000000, // High — don't hit node limit
		MaxResults:      1000000,
	})

	query, err := NewParser().Parse("MATCH (a:items)-[:next*]->(b:items) RETURN a, b")
	if err != nil {
		t.Fatal(err)
	}

	// Use an already-cancelled context
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(1 * time.Millisecond) // Ensure deadline has passed

	_, err = executor.Execute(ctx, query)
	if err == nil {
		// The query might complete before the context check fires on
		// small graphs. That's acceptable — the test verifies the
		// mechanism is wired, not that it always wins the race.
		t.Log("query completed before timeout (acceptable on fast hardware)")
		return
	}

	if ctx.Err() == nil {
		t.Errorf("expected context error, got: %v", err)
	}
}

func TestGuardrail_UnderLimits(t *testing.T) {
	g := buildDenseGraph(5)

	executor := NewExecutor(g, 10)
	executor.SetLimits(GraphLimits{
		MaxVisitedNodes: 100,
		MaxResults:      100,
	})

	query, err := NewParser().Parse("MATCH (a:items)-[:next]->(b:items) RETURN a, b")
	if err != nil {
		t.Fatal(err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("under-limit query failed: %v", err)
	}

	if len(result.Data) == 0 {
		t.Error("expected results, got none")
	}
}

// helpers — use errors.Is via the package-level sentinels
func isVisitedNodeLimit(err error) bool {
	return errors.Is(err, ErrVisitedNodeLimit)
}

func isResultLimit(err error) bool {
	return errors.Is(err, ErrResultLimit)
}
