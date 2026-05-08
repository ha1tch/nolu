# nolu Changelog

## 0.1.0 — 2026-05-06

Initial skeleton release.

### Added

- `pkg/identity` — GlobalID and LocalRef types; OwnershipRecord and Transfer structs
- `pkg/registry` — Registry interface: Register, Get, Resolve, Transfer, Retire, Subscribe
- `pkg/events` — Bus interface; NoopBus, MemoryBus implementations; ActivityPubBridge placeholder
- `pkg/routing` — Router interface; Subscription, Endpoint, WatchScope types
- `pkg/transfer` — Negotiator interface; Proposal lifecycle (proposed → accepted → completed / rejected / cancelled)
- `pkg/config` — Config struct and defaults
- `pkg/version` — Version constant
- `cmd/nolu` — Entry point with flag parsing and graceful shutdown; implementations pending

### Architecture notes

nolu is the federated entity registry that enables xolu instances to coordinate
across organisational boundaries. It owns GlobalIDs and the routing table;
entity data stays in xolu.

The event bus abstraction supports memory (default), NATS JetStream (production),
and a future aulsql substrate. The ActivityPubBridge placeholder allows optional
fediverse integration without making it a hard dependency.

All core types are defined as interfaces to allow backend substitution. The
next milestone implements MemoryRegistry and wires the HTTP API.

## 0.1.1 — 2026-05-06

### Added

- `pkg/events/nats_bus.go` — NATSBus: durable JetStream bus implementation
  - CreateOrUpdateStream on connect (stream name configurable)
  - Durable push consumers with explicit ack, max 5 redeliveries
  - Graceful drain on Close()
  - sanitiseName() for NATS-safe consumer identifiers
- `pkg/registry/memory.go` — MemoryRegistry: concrete Registry implementation
  - Thread-safe with RWMutex
  - Publishes events to Bus asynchronously (non-blocking under lock)
  - In-process Subscribe with SubscriptionFilter support
- `pkg/transfer/memory.go` — MemoryNegotiator: concrete Negotiator implementation
  - Full proposal lifecycle: propose → accept/reject/cancel → complete
  - Accept drives registry.Transfer atomically
- `cmd/demo/main.go` — Clearinghouse scenario demo
  - 9-phase story: registration, batch sale, rejection, cancellation,
    repair cycle, return, retirement, subscription drain, final snapshot
  - Works with -bus=memory (no dependencies) or -bus=nats
- `Dockerfile` — multi-stage Alpine build; produces nolu + nolu-demo binaries
- `docker-compose.yml` — NATS JetStream + 3 xolu instances + nolu-registry + demo runner
- `Makefile` — build, demo, demo-nats, docker-build, docker-up, docker-demo

## 0.1.2 — 2026-05-06

### Added

- `pkg/registry/registry_test.go` — 17 tests covering Register, Get, Resolve,
  Transfer (success, wrong owner, multiple hops), Retire (success, resolve fails,
  transfer fails, double-retire), ListByInstance, ListByEntityType, Subscribe
  (all events, entity-type filter), and TestDemoScenario (asserts exact Phase 8
  snapshot programmatically)
- `pkg/transfer/transfer_test.go` — 9 tests covering Propose, Accept (success,
  wrong state), Reject (registry unchanged), Cancel (registry unchanged),
  Complete (requires accepted, success), ListByGlobalID
- `Makefile` — complete target set:
  - `make check`        — vet + test + demo (CI target)
  - `make verify`       — TestDemoScenario only (fast targeted check)
  - `make test-verbose` — verbose test output
  - `make demo-nats`    — demo against local NATS
  - `make docker-verify`— bring up stack + run demo
  - `make docker-nats-info` — inspect JetStream stream post-demo
  - `make version`      — print current version

## 0.1.3 — 2026-05-06

### Added

- `Makefile` — `make help` target: sectioned, colour-coded, version-stamped,
  includes common workflow recipes

