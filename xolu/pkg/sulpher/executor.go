// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/rs/zerolog"
)

// Sentinel errors for graph query limit violations.
var (
	ErrVisitedNodeLimit = errors.New("graph visited node limit exceeded")
	ErrResultLimit      = errors.New("graph result limit exceeded")
)

// EntityGetter is the narrow store interface required by the Executor for
// property hydration. Any storage.Store satisfies this interface, but test
// mocks only need to implement Get rather than the full storage.Store.
type EntityGetter interface {
	Get(ctx context.Context, entity string, id int) (map[string]interface{}, error)
}

// QueryResult represents the result of a query execution
type QueryResult struct {
	Data  []map[string]interface{} `json:"data"`
	Stats QueryStats               `json:"stats"`
}

// QueryStats contains execution statistics
type QueryStats struct {
	NodesTraversed int           `json:"nodes_traversed"`
	PathsFound     int           `json:"paths_found"`
	ExecutionTime  time.Duration `json:"execution_time_ms"`
}

// Executor executes Sulpher queries against a graph
type Executor struct {
	graph        graph.Graph
	maxDepth     int
	limits       GraphLimits
	tenantPrefix string         // XXXX@ prefix for tenant isolation; empty means unscoped
	// store is optional. When set, entity data is fetched on demand during
	// query execution so that property conditions in WHERE clauses and inline
	// node patterns can match against real entity fields, not just the
	// topology-derived "type" and "id" keys that the graph stores natively.
	// The fetch is lazy (only when a property condition actually needs a field
	// not already in the snapshot) and cached per snapshot so each node is
	// fetched at most once per query.
	store        EntityGetter
	logger       zerolog.Logger // nop by default; set via WithLogger
	mu           sync.RWMutex
}

// WithLogger attaches a logger to the executor.  The logger is used to emit
// WARN-level alerts when cross-tenant node IDs are detected in query results.
func (e *Executor) WithLogger(l zerolog.Logger) *Executor {
	e.logger = l
	return e
}

// WithStore attaches a storage backend to the executor. When set, property
// conditions in WHERE clauses and inline node patterns are evaluated against
// the full entity data fetched from the store, not just the topology-derived
// "type" and "id" keys. The store should be scoped to the same tenant as the
// executor (i.e. already constructed with the matching TenantID).
func (e *Executor) WithStore(s EntityGetter) *Executor {
	e.store = s
	return e
}

// GraphLimits holds server-enforced limits for graph query execution.
type GraphLimits struct {
	MaxVisitedNodes int // Max nodes visited during traversal (0 = default 10000)
	MaxResults      int // Max result paths returned (0 = no limit)
}

// NewExecutor creates a new query executor with no tenant scoping.
func NewExecutor(g graph.Graph, maxDepth int) *Executor {
	return &Executor{
		graph:    g,
		maxDepth: maxDepth,
	}
}

// NewExecutorForTenant creates a query executor scoped to a specific tenant.
// tenantPrefix is the XXXX@ prefix string for the tenant (e.g. "0001@").
// The executor will only traverse nodes belonging to this tenant, and will
// return node IDs with the prefix stripped (i.e. client-facing "entity:id" format).
func NewExecutorForTenant(g graph.Graph, maxDepth int, tenantPrefix string) *Executor {
	return &Executor{
		graph:        g,
		maxDepth:     maxDepth,
		tenantPrefix: tenantPrefix,
	}
}

// SetLimits configures graph query execution limits.
func (e *Executor) SetLimits(limits GraphLimits) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.limits = limits
}

