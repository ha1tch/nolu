// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package xoluclient provides a thin HTTP client for the xolu REST API.
//
// Only the operations required by nolu's e2e test are implemented:
// health check, entity create, entity get, entity delete, and entity exists.
// This is intentionally minimal — nolu does not replicate or proxy xolu data,
// it only needs to verify that entities exist where the registry says they do.
package xoluclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Client is a thin HTTP client for a single xolu instance.
type Client struct {
	baseURL    string
	tenantID   uint16
	tenantName string // named tenant for path-mode xolu (set by NewTenant)
	httpClient *http.Client
}

// New creates a Client targeting the xolu instance at baseURL.
// If tenantID is non-zero, all entity operations are scoped to that tenant.
func New(baseURL string, tenantID uint16) *Client {
	return &Client{
		baseURL:  baseURL,
		tenantID: tenantID,
		httpClient: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

// Healthy returns nil if the xolu instance responds to GET /health.
func (c *Client) Healthy(ctx context.Context) error {
	resp, err := c.get(ctx, "/health")
	if err != nil {
		return fmt.Errorf("xolu health: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xolu health: status %d", resp.StatusCode)
	}
	return nil
}

// Create POSTs data to /api/v1/{entity} and returns the created entity
// including its server-assigned integer id.
func (c *Client) Create(ctx context.Context, entity string, data map[string]interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("xolu create %s: marshal: %w", entity, err)
	}

	resp, err := c.post(ctx, c.entityPath(entity), body)
	if err != nil {
		return nil, fmt.Errorf("xolu create %s: %w", entity, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return nil, fmt.Errorf("xolu create %s: status %d: %s", entity, resp.StatusCode, readBody(resp.Body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("xolu create %s: decode response: %w", entity, err)
	}
	return result, nil
}

// Get retrieves a single entity by its integer id.
// Returns ErrNotFound if the entity does not exist.
func (c *Client) Get(ctx context.Context, entity string, id int) (map[string]interface{}, error) {
	resp, err := c.get(ctx, fmt.Sprintf("%s/%d", c.entityPath(entity), id))
	if err != nil {
		return nil, fmt.Errorf("xolu get %s/%d: %w", entity, id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ErrNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xolu get %s/%d: status %d", entity, id, resp.StatusCode)
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("xolu get %s/%d: decode: %w", entity, id, err)
	}
	return result, nil
}

// Exists returns true if the entity with the given id exists on this instance.
func (c *Client) Exists(ctx context.Context, entity string, id int) (bool, error) {
	_, err := c.Get(ctx, entity, id)
	if err == ErrNotFound {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// Delete removes an entity by id. Returns nil if the entity was deleted or
// did not exist (idempotent).
func (c *Client) Delete(ctx context.Context, entity string, id int) error {
	resp, err := c.do(ctx, http.MethodDelete, fmt.Sprintf("%s/%d", c.entityPath(entity), id), nil)
	if err != nil {
		return fmt.Errorf("xolu delete %s/%d: %w", entity, id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusNoContent || resp.StatusCode == http.StatusOK {
		return nil
	}
	return fmt.Errorf("xolu delete %s/%d: status %d", entity, id, resp.StatusCode)
}

// Patch applies a partial update to an entity.
func (c *Client) Patch(ctx context.Context, entity string, id int, data map[string]interface{}) (map[string]interface{}, error) {
	body, err := json.Marshal(data)
	if err != nil {
		return nil, fmt.Errorf("xolu patch %s/%d: marshal: %w", entity, id, err)
	}

	resp, err := c.do(ctx, http.MethodPatch, fmt.Sprintf("%s/%d", c.entityPath(entity), id), body)
	if err != nil {
		return nil, fmt.Errorf("xolu patch %s/%d: %w", entity, id, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xolu patch %s/%d: status %d: %s", entity, id, resp.StatusCode, readBody(resp.Body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("xolu patch %s/%d: decode: %w", entity, id, err)
	}
	return result, nil
}

// IntID extracts the integer "id" field from a Create response.
func IntID(data map[string]interface{}) (int, error) {
	v, ok := data["id"]
	if !ok {
		return 0, fmt.Errorf("xoluclient: response has no 'id' field")
	}
	switch id := v.(type) {
	case float64:
		return int(id), nil
	case int:
		return id, nil
	case int64:
		return int(id), nil
	}
	return 0, fmt.Errorf("xoluclient: 'id' field has unexpected type %T", v)
}

// ── Errors ────────────────────────────────────────────────────────────────────

// ErrNotFound is returned when xolu responds with 404.
var ErrNotFound = fmt.Errorf("xolu: entity not found")

// ── Internals ─────────────────────────────────────────────────────────────────

// entityPath returns the API path for an entity collection, tenant-scoped
// when the client was constructed with a tenantName or non-zero tenantID.
func (c *Client) entityPath(entity string) string {
	if c.tenantName != "" {
		return fmt.Sprintf("/api/v1/tenant/%s/%s", c.tenantName, entity)
	}
	if c.tenantID != 0 {
		return fmt.Sprintf("/api/v1/tenant/%d/%s", c.tenantID, entity)
	}
	return fmt.Sprintf("/api/v1/%s", entity)
}

func (c *Client) get(ctx context.Context, path string) (*http.Response, error) {
	return c.do(ctx, http.MethodGet, path, nil)
}

func (c *Client) post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	return c.do(ctx, http.MethodPost, path, body)
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(body)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bodyReader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	return c.httpClient.Do(req)
}

func readBody(r io.Reader) string {
	b, _ := io.ReadAll(io.LimitReader(r, 512))
	return string(b)
}

// ── Quiesce operations ────────────────────────────────────────────────────────

// QuiesceRequest is the optional body for QuiesceTenant.
type QuiesceRequest struct {
	RedirectURL string `json:"redirect_url,omitempty"`
}

// QuiesceResponse is the JSON shape returned by xolu's quiesce endpoints.
type QuiesceResponse struct {
	TenantID    uint16  `json:"tenant_id"`
	Quiesced    bool    `json:"quiesced"`
	QuiescedAt  string  `json:"quiesced_at,omitempty"`
	RedirectURL string  `json:"redirect_url,omitempty"`
	InFlight    int64   `json:"in_flight"`
	Drained     bool    `json:"drained"`
	DrainedAt   string  `json:"drained_at,omitempty"`
	Message     string  `json:"message,omitempty"`
}

// quiescePath returns the management URL for the named tenant's quiesce endpoint.
func (c *Client) quiescePath(tenantName string) string {
	return fmt.Sprintf("/api/v1/tenant/%s/quiesce", tenantName)
}

// QuiesceTenant activates quiesce for tenantName on this xolu instance.
// If redirectURL is non-empty, xolu will return 307 responses directing
// writers to the new instance. Returns the quiesce state on success.
func (c *Client) QuiesceTenant(ctx context.Context, tenantName, redirectURL string) (*QuiesceResponse, error) {
	body, _ := json.Marshal(QuiesceRequest{RedirectURL: redirectURL})
	resp, err := c.do(ctx, http.MethodPost, c.quiescePath(tenantName), body)
	if err != nil {
		return nil, fmt.Errorf("xolu quiesce %s: %w", tenantName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil, fmt.Errorf("xolu quiesce %s: already quiesced", tenantName)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xolu quiesce %s: status %d: %s", tenantName, resp.StatusCode, readBody(resp.Body))
	}
	var qr QuiesceResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return nil, fmt.Errorf("xolu quiesce %s: decode: %w", tenantName, err)
	}
	return &qr, nil
}

// QuiesceStatus returns the current quiesce state for tenantName.
// Returns nil and no error if the tenant is not currently quiesced.
func (c *Client) QuiesceStatus(ctx context.Context, tenantName string) (*QuiesceResponse, error) {
	resp, err := c.do(ctx, http.MethodGet, c.quiescePath(tenantName), nil)
	if err != nil {
		return nil, fmt.Errorf("xolu quiesce status %s: %w", tenantName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, nil // not quiesced
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("xolu quiesce status %s: status %d", tenantName, resp.StatusCode)
	}
	var qr QuiesceResponse
	if err := json.NewDecoder(resp.Body).Decode(&qr); err != nil {
		return nil, fmt.Errorf("xolu quiesce status %s: decode: %w", tenantName, err)
	}
	return &qr, nil
}

// UnquiesceTenant lifts quiesce for tenantName, restoring normal write access.
// Used during rollback to undo a quiesce that was not followed by a completed migration.
// Returns nil and no error if the tenant was not quiesced.
func (c *Client) UnquiesceTenant(ctx context.Context, tenantName string) error {
	resp, err := c.do(ctx, http.MethodDelete, c.quiescePath(tenantName), nil)
	if err != nil {
		return fmt.Errorf("xolu unquiesce %s: %w", tenantName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil // already not quiesced — idempotent
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("xolu unquiesce %s: status %d: %s", tenantName, resp.StatusCode, readBody(resp.Body))
	}
	return nil
}
