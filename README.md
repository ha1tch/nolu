# nolu

**Federated entity registry for xolu**

nolu is the horizontal scaling layer for [xolu](https://github.com/ha1tch/xolu). It enables a network of xolu instances — each sovereign, isolated, and independently operated — to coordinate across organisational boundaries without centralising data.

## What nolu does

Within a single xolu instance, entities are identified by `(entity_type, id)` pairs. These identifiers are local. Two separate xolu instances may each have a `devices:42` with no relationship to each other.

nolu assigns every entity a **GlobalID**: a stable URI that persists across ownership changes, instance migrations, and tenant reassignments:

```
nolu://registry.acme.com/devices/01920d4e-9f3b-7a2c-8e1f-4b5c6d7e8f9a
```

nolu owns this identity. Everything else stays inside xolu.

## What nolu does not do

- **Store entity data.** That stays in xolu.
- **Orchestrate xolu instances.** Each instance remains sovereign.
- **Replicate data across tenants.** Cross-tenant traversal happens through the routing and subscription model, not data copying.

## Core concepts

### GlobalID
A stable, portable URI for an entity across all xolu instances in a nolu federation. Once minted, never reused.

### LocalRef
The xolu-side handle: instance URL + tenant ID + entity type + local integer ID. The registry maps GlobalID ↔ LocalRef and updates the mapping when entities change hands.

### Registry
The clearinghouse. Owns the GlobalID → LocalRef mapping and the complete ownership history. Both parties in a transfer trust the registry's record.

### Transfer protocol
Asset transfers are first-class events with a negotiated lifecycle:

```
PROPOSED → ACCEPTED → COMPLETED
         ↘ REJECTED
PROPOSED → CANCELLED
```

History portability (how much of an entity's event stream travels with it) is negotiated between PROPOSED and ACCEPTED.

### Event bus
The registry emits events on subject patterns:

```
nolu.events.<kind>.<entity-type>
```

The bus is substrate-agnostic: in-process channels for development, NATS JetStream for production, aulsql as a future option.

### Router
Maintains the subscription table. Subscribers watch GlobalIDs or entity types; the router delivers events to matching endpoints (xolu instances, ActivityPub inboxes, webhooks, NATS subjects).

## Configuration

```bash
nolu -host registry.acme.com \
     -listen :7070 \
     -bus nats \
     -nats nats://localhost:4222 \
     -storage xolu \
     -xolu http://localhost:9090
```

| Flag       | Env                  | Default     | Description                        |
|------------|----------------------|-------------|------------------------------------|
| `-host`    | `NOLU_REGISTRY_HOST` | `localhost` | Registry hostname for GlobalIDs    |
| `-listen`  | `NOLU_LISTEN_ADDR`   | `:7070`     | HTTP API listen address            |
| `-bus`     | `NOLU_BUS_TYPE`      | `memory`    | Event bus: `memory` or `nats`      |
| `-nats`    | `NOLU_NATS_URL`      | —           | NATS server URL                    |
| `-storage` | `NOLU_STORAGE_TYPE`  | `memory`    | Storage: `memory` or `xolu`        |
| `-xolu`    | `NOLU_XOLU_URL`      | —           | xolu instance URL                  |
| `-log`     | `NOLU_LOG_LEVEL`     | `info`      | Log level                          |

## Status

v0.1.0 — skeleton. All interfaces defined; concrete implementations pending.

```
pkg/identity   ✓ GlobalID, LocalRef, OwnershipRecord, Transfer
pkg/registry   ✓ Registry interface
pkg/events     ✓ Bus interface, NoopBus, MemoryBus, ActivityPubBridge placeholder
pkg/routing    ✓ Router interface, Subscription types
pkg/transfer   ✓ Negotiator interface, Proposal lifecycle
cmd/nolu       ✓ Entry point, flag parsing, graceful shutdown
               ☐ HTTP API (next)
               ☐ MemoryRegistry implementation (next)
               ☐ XoluRegistry implementation
               ☐ NATSBus implementation
```

## Relation to xolu

nolu depends on xolu's models and storage interfaces but does not embed xolu. A nolu instance uses a dedicated xolu deployment as its own durable store for registry records — giving nolu's data the same graph, OQL, and timeseries capabilities as any other xolu deployment.

xolu instances do not depend on nolu. A standalone xolu deployment has no knowledge of nolu. nolu is an optional federation layer, not a required component.

## License

Copyright (c) 2026 haitch  
Apache License 2.0 — https://www.apache.org/licenses/LICENSE-2.0
