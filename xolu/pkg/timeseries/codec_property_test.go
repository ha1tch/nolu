// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

// codec_property_test.go
//
// Property-style tests for the key/value codec. Rather than fixed examples,
// these generate inputs across the full valid parameter space and assert
// encode→decode round-trip identity. Edge cases that example-based tests
// tend to miss are covered explicitly at the end.

import (
	"bytes"
	"math"
	"testing"
	"time"
)

// --- Round-trip properties ---

// TestCodecProperty_KeyRoundTrip_AllDims encodes and decodes keys for every
// valid dimension count (1–5) using boundary and mid-range values.
func TestCodecProperty_KeyRoundTrip_AllDims(t *testing.T) {
	ts := time.Date(2024, 6, 15, 10, 30, 0, 999999999, time.UTC)

	dimCases := [][]uint64{
		{0},
		{math.MaxUint64},
		{1, 2},
		{0, math.MaxUint64},
		{1, 2, 3},
		{math.MaxUint64, 0, math.MaxUint64},
		{1, 2, 3, 4},
		{0, 0, 0, 0},
		{1, 2, 3, 4, 5},
		{math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64, math.MaxUint64},
	}

	for _, dv := range dimCases {
		dims := uint8(len(dv))
		tid := TimelineID(0x1234)

		key, err := EncodeKey(tid, dims, dv, ts)
		if err != nil {
			t.Fatalf("dims=%d dv=%v: EncodeKey: %v", dims, dv, err)
		}
		if len(key) != KeySize(dims) {
			t.Fatalf("dims=%d: key len %d, want %d", dims, len(key), KeySize(dims))
		}

		gotTID, gotDV, gotTS, err := DecodeKey(key, dims)
		if err != nil {
			t.Fatalf("dims=%d dv=%v: DecodeKey: %v", dims, dv, err)
		}
		if gotTID != tid {
			t.Errorf("dims=%d: tid %d, want %d", dims, gotTID, tid)
		}
		for i, d := range dv {
			if gotDV[i] != d {
				t.Errorf("dims=%d dv[%d]: got %d, want %d", dims, i, gotDV[i], d)
			}
		}
		// Timestamp round-trips to nanosecond precision.
		if !gotTS.Equal(ts) {
			t.Errorf("dims=%d: ts %v, want %v", dims, gotTS, ts)
		}
	}
}

// TestCodecProperty_KeyOrdering verifies that keys sort correctly within a
// single timeline: dimension-major, then time-ascending. This is the ordering
// property the scan logic depends on.
func TestCodecProperty_KeyOrdering(t *testing.T) {
	tid := TimelineID(1)
	t0 := time.Unix(1_000_000, 0).UTC()
	t1 := time.Unix(2_000_000, 0).UTC()
	t2 := time.Unix(3_000_000, 0).UTC()

	// dims=2: keys should sort as [d0][d1][ts] lexicographically.
	type keyCase struct {
		dv []uint64
		ts time.Time
	}
	// In correct sort order:
	cases := []keyCase{
		{[]uint64{1, 1}, t0},
		{[]uint64{1, 1}, t1},
		{[]uint64{1, 2}, t0},
		{[]uint64{2, 1}, t2},
		{[]uint64{2, 2}, t0},
	}

	keys := make([][]byte, len(cases))
	for i, c := range cases {
		k, err := EncodeKey(tid, 2, c.dv, c.ts)
		if err != nil {
			t.Fatalf("EncodeKey %d: %v", i, err)
		}
		keys[i] = k
	}

	for i := 1; i < len(keys); i++ {
		if bytes.Compare(keys[i-1], keys[i]) >= 0 {
			t.Errorf("key[%d] >= key[%d]: sort invariant violated\n  k[%d]=%x\n  k[%d]=%x",
				i-1, i, i-1, keys[i-1], i, keys[i])
		}
	}
}

