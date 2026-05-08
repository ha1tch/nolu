// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package timeseries

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ha1tch/xolu/pkg/tenant"
)

// DefaultManager manages per-tenant Store lifecycle.
// It is backend-agnostic — the StoreFactory controls which engine is used.
type DefaultManager struct {
	baseDir string
	factory StoreFactory
	config  StoreConfig
	stores  sync.Map   // tenantID (uint16) -> Store
	known   sync.Map   // tenantID (uint16) -> struct{}
	mu      sync.Mutex // serialises Provision and lazy-open
}

// NewManager creates a timeseries manager. It scans baseDir for existing
// tenant directories and registers them as provisioned (lazy-open on first
// request).
func NewManager(baseDir string, factory StoreFactory, cfg StoreConfig) (*DefaultManager, error) {
	if err := os.MkdirAll(baseDir, 0755); err != nil {
		return nil, fmt.Errorf("ts manager: mkdir %s: %w", baseDir, err)
	}

	m := &DefaultManager{
		baseDir: baseDir,
		factory: factory,
		config:  cfg,
	}

	entries, err := os.ReadDir(baseDir)
	if err != nil {
		return nil, fmt.Errorf("ts manager: scan %s: %w", baseDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if id, ok := parseTenantDirName(e.Name()); ok {
			m.known.Store(id, struct{}{})
		}
	}
	return m, nil
}

// Provision creates a timeseries store for a tenant. Idempotent.
func (m *DefaultManager) Provision(ctx context.Context, tenantID uint16) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if _, loaded := m.stores.Load(tenantID); loaded {
		return nil
	}
	dir := m.tenantDir(tenantID)
	store, err := m.factory(dir, m.config)
	if err != nil {
		return fmt.Errorf("ts provision tenant %d: %w", tenantID, err)
	}
	m.stores.Store(tenantID, store)
	m.known.Store(tenantID, struct{}{})
	return nil
}

// StoreFor returns the Store for a tenant, opening it lazily if needed.
func (m *DefaultManager) StoreFor(tenantID uint16) (Store, error) {
	if v, ok := m.stores.Load(tenantID); ok {
		return v.(Store), nil
	}
	if _, ok := m.known.Load(tenantID); !ok {
		return nil, fmt.Errorf("tenant %d not provisioned for timeseries (OLU-TS003)", tenantID)
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if v, ok := m.stores.Load(tenantID); ok {
		return v.(Store), nil
	}
	dir := m.tenantDir(tenantID)
	store, err := m.factory(dir, m.config)
	if err != nil {
		return nil, fmt.Errorf("ts lazy open tenant %d: %w", tenantID, err)
	}
	m.stores.Store(tenantID, store)
	return store, nil
}

// IsProvisioned reports whether a tenant has timeseries storage.
func (m *DefaultManager) IsProvisioned(tenantID uint16) bool {
	_, ok := m.known.Load(tenantID)
	return ok
}

// Close shuts down all open stores.
func (m *DefaultManager) Close() error {
	var firstErr error
	m.stores.Range(func(key, value any) bool {
		if err := value.(Store).Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		m.stores.Delete(key)
		return true
	})
	return firstErr
}

// --- Internal ---

func (m *DefaultManager) tenantDir(tenantID uint16) string {
	return filepath.Join(m.baseDir, tenant.StorageDirSegment(tenantID))
}

func parseTenantDirName(name string) (uint16, bool) {
	if !strings.HasPrefix(name, "t") || len(name) < 2 {
		return 0, false
	}
	var id uint16
	n, err := fmt.Sscanf(name, "t%x", &id)
	if err != nil || n != 1 {
		return 0, false
	}
	return id, true
}
