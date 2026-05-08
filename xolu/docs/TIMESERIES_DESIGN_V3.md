# olu Timeseries Storage

**Version:** 0.3  
**Author:** haitch <h@ual.fi>  
**Date:** March 2026  
**Status:** Active design reference  

---

## 1. Problem

Asset management systems ingest sensor events as regular JSON documents
through olu's entity CRUD API. Each event is a row in the shared
`entities` table with a `(tenant_id, entity_type, id)` primary key.
The timestamp, trigger type, sensor ID, and numeric readings are fields
inside the JSON blob.

This works at small scale but breaks down as sensor density grows:

- **Queries are slow.** Time-range filters require `json_extract(data,
  '$.timestamp')` on every row. No B-tree index can accelerate this.
- **Storage is bloated.** Each event carries full JSON overhead (~182 bytes
  typical) plus 256 bytes of index overhead across 4 indices. Effective
  cost: ~490 bytes per event.
- **Write throughput is shared.** Sensor event writes compete with asset
  CRUD for SQLite's single WAL writer slot. At a 400–600 writes/s
  ceiling, high-frequency ingest risks starving normal operations.
- **Aggregation is expensive.** Rules engine queries like "average
  temperature over 15 minutes" must deserialise JSON and aggregate in Go.
- **Access patterns are fixed.** All data is keyed identically regardless
  of the query shape; there is no efficient way to query by event type
  across assets, or to serve both per-asset and cross-asset access
  patterns from the same data.

---

## 2. Approach

