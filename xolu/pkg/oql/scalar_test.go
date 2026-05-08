// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Direct scalar function tests — these call the functions directly via
// the ScalarFunctions map to get coverage on scalar.go
// ---------------------------------------------------------------------------

func TestScalar_Upper(t *testing.T) {
	fn := ScalarFunctions["UPPER"]
	if fn([]interface{}{"hello"}) != "HELLO" {
		t.Error("UPPER failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("UPPER(nil) should return nil")
	}
	if fn([]interface{}{}) != nil {
		t.Error("UPPER() with no args should return nil")
	}
}

func TestScalar_Lower(t *testing.T) {
	fn := ScalarFunctions["LOWER"]
	if fn([]interface{}{"HELLO"}) != "hello" {
		t.Error("LOWER failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("LOWER(nil) should return nil")
	}
}

func TestScalar_Len(t *testing.T) {
	fn := ScalarFunctions["LEN"]
	if fn([]interface{}{"hello"}) != 5 {
		t.Errorf("LEN('hello') = %v", fn([]interface{}{"hello"}))
	}
	if fn([]interface{}{""}) != 0 {
		t.Error("LEN('') should be 0")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("LEN(nil) should return nil")
	}
}

func TestScalar_Trim(t *testing.T) {
	fn := ScalarFunctions["TRIM"]
	if fn([]interface{}{"  hello  "}) != "hello" {
		t.Error("TRIM failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("TRIM(nil) should return nil")
	}
}

func TestScalar_Coalesce(t *testing.T) {
	fn := ScalarFunctions["COALESCE"]
	if fn([]interface{}{nil, nil, "found"}) != "found" {
		t.Error("COALESCE should return first non-nil")
	}
	if fn([]interface{}{"first", "second"}) != "first" {
		t.Error("COALESCE should return first non-nil")
	}
	if fn([]interface{}{nil, nil, nil}) != nil {
		t.Error("COALESCE all nil should return nil")
	}
	if fn([]interface{}{}) != nil {
		t.Error("COALESCE empty should return nil")
	}
}

func TestScalar_Concat(t *testing.T) {
	fn := ScalarFunctions["CONCAT"]
	if fn([]interface{}{"hello", " ", "world"}) != "hello world" {
		t.Error("CONCAT failed")
	}
	if fn([]interface{}{"a", nil, "b"}) != "ab" {
		t.Error("CONCAT should skip nils")
	}
	if fn([]interface{}{}) != "" {
		t.Error("CONCAT empty should return empty string")
	}
}

func TestScalar_Cast(t *testing.T) {
	fn := ScalarFunctions["CAST"]
	if fn([]interface{}{42}) != "42" {
		t.Error("CAST(42) should return '42'")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("CAST(nil) should return nil")
	}
	if fn([]interface{}{}) != nil {
		t.Error("CAST() no args should return nil")
	}
}

func TestScalar_Abs(t *testing.T) {
	fn := ScalarFunctions["ABS"]
	if fn([]interface{}{float64(-5)}).(float64) != 5.0 {
		t.Error("ABS(-5) should be 5")
	}
	if fn([]interface{}{float64(5)}).(float64) != 5.0 {
		t.Error("ABS(5) should be 5")
	}
	if fn([]interface{}{float64(0)}).(float64) != 0.0 {
		t.Error("ABS(0) should be 0")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("ABS(nil) should return nil")
	}
}

func TestScalar_Round(t *testing.T) {
	fn := ScalarFunctions["ROUND"]
	// Round to 0 decimal places (default)
	r := fn([]interface{}{float64(3.7)}).(float64)
	if r != 4.0 {
		t.Errorf("ROUND(3.7) = %v, want 4", r)
	}
	// Round with precision
	r = fn([]interface{}{float64(3.456), float64(2)}).(float64)
	if r != 3.46 {
		t.Errorf("ROUND(3.456, 2) = %v, want 3.46", r)
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("ROUND(nil) should return nil")
	}
}

func TestScalar_Floor(t *testing.T) {
	fn := ScalarFunctions["FLOOR"]
	if fn([]interface{}{float64(3.7)}).(float64) != 3.0 {
		t.Error("FLOOR(3.7) should be 3")
	}
	if fn([]interface{}{float64(3.0)}).(float64) != 3.0 {
		t.Error("FLOOR(3.0) should be 3")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("FLOOR(nil) should return nil")
	}
}

func TestScalar_Ceiling(t *testing.T) {
	fn := ScalarFunctions["CEILING"]
	if fn([]interface{}{float64(3.2)}).(float64) != 4.0 {
		t.Error("CEILING(3.2) should be 4")
	}
	if fn([]interface{}{float64(3.0)}).(float64) != 3.0 {
		t.Error("CEILING(3.0) should be 3")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("CEILING(nil) should return nil")
	}
}

func TestScalar_GetDate(t *testing.T) {
	fn := ScalarFunctions["GETDATE"]
	result := fn([]interface{}{})
	s, ok := result.(string)
	if !ok {
		t.Fatalf("GETDATE should return string, got %T", result)
	}
	_, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Errorf("GETDATE returned unparseable time: %s", s)
	}
}

func TestScalar_GetUTCDate(t *testing.T) {
	fn := ScalarFunctions["GETUTCDATE"]
	result := fn([]interface{}{})
	s, ok := result.(string)
	if !ok {
		t.Fatalf("GETUTCDATE should return string, got %T", result)
	}
	if !strings.HasSuffix(s, "Z") && !strings.Contains(s, "+00:00") {
		t.Errorf("GETUTCDATE should be UTC: %s", s)
	}
}

func TestScalar_Year(t *testing.T) {
	fn := ScalarFunctions["YEAR"]
	if fn([]interface{}{"2024-03-15T10:30:00Z"}).(int) != 2024 {
		t.Error("YEAR failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("YEAR(nil) should return nil")
	}
	if fn([]interface{}{"not-a-date"}) != nil {
		t.Error("YEAR(invalid) should return nil")
	}
}

func TestScalar_Month(t *testing.T) {
	fn := ScalarFunctions["MONTH"]
	if fn([]interface{}{"2024-03-15T10:30:00Z"}).(int) != 3 {
		t.Error("MONTH failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("MONTH(nil) should return nil")
	}
}

func TestScalar_Day(t *testing.T) {
	fn := ScalarFunctions["DAY"]
	if fn([]interface{}{"2024-03-15T10:30:00Z"}).(int) != 15 {
		t.Error("DAY failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("DAY(nil) should return nil")
	}
}

func TestScalar_DatePart(t *testing.T) {
	fn := ScalarFunctions["DATEPART"]
	ts := "2024-03-15T14:30:45Z"

	tests := []struct {
		part string
		want int
	}{
		{"year", 2024},
		{"yyyy", 2024},
		{"yy", 2024},
		{"month", 3},
		{"mm", 3},
		{"m", 3},
		{"day", 15},
		{"dd", 15},
		{"d", 15},
		{"hour", 14},
		{"hh", 14},
		{"minute", 30},
		{"mi", 30},
		{"n", 30},
		{"second", 45},
		{"ss", 45},
		{"s", 45},
	}

	for _, tt := range tests {
		result := fn([]interface{}{tt.part, ts})
		if result != tt.want {
			t.Errorf("DATEPART('%s', ts) = %v, want %d", tt.part, result, tt.want)
		}
	}

	// Day of week
	result := fn([]interface{}{"dayofweek", ts})
	if result == nil {
		t.Error("DATEPART('dayofweek') returned nil")
	}

	// Day of year
	result = fn([]interface{}{"dayofyear", ts})
	if result == nil {
		t.Error("DATEPART('dayofyear') returned nil")
	}

	// Edge cases
	if fn([]interface{}{"year"}) != nil {
		t.Error("DATEPART with too few args should return nil")
	}
	if fn([]interface{}{42, ts}) != nil {
		t.Error("DATEPART with non-string part should return nil")
	}
	if fn([]interface{}{"year", "not-a-date"}) != nil {
		t.Error("DATEPART with bad date should return nil")
	}
	if fn([]interface{}{"invalid_part", ts}) != nil {
		t.Error("DATEPART with unknown part should return nil")
	}
}

func TestScalar_DateDiff(t *testing.T) {
	fn := ScalarFunctions["DATEDIFF"]
	start := "2024-01-15T10:00:00Z"
	end := "2024-03-20T14:30:00Z"

	tests := []struct {
		part string
		want int
	}{
		{"year", 0},
		{"month", 2},
		{"day", 65},
		{"hour", 1564},
	}

	for _, tt := range tests {
		result := fn([]interface{}{tt.part, start, end})
		if result != tt.want {
			t.Errorf("DATEDIFF('%s', start, end) = %v, want %d", tt.part, result, tt.want)
		}
	}

	// Year diff across years
	result := fn([]interface{}{"year", "2020-01-01T00:00:00Z", "2024-06-01T00:00:00Z"})
	if result != 4 {
		t.Errorf("DATEDIFF year across years = %v, want 4", result)
	}

	// Minute and second diffs
	result = fn([]interface{}{"minute", "2024-01-01T10:00:00Z", "2024-01-01T10:45:00Z"})
	if result != 45 {
		t.Errorf("DATEDIFF minute = %v, want 45", result)
	}
	result = fn([]interface{}{"second", "2024-01-01T10:00:00Z", "2024-01-01T10:01:30Z"})
	if result != 90 {
		t.Errorf("DATEDIFF second = %v, want 90", result)
	}

	// Edge cases
	if fn([]interface{}{"year", start}) != nil {
		t.Error("DATEDIFF with too few args should return nil")
	}
	if fn([]interface{}{42, start, end}) != nil {
		t.Error("DATEDIFF with non-string part should return nil")
	}
	if fn([]interface{}{"year", "bad", end}) != nil {
		t.Error("DATEDIFF with bad start should return nil")
	}
	if fn([]interface{}{"unknown", start, end}) != nil {
		t.Error("DATEDIFF with unknown part should return nil")
	}
}

func TestScalar_DateTrunc(t *testing.T) {
	fn := ScalarFunctions["DATE_TRUNC"]
	ts := "2024-03-15T14:30:45Z"

	tests := []struct {
		precision string
		contains  string
	}{
		{"year", "2024-01-01T00:00:00"},
		{"month", "2024-03-01T00:00:00"},
		{"day", "2024-03-15T00:00:00"},
		{"hour", "2024-03-15T14:00:00"},
		{"minute", "2024-03-15T14:30:00"},
		{"second", "2024-03-15T14:30:45"},
	}

	for _, tt := range tests {
		result := fn([]interface{}{tt.precision, ts})
		s, ok := result.(string)
		if !ok {
			t.Errorf("DATE_TRUNC('%s') returned %T, want string", tt.precision, result)
			continue
		}
		if !strings.Contains(s, tt.contains) {
			t.Errorf("DATE_TRUNC('%s', ts) = %s, want to contain %s", tt.precision, s, tt.contains)
		}
	}

	// Edge cases
	if fn([]interface{}{"year"}) != nil {
		t.Error("DATE_TRUNC with too few args should return nil")
	}
	if fn([]interface{}{42, ts}) != nil {
		t.Error("DATE_TRUNC with non-string precision should return nil")
	}
	if fn([]interface{}{"year", "not-a-date"}) != nil {
		t.Error("DATE_TRUNC with bad date should return nil")
	}
	if fn([]interface{}{"invalid", ts}) != nil {
		t.Error("DATE_TRUNC with unknown precision should return nil")
	}
}

func TestScalar_Substring(t *testing.T) {
	fn := ScalarFunctions["SUBSTRING"]
	// SUBSTRING is 1-indexed like T-SQL
	if fn([]interface{}{"hello world", float64(1), float64(5)}) != "hello" {
		t.Error("SUBSTRING(1,5) failed")
	}
	if fn([]interface{}{"hello", float64(3), float64(10)}) != "llo" {
		t.Error("SUBSTRING past end should truncate")
	}
	if fn([]interface{}{"hello", float64(10), float64(5)}) != "" {
		t.Error("SUBSTRING past string should return empty")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("SUBSTRING(nil) should return nil")
	}
}

func TestScalar_Left(t *testing.T) {
	fn := ScalarFunctions["LEFT"]
	if fn([]interface{}{"hello", float64(3)}) != "hel" {
		t.Error("LEFT(3) failed")
	}
	if fn([]interface{}{"hi", float64(10)}) != "hi" {
		t.Error("LEFT past length should return whole string")
	}
	if fn([]interface{}{"hello", float64(-1)}) != "" {
		t.Error("LEFT(-1) should return empty")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("LEFT(nil) should return nil")
	}
}

func TestScalar_Right(t *testing.T) {
	fn := ScalarFunctions["RIGHT"]
	if fn([]interface{}{"hello", float64(3)}) != "llo" {
		t.Error("RIGHT(3) failed")
	}
	if fn([]interface{}{"hi", float64(10)}) != "hi" {
		t.Error("RIGHT past length should return whole string")
	}
	if fn([]interface{}{"hello", float64(-1)}) != "" {
		t.Error("RIGHT(-1) should return empty")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("RIGHT(nil) should return nil")
	}
}

func TestScalar_Replace(t *testing.T) {
	fn := ScalarFunctions["REPLACE"]
	if fn([]interface{}{"hello world", "world", "there"}) != "hello there" {
		t.Error("REPLACE failed")
	}
	if fn([]interface{}{"aaa", "a", "bb"}) != "bbbbbb" {
		t.Error("REPLACE all occurrences failed")
	}
	if fn([]interface{}{nil}) != nil {
		t.Error("REPLACE(nil) should return nil")
	}
}

func TestScalar_CharIndex(t *testing.T) {
	fn := ScalarFunctions["CHARINDEX"]
	// CHARINDEX returns 1-indexed position
	if fn([]interface{}{"world", "hello world"}) != 7 {
		t.Errorf("CHARINDEX got %v, want 7", fn([]interface{}{"world", "hello world"}))
	}
	if fn([]interface{}{"xyz", "hello"}) != 0 {
		t.Error("CHARINDEX not found should return 0")
	}
	if fn([]interface{}{nil, "hello"}) != 0 {
		t.Error("CHARINDEX(nil) should return 0")
	}
	if fn([]interface{}{"a", nil}) != 0 {
		t.Error("CHARINDEX with nil string should return 0")
	}
}

func TestScalar_IsScalarFunction(t *testing.T) {
	// Test with non-FunctionCall expression (returns false)
	if IsScalarFunction(nil) {
		t.Error("nil should not be a scalar function")
	}
}

// ---------------------------------------------------------------------------
// parseTime helper
// ---------------------------------------------------------------------------

func TestParseTime(t *testing.T) {
	tests := []struct {
		input interface{}
		ok    bool
	}{
		{"2024-03-15T10:30:00Z", true},
		{"2024-03-15T10:30:00+05:00", true},
		{"2024-03-15T10:30:00", true},
		{"2024-03-15 10:30:00", true},
		{"2024-03-15", true},
		{"not a date", false},
		{42, false},
		{nil, false},
	}

	for _, tt := range tests {
		_, ok := parseTime(tt.input)
		if ok != tt.ok {
			t.Errorf("parseTime(%v) ok=%v, want %v", tt.input, ok, tt.ok)
		}
	}

	// time.Time input
	now := time.Now()
	parsed, ok := parseTime(now)
	if !ok || !parsed.Equal(now) {
		t.Error("parseTime(time.Time) should pass through")
	}
}

// ---------------------------------------------------------------------------
// toFloat coverage (aggregator.go)
// ---------------------------------------------------------------------------

func TestToFloat(t *testing.T) {
	tests := []struct {
		input interface{}
		want  float64
	}{
		{float64(3.14), 3.14},
		{int(42), 42.0},
		{int64(100), 100.0},
		{float32(2.5), 2.5},
		{"3.14", 3.14},
		{"not a number", 0.0},
		{nil, 0.0},
		{true, 0.0}, // unsupported type
	}

	for _, tt := range tests {
		got := toFloat(tt.input)
		if got != tt.want {
			t.Errorf("toFloat(%v) = %v, want %v", tt.input, got, tt.want)
		}
	}
}
