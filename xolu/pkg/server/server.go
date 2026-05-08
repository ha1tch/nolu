// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/ha1tch/xolu/pkg/cache"
	"github.com/ha1tch/xolu/pkg/config"
	oluerr "github.com/ha1tch/xolu/pkg/errors"
	"github.com/ha1tch/xolu/pkg/graph"
	oluMiddleware "github.com/ha1tch/xolu/pkg/middleware"
	"github.com/ha1tch/xolu/pkg/models"
	"github.com/ha1tch/xolu/pkg/oql"
	"github.com/ha1tch/xolu/pkg/storage"
	"github.com/ha1tch/xolu/pkg/sulpher"
	"github.com/ha1tch/xolu/pkg/tenant"
	"github.com/ha1tch/xolu/pkg/timeseries"
	"github.com/ha1tch/xolu/pkg/validation"
	"github.com/ha1tch/xolu/pkg/version"
	"github.com/rs/zerolog"
)

// Context key type for tenant isolation
type contextKey string

const (
	tenantContextKey contextKey = "tenant_id"
	storeContextKey  contextKey = "tenant_store"
)

// Server represents the HTTP server
type Server struct {
	config         *config.Config
	storage        storage.Store
	cache          cache.Cache
	graph          graph.Graph
	persister      *graph.AdaptivePersister
	validator      validation.Validator
	sulpherJobs       *sulpher.JobManager
	tenantSulpherJobs sync.Map // uint16 -> *sulpher.JobManager; lazily initialised
	oqlJobs        *oql.JobManager
	rateLimiter    *oluMiddleware.RateLimiter
	metrics        *oluMiddleware.Metrics
	logger         zerolog.Logger
	router         *chi.Mux
	tenantRegistry *tenant.Registry
	tsManager      *timeseries.DefaultManager // nil when timeseries disabled
	tsRetention    *timeseries.RetentionWorker          // nil when retention disabled
	httpServer     *http.Server
	metricsServer  *http.Server // non-nil only when config.MetricsPort > 0
	tenantStores   sync.Map // tenantID (uint16) -> storage.Store; avoids per-request sql.Open
	ready          int32    // atomic: 0 = not ready, 1 = ready; set when Start() is called
}

// storeForTenant returns a Store scoped to the given tenant ID.
//
// Each tenant store maintains its own connection pools against the shared
// SQLite database: one writer (MaxOpenConns=1) and one reader pool
// (MaxOpenConns=NumCPU by default). With N active tenants, total connections
// are approximately N×(1+NumCPU). SQLite's WAL mode handles concurrent
// readers well, but operators should be aware of the multiplication when
// sizing file descriptor limits (ulimit -n) for high tenant counts.
//
// Stores are created lazily on first access and cached in a sync.Map.
// The LoadOrStore pattern handles concurrent creation races safely.
// For tenant 0, returns the default unscoped store.
// For non-zero tenants, returns a cached store instance that shares

// TenantRegistry returns the server's tenant registry. This is primarily
// useful for pre-registering tenants in strict mode before starting the
// server.
func (s *Server) TenantRegistry() *tenant.Registry {
	return s.tenantRegistry
}
// the same underlying database but scopes all queries. Stores are
// created once per tenant and reused across requests, avoiding the
// overhead of sql.Open on every request.
// sulpherJobsForTenant returns a Sulpher JobManager scoped to a specific tenant.
// Managers are lazily created and reused across requests for the same tenant.
// Returns nil if graph or Sulpher is not enabled.
func (s *Server) sulpherJobsForTenant(tenantID uint16) *sulpher.JobManager {
	if s.sulpherJobs == nil {
		return nil
	}
	if v, ok := s.tenantSulpherJobs.Load(tenantID); ok {
		return v.(*sulpher.JobManager)
	}
	prefix := tenant.GraphNodePrefix(tenantID)
	tenantStore, err := s.storeForTenant(tenantID)
	if err != nil {
		s.logger.Warn().Err(err).Uint16("tenant", tenantID).
			Msg("sulpherJobsForTenant: could not get tenant store; property conditions will not hydrate")
	}
	executor := sulpher.NewExecutorForTenant(s.graph, s.config.MaxQueryDepth, prefix).WithLogger(s.logger)
	if tenantStore != nil {
		executor = executor.WithStore(tenantStore)
	}
	cfg := s.config
	jm := sulpher.NewJobManager(executor, time.Duration(cfg.GraphQueryTTL)*time.Second)
	jm.SetLimits(sulpher.GraphLimits{
		MaxVisitedNodes: cfg.GraphMaxVisitedNodes,
		MaxResults:      cfg.GraphMaxResults,
	})
	if cfg.QueryTimeout > 0 {
		jm.SetQueryTimeout(time.Duration(cfg.QueryTimeout) * time.Second)
	}
	actual, _ := s.tenantSulpherJobs.LoadOrStore(tenantID, jm)
	return actual.(*sulpher.JobManager)
}

func (s *Server) storeForTenant(tenantID uint16) (storage.Store, error) {
	if tenantID == 0 {
		return s.storage, nil
	}

	// Fast path: check cache
	if cached, ok := s.tenantStores.Load(tenantID); ok {
		return cached.(storage.Store), nil
	}

	// Slow path: create and cache
	baseCfg := s.storage.Config()
	store, err := storage.NewStoreFromConfig(storage.StoreConfig{
		Type:                      baseCfg.Type,
		DBPath:                    baseCfg.DBPath,
		BaseDir:                   baseCfg.BaseDir,
		Schema:                    baseCfg.Schema,
		FullTextEnabled:           baseCfg.FullTextEnabled,
		GraphEnabled:              baseCfg.GraphEnabled,
		TenantID:                  tenantID,
		SQLiteCacheSize:           baseCfg.SQLiteCacheSize,
		SQLiteBusyTimeout:         baseCfg.SQLiteBusyTimeout,
		SQLiteMaxOpenConns:        baseCfg.SQLiteMaxOpenConns,
		SQLiteMaxIdleConns:        baseCfg.SQLiteMaxIdleConns,
		SQLiteReadPoolSize:        baseCfg.SQLiteReadPoolSize,
		SQLiteContentionThreshold: baseCfg.SQLiteContentionThreshold,
	})
	if err != nil {
		return nil, err
	}
	if sqlStore, ok := store.(*storage.SQLiteStore); ok {
		sqlStore.WithLogger(s.logger)
	}

	// LoadOrStore handles the race where two requests create the same tenant store
	// concurrently: one wins, the other's store gets discarded (and closed).
	actual, loaded := s.tenantStores.LoadOrStore(tenantID, store)
	if loaded {
		// Another goroutine won the race — close our duplicate and use theirs.
		store.Close()
		return actual.(storage.Store), nil
	}
	return store, nil
}

// getStore returns the tenant-scoped store from context, or the default
// unscoped store if no tenant context is present. Handlers should use
// this instead of s.storage directly.
func (s *Server) getStore(ctx context.Context) storage.Store {
	if v := ctx.Value(storeContextKey); v != nil {
		return v.(storage.Store)
	}
	return s.storage
}

// getTenantIDNumeric returns the numeric tenant ID from context, or 0.
func getTenantIDNumeric(ctx context.Context) uint16 {
	if v := ctx.Value(storeContextKey); v != nil {
		return v.(storage.Store).Config().TenantID
	}
	return 0
}

