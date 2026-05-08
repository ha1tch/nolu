// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package graph

// FlatGraph is the Graph implementation whose core data structure is a single
// map[string]*nodeRecord. One pointer dereference yields the node's type, all
// outgoing edges, and all incoming edges — no parallel maps holding the same
// string in different forms.
//
// FlatGraph supports multi-tenant deployments. All tenants' nodes coexist in
// the same map, distinguished by their XXXX@ prefix (e.g. "0001@items:42").
// The *ForTenant methods filter by prefix so tenant A cannot see tenant B's
// nodes through stats or enumeration endpoints. Individual traversals are
// always prefix-correct because the caller supplies the full node ID.
//
// FlatGraph is safe for concurrent use via a single sync.RWMutex.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/rs/zerolog"

	oluerr "github.com/ha1tch/xolu/pkg/errors"
	"github.com/ha1tch/xolu/pkg/models"
	"github.com/ha1tch/xolu/pkg/tenant"
)

// nodeRecord holds everything known about a single node.
type nodeRecord struct {
	typ string
	out map[string]string // neighbour -> relationship label
	in  map[string]string // source -> relationship label
}

// FlatGraph implements Graph with a single node map per instance.
type FlatGraph struct {
	nodes           map[string]*nodeRecord
	index           map[string]map[string]struct{} // entityType -> set of nodeIDs
	tenantNodes      map[string]map[string]struct{} // tenant prefix -> set of nodeIDs; O(1) per-tenant enumeration
	mu              sync.RWMutex
	loadMu          sync.Mutex   // serialises concurrent Load calls
	edgeCount       int
	nodeCounters    map[string]int // tenant prefix -> count; "" = tenant-0
	edgeCounters    map[string]int // tenant prefix -> count; "" = tenant-0
	vodeCounters    map[string]int // tenant prefix -> vode (placeholder) node count
	cycleDetection  string
	cycleCheckLimit int
	logger          zerolog.Logger
}

// Compile-time assertion.
var _ Graph = (*FlatGraph)(nil)

// newFlatGraph is the internal constructor; all public constructors delegate here.
// If mode is not "ignore", "warn", or "error" a warning is printed to stderr
// and the mode falls back to "ignore".
func newFlatGraph(logger zerolog.Logger, mode string) *FlatGraph {
	switch mode {
	case "ignore", "warn", "error":
	default:
		fmt.Fprintf(os.Stderr,
			"olu/graph: unknown cycle_detection mode %q; defaulting to \"ignore\"\n", mode)
		mode = "ignore"
	}
	return &FlatGraph{
		nodes:           make(map[string]*nodeRecord),
		index:           make(map[string]map[string]struct{}),
		tenantNodes:     make(map[string]map[string]struct{}),
		nodeCounters:    make(map[string]int),
		edgeCounters:    make(map[string]int),
		vodeCounters:    make(map[string]int),
		cycleDetection:  mode,
		cycleCheckLimit: DefaultCycleCheckLimit,
		logger:          logger,
	}
}

// NewFlatGraph returns an empty FlatGraph with cycle detection disabled.
func NewFlatGraph() *FlatGraph {
	return newFlatGraph(zerolog.Nop(), "ignore")
}

// NewFlatGraphWithLogger returns a FlatGraph that emits structured log events
// via the supplied zerolog.Logger. Use zerolog.Nop() (the default) to suppress
// all graph-layer logging.
func NewFlatGraphWithLogger(logger zerolog.Logger) *FlatGraph {
	return newFlatGraph(logger, "ignore")
}

// NewFlatGraphWithCycleDetection returns a FlatGraph with the given cycle
// detection mode ("ignore", "warn", or "error"). An unrecognised mode prints
// a warning to stderr and falls back to "ignore".
func NewFlatGraphWithCycleDetection(mode string) *FlatGraph {
	return newFlatGraph(zerolog.Nop(), mode)
}

// SetCycleCheckLimit sets the maximum number of unique nodes the BFS in
// wouldCreateCycle may visit before returning true conservatively.
// A value of 0 restores the package default (DefaultCycleCheckLimit = 512).
// Negative values are treated as 0 (i.e. the default is used).
// This method is safe to call concurrently; it acquires the write lock.
// It is the runtime counterpart of the OLU_GRAPH_CYCLE_CHECK_LIMIT config key.
func (g *FlatGraph) SetCycleCheckLimit(n int) {
	if n <= 0 {
		n = DefaultCycleCheckLimit
	}
	g.mu.Lock()
	g.cycleCheckLimit = n
	g.mu.Unlock()
}

// ── Mutation ──────────────────────────────────────────────────────────────────

func (g *FlatGraph) AddNode(nodeID, nodeType string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.addNodeLocked(nodeID, nodeType)
}

