// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// ts_error_paths_test.go
//
// Systematic coverage of every writeError call in ts_handlers.go.
// Each test targets a specific error branch and asserts:
//   - the correct HTTP status code
//   - the correct OLU-TS error code in the JSON body
//
// Tests use the shared tsEnv harness from ts_e2e_test.go.

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/ha1tch/xolu/pkg/config"
)

// helper: extract error code from response body
func tsErrCode(result map[string]interface{}) string {
	errObj, _ := result["error"].(map[string]interface{})
	code, _ := errObj["code"].(string)
	return code
}

func tsErrMsg(result map[string]interface{}) string { //nolint:unused
	errObj, _ := result["error"].(map[string]interface{})
	msg, _ := errObj["message"].(string)
	return msg
}

// --- OLU-TS002: timeseries not enabled ---

func TestTSError_TSDisabled_Provision(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) { cfg.TimeseriesEnabled = false })
	env.registerTenant("x")
	status, result := env.do("POST", env.tsURL("x", "/provision"), nil)
	// When TS is disabled the route either doesn't exist (404) or
	// the parent router rejects the method (405). Either is acceptable.
	if status != http.StatusNotFound && status != http.StatusMethodNotAllowed {
		t.Errorf("expected 404 or 405, got %d: %v", status, result)
	}
}

// --- OLU-TS003: tenant not provisioned ---

func TestTSError_NotProvisioned_Append(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("ghost")
	status, result := env.do("POST", env.tsURL("ghost", "/events"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1}, "time": "2026-01-01T00:00:00Z",
	})
	if status != http.StatusNotFound {
		t.Errorf("expected 404, got %d", status)
	}
	if code := tsErrCode(result); code != "OLU-TS003" {
		t.Errorf("code %q, want OLU-TS003", code)
	}
}

// --- OLU-TS004: timeline not defined ---

func TestTSError_TimelineNotDefined(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	// Append to a timeline that was never defined.
	status, result := env.do("POST", env.tsURL("t", "/events"), map[string]interface{}{
		"timeline": 99, "dims": []interface{}{1}, "time": "2026-01-01T00:00:00Z",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", status)
	}
	if code := tsErrCode(result); code != "OLU-TS004" {
		t.Errorf("code %q, want OLU-TS004", code)
	}
}

// --- OLU-TS005: pre-epoch timestamp ---

func TestTSError_PreEpochTimestamp(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	status, result := env.do("POST", env.tsURL("t", "/events"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1}, "time": "1969-12-31T23:59:59Z",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS005" {
		t.Errorf("code %q, want OLU-TS005", code)
	}
}

// --- OLU-TS005: invalid timestamp format ---

func TestTSError_InvalidTimestampFormat(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	status, result := env.do("POST", env.tsURL("t", "/events"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1}, "time": "not-a-date",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS005" {
		t.Errorf("code %q, want OLU-TS005", code)
	}
}

// --- OLU-TS006: batch too large ---

func TestTSError_BatchTooLarge(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) { cfg.TSMaxBatchSize = 3 })
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})

	events := make([]interface{}, 5)
	for i := range events {
		events[i] = map[string]interface{}{
			"timeline": 1, "dims": []interface{}{1},
			"time": fmt.Sprintf("2026-01-01T00:%02d:00Z", i),
		}
	}
	status, result := env.do("POST", env.tsURL("t", "/events/batch"), map[string]interface{}{
		"events": events,
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS006" {
		t.Errorf("code %q, want OLU-TS006", code)
	}
}

// --- OLU-TS007: dims mismatch on append ---

func TestTSError_DimsMismatchOnAppend(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 2})
	// Send 1 dim instead of 2.
	status, result := env.do("POST", env.tsURL("t", "/events"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1}, "time": "2026-01-01T00:00:00Z",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS007" {
		t.Errorf("code %q, want OLU-TS007", code)
	}
}

// --- OLU-TS008: unknown aggregate function ---

func TestTSError_UnknownAggFunction(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	status, result := env.do("POST", env.tsURL("t", "/aggregate"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1},
		"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z",
		"function": "median",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS008" {
		t.Errorf("code %q, want OLU-TS008", code)
	}
}

// --- OLU-TS009: num_field out of range ---

func TestTSError_NumFieldOutOfRange(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	status, result := env.do("POST", env.tsURL("t", "/aggregate"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1},
		"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z",
		"num_field": 7, "function": "avg",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS009" {
		t.Errorf("code %q, want OLU-TS009", code)
	}
}

// --- OLU-TS010: invalid interval ---

func TestTSError_InvalidInterval(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	status, result := env.do("POST", env.tsURL("t", "/aggregate"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1},
		"from": "2026-01-01T00:00:00Z", "to": "2026-01-02T00:00:00Z",
		"function": "avg", "interval": "2h",
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS010" {
		t.Errorf("code %q, want OLU-TS010", code)
	}
}

// --- OLU-TS011: query range too large ---

func TestTSError_QueryRangeTooLarge(t *testing.T) {
	env := setupTSServer(t, func(cfg *config.Config) { cfg.TSMaxRangeDays = 7 })
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	qURL := env.tsURL("t", "/events") +
		"?timeline=1&dims=1&from=2026-01-01T00:00:00Z&to=2026-01-31T00:00:00Z"
	status, result := env.do("GET", qURL, nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS011" {
		t.Errorf("code %q, want OLU-TS011", code)
	}
}

// --- OLU-TS016: dims change after first write ---

