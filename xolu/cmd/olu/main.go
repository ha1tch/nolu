// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/rs/zerolog"
	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	"github.com/ha1tch/xolu/pkg/graph"
	"github.com/ha1tch/xolu/pkg/server"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/tenant"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/ha1tch/xolu/pkg/version"
)

func main() {
	// Handle version commands
	if len(os.Args) > 1 {
		arg := os.Args[1]
		switch arg {
		case "version", "--version", "-v", "-V":
			fmt.Println(version.Version)
			os.Exit(0)
		case "help", "--help", "-h":
			printUsage()
			os.Exit(0)
		}
	}

	// Setup logger
	logger := zerolog.New(os.Stdout).With().
		Timestamp().
		Logger().
		Output(zerolog.ConsoleWriter{Out: os.Stdout, TimeFormat: time.RFC3339})
	
	// Load configuration
	cfg := config.Default()
	config.LoadFromEnv(cfg)

	// Apply log level from config. zerolog.GlobalLevel filters all loggers.
	switch cfg.LogLevel {
	case "debug":
		zerolog.SetGlobalLevel(zerolog.DebugLevel)
	case "warn":
		zerolog.SetGlobalLevel(zerolog.WarnLevel)
	case "error":
		zerolog.SetGlobalLevel(zerolog.ErrorLevel)
	default:
		zerolog.SetGlobalLevel(zerolog.InfoLevel)
	}
	logger = logger.Level(zerolog.GlobalLevel())

	// Validate configuration
	if errs, warnings := cfg.Validate(); len(errs) > 0 || len(warnings) > 0 {
		for _, w := range warnings {
			logger.Warn().Str("config", w).Msg("Configuration warning")
		}
		if len(errs) > 0 {
			for _, e := range errs {
				logger.Error().Str("config", e).Msg("Configuration error")
			}
			logger.Fatal().Int("errors", len(errs)).Msg("Invalid configuration; exiting")
		}
	}
	
	// Print banner
	printBanner(cfg, logger)
	
	// Create directories
	if err := os.MkdirAll(cfg.BaseDir, 0755); err != nil {
		logger.Fatal().Err(err).Msg("Failed to create base directory")
	}
	if err := os.MkdirAll(cfg.SchemaDir, 0755); err != nil {
		logger.Fatal().Err(err).Msg("Failed to create schema directory")
	}
	
	// Initialize storage
	store, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:                      cfg.StorageType,
		DBPath:                    cfg.DBPath,
		BaseDir:                   cfg.BaseDir,
		Schema:                    cfg.Schema,
		FullTextEnabled:           cfg.FullTextEnabled,
		GraphEnabled:              cfg.GraphEnabled,
		SQLiteCacheSize:           cfg.SQLiteCacheSize,
		SQLiteBusyTimeout:         cfg.SQLiteBusyTimeout,
		SQLiteMaxOpenConns:        cfg.SQLiteMaxOpenConns,
		SQLiteMaxIdleConns:        cfg.SQLiteMaxIdleConns,
		SQLiteReadPoolSize:        cfg.SQLiteReadPoolSize,
		SQLiteContentionThreshold: cfg.SQLiteContentionThreshold,
	})
	if err != nil {
		logger.Fatal().Err(err).Msg("Failed to initialize storage")
	}
	defer store.Close()
	// Attach the application logger to the store if it supports it.
	if sqlStore, ok := store.(interface{ WithLogger(zerolog.Logger) *storage.SQLiteStore }); ok {
		sqlStore.WithLogger(logger)
	}
	
	// Log store info
	if infoProvider, ok := store.(storage.InfoProvider); ok {
		info := infoProvider.Info()
		logger.Info().
			Str("type", info.Type).
			Str("version", info.Version).
			Bool("supports_search", info.SupportsSearch).
			Bool("supports_batch", info.SupportsBatch).
			Bool("supports_transaction", info.SupportsTransaction).
			Msg("Storage initialized")
	}
	
	// Initialize cache
	var cacheInstance cache.Cache
	if cfg.CacheType == "redis" {
		redisCache, err := cache.NewRedisCache(
			cfg.RedisHost,
			cfg.RedisPort,
			time.Duration(cfg.CacheTTL)*time.Second,
			cfg.RedisPoolSize,
			cfg.RedisMinIdleConns,
		)
		if err != nil {
			logger.Warn().Err(err).Msg("Failed to connect to Redis, falling back to memory cache")
			cacheInstance = cache.NewShardedMemoryCache(cfg.CacheSize, time.Duration(cfg.CacheTTL)*time.Second, cfg.CacheShards)
		} else {
			cacheInstance = redisCache
			logger.Info().Msg("Using Redis cache")
		}
	} else {
		cacheInstance = cache.NewShardedMemoryCache(cfg.CacheSize, time.Duration(cfg.CacheTTL)*time.Second, cfg.CacheShards)
		logger.Info().Msg("Using in-memory cache")
	}
	defer cacheInstance.Close()
	
	// Initialize graph
	var graphInstance graph.Graph
	var persister *graph.AdaptivePersister
	if cfg.GraphEnabled && cfg.GraphMode == "flat" {
		graphInstance = graph.NewFlatGraphWithCycleDetection(cfg.GraphCycleDetection)
		if cfg.GraphCycleCheckLimit > 0 {
			graphInstance.(*graph.FlatGraph).SetCycleCheckLimit(cfg.GraphCycleCheckLimit)
		}

		graphFile := filepath.Join(cfg.BaseDir, cfg.GraphDataFile)

		// Determine whether the store provides its own durable graph edge
		// tables (GraphEdgeScanner). If it does (SQLite), the in-memory graph
		// is a cache rebuilt from those tables on every startup, and the JSON
		// file is neither read nor written — the persister would be pure noise.
		// If the store does not implement GraphEdgeScanner (jsonfile), the JSON
		// file is the only durable graph representation, so both the load and
		// the persister are required.
		_, storeHasEdgeTable := store.(storage.GraphEdgeScanner)

		if !storeHasEdgeTable {
			// JSON filestore path: load graph from file if it exists.
			if err := graphInstance.Load(graphFile); err != nil {
				logger.Warn().Err(err).Msg("Failed to load graph, starting with empty graph")
			} else {
				logger.Info().Msg("Loaded existing graph")
			}
		}

		// Load entities into graph (fast edge-table path for SQLite,
		// slow JSON-deserialise path for jsonfile).
		if err := loadEntitiesIntoGraph(cfg, store, graphInstance, logger); err != nil {
			// Partial hydration is worse than no hydration: an undefined subset of
			// edges would be in the in-memory graph, silently producing wrong query
			// results. Clear the graph so that the server starts with a known-empty
			// state. Operators can call POST /api/v1/graph/rebuild to repopulate
			// after the underlying cause (timeout, disk issue) is resolved.
			logger.Error().Err(err).Msg("Failed to load entities into graph — clearing in-memory graph to avoid partial state")
			if clearErr := graphInstance.Clear(); clearErr != nil {
				logger.Error().Err(clearErr).Msg("Failed to clear graph after hydration failure")
			}
		}

		if !storeHasEdgeTable {
			// JSON filestore path: start the adaptive persister so that
			// in-memory graph mutations are periodically flushed to disk.
			persister = graph.NewAdaptivePersister(graphInstance, graphFile, logger)
			persister.Start()
		} else {
			logger.Info().Msg("Graph persistence delegated to store edge tables; JSON persister not started")
		}

		logger.Info().Msg("Graph initialized")
	} else {
		logger.Info().Msg("Graph disabled")
	}
	
	// Initialize validator
	validator := validation.NewJSONSchemaValidator(cfg.SchemaDir)
	if err := validator.LoadAllSchemas(); err != nil {
		logger.Warn().Err(err).Msg("Failed to load schemas")
	}

	// Sync adapted tables: for every loaded schema, ensure an adapted
	// table exists. This handles schemas added to the directory while
	// the server was down. RegisterAdaptedEntity is idempotent — it
	// skips tables whose schema hash hasn't changed.
	if sqlStore, ok := store.(*storage.SQLiteStore); ok {
		syncCtx, syncCancel := context.WithTimeout(context.Background(), 30*time.Second)
		for _, entity := range validator.LoadedEntities() {
			raw, err := validator.GetSchema(entity)
			if err != nil {
				continue
			}
			if err := sqlStore.RegisterAdaptedEntity(syncCtx, entity, raw); err != nil {
				logger.Warn().Err(err).Str("entity", entity).Msg("Failed to register adapted table at startup")
			}
		}
		syncCancel()
	}
	
	// Create server
	srv := server.New(cfg, store, cacheInstance, graphInstance, persister, validator, logger)
	
	// Setup graceful shutdown
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-sigChan
		logger.Info().Msg("Shutting down gracefully...")

		// Give in-flight requests up to 15 seconds to complete
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()

		// Shut down HTTP server first (stops accepting new requests,
		// waits for in-flight requests to finish)
		if err := srv.Shutdown(ctx); err != nil {
			logger.Error().Err(err).Msg("HTTP server shutdown error")
		}

		// Stop persister (triggers final save)
		if persister != nil {
			persister.Stop()
		}

		// Stop rate limiter cleanup
		srv.Stop()
	}()

	// Start server (blocks until Shutdown is called or a fatal error occurs)
	logger.Info().Msg("Server ready to accept requests")
	if err := srv.Start(); err != nil && err != http.ErrServerClosed {
		logger.Fatal().Err(err).Msg("Server failed")
	}

	// After Shutdown, main returns and defers execute (store.Close, cache.Close)
	logger.Info().Msg("Server stopped")
}

