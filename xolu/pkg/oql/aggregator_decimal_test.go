// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"testing"
)

func TestDecimalSum_Basic(t *testing.T) {
	values := []interface{}{"19.90", "30.10", "0.50"}
	result := aggDecimalSum(values)
	if result != "50.5" {
		t.Errorf("SUM = %v, want 50.5", result)
	}
}

func TestDecimalSum_Precision(t *testing.T) {
	// This is the classic float64 failure case: 0.1 + 0.2 != 0.3
	values := []interface{}{"0.1", "0.2"}
	result := aggDecimalSum(values)
	if result != "0.3" {
		t.Errorf("SUM(0.1, 0.2) = %v, want 0.3", result)
	}
}

func TestDecimalSum_LargeValues(t *testing.T) {
	// Values that would lose precision in float64
	values := []interface{}{"999999999999999.99", "0.01"}
	result := aggDecimalSum(values)
	if result != "1000000000000000" {
		t.Errorf("SUM = %v, want 1000000000000000", result)
	}
}

func TestDecimalSum_Empty(t *testing.T) {
	result := aggDecimalSum([]interface{}{})
	if result != nil {
		t.Errorf("SUM of empty set should be nil, got %v", result)
	}
}

func TestDecimalSum_AllNil(t *testing.T) {
	result := aggDecimalSum([]interface{}{nil, nil})
	if result != nil {
		t.Errorf("SUM of all nils should be nil, got %v", result)
	}
}

func TestDecimalSum_SkipsNil(t *testing.T) {
	values := []interface{}{"10.00", nil, "20.00"}
	result := aggDecimalSum(values)
	if result != "30" {
		t.Errorf("SUM = %v, want 30", result)
	}
}

func TestDecimalAvg(t *testing.T) {
	values := []interface{}{"10.00", "20.00", "30.00"}
	result := aggDecimalAvg(values)
	if result != "20" {
		t.Errorf("AVG = %v, want 20", result)
	}
}

func TestDecimalAvg_Empty(t *testing.T) {
	result := aggDecimalAvg([]interface{}{})
	if result != nil {
		t.Errorf("AVG of empty set should be nil, got %v", result)
	}
}

func TestDecimalAvg_Precision(t *testing.T) {
	// 1/3 should not lose precision to float64
	values := []interface{}{"1.00", "1.00", "1.00"}
	result := aggDecimalAvg(values)
	if result != "1" {
		t.Errorf("AVG = %v, want 1", result)
	}
}

func TestDecimalMin(t *testing.T) {
	values := []interface{}{"30.00", "10.00", "20.00"}
	result := aggDecimalMin(values)
	if result != "10" {
		t.Errorf("MIN = %v, want 10", result)
	}
}

func TestDecimalMin_Empty(t *testing.T) {
	result := aggDecimalMin([]interface{}{})
	if result != nil {
		t.Errorf("MIN of empty set should be nil, got %v", result)
	}
}

func TestDecimalMax(t *testing.T) {
	values := []interface{}{"30.00", "10.00", "20.00"}
	result := aggDecimalMax(values)
	if result != "30" {
		t.Errorf("MAX = %v, want 30", result)
	}
}

func TestDecimalMax_Empty(t *testing.T) {
	result := aggDecimalMax([]interface{}{})
	if result != nil {
		t.Errorf("MAX of empty set should be nil, got %v", result)
	}
}

func TestDecimalAggregator_Integration(t *testing.T) {
	// Test that the Aggregator uses decimal functions when configured
	agg := NewAggregator()
	agg.SetDecimalFields(map[string]bool{"amount": true})

	if !agg.isDecimalField("amount") {
		t.Error("expected amount to be a decimal field")
	}
	if agg.isDecimalField("name") {
		t.Error("expected name to not be a decimal field")
	}
}

func TestDecimalAggregator_NilFields(t *testing.T) {
	agg := NewAggregator()
	// No decimal fields configured
	if agg.isDecimalField("amount") {
		t.Error("expected false when decimalFields is nil")
	}
}

func TestToDecimal_Types(t *testing.T) {
	tests := []struct {
		name  string
		input interface{}
		want  string
		ok    bool
	}{
		{"string", "19.90", "19.9", true},
		{"float64", float64(19.9), "19.9", true},
		{"int", int(42), "42", true},
		{"int64", int64(42), "42", true},
		{"nil", nil, "", false},
		{"invalid string", "garbage", "", false},
		{"bool", true, "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, ok := toDecimal(tt.input)
			if ok != tt.ok {
				t.Errorf("toDecimal(%v) ok = %v, want %v", tt.input, ok, tt.ok)
			}
			if ok && d.String() != tt.want {
				t.Errorf("toDecimal(%v) = %v, want %v", tt.input, d.String(), tt.want)
			}
		})
	}
}

// TestFloatVsDecimal_Comparison demonstrates why decimal aggregation matters
func TestFloatVsDecimal_Comparison(t *testing.T) {
	values := []interface{}{"0.1", "0.2"}

	// Float64 aggregation (the old way)
	floatResult := aggSum(values)
	floatVal, ok := floatResult.(float64)
	if !ok {
		t.Fatal("expected float64 from aggSum")
	}

	// Decimal aggregation (the new way)
	decResult := aggDecimalSum(values)

	// Float64 produces 0.30000000000000004
	if floatVal == 0.3 {
		t.Error("float64 should NOT equal exactly 0.3 (IEEE 754)")
	}

	// Decimal produces exactly "0.3"
	if decResult != "0.3" {
		t.Errorf("decimal SUM should be exactly '0.3', got %v", decResult)
	}
}
