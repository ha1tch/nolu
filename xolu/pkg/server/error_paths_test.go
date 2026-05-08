// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// error_paths_test.go
//
// Systematic tests for handler error branches. Each test targets a specific
// writeError() call path in handlers.go or server.go to ensure error
// responses are correct and don't panic.
//
// Author: ha1tch <h@ual.fi>

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// =============================================================================
// CRUD Error Paths
// =============================================================================

func TestErrorPaths_InvalidEntityName(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	badNames := []struct {
		name string
		path string
	}{
		{"starts with number", "/api/v1/123invalid"},
		{"has special chars", "/api/v1/my-entity"},
		{"has dots", "/api/v1/my.entity"},
		{"has spaces (encoded)", "/api/v1/my%20entity"},
	}

	methods := []string{"GET", "POST"}
	for _, bad := range badNames {
		for _, method := range methods {
			t.Run(fmt.Sprintf("%s_%s", method, bad.name), func(t *testing.T) {
				var body interface{}
				if method == "POST" {
					body = map[string]interface{}{"name": "test"}
				}
				resp, _ := ts.doRequest(method, bad.path, body)
				if resp.StatusCode != http.StatusBadRequest {
					t.Errorf("%s %s: expected 400, got %d", method, bad.path, resp.StatusCode)
				}
			})
		}
	}
}

func TestErrorPaths_InvalidID(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create an entity first
	ts.doRequest("POST", "/api/v1/items", map[string]interface{}{"name": "Widget"})

	badIDs := []string{"abc", "-1", "1.5", "999999999999999999999"}

	for _, id := range badIDs {
		t.Run("GET_"+id, func(t *testing.T) {
			resp, _ := ts.doRequest("GET", "/api/v1/items/"+id, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("GET items/%s: expected 400, got %d", id, resp.StatusCode)
			}
		})

		t.Run("PUT_"+id, func(t *testing.T) {
			resp, _ := ts.doRequest("PUT", "/api/v1/items/"+id, map[string]interface{}{"name": "x"})
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("PUT items/%s: expected 400, got %d", id, resp.StatusCode)
			}
		})

		t.Run("PATCH_"+id, func(t *testing.T) {
			resp, _ := ts.doRequest("PATCH", "/api/v1/items/"+id, map[string]interface{}{"name": "x"})
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("PATCH items/%s: expected 400, got %d", id, resp.StatusCode)
			}
		})

		t.Run("DELETE_"+id, func(t *testing.T) {
			resp, _ := ts.doRequest("DELETE", "/api/v1/items/"+id, nil)
			if resp.StatusCode != http.StatusBadRequest {
				t.Errorf("DELETE items/%s: expected 400, got %d", id, resp.StatusCode)
			}
		})
	}
}

