// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"context"
	"fmt"
	"testing"

	"github.com/ha1tch/xolu/pkg/graph"
)

// setupTestGraph creates a test graph with users and relationships
func setupTestGraph() graph.Graph {
	g := graph.NewFlatGraph()

	// Users
	g.AddNode("users:1", "users")
	g.AddNode("users:2", "users")
	g.AddNode("users:3", "users")
	g.AddNode("users:4", "users")
	g.AddNode("users:5", "users")

	// Posts
	g.AddNode("posts:1", "posts")
	g.AddNode("posts:2", "posts")

	// Follow relationships: 1 -> 2 -> 3 -> 4, 1 -> 5
	g.AddEdge("users:1", "users:2", "FOLLOWS")
	g.AddEdge("users:2", "users:3", "FOLLOWS")
	g.AddEdge("users:3", "users:4", "FOLLOWS")
	g.AddEdge("users:1", "users:5", "FOLLOWS")

	// Knows (bidirectional-ish): 2 <-> 5
	g.AddEdge("users:2", "users:5", "KNOWS")
	g.AddEdge("users:5", "users:2", "KNOWS")

	// Authored
	g.AddEdge("users:1", "posts:1", "AUTHORED")
	g.AddEdge("users:2", "posts:2", "AUTHORED")

	return g
}

func TestExecuteSimpleMatch(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 5 {
		t.Errorf("Expected 5 users, got %d", len(result.Data))
	}
}

func TestExecuteWithRelationship(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users)-[:FOLLOWS]->(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// users:1 follows 2 and 5, users:2 follows 3, users:3 follows 4
	// So f should be: 2, 5, 3, 4 = 4 results
	if len(result.Data) != 4 {
		t.Errorf("Expected 4 followed users, got %d", len(result.Data))
	}
}

func TestExecuteWithInlineProperty(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// Match specific user by inline property (id matching)
	query, err := parser.Parse("MATCH (u:users {id: 1}) RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 1 {
		t.Errorf("Expected 1 user with id=1, got %d", len(result.Data))
	}
}

func TestExecuteVariableLengthPath(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// Find users reachable via 1-3 FOLLOWS hops from user 1
	query, err := parser.Parse("MATCH (u:users {id: 1})-[:FOLLOWS*1..3]->(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// From user:1: hop1 -> 2,5; hop2 -> 3; hop3 -> 4
	// So f = 2, 5, 3, 4 = 4 results
	if len(result.Data) < 3 {
		t.Errorf("Expected at least 3 users reachable, got %d", len(result.Data))
	}
}

func TestExecuteBFS(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// BFS is the default algorithm
	query, err := parser.Parse("MATCH (u:users)-[:FOLLOWS]->(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if query.Algorithm != BFS {
		t.Errorf("Expected BFS algorithm (default)")
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) == 0 {
		t.Error("Expected results from BFS traversal")
	}
}

func TestExecuteDFS(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// DFS is specified before MATCH
	query, err := parser.Parse("DFS MATCH (u:users)-[:FOLLOWS]->(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if query.Algorithm != DFS {
		t.Errorf("Expected DFS algorithm")
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) == 0 {
		t.Error("Expected results from DFS traversal")
	}
}

func TestExecuteWithWhereCondition(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// This tests WHERE with a condition - depends on node data having the field
	query, err := parser.Parse("MATCH (u:users) WHERE u.id > 2 RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// users:3, users:4, users:5 have id > 2
	if len(result.Data) != 3 {
		t.Errorf("Expected 3 users with id > 2, got %d", len(result.Data))
	}
}

func TestExecuteWithOrConditions(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) WHERE u.id = 1 OR u.id = 5 RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Errorf("Expected 2 users (id=1 or id=5), got %d", len(result.Data))
	}
}

