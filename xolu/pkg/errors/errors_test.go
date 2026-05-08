// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package errors

import (
	"fmt"
	"testing"
)

func TestAPIError_Error(t *testing.T) {
	e := &APIError{
		Code:    ErrEntityNotFound,
		Message: "sensors with ID 42 not found",
		Status:  404,
	}
	got := e.Error()
	want := "OLU-ST001: sensors with ID 42 not found"
	if got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestNew(t *testing.T) {
	e := New(ErrInvalidID, 400, "ID %d is out of range", -1)
	if e.Code != ErrInvalidID {
		t.Errorf("Code = %q, want %q", e.Code, ErrInvalidID)
	}
	if e.Status != 400 {
		t.Errorf("Status = %d, want 400", e.Status)
	}
	if e.Message != "ID -1 is out of range" {
		t.Errorf("Message = %q, want %q", e.Message, "ID -1 is out of range")
	}
}

func TestWrap(t *testing.T) {
	orig := fmt.Errorf("disk full")
	e := Wrap(ErrStorageFailed, 500, orig)
	if e.Code != ErrStorageFailed {
		t.Errorf("Code = %q, want %q", e.Code, ErrStorageFailed)
	}
	if e.Message != "disk full" {
		t.Errorf("Message = %q, want %q", e.Message, "disk full")
	}
}

func TestCodeFormat(t *testing.T) {
	// Verify all codes follow OLU-SSNNN format
	codes := []Code{
		ErrEntityNotFound, ErrEntityExists, ErrInvalidEntity, ErrInvalidID,
		ErrVersionConflict, ErrStorageFailed, ErrEntityTooLarge,
		ErrSchemaNotFound, ErrSchemaLoadFailed,
		ErrCycleDetected, ErrGraphDisabled, ErrGraphUnsupported, ErrGraphFailed,
		ErrGraphVisitedLimit, ErrGraphResultLimit,
		ErrQuerySyntax, ErrQueryDepthExceeded, ErrQueryNotFound, ErrQueryFailed,
		ErrQueryRequired, ErrQueryIDRequired, ErrQueryEngineNotInit,
		ErrQueryTimeout, ErrQueryRowLimit, ErrQueryScanLimit, ErrQueryResponseSize,
		ErrSearchFailed, ErrSearchDisabled,
		ErrValidationFailed, ErrInvalidJSON, ErrMissingParam,
		ErrUnauthorized, ErrInvalidToken, ErrForbidden,
		ErrRateLimited,
		ErrTenantNotFound, ErrTenantRequired,
		ErrConfigInvalid,
	}

	seen := make(map[Code]bool, len(codes))
	for _, code := range codes {
		if seen[code] {
			t.Errorf("Duplicate error code: %q", code)
		}
		seen[code] = true

		s := string(code)
		if len(s) != 9 {
			t.Errorf("Code %q has length %d, want 9 (OLU-SSNNN)", s, len(s))
			continue
		}
		if s[:4] != "OLU-" {
			t.Errorf("Code %q does not start with OLU-", s)
		}
		// Last 3 chars should be digits
		for _, c := range s[6:] {
			if c < '0' || c > '9' {
				t.Errorf("Code %q has non-digit in numeric portion", s)
				break
			}
		}
	}
}
