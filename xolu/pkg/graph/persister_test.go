// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package graph

import (
	"errors"
	"math"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/rs/zerolog"
)

// ---------------------------------------------------------------------------
// Mock Graph for persister tests
// ---------------------------------------------------------------------------

// mockGraph records Save calls and can be configured to fail.
type mockGraph struct {
	mu        sync.Mutex
	saveCalls []string // filenames passed to each Save call
	saveErr   error    // if non-nil, Save returns this error
}

func (m *mockGraph) Save(filename string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.saveCalls = append(m.saveCalls, filename)
	return m.saveErr
}

func (m *mockGraph) saveCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.saveCalls)
}

// Stub implementations of the remaining Graph interface methods.
// Mutation
func (m *mockGraph) AddNode(string, string) error                                               { return nil }
func (m *mockGraph) RemoveNode(string) error                                                    { return nil }
func (m *mockGraph) AddEdge(string, string, string) error                                       { return nil }
func (m *mockGraph) CheckEdge(string, string, string) error                                     { return nil }
func (m *mockGraph) RemoveEdge(string, string) error                                            { return nil }
func (m *mockGraph) UpdateFromEntityForTenant(uint16, string, int, map[string]interface{}) error { return nil }
func (m *mockGraph) UpdateFromEntity(string, int, map[string]interface{}) error                  { return nil }
// Traversal
func (m *mockGraph) GetNeighbors(string) (map[string]string, error)                             { return nil, nil }
func (m *mockGraph) GetIncomingEdges(string) (map[string]string, error)                         { return nil, nil }
func (m *mockGraph) FindPath(string, string, int) ([]string, error)                             { return nil, nil }
func (m *mockGraph) PathExists(string, string, int) (bool, int, error)                          { return false, 0, nil }
func (m *mockGraph) SharedOutNeighbors(string, string) ([]string, error)                        { return nil, nil }
func (m *mockGraph) HasCycleForTenant(string) (bool, error)                                     { return false, nil }
func (m *mockGraph) VodeCount() int                                                              { return 0 }
func (m *mockGraph) VodeCountForTenant(string) (int, error)                                     { return 0, nil }
// Node queries
func (m *mockGraph) NodeExists(string) bool                                                      { return false }
func (m *mockGraph) GetNodeInfo(string) (*NodeInfo, error)                                       { return nil, nil }
func (m *mockGraph) GetNodesByType(string) []string                                              { return nil }
func (m *mockGraph) GetAllNodes() []string                                                       { return nil }
func (m *mockGraph) GetDegree(string) (Degree, error)                                            { return Degree{}, nil }
// Metrics
func (m *mockGraph) HasCycle() bool                                                              { return false }
func (m *mockGraph) NodeCount() int                                                              { return 0 }
func (m *mockGraph) EdgeCount() int                                                              { return 0 }
// Tenant-scoped queries
func (m *mockGraph) NodeCountForTenant(string) (int, error)                                     { return 0, nil }
func (m *mockGraph) EdgeCountForTenant(string) (int, error)                                     { return 0, nil }
func (m *mockGraph) GetAllNodesForTenant(string) ([]string, error)                              { return nil, nil }
func (m *mockGraph) GetNodesByTypeForTenant(string, string) ([]string, error)                   { return nil, nil }
// Persistence
func (m *mockGraph) Load(string) error                                                           { return nil }
func (m *mockGraph) Clear() error                                                                { return nil }

// ---------------------------------------------------------------------------
// Helper: build a persister with short intervals, no goroutine started.
// ---------------------------------------------------------------------------