// Execute runs a parsed query and returns results.
// The context is checked during traversal; if cancelled, the query
// stops and returns an error rather than continuing to consume resources.
func (e *Executor) Execute(ctx context.Context, query *Query) (*QueryResult, error) {
	startTime := time.Now()
	stats := QueryStats{}

	// Take a snapshot of the graph for consistent reads
	e.mu.RLock()
	snapshot := e.takeSnapshot()
	e.mu.RUnlock()

	// Find matching start nodes
	startPattern := query.Path[0].Node
	startNodes := e.findMatchingNodes(ctx, snapshot, startPattern)

	var allPaths [][]pathNode
	for _, startNode := range startNodes {
		// Check context before each start node
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("graph query cancelled: %w", err)
		}
		var paths [][]pathNode
		if query.Algorithm == DFS {
			paths = e.dfsTraverse(ctx, snapshot, startNode, query.Path, &stats, e.maxDepth)
		} else {
			paths = e.bfsTraverse(ctx, snapshot, startNode, query.Path, &stats, e.maxDepth)
		}
		allPaths = append(allPaths, paths...)
	}

	// Check if traversal hit the visited-node limit
	maxVisited := e.limits.MaxVisitedNodes
	if maxVisited <= 0 {
		maxVisited = 10000
	}
	if stats.NodesTraversed >= maxVisited {
		return nil, fmt.Errorf("%w: visited %d nodes (max %d)", ErrVisitedNodeLimit, stats.NodesTraversed, maxVisited)
	}

	// Enforce result limit before expensive post-processing
	maxResults := e.limits.MaxResults
	if maxResults > 0 && len(allPaths) > maxResults {
		return nil, fmt.Errorf("%w: %d paths (max %d)", ErrResultLimit, len(allPaths), maxResults)
	}

	// Check context after traversal
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("graph query cancelled: %w", err)
	}

	// Apply WHERE conditions (with OR support)
	if len(query.ConditionGroups) > 0 {
		allPaths = e.applyConditionGroups(ctx, allPaths, query.ConditionGroups, query.Path, snapshot)
	} else if len(query.Conditions) > 0 {
		allPaths = e.applyConditions(ctx, allPaths, query.Conditions, query.Path, snapshot)
	}

	stats.PathsFound = len(allPaths)

	// Apply RETURN projection
	results := e.applyReturn(ctx, allPaths, query.ReturnItems, query.Path, snapshot)

	// Apply DISTINCT
	if query.Distinct {
		results = e.applyDistinct(results)
	}

	// Apply ORDER BY
	if len(query.OrderBy) > 0 {
		results = e.applyOrderBy(results, query.OrderBy)
	}

	// Apply LIMIT
	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}

	stats.ExecutionTime = time.Since(startTime)

	return &QueryResult{
		Data:  results,
		Stats: stats,
	}, nil
}

// ExecuteWithDepth executes a query using an explicit maxDepth override.
// This avoids mutating shared executor state and is safe for concurrent use.
func (e *Executor) ExecuteWithDepth(ctx context.Context, query *Query, maxDepth int) (*QueryResult, error) {
	if maxDepth <= 0 {
		return e.Execute(ctx, query)
	}
	startTime := time.Now()
	stats := QueryStats{}

	e.mu.RLock()
	snapshot := e.takeSnapshot()
	e.mu.RUnlock()

	startPattern := query.Path[0].Node
	startNodes := e.findMatchingNodes(ctx, snapshot, startPattern)

	var allPaths [][]pathNode
	for _, startNode := range startNodes {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("graph query cancelled: %w", err)
		}
		var paths [][]pathNode
		if query.Algorithm == DFS {
			paths = e.dfsTraverse(ctx, snapshot, startNode, query.Path, &stats, maxDepth)
		} else {
			paths = e.bfsTraverse(ctx, snapshot, startNode, query.Path, &stats, maxDepth)
		}
		allPaths = append(allPaths, paths...)
	}

	maxVisited := e.limits.MaxVisitedNodes
	if maxVisited <= 0 {
		maxVisited = 10000
	}
	if stats.NodesTraversed >= maxVisited {
		return nil, fmt.Errorf("%w: visited %d nodes (max %d)", ErrVisitedNodeLimit, stats.NodesTraversed, maxVisited)
	}
	maxResults := e.limits.MaxResults
	if maxResults > 0 && len(allPaths) > maxResults {
		return nil, fmt.Errorf("%w: %d paths (max %d)", ErrResultLimit, len(allPaths), maxResults)
	}
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("graph query cancelled: %w", err)
	}
	if len(query.ConditionGroups) > 0 {
		allPaths = e.applyConditionGroups(ctx, allPaths, query.ConditionGroups, query.Path, snapshot)
	} else if len(query.Conditions) > 0 {
		allPaths = e.applyConditions(ctx, allPaths, query.Conditions, query.Path, snapshot)
	}
	stats.PathsFound = len(allPaths)
	results := e.applyReturn(ctx, allPaths, query.ReturnItems, query.Path, snapshot)
	if query.Distinct {
		results = e.applyDistinct(results)
	}
	if len(query.OrderBy) > 0 {
		results = e.applyOrderBy(results, query.OrderBy)
	}
	if query.Limit > 0 && len(results) > query.Limit {
		results = results[:query.Limit]
	}
	stats.ExecutionTime = time.Since(startTime)
	return &QueryResult{
		Data:  results,
		Stats: stats,
	}, nil
}

