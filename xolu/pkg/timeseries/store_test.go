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

func testStoreConfig() StoreConfig {
	return StoreConfig{DefaultRetentionDays: 30}
}

func testPebbleConfig() PebbleConfig {
	return PebbleConfig{
		MemtableSize:          4 * 1024 * 1024,
		BlockSize:             4096,
		Compression:           "snappy",
		L0CompactionThreshold: 4,
		MaxOpenFiles:          50,
	}
}

func mustOpenStore(t *testing.T) Store {
	t.Helper()
	store, err := NewPebbleStore(t.TempDir(), testStoreConfig(), testPebbleConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func mustDefine(t *testing.T, store Store, id TimelineID, dims uint8) {
	t.Helper()
	if err := store.DefineTimeline(id, TimelineConfig{Name: "test", Dims: dims}); err != nil {
		t.Fatalf("DefineTimeline %d: %v", id, err)
	}
}

var base = time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)

// --- Codec round-trip ---

func TestCodec_KeyRoundTrip(t *testing.T) {
	for dims := uint8(1); dims <= 5; dims++ {
		dv := make([]uint64, dims)
		for i := range dv {
			dv[i] = uint64(i+1) * 1000
		}
		key, err := EncodeKey(TimelineID(1), dims, dv, base)
		if err != nil {
			t.Fatalf("dims=%d EncodeKey: %v", dims, err)
		}
		if len(key) != KeySize(dims) {
			t.Fatalf("dims=%d: key len %d, want %d", dims, len(key), KeySize(dims))
		}
		gotTID, gotDV, gotTS, err := DecodeKey(key, dims)
		if err != nil {
			t.Fatalf("dims=%d DecodeKey: %v", dims, err)
		}
		if gotTID != 1 {
			t.Errorf("dims=%d: tid %d, want 1", dims, gotTID)
		}
		for i, d := range dv {
			if gotDV[i] != d {
				t.Errorf("dims=%d: dv[%d] %d, want %d", dims, i, gotDV[i], d)
			}
		}
		if !gotTS.Equal(base) {
			t.Errorf("dims=%d: ts %v, want %v", dims, gotTS, base)
		}
	}
}

func TestCodec_ValueRoundTrip(t *testing.T) {
	cases := []struct {
		nums    []float64
		payload []byte
	}{
		{nil, nil},
		{[]float64{1.5}, nil},
		{[]float64{1.5, 2.5, 3.5}, nil},
		{[]float64{1.5, 2.5}, []byte(`{"unit":"°C"}`)},
		{nil, []byte("raw payload")},
		{[]float64{math.MaxFloat64, -math.MaxFloat64, 0}, []byte("p")},
	}
	for _, tc := range cases {
		val, err := EncodeValue(tc.nums, tc.payload)
		if err != nil {
			t.Fatalf("EncodeValue: %v", err)
		}
		gotNums, gotPayload, err := DecodeValue(val)
		if err != nil {
			t.Fatalf("DecodeValue: %v", err)
		}
		if len(gotNums) != len(tc.nums) {
			t.Fatalf("nums len %d, want %d", len(gotNums), len(tc.nums))
		}
		for i, v := range tc.nums {
			if gotNums[i] != v {
				t.Errorf("num[%d]: %v, want %v", i, gotNums[i], v)
			}
		}
		if string(gotPayload) != string(tc.payload) {
			t.Errorf("payload %q, want %q", gotPayload, tc.payload)
		}
	}
}

func TestCodec_NaNRejected(t *testing.T) {
	_, err := EncodeValue([]float64{math.NaN()}, nil)
	if err == nil {
		t.Fatal("expected error for NaN, got nil")
	}
}

// --- Registry ---

func TestRegistry_DimsImmutableAfterFirstWrite(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	if err := store.Append(context.Background(), Event{
		Timeline: 1,
		Dims:     []uint64{1, 2},
		Time:     base,
		Nums:     []float64{1.0},
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	// Try to redefine with different dims — must fail.
	err := store.DefineTimeline(1, TimelineConfig{Dims: 3})
	if err == nil {
		t.Fatal("expected error when changing dims after first write, got nil")
	}
}

func TestRegistry_DefaultRetentionDays(t *testing.T) {
	store := mustOpenStore(t)
	if got := store.DefaultRetentionDays(); got != 30 {
		t.Errorf("default retention %d, want 30", got)
	}
	if err := store.SetDefaultRetentionDays(90); err != nil {
		t.Fatalf("SetDefaultRetentionDays: %v", err)
	}
	if got := store.DefaultRetentionDays(); got != 90 {
		t.Errorf("after set: %d, want 90", got)
	}
}

// --- Append + QueryRange (full prefix) ---

func TestStore_AppendAndQueryRange_FullPrefix(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	events := []Event{
		{Timeline: 1, Dims: []uint64{42, 7}, Time: base, Nums: []float64{22.5}},
		{Timeline: 1, Dims: []uint64{42, 7}, Time: base.Add(time.Minute), Nums: []float64{23.0}},
		{Timeline: 1, Dims: []uint64{42, 7}, Time: base.Add(2 * time.Minute), Nums: []float64{23.5}},
	}
	for _, e := range events {
		if err := store.Append(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{42, 7},
		From: base, To: base.Add(2 * time.Minute),
	})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	for i, e := range got {
		if e.Nums[0] != events[i].Nums[0] {
			t.Errorf("event[%d] num0: %v, want %v", i, e.Nums[0], events[i].Nums[0])
		}
	}
}

// --- Partial prefix: time filter correctness (Bug 2 regression) ---

func TestStore_QueryRange_PartialPrefix_TimeFilter(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	// d0=42, d1=1: one event inside query window, one outside.
	// d0=42, d1=5: one event well outside the query window.
	// A partial prefix query on dims=[42] must NOT return the out-of-window events.
	queryFrom := base.Add(30 * time.Minute)
	queryTo := base.Add(90 * time.Minute)

	events := []struct {
		d1      uint64
		ts      time.Time
		inRange bool
	}{
		{1, base.Add(60 * time.Minute), true},   // d1=1, inside
		{1, base.Add(120 * time.Minute), false},  // d1=1, after To
		{5, base.Add(10 * time.Minute), false},   // d1=5, before From
		{5, base.Add(60 * time.Minute), true},    // d1=5, inside
	}
	for _, e := range events {
		if err := store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{42, e.d1}, Time: e.ts, Nums: []float64{1.0},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{42}, // partial prefix
		From: queryFrom, To: queryTo,
	})
	if err != nil {
		t.Fatalf("QueryRange partial: %v", err)
	}
	wantCount := 0
	for _, e := range events {
		if e.inRange {
			wantCount++
		}
	}
	if len(got) != wantCount {
		t.Errorf("partial prefix: got %d events, want %d", len(got), wantCount)
		for _, e := range got {
			t.Logf("  returned: dims=%v ts=%v", e.Dims, e.Time)
		}
	}
}

// --- Purge: correctness across multiple dimension values (Bug 1 regression) ---

func TestStore_Purge_MultiDimTimeline(t *testing.T) {
	cfg := testStoreConfig()
	cfg.DefaultRetentionDays = 0 // no store-level default
	store, err := NewPebbleStore(t.TempDir(), cfg, testPebbleConfig())
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	// Define timeline with 7-day retention.
	if err := store.DefineTimeline(1, TimelineConfig{Dims: 2, RetentionDays: 7}); err != nil {
		t.Fatalf("define: %v", err)
	}

	ctx := context.Background()
	now := time.Now().UTC()
	old := now.Add(-10 * 24 * time.Hour) // 10 days ago — should be purged
	recent := now.Add(-1 * 24 * time.Hour) // 1 day ago — should stay

	// Write old events at d1=1 and d1=2 (different dimension values).
	// Write recent events at d1=1 and d1=2.
	toWrite := []struct {
		d1   uint64
		ts   time.Time
		keep bool
	}{
		{1, old, false},
		{1, recent, true},
		{2, old, false},    // Bug 1: purge used to miss this after seeing recent at d1=1
		{2, recent, true},
	}
	for _, e := range toWrite {
		if err := store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{42, e.d1}, Time: e.ts, Nums: []float64{1.0},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	if err := store.Purge(ctx); err != nil {
		t.Fatalf("purge: %v", err)
	}

	// Query all events — should only find the two recent ones.
	got, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{42},
		From: old.Add(-time.Hour), To: now.Add(time.Hour),
	})
	if err != nil {
		t.Fatalf("post-purge query: %v", err)
	}
	kept := 0
	for _, e := range toWrite {
		if e.keep {
			kept++
		}
	}
	if len(got) != kept {
		t.Errorf("after purge: got %d events, want %d", len(got), kept)
		for _, e := range got {
			t.Logf("  remaining: dims=%v ts=%v", e.Dims, e.Time)
		}
	}
}

// --- Latest ---

func TestStore_Latest(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		if err := store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{42, 7}, Time: base.Add(time.Duration(i) * time.Minute),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	got, err := store.Latest(ctx, LatestQuery{Timeline: 1, Dims: []uint64{42, 7}, N: 3})
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d events, want 3", len(got))
	}
	// Latest returns in reverse-chronological order.
	if !got[0].Time.After(got[1].Time) {
		t.Errorf("Latest not reverse-chronological: [0]=%v [1]=%v", got[0].Time, got[1].Time)
	}
}

