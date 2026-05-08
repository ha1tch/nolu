// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package proxy_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/proxy"
)

// ── locationCache ─────────────────────────────────────────────────────────────

func TestCache_SetGet(t *testing.T) {
	cfg := proxy.Config{CacheSize: 10, CacheTTL: time.Second}
	// Access cache indirectly via RegistryResolver.
	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		return identity.LocalRef{InstanceURL: "http://xolu:9090", TenantID: 1}, nil
	}
	r := proxy.NewRegistryResolver(locator, cfg)

	loc1, err := r.Locate(context.Background(), "vendocorp")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if loc1.InstanceURL != "http://xolu:9090" {
		t.Errorf("unexpected instance: %s", loc1.InstanceURL)
	}

	// Second call should hit cache (locator would return different value now
	// if called, but cache returns the first).
	calls := 0
	locator2 := func(ctx context.Context, name string) (identity.LocalRef, error) {
		calls++
		return identity.LocalRef{InstanceURL: "http://xolu-new:9090"}, nil
	}
	r2 := proxy.NewRegistryResolver(locator2, cfg)
	r2.Locate(context.Background(), "vendocorp") // prime cache
	r2.Locate(context.Background(), "vendocorp") // should hit cache
	r2.Locate(context.Background(), "vendocorp") // should hit cache

	if calls > 1 {
		t.Errorf("cache miss: locator called %d times, expected 1", calls)
	}
}

func TestCache_Invalidate(t *testing.T) {
	calls := 0
	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		calls++
		return identity.LocalRef{InstanceURL: fmt.Sprintf("http://xolu-%d:9090", calls)}, nil
	}
	cfg := proxy.Config{CacheSize: 10, CacheTTL: time.Minute}
	r := proxy.NewRegistryResolver(locator, cfg)

	loc1, _ := r.Locate(context.Background(), "vendocorp")
	r.Invalidate("vendocorp")
	loc2, _ := r.Locate(context.Background(), "vendocorp")

	if loc1.InstanceURL == loc2.InstanceURL {
		t.Errorf("invalidate did not clear cache: both returned %s", loc1.InstanceURL)
	}
	if calls != 2 {
		t.Errorf("expected 2 locator calls after invalidate, got %d", calls)
	}
}

func TestCache_TTLExpiry(t *testing.T) {
	calls := 0
	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		calls++
		return identity.LocalRef{InstanceURL: "http://xolu:9090"}, nil
	}
	cfg := proxy.Config{CacheSize: 10, CacheTTL: 50 * time.Millisecond}
	r := proxy.NewRegistryResolver(locator, cfg)

	r.Locate(context.Background(), "vendocorp")
	time.Sleep(80 * time.Millisecond) // wait for TTL expiry
	r.Locate(context.Background(), "vendocorp")

	if calls < 2 {
		t.Errorf("expected cache miss after TTL expiry, got %d calls", calls)
	}
}

func TestCache_LRUEviction(t *testing.T) {
	// Cache holds 2 entries. Fill it, add a third, verify one was evicted
	// by checking that total fetch calls exceed the number of tenants.
	cfg := proxy.Config{CacheSize: 2, CacheTTL: time.Minute}
	calls := map[string]int{}
	mu := sync.Mutex{}
	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		mu.Lock()
		calls[name]++
		mu.Unlock()
		return identity.LocalRef{InstanceURL: "http://xolu:9090"}, nil
	}
	r := proxy.NewRegistryResolver(locator, cfg)

	// Fill cache to capacity.
	r.Locate(context.Background(), "a") // calls[a]=1
	r.Locate(context.Background(), "b") // calls[b]=1
	// Add c — evicts one of {a, b} (LRU = a).
	r.Locate(context.Background(), "c") // calls[c]=1
	// Access all three. The evicted one will be re-fetched.
	r.Locate(context.Background(), "a")
	r.Locate(context.Background(), "b")
	r.Locate(context.Background(), "c")

	mu.Lock()
	total := calls["a"] + calls["b"] + calls["c"]
	mu.Unlock()

	// With capacity=2 and 3 distinct tenants, at least one must have been
	// evicted and re-fetched, so total calls must be > 3.
	if total <= 3 {
		t.Errorf("expected eviction to cause re-fetch (total calls > 3), got %d", total)
	}
}

func TestCache_Concurrent(t *testing.T) {
	cfg := proxy.Config{CacheSize: 100, CacheTTL: time.Second}
	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		return identity.LocalRef{InstanceURL: "http://xolu:9090"}, nil
	}
	r := proxy.NewRegistryResolver(locator, cfg)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			name := fmt.Sprintf("tenant-%d", n%10)
			r.Locate(context.Background(), name)
			r.Invalidate(name)
			r.Locate(context.Background(), name)
		}(i)
	}
	wg.Wait()
	// No panic or data race — success.
}

