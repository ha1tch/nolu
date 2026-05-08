#!/bin/bash

# run_tests.sh - olu base test suite with coverage reporting
#
# Coverage reporting is always on. Every run produces coverage.out
# and a per-package summary.
#
# Usage:
#   ./run_tests.sh                  Standard run (short mode)
#   ./run_tests.sh --redis          Include Redis backend tests
#   ./run_tests.sh --full           Include stress tests (no -short)
#   ./run_tests.sh --race           Enable race detector
#   ./run_tests.sh --threshold 75   Fail if aggregate coverage below 75%
#   ./run_tests.sh --html           Generate coverage.html report
#
# Copyright (c) 2026 haitch
# Licensed under Apache 2.0

set -uo pipefail

# Defaults
SHORT="-short"
TAGS=""
RACE=""
THRESHOLD=""
HTML=false

while [[ $# -gt 0 ]]; do
    case "$1" in
        --redis)     TAGS="-tags redis"; shift ;;
        --full)      SHORT=""; shift ;;
        --race)      RACE="-race"; shift ;;
        --html)      HTML=true; shift ;;
        --threshold) THRESHOLD="$2"; shift 2 ;;
        --help|-h)
            sed -n '3,15p' "$0" | sed 's/^# \?//'
            exit 0 ;;
        *)
            echo "Unknown option: $1" >&2; exit 1 ;;
    esac
done

COVERFILE="coverage.out"

echo "olu test suite"
echo "=============="
echo ""

# --- Run ---

# shellcheck disable=SC2086
OUTPUT=$(go test $SHORT $TAGS $RACE -count=1 -coverprofile="$COVERFILE" ./... 2>&1)
EXIT=$?

# --- Failures first ---

FAIL_COUNT=$(echo "$OUTPUT" | grep -c '^FAIL' || true)
if [ "$FAIL_COUNT" -gt 0 ]; then
    echo "FAILURES:"
    echo "$OUTPUT" | grep '^FAIL' | sed 's/^/  /'
    echo ""
fi

# --- Per-package coverage table ---

echo "Coverage by package:"
echo ""
printf "  %-42s %10s %8s\n" "Package" "Time" "Cover"
printf "  %-42s %10s %8s\n" "---" "----" "-----"

echo "$OUTPUT" | grep '^ok' | while IFS= read -r line; do
    pkg=$(echo "$line" | awk '{print $2}')
    short_pkg=${pkg#github.com/ha1tch/olu/}
    timing=$(echo "$line" | awk '{print $3}')
    cov=$(echo "$line" | grep -oE '[0-9]+\.[0-9]+%' || echo "n/a")
    printf "  %-42s %10s %8s\n" "$short_pkg" "$timing" "$cov"
done

echo ""

# --- Aggregate ---

AGGREGATE="n/a"
if [ -f "$COVERFILE" ]; then
    AGGREGATE=$(go tool cover -func="$COVERFILE" 2>/dev/null | tail -1 | awk '{print $NF}')
fi

PASS_COUNT=$(echo "$OUTPUT" | grep -c '^ok' || true)
SKIP_LINE=$(echo "$OUTPUT" | grep -oE '[0-9]+ skip' | head -1 || true)

echo "Aggregate coverage: $AGGREGATE"
echo "Packages:           $PASS_COUNT pass, $FAIL_COUNT fail"
echo ""

# --- HTML report ---

if $HTML && [ -f "$COVERFILE" ]; then
    go tool cover -html="$COVERFILE" -o coverage.html 2>/dev/null
    echo "Reports: coverage.out, coverage.html"
    echo ""
fi

# --- Threshold gate ---

if [ -n "$THRESHOLD" ] && [ -f "$COVERFILE" ]; then
    ACTUAL=$(echo "$AGGREGATE" | tr -d '%')
    PASS_GATE=$(echo "$ACTUAL >= $THRESHOLD" | bc -l 2>/dev/null || echo "0")
    if [ "$PASS_GATE" -eq 1 ]; then
        echo "Threshold: ${ACTUAL}% >= ${THRESHOLD}% (ok)"
    else
        echo "Threshold: ${ACTUAL}% < ${THRESHOLD}% (FAIL)"
        exit 1
    fi
fi

exit $EXIT
