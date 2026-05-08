// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Tenant-scoped graph and Sulpher query handlers.
//
// These handlers mirror the non-tenant graph handlers but enforce tenant
// isolation by:
//
//  1. Reading the tenant ID from the request context (set by tenantMiddleware).
//  2. Adding the XXXX@ prefix to all node IDs received from the client before
//     passing them to the graph layer.
//  3. Stripping the XXXX@ prefix from all node IDs returned by the graph
//     layer before sending them to the client.
//
// Clients always use the clean "entity:id" format. The XXXX@ prefix is an
// internal implementation detail that clients never see.
//
// Sulpher queries submitted via tenant-scoped routes are executed against a
// tenant-scoped snapshot that contains only that tenant's nodes.

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	oluerr "github.com/ha1tch/xolu/pkg/errors"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/sulpher"
	"github.com/ha1tch/xolu/pkg/tenant"
)

// addPrefix prepends the tenant prefix to a client-facing node ID.
func addPrefix(prefix, nodeID string) string {
	if prefix == "" {
		return nodeID
	}
	return prefix + nodeID
}

// stripPrefix removes the tenant prefix from an internal node ID.
func stripPrefix(prefix, nodeID string) string {
	if prefix == "" {
		return nodeID
	}
	return strings.TrimPrefix(nodeID, prefix)
}

// stripPrefixFromEdgeMap strips the tenant prefix from the keys of a
// neighbor map (map[nodeID]relationship).
func stripPrefixFromEdgeMap(prefix string, m map[string]string) map[string]string {
	if prefix == "" || len(m) == 0 {
		return m
	}
	result := make(map[string]string, len(m))
	for nodeID, rel := range m {
		result[stripPrefix(prefix, nodeID)] = rel
	}
	return result
}

// stripPrefixFromSlice strips the tenant prefix from each element of a
// slice of node IDs.
func stripPrefixFromSlice(prefix string, nodes []string) []string {
	if prefix == "" || len(nodes) == 0 {
		return nodes
	}
	result := make([]string, len(nodes))
	for i, n := range nodes {
		result[i] = stripPrefix(prefix, n)
	}
	return result
}

// guardSlice removes any element from a stripped slice that still contains '@',
// which indicates a cross-tenant node ID leaked through. Each violation is
// logged as a WARN so the server operator is alerted to a data integrity issue.
//
// A fresh backing array is always allocated so that the caller's slice is
// never mutated. The filter-in-place idiom (nodes[:0:len(nodes)]) was
// previously used here; it was safe only because every call site passed a
// freshly-allocated result from stripPrefixFromSlice, but the aliasing
// contract was invisible from the call site and easy to violate.
func (s *Server) guardSlice(prefix string, nodes []string, context string) []string {
	if prefix == "" {
		return nodes
	}
	clean := make([]string, 0, len(nodes))
	for _, n := range nodes {
		if strings.Contains(n, "@") {
			s.logger.Warn().
				Str("tenant_prefix", prefix).
				Str("foreign_node", n).
				Str("context", context).
				Msg("cross-tenant node detected in REST handler result — excluding from response")
			continue
		}
		clean = append(clean, n)
	}
	return clean
}

// guardEdgeMap removes any key from a stripped edge map that still contains '@',
// which indicates a cross-tenant node ID leaked through. Each violation is
// logged as a WARN so the server operator is alerted to a data integrity issue.
//
// A fresh map is always allocated so that the caller's map is never mutated —
// consistent with the same discipline applied to guardSlice. The in-place
// delete idiom is safe only when the caller passes a freshly-allocated map
// (e.g. from stripPrefixFromEdgeMap), but that contract is invisible at call
// sites and easy to violate, so we allocate defensively.
func (s *Server) guardEdgeMap(prefix string, m map[string]string, context string) map[string]string {
	if prefix == "" {
		return m
	}
	clean := make(map[string]string, len(m))
	for k, v := range m {
		if strings.Contains(k, "@") {
			s.logger.Warn().
				Str("tenant_prefix", prefix).
				Str("foreign_node", k).
				Str("context", context).
				Msg("cross-tenant node detected in REST handler edge map — excluding from response")
			continue
		}
		clean[k] = v
	}
	return clean
}

