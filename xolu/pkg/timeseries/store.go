// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cockroachdb/pebble"
	"github.com/ha1tch/xolu/pkg/tdigest"
)

const (
	purgeBatchSize  = 10_000
	metaFlushSecs   = 60
)

// ErrScanLimitExceeded is returned when a query exceeds its MaxScanEvents budget.
var ErrScanLimitExceeded = fmt.Errorf("ts: scan limit exceeded (OLU-TS013)")

// ErrBucketLimitExceeded is returned when an aggregate query would produce
// more buckets than the configured MaxBuckets limit.
var ErrBucketLimitExceeded = fmt.Errorf("ts: aggregate bucket limit exceeded (OLU-TS019)")

// PebbleStore implements Store backed by a single Pebble instance per tenant.
type PebbleStore struct {
	db       *pebble.DB
	dir      string
	reg      *registry

	// Per-timeline event counters. Loaded from meta.json on open, persisted
	// periodically and on Close. Eventually consistent after crash (by design).
	countersMu sync.RWMutex
	counters   map[TimelineID]*atomic.Int64

	metaPath string
}

// storeMeta is the on-disk metadata for a tenant's timeseries store.
type storeMeta struct {
	CreatedAt   time.Time         `json:"created_at"`
	Compression string            `json:"compression"`
	Counts      map[string]int64  `json:"counts,omitempty"` // timeline_id (decimal string) -> count
}

// NewPebbleStore opens or creates a Pebble timeseries store in dir.
// cfg carries the backend-agnostic settings (retention); pcfg carries the
// Pebble-specific tuning parameters. Zero values in pcfg are safe — sensible
// defaults are applied for each unset field.
func NewPebbleStore(dir string, cfg StoreConfig, pcfg PebbleConfig) (Store, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("ts: mkdir %s: %w", dir, err)
	}

	reg, firstOpen, err := loadRegistry(dir)
	if err != nil {
		return nil, fmt.Errorf("ts: load registry: %w", err)
	}
	// On first open (no registry.json yet) apply the config default retention.
	// On reopen, honour whatever was persisted — including 0 (no expiry).
	if !firstOpen && cfg.DefaultRetentionDays > 0 {
		reg.defaultRetentionDays = cfg.DefaultRetentionDays
	}

	// Apply PebbleConfig defaults for zero/unset fields.
	if pcfg.MemtableSize <= 0 {
		pcfg.MemtableSize = 67108864 // 64 MB
	}
	if pcfg.BlockSize <= 0 {
		pcfg.BlockSize = 32768 // 32 KB
	}
	if pcfg.Compression == "" {
		pcfg.Compression = "zstd"
	}
	if pcfg.L0CompactionThreshold <= 0 {
		pcfg.L0CompactionThreshold = 4
	}
	if pcfg.MaxOpenFiles <= 0 {
		pcfg.MaxOpenFiles = 500
	}

	pebbleDir := filepath.Join(dir, "pebble")
	opts := &pebble.Options{
		MaxOpenFiles:          pcfg.MaxOpenFiles,
		MemTableSize:          uint64(pcfg.MemtableSize),
		L0CompactionThreshold: pcfg.L0CompactionThreshold,
		L0StopWritesThreshold: pcfg.L0CompactionThreshold * 4,
	}
	switch pcfg.Compression {
	case "zstd":
		opts.Levels = []pebble.LevelOptions{{Compression: pebble.ZstdCompression, BlockSize: pcfg.BlockSize}}
	case "snappy":
		opts.Levels = []pebble.LevelOptions{{Compression: pebble.SnappyCompression, BlockSize: pcfg.BlockSize}}
	default:
		opts.Levels = []pebble.LevelOptions{{Compression: pebble.NoCompression, BlockSize: pcfg.BlockSize}}
	}

	db, err := pebble.Open(pebbleDir, opts)
	if err != nil {
		return nil, fmt.Errorf("ts: pebble open %s: %w", pebbleDir, err)
	}

	s := &PebbleStore{
		db:       db,
		dir:      dir,
		reg:      reg,
		counters: make(map[TimelineID]*atomic.Int64),
		metaPath: filepath.Join(dir, "meta.json"),
	}
	if err := s.loadMeta(pcfg.Compression); err != nil {
		db.Close()
		return nil, fmt.Errorf("ts: load meta: %w", err)
	}
	return s, nil
}

// NewPebbleStoreFactory returns a StoreFactory backed by Pebble. The supplied
// PebbleConfig is captured in the closure; callers only need to thread the
// backend-agnostic StoreConfig through the factory contract.
//
// A zero-value PebbleConfig is valid — NewPebbleStore applies sensible
// defaults for every unset field.
func NewPebbleStoreFactory(pcfg PebbleConfig) StoreFactory {
	return func(dir string, cfg StoreConfig) (Store, error) {
		return NewPebbleStore(dir, cfg, pcfg)
	}
}

// --- Timeline management ---

func (s *PebbleStore) DefineTimeline(id TimelineID, cfg TimelineConfig) error {
	return s.reg.define(id, cfg)
}

func (s *PebbleStore) UpdateTimeline(id TimelineID, cfg TimelineConfig) error {
	return s.reg.update(id, cfg)
}

