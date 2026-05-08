// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/storage"
)

// mockStore implements storage.Store for testing
type mockStore struct {
	data map[string][]map[string]interface{}
}

func newMockStore() *mockStore {
	return &mockStore{
		data: make(map[string][]map[string]interface{}),
	}
}

func (m *mockStore) Create(ctx context.Context, entity string, data map[string]interface{}) (int, error) {
	if m.data[entity] == nil {
		m.data[entity] = []map[string]interface{}{}
	}
	id := len(m.data[entity]) + 1
	data["id"] = id
	m.data[entity] = append(m.data[entity], data)
	return id, nil
}

func (m *mockStore) Get(ctx context.Context, entity string, id int) (map[string]interface{}, error) {
	records := m.data[entity]
	for _, r := range records {
		if r["id"] == id {
			return r, nil
		}
	}
	return nil, nil
}

func (m *mockStore) Update(ctx context.Context, entity string, id int, data map[string]interface{}) error {
	records := m.data[entity]
	for i, r := range records {
		if r["id"] == id {
			data["id"] = id
			m.data[entity][i] = data
			return nil
		}
	}
	return nil
}

func (m *mockStore) Patch(ctx context.Context, entity string, id int, data map[string]interface{}) error {
	return m.Update(ctx, entity, id, data)
}

func (m *mockStore) PatchValidated(ctx context.Context, entity string, id int, data map[string]interface{}, validate func(merged map[string]interface{}) error) error {
	return m.Patch(ctx, entity, id, data)
}

func (m *mockStore) Delete(ctx context.Context, entity string, id int) error {
	records := m.data[entity]
	for i, r := range records {
		if r["id"] == id {
			m.data[entity] = append(records[:i], records[i+1:]...)
			return nil
		}
	}
	return nil
}

func (m *mockStore) Save(ctx context.Context, entity string, id int, data map[string]interface{}) (bool, error) {
	return false, m.Update(ctx, entity, id, data)
}

func (m *mockStore) List(ctx context.Context, entity string) ([]map[string]interface{}, error) {
	return m.data[entity], nil
}

func (m *mockStore) Exists(ctx context.Context, entity string, id int) bool {
	r, _ := m.Get(ctx, entity, id)
	return r != nil
}

func (m *mockStore) Close() error {
	return nil
}

func (m *mockStore) Config() storage.StoreConfig {
	return storage.StoreConfig{Type: "mock"}
}

func (m *mockStore) Search(ctx context.Context, entity string, field string, query string, matchType string) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}

func (m *mockStore) FullTextSearch(ctx context.Context, query string, entity string) ([]map[string]interface{}, error) {
	return []map[string]interface{}{}, nil
}

func (m *mockStore) Ping(ctx context.Context) error {
	return nil
}

func (m *mockStore) Commit(ctx context.Context, req storage.CommitRequest) (storage.CommitResult, error) {
	return storage.CommitResult{}, nil
}

// setupTestEngine creates an OQL engine with test data
func setupTestEngine(t *testing.T) (*Engine, *mockStore, string) {
	store := newMockStore()
	ctx := context.Background()

	// Create test data
	store.Create(ctx, "items", map[string]interface{}{
		"category_id": float64(1),
		"status":  "active",
		"value": float64(23.5),
	})
	store.Create(ctx, "items", map[string]interface{}{
		"category_id": float64(1),
		"status":  "active",
		"value": float64(24.1),
	})
	store.Create(ctx, "items", map[string]interface{}{
		"category_id": float64(2),
		"status":  "inactive",
		"value": float64(0),
	})
	store.Create(ctx, "items", map[string]interface{}{
		"category_id": float64(2),
		"status":  "active",
		"value": float64(25.0),
	})
	store.Create(ctx, "items", map[string]interface{}{
		"category_id": float64(3),
		"status":  "active",
		"value": float64(22.0),
	})

	// Create temp schema directory with items entity
	tmpDir, err := os.MkdirTemp("", "oql-test")
	if err != nil {
		t.Fatalf("Failed to create temp dir: %v", err)
	}

	// Create entity directory (validator checks for directories)
	itemsDir := filepath.Join(tmpDir, "items")
	if err := os.MkdirAll(itemsDir, 0755); err != nil {
		t.Fatalf("Failed to create items dir: %v", err)
	}

	engine := NewEngine(store, tmpDir)
	return engine, store, tmpDir
}

