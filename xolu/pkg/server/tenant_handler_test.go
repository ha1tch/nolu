// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/tenant"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Noop stubs for cache and validator (handler tests focus on tenant wiring)
// ---------------------------------------------------------------------------

type noopCache struct{}

func (c *noopCache) Get(_ context.Context, _ string) (interface{}, error) {
	return nil, fmt.Errorf("miss")
}
func (c *noopCache) Set(_ context.Context, _ string, _ interface{}, _ time.Duration) error {
	return nil
}
func (c *noopCache) Delete(_ context.Context, _ string) error       { return nil }
func (c *noopCache) DeletePattern(_ context.Context, _ string) error { return nil }
func (c *noopCache) Exists(_ context.Context, _ string) (bool, error) {
	return false, nil
}
func (c *noopCache) Close() error { return nil }

// Verify interface compliance
var _ cache.Cache = (*noopCache)(nil)

type noopValidator struct{}

func (v *noopValidator) Validate(_ string, _ map[string]interface{}) (bool, []string) {
	return true, nil
}
func (v *noopValidator) LoadSchema(_ string, _ map[string]interface{}) error { return nil }
func (v *noopValidator) HasSchema(_ string) bool                             { return false }
func (v *noopValidator) GetSchema(_ string) (map[string]interface{}, error) {
	return nil, fmt.Errorf("no schema")
}
func (v *noopValidator) SaveSchema(_ string, _ map[string]interface{}) error { return nil }

var _ validation.Validator = (*noopValidator)(nil)

// ---------------------------------------------------------------------------
// Test server factory
// ---------------------------------------------------------------------------

// newTestServer creates a minimal Server with two registered tenants ("alpha"
// and "beta") backed by the same SQLite database. The chi router is fully
// wired, so requests routed through it exercise middleware + handlers.
func newTestServer(t *testing.T) *Server {
	return newTestServerWithMode(t, "strict")
}

func newTestServerWithMode(t *testing.T, tenantMode string) *Server {
	t.Helper()

	dbPath := filepath.Join(t.TempDir(), "handler_tenant.db")

	// Create base store (tenant 0)
	baseStore, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:            "sqlite",
		DBPath:          dbPath,
		FullTextEnabled: true,
		GraphEnabled:    false,
		TenantID:        0,
	})
	if err != nil {
		t.Fatalf("base store: %v", err)
	}
	t.Cleanup(func() { baseStore.Close() })

	cfg := config.Default()
	cfg.StorageType = "sqlite"
	cfg.DBPath = dbPath
	cfg.FullTextEnabled = true
	cfg.GraphEnabled = false
	cfg.AuthType = "none"
	cfg.MaxEntitySize = 1 << 20 // 1 MB
	cfg.DefaultPageSize = 100
	cfg.MaxEmbedDepth = 0
	cfg.RefEmbedDepth = 0
	cfg.TenantMode = tenantMode
	cfg.TenantAutoRegister = (tenantMode == "path") // path mode tests rely on auto-registration

	logger := zerolog.Nop()

	s := New(cfg, baseStore, &noopCache{}, nil, nil, &noopValidator{}, logger)

	// Register two tenants
	s.tenantRegistry.Register(context.Background(), "alpha", 1)
	s.tenantRegistry.Register(context.Background(), "beta", 2)

	return s
}

// doRequest sends an HTTP request through the server's router and returns
// the response recorder and decoded JSON body.
func doRequest(t *testing.T, s *Server, method, path string, body interface{}) (*httptest.ResponseRecorder, map[string]interface{}) {
	t.Helper()

	var reqBody *bytes.Buffer
	if body != nil {
		b, _ := json.Marshal(body)
		reqBody = bytes.NewBuffer(b)
	} else {
		reqBody = bytes.NewBuffer(nil)
	}

	req := httptest.NewRequest(method, path, reqBody)
	req.Header.Set("Content-Type", "application/json")

	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	var result map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &result)
	return rr, result
}