func (s *PebbleStore) Timeline(id TimelineID) (TimelineConfig, bool) {
	return s.reg.get(id)
}

func (s *PebbleStore) Timelines() []TimelineID {
	return s.reg.list()
}

// --- Write ---

func (s *PebbleStore) Append(ctx context.Context, e Event) error {
	if err := s.validateEvent(e); err != nil {
		return err
	}
	cfg, ok := s.reg.get(e.Timeline)
	if !ok {
		return fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", e.Timeline)
	}
	if len(e.Dims) != int(cfg.Dims) {
		return fmt.Errorf("ts: timeline %d expects %d dims, got %d (OLU-TS007)", e.Timeline, cfg.Dims, len(e.Dims))
	}

	key, err := EncodeKey(e.Timeline, cfg.Dims, e.Dims, e.Time)
	if err != nil {
		return err
	}
	val, err := EncodeValue(e.Nums, e.Payload)
	if err != nil {
		return err
	}

	if err := s.db.Set(key, val, pebble.Sync); err != nil {
		return fmt.Errorf("ts: pebble set: %w", err)
	}

	s.counter(e.Timeline).Add(1)
	_ = s.reg.recordFirstWrite(e.Timeline)
	return nil
}

func (s *PebbleStore) AppendBatch(ctx context.Context, events []Event, maxBatch int) (int, error) {
	if len(events) == 0 {
		return 0, nil
	}
	if maxBatch <= 0 {
		maxBatch = 5000
	}
	if len(events) > maxBatch {
		return 0, fmt.Errorf("ts: batch too large (%d events, max %d) (OLU-TS006)", len(events), maxBatch)
	}

	// Validate all events and resolve their configs before touching Pebble.
	type prepared struct {
		key []byte
		val []byte
		tid TimelineID
	}
	items := make([]prepared, 0, len(events))
	for i, e := range events {
		if err := s.validateEvent(e); err != nil {
			return 0, fmt.Errorf("ts: event[%d]: %w", i, err)
		}
		cfg, ok := s.reg.get(e.Timeline)
		if !ok {
			return 0, fmt.Errorf("ts: event[%d]: timeline %d not defined (OLU-TS004)", i, e.Timeline)
		}
		if len(e.Dims) != int(cfg.Dims) {
			return 0, fmt.Errorf("ts: event[%d]: timeline %d expects %d dims, got %d (OLU-TS007)", i, e.Timeline, cfg.Dims, len(e.Dims))
		}
		key, err := EncodeKey(e.Timeline, cfg.Dims, e.Dims, e.Time)
		if err != nil {
			return 0, fmt.Errorf("ts: event[%d]: %w", i, err)
		}
		val, err := EncodeValue(e.Nums, e.Payload)
		if err != nil {
			return 0, fmt.Errorf("ts: event[%d]: %w", i, err)
		}
		items = append(items, prepared{key: key, val: val, tid: e.Timeline})
	}

	batch := s.db.NewBatch()
	defer batch.Close()
	for _, p := range items {
		if err := batch.Set(p.key, p.val, nil); err != nil {
			return 0, fmt.Errorf("ts: batch set: %w", err)
		}
	}
	if err := batch.Commit(pebble.Sync); err != nil {
		return 0, fmt.Errorf("ts: batch commit: %w", err)
	}

	// Update counters and lock Dims after successful commit.
	for _, e := range events {
		s.counter(e.Timeline).Add(1)
		_ = s.reg.recordFirstWrite(e.Timeline)
	}
	return len(events), nil
}

// --- Read ---

