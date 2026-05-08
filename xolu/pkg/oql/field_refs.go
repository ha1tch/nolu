// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"github.com/ha1tch/tsqlparser/ast"
)

// extractQueryFields collects all field names referenced anywhere in a
// SELECT statement: columns, WHERE, GROUP BY, HAVING, ORDER BY. If any
// column is SELECT * the function returns isSelectStar=true and an empty
// field list (the caller must fetch all fields).
//
// The returned fields are deduplicated but not ordered.
func extractQueryFields(s *ast.SelectStatement) (fields []string, isSelectStar bool) {
	seen := map[string]bool{}

	// SELECT columns
	for _, col := range s.Columns {
		if col.AllColumns {
			return nil, true
		}
		collectFieldRefs(col.Expression, seen)
	}

	// WHERE
	if s.Where != nil {
		collectFieldRefs(s.Where, seen)
	}

	// GROUP BY
	for _, expr := range s.GroupBy {
		collectFieldRefs(expr, seen)
	}

	// HAVING
	if s.Having != nil {
		collectFieldRefs(s.Having, seen)
	}

	// ORDER BY
	for _, item := range s.OrderBy {
		collectFieldRefs(item.Expression, seen)
	}

	result := make([]string, 0, len(seen))
	for f := range seen {
		result = append(result, f)
	}
	return result, false
}

// collectFieldRefs walks an AST expression tree and adds all simple
// field references (identifiers) to the seen set.
func collectFieldRefs(expr ast.Expression, seen map[string]bool) {
	if expr == nil {
		return
	}

	switch e := expr.(type) {
	case *ast.Identifier:
		// Skip SQL keywords that parse as identifiers
		switch e.Value {
		case "true", "TRUE", "false", "FALSE", "null", "NULL":
			return
		}
		seen[e.Value] = true

	case *ast.QualifiedIdentifier:
		// Nested field like address.city — the root is the JSON key
		if len(e.Parts) > 0 {
			seen[e.Parts[0].Value] = true
		}

	case *ast.InfixExpression:
		collectFieldRefs(e.Left, seen)
		collectFieldRefs(e.Right, seen)

	case *ast.PrefixExpression:
		collectFieldRefs(e.Right, seen)

	case *ast.FunctionCall:
		for _, arg := range e.Arguments {
			collectFieldRefs(arg, seen)
		}

	case *ast.IsNullExpression:
		collectFieldRefs(e.Expr, seen)

	case *ast.BetweenExpression:
		collectFieldRefs(e.Expr, seen)
		collectFieldRefs(e.Low, seen)
		collectFieldRefs(e.High, seen)

	case *ast.InExpression:
		collectFieldRefs(e.Expr, seen)
		for _, v := range e.Values {
			collectFieldRefs(v, seen)
		}

	case *ast.LikeExpression:
		collectFieldRefs(e.Expr, seen)
		collectFieldRefs(e.Pattern, seen)

	case *ast.CaseExpression:
		if e.Operand != nil {
			collectFieldRefs(e.Operand, seen)
		}
		for _, w := range e.WhenClauses {
			collectFieldRefs(w.Condition, seen)
			collectFieldRefs(w.Result, seen)
		}
		if e.ElseClause != nil {
			collectFieldRefs(e.ElseClause, seen)
		}

	case *ast.CastExpression:
		collectFieldRefs(e.Expression, seen)

		// Literals — no field references
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.StringLiteral, *ast.NullLiteral:
		return
	}
}
