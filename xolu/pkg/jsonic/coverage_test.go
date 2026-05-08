// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"testing"
)

// ---------------------------------------------------------------------------
// Tokeniser
// ---------------------------------------------------------------------------

func TestTokeniser_SimpleObject_Basic(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	input := []byte(`{"name":"alice","age":30}`)
	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("tokenise: %v", err)
	}

	tokens := tok.Tokens()
	if tokens[0].Type != TokObjStart {
		t.Errorf("token[0]: want ObjStart, got %v", tokens[0].Type)
	}
	if tokens[len(tokens)-1].Type != TokObjEnd {
		t.Errorf("last token: want ObjEnd, got %v", tokens[len(tokens)-1].Type)
	}

	// "name" key
	if tokens[1].Type != TokString {
		t.Errorf("token[1]: want String, got %v", tokens[1].Type)
	}
	if tok.TokenString(tokens[1]) != "name" {
		t.Errorf("token[1] string: want name, got %q", tok.TokenString(tokens[1]))
	}
}

func TestTokeniser_AllScalarTypes(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	input := []byte(`{"s":"hello","n":42,"f":3.14,"b":true,"x":false,"z":null}`)
	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("tokenise: %v", err)
	}

	typeMap := map[string]TokenType{}
	tokens := tok.Tokens()
	for i := 1; i < len(tokens)-1; {
		if tokens[i].Type == TokComma {
			i++
			continue
		}
		if tokens[i].Type == TokString {
			key := tok.TokenString(tokens[i])
			i++                      // key
			i++                      // colon
			if i < len(tokens) {
				typeMap[key] = tokens[i].Type
			}
			i++
		} else {
			i++
		}
	}

	cases := map[string]TokenType{
		"s": TokString,
		"n": TokNumber,
		"f": TokNumber,
		"b": TokTrue,
		"x": TokFalse,
		"z": TokNull,
	}
	for k, want := range cases {
		if got, ok := typeMap[k]; !ok {
			t.Errorf("key %q not found", k)
		} else if got != want {
			t.Errorf("key %q: want %v, got %v", k, want, got)
		}
	}
}

func TestTokeniser_NestedObject_Depth(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	input := []byte(`{"a":{"b":1}}`)
	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	if tok.TokenCount() == 0 {
		t.Error("expected tokens for nested object")
	}
}

func TestTokeniser_Array_Basic(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	input := []byte(`[1,2,3]`)
	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("tokenise: %v", err)
	}
	tokens := tok.Tokens()
	if tokens[0].Type != TokArrStart {
		t.Errorf("want ArrStart, got %v", tokens[0].Type)
	}
}

func TestTokeniser_EscapedString_Basic(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	input := []byte(`{"k":"hello \"world\""}`)
	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("tokenise: %v", err)
	}
}

func TestTokeniser_NegativeNumber_Basic(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	input := []byte(`{"v":-99}`)
	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("tokenise: %v", err)
	}
}

func TestTokeniser_ErrorOnEmpty(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise([]byte{}); err == nil {
		t.Error("empty input should return error")
	}
}

func TestTokeniser_ErrorOnUnterminatedString(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise([]byte(`{"k":"unterminated`)); err == nil {
		t.Error("unterminated string should return error")
	}
}

func TestTokeniser_ErrorOnUnterminatedObject(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise([]byte(`{"k":1`)); err == nil {
		t.Error("unterminated object should return error")
	}
}

func TestTokeniser_Pool(t *testing.T) {
	tok := GetTokeniser()
	_ = tok.Tokenise([]byte(`{"x":1}`))
	PutTokeniser(tok)

	tok2 := GetTokeniser()
	defer PutTokeniser(tok2)
	if tok2.TokenCount() != 0 {
		t.Error("pooled tokeniser should reset token count")
	}
}

func TestSkipValue_Object(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`[{"a":1},2]`))
	tokens := tok.Tokens()
	// tokens[0]=ArrStart, tokens[1]=ObjStart; skip the object, land on comma, then number
	next := SkipValue(tokens, 1)
	// next points at TokComma; advance past it to reach the number
	if next < len(tokens) && tokens[next].Type == TokComma {
		next++
	}
	if tokens[next].Type != TokNumber {
		t.Errorf("after skipping object, want Number, got %v", tokens[next].Type)
	}
}