func printBanner(cfg *config.Config, logger zerolog.Logger) {
	// Light blue color code
	lightBlue := "\033[1;36m"
	reset := "\033[0m"
	
	// Print ASCII art in light blue
	fmt.Print(lightBlue)
	fmt.Println("//////////////////////////////////////////////")
	fmt.Println("//..........................................//")
	fmt.Println("//..........................................//")
	fmt.Println("//....._,gggggg,_...........................//")
	fmt.Println("//...,d8P\"\"d8P\"Y8b,....,dPYb,...............//")
	fmt.Println("//..,d8'...Y8...\"8b,dP.IP'`Yb...............//")
	fmt.Println("//..d8'....`Ybaaad88P'.I8..8I...............//")
	fmt.Println("//..8P.......`\"\"\"\"Y8...I8..8'...............//")
	fmt.Println("//..8b............d8...I8.dP..gg......gg....//")
	fmt.Println("//..Y8,..........,8P...I8dP...I8......8I....//")
	fmt.Println("//..`Y8,........,8P'...I8P....I8,....,8I....//")
	fmt.Println("//...`Y8b,,__,,d8P'...,d8b,_.,d8b,..,d8b,...//")
	fmt.Println("//.....`\"Y8888P\"'.....8P'\"Y888P'\"Y88P\"`Y8...//")
	fmt.Println("//..........................................//")
	fmt.Println("//..........................................//")
	fmt.Println("//..........................................//")
	fmt.Println("//..........................................//")
	fmt.Println("//..........................................//")
	fmt.Println("//////////////////////////////////////////////")
	fmt.Print(reset)
	
	fmt.Println()
	fmt.Println("//////////////////////////// olu " + version.Version + " /////////////////////////////")
	fmt.Println("----------------------------------------------------------------------")
	fmt.Println("Server Configuration:")
	fmt.Printf("  Host: %s\n", cfg.Host)
	fmt.Printf("  Port: %d\n", cfg.Port)
	if cfg.MetricsPort > 0 {
		metricsHost := cfg.MetricsHost
		if metricsHost == "" {
			if cfg.Host != "" && cfg.Host != "0.0.0.0" && cfg.Host != "::" {
				metricsHost = cfg.Host
			} else {
				metricsHost = "0.0.0.0"
			}
		}
		fmt.Printf("  Metrics Port: %d (dedicated, %s)\n", cfg.MetricsPort, metricsHost)
	} else {
		fmt.Printf("  Metrics Port: %d (shared with API)\n", cfg.Port)
	}
	fmt.Printf("  Schema: %s\n", cfg.Schema)
	fmt.Println()
	fmt.Println("Graph Configuration:")
	if cfg.GraphEnabled {
		fmt.Printf("  Mode: Enabled (%s)\n", cfg.GraphMode)
		fmt.Printf("  Query TTL: %d seconds\n", cfg.GraphQueryTTL)
		fmt.Printf("  Cycle Detection: %s\n", cfg.GraphCycleDetection)
		if cfg.GraphCycleCheckLimit > 0 {
			fmt.Printf("  Cycle Check Limit: %d\n", cfg.GraphCycleCheckLimit)
		} else {
			fmt.Printf("  Cycle Check Limit: %d (default)\n", graph.DefaultCycleCheckLimit)
		}
	} else {
		fmt.Println("  Mode: Disabled")
	}
	fmt.Println()
	fmt.Println("Cache Configuration:")
	fmt.Printf("  Type: %s\n", cfg.CacheType)
	fmt.Printf("  TTL: %d seconds\n", cfg.CacheTTL)
	if cfg.CacheType == "redis" {
		fmt.Printf("  Redis: %s:%d\n", cfg.RedisHost, cfg.RedisPort)
	}
	fmt.Println()
	fmt.Println("Other Configuration:")
	fmt.Printf("  Full-text search: %v\n", cfg.FullTextEnabled)
	fmt.Printf("  Cascading delete: %v\n", cfg.CascadingDelete)
	fmt.Printf("  REF embed depth: %d\n", cfg.RefEmbedDepth)
	fmt.Printf("  Patch null handling: %s\n", cfg.PatchNullBehavior)
	fmt.Printf("  Max query depth: %d\n", cfg.MaxQueryDepth)
	fmt.Println("----------------------------------------------------------------------")
	fmt.Println()
}

