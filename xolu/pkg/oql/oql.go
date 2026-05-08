// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package oql provides SQL-compatible query language for olu.
//
// OQL supports a subset of T-SQL syntax for querying and mutating data:
//
//   - SELECT with aggregates (COUNT, SUM, AVG, MIN, MAX)
//   - GROUP BY, HAVING, ORDER BY, TOP
//   - INSERT with VALUES
//   - UPDATE with WHERE (required)
//   - DELETE with WHERE (required)
//
// JOINs are not supported as relationships are handled by the graph layer.
package oql

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/ha1tch/tsqlparser"
	"github.com/ha1tch/tsqlparser/ast"
	"github.com/ha1tch/xolu/pkg/storage"
)

// SchemaValidator validates entity data against schemas
type SchemaValidator interface {
	Validate(entity string, data map[string]interface{}) (bool, []string)
}

// Engine is the main OQL query engine
type Engine struct {
	store           storage.Store
	validator       *Validator
	executor        *Executor
	schemaValidator SchemaValidator
	mu              sync.RWMutex
}

// SetLimits configures query execution limits for this engine.
func (e *Engine) SetLimits(limits QueryLimits) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executor.SetLimits(limits)
}

// SetProfile updates the hardware profile used by the query planner.
// Call this during server startup after calibration or profile selection.
func (e *Engine) SetProfile(profile *HardwareProfile) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.executor.SetProfile(profile)
}

// NewEngine creates a new OQL engine
func NewEngine(store storage.Store, schemaDir string) *Engine {
	// Check if store supports entity listing
	var validator *Validator
	if checker, ok := store.(EntityChecker); ok {
		validator = NewValidatorWithStore(schemaDir, checker)
	} else {
		validator = NewValidator(schemaDir)
	}
	
	return &Engine{
		store:     store,
		validator: validator,
		executor:  NewExecutor(store, nil),
	}
}

// NewEngineWithSchemaValidator creates an OQL engine with schema validation
func NewEngineWithSchemaValidator(store storage.Store, schemaDir string, sv SchemaValidator) *Engine {
	// Check if store supports entity listing
	var validator *Validator
	if checker, ok := store.(EntityChecker); ok {
		validator = NewValidatorWithStore(schemaDir, checker)
	} else {
		validator = NewValidator(schemaDir)
	}
	
	return &Engine{
		store:           store,
		validator:       validator,
		executor:        NewExecutor(store, sv),
		schemaValidator: sv,
	}
}

// Execute parses, validates, and executes an OQL query
func (e *Engine) Execute(ctx context.Context, sql string) (*Result, error) {
	return e.ExecuteWithTenant(ctx, sql, "")
}

// ExecuteWithTenant parses, validates, and executes an OQL query scoped to a tenant.
// When tenantID is non-empty, all operations are filtered to the specified tenant.
// Deprecated: Use ExecuteWithStore for new code.
func (e *Engine) ExecuteWithTenant(ctx context.Context, sql string, tenantID string) (*Result, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	// 1. Parse
	stmt, err := e.parse(sql)
	if err != nil {
		return nil, err
	}

	// 2. Validate
	if err := e.validator.Validate(stmt); err != nil {
		return nil, err
	}

	// 3. Execute with tenant scoping
	return e.executor.ExecuteWithTenant(ctx, stmt, tenantID)
}

// ExecuteWithStore parses, validates, and executes an OQL query using a specific store.
// The store should already be scoped to the target tenant.
func (e *Engine) ExecuteWithStore(ctx context.Context, sql string, store storage.Store) (*Result, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()

	stmt, err := e.parse(sql)
	if err != nil {
		return nil, err
	}

	if err := e.validator.Validate(stmt); err != nil {
		return nil, err
	}

	return e.executor.ExecuteWithStore(ctx, stmt, store)
}

// parse parses a SQL string into an AST statement
func (e *Engine) parse(sql string) (ast.Statement, error) {
	program, errs := tsqlparser.Parse(sql)
	if len(errs) > 0 {
		return nil, fmt.Errorf("parse error: %v", errs[0])
	}

	if program == nil || len(program.Statements) == 0 {
		return nil, fmt.Errorf("empty query")
	}

	if len(program.Statements) > 1 {
		return nil, fmt.Errorf("only single statements are supported")
	}

	return program.Statements[0], nil
}

// RefreshSchema reloads the entity list from disk
func (e *Engine) RefreshSchema() {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.validator.RefreshEntities()
}

// Job represents an async OQL query job
type Job struct {
	ID        string
	Query     string
	Status    JobStatus
	Result    *Result
	Error     string
	CreatedAt time.Time
	UpdatedAt time.Time
	store     storage.Store // tenant-scoped store captured at submission time (unexported)
}

