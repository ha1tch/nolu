# olu Fleet Architecture

**Version:** 0.2.2-draft
**Author:** haitch <h@ual.fi>
**Date:** February 2026
**Status:** Design proposal (post-review revision)

---

**Revision history:**

- **0.2.2** — Incorporated second reviewer feedback: gateway self-registration
  via instances table with heartbeat-based discovery for elastic environments;
  migration tombstone/cool-down period on source instance after cutover to
  prevent stale-routing 404s; Tenant 0 explicitly scoped as local-to-instance
  and disabled in fleet deployments; internal API header trust boundary
  requiring mTLS/token authentication before honouring `X-Placement-Epoch`;
  noisy-neighbour acknowledgement with dedicated-instance mitigation; warm
  migration via Litestream streaming added to future considerations.
- **0.2.1** — Tightened three specification nits from second review: explicit
  Retry-After computation method (conservative fixed estimate from data size);
  clarified epoch check as per-request comparison against in-memory cache with
  background refresh; added `X-Placement-Epoch` gateway header for zero-latency
  split-brain detection on the proxied path. Repositioned gateway description
  to acknowledge its control-plane edge role.
- **0.2.0** — Incorporated review feedback: added `migrating` tenant lifecycle
  state with explicit SLA; placement epochs for split-brain prevention;
  advisory locking for concurrent admin operations; gateway cache invalidation
  for urgent state transitions; per-tenant durability setting (async/confirmed
  RPO contract); expanded archive format trade-offs with SQLite ATTACH escape
  hatch.
- **0.1.0** — Initial draft.

---

## 1. Design Philosophy

olu is a single-binary, SQLite-backed document store. Its operational strength
is simplicity: one process, one file, no external dependencies. The fleet
architecture preserves this property at the instance level while adding
coordination at the edges. Each olu instance remains sovereign over its data.
No instance shares a SQLite file with another. No instance participates in
consensus or distributed transactions.

Coordination is handled by two lightweight components: a tenant registry
(a small SQLite database) and a gateway (a thin reverse proxy). Neither
component adds runtime complexity to the hot path of serving tenant requests.

The guiding constraints are:

- **No distributed state.** Each hot instance owns its tenants' data completely.
  A tenant lives on exactly one hot instance at a time.
- **Privilege separation.** The main olu binary can create tenants but never
  remove, disable, or archive them. Destructive operations exist only in the
  admin binary, which is compiled separately and runs on demand.
- **Failure independence.** The failure of any single component (hot instance,
  admin, gateway, registry) does not cascade. Degraded operation is always
  preferable to coordinated failure.
- **Operational transparency.** Every state change (tenant creation, placement
  migration, data archival) is recorded in the registry with enough detail to
  reconstruct the history of any tenant.

---

## 2. Tenancy Model

### 2.1 Tenant Modes

olu supports two tenant modes, configured via `TenantMode` in the instance
configuration:

| Mode | Non-tenant routes | Tenant routes | Registration |
|------|-------------------|---------------|-------------|
| `path` | Tenant 0 (unscoped) | Auto-register on first access | Implicit |
| `strict` | 403 Forbidden | Pre-registered only | Explicit |

The value `"none"` is accepted as a deprecated alias for `"path"` to avoid
breaking existing configuration files. New deployments should use `"path"`.

In all modes, tenant isolation is absolute at the storage layer. Tenant A
cannot read, write, or delete tenant B's data. Entity IDs are scoped per
tenant — both tenants may independently have an entity with `id=1`.

Tenant 0 is the unscoped store used by non-tenant routes (`/api/v1/{entity}`).
It is its own isolated scope, not a privileged cross-tenant view. There is no
API path in the main olu binary that aggregates data across tenants.

**Tenant 0 in fleet deployments.** In a multi-instance fleet, Tenant 0 is local
to each hot instance — it is not replicated, routed through the gateway, or
given any cross-instance meaning. Each hot instance has its own independent
Tenant 0 store, which exists as a side effect of having non-tenant routes in
the codebase. In `strict` mode (recommended for production fleets), non-tenant
routes return 403, making Tenant 0 effectively disabled. For this reason, fleet
deployments should not rely on Tenant 0 for any purpose. It is a development
convenience, not a fleet-level concept.

### 2.2 Recommended Production Mode

Production deployments should use `strict` mode. Tenants are provisioned
through the admin binary before the instance accepts traffic. This ensures:

- No accidental tenant creation from typos in client configuration.
- The registry is the single source of truth for which tenants exist.
- Tenant IDs are assigned deliberately, not sequentially by arrival order.

Development and testing environments may use `path` mode for convenience.

### 2.3 Tenant Lifecycle

A tenant progresses through the following states:

```
provisioned ──> active ──> draining ──> frozen ──> deleted
                  │  │                    │
                  │  └──> migrating ──────┤
                  │       (to new         │
                  │        instance)      │
                  │                       │
                  └──── active ◄──────────┘
                       (thaw)
```

| State | Hot data | New writes | Reads | Registry status |
|-------|----------|-----------|-------|-----------------|
| `provisioned` | Empty | Blocked (instance not yet routing) | Blocked | `provisioned` |
| `active` | Present | Allowed | Allowed | `active` |
| `migrating` | Present on source; copying to destination | Reads allowed; writes rejected (503) | Allowed (from source) | `migrating` |
| `draining` | Present, being archived | Allowed | Allowed | `draining` |
| `frozen` | Archived only | Rejected (404) | Via admin on-demand | `frozen` |
| `deleted` | Purged | Rejected (404) | Rejected (404) | `deleted` |

