// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ha1tch/xolu/pkg/tenant"
)

// JSONFileStore implements Store interface using JSON files
type JSONFileStore struct {
	baseDir     string
	schema      string
	idLocks     map[string]*sync.Mutex
	idMutex     sync.RWMutex
	storeConfig StoreConfig
}

// NewJSONFileStore creates a new JSON file-based storage
func NewJSONFileStore(baseDir, schema string) (*JSONFileStore, error) {
	schemaPath := filepath.Join(baseDir, schema)
	if err := os.MkdirAll(schemaPath, 0755); err != nil {
		return nil, fmt.Errorf("failed to create schema directory: %w", err)
	}
	
	return &JSONFileStore{
		baseDir: baseDir,
		schema:  schema,
		idLocks: make(map[string]*sync.Mutex),
		storeConfig: StoreConfig{
			Type:    "jsonfile",
			BaseDir: baseDir,
			Schema:  schema,
		},
	}, nil
}

// Config returns the store's StoreConfig.
func (s *JSONFileStore) Config() StoreConfig {
	return s.storeConfig
}

// Info returns store information
func (s *JSONFileStore) Info() StoreInfo {
	return StoreInfo{
		Type:                "jsonfile",
		Version:             "1.0.0",
		SupportsSearch:      true,
		SupportsBatch:       false,
		SupportsTransaction: false,
	}
}

// getIDLock gets or creates a mutex for an entity's ID generation
func (s *JSONFileStore) getIDLock(entity string) *sync.Mutex {
	s.idMutex.Lock()
	defer s.idMutex.Unlock()
	
	if lock, exists := s.idLocks[entity]; exists {
		return lock
	}
	
	lock := &sync.Mutex{}
	s.idLocks[entity] = lock
	return lock
}

// GetEntityDir returns the directory path for an entity
func (s *JSONFileStore) GetEntityDir(entity string) string {
	if s.storeConfig.TenantID != 0 {
		return filepath.Join(s.baseDir, s.schema,
			tenant.StorageDirSegment(s.storeConfig.TenantID), entity)
	}
	return filepath.Join(s.baseDir, s.schema, entity)
}

// getEntityFile returns the file path for a specific entity instance
func (s *JSONFileStore) getEntityFile(entity string, id int) string {
	return filepath.Join(s.GetEntityDir(entity), fmt.Sprintf("%d.json", id))
}

// getNextIDFile returns the file path for storing the next ID
func (s *JSONFileStore) getNextIDFile(entity string) string {
	return filepath.Join(s.GetEntityDir(entity), "_next_id.json")
}

// NextID gets the next available ID for an entity
func (s *JSONFileStore) NextID(ctx context.Context, entity string) (int, error) {
	lock := s.getIDLock(entity)
	lock.Lock()
	defer lock.Unlock()
	
	entityDir := s.GetEntityDir(entity)
	if err := os.MkdirAll(entityDir, 0755); err != nil {
		return 0, fmt.Errorf("failed to create entity directory: %w", err)
	}
	
	nextIDFile := s.getNextIDFile(entity)
	
	// Read current next ID
	var nextID int = 1
	if data, err := os.ReadFile(nextIDFile); err == nil {
		var idData struct {
			NextID int `json:"next_id"`
		}
		if err := json.Unmarshal(data, &idData); err == nil {
			nextID = idData.NextID
		}
	}
	
	// Write incremented ID
	idData := struct {
		NextID int `json:"next_id"`
	}{NextID: nextID + 1}
	
	data, err := json.Marshal(idData)
	if err != nil {
		return 0, err
	}
	
	if err := os.WriteFile(nextIDFile, data, 0644); err != nil {
		return 0, err
	}
	
	return nextID, nil
}

// Create creates a new entity with auto-generated ID
func (s *JSONFileStore) Create(ctx context.Context, entity string, data map[string]interface{}) (int, error) {
	id, err := s.NextID(ctx, entity)
	if err != nil {
		return 0, err
	}

	// Build stored copy with version tracking.
	stored := make(map[string]interface{}, len(data)+2)
	for k, v := range data {
		if k == "_version" {
			continue
		}
		stored[k] = v
	}
	stored["id"] = id
	stored["_version"] = 1

	filePath := s.getEntityFile(entity, id)
	jsonData, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return 0, err
	}

	if err := os.WriteFile(filePath, jsonData, 0644); err != nil {
		return 0, err
	}

	return id, nil
}

// Get retrieves an entity by ID
func (s *JSONFileStore) Get(ctx context.Context, entity string, id int) (map[string]interface{}, error) {
	filePath := s.getEntityFile(entity, id)
	
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s with id %d", ErrNotFound, entity, id)
		}
		return nil, err
	}
	
	var result map[string]interface{}
	if err := json.Unmarshal(data, &result); err != nil {
		return nil, err
	}
	
	return result, nil
}

