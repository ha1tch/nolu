# Adapted Tables for Schema-ful Entities

**Version:** 0.1.0-draft
**Author:** haitch &lt;h@ual.fi&gt;
**Date:** February 2026
**Status:** Design proposal

---

## 1. Problem Statement

olu stores all entity data in a single `entities` table as serialised JSON in
a `data TEXT` column. Every field-level query operation — WHERE predicates,
ORDER BY, aggregation — requires SQLite's `json_extract()` function to parse
the blob at runtime, once per row, per field reference.

This design is correct and flexible. It is also slow when the dataset grows.
Benchmarks on the current codebase show:

| Query type | 10,000 rows | 50,000 rows |
|---|---|---|
| Filtered scan (`age > 50`) | 27ms blob vs 10ms adapted (2.6x) |  186ms vs 60ms (3.1x) |
| Sort with LIMIT 20 | 11ms vs 1.5ms (7.4x) | 93ms vs 11ms (8.6x) |
| Indexed point lookup (`email = ?`) | 10ms vs 0.2ms (45.7x) | 88ms vs 0.7ms (124.4x) |

The blob layout's cost comes from two sources: `json_extract()` call overhead
per row per field, and `json.Unmarshal` in Go to rehydrate results. Both are
eliminated when fields are stored as native columns.

For entity types with a declared JSON Schema, olu already knows the full type
structure at startup. This document proposes using that knowledge to generate
per-entity-type tables with native columns, while preserving the blob layout
for schema-less entities.


## 2. Design Goals

1. **Schema-ful entities get native columns.** When a JSON Schema is present
   for an entity type, olu creates a dedicated table (`olu_{entity}`) with one
   column per declared property, typed to match the schema.

2. **Schema-less entities are unchanged.** Entity types without a schema
   continue to use the `entities` table with the `data TEXT` blob. No
   migration is forced.

3. **The REST API contract is unchanged.** Clients send and receive JSON.
   The adapted table is an internal storage optimisation; the HTTP interface
   is identical for both layouts.

4. **The Store interface is unchanged.** CRUD methods continue to accept and
   return `map[string]interface{}`. The adapted table logic lives inside the
   SQLite store implementation, not in the interface.

5. **OQL and Sulpher benefit transparently.** The OQL planner's push-down
   path generates column references instead of `json_extract()` calls. No
   changes to the parser or planner; only the SQL dialect layer.

6. **Backend portability.** The design must support a future PostgreSQL
   backend. All backend-specific SQL is confined to the dialect abstraction.
   No SQLite-specific assumptions in the table management, CRUD, or query
   layers.

7. **Fixed-point decimal support.** Financial and sensor data require exact
   decimal arithmetic. The adapted table layout introduces a `decimal` type
   with configurable precision and scale, backed by `shopspring/decimal` for
   validation and normalisation.


## 3. Schema-to-Table Mapping

### 3.1 Type Correspondence

JSON Schema properties map to native column types. The mapping is
dialect-specific, emitted by a new `ColumnType()` method on `SQLDialect`.

| JSON Schema | `format` | SQLite | PostgreSQL |
|---|---|---|---|
| `"string"` | — | `TEXT` | `TEXT` |
| `"integer"` | — | `INTEGER` | `INTEGER` |
| `"number"` | — | `REAL` | `DOUBLE PRECISION` |
| `"number"` | `"decimal"` | `INTEGER` | `NUMERIC(p,s)` |
| `"boolean"` | — | `INTEGER` | `BOOLEAN` |
| `"array"` | — | `TEXT` | `JSONB` |
| `"object"` | — | `TEXT` | `JSONB` |

Properties of type `"array"` or `"object"` remain serialised. They are stored
in the column as JSON text (SQLite) or JSONB (PostgreSQL). Field-level queries
on these columns still use `json_extract()` / `->>`; the adapted table
provides no benefit for nested structures. This is expected — the performance
gain comes from flat scalar fields, which are the majority of what WHERE and
ORDER BY clauses reference.