The `migrating` state is entered when a tenant is being moved between hot
instances. During migration, the gateway continues routing read requests to the
source instance. Write requests receive a `503 Service Unavailable` response
with a `Retry-After` header. The header value is a conservative fixed estimate
computed by the admin binary before migration begins: it queries the source
instance for the tenant's data size (including timeseries storage if
provisioned), applies a transfer rate appropriate to the
deployment's network topology (defaulting to 100 MB/s for same-region, 20 MB/s
for cross-region), and rounds up by 50%. This estimate is stored in the
tenant's `metadata` JSON field when the status moves to `migrating`, and the
gateway reads it from there.

The estimate is not updated mid-flight. If the migration runs longer than
predicted, the client's Retry-After window expires and the next write attempt
receives a fresh 503 with the same estimate. This continues until the migration
completes. Over-engineering dynamic estimates would add complexity for marginal
benefit — the client simply retries, and the conservative rounding ensures most
migrations complete within the first Retry-After window. This
creates a brief write-unavailability window (typically seconds to a few minutes
depending on data volume) while preserving read availability throughout.

The migration sequence is:

1. Admin sets tenant status to `migrating`; gateway immediately notified
   (see Section 3.6).
2. Admin exports data from source instance to destination instance.
   If the tenant has timeseries storage provisioned, this includes
   a Pebble checkpoint of the timeseries data directory (see
   TIMESERIES_DESIGN_V3.md Section 14.1).
3. Admin verifies row counts and checksums.
4. Admin creates new placement (destination, `primary`) with incremented
   epoch, closes old placement.
5. Admin sets tenant status back to `active`.
6. Gateway resumes routing all traffic (reads and writes) to the destination.
7. **Cool-down period.** The source instance retains the tenant's data in a
   read-only tombstone state for a configurable period (default: 60 seconds,
   minimum: 2x the gateway poll interval). During this window, any stray
   requests that reach the source (from a gateway with a stale cache) receive
   `301 Moved Permanently` pointing to the destination's tenant endpoint. This
   handles the race where the registry has been updated but a gateway has not
   yet refreshed.
8. After the cool-down expires, admin deletes tenant data from the source
   instance.

If migration fails at any step, the admin binary sets the tenant back to
`active` on the source instance. The gateway resumes normal routing. No data
is lost because the source is not modified until step 8, which occurs only
after the destination is verified, active, and the cool-down has elapsed.

The `draining` state allows a grace period where the hot instance continues
serving the tenant while the admin binary exports historical data to archive
storage. The tenant remains fully functional during this phase.

Thawing a frozen tenant restores it to `active` by loading archived data back
onto a hot instance. This is an admin operation that may take minutes to hours
depending on the volume of archived data.

---

## 3. Registry

### 3.1 Purpose

The registry is a small SQLite database (`registry.db`) that serves as the
single source of truth for:

- Which tenants exist and their current state.
- Which hot instance each tenant is placed on.
- Retention policies governing data lifecycle.
- Instance inventory and health metadata.
- An audit trail of administrative actions.

The registry does not store entity data. It is a coordination database,
typically a few kilobytes to a few megabytes in size.

### 3.2 Schema

```sql
-- Instance inventory
CREATE TABLE instances (
    id              INTEGER PRIMARY KEY,
    name            TEXT NOT NULL UNIQUE,    -- "us-east-hot-1"
    role            TEXT NOT NULL,           -- "hot", "admin", "gateway"
    endpoint        TEXT NOT NULL,           -- "https://10.0.1.15:8443"
    internal_addr   TEXT,                    -- "10.0.1.15:8443" (for admin)
    region          TEXT,
    status          TEXT NOT NULL DEFAULT 'active',
                    -- "active", "standby", "draining", "offline"
    capacity_limit  INTEGER,                -- max tenants (advisory)
    created_at      DATETIME DEFAULT (datetime('now')),
    last_heartbeat  DATETIME,

    CHECK (role IN ('hot', 'admin', 'gateway')),
    CHECK (status IN ('active', 'standby', 'draining', 'offline'))
);

-- Tenant directory
CREATE TABLE tenants (
    id              INTEGER PRIMARY KEY,    -- numeric tenant ID
    name            TEXT NOT NULL UNIQUE,    -- human-readable name
    status          TEXT NOT NULL DEFAULT 'provisioned',
                    -- "provisioned", "active", "migrating", "draining",
                    -- "frozen", "deleted"
    locked_by       TEXT,                   -- admin operation holding the lock
                    -- e.g. "migrate:hot-A->hot-B", "freeze", "archive"
    locked_at       DATETIME,               -- when the lock was acquired
    created_at      DATETIME DEFAULT (datetime('now')),
    activated_at    DATETIME,
    frozen_at       DATETIME,
    deleted_at      DATETIME,
    metadata        TEXT,                   -- JSON blob for custom fields

    CHECK (status IN ('provisioned','active','migrating','draining',
                      'frozen','deleted'))
);

-- Tenant-to-instance placement
CREATE TABLE placements (
    tenant_id       INTEGER NOT NULL REFERENCES tenants(id),
    instance_id     INTEGER NOT NULL REFERENCES instances(id),
    role            TEXT NOT NULL DEFAULT 'primary',
                    -- "primary", "replica", "archive"
    epoch           INTEGER NOT NULL DEFAULT 1,
                    -- Monotonically increasing per tenant. Each new primary
                    -- placement increments the epoch. Hot instances compare
                    -- their cached epoch against the registry; a stale epoch
                    -- means "stop serving this tenant". Prevents split-brain
                    -- after instance recovery or migration.
    since           DATETIME DEFAULT (datetime('now')),
    until           DATETIME,               -- NULL = current

    PRIMARY KEY (tenant_id, instance_id, since)
);
CREATE INDEX idx_placements_active
    ON placements(tenant_id) WHERE until IS NULL;

-- Retention policies
CREATE TABLE retention_policies (
    id              INTEGER PRIMARY KEY,
    tenant_id       INTEGER REFERENCES tenants(id),
                    -- NULL = default policy for all tenants
    entity_type     TEXT,
                    -- NULL = applies to all entity types
    hot_days        INTEGER NOT NULL DEFAULT 30,
    durability      TEXT NOT NULL DEFAULT 'async',
                    -- "async"     : Litestream replicates asynchronously.
                    --               RPO: seconds. No write latency penalty.
                    -- "confirmed" : Write returns 200 only after Litestream
                    --               confirms the WAL frame is replicated.
                    --               RPO: zero. Adds a round-trip to object
                    --               storage on every write (~50-200ms).
    archive_format  TEXT NOT NULL DEFAULT 'parquet',
                    -- "parquet" : Columnar, compact, efficient for analytics.
                    --             Not directly queryable by olu. Best when
                    --             historical queries are rare.
                    -- "sqlite"  : Directly attachable by the admin binary via
                    --             ATTACH DATABASE. Full OQL query capability
                    --             over archived data. Larger files, but best
                    --             when historical queries are frequent.
                    -- "jsonl"   : Line-delimited JSON. Portable, human-readable,
                    --             processable by standard unix tools. Largest
                    --             files; no structured query without loading.
    archive_path    TEXT,                   -- base path in object storage
    compress        TEXT DEFAULT 'zstd',
    created_at      DATETIME DEFAULT (datetime('now')),

    UNIQUE (tenant_id, entity_type)
);

-- Administrative audit log
CREATE TABLE audit_log (
    id              INTEGER PRIMARY KEY,
    timestamp       DATETIME DEFAULT (datetime('now')),
    actor           TEXT NOT NULL,           -- "admin-cli", "cleanup-job", etc.
    action          TEXT NOT NULL,           -- "tenant.create", "tenant.freeze", etc.
    target_type     TEXT,                    -- "tenant", "instance", "policy"
    target_id       INTEGER,
    detail          TEXT,                    -- JSON blob with action-specific data
    outcome         TEXT NOT NULL DEFAULT 'success'
                    -- "success", "failed", "partial"
);
CREATE INDEX idx_audit_by_target
    ON audit_log(target_type, target_id, timestamp);
```

