// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"context"
	"time"
)

// TimelineID is a uint16 identifier for a timeline within a tenant store.
// ID 0x0000 is reserved; valid IDs are 0x0001–0xFFFF.
type TimelineID uint16

// MaxTimelineID is the highest valid timeline ID.
const MaxTimelineID TimelineID = 0xFFFF

// MinDims and MaxDims bound the number of dimensions a timeline may declare.
const (
	MinDims = 1
	MaxDims = 5
)

// TimelineConfig describes a timeline's declaration. Dims is immutable after
// the first write; Name and RetentionDays may be updated freely.
type TimelineConfig struct {
	Name          string    // optional, human-readable label
	Dims          uint8     // 1–5, immutable after FirstWriteAt is set
	RetentionDays int       // 0 = use store-level default
	CreatedAt     time.Time
	FirstWriteAt  time.Time // zero until first event written; Dims locks here
}

// Event is a single timeseries record written to or read from a timeline.
type Event struct {
	Timeline TimelineID
	Dims     []uint64  // len must equal timeline's Dims
	Time     time.Time
	Nums     []float64 // optional, up to 7; nil means no numeric fields
	Payload  []byte    // optional, caller-defined opaque bytes
}

// RangeQuery retrieves events from a timeline over a time range.
// Dims is a leading prefix: 1 ≤ len(Dims) ≤ timeline.Dims.
type RangeQuery struct {
	Timeline      TimelineID
	Dims          []uint64
	From          time.Time
	To            time.Time
	Limit         int    // default 1000, max 10000
	Order         string // "asc" (default) or "desc"
	MaxScanEvents int    // 0 = use store/server default; aborts scan if exceeded
}

// LatestQuery retrieves the N most recent events matching a dimension prefix.
//
// Dims may be a leading prefix of the timeline's declared dimension count;
// all events matching that prefix are considered, across all remaining
// dimension values. This is intentional and useful for "latest across all
// sub-dimensions" queries.
//
// From and To are optional time bounds. When non-zero, only events within
// [From, To] are returned. This is applied as a Go-side filter, consistent
// with the partial-prefix time filter in QueryRange.
type LatestQuery struct {
	Timeline TimelineID
	Dims     []uint64
	N        int       // default 10, max 10000
	From     time.Time // optional lower bound (zero = unbounded)
	To       time.Time // optional upper bound (zero = unbounded)
}

// AggregateQuery computes an aggregate over a numeric field for all events
// matching the dimension prefix and time range.
type AggregateQuery struct {
	Timeline      TimelineID
	Dims          []uint64
	From          time.Time
	To            time.Time
	NumField      uint8         // index into Nums (0-based, max 6)
	Function      string        // "avg", "min", "max", "sum", "count"
	Interval      time.Duration // 0 = scalar result; > 0 = time-bucketed
	MaxScanEvents int           // 0 = no scan limit
	MaxBuckets    int           // 0 = no bucket limit; > 0 aborts when exceeded (OLU-TS019)
}

// Bucket holds one time bucket of an aggregation result.
// RangeNumQuery is the query shape for single-field scalar range functions:
// RangeSum, RangeAvg, RangeMin, RangeMax, RangeCount.
// NumField is validated (0–6) and must correspond to a populated field.
type RangeNumQuery struct {
	Timeline      TimelineID
	Dims          []uint64
	From          time.Time
	To            time.Time
	NumField      uint8 // 0–6
	MaxScanEvents int   // 0 = no limit
}

// RangeAllQuery is the query shape for RangeAggregate, which computes
// statistics over all populated numeric fields in a single scan pass.
// No NumField — the result covers every field present in the matched events.
type RangeAllQuery struct {
	Timeline      TimelineID
	Dims          []uint64
	From          time.Time
	To            time.Time
	MaxScanEvents int // 0 = no limit
}

