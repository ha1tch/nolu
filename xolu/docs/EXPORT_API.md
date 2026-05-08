# Export API

The export endpoint provides a complete data snapshot for offline analysis, backup, or migration.

## Endpoint

```
GET /api/v1/export
```

## Response

Returns a zip archive containing:

```
olu-export-2025-01-06T150405Z.zip
├── manifest.json      # Export metadata
├── entities.db        # SQLite database (if using sqlite storage)
├── data/              # JSON files (if using jsonfile storage)
├── graph.data         # Binary graph data (if graph enabled)
├── graph.index        # Binary graph index (if graph enabled)
└── graph.json         # Human-readable graph export
```

### Headers

```
Content-Type: application/zip
Content-Disposition: attachment; filename="olu-export-2025-01-06T150405Z.zip"
```

## Manifest Format

```json
{
  "version": "0.8.0",
  "exported_at": "2025-01-06T15:04:05Z",
  "storage_type": "sqlite",
  "graph_enabled": true,
  "entities_file": "entities.db",
  "graph_files": ["graph.data", "graph.index"],
  "graph_json": "graph.json"
}
```

## Usage Examples

### Download with curl

```bash
curl -o backup.zip http://localhost:9090/api/v1/export
```

### Scheduled backup

```bash
#!/bin/bash
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
curl -o "olu-backup-${TIMESTAMP}.zip" http://localhost:9090/api/v1/export
```

### Analyse with DuckDB

```bash
unzip backup.zip
duckdb -c "ATTACH 'entities.db' AS olu; SELECT * FROM olu.items LIMIT 10;"
```

### Analyse with Python

```python
import sqlite3
import json
from zipfile import ZipFile

with ZipFile('backup.zip') as z:
    # Read manifest
    manifest = json.loads(z.read('manifest.json'))
    print(f"Exported: {manifest['exported_at']}")
    
    # Extract and query SQLite
    z.extract('entities.db')
    conn = sqlite3.connect('entities.db')
    cursor = conn.execute("SELECT name FROM sqlite_master WHERE type='table'")
    print("Tables:", [row[0] for row in cursor])
```

## Notes

- Export streams directly to response; no temporary files on server
- Safe to call during normal operation (uses SQLite WAL mode)
- Graph JSON provides human-readable format for analysis tools
- Large databases may take time to download; use appropriate timeouts
