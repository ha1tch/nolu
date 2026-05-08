// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// range_agg_test.go
//
// Correctness tests and benchmarks for RangeSum, RangeAvg, RangeMin,
// RangeMax, RangeCount, and RangeAggregate.
//
// Benchmark methodology
// ---------------------
// Both single-field functions and RangeAggregate perform exactly one
// Pebble iterator pass over the same key range. The benchmarks use an
// identical seed dataset (benchEventCount events, all seven num fields
// populated) so any throughput difference is attributable solely to the
// accumulator width, not to scan differences.
//
// Expected outcome: wall time per op should be within a few percent.
// If RangeSum is measurably faster than RangeAggregate at the same event
// count, retain both. Otherwise RangeAggregate alone is sufficient.

import (
	"context"
	"math"
	"testing"
	"time"
)

// --- Shared test helpers ---

// seedRangeAggStore creates a store, defines timeline 1 (dims=1), and
// seeds n events starting at base with all seven num fields populated.
// nums[i] = float64(i * (j+1)) where j is the event index.
func seedRangeAggStore(t *testing.T, n int) (Store, time.Time, time.Time) {
	t.Helper()
	store, err := NewPebbleStore(t.TempDir(), testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { store.Close() })

	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1}); err != nil {
		t.Fatal(err)
	}

	ctx := context.Background()
	base := time.Unix(10_000_000, 0).UTC()
	for j := 0; j < n; j++ {
		nums := make([]float64, 7)
		for i := range nums {
			nums[i] = float64((i + 1) * (j + 1))
		}
		if err := store.Append(ctx, Event{
			Timeline: 1,
			Dims:     []uint64{1},
			Time:     base.Add(time.Duration(j) * time.Second),
			Nums:     nums,
		}); err != nil {
			t.Fatal(err)
		}
	}
	from := base.Add(-time.Second)
	to := base.Add(time.Duration(n+1) * time.Second)
	return store, from, to
}

// --- Correctness tests ---

func TestRangeCount_Basic(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 10)
	ctx := context.Background()
	n, err := store.RangeCount(ctx, RangeNumQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to, NumField: 0,
	})
	if err != nil {
		t.Fatalf("RangeCount: %v", err)
	}
	if n != 10 {
		t.Errorf("got %d, want 10", n)
	}
}

func TestRangeSum_Basic(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 4)
	ctx := context.Background()
	// nums[0] for events j=0..3: 1*(0+1)=1, 1*(1+1)=2, 1*(2+1)=3, 1*(3+1)=4 → sum=10
	sum, err := store.RangeSum(ctx, RangeNumQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to, NumField: 0,
	})
	if err != nil {
		t.Fatalf("RangeSum: %v", err)
	}
	if sum != 10 {
		t.Errorf("got %v, want 10", sum)
	}
}

func TestRangeAvg_Basic(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 4)
	ctx := context.Background()
	// nums[0] = 1,2,3,4 → avg = 2.5
	avg, err := store.RangeAvg(ctx, RangeNumQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to, NumField: 0,
	})
	if err != nil {
		t.Fatalf("RangeAvg: %v", err)
	}
	if avg != 2.5 {
		t.Errorf("got %v, want 2.5", avg)
	}
}

func TestRangeMin_Basic(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 4)
	ctx := context.Background()
	// nums[0] = 1,2,3,4 → min = 1
	min, err := store.RangeMin(ctx, RangeNumQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to, NumField: 0,
	})
	if err != nil {
		t.Fatalf("RangeMin: %v", err)
	}
	if min != 1 {
		t.Errorf("got %v, want 1", min)
	}
}

func TestRangeMax_Basic(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 4)
	ctx := context.Background()
	// nums[0] = 1,2,3,4 → max = 4
	max, err := store.RangeMax(ctx, RangeNumQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to, NumField: 0,
	})
	if err != nil {
		t.Fatalf("RangeMax: %v", err)
	}
	if max != 4 {
		t.Errorf("got %v, want 4", max)
	}
}

