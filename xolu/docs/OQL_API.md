# OQL API Documentation

**Version:** 0.8.0  
**Status:** Active

OQL (Olu Query Language) provides SQL-compatible query and mutation capabilities for olu. It uses a subset of T-SQL syntax powered by the [tsqlparser](https://github.com/ha1tch/tsqlparser) library.

---

## Overview

OQL complements the Sulpher graph query language:

| Language | Syntax | Best For |
|----------|--------|----------|
| **Sulpher** | Cypher-like | Graph traversal, paths, relationships |
| **OQL** | SQL | Aggregates, tabular queries, bulk mutations |

---

## Endpoints

### Execute Query (Sync)

```
POST /api/v1/oql/query
```

Execute an OQL query synchronously.

**Request:**
```json
{
  "query": "SELECT category_id, COUNT(*) AS count FROM items GROUP BY category_id"
}
```

**Response (200 OK):**
```json
{
  "status": "completed",
  "data": [
    {"category_id": 1, "count": 42},
    {"category_id": 2, "count": 17}
  ],
  "stats": {
    "rows_scanned": 59,
    "rows_returned": 2,
    "rows_affected": 0,
    "execution_time_ms": 12
  }
}
```

---

### Execute Query (Async)

```
POST /api/v1/oql/query/async
```

Submit an OQL query for asynchronous execution.

**Request:**
```json
{
  "query": "SELECT * FROM items WHERE status = 'active'"
}
```

**Response (202 Accepted):**
```json
{
  "query_id": "oql_1704520800000000000",
  "status": "pending"
}
```

---

### Get Query Status

```
GET /api/v1/oql/query/{query_id}
```

Get the status of an async query.

**Response (200 OK):**
```json
{
  "query_id": "oql_1704520800000000000",
  "query": "SELECT * FROM items WHERE status = 'active'",
  "status": "completed",
  "created_at": "2025-01-06T12:00:00Z",
  "updated_at": "2025-01-06T12:00:01Z"
}
```

**Status values:** `pending`, `running`, `completed`, `failed`

---

### Get Query Result

```
GET /api/v1/oql/query/{query_id}/result
```

Get the result of a completed async query.

**Response (200 OK):**
```json
{
  "query_id": "oql_1704520800000000000",
  "status": "completed",
  "data": [...],
  "stats": {
    "rows_scanned": 100,
    "rows_returned": 42,
    "rows_affected": 0,
    "execution_time_ms": 15
  }
}
```

**Response (202 Accepted)** if still processing:
```json
{
  "query_id": "oql_1704520800000000000",
  "status": "running",
  "message": "Query is still processing"
}
```

---

## Supported SQL Syntax

### SELECT

```sql
SELECT [DISTINCT] [TOP n] columns
FROM entity
[WHERE conditions]
[GROUP BY columns]
[HAVING aggregate_conditions]
[ORDER BY columns [ASC|DESC]]
```

**Examples:**

```sql
-- Basic select
SELECT * FROM items

-- With conditions
SELECT id, name, value FROM items WHERE status = 'active'

-- With aggregates
SELECT category_id, COUNT(*) AS count, AVG(value) AS avg_value
FROM items
GROUP BY category_id

-- With HAVING
SELECT category_id, COUNT(*) AS count
FROM items
GROUP BY category_id
HAVING COUNT(*) > 5

-- With ORDER BY and TOP
SELECT TOP 10 * FROM items ORDER BY value DESC

-- DISTINCT
SELECT DISTINCT status FROM items
```

### INSERT

```sql
INSERT INTO entity (column1, column2, ...) VALUES (value1, value2, ...), ...
```

**Examples:**

```sql
-- Single row
INSERT INTO items (category_id, status, value) VALUES (1, 'active', 23.5)

-- Multiple rows
INSERT INTO items (category_id, status) VALUES 
  (1, 'active'),
  (2, 'active'),
  (3, 'inactive')
```

### UPDATE

```sql
UPDATE entity SET column = value, ... WHERE condition
```

**Note:** WHERE clause is **required**. UPDATE without WHERE is rejected.

**Examples:**

```sql
-- Update single field
UPDATE items SET status = 'inactive' WHERE category_id = 5

-- Update multiple fields
UPDATE items SET status = 'maintenance', value = 0 WHERE id = 123
```

### DELETE

```sql
DELETE FROM entity WHERE condition
```

**Note:** WHERE clause is **required**. DELETE without WHERE is rejected.

**Examples:**

```sql
-- Delete by condition
DELETE FROM items WHERE status = 'decommissioned'

-- Delete with multiple conditions
DELETE FROM items WHERE category_id = 5 AND last_value < '2024-01-01'
```

---

## Aggregate Functions

| Function | Description | Example |
|----------|-------------|---------|
| `COUNT(*)` | Count all rows | `SELECT COUNT(*) FROM items` |
| `COUNT(column)` | Count non-null values | `SELECT COUNT(value) FROM items` |
| `SUM(column)` | Sum of values | `SELECT SUM(value) FROM items` |
| `AVG(column)` | Average of values | `SELECT AVG(value) FROM items` |
| `MIN(column)` | Minimum value | `SELECT MIN(value) FROM items` |
| `MAX(column)` | Maximum value | `SELECT MAX(value) FROM items` |

---

## WHERE Clause Operators

| Operator | Description | Example |
|----------|-------------|---------|
| `=` | Equal | `WHERE status = 'active'` |
| `!=`, `<>` | Not equal | `WHERE status != 'inactive'` |
| `<` | Less than | `WHERE value < 100` |
| `>` | Greater than | `WHERE value > 50` |
| `<=` | Less than or equal | `WHERE value <= 100` |
| `>=` | Greater than or equal | `WHERE value >= 50` |
| `AND` | Logical AND | `WHERE a = 1 AND b = 2` |
| `OR` | Logical OR | `WHERE a = 1 OR b = 2` |
| `NOT` | Logical NOT | `WHERE NOT status = 'inactive'` |
| `BETWEEN` | Range | `WHERE value BETWEEN 10 AND 100` |
| `IN` | Set membership | `WHERE status IN ('active', 'pending')` |
| `LIKE` | Pattern match | `WHERE name LIKE 'item%'` |
| `IS NULL` | Null check | `WHERE value IS NULL` |
| `IS NOT NULL` | Not null check | `WHERE value IS NOT NULL` |

---

## Limitations

| Feature | Status |
|---------|--------|
| SELECT with aggregates | ✓ Supported |
| INSERT with VALUES | ✓ Supported |
| UPDATE with WHERE | ✓ Supported |
| DELETE with WHERE | ✓ Supported |
| GROUP BY, HAVING | ✓ Supported |
| ORDER BY, TOP | ✓ Supported |
| DISTINCT | ✓ Supported |
| JOINs | ✗ Not supported (use Sulpher for relationships) |
| Subqueries | ✗ Not supported |
| INSERT ... SELECT | ✗ Not supported |
| UPDATE without WHERE | ✗ Rejected |
| DELETE without WHERE | ✗ Rejected |
| Window functions | ✗ Not supported |
| CTEs | ✗ Not supported |

---

## Error Responses

**400 Bad Request** - Invalid query syntax or validation error:
```json
{
  "error": "parse error: unexpected token at position 15"
}
```

**400 Bad Request** - Safety violation:
```json
{
  "error": "UPDATE without WHERE clause is not permitted"
}
```

**400 Bad Request** - Entity not found:
```json
{
  "error": "entity 'nonexistent' does not exist"
}
```

**404 Not Found** - Query ID not found:
```json
{
  "error": "Query not found"
}
```

---

## Usage Examples

### Analytics Query

```bash
curl -X POST http://localhost:8080/api/v1/oql/query \
  -H "Content-Type: application/json" \
  -d '{
    "query": "SELECT category_id, COUNT(*) as count, AVG(value) as avg FROM items WHERE status = '\''active'\'' GROUP BY category_id ORDER BY count DESC"
  }'
```

### Bulk Update

```bash
curl -X POST http://localhost:8080/api/v1/oql/query \
  -H "Content-Type: application/json" \
  -d '{
    "query": "UPDATE items SET status = '\''maintenance'\'' WHERE category_id = 5"
  }'
```

### Batch Insert

```bash
curl -X POST http://localhost:8080/api/v1/oql/query \
  -H "Content-Type: application/json" \
  -d '{
    "query": "INSERT INTO items (category_id, status, value) VALUES (1, '\''active'\'', 23.5), (2, '\''active'\'', 24.1)"
  }'
```

### Async Query

```bash
# Submit
QUERY_ID=$(curl -s -X POST http://localhost:8080/api/v1/oql/query/async \
  -H "Content-Type: application/json" \
  -d '{"query": "SELECT * FROM items"}' | jq -r '.query_id')

# Poll status
curl http://localhost:8080/api/v1/oql/query/$QUERY_ID

# Get result
curl http://localhost:8080/api/v1/oql/query/$QUERY_ID/result
```

---

## When to Use OQL vs Sulpher

| Use Case | Recommended |
|----------|-------------|
| Count records by category | OQL |
| Calculate averages, sums | OQL |
| Bulk update/delete | OQL |
| Batch insert | OQL |
| Find paths between nodes | Sulpher |
| Traverse relationships | Sulpher |
| Variable-length paths | Sulpher |
| Graph pattern matching | Sulpher |

**Combined Example:**

```
# Find users followed by user 123 (Sulpher)
POST /api/v1/graph/query
{"query": "MATCH (u:User)-[:FOLLOWS*1..3]->(f:User) WHERE u.id = 123 RETURN f"}

# Count followers per user (OQL)
POST /api/v1/oql/query
{"query": "SELECT user_id, COUNT(*) as followers FROM follows GROUP BY user_id ORDER BY followers DESC TOP 10"}
```