// pathNode represents a node in a traversal path
type pathNode struct {
	NodeID string
	Data   map[string]interface{}
}

// graphSnapshot is an in-memory copy of the graph for consistent reads
type graphSnapshot struct {
	adjacency    map[string]map[string]string // node -> {neighbor -> relationship} (outgoing)
	revAdjacency map[string]map[string]string // node -> {neighbor -> relationship} (incoming)
	nodeData     map[string]map[string]interface{}
	// hydrated tracks which node IDs have already had their full entity data
	// fetched from the store. This prevents redundant store round-trips when
	// the same node is visited multiple times during a traversal.
	hydrated map[string]bool
}

// takeSnapshot creates a consistent snapshot of the graph.
// When e.tenantPrefix is set, only nodes belonging to that tenant are
// included, and the prefix is stripped from all node IDs in the snapshot.
// This means the rest of the traversal engine always sees clean "entity:id"
// style IDs regardless of whether a tenant context is active.
func (e *Executor) takeSnapshot() *graphSnapshot {
	snapshot := &graphSnapshot{
		adjacency:    make(map[string]map[string]string),
		revAdjacency: make(map[string]map[string]string),
		nodeData:     make(map[string]map[string]interface{}),
		hydrated:     make(map[string]bool),
	}

	prefix := e.tenantPrefix

	// Get the relevant nodes — tenant-scoped or all.
	var nodes []string
	if prefix != "" {
		var err error
		nodes, err = e.graph.GetAllNodesForTenant(prefix)
		if err != nil {
			// prefix is always non-empty here; this path should never be reached
			// in practice but is handled defensively.
			log.Printf("[ERROR] takeSnapshot: GetAllNodesForTenant failed: %v", err)
			return snapshot
		}
	} else {
		nodes = e.graph.GetAllNodes()
	}

	// strip removes the tenant prefix from a node ID for client-facing use.
	strip := func(nodeID string) string {
		if prefix == "" {
			return nodeID
		}
		return strings.TrimPrefix(nodeID, prefix)
	}

	for _, rawNodeID := range nodes {
		clientID := strip(rawNodeID)

		// Copy outgoing adjacency, stripping prefixes from neighbour IDs.
		// If a stripped neighbour ID still contains '@', the edge points to a
		// node owned by a different tenant — a data integrity violation.
		// Log a WARN and exclude the edge so it never reaches the client.
		neighbors, _ := e.graph.GetNeighbors(rawNodeID)
		stripped := make(map[string]string, len(neighbors))
		for neighborRaw, rel := range neighbors {
			neighborClient := strip(neighborRaw)
			if prefix != "" && strings.Contains(neighborClient, "@") {
				e.logger.Warn().
					Str("tenant_prefix", prefix).
					Str("source_node", clientID).
					Str("foreign_node_raw", neighborRaw).
					Str("relationship", rel).
					Msg("cross-tenant edge detected in graph snapshot — excluding from query results")
				continue
			}
			stripped[neighborClient] = rel
		}
		snapshot.adjacency[clientID] = stripped

		// Build reverse adjacency from stripped outgoing edges.
		for neighborStripped, relType := range stripped {
			if snapshot.revAdjacency[neighborStripped] == nil {
				snapshot.revAdjacency[neighborStripped] = make(map[string]string)
			}
			snapshot.revAdjacency[neighborStripped][clientID] = relType
		}

		// Parse node data from ID (entity:id format after stripping).
		parts := strings.SplitN(clientID, ":", 2)
		if len(parts) == 2 {
			snapshot.nodeData[clientID] = map[string]interface{}{
				"type": parts[0],
				"id":   clientID,
			}
		}
	}

	return snapshot
}

// getNeighborsByDirection returns neighbors based on relationship direction
func (e *Executor) getNeighborsByDirection(snapshot *graphSnapshot, node string, direction RelDirection) map[string]string {
	result := make(map[string]string)

	switch direction {
	case RelOutgoing:
		for k, v := range snapshot.adjacency[node] {
			result[k] = v
		}
	case RelIncoming:
		for k, v := range snapshot.revAdjacency[node] {
			result[k] = v
		}
	case RelBidirectional:
		// Both outgoing and incoming
		for k, v := range snapshot.adjacency[node] {
			result[k] = v
		}
		for k, v := range snapshot.revAdjacency[node] {
			if _, exists := result[k]; !exists {
				result[k] = v
			}
		}
	}

	return result
}

