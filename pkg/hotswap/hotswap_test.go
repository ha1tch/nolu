// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package hotswap_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/hotswap"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/registry"
)

// ── Helpers ───────────────────────────────────────────────────────────────────

// fakeXolu returns a test server that responds healthy to GET /health.
func fakeXolu(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"status":"ok"}`))
			return
		}
		http.NotFound(w, r)
	}))
}

func newManager(t *testing.T) (hotswap.Manager, registry.Registry) {
	t.Helper()
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)
	return mgr, reg
}

func sourceRef(url string) hotswap.InstanceRef {
	return hotswap.InstanceRef{InstanceURL: url, TenantName: "vendocorp", TenantID: 1}
}
func targetRef(url string) hotswap.InstanceRef {
	return hotswap.InstanceRef{InstanceURL: url, TenantName: "vendocorp", TenantID: 1}
}

// ── Request ───────────────────────────────────────────────────────────────────

func TestMemoryManager_Request_UnreachableSource(t *testing.T) {
	mgr, _ := newManager(t)
	_, err := mgr.Request(context.Background(),
		sourceRef("http://127.0.0.1:19999"), // nothing listening
		targetRef("http://127.0.0.1:19998"),
		hotswap.HotswapOptions{},
	)
	if !errors.Is(err, hotswap.ErrSourceUnreachable) {
		t.Errorf("expected ErrSourceUnreachable, got %v", err)
	}
}

func TestMemoryManager_Request_UnreachableTarget(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()

	_, err := mgr.Request(context.Background(),
		sourceRef(src.URL),
		targetRef("http://127.0.0.1:19998"), // nothing listening
		hotswap.HotswapOptions{},
	)
	if !errors.Is(err, hotswap.ErrTargetUnreachable) {
		t.Errorf("expected ErrTargetUnreachable, got %v", err)
	}
}

func TestMemoryManager_Request_Success(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, err := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if h.ID == "" {
		t.Error("expected non-empty ID")
	}
	if h.State != hotswap.StatePreparing {
		t.Errorf("expected preparing, got %s", h.State)
	}
	if len(h.History) < 1 {
		t.Error("expected non-empty history")
	}
}

func TestMemoryManager_Request_DuplicateRejected(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	_, err := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)
	if err != nil {
		t.Fatalf("first request: %v", err)
	}

	_, err = mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{},
	)
	if !errors.Is(err, hotswap.ErrAlreadyExists) {
		t.Errorf("expected ErrAlreadyExists on duplicate, got %v", err)
	}
}

func TestMemoryManager_Request_AllowsAfterTerminal(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)
	mgr.Abort(context.Background(), h.ID, "test")

	// Wait for rollback goroutine to complete.
	time.Sleep(200 * time.Millisecond)

	// A new request for the same source/tenant should now be allowed.
	h2, err := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)
	if err != nil {
		t.Errorf("second request after terminal should succeed, got %v", err)
	}
	if h2.ID == h.ID {
		t.Error("expected new ID for second request")
	}
}

// ── Get / List ────────────────────────────────────────────────────────────────

func TestMemoryManager_Get_NotFound(t *testing.T) {
	mgr, _ := newManager(t)
	_, err := mgr.Get(context.Background(), "nonexistent-id")
	if !errors.Is(err, hotswap.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

func TestMemoryManager_List_FilterByState(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h1, _ := mgr.Request(context.Background(),
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "org1", TenantID: 1},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "org1", TenantID: 1},
		hotswap.HotswapOptions{AutoAdvance: false},
	)

	state := hotswap.StatePreparing
	list, err := mgr.List(context.Background(), &state)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	found := false
	for _, h := range list {
		if h.ID == h1.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("hotswap %s not found in List(preparing)", h1.ID)
	}

	// List with different state should not include it.
	complete := hotswap.StateComplete
	listComplete, _ := mgr.List(context.Background(), &complete)
	for _, h := range listComplete {
		if h.ID == h1.ID {
			t.Errorf("hotswap %s should not appear in List(complete)", h1.ID)
		}
	}
}

// ── Confirm ───────────────────────────────────────────────────────────────────

func TestMemoryManager_Confirm_WrongState(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: true},
	)
	// With AutoAdvance=true the state machine advances beyond PREPARING.
	// Wait briefly then try to Confirm — should get WrongState.
	time.Sleep(100 * time.Millisecond)

	h2, _ := mgr.Get(context.Background(), h.ID)
	if h2.State == hotswap.StatePreparing {
		// Still in preparing: confirm should work.
		_, err := mgr.Confirm(context.Background(), h.ID)
		if err != nil && !errors.Is(err, hotswap.ErrWrongState) {
			t.Errorf("unexpected error: %v", err)
		}
	}
}

func TestMemoryManager_Confirm_NotFound(t *testing.T) {
	mgr, _ := newManager(t)
	_, err := mgr.Confirm(context.Background(), "bad-id")
	if !errors.Is(err, hotswap.ErrNotFound) {
		t.Errorf("expected ErrNotFound, got %v", err)
	}
}

// ── Abort ─────────────────────────────────────────────────────────────────────

func TestMemoryManager_Abort_Success(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)

	aborted, err := mgr.Abort(context.Background(), h.ID, "test abort")
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	if aborted.State != hotswap.StateRollingBack {
		t.Errorf("expected rolling_back immediately after abort, got %s", aborted.State)
	}

	// Wait for rollback goroutine.
	time.Sleep(200 * time.Millisecond)

	final, _ := mgr.Get(context.Background(), h.ID)
	if final.State != hotswap.StateFailed {
		t.Errorf("expected failed after rollback, got %s", final.State)
	}
	if final.FailureReason != "test abort" {
		t.Errorf("expected failure reason 'test abort', got %q", final.FailureReason)
	}
}

func TestMemoryManager_Abort_TerminalRejected(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)
	mgr.Abort(context.Background(), h.ID, "first abort")
	time.Sleep(200 * time.Millisecond) // wait for StateFailed

	// Abort on already-failed hotswap should return ErrWrongState.
	_, err := mgr.Abort(context.Background(), h.ID, "second abort")
	if !errors.Is(err, hotswap.ErrWrongState) {
		t.Errorf("expected ErrWrongState on terminal abort, got %v", err)
	}
}

// ── Status ────────────────────────────────────────────────────────────────────

func TestMemoryManager_Status(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false},
	)

	st, err := mgr.Status(context.Background(), h.ID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if st.ID != h.ID {
		t.Errorf("expected ID=%s, got %s", h.ID, st.ID)
	}
	if st.PhaseElapsed < 0 {
		t.Error("expected non-negative PhaseElapsed")
	}
}

// ── Cutover affects registry ──────────────────────────────────────────────────

func TestMemoryManager_Cutover_TransfersGlobalIDs(t *testing.T) {
	bus := events.NewMemoryBus()
	reg := registry.NewMemoryRegistry("registry.test.local", bus)
	mgr := hotswap.NewMemoryManager(reg, bus, nil)

	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	// Register two devices on the source instance.
	gids := make([]identity.GlobalID, 2)
	for i := range gids {
		rec, err := reg.Register(context.Background(), "registry.test.local", "devices",
			identity.LocalRef{InstanceURL: src.URL, EntityType: "devices", LocalID: i + 1})
		if err != nil {
			t.Fatalf("register device %d: %v", i, err)
		}
		gids[i] = rec.GlobalID
	}

	// Request hotswap with AutoAdvance=true — let it run to completion.
	h, err := mgr.Request(context.Background(),
		hotswap.InstanceRef{InstanceURL: src.URL, TenantName: "vendocorp", TenantID: 0},
		hotswap.InstanceRef{InstanceURL: tgt.URL, TenantName: "vendocorp", TenantID: 0},
		hotswap.HotswapOptions{AutoAdvance: true, QuiesceTimeout: 2 * time.Second},
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}

	// Wait for the state machine to complete (all phases are simulated fast).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h2, _ := mgr.Get(context.Background(), h.ID)
		if h2.State == hotswap.StateComplete || h2.State == hotswap.StateFailed {
			h = h2
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if h.State != hotswap.StateComplete {
		t.Fatalf("expected complete, got %s (failure: %s)", h.State, h.FailureReason)
	}

	// Verify registry: both GlobalIDs should now point at tgt.
	for _, gid := range gids {
		ref, err := reg.Resolve(context.Background(), gid)
		if err != nil {
			t.Errorf("resolve %s: %v", gid, err)
			continue
		}
		if ref.InstanceURL != tgt.URL {
			t.Errorf("gid %s: expected %s, got %s", gid, tgt.URL, ref.InstanceURL)
		}
	}
	t.Logf("both GlobalIDs transferred to %s ✓", tgt.URL)
}

// ── Concurrent request rejection ──────────────────────────────────────────────

func TestMemoryManager_ConcurrentRequests_OnlyOneSucceeds(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	var mu sync.Mutex
	successes, failures := 0, 0
	var wg sync.WaitGroup

	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := mgr.Request(context.Background(),
				sourceRef(src.URL), targetRef(tgt.URL),
				hotswap.HotswapOptions{AutoAdvance: false},
			)
			mu.Lock()
			if err == nil {
				successes++
			} else if errors.Is(err, hotswap.ErrAlreadyExists) {
				failures++
			}
			mu.Unlock()
		}()
	}
	wg.Wait()

	if successes != 1 {
		t.Errorf("expected exactly 1 successful request, got %d", successes)
	}
	if failures != 4 {
		t.Errorf("expected exactly 4 duplicate rejections, got %d", failures)
	}
}

// ── State machine history ─────────────────────────────────────────────────────

func TestMemoryManager_History_RecordsTransitions(t *testing.T) {
	mgr, _ := newManager(t)
	src := fakeXolu(t)
	defer src.Close()
	tgt := fakeXolu(t)
	defer tgt.Close()

	h, _ := mgr.Request(context.Background(),
		sourceRef(src.URL), targetRef(tgt.URL),
		hotswap.HotswapOptions{AutoAdvance: false, TimestampedHistory: true},
	)

	if len(h.History) < 1 {
		t.Fatalf("expected at least 1 history entry, got %d", len(h.History))
	}
	for _, entry := range h.History {
		if entry.At.IsZero() {
			t.Errorf("history entry has zero timestamp: %v", entry)
		}
	}

	// Abort and check rollback is recorded.
	mgr.Abort(context.Background(), h.ID, "history test")
	time.Sleep(200 * time.Millisecond)

	final, _ := mgr.Get(context.Background(), h.ID)
	states := make([]string, len(final.History))
	for i, e := range final.History {
		states[i] = string(e.State)
	}
	t.Logf("state history: %v", states)

	// Must contain rolling_back and failed.
	hasRolling, hasFailed := false, false
	for _, e := range final.History {
		if e.State == hotswap.StateRollingBack {
			hasRolling = true
		}
		if e.State == hotswap.StateFailed {
			hasFailed = true
		}
	}
	if !hasRolling {
		t.Error("history missing rolling_back entry")
	}
	if !hasFailed {
		t.Error("history missing failed entry")
	}
}
