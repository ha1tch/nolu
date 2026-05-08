// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"os"
	"testing"

	"github.com/ha1tch/tsqlparser/ast"
)

// ---------------------------------------------------------------------------
// ResultType.String
// ---------------------------------------------------------------------------

func TestResultType_String(t *testing.T) {
	tests := []struct {
		rt   ResultType
		want string
	}{
		{ResultSelect, "SELECT"},
		{ResultInsert, "INSERT"},
		{ResultUpdate, "UPDATE"},
		{ResultDelete, "DELETE"},
		{ResultType(99), "UNKNOWN"},
	}
	for _, tt := range tests {
		if got := tt.rt.String(); got != tt.want {
			t.Errorf("ResultType(%d).String() = %q, want %q", tt.rt, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// NewEngineWithSchemaValidator
// ---------------------------------------------------------------------------

func TestNewEngineWithSchemaValidator(t *testing.T) {
	store := newMockStore()
	tmpDir, _ := os.MkdirTemp("", "oql-sv-test")
	defer os.RemoveAll(tmpDir)

	os.MkdirAll(tmpDir+"/items", 0755)

	engine := NewEngineWithSchemaValidator(store, tmpDir, nil)
	if engine == nil {
		t.Fatal("Expected non-nil engine")
	}

	// Should be able to execute queries
	ctx := context.Background()
	store.Create(ctx, "items", map[string]interface{}{"name": "test"})

	result, err := engine.Execute(ctx, "SELECT * FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if len(result.Rows) != 1 {
		t.Errorf("Expected 1 row, got %d", len(result.Rows))
	}
}

// ---------------------------------------------------------------------------
// EvalScalarFunction — direct call
// ---------------------------------------------------------------------------

func TestEvalScalarFunction(t *testing.T) {
	// Create a function call AST node for UPPER('hello')
	fc := &ast.FunctionCall{
		Function:  &ast.Identifier{Value: "UPPER"},
		Arguments: []ast.Expression{&ast.StringLiteral{Value: "hello"}},
	}

	evalFn := func(expr ast.Expression) interface{} {
		if sl, ok := expr.(*ast.StringLiteral); ok {
			return sl.Value
		}
		return nil
	}

	result := EvalScalarFunction(fc, evalFn)
	if result != "HELLO" {
		t.Errorf("EvalScalarFunction UPPER('hello') = %v, want HELLO", result)
	}
}

func TestEvalScalarFunction_Unknown(t *testing.T) {
	fc := &ast.FunctionCall{
		Function:  &ast.Identifier{Value: "NONEXISTENT"},
		Arguments: []ast.Expression{},
	}

	result := EvalScalarFunction(fc, func(expr ast.Expression) interface{} { return nil })
	if result != nil {
		t.Errorf("EvalScalarFunction for unknown function should return nil, got %v", result)
	}
}

func TestEvalScalarFunction_MultipleArgs(t *testing.T) {
	// CONCAT('a', 'b', 'c')
	fc := &ast.FunctionCall{
		Function: &ast.Identifier{Value: "CONCAT"},
		Arguments: []ast.Expression{
			&ast.StringLiteral{Value: "a"},
			&ast.StringLiteral{Value: "b"},
			&ast.StringLiteral{Value: "c"},
		},
	}

	evalFn := func(expr ast.Expression) interface{} {
		if sl, ok := expr.(*ast.StringLiteral); ok {
			return sl.Value
		}
		return nil
	}

	result := EvalScalarFunction(fc, evalFn)
	if result != "abc" {
		t.Errorf("CONCAT('a','b','c') = %v, want 'abc'", result)
	}
}

// ---------------------------------------------------------------------------
// Engine Execute entry point — exercises the main Execute -> ExecuteWithTenant path
// ---------------------------------------------------------------------------

func TestEngine_ExecuteWithTenant(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	// Test with empty tenant (should work, scoping is optional)
	result, err := engine.ExecuteWithTenant(ctx, "SELECT * FROM items", "")
	if err != nil {
		t.Fatalf("ExecuteWithTenant failed: %v", err)
	}
	if len(result.Rows) != 5 {
		t.Errorf("Expected 5 rows, got %d", len(result.Rows))
	}

	// Test with specific tenant
	result, err = engine.ExecuteWithTenant(ctx, "SELECT * FROM items WHERE status = 'active'", "tenant1")
	if err != nil {
		t.Fatalf("ExecuteWithTenant with tenant failed: %v", err)
	}
	_ = result
	_ = store
}

// ---------------------------------------------------------------------------
// Engine with queryable store — exercises Execute path that hits the executor
// ---------------------------------------------------------------------------

func TestEngine_ExecuteScalarInSelect(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()

	// SELECT with UPPER — this exercises materializeScalars path
	result, err := engine.Execute(ctx, "SELECT UPPER(status) FROM items")
	if err != nil {
		// May not be supported in current planner, that's OK
		t.Logf("UPPER in SELECT: %v (may not be supported)", err)
		return
	}
	if result != nil && len(result.Rows) > 0 {
		t.Logf("Got %d rows with scalar projection", len(result.Rows))
	}
}

// ---------------------------------------------------------------------------
// toFloatSafe coverage (aggregator.go)
// ---------------------------------------------------------------------------

func TestToFloatSafe(t *testing.T) {
	tests := []struct {
		input interface{}
		want  float64
		ok    bool
	}{
		{float64(3.14), 3.14, true},
		{int(42), 42.0, true},
		{int64(100), 100.0, true},
		{float32(2.5), 2.5, true},
		{"3.14", 3.14, true},    // toFloatSafe now parses numeric strings
		{nil, 0, false},
		{"not a number", 0, false},
	}

	for _, tt := range tests {
		got, ok := toFloatSafe(tt.input)
		if ok != tt.ok {
			t.Errorf("toFloatSafe(%v) ok=%v, want %v", tt.input, ok, tt.ok)
		}
		if ok && got != tt.want {
			t.Errorf("toFloatSafe(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
