# OQL Adaptive Query Planner — Engineering Reference

**Version:** 2.0  
**Status:** Active  
**Introduced:** February 2026  
**Last verified:** 8 March 2026 (v0.9.7-patched41)

This document is the primary reference for anyone modifying, debugging,
or extending the query planner. It focuses on the things you cannot
easily read out of the code: invariants, semantic traps, dialect
differences, and known divergence risks between the two execution paths.

---

## 1. Two execution paths, one contract

Every OQL query can execute through two paths:

- **Go path**: `store.List()` → filter/sort/project in Go. The original
  path. Always correct. Used for small datasets and non-Queryable backends.

- **Push-down path**: Generate SQL with `json_extract()`, execute via
  `storage.Queryable.QueryWithPlan()`, then finish in Go for any
  operations that weren't pushed. Used when count >= threshold and the
  backend is Queryable.

**The contract**: both paths MUST produce identical results for every
query. The equivalence tests (`equivalence_test.go`) verify this with
2,000 seeded records across 26 query shapes. If you add a new expression
type or change the planner, add an equivalence test.

---

## 2. Three-language pipeline

This is the most important conceptual point in the subsystem. There are
three SQL dialects in play, and confusing them is the fastest way to
introduce bugs:

```
T-SQL (in)  →  OQL planner (decides)  →  Backend SQL (out)
tsqlparser       planner.go               sqlgen.go + dialect
```

| Stage | Dialect | Example |
|---|---|---|
| Parser input | T-SQL | `SELECT TOP 10 * FROM w WHERE status <> 'x'` |
| AST | Language-neutral | `*ast.SelectStatement` with `Top`, `Where`, `OrderBy` |
| Generator output (SQLite) | SQLite SQL | `SELECT data FROM entities WHERE entity_type = ? AND json_extract(data, '$.status') != ? LIMIT ?` |
| Generator output (Postgres, future) | PostgreSQL | `SELECT data FROM entities WHERE entity_type = $1 AND data->>'status' != $2 LIMIT $3` |

The AST is always T-SQL-shaped (e.g., `TOP` not `LIMIT`, `<>` not `!=`).
The `SQLDialect` interface translates these to backend-specific SQL. If
you are debugging a query, check which layer the problem is in:

- Wrong parse? → tsqlparser issue (upstream)
- Wrong push decision? → planner.go
- Wrong SQL output? → sqlgen.go or the dialect
- Wrong results? → semantic divergence (see section 5)

---

## 3. File map and responsibilities

```
pkg/storage/queryable.go       Interface: Queryable, QueryCapabilities
pkg/storage/sqlite.go          Implements Queryable (3 methods) on SQLiteStore
                               NOTE: compile-time check var _ Queryable = (*SQLiteStore)(nil)

pkg/oql/planner.go             Planner struct, Plan(), PlanMutation()
                               Decision logic, pushability checks
                               Debug logging via zerolog

pkg/oql/sqlgen.go              SQLDialect interface, SQLiteDialect
                               GenerateSQL() — SELECT push-down
                               generateMutationSQL() — UPDATE/DELETE push-down
                               Field name validation, type coercion

pkg/oql/executor.go            executeSelect: branches on plan.hasPush()
                               executeUpdate: branches on plan.pushed(PushWhere)
                               executeDelete: branches on plan.pushed(PushWhere)
                               Executor struct now carries *Planner and SQLDialect

pkg/oql/oql.go                 Engine constructor. Planner is wired through
                               Executor (NewExecutor auto-creates Planner).
                               Engine itself is unchanged.
```

---

## 4. Traps and gotchas

### 4.1 NOT without parentheses

tsqlparser parses `NOT status = 'deleted'` as `(NOT status) = 'deleted'`
— the NOT binds to the identifier, not the comparison. This produces an
`InfixExpression` with a `PrefixExpression` on the left side, which
`isSimpleField()` rejects, so push-down falls back to Go.

**Correct syntax**: `NOT (status = 'deleted')`.

This is a tsqlparser behaviour, not a bug. T-SQL precedence rules give
NOT lower precedence than comparison operators only when used with
parentheses. If tsqlparser is ever updated to change this, the planner
test `TestPlanner_WherePushability/not` will catch it.

### 4.2 List() orders by id; push-down does not (by default)

`SQLiteStore.List()` always appends `ORDER BY id`. The push-down path
only appends ORDER BY if `PushOrderBy` is in the plan. This means:

- For unordered queries (no ORDER BY in OQL), the Go path returns
  records in id order; the push-down path returns them in whatever
  order SQLite's query plan produces (which happens to be insertion
  order for simple WHERE scans, but this is NOT guaranteed).

