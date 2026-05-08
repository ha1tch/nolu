# olu Graph API Reference

> **Status: production-ready** — The graph layer is fully tenant-isolated and
> safe to use in both `path` (single-tenant) and `strict` (multi-tenant) modes.
> Tenant isolation is enforced at the graph snapshot layer, the handler layer,
> and the edge layer. All 12 handler surfaces and the Sulpher query engine are
> covered by an adversarial isolation test suite. See `CHANGELOG.md` [v0.9.5].

## Overview

olu provides a graph layer for navigating relationships between entities. The graph is automatically maintained when entities with REF fields are created, updated, or deleted.

Graph features are available in both operational modes:

- **Single-tenant** (`OLU_TENANT_MODE=path`): graph routes at `/api/v1/graph/...` and `/api/v1/sulpher/...`
- **Multi-tenant** (`OLU_TENANT_MODE=strict`): graph routes at `/api/v1/tenant/{tenant_id}/graph/...` and `/api/v1/tenant/{tenant_id}/sulpher/...`

In strict mode the graph layer is fully tenant-isolated. Node IDs in requests and responses use the client-facing `entity:id` format; the internal `XXXX@entity:id` tenant prefix is added and stripped transparently. Cross-tenant edge leakage is enforced at both the snapshot layer (graph traversal) and the handler layer (node info, degree, in/out edges). The isolation guarantee is covered by an adversarial integration test suite (`graph_tenant_exhaustive_test.go`).

**Query Languages:**

| Language | Documentation | Best For |
|----------|---------------|----------|
| **Sulpher** | This document | Graph traversal, paths, relationships |
| **OQL** | [OQL_API.md](OQL_API.md) | SQL queries, aggregates, bulk mutations |

## Endpoints

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET | `/api/v1/graph/stats` | Graph-wide statistics |
| POST | `/api/v1/graph/path` | Find path between nodes |
| POST | `/api/v1/graph/neighbors` | Get neighbors of a node |
| GET | `/api/v1/graph/nodes/{node_id}` | Full node info with edges |
| GET | `/api/v1/graph/nodes/{node_id}/degree` | In/out degree counts |
| GET | `/api/v1/graph/{node_id}/in` | Incoming edges |
| GET | `/api/v1/graph/{node_id}/out` | Outgoing edges |
| POST | `/api/v1/graph/shortestPath` | Find shortest path |
| POST | `/api/v1/graph/pathExists` | Check if path exists |
| POST | `/api/v1/graph/commonNeighbors` | Find shared outgoing neighbours |
| POST | `/api/v1/graph/nodes/search` | Search nodes by type |

## Node ID Format

Nodes are identified as `{entity}:{id}`, e.g., `items:42`, `records:7`.

---

## Endpoint Details

### GET /api/v1/graph/stats

Returns graph-wide statistics.

**Response:**
```json
{
  "node_count": 150,
  "edge_count": 342,
  "has_cycle": false
}
```

---

### GET /api/v1/graph/nodes/{node_id}

Returns comprehensive information about a node.

**Example:** `GET /api/v1/graph/nodes/items:42`

**Response:**
```json
{
  "id": "items:42",
  "entity": "items",
  "entity_id": 42,
  "outgoing": {
    "locations:5": "location_ref",
    "records:12": "record_ref"
  },
  "incoming": {
    "events:7": "item_ref"
  },
  "degree": {
    "in": 1,
    "out": 2,
    "total": 3
  }
}
```

---

### GET /api/v1/graph/nodes/{node_id}/degree

Returns degree counts for a node.

For adapted entities that have no REF fields — and therefore no edges — the
endpoint returns `{in:0, out:0, total:0}` rather than 404. Non-existent nodes
still return 404.

**Example:** `GET /api/v1/graph/nodes/items:42/degree`

**Response:**
```json
{
  "node_id": "items:42",
  "degree": {
    "in": 1,
    "out": 2,
    "total": 3
  }
}
```

---

### GET /api/v1/graph/{node_id}/in

Returns all incoming edges to a node.

**Example:** `GET /api/v1/graph/items:42/in`

**Response:**
```json
{
  "node_id": "items:42",
  "edges": [
    {
      "source": "events:7",
      "target": "items:42",
      "relationship": "item_ref"
    }
  ],
  "count": 1
}
```

---

### GET /api/v1/graph/{node_id}/out

Returns all outgoing edges from a node.

**Example:** `GET /api/v1/graph/items:42/out`

**Response:**
```json
{
  "node_id": "items:42",
  "edges": [
    {
      "source": "items:42",
      "target": "locations:5",
      "relationship": "location_ref"
    },
    {
      "source": "items:42",
      "target": "records:12",
      "relationship": "record_ref"
    }
  ],
  "count": 2
}
```

---

