// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

// ---------------------------------------------------------------------------
// Setup helpers
// ---------------------------------------------------------------------------

// fieldQueryEnv holds a SQLiteStore with pre-loaded blob entities for testing
// the FieldQueryable interface.
type fieldQueryEnv struct {
	store   storage.Store
	ctx     context.Context
	cleanup func()
}

func newFieldQueryEnv(t *testing.T) *fieldQueryEnv {
	t.Helper()

	tmpFile, err := os.CreateTemp("", "olu-fieldquery-*.db")
	if err != nil {
		t.Fatalf("create temp file: %v", err)
	}
	tmpFile.Close()
	dbPath := tmpFile.Name()

	config := map[string]interface{}{
		"db_path": dbPath,
	}
	store, err := storage.NewStore("sqlite", config)
	if err != nil {
		os.Remove(dbPath)
		t.Fatalf("create store: %v", err)
	}

	cleanup := func() {
		store.Close()
		os.Remove(dbPath)
	}

	ctx := context.Background()

	// Insert test data with varied types.
	testRecords := []map[string]interface{}{
		{
			"name":     "Alice",
			"email":    "alice@example.com",
			"age":      float64(30),
			"active":   true,
			"score":    float64(95.5),
			"nickname": nil,
			"address":  map[string]interface{}{"city": "London", "zip": "SW1A"},
			"tags":     []interface{}{"admin", "user"},
		},
		{
			"name":     "Bob",
			"email":    "bob@example.com",
			"age":      float64(25),
			"active":   false,
			"score":    float64(82.3),
			"nickname": "bobby",
			"address":  map[string]interface{}{"city": "Paris", "zip": "75001"},
			"tags":     []interface{}{"user"},
		},
		{
			"name":                    "Charlie",
			"email":                   "charlie@example.com",
			"age":                     float64(40),
			"active":                  true,
			"score":                   float64(71.0),
			"nickname":                nil,
			"long_field_name_for_hash": "hashed_atom_test",
		},
	}

	for _, rec := range testRecords {
		if _, err := store.Create(ctx, "users", rec); err != nil {
			cleanup()
			t.Fatalf("insert test record: %v", err)
		}
	}

	return &fieldQueryEnv{store: store, ctx: ctx, cleanup: cleanup}
}

// fq returns the FieldQueryable interface or skips the test.
func (env *fieldQueryEnv) fq(t *testing.T) storage.FieldQueryable {
	t.Helper()
	fq, ok := env.store.(storage.FieldQueryable)
	if !ok {
		t.Skip("store does not implement FieldQueryable")
	}
	return fq
}

// ---------------------------------------------------------------------------
// ListWithFields tests
// ---------------------------------------------------------------------------