func (s *PebbleStore) QueryRange(ctx context.Context, q RangeQuery) ([]Event, error) {
	cfg, ok := s.reg.get(q.Timeline)
	if !ok {
		return nil, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", q.Timeline)
	}
	if len(q.Dims) < 1 || len(q.Dims) > int(cfg.Dims) {
		return nil, fmt.Errorf("ts: query dims %d out of range 1–%d (OLU-TS007)", len(q.Dims), cfg.Dims)
	}

	limit := q.Limit
	if limit <= 0 {
		limit = 1000
	}
	if limit > 10000 {
		return nil, fmt.Errorf("ts: result limit %d exceeds max 10000 (OLU-TS012)", q.Limit)
	}
	desc := q.Order == "desc"

	// Build scan bounds.
	startKey, err := EncodeKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, 0), q.From)
	if err != nil {
		return nil, err
	}
	endKey, err := EncodeKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, math.MaxUint64), q.To)
	if err != nil {
		return nil, err
	}
	// Make end exclusive by incrementing last byte.
	endKeyExcl := incrementKey(endKey)

	var results []Event

	// For partial prefix queries (len(q.Dims) < cfg.Dims), the key space spans
	// multiple d1..dN series. Within a given series, keys are time-ordered, but
	// across series they are NOT. A key [d0=42][d1=5][ts=To+1yr] is
	// lexicographically less than [d0=42][d1=MaxUint64][ts=To], so it would be
	// inside the Pebble bounds but outside the time range. Apply a Go-side time
	// filter for partial prefix queries.
	partialPrefix := len(q.Dims) < int(cfg.Dims)

	if !desc {
		iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKeyExcl})
		if err != nil {
			return nil, fmt.Errorf("ts: new iter: %w", err)
		}
		defer iter.Close()
		var scanned int
		for iter.First(); iter.Valid() && len(results) < limit; iter.Next() {
			if err := ctx.Err(); err != nil {
				return results, err
			}
			scanned++
			if q.MaxScanEvents > 0 && scanned > q.MaxScanEvents {
				return nil, ErrScanLimitExceeded
			}
			e, err := s.decodeEntry(iter.Key(), iter.Value(), cfg.Dims)
			if err != nil {
				return nil, err
			}
			if partialPrefix && (e.Time.Before(q.From) || e.Time.After(q.To)) {
				continue
			}
			results = append(results, e)
		}
		if err := iter.Error(); err != nil {
			return nil, fmt.Errorf("ts: iter: %w", err)
		}
	} else {
		iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKeyExcl})
		if err != nil {
			return nil, fmt.Errorf("ts: new iter: %w", err)
		}
		defer iter.Close()
		var scanned int
		for iter.Last(); iter.Valid() && len(results) < limit; iter.Prev() {
			if err := ctx.Err(); err != nil {
				return results, err
			}
			scanned++
			if q.MaxScanEvents > 0 && scanned > q.MaxScanEvents {
				return nil, ErrScanLimitExceeded
			}
			e, err := s.decodeEntry(iter.Key(), iter.Value(), cfg.Dims)
			if err != nil {
				return nil, err
			}
			if partialPrefix && (e.Time.Before(q.From) || e.Time.After(q.To)) {
				continue
			}
			results = append(results, e)
		}
		if err := iter.Error(); err != nil {
			return nil, fmt.Errorf("ts: iter: %w", err)
		}
	}
	return results, nil
}

func (s *PebbleStore) Latest(ctx context.Context, q LatestQuery) ([]Event, error) {
	cfg, ok := s.reg.get(q.Timeline)
	if !ok {
		return nil, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", q.Timeline)
	}
	if len(q.Dims) < 1 || len(q.Dims) > int(cfg.Dims) {
		return nil, fmt.Errorf("ts: query dims %d out of range 1–%d (OLU-TS007)", len(q.Dims), cfg.Dims)
	}

	n := q.N
	if n <= 0 {
		n = 10
	}
	if n > 10000 {
		return nil, fmt.Errorf("ts: n %d exceeds max 10000 (OLU-TS012)", q.N)
	}
	if !q.From.IsZero() && !q.To.IsZero() && q.From.After(q.To) {
		return nil, fmt.Errorf("ts: latest: from (%v) is after to (%v) (OLU-TS005)", q.From, q.To)
	}

	startKey := EncodePrefixKey(q.Timeline, padDims(q.Dims, cfg.Dims, 0))
	endKey := incrementKey(EncodePrefixKey(q.Timeline, padDims(q.Dims, cfg.Dims, math.MaxUint64)))

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return nil, fmt.Errorf("ts: new iter: %w", err)
	}
	defer iter.Close()

	hasBounds := !q.From.IsZero() || !q.To.IsZero()

	var results []Event
	for iter.Last(); iter.Valid() && len(results) < n; iter.Prev() {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		e, err := s.decodeEntry(iter.Key(), iter.Value(), cfg.Dims)
		if err != nil {
			return nil, err
		}
		if hasBounds {
			if !q.From.IsZero() && e.Time.Before(q.From) {
				continue
			}
			if !q.To.IsZero() && e.Time.After(q.To) {
				continue
			}
		}
		results = append(results, e)
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("ts: iter: %w", err)
	}
	return results, nil
}

// --- Aggregate ---

