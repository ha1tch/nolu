// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// nolu-proxy is the standalone tenant-aware reverse proxy sidecar for xolu.
//
// It resolves tenant names to their current xolu instance by calling nolu's
// /tenants/{name}/locate endpoint, then forwards requests transparently.
// 307 redirects from xolu (indicating a tenant has moved) are handled
// automatically — the caller never sees them.
//
// Deploy alongside nolu when you want the proxy to run independently of the
// registry, for example when scaling the proxy layer separately or when the
// proxy must be available even if nolu is temporarily unreachable (using
// cached locations).
//
// Usage:
//
//	nolu-proxy [flags]
//
// Flags:
//
//	-nolu     nolu registry URL (required)  e.g. http://nolu:7070
//	-listen   proxy listen address          default: :7071
//	-ttl      tenant location cache TTL     default: 30s
//	-log      log level                     default: info
//
// Environment variables (override flags):
//
//	NOLU_PROXY_NOLU_URL   nolu registry URL
//	NOLU_PROXY_LISTEN     listen address
//	NOLU_PROXY_CACHE_TTL  cache TTL (Go duration string, e.g. "30s")
//	NOLU_PROXY_LOG_LEVEL  log level
//
// Path structure:
//
//	Incoming:  GET /tenant/vendocorp/devices/42[?query]
//	Forwarded: GET http://xolu-hub-b:9091/api/v1/tenant/1/devices/42[?query]
//
// The proxy is stateless — it can be restarted without data loss. The
// cache warms up automatically as tenants are first accessed.
//
// Health endpoint:
//
//	GET /health  →  200 {"status":"ok","version":"...","cache_size":N}
package main

import (
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ha1tch/nolu/pkg/proxy"
	"github.com/ha1tch/nolu/pkg/version"
)

func main() {
	cfg := proxy.SidecarDefault()

	flag.StringVar(&cfg.NoluURL,   "nolu",   envOr("NOLU_PROXY_NOLU_URL", ""),        "nolu registry URL (required)")
	flag.StringVar(&cfg.ListenAddr,"listen", envOr("NOLU_PROXY_LISTEN", cfg.ListenAddr), "proxy listen address")
	flag.StringVar(&cfg.LogLevel,  "log",    envOr("NOLU_PROXY_LOG_LEVEL", cfg.LogLevel), "log level")

	var cacheTTLStr string
	flag.StringVar(&cacheTTLStr, "ttl", envOr("NOLU_PROXY_CACHE_TTL", "30s"), "tenant location cache TTL")
	flag.Parse()

	if ttl, err := time.ParseDuration(cacheTTLStr); err == nil {
		cfg.CacheTTL = ttl
	}

	// Logging.
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})

	if cfg.NoluURL == "" {
		log.Fatal().Msg("nolu-proxy: -nolu flag is required (nolu registry URL)")
	}

	log.Info().
		Str("version", version.Version).
		Str("listen", cfg.ListenAddr).
		Str("nolu", cfg.NoluURL).
		Dur("cache_ttl", cfg.CacheTTL).
		Msg("nolu-proxy starting")

	// Build resolver and proxy.
	resolver := proxy.NewHTTPResolver(cfg.NoluURL, cfg)
	p := proxy.New(resolver, cfg)

	// Build mux.
	mux := http.NewServeMux()

	// Health endpoint.
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"status":  "ok",
			"version": version.Version,
			"mode":    "sidecar",
		})
	})

	// All other requests go to the proxy.
	// In sidecar mode the mount path is "/" so we match everything.
	mux.Handle("/", p.Handler())

	srv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      mux,
		ReadTimeout:  cfg.ForwardTimeout + 5*time.Second,
		WriteTimeout: cfg.ForwardTimeout + 5*time.Second,
		IdleTimeout:  120 * time.Second,
	}

	// Start.
	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("nolu-proxy listening")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("nolu-proxy: server error")
		}
	}()

	// Graceful shutdown.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("nolu-proxy shutting down")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error().Err(err).Msg("nolu-proxy: shutdown error")
	}
	log.Info().Msg("nolu-proxy stopped")
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
