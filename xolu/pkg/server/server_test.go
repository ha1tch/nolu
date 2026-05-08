// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"context"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// TestServer holds test server instance and helpers
type TestServer struct {
	server      *server.Server
	ts          *httptest.Server
	cfg         *config.Config
	t           *testing.T
	sqliteStore storage.Store // Optional, for SQLite-based tests
}

// setupTestServer creates a test server with temporary storage
func setupTestServer(t *testing.T) *TestServer {
	// Create temporary directory for test data
	tmpDir, err := os.MkdirTemp("", "olu-test-*")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Host:               "localhost",
		Port:               0, // Let httptest choose port
		BaseDir:            tmpDir,
		Schema:             "test_schema",
		SchemaDir:          filepath.Join(tmpDir, "test_schema"), // For OQL entity discovery
		CacheType:          "memory",
		CacheTTL:           300,
		GraphEnabled:       true,
		GraphMode:          "flat",
		FullTextEnabled:    false,
		CascadingDelete:    false,
		RefEmbedDepth:      3,
		MaxEmbedDepth:      10,
		MaxEntitySize:      1048576,
		PatchNullBehavior:  "store",
		GraphDataFile:      filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:     filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		TenantMode:          "path",
		TenantAutoRegister:  true, // Tests rely on auto-registration
	}

	// Initialize components
	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}

	store, err := storage.NewStore("jsonfile", storeConfig)
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	return &TestServer{
		server: srv,
		ts:     ts,
		cfg:    cfg,
		t:      t,
	}
}

// cleanup removes temporary test data
func (ts *TestServer) cleanup() {
	ts.ts.Close()
	if ts.sqliteStore != nil {
		ts.sqliteStore.Close()
	}
	os.RemoveAll(ts.cfg.BaseDir)
}

// doRequest makes HTTP request and returns response
func (ts *TestServer) doRequest(method, path string, body interface{}) (*http.Response, []byte) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = json.Marshal(body)
		if err != nil {
			ts.t.Fatal(err)
		}
	}

	req, err := http.NewRequest(method, ts.ts.URL+path, bytes.NewBuffer(bodyBytes))
	if err != nil {
		ts.t.Fatal(err)
	}

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		ts.t.Fatal(err)
	}
	defer resp.Body.Close()

	respBody := &bytes.Buffer{}
	respBody.ReadFrom(resp.Body)

	return resp, respBody.Bytes()
}

// TestHealthEndpoints tests health and version endpoints
func TestHealthEndpoints(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("GET /health", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/health", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}

		if result["status"] != "ok" {
			t.Errorf("Expected status ok, got %v", result["status"])
		}
	})

	t.Run("GET /ready", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/ready", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}

		if result["status"] != "ready" {
			t.Errorf("Expected status ready, got %v", result["status"])
		}
	})

	t.Run("GET /version", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/version", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}

		if result["version"] == nil {
			t.Error("Expected version field")
		}
	})
}

// TestCORSMiddleware tests CORS header behaviour
func TestCORSMiddleware(t *testing.T) {
	// Create a server with CORS enabled
	tmpDir, err := os.MkdirTemp("", "olu-cors-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Host:               "localhost",
		Port:               0,
		BaseDir:            tmpDir,
		Schema:             "test_schema",
		SchemaDir:          filepath.Join(tmpDir, "test_schema"),
		CacheType:          "memory",
		CacheTTL:           300,
		GraphEnabled:       true,
		GraphMode:          "flat",
		FullTextEnabled:    false,
		CascadingDelete:    false,
		RefEmbedDepth:      3,
		MaxEmbedDepth:      10,
		MaxEntitySize:      1048576,
		PatchNullBehavior:  "store",
		GraphDataFile:      filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:     filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		TenantMode:          "path",
		TenantAutoRegister:  true,
		CORSOrigins:         []string{"https://dashboard.example.com", "https://admin.example.com"},
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}
	store, err := storage.NewStore("jsonfile", storeConfig)
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	t.Run("allowed origin gets CORS headers", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
		req.Header.Set("Origin", "https://dashboard.example.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		acao := resp.Header.Get("Access-Control-Allow-Origin")
		if acao != "https://dashboard.example.com" {
			t.Errorf("Expected ACAO header 'https://dashboard.example.com', got %q", acao)
		}
	})

	t.Run("disallowed origin gets no CORS headers", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		acao := resp.Header.Get("Access-Control-Allow-Origin")
		if acao != "" {
			t.Errorf("Expected no ACAO header for disallowed origin, got %q", acao)
		}
	})

	t.Run("preflight OPTIONS returns 204", func(t *testing.T) {
		req, _ := http.NewRequest("OPTIONS", ts.URL+"/api/v1/test", nil)
		req.Header.Set("Origin", "https://dashboard.example.com")
		req.Header.Set("Access-Control-Request-Method", "POST")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusNoContent {
			t.Errorf("Expected 204 for preflight, got %d", resp.StatusCode)
		}

		methods := resp.Header.Get("Access-Control-Allow-Methods")
		if methods == "" {
			t.Error("Expected Access-Control-Allow-Methods header")
		}
	})

	t.Run("no origin header skips CORS", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.URL+"/health", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()

		acao := resp.Header.Get("Access-Control-Allow-Origin")
		if acao != "" {
			t.Errorf("Expected no ACAO header when no Origin sent, got %q", acao)
		}
	})
}