### 3.2 REF Fields

olu's REF convention stores references as objects:

```json
{"type": "REF", "entity": "users", "id": 42}
```

In the adapted table, a property declared as `"type": "object"` with
`"format": "ref"` (a new convention) produces two columns with a `REF_`
prefix:

```sql
REF_{field}_entity TEXT,
REF_{field}_id     INTEGER
```

The `REF_` prefix serves three purposes: it makes reference columns
immediately identifiable in DDL output (e.g. `PRAGMA table_info`,
`\d` in PostgreSQL); it prevents naming collisions with regular fields
that might end in `_entity` or `_id`; and it groups all reference columns
together in alphabetical listings.

The graph edge sync logic (`syncGraphEdges`) already decomposes REF objects
into source/target pairs. The adapted table stores the same information in
columns, enabling direct joins and indexed lookups on reference targets
without JSON parsing.

Schema declaration for a REF field:

```json
{
  "author": {
    "type": "object",
    "format": "ref",
    "properties": {
      "entity": {"type": "string"},
      "id": {"type": "integer"}
    }
  }
}
```

### 3.3 The Overflow Column

When `additionalProperties` is not explicitly `false` in the schema (olu's
current default), entities may contain fields not declared in the schema.
These must be stored somewhere.

The adapted table includes a `_extra TEXT` column that holds a JSON object
containing all fields not mapped to named columns. On write, the CRUD layer
partitions the incoming `map[string]interface{}` into known columns and
remainder; the remainder is serialised into `_extra`. On read, the named
columns and `_extra` are merged back into a single map.

If `_extra` is `NULL` or `"{}"`, there are no overflow fields. Queries
against overflow fields use `json_extract(_extra, '$.field')` (SQLite) or
`_extra->>'field'` (PostgreSQL), which is no worse than the current blob
layout and only affects the rare case of querying untyped fields.

If `additionalProperties` is explicitly `false`, the `_extra` column is
omitted and any unrecognised fields are rejected at validation time (before
reaching the store).

### 3.4 System Columns

Every adapted table includes system columns that mirror the `entities` table:

```sql
CREATE TABLE olu_{entity} (
    id          INTEGER PRIMARY KEY,
    tenant_id   INTEGER NOT NULL DEFAULT 0,
    -- ... schema-derived columns ...
    _extra      TEXT,                           -- overflow (nullable)
    _version    INTEGER NOT NULL DEFAULT 1,     -- optimistic concurrency
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

The `tenant_id` column supports multi-tenant scoping with the same semantics
as the blob table. In strict mode, all queries include `AND tenant_id = ?`.


## 4. The Decimal Type

### 4.1 Motivation

Financial data (transaction amounts, balances, exchange rates) and
high-precision sensor data require exact decimal representation. IEEE 754
floating-point (`REAL` / `DOUBLE PRECISION`) introduces rounding errors:
`0.1 + 0.2 ≠ 0.3`. This is unacceptable for financial calculations.

### 4.2 Wire Format

Decimal values are transmitted as JSON strings to avoid precision loss in
`json.Unmarshal` (which decodes numbers as `float64`):

```json
{"amount": "12345.6789", "currency": "USD"}
```

The validator accepts both string and number representations but normalises
to string internally. If the JSON contains `"amount": 123.45` (a number),
the validator converts it to `"123.45"` via `shopspring/decimal` before it
reaches the store.

### 4.3 Schema Declaration

```json
{
  "amount": {
    "type": "number",
    "format": "decimal",
    "decimalPrecision": 18,
    "decimalScale": 4
  }
}
```

- `decimalPrecision`: total significant digits (default: 18).
- `decimalScale`: digits after the decimal point (default: 4).

If `format` is `"decimal"` but precision/scale are omitted, the defaults
apply. The defaults cover most financial use cases (up to 99,999,999,999,999.9999).

### 4.4 Storage

| Backend | Column type | Stored as |
|---|---|---|
| SQLite | `INTEGER` | Scaled integer: value × 10^scale |
| PostgreSQL | `NUMERIC(p,s)` | Native fixed-point; the driver handles conversion |

SQLite has no native decimal type. The value is stored as a scaled
integer (the "money in cents" pattern). `19.90` at scale 2 becomes
`1990`. `shopspring/decimal` handles the scaling with exact arithmetic.
The maximum precision is 18 digits (int64 range).

### 4.5 Query Behaviour

Because decimal columns are stored as INTEGER in SQLite, comparison and
sorting work natively with correct numeric ordering across the full
signed range:

```sql
-- Range query on a decimal field (SQLite)
WHERE amount BETWEEN ? AND ?