func loadEntitiesIntoGraph(
	cfg *config.Config,
	store storage.Store,
	g graph.Graph,
	logger zerolog.Logger,
) error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Fast path: if the store can stream edges directly from its edge table,
	// use that instead of deserialising full entity JSON. This is O(edges)
	// rather than O(entities × JSON size).
	if scanner, ok := store.(storage.GraphEdgeScanner); ok {
		return loadEntitiesFromEdgeTable(ctx, scanner, g, logger)
	}

	// Slow path: deserialise all entities and extract REF fields in Go.
	// Used by the jsonfile store and any future backend that does not implement
	// GraphEdgeScanner.
	return loadEntitiesFromStore(ctx, cfg, store, g, logger)
}

// loadEntitiesFromEdgeTable is the fast hydration path for stores that
// implement GraphEdgeScanner. It reads one row per edge — five narrow columns
// — and calls AddNode + AddEdge directly, never allocating entity JSON.
//
// If the store also implements TenantIDLister, all registered tenants
// (including tenant 0) are hydrated uniformly. Tenants with no edges are
// silently skipped.
func loadEntitiesFromEdgeTable(
	ctx context.Context,
	scanner storage.GraphEdgeScanner,
	g graph.Graph,
	logger zerolog.Logger,
) error {
	lister, ok := scanner.(storage.TenantIDLister)
	if !ok {
		// Store does not enumerate tenants; fall back to hydrating tenant 0 only.
		count, err := scanTenantEdges(ctx, scanner, g, 0, logger)
		if err != nil {
			return err
		}
		if count > 0 {
			logger.Info().Int("edges", count).Uint16("tenant", 0).Msg("Loaded tenant graph from edge table")
		}
		if n := g.VodeCount(); n > 0 {
			vodes := g.GetNodesByType(graph.NodeTypeVode)
			sample := vodes
			if len(sample) > 10 {
				sample = sample[:10]
			}
			wev := logger.Warn().Int("vode_count", n).Strs("vode_sample", sample)
			if len(vodes) > 10 {
				wev = wev.Int("vode_remaining", len(vodes)-10)
			}
			wev.Msg("Graph hydration complete but vode nodes remain — REF targets not yet written to store")
		}
		return nil
	}

	tenantIDs, err := lister.GraphTenantIDs(ctx)
	if err != nil {
		// Non-fatal: log and continue with an empty graph.
		logger.Warn().Err(err).Msg("loadEntitiesFromEdgeTable: could not enumerate tenants; graph will be empty")
		return nil
	}
	for _, tid := range tenantIDs {
		n, err := scanTenantEdges(ctx, scanner, g, tid, logger)
		if err != nil {
			logger.Warn().Err(err).Uint16("tenant", tid).Msg("loadEntitiesFromEdgeTable: tenant hydration failed; skipping")
			continue
		}
		if n > 0 {
			logger.Info().Int("edges", n).Uint16("tenant", tid).Msg("Loaded tenant graph from edge table")
		}
	}
	if n := g.VodeCount(); n > 0 {
		vodes := g.GetNodesByType(graph.NodeTypeVode)
		sample := vodes
		if len(sample) > 10 {
			sample = sample[:10]
		}
		wev := logger.Warn().Int("vode_count", n).Strs("vode_sample", sample)
		if len(vodes) > 10 {
			wev = wev.Int("vode_remaining", len(vodes)-10)
		}
		wev.Msg("Graph hydration complete but vode nodes remain — REF targets not yet written to store")
	}
	return nil
}