### POST /api/v1/graph/shortestPath

Finds the shortest path between two nodes.

**Request:**
```json
{
  "from": "items:42",
  "to": "groups:3",
  "max_depth": 10
}
```

**Response (path found):**
```json
{
  "from": "items:42",
  "to": "groups:3",
  "exists": true,
  "path": ["items:42", "records:12", "groups:3"],
  "length": 2
}
```

**Response (no path):**
```json
{
  "from": "items:42",
  "to": "groups:99",
  "exists": false,
  "path": null,
  "length": 0
}
```

---

### POST /api/v1/graph/pathExists

Efficiently checks if a path exists (without computing full path).

**Request:**
```json
{
  "from": "items:42",
  "to": "groups:3",
  "max_depth": 10
}
```

**Response:**
```json
{
  "from": "items:42",
  "to": "groups:3",
  "exists": true,
  "length": 2
}
```

---

### POST /api/v1/graph/commonNeighbors

Finds nodes that both `node_a` and `node_b` have outgoing edges to — i.e.
shared out-neighbours in the directed graph. Nodes that point *to* `node_a`
or `node_b` via incoming edges are not included.

**Request:**
```json
{
  "node_a": "items:42",
  "node_b": "items:57"
}
```

**Response:**
```json
{
  "node_a": "items:42",
  "node_b": "items:57",
  "common": ["locations:5", "groups:3"],
  "count": 2
}
```