// findMatchingNodes finds nodes matching a pattern
func (e *Executor) findMatchingNodes(ctx context.Context, snapshot *graphSnapshot, pattern NodePattern) []string {
	var matches []string

	for nodeID := range snapshot.adjacency {
		if e.matchesNodePattern(ctx, nodeID, snapshot.nodeData[nodeID], pattern, snapshot) {
			matches = append(matches, nodeID)
		}
	}

	return matches
}

// matchesNodePattern checks if a node matches a pattern.
// It first checks type (derived from the node ID) and the special "id" key
// without a store round-trip. For any other property keys it calls
// hydrateNodeData to ensure the full entity data is present in the snapshot,
// so that WHERE conditions and inline property filters work correctly.
func (e *Executor) matchesNodePattern(ctx context.Context, nodeID string, nodeData map[string]interface{}, pattern NodePattern, snapshot *graphSnapshot) bool {
	// Check type if specified — derived from the node ID, no store needed.
	if pattern.Type != "" {
		parts := strings.SplitN(nodeID, ":", 2)
		if len(parts) < 2 || parts[0] != pattern.Type {
			return false
		}
	}

	// Check inline properties.
	for key, expected := range pattern.Properties {
		// "id" is encoded in the node ID string — no store fetch needed.
		if key == "id" {
			parts := strings.SplitN(nodeID, ":", 2)
			if len(parts) == 2 {
				switch v := expected.(type) {
				case int:
					if parts[1] != fmt.Sprintf("%d", v) {
						return false
					}
				case string:
					if parts[1] != v {
						return false
					}
				}
				continue
			}
		}

		// For any other key, ensure nodeData is hydrated from the store.
		// hydrateNodeData is idempotent per snapshot: it marks the node in
		// snapshot.hydrated so redundant store.Get calls are avoided.
		if _, alreadyPresent := nodeData[key]; !alreadyPresent {
			e.hydrateNodeData(ctx, nodeID, nodeData, snapshot.hydrated)
		}

		actual, exists := nodeData[key]
		if !exists || !valuesEqual(actual, expected) {
			return false
		}
	}

	return true
}

// hydrateNodeData fetches the full entity data for nodeID from the store and
// merges it into nodeData. It is idempotent: subsequent calls for the same
// nodeID within the same snapshot are no-ops (the hydrated flag is checked by
// the caller before invoking this).
//
// nodeID must be in "entity:id" format (client-facing, prefix already stripped).
// If the store lookup fails the existing nodeData is left unchanged — property
// conditions on non-present fields will not match, which is the correct
// behaviour (silent miss, no error propagated to the query caller).
// hydrateNodeData fetches the full entity data for nodeID from the store and
// merges it into nodeData. It is a no-op if the node has already been hydrated
// in this snapshot (tracked via snapshot.hydrated) or if no store is attached.
//
// nodeID must be in "entity:id" format (client-facing, prefix already stripped).
// If the store lookup fails the node is still marked hydrated so that
// subsequent conditions on the same node do not trigger redundant store calls —
// missing fields simply do not match, which is correct behaviour.
func (e *Executor) hydrateNodeData(ctx context.Context, nodeID string, nodeData map[string]interface{}, hydrated map[string]bool) {
	if e.store == nil {
		return
	}
	// Already attempted — avoid a redundant store.Get even if the previous
	// attempt failed (e.g. the entity was deleted after the snapshot was taken).
	if hydrated[nodeID] {
		return
	}
	hydrated[nodeID] = true

	parts := strings.SplitN(nodeID, ":", 2)
	if len(parts) != 2 {
		return
	}
	entityType := parts[0]
	entityID, err := strconv.Atoi(parts[1])
	if err != nil {
		return
	}
	data, err := e.store.Get(ctx, entityType, entityID)
	if err != nil {
		// Node may have been deleted after the graph snapshot was taken.
		// Leave nodeData as-is; property conditions on missing fields return false.
		e.logger.Debug().
			Str("node", nodeID).Err(err).
			Msg("hydrateNodeData: store.Get failed; property conditions will not match")
		return
	}
	for k, v := range data {
		nodeData[k] = v
	}
}

