// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/ha1tch/tsqlparser/ast"
)

// ---------------------------------------------------------------------------
// Dialect abstraction
// ---------------------------------------------------------------------------

// SQLDialect defines how to emit backend-specific SQL fragments.
// The generator calls dialect methods to produce the correct syntax
// for the target database. T-SQL arrives via tsqlparser's AST; the
// dialect translates it to the backend's native SQL.
type SQLDialect interface {
	// JSONField emits a field extraction from the JSON data column.
	// For SQLite: json_extract(data, '$.field')
	// For Postgres: data->>'field' (text) or data->'field' (json)
	JSONField(fieldPath string) string

	// JSONFieldNumeric emits a numeric-typed field extraction.
	// For SQLite: CAST(json_extract(data, '$.field') AS REAL)
	// For Postgres: CAST(data->>'field' AS NUMERIC)
	JSONFieldNumeric(fieldPath string) string

	// Placeholder emits a parameter placeholder for the n-th argument (1-based).
	// For SQLite: ?
	// For Postgres: $1, $2, ...
	Placeholder(n int) string

	// LimitClause emits the LIMIT/TOP equivalent.
	// T-SQL uses TOP N (before columns); SQLite and Postgres use LIMIT N (at end).
	LimitClause(placeholder string) string

	// BaseQuery emits the initial SELECT ... FROM ... WHERE entity_type = <param>.
	// Returns the SQL fragment and the initial argument (entity name).
	BaseQuery(entity string) (sql string, arg interface{})

	// Name returns the dialect identifier (for debug logging).
	Name() string

	// DefaultThreshold returns the minimum entity count at which push-down
	// becomes worthwhile for this backend. Below this count, the fixed
	// overhead of generating and executing backend SQL exceeds the cost
	// of Go-side processing. This varies by backend: an in-process SQLite
	// has near-zero call overhead, while a networked Postgres has
	// connection and round-trip costs that raise the crossover point.
	DefaultThreshold() int

	// ScalarFunction translates an OQL scalar function name to the
	// backend's equivalent SQL expression. Returns the SQL fragment
	// and true, or ("", false) if the function is not supported.
	// Example: ("LEN", "col") -> ("LENGTH(col)", true) on SQLite,
	//          ("LEN", "col") -> ("CHAR_LENGTH(col)", true) on PostgreSQL.
	ScalarFunction(name string, argSQL string) (string, bool)

	// CastExpression emits a CAST for the target backend.
	// Example: CastExpression("price", "REAL") -> "CAST(price AS REAL)"
	// on SQLite, "CAST(price AS DOUBLE PRECISION)" on PostgreSQL.
	CastExpression(expr, targetType string) string
}

// ---------------------------------------------------------------------------
// SQLite dialect
// ---------------------------------------------------------------------------

// SQLiteDialect generates SQLite-compatible SQL using json_extract().
type SQLiteDialect struct{}

func (d *SQLiteDialect) JSONField(fieldPath string) string {
	return fmt.Sprintf("json_extract(data, '$.%s')", fieldPath)
}

func (d *SQLiteDialect) JSONFieldNumeric(fieldPath string) string {
	return fmt.Sprintf("CAST(json_extract(data, '$.%s') AS REAL)", fieldPath)
}

func (d *SQLiteDialect) Placeholder(_ int) string {
	return "?"
}

func (d *SQLiteDialect) LimitClause(placeholder string) string {
	return "LIMIT " + placeholder
}

func (d *SQLiteDialect) BaseQuery(entity string) (string, interface{}) {
	return "SELECT data, _version FROM entities WHERE entity_type = ?", entity
}

func (d *SQLiteDialect) Name() string { return "sqlite" }

// DefaultThreshold returns 50 for SQLite. Benchmarked crossover point:
// push-down is faster than Go-side even at 100 records (1.5x) because
// SQLite is in-process with zero network overhead. The only case where
// push-down is marginal is broad LIKE patterns, where the crossover
// is higher (~500), but a 50-record threshold captures the common case.
func (d *SQLiteDialect) DefaultThreshold() int { return 50 }

// ScalarFunction translates OQL scalar function names to SQLite equivalents.
// Returns ("", false) for functions that have no clean SQLite translation.
func (d *SQLiteDialect) ScalarFunction(name string, argSQL string) (string, bool) {
	switch strings.ToUpper(name) {
	case "UPPER":
		return "UPPER(" + argSQL + ")", true
	case "LOWER":
		return "LOWER(" + argSQL + ")", true
	case "LEN":
		return "LENGTH(" + argSQL + ")", true
	case "TRIM":
		return "TRIM(" + argSQL + ")", true
	case "LTRIM":
		return "LTRIM(" + argSQL + ")", true
	case "RTRIM":
		return "RTRIM(" + argSQL + ")", true
	case "ABS":
		return "ABS(" + argSQL + ")", true
	case "ROUND":
		return "ROUND(" + argSQL + ")", true
	case "COALESCE":
		return "COALESCE(" + argSQL + ")", true
	case "REPLACE":
		return "REPLACE(" + argSQL + ")", true
	case "LENGTH":
		return "LENGTH(" + argSQL + ")", true
	case "TYPEOF":
		return "TYPEOF(" + argSQL + ")", true
	default:
		return "", false
	}
}

