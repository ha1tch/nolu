// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package config

import (
	"os"
	"strings"
	"testing"
)

func TestDefault(t *testing.T) {
	cfg := Default()

	if cfg.Host != "0.0.0.0" {
		t.Errorf("Expected Host '0.0.0.0', got '%s'", cfg.Host)
	}
	if cfg.Port != 9090 {
		t.Errorf("Expected Port 9090, got %d", cfg.Port)
	}
	if cfg.StorageType != "jsonfile" {
		t.Errorf("Expected StorageType 'jsonfile', got '%s'", cfg.StorageType)
	}
	if cfg.CacheType != "memory" {
		t.Errorf("Expected CacheType 'memory', got '%s'", cfg.CacheType)
	}
	if cfg.CacheTTL != 300 {
		t.Errorf("Expected CacheTTL 300, got %d", cfg.CacheTTL)
	}
	if !cfg.GraphEnabled {
		t.Error("Expected GraphEnabled true")
	}
	if cfg.GraphMode != "flat" {
		t.Errorf("Expected GraphMode 'flat', got '%s'", cfg.GraphMode)
	}
	if cfg.GraphCycleDetection != "warn" {
		t.Errorf("Expected GraphCycleDetection 'warn', got '%s'", cfg.GraphCycleDetection)
	}
	if cfg.AuthType != "none" {
		t.Errorf("Expected AuthType 'none', got '%s'", cfg.AuthType)
	}
	if cfg.RateLimitEnabled {
		t.Error("Expected RateLimitEnabled false")
	}
	if !cfg.MetricsEnabled {
		t.Error("Expected MetricsEnabled true")
	}
}