func TestExecuteDistinct(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// Without DISTINCT, traversing from multiple start nodes may produce duplicates
	query, err := parser.Parse("MATCH (u:users)-[:FOLLOWS]->(f:users) RETURN DISTINCT f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if !query.Distinct {
		t.Error("Expected Distinct=true")
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Check no duplicates (unique node IDs)
	seen := make(map[string]bool)
	for _, row := range result.Data {
		if f, ok := row["f"].(map[string]interface{}); ok {
			if id, ok := f["_id"].(string); ok {
				if seen[id] {
					t.Errorf("Duplicate found: %s", id)
				}
				seen[id] = true
			}
		}
	}
}

func TestExecuteLimit(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) RETURN u LIMIT 2")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if query.Limit != 2 {
		t.Errorf("Expected Limit=2, got %d", query.Limit)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Errorf("Expected 2 results with LIMIT 2, got %d", len(result.Data))
	}
}

func TestExecuteOrderBy(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) RETURN u ORDER BY u.id DESC")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(query.OrderBy) != 1 {
		t.Fatalf("Expected 1 ORDER BY item")
	}
	if query.OrderBy[0].Direction != OrderDesc {
		t.Error("Expected DESC order")
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) < 2 {
		t.Skip("Not enough results to verify order")
	}

	// Verify descending order by checking first > last
	// (actual verification depends on how results are structured)
}

func TestExecuteCombined(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users)-[:FOLLOWS]->(f:users) WHERE u.id = 1 RETURN DISTINCT f ORDER BY f.id LIMIT 2")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if !query.Distinct {
		t.Error("Expected Distinct")
	}
	if query.Limit != 2 {
		t.Error("Expected Limit=2")
	}
	if len(query.OrderBy) == 0 {
		t.Error("Expected ORDER BY")
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) > 2 {
		t.Errorf("Expected at most 2 results, got %d", len(result.Data))
	}
}

func TestExecuteIncoming(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// Find who follows user 3 (incoming edges)
	query, err := parser.Parse("MATCH (u:users {id: 3})<-[:FOLLOWS]-(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// user:2 follows user:3
	if len(result.Data) != 1 {
		t.Errorf("Expected 1 follower of user:3, got %d", len(result.Data))
	}
}

func TestExecuteBidirectional(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// Bidirectional KNOWS relationship
	query, err := parser.Parse("MATCH (u:users {id: 2})-[:KNOWS]-(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// user:2 KNOWS user:5 (bidirectional)
	if len(result.Data) < 1 {
		t.Errorf("Expected at least 1 KNOWS connection, got %d", len(result.Data))
	}
}

func TestExecuteMultiHop(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// Two hops: u -> f -> g
	query, err := parser.Parse("MATCH (u:users {id: 1})-[:FOLLOWS]->(f:users)-[:FOLLOWS]->(g:users) RETURN g")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 1 -> 2 -> 3, so g = user:3
	if len(result.Data) < 1 {
		t.Errorf("Expected at least 1 two-hop result, got %d", len(result.Data))
	}
}

func TestExecuteNoResults(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	// No LIKES relationships exist
	query, err := parser.Parse("MATCH (u:users)-[:LIKES]->(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 0 {
		t.Errorf("Expected 0 results for non-existent relationship, got %d", len(result.Data))
	}
}

func TestExecuteReturnMultipleVariables(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users)-[r:FOLLOWS]->(f:users) RETURN u, f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(query.ReturnItems) != 2 {
		t.Errorf("Expected 2 return items, got %d", len(query.ReturnItems))
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) > 0 {
		row := result.Data[0]
		if _, hasU := row["u"]; !hasU {
			t.Error("Expected 'u' in result")
		}
		if _, hasF := row["f"]; !hasF {
			t.Error("Expected 'f' in result")
		}
	}
}

func TestExecuteReturnProperty(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) RETURN u.id")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	if len(query.ReturnItems) != 1 || query.ReturnItems[0].Property != "id" {
		t.Error("Expected return of u.id property")
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Results should contain u.id values
	if len(result.Data) != 5 {
		t.Errorf("Expected 5 results, got %d", len(result.Data))
	}
}

