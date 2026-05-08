// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

// tier1_oql_test.go
//
// Tier 1 integration tests targeting the highest-risk untested areas
// from the a caller gap analysis:
//
//   1. DATE_TRUNC scalar function in OQL (event analytics)
//   2. GROUP BY with aggregates through the HTTP API (tenant-scoped)
//   3. List endpoint pagination, filtering, and sorting
//
// Author: ha1tch <h@ual.fi>
// Repository: https://github.com/ha1tch/xolu/

import (
	"fmt"
	"net/http"
	"testing"
)

// ============================================================================
// 1. DATE_TRUNC SCALAR FUNCTION
// ============================================================================

func TestOQLDateTrunc(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create events with precise timestamps spanning multiple days and hours
	timestamps := []string{
		"2025-01-15T08:30:00Z",
		"2025-01-15T08:45:00Z",
		"2025-01-15T09:10:00Z",
		"2025-01-15T09:55:00Z",
		"2025-01-16T10:00:00Z",
		"2025-01-16T10:30:00Z",
		"2025-01-17T14:00:00Z",
	}
	for _, ts := range timestamps {
		env.createEntity("/api/v1/events", map[string]interface{}{
			"trigger_type": "sensor_reading",
			"timestamp":    ts,
			"asset_id":     1,
		})
	}

	t.Run("DATE_TRUNC hour groups correctly", func(t *testing.T) {
		// typical hourly analytics query
		data := env.oqlData("/api/v1/oql/query",
			"SELECT DATE_TRUNC('hour', timestamp) as period, COUNT(*) as count FROM events GROUP BY period ORDER BY period")
		// Should produce 4 groups: 2025-01-15T08, T09, 2025-01-16T10, 2025-01-17T14
		if len(data) != 4 {
			t.Fatalf("expected 4 hourly groups, got %d: %v", len(data), data)
		}

		// Verify first group (08:xx)
		row0 := data[0].(map[string]interface{})
		if row0["period"] != "2025-01-15T08:00:00Z" {
			t.Errorf("expected period=2025-01-15T08:00:00Z, got %v", row0["period"])
		}
		if int(toFloat64(row0["count"])) != 2 {
			t.Errorf("expected count=2 for 08:xx, got %v", row0["count"])
		}

		// Verify second group (09:xx)
		row1 := data[1].(map[string]interface{})
		if row1["period"] != "2025-01-15T09:00:00Z" {
			t.Errorf("expected period=2025-01-15T09:00:00Z, got %v", row1["period"])
		}
		if int(toFloat64(row1["count"])) != 2 {
			t.Errorf("expected count=2 for 09:xx, got %v", row1["count"])
		}

		// Third group (10:xx on 16th)
		row2 := data[2].(map[string]interface{})
		if row2["period"] != "2025-01-16T10:00:00Z" {
			t.Errorf("expected period=2025-01-16T10:00:00Z, got %v", row2["period"])
		}
		if int(toFloat64(row2["count"])) != 2 {
			t.Errorf("expected count=2 for 10:xx, got %v", row2["count"])
		}

		// Fourth group (14:xx on 17th)
		row3 := data[3].(map[string]interface{})
		if row3["period"] != "2025-01-17T14:00:00Z" {
			t.Errorf("expected period=2025-01-17T14:00:00Z, got %v", row3["period"])
		}
		if int(toFloat64(row3["count"])) != 1 {
			t.Errorf("expected count=1 for 14:xx, got %v", row3["count"])
		}
	})

	t.Run("DATE_TRUNC day groups correctly", func(t *testing.T) {
		// typical daily analytics query
		data := env.oqlData("/api/v1/oql/query",
			"SELECT DATE_TRUNC('day', timestamp) as period, COUNT(*) as count FROM events GROUP BY period ORDER BY period")
		// Should produce 3 groups: 2025-01-15, 16, 17
		if len(data) != 3 {
			t.Fatalf("expected 3 daily groups, got %d", len(data))
		}

		row0 := data[0].(map[string]interface{})
		if row0["period"] != "2025-01-15T00:00:00Z" {
			t.Errorf("expected period=2025-01-15T00:00:00Z, got %v", row0["period"])
		}
		if int(toFloat64(row0["count"])) != 4 {
			t.Errorf("expected count=4 for Jan 15, got %v", row0["count"])
		}

		row1 := data[1].(map[string]interface{})
		if int(toFloat64(row1["count"])) != 2 {
			t.Errorf("expected count=2 for Jan 16, got %v", row1["count"])
		}

		row2 := data[2].(map[string]interface{})
		if int(toFloat64(row2["count"])) != 1 {
			t.Errorf("expected count=1 for Jan 17, got %v", row2["count"])
		}
	})

	t.Run("DATE_TRUNC month groups correctly", func(t *testing.T) {
		// Add an event in a different month
		env.createEntity("/api/v1/events", map[string]interface{}{
			"trigger_type": "sensor_reading",
			"timestamp":    "2025-02-10T12:00:00Z",
			"asset_id":     1,
		})

		data := env.oqlData("/api/v1/oql/query",
			"SELECT DATE_TRUNC('month', timestamp) as period, COUNT(*) as count FROM events GROUP BY period ORDER BY period")
		if len(data) != 2 {
			t.Fatalf("expected 2 monthly groups, got %d", len(data))
		}

		row0 := data[0].(map[string]interface{})
		if row0["period"] != "2025-01-01T00:00:00Z" {
			t.Errorf("expected period=2025-01-01T00:00:00Z, got %v", row0["period"])
		}
		if int(toFloat64(row0["count"])) != 7 {
			t.Errorf("expected count=7 for January, got %v", row0["count"])
		}
	})

	t.Run("DATE_TRUNC with WHERE range filter", func(t *testing.T) {
		// typical query with time range
		data := env.oqlData("/api/v1/oql/query",
			"SELECT DATE_TRUNC('hour', timestamp) as period, COUNT(*) as count FROM events WHERE timestamp >= '2025-01-15T09:00:00Z' AND timestamp <= '2025-01-16T10:30:00Z' GROUP BY period ORDER BY period")
		// Should include: 09:xx (2 events), 10:xx (2 events)
		if len(data) != 2 {
			t.Fatalf("expected 2 groups in range, got %d: %v", len(data), data)
		}
	})

	t.Run("DATE_TRUNC with tenant isolation", func(t *testing.T) {
		// Create tenant-scoped events
		env.createEntity("/api/v1/tenant/acme/events", map[string]interface{}{
			"trigger_type": "alert",
			"timestamp":    "2025-03-01T10:00:00Z",
		})
		env.createEntity("/api/v1/tenant/acme/events", map[string]interface{}{
			"trigger_type": "alert",
			"timestamp":    "2025-03-01T11:00:00Z",
		})
		env.createEntity("/api/v1/tenant/globex/events", map[string]interface{}{
			"trigger_type": "alert",
			"timestamp":    "2025-03-01T10:30:00Z",
		})

		data := env.oqlData("/api/v1/tenant/acme/oql/query",
			"SELECT DATE_TRUNC('day', timestamp) as period, COUNT(*) as count FROM events GROUP BY period")
		if len(data) != 1 {
			t.Fatalf("expected 1 daily group for acme, got %d", len(data))
		}
		row := data[0].(map[string]interface{})
		if int(toFloat64(row["count"])) != 2 {
			t.Errorf("expected count=2 for acme, got %v", row["count"])
		}
	})

	t.Run("DATE_TRUNC with nil timestamp returns nil", func(t *testing.T) {
		env.createEntity("/api/v1/events", map[string]interface{}{
			"trigger_type": "manual",
			"asset_id":     2,
			// No timestamp field
		})

		// The event without timestamp should end up in a nil-period group
		data := env.oqlData("/api/v1/oql/query",
			"SELECT DATE_TRUNC('day', timestamp) as period, COUNT(*) as count FROM events WHERE asset_id = 2 GROUP BY period")
		if len(data) != 1 {
			t.Fatalf("expected 1 group for null-timestamp events, got %d", len(data))
		}
		row := data[0].(map[string]interface{})
		if row["period"] != nil {
			t.Errorf("expected nil period for events without timestamp, got %v", row["period"])
		}
	})
}

