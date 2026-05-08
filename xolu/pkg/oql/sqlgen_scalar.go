// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"fmt"
	"strings"

	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/storage"
)

// translateScalarExpr translates a scalar expression (which may be a
// function call, identifier, or literal) to its SQL representation for
// an adapted table. This is used for SELECT columns, GROUP BY items,
// and ORDER BY items in full push-down queries.
//
// For function calls, it delegates to the dialect's ScalarFunction
// method. For plain identifiers, it maps to the native column name.
// Returns an error if the expression cannot be translated (which
// triggers fallback to the Go path).
func translateScalarExpr(
	expr ast.Expression,
	entity string,
	store storage.AggregateQueryable,
	dialect SQLDialect,
	addArg func(interface{}) string,
) (string, error) {
	switch e := expr.(type) {
	case *ast.FunctionCall:
		funcName := strings.ToUpper(exprToString(e.Function))

		// Check for aggregate functions — these are handled separately
		if isAggregateFunc(funcName) {
			return translateAggregateExpr(e, funcName, entity, store, dialect, addArg)
		}

		// Scalar function: translate arguments, then ask the dialect
		if len(e.Arguments) == 0 {
			// Zero-arg functions (GETDATE, GETUTCDATE)
			sql, ok := dialect.ScalarFunction(funcName, "")
			if !ok {
				return "", fmt.Errorf("unsupported scalar function %q for push-down", funcName)
			}
			return sql, nil
		}

		// Translate each argument
		var argParts []string
		for _, arg := range e.Arguments {
			argSQL, err := translateScalarExpr(arg, entity, store, dialect, addArg)
			if err != nil {
				return "", err
			}
			argParts = append(argParts, argSQL)
		}

		// Multi-argument functions need comma-separated args
		argsSQL := strings.Join(argParts, ", ")
		sql, ok := dialect.ScalarFunction(funcName, argsSQL)
		if !ok {
			return "", fmt.Errorf("unsupported scalar function %q for push-down", funcName)
		}
		return sql, nil

	case *ast.Identifier:
		// Plain field reference — map to native column
		colName, _, _, ok := store.AdaptedColumnInfo(entity, e.Value)
		if !ok {
			// Could be an aggregate alias or a literal like TRUE/FALSE
			upper := strings.ToUpper(e.Value)
			if upper == "TRUE" || upper == "FALSE" {
				return upper, nil
			}
			return "", fmt.Errorf("field %q not found in adapted table %q", e.Value, entity)
		}
		return colName, nil

	case *ast.QualifiedIdentifier:
		// Dotted path — try the full string
		colName, _, _, ok := store.AdaptedColumnInfo(entity, e.String())
		if !ok {
			return "", fmt.Errorf("field %q not found in adapted table %q", e.String(), entity)
		}
		return colName, nil

	case *ast.IntegerLiteral:
		return addArg(e.Value), nil

	case *ast.FloatLiteral:
		return addArg(e.Value), nil

	case *ast.StringLiteral:
		return addArg(e.Value), nil

	case *ast.NullLiteral:
		return "NULL", nil

	case *ast.InfixExpression:
		// Arithmetic expression: translate both sides
		left, err := translateScalarExpr(e.Left, entity, store, dialect, addArg)
		if err != nil {
			return "", err
		}
		right, err := translateScalarExpr(e.Right, entity, store, dialect, addArg)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s %s %s)", left, e.Operator, right), nil

	default:
		return "", fmt.Errorf("unsupported expression type for adapted push-down: %T", expr)
	}
}

// translateAggregateExpr translates an aggregate function call to SQL.
func translateAggregateExpr(
	fc *ast.FunctionCall,
	funcName string,
	entity string,
	store storage.AggregateQueryable,
	dialect SQLDialect,
	addArg func(interface{}) string,
) (string, error) {
	if funcName == "COUNT" && isCountStar(fc) {
		return "COUNT(*)", nil
	}

	if len(fc.Arguments) == 0 {
		return "", fmt.Errorf("aggregate %s requires an argument", funcName)
	}

	argSQL, err := translateScalarExpr(fc.Arguments[0], entity, store, dialect, addArg)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s(%s)", funcName, argSQL), nil
}

// isAggregateFunc reports whether a function name is an aggregate.
func isAggregateFunc(name string) bool {
	switch name {
	case "COUNT", "SUM", "AVG", "MIN", "MAX":
		return true
	default:
		return false
	}
}