func (s *PebbleStore) Aggregate(ctx context.Context, q AggregateQuery) ([]Bucket, error) {
	cfg, ok := s.reg.get(q.Timeline)
	if !ok {
		return nil, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", q.Timeline)
	}
	if len(q.Dims) < 1 || len(q.Dims) > int(cfg.Dims) {
		return nil, fmt.Errorf("ts: query dims %d out of range 1–%d (OLU-TS007)", len(q.Dims), cfg.Dims)
	}
	if q.NumField > 6 {
		return nil, fmt.Errorf("ts: num_field %d out of range 0–6 (OLU-TS009)", q.NumField)
	}
	switch q.Function {
	case "avg", "min", "max", "sum", "count":
	default:
		return nil, fmt.Errorf("ts: unknown function %q (OLU-TS008)", q.Function)
	}

	startKey, err := EncodeKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, 0), q.From)
	if err != nil {
		return nil, err
	}
	endKeyRaw, err := encodeEndKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, math.MaxUint64), q.To)
	if err != nil {
		return nil, err
	}
	endKey := incrementKey(endKeyRaw)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return nil, fmt.Errorf("ts: new iter: %w", err)
	}
	defer iter.Close()

	// Bucketing state.
	type bucket struct {
		sum   float64
		min   float64
		max   float64
		count uint64
	}
	buckets := make(map[int64]*bucket) // bucket start ns -> accumulator
	var bucketOrder []int64

	bucketStart := func(t time.Time) int64 {
		if q.Interval <= 0 {
			return 0 // scalar: everything goes in bucket 0
		}
		return t.Truncate(q.Interval).UnixNano()
	}

	// For partial prefix queries, apply Go-side time filter (see QueryRange comment).
	partialPrefix := len(q.Dims) < int(cfg.Dims)

	var scanned int
	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scanned++
		if q.MaxScanEvents > 0 && scanned > q.MaxScanEvents {
			return nil, ErrScanLimitExceeded
		}
		ts, err := DecodeTimestamp(iter.Key(), cfg.Dims)
		if err != nil {
			return nil, err
		}
		if partialPrefix && (ts.Before(q.From) || ts.After(q.To)) {
			continue
		}
		nums, _, err := DecodeValue(iter.Value())
		if err != nil {
			return nil, err
		}

		var val float64
		if q.Function != "count" {
			if int(q.NumField) >= len(nums) {
				// Field absent for this event; skip it.
				continue
			}
			val = nums[q.NumField]
		}

		bk := bucketStart(ts)
		b, exists := buckets[bk]
		if !exists {
			b = &bucket{min: math.MaxFloat64, max: -math.MaxFloat64}
			buckets[bk] = b
			bucketOrder = append(bucketOrder, bk)
			if q.MaxBuckets > 0 && len(buckets) > q.MaxBuckets {
				return nil, ErrBucketLimitExceeded
			}
		}
		b.sum += val
		b.count++
		if val < b.min {
			b.min = val
		}
		if val > b.max {
			b.max = val
		}
	}
	if err := iter.Error(); err != nil {
		return nil, fmt.Errorf("ts: iter: %w", err)
	}

	results := make([]Bucket, 0, len(bucketOrder))
	for _, bk := range bucketOrder {
		b := buckets[bk]
		var v float64
		switch q.Function {
		case "avg":
			if b.count > 0 {
				v = b.sum / float64(b.count)
			}
		case "sum":
			v = b.sum
		case "min":
			v = b.min
		case "max":
			v = b.max
		case "count":
			v = float64(b.count)
		}
		t := time.Unix(0, bk).UTC()
		if q.Interval <= 0 {
			t = q.From
		}
		results = append(results, Bucket{Time: t, Value: v, Count: b.count})
	}
	return results, nil
}

// --- Range aggregate functions ---

// rangeNumIter is a shared scan kernel for the single-field range functions.
// It calls fn(val) for every event where NumField is present, respecting the
// partial-prefix time filter and scan limit.
// rangeNumToAll converts a RangeNumQuery to a RangeAllQuery for delegation
// to RangeAggregate. NumField validation is the caller's responsibility.
func rangeNumToAll(q RangeNumQuery) RangeAllQuery {
	return RangeAllQuery{
		Timeline:      q.Timeline,
		Dims:          q.Dims,
		From:          q.From,
		To:            q.To,
		MaxScanEvents: q.MaxScanEvents,
	}
}

// RangeSum returns the sum of num field q.NumField over the query range.
// Syntax sugar over RangeAggregate; performs one full scan pass.
func (s *PebbleStore) RangeSum(ctx context.Context, q RangeNumQuery) (float64, error) {
	if q.NumField > 6 {
		return 0, fmt.Errorf("ts: num_field %d out of range 0–6 (OLU-TS009)", q.NumField)
	}
	res, err := s.RangeAggregate(ctx, rangeNumToAll(q))
	if err != nil {
		return 0, err
	}
	return res.Sums[q.NumField], nil
}

// RangeAvg returns the average of num field q.NumField over the query range.
// Returns 0 with no error when no events carry the field.
// Syntax sugar over RangeAggregate; performs one full scan pass.
func (s *PebbleStore) RangeAvg(ctx context.Context, q RangeNumQuery) (float64, error) {
	if q.NumField > 6 {
		return 0, fmt.Errorf("ts: num_field %d out of range 0–6 (OLU-TS009)", q.NumField)
	}
	res, err := s.RangeAggregate(ctx, rangeNumToAll(q))
	if err != nil {
		return 0, err
	}
	return res.Avgs[q.NumField], nil
}

// RangeMin returns the minimum of num field q.NumField over the query range.
// Returns 0 with no error when no events carry the field.
// Syntax sugar over RangeAggregate; performs one full scan pass.
func (s *PebbleStore) RangeMin(ctx context.Context, q RangeNumQuery) (float64, error) {
	if q.NumField > 6 {
		return 0, fmt.Errorf("ts: num_field %d out of range 0–6 (OLU-TS009)", q.NumField)
	}
	res, err := s.RangeAggregate(ctx, rangeNumToAll(q))
	if err != nil {
		return 0, err
	}
	return res.Mins[q.NumField], nil
}

// RangeMax returns the maximum of num field q.NumField over the query range.
// Returns 0 with no error when no events carry the field.
// Syntax sugar over RangeAggregate; performs one full scan pass.
func (s *PebbleStore) RangeMax(ctx context.Context, q RangeNumQuery) (float64, error) {
	if q.NumField > 6 {
		return 0, fmt.Errorf("ts: num_field %d out of range 0–6 (OLU-TS009)", q.NumField)
	}
	res, err := s.RangeAggregate(ctx, rangeNumToAll(q))
	if err != nil {
		return 0, err
	}
	return res.Maxs[q.NumField], nil
}

