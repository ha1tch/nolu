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

// AdaptedSQL holds the output of GenerateAdaptedSQL: a complete SELECT
// statement targeting native columns in an adapted table, plus metadata
// for post-query decimal denormalisation.
type AdaptedSQL struct {
	SQL     string
	Args    []interface{}
	Aliases []string // Column aliases in SELECT order

	// DecimalColumns tracks which result aliases need denormalisation.
	// Only populated when the backend stores decimals as scaled integers
	// (i.e. SupportsNativeDecimalAggregation() returns false).
	DecimalColumns map[string]int
}

// GenerateAdaptedSQL translates a complete OQL SELECT statement into
// adapted-table SQL. Unlike GenerateAggregateSQL (which only handles
// GROUP BY + aggregates), this generates full queries including:
//
//   - SELECT with scalars, aggregates, and plain columns
//   - WHERE with all comparison operators
//   - GROUP BY
//   - HAVING (translated to SQL, not evaluated in Go)
//   - ORDER BY
//   - DISTINCT
//   - LIMIT
//
// The caller must verify isFullyTranslatable() before calling this.
// If any clause is untranslatable, the executor falls back to the
// Go pipeline via the existing paths.
func GenerateAdaptedSQL(
	stmt *ast.SelectStatement,
	entity string,
	tenantID string,
	store storage.AggregateQueryable,
	dialect SQLDialect,
) (*AdaptedSQL, error) {
	tableName, ok := store.AdaptedTableName(entity)
	if !ok {
		return nil, fmt.Errorf("entity %q is not adapted", entity)
	}

	var args []interface{}
	argIdx := 0
	addArg := func(val interface{}) string {
		args = append(args, val)
		argIdx++
		return dialect.Placeholder(argIdx)
	}

	// Check native decimal support
	storageDialect := store.StorageDialectFor(entity)
	nativeDecimal := storageDialect != nil && storageDialect.SupportsNativeDecimalAggregation()

	// -- SELECT columns --
	var selectExprs []string
	var aliases []string
	decimalCols := make(map[string]int)

	for _, col := range stmt.Columns {
		alias := columnAlias(col)

		sqlExpr, err := translateScalarExpr(col.Expression, entity, store, dialect, addArg)
		if err != nil {
			return nil, fmt.Errorf("SELECT column %q: %w", alias, err)
		}

		// Track decimal aggregates for denormalisation
		if !nativeDecimal {
			trackDecimalColumn(col, entity, store, alias, decimalCols)
		}

		selectExprs = append(selectExprs, sqlExpr)
		aliases = append(aliases, alias)
	}

	// -- DISTINCT --
	selectKeyword := "SELECT"
	if stmt.Distinct {
		selectKeyword = "SELECT DISTINCT"
	}

	// -- WHERE --
	var whereParts []string

	if tenantID != "" {
		whereParts = append(whereParts, fmt.Sprintf("tenant_id = %s", addArg(tenantID)))
	}

	if stmt.Where != nil {
		whereSQL, err := translateAdaptedWhere(stmt.Where, entity, store, &args, &argIdx, dialect)
		if err != nil {
			return nil, fmt.Errorf("WHERE: %w", err)
		}
		whereParts = append(whereParts, "("+whereSQL+")")
	}

	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = "WHERE " + strings.Join(whereParts, " AND ")
	}

	// -- GROUP BY --
	var groupByExprs []string
	for _, gb := range stmt.GroupBy {
		gbSQL, err := translateScalarExpr(gb, entity, store, dialect, addArg)
		if err != nil {
			return nil, fmt.Errorf("GROUP BY: %w", err)
		}
		groupByExprs = append(groupByExprs, gbSQL)
	}

	// -- Assemble base query --
	sql := fmt.Sprintf("%s %s FROM %s %s",
		selectKeyword,
		strings.Join(selectExprs, ", "),
		tableName,
		whereClause)

	if len(groupByExprs) > 0 {
		sql += " GROUP BY " + strings.Join(groupByExprs, ", ")
	}

	// -- HAVING --
	if stmt.Having != nil {
		havingSQL, err := translateAdaptedHaving(stmt.Having, entity, store, &args, &argIdx, dialect)
		if err != nil {
			return nil, fmt.Errorf("HAVING: %w", err)
		}
		sql += " HAVING " + havingSQL
	}

	// -- ORDER BY --
	if len(stmt.OrderBy) > 0 {
		var orderExprs []string
		for _, ob := range stmt.OrderBy {
			obSQL, err := translateScalarExpr(ob.Expression, entity, store, dialect, addArg)
			if err != nil {
				return nil, fmt.Errorf("ORDER BY: %w", err)
			}
			dir := "ASC"
			if ob.Descending {
				dir = "DESC"
			}
			orderExprs = append(orderExprs, obSQL+" "+dir)
		}
		sql += " ORDER BY " + strings.Join(orderExprs, ", ")
	}

	// -- LIMIT --
	if stmt.Top != nil {
		limitVal, err := evalTopCount(stmt.Top)
		if err != nil {
			return nil, fmt.Errorf("LIMIT: %w", err)
		}
		sql += " LIMIT " + addArg(limitVal)
	}

	return &AdaptedSQL{
		SQL:            sql,
		Args:           args,
		Aliases:        aliases,
		DecimalColumns: decimalCols,
	}, nil
}