// TestEntityCRUD tests complete entity CRUD operations
func TestEntityCRUD(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	entity := "users"
	var createdID float64

	t.Run("POST /api/v1/{entity} - Create", func(t *testing.T) {
		data := map[string]interface{}{
			"name":  "Alice Smith",
			"email": "alice@example.com",
			"age":   30,
		}

		resp, body := ts.doRequest("POST", "/api/v1/"+entity, data)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("Expected 201, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}

		if result["id"] == nil {
			t.Fatal("Expected id in response")
		}
		createdID = result["id"].(float64)
	})

	t.Run("GET /api/v1/{entity}/{id} - Get", func(t *testing.T) {
		resp, body := ts.doRequest("GET", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}

		if result["name"] != "Alice Smith" {
			t.Errorf("Expected name 'Alice Smith', got %v", result["name"])
		}
		if result["email"] != "alice@example.com" {
			t.Errorf("Expected email 'alice@example.com', got %v", result["email"])
		}
	})

	t.Run("PUT /api/v1/{entity}/{id} - Update", func(t *testing.T) {
		data := map[string]interface{}{
			"name":  "Alice Johnson",
			"email": "alice.johnson@example.com",
			"age":   31,
		}

		resp, body := ts.doRequest("PUT", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), data)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		// Verify update
		_, body = ts.doRequest("GET", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), nil)
		var result map[string]interface{}
		_ = json.Unmarshal(body, &result)

		if result["name"] != "Alice Johnson" {
			t.Errorf("Expected updated name, got %v", result["name"])
		}
	})

	t.Run("PATCH /api/v1/{entity}/{id} - Patch", func(t *testing.T) {
		data := map[string]interface{}{
			"age": 32,
		}

		resp, body := ts.doRequest("PATCH", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), data)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		// Verify patch
		_, body = ts.doRequest("GET", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), nil)
		var result map[string]interface{}
		_ = json.Unmarshal(body, &result)

		if result["age"].(float64) != 32 {
			t.Errorf("Expected age 32, got %v", result["age"])
		}
		// Name should still be from update
		if result["name"] != "Alice Johnson" {
			t.Errorf("Expected name unchanged, got %v", result["name"])
		}
	})

	t.Run("GET /api/v1/{entity} - List", func(t *testing.T) {
		// Create another entity
		data := map[string]interface{}{
			"name":  "Bob Smith",
			"email": "bob@example.com",
			"age":   25,
		}
		ts.doRequest("POST", "/api/v1/"+entity, data)

		resp, body := ts.doRequest("GET", "/api/v1/"+entity, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		if err := json.Unmarshal(body, &result); err != nil {
			t.Fatal(err)
		}

		dataArray, ok := result["data"].([]interface{})
		if !ok {
			t.Fatal("Expected data array")
		}

		if len(dataArray) < 2 {
			t.Errorf("Expected at least 2 entities, got %d", len(dataArray))
		}
	})

	t.Run("DELETE /api/v1/{entity}/{id} - Delete", func(t *testing.T) {
		resp, body := ts.doRequest("DELETE", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		// Verify deletion
		resp, _ = ts.doRequest("GET", fmt.Sprintf("/api/v1/%s/%d", entity, int(createdID)), nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404 after delete, got %d", resp.StatusCode)
		}
	})
}

// TestEntitySave tests save with specific ID
func TestEntitySave(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("POST /api/v1/{entity}/save/{id}", func(t *testing.T) {
		data := map[string]interface{}{
			"name":  "Charlie",
			"email": "charlie@example.com",
		}

		resp, body := ts.doRequest("POST", "/api/v1/users/save/100", data)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("Expected 201 on first save, got %d: %s", resp.StatusCode, string(body))
		}

		// Verify entity exists with ID 100
		resp, body = ts.doRequest("GET", "/api/v1/users/100", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)
		if result["id"].(float64) != 100 {
			t.Errorf("Expected id 100, got %v", result["id"])
		}
	})

	t.Run("POST /api/v1/{entity}/save/{id} - Overwrite", func(t *testing.T) {
		// Second save to the same ID should overwrite (upsert), not conflict.
		data := map[string]interface{}{
			"name":  "Charlie Updated",
			"email": "charlie-updated@example.com",
		}

		resp, body := ts.doRequest("POST", "/api/v1/users/save/100", data)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 on overwrite, got %d: %s", resp.StatusCode, string(body))
		}

		// Verify the data was actually replaced.
		resp, body = ts.doRequest("GET", "/api/v1/users/100", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 on GET after overwrite, got %d", resp.StatusCode)
		}
		var result map[string]interface{}
		json.Unmarshal(body, &result)
		if result["name"] != "Charlie Updated" {
			t.Errorf("Expected overwritten name 'Charlie Updated', got %v", result["name"])
		}
	})
}