// ── Proxy path parsing ────────────────────────────────────────────────────────

func TestProxy_PathParsing(t *testing.T) {
	cases := []struct {
		mount      string
		path       string
		wantTenant string
		wantXolu   string
		wantErr    bool
	}{
		{"/proxy", "/proxy/tenant/vendocorp/devices/42", "vendocorp", "/devices/42", false},
		{"/proxy", "/proxy/tenant/vendocorp/", "vendocorp", "/", false},
		{"/proxy", "/proxy/tenant/vendocorp", "vendocorp", "/", false},
		{"/proxy", "/proxy/tenant/retailchain/entities/foo/bar?q=1", "retailchain", "/entities/foo/bar", false},
		{"/", "/tenant/vendocorp/devices/1", "vendocorp", "/devices/1", false},
		{"/", "/tenant/vendocorp", "vendocorp", "/", false},
		// Error cases
		{"/proxy", "/proxy/notenant/vendocorp", "", "", true},
		{"/proxy", "/proxy/tenant/", "", "", true},
		{"/proxy", "/other/path", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.path, func(t *testing.T) {
			cfg := proxy.Config{
				MountPath:      tc.mount,
				CacheSize:      10,
				CacheTTL:       time.Minute,
				ForwardTimeout: 5 * time.Second,
				MaxRedirects:   3,
			}
			locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
				return identity.LocalRef{InstanceURL: "http://xolu:9090", TenantID: 1}, nil
			}
			resolver := proxy.NewRegistryResolver(locator, cfg)
			p := proxy.New(resolver, cfg)

			// Use a fake upstream to capture what the proxy sends.
			var capturedPath string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				capturedPath = r.URL.Path
				w.WriteHeader(http.StatusOK)
			}))
			defer upstream.Close()

			// Override resolver to return our test upstream.
			locator2 := func(ctx context.Context, name string) (identity.LocalRef, error) {
				return identity.LocalRef{
					InstanceURL: upstream.URL,
					TenantID:    1,
					EntityType:  "",
					LocalID:     0,
				}, nil
			}
			resolver2 := proxy.NewRegistryResolver(locator2, cfg)
			p2 := proxy.New(resolver2, cfg)

			req := httptest.NewRequest(http.MethodGet, tc.path, nil)
			w := httptest.NewRecorder()
			p2.ServeHTTP(w, req)

			if tc.wantErr {
				if w.Code == http.StatusOK {
					t.Errorf("expected error response for path %q, got 200", tc.path)
				}
				return
			}

			if w.Code != http.StatusOK {
				t.Errorf("expected 200 for path %q, got %d", tc.path, w.Code)
			}

			// Verify the xolu path was correct.
			// Path is /api/v1/tenant/{id}{xoluPath}
			expectedSuffix := tc.wantXolu
			if capturedPath == "" {
				t.Errorf("upstream never received request for path %q", tc.path)
			}
			if len(capturedPath) > 0 && !hasSuffix(capturedPath, expectedSuffix) {
				t.Errorf("path %q: expected xolu path suffix %q, got full path %q",
					tc.path, expectedSuffix, capturedPath)
			}
			_ = p
		})
	}
}

func hasSuffix(s, suffix string) bool {
	if suffix == "/" {
		return true // root always matches
	}
	return len(s) >= len(suffix) && s[len(s)-len(suffix):] == suffix
}

// ── 307 redirect handling ─────────────────────────────────────────────────────

func TestProxy_307_CacheInvalidationAndRetry(t *testing.T) {
	// Upstream 1: returns 307 on first request, then the resolver
	// switches to upstream 2.
	var mu sync.Mutex
	redirectCount := 0
	upstream2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Served-By", "upstream2")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream2.Close()

	upstream1 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		redirectCount++
		mu.Unlock()
		w.Header().Set("Location", upstream2.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer upstream1.Close()

	// Resolver returns upstream1 first (from cache), then upstream2 after
	// cache is invalidated.
	locateCalls := 0
	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		locateCalls++
		if locateCalls == 1 {
			return identity.LocalRef{InstanceURL: upstream1.URL, TenantID: 0}, nil
		}
		return identity.LocalRef{InstanceURL: upstream2.URL, TenantID: 0}, nil
	}

	cfg := proxy.Config{
		MountPath:      "/proxy",
		CacheSize:      10,
		CacheTTL:       time.Minute,
		ForwardTimeout: 5 * time.Second,
		DialTimeout:    2 * time.Second,
		MaxRedirects:   3,
	}
	resolver := proxy.NewRegistryResolver(locator, cfg)
	p := proxy.New(resolver, cfg)

	req := httptest.NewRequest(http.MethodGet, "/proxy/tenant/vendocorp/devices/1", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 after 307 retry, got %d", w.Code)
	}
	if w.Header().Get("X-Served-By") != "upstream2" {
		t.Errorf("expected to be served by upstream2, header: %q", w.Header().Get("X-Served-By"))
	}
	mu.Lock()
	rc := redirectCount
	mu.Unlock()
	if rc != 1 {
		t.Errorf("expected exactly 1 redirect from upstream1, got %d", rc)
	}
	if locateCalls < 2 {
		t.Errorf("expected resolver to be called at least twice (initial + after 307), got %d", locateCalls)
	}
}