## 0.1.4 — 2026-05-06

### Added

- `pkg/xoluclient/client.go` — thin HTTP client for the xolu REST API:
  Healthy, Create, Get, Exists, Delete, Patch, IntID helper; tenant-scoped
  paths; ErrNotFound sentinel
- `test/e2e/e2e_test.go` — true end-to-end test suite (3 tests):
  - TestE2E_XoluInstancesHealthy — connectivity smoke test
  - TestE2E_FullTransferLifecycle — 11-step scenario: create on vendocorp,
    register GlobalID, resolve, create on retailchain, propose/accept/complete
    transfer, resolve post-transfer, verify entity on retailchain, verify source
    still exists (nolu does not delete), NATS event count, retire, verify entity
    survives retirement, cleanup
  - TestE2E_RepairCycle — three-party repair: retailchain → serviceco →
    retailchain, verifying entity presence and registry state at each hop
  - All tests skip automatically when services are not reachable
- `Makefile` — e2e targets: test-e2e, test-e2e-health, docker-e2e;
  help updated with E2E TESTS section and "First real e2e run" workflow

## 0.1.5 — 2026-05-06

### Fixed

- `Dockerfile` — replaced `go mod download` (fails without network in Docker
  build) with `COPY vendor/ vendor/` + `-mod=vendor` build flags; builds are
  now fully offline once `make vendor` has been run

### Added

- `vendor/` — vendored dependencies (12 MB); Docker builds no longer need
  outbound network access to proxy.golang.org
- `.dockerignore` — excludes .git, bin/, test/, docs/, *.zip, *.md from
  the Docker build context
- `Makefile` — `vendor` target (`go mod vendor`); `docker-build` now depends
  on `vendor` so the vendor directory is always current before a build

## 0.1.6 — 2026-05-06

### Fixed

- `go.mod` — pinned to `go 1.22` to match the `golang:1.22-alpine` Docker
  image; pinned all transitive dependencies to Go 1.22 compatible versions:
  - `nats.go` v1.51.0 → v1.39.1 (last release requiring only Go 1.22)
  - `nats/nkeys` v0.4.15 → v0.4.9
  - `klauspost/compress` v1.18.5 → v1.17.9
  - `golang.org/x/sys` v0.42.0 → v0.30.0
  - `golang.org/x/text` v0.35.0 → v0.22.0
  - `golang.org/x/crypto` v0.49.0 → v0.31.0
- `vendor/` — re-vendored against the pinned dependency set

### Added

- `Makefile` — `tidy` target: runs `go mod tidy` with `GOTOOLCHAIN=local`,
  pins the `go` directive back to 1.22, and re-vendors; prevents a Go 1.25+
  toolchain from silently upgrading `go.mod` on the next tidy
- `Makefile` — all `vendor` and `tidy` targets use `GOTOOLCHAIN=local` to
  prevent automatic toolchain switching

## 0.1.7 — 2026-05-06

### Fixed

- `docker-compose.yml` — NATS and xolu healthchecks replaced `wget` (not
  present in alpine images) with `nc -z <host> <port>`; added `start_period`
  to NATS check to give it time to bind before probes begin
- `docker-compose.yml` — removed obsolete `version: "3.9"` attribute

## 0.1.8 — 2026-05-06

### Fixed

- `docker-compose.yml` — NATS would not start due to `--log_time` flag
  (not a valid nats-server flag); corrected to `--logtime`
- `docker-compose.yml` — removed all healthchecks that relied on `wget`,
  `nc`, `/dev/tcp`, or `--signal` (none reliably present in Alpine-based
  images); NATS and xolu services now use `restart: unless-stopped` for
  recovery instead; `nolu-registry` retains `pgrep`-based check
- `docker-compose.yml` — removed `condition: service_healthy` from all
  `depends_on` blocks since NATS no longer has a healthcheck; simplified
  to plain service name lists

