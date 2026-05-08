// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package config

import (
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
)

// Config holds application configuration
type Config struct {
	// Server configuration
	Host string
	Port int

	// Storage configuration
	StorageType string // "jsonfile" or "sqlite"
	BaseDir     string
	SchemaDir   string
	Schema      string
	DBPath      string // SQLite database path

	// Cache configuration
	CacheType string // "memory" or "redis"
	CacheTTL  int    // seconds
	RedisHost string
	RedisPort int
	CacheSize   int
	CacheShards int // Number of shards for in-memory cache (0 = default 16; must be power of 2)

	// Graph configuration
	GraphEnabled        bool
	GraphMode           string // "flat" (default) or "disabled"
	GraphDataFile       string
	GraphIndexFile      string
	GraphQueryTTL       int
	GraphResultTTL      int
	GraphMaxVisitedNodes  int // Max nodes visited during traversal (0 = default 10000)
	GraphMaxResults       int // Max result paths returned (0 = no limit)
	GraphCycleDetection   string // "warn", "error", "ignore"
	GraphCycleCheckLimit  int    // BFS budget for cycle detection (0 = use default 512)

	// Full-text search
	FullTextEnabled bool

	// Commit endpoint behaviour.
	//
	// When StrictCommit is true (the default), POST /commit runs the same
	// schema validation and graph cycle prechecks as the normal write
	// endpoints (save, create, patch) before executing the storage
	// transaction. Set to false only when the caller is trusted
	// infrastructure that manages its own invariants and the extra
	// validation overhead is undesirable.
	//
	// The /commit endpoint is not available when StorageType is "jsonfile".
	// The jsonfile backend does not provide true transactional atomicity and
	// has been deprecated for production use; enforcing this at the HTTP
	// layer prevents silent correctness problems.
	StrictCommit bool

	// Query configuration
	MaxQueryDepth   int
	MaxEmbedDepth   int
	RefEmbedDepth   int
	DefaultPageSize int

	// Query guardrails — limits that prevent runaway queries from becoming
	// outages. All limits are enforced server-side; client timeouts alone
	// are not sufficient because they abandon work without freeing resources.
	QueryTimeout         int // Seconds; max execution time for OQL/Sulpher queries (0 = use default 30)
	QueryMaxRows         int // Max rows returned by a single query (0 = use default 10000)
	QueryMaxScanRows     int // Max rows scanned before aborting (0 = use default 100000)
	QueryMaxResponseBytes int // Max JSON response size in bytes (0 = use default 10MB)

	// Entity configuration
	PatchNullBehavior string // "store" or "delete"
	MaxEntitySize     int    // bytes

	// Cascade delete configuration
	CascadingDelete     bool
	MaxCascadeDeletions int
	MaxCascadeWork      int

	// Debug
	Debug      bool
	DebugLocks bool
	// LogLevel sets the minimum log level: "debug", "info", "warn", "error".
	// OLU_LOG_LEVEL is the primary env var. OLU_DEBUG=true is a legacy alias
	// that maps to LogLevel "debug". When both are set, OLU_LOG_LEVEL wins.
	// Defaults to "info" when neither is set.
	LogLevel string

	// Authentication
	AuthType         string   // "none", "jwt", "apikey"
	JWTSecret        string   // Secret for JWT validation
	JWTIssuer        string   // Expected issuer claim
	APIKeys          []string // Valid API keys (comma-separated in env)
	// InternalToken is the shared secret for the "bearertoken" auth type.
	// The incoming Authorization: Bearer <token> value is compared against
	// this using subtle.ConstantTimeCompare. Typically a 32-byte hex string
	// generated with `openssl rand -hex 32`. Set via OLU_INTERNAL_TOKEN.
	InternalToken string
	AuthExcludePaths []string // Paths excluded from auth (e.g., /health)

	// Rate limiting
	RateLimitEnabled bool
	RateLimitRate    int // Requests per window
	RateLimitWindow  int // Window in seconds
	RateLimitByIP    bool
	RateLimitByKey   bool // Rate limit by API key or JWT subject

	// Metrics
	MetricsEnabled bool
	// MetricsPort, when > 0, starts a dedicated listener that serves only
	// /metrics on the given port. The main API port will no longer expose
	// /metrics, allowing Prometheus scrape traffic to be separated from
	// operational reads and writes. When 0 (the default), /metrics is served
	// on the main port as before. Controlled by OLU_METRICS_PORT.
	MetricsPort int
	// MetricsHost sets the bind address for the dedicated metrics listener.
	// Only meaningful when MetricsPort > 0. When unset, the value is derived
	// from Host: if Host is a real address (not 0.0.0.0 or ::), it is
	// inherited; otherwise the metrics listener binds to 0.0.0.0. When
	// explicitly set, it always takes precedence. Controlled by OLU_METRICS_HOST.
	MetricsHost string

	// CORS
	// CORSOrigins lists allowed origins for cross-origin requests. Empty
	// disables CORS entirely. Use "*" for development only. When combined
	// with cookie-based auth, restrict to specific trusted domains — see
	// the security note on corsMiddleware in server.go.
	CORSOrigins []string

	// Performance tuning — SQLite
	SQLiteMaxOpenConns        int // Max open write connections (0 = backend default)
	SQLiteMaxIdleConns        int // Max idle write connections (0 = backend default)
	SQLiteReadPoolSize        int // Max open read connections (0 = backend default)
	SQLiteContentionThreshold int // Adaptive lock threshold 0-100 (0 = disabled, 95 = default)
	SQLiteBusyTimeout         int // Milliseconds to wait on locked database (0 = use default 5000)
	SQLiteCacheSize           int // Page cache size in KB (0 = use default 2000)

	// Performance tuning — query planner
	// PerformanceProfile selects hardware-specific thresholds for the
	// query planner's push-down decisions. Accepted values:
	//   "auto"      - Run a ~200ms startup micro-benchmark to calibrate (default)
	//   "edge"      - ARM SBCs, gateways (1-2 cores, 1-4 GB RAM)
	//   "vps"       - Small cloud instances (1-2 vCPU, 2-8 GB RAM)
	//   "dedicated" - Bare metal or large instances (4+ cores, 16+ GB)
	PerformanceProfile string

	// Performance tuning — Redis
	RedisPoolSize     int // Redis connection pool size (0 = use default 50)
	RedisMinIdleConns int // Redis minimum idle connections (0 = use default 10)

	// Performance tuning — HTTP server
	HTTPReadTimeout    int // Seconds; max duration for reading request (0 = no timeout)
	HTTPWriteTimeout   int // Seconds; max duration for writing response (0 = no timeout)
	HTTPIdleTimeout    int // Seconds; max duration for keep-alive idle (0 = no timeout)
	HTTPRequestTimeout int // Seconds; per-request middleware timeout (0 = use default 60)

	// Multi-tenancy
	// TenantMode controls tenant isolation behaviour:
	//   "path"   - Tenant routes available with auto-registration, non-tenant routes
	//              use tenant 0 (default)
	//   "strict" - All entity requests require tenant context; non-tenant routes
	//              return 403; tenants must be pre-registered
	TenantMode string

	// TenantAutoRegister controls whether unknown tenant names are automatically
	// registered on first access. When true, any request to /api/v1/tenant/{name}/...
	// will create the tenant if it doesn't exist. When false (default), unknown
	// tenants return 404. Ignored when TenantMode is "strict".
	TenantAutoRegister bool

	// Timeseries storage (Pebble-backed, requires StorageType = "sqlite")
	TimeseriesEnabled        bool
	TSMemtableSize           int    // bytes, default 67108864 (64 MB)
	TSBlockSize              int    // bytes, default 32768 (32 KB)
	TSCompression            string // "snappy", "zstd", or "none"
	TSL0CompactionThreshold  int    // L0 files before compaction trigger, default 4
	TSMaxOpenFiles           int    // per-tenant Pebble file limit, default 500
	TSDefaultRetentionDays   int    // default 90
	TSCompactionIntervalSecs int    // retention sweep interval in seconds, default 3600
	TSRetentionEnabled       bool   // run background retention goroutine, default false

	// Timeseries query guardrails
	TSQueryTimeoutSecs      int // max execution time per query (0 = default 30s)
	TSMaxQueryEvents        int // max events returned by QueryRange/Latest (0 = default 10000)
	TSMaxScanEvents         int // max events scanned before aborting (0 = default 500000)
	TSMaxRangeDays          int // max From→To window in days (0 = default 366)
	TSMaxBatchSize          int // max events per batch append (0 = default 5000)
	TSMaxResponseBytes      int // max JSON response size in bytes (0 = default 10MB)
	TSMaxAggregateBuckets   int // max buckets in a windowed aggregate (0 = default 10000)
}

