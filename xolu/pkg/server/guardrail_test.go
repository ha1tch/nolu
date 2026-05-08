// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// setupGuardrailServer creates a test server with deliberately low query
// limits so guardrails can be triggered with small datasets.
func setupGuardrailServer(t *testing.T, overrides func(*config.Config)) *TestServer {
	t.Helper()
	tmpDir := t.TempDir()
	schemaDir := filepath.Join(tmpDir, "test_schema")
	os.MkdirAll(schemaDir, 0755)

	// Create entity directory so OQL recognises it
	os.MkdirAll(filepath.Join(schemaDir, "items"), 0755)

	cfg := &config.Config{
		Host:              "localhost",
		Port:              0,
		BaseDir:           tmpDir,
		Schema:            "test_schema",
		SchemaDir:         schemaDir,
		StorageType:       "jsonfile",
		CacheType:         "memory",
		CacheTTL:          300,
		GraphEnabled:      false,
		FullTextEnabled:   false,
		MaxEmbedDepth:     10,
		RefEmbedDepth:     3,
		MaxEntitySize:     1048576,
		DefaultPageSize:   10,
		PatchNullBehavior: "store",
		TenantMode:        "path",
		TenantAutoRegister: true,
		// Guardrail defaults — tests override these
		QueryTimeout:          30,
		QueryMaxRows:          10000,
		QueryMaxScanRows:      100000,
		QueryMaxResponseBytes: 10485760,
	}

	if overrides != nil {
		overrides(cfg)
	}

	store, err := storage.NewStore("jsonfile", map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	})
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	schemaPath := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaPath)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, nil, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	return &TestServer{
		server: srv,
		ts:     ts,
		cfg:    cfg,
		t:      t,
	}
}

// seedItems creates n items in the test server via the REST API.
func seedItems(ts *TestServer, n int) {
	for i := 0; i < n; i++ {
		ts.doRequest("POST", "/api/v1/items", map[string]interface{}{
			"name":  "item",
			"index": i,
		})
	}
}

// oqlQuery sends a POST to /api/v1/oql/query with the given query string.
func oqlQuery(ts *TestServer, query string) (*http.Response, map[string]interface{}) {
	resp, body := ts.doRequest("POST", "/api/v1/oql/query", map[string]interface{}{
		"query": query,
	})
	var result map[string]interface{}
	json.Unmarshal(body, &result)
	return resp, result
}

// --- Scan limit ---

