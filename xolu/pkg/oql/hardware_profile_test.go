// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"testing"

	"github.com/ha1tch/tsqlparser/ast"
)

// ---------------------------------------------------------------------------
// ProfileByName
// ---------------------------------------------------------------------------

func TestProfileByName(t *testing.T) {
	tests := []struct {
		name     string
		expected string
	}{
		{"edge", "edge"},
		{"Edge", "edge"},
		{"EDGE", "edge"},
		{"vps", "vps"},
		{"dedicated", "dedicated"},
		{"  vps  ", "vps"},
	}
	for _, tt := range tests {
		p := ProfileByName(tt.name)
		if p == nil {
			t.Errorf("ProfileByName(%q) = nil, want %q", tt.name, tt.expected)
			continue
		}
		if p.Name != tt.expected {
			t.Errorf("ProfileByName(%q).Name = %q, want %q", tt.name, p.Name, tt.expected)
		}
	}

	if p := ProfileByName("unknown"); p != nil {
		t.Errorf("ProfileByName(\"unknown\") = %v, want nil", p)
	}
	if p := ProfileByName(""); p != nil {
		t.Errorf("ProfileByName(\"\") = %v, want nil", p)
	}
}

func TestDefaultProfile(t *testing.T) {
	p := DefaultProfile()
	if p.Name != "vps" {
		t.Errorf("DefaultProfile().Name = %q, want \"vps\"", p.Name)
	}
}

// ---------------------------------------------------------------------------
// AST helpers (prefixed hp to avoid collisions with other test files)
// ---------------------------------------------------------------------------

func hpCol(expr ast.Expression) ast.SelectColumn {
	return ast.SelectColumn{Expression: expr}
}

func hpCountStar() *ast.FunctionCall {
	return &ast.FunctionCall{
		Function:  &ast.Identifier{Value: "COUNT"},
		Arguments: []ast.Expression{&ast.Identifier{Value: "*"}},
	}
}

func hpFunc(name string, args ...ast.Expression) *ast.FunctionCall {
	return &ast.FunctionCall{
		Function:  &ast.Identifier{Value: name},
		Arguments: args,
	}
}

func hpId(name string) *ast.Identifier {
	return &ast.Identifier{Value: name}
}

func hpOB(name string) *ast.OrderByItem {
	return &ast.OrderByItem{Expression: hpId(name)}
}

// ---------------------------------------------------------------------------
// EstimateComplexity
// ---------------------------------------------------------------------------

