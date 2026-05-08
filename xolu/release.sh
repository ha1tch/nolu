#!/usr/bin/env bash
# release.sh - olu release hygiene automation
#
# Single-pass release preparation:
#   1. Validates version string and CHANGELOG entry
#   2. Syncs VERSION + version.go
#   3. Builds the project
#   4. Runs tests ONCE (json + coverprofile)
#   5. Generates TESTING.md from test output
#   6. Updates README.md and MANUAL.md version/test-count badges
#   7. Verifies all version strings are consistent
#   8. Cuts a checkpoint zip
#
# Usage:
#   ./release.sh <version>            e.g. ./release.sh 0.9.4
#   ./release.sh <version> --short    skip stress tests (faster)
#   ./release.sh <version> --no-zip   dry run, no checkpoint
#
# Copyright (c) 2026 haitch
# Licensed under the Apache License, Version 2.0

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# Defaults
SHORT_FLAG=""
CUT_ZIP=true
VERSION=""

for arg in "$@"; do
    case "$arg" in
        --short)   SHORT_FLAG="-short" ;;
        --no-zip)  CUT_ZIP=false ;;
        --help|-h) sed -n '3,15p' "$0" | sed 's/^# \?//'; exit 0 ;;
        --*)       echo "Unknown option: $arg" >&2; exit 1 ;;
        *)
            if [ -z "$VERSION" ]; then VERSION="$arg"
            else echo "Unexpected argument: $arg" >&2; exit 1; fi ;;
    esac
done

[ -z "$VERSION" ] && { echo "Usage: $0 <version> [--short] [--no-zip]" >&2; exit 1; }

step() { echo ""; echo "-- $1"; }
ok()   { echo "   OK $1"; }
warn() { echo "   !! $1"; }
fail() { echo "   FAIL $1" >&2; exit 1; }

# 1. Validate version format
step "Version: $VERSION"
echo "$VERSION" | grep -qE '^[0-9]+\.[0-9]+\.[0-9]+(-[a-zA-Z0-9.]+)?$' \
    || fail "Invalid version format. Expected X.Y.Z or X.Y.Z-suffix"
ok "Format valid"

# 1b. Dirty tree warning
if git rev-parse --git-dir > /dev/null 2>&1; then
    if ! git diff --quiet 2>/dev/null || ! git diff --cached --quiet 2>/dev/null; then
        warn "Working tree has uncommitted changes -- checkpoint will not correspond to a clean commit"
    else
        GIT_SHA=$(git rev-parse --short HEAD 2>/dev/null || echo "unknown")
        ok "Git tree clean at $GIT_SHA"
    fi
else
    warn "Not a git repository -- skipping tree check"
fi

# 2. Check CHANGELOG entry
step "Checking CHANGELOG"
if grep -q "^## \[${VERSION}\]" CHANGELOG.md; then
    ok "Entry exists: [${VERSION}]"
else
    warn "No CHANGELOG entry for [${VERSION}] -- continuing anyway"
fi

# 3. Sync version files
step "Syncing version files"
./syncver.sh set "$VERSION"
ok "VERSION and version.go = $VERSION"

# 4. Build
step "Building"
make build 2>&1 | tail -3
ok "Build clean"

# 5. Single test pass
step "Running tests (single pass)"
TEST_JSON="test-output.json"
COVER_OUT="cover.out"
COVER_SUMMARY="cover-summary.txt"
[ -n "$SHORT_FLAG" ] && warn "Short mode: stress tests skipped"

set +e
# Scope: ./pkg/... only -- cmd/ has no unit-testable entry points
go test -json $SHORT_FLAG -count=1 -coverprofile="$COVER_OUT" ./pkg/... \
    2>test-errors.txt | tee "$TEST_JSON" | python3 -c "
import sys, json
counts = {'pass': 0, 'fail': 0, 'skip': 0}
pkg_fail = []
for line in sys.stdin:
    try: ev = json.loads(line)
    except: continue
    a = ev.get('Action',''); t = ev.get('Test','')
    p = ev.get('Package','').replace('github.com/ha1tch/olu/','')
    if t and '/' not in t and a in counts: counts[a] += 1
    if a == 'fail' and not t: pkg_fail.append(p)
print(f'  top-level: pass={counts[\"pass\"]} skip={counts[\"skip\"]} fail={counts[\"fail\"]}')
[print(f'  FAIL {p}') for p in pkg_fail]
"
TEST_EXIT=${PIPESTATUS[0]}
set -e

[ -f "$COVER_OUT" ] && go tool cover -func="$COVER_OUT" > "$COVER_SUMMARY" 2>/dev/null || true

# Validate coverage output
if [ ! -s "$COVER_OUT" ]; then
    fail "cover.out is missing or empty -- coverage was not collected"
fi
if [ -f "$COVER_SUMMARY" ] && ! grep -q "[0-9]" "$COVER_SUMMARY" 2>/dev/null; then
    warn "cover-summary.txt looks empty -- check the test run"
fi

if [ $TEST_EXIT -ne 0 ]; then
    [ -s test-errors.txt ] && cat test-errors.txt >&2
    fail "Tests failed -- aborting"
fi
ok "All tests passed"

# 5a. Lint
step "Running linter"
if command -v golangci-lint >/dev/null 2>&1; then
    golangci-lint run --timeout=5m
    ok "Lint clean"
else
    warn "golangci-lint not found -- skipping lint (install from https://golangci-lint.run)"
fi

