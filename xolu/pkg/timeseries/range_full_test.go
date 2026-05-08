// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"context"
	"math"
	"testing"
	"time"
)

func seedFullStore(t *testing.T, n int) (Store, RangeFullQuery) {
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
	q := RangeFullQuery{
		RangeAllQuery: RangeAllQuery{
			Timeline: 1,
			Dims:     []uint64{1},
			From:     base.Add(-time.Second),
			To:       base.Add(time.Duration(n+1) * time.Second),
		},
	}
	return store, q
}

func TestRangeFullAggregate_InvalidQuantile(t *testing.T) {
	s, q := seedFullStore(t, 100)
	q.Quantiles = []float64{-0.1}
	if _, err := s.RangeFullAggregate(context.Background(), q); err == nil {
		t.Error("expected error for quantile < 0")
	}
	q.Quantiles = []float64{1.1}
	if _, err := s.RangeFullAggregate(context.Background(), q); err == nil {
		t.Error("expected error for quantile > 1")
	}
}

func TestRangeFullAggregate_InvalidQuantileField(t *testing.T) {
	s, q := seedFullStore(t, 100)
	q.Quantiles = []float64{0.5}
	q.QuantileFields = []uint8{7}
	if _, err := s.RangeFullAggregate(context.Background(), q); err == nil {
		t.Error("expected error for QuantileField=7")
	}
}

func TestRangeFullAggregate_NoQuantiles_EqualsRangeAggregate(t *testing.T) {
	const N = 300
	s, fq := seedFullStore(t, N)
	ctx := context.Background()
	full, err := s.RangeFullAggregate(ctx, fq)
	if err != nil {
		t.Fatalf("RangeFullAggregate: %v", err)
	}
	agg, err := s.RangeAggregate(ctx, fq.RangeAllQuery)
	if err != nil {
		t.Fatalf("RangeAggregate: %v", err)
	}
	if full.Aggregate.Count != agg.Count {
		t.Errorf("Count: full=%d agg=%d", full.Aggregate.Count, agg.Count)
	}
	if full.Aggregate.Sums[0] != agg.Sums[0] {
		t.Errorf("Sums[0]: full=%g agg=%g", full.Aggregate.Sums[0], agg.Sums[0])
	}
	if full.Aggregate.Mins[0] != agg.Mins[0] {
		t.Errorf("Mins[0]: full=%g agg=%g", full.Aggregate.Mins[0], agg.Mins[0])
	}
	if full.Aggregate.Maxs[0] != agg.Maxs[0] {
		t.Errorf("Maxs[0]: full=%g agg=%g", full.Aggregate.Maxs[0], agg.Maxs[0])
	}
	for i, qs := range full.Quantiles {
		if qs != nil {
			t.Errorf("Quantiles[%d]: expected nil (no quantiles requested), got %v", i, qs)
		}
	}
}

func TestRangeFullAggregate_P50Accuracy(t *testing.T) {
	const N = 500
	s, fq := seedFullStore(t, N)
	fq.Quantiles = []float64{0.5}
	fq.QuantileFields = []uint8{0}
	res, err := s.RangeFullAggregate(context.Background(), fq)
	if err != nil {
		t.Fatalf("RangeFullAggregate: %v", err)
	}
	if len(res.Quantiles[0]) != 1 {
		t.Fatalf("Quantiles[0]: expected 1 estimate, got %d", len(res.Quantiles[0]))
	}
	got := res.Quantiles[0][0]
	want := float64(N) / 2
	relErr := math.Abs(got-want) / want
	if relErr > 0.05 {
		t.Errorf("P50: got %.1f, want ~%.1f (rel err %.2f%%)", got, want, relErr*100)
	}
}

func TestRangeFullAggregate_MultipleQuantiles(t *testing.T) {
	const N = 500
	s, fq := seedFullStore(t, N)
	fq.Quantiles = []float64{0.5, 0.9, 0.99}
	fq.QuantileFields = []uint8{0}
	res, err := s.RangeFullAggregate(context.Background(), fq)
	if err != nil {
		t.Fatalf("RangeFullAggregate: %v", err)
	}
	qs := res.Quantiles[0]
	if len(qs) != 3 {
		t.Fatalf("expected 3 quantile estimates, got %d", len(qs))
	}
	cases := []struct{ q, want float64 }{
		{0.5, float64(N) * 0.5},
		{0.9, float64(N) * 0.9},
		{0.99, float64(N) * 0.99},
	}
	for i, tc := range cases {
		relErr := math.Abs(qs[i]-tc.want) / tc.want
		if relErr > 0.05 {
			t.Errorf("P%.0f: got %.1f, want ~%.1f (rel err %.2f%%)", tc.q*100, qs[i], tc.want, relErr*100)
		}
	}
	if qs[0] > qs[1] || qs[1] > qs[2] {
		t.Errorf("quantiles not monotone: %v", qs)
	}
}

