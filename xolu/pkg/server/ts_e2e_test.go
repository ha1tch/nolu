// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// ts_e2e_test.go
//
// End-to-end tests for the timeseries HTTP API. Every test goes through
// httptest.Server — no direct store access. Covers full workflows, multi-
// tenant isolation, partial-prefix queries, batch semantics, and ordering.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/rs/zerolog"
)

// --- Harness ---

type tsEnv struct {
	ts     *httptest.Server
	srv    *server.Server
	tmpDir string
	t      *testing.T
}

func setupTSServer(t *testing.T, overrides func(*config.Config)) *tsEnv {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "olu-ts-e2e-*")
	if err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{
		Host:             "localhost",
		Port:             0,
		BaseDir:          tmpDir,
		Schema:           "test_schema",
		SchemaDir:        tmpDir + "/test_schema",
		StorageType:      "sqlite",
		CacheType:        "memory",
		CacheTTL:         300,
		MaxEntitySize:    1048576,
		PatchNullBehavior: "store",
		TenantMode:       "strict",
		TenantAutoRegister: true,

		// Timeseries
		TimeseriesEnabled:        true,
		TSMemtableSize:           4 * 1024 * 1024,
		TSBlockSize:              4096,
		TSCompression:            "snappy",
		TSL0CompactionThreshold:  4,
		TSMaxOpenFiles:           50,
		TSDefaultRetentionDays:   90,
		TSCompactionIntervalSecs: 3600,
		TSRetentionEnabled:       false,

		// Guardrails — generous defaults; individual tests override.
		TSQueryTimeoutSecs:  30,
		TSMaxQueryEvents:    10000,
		TSMaxScanEvents:     500000,
		TSMaxRangeDays:      366,
		TSMaxBatchSize:      5000,
		TSMaxResponseBytes:  10 * 1024 * 1024,
	}

	if overrides != nil {
		overrides(cfg)
	}

	os.MkdirAll(cfg.SchemaDir, 0755)

	store, err := storage.NewStore("sqlite", map[string]interface{}{
		"db_path": tmpDir + "/olu.db",
	})
	if err != nil {
		os.RemoveAll(tmpDir)
		t.Fatal(err)
	}

	memCache := cache.NewMemoryCache(1000, time.Duration(cfg.CacheTTL)*time.Second)
	schemaPath := cfg.SchemaDir + "/_schemas"
	os.MkdirAll(schemaPath, 0755)
	validator := validation.NewJSONSchemaValidator(schemaPath)
	logger := zerolog.New(os.Stdout).Level(zerolog.Disabled)

	srv := server.New(cfg, store, memCache, nil, nil, validator, logger)
	ts := httptest.NewServer(srv.Handler())

	env := &tsEnv{ts: ts, srv: srv, tmpDir: tmpDir, t: t}
	t.Cleanup(func() {
		ts.Close()
		os.RemoveAll(tmpDir)
	})
	return env
}

// registerTenant pre-registers a tenant name in the registry so strict-mode
// middleware can resolve it. Must be called before any HTTP call that uses
// that tenant.
func (e *tsEnv) registerTenant(name string) {
	e.t.Helper()
	_, err := e.srv.TenantRegistry().GetOrRegister(context.Background(), name)
	if err != nil {
		e.t.Fatalf("registerTenant %q: %v", name, err)
	}
}

func (e *tsEnv) tsURL(tenant, path string) string {
	return fmt.Sprintf("%s/api/v1/tenant/%s/ts%s", e.ts.URL, tenant, path)
}

func (e *tsEnv) do(method, url string, body interface{}) (int, map[string]interface{}) {
	e.t.Helper()
	return doJSONRequest(e.t, method, url, body)
}

// provision provisions timeseries for a tenant and asserts 200.
func (e *tsEnv) provision(tenant string) {
	e.t.Helper()
	status, result := e.do("POST", e.tsURL(tenant, "/provision"), nil)
	if status != http.StatusCreated {
		e.t.Fatalf("provision %s: got %d: %v", tenant, status, result)
	}
}

// defineTimeline defines a timeline and returns its ID. Asserts 201.
func (e *tsEnv) defineTimeline(tenant string, body map[string]interface{}) int {
	e.t.Helper()
	status, result := e.do("POST", e.tsURL(tenant, "/timelines"), body)
	if status != http.StatusCreated {
		e.t.Fatalf("defineTimeline: got %d: %v", status, result)
	}
	id, ok := result["id"].(float64)
	if !ok {
		e.t.Fatalf("defineTimeline: no id in response: %v", result)
	}
	return int(id)
}