### 3.3 Access Model

| Process | tenants | placements | policies | audit_log | instances |
|---------|---------|-----------|----------|-----------|-----------|
| Main olu (strict) | Read | Read | Read | — | — |
| Main olu (path) | Read + insert | Read | Read | — | — |
| Admin binary | Full CRUD | Full CRUD | Full CRUD | Insert | Full CRUD |
| Cleanup job | Read | Read | Read | Insert | — |
| Gateway | Read | Read | — | — | Read |

The main olu binary can create tenant rows (in `path` mode) but never
update their status, delete them, or modify placements. This asymmetry is
deliberate: creation is low-risk and should be frictionless; state transitions
and destruction are high-risk and go through the admin path.

### 3.4 Replication

The registry is replicated using Litestream to object storage (S3, MinIO, or
local filesystem). This provides:

- Point-in-time recovery after corruption or accidental deletion.
- Read-only copies at each hot instance and gateway for local cache.
- Cross-region availability without a distributed database.

Write path: admin binary → `registry.db` → Litestream → object storage.
Read path: gateway / hot instance ← Litestream ← object storage (streamed).

The gateway and hot instances refresh their local read-only copy on a
configurable interval (default: 10 seconds). A stale registry means a newly
provisioned tenant may take up to 10 seconds to become routable — an
acceptable trade-off for the simplicity of avoiding a coordination protocol.

### 3.5 Credential Management

The registry stores instance endpoints but never stores credentials (API keys,
TLS client certificates, tokens). Credentials are resolved at runtime from the
deployment's secrets infrastructure:

| Deployment | Secrets source |
|-----------|---------------|
| Cloud (AWS/GCP) | Secrets Manager / Secret Manager |
| Kubernetes | Mounted secrets, service account tokens |
| On-premises | Environment variables, mounted volumes |
| Air-gapped | Envelope-encrypted blob (key provided at startup) |

The registry maps instance names to endpoints. The secrets store maps instance
names to credentials. The admin binary joins them at runtime.

### 3.6 Gateway Cache Invalidation

The gateway polls the registry on a configurable interval (default: 10
seconds). This is acceptable for routine operations — a newly provisioned
tenant may take up to 10 seconds to become routable.

However, certain state transitions have correctness implications if the
gateway continues routing based on stale data:

| Transition | Risk of stale routing |
|------------|----------------------|
| Tenant frozen | Writes reach a frozen tenant and are silently lost |
| Tenant migrating | Writes reach the source instance after cutover |
| Instance draining | Traffic continues to an instance that is shutting down |

For these transitions, the admin binary sends an immediate cache invalidation
to all known gateways after updating the registry.

**Gateway discovery.** The admin binary discovers gateways by querying the
`instances` table for rows with `role = 'gateway'` and `status = 'active'`.
Gateways register themselves on startup by inserting (or updating) a row in
the `instances` table, and maintain their presence via periodic heartbeats
(updating `last_heartbeat` on the same interval as the registry poll). The
admin binary ignores gateways whose heartbeat is older than 3x the poll
interval (presumed dead). On graceful shutdown, the gateway sets its own
status to `'offline'`.

In elastic environments (Kubernetes, auto-scaling groups), this ensures that
newly launched gateways are immediately discoverable for cache invalidation,
and terminated gateways are automatically pruned. A gateway that starts up
between an admin state change and the next poll cycle will self-correct via
its normal poll — it never reads stale data from before it existed.

