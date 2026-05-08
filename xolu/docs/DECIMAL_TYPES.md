# Decimal Types in olu

This guide explains how to use fixed-point decimal fields in olu for
financial, metering, and other data that requires exact numeric
precision.


## Why Decimals?

JSON numbers are IEEE 754 floating-point values. They cannot represent
all decimal fractions exactly. For example, `0.1 + 0.2` evaluates to
`0.30000000000000004` in floating-point arithmetic. For financial
amounts, inventory quantities, tax rates, and similar data, this
imprecision is unacceptable.

olu's decimal type stores values as exact fixed-point numbers with a
declared precision and scale. No precision is lost at any stage: not
on input, not in storage, not during aggregation.


## Declaring a Decimal Field

In a JSON Schema definition, declare a decimal field as a `string` with
`format: "decimal"`:

```json
{
  "type": "object",
  "properties": {
    "amount": {
      "type": "string",
      "format": "decimal",
      "decimalPrecision": 12,
      "decimalScale": 2
    },
    "tax_rate": {
      "type": "string",
      "format": "decimal",
      "decimalPrecision": 6,
      "decimalScale": 4
    }
  },
  "required": ["amount"]
}
```

The parameters:

- **decimalPrecision** — Total number of significant digits (integer
  part + fractional part). Defaults to 18 if omitted.
- **decimalScale** — Number of digits after the decimal point. Defaults
  to 0 if omitted (integer decimal).

Precision includes scale. A field with `decimalPrecision: 12` and
`decimalScale: 2` stores values with up to 10 integer digits and
exactly 2 fractional digits, covering the range `-9999999999.99` to
`9999999999.99`.


## Sending Decimal Values

Decimal values **must** be sent as JSON strings, not JSON numbers:

```json
{
  "amount": "19.90",
  "tax_rate": "0.0750"
}
```

**Correct:** `"amount": "19.90"` — exact decimal string.

**Wrong:** `"amount": 19.90` — this is a JSON number (float64), and
olu will reject it with a type validation error.

This is intentional. A JSON number like `19.9` is parsed by Go as
`float64(19.9)`, which is already an approximation. By requiring a
string, the exact representation is preserved end to end.


## What olu Does With Your Values

When you create or update an entity with decimal fields, olu:

1. **Validates** the string is a well-formed decimal number.
2. **Checks precision bounds** — rejects values that exceed the
   declared precision or scale.
3. **Normalises** the value to a scaled integer for storage (the value
   multiplied by `10^scale`).

Examples for a field with `decimalPrecision: 8, decimalScale: 2`:

| You send | Stored as | Returned as |
|---|---|---|
| `"19.9"` | `1990` | `"19.90"` |
| `"19.90"` | `1990` | `"19.90"` |
| `"0.5"` | `50` | `"0.50"` |
| `"100"` | `10000` | `"100.00"` |
| `"999999.99"` | `99999999` | `"999999.99"` |
| `"0"` | `0` | `"0.00"` |
| `"-19.90"` | `-1990` | `"-19.90"` |
| `"-0.01"` | `-1` | `"-0.01"` |

The scaled integer storage is internal — you never see it in API
responses. It uses the common "money in cents" pattern: multiply by
`10^scale` on write, divide on read. This gives correct ordering,
range queries, and indexing across the full signed range.

When you read the entity back, olu divides by the scale factor and
formats the result with the declared number of fractional digits
(`"19.90"`, not `1990`). Trailing fractional zeroes are preserved
because they indicate the field's declared scale.

The scaled integer is stored as a 64-bit signed integer, which
supports up to 18 total digits of precision. This covers all practical
use cases.


## Validation Errors

olu rejects decimal values in the following cases:

| Input | Error |
|---|---|
| `"not-a-number"` | Invalid decimal value |
| `""` (empty string) | Invalid decimal value |
| `19.90` (JSON number) | Type error: expected string |
| `"1000000.00"` (for precision 8, scale 2) | Exceeds precision: integer part requires more than 6 digits |
| `"-1000000.00"` (for precision 8, scale 2) | Exceeds precision: integer part requires more than 6 digits |

Precision bounds apply equally to positive and negative values. A field
with precision 8 and scale 2 accepts any value from `"-999999.99"` to
`"999999.99"`.


## Querying Decimal Fields

### Range Queries

Because decimal values are stored as scaled integers, range queries
and ordering work correctly for the full signed range, including
negative values:

```sql
SELECT * FROM invoices WHERE amount > '100.00'
SELECT * FROM invoices WHERE amount BETWEEN '50.00' AND '500.00'
SELECT * FROM invoices WHERE balance < '-10.00'
SELECT * FROM invoices ORDER BY amount
```

OQL translates decimal comparison values to scaled integers
internally.


### Aggregation

OQL automatically uses exact decimal arithmetic for `SUM`, `AVG`,
`MIN`, and `MAX` on decimal fields:

```sql
SELECT SUM(amount) FROM invoices
SELECT AVG(tax_rate) FROM line_items WHERE category = 'electronics'
```

The results are exact — no float64 approximation. `SUM` of `0.10` and
`0.20` returns `"0.3"`, not `0.30000000000000004`.

`COUNT` works normally and returns an integer regardless of field type.


## Common Patterns

### Currency Amounts

```json
{
  "price": {
    "type": "string",
    "format": "decimal",
    "decimalPrecision": 12,
    "decimalScale": 2
  }
}
```

12 total digits, 2 fractional. Range: `-9999999999.99` to `9999999999.99`.
Suitable for most currency amounts.


### Tax Rates and Percentages

```json
{
  "tax_rate": {
    "type": "string",
    "format": "decimal",
    "decimalPrecision": 6,
    "decimalScale": 4
  }
}
```

6 total digits, 4 fractional. Range: `-99.9999` to `99.9999`.
Sufficient for tax rates expressed as percentages with basis-point
precision.


### Quantities and Measurements

```json
{
  "weight_kg": {
    "type": "string",
    "format": "decimal",
    "decimalPrecision": 10,
    "decimalScale": 3
  }
}
```

10 total digits, 3 fractional. Range: `-9999999.999` to `9999999.999`.
Suitable for weights, dimensions, and similar measurements.


### Integer Decimals (No Fractional Part)

```json
{
  "units": {
    "type": "string",
    "format": "decimal",
    "decimalPrecision": 10,
    "decimalScale": 0
  }
}
```

Scale 0 means no fractional digits. Values like `"42"` and `"1000"`
are valid. This is useful when you want exact integer storage with
precision bounds checking and consistent normalisation, without using
the `integer` JSON Schema type.


## Backend Behaviour

The decimal implementation is backend-aware. On SQLite (the current
backend), values are stored as scaled integers (`int64`) and
aggregation happens in Go with exact arithmetic. On PostgreSQL
(future), values map to native `NUMERIC(p,s)` columns and aggregation
happens in SQL — no Go-side computation needed. The API behaviour is
identical regardless of backend.
