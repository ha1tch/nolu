# olu `/commit` Endpoint Design

**Version:** 0.2  
**Author:** haitch <h@ual.fi>  
**Date:** March 2026  
**Status:** Implemented (v0.9.7-patched59)  

---

## 1. Problem

olu's write API provides three independent paths:

- `POST /api/v1/tenant/{t}/objects/{id}` — create an entity
- `POST /api/v1/tenant/{t}/save/{id}` — upsert an entity
- `POST /api/v1/tenant/{t}/{entity}` — create a record of any entity type

These operations are independent HTTP calls. There is no mechanism to
execute two or more of them atomically. A caller that needs to update one
entity and append a record to another must make two separate requests,
and must accept that the following failure modes are possible:

- The first write commits; the second fails. The entity has advanced to a
  new state with no corresponding record in the audit trail.
- The second write commits before the first is visible to other readers
  (no ordering guarantee across two HTTP calls, even to the same server).
- A concurrent writer observes the entity in the new state before the
  audit record exists.

These are not hypothetical edge cases. Consider an FSM executor that
writes a state transition and a timeseries audit entry in two sequential
POST requests. If the timeseries write fails, entity state has advanced
but history has a gap. The two are permanently inconsistent; the gap
cannot be detected from the data alone because there is no record of
what was missing.

A financial application has the same structure: update an account balance
and append a transaction log entry. The pair must land together or not at
all. A balance update without a log entry violates the audit trail. A log
entry without a balance update produces an incorrect balance.

This pattern — one conditional update plus one or more appended records —
recurs across domains. It is the dominant write pattern for any system
that maintains both current state and a history of how that state was
reached.

---

## 2. The Pattern

The pattern has two components:

**Current state** is represented by a single entity record that is
overwritten on each transition. It answers "what is the state of X right
now?" in O(1) without scanning history. In an FSM application this is the
entity record keyed by its ID; in a financial application it is the account
balance record.

**History** is an append-only sequence of records that captures every
transition. It answers "how did X arrive at its current state?" and "what
was the state of X at time T?". In an FSM application this is the
timeseries entry; in a financial application it is the transaction log.

The invariant that must hold across both: **the most recent history entry
always corresponds to the current state entry**. Current state is
derivable from history by replaying all entries from the beginning, or
from any known snapshot forward. History is what makes current state
trustworthy.

When the two writes are separate HTTP calls this invariant is a
best-effort property that can be violated by transient failures. When
they are a single atomic operation the invariant is enforced by the
database engine.

This is the application-level analogue of what traditional SQL engines
call a write-ahead log: the log is primary, the materialised state is
derived. The difference is that here both are first-class entities in olu
rather than implementation details of the storage engine.

---

## 3. Proposed Endpoint

    POST /api/v1/tenant/{tenant_id}/commit

A single HTTP call that executes atomically:

1. A conditional upsert of one entity record (the state update), with an
   optional version check (CAS).
2. One or more unconditional appends of records to any entity type (the
   audit entries).

All operations execute within a single SQLite transaction. Either all
commit or none do. There is no partial success.

---

## 4. Request Shape

```json
{
  "update": {
    "entity": "objects",
    "id": 3012345678901234,
    "version": 7,
    "data": {
      "state": "in-transit",
      "event_id": "evt-a3f9b2c1d4e5f6a7",
      "updated_at": "2026-03-10T14:32:00Z"
    }
  },
  "append": [
    {
      "entity": "timeseries",
      "data": {
        "asset_id":   3012345678901234,
        "from_state": "on-shelf",
        "to_state":   "in-transit",
        "action":     "scan",
        "ts":         "2026-03-10T14:32:00Z"
      }
    }
  ]
}
```

### 4.1 `update` object

| Field    | Type    | Required | Description |
|----------|---------|----------|-------------|
| `entity` | string  | yes      | Entity type. Any entity type valid for `save/{id}`. |
| `id`     | integer | yes      | Entity ID. Must be a positive integer. |
| `data`   | object  | yes      | The full document to write. Replaces the existing document if the entity exists. |
| `version`| integer | no       | If present, a CAS check is performed. The write proceeds only if the stored `_version` equals this value. Omitting `version` is an unconditional upsert. |

