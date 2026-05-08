// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// server_errors_test.go — tests for error paths and edge cases.
// Complements server_test.go which covers happy paths.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// ── Registry error paths ──────────────────────────────────────────────────────

func TestHTTP_Register_BadJSON(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp, err := http.Post(ts.URL+"/registry", "application/json",
		bytes.NewBufferString(`{bad json}`))
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-XX001")
}

func TestHTTP_Resolve_RetiredEntity(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu:9090", 1)
	encoded := url.PathEscape(gid)

	// Retire it.
	postJSON(t, ts, "/registry/"+encoded+"/retire", map[string]interface{}{
		"reason": "decommissioned",
	})

	// Resolve should return 410 Gone with NOLU-RG003.
	resp := get(t, ts, "/registry/"+encoded+"/resolve")
	if resp.StatusCode != http.StatusGone {
		t.Errorf("expected 410, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-RG003")
}

func TestHTTP_Transfer_WrongCurrentOwner(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)
	encoded := url.PathEscape(gid)

	// Transfer with wrong From (local_id=99 not 1).
	resp := postJSON(t, ts, "/registry/"+encoded+"/transfer", map[string]interface{}{
		"from": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     99, // wrong
		},
		"to": map[string]interface{}{
			"instance_url": "http://xolu-b:9091",
			"entity_type":  "devices",
			"local_id":     2,
		},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-RG004")
}

func TestHTTP_Transfer_RetiredEntity(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)
	encoded := url.PathEscape(gid)

	postJSON(t, ts, "/registry/"+encoded+"/retire", map[string]interface{}{})

	resp := postJSON(t, ts, "/registry/"+encoded+"/transfer", map[string]interface{}{
		"from": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
		"to": map[string]interface{}{
			"instance_url": "http://xolu-b:9091",
			"entity_type":  "devices",
			"local_id":     2,
		},
	})
	if resp.StatusCode != http.StatusGone {
		t.Errorf("expected 410 for retired entity transfer, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-RG003")
}

func TestHTTP_Retire_AlreadyRetired(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)
	encoded := url.PathEscape(gid)

	postJSON(t, ts, "/registry/"+encoded+"/retire", map[string]interface{}{})

	// Second retire should return 410 with NOLU-RG003.
	resp := postJSON(t, ts, "/registry/"+encoded+"/retire", map[string]interface{}{})
	if resp.StatusCode != http.StatusGone {
		t.Errorf("expected 410 on double retire, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-RG003")
}

func TestHTTP_Get_MalformedGlobalID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// A clearly invalid GlobalID (not nolu:// scheme).
	resp := get(t, ts, "/registry/not-a-valid-global-id")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 for malformed GlobalID, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-XX001")
}

func TestHTTP_RegistryList_NoFilter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/registry")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 with no filter, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-XX001")
}

func TestHTTP_RegistryList_ByInstance(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	registerDevice(t, ts, "http://xolu-a:9090", 1)
	registerDevice(t, ts, "http://xolu-a:9090", 2)
	registerDevice(t, ts, "http://xolu-b:9091", 3)

	resp := get(t, ts, "/registry?instance_url=http://xolu-a:9090")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	count, _ := body["count"].(float64)
	if count != 2 {
		t.Errorf("expected 2 entities for xolu-a, got %.0f", count)
	}
}

func TestHTTP_RegistryList_ByEntityType(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	registerDevice(t, ts, "http://xolu-a:9090", 1)
	registerDevice(t, ts, "http://xolu-a:9090", 2)

	// Also register a different entity type.
	postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "shelves",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "shelves",
			"local_id":     1,
		},
	})

	resp := get(t, ts, "/registry?entity_type=devices")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	count, _ := body["count"].(float64)
	if count != 2 {
		t.Errorf("expected 2 devices, got %.0f", count)
	}
}

// ── Transfer negotiation error paths ─────────────────────────────────────────