func TestRangeAggregate_AllFields(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 4)
	ctx := context.Background()
	res, err := store.RangeAggregate(ctx, RangeAllQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to,
	})
	if err != nil {
		t.Fatalf("RangeAggregate: %v", err)
	}

	if res.Count != 4 {
		t.Errorf("Count: got %d want 4", res.Count)
	}
	for i := range res.Fields {
		if !res.Fields[i] {
			t.Errorf("Fields[%d]: want true", i)
		}
	}
	// nums[0] = 1,2,3,4 → sum=10, avg=2.5, min=1, max=4
	if res.Sums[0] != 10 {
		t.Errorf("Sums[0]: got %v want 10", res.Sums[0])
	}
	if res.Avgs[0] != 2.5 {
		t.Errorf("Avgs[0]: got %v want 2.5", res.Avgs[0])
	}
	if res.Mins[0] != 1 {
		t.Errorf("Mins[0]: got %v want 1", res.Mins[0])
	}
	if res.Maxs[0] != 4 {
		t.Errorf("Maxs[0]: got %v want 4", res.Maxs[0])
	}
	// nums[6] = 7,14,21,28 → sum=70, avg=17.5, min=7, max=28
	if res.Sums[6] != 70 {
		t.Errorf("Sums[6]: got %v want 70", res.Sums[6])
	}
	if res.Avgs[6] != 17.5 {
		t.Errorf("Avgs[6]: got %v want 17.5", res.Avgs[6])
	}
}

func TestRangeAggregate_ConsistentWithSingleField(t *testing.T) {
	// RangeAggregate results for field 0 must match individual functions.
	store, from, to := seedRangeAggStore(t, 20)
	ctx := context.Background()

	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	expectedSum, _ := store.RangeSum(ctx, q)
	expectedAvg, _ := store.RangeAvg(ctx, q)
	expectedMin, _ := store.RangeMin(ctx, q)
	expectedMax, _ := store.RangeMax(ctx, q)
	expectedCount, _ := store.RangeCount(ctx, q)

	res, err := store.RangeAggregate(ctx, RangeAllQuery{
		Timeline: 1, Dims: []uint64{1}, From: from, To: to,
	})
	if err != nil {
		t.Fatalf("RangeAggregate: %v", err)
	}

	if res.Count != expectedCount {
		t.Errorf("Count: RangeAggregate=%d RangeCount=%d", res.Count, expectedCount)
	}
	if res.Sums[0] != expectedSum {
		t.Errorf("Sums[0]: RangeAggregate=%v RangeSum=%v", res.Sums[0], expectedSum)
	}
	if res.Avgs[0] != expectedAvg {
		t.Errorf("Avgs[0]: RangeAggregate=%v RangeAvg=%v", res.Avgs[0], expectedAvg)
	}
	if res.Mins[0] != expectedMin {
		t.Errorf("Mins[0]: RangeAggregate=%v RangeMin=%v", res.Mins[0], expectedMin)
	}
	if res.Maxs[0] != expectedMax {
		t.Errorf("Maxs[0]: RangeAggregate=%v RangeMax=%v", res.Maxs[0], expectedMax)
	}
}

