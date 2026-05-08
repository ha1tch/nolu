#!/usr/bin/env python3
"""Update README.md and MANUAL.md version/test-count badges. Called by release.sh."""
import re, sys

ver, count = sys.argv[1], sys.argv[2]

# README: replace only the anchor block (idempotent)
path = 'README.md'
with open(path) as f: content = f.read()
badge = f'> **v{ver}** \u2014 {count} tests passing. See [MANUAL.md](MANUAL.md) for complete documentation.'
new_block = f'<!-- RELEASE_BADGE -->\n{badge}\n<!-- /RELEASE_BADGE -->'
anchor_pat = re.compile(r'<!-- RELEASE_BADGE -->.*?<!-- /RELEASE_BADGE -->', re.DOTALL)
if anchor_pat.search(content):
    updated = anchor_pat.sub(new_block, content)
else:
    # No anchor yet -- replace bare badge line and wrap it
    updated = re.sub(r'> \*\*v[0-9][^\n]+tests passing\.[^\n]*', new_block, content)
with open(path, 'w') as f: f.write(updated)

# MANUAL header
path = 'MANUAL.md'
with open(path) as f: content = f.read()
content = re.sub(r'Complete reference documentation for Olu v[^(]+\([^)]+\)\.',
                 f'Complete reference documentation for Olu v{ver} ({count} tests).', content)
content = re.sub(r'The current version is `[^`]+`\.',
                 f'The current version is `{ver}`.', content)
with open(path, 'w') as f: f.write(content)

print(f'README.md and MANUAL.md updated to v{ver}, {count} tests')