// handleTenantGraphStats returns graph statistics for a specific tenant.
// GET /api/v1/tenant/{tenant_id}/graph/stats
func (s *Server) handleTenantGraphStats(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)

	nodeCount, err := s.graph.NodeCountForTenant(prefix)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrTenantRequired, err.Error())
		return
	}
	edgeCount, err := s.graph.EdgeCountForTenant(prefix)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrTenantRequired, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_count": nodeCount,
		"edge_count": edgeCount,
	})
}

// handleTenantGraphNodeInfo returns detailed info about a specific node
// within the requesting tenant's subgraph.
// GET /api/v1/tenant/{tenant_id}/graph/nodes/{node_id}
func (s *Server) handleTenantGraphNodeInfo(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	nodeID := chi.URLParam(r, "node_id")
	if nodeID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "node_id required")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)
	internalID := addPrefix(prefix, nodeID)

	info, err := s.graph.GetNodeInfo(internalID)
	if err != nil {
		s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound, err.Error())
		return
	}

	// Strip prefix from ID-bearing fields in the response.
	// GetNodeInfo already strips the XXXX@ prefix from Entity internally,
	// so only info.ID and the edge maps need cleaning here.
	info.ID = stripPrefix(prefix, info.ID)
	info.Outgoing = s.guardEdgeMap(prefix, stripPrefixFromEdgeMap(prefix, info.Outgoing), "nodeInfo/outgoing")
	info.Incoming = s.guardEdgeMap(prefix, stripPrefixFromEdgeMap(prefix, info.Incoming), "nodeInfo/incoming")
	s.writeJSON(w, http.StatusOK, info)
}

// handleTenantGraphNodeDegree returns degree counts for a node.
// GET /api/v1/tenant/{tenant_id}/graph/nodes/{node_id}/degree
func (s *Server) handleTenantGraphNodeDegree(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	nodeID := chi.URLParam(r, "node_id")
	if nodeID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "node_id required")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)
	internalID := addPrefix(prefix, nodeID)

	degree, err := s.graph.GetDegree(internalID)
	if err != nil {
		// Node absent from in-memory graph — may be an edge-free adapted entity.
		// Fall back to edge table COUNT before returning 404.
		entityParts := strings.SplitN(nodeID, ":", 2)
		tenantStore, storeErr := s.storeForTenant(tid)
		if storeErr == nil {
			if sqlStore, ok := tenantStore.(*storage.SQLiteStore); ok && len(entityParts) == 2 {
				var id int
				if _, scanErr := fmt.Sscanf(entityParts[1], "%d", &id); scanErr == nil {
					if d, found := s.degreeFromStorage(r.Context(), sqlStore, tid, entityParts[0], id); found {
						s.writeJSON(w, http.StatusOK, map[string]interface{}{"node_id": nodeID, "degree": d})
						return
					}
				}
			}
		}
		s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound, err.Error())
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id": nodeID,
		"degree":  degree,
	})
}

// handleTenantGraphIncoming returns incoming edges to a node.
// GET /api/v1/tenant/{tenant_id}/graph/{node_id}/in
func (s *Server) handleTenantGraphIncoming(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	nodeID := chi.URLParam(r, "node_id")
	if nodeID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "node_id required")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)
	internalID := addPrefix(prefix, nodeID)

	incoming, err := s.graph.GetIncomingEdges(internalID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, err.Error())
		return
	}

	edges := make([]map[string]string, 0, len(incoming))
	for source, relationship := range incoming {
		cleanSource := stripPrefix(prefix, source)
		if prefix != "" && strings.Contains(cleanSource, "@") {
			s.logger.Warn().
				Str("tenant_prefix", prefix).
				Str("foreign_node", source).
				Str("context", "incoming/source").
				Msg("cross-tenant node detected in REST handler result — excluding from response")
			continue
		}
		edges = append(edges, map[string]string{
			"source":       cleanSource,
			"target":       nodeID,
			"relationship": relationship,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id": nodeID,
		"edges":   edges,
		"count":   len(edges),
	})
}

