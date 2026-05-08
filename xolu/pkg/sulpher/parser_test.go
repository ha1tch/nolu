// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"testing"
)

func TestParserSimpleQuery(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	// Test: Simple node query
	query, err := parser.Parse("MATCH (u:User) RETURN u")
	if err != nil {
		t.Fatalf("Failed to parse simple query: %v", err)
	}

	if query.Algorithm != BFS {
		t.Errorf("Expected BFS algorithm, got %s", query.Algorithm)
	}

	if len(query.Path) != 1 {
		t.Fatalf("Expected 1 path element, got %d", len(query.Path))
	}

	if query.Path[0].Node.Variable != "u" {
		t.Errorf("Expected variable 'u', got '%s'", query.Path[0].Node.Variable)
	}

	if query.Path[0].Node.Type != "User" {
		t.Errorf("Expected type 'User', got '%s'", query.Path[0].Node.Type)
	}

	if len(query.ReturnItems) != 1 || query.ReturnItems[0].Variable != "u" {
		t.Errorf("Expected return item 'u'")
	}
}

func TestParserWithInlineProperties(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User {id: 123, active: true}) RETURN u")
	if err != nil {
		t.Fatalf("Failed to parse query with properties: %v", err)
	}

	props := query.Path[0].Node.Properties
	if props["id"] != 123 {
		t.Errorf("Expected id=123, got %v", props["id"])
	}
	if props["active"] != true {
		t.Errorf("Expected active=true, got %v", props["active"])
	}
}

func TestParserSingleHop(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[r:FOLLOWS]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse single hop query: %v", err)
	}

	if len(query.Path) != 2 {
		t.Fatalf("Expected 2 path elements, got %d", len(query.Path))
	}

	// First node
	if query.Path[0].Node.Variable != "u" || query.Path[0].Node.Type != "User" {
		t.Errorf("First node incorrect: %+v", query.Path[0].Node)
	}

	// Relationship
	if query.Path[0].Relationship == nil {
		t.Fatal("Expected relationship")
	}
	if query.Path[0].Relationship.Variable != "r" {
		t.Errorf("Expected relationship variable 'r', got '%s'", query.Path[0].Relationship.Variable)
	}
	if query.Path[0].Relationship.Type != "FOLLOWS" {
		t.Errorf("Expected relationship type 'FOLLOWS', got '%s'", query.Path[0].Relationship.Type)
	}

	// Second node
	if query.Path[1].Node.Variable != "f" || query.Path[1].Node.Type != "User" {
		t.Errorf("Second node incorrect: %+v", query.Path[1].Node)
	}
}

func TestParserMultiHop(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS]->(f:User)-[:LIKES]->(p:Post) RETURN p")
	if err != nil {
		t.Fatalf("Failed to parse multi-hop query: %v", err)
	}

	if len(query.Path) != 3 {
		t.Fatalf("Expected 3 path elements, got %d", len(query.Path))
	}

	if query.Path[0].Relationship.Type != "FOLLOWS" {
		t.Errorf("Expected first relationship type 'FOLLOWS'")
	}

	if query.Path[1].Relationship.Type != "LIKES" {
		t.Errorf("Expected second relationship type 'LIKES'")
	}

	if query.Path[2].Node.Type != "Post" {
		t.Errorf("Expected final node type 'Post'")
	}
}

func TestParserWithWhere(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS]->(f:User) WHERE u.id = 123 RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse query with WHERE: %v", err)
	}

	if len(query.Conditions) != 1 {
		t.Fatalf("Expected 1 condition, got %d", len(query.Conditions))
	}

	cond := query.Conditions[0]
	if cond.VarPath != "u.id" {
		t.Errorf("Expected condition VarPath 'u.id', got '%s'", cond.VarPath)
	}
	if cond.Operator != OpEq {
		t.Errorf("Expected operator '=', got '%s'", cond.Operator)
	}
	if cond.Value != 123 {
		t.Errorf("Expected value 123, got %v", cond.Value)
	}
}

func TestParserWithMultipleConditions(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) WHERE u.age >= 18 AND u.active = true RETURN u")
	if err != nil {
		t.Fatalf("Failed to parse query with multiple conditions: %v", err)
	}

	if len(query.Conditions) != 2 {
		t.Fatalf("Expected 2 conditions, got %d", len(query.Conditions))
	}

	if query.Conditions[0].Operator != OpGte {
		t.Errorf("Expected first operator '>='")
	}

	if query.Conditions[1].Value != true {
		t.Errorf("Expected second value true")
	}
}

func TestParserDFS(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("DFS MATCH (u:User)-[:FOLLOWS]->(f) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse DFS query: %v", err)
	}

	if query.Algorithm != DFS {
		t.Errorf("Expected DFS algorithm, got %s", query.Algorithm)
	}
}

