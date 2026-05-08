// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server_test

import (
	"encoding/json"
	"fmt"
	"testing"
)

// TestAdaptedTable_SchemaToDecimalCRUD exercises the full path:
// POST schema (triggers adapted table registration) -> CRUD with decimal
// fields through the HTTP API -> verify values round-trip correctly.
func TestAdaptedTable_SchemaToDecimalCRUD(t *testing.T) {
	ts := setupSQLiteTestServer(t)
	defer ts.ts.Close()
	if ts.sqliteStore != nil {
		defer ts.sqliteStore.Close()
	}

	// 1. POST a schema with decimal fields
	schema := map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"description": map[string]interface{}{"type": "string"},
			"amount": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(10),
				"decimalScale":     float64(2),
			},
			"unit_price": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(12),
				"decimalScale":     float64(4),
			},
		},
		"required": []interface{}{"description"},
	}

	resp, body := ts.doRequest("POST", "/api/v1/schema/line_items", schema)
	if resp.StatusCode != 201 {
		t.Fatalf("POST schema: got %d, body: %s", resp.StatusCode, string(body))
	}

	// 2. Create entities with decimal values
	cases := []struct {
		desc       string
		amount     string
		unitPrice  string
		wantAmount string
		wantUnit   string
	}{
		{"Item A", "1234.56", "99.9900", "1234.56", "99.9900"},
		{"Credit", "-42.50", "-0.0100", "-42.50", "-0.0100"},
		{"Zero", "0", "0", "0.00", "0.0000"},
		{"Small", "0.01", "0.0001", "0.01", "0.0001"},
	}

	ids := make([]int, len(cases))
	for i, tc := range cases {
		data := map[string]interface{}{
			"description": tc.desc,
			"amount":      tc.amount,
			"unit_price":  tc.unitPrice,
		}
		resp, body = ts.doRequest("POST", "/api/v1/line_items", data)
		if resp.StatusCode != 201 {
			t.Fatalf("POST line_items[%d]: got %d, body: %s", i, resp.StatusCode, string(body))
		}

		var result map[string]interface{}
		json.Unmarshal(body, &result)
		id, ok := result["id"].(float64)
		if !ok {
			t.Fatalf("POST line_items[%d]: no id in response: %v", i, result)
		}
		ids[i] = int(id)
	}

	// 3. GET each entity and verify decimal values round-trip
	for i, tc := range cases {
		resp, body = ts.doRequest("GET", fmt.Sprintf("/api/v1/line_items/%d", ids[i]), nil)
		if resp.StatusCode != 200 {
			t.Fatalf("GET line_items/%d: got %d", ids[i], resp.StatusCode)
		}

		var entity map[string]interface{}
		json.Unmarshal(body, &entity)

		got := entity["amount"]
		if got != tc.wantAmount {
			t.Errorf("GET line_items/%d: amount = %v, want %q", ids[i], got, tc.wantAmount)
		}
		got = entity["unit_price"]
		if got != tc.wantUnit {
			t.Errorf("GET line_items/%d: unit_price = %v, want %q", ids[i], got, tc.wantUnit)
		}
	}

	// 4. PUT (update) with new decimal values
	update := map[string]interface{}{
		"description": "Updated item",
		"amount":      "-999.99",
		"unit_price":  "50.0000",
	}
	resp, body = ts.doRequest("PUT", fmt.Sprintf("/api/v1/line_items/%d", ids[0]), update)
	if resp.StatusCode != 200 {
		t.Fatalf("PUT line_items/%d: got %d, body: %s", ids[0], resp.StatusCode, string(body))
	}

	resp, body = ts.doRequest("GET", fmt.Sprintf("/api/v1/line_items/%d", ids[0]), nil)
	if resp.StatusCode != 200 {
		t.Fatalf("GET after PUT: got %d", resp.StatusCode)
	}
	var updated map[string]interface{}
	json.Unmarshal(body, &updated)
	if updated["amount"] != "-999.99" {
		t.Errorf("after PUT: amount = %v, want %q", updated["amount"], "-999.99")
	}

	// 5. DELETE
	resp, _ = ts.doRequest("DELETE", fmt.Sprintf("/api/v1/line_items/%d", ids[0]), nil)
	if resp.StatusCode != 200 && resp.StatusCode != 204 {
		t.Errorf("DELETE: got %d", resp.StatusCode)
	}

	// Verify deleted
	resp, _ = ts.doRequest("GET", fmt.Sprintf("/api/v1/line_items/%d", ids[0]), nil)
	if resp.StatusCode != 404 {
		t.Errorf("GET after DELETE: got %d, want 404", resp.StatusCode)
	}

	// 6. LIST remaining items
	resp, body = ts.doRequest("GET", "/api/v1/line_items", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("LIST: got %d", resp.StatusCode)
	}
	var listResp map[string]interface{}
	json.Unmarshal(body, &listResp)
	listData, _ := listResp["data"].([]interface{})
	if len(listData) != len(cases)-1 {
		t.Errorf("LIST: got %d items, want %d (body: %s)", len(listData), len(cases)-1, string(body))
	}
}
