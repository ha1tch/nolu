# Graph Layer Invariants

This document describes the invariants enforced by the three mutation methods
in `pkg/graph/flat_graph.go`, why they matter, and what to do when adding
a new one.

---

## The three methods and what they enforce

All mutations to the node map and type index go through one of these three
methods on `FlatGraph`. They are the single point of enforcement for every
correctness property the graph layer provides. The lock (`mu sync.RWMutex`)
is acquired by the public caller; the unexported `*Locked` variants assume
the lock is already held.

### `addNodeLocked(nodeID, nodeType string) error`

| Invariant | Consequence of bypassing |
|-----------|-----------------------------|
| `ErrMalformedNodeID` — rejects IDs with `@` that lack a valid `XXXX@` tenant prefix | Corrupt node enters graph; invisible to tenant isolation machinery; can produce ghost entries in tenant-scoped queries |
| `nodeCounters` — increments the per-tenant counter for new nodes | `NodeCountForTenant` returns stale (low) values; stats dashboard shows wrong node counts |
| Type index (`g.index`) — records `nodeID` under `nodeType` when `nodeType != ""`, even for nodes that already exist | `GetNodesByType` / `GetNodesByTypeForTenant` returns empty results for nodes whose type was assigned after implicit creation |

### `addEdgeLocked(from, to, relationship string) error`

| Invariant | Consequence of bypassing |
|-----------|-----------------------------|
| `ErrCrossTenantEdge` — endpoints with different non-empty tenant prefixes rejected | Edge silently bridges two tenants; traversal leaks data across tenant boundary |
| `ErrMalformedNodeID` via `addNodeLocked` — both endpoints validated | Malformed IDs enter graph through edge creation even if `AddNode` would reject them |
| `nodeCounters` via `addNodeLocked` — both endpoints created and counted if absent | Implicitly created nodes not counted; `NodeCountForTenant` returns stale values |
| Cycle detection — `wouldCreateCycle` checked before any state mutation | Cycle enters graph before rejection in `"error"` mode |
| `ErrEdgeAlreadyExists` — same pair with different relationship name rejected | Silent relationship rename; stale relationship label stored |
| `edgeCount` — incremented for genuinely new edges only | `EdgeCount` / `EdgeCountForTenant` over-counts |
| Incoming index (`rec.in`) — target node's incoming set updated | `GetIncomingEdges` returns incomplete results; degree counts wrong |

### `RemoveEdge(from, to string) error`

| Invariant | Consequence of bypassing |
|-----------|-----------------------------|
| `edgeCount` — decremented unconditionally when edge existed | Counter underflows; poisons subsequent edge counts |
| Incoming index — `toRec.in[from]` always cleaned up | Stale reverse entries survive after edge removal; `GetIncomingEdges` returns phantom predecessors |

---

## When you add a new invariant

All mutation state is private to `FlatGraph` (`g.nodes`, `g.index`,
`g.nodeCounters`, `g.edgeCount`). Writes from outside the
`addNodeLocked` / `addEdgeLocked` / `RemoveEdge` methods are not
automatically prevented by the compiler, so discipline is required.

To add a new invariant:

1. Add the check to the appropriate unexported method (`addNodeLocked`,
   `addEdgeLocked`) or to `RemoveEdge` directly.
2. Add a test in `pkg/graph/graph_contract_test.go` verifying the new
   invariant through the `Graph` interface.
3. Run `go test ./pkg/graph/...`.

To audit that no direct map writes bypass the helpers:

```bash
# All writes to g.nodes outside addNodeLocked should be initialisation only.
grep -n 'g\.nodes\[' pkg/graph/flat_graph.go
# All writes to g.index outside addNodeLocked should be initialisation only.
grep -n 'g\.index\[' pkg/graph/flat_graph.go
```

---

## The load path pattern

`Load` resets all state to empty and then replays every node and edge
through `addNodeLocked` / `addEdgeLocked`, exactly as at runtime. This
ensures all invariants — counters, indexes, cycle state — are rebuilt
correctly from the file contents without a separate reconciliation pass.

If you add a load path it must follow this pattern. Direct map assignment
during load bypasses every invariant listed above.

### Cycle-detection policy persistence

Since v0.9.7-patched40, `flatGraphData` includes `cycle_detection` and
`cycle_check_limit` fields. `Save` writes them; `Load` restores them when
present. Files from older versions that lack these keys are still valid —
`Load` leaves the constructor-supplied mode (`NewFlatGraphWithCycleDetection`)
intact for absent fields. This means a graph configured with `"error"` mode
will resume that mode after restart without any extra caller configuration.

Since v0.9.7-patched42, `Load` validates the `cycle_detection` value against
the three legal modes (`"ignore"`, `"warn"`, `"error"`) and returns an error
for any other value. Previously an unrecognised mode was silently stored;
`addEdgeLocked`'s switch has no default case, so a bad mode would trigger the
cycle-detection path but then admit the cycle silently.

---

## Error message hygiene

Node IDs passed to callers in error messages must not contain the `XXXX@`
tenant prefix, which is an internal implementation detail. Use
`tenant.NodeIDStripped(nodeID)` in any `fmt.Errorf` or `errors.New` call
that will be returned to a caller outside the graph package. The internal
`log.Printf` lines may retain full node IDs for diagnostics.

The contract was tightened in v0.9.7-patched42:
- `FlatGraph.CommonNeighbors` error (node not found)
- `FlatGraph.UpdateFromEntityForTenant` relabel/rollback errors

---

## `CommonNeighbors` return contract

`FlatGraph.CommonNeighbors` always returns a non-nil slice (guaranteed since
v0.9.7-patched42). Callers must not add `if result == nil` guards — the
guarantee belongs in the implementation, not at each call site. The `Graph`
interface documents this explicitly. Any future re-implementation must honour
the same contract.

---

## History note

Prior to v0.9.7-patched38, the graph layer had two implementations:
`IndexedGraph` (backed by a `pkg/graph/state` sub-package) and `FlatGraph`.
`IndexedGraph` and `pkg/graph/state` were removed in patched38; `FlatGraph`
is now the sole implementation. The design history of the state sub-package
is in `CHANGELOG.md` entries for v0.9.5–v0.9.7.

**v0.9.7-patched40 counter-correction fix:** `UpdateFromEntityForTenant`
contained an inverted counter correction in the double-failure path of the
edge-relabel code. When both the re-add and the rollback restore failed,
the code incremented `edgeCount`/`edgeCounters` — the opposite of the
comment's stated intent. The counters were already consistent (decremented)
and the erroneous increments caused a permanent over-count. Fixed in
patched40 by removing the two increment lines.

**v0.9.7-patched42 correctness and hygiene sweep:** (a) `CommonNeighbors`
now returns a non-nil empty slice instead of nil when there is no overlap;
(b) `Load` rejects unrecognised `cycle_detection` values rather than
silently accepting them; (c) `guardEdgeMap` allocates a fresh map instead
of mutating its argument; (d) error messages from `CommonNeighbors` and
`UpdateFromEntityForTenant` rollback paths now strip the `XXXX@` tenant
prefix via `tenant.NodeIDStripped`; (e) `handleTenantGraphPath` and
`handleTenantGraphShortestPath` share a `tenantPathResult` helper that
guarantees `length >= 0`.
