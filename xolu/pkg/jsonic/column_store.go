// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package jsonic

import (
	"fmt"
	"sort"
)

// ColumnStore holds typed columnar data indexed by Atom. Each column is
// a contiguous slice of a single type, enabling tight loops that the CPU
// can pipeline. Row N across all columns corresponds to the same entity.
type ColumnStore struct {
	Strings map[Atom][]string
	Ints    map[Atom][]int64
	Floats  map[Atom][]float64
	Bools   map[Atom][]bool
	Rows    int
}

// NewColumnStore creates a new column store. The capacity hint is used
// to pre-allocate column slices (not required for correctness).
func NewColumnStore(capacity int) *ColumnStore {
	_ = capacity // reserved for future pre-allocation
	return &ColumnStore{
		Strings: make(map[Atom][]string),
		Ints:    make(map[Atom][]int64),
		Floats:  make(map[Atom][]float64),
		Bools:   make(map[Atom][]bool),
	}
}

// IncrementRows increments the row counter. Called after extracting
// fields from one JSON object.
func (cs *ColumnStore) IncrementRows() {
	cs.Rows++
}

// ---------------------------------------------------------------------------
// ToMaps: backward compatibility with []map[string]interface{}
// ---------------------------------------------------------------------------

// ToMaps converts the columnar data back to the traditional
// []map[string]interface{} representation. This is the compatibility
// bridge for executor code that hasn't been migrated to columnar input.
//
// The nameMap maps Atom -> field name string for the output maps.
func (cs *ColumnStore) ToMaps(nameMap map[Atom]string) []map[string]interface{} {
	if cs.Rows == 0 {
		return []map[string]interface{}{}
	}

	result := make([]map[string]interface{}, cs.Rows)
	for i := range result {
		result[i] = make(map[string]interface{})
	}

	for atom, col := range cs.Strings {
		name, ok := nameMap[atom]
		if !ok {
			continue
		}
		for i, v := range col {
			if i < cs.Rows {
				result[i][name] = v
			}
		}
	}
	for atom, col := range cs.Ints {
		name, ok := nameMap[atom]
		if !ok {
			continue
		}
		for i, v := range col {
			if i < cs.Rows {
				result[i][name] = v
			}
		}
	}
	for atom, col := range cs.Floats {
		name, ok := nameMap[atom]
		if !ok {
			continue
		}
		for i, v := range col {
			if i < cs.Rows {
				result[i][name] = v
			}
		}
	}
	for atom, col := range cs.Bools {
		name, ok := nameMap[atom]
		if !ok {
			continue
		}
		for i, v := range col {
			if i < cs.Rows {
				result[i][name] = v
			}
		}
	}

	return result
}

// ---------------------------------------------------------------------------
// Generic typed operations
// ---------------------------------------------------------------------------

// Numeric is a type constraint for numeric column operations.
type Numeric interface {
	~int64 | ~float64
}

// Sum returns the sum of all values in a numeric column.
func Sum[T Numeric](col []T) T {
	var acc T
	for _, v := range col {
		acc += v
	}
	return acc
}

// Avg returns the arithmetic mean of a numeric column.
func Avg[T Numeric](col []T) float64 {
	if len(col) == 0 {
		return 0
	}
	var acc float64
	for _, v := range col {
		acc += float64(v)
	}
	return acc / float64(len(col))
}

// Min returns the minimum value in a numeric column.
func Min[T Numeric](col []T) T {
	if len(col) == 0 {
		var zero T
		return zero
	}
	m := col[0]
	for _, v := range col[1:] {
		if v < m {
			m = v
		}
	}
	return m
}

// Max returns the maximum value in a numeric column.
func Max[T Numeric](col []T) T {
	if len(col) == 0 {
		var zero T
		return zero
	}
	m := col[0]
	for _, v := range col[1:] {
		if v > m {
			m = v
		}
	}
	return m
}

// Count returns the number of elements in a slice. Trivial, but
// provided for API consistency with other aggregate functions.
func Count[T any](col []T) int {
	return len(col)
}

