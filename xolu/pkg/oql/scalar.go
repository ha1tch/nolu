// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"fmt"
	"strings"
	"time"

	"github.com/ha1tch/tsqlparser/ast"
)

// ScalarFunc is a function that operates on a single value and returns a result.
type ScalarFunc func(args []interface{}) interface{}

// ScalarFunctions maps function names to their implementations.
var ScalarFunctions = map[string]ScalarFunc{
	"DATE_TRUNC":  scalarDateTrunc,
	"UPPER":       scalarUpper,
	"LOWER":       scalarLower,
	"LEN":         scalarLen,
	"TRIM":        scalarTrim,
	"COALESCE":    scalarCoalesce,
	"ISNULL":      scalarCoalesce, // SQL Server alias for COALESCE with 2 args
	"CONCAT":      scalarConcat,
	"CAST":        scalarCast,
	"ABS":         scalarAbs,
	"ROUND":       scalarRound,
	"FLOOR":       scalarFloor,
	"CEILING":     scalarCeiling,
	"GETDATE":     scalarGetDate,
	"GETUTCDATE":  scalarGetUTCDate,
	"YEAR":        scalarYear,
	"MONTH":       scalarMonth,
	"DAY":         scalarDay,
	"DATEPART":    scalarDatePart,
	"DATEDIFF":    scalarDateDiff,
	"SUBSTRING":   scalarSubstring,
	"LEFT":        scalarLeft,
	"RIGHT":       scalarRight,
	"REPLACE":     scalarReplace,
	"CHARINDEX":   scalarCharIndex,
}

// IsScalarFunction checks whether a function call is a scalar (non-aggregate)
// function. Returns true if the function name matches a known scalar.
func IsScalarFunction(expr ast.Expression) bool {
	fc, ok := expr.(*ast.FunctionCall)
	if !ok {
		return false
	}
	funcName := strings.ToUpper(exprToString(fc.Function))
	_, isScalar := ScalarFunctions[funcName]
	return isScalar
}

// EvalScalarFunction evaluates a scalar function call against a record.
// The evalFn callback is used to resolve argument expressions to values.
func EvalScalarFunction(fc *ast.FunctionCall, evalFn func(ast.Expression) interface{}) interface{} {
	funcName := strings.ToUpper(exprToString(fc.Function))
	fn, exists := ScalarFunctions[funcName]
	if !exists {
		return nil
	}

	// Evaluate arguments
	args := make([]interface{}, len(fc.Arguments))
	for i, arg := range fc.Arguments {
		args[i] = evalFn(arg)
	}

	return fn(args)
}

// --- Time parsing helpers ---

// Common timestamp formats to try when parsing time strings.
var timeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05",
	"2006-01-02T15:04:05Z",
	"2006-01-02",
}

// parseTime attempts to parse a string as a time value.
func parseTime(v interface{}) (time.Time, bool) {
	switch val := v.(type) {
	case time.Time:
		return val, true
	case string:
		for _, fmt := range timeFormats {
			if t, err := time.Parse(fmt, val); err == nil {
				return t, true
			}
		}
	}
	return time.Time{}, false
}

// --- Scalar function implementations ---

// DATE_TRUNC(precision, timestamp) — truncates a timestamp to the given precision.
// Precision values: 'year', 'month', 'day', 'hour', 'minute', 'second'.
func scalarDateTrunc(args []interface{}) interface{} {
	if len(args) < 2 {
		return nil
	}

	precision, ok := args[0].(string)
	if !ok {
		return nil
	}
	precision = strings.ToLower(precision)

	t, ok := parseTime(args[1])
	if !ok {
		return nil
	}

	var truncated time.Time
	switch precision {
	case "year":
		truncated = time.Date(t.Year(), 1, 1, 0, 0, 0, 0, t.Location())
	case "month":
		truncated = time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, t.Location())
	case "day":
		truncated = time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	case "hour":
		truncated = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, t.Location())
	case "minute":
		truncated = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), 0, 0, t.Location())
	case "second":
		truncated = time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), t.Minute(), t.Second(), 0, t.Location())
	default:
		return nil
	}

	return truncated.Format(time.RFC3339)
}

// UPPER(string) — converts to uppercase.
func scalarUpper(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	return strings.ToUpper(fmt.Sprintf("%v", args[0]))
}

// LOWER(string) — converts to lowercase.
func scalarLower(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	return strings.ToLower(fmt.Sprintf("%v", args[0]))
}

// LEN(string) — returns length.
func scalarLen(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	return len(fmt.Sprintf("%v", args[0]))
}

// TRIM(string) — removes leading/trailing whitespace.
func scalarTrim(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	return strings.TrimSpace(fmt.Sprintf("%v", args[0]))
}

// COALESCE(a, b, ...) — returns first non-nil value.
func scalarCoalesce(args []interface{}) interface{} {
	for _, a := range args {
		if a != nil {
			return a
		}
	}
	return nil
}

// CONCAT(a, b, ...) — concatenates all arguments as strings.
func scalarConcat(args []interface{}) interface{} {
	var sb strings.Builder
	for _, a := range args {
		if a != nil {
			sb.WriteString(fmt.Sprintf("%v", a))
		}
	}
	return sb.String()
}

// CAST — simplified: converts value to string representation.
func scalarCast(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	return fmt.Sprintf("%v", args[0])
}

// ABS(number) — absolute value.
func scalarAbs(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	f := toFloat(args[0])
	if f < 0 {
		return -f
	}
	return f
}