func TestSkipValue_Array(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`[[1,2],3]`))
	tokens := tok.Tokens()
	next := SkipValue(tokens, 1)
	if next < len(tokens) && tokens[next].Type == TokComma {
		next++
	}
	if tokens[next].Type != TokNumber {
		t.Errorf("after skipping nested array, want Number, got %v", tokens[next].Type)
	}
}

// ---------------------------------------------------------------------------
// FieldExtractor + ColumnStore
// ---------------------------------------------------------------------------

func TestFieldExtractor_ExtractsSpecifiedFields(t *testing.T) {
	fields := []FieldSpec{
		MakeFieldSpec("name", FieldString),
		MakeFieldSpec("age", FieldInt),
		MakeFieldSpec("score", FieldFloat),
		MakeFieldSpec("active", FieldBool),
	}
	fe := NewFieldExtractor(fields, nil, true)
	cs := NewColumnStore(1)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`{"name":"bob","age":25,"score":9.5,"active":true}`))
	fe.Extract(tok, cs)

	nameAtom := MakeAtom("name")
	ageAtom := MakeAtom("age")
	scoreAtom := MakeAtom("score")
	activeAtom := MakeAtom("active")

	if cs.Rows != 1 {
		t.Errorf("rows: want 1, got %d", cs.Rows)
	}
	if got := cs.Strings[nameAtom]; len(got) != 1 || got[0] != "bob" {
		t.Errorf("name: want [bob], got %v", got)
	}
	if got := cs.Ints[ageAtom]; len(got) != 1 || got[0] != 25 {
		t.Errorf("age: want [25], got %v", got)
	}
	if got := cs.Floats[scoreAtom]; len(got) != 1 || got[0] != 9.5 {
		t.Errorf("score: want [9.5], got %v", got)
	}
	if got := cs.Bools[activeAtom]; len(got) != 1 || !got[0] {
		t.Errorf("active: want [true], got %v", got)
	}
}

func TestFieldExtractor_IgnoresUnspecifiedFields(t *testing.T) {
	fields := []FieldSpec{MakeFieldSpec("name", FieldString)}
	fe := NewFieldExtractor(fields, nil, true)
	cs := NewColumnStore(1)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`{"name":"carol","extra":"ignored","num":99}`))
	fe.Extract(tok, cs)

	if cs.Rows != 1 {
		t.Errorf("rows: want 1, got %d", cs.Rows)
	}
	// Only name should be present.
	if len(cs.Ints) != 0 {
		t.Errorf("ints map should be empty, got %v", cs.Ints)
	}
}

func TestFieldExtractor_MultipleRows(t *testing.T) {
	blobs := [][]byte{
		[]byte(`{"id":1,"val":"a"}`),
		[]byte(`{"id":2,"val":"b"}`),
		[]byte(`{"id":3,"val":"c"}`),
	}
	fields := []FieldSpec{
		MakeFieldSpec("id", FieldInt),
		MakeFieldSpec("val", FieldString),
	}
	cs := ExtractRows(blobs, fields, nil, true)

	if cs.Rows != 3 {
		t.Errorf("rows: want 3, got %d", cs.Rows)
	}
	idAtom := MakeAtom("id")
	ids := cs.Ints[idAtom]
	if len(ids) != 3 || ids[0] != 1 || ids[1] != 2 || ids[2] != 3 {
		t.Errorf("ids: want [1,2,3], got %v", ids)
	}
}

func TestFieldExtractor_SkipsMalformedRows(t *testing.T) {
	blobs := [][]byte{
		[]byte(`{"id":1}`),
		[]byte(`not json`),
		[]byte(`{"id":3}`),
	}
	fields := []FieldSpec{MakeFieldSpec("id", FieldInt)}
	cs := ExtractRows(blobs, fields, nil, true)

	if cs.Rows != 2 {
		t.Errorf("rows: want 2 (malformed skipped), got %d", cs.Rows)
	}
}