// ============================================================================
// 2. GROUP BY WITH AGGREGATES (through HTTP API, with tenant isolation)
// ============================================================================

func TestOQLGroupByIntegration(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Seed events with various trigger types across tenants
	acmeEvents := []map[string]interface{}{
		{"trigger_type": "sensor_reading", "asset_id": 1, "value": 42},
		{"trigger_type": "sensor_reading", "asset_id": 1, "value": 55},
		{"trigger_type": "sensor_reading", "asset_id": 2, "value": 38},
		{"trigger_type": "manual", "asset_id": 1, "value": 10},
		{"trigger_type": "alert", "asset_id": 2, "value": 99},
		{"trigger_type": "alert", "asset_id": 2, "value": 88},
	}
	for _, ev := range acmeEvents {
		env.createEntity("/api/v1/tenant/acme/events", ev)
	}
	// Globex has its own events
	env.createEntity("/api/v1/tenant/globex/events", map[string]interface{}{
		"trigger_type": "sensor_reading", "asset_id": 10, "value": 70,
	})
	env.createEntity("/api/v1/tenant/globex/events", map[string]interface{}{
		"trigger_type": "sensor_reading", "asset_id": 10, "value": 80,
	})

	t.Run("GROUP BY trigger_type with COUNT (typical query)", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, COUNT(*) as count FROM events GROUP BY trigger_type")
		if len(data) != 3 {
			t.Fatalf("expected 3 trigger_type groups, got %d: %v", len(data), data)
		}

		// Build a map for easier assertions
		byType := make(map[string]int)
		for _, row := range data {
			rec := row.(map[string]interface{})
			tt := fmt.Sprintf("%v", rec["trigger_type"])
			byType[tt] = int(toFloat64(rec["count"]))
		}

		if byType["sensor_reading"] != 3 {
			t.Errorf("expected 3 sensor_reading, got %d", byType["sensor_reading"])
		}
		if byType["manual"] != 1 {
			t.Errorf("expected 1 manual, got %d", byType["manual"])
		}
		if byType["alert"] != 2 {
			t.Errorf("expected 2 alert, got %d", byType["alert"])
		}
	})

	t.Run("GROUP BY does not leak cross-tenant", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/globex/oql/query",
			"SELECT trigger_type, COUNT(*) as count FROM events GROUP BY trigger_type")
		if len(data) != 1 {
			t.Fatalf("expected 1 group for globex, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if int(toFloat64(rec["count"])) != 2 {
			t.Errorf("expected 2 globex events, got %v", rec["count"])
		}
	})

	t.Run("GROUP BY with SUM aggregate", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, SUM(value) as total FROM events GROUP BY trigger_type")
		byType := make(map[string]float64)
		for _, row := range data {
			rec := row.(map[string]interface{})
			tt := fmt.Sprintf("%v", rec["trigger_type"])
			byType[tt] = toFloat64(rec["total"])
		}
		// sensor_reading: 42+55+38 = 135
		if byType["sensor_reading"] != 135 {
			t.Errorf("expected SUM=135 for sensor_reading, got %v", byType["sensor_reading"])
		}
		// alert: 99+88 = 187
		if byType["alert"] != 187 {
			t.Errorf("expected SUM=187 for alert, got %v", byType["alert"])
		}
	})

	t.Run("GROUP BY with AVG aggregate", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, AVG(value) as avg_val FROM events GROUP BY trigger_type")
		byType := make(map[string]float64)
		for _, row := range data {
			rec := row.(map[string]interface{})
			tt := fmt.Sprintf("%v", rec["trigger_type"])
			byType[tt] = toFloat64(rec["avg_val"])
		}
		// sensor_reading: (42+55+38)/3 = 45
		if byType["sensor_reading"] != 45 {
			t.Errorf("expected AVG=45 for sensor_reading, got %v", byType["sensor_reading"])
		}
	})

	t.Run("GROUP BY with MIN/MAX aggregates", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, MIN(value) as lo, MAX(value) as hi FROM events GROUP BY trigger_type")
		byType := make(map[string]map[string]interface{})
		for _, row := range data {
			rec := row.(map[string]interface{})
			tt := fmt.Sprintf("%v", rec["trigger_type"])
			byType[tt] = rec
		}
		if toFloat64(byType["sensor_reading"]["lo"]) != 38 {
			t.Errorf("expected MIN=38 for sensor_reading, got %v", byType["sensor_reading"]["lo"])
		}
		if toFloat64(byType["sensor_reading"]["hi"]) != 55 {
			t.Errorf("expected MAX=55 for sensor_reading, got %v", byType["sensor_reading"]["hi"])
		}
	})

	t.Run("GROUP BY on integer field (asset_id)", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT asset_id, COUNT(*) as count FROM events GROUP BY asset_id")
		if len(data) != 2 {
			t.Fatalf("expected 2 asset groups, got %d", len(data))
		}
		byAsset := make(map[int]int)
		for _, row := range data {
			rec := row.(map[string]interface{})
			aid := int(toFloat64(rec["asset_id"]))
			byAsset[aid] = int(toFloat64(rec["count"]))
		}
		if byAsset[1] != 3 {
			t.Errorf("expected 3 events for asset_id=1, got %d", byAsset[1])
		}
		if byAsset[2] != 3 {
			t.Errorf("expected 3 events for asset_id=2, got %d", byAsset[2])
		}
	})

	t.Run("GROUP BY with HAVING filter", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, COUNT(*) as count FROM events GROUP BY trigger_type HAVING COUNT(*) > 1")
		// Only sensor_reading (3) and alert (2) have count > 1
		if len(data) != 2 {
			t.Fatalf("expected 2 groups with HAVING count>1, got %d", len(data))
		}
	})

	// NOTE: No "non-tenant GROUP BY" sub-test. Non-tenant routes use tenant 0
	// (unscoped store) and cannot aggregate across tenant-scoped data.

	t.Run("GROUP BY with WHERE filter before grouping", func(t *testing.T) {
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, COUNT(*) as count FROM events WHERE asset_id = 2 GROUP BY trigger_type")
		// asset_id=2 has: sensor_reading(1), alert(2)
		if len(data) != 2 {
			t.Fatalf("expected 2 groups for asset_id=2, got %d", len(data))
		}
	})

	t.Run("GROUP BY with zero matching rows returns empty", func(t *testing.T) {
		// With GROUP BY, SQL standard says zero input = zero output rows
		data := env.oqlData(
			"/api/v1/tenant/acme/oql/query",
			"SELECT trigger_type, COUNT(*) as count FROM events WHERE asset_id = 9999 GROUP BY trigger_type")
		if len(data) != 0 {
			t.Errorf("expected 0 groups for non-existent asset, got %d", len(data))
		}
	})
}

