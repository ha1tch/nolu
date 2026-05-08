// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const registryFile = "registry.json"

// registryEntry is the on-disk representation of a timeline's config.
type registryEntry struct {
	ID            uint16    `json:"id"`
	Name          string    `json:"name,omitempty"`
	Dims          uint8     `json:"dims"`
	RetentionDays int       `json:"retention_days"`
	CreatedAt     time.Time `json:"created_at"`
	FirstWriteAt  time.Time `json:"first_write_at,omitempty"`
}

// registryFile on-disk structure.
type registryDisk struct {
	DefaultRetentionDays int              `json:"default_retention_days"`
	Timelines            []registryEntry  `json:"timelines"`
}

// registry holds the in-memory timeline registry for a store.
type registry struct {
	mu                   sync.RWMutex
	dir                  string
	defaultRetentionDays int
	timelines            map[TimelineID]*TimelineConfig
}

// loadRegistry reads registry.json from dir, creating it if absent.
// The returned bool is true if the file already existed (i.e. this is a
// reopen, not a first-open). Callers use this to decide whether to apply
// config defaults or respect persisted values.
func loadRegistry(dir string) (*registry, bool, error) {
	r := &registry{
		dir:       dir,
		timelines: make(map[TimelineID]*TimelineConfig),
	}
	path := filepath.Join(dir, registryFile)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("timeseries: registry: read %s: %w", path, err)
	}
	var disk registryDisk
	if err := json.Unmarshal(data, &disk); err != nil {
		return nil, false, fmt.Errorf("timeseries: registry: parse %s: %w", path, err)
	}
	r.defaultRetentionDays = disk.DefaultRetentionDays
	for _, e := range disk.Timelines {
		cfg := &TimelineConfig{
			Name:          e.Name,
			Dims:          e.Dims,
			RetentionDays: e.RetentionDays,
			CreatedAt:     e.CreatedAt,
			FirstWriteAt:  e.FirstWriteAt,
		}
		r.timelines[TimelineID(e.ID)] = cfg
	}
	return r, true, nil
}

// save flushes the registry to disk. Caller must hold r.mu (at least read lock).
func (r *registry) save() error {
	disk := registryDisk{
		DefaultRetentionDays: r.defaultRetentionDays,
	}
	for id, cfg := range r.timelines {
		disk.Timelines = append(disk.Timelines, registryEntry{
			ID:            uint16(id),
			Name:          cfg.Name,
			Dims:          cfg.Dims,
			RetentionDays: cfg.RetentionDays,
			CreatedAt:     cfg.CreatedAt,
			FirstWriteAt:  cfg.FirstWriteAt,
		})
	}
	data, err := json.MarshalIndent(disk, "", "  ")
	if err != nil {
		return fmt.Errorf("timeseries: registry: marshal: %w", err)
	}
	path := filepath.Join(r.dir, registryFile)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("timeseries: registry: write tmp: %w", err)
	}
	return os.Rename(tmp, path)
}

// define registers a new timeline. Returns an error if the ID is reserved
// or if the timeline already exists with a different Dims value after its
// first write.
func (r *registry) define(id TimelineID, cfg TimelineConfig) error {
	if id == 0 {
		return fmt.Errorf("timeseries: timeline ID 0x0000 is reserved (OLU-TS018)")
	}
	if cfg.Dims < MinDims || cfg.Dims > MaxDims {
		return fmt.Errorf("timeseries: dims must be %d–%d, got %d", MinDims, MaxDims, cfg.Dims)
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if existing, ok := r.timelines[id]; ok {
		// Idempotent if dims match or first write not yet set.
		if !existing.FirstWriteAt.IsZero() && existing.Dims != cfg.Dims {
			return fmt.Errorf("timeseries: timeline %d: dims are immutable after first write (OLU-TS016)", id)
		}
		// Allow re-definition to update name/retention before first write.
		existing.Name = cfg.Name
		existing.RetentionDays = cfg.RetentionDays
		return r.save()
	}

	c := cfg
	if c.CreatedAt.IsZero() {
		c.CreatedAt = time.Now().UTC()
	}
	r.timelines[id] = &c
	return r.save()
}

// update changes the mutable fields (Name, RetentionDays) of an existing timeline.
func (r *registry) update(id TimelineID, cfg TimelineConfig) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	existing, ok := r.timelines[id]
	if !ok {
		return fmt.Errorf("timeseries: timeline %d not defined (OLU-TS004)", id)
	}
	existing.Name = cfg.Name
	existing.RetentionDays = cfg.RetentionDays
	return r.save()
}

// get returns a copy of the TimelineConfig for id.
func (r *registry) get(id TimelineID) (TimelineConfig, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.timelines[id]
	if !ok {
		return TimelineConfig{}, false
	}
	return *cfg, true
}

// list returns all defined timeline IDs.
func (r *registry) list() []TimelineID {
	r.mu.RLock()
	defer r.mu.RUnlock()
	ids := make([]TimelineID, 0, len(r.timelines))
	for id := range r.timelines {
		ids = append(ids, id)
	}
	return ids
}

// recordFirstWrite marks the timeline's FirstWriteAt if not already set,
// locking in its Dims value. Called on first successful Append to a timeline.
// Fast path: takes only a read lock when FirstWriteAt is already set, which
// is the common case after the first event.
func (r *registry) recordFirstWrite(id TimelineID) error {
	// Fast path: read lock only — avoids write-lock contention on every Append
	// after the timeline is initialised.
	r.mu.RLock()
	cfg, ok := r.timelines[id]
	if ok && !cfg.FirstWriteAt.IsZero() {
		r.mu.RUnlock()
		return nil
	}
	r.mu.RUnlock()

	// Slow path: first write on this timeline. Take write lock and re-check.
	r.mu.Lock()
	defer r.mu.Unlock()
	cfg, ok = r.timelines[id]
	if !ok {
		return fmt.Errorf("timeseries: timeline %d not defined (OLU-TS004)", id)
	}
	if cfg.FirstWriteAt.IsZero() {
		cfg.FirstWriteAt = time.Now().UTC()
		return r.save()
	}
	return nil
}

// effectiveRetention returns the retention days for a timeline, falling
// back to the store-level default if the timeline's value is 0.
func (r *registry) effectiveRetention(id TimelineID) int {
	r.mu.RLock()
	defer r.mu.RUnlock()
	cfg, ok := r.timelines[id]
	if !ok || cfg.RetentionDays == 0 {
		return r.defaultRetentionDays
	}
	return cfg.RetentionDays
}

// setDefaultRetention updates the store-level default and persists it.
func (r *registry) setDefaultRetention(days int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.defaultRetentionDays = days
	return r.save()
}
