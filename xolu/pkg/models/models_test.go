// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package models

import (
	"encoding/json"
	"errors"
	"testing"
)

// =============================================================================
// Entity JSON round-trip tests
// =============================================================================

func TestEntity_UnmarshalJSON_ValidEntity(t *testing.T) {
	t.Parallel()
	input := `{"id": 42, "type": "sensor", "name": "Flow Meter", "status": "active"}`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if e.ID != 42 {
		t.Errorf("ID = %d, want 42", e.ID)
	}
	if e.Type != "sensor" {
		t.Errorf("Type = %q, want %q", e.Type, "sensor")
	}
	if e.Data["name"] != "Flow Meter" {
		t.Errorf("Data[name] = %v, want %q", e.Data["name"], "Flow Meter")
	}
	if e.Data["status"] != "active" {
		t.Errorf("Data[status] = %v, want %q", e.Data["status"], "active")
	}
}

func TestEntity_UnmarshalJSON_MissingID(t *testing.T) {
	t.Parallel()
	input := `{"type": "sensor", "name": "Unnamed"}`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if e.ID != 0 {
		t.Errorf("ID = %d, want 0 (zero value when missing)", e.ID)
	}
	if e.Type != "sensor" {
		t.Errorf("Type = %q, want %q", e.Type, "sensor")
	}
}

func TestEntity_UnmarshalJSON_MissingType(t *testing.T) {
	t.Parallel()
	input := `{"id": 7, "name": "Widget"}`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if e.ID != 7 {
		t.Errorf("ID = %d, want 7", e.ID)
	}
	if e.Type != "" {
		t.Errorf("Type = %q, want empty string when missing", e.Type)
	}
}

func TestEntity_UnmarshalJSON_EmptyObject(t *testing.T) {
	t.Parallel()
	input := `{}`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if e.ID != 0 {
		t.Errorf("ID = %d, want 0", e.ID)
	}
	if e.Type != "" {
		t.Errorf("Type = %q, want empty", e.Type)
	}
	if e.Data == nil {
		t.Error("Data should be non-nil empty map, got nil")
	}
}

func TestEntity_UnmarshalJSON_IDAsString(t *testing.T) {
	t.Parallel()
	// JSON id as string — should NOT set ID since it expects float64
	input := `{"id": "not-a-number", "type": "widget"}`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	if e.ID != 0 {
		t.Errorf("ID = %d, want 0 (string id should not convert)", e.ID)
	}
}

func TestEntity_UnmarshalJSON_InvalidJSON(t *testing.T) {
	t.Parallel()
	input := `not json at all`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err == nil {
		t.Error("Expected error for invalid JSON, got nil")
	}
}

func TestEntity_UnmarshalJSON_NestedData(t *testing.T) {
	t.Parallel()
	input := `{"id": 1, "metadata": {"location": {"lat": 34.05, "lng": -118.25}}}`
	var e Entity
	if err := json.Unmarshal([]byte(input), &e); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}
	meta, ok := e.Data["metadata"].(map[string]interface{})
	if !ok {
		t.Fatal("metadata should be a nested map")
	}
	loc, ok := meta["location"].(map[string]interface{})
	if !ok {
		t.Fatal("location should be a nested map")
	}
	if loc["lat"] != 34.05 {
		t.Errorf("lat = %v, want 34.05", loc["lat"])
	}
}

func TestEntity_MarshalJSON_NilData(t *testing.T) {
	t.Parallel()
	e := Entity{ID: 1, Type: "test", Data: nil}
	data, err := json.Marshal(&e)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}
	// Marshaling nil map produces "null"
	if string(data) != "null" {
		t.Errorf("Marshal nil Data = %s, want null", string(data))
	}
}

func TestEntity_MarshalJSON_RoundTrip(t *testing.T) {
	t.Parallel()
	original := Entity{
		ID:   10,
		Type: "asset",
		Data: map[string]interface{}{
			"id":     float64(10), // JSON numbers are float64
			"type":   "asset",
			"name":   "Pump Station",
			"active": true,
		},
	}

	data, err := json.Marshal(&original)
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var decoded Entity
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal failed: %v", err)
	}

	if decoded.ID != original.ID {
		t.Errorf("Round-trip ID = %d, want %d", decoded.ID, original.ID)
	}
	if decoded.Data["name"] != "Pump Station" {
		t.Errorf("Round-trip name = %v, want %q", decoded.Data["name"], "Pump Station")
	}
}

// =============================================================================
// IsReference tests
// =============================================================================

