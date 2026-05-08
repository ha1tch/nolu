// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"testing"

	"github.com/ha1tch/tsqlparser"
	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/storage"
)

// ---------------------------------------------------------------------------
// Mock storage backends for planner testing
// ---------------------------------------------------------------------------

// mockQueryableStore implements both storage.Store and storage.Queryable.
type mockQueryableStore struct {
	storage.Store // embedded nil — we only need Queryable methods
	caps          storage.QueryCapabilities
	counts        map[string]int
	queryResults  []map[string]interface{}
	queryErr      error
}

func (m *mockQueryableStore) Capabilities() storage.QueryCapabilities {
	return m.caps
}

func (m *mockQueryableStore) CountEntities(_ context.Context, entity string) (int, error) {
	if c, ok := m.counts[entity]; ok {
		return c, nil
	}
	return 0, nil
}

func (m *mockQueryableStore) QueryWithPlan(_ context.Context, _ string, _ []interface{}) ([]map[string]interface{}, error) {
	return m.queryResults, m.queryErr
}

// Minimal Store interface methods (the planner only needs Queryable, but
// Go requires the full interface to be satisfied for type assertions).
func (m *mockQueryableStore) Create(_ context.Context, _ string, _ map[string]interface{}) (int, error) {
	return 0, nil
}
func (m *mockQueryableStore) Get(_ context.Context, _ string, _ int) (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockQueryableStore) Update(_ context.Context, _ string, _ int, _ map[string]interface{}) error {
	return nil
}
func (m *mockQueryableStore) Patch(_ context.Context, _ string, _ int, _ map[string]interface{}) error {
	return nil
}
func (m *mockQueryableStore) Delete(_ context.Context, _ string, _ int) error { return nil }
func (m *mockQueryableStore) Save(_ context.Context, _ string, _ int, _ map[string]interface{}) (bool, error) { return false, nil }
func (m *mockQueryableStore) List(_ context.Context, _ string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockQueryableStore) Exists(_ context.Context, _ string, _ int) bool { return false }
func (m *mockQueryableStore) Search(_ context.Context, _ string, _ string, _ string, _ string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockQueryableStore) FullTextSearch(_ context.Context, _ string, _ string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockQueryableStore) Close() error { return nil }

// Compile-time checks
var _ storage.Store = (*mockQueryableStore)(nil)
var _ storage.Queryable = (*mockQueryableStore)(nil)

// mockPlainStore implements only storage.Store, not Queryable.
type mockPlainStore struct {
	storage.Store
}

func (m *mockPlainStore) Create(_ context.Context, _ string, _ map[string]interface{}) (int, error) {
	return 0, nil
}
func (m *mockPlainStore) Get(_ context.Context, _ string, _ int) (map[string]interface{}, error) {
	return nil, nil
}
func (m *mockPlainStore) Update(_ context.Context, _ string, _ int, _ map[string]interface{}) error {
	return nil
}
func (m *mockPlainStore) Patch(_ context.Context, _ string, _ int, _ map[string]interface{}) error {
	return nil
}
func (m *mockPlainStore) Delete(_ context.Context, _ string, _ int) error { return nil }
func (m *mockPlainStore) Save(_ context.Context, _ string, _ int, _ map[string]interface{}) (bool, error) { return false, nil }
func (m *mockPlainStore) List(_ context.Context, _ string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockPlainStore) Exists(_ context.Context, _ string, _ int) bool { return false }
func (m *mockPlainStore) Search(_ context.Context, _ string, _ string, _ string, _ string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockPlainStore) FullTextSearch(_ context.Context, _ string, _ string) ([]map[string]interface{}, error) {
	return nil, nil
}
func (m *mockPlainStore) Close() error { return nil }

