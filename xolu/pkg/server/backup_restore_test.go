// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// setupSQLiteTestServer creates a SQLite-backed test server.
func setupSQLiteTestServer(t *testing.T) *TestServer {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "test.db")

	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:         "sqlite",
		DBPath:              dbPath,
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        false,
		FullTextEnabled:     false,
		MaxEmbedDepth:       10,
		RefEmbedDepth:       3,
		MaxEntitySize:       1048576,
		DefaultPageSize:     10,
		PatchNullBehavior:   "store",
		TenantMode:          "path",
		TenantAutoRegister:  true,
		MaxCascadeDeletions: 100,
		QueryTimeout:        30,
		QueryMaxRows:        10000,
		QueryMaxScanRows:    100000,
		QueryMaxResponseBytes: 10485760,
	}

	os.MkdirAll(cfg.SchemaDir, 0755)

	storeConfig := map[string]interface{}{
		"db_path": dbPath,
	}
	store, err := storage.NewStore("sqlite", storeConfig)
	if err != nil {
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, nil, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	return &TestServer{
		server:      srv,
		ts:          ts,
		cfg:         cfg,
		t:           t,
		sqliteStore: store,
	}
}

// startServerFromDB creates a new server backed by an existing SQLite database.
func startServerFromDB(t *testing.T, dbPath string) *TestServer {
	t.Helper()
	tmpDir := t.TempDir()

	// Copy the DB to a new location so we don't share file handles
	restoredDB := filepath.Join(tmpDir, "restored.db")
	src, err := os.ReadFile(dbPath)
	if err != nil {
		t.Fatalf("read backup db: %v", err)
	}
	if err := os.WriteFile(restoredDB, src, 0644); err != nil {
		t.Fatalf("write restored db: %v", err)
	}

	cfg := &config.Config{
		Host:                "localhost",
		Port:                0,
		StorageType:         "sqlite",
		DBPath:              restoredDB,
		BaseDir:             tmpDir,
		Schema:              "test_schema",
		SchemaDir:           filepath.Join(tmpDir, "test_schema"),
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        false,
		FullTextEnabled:     false,
		MaxEmbedDepth:       10,
		RefEmbedDepth:       3,
		MaxEntitySize:       1048576,
		DefaultPageSize:     100, // large enough to see all records
		PatchNullBehavior:   "store",
		TenantMode:          "path",
		TenantAutoRegister:  true,
		MaxCascadeDeletions: 100,
		QueryTimeout:        30,
		QueryMaxRows:        10000,
		QueryMaxScanRows:    100000,
		QueryMaxResponseBytes: 10485760,
	}

	os.MkdirAll(cfg.SchemaDir, 0755)

	storeConfig := map[string]interface{}{
		"db_path": restoredDB,
	}
	store, err := storage.NewStore("sqlite", storeConfig)
	if err != nil {
		t.Fatalf("open restored db: %v", err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, nil, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	return &TestServer{
		server:      srv,
		ts:          ts,
		cfg:         cfg,
		t:           t,
		sqliteStore: store,
	}
}

// TestBackupRestore_SQLite is the backup/restore verification drill.
// It exercises the full round trip:
//   1. Create a SQLite-backed server
//   2. Write entities via REST
//   3. Export via /api/v1/export (backup)
//   4. Extract the DB from the zip
//   5. Start a new server against the restored DB
//   6. Verify data integrity
func TestBackupRestore_SQLite(t *testing.T) {
	// --- Phase 1: Create and populate ---

	origin := setupSQLiteTestServer(t)
	defer origin.cleanup()

	// Seed some entities
	entities := []map[string]interface{}{
		{"name": "Alpha", "priority": 1, "status": "active"},
		{"name": "Bravo", "priority": 2, "status": "active"},
		{"name": "Charlie", "priority": 3, "status": "archived"},
		{"name": "Delta", "priority": 4, "status": "active"},
		{"name": "Echo", "priority": 5, "status": "archived"},
	}

	var createdIDs []float64
	for _, entity := range entities {
		resp, body := origin.doRequest("POST", "/api/v1/tasks", entity)
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("seed: POST /tasks got %d: %s", resp.StatusCode, string(body))
		}
		var created map[string]interface{}
		json.Unmarshal(body, &created)
		if id, ok := created["id"].(float64); ok {
			createdIDs = append(createdIDs, id)
		}
	}

	if len(createdIDs) != 5 {
		t.Fatalf("seed: created %d items, want 5", len(createdIDs))
	}

	// Verify data exists before backup
	resp, body := origin.doRequest("GET", "/api/v1/tasks?per_page=100", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("pre-backup list: %d", resp.StatusCode)
	}
	var listResult map[string]interface{}
	json.Unmarshal(body, &listResult)
	preBackupData, _ := listResult["data"].([]interface{})
	if len(preBackupData) != 5 {
		t.Fatalf("pre-backup: %d items, want 5", len(preBackupData))
	}

	// --- Phase 2: Export (backup) ---

	resp, body = origin.doRequest("GET", "/api/v1/export", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("export: got %d", resp.StatusCode)
	}

	// Parse the zip
	zipReader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("export: invalid zip: %v", err)
	}

	// Find the entities.db file
	var dbFile *zip.File
	var manifestFile *zip.File
	for _, f := range zipReader.File {
		switch f.Name {
		case "entities.db":
			dbFile = f
		case "manifest.json":
			manifestFile = f
		}
	}

	if dbFile == nil {
		names := make([]string, len(zipReader.File))
		for i, f := range zipReader.File {
			names[i] = f.Name
		}
		t.Fatalf("export: no entities.db in zip (files: %v)", names)
	}

	if manifestFile != nil {
		rc, _ := manifestFile.Open()
		manifestData, _ := io.ReadAll(rc)
		rc.Close()
		var manifest map[string]interface{}
		json.Unmarshal(manifestData, &manifest)
		if manifest["storage_type"] != "sqlite" {
			t.Errorf("manifest: storage_type = %v, want sqlite", manifest["storage_type"])
		}
	}

	// Extract the DB to a temp file
	rc, err := dbFile.Open()
	if err != nil {
		t.Fatalf("extract db: %v", err)
	}
	extractedDB := filepath.Join(t.TempDir(), "backup.db")
	dbBytes, _ := io.ReadAll(rc)
	rc.Close()
	if err := os.WriteFile(extractedDB, dbBytes, 0644); err != nil {
		t.Fatalf("write extracted db: %v", err)
	}

	// Sanity: the extracted file should be a valid SQLite database
	if len(dbBytes) < 16 || string(dbBytes[:16]) != "SQLite format 3\x00" {
		t.Fatal("extracted file is not a valid SQLite database")
	}

	// --- Phase 3: Restore ---

	// Close origin server to release the DB file
	origin.ts.Close()
	if origin.sqliteStore != nil {
		origin.sqliteStore.Close()
		origin.sqliteStore = nil // prevent double-close in deferred cleanup
	}

	restored := startServerFromDB(t, extractedDB)
	defer restored.cleanup()

	// --- Phase 4: Verify data integrity ---

	// 4a. Count check
	resp, body = restored.doRequest("GET", "/api/v1/tasks?per_page=100", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restored list: %d: %s", resp.StatusCode, string(body))
	}
	json.Unmarshal(body, &listResult)
	restoredData, _ := listResult["data"].([]interface{})
	if len(restoredData) != 5 {
		t.Fatalf("restored: %d items, want 5", len(restoredData))
	}

	// 4b. Content check — verify each record's fields survived
	for _, item := range restoredData {
		rec, ok := item.(map[string]interface{})
		if !ok {
			t.Fatal("restored: record is not a map")
		}
		name, _ := rec["name"].(string)
		if name == "" {
			t.Error("restored: record missing 'name' field")
		}
		priority, hasPriority := rec["priority"]
		if !hasPriority || priority == nil {
			t.Errorf("restored: record %q missing 'priority'", name)
		}
		status, hasStatus := rec["status"]
		if !hasStatus || status == nil {
			t.Errorf("restored: record %q missing 'status'", name)
		}
	}

	// 4c. Individual record fetch
	resp, body = restored.doRequest("GET", "/api/v1/tasks/1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("restored get /tasks/1: %d", resp.StatusCode)
	}
	var singleResult map[string]interface{}
	json.Unmarshal(body, &singleResult)
	if singleResult["name"] != "Alpha" {
		t.Errorf("restored: tasks/1 name = %v, want Alpha", singleResult["name"])
	}

	// 4d. Health check on restored server
	resp, _ = restored.doRequest("GET", "/health", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("restored health: %d", resp.StatusCode)
	}

	resp, _ = restored.doRequest("GET", "/ready", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("restored ready: %d", resp.StatusCode)
	}
}