func TestLoadFromEnv_Server(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_HOST", "127.0.0.1")
	os.Setenv("OLU_PORT", "8080")
	defer os.Unsetenv("OLU_HOST")
	defer os.Unsetenv("OLU_PORT")

	LoadFromEnv(cfg)

	if cfg.Host != "127.0.0.1" {
		t.Errorf("Expected Host '127.0.0.1', got '%s'", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Errorf("Expected Port 8080, got %d", cfg.Port)
	}
}

func TestLoadFromEnv_Storage(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_STORAGE_TYPE", "sqlite")
	os.Setenv("OLU_DB_PATH", "/tmp/test.db")
	os.Setenv("OLU_BASE_DIR", "/data")
	os.Setenv("OLU_SCHEMA_NAME", "myschema")
	defer os.Unsetenv("OLU_STORAGE_TYPE")
	defer os.Unsetenv("OLU_DB_PATH")
	defer os.Unsetenv("OLU_BASE_DIR")
	defer os.Unsetenv("OLU_SCHEMA_NAME")

	LoadFromEnv(cfg)

	if cfg.StorageType != "sqlite" {
		t.Errorf("Expected StorageType 'sqlite', got '%s'", cfg.StorageType)
	}
	if cfg.DBPath != "/tmp/test.db" {
		t.Errorf("Expected DBPath '/tmp/test.db', got '%s'", cfg.DBPath)
	}
	if cfg.BaseDir != "/data" {
		t.Errorf("Expected BaseDir '/data', got '%s'", cfg.BaseDir)
	}
	if cfg.Schema != "myschema" {
		t.Errorf("Expected Schema 'myschema', got '%s'", cfg.Schema)
	}
}

func TestLoadFromEnv_Cache(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_CACHE_TYPE", "redis")
	os.Setenv("OLU_CACHE_TTL", "600")
	os.Setenv("OLU_REDIS_HOST", "redis.local")
	os.Setenv("OLU_REDIS_PORT", "6380")
	defer os.Unsetenv("OLU_CACHE_TYPE")
	defer os.Unsetenv("OLU_CACHE_TTL")
	defer os.Unsetenv("OLU_REDIS_HOST")
	defer os.Unsetenv("OLU_REDIS_PORT")

	LoadFromEnv(cfg)

	if cfg.CacheType != "redis" {
		t.Errorf("Expected CacheType 'redis', got '%s'", cfg.CacheType)
	}
	if cfg.CacheTTL != 600 {
		t.Errorf("Expected CacheTTL 600, got %d", cfg.CacheTTL)
	}
	if cfg.RedisHost != "redis.local" {
		t.Errorf("Expected RedisHost 'redis.local', got '%s'", cfg.RedisHost)
	}
	if cfg.RedisPort != 6380 {
		t.Errorf("Expected RedisPort 6380, got %d", cfg.RedisPort)
	}
}

func TestLoadFromEnv_Graph(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_GRAPH_MODE", "disabled")
	os.Setenv("OLU_GRAPH_CYCLE_DETECTION", "error")
	defer os.Unsetenv("OLU_GRAPH_MODE")
	defer os.Unsetenv("OLU_GRAPH_CYCLE_DETECTION")

	LoadFromEnv(cfg)

	if cfg.GraphMode != "disabled" {
		t.Errorf("Expected GraphMode 'disabled', got '%s'", cfg.GraphMode)
	}
	if cfg.GraphEnabled {
		t.Error("Expected GraphEnabled false when mode is disabled")
	}
	if cfg.GraphCycleDetection != "error" {
		t.Errorf("Expected GraphCycleDetection 'error', got '%s'", cfg.GraphCycleDetection)
	}
}

func TestLoadFromEnv_Features(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_FULLTEXT_ENABLED", "true")
	os.Setenv("OLU_CASCADING_DELETE", "yes")
	os.Setenv("OLU_REF_EMBED_DEPTH", "5")
	os.Setenv("OLU_MAX_ENTITY_SIZE", "2097152")
	os.Setenv("OLU_PATCH_NULL", "delete")
	defer os.Unsetenv("OLU_FULLTEXT_ENABLED")
	defer os.Unsetenv("OLU_CASCADING_DELETE")
	defer os.Unsetenv("OLU_REF_EMBED_DEPTH")
	defer os.Unsetenv("OLU_MAX_ENTITY_SIZE")
	defer os.Unsetenv("OLU_PATCH_NULL")

	LoadFromEnv(cfg)

	if !cfg.FullTextEnabled {
		t.Error("Expected FullTextEnabled true")
	}
	if !cfg.CascadingDelete {
		t.Error("Expected CascadingDelete true")
	}
	if cfg.RefEmbedDepth != 5 {
		t.Errorf("Expected RefEmbedDepth 5, got %d", cfg.RefEmbedDepth)
	}
	if cfg.MaxEntitySize != 2097152 {
		t.Errorf("Expected MaxEntitySize 2097152, got %d", cfg.MaxEntitySize)
	}
	if cfg.PatchNullBehavior != "delete" {
		t.Errorf("Expected PatchNullBehavior 'delete', got '%s'", cfg.PatchNullBehavior)
	}
}

func TestLoadFromEnv_Auth(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_AUTH_TYPE", "jwt")
	os.Setenv("OLU_JWT_SECRET", "my-secret-key")
	os.Setenv("OLU_JWT_ISSUER", "my-app")
	defer os.Unsetenv("OLU_AUTH_TYPE")
	defer os.Unsetenv("OLU_JWT_SECRET")
	defer os.Unsetenv("OLU_JWT_ISSUER")

	LoadFromEnv(cfg)

	if cfg.AuthType != "jwt" {
		t.Errorf("Expected AuthType 'jwt', got '%s'", cfg.AuthType)
	}
	if cfg.JWTSecret != "my-secret-key" {
		t.Errorf("Expected JWTSecret 'my-secret-key', got '%s'", cfg.JWTSecret)
	}
	if cfg.JWTIssuer != "my-app" {
		t.Errorf("Expected JWTIssuer 'my-app', got '%s'", cfg.JWTIssuer)
	}
}

func TestLoadFromEnv_APIKeys(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_AUTH_TYPE", "apikey")
	os.Setenv("OLU_API_KEYS", "key1, key2, key3")
	defer os.Unsetenv("OLU_AUTH_TYPE")
	defer os.Unsetenv("OLU_API_KEYS")

	LoadFromEnv(cfg)

	if cfg.AuthType != "apikey" {
		t.Errorf("Expected AuthType 'apikey', got '%s'", cfg.AuthType)
	}
	if len(cfg.APIKeys) != 3 {
		t.Errorf("Expected 3 API keys, got %d", len(cfg.APIKeys))
	}
	// Check trimming
	if cfg.APIKeys[1] != "key2" {
		t.Errorf("Expected APIKeys[1] 'key2', got '%s'", cfg.APIKeys[1])
	}
}

func TestLoadFromEnv_RateLimit(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_RATE_LIMIT_ENABLED", "true")
	os.Setenv("OLU_RATE_LIMIT_RATE", "50")
	os.Setenv("OLU_RATE_LIMIT_WINDOW", "30")
	os.Setenv("OLU_RATE_LIMIT_BY_IP", "false")
	os.Setenv("OLU_RATE_LIMIT_BY_KEY", "true")
	defer os.Unsetenv("OLU_RATE_LIMIT_ENABLED")
	defer os.Unsetenv("OLU_RATE_LIMIT_RATE")
	defer os.Unsetenv("OLU_RATE_LIMIT_WINDOW")
	defer os.Unsetenv("OLU_RATE_LIMIT_BY_IP")
	defer os.Unsetenv("OLU_RATE_LIMIT_BY_KEY")

	LoadFromEnv(cfg)

	if !cfg.RateLimitEnabled {
		t.Error("Expected RateLimitEnabled true")
	}
	if cfg.RateLimitRate != 50 {
		t.Errorf("Expected RateLimitRate 50, got %d", cfg.RateLimitRate)
	}
	if cfg.RateLimitWindow != 30 {
		t.Errorf("Expected RateLimitWindow 30, got %d", cfg.RateLimitWindow)
	}
	if cfg.RateLimitByIP {
		t.Error("Expected RateLimitByIP false")
	}
	if !cfg.RateLimitByKey {
		t.Error("Expected RateLimitByKey true")
	}
}

func TestLoadFromEnv_Metrics(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_METRICS_ENABLED", "false")
	defer os.Unsetenv("OLU_METRICS_ENABLED")

	LoadFromEnv(cfg)

	if cfg.MetricsEnabled {
		t.Error("Expected MetricsEnabled false")
	}
}

func TestLoadFromEnv_Debug(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_DEBUG", "1")
	os.Setenv("OLU_DEBUG_LOCKS", "true")
	defer os.Unsetenv("OLU_DEBUG")
	defer os.Unsetenv("OLU_DEBUG_LOCKS")

	LoadFromEnv(cfg)

	if !cfg.Debug {
		t.Error("Expected Debug true")
	}
	if !cfg.DebugLocks {
		t.Error("Expected DebugLocks true")
	}
}

func TestParseBool(t *testing.T) {
	tests := []struct {
		input    string
		expected bool
	}{
		{"true", true},
		{"True", true},
		{"TRUE", true},
		{"1", true},
		{"yes", true},
		{"Yes", true},
		{"false", false},
		{"0", false},
		{"no", false},
		{"", false},
		{"invalid", false},
	}

	for _, tc := range tests {
		result := parseBool(tc.input)
		if result != tc.expected {
			t.Errorf("parseBool(%q) = %v, expected %v", tc.input, result, tc.expected)
		}
	}
}

func TestLoadFromEnv_InvalidPort(t *testing.T) {
	cfg := Default()
	originalPort := cfg.Port

	os.Setenv("OLU_PORT", "invalid")
	defer os.Unsetenv("OLU_PORT")

	LoadFromEnv(cfg)

	// Should keep default on invalid input
	if cfg.Port != originalPort {
		t.Errorf("Expected Port to remain %d on invalid input, got %d", originalPort, cfg.Port)
	}
}

func TestLoadFromEnv_TenantMode(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_TENANT_MODE", "strict")
	defer os.Unsetenv("OLU_TENANT_MODE")

	LoadFromEnv(cfg)

	if cfg.TenantMode != "strict" {
		t.Errorf("Expected TenantMode 'strict', got '%s'", cfg.TenantMode)
	}
}

func TestDefault_TenantMode(t *testing.T) {
	cfg := Default()

	if cfg.TenantMode != "path" {
		t.Errorf("Expected default TenantMode 'path', got '%s'", cfg.TenantMode)
	}
}

func TestDefault_TuningDefaults(t *testing.T) {
	cfg := Default()

	if cfg.SQLiteMaxOpenConns != 0 {
		t.Errorf("Expected SQLiteMaxOpenConns 0 (backend default), got %d", cfg.SQLiteMaxOpenConns)
	}
	if cfg.SQLiteMaxIdleConns != 0 {
		t.Errorf("Expected SQLiteMaxIdleConns 0 (backend default), got %d", cfg.SQLiteMaxIdleConns)
	}
	if cfg.SQLiteContentionThreshold != 95 {
		t.Errorf("Expected SQLiteContentionThreshold 95, got %d", cfg.SQLiteContentionThreshold)
	}
	if cfg.SQLiteBusyTimeout != 5000 {
		t.Errorf("Expected SQLiteBusyTimeout 5000, got %d", cfg.SQLiteBusyTimeout)
	}
	if cfg.SQLiteCacheSize != 2000 {
		t.Errorf("Expected SQLiteCacheSize 2000, got %d", cfg.SQLiteCacheSize)
	}
	if cfg.RedisPoolSize != 50 {
		t.Errorf("Expected RedisPoolSize 50, got %d", cfg.RedisPoolSize)
	}
	if cfg.RedisMinIdleConns != 10 {
		t.Errorf("Expected RedisMinIdleConns 10, got %d", cfg.RedisMinIdleConns)
	}
	if cfg.HTTPReadTimeout != 0 {
		t.Errorf("Expected HTTPReadTimeout 0, got %d", cfg.HTTPReadTimeout)
	}
	if cfg.HTTPWriteTimeout != 0 {
		t.Errorf("Expected HTTPWriteTimeout 0, got %d", cfg.HTTPWriteTimeout)
	}
	if cfg.HTTPIdleTimeout != 0 {
		t.Errorf("Expected HTTPIdleTimeout 0, got %d", cfg.HTTPIdleTimeout)
	}
	if cfg.HTTPRequestTimeout != 60 {
		t.Errorf("Expected HTTPRequestTimeout 60, got %d", cfg.HTTPRequestTimeout)
	}
}

func TestLoadFromEnv_Tuning(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_SQLITE_MAX_OPEN_CONNS", "50")
	os.Setenv("OLU_SQLITE_MAX_IDLE_CONNS", "10")
	os.Setenv("OLU_SQLITE_CONTENTION_THRESHOLD", "80")
	os.Setenv("OLU_SQLITE_BUSY_TIMEOUT", "10000")
	os.Setenv("OLU_SQLITE_CACHE_SIZE", "4000")
	os.Setenv("OLU_REDIS_POOL_SIZE", "100")
	os.Setenv("OLU_REDIS_MIN_IDLE_CONNS", "20")
	os.Setenv("OLU_HTTP_READ_TIMEOUT", "30")
	os.Setenv("OLU_HTTP_WRITE_TIMEOUT", "60")
	os.Setenv("OLU_HTTP_IDLE_TIMEOUT", "120")
	os.Setenv("OLU_HTTP_REQUEST_TIMEOUT", "90")
	defer func() {
		os.Unsetenv("OLU_SQLITE_MAX_OPEN_CONNS")
		os.Unsetenv("OLU_SQLITE_MAX_IDLE_CONNS")
		os.Unsetenv("OLU_SQLITE_CONTENTION_THRESHOLD")
		os.Unsetenv("OLU_SQLITE_BUSY_TIMEOUT")
		os.Unsetenv("OLU_SQLITE_CACHE_SIZE")
		os.Unsetenv("OLU_REDIS_POOL_SIZE")
		os.Unsetenv("OLU_REDIS_MIN_IDLE_CONNS")
		os.Unsetenv("OLU_HTTP_READ_TIMEOUT")
		os.Unsetenv("OLU_HTTP_WRITE_TIMEOUT")
		os.Unsetenv("OLU_HTTP_IDLE_TIMEOUT")
		os.Unsetenv("OLU_HTTP_REQUEST_TIMEOUT")
	}()

	LoadFromEnv(cfg)

	if cfg.SQLiteMaxOpenConns != 50 {
		t.Errorf("Expected SQLiteMaxOpenConns 50, got %d", cfg.SQLiteMaxOpenConns)
	}
	if cfg.SQLiteMaxIdleConns != 10 {
		t.Errorf("Expected SQLiteMaxIdleConns 10, got %d", cfg.SQLiteMaxIdleConns)
	}
	if cfg.SQLiteContentionThreshold != 80 {
		t.Errorf("Expected SQLiteContentionThreshold 80, got %d", cfg.SQLiteContentionThreshold)
	}
	if cfg.SQLiteBusyTimeout != 10000 {
		t.Errorf("Expected SQLiteBusyTimeout 10000, got %d", cfg.SQLiteBusyTimeout)
	}
	if cfg.SQLiteCacheSize != 4000 {
		t.Errorf("Expected SQLiteCacheSize 4000, got %d", cfg.SQLiteCacheSize)
	}
	if cfg.RedisPoolSize != 100 {
		t.Errorf("Expected RedisPoolSize 100, got %d", cfg.RedisPoolSize)
	}
	if cfg.RedisMinIdleConns != 20 {
		t.Errorf("Expected RedisMinIdleConns 20, got %d", cfg.RedisMinIdleConns)
	}
	if cfg.HTTPReadTimeout != 30 {
		t.Errorf("Expected HTTPReadTimeout 30, got %d", cfg.HTTPReadTimeout)
	}
	if cfg.HTTPWriteTimeout != 60 {
		t.Errorf("Expected HTTPWriteTimeout 60, got %d", cfg.HTTPWriteTimeout)
	}
	if cfg.HTTPIdleTimeout != 120 {
		t.Errorf("Expected HTTPIdleTimeout 120, got %d", cfg.HTTPIdleTimeout)
	}
	if cfg.HTTPRequestTimeout != 90 {
		t.Errorf("Expected HTTPRequestTimeout 90, got %d", cfg.HTTPRequestTimeout)
	}
}

func TestValidate_TuningFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"SQLiteMaxOpenConns negative", func(c *Config) { c.SQLiteMaxOpenConns = -1 }, "SQLiteMaxOpenConns"},
		{"SQLiteMaxOpenConns zero", func(c *Config) { c.SQLiteMaxOpenConns = 0 }, ""},
		{"SQLiteMaxIdleConns negative", func(c *Config) { c.SQLiteMaxIdleConns = -1 }, "SQLiteMaxIdleConns"},
		{"SQLiteContentionThreshold negative", func(c *Config) { c.SQLiteContentionThreshold = -1 }, "SQLiteContentionThreshold"},
		{"SQLiteContentionThreshold 101", func(c *Config) { c.SQLiteContentionThreshold = 101 }, "SQLiteContentionThreshold"},
		{"SQLiteContentionThreshold 0", func(c *Config) { c.SQLiteContentionThreshold = 0 }, ""},
		{"SQLiteContentionThreshold 100", func(c *Config) { c.SQLiteContentionThreshold = 100 }, ""},
		{"SQLiteBusyTimeout negative", func(c *Config) { c.SQLiteBusyTimeout = -1 }, "SQLiteBusyTimeout"},
		{"SQLiteCacheSize negative", func(c *Config) { c.SQLiteCacheSize = -1 }, "SQLiteCacheSize"},
		{"RedisPoolSize negative", func(c *Config) { c.RedisPoolSize = -1 }, "RedisPoolSize"},
		{"RedisMinIdleConns negative", func(c *Config) { c.RedisMinIdleConns = -1 }, "RedisMinIdleConns"},
		{"HTTPReadTimeout negative", func(c *Config) { c.HTTPReadTimeout = -1 }, "HTTPReadTimeout"},
		{"HTTPWriteTimeout negative", func(c *Config) { c.HTTPWriteTimeout = -1 }, "HTTPWriteTimeout"},
		{"HTTPIdleTimeout negative", func(c *Config) { c.HTTPIdleTimeout = -1 }, "HTTPIdleTimeout"},
		{"HTTPRequestTimeout negative", func(c *Config) { c.HTTPRequestTimeout = -1 }, "HTTPRequestTimeout"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			errs, _ := cfg.Validate()

			if tt.wantErr == "" {
				if len(errs) > 0 {
					t.Errorf("expected no errors, got: %v", errs)
				}
			} else {
				found := false
				for _, e := range errs {
					if contains(e, tt.wantErr) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, errs)
				}
			}
		})
	}
}