// readSecret returns the value of the named secret, preferring the
// environment variable (upper-cased name) over a Docker-style secret file.
//
// Resolution order:
//  1. Environment variable `strings.ToUpper(name)` — returned as-is if non-empty.
//  2. File `/run/secrets/<name>` — trailing newlines stripped.
//  3. Empty string if neither is set.
//
// This allows secrets to be supplied either via environment variables
// (development, CI) or via Docker/Compose secret mounts (production).
func readSecret(name string) string {
	if v := os.Getenv(strings.ToUpper(name)); v != "" {
		return v
	}
	data, err := os.ReadFile("/run/secrets/" + name)
	if err != nil {
		return ""
	}
	return strings.TrimRight(string(data), "\n\r")
}

// Default returns the default configuration
func Default() *Config {
	return &Config{
		Host:                "0.0.0.0",
		Port:                9090,
		StorageType:         "jsonfile",
		BaseDir:             "data",
		SchemaDir:           "schema",
		Schema:              "default",
		DBPath:              "olu.db",
		CacheType:           "memory",
		CacheTTL:            300,
		CacheSize:           1024,
		CacheShards:         16,
		RedisHost:           "localhost",
		RedisPort:           6379,
		GraphEnabled:        true,
		GraphMode:           "flat",
		GraphDataFile:       "graph.data",
		GraphIndexFile:      "graph.index",
		GraphQueryTTL:       86400,
		GraphResultTTL:      3600,
		GraphMaxVisitedNodes: 10000,
		GraphMaxResults:      10000,
		GraphCycleDetection:  "warn",
		GraphCycleCheckLimit: 0, // 0 means use the graph layer default (512)
		FullTextEnabled:     false,
		StrictCommit:        true,
		MaxQueryDepth:       10,
		MaxEmbedDepth:       10,
		RefEmbedDepth:       3,
		DefaultPageSize:     10,
		QueryTimeout:         30,       // 30 seconds
		QueryMaxRows:         10000,    // 10k rows max returned
		QueryMaxScanRows:     100000,   // 100k rows max scanned
		QueryMaxResponseBytes: 10485760, // 10 MB
		PatchNullBehavior:   "store",
		MaxEntitySize:       1048576, // 1MB
		CascadingDelete:     false,
		MaxCascadeDeletions: 10000,
		MaxCascadeWork:      100000,
		Debug:               false,
		DebugLocks:          false,
		LogLevel:            "info",
		AuthType:            "none",
		JWTSecret:           "",
		JWTIssuer:           "",
		APIKeys:             []string{},
		AuthExcludePaths:    []string{"/health", "/ready", "/version", "/metrics"},
		RateLimitEnabled:    false,
		RateLimitRate:       100,
		RateLimitWindow:     60,
		RateLimitByIP:       true,
		RateLimitByKey:      false,
		MetricsEnabled:      true,
		CORSOrigins:         []string{},
		TenantMode:          "path",
		TimeseriesEnabled:        false,
		TSMemtableSize:           67108864, // 64 MB
		TSBlockSize:              32768,    // 32 KB
		TSCompression:            "zstd",
		TSL0CompactionThreshold:  4,
		TSMaxOpenFiles:           500,
		TSDefaultRetentionDays:   90,
		TSCompactionIntervalSecs: 3600,
		TSRetentionEnabled:       false,
		TSQueryTimeoutSecs:      30,
		TSMaxQueryEvents:        10000,
		TSMaxScanEvents:         500000,
		TSMaxRangeDays:          366,
		TSMaxBatchSize:          5000,
		TSMaxResponseBytes:      10485760, // 10 MB
		TSMaxAggregateBuckets:   10000,
		SQLiteMaxOpenConns:        0, // 0 = backend default (1 for SQLite, higher for Postgres)
		SQLiteMaxIdleConns:        0, // 0 = backend default
		SQLiteReadPoolSize:        0, // 0 = backend default (NumCPU for SQLite)
		SQLiteContentionThreshold: 95,
		SQLiteBusyTimeout:         5000,
		SQLiteCacheSize:           2000,
		PerformanceProfile:        "auto",
		RedisPoolSize:             50,
		RedisMinIdleConns:         10,
		HTTPReadTimeout:           0,
		HTTPWriteTimeout:          0,
		HTTPIdleTimeout:           0,
		HTTPRequestTimeout:        60,
	}
}