**`id` is a positive integer**, not a string. The JSON decoder rejects
string values. Callers working with string identifiers (e.g. barcodes,
account numbers, external reference codes) must parse them to integers
before constructing the request. olu's entity ID space is `int64`; 14-digit
numeric strings and similar fit without loss.

`version` is the value previously returned in `_version` by a `GET` on
the same entity. The semantics are identical to the `_version` field in
the `save/{id}` endpoint (olu v0.9.7-patched57 and later).

### 4.2 `append` array

Each entry in `append` describes one record to insert. Appends are always
creates; there is no conditional check. `id` is optional — if omitted,
olu assigns the next sequence ID for the entity type, matching the
behaviour of `POST /{entity}`.

| Field    | Type   | Required | Description |
|----------|--------|----------|-------------|
| `entity` | string | yes      | Entity type. Any entity type valid for `POST /{entity}`. |
| `id`     | integer | no      | Record ID. Must be a positive integer if supplied. If omitted, olu assigns the next sequence ID for the entity type. |
| `data`   | object | yes      | The document to insert. |

Minimum: `append` must contain at least one entry. A `commit` request
with an empty `append` array is rejected with OLU-CM003. Callers that
need only a conditional upsert without an append should use `save/{id}`
directly.

Maximum: `append` may contain up to 25 entries. Requests exceeding this
limit are rejected with OLU-CM004.

### 4.3 `update` is required

`update` is mandatory. A `commit` with no `update` object is rejected
with OLU-CM002. The endpoint is specifically designed for the pattern of
updating one record and appending one or more; callers that need only a
batch of appends should use multiple `POST /{entity}` calls, or a future
batch-append endpoint.

---

## 5. Response Shape

### 5.1 Success — `200 OK`

```json
{
  "update": {
    "entity": "objects",
    "id": 3012345678901234,
    "created": false,
    "version": 8
  },
  "appended": [
    {
      "entity": "timeseries",
      "id": 10042
    }
  ]
}
```

`update.created` is `true` if the entity did not exist prior to this
commit (equivalent to the `201` vs `200` distinction in `save/{id}`).
`update.version` is the new `_version` value after the commit.

`appended` lists the IDs of all inserted records in the order they
appeared in the request. For entries that supplied an explicit `id`, the
same `id` is echoed back. For entries with no `id`, the assigned sequence ID is
returned here so the caller can reference the record if needed.

### 5.2 Version conflict — `409 Conflict`

```json
{
  "error": {
    "code": "OLU-CM001",
    "message": "Version conflict: expected _version 7, current is 9.",
    "status": 409
  },
  "current_version": 9
}
```

`current_version` is the version presently stored. The caller should
re-read the entity, recompute the transition, and retry with the new
version. The retry protocol is identical to the one used with `save/{id}`.

No partial writes occur on a `409`. Neither the `update` nor any of the
`append` entries are committed.

---

## 6. Atomicity Guarantee

The entire `commit` operation — the `update` upsert and all `append`
inserts — executes within a single SQLite transaction:

```
BEGIN IMMEDIATE
  SELECT _version FROM entities WHERE ...   -- CAS check if version supplied
  INSERT OR REPLACE INTO entities ...       -- update
  INSERT INTO entities ...                  -- append[0]
  INSERT INTO entities ...                  -- append[1]
  ...
COMMIT
```

