// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ReverseProxy is the core proxy handler. It:
//
//  1. Parses the tenant name from the incoming request path.
//  2. Resolves the tenant's current xolu location via the Resolver.
//  3. Rewrites the request URL to target the correct xolu instance.
//  4. Forwards the request and streams the response back.
//  5. On 307 from xolu, invalidates the cache and retries (up to MaxRedirects).
//  6. On successful response, passes it through unmodified.
//
// Path convention (embedded mode, MountPath="/proxy"):
//
//	Incoming:  GET /proxy/tenant/vendocorp/devices/42
//	Forwarded: GET http://xolu-hub-b:9091/api/v1/tenant/1/devices/42
//
// Path convention (sidecar mode, MountPath="/"):
//
//	Incoming:  GET /tenant/vendocorp/devices/42
//	Forwarded: GET http://xolu-hub-b:9091/api/v1/tenant/1/devices/42
//
// The nolu-specific path prefix (/proxy/tenant/{name}) is always rewritten
// to the xolu-native path (/api/v1/tenant/{id}) before forwarding.
type ReverseProxy struct {
	resolver   Resolver
	cfg        Config
	transport  http.RoundTripper
	mountPath  string // normalised: no trailing slash
}

// New creates a ReverseProxy with the given Resolver and Config.
func New(resolver Resolver, cfg Config) *ReverseProxy {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout:   cfg.DialTimeout,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	mount := strings.TrimRight(cfg.MountPath, "/")
	if mount == "" {
		mount = "/"
	}

	return &ReverseProxy{
		resolver:  resolver,
		cfg:       cfg,
		transport: transport,
		mountPath: mount,
	}
}

// Handler returns an http.Handler that can be mounted at cfg.MountPath.
// In embedded mode: mux.Handle("/proxy/", proxy.Handler())
// In sidecar mode:  http.Handle("/", proxy.Handler())
func (p *ReverseProxy) Handler() http.Handler {
	return http.HandlerFunc(p.ServeHTTP)
}

// ServeHTTP implements http.Handler.
func (p *ReverseProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	logger := log.Ctx(r.Context()).With().
		Str("proxy_method", r.Method).
		Str("proxy_path", r.URL.Path).
		Logger()

	tenantName, xoluPath, err := p.parsePath(r.URL.Path)
	if err != nil {
		logger.Debug().Err(err).Msg("proxy: bad path")
		http.Error(w, fmt.Sprintf("proxy: %v", err), http.StatusBadRequest)
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), p.cfg.ForwardTimeout)
	defer cancel()

	if err := p.forward(ctx, w, r, tenantName, xoluPath, 0, logger); err != nil {
		if ctx.Err() != nil {
			http.Error(w, "proxy: upstream timeout", http.StatusGatewayTimeout)
			return
		}
		logger.Error().Err(err).Msg("proxy: forward failed")
		http.Error(w, fmt.Sprintf("proxy: %v", err), http.StatusBadGateway)
	}
}

