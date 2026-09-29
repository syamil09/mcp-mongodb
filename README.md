# mcp-mongodb

A standalone Go MCP (Model Context Protocol) server that gives Claude Code **read-only** access to MongoDB. It mirrors [mcp-firestore-server](../mcp-firestore-server): same tool style, same smart overflow handling, MongoDB backend.

## Features

- 9 MCP tools: get document, get collection, query, count, aggregate, list collections, list databases, export to JSON, compare
- Strictly read-only: no code path writes data; aggregation pipelines containing `$out` / `$merge` (at any depth) are rejected before any network call
- Native MongoDB queries: `filter`, `projection`, `sort` and `pipeline` are MongoDB Extended JSON (`{"$oid": ...}`, `{"$date": ...}` work)
- Named multi-connection config (DEV, PROD, ...) with lazy connect: an unreachable connection never blocks the others
- Smart overflow handling: large responses auto-save to file and return a compact preview
- Credentials never appear in tool output or error messages

## Setup

### 1. Build

```bash
cd mcp-mongodb
go build -o mcp-mongodb.exe .
```

### 2. Configure

Copy `.mcp-mongodb-config.json.example` to `.mcp-mongodb-config.json` next to the binary and fill in your values. The real config file is gitignored.

```json
{
  "connections": {
    "DEV": {
      "host": "127.0.0.1",
      "port": 27017,
      "username": "readonly_user",
      "password": "CHANGE_ME",
      "database": "my_database",
      "auth_source": "admin",
      "direct_connection": true,
      "export_path": "exports/dev"
    }
  }
}
```

| Field | Required | Default | Description |
|-------|----------|---------|-------------|
| `host` | Yes | - | Single host, or comma-separated `h1:27017,h2:27017` (then `port` is ignored) |
| `port` | No | `27017` | Used only when `host` has no comma and no `:` |
| `username` | No | - | If empty, no credential is sent |
| `password` | No | - | Passed to the driver directly (never put in a URI, never echoed) |
| `database` | Yes | - | Default database for all tools; overridable per call |
| `auth_source` | No | value of `database` | Authentication database |
| `direct_connection` | No | `false` | Connect to the single host directly (cannot be combined with multiple hosts) |
| `replica_set` | No | - | Replica set name |
| `tls` | No | `false` | Enable TLS with the system CA pool |
| `export_path` | No | `.` | Directory for exports, auto-saved overflow files and compare results |
| `max_documents` | No | `500` | Cap for get_collection / query / aggregate / export |
| `max_response_chars` | No | `50000` | Char threshold that triggers auto-save + preview |
| `doc_preview_chars` | No | `2000` | Per-doc char threshold for field-names-only preview |
| `timeout_seconds` | No | `30` | Per-tool-call timeout; also the connect and server-selection timeout |

**Paths can be relative.** `export_path` resolves against the config file's directory, not the current working directory.

**Connection URIs are not supported.** Configure fields individually; a `uri` key is ignored.

**Backward compatible.** A single-connection config (no `connections` key) is accepted and named `default`.

Config file search order:
1. `MCP_MONGODB_CONFIG` environment variable (full path)
2. Same directory as the executable
3. Current working directory

The default connection is `default` if present, otherwise the alphabetically first name.

**Use a read-only MongoDB user.** The server never writes, but as defence in depth connect with a user that has only the `read` role:

```js
db.getSiblingDB("admin").createUser({
  user: "readonly_user",
  pwd: "CHANGE_ME",
  roles: [{ role: "read", db: "my_database" }]
})
```

### 3. Register in Claude Code

Add to your project's `.mcp.json`:

```json
{
  "mcpServers": {
    "mongodb": {
      "command": "./mcp-mongodb.exe"
    }
  }
}
```

Copy the binary (and its config) to the same directory as `.mcp.json`, then restart Claude Code.

## Tools