func TestEstimateComplexity_Simple(t *testing.T) {
	// SELECT region, COUNT(*) FROM items GROUP BY region
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{hpCol(hpId("region")), hpCol(hpCountStar())},
		GroupBy: []ast.Expression{hpId("region")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 0 {
		t.Errorf("TempBTrees = %d, want 0 (single-key GROUP BY)", qc.TempBTrees)
	}
	if qc.NonCovering {
		t.Errorf("NonCovering = true, want false (COUNT only)")
	}
	if !qc.IsSimple() {
		t.Error("IsSimple() = false, want true")
	}
}

func TestEstimateComplexity_NonCoveringAggregate(t *testing.T) {
	// SELECT category, SUM(quantity) FROM items GROUP BY category
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{
			hpCol(hpId("category")),
			hpCol(hpFunc("SUM", hpId("quantity"))),
		},
		GroupBy: []ast.Expression{hpId("category")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 0 {
		t.Errorf("TempBTrees = %d, want 0", qc.TempBTrees)
	}
	if !qc.NonCovering {
		t.Error("NonCovering = false, want true (SUM on non-GROUP-BY column)")
	}
	if qc.IsSimple() {
		t.Error("IsSimple() = true, want false")
	}
}

func TestEstimateComplexity_CoveringAggregate(t *testing.T) {
	// SUM on a GROUP BY column is covering
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{
			hpCol(hpId("region")),
			hpCol(hpFunc("SUM", hpId("region"))),
		},
		GroupBy: []ast.Expression{hpId("region")},
	}

	qc := EstimateComplexity(stmt)
	if qc.NonCovering {
		t.Error("NonCovering = true, want false (SUM on GROUP BY column)")
	}
}

func TestEstimateComplexity_MultiKeyGroupBy(t *testing.T) {
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{
			hpCol(hpId("region")), hpCol(hpId("category")), hpCol(hpCountStar()),
		},
		GroupBy: []ast.Expression{hpId("region"), hpId("category")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 1 {
		t.Errorf("TempBTrees = %d, want 1 (multi-key GROUP BY)", qc.TempBTrees)
	}
}

func TestEstimateComplexity_MisalignedOrderBy(t *testing.T) {
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{hpCol(hpId("region")), hpCol(hpCountStar())},
		GroupBy: []ast.Expression{hpId("region")},
		OrderBy: []*ast.OrderByItem{hpOB("cnt")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 1 {
		t.Errorf("TempBTrees = %d, want 1 (ORDER BY misaligned)", qc.TempBTrees)
	}
}

func TestEstimateComplexity_AlignedOrderBy(t *testing.T) {
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{hpCol(hpId("region")), hpCol(hpCountStar())},
		GroupBy: []ast.Expression{hpId("region")},
		OrderBy: []*ast.OrderByItem{hpOB("region")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 0 {
		t.Errorf("TempBTrees = %d, want 0 (ORDER BY aligned)", qc.TempBTrees)
	}
}

func TestEstimateComplexity_FullComplex(t *testing.T) {
	// 2 temp B-trees + non-covering
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{
			hpCol(hpId("region")), hpCol(hpId("category")),
			hpCol(hpCountStar()), hpCol(hpFunc("SUM", hpId("quantity"))),
		},
		GroupBy: []ast.Expression{hpId("region"), hpId("category")},
		OrderBy: []*ast.OrderByItem{hpOB("category")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 2 {
		t.Errorf("TempBTrees = %d, want 2", qc.TempBTrees)
	}
	if !qc.NonCovering {
		t.Error("NonCovering = false, want true")
	}
}

func TestEstimateComplexity_OrderByPrefixOfGroupBy(t *testing.T) {
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{
			hpCol(hpId("region")), hpCol(hpId("category")), hpCol(hpCountStar()),
		},
		GroupBy: []ast.Expression{hpId("region"), hpId("category")},
		OrderBy: []*ast.OrderByItem{hpOB("region")},
	}

	qc := EstimateComplexity(stmt)
	if qc.TempBTrees != 1 {
		t.Errorf("TempBTrees = %d, want 1 (multi-key GB, but ORDER BY aligned)", qc.TempBTrees)
	}
}

func TestEstimateComplexity_NoGroupBy(t *testing.T) {
	stmt := &ast.SelectStatement{
		Columns: []ast.SelectColumn{hpCol(hpId("region")), hpCol(hpId("amount"))},
		OrderBy: []*ast.OrderByItem{hpOB("amount")},
	}

	qc := EstimateComplexity(stmt)
	if !qc.IsSimple() {
		t.Error("IsSimple() = false, want true (no GROUP BY)")
	}
}

// ---------------------------------------------------------------------------
// QueryComplexity.Threshold
// ---------------------------------------------------------------------------

func TestQueryComplexity_Threshold(t *testing.T) {
	vps := DefaultProfile()

	tests := []struct {
		label    string
		qc       QueryComplexity
		expected int
	}{
		{"simple", QueryComplexity{0, false}, 0},
		{"nonCovering", QueryComplexity{0, true}, vps.NonCoveringThreshold},
		{"1 tempBTree", QueryComplexity{1, false}, vps.TempBTree1Threshold},
		{"2 tempBTrees", QueryComplexity{2, false}, vps.TempBTree2Threshold},
	}

	for _, tt := range tests {
		t.Run(tt.label, func(t *testing.T) {
			got := tt.qc.Threshold(&vps)
			if got != tt.expected {
				t.Errorf("Threshold() = %d, want %d", got, tt.expected)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// deriveProfile sanity checks
// ---------------------------------------------------------------------------

func TestDeriveProfile_GoFasterThanSQL(t *testing.T) {
	profile := deriveProfile(1000, 2000, 3000)
	if profile.Name != "calibrated" {
		t.Errorf("Name = %q, want \"calibrated\"", profile.Name)
	}
	if profile.BlobPushThreshold < 100 {
		t.Errorf("BlobPushThreshold = %d, want >= 100", profile.BlobPushThreshold)
	}
}

func TestDeriveProfile_TypicalVPS(t *testing.T) {
	profile := deriveProfile(4300, 2000, 3500)
	if profile.BlobPushThreshold < 50 || profile.BlobPushThreshold > 200 {
		t.Errorf("BlobPushThreshold = %d, want 50-200", profile.BlobPushThreshold)
	}
	if profile.TempBTree1Threshold <= profile.BlobPushThreshold {
		t.Errorf("TempBTree1 (%d) <= Blob (%d)",
			profile.TempBTree1Threshold, profile.BlobPushThreshold)
	}
	if profile.TempBTree2Threshold <= profile.TempBTree1Threshold {
		t.Errorf("TempBTree2 (%d) <= TempBTree1 (%d)",
			profile.TempBTree2Threshold, profile.TempBTree1Threshold)
	}
}

func TestDeriveProfile_EdgeHardware(t *testing.T) {
	profile := deriveProfile(12000, 3000, 5000)
	if profile.BlobPushThreshold > 50 {
		t.Errorf("BlobPushThreshold = %d, want <= 50 for edge", profile.BlobPushThreshold)
	}
}
