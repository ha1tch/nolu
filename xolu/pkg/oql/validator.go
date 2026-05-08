// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ha1tch/tsqlparser/ast"
)

// EntityChecker is an interface for checking entity existence
type EntityChecker interface {
	ListEntities(ctx context.Context) ([]string, error)
}

// Validator validates OQL queries against schema
type Validator struct {
	schemaDir     string
	entities      map[string]bool // Cached entity names
	entityChecker EntityChecker   // Optional store-based checker
}

// NewValidator creates a new validator that checks the filesystem
func NewValidator(schemaDir string) *Validator {
	v := &Validator{
		schemaDir: schemaDir,
		entities:  make(map[string]bool),
	}
	v.loadEntitiesFromDisk()
	return v
}

// NewValidatorWithStore creates a validator that checks the store for entities
func NewValidatorWithStore(schemaDir string, checker EntityChecker) *Validator {
	v := &Validator{
		schemaDir:     schemaDir,
		entities:      make(map[string]bool),
		entityChecker: checker,
	}
	v.RefreshEntities()
	return v
}

// loadEntitiesFromDisk scans the schema directory for entity folders
// and schema JSON files (e.g. "author.json" -> entity "author").
func (v *Validator) loadEntitiesFromDisk() {
	entries, err := os.ReadDir(v.schemaDir)
	if err != nil {
		return
	}

	for _, entry := range entries {
		if entry.IsDir() {
			v.entities[entry.Name()] = true
		} else if filepath.Ext(entry.Name()) == ".json" {
			name := entry.Name()[:len(entry.Name())-5]
			if name != "" {
				v.entities[name] = true
			}
		}
	}
}

// loadEntitiesFromStore queries the store for entity types
func (v *Validator) loadEntitiesFromStore() {
	if v.entityChecker == nil {
		return
	}
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	entities, err := v.entityChecker.ListEntities(ctx)
	if err != nil {
		return
	}
	
	for _, entity := range entities {
		v.entities[entity] = true
	}
}

// RefreshEntities reloads the entity list.
// If a store checker is configured, it queries the store.
// Otherwise, it scans the filesystem.
func (v *Validator) RefreshEntities() {
	v.entities = make(map[string]bool)
	if v.entityChecker != nil {
		v.loadEntitiesFromStore()
	}
	// Always also check disk for schema definitions
	v.loadEntitiesFromDisk()
}

// EntityExists checks if an entity exists.
// If the entity is not in the cache, it automatically refreshes
// before returning false. This ensures newly created entity types are
// recognised without requiring manual refresh.
func (v *Validator) EntityExists(name string) bool {
	// Normalize name (lowercase, remove brackets/quotes)
	name = normalizeEntityName(name)
	
	// Check cache first
	if v.entities[name] {
		return true
	}
	
	// Entity not in cache - refresh and retry
	// This handles dynamically created entity types
	v.RefreshEntities()
	return v.entities[name]
}

// Validate validates an AST statement
func (v *Validator) Validate(stmt ast.Statement) error {
	switch s := stmt.(type) {
	case *ast.SelectStatement:
		return v.validateSelect(s)
	case *ast.InsertStatement:
		return v.validateInsert(s)
	case *ast.UpdateStatement:
		return v.validateUpdate(s)
	case *ast.DeleteStatement:
		return v.validateDelete(s)
	default:
		return fmt.Errorf("unsupported statement type: %T", stmt)
	}
}

func (v *Validator) validateSelect(s *ast.SelectStatement) error {
	if s.From == nil {
		return fmt.Errorf("FROM clause required")
	}

	// Validate single table (no JOINs)
	if len(s.From.Tables) != 1 {
		return fmt.Errorf("OQL supports single table queries only")
	}

	tableName, ok := s.From.Tables[0].(*ast.TableName)
	if !ok {
		return fmt.Errorf("invalid table reference")
	}

	entity := normalizeEntityName(tableName.Name.String())
	if !v.EntityExists(entity) {
		return fmt.Errorf("entity '%s' does not exist", entity)
	}

	// Validate no unsupported features
	if s.Union != nil {
		return fmt.Errorf("UNION is not supported")
	}

	// Validate columns reference valid fields (optional - could defer to runtime)

	return nil
}