// --- Aggregate ---

func TestStore_Aggregate_Scalar(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	for i := 1; i <= 4; i++ {
		if err := store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{42, 7},
			Time: base.Add(time.Duration(i) * time.Minute),
			Nums: []float64{float64(i * 10)},
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	// nums: 10, 20, 30, 40 → avg=25, sum=100, min=10, max=40, count=4

	cases := []struct {
		fn   string
		want float64
	}{
		{"avg", 25},
		{"sum", 100},
		{"min", 10},
		{"max", 40},
		{"count", 4},
	}
	for _, tc := range cases {
		buckets, err := store.Aggregate(ctx, AggregateQuery{
			Timeline: 1, Dims: []uint64{42, 7},
			From: base, To: base.Add(10 * time.Minute),
			NumField: 0, Function: tc.fn,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.fn, err)
		}
		if len(buckets) != 1 {
			t.Fatalf("%s: got %d buckets, want 1", tc.fn, len(buckets))
		}
		if buckets[0].Value != tc.want {
			t.Errorf("%s: got %v, want %v", tc.fn, buckets[0].Value, tc.want)
		}
	}
}

func TestStore_Aggregate_Bucketed(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	// 2 events in hour 0, 2 events in hour 1.
	for i, ts := range []time.Time{
		base, base.Add(10 * time.Minute),
		base.Add(time.Hour), base.Add(time.Hour + 10*time.Minute),
	} {
		if err := store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1, 1}, Time: ts,
			Nums: []float64{float64((i + 1) * 10)}, // 10,20,30,40
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
	}

	buckets, err := store.Aggregate(ctx, AggregateQuery{
		Timeline: 1, Dims: []uint64{1, 1},
		From: base, To: base.Add(2 * time.Hour),
		NumField: 0, Function: "sum", Interval: time.Hour,
	})
	if err != nil {
		t.Fatalf("Aggregate: %v", err)
	}
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}
	if buckets[0].Value != 30 { // 10+20
		t.Errorf("bucket[0] sum: %v, want 30", buckets[0].Value)
	}
	if buckets[1].Value != 70 { // 30+40
		t.Errorf("bucket[1] sum: %v, want 70", buckets[1].Value)
	}
}