// CastExpression emits a CAST expression for SQLite.
func (d *SQLiteDialect) CastExpression(expr, targetType string) string {
	return "CAST(" + expr + " AS " + targetType + ")"
}

// ---------------------------------------------------------------------------
// Field name validation
// ---------------------------------------------------------------------------

// validFieldName matches alphanumeric, underscores, and dots (for nested paths).
// Rejects anything that could be used for SQL injection in json_extract paths.
var validFieldName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.]*$`)

// dangerousFieldChars are characters that must never appear in field names
// embedded in SQL, even if the regex above were loosened.
var dangerousFieldChars = []string{"'", "\"", ")", "--", ";", "/*"}

func validateFieldName(name string) error {
	if !validFieldName.MatchString(name) {
		return fmt.Errorf("invalid field name %q: must be alphanumeric with underscores/dots", name)
	}
	for _, ch := range dangerousFieldChars {
		if strings.Contains(name, ch) {
			return fmt.Errorf("invalid field name %q: contains dangerous character %q", name, ch)
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SQL generation
// ---------------------------------------------------------------------------

// GeneratedSQL holds the output of the SQL generator: a parameterised SQL
// string and the ordered argument list. The SQL is ready for execution
// via storage.Queryable.QueryWithPlan().
type GeneratedSQL struct {
	SQL  string
	Args []interface{}
}

// SQLGenerator translates pushable portions of an OQL AST into
// backend-specific SQL. It uses a Dialect to emit the correct syntax.
type SQLGenerator struct {
	dialect SQLDialect
	args    []interface{}
}

// NewSQLGenerator creates a generator for the given dialect.
func NewSQLGenerator(dialect SQLDialect) *SQLGenerator {
	return &SQLGenerator{
		dialect: dialect,
		args:    nil,
	}
}

// addArg appends an argument and returns the placeholder string.
func (g *SQLGenerator) addArg(val interface{}) string {
	g.args = append(g.args, val)
	return g.dialect.Placeholder(len(g.args))
}

// GenerateSQL translates the pushable portions of an OQL SELECT statement
// into backend-specific SQL. Only operations listed in plan.Push are
// translated; the executor handles the rest in Go.
//
// The function is the main entry point for SQL generation and is
// dialect-agnostic — it delegates all syntax decisions to the dialect.
func GenerateSQL(
	stmt *ast.SelectStatement,
	entity string,
	tenantID string,
	plan QueryPlan,
	dialect SQLDialect,
) (*GeneratedSQL, error) {
	gen := NewSQLGenerator(dialect)

	// Base query: SELECT data FROM entities WHERE entity_type = ?
	baseSql, baseArg := dialect.BaseQuery(entity)
	gen.args = append(gen.args, baseArg)

	var clauses []string
	clauses = append(clauses, baseSql)

	// Tenant filter (always applied if tenantID is non-empty, regardless of plan).
	// If tenantID is purely numeric, it targets the tenant_id INTEGER column
	// (used by ExecuteWithStore which extracts the store's uint16 TenantID).
	// Otherwise it targets json_extract(data, '$.tenant_id') for backward
	// compatibility with ExecuteWithTenant's string-based tenant IDs.
	if tenantID != "" {
		if isNumeric(tenantID) {
			clauses = append(clauses, fmt.Sprintf("AND tenant_id = %s",
				gen.addArg(tenantID)))
		} else {
			clauses = append(clauses, fmt.Sprintf("AND %s = %s",
				dialect.JSONField("tenant_id"), gen.addArg(tenantID)))
		}
	}

	// WHERE push-down
	if plan.pushed(PushWhere) && stmt.Where != nil {
		whereSQL, err := gen.translateExpr(stmt.Where)
		if err != nil {
			return nil, fmt.Errorf("WHERE translation: %w", err)
		}
		clauses = append(clauses, "AND ("+whereSQL+")")
	}

	sql := strings.Join(clauses, " ")

	// ORDER BY push-down
	if plan.pushed(PushOrderBy) && len(stmt.OrderBy) > 0 {
		orderSQL, err := gen.translateOrderBy(stmt.OrderBy)
		if err != nil {
			return nil, fmt.Errorf("ORDER BY translation: %w", err)
		}
		sql += " ORDER BY " + orderSQL
	}

	// LIMIT push-down (T-SQL TOP → backend LIMIT)
	if plan.pushed(PushLimit) && stmt.Top != nil {
		limitVal, err := evalTopCount(stmt.Top)
		if err != nil {
			return nil, fmt.Errorf("LIMIT translation: %w", err)
		}
		placeholder := gen.addArg(limitVal)
		sql += " " + dialect.LimitClause(placeholder)
	}

	return &GeneratedSQL{SQL: sql, Args: gen.args}, nil
}

// ---------------------------------------------------------------------------
// Expression translation
// ---------------------------------------------------------------------------

// translateExpr recursively translates an AST expression to SQL.
func (g *SQLGenerator) translateExpr(expr ast.Expression) (string, error) {
	switch ex := expr.(type) {
	case *ast.InfixExpression:
		return g.translateInfix(ex)

	case *ast.PrefixExpression:
		if ex.Operator == "NOT" {
			inner, err := g.translateExpr(ex.Right)
			if err != nil {
				return "", err
			}
			return "NOT (" + inner + ")", nil
		}
		return "", fmt.Errorf("unsupported prefix operator: %s", ex.Operator)

	case *ast.IsNullExpression:
		field, err := g.fieldPath(ex.Expr)
		if err != nil {
			return "", err
		}
		jsonField := g.dialect.JSONField(field)
		if ex.Not {
			return jsonField + " IS NOT NULL", nil
		}
		return jsonField + " IS NULL", nil

	case *ast.BetweenExpression:
		field, err := g.fieldPath(ex.Expr)
		if err != nil {
			return "", err
		}
		lowVal, err := g.literalValue(ex.Low)
		if err != nil {
			return "", err
		}
		highVal, err := g.literalValue(ex.High)
		if err != nil {
			return "", err
		}
		// BETWEEN always uses numeric comparison (range implies ordering)
		jsonField := g.dialect.JSONFieldNumeric(field)
		lowPh := g.addArg(lowVal)
		highPh := g.addArg(highVal)
		sql := fmt.Sprintf("%s BETWEEN %s AND %s", jsonField, lowPh, highPh)
		if ex.Not {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil

	case *ast.InExpression:
		field, err := g.fieldPath(ex.Expr)
		if err != nil {
			return "", err
		}
		jsonField := g.dialect.JSONField(field)
		var placeholders []string
		for _, v := range ex.Values {
			val, err := g.literalValue(v)
			if err != nil {
				return "", err
			}
			placeholders = append(placeholders, g.addArg(val))
		}
		sql := fmt.Sprintf("%s IN (%s)", jsonField, strings.Join(placeholders, ", "))
		if ex.Not {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil

	case *ast.LikeExpression:
		field, err := g.fieldPath(ex.Expr)
		if err != nil {
			return "", err
		}
		patternVal, err := g.literalValue(ex.Pattern)
		if err != nil {
			return "", err
		}
		jsonField := g.dialect.JSONField(field)
		ph := g.addArg(patternVal)
		sql := fmt.Sprintf("%s LIKE %s", jsonField, ph)
		if ex.Not {
			sql = "NOT (" + sql + ")"
		}
		return sql, nil

	default:
		return "", fmt.Errorf("unsupported expression type for push-down: %T", expr)
	}
}

// translateInfix handles AND, OR, and comparison operators.
func (g *SQLGenerator) translateInfix(ex *ast.InfixExpression) (string, error) {
	switch ex.Operator {
	case "AND", "OR":
		left, err := g.translateExpr(ex.Left)
		if err != nil {
			return "", err
		}
		right, err := g.translateExpr(ex.Right)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("(%s) %s (%s)", left, ex.Operator, right), nil

	default:
		// Comparison: field <op> literal
		field, err := g.fieldPath(ex.Left)
		if err != nil {
			return "", err
		}
		val, err := g.literalValue(ex.Right)
		if err != nil {
			return "", err
		}

		// Determine whether to use numeric or text field extraction.
		// Numeric operators always need CAST; equality depends on the
		// literal's type — this matches the Go-side compareValues logic.
		jsonField := g.chooseFieldExtraction(field, ex.Operator, val)
		ph := g.addArg(val)

		// Map T-SQL <> to standard SQL != (both are valid in SQLite,
		// but normalising keeps output predictable for tests)
		op := ex.Operator
		if op == "<>" {
			op = "!="
		}

		return fmt.Sprintf("%s %s %s", jsonField, op, ph), nil
	}
}

// chooseFieldExtraction picks JSONField or JSONFieldNumeric based on
// the operator and the literal value's type. This mirrors the Go-side
// behaviour in compareValues/toFloatSafe.
//
// Rules:
//   - Ordering operators (>, <, >=, <=): always numeric
//   - Equality (=, !=, <>): numeric if the RHS is numeric, text otherwise
func (g *SQLGenerator) chooseFieldExtraction(field, op string, val interface{}) string {
	switch op {
	case ">", "<", ">=", "<=":
		return g.dialect.JSONFieldNumeric(field)
	case "=", "!=", "<>":
		if isNumericValue(val) {
			return g.dialect.JSONFieldNumeric(field)
		}
		return g.dialect.JSONField(field)
	default:
		return g.dialect.JSONField(field)
	}
}

// ---------------------------------------------------------------------------
// ORDER BY translation
// ---------------------------------------------------------------------------

func (g *SQLGenerator) translateOrderBy(items []*ast.OrderByItem) (string, error) {
	var parts []string
	for _, item := range items {
		field, err := g.fieldPath(item.Expression)
		if err != nil {
			return "", err
		}
		jsonField := g.dialect.JSONField(field)
		dir := "ASC"
		if item.Descending {
			dir = "DESC"
		}
		parts = append(parts, jsonField+" "+dir)
	}
	return strings.Join(parts, ", "), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// fieldPath extracts the dotted field path from an Identifier or
// QualifiedIdentifier and validates it for safe embedding in SQL.
func (g *SQLGenerator) fieldPath(expr ast.Expression) (string, error) {
	var name string
	switch e := expr.(type) {
	case *ast.Identifier:
		name = e.Value
	case *ast.QualifiedIdentifier:
		name = e.String()
	default:
		return "", fmt.Errorf("expected field identifier, got %T", expr)
	}

	if err := validateFieldName(name); err != nil {
		return "", err
	}
	return name, nil
}

// literalValue extracts a Go value from an AST literal expression.
func (g *SQLGenerator) literalValue(expr ast.Expression) (interface{}, error) {
	switch e := expr.(type) {
	case *ast.IntegerLiteral:
		return e.Value, nil
	case *ast.FloatLiteral:
		return e.Value, nil
	case *ast.StringLiteral:
		return e.Value, nil
	case *ast.NullLiteral:
		return nil, nil
	case *ast.Identifier:
		// TRUE/FALSE are identifiers in T-SQL
		upper := strings.ToUpper(e.Value)
		switch upper {
		case "TRUE":
			return true, nil
		case "FALSE":
			return false, nil
		default:
			// Could be a field reference on the RHS — not supported in push-down
			return nil, fmt.Errorf("field reference %q on RHS not supported in push-down", e.Value)
		}
	default:
		return nil, fmt.Errorf("unsupported literal type: %T", expr)
	}
}

// evalTopCount extracts the integer count from a TOP clause.
// T-SQL: TOP 10 — the count is an expression in the AST.
func evalTopCount(top *ast.TopClause) (int64, error) {
	if top.Percent {
		return 0, fmt.Errorf("TOP PERCENT not supported in push-down")
	}
	if top.WithTies {
		return 0, fmt.Errorf("TOP WITH TIES not supported in push-down")
	}
	switch c := top.Count.(type) {
	case *ast.IntegerLiteral:
		return c.Value, nil
	default:
		return 0, fmt.Errorf("TOP count must be an integer literal for push-down, got %T", top.Count)
	}
}

// isNumericValue returns true if the value is a numeric type.
func isNumericValue(v interface{}) bool {
	switch v.(type) {
	case int, int64, float64, float32:
		return true
	default:
		return false
	}
}

// isNumeric returns true if s consists entirely of digits (e.g. "1", "42").
// Used to distinguish store-derived numeric tenant IDs (which target the
// tenant_id INTEGER column) from string-based tenant IDs (which target
// json_extract(data, '$.tenant_id')).
func isNumeric(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// generateMutationSQL builds a push-down query for UPDATE/DELETE mutations.
// It produces a SELECT that returns the matching records (with their data)
// so the executor can then apply SET clauses or delete them.
func generateMutationSQL(where ast.Expression, entity, tenantID string, dialect SQLDialect) (*GeneratedSQL, error) {
	gen := NewSQLGenerator(dialect)

	baseSql, baseArg := dialect.BaseQuery(entity)
	gen.args = append(gen.args, baseArg)

	var clauses []string
	clauses = append(clauses, baseSql)

	if tenantID != "" {
		if isNumeric(tenantID) {
			clauses = append(clauses, fmt.Sprintf("AND tenant_id = %s",
				gen.addArg(tenantID)))
		} else {
			clauses = append(clauses, fmt.Sprintf("AND %s = %s",
				dialect.JSONField("tenant_id"), gen.addArg(tenantID)))
		}
	}

	if where != nil {
		whereSQL, err := gen.translateExpr(where)
		if err != nil {
			return nil, fmt.Errorf("mutation WHERE translation: %w", err)
		}
		clauses = append(clauses, "AND ("+whereSQL+")")
	}

	return &GeneratedSQL{SQL: strings.Join(clauses, " "), Args: gen.args}, nil
}