// TestEntityReferences tests entity references and graph updates
func TestEntityReferences(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	var managerID, employeeID float64

	t.Run("Create entities with references", func(t *testing.T) {
		// Create manager
		manager := map[string]interface{}{
			"name": "Manager Bob",
			"role": "manager",
		}
		resp, body := ts.doRequest("POST", "/api/v1/users", manager)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Failed to create manager: %s", string(body))
		}
		var result map[string]interface{}
		json.Unmarshal(body, &result)
		managerID = result["id"].(float64)

		// Create employee with reference to manager
		employee := map[string]interface{}{
			"name": "Employee Alice",
			"role": "employee",
			"manager": map[string]interface{}{
				"type":   "REF",
				"entity": "users",
				"id":     managerID,
			},
		}
		resp, body = ts.doRequest("POST", "/api/v1/users", employee)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("Failed to create employee: %s", string(body))
		}
		json.Unmarshal(body, &result)
		employeeID = result["id"].(float64)
	})

	t.Run("Get with embedded references", func(t *testing.T) {
		resp, body := ts.doRequest("GET", fmt.Sprintf("/api/v1/users/%d?embed_depth=1", int(employeeID)), nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Failed to get employee: %s", string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		manager, ok := result["manager"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected manager to be embedded")
		}

		if manager["name"] != "Manager Bob" {
			t.Errorf("Expected embedded manager name, got %v", manager["name"])
		}
	})
}

// TestGraphOperations tests graph endpoints
func TestGraphOperations(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create test data with relationships
	var user1ID, user2ID, user3ID float64

	// Setup: Create users with relationships
	user1 := map[string]interface{}{"name": "User1"}
	_, body := ts.doRequest("POST", "/api/v1/users", user1)
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	user1ID = result["id"].(float64)

	user2 := map[string]interface{}{
		"name": "User2",
		"friend": map[string]interface{}{
			"type": "REF", "entity": "users", "id": user1ID,
		},
	}
	_, body = ts.doRequest("POST", "/api/v1/users", user2)
	json.Unmarshal(body, &result)
	user2ID = result["id"].(float64)

	user3 := map[string]interface{}{
		"name": "User3",
		"friend": map[string]interface{}{
			"type": "REF", "entity": "users", "id": user2ID,
		},
	}
	_, body = ts.doRequest("POST", "/api/v1/users", user3)
	json.Unmarshal(body, &result)
	user3ID = result["id"].(float64)

	t.Run("GET /api/v1/graph/stats", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/graph/stats", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		if result["node_count"] == nil {
			t.Error("Expected node_count in response")
		}
		if result["edge_count"] == nil {
			t.Error("Expected edge_count in response")
		}
	})

	t.Run("POST /api/v1/graph/path", func(t *testing.T) {
		data := map[string]interface{}{
			"from":      fmt.Sprintf("users:%d", int(user3ID)),
			"to":        fmt.Sprintf("users:%d", int(user1ID)),
			"max_depth": 10,
		}

		resp, body := ts.doRequest("POST", "/api/v1/graph/path", data)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		path, ok := result["path"].([]interface{})
		if !ok {
			t.Fatal("Expected path in response")
		}

		if len(path) < 2 {
			t.Errorf("Expected path with at least 2 nodes, got %d", len(path))
		}
	})

	t.Run("POST /api/v1/graph/neighbors", func(t *testing.T) {
		data := map[string]interface{}{
			"node_id": fmt.Sprintf("users:%d", int(user2ID)),
		}

		resp, body := ts.doRequest("POST", "/api/v1/graph/neighbors", data)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		neighbors, ok := result["neighbors"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected neighbors in response")
		}

		if len(neighbors) == 0 {
			t.Error("Expected at least one neighbor")
		}
	})
}