// scanTenantEdges streams all edges for one tenant into the graph.
// Returns the number of edge rows processed.
func scanTenantEdges(
	ctx context.Context,
	scanner storage.GraphEdgeScanner,
	g graph.Graph,
	tid uint16,
	logger zerolog.Logger,
) (int, error) {
	count := 0
	err := scanner.ScanGraphEdges(ctx, tid, func(e storage.GraphEdge) error {
		if err := g.AddNode(tenant.NodeID(tid, e.SourceEntity, e.SourceID), e.SourceEntity); err != nil {
			logger.Warn().Err(err).
				Str("source", e.SourceEntity).Int("id", e.SourceID).
				Msg("scanTenantEdges: AddNode source failed")
		}
		if err := g.AddNode(tenant.NodeID(tid, e.TargetEntity, e.TargetID), e.TargetEntity); err != nil {
			logger.Warn().Err(err).
				Str("target", e.TargetEntity).Int("id", e.TargetID).
				Msg("scanTenantEdges: AddNode target failed")
		}
		if err := g.AddEdge(
			tenant.NodeID(tid, e.SourceEntity, e.SourceID),
			tenant.NodeID(tid, e.TargetEntity, e.TargetID),
			e.Relationship,
		); err != nil {
			logger.Warn().Err(err).
				Str("source", e.SourceEntity).Str("target", e.TargetEntity).
				Str("rel", e.Relationship).
				Msg("scanTenantEdges: AddEdge failed")
		}
		count++
		return nil
	})
	return count, err
}