## 0.1.9 — 2026-05-06

### Fixed

- `docker-compose.yml` — xolu instances were all listening on port 9090
  internally because `OLU_LISTEN_ADDR` is not a valid xolu env var; replaced
  with the correct `OLU_PORT` variable; retailchain (9091) and serviceco (9092)
  are now reachable from the host, unblocking `make test-e2e`
- `docker-compose.yml` — removed nolu-registry healthcheck (`pgrep` not
  guaranteed in Alpine); all services now rely on `restart: unless-stopped`

## 0.2.0 — 2026-05-06

### Added

- `docs/API.md` — full HTTP API specification covering:
  - Registry (register, get, resolve, transfer, retire, list by instance,
    list by entity type)
  - Transfer negotiation (propose, get, accept, reject, cancel, complete,
    list by entity, list by instance)
  - Subscriptions (create, get, delete, list, pause subscriber,
    resume subscriber)
  - Utility (health, version)
  - Complete error code table (NOLU-RGxxx, NOLU-TXxxx, NOLU-SWxxx,
    NOLU-XXxxx)
  - Design notes on GlobalID URL encoding, idempotency, pagination,
    and authentication

## 0.3.0 — 2026-05-06

### Added

- `pkg/registry/xolu.go` — XoluRegistry: durable Registry implementation
  backed by a xolu instance. Storage layout: `nolu_records` (one document
  per GlobalID with indexed fields for OQL queries) and `nolu_events`
  (append-only event log). Uses xolu's `_version` field for optimistic
  concurrency on Transfer and Retire. Provisions the `nolu_registry` tenant
  on startup (idempotent).
- `pkg/xoluclient/extensions.go` — xoluclient extensions required by
  XoluRegistry: OQL query execution, Save (conditional upsert with _version),
  EnsureTenant, NewTenant (named-tenant client constructor); tenantName field
  added to Client struct for path-mode named tenants
- `test/e2e/xolureg_test.go` — XoluRegistry e2e tests:
  - TestE2E_XoluRegistry_Durability: registers, transfers, retires, then
    creates a new XoluRegistry instance pointing at the same xolu and
    verifies all records survived (simulated restart)
  - TestE2E_XoluRegistry_ConcurrentTransfer: launches two concurrent
    transfers for the same GlobalID and verifies exactly one wins via
    xolu's _version 409 response
  - TestE2E_XoluRegistry_NegotiatedTransfer: full negotiate protocol
    driven by XoluRegistry as backing store
- `cmd/nolu/main.go` — NATSBus and XoluRegistry wired at startup when
  -bus=nats and -storage=xolu flags are set; MemoryRegistry remains the
  default for development
- `Makefile` — test-e2e-xolureg target

## 0.4.0 — 2026-05-06

### Added

- `cmd/demo1/main.go` — Demo 1: three separate xolu instances, one org each,
  memory registry (the original demo, now properly versioned and documented)
- `cmd/demo2/main.go` — Demo 2: one multi-tenant xolu instance (strict mode),
  three org tenants; demonstrates tenant isolation verification; shows that
  nolu's identity model is instance-agnostic
- `cmd/demo3/main.go` — Demo 3: XoluRegistry (durable), 3-node NATS cluster,
  three org xolus + dedicated registry xolu; five phases: registration, positive
  transfers, negative cases (rejection, wrong-owner, retired transfer,
  cancellation, double-retire, concurrent race), restart durability, OQL analytics
- `cmd/demo4/main.go` — Demo 4: large federation topology — EU/US/APAC
  multi-tenant xolu hubs + global service provider + registry; 5-node NATS
  cluster; 20 devices across 3 regions; cross-regional transfers; 5-way
  concurrent race; batch retirement; restart durability; OQL analytics
- `docker-compose.yml` — rewritten with Docker Compose profiles; each demo
  has its own profile (demo1/demo2/demo3/demo4) with dedicated services,
  volumes, and NATS cluster topology