func (g *FlatGraph) RemoveNode(nodeID string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	rec, exists := g.nodes[nodeID]
	if !exists {
		return nil
	}
	nodePrefix := tenant.NodeIDPrefix(nodeID)
	// Remove all outgoing edges; update the in-maps of neighbours.
	for neighbour := range rec.out {
		if nr, ok := g.nodes[neighbour]; ok {
			delete(nr.in, nodeID)
		}
		g.edgeCount--
		g.edgeCounters[nodePrefix]--
	}
	// Remove all incoming edges; update the out-maps of sources.
	for source := range rec.in {
		if sr, ok := g.nodes[source]; ok {
			delete(sr.out, nodeID)
		}
		// Decrement unconditionally — mirrors the outgoing block above.
		// The edge exists in rec.in regardless of whether the source node
		// record is still present (e.g. partially-loaded state), so the
		// counter must always be adjusted to avoid drift.
		g.edgeCount--
		g.edgeCounters[tenant.NodeIDPrefix(source)]--
	}
	// Remove from type index.
	if rec.typ != "" {
		if s := g.index[rec.typ]; s != nil {
			delete(s, nodeID)
			if len(s) == 0 {
				delete(g.index, rec.typ)
			}
		}
	}
	if rec.typ == NodeTypeVode {
		g.vodeCounters[nodePrefix]--
	}
	delete(g.nodes, nodeID)
	g.nodeCounters[nodePrefix]--
	if s := g.tenantNodes[nodePrefix]; s != nil {
		delete(s, nodeID)
		if len(s) == 0 {
			delete(g.tenantNodes, nodePrefix)
		}
	}
	return nil
}

// CheckEdge runs the same pre-flight checks as AddEdge without modifying the
// graph. It is safe to call concurrently. Returns nil if AddEdge would accept
// the edge, or the first error AddEdge would return.
//
// Note: there is an inherent TOCTOU gap between CheckEdge and a subsequent
// AddEdge call — a concurrent write could change the graph state in between.
// CheckEdge is intended for use as a guard before an external storage write
// (e.g. SQLite); false rejections are possible under high concurrency but are
// safe (the write is simply refused). False acceptances are not possible for
// single-threaded or low-concurrency workloads.
func (g *FlatGraph) CheckEdge(from, to, relationship string) error {
	g.mu.RLock()
	defer g.mu.RUnlock()

	// Cross-tenant check (same logic as addEdgeLocked).
	fromPrefix := tenant.NodeIDPrefix(from)
	toPrefix := tenant.NodeIDPrefix(to)
	if fromPrefix != "" && toPrefix != "" && fromPrefix != toPrefix {
		return fmt.Errorf("%w: %q -> %q", ErrCrossTenantEdge, from, to)
	}

	// Duplicate-edge check against current state.
	if rec, ok := g.nodes[from]; ok {
		if existing, exists := rec.out[to]; exists {
			if existing == relationship {
				return nil // idempotent — already exists with same label
			}
			return ErrEdgeAlreadyExists
		}
	}

	// Cycle detection (only meaningful when cycleDetection == "error").
	if g.cycleDetection == "error" {
		if g.wouldCreateCycle(from, to) {
			return ErrCycleDetected
		}
	}

	return nil
}

func (g *FlatGraph) AddEdge(from, to, relationship string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.addEdgeLocked(from, to, relationship)
}

func (g *FlatGraph) addEdgeLocked(from, to, relationship string) error {
	// Reject cross-tenant edges.
	// Only reject when *both* endpoints carry a non-empty, non-matching prefix.
	// Tenant-0 (bare "entity:id", no prefix) acts as a shared global namespace;
	// an edge between a tenant-0 node and a non-zero-tenant node is permitted.
	// This is intentional — tenant-0 is used for unscoped / system-level nodes
	// that legitimately connect to per-tenant nodes.
	fromPrefix := tenant.NodeIDPrefix(from)
	toPrefix := tenant.NodeIDPrefix(to)
	if fromPrefix != "" && toPrefix != "" && fromPrefix != toPrefix {
		return fmt.Errorf("%w: %q -> %q", ErrCrossTenantEdge, from, to)
	}

	// Ensure both nodes exist; addNodeLocked enforces ErrMalformedNodeID.
	// Nodes created here that do not yet exist are vode (placeholder) nodes —
	// forward references whose entity data has not arrived yet. They carry
	// NodeTypeVode so that they are visible in the type index and countable.
	if err := g.addNodeLocked(from, NodeTypeVode); err != nil {
		return err
	}
	if err := g.addNodeLocked(to, NodeTypeVode); err != nil {
		return err
	}
	fromRec := g.nodes[from]
	toRec := g.nodes[to]

	if existing, exists := fromRec.out[to]; exists {
		if existing == relationship {
			return nil // idempotent
		}
		return ErrEdgeAlreadyExists
	}

	// Cycle detection.
	if g.cycleDetection != "ignore" {
		if g.wouldCreateCycle(from, to) {
			switch g.cycleDetection {
			case "error":
				return ErrCycleDetected
			case "warn":
				g.logger.Warn().Str("from", tenant.NodeIDStripped(from)).Str("to", tenant.NodeIDStripped(to)).Msg("FlatGraph.AddEdge: cycle detected")
				// Intentional fall-through: in "warn" mode the edge is still added.
				// The caller is informed via the log but the mutation proceeds.
			}
		}
	}

	fromRec.out[to] = relationship
	toRec.in[from] = relationship
	g.edgeCount++
	g.edgeCounters[fromPrefix]++
	return nil
}