// FilterIndices returns the indices of elements that satisfy the
// predicate. The returned slice is allocated once at ~25% of the
// column length (a reasonable estimate for selective filters).
func FilterIndices[T Numeric](col []T, op func(T) bool) []int {
	idx := make([]int, 0, len(col)/4)
	for i, v := range col {
		if op(v) {
			idx = append(idx, i)
		}
	}
	return idx
}

// FilterIndicesString returns the indices of string elements that
// satisfy the predicate.
func FilterIndicesString(col []string, op func(string) bool) []int {
	idx := make([]int, 0, len(col)/4)
	for i, v := range col {
		if op(v) {
			idx = append(idx, i)
		}
	}
	return idx
}

// FilterIndicesBool returns the indices of bool elements that
// satisfy the predicate.
func FilterIndicesBool(col []bool, op func(bool) bool) []int {
	idx := make([]int, 0, len(col)/4)
	for i, v := range col {
		if op(v) {
			idx = append(idx, i)
		}
	}
	return idx
}

// Gather collects elements at the given indices into a new slice.
func Gather[T any](col []T, indices []int) []T {
	out := make([]T, len(indices))
	for i, idx := range indices {
		if idx < len(col) {
			out[i] = col[idx]
		}
	}
	return out
}

// SortIndicesBy sorts a set of row indices by the values in a numeric
// column. When desc is true, sorts in descending order.
func SortIndicesBy[T Numeric](col []T, indices []int, desc bool) {
	sort.Slice(indices, func(i, j int) bool {
		if desc {
			return col[indices[i]] > col[indices[j]]
		}
		return col[indices[i]] < col[indices[j]]
	})
}

// SortIndicesByString sorts row indices by string column values.
func SortIndicesByString(col []string, indices []int, desc bool) {
	sort.Slice(indices, func(i, j int) bool {
		if desc {
			return col[indices[i]] > col[indices[j]]
		}
		return col[indices[i]] < col[indices[j]]
	})
}

// GroupSum groups a numeric column by a string column and returns the
// sum per group.
func GroupSum[T Numeric](groupCol []string, valCol []T) map[string]T {
	groups := make(map[string]T, 8)
	for i, key := range groupCol {
		if i < len(valCol) {
			groups[key] += valCol[i]
		}
	}
	return groups
}

// GroupSumIndices groups by string column using an index subset.
func GroupSumIndices[T Numeric](groupCol []string, valCol []T, indices []int) map[string]T {
	groups := make(map[string]T, 8)
	for _, idx := range indices {
		if idx < len(groupCol) && idx < len(valCol) {
			groups[groupCol[idx]] += valCol[idx]
		}
	}
	return groups
}

// GroupCount counts elements per group key.
func GroupCount(groupCol []string) map[string]int {
	groups := make(map[string]int, 8)
	for _, key := range groupCol {
		groups[key]++
	}
	return groups
}

// GroupCountIndices counts elements per group key using an index subset.
func GroupCountIndices(groupCol []string, indices []int) map[string]int {
	groups := make(map[string]int, 8)
	for _, idx := range indices {
		if idx < len(groupCol) {
			groups[groupCol[idx]]++
		}
	}
	return groups
}

// AllIndices returns a slice [0, 1, 2, ..., n-1].
func AllIndices(n int) []int {
	idx := make([]int, n)
	for i := range idx {
		idx[i] = i
	}
	return idx
}

// DistinctString returns the unique values in a string column,
// preserving first-occurrence order.
func DistinctString(col []string) []string {
	seen := make(map[string]struct{}, len(col))
	out := make([]string, 0, len(col)/2)
	for _, v := range col {
		if _, ok := seen[v]; !ok {
			seen[v] = struct{}{}
			out = append(out, v)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// String representation (for debugging)
// ---------------------------------------------------------------------------

func (cs *ColumnStore) String() string {
	return fmt.Sprintf("ColumnStore{rows=%d, strings=%d cols, ints=%d cols, floats=%d cols, bools=%d cols}",
		cs.Rows, len(cs.Strings), len(cs.Ints), len(cs.Floats), len(cs.Bools))
}
