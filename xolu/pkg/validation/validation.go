// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package validation

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ha1tch/queryfy"
	"github.com/ha1tch/queryfy/builders"
	"github.com/ha1tch/queryfy/builders/jsonschema"
)

// Validator interface defines validation operations
type Validator interface {
	Validate(entity string, data map[string]interface{}) (bool, []string)
	LoadSchema(entity string, schemaData map[string]interface{}) error
	SaveSchema(entity string, schemaData map[string]interface{}) error
	HasSchema(entity string) bool
	GetSchema(entity string) (map[string]interface{}, error)
}

// entitySchema holds both the raw JSON Schema (for GetSchema and adapted
// tables) and the compiled queryfy schema (for validation).
type entitySchema struct {
	raw      map[string]interface{}
	compiled *builders.ObjectSchema
}

// JSONSchemaValidator implements JSON schema validation using queryfy.
//
// Schemas are stored in two forms: the raw JSON Schema map (returned by
// GetSchema, consumed by the adapted tables layer) and a compiled queryfy
// ObjectSchema (used for validation). Compilation happens once at load
// time via jsonschema.FromJSON.
//
// Validation runs in Loose mode by default: type coercion is applied
// (e.g., integer values pass for number fields) and additional properties
// are allowed unless the schema explicitly sets additionalProperties:false.
type JSONSchemaValidator struct {
	schemas   map[string]*entitySchema
	schemaDir string
	mu        sync.RWMutex
}

// NewJSONSchemaValidator creates a new JSON schema validator
func NewJSONSchemaValidator(schemaDir string) *JSONSchemaValidator {
	return &JSONSchemaValidator{
		schemas:   make(map[string]*entitySchema),
		schemaDir: schemaDir,
	}
}

// LoadSchema loads a schema for an entity. The raw map is stored for
// GetSchema; a compiled queryfy schema is built for validation.
func (v *JSONSchemaValidator) LoadSchema(entity string, schemaData map[string]interface{}) error {
	// Compile to queryfy schema
	raw, err := json.Marshal(schemaData)
	if err != nil {
		return fmt.Errorf("failed to marshal schema for %q: %w", entity, err)
	}

	compiled, convErrs := jsonschema.FromJSON(raw, &jsonschema.Options{
		StoreUnknown: true,
	})
	for _, e := range convErrs {
		if !e.IsWarning {
			return fmt.Errorf("schema compilation error for %q at %s: %s",
				entity, e.Path, e.Message)
		}
	}

	obj, ok := compiled.(*builders.ObjectSchema)
	if !ok {
		return fmt.Errorf("schema for %q did not compile to an ObjectSchema (got %T)",
			entity, compiled)
	}

	// olu default: allow extra properties unless the schema explicitly
	// sets additionalProperties:false. We use Strict mode for type
	// checking (no silent coercion), so we need to set this explicitly
	// on every ObjectSchema in the tree to avoid Strict mode's default
	// of rejecting extra fields.
	setDefaultAllowAdditional(obj)

	v.mu.Lock()
	defer v.mu.Unlock()

	v.schemas[entity] = &entitySchema{
		raw:      schemaData,
		compiled: obj,
	}
	return nil
}

// LoadSchemaFromFile loads a schema from a file
func (v *JSONSchemaValidator) LoadSchemaFromFile(entity string) error {
	schemaFile := filepath.Join(v.schemaDir, entity+".json")

	data, err := os.ReadFile(schemaFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No schema file, validation passes
		}
		return err
	}

	var schemaData map[string]interface{}
	if err := json.Unmarshal(data, &schemaData); err != nil {
		return err
	}

	return v.LoadSchema(entity, schemaData)
}

// HasSchema checks if a schema exists for an entity
func (v *JSONSchemaValidator) HasSchema(entity string) bool {
	v.mu.RLock()
	defer v.mu.RUnlock()

	_, exists := v.schemas[entity]
	return exists
}

// GetSchema retrieves the raw JSON Schema for an entity.
// This is used by the adapted tables layer and the schema API endpoint.
func (v *JSONSchemaValidator) GetSchema(entity string) (map[string]interface{}, error) {
	v.mu.RLock()
	defer v.mu.RUnlock()

	es, exists := v.schemas[entity]
	if !exists {
		return nil, fmt.Errorf("schema not found for entity: %s", entity)
	}
	return es.raw, nil
}

// LoadedEntities returns the names of all entities that have a loaded schema.
func (v *JSONSchemaValidator) LoadedEntities() []string {
	v.mu.RLock()
	defer v.mu.RUnlock()

	entities := make([]string, 0, len(v.schemas))
	for name := range v.schemas {
		entities = append(entities, name)
	}
	return entities
}