func TestFieldExtractor_FalseBool(t *testing.T) {
	fields := []FieldSpec{MakeFieldSpec("flag", FieldBool)}
	fe := NewFieldExtractor(fields, nil, true)
	cs := NewColumnStore(1)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`{"flag":false}`))
	fe.Extract(tok, cs)

	flagAtom := MakeAtom("flag")
	if got := cs.Bools[flagAtom]; len(got) != 1 || got[0] {
		t.Errorf("flag: want [false], got %v", got)
	}
}

// ---------------------------------------------------------------------------
// ColumnStore aggregates
// ---------------------------------------------------------------------------

func TestColumnStore_SumIntFloat(t *testing.T) {
	if got := Sum([]int64{1, 2, 3, 4}); got != 10 {
		t.Errorf("Sum int: want 10, got %d", got)
	}
	if got := Sum([]float64{1.5, 2.5}); got != 4.0 {
		t.Errorf("Sum float: want 4.0, got %f", got)
	}
}

func TestColumnStore_Avg(t *testing.T) {
	if got := Avg([]int64{2, 4, 6}); got != 4.0 {
		t.Errorf("Avg: want 4.0, got %f", got)
	}
	if got := Avg([]int64{}); got != 0 {
		t.Errorf("Avg empty: want 0, got %f", got)
	}
}

func TestColumnStore_MinMax(t *testing.T) {
	col := []int64{5, 1, 9, 3}
	if got := Min(col); got != 1 {
		t.Errorf("Min: want 1, got %d", got)
	}
	if got := Max(col); got != 9 {
		t.Errorf("Max: want 9, got %d", got)
	}
}

func TestColumnStore_MinMaxEmpty(t *testing.T) {
	if got := Min([]int64{}); got != 0 {
		t.Errorf("Min empty: want 0, got %d", got)
	}
	if got := Max([]int64{}); got != 0 {
		t.Errorf("Max empty: want 0, got %d", got)
	}
}

func TestColumnStore_FilterIndices(t *testing.T) {
	col := []int64{1, 5, 2, 8, 3}
	idx := FilterIndices(col, func(v int64) bool { return v > 3 })
	if len(idx) != 2 || idx[0] != 1 || idx[1] != 3 {
		t.Errorf("FilterIndices: want [1,3], got %v", idx)
	}
}

func TestColumnStore_FilterIndicesString(t *testing.T) {
	col := []string{"a", "bb", "c", "dd"}
	idx := FilterIndicesString(col, func(s string) bool { return len(s) > 1 })
	if len(idx) != 2 || idx[0] != 1 || idx[1] != 3 {
		t.Errorf("FilterIndicesString: want [1,3], got %v", idx)
	}
}

func TestColumnStore_Gather(t *testing.T) {
	col := []string{"a", "b", "c", "d"}
	got := Gather(col, []int{0, 2})
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("Gather: want [a,c], got %v", got)
	}
}

func TestColumnStore_SortIndicesBy(t *testing.T) {
	col := []int64{30, 10, 20}
	idx := AllIndices(3)
	SortIndicesBy(col, idx, false)
	if idx[0] != 1 || idx[1] != 2 || idx[2] != 0 {
		t.Errorf("SortIndicesBy asc: want [1,2,0], got %v", idx)
	}
	SortIndicesBy(col, idx, true)
	if idx[0] != 0 || idx[1] != 2 || idx[2] != 1 {
		t.Errorf("SortIndicesBy desc: want [0,2,1], got %v", idx)
	}
}

func TestColumnStore_GroupSum(t *testing.T) {
	groups := GroupSum([]string{"a", "b", "a"}, []int64{1, 2, 3})
	if groups["a"] != 4 || groups["b"] != 2 {
		t.Errorf("GroupSum: want a=4 b=2, got %v", groups)
	}
}

func TestColumnStore_GroupCount(t *testing.T) {
	counts := GroupCount([]string{"x", "y", "x", "x"})
	if counts["x"] != 3 || counts["y"] != 1 {
		t.Errorf("GroupCount: want x=3 y=1, got %v", counts)
	}
}