The gateway exposes a
lightweight internal endpoint for this purpose:

```
POST /internal/cache/invalidate
X-Admin-Token: <shared-secret>
Content-Type: application/json

{"tenants": ["acme"], "reason": "migration-started"}
```

On receiving this notification, the gateway immediately reloads the affected
tenant's placement from its local registry copy (which Litestream has already
updated, or will update within seconds). If the registry copy is not yet
current, the gateway marks the tenant as `uncertain` and returns `503
Retry-After: 5` until the next successful registry refresh confirms the
new state.

The poll interval remains as a fallback. If the push notification fails
(gateway temporarily unreachable, network partition), the gateway will
self-correct on the next poll cycle. The push notification reduces the
consistency window from seconds to milliseconds; it does not introduce a
new failure mode.

**Consistency contract:** Under normal operation, routing converges with
the registry within 500ms of a state change (push notification latency).
Under degraded operation (push fails), routing converges within the poll
interval (default: 10 seconds). No data is corrupted during the consistency
window — the worst case is a rejected request (503) or a request served by
an instance that is about to stop serving the tenant.

### 3.7 Placement Epochs

Each primary placement carries a monotonically increasing `epoch` integer.
When a tenant is migrated to a new instance, the new placement's epoch is
incremented.

Epoch validation operates at two levels:

**Per-request (zero cost).** Each hot instance maintains an in-memory map of
tenant ID to cached epoch. On every incoming request, the instance compares the
request's tenant against this cached value — a single integer comparison with
no I/O. If the cached epoch is stale (see below), the instance returns
`410 Gone` immediately.

**Background refresh.** The cached epoch map is refreshed from the local
registry copy on the same poll interval as the gateway (default: 10 seconds).
This is the only I/O involved in epoch checking. Between refreshes, the
instance uses the cached values. The worst case is that an instance continues
serving a tenant for up to one poll interval after its epoch becomes stale in
the registry.

**Gateway-assisted immediate detection.** To close even the poll-interval
window for gateway-routed traffic, the gateway includes the current epoch in
a header on every proxied request:

```
X-Placement-Epoch: 7
```

The hot instance compares this header value against its cached epoch. If the
header carries a higher epoch, the instance knows immediately — without waiting
for the next background refresh — that its placement is stale. It returns
`410 Gone` and marks the tenant for eviction from its local cache.

This header-based check provides zero-latency split-brain detection for all
traffic that flows through the gateway. The background refresh remains as a
fallback for any traffic that bypasses the gateway (direct access during
debugging, misconfigured clients). Together, the two mechanisms provide
defence in depth: the gateway header catches staleness instantly, and the
background refresh catches it within seconds even without the header.

### 3.8 Advisory Locking for Multi-Step Operations

The tenants table includes `locked_by` and `locked_at` columns. Before
beginning a multi-step operation (migration, freeze, archive), the admin
binary acquires an advisory lock:

```sql
UPDATE tenants
SET locked_by = 'migrate:hot-A->hot-B', locked_at = datetime('now')
WHERE id = ? AND locked_by IS NULL;
```

If the update affects zero rows, another operation is already in progress.
The admin binary reports this and refuses to proceed, preventing semantic
conflicts such as simultaneous freeze and migration.

On completion (success or failure), the admin binary clears the lock:

```sql
UPDATE tenants SET locked_by = NULL, locked_at = NULL WHERE id = ?;
```

**Stale lock recovery:** If the admin binary crashes mid-operation, the lock
remains set. A configurable staleness timeout (default: 30 minutes) allows
a subsequent admin invocation to forcibly break the lock:

```
iolu tenant unlock --name acme --force
```

This logs a warning to the audit trail and clears the lock. The operator
must then inspect the tenant's state and decide whether to resume, retry,
or roll back the interrupted operation.

Concurrent admin invocations targeting *different* tenants are unaffected —
the lock is per-tenant, not global.

---

## 4. Fleet Topology

### 4.1 Components

```
                         ┌──────────────────┐
                         │    Internet /     │
                         │   Client Network  │
                         └────────┬─────────┘
                                  │
                         ┌────────┴─────────┐
                         │     Gateway       │
                         │  (routing + TLS)  │
                         └────────┬─────────┘
                                  │
                ┌─────────────────┼─────────────────┐
                │                 │                  │
         ┌──────┴──────┐  ┌──────┴──────┐  ┌───────┴───────┐
         │  olu hot A  │  │  olu hot B  │  │   olu admin   │
         │  (region 1) │  │  (region 2) │  │  (on demand)  │
         └──────┬──────┘  └──────┬──────┘  └───────┬───────┘
                │                │                  │
                │                │          ┌───────┴───────┐
                │                │          │  registry.db  │
                │                │          │  (Litestream) │
                │                │          └───────────────┘
                │                │
         ┌──────┴──────┐  ┌─────┴───────┐
         │   archive   │  │   archive   │
         │  (object    │  │  (object    │
         │   storage)  │  │   storage)  │
         └─────────────┘  └─────────────┘
```

**Gateway.** A reverse proxy (nginx, Caddy, or a purpose-built Go binary)
that terminates TLS, authenticates requests, and routes them to the correct hot
instance based on the tenant-to-instance mapping from the registry. The gateway
also attaches the current placement epoch to each proxied request via the
`X-Placement-Epoch` header, enabling hot instances to detect stale placements
immediately (see Section 3.7). It receives cache invalidation notifications
from the admin binary for urgent state transitions (see Section 3.6).

While the gateway is stateless (no persistent state of its own — only a cached
read-only copy of the registry), it functions as a lightweight control-plane
edge: it embodies fleet topology, enforces authentication, and propagates
placement metadata. Its failure mode is clean — traffic stops, no data is
corrupted — and it scales horizontally behind a load balancer. But it is not
merely a passthrough; operators should treat it as a critical routing component
with corresponding monitoring and redundancy.

