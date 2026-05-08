// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package tenant

import (
	"context"
	"fmt"
	"testing"
)

// mockPersister implements Persister for testing.
type mockPersister struct {
	mappings map[string]uint16
	saveErr  error // if set, Save returns this error
	loadErr  error // if set, LoadAll returns this error
	saved    []struct{ name string; id uint16 } // records Save calls
}

func newMockPersister() *mockPersister {
	return &mockPersister{mappings: make(map[string]uint16)}
}

func (m *mockPersister) LoadAll(ctx context.Context) (map[string]uint16, error) {
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	// Return a copy so tests can't accidentally mutate the fixture
	out := make(map[string]uint16, len(m.mappings))
	for k, v := range m.mappings {
		out[k] = v
	}
	return out, nil
}

func (m *mockPersister) Save(ctx context.Context, name string, id uint16) error {
	if m.saveErr != nil {
		return m.saveErr
	}
	m.saved = append(m.saved, struct{ name string; id uint16 }{name, id})
	m.mappings[name] = id
	return nil
}

// --- SetPersister ---

func TestSetPersister(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	r.SetPersister(p)

	// Verify it took effect by registering and checking Save was called
	if err := r.Register(context.Background(), "acme", 1); err != nil {
		t.Fatalf("Register after SetPersister: %v", err)
	}
	if len(p.saved) != 1 || p.saved[0].name != "acme" {
		t.Errorf("expected persister.Save to be called with 'acme', got %+v", p.saved)
	}
}

// --- LoadFrom ---

func TestLoadFrom_HappyPath(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	p.mappings["alpha"] = 10
	p.mappings["beta"] = 20
	r.SetPersister(p)

	if err := r.LoadFrom(context.Background()); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	id, ok := r.Lookup("alpha")
	if !ok || id != 10 {
		t.Errorf("Lookup(alpha) = %d, %v; want 10, true", id, ok)
	}
	id, ok = r.Lookup("beta")
	if !ok || id != 20 {
		t.Errorf("Lookup(beta) = %d, %v; want 20, true", id, ok)
	}
}

func TestLoadFrom_NoPersister(t *testing.T) {
	r := NewRegistry()
	// No persister set — LoadFrom should be a no-op
	if err := r.LoadFrom(context.Background()); err != nil {
		t.Fatalf("LoadFrom without persister should succeed, got: %v", err)
	}
}

func TestLoadFrom_PersisterError(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	p.loadErr = fmt.Errorf("disk on fire")
	r.SetPersister(p)

	err := r.LoadFrom(context.Background())
	if err == nil {
		t.Fatal("LoadFrom should propagate persister error")
	}
	if got := err.Error(); got != "load tenant registry: disk on fire" {
		t.Errorf("unexpected error: %s", got)
	}
}

func TestLoadFrom_SkipsReservedIDZero(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	p.mappings["ghost"] = 0 // reserved
	p.mappings["real"] = 5
	r.SetPersister(p)

	if err := r.LoadFrom(context.Background()); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	if _, ok := r.Lookup("ghost"); ok {
		t.Error("tenant with ID 0 should have been skipped")
	}
	if id, ok := r.Lookup("real"); !ok || id != 5 {
		t.Errorf("Lookup(real) = %d, %v; want 5, true", id, ok)
	}
}

func TestLoadFrom_ConflictNameDifferentID(t *testing.T) {
	r := NewRegistry()
	// Pre-register "acme" with ID 1
	r.Register(context.Background(), "acme", 1)

	p := newMockPersister()
	p.mappings["acme"] = 99 // different ID for same name
	r.SetPersister(p)

	err := r.LoadFrom(context.Background())
	if err == nil {
		t.Fatal("LoadFrom should detect name/ID conflict")
	}
	// Verify the error identifies the conflict
	if got := err.Error(); got == "" {
		t.Error("expected non-empty error message")
	}
}

func TestLoadFrom_ConflictIDDifferentName(t *testing.T) {
	r := NewRegistry()
	// Pre-register "acme" with ID 5
	r.Register(context.Background(), "acme", 5)

	p := newMockPersister()
	p.mappings["globex"] = 5 // different name for same ID
	r.SetPersister(p)

	err := r.LoadFrom(context.Background())
	if err == nil {
		t.Fatal("LoadFrom should detect ID/name conflict")
	}
}

func TestLoadFrom_IdempotentSameMapping(t *testing.T) {
	r := NewRegistry()
	r.Register(context.Background(), "acme", 10)

	p := newMockPersister()
	p.mappings["acme"] = 10 // same mapping — should not conflict
	r.SetPersister(p)

	if err := r.LoadFrom(context.Background()); err != nil {
		t.Fatalf("LoadFrom with identical mapping should succeed: %v", err)
	}
}

func TestLoadFrom_AdvancesNextAuto(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	p.mappings["high"] = 500
	r.SetPersister(p)

	if err := r.LoadFrom(context.Background()); err != nil {
		t.Fatalf("LoadFrom: %v", err)
	}

	// Auto-registered tenants should get IDs above 500
	id, err := r.GetOrRegister(context.Background(), "auto")
	if err != nil {
		t.Fatalf("GetOrRegister: %v", err)
	}
	if id <= 500 {
		t.Errorf("auto-assigned ID %d should be > 500", id)
	}
}

// --- Register/GetOrRegister with persistence errors ---

func TestRegister_PersistenceError(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	p.saveErr = fmt.Errorf("write failed")
	r.SetPersister(p)

	err := r.Register(context.Background(), "acme", 1)
	if err == nil {
		t.Fatal("Register should propagate persistence error")
	}

	// Verify the mapping was NOT committed to memory
	if _, ok := r.Lookup("acme"); ok {
		t.Error("failed persistence should not commit to in-memory registry")
	}
}

func TestGetOrRegister_PersistenceError(t *testing.T) {
	r := NewRegistry()
	p := newMockPersister()
	p.saveErr = fmt.Errorf("write failed")
	r.SetPersister(p)

	_, err := r.GetOrRegister(context.Background(), "acme")
	if err == nil {
		t.Fatal("GetOrRegister should propagate persistence error")
	}

	// Verify the mapping was NOT committed to memory
	if _, ok := r.Lookup("acme"); ok {
		t.Error("failed persistence should not commit to in-memory registry")
	}
}