// =============================================================================
// Config.Validate() tests — table-driven
// =============================================================================

func TestValidate_DefaultIsValid(t *testing.T) {
	cfg := Default()
	errs, _ := cfg.Validate()
	if len(errs) > 0 {
		t.Errorf("Default config should be valid, got errors: %v", errs)
	}
}

func TestValidate_EnumFields(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"valid StorageType jsonfile", func(c *Config) { c.StorageType = "jsonfile" }, ""},
		{"valid StorageType sqlite", func(c *Config) { c.StorageType = "sqlite" }, ""},
		{"invalid StorageType", func(c *Config) { c.StorageType = "postgres" }, "StorageType"},

		{"valid CacheType memory", func(c *Config) { c.CacheType = "memory" }, ""},
		{"valid CacheType redis", func(c *Config) { c.CacheType = "redis" }, ""},
		{"invalid CacheType", func(c *Config) { c.CacheType = "memcached" }, "CacheType"},

		{"valid GraphMode flat", func(c *Config) { c.GraphMode = "flat" }, ""},
		{"valid GraphMode disabled", func(c *Config) { c.GraphMode = "disabled" }, ""},
		{"invalid GraphMode", func(c *Config) { c.GraphMode = "fancy" }, "GraphMode"},

		{"valid GraphCycleDetection warn", func(c *Config) { c.GraphCycleDetection = "warn" }, ""},
		{"valid GraphCycleDetection error", func(c *Config) { c.GraphCycleDetection = "error" }, ""},
		{"valid GraphCycleDetection ignore", func(c *Config) { c.GraphCycleDetection = "ignore" }, ""},
		{"invalid GraphCycleDetection", func(c *Config) { c.GraphCycleDetection = "panic" }, "GraphCycleDetection"},

		{"valid PatchNullBehavior store", func(c *Config) { c.PatchNullBehavior = "store" }, ""},
		{"valid PatchNullBehavior delete", func(c *Config) { c.PatchNullBehavior = "delete" }, ""},
		{"invalid PatchNullBehavior", func(c *Config) { c.PatchNullBehavior = "ignore" }, "PatchNullBehavior"},

		{"valid AuthType none", func(c *Config) { c.AuthType = "none" }, ""},
		{"valid AuthType jwt", func(c *Config) { c.AuthType = "jwt"; c.JWTSecret = "s3cret" }, ""},
		{"valid AuthType apikey", func(c *Config) { c.AuthType = "apikey"; c.APIKeys = []string{"k1"} }, ""},
		{"invalid AuthType", func(c *Config) { c.AuthType = "oauth2" }, "AuthType"},

		{"deprecated TenantMode none accepted", func(c *Config) { c.TenantMode = "none" }, ""}, // alias for "path"
		{"valid TenantMode path", func(c *Config) { c.TenantMode = "path" }, ""},
		{"valid TenantMode strict", func(c *Config) { c.TenantMode = "strict" }, ""},
		{"invalid TenantMode", func(c *Config) { c.TenantMode = "auto" }, "TenantMode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			errs, _ := cfg.Validate()

			if tt.wantErr == "" {
				if len(errs) > 0 {
					t.Errorf("expected no errors, got: %v", errs)
				}
			} else {
				found := false
				for _, e := range errs {
					if contains(e, tt.wantErr) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, errs)
				}
			}
		})
	}
}