func (g *FlatGraph) RemoveEdge(from, to string) error {
	g.mu.Lock()
	defer g.mu.Unlock()
	fromRec, ok := g.nodes[from]
	if !ok {
		return nil
	}
	if _, exists := fromRec.out[to]; !exists {
		return nil
	}
	delete(fromRec.out, to)
	if toRec, ok := g.nodes[to]; ok {
		delete(toRec.in, from)
	}
	g.edgeCount--
	g.edgeCounters[tenant.NodeIDPrefix(from)]--
	return nil
}

// wouldCreateCycle reports whether adding from→to would create a cycle.
// Caller must hold at least a read lock.
//
// BFS from `to`; if we reach `from`, a cycle would be created.
// Budget is measured by unique nodes visited (len(visited)), not total
// dequeues — this is the canonical metric, chosen because bushy graphs
// with many parallel paths over-counted with the old `steps` variable,
// triggering conservative rejection earlier than intended.
func (g *FlatGraph) wouldCreateCycle(from, to string) bool {
	if from == to {
		return true
	}
	fromPrefix := tenant.NodeIDPrefix(from)
	visited := make(map[string]struct{})
	queue := []string{to}
	head := 0
	for head < len(queue) {
		cur := queue[head]
		head++
		if cur == from {
			return true
		}
		if g.cycleCheckLimit > 0 && len(visited) >= g.cycleCheckLimit {
			return true // budget exhausted — conservatively assume cycle
		}
		if _, seen := visited[cur]; seen {
			continue
		}
		visited[cur] = struct{}{}
		if rec, ok := g.nodes[cur]; ok {
			for neighbour := range rec.out {
				// Skip neighbours from a different non-zero tenant: cross-tenant
				// edges cannot exist, so they can never complete a cycle.
				npfx := tenant.NodeIDPrefix(neighbour)
				if npfx != "" && fromPrefix != "" && npfx != fromPrefix {
					continue
				}
				if _, seen := visited[neighbour]; !seen {
					queue = append(queue, neighbour)
				}
			}
		}
	}
	return false
}