// isFullyTranslatable checks whether a complete SELECT statement can be
// translated to adapted-table SQL. Returns true if every expression in
// SELECT, WHERE, GROUP BY, HAVING, and ORDER BY can be translated.
//
// This is a conservative check: if anything is untranslatable, the
// entire query falls back to the Go pipeline.
func isFullyTranslatable(
	stmt *ast.SelectStatement,
	entity string,
	store storage.AggregateQueryable,
	dialect SQLDialect,
) bool {
	// Check SELECT columns
	for _, col := range stmt.Columns {
		if _, err := translateScalarExpr(col.Expression, entity, store, dialect, dummyAddArg); err != nil {
			return false
		}
	}

	// Check WHERE
	if stmt.Where != nil {
		if !isAdaptedWherePushable(stmt.Where, entity, store) {
			return false
		}
	}

	// Check GROUP BY
	for _, gb := range stmt.GroupBy {
		if _, err := translateScalarExpr(gb, entity, store, dialect, dummyAddArg); err != nil {
			return false
		}
	}

	// Check HAVING
	if stmt.Having != nil {
		if !isAdaptedHavingPushable(stmt.Having, entity, store, dialect) {
			return false
		}
	}

	// Check ORDER BY
	for _, ob := range stmt.OrderBy {
		if _, err := translateScalarExpr(ob.Expression, entity, store, dialect, dummyAddArg); err != nil {
			return false
		}
	}

	return true
}

// dummyAddArg is a no-op placeholder generator used only during
// translatability checks (we don't need actual placeholders).
func dummyAddArg(_ interface{}) string { return "?" }

// isAdaptedWherePushable checks if a WHERE expression is translatable
// for adapted tables. This reuses the blob-path pushability check for
// expression structure, but also verifies that all referenced fields
// exist as adapted columns.
func isAdaptedWherePushable(expr ast.Expression, entity string, store storage.AggregateQueryable) bool {
	switch ex := expr.(type) {
	case *ast.InfixExpression:
		switch ex.Operator {
		case "AND", "OR":
			return isAdaptedWherePushable(ex.Left, entity, store) &&
				isAdaptedWherePushable(ex.Right, entity, store)
		default:
			return isAdaptedField(ex.Left, entity, store) && isLiteralOrParam(ex.Right)
		}
	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			return isAdaptedWherePushable(ex.Right, entity, store)
		}
		return false
	case *ast.IsNullExpression:
		return isAdaptedField(ex.Expr, entity, store)
	case *ast.BetweenExpression:
		return isAdaptedField(ex.Expr, entity, store) &&
			isLiteralOrParam(ex.Low) && isLiteralOrParam(ex.High)
	case *ast.InExpression:
		if !isAdaptedField(ex.Expr, entity, store) {
			return false
		}
		for _, v := range ex.Values {
			if !isLiteralOrParam(v) {
				return false
			}
		}
		return true
	case *ast.LikeExpression:
		return isAdaptedField(ex.Expr, entity, store) && isLiteralOrParam(ex.Pattern)
	default:
		return false
	}
}

// isAdaptedField checks if an expression is a field reference that exists
// in the adapted table.
func isAdaptedField(expr ast.Expression, entity string, store storage.AggregateQueryable) bool {
	switch e := expr.(type) {
	case *ast.Identifier:
		_, _, _, ok := store.AdaptedColumnInfo(entity, e.Value)
		return ok
	case *ast.QualifiedIdentifier:
		_, _, _, ok := store.AdaptedColumnInfo(entity, e.String())
		return ok
	default:
		return false
	}
}

// isAdaptedHavingPushable checks if a HAVING expression can be translated.
// HAVING may reference aggregate aliases or aggregate expressions.
func isAdaptedHavingPushable(
	expr ast.Expression,
	entity string,
	store storage.AggregateQueryable,
	dialect SQLDialect,
) bool {
	switch ex := expr.(type) {
	case *ast.InfixExpression:
		switch ex.Operator {
		case "AND", "OR":
			return isAdaptedHavingPushable(ex.Left, entity, store, dialect) &&
				isAdaptedHavingPushable(ex.Right, entity, store, dialect)
		default:
			// LHS could be an aggregate function or alias
			_, lErr := translateScalarExpr(ex.Left, entity, store, dialect, dummyAddArg)
			return lErr == nil && isLiteralOrParam(ex.Right)
		}
	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			return isAdaptedHavingPushable(ex.Right, entity, store, dialect)
		}
		return false
	default:
		return false
	}
}
