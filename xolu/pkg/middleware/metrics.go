// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Metrics collects server metrics for observability
type Metrics struct {
	mu sync.RWMutex

	// Request counters
	requestsTotal  uint64
	requestsByPath map[string]uint64
	requestsByCode map[int]uint64
	requestErrors  uint64

	// Latency tracking (in microseconds)
	latencySum     uint64
	latencyCount   uint64
	latencyBuckets map[string]uint64 // "0.001", "0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10"

	// Active connections
	activeRequests int64

	// Entity operation counters
	entityCreates uint64
	entityReads   uint64
	entityUpdates uint64
	entityDeletes uint64
	entityLists   uint64

	// Cache stats
	cacheHits   uint64
	cacheMisses uint64

	// Search stats
	searchQueries uint64
	oqlQueries    uint64
	graphQueries  uint64

	// Start time for uptime calculation
	startTime time.Time
}

// NewMetrics creates a new metrics collector
func NewMetrics() *Metrics {
	return &Metrics{
		requestsByPath: make(map[string]uint64),
		requestsByCode: make(map[int]uint64),
		latencyBuckets: map[string]uint64{
			"0.001": 0, "0.005": 0, "0.01": 0, "0.025": 0,
			"0.05": 0, "0.1": 0, "0.25": 0, "0.5": 0,
			"1": 0, "2.5": 0, "5": 0, "10": 0, "+Inf": 0,
		},
		startTime: time.Now(),
	}
}

// MetricsMiddleware creates a middleware that collects request metrics
func MetricsMiddleware(m *Metrics) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()

			// Track active requests
			atomic.AddInt64(&m.activeRequests, 1)
			defer atomic.AddInt64(&m.activeRequests, -1)

			// Wrap response writer to capture status code
			wrapped := &responseWriter{ResponseWriter: w, statusCode: http.StatusOK}

			// Call next handler
			next.ServeHTTP(wrapped, r)

			// Record metrics
			duration := time.Since(start)
			m.recordRequest(r.URL.Path, wrapped.statusCode, duration)
		})
	}
}

// responseWriter wraps http.ResponseWriter to capture status code
type responseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.statusCode = code
	rw.ResponseWriter.WriteHeader(code)
}

// recordRequest records metrics for a completed request
func (m *Metrics) recordRequest(path string, statusCode int, duration time.Duration) {
	atomic.AddUint64(&m.requestsTotal, 1)

	// Record latency
	durationSec := duration.Seconds()
	atomic.AddUint64(&m.latencySum, uint64(duration.Microseconds()))
	atomic.AddUint64(&m.latencyCount, 1)

	m.mu.Lock()
	defer m.mu.Unlock()

	// Count by path (normalize to avoid cardinality explosion)
	normalizedPath := normalizePath(path)
	m.requestsByPath[normalizedPath]++

	// Count by status code
	m.requestsByCode[statusCode]++

	// Record latency bucket
	switch {
	case durationSec <= 0.001:
		m.latencyBuckets["0.001"]++
	case durationSec <= 0.005:
		m.latencyBuckets["0.005"]++
	case durationSec <= 0.01:
		m.latencyBuckets["0.01"]++
	case durationSec <= 0.025:
		m.latencyBuckets["0.025"]++
	case durationSec <= 0.05:
		m.latencyBuckets["0.05"]++
	case durationSec <= 0.1:
		m.latencyBuckets["0.1"]++
	case durationSec <= 0.25:
		m.latencyBuckets["0.25"]++
	case durationSec <= 0.5:
		m.latencyBuckets["0.5"]++
	case durationSec <= 1:
		m.latencyBuckets["1"]++
	case durationSec <= 2.5:
		m.latencyBuckets["2.5"]++
	case durationSec <= 5:
		m.latencyBuckets["5"]++
	case durationSec <= 10:
		m.latencyBuckets["10"]++
	default:
		m.latencyBuckets["+Inf"]++
	}

	// Track errors
	if statusCode >= 400 {
		atomic.AddUint64(&m.requestErrors, 1)
	}
}

// normalizePath normalizes a path to prevent cardinality explosion
// Only replaces path segments that are purely numeric (e.g., /users/123 -> /users/:id)
// Does not replace numbers within segments (e.g., /api/v1 stays /api/v1)
func normalizePath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if part == "" {
			continue
		}
		// Check if the entire segment is numeric
		isNumeric := true
		for _, c := range part {
			if c < '0' || c > '9' {
				isNumeric = false
				break
			}
		}
		if isNumeric && len(part) > 0 {
			parts[i] = ":id"
		}
	}
	return strings.Join(parts, "/")
}

// RecordEntityCreate records an entity creation
func (m *Metrics) RecordEntityCreate() {
	atomic.AddUint64(&m.entityCreates, 1)
}

