// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"strings"

	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/jsonic"
)

// PredicateCompileResult holds the output of compiling a WHERE clause
// into a jsonic.PredicateSet for inline tokenisation filtering.
type PredicateCompileResult struct {
	// Preds is the set of AND-combined predicates that can be evaluated
	// during JSON tokenisation. May be nil if nothing was extractable.
	Preds *jsonic.PredicateSet

	// Residual is the remaining WHERE expression that cannot be pushed
	// into the tokeniser. Must still be evaluated in Go after extraction.
	// Nil if the entire WHERE was compiled to predicates.
	Residual ast.Expression
}

// CompilePredicates attempts to decompose a WHERE expression into a
// jsonic.PredicateSet for inline evaluation during tokenisation (B4),
// plus a residual expression for terms that can't be pushed down.
//
// Only AND-combined simple comparisons are extracted. OR branches,
// NOT wrappers, IS NULL, BETWEEN, subqueries, function calls, and
// any other complex expressions remain in the residual.
//
// The caller should check result.Preds.Len() > 0 before using the
// predicate path.
func CompilePredicates(where ast.Expression) PredicateCompileResult {
	if where == nil {
		return PredicateCompileResult{}
	}

	var preds []jsonic.FieldPredicate
	var residuals []ast.Expression

	// Flatten the AND chain and try to compile each term.
	terms := flattenAND(where)
	for _, term := range terms {
		if fp, ok := compileTerm(term); ok {
			preds = append(preds, fp)
		} else {
			residuals = append(residuals, term)
		}
	}

	var result PredicateCompileResult
	if len(preds) > 0 {
		result.Preds = jsonic.NewPredicateSet(preds)
	}
	if len(residuals) > 0 {
		result.Residual = rebuildAND(residuals)
	}
	return result
}

// flattenAND recursively decomposes AND-joined expressions into a flat
// list of terms. Non-AND expressions are returned as single-element lists.
func flattenAND(expr ast.Expression) []ast.Expression {
	infix, ok := expr.(*ast.InfixExpression)
	if !ok || strings.ToUpper(infix.Operator) != "AND" {
		return []ast.Expression{expr}
	}
	var terms []ast.Expression
	terms = append(terms, flattenAND(infix.Left)...)
	terms = append(terms, flattenAND(infix.Right)...)
	return terms
}

// rebuildAND reconstructs an AND chain from a list of expressions.
func rebuildAND(exprs []ast.Expression) ast.Expression {
	if len(exprs) == 0 {
		return nil
	}
	result := exprs[0]
	for _, e := range exprs[1:] {
		result = &ast.InfixExpression{
			Left:     result,
			Operator: "AND",
			Right:    e,
		}
	}
	return result
}

// compileTerm attempts to convert a single WHERE term into a
// jsonic.FieldPredicate. Returns (pred, true) on success, or
// (zero, false) if the term can't be pushed down.
func compileTerm(expr ast.Expression) (jsonic.FieldPredicate, bool) {
	switch ex := expr.(type) {
	case *ast.InfixExpression:
		return compileInfix(ex)
	case *ast.InExpression:
		return compileIn(ex)
	case *ast.LikeExpression:
		return compileLike(ex)
	default:
		return jsonic.FieldPredicate{}, false
	}
}

// compileInfix handles simple comparison operators: =, !=, <, <=, >, >=.
// Only "field op literal" or "literal op field" forms are extractable.
func compileInfix(ex *ast.InfixExpression) (jsonic.FieldPredicate, bool) {
	op, ok := mapOp(ex.Operator)
	if !ok {
		return jsonic.FieldPredicate{}, false
	}

	// Try field op literal.
	if name, ok := extractFieldName(ex.Left); ok {
		if val, ft, ok := extractLiteral(ex.Right); ok {
			return jsonic.MakeFieldPredicate(name, ft, op, val), true
		}
	}

	// Try literal op field (reverse the operator).
	if name, ok := extractFieldName(ex.Right); ok {
		if val, ft, ok := extractLiteral(ex.Left); ok {
			return jsonic.MakeFieldPredicate(name, ft, reverseOp(op), val), true
		}
	}

	return jsonic.FieldPredicate{}, false
}