// bfsTraverse performs BFS traversal following the path pattern
func (e *Executor) bfsTraverse(ctx context.Context, snapshot *graphSnapshot, startNode string, pathPattern []PathElement, stats *QueryStats, maxDepth int) [][]pathNode {
	var results [][]pathNode

	type queueItem struct {
		node            string
		patternIndex    int
		path            []pathNode
		varLengthHops   int  // Current hop count in variable-length segment
		inVarLength     bool // Currently traversing a variable-length segment
	}

	queue := []queueItem{{
		node:         startNode,
		patternIndex: 0,
		path:         nil,
	}}

	visited := make(map[string]bool)
	maxIterations := e.limits.MaxVisitedNodes
	if maxIterations <= 0 {
		maxIterations = 10000
	}
	iterations := 0
	head := 0 // index of next item to dequeue; avoids O(N) front-shift

	for head < len(queue) && iterations < maxIterations {
		// Check context every 256 iterations to avoid overhead on small graphs
		if iterations&0xFF == 0 {
			if err := ctx.Err(); err != nil {
				return results // Return partial results on cancellation
			}
		}
		iterations++
		current := queue[head]
		head++

		if len(current.path) > maxDepth {
			continue
		}

		// For variable-length, we use a different visit key that includes hop count
		var visitKey string
		if current.inVarLength {
			visitKey = fmt.Sprintf("%s:%d:%d", current.node, current.patternIndex, current.varLengthHops)
		} else {
			visitKey = fmt.Sprintf("%s:%d", current.node, current.patternIndex)
		}
		if visited[visitKey] {
			continue
		}
		visited[visitKey] = true
		stats.NodesTraversed++

		// Add current node to path
		newPath := make([]pathNode, len(current.path)+1)
		copy(newPath, current.path)
		newPath[len(current.path)] = pathNode{
			NodeID: current.node,
			Data:   snapshot.nodeData[current.node],
		}

		// Check if we've completed the pattern
		if current.patternIndex >= len(pathPattern)-1 {
			results = append(results, newPath)
			continue
		}

		// Get relationship pattern for current position
		relPattern := pathPattern[current.patternIndex].Relationship
		nextNodePattern := pathPattern[current.patternIndex+1].Node

		// Handle variable-length relationships
		if relPattern != nil && relPattern.IsVariable {
			maxHops := relPattern.MaxHops
			if maxHops == 0 {
				maxHops = maxDepth
			}

			// If we've reached minimum hops, we can accept this as a valid endpoint
			// and also continue traversing
			if current.varLengthHops >= relPattern.MinHops {
				// Check if current node matches the next pattern
				if e.matchesNodePattern(ctx, current.node, snapshot.nodeData[current.node], nextNodePattern, snapshot) {
					// This is a valid path endpoint
					if current.patternIndex+1 >= len(pathPattern)-1 {
						// Final node in pattern
						results = append(results, newPath)
					} else {
						// Continue to next pattern segment
						queue = append(queue, queueItem{
							node:         current.node,
							patternIndex: current.patternIndex + 1,
							path:         current.path, // Don't duplicate the node
						})
					}
				}
			}

			// Continue variable-length traversal if under max
			if current.varLengthHops < maxHops {
				direction := RelOutgoing
				if relPattern != nil {
					direction = relPattern.Direction
				}
				for neighbor, edgeType := range e.getNeighborsByDirection(snapshot, current.node, direction) {
					if relPattern.Type != "" && edgeType != relPattern.Type {
						continue
					}
					queue = append(queue, queueItem{
						node:          neighbor,
						patternIndex:  current.patternIndex,
						path:          newPath,
						varLengthHops: current.varLengthHops + 1,
						inVarLength:   true,
					})
				}
			}
		} else {
			// Regular single-hop relationship
			direction := RelOutgoing
			if relPattern != nil {
				direction = relPattern.Direction
			}
			for neighbor, edgeType := range e.getNeighborsByDirection(snapshot, current.node, direction) {
				if relPattern != nil && relPattern.Type != "" && edgeType != relPattern.Type {
					continue
				}
				if !e.matchesNodePattern(ctx, neighbor, snapshot.nodeData[neighbor], nextNodePattern, snapshot) {
					continue
				}
				queue = append(queue, queueItem{
					node:         neighbor,
					patternIndex: current.patternIndex + 1,
					path:         newPath,
				})
			}
		}
	}

	return results
}

