// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"testing"
)

// ============================================================================
// Test data generation (matches columnar-bench probe schema)
// ============================================================================

var regions = []string{"north", "south", "east", "west"}
var statuses = []string{"active", "maintenance", "decommissioned", "transit"}
var categories = []string{"sensor", "actuator", "controller", "gateway", "display"}

func generateJSONBlobs(n int, seed int64) [][]byte {
	rng := rand.New(rand.NewSource(seed))
	blobs := make([][]byte, n)
	for i := 0; i < n; i++ {
		blob := fmt.Sprintf(
			`{"id":%d,"name":"asset-%05d","region":"%s","status":"%s","category":"%s","temperature":%.2f,"humidity":%.2f,"pressure":%.1f,"battery":%d,"signal_strength":%d,"firmware":"v%d.%d.%d","active":%s}`,
			i+1,
			i+1,
			regions[rng.Intn(len(regions))],
			statuses[rng.Intn(len(statuses))],
			categories[rng.Intn(len(categories))],
			20.0+rng.Float64()*30.0,
			30.0+rng.Float64()*60.0,
			980.0+rng.Float64()*40.0,
			rng.Intn(100),
			-90+rng.Intn(60),
			rng.Intn(5)+1, rng.Intn(10), rng.Intn(20),
			[]string{"true", "false"}[rng.Intn(2)],
		)
		blobs[i] = []byte(blob)
	}
	return blobs
}

// Field specs for different test scenarios
var queryFields = []FieldSpec{
	MakeFieldSpec("region", FieldString),
	MakeFieldSpec("temperature", FieldFloat),
	MakeFieldSpec("battery", FieldInt),
}

var allFields = []FieldSpec{
	MakeFieldSpec("id", FieldInt),
	MakeFieldSpec("name", FieldString),
	MakeFieldSpec("region", FieldString),
	MakeFieldSpec("status", FieldString),
	MakeFieldSpec("category", FieldString),
	MakeFieldSpec("temperature", FieldFloat),
	MakeFieldSpec("humidity", FieldFloat),
	MakeFieldSpec("pressure", FieldFloat),
	MakeFieldSpec("battery", FieldInt),
	MakeFieldSpec("signal_strength", FieldInt),
	MakeFieldSpec("firmware", FieldString),
	MakeFieldSpec("active", FieldBool),
}

// ============================================================================
// Atom tests
// ============================================================================

func TestAtom_ShortName(t *testing.T) {
	// Names <= 8 bytes: packed directly, bijective
	a1 := MakeAtom("id")
	a2 := MakeAtom("id")
	if a1 != a2 {
		t.Errorf("same name should produce same atom: %d != %d", a1, a2)
	}

	a3 := MakeAtom("name")
	if a1 == a3 {
		t.Errorf("different names should produce different atoms")
	}

	// Exactly 8 bytes: still packed
	a4 := MakeAtom("firmware")
	a5 := MakeAtom("firmware")
	if a4 != a5 {
		t.Errorf("8-byte name should be consistent: %d != %d", a4, a5)
	}
}

func TestAtom_LongName(t *testing.T) {
	// Names > 8 bytes: FNV-1a hashed
	a1 := MakeAtom("temperature")
	a2 := MakeAtom("temperature")
	if a1 != a2 {
		t.Errorf("same long name should produce same atom")
	}

	a3 := MakeAtom("signal_strength")
	if a1 == a3 {
		t.Errorf("different long names should (almost certainly) produce different atoms")
	}
}

func TestAtom_BytesMatchString(t *testing.T) {
	s := "battery"
	a1 := MakeAtom(s)
	a2 := MakeAtomBytes([]byte(s))
	if a1 != a2 {
		t.Errorf("MakeAtom and MakeAtomBytes should agree: %d != %d", a1, a2)
	}

	s2 := "temperature"
	a3 := MakeAtom(s2)
	a4 := MakeAtomBytes([]byte(s2))
	if a3 != a4 {
		t.Errorf("long name: MakeAtom and MakeAtomBytes should agree: %d != %d", a3, a4)
	}
}

