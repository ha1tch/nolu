# nolu API Reference

Version: 0.1  
Base URL: `http://<host>:7070`  
All request and response bodies are JSON. All timestamps are RFC 3339 UTC.

---

## Overview

nolu exposes three resource groups over HTTP:

- **Registry** — global entity identity: register, resolve, transfer, retire
- **Transfers** — the bilateral transfer negotiation protocol
- **Subscriptions** — durable event delivery to external endpoints

nolu does not store or proxy entity data. It stores identity records only.
The `LocalRef` embedded in every record is the address of the actual data
in a xolu instance.

### GlobalID

Every registered entity has a stable URI of the form:

```
nolu://<registry-host>/<entity-type>/<uuid>
```

Example: `nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a`

GlobalIDs are URL-encoded when used as path segments:

```
GET /registry/nolu%3A%2F%2Fregistry.acme.com%2Fdevices%2F01920d4e...
```

### LocalRef

A LocalRef identifies an entity's current home in a specific xolu instance:

```json
{
  "instance_url": "http://xolu-vendocorp:9090",
  "tenant_id": 0,
  "entity_type": "devices",
  "local_id": 42
}
```

`tenant_id` is 0 for unscoped xolu instances.

### Error responses

All errors follow a consistent envelope:

```json
{
  "error": {
    "code": "NOLU-RG001",
    "message": "entity not found",
    "status": 404
  }
}
```

#### Error codes

| Code | Meaning | HTTP |
|------|---------|------|
| `NOLU-RG001` | GlobalID not found | 404 |
| `NOLU-RG002` | GlobalID already registered | 409 |
| `NOLU-RG003` | Entity is retired | 410 |
| `NOLU-RG004` | Transfer rejected — From does not match current owner | 409 |
| `NOLU-TX001` | Proposal not found | 404 |
| `NOLU-TX002` | Proposal is not in the required state for this operation | 409 |
| `NOLU-TX003` | Caller is not authorised for this operation | 403 |
| `NOLU-SW001` | Subscription not found | 404 |
| `NOLU-SW002` | Duplicate subscription | 409 |
| `NOLU-XX001` | Invalid request body | 400 |
| `NOLU-XX002` | Internal error | 500 |

---

## Registry

### Register an entity

Mints a new GlobalID for an entity and records its initial owner.
The GlobalID is constructed from the registry's configured host and the
supplied entity type. The caller supplies the LocalRef pointing at the
entity's current location in a xolu instance.

```http
POST /registry
Content-Type: application/json
```

**Request**

```json
{
  "entity_type": "devices",
  "owner": {
    "instance_url": "http://xolu-vendocorp:9090",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 1000
  }
}
```

**Response** `201 Created`

```json
{
  "global_id": "nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a",
  "status": "active",
  "current": {
    "instance_url": "http://xolu-vendocorp:9090",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 1000
  },
  "since": "2026-05-06T21:00:00Z",
  "history": [],
  "created_at": "2026-05-06T21:00:00Z",
  "updated_at": "2026-05-06T21:00:00Z"
}
```

**Errors:** `NOLU-XX001` (bad request), `NOLU-RG002` (already exists, should not occur with UUID minting)

---

### Get an entity record

Returns the full registry record for a GlobalID, including complete ownership
history.

```http
GET /registry/{global_id}
```

**Response** `200 OK`

```json
{
  "global_id": "nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a",
  "status": "active",
  "current": {
    "instance_url": "http://xolu-retailchain:9091",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 2000
  },
  "since": "2026-05-06T22:00:00Z",
  "history": [
    {
      "from": {
        "instance_url": "http://xolu-vendocorp:9090",
        "tenant_id": 0,
        "entity_type": "devices",
        "local_id": 1000
      },
      "to": {
        "instance_url": "http://xolu-retailchain:9091",
        "tenant_id": 0,
        "entity_type": "devices",
        "local_id": 2000
      },
      "at": "2026-05-06T22:00:00Z",
      "protocol": "PO-2026-001",
      "history_from": "none"
    }
  ],
  "created_at": "2026-05-06T21:00:00Z",
  "updated_at": "2026-05-06T22:00:00Z"
}
```

**Errors:** `NOLU-RG001` (not found)

---

### Resolve an entity

Returns only the current LocalRef for a GlobalID. The lightweight path for
callers that only need to know where to find an entity right now.