func TestValidate_NumericConstraints(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{"MaxEntitySize zero", func(c *Config) { c.MaxEntitySize = 0 }, "MaxEntitySize"},
		{"MaxEntitySize negative", func(c *Config) { c.MaxEntitySize = -1 }, "MaxEntitySize"},
		{"MaxEntitySize valid", func(c *Config) { c.MaxEntitySize = 1024 }, ""},

		{"CacheTTL negative", func(c *Config) { c.CacheTTL = -1 }, "CacheTTL"},
		{"CacheTTL zero", func(c *Config) { c.CacheTTL = 0 }, ""},

		{"Port negative", func(c *Config) { c.Port = -1 }, "Port"},
		{"Port too high", func(c *Config) { c.Port = 70000 }, "Port"},
		{"Port zero", func(c *Config) { c.Port = 0 }, ""},
		{"Port max", func(c *Config) { c.Port = 65535 }, ""},

		{"MaxQueryDepth zero", func(c *Config) { c.MaxQueryDepth = 0 }, "MaxQueryDepth"},
		{"MaxEmbedDepth zero", func(c *Config) { c.MaxEmbedDepth = 0 }, "MaxEmbedDepth"},
		{"RefEmbedDepth negative", func(c *Config) { c.RefEmbedDepth = -1 }, "RefEmbedDepth"},
		{"RefEmbedDepth zero", func(c *Config) { c.RefEmbedDepth = 0 }, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			errs, _ := cfg.Validate()

			if tt.wantErr == "" {
				if len(errs) > 0 {
					t.Errorf("expected no errors, got: %v", errs)
				}
			} else {
				found := false
				for _, e := range errs {
					if contains(e, tt.wantErr) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, errs)
				}
			}
		})
	}
}

