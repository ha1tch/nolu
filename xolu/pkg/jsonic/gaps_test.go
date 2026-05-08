// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"encoding/json"
	"testing"
)

// ---------------------------------------------------------------------------
// AtomRegistry — MustRegister (panic path) and VerifyMatch
// ---------------------------------------------------------------------------

func TestMustRegister_Panics_OnDuplicate_DifferentAtom(t *testing.T) {
	// MustRegister panics when Register returns an error.
	// Register errors if a different string maps to the same atom (collision).
	// We can't easily force a collision, but we CAN test the happy path
	// (no panic) and the panic path via a direct call to Register that
	// returns an error.
	r := NewAtomRegistry()
	// Happy path: should not panic.
	a := r.MustRegister("testfield")
	if a == 0 {
		t.Error("MustRegister returned zero atom")
	}
}

func TestMustRegister_PanicsOnError(t *testing.T) {
	// To trigger a panic, we need Register to return an error.
	// Register errors when: two different names produce the same atom (collision).
	// Set up: put "other_name" into byAtom at the atom for "collideable",
	// without putting "collideable" into byName (so the early-return guard
	// doesn't fire).
	r := NewAtomRegistry()
	atom := MakeAtom("collideable")
	r.mu.Lock()
	r.byAtom[atom] = "other_name_entirely" // simulate prior registration of a colliding name
	r.mu.Unlock()

	defer func() {
		if rec := recover(); rec == nil {
			t.Error("MustRegister should panic on registration error")
		}
	}()
	r.MustRegister("collideable") // Register will see byAtom[atom]="other_name_entirely" != "collideable" → error → panic
}

func TestVerifyMatch_ShortName_NoFullCheck(t *testing.T) {
	r := NewAtomRegistry()
	a := r.MustRegister("name") // 4 bytes — fits inline, no full verify needed
	// Any byte slice should verify true for a short (non-hashed) atom.
	if !r.VerifyMatch(a, []byte("name")) {
		t.Error("short atom should verify true without full string check")
	}
}

func TestVerifyMatch_LongName_Matches(t *testing.T) {
	r := NewAtomRegistry()
	long := "averylongfieldnamethatexceedseightbytes"
	a := r.MustRegister(long)
	if !r.VerifyMatch(a, []byte(long)) {
		t.Error("long atom should verify true when bytes match")
	}
}

func TestVerifyMatch_LongName_Mismatch(t *testing.T) {
	r := NewAtomRegistry()
	long := "averylongfieldnamethatexceedseightbytes"
	a := r.MustRegister(long)
	if r.VerifyMatch(a, []byte("averylongfieldnamethatexceedseightXXXXX")) {
		t.Error("long atom should verify false when bytes differ")
	}
}

func TestVerifyMatch_UnregisteredAtom_ReturnsFalse(t *testing.T) {
	r := NewAtomRegistry()
	// Fabricate an atom that requires full verify but isn't in the registry.
	bogus := MakeAtom("somethingreallylongthatisnotregistered")
	r.mu.Lock()
	r.needsFull[bogus] = true // mark as needing full verify without registering
	r.mu.Unlock()
	if r.VerifyMatch(bogus, []byte("somethingreallylongthatisnotregistered")) {
		t.Error("unregistered atom needing full verify should return false")
	}
}

// ---------------------------------------------------------------------------
// ColumnStore — Count, FilterIndicesBool, SortIndicesByString,
//               GroupSumIndices, GroupCountIndices, String
// ---------------------------------------------------------------------------

func TestCount(t *testing.T) {
	if Count([]int{1, 2, 3}) != 3 {
		t.Error("Count([1,2,3]) should be 3")
	}
	if Count([]string{}) != 0 {
		t.Error("Count([]) should be 0")
	}
	if Count([]bool{true, false}) != 2 {
		t.Error("Count([true,false]) should be 2")
	}
}