-- ORDER BY decimal field (SQLite)
ORDER BY amount
```

OQL translates decimal literal values to scaled integers when emitting
queries against decimal columns. No CAST is needed.

For PostgreSQL, `NUMERIC` columns compare and sort natively with full
precision.

### 4.6 Aggregation

OQL aggregations (`SUM`, `AVG`) on decimal fields use `shopspring/decimal`
arithmetic in the Go-side executor. This produces exact results regardless
of backend:

```go
// In the OQL executor, when aggregating a decimal-typed field:
sum = sum.Add(decimal.RequireFromString(row["amount"].(string)))
```

The push-down path for aggregations on decimal columns is backend-specific:
PostgreSQL can push `SUM(amount)` natively with full precision. SQLite must
either aggregate in Go (exact) or push `SUM(CAST(amount AS REAL))` (lossy).
The planner should prefer Go-side aggregation for decimal fields on SQLite
unless the dataset is large enough that the performance trade-off is
worthwhile and the precision loss is documented.

### 4.7 Dependency

Fixed-point arithmetic uses `github.com/shopspring/decimal`. This library
has zero transitive dependencies and is widely used in financial Go
applications. It is a direct dependency, not indirect.


## 5. CRUD Operations

### 5.1 Dispatch

The SQLite store determines the storage strategy per entity type at CRUD
time by checking whether the validator has a schema for the entity:

```
validator.HasSchema(entity)
  → true:  use adapted table path (olu_{entity})
  → false: use blob table path (entities)