// handleTenantGraphOutgoing returns outgoing edges from a node.
// GET /api/v1/tenant/{tenant_id}/graph/{node_id}/out
func (s *Server) handleTenantGraphOutgoing(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	nodeID := chi.URLParam(r, "node_id")
	if nodeID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "node_id required")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)
	internalID := addPrefix(prefix, nodeID)

	outgoing, err := s.graph.GetNeighbors(internalID)
	if err != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, err.Error())
		return
	}

	edges := make([]map[string]string, 0, len(outgoing))
	for target, relationship := range outgoing {
		cleanTarget := stripPrefix(prefix, target)
		if prefix != "" && strings.Contains(cleanTarget, "@") {
			s.logger.Warn().
				Str("tenant_prefix", prefix).
				Str("foreign_node", target).
				Str("context", "outgoing/target").
				Msg("cross-tenant node detected in REST handler result — excluding from response")
			continue
		}
		edges = append(edges, map[string]string{
			"source":       nodeID,
			"target":       cleanTarget,
			"relationship": relationship,
		})
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_id": nodeID,
		"edges":   edges,
		"count":   len(edges),
	})
}

// tenantPathResult strips the tenant prefix from each node in path, guards
// against cross-tenant leakage, and returns the clean path together with a
// safe edge-count length.  The length is max(len(path)-1, 0): a single-node
// path (from == to) has length 0, and an empty path — which can only arise
// when guardSlice filters every node due to data-integrity contamination —
// also returns 0 rather than the nonsensical -1 that a bare len-1 would give.
func (s *Server) tenantPathResult(prefix string, path []string, context string) ([]string, int) {
	guarded := s.guardSlice(prefix, stripPrefixFromSlice(prefix, path), context)
	length := len(guarded) - 1
	if length < 0 {
		length = 0
	}
	return guarded, length
}

// handleTenantGraphPath finds a path between two nodes within the tenant.
// Returns 404 when no path exists within maxDepth.
// POST /api/v1/tenant/{tenant_id}/graph/path
func (s *Server) handleTenantGraphPath(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		MaxDepth int    `json:"max_depth"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.From == "" || req.To == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "from and to are required")
		return
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = s.config.MaxQueryDepth
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)

	path, err := s.graph.FindPath(addPrefix(prefix, req.From), addPrefix(prefix, req.To), req.MaxDepth)
	if err != nil {
		s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound, err.Error())
		return
	}

	guardedPath, length := s.tenantPathResult(prefix, path, "path")
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"from":   req.From,
		"to":     req.To,
		"path":   guardedPath,
		"length": length,
	})
}

// handleTenantGraphNeighbors gets neighbours of a node within the tenant.
// POST /api/v1/tenant/{tenant_id}/graph/neighbors
func (s *Server) handleTenantGraphNeighbors(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	var req struct {
		NodeID    string `json:"node_id"`
		Direction string `json:"direction"` // "out", "in", or "both"
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.NodeID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "node_id required")
		return
	}
	if req.Direction == "" {
		req.Direction = "out"
	}
	if req.Direction != "out" && req.Direction != "in" && req.Direction != "both" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, `direction must be "out", "in", or "both"`)
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)
	internalID := addPrefix(prefix, req.NodeID)

	result := make(map[string]interface{})

	if req.Direction == "out" || req.Direction == "both" {
		neighbors, err := s.graph.GetNeighbors(internalID)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, err.Error())
			return
		}
		result["outgoing"] = s.guardEdgeMap(prefix, stripPrefixFromEdgeMap(prefix, neighbors), "neighbors/outgoing")
	}

	if req.Direction == "in" || req.Direction == "both" {
		incoming, err := s.graph.GetIncomingEdges(internalID)
		if err != nil {
			s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, err.Error())
			return
		}
		result["incoming"] = s.guardEdgeMap(prefix, stripPrefixFromEdgeMap(prefix, incoming), "neighbors/incoming")
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"neighbors": result,
	})
}

// handleTenantGraphShortestPath finds the shortest path between two nodes.
// Unlike handleTenantGraphPath, a missing path is not an error: it returns
// HTTP 200 with "exists": false so callers can always decode the same shape.
// POST /api/v1/tenant/{tenant_id}/graph/shortestPath
func (s *Server) handleTenantGraphShortestPath(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		MaxDepth int    `json:"max_depth"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.From == "" || req.To == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "from and to are required")
		return
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = s.config.MaxQueryDepth
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)

	path, err := s.graph.FindPath(addPrefix(prefix, req.From), addPrefix(prefix, req.To), req.MaxDepth)
	if err != nil {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"from":   req.From,
			"to":     req.To,
			"exists": false,
			"path":   nil,
			"length": 0,
		})
		return
	}

	guardedPath, length := s.tenantPathResult(prefix, path, "shortestPath")
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"from":   req.From,
		"to":     req.To,
		"exists": true,
		"path":   guardedPath,
		"length": length,
	})
}