// Update replaces an entity completely.
// Optimistic concurrency: if data contains "_version", the write is conditional
// on the stored version matching. Returns ErrConflict on mismatch, ErrNotFound
// if the entity does not exist.
func (s *JSONFileStore) Update(ctx context.Context, entity string, id int, data map[string]interface{}) error {
	filePath := s.getEntityFile(entity, id)

	existingBytes, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%w: %s with id %d", ErrNotFound, entity, id)
		}
		return err
	}

	var existing map[string]interface{}
	if err := json.Unmarshal(existingBytes, &existing); err != nil {
		return fmt.Errorf("failed to unmarshal existing entity: %w", err)
	}

	// Extract expected version from request (opt-in conditional write).
	var expectVersion int
	var hasVersion bool
	if v, ok := data["_version"]; ok {
		hasVersion = true
		switch tv := v.(type) {
		case float64:
			expectVersion = int(tv)
		case int:
			expectVersion = tv
		}
	}

	// Read current stored version.
	currentVersion := 1
	if sv, ok := existing["_version"]; ok {
		switch tv := sv.(type) {
		case float64:
			currentVersion = int(tv)
		case int:
			currentVersion = tv
		}
	}

	if hasVersion && currentVersion != expectVersion {
		return ErrConflict
	}

	// Build stored copy, stripping _version from request data.
	stored := make(map[string]interface{}, len(data)+2)
	for k, v := range data {
		if k == "_version" {
			continue
		}
		stored[k] = v
	}
	stored["id"] = id
	stored["_version"] = currentVersion + 1

	jsonData, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		return err
	}

	return os.WriteFile(filePath, jsonData, 0644)
}

// Patch partially updates an entity
func (s *JSONFileStore) Patch(ctx context.Context, entity string, id int, patchData map[string]interface{}) error {
	return s.PatchValidated(ctx, entity, id, patchData, nil)
}

// PatchValidated merges patch data and optionally validates the merged result.
// Note: JSONFileStore does not provide transactional isolation for Patch —
// concurrent patches may still race. For production multi-tenant use, use SQLite.
func (s *JSONFileStore) PatchValidated(ctx context.Context, entity string, id int, patchData map[string]interface{}, validate func(merged map[string]interface{}) error) error {
	existing, err := s.Get(ctx, entity, id)
	if err != nil {
		return err
	}
	
	// Extract expected version before merging (opt-in conditional write).
	var expectVersion interface{}
	if v, ok := patchData["_version"]; ok {
		expectVersion = v
	}

	// Merge patch data into existing data; skip metadata keys.
	for k, v := range patchData {
		if k == "id" || k == "_version" {
			continue
		}
		existing[k] = v
	}

	if validate != nil {
		if err := validate(existing); err != nil {
			return err
		}
	}

	// Pass expected version through to Update for conditional write.
	if expectVersion != nil {
		existing["_version"] = expectVersion
	}

	return s.Update(ctx, entity, id, existing)
}

// Delete removes an entity
func (s *JSONFileStore) Delete(ctx context.Context, entity string, id int) error {
	filePath := s.getEntityFile(entity, id)
	
	if !s.Exists(ctx, entity, id) {
		return fmt.Errorf("%w: %s with id %d", ErrNotFound, entity, id)
	}
	
	return os.Remove(filePath)
}

// Save upserts an entity with the caller-specified ID.
// Returns (true, nil) when a new file was created, (false, nil) when an
// existing file was overwritten.
//
// Optimistic concurrency: if data contains "_version", the overwrite path is
// conditional. If the stored _version differs from the expected value, Save
// returns ErrConflict without writing. On a successful overwrite, _version is
// incremented. The create path always starts at _version = 1.
func (s *JSONFileStore) Save(ctx context.Context, entity string, id int, data map[string]interface{}) (bool, error) {
	created := !s.Exists(ctx, entity, id)

	// Extract expected version before copying data.
	var expectVersion int
	var hasVersion bool
	if v, ok := data["_version"]; ok {
		hasVersion = true
		switch tv := v.(type) {
		case float64:
			expectVersion = int(tv)
		case int:
			expectVersion = tv
		}
	}

	// Strip _version from written data; we manage it ourselves.
	clean := make(map[string]interface{}, len(data))
	for k, v := range data {
		if k == "_version" {
			continue
		}
		clean[k] = v
	}
	clean["id"] = id

	entityDir := s.GetEntityDir(entity)
	if err := os.MkdirAll(entityDir, 0755); err != nil {
		return false, fmt.Errorf("failed to create entity directory: %w", err)
	}

	filePath := s.getEntityFile(entity, id)

	var nextVersion int
	if created {
		nextVersion = 1
	} else {
		// Read current version for conditional check / increment.
		if existing, err := os.ReadFile(filePath); err == nil {
			var stored map[string]interface{}
			if json.Unmarshal(existing, &stored) == nil {
				if sv, ok := stored["_version"]; ok {
					switch tv := sv.(type) {
					case float64:
						nextVersion = int(tv)
					case int:
						nextVersion = tv
					}
				}
			}
		}
		if hasVersion && nextVersion != expectVersion {
			return false, ErrConflict
		}
		nextVersion++
	}
	clean["_version"] = nextVersion

	jsonData, err := json.MarshalIndent(clean, "", "  ")
	if err != nil {
		return false, err
	}

	return created, os.WriteFile(filePath, jsonData, 0644)
}