// --- AppendBatch ---

func TestStore_AppendBatch(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 1)
	ctx := context.Background()

	events := make([]Event, 10)
	for i := range events {
		events[i] = Event{
			Timeline: 1, Dims: []uint64{1},
			Time: base.Add(time.Duration(i) * time.Second),
			Nums: []float64{float64(i)},
		}
	}
	n, err := store.AppendBatch(ctx, events, 0)
	if err != nil {
		t.Fatalf("AppendBatch: %v", err)
	}
	if n != 10 {
		t.Errorf("accepted %d, want 10", n)
	}

	got, err := store.QueryRange(ctx, RangeQuery{
		Timeline: 1, Dims: []uint64{1},
		From: base, To: base.Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if len(got) != 10 {
		t.Errorf("got %d events, want 10", len(got))
	}
}

// --- TimelineStats ---

func TestStore_TimelineStats(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 1)
	ctx := context.Background()

	for i := 0; i < 5; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: base.Add(time.Duration(i) * time.Hour),
		})
	}

	stats, err := store.TimelineStats(ctx, 1)
	if err != nil {
		t.Fatalf("TimelineStats: %v", err)
	}
	if stats.TotalEvents != 5 {
		t.Errorf("TotalEvents %d, want 5", stats.TotalEvents)
	}
	if stats.OldestEvent.IsZero() || stats.NewestEvent.IsZero() {
		t.Error("OldestEvent or NewestEvent is zero")
	}
	if !stats.OldestEvent.Before(stats.NewestEvent) {
		t.Errorf("OldestEvent %v should be before NewestEvent %v", stats.OldestEvent, stats.NewestEvent)
	}
}

// --- Scan limit ---

func TestStore_QueryRange_ScanLimitExceeded(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 1)
	ctx := context.Background()

	for i := 0; i < 20; i++ {
		store.Append(ctx, Event{
			Timeline: 1, Dims: []uint64{1},
			Time: base.Add(time.Duration(i) * time.Minute),
		})
	}

	_, err := store.QueryRange(ctx, RangeQuery{
		Timeline:      1,
		Dims:          []uint64{1},
		From:          base,
		To:            base.Add(time.Hour),
		MaxScanEvents: 5, // only 5 allowed; 20 exist
	})
	if err == nil {
		t.Fatal("expected ErrScanLimitExceeded, got nil")
	}
}

// --- Validation ---

func TestStore_Validation(t *testing.T) {
	store := mustOpenStore(t)
	mustDefine(t, store, 1, 2)
	ctx := context.Background()

	// Reserved timeline ID
	err := store.Append(ctx, Event{Timeline: 0, Dims: []uint64{1, 2}, Time: base})
	if err == nil {
		t.Error("expected error for timeline ID 0")
	}

	// Wrong dim count
	err = store.Append(ctx, Event{Timeline: 1, Dims: []uint64{1}, Time: base})
	if err == nil {
		t.Error("expected error for wrong dim count")
	}

	// Pre-epoch timestamp
	err = store.Append(ctx, Event{Timeline: 1, Dims: []uint64{1, 2}, Time: time.Unix(-1, 0)})
	if err == nil {
		t.Error("expected error for pre-epoch timestamp")
	}

	// Undefined timeline
	err = store.Append(ctx, Event{Timeline: 99, Dims: []uint64{1, 2}, Time: base})
	if err == nil {
		t.Error("expected error for undefined timeline")
	}
}
