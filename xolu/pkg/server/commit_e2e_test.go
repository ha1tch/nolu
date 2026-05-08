// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// commit_e2e_test.go
//
// End-to-end tests for the /commit endpoint through the HTTP layer.
//
// /commit is only available on the SQLite backend; the jsonfile backend
// returns 501 Not Implemented. All substantive tests therefore use a
// SQLite environment. The 501 behaviour is verified separately using
// the standard jsonfile e2e env.
//
// Tests cover:
//   - Basic happy path (create + append)
//   - FSM round-trip (GET state → commit with CAS → verify)
//   - CAS conflict (stale version → 409 with current_version)
//   - Validation errors (structural: missing entity, empty append)
//   - Rollback on duplicate explicit append ID
//   - 501 on jsonfile backend
//   - Strict mode: schema validation failure blocks the commit

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
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

// ----------------------------------------------------------------------------
// SQLite commit environment
// ----------------------------------------------------------------------------

// commitEnv is a fully-wired test server backed by SQLite.
// It is separate from the jsonfile-backed e2eEnv because /commit requires SQLite.
type commitEnv struct {
	*e2eEnv
}

func newCommitEnv(t *testing.T) *commitEnv {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "olu-commit-e2e-*")
	if err != nil {
		t.Fatal(err)
	}

	dbPath := filepath.Join(tmpDir, "test.db")
	schemaDir := filepath.Join(tmpDir, "commit_schema")
	os.MkdirAll(schemaDir, 0755)

	cfg := &config.Config{
		Host:                  "localhost",
		Port:                  0,
		StorageType:           "sqlite",
		DBPath:                dbPath,
		BaseDir:               tmpDir,
		Schema:                "commit_schema",
		SchemaDir:             schemaDir,
		CacheType:             "memory",
		CacheTTL:              300,
		GraphEnabled:          true,
		GraphMode:             "flat",
		FullTextEnabled:       false,
		MaxEmbedDepth:         10,
		RefEmbedDepth:         3,
		MaxEntitySize:         1048576,
		DefaultPageSize:       10,
		PatchNullBehavior:     "store",
		TenantMode:            "path",
		TenantAutoRegister:    true,
		MaxCascadeDeletions:   100,
		QueryTimeout:          30,
		QueryMaxRows:          10000,
		QueryMaxScanRows:      100000,
		QueryMaxResponseBytes: 10485760,
		GraphDataFile:         filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:        filepath.Join(tmpDir, "graph.index"),
		GraphQueryTTL:         86400,
		MaxQueryDepth:         10,
		StrictCommit:          true,
	}

	storeConfig := map[string]interface{}{
		"db_path": dbPath,
	}
	store, err := storage.NewStore("sqlite", storeConfig)
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	validator := validation.NewJSONSchemaValidator(filepath.Join(schemaDir, "_schemas"))
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	inner := &e2eEnv{ts: ts, tmpDir: tmpDir, t: t}
	return &commitEnv{inner}
}

// commitURL returns the /commit path, optionally tenant-scoped.
func commitURL(tenantID string) string {
	if tenantID == "" {
		return "/api/v1/commit"
	}
	return "/api/v1/tenant/" + tenantID + "/commit"
}

// ----------------------------------------------------------------------------
// Happy path
// ----------------------------------------------------------------------------

func TestCommitE2E_CreateAndAppend(t *testing.T) {
	env := newCommitEnv(t)
	defer env.cleanup()

	req := map[string]interface{}{
		"update": map[string]interface{}{
			"entity": "assets",
			"id":     5001,
			"data":   map[string]interface{}{"state": "on-shelf"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"data": map[string]interface{}{
					"asset_id": 5001,
					"to_state": "on-shelf",
				},
			},
		},
	}

	status, result := env.doJSON("POST", commitURL(""), req)
	if status != http.StatusOK {
		t.Fatalf("expected 200, got %d: %v", status, result)
	}

	update, _ := result["update"].(map[string]interface{})
	if update == nil {
		t.Fatalf("response missing 'update': %v", result)
	}
	if created, _ := update["created"].(bool); !created {
		t.Errorf("expected created=true")
	}
	if v, _ := update["version"].(float64); v != 1 {
		t.Errorf("expected version=1, got %v", v)
	}
	appended, _ := result["appended"].([]interface{})
	if len(appended) != 1 {
		t.Fatalf("expected 1 appended, got %d", len(appended))
	}
	a := appended[0].(map[string]interface{})
	if a["entity"] != "events" {
		t.Errorf("expected appended entity 'events', got %v", a["entity"])
	}
	if id, _ := a["id"].(float64); id <= 0 {
		t.Errorf("expected positive appended ID, got %v", a["id"])
	}
}