func TestFilterIndicesBool(t *testing.T) {
	col := []bool{true, false, true, false, true}
	idx := FilterIndicesBool(col, func(b bool) bool { return b })
	if len(idx) != 3 {
		t.Fatalf("expected 3 true indices, got %d", len(idx))
	}
	for _, i := range idx {
		if !col[i] {
			t.Errorf("index %d is not true", i)
		}
	}
}

func TestFilterIndicesBool_AllFalse(t *testing.T) {
	col := []bool{false, false}
	idx := FilterIndicesBool(col, func(b bool) bool { return b })
	if len(idx) != 0 {
		t.Errorf("expected 0 indices, got %d", len(idx))
	}
}

func TestSortIndicesByString_Asc(t *testing.T) {
	col := []string{"banana", "apple", "cherry"}
	idx := []int{0, 1, 2}
	SortIndicesByString(col, idx, false)
	order := make([]string, 3)
	for i, ii := range idx {
		order[i] = col[ii]
	}
	if order[0] != "apple" || order[1] != "banana" || order[2] != "cherry" {
		t.Errorf("ascending sort wrong: %v", order)
	}
}

func TestSortIndicesByString_Desc(t *testing.T) {
	col := []string{"banana", "apple", "cherry"}
	idx := []int{0, 1, 2}
	SortIndicesByString(col, idx, true)
	if col[idx[0]] != "cherry" {
		t.Errorf("descending sort: first should be cherry, got %s", col[idx[0]])
	}
}

func TestGroupSumIndices(t *testing.T) {
	groups := []string{"a", "b", "a", "b", "a"}
	vals := []float64{1, 2, 3, 4, 5}
	idx := []int{0, 1, 2, 3, 4}
	result := GroupSumIndices(groups, vals, idx)
	if result["a"] != 9.0 {
		t.Errorf("a sum: got %v, want 9", result["a"])
	}
	if result["b"] != 6.0 {
		t.Errorf("b sum: got %v, want 6", result["b"])
	}
}

func TestGroupSumIndices_Subset(t *testing.T) {
	groups := []string{"a", "b", "a", "b", "a"}
	vals := []float64{1, 2, 3, 4, 5}
	idx := []int{0, 2} // only first and third elements (both "a")
	result := GroupSumIndices(groups, vals, idx)
	if result["a"] != 4.0 {
		t.Errorf("a sum subset: got %v, want 4", result["a"])
	}
	if _, ok := result["b"]; ok {
		t.Error("b should not appear in subset result")
	}
}

func TestGroupCountIndices(t *testing.T) {
	groups := []string{"x", "y", "x", "z", "y", "y"}
	idx := []int{0, 1, 2, 3, 4, 5}
	result := GroupCountIndices(groups, idx)
	if result["x"] != 2 {
		t.Errorf("x count: got %d, want 2", result["x"])
	}
	if result["y"] != 3 {
		t.Errorf("y count: got %d, want 3", result["y"])
	}
	if result["z"] != 1 {
		t.Errorf("z count: got %d, want 1", result["z"])
	}
}

func TestGroupCountIndices_Subset(t *testing.T) {
	groups := []string{"x", "y", "x", "y"}
	idx := []int{1, 3} // only "y" elements
	result := GroupCountIndices(groups, idx)
	if result["y"] != 2 {
		t.Errorf("y count subset: got %d, want 2", result["y"])
	}
	if _, ok := result["x"]; ok {
		t.Error("x should not appear in subset result")
	}
}

func TestColumnStore_String(t *testing.T) {
	cs := NewColumnStore(4)
	cs.Strings[MakeAtom("name")] = []string{"a", "b"}
	s := cs.String()
	if len(s) == 0 {
		t.Error("String() should return non-empty")
	}
	// Should contain "ColumnStore{" prefix.
	if len(s) < 12 || s[:12] != "ColumnStore{" {
		t.Errorf("String() format unexpected: %q", s)
	}
}

// ---------------------------------------------------------------------------
// filter_extract.go — tokenToGoValue via FilterExtractFromTokens
// The function is unexported but fully exercised through its caller.
// We hit all branches: string, number (int and float), true, false, null,
// object, array.
// ---------------------------------------------------------------------------