// JobStatus represents the status of a job
type JobStatus string

const (
	JobPending   JobStatus = "pending"
	JobRunning   JobStatus = "running"
	JobCompleted JobStatus = "completed"
	JobFailed    JobStatus = "failed"
)

// JobManager manages async OQL query jobs
type JobManager struct {
	engine       *Engine
	jobs         map[string]*Job
	mu           sync.RWMutex
	ttl          time.Duration
	queryTimeout time.Duration
	closeCh      chan struct{}
}

// NewJobManager creates a new job manager
func NewJobManager(engine *Engine, ttl time.Duration) *JobManager {
	jm := &JobManager{
		engine:       engine,
		jobs:         make(map[string]*Job),
		ttl:          ttl,
		queryTimeout: 5 * time.Minute, // default; override via SetQueryTimeout
		closeCh:      make(chan struct{}),
	}
	go jm.cleanupLoop()
	return jm
}

// SetQueryTimeout sets the maximum execution time for async queries.
func (jm *JobManager) SetQueryTimeout(d time.Duration) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	jm.queryTimeout = d
}

// Submit submits a query for async execution using the provided store.
// The store is captured at submission time so the background goroutine
// executes against the correct tenant scope.
func (jm *JobManager) Submit(query string, store storage.Store) string {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	id := generateJobID()
	job := &Job{
		ID:        id,
		Query:     query,
		Status:    JobPending,
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
		store:     store,
	}
	jm.jobs[id] = job

	// Execute in background
	go jm.executeJob(job)

	return id
}

// ExecuteSync executes a query synchronously
func (jm *JobManager) ExecuteSync(ctx context.Context, query string) (*Result, error) {
	return jm.engine.Execute(ctx, query)
}

// ExecuteSyncWithTenant executes a query synchronously scoped to a tenant
// Deprecated: Use ExecuteSyncWithStore for new code.
func (jm *JobManager) ExecuteSyncWithTenant(ctx context.Context, query string, tenantID string) (*Result, error) {
	return jm.engine.ExecuteWithTenant(ctx, query, tenantID)
}

// ExecuteSyncWithStore executes a query synchronously using a specific store.
func (jm *JobManager) ExecuteSyncWithStore(ctx context.Context, query string, store storage.Store) (*Result, error) {
	return jm.engine.ExecuteWithStore(ctx, query, store)
}

// GetJob returns a job by ID
func (jm *JobManager) GetJob(id string) *Job {
	jm.mu.RLock()
	defer jm.mu.RUnlock()
	job, exists := jm.jobs[id]
	if !exists {
		return nil
	}
	// Return a copy so callers can safely read fields without holding the lock.
	copy := *job
	return &copy
}

// GetJobResult returns the result of a completed job
func (jm *JobManager) GetJobResult(id string) (*Result, error) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, exists := jm.jobs[id]
	if !exists {
		return nil, fmt.Errorf("job not found: %s", id)
	}

	if job.Status == JobPending || job.Status == JobRunning {
		return nil, fmt.Errorf("job not completed")
	}

	if job.Status == JobFailed {
		return nil, fmt.Errorf("job failed: %s", job.Error)
	}

	return job.Result, nil
}

func (jm *JobManager) executeJob(job *Job) {
	jm.mu.Lock()
	job.Status = JobRunning
	job.UpdatedAt = time.Now()
	jm.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), jm.queryTimeout)
	defer cancel()

	// Use the tenant-scoped store captured at submission time.
	// Falls back to the engine's default store if none was provided
	// (e.g. for non-tenant routes or admin queries).
	var result *Result
	var err error
	if job.store != nil {
		result, err = jm.engine.ExecuteWithStore(ctx, job.Query, job.store)
	} else {
		result, err = jm.engine.Execute(ctx, job.Query)
	}

	jm.mu.Lock()
	defer jm.mu.Unlock()

	if err != nil {
		job.Status = JobFailed
		job.Error = err.Error()
	} else {
		job.Status = JobCompleted
		job.Result = result
	}
	job.UpdatedAt = time.Now()
}

func (jm *JobManager) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			jm.cleanup()
		case <-jm.closeCh:
			return
		}
	}
}

func (jm *JobManager) cleanup() {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	cutoff := time.Now().Add(-jm.ttl)
	for id, job := range jm.jobs {
		if job.UpdatedAt.Before(cutoff) {
			delete(jm.jobs, id)
		}
	}
}

// Close stops the job manager
func (jm *JobManager) Close() {
	close(jm.closeCh)
}

// generateJobID creates a unique job ID
func generateJobID() string {
	return "oql_" + uuid.New().String()
}
