// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package graph

import "errors"

// Sentinel errors. Previously re-exported from pkg/graph/state; now defined
// here directly after the removal of IndexedGraph and the state package.
var (
	ErrCycleDetected    = errors.New("adding this edge would create a cycle")
	ErrCrossTenantEdge  = errors.New("edge endpoints belong to different tenants")
	ErrMalformedNodeID  = errors.New("node ID contains '@' but is not a valid XXXX@-prefixed ID")
	// ErrEdgeAlreadyExists is returned by AddEdge when an edge from→to already
	// exists with a *different* relationship label. The same pair with the same
	// label is a no-op (idempotent); only label conflicts trigger this.
	//
	// NOTE: this sentinel is also used internally by UpdateFromEntityForTenant
	// as a signal that an existing edge needs its label updated. External callers
	// should only see it when they call AddEdge directly. If the semantics of
	// this error ever change, UpdateFromEntityForTenant must be audited too.
	ErrEdgeAlreadyExists = errors.New("edge already exists with a different relationship name")

	// ErrNodeTypeMismatch is returned by AddNode when the node already exists
	// with an established non-vode type and the caller supplies a different type.
	// Silently retyping a node corrupts the type index and is almost certainly a
	// caller bug.
	//
	// Vode nodes (type NodeTypeVode, created implicitly by AddEdge as forward-
	// reference placeholders) are explicitly exempt: promoting a vode to a real
	// type via AddNode is the intended completion path and does not trigger this
	// error.
	ErrNodeTypeMismatch = errors.New("node already exists with a different type")
)

// NodeTypeVode is the synthetic type assigned to a graph node that was created
// implicitly by AddEdge as a forward-reference placeholder. A vode represents
// a node that has been pointed at by a REF field but whose entity data has not
// yet arrived (e.g. during streaming graph hydration where edges may be
// replayed before their target entities are written).
//
// Vode lifecycle:
//
//  1. AddEdge("a:1", "b:1", "R") is called when "b:1" does not exist yet.
//     addEdgeLocked creates "b:1" with type NodeTypeVode as a placeholder.
//     VodeCount increments.
//
//  2. The entity for "b:1" is later written and UpdateFromEntity / AddNode
//     is called with the real type. addNodeLocked detects the promotion from
//     NodeTypeVode, removes the node from the vode type index, adds it to the
//     real type index, and decrements VodeCount.
//
//  3. At the end of successful hydration, VodeCount() should be zero.
//     A non-zero count indicates dangling references — entity data that was
//     referenced by a REF field but never written to the store.
//
// The string value "__vode__" is intentionally non-colliding with any
// valid olu entity type name (which may not contain underscores by convention).
// "Vode" is a domain-specific term; it is not an acronym and should not be
// confused with REF, which is the *edge* pointing at a vode node.
const NodeTypeVode = "__vode__"