- `Dockerfile` — builds all four demo binaries (nolu-demo1 through nolu-demo4)
- `Makefile` — per-demo targets: demo1-up/down, demo2-up/down, demo3-up/down,
  demo4-up/down, docker-demo1, docker-demo2, docker-demo3, docker-demo4;
  legacy docker-up/docker-demo/docker-down aliases preserved

## 0.4.1 — 2026-05-06

### Fixed

- `docker-compose.yml` — all multi-tenant xolu instances (xolu-hub, xolu-d3-registry,
  xolu-d4-registry, xolu-d4-eu, xolu-d4-us, xolu-d4-apac) switched from
  `OLU_TENANT_MODE=strict` to `OLU_TENANT_MODE=path` with
  `OLU_TENANT_AUTO_REGISTER=true`; xolu strict mode has no REST endpoint for
  tenant creation, causing 403 OLU-TN002 errors on demo2 startup
- `pkg/xoluclient/extensions.go` — `EnsureTenant` rewritten to use a harmless
  OQL probe query that triggers xolu path-mode auto-registration, replacing the
  non-existent `POST /api/v1/tenants` endpoint call

## 0.4.2 — 2026-05-06

### Fixed

- `docker-compose.yml` — reverted path/auto-register workaround; all
  multi-tenant xolu instances now correctly use `OLU_TENANT_MODE=strict`;
  tenants are pre-registered using `iolu tenant create` via init containers
  (xolu-hub-init, xolu-d3-registry-init, xolu-d4-registry-init,
  xolu-d4-eu-init, xolu-d4-us-init, xolu-d4-apac-init) that run against the
  shared SQLite volume before xolu starts; main services use
  `condition: service_completed_successfully` on their init dependency
- `pkg/xoluclient/extensions.go` — `EnsureTenant` simplified: now performs
  a lightweight OQL probe to confirm tenant accessibility rather than
  attempting tenant creation; creation is iolu's responsibility

### Notes

  iolu (the xolu administrative CLI) handles tenant lifecycle management.
  The correct pattern for strict-mode tenant provisioning is:
    iolu tenant create --db /data/xolu.db --name <tenant> [--id <n>]
  This writes directly to the SQLite file and must be done before xolu starts.

## 0.4.3 — 2026-05-06

### Fixed

- `docker-compose.yml` — iolu init containers now build `iolu` from the
  sibling `../xolu` source directory using `Dockerfile.iolu`; the xolu
  runtime image (`ghcr.io/ha1tch/xolu:latest`) only ships `olu`, not `iolu`
- `Dockerfile.iolu` — new Dockerfile that builds only the `iolu` binary from
  the xolu source tree; used as the build target for all init containers;
  build context is `../xolu`, dockerfile is `../nolu/Dockerfile.iolu`

### Notes

  The `../xolu` source directory must be present when running any multi-tenant
  demo (demo2, demo3, demo4). The directory layout expected:
    repo/
      nolu/   ← docker compose is run from here
      xolu/   ← iolu init containers build from here

## 0.5.0 — 2026-05-07

### Added (iolu)

- `iolu db init` — creates a new SQLite database with the complete xolu schema
  (entities, entity_sequences, schemas, schema_version, tenants, FTS5 virtual
  table), sets WAL/NORMAL/FK pragmas, records schema version markers 2 and 3,
  optionally creates graph edge table for tenant 0 (--graph), and registers
  initial tenants (--tenant name:id, repeatable). Exits non-zero if the
  database already exists. This is the correct bootstrap primitive for strict-
  mode xolu deployments and for nolu init containers.
- `iolu db status` — reports file size, WAL presence, schema versions, row
  counts for all core tables, graph tables with edge counts, tenant list with
  entity counts, timeseries directory status.
- `iolu db upgrade` — applies pending schema migrations (v2 baseline, v3
  _version column) to an existing database. Safe to run on already-current
  databases.