```http
GET /registry/{global_id}/resolve
```

**Response** `200 OK`

```json
{
  "global_id": "nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a",
  "current": {
    "instance_url": "http://xolu-retailchain:9091",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 2000
  }
}
```

**Errors:** `NOLU-RG001` (not found), `NOLU-RG003` (retired — entity exists but has no current owner)

---

### Transfer ownership

Atomically moves ownership of a GlobalID from one LocalRef to another.
`from` must exactly match the registry's current owner record — this acts
as an optimistic concurrency guard. If it does not match, the transfer is
rejected with `NOLU-RG004`.

This is the clearinghouse operation. Once it returns 200, the registry record
is the authoritative statement of current ownership. Both xolu instances may
be updated asynchronously; the registry is the ground truth.

Note: for bilateral transfers where the incoming owner must explicitly accept,
use the Transfer Negotiation endpoints instead. Direct transfer is appropriate
when both sides are controlled by the same system.

```http
POST /registry/{global_id}/transfer
Content-Type: application/json
```

**Request**

```json
{
  "from": {
    "instance_url": "http://xolu-vendocorp:9090",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 1000
  },
  "to": {
    "instance_url": "http://xolu-retailchain:9091",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 2000
  },
  "protocol": "PO-2026-001",
  "history_from": "none"
}
```

`history_from` is advisory metadata for the application layer: `"none"`,
`"full"`, or an RFC 3339 timestamp. nolu records it but does not enforce it.

**Response** `200 OK` — the updated record (same shape as GET).

**Errors:** `NOLU-RG001` (not found), `NOLU-RG003` (retired), `NOLU-RG004` (wrong current owner)

---

### Retire an entity

Permanently decommissions a GlobalID. No further transfers are permitted.
The record and its full history remain queryable. Resolve will return
`NOLU-RG003` for retired entities.

```http
POST /registry/{global_id}/retire
Content-Type: application/json
```

**Request**

```json
{
  "reason": "exceeded 7-year service life"
}
```

**Response** `200 OK` — the updated record with `"status": "retired"`.

**Errors:** `NOLU-RG001` (not found), `NOLU-RG003` (already retired)

---

### List by instance

Returns all active GlobalIDs currently owned by the given xolu instance.
Useful for migration planning and instance health audits.

```http
GET /registry?instance_url={url}
```

**Response** `200 OK`

```json
{
  "instance_url": "http://xolu-vendocorp:9090",
  "count": 3,
  "global_ids": [
    "nolu://registry.acme.com/devices/01920d4e-...",
    "nolu://registry.acme.com/devices/02a30e5f-...",
    "nolu://registry.acme.com/shelves/09f40a6g-..."
  ]
}
```

---

### List by entity type

Returns all active GlobalIDs of the given entity type across all instances.

```http
GET /registry?entity_type={type}
```

**Response** `200 OK`

```json
{
  "entity_type": "devices",
  "count": 42,
  "global_ids": [
    "nolu://registry.acme.com/devices/01920d4e-...",
    "..."
  ]
}
```

---

## Transfer Negotiation

The transfer negotiation protocol provides a bilateral handshake for ownership
changes that require explicit acceptance by the incoming owner. It is appropriate
when transferring between independent organisations, or when history portability
needs to be negotiated.

### Lifecycle

```
PROPOSED → ACCEPTED  → COMPLETED
         ↘ REJECTED
PROPOSED → CANCELLED
```

The outgoing owner (From) creates proposals and completes or cancels them.  
The incoming owner (To) accepts or rejects them.  
Accepting a proposal drives the registry transfer atomically.

---

### Propose a transfer

```http
POST /transfers
Content-Type: application/json
```

**Request**

```json
{
  "global_id": "nolu://registry.acme.com/devices/01920d4e-...",
  "from": {
    "instance_url": "http://xolu-vendocorp:9090",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 1000
  },
  "to": {
    "instance_url": "http://xolu-retailchain:9091",
    "tenant_id": 0,
    "entity_type": "devices",
    "local_id": 2000
  },
  "protocol": "PO-2026-001",
  "history_offer": {
    "mode": "full",
    "note": "Full manufacturing and QA history included"
  }
}
```

`history_offer.mode` is one of `"none"`, `"full"`, or `"from"`.  
When `"from"`, include `"from": "<RFC 3339 timestamp>"` in `history_offer`.

**Response** `201 Created`

