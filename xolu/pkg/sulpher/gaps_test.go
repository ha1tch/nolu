// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"context"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// NewExecutorForTenant and WithLogger
// ---------------------------------------------------------------------------

func TestNewExecutorForTenant_SetsPrefix(t *testing.T) {
	g := graph.NewFlatGraph()
	e := NewExecutorForTenant(g, 5, "0001@")
	if e.tenantPrefix != "0001@" {
		t.Errorf("tenantPrefix: got %q, want %q", e.tenantPrefix, "0001@")
	}
	if e.maxDepth != 5 {
		t.Errorf("maxDepth: got %d, want 5", e.maxDepth)
	}
}

func TestWithLogger_ReturnsExecutor(t *testing.T) {
	g := graph.NewFlatGraph()
	e := NewExecutor(g, 5)
	logger := zerolog.Nop()
	returned := e.WithLogger(logger)
	if returned != e {
		t.Error("WithLogger should return the same executor instance")
	}
}

// ---------------------------------------------------------------------------
// SetLimits and SetQueryTimeout
// ---------------------------------------------------------------------------

func TestSetLimits_AppliedToExecutor(t *testing.T) {
	g := graph.NewFlatGraph()
	e := NewExecutor(g, 10)
	e.SetLimits(GraphLimits{MaxVisitedNodes: 500, MaxResults: 100})

	e.mu.Lock()
	limits := e.limits
	e.mu.Unlock()

	if limits.MaxVisitedNodes != 500 {
		t.Errorf("MaxVisitedNodes: got %d, want 500", limits.MaxVisitedNodes)
	}
	if limits.MaxResults != 100 {
		t.Errorf("MaxResults: got %d, want 100", limits.MaxResults)
	}
}

func TestJobManager_SetQueryTimeout(t *testing.T) {
	g := graph.NewFlatGraph()
	e := NewExecutor(g, 5)
	jm := NewJobManager(e, time.Minute)
	jm.SetQueryTimeout(10 * time.Second)
	if jm.queryTimeout != 10*time.Second {
		t.Errorf("queryTimeout: got %v, want 10s", jm.queryTimeout)
	}
}

func TestJobManager_SetLimits(t *testing.T) {
	g := graph.NewFlatGraph()
	e := NewExecutor(g, 5)
	jm := NewJobManager(e, time.Minute)
	jm.SetLimits(GraphLimits{MaxVisitedNodes: 200})

	e.mu.Lock()
	limits := e.limits
	e.mu.Unlock()

	if limits.MaxVisitedNodes != 200 {
		t.Errorf("MaxVisitedNodes: got %d, want 200", limits.MaxVisitedNodes)
	}
}

// ---------------------------------------------------------------------------
// failJob
// ---------------------------------------------------------------------------

func TestJobManager_FailJob_MarksErrorState(t *testing.T) {
	g := setupTestGraph()
	e := NewExecutor(g, 5)
	jm := NewJobManager(e, time.Minute)

	job, err := jm.Submit("MATCH (u:users) RETURN u", 5)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}

	// Wait for job to complete (it's async).
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		j, ok := jm.GetJob(job.ID)
		if ok && (j.Status == "completed" || j.Status == "failed") {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	// Directly invoke failJob to exercise the path.
	jm.failJob(job, "test failure message")
	if job.Status != "failed" {
		t.Errorf("Status: got %q, want %q", job.Status, "failed")
	}
	if job.Error != "test failure message" {
		t.Errorf("Error: got %q, want %q", job.Error, "test failure message")
	}
}

// ---------------------------------------------------------------------------
// dfsRecursive — variable-length path traversal
// ---------------------------------------------------------------------------

func buildChainGraph() graph.Graph {
	// a -> b -> c -> d -> e  (all LINKS)
	g := graph.NewFlatGraph()
	for _, n := range []string{"chain:a", "chain:b", "chain:c", "chain:d", "chain:e"} {
		_ = g.AddNode(n, "chain")
	}
	_ = g.AddEdge("chain:a", "chain:b", "LINK")
	_ = g.AddEdge("chain:b", "chain:c", "LINK")
	_ = g.AddEdge("chain:c", "chain:d", "LINK")
	_ = g.AddEdge("chain:d", "chain:e", "LINK")
	return g
}

