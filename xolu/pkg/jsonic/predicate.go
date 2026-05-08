// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
)

// PredicateOp identifies a comparison operator for field predicates.
type PredicateOp uint8

const (
	OpEq  PredicateOp = iota // =
	OpNeq                    // !=
	OpLt                     // <
	OpLte                    // <=
	OpGt                     // >
	OpGte                    // >=
	OpIn                     // IN (value list)
	OpLike                   // LIKE (string pattern)
)

// String returns the operator symbol.
func (op PredicateOp) String() string {
	switch op {
	case OpEq:
		return "="
	case OpNeq:
		return "!="
	case OpLt:
		return "<"
	case OpLte:
		return "<="
	case OpGt:
		return ">"
	case OpGte:
		return ">="
	case OpIn:
		return "IN"
	case OpLike:
		return "LIKE"
	default:
		return "?"
	}
}

// FieldPredicate describes a filter condition on a single JSON field.
// The Value is stored as the native Go type that will be compared
// against the tokenised JSON value.
type FieldPredicate struct {
	Name string      // JSON key name
	Atom Atom        // pre-computed atom for fast key matching
	Type FieldType   // expected value type of the field
	Op   PredicateOp // comparison operator
	Val  interface{} // comparison target: string, float64, int64, bool, or []interface{} for IN
}

// MakeFieldPredicate creates a predicate, computing the Atom from the name.
func MakeFieldPredicate(name string, typ FieldType, op PredicateOp, val interface{}) FieldPredicate {
	return FieldPredicate{
		Name: name,
		Atom: MakeAtom(name),
		Type: typ,
		Op:   op,
		Val:  val,
	}
}

// PredicateSet is an AND-combined set of field predicates. All must
// match for a row to pass. This represents the subset of WHERE clauses
// that can be evaluated during tokenisation.
type PredicateSet struct {
	Predicates []FieldPredicate
	atoms      map[Atom]int // atom -> index in Predicates
}

// NewPredicateSet creates a predicate set from the given predicates.
func NewPredicateSet(preds []FieldPredicate) *PredicateSet {
	atoms := make(map[Atom]int, len(preds))
	for i, p := range preds {
		atoms[p.Atom] = i
	}
	return &PredicateSet{
		Predicates: preds,
		atoms:      atoms,
	}
}

// Len returns the number of predicates.
func (ps *PredicateSet) Len() int {
	if ps == nil {
		return 0
	}
	return len(ps.Predicates)
}

// LookupAtom returns the predicate index for a given atom, or -1 if
// the atom doesn't correspond to any predicate field.
func (ps *PredicateSet) LookupAtom(a Atom) int {
	if ps == nil {
		return -1
	}
	idx, ok := ps.atoms[a]
	if !ok {
		return -1
	}
	return idx
}

// ---------------------------------------------------------------------------
// Predicate evaluation
// ---------------------------------------------------------------------------

// EvalTokenValue evaluates a predicate against a raw token from the
// JSON input. Returns (matched, fieldWasSeen). If the token type
// doesn't match the predicate's expected type, returns (false, true)
// — the field was seen but the type mismatch means no match.
func (fp *FieldPredicate) EvalTokenValue(tokens []Token, input []byte, tokenIdx int) (bool, bool) {
	if tokenIdx >= len(tokens) {
		return false, false
	}
	tok := tokens[tokenIdx]

	switch fp.Type {
	case FieldString:
		if tok.Type != TokString {
			return false, true
		}
		s := unsafeString(input[tok.Start:tok.End])
		return fp.evalString(s), true

	case FieldInt:
		if tok.Type != TokNumber {
			return false, true
		}
		raw := input[tok.Start:tok.End]
		v := parseIntFast(raw)
		return fp.evalInt(v), true

	case FieldFloat:
		if tok.Type != TokNumber {
			return false, true
		}
		raw := input[tok.Start:tok.End]
		v := parseFloatFast(raw)
		return fp.evalFloat(v), true

	case FieldBool:
		if tok.Type != TokTrue && tok.Type != TokFalse {
			return false, true
		}
		v := tok.Type == TokTrue
		return fp.evalBool(v), true
	}
	return false, true
}

func (fp *FieldPredicate) evalString(v string) bool {
	// IN and LIKE have special value types, handle first.
	switch fp.Op {
	case OpIn:
		return fp.evalIn(v)
	case OpLike:
		target, ok := fp.Val.(string)
		if !ok {
			return false
		}
		return matchLike(v, target)
	}

	target, ok := fp.Val.(string)
	if !ok {
		return false
	}
	return compareOrdered(v, target, fp.Op)
}

