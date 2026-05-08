# Olu Manual

Complete reference documentation for Olu v0.9.7-patched43.

## Table of Contents

1. [Installation](#installation)
2. [Configuration](#configuration)
3. [API Reference](#api-reference)
4. [Query Languages](#query-languages)
5. [Authentication](#authentication)
6. [Rate Limiting](#rate-limiting)
7. [Metrics & Monitoring](#metrics--monitoring)
8. [Storage Backends](#storage-backends)
9. [Graph Features](#graph-features)
10. [Testing & Benchmarks](#testing--benchmarks)
11. [Deployment](#deployment)

Timeseries storage has its own design document: [Timeseries Design](docs/TIMESERIES_DESIGN_V3.md).

---

## Installation

### From Source

```bash
git clone https://github.com/ha1tch/xolu.git
cd olu
make build
./bin/olu
```

### Using Go Install

```bash
go install github.com/ha1tch/xolu/cmd/olu@latest
```

### Docker

```bash
docker pull ghcr.io/ha1tch/olu:latest
docker run -p 9090:9090 -v $(pwd)/data:/data ghcr.io/ha1tch/olu:latest
```

### Build Options

```bash
make build          # Build binary
make build-all      # Build for all platforms (18 OS/arch)
make docker-build   # Build Docker image
make install        # Install to $GOPATH/bin
```

---

## Configuration

All configuration is via environment variables.

### Server

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_HOST` | `0.0.0.0` | Server bind address |
| `OLU_PORT` | `9090` | Server port |

### Storage

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_STORAGE_TYPE` | `jsonfile` | Backend: `jsonfile` or `sqlite` |
| `OLU_BASE_DIR` | `data` | Base directory for JSONFile storage |
| `OLU_DB_PATH` | `olu.db` | SQLite database path |
| `OLU_SCHEMA_NAME` | `default` | Schema/namespace name |

### Cache

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_CACHE_TYPE` | `memory` | Cache type: `memory` or `redis` |
| `OLU_CACHE_TTL` | `300` | Cache TTL in seconds |
| `OLU_REDIS_HOST` | `localhost` | Redis host (if using redis cache) |
| `OLU_REDIS_PORT` | `6379` | Redis port |

### Graph

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_GRAPH_MODE` | `flat` | Graph mode: `flat` or `disabled` |
| `OLU_GRAPH_CYCLE_DETECTION` | `warn` | Cycle handling: `warn`, `error`, `ignore` |
| `OLU_GRAPH_MAX_VISITED_NODES` | `10000` | Max nodes visited during a single traversal |
| `OLU_GRAPH_MAX_RESULTS` | `10000` | Max result paths returned by a graph query |

When a graph limit is exceeded, the server returns a specific error code:

| Code | Meaning | HTTP Status |
|------|---------|-------------|
| `OLU-GR005` | Visited-node limit exceeded | 413 |
| `OLU-GR006` | Result limit exceeded | 413 |

Graph queries also respect the shared `OLU_QUERY_TIMEOUT` and
`OLU_QUERY_MAX_RESPONSE_BYTES` limits documented in the Query Guardrails
section below.

### Features

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_FULLTEXT_ENABLED` | `false` | Enable FTS5 full-text search (SQLite only) |
| `OLU_CASCADING_DELETE` | `false` | Delete referencing entities on delete |
| `OLU_REF_EMBED_DEPTH` | `3` | Default reference embedding depth |
| `OLU_MAX_EMBED_DEPTH` | `10` | Maximum allowed embed depth |
| `OLU_MAX_ENTITY_SIZE` | `1048576` | Maximum entity size in bytes |
| `OLU_TENANT_MODE` | `path` | Tenant mode: `path` or `strict` |
| `OLU_TENANT_AUTO_REGISTER` | `false` | Auto-create tenants on first access (path mode only) |
| `OLU_TIMESERIES_ENABLED` | `false` | Enable Pebble-backed timeseries storage |
| `OLU_TS_MEMTABLE_SIZE` | `67108864` | Pebble memtable size in bytes (64 MB) |
| `OLU_TS_BLOCK_SIZE` | `32768` | Pebble block size in bytes (32 KB) |
| `OLU_TS_COMPRESSION` | `zstd` | Compression: `zstd`, `snappy`, or `none` |
| `OLU_TS_L0_COMPACTION_THRESHOLD` | `4` | L0 files before compaction trigger |
| `OLU_TS_MAX_OPEN_FILES` | `500` | Per-tenant Pebble file descriptor limit |
| `OLU_TS_DEFAULT_RETENTION_DAYS` | `90` | Default retention policy for new tenants |
| `OLU_TS_COMPACTION_INTERVAL` | `3600` | Retention sweep interval in seconds |
| `OLU_TS_RETENTION_ENABLED` | `false` | Run background retention goroutine |
| `OLU_TS_QUERY_TIMEOUT` | `30` | Per-query context deadline in seconds |
| `OLU_TS_MAX_QUERY_EVENTS` | `10000` | Maximum events returned by a single range query or Latest |
| `OLU_TS_MAX_SCAN_EVENTS` | `500000` | Maximum events scanned before aborting (returns OLU-TS013) |
| `OLU_TS_MAX_RANGE_DAYS` | `366` | Maximum From→To window in days (returns OLU-TS011 if exceeded) |
| `OLU_TS_MAX_BATCH_SIZE` | `5000` | Maximum events per batch append (returns OLU-TS006 if exceeded) |
| `OLU_TS_MAX_RESPONSE_BYTES` | `10485760` | Maximum JSON response size in bytes (10 MB) |
| `OLU_TS_MAX_AGGREGATE_BUCKETS` | `10000` | Maximum time buckets in a windowed aggregate (returns OLU-TS019 if exceeded) |

### SQLite Tuning

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_SQLITE_MAX_OPEN_CONNS` | `0` | Writer pool max connections (0 = backend default: 1 for WAL) |
| `OLU_SQLITE_MAX_IDLE_CONNS` | `0` | Writer pool idle connections (0 = backend default: 1) |
| `OLU_SQLITE_READ_POOL_SIZE` | `0` | Reader pool max connections (0 = backend default: NumCPU) |
| `OLU_SQLITE_BUSY_TIMEOUT` | `5000` | SQLite busy timeout in milliseconds |
| `OLU_SQLITE_CACHE_SIZE` | `2000` | SQLite page cache size (pages) |
| `OLU_SQLITE_CONTENTION_THRESHOLD` | `95` | Adaptive lock contention threshold (0-100) |
| `OLU_PATCH_NULL` | `store` | Null handling in PATCH: `store` or `delete` |

### Query Guardrails

Server-side limits that prevent runaway queries from becoming outages.
All limits are on by default and enforced consistently across OQL, search,
and list endpoints.

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_QUERY_TIMEOUT` | `30` | Max query execution time in seconds |
| `OLU_QUERY_MAX_ROWS` | `10000` | Max rows returned by a single query |
| `OLU_QUERY_MAX_SCAN_ROWS` | `100000` | Max rows scanned before query is aborted |
| `OLU_QUERY_MAX_RESPONSE_BYTES` | `10485760` | Max JSON response size in bytes (10 MB) |

When a limit is exceeded, the server returns a specific error code:

| Code | Meaning | HTTP Status |
|------|---------|-------------|
| `OLU-QL008` | Query timed out | 504 |
| `OLU-QL009` | Too many rows returned | 413 |
| `OLU-QL010` | Too many rows scanned | 413 |
| `OLU-QL011` | Response too large | 413 |

### Authentication

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_AUTH_TYPE` | `none` | Auth type: `none`, `jwt`, `apikey` |
| `OLU_JWT_SECRET` | | Secret key for JWT validation |
| `OLU_JWT_ISSUER` | | Expected JWT issuer claim |
| `OLU_API_KEYS` | | Comma-separated list of valid API keys |

### Rate Limiting

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_RATE_LIMIT_ENABLED` | `false` | Enable rate limiting |
| `OLU_RATE_LIMIT_RATE` | `100` | Requests per window |
| `OLU_RATE_LIMIT_WINDOW` | `60` | Window duration in seconds |
| `OLU_RATE_LIMIT_BY_IP` | `true` | Rate limit by client IP |
| `OLU_RATE_LIMIT_BY_KEY` | `false` | Rate limit by auth key/subject |

### Observability

| Variable | Default | Description |
|----------|---------|-------------|
| `OLU_METRICS_ENABLED` | `true` | Enable Prometheus metrics |
| `OLU_DEBUG` | `false` | Enable debug logging |

---

## API Reference

### Entity Operations

#### Create Entity
```http
POST /api/v1/{entity}
Content-Type: application/json

{"name": "Alice", "email": "alice@example.com"}
```

Response: `201 Created`
```json
{"id": 1, "name": "Alice", "email": "alice@example.com"}
```

#### Get Entity
```http
GET /api/v1/{entity}/{id}
```

Query parameters:
- `embed=false` - Disable reference embedding
- `embed_depth=N` - Override default embed depth

#### List Entities
```http
GET /api/v1/{entity}?page=1&per_page=20
```

Response includes pagination:
```json
{
  "data": [...],
  "pagination": {
    "page": 1,
    "per_page": 20,
    "total_items": 100,
    "total_pages": 5
  }
}
```

#### Update Entity (Full)
```http
PUT /api/v1/{entity}/{id}
Content-Type: application/json

{"name": "Alice Smith", "email": "alice.smith@example.com"}
```

#### Update Entity (Partial)
```http
PATCH /api/v1/{entity}/{id}
Content-Type: application/json

{"email": "newemail@example.com"}
```

#### Delete Entity
```http
DELETE /api/v1/{entity}/{id}
```

#### Save Entity (Upsert with Caller-Specified ID)
```http
POST /api/v1/{entity}/save/{id}
Content-Type: application/json

{"name": "Bob", "email": "bob@example.com"}
```

Creates the entity if it does not exist; overwrites it completely if it does.
Never returns a conflict error for a duplicate ID.

Responses:
- `201 Created` — new record created with the specified ID.
- `200 OK` — existing record replaced.

Use this endpoint when the caller controls the ID (e.g. importing records
with known keys, idempotent configuration writes, migration). For
server-assigned IDs use `POST /api/v1/{entity}` instead.

### Optimistic Concurrency (Conditional Writes)

All three write endpoints — `PUT /{entity}/{id}`, `PATCH /{entity}/{id}`, and
`POST /{entity}/save/{id}` — support conditional writes via an integer version
field embedded in every entity response.

**Reading the version**

Every `GET` response includes `"_version": N` in the entity body:

```json
{
  "id": 42,
  "state": "in-transit",
  "_version": 7
}
```

**Writing conditionally**

Include `"_version": N` in the request body to make the write conditional.
olu checks the stored version inside the write transaction:

```http
POST /api/v1/tenant/acme/objects/save/device-001
Content-Type: application/json

{"state": "delivered", "_version": 7}
```

- `200 OK` / `201 Created` — stored version matched; version is now `8`.
- `409 Conflict` — stored version differed. Response body includes
  `"current_version"` so the caller can retry without an extra `GET`:

```json
{
  "error": {"code": "OLU-ST005", "message": "Version conflict ...", "status": 409},
  "current_version": 8
}
```

Omitting `_version` from the request body makes the write unconditional —
existing behaviour is preserved.

**FSM / CAS pattern**

```
1. GET  /tenant/{t}/objects/{giai}           → {"state": "A", "_version": 7}
2. Compute transition A → B
3. POST /tenant/{t}/objects/save/{giai}      body: {"state": "B", "_version": 7}
   → 200 OK  (version is now 8)
   → 409 Conflict + current_version  (another writer got there first; re-read and retry)
```

No locking or inter-process coordination is required. SQLite's transaction
semantics guarantee the check-and-write is atomic on a single olu instance.

### Multi-Tenant Operations

All entity operations support tenant isolation via URL prefix:

```http
GET /api/v1/tenant/{tenant_id}/{entity}
POST /api/v1/tenant/{tenant_id}/{entity}
GET /api/v1/tenant/{tenant_id}/{entity}/{id}
```

### Graph Operations

#### Shortest Path
```http
GET /api/v1/graph/shortestPath?from={entity}:{id}&to={entity}:{id}
```

#### Path Exists
```http
GET /api/v1/graph/pathExists?from={entity}:{id}&to={entity}:{id}
```

#### Common Neighbors
```http
GET /api/v1/graph/commonNeighbors?node1={entity}:{id}&node2={entity}:{id}
```

#### Node Information
```http
GET /api/v1/graph/node/{entity}:{id}
GET /api/v1/graph/node/{entity}:{id}/degree
GET /api/v1/graph/node/{entity}:{id}/neighbors?direction=out
```

### Search Operations

#### Full-Text Search (SQLite only)
```http
GET /api/v1/search?q={query}&entity={entity}
```

#### Field Search
```http
GET /api/v1/{entity}/search?field={field}&query={value}&match={type}
```

Match types: `exact`, `contains`, `prefix`, `suffix`

### Export Operations

```http
GET /api/v1/export
```

Returns a ZIP archive containing:
- `manifest.json` - Export metadata
- `entities.db` or `data/` - Entity data
- `graph.json` - Graph structure
- `graph.data`, `graph.index` - Binary graph files

### Schema Operations

```http
GET /api/v1/schema
GET /api/v1/schema/{entity}
POST /api/v1/schema/{entity}
```

### System Operations

```http
GET /health        # Health check
GET /version       # Version info
GET /metrics       # Prometheus metrics
```

### Timeseries Operations

Available only when `OLU_TIMESERIES_ENABLED=true` and `OLU_TENANT_MODE=strict`.
All timeseries endpoints are tenant-scoped under `/api/v1/tenant/{id}/ts/`.

Data is stored in per-tenant Pebble (LSM) instances with Zstd compression.
~30 bytes per event effective. See [Timeseries Design](docs/TIMESERIES_DESIGN_V3.md)
for the full specification.

#### Provisioning

```http
POST /api/v1/tenant/{id}/ts/provision
```

Enables timeseries storage for a tenant. Idempotent. The tenant must already
exist in the registry (`OLU_TENANT_MODE=strict` requires this). Returns 201
on creation, 200 if already provisioned.

#### Timeline Management

Each tenant has up to 65535 named timelines (IDs 1–0xFFFF). A timeline
declares a fixed number of *dimensions* (1–5 uint64 values) used as the
sort key prefix. Dimensions are immutable after the first event is written.

```http
POST   /api/v1/tenant/{id}/ts/timelines              # Define a timeline
GET    /api/v1/tenant/{id}/ts/timelines              # List all timelines
GET    /api/v1/tenant/{id}/ts/timelines/{tid}        # Get a timeline
PATCH  /api/v1/tenant/{id}/ts/timelines/{tid}        # Update name / retention
GET    /api/v1/tenant/{id}/ts/timelines/{tid}/stats  # Timeline diagnostics
```

Define request body:
```json
{
  "id": 1,
  "dims": 2,
  "name": "temperature",
  "retention_days": 90
}
```

`dims` is required on define and immutable after the first write. `name` and
`retention_days` can be changed freely via PATCH. `retention_days: 0` inherits
the store-level default (`OLU_TS_DEFAULT_RETENTION_DAYS`); a negative value
disables expiry for the timeline.

#### Writing Events

```http
POST /api/v1/tenant/{id}/ts/events         # Single event
POST /api/v1/tenant/{id}/ts/events/batch   # Atomic batch (up to OLU_TS_MAX_BATCH_SIZE)
```

Single event body:
```json
{
  "timeline": 1,
  "dims":     [42, 7],
  "time":     "2026-01-15T10:30:00Z",
  "nums":     [23.4, 1013.25],
  "payload":  "base64encodedopaquebytes"
}
```

`dims` must match the timeline's declared dimension count. `time` must be an
RFC 3339 timestamp at or after the Unix epoch. `nums` holds up to 7 float64
values (no NaN). `payload` is optional opaque bytes (up to 64 KB, base64).

Batch body:
```json
{ "events": [ ...event objects... ] }
```

A batch is atomic: if any event fails validation, no events are written.

#### Querying Events

```http
GET /api/v1/tenant/{id}/ts/events?timeline=1&dims=42,7&from=...&to=...
```

Query parameters: `timeline` (required), `dims` (required, comma-separated),
`from` / `to` (required, RFC 3339), `limit` (default 1000, max capped by
`OLU_TS_MAX_QUERY_EVENTS`), `order` (`asc` or `desc`, default `asc`).

`dims` may be a *prefix*: supplying fewer values than the timeline's dimension
count returns all events matching that leading prefix across all remaining
dimension values. A Go-side time filter is applied to prevent out-of-range
events leaking through the prefix scan.

```http
GET /api/v1/tenant/{id}/ts/events/latest?timeline=1&dims=42,7&n=10
```

Returns the N most recent events (default 10, max capped by
`OLU_TS_MAX_QUERY_EVENTS`) matching the dimension prefix.

#### Aggregation

```http
POST /api/v1/tenant/{id}/ts/aggregate
```

```json
{
  "timeline":  1,
  "dims":      [42],
  "from":      "2026-01-01T00:00:00Z",
  "to":        "2026-01-08T00:00:00Z",
  "function":  "avg",
  "num_field": 0,
  "interval":  "1h"
}
```

`function`: `avg`, `min`, `max`, `sum`, `count`. `num_field`: 0-based index
into the event's `nums` array (0–6). `interval`: one of `1m 5m 15m 30m 1h
6h 12h 1d 7d`. Omit `interval` for a scalar result. Partial-prefix `dims`
are supported with the same time filter as range queries.

#### Range Aggregate (single-pass, all fields)

```http
POST /api/v1/tenant/{id}/ts/range_aggregate
```

```json
{
  "timeline": 1,
  "dims":     [42],
  "from":     "2026-01-01T00:00:00Z",
  "to":       "2026-01-08T00:00:00Z"
}
```

Computes count, sum, avg, min, and max for **all seven numeric fields** in a
single Pebble scan pass. More efficient than issuing multiple `/aggregate`
calls when several fields are needed. No `function` or `num_field` parameter —
the result always covers all fields.

Response shape:

```json
{
  "count": 12500,
  "fields": [true, true, true, false, false, false, false],
  "sums":   [1230.5, 98432.1, 0.0, 0, 0, 0, 0],
  "avgs":   [0.098,  7.874,   0.0, 0, 0, 0, 0],
  "mins":   [0.001,  1.2,     0.0, 0, 0, 0, 0],
  "maxs":   [0.999,  99.9,    0.0, 0, 0, 0, 0]
}
```

`fields[i]` is `true` if field `i` was present in at least one event in the
range. Entries for absent fields are zero and should be ignored.

#### Convenience Range Functions

Single-field scalar functions over a range. All delegate to the same scan as
`range_aggregate` internally; performance is identical.

```http
GET /api/v1/tenant/{id}/ts/range/sum?timeline=1&dims=42&from=...&to=...&num_field=0
GET /api/v1/tenant/{id}/ts/range/avg?timeline=1&dims=42&from=...&to=...&num_field=0
GET /api/v1/tenant/{id}/ts/range/min?timeline=1&dims=42&from=...&to=...&num_field=0
GET /api/v1/tenant/{id}/ts/range/max?timeline=1&dims=42&from=...&to=...&num_field=0
GET /api/v1/tenant/{id}/ts/range/count?timeline=1&dims=42&from=...&to=...&num_field=0
```

Each returns `{ "value": <float64> }` (or `{ "count": <uint64> }` for count).
Use `range_aggregate` when you need more than one statistic to avoid redundant scans.

#### Retention and Diagnostics

```http
GET   /api/v1/tenant/{id}/ts/retention    # View store-level default retention
PATCH /api/v1/tenant/{id}/ts/retention    # Update store-level default retention
GET   /api/v1/tenant/{id}/ts/stats        # Tenant store diagnostics
```

Retention PATCH body: `{ "retention_days": 90 }`. Setting `0` disables expiry.

Stats response includes `timelines` (count) and `disk_bytes` (Pebble estimate).
Per-timeline stats (via `GET /ts/timelines/{tid}/stats`) include
`total_events` (approximate — eventually consistent after crash),
`oldest_event`, and `newest_event`.

#### Error Codes

| Code | HTTP | Meaning |
|------|------|---------|
| OLU-TS002 | 404/405 | Timeseries not enabled |
| OLU-TS003 | 400 | Tenant not provisioned for timeseries |
| OLU-TS004 | 400 | Timeline not defined |
| OLU-TS005 | 400 | Timestamp before Unix epoch or invalid format |
| OLU-TS006 | 400 | Batch exceeds `OLU_TS_MAX_BATCH_SIZE` |
| OLU-TS007 | 400 | Dimension count mismatch |
| OLU-TS008 | 400 | Unknown aggregate function |
| OLU-TS009 | 400 | `num_field` out of range (0–6) |
| OLU-TS010 | 400 | Invalid interval value |
| OLU-TS011 | 400 | Query window exceeds `OLU_TS_MAX_RANGE_DAYS` |
| OLU-TS013 | 400 | Scan aborted — exceeded `OLU_TS_MAX_SCAN_EVENTS` |
| OLU-TS016 | 409 | Attempt to change dims after first write |
| OLU-TS017 | 400 | NaN in numeric field |
| OLU-TS018 | 400 | Reserved timeline ID (0) |
| OLU-TS019 | 400 | Aggregate bucket limit exceeded `OLU_TS_MAX_AGGREGATE_BUCKETS` |

---

## Query Languages

### OQL (Olu Query Language)

SQL-like query language for entities.

```http
POST /api/v1/oql/query
Content-Type: application/json

{"query": "SELECT * FROM users WHERE age > 25 ORDER BY name LIMIT 10"}
```

#### Supported Features

- `SELECT` with field selection and `*`
- `WHERE` with operators: `=`, `!=`, `>`, `<`, `>=`, `<=`, `LIKE`, `IN`
- `ORDER BY` with `ASC`/`DESC`
- `LIMIT` and `OFFSET`
- `GROUP BY` with aggregates: `COUNT`, `SUM`, `AVG`, `MIN`, `MAX`

#### Examples

```sql
-- Basic query
SELECT name, email FROM users WHERE status = 'active'

-- Aggregation
SELECT department, COUNT(*) as count, AVG(salary) as avg_salary
FROM employees
GROUP BY department

-- Pattern matching
SELECT * FROM products WHERE name LIKE '%widget%'

-- Sorting and pagination
SELECT * FROM orders ORDER BY created_at DESC LIMIT 20 OFFSET 40
```

#### Async Queries

For long-running queries:

```http
POST /api/v1/oql/async
Content-Type: application/json

{"query": "SELECT * FROM large_table"}
```

Returns job ID:
```json
{"job_id": "abc123", "status": "pending"}
```

Check status:
```http
GET /api/v1/oql/job/{job_id}
```

### Sulpher (Graph Query Language)

Path-based query language for graph traversal.

```http
POST /api/v1/sulpher/query
Content-Type: application/json

{"query": "users:1 -[*1..3]-> posts"}
```

#### Syntax

```
source -[edge_spec]-> target
```

Edge specifications:
- `*` - Any edge type
- `*1..3` - 1 to 3 hops
- `manages` - Specific edge type

#### Examples

```
-- Direct connections
users:1 -> posts

-- Multi-hop paths
users:1 -[*1..5]-> users

-- Bidirectional
users:1 <-> users:2
```

---

## Authentication

### JWT Authentication

```bash
export OLU_AUTH_TYPE=jwt
export OLU_JWT_SECRET=your-secret-key-min-32-chars
export OLU_JWT_ISSUER=your-app  # Optional
```

Request with JWT:
```http
GET /api/v1/users
Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
```

JWT requirements:
- Algorithm: HS256
- Claims: `sub` (subject), `exp` (expiration)
- Optional: `iss` (issuer), `nbf` (not before)

### API Key Authentication

```bash
export OLU_AUTH_TYPE=apikey
export OLU_API_KEYS=key1,key2,key3
```

Request with API key:
```http
GET /api/v1/users
X-API-Key: key1
```

Or:
```http
GET /api/v1/users
Authorization: ApiKey key1
```

### Excluded Paths

By default, these paths don't require authentication:
- `/health`
- `/version`
- `/metrics`

---

## Rate Limiting

Enable rate limiting to protect your API:

```bash
export OLU_RATE_LIMIT_ENABLED=true
export OLU_RATE_LIMIT_RATE=100      # requests
export OLU_RATE_LIMIT_WINDOW=60     # seconds
export OLU_RATE_LIMIT_BY_IP=true
```

### Response Headers

All responses include rate limit headers:

```http
X-RateLimit-Limit: 100
X-RateLimit-Remaining: 95
X-RateLimit-Reset: 1704067200
```

### Rate Limited Response

When limit exceeded:
```http
HTTP/1.1 429 Too Many Requests
Retry-After: 45

{"error": "Too Many Requests", "message": "Rate limit exceeded", "retry_after": 45}
```

---

## Metrics & Monitoring

### Prometheus Endpoint

```http
GET /metrics
```

### Available Metrics

| Metric | Type | Description |
|--------|------|-------------|
| `olu_uptime_seconds` | gauge | Server uptime |
| `olu_requests_total` | counter | Total HTTP requests |
| `olu_requests_by_status_total` | counter | Requests by status code |
| `olu_request_errors_total` | counter | Total 4xx/5xx responses |
| `olu_active_requests` | gauge | Current in-flight requests |
| `olu_request_duration_seconds_bucket` | histogram | Request latency distribution |
| `olu_entity_operations_total` | counter | CRUD operations by type |
| `olu_cache_total` | counter | Cache hits/misses |
| `olu_queries_total` | counter | Query operations by type |

### JSON Format

```http
GET /metrics
Accept: application/json
```

### Prometheus Configuration

```yaml
scrape_configs:
  - job_name: 'olu'
    static_configs:
      - targets: ['localhost:9090']
```

---

## Storage Backends

### JSONFile Storage

Human-readable storage using JSON files.

```bash
export OLU_STORAGE_TYPE=jsonfile
export OLU_BASE_DIR=data
export OLU_SCHEMA_NAME=myapp
```

Directory structure:
```
data/
└── myapp/
    ├── users/
    │   ├── 1.json
    │   ├── 2.json
    │   └── ...
    ├── posts/
    │   └── ...
    └── _schemas/
        └── users.json
```

**Advantages:**
- Human-readable
- Easy debugging
- Git-friendly

**Limitations:**
- Slower at scale
- No ACID guarantees
- No full-text search

### SQLite Storage

Production-ready storage with ACID guarantees, WAL mode, and read/write connection pool split.

```bash
export OLU_STORAGE_TYPE=sqlite
export OLU_DB_PATH=olu.db
export OLU_FULLTEXT_ENABLED=true
```

**Advantages:**
- ACID transactions with WAL mode for concurrent reads
- Separate reader and writer connection pools
- Full-text search support (FTS5)
- Adaptive lock contention monitoring
- Single-file database

#### Read/Write Connection Pool Split

SQLite in WAL mode supports concurrent readers alongside a single writer, but only if they use separate database connections. Olu maintains two connection pools:

**Writer pool** (`OLU_SQLITE_MAX_OPEN_CONNS`, default: 1): Handles all INSERT, UPDATE, DELETE, and transaction operations. Default of 1 matches SQLite's single-writer constraint under WAL. A future PostgreSQL backend would use a higher default.

**Reader pool** (`OLU_SQLITE_READ_POOL_SIZE`, default: NumCPU): Handles all SELECT, COUNT, and search queries. Uses `PRAGMA query_only=ON` to prevent accidental writes. Scales with available CPU cores.

Both pools share identical WAL, synchronous, cache, and busy_timeout pragmas. Pool size defaults are 0, meaning "let the backend decide" — this keeps the configuration backend-neutral for future storage backends.

#### Adaptive Concurrency

Under high write contention, SQLite returns SQLITE_BUSY. Olu's adaptive lock monitors contention rates and automatically backs off when the threshold is exceeded (`OLU_SQLITE_CONTENTION_THRESHOLD`, default 95%). This prevents cascading failures under burst write loads.

**Migration:**

```bash
./bin/olu-migrate --from jsonfile --to sqlite \
  --source-dir ./data/myapp \
  --target-db ./olu.db
```

---

## Graph Features

### Reference Format

Create relationships using REF objects:

```json
{
  "name": "Alice",
  "manager": {
    "type": "REF",
    "entity": "users",
    "id": 42
  },
  "department": {
    "type": "REF",
    "entity": "departments",
    "id": 5
  }
}
```

### Automatic Graph Sync

References are automatically:
- Added to graph on entity creation
- Updated when entity is modified
- Removed when entity is deleted

### Cycle Detection

Configure cycle handling:

```bash
export OLU_GRAPH_CYCLE_DETECTION=warn   # Log warning, allow
export OLU_GRAPH_CYCLE_DETECTION=error  # Reject edge creation
export OLU_GRAPH_CYCLE_DETECTION=ignore # Allow silently
```

### Reference Embedding

Fetch entities with references resolved:

```http
GET /api/v1/users/1
```

Returns:
```json
{
  "id": 1,
  "name": "Alice",
  "manager": {
    "id": 42,
    "name": "Bob",
    "manager": {
      "id": 10,
      "name": "Carol"
    }
  }
}
```

Control embedding:
```http
GET /api/v1/users/1?embed=false
GET /api/v1/users/1?embed_depth=1
```

### Cascading Deletes

When enabled, deleting an entity also deletes entities that reference it:

```bash
export OLU_CASCADING_DELETE=true
```

---

## Testing & Benchmarks

### Running Tests

```bash
make test           # Quick tests
make test-v         # Verbose output
make test-race      # With race detector
make test-full      # Full suite + stress tests
make coverage       # With coverage report
```

### Package Tests

```bash
make test-storage   # Storage tests
make test-sqlite    # SQLite-specific
make test-server    # HTTP server tests
make test-graph     # Graph operations
make test-oql       # OQL parser/executor
make test-sulpher   # Sulpher queries
```

### Benchmarks

```bash
make bench          # All benchmarks
make bench-storage  # Storage benchmarks
make bench-server   # HTTP benchmarks
```

### Stress Tests

```bash
make stress         # 10k record stress test
make stress-race    # With race detector
```

---

## Deployment

### Docker Compose Profiles

Olu ships with a multi-profile `docker-compose.yml` for different scenarios:

```bash
# Basic: memory cache, no auth
docker compose up

# With Redis cache
docker compose --profile redis up

# Full features: Redis, SQLite+FTS, auth, rate limiting, metrics
docker compose --profile full up

# Run integration tests
docker compose --profile test up
```

Or use the Makefile shortcuts:

```bash
make docker-up          # Basic
make docker-up-redis    # With Redis
make docker-up-full     # All features
make docker-test        # Integration tests
make docker-down        # Stop all
make docker-clean       # Stop and remove volumes
```

### Building the Docker Image

```bash
make docker-build
```

This builds `olu:latest` using Go 1.22. No CGO is required — `modernc.org/sqlite` is a pure-Go SQLite port.

### Development Configuration

For local development with minimal setup:

```yaml
services:
  olu:
    build: .
    ports:
      - "9090:9090"
    environment:
      - OLU_STORAGE_TYPE=jsonfile
      - OLU_CACHE_TYPE=memory
      - OLU_AUTH_TYPE=none
      - OLU_GRAPH_MODE=flat
    volumes:
      - ./data:/app/data
      - ./schema:/app/schema
```

### Production Configuration

For production with all security and performance features:

```yaml
services:
  olu:
    image: olu:latest
    ports:
      - "9090:9090"
    environment:
      # Storage
      - OLU_STORAGE_TYPE=sqlite
      - OLU_DB_PATH=/app/data/olu.db
      - OLU_FULLTEXT_ENABLED=true
      # Cache
      - OLU_CACHE_TYPE=redis
      - OLU_CACHE_TTL=300
      - OLU_REDIS_HOST=redis
      - OLU_REDIS_PORT=6379
      # Graph
      - OLU_GRAPH_MODE=flat
      - OLU_GRAPH_CYCLE_DETECTION=error
      # Authentication
      - OLU_AUTH_TYPE=apikey
      - OLU_API_KEYS=${API_KEYS}
      # Rate limiting
      - OLU_RATE_LIMIT_ENABLED=true
      - OLU_RATE_LIMIT_RATE=100
      - OLU_RATE_LIMIT_WINDOW=60
      - OLU_RATE_LIMIT_BY_KEY=true
      # Metrics
      - OLU_METRICS_ENABLED=true
      # Multi-tenancy
      - OLU_TENANT_MODE=strict
      - OLU_TENANT_AUTO_REGISTER=false
    volumes:
      - olu-data:/app/data
      - ./schema:/app/schema
    depends_on:
      - redis
    restart: unless-stopped
    healthcheck:
      test: ["CMD", "wget", "-q", "--spider", "http://localhost:9090/health"]
      interval: 30s
      timeout: 10s
      retries: 3

  redis:
    image: redis:7-alpine
    volumes:
      - redis-data:/data
    command: redis-server --appendonly yes
    restart: unless-stopped

volumes:
  olu-data:
  redis-data:
```

### Testing with Containers

#### Unit Tests with Redis

```bash
# Run Redis cache tests (starts/stops Redis automatically)
make test-redis

# Run Redis stress tests (concurrent access, large payloads)
make test-redis-stress
```

#### Integration Tests

The `make docker-test` target runs a comprehensive integration test suite:

```bash
make docker-test
```

Tests include:
- Health, version, and Prometheus metrics endpoints
- Authentication (API key validation)
- Entity CRUD operations
- Full-text search (SQLite FTS)
- Graph operations
- Multi-tenancy (tenant-scoped routes)
- Rate limiting (verifies 429 responses)

Expected output:
```
========================================
Running Olu Integration Tests
========================================

--- Health & System Endpoints ---
✓ Health check (200)
✓ Version (200)
✓ Metrics (Prometheus) (200)

--- Authentication ---
✓ No auth rejected (401)
✓ Bad API key rejected (401)
✓ Valid API key accepted (200)

--- Entity CRUD ---
✓ Create entity (201)
✓ Get entity (200)
✓ List entities (200)
✓ Update entity (200)
✓ Patch entity (200)
✓ Save entity — create (201)
✓ Save entity — overwrite (200)

--- Full-Text Search (SQLite FTS) ---
✓ FTS search (200)

--- Graph Operations ---
✓ Graph stats (200)

--- Multi-Tenancy ---
✓ Tenant create (201)
✓ Tenant list (200)

--- Cleanup ---
✓ Delete entity (200)

--- Rate Limiting ---
✓ Rate limiting triggered (429)

========================================
Results: 17 passed, 0 failed
========================================
```

### Reverse Proxy (nginx)

```nginx
upstream olu {
    server 127.0.0.1:9090;
}

server {
    listen 443 ssl;
    server_name api.example.com;

    ssl_certificate /etc/ssl/certs/api.crt;
    ssl_certificate_key /etc/ssl/private/api.key;

    location / {
        proxy_pass http://olu;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
    }
}
```

### Health Checks

```bash
# Simple health check
curl -f http://localhost:9090/health

# Version info
curl http://localhost:9090/version

# Prometheus metrics
curl http://localhost:9090/metrics
```

Kubernetes probes:

```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 9090
  initialDelaySeconds: 5
  periodSeconds: 10

readinessProbe:
  httpGet:
    path: /health
    port: 9090
  initialDelaySeconds: 3
  periodSeconds: 5
```

### Backup and Restore

#### SQLite Backup

```bash
# Using sqlite3
sqlite3 /app/data/olu.db ".backup /backup/olu-$(date +%Y%m%d).db"

# Using export endpoint (includes graph data)
curl http://localhost:9090/api/v1/export > backup-$(date +%Y%m%d).zip
```

#### Timeseries Backup

Timeseries backup tooling is **not yet available**. The `iolu` binary
referenced in the timeseries design documents has not been implemented.

If timeseries is enabled, the underlying Pebble data directories can be
backed up manually by copying each tenant's directory while the server is
stopped. Pebble SSTables are immutable, so a filesystem-level copy of a
quiescent directory is consistent. However, copying while the server is
running risks capturing an inconsistent state.

For production use with timeseries enabled, plan for a brief maintenance
window (stop the server, copy the directories, restart) until checkpoint-
based backup tooling is shipped.

See [Timeseries Design](docs/TIMESERIES_DESIGN_V3.md) Section 13 for the
planned backup architecture using Pebble checkpoints.

#### Scheduled Backup (cron)

```bash
0 2 * * * docker exec olu sqlite3 /app/data/olu.db ".backup /backup/olu-daily.db"
```

### Scaling Considerations

**Single instance with SQLite (recommended for most use cases):**
- Read/write split provides concurrent reads with WAL mode
- SQLite handles ~2,100 writes/sec (single-writer) and 16,000+ reads/sec
- Memory cache is sufficient for single instances
- Capacity for 10,000 to 1,900,000 IoT sensors depending on reporting interval

**SQLite capacity estimates (single instance):**

| Reporting interval | Max sensors | Binding constraint |
|--------------------|-------------|-------------------|
| 1 second | ~10,000 | Write throughput (~2,100 w/s) |
| 5 seconds | ~50,000 | Write throughput |
| 30 seconds | ~300,000 | Working set / cache pressure |
| 5 minutes | ~1,000,000 | Database size (~50 GB) |
| 15 minutes | ~1,900,000 | Database size (~100 GB) |

**Multiple instances:**
- Use Redis cache for shared state
- Each instance needs its own SQLite database (sharing a single file across instances is not supported)
- See [Fleet Architecture](docs/FLEET_ARCHITECTURE.md) for multi-instance deployment with tenant placement

---

## Implementation Notes

### Cache Backends

Olu supports two cache backends:

#### Memory Cache (Default)

Simple in-process LRU cache. Good for development and single-instance deployments.

```bash
export OLU_CACHE_TYPE=memory
export OLU_CACHE_TTL=300
```

**Characteristics:**
- Fast (no network overhead)
- Not shared between instances
- Lost on restart
- Uses global TTL (per-item TTL not supported)

#### Redis Cache

Production-grade distributed cache. Use when running multiple instances or when you need per-item TTL control.

```bash
export OLU_CACHE_TYPE=redis
export OLU_CACHE_TTL=300
export OLU_REDIS_HOST=localhost
export OLU_REDIS_PORT=6379
```

**Characteristics:**
- Shared across all olu instances
- Survives restarts (if Redis persistence enabled)
- Supports per-item TTL
- Network latency on every operation
- Requires Redis infrastructure

#### When to Use Redis

- Running multiple olu instances behind a load balancer
- Need cache to survive restarts
- Need per-item TTL control
- Want to inspect cache contents via redis-cli

For single-instance deployments or development, the memory cache is simpler and faster.

### OQL Entity Discovery

OQL validates that entity types exist before executing queries. Entity discovery happens automatically:

1. On startup, OQL scans the schema directory for entity folders
2. When a query references an unknown entity, OQL automatically rescans the directory
3. If the entity still doesn't exist, the query fails with "entity does not exist"

This means **newly created entity types are recognised automatically** without server restart or manual refresh. The first query against a new entity type may incur a small overhead for the rescan.

### Multi-Tenancy

Olu supports two operational modes for tenant isolation.

#### Operational Modes

| Mode | CRUD | REFs | OQL | FTS | Graph | Non-tenant routes |
|------|------|------|-----|-----|-------|-------------------|
| `path` (single-tenant) | \u2713 | \u2713 | \u2713 | \u2713 | \u2713 | Available (default store) |
| `strict` (multi-tenant) | \u2713 | \u2713 | \u2713 | \u2713 | \u2713 | Blocked |

In strict mode, graph queries are available exclusively via tenant-scoped routes
(`/api/v1/tenant/{tenant_id}/graph/...`). The graph layer is fully tenant-isolated:

- Node IDs in requests and responses use the client-facing `entity:id` format; the internal `XXXX@entity:id` prefix is added and stripped transparently
- Graph traversal operates on a per-request snapshot that contains only the requesting tenant's nodes and edges; cross-tenant edges are detected and excluded with a WARN log
- All 12 handler surfaces (stats, nodeInfo, nodeDegree, in/out edges, path, neighbors, shortestPath, pathExists, commonNeighbors, nodeSearch, Sulpher sync and async) are covered by an adversarial isolation test suite
- Non-tenant graph routes (`/api/v1/graph/...`) are blocked in strict mode along with all other non-tenant routes

Configure via environment:
```bash
export OLU_TENANT_MODE=strict
export OLU_TENANT_AUTO_REGISTER=false
```

#### Tenant Scoping Architecture

Tenant isolation is enforced at the **storage layer**. Each tenant gets a scoped `Store` instance that filters all operations by `tenant_id`. This means:

- Every CRUD operation (Create, Get, List, Update, Patch, Delete) is scoped to the tenant's store
- OQL queries (both sync and async) execute against the tenant-scoped store
- OQL SQL push-down includes `AND tenant_id = ?` in generated SQL
- Async OQL jobs capture the tenant-scoped store at submission time, so background goroutines execute in the correct scope
- Full-text search queries include tenant_id filtering
- REF resolution only resolves references within the same tenant

#### Auto-Registration

In `path` mode, the `OLU_TENANT_AUTO_REGISTER` flag controls whether unknown tenant names in the URL automatically create new tenants:

| `OLU_TENANT_AUTO_REGISTER` | Behaviour |
|-----------------------------|-----------|
| `true` | `/api/v1/tenant/new-name/...` creates tenant "new-name" on first access |
| `false` (default) | Unknown tenants return 404 |

In `strict` mode, auto-registration is ignored; tenants must be pre-registered.

#### Security Model

This is **application-level isolation** designed for trusted environments (internal services, not adversarial internet clients). The isolation model prevents accidental cross-tenant data access in normal CRUD, OQL, and search flows.

It is not a compliance-grade security boundary. For hostile multi-tenancy, use separate Olu instances per tenant with separate databases.

#### Example Usage

```bash
# Create entity in tenant "acme"
curl -X POST http://localhost:9090/api/v1/tenant/acme/users \
  -H "Content-Type: application/json" \
  -d '{"name": "Alice"}'

# List only acme's users
curl http://localhost:9090/api/v1/tenant/acme/users

# OQL scoped to tenant
curl -X POST http://localhost:9090/api/v1/tenant/acme/oql/query \
  -d '{"query": "SELECT * FROM users WHERE status = '"'"'active'"'"'"}'

# In strict mode, non-tenant routes return 403:
curl http://localhost:9090/api/v1/users
# {"error": "Tenant context required. Use /api/v1/tenant/{tenant_id}/... routes"}
```

---

## Versioning and Compatibility

### Version Scheme

Olu follows semantic versioning: `MAJOR.MINOR.PATCH`. During the `0.x`
series, minor versions may include breaking changes to the database format
or API. The current version is `0.9.7-patched73`.

### Database Format Stability

**Within `0.9.x`:** The SQLite schema is stable. Patch releases (`0.9.1`,
`0.9.2`, etc.) will not require migrations. If a schema change is needed,
it will be shipped as part of `0.10.0` or later with an explicit migration.

**Across minor versions (`0.9` → `0.10`):** Schema changes are possible.
When they occur, `olu-migrate` will be updated with the necessary migration
subcommand. Release notes will state whether a migration is required.

### When Migrations Are Required

Migrations are required when:

- A new column is added to the `entities` or `tenants` table.
- A new system table is created (e.g., `entity_sequences` was added in
  the v1 → v2 schema migration).
- An index is changed or added.

Migrations are **not** required for:

- New API endpoints.
- Configuration changes.
- Bug fixes that don't alter stored data.

The `olu-migrate schema` command is idempotent: running it against an
already-migrated database is safe and produces no changes.

### Rollback

Rollback is **not supported**. Migrations are forward-only. Before
upgrading, take a backup:

```bash
sqlite3 /app/data/olu.db ".backup /backup/olu-pre-upgrade.db"
```

If the upgrade fails, restore from the backup and stay on the previous
version.

### Safe Upgrade Steps

1. **Back up** the SQLite database and any Pebble timeseries directories.
2. **Stop** the running server.
3. **Replace** the `olu` binary with the new version.
4. **Run migrations** if the release notes require it:
   `olu-migrate schema -db /path/to/olu.db`
5. **Start** the server.
6. **Verify** via `/health` and `/ready`.

For zero-downtime upgrades behind a load balancer, run step 4 before
starting the new instance. The migrated database is backwards-compatible
within the same minor version, so the old binary can still read it if
you need to roll back the binary without rolling back the database.

### API Compatibility

REST API endpoints are stable within a minor version. New endpoints may
be added in patch releases but existing endpoints will not change their
request/response format. Deprecations will be announced at least one
minor version before removal.

## Troubleshooting

For a quick-reference operational guide covering health checks, common
failure modes, and emergency procedures, see [docs/RUNBOOK.md](docs/RUNBOOK.md).

### Common Issues

**"Database is locked"**
- SQLite concurrent write issue
- Solution: Ensure single writer or use WAL mode

**"Entity not found" after creation**
- Cache may be stale
- Solution: Check cache TTL or disable caching for debugging

**Graph queries return empty**
- Graph may not be initialized
- Check: `OLU_GRAPH_MODE=flat`

**Rate limiting too aggressive**
- Adjust `OLU_RATE_LIMIT_RATE` and `OLU_RATE_LIMIT_WINDOW`
- Consider `OLU_RATE_LIMIT_BY_KEY=true` for authenticated clients

### Debug Mode

```bash
export OLU_DEBUG=true
```

Enables verbose logging including:
- Request/response details
- Query execution
- Graph operations

---

## License

Apache 2.0 - See [LICENSE](LICENSE)
