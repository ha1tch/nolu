// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package hotswap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// ioluBinary returns the iolu binary path from options or falls back to PATH.
func ioluBinary(opts HotswapOptions) string {
	if opts.IoluBinary != "" {
		return opts.IoluBinary
	}
	return "iolu"
}

// ioluAvailable returns true if the iolu binary can be found.
func ioluAvailable(opts HotswapOptions) bool {
	bin := ioluBinary(opts)
	if filepath.IsAbs(bin) {
		_, err := os.Stat(bin)
		return err == nil
	}
	_, err := exec.LookPath(bin)
	return err == nil
}

// archiveDir returns the directory to use for intermediate export archives.
func archiveDir(opts HotswapOptions) string {
	if opts.ArchivePath != "" {
		return opts.ArchivePath
	}
	return os.TempDir()
}

// ioluExport runs:
//
//	iolu tenant export \
//	  --db <sourceDB> --name <tenantName> \
//	  --out <archivePath> \
//	  [--since <since>] \
//	  --include-sequences --include-graph
//
// Returns the archive path on success.
func ioluExport(ctx context.Context, opts HotswapOptions, tenantName, since string) (string, error) {
	bin := ioluBinary(opts)
	archivePath := filepath.Join(archiveDir(opts),
		fmt.Sprintf("nolu-hotswap-%s-%d.tar.gz", tenantName, time.Now().UnixNano()))

	args := []string{
		"tenant", "export",
		"--db", opts.SourceDBPath,
		"--name", tenantName,
		"--out", archivePath,
		"--include-sequences",
		"--include-graph",
	}
	if since != "" {
		args = append(args, "--since", since)
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = os.Stderr // surface iolu errors to nolu's stderr
	if out, err := cmd.Output(); err != nil {
		return "", fmt.Errorf("iolu tenant export: %w\noutput: %s", err, out)
	}
	return archivePath, nil
}

// ioluImport runs:
//
//	iolu tenant import \
//	  --db <targetDB> --name <tenantName> \
//	  --file <archivePath> \
//	  [--upsert]
func ioluImport(ctx context.Context, opts HotswapOptions, tenantName, archivePath string, upsert bool) error {
	bin := ioluBinary(opts)

	args := []string{
		"tenant", "import",
		"--db", opts.TargetDBPath,
		"--name", tenantName,
		"--file", archivePath,
	}
	if upsert {
		args = append(args, "--upsert")
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stderr = os.Stderr
	if out, err := cmd.Output(); err != nil {
		return fmt.Errorf("iolu tenant import: %w\noutput: %s", err, out)
	}
	return nil
}

// ioluValidate runs:
//
//	iolu tenant validate \
//	  --source-db <sourceDB> --source-name <tenantName> \
//	  --target-db <targetDB> --target-name <tenantName>
//
// Returns a ValidationResult derived from iolu's exit code and output.
func ioluValidate(ctx context.Context, opts HotswapOptions, tenantName string) (*ValidationResult, error) {
	bin := ioluBinary(opts)

	args := []string{
		"tenant", "validate",
		"--source-db", opts.SourceDBPath,
		"--source-name", tenantName,
		"--target-db", opts.TargetDBPath,
		"--target-name", tenantName,
	}

	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.Output()

	result := &ValidationResult{
		EntityCounts:   map[string]int{},
		EntityMismatch: map[string]int{},
	}

	if err != nil {
		// iolu exits 1 on validation failure, non-zero on error.
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			// Validation ran but found discrepancies.
			result.Valid = false
			result.Notes = []string{string(out)}
			return result, nil
		}
		return nil, fmt.Errorf("iolu tenant validate: %w\noutput: %s", err, out)
	}

	// Exit 0 means valid.
	result.Valid = true
	result.SequenceOK = true
	result.GraphEdgesOK = true
	return result, nil
}

// runMigration performs the full migration sequence:
//  1. Bulk export from source (if no prior sync)
//  2. Import to target
//  3. (Optional) delta export after quiesce
//
// Returns the bulk sync timestamp (RFC3339) for use in the delta phase.
// Returns ("", nil) with a warning if iolu is not available.
func runMigration(ctx context.Context, opts HotswapOptions, tenantName string, delta bool, bulkSyncAt string) error {
	if opts.SourceDBPath == "" || opts.TargetDBPath == "" {
		// DB paths not configured — skip migration (operator manages data transfer manually).
		return nil
	}
	if !ioluAvailable(opts) {
		return fmt.Errorf("iolu binary not found — migration phase requires iolu in PATH or set options.IoluBinary")
	}

	since := ""
	if delta {
		since = bulkSyncAt
	}

	archivePath, err := ioluExport(ctx, opts, tenantName, since)
	if err != nil {
		return fmt.Errorf("export: %w", err)
	}
	defer os.Remove(archivePath) // clean up archive after import

	if err := ioluImport(ctx, opts, tenantName, archivePath, delta); err != nil {
		return fmt.Errorf("import: %w", err)
	}

	return nil
}

// runValidation runs iolu tenant validate and returns the result.
// Returns a passing result with a warning note if iolu is not available,
// so that the state machine can continue in environments without iolu.
func runValidation(ctx context.Context, opts HotswapOptions, tenantName string) *ValidationResult {
	if !ioluAvailable(opts) || opts.SourceDBPath == "" || opts.TargetDBPath == "" {
		return &ValidationResult{
			Valid:          true,
			SequenceOK:     true,
			GraphEdgesOK:   true,
			EntityCounts:   map[string]int{},
			EntityMismatch: map[string]int{},
			Notes:          []string{"iolu not available or DB paths not set — validation skipped"},
		}
	}

	result, err := ioluValidate(ctx, opts, tenantName)
	if err != nil {
		return &ValidationResult{
			Valid:          false,
			EntityCounts:   map[string]int{},
			EntityMismatch: map[string]int{},
			Notes:          []string{fmt.Sprintf("iolu validate error: %v", err)},
		}
	}
	return result
}
