// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package graph

import (
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
)

// AdaptivePersister handles graph persistence with adaptive timing.
// Under high write load, saves are batched to reduce I/O.
// Under low load, changes persist quickly.
//
// DEPRECATED: The JSON filestore backend is deprecated. The AdaptivePersister
// is only started when that backend is in use (storeHasEdgeTable == false in
// main.go). SQLite deployments — i.e. all production configurations — never
// start it; graph persistence is handled by the store's edge tables in that
// path. When the JSON filestore is removed, this entire type should be deleted
// along with it.
type AdaptivePersister struct {
	graph    Graph
	filename string
	logger   zerolog.Logger

	// Adaptive state
	activeWriters atomic.Int64
	dirty         atomic.Bool
	started       atomic.Bool // true once Start() has been called
	lastSave      time.Time
	mu            sync.Mutex

	// Configuration
	baseInterval time.Duration // minimum interval between saves
	maxInterval  time.Duration // maximum interval between saves

	// Lifecycle
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewAdaptivePersister creates a new adaptive persister
func NewAdaptivePersister(g Graph, filename string, logger zerolog.Logger) *AdaptivePersister {
	return &AdaptivePersister{
		graph:        g,
		filename:     filename,
		logger:       logger,
		baseInterval: 500 * time.Millisecond,
		maxInterval:  30 * time.Second,
		lastSave:     time.Now(),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Start begins the background persistence loop.
// It is safe to call more than once — only the first call starts the loop.
// Calling Stop without a prior Start is safe and returns immediately.
func (p *AdaptivePersister) Start() {
	if !p.started.CompareAndSwap(false, true) {
		return // already running
	}
	go p.loop()
	p.logger.Info().
		Dur("base_interval", p.baseInterval).
		Dur("max_interval", p.maxInterval).
		Msg("Adaptive graph persister started")
}

// Stop gracefully shuts down, ensuring a final save.
// If Start was never called, Stop is a no-op and returns immediately.
func (p *AdaptivePersister) Stop() {
	if !p.started.Load() {
		return
	}
	close(p.stopCh)
	<-p.doneCh
	p.logger.Info().Msg("Adaptive graph persister stopped")
}

// MarkDirty signals that the graph has been modified and needs to be
// persisted. Call this after any mutation (AddEdge, RemoveNode, etc.).
func (p *AdaptivePersister) MarkDirty() {
	p.dirty.Store(true)
}

// WriterEnter increments active writer count.
// Call at the start of a write operation.
func (p *AdaptivePersister) WriterEnter() {
	p.activeWriters.Add(1)
}

// WriterExit decrements active writer count.
// Call at the end of a write operation (defer recommended).
func (p *AdaptivePersister) WriterExit() {
	p.activeWriters.Add(-1)
}

// intervalForWriters returns the adaptive save interval for an already-known
// writer count. Separating the calculation from the atomic Load lets callers
// that have already read activeWriters avoid a second Load, keeping the
// logged (writers, interval) pair internally consistent.
//
// Scaling table (base = 500ms):
//   1 writer   → base * 1.0 = 500ms
//   3 writers  → base * 2.1 = 1.05s
//   10 writers → base * 3.3 = 1.65s
//   50 writers → base * 4.9 = 2.45s
//   100 writers → base * 5.6 = 2.8s
func (p *AdaptivePersister) intervalForWriters(writers int64) time.Duration {
	if writers <= 0 {
		writers = 1
	}
	multiplier := 1.0 + math.Log(float64(writers))
	interval := time.Duration(float64(p.baseInterval) * multiplier)
	if interval > p.maxInterval {
		interval = p.maxInterval
	}
	return interval
}

// currentInterval returns the adaptive save interval for the current writer
// count. Use intervalForWriters when the count is already in hand.
func (p *AdaptivePersister) currentInterval() time.Duration {
	return p.intervalForWriters(p.activeWriters.Load())
}

// Stats returns current persister statistics
func (p *AdaptivePersister) Stats() map[string]interface{} {
	p.mu.Lock()
	lastSave := p.lastSave
	p.mu.Unlock()
	return map[string]interface{}{
		"active_writers":   p.activeWriters.Load(),
		"dirty":            p.dirty.Load(),
		"current_interval": p.currentInterval().String(),
		"last_save":        lastSave.Format(time.RFC3339),
	}
}

func (p *AdaptivePersister) loop() {
	defer close(p.doneCh)

	ticker := time.NewTicker(100 * time.Millisecond) // check frequently
	defer ticker.Stop()

	for {
		select {
		case <-p.stopCh:
			// Final save on shutdown
			p.save("shutdown")
			return

		case <-ticker.C:
			if !p.dirty.Load() {
				continue
			}

			p.mu.Lock()
			elapsed := time.Since(p.lastSave)
			p.mu.Unlock()
			required := p.currentInterval()

			if elapsed >= required {
				p.save("periodic")
			}
		}
	}
}

func (p *AdaptivePersister) save(reason string) {
	p.mu.Lock()

	if !p.dirty.Load() {
		p.mu.Unlock()
		return // already saved by another goroutine
	}

	// Clear dirty BEFORE the save so that any MarkDirty() call that races
	// with graph.Save() is not silently swallowed. If the save fails we
	// restore the flag so the next tick retries.
	p.dirty.Store(false)

	// Release the mutex before calling graph.Save() — the save can take
	// 10s–100s of milliseconds and there is no reason to hold the lock during
	// that I/O. p.mu only protects p.lastSave; re-acquire to update it.
	p.mu.Unlock()

	if err := p.graph.Save(p.filename); err != nil {
		p.dirty.Store(true) // restore so the next tick retries
		p.logger.Error().Err(err).Str("reason", reason).Msg("Failed to save graph")
		return
	}

	p.mu.Lock()
	p.lastSave = time.Now()
	p.mu.Unlock()

	// Single Load so the logged (writers, interval) pair is consistent.
	writers := p.activeWriters.Load()
	p.logger.Debug().
		Str("reason", reason).
		Int64("active_writers", writers).
		Dur("interval", p.intervalForWriters(writers)).
		Msg("Graph saved")

	// If dirty was re-set during the save window (a MarkDirty raced between
	// dirty.Store(false) and graph.Save completing), log it so operators
	// understand why a back-to-back save follows. The actual re-save happens
	// on the next tick through the normal path; no extra work is done here.
	if p.dirty.Load() {
		p.logger.Debug().
			Str("reason", reason).
			Msg("MarkDirty called during save window — next tick will re-save (not a redundant periodic save)")
	}
}
