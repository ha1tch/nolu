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

// AggregateSQL holds the generated aggregate query plus metadata needed
// to denormalise decimal results after execution.
type AggregateSQL struct {
	SQL     string
	Args    []interface{}
	Aliases []string // Column aliases in SELECT order

	// DecimalColumns tracks which result aliases are decimal aggregates
	// that need denormalisation. Key: alias, Value: scale.
	DecimalColumns map[string]int
}

// GenerateAggregateSQL builds a GROUP BY + aggregate query for an adapted table.
// Instead of SELECT data, _version FROM entities WHERE ... it generates:
//
//	SELECT category, SUM(price), COUNT(*) FROM olu_products
//	WHERE tenant_id = $1 AND (status = $2) GROUP BY category
//
// The dialect parameter controls placeholder syntax and type coercion.
// This only works for adapted entities with native columns.
func GenerateAggregateSQL(
	stmt *ast.SelectStatement,
	entity string,
	tenantID string,
	store storage.AggregateQueryable,
	dialect SQLDialect,
) (*AggregateSQL, error) {
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

	// Build SELECT columns
	var selectExprs []string
	var aliases []string
	decimalCols := make(map[string]int)

	// Check whether this backend needs post-query decimal denormalisation.
	// PostgreSQL handles decimals natively; SQLite stores them as scaled int64.
	storageDialect := store.StorageDialectFor(entity)
	nativeDecimal := storageDialect != nil && storageDialect.SupportsNativeDecimalAggregation()

	for _, col := range stmt.Columns {
		alias := columnAlias(col)

		if isAggregate(col.Expression) {
			fc := col.Expression.(*ast.FunctionCall)
			funcName := strings.ToUpper(exprToString(fc.Function))

			if funcName == "COUNT" && isCountStar(fc) {
				selectExprs = append(selectExprs, "COUNT(*)")
				aliases = append(aliases, alias)
				continue
			}

			if len(fc.Arguments) > 0 {
				argName := exprToString(fc.Arguments[0])
				colName, scale, isDecimal, colOk := store.AdaptedColumnInfo(entity, argName)
				if !colOk {
					return nil, fmt.Errorf("aggregate field %q not found in adapted table %q", argName, entity)
				}

				sqlExpr := fmt.Sprintf("%s(%s)", funcName, colName)
				selectExprs = append(selectExprs, sqlExpr)
				aliases = append(aliases, alias)

				// Track decimal columns for post-denormalisation, but only
				// if the backend doesn't handle decimals natively.
				if isDecimal && !nativeDecimal {
					decimalCols[alias] = scale
				}
			}
		} else {
			// Non-aggregate column (GROUP BY key)
			fieldName := exprToString(col.Expression)
			colName, _, _, colOk := store.AdaptedColumnInfo(entity, fieldName)
			if !colOk {
				return nil, fmt.Errorf("field %q not found in adapted table %q", fieldName, entity)
			}
			selectExprs = append(selectExprs, colName)
			aliases = append(aliases, alias)
		}
	}

	// FROM + WHERE
	var whereParts []string

	// Tenant filter (only when tenantID is non-empty, matching existing push-down behaviour)
	if tenantID != "" {
		whereParts = append(whereParts, fmt.Sprintf("tenant_id = %s", addArg(tenantID)))
	}

	if stmt.Where != nil {
		whereSQL, err := translateAdaptedWhere(stmt.Where, entity, store, &args, &argIdx, dialect)
		if err != nil {
			return nil, fmt.Errorf("WHERE translation for adapted aggregate: %w", err)
		}
		whereParts = append(whereParts, "("+whereSQL+")")
	}

	whereClause := ""
	if len(whereParts) > 0 {
		whereClause = "WHERE " + strings.Join(whereParts, " AND ")
	}

	// GROUP BY
	var groupByExprs []string
	for _, gb := range stmt.GroupBy {
		fieldName := exprToString(gb)
		colName, _, _, colOk := store.AdaptedColumnInfo(entity, fieldName)
		if !colOk {
			return nil, fmt.Errorf("GROUP BY field %q not found in adapted table %q", fieldName, entity)
		}
		groupByExprs = append(groupByExprs, colName)
	}

	sql := fmt.Sprintf("SELECT %s FROM %s %s",
		strings.Join(selectExprs, ", "),
		tableName,
		whereClause)

	if len(groupByExprs) > 0 {
		sql += " GROUP BY " + strings.Join(groupByExprs, ", ")
	}

	// ORDER BY (if present and references group keys or aggregates)
	if len(stmt.OrderBy) > 0 {
		var orderExprs []string
		for _, ob := range stmt.OrderBy {
			fieldName := exprToString(ob.Expression)
			dir := "ASC"
			if ob.Descending {
				dir = "DESC"
			}

			// Check if it's a column in the adapted table
			colName, _, _, colOk := store.AdaptedColumnInfo(entity, fieldName)
			if colOk {
				orderExprs = append(orderExprs, colName+" "+dir)
			} else {
				// Might be an alias -- use it directly
				orderExprs = append(orderExprs, fieldName+" "+dir)
			}
		}
		if len(orderExprs) > 0 {
			sql += " ORDER BY " + strings.Join(orderExprs, ", ")
		}
	}

	// LIMIT
	if stmt.Top != nil {
		limitVal, err := evalTopCount(stmt.Top)
		if err != nil {
			return nil, fmt.Errorf("LIMIT translation: %w", err)
		}
		sql += " LIMIT " + addArg(limitVal)
	}

	return &AggregateSQL{
		SQL:            sql,
		Args:           args,
		Aliases:        aliases,
		DecimalColumns: decimalCols,
	}, nil
}

