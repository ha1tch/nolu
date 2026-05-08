// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// TestComparativeE2E_BlobVsAdapted runs identical workloads through the
// olu Store interface for both blob-storage entities and adapted-table
// entities, then prints a comparative timing report.
//
// The test creates two entity types on the SAME SQLiteStore:
//   - "products_blob"  — no schema registered, uses blob path
//   - "products_opt"   — schema registered, uses adapted table path
//
// Workloads: bulk create, random single get, filtered list (Go-side),
// bulk update, point delete, full list.
//
// Run: go test -v -run TestComparativeE2E_BlobVsAdapted -count=1 ./pkg/storage/
func TestComparativeE2E_BlobVsAdapted(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping comparative benchmark in -short mode")
	}

	sizes := []int{500, 2000, 5000}
	results := make([]benchResult, 0, len(sizes)*2)

	for _, n := range sizes {
		blobRes, optRes := runComparison(t, n)
		results = append(results, blobRes, optRes)
	}

	report := formatReport(results)
	t.Log("\n" + report)

	// Write report to working directory if available
	outPath := filepath.Join(os.TempDir(), "olu-blob-vs-adapted.md")
	if err := os.WriteFile(outPath, []byte(report), 0644); err == nil {
		t.Logf("Report written to %s", outPath)
	}
}

type benchResult struct {
	Mode  string // "blob" or "adapted"
	N     int
	Ops   map[string]time.Duration
	Count map[string]int
}

