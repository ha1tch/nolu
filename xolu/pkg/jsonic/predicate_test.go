// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Predicate evaluation unit tests
// ---------------------------------------------------------------------------

func TestPredicateOp_String(t *testing.T) {
	tests := []struct {
		op   PredicateOp
		want string
	}{
		{OpEq, "="},
		{OpNeq, "!="},
		{OpLt, "<"},
		{OpLte, "<="},
		{OpGt, ">"},
		{OpGte, ">="},
		{OpIn, "IN"},
		{OpLike, "LIKE"},
	}
	for _, tt := range tests {
		if got := tt.op.String(); got != tt.want {
			t.Errorf("PredicateOp(%d).String() = %q, want %q", tt.op, got, tt.want)
		}
	}
}

func TestMatchLike(t *testing.T) {
	tests := []struct {
		s, pattern string
		want       bool
	}{
		{"hello", "hello", true},
		{"hello", "HELLO", true}, // case-insensitive
		{"hello", "hell%", true},
		{"hello", "%llo", true},
		{"hello", "%ell%", true},
		{"hello", "h_llo", true},
		{"hello", "h_lo", false},
		{"", "%", true},
		{"", "_", false},
		{"abc", "a%c", true},
		{"abc", "a%d", false},
		{"a", "%%", true},
	}
	for _, tt := range tests {
		got := matchLike(tt.s, tt.pattern)
		if got != tt.want {
			t.Errorf("matchLike(%q, %q) = %v, want %v", tt.s, tt.pattern, got, tt.want)
		}
	}
}

func TestPredicateSet_LookupAtom(t *testing.T) {
	ps := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("name", FieldString, OpEq, "Alice"),
		MakeFieldPredicate("age", FieldFloat, OpGt, float64(21)),
	})

	nameAtom := MakeAtom("name")
	ageAtom := MakeAtom("age")
	unknownAtom := MakeAtom("xyz")

	if idx := ps.LookupAtom(nameAtom); idx != 0 {
		t.Errorf("LookupAtom(name) = %d, want 0", idx)
	}
	if idx := ps.LookupAtom(ageAtom); idx != 1 {
		t.Errorf("LookupAtom(age) = %d, want 1", idx)
	}
	if idx := ps.LookupAtom(unknownAtom); idx != -1 {
		t.Errorf("LookupAtom(xyz) = %d, want -1", idx)
	}

	// Nil predicate set.
	var nilPS *PredicateSet
	if idx := nilPS.LookupAtom(nameAtom); idx != -1 {
		t.Errorf("nil.LookupAtom(name) = %d, want -1", idx)
	}
}

// ---------------------------------------------------------------------------
// FilterExtractFromTokens tests
// ---------------------------------------------------------------------------

func tokenise(t *testing.T, input string) *Tokeniser {
	t.Helper()
	tok := GetTokeniser()
	if err := tok.Tokenise([]byte(input)); err != nil {
		t.Fatalf("Tokenise(%q) failed: %v", input, err)
	}
	return tok
}

func TestFilterExtract_NoPredicate(t *testing.T) {
	tok := tokenise(t, `{"name":"Alice","age":30,"active":true}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name", "age"})
	result := FilterExtractFromTokens(tok, fields, nil)

	if !result.Passed {
		t.Fatal("expected Passed=true with no predicates")
	}
	if result.Data["name"] != "Alice" {
		t.Errorf("name = %v, want Alice", result.Data["name"])
	}
	if result.Data["age"] != float64(30) {
		t.Errorf("age = %v, want 30", result.Data["age"])
	}
	if _, ok := result.Data["active"]; ok {
		t.Error("active should not be in output (not in field list)")
	}
}

func TestFilterExtract_PredicatePass(t *testing.T) {
	tok := tokenise(t, `{"name":"Alice","age":30,"city":"London"}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name", "city"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpGte, float64(18)),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if !result.Passed {
		t.Fatal("expected Passed=true, age 30 >= 18")
	}
	if result.Data["name"] != "Alice" {
		t.Errorf("name = %v, want Alice", result.Data["name"])
	}
	if result.Data["city"] != "London" {
		t.Errorf("city = %v, want London", result.Data["city"])
	}
}