// tenantMiddleware resolves the tenant name from the URL, looks it up in the
// registry, constructs a scoped store, and injects both into the context.
//
// Resolution order:
//  1. Look up the URL parameter as a tenant name in the registry.
//  2. If not found, try parsing it as a numeric tenant ID (uint16) and
//     resolve the registered name via reverse lookup. This allows
//     /tenant/1 as an alias for /tenant/acme when acme has ID 1.
//  3. In path mode only, if neither lookup succeeds the name is
//     auto-registered as a new tenant (existing behaviour).
func (s *Server) tenantMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantName := chi.URLParam(r, "tenant_id")
		if tenantName == "" {
			s.writeError(w, http.StatusBadRequest, oluerr.ErrMissingParam, "tenant_id required")
			return
		}

		var tid uint16
		if s.config.TenantMode == "strict" {
			// Strict mode: tenant must be pre-registered.
			var ok bool
			tid, ok = s.tenantRegistry.Lookup(tenantName)
			if !ok {
				// Numeric fallback: try parsing as a tenant ID.
				tid, tenantName, ok = s.resolveNumericTenant(tenantName)
				if !ok {
					s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound,
						fmt.Sprintf("Unknown tenant: %s", chi.URLParam(r, "tenant_id")))
					return
				}
			}
		} else {
			// Non-strict (path) mode: check registry first, then
			// numeric fallback, then optionally auto-register.
			var ok bool
			tid, ok = s.tenantRegistry.Lookup(tenantName)
			if !ok {
				if numTid, numName, numOK := s.resolveNumericTenant(tenantName); numOK {
					tid, tenantName = numTid, numName
				} else if s.config.TenantAutoRegister {
					// Auto-register on first access (only when enabled).
					var err error
					tid, err = s.tenantRegistry.GetOrRegister(r.Context(), tenantName)
					if err != nil {
						s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidEntity,
							fmt.Sprintf("Invalid tenant: %s", tenantName))
						return
					}
				} else {
					s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound,
						fmt.Sprintf("Unknown tenant: %s (auto-registration disabled)", tenantName))
					return
				}
			}
		}

		store, err := s.storeForTenant(tid)
		if err != nil {
			s.logger.Error().Err(err).Str("tenant", tenantName).Msg("Failed to create tenant store")
			s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, "Failed to initialise tenant context")
			return
		}

		ctx := context.WithValue(r.Context(), tenantContextKey, tenantName)
		ctx = context.WithValue(ctx, storeContextKey, store)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// resolveNumericTenant attempts to parse raw as a decimal uint16 and look up
// the corresponding tenant name in the registry. Returns the tenant ID,
// resolved name, and true on success. Returns zero values and false if raw
// is not numeric, out of range, zero, or not registered.
func (s *Server) resolveNumericTenant(raw string) (uint16, string, bool) {
	n, err := strconv.ParseUint(raw, 10, 16)
	if err != nil || n == 0 {
		return 0, "", false
	}
	id := uint16(n)
	name, ok := s.tenantRegistry.Name(id)
	if !ok {
		return 0, "", false
	}
	return id, name, ok
}

// tenantStrictMiddleware enforces tenant context in strict mode.
// It rejects requests to entity routes that don't have tenant context.
// Used when TenantMode is "strict".
func (s *Server) tenantStrictMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check if this is an entity operation path (not system paths)
		path := r.URL.Path
		
		// Allow system endpoints
		if path == "/health" || path == "/ready" || path == "/version" || path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}
		
		// Allow schema endpoints (tenant-independent)
		if strings.HasPrefix(path, "/api/v1/schema/") || path == "/api/v1/schema" {
			next.ServeHTTP(w, r)
			return
		}

		// Allow tenant-scoped routes (they have their own tenant extraction)
		if strings.Contains(path, "/tenant/") {
			next.ServeHTTP(w, r)
			return
		}
		
		// Everything else under /api/v1/ requires tenant context in strict mode.
		// Non-tenant OQL, search, export, and graph routes are not registered
		// in strict mode (see setupRoutes), so they 404 naturally. But if
		// an entity happens to be named "search" or "export", the wildcard
		// /{entity} route would match — the middleware blocks it here.
		if strings.HasPrefix(path, "/api/v1/") {
			s.writeError(w, http.StatusForbidden, oluerr.ErrTenantRequired,
				"Tenant context required. Use /api/v1/tenant/{tenant_id}/... routes")
			return
		}
		
		next.ServeHTTP(w, r)
	})
}