func (g *FlatGraph) UpdateFromEntityForTenant(tenantID uint16, entity string, id int, data map[string]interface{}) error {
	nodeID := tenant.NodeID(tenantID, entity, id)
	rawEdges, err := models.ExtractEntityEdges(data)
	if err != nil {
		return err
	}
	newEdges := make(map[string]string, len(rawEdges))
	for _, ee := range rawEdges {
		targetNodeID := tenant.NodeID(tenantID, ee.TargetEntity, ee.TargetID)
		newEdges[targetNodeID] = ee.Relationship
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := g.addNodeLocked(nodeID, entity); err != nil {
		return err
	}
	rec := g.nodes[nodeID]
	nodePrefix := tenant.NodeIDPrefix(nodeID)
	// Remove edges that no longer exist.
	for oldTarget := range rec.out {
		if _, stillExists := newEdges[oldTarget]; !stillExists {
			delete(rec.out, oldTarget)
			if toRec, ok := g.nodes[oldTarget]; ok {
				delete(toRec.in, nodeID)
			}
			g.edgeCount--
			g.edgeCounters[nodePrefix]--
		}
	}
	// Add or update edges.
	for targetNodeID, relationship := range newEdges {
		if err := g.addEdgeLocked(nodeID, targetNodeID, relationship); err != nil {
			if !errors.Is(err, ErrEdgeAlreadyExists) {
				return err
			}
			// ErrEdgeAlreadyExists here means the edge exists but the label has
			// changed. We use the sentinel as internal control flow: delete the
			// old edge and re-add with the new label, with counter-safe rollback.
			// See the note on ErrEdgeAlreadyExists in graph.go.
			oldRel := rec.out[targetNodeID]
			delete(rec.out, targetNodeID)
			if toRec, ok := g.nodes[targetNodeID]; ok {
				delete(toRec.in, nodeID)
			}
			g.edgeCount--
			g.edgeCounters[nodePrefix]--
			if addErr := g.addEdgeLocked(nodeID, targetNodeID, relationship); addErr != nil {
				if restoreErr := g.addEdgeLocked(nodeID, targetNodeID, oldRel); restoreErr != nil {
					// Both the re-add and the restore failed. The edge is gone from the
					// graph data and the counters were already decremented — they are
					// already consistent with the actual (edgeless) state. Do not
					// increment them here.
					g.logger.Warn().
						Str("from", tenant.NodeIDStripped(nodeID)).
						Str("to", tenant.NodeIDStripped(targetNodeID)).
						Str("old_rel", oldRel).
						Err(restoreErr).
						Msg("FlatGraph.UpdateFromEntityForTenant: failed to restore edge; edge removed, counters consistent")
					// Return a wrapped error that makes the double-failure explicit.
					// The caller cannot distinguish "edge gone" from "edge intact"
					// from addErr alone; wrapping restoreErr preserves both.
					// Node IDs are stripped of any XXXX@ prefix so the message is
					// safe to surface to callers without leaking tenant internals.
					return fmt.Errorf("relabel %s->%s to %q failed and restore of %q also failed (%v): %w",
						tenant.NodeIDStripped(nodeID), tenant.NodeIDStripped(targetNodeID),
						relationship, oldRel, restoreErr, addErr)
				}
				// Re-add failed but restore succeeded: the original edge is intact.
				// Wrap the error so the caller knows the graph is consistent.
				return fmt.Errorf("relabel %s->%s to %q failed (original relationship %q preserved): %w",
					tenant.NodeIDStripped(nodeID), tenant.NodeIDStripped(targetNodeID),
					relationship, oldRel, addErr)
			}
		}
	}
	return nil
}

// addNodeLocked is AddNode without the mutex — callers must hold the write lock.
func (g *FlatGraph) addNodeLocked(nodeID, nodeType string) error {
	if strings.IndexByte(nodeID, '@') >= 0 && tenant.NodeIDPrefix(nodeID) == "" {
		return fmt.Errorf("%w: %q", ErrMalformedNodeID, nodeID)
	}
	rec, exists := g.nodes[nodeID]
	if !exists {
		rec = &nodeRecord{out: make(map[string]string), in: make(map[string]string)}
		g.nodes[nodeID] = rec
		pfx := tenant.NodeIDPrefix(nodeID)
		g.nodeCounters[pfx]++
		if g.tenantNodes[pfx] == nil {
			g.tenantNodes[pfx] = make(map[string]struct{})
		}
		g.tenantNodes[pfx][nodeID] = struct{}{}
	}
	if nodeType != "" && rec.typ != nodeType {
		pfx := tenant.NodeIDPrefix(nodeID)
		switch {
		case rec.typ != "" && nodeType == NodeTypeVode:
			// Node already has an established type (real or vode).
			// AddEdge passes NodeTypeVode as a forward-reference placeholder,
			// but if the node is already typed it needs no placeholder.
			// Silently skip — the node is already properly classified.
			return nil
		case rec.typ != "" && rec.typ != NodeTypeVode:
			// Established non-vode type: changing it corrupts the type index.
			return fmt.Errorf("%w: node %q has type %q, cannot change to %q",
				ErrNodeTypeMismatch, nodeID, rec.typ, nodeType)
		case rec.typ == NodeTypeVode && nodeType != NodeTypeVode:
			// Promotion: vode → real type. Remove from vode index and counter.
			if s := g.index[NodeTypeVode]; s != nil {
				delete(s, nodeID)
				if len(s) == 0 {
					delete(g.index, NodeTypeVode)
				}
			}
			g.vodeCounters[pfx]--
		case rec.typ == "" && nodeType == NodeTypeVode:
			// New vode: node was just created above with empty type.
			g.vodeCounters[pfx]++
		// case rec.typ == "" && nodeType != NodeTypeVode: plain new node, no counter change.
		// case rec.typ == NodeTypeVode && nodeType == NodeTypeVode: idempotent, no-op.
		}
		if rec.typ != "" {
			// Remove from old type index (covers the NodeTypeVode→NodeTypeVode
			// case too, though that path cannot reach here due to the guard above).
			if s := g.index[rec.typ]; s != nil {
				delete(s, nodeID)
				if len(s) == 0 {
					delete(g.index, rec.typ)
				}
			}
		}
		rec.typ = nodeType
		if g.index[nodeType] == nil {
			g.index[nodeType] = make(map[string]struct{})
		}
		g.index[nodeType][nodeID] = struct{}{}
	}
	return nil
}

func (g *FlatGraph) UpdateFromEntity(entity string, id int, data map[string]interface{}) error {
	return g.UpdateFromEntityForTenant(0, entity, id, data)
}

// ── Traversal ─────────────────────────────────────────────────────────────────

// GetNeighbors returns a copy of the outgoing-edge map for nodeID.
// If nodeID does not exist, an empty map is returned with a nil error —
// deliberately unlike GetNodeInfo / GetDegree which error on absent nodes.
// The server layer relies on this: it calls GetNeighbors before deciding
// whether to add/remove edges and treats "no neighbours" the same as
// "node not yet in graph".
func (g *FlatGraph) GetNeighbors(nodeID string) (map[string]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rec, ok := g.nodes[nodeID]
	if !ok {
		return make(map[string]string), nil
	}
	result := make(map[string]string, len(rec.out))
	for k, v := range rec.out {
		result[k] = v
	}
	return result, nil
}

// GetIncomingEdges returns a copy of the incoming-edge map for nodeID.
// If nodeID does not exist, an empty map is returned with a nil error
// (same silent-empty contract as GetNeighbors; see its comment).
func (g *FlatGraph) GetIncomingEdges(nodeID string) (map[string]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rec, ok := g.nodes[nodeID]
	if !ok {
		return make(map[string]string), nil
	}
	result := make(map[string]string, len(rec.in))
	for k, v := range rec.in {
		result[k] = v
	}
	return result, nil
}

// bfsEntry holds the parent pointer and depth for a BFS-visited node.
// Using a single map[string]bfsEntry instead of two separate maps
// (parent map[string]string + depthOf map[string]int) saves two allocs
// per FindPath call: one map header and one initial bucket array.
type bfsEntry struct {
	parent string
	depth  int
}

func (g *FlatGraph) FindPath(from, to string, maxDepth int) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fromPrefix := tenant.NodeIDPrefix(from)
	toPrefix := tenant.NodeIDPrefix(to)
	if fromPrefix != "" && toPrefix != "" && fromPrefix != toPrefix {
		return nil, fmt.Errorf("%w: %q -> %q", ErrCrossTenantEdge, from, to)
	}
	if _, ok := g.nodes[from]; !ok {
		return nil, fmt.Errorf("node %s not found", tenant.NodeIDStripped(from))
	}
	if _, ok := g.nodes[to]; !ok {
		return nil, fmt.Errorf("node %s not found", tenant.NodeIDStripped(to))
	}
	if from == to {
		return []string{from}, nil
	}
	// Single map replaces separate parent+depthOf: saves two allocs.
	visited := make(map[string]bfsEntry)
	visited[from] = bfsEntry{}
	// Pre-allocated queue and path avoid growth allocs for typical depths.
	queue := make([]string, 0, 64)
	queue = append(queue, from)
	head := 0
	for head < len(queue) {
		cur := queue[head]
		head++
		entry := visited[cur]
		if cur == to {
			path := make([]string, 0, entry.depth+1)
			for n := to; n != from; n = visited[n].parent {
				path = append(path, n)
			}
			path = append(path, from)
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path, nil
		}
		if entry.depth >= maxDepth {
			continue
		}
		rec, ok := g.nodes[cur]
		if !ok {
			continue
		}
		nextDepth := entry.depth + 1
		for neighbour := range rec.out {
			if _, seen := visited[neighbour]; !seen {
				visited[neighbour] = bfsEntry{parent: cur, depth: nextDepth}
				queue = append(queue, neighbour)
			}
		}
	}
	return nil, fmt.Errorf("no path from %s to %s within depth %d", tenant.NodeIDStripped(from), tenant.NodeIDStripped(to), maxDepth)
}

