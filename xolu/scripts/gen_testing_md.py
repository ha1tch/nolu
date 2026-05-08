#!/usr/bin/env python3
"""
scripts/gen_testing_md.py

Generate TESTING.md from a single `go test -json` run.

Reads:
  - go test -json output file (test events, one JSON object per line)
  - coverage profile (go tool cover -func output, via --cover-summary)
  - VERSION file (or --version flag)
  - docs/TESTING_STRATEGY.md (static narrative appended after stats)

Writes:
  - TESTING.md (generated header + narrative)

Usage:
    python3 scripts/gen_testing_md.py \\
        --json    test-output.json \\
        --cover   cover-summary.txt \\
        --version 0.9.4 \\
        --narrative docs/TESTING_STRATEGY.md \\
        --output  TESTING.md

Copyright (c) 2026 haitch
Licensed under the Apache License, Version 2.0
"""

import argparse
import json
import re
import subprocess
import sys
from collections import defaultdict
from datetime import date
from pathlib import Path


# ---------------------------------------------------------------------------
# Parsing
# ---------------------------------------------------------------------------

def parse_test_json(filepath):
    """
    Parse go test -json output.

    Returns:
        packages: dict[short_pkg] -> {
            'top_pass': int,   # top-level tests that passed
            'top_fail': int,   # top-level tests that failed
            'top_skip': int,   # top-level tests that skipped
            'total_run': int,  # all === RUN events (includes subtests)
            'coverage':  str,  # e.g. "92.5%" or "n/a"
            'elapsed':   str,  # e.g. "2.551s"
        }
        totals: dict with 'run', 'top_pass', 'top_fail', 'top_skip'
    """
    PREFIX = 'github.com/ha1tch/olu/'

    packages = defaultdict(lambda: {
        'top_pass': 0, 'top_fail': 0, 'top_skip': 0,
        'total_run': 0, 'coverage': 'n/a', 'elapsed': '',
    })
    totals = {'total_run': 0, 'top_pass': 0, 'top_fail': 0, 'top_skip': 0}

    with open(filepath) as f:
        for line in f:
            line = line.strip()
            if not line:
                continue
            try:
                ev = json.loads(line)
            except json.JSONDecodeError:
                continue

            action = ev.get('Action', '')
            pkg    = ev.get('Package', '')
            test   = ev.get('Test', '')

            short = pkg.replace(PREFIX, '') if pkg.startswith(PREFIX) else pkg

            # Extract per-package coverage and elapsed from summary output lines
            # e.g. "ok  \tgithub.com/.../pkg/cache\t2.5s\tcoverage: 92.5% of statements\n"
            if action == 'output' and not test:
                out = ev.get('Output', '')
                m = re.search(r'coverage:\s+([\d.]+%)', out)
                if m:
                    packages[short]['coverage'] = m.group(1)
                m2 = re.search(r'\t([\d.]+s)\b', out)
                if m2 and not packages[short]['elapsed']:
                    packages[short]['elapsed'] = m2.group(1)

            # Count === RUN events (all tests including subtests)
            if action == 'run' and test:
                packages[short]['total_run'] += 1
                totals['total_run'] += 1

            # Count top-level results (no slash in name)
            if test and '/' not in test:
                if action == 'pass':
                    packages[short]['top_pass'] += 1
                    totals['top_pass'] += 1
                elif action == 'fail':
                    packages[short]['top_fail'] += 1
                    totals['top_fail'] += 1
                elif action == 'skip':
                    packages[short]['top_skip'] += 1
                    totals['top_skip'] += 1

    return dict(packages), totals


def count_benchmarks(pkg_dir):
    """Count benchmark functions in pkg/ without running them."""
    count = 0
    for path in Path(pkg_dir).rglob('*_test.go'):
        with open(path, errors='replace') as f:
            for line in f:
                if line.startswith('func Benchmark'):
                    count += 1
    return count


def parse_aggregate_coverage(cover_summary_path):
    """
    Parse `go tool cover -func cover.out` output for the aggregate total line.
    Returns e.g. "79.4%" or "n/a".
    """
    if not cover_summary_path or not Path(cover_summary_path).exists():
        return 'n/a'
    with open(cover_summary_path) as f:
        for line in f:
            if line.startswith('total:'):
                parts = line.split()
                if parts:
                    return parts[-1]
    return 'n/a'


# ---------------------------------------------------------------------------
# Rendering
# ---------------------------------------------------------------------------

PACKAGE_ORDER = [
    'pkg/errors',
    'pkg/version',
    'pkg/models',
    'pkg/config',
    'pkg/tenant',
    'pkg/validation',
    'pkg/cache',
    'pkg/middleware',
    'pkg/jsonic',
    'pkg/graph',
    'pkg/sulpher',
    'pkg/oql',
    'pkg/storage',
    'pkg/timeseries',
    'pkg/server',
    'pkg/tdigest',
    'cmd/olu-migrate',
]