func (v *Validator) validateInsert(s *ast.InsertStatement) error {
	if s.Table == nil {
		return fmt.Errorf("table name required")
	}

	entity := normalizeEntityName(s.Table.String())
	if !v.EntityExists(entity) {
		return fmt.Errorf("entity '%s' does not exist", entity)
	}

	// Must have values
	if len(s.Values) == 0 && s.Select == nil {
		return fmt.Errorf("INSERT requires VALUES or SELECT")
	}

	// INSERT ... SELECT not supported for now
	if s.Select != nil {
		return fmt.Errorf("INSERT ... SELECT is not supported")
	}

	// Validate column count matches value count
	colCount := len(s.Columns)
	for i, row := range s.Values {
		if colCount > 0 && len(row) != colCount {
			return fmt.Errorf("row %d: column count (%d) does not match value count (%d)",
				i+1, colCount, len(row))
		}
	}

	return nil
}

func (v *Validator) validateUpdate(s *ast.UpdateStatement) error {
	// WHERE is required
	if s.Where == nil {
		return fmt.Errorf("UPDATE without WHERE clause is not permitted")
	}

	if s.Table == nil {
		return fmt.Errorf("table name required")
	}

	entity := normalizeEntityName(s.Table.String())
	if !v.EntityExists(entity) {
		return fmt.Errorf("entity '%s' does not exist", entity)
	}

	// Must have at least one SET clause
	if len(s.SetClauses) == 0 {
		return fmt.Errorf("UPDATE requires at least one SET clause")
	}

	// FROM clause in UPDATE not supported (T-SQL extension)
	if s.From != nil {
		return fmt.Errorf("UPDATE ... FROM is not supported")
	}

	return nil
}

func (v *Validator) validateDelete(s *ast.DeleteStatement) error {
	// WHERE is required
	if s.Where == nil {
		return fmt.Errorf("DELETE without WHERE clause is not permitted")
	}

	// Extract entity name
	entity := extractDeleteEntity(s)
	if entity == "" {
		return fmt.Errorf("table name required")
	}

	entity = normalizeEntityName(entity)
	if !v.EntityExists(entity) {
		return fmt.Errorf("entity '%s' does not exist", entity)
	}

	return nil
}

// normalizeEntityName cleans up an entity name
func normalizeEntityName(name string) string {
	// Remove schema prefix (dbo., etc.)
	if idx := strings.LastIndex(name, "."); idx != -1 {
		name = name[idx+1:]
	}

	// Remove brackets [name]
	name = strings.TrimPrefix(name, "[")
	name = strings.TrimSuffix(name, "]")

	// Remove quotes "name"
	name = strings.Trim(name, "\"")

	// Lowercase
	return strings.ToLower(name)
}

// extractDeleteEntity gets the entity name from a DELETE statement
func extractDeleteEntity(s *ast.DeleteStatement) string {
	if s.Table != nil {
		return s.Table.String()
	}
	if s.From != nil && len(s.From.Tables) > 0 {
		if tn, ok := s.From.Tables[0].(*ast.TableName); ok {
			return tn.Name.String()
		}
	}
	return ""
}

// ValidateSchemaDir checks if the schema directory exists
func ValidateSchemaDir(schemaDir string) error {
	info, err := os.Stat(schemaDir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("schema directory does not exist: %s", schemaDir)
		}
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("schema path is not a directory: %s", schemaDir)
	}
	return nil
}

// GetSchemaPath returns the full path to an entity's schema
func GetSchemaPath(schemaDir, entity string) string {
	return filepath.Join(schemaDir, entity)
}