// appendEvent appends a single event and asserts 201.
func (e *tsEnv) appendEvent(tenant string, body map[string]interface{}) {
	e.t.Helper()
	status, result := e.do("POST", e.tsURL(tenant, "/events"), body)
	if status != http.StatusCreated {
		e.t.Fatalf("appendEvent: got %d: %v", status, result)
	}
}

// --- Tests ---

// TestTSE2E_HappyPath walks the full lifecycle via HTTP.
func TestTSE2E_HappyPath(t *testing.T) {
	env := setupTSServer(t, nil)
	const tenant = "acme"
	env.registerTenant(tenant)
	env.provision(tenant)

	// Define a timeline.
	env.defineTimeline(tenant, map[string]interface{}{
		"id":   1,
		"dims": 2,
		"name": "temperature",
	})

	// Verify it appears in the list.
	listStatus, timelines := doJSONArray(t, "GET", env.tsURL(tenant, "/timelines"), nil)
	if listStatus != http.StatusOK {
		t.Fatalf("list timelines: got %d", listStatus)
	}
	if len(timelines) != 1 {
		t.Fatalf("got %d timelines, want 1", len(timelines))
	}

	// Append single event.
	ts0 := time.Now().UTC().Truncate(time.Second)
	env.appendEvent(tenant, map[string]interface{}{
		"timeline": 1,
		"dims":     []interface{}{42, 7},
		"time":     ts0.Format(time.RFC3339),
		"nums":     []interface{}{22.5},
	})

	// Append batch.
	batch := make([]interface{}, 3)
	for i := range batch {
		batch[i] = map[string]interface{}{
			"timeline": 1,
			"dims":     []interface{}{42, 7},
			"time":     ts0.Add(time.Duration(i+1) * time.Minute).Format(time.RFC3339Nano),
			"nums":     []interface{}{float64(20 + i)},
		}
	}
	var status int
	var result map[string]interface{}
	status, result = env.do("POST", env.tsURL(tenant, "/events/batch"), map[string]interface{}{
		"events": batch,
	})
	if status != http.StatusOK {
		t.Fatalf("batch append: got %d: %v", status, result)
	}
	accepted, _ := result["accepted"].(float64)
	if int(accepted) != 3 {
		t.Errorf("batch accepted %d, want 3", int(accepted))
	}

	// Query range.
	from := ts0.Add(-time.Second).Format(time.RFC3339)
	to := ts0.Add(10 * time.Minute).Format(time.RFC3339)
	qURL := fmt.Sprintf("%s?timeline=1&dims=42,7&from=%s&to=%s", env.tsURL(tenant, "/events"), from, to)
	status, result = env.do("GET", qURL, nil)
	if status != http.StatusOK {
		t.Fatalf("query range: got %d: %v", status, result)
	}
	count, _ := result["count"].(float64)
	if int(count) != 4 {
		t.Errorf("query range: count %d, want 4", int(count))
	}

	// Latest.
	latestURL := fmt.Sprintf("%s?timeline=1&dims=42,7&n=2", env.tsURL(tenant, "/events/latest"))
	status, result = env.do("GET", latestURL, nil)
	if status != http.StatusOK {
		t.Fatalf("latest: got %d: %v", status, result)
	}
	events, _ := result["events"].([]interface{})
	if len(events) != 2 {
		t.Errorf("latest: got %d events, want 2", len(events))
	}

	// Aggregate scalar — no interval means scalar result (value + count at top level).
	status, result = env.do("POST", env.tsURL(tenant, "/aggregate"), map[string]interface{}{
		"timeline":  1,
		"dims":      []interface{}{42, 7},
		"from":      from,
		"to":        to,
		"num_field": 0,
		"function":  "count",
	})
	if status != http.StatusOK {
		t.Fatalf("aggregate: got %d: %v", status, result)
	}
	aggCount, _ := result["count"].(float64)
	if int(aggCount) != 4 {
		t.Errorf("aggregate count: got %d, want 4", int(aggCount))
	}

	// Stats.
	status, result = env.do("GET", env.tsURL(tenant, "/stats"), nil)
	if status != http.StatusOK {
		t.Fatalf("stats: got %d: %v", status, result)
	}

	// Timeline stats.
	status, result = env.do("GET", env.tsURL(tenant, "/stats/1"), nil)
	if status != http.StatusOK {
		t.Fatalf("timeline stats: got %d: %v", status, result)
	}
	total, _ := result["total_events"].(float64)
	if int(total) != 4 {
		t.Errorf("timeline stats total_events: %d, want 4", int(total))
	}

	// Retention.
	status, result = env.do("GET", env.tsURL(tenant, "/retention"), nil)
	if status != http.StatusOK {
		t.Fatalf("retention: got %d: %v", status, result)
	}
}