// dfsTraverse performs DFS traversal following the path pattern
func (e *Executor) dfsTraverse(ctx context.Context, snapshot *graphSnapshot, startNode string, pathPattern []PathElement, stats *QueryStats, maxDepth int) [][]pathNode {
	var results [][]pathNode
	visited := make(map[string]bool)

	e.dfsRecursive(ctx, snapshot, startNode, 0, 0, false, nil, pathPattern, visited, &results, stats, maxDepth)

	return results
}

func (e *Executor) dfsRecursive(
	ctx context.Context,
	snapshot *graphSnapshot,
	node string,
	patternIndex int,
	varLengthHops int,
	inVarLength bool,
	currentPath []pathNode,
	pathPattern []PathElement,
	visited map[string]bool,
	results *[][]pathNode,
	stats *QueryStats,
	maxDepth int,
) {
	if len(currentPath) > maxDepth {
		return
	}

	// Check context periodically (every node visit)
	if ctx.Err() != nil {
		return
	}

	// Check visited nodes limit
	maxVisited := e.limits.MaxVisitedNodes
	if maxVisited <= 0 {
		maxVisited = 10000
	}
	if stats.NodesTraversed >= maxVisited {
		return
	}

	// Create visit key based on whether we're in variable-length mode
	var visitKey string
	if inVarLength {
		visitKey = fmt.Sprintf("%s:%d:%d", node, patternIndex, varLengthHops)
	} else {
		visitKey = fmt.Sprintf("%s:%d", node, patternIndex)
	}
	if visited[visitKey] {
		return
	}

	// Enter: mark this (node, pattern position) as visited for the current
	// branch. Use enter/leave backtracking rather than copying the map so that
	// the total cost is O(V+E) across the whole traversal instead of O(V²).
	visited[visitKey] = true
	stats.NodesTraversed++
	defer delete(visited, visitKey) // leave: allow other branches to revisit

	// Add current node to path
	newPath := make([]pathNode, len(currentPath)+1)
	copy(newPath, currentPath)
	newPath[len(currentPath)] = pathNode{
		NodeID: node,
		Data:   snapshot.nodeData[node],
	}

	// Check if we've completed the pattern
	if patternIndex >= len(pathPattern)-1 {
		*results = append(*results, newPath)
		return
	}

	// Get relationship pattern for current position
	relPattern := pathPattern[patternIndex].Relationship
	nextNodePattern := pathPattern[patternIndex+1].Node

	// Handle variable-length relationships
	if relPattern != nil && relPattern.IsVariable {
		maxHops := relPattern.MaxHops
		if maxHops == 0 {
			maxHops = maxDepth
		}

		// If we've reached minimum hops, we can accept this as a valid endpoint
		if varLengthHops >= relPattern.MinHops {
			// Check if current node matches the next pattern
			if e.matchesNodePattern(ctx, node, snapshot.nodeData[node], nextNodePattern, snapshot) {
				if patternIndex+1 >= len(pathPattern)-1 {
					// Final node in pattern
					*results = append(*results, newPath)
				} else {
					// Continue to next pattern segment
					e.dfsRecursive(ctx, snapshot, node, patternIndex+1, 0, false,
						currentPath, pathPattern, visited, results, stats, maxDepth)
				}
			}
		}

		// Continue variable-length traversal if under max
		if varLengthHops < maxHops {
			direction := RelOutgoing
			if relPattern != nil {
				direction = relPattern.Direction
			}
			for neighbor, edgeType := range e.getNeighborsByDirection(snapshot, node, direction) {
				if relPattern.Type != "" && edgeType != relPattern.Type {
					continue
				}
				e.dfsRecursive(ctx, snapshot, neighbor, patternIndex, varLengthHops+1, true,
					newPath, pathPattern, visited, results, stats, maxDepth)
			}
		}
	} else {
		// Regular single-hop relationship
		direction := RelOutgoing
		if relPattern != nil {
			direction = relPattern.Direction
		}
		for neighbor, edgeType := range e.getNeighborsByDirection(snapshot, node, direction) {
			if relPattern != nil && relPattern.Type != "" && edgeType != relPattern.Type {
				continue
			}
			if !e.matchesNodePattern(ctx, neighbor, snapshot.nodeData[neighbor], nextNodePattern, snapshot) {
				continue
			}
			e.dfsRecursive(ctx, snapshot, neighbor, patternIndex+1, 0, false,
				newPath, pathPattern, visited, results, stats, maxDepth)
		}
	}
}