func newTestPersister(g Graph, base, max time.Duration) *AdaptivePersister {
	return &AdaptivePersister{
		graph:        g,
		filename:     "test-graph.json",
		logger:       zerolog.Nop(),
		baseInterval: base,
		maxInterval:  max,
		lastSave:     time.Now(),
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// ---------------------------------------------------------------------------
// NewAdaptivePersister — constructor
// ---------------------------------------------------------------------------

// TestNewAdaptivePersister_ReturnsInitialisedPersister verifies that the public
// constructor sets all fields to their documented defaults and that the
// persister is ready to Start without further configuration.
func TestNewAdaptivePersister_ReturnsInitialisedPersister(t *testing.T) {
	g := NewFlatGraph()
	filename := "my-graph.json"
	p := NewAdaptivePersister(g, filename, zerolog.Nop())

	if p == nil {
		t.Fatal("NewAdaptivePersister returned nil")
	}

	// Documented defaults.
	if p.baseInterval != 500*time.Millisecond {
		t.Errorf("baseInterval: want 500ms, got %v", p.baseInterval)
	}
	if p.maxInterval != 30*time.Second {
		t.Errorf("maxInterval: want 30s, got %v", p.maxInterval)
	}
	if p.filename != filename {
		t.Errorf("filename: want %q, got %q", filename, p.filename)
	}
	if p.graph != g {
		t.Error("graph field not set to provided graph")
	}

	// Channels must be non-nil and open so Start/Stop work immediately.
	if p.stopCh == nil {
		t.Error("stopCh is nil")
	}
	if p.doneCh == nil {
		t.Error("doneCh is nil")
	}

	// Must start clean.
	if p.dirty.Load() {
		t.Error("dirty should be false on construction")
	}
	if p.activeWriters.Load() != 0 {
		t.Error("activeWriters should be 0 on construction")
	}
}

// TestNewAdaptivePersister_StartStopRoundTrip verifies that a persister created
// via the public constructor (not the test helper) can Start and Stop without
// deadlocking or panicking — i.e. the channels are wired correctly.
func TestNewAdaptivePersister_StartStopRoundTrip(t *testing.T) {
	p := NewAdaptivePersister(NewFlatGraph(), "/dev/null", zerolog.Nop())
	p.Start()

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
		// correct
	case <-time.After(2 * time.Second):
		t.Error("Stop did not return within 2s — channel wiring broken in NewAdaptivePersister")
	}
}



func TestCurrentInterval_NoWriters_UsesBase(t *testing.T) {
	p := newTestPersister(&mockGraph{}, 500*time.Millisecond, 30*time.Second)
	// 0 writers clamps to 1: interval = base * (1 + ln(1)) = base * 1.0
	got := p.currentInterval()
	if got != 500*time.Millisecond {
		t.Errorf("0 writers: want 500ms, got %v", got)
	}
}

func TestCurrentInterval_OneWriter_UsesBase(t *testing.T) {
	p := newTestPersister(&mockGraph{}, 500*time.Millisecond, 30*time.Second)
	p.activeWriters.Store(1)
	got := p.currentInterval()
	if got != 500*time.Millisecond {
		t.Errorf("1 writer: want 500ms, got %v", got)
	}
}

func TestCurrentInterval_ScalesWithWriters(t *testing.T) {
	base := 500 * time.Millisecond
	p := newTestPersister(&mockGraph{}, base, 30*time.Second)

	cases := []struct {
		writers int64
	}{
		{3},
		{10},
		{50},
		{100},
	}
	prev := p.currentInterval()
	for _, c := range cases {
		p.activeWriters.Store(c.writers)
		got := p.currentInterval()
		if got <= prev {
			t.Errorf("writers=%d: interval %v should be greater than previous %v", c.writers, got, prev)
		}
		// Verify formula: interval = base * (1 + ln(writers))
		expected := time.Duration(float64(base) * (1.0 + math.Log(float64(c.writers))))
		if got != expected {
			t.Errorf("writers=%d: want %v (formula), got %v", c.writers, expected, got)
		}
		prev = got
	}
}

func TestCurrentInterval_CapsAtMaxInterval(t *testing.T) {
	p := newTestPersister(&mockGraph{}, 500*time.Millisecond, 1*time.Millisecond)
	p.activeWriters.Store(1000)
	got := p.currentInterval()
	if got != 1*time.Millisecond {
		t.Errorf("should be capped at max 1ms, got %v", got)
	}
}

// ---------------------------------------------------------------------------
// MarkDirty / WriterEnter / WriterExit
// ---------------------------------------------------------------------------

func TestMarkDirty_SetsDirtyFlag(t *testing.T) {
	p := newTestPersister(&mockGraph{}, time.Second, time.Minute)
	if p.dirty.Load() {
		t.Fatal("dirty should start false")
	}
	p.MarkDirty()
	if !p.dirty.Load() {
		t.Error("MarkDirty should set dirty to true")
	}
}

func TestWriterEnterExit_TracksCount(t *testing.T) {
	p := newTestPersister(&mockGraph{}, time.Second, time.Minute)

	p.WriterEnter()
	p.WriterEnter()
	if p.activeWriters.Load() != 2 {
		t.Errorf("after 2 enters: want 2, got %d", p.activeWriters.Load())
	}

	p.WriterExit()
	if p.activeWriters.Load() != 1 {
		t.Errorf("after 1 exit: want 1, got %d", p.activeWriters.Load())
	}

	p.WriterExit()
	if p.activeWriters.Load() != 0 {
		t.Errorf("after 2 exits: want 0, got %d", p.activeWriters.Load())
	}
}

// ---------------------------------------------------------------------------
// save — the core persistence unit
// ---------------------------------------------------------------------------

func TestSave_CallsGraphSaveWhenDirty(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Second, time.Minute)
	p.dirty.Store(true)

	p.save("test")

	if g.saveCount() != 1 {
		t.Errorf("Save should be called once, got %d", g.saveCount())
	}
}