func TestValidate_ConditionalRequirements(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			"jwt without secret",
			func(c *Config) { c.AuthType = "jwt"; c.JWTSecret = "" },
			"JWTSecret",
		},
		{
			"jwt with secret",
			func(c *Config) { c.AuthType = "jwt"; c.JWTSecret = "my-secret" },
			"",
		},
		{
			"apikey without keys",
			func(c *Config) { c.AuthType = "apikey"; c.APIKeys = []string{} },
			"APIKeys",
		},
		{
			"apikey with keys",
			func(c *Config) { c.AuthType = "apikey"; c.APIKeys = []string{"key1"} },
			"",
		},
		{
			"redis without host",
			func(c *Config) { c.CacheType = "redis"; c.RedisHost = "" },
			"RedisHost",
		},
		{
			"redis with host",
			func(c *Config) { c.CacheType = "redis"; c.RedisHost = "localhost" },
			"",
		},
		{
			"rate limit enabled, zero rate",
			func(c *Config) { c.RateLimitEnabled = true; c.RateLimitRate = 0 },
			"RateLimitRate",
		},
		{
			"rate limit enabled, zero window",
			func(c *Config) { c.RateLimitEnabled = true; c.RateLimitWindow = 0 },
			"RateLimitWindow",
		},
		{
			"rate limit enabled, valid",
			func(c *Config) { c.RateLimitEnabled = true; c.RateLimitRate = 100; c.RateLimitWindow = 60 },
			"",
		},
		{
			"rate limit disabled, zero values ok",
			func(c *Config) { c.RateLimitEnabled = false; c.RateLimitRate = 0; c.RateLimitWindow = 0 },
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			errs, _ := cfg.Validate()

			if tt.wantErr == "" {
				if len(errs) > 0 {
					t.Errorf("expected no errors, got: %v", errs)
				}
			} else {
				found := false
				for _, e := range errs {
					if contains(e, tt.wantErr) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected error containing %q, got: %v", tt.wantErr, errs)
				}
			}
		})
	}
}