// Graph interface defines all graph operations. The interface is intentionally
// broad so that the server layer never needs to type-assert to a concrete
// implementation.
type Graph interface {
	// Mutation
	AddNode(nodeID string, nodeType string) error
	RemoveNode(nodeID string) error
	AddEdge(from, to, relationship string) error
	RemoveEdge(from, to string) error
	// CheckEdge runs the same pre-flight checks as AddEdge (cross-tenant guard,
	// cycle detection) without modifying the graph. Returns nil if the edge
	// would be accepted, or the same error that AddEdge would return.
	// Use this to validate edges before committing the owning entity to storage,
	// so that a cycle-detection rejection cannot leave the SQLite edge table and
	// the in-memory graph in disagreement.
	CheckEdge(from, to, relationship string) error
	UpdateFromEntityForTenant(tenantID uint16, entity string, id int, data map[string]interface{}) error
	UpdateFromEntity(entity string, id int, data map[string]interface{}) error

	// Traversal
	GetNeighbors(nodeID string) (map[string]string, error)
	GetIncomingEdges(nodeID string) (map[string]string, error)
	FindPath(from, to string, maxDepth int) ([]string, error)
	PathExists(from, to string, maxDepth int) (bool, int, error)
	// SharedOutNeighbors returns the nodes that both nodeA and nodeB point to
	// via outgoing edges — i.e. shared out-neighbours in a directed graph.
	// Nodes that point *to* nodeA or nodeB (incoming edges) are not
	// considered. Returns an empty (non-nil) slice when there is no overlap.
	// Previously named CommonNeighbors; renamed to match graph-theory convention
	// (shared directed out-neighbours, not undirected common neighbours).
	SharedOutNeighbors(nodeA, nodeB string) ([]string, error)

	// Node queries
	NodeExists(nodeID string) bool
	GetNodeInfo(nodeID string) (*NodeInfo, error)
	GetNodesByType(entityType string) []string
	GetAllNodes() []string
	GetDegree(nodeID string) (Degree, error)

	// Metrics
	NodeCount() int
	EdgeCount() int
	HasCycle() bool
	// HasCycleForTenant reports whether the subgraph for the given tenant
	// prefix contains a directed cycle. It performs a DFS scoped to nodes
	// carrying that prefix; nodes from other tenants are not visited.
	// Returns an error if tenantPrefix is empty (same guard as the other
	// *ForTenant methods).
	HasCycleForTenant(tenantPrefix string) (bool, error)

	// VodeCount returns the total number of vode (placeholder) nodes currently
	// in the graph across all tenants. Should be zero after successful hydration.
	VodeCount() int
	// VodeCountForTenant returns the vode count for a single tenant. Returns
	// ErrTenantRequired if tenantPrefix is empty.
	VodeCountForTenant(tenantPrefix string) (int, error)

	// Tenant-scoped queries
	NodeCountForTenant(tenantPrefix string) (int, error)
	EdgeCountForTenant(tenantPrefix string) (int, error)
	GetAllNodesForTenant(tenantPrefix string) ([]string, error)
	GetNodesByTypeForTenant(tenantPrefix, entityType string) ([]string, error)

	// Persistence
	Save(filename string) error
	Load(filename string) error
	Clear() error
}

// DefaultCycleCheckLimit is the default BFS node-visit budget for
// wouldCreateCycle. It is exported so that callers (e.g. the startup
// diagnostic print) can display the effective default without hard-coding it.
// Override at runtime via FlatGraph.SetCycleCheckLimit or the
// OLU_GRAPH_CYCLE_CHECK_LIMIT environment variable.
const DefaultCycleCheckLimit = 512

// CycleCheckBudgetExceeded is the sentinel returned when wouldCreateCycle
// exhausts its BFS budget without confirming a cycle. This is indistinguishable
// from a true cycle at the API level: AddEdge returns ErrCycleDetected and the
// HTTP handler surfaces a 409. Operators and callers should be aware that on
// large or dense graphs the default budget of DefaultCycleCheckLimit (512)
// visited nodes may be exceeded by a legitimate (non-cyclic) edge addition.
// The limit is configurable via FlatGraph.SetCycleCheckLimit or the
// OLU_GRAPH_CYCLE_CHECK_LIMIT environment variable. This behaviour is
// documented here and in GRAPH_API.md so that callers can reason about it.
//
// Note: this constant is informational only — the code uses cycleCheckLimit
// directly. It is not returned as an error value; use ErrCycleDetected to
// detect both true cycles and budget-exhaustion rejections.
const CycleCheckBudgetExceeded = "cycle-check-budget-exceeded" // informational tag only

type Degree struct {
	In    int `json:"in"`
	Out   int `json:"out"`
	Total int `json:"total"`
}

type NodeInfo struct {
	ID       string            `json:"id"`
	Entity   string            `json:"entity"`
	EntityID int               `json:"entity_id"`
	Outgoing map[string]string `json:"outgoing"`
	Incoming map[string]string `json:"incoming"`
	Degree   Degree            `json:"degree"`
}
