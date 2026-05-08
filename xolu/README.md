# Olu

**A graph-enhanced document store with SQL-like query capabilities**

[![License](https://img.shields.io/badge/license-Apache%202.0-blue.svg)](LICENSE)
[![Go Version](https://img.shields.io/badge/go-1.22+-00ADD8.svg)](https://golang.org/)

<!-- RELEASE_BADGE -->
> **v0.9.7-patched73** — 2360 tests passing. See [MANUAL.md](MANUAL.md) for complete documentation.
<!-- /RELEASE_BADGE -->
## What is Olu?

Olu is a REST API server that combines document storage with automatic graph representation of entity relationships. Define your data, create entities with references, and get both SQL-like queries (OQL) and graph traversal for free.

Olu operates in two modes:

- **Single-tenant** — full feature set: CRUD, REFs, OQL, full-text search, graph traversal
- **Multi-tenant (strict)** — full feature set: CRUD, REFs, OQL, full-text search, graph traversal; all operations tenant-scoped

> Olu is a Go port of [rserv](https://github.com/ha1tch/rserv) (Python). Both share the same API design.

## Quick Start

```bash
git clone https://github.com/ha1tch/xolu.git && cd olu
make build && ./bin/olu
```

Create an entity:
```bash
curl -X POST http://localhost:9090/api/v1/users \
  -H "Content-Type: application/json" \
  -d '{"name": "Alice", "email": "alice@example.com"}'
```

Create with reference:
```bash
curl -X POST http://localhost:9090/api/v1/posts \
  -H "Content-Type: application/json" \
  -d '{"title": "Hello", "author": {"type": "REF", "entity": "users", "id": 1}}'
```

Query with OQL:
```bash
curl -X POST http://localhost:9090/api/v1/oql/query \
  -d '{"query": "SELECT * FROM users WHERE age > 25 ORDER BY name LIMIT 10"}'
```

## Key Features

| Feature | Description |
|---------|-------------|
| **Dual Storage** | JSONFile (development) or SQLite (production) with WAL mode |
| **Automatic Graph** | REF objects create graph edges; full traversal API |
| **OQL** | SQL-like query language with adaptive push-down planner |
| **Full-text Search** | SQLite FTS5 integration with tenant scoping |
| **Multi-tenant** | Path-based isolation with strict mode enforcement |
| **Read/Write Split** | Separate connection pools for concurrent reads under WAL |
| **Adaptive Concurrency** | Lock contention monitoring with automatic backoff |
| **Timeseries** | Pebble-backed append-optimised storage for sensor events (strict mode) |
| **Authentication** | JWT and API key support |
| **Metrics** | Prometheus `/metrics` endpoint |

## Multi-Tenancy

Olu provides two operational modes:

**Single-tenant** (`OLU_TENANT_MODE=path`): All features enabled. Non-tenant routes use the default store (tenant 0). Tenant-prefixed routes (`/api/v1/tenant/{id}/...`) provide optional scoping.

**Multi-tenant strict** (`OLU_TENANT_MODE=strict`): Tenant context required for all data operations. Graph, OQL, search, and export are available through tenant-prefixed routes only. Graph operations are fully tenant-isolated: node IDs are transparently prefixed and stripped, and cross-tenant traversal is blocked at the snapshot layer. Auto-registration of new tenants is controlled by `OLU_TENANT_AUTO_REGISTER`. Timeseries storage (Pebble-backed) is available only in this mode.

See the [Multi-Tenancy section of the manual](MANUAL.md#multi-tenancy) for the full security model.

## API Overview

| Endpoint | Description |
|----------|-------------|
| `POST /api/v1/{entity}` | Create entity |
| `GET /api/v1/{entity}/{id}` | Get entity (with embedded refs) |
| `GET /api/v1/{entity}` | List entities (paginated) |
| `PUT /api/v1/{entity}/{id}` | Full update |
| `PATCH /api/v1/{entity}/{id}` | Partial update |
| `DELETE /api/v1/{entity}/{id}` | Delete entity |
| `POST /api/v1/oql/query` | Run OQL query (sync) |
| `POST /api/v1/oql/query/async` | Run OQL query (async) |
| `GET /api/v1/search?q=term` | Full-text search |
| `POST /api/v1/graph/shortestPath` | Find shortest path |
| `GET /metrics` | Prometheus metrics |

All entity and query endpoints have tenant-scoped variants at `/api/v1/tenant/{tenant_id}/...`.

## Configuration

Key environment variables:

```bash
# Storage
OLU_STORAGE_TYPE=sqlite          # jsonfile or sqlite
OLU_DB_PATH=olu.db               # SQLite path
OLU_FULLTEXT_ENABLED=true        # Enable FTS5

# Multi-tenancy
OLU_TENANT_MODE=strict           # path or strict
OLU_TENANT_AUTO_REGISTER=false   # Explicit tenant creation only

# SQLite tuning (0 = backend default)
OLU_SQLITE_MAX_OPEN_CONNS=0      # Writer pool size (default: 1 for WAL)
OLU_SQLITE_READ_POOL_SIZE=0      # Reader pool size (default: NumCPU)

# Auth
OLU_AUTH_TYPE=jwt                # none, jwt, or apikey
```

See [MANUAL.md](MANUAL.md) for all options.

## Testing

```bash
make test        # Quick tests
make test-full   # Full suite including stress tests
make bench       # Benchmarks
```

## Project Structure

```
olu/
├── cmd/olu/              # Server entry point
├── cmd/olu-migrate/      # JSONFile → SQLite migration tool
├── pkg/
│   ├── cache/            # Memory (sharded) & Redis cache
│   ├── config/           # Configuration with validation
│   ├── errors/           # Structured error codes
│   ├── graph/            # FlatGraph — tenant-isolated in-memory graph with persistence
│   ├── middleware/        # Auth, rate limiting, metrics
│   ├── oql/              # OQL engine, planner, SQL generator
│   ├── server/           # HTTP handlers and routing
│   ├── storage/          # JSONFile & SQLite with read/write split
│   ├── sulpher/          # Sulpher graph query engine
│   ├── tenant/           # Tenant registry
│   ├── timeseries/       # Pebble-backed timeseries storage
│   └── validation/       # JSON Schema validation
└── docs/                 # Architecture and API documentation
```

## Documentation

- **[Manual](MANUAL.md)** — Full API and configuration reference
- **[Timeseries Design](docs/TIMESERIES_DESIGN_V3.md)** — Pebble-backed timeseries storage for sensor events
- **[Fleet Architecture](docs/FLEET_ARCHITECTURE.md)** — Multi-instance deployment design
- **[OQL API](docs/OQL_API.md)** — Query language reference
- **[Graph API](docs/GRAPH_API.md)** — Graph traversal endpoints
- **[Export API](docs/EXPORT_API.md)** — Data export endpoints
- **[Query Planner](docs/QUERY_PLANNER.md)** — Adaptive query planner internals

## License

Copyright (c) 2025-2026 haitch

Apache 2.0 — See [LICENSE](LICENSE)

---

**[Full Documentation →](MANUAL.md)**