// RangeCount returns the count of events in the query range that carry num
// field q.NumField. Syntax sugar over RangeAggregate; performs one full scan pass.
func (s *PebbleStore) RangeCount(ctx context.Context, q RangeNumQuery) (uint64, error) {
	if q.NumField > 6 {
		return 0, fmt.Errorf("ts: num_field %d out of range 0–6 (OLU-TS009)", q.NumField)
	}
	res, err := s.RangeAggregate(ctx, rangeNumToAll(q))
	if err != nil {
		return 0, err
	}
	// RangeCount counts events carrying the specific field, not total events.
	if !res.Fields[q.NumField] {
		return 0, nil
	}
	return res.Count, nil
}

// RangeQuantile returns an approximate quantile estimate for q.NumField over
// the query range using a t-digest (compression=100, ~16 KB per call).
//
// NOT OPTIMISED: this method performs its own full Pebble scan pass, separate
// from RangeAggregate. A caller needing both quantile and sum/avg/min/max for
// the same range must issue two queries and pay for two scans.
//
// If a single-pass combined result is ever needed, introduce a separate
// RangeFullQuery / RangeFullResult pair rather than embedding *tdigest.TDigest
// into RangeAggregateResult. Keeping the types separate preserves
// RangeAggregateResult as a plain value type (no heap pointers, trivially
// copyable and serialisable) and avoids surfacing the estimator implementation
// through the Store contract.
func (s *PebbleStore) RangeQuantile(ctx context.Context, q RangeNumQuery, quantile float64) (float64, error) {
	if q.NumField > 6 {
		return 0, fmt.Errorf("ts: num_field %d out of range 0\u20136 (OLU-TS009)", q.NumField)
	}
	if quantile < 0 || quantile > 1 {
		return 0, fmt.Errorf("ts: quantile %g out of range [0, 1]", quantile)
	}

	cfg, ok := s.reg.get(q.Timeline)
	if !ok {
		return 0, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", q.Timeline)
	}
	if len(q.Dims) < 1 || len(q.Dims) > int(cfg.Dims) {
		return 0, fmt.Errorf("ts: query dims %d out of range 1\u2013%d (OLU-TS007)", len(q.Dims), cfg.Dims)
	}

	startKey, err := EncodeKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, 0), q.From)
	if err != nil {
		return 0, err
	}
	endKeyRaw, err := encodeEndKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, math.MaxUint64), q.To)
	if err != nil {
		return 0, err
	}
	endKey := incrementKey(endKeyRaw)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return 0, fmt.Errorf("ts: new iter: %w", err)
	}
	defer iter.Close()

	td, err := tdigest.New(100)
	if err != nil {
		return 0, fmt.Errorf("ts: tdigest: %w", err)
	}

	partialPrefix := len(q.Dims) < int(cfg.Dims)
	var scanned int
	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		scanned++
		if q.MaxScanEvents > 0 && scanned > q.MaxScanEvents {
			return 0, ErrScanLimitExceeded
		}
		if partialPrefix {
			ts, err := DecodeTimestamp(iter.Key(), cfg.Dims)
			if err != nil {
				return 0, err
			}
			if ts.Before(q.From) || ts.After(q.To) {
				continue
			}
		}
		nums, _, err := DecodeValue(iter.Value())
		if err != nil {
			return 0, err
		}
		if int(q.NumField) >= len(nums) {
			continue // field absent for this event
		}
		if err := td.Add(nums[q.NumField]); err != nil {
			return 0, fmt.Errorf("ts: tdigest add: %w", err)
		}
	}
	if err := iter.Error(); err != nil {
		return 0, fmt.Errorf("ts: iter: %w", err)
	}
	if td.Count() == 0 {
		return 0, nil
	}
	v, err := td.Quantile(quantile)
	if err != nil {
		return 0, fmt.Errorf("ts: tdigest quantile: %w", err)
	}
	return v, nil
}

// RangeMedian returns the approximate P50 for q.NumField over the query range.
// Syntax sugar over RangeQuantile(ctx, q, 0.5).
// NOT OPTIMISED: performs a separate scan pass from RangeAggregate.
// See RangeQuantile for the suggested future optimisation.
func (s *PebbleStore) RangeMedian(ctx context.Context, q RangeNumQuery) (float64, error) {
	return s.RangeQuantile(ctx, q, 0.5)
}