// ----------------------------------------------------------------------------
// FSM round-trip
// ----------------------------------------------------------------------------

func TestCommitE2E_FSMRoundTrip(t *testing.T) {
	env := newCommitEnv(t)
	defer env.cleanup()

	status, _ := env.doJSON("POST", "/api/v1/assets/save/7001",
		map[string]interface{}{"state": "idle"})
	if status != http.StatusCreated && status != http.StatusOK {
		t.Fatalf("seed: unexpected status %d", status)
	}

	getStatus, got := env.doJSON("GET", "/api/v1/assets/7001", nil)
	if getStatus != http.StatusOK {
		t.Fatalf("GET: unexpected status %d", getStatus)
	}
	version := int(got["_version"].(float64))

	commitReq := map[string]interface{}{
		"update": map[string]interface{}{
			"entity":  "assets",
			"id":      7001,
			"version": version,
			"data":    map[string]interface{}{"state": "active"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"data": map[string]interface{}{
					"asset_id":   7001,
					"from_state": "idle",
					"to_state":   "active",
				},
			},
		},
	}

	status, result := env.doJSON("POST", commitURL(""), commitReq)
	if status != http.StatusOK {
		t.Fatalf("commit: expected 200, got %d: %v", status, result)
	}

	update := result["update"].(map[string]interface{})
	newVersion := int(update["version"].(float64))
	if newVersion != version+1 {
		t.Errorf("expected version %d, got %d", version+1, newVersion)
	}

	_, asset := env.doJSON("GET", "/api/v1/assets/7001", nil)
	if asset["state"] != "active" {
		t.Errorf("asset state: want 'active', got %v", asset["state"])
	}
}

// ----------------------------------------------------------------------------
// CAS conflict
// ----------------------------------------------------------------------------

func TestCommitE2E_CASConflict(t *testing.T) {
	env := newCommitEnv(t)
	defer env.cleanup()

	env.doJSON("POST", "/api/v1/assets/save/8001", map[string]interface{}{"state": "A"})
	env.doJSON("POST", "/api/v1/assets/save/8001", map[string]interface{}{"state": "A2"})

	staleVersion := 1
	req := map[string]interface{}{
		"update": map[string]interface{}{
			"entity":  "assets",
			"id":      8001,
			"version": staleVersion,
			"data":    map[string]interface{}{"state": "B"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"data":   map[string]interface{}{"ev": "should-not-land"},
			},
		},
	}

	status, result := env.doJSON("POST", commitURL(""), req)
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %v", status, result)
	}
	errObj, _ := result["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != "OLU-CM001" {
		t.Errorf("expected OLU-CM001, got: %v", result)
	}
	if cv, ok := result["current_version"].(float64); !ok || cv <= 0 {
		t.Errorf("expected positive current_version, got %v", result["current_version"])
	}
	_, asset := env.doJSON("GET", "/api/v1/assets/8001", nil)
	if asset["state"] != "A2" {
		t.Errorf("state should be 'A2' after conflict, got %v", asset["state"])
	}
}

// ----------------------------------------------------------------------------
// Structural validation errors
// ----------------------------------------------------------------------------

