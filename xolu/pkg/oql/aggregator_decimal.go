// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"github.com/shopspring/decimal"
)

// ---------------------------------------------------------------------------
// Decimal-aware aggregate functions
// ---------------------------------------------------------------------------
// These replace the standard float64-based aggregates for columns that
// have format:"decimal" in their schema. They use shopspring/decimal for
// exact arithmetic, avoiding float64 precision loss.
//
// Decimal values arrive as strings (already denormalised by the storage
// read path). The functions parse each string, aggregate precisely, and
// return the result as a string. Non-parseable values are skipped.
// ---------------------------------------------------------------------------

// DecimalAggregates maps aggregate function names to decimal-precise
// implementations.
var DecimalAggregates = map[string]AggregateFunc{
	"SUM": aggDecimalSum,
	"AVG": aggDecimalAvg,
	"MIN": aggDecimalMin,
	"MAX": aggDecimalMax,
}

func aggDecimalSum(values []interface{}) interface{} {
	sum := decimal.Zero
	hasValue := false
	for _, v := range values {
		if v == nil {
			continue
		}
		d, ok := toDecimal(v)
		if !ok {
			continue
		}
		sum = sum.Add(d)
		hasValue = true
	}
	if !hasValue {
		return nil
	}
	return sum.String()
}

func aggDecimalAvg(values []interface{}) interface{} {
	sum := decimal.Zero
	count := 0
	for _, v := range values {
		if v == nil {
			continue
		}
		d, ok := toDecimal(v)
		if !ok {
			continue
		}
		sum = sum.Add(d)
		count++
	}
	if count == 0 {
		return nil
	}
	// DivRound with 10 decimal places for AVG
	avg := sum.Div(decimal.NewFromInt(int64(count)))
	return avg.String()
}

func aggDecimalMin(values []interface{}) interface{} {
	var min *decimal.Decimal
	for _, v := range values {
		if v == nil {
			continue
		}
		d, ok := toDecimal(v)
		if !ok {
			continue
		}
		if min == nil || d.LessThan(*min) {
			min = &d
		}
	}
	if min == nil {
		return nil
	}
	return min.String()
}

func aggDecimalMax(values []interface{}) interface{} {
	var max *decimal.Decimal
	for _, v := range values {
		if v == nil {
			continue
		}
		d, ok := toDecimal(v)
		if !ok {
			continue
		}
		if max == nil || d.GreaterThan(*max) {
			max = &d
		}
	}
	if max == nil {
		return nil
	}
	return max.String()
}

// toDecimal attempts to parse a value as a shopspring/decimal.
func toDecimal(v interface{}) (decimal.Decimal, bool) {
	switch val := v.(type) {
	case string:
		d, err := decimal.NewFromString(val)
		if err != nil {
			return decimal.Zero, false
		}
		return d, true
	case float64:
		return decimal.NewFromFloat(val), true
	case int:
		return decimal.NewFromInt(int64(val)), true
	case int64:
		return decimal.NewFromInt(val), true
	default:
		return decimal.Zero, false
	}
}
