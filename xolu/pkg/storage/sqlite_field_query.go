// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ha1tch/xolu/pkg/jsonic"
)

// Compile-time check: SQLiteStore implements FieldQueryable.
var _ FieldQueryable = (*SQLiteStore)(nil)

// Compile-time check: SQLiteStore implements FilterableStore.
var _ FilterableStore = (*SQLiteStore)(nil)

// ListWithFieldsAndFilter returns records for an entity, extracting
// only the named fields and evaluating predicates inline during
// tokenisation. Rows that fail the predicates are never materialised
// as maps. For adapted entities this falls through to the regular path.
func (s *SQLiteStore) ListWithFieldsAndFilter(ctx context.Context, entity string, fields []string, preds *jsonic.PredicateSet) ([]map[string]interface{}, error) {
	// Adapted tables don't benefit — their List already reads native columns.
	if spec := s.adapted.Get(entity); spec != nil {
		return adaptedList(ctx, s.readDB, spec, s.dialect, int(s.config.TenantID))
	}

	// No predicates: fall through to field extraction without filtering.
	if preds == nil || preds.Len() == 0 {
		return s.ListWithFields(ctx, entity, fields)
	}

	// No field restriction with predicates: extract all fields but still filter.
	// This path handles SELECT * with WHERE on blob entities.

	const listSQL = `
		SELECT data, _version FROM entities
		WHERE tenant_id = ? AND entity_type = ?
		ORDER BY id
	`
	stmt, err := s.stmtCache.Get(listSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare list: %w", err)
	}

	rows, err := stmt.QueryContext(ctx, int(s.config.TenantID), entity)
	if err != nil {
		return nil, fmt.Errorf("failed to list entities: %w", err)
	}
	defer rows.Close()

	outputFields := jsonic.MakeFilterFieldEntries(fields)

	var results []map[string]interface{}
	tok := jsonic.GetTokeniser()
	defer jsonic.PutTokeniser(tok)

	for rows.Next() {
		var jsonData []byte
		var version int
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		if err := tok.Tokenise(jsonData); err != nil {
			continue // skip malformed JSON
		}

		result := jsonic.FilterExtractFromTokens(tok, outputFields, preds)
		if !result.Passed {
			continue
		}

		data := result.Data
		if data == nil {
			data = make(map[string]interface{})
		}
		data["_version"] = version
		results = append(results, data)
	}

	return results, rows.Err()
}

// ListWithFields returns all records for an entity, extracting only the
// named fields from each JSON blob. For adapted entities this falls
// through to the regular List path (native columns are already efficient).
func (s *SQLiteStore) ListWithFields(ctx context.Context, entity string, fields []string) ([]map[string]interface{}, error) {
	// Adapted tables don't benefit — their List already reads native columns.
	if spec := s.adapted.Get(entity); spec != nil {
		return adaptedList(ctx, s.readDB, spec, s.dialect, int(s.config.TenantID))
	}

	// No field restriction: fall back to full deserialisation.
	if len(fields) == 0 {
		return s.List(ctx, entity)
	}

	const listSQL = `
		SELECT data, _version FROM entities
		WHERE tenant_id = ? AND entity_type = ?
		ORDER BY id
	`
	stmt, err := s.stmtCache.Get(listSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to prepare list: %w", err)
	}

	rows, err := stmt.QueryContext(ctx, int(s.config.TenantID), entity)
	if err != nil {
		return nil, fmt.Errorf("failed to list entities: %w", err)
	}
	defer rows.Close()

	// Pre-compute atoms for the requested fields.
	atomSet := buildAtomSet(fields)

	var results []map[string]interface{}
	tok := jsonic.GetTokeniser()
	defer jsonic.PutTokeniser(tok)

	for rows.Next() {
		var jsonData []byte
		var version int
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, fmt.Errorf("failed to scan row: %w", err)
		}

		data := extractFieldsFromBlob(tok, jsonData, atomSet)
		data["_version"] = version
		results = append(results, data)
	}

	return results, rows.Err()
}