// ============================================================================
// 3. LIST ENDPOINT — PAGINATION, FILTERING, SORTING
// ============================================================================

func TestListPagination(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	// Create 25 assets
	for i := 1; i <= 25; i++ {
		env.createEntity("/api/v1/assets", map[string]interface{}{
			"name":   fmt.Sprintf("Asset %02d", i),
			"status": "active",
			"value":  i * 10,
		})
	}

	t.Run("Default pagination returns first page", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination, _ := result["pagination"].(map[string]interface{})
		if pagination == nil {
			t.Fatal("expected pagination object in response")
		}
		totalItems := int(toFloat64(pagination["total_items"]))
		if totalItems != 25 {
			t.Errorf("expected total_items=25, got %d", totalItems)
		}
		data, _ := result["data"].([]interface{})
		if len(data) == 0 {
			t.Fatal("expected data array with items")
		}
		// Default per_page is typically 10
		perPage := int(toFloat64(pagination["per_page"]))
		if len(data) != perPage {
			t.Errorf("expected %d items on first page, got %d", perPage, len(data))
		}
	})

	t.Run("Custom per_page works", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?per_page=5", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		data, _ := result["data"].([]interface{})
		if len(data) != 5 {
			t.Errorf("expected 5 items with per_page=5, got %d", len(data))
		}
		pagination := result["pagination"].(map[string]interface{})
		totalPages := int(toFloat64(pagination["total_pages"]))
		if totalPages != 5 { // 25/5 = 5
			t.Errorf("expected total_pages=5, got %d", totalPages)
		}
	})

	t.Run("Page 2 returns different items", func(t *testing.T) {
		_, result1 := env.doJSON("GET", "/api/v1/assets?per_page=5&page=1", nil)
		_, result2 := env.doJSON("GET", "/api/v1/assets?per_page=5&page=2", nil)

		data1, _ := result1["data"].([]interface{})
		data2, _ := result2["data"].([]interface{})

		if len(data1) == 0 || len(data2) == 0 {
			t.Fatal("expected items on both pages")
		}

		// IDs should not overlap
		id1 := data1[0].(map[string]interface{})["id"]
		id2 := data2[0].(map[string]interface{})["id"]
		if id1 == id2 {
			t.Errorf("page 1 and page 2 should start with different items")
		}
	})

	t.Run("Page beyond data returns empty", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?per_page=10&page=100", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		data, _ := result["data"].([]interface{})
		if len(data) != 0 {
			t.Errorf("expected 0 items for page beyond range, got %d", len(data))
		}
	})

	t.Run("per_page capped at 100", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?per_page=200", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		perPage := int(toFloat64(pagination["per_page"]))
		if perPage > 100 {
			t.Errorf("per_page should be capped at 100, got %d", perPage)
		}
	})
}

