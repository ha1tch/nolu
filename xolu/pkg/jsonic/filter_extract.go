// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import "encoding/json"

// FilterResult holds the outcome of a filtered extraction pass.
type FilterResult struct {
	Passed bool                   // true if all predicates matched
	Data   map[string]interface{} // extracted fields (only populated if Passed)
}

// FilterExtractFromTokens walks a tokenised JSON object, extracts the
// requested fields, and evaluates the predicate set in a single pass.
//
// The walk extracts both predicate fields and output fields as they are
// encountered (JSON key order is arbitrary). At the end of the object,
// all predicates are evaluated. If any predicate field was not found in
// the object, the predicate is treated as not matching (closed-world).
//
// This avoids allocating the output map entirely for rows that fail
// the predicate — the main performance win of B4.
//
// Parameters:
//   - tok: tokeniser with a completed Tokenise() call
//   - outputFields: the SELECT fields to extract (FilterFieldEntry-style)
//   - preds: predicate set to evaluate (may be nil for no filtering)
//
// Returns a FilterResult. If preds is nil, Passed is always true.
func FilterExtractFromTokens(
	tok *Tokeniser,
	outputFields []FilterFieldEntry,
	preds *PredicateSet,
) FilterResult {
	tokens := tok.Tokens()
	input := tok.Input()
	n := len(tokens)

	noPreds := preds == nil || preds.Len() == 0

	// Track predicate evaluation state.
	var predSeen []bool
	var predMatched []bool
	if !noPreds {
		predSeen = make([]bool, preds.Len())
		predMatched = make([]bool, preds.Len())
	}

	// Collect output field values lazily. We defer map allocation until
	// we know the predicates pass (or there are no predicates).
	type pendingField struct {
		name     string
		tokenIdx int
	}
	pending := make([]pendingField, 0, len(outputFields))

	// Build output field atom lookup.
	outputLookup := make(map[Atom]string, len(outputFields))
	for _, f := range outputFields {
		outputLookup[f.Atom] = f.Name
	}

	// Expect ObjStart
	i := 0
	if i >= n || tokens[i].Type != TokObjStart {
		return FilterResult{Passed: noPreds}
	}
	i++

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

		// Check predicates for this key.
		if !noPreds {
			if predIdx := preds.LookupAtom(keyAtom); predIdx >= 0 {
				matched, seen := preds.Predicates[predIdx].EvalTokenValue(tokens, input, i)
				predSeen[predIdx] = seen
				predMatched[predIdx] = matched
			}
		}

		// Check if this is an output field.
		if name, ok := outputLookup[keyAtom]; ok {
			pending = append(pending, pendingField{name: name, tokenIdx: i})
		}

		// Skip the value.
		i = SkipValue(tokens, i)
	}

	// Evaluate predicates: all must match, and unseen predicates fail.
	if !noPreds {
		for j := 0; j < preds.Len(); j++ {
			if !predSeen[j] || !predMatched[j] {
				return FilterResult{Passed: false}
			}
		}
	}

	// Predicates passed — build the output map.
	data := make(map[string]interface{}, len(pending))
	for _, pf := range pending {
		val := tokenToGoValue(tokens, input, pf.tokenIdx)
		if val != nil {
			data[pf.name] = val
		}
	}

	return FilterResult{Passed: true, Data: data}
}

// FilterFieldEntry pairs an Atom with its output field name.
type FilterFieldEntry struct {
	Atom Atom
	Name string
}

// MakeFilterFieldEntries builds a slice of FilterFieldEntry from names.
func MakeFilterFieldEntries(names []string) []FilterFieldEntry {
	entries := make([]FilterFieldEntry, len(names))
	for i, name := range names {
		entries[i] = FilterFieldEntry{
			Atom: MakeAtom(name),
			Name: name,
		}
	}
	return entries
}

// tokenToGoValue converts a token at position i into the correct Go
// type. Numbers follow the encoding/json convention: all become float64
// for consistency with the existing extractFieldsFromBlob path.
func tokenToGoValue(tokens []Token, input []byte, i int) interface{} {
	if i >= len(tokens) {
		return nil
	}
	tok := tokens[i]
	switch tok.Type {
	case TokString:
		return string(input[tok.Start:tok.End])
	case TokNumber:
		raw := input[tok.Start:tok.End]
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return string(raw)
		}
		return v
	case TokTrue:
		return true
	case TokFalse:
		return false
	case TokNull:
		return nil
	case TokObjStart, TokArrStart:
		end := SkipValue(tokens, i)
		startPos := tok.Start
		var endPos uint32
		if end > 0 && end <= len(tokens) {
			endPos = tokens[end-1].End
		} else {
			endPos = uint32(len(input))
		}
		raw := input[startPos:endPos]
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil
		}
		return v
	default:
		return nil
	}
}
