// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"context"
	"testing"
	"time"

	"github.com/ha1tch/xolu/pkg/graph"
)

// ---------------------------------------------------------------------------
// JobManager tests
// ---------------------------------------------------------------------------

func TestJobManager_SubmitAndWait(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	job, err := jm.Submit("MATCH (a:users)-[:FOLLOWS]->(b:users) RETURN a, b", 3)
	if err != nil {
		t.Fatalf("Submit failed: %v", err)
	}

	if job.ID == "" {
		t.Error("Expected non-empty job ID")
	}
	// Do not read job.Status here — the background goroutine may have already
	// written StatusRunning to the shared pointer. Use GetJob for all status reads.

	// Wait for completion
	deadline := time.After(5 * time.Second)
	for {
		retrieved, ok := jm.GetJob(job.ID)
		if !ok {
			t.Fatal("Job not found")
		}
		if retrieved.Status == StatusCompleted {
			if retrieved.Result == nil {
				t.Error("Expected non-nil result")
			}
			break
		}
		if retrieved.Status == StatusFailed {
			t.Fatalf("Job failed: %s", retrieved.Error)
		}
		select {
		case <-deadline:
			t.Fatal("Timed out waiting for job completion")
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func TestJobManager_GetJobResult(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	job, _ := jm.Submit("MATCH (a:users)-[:FOLLOWS]->(b:users) RETURN a, b", 3)

	// Wait for completion
	deadline := time.After(5 * time.Second)
	for {
		j, _ := jm.GetJob(job.ID)
		if j.Status == StatusCompleted || j.Status == StatusFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}

	result, err := jm.GetJobResult(job.ID)
	if err != nil {
		t.Fatalf("GetJobResult failed: %v", err)
	}
	if result == nil {
		t.Error("Expected non-nil result")
	}
}

func TestJobManager_GetJobResult_NotFound(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	_, err := jm.GetJobResult("nonexistent-id")
	if err != ErrJobNotFound {
		t.Errorf("Expected ErrJobNotFound, got: %v", err)
	}
}

func TestJobManager_SubmitInvalidQuery(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	_, err := jm.Submit("THIS IS NOT VALID SULPHER", 3)
	if err == nil {
		t.Error("Expected error for invalid query")
	}
}

func TestJobManager_ExecuteSync(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	result, err := jm.ExecuteSync(context.Background(), "MATCH (a:users)-[:FOLLOWS]->(b:users) RETURN a, b", 3)
	if err != nil {
		t.Fatalf("ExecuteSync failed: %v", err)
	}
	if result == nil {
		t.Fatal("Expected non-nil result")
	}
	if len(result.Data) == 0 {
		t.Error("Expected data in result")
	}
}

func TestJobManager_ExecuteSyncInvalidQuery(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	_, err := jm.ExecuteSync(context.Background(), "INVALID QUERY", 3)
	if err == nil {
		t.Error("Expected error for invalid query")
	}
}

func TestJobManager_ExecuteSyncWithCustomDepth(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 2)
	jm := NewJobManager(executor, time.Minute)

	result, err := jm.ExecuteSync(context.Background(), "MATCH (a:users)-[:FOLLOWS]->(b:users) RETURN a, b", 10)
	if err != nil {
		t.Fatalf("ExecuteSync failed: %v", err)
	}
	if result == nil {
		t.Fatal("Expected result")
	}
}

func TestJobManager_GetJob_NotFound(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, time.Minute)

	_, ok := jm.GetJob("nonexistent")
	if ok {
		t.Error("Expected false for nonexistent job")
	}
}

func TestJobManager_Cleanup(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	jm := NewJobManager(executor, 50*time.Millisecond)

	job, _ := jm.Submit("MATCH (a:users) RETURN a", 3)

	// Wait for completion
	deadline := time.After(5 * time.Second)
	for {
		j, _ := jm.GetJob(job.ID)
		if j.Status == StatusCompleted || j.Status == StatusFailed {
			break
		}
		select {
		case <-deadline:
			t.Fatal("Timed out")
		case <-time.After(10 * time.Millisecond):
		}
	}

	// Wait past TTL, then force cleanup
	time.Sleep(100 * time.Millisecond)
	jm.cleanup()

	_, ok := jm.GetJob(job.ID)
	if ok {
		t.Error("Expected job to be cleaned up after TTL")
	}
}

func TestJobError_Error(t *testing.T) {
	if ErrJobNotFound.Error() != "job not found" {
		t.Errorf("Unexpected error string: %s", ErrJobNotFound.Error())
	}
	if ErrJobNotComplete.Error() != "job not completed" {
		t.Errorf("Unexpected error string: %s", ErrJobNotComplete.Error())
	}
}

// ---------------------------------------------------------------------------
// Executor edge cases — comparison operators, ordering
// ---------------------------------------------------------------------------

func TestExecute_LessThanOperator(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) WHERE u.id < 3 RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Errorf("Expected 2 users with id < 3, got %d", len(result.Data))
	}
}

func TestExecute_GreaterThanOrEqual(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) WHERE u.id >= 4 RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Errorf("Expected 2 users with id >= 4, got %d", len(result.Data))
	}
}

func TestExecute_LessThanOrEqual(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) WHERE u.id <= 2 RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 2 {
		t.Errorf("Expected 2 users with id <= 2, got %d", len(result.Data))
	}
}

func TestExecute_NotEqual(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) WHERE u.id != 3 RETURN u")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 4 {
		t.Errorf("Expected 4 users with id <> 3, got %d", len(result.Data))
	}
}

func TestExecute_OrderByDesc(t *testing.T) {
	g := setupTestGraph()
	executor := NewExecutor(g, 5)
	parser := NewParser()

	query, err := parser.Parse("MATCH (u:users) RETURN u ORDER BY u.id DESC")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 5 {
		t.Fatalf("Expected 5 rows, got %d", len(result.Data))
	}
}

func TestExecute_EmptyGraphQuery(t *testing.T) {
	g := graph.NewFlatGraph()
	executor := NewExecutor(g, 5)
	parser := NewParser()

	query, err := parser.Parse("MATCH (a:nonexistent) RETURN a")
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	result, err := executor.Execute(context.Background(), query)
	if err != nil {
		t.Fatalf("Execute failed: %v", err)
	}

	if len(result.Data) != 0 {
		t.Errorf("Expected 0 rows from empty graph, got %d", len(result.Data))
	}
}