```json
{
  "id": "9468f0cb-476e-406f-8ad8-c89d1f9bc67c",
  "global_id": "nolu://registry.acme.com/devices/01920d4e-...",
  "from": { "instance_url": "http://xolu-vendocorp:9090", "...": "..." },
  "to": { "instance_url": "http://xolu-retailchain:9091", "...": "..." },
  "state": "proposed",
  "protocol": "PO-2026-001",
  "history_offer": { "mode": "full" },
  "proposed_at": "2026-05-06T21:00:00Z",
  "updated_at": "2026-05-06T21:00:00Z"
}
```

---

### Get a proposal

```http
GET /transfers/{proposal_id}
```

**Response** `200 OK` — full proposal record (same shape as above, with
`history_spec` and `rejection_reason` populated as applicable).

**Errors:** `NOLU-TX001` (not found)

---

### Accept a proposal

Called by the incoming owner (To). Drives the registry transfer atomically.
After this call the registry record reflects the new owner.

```http
POST /transfers/{proposal_id}/accept
Content-Type: application/json
```

**Request**

```json
{
  "history_spec": {
    "mode": "full"
  }
}
```

`history_spec.mode` must be compatible with the proposal's `history_offer`:
`"none"` is always valid; `"full"` requires the offer to be `"full"`;
`"from"` requires the offer to be `"full"` or `"from"`.

**Response** `200 OK` — updated proposal with `"state": "accepted"`.

**Errors:** `NOLU-TX001` (not found), `NOLU-TX002` (wrong state), `NOLU-RG004` (registry rejected — From no longer current owner)

---

### Reject a proposal

Called by the incoming owner (To). The registry record is unchanged.

```http
POST /transfers/{proposal_id}/reject
Content-Type: application/json
```

**Request**

```json
{
  "reason": "failed pre-delivery inspection: sensor calibration out of range"
}
```

**Response** `200 OK` — updated proposal with `"state": "rejected"`.

**Errors:** `NOLU-TX001` (not found), `NOLU-TX002` (wrong state)

---

### Cancel a proposal

Called by the outgoing owner (From). Only valid while the proposal is in
`proposed` state. The registry record is unchanged.

```http
POST /transfers/{proposal_id}/cancel
```

**Response** `200 OK` — updated proposal with `"state": "cancelled"`.

**Errors:** `NOLU-TX001` (not found), `NOLU-TX002` (wrong state)

---

### Complete a transfer

Called by the outgoing owner (From) after it has fulfilled its history
delivery obligations. Moves the proposal from `accepted` to `completed`.
This is the settlement confirmation — both parties can treat the transfer
as fully closed once this call succeeds.

```http
POST /transfers/{proposal_id}/complete
```

**Response** `200 OK` — updated proposal with `"state": "completed"`.

**Errors:** `NOLU-TX001` (not found), `NOLU-TX002` (wrong state — must be `accepted`)

---

### List proposals by entity

Returns all proposals for a given GlobalID, most recent first.

```http
GET /transfers?global_id={global_id}
```

**Response** `200 OK`

```json
{
  "global_id": "nolu://registry.acme.com/devices/01920d4e-...",
  "count": 2,
  "proposals": [ { "...": "..." } ]
}
```

---

### List proposals by instance

Returns all proposals where the given xolu instance is either the outgoing
or incoming owner. Optionally filter by state.

```http
GET /transfers?instance_url={url}&state={state}
```

`state` is optional: `proposed`, `accepted`, `completed`, `rejected`,
or `cancelled`.

**Response** `200 OK`

```json
{
  "instance_url": "http://xolu-vendocorp:9090",
  "count": 5,
  "proposals": [ { "...": "..." } ]
}
```

---

## Subscriptions

Subscriptions allow external systems to receive event notifications when
registry records change. Events are delivered to a configured endpoint;
delivery semantics depend on the endpoint kind.

### Endpoint kinds

| Kind | Delivery | Durability |
|------|----------|------------|
| `xolu` | HTTP POST to xolu instance | Best-effort |
| `webhook` | HTTP POST to arbitrary URL | Best-effort |
| `nats` | Publish to NATS subject | Durable (JetStream) |
| `activitypub` | POST to ActivityPub inbox | Best-effort |

### Event payload delivered to endpoints

