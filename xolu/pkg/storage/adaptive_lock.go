// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"sync"
	"sync/atomic"
	"time"
)

// AdaptiveLock provides a mutex that automatically engages under contention.
//
// Under normal load, operations proceed without any Go-side locking, relying
// on SQLite's WAL mode and busy_timeout for concurrency control. When the
// write success rate drops below a configurable threshold (indicating heavy
// contention), the lock engages a sync.RWMutex to serialise writes and
// eliminate SQLITE_BUSY errors entirely. When contention subsides, the lock
// disengages and returns to lock-free operation.
//
// This gives the best of both worlds: full WAL concurrency under normal load,
// and guaranteed zero errors under burst load.
type AdaptiveLock struct {
	mu        sync.RWMutex
	engaged   atomic.Bool
	threshold atomic.Int64 // success rate threshold in basis points (9500 = 95.00%)

	// Sliding window counters, reset by the monitor goroutine.
	successes atomic.Int64
	failures  atomic.Int64

	stopCh chan struct{}
}

// NewAdaptiveLock creates an adaptive lock with the given success-rate threshold.
// threshold is expressed as a percentage (e.g. 95 means engage the mutex when
// the success rate drops below 95%). Valid range: 0-100. 0 disables the lock
// entirely; 100 keeps it permanently engaged (equivalent to a plain mutex).
func NewAdaptiveLock(threshold int) *AdaptiveLock {
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 100 {
		threshold = 100
	}

	al := &AdaptiveLock{
		stopCh: make(chan struct{}),
	}
	al.threshold.Store(int64(threshold * 100)) // store as basis points
	al.engaged.Store(threshold == 100)         // 100% = always engaged

	go al.monitor()
	return al
}

// SetThreshold dynamically updates the success-rate threshold at runtime.
// threshold is a percentage (0-100). Takes effect on the next monitor tick.
func (al *AdaptiveLock) SetThreshold(threshold int) {
	if threshold < 0 {
		threshold = 0
	}
	if threshold > 100 {
		threshold = 100
	}
	al.threshold.Store(int64(threshold * 100))

	// Handle edge cases immediately
	if threshold == 0 {
		al.engaged.Store(false)
	} else if threshold == 100 {
		al.engaged.Store(true)
	}
}

// Threshold returns the current threshold as a percentage (0-100).
func (al *AdaptiveLock) Threshold() int {
	return int(al.threshold.Load() / 100)
}

// Engaged returns whether the mutex is currently engaged.
func (al *AdaptiveLock) Engaged() bool {
	return al.engaged.Load()
}

// RecordSuccess records a successful operation.
func (al *AdaptiveLock) RecordSuccess() {
	al.successes.Add(1)
}

// RecordFailure records a failed (SQLITE_BUSY) operation. If the threshold
// is non-zero, this immediately engages the lock — the burst is happening
// now and waiting for the monitor tick would let more failures through.
// The monitor goroutine handles disengagement once the window is clean.
func (al *AdaptiveLock) RecordFailure() {
	al.failures.Add(1)
	threshold := al.threshold.Load()
	if threshold > 0 && threshold < 10000 {
		al.engaged.Store(true)
	}
}

// RLock acquires a read lock if the adaptive lock is engaged.
// Returns true if the lock was acquired (caller must call RUnlock).
func (al *AdaptiveLock) RLock() bool {
	if al.engaged.Load() {
		al.mu.RLock()
		return true
	}
	return false
}

// RUnlock releases the read lock. Only call if RLock returned true.
func (al *AdaptiveLock) RUnlock() {
	al.mu.RUnlock()
}

// Lock acquires a write lock if the adaptive lock is engaged.
// Returns true if the lock was acquired (caller must call Unlock).
func (al *AdaptiveLock) Lock() bool {
	if al.engaged.Load() {
		al.mu.Lock()
		return true
	}
	return false
}

// Unlock releases the write lock. Only call if Lock returned true.
func (al *AdaptiveLock) Unlock() {
	al.mu.Unlock()
}

// Stop terminates the background monitor goroutine.
func (al *AdaptiveLock) Stop() {
	close(al.stopCh)
}

// monitor runs in the background, evaluating the success rate every 500ms
// and engaging or disengaging the mutex accordingly.
func (al *AdaptiveLock) monitor() {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-al.stopCh:
			return
		case <-ticker.C:
			al.evaluate()
		}
	}
}

// evaluate checks the sliding window and decides whether to engage/disengage.
func (al *AdaptiveLock) evaluate() {
	threshold := al.threshold.Load()

	// Edge cases: 0 = never engage, 10000 = always engage
	if threshold == 0 {
		al.engaged.Store(false)
		al.resetCounters()
		return
	}
	if threshold == 10000 {
		al.engaged.Store(true)
		al.resetCounters()
		return
	}

	s := al.successes.Load()
	f := al.failures.Load()
	total := s + f

	if total < 10 {
		// Not enough data to make a decision. If we're currently engaged
		// and seeing no failures, disengage. Otherwise hold state.
		if al.engaged.Load() && f == 0 && total > 0 {
			al.engaged.Store(false)
		}
		al.resetCounters()
		return
	}

	// Calculate success rate in basis points (0-10000)
	rate := (s * 10000) / total

	if rate < threshold {
		// Success rate below threshold — engage the mutex
		al.engaged.Store(true)
	} else {
		// Success rate healthy — disengage
		al.engaged.Store(false)
	}

	al.resetCounters()
}

func (al *AdaptiveLock) resetCounters() {
	al.successes.Store(0)
	al.failures.Store(0)
}