func TestSave_ClearsDirtyOnSuccess(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Second, time.Minute)
	p.dirty.Store(true)

	p.save("test")

	if p.dirty.Load() {
		t.Error("dirty should be cleared after successful save")
	}
}

func TestSave_UpdatesLastSaveOnSuccess(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Second, time.Minute)
	before := p.lastSave
	p.dirty.Store(true)

	time.Sleep(2 * time.Millisecond)
	p.save("test")

	if !p.lastSave.After(before) {
		t.Error("lastSave should be updated after successful save")
	}
}

func TestSave_DoesNotSaveWhenNotDirty(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Second, time.Minute)
	// dirty is false by default

	p.save("test")

	if g.saveCount() != 0 {
		t.Errorf("Save should not be called when not dirty, got %d calls", g.saveCount())
	}
}

func TestSave_DoesNotClearDirtyOnError(t *testing.T) {
	g := &mockGraph{saveErr: errors.New("disk full")}
	p := newTestPersister(g, time.Second, time.Minute)
	p.dirty.Store(true)

	p.save("test")

	if !p.dirty.Load() {
		t.Error("dirty should remain true after a failed save")
	}
}

func TestSave_Idempotent_DoesNotDoubleSave(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Second, time.Minute)
	p.dirty.Store(true)

	p.save("first")
	p.save("second") // dirty is now false; second save should be a no-op

	if g.saveCount() != 1 {
		t.Errorf("second save should be a no-op, got %d total saves", g.saveCount())
	}
}

func TestSave_UsesConfiguredFilename(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Second, time.Minute)
	p.filename = "my-graph-data.json"
	p.dirty.Store(true)

	p.save("test")

	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.saveCalls) != 1 || g.saveCalls[0] != "my-graph-data.json" {
		t.Errorf("expected save to file my-graph-data.json, got %v", g.saveCalls)
	}
}

// ---------------------------------------------------------------------------
// Stats
// ---------------------------------------------------------------------------

func TestStats_ContainsExpectedKeys(t *testing.T) {
	p := newTestPersister(&mockGraph{}, 500*time.Millisecond, 30*time.Second)
	p.WriterEnter()
	p.MarkDirty()

	stats := p.Stats()

	keys := []string{"active_writers", "dirty", "current_interval", "last_save"}
	for _, k := range keys {
		if _, ok := stats[k]; !ok {
			t.Errorf("Stats missing key %q", k)
		}
	}
	if stats["active_writers"].(int64) != 1 {
		t.Errorf("active_writers: want 1, got %v", stats["active_writers"])
	}
	if stats["dirty"].(bool) != true {
		t.Errorf("dirty: want true, got %v", stats["dirty"])
	}
}

// ---------------------------------------------------------------------------
// Start / Stop lifecycle
// ---------------------------------------------------------------------------

func TestStop_TriggersFinalSaveWhenDirty(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Hour, time.Hour) // long interval: periodic save won't fire
	p.Start()

	p.MarkDirty()
	p.Stop()

	if g.saveCount() != 1 {
		t.Errorf("Stop should trigger final save when dirty, got %d saves", g.saveCount())
	}
	if p.dirty.Load() {
		t.Error("dirty should be cleared after final save on Stop")
	}
}

func TestStop_DoesNotSaveWhenNotDirty(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Hour, time.Hour)
	p.Start()

	// Never mark dirty
	p.Stop()

	if g.saveCount() != 0 {
		t.Errorf("Stop should not save when not dirty, got %d saves", g.saveCount())
	}
}