- The equivalence tests use **order-independent comparison** for
  queries without ORDER BY. If you add a test that expects specific
  row ordering without ORDER BY, it will fail on the push-down path.

- This is fine because OQL does not guarantee any ordering unless
  ORDER BY is specified. But if downstream code depends on List()
  ordering, it must not run through the push-down path.

### 4.3 NULL comparison semantics

Go-side `compareValues(nil, nil)` returns 0 (equal). SQLite `NULL = NULL`
returns NULL (falsy). This means:

- `WHERE field = NULL` in Go would match null fields. In SQLite push-down,
  it would match nothing.
- This is not currently a problem because: (a) `NullLiteral` on the RHS
  of `=` is unlikely in practice — users write `IS NULL`; and (b) the
  `IsNullExpression` handler in both paths agrees on semantics.
- **However**: if you ever extend the push-down to support `= NULL`,
  you must translate it to `IS NULL` in the SQL generator, not leave it
  as `= ?` with a nil argument.

### 4.4 JSON number types after unmarshal

`json.Unmarshal` into `map[string]interface{}` produces `float64` for
all JSON numbers. Both `List()` and `QueryWithPlan()` use the same
unmarshal path (`json.Unmarshal` into `map[string]interface{}`), so
they agree on types. But be aware:

- An integer stored as `{"value": 42}` comes back as `float64(42.0)`.
- `CAST(json_extract(data, '$.value') AS REAL)` produces `42.0`.
- `toFloatSafe()` in the Go path handles `int`, `int64`, `float64`.
- After unmarshal, all values are `float64`, so the Go path's numeric
  comparisons work correctly.

**Trap**: If a future code path returns records with Go `int` or `int64`
values (not from JSON unmarshal), `chooseFieldExtraction` might apply
CAST when the Go path wouldn't. This hasn't happened yet because all
record data flows through JSON unmarshal.

### 4.5 LIKE case sensitivity

Both paths are case-insensitive for LIKE, but for different reasons:

- Go path: `matchLike()` lowercases both value and pattern explicitly.
- SQLite: LIKE is case-insensitive for ASCII by default.

**Divergence risk**: SQLite LIKE is only case-insensitive for ASCII
(A-Z). For Unicode characters (e.g., ü, ñ), SQLite LIKE is
case-sensitive unless the ICU extension is loaded. The Go path is
case-insensitive for all Unicode (via `strings.ToLower`). This means
a query like `WHERE name LIKE '%münchen%'` could return different
results on each path if the data contains `'München'`.

This is a known limitation. For v1, it is acceptable because the OQL
documentation does not guarantee Unicode-aware LIKE. If this becomes
a problem, the fix is to add `COLLATE NOCASE` to the SQLite LIKE or
load the ICU extension.

### 4.6 Graceful fallback masks errors

If `GenerateSQL()` fails, the executor silently falls back to the Go
path. This is by design (query still executes), but it means:

- A bug in the SQL generator might go unnoticed because queries still
  work — they just don't get push-down performance benefits.
- The equivalence tests catch this because they force push-down via
  threshold=1. If the SQL generator produces wrong SQL, the push-down
  executor returns an error or wrong results, and the test fails.
- In production, the only signal is the debug log. If you suspect
  push-down isn't working, check for planner debug log entries.

### 4.7 Threshold is dialect-owned and strict less-than

`count < threshold` means: for SQLite (threshold=50), count=49 → Go
path; count=50 → push-down.

The threshold is defined by `SQLDialect.DefaultThreshold()`, not a
global constant. Each backend knows its own overhead profile:

| Backend | Threshold | Rationale |
|---|---|---|
| SQLite (in-process) | **50** | Near-zero call overhead. Benchmarks show push-down wins at 100 records (1.5x) because JSON unmarshal dominates. |
| PostgreSQL (future) | **200–500** (est.) | Network round-trips, connection pooling, query parsing add fixed overhead. Needs benchmarking. |

The fallback `DefaultPushDownThreshold = 200` is used only when no
dialect is available (non-Queryable backends, where push-down never
triggers anyway).

### 4.8 The Executor auto-selects SQLiteDialect

In `NewExecutor()`, any Queryable store gets `SQLiteDialect`, and the
planner's threshold is derived from `SQLiteDialect.DefaultThreshold()`.
When a second backend arrives (e.g., Postgres), this must be replaced
with dialect selection logic. Options:

- Store provides its own dialect via an interface method
- A factory function maps store type → dialect
- The Engine constructor receives an explicit dialect

The current approach was chosen to avoid over-engineering before the
second backend exists.

### 4.9 GenerateSQL signature differs from the original plan