`BEGIN IMMEDIATE` acquires the write lock at the start of the
transaction, eliminating the possibility of another writer interleaving
between the CAS check and the update. This is more conservative than
`BEGIN DEFERRED` (olu's default for single-statement writes) but
necessary here because the CAS check and the write are separate
statements within the same transaction.

If any statement fails — including the CAS check on the version — the
transaction is rolled back entirely. The caller either sees all effects
or none.

The `IMMEDIATE` lock does not block concurrent reads. SQLite's WAL mode
allows readers to proceed concurrently with a write transaction in
progress. The only cost is serialisation with other writers, which SQLite
already enforces.

---

## 7. CAS Protocol for FSM Executors

The full round-trip for a stateful FSM executor using `/commit`:

```
1. GET  /api/v1/tenant/{t}/objects/{id}
   ← { "data": { "state": "active", "_version": 7 } }

2. Compute transition: active + event → archived

3. POST /api/v1/tenant/{t}/commit
   → {
       "update": {
         "entity": "objects",
         "id": 1234567890,
         "version": 7,
         "data": { "state": "archived", "event_id": "...", "updated_at": "..." }
       },
       "append": [
         {
           "entity": "timeseries",
           "data": { "asset_id": 1234567890, "from_state": "active",
                     "to_state": "archived", "action": "event", "ts": "..." }
         }
       ]
     }

   ← 200 OK  { "update": { "version": 8 }, "appended": [{ "id": "..." }] }
   ← 409     { "current_version": 9 }  → re-read and retry
```

This is three olu interactions per event instead of the current four
(GET state, POST transition, POST timeseries). More importantly, the
transition and the timeseries entry are now guaranteed to land together.
The partial-write failure mode that motivated the Grafana alert on
`fsm_olu_write_errors_total{operation="timeseries"}` is eliminated.

---

## 8. Financial Application Example

```
POST /api/v1/tenant/{t}/commit
{
  "update": {
    "entity": "account",
    "id": 42,
    "version": 311,
    "data": {
      "balance": 9432.17,
      "currency": "USD",
      "last_tx": "tx-2026-03-10-001"
    }
  },
  "append": [
    {
      "entity": "transaction",
      "id": 20260310001,
      "data": {
        "account_id":    42,
        "amount":        -567.83,
        "currency":      "USD",
        "balance_after": 9432.17,
        "description":   "Wire transfer to supplier",
        "ts":            "2026-03-10T14:32:00Z"
      }
    }
  ]
}
```

The `version` check on the account prevents a double-spend: if two
concurrent payment requests both read `_version: 311` and both attempt a
commit, exactly one will succeed and the other will receive a `409` with
`current_version: 312`, at which point it must re-read the balance and
determine whether the payment should still proceed.

`balance_after` in the transaction record is set by the caller before the
request is sent. Because the commit is atomic, the stored transaction
record always reflects the actual resulting balance — it cannot be written
with a different balance than the one that committed.

---

## 9. History Reconstruction

Because every state change passes through `/commit`, and because the
`append` entries are causally linked to the `update` by the transaction,
the append-only records form a complete and ordered history of the
entity's state changes.

This makes current state reconstructable from history:

```
GET /api/v1/tenant/{t}/timeseries?asset_id={id}&order=asc
```

Replaying these records in order from the beginning, or from any known
snapshot forward, yields the current state. This is the same property
that traditional database WAL files provide for crash recovery, applied
at the application entity level.

Practical consequences:

- **Point-in-time queries.** "What was the state of entity X at 09:00 on
  Tuesday?" is answerable by scanning the timeseries up to that timestamp
  and taking the last `to_state`.

- **Audit trail completeness.** There is no way to arrive at the current
  state without a corresponding history entry, because the two are written
  in the same transaction. Silent state mutations are impossible through
  the `/commit` path.

- **Trust properties for regulated domains.** In financial, logistics,
  and compliance contexts, an append-only log that is co-written with
  every balance or state change, and that cannot be revised without
  issuing a new correcting entry, satisfies common audit requirements
  without additional tooling.

The history reconstruction property holds only for writes made through
`/commit`. Direct writes to `save/{id}` or `objects/{id}` bypass the
audit trail. Callers should use `/commit` for all state transitions that
require an audit trail and reserve the single-entity write endpoints for
initialisation, bulk imports, or operations where history is genuinely
not required.

---

## 10. Relationship to Existing Endpoints

| Endpoint | Atomic | CAS | Appends | Use when |
|---|---|---|---|---|
| `POST objects/{id}` | yes (single) | no | no | Create entity, no prior state |
| `POST save/{id}` | yes (single) | optional | no | Upsert with optional CAS; no audit trail required |
| `PUT {entity}/{id}` | yes (single) | optional | no | Full replace with optional CAS |
| `PATCH {entity}/{id}` | yes (single) | optional | no | Partial update with optional CAS |
| `POST commit` | yes (multi) | optional | required | State transition with required audit trail |

`/commit` does not replace the single-entity write endpoints. It
complements them. The single-entity endpoints remain the correct choice
for writes that have no associated audit trail — for example, writing
routing table entries or FSM rule definitions, where the record itself
is the authoritative value and no history is maintained.

---

## 11. Backend Availability

`/commit` is **only available on the SQLite backend**. Invoking it against
a server running the jsonfile backend returns `501 Not Implemented` with
error code `OLU-CM009`.

**How the restriction is enforced:** `JSONFileStore.Commit` returns
`storage.ErrNotSupported`. The HTTP handler maps that sentinel to 501/OLU-CM009.
The handler itself contains no backend-specific logic; any backend that does
not implement `/commit` signals that by returning `ErrNotSupported`.

This is a deliberate design decision, not a temporary limitation. The
jsonfile backend was never a production-grade store; it lacks true
transactional atomicity (see Section 6). Its primary use has always been
test isolation, and it is now deprecated. The old implementation gave a
false impression of atomicity that the filesystem cannot provide; it has
been replaced with a stub that is honest about the contract.

Operators running olu in production should use `OLU_STORAGE_TYPE=sqlite`.
Operators running olu for integration testing who want to exercise
`/commit` code paths should also use SQLite — per-test databases are cheap
with a temp directory.

---

## 12. Strict Mode (`OLU_STRICT_COMMIT`)

`/commit` supports two operational modes controlled by the
`OLU_STRICT_COMMIT` environment variable.

| Setting | Default | Behaviour |
|---------|---------|-----------|
| `true` | **yes** | Schema validation and graph cycle prechecks run before the storage transaction, matching the guarantees of `save`/`create`/`patch`. |
| `false` | no | Validation is skipped. Only structural checks (entity names, ID positivity, append count) are performed. The storage transaction still enforces CAS and duplicate-ID constraints. |

**Default is `true`.** This is the safe default: `/commit` is a
first-class write surface and should behave consistently with the rest of
the API unless the caller explicitly opts out.

**When to use `false`:** Trusted infrastructure that constructs its own
payloads and manages schema invariants independently (for example, an FSM
executor that only writes fields it owns) may set `OLU_STRICT_COMMIT=false`
to avoid redundant validation on the hot path. This is a deliberate
operator choice, not a recommended default.

**Structural validation is always enforced regardless of this setting:**
entity name format, positive IDs, and append count (1–25).

**In-memory graph updates are always performed after a successful commit**
regardless of this setting. A stale `FlatGraph` after a successful write
would be a correctness bug, not a policy choice.

---

## 13. Error Codes

Errors specific to the `/commit` endpoint use the `OLU-CM` prefix.
Standard entity errors (`OLU-ST*`, `OLU-VL*`) may also be returned when
the underlying store operations fail or when strict mode validation rejects
a payload.

| Code       | HTTP | Meaning |
|------------|------|---------|
| OLU-CM001  | 409  | Version conflict. `current_version` field present in response body. |
| OLU-CM002  | 400  | `update` object missing or null. |
| OLU-CM003  | 400  | `append` array is empty or missing. |
| OLU-CM004  | 400  | `append` array exceeds 25 entries. |
| OLU-CM005  | 400  | `update.entity` is not a valid entity type for this tenant. |
| OLU-CM006  | 400  | One or more `append` entries reference an invalid entity type. |
| OLU-CM007  | 409  | An `append` entry specifies an explicit `id` that already exists. |
| OLU-CM008  | 500  | Transaction failed (SQLite error). All changes rolled back. |
| OLU-CM009  | 501  | `/commit` is not available on the current storage backend (jsonfile). |

OLU-CM007 deserves a note: `append` entries are inserts, not upserts. If
the caller supplies an explicit ID for an append entry and a record with
that ID already exists in that entity type, the entire commit is
rejected. Callers should use olu-generated IDs (omit the `id` field) for
append entries unless the ID is guaranteed unique by construction (e.g. a
UUID generated by the caller immediately before the request).

When `OLU_STRICT_COMMIT=true` (the default), schema validation failures
return `400 OLU-VL001` and graph cycle violations return `409 OLU-GR001`.
These use the same codes as the normal write surface intentionally, so
that client error-handling code does not need to special-case `/commit`.

---

## 14. Implementation Notes

### 14.1 SQLite transaction shape

```go
func (s *SQLiteStore) Commit(ctx context.Context, req CommitRequest) (CommitResult, error) {
    var result CommitResult
    err := s.withRetry(func() error {
        var innerErr error
        result, innerErr = s.commitInner(ctx, req)
        return innerErr
    })
    return result, err
}

func (s *SQLiteStore) commitInner(ctx context.Context, req CommitRequest) (CommitResult, error) {
    tx, err := s.db.BeginTx(ctx, nil)   // default isolation; see below
    // ...
}
```

**Transaction serialisation:** `BeginTx` is called with `nil` options, not
`sql.LevelSerializable`. SQLite ignores most `database/sql` isolation level
flags; serialisation is enforced by the WAL write lock. The actual
mechanism is `withRetry`, which holds olu's adaptive lock during the
transaction and retries up to 7 times with exponential backoff on
`SQLITE_BUSY`. Only one writer can hold the WAL write lock at a time;
`withRetry` ensures olu does not compete with itself under contention.

`withRetry` on `Commit` is intentional. It retries only on `SQLITE_BUSY`.
A version conflict returns `ErrConflict`, which is not a BUSY error, so
the retry loop exits immediately and 409 reaches the caller. A retry
cannot silently double-write or mask a conflict; the CAS guarantee is
fully preserved across retries.

### 14.2 Storage interface change

```go
type Store interface {
    // ... existing methods ...

    Commit(ctx context.Context, req CommitRequest) (CommitResult, error)
}
```

`CommitRequest` and `CommitResult` are new types. The jsonfile backend
implements `Commit` as a stub returning `storage.ErrNotSupported`, which
the HTTP handler maps to 501 OLU-CM009. See Section 11.

### 14.3 Handler registration

```go
mux.HandleFunc("POST /api/v1/tenant/{tenant_id}/commit", handleCommit)
```

`handleCommit` decodes the request body, validates required fields,
delegates to `store.Commit`, and maps `*ConflictError` to a `409`
response with the `current_version` field.

---

## 15. Future Considerations

**Multiple updates.** The current design allows exactly one `update`
entry. Allowing multiple updates within a single commit (e.g. two account
balances updated together) would make the endpoint a general-purpose
mini-transaction. This is intentionally deferred — the single-update
constraint keeps the semantics simple, prevents misuse as a general batch
endpoint, and covers the vast majority of the target pattern. The design
does not preclude relaxing this constraint in a future version.

**Timeseries backend.** Once the Pebble-backed timeseries store is active
(TIMESERIES_DESIGN_V3.md), append entries targeting timeseries entity
types could be routed to Pebble rather than SQLite. The transaction
guarantee across a SQLite write and a Pebble write would require a
two-phase approach; this is a non-trivial extension and is not in scope
for the initial implementation. In the interim, timeseries entries in a
`commit` request write to the SQLite entity store, matching current
single-request behaviour.

**Idempotency key.** High-reliability callers (financial, logistics) may
want to supply a caller-generated idempotency key so that a retry after a
network timeout does not produce a duplicate commit. This is a meaningful
addition but orthogonal to the core design; it can be added as an
optional top-level field without changing the semantics described here.