func TestStop_BlocksUntilLoopExits(t *testing.T) {
	p := newTestPersister(&mockGraph{}, time.Hour, time.Hour)
	p.Start()

	done := make(chan struct{})
	go func() {
		p.Stop()
		close(done)
	}()

	select {
	case <-done:
		// correct
	case <-time.After(2 * time.Second):
		t.Error("Stop did not return within 2s")
	}
}

// ---------------------------------------------------------------------------
// Periodic save via the loop
// ---------------------------------------------------------------------------

func TestLoop_SavesWhenDirtyAndIntervalElapsed(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, 10*time.Millisecond, time.Second)
	// Push lastSave far enough into the past that the interval has already elapsed.
	p.lastSave = time.Now().Add(-100 * time.Millisecond)
	p.Start()
	p.MarkDirty()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if g.saveCount() > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if g.saveCount() == 0 {
		t.Error("periodic save should have fired within 500ms")
	}

	p.Stop()
}

func TestLoop_DoesNotSaveWhenNotDirty(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, 1*time.Millisecond, time.Second)
	p.lastSave = time.Now().Add(-time.Second) // interval long past
	p.Start()

	// Give the loop several ticks without marking dirty.
	time.Sleep(50 * time.Millisecond)
	p.Stop()

	if g.saveCount() != 0 {
		t.Errorf("loop should not save when not dirty, got %d saves", g.saveCount())
	}
}

func TestLoop_DoesNotSaveBeforeIntervalElapsed(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Hour, time.Hour) // interval will never elapse
	p.Start()
	p.MarkDirty()

	time.Sleep(50 * time.Millisecond)

	// The loop should not have saved yet (interval hasn't elapsed).
	// Stop will trigger the final save.
	if g.saveCount() != 0 {
		t.Errorf("should not save before interval elapses, got %d saves", g.saveCount())
	}

	p.Stop()
}

// ---------------------------------------------------------------------------
// Concurrency: MarkDirty / WriterEnter / WriterExit under concurrent access
// ---------------------------------------------------------------------------

func TestConcurrent_WriterEnterExit(t *testing.T) {
	p := newTestPersister(&mockGraph{}, time.Hour, time.Hour)

	var wg sync.WaitGroup
	const goroutines = 50
	for i := 0; i < goroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.WriterEnter()
			time.Sleep(time.Millisecond)
			p.WriterExit()
		}()
	}
	wg.Wait()

	if got := p.activeWriters.Load(); got != 0 {
		t.Errorf("after all goroutines done: activeWriters want 0, got %d", got)
	}
}

func TestConcurrent_MarkDirtyAndSave(t *testing.T) {
	g := &mockGraph{}
	p := newTestPersister(g, time.Hour, time.Hour)

	var wg sync.WaitGroup
	var saveCount atomic.Int64

	// Multiple goroutines marking dirty concurrently.
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.MarkDirty()
		}()
	}

	// One goroutine saving concurrently.
	wg.Add(1)
	go func() {
		defer wg.Done()
		p.dirty.Store(true)
		p.save("concurrent")
		saveCount.Add(int64(g.saveCount()))
	}()

	wg.Wait()

	// No panic, no data race — that's the assertion.
	_ = saveCount.Load()
}

// ---------------------------------------------------------------------------
// Integration: real file on disk
// ---------------------------------------------------------------------------

func TestPersister_WritesRealFile(t *testing.T) {
	// Use an actual FlatGraph so Save writes a real file.
	fg := NewFlatGraph()
	_ = fg.AddNode("post:1", "post")
	_ = fg.AddNode("author:1", "author")
	_ = fg.AddEdge("post:1", "author:1", "written_by")

	tmp, err := os.CreateTemp(t.TempDir(), "graph-*.json")
	if err != nil {
		t.Fatal(err)
	}
	tmp.Close()
	filename := tmp.Name()

	p := newTestPersister(fg, 10*time.Millisecond, time.Second)
	p.filename = filename
	p.lastSave = time.Now().Add(-100 * time.Millisecond)
	p.Start()
	p.MarkDirty()

	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if !p.dirty.Load() {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	p.Stop()

	info, err := os.Stat(filename)
	if err != nil {
		t.Fatalf("graph file not found: %v", err)
	}
	if info.Size() == 0 {
		t.Error("graph file should be non-empty")
	}
}