func TestEngineSelectAll(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT * FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) != 5 {
		t.Errorf("Expected 5 rows, got %d", len(result.Rows))
	}
	if result.Stats.RowsScanned != 5 {
		t.Errorf("Expected 5 rows scanned, got %d", result.Stats.RowsScanned)
	}
}

func TestEngineSelectWithWhere(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT * FROM items WHERE status = 'active'")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) != 4 {
		t.Errorf("Expected 4 active items, got %d", len(result.Rows))
	}
}

func TestEngineSelectWithNumericWhere(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT * FROM items WHERE value > 23")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// values > 23: 23.5, 24.1, 25.0 = 3
	if len(result.Rows) != 3 {
		t.Errorf("Expected 3 items with value > 23, got %d", len(result.Rows))
	}
}

func TestEngineSelectColumns(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT category_id, status FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) != 5 {
		t.Errorf("Expected 5 rows, got %d", len(result.Rows))
	}

	// Check that only requested columns are returned
	if len(result.Rows) > 0 {
		row := result.Rows[0]
		if _, hasZone := row["category_id"]; !hasZone {
			t.Error("Expected category_id in result")
		}
		if _, hasStatus := row["status"]; !hasStatus {
			t.Error("Expected status in result")
		}
	}
}

func TestEngineSelectTop(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT TOP 2 * FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) != 2 {
		t.Errorf("Expected 2 rows with TOP 2, got %d", len(result.Rows))
	}
}

func TestEngineSelectDistinct(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT DISTINCT status FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Should have 2 distinct statuses: active, inactive
	if len(result.Rows) != 2 {
		t.Errorf("Expected 2 distinct statuses, got %d", len(result.Rows))
	}
}

func TestEngineSelectOrderBy(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT * FROM items ORDER BY value DESC")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) < 2 {
		t.Skip("Not enough rows to verify order")
	}

	// First should have highest value (25.0)
	first := result.Rows[0]["value"].(float64)
	second := result.Rows[1]["value"].(float64)
	if first < second {
		t.Errorf("Expected descending order, got %v before %v", first, second)
	}
}

func TestEngineInsert(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx,
		"INSERT INTO items (category_id, status, value) VALUES (4, 'active', 26.5)")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Type != ResultInsert {
		t.Errorf("Expected INSERT result type")
	}
	if result.Stats.RowsAffected != 1 {
		t.Errorf("Expected 1 row affected, got %d", result.Stats.RowsAffected)
	}

	// Verify data was inserted
	records, _ := store.List(ctx, "items")
	if len(records) != 6 {
		t.Errorf("Expected 6 items after insert, got %d", len(records))
	}
}

func TestEngineInsertMultiple(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx,
		"INSERT INTO items (category_id, status) VALUES (5, 'active'), (6, 'inactive')")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Stats.RowsAffected != 2 {
		t.Errorf("Expected 2 rows affected, got %d", result.Stats.RowsAffected)
	}

	records, _ := store.List(ctx, "items")
	if len(records) != 7 {
		t.Errorf("Expected 7 items after insert, got %d", len(records))
	}
}

func TestEngineUpdateWithWhere(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx,
		"UPDATE items SET status = 'maintenance' WHERE category_id = 2")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Type != ResultUpdate {
		t.Errorf("Expected UPDATE result type")
	}
	if result.Stats.RowsAffected != 2 {
		t.Errorf("Expected 2 rows affected (category 2 has 2 items), got %d", result.Stats.RowsAffected)
	}

	// Verify updates
	records, _ := store.List(ctx, "items")
	maintenanceCount := 0
	for _, r := range records {
		if r["status"] == "maintenance" {
			maintenanceCount++
		}
	}
	if maintenanceCount != 2 {
		t.Errorf("Expected 2 maintenance items, got %d", maintenanceCount)
	}
}