```json
{
  "id": "uuid",
  "subject": "nolu.events.transferred.devices",
  "global_id": "nolu://registry.acme.com/devices/01920d4e-...",
  "kind": "transferred",
  "entity_type": "devices",
  "at": "2026-05-06T22:00:00Z",
  "payload": { "...full registry record..." }
}
```

---

### Create a subscription

```http
POST /subscriptions
Content-Type: application/json
```

**Request**

```json
{
  "subscriber_id": "http://xolu-retailchain:9091",
  "scope": {
    "global_ids": [
      "nolu://registry.acme.com/devices/01920d4e-..."
    ],
    "event_kinds": ["transferred", "retired"]
  },
  "endpoint": {
    "kind": "webhook",
    "url": "http://xolu-retailchain:9091/nolu/events"
  }
}
```

`scope.global_ids` and `scope.entity_types` may both be set; events matching
either are delivered. `scope.event_kinds` defaults to all kinds if omitted.

For a NATS endpoint:

```json
{
  "endpoint": {
    "kind": "nats",
    "url": "nats://nats:4222",
    "subject": "retail.nolu.events"
  }
}
```

For an authenticated endpoint, include `"auth_token"` in the endpoint object.
It is stored encrypted and never returned in GET responses.

**Response** `201 Created`

```json
{
  "id": "sub-uuid",
  "subscriber_id": "http://xolu-retailchain:9091",
  "scope": { "...": "..." },
  "endpoint": { "kind": "webhook", "url": "http://..." },
  "active": true,
  "created_at": "2026-05-06T21:00:00Z"
}
```

---

### Get a subscription

```http
GET /subscriptions/{subscription_id}
```

**Response** `200 OK` — subscription record. `auth_token` is never returned.

**Errors:** `NOLU-SW001` (not found)

---

### Delete a subscription

```http
DELETE /subscriptions/{subscription_id}
```

**Response** `204 No Content`

**Errors:** `NOLU-SW001` (not found)

---

### List subscriptions by subscriber

```http
GET /subscriptions?subscriber_id={id}
```

**Response** `200 OK`

```json
{
  "subscriber_id": "http://xolu-retailchain:9091",
  "count": 3,
  "subscriptions": [ { "...": "..." } ]
}
```

---

### Pause a subscriber

Suspends delivery to all endpoints for the given subscriber. Events accumulate
in the NATS stream (up to the stream's retention limit) and are replayed
when the subscriber resumes. Webhook and ActivityPub events are dropped
while paused.

```http
POST /subscriptions/pause
Content-Type: application/json
```

**Request**

```json
{ "subscriber_id": "http://xolu-retailchain:9091" }
```

**Response** `200 OK`

```json
{ "subscriber_id": "http://xolu-retailchain:9091", "active": false }
```

---

### Resume a subscriber

```http
POST /subscriptions/resume
Content-Type: application/json
```

**Request**

```json
{ "subscriber_id": "http://xolu-retailchain:9091" }
```

**Response** `200 OK`

```json
{ "subscriber_id": "http://xolu-retailchain:9091", "active": true }
```

---

## Utility

### Health check

```http
GET /health
```

**Response** `200 OK`

```json
{
  "status": "ok",
  "version": "0.1.9"
}
```

---

### Version

```http
GET /version
```

**Response** `200 OK`

```json
{
  "version": "0.1.9",
  "registry_host": "registry.acme.com",
  "bus": "nats"
}
```

---

## Design notes

**GlobalIDs in URLs.** The `nolu://` scheme contains characters that must be
percent-encoded in path segments. Clients must URL-encode the GlobalID before
embedding it in a path. Alternatively, the GlobalID may be passed as a query
parameter where encoding requirements are the same but the intent is clearer:

```
GET /registry?global_id=nolu%3A%2F%2Fregistry.acme.com%2Fdevices%2F...
```

The implementation will support both forms.

**Idempotency.** `POST /transfers/{id}/complete` and `POST /transfers/{id}/cancel`
are idempotent: calling them on an already-completed or already-cancelled
proposal returns 200 with the current record rather than an error.
`POST /registry/{id}/retire` is idempotent in the same way.

**Pagination.** List endpoints currently return all results. Pagination
(`?page=N&per_page=N`) will be added when the persistent registry backend
is implemented.

**Authentication.** Not defined in v0.1. The implementation will add bearer
token authentication as a configuration option, consistent with xolu's
`OLU_AUTH_TYPE` model.
