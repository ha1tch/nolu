// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package xoluclient

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
)

// NewTenant creates a Client scoped to a named tenant. All entity operations
// will use the /api/v1/tenant/{tenantName}/... path prefix.
// tenantName is a string (xolu's path-mode tenant identifier).
func NewTenant(baseURL, tenantName string) *Client {
	c := New(baseURL, 0)
	c.tenantName = tenantName
	return c
}

// OQL executes an OQL query against the xolu instance and returns the results
// as a slice of raw JSON maps. Each map is one row.
//
// For tenant-scoped clients, the query runs against the tenant's store.
func (c *Client) OQL(ctx context.Context, query string) ([]map[string]interface{}, error) {
	body, err := json.Marshal(map[string]string{"query": query})
	if err != nil {
		return nil, fmt.Errorf("xolu oql: marshal query: %w", err)
	}

	resp, err := c.post(ctx, c.oqlPath(), body)
	if err != nil {
		return nil, fmt.Errorf("xolu oql: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xolu oql: status %d: %s", resp.StatusCode, readBody(resp.Body))
	}

	// xolu OQL response shape: {"data": [...], "pagination": {...}}
	var envelope struct {
		Data []map[string]interface{} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&envelope); err != nil {
		return nil, fmt.Errorf("xolu oql: decode: %w", err)
	}
	if envelope.Data == nil {
		return []map[string]interface{}{}, nil
	}
	return envelope.Data, nil
}

// Save upserts an entity at a specific integer ID using xolu's
// POST /api/v1/{entity}/save/{id} endpoint.
//
// If data contains "_version", the write is conditional: xolu will return 409
// if the stored version does not match. This is the optimistic concurrency
// mechanism used by XoluRegistry.Transfer and XoluRegistry.Retire.
//
// Returns the full entity document as returned by xolu.
func (c *Client) Save(ctx context.Context, entity string, id int, data map[string]interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("xolu save %s/%d: marshal: %w", entity, id, err)
	}

	path := fmt.Sprintf("%s/save/%d", c.entityPath(entity), id)
	resp, err := c.post(ctx, path, body)
	if err != nil {
		return nil, fmt.Errorf("xolu save %s/%d: %w", entity, id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusConflict {
		raw := readBody(resp.Body)
		return nil, fmt.Errorf("xolu save %s/%d: status 409: %s", entity, id, raw)
	}
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("xolu save %s/%d: status %d: %s", entity, id, resp.StatusCode, readBody(resp.Body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("xolu save %s/%d: decode: %w", entity, id, err)
	}
	return result, nil
}

// EnsureTenant verifies that a named tenant exists and is accessible on a
// xolu instance running in strict mode.
//
// In strict mode, tenants must be pre-registered via iolu before xolu starts.
// This method confirms the tenant was registered successfully by performing
// a probe entity GET that will return a well-typed xolu error rather than a
// generic "Unknown tenant" error if the tenant is missing.
//
// Distinguishes:
//   - "Unknown tenant" (OLU-ST001) → tenant not registered → return error
//   - "entity not found" / empty results → tenant exists, no data yet → OK
//   - HTTP 200 → tenant exists and has data → OK
func (c *Client) EnsureTenant(ctx context.Context, tenantName string) error {
	probe := NewTenant(c.baseURL, tenantName)
	// Attempt to GET a nonexistent entity ID (0).
	// If the tenant exists: returns 404 OLU-ST002/OLU-EN001 (entity not found) → OK.
	// If the tenant is unknown: returns 404 OLU-ST001 (Unknown tenant) → error.
	_, err := probe.Get(ctx, "_probe", 0)
	if err == nil || err == ErrNotFound {
		// Either found something (unlikely for id=0) or got a clean 404 on entity.
		return nil
	}
	msg := err.Error()
	// OLU-ST001 means the tenant itself is unknown — iolu didn't register it.
	if containsStr(msg, "OLU-ST001") || containsStr(msg, "Unknown tenant") {
		return fmt.Errorf("xolu ensure_tenant %q: tenant not registered in xolu strict mode (run iolu tenant create): %w", tenantName, err)
	}
	// Any other error (entity not found, OQL errors) means tenant exists.
	return nil
}

func containsStr(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// IntIDFromMap extracts the integer "id" field from a raw response map.
// Convenience wrapper for callers that work with map[string]interface{}.
func IntIDFromMap(m map[string]interface{}) (int, error) {
	return IntID(m)
}


// oqlPath returns the OQL endpoint path, tenant-scoped if applicable.
func (c *Client) oqlPath() string {
	if c.tenantName != "" {
		return fmt.Sprintf("/api/v1/tenant/%s/oql/query", c.tenantName)
	}
	if c.tenantID != 0 {
		return fmt.Sprintf("/api/v1/tenant/%d/oql/query", c.tenantID)
	}
	return "/api/v1/oql/query"
}