// loadEntitiesFromStore is the slow hydration path: deserialises all entities
// and calls UpdateFromEntityForTenant to extract REF fields. Used by the
// jsonfile store and any backend that does not implement GraphEdgeScanner.
func loadEntitiesFromStore(
	ctx context.Context,
	cfg *config.Config,
	store storage.Store,
	g graph.Graph,
	logger zerolog.Logger,
) error {
	schemaPath := filepath.Join(cfg.BaseDir, cfg.Schema)

	// Always hydrate tenant 0 (the default unscoped namespace).
	count := loadTenantEntitiesFromStore(ctx, cfg, store, g, 0, schemaPath, logger)
	logger.Info().Int("count", count).Uint16("tenant", 0).Msg("Loaded tenant-0 entities into graph")

	// Hydrate any non-zero tenants whose data directories exist on disk.
	// The jsonfile store uses "tXXXX" subdirectories inside the schema path
	// (e.g. schema/t0001/users/1.json). We enumerate those directories and
	// parse the tenant ID from the name so each tenant's graph nodes carry
	// the correct XXXX@ prefix.
	top, err := os.ReadDir(schemaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	for _, entry := range top {
		if !entry.IsDir() {
			continue
		}
		name := entry.Name()
		// Match the "tXXXX" format produced by tenant.StorageDirSegment.
		if len(name) != 5 || name[0] != 't' {
			continue
		}
		parsed, err := strconv.ParseUint(name[1:], 16, 16)
		if err != nil || parsed == 0 {
			continue
		}
		tid := uint16(parsed)
		tenantSchemaPath := filepath.Join(schemaPath, name)
		n := loadTenantEntitiesFromStore(ctx, cfg, store, g, tid, tenantSchemaPath, logger)
		if n > 0 {
			logger.Info().Int("count", n).Uint16("tenant", tid).Msg("Loaded tenant entities into graph")
		}
	}

	return nil
}

// loadTenantEntitiesFromStore hydrates one tenant's graph nodes from the
// entity JSON files under schemaPath. It lists entity directories, reads
// every entity via the store, and calls UpdateFromEntityForTenant with the
// given tenant ID. Returns the number of entities successfully added.
func loadTenantEntitiesFromStore(
	ctx context.Context,
	cfg *config.Config,
	store storage.Store,
	g graph.Graph,
	tid uint16,
	schemaPath string,
	logger zerolog.Logger,
) int {
	entries, err := os.ReadDir(schemaPath)
	if err != nil {
		if !os.IsNotExist(err) {
			logger.Warn().Err(err).Str("path", schemaPath).Msg("loadTenantEntitiesFromStore: ReadDir failed")
		}
		return 0
	}

	// For non-zero tenants the store must be scoped to that tenant so that
	// List returns the right data directory. We construct a temporary scoped
	// store using the same base configuration.
	var scopedStore storage.Store
	if tid == 0 {
		scopedStore = store
	} else {
		baseCfg := store.Config()
		scopedStore, err = storage.NewStoreFromConfig(storage.StoreConfig{
			Type:     baseCfg.Type,
			BaseDir:  baseCfg.BaseDir,
			Schema:   baseCfg.Schema,
			TenantID: tid,
		})
		if err != nil {
			logger.Warn().Err(err).Uint16("tenant", tid).Msg("loadTenantEntitiesFromStore: could not create scoped store; skipping tenant")
			return 0
		}
		defer scopedStore.Close()
	}

	count := 0
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		entityName := entry.Name()
		entities, err := scopedStore.List(ctx, entityName)
		if err != nil {
			logger.Warn().Err(err).Str("entity", entityName).Uint16("tenant", tid).Msg("Failed to list entities")
			continue
		}
		for _, data := range entities {
			id, ok := data["id"].(float64)
			if !ok {
				if idInt, ok := data["id"].(int); ok {
					id = float64(idInt)
				} else {
					continue
				}
			}
			if err := g.UpdateFromEntityForTenant(tid, entityName, int(id), data); err != nil {
				logger.Warn().Err(err).
					Str("entity", entityName).
					Int("id", int(id)).
					Uint16("tenant", tid).
					Msg("Failed to add entity to graph")
			} else {
				count++
			}
		}
	}
	return count
}

func printUsage() {
	fmt.Printf("olu %s - JSON document store with graph layer\n\n", version.Version)
	fmt.Println("Usage:")
	fmt.Println("  olu                Start the server")
	fmt.Println("  olu version        Show version information")
	fmt.Println("  olu help           Show this help message")
	fmt.Println()
	fmt.Println("Options:")
	fmt.Println("  --version, -v      Show version information")
	fmt.Println("  --help, -h         Show this help message")
	fmt.Println()
	fmt.Println("Configuration:")
	fmt.Println("  Configuration is done via environment variables.")
	fmt.Println("  See documentation for available options.")
	fmt.Println()
	fmt.Println("Examples:")
	fmt.Println("  olu                           # Start with defaults")
	fmt.Println("  OLU_PORT=8080 olu             # Start on port 8080")
	fmt.Println("  OLU_GRAPH_MODE=disabled olu   # Start without graph")
}