// forward resolves the tenant, builds the target URL, and performs the
// upstream HTTP request. On 307 it invalidates the cache and recurses
// (up to MaxRedirects times).
func (p *ReverseProxy) forward(
	ctx context.Context,
	w http.ResponseWriter,
	r *http.Request,
	tenantName, xoluPath string,
	redirectCount int,
	logger zerolog.Logger,
) error {
	if redirectCount > p.cfg.MaxRedirects {
		return fmt.Errorf("exceeded max redirects (%d) resolving tenant %q",
			p.cfg.MaxRedirects, tenantName)
	}

	loc, err := p.resolver.Locate(ctx, tenantName)
	if err != nil {
		return err
	}

	// Build the target URL.
	target, err := url.Parse(loc.InstanceURL)
	if err != nil {
		return fmt.Errorf("invalid instance URL %q: %w", loc.InstanceURL, err)
	}
	target.Path = loc.XoluPath() + xoluPath
	target.RawQuery = r.URL.RawQuery

	logger.Debug().
		Str("tenant", tenantName).
		Str("target", target.String()).
		Int("redirect_count", redirectCount).
		Msg("proxy: forwarding")

	// Build the outgoing request.
	outReq, err := http.NewRequestWithContext(ctx, r.Method, target.String(), r.Body)
	if err != nil {
		return fmt.Errorf("build upstream request: %w", err)
	}
	copyHeaders(outReq.Header, r.Header)
	if p.cfg.TrustForwardedFor {
		appendForwardedFor(outReq, r)
	}
	outReq.Header.Set("X-Nolu-Proxy", "1")
	outReq.Header.Set("X-Nolu-Tenant", tenantName)

	// Execute the upstream request.
	resp, err := p.transport.RoundTrip(outReq)
	if err != nil {
		return fmt.Errorf("upstream %s %s: %w", r.Method, target, err)
	}
	defer resp.Body.Close()

	// Handle 307: xolu is telling us the tenant has moved.
	if resp.StatusCode == http.StatusTemporaryRedirect {
		location := resp.Header.Get("Location")
		logger.Info().
			Str("tenant", tenantName).
			Str("redirect_to", location).
			Int("redirect_count", redirectCount).
			Msg("proxy: tenant moved (307), invalidating cache")

		p.resolver.Invalidate(tenantName)

		// If xolu gave us a Location header pointing back to nolu's locate
		// endpoint, honour it. Otherwise just re-resolve and retry.
		return p.forward(ctx, w, r, tenantName, xoluPath, redirectCount+1, logger)
	}

	// Stream the response back to the caller.
	copyHeaders(w.Header(), resp.Header)
	// Remove hop-by-hop headers that should not be forwarded.
	for _, h := range hopByHopHeaders {
		w.Header().Del(h)
	}
	w.WriteHeader(resp.StatusCode)
	if _, err := io.Copy(w, resp.Body); err != nil {
		logger.Debug().Err(err).Msg("proxy: error streaming response body")
	}

	logger.Debug().
		Str("tenant", tenantName).
		Int("status", resp.StatusCode).
		Msg("proxy: response forwarded")

	return nil
}

// parsePath extracts the tenant name and the remaining xolu path from the
// incoming request path.
//
// Expected path structure (after mount prefix is stripped):
//
//	/tenant/{name}[/rest...]
//
// Returns tenantName and xoluPath (the /rest... part, including leading slash,
// or "/" if there is no rest).
func (p *ReverseProxy) parsePath(rawPath string) (tenantName, xoluPath string, err error) {
	// Strip mount path prefix.
	path := rawPath
	if p.mountPath != "/" {
		if !strings.HasPrefix(path, p.mountPath) {
			return "", "", fmt.Errorf("path %q does not start with mount %q", rawPath, p.mountPath)
		}
		path = path[len(p.mountPath):]
	}

	// Expect: /tenant/{name}[/...]
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 3)

	if len(parts) < 2 || parts[0] != "tenant" {
		return "", "", fmt.Errorf("expected /tenant/{name}/..., got %q", rawPath)
	}

	tenantName = parts[1]
	if tenantName == "" {
		return "", "", fmt.Errorf("tenant name is empty in path %q", rawPath)
	}

	if len(parts) == 3 && parts[2] != "" {
		xoluPath = "/" + parts[2]
	} else {
		xoluPath = "/"
	}

	return tenantName, xoluPath, nil
}

// ── HTTP helpers ──────────────────────────────────────────────────────────────

// hopByHopHeaders are headers that should not be forwarded between hops.
var hopByHopHeaders = []string{
	"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
	"TE", "Trailer", "Transfer-Encoding", "Upgrade",
}

func copyHeaders(dst, src http.Header) {
	for k, vs := range src {
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func appendForwardedFor(outReq *http.Request, inReq *http.Request) {
	clientIP, _, err := net.SplitHostPort(inReq.RemoteAddr)
	if err != nil {
		clientIP = inReq.RemoteAddr
	}
	if prior := outReq.Header.Get("X-Forwarded-For"); prior != "" {
		outReq.Header.Set("X-Forwarded-For", prior+", "+clientIP)
	} else {
		outReq.Header.Set("X-Forwarded-For", clientIP)
	}
}