// TestSchemaOperations tests schema endpoints
func TestSchemaOperations(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	entity := "products"

	t.Run("POST /api/v1/schema/{entity}", func(t *testing.T) {
		schema := map[string]interface{}{
			"type": "object",
			"required": []string{"name", "price"},
			"properties": map[string]interface{}{
				"name": map[string]interface{}{
					"type": "string",
				},
				"price": map[string]interface{}{
					"type": "number",
				},
			},
		}

		resp, body := ts.doRequest("POST", "/api/v1/schema/"+entity, schema)
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("Expected 201, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("GET /api/v1/schema/{entity}", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/schema/"+entity, nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		if result["type"] != "object" {
			t.Error("Expected schema type object")
		}
	})
}

// TestPagination tests list pagination
func TestPagination(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create 10 test entities
	for i := 0; i < 10; i++ {
		data := map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
			"age":  20 + i,
		}
		ts.doRequest("POST", "/api/v1/users", data)
	}

	t.Run("Pagination parameters", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/users?page=1&per_page=5", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		pagination, ok := result["pagination"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected pagination in response")
		}

		if pagination["page"].(float64) != 1 {
			t.Errorf("Expected page 1, got %v", pagination["page"])
		}
		if pagination["per_page"].(float64) != 5 {
			t.Errorf("Expected per_page 5, got %v", pagination["per_page"])
		}

		data, ok := result["data"].([]interface{})
		if !ok {
			t.Fatal("Expected data array")
		}
		if len(data) != 5 {
			t.Errorf("Expected 5 items, got %d", len(data))
		}
	})
}

// TestErrorHandling tests error responses
func TestErrorHandling(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("GET non-existent entity", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/users/99999", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("Invalid ID format", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/users/invalid", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("Invalid JSON body", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/users", bytes.NewBufferString("invalid json"))
		req.Header.Set("Content-Type", "application/json")
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("request failed: %v", doErr)
		}
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected 400, got %d", resp.StatusCode)
		}
	})
}

// TestErrorResponseEnvelope verifies that API error responses use the
// structured envelope: {"error": {"code": "OLU-...", "message": "...", "status": N}}.
// This catches regressions if someone changes writeError or adds a new
// error path that uses the old flat format.
func TestErrorResponseEnvelope(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Trigger a writeError response — invalid ID is the simplest case.
	resp, body := ts.doRequest("GET", "/api/v1/users/invalid", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("Expected 400, got %d", resp.StatusCode)
	}

	var envelope map[string]interface{}
	if err := json.Unmarshal(body, &envelope); err != nil {
		t.Fatalf("Response is not valid JSON: %v", err)
	}

	errObj, ok := envelope["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("Expected nested 'error' object, got %T: %v", envelope["error"], envelope["error"])
	}

	// Verify all three required fields exist
	code, ok := errObj["code"].(string)
	if !ok || code == "" {
		t.Errorf("error.code missing or empty: %v", errObj["code"])
	}
	msg, ok := errObj["message"].(string)
	if !ok || msg == "" {
		t.Errorf("error.message missing or empty: %v", errObj["message"])
	}
	status, ok := errObj["status"].(float64) // JSON numbers are float64
	if !ok || status != 400 {
		t.Errorf("error.status expected 400, got %v", errObj["status"])
	}
}

// ============================================================================
// Full-Text Search Endpoint Tests
// ============================================================================

// setupTestServerWithFTS creates a test server with SQLite and FTS enabled
func setupTestServerWithFTS(t *testing.T) *TestServer {
	tmpDir, err := os.MkdirTemp("", "olu-fts-test-*")
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &config.Config{
		Host:               "localhost",
		Port:               0,
		StorageType:        "sqlite",
		DBPath:             dbPath,
		BaseDir:            tmpDir,
		Schema:             "test_schema",
		CacheType:          "memory",
		CacheTTL:           300,
		GraphEnabled:       true,
		GraphMode:          "flat",
		FullTextEnabled:    true,
		CascadingDelete:    false,
		RefEmbedDepth:      3,
		MaxEmbedDepth:      10,
		MaxEntitySize:      1048576,
		PatchNullBehavior:  "store",
		GraphDataFile:      filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:     filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		TenantMode:          "path",
		TenantAutoRegister:  true, // Tests rely on auto-registration
	}

	storeConfig := map[string]interface{}{
		"db_path":           dbPath,
		"full_text_enabled": true,
	}

	store, err := storage.NewStore("sqlite", storeConfig)
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	httpServer := httptest.NewServer(srv.Handler())

	// Store the store reference for cleanup
	testServer := &TestServer{
		server: srv,
		ts:     httpServer,
		cfg:    cfg,
		t:      t,
	}
	// Override cleanup to also close SQLite
	testServer.sqliteStore = store
	return testServer
}