// corsMiddleware returns a handler that sets CORS headers for the given origins.
// It handles preflight OPTIONS requests and adds appropriate headers to all responses.
//
// SECURITY NOTE: This middleware sets Access-Control-Allow-Credentials: true,
// which means cookies and HTTP auth headers are forwarded to the API by
// browsers. When combined with a wildcard origin ("*"), this effectively
// allows credentialed requests from any domain. This is acceptable when
// authentication uses API keys or Bearer tokens (which are not sent
// automatically by browsers), but becomes dangerous if cookie-based auth
// is ever added. In that case, restrict CORSOrigins to specific domains.
func corsMiddleware(origins []string) func(http.Handler) http.Handler {
	originSet := make(map[string]bool, len(origins))
	allowAll := false
	for _, o := range origins {
		if o == "*" {
			allowAll = true
		}
		originSet[o] = true
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}

			if allowAll || originSet[origin] {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Accept, Authorization, Content-Type, X-API-Key, X-Request-ID")
				w.Header().Set("Access-Control-Allow-Credentials", "true")
				w.Header().Set("Access-Control-Max-Age", "300")
				w.Header().Set("Vary", "Origin")
			}

			// Handle preflight
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// New creates a new server instance
func New(
	cfg *config.Config,
	store storage.Store,
	cache cache.Cache,
	g graph.Graph,
	persister *graph.AdaptivePersister,
	validator validation.Validator,
	logger zerolog.Logger,
) *Server {
	s := &Server{
		config:         cfg,
		storage:        store,
		cache:          cache,
		graph:          g,
		persister:      persister,
		validator:      validator,
		logger:         logger,
		router:         chi.NewRouter(),
		tenantRegistry: tenant.NewRegistry(),
	}

	// Initialize rate limiter if enabled
	if cfg.RateLimitEnabled {
		s.rateLimiter = oluMiddleware.NewRateLimiter(cfg)
	}

	// Initialize metrics if enabled
	if cfg.MetricsEnabled {
		s.metrics = oluMiddleware.NewMetrics()
	}

	// Initialize Sulpher query engine if graph is enabled
	if cfg.GraphEnabled {
		executor := sulpher.NewExecutor(g, cfg.MaxQueryDepth).WithStore(s.storage)
		s.sulpherJobs = sulpher.NewJobManager(executor, time.Duration(cfg.GraphQueryTTL)*time.Second)
		s.sulpherJobs.SetLimits(sulpher.GraphLimits{
			MaxVisitedNodes: cfg.GraphMaxVisitedNodes,
			MaxResults:      cfg.GraphMaxResults,
		})
		if cfg.QueryTimeout > 0 {
			s.sulpherJobs.SetQueryTimeout(time.Duration(cfg.QueryTimeout) * time.Second)
		}
	}

	// Initialize OQL query engine with schema validation
	oqlEngine := oql.NewEngineWithSchemaValidator(store, cfg.SchemaDir, validator)
	oqlEngine.SetLimits(oql.QueryLimits{
		MaxRows:     cfg.QueryMaxRows,
		MaxScanRows: cfg.QueryMaxScanRows,
	})

	// Select hardware profile for query planner thresholds.
	profileName := strings.ToLower(cfg.PerformanceProfile)
	if profileName == "" {
		profileName = "auto"
	}
	switch profileName {
	case "auto":
		// Run startup micro-benchmark to calibrate thresholds.
		if sqlStore, ok := store.(*storage.SQLiteStore); ok {
			profile, err := oql.Calibrate(sqlStore.DB())
			if err != nil {
				logger.Warn().Err(err).Msg("Hardware calibration failed, using VPS defaults")
			} else {
				oqlEngine.SetProfile(profile)
			}
		}
	case "edge", "vps", "dedicated":
		if profile := oql.ProfileByName(profileName); profile != nil {
			oqlEngine.SetProfile(profile)
			logger.Info().Str("profile", profileName).Msg("Using hardware profile")
		}
	default:
		logger.Warn().Str("profile", cfg.PerformanceProfile).Msg("Unknown performance profile, using VPS defaults")
	}

	s.oqlJobs = oql.NewJobManager(oqlEngine, time.Duration(cfg.GraphQueryTTL)*time.Second)
	if cfg.QueryTimeout > 0 {
		s.oqlJobs.SetQueryTimeout(time.Duration(cfg.QueryTimeout) * time.Second)
	}

	// Attach tenant persistence if using SQLite storage.
	// This ensures name-to-ID mappings are stable across restarts.
	if cfg.StorageType == "sqlite" {
		if sqlStore, ok := store.(*storage.SQLiteStore); ok {
			tp := storage.NewSQLiteTenantPersister(sqlStore.DB(), sqlStore.ReaderDB())
			s.tenantRegistry.SetPersister(tp)
			if err := s.tenantRegistry.LoadFrom(context.Background()); err != nil {
				logger.Error().Err(err).Msg("Failed to load tenant registry from database")
			}
		}
	}

	// Initialize timeseries manager if enabled
	if cfg.TimeseriesEnabled {
		tsBaseDir := filepath.Join(cfg.BaseDir, "ts")
		tsCfg := timeseries.StoreConfig{
			DefaultRetentionDays: cfg.TSDefaultRetentionDays,
		}
		pebbleCfg := timeseries.PebbleConfig{
			MemtableSize:          cfg.TSMemtableSize,
			BlockSize:             cfg.TSBlockSize,
			Compression:           cfg.TSCompression,
			L0CompactionThreshold: cfg.TSL0CompactionThreshold,
			MaxOpenFiles:          cfg.TSMaxOpenFiles,
		}
		tsm, err := timeseries.NewManager(tsBaseDir, timeseries.NewPebbleStoreFactory(pebbleCfg), tsCfg)
		if err != nil {
			logger.Error().Err(err).Msg("Failed to initialise timeseries manager")
		} else {
			s.tsManager = tsm
			if cfg.TSRetentionEnabled && cfg.TSCompactionIntervalSecs > 0 {
				interval := time.Duration(cfg.TSCompactionIntervalSecs) * time.Second
				w := timeseries.NewRetentionWorker(tsm, interval)
				w.Start()
				s.tsRetention = w
			}
		}
	}

	s.setupRoutes()
	atomic.StoreInt32(&s.ready, 1)
	return s
}

// setupRoutes configures all HTTP routes
func (s *Server) setupRoutes() {
	s.router.Use(middleware.RequestID)
	s.router.Use(middleware.RealIP)
	s.router.Use(middleware.Logger)
	s.router.Use(middleware.Recoverer)
	reqTimeout := 60
	if s.config.HTTPRequestTimeout > 0 {
		reqTimeout = s.config.HTTPRequestTimeout
	}
	s.router.Use(middleware.Timeout(time.Duration(reqTimeout) * time.Second))

	// Limit request body size to prevent abuse. Uses MaxEntitySize from config.
	maxBody := int64(s.config.MaxEntitySize)
	if maxBody <= 0 {
		maxBody = 1 << 20 // 1MB fallback
	}
	s.router.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, maxBody)
			next.ServeHTTP(w, r)
		})
	})

	// CORS middleware (before auth so preflight requests are handled)
	if len(s.config.CORSOrigins) > 0 {
		s.router.Use(corsMiddleware(s.config.CORSOrigins))
	}

	// Metrics middleware (before auth so we capture all requests)
	if s.metrics != nil {
		s.router.Use(oluMiddleware.MetricsMiddleware(s.metrics))
	}

	// Authentication middleware (applied to all routes, checks exclusions internally)
	s.router.Use(oluMiddleware.AuthMiddleware(s.config))

	// Rate limiting middleware
	if s.rateLimiter != nil {
		s.router.Use(oluMiddleware.RateLimitMiddleware(s.config, s.rateLimiter))
	}

	// Tenant strict mode middleware
	if s.config.TenantMode == "strict" {
		s.router.Use(s.tenantStrictMiddleware)
	}

	// Health check, readiness, and metrics
	s.router.Get("/health", s.handleHealth)
	s.router.Get("/ready", s.handleReady)
	s.router.Get("/version", s.handleVersion)
	// /metrics is served on the main port only when no dedicated metrics port
	// is configured. When MetricsPort > 0 it moves to the dedicated listener
	// started in Start(), keeping Prometheus scrape traffic off the API port.
	if s.config.MetricsPort == 0 {
		s.router.Get("/metrics", s.handleMetrics)
	}

	// API routes
	s.router.Route("/api/v1", func(r chi.Router) {
		// Entity CRUD operations (non-tenant)
		r.Post("/{entity}", s.handleCreate)
		r.Get("/{entity}", s.handleList)
		r.Get("/{entity}/{id}", s.handleGet)
		r.Put("/{entity}/{id}", s.handleUpdate)
		r.Patch("/{entity}/{id}", s.handlePatch)
		r.Delete("/{entity}/{id}", s.handleDelete)
		r.Post("/{entity}/save/{id}", s.handleSave)
		r.Post("/commit", s.handleCommit)
		r.Route("/tenant/{tenant_id}", func(tr chi.Router) {
			tr.Use(s.tenantMiddleware)
			tr.Post("/{entity}", s.handleCreate)
			tr.Get("/{entity}", s.handleList)
			tr.Get("/{entity}/{id}", s.handleGet)
			tr.Put("/{entity}/{id}", s.handleUpdate)
			tr.Patch("/{entity}/{id}", s.handlePatch)
			tr.Delete("/{entity}/{id}", s.handleDelete)
			tr.Post("/{entity}/save/{id}", s.handleSave)
			tr.Post("/commit", s.handleCommit)
			tr.Get("/search", s.handleFullTextSearch)

			// Tenant-scoped OQL queries
			tr.Post("/oql/query", s.handleOQLQuery)
			tr.Post("/oql/query/async", s.handleOQLQueryAsync)
			tr.Get("/oql/query/{query_id}", s.handleOQLQueryStatus)
			tr.Get("/oql/query/{query_id}/result", s.handleOQLQueryResult)

			// Tenant-scoped timeseries routes
			if s.tsManager != nil {
				tr.Route("/ts", func(tsr chi.Router) {
					tsr.Post("/provision", s.HandleTSProvision)
					// Timeline management
					tsr.Post("/timelines", s.HandleTSDefineTimeline)
					tsr.Get("/timelines", s.HandleTSListTimelines)
					tsr.Get("/timelines/{timeline_id}", s.HandleTSGetTimeline)
					tsr.Patch("/timelines/{timeline_id}", s.HandleTSUpdateTimeline)
					// Events
					tsr.Post("/events", s.HandleTSAppend)
					tsr.Post("/events/batch", s.HandleTSBatchAppend)
					tsr.Get("/events", s.HandleTSQueryRange)
					tsr.Get("/events/latest", s.HandleTSLatest)
					// Aggregate
					tsr.Post("/aggregate", s.HandleTSAggregate)
					// Retention + stats
					tsr.Get("/retention", s.HandleTSGetRetention)
					tsr.Get("/stats", s.HandleTSStats)
					tsr.Get("/stats/{timeline_id}", s.HandleTSTimelineStats)
				})
			}

			// Tenant-scoped graph routes — available in both path and strict mode.
			// Node IDs in requests/responses use the client-facing "entity:id"
			// format; the XXXX@ prefix is added/stripped transparently.
			if s.config.GraphEnabled {
				tr.Route("/graph", func(gr chi.Router) {
					gr.Get("/stats", s.handleTenantGraphStats)
					gr.Get("/nodes/{node_id}", s.handleTenantGraphNodeInfo)
					gr.Get("/nodes/{node_id}/degree", s.handleTenantGraphNodeDegree)
					gr.Get("/{node_id}/in", s.handleTenantGraphIncoming)
					gr.Get("/{node_id}/out", s.handleTenantGraphOutgoing)
					gr.Post("/path", s.handleTenantGraphPath)
					gr.Post("/neighbors", s.handleTenantGraphNeighbors)
					gr.Post("/shortestPath", s.handleTenantGraphShortestPath)
					gr.Post("/pathExists", s.handleTenantGraphPathExists)
					gr.Post("/commonNeighbors", s.handleTenantGraphCommonNeighbors)
					gr.Post("/nodes/search", s.handleTenantGraphNodeSearch)
					gr.Post("/query", s.handleTenantSulpherQuery)
					gr.Post("/query/async", s.handleTenantSulpherQueryAsync)
					gr.Get("/query/{query_id}", s.handleTenantSulpherQueryStatus)
					gr.Get("/query/{query_id}/result", s.handleTenantSulpherQueryResult)
				})
			}
		})

		// Non-tenant routes: these operate against the default store (tenant 0).
		// In strict mode they are disabled to prevent accidental unscoped queries.
		if s.config.TenantMode != "strict" {
			// OQL (SQL) query language endpoints (default store)
			r.Post("/oql/query", s.handleOQLQuery)
			r.Post("/oql/query/async", s.handleOQLQueryAsync)
			r.Get("/oql/query/{query_id}", s.handleOQLQueryStatus)
			r.Get("/oql/query/{query_id}/result", s.handleOQLQueryResult)

			// Search operations (default store)
			r.Get("/search", s.handleFullTextSearch)

			// Export operations (default store)
			r.Get("/export", s.handleExport)

			// Graph operations (default store, unscoped — sees all tenant nodes)
			if s.config.GraphEnabled {
				r.Post("/graph/path", s.handleGraphPath)
				r.Post("/graph/neighbors", s.handleGraphNeighbors)
				r.Get("/graph/stats", s.handleGraphStats)
				r.Get("/graph/nodes/{node_id}", s.handleGraphNodeInfo)
				r.Get("/graph/nodes/{node_id}/degree", s.handleGraphNodeDegree)
				r.Get("/graph/{node_id}/in", s.handleGraphIncoming)
				r.Get("/graph/{node_id}/out", s.handleGraphOutgoing)
				r.Post("/graph/shortestPath", s.handleGraphShortestPath)
				r.Post("/graph/pathExists", s.handleGraphPathExists)
				r.Post("/graph/commonNeighbors", s.handleGraphCommonNeighbors)
				r.Post("/graph/nodes/search", s.handleGraphNodeSearch)
				r.Post("/graph/query", s.handleSulpherQuery)
				r.Post("/graph/query/async", s.handleSulpherQueryAsync)
				r.Get("/graph/query/{query_id}", s.handleSulpherQueryStatus)
				r.Get("/graph/query/{query_id}/result", s.handleSulpherQueryResult)
				// Admin / maintenance endpoints (no-op 501 on non-SQLite backends)
				r.Get("/graph/admin/verify", s.handleGraphVerify)
				r.Post("/graph/admin/rebuild", s.handleGraphRebuild)
			}
		}

		// Schema operations (tenant-independent, always available)
		r.Post("/schema/{entity}", s.handleCreateSchema)
		r.Get("/schema/{entity}", s.handleGetSchema)
	})
}