Every tool accepts optional `connection` (defaults to `default` or the first connection) and `database` (defaults to the connection's `database`). JSON arguments are MongoDB Extended JSON, passed as a string. Objects/arrays are also accepted, but pass `sort` as a string to keep multi-key order.

### `mongo_get_document`

Fetch one document by `_id`.

```
collection: "orders"
id: "650f1c2e9b1d4a0012345678"
id_type: "auto"   (optional: auto | objectid | string | int; auto = 24-hex → ObjectID, else string)
```

### `mongo_get_collection`

Fetch documents in a collection, up to `max_documents`.

```
collection: "orders"
```

### `mongo_query`

```
collection: "orders"
filter: {"status": "PAID", "createdAt": {"$gte": {"$date": "2026-01-01T00:00:00Z"}}}
projection: {"status": 1, "total": 1}
sort: {"createdAt": -1}
limit: 50        (default and cap: max_documents)
skip: 0
```

### `mongo_count`

```
collection: "orders"
filter: {"status": "PAID"}
estimated: false   (true = fast metadata count; the filter is ignored and the response says so)
```

Returns `{collection, database, count, estimated, filter}`.

### `mongo_aggregate`

```
collection: "orders"
pipeline: [{"$match": {"status": "PAID"}}, {"$group": {"_id": "$customerId", "total": {"$sum": "$total"}}}]
```

Rejected: any `$out` or `$merge`, including inside `$facet`, `$lookup.pipeline` and `$unionWith.pipeline`. Reading stops after `max_documents` results (`truncated: true` in the response).

### `mongo_list_collections`

```
name_filter: "^order"   (optional regex)
```

Returns `[{name, type}]` sorted by name (`type` = `collection`, `view` or `timeseries`).

### `mongo_list_databases`

Lists databases the user is authorized to see. If the user lacks the privilege, you get a readable tool error.

### `mongo_export_json`

Single mode (one file):

```
collection: "orders"
id: "650f1c2e9b1d4a0012345678"     (optional: export one document)
filter: {"status": "PAID"}         (optional: export matching documents; not together with id)
output_path: "orders-paid.json"    (optional; relative paths resolve against export_path)
```

Default file name: `{export_path}/{database}_{collection}[_{id}]_{timestamp}.json`.

Batch mode (one file per document; errors are reported per item and the batch continues):

```
documents: [{"collection": "orders", "id": "650f..."}, {"collection": "customers", "id": "C-001"}]
```

### `mongo_compare`

Compare two sources field by field: `mongo` (`collection` or `collection/id`, split on the first `/`), `file` (a JSON file, including this server's export files), or `json` (raw JSON).

```
connection_a: "DEV"
connection_b: "PROD"
source_a: "orders/650f1c2e9b1d4a0012345678"
source_a_type: "mongo"
source_b: "orders/650f1c2e9b1d4a0012345678"
source_b_type: "mongo"
mode: "document"   (or "collection": documents matched by _id)
```

Differences are reported as `value_mismatch`, `missing_in_a` or `missing_in_b` with dot-notation paths (`address.city`, `items[2]`). The full result is saved under `{export_path}/compare/`.

## BSON → JSON

| BSON | JSON |
|------|------|
| ObjectID | hex string |
| Date | RFC3339 UTC string |
| Decimal128 | string |
| Binary | base64 string |
| Timestamp | `{"t": ..., "i": ...}` |
| Regex | `"/pattern/options"` |
| NaN / ±Infinity | `"NaN"`, `"+Inf"`, `"-Inf"` |
| embedded document | object (keys sorted) |

## Smart Overflow Handling

When a response exceeds `max_response_chars`:

1. Full data is saved to `{export_path}/temp/_auto_{database}_{collection}_{timestamp}.json`
2. The response returns a compact preview:
   - Documents under `doc_preview_chars` are shown in full
   - Larger documents show only `_id` and a sorted list of field names
   - If the preview is still too large, it is halved until it fits

```json
{
  "collection": "orders",
  "database": "shop",
  "count": 312,
  "preview_count": 28,
  "total_chars": 878857,
  "full_data_path": "C:\\exports\\dev\\temp\\_auto_shop_orders_20260929_101500.json",
  "note": "Response too large. Full data saved to file. Large documents show field names only.",
  "documents": [
    { "_id": "650f...", "_preview": "fields_only", "_fields": ["customer", "items", "status"] },
    { "_id": "650e...", "status": "PAID", "total": 125000 }
  ]
}
```

## Testing

```bash
go test ./...
go vet ./...
```

Integration tests run against a real MongoDB and are skipped unless `MONGODB_TEST_HOST` is set. They seed and then drop a temporary `_mcp_test_<timestamp>` collection, so the test user needs `readWrite` on the test database:

```bash
MONGODB_TEST_HOST=127.0.0.1 MONGODB_TEST_PORT=27017 MONGODB_TEST_USER=... MONGODB_TEST_PASSWORD=... \
MONGODB_TEST_DB=... MONGODB_TEST_AUTH_SOURCE=... go test -tags integration ./...
```

## Project Structure

```
mcp-mongodb/
  main.go        -- Entry point: load config, register tools, serve stdio
  config.go      -- Config loader, ConnectionConfig, defaults, path resolution
  connection.go  -- ConnectionManager: client options from fields, lazy ping, Get
  convert.go     -- BSON <-> JSON: sanitizeValue, Extended JSON parsing, parseID
  overflow.go    -- Response helpers: auto-save + smart preview
  mongo.go       -- Read tool handlers
  export.go      -- mongo_export_json
  compare.go     -- mongo_compare + diff engine
  pipeline.go    -- Aggregation write-stage detector
  tools.go       -- MCP tool definitions
```

## Architecture Decisions

- **Read-only by construction**: handlers only call `Find`, `FindOne`, `CountDocuments`, `EstimatedDocumentCount`, `Aggregate`, `ListCollectionSpecifications`, `ListDatabases` and `Ping`. Pipelines are scanned recursively for `$out` / `$merge`. The read preference is `secondaryPreferred`.
- **Validate before connecting**: every argument is parsed and checked before the connection is touched, so bad input fails fast and never reaches the server.
- **Lazy connections**: the v2 driver does not dial on connect. Each connection is pinged on first use; a failed ping is returned as a tool error and retried on the next call.
- **Field-by-field credentials**: no URI parsing or URL-encoding of passwords; the credential goes straight to the driver.
- **Char-based overflow detection**: checks actual serialized JSON size, not document count. Progressive truncation halves the preview until it fits.
- **Standalone project**: helpers are ported from mcp-firestore-server rather than shared, so the two servers evolve independently.