func TestFilterExtract_PredicateFail(t *testing.T) {
	tok := tokenise(t, `{"name":"Bob","age":15,"city":"Paris"}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name", "city"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpGte, float64(18)),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if result.Passed {
		t.Fatal("expected Passed=false, age 15 < 18")
	}
	if result.Data != nil && len(result.Data) > 0 {
		t.Errorf("Data should be empty on failure, got %v", result.Data)
	}
}

func TestFilterExtract_MissingPredicateField(t *testing.T) {
	tok := tokenise(t, `{"name":"Charlie","city":"Berlin"}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpGt, float64(0)),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if result.Passed {
		t.Fatal("expected Passed=false when predicate field is missing")
	}
}

func TestFilterExtract_MultiplePredicates(t *testing.T) {
	tok := tokenise(t, `{"name":"Diana","age":25,"active":true}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpGte, float64(18)),
		MakeFieldPredicate("active", FieldBool, OpEq, true),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if !result.Passed {
		t.Fatal("expected Passed=true, both predicates match")
	}
	if result.Data["name"] != "Diana" {
		t.Errorf("name = %v, want Diana", result.Data["name"])
	}
}

func TestFilterExtract_MultiplePredicatesPartialFail(t *testing.T) {
	tok := tokenise(t, `{"name":"Eve","age":25,"active":false}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpGte, float64(18)),
		MakeFieldPredicate("active", FieldBool, OpEq, true),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if result.Passed {
		t.Fatal("expected Passed=false, active=false != true")
	}
}

func TestFilterExtract_StringEquality(t *testing.T) {
	tok := tokenise(t, `{"status":"active","name":"Frank"}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("status", FieldString, OpEq, "active"),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if !result.Passed {
		t.Fatal("expected Passed=true, status=active")
	}
	if result.Data["name"] != "Frank" {
		t.Errorf("name = %v, want Frank", result.Data["name"])
	}
}

func TestFilterExtract_StringInequality(t *testing.T) {
	tok := tokenise(t, `{"status":"inactive","name":"Grace"}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("status", FieldString, OpEq, "active"),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if result.Passed {
		t.Fatal("expected Passed=false, status=inactive != active")
	}
}

func TestFilterExtract_PredicateFieldAlsoInOutput(t *testing.T) {
	// The predicate field is also a SELECT field.
	tok := tokenise(t, `{"name":"Heidi","age":30}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name", "age"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpEq, float64(30)),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if !result.Passed {
		t.Fatal("expected Passed=true")
	}
	if result.Data["age"] != float64(30) {
		t.Errorf("age = %v, want 30", result.Data["age"])
	}
}

func TestFilterExtract_NestedValueSkipped(t *testing.T) {
	tok := tokenise(t, `{"name":"Ivan","meta":{"x":1},"age":40}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("age", FieldFloat, OpGt, float64(35)),
	})

	result := FilterExtractFromTokens(tok, fields, preds)

	if !result.Passed {
		t.Fatal("expected Passed=true, nested object should be skipped correctly")
	}
}

func TestFilterExtract_NumericComparisons(t *testing.T) {
	tests := []struct {
		name string
		json string
		op   PredicateOp
		val  float64
		want bool
	}{
		{"eq_match", `{"x":10}`, OpEq, 10, true},
		{"eq_miss", `{"x":10}`, OpEq, 11, false},
		{"neq_match", `{"x":10}`, OpNeq, 11, true},
		{"neq_miss", `{"x":10}`, OpNeq, 10, false},
		{"lt_match", `{"x":10}`, OpLt, 11, true},
		{"lt_miss", `{"x":10}`, OpLt, 10, false},
		{"lte_match_eq", `{"x":10}`, OpLte, 10, true},
		{"lte_match_lt", `{"x":10}`, OpLte, 11, true},
		{"lte_miss", `{"x":10}`, OpLte, 9, false},
		{"gt_match", `{"x":10}`, OpGt, 9, true},
		{"gt_miss", `{"x":10}`, OpGt, 10, false},
		{"gte_match_eq", `{"x":10}`, OpGte, 10, true},
		{"gte_miss", `{"x":10}`, OpGte, 11, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := tokenise(t, tt.json)
			defer PutTokeniser(tok)

			fields := MakeFilterFieldEntries([]string{"x"})
			preds := NewPredicateSet([]FieldPredicate{
				MakeFieldPredicate("x", FieldFloat, tt.op, tt.val),
			})

			result := FilterExtractFromTokens(tok, fields, preds)
			if result.Passed != tt.want {
				t.Errorf("Passed = %v, want %v", result.Passed, tt.want)
			}
		})
	}
}