// metricsHost returns the bind address for the dedicated metrics listener.
//
// If the operator set OLU_METRICS_HOST explicitly, that value wins.
// Otherwise: if OLU_HOST is a real interface address (not 0.0.0.0 or ::),
// the metrics listener inherits it — keeping scrape traffic on the same
// interface as the API without any extra config. If OLU_HOST is a wildcard,
// there is no meaningful interface preference to propagate, so we fall back
// to 0.0.0.0.
func (s *Server) metricsHost() string {
	if s.config.MetricsHost != "" {
		return s.config.MetricsHost
	}
	h := s.config.Host
	if h != "" && h != "0.0.0.0" && h != "::" {
		return h
	}
	return "0.0.0.0"
}

// Start starts the HTTP server
func (s *Server) Start() error {
	addr := fmt.Sprintf("%s:%d", s.config.Host, s.config.Port)
	s.logger.Info().Str("addr", addr).Msg("Starting server")
	s.httpServer = &http.Server{
		Addr:    addr,
		Handler: s.router,
	}
	if s.config.HTTPReadTimeout > 0 {
		s.httpServer.ReadTimeout = time.Duration(s.config.HTTPReadTimeout) * time.Second
	}
	if s.config.HTTPWriteTimeout > 0 {
		s.httpServer.WriteTimeout = time.Duration(s.config.HTTPWriteTimeout) * time.Second
	}
	if s.config.HTTPIdleTimeout > 0 {
		s.httpServer.IdleTimeout = time.Duration(s.config.HTTPIdleTimeout) * time.Second
	}

	// When a dedicated metrics port is configured, start a minimal listener
	// that serves only /metrics. This keeps Prometheus scrape traffic
	// separated from the main API port.
	if s.config.MetricsPort > 0 {
		metricsAddr := fmt.Sprintf("%s:%d", s.metricsHost(), s.config.MetricsPort)
		s.logger.Info().Str("addr", metricsAddr).Msg("Starting dedicated metrics listener")
		metricsMux := http.NewServeMux()
		metricsMux.HandleFunc("/metrics", s.handleMetrics)
		s.metricsServer = &http.Server{
			Addr:    metricsAddr,
			Handler: metricsMux,
		}
		go func() {
			if err := s.metricsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				s.logger.Error().Err(err).Str("addr", metricsAddr).Msg("Metrics listener failed")
			}
		}()
	}

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the HTTP server, allowing in-flight
// requests to complete within the given context deadline.
func (s *Server) Shutdown(ctx context.Context) error {
	// Shut down the metrics listener first (best-effort; scrapes are idempotent).
	if s.metricsServer != nil {
		_ = s.metricsServer.Shutdown(ctx)
	}
	if s.httpServer != nil {
		return s.httpServer.Shutdown(ctx)
	}
	return nil
}

// Stop stops the server and cleans up resources
func (s *Server) Stop() {
	if s.rateLimiter != nil {
		s.rateLimiter.Stop()
	}
	// Stop retention worker before closing stores.
	if s.tsRetention != nil {
		s.tsRetention.Stop()
	}
	// Close timeseries stores before tenant stores.
	if s.tsManager != nil {
		if err := s.tsManager.Close(); err != nil {
			s.logger.Error().Err(err).Msg("Failed to close timeseries manager")
		}
	}
	// Close cached tenant stores
	s.tenantStores.Range(func(key, value interface{}) bool {
		if st, ok := value.(storage.Store); ok {
			st.Close()
		}
		s.tenantStores.Delete(key)
		return true
	})
}