For high availability, deploy two gateway instances behind a load balancer.

**Hot instances.** Standard olu binaries running in `strict` mode. Each hot
instance serves a subset of tenants. A tenant is assigned to exactly one hot
instance at a time (its "primary placement"). Hot instances have no knowledge
of each other.

**Admin instance.** A separate binary compiled from the same repository with
the `admin` build tag. It runs on demand — started when administrative
operations are needed, shut down when idle. It is the only process with write
access to the registry and the only process that can initiate cross-instance
operations (archival, tenant migration, fleet-wide queries).

**Archive storage.** Object storage (S3, MinIO, local filesystem) holding
frozen tenant data in compressed, partitioned files. Archive storage is
write-once, read-rarely. It is not an olu instance — it's a durable blob
store.

### 4.2 Request Flow

**Normal tenant request:**

1. Client sends `POST /api/v1/tenant/acme/assets` to the gateway.
2. Gateway authenticates the request (API key, JWT, mTLS).
3. Gateway looks up "acme" in its cached registry copy → instance `hot-A`.
4. Gateway proxies the request to `hot-A`'s internal endpoint.
5. `hot-A` processes the request against its local SQLite database.
6. Response flows back through the gateway to the client.

**New tenant (path mode):**

1. Client sends a request for an unknown tenant.
2. Gateway has no mapping → proxies to the designated auto-provision instance.
3. The hot instance's `GetOrRegister` creates the tenant in its local
   in-memory registry and begins serving.
4. The hot instance writes the new tenant to `registry.db` (insert only).
5. On next refresh cycle, all gateways and instances learn about the new tenant.

**New tenant (strict mode):**

1. Operator runs `iolu tenant create --name acme --instance hot-A`.
2. Admin binary inserts tenant row, creates placement, writes audit log.
3. On next refresh cycle, the gateway routes "acme" to `hot-A`.
4. `hot-A` loads the updated registry and begins accepting requests for "acme".

### 4.3 Scaling Model

The fleet scales by tenants, not by request volume. Each hot instance can
sustain approximately 30,000 write ops/sec and 270,000 read ops/sec on
commodity hardware (tested on Apple M-series; ARM servers show similar
characteristics). A single hot instance comfortably handles:

- 50,000+ environmental sensors reporting every 5 minutes
- 20,000 tracked fleet assets with 30-second intervals
- 500,000 smart meters reporting every 15 minutes

Most deployments run two hot instances purely for redundancy, not capacity.
Add a third instance when you need geographic distribution or when a single
SQLite file approaches the practical size limit (~50 GB).

**Resource isolation.** While tenant data is fully isolated at the storage
layer, CPU and I/O are shared across all tenants on a hot instance. An
intensive aggregation query from one tenant can degrade latency for other
tenants on the same instance. For high-value tenants with strict latency
requirements, the recommended mitigation is dedicated instance placement:
assign the tenant as the sole occupant of a hot instance. This is an
operational decision managed through the placement table, not an architectural
change.

Tenant migration between instances is an admin operation managed through the
`migrating` lifecycle state (see Section 2.3). During migration, reads remain
available from the source instance; writes receive `503 Retry-After`. The
write-unavailability window depends on data volume:

| Tenant data size | Expected write downtime |
|-----------------|------------------------|
| < 100 MB | < 30 seconds |
| 100 MB - 1 GB | 1 - 5 minutes |
| 1 GB - 10 GB | 5 - 30 minutes |
| > 10 GB | Schedule during maintenance window |

These estimates assume a network-local transfer between instances in the same
region. Cross-region migrations are slower and should always be scheduled.

---

## 5. Admin Binary

### 5.1 Build Separation

The admin binary is compiled with the `admin` build tag:

```makefile
build:
    go build -o olu ./cmd/olu

build-iolu:
    go build -tags admin -o iolu ./cmd/iolu
```

Code guarded by `//go:build admin` includes:

- Cross-tenant query execution (iterates over tenants, aggregates results).
- Tenant lifecycle management (disable, freeze, delete).
- Data archival and restoration.
- Instance management (register, decommission, health monitoring).
- Registry write operations.

The main olu binary does not contain this code. The attack surface for
cross-tenant data access does not exist if the binary isn't compiled.

### 5.2 Operations

**Tenant management:**

```
iolu tenant create   --name acme --instance hot-A
iolu tenant disable  --name acme --reason "billing"
iolu tenant freeze   --name acme
iolu tenant thaw     --name acme --instance hot-B
iolu tenant delete   --name acme --confirm
iolu tenant list     [--status active] [--instance hot-A]
iolu tenant inspect  --name acme
```

**Data lifecycle:**

```
iolu archive run     [--tenant acme] [--entity events] [--before 2026-01-01]
iolu archive status  --tenant acme
iolu archive restore --tenant acme --month 2026-01 --instance hot-A
```

**Fleet operations:**

```
iolu instance register  --name hot-C --endpoint https://...
iolu instance drain     --name hot-A
iolu instance status
iolu migrate tenant     --name acme --from hot-A --to hot-B
```

**Reporting:**

```
iolu report usage       [--tenant acme]
iolu report storage     [--instance hot-A]
iolu report audit       [--since 2026-02-01] [--action tenant.*]
```

### 5.3 Runtime Model

The admin binary is not a long-running service. It starts, performs the
requested operation, and exits. For scheduled operations (nightly archival),
a cron job or systemd timer invokes it.

For interactive administration, the admin binary can optionally start a
short-lived HTTP server (`iolu serve --port 9090 --timeout 30m`) that
exposes the same operations as a REST API. This server binds to localhost
or a unix socket — never to a public interface. It shuts down after the
configured idle timeout.

