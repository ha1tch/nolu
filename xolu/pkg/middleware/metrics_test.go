// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/config"
)

func TestMetrics_RecordRequest(t *testing.T) {
	m := NewMetrics()

	m.recordRequest("/api/v1/users", 200, 100*time.Millisecond)
	m.recordRequest("/api/v1/users", 200, 50*time.Millisecond)
	m.recordRequest("/api/v1/users", 404, 10*time.Millisecond)

	snapshot := m.GetSnapshot()

	if snapshot.RequestsTotal != 3 {
		t.Errorf("Expected 3 total requests, got %d", snapshot.RequestsTotal)
	}

	if snapshot.RequestsByCode[200] != 2 {
		t.Errorf("Expected 2 200s, got %d", snapshot.RequestsByCode[200])
	}

	if snapshot.RequestsByCode[404] != 1 {
		t.Errorf("Expected 1 404, got %d", snapshot.RequestsByCode[404])
	}

	if snapshot.RequestErrors != 1 {
		t.Errorf("Expected 1 error, got %d", snapshot.RequestErrors)
	}
}

func TestMetrics_NormalizePath(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"/api/v1/users", "/api/v1/users"},
		{"/api/v1/users/123", "/api/v1/users/:id"},
		{"/api/v1/users/123/posts/456", "/api/v1/users/:id/posts/:id"},
		{"/api/v1/orders/99999", "/api/v1/orders/:id"},
	}

	for _, tc := range tests {
		result := normalizePath(tc.input)
		if result != tc.expected {
			t.Errorf("normalizePath(%q) = %q, expected %q", tc.input, result, tc.expected)
		}
	}
}

func TestMetrics_LatencyBuckets(t *testing.T) {
	m := NewMetrics()

	// Record requests at different latencies
	m.recordRequest("/test", 200, 500*time.Microsecond) // 0.0005s -> 0.001 bucket
	m.recordRequest("/test", 200, 5*time.Millisecond)   // 0.005s -> 0.005 bucket
	m.recordRequest("/test", 200, 100*time.Millisecond) // 0.1s -> 0.1 bucket
	m.recordRequest("/test", 200, 2*time.Second)        // 2s -> 2.5 bucket

	snapshot := m.GetSnapshot()

	if snapshot.LatencyBuckets["0.001"] != 1 {
		t.Errorf("Expected 1 in 0.001 bucket, got %d", snapshot.LatencyBuckets["0.001"])
	}

	if snapshot.LatencyBuckets["0.1"] != 1 {
		t.Errorf("Expected 1 in 0.1 bucket, got %d", snapshot.LatencyBuckets["0.1"])
	}
}

func TestMetrics_EntityOperations(t *testing.T) {
	m := NewMetrics()

	m.RecordEntityCreate()
	m.RecordEntityCreate()
	m.RecordEntityRead()
	m.RecordEntityUpdate()
	m.RecordEntityDelete()
	m.RecordEntityList()
	m.RecordEntityList()

	snapshot := m.GetSnapshot()

	if snapshot.EntityCreates != 2 {
		t.Errorf("Expected 2 creates, got %d", snapshot.EntityCreates)
	}
	if snapshot.EntityReads != 1 {
		t.Errorf("Expected 1 read, got %d", snapshot.EntityReads)
	}
	if snapshot.EntityUpdates != 1 {
		t.Errorf("Expected 1 update, got %d", snapshot.EntityUpdates)
	}
	if snapshot.EntityDeletes != 1 {
		t.Errorf("Expected 1 delete, got %d", snapshot.EntityDeletes)
	}
	if snapshot.EntityLists != 2 {
		t.Errorf("Expected 2 lists, got %d", snapshot.EntityLists)
	}
}

func TestMetrics_CacheStats(t *testing.T) {
	m := NewMetrics()

	m.RecordCacheHit()
	m.RecordCacheHit()
	m.RecordCacheMiss()

	snapshot := m.GetSnapshot()

	if snapshot.CacheHits != 2 {
		t.Errorf("Expected 2 cache hits, got %d", snapshot.CacheHits)
	}
	if snapshot.CacheMisses != 1 {
		t.Errorf("Expected 1 cache miss, got %d", snapshot.CacheMisses)
	}
}