// Handler returns the HTTP handler (useful for testing)
func (s *Server) Handler() http.Handler {
	return s.router
}

// handleHealth returns server health status with database connectivity check.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if s.storage != nil {
		if err := s.storage.Ping(ctx); err != nil {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"status":  "degraded",
				"version": version.Version,
				"db":      "unreachable",
			})
			return
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status":  "ok",
		"version": version.Version,
	})
}

// handleVersion returns server version
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	s.writeJSON(w, http.StatusOK, map[string]string{
		"version": version.Version,
	})
}

// handleReady returns readiness status for Kubernetes-style probes.
// Returns 503 during server initialisation or when the database is
// unreachable; returns 200 once the server is fully operational.
func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	if atomic.LoadInt32(&s.ready) == 0 {
		s.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
			"status": "initialising",
		})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if s.storage != nil {
		if err := s.storage.Ping(ctx); err != nil {
			s.writeJSON(w, http.StatusServiceUnavailable, map[string]interface{}{
				"status": "not_ready",
				"db":     "unreachable",
			})
			return
		}
	}

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"status": "ready",
	})
}

// MarkReady sets the server as ready. This is called automatically by
// Start(), but can be called manually in test setups that use
// httptest.NewServer instead of Start().
func (s *Server) MarkReady() {
	atomic.StoreInt32(&s.ready, 1)
}

// handleMetrics returns Prometheus-format metrics
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	if s.metrics == nil {
		s.writeError(w, http.StatusServiceUnavailable, oluerr.ErrSearchDisabled, "Metrics not enabled")
		return
	}

	// Check Accept header for format preference
	accept := r.Header.Get("Accept")
	if accept == "application/json" {
		// Return JSON format
		snapshot := s.metrics.GetSnapshot()
		s.writeJSON(w, http.StatusOK, snapshot)
		return
	}

	// Default to Prometheus format
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(s.metrics.PrometheusFormat()))
}

// handleCreate creates a new entity
func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	entity := chi.URLParam(r, "entity")
	if err := validateEntityName(entity); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidEntity, err.Error())
		return
	}

	var data map[string]interface{}
	if !s.decodeJSON(w, r, &data) {
		return
	}

	// Validate against schema
	if valid, errors := s.validator.Validate(entity, data); !valid {
		s.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    string(oluerr.ErrValidationFailed),
				"message": "Validation failed",
				"status":  http.StatusBadRequest,
			},
			"details": errors,
		})
		return
	}

	// Check size limit
	jsonData, _ := json.Marshal(data)
	if len(jsonData) > s.config.MaxEntitySize {
		s.writeError(w, http.StatusRequestEntityTooLarge, oluerr.ErrEntityTooLarge,
			fmt.Sprintf("Entity too large: %d bytes (max: %d)",
				len(jsonData), s.config.MaxEntitySize))
		return
	}

	// Pre-validate graph edges before the store write. This prevents the
	// SQLite edge table and in-memory graph from diverging when cycle detection
	// is active (cycleDetection == "error").
	// ID is not known yet (auto-assigned); use 0 as a placeholder — cycle
	// checks only examine existing graph topology, not the new node's ID.
	if err := s.validateGraphEdges(r.Context(), entity, 0, data); err != nil {
		if errors.Is(err, graph.ErrCycleDetected) {
			s.writeError(w, http.StatusConflict, oluerr.ErrStorageFailed, err.Error())
			return
		}
		if errors.Is(err, models.ErrDuplicateEdgeTarget) {
			s.writeError(w, http.StatusBadRequest, oluerr.ErrDuplicateEdgeRef, err.Error())
			return
		}
	}

	// Create entity using tenant-scoped store
	store := s.getStore(r.Context())
	id, err := store.Create(r.Context(), entity, data)
	if err != nil {
		if errors.Is(err, models.ErrDuplicateEdgeTarget) {
			s.writeError(w, http.StatusBadRequest, oluerr.ErrDuplicateEdgeRef, err.Error())
			return
		}
		s.logger.Error().Err(err).Msg("Failed to create entity")
		s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, "Failed to create entity")
		return
	}

	// Update graph
	data["id"] = id
	s.updateGraph(r.Context(), entity, id, data)

	// Invalidate cache
	s.invalidateCacheForID(r.Context(), entity, id)

	s.logger.Info().Str("entity", entity).Int("id", id).Msg("Created entity")

	s.writeJSON(w, http.StatusCreated, map[string]interface{}{
		"message": fmt.Sprintf("Resource of entity %s created successfully", entity),
		"id":      id,
	})
}