// RecordEntityRead records an entity read
func (m *Metrics) RecordEntityRead() {
	atomic.AddUint64(&m.entityReads, 1)
}

// RecordEntityUpdate records an entity update
func (m *Metrics) RecordEntityUpdate() {
	atomic.AddUint64(&m.entityUpdates, 1)
}

// RecordEntityDelete records an entity deletion
func (m *Metrics) RecordEntityDelete() {
	atomic.AddUint64(&m.entityDeletes, 1)
}

// RecordEntityList records an entity list operation
func (m *Metrics) RecordEntityList() {
	atomic.AddUint64(&m.entityLists, 1)
}

// RecordCacheHit records a cache hit
func (m *Metrics) RecordCacheHit() {
	atomic.AddUint64(&m.cacheHits, 1)
}

// RecordCacheMiss records a cache miss
func (m *Metrics) RecordCacheMiss() {
	atomic.AddUint64(&m.cacheMisses, 1)
}

// RecordSearchQuery records a search query
func (m *Metrics) RecordSearchQuery() {
	atomic.AddUint64(&m.searchQueries, 1)
}

// RecordOQLQuery records an OQL query
func (m *Metrics) RecordOQLQuery() {
	atomic.AddUint64(&m.oqlQueries, 1)
}

// RecordGraphQuery records a graph query
func (m *Metrics) RecordGraphQuery() {
	atomic.AddUint64(&m.graphQueries, 1)
}

// GetSnapshot returns a snapshot of current metrics
func (m *Metrics) GetSnapshot() MetricsSnapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	// Copy maps
	pathCounts := make(map[string]uint64, len(m.requestsByPath))
	for k, v := range m.requestsByPath {
		pathCounts[k] = v
	}

	codeCounts := make(map[int]uint64, len(m.requestsByCode))
	for k, v := range m.requestsByCode {
		codeCounts[k] = v
	}

	buckets := make(map[string]uint64, len(m.latencyBuckets))
	for k, v := range m.latencyBuckets {
		buckets[k] = v
	}

	latencyCount := atomic.LoadUint64(&m.latencyCount)
	var avgLatency float64
	if latencyCount > 0 {
		avgLatency = float64(atomic.LoadUint64(&m.latencySum)) / float64(latencyCount) / 1000 // Convert to ms
	}

	return MetricsSnapshot{
		Uptime:         time.Since(m.startTime),
		RequestsTotal:  atomic.LoadUint64(&m.requestsTotal),
		RequestsByPath: pathCounts,
		RequestsByCode: codeCounts,
		RequestErrors:  atomic.LoadUint64(&m.requestErrors),
		ActiveRequests: atomic.LoadInt64(&m.activeRequests),
		LatencyAvgMs:   avgLatency,
		LatencySumSec:  float64(atomic.LoadUint64(&m.latencySum)) / 1e6, // microseconds -> seconds
		LatencyCount:   latencyCount,
		LatencyBuckets: buckets,
		EntityCreates:  atomic.LoadUint64(&m.entityCreates),
		EntityReads:    atomic.LoadUint64(&m.entityReads),
		EntityUpdates:  atomic.LoadUint64(&m.entityUpdates),
		EntityDeletes:  atomic.LoadUint64(&m.entityDeletes),
		EntityLists:    atomic.LoadUint64(&m.entityLists),
		CacheHits:      atomic.LoadUint64(&m.cacheHits),
		CacheMisses:    atomic.LoadUint64(&m.cacheMisses),
		SearchQueries:  atomic.LoadUint64(&m.searchQueries),
		OQLQueries:     atomic.LoadUint64(&m.oqlQueries),
		GraphQueries:   atomic.LoadUint64(&m.graphQueries),
	}
}

// MetricsSnapshot is a point-in-time snapshot of metrics
type MetricsSnapshot struct {
	Uptime         time.Duration
	RequestsTotal  uint64
	RequestsByPath map[string]uint64
	RequestsByCode map[int]uint64
	RequestErrors  uint64
	ActiveRequests int64
	LatencyAvgMs   float64
	LatencySumSec  float64           // total latency in seconds (for Prometheus _sum)
	LatencyCount   uint64            // total observations (for Prometheus _count)
	LatencyBuckets map[string]uint64
	EntityCreates  uint64
	EntityReads    uint64
	EntityUpdates  uint64
	EntityDeletes  uint64
	EntityLists    uint64
	CacheHits      uint64
	CacheMisses    uint64
	SearchQueries  uint64
	OQLQueries     uint64
	GraphQueries   uint64
}