func TestListFiltering(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Forklift A", "status": "active", "type_id": 1,
	})
	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Forklift B", "status": "active", "type_id": 1,
	})
	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Crane C", "status": "idle", "type_id": 2,
	})
	env.createEntity("/api/v1/assets", map[string]interface{}{
		"name": "Crane D", "status": "active", "type_id": 2,
	})

	t.Run("Filter by string field", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?status=active", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 3 {
			t.Errorf("expected 3 active assets, got %d", total)
		}
	})

	t.Run("Filter by numeric field", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?type_id=2", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 2 {
			t.Errorf("expected 2 type_id=2 assets, got %d", total)
		}
	})

	t.Run("Multiple filters combined (AND)", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?status=active&type_id=1", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 2 {
			t.Errorf("expected 2 active type_id=1 assets, got %d", total)
		}
	})

	t.Run("Filter with no matches returns empty", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets?status=decommissioned", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 0 {
			t.Errorf("expected 0 decommissioned assets, got %d", total)
		}
	})
}

func TestListTenantIsolation(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
		"name": "Acme Asset 1",
	})
	env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
		"name": "Acme Asset 2",
	})
	env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
		"name": "Acme Asset 3",
	})
	env.createEntity("/api/v1/tenant/globex/assets", map[string]interface{}{
		"name": "Globex Asset 1",
	})

	t.Run("Tenant-scoped list returns only that tenant", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/tenant/acme/assets", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 3 {
			t.Errorf("expected 3 acme assets, got %d", total)
		}
	})

	t.Run("Different tenant sees only its data", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/tenant/globex/assets", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 1 {
			t.Errorf("expected 1 globex asset, got %d", total)
		}
	})

	// NOTE: No "non-tenant list returns all" sub-test. Non-tenant routes use
	// tenant 0 (unscoped store) and cannot list tenant-scoped data.

	t.Run("Tenant-scoped list with filter", func(t *testing.T) {
		// Add typed assets
		env.createEntity("/api/v1/tenant/acme/assets", map[string]interface{}{
			"name": "Typed Acme", "status": "idle",
		})
		status, result := env.doJSON("GET", "/api/v1/tenant/acme/assets?status=idle", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 1 {
			t.Errorf("expected 1 idle acme asset, got %d", total)
		}
	})

	t.Run("Tenant-scoped list with pagination", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/tenant/acme/assets?per_page=2&page=1", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		data, _ := result["data"].([]interface{})
		if len(data) != 2 {
			t.Errorf("expected 2 items on page 1, got %d", len(data))
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 4 { // 3 original + 1 typed
			t.Errorf("expected 4 total acme assets, got %d", total)
		}
	})
}