// LoadFromEnv loads configuration from environment variables.
// All environment variables use the OLU_ prefix.
func LoadFromEnv(cfg *Config) {
	// OLU_ADDR is a convenience alias for setting host and port together
	// as a single "host:port" value. OLU_HOST and OLU_PORT take precedence
	// if set, so existing deployments are unaffected.
	if val := os.Getenv("OLU_ADDR"); val != "" {
		if h, p, err := net.SplitHostPort(val); err == nil {
			cfg.Host = h
			if port, err := strconv.Atoi(p); err == nil {
				cfg.Port = port
			}
		}
	}
	if val := os.Getenv("OLU_HOST"); val != "" {
		cfg.Host = val
	}
	if val := os.Getenv("OLU_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.Port = port
		}
	}
	if val := os.Getenv("OLU_STORAGE_TYPE"); val != "" {
		cfg.StorageType = val
	}
	if val := os.Getenv("OLU_DB_PATH"); val != "" {
		cfg.DBPath = val
	}
	// OLU_SQLITE_PATH is an alias for OLU_DB_PATH; takes precedence if both set.
	if val := os.Getenv("OLU_SQLITE_PATH"); val != "" {
		cfg.DBPath = val
	}
	if val := os.Getenv("OLU_BASE_DIR"); val != "" {
		cfg.BaseDir = val
	}
	if val := os.Getenv("OLU_SCHEMA_DIR"); val != "" {
		cfg.SchemaDir = val
	}
	if val := os.Getenv("OLU_SCHEMA_NAME"); val != "" {
		cfg.Schema = val
	}
	if val := os.Getenv("OLU_CACHE_TYPE"); val != "" {
		cfg.CacheType = val
	}
	if val := os.Getenv("OLU_CACHE_TTL"); val != "" {
		if ttl, err := strconv.Atoi(val); err == nil {
			cfg.CacheTTL = ttl
		}
	}
	if val := os.Getenv("OLU_CACHE_SHARDS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.CacheShards = n
		}
	}
	if val := os.Getenv("OLU_REDIS_HOST"); val != "" {
		cfg.RedisHost = val
	}
	if val := os.Getenv("OLU_REDIS_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.RedisPort = port
		}
	}
	if val := os.Getenv("OLU_GRAPH_MODE"); val != "" {
		cfg.GraphMode = val
		cfg.GraphEnabled = val != "disabled"
	}
	if val := os.Getenv("OLU_GRAPH_CYCLE_DETECTION"); val != "" {
		cfg.GraphCycleDetection = val
	}
	if val := os.Getenv("OLU_GRAPH_CYCLE_CHECK_LIMIT"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.GraphCycleCheckLimit = v
		}
	}
	if val := os.Getenv("OLU_GRAPH_MAX_VISITED_NODES"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.GraphMaxVisitedNodes = v
		}
	}
	if val := os.Getenv("OLU_GRAPH_MAX_RESULTS"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.GraphMaxResults = v
		}
	}
	if val := os.Getenv("OLU_FULLTEXT_ENABLED"); val != "" {
		cfg.FullTextEnabled = parseBool(val)
	}
	if val := os.Getenv("OLU_STRICT_COMMIT"); val != "" {
		cfg.StrictCommit = parseBool(val)
	}
	if val := os.Getenv("OLU_CASCADING_DELETE"); val != "" {
		cfg.CascadingDelete = parseBool(val)
	}
	if val := os.Getenv("OLU_DEBUG"); val != "" {
		cfg.Debug = parseBool(val)
		// Legacy compat: OLU_DEBUG=true maps to log level "debug".
		// OLU_LOG_LEVEL (read below) takes precedence if set.
		if cfg.Debug {
			cfg.LogLevel = "debug"
		}
	}
	// OLU_LOG_LEVEL accepts: debug, info, warn, error.
	// Takes precedence over the OLU_DEBUG legacy alias.
	if val := strings.ToLower(strings.TrimSpace(os.Getenv("OLU_LOG_LEVEL"))); val != "" {
		switch val {
		case "debug", "info", "warn", "error":
			cfg.LogLevel = val
		}
	}
	if val := os.Getenv("OLU_DEBUG_LOCKS"); val != "" {
		cfg.DebugLocks = parseBool(val)
	}
	if val := os.Getenv("OLU_REF_EMBED_DEPTH"); val != "" {
		if depth, err := strconv.Atoi(val); err == nil {
			cfg.RefEmbedDepth = depth
		}
	}
	if val := os.Getenv("OLU_MAX_ENTITY_SIZE"); val != "" {
		if size, err := strconv.Atoi(val); err == nil {
			cfg.MaxEntitySize = size
		}
	}
	if val := os.Getenv("OLU_QUERY_TIMEOUT"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.QueryTimeout = v
		}
	}
	if val := os.Getenv("OLU_QUERY_MAX_ROWS"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.QueryMaxRows = v
		}
	}
	if val := os.Getenv("OLU_QUERY_MAX_SCAN_ROWS"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.QueryMaxScanRows = v
		}
	}
	if val := os.Getenv("OLU_QUERY_MAX_RESPONSE_BYTES"); val != "" {
		if v, err := strconv.Atoi(val); err == nil {
			cfg.QueryMaxResponseBytes = v
		}
	}
	if val := os.Getenv("OLU_PATCH_NULL"); val != "" {
		cfg.PatchNullBehavior = val
	}

	// Authentication settings
	if val := os.Getenv("OLU_AUTH_TYPE"); val != "" {
		cfg.AuthType = val
	}
	if val := readSecret("olu_jwt_secret"); val != "" {
		cfg.JWTSecret = val
	}
	if val := os.Getenv("OLU_JWT_ISSUER"); val != "" {
		cfg.JWTIssuer = val
	}
	if val := os.Getenv("OLU_API_KEYS"); val != "" {
		cfg.APIKeys = strings.Split(val, ",")
		for i := range cfg.APIKeys {
			cfg.APIKeys[i] = strings.TrimSpace(cfg.APIKeys[i])
		}
	}
	if val := readSecret("olu_internal_token"); val != "" {
		cfg.InternalToken = val
	}

	// Rate limiting settings
	if val := os.Getenv("OLU_RATE_LIMIT_ENABLED"); val != "" {
		cfg.RateLimitEnabled = parseBool(val)
	}
	if val := os.Getenv("OLU_RATE_LIMIT_RATE"); val != "" {
		if rate, err := strconv.Atoi(val); err == nil {
			cfg.RateLimitRate = rate
		}
	}
	if val := os.Getenv("OLU_RATE_LIMIT_WINDOW"); val != "" {
		if window, err := strconv.Atoi(val); err == nil {
			cfg.RateLimitWindow = window
		}
	}
	if val := os.Getenv("OLU_RATE_LIMIT_BY_IP"); val != "" {
		cfg.RateLimitByIP = parseBool(val)
	}
	if val := os.Getenv("OLU_RATE_LIMIT_BY_KEY"); val != "" {
		cfg.RateLimitByKey = parseBool(val)
	}
	if val := os.Getenv("OLU_METRICS_ENABLED"); val != "" {
		cfg.MetricsEnabled = parseBool(val)
	}
	// OLU_METRICS_ADDR is a convenience alias for OLU_METRICS_HOST + OLU_METRICS_PORT.
	// OLU_METRICS_HOST and OLU_METRICS_PORT take precedence if also set.
	if val := os.Getenv("OLU_METRICS_ADDR"); val != "" {
		if h, p, err := net.SplitHostPort(val); err == nil {
			cfg.MetricsHost = h
			if port, err := strconv.Atoi(p); err == nil {
				cfg.MetricsPort = port
			}
		}
	}
	if val := os.Getenv("OLU_METRICS_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil {
			cfg.MetricsPort = port
		}
	}
	if val := os.Getenv("OLU_METRICS_HOST"); val != "" {
		cfg.MetricsHost = val
	}
	if val := os.Getenv("OLU_CORS_ORIGINS"); val != "" {
		origins := strings.Split(val, ",")
		for i := range origins {
			origins[i] = strings.TrimSpace(origins[i])
		}
		cfg.CORSOrigins = origins
	}

	// Tenant mode
	if val := os.Getenv("OLU_TENANT_MODE"); val != "" {
		cfg.TenantMode = val
	}
	if val := os.Getenv("OLU_TENANT_AUTO_REGISTER"); val != "" {
		cfg.TenantAutoRegister = parseBool(val)
	}

	// Timeseries
	if val := os.Getenv("OLU_TIMESERIES_ENABLED"); val != "" {
		cfg.TimeseriesEnabled = parseBool(val)
	}
	if val := os.Getenv("OLU_TS_MEMTABLE_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMemtableSize = n
		}
	}
	if val := os.Getenv("OLU_TS_BLOCK_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSBlockSize = n
		}
	}
	if val := os.Getenv("OLU_TS_COMPRESSION"); val != "" {
		cfg.TSCompression = val
	}
	if val := os.Getenv("OLU_TS_L0_COMPACTION_THRESHOLD"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSL0CompactionThreshold = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_OPEN_FILES"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxOpenFiles = n
		}
	}
	if val := os.Getenv("OLU_TS_DEFAULT_RETENTION_DAYS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSDefaultRetentionDays = n
		}
	}
	if val := os.Getenv("OLU_TS_COMPACTION_INTERVAL"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSCompactionIntervalSecs = n
		}
	}
	if val := os.Getenv("OLU_TS_RETENTION_ENABLED"); val != "" {
		cfg.TSRetentionEnabled = parseBool(val)
	}
	if val := os.Getenv("OLU_TS_QUERY_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSQueryTimeoutSecs = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_QUERY_EVENTS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxQueryEvents = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_SCAN_EVENTS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxScanEvents = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_RANGE_DAYS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxRangeDays = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_BATCH_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxBatchSize = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_RESPONSE_BYTES"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxResponseBytes = n
		}
	}
	if val := os.Getenv("OLU_TS_MAX_AGGREGATE_BUCKETS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.TSMaxAggregateBuckets = n
		}
	}

	// Performance tuning — SQLite
	if val := os.Getenv("OLU_SQLITE_MAX_OPEN_CONNS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.SQLiteMaxOpenConns = n
		}
	}
	if val := os.Getenv("OLU_SQLITE_MAX_IDLE_CONNS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.SQLiteMaxIdleConns = n
		}
	}
	if val := os.Getenv("OLU_SQLITE_READ_POOL_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.SQLiteReadPoolSize = n
		}
	}
	if val := os.Getenv("OLU_SQLITE_CONTENTION_THRESHOLD"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.SQLiteContentionThreshold = n
		}
	}
	if val := os.Getenv("OLU_SQLITE_BUSY_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.SQLiteBusyTimeout = n
		}
	}
	if val := os.Getenv("OLU_SQLITE_CACHE_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.SQLiteCacheSize = n
		}
	}

	// Performance tuning — query planner
	if val := os.Getenv("OLU_PERFORMANCE_PROFILE"); val != "" {
		cfg.PerformanceProfile = val
	}

	// Performance tuning — Redis
	if val := os.Getenv("OLU_REDIS_POOL_SIZE"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.RedisPoolSize = n
		}
	}
	if val := os.Getenv("OLU_REDIS_MIN_IDLE_CONNS"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.RedisMinIdleConns = n
		}
	}

	// Performance tuning — HTTP server
	if val := os.Getenv("OLU_HTTP_READ_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.HTTPReadTimeout = n
		}
	}
	if val := os.Getenv("OLU_HTTP_WRITE_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.HTTPWriteTimeout = n
		}
	}
	if val := os.Getenv("OLU_HTTP_IDLE_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.HTTPIdleTimeout = n
		}
	}
	if val := os.Getenv("OLU_HTTP_REQUEST_TIMEOUT"); val != "" {
		if n, err := strconv.Atoi(val); err == nil {
			cfg.HTTPRequestTimeout = n
		}
	}
}
func parseBool(val string) bool {
	val = strings.ToLower(val)
	return val == "true" || val == "1" || val == "yes"
}