// doRequestList is like doRequest but decodes a paginated list response,
// returning the "data" array from the PagedResponse wrapper.
func doRequestList(t *testing.T, s *Server, method, path string) (*httptest.ResponseRecorder, []interface{}) {
	t.Helper()

	req := httptest.NewRequest(method, path, nil)
	rr := httptest.NewRecorder()
	s.router.ServeHTTP(rr, req)

	// Response is {"data": [...], "pagination": {...}}
	var wrapper map[string]interface{}
	json.Unmarshal(rr.Body.Bytes(), &wrapper)

	if data, ok := wrapper["data"].([]interface{}); ok {
		return rr, data
	}
	// Fallback: try raw array (some endpoints may not paginate)
	var result []interface{}
	json.Unmarshal(rr.Body.Bytes(), &result)
	return rr, result
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

// TestHandler_TenantCreate verifies that POST to tenant routes creates
// entities in the correct tenant's scope.
func TestHandler_TenantCreate(t *testing.T) {
	s := newTestServer(t)

	// Create in tenant alpha
	rr, body := doRequest(t, s, "POST", "/api/v1/tenant/alpha/widgets",
		map[string]interface{}{"name": "Alpha Widget", "color": "red"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("alpha create: status %d, body: %s", rr.Code, rr.Body.String())
	}
	alphaID := body["id"]

	// Create in tenant beta
	rr, body = doRequest(t, s, "POST", "/api/v1/tenant/beta/widgets",
		map[string]interface{}{"name": "Beta Widget", "color": "blue"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("beta create: status %d, body: %s", rr.Code, rr.Body.String())
	}
	betaID := body["id"]

	// Both should get ID 1 (per-tenant sequences)
	if alphaID != float64(1) {
		t.Errorf("alpha ID = %v, want 1", alphaID)
	}
	if betaID != float64(1) {
		t.Errorf("beta ID = %v, want 1", betaID)
	}
}

// TestHandler_TenantListIsolation verifies that GET list returns only
// the requesting tenant's entities.
func TestHandler_TenantListIsolation(t *testing.T) {
	s := newTestServer(t)

	// Seed: 3 items in alpha, 2 in beta
	for i := 0; i < 3; i++ {
		doRequest(t, s, "POST", "/api/v1/tenant/alpha/items",
			map[string]interface{}{"n": i, "owner": "alpha"})
	}
	for i := 0; i < 2; i++ {
		doRequest(t, s, "POST", "/api/v1/tenant/beta/items",
			map[string]interface{}{"n": i, "owner": "beta"})
	}

	// List alpha
	rr, listA := doRequestList(t, s, "GET", "/api/v1/tenant/alpha/items")
	if rr.Code != http.StatusOK {
		t.Fatalf("alpha list: status %d", rr.Code)
	}
	if len(listA) != 3 {
		t.Errorf("alpha list = %d items, want 3", len(listA))
	}

	// List beta
	rr, listB := doRequestList(t, s, "GET", "/api/v1/tenant/beta/items")
	if rr.Code != http.StatusOK {
		t.Fatalf("beta list: status %d", rr.Code)
	}
	if len(listB) != 2 {
		t.Errorf("beta list = %d items, want 2", len(listB))
	}

	// Verify data ownership
	for _, item := range listA {
		rec, _ := item.(map[string]interface{})
		if owner, _ := rec["owner"].(string); owner != "alpha" {
			t.Errorf("alpha list contains owner=%q", owner)
		}
	}
	for _, item := range listB {
		rec, _ := item.(map[string]interface{})
		if owner, _ := rec["owner"].(string); owner != "beta" {
			t.Errorf("beta list contains owner=%q", owner)
		}
	}
}

// TestHandler_TenantGetIsolation verifies that GET by ID returns only
// the correct tenant's record (both tenants have ID 1).
func TestHandler_TenantGetIsolation(t *testing.T) {
	s := newTestServer(t)

	doRequest(t, s, "POST", "/api/v1/tenant/alpha/products",
		map[string]interface{}{"name": "Alpha Product"})
	doRequest(t, s, "POST", "/api/v1/tenant/beta/products",
		map[string]interface{}{"name": "Beta Product"})

	// Get ID 1 from alpha
	rr, bodyA := doRequest(t, s, "GET", "/api/v1/tenant/alpha/products/1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("alpha get: status %d", rr.Code)
	}
	if name, _ := bodyA["name"].(string); name != "Alpha Product" {
		t.Errorf("alpha get name = %q, want 'Alpha Product'", name)
	}

	// Get ID 1 from beta
	rr, bodyB := doRequest(t, s, "GET", "/api/v1/tenant/beta/products/1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("beta get: status %d", rr.Code)
	}
	if name, _ := bodyB["name"].(string); name != "Beta Product" {
		t.Errorf("beta get name = %q, want 'Beta Product'", name)
	}
}

// TestHandler_TenantUpdateIsolation verifies that PUT updates only
// the target tenant's record.
func TestHandler_TenantUpdateIsolation(t *testing.T) {
	s := newTestServer(t)

	doRequest(t, s, "POST", "/api/v1/tenant/alpha/configs",
		map[string]interface{}{"setting": "dark", "value": 1})
	doRequest(t, s, "POST", "/api/v1/tenant/beta/configs",
		map[string]interface{}{"setting": "dark", "value": 1})

	// Update alpha's record
	rr, _ := doRequest(t, s, "PUT", "/api/v1/tenant/alpha/configs/1",
		map[string]interface{}{"id": 1, "setting": "light", "value": 42})
	if rr.Code != http.StatusOK {
		t.Fatalf("alpha update: status %d, body: %s", rr.Code, rr.Body.String())
	}

	// Alpha's record should be updated
	_, bodyA := doRequest(t, s, "GET", "/api/v1/tenant/alpha/configs/1", nil)
	if s, _ := bodyA["setting"].(string); s != "light" {
		t.Errorf("alpha setting = %q, want 'light'", s)
	}

	// Beta's record should be unchanged
	_, bodyB := doRequest(t, s, "GET", "/api/v1/tenant/beta/configs/1", nil)
	if s, _ := bodyB["setting"].(string); s != "dark" {
		t.Errorf("beta setting = %q, want 'dark' (unchanged)", s)
	}
}

// TestHandler_TenantDeleteIsolation verifies that DELETE removes only
// the target tenant's record.
func TestHandler_TenantDeleteIsolation(t *testing.T) {
	s := newTestServer(t)

	doRequest(t, s, "POST", "/api/v1/tenant/alpha/notes",
		map[string]interface{}{"text": "alpha note"})
	doRequest(t, s, "POST", "/api/v1/tenant/beta/notes",
		map[string]interface{}{"text": "beta note"})

	// Delete from alpha
	rr, _ := doRequest(t, s, "DELETE", "/api/v1/tenant/alpha/notes/1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("alpha delete: status %d, body: %s", rr.Code, rr.Body.String())
	}

	// Alpha should have 0 notes
	_, listA := doRequestList(t, s, "GET", "/api/v1/tenant/alpha/notes")
	if len(listA) != 0 {
		t.Errorf("alpha after delete = %d notes, want 0", len(listA))
	}

	// Beta still has its note
	_, listB := doRequestList(t, s, "GET", "/api/v1/tenant/beta/notes")
	if len(listB) != 1 {
		t.Errorf("beta after alpha's delete = %d notes, want 1", len(listB))
	}
}

// TestHandler_TenantGetCrossTenantNotFound verifies that requesting an
// entity that exists in one tenant returns 404 from another tenant
// (when the other tenant doesn't have that ID).
func TestHandler_TenantGetCrossTenantNotFound(t *testing.T) {
	s := newTestServer(t)

	// Create 3 records in alpha, 0 in beta
	for i := 0; i < 3; i++ {
		doRequest(t, s, "POST", "/api/v1/tenant/alpha/things",
			map[string]interface{}{"n": i})
	}

	// Beta requesting ID 1 of "things" should 404 (beta has no things)
	rr, _ := doRequest(t, s, "GET", "/api/v1/tenant/beta/things/1", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("beta get alpha's entity: status %d, want 404", rr.Code)
	}
}

// TestHandler_TenantDeleteCrossTenantNotFound verifies that deleting
// an entity in one tenant that only exists in another returns 404.
func TestHandler_TenantDeleteCrossTenantNotFound(t *testing.T) {
	s := newTestServer(t)

	doRequest(t, s, "POST", "/api/v1/tenant/alpha/stuff",
		map[string]interface{}{"x": 1})

	rr, _ := doRequest(t, s, "DELETE", "/api/v1/tenant/beta/stuff/1", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("beta delete alpha's entity: status %d, want 404", rr.Code)
	}

	// Alpha's record should still exist
	rr, _ = doRequest(t, s, "GET", "/api/v1/tenant/alpha/stuff/1", nil)
	if rr.Code != http.StatusOK {
		t.Errorf("alpha's record should survive: status %d", rr.Code)
	}
}

// TestHandler_UnknownTenantReturns404 verifies the middleware rejects
// requests for unregistered tenant names.
func TestHandler_UnknownTenantReturns404(t *testing.T) {
	s := newTestServer(t)

	rr, _ := doRequest(t, s, "GET", "/api/v1/tenant/nonexistent/things", nil)
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown tenant: status %d, want 404", rr.Code)
	}
}

// TestHandler_NumericTenantFallback verifies the middleware falls back
// to numeric parsing when the tenant name isn't in the registry.
func TestHandler_NumericTenantFallback(t *testing.T) {
	s := newTestServer(t)

	// Tenant "1" is not registered by name, but numeric fallback resolves to ID 1
	// which is the same as "alpha"
	doRequest(t, s, "POST", "/api/v1/tenant/alpha/fallback_test",
		map[string]interface{}{"via": "name"})

	// Access via numeric ID
	rr, body := doRequest(t, s, "GET", "/api/v1/tenant/1/fallback_test/1", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("numeric tenant get: status %d, body: %s", rr.Code, rr.Body.String())
	}
	if via, _ := body["via"].(string); via != "name" {
		t.Errorf("numeric fallback returned via=%q, want 'name'", via)
	}
}

// TestHandler_NumericTenantFallbackEdgeCases exercises additional scenarios
// for the numeric tenant ID resolution.
func TestHandler_NumericTenantFallbackEdgeCases(t *testing.T) {
	t.Run("strict mode - unknown numeric ID returns 404", func(t *testing.T) {
		s := newTestServer(t) // strict mode, alpha=1 beta=2
		rr, _ := doRequest(t, s, "GET", "/api/v1/tenant/999/things", nil)
		if rr.Code != http.StatusNotFound {
			t.Errorf("expected 404 for unregistered numeric ID, got %d", rr.Code)
		}
	})

	t.Run("strict mode - zero is rejected", func(t *testing.T) {
		s := newTestServer(t)
		rr, _ := doRequest(t, s, "GET", "/api/v1/tenant/0/things", nil)
		if rr.Code != http.StatusNotFound {
			t.Errorf("expected 404 for tenant 0, got %d", rr.Code)
		}
	})

	t.Run("strict mode - second tenant via numeric ID", func(t *testing.T) {
		s := newTestServer(t) // alpha=1, beta=2

		// Write via name
		doRequest(t, s, "POST", "/api/v1/tenant/beta/widgets",
			map[string]interface{}{"colour": "red"})

		// Read via numeric ID 2 (= beta)
		rr, body := doRequest(t, s, "GET", "/api/v1/tenant/2/widgets/1", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("numeric tenant 2 get: status %d, body: %s", rr.Code, rr.Body.String())
		}
		if c, _ := body["colour"].(string); c != "red" {
			t.Errorf("got colour=%q, want 'red'", c)
		}
	})

	t.Run("name takes priority over numeric parse", func(t *testing.T) {
		// Register a tenant whose name is literally "2"
		s := newTestServer(t)
		s.tenantRegistry.Register(context.Background(), "2", 50)

		// Write via tenant name "2" (ID 50, NOT beta which is ID 2)
		doRequest(t, s, "POST", "/api/v1/tenant/2/items",
			map[string]interface{}{"source": "name-two"})

		// Read back — should hit ID 50 (name "2"), not ID 2 (name "beta")
		rr, body := doRequest(t, s, "GET", "/api/v1/tenant/2/items/1", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("status %d, body: %s", rr.Code, rr.Body.String())
		}
		if src, _ := body["source"].(string); src != "name-two" {
			t.Errorf("got source=%q, want 'name-two'", src)
		}

		// Confirm beta (ID 2) has no items
		rr2, _ := doRequest(t, s, "GET", "/api/v1/tenant/beta/items/1", nil)
		if rr2.Code != http.StatusNotFound {
			t.Errorf("expected 404 for beta/items/1, got %d", rr2.Code)
		}
	})

	t.Run("path mode - numeric ID resolves existing tenant", func(t *testing.T) {
		s := newTestServerWithMode(t, "path")
		s.tenantRegistry.Register(context.Background(), "gamma", 5)

		doRequest(t, s, "POST", "/api/v1/tenant/gamma/docs",
			map[string]interface{}{"text": "hello"})

		rr, body := doRequest(t, s, "GET", "/api/v1/tenant/5/docs/1", nil)
		if rr.Code != http.StatusOK {
			t.Fatalf("path mode numeric get: status %d, body: %s", rr.Code, rr.Body.String())
		}
		if txt, _ := body["text"].(string); txt != "hello" {
			t.Errorf("got text=%q, want 'hello'", txt)
		}
	})

	t.Run("path mode - unknown numeric auto-registers as name", func(t *testing.T) {
		// In path mode, "42" with no matching ID auto-registers as tenant name "42"
		s := newTestServerWithMode(t, "path")

		rr, _ := doRequest(t, s, "POST", "/api/v1/tenant/42/things",
			map[string]interface{}{"ok": true})
		if rr.Code != http.StatusCreated {
			t.Fatalf("expected 201, got %d", rr.Code)
		}
	})
}

// TestHandler_NonTenantRouteUsesTenantZero verifies that requests to
// non-tenant routes (no /tenant/ prefix) use the default store (tenant 0).
func TestHandler_NonTenantRouteUsesTenantZero(t *testing.T) {
	s := newTestServerWithMode(t, "path") // Explicit: non-tenant routes use tenant 0

	// Create via non-tenant route
	rr, body := doRequest(t, s, "POST", "/api/v1/global_items",
		map[string]interface{}{"scope": "global"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("non-tenant create: status %d, body: %s", rr.Code, rr.Body.String())
	}
	if body["id"] != float64(1) {
		t.Errorf("non-tenant ID = %v, want 1", body["id"])
	}

	// Create via tenant alpha route — should be independent
	rr, body = doRequest(t, s, "POST", "/api/v1/tenant/alpha/global_items",
		map[string]interface{}{"scope": "alpha"})
	if rr.Code != http.StatusCreated {
		t.Fatalf("alpha create: status %d, body: %s", rr.Code, rr.Body.String())
	}
	// Alpha also gets ID 1 (separate sequence)
	if body["id"] != float64(1) {
		t.Errorf("alpha ID = %v, want 1", body["id"])
	}

	// List from non-tenant route: sees only tenant 0 data
	_, listGlobal := doRequestList(t, s, "GET", "/api/v1/global_items")
	if len(listGlobal) != 1 {
		t.Errorf("non-tenant list = %d, want 1", len(listGlobal))
	}
	if len(listGlobal) == 1 {
		rec, _ := listGlobal[0].(map[string]interface{})
		if scope, _ := rec["scope"].(string); scope != "global" {
			t.Errorf("non-tenant item scope = %q, want 'global'", scope)
		}
	}

	// List from alpha: sees only alpha data
	_, listAlpha := doRequestList(t, s, "GET", "/api/v1/tenant/alpha/global_items")
	if len(listAlpha) != 1 {
		t.Errorf("alpha list = %d, want 1", len(listAlpha))
	}
}

// TestHandler_TenantFullTextSearchIsolation verifies that /search
// through tenant routes returns only that tenant's results.
func TestHandler_TenantFullTextSearchIsolation(t *testing.T) {
	s := newTestServer(t)

	doRequest(t, s, "POST", "/api/v1/tenant/alpha/articles",
		map[string]interface{}{"title": "quantum computing breakthrough"})
	doRequest(t, s, "POST", "/api/v1/tenant/beta/articles",
		map[string]interface{}{"title": "classical music review"})

	// Search alpha for "quantum" — should find 1
	rr, resultA := doRequest(t, s, "GET",
		"/api/v1/tenant/alpha/search?q=quantum&entity=articles", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("alpha FTS: status %d, body: %s", rr.Code, rr.Body.String())
	}
	dataA, _ := resultA["results"].([]interface{})
	if len(dataA) != 1 {
		t.Errorf("alpha FTS 'quantum' = %d results, want 1", len(dataA))
	}

	// Search beta for "quantum" — should find 0
	rr, resultB := doRequest(t, s, "GET",
		"/api/v1/tenant/beta/search?q=quantum&entity=articles", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("beta FTS: status %d, body: %s", rr.Code, rr.Body.String())
	}
	dataB, _ := resultB["results"].([]interface{})
	if len(dataB) != 0 {
		t.Errorf("beta FTS 'quantum' = %d results, want 0", len(dataB))
	}

	// Search beta for "classical" — should find 1
	rr, resultB2 := doRequest(t, s, "GET",
		"/api/v1/tenant/beta/search?q=classical&entity=articles", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("beta FTS classical: status %d", rr.Code)
	}
	dataB2, _ := resultB2["results"].([]interface{})
	if len(dataB2) != 1 {
		t.Errorf("beta FTS 'classical' = %d results, want 1", len(dataB2))
	}

	// Search alpha for "classical" — should find 0
	rr, resultA2 := doRequest(t, s, "GET",
		"/api/v1/tenant/alpha/search?q=classical&entity=articles", nil)
	if rr.Code != http.StatusOK {
		t.Fatalf("alpha FTS classical: status %d", rr.Code)
	}
	dataA2, _ := resultA2["results"].([]interface{})
	if len(dataA2) != 0 {
		t.Errorf("alpha FTS 'classical' = %d results, want 0", len(dataA2))
	}
}

// TestHandler_TenantRegistryIsolation verifies that the Registry
// correctly maps names to IDs and that different names produce
// different scoped stores.
func TestHandler_TenantRegistryIsolation(t *testing.T) {
	reg := tenant.NewRegistry()
	reg.Register(context.Background(), "acme", 10)
	reg.Register(context.Background(), "globex", 20)

	id1, ok1 := reg.Lookup("acme")
	id2, ok2 := reg.Lookup("globex")
	_, ok3 := reg.Lookup("unknown")

	if !ok1 || id1 != 10 {
		t.Errorf("acme lookup: id=%d ok=%v", id1, ok1)
	}
	if !ok2 || id2 != 20 {
		t.Errorf("globex lookup: id=%d ok=%v", id2, ok2)
	}
	if ok3 {
		t.Error("unknown tenant should not resolve")
	}
}
