# Query Optimisation Progress Tracker

**Project:** olu v0.9.7-patched41
**Scope:** OQL executor performance — adapted tables (SQL push-down) and blob tables (jsonic)
**Started:** 2026-02-22
**Last updated:** 2026-03-08 (v0.9.7-patched41)


## Phase Status

| Phase | Description | Status | RC | Tests added | Key files |
|---|---|---|---|---|---|
| 0 | Abstraction cleanup | Done | rc16 | — | sqlgen_aggregate.go |
| A1 | Full SELECT push-down (adapted) | Done | rc16 | 44 | sqlgen_adapted.go, sqlgen_scalar.go, adapted_pushdown_test.go, adapted_full_pushdown_test.go |
| B1 | jsonic tokeniser package | Done | rc16 | 41 | pkg/jsonic/ (tokeniser, atoms, column_store, field_extractor) |
| A2 | Planner integration + dispatch refactor | Done | rc18 | 3 files updated | planner.go (PushFull, dialect field), executor.go (strategy switch) |
| B2 | FieldQueryable (jsonic into Store) | Done | rc18 | 20 | field_queryable.go, sqlite_field_query.go, field_query_test.go, field_query_e2e_test.go |
| A3 | Prepared statement cache | Done | rc19 | 10 | stmt_cache.go, sqlite.go (wiring), sqlite_aggregate.go, sqlite_field_query.go |
| B3 | Executor columnar path | Deferred | — | — | See design note below |
| B4 | Predicate push-down during tokenisation | Done | rc19 | 31 | jsonic/predicate.go, jsonic/filter_extract.go, predicate_compiler.go, executor.go (B4 path), field_queryable.go (FilterableStore), sqlite_field_query.go |
| A4 | Hardware-aware complexity gating | Done | v0.9.2 | 26 | complexity_estimator.go, complexity_profiles.go, complexity_planner.go, complexity_bench_test.go, complexity_planner_test.go, calibrate_test.go |

## Future work (pre-PostgreSQL)

The following items have no impact on current functionality. They are
prerequisites for adding a second storage backend, which is not planned.

| Item | Description | Effort | Trigger |
|---|---|---|---|
| WHERE CAST emission | Wire `dialect.CastExpression()` into WHERE push-down SQL generation (Rule 5). SQLite's type affinity handles this implicitly; PostgreSQL would not. | Small (1-2 ops) | PostgreSQL backend work begins |
| Tests against Store interface | Refactor comparative tests to use `Store`/`SQLDialect` interfaces instead of `*SQLiteStore` (Rule 6). | Medium (3-4 ops) | PostgreSQL backend work begins |
| PostgreSQL backend | `StorageDialect` implementation, connection pooling, JSONB extraction, native decimal aggregation. | Large | Business need for PostgreSQL |


## Future work: JOIN push-down (exploration)

OQL currently validates that queries reference exactly one table
(`validator.go`, line 137: `len(s.From.Tables) != 1`). Cross-entity
queries require multiple round trips through OQL and manual client-side
stitching, or use of the graph API for traversal.

Both adapted entities live in the same SQLite database as `olu_{entity}`
tables with extracted columns. SQLite can JOIN them natively. This
section captures the design analysis for potential JOIN support.

### What would be needed

| Component | Work | Complexity |
|---|---|---|
| Relationship metadata | Schema annotations declaring foreign-key mappings between adapted entities (e.g., `orders.customer_id` references `customers.id`). The graph layer knows *that* entities relate but not *how* at the field level. | New concept |
| Validator relaxation | Allow 2+ tables in FROM when all referenced entities are adapted and have declared relationships. | Small |
| SQL generation | Extend `sqlgen_adapted.go` to emit `FROM olu_orders JOIN olu_customers ON ...`. Mechanical extension of existing generators. | Medium |
| Planner extension | Complexity estimator must account for JOIN cost. The EXPLAIN-based approach extends naturally — SQLite reports nested loop vs index lookup for JOINs. | Medium |
| Alias/qualification | Column references must be qualified (`orders.amount`, `customers.name`) to avoid ambiguity. Requires AST-level awareness in projection, WHERE, ORDER BY, GROUP BY. | Medium |

### The backend-compatibility tension

Currently OQL is backend-agnostic: every query goes through the `Store`
interface (`List`, `Get`, `Search`), which both SQLite and JSON file
backends implement. Push-down is an optimisation *within* the SQLite
backend; the Go path produces identical results for JSON.

