// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0
//
// Portions adapted from github.com/caio/go-tdigest:
// The MIT License (MIT)
// Copyright (c) 2015 Caio Romão Costa Nascimento
// Permission is hereby granted, free of charge, to any person obtaining a copy
// of this software and associated documentation files (the "Software"), to deal
// in the Software without restriction, including without limitation the rights
// to use, copy, modify, merge, publish, distribute, sublicense, and/or sell
// copies of the Software, and to permit persons to whom the Software is
// furnished to do so, subject to the following conditions:
// The above copyright notice and this permission notice shall be included in all
// copies or substantial portions of the Software.
// THE SOFTWARE IS PROVIDED "AS IS", WITHOUT WARRANTY OF ANY KIND, EXPRESS OR
// IMPLIED, INCLUDING BUT NOT LIMITED TO THE WARRANTIES OF MERCHANTABILITY,
// FITNESS FOR A PARTICULAR PURPOSE AND NONINFRINGEMENT. IN NO EVENT SHALL THE
// AUTHORS OR COPYRIGHT HOLDERS BE LIABLE FOR ANY CLAIM, DAMAGES OR OTHER
// LIABILITY, WHETHER IN AN ACTION OF CONTRACT, TORT OR OTHERWISE, ARISING FROM,
// OUT OF OR IN CONNECTION WITH THE SOFTWARE OR THE USE OR OTHER DEALINGS IN THE
// SOFTWARE.

package tdigest

import (
	"fmt"
	"math"
	"sort"
)

// summary is the centroid store: two parallel slices kept in ascending order
// by mean. Insertion maintains the sort invariant via bubble-style adjust.
// Binary search is used above 250 centroids; linear scan below.
type summary struct {
	means  []float64
	counts []uint64
}

func newSummary(capacity int) *summary {
	return &summary{
		means:  make([]float64, 0, capacity),
		counts: make([]uint64, 0, capacity),
	}
}

func (s *summary) len() int { return len(s.means) }

func (s *summary) add(mean float64, count uint64) error {
	if math.IsNaN(mean) {
		return fmt.Errorf("tdigest: mean must not be NaN")
	}
	if count == 0 {
		return fmt.Errorf("tdigest: count must be > 0")
	}
	idx := s.insertionIndex(mean)
	s.means = append(s.means, math.NaN())
	s.counts = append(s.counts, 0)
	copy(s.means[idx+1:], s.means[idx:])
	copy(s.counts[idx+1:], s.counts[idx:])
	s.means[idx] = mean
	s.counts[idx] = count
	return nil
}

// insertionIndex returns the position at which mean should be inserted
// to maintain ascending sort (insert to the right of equals).
func (s *summary) insertionIndex(x float64) int {
	if len(s.means) < 250 {
		for i, m := range s.means {
			if m > x {
				return i
			}
		}
		return len(s.means)
	}
	return sort.Search(len(s.means), func(i int) bool {
		return s.means[i] > x
	})
}

// floor returns the index of the rightmost centroid with mean <= x,
// or -1 if all centroids have mean > x.
func (s *summary) floor(x float64) int {
	return s.lowerBound(x) - 1
}

// lowerBound returns the index of the first centroid with mean >= x.
func (s *summary) lowerBound(x float64) int {
	if len(s.means) < 250 {
		for i, m := range s.means {
			if m >= x {
				return i
			}
		}
		return len(s.means)
	}
	return sort.Search(len(s.means), func(i int) bool {
		return s.means[i] >= x
	})
}

// floorSum returns the index of the last centroid i for which the cumulative
// count of [0..i] is <= sum, and the cumulative sum up to (but not including)
// that centroid.
func (s *summary) floorSum(sum float64) (index int, cumSum float64) {
	index = -1
	for i, count := range s.counts {
		if cumSum <= sum {
			index = i
		} else {
			break
		}
		cumSum += float64(count)
	}
	if index != -1 {
		cumSum -= float64(s.counts[index])
	}
	return index, cumSum
}

// headSum returns the cumulative count of centroids [0..idx).
func (s *summary) headSum(idx int) float64 {
	return float64(sumCounts(s.counts, idx))
}

// setAt updates mean and count at idx and re-sorts the entry.
func (s *summary) setAt(idx int, mean float64, count uint64) {
	s.means[idx] = mean
	s.counts[idx] = count
	s.adjustRight(idx)
	s.adjustLeft(idx)
}

func (s *summary) adjustRight(idx int) {
	for i := idx + 1; i < len(s.means) && s.means[i-1] > s.means[i]; i++ {
		s.means[i-1], s.means[i] = s.means[i], s.means[i-1]
		s.counts[i-1], s.counts[i] = s.counts[i], s.counts[i-1]
	}
}

func (s *summary) adjustLeft(idx int) {
	for i := idx - 1; i >= 0 && s.means[i] > s.means[i+1]; i-- {
		s.means[i], s.means[i+1] = s.means[i+1], s.means[i]
		s.counts[i], s.counts[i+1] = s.counts[i+1], s.counts[i]
	}
}

// forEach calls f for each centroid in order. Stops early if f returns false.
func (s *summary) forEach(f func(mean float64, count uint64) bool) {
	for i, mean := range s.means {
		if !f(mean, s.counts[i]) {
			return
		}
	}
}

// shuffle randomly permutes the centroid list in-place (Fisher-Yates).
// Used before Compress to avoid pathological insertion order.
func (s *summary) shuffle(rng *localRNG) {
	for i := len(s.means) - 1; i > 1; i-- {
		j := rng.intn(i + 1)
		s.means[i], s.means[j] = s.means[j], s.means[i]
		s.counts[i], s.counts[j] = s.counts[j], s.counts[i]
	}
}

// reset truncates both slices to zero without releasing memory.
// Safe to call on a pooled instance before reuse.
func (s *summary) reset() {
	s.means = s.means[:0]
	s.counts = s.counts[:0]
}

// sumCounts returns the sum of counts[0..idx) with a 4-way loop unroll.
func sumCounts(counts []uint64, idx int) uint64 {
	var cum uint64
	i := idx - 1
	for ; i >= 3; i -= 4 {
		cum += counts[i]
		cum += counts[i-1]
		cum += counts[i-2]
		cum += counts[i-3]
	}
	for ; i >= 0; i-- {
		cum += counts[i]
	}
	return cum
}