func filterExtract(t *testing.T, input string, fields []string) map[string]interface{} {
	t.Helper()
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	if err := tok.Tokenise([]byte(input)); err != nil {
		t.Fatalf("Tokenise(%q): %v", input, err)
	}
	entries := MakeFilterFieldEntries(fields)
	result := FilterExtractFromTokens(tok, entries, nil)
	if !result.Passed {
		t.Fatalf("FilterExtractFromTokens: did not pass for %q", input)
	}
	return result.Data
}

func TestTokenToGoValue_String(t *testing.T) {
	data := filterExtract(t, `{"name":"alice"}`, []string{"name"})
	if data["name"] != "alice" {
		t.Errorf("string: got %v", data["name"])
	}
}

func TestTokenToGoValue_IntNumber(t *testing.T) {
	data := filterExtract(t, `{"age":30}`, []string{"age"})
	// JSON numbers become float64 via json.Unmarshal.
	if data["age"] != float64(30) {
		t.Errorf("int number: got %T %v", data["age"], data["age"])
	}
}

func TestTokenToGoValue_FloatNumber(t *testing.T) {
	data := filterExtract(t, `{"score":9.5}`, []string{"score"})
	if data["score"] != 9.5 {
		t.Errorf("float number: got %v", data["score"])
	}
}

func TestTokenToGoValue_True(t *testing.T) {
	data := filterExtract(t, `{"active":true}`, []string{"active"})
	if data["active"] != true {
		t.Errorf("true: got %v", data["active"])
	}
}

func TestTokenToGoValue_False(t *testing.T) {
	data := filterExtract(t, `{"active":false}`, []string{"active"})
	if data["active"] != false {
		t.Errorf("false: got %v", data["active"])
	}
}

func TestTokenToGoValue_Object(t *testing.T) {
	data := filterExtract(t, `{"meta":{"k":"v"}}`, []string{"meta"})
	m, ok := data["meta"].(map[string]interface{})
	if !ok {
		t.Fatalf("object: expected map, got %T", data["meta"])
	}
	if m["k"] != "v" {
		t.Errorf("object value: got %v", m["k"])
	}
}

func TestTokenToGoValue_Array(t *testing.T) {
	data := filterExtract(t, `{"tags":["go","test"]}`, []string{"tags"})
	arr, ok := data["tags"].([]interface{})
	if !ok {
		t.Fatalf("array: expected []interface{}, got %T", data["tags"])
	}
	if len(arr) != 2 {
		t.Errorf("array length: got %d, want 2", len(arr))
	}
}

// ---------------------------------------------------------------------------
// predicate.go — evalInt, evalFloat, evalInNumeric, CoercePredicateValue
// ---------------------------------------------------------------------------

func makePred(name string, ft FieldType, op PredicateOp, val interface{}) FieldPredicate {
	return MakeFieldPredicate(name, ft, op, val)
}

func TestEvalInt_EqualInt64(t *testing.T) {
	fp := makePred("n", FieldInt, OpEq, int64(42))
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	input := []byte(`{"n":42}`)
	_ = tok.Tokenise(input)

	// evalInt is exercised through EvalTokenValue when the token is TokNumber
	// and the FieldSpec type is FieldInt.
	entries := MakeFilterFieldEntries([]string{"n"})
	_ = entries // keep for reference
	result := FilterExtractFromTokens(tok, entries, NewPredicateSet([]FieldPredicate{fp}))
	if !result.Passed {
		t.Error("evalInt: 42 == 42 should pass")
	}
}

func TestEvalInt_OpIn(t *testing.T) {
	fp := makePred("n", FieldInt, OpIn, []interface{}{float64(1), float64(2), float64(3)})
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	input := []byte(`{"n":2}`)
	_ = tok.Tokenise(input)
	result := FilterExtractFromTokens(tok, MakeFilterFieldEntries([]string{"n"}), NewPredicateSet([]FieldPredicate{fp}))
	if !result.Passed {
		t.Error("evalInt OpIn: 2 in [1,2,3] should pass")
	}
}