JOINs break this contract. There is no Go-path equivalent of "read
from two entities and correlate them by key" against the JSON backend
without building an in-memory join engine.

### Options evaluated

| Option | Description | Trade-off |
|---|---|---|
| 1. JOINs are SQLite-only | Validator rejects JOINs when backend is JSON | Fragments the query language — "OQL" means different things depending on config |
| 2. Build Go-path join | Nested-loop join in Go: List entity A, for each row look up entity B by key | Correct but slow; maintenance burden for a backend already 12x slower on simple searches |
| 3. Deprecate JSON backend | JOINs become the forcing function for SQLite-only | Clean but removes the zero-dependency mode useful for debugging and tiny deployments |

**Recommended path**: Option 1 (SQLite-only JOINs) with a long-term
lean toward option 3. The JSON backend retains value as a
zero-dependency mode, but trying to maintain feature parity across
both backends will drag development pace as query capabilities grow.

### Entity combination matrix

| Left | Right | JOIN feasibility |
|---|---|---|
| Adapted | Adapted | Full push-down — both have extracted columns in SQLite |
| Adapted | Blob | Partial — push adapted side, materialise, client-side match on blob |
| Blob | Blob | No push-down — neither side has columns to join on |

### Relationship with the graph layer

The graph layer retains its role for traversals that SQL JOINs handle
poorly: path finding, cycle detection, transitive closure, variable-
depth neighbourhood queries. JOINs address the flat correlational
case ("give me orders with their customer name") where three round
trips through OQL + graph is unnecessary overhead.

### Status

Exploration only. No implementation planned for v0.9.x. This section
exists to capture the analysis so it does not need to be re-derived.


## Architecture rules

Six rules govern how query optimisation code is structured. They exist
to keep the codebase backend-portable (SQLite today, PostgreSQL later)
and to maintain clean separation between layers.

| # | Rule | Rationale | Status |
|---|---|---|---|
| 1 | No SQL literals in pkg/oql/ | SQL generators use dialect methods for all syntax. Only SQL-92 aggregate/scalar names (COUNT, SUM, etc.) appear as string constants. Ensures a new dialect doesn't require editing pkg/oql/. | Compliant |
| 2 | Jsonic stays in pkg/storage/ or pkg/jsonic/ | The executor never imports jsonic. The Store interface abstracts field extraction; how it's implemented (jsonic tokenisation, `json_extract`, PostgreSQL `->>`) is the backend's business. | Compliant |
| 3 | Decimal handling via dialect | No code outside `StorageDialect` implementations may assume how decimals are stored. `NormaliseDecimal`, `DenormaliseDecimal`, and `SupportsNativeDecimalAggregation` are the only entry points. | Compliant |
| 4 | Parameterised placeholders | Every SQL generator uses `dialect.Placeholder(n)`. No hardcoded `?` or `$N`. | Compliant |
| 5 | Explicit type coercion | WHERE translators must emit `CAST()` when comparing values of different types (e.g. TEXT column vs INTEGER literal). The dialect provides `CastExpression(expr, targetType)`. Infrastructure is in place but not yet wired into all WHERE push-down paths. | Infrastructure only |
| 6 | Test against the interface | Comparative correctness tests should use `Store` and `SQLDialect` interfaces, not concrete types. This ensures the same test suite validates any backend. Currently tests use `*SQLiteStore` directly, which is acceptable while there is only one backend. | Partial |

Rules 1-4 are fully compliant. Rules 5-6 are pre-PostgreSQL hygiene —
they don't affect correctness on SQLite but would need addressing before
a second backend is added.


## Dependency graph

```
Phase 0 ──> A1 ──> A2 (done)
              \
               ──> A3 (done) ──> A4 (done)

B1 ──> B2 (done) ──> B3 (deferred)
                  \
                   ──> B4 (done)
```

All adapted-table optimisation phases (A-track) are complete through A4.
B4 (predicate push-down during tokenisation) is complete.
B3 (columnar executor) has been deferred — see design note below.

A4 (hardware-aware complexity gating) adds EXPLAIN-based cost estimation
to the adapted full push-down path. Complex queries (non-covering
aggregates, temp B-tree sorts) are gated against hardware-specific
thresholds derived from calibration benchmarks. Three preset profiles
(VPS, dedicated, bare-metal) and a runtime calibration function allow
the planner to make correct decisions on hardware ranging from 1-vCPU
containers to Apple M1 desktops.