func (g *FlatGraph) PathExists(from, to string, maxDepth int) (bool, int, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	fromPfx := tenant.NodeIDPrefix(from)
	toPfx := tenant.NodeIDPrefix(to)
	if fromPfx != "" && toPfx != "" && fromPfx != toPfx {
		return false, 0, fmt.Errorf("%w: %q -> %q", ErrCrossTenantEdge, from, to)
	}
	if _, ok := g.nodes[from]; !ok {
		return false, 0, fmt.Errorf("node %s not found", tenant.NodeIDStripped(from))
	}
	if _, ok := g.nodes[to]; !ok {
		return false, 0, fmt.Errorf("node %s not found", tenant.NodeIDStripped(to))
	}
	if from == to {
		return true, 0, nil
	}
	type entry struct {
		node  string
		depth int
	}
	visited := map[string]bool{from: true}
	queue := []entry{{from, 0}}
	head := 0
	for head < len(queue) {
		cur := queue[head]
		head++
		if cur.depth >= maxDepth {
			continue
		}
		rec, ok := g.nodes[cur.node]
		if !ok {
			continue
		}
		for neighbour := range rec.out {
			if neighbour == to {
				visited[to] = true
				return true, cur.depth + 1, nil
			}
			if !visited[neighbour] {
				visited[neighbour] = true
				queue = append(queue, entry{neighbour, cur.depth + 1})
			}
		}
	}
	return false, 0, nil
}

// SharedOutNeighbors returns nodes reachable from both nodeA and nodeB via
// outgoing edges. Only the out-maps are consulted: a node C that points to
// nodeA or nodeB via an incoming edge is not included.
//
// When nodeA == nodeB the method returns all outgoing neighbours of that
// single node — every neighbour trivially satisfies "reachable from both".
//
// Returns a non-nil (possibly empty) slice. Callers may rely on this; they
// do not need to guard against nil before ranging or checking length.
//
// Formerly named CommonNeighbors; renamed to SharedOutNeighbors to make the
// directed out-edge-only semantics explicit and avoid confusion with the
// graph-theory term "common neighbours" which is typically undirected.
func (g *FlatGraph) SharedOutNeighbors(nodeA, nodeB string) ([]string, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	recA, okA := g.nodes[nodeA]
	recB, okB := g.nodes[nodeB]
	// Report which specific node is absent — consistent with FindPath and
	// PathExists which also error per-node. Checking okA first so that a
	// caller supplying two absent nodes gets a clear message about nodeA.
	if !okA {
		return nil, fmt.Errorf("node %s not found", tenant.NodeIDStripped(nodeA))
	}
	if !okB {
		return nil, fmt.Errorf("node %s not found", tenant.NodeIDStripped(nodeB))
	}
	result := make([]string, 0)
	for n := range recA.out {
		if _, ok := recB.out[n]; ok {
			result = append(result, n)
		}
	}
	return result, nil
}

// ── Node queries ──────────────────────────────────────────────────────────────

func (g *FlatGraph) NodeExists(nodeID string) bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	_, ok := g.nodes[nodeID]
	return ok
}

