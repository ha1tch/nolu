# nolu + xolu Operations Manual

Version: nolu 0.7.7 / xolu 0.9.7  
Audience: operators, platform engineers, system integrators

---

## Table of Contents

1. [Architecture overview](#1-architecture-overview)
2. [xolu: installation and configuration](#2-xolu-installation-and-configuration)
3. [iolu: database administration CLI](#3-iolu-database-administration-cli)
4. [nolu: installation and configuration](#4-nolu-installation-and-configuration)
5. [nolu-proxy: the tenant-aware reverse proxy](#5-nolu-proxy-the-tenant-aware-reverse-proxy)
6. [Deployment patterns](#6-deployment-patterns)
7. [Day-to-day operations](#7-day-to-day-operations)
8. [Tenant hotswap](#8-tenant-hotswap)
9. [Health, monitoring, and diagnostics](#9-health-monitoring-and-diagnostics)
10. [Troubleshooting](#10-troubleshooting)
11. [Reference: API error codes](#11-reference-api-error-codes)
12. [Reference: environment variables](#12-reference-environment-variables)

---

## 1. Architecture overview

### Components

```
┌─────────────────────────────────────────────────────────┐
│  nolu (port 7070)                                        │
│  ┌──────────────┐  ┌──────────────┐  ┌───────────────┐  │
│  │  Registry    │  │  Transfers   │  │  Hotswap      │  │
│  │  (GlobalID   │  │  (negotiation│  │  (migration   │  │
│  │   mapping)   │  │   lifecycle) │  │   state FSM)  │  │
│  └──────────────┘  └──────────────┘  └───────────────┘  │
│  ┌──────────────────────────────────────────────────┐    │
│  │  Tenant Directory  (event-driven locate index)   │    │
│  └──────────────────────────────────────────────────┘    │
│  ┌──────────────────────────────────────────────────┐    │
│  │  Reverse Proxy  /proxy/tenant/{name}/...         │    │
│  └──────────────────────────────────────────────────┘    │
└──────────────────────────┬──────────────────────────────┘
                           │ stores registry records
                           ▼
               ┌────────────────────┐
               │  xolu (port 9090)  │  ← nolu's own backing store
               │  tenant: nolu_registry
               └────────────────────┘

           ┌────────────────┐   ┌────────────────┐
           │  xolu-hub-a    │   │  xolu-hub-b    │  ← application data
           │  (port 9090)   │   │  (port 9091)   │
           └────────────────┘   └────────────────┘
```

### Roles

**xolu** is a document store with graph traversal, OQL queries, and optional timeseries. It is self-contained and has no knowledge of nolu. Each xolu instance is sovereign — it owns and serves its own data.

**nolu** is a clearinghouse. It does not store application data. It assigns every entity a GlobalID — a stable URI that persists when the entity moves between xolu instances — and maintains the authoritative mapping from GlobalID to current xolu location (a LocalRef).

**iolu** is a CLI for xolu database administration. It creates databases, manages tenants, exports and imports data, and verifies database integrity. It operates directly on SQLite files and must not be used against a running xolu instance except for tenant create/delete operations.

**nolu-proxy** is an optional sidecar that resolves tenant names to xolu instances and forwards requests transparently. Applications route through the proxy and never need to know which xolu instance holds a tenant.

### Data flow

When an application registers a new entity:

1. Application creates the entity in xolu: `POST /api/v1/tenant/acme/devices` → gets local ID `42`
2. Application registers the entity in nolu: `POST /registry` with the xolu LocalRef → gets GlobalID `nolu://registry.acme.com/devices/<uuid>`
3. nolu stores the mapping internally and updates the tenant directory

When an entity transfers to a new xolu instance:

1. Parties negotiate via `POST /transfers` → `POST /transfers/{id}/accept` → `POST /transfers/{id}/complete`
2. On complete, nolu updates the GlobalID → LocalRef mapping atomically
3. The tenant directory reflects the new location within milliseconds

---

## 2. xolu: installation and configuration

### Installing xolu

```bash
# From the published Docker image (recommended)
docker pull ghcr.io/ha1tch/xolu:latest

# From source
git clone https://github.com/ha1tch/xolu.git
cd xolu && make build
./bin/olu
```

### Storage backends

xolu supports two storage backends:

**SQLite** (recommended for production)

```bash
OLU_STORAGE_TYPE=sqlite
OLU_DB_PATH=/data/instance.db
```

SQLite with WAL mode. One writer, multiple readers. Suitable for single-node and single-tenant workloads. For multi-tenant deployments each tenant gets its own connection pool — plan file descriptors accordingly (at least `tenants × 4 + 50`).

**JSONFile** (development only)

```bash
OLU_STORAGE_TYPE=jsonfile
OLU_BASE_DIR=/data
OLU_SCHEMA_NAME=default
```

No SQLite dependency. Not suitable for production.

### Tenant modes

**Path mode** (default) — tenants are created automatically on first access. Suitable for single-organisation or development deployments where every client is trusted.

```bash
OLU_TENANT_MODE=path
OLU_TENANT_AUTO_REGISTER=true
```

**Strict mode** — tenants must be pre-registered with iolu before xolu starts. Suitable for multi-organisation hubs where tenant isolation is a security boundary. A request for an unregistered tenant returns 404.

```bash
OLU_TENANT_MODE=strict
OLU_TENANT_AUTO_REGISTER=false
```

With strict mode, always initialise the database with iolu before starting xolu. See [§3 iolu](#3-iolu-database-administration-cli).

### Full configuration reference

| Variable | Default | Description |
|---|---|---|
| `OLU_HOST` | `0.0.0.0` | Bind address |
| `OLU_PORT` | `9090` | HTTP port |
| `OLU_STORAGE_TYPE` | `jsonfile` | `sqlite` or `jsonfile` |
| `OLU_DB_PATH` | `olu.db` | SQLite database path |
| `OLU_BASE_DIR` | `data` | JSONFile base directory |
| `OLU_SCHEMA_NAME` | `default` | JSONFile schema namespace |
| `OLU_TENANT_MODE` | `path` | `path` or `strict` |
| `OLU_TENANT_AUTO_REGISTER` | `false` | Auto-create tenants in path mode |
| `OLU_CACHE_TYPE` | `memory` | `memory` or `redis` |
| `OLU_CACHE_TTL` | `300` | Cache TTL in seconds |
| `OLU_REDIS_HOST` | `localhost` | Redis host |
| `OLU_REDIS_PORT` | `6379` | Redis port |
| `OLU_GRAPH_MODE` | `flat` | `flat` or `disabled` |
| `OLU_AUTH_TYPE` | `none` | `none`, `jwt`, or `apikey` |
| `OLU_JWT_SECRET` | — | JWT validation secret |
| `OLU_API_KEYS` | — | Comma-separated valid API keys |
| `OLU_RATE_LIMIT_ENABLED` | `false` | Enable rate limiting |
| `OLU_RATE_LIMIT_RATE` | `100` | Requests per window |
| `OLU_RATE_LIMIT_WINDOW` | `60` | Window duration in seconds |
| `OLU_QUERY_TIMEOUT` | `30s` | OQL query timeout |
| `OLU_QUERY_MAX_ROWS` | `10000` | Max rows returned by OQL |
| `OLU_QUERY_MAX_SCAN_ROWS` | `100000` | Max rows scanned by OQL |
| `OLU_TIMESERIES_ENABLED` | `false` | Enable Pebble timeseries |
| `OLU_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `OLU_SQLITE_BUSY_TIMEOUT` | `5000` | SQLite busy timeout in ms |

### Running xolu in Docker

```bash
docker run -d \
  --name xolu \
  -p 9090:9090 \
  -v /opt/xolu/data:/data \
  -e OLU_STORAGE_TYPE=sqlite \
  -e OLU_DB_PATH=/data/instance.db \
  -e OLU_PORT=9090 \
  -e OLU_LOG_LEVEL=info \
  ghcr.io/ha1tch/xolu:latest
```

### Health endpoints

```
GET /health   → 200 {"status":"ok"} when storage is reachable
GET /ready    → same; use for Kubernetes readiness probes
GET /version  → version string
GET /metrics  → Prometheus-format counters
```

---

## 3. iolu: database administration CLI

iolu administers xolu SQLite databases. It operates directly on the file — always use it against a stopped instance, or only for safe concurrent operations (tenant create/delete) against a running one.

### Installation

iolu is built from the xolu source tree. In a nolu deployment it is available as a Docker image for use as an init container:

```bash
# Build from source
cd xolu && go build -o iolu ./cmd/iolu

# Or run directly from the nolu embedded source
cd nolu && go run ./xolu/cmd/iolu help
```

### Database lifecycle

**Initialise a new database before starting xolu in strict mode:**

```bash
iolu db init \
  --db /data/hub.db \
  --graph \
  --tenant vendocorp:1 \
  --tenant retailchain:2 \
  --tenant serviceco:3
```

Options:

| Flag | Description |
|---|---|
| `--db` | Path to create the database (required; must not exist) |
| `--graph` | Create graph edge tables |
| `--tenant name:id` | Register a tenant (repeatable; id optional, auto-assigned if omitted) |
| `--ts-dir` | Provision timeseries directories |

**Check database health before starting xolu:**

```bash
iolu db status --db /data/hub.db
```

Output includes: file size, WAL status, schema versions, table row counts, registered tenants with entity counts, graph tables, timeseries directory.

**Apply migrations after upgrading xolu:**

```bash
iolu db upgrade --db /data/hub.db
```

Safe to run on already-current databases. Idempotent.

**Verify database integrity:**

```bash
iolu db check --db /data/hub.db
```

Runs SQLite `integrity_check` and `foreign_key_check`, verifies schema version, checks for orphaned entity rows, checks sequence consistency, and checks FTS row count. Exits 0 on success, 1 on any issue.

### Tenant management

```bash
# Add a tenant to an existing database
iolu tenant create --db /data/hub.db --name newpartner

# Add with an explicit ID
iolu tenant create --db /data/hub.db --name newpartner --id 4

# List all tenants
iolu tenant list --db /data/hub.db

# Detailed tenant info (entity breakdown, graph, timeseries)
iolu tenant info --db /data/hub.db --name vendocorp

# Delete a tenant (fails if entity data exists unless --force)
iolu tenant delete --db /data/hub.db --name oldpartner

# Rename a tenant (xolu must be restarted afterwards)
iolu tenant rename --db /data/hub.db --from oldname --to newname

# Provision timeseries storage for a tenant
iolu tenant provision-ts \
  --db /data/hub.db \
  --name vendocorp \
  --ts-dir /data/ts
```

### Data migration (for hotswap)

These commands are used by the nolu hotswap system but can also be run manually.

```bash
# Export a tenant's data to a portable archive
iolu tenant export \
  --db /data/hub-a.db \
  --name vendocorp \
  --out /shared/vendocorp-$(date +%Y%m%d).tar.gz \
  --include-sequences \
  --include-graph

# Delta export (rows modified after a timestamp)
iolu tenant export \
  --db /data/hub-a.db \
  --name vendocorp \
  --out /shared/vendocorp-delta.tar.gz \
  --since 2026-05-01T00:00:00Z \
  --include-sequences \
  --include-graph

# Import to a target database
iolu tenant import \
  --db /data/hub-b.db \
  --name vendocorp \
  --file /shared/vendocorp-20260501.tar.gz

# Import delta (upsert existing rows)
iolu tenant import \
  --db /data/hub-b.db \
  --name vendocorp \
  --file /shared/vendocorp-delta.tar.gz \
  --upsert

# Validate consistency between two databases
iolu tenant validate \
  --source-db /data/hub-a.db --source-name vendocorp \
  --target-db /data/hub-b.db --target-name vendocorp

# Deep validation (row-by-row content comparison)
iolu tenant validate \
  --source-db /data/hub-a.db --source-name vendocorp \
  --target-db /data/hub-b.db --target-name vendocorp \
  --deep

# Archive after successful migration
iolu tenant archive \
  --db /data/hub-a.db \
  --name vendocorp \
  --migrated-to http://xolu-hub-b:9091
```

---

## 4. nolu: installation and configuration

### Installing nolu

```bash
# Build from source
git clone https://github.com/ha1tch/nolu.git
cd nolu && make build
./bin/nolu -host registry.acme.com -listen :7070
```

### Starting nolu

Minimal development start (in-memory storage, loses state on restart):

```bash
nolu -host registry.acme.com
```

Production start with xolu-backed storage and NATS event bus:

```bash
nolu \
  -host registry.acme.com \
  -listen :7070 \
  -storage xolu \
  -xolu http://xolu-registry:9090 \
  -bus nats \
  -nats nats://nats:4222 \
  -log info
```

### Configuration reference

All flags have equivalent environment variables.

| Flag | Env | Default | Description |
|---|---|---|---|
| `-host` | `NOLU_REGISTRY_HOST` | `localhost` | Registry hostname embedded in minted GlobalIDs |
| `-listen` | `NOLU_LISTEN_ADDR` | `:7070` | HTTP API listen address |
| `-storage` | `NOLU_STORAGE_TYPE` | `memory` | `memory` or `xolu` |
| `-xolu` | `NOLU_XOLU_URL` | — | xolu URL (required when storage=xolu) |
| `-bus` | `NOLU_BUS_TYPE` | `memory` | `memory` or `nats` |
| `-nats` | `NOLU_NATS_URL` | — | NATS server URL |
| — | `NOLU_NATS_STREAM` | `NOLU_EVENTS` | NATS JetStream stream name |
| — | `NOLU_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| — | `NOLU_HOTSWAP_ENABLED` | `true` | Enable hotswap state machine |
| — | `NOLU_HOTSWAP_STORAGE` | (same as storage) | `memory` or `xolu` |
| — | `NOLU_QUIESCE_TIMEOUT` | `30s` | Default quiesce wait timeout |
| — | `NOLU_URL` | — | This nolu's URL, for sidecar proxy resolution |

### Storage selection

**Memory storage** (`-storage memory`): all registry records are held in RAM. Suitable for development, testing, and demos. State is lost on restart.

**xolu storage** (`-storage xolu`): registry records are persisted to a dedicated xolu instance. This xolu instance uses the `nolu_registry` tenant in path mode — it is created automatically on first access. The hotswap manager uses the same xolu instance when `NOLU_HOTSWAP_STORAGE=xolu`.

The xolu instance used by nolu for its own storage is distinct from the xolu instances that hold application data. Do not use the same xolu instance for both purposes.

### xolu storage setup

When using xolu storage, configure the backing xolu in path mode with auto-register:

```bash
# xolu instance for nolu's own registry records
docker run -d \
  --name xolu-registry \
  -p 9093:9090 \
  -v /opt/nolu-registry/data:/data \
  -e OLU_STORAGE_TYPE=sqlite \
  -e OLU_DB_PATH=/data/registry.db \
  -e OLU_TENANT_MODE=path \
  -e OLU_TENANT_AUTO_REGISTER=true \
  ghcr.io/ha1tch/xolu:latest
```

Then start nolu pointing at it:

```bash
nolu -host registry.acme.com \
     -storage xolu \
     -xolu http://xolu-registry:9093
```

### Health endpoint

```
GET /health → 200 {"status":"ok","version":"0.7.7"}
GET /version → {"version":"...","registry_host":"...","hotswap":true,"proxy":true}
```

---

## 5. nolu-proxy: the tenant-aware reverse proxy

The proxy allows applications to use a single address for all xolu access. It resolves tenant names to xolu instances and forwards requests transparently. When a tenant moves (hotswap), the proxy handles the redirection automatically — applications see no disruption.

### Deployment modes

**Embedded** (default): the proxy runs inside nolu and is mounted at `/proxy/`. Resolution uses the in-process tenant directory — zero additional network hops.

```
Application → GET http://nolu:7070/proxy/tenant/vendocorp/devices/42
             → nolu resolves vendocorp → http://xolu-hub-a:9090
             → forwards to GET http://xolu-hub-a:9090/api/v1/tenant/1/devices/42
```

**Sidecar** (`nolu-proxy`): a standalone process that resolves by calling nolu's `/tenants/{name}/locate` endpoint. Run independently for horizontal scaling of the proxy layer.

```bash
nolu-proxy \
  -nolu http://nolu:7070 \
  -listen :7071 \
  -ttl 30s
```

```
Application → GET http://nolu-proxy:7071/tenant/vendocorp/devices/42
             → nolu-proxy calls GET http://nolu:7070/tenants/vendocorp/locate
             → forwards to xolu
```

### URL structure

Embedded mode: `http://nolu:7070/proxy/tenant/{name}/{entity_type}/{id}`

Sidecar mode: `http://nolu-proxy:7071/tenant/{name}/{entity_type}/{id}`

Both forward to xolu as: `http://{instance_url}/api/v1/tenant/{numeric_id}/{entity_type}/{id}`

### Sidecar configuration

| Flag | Env | Default | Description |
|---|---|---|---|
| `-nolu` | `NOLU_PROXY_NOLU_URL` | — | nolu registry URL (required) |
| `-listen` | `NOLU_PROXY_LISTEN` | `:7071` | Proxy listen address |
| `-ttl` | `NOLU_PROXY_CACHE_TTL` | `30s` | Tenant location cache TTL |
| `-log` | `NOLU_PROXY_LOG_LEVEL` | `info` | Log level |

### 307 redirect handling

xolu returns `307 Temporary Redirect` when a tenant has been quiesced for migration. The proxy detects this, invalidates its cache, re-resolves the tenant, and retries — the caller never sees the 307. This is the primary mechanism that makes hotswaps transparent to applications routing through the proxy.

Applications that call xolu directly (bypassing the proxy) must handle 307 themselves.

### Health endpoint

```
GET /health → {"status":"ok","version":"...","mode":"sidecar"}
```

---

## 6. Deployment patterns

### Pattern A: single organisation, single xolu instance

Suitable for development, small deployments, or single-tenant use. No strict mode required.

```
nolu (memory storage) ←→ xolu (path mode, auto-register)
```

```bash
# xolu
docker run -d -p 9090:9090 \
  -e OLU_STORAGE_TYPE=sqlite \
  -e OLU_DB_PATH=/data/app.db \
  -e OLU_TENANT_MODE=path \
  -e OLU_TENANT_AUTO_REGISTER=true \
  ghcr.io/ha1tch/xolu:latest

# nolu
nolu -host registry.local -listen :7070 \
     -storage memory
```

### Pattern B: multi-organisation hub (strict mode)

Multiple organisations share a single xolu instance as tenants. Tenant isolation is a hard boundary — strict mode ensures no accidental cross-tenant data access.

```
       ┌─────────────┐
       │    nolu     │  (durable storage → xolu-registry)
       └─────────────┘
              │
       ┌─────────────┐
       │  xolu-hub   │  strict mode: vendocorp, retailchain, serviceco
       └─────────────┘
```

```bash
# 1. Initialise the database before starting xolu
iolu db init \
  --db /data/hub.db \
  --graph \
  --tenant vendocorp:1 \
  --tenant retailchain:2 \
  --tenant serviceco:3

# 2. Start xolu in strict mode
docker run -d -p 9090:9090 \
  -v /data:/data \
  -e OLU_STORAGE_TYPE=sqlite \
  -e OLU_DB_PATH=/data/hub.db \
  -e OLU_TENANT_MODE=strict \
  -e OLU_TENANT_AUTO_REGISTER=false \
  ghcr.io/ha1tch/xolu:latest

# 3. Start nolu
nolu -host registry.acme.com \
     -storage xolu \
     -xolu http://xolu-registry:9093
```

### Pattern C: federated multi-instance (Docker Compose)

Multiple independent xolu instances, each owned by a different organisation, coordinated by a shared nolu registry. The four provided demos illustrate this pattern.

```bash
# Run the four-organisation federation demo
cd nolu
make run-demo4
```

The demo brings up: 5-node NATS cluster, EU/US/APAC regional hubs (each strict mode, provisioned by iolu init containers), a global service xolu instance, and a registry xolu instance (path mode).

### Pattern D: production with durable storage and NATS

```bash
# NATS
docker run -d --name nats \
  -p 4222:4222 \
  nats:2.10-alpine --jetstream

# xolu for nolu's own registry records
docker run -d --name xolu-registry \
  -p 9093:9090 \
  -v /opt/nolu-registry:/data \
  -e OLU_STORAGE_TYPE=sqlite \
  -e OLU_DB_PATH=/data/registry.db \
  -e OLU_TENANT_MODE=path \
  -e OLU_TENANT_AUTO_REGISTER=true \
  ghcr.io/ha1tch/xolu:latest

# nolu
docker run -d --name nolu \
  -p 7070:7070 \
  -e NOLU_REGISTRY_HOST=registry.acme.com \
  -e NOLU_LISTEN_ADDR=:7070 \
  -e NOLU_STORAGE_TYPE=xolu \
  -e NOLU_XOLU_URL=http://xolu-registry:9093 \
  -e NOLU_BUS_TYPE=nats \
  -e NOLU_NATS_URL=nats://nats:4222 \
  -e NOLU_HOTSWAP_STORAGE=xolu \
  nolu:0.7.7
```

---

## 7. Day-to-day operations

### Registering an entity

First create the entity in xolu, then register its identity in nolu.

```bash
# Step 1: create the entity in xolu
curl -X POST http://xolu-hub:9090/api/v1/tenant/vendocorp/devices \
  -H "Content-Type: application/json" \
  -d '{"serial":"SN-12345","model":"VM-100"}'
# → {"id": 42, ...}

# Step 2: register the GlobalID in nolu
curl -X POST http://nolu:7070/registry \
  -H "Content-Type: application/json" \
  -d '{
    "entity_type": "devices",
    "owner": {
      "instance_url": "http://xolu-hub:9090",
      "tenant_name": "vendocorp",
      "tenant_id": 1,
      "entity_type": "devices",
      "local_id": 42
    }
  }'
# → {"global_id": "nolu://registry.acme.com/devices/<uuid>", "status": "active", ...}
```

### Resolving an entity's current location

```bash
GLOBAL_ID="nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a"
ENCODED=$(python3 -c "import urllib.parse; print(urllib.parse.quote('$GLOBAL_ID', safe=''))")

curl http://nolu:7070/registry/${ENCODED}/resolve
# → {
#     "global_id": "nolu://...",
#     "current": {
#       "instance_url": "http://xolu-hub:9090",
#       "tenant_id": 1,
#       "entity_type": "devices",
#       "local_id": 42
#     }
#   }
```

### Looking up a tenant's location

```bash
curl http://nolu:7070/tenants/vendocorp/locate
# → {
#     "tenant": "vendocorp",
#     "instance_url": "http://xolu-hub:9090",
#     "tenant_id": 1,
#     "entity_count": 47,
#     "stable_until": "2026-05-01T12:30:00Z"
#   }
```

### Negotiating a transfer

```bash
# Propose
curl -X POST http://nolu:7070/transfers \
  -H "Content-Type: application/json" \
  -d '{
    "global_id": "nolu://registry.acme.com/devices/<uuid>",
    "from": {"instance_url":"http://xolu-a:9090","tenant_id":1,"entity_type":"devices","local_id":42},
    "to":   {"instance_url":"http://xolu-b:9091","tenant_id":1,"entity_type":"devices","local_id":7},
    "protocol": "PO-2026-001",
    "history_offer": {"mode": "full"}
  }'
# → {"id": "txn-id", "state": "proposed", ...}

# Accept (by the receiving party)
curl -X POST http://nolu:7070/transfers/txn-id/accept \
  -H "Content-Type: application/json" \
  -d '{"history_spec": {"mode": "full"}}'

# Complete (after data has been physically moved in xolu)
curl -X POST http://nolu:7070/transfers/txn-id/complete

# Check state
curl http://nolu:7070/transfers/txn-id
```

Transfer states: `proposed → accepted → completed`. Can be `rejected` or `cancelled` before completion.

### Retiring an entity

```bash
curl -X POST http://nolu:7070/registry/${ENCODED}/retire \
  -H "Content-Type: application/json" \
  -d '{"reason": "device decommissioned"}'
```

After retirement, `GET /registry/{id}/resolve` returns `410 Gone`. No further transfers are permitted.

### Listing all entities on an instance

```bash
curl "http://nolu:7070/registry?instance_url=http://xolu-hub:9090"
# → {"instance_url":"...","count":47,"global_ids":["nolu://...",...]}
```

### Listing transfers for a GlobalID

```bash
curl "http://nolu:7070/transfers?global_id=nolu%3A%2F%2F..."
```

---

## 8. Tenant hotswap

A tenant hotswap migrates all entities belonging to a tenant from one xolu instance to another with a brief write-outage window. nolu orchestrates the state machine; iolu performs the data migration; xolu quiesces to stop writes during the critical window.

### State machine

```
REQUESTED → PREPARING → QUIESCING → MIGRATING → VALIDATING → CUTTING_OVER → COMPLETE
                     ↘           ↘           ↘            ↘
                      ROLLING_BACK ────────────────────────────── FAILED
```

| State | Description |
|---|---|
| REQUESTED | Operator initiated. nolu validates reachability. |
| PREPARING | Bulk sync window. Source still serving reads and writes. |
| QUIESCING | Source stops accepting writes. In-flight requests drain. |
| MIGRATING | Delta sync: iolu exports rows modified since bulk sync and imports to target. |
| VALIDATING | iolu validates row counts, sequences, and graph edges between source and target. |
| CUTTING_OVER | nolu atomically updates all GlobalIDs to point at the target. |
| COMPLETE | Hotswap finished. Source returns 307 for this tenant. |
| ROLLING_BACK | Failure detected. Source quiesce lifted. |
| FAILED | Terminal. Source unchanged. |

### Initiating a hotswap

```bash
curl -X POST http://nolu:7070/hotswaps \
  -H "Content-Type: application/json" \
  -d '{
    "source": {
      "instance_url": "http://xolu-hub-a:9090",
      "tenant_name": "vendocorp",
      "tenant_id": 1
    },
    "target": {
      "instance_url": "http://xolu-hub-b:9091",
      "tenant_name": "vendocorp",
      "tenant_id": 1
    },
    "options": {
      "auto_advance": false,
      "quiesce_timeout": "30s",
      "source_db_path": "/data/hub-a.db",
      "target_db_path": "/data/hub-b.db"
    }
  }'
# → {"id": "hs-id", "state": "preparing", "entity_count": 47, ...}
```

`auto_advance: false` keeps the hotswap in PREPARING until the operator explicitly confirms. This gives time for any operator-managed bulk data sync to complete before quiescing the source.

If `source_db_path` and `target_db_path` are not set, nolu skips the iolu migration and validation phases. The operator is responsible for ensuring data is in place before confirming.

### Monitoring progress

```bash
# Poll-friendly status with phase elapsed time
curl http://nolu:7070/hotswaps/hs-id/status

# Full record with state history
curl http://nolu:7070/hotswaps/hs-id

# List all active hotswaps
curl "http://nolu:7070/hotswaps?state=preparing"
```

### Confirming (advancing from PREPARING to QUIESCING)

When `auto_advance` is false, call confirm when ready to quiesce the source:

```bash
curl -X POST http://nolu:7070/hotswaps/hs-id/confirm
```

nolu then signals xolu to stop accepting writes for this tenant. The state advances automatically through MIGRATING → VALIDATING → CUTTING_OVER → COMPLETE.

### Aborting a hotswap

At any non-terminal state:

```bash
curl -X POST http://nolu:7070/hotswaps/hs-id/abort \
  -H "Content-Type: application/json" \
  -d '{"reason": "target capacity insufficient"}'
```

nolu lifts the quiesce on the source and the tenant remains on the source instance. All GlobalIDs are unchanged.

### Post-migration cleanup

After a successful COMPLETE:

```bash
# Archive the tenant data from the source
iolu tenant archive \
  --db /data/hub-a.db \
  --name vendocorp \
  --migrated-to http://xolu-hub-b:9091
```

The source xolu instance will return 307 for all requests to this tenant, directing clients to the new instance via nolu or the proxy.

### Database path requirements

The `source_db_path` and `target_db_path` must be accessible from the machine running nolu. In Docker or Kubernetes, this typically requires the database volumes to be mounted into the nolu container as well as the xolu containers, or a shared NFS/object-storage mount.

If direct database access is not available, omit the path options and manage data migration manually before confirming.

---

## 9. Health, monitoring, and diagnostics

### Health checks

| Endpoint | Service | Use |
|---|---|---|
| `GET /health` | xolu, nolu, nolu-proxy | Liveness |
| `GET /ready` | xolu | Readiness (K8s) |
| `GET /metrics` | xolu | Prometheus scrape target |
| `GET /version` | xolu, nolu, nolu-proxy | Version and capability flags |

**Kubernetes probe example:**

```yaml
livenessProbe:
  httpGet:
    path: /health
    port: 9090
  initialDelaySeconds: 5
  periodSeconds: 10
  failureThreshold: 3
readinessProbe:
  httpGet:
    path: /ready
    port: 9090
  initialDelaySeconds: 3
  periodSeconds: 5
```

### Database health check

Before starting xolu, or after an unclean shutdown:

```bash
iolu db check --db /data/hub.db
echo "Exit code: $?"  # 0 = healthy, 1 = issues found
```

### Verifying the federation

Check that nolu can see all expected tenants:

```bash
# List all entities on a specific xolu instance
curl "http://nolu:7070/registry?instance_url=http://xolu-hub:9090"

# Locate a tenant
curl http://nolu:7070/tenants/vendocorp/locate
```

Check the proxy is resolving correctly:

```bash
curl http://nolu:7070/proxy/tenant/vendocorp/devices/42
# Should forward to the correct xolu instance and return the device
```

### Log interpretation

nolu and nolu-proxy emit structured JSON logs. Key fields:

| Field | Meaning |
|---|---|
| `level` | `debug`, `info`, `warn`, `error` |
| `message` | Human-readable description |
| `id` | Hotswap ID (hotswap log lines) |
| `state` | New state after transition (hotswap) |
| `tenant` | Tenant name |
| `global_id` | GlobalID being processed |
| `elapsed` | Request duration |

Example: tracking a hotswap through logs:

```
{"level":"info","id":"hs-abc","state":"quiescing","note":"operator confirmed"}
{"level":"info","id":"hs-abc","source":"http://xolu-hub-a:9090","tenant":"vendocorp","message":"hotswap: signalling quiesce"}
{"level":"info","id":"hs-abc","state":"migrating","note":"source quiesce confirmed"}
{"level":"info","id":"hs-abc","message":"hotswap: migration phase — invoking iolu"}
{"level":"info","id":"hs-abc","state":"validating","note":"delta migration complete"}
{"level":"info","id":"hs-abc","state":"cutting_over","note":"validation passed"}
{"level":"info","id":"hs-abc","transferred":47,"total":47,"message":"hotswap: complete"}
{"level":"info","id":"hs-abc","state":"complete","note":"cutover: 47/47 GlobalIDs transferred"}
```

---

## 10. Troubleshooting

### xolu returns 404 for a tenant

**Cause A: strict mode, tenant not registered.**

```bash
iolu tenant list --db /data/hub.db
# Check if the tenant appears in the list
```

If missing, register it (xolu can be running for tenant create):

```bash
iolu tenant create --db /data/hub.db --name missingtenant
# Then restart xolu so it loads the new tenant from the database
```

**Cause B: wrong tenant name in the request path.**

Check the path: `/api/v1/tenant/{name-or-id}/{entity_type}`. In strict mode the name must match exactly what was registered.

### nolu returns 404 on `/tenants/{name}/locate`

The tenant directory is event-driven and builds from registration events. If no entities have been registered for this tenant in nolu, the directory is empty.

```bash
# Check if any GlobalIDs are registered for this xolu instance
curl "http://nolu:7070/registry?instance_url=http://xolu-hub:9090"
```

If empty, ensure entities are being registered in nolu after creation in xolu. The locate endpoint reflects nolu's knowledge only — it does not query xolu directly.

### nolu returns 503 on `/tenants/{name}/locate`

The tenant directory has not finished starting up. Wait a few seconds and retry. This occurs only briefly at nolu startup.

### Hotswap stuck in PREPARING

If `auto_advance` is false, the hotswap waits for operator confirmation. Call:

```bash
curl -X POST http://nolu:7070/hotswaps/hs-id/confirm
```

If `auto_advance` is true and the hotswap is still in PREPARING after several seconds, check nolu logs for errors in the quiesce or migration phases.

### Hotswap failed with "iolu binary not found"

The `iolu_binary` option was not set and `iolu` is not in `PATH` on the machine running nolu, or `source_db_path`/`target_db_path` were set but iolu is unavailable.

Either:

1. Install iolu and ensure it is in PATH
2. Set `options.iolu_binary` to the absolute path of the iolu binary
3. Omit `source_db_path` and `target_db_path` to skip iolu-managed migration and manage data transfer manually

### SQLite "database is locked"

This means another process has the database file open in write mode simultaneously.

1. Check for other processes: `fuser /data/hub.db`
2. If the WAL file is large, a checkpoint is pending — this is normal under write load
3. Increase busy timeout: `OLU_SQLITE_BUSY_TIMEOUT=10000`
4. Never run `iolu db init` or `iolu db upgrade` while xolu is running

### Proxy returning 502 Bad Gateway

The proxy resolved the tenant to a xolu instance but could not connect.

1. Check the instance is running: `curl http://xolu-hub:9090/health`
2. Check network connectivity from the proxy to xolu
3. Check the `stable_until` field from `/tenants/{name}/locate` — if in the past, a hotswap recently completed and the proxy cache may be stale (it will revalidate automatically)

### Entities not appearing in locate after registration

The tenant directory processes events asynchronously via a buffered channel. Under normal conditions the lag is under 1 millisecond. Under very high registration rates or immediately after nolu starts (during bootstrap scan), it may take slightly longer.

If entities registered before nolu started are not appearing, check that `MemoryRegistry.SeedDirectory` ran successfully at startup (look for no errors in the startup log).

---

## 11. Reference: API error codes

### nolu error codes

| Code | HTTP | Meaning |
|---|---|---|
| `NOLU-RG001` | 404 | GlobalID not found |
| `NOLU-RG002` | 409 | GlobalID already registered |
| `NOLU-RG003` | 410 | Entity is retired |
| `NOLU-RG004` | 409 | Transfer From does not match current owner |
| `NOLU-RG005` | 503 | Tenant directory not initialised |
| `NOLU-TX001` | 404 | Transfer proposal not found |
| `NOLU-TX002` | 409 | Proposal is not in the required state |
| `NOLU-TX003` | 403 | Caller not authorised |
| `NOLU-HS001` | 409 | Hotswap already in progress for this tenant |
| `NOLU-HS002` | 502 | Source or target xolu unreachable |
| `NOLU-HS003` | 404 | Hotswap not found |
| `NOLU-HS004` | 409 | Hotswap is not in the required state |
| `NOLU-XX001` | 400 | Invalid request (missing field, bad JSON) |
| `NOLU-XX002` | 500 | Internal error |

### xolu error codes (selected)

| Code | HTTP | Meaning |
|---|---|---|
| `OLU-ST001` | 404 | Unknown tenant |
| `OLU-ST002` | 404 | Entity not found |
| `OLU-QS001` | 503 / 307 | Tenant quiesced (write rejected) |
| `OLU-GR005` | 413 | Graph traversal node limit exceeded |
| `OLU-QL001` | 400 | OQL parse error |
| `OLU-QL002` | 413 | OQL result size limit exceeded |
| `OLU-QL003` | 408 | OQL query timeout |

---

## 12. Reference: environment variables

### xolu

| Variable | Default | Description |
|---|---|---|
| `OLU_HOST` | `0.0.0.0` | Bind address |
| `OLU_PORT` | `9090` | HTTP port |
| `OLU_STORAGE_TYPE` | `jsonfile` | `sqlite` or `jsonfile` |
| `OLU_DB_PATH` | `olu.db` | SQLite database path |
| `OLU_BASE_DIR` | `data` | JSONFile base directory |
| `OLU_SCHEMA_NAME` | `default` | JSONFile namespace |
| `OLU_TENANT_MODE` | `path` | `path` or `strict` |
| `OLU_TENANT_AUTO_REGISTER` | `false` | Auto-create tenants (path mode) |
| `OLU_CACHE_TYPE` | `memory` | `memory` or `redis` |
| `OLU_CACHE_TTL` | `300` | Cache TTL in seconds |
| `OLU_REDIS_HOST` | `localhost` | Redis host |
| `OLU_REDIS_PORT` | `6379` | Redis port |
| `OLU_GRAPH_MODE` | `flat` | `flat` or `disabled` |
| `OLU_GRAPH_MAX_VISITED_NODES` | `10000` | Max nodes per traversal |
| `OLU_AUTH_TYPE` | `none` | `none`, `jwt`, or `apikey` |
| `OLU_JWT_SECRET` | — | JWT validation secret |
| `OLU_JWT_ISSUER` | — | Expected JWT issuer |
| `OLU_API_KEYS` | — | Comma-separated valid API keys |
| `OLU_RATE_LIMIT_ENABLED` | `false` | Enable rate limiting |
| `OLU_RATE_LIMIT_RATE` | `100` | Requests per window |
| `OLU_RATE_LIMIT_WINDOW` | `60` | Window in seconds |
| `OLU_QUERY_TIMEOUT` | `30s` | OQL query timeout |
| `OLU_QUERY_MAX_ROWS` | `10000` | Max rows returned |
| `OLU_QUERY_MAX_SCAN_ROWS` | `100000` | Max rows scanned |
| `OLU_TIMESERIES_ENABLED` | `false` | Enable Pebble timeseries |
| `OLU_SQLITE_BUSY_TIMEOUT` | `5000` | SQLite busy timeout (ms) |
| `OLU_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |

### nolu

| Variable | Default | Description |
|---|---|---|
| `NOLU_REGISTRY_HOST` | `localhost` | Hostname embedded in GlobalIDs |
| `NOLU_LISTEN_ADDR` | `:7070` | HTTP API listen address |
| `NOLU_STORAGE_TYPE` | `memory` | `memory` or `xolu` |
| `NOLU_XOLU_URL` | — | xolu URL for registry storage |
| `NOLU_BUS_TYPE` | `memory` | `memory` or `nats` |
| `NOLU_NATS_URL` | — | NATS server URL |
| `NOLU_NATS_STREAM` | `NOLU_EVENTS` | NATS JetStream stream name |
| `NOLU_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `NOLU_HOTSWAP_ENABLED` | `true` | Enable hotswap state machine |
| `NOLU_HOTSWAP_STORAGE` | (same as storage) | `memory` or `xolu` |
| `NOLU_QUIESCE_TIMEOUT` | `30s` | Default quiesce wait timeout |
| `NOLU_URL` | — | This nolu's URL (for sidecar proxy) |

### nolu-proxy (sidecar)

| Variable | Default | Description |
|---|---|---|
| `NOLU_PROXY_NOLU_URL` | — | nolu registry URL (required) |
| `NOLU_PROXY_LISTEN` | `:7071` | Proxy listen address |
| `NOLU_PROXY_CACHE_TTL` | `30s` | Tenant location cache TTL |
| `NOLU_PROXY_CACHE_SIZE` | `1024` | Max cached tenant locations |
| `NOLU_PROXY_DIAL_TIMEOUT` | `5s` | Connection timeout to xolu |
| `NOLU_PROXY_FORWARD_TIMEOUT` | `30s` | Total request forward timeout |
| `NOLU_PROXY_MAX_REDIRECTS` | `3` | Max 307 redirects to follow |
| `NOLU_PROXY_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