// applyConditions filters paths by WHERE conditions
func (e *Executor) applyConditions(ctx context.Context, paths [][]pathNode, conditions []Condition, pathPattern []PathElement, snapshot *graphSnapshot) [][]pathNode {
	var filtered [][]pathNode

	for _, path := range paths {
		match := true
		for _, cond := range conditions {
			if !e.evaluateCondition(ctx, path, cond, pathPattern, snapshot) {
				match = false
				break
			}
		}
		if match {
			filtered = append(filtered, path)
		}
	}

	return filtered
}

// applyConditionGroups filters paths by OR-joined condition groups
func (e *Executor) applyConditionGroups(ctx context.Context, paths [][]pathNode, groups []ConditionGroup, pathPattern []PathElement, snapshot *graphSnapshot) [][]pathNode {
	var filtered [][]pathNode

	for _, path := range paths {
		// Path matches if ANY group matches (OR logic)
		for _, group := range groups {
			// Within a group, ALL conditions must match (AND logic)
			groupMatch := true
			for _, cond := range group.Conditions {
				if !e.evaluateCondition(ctx, path, cond, pathPattern, snapshot) {
					groupMatch = false
					break
				}
			}
			if groupMatch {
				filtered = append(filtered, path)
				break // Found a matching group, no need to check others
			}
		}
	}

	return filtered
}

// applyDistinct removes duplicate results based on JSON serialization
func (e *Executor) applyDistinct(results []map[string]interface{}) []map[string]interface{} {
	if len(results) == 0 {
		return results
	}

	seen := make(map[string]bool)
	var unique []map[string]interface{}

	for _, result := range results {
		// Serialize to JSON for comparison
		jsonBytes, err := json.Marshal(result)
		if err != nil {
			// If serialization fails, include the result
			unique = append(unique, result)
			continue
		}

		key := string(jsonBytes)
		if !seen[key] {
			seen[key] = true
			unique = append(unique, result)
		}
	}

	return unique
}

// applyOrderBy sorts results by the specified fields
func (e *Executor) applyOrderBy(results []map[string]interface{}, orderBy []OrderByItem) []map[string]interface{} {
	if len(results) == 0 || len(orderBy) == 0 {
		return results
	}

	sort.SliceStable(results, func(i, j int) bool {
		for _, ob := range orderBy {
			vi := getNestedValue(results[i], ob.VarPath)
			vj := getNestedValue(results[j], ob.VarPath)

			cmp := compareForSort(vi, vj)
			if cmp == 0 {
				continue // Equal, check next field
			}

			if ob.Direction == OrderDesc {
				return cmp > 0
			}
			return cmp < 0
		}
		return false // All fields equal
	})

	return results
}

// getNestedValue gets a value from a map using dot notation
func getNestedValue(m map[string]interface{}, path string) interface{} {
	// First try direct key
	if v, ok := m[path]; ok {
		return v
	}

	// Try nested access
	parts := strings.SplitN(path, ".", 2)
	if len(parts) == 2 {
		if nested, ok := m[parts[0]].(map[string]interface{}); ok {
			return nested[parts[1]]
		}
	}

	return nil
}

// compareForSort compares two values for sorting, returns -1, 0, or 1
func compareForSort(a, b interface{}) int {
	if a == nil && b == nil {
		return 0
	}
	if a == nil {
		return -1
	}
	if b == nil {
		return 1
	}

	// Try numeric comparison
	aFloat, aIsNum := toFloat64(a)
	bFloat, bIsNum := toFloat64(b)
	if aIsNum && bIsNum {
		if aFloat < bFloat {
			return -1
		}
		if aFloat > bFloat {
			return 1
		}
		return 0
	}

	// Fall back to string comparison
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	if aStr < bStr {
		return -1
	}
	if aStr > bStr {
		return 1
	}
	return 0
}

// toFloat64 attempts to convert a value to float64
func toFloat64(v interface{}) (float64, bool) {
	switch val := v.(type) {
	case int:
		return float64(val), true
	case int64:
		return float64(val), true
	case float64:
		return val, true
	case float32:
		return float64(val), true
	case string:
		var f float64
		_, err := fmt.Sscanf(val, "%f", &f)
		return f, err == nil
	}
	return 0, false
}