# 6. Generate TESTING.md
step "Generating TESTING.md"
python3 scripts/gen_testing_md.py \
    --json      "$TEST_JSON" \
    --cover     "$COVER_SUMMARY" \
    --version   "$VERSION" \
    --narrative docs/TESTING_STRATEGY.md \
    --output    TESTING.md
ok "TESTING.md updated"

# 7. Update README.md and MANUAL.md
step "Updating badges"

TOTAL_RUN=$(python3 -c "
import json
n=0
with open('$TEST_JSON') as f:
    for line in f:
        try: ev=json.loads(line)
        except: continue
        if ev.get('Action')=='run' and ev.get('Test'): n+=1
print(n)
" 2>/dev/null || echo "?")

python3 scripts/update_badges.py "$VERSION" "$TOTAL_RUN"

ok "README.md: v${VERSION}, ${TOTAL_RUN} tests"
ok "MANUAL.md: v${VERSION}, ${TOTAL_RUN} tests"

# 8. Consistency check
step "Consistency check"
./syncver.sh check
CHANGELOG_TOP=$(grep "^## \[" CHANGELOG.md | head -1 | sed 's/.*\[\(.*\)\].*/\1/')
if [ "$CHANGELOG_TOP" != "$VERSION" ]; then
    warn "CHANGELOG top entry [$CHANGELOG_TOP] does not match [$VERSION]"
fi
ok "All version strings consistent: $VERSION"

# 9. Cut zip
if $CUT_ZIP; then
    step "Cutting checkpoint"
    ZIPNAME="olu-v${VERSION}-checkpoint.zip"
    rm -f "$ZIPNAME"

    # Explicit source list — never zip '.' or a parent directory.
    # Each entry here must be a known project file or directory.
    # Binary exclusions (-x) are a second line of defence; the explicit
    # list is the primary guard against container/workspace contamination.
    ZIP_SOURCES=(
        README.md CHANGELOG.md MANUAL.md TESTING.md VERSION LICENSE
        Makefile Dockerfile docker-compose.yml .golangci.yml
        run_tests.sh syncver.sh release.sh
        go.mod go.sum
        cmd/ pkg/ docs/ scripts/ .github/
    )
    # TS_PROGRESS.md is optional — include only if present.
    [ -f TS_PROGRESS.md ] && ZIP_SOURCES+=(TS_PROGRESS.md)

    zip -X -r "$ZIPNAME" "${ZIP_SOURCES[@]}" \
        -x "*.db" -x "*.db-shm" -x "*.db-wal" -x "*.db-journal" -x "*.tmp" \
        -x "test-output.json" -x "test-errors.txt" -x "cover.out" -x "cover-summary.txt" \
        -x "*.so" -x "*.dylib" -x "*.dll" -x "*.exe" -x "*.a" -x "*.o" \
        > /dev/null 2>&1

    # Post-zip sanity checks.

    # 1. No database files.
    DB_COUNT=$(unzip -l "$ZIPNAME" | grep -cE '\.db$|\.db-shm$|\.db-wal$' || true)
    [ "$DB_COUNT" -gt 0 ] && fail "Checkpoint contains $DB_COUNT .db file(s)"

    # 2. No ELF/Mach-O/PE binaries (sniff magic bytes via xxd + unzip -p).
    #    We test only the first 4 bytes of each entry; this catches compiled
    #    Go binaries, shared objects, and Chrome-style container artefacts
    #    that have slipped into checkpoints before.
    BINARY_FILES=""
    while IFS= read -r entry; do
        magic=$(unzip -p "$ZIPNAME" "$entry" 2>/dev/null | head -c 4 | xxd -p 2>/dev/null || true)
        case "$magic" in
            7f454c46)  BINARY_FILES="$BINARY_FILES\n  ELF:    $entry" ;;   # ELF
            cafebabe)  BINARY_FILES="$BINARY_FILES\n  Mach-O: $entry" ;;   # Mach-O fat
            feedface)  BINARY_FILES="$BINARY_FILES\n  Mach-O: $entry" ;;
            feedfacf)  BINARY_FILES="$BINARY_FILES\n  Mach-O: $entry" ;;
            4d5a*)     BINARY_FILES="$BINARY_FILES\n  PE/MZ:  $entry" ;;   # Windows PE
        esac
    done < <(unzip -l "$ZIPNAME" | awk 'NR>3 && /[^/]$/ {print $NF}' | head -200)

    if [ -n "$BINARY_FILES" ]; then
        echo ""
        printf "   Detected binary files in checkpoint:%b\n" "$BINARY_FILES" >&2
        fail "Checkpoint contains binary files -- aborting"
    fi

    # 3. Size guard: warn if zip exceeds 5 MB (source-only checkpoints are
    #    typically well under 1 MB; anything larger suggests contamination).
    ZIP_BYTES=$(wc -c < "$ZIPNAME")
    if [ "$ZIP_BYTES" -gt 3145728 ]; then
        ZIP_MB=$(awk "BEGIN {printf \"%.1f\", $ZIP_BYTES/1048576}")
        warn "Checkpoint is ${ZIP_MB} MB — exceeds 3 MB ceiling for a source-only archive; review contents"
    fi

    ZIP_SIZE=$(du -sh "$ZIPNAME" | cut -f1)
    ok "Created: ${ZIPNAME} (${ZIP_SIZE})"
else
    warn "Skipping zip (--no-zip)"
fi

echo ""
echo "======================================"
echo "  Release v${VERSION} prepared"
echo "  Tests run: ${TOTAL_RUN}"
$CUT_ZIP && echo "  Zip: olu-v${VERSION}-checkpoint.zip"
echo "======================================"
echo ""
