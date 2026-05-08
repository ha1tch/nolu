// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

// nolu is a federated entity registry that enables xolu instances to
// coordinate across organisational boundaries without centralising data.
//
// nolu owns global entity identities (GlobalIDs) and the routing table that
// maps them to their current xolu home. It witnesses ownership transfers,
// manages subscriptions, and publishes events to the bus. It does not own,
// store, or replicate entity data — that stays in xolu.
//
// Think of nolu as the clearinghouse: both parties trust its record; neither
// party needs to trust the other directly.
//
// Usage:
//
//	nolu [flags]
//
// Flags:
//
//	-host    Registry hostname (default: localhost)
//	-listen  HTTP listen address (default: :7070)
//	-bus     Event bus type: memory|nats (default: memory)
//	-nats    NATS server URL (required when -bus=nats)
//	-log     Log level: debug|info|warn|error (default: info)
package main

import (
	"flag"
	"os"
	"os/signal"
	"syscall"

	"context"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/ha1tch/nolu/pkg/config"
	"fmt"
	"net/http"

	"github.com/ha1tch/nolu/pkg/events"
	"github.com/ha1tch/nolu/pkg/hotswap"
	"github.com/ha1tch/nolu/pkg/identity"
	"github.com/ha1tch/nolu/pkg/proxy"
	"github.com/ha1tch/nolu/pkg/registry"
	"github.com/ha1tch/nolu/pkg/server"
	"github.com/ha1tch/nolu/pkg/transfer"
	"github.com/ha1tch/nolu/pkg/version"
)