func TestListEmptyEntity(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	t.Run("List on entity with no data returns empty", func(t *testing.T) {
		status, result := env.doJSON("GET", "/api/v1/assets", nil)
		if status != http.StatusOK {
			t.Fatalf("expected 200, got %d", status)
		}
		data, _ := result["data"].([]interface{})
		if len(data) != 0 {
			t.Errorf("expected empty data, got %d items", len(data))
		}
		pagination := result["pagination"].(map[string]interface{})
		total := int(toFloat64(pagination["total_items"]))
		if total != 0 {
			t.Errorf("expected total_items=0, got %d", total)
		}
	})
}

// ============================================================================
// 4. COMBINED: Full event analytics workflow
//    Create events -> query with DATE_TRUNC -> verify tenant isolation
// ============================================================================

func TestEventAnalyticsWorkflow(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	tenant := "acme"
	base := "/api/v1/tenant/" + tenant

	// Simulate 2 days of sensor events
	events := []struct {
		ts      string
		trigger string
		assetID int
	}{
		{"2025-06-15T08:00:00Z", "sensor_reading", 1},
		{"2025-06-15T08:15:00Z", "sensor_reading", 1},
		{"2025-06-15T09:00:00Z", "alert", 1},
		{"2025-06-15T09:30:00Z", "sensor_reading", 2},
		{"2025-06-15T10:00:00Z", "manual", 1},
		{"2025-06-16T08:00:00Z", "sensor_reading", 1},
		{"2025-06-16T08:30:00Z", "sensor_reading", 2},
		{"2025-06-16T09:00:00Z", "alert", 2},
	}
	for _, ev := range events {
		env.createEntity(base+"/events", map[string]interface{}{
			"trigger_type": ev.trigger,
			"timestamp":    ev.ts,
			"asset_id":     ev.assetID,
		})
	}

	// Add noise in another tenant
	env.createEntity("/api/v1/tenant/globex/events", map[string]interface{}{
		"trigger_type": "sensor_reading",
		"timestamp":    "2025-06-15T08:00:00Z",
		"asset_id":     99,
	})

	t.Run("Daily breakdown (a caller's group_by=day query)", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			"SELECT DATE_TRUNC('day', timestamp) as period, COUNT(*) as count FROM events WHERE timestamp >= '2025-06-15T00:00:00Z' AND timestamp <= '2025-06-16T23:59:59Z' GROUP BY period ORDER BY period")
		if len(data) != 2 {
			t.Fatalf("expected 2 daily buckets, got %d", len(data))
		}
		day1 := data[0].(map[string]interface{})
		day2 := data[1].(map[string]interface{})
		if int(toFloat64(day1["count"])) != 5 {
			t.Errorf("expected 5 events on Jun 15, got %v", day1["count"])
		}
		if int(toFloat64(day2["count"])) != 3 {
			t.Errorf("expected 3 events on Jun 16, got %v", day2["count"])
		}
	})

	t.Run("Hourly breakdown (a caller's group_by=hour query)", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			"SELECT DATE_TRUNC('hour', timestamp) as period, COUNT(*) as count FROM events WHERE timestamp >= '2025-06-15T00:00:00Z' AND timestamp <= '2025-06-15T23:59:59Z' GROUP BY period ORDER BY period")
		// Jun 15: 08:xx(2), 09:xx(2), 10:xx(1)
		if len(data) != 3 {
			t.Fatalf("expected 3 hourly buckets for Jun 15, got %d", len(data))
		}
	})

	t.Run("By trigger_type (a caller's group_by=trigger_type query)", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			"SELECT trigger_type, COUNT(*) as count FROM events WHERE timestamp >= '2025-06-15T00:00:00Z' AND timestamp <= '2025-06-16T23:59:59Z' GROUP BY trigger_type")
		byType := make(map[string]int)
		for _, row := range data {
			rec := row.(map[string]interface{})
			byType[fmt.Sprintf("%v", rec["trigger_type"])] = int(toFloat64(rec["count"]))
		}
		if byType["sensor_reading"] != 5 {
			t.Errorf("expected 5 sensor_reading, got %d", byType["sensor_reading"])
		}
		if byType["alert"] != 2 {
			t.Errorf("expected 2 alert, got %d", byType["alert"])
		}
		if byType["manual"] != 1 {
			t.Errorf("expected 1 manual, got %d", byType["manual"])
		}
	})

	t.Run("Globex events not counted in acme analytics", func(t *testing.T) {
		data := env.oqlData(base+"/oql/query",
			"SELECT COUNT(*) as count FROM events")
		row := data[0].(map[string]interface{})
		total := int(toFloat64(row["count"]))
		if total != 8 {
			t.Errorf("expected 8 acme events total, got %d (cross-tenant leak?)", total)
		}
	})
}