The implementation plan had `GenerateSQL(stmt, entity, tenantID, plan)`.
The actual signature is `GenerateSQL(stmt, entity, tenantID, plan, dialect)`.
The dialect parameter was added to support multiple backends. This is an
intentional deviation.

### 4.10 Tenant scoping in push-down SQL

When OQL executes via `ExecuteWithStore` (the standard multi-tenant path),
the executor extracts the numeric `TenantID` from the store's config and
injects it into the generated SQL as `AND tenant_id = ?`, targeting the
`tenant_id` INTEGER column in the `entities` table. This ensures push-down
queries cannot bypass tenant isolation even though `QueryWithPlan` executes
raw SQL.

For the legacy `ExecuteWithTenant` path (string-based tenant IDs), the
generator uses `json_extract(data, '$.tenant_id') = ?` instead. The
`isNumeric()` function in `sqlgen.go` distinguishes the two cases.

The Go-path fallback does not need additional tenant filtering when using
a scoped store because the store's `List` method already includes
`WHERE tenant_id = ?` in its SQL.

### 4.11 The matchLike() Go-side implementation is simplified

The Go path's `matchLike()` only handles `%prefix`, `suffix%`, and
`%contains%` patterns. It does not handle:

- `_` (single character wildcard)
- Patterns with `%` in the middle (e.g., `'abc%def'`)
- Escaped wildcards

SQLite's LIKE handles all of these correctly. This means for complex
LIKE patterns, the push-down path is actually more correct than the Go
path. The equivalence tests use only simple patterns. If complex LIKE
patterns are needed, `matchLike()` should be fixed first (the Go path
is the source of truth).

---

## 5. Semantic divergence risk matrix

| Area | Go path | Push-down path | Risk | Notes |
|---|---|---|---|---|
| NULL = NULL | Returns true | Returns false | Low | Users should use IS NULL; planner doesn't push = NULL |
| LIKE case (ASCII) | Case-insensitive | Case-insensitive | None | Both agree |
| LIKE case (Unicode) | Case-insensitive | Case-sensitive | Low | Documented limitation |
| LIKE complex patterns | Incomplete | Correct | Low | Go-side `matchLike` only handles simple patterns |
| Row order (no ORDER BY) | Deterministic (by id) | Non-deterministic | None | OQL doesn't guarantee order without ORDER BY |
| Numeric comparison | `toFloatSafe` → float64 | `CAST(... AS REAL)` | None | Both produce float64 semantics |
| String comparison | `fmt.Sprintf("%v", x)` | SQLite text comparison | Low | Edge cases with booleans or non-string types |
| Boolean TRUE/FALSE | Go `bool` → parameterised | SQLite receives Go bool → integer 1/0 | None | Tested in equivalence suite |
| NOT BETWEEN | Go: `!(low <= x <= high)` | SQLite: `NOT (x BETWEEN low AND high)` | None | Both tested |
| NOT IN | Go: `!(x in set)` | SQLite: `NOT (x IN (...))` | None | Both tested |
| `<>` operator | `compareValues != 0` | Normalised to `!=` in SQL | None | Both equivalent |

---

## 6. The Queryable interface contract

Any storage backend that wants push-down support must implement:

```go
type Queryable interface {
    Capabilities() QueryCapabilities
    CountEntities(ctx context.Context, entity string) (int, error)
    QueryWithPlan(ctx context.Context, sql string, args []interface{}) ([]map[string]interface{}, error)
}
```

**Critical**: `QueryWithPlan` must return records in the **same format**
as `List()`. Specifically:

- Each record is a `map[string]interface{}` produced by
  `json.Unmarshal([]byte(jsonData), &data)`.
- The `id` field is embedded in the JSON data (as `float64` after
  unmarshal).
- The query always selects `data` from the `entities` table, so the
  unmarshal is always from the JSON data column.

If a future backend stores data differently (e.g., JSONB in Postgres),
the `QueryWithPlan` implementation must normalise the output to match
this format.

---

## 7. The SQLDialect interface contract

```go
type SQLDialect interface {
    JSONField(fieldPath string) string
    JSONFieldNumeric(fieldPath string) string
    Placeholder(n int) string
    LimitClause(placeholder string) string
    BaseQuery(entity string) (string, interface{})
    Name() string
}
```

**Rules for implementing a new dialect:**

- `JSONField` returns a text-typed field extraction. For SQLite,
  `json_extract()` returns the natural JSON type; for Postgres,
  `->>` returns text.

- `JSONFieldNumeric` returns a numeric-typed extraction. Must produce
  a value that can be compared with `>`, `<`, `BETWEEN`, etc. The
  type must be float-compatible (REAL, NUMERIC, DOUBLE PRECISION).