def render_header(version, run_date, totals, packages, bench_count, agg_coverage):
    today = run_date or date.today().isoformat()

    lines = [
        '# olu Test Suite\n',
        '\n',
        f'> Auto-generated by `release.sh` on {today} for v{version}.\n',
        '> Do not edit directly — update `docs/TESTING_STRATEGY.md` for narrative changes.\n',
        '> Test scope: `./pkg/...` — `cmd/` is excluded (no unit-testable entry points).\n',
        '\n',
        '## Test Statistics\n',
        '\n',
        f'Version **{version}**, generated {today}.\n',
        '\n',
        '| Metric | Count |\n',
        '|--------|-------|\n',
        f'| Tests (including subtests) | {totals["total_run"]} |\n',
        f'| Top-level tests | {totals["top_pass"] + totals["top_skip"] + totals["top_fail"]} |\n',
        f'| Passed | {totals["top_pass"]} |\n',
        f'| Skipped | {totals["top_skip"]} |\n',
        f'| Failed | {totals["top_fail"]} |\n',
        f'| Packages | {len(packages)} |\n',
        f'| Benchmark functions | {bench_count} |\n',
        f'| Aggregate coverage | {agg_coverage} |\n',
        '\n',
        '## Package Breakdown\n',
        '\n',
        '| Package | Tests (incl. subtests) | Skipped | Coverage |\n',
        '|---------|----------------------:|--------:|---------:|\n',
    ]

    # Emit in preferred order, then any remainder alphabetically
    seen = set()
    ordered = [p for p in PACKAGE_ORDER if p in packages]
    seen.update(ordered)
    remainder = sorted(k for k in packages if k not in seen)

    for pkg in ordered + remainder:
        d = packages[pkg]
        skipped = d['top_skip']
        cov     = d['coverage']
        lines.append(f'| `{pkg}` | {d["total_run"]} | {skipped} | {cov} |\n')

    lines.append('\n')
    lines.append('---\n')
    lines.append('\n')
    return lines


# ---------------------------------------------------------------------------
# Main
# ---------------------------------------------------------------------------

def main():
    ap = argparse.ArgumentParser(description=__doc__)
    ap.add_argument('--json',      required=True,  help='go test -json output file')
    ap.add_argument('--cover',     default=None,   help='go tool cover -func output file')
    ap.add_argument('--version',   default=None,   help='Version string (overrides VERSION file)')
    ap.add_argument('--date',      default=None,   help='Date string YYYY-MM-DD (default: today)')
    ap.add_argument('--narrative', required=True,  help='Static narrative file (docs/TESTING_STRATEGY.md)')
    ap.add_argument('--output',    required=True,  help='Output path for TESTING.md')
    ap.add_argument('--pkg-dir',   default='pkg',  help='Directory to scan for benchmarks (default: pkg)')
    args = ap.parse_args()

    # Version
    version = args.version
    if not version:
        vfile = Path('VERSION')
        if vfile.exists():
            version = vfile.read_text().strip()
        else:
            print('ERROR: --version not given and VERSION file not found', file=sys.stderr)
            sys.exit(1)

    # Parse test output
    packages, totals = parse_test_json(args.json)

    if not packages:
        print('WARNING: no test data found in JSON file', file=sys.stderr)

    # Benchmarks (from source, no run needed)
    bench_count = count_benchmarks(args.pkg_dir)

    # Aggregate coverage
    agg_coverage = parse_aggregate_coverage(args.cover)

    # Render generated header
    header_lines = render_header(version, args.date, totals, packages, bench_count, agg_coverage)

    # Read narrative
    narrative_path = Path(args.narrative)
    if not narrative_path.exists():
        print(f'ERROR: narrative file not found: {args.narrative}', file=sys.stderr)
        sys.exit(1)
    narrative = narrative_path.read_text()

    # Strip the "# olu Test Suite — Narrative" title from narrative
    # (our generated header already has the real title)
    narrative_lines = narrative.splitlines(keepends=True)
    start = 0
    for i, line in enumerate(narrative_lines):
        # Skip header lines (title + preamble block) until a real section
        if line.startswith('## '):
            start = i
            break
    narrative_body = ''.join(narrative_lines[start:])

    # Write output
    output = Path(args.output)
    output.write_text(''.join(header_lines) + narrative_body)

    # Summary to stdout
    print(f'TESTING.md generated: v{version}, '
          f'{totals["total_run"]} tests run, '
          f'{totals["top_pass"]} top-level passed, '
          f'{totals["top_skip"]} skipped, '
          f'{totals["top_fail"]} failed, '
          f'{bench_count} benchmarks, '
          f'coverage {agg_coverage}')


if __name__ == '__main__':
    main()