// POST /api/v1/tenant/{tenant_id}/graph/pathExists
func (s *Server) handleTenantGraphPathExists(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	var req struct {
		From     string `json:"from"`
		To       string `json:"to"`
		MaxDepth int    `json:"max_depth"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.From == "" || req.To == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "from and to are required")
		return
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = s.config.MaxQueryDepth
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)

	exists, length, err := s.graph.PathExists(addPrefix(prefix, req.From), addPrefix(prefix, req.To), req.MaxDepth)
	if err != nil {
		// Use the client-supplied IDs in the error message, not the internal
		// prefixed form, to avoid leaking the XXXX@ tenant prefix.
		s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound,
			fmt.Sprintf("node %s or %s not found", req.From, req.To))
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"from":   req.From,
		"to":     req.To,
		"exists": exists,
		"length": length,
	})
}

// handleTenantGraphCommonNeighbors finds shared out-neighbours of two nodes.
// POST /api/v1/tenant/{tenant_id}/graph/commonNeighbors
func (s *Server) handleTenantGraphCommonNeighbors(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	var req struct {
		NodeA string `json:"node_a"`
		NodeB string `json:"node_b"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.NodeA == "" || req.NodeB == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "node_a and node_b are required")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)

	common, err := s.graph.SharedOutNeighbors(addPrefix(prefix, req.NodeA), addPrefix(prefix, req.NodeB))
	if err != nil {
		s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound, err.Error())
		return
	}

	stripped := s.guardSlice(prefix, stripPrefixFromSlice(prefix, common), "commonNeighbors")
	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"node_a": req.NodeA,
		"node_b": req.NodeB,
		"common": stripped,
		"count":  len(stripped),
	})
}