```

This check is O(1) (map lookup with RLock) and adds negligible overhead.

### 5.2 Create

Adapted path:

1. Validate the incoming data against the schema (unchanged).
2. Partition the data into schema columns and overflow:
   - For each key in the map, check if it is a declared schema property.
   - Declared properties go into named column values.
   - Undeclared properties go into the `_extra` map.
   - Decimal-typed properties are normalised via `shopspring/decimal`.
   - REF-typed properties are decomposed into `REF_{field}_entity` and
     `REF_{field}_id` column values.
3. Serialise `_extra` to JSON (or NULL if empty).
4. Generate an `INSERT INTO olu_{entity} (col1, col2, ...) VALUES (?, ?, ...)`
   statement. Column list and placeholder count are derived from the schema
   at table creation time and cached.
5. Execute within the existing transaction (same as blob path).
6. Sync graph edges (unchanged — the decomposed REF values are available
   from step 2).
7. Index for FTS (unchanged — the concatenated text content is derived from
   the data map, not the storage format).

Blob path: unchanged.

### 5.3 Get

Adapted path:

1. `SELECT col1, col2, ..., _extra, _version FROM olu_{entity} WHERE id = ? AND tenant_id = ?`
2. Scan columns into typed variables.
3. Assemble `map[string]interface{}` from column values.
4. If `_extra` is non-null and non-empty, unmarshal and merge into the map.
5. Add `"id"` to the map (same as blob path).

Blob path: unchanged.

### 5.4 Update / Save

Adapted path:

1. Partition data (same as Create step 2).
2. Generate `UPDATE olu_{entity} SET col1 = ?, col2 = ?, ..., _extra = ?, _version = _version + 1, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND tenant_id = ?`.
3. Check `_version` for optimistic concurrency (same as blob path).

Blob path: unchanged.

### 5.5 Patch

Adapted path:

1. Read the existing row (adapted Get).
2. Merge the patch data into the existing map.
3. Re-partition and validate the merged result.
4. Write back (adapted Update).

This is the same logical flow as the blob path, but the read and write
use column access instead of JSON serialise/deserialise.

### 5.6 Delete

Adapted path: `DELETE FROM olu_{entity} WHERE id = ? AND tenant_id = ?`.
Graph edge cleanup and FTS de-indexing are unchanged.

Blob path: unchanged.

### 5.7 List and ListPaged

Adapted path:

1. `SELECT col1, col2, ..., _extra FROM olu_{entity} WHERE tenant_id = ? LIMIT ? OFFSET ?`
2. Assemble maps from rows (same as Get, in a loop).

Blob path: unchanged.

The `PagedLister` interface is naturally satisfied by the adapted path since
the table has native `LIMIT`/`OFFSET` support without `json_extract`.


## 6. Dialect Extensions

### 6.1 New Methods on SQLDialect

The existing `SQLDialect` interface is extended with methods for adapted
table support. The extensions are additive — existing implementations
continue to work for blob-mode queries.

```go
type SQLDialect interface {
    // ... existing methods (unchanged) ...

    // ColumnDef returns the column definition SQL for a schema property.
    // jsonType is "string", "integer", "number", "boolean", "array", "object".
    // format is "" or "decimal" or "ref".
    // precision and scale apply only when format is "decimal".
    ColumnDef(name, jsonType, format string, precision, scale int) string

    // FieldExpression returns the SQL expression for reading a field.
    // In blob mode: json_extract(data, '$.field') or CAST variant.
    // In adapted mode: bare column name (INTEGER columns for decimals
    // compare and sort natively).
    // isDecimal indicates whether the field has format "decimal".
    FieldExpression(fieldName string, adapted, isDecimal bool) string

    // OverflowField returns the SQL expression for reading a field from
    // the overflow column.
    // SQLite:    json_extract(_extra, '$.field')
    // Postgres:  _extra->>'field'
    OverflowField(fieldName string) string

    // AdaptedBaseQuery returns the base SELECT for an adapted table.
    // Returns the SQL fragment, column list, and the initial arguments.
    AdaptedBaseQuery(entity string, columns []string) (sql string, args []interface{})
}
```

### 6.2 SQLite Implementation

```go
func (d *SQLiteDialect) ColumnDef(name, jsonType, format string, precision, scale int) string {
    switch {
    case format == "decimal":
        return name + " INTEGER"
    case format == "ref":
        return "REF_" + name + "_entity TEXT, REF_" + name + "_id INTEGER"
    case jsonType == "string":
        return name + " TEXT"
    case jsonType == "integer":
        return name + " INTEGER"
    case jsonType == "number":
        return name + " REAL"
    case jsonType == "boolean":
        return name + " INTEGER"
    default: // array, object
        return name + " TEXT"
    }
}

func (d *SQLiteDialect) FieldExpression(fieldName string, adapted, isDecimal bool) string {
    if adapted {
        return fieldName // INTEGER columns compare and sort natively
    }
    // Blob mode (existing behaviour)
    return "json_extract(data, '$." + fieldName + "')"
}

func (d *SQLiteDialect) OverflowField(fieldName string) string {
    return "json_extract(_extra, '$." + fieldName + "')"
}
```

### 6.3 PostgreSQL Implementation (Future)

```go
func (d *PostgresDialect) ColumnDef(name, jsonType, format string, precision, scale int) string {
    switch {
    case format == "decimal":
        return fmt.Sprintf("%s NUMERIC(%d,%d)", name, precision, scale)
    case format == "ref":
        return "REF_" + name + "_entity TEXT, REF_" + name + "_id INTEGER"
    case jsonType == "string":
        return name + " TEXT"
    case jsonType == "integer":
        return name + " INTEGER"
    case jsonType == "number":
        return name + " DOUBLE PRECISION"
    case jsonType == "boolean":
        return name + " BOOLEAN"
    default: // array, object
        return name + " JSONB"
    }
}

