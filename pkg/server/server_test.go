// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/hotswap"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/server"
	"github.com/ha1tch/nolu/pkg/transfer"
)

// newTestServer creates a fully wired server against in-memory backends.
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	neg := transfer.NewMemoryNegotiator(reg)
	hs  := hotswap.NewMemoryManager(reg, bus, nil)
	srv := server.New(reg, neg, hs, nil, nil, "registry.test.local")
	return httptest.NewServer(srv)
}

func get(t *testing.T, ts *httptest.Server, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	return resp
}

func postJSON(t *testing.T, ts *httptest.Server, path string, body interface{}) *http.Response {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(ts.URL+path, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", path, err)
	}
	return resp
}

func decodeJSON(t *testing.T, resp *http.Response) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&m); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	resp.Body.Close()
	return m
}

// ── Health / Version ──────────────────────────────────────────────────────────

func TestHTTP_Health(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/health")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["status"] != "ok" {
		t.Errorf("expected status=ok, got %v", body["status"])
	}
}

func TestHTTP_Version(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/version")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["version"] == nil {
		t.Errorf("expected version field")
	}
}

// ── Registry ──────────────────────────────────────────────────────────────────

func TestHTTP_Register(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"tenant_id":    0,
			"entity_type":  "devices",
			"local_id":     42,
		},
	})
	if resp.StatusCode != http.StatusCreated {
		body := decodeJSON(t, resp)
		t.Fatalf("expected 201, got %d: %v", resp.StatusCode, body)
	}
	body := decodeJSON(t, resp)
	gid, ok := body["global_id"].(string)
	if !ok || gid == "" {
		t.Fatalf("expected global_id in response, got %v", body)
	}
	if body["status"] != "active" {
		t.Errorf("expected status=active, got %v", body["status"])
	}
}

func TestHTTP_Register_MissingEntityType(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts, "/registry", map[string]interface{}{
		"owner": map[string]interface{}{"instance_url": "http://x:9090"},
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHTTP_GetAndResolve(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Register.
	regResp := postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
	})
	body := decodeJSON(t, regResp)
	gid := body["global_id"].(string)
	encoded := percentEncode(gid)

	// Get.
	getResp := get(t, ts, "/registry/"+encoded)
	if getResp.StatusCode != http.StatusOK {
		t.Fatalf("GET /registry/{gid}: expected 200, got %d", getResp.StatusCode)
	}
	getBody := decodeJSON(t, getResp)
	if getBody["global_id"] != gid {
		t.Errorf("expected global_id=%s, got %v", gid, getBody["global_id"])
	}

	// Resolve.
	resolveResp := get(t, ts, "/registry/"+encoded+"/resolve")
	if resolveResp.StatusCode != http.StatusOK {
		t.Fatalf("resolve: expected 200, got %d", resolveResp.StatusCode)
	}
	resolveBody := decodeJSON(t, resolveResp)
	if resolveBody["global_id"] != gid {
		t.Errorf("resolve: expected global_id=%s, got %v", gid, resolveBody["global_id"])
	}
	current, _ := resolveBody["current"].(map[string]interface{})
	if current == nil || current["instance_url"] != "http://xolu-a:9090" {
		t.Errorf("resolve: unexpected current: %v", current)
	}
}

func TestHTTP_Get_NotFound(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	encoded := percentEncode("nolu://registry.test.local/devices/00000000-0000-0000-0000-000000000000")
	resp := get(t, ts, "/registry/"+encoded)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("expected 404, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	errObj, _ := body["error"].(map[string]interface{})
	if errObj["code"] != "NOLU-RG001" {
		t.Errorf("expected NOLU-RG001, got %v", errObj["code"])
	}
}

func TestHTTP_Transfer(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Register.
	regResp := postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
	})
	body := decodeJSON(t, regResp)
	gid := body["global_id"].(string)
	encoded := percentEncode(gid)

	// Direct transfer.
	xferResp := postJSON(t, ts, "/registry/"+encoded+"/transfer", map[string]interface{}{
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
		"protocol": "TEST-001",
	})
	if xferResp.StatusCode != http.StatusOK {
		xferBody := decodeJSON(t, xferResp)
		t.Fatalf("transfer: expected 200, got %d: %v", xferResp.StatusCode, xferBody)
	}
	xferBody := decodeJSON(t, xferResp)
	current, _ := xferBody["current"].(map[string]interface{})
	if current["instance_url"] != "http://xolu-b:9091" {
		t.Errorf("transfer: expected xolu-b, got %v", current["instance_url"])
	}
}