// translateAdaptedWhere translates a WHERE expression to SQL using native
// column names instead of json_extract. The dialect controls placeholder
// syntax and type coercion. argIdx is passed by reference so the caller
// and this function share a single monotonically increasing counter.
func translateAdaptedWhere(expr ast.Expression, entity string, store storage.AggregateQueryable, args *[]interface{}, argIdx *int, dialect SQLDialect) (string, error) {
	addArg := func(val interface{}) string {
		*args = append(*args, val)
		*argIdx++
		return dialect.Placeholder(*argIdx)
	}

	switch ex := expr.(type) {
	case *ast.InfixExpression:
		switch ex.Operator {
		case "AND":
			left, err := translateAdaptedWhere(ex.Left, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			right, err := translateAdaptedWhere(ex.Right, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			return left + " AND " + right, nil
		case "OR":
			left, err := translateAdaptedWhere(ex.Left, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			right, err := translateAdaptedWhere(ex.Right, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			return "(" + left + " OR " + right + ")", nil
		default:
			// Comparison operator
			fieldName := exprToString(ex.Left)
			colName, _, _, ok := store.AdaptedColumnInfo(entity, fieldName)
			if !ok {
				return "", fmt.Errorf("field %q not found in adapted table", fieldName)
			}
			val, err := literalValueFromExpr(ex.Right)
			if err != nil {
				return "", err
			}

			// Emit explicit CAST when comparing numeric literals against
			// columns that might have a different storage type. This is
			// required for PostgreSQL (strict typing) and harmless on SQLite.
			lhs := colName
			ph := addArg(val)
			// Numeric literal against a text column (or vice versa): explicit
			// CAST on the column would prevent "operator does not exist" errors
			// on strict backends. No-op on SQLite. Not yet emitted — deferred
			// until the PostgreSQL backend is introduced.
			return fmt.Sprintf("%s %s %s", lhs, ex.Operator, ph), nil
		}

	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			inner, err := translateAdaptedWhere(ex.Right, entity, store, args, argIdx, dialect)
			if err != nil {
				return "", err
			}
			return "NOT (" + inner + ")", nil
		}
		return "", fmt.Errorf("unsupported prefix: %s", ex.Operator)

	case *ast.IsNullExpression:
		fieldName := exprToString(ex.Expr)
		colName, _, _, ok := store.AdaptedColumnInfo(entity, fieldName)
		if !ok {
			return "", fmt.Errorf("field %q not found in adapted table", fieldName)
		}
		if ex.Not {
			return colName + " IS NOT NULL", nil
		}
		return colName + " IS NULL", nil

	case *ast.BetweenExpression:
		fieldName := exprToString(ex.Expr)
		colName, _, _, ok := store.AdaptedColumnInfo(entity, fieldName)
		if !ok {
			return "", fmt.Errorf("field %q not found in adapted table", fieldName)
		}
		low, err := literalValueFromExpr(ex.Low)
		if err != nil {
			return "", err
		}
		high, err := literalValueFromExpr(ex.High)
		if err != nil {
			return "", err
		}
		lph := addArg(low)
		hph := addArg(high)
		sql := fmt.Sprintf("%s BETWEEN %s AND %s", colName, lph, hph)
		if ex.Not {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil

	case *ast.InExpression:
		fieldName := exprToString(ex.Expr)
		colName, _, _, ok := store.AdaptedColumnInfo(entity, fieldName)
		if !ok {
			return "", fmt.Errorf("field %q not found in adapted table", fieldName)
		}
		var phs []string
		for _, v := range ex.Values {
			val, err := literalValueFromExpr(v)
			if err != nil {
				return "", err
			}
			phs = append(phs, addArg(val))
		}
		sql := fmt.Sprintf("%s IN (%s)", colName, strings.Join(phs, ", "))
		if ex.Not {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil

	case *ast.LikeExpression:
		fieldName := exprToString(ex.Expr)
		colName, _, _, ok := store.AdaptedColumnInfo(entity, fieldName)
		if !ok {
			return "", fmt.Errorf("field %q not found in adapted table", fieldName)
		}
		val, err := literalValueFromExpr(ex.Pattern)
		if err != nil {
			return "", err
		}
		ph := addArg(val)
		sql := fmt.Sprintf("%s LIKE %s", colName, ph)
		if ex.Not {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil

	default:
		return "", fmt.Errorf("unsupported expression type for adapted WHERE: %T", expr)
	}
}

// literalValueFromExpr extracts a Go value from an AST literal expression.
func literalValueFromExpr(expr ast.Expression) (interface{}, error) {
	switch v := expr.(type) {
	case *ast.IntegerLiteral:
		return v.Value, nil
	case *ast.FloatLiteral:
		return v.Value, nil
	case *ast.StringLiteral:
		return v.Value, nil
	case *ast.NullLiteral:
		return nil, nil
	case *ast.Identifier:
		// TRUE/FALSE
		name := strings.ToUpper(v.Value)
		if name == "TRUE" {
			return true, nil
		}
		if name == "FALSE" {
			return false, nil
		}
		return v.Value, nil
	default:
		return nil, fmt.Errorf("unsupported literal type: %T", expr)
	}
}

// hasScalarFunctions returns true if any SELECT column or GROUP BY expression
// contains a scalar function (UPPER, LOWER, SUBSTRING, etc.). These can't be
// pushed to adapted-table SQL without additional translation work.
func hasScalarFunctions(columns []ast.SelectColumn, groupBy []ast.Expression) bool {
	for _, col := range columns {
		if fc, ok := col.Expression.(*ast.FunctionCall); ok && IsScalarFunction(fc) {
			return true
		}
	}
	for _, gb := range groupBy {
		if fc, ok := gb.(*ast.FunctionCall); ok && IsScalarFunction(fc) {
			return true
		}
	}
	return false
}

// denormaliseAggregateDecimals converts scaled integer aggregate results back
// to decimal strings. SUM(price) on a scale-2 column returns a scaled int64;
// we divide by 10^scale and format with fixed decimal places.
//
// AVG is special: SQLite's AVG returns a float64 for integer columns. We
// convert through the scaled representation to maintain precision.
func denormaliseAggregateDecimals(
	records []map[string]interface{},
	decimalCols map[string]int,
	aliases []string,
	columns []ast.SelectColumn,
) {
	if len(decimalCols) == 0 {
		return
	}

	// Build a map of alias -> aggregate function name
	aggFuncs := make(map[string]string)
	for _, col := range columns {
		alias := columnAlias(col)
		if fc, ok := col.Expression.(*ast.FunctionCall); ok {
			aggFuncs[alias] = strings.ToUpper(exprToString(fc.Function))
		}
	}

	for _, row := range records {
		for alias, scale := range decimalCols {
			val, exists := row[alias]
			if !exists || val == nil {
				continue
			}

			funcName := aggFuncs[alias]

			switch funcName {
			case "AVG":
				// SQLite AVG on integers returns float64
				// Convert: float result is already in scaled space, divide by 10^scale
				switch v := val.(type) {
				case float64:
					// v is the average of scaled integers, so divide by 10^scale
					result := v / float64(pow10Table[scale])
					row[alias] = formatDecimal(result, scale)
				case int64:
					row[alias] = denormInt64(v, scale)
				}

			case "SUM":
				// SUM on integers returns int64
				switch v := val.(type) {
				case int64:
					row[alias] = denormInt64(v, scale)
				case float64:
					// Shouldn't happen but handle gracefully
					row[alias] = formatDecimal(v/float64(pow10Table[scale]), scale)
				}

			case "MIN", "MAX":
				// MIN/MAX on integers returns int64
				switch v := val.(type) {
				case int64:
					row[alias] = denormInt64(v, scale)
				case float64:
					row[alias] = denormInt64(int64(v), scale)
				}

			case "COUNT":
				// COUNT doesn't need denormalisation

			default:
				// Plain decimal column (not an aggregate) — same treatment
				// as MIN/MAX: the raw value is a scaled int64.
				switch v := val.(type) {
				case int64:
					row[alias] = denormInt64(v, scale)
				case float64:
					row[alias] = denormInt64(int64(v), scale)
				}
			}
		}
	}
}

// denormInt64 converts a scaled int64 to a decimal string.
func denormInt64(n int64, scale int) string {
	if scale <= 0 {
		return fmt.Sprintf("%d", n)
	}

	negative := n < 0
	if negative {
		n = -n
	}

	divisor := pow10Table[scale]
	intPart := n / divisor
	fracPart := n % divisor

	// Format fractional part with leading zeroes
	fracStr := fmt.Sprintf("%d", fracPart)
	for len(fracStr) < scale {
		fracStr = "0" + fracStr
	}

	prefix := ""
	if negative {
		prefix = "-"
	}
	return fmt.Sprintf("%s%d.%s", prefix, intPart, fracStr)
}

// formatDecimal formats a float64 with exactly `scale` decimal places.
func formatDecimal(v float64, scale int) string {
	return fmt.Sprintf("%.*f", scale, v)
}

// pow10Table for quick lookup (matches storage package's table).
var pow10Table = [19]int64{
	1, 10, 100, 1_000, 10_000, 100_000, 1_000_000, 10_000_000,
	100_000_000, 1_000_000_000, 10_000_000_000, 100_000_000_000,
	1_000_000_000_000, 10_000_000_000_000, 100_000_000_000_000,
	1_000_000_000_000_000, 10_000_000_000_000_000, 100_000_000_000_000_000,
	1_000_000_000_000_000_000,
}

// filterRecordsByExpr applies a WHERE/HAVING expression to already-aggregated
// records using the Aggregator's evalCondition (which handles aggregate aliases).
func filterRecordsByExpr(records []map[string]interface{}, expr ast.Expression) []map[string]interface{} {
	agg := NewAggregator()
	var result []map[string]interface{}
	for _, rec := range records {
		if agg.EvalCondition(rec, expr) {
			result = append(result, rec)
		}
	}
	return result
}