func TestFullTextSearchEndpoint(t *testing.T) {
	ts := setupTestServerWithFTS(t)
	defer ts.cleanup()

	// Create test data
	ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name":  "Alice Engineer",
		"email": "alice@example.com",
		"bio":   "Software developer who loves Go programming",
	})
	ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name":  "Bob Manager",
		"email": "bob@example.com",
		"bio":   "Product manager with technical background",
	})
	ts.doRequest("POST", "/api/v1/posts", map[string]interface{}{
		"title":   "Go Programming Tips",
		"content": "Learn Go programming effectively",
	})

	t.Run("Search without query returns error", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/search", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected 400 for missing query, got %d", resp.StatusCode)
		}
	})

	t.Run("Search across all entities", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/search?q=programming", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d. Body: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		countVal, ok := result["count"].(float64)
		if !ok {
			t.Fatalf("Expected count in response, got: %v", result)
		}
		count := int(countVal)
		if count != 2 {
			t.Errorf("Expected 2 results for 'programming', got %d", count)
		}
	})

	t.Run("Search within entity type", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/search?q=programming&entity=users", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d. Body: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		countVal, ok := result["count"].(float64)
		if !ok {
			t.Fatalf("Expected count in response, got: %v", result)
		}
		count := int(countVal)
		if count != 1 {
			t.Errorf("Expected 1 result for 'programming' in users, got %d", count)
		}
	})

	t.Run("Search with no matches", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/search?q=xyznonexistent", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d. Body: %s", resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		countVal, ok := result["count"].(float64)
		if !ok {
			t.Fatalf("Expected count in response, got: %v", result)
		}
		count := int(countVal)
		if count != 0 {
			t.Errorf("Expected 0 results for nonexistent term, got %d", count)
		}
	})
}

func TestFullTextSearchDisabled(t *testing.T) {
	// Use regular setup (FTS disabled)
	ts := setupTestServer(t)
	defer ts.cleanup()

	resp, _ := ts.doRequest("GET", "/api/v1/search?q=test", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("Expected 503 when FTS disabled, got %d", resp.StatusCode)
	}
}

// ============================================================================
// REF Embed Depth Tests
// ============================================================================

func TestRefEmbedDepth(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create a chain of references: user1 -> user2 -> user3
	_, body1 := ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name": "User 3 (deepest)",
	})
	var u3 map[string]interface{}
	json.Unmarshal(body1, &u3)
	id3 := int(u3["id"].(float64))

	_, body2 := ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name": "User 2 (middle)",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     id3,
		},
	})
	var u2 map[string]interface{}
	json.Unmarshal(body2, &u2)
	id2 := int(u2["id"].(float64))

	ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name": "User 1 (top)",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     id2,
		},
	})

	t.Run("Default embedding resolves refs", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/users/3", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		// Manager should be embedded (not a REF object)
		manager, ok := result["manager"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected manager to be embedded object")
		}
		if manager["type"] == "REF" {
			t.Error("Expected manager to be resolved, not REF")
		}
		if manager["name"] != "User 2 (middle)" {
			t.Errorf("Expected manager name 'User 2 (middle)', got %v", manager["name"])
		}
	})

	t.Run("embed=false disables embedding", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/users/3?embed=false", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		// Manager should be a REF object
		manager, ok := result["manager"].(map[string]interface{})
		if !ok {
			t.Fatal("Expected manager field")
		}
		if manager["type"] != "REF" {
			t.Error("Expected manager to be REF when embed=false")
		}
	})

	t.Run("embed_depth=1 limits depth", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/users/3?embed_depth=1", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		// First level should be embedded
		manager, ok := result["manager"].(map[string]interface{})
		if !ok || manager["type"] == "REF" {
			t.Error("Expected first level manager to be embedded")
		}

		// Second level should still be REF (depth exhausted)
		nestedManager, ok := manager["manager"].(map[string]interface{})
		if ok && nestedManager["type"] != "REF" {
			t.Error("Expected nested manager to remain as REF at depth 1")
		}
	})
}

// ============================================================================
// OQL Endpoint Tests
// ============================================================================