func runComparison(t *testing.T, n int) (benchResult, benchResult) {
	t.Helper()
	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, fmt.Sprintf("bench_%d.db", n))

	storeConfig := map[string]interface{}{"db_path": dbPath}
	store, err := NewStore("sqlite", storeConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	sqlStore := store.(*SQLiteStore)
	ctx := context.Background()

	// Register schema for the adapted entity
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"name":  map[string]interface{}{"type": "string"},
			"sku":   map[string]interface{}{"type": "string"},
			"category": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"electronics", "clothing", "food", "tools", "furniture"},
			},
			"price": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(10),
				"decimalScale":     float64(2),
			},
			"weight": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(8),
				"decimalScale":     float64(3),
			},
			"in_stock": map[string]interface{}{"type": "boolean"},
			"quantity": map[string]interface{}{"type": "integer"},
		},
		"required": []interface{}{"name", "sku", "price"},
	}

	if err := sqlStore.RegisterAdaptedEntity(ctx, "products_opt", schema); err != nil {
		t.Fatalf("RegisterAdaptedEntity: %v", err)
	}

	// Verify adapted table is registered
	if !sqlStore.AdaptedRegistry().IsAdapted("products_opt") {
		t.Fatal("products_opt not in adapted registry")
	}
	if sqlStore.AdaptedRegistry().IsAdapted("products_blob") {
		t.Fatal("products_blob should NOT be adapted")
	}

	// Generate deterministic test data
	rng := rand.New(rand.NewSource(42))
	categories := []string{"electronics", "clothing", "food", "tools", "furniture"}
	data := make([]map[string]interface{}, n)
	for i := 0; i < n; i++ {
		price := float64(rng.Intn(99900)+100) / 100.0 // 1.00 - 999.99
		weight := float64(rng.Intn(50000)+10) / 1000.0 // 0.010 - 50.009
		data[i] = map[string]interface{}{
			"name":     fmt.Sprintf("Product-%05d", i),
			"sku":      fmt.Sprintf("SKU-%05d", i),
			"category": categories[rng.Intn(len(categories))],
			"price":    fmt.Sprintf("%.2f", price),
			"weight":   fmt.Sprintf("%.3f", weight),
			"in_stock": rng.Intn(2) == 1,
			"quantity": float64(rng.Intn(1000)),
		}
	}

	blobRes := benchResult{
		Mode:  "blob",
		N:     n,
		Ops:   make(map[string]time.Duration),
		Count: make(map[string]int),
	}
	optRes := benchResult{
		Mode:  "adapted",
		N:     n,
		Ops:   make(map[string]time.Duration),
		Count: make(map[string]int),
	}

	// ---------------------------------------------------------------
	// 1. Bulk Create
	// ---------------------------------------------------------------
	blobIDs := make([]int, n)
	start := time.Now()
	for i := 0; i < n; i++ {
		id, err := store.Create(ctx, "products_blob", data[i])
		if err != nil {
			t.Fatalf("blob create %d: %v", i, err)
		}
		blobIDs[i] = id
	}
	blobRes.Ops["create_all"] = time.Since(start)
	blobRes.Count["create_all"] = n

	optIDs := make([]int, n)
	start = time.Now()
	for i := 0; i < n; i++ {
		id, err := store.Create(ctx, "products_opt", data[i])
		if err != nil {
			t.Fatalf("adapted create %d: %v", i, err)
		}
		optIDs[i] = id
	}
	optRes.Ops["create_all"] = time.Since(start)
	optRes.Count["create_all"] = n

	// ---------------------------------------------------------------
	// 2. Random Single Get (100 random reads)
	// ---------------------------------------------------------------
	nReads := 100
	if n < nReads {
		nReads = n
	}

	readIdxs := rng.Perm(n)[:nReads]

	start = time.Now()
	for _, idx := range readIdxs {
		_, err := store.Get(ctx, "products_blob", blobIDs[idx])
		if err != nil {
			t.Fatalf("blob get %d: %v", blobIDs[idx], err)
		}
	}
	blobRes.Ops["random_get"] = time.Since(start)
	blobRes.Count["random_get"] = nReads

	start = time.Now()
	for _, idx := range readIdxs {
		_, err := store.Get(ctx, "products_opt", optIDs[idx])
		if err != nil {
			t.Fatalf("adapted get %d: %v", optIDs[idx], err)
		}
	}
	optRes.Ops["random_get"] = time.Since(start)
	optRes.Count["random_get"] = nReads

	// ---------------------------------------------------------------
	// 3. Full List
	// ---------------------------------------------------------------
	start = time.Now()
	blobAll, err := store.List(ctx, "products_blob")
	if err != nil {
		t.Fatalf("blob list: %v", err)
	}
	blobRes.Ops["list_all"] = time.Since(start)
	blobRes.Count["list_all"] = len(blobAll)

	start = time.Now()
	optAll, err := store.List(ctx, "products_opt")
	if err != nil {
		t.Fatalf("adapted list: %v", err)
	}
	optRes.Ops["list_all"] = time.Since(start)
	optRes.Count["list_all"] = len(optAll)

	// Sanity checks
	if len(blobAll) != n {
		t.Errorf("blob list returned %d, want %d", len(blobAll), n)
	}
	if len(optAll) != n {
		t.Errorf("adapted list returned %d, want %d", len(optAll), n)
	}

	// ---------------------------------------------------------------
	// 4. Go-side Filtered Count (simulate: price > 500.00)
	// ---------------------------------------------------------------
	start = time.Now()
	blobFiltered := 0
	for _, item := range blobAll {
		if ps, ok := item["price"].(string); ok {
			// Parse and compare — blob stores as string via JSON
			var pf float64
			fmt.Sscanf(ps, "%f", &pf)
			if pf > 500.0 {
				blobFiltered++
			}
		} else if pf, ok := item["price"].(float64); ok {
			if pf > 500.0 {
				blobFiltered++
			}
		}
	}
	blobRes.Ops["filter_price"] = time.Since(start)
	blobRes.Count["filter_price"] = blobFiltered

	start = time.Now()
	optFiltered := 0
	for _, item := range optAll {
		if ps, ok := item["price"].(string); ok {
			var pf float64
			fmt.Sscanf(ps, "%f", &pf)
			if pf > 500.0 {
				optFiltered++
			}
		}
	}
	optRes.Ops["filter_price"] = time.Since(start)
	optRes.Count["filter_price"] = optFiltered

	// ---------------------------------------------------------------
	// 5. Bulk Update (update 10% of records)
	// ---------------------------------------------------------------
	nUpdates := n / 10
	if nUpdates < 1 {
		nUpdates = 1
	}
	updateIdxs := rng.Perm(n)[:nUpdates]

	start = time.Now()
	for _, idx := range updateIdxs {
		updated := copyMap(data[idx])
		updated["price"] = "999.99"
		updated["quantity"] = float64(0)
		if err := store.Update(ctx, "products_blob", blobIDs[idx], updated); err != nil {
			t.Fatalf("blob update %d: %v", blobIDs[idx], err)
		}
	}
	blobRes.Ops["update_batch"] = time.Since(start)
	blobRes.Count["update_batch"] = nUpdates

	start = time.Now()
	for _, idx := range updateIdxs {
		updated := copyMap(data[idx])
		updated["price"] = "999.99"
		updated["quantity"] = float64(0)
		if err := store.Update(ctx, "products_opt", optIDs[idx], updated); err != nil {
			t.Fatalf("adapted update %d: %v", optIDs[idx], err)
		}
	}
	optRes.Ops["update_batch"] = time.Since(start)
	optRes.Count["update_batch"] = nUpdates

	// ---------------------------------------------------------------
	// 6. Point Delete (delete 5% of records)
	// ---------------------------------------------------------------
	nDeletes := n / 20
	if nDeletes < 1 {
		nDeletes = 1
	}
	deleteIdxs := rng.Perm(n)[:nDeletes]

	start = time.Now()
	for _, idx := range deleteIdxs {
		if err := store.Delete(ctx, "products_blob", blobIDs[idx]); err != nil {
			t.Fatalf("blob delete %d: %v", blobIDs[idx], err)
		}
	}
	blobRes.Ops["delete_batch"] = time.Since(start)
	blobRes.Count["delete_batch"] = nDeletes

	start = time.Now()
	for _, idx := range deleteIdxs {
		if err := store.Delete(ctx, "products_opt", optIDs[idx]); err != nil {
			t.Fatalf("adapted delete %d: %v", optIDs[idx], err)
		}
	}
	optRes.Ops["delete_batch"] = time.Since(start)
	optRes.Count["delete_batch"] = nDeletes

	// ---------------------------------------------------------------
	// 7. SQL-level queries (this is where adapted tables shine)
	// ---------------------------------------------------------------
	// These bypass the Store interface and hit SQL directly, measuring
	// the query execution advantage of native columns vs json_extract.
	db := sqlStore.DB()

	// 7a. Filtered SELECT (WHERE on numeric field, ~50% selectivity)
	start = time.Now()
	rows, err := db.QueryContext(ctx,
		`SELECT data FROM entities WHERE tenant_id = 0 AND entity_type = 'products_blob' AND json_extract(data, '$.quantity') > 500`)
	if err != nil {
		t.Fatalf("blob SQL filter: %v", err)
	}
	blobSQLCount := drain(rows)
	blobRes.Ops["sql_filter"] = time.Since(start)
	blobRes.Count["sql_filter"] = blobSQLCount

	optTable := sqlStore.AdaptedRegistry().Get("products_opt").TableName()
	start = time.Now()
	rows, err = db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, name, sku, category, price, weight, in_stock, quantity FROM %s WHERE tenant_id = 0 AND quantity > 500`, optTable))
	if err != nil {
		t.Fatalf("adapted SQL filter: %v", err)
	}
	optSQLCount := drainN(rows, 8)
	optRes.Ops["sql_filter"] = time.Since(start)
	optRes.Count["sql_filter"] = optSQLCount

	// 7b. ORDER BY + LIMIT (top 20 by price)
	start = time.Now()
	rows, err = db.QueryContext(ctx,
		`SELECT data FROM entities WHERE tenant_id = 0 AND entity_type = 'products_blob' ORDER BY CAST(json_extract(data, '$.price') AS REAL) DESC LIMIT 20`)
	if err != nil {
		t.Fatalf("blob SQL sort: %v", err)
	}
	drain(rows)
	blobRes.Ops["sql_sort_limit"] = time.Since(start)
	blobRes.Count["sql_sort_limit"] = 20

	start = time.Now()
	rows, err = db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, name, sku, category, price, weight, in_stock, quantity FROM %s WHERE tenant_id = 0 ORDER BY price DESC LIMIT 20`, optTable))
	if err != nil {
		t.Fatalf("adapted SQL sort: %v", err)
	}
	drainN(rows, 8)
	optRes.Ops["sql_sort_limit"] = time.Since(start)
	optRes.Count["sql_sort_limit"] = 20

	// 7c. Range scan (price between 100.00 and 200.00 as scaled integers)
	start = time.Now()
	rows, err = db.QueryContext(ctx,
		`SELECT data FROM entities WHERE tenant_id = 0 AND entity_type = 'products_blob' AND CAST(json_extract(data, '$.price') AS REAL) BETWEEN 100.0 AND 200.0`)
	if err != nil {
		t.Fatalf("blob SQL range: %v", err)
	}
	blobRangeCount := drain(rows)
	blobRes.Ops["sql_range"] = time.Since(start)
	blobRes.Count["sql_range"] = blobRangeCount

	// For adapted, price is stored as scaled int: 100.00 = 10000, 200.00 = 20000
	start = time.Now()
	rows, err = db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, name, sku, category, price, weight, in_stock, quantity FROM %s WHERE tenant_id = 0 AND price BETWEEN 10000 AND 20000`, optTable))
	if err != nil {
		t.Fatalf("adapted SQL range: %v", err)
	}
	optRangeCount := drainN(rows, 8)
	optRes.Ops["sql_range"] = time.Since(start)
	optRes.Count["sql_range"] = optRangeCount

	// 7d. Text equality (point lookup by SKU)
	target := fmt.Sprintf("SKU-%05d", n/2)
	start = time.Now()
	rows, err = db.QueryContext(ctx,
		`SELECT data FROM entities WHERE tenant_id = 0 AND entity_type = 'products_blob' AND json_extract(data, '$.sku') = ?`, target)
	if err != nil {
		t.Fatalf("blob SQL text_eq: %v", err)
	}
	drain(rows)
	blobRes.Ops["sql_text_eq"] = time.Since(start)
	blobRes.Count["sql_text_eq"] = 1

	start = time.Now()
	rows, err = db.QueryContext(ctx,
		fmt.Sprintf(`SELECT id, name, sku, category, price, weight, in_stock, quantity FROM %s WHERE tenant_id = 0 AND sku = ?`, optTable), target)
	if err != nil {
		t.Fatalf("adapted SQL text_eq: %v", err)
	}
	drainN(rows, 8)
	optRes.Ops["sql_text_eq"] = time.Since(start)
	optRes.Count["sql_text_eq"] = 1

	return blobRes, optRes
}

// drainN reads all rows scanning n columns (discards values)
func drainN(rows *sql.Rows, ncols int) int {
	count := 0
	dest := make([]interface{}, ncols)
	for i := range dest {
		dest[i] = new(interface{})
	}
	for rows.Next() {
		rows.Scan(dest...)
		count++
	}
	rows.Close()
	return count
}

func copyMap(m map[string]interface{}) map[string]interface{} {
	c := make(map[string]interface{}, len(m))
	for k, v := range m {
		c[k] = v
	}
	return c
}

func formatReport(results []benchResult) string {
	var b []byte

	b = append(b, []byte("# olu Store API: Blob vs Adapted Table Benchmark\n\n")...)
	b = append(b, []byte(fmt.Sprintf("**Date:** %s\n", time.Now().Format("2006-01-02")))...)
	b = append(b, []byte("**Method:** End-to-end through `storage.Store` interface (Create/Get/List/Update/Delete)\n")...)
	b = append(b, []byte("**Schema:** 7 fields (2 string, 1 enum, 2 decimal, 1 boolean, 1 integer)\n")...)
	b = append(b, []byte("**Decimal fields:** price (10,2), weight (8,3) — scaled integer storage\n\n")...)

	// Group by size
	type pair struct {
		blob, adapted benchResult
	}
	bySize := make(map[int]*pair)
	for _, r := range results {
		p, ok := bySize[r.N]
		if !ok {
			p = &pair{}
			bySize[r.N] = p
		}
		if r.Mode == "blob" {
			p.blob = r
		} else {
			p.adapted = r
		}
	}

	ops := []struct {
		key  string
		name string
		unit string
	}{
		{"create_all", "Bulk create", "total"},
		{"random_get", "Random get (100)", "total"},
		{"list_all", "List all", "total"},
		{"filter_price", "Go-side filter", "total"},
		{"update_batch", "Bulk update (10%)", "total"},
		{"delete_batch", "Bulk delete (5%)", "total"},
		{"sql_filter", "SQL WHERE quantity>500", "total"},
		{"sql_sort_limit", "SQL ORDER BY LIMIT 20", "total"},
		{"sql_range", "SQL range (price)", "total"},
		{"sql_text_eq", "SQL point lookup (SKU)", "total"},
	}

	// Collect sizes in order
	var sizes []int
	for s := range bySize {
		sizes = append(sizes, s)
	}
	// Sort
	for i := 0; i < len(sizes); i++ {
		for j := i + 1; j < len(sizes); j++ {
			if sizes[i] > sizes[j] {
				sizes[i], sizes[j] = sizes[j], sizes[i]
			}
		}
	}

	for _, n := range sizes {
		p := bySize[n]
		b = append(b, []byte(fmt.Sprintf("## %d records\n\n", n))...)
		b = append(b, []byte("| Operation | Blob | Adapted | Speedup | Count |\n")...)
		b = append(b, []byte("|---|---|---|---|---|\n")...)

		for _, op := range ops {
			bDur := p.blob.Ops[op.key]
			aDur := p.adapted.Ops[op.key]
			count := p.blob.Count[op.key]

			speedup := "—"
			if aDur > 0 {
				ratio := float64(bDur) / float64(aDur)
				if ratio >= 1.0 {
					speedup = fmt.Sprintf("%.1fx faster", ratio)
				} else {
					speedup = fmt.Sprintf("%.1fx slower", 1.0/ratio)
				}
			}

			b = append(b, []byte(fmt.Sprintf("| %s | %s | %s | %s | %d |\n",
				op.name, fmtDuration(bDur), fmtDuration(aDur), speedup, count))...)
		}
		b = append(b, '\n')
	}

	b = append(b, []byte("## Notes\n\n")...)
	b = append(b, []byte("- Both paths run through the same `SQLiteStore` instance against the same database file.\n")...)
	b = append(b, []byte("- Blob path: JSON marshal/unmarshal on every write and read, `json_extract` for queries.\n")...)
	b = append(b, []byte("- Adapted path: column-per-field storage, decimal normalise/denormalise (scaled int64), no JSON parsing on read.\n")...)
	b = append(b, []byte("- CRUD operations (create/get/list/update/delete) go through the full `Store` interface with all overhead.\n")...)
	b = append(b, []byte("- SQL operations hit the database directly, measuring raw query advantage of native columns vs `json_extract`.\n")...)
	b = append(b, []byte("- \"Go-side filter\" measures post-fetch filtering only (both paths fetch all rows first via List).\n")...)
	b = append(b, []byte("- Blob SQL queries use `CAST(json_extract(...) AS REAL)` for numeric comparisons and ordering.\n")...)
	b = append(b, []byte("- Adapted SQL queries use native INTEGER column comparisons (scaled int64 for decimals).\n")...)
	b = append(b, []byte("- All operations include transaction overhead, WAL writes, and any internal retries.\n")...)

	return string(b)
}

func fmtDuration(d time.Duration) string {
	if d < time.Millisecond {
		return fmt.Sprintf("%d us", d.Microseconds())
	}
	if d < time.Second {
		return fmt.Sprintf("%.1f ms", float64(d.Microseconds())/1000.0)
	}
	return fmt.Sprintf("%.2f s", d.Seconds())
}

// marshalJSON is used to verify blob data is properly handled
func marshalJSON(v interface{}) string { //nolint:unused
	b, _ := json.Marshal(v)
	return string(b)
}