// TestTSE2E_MultiTenantIsolation verifies that events written to one tenant
// are not visible to another.
func TestTSE2E_MultiTenantIsolation(t *testing.T) {
	env := setupTSServer(t, nil)

	for _, tenant := range []string{"alpha", "beta"} {
		env.registerTenant(tenant)
		env.provision(tenant)
		env.defineTimeline(tenant, map[string]interface{}{
			"id": 1, "dims": 1, "name": "readings",
		})
	}

	ts0 := time.Now().UTC()
	env.appendEvent("alpha", map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1},
		"time": ts0.Format(time.RFC3339Nano), "nums": []interface{}{100.0},
	})
	// beta gets no events.

	from := ts0.Add(-time.Minute).Format(time.RFC3339)
	to := ts0.Add(time.Minute).Format(time.RFC3339)

	// alpha sees its event.
	alphaURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("alpha", "/events"), from, to)
	status, result := env.do("GET", alphaURL, nil)
	if status != http.StatusOK {
		t.Fatalf("alpha query: %d: %v", status, result)
	}
	count, _ := result["count"].(float64)
	if int(count) != 1 {
		t.Errorf("alpha: got %d events, want 1", int(count))
	}

	// beta sees nothing.
	betaURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("beta", "/events"), from, to)
	status, result = env.do("GET", betaURL, nil)
	if status != http.StatusOK {
		t.Fatalf("beta query: %d: %v", status, result)
	}
	count, _ = result["count"].(float64)
	if int(count) != 0 {
		t.Errorf("beta: got %d events, want 0 (tenant isolation violated)", int(count))
	}
}

// TestTSE2E_TimelineLifecycle exercises define, get, list, update (name and
// retention), and verifies dims immutability after first write via HTTP.
func TestTSE2E_TimelineLifecycle(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("corp")
	env.provision("corp")

	// Define.
	env.defineTimeline("corp", map[string]interface{}{
		"id": 5, "dims": 2, "name": "vibration", "retention_days": 14,
	})

	// Get.
	status, result := env.do("GET", env.tsURL("corp", "/timelines/5"), nil)
	if status != http.StatusOK {
		t.Fatalf("get timeline: %d: %v", status, result)
	}
	if result["name"] != "vibration" {
		t.Errorf("name %q, want vibration", result["name"])
	}

	// Update name.
	status, result = env.do("PATCH", env.tsURL("corp", "/timelines/5"), map[string]interface{}{
		"name": "vib-updated",
	})
	if status != http.StatusOK {
		t.Fatalf("patch timeline: %d: %v", status, result)
	}

	// Verify update.
	_, result = env.do("GET", env.tsURL("corp", "/timelines/5"), nil)
	if result["name"] != "vib-updated" {
		t.Errorf("after patch: name %q, want vib-updated", result["name"])
	}

	// Write one event to lock dims.
	env.appendEvent("corp", map[string]interface{}{
		"timeline": 5,
		"dims":     []interface{}{1, 2},
		"time":     time.Now().UTC().Format(time.RFC3339Nano),
	})

	// Attempt to redefine with different dims — must fail.
	status, _ = env.do("POST", env.tsURL("corp", "/timelines"), map[string]interface{}{
		"id": 5, "dims": 3,
	})
	if status == http.StatusCreated {
		t.Error("expected failure when changing dims after first write, got 201")
	}
}