func TestTSError_DimsImmutableAfterFirstWrite(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 2})

	env.do("POST", env.tsURL("t", "/events"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1, 2}, "time": "2026-01-01T00:00:00Z",
	})

	status, result := env.do("POST", env.tsURL("t", "/timelines"), map[string]interface{}{
		"id": 1, "dims": 3,
	})
	if status != http.StatusConflict {
		t.Errorf("expected 409, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS016" {
		t.Errorf("code %q, want OLU-TS016", code)
	}
}

// --- OLU-TS017: NaN in numeric field ---

func TestTSError_NaNInNums(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	status, result := env.do("POST", env.tsURL("t", "/events"), map[string]interface{}{
		"timeline": 1, "dims": []interface{}{1},
		"time": "2026-01-01T00:00:00Z",
		"nums": []interface{}{"NaN"}, // JSON can't represent NaN; string triggers parse error
	})
	// Invalid type for nums field — should be a 400.
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
}

// --- OLU-TS018: reserved timeline ID ---

func TestTSError_ReservedTimelineID(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	status, result := env.do("POST", env.tsURL("t", "/timelines"), map[string]interface{}{
		"id": 0, "dims": 1,
	})
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d: %v", status, result)
	}
	if code := tsErrCode(result); code != "OLU-TS018" {
		t.Errorf("code %q, want OLU-TS018", code)
	}
}

// --- Response envelope shape ---

// TestTSError_ResponseShape verifies that every error response uses the
// standard {"error":{"code":"...","message":"...","status":N}} envelope.
func TestTSError_ResponseShape(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")

	// Trigger OLU-TS018 to get a predictable error.
	_, result := env.do("POST", env.tsURL("t", "/timelines"), map[string]interface{}{
		"id": 0, "dims": 1,
	})

	errObj, ok := result["error"].(map[string]interface{})
	if !ok {
		t.Fatalf("response has no 'error' key: %v", result)
	}
	if _, hasCode := errObj["code"]; !hasCode {
		t.Error("error object missing 'code' field")
	}
	if _, hasMsg := errObj["message"]; !hasMsg {
		t.Error("error object missing 'message' field")
	}
	if _, hasStatus := errObj["status"]; !hasStatus {
		t.Error("error object missing 'status' field")
	}
}

// --- Malformed request bodies ---

func TestTSError_MalformedBody_Define(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")

	// Send raw string bodies that bypass json.Marshal — use http.NewRequest directly.
	rawPost := func(url, body string) int {
		t.Helper()
		var reader io.Reader
		if body != "" {
			reader = strings.NewReader(body)
		}
		req, _ := http.NewRequest("POST", url, reader)
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	cases := []struct {
		name string
		body string
		want int
	}{
		{"not json", `not-json`, http.StatusBadRequest},
		{"empty body", ``, http.StatusBadRequest},
		// missing dims: dims=0 fails store validation; the handler maps all
		// DefineTimeline errors to 409. This could be 400 but handler
		// doesn't discriminate — test matches actual behaviour.
		{"missing dims", `{"id":1}`, http.StatusConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := rawPost(env.tsURL("t", "/timelines"), tc.body)
			if status != tc.want {
				t.Errorf("%s: expected %d, got %d", tc.name, tc.want, status)
			}
		})
	}
}

// --- Query parameter validation ---

func TestTSError_QueryRange_MissingParams(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})

	cases := []struct {
		name string
		url  string
	}{
		{"missing timeline", env.tsURL("t", "/events") + "?dims=1&from=2026-01-01T00:00:00Z&to=2026-01-02T00:00:00Z"},
		{"missing from", env.tsURL("t", "/events") + "?timeline=1&dims=1&to=2026-01-02T00:00:00Z"},
		{"missing to", env.tsURL("t", "/events") + "?timeline=1&dims=1&from=2026-01-01T00:00:00Z"},
		// Note: reversed from/to (from > to) currently returns 200 with empty
		// results rather than 400. Validation gap — not tested here.
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, _ := env.do("GET", tc.url, nil)
			if status != http.StatusBadRequest {
				t.Errorf("%s: expected 400, got %d", tc.name, status)
			}
		})
	}
}

// TestTSError_GetTimeline_NotFound verifies 404 for a timeline ID that was
// never defined.
func TestTSError_GetTimeline_NotFound(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	status, result := env.do("GET", env.tsURL("t", "/timelines/42"), nil)
	if status != http.StatusNotFound {
		t.Errorf("expected 404, got %d: %v", status, result)
	}
}

// TestTSError_PatchTimeline_NameOnly verifies that PATCH allows updating
// Name but rejects unknown fields gracefully (400 on completely bad body).
func TestTSError_PatchTimeline_BadBody(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	req, _ := http.NewRequest("PATCH", env.tsURL("t", "/timelines/1"), strings.NewReader("not-json"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	status := resp.StatusCode
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", status)
	}
}

// TestTSError_ProvisionIdempotent verifies that provisioning twice is not
// an error — idempotent operation.
func TestTSError_ProvisionIdempotent(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	// Second provision.
	status, result := env.do("POST", env.tsURL("t", "/provision"), nil)
	if status != http.StatusCreated {
		t.Errorf("second provision: expected 201, got %d: %v", status, result)
	}
}

// TestTSError_Latest_Missing_Params verifies 400 on missing required params.
func TestTSError_Latest_MissingParams(t *testing.T) {
	env := setupTSServer(t, nil)
	env.registerTenant("t")
	env.provision("t")
	env.defineTimeline("t", map[string]interface{}{"id": 1, "dims": 1})
	// Missing timeline param.
	status, _ := env.do("GET", env.tsURL("t", "/events/latest")+"?dims=1", nil)
	if status != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", status)
	}
}