// handleList lists all entities of a type
func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	entity := chi.URLParam(r, "entity")
	if err := validateEntityName(entity); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidEntity, err.Error())
		return
	}

	// Get pagination params
	page, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	perPage, _ := strconv.Atoi(r.URL.Query().Get("per_page"))
	if perPage < 1 || perPage > 100 {
		perPage = s.config.DefaultPageSize
		if perPage < 1 {
			perPage = 10 // Fallback default
		}
	}

	// Extract filter params (exclude pagination and system params)
	filters := extractFilters(r.URL.Query())

	// Build cache key including filters and tenant scope
	tid := getTenantIDNumeric(r.Context())
	cacheKey := tenant.ScopeKey(tid, buildListCacheKey(entity, page, perPage, filters))
	if cached, err := s.cache.Get(r.Context(), cacheKey); err == nil {
		s.writeJSON(w, http.StatusOK, cached)
		return
	}

	// Get all entities from tenant-scoped store
	store := s.getStore(r.Context())

	// Check if this entity uses adapted tables (column-per-field storage).
	// Push-down generates SQL against the blob entities table, which doesn't
	// contain adapted entity data, so we skip it for adapted entities and
	// let the ListPaged or List paths handle them.
	isAdapted := false
	if sqlStore, ok := store.(*storage.SQLiteStore); ok {
		if sqlStore.AdaptedRegistry().IsAdapted(entity) {
			isAdapted = true
		}
	}

	// Try push-down path: if the store supports Queryable, push WHERE + LIMIT
	// to SQL rather than loading everything into Go memory.
	if !isAdapted {
	if qs, ok := store.(storage.Queryable); ok && qs.Capabilities().Where && qs.Capabilities().Limit {
		result, totalItems, pushErr := s.listWithPushDown(r.Context(), qs, entity, page, perPage, filters)
		if pushErr == nil {
			totalPages := (totalItems + perPage - 1) / perPage

			// Embed references if requested
			embedParam := r.URL.Query().Get("embed")
			if embedParam != "false" && embedParam != "0" {
				embedDepth := 0
				if depthParam := r.URL.Query().Get("embed_depth"); depthParam != "" {
					if parsed, err := strconv.Atoi(depthParam); err == nil && parsed > 0 {
						embedDepth = parsed
					}
				}
				if embedDepth > s.config.MaxEmbedDepth {
					embedDepth = s.config.MaxEmbedDepth
				}
				if embedDepth > 0 {
					for i, item := range result {
						result[i] = s.embedReferences(r.Context(), item, embedDepth)
					}
				}
			}

			response := models.PagedResponse{
				Data: result,
			}
			response.Pagination.Page = page
			response.Pagination.PerPage = perPage
			response.Pagination.TotalItems = totalItems
			response.Pagination.TotalPages = totalPages

			_ = s.cache.Set(r.Context(), cacheKey, response, time.Duration(s.config.CacheTTL)*time.Second)
			s.writeJSON(w, http.StatusOK, response)
			return
		}
		// Push-down failed — fall through
		s.logger.Debug().Err(pushErr).Msg("List push-down failed, falling back")
	}
	} // !isAdapted

	// Try PagedLister path: storage-level LIMIT/OFFSET without filters.
	// This avoids loading all records when only a page is needed.
	if len(filters) == 0 {
		if pl, ok := store.(storage.PagedLister); ok {
			offset := (page - 1) * perPage
			pr, plErr := pl.ListPaged(r.Context(), entity, perPage, offset)
			if plErr == nil {
				pageData := pr.Data
				totalPages := (pr.TotalItems + perPage - 1) / perPage

				// Embed references if requested
				embedParam := r.URL.Query().Get("embed")
				if embedParam != "false" && embedParam != "0" {
					embedDepth := 0
					if depthParam := r.URL.Query().Get("embed_depth"); depthParam != "" {
						if parsed, err := strconv.Atoi(depthParam); err == nil && parsed > 0 {
							embedDepth = parsed
						}
					}
					if embedDepth > s.config.MaxEmbedDepth {
						embedDepth = s.config.MaxEmbedDepth
					}
					if embedDepth > 0 {
						for i, item := range pageData {
							pageData[i] = s.embedReferences(r.Context(), item, embedDepth)
						}
					}
				}

				response := models.PagedResponse{
					Data: pageData,
				}
				response.Pagination.Page = page
				response.Pagination.PerPage = perPage
				response.Pagination.TotalItems = pr.TotalItems
				response.Pagination.TotalPages = totalPages

				_ = s.cache.Set(r.Context(), cacheKey, response, time.Duration(s.config.CacheTTL)*time.Second)
				s.writeJSON(w, http.StatusOK, response)
				return
			}
			s.logger.Debug().Err(plErr).Msg("ListPaged failed, falling back to full List")
		}
	}

	entities, err := store.List(r.Context(), entity)
	if err != nil {
		s.logger.Error().Err(err).Msg("Failed to list entities")
		s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, "Failed to list entities")
		return
	}

	// Apply filters
	if len(filters) > 0 {
		entities = applyFilters(entities, filters)
	}

	// Apply pagination
	totalItems := len(entities)
	totalPages := (totalItems + perPage - 1) / perPage

	start := (page - 1) * perPage
	end := start + perPage
	if end > totalItems {
		end = totalItems
	}

	var pageData []map[string]interface{}
	if start < totalItems {
		pageData = entities[start:end]
	} else {
		pageData = []map[string]interface{}{}
	}

	// Embed references if explicitly requested (not default for lists due to performance)
	embedParam := r.URL.Query().Get("embed")
	if embedParam != "false" && embedParam != "0" {
		embedDepth := 0
		if depthParam := r.URL.Query().Get("embed_depth"); depthParam != "" {
			if parsed, err := strconv.Atoi(depthParam); err == nil && parsed > 0 {
				embedDepth = parsed
			}
		}
		if embedDepth > s.config.MaxEmbedDepth {
			embedDepth = s.config.MaxEmbedDepth
		}
		if embedDepth > 0 {
			for i, item := range pageData {
				pageData[i] = s.embedReferences(r.Context(), item, embedDepth)
			}
		}
	}

	response := models.PagedResponse{
		Data: pageData,
	}
	response.Pagination.Page = page
	response.Pagination.PerPage = perPage
	response.Pagination.TotalItems = totalItems
	response.Pagination.TotalPages = totalPages

	// Cache result
	_ = s.cache.Set(r.Context(), cacheKey, response, time.Duration(s.config.CacheTTL)*time.Second)

	s.writeJSON(w, http.StatusOK, response)
}

// handleGet retrieves a single entity
func (s *Server) handleGet(w http.ResponseWriter, r *http.Request) {
	entity := chi.URLParam(r, "entity")
	idStr := chi.URLParam(r, "id")

	if err := validateEntityName(entity); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidEntity, err.Error())
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil || id < 0 {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidID, "Invalid ID")
		return
	}

	// Check cache for raw entity data
	tid := getTenantIDNumeric(r.Context())
	cacheKey := tenant.CacheKey(tid, entity, id)
	var data map[string]interface{}

	if cached, err := s.cache.Get(r.Context(), cacheKey); err == nil {
		// Cache hit - use cached data
		if cachedData, ok := cached.(map[string]interface{}); ok {
			// Make a copy to avoid mutating cached data during embedding
			data = make(map[string]interface{}, len(cachedData))
			for k, v := range cachedData {
				data[k] = v
			}
		}
	}

	if data == nil {
		// Cache miss - fetch from tenant-scoped store
		store := s.getStore(r.Context())
		var err error
		data, err = store.Get(r.Context(), entity, id)
		if err != nil {
			if errors.Is(err, storage.ErrNotFound) {
				s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound,
					fmt.Sprintf("Resource of entity %s with id %d not found", entity, id))
				return
			}
			s.logger.Error().Err(err).Msg("Failed to get entity")
			s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, "Failed to get entity")
			return
		}

		// Cache the raw data
		_ = s.cache.Set(r.Context(), cacheKey, data, time.Duration(s.config.CacheTTL)*time.Second)
	}

	// Embed references
	// Use RefEmbedDepth as default, allow override via query param
	// Use embed=false to disable entirely
	embedParam := r.URL.Query().Get("embed")
	if embedParam == "false" || embedParam == "0" {
		// Embedding explicitly disabled
	} else {
		embedDepth := s.config.RefEmbedDepth
		if depthParam := r.URL.Query().Get("embed_depth"); depthParam != "" {
			if parsed, err := strconv.Atoi(depthParam); err == nil && parsed >= 0 {
				embedDepth = parsed
			}
		}
		if embedDepth > s.config.MaxEmbedDepth {
			embedDepth = s.config.MaxEmbedDepth
		}
		if embedDepth > 0 {
			data = s.embedReferences(r.Context(), data, embedDepth)
		}
	}

	s.writeJSON(w, http.StatusOK, data)
}

