// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
)

// ---------------------------------------------------------------------------
// Schema diff
// ---------------------------------------------------------------------------

// SchemaDiff describes the differences between an old and new
// AdaptedTableSpec. It is the migration plan for schema evolution.
type SchemaDiff struct {
	Added   []ColumnDef // Columns in new but not in old
	Dropped []ColumnDef // Columns in old but not in new
	Changed []ColumnChange // Columns present in both but with incompatible type changes

	IndexesAdded   []IndexDef // Indexes in new but not in old
	IndexesDropped []IndexDef // Indexes in old but not in new

	HasExtraChanged bool // Whether _extra presence changed
	NewHasExtra     bool // The new HasExtra value (only meaningful if HasExtraChanged)
}

// ColumnChange records an incompatible type change for a column.
type ColumnChange struct {
	Name       string
	OldSQLType string
	NewSQLType string
	OldType    string
	NewType    string
}

// IsEmpty reports whether the diff contains no changes.
func (d *SchemaDiff) IsEmpty() bool {
	return len(d.Added) == 0 &&
		len(d.Dropped) == 0 &&
		len(d.Changed) == 0 &&
		len(d.IndexesAdded) == 0 &&
		len(d.IndexesDropped) == 0 &&
		!d.HasExtraChanged
}

// HasTypeConflicts reports whether there are incompatible type changes
// that prevent automatic migration.
func (d *SchemaDiff) HasTypeConflicts() bool {
	return len(d.Changed) > 0
}

// DiffAdaptedSpecs compares two AdaptedTableSpecs and produces a
// migration plan. The old spec represents the currently deployed table;
// the new spec represents the desired state from the updated schema.
func DiffAdaptedSpecs(old, new *AdaptedTableSpec) *SchemaDiff {
	diff := &SchemaDiff{}

	// Build lookup maps by column name.
	oldByName := make(map[string]ColumnDef, len(old.Columns))
	for _, col := range old.Columns {
		oldByName[col.Name] = col
	}
	newByName := make(map[string]ColumnDef, len(new.Columns))
	for _, col := range new.Columns {
		newByName[col.Name] = col
	}

	// Added columns: in new but not in old.
	for _, col := range new.Columns {
		if _, exists := oldByName[col.Name]; !exists {
			diff.Added = append(diff.Added, col)
		}
	}

	// Dropped columns: in old but not in new.
	for _, col := range old.Columns {
		if _, exists := newByName[col.Name]; !exists {
			diff.Dropped = append(diff.Dropped, col)
		}
	}

	// Type changes: same name, different SQL type.
	for _, newCol := range new.Columns {
		if oldCol, exists := oldByName[newCol.Name]; exists {
			if oldCol.SQLType != newCol.SQLType {
				diff.Changed = append(diff.Changed, ColumnChange{
					Name:       newCol.Name,
					OldSQLType: oldCol.SQLType,
					NewSQLType: newCol.SQLType,
					OldType:    oldCol.Type,
					NewType:    newCol.Type,
				})
			}
		}
	}

	// Index diff.
	oldIdxSet := make(map[string]IndexDef)
	for _, idx := range old.Indexes {
		oldIdxSet[idx.Name] = idx
	}
	newIdxSet := make(map[string]IndexDef)
	for _, idx := range new.Indexes {
		newIdxSet[idx.Name] = idx
	}
	for _, idx := range new.Indexes {
		if _, exists := oldIdxSet[idx.Name]; !exists {
			diff.IndexesAdded = append(diff.IndexesAdded, idx)
		}
	}
	for _, idx := range old.Indexes {
		if _, exists := newIdxSet[idx.Name]; !exists {
			diff.IndexesDropped = append(diff.IndexesDropped, idx)
		}
	}

	// HasExtra change.
	if old.HasExtra != new.HasExtra {
		diff.HasExtraChanged = true
		diff.NewHasExtra = new.HasExtra
	}

	return diff
}

// ---------------------------------------------------------------------------
// Migration execution
// ---------------------------------------------------------------------------