func TestRangeFullAggregate_QuantileFieldsSubset(t *testing.T) {
	store, err := NewPebbleStore(t.TempDir(), testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatal(err)
	}
	store.DefineTimeline(1, TimelineConfig{Dims: 1})
	ctx := context.Background()
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	const N = 200
	events := make([]Event, N)
	for i := range events {
		nums := make([]float64, 4)
		nums[0] = float64(i)
		nums[3] = float64(i) * 2
		events[i] = Event{
			Timeline: 1,
			Dims:     []uint64{1},
			Time:     base.Add(time.Duration(i) * time.Second),
			Nums:     nums,
		}
	}
	store.AppendBatch(ctx, events, 0)
	q := RangeFullQuery{
		RangeAllQuery: RangeAllQuery{
			Timeline: 1,
			Dims:     []uint64{1},
			From:     base.Add(-time.Second),
			To:       base.Add(time.Duration(N+1) * time.Second),
		},
		Quantiles:      []float64{0.5},
		QuantileFields: []uint8{3},
	}
	res, err := store.RangeFullAggregate(ctx, q)
	if err != nil {
		t.Fatalf("RangeFullAggregate: %v", err)
	}
	if !res.Aggregate.Fields[0] {
		t.Error("Fields[0] should be true")
	}
	if res.Quantiles[0] != nil {
		t.Errorf("Quantiles[0]: expected nil (not requested), got %v", res.Quantiles[0])
	}
	if !res.Aggregate.Fields[3] {
		t.Error("Fields[3] should be true")
	}
	if len(res.Quantiles[3]) != 1 {
		t.Fatalf("Quantiles[3]: expected 1 estimate, got %d", len(res.Quantiles[3]))
	}
	want := float64(N) * 2 / 2
	relErr := math.Abs(res.Quantiles[3][0]-want) / want
	if relErr > 0.05 {
		t.Errorf("P50 field 3: got %.1f, want ~%.1f (rel err %.2f%%)", res.Quantiles[3][0], want, relErr*100)
	}
}

func TestRangeFullAggregate_ConsistentWithRangeQuantile(t *testing.T) {
	const N = 500
	s, fq := seedFullStore(t, N)
	ctx := context.Background()
	fq.Quantiles = []float64{0.5, 0.9}
	fq.QuantileFields = []uint8{0}
	full, err := s.RangeFullAggregate(ctx, fq)
	if err != nil {
		t.Fatalf("RangeFullAggregate: %v", err)
	}
	rq := RangeNumQuery{
		Timeline: fq.Timeline,
		Dims:     fq.Dims,
		From:     fq.From,
		To:       fq.To,
		NumField: 0,
	}
	for j, qv := range []float64{0.5, 0.9} {
		standalone, err := s.RangeQuantile(ctx, rq, qv)
		if err != nil {
			t.Fatalf("RangeQuantile(%.2f): %v", qv, err)
		}
		combined := full.Quantiles[0][j]
		if standalone == 0 {
			continue
		}
		relDiff := math.Abs(combined-standalone) / standalone
		if relDiff > 0.01 {
			t.Errorf("P%.0f: combined=%.2f standalone=%.2f (rel diff %.3f%%)",
				qv*100, combined, standalone, relDiff*100)
		}
	}
}

func TestRangeFullAggregate_EmptyRange(t *testing.T) {
	s, fq := seedFullStore(t, 100)
	fq.RangeAllQuery.From = fq.RangeAllQuery.From.Add(-48 * time.Hour)
	fq.RangeAllQuery.To = fq.RangeAllQuery.From.Add(time.Hour)
	fq.Quantiles = []float64{0.5}
	fq.QuantileFields = []uint8{0}
	res, err := s.RangeFullAggregate(context.Background(), fq)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res.Aggregate.Count != 0 {
		t.Errorf("empty range: Count=%d want 0", res.Aggregate.Count)
	}
	if res.Quantiles[0] != nil {
		t.Errorf("empty range: Quantiles[0] should be nil, got %v", res.Quantiles[0])
	}
}

func TestRangeFullAggregate_ScanLimit(t *testing.T) {
	s, fq := seedFullStore(t, 200)
	fq.RangeAllQuery.MaxScanEvents = 50
	fq.Quantiles = []float64{0.5}
	fq.QuantileFields = []uint8{0}
	_, err := s.RangeFullAggregate(context.Background(), fq)
	if err != ErrScanLimitExceeded {
		t.Errorf("expected ErrScanLimitExceeded, got %v", err)
	}
}

func BenchmarkRangeFullAggregate_WithMedian(b *testing.B) {
	s, from, to := benchSeedStore(b)
	q := RangeFullQuery{
		RangeAllQuery: RangeAllQuery{
			Timeline: 1,
			Dims:     []uint64{1},
			From:     from,
			To:       to,
		},
		Quantiles:      []float64{0.5},
		QuantileFields: []uint8{0},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.RangeFullAggregate(context.Background(), q)
	}
}

func BenchmarkRangeFullAggregate_NoQuantiles(b *testing.B) {
	s, from, to := benchSeedStore(b)
	q := RangeFullQuery{
		RangeAllQuery: RangeAllQuery{
			Timeline: 1,
			Dims:     []uint64{1},
			From:     from,
			To:       to,
		},
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.RangeFullAggregate(context.Background(), q)
	}
}
