// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/rs/zerolog/log"
)

// calibrationRows is the number of rows seeded into the temp table
// for the micro-benchmark. 200 rows is enough to get stable timing
// ratios without dominating startup time.
const calibrationRows = 200

// calibrationIterations is the number of times each benchmark is
// repeated to reduce variance. Each iteration processes all rows.
const calibrationIterations = 5

// Calibrate runs a short micro-benchmark against the given SQLite
// database to measure the relative speed of Go JSON processing
// versus SQLite's query engine on the current hardware. It returns
// a HardwareProfile with thresholds tuned to the measured ratios.
//
// The benchmark takes approximately 100-300ms depending on hardware.
// It creates and drops a temporary table, so it has no lasting side
// effects on the database.
//
// If calibration fails for any reason, it returns the VPS default
// profile with an error.
func Calibrate(db *sql.DB) (*HardwareProfile, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	// --- Setup: create temp table with representative JSON blobs ---
	if err := calibrationSetup(ctx, db); err != nil {
		def := DefaultProfile()
		return &def, fmt.Errorf("calibration setup: %w", err)
	}
	defer calibrationTeardown(db)

	// --- Benchmark 1: Go JSON processing rate ---
	goNsPerRow, err := benchGoPath(ctx, db)
	if err != nil {
		def := DefaultProfile()
		return &def, fmt.Errorf("calibration Go benchmark: %w", err)
	}

	// --- Benchmark 2: SQLite json_extract query rate ---
	sqlNsPerRow, err := benchSQLPath(ctx, db)
	if err != nil {
		def := DefaultProfile()
		return &def, fmt.Errorf("calibration SQL benchmark: %w", err)
	}

	// --- Benchmark 3: SQLite temp B-tree cost ---
	tempBTreeNsPerRow, err := benchTempBTree(ctx, db)
	if err != nil {
		def := DefaultProfile()
		return &def, fmt.Errorf("calibration temp B-tree benchmark: %w", err)
	}

	// --- Derive thresholds from measured ratios ---
	profile := deriveProfile(goNsPerRow, sqlNsPerRow, tempBTreeNsPerRow)

	log.Info().
		Str("profile", profile.Name).
		Float64("goNsPerRow", goNsPerRow).
		Float64("sqlNsPerRow", sqlNsPerRow).
		Float64("tempBTreeNsPerRow", tempBTreeNsPerRow).
		Float64("goSqlRatio", goNsPerRow/sqlNsPerRow).
		Int("blobThreshold", profile.BlobPushThreshold).
		Int("nonCoveringThreshold", profile.NonCoveringThreshold).
		Int("tempBTree1Threshold", profile.TempBTree1Threshold).
		Int("tempBTree2Threshold", profile.TempBTree2Threshold).
		Msg("Hardware calibration complete")

	return profile, nil
}