// handleTenantGraphNodeSearch searches for nodes within the tenant's subgraph.
// POST /api/v1/tenant/{tenant_id}/graph/nodes/search
func (s *Server) handleTenantGraphNodeSearch(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	var req struct {
		Entity string `json:"entity"`
		Limit  int    `json:"limit"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}

	tid := getTenantIDNumeric(r.Context())
	prefix := tenant.GraphNodePrefix(tid)

	var rawNodes []string
	if req.Entity != "" {
		// Prefer the adapted table when available: it returns every entity of
		// that type, including those with no edges absent from the graph index.
		tenantStore, storeErr := s.storeForTenant(tid)
		if storeErr == nil {
			if sqlStore, ok := tenantStore.(*storage.SQLiteStore); ok {
				if ids := s.adaptedEntityIDs(r.Context(), sqlStore, req.Entity, tid); ids != nil {
					rawNodes = ids
				}
			}
		}
		if rawNodes == nil {
			nodes, err := s.graph.GetNodesByTypeForTenant(prefix, req.Entity)
			if err != nil {
				s.writeError(w, http.StatusBadRequest, oluerr.ErrTenantRequired, err.Error())
				return
			}
			rawNodes = nodes
		}
	} else {
		nodes, err := s.graph.GetAllNodesForTenant(prefix)
		if err != nil {
			s.writeError(w, http.StatusBadRequest, oluerr.ErrTenantRequired, err.Error())
			return
		}
		rawNodes = nodes
	}

	nodes := s.guardSlice(prefix, stripPrefixFromSlice(prefix, rawNodes), "nodeSearch")
	if nodes == nil {
		nodes = []string{}
	}

	if req.Limit > 0 && len(nodes) > req.Limit {
		nodes = nodes[:req.Limit]
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"nodes": nodes,
		"count": len(nodes),
	})
}

// =============================================================================
// Tenant-scoped Sulpher query handlers
// =============================================================================

// handleTenantSulpherQuery executes a Sulpher query synchronously within the
// tenant's subgraph.
// POST /api/v1/tenant/{tenant_id}/graph/query
func (s *Server) handleTenantSulpherQuery(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	jm := s.sulpherJobsForTenant(tid)
	if jm == nil {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrQueryEngineNotInit, "Sulpher query engine not initialized")
		return
	}

	var req struct {
		Query    string `json:"query"`
		MaxDepth int    `json:"max_depth"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Query == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrQueryRequired, "Query is required")
		return
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = s.config.MaxQueryDepth
	}

	timeout := time.Duration(s.config.QueryTimeout) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	result, err := jm.ExecuteSync(ctx, req.Query, req.MaxDepth)
	if err != nil {
		code := oluerr.ErrGraphFailed
		status := http.StatusBadRequest
		if ctx.Err() != nil {
			code = oluerr.ErrQueryTimeout
			status = http.StatusGatewayTimeout
		} else if errors.Is(err, sulpher.ErrVisitedNodeLimit) {
			code = oluerr.ErrGraphVisitedLimit
			status = http.StatusRequestEntityTooLarge
		} else if errors.Is(err, sulpher.ErrResultLimit) {
			code = oluerr.ErrGraphResultLimit
			status = http.StatusRequestEntityTooLarge
		}
		s.writeError(w, status, code, err.Error())
		return
	}

	response := map[string]interface{}{
		"status": "completed",
		"result": result.Data,
		"stats": map[string]interface{}{
			"nodes_traversed":   result.Stats.NodesTraversed,
			"paths_found":       result.Stats.PathsFound,
			"execution_time_ms": result.Stats.ExecutionTime.Milliseconds(),
		},
	}

	maxBytes := s.config.QueryMaxResponseBytes
	if maxBytes <= 0 {
		maxBytes = 10 * 1024 * 1024
	}
	encoded, jsonErr := json.Marshal(response)
	if jsonErr != nil {
		s.writeError(w, http.StatusInternalServerError, oluerr.ErrQueryFailed, "failed to encode response")
		return
	}
	if len(encoded) > maxBytes {
		s.writeError(w, http.StatusRequestEntityTooLarge, oluerr.ErrQueryResponseSize,
			fmt.Sprintf("response too large: %d bytes (max %d)", len(encoded), maxBytes))
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write(encoded) //nolint:errcheck

	if result.Stats.ExecutionTime > 5*time.Second {
		s.logger.Warn().
			Str("type", "sulpher_tenant").
			Uint16("tenant_id", tid).
			Int64("duration_ms", result.Stats.ExecutionTime.Milliseconds()).
			Int("nodes_traversed", result.Stats.NodesTraversed).
			Int("paths_found", result.Stats.PathsFound).
			Int("response_bytes", len(encoded)).
			Msg("Slow query")
	}
}

