// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"fmt"
	"testing"
)

// ---------------------------------------------------------------------------
// NormaliseDecimal tests — scaled integer storage
// ---------------------------------------------------------------------------

func TestNormaliseDecimal_Positive(t *testing.T) {
	d := &SQLiteStorageDialect{}

	tests := []struct {
		name      string
		value     string
		precision int
		scale     int
		want      string
	}{
		{"simple", "19.9", 6, 2, "1990"},
		{"with trailing zero", "19.90", 6, 2, "1990"},
		{"full precision", "9999.99", 6, 2, "999999"},
		{"zero", "0", 6, 2, "0"},
		{"small value", "0.5", 6, 2, "50"},
		{"no fractional", "42", 6, 2, "4200"},
		{"large precision", "123456789012.1234", 18, 4, "1234567890121234"},
		{"scale zero", "42", 6, 0, "42"},
		{"one cent", "0.01", 6, 2, "1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.NormaliseDecimal(tt.value, tt.precision, tt.scale)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("NormaliseDecimal(%q, %d, %d) = %q, want %q",
					tt.value, tt.precision, tt.scale, got, tt.want)
			}
		})
	}
}

func TestNormaliseDecimal_Negative(t *testing.T) {
	d := &SQLiteStorageDialect{}

	tests := []struct {
		name      string
		value     string
		precision int
		scale     int
		want      string
	}{
		{"simple negative", "-19.90", 6, 2, "-1990"},
		{"small negative", "-0.01", 6, 2, "-1"},
		{"max negative", "-9999.99", 6, 2, "-999999"},
		{"negative no frac", "-42", 6, 0, "-42"},
		{"negative one", "-1.00", 6, 2, "-100"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := d.NormaliseDecimal(tt.value, tt.precision, tt.scale)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Errorf("NormaliseDecimal(%q, %d, %d) = %q, want %q",
					tt.value, tt.precision, tt.scale, got, tt.want)
			}
		})
	}
}

func TestNormaliseDecimal_NegativeZero(t *testing.T) {
	d := &SQLiteStorageDialect{}
	got, err := d.NormaliseDecimal("-0", 6, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "0" {
		t.Errorf("NormaliseDecimal('-0') = %q, want %q", got, "0")
	}
}

func TestNormaliseDecimal_IntegerSort(t *testing.T) {
	d := &SQLiteStorageDialect{}

	// When parsed as integers, the scaled values sort numerically correct
	ascending := []string{"-9999.99", "-100.00", "-19.90", "-0.01", "0", "0.01", "19.90", "100.00", "9999.99"}
	var prev int64
	first := true
	for _, v := range ascending {
		got, err := d.NormaliseDecimal(v, 6, 2)
		if err != nil {
			t.Fatalf("unexpected error for %q: %v", v, err)
		}
		var n int64
		_, _ = fmt.Sscanf(got, "%d", &n)
		if !first && n <= prev {
			t.Errorf("ordering broken: %d (%s) should be > %d", n, v, prev)
		}
		prev = n
		first = false
	}
}

func TestNormaliseDecimal_Errors(t *testing.T) {
	d := &SQLiteStorageDialect{}

	tests := []struct {
		name      string
		value     string
		precision int
		scale     int
	}{
		{"invalid string", "not-a-number", 6, 2},
		{"empty string", "", 6, 2},
		{"exceeds precision positive", "10000.00", 6, 2},
		{"exceeds precision negative", "-10000.00", 6, 2},
		{"exceeds precision large", "9999999999999999999.00", 18, 2},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := d.NormaliseDecimal(tt.value, tt.precision, tt.scale)
			if err == nil {
				t.Errorf("expected error for NormaliseDecimal(%q, %d, %d)", tt.value, tt.precision, tt.scale)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// DenormaliseDecimal tests
// ---------------------------------------------------------------------------

func TestDenormaliseDecimal_Positive(t *testing.T) {
	d := &SQLiteStorageDialect{}

	tests := []struct {
		name  string
		value string
		scale int
		want  string
	}{
		{"simple", "1990", 2, "19.90"},
		{"zero", "0", 2, "0.00"},
		{"one cent", "1", 2, "0.01"},
		{"large", "999999", 2, "9999.99"},
		{"scale zero", "42", 0, "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.DenormaliseDecimal(tt.value, 6, tt.scale)
			if got != tt.want {
				t.Errorf("DenormaliseDecimal(%q, scale=%d) = %q, want %q", tt.value, tt.scale, got, tt.want)
			}
		})
	}
}

func TestDenormaliseDecimal_Negative(t *testing.T) {
	d := &SQLiteStorageDialect{}

	tests := []struct {
		name  string
		value string
		scale int
		want  string
	}{
		{"simple negative", "-1990", 2, "-19.90"},
		{"small negative", "-1", 2, "-0.01"},
		{"max negative", "-999999", 2, "-9999.99"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := d.DenormaliseDecimal(tt.value, 6, tt.scale)
			if got != tt.want {
				t.Errorf("DenormaliseDecimal(%q, scale=%d) = %q, want %q", tt.value, tt.scale, got, tt.want)
			}
		})
	}
}

func TestDenormaliseDecimal_Empty(t *testing.T) {
	d := &SQLiteStorageDialect{}
	got := d.DenormaliseDecimal("", 6, 2)
	if got != "" {
		t.Errorf("DenormaliseDecimal('') = %q, want ''", got)
	}
}

func TestDenormaliseDecimal_RoundTrip(t *testing.T) {
	d := &SQLiteStorageDialect{}

	inputs := []struct {
		raw       string
		clean     string
		precision int
		scale     int
	}{
		{"19.9", "19.90", 6, 2},
		{"0.5", "0.50", 6, 2},
		{"100", "100.00", 6, 2},
		{"42.123", "42.1230", 10, 4},
		{"-19.9", "-19.90", 6, 2},
		{"-0.01", "-0.01", 6, 2},
		{"-9999.99", "-9999.99", 6, 2},
		{"0", "0.00", 6, 2},
		{"-0", "0.00", 6, 2},
	}

	for _, tt := range inputs {
		normalised, err := d.NormaliseDecimal(tt.raw, tt.precision, tt.scale)
		if err != nil {
			t.Fatalf("normalise(%q): %v", tt.raw, err)
		}
		denormalised := d.DenormaliseDecimal(normalised, tt.precision, tt.scale)
		if denormalised != tt.clean {
			t.Errorf("round-trip %q -> %q -> %q, want %q",
				tt.raw, normalised, denormalised, tt.clean)
		}
	}
}

// ---------------------------------------------------------------------------
// NormaliseDecimalColumns / DenormaliseDecimalColumns tests
// ---------------------------------------------------------------------------

func TestNormaliseDecimalColumns(t *testing.T) {
	dialect := &SQLiteStorageDialect{}
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", Format: ""},
			{Name: "amount", JSONField: "amount", Type: "string", Format: "decimal", Precision: 8, Scale: 2},
			{Name: "count", JSONField: "count", Type: "integer", Format: ""},
		},
	}

	colVals := []interface{}{"Alice", "19.9", float64(42)}

	err := NormaliseDecimalColumns(spec, dialect, colVals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if colVals[0] != "Alice" {
		t.Errorf("name changed: got %v", colVals[0])
	}
	// Should be int64 scaled value
	if colVals[1] != int64(1990) {
		t.Errorf("amount = %v (%T), want int64(1990)", colVals[1], colVals[1])
	}
	if colVals[2] != float64(42) {
		t.Errorf("count changed: got %v", colVals[2])
	}
}

func TestNormaliseDecimalColumns_Negative(t *testing.T) {
	dialect := &SQLiteStorageDialect{}
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "balance", JSONField: "balance", Type: "string", Format: "decimal", Precision: 8, Scale: 2},
		},
	}

	colVals := []interface{}{"-42.50"}

	err := NormaliseDecimalColumns(spec, dialect, colVals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if colVals[0] != int64(-4250) {
		t.Errorf("balance = %v (%T), want int64(-4250)", colVals[0], colVals[0])
	}
}