- `Placeholder` must return the n-th placeholder (1-based). SQLite
  ignores the argument (always `?`); Postgres uses it for `$1`, `$2`.

- `BaseQuery` returns the initial SELECT statement and its first
  argument. The SQL generator appends AND clauses to this. The
  returned SQL must end in a state where `AND (...)` can be appended.

- `LimitClause` receives the placeholder string (e.g., `?` or `$3`)
  and returns the full LIMIT clause. For backends that use a different
  keyword (e.g., `FETCH FIRST ? ROWS ONLY`), override this.

- `DefaultThreshold` returns the minimum entity count for push-down.
  Derive from benchmarks on the actual backend. In-process engines
  (SQLite) have low overhead (~50). Networked engines (Postgres) have
  higher overhead due to round-trips and should return 200–500.

---

## 8. Adding a new pushable expression type

1. Add a case to `isWherePushable()` in `planner.go`.
2. Add a case to `translateExpr()` in `sqlgen.go`.
3. Add a table-driven test case to `TestPlanner_WherePushability`.
4. Add a table-driven test case to `TestSQLGen_Where`.
5. Add a `TestEquivalence_*` test in `equivalence_test.go`.
6. Run the full test suite: `go test ./... -tags integration -count=1`.

Do not skip step 5. The equivalence test is the only thing that verifies
both paths produce the same result for real data.

---

## 9. Adding a new storage backend with push-down

1. **Implement `storage.Queryable`** on the new store:
   - `Capabilities()` — report which operations the backend supports
   - `CountEntities(ctx, entity)` — return record count (must be fast)
   - `QueryWithPlan(ctx, sql, args)` — execute SQL and return
     `[]map[string]interface{}` in the same format as `List()`

2. **Create a dialect** in `pkg/oql/` (e.g., `dialect_postgres.go`):

   ```go
   type PostgresDialect struct{}

   func (d *PostgresDialect) JSONField(fieldPath string) string {
       return fmt.Sprintf("data->>'%s'", fieldPath)
   }

   func (d *PostgresDialect) JSONFieldNumeric(fieldPath string) string {
       return fmt.Sprintf("CAST(data->>'%s' AS NUMERIC)", fieldPath)
   }

   func (d *PostgresDialect) Placeholder(n int) string {
       return fmt.Sprintf("$%d", n)
   }

   func (d *PostgresDialect) LimitClause(placeholder string) string {
       return "LIMIT " + placeholder
   }

   func (d *PostgresDialect) BaseQuery(entity string) (string, interface{}) {
       return "SELECT data FROM entities WHERE entity_type = $1", entity
   }

   func (d *PostgresDialect) Name() string { return "postgres" }
   ```

3. **Wire the dialect** in `Executor.NewExecutor()`:
   - Currently auto-selects `SQLiteDialect` for any Queryable store
   - Extend with a type switch or store-provided dialect method

4. **Run equivalence tests** against the new backend to verify semantic
   correctness. The test infrastructure in `equivalence_test.go` can be
   adapted by swapping the store constructor.

---

## 10. Test architecture

```
planner_test.go          Unit tests for decision logic.
                         Uses mock stores (mockQueryableStore, mockPlainStore).
                         Tests thresholds, pushability, capability restrictions.
                         DOES NOT execute any SQL.

sqlgen_test.go           Unit tests for SQL generation.
                         Parses OQL, generates SQL, checks output string + args.
                         Verifies parameterisation, injection safety, coercion.
                         DOES NOT execute any SQL.

equivalence_test.go      Integration tests. Uses a real SQLiteStore.
                         Seeds 2,000 records (500 sensors, 500 readings,
                         500 assets, 500 events).
                         Two executors: goExec (threshold=MaxInt32, always Go)
                         and pdExec (threshold=1, always push-down).
                         Runs same query through both, compares results.
                         ORDER-INDEPENDENT comparison for unordered queries.
                         Order-dependent comparison for ORDER BY queries.

queryable_test.go        Storage-layer tests. Uses a real SQLiteStore.
                         Verifies interface satisfaction, CountEntities,
                         QueryWithPlan equivalence with List.
```

**Why two executors in equivalence tests**: The planner decides
automatically based on count vs threshold. To force both paths, we set
threshold=MaxInt32 (Go always wins) and threshold=1 (push-down always
wins). This is more reliable than mocking.

---

## 11. Performance characteristics

### Benchmark data (Xeon Platinum 8581C, SQLite in-process)

**WHERE only (status = 'critical', 25% selectivity)**