// handleUpdate updates an entire entity
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	entity := chi.URLParam(r, "entity")
	idStr := chi.URLParam(r, "id")

	if err := validateEntityName(entity); err != nil {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidEntity, err.Error())
		return
	}

	id, err := strconv.Atoi(idStr)
	if err != nil || id < 0 {
		s.writeError(w, http.StatusBadRequest, oluerr.ErrInvalidID, "Invalid ID")
		return
	}

	var data map[string]interface{}
	if !s.decodeJSON(w, r, &data) {
		return
	}

	// Validate
	data["id"] = id
	if valid, errors := s.validator.Validate(entity, data); !valid {
		s.writeJSON(w, http.StatusBadRequest, map[string]interface{}{
			"error": map[string]interface{}{
				"code":    string(oluerr.ErrValidationFailed),
				"message": "Validation failed",
				"status":  http.StatusBadRequest,
			},
			"details": errors,
		})
		return
	}

	// Pre-validate graph edges before the store write (Bug 1 fix).
	if err := s.validateGraphEdges(r.Context(), entity, id, data); err != nil {
		if errors.Is(err, graph.ErrCycleDetected) {
			s.writeError(w, http.StatusConflict, oluerr.ErrStorageFailed, err.Error())
			return
		}
		if errors.Is(err, models.ErrDuplicateEdgeTarget) {
			s.writeError(w, http.StatusBadRequest, oluerr.ErrDuplicateEdgeRef, err.Error())
			return
		}
	}

	// Update using tenant-scoped store
	store := s.getStore(r.Context())
	if err := store.Update(r.Context(), entity, id, data); err != nil {
		if errors.Is(err, storage.ErrNotFound) {
			s.writeError(w, http.StatusNotFound, oluerr.ErrEntityNotFound,
				fmt.Sprintf("Resource of entity %s with id %d not found", entity, id))
			return
		}
		if errors.Is(err, storage.ErrConflict) {
			currentVer := s.fetchCurrentVersion(r.Context(), entity, id)
			s.writeJSON(w, http.StatusConflict, map[string]interface{}{
				"error": map[string]interface{}{
					"code":    string(oluerr.ErrVersionConflict),
					"message": fmt.Sprintf("Version conflict: %s with id %d has been modified by another request", entity, id),
					"status":  http.StatusConflict,
				},
				"current_version": currentVer,
			})
			return
		}
		if errors.Is(err, models.ErrDuplicateEdgeTarget) {
			s.writeError(w, http.StatusBadRequest, oluerr.ErrDuplicateEdgeRef, err.Error())
			return
		}
		s.logger.Error().Err(err).Msg("Failed to update entity")
		s.writeError(w, http.StatusInternalServerError, oluerr.ErrStorageFailed, "Failed to update entity")
		return
	}

	// Update graph
	s.updateGraph(r.Context(), entity, id, data)

	s.invalidateCache(r.Context(), entity)
	s.logger.Info().Str("entity", entity).Int("id", id).Msg("Updated entity")

	s.writeJSON(w, http.StatusOK, map[string]interface{}{
		"message": fmt.Sprintf("Resource of entity %s with id %d updated successfully", entity, id),
	})
}

// Continued in next part...

// reservedQueryParams are params that should not be treated as filters
var reservedQueryParams = map[string]bool{
	"page":        true,
	"per_page":    true,
	"embed_depth": true,
	"sort":        true,
	"order":       true,
}

// extractFilters extracts filter parameters from query string
// listWithPushDown generates a SQL query for the list endpoint and executes it
// via the Queryable interface. Returns the page of results and total item count.
// Filters are translated to json_extract WHERE clauses; pagination uses LIMIT/OFFSET.
func (s *Server) listWithPushDown(ctx context.Context, qs storage.Queryable, entity string, page, perPage int, filters map[string]string) ([]map[string]interface{}, int, error) {
	tid := int(getTenantIDNumeric(ctx))

	// Build filter WHERE clauses (sorted for deterministic queries)
	var filterClauses []string
	var filterArgs []interface{}
	for field, value := range filters {
		if err := validateFieldName(field); err != nil {
			return nil, 0, fmt.Errorf("invalid filter: %w", err)
		}
		filterClauses = append(filterClauses, fmt.Sprintf("json_extract(data, '$.%s') = ?", field))
		filterArgs = append(filterArgs, value)
	}
	sort.Strings(filterClauses)

	filterSQL := ""
	if len(filterClauses) > 0 {
		filterSQL = " AND " + strings.Join(filterClauses, " AND ")
	}

	// Step 1: Get total count. Use CountEntities for unfiltered, or a
	// lightweight count query wrapped as JSON for filtered requests so
	// it can pass through QueryWithPlan's (data, _version) scan.
	var totalItems int
	if len(filters) == 0 {
		count, err := qs.CountEntities(ctx, entity)
		if err != nil {
			return nil, 0, fmt.Errorf("count: %w", err)
		}
		totalItems = count
	} else {
		// Filtered count: synthesise a JSON blob so QueryWithPlan can scan it.
		cntSQL := `SELECT '{"c":' || COUNT(*) || '}' AS data, 0 AS _version FROM entities WHERE tenant_id = ? AND entity_type = ?` + filterSQL
		cntArgs := append([]interface{}{tid, entity}, filterArgs...)
		cntRows, err := qs.QueryWithPlan(ctx, cntSQL, cntArgs)
		if err != nil {
			return nil, 0, fmt.Errorf("filtered count: %w", err)
		}
		if len(cntRows) > 0 {
			if c, ok := cntRows[0]["c"].(float64); ok {
				totalItems = int(c)
			}
		}
	}

	if totalItems == 0 {
		return []map[string]interface{}{}, 0, nil
	}

	// Step 2: Fetch the page.
	offset := (page - 1) * perPage
	dataSQL := "SELECT data, _version FROM entities WHERE tenant_id = ? AND entity_type = ?" + filterSQL + " ORDER BY id LIMIT ? OFFSET ?"
	dataArgs := append([]interface{}{tid, entity}, filterArgs...)
	dataArgs = append(dataArgs, perPage, offset)

	rows, err := qs.QueryWithPlan(ctx, dataSQL, dataArgs)
	if err != nil {
		return nil, 0, fmt.Errorf("data query: %w", err)
	}

	return rows, totalItems, nil
}

func extractFilters(query url.Values) map[string]string {
	filters := make(map[string]string)
	for key, values := range query {
		if reservedQueryParams[key] {
			continue
		}
		if validateFieldName(key) != nil {
			continue // silently skip invalid field names
		}
		if len(values) > 0 && values[0] != "" {
			filters[key] = values[0]
		}
	}
	return filters
}