// QueryWithFields executes a push-down SQL query and extracts only the
// named fields from each result's JSON blob.
func (s *SQLiteStore) QueryWithFields(ctx context.Context, sqlQuery string, args []interface{}, fields []string) ([]map[string]interface{}, error) {
	stmt, err := s.stmtCache.Get(sqlQuery)
	if err != nil {
		return nil, fmt.Errorf("push-down prepare failed: %w", err)
	}

	rows, err := stmt.QueryContext(ctx, args...)
	if err != nil {
		return nil, fmt.Errorf("push-down query failed: %w", err)
	}
	defer rows.Close()

	atomSet := buildAtomSet(fields)

	var results []map[string]interface{}
	tok := jsonic.GetTokeniser()
	defer jsonic.PutTokeniser(tok)

	for rows.Next() {
		var jsonData []byte
		var version int64
		if err := rows.Scan(&jsonData, &version); err != nil {
			return nil, fmt.Errorf("scan push-down row: %w", err)
		}

		data := extractFieldsFromBlob(tok, jsonData, atomSet)
		data["_version"] = version
		results = append(results, data)
	}

	return results, rows.Err()
}

// atomEntry pairs an Atom with its original field name for lookup.
type atomEntry struct {
	atom jsonic.Atom
	name string
}

// buildAtomSet creates a lookup structure for the requested field names.
func buildAtomSet(fields []string) []atomEntry {
	entries := make([]atomEntry, len(fields))
	for i, f := range fields {
		entries[i] = atomEntry{
			atom: jsonic.MakeAtom(f),
			name: f,
		}
	}
	return entries
}

// extractFieldsFromBlob tokenises a JSON blob and extracts only the
// fields whose atoms match the requested set. Values are deserialised
// from the raw byte slice to preserve correct Go types (float64 for
// numbers, bool for booleans, etc.).
func extractFieldsFromBlob(tok *jsonic.Tokeniser, blob []byte, fields []atomEntry) map[string]interface{} {
	data := make(map[string]interface{}, len(fields))

	if err := tok.Tokenise(blob); err != nil {
		// Malformed JSON — return empty map rather than error.
		return data
	}

	tokens := tok.Tokens()
	input := tok.Input()
	n := len(tokens)

	// Expect ObjStart
	i := 0
	if i >= n || tokens[i].Type != jsonic.TokObjStart {
		return data
	}
	i++

	found := 0
	total := len(fields)

	for i < n && found < total {
		if tokens[i].Type == jsonic.TokObjEnd {
			break
		}
		if tokens[i].Type == jsonic.TokComma {
			i++
			continue
		}

		// Key
		if tokens[i].Type != jsonic.TokString {
			i++
			continue
		}
		keyBytes := input[tokens[i].Start:tokens[i].End]
		keyAtom := jsonic.MakeAtomBytes(keyBytes)
		i++ // past key

		// Colon
		if i < n && tokens[i].Type == jsonic.TokColon {
			i++
		}

		// Value
		if i >= n {
			break
		}

		// Check if this key matches any requested field.
		var matchName string
		for _, entry := range fields {
			if entry.atom == keyAtom {
				matchName = entry.name
				break
			}
		}

		if matchName != "" {
			// Extract the value from the raw input.
			val := tokenToValue(tokens, input, i)
			if val != nil {
				data[matchName] = val
				found++
			}
		}

		// Skip the value (may be nested object/array).
		i = jsonic.SkipValue(tokens, i)
	}

	return data
}

// tokenToValue converts a token at position i into the correct Go type
// by examining the token type and parsing the raw bytes.
func tokenToValue(tokens []jsonic.Token, input []byte, i int) interface{} {
	if i >= len(tokens) {
		return nil
	}
	tok := tokens[i]
	switch tok.Type {
	case jsonic.TokString:
		return string(input[tok.Start:tok.End])
	case jsonic.TokNumber:
		// Use json.Unmarshal to get the correct numeric type.
		// json.Unmarshal into interface{} produces float64.
		raw := input[tok.Start:tok.End]
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return string(raw) // fallback
		}
		return v
	case jsonic.TokTrue:
		return true
	case jsonic.TokFalse:
		return false
	case jsonic.TokNull:
		return nil
	case jsonic.TokObjStart, jsonic.TokArrStart:
		// Nested object/array — deserialise the full subtree.
		// tok.Start points to '{' or '['; the matching close token's
		// End points past '}' or ']'.
		end := jsonic.SkipValue(tokens, i)
		startPos := tok.Start
		var endPos uint32
		if end > 0 && end <= len(tokens) {
			endPos = tokens[end-1].End
		} else {
			endPos = uint32(len(input))
		}
		raw := input[startPos:endPos]
		var v interface{}
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil
		}
		return v
	default:
		return nil
	}
}