func (g *FlatGraph) GetNodeInfo(nodeID string) (*NodeInfo, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rec, ok := g.nodes[nodeID]
	if !ok {
		return nil, fmt.Errorf("node %s not found", tenant.NodeIDStripped(nodeID))
	}
	// Parse entity and numeric ID from "entity:N" or "XXXX@entity:N".
	entity := ""
	entityID := 0
	if idx := strings.Index(nodeID, ":"); idx >= 0 {
		entity = nodeID[:idx]
		if p := tenant.NodeIDPrefix(nodeID); p != "" {
			entity = strings.TrimPrefix(entity, p)
		}
		if _, scanErr := fmt.Sscanf(nodeID[idx+1:], "%d", &entityID); scanErr != nil {
			g.logger.Debug().Str("node_id", nodeID).Err(scanErr).Msg("GetNodeInfo: could not parse entity ID as integer; reporting 0")
		}
	}
	outgoing := make(map[string]string, len(rec.out))
	for k, v := range rec.out {
		outgoing[k] = v
	}
	incoming := make(map[string]string, len(rec.in))
	for k, v := range rec.in {
		incoming[k] = v
	}
	return &NodeInfo{
		ID:       nodeID,
		Entity:   entity,
		EntityID: entityID,
		Outgoing: outgoing,
		Incoming: incoming,
		Degree:   Degree{In: len(incoming), Out: len(outgoing), Total: len(incoming) + len(outgoing)},
	}, nil
}

func (g *FlatGraph) GetNodesByType(entityType string) []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	s, ok := g.index[entityType]
	if !ok || len(s) == 0 {
		return []string{}
	}
	result := make([]string, 0, len(s))
	for nodeID := range s {
		result = append(result, nodeID)
	}
	return result
}

func (g *FlatGraph) GetAllNodes() []string {
	g.mu.RLock()
	defer g.mu.RUnlock()
	result := make([]string, 0, len(g.nodes))
	for nodeID := range g.nodes {
		result = append(result, nodeID)
	}
	return result
}

func (g *FlatGraph) GetDegree(nodeID string) (Degree, error) {
	g.mu.RLock()
	defer g.mu.RUnlock()
	rec, ok := g.nodes[nodeID]
	if !ok {
		return Degree{}, fmt.Errorf("node %s not found", tenant.NodeIDStripped(nodeID))
	}
	return Degree{In: len(rec.in), Out: len(rec.out), Total: len(rec.in) + len(rec.out)}, nil
}

// ── Metrics ───────────────────────────────────────────────────────────────────

func (g *FlatGraph) NodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.nodes)
}

func (g *FlatGraph) EdgeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.edgeCount
}

func (g *FlatGraph) VodeCount() int {
	g.mu.RLock()
	defer g.mu.RUnlock()
	return len(g.index[NodeTypeVode])
}

func (g *FlatGraph) VodeCountForTenant(tenantPrefix string) (int, error) {
	if tenantPrefix == "" {
		return 0, oluerr.New(oluerr.ErrTenantRequired, 400, "tenant prefix required for vode count")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := g.vodeCounters[tenantPrefix]
	if n < 0 {
		g.logger.Error().Str("tenant_prefix", tenantPrefix).Int("counter", n).
			Msg("FlatGraph.VodeCountForTenant: counter is negative (should never happen); returning 0")
		return 0, nil
	}
	return n, nil
}

func (g *FlatGraph) HasCycle() bool {
	g.mu.RLock()
	defer g.mu.RUnlock()
	// Iterative DFS with three-colour marking: white=0, grey=1, black=2.
	// Iterative rather than recursive to avoid stack overflow on deep graphs.
	colour := make(map[string]int, len(g.nodes))

	type frame struct {
		node     string
		iterator []string
		index    int
	}

	for start := range g.nodes {
		if colour[start] != 0 {
			continue
		}
		rec := g.nodes[start]
		neighbours := make([]string, 0, len(rec.out))
		for n := range rec.out {
			neighbours = append(neighbours, n)
		}
		stack := []frame{{node: start, iterator: neighbours, index: 0}}
		colour[start] = 1

		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.index < len(top.iterator) {
				neighbour := top.iterator[top.index]
				top.index++
				switch colour[neighbour] {
				case 0:
					colour[neighbour] = 1
					var nbrs []string
					if nrec, ok := g.nodes[neighbour]; ok {
						nbrs = make([]string, 0, len(nrec.out))
						for n := range nrec.out {
							nbrs = append(nbrs, n)
						}
					}
					stack = append(stack, frame{node: neighbour, iterator: nbrs, index: 0})
				case 1:
					return true // back edge → cycle
				}
			} else {
				colour[top.node] = 2 // fully explored
				stack = stack[:len(stack)-1]
			}
		}
	}
	return false
}