func TestIsReference_ValidRef_Float64ID(t *testing.T) {
	t.Parallel()
	// JSON-decoded ids come through as float64
	v := map[string]interface{}{
		"type":   "REF",
		"entity": "users",
		"id":     float64(42),
	}
	ref, ok := IsReference(v)
	if !ok {
		t.Fatal("Expected IsReference to return true")
	}
	if ref.Entity != "users" {
		t.Errorf("Entity = %q, want %q", ref.Entity, "users")
	}
	if ref.ID != 42 {
		t.Errorf("ID = %d, want 42", ref.ID)
	}
}

func TestIsReference_ValidRef_IntID(t *testing.T) {
	t.Parallel()
	// Programmatically-constructed refs may use int
	v := map[string]interface{}{
		"type":   "REF",
		"entity": "assets",
		"id":     7,
	}
	ref, ok := IsReference(v)
	if !ok {
		t.Fatal("Expected IsReference to return true")
	}
	if ref.ID != 7 {
		t.Errorf("ID = %d, want 7", ref.ID)
	}
}

func TestIsReference_NotAMap(t *testing.T) {
	t.Parallel()
	_, ok := IsReference("not a map")
	if ok {
		t.Error("String should not be a reference")
	}
	_, ok = IsReference(42)
	if ok {
		t.Error("Int should not be a reference")
	}
	_, ok = IsReference(nil)
	if ok {
		t.Error("Nil should not be a reference")
	}
}

func TestIsReference_WrongType(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{
		"type":   "LINK", // not "REF"
		"entity": "users",
		"id":     float64(1),
	}
	_, ok := IsReference(v)
	if ok {
		t.Error("type=LINK should not be a reference")
	}
}

func TestIsReference_MissingEntity(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{
		"type": "REF",
		"id":   float64(1),
	}
	_, ok := IsReference(v)
	if ok {
		t.Error("Missing entity should not be a reference")
	}
}

func TestIsReference_MissingID(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{
		"type":   "REF",
		"entity": "users",
	}
	_, ok := IsReference(v)
	if ok {
		t.Error("Missing id should not be a reference")
	}
}

func TestIsReference_StringID(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{
		"type":   "REF",
		"entity": "users",
		"id":     "not-numeric",
	}
	_, ok := IsReference(v)
	if ok {
		t.Error("String id should not be a valid reference")
	}
}

func TestIsReference_RegularMap(t *testing.T) {
	t.Parallel()
	// A map that happens to have some overlapping keys but isn't a REF
	v := map[string]interface{}{
		"type":   "sensor",
		"entity": "temperature",
		"id":     float64(5),
		"name":   "Thermocouple",
	}
	_, ok := IsReference(v)
	if ok {
		t.Error("Map with type != REF should not be a reference")
	}
}

func TestIsReference_EmptyMap(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{}
	_, ok := IsReference(v)
	if ok {
		t.Error("Empty map should not be a reference")
	}
}

func TestIsReference_ZeroID(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{
		"type":   "REF",
		"entity": "users",
		"id":     float64(0),
	}
	ref, ok := IsReference(v)
	if !ok {
		t.Fatal("REF with id=0 should be valid")
	}
	if ref.ID != 0 {
		t.Errorf("ID = %d, want 0", ref.ID)
	}
}

func TestIsReference_NegativeID(t *testing.T) {
	t.Parallel()
	v := map[string]interface{}{
		"type":   "REF",
		"entity": "users",
		"id":     float64(-1),
	}
	ref, ok := IsReference(v)
	if !ok {
		t.Fatal("REF with negative id should still parse")
	}
	if ref.ID != -1 {
		t.Errorf("ID = %d, want -1", ref.ID)
	}
}

// =============================================================================
// ExtractEntityEdges — parity and correctness tests
//
// These tests are the structural guarantee that the two REF extraction
// pipelines (syncGraphEdges in the storage layer and updateGraph in the
// server layer) produce identical edges for the same entity data.
// Both pipelines call ExtractEntityEdges, so these tests cover both.
// =============================================================================

func TestExtractEntityEdges_SingleREF(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		"owner": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     float64(42),
		},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge, got %d", len(edges))
	}
	e := edges[0]
	if e.TargetEntity != "users" {
		t.Errorf("TargetEntity = %q, want %q", e.TargetEntity, "users")
	}
	if e.TargetID != 42 {
		t.Errorf("TargetID = %d, want 42", e.TargetID)
	}
	if e.Relationship != "owner" {
		t.Errorf("Relationship = %q, want %q", e.Relationship, "owner")
	}
}

func TestExtractEntityEdges_AtREFS(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		"tags": []interface{}{
			map[string]interface{}{"type": "REF", "entity": "tags", "id": float64(10)},
			map[string]interface{}{"type": "REF", "entity": "tags", "id": float64(11)},
		},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	for _, e := range edges {
		if e.TargetEntity != "tags" {
			t.Errorf("TargetEntity = %q, want %q", e.TargetEntity, "tags")
		}
		if e.Relationship != "tags" {
			t.Errorf("Relationship = %q, want %q", e.Relationship, "tags")
		}
	}
}