| Records | Go path | Push-down | Speedup | Memory reduction |
|---|---|---|---|---|
| 100 | 757 µs | 506 µs | 1.5x | 72% fewer allocs |
| 1K | 5.8 ms | 3.5 ms | 1.6x | 77% fewer allocs |
| 10K | 60 ms | 34 ms | 1.8x | 77% fewer allocs |
| 100K | 737 ms | 424 ms | 1.7x | 137 MB → 33 MB |
| 1M | 6.73 s | 3.54 s | 1.9x | 1.39 GB → 342 MB |

**WHERE + ORDER BY + TOP 10**

| Records | Go path | Push-down | Speedup | Memory reduction |
|---|---|---|---|---|
| 100 | 743 µs | 493 µs | 1.5x | 85% fewer allocs |
| 1K | 6.0 ms | 2.5 ms | 2.4x | 98% fewer allocs |
| 10K | 62 ms | 23 ms | 2.7x | 99.8% fewer allocs |
| 100K | 745 ms | 256 ms | 2.9x | 137 MB → 34 KB |
| 1M | 7.77 s | 2.39 s | 3.3x | 1.39 GB → 36 KB |

**Selective compound WHERE (sensor_id AND status)**

| Records | Go path | Push-down | Speedup |
|---|---|---|---|
| 100K | 737 ms | 253 ms | 2.9x |
| 1M | 6.78 s | 2.29 s | 3.0x |

**LIKE pattern (10% match rate — worst case for push-down)**

| Records | Go path | Push-down | Speedup |
|---|---|---|---|
| 1M | 6.84 s | 7.23 s | 0.9x (push-down loses) |

### Key observations

The Go path's cost is almost entirely `List()` — deserialising every
JSON record into `map[string]interface{}` before filtering. This cost
is proportional to the total record count, regardless of selectivity.

Push-down avoids this: SQLite filters first, and only matching records
are deserialised. The savings are dramatic when:

1. **Selectivity is good** (few rows match): push-down deserialises
   far fewer records.
2. **LIMIT is present**: push-down returns only N rows. At 1M records,
   TOP 10 reduces memory from 1.39 GB to 36 KB — a 38,000:1 ratio.
3. **Selectivity is poor** (many rows match): push-down barely wins or
   loses slightly, because most records are deserialised anyway and
   the `json_extract()` overhead is comparable to Go string comparison.

### Planner overhead

The planner adds two costs to every SELECT:

1. **CountEntities**: `SELECT COUNT(*) ... WHERE entity_type = ?` on an
   indexed column. Measured at <10µs for up to 1M records.
2. **Plan decision logic**: Pure Go, no I/O. Negligible (<1µs).

---

## 12. Configuration

The threshold is owned by the `SQLDialect` implementation. The planner
calls `dialect.DefaultThreshold()` at construction time. Currently:

- `SQLiteDialect.DefaultThreshold()` returns 50.
- `DefaultPushDownThreshold = 200` is the fallback for non-Queryable
  backends (where push-down never triggers, so the value is moot).
- `NewPlannerWithThreshold()` accepts an explicit override for testing.

If the threshold ever needs to be configurable at runtime:

1. Add a `PushDownThreshold` field to the olu config struct.
2. Pass it through `NewEngine` → `NewExecutor` → `NewPlannerWithThreshold`.
3. The planner is already designed for this — it stores `threshold int`.

---

## 13. What is NOT in v1

| Feature | Why deferred | When to add |
|---|---|---|
| JOIN push-down (adapted-to-adapted) | Requires relationship metadata (FK annotations between adapted entities), multi-table SQL generation, and qualified column resolution. Feasible — both entities live in the same SQLite DB. See `QUERY_OPTIMISATION_PROGRESS.md` for full analysis. | When cross-entity queries become a regular pattern. Breaks backend-agnostic contract (JSON backend cannot support JOINs without an in-memory join engine). |
| JOIN push-down (adapted-to-blob or blob-to-blob) | One or both sides lack extracted columns. Would require materialise-and-match in Go. | Only if adapted-to-adapted JOINs prove insufficient |
| Partial WHERE push-down | e.g., push `status = 'active'` and filter `UPPER(name) = 'FOO'` in Go | v2, if mixed pushable/non-pushable queries become common |
| Expression indexes | `CREATE INDEX ... ON entities(entity_type, json_extract(data, '$.field'))` | When any entity type regularly exceeds 50,000 records |
| JSONFile push-down | JSONFile has no query engine | Not planned |
| PostgreSQL dialect | Requires `data->>'field'`, `$N` placeholders, `CAST(... AS NUMERIC)` | When PostgreSQL storage backend is implemented |