// Validate checks the configuration for invalid or inconsistent values.
// It returns hard errors (config is broken) and warnings (config is
// suspicious but functional) as separate slices.
func (c *Config) Validate() (errs []string, warnings []string) {

	check := func(field, value string, allowed []string) {
		for _, a := range allowed {
			if value == a {
				return
			}
		}
		errs = append(errs, fmt.Sprintf("%s: %q is not valid (must be one of: %s)",
			field, value, strings.Join(allowed, ", ")))
	}

	check("StorageType", c.StorageType, []string{"jsonfile", "sqlite"})
	check("CacheType", c.CacheType, []string{"memory", "redis"})
	check("GraphMode", c.GraphMode, []string{"flat", "disabled"})
	check("GraphCycleDetection", c.GraphCycleDetection, []string{"warn", "error", "ignore"})
	check("PatchNullBehavior", c.PatchNullBehavior, []string{"store", "delete"})
	check("AuthType", c.AuthType, []string{"none", "jwt", "apikey", "bearertoken"})
	// Accept "none" as deprecated alias for "path"
	if c.TenantMode == "none" {
		c.TenantMode = "path"
	}
	check("TenantMode", c.TenantMode, []string{"path", "strict"})

	if c.MaxEntitySize <= 0 {
		errs = append(errs, "MaxEntitySize must be > 0")
	}
	if c.GraphCycleCheckLimit < 0 {
		errs = append(errs, "GraphCycleCheckLimit must be >= 0 (0 = use built-in default)")
	}
	if c.CacheTTL < 0 {
		errs = append(errs, "CacheTTL must be >= 0")
	}
	if c.Port < 0 || c.Port > 65535 {
		errs = append(errs, fmt.Sprintf("Port: %d is out of range (0-65535)", c.Port))
	}
	if c.MetricsPort < 0 || c.MetricsPort > 65535 {
		errs = append(errs, fmt.Sprintf("MetricsPort: %d is out of range (0-65535)", c.MetricsPort))
	}
	if c.MetricsPort > 0 && c.MetricsPort == c.Port {
		errs = append(errs, fmt.Sprintf("MetricsPort (%d) must differ from Port (%d)", c.MetricsPort, c.Port))
	}
	if c.MaxQueryDepth <= 0 {
		errs = append(errs, "MaxQueryDepth must be > 0")
	}
	if c.MaxEmbedDepth <= 0 {
		errs = append(errs, "MaxEmbedDepth must be > 0")
	}
	if c.RefEmbedDepth < 0 {
		errs = append(errs, "RefEmbedDepth must be >= 0")
	}
	if c.RateLimitEnabled {
		if c.RateLimitRate <= 0 {
			errs = append(errs, "RateLimitRate must be > 0 when rate limiting is enabled")
		}
		if c.RateLimitWindow <= 0 {
			errs = append(errs, "RateLimitWindow must be > 0 when rate limiting is enabled")
		}
	}
	if c.AuthType == "jwt" && c.JWTSecret == "" {
		errs = append(errs, "JWTSecret is required when AuthType is \"jwt\"")
	}
	if c.AuthType == "apikey" && len(c.APIKeys) == 0 {
		errs = append(errs, "APIKeys must not be empty when AuthType is \"apikey\"")
	}
	if c.AuthType == "bearertoken" && c.InternalToken == "" {
		errs = append(errs, "InternalToken (OLU_INTERNAL_TOKEN) is required when AuthType is \"bearertoken\"")
	}
	if c.CacheType == "redis" && c.RedisHost == "" {
		errs = append(errs, "RedisHost is required when CacheType is \"redis\"")
	}

	// Performance tuning — range validation
	if c.SQLiteMaxOpenConns < 0 {
		errs = append(errs, "SQLiteMaxOpenConns must be >= 0")
	}
	if c.SQLiteMaxIdleConns < 0 {
		errs = append(errs, "SQLiteMaxIdleConns must be >= 0")
	}
	if c.SQLiteReadPoolSize < 0 {
		errs = append(errs, "SQLiteReadPoolSize must be >= 0")
	}
	if c.SQLiteContentionThreshold < 0 || c.SQLiteContentionThreshold > 100 {
		errs = append(errs, "SQLiteContentionThreshold must be 0-100")
	}
	if c.SQLiteBusyTimeout < 0 {
		errs = append(errs, "SQLiteBusyTimeout must be >= 0")
	}
	if c.SQLiteCacheSize < 0 {
		errs = append(errs, "SQLiteCacheSize must be >= 0")
	}
	switch strings.ToLower(c.PerformanceProfile) {
	case "auto", "edge", "vps", "dedicated", "":
		// valid
	default:
		errs = append(errs, fmt.Sprintf("PerformanceProfile %q is not valid; use auto, edge, vps, or dedicated", c.PerformanceProfile))
	}
	if c.RedisPoolSize < 0 {
		errs = append(errs, "RedisPoolSize must be >= 0")
	}
	if c.RedisMinIdleConns < 0 {
		errs = append(errs, "RedisMinIdleConns must be >= 0")
	}
	if c.HTTPReadTimeout < 0 {
		errs = append(errs, "HTTPReadTimeout must be >= 0")
	}
	if c.HTTPWriteTimeout < 0 {
		errs = append(errs, "HTTPWriteTimeout must be >= 0")
	}
	if c.HTTPIdleTimeout < 0 {
		errs = append(errs, "HTTPIdleTimeout must be >= 0")
	}
	if c.HTTPRequestTimeout < 0 {
		errs = append(errs, "HTTPRequestTimeout must be >= 0")
	}

	// Cross-field consistency warnings
	if c.SQLiteMaxIdleConns > 0 && c.SQLiteMaxOpenConns > 0 && c.SQLiteMaxIdleConns > c.SQLiteMaxOpenConns {
		warnings = append(warnings, fmt.Sprintf(
			"SQLiteMaxIdleConns (%d) > SQLiteMaxOpenConns (%d); idle will be clamped to max open",
			c.SQLiteMaxIdleConns, c.SQLiteMaxOpenConns))
	}
	if c.RedisMinIdleConns > 0 && c.RedisPoolSize > 0 && c.RedisMinIdleConns > c.RedisPoolSize {
		warnings = append(warnings, fmt.Sprintf(
			"RedisMinIdleConns (%d) > RedisPoolSize (%d); min idle will be clamped to pool size",
			c.RedisMinIdleConns, c.RedisPoolSize))
	}
	if c.StorageType != "sqlite" {
		hasSQLiteTuning := c.SQLiteMaxOpenConns != 0 || c.SQLiteMaxIdleConns != 0 || c.SQLiteReadPoolSize != 0 ||
			c.SQLiteContentionThreshold != 95 || c.SQLiteBusyTimeout != 5000 || c.SQLiteCacheSize != 2000
		if hasSQLiteTuning {
			warnings = append(warnings, "SQLite tuning parameters set but StorageType is not \"sqlite\"; they will be ignored")
		}
	}
	if c.CacheType != "redis" {
		hasRedisTuning := c.RedisPoolSize != 50 || c.RedisMinIdleConns != 10
		if hasRedisTuning {
			warnings = append(warnings, "Redis tuning parameters set but CacheType is not \"redis\"; they will be ignored")
		}
	}
	if c.HTTPWriteTimeout > 0 && c.HTTPRequestTimeout > 0 && c.HTTPRequestTimeout > c.HTTPWriteTimeout {
		warnings = append(warnings, fmt.Sprintf(
			"HTTPRequestTimeout (%ds) > HTTPWriteTimeout (%ds); the write timeout will close connections before the request timeout fires",
			c.HTTPRequestTimeout, c.HTTPWriteTimeout))
	}
	if c.RefEmbedDepth > 0 && c.MaxEmbedDepth > 0 && c.RefEmbedDepth > c.MaxEmbedDepth {
		warnings = append(warnings, fmt.Sprintf(
			"RefEmbedDepth (%d) > MaxEmbedDepth (%d); ref embedding will be capped at MaxEmbedDepth",
			c.RefEmbedDepth, c.MaxEmbedDepth))
	}
	if c.CacheType == "memory" && c.CacheSize > 0 && c.CacheSize < 16 {
		warnings = append(warnings, fmt.Sprintf(
			"CacheSize (%d) is very small for in-memory cache; this may cause excessive evictions",
			c.CacheSize))
	}
	if c.CacheShards < 0 {
		errs = append(errs, "CacheShards must be >= 0")
	}

	// Timeseries validation
	if c.TimeseriesEnabled {
		if c.StorageType != "sqlite" {
			errs = append(errs, "TimeseriesEnabled requires StorageType \"sqlite\"")
		}
		check("TSCompression", c.TSCompression, []string{"snappy", "zstd", "none"})
		if c.TSMemtableSize <= 0 {
			errs = append(errs, "TSMemtableSize must be > 0")
		}
		if c.TSBlockSize <= 0 {
			errs = append(errs, "TSBlockSize must be > 0")
		}
		if c.TSL0CompactionThreshold <= 0 {
			errs = append(errs, "TSL0CompactionThreshold must be > 0")
		}
		if c.TSMaxOpenFiles <= 0 {
			errs = append(errs, "TSMaxOpenFiles must be > 0")
		}
		if c.TSDefaultRetentionDays <= 0 {
			errs = append(errs, "TSDefaultRetentionDays must be > 0")
		}
		if c.TSCompactionIntervalSecs <= 0 {
			errs = append(errs, "TSCompactionIntervalSecs must be > 0")
		}
	}
	if !c.TimeseriesEnabled {
		hasTSTuning := c.TSCompression != "zstd" || c.TSMemtableSize != 67108864 ||
			c.TSBlockSize != 32768 || c.TSL0CompactionThreshold != 4 ||
			c.TSMaxOpenFiles != 500 || c.TSDefaultRetentionDays != 90 ||
			c.TSCompactionIntervalSecs != 3600 || c.TSRetentionEnabled
		if hasTSTuning {
			warnings = append(warnings, "Timeseries tuning parameters set but TimeseriesEnabled is false; they will be ignored")
		}
	}

	return errs, warnings
}