// TestCodecProperty_PrefixKeyIsStrictPrefix verifies that EncodePrefixKey
// output is a true byte prefix of the corresponding full key.
func TestCodecProperty_PrefixKeyIsStrictPrefix(t *testing.T) {
	tid := TimelineID(7)
	ts := time.Unix(1_000_000, 0).UTC()
	dv := []uint64{42, 99, 7}

	fullKey, err := EncodeKey(tid, 3, dv, ts)
	if err != nil {
		t.Fatalf("EncodeKey: %v", err)
	}

	for prefixLen := 1; prefixLen <= 3; prefixLen++ {
		prefix := EncodePrefixKey(tid, dv[:prefixLen])
		if !bytes.HasPrefix(fullKey, prefix) {
			t.Errorf("prefix len=%d: %x is not a prefix of %x", prefixLen, prefix, fullKey)
		}
	}
}

// TestCodecProperty_ValueRoundTrip_Matrix covers the cross-product of
// num counts (0–7) and payload presence (absent / non-empty).
func TestCodecProperty_ValueRoundTrip_Matrix(t *testing.T) {
	payload := []byte(`{"unit":"°C","sensor":"s1"}`)

	for numCount := 0; numCount <= 7; numCount++ {
		nums := make([]float64, numCount)
		for i := range nums {
			nums[i] = float64(i+1) * 1.1
		}

		for _, pld := range [][]byte{nil, payload} {
			val, err := EncodeValue(nums, pld)
			if err != nil {
				t.Fatalf("numCount=%d payload=%v: EncodeValue: %v", numCount, pld != nil, err)
			}

			gotNums, gotPayload, err := DecodeValue(val)
			if err != nil {
				t.Fatalf("numCount=%d payload=%v: DecodeValue: %v", numCount, pld != nil, err)
			}

			if len(gotNums) != numCount {
				t.Errorf("numCount=%d: got %d nums", numCount, len(gotNums))
				continue
			}
			for i, v := range nums {
				if gotNums[i] != v {
					t.Errorf("numCount=%d: num[%d] = %v, want %v", numCount, i, gotNums[i], v)
				}
			}
			if !bytes.Equal(gotPayload, pld) {
				t.Errorf("numCount=%d: payload mismatch", numCount)
			}
		}
	}
}

// TestCodecProperty_ValueRoundTrip_SpecialFloats verifies that ±Inf and
// extreme finite values survive the encode→decode cycle.
func TestCodecProperty_ValueRoundTrip_SpecialFloats(t *testing.T) {
	// Must stay within maxNums=7.
	cases := []float64{
		0,
		1,
		-1,
		math.MaxFloat64,
		-math.MaxFloat64,
		math.Inf(1),
		math.Inf(-1),
	}

	val, err := EncodeValue(cases, nil)
	if err != nil {
		t.Fatalf("EncodeValue special floats: %v", err)
	}
	got, _, err := DecodeValue(val)
	if err != nil {
		t.Fatalf("DecodeValue special floats: %v", err)
	}
	for i, want := range cases {
		if math.IsInf(want, 1) && math.IsInf(got[i], 1) {
			continue
		}
		if math.IsInf(want, -1) && math.IsInf(got[i], -1) {
			continue
		}
		if got[i] != want {
			t.Errorf("special float[%d]: got %v, want %v", i, got[i], want)
		}
	}
}

// --- Edge cases ---

// TestCodecEdge_EpochZeroTimestamp verifies that Unix epoch zero is a valid
// key timestamp.
func TestCodecEdge_EpochZeroTimestamp(t *testing.T) {
	ts := time.Unix(0, 0).UTC()
	key, err := EncodeKey(1, 1, []uint64{0}, ts)
	if err != nil {
		t.Fatalf("epoch zero: EncodeKey: %v", err)
	}
	_, _, gotTS, err := DecodeKey(key, 1)
	if err != nil {
		t.Fatalf("epoch zero: DecodeKey: %v", err)
	}
	if !gotTS.Equal(ts) {
		t.Errorf("epoch zero: ts %v, want %v", gotTS, ts)
	}
}