// ROUND(number, precision) — rounds to given decimal places.
func scalarRound(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	f := toFloat(args[0])
	precision := 0
	if len(args) >= 2 && args[1] != nil {
		precision = int(toFloat(args[1]))
	}
	pow := 1.0
	for i := 0; i < precision; i++ {
		pow *= 10
	}
	return float64(int(f*pow+0.5)) / pow
}

// FLOOR(number) — floor value.
func scalarFloor(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	f := toFloat(args[0])
	return float64(int(f))
}

// CEILING(number) — ceiling value.
func scalarCeiling(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	f := toFloat(args[0])
	i := int(f)
	if float64(i) < f {
		return float64(i + 1)
	}
	return float64(i)
}

// GETDATE() — current date/time.
func scalarGetDate(args []interface{}) interface{} {
	return time.Now().Format(time.RFC3339)
}

// GETUTCDATE() — current UTC date/time.
func scalarGetUTCDate(args []interface{}) interface{} {
	return time.Now().UTC().Format(time.RFC3339)
}

// YEAR(date) — extracts year.
func scalarYear(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	if t, ok := parseTime(args[0]); ok {
		return t.Year()
	}
	return nil
}

// MONTH(date) — extracts month (1-12).
func scalarMonth(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	if t, ok := parseTime(args[0]); ok {
		return int(t.Month())
	}
	return nil
}

// DAY(date) — extracts day of month.
func scalarDay(args []interface{}) interface{} {
	if len(args) < 1 || args[0] == nil {
		return nil
	}
	if t, ok := parseTime(args[0]); ok {
		return t.Day()
	}
	return nil
}

// DATEPART(part, date) — extracts a part of the date.
func scalarDatePart(args []interface{}) interface{} {
	if len(args) < 2 {
		return nil
	}
	part, ok := args[0].(string)
	if !ok {
		return nil
	}
	t, ok := parseTime(args[1])
	if !ok {
		return nil
	}
	switch strings.ToLower(part) {
	case "year", "yy", "yyyy":
		return t.Year()
	case "month", "mm", "m":
		return int(t.Month())
	case "day", "dd", "d":
		return t.Day()
	case "hour", "hh":
		return t.Hour()
	case "minute", "mi", "n":
		return t.Minute()
	case "second", "ss", "s":
		return t.Second()
	case "dayofweek", "dw", "w":
		return int(t.Weekday())
	case "dayofyear", "dy", "y":
		return t.YearDay()
	}
	return nil
}

// DATEDIFF(part, start, end) — difference between two dates.
func scalarDateDiff(args []interface{}) interface{} {
	if len(args) < 3 {
		return nil
	}
	part, ok := args[0].(string)
	if !ok {
		return nil
	}
	start, ok1 := parseTime(args[1])
	end, ok2 := parseTime(args[2])
	if !ok1 || !ok2 {
		return nil
	}
	diff := end.Sub(start)
	switch strings.ToLower(part) {
	case "year", "yy", "yyyy":
		return end.Year() - start.Year()
	case "month", "mm", "m":
		return (end.Year()-start.Year())*12 + int(end.Month()-start.Month())
	case "day", "dd", "d":
		return int(diff.Hours() / 24)
	case "hour", "hh":
		return int(diff.Hours())
	case "minute", "mi", "n":
		return int(diff.Minutes())
	case "second", "ss", "s":
		return int(diff.Seconds())
	}
	return nil
}

// SUBSTRING(string, start, length) — extracts a substring (1-indexed).
func scalarSubstring(args []interface{}) interface{} {
	if len(args) < 3 || args[0] == nil {
		return nil
	}
	s := fmt.Sprintf("%v", args[0])
	start := int(toFloat(args[1])) - 1 // Convert from 1-indexed
	length := int(toFloat(args[2]))
	if start < 0 {
		start = 0
	}
	if start >= len(s) {
		return ""
	}
	end := start + length
	if end > len(s) {
		end = len(s)
	}
	return s[start:end]
}

// LEFT(string, n) — first n characters.
func scalarLeft(args []interface{}) interface{} {
	if len(args) < 2 || args[0] == nil {
		return nil
	}
	s := fmt.Sprintf("%v", args[0])
	n := int(toFloat(args[1]))
	if n > len(s) {
		n = len(s)
	}
	if n < 0 {
		n = 0
	}
	return s[:n]
}

// RIGHT(string, n) — last n characters.
func scalarRight(args []interface{}) interface{} {
	if len(args) < 2 || args[0] == nil {
		return nil
	}
	s := fmt.Sprintf("%v", args[0])
	n := int(toFloat(args[1]))
	if n > len(s) {
		n = len(s)
	}
	if n < 0 {
		n = 0
	}
	return s[len(s)-n:]
}

// REPLACE(string, from, to) — replaces all occurrences.
func scalarReplace(args []interface{}) interface{} {
	if len(args) < 3 || args[0] == nil {
		return nil
	}
	s := fmt.Sprintf("%v", args[0])
	old := fmt.Sprintf("%v", args[1])
	new := fmt.Sprintf("%v", args[2])
	return strings.ReplaceAll(s, old, new)
}

// CHARINDEX(search, string) — 1-indexed position of search in string, 0 if not found.
func scalarCharIndex(args []interface{}) interface{} {
	if len(args) < 2 || args[0] == nil || args[1] == nil {
		return 0
	}
	search := fmt.Sprintf("%v", args[0])
	s := fmt.Sprintf("%v", args[1])
	idx := strings.Index(s, search)
	if idx < 0 {
		return 0
	}
	return idx + 1 // 1-indexed
}