If socket activation is available (systemd), the admin server can be
configured to start on first connection and stop after idle timeout,
achieving zero resource usage when not needed.

---

## 6. Data Lifecycle

### 6.1 Hot Storage

Hot storage is the SQLite database on each hot instance. It holds recent data
that is actively queried and written to. The retention period is governed by
the retention policy for each tenant and entity type.

For tenants with timeseries storage provisioned, hot storage also includes a
per-tenant Pebble data directory under `{OLU_BASE_DIR}/ts/t{XXXX}/`. The
timeseries store supports per-timeline retention policies with a store-level
default (default 90 days), independent of entity retention.
See TIMESERIES_DESIGN_V3.md for details.

Default entity retention: 30 days. Default timeseries retention: 90 days.
Both configurable per tenant.

### 6.2 Archive Process

The archive process runs as a scheduled admin operation (typically nightly):

1. **Select candidates.** Query the registry for active retention policies.
   For each policy, identify rows in the hot instance older than
   `hot_days`.

2. **Export.** Connect to the hot instance (read-only). Extract qualifying
   rows in batches of 10,000. Write to the configured archive format
   (Parquet, compressed SQLite, or JSON lines) partitioned by:
   ```
   {archive_path}/{tenant_name}/{entity_type}/{year}-{month}.{format}.{compress}
   ```

3. **Verify.** Checksum the archive file. Read back a sample and compare
   with the source. Record the archive manifest in the registry.

4. **Delete from hot.** Delete the archived rows from the hot instance in
   batches of 500-1,000 with short pauses between batches (to avoid sustained
   write contention triggering the adaptive lock). Run `PRAGMA
   incremental_vacuum` after each batch cycle.

5. **Audit.** Record the operation in the audit log: tenant, entity type,
   date range, row count, archive location, checksum.

If any step fails, the process stops and logs the failure. The next run
resumes from where it left off (idempotent by design — the archive file
is written before deletion begins, and deletion only removes rows that
exist in a verified archive).

### 6.3 Archive Storage Layout

```
s3://olu-archive/
  acme/
    assets/
      2025-12.parquet.zst
      2026-01.parquet.zst
    events/
      2025-12.parquet.zst
      2026-01.parquet.zst
    _manifest.json            ← per-tenant manifest
  globex/
    sensors/
      2026-01.parquet.zst
    _manifest.json
```

The manifest records each archive file's date range, row count, checksum, and
the hot instance it was exported from. This enables verification and
restoration without scanning the archive files themselves.

### 6.4 Querying Archived Data

Archived data is not directly queryable through the normal olu API. The
query path depends on the archive format chosen in the retention policy.

**SQLite archives (recommended when historical queries are frequent):**

The admin binary can attach archived SQLite files directly using SQLite's
`ATTACH DATABASE` mechanism. This gives full OQL query capability over
historical data without loading it into a hot instance or spinning up a
temporary server:

```
iolu query --tenant acme --archive 2026-01 \
    "SELECT * FROM assets WHERE status = 'decommissioned'"
```

Internally, the admin binary opens a read-only connection to the archive
file (downloaded from object storage to a temporary path if needed) and
executes the query using the same OQL engine as the main binary. Multiple
archive files can be attached simultaneously for queries spanning several
months.

This is the recommended archive format for deployments that anticipate
regular historical queries — compliance audits, regulatory reporting,
trend analysis.

**Parquet archives (recommended when storage efficiency is the priority):**

Parquet files are compact and efficient for bulk analytics but are not
directly queryable by olu's OQL engine. Two access paths are available:

1. **Temporary olu instance.** The admin binary loads the Parquet data into
   a temporary SQLite database, starts a read-only olu instance, executes
   the query, and destroys the temporary instance. This is transparent to
   the operator but slower than the ATTACH path.

2. **External analytics tools.** Parquet files can be queried directly by
   tools such as DuckDB, Apache Spark, or pandas. This is appropriate when
   the archive data feeds into an existing analytics pipeline rather than
   being queried through olu.

**JSON Lines archives (for portability):**

JSON Lines files are human-readable and processable by standard unix tools
(`jq`, `grep`, `awk`). They are the largest format and the slowest to query.
Use this format only when interoperability with systems that cannot read
Parquet or SQLite is required.

**Format selection guidance:**

| Criterion | SQLite | Parquet | JSON Lines |
|----------|--------|---------|------------|
| Historical query frequency | High | Low | Rare |
| Storage efficiency | Moderate | Best | Worst |
| Query latency (admin) | Milliseconds | Seconds (load + query) | Minutes (parse + filter) |
| External tool compatibility | SQLite clients | Analytics ecosystem | Universal |
| Recommended for | Compliance, audits | Bulk storage, analytics | Interchange, debugging |

The archive format is configured per tenant and per entity type in the
retention policy. Different entity types within the same tenant may use
different formats — for example, high-volume sensor readings archived as
Parquet for storage efficiency, while audit-critical transaction records
are archived as SQLite for fast regulatory query access.

This trade-off — archived data requires an explicit, slower access path — is
deliberate. It keeps the hot path fast and the storage costs low. If a
deployment finds itself querying archives frequently, the first response
should be to increase the `hot_days` retention period. The archive query
path is an escape hatch, not a substitute for keeping operationally relevant
data in hot storage.

---

## 7. Failure Modes and Recovery

### 7.1 Component Failures