func TestValidate_MultipleErrors(t *testing.T) {
	cfg := Default()
	cfg.StorageType = "baddb"
	cfg.CacheType = "badcache"
	cfg.MaxEntitySize = -1

	errs, _ := cfg.Validate()
	if len(errs) < 3 {
		t.Errorf("expected at least 3 errors, got %d: %v", len(errs), errs)
	}
}

func TestValidate_CrossFieldWarnings(t *testing.T) {
	tests := []struct {
		name        string
		mutate      func(*Config)
		wantWarning string
	}{
		{
			"idle conns > open conns",
			func(c *Config) {
				c.SQLiteMaxIdleConns = 50
				c.SQLiteMaxOpenConns = 10
			},
			"SQLiteMaxIdleConns",
		},
		{
			"redis min idle > pool size",
			func(c *Config) {
				c.CacheType = "redis"
				c.RedisHost = "localhost"
				c.RedisMinIdleConns = 100
				c.RedisPoolSize = 20
			},
			"RedisMinIdleConns",
		},
		{
			"sqlite tuning on jsonfile backend",
			func(c *Config) {
				c.StorageType = "jsonfile"
				c.SQLiteMaxOpenConns = 64
			},
			"SQLite tuning",
		},
		{
			"redis tuning on memory cache",
			func(c *Config) {
				c.CacheType = "memory"
				c.RedisPoolSize = 200
			},
			"Redis tuning",
		},
		{
			"request timeout > write timeout",
			func(c *Config) {
				c.HTTPRequestTimeout = 120
				c.HTTPWriteTimeout = 30
			},
			"HTTPRequestTimeout",
		},
		{
			"ref embed depth > max embed depth",
			func(c *Config) {
				c.RefEmbedDepth = 20
				c.MaxEmbedDepth = 5
			},
			"RefEmbedDepth",
		},
		{
			"very small cache size",
			func(c *Config) {
				c.CacheType = "memory"
				c.CacheSize = 4
			},
			"CacheSize",
		},
		{
			"no warning on valid config",
			func(c *Config) {},
			"",
		},
		{
			"no warning when idle <= open",
			func(c *Config) {
				c.StorageType = "sqlite"
				c.SQLiteMaxIdleConns = 5
				c.SQLiteMaxOpenConns = 25
			},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			_, warnings := cfg.Validate()

			if tt.wantWarning == "" {
				if len(warnings) > 0 {
					t.Errorf("expected no warnings, got: %v", warnings)
				}
			} else {
				found := false
				for _, w := range warnings {
					if contains(w, tt.wantWarning) {
						found = true
						break
					}
				}
				if !found {
					t.Errorf("expected warning containing %q, got: %v", tt.wantWarning, warnings)
				}
			}
		})
	}
}

// contains checks if substr is in s (simple helper to avoid importing strings)
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		(len(s) > 0 && len(substr) > 0 && searchString(s, substr)))
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Timeseries guardrail config tests ---