func TestCommitE2E_ValidationErrors(t *testing.T) {
	env := newCommitEnv(t)
	defer env.cleanup()

	cases := []struct {
		name     string
		body     map[string]interface{}
		wantCode string
	}{
		{
			name: "missing update entity",
			body: map[string]interface{}{
				"update": map[string]interface{}{"id": 1, "data": map[string]interface{}{}},
				"append": []interface{}{map[string]interface{}{"entity": "events", "data": map[string]interface{}{}}},
			},
			wantCode: "OLU-CM002",
		},
		{
			name: "empty append array",
			body: map[string]interface{}{
				"update": map[string]interface{}{"entity": "assets", "id": 1, "data": map[string]interface{}{}},
				"append": []interface{}{},
			},
			wantCode: "OLU-CM003",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, result := env.doJSON("POST", commitURL(""), tc.body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d: %v", status, result)
			}
			errObj, _ := result["error"].(map[string]interface{})
			if errObj == nil || errObj["code"] != tc.wantCode {
				t.Errorf("expected error code %s, got: %v", tc.wantCode, result)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// Rollback on duplicate explicit append ID
// ----------------------------------------------------------------------------

func TestCommitE2E_AppendDuplicateIDRollback(t *testing.T) {
	env := newCommitEnv(t)
	defer env.cleanup()

	env.doJSON("POST", "/api/v1/events/save/999", map[string]interface{}{"ev": "existing"})

	dupID := 999
	req := map[string]interface{}{
		"update": map[string]interface{}{
			"entity": "assets",
			"id":     3001,
			"data":   map[string]interface{}{"state": "X"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"id":     dupID,
				"data":   map[string]interface{}{"ev": "duplicate"},
			},
		},
	}

	status, result := env.doJSON("POST", commitURL(""), req)
	if status != http.StatusConflict {
		t.Fatalf("expected 409, got %d: %v", status, result)
	}
	errObj, _ := result["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != "OLU-CM007" {
		t.Errorf("expected OLU-CM007, got: %v", result)
	}

	getStatus, _ := env.doJSON("GET", "/api/v1/assets/3001", nil)
	if getStatus != http.StatusNotFound {
		t.Errorf("asset 3001 should not exist after rollback, got status %d", getStatus)
	}
}

// ----------------------------------------------------------------------------
// 501 on jsonfile backend
// ----------------------------------------------------------------------------

// TestCommitE2E_JSONFileReturns501 verifies that /commit returns 501 Not
// Implemented when the server is running with the jsonfile backend.
// The jsonfile backend is deprecated and does not provide true transactional
// atomicity. OLU-CM009 is the canonical error code for this condition.
func TestCommitE2E_JSONFileReturns501(t *testing.T) {
	env := newE2EEnv(t) // standard jsonfile-backed env
	defer env.cleanup()

	req := map[string]interface{}{
		"update": map[string]interface{}{
			"entity": "assets",
			"id":     1,
			"data":   map[string]interface{}{"state": "x"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"data":   map[string]interface{}{"ev": "test"},
			},
		},
	}

	status, result := env.doJSON("POST", commitURL(""), req)
	if status != http.StatusNotImplemented {
		t.Fatalf("expected 501, got %d: %v", status, result)
	}
	errObj, _ := result["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != "OLU-CM009" {
		t.Errorf("expected OLU-CM009, got: %v", result)
	}
}

// ----------------------------------------------------------------------------
// Strict mode: schema validation
// ----------------------------------------------------------------------------

// TestCommitE2E_StrictModeSchemaValidation verifies that when StrictCommit
// is true (the default) and a JSON schema is registered for an entity,
// payloads that violate the schema are rejected before the storage transaction
// runs, and nothing is written to the store.
func TestCommitE2E_StrictModeSchemaValidation(t *testing.T) {
	env := newCommitEnv(t) // StrictCommit=true
	defer env.cleanup()

	// Register a minimal schema for "assets" that requires "state".
	schema := map[string]interface{}{
		"type":     "object",
		"required": []interface{}{"state"},
		"properties": map[string]interface{}{
			"state": map[string]interface{}{"type": "string"},
		},
	}
	schemaStatus, _ := env.doJSON("POST", "/api/v1/schema/assets", schema)
	if schemaStatus != http.StatusCreated && schemaStatus != http.StatusOK {
		t.Skipf("schema registration returned %d; skipping strict-mode schema test", schemaStatus)
	}

	// Commit with a payload that violates the schema (missing "state").
	badReq := map[string]interface{}{
		"update": map[string]interface{}{
			"entity": "assets",
			"id":     6001,
			"data":   map[string]interface{}{"ref_id": "missing-state-field"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"data":   map[string]interface{}{"ev": "should-not-land"},
			},
		},
	}

	status, result := env.doJSON("POST", commitURL(""), badReq)
	if status != http.StatusBadRequest {
		t.Fatalf("strict mode: expected 400 for schema violation, got %d: %v", status, result)
	}
	errObj, _ := result["error"].(map[string]interface{})
	if errObj == nil || errObj["code"] != "OLU-VL001" {
		t.Errorf("expected OLU-VL001, got: %v", result)
	}

	// Nothing should have been written.
	getStatus, _ := env.doJSON("GET", "/api/v1/assets/6001", nil)
	if getStatus != http.StatusNotFound {
		t.Errorf("asset 6001 should not exist after rejected commit, got status %d", getStatus)
	}

	// A valid payload (with "state") should succeed.
	goodReq := map[string]interface{}{
		"update": map[string]interface{}{
			"entity": "assets",
			"id":     6001,
			"data":   map[string]interface{}{"state": "on-shelf"},
		},
		"append": []interface{}{
			map[string]interface{}{
				"entity": "events",
				"data":   map[string]interface{}{"ev": "ingested"},
			},
		},
	}
	status, result = env.doJSON("POST", commitURL(""), goodReq)
	if status != http.StatusOK {
		t.Fatalf("strict mode: valid payload should succeed, got %d: %v", status, result)
	}
}