func TestErrorPaths_NotFound(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("GET nonexistent", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/items/99999", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("PUT nonexistent", func(t *testing.T) {
		resp, _ := ts.doRequest("PUT", "/api/v1/items/99999",
			map[string]interface{}{"name": "ghost"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("PATCH nonexistent", func(t *testing.T) {
		resp, _ := ts.doRequest("PATCH", "/api/v1/items/99999",
			map[string]interface{}{"name": "ghost"})
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("DELETE nonexistent", func(t *testing.T) {
		resp, _ := ts.doRequest("DELETE", "/api/v1/items/99999", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})
}

func TestErrorPaths_InvalidJSON(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create one entity so PATCH/PUT have a valid target
	ts.doRequest("POST", "/api/v1/items", map[string]interface{}{"name": "existing"})

	malformed := `{"name": "broken`

	t.Run("POST malformed", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/items",
			strings.NewReader(malformed))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("PATCH malformed", func(t *testing.T) {
		req, _ := http.NewRequest("PATCH", ts.ts.URL+"/api/v1/items/1",
			strings.NewReader(malformed))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("PUT malformed", func(t *testing.T) {
		req, _ := http.NewRequest("PUT", ts.ts.URL+"/api/v1/items/1",
			strings.NewReader(malformed))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})
}

func TestErrorPaths_SaveUpsert(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Create entity at ID 1
	ts.doRequest("POST", "/api/v1/items", map[string]interface{}{"name": "original"})

	// Save at same ID should overwrite, not conflict
	t.Run("save existing id", func(t *testing.T) {
		resp, body := ts.doRequest("POST", "/api/v1/items/save/1",
			map[string]interface{}{"name": "overwritten"})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200 on upsert overwrite, got %d: %s", resp.StatusCode, string(body))
		}
	})

	t.Run("save invalid id", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/items/save/abc",
			map[string]interface{}{"name": "bad"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("save malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/items/save/999",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// Graph Error Paths (disabled mode)
// =============================================================================

func TestErrorPaths_GraphDisabled(t *testing.T) {
	ts := setupTestServerNoGraph(t)
	defer ts.cleanup()

	// When graph is disabled, routes are not registered at all.
	// GET on unregistered paths that match /{entity} pattern may hit CRUD handlers.
	// POST on unregistered paths return 405 (Method Not Allowed).
	// The key guarantee: graph operations are NOT available.

	graphEndpoints := []struct {
		method string
		path   string
		body   interface{}
	}{
		{"POST", "/api/v1/graph/shortestPath", map[string]interface{}{"from": "a:1", "to": "b:1"}},
		{"POST", "/api/v1/graph/pathExists", map[string]interface{}{"from": "a:1", "to": "b:1"}},
		{"POST", "/api/v1/graph/commonNeighbors", map[string]interface{}{"node_a": "a:1", "node_b": "b:1"}},
		{"POST", "/api/v1/graph/path", map[string]interface{}{"from": "a:1", "to": "b:1"}},
		{"POST", "/api/v1/graph/neighbors", map[string]interface{}{"node_id": "a:1"}},
	}

	for _, ep := range graphEndpoints {
		name := fmt.Sprintf("%s_%s", ep.method, ep.path)
		t.Run(name, func(t *testing.T) {
			resp, _ := ts.doRequest(ep.method, ep.path, ep.body)
			// Should NOT return 200/201 — graph ops must not succeed
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusCreated {
				t.Errorf("%s %s: graph operation should not succeed when disabled, got %d",
					ep.method, ep.path, resp.StatusCode)
			}
		})
	}
}

// =============================================================================
// Graph Error Paths (enabled, bad input)
// =============================================================================

func TestErrorPaths_GraphBadInput(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("shortestPath missing fields", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/shortestPath",
			map[string]interface{}{"from": "a:1"}) // missing "to"
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("pathExists missing fields", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/pathExists",
			map[string]interface{}{"to": "b:1"}) // missing "from"
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("commonNeighbors missing fields", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/commonNeighbors",
			map[string]interface{}{"node_a": "a:1"}) // missing "node_b"
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("shortestPath malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/shortestPath",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("pathExists malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/pathExists",
			strings.NewReader(`{broken`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("commonNeighbors malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/commonNeighbors",
			strings.NewReader(`{broken`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("graph path malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/path",
			strings.NewReader(`{broken`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("graph neighbors malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/neighbors",
			strings.NewReader(`{broken`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("graph node search malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/nodes/search",
			strings.NewReader(`{broken`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// OQL Error Paths
// =============================================================================

func TestErrorPaths_OQLBadInput(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("sync empty query", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/oql/query",
			map[string]interface{}{"query": ""})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("sync missing query field", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/oql/query",
			map[string]interface{}{"not_query": "SELECT * FROM items"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("sync malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/oql/query",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("async empty query", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/oql/query/async",
			map[string]interface{}{"query": ""})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("async malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/oql/query/async",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("status nonexistent query", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/oql/query/nonexistent-id-12345", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("result nonexistent query", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/oql/query/nonexistent-id-12345/result", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// Sulpher (graph query) Error Paths
// =============================================================================

func TestErrorPaths_SulpherBadInput(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("sync empty query", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/query",
			map[string]interface{}{"query": ""})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("sync missing query field", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/query",
			map[string]interface{}{"not_query": "FIND nodes"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("sync malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/query",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("async empty query", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/graph/query/async",
			map[string]interface{}{"query": ""})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("async malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/graph/query/async",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("status nonexistent query", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/graph/query/nonexistent-id-12345", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("result nonexistent query", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/graph/query/nonexistent-id-12345/result", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// Schema Error Paths
// =============================================================================

func TestErrorPaths_SchemaBadInput(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("POST schema malformed json", func(t *testing.T) {
		req, _ := http.NewRequest("POST", ts.ts.URL+"/api/v1/schema/items",
			strings.NewReader(`not json`))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("POST schema invalid entity name", func(t *testing.T) {
		resp, _ := ts.doRequest("POST", "/api/v1/schema/123bad",
			map[string]interface{}{"type": "object"})
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})

	t.Run("GET schema nonexistent", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/schema/nonexistent_entity_xyz", nil)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("GET schema invalid entity name", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/schema/123bad", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// FTS Error Paths
// =============================================================================

func TestErrorPaths_FTSDisabled(t *testing.T) {
	// Default test server has FTS disabled
	ts := setupTestServer(t)
	defer ts.cleanup()

	t.Run("search returns 503 when disabled", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/search?q=test", nil)
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("expected 503, got %d", resp.StatusCode)
		}
	})

	t.Run("missing q param returns 400", func(t *testing.T) {
		resp, _ := ts.doRequest("GET", "/api/v1/search", nil)
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400, got %d", resp.StatusCode)
		}
	})
}

// =============================================================================
// Error response shape validation
// =============================================================================

func TestErrorPaths_ResponseShape(t *testing.T) {
	ts := setupTestServer(t)
	defer ts.cleanup()

	// Trigger a known error
	resp, body := ts.doRequest("GET", "/api/v1/items/abc", nil)
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", resp.StatusCode)
	}

	// Verify error response structure
	var errResp map[string]interface{}
	if err := json.Unmarshal(body, &errResp); err != nil {
		t.Fatalf("error response is not valid JSON: %v", err)
	}

	// Should have an error field
	errObj, ok := errResp["error"]
	if !ok {
		t.Fatal("error response missing 'error' field")
	}

	// Error should be a map with message and status
	errMap, ok := errObj.(map[string]interface{})
	if !ok {
		// Some handlers return error as a string — that's also acceptable
		_, ok = errObj.(string)
		if !ok {
			t.Error("error field should be a string or an object with message/status")
		}
		return
	}

	if _, hasMsg := errMap["message"]; !hasMsg {
		t.Error("error object missing 'message' field")
	}
}

// =============================================================================
// Helpers
// =============================================================================

// setupTestServerNoGraph creates a test server with graph operations disabled
func setupTestServerNoGraph(t *testing.T) *TestServer {
	ts := setupTestServer(t)
	ts.cfg.GraphEnabled = false

	// Recreate server with updated config (graph disabled)
	ts.ts.Close()

	store, _ := storage.NewStore("jsonfile", map[string]interface{}{
		"base_dir": ts.cfg.BaseDir,
		"schema":   ts.cfg.Schema,
	})
	memCache := cache.NewMemoryCache(1000, 300*time.Second)
	validator := validation.NewJSONSchemaValidator(
		filepath.Join(ts.cfg.BaseDir, ts.cfg.Schema, "_schemas"))
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(ts.cfg, store, memCache, nil, nil, validator, logger)
	ts.server = srv
	ts.ts = httptest.NewServer(srv.Handler())

	return ts
}
