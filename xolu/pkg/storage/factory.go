// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"fmt"
	"sync"
)

// StoreFactory is a function that creates a new Store instance
type StoreFactory func(config map[string]interface{}) (Store, error)

var (
	storeMu       sync.RWMutex
	storeRegistry = make(map[string]StoreFactory)
)

// RegisterStore registers a new store implementation
func RegisterStore(name string, factory StoreFactory) {
	storeMu.Lock()
	defer storeMu.Unlock()
	storeRegistry[name] = factory
}

// NewStore creates a new store instance by name
func NewStore(name string, config map[string]interface{}) (Store, error) {
	storeMu.RLock()
	factory, exists := storeRegistry[name]
	storeMu.RUnlock()
	
	if !exists {
		return nil, fmt.Errorf("unknown store type: %s", name)
	}
	
	return factory(config)
}

// ListStores returns all registered store types
func ListStores() []string {
	storeMu.RLock()
	defer storeMu.RUnlock()
	
	stores := make([]string, 0, len(storeRegistry))
	for name := range storeRegistry {
		stores = append(stores, name)
	}
	return stores
}

// NewStoreFromConfig creates a store directly from a StoreConfig.
// This is the preferred constructor for tenant-scoped stores.
func NewStoreFromConfig(cfg StoreConfig) (Store, error) {
	switch cfg.Type {
	case "sqlite":
		return NewSQLiteStore(cfg.DBPath, SQLiteConfig{
			DBPath:              cfg.DBPath,
			EnableWAL:           true,
			EnableForeignKeys:   true,
			CacheSize:           cfg.SQLiteCacheSize,
			BusyTimeout:         cfg.SQLiteBusyTimeout,
			FullTextEnabled:     cfg.FullTextEnabled,
			GraphEnabled:        cfg.GraphEnabled,
			TenantID:            cfg.TenantID,
			MaxOpenConns:        cfg.SQLiteMaxOpenConns,
			MaxIdleConns:        cfg.SQLiteMaxIdleConns,
			ReadPoolSize:        cfg.SQLiteReadPoolSize,
			ContentionThreshold: cfg.SQLiteContentionThreshold,
		})
	case "jsonfile":
		store, err := NewJSONFileStore(cfg.BaseDir, cfg.Schema)
		if err != nil {
			return nil, err
		}
		store.storeConfig.TenantID = cfg.TenantID
		store.storeConfig.FullTextEnabled = cfg.FullTextEnabled
		store.storeConfig.GraphEnabled = cfg.GraphEnabled
		return store, nil
	default:
		return nil, fmt.Errorf("unknown store type: %s", cfg.Type)
	}
}

// init registers built-in stores
func init() {
	// Register JSONFileStore
	RegisterStore("jsonfile", func(config map[string]interface{}) (Store, error) {
		baseDir, ok := config["base_dir"].(string)
		if !ok {
			baseDir = "data"
		}
		
		schema, ok := config["schema"].(string)
		if !ok {
			schema = "default"
		}
		
		return NewJSONFileStore(baseDir, schema)
	})
	
	// Register SQLiteStore
	RegisterStore("sqlite", func(config map[string]interface{}) (Store, error) {
		dbPath, ok := config["db_path"].(string)
		if !ok {
			dbPath = "olu.db"
		}
		
		sqliteConfig := SQLiteConfig{
			DBPath:           dbPath,
			EnableWAL:        true,
			EnableForeignKeys: true,
			CacheSize:        2000, // 2MB
			BusyTimeout:      5000, // 5 seconds
			FullTextEnabled:  false,
		}
		
		// Allow overriding config options
		if wal, ok := config["enable_wal"].(bool); ok {
			sqliteConfig.EnableWAL = wal
		}
		if fk, ok := config["enable_foreign_keys"].(bool); ok {
			sqliteConfig.EnableForeignKeys = fk
		}
		if cache, ok := config["cache_size"].(int); ok {
			sqliteConfig.CacheSize = cache
		}
		if timeout, ok := config["busy_timeout"].(int); ok {
			sqliteConfig.BusyTimeout = timeout
		}
		if fts, ok := config["full_text_enabled"].(bool); ok {
			sqliteConfig.FullTextEnabled = fts
		}
		
		return NewSQLiteStore(dbPath, sqliteConfig)
	})
}

