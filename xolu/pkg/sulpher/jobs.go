// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package sulpher

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// JobStatus represents the status of a query job
type JobStatus string

const (
	StatusPending   JobStatus = "pending"
	StatusRunning   JobStatus = "running"
	StatusCompleted JobStatus = "completed"
	StatusFailed    JobStatus = "failed"
)

// Job represents an async query job
type Job struct {
	ID        string                   `json:"id"`
	Query     string                   `json:"query"`
	Status    JobStatus                `json:"status"`
	Result    *QueryResult             `json:"result,omitempty"`
	Error     string                   `json:"error,omitempty"`
	CreatedAt time.Time                `json:"created_at"`
	StartedAt *time.Time               `json:"started_at,omitempty"`
	EndedAt   *time.Time               `json:"ended_at,omitempty"`
	MaxDepth  int                      `json:"max_depth"`
}

// JobManager manages async query jobs
type JobManager struct {
	jobs         map[string]*Job
	executor     *Executor
	parser       *Parser
	mu           sync.RWMutex
	ttl          time.Duration
	queryTimeout time.Duration
}

// NewJobManager creates a new job manager
func NewJobManager(executor *Executor, ttl time.Duration) *JobManager {
	jm := &JobManager{
		jobs:         make(map[string]*Job),
		executor:     executor,
		parser:       NewParser(),
		ttl:          ttl,
		queryTimeout: 5 * time.Minute, // default; override via SetQueryTimeout
	}

	// Start cleanup goroutine
	go jm.cleanupLoop()

	return jm
}

// SetQueryTimeout sets the maximum execution time for async graph queries.
func (jm *JobManager) SetQueryTimeout(d time.Duration) {
	jm.mu.Lock()
	defer jm.mu.Unlock()
	jm.queryTimeout = d
}

// SetLimits configures graph query execution limits on the underlying executor.
func (jm *JobManager) SetLimits(limits GraphLimits) {
	jm.executor.SetLimits(limits)
}

// Submit submits a new query job and returns immediately
func (jm *JobManager) Submit(queryStr string, maxDepth int) (*Job, error) {
	// Validate query syntax first
	_, err := jm.parser.Parse(queryStr)
	if err != nil {
		return nil, err
	}

	job := &Job{
		ID:        uuid.New().String(),
		Query:     queryStr,
		Status:    StatusPending,
		CreatedAt: time.Now(),
		MaxDepth:  maxDepth,
	}

	jm.mu.Lock()
	jm.jobs[job.ID] = job
	jm.mu.Unlock()

	// Execute in background
	go jm.executeJob(job)

	return job, nil
}

// ExecuteSync executes a query synchronously
func (jm *JobManager) ExecuteSync(ctx context.Context, queryStr string, maxDepth int) (*QueryResult, error) {
	query, err := jm.parser.Parse(queryStr)
	if err != nil {
		return nil, err
	}
	// ExecuteWithDepth is concurrent-safe; it does not mutate shared executor state.
	return jm.executor.ExecuteWithDepth(ctx, query, maxDepth)
}

// GetJob retrieves a job by ID
func (jm *JobManager) GetJob(id string) (Job, bool) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, exists := jm.jobs[id]
	if !exists {
		return Job{}, false
	}
	// Return a copy so callers can safely read fields without holding the lock.
	return *job, true
}

// GetJobResult retrieves the result of a completed job
func (jm *JobManager) GetJobResult(id string) (*QueryResult, error) {
	jm.mu.RLock()
	defer jm.mu.RUnlock()

	job, exists := jm.jobs[id]
	if !exists {
		return nil, ErrJobNotFound
	}

	if job.Status != StatusCompleted {
		return nil, ErrJobNotComplete
	}

	return job.Result, nil
}

// executeJob runs the query in the background
func (jm *JobManager) executeJob(job *Job) {
	jm.mu.Lock()
	job.Status = StatusRunning
	now := time.Now()
	job.StartedAt = &now
	jm.mu.Unlock()

	query, err := jm.parser.Parse(job.Query)
	if err != nil {
		jm.failJob(job, err.Error())
		return
	}

	// Set max depth for this execution
	ctx, cancel := context.WithTimeout(context.Background(), jm.queryTimeout)
	defer cancel()

	// ExecuteWithDepth is concurrent-safe; it does not mutate shared executor state.
	result, err := jm.executor.ExecuteWithDepth(ctx, query, job.MaxDepth)

	if err != nil {
		jm.failJob(job, err.Error())
		return
	}

	jm.mu.Lock()
	job.Status = StatusCompleted
	job.Result = result
	endTime := time.Now()
	job.EndedAt = &endTime
	jm.mu.Unlock()
}

// failJob marks a job as failed
func (jm *JobManager) failJob(job *Job, errMsg string) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	job.Status = StatusFailed
	job.Error = errMsg
	endTime := time.Now()
	job.EndedAt = &endTime
}

// cleanupLoop periodically removes old completed jobs
func (jm *JobManager) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		jm.cleanup()
	}
}

// cleanup removes jobs older than TTL
func (jm *JobManager) cleanup() {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	cutoff := time.Now().Add(-jm.ttl)
	for id, job := range jm.jobs {
		if job.Status == StatusCompleted || job.Status == StatusFailed {
			if job.EndedAt != nil && job.EndedAt.Before(cutoff) {
				delete(jm.jobs, id)
			}
		}
	}
}

// Errors
type jobError string

func (e jobError) Error() string { return string(e) }

const (
	ErrJobNotFound   = jobError("job not found")
	ErrJobNotComplete = jobError("job not completed")
)
