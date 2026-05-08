// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"database/sql"
	"fmt"
)

// SQLiteTenantPersister implements tenant.Persister using the tenants table
// in the shared SQLite database. It uses the writer pool for saves and
// the reader pool for loads.
type SQLiteTenantPersister struct {
	db     *sql.DB // writer
	readDB *sql.DB // reader
}

// NewSQLiteTenantPersister creates a persister backed by the given database
// connections. Both must point to the same SQLite database. The writer is
// used for Save; the reader for LoadAll.
func NewSQLiteTenantPersister(db, readDB *sql.DB) *SQLiteTenantPersister {
	return &SQLiteTenantPersister{db: db, readDB: readDB}
}

// LoadAll returns all persisted tenant name-to-ID mappings.
func (p *SQLiteTenantPersister) LoadAll(ctx context.Context) (map[string]uint16, error) {
	rows, err := p.readDB.QueryContext(ctx,
		`SELECT name, id FROM tenants ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("query tenants: %w", err)
	}
	defer rows.Close()

	result := make(map[string]uint16)
	for rows.Next() {
		var name string
		var id int
		if err := rows.Scan(&name, &id); err != nil {
			return nil, fmt.Errorf("scan tenant row: %w", err)
		}
		if id > 0 && id <= 65535 {
			result[name] = uint16(id)
		}
	}
	return result, rows.Err()
}

// Save persists a tenant mapping. Idempotent: re-saving the same
// (name, id) pair is not an error. Conflicts (same ID with different name,
// or same name with different ID) return an error.
func (p *SQLiteTenantPersister) Save(ctx context.Context, name string, id uint16) error {
	// Check for conflicts explicitly before inserting.
	// This prevents the silent-success problem where ON CONFLICT DO UPDATE
	// with a WHERE clause can match zero rows without error.
	var existingName string
	err := p.readDB.QueryRowContext(ctx,
		`SELECT name FROM tenants WHERE id = ?`, int(id)).Scan(&existingName)
	if err == nil {
		// ID exists — check if it's the same mapping
		if existingName == name {
			return nil // idempotent: exact same mapping
		}
		return fmt.Errorf("tenant ID %d already persisted as %q, cannot reassign to %q", id, existingName, name)
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("check tenant ID %d: %w", id, err)
	}

	var existingID int
	err = p.readDB.QueryRowContext(ctx,
		`SELECT id FROM tenants WHERE name = ?`, name).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("tenant name %q already persisted with ID %d, cannot reassign to ID %d", name, existingID, id)
	} else if err != sql.ErrNoRows {
		return fmt.Errorf("check tenant name %q: %w", name, err)
	}

	// No conflicts — insert
	_, err = p.db.ExecContext(ctx,
		`INSERT INTO tenants (id, name) VALUES (?, ?)`,
		int(id), name)
	if err != nil {
		return fmt.Errorf("persist tenant %q (ID %d): %w", name, id, err)
	}
	return nil
}