// handleTenantSulpherQueryAsync submits a Sulpher query for async execution
// within the tenant's subgraph.
// POST /api/v1/tenant/{tenant_id}/graph/query/async
func (s *Server) handleTenantSulpherQueryAsync(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	jm := s.sulpherJobsForTenant(tid)
	if jm == nil {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrQueryEngineNotInit, "Sulpher query engine not initialized")
		return
	}

	var req struct {
		Query    string `json:"query"`
		MaxDepth int    `json:"max_depth"`
	}
	if !s.decodeJSON(w, r, &req) {
		return
	}
	if req.Query == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrQueryRequired, "Query is required")
		return
	}
	if req.MaxDepth <= 0 {
		req.MaxDepth = s.config.MaxQueryDepth
	}

	job, err := jm.Submit(req.Query, req.MaxDepth)
	if err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidEntity, err.Error())
		return
	}

	s.writeJSON(w, http.StatusAccepted, map[string]interface{}{
		"query_id":   job.ID,
		"status":     sulpher.StatusPending, // always pending at submission time; avoid reading from shared pointer
		"created_at": job.CreatedAt,
	})
}

// handleTenantSulpherQueryStatus returns the status of an async query.
// GET /api/v1/tenant/{tenant_id}/graph/query/{query_id}
func (s *Server) handleTenantSulpherQueryStatus(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	jm := s.sulpherJobsForTenant(tid)
	if jm == nil {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrQueryEngineNotInit, "Sulpher query engine not initialized")
		return
	}

	queryID := chi.URLParam(r, "query_id")
	if queryID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "query_id required")
		return
	}

	job, exists := jm.GetJob(queryID)
	if !exists {
		s.writeError(w, http.StatusNotFound, oluerr.ErrQueryNotFound, "Query not found")
		return
	}

	response := map[string]interface{}{
		"query_id":   job.ID,
		"query":      job.Query,
		"status":     job.Status,
		"created_at": job.CreatedAt,
	}
	if job.StartedAt != nil {
		response["started_at"] = job.StartedAt
	}
	if job.EndedAt != nil {
		response["ended_at"] = job.EndedAt
	}
	if job.Error != "" {
		response["error"] = job.Error
	}

	s.writeJSON(w, http.StatusOK, response)
}

// handleTenantSulpherQueryResult returns the result of a completed async query.
// GET /api/v1/tenant/{tenant_id}/graph/query/{query_id}/result
func (s *Server) handleTenantSulpherQueryResult(w http.ResponseWriter, r *http.Request) {
	if !s.config.GraphEnabled {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrGraphDisabled, "Graph operations are disabled")
		return
	}

	tid := getTenantIDNumeric(r.Context())
	jm := s.sulpherJobsForTenant(tid)
	if jm == nil {
		s.writeError(w, http.StatusNotImplemented, oluerr.ErrQueryEngineNotInit, "Sulpher query engine not initialized")
		return
	}

	queryID := chi.URLParam(r, "query_id")
	if queryID == "" {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "query_id required")
		return
	}

	job, exists := jm.GetJob(queryID)
	if !exists {
		s.writeError(w, http.StatusNotFound, oluerr.ErrQueryNotFound, "Query not found")
		return
	}

	if job.Status == "pending" || job.Status == "running" {
		s.writeJSON(w, http.StatusAccepted, map[string]interface{}{
			"query_id": job.ID,
			"status":   job.Status,
			"message":  "Query is still processing",
		})
		return
	}

	if job.Status == "failed" {
		s.writeJSON(w, http.StatusOK, map[string]interface{}{
			"query_id": job.ID,
			"status":   job.Status,
			"error":    job.Error,
		})
		return
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"query_id": job.ID,
		"status":   job.Status,
		"result":   job.Result.Data,
		"stats": map[string]interface{}{
			"nodes_traversed":   job.Result.Stats.NodesTraversed,
			"paths_found":       job.Result.Stats.PathsFound,
			"execution_time_ms": job.Result.Stats.ExecutionTime.Milliseconds(),
		},
	})
}