func TestAtom_MatchBytes(t *testing.T) {
	a := MakeAtom("region")
	if !a.MatchBytes([]byte("region")) {
		t.Error("MatchBytes should return true for matching bytes")
	}
	if a.MatchBytes([]byte("status")) {
		t.Error("MatchBytes should return false for non-matching bytes")
	}
}

// ============================================================================
// AtomRegistry tests
// ============================================================================

func TestAtomRegistry_RegisterAndLookup(t *testing.T) {
	reg := NewAtomRegistry()

	a1, err := reg.Register("temperature")
	if err != nil {
		t.Fatalf("Register: %v", err)
	}

	a2, ok := reg.Lookup("temperature")
	if !ok {
		t.Fatal("Lookup should find registered name")
	}
	if a1 != a2 {
		t.Errorf("Register and Lookup should return same atom")
	}

	_, ok = reg.Lookup("nonexistent")
	if ok {
		t.Error("Lookup should return false for unregistered name")
	}
}

func TestAtomRegistry_DuplicateRegister(t *testing.T) {
	reg := NewAtomRegistry()

	a1, _ := reg.Register("region")
	a2, _ := reg.Register("region")
	if a1 != a2 {
		t.Error("duplicate registration should return same atom")
	}
}

func TestAtomRegistry_NeedsFullVerify(t *testing.T) {
	reg := NewAtomRegistry()

	reg.Register("id")          // <= 8 bytes
	reg.Register("temperature") // > 8 bytes

	if reg.NeedsFullVerify(MakeAtom("id")) {
		t.Error("short name should not need full verification")
	}
	if !reg.NeedsFullVerify(MakeAtom("temperature")) {
		t.Error("long name should need full verification")
	}
}

func TestAtomRegistry_VerifyMatch(t *testing.T) {
	reg := NewAtomRegistry()
	reg.Register("temperature")

	a := MakeAtom("temperature")
	if !reg.VerifyMatch(a, []byte("temperature")) {
		t.Error("VerifyMatch should succeed for correct name")
	}
}

// ============================================================================
// Tokeniser tests
// ============================================================================