func TestExtractEntityEdges_SkipsIDField(t *testing.T) {
	t.Parallel()
	// The "id" key must never produce an edge even if it coincidentally
	// looks like a REF (it won't in practice, but we guard against it).
	data := map[string]interface{}{
		"id":    float64(1),
		"owner": map[string]interface{}{"type": "REF", "entity": "users", "id": float64(5)},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (not the id field), got %d", len(edges))
	}
}

func TestExtractEntityEdges_SkipsTSREF(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		"ts": map[string]interface{}{
			"type":     "TSREF",
			"timeline": "temperature",
		},
		"owner": map[string]interface{}{"type": "REF", "entity": "users", "id": float64(7)},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (TSREF excluded), got %d", len(edges))
	}
	if edges[0].TargetEntity != "users" {
		t.Errorf("TargetEntity = %q, want users", edges[0].TargetEntity)
	}
}

// TestExtractEntityEdges_ZeroIDAllowed verifies that entity ID 0 is accepted.
// olu auto-increment starts at 1 by convention but nothing prevents explicit
// use of ID 0 (the graph package pre-creates nodes with ID 0 in some tests).
func TestExtractEntityEdges_ZeroIDAllowed(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		"owner": map[string]interface{}{"type": "REF", "entity": "users", "id": float64(0)},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 1 {
		t.Fatalf("expected 1 edge (id=0 is valid), got %d", len(edges))
	}
	if edges[0].TargetID != 0 {
		t.Errorf("TargetID = %d, want 0", edges[0].TargetID)
	}
}

// TestExtractEntityEdges_SkipsEmptyEntity verifies that a REF with an empty
// entity string is rejected. An empty entity would produce the malformed
// node ID ":N" which is not a valid node ID in the graph.
func TestExtractEntityEdges_SkipsEmptyEntity(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		"bad": map[string]interface{}{"type": "REF", "entity": "", "id": float64(5)},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges (empty entity rejected), got %d", len(edges))
	}
}

func TestExtractEntityEdges_EmptyData(t *testing.T) {
	t.Parallel()
	edges, err := ExtractEntityEdges(map[string]interface{}{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 0 {
		t.Fatalf("expected 0 edges for empty data, got %d", len(edges))
	}
}

func TestExtractEntityEdges_MultipleFields(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id":   float64(1),
		"name": "sensor-A",
		"gateway": map[string]interface{}{"type": "REF", "entity": "gateways", "id": float64(3)},
		"site":    map[string]interface{}{"type": "REF", "entity": "sites", "id": float64(9)},
	}
	edges, err := ExtractEntityEdges(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(edges) != 2 {
		t.Fatalf("expected 2 edges, got %d", len(edges))
	}
	seen := map[string]bool{}
	for _, e := range edges {
		seen[e.Relationship] = true
	}
	if !seen["gateway"] || !seen["site"] {
		t.Errorf("expected edges for 'gateway' and 'site', got %v", seen)
	}
}

// TestExtractEntityEdges_DuplicateSameField verifies that the same (entity, id)
// target appearing twice within a single @REFS array field is also rejected.
func TestExtractEntityEdges_DuplicateSameField(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		// Same target twice in one @REFS field.
		"tags": []interface{}{
			map[string]interface{}{"type": "REF", "entity": "tags", "id": float64(10)},
			map[string]interface{}{"type": "REF", "entity": "tags", "id": float64(10)},
		},
	}
	_, err := ExtractEntityEdges(data)
	if err == nil {
		t.Fatal("expected ErrDuplicateEdgeTarget for same-field duplicate, got nil")
	}
	if !errors.Is(err, ErrDuplicateEdgeTarget) {
		t.Errorf("expected ErrDuplicateEdgeTarget, got %v", err)
	}
}

// TestExtractEntityEdges_DuplicateTarget verifies that two fields referencing
// the same (entity, id) pair are rejected with ErrDuplicateEdgeTarget.
// In olu's graph model each ordered node pair carries at most one labelled edge.
func TestExtractEntityEdges_DuplicateTarget(t *testing.T) {
	t.Parallel()
	data := map[string]interface{}{
		"id": float64(1),
		// Both fields reference users:42 — one of these must be an error.
		"owner":   map[string]interface{}{"type": "REF", "entity": "users", "id": float64(42)},
		"manager": map[string]interface{}{"type": "REF", "entity": "users", "id": float64(42)},
	}
	_, err := ExtractEntityEdges(data)
	if err == nil {
		t.Fatal("expected ErrDuplicateEdgeTarget, got nil")
	}
	if !errors.Is(err, ErrDuplicateEdgeTarget) {
		t.Errorf("expected ErrDuplicateEdgeTarget, got %v", err)
	}
}