func TestListWithFields_BasicSelection(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	results, err := fq.ListWithFields(env.ctx, "users", []string{"name", "email"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	if len(results) != 3 {
		t.Fatalf("expected 3 rows, got %d", len(results))
	}

	// Each row should have name, email, and _version — nothing else.
	for i, row := range results {
		if _, ok := row["name"]; !ok {
			t.Errorf("row %d: missing 'name'", i)
		}
		if _, ok := row["email"]; !ok {
			t.Errorf("row %d: missing 'email'", i)
		}
		if _, ok := row["_version"]; !ok {
			t.Errorf("row %d: missing '_version'", i)
		}
		// Should NOT have other fields.
		for key := range row {
			switch key {
			case "name", "email", "_version":
				// expected
			default:
				t.Errorf("row %d: unexpected key %q", i, key)
			}
		}
	}

	// Verify values.
	if results[0]["name"] != "Alice" {
		t.Errorf("row 0 name: got %v, want Alice", results[0]["name"])
	}
	if results[1]["email"] != "bob@example.com" {
		t.Errorf("row 1 email: got %v, want bob@example.com", results[1]["email"])
	}
}

func TestListWithFields_TypePreservation(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	results, err := fq.ListWithFields(env.ctx, "users", []string{"name", "age", "active", "score"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	row := results[0] // Alice

	// String
	if name, ok := row["name"].(string); !ok || name != "Alice" {
		t.Errorf("name: got %v (%T), want string Alice", row["name"], row["name"])
	}

	// Number (json.Unmarshal produces float64)
	if age, ok := row["age"].(float64); !ok || age != 30.0 {
		t.Errorf("age: got %v (%T), want float64 30", row["age"], row["age"])
	}

	// Boolean
	if active, ok := row["active"].(bool); !ok || active != true {
		t.Errorf("active: got %v (%T), want bool true", row["active"], row["active"])
	}

	// Float
	if score, ok := row["score"].(float64); !ok || math.Abs(score-95.5) > 0.001 {
		t.Errorf("score: got %v (%T), want float64 95.5", row["score"], row["score"])
	}

	// Bob: active=false
	row1 := results[1]
	if active, ok := row1["active"].(bool); !ok || active != false {
		t.Errorf("Bob active: got %v (%T), want bool false", row1["active"], row1["active"])
	}
}

func TestListWithFields_NestedObject(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	results, err := fq.ListWithFields(env.ctx, "users", []string{"name", "address"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	// Alice's address should be a nested map.
	addr, ok := results[0]["address"]
	if !ok {
		t.Fatal("row 0: missing 'address'")
	}
	addrMap, ok := addr.(map[string]interface{})
	if !ok {
		t.Fatalf("address: got %T, want map[string]interface{}", addr)
	}
	if addrMap["city"] != "London" {
		t.Errorf("address.city: got %v, want London", addrMap["city"])
	}
}

func TestListWithFields_Array(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	results, err := fq.ListWithFields(env.ctx, "users", []string{"name", "tags"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	// Alice's tags should be ["admin", "user"].
	tags, ok := results[0]["tags"]
	if !ok {
		t.Fatal("row 0: missing 'tags'")
	}
	tagSlice, ok := tags.([]interface{})
	if !ok {
		t.Fatalf("tags: got %T, want []interface{}", tags)
	}
	if len(tagSlice) != 2 {
		t.Errorf("tags length: got %d, want 2", len(tagSlice))
	}
}

func TestListWithFields_MissingField(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	// "address" exists on Alice/Bob but not Charlie (who has long_field_name_for_hash instead).
	results, err := fq.ListWithFields(env.ctx, "users", []string{"name", "address"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	// Charlie (row 2) should have name but not address.
	charlie := results[2]
	if charlie["name"] != "Charlie" {
		t.Errorf("Charlie name: got %v", charlie["name"])
	}
	if _, ok := charlie["address"]; ok {
		t.Error("Charlie should not have 'address' field")
	}
}

func TestListWithFields_LongFieldName(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	// Field name > 8 bytes triggers hashed atom path.
	results, err := fq.ListWithFields(env.ctx, "users", []string{"long_field_name_for_hash"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	// Only Charlie has this field.
	charlie := results[2]
	if charlie["long_field_name_for_hash"] != "hashed_atom_test" {
		t.Errorf("long field: got %v, want hashed_atom_test", charlie["long_field_name_for_hash"])
	}

	// Alice/Bob should not have it.
	if _, ok := results[0]["long_field_name_for_hash"]; ok {
		t.Error("Alice should not have long_field_name_for_hash")
	}
}

func TestListWithFields_EmptyFieldsIsList(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	withFields, err := fq.ListWithFields(env.ctx, "users", nil)
	if err != nil {
		t.Fatalf("ListWithFields(nil): %v", err)
	}

	list, err := env.store.List(env.ctx, "users")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(withFields) != len(list) {
		t.Fatalf("row count: ListWithFields=%d List=%d", len(withFields), len(list))
	}

	// Both should have all fields (empty = no restriction).
	for i, row := range withFields {
		for key := range list[i] {
			if _, ok := row[key]; !ok {
				t.Errorf("row %d: ListWithFields missing key %q that List has", i, key)
			}
		}
	}
}

func TestListWithFields_EmptyEntity(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	results, err := fq.ListWithFields(env.ctx, "nonexistent", []string{"name"})
	if err != nil {
		t.Fatalf("ListWithFields on empty entity: %v", err)
	}

	if len(results) != 0 {
		t.Errorf("expected 0 rows for nonexistent entity, got %d", len(results))
	}
}

// ---------------------------------------------------------------------------
// Comparative: ListWithFields vs List (correctness oracle)
// ---------------------------------------------------------------------------

func TestListWithFields_MatchesList(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	fields := []string{"name", "email", "age", "active", "score"}

	selective, err := fq.ListWithFields(env.ctx, "users", fields)
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	full, err := env.store.List(env.ctx, "users")
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if len(selective) != len(full) {
		t.Fatalf("row count: selective=%d full=%d", len(selective), len(full))
	}

	for i := range selective {
		for _, f := range fields {
			sVal := selective[i][f]
			fVal := full[i][f]

			// Compare as JSON to handle type mismatches gracefully.
			sJSON, _ := json.Marshal(sVal)
			fJSON, _ := json.Marshal(fVal)
			if string(sJSON) != string(fJSON) {
				t.Errorf("row %d field %q: selective=%s full=%s", i, f, sJSON, fJSON)
			}
		}
		// Verify _version matches.
		if fmt.Sprintf("%v", selective[i]["_version"]) != fmt.Sprintf("%v", full[i]["_version"]) {
			t.Errorf("row %d _version: selective=%v full=%v",
				i, selective[i]["_version"], full[i]["_version"])
		}
	}
}

// ---------------------------------------------------------------------------
// QueryWithFields tests
// ---------------------------------------------------------------------------

func TestQueryWithFields_BasicPushDown(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	sql := `SELECT data, _version FROM entities WHERE tenant_id = 0 AND entity_type = 'users' AND json_extract(data, '$.active') = 1`

	results, err := fq.QueryWithFields(env.ctx, sql, nil, []string{"name", "email"})
	if err != nil {
		t.Fatalf("QueryWithFields: %v", err)
	}

	// Active users: Alice and Charlie.
	if len(results) != 2 {
		t.Fatalf("expected 2 active users, got %d", len(results))
	}

	names := map[string]bool{}
	for _, row := range results {
		name, _ := row["name"].(string)
		names[name] = true
		// Should only have name, email, _version.
		for key := range row {
			switch key {
			case "name", "email", "_version":
			default:
				t.Errorf("unexpected key %q in row", key)
			}
		}
	}
	if !names["Alice"] || !names["Charlie"] {
		t.Errorf("expected Alice and Charlie, got %v", names)
	}
}

func TestQueryWithFields_MatchesQueryWithPlan(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	queryable, ok := env.store.(storage.Queryable)
	if !ok {
		t.Skip("store does not implement Queryable")
	}

	sql := `SELECT data, _version FROM entities WHERE tenant_id = 0 AND entity_type = 'users' AND json_extract(data, '$.age') > 25`
	fields := []string{"name", "age", "score"}

	selective, err := fq.QueryWithFields(env.ctx, sql, nil, fields)
	if err != nil {
		t.Fatalf("QueryWithFields: %v", err)
	}

	full, err := queryable.QueryWithPlan(env.ctx, sql, nil)
	if err != nil {
		t.Fatalf("QueryWithPlan: %v", err)
	}

	if len(selective) != len(full) {
		t.Fatalf("row count: selective=%d full=%d", len(selective), len(full))
	}

	for i := range selective {
		for _, f := range fields {
			sJSON, _ := json.Marshal(selective[i][f])
			fJSON, _ := json.Marshal(full[i][f])
			if string(sJSON) != string(fJSON) {
				t.Errorf("row %d field %q: selective=%s full=%s", i, f, sJSON, fJSON)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// Null handling
// ---------------------------------------------------------------------------

func TestListWithFields_NullValue(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	// "nickname" is nil for Alice and Charlie, "bobby" for Bob.
	results, err := fq.ListWithFields(env.ctx, "users", []string{"name", "nickname"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	// Bob has a nickname.
	bob := results[1]
	if bob["nickname"] != "bobby" {
		t.Errorf("Bob nickname: got %v, want bobby", bob["nickname"])
	}

	// Alice: nickname is JSON null. tokenToValue returns nil for TokNull,
	// and the extraction code skips nil values (val != nil check). So the
	// key should be absent, consistent with "field not present" semantics.
	// This is a deliberate design choice: jsonic treats null the same as
	// absent to avoid ambiguity in the columnar path.
	//
	// If this test fails because nickname IS present with nil value, that's
	// also acceptable — document whichever behaviour we find.
	alice := results[0]
	aliceNick, hasNick := alice["nickname"]
	if hasNick && aliceNick != nil {
		t.Errorf("Alice nickname: got %v (%T), want absent or nil", aliceNick, aliceNick)
	}
}

// ---------------------------------------------------------------------------
// Version injection
// ---------------------------------------------------------------------------

func TestListWithFields_VersionAlwaysPresent(t *testing.T) {
	env := newFieldQueryEnv(t)
	defer env.cleanup()
	fq := env.fq(t)

	// Even when requesting only one field, _version must be present.
	results, err := fq.ListWithFields(env.ctx, "users", []string{"name"})
	if err != nil {
		t.Fatalf("ListWithFields: %v", err)
	}

	for i, row := range results {
		v, ok := row["_version"]
		if !ok {
			t.Errorf("row %d: _version missing", i)
			continue
		}
		// Should be an integer (1 for freshly created records).
		switch ver := v.(type) {
		case int:
			if ver < 1 {
				t.Errorf("row %d: _version=%d, want >= 1", i, ver)
			}
		case int64:
			if ver < 1 {
				t.Errorf("row %d: _version=%d, want >= 1", i, ver)
			}
		default:
			t.Errorf("row %d: _version is %T, want int", i, v)
		}
	}
}