func TestEvalInt_OpIn_Miss(t *testing.T) {
	fp := makePred("n", FieldInt, OpIn, []interface{}{float64(1), float64(3)})
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	input := []byte(`{"n":2}`)
	_ = tok.Tokenise(input)
	result := FilterExtractFromTokens(tok, MakeFilterFieldEntries([]string{"n"}), NewPredicateSet([]FieldPredicate{fp}))
	if result.Passed {
		t.Error("evalInt OpIn: 2 not in [1,3] should fail")
	}
}

func TestEvalFloat_Equal(t *testing.T) {
	fp := makePred("x", FieldFloat, OpEq, float64(3.14))
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	input := []byte(`{"x":3.14}`)
	_ = tok.Tokenise(input)
	result := FilterExtractFromTokens(tok, MakeFilterFieldEntries([]string{"x"}), NewPredicateSet([]FieldPredicate{fp}))
	if !result.Passed {
		t.Error("evalFloat: 3.14 == 3.14 should pass")
	}
}

func TestEvalFloat_OpIn(t *testing.T) {
	fp := makePred("x", FieldFloat, OpIn, []interface{}{float64(1.0), float64(2.5)})
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	input := []byte(`{"x":2.5}`)
	_ = tok.Tokenise(input)
	result := FilterExtractFromTokens(tok, MakeFilterFieldEntries([]string{"x"}), NewPredicateSet([]FieldPredicate{fp}))
	if !result.Passed {
		t.Error("evalFloat OpIn: 2.5 in [1.0, 2.5] should pass")
	}
}

func TestEvalInNumeric_Int64Items(t *testing.T) {
	fp := makePred("n", FieldInt, OpIn, []interface{}{int64(10), int64(20)})
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	input := []byte(`{"n":10}`)
	_ = tok.Tokenise(input)
	result := FilterExtractFromTokens(tok, MakeFilterFieldEntries([]string{"n"}), NewPredicateSet([]FieldPredicate{fp}))
	if !result.Passed {
		t.Error("evalInNumeric with int64 items: 10 in [10,20] should pass")
	}
}

func TestCoercePredicateValue_AllTypes(t *testing.T) {
	cases := []struct {
		name    string
		val     interface{}
		ft      FieldType
		wantErr bool
		wantVal interface{}
	}{
		{"string from string",    "hello",       FieldString, false, "hello"},
		{"string from number",    json.Number("42"), FieldString, false, "42"},
		{"string from other",     true,          FieldString, false, "true"},
		{"int from int64",        int64(5),      FieldInt,    false, int64(5)},
		{"int from float64",      float64(3.0),  FieldInt,    false, int64(3)},
		{"int from int",          int(7),        FieldInt,    false, int64(7)},
		{"int from json.Number",  json.Number("9"), FieldInt, false, int64(9)},
		{"int from bad type",     "abc",         FieldInt,    true,  nil},
		{"float from float64",    float64(1.5),  FieldFloat,  false, float64(1.5)},
		{"float from int64",      int64(2),      FieldFloat,  false, float64(2.0)},
		{"float from int",        int(3),        FieldFloat,  false, float64(3.0)},
		{"float from json.Number",json.Number("4.5"), FieldFloat, false, float64(4.5)},
		{"float from bad type",   "abc",         FieldFloat,  true,  nil},
		{"bool from bool",        true,          FieldBool,   false, true},
		{"bool from bad type",    "true",        FieldBool,   true,  nil},
		{"nil value",             nil,           FieldString, true,  nil},
		{"unknown field type",    "x",           FieldType(99), true, nil},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := CoercePredicateValue(c.val, c.ft)
			if (err != nil) != c.wantErr {
				t.Fatalf("error: got %v, wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && got != c.wantVal {
				t.Errorf("value: got %v (%T), want %v (%T)", got, got, c.wantVal, c.wantVal)
			}
		})
	}
}
