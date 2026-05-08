// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"strconv"
)

// FieldType identifies the expected type of a field for extraction.
type FieldType uint8

const (
	FieldString FieldType = iota
	FieldInt
	FieldFloat
	FieldBool
)

// FieldSpec describes a field to extract from a JSON object.
type FieldSpec struct {
	Name string    // JSON key name
	Atom Atom      // pre-computed atom for fast matching
	Type FieldType // expected value type
}

// MakeFieldSpec creates a FieldSpec, computing the Atom from the name.
func MakeFieldSpec(name string, typ FieldType) FieldSpec {
	return FieldSpec{
		Name: name,
		Atom: MakeAtom(name),
		Type: typ,
	}
}

// FieldExtractor walks a tokenised JSON object and populates a
// ColumnStore with only the specified fields. Unrecognised fields are
// skipped without allocation.
//
// The copyStrings parameter controls whether string values are copied
// from the input buffer. When true (production use), strings are heap-
// allocated and safe to use after the input buffer is recycled. When
// false (benchmark use), zero-copy unsafeString is used.
type FieldExtractor struct {
	Fields      []FieldSpec
	Registry    *AtomRegistry // optional, for collision verification
	CopyStrings bool          // if true, copy strings from input (safe for pooled buffers)

	// lookup is built once from Fields for fast atom matching
	lookup map[Atom]int // atom -> index in Fields
}

// NewFieldExtractor creates a new extractor for the given fields.
// If registry is non-nil, hashed atom matches (names > 8 bytes) are
// verified against the full string to detect collisions.
func NewFieldExtractor(fields []FieldSpec, registry *AtomRegistry, copyStrings bool) *FieldExtractor {
	lookup := make(map[Atom]int, len(fields))
	for i, f := range fields {
		lookup[f.Atom] = i
	}
	return &FieldExtractor{
		Fields:      fields,
		Registry:    registry,
		CopyStrings: copyStrings,
		lookup:      lookup,
	}
}

// Extract walks the tokens from a single JSON object and appends
// extracted field values to the appropriate columns in the store.
// After calling Extract for each row, call cs.IncrementRows().
//
// The method expects the token stream to start with TokObjStart.
// Nested objects and arrays are skipped correctly but not extracted.
func (fe *FieldExtractor) Extract(tok *Tokeniser, cs *ColumnStore) {
	tokens := tok.Tokens()
	input := tok.Input()
	n := len(tokens)

	// Expect ObjStart
	i := 0
	if i >= n || tokens[i].Type != TokObjStart {
		return
	}
	i++ // skip ObjStart

	for i < n {
		if tokens[i].Type == TokObjEnd {
			break
		}
		if tokens[i].Type == TokComma {
			i++
			continue
		}

		// Key
		if tokens[i].Type != TokString {
			i++
			continue
		}
		keyBytes := input[tokens[i].Start:tokens[i].End]
		keyAtom := MakeAtomBytes(keyBytes)
		i++ // past key

		// Colon
		if i < n && tokens[i].Type == TokColon {
			i++
		}

		// Value
		if i >= n {
			break
		}

		fieldIdx, wanted := fe.lookup[keyAtom]
		if wanted {
			// For hashed atoms (names > 8 bytes), verify the full string
			if fe.Registry != nil && fe.Registry.NeedsFullVerify(keyAtom) {
				if !fe.Registry.VerifyMatch(keyAtom, keyBytes) {
					wanted = false
				}
			}
		}

		if wanted {
			spec := fe.Fields[fieldIdx]
			switch spec.Type {
			case FieldString:
				if tokens[i].Type == TokString {
					var s string
					if fe.CopyStrings {
						// Safe copy: allocate a new string
						s = string(input[tokens[i].Start:tokens[i].End])
					} else {
						// Zero-copy: only valid while input is alive
						s = unsafeString(input[tokens[i].Start:tokens[i].End])
					}
					cs.Strings[spec.Atom] = append(cs.Strings[spec.Atom], s)
				}
			case FieldInt:
				if tokens[i].Type == TokNumber {
					v := parseIntFast(input[tokens[i].Start:tokens[i].End])
					cs.Ints[spec.Atom] = append(cs.Ints[spec.Atom], v)
				}
			case FieldFloat:
				if tokens[i].Type == TokNumber {
					v := parseFloatFast(input[tokens[i].Start:tokens[i].End])
					cs.Floats[spec.Atom] = append(cs.Floats[spec.Atom], v)
				}
			case FieldBool:
				if tokens[i].Type == TokTrue {
					cs.Bools[spec.Atom] = append(cs.Bools[spec.Atom], true)
				} else if tokens[i].Type == TokFalse {
					cs.Bools[spec.Atom] = append(cs.Bools[spec.Atom], false)
				}
			}
		}

		// Skip value (may be nested object/array)
		i = SkipValue(tokens, i)
	}
	cs.IncrementRows()
}

// ---------------------------------------------------------------------------
// Number parsing
// ---------------------------------------------------------------------------

// parseIntFast parses an integer from raw bytes without allocation.
func parseIntFast(b []byte) int64 {
	v, _ := strconv.ParseInt(unsafeString(b), 10, 64)
	return v
}

// parseFloatFast parses a float from raw bytes without allocation.
func parseFloatFast(b []byte) float64 {
	v, _ := strconv.ParseFloat(unsafeString(b), 64)
	return v
}

// ---------------------------------------------------------------------------
// Convenience: extract multiple rows
// ---------------------------------------------------------------------------

// ExtractRows tokenises and extracts fields from multiple JSON blobs
// into a single ColumnStore. This is the primary entry point for
// batch processing.
func ExtractRows(blobs [][]byte, fields []FieldSpec, registry *AtomRegistry, copyStrings bool) *ColumnStore {
	cs := NewColumnStore(len(blobs))
	fe := NewFieldExtractor(fields, registry, copyStrings)
	tok := GetTokeniser()
	defer PutTokeniser(tok)

	for _, blob := range blobs {
		if err := tok.Tokenise(blob); err != nil {
			continue // skip malformed JSON
		}
		fe.Extract(tok, cs)
	}

	return cs
}