// evaluateCondition evaluates a single condition against a path
func (e *Executor) evaluateCondition(ctx context.Context, path []pathNode, cond Condition, pathPattern []PathElement, snapshot *graphSnapshot) bool {
	// Parse varPath: "u.name" -> variable "u", property "name"
	parts := strings.SplitN(cond.VarPath, ".", 2)
	if len(parts) != 2 {
		return false
	}

	varName := parts[0]
	propName := parts[1]

	// Find the node with this variable
	for i, elem := range pathPattern {
		if elem.Node.Variable == varName && i < len(path) {
			nodeData := path[i].Data
			if nodeData == nil {
				return false
			}

			// Special case for "id" — derived from node ID, no store needed.
			var value interface{}
			if propName == "id" {
				nodeParts := strings.SplitN(path[i].NodeID, ":", 2)
				if len(nodeParts) == 2 {
					value = nodeParts[1]
				}
			} else {
				// For any other property, ensure the node data is hydrated.
				// hydrateNodeData mutates nodeData in place; path[i].Data points
				// to the same map, so subsequent reads see the fetched fields.
				if _, present := nodeData[propName]; !present {
					e.hydrateNodeData(ctx, path[i].NodeID, nodeData, snapshot.hydrated)
				}
				value = nodeData[propName]
			}

			return compareValues(value, cond.Operator, cond.Value)
		}
	}

	return false
}

// applyReturn projects the requested fields from paths
func (e *Executor) applyReturn(ctx context.Context, paths [][]pathNode, returnItems []ReturnItem, pathPattern []PathElement, snapshot *graphSnapshot) []map[string]interface{} {
	var results []map[string]interface{}

	for _, path := range paths {
		result := make(map[string]interface{})

		for _, item := range returnItems {
			// Find the node with this variable
			for i, elem := range pathPattern {
				if elem.Node.Variable == item.Variable && i < len(path) {
					if item.Property != "" {
						// Return specific property
						key := item.Variable + "." + item.Property
						if item.Property == "id" {
							// Extract ID from node ID
							parts := strings.SplitN(path[i].NodeID, ":", 2)
							if len(parts) == 2 {
								result[key] = parts[1]
							}
						} else if path[i].Data != nil {
							// Hydrate on demand so RETURN u.name works even when no
							// WHERE condition has previously triggered hydration.
							if _, present := path[i].Data[item.Property]; !present {
								e.hydrateNodeData(ctx, path[i].NodeID, path[i].Data, snapshot.hydrated)
							}
							result[key] = path[i].Data[item.Property]
						}
					} else {
						// Return whole node — hydrate so all entity fields are present.
						if path[i].Data != nil {
							e.hydrateNodeData(ctx, path[i].NodeID, path[i].Data, snapshot.hydrated)
						}
						nodeResult := make(map[string]interface{})
						nodeResult["_id"] = path[i].NodeID
						for k, v := range path[i].Data {
							nodeResult[k] = v
						}
						result[item.Variable] = nodeResult
					}
					break
				}
			}
		}

		results = append(results, result)
	}

	return results
}

// Helper functions

func valuesEqual(a, b interface{}) bool {
	// Convert to comparable types
	aStr := fmt.Sprintf("%v", a)
	bStr := fmt.Sprintf("%v", b)
	return aStr == bStr
}

func compareValues(value interface{}, op Operator, expected interface{}) bool {
	if value == nil {
		return false
	}

	switch op {
	case OpEq:
		return valuesEqual(value, expected)
	case OpNe:
		return !valuesEqual(value, expected)
	case OpLt, OpGt, OpLte, OpGte:
		return compareNumeric(value, op, expected)
	}

	return false
}

func compareNumeric(value interface{}, op Operator, expected interface{}) bool {
	var vFloat, eFloat float64

	switch v := value.(type) {
	case int:
		vFloat = float64(v)
	case int64:
		vFloat = float64(v)
	case float64:
		vFloat = v
	case string:
		if f, err := parseNumeric(v); err == nil {
			vFloat = f
		} else {
			return false
		}
	default:
		return false
	}

	switch e := expected.(type) {
	case int:
		eFloat = float64(e)
	case int64:
		eFloat = float64(e)
	case float64:
		eFloat = e
	case string:
		if f, err := parseNumeric(e); err == nil {
			eFloat = f
		} else {
			return false
		}
	default:
		return false
	}

	switch op {
	case OpLt:
		return vFloat < eFloat
	case OpGt:
		return vFloat > eFloat
	case OpLte:
		return vFloat <= eFloat
	case OpGte:
		return vFloat >= eFloat
	}

	return false
}

func parseNumeric(s string) (float64, error) {
	var f float64
	_, err := fmt.Sscanf(s, "%f", &f)
	return f, err
}