func TestParserReturnProperties(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[r:MANAGES]->(e:Employee) RETURN u.name, e.email, r")
	if err != nil {
		t.Fatalf("Failed to parse query with property returns: %v", err)
	}

	if len(query.ReturnItems) != 3 {
		t.Fatalf("Expected 3 return items, got %d", len(query.ReturnItems))
	}

	// u.name
	if query.ReturnItems[0].Variable != "u" || query.ReturnItems[0].Property != "name" {
		t.Errorf("First return item incorrect: %+v", query.ReturnItems[0])
	}

	// e.email
	if query.ReturnItems[1].Variable != "e" || query.ReturnItems[1].Property != "email" {
		t.Errorf("Second return item incorrect: %+v", query.ReturnItems[1])
	}

	// r (no property)
	if query.ReturnItems[2].Variable != "r" || query.ReturnItems[2].Property != "" {
		t.Errorf("Third return item incorrect: %+v", query.ReturnItems[2])
	}
}

func TestParserInvalidQueries(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	invalidQueries := []string{
		"",
		"SELECT * FROM users",
		"MATCH (u) RETURN",
		"MATCH RETURN u",
		"(u:User) RETURN u",
	}

	for _, q := range invalidQueries {
		_, err := parser.Parse(q)
		if err == nil {
			t.Errorf("Expected error for invalid query: %s", q)
		}
	}
}

func TestParserCaseInsensitive(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	// Keywords should be case-insensitive
	queries := []string{
		"match (u:User) return u",
		"MATCH (u:User) RETURN u",
		"Match (u:User) Return u",
		"bfs MATCH (u:User) RETURN u",
		"BFS match (u:User) return u",
	}

	for _, q := range queries {
		_, err := parser.Parse(q)
		if err != nil {
			t.Errorf("Failed to parse case variant: %s - %v", q, err)
		}
	}
}

// Variable-length path tests

func TestParserVariableLengthMinMax(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS*1..5]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse variable-length query: %v", err)
	}

	rel := query.Path[0].Relationship
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
	if rel.MinHops != 1 {
		t.Errorf("Expected MinHops=1, got %d", rel.MinHops)
	}
	if rel.MaxHops != 5 {
		t.Errorf("Expected MaxHops=5, got %d", rel.MaxHops)
	}
	if rel.Type != "FOLLOWS" {
		t.Errorf("Expected type FOLLOWS, got %s", rel.Type)
	}
}

func TestParserVariableLengthMaxOnly(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS*..3]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse *..3 query: %v", err)
	}

	rel := query.Path[0].Relationship
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
	if rel.MinHops != 1 {
		t.Errorf("Expected MinHops=1 (default), got %d", rel.MinHops)
	}
	if rel.MaxHops != 3 {
		t.Errorf("Expected MaxHops=3, got %d", rel.MaxHops)
	}
}

func TestParserVariableLengthMinOnly(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS*2..]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse *2.. query: %v", err)
	}

	rel := query.Path[0].Relationship
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
	if rel.MinHops != 2 {
		t.Errorf("Expected MinHops=2, got %d", rel.MinHops)
	}
	if rel.MaxHops != 0 {
		t.Errorf("Expected MaxHops=0 (unlimited), got %d", rel.MaxHops)
	}
}

func TestParserVariableLengthUnlimited(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS*]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse * query: %v", err)
	}

	rel := query.Path[0].Relationship
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
	if rel.MinHops != 1 {
		t.Errorf("Expected MinHops=1, got %d", rel.MinHops)
	}
	if rel.MaxHops != 0 {
		t.Errorf("Expected MaxHops=0 (unlimited), got %d", rel.MaxHops)
	}
}

func TestParserVariableLengthExact(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS*3]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse *3 query: %v", err)
	}

	rel := query.Path[0].Relationship
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
	if rel.MinHops != 3 {
		t.Errorf("Expected MinHops=3, got %d", rel.MinHops)
	}
	if rel.MaxHops != 3 {
		t.Errorf("Expected MaxHops=3, got %d", rel.MaxHops)
	}
}

func TestParserVariableLengthWithVariable(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[r:FOLLOWS*1..5]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse query with rel variable: %v", err)
	}

	rel := query.Path[0].Relationship
	if rel.Variable != "r" {
		t.Errorf("Expected variable 'r', got '%s'", rel.Variable)
	}
	if rel.Type != "FOLLOWS" {
		t.Errorf("Expected type 'FOLLOWS', got '%s'", rel.Type)
	}
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
}

func TestParserVariableLengthNoType(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[*1..3]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse query without rel type: %v", err)
	}

	rel := query.Path[0].Relationship
	if rel.Type != "" {
		t.Errorf("Expected empty type, got '%s'", rel.Type)
	}
	if !rel.IsVariable {
		t.Error("Expected IsVariable to be true")
	}
	if rel.MinHops != 1 || rel.MaxHops != 3 {
		t.Errorf("Expected hops 1..3, got %d..%d", rel.MinHops, rel.MaxHops)
	}
}

func TestParserVariableLengthInvalid(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	invalidQueries := []string{
		"MATCH (u:User)-[:FOLLOWS*5..2]->(f:User) RETURN f", // max < min
		"MATCH (u:User)-[:FOLLOWS*-1..5]->(f:User) RETURN f", // negative min
		"MATCH (u:User)-[:FOLLOWS*abc]->(f:User) RETURN f",   // non-numeric
	}

	for _, q := range invalidQueries {
		_, err := parser.Parse(q)
		if err == nil {
			t.Errorf("Expected error for invalid query: %s", q)
		}
	}
}