func TestFilterExtract_LikePredicate(t *testing.T) {
	tests := []struct {
		name    string
		json    string
		pattern string
		want    bool
	}{
		{"prefix", `{"name":"Alice"}`, "Ali%", true},
		{"suffix", `{"name":"Alice"}`, "%ice", true},
		{"contains", `{"name":"Alice"}`, "%lic%", true},
		{"no_match", `{"name":"Alice"}`, "Bob%", false},
		{"case_insensitive", `{"name":"Alice"}`, "alice", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := tokenise(t, tt.json)
			defer PutTokeniser(tok)

			fields := MakeFilterFieldEntries([]string{"name"})
			preds := NewPredicateSet([]FieldPredicate{
				MakeFieldPredicate("name", FieldString, OpLike, tt.pattern),
			})

			result := FilterExtractFromTokens(tok, fields, preds)
			if result.Passed != tt.want {
				t.Errorf("Passed = %v, want %v", result.Passed, tt.want)
			}
		})
	}
}

func TestFilterExtract_InPredicate(t *testing.T) {
	tok := tokenise(t, `{"status":"pending","id":1}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"id"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("status", FieldString, OpIn,
			[]interface{}{"active", "pending", "review"}),
	})

	result := FilterExtractFromTokens(tok, fields, preds)
	if !result.Passed {
		t.Fatal("expected Passed=true, status=pending is in list")
	}

	// Negative case.
	tok2 := tokenise(t, `{"status":"deleted","id":2}`)
	defer PutTokeniser(tok2)

	result2 := FilterExtractFromTokens(tok2, fields, preds)
	if result2.Passed {
		t.Fatal("expected Passed=false, status=deleted not in list")
	}
}

func TestFilterExtract_EmptyObject(t *testing.T) {
	tok := tokenise(t, `{}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("name", FieldString, OpEq, "x"),
	})

	result := FilterExtractFromTokens(tok, fields, preds)
	if result.Passed {
		t.Fatal("expected Passed=false for empty object with predicate")
	}
}

func TestFilterExtract_EmptyPredicateSet(t *testing.T) {
	tok := tokenise(t, `{"name":"Alice"}`)
	defer PutTokeniser(tok)

	fields := MakeFilterFieldEntries([]string{"name"})
	preds := NewPredicateSet([]FieldPredicate{})

	result := FilterExtractFromTokens(tok, fields, preds)
	if !result.Passed {
		t.Fatal("expected Passed=true with empty predicate set")
	}
}

// ---------------------------------------------------------------------------
// CoercePredicateValue tests
// ---------------------------------------------------------------------------

func TestCoercePredicateValue(t *testing.T) {
	// String coercion.
	v, err := CoercePredicateValue("hello", FieldString)
	if err != nil || v != "hello" {
		t.Errorf("string coerce: got %v, %v", v, err)
	}

	// Float coercion from int.
	v, err = CoercePredicateValue(42, FieldFloat)
	if err != nil || v != float64(42) {
		t.Errorf("float from int: got %v, %v", v, err)
	}

	// Int coercion from float.
	v, err = CoercePredicateValue(float64(42), FieldInt)
	if err != nil || v != int64(42) {
		t.Errorf("int from float: got %v, %v", v, err)
	}

	// Bool coercion.
	v, err = CoercePredicateValue(true, FieldBool)
	if err != nil || v != true {
		t.Errorf("bool: got %v, %v", v, err)
	}

	// Nil should error.
	_, err = CoercePredicateValue(nil, FieldString)
	if err == nil {
		t.Error("expected error for nil value")
	}
}