func TestProxy_307_MaxRedirectsExceeded(t *testing.T) {
	// Upstream always returns 307 — proxy should give up after MaxRedirects.
	infinite307 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://somewhere")
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer infinite307.Close()

	locator := func(ctx context.Context, name string) (identity.LocalRef, error) {
		return identity.LocalRef{InstanceURL: infinite307.URL}, nil
	}
	cfg := proxy.Config{
		MountPath:      "/proxy",
		CacheSize:      10,
		CacheTTL:       time.Second,
		ForwardTimeout: 5 * time.Second,
		DialTimeout:    2 * time.Second,
		MaxRedirects:   2,
	}
	resolver := proxy.NewRegistryResolver(locator, cfg)
	p := proxy.New(resolver, cfg)

	req := httptest.NewRequest(http.MethodGet, "/proxy/tenant/vendocorp/devices/1", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)

	if w.Code == http.StatusOK {
		t.Errorf("expected error after exceeding max redirects, got 200")
	}
	if w.Code != http.StatusBadGateway {
		t.Logf("got status %d (non-200, acceptable)", w.Code)
	}
}

// ── HTTPResolver ──────────────────────────────────────────────────────────────

func TestHTTPResolver_Locate(t *testing.T) {
	// Fake nolu server that serves /tenants/{name}/locate.
	noluSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tenants/vendocorp/locate" {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprintf(w, `{
				"tenant":"vendocorp",
				"instance_url":"http://xolu-hub:9090",
				"tenant_id":1,
				"stable_until":"%s"
			}`, time.Now().Add(30*time.Second).UTC().Format(time.RFC3339))
			return
		}
		http.NotFound(w, r)
	}))
	defer noluSrv.Close()

	cfg := proxy.Config{
		NoluURL:        noluSrv.URL,
		CacheSize:      10,
		CacheTTL:       time.Minute,
		ForwardTimeout: 5 * time.Second,
		DialTimeout:    2 * time.Second,
	}
	r := proxy.NewHTTPResolver(noluSrv.URL, cfg)

	loc, err := r.Locate(context.Background(), "vendocorp")
	if err != nil {
		t.Fatalf("locate: %v", err)
	}
	if loc.InstanceURL != "http://xolu-hub:9090" {
		t.Errorf("unexpected instance: %s", loc.InstanceURL)
	}
	if loc.TenantID != 1 {
		t.Errorf("unexpected tenant_id: %d", loc.TenantID)
	}
}

func TestHTTPResolver_NotFound(t *testing.T) {
	noluSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer noluSrv.Close()

	cfg := proxy.Config{CacheSize: 10, CacheTTL: time.Minute, DialTimeout: 2 * time.Second, ForwardTimeout: 5 * time.Second}
	r := proxy.NewHTTPResolver(noluSrv.URL, cfg)

	_, err := r.Locate(context.Background(), "unknown")
	if err == nil {
		t.Error("expected error for unknown tenant, got nil")
	}
}

func TestHTTPResolver_Caches(t *testing.T) {
	calls := 0
	noluSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"tenant":"t","instance_url":"http://x:9090","tenant_id":1,"stable_until":"%s"}`,
			time.Now().Add(time.Minute).UTC().Format(time.RFC3339))
	}))
	defer noluSrv.Close()

	cfg := proxy.Config{CacheSize: 10, CacheTTL: time.Minute, DialTimeout: 2 * time.Second, ForwardTimeout: 5 * time.Second}
	r := proxy.NewHTTPResolver(noluSrv.URL, cfg)

	r.Locate(context.Background(), "t")
	r.Locate(context.Background(), "t")
	r.Locate(context.Background(), "t")

	if calls > 1 {
		t.Errorf("expected 1 upstream call (cached), got %d", calls)
	}
}

// ── TenantLocation.XoluPath ───────────────────────────────────────────────────

func TestTenantLocation_XoluPath(t *testing.T) {
	cases := []struct {
		tenantID uint16
		want     string
	}{
		{0, "/api/v1"},
		{1, "/api/v1/tenant/1"},
		{42, "/api/v1/tenant/42"},
		{65535, "/api/v1/tenant/65535"},
	}
	for _, tc := range cases {
		loc := proxy.TenantLocation{TenantID: tc.tenantID}
		if got := loc.XoluPath(); got != tc.want {
			t.Errorf("TenantID=%d: expected %q, got %q", tc.tenantID, tc.want, got)
		}
	}
}