func (d *PostgresDialect) FieldExpression(fieldName string, adapted, isDecimal bool) string {
    if adapted {
        return fieldName // NUMERIC columns compare natively
    }
    return "data->>'" + fieldName + "'"
}

func (d *PostgresDialect) OverflowField(fieldName string) string {
    return "_extra->>'" + fieldName + "'"
}
```


## 7. OQL Integration

### 7.1 Planner Changes

The OQL planner (`pkg/oql/planner.go`) currently decides whether to push
operations to the storage engine based on `Queryable.Capabilities()` and
entity cardinality. For adapted tables, the push-down threshold should be
lower (effectively 0) because there is no `json_extract` overhead — every
field access is a native column read.

The planner needs one new piece of information: whether the target entity
uses an adapted table. This is provided by extending the `Queryable`
interface:

```go
type Queryable interface {
    // ... existing methods ...

    // IsAdapted reports whether the given entity type uses an adapted
    // table layout (per-column storage) rather than the blob layout.
    IsAdapted(ctx context.Context, entity string) bool
}
```

When `IsAdapted` returns true, the planner:

1. Sets the push-down threshold to 0 (always push).
2. Passes `adapted=true` to the SQL generator.
3. The generator calls `dialect.FieldExpression(field, true, isDecimal)`
   instead of `dialect.JSONField(field)`.

### 7.2 SQL Generator Changes

The `GenerateSQL` function receives an additional parameter indicating
adapted mode. In adapted mode:

- `BaseQuery` emits `SELECT {columns}, _extra FROM olu_{entity} WHERE tenant_id = ?`
  instead of `SELECT data, _version FROM entities WHERE entity_type = ?`.
- Field references use `FieldExpression` instead of `JSONField`.
- Fields not in the schema are handled via `OverflowField`.

The generator needs access to the schema's property list to distinguish
declared fields from overflow fields. This is provided as a
`map[string]FieldInfo` where `FieldInfo` carries the JSON Schema type and
format.

### 7.3 Result Reassembly

The `QueryWithPlan` method currently expects rows containing `(data TEXT, _version INTEGER)`
and unmarshals `data` via `json.Unmarshal`. For adapted tables, it receives
individual columns. A new `QueryWithPlanAdapted` method (or a mode flag on
the existing method) handles the column-to-map assembly.

This is the same reassembly logic as the adapted Get path (Section 5.3) but
applied in a loop across result rows.


## 8. Schema Evolution

### 8.1 Design Principle

Schema changes to adapted tables follow a **rebuild-and-swap** strategy.
This is the safest approach for two reasons:

1. SQLite's `ALTER TABLE` is limited. It cannot change column types, and
   `DROP COLUMN` was only added in 3.35.0 (the modernc.org/sqlite v1.29.0
   build may not expose it reliably).

2. Designing to SQLite's constraints means the same migration logic works
   on PostgreSQL without modification. PostgreSQL's richer `ALTER TABLE`
   is a potential optimisation, not a requirement.

### 8.2 Supported Operations

| Operation | Method | Data risk |
|---|---|---|
| Add field | `ALTER TABLE ADD COLUMN` | None (new column is NULL) |
| Remove field | Rebuild | Removed column's data is lost |
| Change field type | Rebuild | Conversion may fail |
| Rename field | Rebuild | Transparent to data |

"Rebuild" means: create a new table with the updated schema, copy data from
the old table with type conversions, drop the old table, rename the new table.
This is wrapped in a transaction.

### 8.3 Add Field (Fast Path)

Adding a field is common and cheap. It does not require a rebuild:

```sql
ALTER TABLE olu_{entity} ADD COLUMN {name} {type};
```

The new column is `NULL` for all existing rows. If the schema declares a
default value, a follow-up `UPDATE olu_{entity} SET {name} = ? WHERE {name} IS NULL`
applies it. Both statements execute within a single transaction.

Fields previously stored in `_extra` are not migrated automatically. They
continue to live in `_extra` until the next write to each row, at which
point the CRUD layer's partition logic (Section 5.2) moves them to the
named column. This is eventually consistent and requires no bulk migration.

### 8.4 Rebuild (General Case)

The rebuild procedure for remove, rename, and type-change operations:

1. Begin a transaction.
2. `CREATE TABLE olu_{entity}_new (...)` with the updated schema.
3. `INSERT INTO olu_{entity}_new SELECT {mapped_columns} FROM olu_{entity}`.
   The column mapping handles renames, type conversions, and column removal.
   Type conversions use `CAST` for safe cases (integer to text) and fail
   explicitly for unsafe cases (text to integer where the text is not
   numeric).
4. `DROP TABLE olu_{entity}`.
5. `ALTER TABLE olu_{entity}_new RENAME TO olu_{entity}`.
6. Recreate indexes.
7. Commit.

If any step fails, the transaction rolls back and the original table is
untouched.

### 8.5 Schema Version Tracking

Each adapted table has a corresponding row in a `table_schemas` metadata
table:

```sql
CREATE TABLE IF NOT EXISTS table_schemas (
    entity_type  TEXT PRIMARY KEY,
    schema_hash  TEXT NOT NULL,     -- SHA-256 of canonical schema JSON
    column_spec  TEXT NOT NULL,     -- JSON: ordered list of {name, type, format, ...}
    created_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at   TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);
