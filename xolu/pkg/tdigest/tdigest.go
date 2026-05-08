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

// Package tdigest provides an approximate quantile estimator for streaming
// numeric data.
//
// The implementation follows the t-digest algorithm by Ted Dunning (2013).
// A t-digest maintains a sorted list of centroids — (mean, weight) pairs —
// whose maximum size is bounded by the compression parameter δ. Centroids
// near the tails of the distribution are kept small (exact), while centroids
// near the median are allowed to be large (approximate). This trade-off
// gives good accuracy at the tails where precision matters most (P99, P999)
// and acceptable accuracy at the median.
//
// Typical usage:
//
//	td := tdigest.New(100) // compression=100
//	for _, v := range values {
//	    if err := td.Add(v); err != nil { ... }
//	}
//	p50, err := td.Quantile(0.5)
//	p99, err := td.Quantile(0.99)
//	td.Reset() // reuse without reallocating
//
// This package is internal to olu. It intentionally omits serialisation,
// merge, CDF, TrimmedMean, and other features not required by the timeseries
// subsystem.
//
// Core algorithm and data structures adapted from github.com/caio/go-tdigest
// by Caio Alonso. For general-purpose use, use that library instead of this one.
package tdigest

import (
	"fmt"
	"math"
)

// TDigest is a streaming approximate quantile estimator.
// The zero value is not valid; use New.
type TDigest struct {
	s           *summary
	compression float64
	count       uint64
	rng         *localRNG
}

// New creates a TDigest with the given compression parameter δ.
// δ controls the maximum number of centroids: larger δ means more centroids,
// more memory, and better accuracy. Typical values: 50–200.
// δ must be > 0.
func New(compression float64) (*TDigest, error) {
	if compression <= 0 {
		return nil, fmt.Errorf("tdigest: compression must be > 0, got %g", compression)
	}
	return &TDigest{
		s:           newSummary(estimateCap(compression)),
		compression: compression,
		rng:         newLocalRNG(1),
	}, nil
}

// Add registers a single sample value.
// Returns an error if value is NaN.
func (t *TDigest) Add(value float64) error {
	return t.AddWeighted(value, 1)
}

// AddWeighted registers a sample that occurred count times.
// Returns an error if value is NaN or count is zero.
func (t *TDigest) AddWeighted(value float64, count uint64) error {
	if math.IsNaN(value) {
		return fmt.Errorf("tdigest: NaN is not a valid sample value")
	}
	if count == 0 {
		return fmt.Errorf("tdigest: count must be > 0")
	}

	if t.s.len() == 0 {
		t.count = count
		return t.s.add(value, count)
	}

	begin := t.s.floor(value)
	if begin < 0 {
		begin = 0
	}
	begin, end := t.findNeighbors(begin, value)
	closest := t.chooseMergeCandidate(begin, end, count)

	if closest == t.s.len() {
		if err := t.s.add(value, count); err != nil {
			return err
		}
	} else {
		c := float64(t.s.counts[closest])
		newMean := boundedWeightedAvg(t.s.means[closest], c, value, float64(count))
		t.s.setAt(closest, newMean, uint64(c)+count)
	}
	t.count += count

	if float64(t.s.len()) > 20*t.compression {
		return t.compress()
	}
	return nil
}