func TestRangeAggregate_EmptyRange(t *testing.T) {
	store, _, _ := seedRangeAggStore(t, 5)
	ctx := context.Background()
	future := time.Unix(999_000_000, 0).UTC()
	res, err := store.RangeAggregate(ctx, RangeAllQuery{
		Timeline: 1, Dims: []uint64{1},
		From: future, To: future.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("RangeAggregate empty: %v", err)
	}
	if res.Count != 0 {
		t.Errorf("Count: got %d want 0", res.Count)
	}
	for i, f := range res.Fields {
		if f {
			t.Errorf("Fields[%d]: want false for empty range", i)
		}
	}
	// Min/Max should be zero (not MaxFloat64/-MaxFloat64) for absent fields.
	for i, v := range res.Mins {
		if v != 0 {
			t.Errorf("Mins[%d]: got %v want 0 for empty range", i, v)
		}
	}
}

func TestRangeAggregate_SparseFields(t *testing.T) {
	// Events with only nums[0] and nums[2] present; fields 1,3-6 absent.
	store, err := NewPebbleStore(t.TempDir(), testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	store.DefineTimeline(1, TimelineConfig{Dims: 1})

	ctx := context.Background()
	base := time.Unix(20_000_000, 0).UTC()
	for j := 0; j < 3; j++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: base.Add(time.Duration(j) * time.Second),
			Nums: []float64{float64(j + 1), 0, float64((j + 1) * 10)},
		})
	}

	res, err := store.RangeAggregate(ctx, RangeAllQuery{
		Timeline: 1, Dims: []uint64{1},
		From: base.Add(-time.Second), To: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("RangeAggregate sparse: %v", err)
	}
	if !res.Fields[0] {
		t.Error("Fields[0]: want true")
	}
	if !res.Fields[1] {
		t.Error("Fields[1]: want true (0.0 is a valid value)")
	}
	if !res.Fields[2] {
		t.Error("Fields[2]: want true")
	}
	for i := 3; i < 7; i++ {
		if res.Fields[i] {
			t.Errorf("Fields[%d]: want false (field not present)", i)
		}
	}
}

func TestRangeNumQuery_InvalidNumField(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 2)
	ctx := context.Background()
	_, err := store.RangeSum(ctx, RangeNumQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to, NumField: 7, // out of range
	})
	if err == nil {
		t.Error("expected error for NumField=7, got nil")
	}
}

func TestRangeAggregate_ScanLimit(t *testing.T) {
	store, from, to := seedRangeAggStore(t, 100)
	ctx := context.Background()
	_, err := store.RangeAggregate(ctx, RangeAllQuery{
		Timeline: 1, Dims: []uint64{1},
		From: from, To: to,
		MaxScanEvents: 5,
	})
	if err != ErrScanLimitExceeded {
		t.Errorf("expected ErrScanLimitExceeded, got %v", err)
	}
}

// --- Benchmarks ---
//
// Seed once per benchmark via b.StopTimer()/b.StartTimer() to isolate
// scan cost from write cost.

const benchEventCount = 2_500