// trackDecimalColumn checks if a SELECT column involves a decimal field
// that needs post-query denormalisation, and records it.
// This covers both aggregate functions on decimal columns (SUM, AVG, MIN,
// MAX) and plain decimal column references.
func trackDecimalColumn(
	col ast.SelectColumn,
	entity string,
	store storage.AggregateQueryable,
	alias string,
	decimalCols map[string]int,
) {
	if isAggregate(col.Expression) {
		fc := col.Expression.(*ast.FunctionCall)
		funcName := strings.ToUpper(exprToString(fc.Function))
		if funcName == "COUNT" {
			return // COUNT doesn't need denormalisation
		}
		if len(fc.Arguments) == 0 {
			return
		}
		argName := exprToString(fc.Arguments[0])
		_, scale, isDecimal, ok := store.AdaptedColumnInfo(entity, argName)
		if ok && isDecimal {
			decimalCols[alias] = scale
		}
		return
	}

	// Plain column reference — check if it's a decimal field
	fieldName := exprToString(col.Expression)
	_, scale, isDecimal, ok := store.AdaptedColumnInfo(entity, fieldName)
	if ok && isDecimal {
		decimalCols[alias] = scale
	}
}

// translateAdaptedHaving translates a HAVING expression to SQL using
// aggregate functions and native column names.
func translateAdaptedHaving(
	expr ast.Expression,
	entity string,
	store storage.AggregateQueryable,
	args *[]interface{},
	argIdx *int,
	dialect SQLDialect,
) (string, error) {
	addArg := func(val interface{}) string {
		*args = append(*args, val)
		*argIdx++
		return dialect.Placeholder(*argIdx)
	}

	switch ex := expr.(type) {
	case *ast.InfixExpression:
		switch ex.Operator {
		case "AND":
			left, err := translateAdaptedHaving(ex.Left, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			right, err := translateAdaptedHaving(ex.Right, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			return left + " AND " + right, nil
		case "OR":
			left, err := translateAdaptedHaving(ex.Left, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			right, err := translateAdaptedHaving(ex.Right, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			return "(" + left + " OR " + right + ")", nil
		default:
			// Comparison: LHS is an aggregate or field, RHS is a literal
			lhsSQL, err := translateScalarExpr(ex.Left, entity, store, dialect, addArg)
			if err != nil {
				return "", fmt.Errorf("HAVING LHS: %w", err)
			}
			val, err := literalValueFromExpr(ex.Right)
			if err != nil {
				return "", fmt.Errorf("HAVING RHS: %w", err)
			}
			ph := addArg(val)
			return fmt.Sprintf("%s %s %s", lhsSQL, ex.Operator, ph), nil
		}
	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			inner, err := translateAdaptedHaving(ex.Right, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			return "NOT (" + inner + ")", nil
		}
		return "", fmt.Errorf("unsupported HAVING prefix: %s", ex.Operator)
	default:
		return "", fmt.Errorf("unsupported HAVING expression type: %T", expr)
	}
}
