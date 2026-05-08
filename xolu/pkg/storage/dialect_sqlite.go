// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package storage

import (
	"fmt"
	"strconv"
	"strings"
)

// pow10 returns 10^n for n in [0, 18]. Panics for n outside this range.
var pow10 = [19]int64{
	1,
	10,
	100,
	1_000,
	10_000,
	100_000,
	1_000_000,
	10_000_000,
	100_000_000,
	1_000_000_000,
	10_000_000_000,
	100_000_000_000,
	1_000_000_000_000,
	10_000_000_000_000,
	100_000_000_000_000,
	1_000_000_000_000_000,
	10_000_000_000_000_000,
	100_000_000_000_000_000,
	1_000_000_000_000_000_000,
}

// SQLiteStorageDialect implements StorageDialect for SQLite.
type SQLiteStorageDialect struct{}

func (d *SQLiteStorageDialect) Name() string { return "sqlite" }

func (d *SQLiteStorageDialect) Placeholder(_ int) string { return "?" }

func (d *SQLiteStorageDialect) ColumnType(jsonType, format string, precision, scale int) string {
	if format == "decimal" {
		return "INTEGER" // Exact decimal stored as scaled integer
	}
	switch jsonType {
	case "string":
		return "TEXT"
	case "integer":
		return "INTEGER"
	case "number":
		return "REAL"
	case "boolean":
		return "INTEGER" // SQLite has no native boolean
	case "array", "object":
		return "TEXT" // Stored as JSON text
	default:
		return "TEXT"
	}
}

func (d *SQLiteStorageDialect) CreateTableSQL(spec *AdaptedTableSpec) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s (\n", spec.TableName()))
	b.WriteString("    id INTEGER NOT NULL,\n")
	b.WriteString("    tenant_id INTEGER NOT NULL DEFAULT 0,\n")

	for _, col := range spec.Columns {
		nullable := "NOT NULL"
		if !col.Required {
			nullable = ""
		}
		if nullable != "" {
			b.WriteString(fmt.Sprintf("    %s %s %s,\n", col.Name, col.SQLType, nullable))
		} else {
			b.WriteString(fmt.Sprintf("    %s %s,\n", col.Name, col.SQLType))
		}
	}

	if spec.HasExtra {
		b.WriteString("    _extra TEXT,\n")
	}

	b.WriteString("    _version INTEGER NOT NULL DEFAULT 1,\n")
	b.WriteString("    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,\n")
	b.WriteString("    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,\n")
	b.WriteString("    PRIMARY KEY (tenant_id, id)\n")
	b.WriteString(");\n")

	return b.String()
}

func (d *SQLiteStorageDialect) CreateIndexSQL(spec *AdaptedTableSpec) []string {
	var stmts []string

	stmts = append(stmts, fmt.Sprintf(
		"CREATE INDEX IF NOT EXISTS idx_%s_tenant ON %s(tenant_id);",
		spec.TableName(), spec.TableName()))

	for _, idx := range spec.Indexes {
		cols := strings.Join(idx.Columns, ", ")
		unique := ""
		if idx.Unique {
			unique = "UNIQUE "
		}
		stmts = append(stmts, fmt.Sprintf(
			"CREATE %sINDEX IF NOT EXISTS %s ON %s(%s);",
			unique, idx.Name, spec.TableName(), cols))
	}

	return stmts
}

func (d *SQLiteStorageDialect) InsertSQL(spec *AdaptedTableSpec, hasExtra bool) (string, []string) {
	cols := []string{"id", "tenant_id"}
	placeholders := []string{"?", "?"}

	for _, col := range spec.Columns {
		cols = append(cols, col.Name)
		placeholders = append(placeholders, "?")
	}

	if hasExtra {
		cols = append(cols, "_extra")
		placeholders = append(placeholders, "?")
	}

	sql := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		spec.TableName(),
		strings.Join(cols, ", "),
		strings.Join(placeholders, ", "))

	return sql, cols
}