```

On startup, olu compares the in-memory schema's hash against the stored
hash. If they differ, it computes a diff (added/removed/changed columns)
and applies the appropriate migration strategy (add-column fast path or
full rebuild).

### 8.6 Schema Downgrade Protection

A removed field's data is irrecoverable after a rebuild. To guard against
accidental schema changes:

1. Schema changes are logged at WARN level with the diff.
2. Destructive changes (remove, type change) require an explicit
   confirmation flag: `OLU_SCHEMA_ALLOW_DESTRUCTIVE=true`. Without it,
   olu logs an error and refuses to start if a destructive migration is
   detected.
3. The pre-migration table is exported as a backup (via the existing
   SQLite backup mechanism) before the rebuild begins.


## 9. Table Lifecycle

### 9.1 Table Creation

When olu starts (or when a schema is registered at runtime via
`POST /api/v1/schema/{entity}`):

1. Check `table_schemas` for existing adapted table.
2. If no entry exists:
   a. Derive column definitions from the schema.
   b. Execute `CREATE TABLE IF NOT EXISTS olu_{entity} (...)`.
   c. Create indexes on fields commonly used in WHERE/ORDER BY (heuristic:
      fields declared `required`, fields with constraints, REF fields).
   d. If the blob table contains existing data for this entity type, migrate
      rows from `entities` to the adapted table.
   e. Record the schema in `table_schemas`.
3. If an entry exists and the hash matches: no action.
4. If an entry exists and the hash differs: schema evolution (Section 8).

### 9.2 Data Migration from Blob to Adapted

When an entity type transitions from schema-less to schema-ful (or when
olu upgrades to a version with adapted table support):

1. Count rows in `entities WHERE entity_type = ?`.
2. If zero: skip (the adapted table is ready for new data).
3. If non-zero:
   a. Read rows in batches of 1000.
   b. For each row, unmarshal JSON, partition into columns + overflow,
      insert into the adapted table.
   c. After all rows are migrated, delete the blob rows.
   d. All within a transaction.

This migration is idempotent — the adapted table's `INSERT OR IGNORE`
prevents duplicates if the migration is interrupted and restarted.

### 9.3 FTS Integration

The FTS virtual table (`entities_fts`) is entity-type-agnostic. It indexes
concatenated text content regardless of storage layout. The FTS indexing
step in the CRUD path extracts searchable text from the `map[string]interface{}`
(which is available in both blob and adapted paths) and inserts/updates the
FTS table identically.

No changes to FTS are required.


## 10. Indexing Strategy

### 10.1 Automatic Indexes

When creating an adapted table, olu generates indexes based on schema
metadata:

| Condition | Index created |
|---|---|
| Field is in `required` array | Single-column index |
| Field has `format: "decimal"` | Single-column index |
| Field has `format: "ref"` | Index on `REF_{field}_id` |
| Field has `enum` constraint | Single-column index |

These heuristics target the most likely query patterns. The benchmarks show
that indexes on point-lookup fields (email, external IDs) provide the
largest benefit (45-124x), while indexes on low-selectivity fields (age
ranges, boolean flags) may hurt slightly due to the planner choosing an
index scan over a table scan.

### 10.2 Manual Index Hints

An optional `"x-olu-index"` extension in the JSON Schema property allows
explicit index control:

```json
{
  "email": {
    "type": "string",
    "x-olu-index": true
  },
  "status": {
    "type": "string",
    "x-olu-index": false
  }
}
```

When present, `x-olu-index` overrides the automatic heuristics.

### 10.3 Composite Indexes

Composite indexes (multi-column) are not generated automatically. They can
be declared with a table-level `x-olu-indexes` extension:

```json
{
  "x-olu-indexes": [
    {"columns": ["tenant_id", "status", "created_at"]}
  ]
}
```

This is a power-user feature. The automatic single-column indexes cover
most cases; composite indexes are for specific query patterns that profiling
identifies.


## 11. Configuration

### 11.1 Environment Variables

| Variable | Default | Description |
|---|---|---|
| `OLU_ADAPTED_TABLES` | `auto` | `auto`: use adapted tables for schema-ful entities. `off`: always use blob layout. `on`: require adapted tables (fail if schema is missing). |
| `OLU_SCHEMA_ALLOW_DESTRUCTIVE` | `false` | Allow destructive schema migrations (column removal, type changes). |

### 11.2 Per-Entity Override

The schema can include an `x-olu-storage` extension to override the global
setting for a specific entity type:

```json
{
  "x-olu-storage": "blob",
  "properties": { ... }
}
```

Valid values: `"adapted"` (force adapted), `"blob"` (force blob). If absent,
the global `OLU_ADAPTED_TABLES` setting applies.


## 12. Implementation Plan

### Phase 1: Table Generation and Basic CRUD (5 days)

**Scope:** Schema-to-DDL generation, Create/Get/Update/Delete on adapted
tables, dual-path dispatch in the SQLite store.

1. Extend `SQLDialect` with `ColumnDef`, `FieldExpression`, `OverflowField`.
2. Implement `tableSchemaManager` — derives DDL from JSON Schema, manages
   `table_schemas` metadata, compares hashes on startup.
3. Implement adapted Create (partition + column INSERT).
4. Implement adapted Get (column SELECT + map assembly).
5. Implement adapted Update/Save (partition + column UPDATE).
6. Implement adapted Patch (read + merge + write).
7. Implement adapted Delete.
8. Implement adapted List and ListPaged.
9. Dual-path dispatch: `if validator.HasSchema(entity) && config.AdaptedTables != "off"`.
10. Tests: adapted CRUD operations mirror existing blob tests.

**Deliverable:** All CRUD operations work against adapted tables. REST API
is indistinguishable from blob mode.

### Phase 2: OQL Integration (3 days)

**Scope:** Push-down path generates native column references for adapted
entities.

1. Add `IsAdapted()` to `Queryable` interface.
2. Modify planner to pass adapted flag and field metadata to the generator.
3. Modify `GenerateSQL` to use `FieldExpression` / `OverflowField`.
4. Implement `QueryWithPlanAdapted` (or adapt `QueryWithPlan` with a mode).
5. Tests: equivalence tests — same OQL query, same results, blob vs adapted.

**Deliverable:** OQL queries on adapted entities use native columns.

### Phase 3: Decimal Type (2 days)

**Scope:** Add `shopspring/decimal` dependency, extend validation,
implement decimal column handling.

1. Add `shopspring/decimal` to go.mod.
2. Extend `JSONSchemaValidator` to recognise `format: "decimal"` and
   validate with `decimal.NewFromString()`.
3. Normalise decimal values on write: scale to int64 via `shopspring/decimal`.
4. Extend `ColumnDef` for decimal type (INTEGER on SQLite, NUMERIC on Postgres).
5. Denormalise on read: divide by 10^scale, format with `StringFixed`.
6. Tests: decimal CRUD, validation (precision/scale enforcement), OQL
   queries on decimal fields, signed values, round-trip correctness.

**Deliverable:** Decimal fields store and query correctly.

### Phase 4: Schema Evolution (3 days)

**Scope:** Add-column fast path, rebuild-and-swap for destructive changes,
schema hash tracking, startup reconciliation.

1. Implement `table_schemas` metadata table.
2. Implement schema hash comparison on startup.
3. Implement add-column fast path.
4. Implement rebuild-and-swap for general case.
5. Implement `OLU_SCHEMA_ALLOW_DESTRUCTIVE` guard.
6. Tests: add field, remove field (with guard), type change, startup
   reconciliation.

**Deliverable:** Schema evolution is safe and automated.

### Phase 5: Blob-to-Adapted Migration (2 days)

**Scope:** Migrate existing blob data when a schema is added to an
existing entity type.

1. Implement batch migration (read blob, partition, insert adapted).
2. Implement idempotent cleanup (delete blob rows after migration).
3. Runtime migration via `POST /api/v1/schema/{entity}` (create schema →
   create adapted table → migrate data).
4. Tests: migrate 0 rows, 1 row, 1000 rows, interrupted migration.

**Deliverable:** Existing deployments can adopt adapted tables without
data loss.

### Phase 6: Benchmarks and Documentation (1 day)

1. Integrate `adapted_bench_test.go` into the benchmark suite.
2. Add comparative benchmarks for OQL queries (blob vs adapted).
3. Update MANUAL.md with adapted table documentation.
4. Update TESTING.md with new test counts.

**Total estimate:** ~16 days.


## 13. Risks and Mitigations

| Risk | Impact | Mitigation |
|---|---|---|
| Schema evolution bug corrupts data | High | Backup before rebuild; destructive guard; transaction rollback |
| Overflow column accumulates stale data | Low | Partition logic normalises on every write; background compaction is optional |
| `CAST(decimal AS REAL)` loses precision on comparison | Medium | Document the ~15 significant digit limit; recommend string equality for exact match; PostgreSQL has no issue |
| Adapted table diverges from blob during dual-mode operation | Medium | Single code path per entity type — never both simultaneously for the same entity |
| REF decomposition changes graph sync behaviour | Low | Same decomposition logic, different input source; tests verify equivalence |


## 14. What This Design Does Not Cover

- **PostgreSQL backend implementation.** The dialect methods are defined but
  only the SQLite implementation is built. PostgreSQL is a future phase.
- **Automatic index tuning.** The heuristic indexes are a starting point.
  Query-driven index recommendations (analyse slow queries, suggest indexes)
  are a separate feature.
- **Nested object flattening.** Only top-level scalar properties become
  columns. Nested objects remain JSON. Flattening `address.city` into an
  `address_city` column is not in scope; it raises naming collision issues
  and is better handled by schema redesign.
- **Partial schema adoption.** An entity is either fully adapted (all
  declared properties become columns) or fully blob. There is no mode where
  some properties are columns and others are deliberately left in the blob.
  The overflow column handles undeclared properties, but declared properties
  are always promoted to columns.


---

**Revision history:**

- **0.1.0** — Initial draft. Based on benchmark results (2.6x to 124x
  improvement across query types and dataset sizes) and design session
  covering type mapping, REF fields, overflow column, decimal type,
  schema evolution, OQL integration, and backend portability.
