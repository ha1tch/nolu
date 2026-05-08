// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"strings"
	"testing"

	"github.com/ha1tch/tsqlparser"
	"github.com/ha1tch/tsqlparser/ast"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func parseSQLGen(t *testing.T, sql string) *ast.SelectStatement {
	t.Helper()
	prog, errs := tsqlparser.Parse(sql)
	if len(errs) > 0 {
		t.Fatalf("parse %q: %v", sql, errs[0])
	}
	s, ok := prog.Statements[0].(*ast.SelectStatement)
	if !ok {
		t.Fatalf("expected SelectStatement, got %T", prog.Statements[0])
	}
	return s
}

func allPush() QueryPlan {
	return QueryPlan{Push: []PushDecision{PushWhere, PushOrderBy, PushLimit}}
}

func wherePush() QueryPlan {
	return QueryPlan{Push: []PushDecision{PushWhere}}
}

func whereOrderPush() QueryPlan {
	return QueryPlan{Push: []PushDecision{PushWhere, PushOrderBy}}
}

func sqlite() SQLDialect {
	return &SQLiteDialect{}
}

// ---------------------------------------------------------------------------
// WHERE translation — table-driven
// ---------------------------------------------------------------------------

func TestSQLGen_Where(t *testing.T) {
	tests := []struct {
		name        string
		oql         string
		expectSQL   string // substring that must appear in generated SQL
		expectArgs  int    // expected number of args (including entity_type)
		expectError bool
	}{
		{
			name:       "string_equals",
			oql:        "SELECT * FROM widgets WHERE status = 'active'",
			expectSQL:  "json_extract(data, '$.status') = ?",
			expectArgs: 2, // entity_type + 'active'
		},
		{
			name:       "numeric_greater",
			oql:        "SELECT * FROM widgets WHERE value > 42",
			expectSQL:  "CAST(json_extract(data, '$.value') AS REAL) > ?",
			expectArgs: 2,
		},
		{
			name:       "numeric_less_equal",
			oql:        "SELECT * FROM widgets WHERE value <= 100",
			expectSQL:  "CAST(json_extract(data, '$.value') AS REAL) <= ?",
			expectArgs: 2,
		},
		{
			name:       "numeric_equals",
			oql:        "SELECT * FROM widgets WHERE count = 5",
			expectSQL:  "CAST(json_extract(data, '$.count') AS REAL) = ?",
			expectArgs: 2,
		},
		{
			name:       "not_equal_string",
			oql:        "SELECT * FROM widgets WHERE status != 'deleted'",
			expectSQL:  "json_extract(data, '$.status') != ?",
			expectArgs: 2,
		},
		{
			name:       "t_sql_not_equal",
			oql:        "SELECT * FROM widgets WHERE status <> 'deleted'",
			expectSQL:  "json_extract(data, '$.status') != ?", // <> normalised to !=
			expectArgs: 2,
		},
		{
			name:       "like",
			oql:        "SELECT * FROM widgets WHERE name LIKE '%pump%'",
			expectSQL:  "json_extract(data, '$.name') LIKE ?",
			expectArgs: 2,
		},
		{
			name:       "not_like",
			oql:        "SELECT * FROM widgets WHERE name NOT LIKE '%pump%'",
			expectSQL:  "NOT (json_extract(data, '$.name') LIKE ?)",
			expectArgs: 2,
		},
		{
			name:       "is_null",
			oql:        "SELECT * FROM widgets WHERE deleted_at IS NULL",
			expectSQL:  "json_extract(data, '$.deleted_at') IS NULL",
			expectArgs: 1, // only entity_type
		},
		{
			name:       "is_not_null",
			oql:        "SELECT * FROM widgets WHERE updated_at IS NOT NULL",
			expectSQL:  "json_extract(data, '$.updated_at') IS NOT NULL",
			expectArgs: 1,
		},
		{
			name:       "between",
			oql:        "SELECT * FROM widgets WHERE value BETWEEN 10 AND 100",
			expectSQL:  "CAST(json_extract(data, '$.value') AS REAL) BETWEEN ? AND ?",
			expectArgs: 3, // entity_type + 10 + 100
		},
		{
			name:       "not_between",
			oql:        "SELECT * FROM widgets WHERE value NOT BETWEEN 10 AND 100",
			expectSQL:  "NOT (CAST(json_extract(data, '$.value') AS REAL) BETWEEN ? AND ?)",
			expectArgs: 3,
		},
		{
			name:       "in_strings",
			oql:        "SELECT * FROM widgets WHERE status IN ('active', 'pending', 'review')",
			expectSQL:  "json_extract(data, '$.status') IN (?, ?, ?)",
			expectArgs: 4, // entity_type + 3 values
		},
		{
			name:       "in_numbers",
			oql:        "SELECT * FROM widgets WHERE id IN (1, 2, 3)",
			expectSQL:  "json_extract(data, '$.id') IN (?, ?, ?)",
			expectArgs: 4,
		},
		{
			name:       "not_in",
			oql:        "SELECT * FROM widgets WHERE status NOT IN ('deleted', 'archived')",
			expectSQL:  "NOT (json_extract(data, '$.status') IN (?, ?))",
			expectArgs: 3,
		},
		{
			name:       "and",
			oql:        "SELECT * FROM widgets WHERE status = 'active' AND value > 10",
			expectSQL:  ") AND (",
			expectArgs: 3,
		},
		{
			name:       "or",
			oql:        "SELECT * FROM widgets WHERE status = 'active' OR status = 'pending'",
			expectSQL:  ") OR (",
			expectArgs: 3,
		},
		{
			name:       "not_expr",
			oql:        "SELECT * FROM widgets WHERE NOT (status = 'deleted')",
			expectSQL:  "NOT (",
			expectArgs: 2,
		},
		{
			name:       "compound_and_or",
			oql:        "SELECT * FROM widgets WHERE (status = 'active' OR status = 'pending') AND value > 0",
			expectSQL:  ") AND (",
			expectArgs: 4,
		},
		{
			name:       "boolean_true",
			oql:        "SELECT * FROM widgets WHERE active = TRUE",
			expectSQL:  "json_extract(data, '$.active') = ?",
			expectArgs: 2,
		},
		{
			name:       "float_literal",
			oql:        "SELECT * FROM widgets WHERE temperature > 98.6",
			expectSQL:  "CAST(json_extract(data, '$.temperature') AS REAL) > ?",
			expectArgs: 2,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseSQLGen(t, tt.oql)
			gen, err := GenerateSQL(s, "widgets", "", wherePush(), sqlite())

			if tt.expectError {
				if err == nil {
					t.Error("expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}

			if !strings.Contains(gen.SQL, tt.expectSQL) {
				t.Errorf("SQL %q does not contain expected %q", gen.SQL, tt.expectSQL)
			}
			if len(gen.Args) != tt.expectArgs {
				t.Errorf("expected %d args, got %d: %v", tt.expectArgs, len(gen.Args), gen.Args)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Parameterisation safety
// ---------------------------------------------------------------------------

func TestSQLGen_NoLiteralsInSQL(t *testing.T) {
	// Verify that literal values never appear directly in the SQL string.
	// They must always be parameterised as ? with values in Args.
	tests := []struct {
		name     string
		oql      string
		literals []string // values that must NOT appear in the SQL
	}{
		{
			"string_value",
			"SELECT * FROM w WHERE name = 'dangerous'",
			[]string{"dangerous"},
		},
		{
			"numeric_value",
			"SELECT * FROM w WHERE value > 12345",
			[]string{"12345"},
		},
		{
			"in_values",
			"SELECT * FROM w WHERE status IN ('alpha', 'bravo')",
			[]string{"alpha", "bravo"},
		},
		{
			"between_values",
			"SELECT * FROM w WHERE val BETWEEN 100 AND 999",
			[]string{"100", "999"},
		},
		{
			"like_pattern",
			"SELECT * FROM w WHERE name LIKE '%secret%'",
			[]string{"secret"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseSQLGen(t, tt.oql)
			gen, err := GenerateSQL(s, "w", "", wherePush(), sqlite())
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}

			for _, lit := range tt.literals {
				if strings.Contains(gen.SQL, lit) {
					t.Errorf("SQL contains literal %q — should be parameterised.\nSQL: %s", lit, gen.SQL)
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Field name validation / injection
// ---------------------------------------------------------------------------

func TestSQLGen_FieldNameValidation(t *testing.T) {
	tests := []struct {
		name        string
		oql         string
		expectError bool
	}{
		{"normal_field", "SELECT * FROM w WHERE status = 'ok'", false},
		{"dotted_field", "SELECT * FROM w WHERE address.city = 'London'", false},
		{"underscore_field", "SELECT * FROM w WHERE last_name = 'Smith'", false},
	}
	// Note: we can't easily test injection via field names through the parser,
	// because the parser itself rejects most SQL injection attempts during
	// tokenisation. The validateFieldName function is our defence-in-depth.

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseSQLGen(t, tt.oql)
			_, err := GenerateSQL(s, "w", "", wherePush(), sqlite())
			if tt.expectError && err == nil {
				t.Error("expected error")
			}
			if !tt.expectError && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestSQLGen_ValidateFieldName_Direct(t *testing.T) {
	// Direct unit tests for the validation function itself
	tests := []struct {
		name  string
		field string
		valid bool
	}{
		{"simple", "status", true},
		{"dotted", "address.city", true},
		{"underscored", "last_name", true},
		{"numeric_suffix", "sensor1", true},
		{"leading_underscore", "_private", true},
		{"single_quote", "it's", false},
		{"double_quote", `col"x`, false},
		{"close_paren", "col)", false},
		{"sql_comment", "col--", false},
		{"semicolon", "col;DROP", false},
		{"block_comment", "col/*", false},
		{"space", "col name", false},
		{"leading_digit", "1col", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateFieldName(tt.field)
			if tt.valid && err != nil {
				t.Errorf("expected valid, got error: %v", err)
			}
			if !tt.valid && err == nil {
				t.Errorf("expected invalid for %q, got nil", tt.field)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tenant filtering
// ---------------------------------------------------------------------------

func TestSQLGen_TenantFilter(t *testing.T) {
	s := parseSQLGen(t, "SELECT * FROM widgets WHERE status = 'active'")

	t.Run("with_tenant", func(t *testing.T) {
		gen, err := GenerateSQL(s, "widgets", "tenant_abc", wherePush(), sqlite())
		if err != nil {
			t.Fatalf("GenerateSQL: %v", err)
		}
		// Non-numeric tenant ID → json_extract path
		if !strings.Contains(gen.SQL, "json_extract(data, '$.tenant_id') = ?") {
			t.Errorf("SQL missing tenant filter: %s", gen.SQL)
		}
		// Args: entity_type, tenant_id, status_value
		if len(gen.Args) != 3 {
			t.Errorf("expected 3 args, got %d: %v", len(gen.Args), gen.Args)
		}
		// Verify tenant_id is the second arg
		if gen.Args[1] != "tenant_abc" {
			t.Errorf("expected args[1]=tenant_abc, got %v", gen.Args[1])
		}
	})

	t.Run("with_numeric_tenant", func(t *testing.T) {
		gen, err := GenerateSQL(s, "widgets", "42", wherePush(), sqlite())
		if err != nil {
			t.Fatalf("GenerateSQL: %v", err)
		}
		// Numeric tenant ID → column path
		if !strings.Contains(gen.SQL, "AND tenant_id = ?") {
			t.Errorf("SQL missing column tenant filter: %s", gen.SQL)
		}
		if strings.Contains(gen.SQL, "json_extract(data, '$.tenant_id')") {
			t.Errorf("numeric tenant should use column, not json_extract for tenant_id: %s", gen.SQL)
		}
	})

	t.Run("without_tenant", func(t *testing.T) {
		gen, err := GenerateSQL(s, "widgets", "", wherePush(), sqlite())
		if err != nil {
			t.Fatalf("GenerateSQL: %v", err)
		}
		if strings.Contains(gen.SQL, "tenant_id") {
			t.Errorf("SQL should not contain tenant filter when tenantID is empty: %s", gen.SQL)
		}
		if len(gen.Args) != 2 {
			t.Errorf("expected 2 args, got %d", len(gen.Args))
		}
	})
}

// ---------------------------------------------------------------------------
// Type coercion
// ---------------------------------------------------------------------------

func TestSQLGen_TypeCoercion(t *testing.T) {
	tests := []struct {
		name       string
		oql        string
		expectCAST bool // should the SQL contain CAST(... AS REAL)?
	}{
		{"string_equals_no_cast", "SELECT * FROM w WHERE name = 'hello'", false},
		{"numeric_equals_cast", "SELECT * FROM w WHERE value = 42", true},
		{"numeric_greater_cast", "SELECT * FROM w WHERE value > 10", true},
		{"numeric_less_cast", "SELECT * FROM w WHERE value < 100", true},
		{"numeric_gte_cast", "SELECT * FROM w WHERE value >= 0", true},
		{"numeric_lte_cast", "SELECT * FROM w WHERE value <= 999", true},
		{"string_not_equals_no_cast", "SELECT * FROM w WHERE status != 'bad'", false},
		{"like_no_cast", "SELECT * FROM w WHERE name LIKE '%x%'", false},
		{"float_greater_cast", "SELECT * FROM w WHERE temp > 98.6", true},
		{"float_equals_cast", "SELECT * FROM w WHERE temp = 98.6", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseSQLGen(t, tt.oql)
			gen, err := GenerateSQL(s, "w", "", wherePush(), sqlite())
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}

			hasCAST := strings.Contains(gen.SQL, "CAST(")
			if tt.expectCAST && !hasCAST {
				t.Errorf("expected CAST in SQL, got: %s", gen.SQL)
			}
			if !tt.expectCAST && hasCAST {
				t.Errorf("did not expect CAST in SQL, got: %s", gen.SQL)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ORDER BY translation
// ---------------------------------------------------------------------------

func TestSQLGen_OrderBy(t *testing.T) {
	tests := []struct {
		name      string
		oql       string
		expectSQL string
	}{
		{
			"single_asc",
			"SELECT * FROM w WHERE status = 'active' ORDER BY name",
			"ORDER BY json_extract(data, '$.name') ASC",
		},
		{
			"single_desc",
			"SELECT * FROM w WHERE status = 'active' ORDER BY name DESC",
			"ORDER BY json_extract(data, '$.name') DESC",
		},
		{
			"multiple",
			"SELECT * FROM w WHERE status = 'active' ORDER BY category, name DESC",
			"ORDER BY json_extract(data, '$.category') ASC, json_extract(data, '$.name') DESC",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseSQLGen(t, tt.oql)
			gen, err := GenerateSQL(s, "w", "", whereOrderPush(), sqlite())
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}

			if !strings.Contains(gen.SQL, tt.expectSQL) {
				t.Errorf("SQL %q does not contain %q", gen.SQL, tt.expectSQL)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LIMIT (TOP) translation
// ---------------------------------------------------------------------------

func TestSQLGen_Limit(t *testing.T) {
	t.Run("top_to_limit", func(t *testing.T) {
		s := parseSQLGen(t, "SELECT TOP 25 * FROM w WHERE status = 'active' ORDER BY name")
		gen, err := GenerateSQL(s, "w", "", allPush(), sqlite())
		if err != nil {
			t.Fatalf("GenerateSQL: %v", err)
		}

		if !strings.Contains(gen.SQL, "LIMIT ?") {
			t.Errorf("SQL should contain LIMIT ?, got: %s", gen.SQL)
		}
		// Last arg should be the limit value
		lastArg := gen.Args[len(gen.Args)-1]
		if lastArg != int64(25) {
			t.Errorf("expected last arg=25, got %v (%T)", lastArg, lastArg)
		}
	})

	t.Run("top_percent_error", func(t *testing.T) {
		// TOP PERCENT isn't supported in push-down
		s := parseSQLGen(t, "SELECT TOP 50 PERCENT * FROM w WHERE status = 'active'")
		_, err := GenerateSQL(s, "w", "", allPush(), sqlite())
		if err == nil {
			t.Error("expected error for TOP PERCENT")
		}
	})
}

// ---------------------------------------------------------------------------
// Base query structure
// ---------------------------------------------------------------------------

func TestSQLGen_BaseQueryStructure(t *testing.T) {
	s := parseSQLGen(t, "SELECT * FROM sensors WHERE status = 'active'")
	gen, err := GenerateSQL(s, "sensors", "", wherePush(), sqlite())
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}

	// Must start with base SELECT
	if !strings.HasPrefix(gen.SQL, "SELECT data, _version FROM entities WHERE entity_type = ?") {
		t.Errorf("SQL should start with base query, got: %s", gen.SQL)
	}

	// First arg must be the entity name
	if gen.Args[0] != "sensors" {
		t.Errorf("first arg should be entity name, got %v", gen.Args[0])
	}
}

// ---------------------------------------------------------------------------
// Full SQL output verification (exact)
// ---------------------------------------------------------------------------

func TestSQLGen_FullOutput(t *testing.T) {
	tests := []struct {
		name     string
		oql      string
		entity   string
		tenant   string
		plan     QueryPlan
		wantSQL  string
		wantArgs []interface{}
	}{
		{
			name:     "simple_where",
			oql:      "SELECT * FROM assets WHERE status = 'active'",
			entity:   "assets",
			plan:     wherePush(),
			wantSQL:  "SELECT data, _version FROM entities WHERE entity_type = ? AND (json_extract(data, '$.status') = ?)",
			wantArgs: []interface{}{"assets", "active"},
		},
		{
			name:     "where_with_tenant",
			oql:      "SELECT * FROM assets WHERE status = 'active'",
			entity:   "assets",
			tenant:   "t1",
			plan:     wherePush(),
			wantSQL:  "SELECT data, _version FROM entities WHERE entity_type = ? AND json_extract(data, '$.tenant_id') = ? AND (json_extract(data, '$.status') = ?)",
			wantArgs: []interface{}{"assets", "t1", "active"},
		},
		{
			name:   "where_orderby_limit",
			oql:    "SELECT TOP 10 * FROM readings WHERE sensor_id = 'S1' ORDER BY timestamp DESC",
			entity: "readings",
			plan:   allPush(),
			wantSQL: "SELECT data, _version FROM entities WHERE entity_type = ? AND (json_extract(data, '$.sensor_id') = ?)" +
				" ORDER BY json_extract(data, '$.timestamp') DESC LIMIT ?",
			wantArgs: []interface{}{"readings", "S1", int64(10)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := parseSQLGen(t, tt.oql)
			gen, err := GenerateSQL(s, tt.entity, tt.tenant, tt.plan, sqlite())
			if err != nil {
				t.Fatalf("GenerateSQL: %v", err)
			}

			if gen.SQL != tt.wantSQL {
				t.Errorf("SQL mismatch:\n  got:  %s\n  want: %s", gen.SQL, tt.wantSQL)
			}

			if len(gen.Args) != len(tt.wantArgs) {
				t.Fatalf("args count: got %d, want %d\n  got:  %v\n  want: %v",
					len(gen.Args), len(tt.wantArgs), gen.Args, tt.wantArgs)
			}

			for i := range tt.wantArgs {
				if gen.Args[i] != tt.wantArgs[i] {
					t.Errorf("args[%d]: got %v (%T), want %v (%T)",
						i, gen.Args[i], gen.Args[i], tt.wantArgs[i], tt.wantArgs[i])
				}
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Dialect interface — verify SQLiteDialect satisfies it
// ---------------------------------------------------------------------------

func TestSQLiteDialect_Interface(t *testing.T) {
	var d SQLDialect = &SQLiteDialect{}
	if d.Name() != "sqlite" {
		t.Errorf("expected dialect name 'sqlite', got %q", d.Name())
	}

	if got := d.JSONField("status"); got != "json_extract(data, '$.status')" {
		t.Errorf("JSONField: %s", got)
	}
	if got := d.JSONFieldNumeric("value"); got != "CAST(json_extract(data, '$.value') AS REAL)" {
		t.Errorf("JSONFieldNumeric: %s", got)
	}
	if got := d.Placeholder(1); got != "?" {
		t.Errorf("Placeholder: %s", got)
	}
	if got := d.Placeholder(5); got != "?" {
		t.Errorf("Placeholder should always be ? for SQLite, got: %s", got)
	}
	if got := d.LimitClause("?"); got != "LIMIT ?" {
		t.Errorf("LimitClause: %s", got)
	}
}

// ---------------------------------------------------------------------------
// PushNone plan should produce bare query
// ---------------------------------------------------------------------------

func TestSQLGen_PushNone(t *testing.T) {
	s := parseSQLGen(t, "SELECT * FROM w WHERE status = 'active' ORDER BY name")
	plan := QueryPlan{Push: []PushDecision{PushNone}}
	gen, err := GenerateSQL(s, "w", "", plan, sqlite())
	if err != nil {
		t.Fatalf("GenerateSQL: %v", err)
	}

	// Should just be the base query with no WHERE/ORDER BY appended
	expected := "SELECT data, _version FROM entities WHERE entity_type = ?"
	if gen.SQL != expected {
		t.Errorf("PushNone SQL:\n  got:  %s\n  want: %s", gen.SQL, expected)
	}
	if len(gen.Args) != 1 {
		t.Errorf("expected 1 arg, got %d", len(gen.Args))
	}
}
