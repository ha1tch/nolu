# Olu Runbook

Quick-reference guide for operators. Covers health checks, common failure
modes, and first-response actions. For full configuration details, see
MANUAL.md.

## Health Checks

```
GET /health     Liveness — pings the storage backend
GET /ready      Readiness — same check, for K8s probes
GET /version    Returns version string
GET /metrics    Prometheus-format counters
```

A healthy server returns `200` with `{"status": "ok"}` on both `/health`
and `/ready`. Any non-200 means the storage backend is unreachable.

**Monitoring setup:** Poll `/health` every 10s. Alert on 3 consecutive
failures. Poll `/metrics` for scraping into Prometheus/Grafana.

## Common Failure Modes

### SQLite "database is locked"

**Symptom:** 500 errors on writes, logs show `database is locked`.

**Cause:** Write contention under load. SQLite allows one writer at a time.
The busy timeout (`OLU_SQLITE_BUSY_TIMEOUT`, default 5000ms) determines
how long a writer waits before giving up.

**Response:**
1. Check if another process has the DB open (`fuser /path/to/olu.db`).
2. Increase busy timeout: `OLU_SQLITE_BUSY_TIMEOUT=10000`.
3. If using multi-tenant mode, each tenant gets its own connection pool.
   Check `ulimit -n` — you need at least `(tenants x 4) + 50` file
   descriptors.

### File Descriptor Exhaustion

**Symptom:** New connections refused, logs show `too many open files`.

**Cause:** Multi-tenant SQLite opens separate connection pools per tenant.
Each pool uses 1 writer + N readers (default: NumCPU readers).

**Response:**
1. Check current usage: `ls /proc/$(pidof olu)/fd | wc -l`
2. Check limit: `cat /proc/$(pidof olu)/limits | grep "open files"`
3. Increase: `ulimit -n 65536` (or via systemd `LimitNOFILE`).

### Disk Full

**Symptom:** 500 errors on writes, WAL file stops growing.

**Response:**
1. Check disk: `df -h /path/to/data/`
2. If WAL is large, force a checkpoint: `sqlite3 olu.db "PRAGMA wal_checkpoint(TRUNCATE)"`
3. The WAL file won't shrink until checkpointed.
4. Consider enabling retention policies if timeseries is filling disk.

### Slow Queries

**Symptom:** Requests to `/api/v1/oql/query` hang or return `504`.

**Cause:** OQL query scanning too many rows or producing too much output.

**Response:**
1. Check the query guardrails in effect:

   | Variable | Default | What it limits |
   |---|---|---|
   | `OLU_QUERY_TIMEOUT` | 30s | Execution time |
   | `OLU_QUERY_MAX_ROWS` | 10,000 | Rows returned |
   | `OLU_QUERY_MAX_SCAN_ROWS` | 100,000 | Rows scanned |
   | `OLU_QUERY_MAX_RESPONSE_BYTES` | 10 MB | JSON response size |

2. Error codes in the response identify which limit was hit:

   | Code | Meaning |
   |---|---|
   | OLU-QL008 | Query timed out |
   | OLU-QL009 | Too many rows returned |
   | OLU-QL010 | Too many rows scanned |
   | OLU-QL011 | Response too large |

3. Ask the user to add `WHERE` clauses or `TOP N` to narrow the query.
4. If the defaults are too low for your workload, increase them via env vars.

### Graph Query Melting CPU

**Symptom:** High CPU on graph queries, `/metrics` shows long execution
times on Sulpher endpoints.

**Cause:** Graph traversals can fan out explosively on dense graphs.
A query that matches many start nodes and has deep or variable-length
paths will visit thousands of nodes.

**Response:**
1. Check the traversal limits in effect:

   | Variable | Default | What it limits |
   |---|---|---|
   | `OLU_GRAPH_MAX_VISITED_NODES` | 10,000 | Nodes visited per traversal |
   | `OLU_GRAPH_MAX_RESULTS` | 10,000 | Result paths returned |
   | `OLU_QUERY_TIMEOUT` | 30s | Execution time (shared with OQL) |
   | `OLU_QUERY_MAX_RESPONSE_BYTES` | 10 MB | Response size (shared with OQL) |

2. Timeouts actually cancel the traversal — the engine checks
   `ctx.Done()` during BFS/DFS loops, so a cancelled query releases
   CPU and memory.

3. Lower `OLU_GRAPH_MAX_VISITED_NODES` to reduce the worst-case work
   per query. The default of 10,000 is generous for most graphs.

4. Ask the user to add `LIMIT` or tighter `WHERE` conditions.

5. If a specific query pattern consistently fans out, consider whether
   the graph model needs restructuring (e.g., intermediate grouping nodes
   to reduce edge density).

### Cache Misses / High Latency on Reads

**Symptom:** Read latency increases, `/metrics` shows low cache hit rate.

**Response:**
1. Check cache type: in-memory (`OLU_CACHE_TYPE=memory`) or Redis.
2. For in-memory: increase size with `OLU_CACHE_SIZE` (default 1000 entries).
3. For Redis: check Redis connectivity and pool size (`OLU_REDIS_POOL_SIZE`).
4. Check TTL isn't too short (`OLU_CACHE_TTL`, default 300s).

## Where to Look First

| What happened | Where to look |
|---|---|
| Server won't start | stderr / journalctl — config validation errors print on startup |
| Writes failing | Check `/health` first, then SQLite lock state |
| Reads returning stale data | Cache TTL, or check if writes are actually succeeding |
| OQL query errors | Error code in response body (OLU-QL0xx series) |
| High memory usage | Check tenant count x connection pool size |
| Export/backup empty | WAL checkpoint issue — see Disk Full section |

## Emergency Procedures

### Force WAL Checkpoint

```bash
sqlite3 /path/to/olu.db "PRAGMA wal_checkpoint(TRUNCATE);"
```

### Emergency Backup

```bash
# Stop the server first for a clean backup
systemctl stop olu
cp /path/to/olu.db /backup/olu-emergency-$(date +%s).db
systemctl start olu
```

Or use the export endpoint while running (WAL is now checkpointed
automatically before export):

```bash
curl -o backup.zip http://localhost:9090/api/v1/export
```

### Kill a Runaway Query

OQL queries respect server-side timeouts (`OLU_QUERY_TIMEOUT`). If a
query is stuck, it will be cancelled automatically when the deadline
expires. Async queries (`/oql/query/async`) are also subject to this
timeout.

If the server itself is unresponsive, restart it. SQLite WAL mode
ensures no data is lost on unclean shutdown.
