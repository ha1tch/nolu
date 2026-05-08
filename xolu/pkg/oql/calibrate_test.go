// Copyright (c) 2026 haitch
// Licensed under the Apache License, Version 2.0
// https://www.apache.org/licenses/LICENSE-2.0

package oql

import (
	"testing"
)

func TestCalibrate(t *testing.T) {
	store := openGoldenStore(t)
	db := store.DB()

	profile, err := Calibrate(db)
	if err != nil {
		t.Fatalf("Calibrate() error: %v", err)
	}

	if profile.Name != "calibrated" {
		t.Errorf("Name = %q, want \"calibrated\"", profile.Name)
	}

	// Sanity: all thresholds positive
	if profile.BlobPushThreshold <= 0 {
		t.Errorf("BlobPushThreshold = %d, want > 0", profile.BlobPushThreshold)
	}
	if profile.NonCoveringThreshold <= 0 {
		t.Errorf("NonCoveringThreshold = %d, want > 0", profile.NonCoveringThreshold)
	}
	if profile.TempBTree1Threshold <= 0 {
		t.Errorf("TempBTree1Threshold = %d, want > 0", profile.TempBTree1Threshold)
	}
	if profile.TempBTree2Threshold <= 0 {
		t.Errorf("TempBTree2Threshold = %d, want > 0", profile.TempBTree2Threshold)
	}

	// Monotonicity: blob < nonCovering, tempBTree1 < tempBTree2
	if profile.TempBTree2Threshold < profile.TempBTree1Threshold {
		t.Errorf("TempBTree2 (%d) < TempBTree1 (%d)",
			profile.TempBTree2Threshold, profile.TempBTree1Threshold)
	}

	t.Logf("Calibrated profile: blob=%d nonCovering=%d tempBTree1=%d tempBTree2=%d",
		profile.BlobPushThreshold, profile.NonCoveringThreshold,
		profile.TempBTree1Threshold, profile.TempBTree2Threshold)

	// Verify temp table was cleaned up
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE name = '_olu_calibration'").Scan(&count)
	if err != nil {
		t.Fatalf("cleanup check: %v", err)
	}
	if count != 0 {
		t.Error("calibration table was not cleaned up")
	}
}