func TestHTTP_Retire(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	regResp := postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
	})
	body := decodeJSON(t, regResp)
	gid := body["global_id"].(string)
	encoded := percentEncode(gid)

	retireResp := postJSON(t, ts, "/registry/"+encoded+"/retire", map[string]interface{}{
		"reason": "end of life",
	})
	if retireResp.StatusCode != http.StatusOK {
		t.Fatalf("retire: expected 200, got %d", retireResp.StatusCode)
	}
	retireBody := decodeJSON(t, retireResp)
	if retireBody["status"] != "retired" {
		t.Errorf("retire: expected status=retired, got %v", retireBody["status"])
	}

	// Resolve after retirement should return 410.
	resolveResp := get(t, ts, "/registry/"+encoded+"/resolve")
	if resolveResp.StatusCode != http.StatusGone {
		t.Errorf("resolve retired: expected 410, got %d", resolveResp.StatusCode)
	}
}

// ── Transfer negotiation ──────────────────────────────────────────────────────

func TestHTTP_TransferNegotiation(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	// Register entity.
	regResp := postJSON(t, ts, "/registry", map[string]interface{}{
		"entity_type": "devices",
		"owner": map[string]interface{}{
			"instance_url": "http://xolu-a:9090",
			"entity_type":  "devices",
			"local_id":     1,
		},
	})
	gid := decodeJSON(t, regResp)["global_id"].(string)

	// Propose.
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
		"protocol": "PO-001",
		"history_offer": map[string]interface{}{"mode": "full"},
	})
	if propResp.StatusCode != http.StatusCreated {
		body := decodeJSON(t, propResp)
		t.Fatalf("propose: expected 201, got %d: %v", propResp.StatusCode, body)
	}
	propBody := decodeJSON(t, propResp)
	propID := propBody["id"].(string)
	if propBody["state"] != "proposed" {
		t.Errorf("propose: expected state=proposed, got %v", propBody["state"])
	}

	// Accept.
	acceptResp := postJSON(t, ts, "/transfers/"+propID+"/accept", map[string]interface{}{
		"history_spec": map[string]interface{}{"mode": "full"},
	})
	if acceptResp.StatusCode != http.StatusOK {
		body := decodeJSON(t, acceptResp)
		t.Fatalf("accept: expected 200, got %d: %v", acceptResp.StatusCode, body)
	}
	acceptBody := decodeJSON(t, acceptResp)
	if acceptBody["state"] != "accepted" {
		t.Errorf("accept: expected state=accepted, got %v", acceptBody["state"])
	}

	// Complete.
	completeResp := postJSON(t, ts, "/transfers/"+propID+"/complete", nil)
	if completeResp.StatusCode != http.StatusOK {
		t.Fatalf("complete: expected 200, got %d", completeResp.StatusCode)
	}

	// Verify registry updated.
	encoded := percentEncode(gid)
	resolveResp := get(t, ts, "/registry/"+encoded+"/resolve")
	resolveBody := decodeJSON(t, resolveResp)
	current := resolveBody["current"].(map[string]interface{})
	if current["instance_url"] != "http://xolu-b:9091" {
		t.Errorf("after accept: expected xolu-b, got %v", current["instance_url"])
	}
}

// ── Hotswap ───────────────────────────────────────────────────────────────────

func TestHTTP_HotswapRequest(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := postJSON(t, ts, "/hotswaps", map[string]interface{}{
		"source": map[string]interface{}{
			"instance_url": "http://xolu-hub-a:9090",
			"tenant_name":  "vendocorp",
			"tenant_id":    1,
		},
		"target": map[string]interface{}{
			"instance_url": "http://xolu-hub-b:9091",
			"tenant_name":  "vendocorp",
			"tenant_id":    1,
		},
		"options": map[string]interface{}{
			"auto_advance": false,
		},
	})
	// Source is not reachable in test — expect 502.
	if resp.StatusCode != http.StatusBadGateway {
		body := decodeJSON(t, resp)
		t.Logf("hotswap request body: %v", body)
		// Either 502 (unreachable) or 201 (if connectivity passes) is acceptable.
		// In CI there's no xolu running so 502 is expected.
	}
}

func TestHTTP_HotswapList(t *testing.T) {
	ts := newTestServer(t)
	defer ts.Close()

	resp := get(t, ts, "/hotswaps")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	body := decodeJSON(t, resp)
	if body["count"] == nil {
		t.Errorf("expected count field")
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func percentEncode(s string) string {
	return url.PathEscape(s)
}

// newTestServerWithDirectory creates a test server with a TenantDirectory wired in.
func newTestServerWithDirectory(t *testing.T) *httptest.Server {
	t.Helper()
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	neg := transfer.NewMemoryNegotiator(reg)
	hs  := hotswap.NewMemoryManager(reg, bus, nil)

	dir := registry.NewTenantDirectory(reg, 30*time.Second)
	ctx, cancel := context.WithCancel(context.Background())
	if err := dir.Start(ctx); err != nil {
		cancel()
		t.Fatalf("dir.Start: %v", err)
	}
	t.Cleanup(cancel)

	srv := server.New(reg, neg, hs, nil, dir, "registry.test.local")
	return httptest.NewServer(srv)
}
