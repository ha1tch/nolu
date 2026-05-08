// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package validation

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAllSchemas(t *testing.T) {
	t.Run("nonexistent directory", func(t *testing.T) {
		v := NewJSONSchemaValidator("/nonexistent/path")
		err := v.LoadAllSchemas()
		if err != nil {
			t.Errorf("Expected nil error for nonexistent dir, got: %v", err)
		}
	})

	t.Run("empty directory", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "val-test")
		defer os.RemoveAll(tmpDir)

		v := NewJSONSchemaValidator(tmpDir)
		err := v.LoadAllSchemas()
		if err != nil {
			t.Errorf("Expected nil error for empty dir, got: %v", err)
		}
	})

	t.Run("loads schema files", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "val-test")
		defer os.RemoveAll(tmpDir)

		// Write a schema file
		schema := `{"type":"object","properties":{"name":{"type":"string"}},"required":["name"]}`
		os.WriteFile(filepath.Join(tmpDir, "items.json"), []byte(schema), 0644)

		// Write a non-JSON file (should be skipped)
		os.WriteFile(filepath.Join(tmpDir, "readme.txt"), []byte("ignore me"), 0644)

		// Create a subdirectory (should be skipped)
		os.MkdirAll(filepath.Join(tmpDir, "subdir"), 0755)

		v := NewJSONSchemaValidator(tmpDir)
		err := v.LoadAllSchemas()
		if err != nil {
			t.Fatalf("LoadAllSchemas failed: %v", err)
		}

		if !v.HasSchema("items") {
			t.Error("Expected items schema to be loaded")
		}
	})

	t.Run("handles invalid JSON", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "val-test")
		defer os.RemoveAll(tmpDir)

		os.WriteFile(filepath.Join(tmpDir, "bad.json"), []byte("{invalid}"), 0644)

		v := NewJSONSchemaValidator(tmpDir)
		err := v.LoadAllSchemas()
		if err == nil {
			t.Error("Expected error for invalid JSON schema file")
		}
	})
}

func TestSaveSchema(t *testing.T) {
	t.Run("saves and loads schema", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "val-test")
		defer os.RemoveAll(tmpDir)

		v := NewJSONSchemaValidator(tmpDir)

		schema := map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"name": map[string]interface{}{"type": "string"},
			},
			"required": []interface{}{"name"},
		}

		err := v.SaveSchema("widgets", schema)
		if err != nil {
			t.Fatalf("SaveSchema failed: %v", err)
		}

		// Schema should be in memory
		if !v.HasSchema("widgets") {
			t.Error("Expected widgets schema in memory")
		}

		// File should exist on disk
		schemaFile := filepath.Join(tmpDir, "widgets.json")
		if _, err := os.Stat(schemaFile); os.IsNotExist(err) {
			t.Error("Expected schema file on disk")
		}

		// A fresh validator should be able to load it
		v2 := NewJSONSchemaValidator(tmpDir)
		err = v2.LoadSchemaFromFile("widgets")
		if err != nil {
			t.Fatalf("LoadSchemaFromFile failed: %v", err)
		}
		if !v2.HasSchema("widgets") {
			t.Error("Expected widgets schema in fresh validator")
		}
	})

	t.Run("creates schema directory if missing", func(t *testing.T) {
		tmpDir, _ := os.MkdirTemp("", "val-test")
		defer os.RemoveAll(tmpDir)

		nestedDir := filepath.Join(tmpDir, "schemas", "deep")
		v := NewJSONSchemaValidator(nestedDir)

		err := v.SaveSchema("test", map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"x": map[string]interface{}{"type": "string"},
			},
		})
		if err != nil {
			t.Fatalf("SaveSchema failed: %v", err)
		}

		if _, err := os.Stat(nestedDir); os.IsNotExist(err) {
			t.Error("Expected nested directory to be created")
		}
	})
}

func TestValidate_TypeChecks(t *testing.T) {
	v := NewJSONSchemaValidator("")
	v.LoadSchema("test", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"count":  map[string]interface{}{"type": "number"},
			"active": map[string]interface{}{"type": "boolean"},
			"tags":   map[string]interface{}{"type": "array"},
			"meta":   map[string]interface{}{"type": "object"},
		},
	})

	t.Run("integer accepted as number", func(t *testing.T) {
		ok, errs := v.Validate("test", map[string]interface{}{"count": float64(42)})
		if !ok {
			t.Errorf("Expected pass, got errors: %v", errs)
		}
	})

	t.Run("string rejected for number field", func(t *testing.T) {
		ok, _ := v.Validate("test", map[string]interface{}{"count": "not a number"})
		if ok {
			t.Error("Expected validation failure for string in number field")
		}
	})

	t.Run("boolean field", func(t *testing.T) {
		ok, errs := v.Validate("test", map[string]interface{}{"active": true})
		if !ok {
			t.Errorf("Expected pass, got: %v", errs)
		}
	})

	t.Run("array field", func(t *testing.T) {
		ok, errs := v.Validate("test", map[string]interface{}{"tags": []interface{}{"a"}})
		if !ok {
			t.Errorf("Expected pass, got: %v", errs)
		}
	})

	t.Run("object field", func(t *testing.T) {
		ok, errs := v.Validate("test", map[string]interface{}{
			"meta": map[string]interface{}{"key": "val"},
		})
		if !ok {
			t.Errorf("Expected pass, got: %v", errs)
		}
	})
}