A dedicated timeseries storage backend using Pebble, a pure-Go LSM-tree
key-value store (CockroachDB's storage engine), is added alongside the
existing SQLite entity store. Timeseries data is stored in compact binary
format with key ordering that makes time-range queries sequential reads.
The entity store is unchanged; timeseries is a parallel, opt-in
capability.

The storage layer is **domain-agnostic**: it stores generic events with
typed numeric fields and a caller-defined payload, organised into named
**timelines** with independently declared key shapes. The IoT domain
concepts — assets, sensors, trigger types, event categories — are mapped
to timeline IDs and dimension values by the calling layer above the store.

### 2.1 Why Pebble

Pebble is a sorted key-value store with properties that align with
timeseries workloads:

- **Append-optimised.** LSM trees buffer writes in memory and flush
  sorted runs to disk. No write-ahead lock contention.
- **Range scan native.** Seek to a key prefix and iterate forward is the
  fundamental operation. Time-range queries become sequential I/O.
- **Block compression.** SSTables are compressed (Snappy or Zstd) at the
  block level. Structured data with regular intervals and slowly changing
  values compresses well.
- **Pure Go.** No CGo, no C++ build toolchain. Single binary deployment
  is preserved.
- **Battle-tested.** Powers CockroachDB's storage layer in production.

### 2.2 Why not SQLite

A SQLite-backed timeseries table (per-tenant, with extracted columns and
proper indices) was evaluated. It works but costs ~490 bytes per event
versus ~30–42 bytes with Pebble+Zstd — a 12–16x storage penalty. More
critically, SQLite's single-writer constraint caps sustained ingest at
~1,000 events/s, while Pebble handles 10,000–160,000 events/s depending
on batch size.

### 2.3 Why not an external timeseries database

TimescaleDB, QuestDB, and InfluxDB all outperform this design on raw
throughput and compression. They also require installing and operating a
separate database system. olu's value proposition is operational
simplicity: one binary, one data directory, no external dependencies. The
timeseries backend preserves this property.

For deployments where a dedicated timeseries database is justified, olu's
architecture already accommodates a sidecar: event ingest dual-writes to
olu (metadata, graph edges) and to the external store (raw telemetry).
This design covers the single-binary deployment tier.

---

## 3. Core Concept: Generic Timelines

A **timeline** is a named, isolated key space within a tenant's Pebble
store. Each timeline has a fixed number of **dimensions** (1–5), declared
at definition time and immutable after the first write. All keys within a
timeline are:

```
[timeline_id:2][d0:8][d1:8?][d2:8?][d3:8?][d4:8?][ts:8]
```

A leading `uint16` timeline identifier, followed by N `uint64` dimension
values, followed by a `uint64` Unix nanosecond timestamp. All fields are
big-endian.

The store has no knowledge of what the dimensions represent. The mapping —
"d0 is asset_id, d1 is sensor_id" — is the responsibility of the calling
layer. The store enforces only that the number of dimension values
provided on every write and query matches the dimension count declared for
that timeline.

A tenant may define up to 65,535 distinct timelines (IDs 0x0001–0xFFFF;
0x0000 is reserved).

### 3.1 Why multiple timelines

Different queries require different key orderings. A query for "all
readings from sensor 7 on asset 42 in the last hour" requires leading
dimensions `(asset_id, sensor_id)`. A query for "all door-open events
across all assets in the last hour" requires a leading dimension of
`event_type_id`. These access patterns are incompatible in a single key
ordering.

Rather than adding secondary indexes — which add write amplification and
purge complexity — the calling layer defines separate timelines with
appropriate key orderings and writes each event to the relevant timelines
in a single Pebble batch commit. Multiple timeline entries per logical
event share one fsync; the wall-clock cost is the same as a single write.

### 3.2 Retention per timeline

Retention is declared per timeline rather than per store. Raw sensor
readings can expire after 90 days; pre-computed rollups and alert history
can be retained indefinitely — all within the same Pebble instance. A
store-level default retention applies to any timeline that does not
declare its own.

### 3.3 Addressable dimensions, not operation inventory

The query model of a timeseries store is defined by which dimensions can
be independently fixed in a query, not by what named operations exist.
This distinction matters when reasoning about whether a given access
pattern is well-served.

A dimension is **addressable** if it appears as a leading key field in
some timeline's key layout. Given the key ordering `[d0][d1][ts]`, d0 is
fully addressable (any value of d0 can be the sole filter), d1 is
addressable only when d0 is also fixed (because it is not a leading
field), and any dimension absent from the key entirely is not addressable
without a separate timeline.

In v0.1, `sensor_id` was not a leading dimension — it sat after the
timestamp — so "all readings for sensor X across all assets" required a
secondary index. The secondary index made that dimension addressable at
the cost of write amplification. In the generic model the same result is
achieved by defining a second timeline with `sensor_id` as d0, written
in the same batch commit at no additional fsync cost.

When evaluating whether a new query pattern is efficiently supported,
the question to ask is: *is the filtering dimension a leading field in
some timeline's key?* If yes, the query is a bounded sequential scan. If
no, it requires either a new timeline, a full scan of an existing
timeline, or a dimension-enumeration step through the entity store.

---

## 4. Availability

### 4.1 Tenant mode compatibility

Timeseries storage is available in **all tenant modes** (`path` and
`strict`). Each tenant's timeseries data is fully isolated — a separate
Pebble instance per tenant, keyed by numeric tenant ID, with no shared
state between tenants.

In `path` mode with `TenantAutoRegister` enabled, operators should be
aware that each provisioned tenant allocates a Pebble instance (default:
64 MB memtable, 500 file descriptors). Enabling authentication or
disabling auto-registration is recommended when timeseries is active to
prevent uncontrolled resource allocation.

### 4.2 Feature flag

Timeseries is controlled by a configuration flag:

    TimeseriesEnabled  bool   // default: false

Environment variable: `OLU_TIMESERIES_ENABLED`.

The flag is validated at startup:

- If `TimeseriesEnabled` is true and `StorageType` is not `"sqlite"`,
  the server refuses to start with a clear error message.
- If `TimeseriesEnabled` is false, all `/ts/` routes are unregistered and
  return 404.

### 4.3 Per-tenant opt-in

Even with `TimeseriesEnabled: true`, individual tenants must be
explicitly provisioned for timeseries storage. Provisioning creates the
tenant's Pebble instance and data directory.

A tenant without timeseries provisioning receives `404 Not Found` on
`/ts/` endpoints.

---

## 5. Storage Design

### 5.1 Data directory layout

Each tenant gets an isolated Pebble instance under the olu data
directory:

    {OLU_BASE_DIR}/
      olu.db                    # existing SQLite database
      ts/
        t0001/                  # tenant 0x0001
          pebble/               # Pebble data directory
            MANIFEST-000001
            000001.sst
            ...
          registry.json         # timeline definitions (id, name, dims, retention)
          meta.json             # store metadata (created_at, event counter)
        t0002/
          pebble/
          registry.json
          meta.json

Per-tenant instances provide:

- Clean isolation — no cross-tenant data in the same LSM tree.
- Independent compaction — one tenant's write burst doesn't stall
  another's reads.
- Simple tenant deletion — remove the directory.
- Independent backup and migration — copy the directory.

### 5.2 Key format

Keys are variable-size binary, big-endian encoded. The number of
dimension fields is fixed per timeline and stored in `registry.json`.

```
Offset  Size       Field
─────────────────────────────────────────────
 0       2         timeline_id  uint16
 2       8         d0           uint64
10       8?        d1           uint64 (if dims ≥ 2)
18       8?        d2           uint64 (if dims ≥ 3)
26       8?        d3           uint64 (if dims ≥ 4)
34       8?        d4           uint64 (if dims ≥ 5)
─────────────────────────────────────────────
 2+N×8   8         ts           uint64, Unix nanoseconds
─────────────────────────────────────────────
Total: 18, 26, 34, 42, or 50 bytes (dims 1–5)
```

Lexicographic byte ordering equals logical ordering: all events in
timeline X are contiguous, events with the same dimension prefix are
contiguous and sorted chronologically, and a range scan with all
dimensions fixed is a bounded sequential read.

**Timestamp encoding.** Timestamps are stored as `uint64` Unix
nanoseconds. This gives nanosecond precision and covers dates from 1970
to 2554 without overflow. Events with timestamps before the Unix epoch
are rejected.

**No prefix byte.** The `timeline_id` field serves as the key space
separator; there is no additional prefix byte. Timeline 1 keys can never
be confused with timeline 2 keys because the first two bytes differ.

### 5.3 Value format

```
Offset   Size      Field
──────────────────────────────────────────────────────────
 0        1        flags (bitmask)
 1        0 or 8   num0    float64, IEEE 754 (if bit 0 set)
                   num1    float64            (if bit 1 set)
                   num2    float64            (if bit 2 set)
                   num3    float64            (if bit 3 set)
                   num4    float64            (if bit 4 set)
                   num5    float64            (if bit 5 set)
                   num6    float64            (if bit 6 set)
 varies   0 or 2   payload_len  uint16        (if bit 7 set)
 varies   varies   payload      bytes
```

Flags byte:

| Bit | Field |
|-----|-------|
| 0   | num0 present |
| 1   | num1 present |
| 2   | num2 present |
| 3   | num3 present |
| 4   | num4 present |
| 5   | num5 present |
| 6   | num6 present |
| 7   | payload present |

Up to 7 `float64` numeric fields are available; these are the fields that
`Aggregate` operates on. The payload is caller-defined bytes — JSON,
protobuf, msgpack, or any other encoding the application layer requires.
The store reads and writes it as opaque bytes.

NaN values in numeric fields are rejected at write time (error
OLU-TS017). Infinity is permitted; IEEE 754 comparison semantics in
aggregation handle it correctly.

The `uint16` payload length prefix caps individual payloads at 65,535
bytes. This is sufficient for structured telemetry. Payloads larger than
this — images, compressed segments, binary blobs — should be stored
externally and referenced by a key in the payload field.

**Example — temperature reading (2 numerics, small JSON payload):**

```
flags = 0b10000011  (num0, num1, payload)
num0:    8 bytes  (temperature as float64)
num1:    8 bytes  (humidity as float64)
len:     2 bytes  (payload length)
payload: ~12 bytes (e.g. {"unit":"°C"})
──────────────────
Total:   ~31 bytes
```

### 5.4 Timeline registry

Each tenant store maintains a **timeline registry** persisted to
`registry.json`. The registry is loaded into memory at store open and
updated by `DefineTimeline` / `UpdateTimeline`.

```go
type TimelineID uint16

type TimelineConfig struct {
    Name          string    // optional, human-readable label
    Dims          uint8     // 1–5, immutable after first write
    RetentionDays int       // 0 = use store-level default
    CreatedAt     time.Time
    FirstWriteAt  time.Time // zero until first event; Dims locks here
}
```

`Dims` is immutable after `FirstWriteAt` is set. Attempting to redefine
the dimension count of a timeline that has received events returns
OLU-TS016. Name and `RetentionDays` may be updated freely.

The registry also stores the store-level `DefaultRetentionDays`, which
applies to any timeline whose `RetentionDays` is 0. A value of 0 for
both means no expiry.

### 5.5 Event counter consistency

`TimelineStats.TotalEvents` is maintained as an atomic in-memory counter
per timeline, incremented on every successful write and decremented by
`Purge`. The counter is persisted to `meta.json` periodically (every 60
seconds and on graceful shutdown) and seeded from `meta.json` on store
open.

The counter is therefore **eventually consistent after a crash**: events
written after the last `meta.json` flush but before the crash are not
reflected in the seeded value at next open, causing a transient
under-count until the next write corrects it. The error is bounded by
the flush interval and self-heals on the next write.

This is a deliberate tradeoff. Exact-on-open counts would require either
a full key scan on open (O(total events) per timeline) or a synchronous
`meta.json` write on every commit (eliminating the batching advantage).
Neither is acceptable. Applications that require exact event counts for
billing or audit purposes should maintain their own counters outside the
timeseries store.

`Purge` decrements each timeline's counter as it processes that timeline.
If `Purge` is interrupted mid-run (context cancellation or crash), the
counters for completed timelines are correct and the counter for the
interrupted timeline is stale until the next `Purge` pass.

### 5.6 Storage estimates

Key sizes are fixed per dimension count (Section 5.2). Value sizes depend
on the number of numeric fields and payload size. Representative
per-event raw sizes for the IoT sensor reading profile (2 numeric fields,
small payload):

```
Key (dims=2):               26 bytes
Value (2 floats + payload): ~31 bytes
Pebble entry overhead:      ~13 bytes
─────────────────────────
Total raw:                  ~70 bytes/event
```

After Pebble block-level Zstd compression (~2.3x on this profile):

```
~30 bytes/event
```

**Deployment projections (Zstd, sensor reading timeline, dims=2):**

| Configuration                 | Events/month  | MB/month | GB/year |
|-------------------------------|---------------|----------|---------|
| 50 sensors @ 1/min            | 2,160,000     | 62       | 0.7     |
| 200 sensors @ 1/min           | 8,640,000     | 247      | 2.9     |
| 200 sensors @ 1/10s           | 51,840,000    | 1,482    | 17.6    |
| 500 sensors @ 1/min           | 21,600,000    | 617      | 7.3     |
| 500 sensors @ 1/10s           | 129,600,000   | 3,703    | 44.1    |

Add approximately 15–20% for dual-write timelines (event type index,
presence) under typical IoT workloads.

**Comparison with other approaches (effective bytes per event):**

| Approach                  | Bytes/event |
|---------------------------|-------------|
| SQLite JSON entity store  | ~490        |
| Pebble + Snappy           | ~48         |
| Pebble + Zstd             | ~30         |
| TimescaleDB (compressed)  | ~30–60      |

Pebble+Zstd achieves parity with TimescaleDB without requiring
PostgreSQL.

### 5.7 Write throughput

All writes for one logical event (across 1–2 timelines) are committed in
a single `batch.Commit(pebble.Sync)`. The fsync cost is paid once
regardless of how many timeline entries are in the batch.

Observed throughput (olu v0.9.2, container environment):

| Pattern                        | Throughput          |
|--------------------------------|---------------------|
| Single-event commits           | ~400 events/sec     |
| Batch of 500 (one fsync)       | ~160,000 events/sec |

All realistic IoT deployment scales fall well within the single-event
ceiling. For high-density ingest, the application layer should batch
events from multiple sensors before committing.

---

## 6. Configuration

New fields in the olu configuration:

    # Feature flag
    OLU_TIMESERIES_ENABLED=true

    # Pebble tuning
    OLU_TS_MEMTABLE_SIZE=67108864        # 64 MB memtable (default)
    OLU_TS_BLOCK_SIZE=32768              # 32 KB block size (default)
    OLU_TS_COMPRESSION=zstd              # "snappy", "zstd", or "none"
    OLU_TS_L0_COMPACTION_THRESHOLD=4     # L0 files before compaction
    OLU_TS_MAX_OPEN_FILES=500            # Per-tenant Pebble file limit

    # Retention
    OLU_TS_DEFAULT_RETENTION_DAYS=90     # Store-level fallback; 0 = no expiry
    OLU_TS_COMPACTION_INTERVAL=3600      # Seconds between retention sweeps
    OLU_TS_RETENTION_ENABLED=false       # Background retention goroutine (opt-in)

Pebble instances are opened lazily on first timeseries request to a
provisioned tenant, and closed on server shutdown.

---

## 7. Go Interfaces

### 7.1 Store

```go
type Store interface {
    // Timeline management
    DefineTimeline(id TimelineID, cfg TimelineConfig) error
    UpdateTimeline(id TimelineID, cfg TimelineConfig) error // name + RetentionDays only
    Timeline(id TimelineID) (TimelineConfig, bool)
    Timelines() []TimelineID

    // Write
    Append(ctx context.Context, e Event) error
    AppendBatch(ctx context.Context, events []Event) (int, error)

    // Read
    QueryRange(ctx context.Context, q RangeQuery) ([]Event, error)
    Latest(ctx context.Context, q LatestQuery) ([]Event, error)

    // Aggregate
    Aggregate(ctx context.Context, q AggregateQuery) ([]Bucket, error)

    // Retention — applies per-timeline RetentionDays, falls back to store default
    Purge(ctx context.Context) error

    // Diagnostics
    Stats(ctx context.Context) (*StoreStats, error)
    TimelineStats(ctx context.Context, id TimelineID) (*TimelineStats, error)

    // Lifecycle
    Close() error
}
```

### 7.2 Event and query types

```go
// Event is a single timeseries record written to or read from a timeline.
type Event struct {
    Timeline TimelineID
    Dims     []uint64  // len must equal timeline's Dims
    Time     time.Time
    Nums     []float64 // optional, up to 7; nil means no numeric fields
    Payload  []byte    // optional, caller-defined
}
```

The store validates that `len(Dims) == timeline.Dims` on every write and
that `1 ≤ len(Dims) ≤ timeline.Dims` on every query. All other
constraints on dimension values — non-zero, within an application-defined
range, referencing a known entity — are the calling layer's
responsibility. The store does not know what the dimensions represent.

```go
// RangeQuery retrieves events from a timeline over a time range.
// Dims is a leading prefix: 1 ≤ len(Dims) ≤ timeline.Dims.
type RangeQuery struct {
    Timeline TimelineID
    Dims     []uint64
    From     time.Time
    To       time.Time
    Limit    int    // default 1000, max 10000
    Order    string // "asc" (default) or "desc"
}

// LatestQuery retrieves the N most recent events matching a dimension prefix.
type LatestQuery struct {
    Timeline TimelineID
    Dims     []uint64
    N        int // default 10, max 10000
}

// AggregateQuery computes an aggregate over numeric field NumField
// for all events matching the dimension prefix and time range.
type AggregateQuery struct {
    Timeline TimelineID
    Dims     []uint64
    From     time.Time
    To       time.Time
    NumField uint8         // index into Nums (0-based, max 6)
    Function string        // "avg", "min", "max", "sum", "count"
    Interval time.Duration // 0 = scalar result; > 0 = time-bucketed
}

// Bucket holds one time bucket of an aggregation result.
type Bucket struct {
    Time  time.Time
    Value float64
    Count int
}

// StoreStats holds aggregate diagnostics for the entire tenant store.
type StoreStats struct {
    Timelines int
    DiskBytes int64
}

// TimelineStats holds diagnostics for a single timeline.
type TimelineStats struct {
    TotalEvents int64
    OldestEvent time.Time
    NewestEvent time.Time
}
```

### 7.3 Store configuration

```go
type StoreConfig struct {
    MemtableSize          int    // bytes
    BlockSize             int    // bytes
    Compression           string // "snappy", "zstd", or "none"
    L0CompactionThreshold int
    MaxOpenFiles          int
    DefaultRetentionDays  int    // store-level fallback; 0 = no expiry
}

type StoreFactory func(dir string, cfg StoreConfig) (Store, error)
```

### 7.4 Manager

```go
type Manager interface {
    Provision(ctx context.Context, tenantID uint16) error
    StoreFor(tenantID uint16) (Store, error)
    IsProvisioned(tenantID uint16) bool
    Close() error
}
```

---

## 8. Purge and Retention

`Purge(ctx)` is the single retention entry point, called by the
`RetentionWorker` once per sweep interval per provisioned tenant.

For each timeline in the registry, `Purge`:

1. Determines the applicable `RetentionDays` — timeline-level if set,
   otherwise the store-level `DefaultRetentionDays`.
2. If the effective `RetentionDays` is 0, skips the timeline (no expiry).
3. Computes `cutoff = now − RetentionDays`.
4. Scans the timeline's key prefix for keys with `ts < cutoff`; extracts
   the timestamp at byte offset `2 + Dims×8` (variable per timeline, read
   from the registry).
5. Deletes matching keys in batches of up to 10,000 to bound memory usage.
6. Checks `ctx.Err()` at the start of each batch to support cancellation.

The `RetentionWorker` calls `store.Purge(ctx)` per tenant. All cutoff
computation and per-timeline logic is encapsulated in `Purge`; the worker
has no knowledge of individual timeline configurations.

---

## 9. IoT Adapter Layer

The calling layer (`pkg/iot` or the application adapter) defines the
mapping from domain concepts to generic timeline IDs and dimension values.
This mapping is not part of the `pkg/timeseries` package.

### 9.1 Well-known timeline definitions

The following timelines cover the full range of IoT asset management
access patterns:

| ID     | Name               | Dims | d0            | d1             | Purpose |
|--------|--------------------|------|---------------|----------------|---------|
| 0x0001 | SensorReadings     | 2    | asset_id      | sensor_id      | Raw sensor data; per-asset and per-sensor queries |
| 0x0002 | EventsByAsset      | 2    | asset_id      | event_type_id  | Per-asset event history |
| 0x0003 | EventsByType       | 2    | event_type_id | asset_id       | Cross-asset event-type queries ("all door opens") |
| 0x0004 | Presence           | 1    | asset_id      | —              | Heartbeat / last-seen / uptime |
| 0x0005 | SensorAvailability | 2    | asset_id      | sensor_id      | Sensor reporting state transitions |
| 0x0006 | Location           | 1    | asset_id      | —              | Asset position over time |
| 0x0007 | Alerts             | 2    | asset_id      | alert_type_id  | Generated alerts; independent retention |
| 0x0008 | CommandHistory     | 1    | asset_id      | —              | Commands sent and outcomes |
| 0x0009 | SensorRollups      | 2    | asset_id      | sensor_id      | Pre-computed aggregates; independent retention |
| 0x000A | FleetByZone        | 2    | zone_id       | asset_id       | Zone-organised cross-asset queries |

All dimension values are stable numeric IDs managed by the entity store.
No hashing occurs in the timeseries layer.

**Notes:**

- Timelines 0x0002 and 0x0003 are a **dual-write pair**: every asset
  event is written to both in a single Pebble batch commit. They carry
  the same data organised by different leading dimensions to serve
  different query shapes efficiently.
- Timeline 0x0005 records state transitions only (sensor starts or stops
  reporting), not every reading. It is populated by a background
  availability worker, not the ingestion path.
- Timeline 0x0009 is populated by a background rollup worker that reads
  from 0x0001. Its `RetentionDays` is 0 (no expiry); timeline 0x0001
  expires after the configured retention window.
- Timelines 0x0004 (Presence) and 0x0005 (SensorAvailability) logically
  represent **interval data** — state that has a start and an end — but
  are implemented as point events. This is a meaningful limitation. To
  answer "was this asset online at 14:32?" the application must fetch all
  heartbeat events around that timestamp and infer connectivity state from
  the gaps between them. This inference breaks in two cases: if heartbeat
  intervals are irregular, a gap may be a real outage or simply a slow
  sender; and if retention has purged old events, the most recent event
  before the query time may no longer exist, making the state at T
  unknowable from the store alone. Uptime percentages and SLA reports are
  particularly affected: a 90-day retention window means connectivity state
  at the boundary cannot be accurately reconstructed. A native interval
  timeline type with key layout `[timeline_id:2][d0:8][start_ts:8][end_ts:8]`
  would store closed intervals explicitly, making overlap and gap queries
  O(result_set) without inference. The registry model accommodates this
  without structural changes to the store (see Section 16).

### 9.2 Write amplification in practice

| Event type         | Timelines written | Keys per event | Fsyncs |
|--------------------|-------------------|----------------|--------|
| Sensor reading     | 0x0001, 0x0004    | 2              | 1      |
| Asset event        | 0x0002, 0x0003    | 2              | 1      |
| Position update    | 0x0006, 0x000A    | 2              | 1      |
| Alert              | 0x0007            | 1              | 1      |
| Command            | 0x0008            | 1              | 1      |

All writes for one logical event are batched into a single Pebble
`Commit(pebble.Sync)`. The fsync cost is paid once regardless of how many
timeline entries are included.

### 9.3 Entity store as the asset registry

The timeseries store has no dimension enumeration — there is no
`ListAssets`, `ListSensors`, or equivalent. This is a deliberate design
decision, not a gap.

The entity store is the authoritative registry of all assets, sensors,
and their relationships. Every asset was explicitly created there; it has
an ID, a type, attributes, and graph edges to its sensors. The right
answer to "what assets does this tenant have?" comes from the entity
store, not the timeseries store.

The consequence is that timeseries queries are never freestanding. The
calling pattern is always:

1. Query the entity store to determine which asset or sensor IDs are
   relevant — by type, by zone, by attribute, by graph traversal.
2. Use those IDs as dimension values in timeseries queries.

Step 1 provides the dimension values. Step 2 uses them. The timeseries
store is never the entry point for discovery, only for retrieval once the
relevant IDs are known.

Adding dimension enumeration to the timeseries store would duplicate
responsibility and create a second, potentially inconsistent source of
truth for which assets exist. It would also couple the timeseries store
to the lifecycle of the entity store — an asset deleted from the entity
store could still appear in a timeseries dimension scan, requiring
cross-store reconciliation.

For applications that do not use olu's entity store, dimension
enumeration would need to be provided by whatever registry they maintain.
The absence of it in `pkg/timeseries` is the correct expression of this
separation of concerns.

---

## 10. HTTP API

All timeseries endpoints live under the tenant route prefix:

    /api/v1/tenant/{tenant_id}/ts/...

### 10.1 Provisioning

**Enable timeseries for a tenant:**

    POST /api/v1/tenant/{tenant_id}/ts/provision

No required body. The store is created with the server-level
`StoreConfig` defaults. Idempotent — re-provisioning an already
provisioned tenant returns 200 with current configuration.

Response `201 Created`:

```json
{
  "tenant_id": "acme",
  "timeseries": "enabled"
}
```

**Disable timeseries for a tenant (admin binary only):**

Deprovisioning closes the Pebble instance and removes the data directory.
There is no HTTP endpoint for this operation. It is performed by the
admin binary directly against the filesystem. This is intentional —
deprovisioning is a destructive, rarely-needed operation that should
require explicit operator access to the server.

### 10.2 Timeline management

**Define a timeline:**

    POST /api/v1/tenant/{tenant_id}/ts/timelines

Request body:

```json
{
  "id": 1,
  "name": "SensorReadings",
  "dims": 2,
  "retention_days": 90
}
```

Response `201 Created`:

```json
{
  "id": 1,
  "name": "SensorReadings",
  "dims": 2,
  "retention_days": 90,
  "created_at": "2026-03-02T14:00:00Z"
}
```

**List all timelines:**

    GET /api/v1/tenant/{tenant_id}/ts/timelines

**Get a specific timeline:**

    GET /api/v1/tenant/{tenant_id}/ts/timelines/{timeline_id}

**Update a timeline (name and retention_days only):**

    PATCH /api/v1/tenant/{tenant_id}/ts/timelines/{timeline_id}

Attempting to change `dims` after the first write returns `409 Conflict`
with OLU-TS016.

### 10.3 Append

**Single event:**

    POST /api/v1/tenant/{tenant_id}/ts/events

Request body:

```json
{
  "timeline": 1,
  "dims": [42, 7],
  "time": "2026-03-02T14:30:00Z",
  "nums": [22.5, 45.0],
  "payload": {"unit": "°C", "quality": "good"}
}
```

`dims` must have exactly the number of elements declared for that
timeline. `nums` is optional (up to 7 values, indexed 0–6). `payload`
is stored as raw bytes and is caller-defined. Response `201 Created`.

**Batch append:**

    POST /api/v1/tenant/{tenant_id}/ts/events/batch

Request body:

```json
{
  "events": [
    {
      "timeline": 1,
      "dims": [42, 7],
      "time": "2026-03-02T14:30:00Z",
      "nums": [22.5, 45.0]
    },
    ...
  ]
}
```

Maximum batch size: 5,000 events. All events in a batch are written in
a single Pebble batch (atomic). Response `200 OK`:

```json
{
  "total": 100,
  "accepted": 100,
  "failed": 0
}
```

### 10.4 Query

**Range query:**

    GET /api/v1/tenant/{tenant_id}/ts/events

Parameters:

    timeline   required  uint16
    dims       required  comma-separated uint64 values (leading prefix)
    from       required  ISO8601 timestamp (inclusive)
    to         required  ISO8601 timestamp (inclusive)
    limit      optional  max results (default 1000, max 10000)
    order      optional  "asc" (default) or "desc"

`dims` specifies a leading dimension prefix. The constraint is
`1 ≤ len(dims) ≤ timeline.Dims`. Providing more values than the timeline
declares returns OLU-TS007. Providing zero values returns OLU-TS007.

When `len(dims) == timeline.Dims`, the query is a single bounded Pebble
key seek — O(result_set). When `len(dims) < timeline.Dims`, the query
scans all series sharing that prefix, still bounded and sequential, but
spanning multiple series.

Response `200 OK`:

```json
{
  "count": 42,
  "events": [
    {
      "timeline": 1,
      "dims": [42, 7],
      "time": "2026-03-02T14:30:00Z",
      "nums": [22.5, 45.0],
      "payload": "..."
    },
    ...
  ]
}
```

**Latest N events:**

    GET /api/v1/tenant/{tenant_id}/ts/events/latest

Parameters:

    timeline   required  uint16
    dims       required  comma-separated uint64 values (leading prefix)
    n          optional  default 10, max 10000

Returns the most recent N events matching the dimension prefix, in
reverse-chronological order.

### 10.5 Aggregation

    POST /api/v1/tenant/{tenant_id}/ts/aggregate

Request body:

```json
{
  "timeline": 1,
  "dims": [42, 7],
  "from": "2026-03-01T00:00:00Z",
  "to": "2026-03-02T00:00:00Z",
  "num_field": 0,
  "function": "avg",
  "interval": "1h"
}
```

Parameters:

    timeline    required  uint16
    dims        required  leading dimension prefix
    from        required  ISO8601
    to          required  ISO8601
    num_field   required  0–6 (index into the event's Nums slice)
    function    required  "avg", "min", "max", "sum", "count"
    interval    optional  bucketing duration: "1m", "5m", "15m", "30m",
                          "1h", "6h", "12h", "1d", "7d"
                          omit for a single scalar result

Response `200 OK` (with interval):

```json
{
  "timeline": 1,
  "num_field": 0,
  "function": "avg",
  "interval": "1h",
  "buckets": [
    {"time": "2026-03-01T00:00:00Z", "value": 21.3, "count": 60},
    {"time": "2026-03-01T01:00:00Z", "value": 21.8, "count": 60},
    ...
  ]
}
```

Response `200 OK` (without interval — scalar):

```json
{
  "timeline": 1,
  "num_field": 0,
  "function": "avg",
  "value": 21.7,
  "count": 1440,
  "from": "2026-03-01T00:00:00Z",
  "to": "2026-03-02T00:00:00Z"
}
```

### 10.6 Retention management

**View retention configuration:**

    GET /api/v1/tenant/{tenant_id}/ts/retention

Response:

```json
{
  "default_retention_days": 90,
  "timelines": [
    {"id": 1, "name": "SensorReadings",  "retention_days": 90},
    {"id": 9, "name": "SensorRollups",   "retention_days": 0}
  ]
}
```

**Update store-level default:**

    PATCH /api/v1/tenant/{tenant_id}/ts/retention

```json
{ "default_retention_days": 30 }
```

Per-timeline retention is updated via the timeline PATCH endpoint
(Section 10.2).

### 10.7 Diagnostics

**Store-level stats:**

    GET /api/v1/tenant/{tenant_id}/ts/stats

Response:

```json
{
  "tenant_id": "acme",
  "timelines": 10,
  "disk_bytes": 1340000000
}
```

**Per-timeline stats:**

    GET /api/v1/tenant/{tenant_id}/ts/stats/{timeline_id}

Response:

```json
{
  "timeline_id": 1,
  "name": "SensorReadings",
  "total_events": 21600000,
  "oldest_event": "2026-01-01T00:00:00Z",
  "newest_event": "2026-03-02T14:30:00Z"
}
```

### 10.8 Sugar endpoints

The server layer exposes domain-specific composite endpoints that fan out
to multiple underlying stores concurrently. For example:

    GET /api/v1/tenant/{tenant_id}/assets/{asset_id}/readings

This endpoint fetches asset metadata from the entity store and sensor
readings from the timeseries store in parallel, then joins the results
into a single coherent response. The client pays one network round trip
regardless of the number of underlying stores involved.

The parallel fan-out uses `errgroup`:

```go
g, ctx := errgroup.WithContext(ctx)
g.Go(func() error { asset, err = entityStore.Get(ctx, assetID); return err })
g.Go(func() error { events, err = tsStore.QueryRange(ctx, q); return err })
_ = g.Wait()
```

Wall-clock latency is `max(entity_latency, ts_latency)`, not their sum.
Because both stores are local — same process, same disk — both latencies
are sub-millisecond for typical queries. For a future remote backend
(e.g. Postgres for the entity store), the parallel pattern continues to
hold; two concurrent remote queries rather than two sequential ones.

Sugar endpoints are defined in the IoT adapter layer, not in
`pkg/timeseries`.

---

## 11. Error Codes

| Code      | Meaning |
|-----------|---------|
| OLU-TS001 | Timeseries not available (wrong tenant mode) |
| OLU-TS002 | Timeseries not enabled (feature flag off) |
| OLU-TS003 | Tenant not provisioned for timeseries |
| OLU-TS004 | Timeline not defined |
| OLU-TS005 | Invalid timestamp (before epoch, or unparseable) |
| OLU-TS006 | Batch too large (> 5000 events) |
| OLU-TS007 | Wrong dimension count for timeline |
| OLU-TS008 | Invalid aggregation function |
| OLU-TS009 | Invalid num_field index (> 6) |
| OLU-TS010 | Invalid interval format |
| OLU-TS011 | Query range too wide (> 366 days) |
| OLU-TS012 | Result limit exceeded (> 10000) |
| OLU-TS013 | Store error (internal) |
| OLU-TS014 | Retention update failed |
| OLU-TS015 | Provision failed (disk error, permissions) |
| OLU-TS016 | Timeline dims immutable after first write |
| OLU-TS017 | NaN value rejected |
| OLU-TS018 | Timeline ID reserved (0x0000) |

---

## 12. Performance: Comparison with Relational Databases

Pebble LSM range scans on time-ordered keys are sequential I/O. A query
for "all readings from asset 42 sensor 7 in the last hour" is a bounded
seek and forward iteration — no random page reads, no additional index
lookups beyond the initial seek.

Equivalent queries against an InnoDB table (even with proper indexing)
require random page reads under a cold buffer pool, and secondary index
fragmentation degrades over time. For time-series append-only workloads,
LSM-tree range scans consistently outperform B-tree range scans at scale.

InnoDB with a warm buffer pool, single-row inserts with one secondary
index: roughly 5,000–15,000 rows/sec. With batched multi-row inserts:
50,000–100,000 rows/sec, without per-row durability. Pebble with
batch=500 and `pebble.Sync` on every commit reaches ~160,000 events/sec
with full per-batch durability.

InnoDB has an advantage for in-place updates and joins. Since timeseries
data is append-only and entity metadata lives in a separate store, those
advantages do not apply to this workload.

---

## 13. Backup, Archival, and Thawing

> **Implementation status:** The `iolu` binary described in this
> section has not yet been implemented. Until tooling is available, back
> up Pebble directories manually with the server stopped. See MANUAL.md
> for current guidance.

### 13.1 Backup

Pebble SSTables are immutable once written. A consistent backup is taken
by creating a Pebble checkpoint, which hard-links the current set of
SSTables into a snapshot directory. This is instantaneous regardless of
data volume and does not block reads or writes.

    iolu ts backup --tenant acme --output /backup/acme-ts-20260302/

Internally this calls Pebble's `Checkpoint()` method. The checkpoint
directory can then be compressed and transferred:

    tar -cf - /backup/acme-ts-20260302/ | zstd -3 > acme-ts-20260302.tar.zst

Since SSTables are already Zstd-compressed internally, outer compression
yields only 5–15% further reduction.

**Backup size estimates (Zstd-compressed Pebble, sensor readings):**

| Scenario                        | Live size  | Backup size |
|---------------------------------|------------|-------------|
| 50 sensors, 1/min, 30 days      | 62 MB      | 56 MB       |
| 50 sensors, 1/min, 90 days      | 187 MB     | 168 MB      |
| 200 sensors, 1/min, 30 days     | 247 MB     | 222 MB      |
| 200 sensors, 1/min, 90 days     | 741 MB     | 667 MB      |
| 500 sensors, 1/min, 30 days     | 617 MB     | 555 MB      |
| 500 sensors, 1/min, 90 days     | 1.9 GB     | 1.7 GB      |
| 500 sensors, 1/10s, 90 days     | 11.1 GB    | 10.0 GB     |

Backup frequency recommendation: daily for active tenants, weekly for
low-activity tenants.

### 13.2 Restore

Restore replaces the tenant's Pebble data directory with a backup:

    iolu ts restore --tenant acme --input /backup/acme-ts-20260302/

The tenant's store must be closed before restore. After restore, the
Pebble instance is reopened immediately. No rebuilding or replay is
required — the SSTables are the data.

For a live instance, the restore sequence is:

1. Pause timeseries ingest for the tenant (503 on `/ts/` routes).
2. Close the Pebble instance.
3. Replace the data directory.
4. Reopen the Pebble instance.
5. Resume ingest.

The pause window is typically under 1 second for a local restore.

### 13.3 Archival

**Archive process:**

1. **Select time range.** Events older than `retention_days` are
   candidates for archival.

2. **Export.** Iterate the Pebble key range for the target time range
   and write to the archive format. Supported formats:

   - **Pebble snapshot** (recommended). Create a checkpoint of the data
     being archived. Preserves binary format; enables fast restoration.
   - **Parquet.** Columnar format, partitioned by month. Best for
     long-term storage where external analytics tools may query the
     data. Achieves better compression (~20–25 bytes/event) via
     per-column encoding.
   - **JSON Lines.** Largest format but universally readable.

3. **Verify.** Checksum the archive. Read back a sample and compare.

4. **Purge from live.** Delete the archived key range using bounded
   Pebble deletes. Actual disk reclamation happens during background
   compaction. A manual compaction can be triggered to reclaim space
   immediately:

       iolu ts compact --tenant acme

5. **Audit.** Record the operation in the registry audit log.

**Archive storage layout:**

    s3://olu-archive/
      acme/
        ts/
          2025-12.pebble.tar.zst
          2026-01.pebble.tar.zst
          _manifest.json           ← date ranges, event counts, checksums
        entities/
          ...

### 13.4 Thawing

Thawing restores archived timeseries data to a hot instance for
compliance audits, incident investigations, or trend analysis that
exceeds the retention window.

**Thaw to live store:**

    iolu ts thaw --tenant acme --archive 2025-12

Downloads the archive and ingests events into the tenant's live store.

Thaw time estimates:

| Archive size   | Events     | Estimated thaw time |
|----------------|------------|---------------------|
| 100 MB         | ~3M events | 2–10 seconds        |
| 1 GB           | ~33M events| 20–100 seconds      |
| 5 GB           | ~165M events| 2–8 minutes        |
| 10 GB          | ~330M events| 4–16 minutes       |

For Pebble snapshot archives, a faster path copies the archived SSTables
directly into the live instance's data directory and triggers an ingest,
bypassing the decode-re-encode cycle. The tenant's store must be briefly
paused.

**Thaw to temporary read-only instance:**

    iolu ts thaw --tenant acme --archive 2025-12 --readonly

Opens the archived snapshot as a separate read-only instance on a
temporary port. Preferred for one-off queries — avoids polluting the
live store with old data that would need to be re-archived.

---

## 14. Fleet Integration

### 14.1 Migration between instances

When a tenant is migrated between hot instances, the timeseries data
directory must be transferred alongside the SQLite database. The
migration sequence extends as follows:

1. Admin sets tenant status to `migrating`.
2. Admin exports SQLite data from source to destination (existing step).
3. Admin transfers timeseries data — create a Pebble checkpoint on the
   source, compress, and transfer to the destination.
4. Admin verifies event counts and checksums for both stores.
5. Admin creates new placement with incremented epoch.
6. Admin sets tenant status to `active` on destination.

The `Retry-After` estimate must account for the timeseries transfer
volume in addition to the SQLite data size. The admin binary computes
this from the tenant's `ts/stats` endpoint:

    total_transfer = sqlite_size + ts_disk_bytes
    estimated_seconds = ceil(total_transfer / transfer_rate_bytes_per_sec × 1.5)

### 14.2 Data size reporting

    iolu tenant info acme

    Tenant: acme (ID: 0x0001)
    Status: active
    Instance: hot-east-01
    SQLite: 42.3 MB (entities, graph, FTS)
    Timeseries: 741 MB (Pebble, Zstd, 90-day retention, 10 timelines)
    Total: 783 MB

### 14.3 Fleet-wide retention

Retention policies are stored in `registry.json` per tenant. Policies
follow the tenant when it moves between instances.

### 14.4 Capacity planning

    iolu ts growth --tenant acme

    Tenant: acme
    Current size: 741 MB
    30-day growth: 247 MB/month (sensor readings timeline)
    Projected 90-day: 1.5 GB
    Projected 1-year: 3.7 GB

---

## 15. Migration from the v0.1 Implementation

The v0.1 implementation (olu v0.9.x, `pkg/timeseries`) is the starting
point. This section describes what changes, what is new, and what is
deleted.

### Phase 1: Core storage layer

**Rewrite entirely:**

- `pkg/timeseries/types.go` — replace domain-specific types (`TSEvent`,
  `TSRangeQuery`, `TSAggregateQuery`, `TSRetentionConfig`) with generic
  types (`Event`, `RangeQuery`, `LatestQuery`, `AggregateQuery`,
  `TimelineConfig`, `StoreStats`, `TimelineStats`). Replace `TimeseriesStore`
  and `TimeseriesManager` interfaces with `Store` and `Manager`.
- `pkg/timeseries/codec.go` — replace fixed 18-byte primary and 14-byte
  secondary key layouts with the variable-length `[timeline_id:2][dims][ts:8]`
  layout. Replace domain-specific value fields with generic flags+numerics+
  payload encoding. Remove all secondary index encoding.
- `pkg/timeseries/store.go` — replace `PebbleTimeseriesStore` with
  `PebbleStore`. Remove `GetRetention`, `SetRetention`, `PurgeOlderThan`;
  replace with `Purge`. Add `DefineTimeline`, `UpdateTimeline`, `Timeline`,
  `Timelines`, `TimelineStats`. Remove secondary index writes from `Append`
  and `AppendBatch`.
- `pkg/timeseries/manager.go` — `Provision` loses the `TSRetentionConfig`
  argument. Internal changes to load `registry.json` alongside `meta.json`.

**Add new:**

- `pkg/timeseries/registry.go` — `TimelineConfig` JSON persistence and
  in-memory map. New; no equivalent in v0.1.

**Delete:**

- `pkg/timeseries/triggers.go` — domain-specific trigger type dictionary.
  Moves to the IoT adapter layer (`pkg/iot`).

**Retain unchanged:**

- `pkg/timeseries/retention.go` — `RetentionWorker` structure unchanged;
  its call to `store.PurgeOlderThan(cutoff)` becomes `store.Purge(ctx)`.

### Phase 2: API layer

- Replace error codes OLU-TS004–TS015 with the new set (Section 11).
- Add timeline management handlers (`POST`, `GET`, `PATCH` on `/ts/timelines`).
- Update write, read, aggregate, and retention handlers for the new types.
- Update `pkg/server/server.go` route registration to include timeline
  management routes.

### Phase 3: IoT adapter

New package, no equivalent in v0.1:

- `pkg/iot/timelines.go` — well-known timeline ID constants and
  `TimelineConfig` definitions for the ten timelines in Section 9.1.
- `pkg/iot/writer.go` — domain-level write helpers that translate sensor
  readings, asset events, presence pings, etc. into `Event` values and
  fan out to the correct timelines in a single batch commit.
- Sugar endpoints added to the server layer (Section 10.8).

### Phase 4: Data migration

Existing v0.1 Pebble data is not readable by the v0.3 store — the key
formats are incompatible. Run `olu-migrate ts upgrade` per tenant before
bringing the new store online (Section 14 covers the migration procedure).

### Phase 5: Tests

- Codec tests: extend to cover all dimension counts (1–5) and the new
  value encoding. Remove secondary index codec tests.
- Store tests: update all existing tests for new interface. Add per-timeline
  retention tests and variable timestamp offset in purge.
- Registry tests: new — define, persist, reopen, verify; `Dims` immutability
  after first write.
- Manager tests: update `Provision` signature; add registry load-on-open.
- Retire `pkg/server/ts_e2e_test.go` test cases for removed endpoints
  (`/ts/retention` global PATCH); add timeline management e2e tests.
- Retire TIMESERIES_DESIGN.md and TIMESERIES_DESIGN_V2.md.


---

## 16. Backend Alternatives

The `StoreFactory` interface makes the Pebble backend swappable without
structural changes to the timeseries layer.

### YogaDB

[YogaDB](https://github.com/glycerine/yogadb) is a pure-Go embedded
key-value store based on the FlexSpace architecture (EuroSys '22). It
claims 2x faster writes than Pebble in its own benchmarks by deferring
GC rather than compacting eagerly. The `Ascend`/`Descend` iterator API
maps cleanly to our range-scan access patterns, and `Batch.Commit()` with
the redo log corresponds to our `batch.Commit(pebble.Sync)` calls.

**Why not yet.** Two blockers make it unsuitable for the initial
implementation:

1. **No block-level compression.** YogaDB's benchmark shows 49 MB for a
   dataset Pebble stores in 14 MB. The storage projections in Section 5.6
   are predicated on Pebble+Zstd's ~30 bytes/event. Without equivalent
   compression, those estimates are invalid by a factor of ~3.5x.

2. **Maturity.** As of March 2026 the repository has 12 stars, zero
   issues, and no known production deployments. The faster write headline
   is also a space-time trade — write amplification is 5.5x versus
   Pebble's 2.3x in comparable benchmarks; the deferred `VacuumKV()` cost
   is not eliminated. The `Checkpoint()` API we rely on for tenant backup
   has no direct equivalent.

**Revisit when** YogaDB gains block-level compression and has a credible
deployment history. The underlying architecture is sound and the Go API
is clean. If compression parity is reached, the write-latency advantage
on SSDs could benefit high-density ingest scenarios.