// TestCodecEdge_PreEpochRejected verifies that a pre-epoch timestamp is
// rejected at encode time.
func TestCodecEdge_PreEpochRejected(t *testing.T) {
	_, err := EncodeKey(1, 1, []uint64{0}, time.Unix(-1, 0))
	if err == nil {
		t.Error("expected error for pre-epoch timestamp, got nil")
	}
}

// TestCodecEdge_MaxTimelineID verifies that the maximum timeline ID
// (0xFFFF) encodes and decodes correctly.
func TestCodecEdge_MaxTimelineID(t *testing.T) {
	key, err := EncodeKey(MaxTimelineID, 1, []uint64{1}, time.Unix(1_000_000, 0).UTC())
	if err != nil {
		t.Fatalf("MaxTimelineID: EncodeKey: %v", err)
	}
	tid, _, _, err := DecodeKey(key, 1)
	if err != nil {
		t.Fatalf("MaxTimelineID: DecodeKey: %v", err)
	}
	if tid != MaxTimelineID {
		t.Errorf("tid %d, want %d", tid, MaxTimelineID)
	}
}

// TestCodecEdge_MaxPayload verifies that a 65535-byte payload encodes and
// decodes correctly.
func TestCodecEdge_MaxPayload(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 65535)
	val, err := EncodeValue(nil, payload)
	if err != nil {
		t.Fatalf("max payload: EncodeValue: %v", err)
	}
	_, gotPayload, err := DecodeValue(val)
	if err != nil {
		t.Fatalf("max payload: DecodeValue: %v", err)
	}
	if !bytes.Equal(gotPayload, payload) {
		t.Errorf("max payload: mismatch (got %d bytes, want 65535)", len(gotPayload))
	}
}

// TestCodecEdge_OversizePayloadRejected verifies that a 65536-byte payload
// is rejected.
func TestCodecEdge_OversizePayloadRejected(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 65536)
	_, err := EncodeValue(nil, payload)
	if err == nil {
		t.Error("expected error for oversize payload, got nil")
	}
}

// TestCodecEdge_IncrementKey verifies incrementKey behaviour including the
// overflow case.
func TestCodecEdge_IncrementKey(t *testing.T) {
	// Normal case: last byte increments.
	key := []byte{0x01, 0x02, 0x03}
	got := incrementKey(key)
	want := []byte{0x01, 0x02, 0x04}
	if !bytes.Equal(got, want) {
		t.Errorf("incrementKey normal: %x, want %x", got, want)
	}

	// Carry propagation.
	key = []byte{0x01, 0x02, 0xFF}
	got = incrementKey(key)
	want = []byte{0x01, 0x03, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("incrementKey carry: %x, want %x", got, want)
	}

	// All-0xFF: overflow returns nil (open upper bound).
	key = []byte{0xFF, 0xFF, 0xFF}
	got = incrementKey(key)
	if got != nil {
		t.Errorf("incrementKey overflow: %x, want nil", got)
	}
}

// TestCodecEdge_DecodeKeyWrongLength verifies that DecodeKey returns an
// error if the key length doesn't match the declared dims.
func TestCodecEdge_DecodeKeyWrongLength(t *testing.T) {
	key := make([]byte, KeySize(1)) // right for dims=1
	if _, _, _, err := DecodeKey(key, 2); err == nil {
		t.Error("expected error for wrong key length, got nil")
	}
}

// TestCodecEdge_DecodeTimestamp_TooShort verifies DecodeTimestamp error
// on a truncated key.
func TestCodecEdge_DecodeTimestamp_TooShort(t *testing.T) {
	key := make([]byte, 4) // too short for any dims
	if _, err := DecodeTimestamp(key, 1); err == nil {
		t.Error("expected error for short key, got nil")
	}
}

// TestCodecEdge_DimMismatchRejected verifies that EncodeKey rejects a
// dims argument that doesn't match the length of the dimension slice.
func TestCodecEdge_DimMismatchRejected(t *testing.T) {
	_, err := EncodeKey(1, 2, []uint64{1}, time.Unix(1_000_000, 0).UTC()) // dims=2 but 1 value
	if err == nil {
		t.Error("expected error for dim mismatch, got nil")
	}
}
