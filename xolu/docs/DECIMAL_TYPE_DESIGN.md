# Decimal Type Support in olu

**Version:** 0.1.0  
**Date:** February 2026  
**Author:** haitch &lt;h@ual.fi&gt;  
**Status:** Draft  
**Applies to:** olu v0.9.x (adapted tables), queryfy v0.3.0+

---

## 1. Problem Statement

JSON has one numeric type: IEEE 754 double-precision floating point.
Go's `encoding/json` unmarshals all JSON numbers to `float64`, which
provides approximately 15–17 significant decimal digits. This is
insufficient for financial, metering, and scientific data that requires
exact fixed-point arithmetic.

The canonical illustration: `0.1 + 0.2 = 0.30000000000000004` in
float64. Over large datasets, these errors accumulate. A data store
that silently corrupts decimal precision is not fit for purpose in
domains where exactness matters.

olu must support fixed-point decimal values with:

- Exact storage with no precision loss on write or read
- Correct indexing and range query behaviour
- Correct aggregation (SUM, AVG) without float64 approximation
- Validation and normalisation on the write path
- Backend-agnostic design (SQLite today, PostgreSQL later)


## 2. Wire Format

Decimal values are transmitted as JSON strings, not JSON numbers.

```json
{
  "amount": "19.90",
  "tax_rate": "0.075"
}
```

**Rationale:** A JSON number like `19.9` is parsed by Go as
`float64(19.9)`, which is already an approximation of the true
decimal value. By using a string, the exact decimal representation
survives the JSON parse step intact. The validation layer then parses
the string with an arbitrary-precision decimal library, ensuring the
value is both syntactically valid and within the declared
precision/scale bounds.

Clients that send a JSON number for a decimal field receive a
validation error. This is intentional — silent float64 coercion is
the problem we are solving.


## 3. Schema Declaration

Decimal fields are declared in JSON Schema as:

```json
{
  "type": "string",
  "format": "decimal",
  "decimalPrecision": 12,
  "decimalScale": 2
}
```

Where:

- **type** is always `"string"` (see Section 2)
- **format** is `"decimal"` — a custom format recognised by olu
- **decimalPrecision** is the total number of significant digits
  (integer + fractional). Defaults to 18 if omitted.
- **decimalScale** is the number of digits after the decimal point.
  Defaults to 4 if omitted.

These custom properties flow through queryfy's `jsonschema.FromJSON`
converter with `StoreUnknown: true`, which stores them as schema
metadata via `Meta("decimalPrecision", N)` and
`Meta("decimalScale", N)`. The adapted table layer reads them back via
`FieldIntrospector.Meta()`.

**Relationship between precision and scale:** Precision includes scale.
A field with precision 6 and scale 2 stores values from `-9999.99` to
`9999.99` — four integer digits, two fractional digits, six total.
This follows the SQL `NUMERIC(p,s)` convention.


## 4. Validation

Validation happens at write time, before the value reaches the storage
layer. It is implemented as a queryfy transform registered during
`LoadSchema`.

The transform performs three checks in order:

1. **Parse:** The string is parsed with `shopspring/decimal` (or
   equivalent). If parsing fails, the value is rejected with
   `"invalid decimal value"`.

2. **Precision check:** The total number of significant digits in the
   parsed value must not exceed the declared precision. A value of
   `"100000.00"` against precision 6, scale 2 is rejected because it
   requires 8 digits (6 integer + 2 fractional), exceeding the 6-digit
   limit.

3. **Scale check:** Fractional digits beyond the declared scale are
   rejected, not silently truncated. A value of `"19.999"` against
   scale 2 is rejected. Silent truncation masks data entry errors.

If all checks pass, the transform normalises the value (scales to
integer — see Section 5) and returns the normalised string. The
normalised value is what reaches the CRUD layer.

**Where this runs:** The queryfy transform pipeline executes during
`Validate()` on the compiled `ObjectSchema`. The validator's
`LoadSchema` method post-processes the compiled schema to wrap decimal
fields in a transform closure that captures the field's precision and
scale from its metadata. This is analogous to the existing
`setDefaultAllowAdditional` post-processing step.


## 5. Normalisation

All decimal values are normalised to a **scaled integer** before
storage. The value is multiplied by `10^scale` to produce an integer
that preserves the exact decimal value.

For a field with precision 6 and scale 2:

| Client sends | Stored as | Notes |
|---|---|---|
| `"19.9"` | `1990` | 19.9 × 100 |
| `"19.90"` | `1990` | Same canonical form |
| `"0.5"` | `50` | |
| `"9999.99"` | `999999` | Maximum value |
| `"0"` | `0` | |
| `"-19.90"` | `-1990` | Negatives stored directly |
| `"-0.01"` | `-1` | |
| `"-9999.99"` | `-999999` | Most negative value |

This guarantees:

- **One canonical representation per value.** `"19.9"` and `"19.90"`
  both become `1990`.
- **Correct ordering and range queries.** SQLite INTEGER comparison
  gives correct numeric order for the full signed range, including
  negatives. `WHERE amount > -5000` and `ORDER BY amount` work
  correctly using the index.
- **Correct indexed range scans.** B-tree indexes on INTEGER columns
  support equality, range, and ORDER BY natively.
- **Readable with scale knowledge.** Seeing `1990` in the database
  and knowing the column has scale 2 immediately gives `19.90`. This
  is the same "money in cents" pattern used widely in financial
  systems.

### 5.1 int64 Range Limitation

The scaled integer is stored as a 64-bit signed integer. The maximum
value of int64 is `9223372036854775807` (approximately `9.2 × 10^18`).

For the default maximum precision of 18, the maximum scaled value is
`10^18 = 1000000000000000000`, which fits comfortably in int64. This
covers all practical use cases including large financial amounts with
high-precision fractional parts.

If a future need arises for precision beyond 18, the PostgreSQL
backend's `NUMERIC` type has no such limitation.


### 5.2 Denormalisation on Read

When returning values to clients, the stored integer is divided by
`10^scale` and formatted with the declared number of fractional digits.

`1990` with scale 2 → `"19.90"`. `-1` with scale 2 → `"-0.01"`.

Trailing fractional zeroes are preserved (they indicate the declared
scale). This is handled by `shopspring/decimal`'s `StringFixed` method.

Denormalisation happens in `DenormaliseDecimalColumns`, called on the
read path after SQL scan. The dialect controls whether denormalisation
is needed (see Section 8).


## 6. Storage

### 6.1 SQLite

Column type: `INTEGER`.

SQLite stores integers in a variable-length encoding (1–8 bytes
depending on magnitude), so small values like `1990` use less space
than `999999999999999999`. The adapted table layer maps
`format: "decimal"` to `dialect.ColumnType("string", "decimal",
precision, scale)`. The `SQLiteStorageDialect` returns `"INTEGER"` for
this combination.

Index behaviour: a standard B-tree index on the INTEGER column supports
equality, range queries, and ORDER BY correctly for the full signed
range. No special handling is needed for negative values.


### 6.2 PostgreSQL (future)

Column type: `NUMERIC(precision, scale)`.

PostgreSQL handles decimal values natively. No scaling, no
Go-side normalisation. The PostgreSQL dialect's `NormaliseDecimal`
method returns the value unchanged. PostgreSQL normalises internally.


## 7. Query and Aggregation

### 7.1 Range Queries (WHERE, ORDER BY)

**SQLite:** Range queries work natively on INTEGER columns. `WHERE
amount > 10000` (i.e., > 100.00 at scale 2) is correct. OQL translates
decimal literal values to scaled integers when emitting queries against
decimal columns.

**PostgreSQL:** Range queries work natively on `NUMERIC`. Same OQL
output, correct results.

### 7.2 Aggregation (SUM, AVG)

**SQLite:** SQLite's built-in `SUM()` on INTEGER columns produces
exact integer results, which is correct for scaled integers. However,
`AVG()` would require division and rounding awareness, and mixing
different scale factors would produce wrong results. For consistency
and correctness, aggregation is performed Go-side using
`shopspring/decimal`, which handles denormalisation and exact arithmetic
uniformly.

The approach: OQL detects decimal columns via the adapted table
metadata. For aggregate functions on decimal columns, OQL delegates
filtering to SQLite (`WHERE` clause, indexes) but performs the
aggregation step in Go using `shopspring/decimal` over the filtered
result set.

The dialect signals this capability via a method on `StorageDialect`.
When the dialect does not support native decimal aggregation, OQL
fetches the filtered values and aggregates in Go. When the dialect
does support it, OQL emits the aggregate function directly.

**PostgreSQL:** `SUM()` and `AVG()` on `NUMERIC` columns produce exact
results. OQL emits standard SQL aggregates. No Go-side computation.

### 7.3 Dialect Interface Extensions

Two methods added to `StorageDialect`:

```go
// NormaliseDecimal transforms a validated decimal string into a
// scaled integer string for storage. SQLite multiplies by 10^scale.
// PostgreSQL returns the value unchanged.
NormaliseDecimal(value string, precision, scale int) (string, error)

// DenormaliseDecimal transforms the stored representation back to
// a client-facing decimal string. SQLite divides by 10^scale.
// PostgreSQL returns the value unchanged.
DenormaliseDecimal(value string, precision, scale int) string

// SupportsNativeDecimalAggregation reports whether the backend
// handles SUM/AVG on decimal columns with exact arithmetic.
// If false, OQL aggregates in Go.
SupportsNativeDecimalAggregation() bool
```


## 8. Layer Summary

| Layer | SQLite | PostgreSQL |
|---|---|---|
| Wire format | `type: "string"`, decimal as string | Same |
| Validation | queryfy transform: parse, check bounds, normalise | queryfy transform: parse, check bounds, pass through |
| Column type | `INTEGER` | `NUMERIC(p,s)` |
| Write normalisation | Multiply by `10^scale` in Go → int64 | None (Postgres normalises) |
| Read denormalisation | Divide by `10^scale` in Go, format with fixed scale | None |
| Range queries | Native INTEGER comparison — correct for full signed range | Native on NUMERIC — correct |
| Aggregation | Go-side with `shopspring/decimal` (denormalises scaled ints) | SQL-side `SUM()`, `AVG()` — exact |
| OQL generation | Bare column for WHERE/ORDER; Go aggregation | Bare column for everything |


## 9. Dependencies

| Dependency | Purpose | Scope |
|---|---|---|
| `shopspring/decimal` | Parsing, validation, normalisation, Go-side aggregation | `pkg/storage`, `pkg/oql` |
| `queryfy` v0.3.0 | Schema metadata, transform pipeline, format validation | `pkg/validation`, `pkg/storage` |

`shopspring/decimal` is a well-maintained, widely-used Go library for
arbitrary-precision decimal arithmetic. It has no transitive
dependencies. It is used in two places: the validation transform
(write path) and the OQL aggregator (read path).


## 10. Implementation Sequence

1. **Add `shopspring/decimal` dependency.** `go get`.

2. **Implement decimal validation transform.** Register in the
   `LoadSchema` post-processing step alongside
   `setDefaultAllowAdditional`. Walks the compiled schema, finds
   string fields with `Meta("format", "decimal")`, wraps them in a
   transform that validates and normalises.

3. **Add `NormaliseDecimal` and `DenormaliseDecimal` to
   `StorageDialect`.** Implement for `SQLiteStorageDialect`
   (scale to int64 and format back). Stub for future PostgreSQL dialect
   (pass-through).

4. **Wire normalisation into adapted CRUD write path.** `adaptedCreate`
   and `adaptedUpdate` call `dialect.NormaliseDecimal` for decimal
   columns during the `PartitionData` step.

5. **Wire denormalisation into adapted CRUD read path.**
   `ReassembleData` calls `dialect.DenormaliseDecimal` for decimal
   columns.

6. **Add `SupportsNativeDecimalAggregation` to `StorageDialect`.**
   SQLite returns false.

7. **Teach OQL to handle decimal aggregation.** When emitting `SUM` or
   `AVG` on a decimal column and the dialect does not support native
   decimal aggregation, OQL fetches the column values and aggregates
   in Go.

8. **Tests.** Validation (valid, invalid, precision overflow, scale
   overflow), normalisation round-trip, storage round-trip, range query
   correctness, aggregation correctness.

Steps 1–5 are the core and can land together. Steps 6–7 are the OQL
integration and can follow.


## 11. Things Explicitly Out of Scope

- **Decimal arithmetic in OQL expressions.** `SELECT amount * 1.1`
  requires expression-level decimal awareness. Not needed for v0.9.x.
- **Currency types.** A currency is a decimal with an associated
  currency code. That is a higher-level concern built on top of
  decimal support, not part of it.
- **Configurable rounding modes.** Normalisation rejects values that
  exceed declared scale rather than rounding. Rounding policy is an
  application concern, not a storage concern.

---

**Revision history:**

- **0.3.0** — Switched to scaled integer storage. Decimal values
  stored as `int64` (value × `10^scale`) in SQLite INTEGER columns.
  Correct ordering, range queries, and indexing for the full signed
  range. Maximum precision 18 (int64 limit).
- **0.2.0** — Signed decimal support with `N`/`P` text prefix scheme
  (superseded by 0.3.0).
- **0.1.0** — Initial design (unsigned text-based, superseded).