// compileIn handles IN expressions: field IN (literal, literal, ...).
func compileIn(ex *ast.InExpression) (jsonic.FieldPredicate, bool) {
	// NOT IN can't be pushed down (it's a negative predicate).
	if ex.Not {
		return jsonic.FieldPredicate{}, false
	}
	// Subquery IN can't be pushed down.
	if ex.Subquery != nil {
		return jsonic.FieldPredicate{}, false
	}

	name, ok := extractFieldName(ex.Expr)
	if !ok {
		return jsonic.FieldPredicate{}, false
	}

	// All values must be literals of the same type family.
	var vals []interface{}
	var ft jsonic.FieldType
	for i, v := range ex.Values {
		val, vft, ok := extractLiteral(v)
		if !ok {
			return jsonic.FieldPredicate{}, false
		}
		if i == 0 {
			ft = vft
		}
		vals = append(vals, val)
	}

	return jsonic.MakeFieldPredicate(name, ft, jsonic.OpIn, vals), true
}

// compileLike handles LIKE expressions: field LIKE 'pattern'.
func compileLike(ex *ast.LikeExpression) (jsonic.FieldPredicate, bool) {
	// NOT LIKE can't be pushed down.
	if ex.Not {
		return jsonic.FieldPredicate{}, false
	}
	// ESCAPE clause makes matching more complex — skip.
	if ex.Escape != nil {
		return jsonic.FieldPredicate{}, false
	}

	name, ok := extractFieldName(ex.Expr)
	if !ok {
		return jsonic.FieldPredicate{}, false
	}

	pattern, ok := extractStringLiteral(ex.Pattern)
	if !ok {
		return jsonic.FieldPredicate{}, false
	}

	return jsonic.MakeFieldPredicate(name, jsonic.FieldString, jsonic.OpLike, pattern), true
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// extractFieldName returns the field name from an Identifier expression.
func extractFieldName(expr ast.Expression) (string, bool) {
	switch ex := expr.(type) {
	case *ast.Identifier:
		upper := strings.ToUpper(ex.Value)
		if upper == "TRUE" || upper == "FALSE" || upper == "NULL" {
			return "", false
		}
		return ex.Value, true
	default:
		return "", false
	}
}

// extractLiteral returns the Go value, jsonic FieldType, and ok flag
// for a literal expression.
func extractLiteral(expr ast.Expression) (interface{}, jsonic.FieldType, bool) {
	switch ex := expr.(type) {
	case *ast.StringLiteral:
		return ex.Value, jsonic.FieldString, true
	case *ast.IntegerLiteral:
		// JSON numbers are float64 by convention; use FieldFloat for
		// consistent matching against tokenised values.
		return float64(ex.Value), jsonic.FieldFloat, true
	case *ast.FloatLiteral:
		return ex.Value, jsonic.FieldFloat, true
	case *ast.Identifier:
		upper := strings.ToUpper(ex.Value)
		if upper == "TRUE" {
			return true, jsonic.FieldBool, true
		}
		if upper == "FALSE" {
			return false, jsonic.FieldBool, true
		}
		return nil, 0, false
	default:
		return nil, 0, false
	}
}

// extractStringLiteral extracts a string value from a StringLiteral.
func extractStringLiteral(expr ast.Expression) (string, bool) {
	sl, ok := expr.(*ast.StringLiteral)
	if !ok {
		return "", false
	}
	return sl.Value, true
}

// mapOp maps an OQL comparison operator string to jsonic.PredicateOp.
func mapOp(op string) (jsonic.PredicateOp, bool) {
	switch op {
	case "=":
		return jsonic.OpEq, true
	case "!=", "<>":
		return jsonic.OpNeq, true
	case "<":
		return jsonic.OpLt, true
	case "<=":
		return jsonic.OpLte, true
	case ">":
		return jsonic.OpGt, true
	case ">=":
		return jsonic.OpGte, true
	default:
		return 0, false
	}
}

// reverseOp flips a comparison operator for "literal op field" → "field reverseOp literal".
func reverseOp(op jsonic.PredicateOp) jsonic.PredicateOp {
	switch op {
	case jsonic.OpLt:
		return jsonic.OpGt
	case jsonic.OpLte:
		return jsonic.OpGte
	case jsonic.OpGt:
		return jsonic.OpLt
	case jsonic.OpGte:
		return jsonic.OpLte
	default:
		return op // =, != are symmetric
	}
}