// RangeAggregateResult holds per-field statistics from a single scan pass.
// Fields[i] indicates whether num field i was present in at least one event;
// entries for absent fields carry zero values.
type RangeAggregateResult struct {
	Count  uint64
	Sums   [7]float64
	Avgs   [7]float64 // populated after scan: Sums[i]/Count; NaN if Count==0
	Mins   [7]float64
	Maxs   [7]float64
	Fields [7]bool // true if field i appeared in at least one event
}


// RangeFullQuery is the query shape for RangeFullAggregate, which computes
// sum, avg, min, max, count (via RangeAggregateResult) AND approximate
// quantiles for selected numeric fields — all in a single Pebble scan pass.
//
// Quantiles lists the desired quantile values, e.g. [0.5, 0.9, 0.99].
// Each value must be in [0, 1]; RangeFullAggregate returns an error otherwise.
//
// QuantileFields lists which numeric fields (0–6) should have quantiles
// computed. If nil, quantiles are computed for all seven fields, allocating
// ~16 KB of t-digest state per field. Callers should be explicit about which
// fields they need to avoid unnecessary allocation.
//
// If Quantiles is empty, RangeFullAggregate behaves identically to
// RangeAggregate (no digests allocated).
type RangeFullQuery struct {
	RangeAllQuery
	Quantiles      []float64 // quantile values to estimate, e.g. [0.5, 0.9, 0.99]
	QuantileFields []uint8   // num fields to estimate quantiles for (0–6); nil = all fields
}

// RangeFullResult holds the combined output of RangeFullAggregate.
// Aggregate contains the exact statistics (same as RangeAggregate).
// Quantiles[i][j] is the estimate for field i at Quantiles[j] from
// RangeFullQuery. A nil inner slice means field i was not requested or
// carried no events. The outer array is always length 7 (one slot per field).
type RangeFullResult struct {
	Aggregate RangeAggregateResult
	Quantiles [7][]float64
}

type Bucket struct {
	Time  time.Time
	Value float64
	Count uint64
}

// StoreStats holds aggregate diagnostics for the entire tenant store.
type StoreStats struct {
	Timelines int
	DiskBytes int64
}

// TimelineStats holds diagnostics for a single timeline.
// TotalEvents is derived from an in-memory counter that is persisted
// periodically to meta.json. After a crash without a clean Close, the
// counter may be stale; TotalEventsApproximate is always true for the
// current PebbleStore implementation.
type TimelineStats struct {
	TotalEvents             int64
	TotalEventsApproximate  bool // always true; counter is eventually consistent
	OldestEvent             time.Time
	NewestEvent             time.Time
}

// StoreConfig holds configuration that is meaningful to any timeseries store
// backend. It is passed through the StoreFactory contract and must not contain
// engine-specific knobs.
type StoreConfig struct {
	DefaultRetentionDays int // store-level fallback; 0 = no expiry
}

// PebbleConfig holds LSM-tree tuning parameters specific to the Pebble
// storage engine. It is consumed only by NewPebbleStore / NewPebbleStoreFactory
// and has no meaning to other backends.
//
// Zero values are safe: NewPebbleStore applies sensible defaults for any
// field that is ≤ 0 or empty.
type PebbleConfig struct {
	MemtableSize          int    // bytes; default 67108864 (64 MB)
	BlockSize             int    // bytes; default 32768 (32 KB)
	Compression           string // "snappy", "zstd", or "none"; default "zstd"
	L0CompactionThreshold int    // L0 files before compaction; default 4
	MaxOpenFiles          int    // per-store file descriptor limit; default 500
}