// PrometheusFormat returns metrics in Prometheus exposition format
func (m *Metrics) PrometheusFormat() string {
	snapshot := m.GetSnapshot()

	var result string

	// Uptime
	result += "# HELP olu_uptime_seconds Server uptime in seconds\n"
	result += "# TYPE olu_uptime_seconds gauge\n"
	result += "olu_uptime_seconds " + strconv.FormatFloat(snapshot.Uptime.Seconds(), 'f', 2, 64) + "\n\n"

	// Request totals
	result += "# HELP olu_requests_total Total number of HTTP requests\n"
	result += "# TYPE olu_requests_total counter\n"
	result += "olu_requests_total " + strconv.FormatUint(snapshot.RequestsTotal, 10) + "\n\n"

	// Requests by status code
	result += "# HELP olu_requests_by_status_total HTTP requests by status code\n"
	result += "# TYPE olu_requests_by_status_total counter\n"
	for code, count := range snapshot.RequestsByCode {
		result += "olu_requests_by_status_total{code=\"" + strconv.Itoa(code) + "\"} " + strconv.FormatUint(count, 10) + "\n"
	}
	result += "\n"

	// Request errors
	result += "# HELP olu_request_errors_total Total number of request errors (4xx/5xx)\n"
	result += "# TYPE olu_request_errors_total counter\n"
	result += "olu_request_errors_total " + strconv.FormatUint(snapshot.RequestErrors, 10) + "\n\n"

	// Active requests
	result += "# HELP olu_active_requests Current number of active requests\n"
	result += "# TYPE olu_active_requests gauge\n"
	result += "olu_active_requests " + strconv.FormatInt(snapshot.ActiveRequests, 10) + "\n\n"

	// Latency histogram
	result += "# HELP olu_request_duration_seconds Request duration histogram\n"
	result += "# TYPE olu_request_duration_seconds histogram\n"
	cumulative := uint64(0)
	bucketOrder := []string{"0.001", "0.005", "0.01", "0.025", "0.05", "0.1", "0.25", "0.5", "1", "2.5", "5", "10", "+Inf"}
	for _, bucket := range bucketOrder {
		cumulative += snapshot.LatencyBuckets[bucket]
		result += "olu_request_duration_seconds_bucket{le=\"" + bucket + "\"} " + strconv.FormatUint(cumulative, 10) + "\n"
	}
	result += "olu_request_duration_seconds_sum " + strconv.FormatFloat(snapshot.LatencySumSec, 'f', 6, 64) + "\n"
	result += "olu_request_duration_seconds_count " + strconv.FormatUint(snapshot.LatencyCount, 10) + "\n"
	result += "\n"

	// Average latency
	result += "# HELP olu_request_latency_avg_ms Average request latency in milliseconds\n"
	result += "# TYPE olu_request_latency_avg_ms gauge\n"
	result += "olu_request_latency_avg_ms " + strconv.FormatFloat(snapshot.LatencyAvgMs, 'f', 3, 64) + "\n\n"

	// Entity operations
	result += "# HELP olu_entity_operations_total Entity operations by type\n"
	result += "# TYPE olu_entity_operations_total counter\n"
	result += "olu_entity_operations_total{operation=\"create\"} " + strconv.FormatUint(snapshot.EntityCreates, 10) + "\n"
	result += "olu_entity_operations_total{operation=\"read\"} " + strconv.FormatUint(snapshot.EntityReads, 10) + "\n"
	result += "olu_entity_operations_total{operation=\"update\"} " + strconv.FormatUint(snapshot.EntityUpdates, 10) + "\n"
	result += "olu_entity_operations_total{operation=\"delete\"} " + strconv.FormatUint(snapshot.EntityDeletes, 10) + "\n"
	result += "olu_entity_operations_total{operation=\"list\"} " + strconv.FormatUint(snapshot.EntityLists, 10) + "\n\n"

	// Cache stats
	result += "# HELP olu_cache_total Cache hits and misses\n"
	result += "# TYPE olu_cache_total counter\n"
	result += "olu_cache_total{result=\"hit\"} " + strconv.FormatUint(snapshot.CacheHits, 10) + "\n"
	result += "olu_cache_total{result=\"miss\"} " + strconv.FormatUint(snapshot.CacheMisses, 10) + "\n\n"

	// Query stats
	result += "# HELP olu_queries_total Query operations by type\n"
	result += "# TYPE olu_queries_total counter\n"
	result += "olu_queries_total{type=\"search\"} " + strconv.FormatUint(snapshot.SearchQueries, 10) + "\n"
	result += "olu_queries_total{type=\"oql\"} " + strconv.FormatUint(snapshot.OQLQueries, 10) + "\n"
	result += "olu_queries_total{type=\"graph\"} " + strconv.FormatUint(snapshot.GraphQueries, 10) + "\n"

	return result
}