// TestTSE2E_PartialPrefixQuery writes events with two different d1 values
// and verifies that a partial-prefix query (d0 only) returns both series
// but respects the time bounds.
func TestTSE2E_PartialPrefixQuery(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("sensor")
	env.provision("sensor")
	env.defineTimeline("sensor", map[string]interface{}{
		"id": 1, "dims": 2, "name": "sensors",
	})

	base := time.Now().UTC().Truncate(time.Second)

	// d0=10, d1=1: two events within query window.
	// d0=10, d1=2: one event within window, one outside.
	events := []struct {
		d0, d1 int
		offset time.Duration
	}{
		{10, 1, 10 * time.Minute},
		{10, 1, 20 * time.Minute},
		{10, 2, 15 * time.Minute},
		{10, 2, 90 * time.Minute}, // outside window
	}
	for _, e := range events {
		env.appendEvent("sensor", map[string]interface{}{
			"timeline": 1,
			"dims":     []interface{}{e.d0, e.d1},
			"time":     base.Add(e.offset).Format(time.RFC3339Nano),
		})
	}

	from := base.Format(time.RFC3339)
	to := base.Add(60 * time.Minute).Format(time.RFC3339)
	// Partial prefix: dims=10 only (d0=10, any d1).
	qURL := fmt.Sprintf("%s?timeline=1&dims=10&from=%s&to=%s", env.tsURL("sensor", "/events"), from, to)
	status, result := env.do("GET", qURL, nil)
	if status != http.StatusOK {
		t.Fatalf("partial prefix query: %d: %v", status, result)
	}
	count, _ := result["count"].(float64)
	if int(count) != 3 {
		t.Errorf("partial prefix: got %d events, want 3", int(count))
	}
}

// TestTSE2E_QueryOrdering verifies that asc and desc results are mirrors.
func TestTSE2E_QueryOrdering(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("order")
	env.provision("order")
	env.defineTimeline("order", map[string]interface{}{
		"id": 1, "dims": 1, "name": "seq",
	})

	base := time.Now().UTC().Truncate(time.Second)
	for i := 0; i < 5; i++ {
		env.appendEvent("order", map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1},
			"time": base.Add(time.Duration(i) * time.Minute).Format(time.RFC3339Nano),
			"nums": []interface{}{float64(i)},
		})
	}

	from := base.Add(-time.Second).Format(time.RFC3339)
	to := base.Add(10 * time.Minute).Format(time.RFC3339)

	ascURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s&order=asc", env.tsURL("order", "/events"), from, to)
	descURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s&order=desc", env.tsURL("order", "/events"), from, to)

	_, ascResult := env.do("GET", ascURL, nil)
	_, descResult := env.do("GET", descURL, nil)

	ascEvents, _ := ascResult["events"].([]interface{})
	descEvents, _ := descResult["events"].([]interface{})

	if len(ascEvents) != 5 || len(descEvents) != 5 {
		t.Fatalf("asc=%d desc=%d events, want 5 each", len(ascEvents), len(descEvents))
	}

	for i := 0; i < 5; i++ {
		aEvt := ascEvents[i].(map[string]interface{})
		dEvt := descEvents[4-i].(map[string]interface{})
		if aEvt["time"] != dEvt["time"] {
			t.Errorf("position %d: asc time %v != desc[%d] time %v", i, aEvt["time"], 4-i, dEvt["time"])
		}
	}
}

// TestTSE2E_BatchAtomicity verifies that a batch with one invalid event is
// entirely rejected — no partial writes.
func TestTSE2E_BatchAtomicity(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("atom")
	env.provision("atom")
	env.defineTimeline("atom", map[string]interface{}{
		"id": 1, "dims": 1, "name": "readings",
	})

	base := time.Now().UTC().Truncate(time.Second)

	// Events 0 and 2 are valid; event 1 has wrong dim count (dims=2 for a dims=1 timeline).
	batch := []interface{}{
		map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1},
			"time": base.Format(time.RFC3339Nano), "nums": []interface{}{1.0},
		},
		map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1, 2}, // wrong: dims=2 for dims=1 timeline
			"time": base.Add(time.Minute).Format(time.RFC3339Nano),
		},
		map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1},
			"time": base.Add(2 * time.Minute).Format(time.RFC3339Nano), "nums": []interface{}{3.0},
		},
	}
	status, _ := env.do("POST", env.tsURL("atom", "/events/batch"), map[string]interface{}{
		"events": batch,
	})
	if status == http.StatusCreated {
		t.Fatal("batch with invalid event should be rejected, got 201")
	}

	// Verify no events were written.
	from := base.Add(-time.Second).Format(time.RFC3339)
	to := base.Add(10 * time.Minute).Format(time.RFC3339)
	qURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("atom", "/events"), from, to)
	_, result := env.do("GET", qURL, nil)
	count, _ := result["count"].(float64)
	if int(count) != 0 {
		t.Errorf("after rejected batch: found %d events, want 0 (atomicity violated)", int(count))
	}
}