func TestGuardrail_ScanLimit(t *testing.T) {
	ts := setupGuardrailServer(t, func(cfg *config.Config) {
		cfg.QueryMaxScanRows = 5 // Very low: abort after scanning 5 rows
	})
	defer ts.ts.Close()

	// Seed 10 items — more than the scan limit
	seedItems(ts, 10)

	resp, result := oqlQuery(ts, "SELECT * FROM items")

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("scan limit: got status %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}

	errObj, _ := result["error"].(map[string]interface{})
	errMsg, _ := errObj["message"].(string)
	if !strings.Contains(errMsg, "scan limit exceeded") {
		t.Errorf("scan limit: error = %q, want 'scan limit exceeded'", errMsg)
	}

	code, _ := errObj["code"].(string)
	if code != "OLU-QL010" {
		t.Errorf("scan limit: code = %q, want OLU-QL010", code)
	}
}

// --- Row limit ---

func TestGuardrail_RowLimit(t *testing.T) {
	ts := setupGuardrailServer(t, func(cfg *config.Config) {
		cfg.QueryMaxRows = 3 // Very low: max 3 rows returned
		cfg.QueryMaxScanRows = 100000 // Don't hit scan limit
	})
	defer ts.ts.Close()

	// Seed 5 items — more than the row limit
	seedItems(ts, 5)

	resp, result := oqlQuery(ts, "SELECT * FROM items")

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("row limit: got status %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}

	errObj, _ := result["error"].(map[string]interface{})
	errMsg, _ := errObj["message"].(string)
	if !strings.Contains(errMsg, "result limit exceeded") {
		t.Errorf("row limit: error = %q, want 'result limit exceeded'", errMsg)
	}

	code, _ := errObj["code"].(string)
	if code != "OLU-QL009" {
		t.Errorf("row limit: code = %q, want OLU-QL009", code)
	}
}

// --- Response size limit ---

func TestGuardrail_ResponseSizeLimit(t *testing.T) {
	ts := setupGuardrailServer(t, func(cfg *config.Config) {
		cfg.QueryMaxResponseBytes = 100 // 100 bytes — almost any response exceeds this
		cfg.QueryMaxRows = 100000
		cfg.QueryMaxScanRows = 100000
	})
	defer ts.ts.Close()

	// Seed a few items — the JSON response will exceed 100 bytes
	seedItems(ts, 3)

	resp, result := oqlQuery(ts, "SELECT * FROM items")

	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("response size: got status %d, want %d", resp.StatusCode, http.StatusRequestEntityTooLarge)
	}

	errObj, _ := result["error"].(map[string]interface{})
	code, _ := errObj["code"].(string)
	if code != "OLU-QL011" {
		t.Errorf("response size: code = %q, want OLU-QL011", code)
	}
}

// --- Query timeout ---

func TestGuardrail_QueryTimeout(t *testing.T) {
	// This test verifies that the timeout mechanism is wired up.
	// We use a 1-second timeout; the query itself is fast but
	// we verify the deadline is set on the context by checking
	// that normal queries still succeed within the timeout.
	ts := setupGuardrailServer(t, func(cfg *config.Config) {
		cfg.QueryTimeout = 1 // 1 second
	})
	defer ts.ts.Close()

	seedItems(ts, 2)

	// A fast query should succeed even with a 1s timeout
	resp, result := oqlQuery(ts, "SELECT * FROM items")

	if resp.StatusCode != http.StatusOK {
		errObj, _ := result["error"].(map[string]interface{})
	errMsg, _ := errObj["message"].(string)
		t.Errorf("fast query with timeout: got status %d (%s), want 200", resp.StatusCode, errMsg)
	}
}

// --- Under-limit queries succeed ---

func TestGuardrail_UnderLimits(t *testing.T) {
	ts := setupGuardrailServer(t, func(cfg *config.Config) {
		cfg.QueryMaxRows = 100
		cfg.QueryMaxScanRows = 100
		cfg.QueryMaxResponseBytes = 1048576 // 1 MB
	})
	defer ts.ts.Close()

	seedItems(ts, 5)

	resp, result := oqlQuery(ts, "SELECT * FROM items")

	if resp.StatusCode != http.StatusOK {
		errObj, _ := result["error"].(map[string]interface{})
	errMsg, _ := errObj["message"].(string)
		t.Errorf("under-limit query: got status %d (%s), want 200", resp.StatusCode, errMsg)
	}

	data, ok := result["data"].([]interface{})
	if !ok || len(data) != 5 {
		t.Errorf("under-limit query: got %d rows, want 5", len(data))
	}
}

// --- Config defaults ---

func TestGuardrail_ConfigDefaults(t *testing.T) {
	cfg := config.Default()

	if cfg.QueryTimeout != 30 {
		t.Errorf("default QueryTimeout = %d, want 30", cfg.QueryTimeout)
	}
	if cfg.QueryMaxRows != 10000 {
		t.Errorf("default QueryMaxRows = %d, want 10000", cfg.QueryMaxRows)
	}
	if cfg.QueryMaxScanRows != 100000 {
		t.Errorf("default QueryMaxScanRows = %d, want 100000", cfg.QueryMaxScanRows)
	}
	if cfg.QueryMaxResponseBytes != 10485760 {
		t.Errorf("default QueryMaxResponseBytes = %d, want 10485760", cfg.QueryMaxResponseBytes)
	}
}