Use case: Finding shared outgoing relationships (e.g., "which location or group do
both these items reference?"). For shared in-neighbours, query the incoming edges
of each node separately and intersect the results in the application layer.

---

### POST /api/v1/graph/nodes/search

Searches for nodes by entity type.

**Request:**
```json
{
  "entity": "items",
  "limit": 100
}
```

**Response:**
```json
{
  "nodes": ["items:1", "items:2", "items:42", "..."],
  "count": 42
}
```

When `entity` is specified and the entity has an adapted table (schema
registered via `POST /api/v1/schema/{entity}`), results are drawn directly
from the adapted table and include all entities of that type regardless of
whether they have any edges. For non-adapted entities the in-memory graph
index is used — only nodes with at least one REF edge are returned.

If `entity` is omitted, returns all nodes known to the graph (up to limit).

---

## Configuration

Graph features are controlled by the following config options:

```yaml
graph_enabled: true
max_query_depth: 10
```

### Cycle Detection

When the graph is configured with `cycle_detection: "warn"` or `cycle_detection: "error"`, olu
runs a BFS from the target node back towards the source before committing any new edge. If a
path is found, the edge would create a cycle.

The BFS is bounded by a node-visit budget (`cycle_check_limit`, default **512**). When the budget
is exhausted before a cycle is confirmed or ruled out, the check returns **true conservatively**:

- In `"error"` mode this causes `AddEdge` to return `ErrCycleDetected` (HTTP 409), even though
  no actual cycle was detected. A caller adding a legitimate edge to a large or dense graph may
  receive a 409 with no way to distinguish "genuine cycle" from "budget exhausted".
- In `"warn"` mode a log event is emitted but the edge is still added.
- In `"ignore"` mode (the default) no check is performed; budget is irrelevant.

**Implications for operators:**

- On graphs with more than a few hundred nodes per connected component, consider raising
  `cycle_check_limit` or switching to `"warn"` mode if false positives are observed.
- The error message for budget exhaustion and genuine cycle detection is identical at the API
  level (`"adding this edge would create a cycle"`). There is currently no way to distinguish
  the two from outside the server.

---

## Compatibility with rserv

Endpoint mapping for teams migrating from rserv v0.5.3:

| rserv Endpoint | olu Equivalent |
|----------------|----------------|
| `GET /api/v1/graph/nodes/{id}` | `GET /api/v1/graph/nodes/{node_id}` |
| `GET /api/v1/graph/nodes/{id}/degree` | `GET /api/v1/graph/nodes/{node_id}/degree` |
| `GET /api/v1/graph/{entity}:{id}/in` | `GET /api/v1/graph/{node_id}/in` |
| `GET /api/v1/graph/{entity}:{id}/out` | `GET /api/v1/graph/{node_id}/out` |
| `POST /api/v1/graph/shortestPath` | `POST /api/v1/graph/shortestPath` |
| `POST /api/v1/graph/pathExists` | `POST /api/v1/graph/pathExists` |
| `POST /api/v1/graph/commonNeighbors` | `POST /api/v1/graph/commonNeighbors` |

**Note:** Full-text search is available via `GET /api/v1/search`.

---

## Sulpher Query Language

Sulpher is olu's graph query language. It provides a Cypher-like syntax for traversing and querying the graph.

### Syntax

```
[BFS|DFS] MATCH <pattern> [WHERE <conditions>] RETURN <items>
```

**Components:**

| Component | Description | Example |
|-----------|-------------|---------|
| Algorithm | Optional traversal algorithm (default: BFS) | `BFS`, `DFS` |
| Pattern | Node and relationship patterns | `(u:User)-[r:FOLLOWS]->(f:User)` |
| WHERE | Optional filter conditions | `WHERE u.id = 123 AND u.active = true` |
| RETURN | Fields to return | `RETURN u, f.name` |

### Node Patterns

```
(variable:Type)           -- Variable and type
(variable:Type {props})   -- With inline properties
(variable)                -- Variable only (matches any type)
```

**Examples:**
```
(u:User)                      -- User node assigned to variable 'u'
(u:User {id: 123})            -- User with id=123
(u:User {active: true})       -- Active users
(p:Post {status: "published"}) -- Published posts
```

### Relationship Patterns

```
-[variable:TYPE]->            -- Directed relationship (single hop)
-[:TYPE]->                    -- Type only (no variable)
-[variable]->                 -- Variable only (any type)
-[]->                         -- Any relationship

-- Variable-length patterns:
-[:TYPE*1..5]->               -- 1 to 5 hops
-[:TYPE*..3]->                -- 1 to 3 hops (min defaults to 1)
-[:TYPE*2..]->                -- 2+ hops (uses max_depth limit)
-[:TYPE*]->                   -- 1+ hops (uses max_depth limit)
-[:TYPE*3]->                  -- Exactly 3 hops
-[r:TYPE*1..5]->              -- With variable binding
-[*1..3]->                    -- Any type, 1-3 hops
```

**Examples:**
```
-[r:FOLLOWS]->                -- FOLLOWS relationship
-[:MANAGES]->                 -- MANAGES (no variable needed)
-[r]->                        -- Any relationship, capture as 'r'
-[:FOLLOWS*1..3]->            -- 1-3 hops via FOLLOWS
-[*2..5]->                    -- 2-5 hops via any relationship
```

### WHERE Conditions

Conditions are joined with `AND`. Supported operators: `=`, `!=`, `<`, `>`, `<=`, `>=`

```
WHERE u.age >= 18
WHERE u.name = "Alice" AND u.active = true
WHERE f.score > 100
```

### RETURN Clause

Return whole nodes or specific properties:

```
RETURN u                      -- Whole node
RETURN u.name                 -- Specific property
RETURN u, f, u.name, f.email  -- Multiple items
```

---

### Sulpher Endpoints

#### POST /api/v1/graph/query

Execute a Sulpher query synchronously.

**Request:**
```json
{
  "query": "MATCH (u:User)-[:FOLLOWS]->(f:User) WHERE u.id = 123 RETURN f",
  "max_depth": 10
}
```

**Response:**
```json
{
  "status": "completed",
  "result": [
    {"f": {"_id": "User:456", "type": "User", "name": "Bob"}},
    {"f": {"_id": "User:789", "type": "User", "name": "Carol"}}
  ],
  "stats": {
    "nodes_traversed": 15,
    "paths_found": 2,
    "execution_time_ms": 5
  }
}
```

#### POST /api/v1/graph/query/async

Submit a query for asynchronous execution.

**Request:**
```json
{
  "query": "DFS MATCH (u:User)-[:FOLLOWS]->(f:User)-[:LIKES]->(p:Post) RETURN p",
  "max_depth": 5
}
```

**Response:**
```json
{
  "query_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "pending",
  "created_at": "2024-01-15T10:30:00Z"
}
```

#### GET /api/v1/graph/query/{query_id}

Check the status of an async query.

**Response:**
```json
{
  "query_id": "550e8400-e29b-41d4-a716-446655440000",
  "query": "DFS MATCH (u:User)-[:FOLLOWS]->(f:User)-[:LIKES]->(p:Post) RETURN p",
  "status": "completed",
  "created_at": "2024-01-15T10:30:00Z",
  "started_at": "2024-01-15T10:30:00Z",
  "ended_at": "2024-01-15T10:30:01Z"
}
```

#### GET /api/v1/graph/query/{query_id}/result

Retrieve results of a completed async query.

**Response:**
```json
{
  "query_id": "550e8400-e29b-41d4-a716-446655440000",
  "status": "completed",
  "result": [...],
  "stats": {
    "nodes_traversed": 150,
    "paths_found": 12,
    "execution_time_ms": 45
  }
}
```

---

### Example Queries

**Simple node lookup:**
```
MATCH (u:User {id: 123}) RETURN u
```

**Single-hop traversal:**
```
MATCH (u:User)-[:FOLLOWS]->(f:User) WHERE u.id = 123 RETURN f
```

**Multi-hop traversal:**
```
MATCH (u:User)-[:FOLLOWS]->(f:User)-[:LIKES]->(p:Post) RETURN p
```

**DFS traversal:**
```
DFS MATCH (a:items)-[:record_ref]->(s:records) WHERE a.status = "active" RETURN s
```

**Return specific properties:**
```
MATCH (u:User)-[r:MANAGES]->(e:Employee) RETURN u.name, e.email
```

**With multiple conditions:**
```
MATCH (a:items) WHERE a.status = "active" AND a.priority > 5 RETURN a
```

**Variable-length paths (1-5 hops):**
```
MATCH (u:User)-[:FOLLOWS*1..5]->(f:User) RETURN f
```

**Find all reachable nodes (any depth up to max_depth):**
```
MATCH (a:items)-[:connected_to*]->(b:items) WHERE a.id = 1 RETURN b
```

**Exactly 3 hops:**
```
MATCH (u:User)-[:FOLLOWS*3]->(f:User) RETURN f
```

**At least 2 hops:**
```
MATCH (u:User)-[:FOLLOWS*2..]->(f:User) RETURN f
```

---

### Limitations

| Feature | Status |
|---------|--------|
| BFS/DFS traversal | ✓ Supported |
| Node type matching | ✓ Supported |
| Inline properties | ✓ Supported |
| Relationship types | ✓ Supported |
| WHERE with AND | ✓ Supported |
| WHERE with OR | ✓ Supported |
| Comparison operators | ✓ Supported |
| Property returns | ✓ Supported |
| Variable-length paths `*1..5` | ✓ Supported |
| DISTINCT | ✓ Supported |
| LIMIT | ✓ Supported |
| ORDER BY | ✓ Supported |
| Incoming relationships `<-[]-` | ✓ Supported |
| Bidirectional `-[]-` | ✓ Supported |
| OPTIONAL MATCH | ✗ Future |

**Note:** For SQL-style aggregates (COUNT, SUM, AVG, MIN, MAX) and bulk mutations (UPDATE, DELETE with WHERE), see [OQL_API.md](OQL_API.md).

---

## Advanced Features

### DISTINCT

Remove duplicate results:

```
MATCH (u:User)-[:FOLLOWS*1..3]->(f:User) RETURN DISTINCT f
```

### LIMIT

Limit the number of results:

```
MATCH (u:User) RETURN u LIMIT 10
```

### ORDER BY

Sort results by one or more fields:

```
MATCH (u:User) RETURN u ORDER BY u.name
MATCH (u:User) RETURN u ORDER BY u.age DESC
MATCH (u:User) RETURN u ORDER BY u.name ASC, u.age DESC
```

### OR in WHERE

Combine conditions with OR (groups are AND-joined):

```
-- Match Alice OR Bob
MATCH (u:User) WHERE u.name = 'Alice' OR u.name = 'Bob' RETURN u

-- (age > 18 AND active) OR (role = 'admin' AND verified)
MATCH (u:User) WHERE u.age > 18 AND u.active = true OR u.role = 'admin' AND u.verified = true RETURN u
```

### Relationship Directions

**Outgoing (default):**
```
MATCH (u:User)-[:FOLLOWS]->(f:User) RETURN f
```

**Incoming:**
```
MATCH (u:User)<-[:FOLLOWS]-(f:User) RETURN f
```

**Bidirectional (either direction):**
```
MATCH (u:User)-[:KNOWS]-(f:User) RETURN f    -- undirected
MATCH (u:User)<-[:KNOWS]->(f:User) RETURN f  -- both arrows
```

### Combined Example

```
MATCH (u:User)-[:FOLLOWS*1..3]->(f:User)
WHERE u.id = 123 OR u.role = 'influencer'
RETURN DISTINCT f
ORDER BY f.followers DESC
LIMIT 20
```

---

## When to Use Sulpher vs OQL

| Use Case | Recommended | Example |
|----------|-------------|---------|
| Find paths between nodes | **Sulpher** | `MATCH (a)-[:KNOWS*1..5]->(b) RETURN b` |
| Traverse relationships | **Sulpher** | `MATCH (u)-[:FOLLOWS]->(f) RETURN f` |
| Variable-length paths | **Sulpher** | `MATCH (u)-[:FOLLOWS*2..]->(f) RETURN f` |
| Graph pattern matching | **Sulpher** | `MATCH (a)-[r]->(b)<-[s]-(c) RETURN a,b,c` |
| Count records by category | **OQL** | `SELECT zone, COUNT(*) FROM records GROUP BY zone` |
| Calculate averages, sums | **OQL** | `SELECT AVG(value) FROM records` |
| Bulk update/delete | **OQL** | `UPDATE records SET status='off' WHERE zone=5` |
| Batch insert | **OQL** | `INSERT INTO records (a,b) VALUES (1,2),(3,4)` |

See [OQL_API.md](OQL_API.md) for full OQL documentation.