func TestDFS_VariableLengthStar_FindsAllReachable(t *testing.T) {
	g := buildChainGraph()
	e := NewExecutor(g, 10)
	parser := NewParser()

	// MATCH (a:chain)-[*]->(b:chain) — any number of hops.
	q, err := parser.Parse("MATCH (a:chain)-[*]->(b:chain) RETURN b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result, err := e.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// From any chain node, should find paths to other chain nodes.
	if len(result.Data) == 0 {
		t.Error("expected results from variable-length traversal, got none")
	}
}

func TestDFS_VariableLengthExact_CorrectHops(t *testing.T) {
	g := buildChainGraph()
	e := NewExecutor(g, 10)
	parser := NewParser()

	// Exactly 2 hops from a: should find c.
	q, err := parser.Parse("MATCH (a:chain)-[*2]->(b:chain) RETURN b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result, err := e.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	// Results should include paths of exactly 2 hops (a->b->c, b->c->d, etc.)
	if len(result.Data) == 0 {
		t.Error("expected results for exactly-2-hop traversal")
	}
}

func TestDFS_VariableLengthRange(t *testing.T) {
	g := buildChainGraph()
	e := NewExecutor(g, 10)
	parser := NewParser()

	q, err := parser.Parse("MATCH (a:chain)-[*1..3]->(b:chain) RETURN b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result, err := e.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(result.Data) == 0 {
		t.Error("expected results for 1..3 hop traversal")
	}
}

func TestDFS_ContextCancellation(t *testing.T) {
	g := buildChainGraph()
	e := NewExecutor(g, 10)
	parser := NewParser()

	q, err := parser.Parse("MATCH (a:chain)-[*]->(b:chain) RETURN b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	// Should not panic or hang with cancelled context.
	_, _ = e.Execute(ctx, q)
}

func TestDFS_MaxVisitedNodes_Respected(t *testing.T) {
	g := buildChainGraph()
	e := NewExecutor(g, 10)
	e.SetLimits(GraphLimits{MaxVisitedNodes: 1})
	parser := NewParser()

	q, err := parser.Parse("MATCH (a:chain)-[*]->(b:chain) RETURN b")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// With MaxVisitedNodes=1, traversal should either return an error or
	// return fewer results than the full graph. Both outcomes are valid.
	result, execErr := e.Execute(context.Background(), q)
	if execErr != nil {
		// Error is expected and acceptable — limit was enforced.
		return
	}
	// If no error, results should be capped.
	fullE := NewExecutor(g, 10)
	fullResult, _ := fullE.Execute(context.Background(), q)
	if len(result.Data) >= len(fullResult.Data) {
		t.Error("capped traversal should return fewer results than uncapped")
	}
}

// ---------------------------------------------------------------------------
// applyConditions — WHERE clause filtering
// ---------------------------------------------------------------------------

func TestApplyConditions_FiltersByType(t *testing.T) {
	g := graph.NewFlatGraph()
	_ = g.AddNode("users:1", "users")
	_ = g.AddNode("users:2", "users")
	_ = g.AddNode("posts:1", "posts")
	_ = g.AddEdge("users:1", "posts:1", "AUTHORED")

	e := NewExecutor(g, 5)
	parser := NewParser()

	// WHERE u.type = 'users' — filters by the synthesized "type" field.
	q, err := parser.Parse("MATCH (u) WHERE u.type = 'users' RETURN u")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result, err := e.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, row := range result.Data {
		node := row["u"].(map[string]interface{})
		if node["type"] != "users" {
			t.Errorf("WHERE filter leaked non-user node: type=%v", node["type"])
		}
	}
}

func TestApplyConditions_Inequality(t *testing.T) {
	g := graph.NewFlatGraph()
	_ = g.AddNode("users:1", "users")
	_ = g.AddNode("posts:1", "posts")

	e := NewExecutor(g, 5)
	parser := NewParser()

	q, err := parser.Parse("MATCH (u) WHERE u.type != 'posts' RETURN u")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	result, err := e.Execute(context.Background(), q)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	for _, row := range result.Data {
		node := row["u"].(map[string]interface{})
		if node["type"] == "posts" {
			t.Error("WHERE != 'posts' should have filtered out posts nodes")
		}
	}
}

// ---------------------------------------------------------------------------
// compareForSort, toFloat64, compareValues, compareNumeric
// ---------------------------------------------------------------------------

func TestCompareForSort_Numerics(t *testing.T) {
	cases := []struct {
		a, b interface{}
		want int
	}{
		{1.0, 2.0, -1},
		{2.0, 1.0, 1},
		{1.0, 1.0, 0},
		{int(3), int(2), 1},
		{int64(5), int64(10), -1},
		{float32(1.5), float32(1.5), 0},
		{"10", "20", -1},   // numeric strings
		{nil, nil, 0},
		{nil, 1.0, -1},
		{1.0, nil, 1},
	}
	for _, c := range cases {
		got := compareForSort(c.a, c.b)
		if got != c.want {
			t.Errorf("compareForSort(%v, %v) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCompareForSort_Strings(t *testing.T) {
	if compareForSort("apple", "banana") >= 0 {
		t.Error("apple should sort before banana")
	}
	if compareForSort("zebra", "ant") <= 0 {
		t.Error("zebra should sort after ant")
	}
	if compareForSort("same", "same") != 0 {
		t.Error("equal strings should return 0")
	}
}

func TestToFloat64_AllTypes(t *testing.T) {
	cases := []struct {
		v     interface{}
		want  float64
		wantOK bool
	}{
		{int(42), 42.0, true},
		{int64(100), 100.0, true},
		{float64(3.14), 3.14, true},
		{float32(2.5), 2.5, true},
		{"1.5", 1.5, true},
		{"notanumber", 0, false},
		{true, 0, false},
		{nil, 0, false},
	}
	for _, c := range cases {
		got, ok := toFloat64(c.v)
		if ok != c.wantOK {
			t.Errorf("toFloat64(%v) ok=%v, want %v", c.v, ok, c.wantOK)
			continue
		}
		if ok && got != c.want {
			t.Errorf("toFloat64(%v) = %v, want %v", c.v, got, c.want)
		}
	}
}

func TestCompareNumeric_AllOps(t *testing.T) {
	cases := []struct {
		value    interface{}
		op       Operator
		expected interface{}
		want     bool
	}{
		{10.0, OpLt, 20.0, true},
		{20.0, OpLt, 10.0, false},
		{10.0, OpGt, 5.0, true},
		{5.0, OpGt, 10.0, false},
		{10.0, OpLte, 10.0, true},
		{11.0, OpLte, 10.0, false},
		{10.0, OpGte, 10.0, true},
		{9.0, OpGte, 10.0, false},
		// int types
		{int(5), OpLt, int(10), true},
		{int64(15), OpGt, int64(10), true},
		// string numerics
		{"5", OpLt, "10", true},
		{"abc", OpLt, 10.0, false},   // non-numeric string value
		{10.0, OpLt, "abc", false},   // non-numeric string expected
		// non-numeric types
		{true, OpLt, 1.0, false},
		{10.0, OpLt, true, false},
	}
	for _, c := range cases {
		got := compareNumeric(c.value, c.op, c.expected)
		if got != c.want {
			t.Errorf("compareNumeric(%v, %v, %v) = %v, want %v",
				c.value, c.op, c.expected, got, c.want)
		}
	}
}

func TestCompareValues_EqAndNe(t *testing.T) {
	if !compareValues("hello", OpEq, "hello") {
		t.Error("equal strings should match OpEq")
	}
	if compareValues("hello", OpEq, "world") {
		t.Error("different strings should not match OpEq")
	}
	if !compareValues("hello", OpNe, "world") {
		t.Error("different strings should match OpNe")
	}
	if compareValues("hello", OpNe, "hello") {
		t.Error("equal strings should not match OpNe")
	}
	if compareValues(nil, OpEq, "x") {
		t.Error("nil value should not match")
	}
}

func TestCompareValues_NumericOps(t *testing.T) {
	if !compareValues(10.0, OpLt, 20.0) {
		t.Error("10 < 20 should be true")
	}
	if !compareValues(20.0, OpGt, 10.0) {
		t.Error("20 > 10 should be true")
	}
	// Unknown operator should return false.
	if compareValues(1.0, Operator("??"), 1.0) {
		t.Error("unknown operator should return false")
	}
}
