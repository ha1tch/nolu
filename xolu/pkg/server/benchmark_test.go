// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
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

// portRecoveryMu serializes benchmark cleanup to allow port recovery
var portRecoveryMu sync.Mutex
var lastBenchmarkEnd time.Time

const portRecoveryDelay = 5 * time.Second

// waitForPortRecovery waits for ports to become available
func waitForPortRecovery(b *testing.B) {
	portRecoveryMu.Lock()
	defer portRecoveryMu.Unlock()

	if !lastBenchmarkEnd.IsZero() {
		elapsed := time.Since(lastBenchmarkEnd)
		if elapsed < portRecoveryDelay {
			wait := portRecoveryDelay - elapsed
			b.Logf("Waiting %.0fs for port recovery...", wait.Seconds())
			time.Sleep(wait)
		}
	}
}

// markBenchmarkEnd records when a benchmark finished
func markBenchmarkEnd() {
	portRecoveryMu.Lock()
	lastBenchmarkEnd = time.Now()
	portRecoveryMu.Unlock()
}

// benchEnv holds the test server and a dedicated HTTP client
type benchEnv struct {
	server *httptest.Server
	client *http.Client
	config *config.Config
}

// setupBenchServer creates a server and dedicated client for benchmarking
func setupBenchServer(b *testing.B) *benchEnv {
	b.Helper()

	// Wait for ports from previous benchmark to recover
	waitForPortRecovery(b)

	tmpDir, err := os.MkdirTemp("", "olu-bench-*")
	if err != nil {
		b.Fatal(err)
	}

	cfg := &config.Config{
		StorageType:         "jsonfile",
		BaseDir:             tmpDir,
		Schema:              "bench_schema",
		CacheType:           "memory",
		CacheTTL:            300,
		GraphEnabled:        true,
		GraphMode:           "flat",
		FullTextEnabled:     false,
		CascadingDelete:     false,
		RefEmbedDepth:       3,
		MaxEmbedDepth:       10,
		GraphDataFile:       filepath.Join(tmpDir, "graph.data"),
		GraphIndexFile:      filepath.Join(tmpDir, "graph.index"),
		MaxCascadeDeletions: 100,
		MaxEntitySize:       1048576, // 1MB
	}

	storeConfig := map[string]interface{}{
		"base_dir": cfg.BaseDir,
		"schema":   cfg.Schema,
	}

	store, err := storage.NewStore("jsonfile", storeConfig)
	if err != nil {
		os.RemoveAll(tmpDir)
		b.Fatalf("Failed to create store: %v", err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	g := graph.NewFlatGraph()
	schemaDir := filepath.Join(cfg.BaseDir, cfg.Schema, "_schemas")
	validator := validation.NewJSONSchemaValidator(schemaDir)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, g, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	// Use the test server's client
	client := ts.Client()

	env := &benchEnv{
		server: ts,
		client: client,
		config: cfg,
	}

	b.Cleanup(func() {
		ts.Close()
		os.RemoveAll(tmpDir)
		markBenchmarkEnd()
	})

	return env
}

// URL returns the server URL
func (e *benchEnv) URL() string {
	return e.server.URL
}

// Do executes an HTTP request using the dedicated client
func (e *benchEnv) Do(b *testing.B, req *http.Request) *http.Response {
	resp, err := e.client.Do(req)
	if err != nil {
		b.Fatalf("Request failed: %v", err)
	}
	return resp
}

// CreateEntity creates an entity and returns its ID
func (e *benchEnv) CreateEntity(b *testing.B, entity string, data map[string]interface{}) int {
	b.Helper()

	bodyBytes, _ := json.Marshal(data)
	req, err := http.NewRequest("POST", e.URL()+"/api/v1/"+entity, bytes.NewBuffer(bodyBytes))
	if err != nil {
		b.Fatalf("Failed to create request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		b.Fatalf("Failed to execute request: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		b.Fatalf("Failed to create entity: status=%d body=%s", resp.StatusCode, string(body))
	}

	var result map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		b.Fatalf("Failed to decode response: %v", err)
	}

	idVal, ok := result["id"]
	if !ok {
		b.Fatal("Response missing 'id' field")
	}

	id, ok := idVal.(float64)
	if !ok {
		b.Fatalf("Invalid id type: %T", idVal)
	}

	return int(id)
}

// BenchmarkCreate benchmarks entity creation
func BenchmarkCreate(b *testing.B) {
	env := setupBenchServer(b)

	data := map[string]interface{}{
		"name":  "Benchmark User",
		"email": "bench@example.com",
		"age":   25,
	}
	bodyBytes, _ := json.Marshal(data)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", env.URL()+"/api/v1/users", bytes.NewBuffer(bodyBytes))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkGet benchmarks entity retrieval
func BenchmarkGet(b *testing.B) {
	env := setupBenchServer(b)
	id := env.CreateEntity(b, "users", map[string]interface{}{
		"name":  "Benchmark User",
		"email": "bench@example.com",
	})

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", fmt.Sprintf("%s/api/v1/users/%d", env.URL(), id), nil)
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkUpdate benchmarks entity updates
func BenchmarkUpdate(b *testing.B) {
	env := setupBenchServer(b)
	id := env.CreateEntity(b, "users", map[string]interface{}{
		"name":  "Benchmark User",
		"email": "bench@example.com",
	})

	updateData := map[string]interface{}{
		"name":  "Updated User",
		"email": "updated@example.com",
	}
	updateBytes, _ := json.Marshal(updateData)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("PUT", fmt.Sprintf("%s/api/v1/users/%d", env.URL(), id), bytes.NewBuffer(updateBytes))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkPatch benchmarks partial updates
func BenchmarkPatch(b *testing.B) {
	env := setupBenchServer(b)
	id := env.CreateEntity(b, "users", map[string]interface{}{
		"name":  "Benchmark User",
		"email": "bench@example.com",
		"age":   25,
	})

	patchData := map[string]interface{}{"age": 26}
	patchBytes, _ := json.Marshal(patchData)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("PATCH", fmt.Sprintf("%s/api/v1/users/%d", env.URL(), id), bytes.NewBuffer(patchBytes))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkList benchmarks entity listing with 10 pre-created entities
func BenchmarkList(b *testing.B) {
	env := setupBenchServer(b)

	// Create only 10 test entities
	for i := 0; i < 10; i++ {
		env.CreateEntity(b, "users", map[string]interface{}{
			"name":  fmt.Sprintf("User%d", i),
			"email": fmt.Sprintf("user%d@example.com", i),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", env.URL()+"/api/v1/users", nil)
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkListPaginated benchmarks paginated listing
func BenchmarkListPaginated(b *testing.B) {
	env := setupBenchServer(b)

	// Create only 10 test entities
	for i := 0; i < 10; i++ {
		env.CreateEntity(b, "users", map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", env.URL()+"/api/v1/users?page=1&per_page=20", nil)
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkDelete benchmarks entity deletion
func BenchmarkDelete(b *testing.B) {
	env := setupBenchServer(b)

	// Only pre-create what we need, with a reasonable cap
	n := b.N
	if n > 50 {
		n = 50
	}

	ids := make([]int, n)
	for i := 0; i < n; i++ {
		ids[i] = env.CreateEntity(b, "users", map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		idx := i % len(ids)
		req, _ := http.NewRequest("DELETE", fmt.Sprintf("%s/api/v1/users/%d", env.URL(), ids[idx]), nil)
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkGraphPath benchmarks path finding
func BenchmarkGraphPath(b *testing.B) {
	env := setupBenchServer(b)

	// Create chain of users
	var prevID int
	for i := 0; i < 5; i++ {
		data := map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
		}
		if i > 0 {
			data["friend"] = map[string]interface{}{
				"type":   "REF",
				"entity": "users",
				"id":     float64(prevID),
			}
		}
		prevID = env.CreateEntity(b, "users", data)
	}

	pathData := map[string]interface{}{
		"from":      fmt.Sprintf("users:%d", prevID),
		"to":        "users:1",
		"max_depth": 10,
	}
	pathBytes, _ := json.Marshal(pathData)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", env.URL()+"/api/v1/graph/path", bytes.NewBuffer(pathBytes))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkGraphNeighbors benchmarks neighbor queries
func BenchmarkGraphNeighbors(b *testing.B) {
	env := setupBenchServer(b)

	mainID := env.CreateEntity(b, "users", map[string]interface{}{"name": "MainUser"})

	// Create only 3 friends
	for i := 0; i < 3; i++ {
		env.CreateEntity(b, "users", map[string]interface{}{
			"name": fmt.Sprintf("Friend%d", i),
			"friendOf": map[string]interface{}{
				"type":   "REF",
				"entity": "users",
				"id":     float64(mainID),
			},
		})
	}

	neighborsData := map[string]interface{}{
		"node_id": fmt.Sprintf("users:%d", mainID),
	}
	neighborsBytes, _ := json.Marshal(neighborsData)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", env.URL()+"/api/v1/graph/neighbors", bytes.NewBuffer(neighborsBytes))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkHealthCheck benchmarks health endpoint
func BenchmarkHealthCheck(b *testing.B) {
	env := setupBenchServer(b)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("GET", env.URL()+"/health", nil)
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}

// BenchmarkConcurrentOperations benchmarks mixed operations
func BenchmarkConcurrentOperations(b *testing.B) {
	env := setupBenchServer(b)

	// Pre-create only 5 entities
	for i := 0; i < 5; i++ {
		env.CreateEntity(b, "users", map[string]interface{}{
			"name": fmt.Sprintf("User%d", i),
		})
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		switch i % 4 {
		case 0: // Create
			data := map[string]interface{}{"name": "New User"}
			bodyBytes, _ := json.Marshal(data)
			req, _ := http.NewRequest("POST", env.URL()+"/api/v1/users", bytes.NewBuffer(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			resp := env.Do(b, req)
			resp.Body.Close()
		case 1: // Get
			req, _ := http.NewRequest("GET", env.URL()+"/api/v1/users/1", nil)
			resp := env.Do(b, req)
			resp.Body.Close()
		case 2: // List
			req, _ := http.NewRequest("GET", env.URL()+"/api/v1/users", nil)
			resp := env.Do(b, req)
			resp.Body.Close()
		case 3: // Update
			data := map[string]interface{}{"name": "Updated"}
			bodyBytes, _ := json.Marshal(data)
			req, _ := http.NewRequest("PUT", env.URL()+"/api/v1/users/1", bytes.NewBuffer(bodyBytes))
			req.Header.Set("Content-Type", "application/json")
			resp := env.Do(b, req)
			resp.Body.Close()
		}
	}
}

// BenchmarkCreateWithReferences benchmarks creating entities with references
func BenchmarkCreateWithReferences(b *testing.B) {
	env := setupBenchServer(b)

	refID := env.CreateEntity(b, "users", map[string]interface{}{"name": "Reference"})

	data := map[string]interface{}{
		"name": "User with Ref",
		"manager": map[string]interface{}{
			"type":   "REF",
			"entity": "users",
			"id":     float64(refID),
		},
	}
	dataBytes, _ := json.Marshal(data)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", env.URL()+"/api/v1/users", bytes.NewBuffer(dataBytes))
		req.Header.Set("Content-Type", "application/json")
		resp := env.Do(b, req)
		resp.Body.Close()
	}
}
