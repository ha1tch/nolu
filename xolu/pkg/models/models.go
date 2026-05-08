// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package models

import (
	"fmt"
	"errors"
	"encoding/json"
	"time"
)

// Entity represents a stored entity with its data
type Entity struct {
	ID   int                    `json:"id"`
	Type string                 `json:"type,omitempty"`
	Data map[string]interface{} `json:"-"`
}

// UnmarshalJSON implements custom unmarshaling for Entity
func (e *Entity) UnmarshalJSON(data []byte) error {
	var raw map[string]interface{}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	
	if id, ok := raw["id"].(float64); ok {
		e.ID = int(id)
	}
	if t, ok := raw["type"].(string); ok {
		e.Type = t
	}
	e.Data = raw
	return nil
}

// MarshalJSON implements custom marshaling for Entity
func (e *Entity) MarshalJSON() ([]byte, error) {
	return json.Marshal(e.Data)
}

// Reference represents a reference to another entity
type Reference struct {
	Type   string `json:"type"`
	Entity string `json:"entity"`
	ID     int64  `json:"id"`
}

// NewReference constructs a Reference from an entity name and integer ID.
// Use ToMap to obtain the map[string]interface{} form required for JSON storage.
func NewReference(entity string, id int64) *Reference {
	return &Reference{Type: "REF", Entity: entity, ID: id}
}

// ToMap returns the canonical map representation of this Reference as stored
// in entity JSON. The shape matches what json.Unmarshal produces on read-back,
// ensuring round-trip consistency. Callers building REF values for storage
// should use this rather than constructing raw maps by hand.
func (r *Reference) ToMap() map[string]interface{} {
	return map[string]interface{}{
		"type":   "REF",
		"entity": r.Entity,
		"id":     r.ID,
	}
}

// TSReference represents a timeseries link stored on a document entity.
// It is the typed counterpart of the {"type":"TSREF",...} map produced by
// @TIMESERIES / @TS in OQL INSERT statements.
//
// TSREFs are intentionally excluded from graph edge indexing — they point to
// a timeseries partition, not to another entity node. syncGraphEdges must not
// create graph edges for TSREF fields.
type TSReference struct {
	Type     string   `json:"type"`
	Timeline int      `json:"timeline"`
	Dims     []uint64 `json:"dims"`
}

// NewTSReference constructs a TSReference.
func NewTSReference(timeline int, dims []uint64) *TSReference {
	return &TSReference{Type: "TSREF", Timeline: timeline, Dims: dims}
}

// ToMap returns the canonical map representation of this TSReference for
// JSON storage, mirroring the shape produced by json.Unmarshal on read-back.
func (r *TSReference) ToMap() map[string]interface{} {
	dims := make([]interface{}, len(r.Dims))
	for i, d := range r.Dims {
		dims[i] = d
	}
	return map[string]interface{}{
		"type":     "TSREF",
		"timeline": r.Timeline,
		"dims":     dims,
	}
}

// IsTSReference checks if a value is a timeseries reference map.
func IsTSReference(v interface{}) (*TSReference, bool) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, false
	}
	typeVal, _ := m["type"].(string)
	if typeVal != "TSREF" {
		return nil, false
	}
	var timeline int
	switch t := m["timeline"].(type) {
	case float64:
		timeline = int(t)
	case int:
		timeline = t
	default:
		return nil, false
	}
	var dims []uint64
	if rawDims, ok := m["dims"].([]interface{}); ok {
		for _, d := range rawDims {
			switch dv := d.(type) {
			case float64:
				dims = append(dims, uint64(dv))
			case uint64:
				dims = append(dims, dv)
			case int:
				dims = append(dims, uint64(dv))
			}
		}
	}
	return &TSReference{Type: "TSREF", Timeline: timeline, Dims: dims}, true
}

// ExtractRefs returns all REF values from a field value as typed References.
// It handles both a single REF map (the common case from @REF) and a
// []interface{} slice of REF maps (from @REFS). TSREF values are silently
// excluded — timeseries links are resolved at query time and must not become
// graph edges. Any non-REF value returns nil.
//
// This is the single point of containment for the map[string]interface{}
// type-switching that would otherwise be scattered across syncGraphEdges,
// UpdateFromEntityForTenant, and any future callers that need to walk entity
// fields looking for graph-indexable references.
func ExtractRefs(v interface{}) []*Reference {
	if ref, ok := IsReference(v); ok {
		return []*Reference{ref}
	}
	slice, ok := v.([]interface{})
	if !ok {
		return nil
	}
	var refs []*Reference
	for _, elem := range slice {
		if ref, ok := IsReference(elem); ok {
			refs = append(refs, ref)
		}
	}
	return refs
}

// IsReference checks if a value is a reference
func IsReference(v interface{}) (*Reference, bool) {
	m, ok := v.(map[string]interface{})
	if !ok {
		return nil, false
	}
	
	typeVal, hasType := m["type"].(string)
	entityVal, hasEntity := m["entity"].(string)
	idVal, hasID := m["id"]
	
	if hasType && typeVal == "REF" && hasEntity && hasID {
		var id int64
		switch v := idVal.(type) {
		case float64:
			id = int64(v)
		case int:
			id = int64(v)
		case int64:
			id = v
		default:
			return nil, false
		}
		return &Reference{
			Type:   typeVal,
			Entity: entityVal,
			ID:     id,
		}, true
	}
	return nil, false
}