- `iolu tenant provision-ts` — provisions the timeseries storage directory
  (ts/t<hex>/) for a named tenant. Previously only available via REST API.
- Improved `iolu help` — covers all db and tenant subcommands with examples.

### Fixed

- `docker-compose.yml` — rewritten from scratch; demo2/demo3 service blocks
  were lost in prior edits; all four demos now correctly defined
- `docker-compose.yml` — demo2 xolu-hub and demo4 org hubs now use strict
  mode with `iolu db init` init containers; demo3/demo4 registry xolus use
  path mode (single nolu_registry tenant, auto-created on first access)
- `xolu/` — embedded xolu source updated with new iolu

## 0.6.0 — 2026-05-07

### Added

- `pkg/proxy/config.go` — ProxyConfig: fully independent of nolu's main Config;
  all settings prefixed NOLU_PROXY_; works in embedded and sidecar mode
- `pkg/proxy/resolver.go` — Resolver interface; TenantLocation type with
  XoluPath() helper; RegistryResolver (in-process, for embedded mode);
  HTTPResolver (calls /tenants/{name}/locate, for sidecar mode); both use
  the location cache with configurable TTL
- `pkg/proxy/cache.go` — thread-safe LRU cache with per-entry TTL and lazy
  expiry; used by both resolver implementations
- `pkg/proxy/proxy.go` — ReverseProxy: path parsing (/proxy/tenant/{name}/...
  → /api/v1/tenant/{id}/...); transparent forwarding; 307 detection and
  automatic cache invalidation + retry; hop-by-hop header stripping;
  X-Forwarded-For propagation; configurable redirect limit
- `cmd/nolu-proxy/main.go` — standalone sidecar binary; health endpoint at
  /health; graceful shutdown; all config via flags or NOLU_PROXY_* env vars
- `Dockerfile` — nolu-proxy binary built and shipped alongside nolu
- `Makefile` — nolu-proxy included in build target