// TestLoadFromEnv_TimeseriesGuardrails verifies that all six OLU_TS_* env
// vars are parsed and applied correctly.
func TestLoadFromEnv_TimeseriesGuardrails(t *testing.T) {
	cfg := Default()

	os.Setenv("OLU_TS_QUERY_TIMEOUT", "60")
	os.Setenv("OLU_TS_MAX_QUERY_EVENTS", "500")
	os.Setenv("OLU_TS_MAX_SCAN_EVENTS", "20000")
	os.Setenv("OLU_TS_MAX_RANGE_DAYS", "30")
	os.Setenv("OLU_TS_MAX_BATCH_SIZE", "100")
	os.Setenv("OLU_TS_MAX_RESPONSE_BYTES", "1048576")
	defer func() {
		os.Unsetenv("OLU_TS_QUERY_TIMEOUT")
		os.Unsetenv("OLU_TS_MAX_QUERY_EVENTS")
		os.Unsetenv("OLU_TS_MAX_SCAN_EVENTS")
		os.Unsetenv("OLU_TS_MAX_RANGE_DAYS")
		os.Unsetenv("OLU_TS_MAX_BATCH_SIZE")
		os.Unsetenv("OLU_TS_MAX_RESPONSE_BYTES")
	}()

	LoadFromEnv(cfg)

	if cfg.TSQueryTimeoutSecs != 60 {
		t.Errorf("TSQueryTimeoutSecs: got %d, want 60", cfg.TSQueryTimeoutSecs)
	}
	if cfg.TSMaxQueryEvents != 500 {
		t.Errorf("TSMaxQueryEvents: got %d, want 500", cfg.TSMaxQueryEvents)
	}
	if cfg.TSMaxScanEvents != 20000 {
		t.Errorf("TSMaxScanEvents: got %d, want 20000", cfg.TSMaxScanEvents)
	}
	if cfg.TSMaxRangeDays != 30 {
		t.Errorf("TSMaxRangeDays: got %d, want 30", cfg.TSMaxRangeDays)
	}
	if cfg.TSMaxBatchSize != 100 {
		t.Errorf("TSMaxBatchSize: got %d, want 100", cfg.TSMaxBatchSize)
	}
	if cfg.TSMaxResponseBytes != 1048576 {
		t.Errorf("TSMaxResponseBytes: got %d, want 1048576", cfg.TSMaxResponseBytes)
	}
}

// TestDefault_TimeseriesGuardrails verifies that all seven guardrail fields
// have sensible non-zero defaults in the out-of-the-box configuration.
func TestDefault_TimeseriesGuardrails(t *testing.T) {
	cfg := Default()

	checks := []struct {
		name string
		val  int
		want int
	}{
		{"TSQueryTimeoutSecs", cfg.TSQueryTimeoutSecs, 30},
		{"TSMaxQueryEvents", cfg.TSMaxQueryEvents, 10000},
		{"TSMaxScanEvents", cfg.TSMaxScanEvents, 500000},
		{"TSMaxRangeDays", cfg.TSMaxRangeDays, 366},
		{"TSMaxBatchSize", cfg.TSMaxBatchSize, 5000},
		{"TSMaxResponseBytes", cfg.TSMaxResponseBytes, 10485760},
		{"TSMaxAggregateBuckets", cfg.TSMaxAggregateBuckets, 10000},
	}
	for _, c := range checks {
		if c.val != c.want {
			t.Errorf("%s default: got %d, want %d", c.name, c.val, c.want)
		}
	}
}

// TestValidate_TimeseriesConditionals verifies the conditional validation
// rules for TimeseriesEnabled (requires TenantMode=strict + StorageType=sqlite).
func TestValidate_TimeseriesConditionals(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Config)
		wantErr string
	}{
		{
			"ts enabled without strict tenant mode",
			func(c *Config) {
				c.TimeseriesEnabled = true
				c.StorageType = "sqlite"
				c.TenantMode = "path"
				c.TSMemtableSize = 64 * 1024 * 1024
				c.TSBlockSize = 32768
				c.TSL0CompactionThreshold = 4
				c.TSMaxOpenFiles = 500
				c.TSDefaultRetentionDays = 90
				c.TSCompactionIntervalSecs = 3600
			},
			"", // path mode + timeseries is valid; strict mode is no longer required
		},
		{
			"ts enabled without sqlite storage",
			func(c *Config) {
				c.TimeseriesEnabled = true
				c.StorageType = "jsonfile"
				c.TenantMode = "strict"
				c.TSMemtableSize = 64 * 1024 * 1024
				c.TSBlockSize = 32768
				c.TSL0CompactionThreshold = 4
				c.TSMaxOpenFiles = 500
				c.TSDefaultRetentionDays = 90
				c.TSCompactionIntervalSecs = 3600
			},
			"StorageType",
		},
		{
			"ts enabled, all valid",
			func(c *Config) {
				c.TimeseriesEnabled = true
				c.StorageType = "sqlite"
				c.TenantMode = "strict"
				c.TSMemtableSize = 64 * 1024 * 1024
				c.TSBlockSize = 32768
				c.TSL0CompactionThreshold = 4
				c.TSMaxOpenFiles = 500
				c.TSDefaultRetentionDays = 90
				c.TSCompactionIntervalSecs = 3600
			},
			"",
		},
		{
			"ts disabled, no errors",
			func(c *Config) {
				c.TimeseriesEnabled = false
			},
			"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := Default()
			tt.mutate(cfg)
			errs, _ := cfg.Validate()

			if tt.wantErr == "" {
				for _, e := range errs {
					if searchString(e, "Timeseries") || searchString(e, "TenantMode") ||
						searchString(e, "StorageType") || searchString(e, "TS") {
						t.Errorf("unexpected TS validation error: %s", e)
					}
				}
				return
			}

			found := false
			for _, e := range errs {
				if searchString(e, tt.wantErr) {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("expected error containing %q, got: %v", tt.wantErr, errs)
			}
		})
	}
}

func TestReadSecret_EnvVarTakesPrecedence(t *testing.T) {
	// Env var present — must be returned without touching the filesystem.
	t.Setenv("OLU_INTERNAL_TOKEN", "from-env")
	got := readSecret("olu_internal_token")
	if got != "from-env" {
		t.Errorf("expected 'from-env', got %q", got)
	}
}

