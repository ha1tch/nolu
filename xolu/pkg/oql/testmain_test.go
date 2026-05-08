// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"testing"

	"github.com/ha1tch/xolu/pkg/storage"
)

// goldenPath holds the path to the pre-seeded test database.
// Set once by TestMain, used by all env constructors via goldenCopy.
var goldenPath string

// goldenN is the record count per entity in the golden database.
const goldenN = 500

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "olu-golden-*")
	if err != nil {
		fmt.Fprintf(os.Stderr, "create golden dir: %v\n", err)
		os.Exit(1)
	}

	goldenPath = filepath.Join(dir, "olu.golden")
	if err := seedGolden(goldenPath); err != nil {
		fmt.Fprintf(os.Stderr, "seed golden: %v\n", err)
		os.RemoveAll(dir)
		os.Exit(1)
	}

	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// seedGolden creates the golden database with all entity types and data
// needed by the OQL test suite. It runs once per `go test` invocation.
//
// Entities seeded:
//
//   items  (adapted) — 500 rows, used by adapted_pushdown + adapted_full_pushdown
//   sales  (adapted) — 500 rows, used by aggregate_pushdown
//   sensors (blob)   — 500 rows, used by equivalence
//   readings (blob)  — 500 rows, used by equivalence
//   assets  (blob)   — 500 rows, used by equivalence
//   events  (blob)   — 500 rows, used by equivalence
func seedGolden(dbPath string) error {
	store, err := storage.NewSQLiteStore(dbPath, storage.SQLiteConfig{})
	if err != nil {
		return fmt.Errorf("NewSQLiteStore: %w", err)
	}
	defer store.Close()

	// Disable fsync during seeding — the golden file is ephemeral and
	// will be copied before use, so durability is irrelevant.
	store.DB().Exec("PRAGMA synchronous=OFF")
	store.DB().Exec("PRAGMA journal_mode=MEMORY")

	ctx := context.Background()

	if err := seedItems(ctx, store); err != nil {
		return fmt.Errorf("seed items: %w", err)
	}
	if err := seedSales(ctx, store); err != nil {
		return fmt.Errorf("seed sales: %w", err)
	}
	if err := seedEquivalence(ctx, store); err != nil {
		return fmt.Errorf("seed equivalence: %w", err)
	}

	return nil
}

// seedItems registers the adapted "items" entity and populates it.
// Unified schema from adapted_pushdown_test and adapted_full_pushdown_test.
//
// Registration goes through the store API (creates the table + metadata).
// Row inserts use raw SQL in a single transaction for speed.
func seedItems(ctx context.Context, store *storage.SQLiteStore) error {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"region": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"north", "south", "east", "west"},
			},
			"product": map[string]interface{}{"type": "string"},
			"category": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"electronics", "clothing", "food", "tools"},
			},
			"amount": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(10),
				"decimalScale":     float64(2),
			},
			"unit_price": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(8),
				"decimalScale":     float64(4),
			},
			"quantity": map[string]interface{}{"type": "integer"},
			"active":   map[string]interface{}{"type": "boolean"},
		},
		"required": []interface{}{"region", "product", "category", "amount", "quantity"},
	}

	if err := store.RegisterAdaptedEntity(ctx, "items", schema); err != nil {
		return err
	}

	// Columns alphabetical: active, amount, category, product, quantity, region, unit_price
	db := store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO olu_items (id, tenant_id, active, amount, category, product, quantity, region, unit_price) VALUES (?, 0, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer ins.Close()

	rng := rand.New(rand.NewSource(42))
	regions := []string{"north", "south", "east", "west"}
	products := []string{"widget", "gadget", "gizmo", "doohickey", "thingamajig"}
	categories := []string{"electronics", "clothing", "food", "tools"}

	for i := 0; i < goldenN; i++ {
		// Decimal normalisation: amount(10,2) → scale by 100, unit_price(8,4) → scale by 10000
		amountRaw := rng.Intn(99900) + 100   // cents: 100..99999
		unitPriceRaw := rng.Intn(500000) + 100 // ten-thousandths: 100..500099
		active := 0
		if rng.Intn(2) == 1 {
			active = 1
		}
		if _, err := ins.ExecContext(ctx,
			i+1, active, amountRaw, categories[rng.Intn(len(categories))],
			products[rng.Intn(len(products))], rng.Intn(100)+1,
			regions[rng.Intn(len(regions))], unitPriceRaw,
		); err != nil {
			return fmt.Errorf("items row %d: %w", i, err)
		}
	}

	// Update entity sequence
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entity_sequences (tenant_id, entity_type, next_id) VALUES (0, 'items', ?)
		 ON CONFLICT(tenant_id, entity_type) DO UPDATE SET next_id = ?`, goldenN, goldenN); err != nil {
		return fmt.Errorf("sequence items: %w", err)
	}

	return tx.Commit()
}

// seedSales registers the adapted "sales" entity and populates it.
// Matches the original aggregate_pushdown_test schema.
//
// Same approach as seedItems: API for registration, raw SQL for data.
func seedSales(ctx context.Context, store *storage.SQLiteStore) error {
	schema := map[string]interface{}{
		"properties": map[string]interface{}{
			"region": map[string]interface{}{
				"type": "string",
				"enum": []interface{}{"north", "south", "east", "west"},
			},
			"product": map[string]interface{}{"type": "string"},
			"amount": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(10),
				"decimalScale":     float64(2),
			},
			"unit_price": map[string]interface{}{
				"type":             "string",
				"format":           "decimal",
				"decimalPrecision": float64(8),
				"decimalScale":     float64(4),
			},
			"quantity": map[string]interface{}{"type": "integer"},
			"active":   map[string]interface{}{"type": "boolean"},
		},
		"required": []interface{}{"region", "product", "amount", "quantity"},
	}

	if err := store.RegisterAdaptedEntity(ctx, "sales", schema); err != nil {
		return err
	}

	// Columns alphabetical: active, amount, product, quantity, region, unit_price
	db := store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO olu_sales (id, tenant_id, active, amount, product, quantity, region, unit_price) VALUES (?, 0, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	defer ins.Close()

	rng := rand.New(rand.NewSource(42))
	regions := []string{"north", "south", "east", "west"}
	products := []string{"widget", "gadget", "gizmo", "doohickey", "thingamajig"}

	for i := 0; i < goldenN; i++ {
		amountRaw := rng.Intn(99900) + 100
		unitPriceRaw := rng.Intn(500000) + 100
		active := 0
		if rng.Intn(2) == 1 {
			active = 1
		}
		if _, err := ins.ExecContext(ctx,
			i+1, active, amountRaw, products[rng.Intn(len(products))],
			rng.Intn(100)+1, regions[rng.Intn(len(regions))], unitPriceRaw,
		); err != nil {
			return fmt.Errorf("sales row %d: %w", i, err)
		}
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO entity_sequences (tenant_id, entity_type, next_id) VALUES (0, 'sales', ?)
		 ON CONFLICT(tenant_id, entity_type) DO UPDATE SET next_id = ?`, goldenN, goldenN); err != nil {
		return fmt.Errorf("sequence sales: %w", err)
	}

	return tx.Commit()
}