var _ storage.Store = (*mockPlainStore)(nil)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func parseSelect(t *testing.T, sql string) *ast.SelectStatement {
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

func fullCaps() storage.QueryCapabilities {
	return storage.QueryCapabilities{
		Where: true, OrderBy: true, Limit: true, Count: true,
	}
}

func newQueryableStore(entity string, count int) *mockQueryableStore {
	return &mockQueryableStore{
		caps:   fullCaps(),
		counts: map[string]int{entity: count},
	}
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

func TestPlanner_NonQueryableStore(t *testing.T) {
	planner := NewPlanner()
	store := &mockPlainStore{}
	s := parseSelect(t, "SELECT * FROM widgets WHERE status = 'active'")

	plan := planner.Plan(context.Background(), s, store)

	if plan.hasPush() {
		t.Error("expected no push-down for non-Queryable store")
	}
	if plan.Reason == "" {
		t.Error("expected a reason")
	}
}

func TestPlanner_BelowThreshold(t *testing.T) {
	planner := NewPlanner()
	store := newQueryableStore("widgets", 100) // Below default 200
	s := parseSelect(t, "SELECT * FROM widgets WHERE status = 'active'")

	plan := planner.Plan(context.Background(), s, store)

	if plan.hasPush() {
		t.Error("expected no push-down below threshold")
	}
	if plan.EstimatedN != 100 {
		t.Errorf("expected EstimatedN=100, got %d", plan.EstimatedN)
	}
}

func TestPlanner_AtThreshold(t *testing.T) {
	planner := NewPlanner()
	store := newQueryableStore("widgets", 200) // Exactly at threshold
	s := parseSelect(t, "SELECT * FROM widgets WHERE status = 'active'")

	plan := planner.Plan(context.Background(), s, store)

	// count=200 is NOT less than threshold=200, so push-down should happen
	if !plan.pushed(PushWhere) {
		t.Error("expected push-down at exactly the threshold (count >= threshold)")
	}
}

func TestPlanner_AboveThreshold_PushableWhere(t *testing.T) {
	planner := NewPlanner()
	store := newQueryableStore("widgets", 500)
	s := parseSelect(t, "SELECT * FROM widgets WHERE status = 'active'")

	plan := planner.Plan(context.Background(), s, store)

	if !plan.hasPush() {
		t.Fatal("expected push-down above threshold with pushable WHERE")
	}
	if !plan.pushed(PushWhere) {
		t.Error("expected PushWhere")
	}
	if plan.EstimatedN != 500 {
		t.Errorf("expected EstimatedN=500, got %d", plan.EstimatedN)
	}
}

func TestPlanner_CustomThreshold(t *testing.T) {
	planner := NewPlannerWithThreshold(10)
	store := newQueryableStore("widgets", 15)
	s := parseSelect(t, "SELECT * FROM widgets WHERE status = 'active'")

	plan := planner.Plan(context.Background(), s, store)

	if !plan.pushed(PushWhere) {
		t.Error("expected push-down with custom threshold=10, count=15")
	}
}

func TestPlanner_NoWhereClause(t *testing.T) {
	planner := NewPlanner()
	store := newQueryableStore("widgets", 500)
	s := parseSelect(t, "SELECT * FROM widgets")

	plan := planner.Plan(context.Background(), s, store)

	if plan.hasPush() {
		t.Error("expected no push-down without WHERE clause")
	}
}

// ---------------------------------------------------------------------------
// WHERE pushability — table-driven
// ---------------------------------------------------------------------------

func TestPlanner_WherePushability(t *testing.T) {
	tests := []struct {
		name     string
		sql      string
		pushable bool
	}{
		// Pushable cases
		{"string_equals", "SELECT * FROM w WHERE status = 'active'", true},
		{"numeric_greater", "SELECT * FROM w WHERE value > 42", true},
		{"numeric_less_equal", "SELECT * FROM w WHERE value <= 100", true},
		{"not_equal", "SELECT * FROM w WHERE status != 'deleted'", true},
		{"like", "SELECT * FROM w WHERE name LIKE '%pump%'", true},
		{"is_null", "SELECT * FROM w WHERE deleted_at IS NULL", true},
		{"is_not_null", "SELECT * FROM w WHERE updated_at IS NOT NULL", true},
		{"between", "SELECT * FROM w WHERE value BETWEEN 10 AND 100", true},
		{"in_strings", "SELECT * FROM w WHERE status IN ('active', 'pending')", true},
		{"in_numbers", "SELECT * FROM w WHERE id IN (1, 2, 3)", true},
		{"and", "SELECT * FROM w WHERE status = 'active' AND value > 10", true},
		{"or", "SELECT * FROM w WHERE status = 'active' OR status = 'pending'", true},
		{"not", "SELECT * FROM w WHERE NOT (status = 'deleted')", true},
		{"compound_and_or", "SELECT * FROM w WHERE (status = 'active' OR status = 'pending') AND value > 0", true},
		{"boolean_true", "SELECT * FROM w WHERE active = TRUE", true},

		// NOT pushable cases
		{"function_in_predicate", "SELECT * FROM w WHERE UPPER(name) = 'FOO'", false},
		{"arithmetic_left", "SELECT * FROM w WHERE value + 10 > 100", false},
	}

	planner := NewPlannerWithThreshold(1) // Low threshold to test pushability, not cardinality
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newQueryableStore("w", 1000)
			s := parseSelect(t, tt.sql)
			plan := planner.Plan(ctx, s, store)

			if tt.pushable && !plan.pushed(PushWhere) {
				t.Errorf("expected WHERE to be pushable but got plan: %s", plan.Reason)
			}
			if !tt.pushable && plan.pushed(PushWhere) {
				t.Errorf("expected WHERE to NOT be pushable but got push-down")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// ORDER BY pushability
// ---------------------------------------------------------------------------

func TestPlanner_OrderByPushability(t *testing.T) {
	tests := []struct {
		name          string
		sql           string
		expectOrderBy bool
	}{
		{
			"simple_field_asc",
			"SELECT * FROM w WHERE status = 'active' ORDER BY name",
			true,
		},
		{
			"simple_field_desc",
			"SELECT * FROM w WHERE status = 'active' ORDER BY name DESC",
			true,
		},
		{
			"multiple_fields",
			"SELECT * FROM w WHERE status = 'active' ORDER BY category, name DESC",
			true,
		},
		{
			"function_in_orderby",
			"SELECT * FROM w WHERE status = 'active' ORDER BY UPPER(name)",
			false,
		},
		{
			"no_where_means_no_orderby_push",
			"SELECT * FROM w ORDER BY name",
			false, // ORDER BY only pushed if WHERE is also pushed
		},
	}

	planner := NewPlannerWithThreshold(1)
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newQueryableStore("w", 1000)
			s := parseSelect(t, tt.sql)
			plan := planner.Plan(ctx, s, store)

			if tt.expectOrderBy && !plan.pushed(PushOrderBy) {
				t.Errorf("expected ORDER BY push-down, got: %s", plan.Reason)
			}
			if !tt.expectOrderBy && plan.pushed(PushOrderBy) {
				t.Errorf("expected ORDER BY NOT pushed, but it was")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// LIMIT pushability
// ---------------------------------------------------------------------------

func TestPlanner_LimitPushability(t *testing.T) {
	tests := []struct {
		name        string
		sql         string
		expectLimit bool
	}{
		{
			"top_with_where",
			"SELECT TOP 10 * FROM w WHERE status = 'active'",
			true,
		},
		{
			"top_with_where_and_orderby",
			"SELECT TOP 5 * FROM w WHERE status = 'active' ORDER BY name",
			true,
		},
		{
			"top_without_where",
			"SELECT TOP 10 * FROM w",
			false, // No WHERE push = no LIMIT push
		},
	}

	planner := NewPlannerWithThreshold(1)
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newQueryableStore("w", 1000)
			s := parseSelect(t, tt.sql)
			plan := planner.Plan(ctx, s, store)

			if tt.expectLimit && !plan.pushed(PushLimit) {
				t.Errorf("expected LIMIT push-down, got: %s", plan.Reason)
			}
			if !tt.expectLimit && plan.pushed(PushLimit) {
				t.Errorf("expected LIMIT NOT pushed, but it was")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Combined push decisions
// ---------------------------------------------------------------------------

func TestPlanner_CombinedPush_WhereOrderByLimit(t *testing.T) {
	planner := NewPlannerWithThreshold(1)
	store := newQueryableStore("readings", 10000)
	s := parseSelect(t, "SELECT TOP 100 * FROM readings WHERE sensor_id = 'S001' ORDER BY timestamp DESC")

	plan := planner.Plan(context.Background(), s, store)

	if !plan.pushed(PushWhere) {
		t.Error("expected PushWhere")
	}
	if !plan.pushed(PushOrderBy) {
		t.Error("expected PushOrderBy")
	}
	if !plan.pushed(PushLimit) {
		t.Error("expected PushLimit")
	}
}

func TestPlanner_WhereOnly_UnpushableOrderBy(t *testing.T) {
	planner := NewPlannerWithThreshold(1)
	store := newQueryableStore("w", 1000)
	s := parseSelect(t, "SELECT TOP 10 * FROM w WHERE status = 'active' ORDER BY UPPER(name)")

	plan := planner.Plan(context.Background(), s, store)

	if !plan.pushed(PushWhere) {
		t.Error("expected PushWhere (WHERE is pushable)")
	}
	if plan.pushed(PushOrderBy) {
		t.Error("expected ORDER BY NOT pushed (function in expression)")
	}
	// LIMIT should still be pushed because WHERE is pushed
	if !plan.pushed(PushLimit) {
		t.Error("expected PushLimit (WHERE is pushed, so LIMIT benefits)")
	}
}

// ---------------------------------------------------------------------------
// Capability restrictions
// ---------------------------------------------------------------------------

func TestPlanner_CapabilityRestrictions(t *testing.T) {
	tests := []struct {
		name        string
		caps        storage.QueryCapabilities
		sql         string
		expectPush  bool
	}{
		{
			"where_disabled",
			storage.QueryCapabilities{Where: false, OrderBy: true, Limit: true, Count: true},
			"SELECT * FROM w WHERE status = 'active'",
			false,
		},
		{
			"orderby_disabled",
			storage.QueryCapabilities{Where: true, OrderBy: false, Limit: true, Count: true},
			"SELECT * FROM w WHERE status = 'active' ORDER BY name",
			true, // WHERE still pushes, just not ORDER BY
		},
	}

	planner := NewPlannerWithThreshold(1)
	ctx := context.Background()

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &mockQueryableStore{
				caps:   tt.caps,
				counts: map[string]int{"w": 1000},
			}
			s := parseSelect(t, tt.sql)
			plan := planner.Plan(ctx, s, store)

			if tt.expectPush && !plan.hasPush() {
				t.Errorf("expected push-down, got: %s", plan.Reason)
			}
			if !tt.expectPush && plan.hasPush() {
				t.Error("expected no push-down")
			}
		})
	}

	// Specific test: WHERE disabled means ORDER BY also can't push
	t.Run("orderby_disabled_no_orderby_push", func(t *testing.T) {
		store := &mockQueryableStore{
			caps:   storage.QueryCapabilities{Where: true, OrderBy: false, Limit: true, Count: true},
			counts: map[string]int{"w": 1000},
		}
		s := parseSelect(t, "SELECT * FROM w WHERE status = 'active' ORDER BY name")
		plan := planner.Plan(ctx, s, store)

		if plan.pushed(PushOrderBy) {
			t.Error("ORDER BY should not be pushed when capability is false")
		}
	})
}

// ---------------------------------------------------------------------------
// PlanMutation
// ---------------------------------------------------------------------------

func TestPlanner_PlanMutation(t *testing.T) {
	planner := NewPlannerWithThreshold(1)
	ctx := context.Background()

	t.Run("pushable_where", func(t *testing.T) {
		store := newQueryableStore("widgets", 500)
		// Parse a DELETE to get its WHERE clause
		prog, errs := tsqlparser.Parse("DELETE FROM widgets WHERE status = 'deleted'")
		if len(errs) > 0 {
			t.Fatal(errs[0])
		}
		del := prog.Statements[0].(*ast.DeleteStatement)
		plan := planner.PlanMutation(ctx, del.Where, "widgets", store)
		if !plan.pushed(PushWhere) {
			t.Error("expected mutation WHERE push-down")
		}
	})

	t.Run("non_queryable_store", func(t *testing.T) {
		store := &mockPlainStore{}
		plan := planner.PlanMutation(ctx, nil, "widgets", store)
		if plan.hasPush() {
			t.Error("expected no push-down for non-Queryable store")
		}
	})

	t.Run("below_threshold", func(t *testing.T) {
		plannerDefault := NewPlanner() // threshold=200
		store := newQueryableStore("widgets", 50)
		prog, _ := tsqlparser.Parse("DELETE FROM widgets WHERE status = 'deleted'")
		del := prog.Statements[0].(*ast.DeleteStatement)
		plan := plannerDefault.PlanMutation(ctx, del.Where, "widgets", store)
		if plan.hasPush() {
			t.Error("expected no push-down below threshold")
		}
	})

	t.Run("nil_where", func(t *testing.T) {
		store := newQueryableStore("widgets", 500)
		plan := planner.PlanMutation(ctx, nil, "widgets", store)
		if plan.hasPush() {
			t.Error("expected no push-down with nil WHERE")
		}
	})
}

// ---------------------------------------------------------------------------
// QueryPlan helpers
// ---------------------------------------------------------------------------

func TestQueryPlan_hasPush(t *testing.T) {
	tests := []struct {
		push     []PushDecision
		expected bool
	}{
		{[]PushDecision{PushNone}, false},
		{[]PushDecision{PushWhere}, true},
		{[]PushDecision{PushWhere, PushOrderBy}, true},
		{nil, false},
	}

	for i, tt := range tests {
		t.Run(fmt.Sprintf("case_%d", i), func(t *testing.T) {
			plan := QueryPlan{Push: tt.push}
			if plan.hasPush() != tt.expected {
				t.Errorf("hasPush()=%v, expected %v", plan.hasPush(), tt.expected)
			}
		})
	}
}

func TestQueryPlan_pushed(t *testing.T) {
	plan := QueryPlan{Push: []PushDecision{PushWhere, PushLimit}}

	if !plan.pushed(PushWhere) {
		t.Error("should find PushWhere")
	}
	if !plan.pushed(PushLimit) {
		t.Error("should find PushLimit")
	}
	if plan.pushed(PushOrderBy) {
		t.Error("should not find PushOrderBy")
	}
	if plan.pushed(PushNone) {
		t.Error("should not find PushNone")
	}
}

func TestQueryPlan_pushNames(t *testing.T) {
	tests := []struct {
		push     []PushDecision
		expected string
	}{
		{[]PushDecision{PushNone}, "none"},
		{[]PushDecision{PushWhere}, "WHERE"},
		{[]PushDecision{PushWhere, PushOrderBy, PushLimit}, "WHERE,ORDER_BY,LIMIT"},
	}

	for _, tt := range tests {
		plan := QueryPlan{Push: tt.push}
		got := plan.pushNames()
		if got != tt.expected {
			t.Errorf("pushNames()=%q, expected %q", got, tt.expected)
		}
	}
}
