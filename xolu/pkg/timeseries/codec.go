// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// Key layout: [timeline_id:2][d0:8][d1:8?]...[dN:8?][ts:8]  (all big-endian)
//
// Total key size in bytes for N dimensions: 2 + N*8 + 8 = 10 + N*8
//   dims=1: 18 bytes
//   dims=2: 26 bytes
//   dims=3: 34 bytes
//   dims=4: 42 bytes
//   dims=5: 50 bytes

// KeySize returns the key size in bytes for a given dimension count.
func KeySize(dims uint8) int {
	return 2 + int(dims)*8 + 8
}

// tsOffset returns the byte offset of the timestamp field for a given dim count.
func tsOffset(dims uint8) int {
	return 2 + int(dims)*8
}

// EncodeKey encodes a Pebble key for the given timeline, dimension values, and timestamp.
// dims must be the timeline's declared dimension count (1–5); len(dv) must equal dims.
func EncodeKey(tid TimelineID, dims uint8, dv []uint64, ts time.Time) ([]byte, error) {
	if int(dims) != len(dv) {
		return nil, fmt.Errorf("timeseries: EncodeKey: dims=%d but %d dimension values provided", dims, len(dv))
	}
	if ts.Before(time.Unix(0, 0)) {
		return nil, fmt.Errorf("timeseries: EncodeKey: timestamp before Unix epoch")
	}

	key := make([]byte, KeySize(dims))
	binary.BigEndian.PutUint16(key[0:2], uint16(tid))
	for i, d := range dv {
		binary.BigEndian.PutUint64(key[2+i*8:2+i*8+8], d)
	}
	binary.BigEndian.PutUint64(key[tsOffset(dims):], uint64(ts.UnixNano()))
	return key, nil
}

// EncodePrefixKey encodes a key prefix for range scanning using a leading
// dimension slice (1 ≤ len(dv) ≤ dims). The timestamp is not included.
func EncodePrefixKey(tid TimelineID, dv []uint64) []byte {
	prefix := make([]byte, 2+len(dv)*8)
	binary.BigEndian.PutUint16(prefix[0:2], uint16(tid))
	for i, d := range dv {
		binary.BigEndian.PutUint64(prefix[2+i*8:2+i*8+8], d)
	}
	return prefix
}

// DecodeKey decodes a full key, given the timeline's dimension count.
func DecodeKey(key []byte, dims uint8) (tid TimelineID, dv []uint64, ts time.Time, err error) {
	expected := KeySize(dims)
	if len(key) != expected {
		return 0, nil, time.Time{}, fmt.Errorf("timeseries: DecodeKey: key len %d, expected %d (dims=%d)", len(key), expected, dims)
	}
	tid = TimelineID(binary.BigEndian.Uint16(key[0:2]))
	dv = make([]uint64, dims)
	for i := range dv {
		dv[i] = binary.BigEndian.Uint64(key[2+i*8 : 2+i*8+8])
	}
	ns := binary.BigEndian.Uint64(key[tsOffset(dims):])
	ts = time.Unix(0, int64(ns)).UTC()
	return tid, dv, ts, nil
}

// DecodeTimestamp extracts the timestamp from a key without decoding dimensions.
// Faster than DecodeKey when only the timestamp is needed (e.g. in Purge).
func DecodeTimestamp(key []byte, dims uint8) (time.Time, error) {
	off := tsOffset(dims)
	if len(key) < off+8 {
		return time.Time{}, fmt.Errorf("timeseries: DecodeTimestamp: key too short")
	}
	ns := binary.BigEndian.Uint64(key[off : off+8])
	return time.Unix(0, int64(ns)).UTC(), nil
}

// --- Value encoding ---
//
// Value layout:
//   [flags:1][num0:8?][num1:8?]...[num6:8?][payload_len:2?][payload:?]
//
// Flags byte: bits 0–6 indicate which num fields are present; bit 7 indicates payload.

const (
	maxNums        = 7
	flagPayload    = uint8(1 << 7)
)

func numFlag(i int) uint8 { return uint8(1 << uint(i)) }

// EncodeValue encodes nums and payload into a compact binary value.
// len(nums) must be 0–7. NaN values are rejected.
func EncodeValue(nums []float64, payload []byte) ([]byte, error) {
	if len(nums) > maxNums {
		return nil, fmt.Errorf("timeseries: EncodeValue: at most %d numeric fields, got %d", maxNums, len(nums))
	}
	for i, v := range nums {
		if math.IsNaN(v) {
			return nil, fmt.Errorf("timeseries: EncodeValue: NaN in num%d (OLU-TS017)", i)
		}
	}
	if len(payload) > 65535 {
		return nil, fmt.Errorf("timeseries: EncodeValue: payload too large (%d bytes, max 65535)", len(payload))
	}

	var flags uint8
	for i := range nums {
		flags |= numFlag(i)
	}
	if len(payload) > 0 {
		flags |= flagPayload
	}

	size := 1 + len(nums)*8
	if len(payload) > 0 {
		size += 2 + len(payload)
	}

	val := make([]byte, size)
	val[0] = flags
	pos := 1
	for _, v := range nums {
		binary.BigEndian.PutUint64(val[pos:pos+8], math.Float64bits(v))
		pos += 8
	}
	if len(payload) > 0 {
		binary.BigEndian.PutUint16(val[pos:pos+2], uint16(len(payload)))
		pos += 2
		copy(val[pos:], payload)
	}
	return val, nil
}

// DecodeValue decodes a value encoded by EncodeValue.
func DecodeValue(val []byte) (nums []float64, payload []byte, err error) {
	if len(val) < 1 {
		return nil, nil, fmt.Errorf("timeseries: DecodeValue: empty value")
	}
	flags := val[0]
	pos := 1

	for i := 0; i < maxNums; i++ {
		if flags&numFlag(i) != 0 {
			if pos+8 > len(val) {
				return nil, nil, fmt.Errorf("timeseries: DecodeValue: truncated at num%d", i)
			}
			nums = append(nums, math.Float64frombits(binary.BigEndian.Uint64(val[pos:pos+8])))
			pos += 8
		}
	}

	if flags&flagPayload != 0 {
		if pos+2 > len(val) {
			return nil, nil, fmt.Errorf("timeseries: DecodeValue: truncated at payload_len")
		}
		plen := int(binary.BigEndian.Uint16(val[pos : pos+2]))
		pos += 2
		if pos+plen > len(val) {
			return nil, nil, fmt.Errorf("timeseries: DecodeValue: truncated at payload body")
		}
		payload = val[pos : pos+plen]
	}
	return nums, payload, nil
}