// Quantile returns the estimated value at quantile q.
// q must be in [0, 1]; returns an error otherwise.
// Returns (0, nil) on an empty digest.
func (t *TDigest) Quantile(q float64) (float64, error) {
	if q < 0 || q > 1 {
		return 0, fmt.Errorf("tdigest: quantile %g out of range [0, 1]", q)
	}
	if t.s.len() == 0 {
		return 0, nil
	}
	if t.s.len() == 1 {
		return t.s.means[0], nil
	}

	index := q * float64(t.count-1)
	previousMean := math.NaN()
	previousIndex := float64(0)

	next, total := t.s.floorSum(index)
	if next > 0 {
		previousMean = t.s.means[next-1]
		previousIndex = total - float64(t.s.counts[next-1]+1)/2
	}

	for {
		nextIndex := total + float64(t.s.counts[next]-1)/2
		if nextIndex >= index {
			if math.IsNaN(previousMean) {
				// index is before the first centroid
				if nextIndex == previousIndex {
					return t.s.means[next], nil
				}
				// assume linear growth before the first centroid
				nextIndex2 := total + float64(t.s.counts[next]) + float64(t.s.counts[next+1]-1)/2
				previousMean = (nextIndex2*t.s.means[next] - nextIndex*t.s.means[next+1]) / (nextIndex2 - nextIndex)
			}
			return interpolate(index, previousIndex, nextIndex, previousMean, t.s.means[next]), nil
		} else if next+1 == t.s.len() {
			// index is after the last centroid
			nextIndex2 := float64(t.count - 1)
			nextMean2 := (t.s.means[next]*(nextIndex2-previousIndex) - previousMean*(nextIndex2-nextIndex)) / (nextIndex - previousIndex)
			return interpolate(index, nextIndex, nextIndex2, t.s.means[next], nextMean2), nil
		}
		total += float64(t.s.counts[next])
		previousMean = t.s.means[next]
		previousIndex = nextIndex
		next++
	}
}

// Count returns the total number of samples added to the digest.
func (t *TDigest) Count() uint64 {
	return t.count
}

// Reset clears the digest without releasing memory.
// After Reset, the instance can be reused as if freshly created.
func (t *TDigest) Reset() {
	t.s.reset()
	t.count = 0
}

// compress reduces the number of centroids by re-inserting them in random
// order into a fresh summary. Called automatically; exposed for testing.
func (t *TDigest) compress() error {
	if t.s.len() <= 1 {
		return nil
	}
	old := t.s
	t.s = newSummary(estimateCap(t.compression))
	t.count = 0
	old.shuffle(t.rng)
	var firstErr error
	old.forEach(func(mean float64, count uint64) bool {
		if err := t.AddWeighted(mean, count); err != nil {
			firstErr = err
			return false
		}
		return true
	})
	return firstErr
}

// --- internal helpers ---

func (t *TDigest) findNeighbors(start int, value float64) (int, int) {
	minDist := math.MaxFloat64
	lastNeighbor := t.s.len()
	for n := start; n < t.s.len(); n++ {
		z := math.Abs(t.s.means[n] - value)
		if z < minDist {
			start = n
			minDist = z
		} else if z > minDist {
			lastNeighbor = n
			break
		}
	}
	return start, lastNeighbor
}

func (t *TDigest) chooseMergeCandidate(begin, end int, count uint64) int {
	closest := t.s.len()
	sum := t.s.headSum(begin)
	var n float32
	for neighbor := begin; neighbor != end; neighbor++ {
		c := float64(t.s.counts[neighbor])
		var q float64
		if t.count == 1 {
			q = 0.5
		} else {
			q = (sum + (c-1)/2) / float64(t.count-1)
		}
		k := 4 * float64(t.count) * q * (1 - q) / t.compression
		if c+float64(count) <= k {
			n++
			if t.rng.float32() < 1/n {
				closest = neighbor
			}
		}
		sum += c
	}
	return closest
}

// interpolate performs linear interpolation between two centroid positions.
func interpolate(index, prevIndex, nextIndex, prevMean, nextMean float64) float64 {
	delta := nextIndex - prevIndex
	prevWeight := (nextIndex - index) / delta
	nextWeight := (index - prevIndex) / delta
	return prevMean*prevWeight + nextMean*nextWeight
}

// boundedWeightedAvg computes the weighted average of two centroids,
// clamping the result to [min(x1,x2), max(x1,x2)] to prevent drift.
func boundedWeightedAvg(x1, w1, x2, w2 float64) float64 {
	if x1 > x2 {
		x1, x2, w1, w2 = x2, x1, w2, w1
	}
	result := (x1*w1 + x2*w2) / (w1 + w2)
	return math.Max(x1, math.Min(result, x2))
}

// estimateCap returns the initial centroid slice capacity for a given
// compression parameter. Matches caio's formula: compression * 10.
func estimateCap(compression float64) int {
	return int(compression) * 10
}