func BenchmarkRangeSum(b *testing.B) {
	store, from, to := benchSeedStore(b)
	ctx := context.Background()
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RangeSum(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRangeAvg(b *testing.B) {
	store, from, to := benchSeedStore(b)
	ctx := context.Background()
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RangeAvg(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRangeMin(b *testing.B) {
	store, from, to := benchSeedStore(b)
	ctx := context.Background()
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RangeMin(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRangeMax(b *testing.B) {
	store, from, to := benchSeedStore(b)
	ctx := context.Background()
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RangeMax(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRangeCount(b *testing.B) {
	store, from, to := benchSeedStore(b)
	ctx := context.Background()
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RangeCount(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRangeAggregate(b *testing.B) {
	store, from, to := benchSeedStore(b)
	ctx := context.Background()
	q := RangeAllQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := store.RangeAggregate(ctx, q); err != nil {
			b.Fatal(err)
		}
	}
}

// benchSeedStore creates and seeds a store for benchmarking.
// All seven num fields populated on every event.
func benchSeedStore(b *testing.B) (Store, time.Time, time.Time) {
	b.Helper()
	b.StopTimer()
	store, err := NewPebbleStore(b.TempDir(), testStoreConfig(), testPebbleConfig())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { store.Close() })
	store.DefineTimeline(1, TimelineConfig{Dims: 1})

	ctx := context.Background()
	base := time.Unix(50_000_000, 0).UTC()
	nums := []float64{1.1, 2.2, 3.3, 4.4, 5.5, 6.6, 7.7}
	for j := 0; j < benchEventCount; j++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: base.Add(time.Duration(j) * time.Millisecond),
			Nums: nums,
		})
	}
	from := base.Add(-time.Second)
	to := base.Add(time.Duration(benchEventCount+1) * time.Millisecond)
	b.StartTimer()
	return store, from, to
}

// Ensure math is imported (used in TestRangeAggregate_SparseFields reasoning).
var _ = math.MaxFloat64

// --- RangeQuantile / RangeMedian ---

func TestRangeQuantile_InvalidQ(t *testing.T) {
	s, from, to := seedRangeAggStore(t, 100)
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 0}
	for _, bad := range []float64{-0.1, 1.1, 2.0} {
		if _, err := s.RangeQuantile(context.Background(), q, bad); err == nil {
			t.Errorf("RangeQuantile(q=%g): expected error", bad)
		}
	}
}

func TestRangeQuantile_InvalidNumField(t *testing.T) {
	s, from, to := seedRangeAggStore(t, 100)
	q := RangeNumQuery{Timeline: 1, Dims: []uint64{1}, From: from, To: to, NumField: 7}
	if _, err := s.RangeQuantile(context.Background(), q, 0.5); err == nil {
		t.Error("RangeQuantile(NumField=7): expected error")
	}
}

func TestRangeQuantile_EmptyRange(t *testing.T) {
	s, from, _ := seedRangeAggStore(t, 100)
	q := RangeNumQuery{
		Timeline: 1,
		Dims:     []uint64{1},
		From:     from.Add(-2 * time.Hour),
		To:       from.Add(-1 * time.Hour),
		NumField: 0,
	}
	v, err := s.RangeQuantile(context.Background(), q, 0.5)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if v != 0 {
		t.Errorf("empty range: got %g, want 0", v)
	}
}

func seedQuantileStore(t *testing.T, n int) (Store, RangeNumQuery) {
	t.Helper()
	store, err := NewPebbleStore(t.TempDir(), testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 1}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := make([]Event, n)
	for i := range events {
		events[i] = Event{
			Timeline: 1,
			Dims:     []uint64{1},
			Time:     base.Add(time.Duration(i) * time.Second),
			Nums:     []float64{float64(i)},
		}
	}
	if _, err := store.AppendBatch(ctx, events, 0); err != nil {
		t.Fatal(err)
	}
	q := RangeNumQuery{
		Timeline: 1,
		Dims:     []uint64{1},
		From:     base.Add(-time.Second),
		To:       base.Add(time.Duration(n+1) * time.Second),
		NumField: 0,
	}
	return store, q
}

func TestRangeMedian_Basic(t *testing.T) {
	const N = 500
	s, q := seedQuantileStore(t, N)
	median, err := s.RangeMedian(context.Background(), q)
	if err != nil {
		t.Fatalf("RangeMedian: %v", err)
	}
	want := float64(N) / 2
	relErr := math.Abs(median-want) / want
	if relErr > 0.05 {
		t.Errorf("RangeMedian: got %.1f, want ~%.1f (rel err %.2f%%)", median, want, relErr*100)
	}
}

func TestRangeQuantile_P90(t *testing.T) {
	const N = 500
	s, q := seedQuantileStore(t, N)
	p90, err := s.RangeQuantile(context.Background(), q, 0.9)
	if err != nil {
		t.Fatalf("RangeQuantile(0.9): %v", err)
	}
	want := float64(N) * 0.9
	relErr := math.Abs(p90-want) / want
	if relErr > 0.05 {
		t.Errorf("RangeQuantile(0.9): got %.1f, want ~%.1f (rel err %.2f%%)", p90, want, relErr*100)
	}
}

func BenchmarkRangeMedian(b *testing.B) {
	s, from, to := benchSeedStore(b)
	q := RangeNumQuery{
		Timeline: 1,
		Dims:     []uint64{1},
		From:     from,
		To:       to,
		NumField: 0,
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.RangeMedian(context.Background(), q)
	}
}