// seedEquivalence populates the blob-table entities used by equivalence_test.
// Uses raw SQL in a single transaction for speed — the store.Create API
// would start a separate transaction per row, which is ~40x slower.
func seedEquivalence(ctx context.Context, store *storage.SQLiteStore) error {
	db := store.DB()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback()

	insert, err := tx.PrepareContext(ctx,
		`INSERT INTO entities (tenant_id, entity_type, id, data) VALUES (0, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare insert: %w", err)
	}
	defer insert.Close()

	statuses := []string{"active", "inactive", "maintenance", "decommissioned"}
	categories := []string{"temperature", "pressure", "humidity", "flow"}

	// --- sensors (500 rows) ---
	for i := 0; i < goldenN; i++ {
		nullable := interface{}(nil)
		if i%2 == 0 {
			nullable = fmt.Sprintf("val_%d", i)
		}
		data := fmt.Sprintf(
			`{"id":%d,"code":"SENS-%04d","status":"%s","category":"%s","value":%s,"floor":%d,"tenant_id":"t%d","nullable":%s}`,
			i+1, i, statuses[i%len(statuses)], categories[i%len(categories)],
			fmt.Sprintf("%.1f", float64(i)*1.5), i%10, i%3,
			jsonNullOrStr(nullable),
		)
		if _, err := insert.ExecContext(ctx, "sensors", i+1, data); err != nil {
			return fmt.Errorf("sensor %d: %w", i, err)
		}
	}

	// --- readings (500 rows) ---
	for i := 0; i < goldenN; i++ {
		data := fmt.Sprintf(
			`{"id":%d,"sensor_id":"SENS-%04d","value":%s,"timestamp":"2026-02-%02dT%02d:00:00Z","quality":%d}`,
			i+1, i%100, fmt.Sprintf("%.1f", float64(i%1000)*0.1),
			(i%28)+1, i%24, i%5,
		)
		if _, err := insert.ExecContext(ctx, "readings", i+1, data); err != nil {
			return fmt.Errorf("reading %d: %w", i, err)
		}
	}

	// --- assets (500 rows) ---
	for i := 0; i < goldenN; i++ {
		data := fmt.Sprintf(
			`{"id":%d,"code":"ASSET-%04d","status":"%s","value":%s,"zone":"zone_%d"}`,
			i+1, i, statuses[i%len(statuses)],
			fmt.Sprintf("%.1f", float64(i)*10.0), i%5,
		)
		if _, err := insert.ExecContext(ctx, "assets", i+1, data); err != nil {
			return fmt.Errorf("asset %d: %w", i, err)
		}
	}

	// --- events (500 rows) ---
	for i := 0; i < goldenN; i++ {
		activeStr := "false"
		if i%3 == 0 {
			activeStr = "true"
		}
		data := fmt.Sprintf(
			`{"id":%d,"type":"%s","severity":%d,"message":"Event %d occurred on sensor SENS-%04d","active":%s}`,
			i+1, categories[i%len(categories)], i%5, i, i%100, activeStr,
		)
		if _, err := insert.ExecContext(ctx, "events", i+1, data); err != nil {
			return fmt.Errorf("event %d: %w", i, err)
		}
	}

	// Update entity_sequences for all four entity types
	seqSQL := `INSERT INTO entity_sequences (tenant_id, entity_type, next_id) VALUES (0, ?, ?)
		ON CONFLICT(tenant_id, entity_type) DO UPDATE SET next_id = ?`
	for _, etype := range []string{"sensors", "readings", "assets", "events"} {
		if _, err := tx.ExecContext(ctx, seqSQL, etype, goldenN, goldenN); err != nil {
			return fmt.Errorf("sequence %s: %w", etype, err)
		}
	}

	return tx.Commit()
}

// jsonNullOrStr returns a JSON-safe representation of a nullable value.
func jsonNullOrStr(v interface{}) string {
	if v == nil {
		return "null"
	}
	return fmt.Sprintf("%q", v)
}
