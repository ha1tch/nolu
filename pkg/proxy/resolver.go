// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package proxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ha1tch/nolu/pkg/identity"
)

// TenantLocation is the resolved location of a tenant on a specific xolu instance.
type TenantLocation struct {
	// TenantName is the human-readable name.
	TenantName string `json:"tenant"`

	// InstanceURL is the base URL of the xolu instance currently serving
	// this tenant. Example: "http://xolu-hub-b:9091"
	InstanceURL string `json:"instance_url"`

	// TenantID is the numeric tenant ID on this instance, used to construct
	// the correct xolu API path: /api/v1/tenant/{TenantID}/...
	TenantID uint16 `json:"tenant_id"`

	// StableUntil is the proxy's assertion that this location is stable.
	// Callers may cache the result until this time. During a hotswap nolu
	// sets this to now (or the near future) to force rapid revalidation.
	StableUntil time.Time `json:"stable_until"`
}

// XoluPath returns the xolu API base path for this tenant.
// Example: "/api/v1/tenant/1"
func (l TenantLocation) XoluPath() string {
	if l.TenantID == 0 {
		return "/api/v1"
	}
	return fmt.Sprintf("/api/v1/tenant/%d", l.TenantID)
}

// Resolver resolves a tenant name to its current xolu location.
// Implementations must be safe for concurrent use.
type Resolver interface {
	// Locate returns the current location for the named tenant.
	// Returns ErrTenantNotFound if the tenant is not registered.
	// Returns ErrTenantUnstable if a hotswap is in progress (caller should
	// retry with backoff).
	Locate(ctx context.Context, tenantName string) (TenantLocation, error)

	// Invalidate removes a tenant from the resolver's cache, forcing the
	// next Locate call to fetch fresh data. Called by the proxy when it
	// receives a 307 from xolu indicating the tenant has moved.
	Invalidate(tenantName string)
}

// Sentinel errors returned by Resolver implementations.
var (
	ErrTenantNotFound  = fmt.Errorf("proxy: tenant not found")
	ErrTenantUnstable  = fmt.Errorf("proxy: tenant location is unstable (hotswap in progress)")
	ErrResolverTimeout = fmt.Errorf("proxy: resolver timeout")
)

// ── RegistryResolver ──────────────────────────────────────────────────────────

// RegistryResolver resolves tenants by calling the nolu registry directly
// in-process. Used in embedded mode where the proxy runs inside nolu.
//
// It wraps a RegistryLocator — a function that looks up a tenant name in the
// nolu registry and returns its LocalRef. This keeps pkg/proxy free of any
// direct dependency on pkg/registry, preserving the package's extractability.
type RegistryResolver struct {
	locate func(ctx context.Context, tenantName string) (identity.LocalRef, error)
	cache  *locationCache
}

// RegistryLocatorFunc is the function signature that RegistryResolver needs.
// In embedded mode, nolu wires this to registry.Registry.LocateTenant.
type RegistryLocatorFunc func(ctx context.Context, tenantName string) (identity.LocalRef, error)

// NewRegistryResolver creates a Resolver that calls locator for each cache miss.
func NewRegistryResolver(locator RegistryLocatorFunc, cfg Config) *RegistryResolver {
	return &RegistryResolver{
		locate: locator,
		cache:  newLocationCache(cfg.CacheSize, cfg.CacheTTL),
	}
}

func (r *RegistryResolver) Locate(ctx context.Context, tenantName string) (TenantLocation, error) {
	if loc, ok := r.cache.get(tenantName); ok {
		return loc, nil
	}

	ref, err := r.locate(ctx, tenantName)
	if err != nil {
		return TenantLocation{}, fmt.Errorf("%w: %s: %v", ErrTenantNotFound, tenantName, err)
	}

	loc := TenantLocation{
		TenantName:  tenantName,
		InstanceURL: ref.InstanceURL,
		TenantID:    ref.TenantID,
		StableUntil: time.Now().Add(r.cache.ttl),
	}
	r.cache.set(tenantName, loc)
	return loc, nil
}

func (r *RegistryResolver) Invalidate(tenantName string) {
	r.cache.delete(tenantName)
}

// Compile-time assertion.
var _ Resolver = (*RegistryResolver)(nil)

// ── HTTPResolver ──────────────────────────────────────────────────────────────

// HTTPResolver resolves tenants by calling nolu's /tenants/{name}/locate
// endpoint over HTTP. Used in sidecar mode where the proxy runs separately
// from nolu.
type HTTPResolver struct {
	noluURL string
	client  *http.Client
	cache   *locationCache
}

// NewHTTPResolver creates a Resolver that calls noluURL/tenants/{name}/locate
// for each cache miss.
func NewHTTPResolver(noluURL string, cfg Config) *HTTPResolver {
	return &HTTPResolver{
		noluURL: noluURL,
		client: &http.Client{
			Timeout: cfg.DialTimeout + cfg.ForwardTimeout,
		},
		cache: newLocationCache(cfg.CacheSize, cfg.CacheTTL),
	}
}

// locateResponse is the JSON shape of /tenants/{name}/locate.
type locateResponse struct {
	Tenant      string    `json:"tenant"`
	InstanceURL string    `json:"instance_url"`
	TenantID    uint16    `json:"tenant_id"`
	StableUntil time.Time `json:"stable_until"`
}

func (r *HTTPResolver) Locate(ctx context.Context, tenantName string) (TenantLocation, error) {
	if loc, ok := r.cache.get(tenantName); ok {
		return loc, nil
	}

	url := fmt.Sprintf("%s/tenants/%s/locate", r.noluURL, tenantName)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return TenantLocation{}, fmt.Errorf("proxy: build locate request: %w", err)
	}

	resp, err := r.client.Do(req)
	if err != nil {
		return TenantLocation{}, fmt.Errorf("proxy: locate %q: %w", tenantName, err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		// ok
	case http.StatusNotFound:
		return TenantLocation{}, fmt.Errorf("%w: %s", ErrTenantNotFound, tenantName)
	case http.StatusServiceUnavailable:
		return TenantLocation{}, fmt.Errorf("%w: %s", ErrTenantUnstable, tenantName)
	default:
		return TenantLocation{}, fmt.Errorf("proxy: locate %q: nolu returned %d", tenantName, resp.StatusCode)
	}

	var lr locateResponse
	if err := json.NewDecoder(resp.Body).Decode(&lr); err != nil {
		return TenantLocation{}, fmt.Errorf("proxy: decode locate response: %w", err)
	}

	loc := TenantLocation{
		TenantName:  lr.Tenant,
		InstanceURL: lr.InstanceURL,
		TenantID:    lr.TenantID,
		StableUntil: lr.StableUntil,
	}

	// Honour StableUntil from nolu if it is shorter than our configured TTL.
	cacheTTL := time.Until(loc.StableUntil)
	if cacheTTL > 0 {
		r.cache.setWithTTL(tenantName, loc, cacheTTL)
	}
	return loc, nil
}

func (r *HTTPResolver) Invalidate(tenantName string) {
	r.cache.delete(tenantName)
}

// Compile-time assertion.
var _ Resolver = (*HTTPResolver)(nil)