func TestTokeniser_SimpleObject(t *testing.T) {
	input := []byte(`{"name":"alice","age":30}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	tokens := tok.Tokens()
	// Expected: ObjStart, "name", Colon, "alice", Comma, "age", Colon, 30, ObjEnd
	if len(tokens) != 9 {
		t.Fatalf("expected 9 tokens, got %d", len(tokens))
	}
	if tokens[0].Type != TokObjStart {
		t.Errorf("token 0: expected ObjStart, got %d", tokens[0].Type)
	}
	if tokens[1].Type != TokString {
		t.Errorf("token 1: expected String, got %d", tokens[1].Type)
	}
	if tok.TokenString(tokens[1]) != "name" {
		t.Errorf("token 1: expected 'name', got %q", tok.TokenString(tokens[1]))
	}
	if tokens[3].Type != TokString {
		t.Errorf("token 3: expected String, got %d", tokens[3].Type)
	}
	if tok.TokenString(tokens[3]) != "alice" {
		t.Errorf("token 3: expected 'alice', got %q", tok.TokenString(tokens[3]))
	}
	if tokens[7].Type != TokNumber {
		t.Errorf("token 7: expected Number, got %d", tokens[7].Type)
	}
	if tok.TokenString(tokens[7]) != "30" {
		t.Errorf("token 7: expected '30', got %q", tok.TokenString(tokens[7]))
	}
}

func TestTokeniser_AllTypes(t *testing.T) {
	input := []byte(`{"s":"text","n":42,"f":3.14,"t":true,"b":false,"nil":null}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	// Check value token types
	tokens := tok.Tokens()
	types := make(map[string]TokenType)
	for i := 0; i < len(tokens); i++ {
		if tokens[i].Type == TokString && i+2 < len(tokens) && tokens[i+1].Type == TokColon {
			key := tok.TokenString(tokens[i])
			types[key] = tokens[i+2].Type
		}
	}

	checks := map[string]TokenType{
		"s":   TokString,
		"n":   TokNumber,
		"f":   TokNumber,
		"t":   TokTrue,
		"b":   TokFalse,
		"nil": TokNull,
	}
	for key, expected := range checks {
		if got, ok := types[key]; !ok {
			t.Errorf("missing key %q", key)
		} else if got != expected {
			t.Errorf("key %q: expected token type %d, got %d", key, expected, got)
		}
	}
}

func TestTokeniser_NestedObject(t *testing.T) {
	input := []byte(`{"outer":{"inner":"val"},"after":1}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	// Verify we can tokenise without error
	if tok.TokenCount() < 5 {
		t.Errorf("expected more than 5 tokens for nested object, got %d", tok.TokenCount())
	}
}

func TestTokeniser_Array(t *testing.T) {
	input := []byte(`{"items":[1,2,3]}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	// Should contain ArrStart, numbers, ArrEnd
	found := false
	for _, tk := range tok.Tokens() {
		if tk.Type == TokArrStart {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected ArrStart token for array value")
	}
}

func TestTokeniser_EscapedString(t *testing.T) {
	input := []byte(`{"msg":"hello \"world\""}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	// The value token should include the escape sequences
	tokens := tok.Tokens()
	// ObjStart, "msg", Colon, <value>, ObjEnd
	if len(tokens) < 5 {
		t.Fatalf("expected at least 5 tokens, got %d", len(tokens))
	}
	val := tok.TokenString(tokens[3])
	if val != `hello \"world\"` {
		t.Errorf("escaped string: expected %q, got %q", `hello \"world\"`, val)
	}
}

func TestTokeniser_NegativeNumber(t *testing.T) {
	input := []byte(`{"temp":-42.5}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	tokens := tok.Tokens()
	// ObjStart, "temp", Colon, -42.5, ObjEnd
	if tokens[3].Type != TokNumber {
		t.Errorf("expected Number token, got %d", tokens[3].Type)
	}
	if tok.TokenString(tokens[3]) != "-42.5" {
		t.Errorf("expected '-42.5', got %q", tok.TokenString(tokens[3]))
	}
}

func TestTokeniser_ScientificNotation(t *testing.T) {
	input := []byte(`{"val":1.5e10}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}

	tokens := tok.Tokens()
	if tokens[3].Type != TokNumber {
		t.Errorf("expected Number token, got %d", tokens[3].Type)
	}
	if tok.TokenString(tokens[3]) != "1.5e10" {
		t.Errorf("expected '1.5e10', got %q", tok.TokenString(tokens[3]))
	}
}

func TestTokeniser_EmptyObject(t *testing.T) {
	input := []byte(`{}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}
	if tok.TokenCount() != 2 {
		t.Errorf("expected 2 tokens for {}, got %d", tok.TokenCount())
	}
}

func TestTokeniser_EmptyArray(t *testing.T) {
	input := []byte(`{"arr":[]}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}
}

func TestTokeniser_Whitespace(t *testing.T) {
	input := []byte(`  {  "key"  :  "value"  }  `)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	if err := tok.Tokenise(input); err != nil {
		t.Fatalf("Tokenise: %v", err)
	}
	if tok.TokenCount() != 5 {
		t.Errorf("expected 5 tokens, got %d", tok.TokenCount())
	}
}

func TestTokeniser_MalformedInput(t *testing.T) {
	cases := []string{
		``,
		`{`,
		`{"key":}`,
		`{"key"`,
		`{"key":"val"`,
	}
	for _, input := range cases {
		tok := GetTokeniser()
		err := tok.Tokenise([]byte(input))
		if err == nil {
			t.Errorf("expected error for malformed input %q", input)
		}
		PutTokeniser(tok)
	}
}

func TestTokeniser_PoolReuse(t *testing.T) {
	// Verify that pooled tokenisers produce correct results
	for i := 0; i < 100; i++ {
		tok := GetTokeniser()
		input := []byte(fmt.Sprintf(`{"i":%d}`, i))
		if err := tok.Tokenise(input); err != nil {
			t.Fatalf("iteration %d: %v", i, err)
		}
		if tok.TokenCount() != 5 {
			t.Errorf("iteration %d: expected 5 tokens, got %d", i, tok.TokenCount())
		}
		PutTokeniser(tok)
	}
}

// ============================================================================
// SkipValue tests
// ============================================================================

func TestSkipValue_Primitives(t *testing.T) {
	input := []byte(`{"a":1,"b":"s","c":true,"d":null}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	tok.Tokenise(input)

	tokens := tok.Tokens()
	// Find the number token (value of "a") and skip it
	// ObjStart(0), "a"(1), :(2), 1(3), ,(4), "b"(5), ...
	next := SkipValue(tokens, 3)
	if next != 4 {
		t.Errorf("skip number: expected index 4, got %d", next)
	}
}

func TestSkipValue_NestedObject(t *testing.T) {
	input := []byte(`{"a":{"x":1,"y":2},"b":3}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	tok.Tokenise(input)

	tokens := tok.Tokens()
	// Find ObjStart of inner object (value of "a")
	// ObjStart(0), "a"(1), :(2), ObjStart(3), "x"(4), :(5), 1(6), ,(7), "y"(8), :(9), 2(10), ObjEnd(11), ,(12), "b"(13)...
	next := SkipValue(tokens, 3)
	if next != 12 {
		t.Errorf("skip nested object: expected index 12, got %d", next)
	}
	// Token at 12 should be comma
	if tokens[12].Type != TokComma {
		t.Errorf("token after nested object should be comma, got %d", tokens[12].Type)
	}
}

func TestSkipValue_NestedArray(t *testing.T) {
	input := []byte(`{"a":[1,[2,3],4],"b":5}`)
	tok := GetTokeniser()
	defer PutTokeniser(tok)
	tok.Tokenise(input)

	tokens := tok.Tokens()
	// Find ArrStart (value of "a") — it's token index 3
	next := SkipValue(tokens, 3)
	// After array: should be comma before "b"
	if tokens[next].Type != TokComma {
		t.Errorf("token after nested array should be comma, got type %d at index %d", tokens[next].Type, next)
	}
}

// ============================================================================
// Field extraction tests
// ============================================================================

func TestExtractFields_SelectiveExtraction(t *testing.T) {
	blobs := generateJSONBlobs(100, 42)

	cs := ExtractRows(blobs, queryFields, nil, true)

	regionAtom := MakeAtom("region")
	tempAtom := MakeAtom("temperature")
	battAtom := MakeAtom("battery")

	if cs.Rows != 100 {
		t.Errorf("expected 100 rows, got %d", cs.Rows)
	}
	if len(cs.Strings[regionAtom]) != 100 {
		t.Errorf("expected 100 region values, got %d", len(cs.Strings[regionAtom]))
	}
	if len(cs.Floats[tempAtom]) != 100 {
		t.Errorf("expected 100 temperature values, got %d", len(cs.Floats[tempAtom]))
	}
	if len(cs.Ints[battAtom]) != 100 {
		t.Errorf("expected 100 battery values, got %d", len(cs.Ints[battAtom]))
	}

	// Should NOT have extracted other fields
	if len(cs.Strings) > 1 {
		t.Errorf("selective extraction should only have 1 string column, got %d", len(cs.Strings))
	}
	if len(cs.Floats) > 1 {
		t.Errorf("selective extraction should only have 1 float column, got %d", len(cs.Floats))
	}
}

func TestExtractFields_AllFields(t *testing.T) {
	blobs := generateJSONBlobs(50, 42)

	cs := ExtractRows(blobs, allFields, nil, true)

	if cs.Rows != 50 {
		t.Errorf("expected 50 rows, got %d", cs.Rows)
	}

	// Check we got all column types
	idAtom := MakeAtom("id")
	nameAtom := MakeAtom("name")
	activeAtom := MakeAtom("active")

	if len(cs.Ints[idAtom]) != 50 {
		t.Errorf("expected 50 id values, got %d", len(cs.Ints[idAtom]))
	}
	if len(cs.Strings[nameAtom]) != 50 {
		t.Errorf("expected 50 name values, got %d", len(cs.Strings[nameAtom]))
	}
	if len(cs.Bools[activeAtom]) != 50 {
		t.Errorf("expected 50 active values, got %d", len(cs.Bools[activeAtom]))
	}
}

func TestExtractFields_WithRegistry(t *testing.T) {
	reg := NewAtomRegistry()
	for _, f := range allFields {
		reg.MustRegister(f.Name)
	}

	blobs := generateJSONBlobs(10, 42)
	cs := NewColumnStore(10)
	fe := NewFieldExtractor(allFields, reg, true)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	for _, blob := range blobs {
		tok.Tokenise(blob)
		fe.Extract(tok, cs)
	}

	if cs.Rows != 10 {
		t.Errorf("expected 10 rows, got %d", cs.Rows)
	}
}

func TestExtractFields_CopyStrings(t *testing.T) {
	// Verify that CopyStrings=true produces independent strings
	input := []byte(`{"name":"alice"}`)
	fields := []FieldSpec{MakeFieldSpec("name", FieldString)}

	cs := NewColumnStore(1)
	fe := NewFieldExtractor(fields, nil, true)
	tok := GetTokeniser()

	tok.Tokenise(input)
	fe.Extract(tok, cs)
	PutTokeniser(tok) // returns tokeniser, clearing input reference

	nameAtom := MakeAtom("name")
	if len(cs.Strings[nameAtom]) != 1 {
		t.Fatalf("expected 1 name, got %d", len(cs.Strings[nameAtom]))
	}
	// With CopyStrings=true, the string should still be valid
	if cs.Strings[nameAtom][0] != "alice" {
		t.Errorf("expected 'alice', got %q", cs.Strings[nameAtom][0])
	}
}

func TestExtractFields_NestedObjectSkipped(t *testing.T) {
	input := []byte(`{"name":"test","nested":{"a":1},"value":42}`)
	fields := []FieldSpec{
		MakeFieldSpec("name", FieldString),
		MakeFieldSpec("value", FieldInt),
	}

	cs := ExtractRows([][]byte{input}, fields, nil, true)

	nameAtom := MakeAtom("name")
	valueAtom := MakeAtom("value")

	if cs.Rows != 1 {
		t.Fatalf("expected 1 row, got %d", cs.Rows)
	}
	if cs.Strings[nameAtom][0] != "test" {
		t.Errorf("expected 'test', got %q", cs.Strings[nameAtom][0])
	}
	if cs.Ints[valueAtom][0] != 42 {
		t.Errorf("expected 42, got %d", cs.Ints[valueAtom][0])
	}
}

func TestExtractFields_ArraySkipped(t *testing.T) {
	input := []byte(`{"name":"test","tags":["a","b","c"],"value":7}`)
	fields := []FieldSpec{
		MakeFieldSpec("name", FieldString),
		MakeFieldSpec("value", FieldInt),
	}

	cs := ExtractRows([][]byte{input}, fields, nil, true)

	valueAtom := MakeAtom("value")
	if cs.Ints[valueAtom][0] != 7 {
		t.Errorf("value after array should be 7, got %d", cs.Ints[valueAtom][0])
	}
}

func TestExtractFields_MalformedBlobSkipped(t *testing.T) {
	blobs := [][]byte{
		[]byte(`{"name":"good","val":1}`),
		[]byte(`{broken json`),
		[]byte(`{"name":"also_good","val":2}`),
	}
	fields := []FieldSpec{
		MakeFieldSpec("name", FieldString),
		MakeFieldSpec("val", FieldInt),
	}

	cs := ExtractRows(blobs, fields, nil, true)

	if cs.Rows != 2 {
		t.Errorf("expected 2 rows (skipping malformed), got %d", cs.Rows)
	}
}

// ============================================================================
// ColumnStore operations tests
// ============================================================================

func TestColumnOps_Sum(t *testing.T) {
	col := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	if got := Sum(col); got != 15.0 {
		t.Errorf("Sum: expected 15.0, got %f", got)
	}

	icol := []int64{10, 20, 30}
	if got := Sum(icol); got != 60 {
		t.Errorf("Sum int64: expected 60, got %d", got)
	}
}

func TestColumnOps_Avg(t *testing.T) {
	col := []float64{10.0, 20.0, 30.0}
	if got := Avg(col); got != 20.0 {
		t.Errorf("Avg: expected 20.0, got %f", got)
	}

	if got := Avg([]float64{}); got != 0 {
		t.Errorf("Avg empty: expected 0, got %f", got)
	}
}

func TestColumnOps_MinMax(t *testing.T) {
	col := []float64{3.0, 1.0, 4.0, 1.5, 9.0, 2.0}
	if got := Min(col); got != 1.0 {
		t.Errorf("Min: expected 1.0, got %f", got)
	}
	if got := Max(col); got != 9.0 {
		t.Errorf("Max: expected 9.0, got %f", got)
	}
}

func TestColumnOps_FilterIndices(t *testing.T) {
	col := []int64{10, 25, 30, 45, 50, 65, 70}
	idx := FilterIndices(col, func(v int64) bool { return v > 40 })
	// >40: 45(3), 50(4), 65(5), 70(6) = 4 indices
	if len(idx) != 4 {
		t.Errorf("expected 4 indices for >40, got %d: %v", len(idx), idx)
	}
}

func TestColumnOps_Gather(t *testing.T) {
	col := []string{"a", "b", "c", "d", "e"}
	idx := []int{1, 3}
	got := Gather(col, idx)
	if len(got) != 2 || got[0] != "b" || got[1] != "d" {
		t.Errorf("Gather: expected [b d], got %v", got)
	}
}

func TestColumnOps_SortIndicesBy(t *testing.T) {
	col := []float64{3.0, 1.0, 4.0, 1.5, 2.0}
	idx := AllIndices(len(col))

	SortIndicesBy(col, idx, false) // ascending
	if idx[0] != 1 || idx[len(idx)-1] != 2 {
		t.Errorf("ascending sort: expected first=1 last=2, got first=%d last=%d", idx[0], idx[len(idx)-1])
	}

	idx = AllIndices(len(col))
	SortIndicesBy(col, idx, true) // descending
	if idx[0] != 2 {
		t.Errorf("descending sort: expected first=2, got %d", idx[0])
	}
}

func TestColumnOps_GroupSum(t *testing.T) {
	groups := []string{"a", "b", "a", "b", "a"}
	vals := []float64{1.0, 2.0, 3.0, 4.0, 5.0}
	result := GroupSum(groups, vals)

	if math.Abs(result["a"]-9.0) > 0.001 {
		t.Errorf("GroupSum a: expected 9.0, got %f", result["a"])
	}
	if math.Abs(result["b"]-6.0) > 0.001 {
		t.Errorf("GroupSum b: expected 6.0, got %f", result["b"])
	}
}

func TestColumnOps_GroupCount(t *testing.T) {
	groups := []string{"a", "b", "a", "c", "a", "b"}
	result := GroupCount(groups)
	if result["a"] != 3 || result["b"] != 2 || result["c"] != 1 {
		t.Errorf("GroupCount: expected a=3 b=2 c=1, got %v", result)
	}
}

func TestColumnOps_DistinctString(t *testing.T) {
	col := []string{"a", "b", "a", "c", "b", "a"}
	got := DistinctString(col)
	if len(got) != 3 {
		t.Errorf("expected 3 distinct values, got %d", len(got))
	}
	// Should preserve first-occurrence order
	if got[0] != "a" || got[1] != "b" || got[2] != "c" {
		t.Errorf("expected [a b c], got %v", got)
	}
}

// ============================================================================
// ToMaps tests
// ============================================================================

func TestToMaps(t *testing.T) {
	blobs := generateJSONBlobs(5, 42)
	cs := ExtractRows(blobs, queryFields, nil, true)

	nameMap := make(map[Atom]string)
	for _, f := range queryFields {
		nameMap[f.Atom] = f.Name
	}

	maps := cs.ToMaps(nameMap)
	if len(maps) != 5 {
		t.Fatalf("expected 5 maps, got %d", len(maps))
	}

	for i, m := range maps {
		if _, ok := m["region"]; !ok {
			t.Errorf("row %d: missing 'region'", i)
		}
		if _, ok := m["temperature"]; !ok {
			t.Errorf("row %d: missing 'temperature'", i)
		}
		if _, ok := m["battery"]; !ok {
			t.Errorf("row %d: missing 'battery'", i)
		}
	}
}

// ============================================================================
// Correctness: columnar vs json.Unmarshal comparison
// ============================================================================

func TestCorrectness_VsUnmarshal(t *testing.T) {
	blobs := generateJSONBlobs(200, 42)

	// Map path (reference)
	var maps []map[string]interface{}
	for _, blob := range blobs {
		var m map[string]interface{}
		if err := json.Unmarshal(blob, &m); err == nil {
			maps = append(maps, m)
		}
	}

	// Columnar path
	cs := ExtractRows(blobs, allFields, nil, true)

	tempAtom := MakeAtom("temperature")
	battAtom := MakeAtom("battery")
	regionAtom := MakeAtom("region")

	// Compare Sum(temperature)
	var mapSum float64
	for _, m := range maps {
		if v, ok := m["temperature"].(float64); ok {
			mapSum += v
		}
	}
	colSum := Sum(cs.Floats[tempAtom])
	if math.Abs(mapSum-colSum) > 0.01 {
		t.Errorf("SUM mismatch: map=%.4f col=%.4f", mapSum, colSum)
	}

	// Compare Avg(temperature)
	mapAvg := mapSum / float64(len(maps))
	colAvg := Avg(cs.Floats[tempAtom])
	if math.Abs(mapAvg-colAvg) > 0.01 {
		t.Errorf("AVG mismatch: map=%.4f col=%.4f", mapAvg, colAvg)
	}

	// Compare Filter(battery > 50) count
	var mapFilterCount int
	for _, m := range maps {
		if v, ok := m["battery"].(float64); ok && v > 50 {
			mapFilterCount++
		}
	}
	colFilterIdx := FilterIndices(cs.Ints[battAtom], func(v int64) bool { return v > 50 })
	if mapFilterCount != len(colFilterIdx) {
		t.Errorf("FILTER count mismatch: map=%d col=%d", mapFilterCount, len(colFilterIdx))
	}

	// Compare GroupSum(region, temperature)
	mapGroups := make(map[string]float64)
	for _, m := range maps {
		key, _ := m["region"].(string)
		val, _ := m["temperature"].(float64)
		mapGroups[key] += val
	}
	colGroups := GroupSum(cs.Strings[regionAtom], cs.Floats[tempAtom])
	for k, mv := range mapGroups {
		cv := colGroups[k]
		if math.Abs(mv-cv) > 0.01 {
			t.Errorf("GROUP SUM[%s] mismatch: map=%.4f col=%.4f", k, mv, cv)
		}
	}
}

// ============================================================================
// Benchmarks: columnar vs json.Unmarshal
// ============================================================================

func BenchmarkUnmarshalParse(b *testing.B) {
	blobs := generateJSONBlobs(1000, 42)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		for _, blob := range blobs {
			var m map[string]interface{}
			json.Unmarshal(blob, &m)
		}
	}
}

func BenchmarkJsonicParse3Fields(b *testing.B) {
	blobs := generateJSONBlobs(1000, 42)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ExtractRows(blobs, queryFields, nil, false)
	}
}

func BenchmarkJsonicParseAllFields(b *testing.B) {
	blobs := generateJSONBlobs(1000, 42)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = ExtractRows(blobs, allFields, nil, false)
	}
}

func BenchmarkUnmarshalFilterSumGroup(b *testing.B) {
	blobs := generateJSONBlobs(1000, 42)
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		var maps []map[string]interface{}
		for _, blob := range blobs {
			var m map[string]interface{}
			if err := json.Unmarshal(blob, &m); err == nil {
				maps = append(maps, m)
			}
		}
		// Filter battery > 50
		var filtered []map[string]interface{}
		for _, m := range maps {
			if v, ok := m["battery"].(float64); ok && v > 50 {
				filtered = append(filtered, m)
			}
		}
		// GroupSum region x temperature
		groups := make(map[string]float64)
		for _, m := range filtered {
			key, _ := m["region"].(string)
			val, _ := m["temperature"].(float64)
			groups[key] += val
		}
		_ = groups
	}
}

func BenchmarkJsonicFilterSumGroup(b *testing.B) {
	blobs := generateJSONBlobs(1000, 42)
	battAtom := MakeAtom("battery")
	regionAtom := MakeAtom("region")
	tempAtom := MakeAtom("temperature")
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		cs := ExtractRows(blobs, queryFields, nil, false)
		idx := FilterIndices(cs.Ints[battAtom], func(v int64) bool { return v > 50 })
		regions := Gather(cs.Strings[regionAtom], idx)
		temps := Gather(cs.Floats[tempAtom], idx)
		_ = GroupSum(regions, temps)
	}
}
