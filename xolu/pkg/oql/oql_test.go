// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"testing"

	"github.com/ha1tch/tsqlparser"
)

func TestParseSelect(t *testing.T) {
	queries := []string{
		"SELECT * FROM items",
		"SELECT id, name FROM items",
		"SELECT id, name FROM items WHERE status = 'active'",
		"SELECT category_id, COUNT(*) FROM items GROUP BY category_id",
		"SELECT category_id, COUNT(*) AS cnt FROM items GROUP BY category_id HAVING COUNT(*) > 5",
		"SELECT id, name FROM items ORDER BY name",
		"SELECT id, name FROM items ORDER BY name DESC",
		"SELECT TOP 10 * FROM items",
		"SELECT DISTINCT status FROM items",
	}

	for _, q := range queries {
		program, errs := tsqlparser.Parse(q)
		if len(errs) > 0 {
			t.Errorf("Failed to parse '%s': %v", q, errs[0])
			continue
		}
		if len(program.Statements) != 1 {
			t.Errorf("Expected 1 statement for '%s', got %d", q, len(program.Statements))
		}
	}
}

func TestParseInsert(t *testing.T) {
	queries := []string{
		"INSERT INTO items (category_id, status) VALUES (1, 'active')",
		"INSERT INTO items (category_id, status) VALUES (1, 'active'), (2, 'inactive')",
	}

	for _, q := range queries {
		program, errs := tsqlparser.Parse(q)
		if len(errs) > 0 {
			t.Errorf("Failed to parse '%s': %v", q, errs[0])
			continue
		}
		if len(program.Statements) != 1 {
			t.Errorf("Expected 1 statement for '%s', got %d", q, len(program.Statements))
		}
	}
}

func TestParseUpdate(t *testing.T) {
	queries := []string{
		"UPDATE items SET status = 'inactive' WHERE category_id = 5",
		"UPDATE items SET status = 'inactive', value = 0 WHERE id = 1",
	}

	for _, q := range queries {
		program, errs := tsqlparser.Parse(q)
		if len(errs) > 0 {
			t.Errorf("Failed to parse '%s': %v", q, errs[0])
			continue
		}
		if len(program.Statements) != 1 {
			t.Errorf("Expected 1 statement for '%s', got %d", q, len(program.Statements))
		}
	}
}

func TestParseDelete(t *testing.T) {
	queries := []string{
		"DELETE FROM items WHERE status = 'decommissioned'",
		"DELETE FROM items WHERE category_id = 5 AND status = 'inactive'",
	}

	for _, q := range queries {
		program, errs := tsqlparser.Parse(q)
		if len(errs) > 0 {
			t.Errorf("Failed to parse '%s': %v", q, errs[0])
			continue
		}
		if len(program.Statements) != 1 {
			t.Errorf("Expected 1 statement for '%s', got %d", q, len(program.Statements))
		}
	}
}

func TestAggregates(t *testing.T) {
	tests := []struct {
		name     string
		values   []interface{}
		expected interface{}
		fn       AggregateFunc
	}{
		{"count", []interface{}{1, 2, 3, nil, 5}, 4, Aggregates["COUNT"]},
		{"sum", []interface{}{1.0, 2.0, 3.0}, 6.0, Aggregates["SUM"]},
		{"avg", []interface{}{2.0, 4.0, 6.0}, 4.0, Aggregates["AVG"]},
		{"min", []interface{}{3, 1, 2}, 1, Aggregates["MIN"]},
		{"max", []interface{}{3, 1, 2}, 3, Aggregates["MAX"]},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := tt.fn(tt.values)
			if result != tt.expected {
				// Handle float comparison
				if f1, ok := result.(float64); ok {
					if f2, ok := tt.expected.(float64); ok {
						if f1 != f2 {
							t.Errorf("%s: expected %v, got %v", tt.name, tt.expected, result)
						}
						return
					}
				}
				if result != tt.expected {
					t.Errorf("%s: expected %v, got %v", tt.name, tt.expected, result)
				}
			}
		})
	}
}

func TestCompareValues(t *testing.T) {
	tests := []struct {
		a, b     interface{}
		expected int
	}{
		{1, 2, -1},
		{2, 1, 1},
		{1, 1, 0},
		{"a", "b", -1},
		{"b", "a", 1},
		{"a", "a", 0},
		{nil, 1, -1},
		{1, nil, 1},
		{nil, nil, 0},
	}

	for _, tt := range tests {
		result := compareValues(tt.a, tt.b)
		if result != tt.expected {
			t.Errorf("compareValues(%v, %v): expected %d, got %d", tt.a, tt.b, tt.expected, result)
		}
	}
}

func TestMatchLike(t *testing.T) {
	tests := []struct {
		value, pattern string
		expected       bool
	}{
		{"hello", "hello", true},
		{"hello", "Hello", true}, // Case insensitive
		{"hello", "%ello", true},
		{"hello", "hell%", true},
		{"hello", "%ell%", true},
		{"hello", "%xyz%", false},
		{"hello", "xyz%", false},
		{"hello", "%xyz", false},
	}

	for _, tt := range tests {
		result := matchLike(tt.value, tt.pattern)
		if result != tt.expected {
			t.Errorf("matchLike(%q, %q): expected %v, got %v", tt.value, tt.pattern, tt.expected, result)
		}
	}
}

func TestNormalizeEntityName(t *testing.T) {
	tests := []struct {
		input, expected string
	}{
		{"items", "items"},
		{"Items", "items"},
		{"[items]", "items"},
		{"dbo.items", "items"},
		{"dbo.[Items]", "items"},
		{`"items"`, "items"},
	}

	for _, tt := range tests {
		result := normalizeEntityName(tt.input)
		if result != tt.expected {
			t.Errorf("normalizeEntityName(%q): expected %q, got %q", tt.input, tt.expected, result)
		}
	}
}

func TestValidatorRejectsUpdateWithoutWhere(t *testing.T) {
	v := NewValidator("/tmp/nonexistent")

	// This should fail validation
	program, _ := tsqlparser.Parse("UPDATE items SET status = 'inactive'")
	if len(program.Statements) == 1 {
		err := v.Validate(program.Statements[0])
		if err == nil {
			t.Error("Expected error for UPDATE without WHERE")
		}
		if err != nil && err.Error() != "UPDATE without WHERE clause is not permitted" {
			t.Errorf("Unexpected error message: %v", err)
		}
	}
}

func TestValidatorRejectsDeleteWithoutWhere(t *testing.T) {
	v := NewValidator("/tmp/nonexistent")

	// This should fail validation
	program, _ := tsqlparser.Parse("DELETE FROM items")
	if len(program.Statements) == 1 {
		err := v.Validate(program.Statements[0])
		if err == nil {
			t.Error("Expected error for DELETE without WHERE")
		}
		if err != nil && err.Error() != "DELETE without WHERE clause is not permitted" {
			t.Errorf("Unexpected error message: %v", err)
		}
	}
}