func TestEngineDeleteWithWhere(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx,
		"DELETE FROM items WHERE status = 'inactive'")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if result.Type != ResultDelete {
		t.Errorf("Expected DELETE result type")
	}
	if result.Stats.RowsAffected != 1 {
		t.Errorf("Expected 1 row affected, got %d", result.Stats.RowsAffected)
	}

	// Verify deletion
	records, _ := store.List(ctx, "items")
	if len(records) != 4 {
		t.Errorf("Expected 4 items after delete, got %d", len(records))
	}
}

func TestEngineRejectsUpdateWithoutWhere(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	_, err := engine.Execute(ctx, "UPDATE items SET status = 'inactive'")
	if err == nil {
		t.Error("Expected error for UPDATE without WHERE")
	}
}

func TestEngineRejectsDeleteWithoutWhere(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	_, err := engine.Execute(ctx, "DELETE FROM items")
	if err == nil {
		t.Error("Expected error for DELETE without WHERE")
	}
}

func TestEngineRejectsNonExistentEntity(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	_, err := engine.Execute(ctx, "SELECT * FROM nonexistent")
	if err == nil {
		t.Error("Expected error for non-existent entity")
	}
}

func TestEngineParseError(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	_, err := engine.Execute(ctx, "SELEKT * FORM items")
	if err == nil {
		t.Error("Expected parse error for invalid SQL")
	}
}

func TestJobManager(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	jm := NewJobManager(engine, 60)
	defer jm.Close()

	// Submit async query (nil store = use engine default, appropriate for non-tenant test)
	queryID := jm.Submit("SELECT * FROM items", nil)
	if queryID == "" {
		t.Fatal("Submit returned empty query ID")
	}

	// Get job
	job := jm.GetJob(queryID)
	if job == nil {
		t.Fatal("GetJob returned nil")
	}

	// Wait for completion with proper sleep
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		job = jm.GetJob(queryID)
		if job.Status == JobCompleted || job.Status == JobFailed {
			break
		}
	}

	if job.Status != JobCompleted {
		t.Errorf("Expected completed status, got %s (error: %s)", job.Status, job.Error)
	}

	// Get result
	result, err := jm.GetJobResult(queryID)
	if err != nil {
		t.Fatalf("GetJobResult failed: %v", err)
	}

	if len(result.Rows) != 5 {
		t.Errorf("Expected 5 rows, got %d", len(result.Rows))
	}
}

func TestJobManagerSync(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	jm := NewJobManager(engine, 60)
	defer jm.Close()

	ctx := context.Background()
	result, err := jm.ExecuteSync(ctx, "SELECT * FROM items WHERE category_id = 1")
	if err != nil {
		t.Fatalf("ExecuteSync failed: %v", err)
	}

	if len(result.Rows) != 2 {
		t.Errorf("Expected 2 items in category 1, got %d", len(result.Rows))
	}
}

// =============================================================================
// GROUP BY Integration Tests
// =============================================================================

func TestEngineSelectGroupBy(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT category_id, COUNT(*) AS cnt FROM items GROUP BY category_id")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// 3 categorys: category 1 (2 items), category 2 (2 items), category 3 (1 item)
	if len(result.Rows) != 3 {
		t.Errorf("Expected 3 groups (categorys), got %d", len(result.Rows))
	}

	// Verify counts
	for _, row := range result.Rows {
		categoryID := row["category_id"]
		cnt := row["COUNT(*)"]
		if cnt == nil {
			cnt = row["cnt"]
		}

		switch categoryID {
		case float64(1):
			if cnt != 2 && cnt != float64(2) {
				t.Errorf("Zone 1 should have 2 items, got %v", cnt)
			}
		case float64(2):
			if cnt != 2 && cnt != float64(2) {
				t.Errorf("Zone 2 should have 2 items, got %v", cnt)
			}
		case float64(3):
			if cnt != 1 && cnt != float64(1) {
				t.Errorf("Zone 3 should have 1 item, got %v", cnt)
			}
		}
	}
}