// Commit performs an atomic-as-possible upsert + append sequence on the
// jsonfile backend. True atomicity is not available in the file system;
// this implementation acquires per-entity ID locks in sorted order to
// prevent races and rolls back completed writes on failure.
//
// Commit is not supported by the jsonfile backend. The jsonfile backend
// does not provide true transactional atomicity and has been deprecated
// for production use. This method exists solely to satisfy the Store
// interface; callers will receive ErrNotSupported, which the HTTP handler
// maps to 501 Not Implemented (OLU-CM009).
func (s *JSONFileStore) Commit(_ context.Context, _ CommitRequest) (CommitResult, error) {
	return CommitResult{}, ErrNotSupported
}

// List returns all entities of a given type
func (s *JSONFileStore) List(ctx context.Context, entity string) ([]map[string]interface{}, error) {
	entityDir := s.GetEntityDir(entity)
	
	if _, err := os.Stat(entityDir); os.IsNotExist(err) {
		return []map[string]interface{}{}, nil
	}
	
	files, err := os.ReadDir(entityDir)
	if err != nil {
		return nil, err
	}
	
	var results []map[string]interface{}
	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" || file.Name() == "_next_id.json" {
			continue
		}
		
		data, err := os.ReadFile(filepath.Join(entityDir, file.Name()))
		if err != nil {
			continue
		}
		
		var entity map[string]interface{}
		if err := json.Unmarshal(data, &entity); err != nil {
			continue
		}
		
		results = append(results, entity)
	}
	
	return results, nil
}

// Exists checks if an entity exists
func (s *JSONFileStore) Exists(ctx context.Context, entity string, id int) bool {
	filePath := s.getEntityFile(entity, id)
	_, err := os.Stat(filePath)
	return err == nil
}

// Ping verifies that the storage directory is accessible.
func (s *JSONFileStore) Ping(ctx context.Context) error {
	_, err := os.Stat(s.baseDir)
	return err
}

// Close closes the storage (cleanup if needed)
func (s *JSONFileStore) Close() error {
	return nil
}

// ListEntities returns all entity types in the schema
func (s *JSONFileStore) ListEntities(ctx context.Context) ([]string, error) {
	schemaPath := filepath.Join(s.baseDir, s.schema)
	if s.storeConfig.TenantID != 0 {
		schemaPath = filepath.Join(s.baseDir, s.schema,
			tenant.StorageDirSegment(s.storeConfig.TenantID))
	}
	
	entries, err := os.ReadDir(schemaPath)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	
	var entities []string
	for _, entry := range entries {
		if entry.IsDir() {
			entities = append(entities, entry.Name())
		}
	}
	
	return entities, nil
}

// Search implements field-based search
func (s *JSONFileStore) Search(ctx context.Context, entity string, field string, query string, matchType string) ([]map[string]interface{}, error) {
	all, err := s.List(ctx, entity)
	if err != nil {
		return nil, err
	}
	
	var results []map[string]interface{}
	query = strings.ToLower(query)
	
	for _, item := range all {
		if value, ok := item[field]; ok {
			valueStr := strings.ToLower(fmt.Sprintf("%v", value))
			
			matched := false
			switch matchType {
			case "contains":
				matched = strings.Contains(valueStr, query)
			case "starts":
				matched = strings.HasPrefix(valueStr, query)
			case "ends":
				matched = strings.HasSuffix(valueStr, query)
			case "exact":
				matched = valueStr == query
			default:
				matched = strings.Contains(valueStr, query)
			}
			
			if matched {
				results = append(results, item)
			}
		}
	}
	
	return results, nil
}


// FullTextSearch is not supported for JSONFileStore, returns empty results
func (s *JSONFileStore) FullTextSearch(ctx context.Context, query string, entity string) ([]map[string]interface{}, error) {
	// Full-text search requires SQLite FTS5
	// For JSONFileStore, fall back to basic search or return empty
	return []map[string]interface{}{}, nil
}