// Store is the interface for a single tenant's timeseries backend.
// Implementations must be safe for concurrent use.
type Store interface {
	// Timeline management
	DefineTimeline(id TimelineID, cfg TimelineConfig) error
	UpdateTimeline(id TimelineID, cfg TimelineConfig) error // name + RetentionDays only
	Timeline(id TimelineID) (TimelineConfig, bool)
	Timelines() []TimelineID

	// Write
	Append(ctx context.Context, e Event) error
	AppendBatch(ctx context.Context, events []Event, maxBatch int) (int, error)

	// Read
	QueryRange(ctx context.Context, q RangeQuery) ([]Event, error)
	Latest(ctx context.Context, q LatestQuery) ([]Event, error)

	// Aggregate — bucketed or scalar, single numeric field
	Aggregate(ctx context.Context, q AggregateQuery) ([]Bucket, error)

	// Single-field scalar range functions. Each performs one scan pass
	// over [From, To] for the given NumField. Kept alongside RangeAggregate
	// to allow direct performance comparison via benchmarks.
	RangeSum(ctx context.Context, q RangeNumQuery) (float64, error)
	RangeAvg(ctx context.Context, q RangeNumQuery) (float64, error)
	RangeMin(ctx context.Context, q RangeNumQuery) (float64, error)
	RangeMax(ctx context.Context, q RangeNumQuery) (float64, error)
	RangeCount(ctx context.Context, q RangeNumQuery) (uint64, error)

	// RangeAggregate computes Count, Sum, Avg, Min, Max for all seven
	// numeric fields simultaneously in a single scan pass.
	RangeAggregate(ctx context.Context, q RangeAllQuery) (*RangeAggregateResult, error)

	// RangeQuantile returns an approximate quantile estimate for a single
	// numeric field over [From, To] using a t-digest (compression=100).
	//
	// q must be in [0, 1]. Returns (0, nil) when no events carry NumField.
	//
	// Performance note: RangeQuantile performs its own full scan pass and
	// cannot be combined with RangeAggregate in a single pass. A caller
	// needing both sum/avg/min/max AND a quantile estimate for the same
	// range must issue two separate queries and pay for two scans.
	//
	// Future optimisation: if a single-pass combined result becomes
	// necessary, introduce a separate RangeFullQuery / RangeFullResult pair
	// rather than embedding *tdigest.TDigest into RangeAggregateResult.
	// Keeping the types separate preserves RangeAggregateResult as a plain
	// value type (no heap pointers, trivially copyable and serialisable) and
	// avoids surfacing the quantile estimator implementation as part of the
	// Store contract.
	RangeQuantile(ctx context.Context, q RangeNumQuery, quantile float64) (float64, error)

	// RangeMedian returns the approximate P50 for a single numeric field
	// over [From, To]. Syntax sugar over RangeQuantile(ctx, q, 0.5).
	// Carries the same two-scan limitation; see RangeQuantile.
	RangeMedian(ctx context.Context, q RangeNumQuery) (float64, error)

	// RangeFullAggregate computes exact sum/avg/min/max/count for all seven
	// numeric fields AND approximate quantiles for selected fields in a single
	// Pebble scan pass.
	//
	// This is the efficient alternative to calling RangeAggregate and
	// RangeQuantile separately when both are needed. RangeAggregateResult
	// is kept as a plain value type; digests are allocated during the scan
	// and discarded after quantile extraction, never stored in the result.
	//
	// If RangeFullQuery.Quantiles is empty the call is equivalent to
	// RangeAggregate with no additional cost.
	RangeFullAggregate(ctx context.Context, q RangeFullQuery) (*RangeFullResult, error)

	// Retention
	Purge(ctx context.Context) error

	// Retention configuration
	DefaultRetentionDays() int
	SetDefaultRetentionDays(days int) error

	// Diagnostics
	Stats(ctx context.Context) (*StoreStats, error)
	TimelineStats(ctx context.Context, id TimelineID) (*TimelineStats, error)

	// Lifecycle
	Close() error
}

// StoreFactory creates a Store for a given data directory.
type StoreFactory func(dir string, cfg StoreConfig) (Store, error)

// Manager manages per-tenant Store lifecycle.
type Manager interface {
	// Provision creates a timeseries store for a tenant.
	Provision(ctx context.Context, tenantID uint16) error

	// StoreFor returns the Store for a tenant, or an error if not provisioned.
	StoreFor(tenantID uint16) (Store, error)

	// IsProvisioned reports whether a tenant has timeseries storage.
	IsProvisioned(tenantID uint16) bool

	// Close shuts down all stores.
	Close() error
}