func TestEngineSelectGroupByWithAggregate(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT category_id, AVG(value) AS avg_value FROM items GROUP BY category_id")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) != 3 {
		t.Errorf("Expected 3 groups, got %d", len(result.Rows))
	}

	// Zone 1: (23.5 + 24.1) / 2 = 23.8
	// Zone 2: (0 + 25.0) / 2 = 12.5
	// Zone 3: 22.0 / 1 = 22.0
	for _, row := range result.Rows {
		categoryID := row["category_id"]
		avg := row["AVG(value)"]
		if avg == nil {
			avg = row["avg_value"]
		}

		avgFloat, ok := avg.(float64)
		if !ok {
			continue
		}

		switch categoryID {
		case float64(1):
			if avgFloat < 23.7 || avgFloat > 23.9 {
				t.Errorf("Zone 1 avg should be ~23.8, got %v", avgFloat)
			}
		case float64(2):
			if avgFloat < 12.4 || avgFloat > 12.6 {
				t.Errorf("Zone 2 avg should be ~12.5, got %v", avgFloat)
			}
		case float64(3):
			if avgFloat < 21.9 || avgFloat > 22.1 {
				t.Errorf("Zone 3 avg should be ~22.0, got %v", avgFloat)
			}
		}
	}
}

func TestEngineSelectGroupByWithHaving(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	// Only categorys with more than 1 item
	result, err := engine.Execute(ctx, "SELECT category_id, COUNT(*) AS cnt FROM items GROUP BY category_id HAVING COUNT(*) > 1")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Only categorys 1 and 2 have > 1 item
	if len(result.Rows) != 2 {
		t.Errorf("Expected 2 groups with HAVING COUNT(*) > 1, got %d", len(result.Rows))
	}
}

func TestEngineSelectAggregateWithoutGroupBy(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT COUNT(*) AS total, SUM(value) AS sum_value, AVG(value) AS avg_value FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	// Should return single row with aggregates over all data
	if len(result.Rows) != 1 {
		t.Errorf("Expected 1 row for aggregate without GROUP BY, got %d", len(result.Rows))
	}

	if len(result.Rows) > 0 {
		row := result.Rows[0]

		// Total count should be 5
		total := row["COUNT(*)"]
		if total == nil {
			total = row["total"]
		}
		if total != 5 && total != float64(5) {
			t.Errorf("Expected total count 5, got %v", total)
		}

		// Sum: 23.5 + 24.1 + 0 + 25.0 + 22.0 = 94.6
		sum := row["SUM(value)"]
		if sum == nil {
			sum = row["sum_value"]
		}
		if sumFloat, ok := sum.(float64); ok {
			if sumFloat < 94.5 || sumFloat > 94.7 {
				t.Errorf("Expected sum ~94.6, got %v", sumFloat)
			}
		}

		// Avg: 94.6 / 5 = 18.92
		avg := row["AVG(value)"]
		if avg == nil {
			avg = row["avg_value"]
		}
		if avgFloat, ok := avg.(float64); ok {
			if avgFloat < 18.9 || avgFloat > 19.0 {
				t.Errorf("Expected avg ~18.92, got %v", avgFloat)
			}
		}
	}
}

func TestEngineSelectMinMax(t *testing.T) {
	engine, _, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	result, err := engine.Execute(ctx, "SELECT MIN(value) AS min_val, MAX(value) AS max_val FROM items")
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Rows) != 1 {
		t.Errorf("Expected 1 row, got %d", len(result.Rows))
	}

	if len(result.Rows) > 0 {
		row := result.Rows[0]

		// Min should be 0 (inactive item)
		minVal := row["MIN(value)"]
		if minVal == nil {
			minVal = row["min_val"]
		}
		if minFloat, ok := minVal.(float64); ok {
			if minFloat != 0 {
				t.Errorf("Expected min 0, got %v", minFloat)
			}
		}

		// Max should be 25.0
		maxVal := row["MAX(value)"]
		if maxVal == nil {
			maxVal = row["max_val"]
		}
		if maxFloat, ok := maxVal.(float64); ok {
			if maxFloat != 25.0 {
				t.Errorf("Expected max 25.0, got %v", maxFloat)
			}
		}
	}
}