| Component | Impact | Detection | Recovery |
|-----------|--------|-----------|----------|
| Hot instance | Tenants on that instance unavailable | Gateway health check fails | Restore from Litestream backup to standby instance; restore timeseries from Pebble backup; update placements |
| Admin instance | No provisioning, no archival | None needed (runs on demand) | Restart when needed |
| Gateway | All client traffic blocked | Load balancer health check | Failover to second gateway |
| Registry | No routing updates | Admin operations fail | Restore from Litestream; hot instances use cached copy |
| Archive storage | Cannot freeze or restore tenants | Archive operations fail | Restore from object storage replication |

### 7.2 Hot Instance Recovery

Each hot instance's SQLite database is continuously replicated via Litestream
to object storage. Recovery procedure:

1. Provision a new instance (or use a standby).
2. Restore the SQLite database from Litestream: `litestream restore -o data.db`.
3. Update the instance's endpoint in the registry.
4. Update tenant placements to point to the new instance.
5. Gateway picks up the change on next refresh cycle.

Expected recovery time: 2-5 minutes for databases under 10 GB. The bottleneck
is the Litestream restore, which streams from object storage.

### 7.3 Split-Brain Prevention

Split-brain is prevented by two mechanisms working together:

**Placement epochs.** Each tenant's primary placement carries a monotonically
increasing epoch (see Section 3.7). When a hot instance fails and is replaced,
the new placement increments the epoch. If the old instance comes back online,
its cached epoch is stale. Three mechanisms detect this:

1. **Gateway header** — every proxied request carries `X-Placement-Epoch`;
   the hot instance rejects immediately if the header epoch exceeds its cache.
2. **Background refresh** — the instance's cached epoch map updates on the
   poll interval; staleness is detected within seconds even without headers.
3. **No traffic** — the gateway routes to the new instance, so the zombie
   receives no requests in the first place.

All three must fail simultaneously for a split-brain write to occur.

**Gateway routing authority.** The gateway is the sole entry point for client
traffic. It routes based on the registry's current placement. A zombie instance
that comes back online receives no traffic because the gateway's placement
table points elsewhere. Even if a client bypasses the gateway and connects
directly to the zombie (a misconfiguration), the epoch check on the hot
instance itself will reject the request.

The placement table is the authoritative record, and only the admin binary can
modify it. The combination of gateway routing and instance-side epoch checking
provides defence in depth: either mechanism alone is sufficient, and both must
fail simultaneously for a split-brain to occur.

**RPO contract.** If a hot instance fails unrecoverably, the Litestream backup
may be missing the final seconds of WAL writes that had not yet been replicated.
This is the RPO (Recovery Point Objective) trade-off of asynchronous
replication. The per-tenant `durability` setting in the retention policy
(see Section 3.2) controls this trade-off:

| Durability | RPO | Write latency impact |
|-----------|-----|---------------------|
| `async` (default) | Seconds (Litestream replication lag) | None |
| `confirmed` | Zero (write confirmed after replication) | +50-200ms per write |

The `confirmed` setting is appropriate for tenants with regulatory or
contractual requirements for zero data loss — for example, financial
transaction records. The `async` setting is appropriate for high-volume
telemetry where losing a few seconds of data after an unrecoverable instance
failure is acceptable. Both settings can coexist on the same hot instance,
applied per-tenant.

### 7.4 Registry Loss

If `registry.db` is lost or corrupted:

1. Restore from Litestream backup (seconds of data loss at most).
2. If Litestream backup is unavailable: reconstruct from hot instances.
   Each hot instance knows its own tenants (from its local data). The admin
   binary can scan each instance and rebuild the tenant and placement tables.

This reconstruction is possible because the registry is derived from the
state of the hot instances, not the other way around. The registry is an
index, not the source of truth for entity data.

---

## 8. Security Boundaries

### 8.1 Trust Model

```
Client ──(TLS)──> Gateway ──(mTLS/internal)──> Hot Instance
                                                    │
                     Admin ──(mTLS/internal)─────────┘
                       │
                       └──(mTLS/internal)──> Registry
```

- Clients authenticate to the gateway. The gateway verifies identity and
  tenant membership.
- The gateway communicates with hot instances over an internal network using
  mutual TLS or a shared internal token.
- The admin binary authenticates to hot instances and the registry using
  credentials from the secrets store.
- Hot instances never communicate with each other.

**Internal header trust boundary.** The gateway attaches metadata headers to
proxied requests (`X-Placement-Epoch`, `X-Tenant-Verified`). Hot instances
must only honour these headers on connections that have been authenticated via
the internal channel (mTLS or shared token). Requests arriving without valid
internal authentication — including any direct client connections that bypass
the gateway — must have these headers stripped or ignored. This prevents a
client from spoofing a high epoch value to force a hot instance to stop
serving a tenant (a denial-of-service vector). The hot instance's internal
authentication check is the first step in request processing, before any
header inspection.

### 8.2 Privilege Ladder

| Level | Actor | Can do |
|-------|-------|--------|
| 0 | Client (tenant-scoped) | CRUD on own tenant's data |
| 1 | Gateway | Route requests; read registry |
| 2 | Hot instance | Serve tenant data; insert tenant rows (path mode) |
| 3 | Admin binary | All registry operations; cross-tenant queries; archive |
| 4 | Operator | Start/stop admin binary; manage secrets; access object storage |

No single credential grants access to all tenants' data through the normal
API. Cross-tenant access requires the admin binary, which requires operator
credentials to start and is audited.

### 8.3 Network Segmentation

Recommended network layout:

| Zone | Components | Ingress | Egress |
|------|-----------|---------|--------|
| DMZ | Gateway, load balancer | Client traffic (443) | Internal network |
| Application | Hot instances | Gateway only (8443) | Object storage |
| Management | Admin binary, registry | Operator only (SSH/VPN) | Application zone, object storage |
| Storage | Object storage (S3/MinIO) | Application + management zones | None |