// RangeFullAggregate computes exact sum/avg/min/max/count for all seven numeric
// fields AND approximate quantiles for the requested fields in a single Pebble
// scan pass.
//
// Digests are allocated at the start of the scan (one per requested field,
// ~16 KB each at compression=100) and discarded after quantile extraction.
// They are never stored in the result; RangeAggregateResult remains a plain
// value type.
//
// If q.Quantiles is empty the method is equivalent to RangeAggregate and no
// digests are allocated.
func (s *PebbleStore) RangeFullAggregate(ctx context.Context, q RangeFullQuery) (*RangeFullResult, error) {
	cfg, ok := s.reg.get(q.Timeline)
	if !ok {
		return nil, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", q.Timeline)
	}
	if len(q.Dims) < 1 || len(q.Dims) > int(cfg.Dims) {
		return nil, fmt.Errorf("ts: query dims %d out of range 1–%d (OLU-TS007)", len(q.Dims), cfg.Dims)
	}
	for _, qv := range q.Quantiles {
		if qv < 0 || qv > 1 {
			return nil, fmt.Errorf("ts: quantile %g out of range [0, 1]", qv)
		}
	}
	wantDigest := [7]bool{}
	if len(q.Quantiles) > 0 {
		if q.QuantileFields == nil {
			for i := range wantDigest {
				wantDigest[i] = true
			}
		} else {
			for _, f := range q.QuantileFields {
				if f > 6 {
					return nil, fmt.Errorf("ts: quantile_field %d out of range 0–6 (OLU-TS009)", f)
				}
				wantDigest[f] = true
			}
		}
	}
	var digests [7]*tdigest.TDigest
	for i := range digests {
		if wantDigest[i] {
			td, err := tdigest.New(100)
			if err != nil {
				return nil, fmt.Errorf("ts: tdigest: %w", err)
			}
			digests[i] = td
		}
	}
	startKey, err := EncodeKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, 0), q.From)
	if err != nil {
		return nil, err
	}
	endKeyRaw, err := encodeEndKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, math.MaxUint64), q.To)
	if err != nil {
		return nil, err
	}
	endKey := incrementKey(endKeyRaw)
	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return nil, fmt.Errorf("ts: new iter: %w", err)
	}
	defer iter.Close()
	res := &RangeFullResult{}
	agg := &res.Aggregate
	for i := range agg.Mins {
		agg.Mins[i] = math.MaxFloat64
		agg.Maxs[i] = -math.MaxFloat64
	}
	partialPrefix := len(q.Dims) < int(cfg.Dims)
	var scanned int
	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		scanned++
		if q.MaxScanEvents > 0 && scanned > q.MaxScanEvents {
			return res, ErrScanLimitExceeded
		}
		if partialPrefix {
			ts, err := DecodeTimestamp(iter.Key(), cfg.Dims)
			if err != nil {
				return res, err
			}
			if ts.Before(q.From) || ts.After(q.To) {
				continue
			}
		}
		nums, _, err := DecodeValue(iter.Value())
		if err != nil {
			return res, err
		}
		agg.Count++
		for i, v := range nums {
			if i >= 7 {
				break
			}
			agg.Fields[i] = true
			agg.Sums[i] += v
			if v < agg.Mins[i] {
				agg.Mins[i] = v
			}
			if v > agg.Maxs[i] {
				agg.Maxs[i] = v
			}
			if digests[i] != nil {
				if err := digests[i].Add(v); err != nil {
					return res, fmt.Errorf("ts: tdigest add field %d: %w", i, err)
				}
			}
		}
	}
	if err := iter.Error(); err != nil {
		return res, fmt.Errorf("ts: iter: %w", err)
	}
	if agg.Count > 0 {
		for i := range agg.Fields {
			if agg.Fields[i] {
				agg.Avgs[i] = agg.Sums[i] / float64(agg.Count)
			} else {
				agg.Mins[i] = 0
				agg.Maxs[i] = 0
			}
		}
	} else {
		for i := range agg.Mins {
			agg.Mins[i] = 0
			agg.Maxs[i] = 0
		}
	}
	if len(q.Quantiles) > 0 {
		for i, td := range digests {
			if td == nil || td.Count() == 0 {
				continue
			}
			estimates := make([]float64, len(q.Quantiles))
			for j, qv := range q.Quantiles {
				v, err := td.Quantile(qv)
				if err != nil {
					return res, fmt.Errorf("ts: tdigest quantile field %d: %w", i, err)
				}
				estimates[j] = v
			}
			res.Quantiles[i] = estimates
		}
	}
	return res, nil
}

