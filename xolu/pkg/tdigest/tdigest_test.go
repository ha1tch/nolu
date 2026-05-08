// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package tdigest

import (
	"math"
	"testing"
)

// --- Constructor ---

func TestNew_InvalidCompression(t *testing.T) {
	for _, c := range []float64{0, -1, -100} {
		if _, err := New(c); err == nil {
			t.Errorf("New(%g): expected error, got nil", c)
		}
	}
}

func TestNew_ValidCompression(t *testing.T) {
	for _, c := range []float64{1, 50, 100, 200} {
		td, err := New(c)
		if err != nil {
			t.Errorf("New(%g): unexpected error: %v", c, err)
		}
		if td.Count() != 0 {
			t.Errorf("new digest should have count 0")
		}
	}
}

// --- Add / AddWeighted error paths ---

func TestAdd_NaN(t *testing.T) {
	td, _ := New(100)
	if err := td.Add(math.NaN()); err == nil {
		t.Error("Add(NaN): expected error")
	}
}

func TestAddWeighted_ZeroCount(t *testing.T) {
	td, _ := New(100)
	if err := td.AddWeighted(1.0, 0); err == nil {
		t.Error("AddWeighted(1.0, 0): expected error")
	}
}

// --- Quantile error paths ---

func TestQuantile_OutOfRange(t *testing.T) {
	td, _ := New(100)
	td.Add(1.0)
	for _, q := range []float64{-0.1, 1.1, 2.0, -1.0} {
		if _, err := td.Quantile(q); err == nil {
			t.Errorf("Quantile(%g): expected error for out-of-range q", q)
		}
	}
}

func TestQuantile_Empty(t *testing.T) {
	td, _ := New(100)
	v, err := td.Quantile(0.5)
	if err != nil {
		t.Fatalf("Quantile on empty digest: unexpected error: %v", err)
	}
	if v != 0 {
		t.Errorf("empty digest Quantile(0.5): got %g, want 0", v)
	}
}

func TestQuantile_SingleValue(t *testing.T) {
	td, _ := New(100)
	td.Add(42.0)
	v, err := td.Quantile(0.5)
	if err != nil {
		t.Fatal(err)
	}
	if v != 42.0 {
		t.Errorf("single-value Quantile(0.5): got %g, want 42.0", v)
	}
}

// --- Count ---

func TestCount(t *testing.T) {
	td, _ := New(100)
	for i := 0; i < 100; i++ {
		td.Add(float64(i))
	}
	if td.Count() != 100 {
		t.Errorf("Count: got %d want 100", td.Count())
	}
}

func TestAddWeighted_Count(t *testing.T) {
	td, _ := New(100)
	td.AddWeighted(5.0, 10)
	if td.Count() != 10 {
		t.Errorf("Count after AddWeighted(5.0,10): got %d want 10", td.Count())
	}
}

// --- Reset ---

func TestReset(t *testing.T) {
	td, _ := New(100)
	for i := 0; i < 50; i++ {
		td.Add(float64(i))
	}
	td.Reset()
	if td.Count() != 0 {
		t.Errorf("after Reset, Count = %d want 0", td.Count())
	}
	// Reuse should work correctly.
	td.Add(7.0)
	v, _ := td.Quantile(0.5)
	if v != 7.0 {
		t.Errorf("after Reset+Add(7.0), Quantile(0.5) = %g want 7.0", v)
	}
}

// --- Monotone uniform distribution accuracy ---
//
// For a uniform distribution over [0, N), the true P50 is N/2, P90 is 0.9N,
// P99 is 0.99N. We accept ±5% relative error as the accuracy threshold.

func TestQuantile_UniformAccuracy(t *testing.T) {
	const N = 10_000
	td, _ := New(100)
	for i := 0; i < N; i++ {
		td.Add(float64(i))
	}
	cases := []struct {
		q    float64
		want float64
	}{
		{0.5, N * 0.5},
		{0.9, N * 0.9},
		{0.99, N * 0.99},
	}
	for _, tc := range cases {
		got, err := td.Quantile(tc.q)
		if err != nil {
			t.Fatalf("Quantile(%g): %v", tc.q, err)
		}
		relErr := math.Abs(got-tc.want) / tc.want
		if relErr > 0.05 {
			t.Errorf("Quantile(%g): got %.1f, want ~%.1f (rel err %.2f%%)",
				tc.q, got, tc.want, relErr*100)
		}
	}
}

// --- Extreme quantiles ---

func TestQuantile_Extremes(t *testing.T) {
	td, _ := New(100)
	for i := 1; i <= 1000; i++ {
		td.Add(float64(i))
	}
	min, _ := td.Quantile(0.0)
	max, _ := td.Quantile(1.0)
	if min > 2.0 {
		t.Errorf("Quantile(0.0): got %g, want ~1", min)
	}
	if max < 998.0 {
		t.Errorf("Quantile(1.0): got %g, want ~1000", max)
	}
}

// --- Boundary quantiles (exactly 0 and 1) ---

func TestQuantile_BoundaryValid(t *testing.T) {
	td, _ := New(100)
	td.Add(5.0)
	td.Add(10.0)
	for _, q := range []float64{0.0, 1.0} {
		if _, err := td.Quantile(q); err != nil {
			t.Errorf("Quantile(%g) should not error: %v", q, err)
		}
	}
}

// --- Auto-compress does not corrupt results ---

func TestCompress_DoesNotCorrupt(t *testing.T) {
	// Feed enough distinct values to trigger auto-compress (> 20*compression).
	td, _ := New(10) // low compression to trigger compress sooner
	const N = 5_000
	for i := 0; i < N; i++ {
		td.Add(float64(i))
	}
	// Median should still be approximately N/2.
	got, err := td.Quantile(0.5)
	if err != nil {
		t.Fatal(err)
	}
	want := float64(N) / 2
	if math.Abs(got-want)/want > 0.1 {
		t.Errorf("post-compress Quantile(0.5): got %.1f, want ~%.1f", got, want)
	}
}

// --- Benchmarks ---

func BenchmarkAdd_100k(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td, _ := New(100)
		for j := 0; j < 100_000; j++ {
			td.Add(float64(j))
		}
	}
}

func BenchmarkQuantile(b *testing.B) {
	td, _ := New(100)
	for i := 0; i < 10_000; i++ {
		td.Add(float64(i))
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		td.Quantile(0.5)
	}
}