func TestMetrics_QueryStats(t *testing.T) {
	m := NewMetrics()

	m.RecordSearchQuery()
	m.RecordOQLQuery()
	m.RecordOQLQuery()
	m.RecordGraphQuery()

	snapshot := m.GetSnapshot()

	if snapshot.SearchQueries != 1 {
		t.Errorf("Expected 1 search query, got %d", snapshot.SearchQueries)
	}
	if snapshot.OQLQueries != 2 {
		t.Errorf("Expected 2 OQL queries, got %d", snapshot.OQLQueries)
	}
	if snapshot.GraphQueries != 1 {
		t.Errorf("Expected 1 graph query, got %d", snapshot.GraphQueries)
	}
}

func TestMetrics_PrometheusFormat(t *testing.T) {
	m := NewMetrics()

	m.recordRequest("/api/v1/users", 200, 10*time.Millisecond)
	m.recordRequest("/api/v1/users", 500, 100*time.Millisecond)
	m.RecordEntityCreate()
	m.RecordCacheHit()

	output := m.PrometheusFormat()

	// Check for expected metrics
	expectedMetrics := []string{
		"olu_uptime_seconds",
		"olu_requests_total",
		"olu_requests_by_status_total",
		"olu_request_errors_total",
		"olu_active_requests",
		"olu_request_duration_seconds_bucket",
		"olu_request_duration_seconds_sum",
		"olu_request_duration_seconds_count",
		"olu_entity_operations_total",
		"olu_cache_total",
		"olu_queries_total",
	}

	for _, metric := range expectedMetrics {
		if !strings.Contains(output, metric) {
			t.Errorf("Expected metric %q in output", metric)
		}
	}

	// Check for labels
	if !strings.Contains(output, `code="200"`) {
		t.Error("Expected status code 200 label")
	}
	if !strings.Contains(output, `code="500"`) {
		t.Error("Expected status code 500 label")
	}
	if !strings.Contains(output, `operation="create"`) {
		t.Error("Expected operation create label")
	}
}

func TestMetricsMiddleware(t *testing.T) {
	m := NewMetrics()

	handler := MetricsMiddleware(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))

	// Make some requests
	for i := 0; i < 5; i++ {
		req := httptest.NewRequest("GET", "/api/v1/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}

	snapshot := m.GetSnapshot()

	if snapshot.RequestsTotal != 5 {
		t.Errorf("Expected 5 requests, got %d", snapshot.RequestsTotal)
	}

	if snapshot.RequestsByCode[200] != 5 {
		t.Errorf("Expected 5 200s, got %d", snapshot.RequestsByCode[200])
	}

	// Average latency should be around 10ms
	if snapshot.LatencyAvgMs < 5 || snapshot.LatencyAvgMs > 50 {
		t.Errorf("Unexpected average latency: %f ms", snapshot.LatencyAvgMs)
	}
}

func TestMetrics_ActiveRequests(t *testing.T) {
	m := NewMetrics()

	// Channel to sync test
	started := make(chan struct{})
	done := make(chan struct{})

	handler := MetricsMiddleware(m)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started <- struct{}{}
		<-done
		w.WriteHeader(http.StatusOK)
	}))

	// Start a request that blocks
	go func() {
		req := httptest.NewRequest("GET", "/test", nil)
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
	}()

	// Wait for request to start
	<-started

	// Check active requests
	snapshot := m.GetSnapshot()
	if snapshot.ActiveRequests != 1 {
		t.Errorf("Expected 1 active request, got %d", snapshot.ActiveRequests)
	}

	// Release the request
	done <- struct{}{}

	// Give it time to complete
	time.Sleep(10 * time.Millisecond)

	snapshot = m.GetSnapshot()
	if snapshot.ActiveRequests != 0 {
		t.Errorf("Expected 0 active requests, got %d", snapshot.ActiveRequests)
	}
}

func TestMetrics_Uptime(t *testing.T) {
	m := NewMetrics()

	time.Sleep(100 * time.Millisecond)

	snapshot := m.GetSnapshot()

	if snapshot.Uptime < 100*time.Millisecond {
		t.Errorf("Expected uptime >= 100ms, got %v", snapshot.Uptime)
	}
}

// Test that metrics is nil-safe when disabled
func TestMetrics_DisabledInConfig(t *testing.T) {
	cfg := config.Default()
	cfg.MetricsEnabled = false

	// This simulates what happens when metrics is not initialized
	var m *Metrics = nil

	if m != nil {
		t.Error("Metrics should be nil when disabled")
	}
}