func TestHTTP_Propose_MissingGlobalID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts, "/transfers", map[string]interface{}{
		"from": map[string]interface{}{"instance_url": "http://xolu-a:9090"},
		"to":   map[string]interface{}{"instance_url": "http://xolu-b:9091"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-XX001")
}

func TestHTTP_TransferGet_NotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/transfers/nonexistent-id")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-TX001")
}

func TestHTTP_Accept_WrongState(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)
	propResp := postJSON(t, ts, "/transfers", map[string]interface{}{
		"global_id": gid,
		"from": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
		"to": map[string]interface{}{
			"instance_url": "http://xolu-b:9091",
			"entity_type":  "devices",
			"local_id":     2,
		},
		"history_offer": map[string]interface{}{"mode": "full"},
	})
	propBody := decodeJSON(t, propResp)
	propID := propBody["id"].(string)

	// Accept it once.
	postJSON(t, ts, "/transfers/"+propID+"/accept", map[string]interface{}{
		"history_spec": map[string]interface{}{"mode": "full"},
	})

	// Accept it again — wrong state (already accepted).
	resp := postJSON(t, ts, "/transfers/"+propID+"/accept", map[string]interface{}{
		"history_spec": map[string]interface{}{"mode": "full"},
	})
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409 on double accept, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-TX002")
}

func TestHTTP_Complete_RequiresAccepted(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)
	propResp := postJSON(t, ts, "/transfers", map[string]interface{}{
		"global_id": gid,
		"from": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
		"to": map[string]interface{}{
			"instance_url": "http://xolu-b:9091",
			"entity_type":  "devices",
			"local_id":     2,
		},
		"history_offer": map[string]interface{}{"mode": "full"},
	})
	propBody := decodeJSON(t, propResp)
	propID := propBody["id"].(string)

	// Complete without accepting first — wrong state.
	resp := postJSON(t, ts, "/transfers/"+propID+"/complete", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Errorf("expected 409, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-TX002")
}

func TestHTTP_Reject_LeavesRegistryUnchanged(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)
	encoded := url.PathEscape(gid)

	propResp := postJSON(t, ts, "/transfers", map[string]interface{}{
		"global_id": gid,
		"from": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
		"to": map[string]interface{}{
			"instance_url": "http://xolu-b:9091",
			"entity_type":  "devices",
			"local_id":     2,
		},
		"history_offer": map[string]interface{}{"mode": "full"},
	})
	propBody := decodeJSON(t, propResp)
	propID := propBody["id"].(string)

	rejResp := postJSON(t, ts, "/transfers/"+propID+"/reject", map[string]interface{}{
		"reason": "inspection failed",
	})
	if rejResp.StatusCode != http.StatusOK {
		t.Fatalf("reject: expected 200, got %d", rejResp.StatusCode)
	}

	// Registry should still point at xolu-a.
	resolveResp := get(t, ts, "/registry/"+encoded+"/resolve")
	resolveBody := decodeJSON(t, resolveResp)
	current := resolveBody["current"].(map[string]interface{})
	if current["instance_url"] != "http://xolu-a:9090" {
		t.Errorf("after rejection: expected xolu-a, got %v", current["instance_url"])
	}
}

func TestHTTP_TransferList_ByGlobalID(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	gid := registerDevice(t, ts, "http://xolu-a:9090", 1)

	postJSON(t, ts, "/transfers", map[string]interface{}{
		"global_id": gid,
		"from": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
		"to": map[string]interface{}{
			"instance_url": "http://xolu-b:9091",
			"entity_type":  "devices",
			"local_id":     2,
		},
		"history_offer": map[string]interface{}{"mode": "full"},
	})

	resp := get(t, ts, "/transfers?global_id="+url.QueryEscape(gid))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	count, _ := body["count"].(float64)
	if count < 1 {
		t.Errorf("expected at least 1 proposal, got %.0f", count)
	}
}