func TestColumnStore_DistinctString(t *testing.T) {
	got := DistinctString([]string{"b", "a", "b", "c", "a"})
	if len(got) != 3 || got[0] != "b" || got[1] != "a" || got[2] != "c" {
		t.Errorf("DistinctString: want [b,a,c], got %v", got)
	}
}

func TestColumnStore_ToMaps(t *testing.T) {
	cs := NewColumnStore(2)
	nameAtom := MakeAtom("name")
	ageAtom := MakeAtom("age")
	cs.Strings[nameAtom] = []string{"alice", "bob"}
	cs.Ints[ageAtom] = []int64{30, 25}
	cs.Rows = 2

	nameMap := map[Atom]string{
		nameAtom: "name",
		ageAtom:  "age",
	}
	rows := cs.ToMaps(nameMap)
	if len(rows) != 2 {
		t.Fatalf("ToMaps: want 2 rows, got %d", len(rows))
	}
	if rows[0]["name"] != "alice" || rows[1]["name"] != "bob" {
		t.Errorf("names: %v %v", rows[0]["name"], rows[1]["name"])
	}
}

func TestColumnStore_ToMapsEmpty(t *testing.T) {
	cs := NewColumnStore(0)
	rows := cs.ToMaps(map[Atom]string{})
	if len(rows) != 0 {
		t.Errorf("ToMaps empty: want 0, got %d", len(rows))
	}
}

// ---------------------------------------------------------------------------
// FilterExtractFromTokens
// ---------------------------------------------------------------------------

func TestFilterExtractFromTokens_NoPreds_ExtractsAll(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`{"name":"dave","age":40}`))
	fields := MakeFilterFieldEntries([]string{"name", "age"})
	result := FilterExtractFromTokens(tok, fields, nil)

	if !result.Passed {
		t.Error("no predicates: Passed should be true")
	}
	if result.Data["name"] != "dave" {
		t.Errorf("name: want dave, got %v", result.Data["name"])
	}
}

func TestFilterExtractFromTokens_MatchingPred(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`{"status":"active","score":10}`))
	fields := MakeFilterFieldEntries([]string{"status"})

	ps := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("status", FieldString, OpEq, "active"),
	})

	result := FilterExtractFromTokens(tok, fields, ps)
	if !result.Passed {
		t.Error("matching predicate: Passed should be true")
	}
}

func TestFilterExtractFromTokens_NonMatchingPred(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	_ = tok.Tokenise([]byte(`{"status":"inactive","score":10}`))
	fields := MakeFilterFieldEntries([]string{"status"})

	ps := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("status", FieldString, OpEq, "active"),
	})

	result := FilterExtractFromTokens(tok, fields, ps)
	if result.Passed {
		t.Error("non-matching predicate: Passed should be false")
	}
	if result.Data != nil {
		t.Error("Data map should not be allocated when predicate fails")
	}
}

func TestFilterExtractFromTokens_MissingPredFieldFails(t *testing.T) {
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	// status field is absent
	_ = tok.Tokenise([]byte(`{"name":"eve"}`))
	fields := MakeFilterFieldEntries([]string{"name"})

	ps := NewPredicateSet([]FieldPredicate{
		MakeFieldPredicate("status", FieldString, OpEq, "active"),
	})

	result := FilterExtractFromTokens(tok, fields, ps)
	if result.Passed {
		t.Error("absent predicate field should fail (closed-world)")
	}
}

func TestMakeFilterFieldEntries(t *testing.T) {
	entries := MakeFilterFieldEntries([]string{"a", "b", "c"})
	if len(entries) != 3 {
		t.Fatalf("want 3 entries, got %d", len(entries))
	}
	for i, name := range []string{"a", "b", "c"} {
		if entries[i].Name != name {
			t.Errorf("entry %d name: want %q, got %q", i, name, entries[i].Name)
		}
		if entries[i].Atom != MakeAtom(name) {
			t.Errorf("entry %d atom mismatch", i)
		}
	}
}