// QueryStats tracks query execution statistics
type QueryStats struct {
	StartTime time.Time              `json:"start_time"`
	EndTime   time.Time              `json:"end_time,omitempty"`
	Duration  float64                `json:"duration,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
}

// Query represents a stored query with its execution state
type Query struct {
	ID           string                   `json:"id"`
	QueryString  string                   `json:"query"`
	Status       string                   `json:"status"` // pending, running, completed, failed
	Result       interface{}              `json:"result,omitempty"`
	Error        string                   `json:"error,omitempty"`
	Stats        QueryStats               `json:"stats"`
	ParsedQuery  map[string]interface{}   `json:"parsed_query,omitempty"`
}

// PaginationParams represents pagination parameters
type PaginationParams struct {
	Page    int `json:"page"`
	PerPage int `json:"per_page"`
}

// SortParam represents a sort parameter
type SortParam struct {
	Field string `json:"field"`
	Order string `json:"order"` // asc or desc
}

// ErrorResponse represents an API error response
type ErrorResponse struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
		Status  int    `json:"status"`
	} `json:"error"`
}

// SuccessResponse represents a generic success response
type SuccessResponse struct {
	Message string      `json:"message"`
	Data    interface{} `json:"data,omitempty"`
}

// ResourceResponse represents a resource-based response
type ResourceResponse struct {
	Type  string      `json:"type"`
	ID    string      `json:"id,omitempty"`
	Data  interface{} `json:"data"`
	Links interface{} `json:"links,omitempty"`
	Meta  interface{} `json:"meta,omitempty"`
}

// PagedResponse represents a paginated response
type PagedResponse struct {
	Data       interface{} `json:"data"`
	Pagination struct {
		Page       int `json:"page"`
		PerPage    int `json:"per_page"`
		TotalItems int `json:"total_items"`
		TotalPages int `json:"total_pages"`
	} `json:"pagination"`
	Links map[string]string `json:"links,omitempty"`
}

// GraphNode represents a node in the graph
type GraphNode struct {
	ID         string                 `json:"id"`
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// GraphEdge represents an edge in the graph
type GraphEdge struct {
	From         string `json:"from"`
	To           string `json:"to"`
	Relationship string `json:"relationship"`
}

// ErrDuplicateEdgeTarget is returned by ExtractEntityEdges when two or more
// fields in the same entity document reference the same target (entity, id)
// pair. In olu's graph model each ordered node pair may carry at most one
// labelled edge. A document that produces two edges to the same target is
// malformed and must be rejected at write time.
var ErrDuplicateEdgeTarget = errors.New("entity has two REF fields pointing to the same target")

// EntityEdge is a raw graph edge extracted from entity data before any
// tenant-scoping or node-ID formatting is applied. It holds the target
// entity type, target ID, and the relationship name (the field key that
// carried the REF value in the source document).
//
// This is the canonical intermediate representation shared by all callers
// that need to extract graph edges from entity data:
//   - storage.SQLiteStore.syncGraphEdges  (writes to tenant graph_tXXXX table)
//   - server.Server.updateGraph           (writes to in-memory graph)
//   - any future backend that derives edges from entity JSON
//
// Keeping a single extraction function here ensures that a change to edge
// semantics (e.g. handling a new REF variant) is made in one place and
// automatically applies to both the durable edge table and the in-memory
// cache.
type EntityEdge struct {
	TargetEntity string
	TargetID     int
	Relationship string
}

// ExtractEntityEdges returns all graph edges implied by the REF fields in
// data. It iterates every key in data (skipping "id"), calls ExtractRefs on
// each value, and converts valid references to EntityEdge values.
//
// This is the single source of truth for "which fields in an entity
// document become graph edges". Both the SQLite storage layer
// (syncGraphEdges) and the server layer (updateGraph) must call this
// function rather than inlining their own ExtractRefs loops, so that the
// two pipelines are structurally guaranteed to agree.
//
// Returns ErrDuplicateEdgeTarget if two fields in data reference the same
// (entity, id) target. In olu's graph model each ordered node pair carries
// at most one labelled edge; a document violating this is malformed.
func ExtractEntityEdges(data map[string]interface{}) ([]EntityEdge, error) {
	var edges []EntityEdge
	seen := make(map[[2]string]string) // [entity, id-str] -> field key that claimed it first
	for key, value := range data {
		if key == "id" {
			continue
		}
		for _, ref := range ExtractRefs(value) {
			// Guard only on empty entity — an empty entity string produces a
			// malformed node ID (":N") and must be rejected. ID 0 is a valid
			// entity ID in olu (auto-increment starts at 1 by convention but
			// nothing prevents explicit use of 0) and must not be filtered.
			if ref.Entity == "" {
				continue
			}
			targetKey := [2]string{ref.Entity, fmt.Sprintf("%d", ref.ID)}
			if first, dup := seen[targetKey]; dup {
				if first == key {
					return nil, fmt.Errorf("%w: target %s:%d appears more than once in field %q",
						ErrDuplicateEdgeTarget, ref.Entity, ref.ID, key)
				}
				return nil, fmt.Errorf("%w: target %s:%d claimed by both field %q and field %q",
					ErrDuplicateEdgeTarget, ref.Entity, ref.ID, first, key)
			}
			seen[targetKey] = key
			edges = append(edges, EntityEdge{
				TargetEntity: ref.Entity,
				TargetID:     int(ref.ID),
				Relationship: key,
			})
		}
	}
	return edges, nil
}

// PathInfo represents a path between two nodes
type PathInfo struct {
	From   string        `json:"from"`
	To     string        `json:"to"`
	Length int           `json:"length"`
	Path   []interface{} `json:"path"`
}
