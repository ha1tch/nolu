// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// Package proxy provides a tenant-aware reverse proxy for xolu instances.
//
// The proxy resolves tenant names to their current xolu instance using a
// Resolver, then forwards HTTP requests transparently. It handles 307
// redirects from xolu (indicating a tenant has moved), caches resolved
// locations with a configurable TTL, and can be mounted as a sub-handler
// within nolu's HTTP API or run as a standalone sidecar server.
//
// Deployment modes:
//
//  1. Embedded: nolu imports pkg/proxy and mounts it at /proxy/.
//     The Resolver calls the in-process registry directly (zero network hop).
//
//  2. Sidecar: cmd/nolu-proxy imports pkg/proxy and mounts it as the sole
//     handler. The Resolver calls nolu's /tenants/{name}/locate endpoint
//     over HTTP. The sidecar runs independently and can be scaled separately.
//
// Both modes use identical proxy logic. The only difference is the Resolver
// implementation injected at startup.
package proxy

import (
	"time"
)

// Config holds all proxy configuration. It is deliberately independent of
// nolu's main Config so that it can be used by the standalone sidecar without
// importing nolu's registry or event bus.
//
// All fields have sensible defaults via Default(). In both embedded and
// sidecar mode, configuration is loaded from environment variables with the
// NOLU_PROXY_ prefix, or from flags.
type Config struct {
	// ListenAddr is the address the proxy HTTP server listens on.
	// Only used in sidecar mode. In embedded mode nolu controls the listener.
	// Default: ":7071"
	// Env: NOLU_PROXY_LISTEN
	ListenAddr string `json:"listen_addr" env:"NOLU_PROXY_LISTEN"`

	// MountPath is the URL path prefix the proxy is mounted at.
	// Embedded: "/proxy" (requests arrive as /proxy/tenant/{name}/...)
	// Sidecar:  "/"      (requests arrive as /tenant/{name}/...)
	// The proxy strips this prefix before forwarding.
	// Default: "/proxy"
	// Env: NOLU_PROXY_MOUNT
	MountPath string `json:"mount_path" env:"NOLU_PROXY_MOUNT"`

	// NoluURL is the nolu registry URL used by the sidecar Resolver to
	// call GET /tenants/{name}/locate. Not used in embedded mode.
	// Example: "http://nolu:7070"
	// Env: NOLU_PROXY_NOLU_URL
	NoluURL string `json:"nolu_url" env:"NOLU_PROXY_NOLU_URL"`

	// CacheTTL is how long a resolved tenant location is cached.
	// A shorter TTL means faster detection of tenant migrations at the cost
	// of more resolve calls. During a hotswap nolu signals cache invaliation
	// via 307, so the TTL is the worst-case staleness for callers who do not
	// encounter a redirect.
	// Default: 30s
	// Env: NOLU_PROXY_CACHE_TTL
	CacheTTL time.Duration `json:"cache_ttl" env:"NOLU_PROXY_CACHE_TTL"`

	// CacheSize is the maximum number of tenant locations to cache.
	// Entries are evicted LRU when the cache is full.
	// Default: 1024
	// Env: NOLU_PROXY_CACHE_SIZE
	CacheSize int `json:"cache_size" env:"NOLU_PROXY_CACHE_SIZE"`

	// DialTimeout is the timeout for establishing a connection to a xolu
	// instance when forwarding a request.
	// Default: 5s
	// Env: NOLU_PROXY_DIAL_TIMEOUT
	DialTimeout time.Duration `json:"dial_timeout" env:"NOLU_PROXY_DIAL_TIMEOUT"`

	// ForwardTimeout is the total timeout for a forwarded request, including
	// the upstream response. Does not include the time to resolve the tenant.
	// Default: 30s
	// Env: NOLU_PROXY_FORWARD_TIMEOUT
	ForwardTimeout time.Duration `json:"forward_timeout" env:"NOLU_PROXY_FORWARD_TIMEOUT"`

	// MaxRedirects is the maximum number of 307 redirects the proxy will
	// follow automatically before returning an error to the caller.
	// A redirect from xolu indicates the tenant has moved; the proxy resolves
	// the new location and retries rather than returning 307 to the caller.
	// This shields callers from needing to understand xolu's redirect semantics.
	// Default: 3
	// Env: NOLU_PROXY_MAX_REDIRECTS
	MaxRedirects int `json:"max_redirects" env:"NOLU_PROXY_MAX_REDIRECTS"`

	// StripTenantFromPath controls whether the proxy rewrites the path when
	// forwarding. When true, /proxy/tenant/vendocorp/devices/42 is forwarded
	// as /api/v1/tenant/vendocorp/devices/42 to xolu. The tenant name is
	// resolved to a numeric tenant ID in the rewritten path.
	// Default: true
	// Env: NOLU_PROXY_STRIP_TENANT
	StripTenantFromPath bool `json:"strip_tenant_from_path" env:"NOLU_PROXY_STRIP_TENANT"`

	// LogLevel is the zerolog level for proxy-specific log output.
	// Default: "info"
	// Env: NOLU_PROXY_LOG_LEVEL
	LogLevel string `json:"log_level" env:"NOLU_PROXY_LOG_LEVEL"`

	// TrustForwardedFor controls whether the proxy passes through
	// X-Forwarded-For headers from upstream clients to xolu.
	// Default: true
	// Env: NOLU_PROXY_TRUST_FORWARDED_FOR
	TrustForwardedFor bool `json:"trust_forwarded_for" env:"NOLU_PROXY_TRUST_FORWARDED_FOR"`
}

// Default returns a Config populated with safe defaults for embedded mode.
// Sidecar mode should call Default() and then set ListenAddr and NoluURL.
func Default() Config {
	return Config{
		ListenAddr:          ":7071",
		MountPath:           "/proxy",
		CacheTTL:            30 * time.Second,
		CacheSize:           1024,
		DialTimeout:         5 * time.Second,
		ForwardTimeout:      30 * time.Second,
		MaxRedirects:        3,
		StripTenantFromPath: true,
		LogLevel:            "info",
		TrustForwardedFor:   true,
	}
}

// SidecarDefault returns a Config suitable for the standalone sidecar.
// The caller must set NoluURL before use.
func SidecarDefault() Config {
	c := Default()
	c.MountPath = "/"
	return c
}