// buildListCacheKey creates a deterministic cache key including filters
func buildListCacheKey(entity string, page, perPage int, filters map[string]string) string {
	if len(filters) == 0 {
		return fmt.Sprintf("%s:list:%d:%d", entity, page, perPage)
	}

	// Sort filter keys for deterministic cache key
	keys := make([]string, 0, len(filters))
	for k := range filters {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var filterParts []string
	for _, k := range keys {
		filterParts = append(filterParts, fmt.Sprintf("%s=%s", k, filters[k]))
	}

	return fmt.Sprintf("%s:list:%d:%d:%s", entity, page, perPage, strings.Join(filterParts, ","))
}

// applyFilters filters entities by matching field values
func applyFilters(entities []map[string]interface{}, filters map[string]string) []map[string]interface{} {
	if len(filters) == 0 {
		return entities
	}

	var result []map[string]interface{}
	for _, entity := range entities {
		if matchesFilters(entity, filters) {
			result = append(result, entity)
		}
	}
	return result
}

// matchesFilters checks if an entity matches all filter criteria
func matchesFilters(entity map[string]interface{}, filters map[string]string) bool {
	for field, expected := range filters {
		value, exists := entity[field]
		if !exists {
			return false
		}

		// Convert value to string for comparison
		var actual string
		switch v := value.(type) {
		case string:
			actual = v
		case float64:
			// Handle both integer and float JSON numbers
			if v == float64(int(v)) {
				actual = strconv.Itoa(int(v))
			} else {
				actual = strconv.FormatFloat(v, 'f', -1, 64)
			}
		case int:
			actual = strconv.Itoa(v)
		case bool:
			actual = strconv.FormatBool(v)
		default:
			actual = fmt.Sprintf("%v", v)
		}

		if actual != expected {
			return false
		}
	}
	return true
}

// updateGraph updates the in-memory graph with tenant-scoped node IDs.
// For tenant 0 (unscoped), node IDs are "entity:id".
// For non-zero tenants, node IDs are "XXXX@entity:id".
func (s *Server) updateGraph(ctx context.Context, entityType string, id int, data map[string]interface{}) {
	if !s.config.GraphEnabled || s.graph == nil {
		return
	}
	if s.persister != nil {
		s.persister.WriterEnter()
		defer s.persister.WriterExit()
	}

	tid := getTenantIDNumeric(ctx)
	nodeID := tenant.NodeID(tid, entityType, id)

	// Add/update the node — index by entity schema name, not data["type"].
	if err := s.graph.AddNode(nodeID, entityType); err != nil {
		s.logger.Error().Err(err).Str("node", nodeID).Msg("Failed to add graph node")
		return
	}

	// Extract REF fields before mutating the graph. Validating first means
	// that if the entity data is malformed (e.g. duplicate edge targets) we
	// abort before removing existing edges, leaving the graph consistent.
	graphEdges, err := models.ExtractEntityEdges(data)
	if err != nil {
		s.logger.Error().Err(err).
			Str("node", nodeID).
			Msg("updateGraph: entity has duplicate edge target; graph not updated")
		return
	}

	// Remove all existing outgoing edges before re-adding from current data.
	// The entity store is the source of truth for REF fields; the graph is a
	// derived index and must reflect the current state after every write.
	if existing, err := s.graph.GetNeighbors(nodeID); err == nil {
		for target := range existing {
			if err := s.graph.RemoveEdge(nodeID, target); err != nil {
				s.logger.Error().Err(err).
					Str("from", nodeID).Str("to", target).
					Msg("Failed to remove stale graph edge")
			}
		}
	}
	for _, ee := range graphEdges {
		targetNodeID := tenant.NodeID(tid, ee.TargetEntity, ee.TargetID)
		if err := s.graph.AddEdge(nodeID, targetNodeID, ee.Relationship); err != nil {
			s.logger.Error().Err(err).
				Str("from", nodeID).Str("to", targetNodeID).
				Msg("updateGraph: failed to add graph edge")
		}
	}

	if s.persister != nil {
		s.persister.MarkDirty()
	}
}

// reloadGraphFromStore clears the in-memory graph and re-hydrates it from the
// store's edge table. Called after RebuildGraph to keep the in-memory graph
// consistent with the just-repaired edge table.
//
// If the store does not implement GraphEdgeScanner the graph is only cleared
// (it cannot be re-hydrated without the edge table). The caller's response
// should note that a restart is needed in that case.
func (s *Server) reloadGraphFromStore(ctx context.Context, store storage.Store) error {
	if s.graph == nil {
		return nil
	}
	if err := s.graph.Clear(); err != nil {
		return fmt.Errorf("reloadGraphFromStore: clear: %w", err)
	}

	scanner, ok := store.(storage.GraphEdgeScanner)
	if !ok {
		return nil // cleared but not re-hydrated; caller must note this
	}

	lister, hasLister := scanner.(storage.TenantIDLister)
	if !hasLister {
		// Hydrate the single tenant served by this store.
		if sqlStore, ok := store.(*storage.SQLiteStore); ok {
			tid := sqlStore.Config().TenantID
			_, err := scanTenantEdgesServer(ctx, scanner, s.graph, tid, s.logger)
			return err
		}
		_, err := scanTenantEdgesServer(ctx, scanner, s.graph, 0, s.logger)
		return err
	}

	tenantIDs, err := lister.GraphTenantIDs(ctx)
	if err != nil {
		return fmt.Errorf("reloadGraphFromStore: list tenants: %w", err)
	}
	for _, tid := range tenantIDs {
		if _, err := scanTenantEdgesServer(ctx, scanner, s.graph, tid, s.logger); err != nil {
			s.logger.Warn().Err(err).Uint16("tenant", tid).Msg("reloadGraphFromStore: tenant hydration failed")
		}
	}
	return nil
}

// scanTenantEdgesServer is the server-package equivalent of the main package's
// scanTenantEdges. It streams edges for one tenant into the in-memory graph.
func scanTenantEdgesServer(
	ctx context.Context,
	scanner storage.GraphEdgeScanner,
	g graph.Graph,
	tid uint16,
	logger zerolog.Logger,
) (int, error) {
	count := 0
	err := scanner.ScanGraphEdges(ctx, tid, func(e storage.GraphEdge) error {
		if err := g.AddNode(tenant.NodeID(tid, e.SourceEntity, e.SourceID), e.SourceEntity); err != nil {
			logger.Warn().Err(err).Str("source", e.SourceEntity).Int("id", e.SourceID).
				Msg("scanTenantEdgesServer: AddNode source failed")
		}
		if err := g.AddNode(tenant.NodeID(tid, e.TargetEntity, e.TargetID), e.TargetEntity); err != nil {
			logger.Warn().Err(err).Str("target", e.TargetEntity).Int("id", e.TargetID).
				Msg("scanTenantEdgesServer: AddNode target failed")
		}
		if err := g.AddEdge(
			tenant.NodeID(tid, e.SourceEntity, e.SourceID),
			tenant.NodeID(tid, e.TargetEntity, e.TargetID),
			e.Relationship,
		); err != nil {
			logger.Warn().Err(err).Str("rel", e.Relationship).
				Msg("scanTenantEdgesServer: AddEdge failed")
		}
		count++
		return nil
	})
	return count, err
}

// validateGraphEdges checks whether the edges implied by data would be
// accepted by the in-memory graph. Returns nil if the graph is disabled,
// the graph has no cycle-detection enforcement, or all edges pass. Returns an
// error (suitable for surfacing as HTTP 409) if any edge would be rejected.
//
// This MUST be called before the store write so that the SQLite edge table
// and the in-memory graph can never diverge due to a cycle-detection rejection.
func (s *Server) validateGraphEdges(ctx context.Context, entityType string, id int, data map[string]interface{}) error {
	if !s.config.GraphEnabled || s.graph == nil {
		return nil
	}
	graphEdges, err := models.ExtractEntityEdges(data)
	if err != nil {
		// Duplicate REF target — also caught by the store layer, but reject
		// early here for a cleaner error before any write.
		return err
	}
	if len(graphEdges) == 0 {
		return nil
	}
	tid := getTenantIDNumeric(ctx)
	fromNodeID := tenant.NodeID(tid, entityType, id)
	for _, ee := range graphEdges {
		toNodeID := tenant.NodeID(tid, ee.TargetEntity, ee.TargetID)
		if err := s.graph.CheckEdge(fromNodeID, toNodeID, ee.Relationship); err != nil {
			if errors.Is(err, graph.ErrCycleDetected) {
				return fmt.Errorf("edge %s->%s (%s) would create a cycle: %w",
					fromNodeID, toNodeID, ee.Relationship, err)
			}
			// ErrEdgeAlreadyExists and ErrCrossTenantEdge are not write-rejection
			// errors — syncGraphEdges handles idempotent overwrites. Only propagate
			// cycle errors.
		}
	}
	return nil
}

func (s *Server) removeGraph(ctx context.Context, entityType string, id int) {
	if !s.config.GraphEnabled || s.graph == nil {
		return
	}
	if s.persister != nil {
		s.persister.WriterEnter()
		defer s.persister.WriterExit()
	}
	tid := getTenantIDNumeric(ctx)
	nodeID := tenant.NodeID(tid, entityType, id)
	if err := s.graph.RemoveNode(nodeID); err != nil {
		s.logger.Error().Err(err).Str("node", nodeID).Msg("Failed to remove graph node")
	}
	if s.persister != nil {
		s.persister.MarkDirty()
	}
}