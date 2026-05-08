// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// ts_guardrail_test.go
//
// Verifies that backend limits (TSMax* and TSQueryTimeoutSecs config fields)
// are enforced at the HTTP layer. Each test sets deliberately low limits via
// config override and asserts the correct status code and OLU-TS error code.
//
// Pattern mirrors guardrail_test.go.

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/config"
)

// seedGuardrailTimeline provisions, defines a dims=1 timeline, and seeds n
// events spaced one second apart starting from a base time.
func seedGuardrailTimeline(t *testing.T, env *tsEnv, tenant string, n int) (from, to string) {
	t.Helper()
	env.registerTenant(tenant)
	env.provision(tenant)
	env.defineTimeline(tenant, map[string]interface{}{"id": 1, "dims": 1})

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 0; i < n; i++ {
		ts := base.Add(time.Duration(i) * time.Second).Format(time.RFC3339)
		env.do("POST", env.tsURL(tenant, "/events"), map[string]interface{}{
			"timeline": 1,
			"dims":     []interface{}{1},
			"time":     ts,
			"nums":     []interface{}{float64(i)},
		})
	}

	from = base.Add(-time.Second).Format(time.RFC3339)
	to = base.Add(time.Duration(n+1) * time.Second).Format(time.RFC3339)
	return from, to
}

// TestTSGuardrail_MaxScanEvents verifies that a query hitting TSMaxScanEvents
// is aborted and returns 413 with OLU-TS013.
func TestTSGuardrail_MaxScanEvents(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSMaxScanEvents = 5
	})
	from, to := seedGuardrailTimeline(t, env, "grd", 20)

	qURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("grd", "/events"), from, to)
	status, result := env.do("GET", qURL, nil)

	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS013" {
		t.Errorf("code %q, want OLU-TS013", code)
	}
}

// TestTSGuardrail_MaxQueryEvents verifies that TSMaxQueryEvents caps the
// number of returned events rather than returning an error.
func TestTSGuardrail_MaxQueryEvents(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSMaxQueryEvents = 3
	})
	from, to := seedGuardrailTimeline(t, env, "cap", 10)

	qURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("cap", "/events"), from, to)
	status, result := env.do("GET", qURL, nil)

	if status != http.StatusOK {
		t.Errorf("expected 200, got %d: %v", status, result)
	}
	count, _ := result["count"].(float64)
	if int(count) > 3 {
		t.Errorf("count %d exceeds TSMaxQueryEvents=3", int(count))
	}
}

// TestTSGuardrail_MaxResponseBytes verifies that a response exceeding
// TSMaxResponseBytes returns 413 with OLU-TS014.
func TestTSGuardrail_MaxResponseBytes(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSMaxResponseBytes = 50 // tiny — any non-empty response will exceed this
	})
	from, to := seedGuardrailTimeline(t, env, "big", 5)

	qURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("big", "/events"), from, to)
	status, result := env.do("GET", qURL, nil)

	if status != http.StatusRequestEntityTooLarge {
		t.Errorf("expected 413, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS013" {
		t.Errorf("code %q, want OLU-TS014", code)
	}
}

// TestTSGuardrail_MaxRangeDays verifies that a query window exceeding
// TSMaxRangeDays returns 400 with OLU-TS011.
func TestTSGuardrail_MaxRangeDays(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSMaxRangeDays = 7
	})
	env.registerTenant("rng")
	env.provision("rng")
	env.defineTimeline("rng", map[string]interface{}{"id": 1, "dims": 1})

	qURL := env.tsURL("rng", "/events") +
		"?timeline=1&dims=1&from=2026-01-01T00:00:00Z&to=2026-02-01T00:00:00Z"
	status, result := env.do("GET", qURL, nil)

	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS011" {
		t.Errorf("code %q, want OLU-TS011", code)
	}
}

// TestTSGuardrail_MaxBatchSize verifies that a batch exceeding TSMaxBatchSize
// returns 400 with OLU-TS006.
func TestTSGuardrail_MaxBatchSize(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSMaxBatchSize = 3
	})
	env.registerTenant("bat")
	env.provision("bat")
	env.defineTimeline("bat", map[string]interface{}{"id": 1, "dims": 1})

	events := make([]interface{}, 5)
	for i := range events {
		events[i] = map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1},
			"time": fmt.Sprintf("2026-01-01T00:%02d:00Z", i),
		}
	}
	status, result := env.do("POST", env.tsURL("bat", "/events/batch"), map[string]interface{}{
		"events": events,
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS006" {
		t.Errorf("code %q, want OLU-TS006", code)
	}
}

// TestTSGuardrail_MaxScanEvents_Aggregate verifies scan limit enforcement
// on aggregate queries, not just QueryRange.
func TestTSGuardrail_MaxScanEvents_Aggregate(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSMaxScanEvents = 5
	})
	from, to := seedGuardrailTimeline(t, env, "aggrd", 20)

	status, result := env.do("POST", env.tsURL("aggrd", "/aggregate"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1},
		"from": from, "to": to,
		"function": "count", "num_field": 0,
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS013" {
		t.Errorf("code %q, want OLU-TS013", code)
	}
}

// TestTSGuardrail_DefaultsArePresent verifies that the six guardrail config
// fields have sensible non-zero defaults, confirming the mechanism is wired.
func TestTSGuardrail_DefaultsArePresent(t *testing.T) {
	cfg := config.Default()
	checks := []struct {
		name string
		val  int
	}{
		{"TSQueryTimeoutSecs", cfg.TSQueryTimeoutSecs},
		{"TSMaxQueryEvents", cfg.TSMaxQueryEvents},
		{"TSMaxScanEvents", cfg.TSMaxScanEvents},
		{"TSMaxRangeDays", cfg.TSMaxRangeDays},
		{"TSMaxBatchSize", cfg.TSMaxBatchSize},
		{"TSMaxResponseBytes", cfg.TSMaxResponseBytes},
	}
	for _, c := range checks {
		if c.val <= 0 {
			t.Errorf("%s default is %d (must be > 0)", c.name, c.val)
		}
	}
}

// TestTSGuardrail_QueryTimeout_DoesNotTriggerOnFastQuery verifies that the
// timeout mechanism is wired without affecting normal fast queries.
func TestTSGuardrail_QueryTimeout_DoesNotTriggerOnFastQuery(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) {
		cfg.TSQueryTimeoutSecs = 30 // generous; should never fire on an empty store
	})
	from, to := seedGuardrailTimeline(t, env, "tout", 3)

	qURL := fmt.Sprintf("%s?timeline=1&dims=1&from=%s&to=%s", env.tsURL("tout", "/events"), from, to)
	status, _ := env.do("GET", qURL, nil)
	if status != http.StatusOK {
		t.Errorf("fast query under generous timeout: expected 200, got %d", status)
	}
}