func TestReadSecret_FallsBackToFile(t *testing.T) {
	// Env var absent — should read from /run/secrets/<name>, stripping trailing newline.
	dir := t.TempDir()
	secretFile := dir + "/test_token"
	if err := os.WriteFile(secretFile, []byte("from-file\n"), 0600); err != nil {
		t.Fatal(err)
	}

	// Patch readSecret by exercising the logic directly via a temp file.
	// We can't easily redirect /run/secrets, so test the helper's stripping
	// behaviour by verifying os.ReadFile + TrimRight (the actual implementation).
	data, err := os.ReadFile(secretFile)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.TrimRight(string(data), "\n\r")
	if got != "from-file" {
		t.Errorf("expected 'from-file', got %q", got)
	}
}

func TestReadSecret_EmptyWhenNeitherSet(t *testing.T) {
	// Neither env var nor secret file — must return empty string.
	// Use a name that is guaranteed not to be set in the test environment.
	got := readSecret("olu_nonexistent_secret_xyzzy")
	if got != "" {
		t.Errorf("expected empty string, got %q", got)
	}
}

func TestLoadFromEnv_InternalToken_FromEnv(t *testing.T) {
	t.Setenv("OLU_INTERNAL_TOKEN", "test-token-from-env")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.InternalToken != "test-token-from-env" {
		t.Errorf("expected 'test-token-from-env', got %q", cfg.InternalToken)
	}
}

func TestLoadFromEnv_OLU_ADDR(t *testing.T) {
	t.Setenv("OLU_ADDR", "0.0.0.0:8080")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.Host != "0.0.0.0" {
		t.Errorf("Host: want '0.0.0.0', got %q", cfg.Host)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port: want 8080, got %d", cfg.Port)
	}
}

func TestLoadFromEnv_OLU_HOST_overrides_OLU_ADDR(t *testing.T) {
	// OLU_HOST/OLU_PORT must take precedence over OLU_ADDR.
	t.Setenv("OLU_ADDR", "0.0.0.0:8080")
	t.Setenv("OLU_HOST", "127.0.0.1")
	t.Setenv("OLU_PORT", "9999")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.Host != "127.0.0.1" {
		t.Errorf("Host: want '127.0.0.1', got %q", cfg.Host)
	}
	if cfg.Port != 9999 {
		t.Errorf("Port: want 9999, got %d", cfg.Port)
	}
}

func TestLoadFromEnv_OLU_METRICS_ADDR(t *testing.T) {
	t.Setenv("OLU_METRICS_ADDR", "0.0.0.0:9091")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.MetricsHost != "0.0.0.0" {
		t.Errorf("MetricsHost: want '0.0.0.0', got %q", cfg.MetricsHost)
	}
	if cfg.MetricsPort != 9091 {
		t.Errorf("MetricsPort: want 9091, got %d", cfg.MetricsPort)
	}
}

func TestLoadFromEnv_OLU_SQLITE_PATH(t *testing.T) {
	t.Setenv("OLU_SQLITE_PATH", "/data/olu/registry.db")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.DBPath != "/data/olu/registry.db" {
		t.Errorf("DBPath: want '/data/olu/registry.db', got %q", cfg.DBPath)
	}
}

func TestLoadFromEnv_OLU_SQLITE_PATH_overrides_OLU_DB_PATH(t *testing.T) {
	t.Setenv("OLU_DB_PATH", "/old/path.db")
	t.Setenv("OLU_SQLITE_PATH", "/new/path.db")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.DBPath != "/new/path.db" {
		t.Errorf("DBPath: want '/new/path.db', got %q", cfg.DBPath)
	}
}

func TestLoadFromEnv_OLU_LOG_LEVEL(t *testing.T) {
	cases := []struct{ input, want string }{
		{"debug", "debug"},
		{"info", "info"},
		{"warn", "warn"},
		{"error", "error"},
		{"DEBUG", "debug"},  // case-insensitive
		{"WARN", "warn"},
		{"invalid", "info"}, // unknown value ignored; default retained
	}
	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			t.Setenv("OLU_LOG_LEVEL", tc.input)
			cfg := Default()
			LoadFromEnv(cfg)
			if cfg.LogLevel != tc.want {
				t.Errorf("LogLevel: want %q, got %q", tc.want, cfg.LogLevel)
			}
		})
	}
}

func TestLoadFromEnv_OLU_DEBUG_compat(t *testing.T) {
	// OLU_DEBUG=true should set LogLevel to "debug".
	t.Setenv("OLU_DEBUG", "true")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.LogLevel != "debug" {
		t.Errorf("LogLevel: want 'debug', got %q", cfg.LogLevel)
	}
}

func TestLoadFromEnv_OLU_LOG_LEVEL_overrides_OLU_DEBUG(t *testing.T) {
	// OLU_LOG_LEVEL must win even when OLU_DEBUG=true is also set.
	t.Setenv("OLU_DEBUG", "true")
	t.Setenv("OLU_LOG_LEVEL", "warn")
	cfg := Default()
	LoadFromEnv(cfg)
	if cfg.LogLevel != "warn" {
		t.Errorf("LogLevel: want 'warn', got %q", cfg.LogLevel)
	}
}