Schema evolution is complete.


## B3 design note: deferral rationale

B3 proposed replacing the executor's internal data representation from
`[]map[string]interface{}` to a typed columnar format, eliminating
per-row map allocations and per-field `interface{}` boxing.

### Why it was deferred

B4 already delivers the primary win B3 was designed for: rows that fail
WHERE predicates never allocate a `map[string]interface{}` at all. The
remaining benefit of columnar — avoiding boxing for rows that *pass* —
is real but modest for olu's workload profile (sub-million row queries
against SQLite, where the Go path is itself a fallback behind SQL
push-down for adapted tables).

The cost is disproportionate. `map[string]interface{}` is not just an
internal executor detail — it is the return type of the `Store`
interface, the input/output type of the `Aggregator`, and the response
format in `Result.Rows`. A columnar rewrite touches every layer:

| Layer | Occurrences | Impact |
|---|---|---|
| `Store` interface | ~13 | Breaking change to all backends |
| `executor.go` | ~19 | Core query pipeline |
| `aggregator.go` | ~13 | GROUP BY, HAVING, all aggregates |
| `result.go` | 2 | API response type |
| `handlers.go` | ~54 | Every REST endpoint |
| `FieldQueryable`, `FilterableStore` | ~10 | B2/B4 interfaces |

Estimated effort: 24-32 operations (4-6 batches), touching ~30% of the
codebase, for an estimated 15-20% throughput improvement on the Go
fallback path only.

### What would need to be done to pick it up

1. **Define a `RecordBatch` type** — columnar storage with typed
   slices per field (e.g. `[]string`, `[]float64`, `[]bool`) plus a
   presence bitmap for nulls. This is the internal executor type.

2. **Rewrite executor internals** — `filterRecords`, `projectColumns`,
   `distinctRecords`, `materializeScalars`, `evalCondition`, `evalExpr`
   all operate on `RecordBatch` instead of `[]map[string]interface{}`.

3. **Rewrite the Aggregator** — `Aggregate`, `buildGroupKey`,
   `extractColumnValues`, `EvalCondition`, `evalExpr` all take
   `RecordBatch`. This is the highest-risk area (subtle correctness
   around type coercion and NULL handling in GROUP BY keys).

4. **Wire `Store`/`FieldQueryable` to return `RecordBatch`** — or,
   more pragmatically, keep the Store interface as-is and convert
   `[]map[string]interface{}` → `RecordBatch` at the executor entry
   point. This limits the blast radius to pkg/oql.

5. **Convert back to maps at the `Result` boundary** — `RecordBatch`
   → `[]map[string]interface{}` for `Result.Rows` and JSON
   serialisation. This is unavoidable unless the REST handlers are
   also rewritten.

6. **Update tests** — 100+ test functions construct or assert on
   `map[string]interface{}` records.

### Recommended scoped alternative

If profiling later shows the Go path is a bottleneck, a lighter
approach would be:

- Introduce `RecordBatch` as an **internal-only** type in pkg/oql
- Convert maps → batch at executor entry, batch → maps at exit
- Rewrite only `filterRecords` and `projectColumns` to use batch
- Leave Aggregator, Store interface, and handlers untouched

This delivers ~60% of the throughput benefit at ~20% of the cost,
and can be done incrementally without breaking any external interface.


## Test counts

| RC | Total tests | Delta |
|---|---|---|
| rc4 | 698 | Baseline |
| rc11 | ~827 | +129 (adapted CRUD, decimals) |
| rc16 | 1645 | +818 (push-down comparative, jsonic) |
| rc18 | 1665 | +20 (B2 FieldQueryable) |
| rc19 | 1706 | +41 (A3 StmtCache 10, B4 predicate push-down 31) |
| rc20 | 1718 | +12 (schema evolution: 7 diff unit, 5 migration integration) |
| v0.9.2 | 1744 | +26 (A4 hardware-aware complexity gating: 8 test functions, 26 subtests) |


## Bugs found during testing

| RC | Bug | Severity | Fix |
|---|---|---|---|
| rc16 | Decimal MIN/MAX string comparison | Medium | `toFloatSafe` in aggregator |
| rc18 | ListWithFields ignores empty field list | Low | Added fallback to List in sqlite_field_query.go |