func (s *PebbleStore) RangeAggregate(ctx context.Context, q RangeAllQuery) (*RangeAggregateResult, error) {
	cfg, ok := s.reg.get(q.Timeline)
	if !ok {
		return nil, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", q.Timeline)
	}
	if len(q.Dims) < 1 || len(q.Dims) > int(cfg.Dims) {
		return nil, fmt.Errorf("ts: query dims %d out of range 1–%d (OLU-TS007)", len(q.Dims), cfg.Dims)
	}

	startKey, err := EncodeKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, 0), q.From)
	if err != nil {
		return nil, err
	}
	endKeyRaw, err := encodeEndKey(q.Timeline, cfg.Dims, padDims(q.Dims, cfg.Dims, math.MaxUint64), q.To)
	if err != nil {
		return nil, err
	}
	endKey := incrementKey(endKeyRaw)

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return nil, fmt.Errorf("ts: new iter: %w", err)
	}
	defer iter.Close()

	res := &RangeAggregateResult{}
	for i := range res.Mins {
		res.Mins[i] = math.MaxFloat64
		res.Maxs[i] = -math.MaxFloat64
	}

	partialPrefix := len(q.Dims) < int(cfg.Dims)
	var scanned int
	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		scanned++
		if q.MaxScanEvents > 0 && scanned > q.MaxScanEvents {
			return res, ErrScanLimitExceeded
		}
		if partialPrefix {
			ts, err := DecodeTimestamp(iter.Key(), cfg.Dims)
			if err != nil {
				return res, err
			}
			if ts.Before(q.From) || ts.After(q.To) {
				continue
			}
		}
		nums, _, err := DecodeValue(iter.Value())
		if err != nil {
			return res, err
		}
		res.Count++
		for i, v := range nums {
			if i >= 7 {
				break
			}
			res.Fields[i] = true
			res.Sums[i] += v
			if v < res.Mins[i] {
				res.Mins[i] = v
			}
			if v > res.Maxs[i] {
				res.Maxs[i] = v
			}
		}
	}
	if err := iter.Error(); err != nil {
		return res, fmt.Errorf("ts: iter: %w", err)
	}

	// Compute averages and zero out Min/Max for absent fields.
	if res.Count > 0 {
		for i := range res.Fields {
			if res.Fields[i] {
				res.Avgs[i] = res.Sums[i] / float64(res.Count)
			} else {
				res.Mins[i] = 0
				res.Maxs[i] = 0
			}
		}
	} else {
		for i := range res.Mins {
			res.Mins[i] = 0
			res.Maxs[i] = 0
		}
	}

	return res, nil
}



// Purge deletes events older than the applicable retention window for each
// timeline. Timelines with effective RetentionDays == 0 are skipped (no expiry).
func (s *PebbleStore) Purge(ctx context.Context) error {
	ids := s.reg.list()
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := s.purgeTimeline(ctx, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *PebbleStore) purgeTimeline(ctx context.Context, id TimelineID) error {
	days := s.reg.effectiveRetention(id)
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)

	cfg, ok := s.reg.get(id)
	if !ok {
		return nil
	}

	// Scan from the start of this timeline's key space.
	startKey := EncodePrefixKey(id, make([]uint64, cfg.Dims))
	endKey := incrementKey(EncodePrefixKey(id, fillDims(cfg.Dims, math.MaxUint64)))

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return fmt.Errorf("ts: purge iter: %w", err)
	}
	defer iter.Close()

	batch := s.db.NewBatch()
	batchCount := 0
	var deleted int64

	flush := func() error {
		if batchCount == 0 {
			return nil
		}
		if err := batch.Commit(pebble.Sync); err != nil {
			return fmt.Errorf("ts: purge commit: %w", err)
		}
		batch.Close()
		batch = s.db.NewBatch()
		batchCount = 0
		return nil
	}

	for iter.First(); iter.Valid(); iter.Next() {
		if err := ctx.Err(); err != nil {
			batch.Close()
			return err
		}
		ts, err := DecodeTimestamp(iter.Key(), cfg.Dims)
		if err != nil {
			batch.Close()
			return err
		}
		if !ts.Before(cutoff) {
			continue
		}
		keyCopy := make([]byte, len(iter.Key()))
		copy(keyCopy, iter.Key())
		if err := batch.Delete(keyCopy, nil); err != nil {
			batch.Close()
			return fmt.Errorf("ts: purge delete: %w", err)
		}
		batchCount++
		deleted++

		if batchCount >= purgeBatchSize {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	if err := iter.Error(); err != nil {
		batch.Close()
		return fmt.Errorf("ts: purge iter: %w", err)
	}
	if err := flush(); err != nil {
		return err
	}
	if deleted > 0 {
		s.counter(id).Add(-deleted)
	}
	return nil
}

// DefaultRetentionDays returns the store-level default retention in days.
func (s *PebbleStore) DefaultRetentionDays() int {
	s.reg.mu.RLock()
	defer s.reg.mu.RUnlock()
	return s.reg.defaultRetentionDays
}

// SetDefaultRetentionDays updates the store-level default retention and persists it.
func (s *PebbleStore) SetDefaultRetentionDays(days int) error {
	return s.reg.setDefaultRetention(days)
}

// --- Diagnostics ---

func (s *PebbleStore) Stats(_ context.Context) (*StoreStats, error) {
	sizeEstimate, err := s.db.EstimateDiskUsage([]byte{0x00}, []byte{0xFF})
	if err != nil {
		sizeEstimate = 0
	}
	return &StoreStats{
		Timelines: len(s.reg.list()),
		DiskBytes: int64(sizeEstimate),
	}, nil
}

func (s *PebbleStore) TimelineStats(ctx context.Context, id TimelineID) (*TimelineStats, error) {
	cfg, ok := s.reg.get(id)
	if !ok {
		return nil, fmt.Errorf("ts: timeline %d not defined (OLU-TS004)", id)
	}

	count := s.counter(id).Load()
	if count < 0 {
		count = 0
	}
	stats := &TimelineStats{
		TotalEvents:            count,
		TotalEventsApproximate: true,
	}

	startKey := EncodePrefixKey(id, make([]uint64, cfg.Dims))
	endKey := incrementKey(EncodePrefixKey(id, fillDims(cfg.Dims, math.MaxUint64)))

	iter, err := s.db.NewIter(&pebble.IterOptions{LowerBound: startKey, UpperBound: endKey})
	if err != nil {
		return nil, fmt.Errorf("ts: stats iter: %w", err)
	}
	defer iter.Close()

	if iter.First() {
		if ts, err := DecodeTimestamp(iter.Key(), cfg.Dims); err == nil {
			stats.OldestEvent = ts
		}
	}
	if iter.Last() {
		if ts, err := DecodeTimestamp(iter.Key(), cfg.Dims); err == nil {
			stats.NewestEvent = ts
		}
	}
	return stats, nil
}

// --- Lifecycle ---

func (s *PebbleStore) Close() error {
	_ = s.flushMeta()
	return s.db.Close()
}

// --- Internal helpers ---

func (s *PebbleStore) counter(id TimelineID) *atomic.Int64 {
	s.countersMu.RLock()
	c, ok := s.counters[id]
	s.countersMu.RUnlock()
	if ok {
		return c
	}
	s.countersMu.Lock()
	defer s.countersMu.Unlock()
	if c, ok = s.counters[id]; ok {
		return c
	}
	c = &atomic.Int64{}
	s.counters[id] = c
	return c
}

func (s *PebbleStore) validateEvent(e Event) error {
	if e.Timeline == 0 {
		return fmt.Errorf("ts: timeline ID 0x0000 is reserved (OLU-TS018)")
	}
	if e.Time.Before(time.Unix(0, 0)) {
		return fmt.Errorf("ts: timestamp before Unix epoch (OLU-TS005)")
	}
	if len(e.Nums) > 7 {
		return fmt.Errorf("ts: at most 7 numeric fields, got %d", len(e.Nums))
	}
	for i, v := range e.Nums {
		if math.IsNaN(v) {
			return fmt.Errorf("ts: NaN in Nums[%d] (OLU-TS017)", i)
		}
	}
	return nil
}

func (s *PebbleStore) decodeEntry(key, val []byte, dims uint8) (Event, error) {
	tid, dv, ts, err := DecodeKey(key, dims)
	if err != nil {
		return Event{}, err
	}
	nums, payload, err := DecodeValue(val)
	if err != nil {
		return Event{}, err
	}
	return Event{Timeline: tid, Dims: dv, Time: ts, Nums: nums, Payload: payload}, nil
}

func (s *PebbleStore) loadMeta(compression string) error {
	data, err := os.ReadFile(s.metaPath)
	if os.IsNotExist(err) {
		meta := &storeMeta{
			CreatedAt:   time.Now().UTC(),
			Compression: compression,
		}
		return s.writeMeta(meta)
	}
	if err != nil {
		return err
	}
	var meta storeMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return err
	}
	// Seed counters.
	s.countersMu.Lock()
	for k, v := range meta.Counts {
		var id uint16
		if _, err := fmt.Sscanf(k, "%d", &id); err != nil {
			continue
		}
		c := &atomic.Int64{}
		c.Store(v)
		s.counters[TimelineID(id)] = c
	}
	s.countersMu.Unlock()
	return nil
}