func TestEngineInsertWithREF(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	// Register the post entity so the validator accepts it
	if err := os.MkdirAll(filepath.Join(tmpDir, "post"), 0755); err != nil {
		t.Fatalf("failed to create post entity dir: %v", err)
	}
	engine.RefreshSchema()

	ctx := context.Background()

	// Single REF
	result, err := engine.Execute(ctx,
		`INSERT INTO post (title, author_ref) VALUES ('Getting started with olu', @REF('author', 1))`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}
	if result.Type != ResultInsert {
		t.Errorf("expected INSERT result type, got %v", result.Type)
	}
	if result.Stats.RowsAffected != 1 {
		t.Errorf("expected 1 row affected, got %d", result.Stats.RowsAffected)
	}

	posts, _ := store.List(ctx, "post")
	if len(posts) != 1 {
		t.Fatalf("expected 1 post, got %d", len(posts))
	}
	post := posts[0]

	if post["title"] != "Getting started with olu" {
		t.Errorf("unexpected title: %v", post["title"])
	}

	ref, ok := post["author_ref"].(map[string]interface{})
	if !ok {
		t.Fatalf("author_ref should be a map, got %T: %v", post["author_ref"], post["author_ref"])
	}
	if ref["type"] != "REF" {
		t.Errorf("expected ref.type=REF, got %v", ref["type"])
	}
	if ref["entity"] != "author" {
		t.Errorf("expected ref.entity=author, got %v", ref["entity"])
	}
	if ref["id"] != int64(1) {
		t.Errorf("expected ref.id=1, got %v (type %T)", ref["id"], ref["id"])
	}
}

func TestEngineInsertWithREFMultipleRows(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	if err := os.MkdirAll(filepath.Join(tmpDir, "post"), 0755); err != nil {
		t.Fatalf("failed to create post entity dir: %v", err)
	}
	engine.RefreshSchema()

	ctx := context.Background()

	_, err := engine.Execute(ctx, `
		INSERT INTO post (title, author_ref)
		VALUES ('Post one', @REF('author', 1)),
		       ('Post two', @REF('author', 2))`)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	posts, _ := store.List(ctx, "post")
	if len(posts) != 2 {
		t.Fatalf("expected 2 posts, got %d", len(posts))
	}

	for i, post := range posts {
		ref, ok := post["author_ref"].(map[string]interface{})
		if !ok {
			t.Fatalf("post[%d] author_ref should be a map, got %T", i, post["author_ref"])
		}
		if ref["type"] != "REF" {
			t.Errorf("post[%d] ref.type: expected REF, got %v", i, ref["type"])
		}
		wantID := int64(i + 1)
		if ref["id"] != wantID {
			t.Errorf("post[%d] ref.id: expected %d, got %v (type %T)", i, wantID, ref["id"], ref["id"])
		}
	}
}

func TestEngineInsertREFCaseInsensitive(t *testing.T) {
	engine, store, tmpDir := setupTestEngine(t)
	defer os.RemoveAll(tmpDir)

	if err := os.MkdirAll(filepath.Join(tmpDir, "post"), 0755); err != nil {
		t.Fatalf("failed to create post entity dir: %v", err)
	}
	engine.RefreshSchema()

	ctx := context.Background()

	// Both @REF and @ref should work
	_, err := engine.Execute(ctx,
		`INSERT INTO post (title, author_ref) VALUES ('Lower ref', @ref('author', 3))`)
	if err != nil {
		t.Fatalf("@ref (lowercase) failed: %v", err)
	}

	posts, _ := store.List(ctx, "post")
	ref, ok := posts[0]["author_ref"].(map[string]interface{})
	if !ok {
		t.Fatalf("author_ref should be a map, got %T", posts[0]["author_ref"])
	}
	if ref["type"] != "REF" {
		t.Errorf("expected ref.type=REF, got %v", ref["type"])
	}
}