// Phase 4 tests: DISTINCT, LIMIT, ORDER BY, OR, bidirectional

func TestParserDistinct(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[:FOLLOWS]->(f:User) RETURN DISTINCT f")
	if err != nil {
		t.Fatalf("Failed to parse DISTINCT query: %v", err)
	}

	if !query.Distinct {
		t.Error("Expected Distinct to be true")
	}
}

func TestParserLimit(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) RETURN u LIMIT 10")
	if err != nil {
		t.Fatalf("Failed to parse LIMIT query: %v", err)
	}

	if query.Limit != 10 {
		t.Errorf("Expected Limit=10, got %d", query.Limit)
	}
}

func TestParserOrderBy(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) RETURN u ORDER BY u.name")
	if err != nil {
		t.Fatalf("Failed to parse ORDER BY query: %v", err)
	}

	if len(query.OrderBy) != 1 {
		t.Fatalf("Expected 1 ORDER BY item, got %d", len(query.OrderBy))
	}

	if query.OrderBy[0].VarPath != "u.name" {
		t.Errorf("Expected ORDER BY u.name, got %s", query.OrderBy[0].VarPath)
	}

	if query.OrderBy[0].Direction != OrderAsc {
		t.Errorf("Expected ASC direction by default")
	}
}

func TestParserOrderByDesc(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) RETURN u ORDER BY u.age DESC")
	if err != nil {
		t.Fatalf("Failed to parse ORDER BY DESC query: %v", err)
	}

	if query.OrderBy[0].Direction != OrderDesc {
		t.Errorf("Expected DESC direction")
	}
}

func TestParserOrderByMultiple(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) RETURN u ORDER BY u.name ASC, u.age DESC")
	if err != nil {
		t.Fatalf("Failed to parse multiple ORDER BY: %v", err)
	}

	if len(query.OrderBy) != 2 {
		t.Fatalf("Expected 2 ORDER BY items, got %d", len(query.OrderBy))
	}

	if query.OrderBy[0].Direction != OrderAsc {
		t.Errorf("Expected first item ASC")
	}

	if query.OrderBy[1].Direction != OrderDesc {
		t.Errorf("Expected second item DESC")
	}
}

func TestParserCombinedClauses(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) RETURN DISTINCT u ORDER BY u.name LIMIT 5")
	if err != nil {
		t.Fatalf("Failed to parse combined clauses: %v", err)
	}

	if !query.Distinct {
		t.Error("Expected Distinct")
	}
	if len(query.OrderBy) != 1 {
		t.Error("Expected ORDER BY")
	}
	if query.Limit != 5 {
		t.Error("Expected LIMIT 5")
	}
}

func TestParserWhereOr(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User) WHERE u.name = 'Alice' OR u.name = 'Bob' RETURN u")
	if err != nil {
		t.Fatalf("Failed to parse WHERE with OR: %v", err)
	}

	if len(query.ConditionGroups) != 2 {
		t.Fatalf("Expected 2 condition groups (OR), got %d", len(query.ConditionGroups))
	}
}

func TestParserWhereAndOr(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	// (a AND b) OR (c AND d)
	query, err := parser.Parse("MATCH (u:User) WHERE u.age > 18 AND u.active = true OR u.role = 'admin' AND u.verified = true RETURN u")
	if err != nil {
		t.Fatalf("Failed to parse WHERE with AND/OR: %v", err)
	}

	if len(query.ConditionGroups) != 2 {
		t.Fatalf("Expected 2 condition groups, got %d", len(query.ConditionGroups))
	}

	if len(query.ConditionGroups[0].Conditions) != 2 {
		t.Errorf("Expected 2 conditions in first group, got %d", len(query.ConditionGroups[0].Conditions))
	}

	if len(query.ConditionGroups[1].Conditions) != 2 {
		t.Errorf("Expected 2 conditions in second group, got %d", len(query.ConditionGroups[1].Conditions))
	}
}

func TestParserBidirectionalUndirected(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)-[r:KNOWS]-(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse bidirectional query: %v", err)
	}

	if query.Path[0].Relationship.Direction != RelBidirectional {
		t.Errorf("Expected bidirectional direction, got %v", query.Path[0].Relationship.Direction)
	}
}

func TestParserIncoming(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)<-[r:FOLLOWS]-(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse incoming query: %v", err)
	}

	if query.Path[0].Relationship.Direction != RelIncoming {
		t.Errorf("Expected incoming direction, got %v", query.Path[0].Relationship.Direction)
	}
}

func TestParserBidirectionalBothArrows(t *testing.T) {
	t.Parallel()
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:User)<-[r:KNOWS]->(f:User) RETURN f")
	if err != nil {
		t.Fatalf("Failed to parse <-[]-> query: %v", err)
	}

	if query.Path[0].Relationship.Direction != RelBidirectional {
		t.Errorf("Expected bidirectional direction, got %v", query.Path[0].Relationship.Direction)
	}
}