func TestNormaliseDecimalColumns_NilSkipped(t *testing.T) {
	dialect := &SQLiteStorageDialect{}
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "amount", JSONField: "amount", Type: "string", Format: "decimal", Precision: 6, Scale: 2},
		},
	}

	colVals := []interface{}{nil}
	err := NormaliseDecimalColumns(spec, dialect, colVals)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if colVals[0] != nil {
		t.Errorf("nil should remain nil, got %v", colVals[0])
	}
}

func TestNormaliseDecimalColumns_InvalidValue(t *testing.T) {
	dialect := &SQLiteStorageDialect{}
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "amount", JSONField: "amount", Type: "string", Format: "decimal", Precision: 6, Scale: 2},
		},
	}

	colVals := []interface{}{"garbage"}
	err := NormaliseDecimalColumns(spec, dialect, colVals)
	if err == nil {
		t.Error("expected error for invalid decimal value")
	}
}

func TestDenormaliseDecimalColumns_Int64(t *testing.T) {
	dialect := &SQLiteStorageDialect{}
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "name", JSONField: "name", Type: "string", Format: ""},
			{Name: "amount", JSONField: "amount", Type: "string", Format: "decimal", Precision: 8, Scale: 2},
		},
	}

	// Simulate what SQLite returns: int64 for INTEGER columns
	colVals := []interface{}{"Alice", int64(1990)}

	DenormaliseDecimalColumns(spec, dialect, colVals)

	if colVals[0] != "Alice" {
		t.Errorf("name changed: got %v", colVals[0])
	}
	if colVals[1] != "19.90" {
		t.Errorf("amount = %q, want %q", colVals[1], "19.90")
	}
}

func TestDenormaliseDecimalColumns_Negative(t *testing.T) {
	dialect := &SQLiteStorageDialect{}
	spec := &AdaptedTableSpec{
		Columns: []ColumnDef{
			{Name: "balance", JSONField: "balance", Type: "string", Format: "decimal", Precision: 8, Scale: 2},
		},
	}

	colVals := []interface{}{int64(-4250)}
	DenormaliseDecimalColumns(spec, dialect, colVals)

	if colVals[0] != "-42.50" {
		t.Errorf("balance = %q, want %q", colVals[0], "-42.50")
	}
}

// ---------------------------------------------------------------------------
// SupportsNativeDecimalAggregation
// ---------------------------------------------------------------------------

func TestSQLite_SupportsNativeDecimalAggregation(t *testing.T) {
	d := &SQLiteStorageDialect{}
	if d.SupportsNativeDecimalAggregation() {
		t.Error("SQLite should return false for native decimal aggregation")
	}
}