// TestTSE2E_AggregateWindowed verifies that bucketed aggregation returns the
// correct number of buckets and correct per-bucket sums.
func TestTSE2E_AggregateWindowed(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("agg")
	env.provision("agg")
	env.defineTimeline("agg", map[string]interface{}{
		"id": 1, "dims": 1, "name": "power",
	})

	// 2 events in hour 0 (nums: 10, 20), 2 events in hour 1 (nums: 30, 40).
	base := time.Now().UTC().Truncate(time.Hour)
	readings := []struct {
		offset time.Duration
		value  float64
	}{
		{5 * time.Minute, 10},
		{15 * time.Minute, 20},
		{time.Hour + 5*time.Minute, 30},
		{time.Hour + 15*time.Minute, 40},
	}
	for _, r := range readings {
		env.appendEvent("agg", map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1},
			"time": base.Add(r.offset).Format(time.RFC3339Nano),
			"nums": []interface{}{r.value},
		})
	}

	status, result := env.do("POST", env.tsURL("agg", "/aggregate"), map[string]interface{}{
		"timeline":  1,
		"dims":      []interface{}{1},
		"from":      base.Format(time.RFC3339),
		"to":        base.Add(2 * time.Hour).Format(time.RFC3339),
		"num_field": 0,
		"function":  "sum",
		"interval":  "1h",
	})
	if status != http.StatusOK {
		t.Fatalf("aggregate windowed: %d: %v", status, result)
	}

	buckets, _ := result["buckets"].([]interface{})
	if len(buckets) != 2 {
		t.Fatalf("got %d buckets, want 2", len(buckets))
	}

	b0 := buckets[0].(map[string]interface{})
	b1 := buckets[1].(map[string]interface{})
	if v, _ := b0["value"].(float64); v != 30 {
		t.Errorf("bucket[0] sum: %v, want 30", v)
	}
	if v, _ := b1["value"].(float64); v != 70 {
		t.Errorf("bucket[1] sum: %v, want 70", v)
	}
}

// TestTSE2E_UnprovisionedTenant verifies that TS operations on a tenant that
// hasn't been provisioned return an appropriate error.
func TestTSE2E_UnprovisionedTenant(t *testing.T) {
	env := setupTSServer(t, nil)
	// Register the tenant so the middleware passes, but do NOT call provision —
	// the TS handler should return OLU-TS003 (not provisioned for timeseries).
	env.registerTenant("ghost")
	status, result := env.do("POST", env.tsURL("ghost", "/timelines"), map[string]interface{}{
		"id": 1, "dims": 1,
	})
	if status == http.StatusCreated {
		t.Fatalf("expected error for unprovisioned tenant, got 201")
	}
	errObj, _ := result["error"].(map[string]interface{})
	code, _ := errObj["code"].(string)
	if code != "OLU-TS003" {
		t.Errorf("expected OLU-TS003 for unprovisioned tenant, got %q", code)
	}
}

// TestTSE2E_TSDisabledReturns404 verifies that TS routes return 404 when
// TimeseriesEnabled is false (the route is not registered at all).
func TestTSE2E_TSDisabledReturns404(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TimeseriesEnabled = false
	})
	// With TS disabled the route simply doesn't exist.
	status, _ := doJSONRequest(t,
		"POST",
		fmt.Sprintf("%s/api/v1/tenant/x/ts/provision", env.ts.URL),
		nil,
	)
	if status != http.StatusNotFound {
		t.Errorf("TS disabled: expected 404, got %d", status)
	}
}

// --- helpers used by multiple ts test files ---

// doJSONArray makes an HTTP request and unmarshals a JSON array response body.
func doJSONArray(t *testing.T, method, url string, body interface{}) (int, []interface{}) {
	t.Helper()
	var buf []byte
	if body != nil {
		var err error
		buf, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}
	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out []interface{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func doJSONRequest(t *testing.T, method, url string, body interface{}) (int, map[string]interface{}) {
	t.Helper()
	var buf []byte
	if body != nil {
		var err error
		buf, err = json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
	}

	var bodyReader io.Reader
	if body != nil {
		bodyReader = bytes.NewReader(buf)
	}
	req, err := http.NewRequest(method, url, bodyReader)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var out map[string]interface{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}