The admin binary and registry are never exposed to the DMZ. Hot instances
accept connections only from the gateway and the admin binary.

---

## 9. Implementation Roadmap

The fleet architecture is built incrementally. Each step is independently
useful and deployable.

### Phase 1: Persistent Registry (days)

- Create `registry.db` schema (tenants, instances, placements).
- Modify `tenant.Registry` to load from and persist to SQLite on startup.
- In `path` mode, new tenants are written to `registry.db` on
  `GetOrRegister`.
- In `strict` mode, `registry.db` is opened read-only.
- Add Litestream configuration for `registry.db`.

**Outcome:** Tenants survive restarts. Registry is the source of truth.

### Phase 2: Admin Binary (days)

- Create `cmd/iolu` with `//go:build admin`.
- Implement tenant lifecycle commands (create, disable, freeze, delete).
- Implement instance registration.
- Implement audit logging.
- Add `make build-iolu` target.

**Outcome:** Tenant provisioning and lifecycle management from the CLI.

### Phase 3: Gateway (days)

- Build a thin Go reverse proxy or configure nginx/Caddy.
- Read tenant-to-instance mapping from a local copy of `registry.db`.
- Implement health checking for hot instances.
- Add TLS termination and client authentication.

**Outcome:** Multi-instance deployment with automatic tenant routing.

### Phase 4: Archive and Retention (one to two weeks)

- Add retention policy table and CLI commands.
- Implement batch export from hot instances (Parquet writer).
- Implement verified delete with batched operations.
- Implement archive manifest and checksum verification.
- Add `iolu archive run` scheduled operation.

**Outcome:** Automatic data lifecycle management. Hot storage stays lean.

### Phase 5: Operational Maturity (one to two weeks)

- Litestream integration for hot instance backup and recovery.
- Tenant migration between instances (`iolu migrate tenant`).
- Cross-tenant reporting in the admin binary.
- Monitoring integration (Prometheus metrics from gateway and hot instances).
- Runbook documentation for common operational procedures.

**Outcome:** Production-grade fleet operations.

### Future Considerations (beyond Phase 5)

The following optimisations are not required for initial deployment but may
become valuable as the fleet grows:

- **Warm migration via Litestream streaming.** The current migration approach
  exports and imports data in bulk, causing write-unavailability proportional
  to data size. A "warm" migration would use Litestream to continuously
  replicate the source instance's WAL to the destination instance while the
  tenant remains fully active. The final cutover would be a brief
  freeze-and-final-sync (seconds, regardless of data size). This requires a
  Litestream-to-Litestream pipeline that does not exist today, but would
  reduce migration downtime for large tenants (>10 GB) from hours to seconds.

- **Per-tenant resource quotas.** Advisory CPU and I/O limits per tenant,
  enforced at the hot instance level, to mitigate the noisy-neighbour effect
  without requiring dedicated instance placement.

- **Multi-region active-active for read-heavy tenants.** A read replica
  placement in a second region, streamed via Litestream, with the gateway
  routing reads to the nearest replica. Writes still go to the single primary.

---

## 10. Appendix: Configuration Reference

### 10.1 Hot Instance

```yaml
server:
  host: 0.0.0.0
  port: 8443
  tls_cert: /etc/olu/server.crt
  tls_key: /etc/olu/server.key

storage:
  type: sqlite
  db_path: /var/lib/olu/data.db

tenant:
  mode: strict
  registry_path: /var/lib/olu/registry.db
  registry_readonly: true
  registry_refresh_interval: 10s  # how often to check for placement changes
  epoch_check: true               # reject requests if local epoch is stale

backup:
  litestream:
    data_db: s3://olu-backup/hot-a/data
    registry_db: s3://olu-backup/shared/registry
```

### 10.2 Admin Binary

```yaml
registry:
  path: /var/lib/olu/registry.db
  readonly: false
  lock_staleness_timeout: 30m     # break advisory locks older than this

archive:
  default_format: parquet         # parquet | sqlite | jsonl
  compression: zstd
  base_path: s3://olu-archive/
  temp_dir: /tmp/olu-archive      # for downloading archive files during queries

durability:
  default: async                  # async | confirmed (per-tenant override in policy)

migration:
  cooldown_seconds: 60            # tombstone period on source after cutover
  transfer_rate_local: 100        # MB/s estimate for same-region (for Retry-After)
  transfer_rate_remote: 20        # MB/s estimate for cross-region

instances:
  credentials_source: env         # env | secrets-manager | file
  gateway_heartbeat_stale: 30s    # ignore gateways with heartbeat older than this

audit:
  enabled: true
```

### 10.3 Gateway

```yaml
listen:
  address: 0.0.0.0:443
  tls_cert: /etc/olu/gateway.crt
  tls_key: /etc/olu/gateway.key

registry:
  path: /var/lib/olu/registry.db
  refresh_interval: 10s
  self_register: true             # register this gateway in the instances table
  instance_name: gateway-1        # name for self-registration (must be unique)
  heartbeat_interval: 10s         # how often to update last_heartbeat

routing:
  unknown_tenant: reject          # reject | auto-provision
  auto_provision_instance: hot-a
  migrating_tenant_reads: proxy   # proxy (to source) | reject (503)
  migrating_tenant_writes: reject # always 503 with Retry-After
  include_epoch_header: true      # send X-Placement-Epoch on every proxied request

cache_invalidation:
  enabled: true
  listen: 127.0.0.1:9091         # internal only; never exposed to DMZ
  admin_token_env: OLU_ADMIN_TOKEN

health_check:
  interval: 5s
  timeout: 2s
  unhealthy_threshold: 3

upstream:
  tls_ca: /etc/olu/internal-ca.crt
```