### Architecture

  Two deployment modes, one code path:
  - Embedded: nolu imports pkg/proxy, mounts at /proxy/, uses RegistryResolver
    (in-process, zero network hop for resolution)
  - Sidecar: cmd/nolu-proxy imports pkg/proxy, mounts at /, uses HTTPResolver
    (calls nolu's /tenants/{name}/locate over HTTP)

  Extraction: pkg/proxy has no dependency on pkg/registry or any other nolu
  package except pkg/identity (for LocalRef). Moving it to a standalone module
  requires only updating the import path.

## 0.7.0 — 2026-05-07

### Added (iolu)

- `iolu tenant export` — consistent tar.gz archive export using a SQLite read
  transaction snapshot; bulk (default) or delta (--since RFC3339); exports
  entities.jsonl, sequences.jsonl, graph.jsonl, timeseries files; manifest.json
  with row counts for post-import validation; --dry-run mode
- `iolu tenant import` — restore from export archive to a target database;
  --upsert for delta imports (INSERT OR REPLACE); sequences reconciled with
  MAX(source, target) to prevent ID reuse; graph table auto-created if absent;
  --dry-run validates manifest without writing
- `iolu tenant validate` — cross-instance consistency check: entity counts per
  type, sequence values (target must be >= source), graph edge counts; --deep
  for row-by-row content comparison; exits 0 (valid) or 1 (invalid) for
  scripting
- `iolu tenant archive` — post-migration cleanup: moves entity rows to
  entities_archive table (or --delete-data for hard delete), removes sequences,
  records migrated_to and archived_at tombstone in tenants table; --force to
  archive without migration context
- `iolu tenant rename` — renames a tenant in the tenants table; server must be
  restarted to pick up the change
- `iolu db check` — integrity verification: SQLite integrity_check,
  foreign_key_check, schema version, orphaned entity rows, sequence consistency
  (next_id >= actual max id), FTS row count vs entities row count
- Updated help and usage strings for all new commands

## 0.7.1 — 2026-05-07

### Added

- `pkg/hotswap/hotswap.go` — Hotswap types and Manager interface: State machine
  (requested → preparing → quiescing → migrating → validating → cutting_over →
  complete / rolling_back → failed); InstanceRef (instance+tenant granularity);
  HotswapOptions (AutoAdvance, LagThreshold, QuiesceTimeout, IncludeTimeseries);
  ValidationResult; HotswapStatus (poll-friendly with PhaseElapsed)
- `pkg/hotswap/manager.go` — MemoryManager: full state machine driver;
  Request validates reachability and counts affected GlobalIDs; Confirm drives
  quiesce → migration → validation → cutover asynchronously; Abort at any
  non-terminal state; driveCutover atomically transfers all tenant GlobalIDs
  via registry.Transfer; publishes nolu.events.hotswap.complete to bus;
  TODO stubs for xolu quiesce endpoint and iolu subprocess invocation
- `pkg/config/config.go` — HotswapEnabled and QuiesceTimeout config fields
- `cmd/nolu/main.go` — MemoryManager wired at startup when HotswapEnabled

## 0.7.2 — 2026-05-07

### Added

- `pkg/server/server.go` — HTTP API server (chi router): all registry, transfer
  negotiation, hotswap, and proxy routes; consistent error envelope with
  NOLU-RGxxx / NOLU-TXxxx / NOLU-HSxxx / NOLU-XXxxx codes; logging middleware
- `pkg/server/types.go` — JSON serialisation for Record, LocalRef, Transfer,
  Proposal; type bridges between server and identity/registry/transfer packages
- `pkg/server/server_test.go` — 11 HTTP integration tests via httptest.Server:
  health, version, register, get, resolve, 404 error codes, direct transfer,
  retire (410 on resolve), full negotiation lifecycle (propose/accept/complete),
  hotswap list
- `cmd/nolu/main.go` — HTTP server fully wired: MemoryNegotiator, MemoryManager,
  proxy (nil in embedded mode until locate is implemented), graceful shutdown
  with 10s drain; nolu now starts and serves HTTP on :7070

## 0.7.3 — 2026-05-07

### Added

- `pkg/hotswap/xolu.go` — XoluHotswapManager: durable hotswap manager backed
  by xolu; storage layout: nolu_hotswaps (one document per Hotswap, flat
  indexed fields for OQL: hotswap_id, state_str, source_url, tenant_name) and
  nolu_hotswap_events (append-only transition log); optimistic concurrency via
  _version on all state transitions; Resume() on startup re-arms drivers for
  all non-terminal hotswaps found in xolu; same phase drivers as MemoryManager
  with full persist-before-advance guarantee
- `test/e2e/hotswap_test.go` — TestE2E_XoluHotswapManager_Durability: creates
  a hotswap, simulates restart with a second manager instance, verifies record
  survives, verifies List() works, aborts from the second instance, verifies
  final state
- `cmd/nolu/main.go` — XoluHotswapManager wired when -storage=xolu (or
  NOLU_HOTSWAP_STORAGE=xolu); MemoryManager remains default
- `pkg/config/config.go` — HotswapStorageType field
- `Makefile` — test-e2e-hotswap target

## 0.7.4 — 2026-05-07

### Added

- `pkg/proxy/proxy_test.go` — 12 proxy tests: cache set/get, invalidation, TTL
  expiry, LRU eviction, concurrent access safety, path parsing (9 cases including
  error paths), 307 cache-invalidation-and-retry, 307 max-redirect guard,
  HTTPResolver locate/not-found/caching, TenantLocation.XoluPath for all
  boundary tenant IDs
- `pkg/hotswap/hotswap_test.go` — 14 hotswap tests: unreachable source/target,
  successful request, duplicate rejection, re-allow after terminal state,
  Get/List/Confirm/Abort/Status happy paths and error paths, cutover transfers
  GlobalIDs to target in registry (end-to-end state machine), 5-way concurrent
  request with exactly-1-winner assertion, state history records all transitions
- `pkg/server/server_errors_test.go` — 20 HTTP error path tests: bad JSON,
  retired-entity resolve (410/NOLU-RG003), wrong-owner transfer (409/NOLU-RG004),
  retired-entity transfer (410/NOLU-RG003), double-retire, malformed GlobalID,
  registry list without filter, registry list by instance/entity_type, propose
  without global_id, transfer get/confirm/abort/status 404s, double-accept
  (409/NOLU-TX002), complete-before-accept (409/NOLU-TX002), reject leaves
  registry unchanged, transfer list without filter, hotswap list by state,
  version response fields

### Fixed

- `pkg/hotswap/manager.go` — AutoAdvance now correctly fires driveQuiesce
  immediately from Request when set; previously AutoAdvance was stored but
  never acted on, leaving the state machine stuck in PREPARING
- `pkg/hotswap/manager.go` — filterByTenant documented with safety analysis:
  for multi-tenant hubs the Transfer From-must-match guard prevents data
  corruption even though filterByTenant returns all GIDs; TODO for
  ListByInstanceAndTenant is preserved with clear explanation
- `Makefile` — check target message updated to reflect full package coverage

## 0.7.5 — 2026-05-07

### Added

- `xolu/pkg/server/quiesce.go` — tenant quiesce endpoint: POST/GET/DELETE
  /api/v1/tenant/{id}/quiesce; quiesceMiddleware blocks writes with 503
  (Retry-After: 5) or 307 (Location: redirect_url) while passing reads
  through unchanged; quiesce is isolated per tenant, stored in sync.Map;
  OLU-QS001 error code
- `xolu/pkg/server/quiesce_test.go` — 10 quiesce tests: activate/status,
  status-not-quiesced (404), double-activate (409), deactivate, deactivate-
  not-quiesced (404), blocks-writes-503, blocks-writes-307-with-redirect,
  allows-reads, writes-restored-after-deactivate, isolated-per-tenant
- `xolu/pkg/server/server.go` — quiescedTenants sync.Map field on Server;
  quiesceMiddleware and quiesce routes wired into /tenant/{id} route block
- `pkg/xoluclient/client.go` — QuiesceTenant, QuiesceStatus, UnquiesceTenant
  methods; QuiesceRequest and QuiesceResponse types
- `pkg/hotswap/manager.go` — driveQuiesce now calls srcClient.QuiesceTenant;
  driveRollback now calls rcClient.UnquiesceTenant; brief 100ms drain pause
  after quiesce activation
- `pkg/hotswap/xolu.go` — same real quiesce/unquiesce calls replacing TODO stubs

### Removed

- All "xolu quiesce endpoint not yet implemented" TODO stubs in hotswap managers

## 0.7.6 — 2026-05-07

### Added

- `pkg/registry/directory.go` — TenantDirectory: event-driven index from
  (instanceURL, tenantName) → TenantEntry; subscribes to registry events on
  Start(); bootstraps from existing records via DirectorySeeder interface;
  Locate(name) O(1) by highest entity count; LocateOnInstance for specific
  instance lookup; InvalidateTenant for hotswap cache-busting (short StableUntil);
  ListAll() sorted by name then instance; Upsert for external population
- `pkg/registry/memory.go` — MemoryRegistry implements DirectorySeeder via
  SeedDirectory: scans all non-retired records on startup to populate directory
- `pkg/identity/identity.go` — optional TenantName field on LocalRef; not used
  for equality/routing, used for directory population when name is known at call site
- `pkg/registry/directory_test.go` — 10 directory tests: bootstrap, live
  registration/transfer/retire tracking, multiple tenants, not-found, invalidation,
  ListAll, Upsert+LocateOnInstance
- `pkg/server/server.go` — handleLocate now queries TenantDirectory; returns
  tenant/instance_url/tenant_id/entity_count/stable_until/first_seen/last_seen;
  503 if directory not initialised; TenantDirectory added to Server struct;
  localRefJSON includes TenantName field; toRef() passes TenantName through
- `pkg/server/server_errors_test.go` — 3 locate tests: found (200), not-found
  (404/NOLU-RG001), no-directory (503)
- `cmd/nolu/main.go` — TenantDirectory created and started before HTTP server;
  embedded proxy locator uses directory.Locate; identity import added

## 0.7.7 — 2026-05-08

### Added

- `pkg/hotswap/iolu.go` — subprocess invocation layer: ioluExport, ioluImport,
  ioluValidate, runMigration, runValidation; empty DB paths → graceful skip (nil);
  bad binary path → error → rollback; validation exit 1 → ValidationResult{Valid:false}
- `pkg/hotswap/hotswap.go` — HotswapOptions gains SourceDBPath, TargetDBPath,
  ArchivePath, IoluBinary; TenantInvalidator interface (avoids circular import)
- `pkg/hotswap/hotswap_coverage_test.go` — 8 new tests closing coverage gaps:
  multi-tenant isolation in cutover (t1 transferred, t2 stays on source),
  NoDB graceful skip to complete, bad iolu binary → rollback + entity stays on
  source, InvalidateTenant called during quiesce (mockInvalidator), abort during
  preparing → rollback + entity preserved, Confirm drives full pipeline to complete,
  history contains all 7 states, CompletedAt set on completion

### Fixed

- `pkg/hotswap/manager.go` — MemoryManager.driveMigration and driveValidation
  now call runMigration/runValidation instead of simulating success
- `pkg/hotswap/xolu.go` — XoluHotswapManager same fix
- `pkg/hotswap/manager.go` — MemoryManager.NewMemoryManager gains dir parameter
- `pkg/hotswap/xolu.go` — NewXoluHotswapManager gains dir parameter
- `pkg/registry/registry.go` — ListByInstanceAndTenant added to Registry interface
- `pkg/registry/memory.go` — MemoryRegistry implements ListByInstanceAndTenant
- `pkg/registry/xolu.go` — XoluRegistry implements ListByInstanceAndTenant;
  xoluRecord gains current_tenant_id flat field for OQL filtering
- `pkg/registry/registry_test.go` — TestMemoryRegistry_ListByInstanceAndTenant
  (package-qualified call fixed)
- All hotswap managers now use ListByInstanceAndTenant instead of filterByTenant
  no-op; multi-tenant hub hotswaps are now correct, not merely safe

## 0.7.8 — 2026-05-08

### Added

- `cmd/demo5/main.go` — cross-organisation asset transfer demo: 3 sovereign xolu
  instances (manufacturer, distributor, repair depot), XoluRegistry backing,
  5 devices manufactured and registered; bilateral sale with one rejection;
  repair cycle with history portability negotiation; retirement with 410 verify;
  final audit resolving all 5 GlobalIDs
- `cmd/demo6/main.go` — live hotswap with traffic continuity demo: 2 xolu hubs,
  XoluRegistry backing, embedded hotswap manager with tenant directory;
  10 devices registered on hub-a; background reader at 20 req/s through proxy;
  operator-confirmed hotswap (auto_advance=false); real-time state progression
  printed as machine advances; read error count (expected: 0); final verification
  all 10 GlobalIDs resolve to hub-b
- `docker-compose.yml` — demo5 and demo6 profiles with xolu services
- `Dockerfile.nolu` — standalone nolu service image for demo6
- `Dockerfile` — nolu-demo5 and nolu-demo6 binaries added
- `Makefile` — demo5/demo6 run, docker, up/down, and compound targets
- `pkg/xoluclient/client.go` — BaseURL() and TenantName() accessor methods