func (d *SQLiteStorageDialect) SelectSQL(spec *AdaptedTableSpec) string {
	cols := make([]string, 0, len(spec.Columns)+3)
	for _, col := range spec.Columns {
		cols = append(cols, col.Name)
	}
	if spec.HasExtra {
		cols = append(cols, "_extra")
	}
	cols = append(cols, "_version")

	return fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = ? AND id = ?",
		strings.Join(cols, ", "), spec.TableName())
}

func (d *SQLiteStorageDialect) SelectAllSQL(spec *AdaptedTableSpec) string {
	cols := []string{"id"}
	for _, col := range spec.Columns {
		cols = append(cols, col.Name)
	}
	if spec.HasExtra {
		cols = append(cols, "_extra")
	}
	cols = append(cols, "_version")

	return fmt.Sprintf("SELECT %s FROM %s WHERE tenant_id = ? ORDER BY id",
		strings.Join(cols, ", "), spec.TableName())
}

func (d *SQLiteStorageDialect) UpdateSQL(spec *AdaptedTableSpec, versionCheck bool) string {
	setClauses := make([]string, 0, len(spec.Columns)+3)

	for _, col := range spec.Columns {
		setClauses = append(setClauses, col.Name+" = ?")
	}

	if spec.HasExtra {
		setClauses = append(setClauses, "_extra = ?")
	}

	setClauses = append(setClauses, "_version = _version + 1")
	setClauses = append(setClauses, "updated_at = CURRENT_TIMESTAMP")

	where := "WHERE tenant_id = ? AND id = ?"
	if versionCheck {
		where += " AND _version = ?"
	}

	return fmt.Sprintf("UPDATE %s SET %s %s",
		spec.TableName(),
		strings.Join(setClauses, ", "),
		where)
}

func (d *SQLiteStorageDialect) DeleteSQL(spec *AdaptedTableSpec) string {
	return fmt.Sprintf("DELETE FROM %s WHERE tenant_id = ? AND id = ?", spec.TableName())
}

func (d *SQLiteStorageDialect) ExistsSQL(spec *AdaptedTableSpec) string {
	return fmt.Sprintf(
		"SELECT EXISTS(SELECT 1 FROM %s WHERE tenant_id = ? AND id = ?)",
		spec.TableName())
}

func (d *SQLiteStorageDialect) MetadataTableSQL() string {
	return `CREATE TABLE IF NOT EXISTS adapted_table_schemas (
    entity_type TEXT PRIMARY KEY,
    schema_hash TEXT NOT NULL,
    column_spec TEXT NOT NULL,
    has_extra   INTEGER NOT NULL DEFAULT 1,
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);`
}