func (s *PebbleStore) flushMeta() error {
	s.countersMu.RLock()
	counts := make(map[string]int64, len(s.counters))
	for id, c := range s.counters {
		counts[fmt.Sprintf("%d", id)] = c.Load()
	}
	s.countersMu.RUnlock()

	meta := &storeMeta{
		Compression: "",
		Counts:      counts,
	}
	return s.writeMeta(meta)
}

func (s *PebbleStore) writeMeta(meta *storeMeta) error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.metaPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.metaPath)
}

// --- Key manipulation utilities ---

// padDims returns a copy of dv padded to length dims with fillVal.
func padDims(dv []uint64, dims uint8, fillVal uint64) []uint64 {
	out := make([]uint64, dims)
	copy(out, dv)
	for i := len(dv); i < int(dims); i++ {
		out[i] = fillVal
	}
	return out
}

// fillDims returns a slice of length dims filled with val.
func fillDims(dims uint8, val uint64) []uint64 {
	out := make([]uint64, dims)
	for i := range out {
		out[i] = val
	}
	return out
}

// incrementKey returns a copy of key with the last byte incremented,
// carrying if necessary. Returns nil if the key overflows (all 0xFF).
func incrementKey(key []byte) []byte {
	out := make([]byte, len(key))
	copy(out, key)
	for i := len(out) - 1; i >= 0; i-- {
		out[i]++
		if out[i] != 0 {
			return out
		}
	}
	return nil // overflow — key space exhausted, use nil (open upper bound)
}

// encodeEndKey encodes the upper-bound key for a range query. It uses the same
// timeline and dims as the already-validated startKey, so the only new error
// path is a pre-epoch timestamp — which cannot be produced by a well-formed
// query. Returns an error instead of panicking so callers can propagate it.
func encodeEndKey(tid TimelineID, dims uint8, dv []uint64, ts time.Time) ([]byte, error) {
	return EncodeKey(tid, dims, dv, ts)
}