// ============================================================================
// 5. SCALAR FUNCTIONS (beyond DATE_TRUNC — the broader framework)
// ============================================================================

func TestOQLScalarFunctions(t *testing.T) {
	env := newIntegrationEnv(t)
	defer env.cleanup()

	env.createEntity("/api/v1/users", map[string]interface{}{
		"name":  "Alice Smith",
		"email": "alice@example.com",
		"age":   30,
	})
	env.createEntity("/api/v1/users", map[string]interface{}{
		"name":  "Bob Jones",
		"email": "bob@example.com",
		"age":   25,
	})

	t.Run("UPPER function", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT UPPER(name) as upper_name FROM users")
		if len(data) < 2 {
			t.Fatalf("expected 2 rows, got %d", len(data))
		}
		names := make(map[string]bool)
		for _, row := range data {
			rec := row.(map[string]interface{})
			names[fmt.Sprintf("%v", rec["upper_name"])] = true
		}
		if !names["ALICE SMITH"] {
			t.Error("expected ALICE SMITH in results")
		}
	})

	t.Run("LOWER function", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT LOWER(email) as lower_email FROM users")
		if len(data) < 1 {
			t.Fatalf("expected rows, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["lower_email"] != "alice@example.com" {
			t.Errorf("expected alice@example.com, got %v", rec["lower_email"])
		}
	})

	t.Run("LEN function", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT name, LEN(name) as name_len FROM users WHERE name = 'Alice Smith'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if int(toFloat64(rec["name_len"])) != 11 {
			t.Errorf("expected LEN=11, got %v", rec["name_len"])
		}
	})

	t.Run("COALESCE function", func(t *testing.T) {
		// Create a user with no email
		env.createEntity("/api/v1/users", map[string]interface{}{
			"name": "Charlie NoEmail",
			"age":  35,
		})
		data := env.oqlData("/api/v1/oql/query",
			"SELECT name, COALESCE(email, 'N/A') as contact FROM users WHERE name = 'Charlie NoEmail'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["contact"] != "N/A" {
			t.Errorf("expected COALESCE to return N/A, got %v", rec["contact"])
		}
	})

	t.Run("ABS function", func(t *testing.T) {
		env.createEntity("/api/v1/events", map[string]interface{}{
			"reading": -42.5,
			"label":   "abs_test",
		})
		data := env.oqlData("/api/v1/oql/query",
			"SELECT ABS(reading) as abs_val FROM events WHERE label = 'abs_test'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if toFloat64(rec["abs_val"]) != 42.5 {
			t.Errorf("expected ABS=42.5, got %v", rec["abs_val"])
		}
	})

	t.Run("YEAR/MONTH/DAY functions", func(t *testing.T) {
		env.createEntity("/api/v1/events", map[string]interface{}{
			"timestamp": "2025-06-15T10:30:00Z",
			"label":     "date_parts_test",
		})
		data := env.oqlData("/api/v1/oql/query",
			"SELECT YEAR(timestamp) as y, MONTH(timestamp) as m, DAY(timestamp) as d FROM events WHERE label = 'date_parts_test'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if int(toFloat64(rec["y"])) != 2025 {
			t.Errorf("expected YEAR=2025, got %v", rec["y"])
		}
		if int(toFloat64(rec["m"])) != 6 {
			t.Errorf("expected MONTH=6, got %v", rec["m"])
		}
		if int(toFloat64(rec["d"])) != 15 {
			t.Errorf("expected DAY=15, got %v", rec["d"])
		}
	})

	t.Run("SUBSTRING function", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT SUBSTRING(name, 1, 5) as short FROM users WHERE name = 'Alice Smith'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["short"] != "Alice" {
			t.Errorf("expected SUBSTRING='Alice', got %v", rec["short"])
		}
	})

	t.Run("LEFT and RIGHT functions", func(t *testing.T) {
		data := env.oqlData("/api/v1/oql/query",
			"SELECT LEFT(name, 5) as l, RIGHT(name, 5) as r FROM users WHERE name = 'Alice Smith'")
		if len(data) != 1 {
			t.Fatalf("expected 1 row, got %d", len(data))
		}
		rec := data[0].(map[string]interface{})
		if rec["l"] != "Alice" {
			t.Errorf("expected LEFT='Alice', got %v", rec["l"])
		}
		if rec["r"] != "Smith" {
			t.Errorf("expected RIGHT='Smith', got %v", rec["r"])
		}
	})
}