func (fp *FieldPredicate) evalInt(v int64) bool {
	switch target := fp.Val.(type) {
	case int64:
		return compareOrdered(v, target, fp.Op)
	case float64:
		return compareOrdered(float64(v), target, fp.Op)
	default:
		if fp.Op == OpIn {
			return fp.evalInNumeric(float64(v))
		}
		return false
	}
}

func (fp *FieldPredicate) evalFloat(v float64) bool {
	switch target := fp.Val.(type) {
	case float64:
		return compareOrdered(v, target, fp.Op)
	case int64:
		return compareOrdered(v, float64(target), fp.Op)
	default:
		if fp.Op == OpIn {
			return fp.evalInNumeric(v)
		}
		return false
	}
}

func (fp *FieldPredicate) evalBool(v bool) bool {
	target, ok := fp.Val.(bool)
	if !ok {
		return false
	}
	switch fp.Op {
	case OpEq:
		return v == target
	case OpNeq:
		return v != target
	default:
		return false
	}
}

// evalIn checks if a string value is in the predicate's value list.
func (fp *FieldPredicate) evalIn(v string) bool {
	list, ok := fp.Val.([]interface{})
	if !ok {
		return false
	}
	for _, item := range list {
		if s, ok := item.(string); ok && s == v {
			return true
		}
	}
	return false
}

// evalInNumeric checks if a numeric value is in the predicate's value list.
func (fp *FieldPredicate) evalInNumeric(v float64) bool {
	list, ok := fp.Val.([]interface{})
	if !ok {
		return false
	}
	for _, item := range list {
		switch n := item.(type) {
		case float64:
			if math.Abs(v-n) < 1e-9 {
				return true
			}
		case int64:
			if math.Abs(v-float64(n)) < 1e-9 {
				return true
			}
		}
	}
	return false
}

// compareOrdered performs ordered comparison for numeric and string types.
func compareOrdered[T ~int64 | ~float64 | ~string](a, b T, op PredicateOp) bool {
	switch op {
	case OpEq:
		return a == b
	case OpNeq:
		return a != b
	case OpLt:
		return a < b
	case OpLte:
		return a <= b
	case OpGt:
		return a > b
	case OpGte:
		return a >= b
	default:
		return false
	}
}

// matchLike implements SQL LIKE pattern matching. '%' matches any
// sequence of characters, '_' matches exactly one character. The match
// is case-insensitive, consistent with SQLite defaults.
func matchLike(s, pattern string) bool {
	s = strings.ToLower(s)
	pattern = strings.ToLower(pattern)
	return matchLikeRec(s, pattern, 0, 0)
}

func matchLikeRec(s, p string, si, pi int) bool {
	for pi < len(p) {
		switch p[pi] {
		case '%':
			// Skip consecutive %
			for pi < len(p) && p[pi] == '%' {
				pi++
			}
			if pi >= len(p) {
				return true // trailing % matches everything
			}
			// Try matching the rest from every position
			for si <= len(s) {
				if matchLikeRec(s, p, si, pi) {
					return true
				}
				si++
			}
			return false
		case '_':
			if si >= len(s) {
				return false
			}
			si++
			pi++
		default:
			if si >= len(s) || s[si] != p[pi] {
				return false
			}
			si++
			pi++
		}
	}
	return si == len(s)
}

// ---------------------------------------------------------------------------
// JSON value coercion helpers (for building predicates from OQL)
// ---------------------------------------------------------------------------

// CoercePredicateValue converts an interface{} value (typically from OQL
// parsing) into the appropriate Go type for the given FieldType. This is
// used when building FieldPredicates from parsed OQL WHERE expressions.
func CoercePredicateValue(val interface{}, ft FieldType) (interface{}, error) {
	if val == nil {
		return nil, fmt.Errorf("nil predicate value")
	}
	switch ft {
	case FieldString:
		switch v := val.(type) {
		case string:
			return v, nil
		case json.Number:
			return v.String(), nil
		default:
			return fmt.Sprintf("%v", v), nil
		}
	case FieldInt:
		switch v := val.(type) {
		case int64:
			return v, nil
		case float64:
			return int64(v), nil
		case int:
			return int64(v), nil
		case json.Number:
			i, err := v.Int64()
			if err != nil {
				return nil, fmt.Errorf("cannot coerce %q to int64: %w", v, err)
			}
			return i, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to int64", val)
		}
	case FieldFloat:
		switch v := val.(type) {
		case float64:
			return v, nil
		case int64:
			return float64(v), nil
		case int:
			return float64(v), nil
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return nil, fmt.Errorf("cannot coerce %q to float64: %w", v, err)
			}
			return f, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to float64", val)
		}
	case FieldBool:
		switch v := val.(type) {
		case bool:
			return v, nil
		default:
			return nil, fmt.Errorf("cannot coerce %T to bool", val)
		}
	default:
		return nil, fmt.Errorf("unknown field type %d", ft)
	}
}
