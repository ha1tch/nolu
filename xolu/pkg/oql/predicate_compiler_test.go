// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"testing"

	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/jsonic"
)

func id(name string) *ast.Identifier {
	return &ast.Identifier{Value: name}
}

func strLit(val string) *ast.StringLiteral {
	return &ast.StringLiteral{Value: val}
}

func intLit(val int64) *ast.IntegerLiteral {
	return &ast.IntegerLiteral{Value: val}
}

func floatLit(val float64) *ast.FloatLiteral {
	return &ast.FloatLiteral{Value: val}
}

func infix(left ast.Expression, op string, right ast.Expression) *ast.InfixExpression {
	return &ast.InfixExpression{Left: left, Operator: op, Right: right}
}

func TestCompilePredicates_SimpleEq(t *testing.T) {
	// WHERE name = 'Alice'
	where := infix(id("name"), "=", strLit("Alice"))
	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %v", result.Preds)
	}
	if result.Residual != nil {
		t.Errorf("expected no residual, got %v", result.Residual)
	}

	p := result.Preds.Predicates[0]
	if p.Name != "name" || p.Op != jsonic.OpEq {
		t.Errorf("predicate = %s %s, want name =", p.Name, p.Op)
	}
	if p.Val != "Alice" {
		t.Errorf("val = %v, want Alice", p.Val)
	}
}

func TestCompilePredicates_NumericGt(t *testing.T) {
	// WHERE age > 18
	where := infix(id("age"), ">", intLit(18))
	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %v", result.Preds)
	}
	p := result.Preds.Predicates[0]
	if p.Name != "age" || p.Op != jsonic.OpGt {
		t.Errorf("predicate = %s %s, want age >", p.Name, p.Op)
	}
	if p.Val != float64(18) {
		t.Errorf("val = %v, want 18.0", p.Val)
	}
}

func TestCompilePredicates_ReversedLiteral(t *testing.T) {
	// WHERE 100 < price  →  price > 100
	where := infix(intLit(100), "<", id("price"))
	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %v", result.Preds)
	}
	p := result.Preds.Predicates[0]
	if p.Name != "price" || p.Op != jsonic.OpGt {
		t.Errorf("predicate = %s %s, want price >", p.Name, p.Op)
	}
}

func TestCompilePredicates_ANDChain(t *testing.T) {
	// WHERE name = 'Alice' AND age > 18 AND active = TRUE
	w1 := infix(id("name"), "=", strLit("Alice"))
	w2 := infix(id("age"), ">", intLit(18))
	w3 := infix(id("active"), "=", id("TRUE"))
	where := infix(infix(w1, "AND", w2), "AND", w3)

	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 3 {
		t.Fatalf("expected 3 predicates, got %d", result.Preds.Len())
	}
	if result.Residual != nil {
		t.Errorf("expected no residual")
	}
}

func TestCompilePredicates_PartialResidual(t *testing.T) {
	// WHERE name = 'Alice' AND x IS NULL
	w1 := infix(id("name"), "=", strLit("Alice"))
	w2 := &ast.IsNullExpression{Expr: id("x")}
	where := infix(w1, "AND", w2)

	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %d", result.Preds.Len())
	}
	if result.Residual == nil {
		t.Fatal("expected residual for IS NULL")
	}
	// The residual should be the IS NULL expression.
	if _, ok := result.Residual.(*ast.IsNullExpression); !ok {
		t.Errorf("residual type = %T, want *ast.IsNullExpression", result.Residual)
	}
}

func TestCompilePredicates_ORNotPushable(t *testing.T) {
	// WHERE name = 'Alice' OR age > 18
	// OR cannot be decomposed — entire expression becomes residual.
	where := infix(infix(id("name"), "=", strLit("Alice")), "OR", infix(id("age"), ">", intLit(18)))

	result := CompilePredicates(where)

	// The OR is a single term (not AND-decomposable), and since it's an
	// InfixExpression with "OR" operator, compileTerm won't match it.
	if result.Preds != nil && result.Preds.Len() > 0 {
		t.Errorf("expected no predicates for OR, got %d", result.Preds.Len())
	}
	if result.Residual == nil {
		t.Fatal("expected residual for OR expression")
	}
}

func TestCompilePredicates_InExpression(t *testing.T) {
	// WHERE status IN ('active', 'pending')
	where := &ast.InExpression{
		Expr:   id("status"),
		Values: []ast.Expression{strLit("active"), strLit("pending")},
	}

	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %v", result.Preds)
	}
	p := result.Preds.Predicates[0]
	if p.Op != jsonic.OpIn {
		t.Errorf("op = %s, want IN", p.Op)
	}
}

func TestCompilePredicates_NotInIsResidual(t *testing.T) {
	// WHERE status NOT IN ('deleted')
	where := &ast.InExpression{
		Expr:   id("status"),
		Not:    true,
		Values: []ast.Expression{strLit("deleted")},
	}

	result := CompilePredicates(where)

	if result.Preds != nil && result.Preds.Len() > 0 {
		t.Errorf("NOT IN should not be pushable")
	}
}

func TestCompilePredicates_LikeExpression(t *testing.T) {
	// WHERE name LIKE 'Ali%'
	where := &ast.LikeExpression{
		Expr:    id("name"),
		Pattern: strLit("Ali%"),
	}

	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %v", result.Preds)
	}
	p := result.Preds.Predicates[0]
	if p.Op != jsonic.OpLike {
		t.Errorf("op = %s, want LIKE", p.Op)
	}
}

func TestCompilePredicates_NilWhere(t *testing.T) {
	result := CompilePredicates(nil)
	if result.Preds != nil {
		t.Error("expected nil preds for nil WHERE")
	}
	if result.Residual != nil {
		t.Error("expected nil residual for nil WHERE")
	}
}

func TestCompilePredicates_BetweenIsResidual(t *testing.T) {
	// WHERE age BETWEEN 18 AND 65
	where := &ast.BetweenExpression{
		Expr: id("age"),
		Low:  intLit(18),
		High: intLit(65),
	}

	result := CompilePredicates(where)

	if result.Preds != nil && result.Preds.Len() > 0 {
		t.Errorf("BETWEEN should not be pushable (for now)")
	}
	if result.Residual == nil {
		t.Fatal("expected residual for BETWEEN")
	}
}

func TestCompilePredicates_FloatLiteral(t *testing.T) {
	// WHERE price >= 19.99
	where := infix(id("price"), ">=", floatLit(19.99))
	result := CompilePredicates(where)

	if result.Preds == nil || result.Preds.Len() != 1 {
		t.Fatalf("expected 1 predicate, got %v", result.Preds)
	}
	p := result.Preds.Predicates[0]
	if p.Type != jsonic.FieldFloat {
		t.Errorf("type = %d, want FieldFloat", p.Type)
	}
	if p.Val != 19.99 {
		t.Errorf("val = %v, want 19.99", p.Val)
	}
}