// NormaliseDecimal converts a decimal string to a scaled int64 string
// for SQLite INTEGER storage. The value is multiplied by 10^scale.
//
// Examples for precision=6, scale=2:
//
//	"19.90"    → "1990"
//	"-19.90"   → "-1990"
//	"0"        → "0"
//	"9999.99"  → "999999"
//	"-0.01"    → "-1"
//
// The scaled integer fits in int64 for precision up to 18.
func (d *SQLiteStorageDialect) NormaliseDecimal(value string, precision, scale int) (string, error) {
	if precision <= 0 {
		precision = 18
	}
	if scale < 0 || scale > 18 {
		return "", fmt.Errorf("scale %d out of range [0, 18]", scale)
	}

	// Parse sign
	s := strings.TrimSpace(value)
	negative := false
	if len(s) > 0 && s[0] == '-' {
		negative = true
		s = s[1:]
	} else if len(s) > 0 && s[0] == '+' {
		s = s[1:]
	}

	if len(s) == 0 {
		return "", fmt.Errorf("invalid decimal value: %q", value)
	}

	// Split into integer and fractional parts
	intPart := s
	fracPart := ""
	if dot := strings.IndexByte(s, '.'); dot >= 0 {
		intPart = s[:dot]
		fracPart = s[dot+1:]
	}

	// Strip leading zeroes from integer part (but keep at least one digit)
	intPart = strings.TrimLeft(intPart, "0")
	if intPart == "" {
		intPart = "0"
	}

	// Validate: all digits
	for _, c := range intPart {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("invalid decimal value: %q", value)
		}
	}
	for _, c := range fracPart {
		if c < '0' || c > '9' {
			return "", fmt.Errorf("invalid decimal value: %q", value)
		}
	}

	// Truncate or pad fractional part to exactly `scale` digits.
	// We round half-up if truncating.
	if len(fracPart) > scale {
		// Check if we need to round up
		roundUp := fracPart[scale] >= '5'
		fracPart = fracPart[:scale]
		if roundUp {
			// Increment the last digit, propagating carry
			digits := []byte(intPart + fracPart)
			carry := true
			for i := len(digits) - 1; i >= 0 && carry; i-- {
				digits[i]++
				if digits[i] > '9' {
					digits[i] = '0'
				} else {
					carry = false
				}
			}
			if carry {
				digits = append([]byte{'1'}, digits...)
			}
			combined := string(digits)
			intPart = combined[:len(combined)-scale]
			if scale > 0 {
				fracPart = combined[len(combined)-scale:]
			}
		}
	} else {
		for len(fracPart) < scale {
			fracPart += "0"
		}
	}

	// Check precision: integer part must fit in (precision - scale) digits
	maxIntDigits := precision - scale
	if maxIntDigits <= 0 {
		maxIntDigits = 1
	}
	if len(intPart) > maxIntDigits {
		return "", fmt.Errorf("value %q exceeds precision(%d,%d): integer part requires more than %d digits",
			value, precision, scale, maxIntDigits)
	}

	// Combine integer + fractional as a single integer string (the scaled value)
	combined := intPart + fracPart
	combined = strings.TrimLeft(combined, "0")
	if combined == "" {
		return "0", nil // -0 normalises to 0
	}

	// Parse as int64 and check bounds
	n, err := strconv.ParseInt(combined, 10, 64)
	if err != nil {
		return "", fmt.Errorf("value %q exceeds int64 range when scaled to %d decimal places",
			value, scale)
	}
	_ = n // bounds check passed

	if negative {
		return "-" + combined, nil
	}
	return combined, nil
}

// DenormaliseDecimal converts a scaled int64 string back to a
// client-facing decimal string by dividing by 10^scale.
//
//	"1990"  with scale=2 → "19.90"
//	"-1990" with scale=2 → "-19.90"
//	"0"     with scale=2 → "0.00"
func (d *SQLiteStorageDialect) DenormaliseDecimal(value string, precision, scale int) string {
	if value == "" || scale < 0 {
		return value
	}

	// Scale 0: the stored integer IS the decimal value
	if scale == 0 {
		return value
	}

	// Parse as int64
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return value // pass through if unparseable
	}

	negative := n < 0
	if negative {
		n = -n
	}

	// Split into integer and fractional parts
	var divisor int64
	if scale <= 18 {
		divisor = pow10[scale]
	} else {
		return value // shouldn't happen with our constraints
	}

	intPart := n / divisor
	fracPart := n % divisor

	// Format fractional part with leading zeroes to `scale` digits
	fracStr := strconv.FormatInt(fracPart, 10)
	for len(fracStr) < scale {
		fracStr = "0" + fracStr
	}

	prefix := ""
	if negative {
		prefix = "-"
	}

	return prefix + strconv.FormatInt(intPart, 10) + "." + fracStr
}

// SupportsNativeDecimalAggregation returns false for SQLite.
// Although SQLite can SUM integers correctly, the scaled representation
// requires division by the scale factor to produce correct decimal
// results. Go-side aggregation with shopspring/decimal avoids this
// complexity and handles AVG correctly.
func (d *SQLiteStorageDialect) SupportsNativeDecimalAggregation() bool {
	return false
}