// MigrateAdaptedTable applies schema changes to an existing adapted table.
// It computes the diff between the stored spec and the new schema, then:
//
//  1. Rejects type changes (incompatible, require manual intervention)
//  2. Adds new columns via ALTER TABLE ADD COLUMN
//  3. Drops removed columns via ALTER TABLE DROP COLUMN (SQLite 3.35+),
//     after migrating any existing data to the _extra overflow column
//  4. Updates indexes (drop old, create new)
//  5. Updates the metadata row in adapted_table_schemas
//  6. Updates the in-memory registry
//
// The entire migration runs in a single transaction.
func MigrateAdaptedTable(
	ctx context.Context,
	db *sql.DB,
	registry *AdaptedRegistry,
	entity string,
	newSchema map[string]interface{},
	dialect StorageDialect,
) error {
	// Derive the new spec from the updated schema.
	newSpec, err := DeriveAdaptedTableSpec(entity, newSchema, dialect)
	if err != nil {
		return fmt.Errorf("failed to derive new spec for %q: %w", entity, err)
	}

	// Load the old spec from the registry (populated at startup).
	oldSpec := registry.Get(entity)
	if oldSpec == nil {
		return fmt.Errorf("entity %q is not registered as adapted", entity)
	}

	// Compute the diff.
	diff := DiffAdaptedSpecs(oldSpec, newSpec)
	if diff.IsEmpty() {
		// Hash changed but layout didn't (e.g. description change).
		// Just update the hash.
		return updateSchemaMetadata(ctx, db, entity, newSpec)
	}

	// Reject type changes.
	if diff.HasTypeConflicts() {
		var conflicts []string
		for _, c := range diff.Changed {
			conflicts = append(conflicts, fmt.Sprintf(
				"%s: %s(%s) → %s(%s)", c.Name, c.OldSQLType, c.OldType, c.NewSQLType, c.NewType))
		}
		return fmt.Errorf(
			"schema for %q has incompatible type changes that require manual migration: %s",
			entity, strings.Join(conflicts, "; "))
	}

	// Reject HasExtra going from true to false (would lose overflow data).
	if diff.HasExtraChanged && !diff.NewHasExtra && oldSpec.HasExtra {
		return fmt.Errorf(
			"schema for %q changed additionalProperties from true to false; "+
				"existing overflow data would be lost. Migrate manually", entity)
	}

	tableName := newSpec.TableName()

	// Execute in a transaction.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin migration transaction: %w", err)
	}
	defer tx.Rollback() //nolint:errcheck // no-op if commit succeeded

	// 1. Add new columns.
	for _, col := range diff.Added {
		stmt := addColumnSQL(tableName, col, dialect)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to add column %s to %s: %w", col.Name, tableName, err)
		}
	}

	// 2. Drop removed columns.
	// If the table has _extra, migrate column data into it first.
	if len(diff.Dropped) > 0 {
		if oldSpec.HasExtra || newSpec.HasExtra {
			if err := migrateDroppedToExtra(ctx, tx, tableName, diff.Dropped); err != nil {
				return fmt.Errorf("failed to migrate dropped columns to _extra: %w", err)
			}
		}
		for _, col := range diff.Dropped {
			stmt := fmt.Sprintf("ALTER TABLE %s DROP COLUMN %s", tableName, col.Name)
			if _, err := tx.ExecContext(ctx, stmt); err != nil {
				return fmt.Errorf("failed to drop column %s from %s: %w", col.Name, tableName, err)
			}
		}
	}

	// 3. If HasExtra changed from false to true, add _extra column.
	if diff.HasExtraChanged && diff.NewHasExtra {
		stmt := fmt.Sprintf("ALTER TABLE %s ADD COLUMN _extra TEXT", tableName)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			// Column might already exist if it was added in a previous partial migration.
			if !strings.Contains(err.Error(), "duplicate column") {
				return fmt.Errorf("failed to add _extra column: %w", err)
			}
		}
	}

	// 4. Update indexes: drop old, create new.
	for _, idx := range diff.IndexesDropped {
		stmt := fmt.Sprintf("DROP INDEX IF EXISTS %s", idx.Name)
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to drop index %s: %w", idx.Name, err)
		}
	}
	for _, idx := range diff.IndexesAdded {
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		stmt := fmt.Sprintf("CREATE %sINDEX IF NOT EXISTS %s ON %s (%s)",
			unique, idx.Name, tableName, strings.Join(idx.Columns, ", "))
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("failed to create index %s: %w", idx.Name, err)
		}
	}

	// 5. Update metadata.
	columnSpecJSON, err := json.Marshal(newSpec.Columns)
	if err != nil {
		return fmt.Errorf("failed to marshal new column spec: %w", err)
	}
	hasExtraInt := 0
	if newSpec.HasExtra {
		hasExtraInt = 1
	}
	_, err = tx.ExecContext(ctx, `
		UPDATE adapted_table_schemas
		SET schema_hash = ?, column_spec = ?, has_extra = ?
		WHERE entity_type = ?
	`, newSpec.SchemaHash, string(columnSpecJSON), hasExtraInt, entity)
	if err != nil {
		return fmt.Errorf("failed to update adapted_table_schemas: %w", err)
	}

	// Commit.
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit migration: %w", err)
	}

	// 6. Update in-memory registry.
	registry.Set(entity, newSpec)

	return nil
}

