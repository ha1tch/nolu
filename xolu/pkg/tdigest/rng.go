// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package tdigest

import "math/rand"

// localRNG wraps a private *rand.Rand so the digest's random shuffle and
// reservoir sampling do not contend on the global rand source.
type localRNG struct {
	r *rand.Rand
}

func newLocalRNG(seed int64) *localRNG {
	return &localRNG{r: rand.New(rand.NewSource(seed))} //nolint:gosec
}

func (l *localRNG) intn(n int) int     { return l.r.Intn(n) }
func (l *localRNG) float32() float32   { return l.r.Float32() }