func TestHTTP_TransferList_NoFilter(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/transfers")
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400 with no filter, got %d", resp.StatusCode)
	}
}

// ── Hotswap error paths ───────────────────────────────────────────────────────

func TestHTTP_HotswapGet_NotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/hotswaps/nonexistent")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-HS003")
}

func TestHTTP_HotswapStatus_NotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/hotswaps/nonexistent/status")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHTTP_HotswapConfirm_NotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts, "/hotswaps/nonexistent/confirm", nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHTTP_HotswapAbort_NotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts, "/hotswaps/nonexistent/abort", map[string]interface{}{
		"reason": "test",
	})
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
}

func TestHTTP_HotswapList_ByState(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/hotswaps?state=preparing")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["hotswaps"] == nil {
		t.Error("expected hotswaps field")
	}
}

// ── Utility endpoints ─────────────────────────────────────────────────────────

func TestHTTP_Version_Fields(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/version")
	body := decodeJSON(t, resp)

	for _, field := range []string{"version", "registry_host", "hotswap", "proxy"} {
		if _, ok := body[field]; !ok {
			t.Errorf("version: missing field %q", field)
		}
	}
}

// ── Test helpers ──────────────────────────────────────────────────────────────

// registerDevice registers a device entity and returns the GlobalID string.
func registerDevice(t *testing.T, ts *httptest.Server, instanceURL string, localID int) string {
	t.Helper()
	resp := postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": instanceURL,
			"entity_type":  "devices",
			"local_id":     localID,
		},
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("registerDevice: expected 201, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	gid, _ := body["global_id"].(string)
	if gid == "" {
		t.Fatal("registerDevice: no global_id in response")
	}
	return gid
}

func assertErrorCode(t *testing.T, body map[string]interface{}, expectedCode string) {
	t.Helper()
	errObj, ok := body["error"].(map[string]interface{})
	if !ok {
		t.Errorf("expected error object in response, got: %v", body)
		return
	}
	if errObj["code"] != expectedCode {
		b, _ := json.Marshal(body)
		t.Errorf("expected error code %q, got %q\nfull response: %s",
			expectedCode, errObj["code"], b)
	}
}


// ── Locate endpoint ───────────────────────────────────────────────────────────

func TestHTTP_Locate_Found(t *testing.T) {
	// Locate requires the TenantDirectory — use a server that has one.
	ts := newTestServerWithDirectory(t)
	defer ts.Close()

	// Register a device with a named tenant so the directory picks it up.
	postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"tenant_name":  "vendocorp",
			"tenant_id":    1,
			"entity_type":  "devices",
			"local_id":     1,
		},
	})

	// Allow directory event to propagate.
	time.Sleep(30 * time.Millisecond)

	resp := get(t, ts, "/tenants/vendocorp/locate")
	if resp.StatusCode != http.StatusOK {
		body := decodeJSON(t, resp)
		t.Fatalf("locate: expected 200, got %d: %v", resp.StatusCode, body)
	}
	body := decodeJSON(t, resp)
	if body["tenant"] != "vendocorp" {
		t.Errorf("locate: expected tenant=vendocorp, got %v", body["tenant"])
	}
	if body["instance_url"] != "http://xolu-a:9090" {
		t.Errorf("locate: expected xolu-a, got %v", body["instance_url"])
	}
	if body["stable_until"] == nil {
		t.Error("locate: expected stable_until field")
	}
}

func TestHTTP_Locate_NotFound(t *testing.T) {
	ts := newTestServerWithDirectory(t)
	defer ts.Close()

	resp := get(t, ts, "/tenants/nonexistent/locate")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	assertErrorCode(t, body, "NOLU-RG001")
}

func TestHTTP_Locate_NoDirectory(t *testing.T) {
	// Server without directory — returns 503.
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/tenants/vendocorp/locate")
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("expected 503 without directory, got %d", resp.StatusCode)
	}
}