func main() {
	ctx := context.Background()
	cfg := config.Default()

	flag.StringVar(&cfg.RegistryHost, "host", cfg.RegistryHost, "Registry hostname used in minted GlobalIDs")
	flag.StringVar(&cfg.ListenAddr, "listen", cfg.ListenAddr, "HTTP API listen address")
	flag.StringVar(&cfg.BusType, "bus", cfg.BusType, "Event bus type: memory|nats")
	flag.StringVar(&cfg.NATSUrl, "nats", cfg.NATSUrl, "NATS server URL (required when -bus=nats)")
	flag.StringVar(&cfg.StorageType, "storage", cfg.StorageType, "Storage backend: memory|xolu")
	flag.StringVar(&cfg.XoluURL, "xolu", cfg.XoluURL, "xolu instance URL (required when -storage=xolu)")
	flag.StringVar(&cfg.LogLevel, "log", cfg.LogLevel, "Log level: debug|info|warn|error")
	flag.Parse()

	// Logging setup.
	level, err := zerolog.ParseLevel(cfg.LogLevel)
	if err != nil {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})

	log.Info().
		Str("version", version.Version).
		Str("host", cfg.RegistryHost).
		Str("listen", cfg.ListenAddr).
		Str("bus", cfg.BusType).
		Str("storage", cfg.StorageType).
		Msg("nolu starting")

	// TODO: wire up registry, router, negotiator, bus, and HTTP server.
	// The interfaces are defined; the concrete implementations are next.
	//
	// Wiring order:
	//   1. Bus (MemoryBus or NATSBus)
	//   2. Registry (MemoryRegistry or XoluRegistry)
	//   3. Router (wired to Registry and Bus)
	//   4. Negotiator (wired to Registry and Router)
	//   5. HTTP server (wired to all of the above)

	// ── Registry setup ──────────────────────────────────────────────────────
	var bus events.Bus
	switch cfg.BusType {
	case "nats":
		nb, err := events.NewNATSBus(events.NATSBusConfig{
			URL:            cfg.NATSUrl,
			StreamName:     cfg.NATSStreamName,
			ConnectTimeout: 15 * time.Second,
		})
		if err != nil {
			log.Fatal().Err(err).Str("url", cfg.NATSUrl).Msg("nats: connect failed")
		}
		defer nb.Close()
		bus = nb
		log.Info().Str("url", cfg.NATSUrl).Msg("nats: connected")
	default:
		bus = events.NewMemoryBus()
		log.Info().Msg("using in-process memory bus")
	}

	var reg registry.Registry
	switch cfg.StorageType {
	case "xolu":
		if cfg.XoluURL == "" {
			log.Fatal().Msg("xolu storage requires -xolu flag")
		}
		xr, err := registry.NewXoluRegistry(ctx, cfg.XoluURL, cfg.RegistryHost, bus)
		if err != nil {
			log.Fatal().Err(err).Str("url", cfg.XoluURL).Msg("xolu_registry: init failed")
		}
		reg = xr
		log.Info().Str("url", cfg.XoluURL).Msg("xolu_registry: ready")
	default:
		reg = registry.NewMemoryRegistry(cfg.RegistryHost, bus)
		log.Warn().Msg("using in-process memory registry — data will be lost on restart")
	}
	_ = reg // HTTP server will use this; wiring pending

	// ── Transfer negotiator ─────────────────────────────────────────────────
	neg := transfer.NewMemoryNegotiator(reg)

	// ── Tenant directory ─────────────────────────────────────────────────────
	dir := registry.NewTenantDirectory(reg, 30*time.Second)
	if err := dir.Start(ctx); err != nil {
		log.Fatal().Err(err).Msg("tenant directory: start failed")
	}
	log.Info().Msg("tenant directory: started")

	// ── Hotswap manager ─────────────────────────────────────────────────────
	var hs hotswap.Manager
	if cfg.HotswapEnabled {
		// Use xolu-backed manager when storage=xolu (or hotswap_storage=xolu),
		// otherwise fall back to in-memory.
		hotswapStorage := cfg.HotswapStorageType
		if hotswapStorage == "" {
			hotswapStorage = cfg.StorageType
		}
		switch hotswapStorage {
		case "xolu":
			if cfg.XoluURL == "" {
				log.Fatal().Msg("xolu hotswap storage requires -xolu flag")
			}
			xhs, err := hotswap.NewXoluHotswapManager(ctx, cfg.XoluURL, reg, bus, dir)
			if err != nil {
				log.Fatal().Err(err).Msg("hotswap: xolu manager init failed")
			}
			hs = xhs
			log.Info().Str("storage", "xolu").Msg("hotswap: durable manager ready")
		default:
			hs = hotswap.NewMemoryManager(reg, bus, dir)
			log.Warn().Msg("hotswap: using in-memory manager — state lost on restart")
		}
	}



	// ── Proxy ────────────────────────────────────────────────────────────────
	proxyCfg := proxy.Default()
	var prx *proxy.ReverseProxy
	if cfg.NoluURL != "" {
		// Sidecar mode: resolve via HTTP locate calls to a nolu instance.
		resolver := proxy.NewHTTPResolver(cfg.NoluURL, proxyCfg)
		prx = proxy.New(resolver, proxyCfg)
	} else {
		// Embedded mode: resolve via in-process tenant directory.
		locator := proxy.RegistryLocatorFunc(func(lctx context.Context, tenantName string) (identity.LocalRef, error) {
			entry, ok := dir.Locate(tenantName)
			if !ok {
				return identity.LocalRef{}, fmt.Errorf("tenant %q not found", tenantName)
			}
			return identity.LocalRef{
				InstanceURL: entry.InstanceURL,
				TenantID:    entry.TenantID,
				TenantName:  entry.TenantName,
			}, nil
		})
		resolver := proxy.NewRegistryResolver(locator, proxyCfg)
		prx = proxy.New(resolver, proxyCfg)
	}

	// ── HTTP server ──────────────────────────────────────────────────────────
	srv := server.New(reg, neg, hs, prx, dir, cfg.RegistryHost)
	httpSrv := &http.Server{
		Addr:         cfg.ListenAddr,
		Handler:      srv,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
	}

	go func() {
		log.Info().Str("addr", cfg.ListenAddr).Msg("nolu listening")
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("nolu: server error")
		}
	}()

	// Wait for shutdown signal.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Info().Msg("nolu shutting down")
	shutCtx, shutCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer shutCancel()
	if err := httpSrv.Shutdown(shutCtx); err != nil {
		log.Error().Err(err).Msg("nolu: shutdown error")
	}
	log.Info().Msg("nolu stopped")
}