// updateSchemaMetadata updates just the hash in the metadata table
// (for non-layout changes like description updates).
func updateSchemaMetadata(ctx context.Context, db *sql.DB, entity string, spec *AdaptedTableSpec) error {
	columnSpecJSON, err := json.Marshal(spec.Columns)
	if err != nil {
		return fmt.Errorf("failed to marshal column spec: %w", err)
	}
	hasExtraInt := 0
	if spec.HasExtra {
		hasExtraInt = 1
	}
	_, err = db.ExecContext(ctx, `
		UPDATE adapted_table_schemas
		SET schema_hash = ?, column_spec = ?, has_extra = ?
		WHERE entity_type = ?
	`, spec.SchemaHash, string(columnSpecJSON), hasExtraInt, entity)
	return err
}

// addColumnSQL generates an ALTER TABLE ADD COLUMN statement.
func addColumnSQL(table string, col ColumnDef, dialect StorageDialect) string {
	return fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, col.Name, col.SQLType)
}

// migrateDroppedToExtra reads existing data from columns about to be
// dropped and merges it into the _extra JSON column, so no data is lost.
func migrateDroppedToExtra(ctx context.Context, tx *sql.Tx, table string, dropped []ColumnDef) error {
	// Build a list of column names to migrate.
	colNames := make([]string, len(dropped))
	for i, col := range dropped {
		colNames[i] = col.Name
	}

	// Read all rows that have non-null values in any dropped column.
	selectCols := append([]string{"id", "_extra"}, colNames...)
	query := fmt.Sprintf("SELECT %s FROM %s", strings.Join(selectCols, ", "), table)

	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return fmt.Errorf("failed to read dropped columns: %w", err)
	}
	defer rows.Close()

	updateSQL := fmt.Sprintf("UPDATE %s SET _extra = ? WHERE id = ?", table)

	for rows.Next() {
		// Scan id, _extra, and all dropped column values.
		vals := make([]interface{}, len(selectCols))
		ptrs := make([]interface{}, len(selectCols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		rowID := vals[0]
		existingExtra := vals[1]

		// Parse existing _extra JSON.
		extra := make(map[string]interface{})
		if s, ok := existingExtra.(string); ok && s != "" {
			_ = json.Unmarshal([]byte(s), &extra) // ignore error — start fresh on bad JSON
		}

		// Merge dropped column values into extra.
		anyAdded := false
		for i, col := range dropped {
			val := vals[i+2] // offset by id and _extra
			if val != nil {
				extra[col.JSONField] = val
				anyAdded = true
			}
		}

		if anyAdded {
			extraJSON, err := json.Marshal(extra)
			if err != nil {
				return fmt.Errorf("failed to marshal _extra: %w", err)
			}
			if _, err := tx.ExecContext(ctx, updateSQL, string(extraJSON), rowID); err != nil {
				return fmt.Errorf("failed to update _extra for row %v: %w", rowID, err)
			}
		}
	}

	return rows.Err()
}