// HasCycleForTenant reports whether the tenant-scoped subgraph (nodes whose
// ID carries tenantPrefix) contains a directed cycle. Only nodes with that
// prefix are visited; edges to other tenants are not followed.
// Returns (false, ErrTenantRequired) when tenantPrefix is empty.
func (g *FlatGraph) HasCycleForTenant(tenantPrefix string) (bool, error) {
	if tenantPrefix == "" {
		return false, oluerr.New(oluerr.ErrTenantRequired, 400, "tenant prefix required for cycle check")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	colour := make(map[string]int)

	type frame struct {
		node     string
		iterator []string
		index    int
	}

	for nodeID := range g.tenantNodes[tenantPrefix] {
		if colour[nodeID] != 0 {
			continue
		}
		rec := g.nodes[nodeID]
		neighbours := make([]string, 0, len(rec.out))
		for n := range rec.out {
			if strings.HasPrefix(n, tenantPrefix) {
				neighbours = append(neighbours, n)
			}
		}
		stack := []frame{{node: nodeID, iterator: neighbours, index: 0}}
		colour[nodeID] = 1

		for len(stack) > 0 {
			top := &stack[len(stack)-1]
			if top.index < len(top.iterator) {
				neighbour := top.iterator[top.index]
				top.index++
				switch colour[neighbour] {
				case 0:
					colour[neighbour] = 1
					var nbrs []string
					if nrec, ok := g.nodes[neighbour]; ok {
						nbrs = make([]string, 0, len(nrec.out))
						for n := range nrec.out {
							if strings.HasPrefix(n, tenantPrefix) {
								nbrs = append(nbrs, n)
							}
						}
					}
					stack = append(stack, frame{node: neighbour, iterator: nbrs, index: 0})
				case 1:
					return true, nil // back edge → cycle
				}
			} else {
				colour[top.node] = 2
				stack = stack[:len(stack)-1]
			}
		}
	}
	return false, nil
}

// ── Tenant-scoped queries ─────────────────────────────────────────────────────
//
// All *ForTenant methods filter by the XXXX@ prefix. An empty prefix is
// rejected — it would silently return cross-tenant data and is a potential
// exfiltration vector.

func (g *FlatGraph) NodeCountForTenant(tenantPrefix string) (int, error) {
	if tenantPrefix == "" {
		return 0, oluerr.New(oluerr.ErrTenantRequired, 400, "tenant prefix required for node count")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := g.nodeCounters[tenantPrefix]
	if n < 0 {
		g.logger.Error().Str("tenant_prefix", tenantPrefix).Int("counter", n).Msg("FlatGraph.NodeCountForTenant: counter is negative (should never happen); returning 0")
		return 0, nil
	}
	return n, nil
}

func (g *FlatGraph) EdgeCountForTenant(tenantPrefix string) (int, error) {
	if tenantPrefix == "" {
		return 0, oluerr.New(oluerr.ErrTenantRequired, 400, "tenant prefix required for edge count")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	n := g.edgeCounters[tenantPrefix]
	if n < 0 {
		g.logger.Error().Str("tenant_prefix", tenantPrefix).Int("counter", n).Msg("FlatGraph.EdgeCountForTenant: counter is negative (should never happen); returning 0")
		return 0, nil
	}
	return n, nil
}

func (g *FlatGraph) GetAllNodesForTenant(tenantPrefix string) ([]string, error) {
	if tenantPrefix == "" {
		g.logger.Warn().Msg("FlatGraph.GetAllNodesForTenant: empty prefix rejected — possible cross-tenant exfiltration attempt")
		return nil, oluerr.New(oluerr.ErrTenantRequired, 400, "tenant prefix required")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	s := g.tenantNodes[tenantPrefix]
	result := make([]string, 0, len(s))
	for nodeID := range s {
		result = append(result, nodeID)
	}
	return result, nil
}

func (g *FlatGraph) GetNodesByTypeForTenant(tenantPrefix, entityType string) ([]string, error) {
	if tenantPrefix == "" {
		g.logger.Warn().Str("entity_type", entityType).Msg("FlatGraph.GetNodesByTypeForTenant: empty prefix rejected — possible cross-tenant exfiltration attempt")
		return nil, oluerr.New(oluerr.ErrTenantRequired, 400, "tenant prefix required")
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	// Iterate the smaller of the two indexes to keep this O(min(N_tenant, N_type)).
	tNodes := g.tenantNodes[tenantPrefix]
	typeSet := g.index[entityType]
	var src map[string]struct{}
	var check func(string) bool
	if len(tNodes) <= len(typeSet) {
		src = tNodes
		check = func(id string) bool { _, ok := typeSet[id]; return ok }
	} else {
		src = typeSet
		check = func(id string) bool { _, ok := tNodes[id]; return ok }
	}
	result := make([]string, 0)
	for nodeID := range src {
		if check(nodeID) {
			result = append(result, nodeID)
		}
	}
	return result, nil
}

// ── Persistence ───────────────────────────────────────────────────────────────

// flatGraphData is the JSON serialisation format for FlatGraph.
// CycleDetection and CycleCheckLimit are included so that a graph
// reloaded from disk recovers the same enforcement policy without
// requiring any additional caller configuration. Both fields use
// omitempty for backward compatibility: files written by older versions
// of olu that lack these keys are still valid; Load leaves the
// constructor-supplied defaults intact for absent fields.
type flatGraphData struct {
	Nodes           map[string]flatNodeData `json:"nodes"`
	CycleDetection  string                  `json:"cycle_detection,omitempty"`
	CycleCheckLimit int                     `json:"cycle_check_limit,omitempty"`
}

type flatNodeData struct {
	Type string            `json:"type,omitempty"`
	Out  map[string]string `json:"out,omitempty"`
}

// Save serialises the graph to filename using an atomic write (write to
// .tmp then rename). The cycle-detection mode and limit are included in the
// file so that Load can restore them without additional caller configuration.
// The read lock is held only long enough to snapshot in-memory state;
// JSON marshalling and disk I/O happen outside the lock.
func (g *FlatGraph) Save(filename string) error {
	g.mu.RLock()
	snapshot := flatGraphData{
		Nodes:           make(map[string]flatNodeData, len(g.nodes)),
		CycleDetection:  g.cycleDetection,
		CycleCheckLimit: g.cycleCheckLimit,
	}
	for nodeID, rec := range g.nodes {
		out := make(map[string]string, len(rec.out))
		for k, v := range rec.out {
			out[k] = v
		}
		snapshot.Nodes[nodeID] = flatNodeData{Type: rec.typ, Out: out}
	}
	g.mu.RUnlock()

	jsonBytes, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := filename + ".tmp"
	if err := os.WriteFile(tmp, jsonBytes, 0644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, filename); err != nil {
		os.Remove(tmp) // prevent .tmp accumulation on cross-device or permission failures
		return err
	}
	return nil
}

// Load replaces the graph's in-memory state with the contents of filename.
// If the file was written by a current version of olu, the cycle-detection
// mode and limit are restored from the file. Files written by older versions
// that lack these fields leave the current runtime configuration unchanged,
// so existing deployments that configure the mode via NewFlatGraphWithCycleDetection
// continue to work without modification.
// Load is a no-op (returns nil) when filename does not exist.
func (g *FlatGraph) Load(filename string) error {
	g.loadMu.Lock()
	defer g.loadMu.Unlock()
	jsonBytes, err := os.ReadFile(filename)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var data flatGraphData
	if err := json.Unmarshal(jsonBytes, &data); err != nil {
		return err
	}
	// Validate cycle-detection mode before mutating any state.
	newCycleDetection := ""
	if data.CycleDetection != "" {
		switch data.CycleDetection {
		case "ignore", "warn", "error":
			newCycleDetection = data.CycleDetection
		default:
			return fmt.Errorf("FlatGraph.Load: unrecognised cycle_detection mode %q in %q (valid: ignore, warn, error)",
				data.CycleDetection, filename)
		}
	}

	// Replay into a scratch graph so the receiver is never left in a partial
	// state on error. We use "ignore" for cycle detection during replay
	// because a well-formed saved file is cycle-free by construction; running
	// BFS cycle checks during replay is both redundant and O(E × N) for an
	// "error"-mode graph. The configured mode is restored after a successful
	// replay.
	scratch := &FlatGraph{
		nodes:           make(map[string]*nodeRecord, len(data.Nodes)),
		index:           make(map[string]map[string]struct{}),
		tenantNodes:     make(map[string]map[string]struct{}),
		nodeCounters:    make(map[string]int),
		edgeCounters:    make(map[string]int),
		vodeCounters:    make(map[string]int),
		cycleDetection:  "ignore",
		cycleCheckLimit: DefaultCycleCheckLimit,
		logger:          g.logger,
	}
	if data.CycleCheckLimit > 0 {
		scratch.cycleCheckLimit = data.CycleCheckLimit
	} else {
		// File was written by an older version that did not persist this field.
		// Preserve whatever limit was set at construction time, mirroring the
		// equivalent fallback for cycleDetection mode below.
		scratch.cycleCheckLimit = g.cycleCheckLimit
	}

	var loadErrs []error
	for nodeID, nd := range data.Nodes {
		if err := scratch.addNodeLocked(nodeID, nd.Type); err != nil {
			loadErrs = append(loadErrs, fmt.Errorf("load node %q: %w", nodeID, err))
			continue // skip edges for a node we couldn't create
		}
		for target, rel := range nd.Out {
			if err := scratch.addEdgeLocked(nodeID, target, rel); err != nil {
				loadErrs = append(loadErrs, fmt.Errorf("load edge %q->%q (%q): %w", nodeID, target, rel, err))
			}
		}
	}
	if len(loadErrs) > 0 {
		// Log every skipped entry so the operator can see the full picture.
		for _, e := range loadErrs {
			g.logger.Warn().Err(e).Msg("FlatGraph.Load: skipped entry")
		}
		return fmt.Errorf("FlatGraph.Load: %d entries skipped from %q (see log for details)", len(loadErrs), filename)
	}

	// Replay succeeded — apply the configured mode and atomically swap
	// the receiver's state. The receiver is never partially mutated.
	if newCycleDetection != "" {
		scratch.cycleDetection = newCycleDetection
	} else {
		// Preserve whatever mode was set at construction time.
		scratch.cycleDetection = g.cycleDetection
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes = scratch.nodes
	g.index = scratch.index
	g.tenantNodes = scratch.tenantNodes
	g.edgeCount = scratch.edgeCount
	g.nodeCounters = scratch.nodeCounters
	g.edgeCounters = scratch.edgeCounters
	g.vodeCounters = scratch.vodeCounters
	g.cycleDetection = scratch.cycleDetection
	g.cycleCheckLimit = scratch.cycleCheckLimit
	return nil
}

func (g *FlatGraph) Clear() error {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.nodes = make(map[string]*nodeRecord)
	g.index = make(map[string]map[string]struct{})
	g.tenantNodes = make(map[string]map[string]struct{})
	g.edgeCount = 0
	g.nodeCounters = make(map[string]int)
	g.edgeCounters = make(map[string]int)
	g.vodeCounters = make(map[string]int)
	return nil
}