// GetCompiledSchema retrieves the compiled queryfy schema for an entity.
// Returns nil if no schema is loaded.
func (v *JSONSchemaValidator) GetCompiledSchema(entity string) *builders.ObjectSchema {
	v.mu.RLock()
	defer v.mu.RUnlock()

	es, exists := v.schemas[entity]
	if !exists {
		return nil
	}
	return es.compiled
}

// Validate validates data against a schema using queryfy in Loose mode.
//
// Returns (true, nil) if validation passes or no schema exists.
// Returns (false, errors) with human-readable error strings on failure.
func (v *JSONSchemaValidator) Validate(entity string, data map[string]interface{}) (bool, []string) {
	v.mu.RLock()
	es, exists := v.schemas[entity]
	v.mu.RUnlock()

	if !exists {
		// No schema means validation passes
		return true, nil
	}

	ctx := queryfy.NewValidationContext(queryfy.Strict)
	_ = es.compiled.Validate(data, ctx)

	if !ctx.HasErrors() {
		return true, nil
	}

	// Convert queryfy field errors to string slice
	fieldErrors := ctx.Errors()
	errors := make([]string, 0, len(fieldErrors))
	for _, fe := range fieldErrors {
		if fe.Path == "" {
			errors = append(errors, fe.Message)
		} else {
			errors = append(errors, fmt.Sprintf("%s: %s", fe.Path, fe.Message))
		}
	}

	return false, errors
}

// LoadAllSchemas loads all schemas from the schema directory
func (v *JSONSchemaValidator) LoadAllSchemas() error {
	if _, err := os.Stat(v.schemaDir); os.IsNotExist(err) {
		return nil // Schema directory doesn't exist yet
	}

	files, err := os.ReadDir(v.schemaDir)
	if err != nil {
		return err
	}

	for _, file := range files {
		if file.IsDir() || filepath.Ext(file.Name()) != ".json" {
			continue
		}

		entity := file.Name()[:len(file.Name())-5] // Remove .json extension
		if err := v.LoadSchemaFromFile(entity); err != nil {
			return fmt.Errorf("failed to load schema for %s: %w", entity, err)
		}
	}

	return nil
}

// SaveSchema saves a schema to a file
func (v *JSONSchemaValidator) SaveSchema(entity string, schemaData map[string]interface{}) error {
	if err := os.MkdirAll(v.schemaDir, 0755); err != nil {
		return err
	}

	schemaFile := filepath.Join(v.schemaDir, entity+".json")
	data, err := json.MarshalIndent(schemaData, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(schemaFile, data, 0644); err != nil {
		return err
	}

	return v.LoadSchema(entity, schemaData)
}

// setDefaultAllowAdditional recursively sets AllowAdditional(true) on
// every ObjectSchema in the tree that hasn't explicitly declared a policy.
// This ensures Strict mode (used for type checking) doesn't accidentally
// reject extra fields on schemas that didn't opt into that behaviour.
func setDefaultAllowAdditional(obj *builders.ObjectSchema) {
	if _, explicit := obj.AllowsAdditional(); !explicit {
		obj.AllowAdditional(true)
	}

	// Recurse into nested ObjectSchemas
	for _, name := range obj.FieldNames() {
		fieldSchema, ok := obj.GetField(name)
		if !ok {
			continue
		}
		if nested, ok := fieldSchema.(*builders.ObjectSchema); ok {
			setDefaultAllowAdditional(nested)
		}
		// Array items that are objects
		if arr, ok := fieldSchema.(*builders.ArraySchema); ok {
			if elem := arr.ElementSchema(); elem != nil {
				if nestedObj, ok := elem.(*builders.ObjectSchema); ok {
					setDefaultAllowAdditional(nestedObj)
				}
			}
		}
	}
}

// NoOpValidator is a validator that always passes
type NoOpValidator struct{}

// NewNoOpValidator creates a no-op validator
func NewNoOpValidator() *NoOpValidator {
	return &NoOpValidator{}
}

// Validate always returns true
func (n *NoOpValidator) Validate(entity string, data map[string]interface{}) (bool, []string) {
	return true, nil
}

// LoadSchema is a no-op
func (n *NoOpValidator) LoadSchema(entity string, schemaData map[string]interface{}) error {
	return nil
}

// SaveSchema is a no-op
func (n *NoOpValidator) SaveSchema(entity string, schemaData map[string]interface{}) error {
	return nil
}

// HasSchema always returns false
func (n *NoOpValidator) HasSchema(entity string) bool {
	return false
}

// GetSchema always returns error
func (n *NoOpValidator) GetSchema(entity string) (map[string]interface{}, error) {
	return nil, fmt.Errorf("no-op validator has no schemas")
}