// calibrationSetup creates a temp table with representative JSON blobs.
func calibrationSetup(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS _olu_calibration (
			id INTEGER PRIMARY KEY,
			data TEXT NOT NULL,
			region TEXT,
			category TEXT,
			quantity INTEGER
		)
	`)
	if err != nil {
		return err
	}

	// Seed with deterministic data
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() //nolint:errcheck // no-op if commit succeeded

	ins, err := tx.PrepareContext(ctx,
		`INSERT INTO _olu_calibration (id, data, region, category, quantity) VALUES (?, ?, ?, ?, ?)`)
	if err != nil {
		return err
	}
	defer ins.Close()

	regions := []string{"north", "south", "east", "west"}
	categories := []string{"electronics", "clothing", "food", "tools"}

	for i := 0; i < calibrationRows; i++ {
		region := regions[i%len(regions)]
		category := categories[i%len(categories)]
		quantity := (i*17 + 3) % 200

		blob, _ := json.Marshal(map[string]interface{}{
			"region":     region,
			"category":   category,
			"product":    fmt.Sprintf("item_%d", i),
			"amount":     float64(i*13+7) / 100.0,
			"quantity":   quantity,
			"active":     i%3 != 0,
			"unit_price": float64(i*7+11) / 1000.0,
			"tags":       fmt.Sprintf("tag_%d,tag_%d", i%5, i%7),
		})

		if _, err := ins.ExecContext(ctx, i, string(blob), region, category, quantity); err != nil {
			return err
		}
	}

	// Create indexes matching adapted table pattern
	for _, col := range []string{"region", "category", "quantity"} {
		if _, err := tx.ExecContext(ctx,
			fmt.Sprintf("CREATE INDEX IF NOT EXISTS idx_calib_%s ON _olu_calibration(%s)", col, col)); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// calibrationTeardown drops the temp table.
func calibrationTeardown(db *sql.DB) {
	_, _ = db.Exec("DROP TABLE IF EXISTS _olu_calibration")
}

// benchGoPath measures the per-row cost of fetching all blobs and
// deserialising them with json.Unmarshal — the Go fallback path.
func benchGoPath(ctx context.Context, db *sql.DB) (float64, error) {
	var totalNs int64

	for iter := 0; iter < calibrationIterations; iter++ {
		rows, err := db.QueryContext(ctx, "SELECT data FROM _olu_calibration")
		if err != nil {
			return 0, err
		}

		start := time.Now()
		count := 0
		for rows.Next() {
			var blob string
			if err := rows.Scan(&blob); err != nil {
				rows.Close()
				return 0, err
			}
			var m map[string]interface{}
			_ = json.Unmarshal([]byte(blob), &m) // error means empty map — handled by zero count
			count++
		}
		rows.Close()
		totalNs += time.Since(start).Nanoseconds()
	}

	return float64(totalNs) / float64(calibrationIterations*calibrationRows), nil
}

// benchSQLPath measures the per-row cost of a filtered json_extract
// query — the blob push-down path.
func benchSQLPath(ctx context.Context, db *sql.DB) (float64, error) {
	var totalNs int64

	// Use a WHERE that matches ~25% of rows (1 of 4 categories)
	query := `SELECT json_extract(data, '$.region'), json_extract(data, '$.quantity')
	          FROM _olu_calibration
	          WHERE json_extract(data, '$.category') = ?`

	for iter := 0; iter < calibrationIterations; iter++ {
		start := time.Now()
		rows, err := db.QueryContext(ctx, query, "electronics")
		if err != nil {
			return 0, err
		}
		count := 0
		for rows.Next() {
			var region string
			var quantity int
			_ = rows.Scan(&region, &quantity)
			count++
		}
		rows.Close()
		elapsed := time.Since(start).Nanoseconds()
		// Normalise to per-row cost across all scanned rows (not just matched)
		totalNs += elapsed
	}

	return float64(totalNs) / float64(calibrationIterations*calibrationRows), nil
}

// benchTempBTree measures the per-row overhead of a query that forces
// SQLite to materialise temp B-trees: multi-key GROUP BY with a
// misaligned ORDER BY.
func benchTempBTree(ctx context.Context, db *sql.DB) (float64, error) {
	var totalNs int64

	// This query forces two temp B-trees:
	//   1. GROUP BY region, category (no composite index)
	//   2. ORDER BY category (misaligned with GROUP BY leading column)
	query := `SELECT region, category, COUNT(*), SUM(quantity)
	          FROM _olu_calibration
	          GROUP BY region, category
	          ORDER BY category`

	for iter := 0; iter < calibrationIterations; iter++ {
		start := time.Now()
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			return 0, err
		}
		for rows.Next() {
			var region, category string
			var count, sum int
			_ = rows.Scan(&region, &category, &count, &sum)
		}
		rows.Close()
		totalNs += time.Since(start).Nanoseconds()
	}

	return float64(totalNs) / float64(calibrationIterations*calibrationRows), nil
}

// deriveProfile computes threshold values from measured per-row costs.
//
// The key insight: push-down wins when SQLite's per-row cost is lower
// than Go's. But for complex queries, SQLite has additional overhead
// (temp B-trees, non-covering scans) that shifts the crossover point.
//
// The Go path cost is roughly constant per row (json.Unmarshal + map
// operations). The SQLite path cost has a fixed component (query
// planning, result materialisation) plus a per-row component that
// varies with query complexity.
//
// We estimate crossover points as:
//   threshold ≈ fixedCost / (goPerRow - sqlPerRow)
// where fixedCost is estimated from the temp B-tree benchmark.
func deriveProfile(goNsPerRow, sqlNsPerRow, tempBTreeNsPerRow float64) *HardwareProfile {
	profile := &HardwareProfile{Name: "calibrated"}

	// Go/SQL ratio: how many times faster is SQL per row?
	ratio := goNsPerRow / sqlNsPerRow
	if ratio < 1.0 {
		// SQL is slower than Go per row — very unusual, use conservative
		// dedicated-class thresholds.
		*profile = ProfileDedicated
		profile.Name = "calibrated"
		return profile
	}

	// Blob push-down threshold: push-down is worth it when the per-row
	// saving (goNsPerRow - sqlNsPerRow) covers the fixed overhead of
	// query planning + CountEntities (~200µs).
	fixedOverheadNs := 200_000.0 // ~200µs for CountEntities + plan
	savingPerRow := goNsPerRow - sqlNsPerRow
	if savingPerRow > 0 {
		blobThreshold := int(math.Ceil(fixedOverheadNs / savingPerRow))
		profile.BlobPushThreshold = clampThreshold(blobThreshold, 10, 500)
	} else {
		profile.BlobPushThreshold = 500 // SQL not faster, use high threshold
	}

	// Temp B-tree overhead per row: the difference between the temp
	// B-tree benchmark and a simple SQL scan gives us the materialisation
	// cost.
	tempOverheadPerRow := tempBTreeNsPerRow - sqlNsPerRow
	if tempOverheadPerRow < 0 {
		tempOverheadPerRow = 0
	}

	// Non-covering threshold: the overhead is roughly proportional to
	// the data page access cost, which we approximate as half the
	// temp B-tree overhead (an index miss is cheaper than a full sort).
	nonCoveringOverheadPerRow := tempOverheadPerRow * 0.5
	if nonCoveringOverheadPerRow > 0 && savingPerRow > nonCoveringOverheadPerRow {
		// Push-down still wins per row, but by less. The fixed cost
		// takes more rows to amortise.
		netSaving := savingPerRow - nonCoveringOverheadPerRow
		profile.NonCoveringThreshold = clampThreshold(
			int(math.Ceil(fixedOverheadNs/netSaving)), 100, 10000)
	} else {
		// Push-down per-row advantage is erased by non-covering overhead.
		// Need significant rows for the optimizer's plan to pay off.
		profile.NonCoveringThreshold = clampThreshold(
			int(math.Ceil(fixedOverheadNs/savingPerRow*3)), 100, 10000)
	}

	// 1 temp B-tree: crossover where Go's brute force beats SQL + 1 sort
	if savingPerRow > tempOverheadPerRow {
		netSaving := savingPerRow - tempOverheadPerRow
		profile.TempBTree1Threshold = clampThreshold(
			int(math.Ceil(fixedOverheadNs/netSaving)), 50, 5000)
	} else {
		// One temp B-tree erases the push-down advantage. Need many rows
		// for SQLite's O(n log n) sort to beat Go's O(n) scan.
		profile.TempBTree1Threshold = clampThreshold(
			int(goNsPerRow/tempOverheadPerRow*100), 200, 5000)
	}

	// 2+ temp B-trees: double the materialisation overhead
	tempOverhead2x := tempOverheadPerRow * 2
	if savingPerRow > tempOverhead2x {
		netSaving := savingPerRow - tempOverhead2x
		profile.TempBTree2Threshold = clampThreshold(
			int(math.Ceil(fixedOverheadNs/netSaving)), 200, 20000)
	} else {
		profile.TempBTree2Threshold = clampThreshold(
			profile.TempBTree1Threshold*3, 500, 20000)
	}

	return profile
}

// clampThreshold constrains a computed threshold to sensible bounds.
func clampThreshold(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}