func TestOQLQueryEndpoint(t *testing.T) {
	// Create temp directory with pre-existing entity folders
	// so OQL validator recognizes them at startup
	tmpDir, err := os.MkdirTemp("", "olu-oql-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Pre-create entity directories so OQL validator finds them
	usersDir := filepath.Join(tmpDir, "test_schema", "users")
	if err := os.MkdirAll(usersDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:        "jsonfile",
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        true,
		GraphMode:           "flat",
		FullTextEnabled:     false,
		RefEmbedDepth:       3,
		MaxEmbedDepth:       10,
		MaxEntitySize:       1048576,
		PatchNullBehavior:   "store",
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}
	store, _ := storage.NewStore("jsonfile", storeConfig)
	memCache := cache.NewMemoryCache(1000, time.Second*300)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doReq := func(method, path string, body interface{}) (*http.Response, []byte) {
		var bodyBytes []byte
		if body != nil {
			bodyBytes, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBuffer(bodyBytes))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("request failed: %v", doErr)
		}
		defer resp.Body.Close()
		respBody := &bytes.Buffer{}
		respBody.ReadFrom(resp.Body)
		return resp, respBody.Bytes()
	}

	// Create test data
	doReq("POST", "/api/v1/users", map[string]interface{}{"name": "Alice", "age": 30})
	doReq("POST", "/api/v1/users", map[string]interface{}{"name": "Bob", "age": 25})
	doReq("POST", "/api/v1/users", map[string]interface{}{"name": "Carol", "age": 35})

	t.Run("Basic SELECT query", func(t *testing.T) {
		resp, body := doReq("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "SELECT * FROM users",
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d. Body: %s", resp.StatusCode, string(body))
			return
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		data, ok := result["data"].([]interface{})
		if !ok {
			t.Fatalf("Expected data array, got: %v", result)
		}
		if len(data) != 3 {
			t.Errorf("Expected 3 results, got %d", len(data))
		}
	})

	t.Run("SELECT with WHERE", func(t *testing.T) {
		resp, body := doReq("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "SELECT * FROM users WHERE age > 28",
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
			return
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		data, ok := result["data"].([]interface{})
		if !ok {
			t.Fatalf("Expected data array")
		}
		if len(data) != 2 {
			t.Errorf("Expected 2 results (age > 28), got %d", len(data))
		}
	})

	t.Run("SELECT with ORDER BY", func(t *testing.T) {
		resp, body := doReq("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "SELECT name FROM users ORDER BY age DESC",
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
			return
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		data, ok := result["data"].([]interface{})
		if !ok || len(data) == 0 {
			t.Fatalf("Expected non-empty data array")
		}
		first := data[0].(map[string]interface{})
		if first["name"] != "Carol" {
			t.Errorf("Expected Carol first (oldest), got %v", first["name"])
		}
	})

	t.Run("SELECT with LIMIT", func(t *testing.T) {
		resp, body := doReq("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "SELECT TOP 2 * FROM users",
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200, got %d", resp.StatusCode)
			return
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		data, ok := result["data"].([]interface{})
		if !ok {
			t.Fatalf("Expected data array")
		}
		if len(data) != 2 {
			t.Errorf("Expected 2 results with TOP 2, got %d", len(data))
		}
	})

	t.Run("Invalid query", func(t *testing.T) {
		resp, _ := doReq("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "INVALID QUERY SYNTAX",
		})
		if resp.StatusCode == http.StatusOK {
			t.Error("Expected error for invalid query")
		}
	})

	t.Run("Empty query", func(t *testing.T) {
		resp, _ := doReq("POST", "/api/v1/oql/query", map[string]interface{}{
			"query": "",
		})
		if resp.StatusCode == http.StatusOK {
			t.Error("Expected error for empty query")
		}
	})
}

func TestOQLAsyncEndpoint(t *testing.T) {
	// Create temp directory with pre-existing entity folders
	tmpDir, err := os.MkdirTemp("", "olu-oql-async-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Pre-create entity directory
	usersDir := filepath.Join(tmpDir, "test_schema", "users")
	if err := os.MkdirAll(usersDir, 0755); err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:        "jsonfile",
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        true,
		GraphMode:           "flat",
		RefEmbedDepth:       3,
		MaxEmbedDepth:       10,
		MaxEntitySize:       1048576,
		PatchNullBehavior:   "store",
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		GraphQueryTTL:       3600,
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}
	store, _ := storage.NewStore("jsonfile", storeConfig)
	memCache := cache.NewMemoryCache(1000, time.Second*300)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doReq := func(method, path string, body interface{}) (*http.Response, []byte) {
		var bodyBytes []byte
		if body != nil {
			bodyBytes, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBuffer(bodyBytes))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("request failed: %v", doErr)
		}
		defer resp.Body.Close()
		respBody := &bytes.Buffer{}
		respBody.ReadFrom(resp.Body)
		return resp, respBody.Bytes()
	}

	// Create test data
	doReq("POST", "/api/v1/users", map[string]interface{}{"name": "Test"})

	t.Run("Submit async query", func(t *testing.T) {
		resp, body := doReq("POST", "/api/v1/oql/query/async", map[string]interface{}{
			"query": "SELECT * FROM users",
		})
		if resp.StatusCode != http.StatusAccepted {
			t.Errorf("Expected 202, got %d. Body: %s", resp.StatusCode, string(body))
			return
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		queryID, ok := result["query_id"].(string)
		if !ok || queryID == "" {
			t.Errorf("Expected query_id in response, got: %v", result)
		}
	})
}

// ============================================================================
// Sulpher Endpoint Tests
// ============================================================================

func TestSulpherQueryEndpoint(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create connected entities
	ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name": "Alice",
	})
	ts.doRequest("POST", "/api/v1/posts", map[string]interface{}{
		"title": "Hello",
		"author": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     1,
		},
	})

	t.Run("Basic path query", func(t *testing.T) {
		resp, body := ts.doRequest("POST", "/api/v1/graph/query", map[string]interface{}{
			"query": "MATCH (u:users) RETURN u",
		})
		// Sulpher queries may return 200 even with no results
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusAccepted {
			t.Errorf("Expected 200 or 202, got %d. Body: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("Invalid query", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/query", map[string]interface{}{
			"query": "",
		})
		if resp.StatusCode == http.StatusOK {
			t.Error("Expected error for empty query")
		}
	})
}

// ============================================================================
// Multi-Tenant Tests
// ============================================================================

func TestTenantIsolation(t *testing.T) {
	// Tenant isolation on point operations (Get/Put/Delete by ID) requires
	// a storage backend that scopes by tenant_id. The jsonfile backend does
	// not support this; use SQLite.
	tmpDir, err := os.MkdirTemp("", "olu-tenant-iso-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	dbPath := filepath.Join(tmpDir, "tenant_iso.db")
	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:        "sqlite",
		DBPath:              dbPath,
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        true,
		GraphMode:           "flat",
		FullTextEnabled:     false,
		CascadingDelete:     false,
		RefEmbedDepth:       3,
		MaxEmbedDepth:       10,
		MaxEntitySize:       1048576,
		PatchNullBehavior:   "store",
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		TenantMode:          "path",
		TenantAutoRegister:  true, // Tests rely on auto-registration
	}

	sqliteStore, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:   "sqlite",
		DBPath: dbPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer sqliteStore.Close()

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, sqliteStore, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doReq := func(method, path string, body interface{}) (*http.Response, []byte) {
		var bodyBytes []byte
		if body != nil {
			bodyBytes, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBuffer(bodyBytes))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("request failed: %v", doErr)
		}
		defer resp.Body.Close()
		respBody := &bytes.Buffer{}
		respBody.ReadFrom(resp.Body)
		return resp, respBody.Bytes()
	}

	// Create entities in tenant1
	doReq("POST", "/api/v1/tenant/tenant1/users", map[string]interface{}{
		"name": "Alice",
	})
	doReq("POST", "/api/v1/tenant/tenant1/users", map[string]interface{}{
		"name": "Bob",
	})

	// Create entity in tenant2
	doReq("POST", "/api/v1/tenant/tenant2/users", map[string]interface{}{
		"name": "Carol",
	})

	t.Run("List only shows tenant's data", func(t *testing.T) {
		resp, body := doReq("GET", "/api/v1/tenant/tenant1/users", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("Expected 200, got %d", resp.StatusCode)
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)

		data := result["data"].([]interface{})
		if len(data) != 2 {
			t.Errorf("Expected 2 users in tenant1, got %d", len(data))
		}
	})

	t.Run("Get from wrong tenant returns 404", func(t *testing.T) {
		// Tenant1 has id=1 (Alice) and id=2 (Bob). Tenant2 has id=1 (Carol).
		// Querying tenant2 for id=2 should return 404 — that ID only exists in tenant1.
		resp, body := doReq("GET", "/api/v1/tenant/tenant2/users/2", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404 for cross-tenant access, got %d; body: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("Update in wrong tenant fails", func(t *testing.T) {
		resp, _ := doReq("PUT", "/api/v1/tenant/tenant2/users/2", map[string]interface{}{
			"name": "Hacked",
		})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404 for cross-tenant update, got %d", resp.StatusCode)
		}
	})

	t.Run("Delete in wrong tenant fails", func(t *testing.T) {
		resp, _ := doReq("DELETE", "/api/v1/tenant/tenant2/users/2", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("Expected 404 for cross-tenant delete, got %d", resp.StatusCode)
		}
	})
}

// ============================================================================
// Additional Server Tests
// ============================================================================

func TestMetricsEndpoint(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("Prometheus format", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.ts.URL+"/metrics", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		// Metrics may be disabled in test config
		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("Expected 200 or 503, got %d", resp.StatusCode)
		}
	})

	t.Run("JSON format", func(t *testing.T) {
		req, _ := http.NewRequest("GET", ts.ts.URL+"/metrics", nil)
		req.Header.Set("Accept", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("Request failed: %v", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("Expected 200 or 503, got %d", resp.StatusCode)
		}
	})
}

func TestExportEndpoint(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create some data
	ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name": "Alice",
	})

	resp, body := ts.doRequest("GET", "/api/v1/export", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("Expected 200, got %d. Body: %s", resp.StatusCode, string(body))
	}

	// Check content type is zip
	contentType := resp.Header.Get("Content-Type")
	if contentType != "application/zip" {
		t.Errorf("Expected Content-Type application/zip, got %s", contentType)
	}
}

