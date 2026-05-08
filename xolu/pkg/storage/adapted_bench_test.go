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

	_ "modernc.org/sqlite"
)

// BenchmarkAdaptedVsBlob compares native-column queries against json_extract
// queries at various dataset sizes. Each sub-benchmark runs filtered SELECTs,
// sorted SELECTs, and range scans against both table layouts.
//
// Target: complete in <60s total.
func BenchmarkAdaptedVsBlob(b *testing.B) {
	sizes := []int{100, 1_000, 10_000, 50_000}

	for _, n := range sizes {
		dbPath := filepath.Join(b.TempDir(), fmt.Sprintf("bench_%d.db", n))
		db := setupBenchDB(b, dbPath, n)

		b.Run(fmt.Sprintf("n=%d/blob_filter", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT data FROM blob_entities WHERE entity_type = 'users' AND json_extract(data, '$.age') > 50`)
				if err != nil {
					b.Fatal(err)
				}
				count := drain(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/adapted_filter", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT id, name, email, age, active FROM adapted_users WHERE age > 50`)
				if err != nil {
					b.Fatal(err)
				}
				count := drainAdapted(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/adapted_indexed_filter", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT id, name, email, age, active FROM adapted_users_idx WHERE age > 50`)
				if err != nil {
					b.Fatal(err)
				}
				count := drainAdapted(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/blob_sort", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT data FROM blob_entities WHERE entity_type = 'users' ORDER BY json_extract(data, '$.age') LIMIT 20`)
				if err != nil {
					b.Fatal(err)
				}
				count := drain(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/adapted_sort", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT id, name, email, age, active FROM adapted_users ORDER BY age LIMIT 20`)
				if err != nil {
					b.Fatal(err)
				}
				count := drainAdapted(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/blob_range", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT data FROM blob_entities WHERE entity_type = 'users' AND json_extract(data, '$.age') BETWEEN 25 AND 35`)
				if err != nil {
					b.Fatal(err)
				}
				count := drain(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/adapted_range", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT id, name, email, age, active FROM adapted_users_idx WHERE age BETWEEN 25 AND 35`)
				if err != nil {
					b.Fatal(err)
				}
				count := drainAdapted(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/blob_text_eq", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT data FROM blob_entities WHERE entity_type = 'users' AND json_extract(data, '$.email') = 'user500@example.com'`)
				if err != nil {
					b.Fatal(err)
				}
				count := drain(rows)
				_ = count
			}
		})

		b.Run(fmt.Sprintf("n=%d/adapted_text_eq", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				rows, err := db.Query(
					`SELECT id, name, email, age, active FROM adapted_users_idx WHERE email = 'user500@example.com'`)
				if err != nil {
					b.Fatal(err)
				}
				count := drainAdapted(rows)
				_ = count
			}
		})

		db.Close()
		os.Remove(dbPath)
	}
}

func setupBenchDB(b *testing.B, path string, n int) *sql.DB {
	b.Helper()
	db, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_synchronous=NORMAL")
	if err != nil {
		b.Fatal(err)
	}

	// Blob table (current olu layout)
	_, err = db.Exec(`
		CREATE TABLE blob_entities (
			tenant_id INTEGER NOT NULL DEFAULT 0,
			entity_type TEXT NOT NULL,
			id INTEGER NOT NULL,
			data TEXT NOT NULL,
			_version INTEGER NOT NULL DEFAULT 1,
			PRIMARY KEY (tenant_id, entity_type, id)
		);
		CREATE INDEX idx_blob_type ON blob_entities(entity_type);
	`)
	if err != nil {
		b.Fatal(err)
	}

	// Adapted table (no extra indexes)
	_, err = db.Exec(`
		CREATE TABLE adapted_users (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			age INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			_version INTEGER NOT NULL DEFAULT 1
		);
	`)
	if err != nil {
		b.Fatal(err)
	}

	// Adapted table with indexes on filterable columns
	_, err = db.Exec(`
		CREATE TABLE adapted_users_idx (
			id INTEGER PRIMARY KEY,
			name TEXT NOT NULL,
			email TEXT NOT NULL,
			age INTEGER NOT NULL,
			active INTEGER NOT NULL DEFAULT 1,
			_version INTEGER NOT NULL DEFAULT 1
		);
		CREATE INDEX idx_adapted_age ON adapted_users_idx(age);
		CREATE INDEX idx_adapted_email ON adapted_users_idx(email);
	`)
	if err != nil {
		b.Fatal(err)
	}

	// Seed data
	ctx := context.Background()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}

	rng := rand.New(rand.NewSource(42))
	names := []string{"Alice", "Bob", "Carol", "Dave", "Eve", "Frank", "Grace", "Heidi"}

	stmtBlob, _ := tx.Prepare(`INSERT INTO blob_entities (tenant_id, entity_type, id, data) VALUES (0, 'users', ?, ?)`)
	stmtAdapted, _ := tx.Prepare(`INSERT INTO adapted_users (id, name, email, age, active) VALUES (?, ?, ?, ?, ?)`)
	stmtAdaptedIdx, _ := tx.Prepare(`INSERT INTO adapted_users_idx (id, name, email, age, active) VALUES (?, ?, ?, ?, ?)`)

	for i := 1; i <= n; i++ {
		name := names[rng.Intn(len(names))]
		email := fmt.Sprintf("user%d@example.com", i)
		age := 18 + rng.Intn(62) // 18-79
		active := rng.Intn(2) == 1

		doc := map[string]interface{}{
			"id":     i,
			"name":   name,
			"email":  email,
			"age":    age,
			"active": active,
		}
		blob, _ := json.Marshal(doc)

		stmtBlob.Exec(i, string(blob))
		stmtAdapted.Exec(i, name, email, age, boolToInt(active))
		stmtAdaptedIdx.Exec(i, name, email, age, boolToInt(active))
	}

	stmtBlob.Close()
	stmtAdapted.Close()
	stmtAdaptedIdx.Close()
	tx.Commit()

	// Force WAL checkpoint so both layouts start from the same state
	db.Exec("PRAGMA wal_checkpoint(TRUNCATE)")

	return db
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

// drain reads all rows from a blob query (simulates current olu read path)
func drain(rows *sql.Rows) int {
	count := 0
	for rows.Next() {
		var data string
		rows.Scan(&data)
		var m map[string]interface{}
		json.Unmarshal([]byte(data), &m)
		count++
	}
	rows.Close()
	return count
}

// drainAdapted reads all rows from an adapted-table query
func drainAdapted(rows *sql.Rows) int {
	count := 0
	for rows.Next() {
		var id, age, active int
		var name, email string
		rows.Scan(&id, &name, &email, &age, &active)
		count++
	}
	rows.Close()
	return count
}