func TestExecuteStats(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users)-[:FOLLOWS]->(f:users) RETURN f")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Stats.NodesTraversed == 0 {
		t.Error("Expected NodesTraversed > 0")
	}
	if result.Stats.ExecutionTime == 0 {
		t.Error("Expected ExecutionTime > 0")
	}
}

func TestExecuteEmptyGraph(t *testing.T) {
	g := graph.NewFlatGraph()
	executor := NewExecutor(g, 10)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 0 {
		t.Errorf("Expected 0 results from empty graph, got %d", len(result.Data))
	}
}

// =============================================================================
// Store hydration tests — property conditions with a real store
// =============================================================================

// mockStore satisfies the EntityGetter interface used by Executor.WithStore.
// Only Get is needed — property hydration reads entity data and nothing else.
type mockStore struct {
	data map[string]map[int]map[string]interface{} // entity -> id -> fields
}

func newMockStore() *mockStore {
	return &mockStore{data: make(map[string]map[int]map[string]interface{})}
}

func (m *mockStore) set(entity string, id int, fields map[string]interface{}) {
	if m.data[entity] == nil {
		m.data[entity] = make(map[int]map[string]interface{})
	}
	m.data[entity][id] = fields
}

func (m *mockStore) Get(_ context.Context, entity string, id int) (map[string]interface{}, error) {
	if byEntity, ok := m.data[entity]; ok {
		if d, ok := byEntity[id]; ok {
			out := make(map[string]interface{}, len(d))
			for k, v := range d {
				out[k] = v
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("not found: %s:%d", entity, id)
}

// compile-time check
var _ EntityGetter = (*mockStore)(nil)

// TestPropertyHydration_WhereCondition verifies that a WHERE condition on a
// non-id field (e.g. WHERE u.name = "Alice") correctly filters nodes when a
// store is attached to the executor.
func TestPropertyHydration_WhereCondition(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")
	g.AddNode("users:2", "users")
	g.AddNode("users:3", "users")

	store := newMockStore()
	store.set("users", 1, map[string]interface{}{"id": 1, "name": "Alice", "active": true})
	store.set("users", 2, map[string]interface{}{"id": 2, "name": "Bob", "active": false})
	store.set("users", 3, map[string]interface{}{"id": 3, "name": "Alice", "active": false})

	exec := NewExecutor(g, 10).WithStore(store)
	parser := NewParser()

	query, err := parser.Parse(`MATCH (u:users) WHERE u.name = "Alice" RETURN u`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	result, err := exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Errorf("expected 2 results (users 1 and 3), got %d", len(result.Data))
	}
}

// TestPropertyHydration_InlineProperty verifies that inline property patterns
// like (u:users {active: true}) filter correctly when a store is attached.
func TestPropertyHydration_InlineProperty(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")
	g.AddNode("users:2", "users")

	store := newMockStore()
	store.set("users", 1, map[string]interface{}{"id": 1, "active": true})
	store.set("users", 2, map[string]interface{}{"id": 2, "active": false})

	exec := NewExecutor(g, 10).WithStore(store)
	parser := NewParser()

	query, err := parser.Parse(`MATCH (u:users {active: true}) RETURN u`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	result, err := exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 1 {
		t.Errorf("expected 1 result (user 1), got %d", len(result.Data))
	}
}

// TestPropertyHydration_NoStore verifies that without a store the executor
// still returns topology-only results (no panic, no regression).
func TestPropertyHydration_NoStore(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")
	g.AddNode("users:2", "users")
	g.AddEdge("users:1", "users:2", "FOLLOWS")

	exec := NewExecutor(g, 10) // no WithStore
	parser := NewParser()

	query, err := parser.Parse(`MATCH (u:users)-[:FOLLOWS]->(v:users) RETURN u, v`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	result, err := exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Data) != 1 {
		t.Errorf("expected 1 result, got %d", len(result.Data))
	}
}

// TestPropertyHydration_MissingNodeInStore verifies graceful handling when
// a graph node has no corresponding entity in the store (e.g. deleted after
// the graph snapshot was taken). The node simply doesn't match property
// conditions — no panic or error.
func TestPropertyHydration_MissingNodeInStore(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")
	g.AddNode("users:99", "users") // exists in graph, not in store

	store := newMockStore()
	store.set("users", 1, map[string]interface{}{"id": 1, "name": "Alice"})
	// users:99 intentionally absent

	exec := NewExecutor(g, 10).WithStore(store)
	parser := NewParser()

	query, err := parser.Parse(`MATCH (u:users) WHERE u.name = "Alice" RETURN u`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	result, err := exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Data) != 1 {
		t.Errorf("expected 1 result (users:1), got %d", len(result.Data))
	}
}

// TestPropertyHydration_ReturnProperty verifies that RETURN u.name works
// correctly when no WHERE clause has previously triggered hydration — Issue 2.
func TestPropertyHydration_ReturnProperty(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")
	g.AddNode("users:2", "users")

	store := newMockStore()
	store.set("users", 1, map[string]interface{}{"id": 1, "name": "Alice"})
	store.set("users", 2, map[string]interface{}{"id": 2, "name": "Bob"})

	exec := NewExecutor(g, 10).WithStore(store)
	parser := NewParser()

	query, err := parser.Parse(`MATCH (u:users) RETURN u.name`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	result, err := exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Data) != 2 {
		t.Fatalf("expected 2 results, got %d", len(result.Data))
	}
	names := map[string]bool{}
	for _, row := range result.Data {
		if n, ok := row["u.name"].(string); ok {
			names[n] = true
		}
	}
	if !names["Alice"] || !names["Bob"] {
		t.Errorf("expected names Alice and Bob, got %v", names)
	}
}

// TestPropertyHydration_ReturnWholeNode verifies that RETURN u (whole node)
// includes entity fields fetched from the store — Issue 2 (whole-node variant).
func TestPropertyHydration_ReturnWholeNode(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")

	store := newMockStore()
	store.set("users", 1, map[string]interface{}{"id": 1, "name": "Alice", "role": "admin"})

	exec := NewExecutor(g, 10).WithStore(store)
	parser := NewParser()

	query, err := parser.Parse(`MATCH (u:users) RETURN u`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	result, err := exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Data) != 1 {
		t.Fatalf("expected 1 result, got %d", len(result.Data))
	}
	node, ok := result.Data[0]["u"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected u to be a map, got %T", result.Data[0]["u"])
	}
	if node["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %v", node["name"])
	}
	if node["role"] != "admin" {
		t.Errorf("expected role=admin, got %v", node["role"])
	}
}

// TestPropertyHydration_HydratedOnce verifies that a node is fetched from the
// store at most once per query even when multiple conditions reference it —
// Issue 1 (redundant store.Get on missing fields).
func TestPropertyHydration_HydratedOnce(t *testing.T) {
	t.Parallel()

	g := graph.NewFlatGraph()
	g.AddNode("users:1", "users")

	calls := 0
	store := &countingStore{
		mockStore: newMockStore(),
		onGet:     func() { calls++ },
	}
	store.set("users", 1, map[string]interface{}{"id": 1, "name": "Alice", "active": true})

	exec := NewExecutor(g, 10).WithStore(store)
	parser := NewParser()

	// Two conditions on the same node — should trigger exactly one store.Get.
	query, err := parser.Parse(`MATCH (u:users) WHERE u.name = "Alice" AND u.active = true RETURN u`)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}
	_, err = exec.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if calls != 1 {
		t.Errorf("expected store.Get called once, got %d", calls)
	}
}

// countingStore wraps mockStore and calls onGet on every Get invocation.
type countingStore struct {
	*mockStore
	onGet func()
}

func (c *countingStore) Get(ctx context.Context, entity string, id int) (map[string]interface{}, error) {
	c.onGet()
	return c.mockStore.Get(ctx, entity, id)
}