func TestSearchEndpoint(t *testing.T) {
	// Full-text search requires SQLite with FTS enabled
	// These tests verify the endpoint behavior with FTS disabled
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create test data
	ts.doRequest("POST", "/api/v1/users", map[string]interface{}{
		"name":  "Alice Smith",
		"email": "alice@example.com",
	})

	t.Run("Full-text search disabled returns 503", func(t *testing.T) {
		resp, body := ts.doRequest("GET", "/api/v1/search?q=Alice", nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("Expected 503 (FTS disabled), got %d. Body: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("Full-text search missing query returns 400", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/search", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("Expected 400 for missing query, got %d", resp.StatusCode)
		}
	})
}

// ============================================================================
// Tenant Strict Mode Tests
// ============================================================================

func TestTenantStrictMode(t *testing.T) {
	// Create temp directory
	tmpDir, err := os.MkdirTemp("", "olu-tenant-strict-test-*")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:        "jsonfile",
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        true,
		GraphMode:           "flat",
		RefEmbedDepth:       3,
		MaxEmbedDepth:       10,
		MaxEntitySize:       1048576,
		PatchNullBehavior:   "store",
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		TenantMode:          "strict", // Explicit: tenants must be pre-registered
		AuthType:            "none",
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}
	store, _ := storage.NewStore("jsonfile", storeConfig)
	memCache := cache.NewMemoryCache(1000, time.Second*300)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)

	// In strict mode, tenants must be pre-registered before use.
	srv.TenantRegistry().Register(context.Background(), "acme", 1)

	ts := httptest.NewServer(srv.Handler())
	defer ts.Close()

	doReq := func(method, path string, body interface{}) (*http.Response, []byte) {
		var bodyBytes []byte
		if body != nil {
			bodyBytes, _ = json.Marshal(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, bytes.NewBuffer(bodyBytes))
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, doErr := http.DefaultClient.Do(req)
		if doErr != nil {
			t.Fatalf("request failed: %v", doErr)
		}
		defer resp.Body.Close()
		respBody := &bytes.Buffer{}
		respBody.ReadFrom(resp.Body)
		return resp, respBody.Bytes()
	}

	t.Run("Non-tenant entity route blocked", func(t *testing.T) {
		resp, _ := doReq("POST", "/api/v1/users", map[string]interface{}{
			"name": "Alice",
		})
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected 403 for non-tenant route in strict mode, got %d", resp.StatusCode)
		}
	})

	t.Run("Tenant route allowed", func(t *testing.T) {
		resp, _ := doReq("POST", "/api/v1/tenant/acme/users", map[string]interface{}{
			"name": "Alice",
		})
		if resp.StatusCode != http.StatusCreated {
			t.Errorf("Expected 201 for tenant route, got %d", resp.StatusCode)
		}
	})

	t.Run("Health endpoint allowed", func(t *testing.T) {
		resp, _ := doReq("GET", "/health", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 for health endpoint, got %d", resp.StatusCode)
		}
	})

	t.Run("Version endpoint allowed", func(t *testing.T) {
		resp, _ := doReq("GET", "/version", nil)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("Expected 200 for version endpoint, got %d", resp.StatusCode)
		}
	})

	t.Run("Graph routes blocked in strict mode", func(t *testing.T) {
		resp, _ := doReq("GET", "/api/v1/graph/stats", nil)
		// Graph is automatically disabled in strict mode (not tenant-isolated).
		// The middleware blocks all non-tenant, non-schema /api/v1/ routes.
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("Expected 403 for graph stats in strict mode, got %d", resp.StatusCode)
		}
	})
}
