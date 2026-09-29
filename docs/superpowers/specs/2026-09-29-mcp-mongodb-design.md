# mcp-mongodb — Design Spec

**Date:** 2026-09-29
**Status:** Approved in brainstorming, pending written-spec review
**Reference project:** `../mcp-firestore-server` (same structure, same behaviour, MongoDB backend)

## 1. Goal

A standalone Go MCP server (stdio) that gives Claude Code **read-only** access to MongoDB, mirroring the tools and behaviour of `mcp-firestore-server`, plus a document-count tool and an aggregation tool.

Success criteria:

- Claude can fetch, query, count, aggregate, list, export, and compare MongoDB data across named connections (DEV, PROD, ...).
- No code path can modify data.
- Large responses never flood the context: they are auto-saved to a file and a smart preview is returned (same algorithm as mcp-firestore).
- Credentials never appear in tool output or error messages.

Non-goals: write operations, change streams, index management, a shared library with mcp-firestore.

## 2. Decisions (from brainstorming)

| Topic | Decision |
|---|---|
| Access | Strictly read-only |
| Query style | Native MongoDB filter as Extended JSON (`filter`, `projection`, `sort`) |
| Aggregation | `mongo_aggregate` tool; pipelines containing `$out` / `$merge` (at any depth) are rejected |
| Credentials | Stored in plain config file (gitignored); **field-by-field**, no URI support |
| Code layout | Standalone project mirroring mcp-firestore's file split; helpers copied and adapted (no shared module) |
| Connections | Named multi-connection; lazy ping so an unreachable connection does not block others |

## 3. Tech stack

- Go 1.25
- `github.com/mark3labs/mcp-go` v0.45.0 (same as mcp-firestore)
- `go.mongodb.org/mongo-driver/v2` (official driver, v2 API)
- Module name: `mcp-mongodb`; binary: `mcp-mongodb.exe`

## 4. Project structure

```
mcp-mongodb/
  main.go          -- Entry point: load config, register tools, serve stdio
  config.go        -- Config loader, ConnectionConfig, defaults, path resolution
  connection.go    -- ConnectionManager: build client options from fields, lazy ping, Get
  convert.go       -- BSON <-> JSON: sanitizeValue, parseExtJSON, parseID
  overflow.go      -- saveFullData, buildSmartPreview, buildOverflowResponse (ported from firestore.go)
  mongo.go         -- Tool handlers: get_document, get_collection, query, count, aggregate, list_*
  export.go        -- mongo_export_json handler
  compare.go       -- mongo_compare handler + diff engine (ported from mcp-firestore compare.go)
  pipeline.go      -- Aggregation pipeline write-stage detector
  tools.go         -- MCP tool registrations
  *_test.go        -- Unit tests; integration_test.go behind `integration` build tag
  .mcp-mongodb-config.json.example
  .gitignore       -- ignores .mcp-mongodb-config.json, *.exe, exports/
  README.md
```

## 5. Configuration

File name: `.mcp-mongodb-config.json`

Search order:
1. `MCP_MONGODB_CONFIG` environment variable (full path)
2. Same directory as the executable
3. Current working directory

```json
{
  "connections": {
    "DEV": {
      "host": "127.0.0.1",
      "port": 27017,
      "username": "sam_inbound",
      "password": "SuperSecretUserPassword123!",
      "database": "sam_inbound_trial",
      "auth_source": "sam_inbound",
      "direct_connection": true,
      "replica_set": "",
      "tls": false,
      "export_path": "exports/dev",
      "max_documents": 500,
      "max_response_chars": 50000,
      "doc_preview_chars": 2000,
      "timeout_seconds": 30
    }
  }
}
```

| Field | Required | Default | Notes |
|---|---|---|---|
| `host` | Yes | — | Single host, or comma-separated `h1:27017,h2:27017` (then `port` is ignored) |
| `port` | No | `27017` | Used only when `host` has no comma and no `:` |
| `username` | No | — | If empty, no auth credential is set |
| `password` | No | — | Passed via `SetAuth`, never URL-encoded, never echoed |
| `database` | Yes | — | Default database for all tools; overridable per call |
| `auth_source` | No | value of `database` | |
| `direct_connection` | No | `false` | |
| `replica_set` | No | — | |
| `tls` | No | `false` | Enables TLS with system CA pool |
| `export_path` | No | `.` | Relative paths resolve against the config file's directory |
| `max_documents` | No | `500` | Cap for get_collection / query / aggregate / export |
| `max_response_chars` | No | `50000` | Threshold that triggers auto-save + preview |
| `doc_preview_chars` | No | `2000` | Per-doc threshold for fields-only preview |
| `timeout_seconds` | No | `30` | Per-tool-call context timeout; also used as connect/server-selection timeout |

Backward-compatible single-connection format (no `connections` key) is accepted and named `default`.

Default connection: `default` if present, else the alphabetically first name.

Validation at startup (fail fast with clear message naming the connection): `host` missing, `database` missing, at least one connection present. A `uri` key, if present, is ignored (not supported).

## 6. Connection management

- `ConnectionManager.Init(configs)`: for each connection, build `options.Client()`:
  - `SetHosts(hosts)` from `host`/`port`
  - `SetAuth(options.Credential{Username, Password, AuthSource})` when `username` non-empty
  - `SetDirect(direct_connection)`, `SetReplicaSet(replica_set)` if non-empty, `SetTLSConfig(&tls.Config{})` if `tls`
  - `SetReadPreference(readpref.SecondaryPreferred())`
  - `SetServerSelectionTimeout` / `SetConnectTimeout` = `timeout_seconds`
  - `SetAppName("mcp-mongodb")`
  - `mongo.Connect(opts)` — v2 does not dial eagerly, so startup never blocks on network.
- `Get(name)` returns the connection; on first use per connection it runs `Ping` once (guarded by `sync.Once`-style state). A ping failure is returned as a tool error and retried on the next call (state reset on failure).
- Unknown connection name → error listing available names (same as mcp-firestore).
- Client-option building is a pure function (`buildClientOptions(cfg) (*options.ClientOptions, error)`) so it is unit-testable.

## 7. Tools

All tools accept optional `connection` (string) and `database` (string, overrides config `database`). All JSON-typed arguments (`filter`, `projection`, `sort`, `pipeline`) are accepted either as a JSON object/array or as a JSON string, and parsed as **MongoDB Extended JSON** (so `{"$oid": ...}`, `{"$date": ...}` work).

| Tool | Required args | Optional args | Behaviour |
|---|---|---|---|
| `mongo_get_document` | `collection`, `id` | `id_type` (`auto`\|`objectid`\|`string`\|`int`) | `auto`: 24-hex → ObjectID, else string. Not found → tool error "document not found". |
| `mongo_get_collection` | `collection` | — | `Find({})` limited to `max_documents`; smart overflow |
| `mongo_query` | `collection` | `filter`, `projection`, `sort`, `limit`, `skip` | `limit` default and cap = `max_documents`; smart overflow |
| `mongo_count` | `collection` | `filter`, `estimated` (bool) | `CountDocuments(filter)`; `estimated=true` uses `EstimatedDocumentCount` and ignores `filter` (response says so). Returns `{collection, database, count, estimated, filter}` |
| `mongo_aggregate` | `collection`, `pipeline` | — | Rejects write stages (§8); appends no stages but stops reading after `max_documents` results; smart overflow |
| `mongo_list_collections` | — | `name_filter` (regex string) | Returns sorted `[{name, type}]` (type = `collection`/`view`/`timeseries`) |
| `mongo_list_databases` | — | — | `ListDatabases` with `authorizedDatabases: true`; permission error returned as a readable tool error |
| `mongo_export_json` | one of: `collection`, `documents` | `id`, `filter`, `output_path` | See §7.1 |
| `mongo_compare` | `source_a`, `source_a_type`, `source_b`, `source_b_type` | `connection_a`, `connection_b`, `database_a`, `database_b`, `mode` (`document`\|`collection`) | See §7.2 |

Response shape for multi-doc tools (mirrors mcp-firestore):

```json
{ "collection": "orders", "database": "sam_inbound_trial", "count": 12, "documents": [ ... ] }
```

Every document keeps its `_id`, converted to JSON (ObjectID → hex string).

### 7.1 mongo_export_json

- **Single:** `collection` (+ optional `id` → one document, or `filter` → matching docs, or neither → whole collection up to `max_documents`). Written to `output_path` or `{export_path}/{database}_{collection}[_{id}]_{timestamp}.json`.
- **Batch:** `documents: [{"collection": "...", "id": "..."}]` → each to its own file in `export_path`; `output_path` ignored.
- Returns list of `{collection, id?, file, count}` plus any per-item errors (batch continues on error).

### 7.2 mongo_compare

- Ported diff engine from mcp-firestore `compare.go` (value_mismatch / missing_in_a / missing_in_b, dot-notation paths, summary counts, output saved to file when large).
- Source types: `mongo`, `file`, `json`.
- `mongo` source syntax: `collection` (collection mode, up to `max_documents`) or `collection/id` (document mode; id parsed with `auto` rules). Split on the **first** `/`.
- Collection mode matches documents by `_id` (stringified).
- Connections resolved lazily: a connection is only looked up when its side is type `mongo`.

## 8. Read-only guarantees

- Handlers only call `Find`, `FindOne`, `CountDocuments`, `EstimatedDocumentCount`, `Aggregate`, `ListCollectionSpecifications`, `ListDatabases`, `Ping`.
- `pipeline.go: findWriteStage(pipeline) (stage string, found bool)` walks the parsed pipeline recursively — including sub-pipelines inside `$facet`, `$lookup.pipeline`, `$unionWith.pipeline` — and rejects `$out` and `$merge`. The request is refused before any network call with error: `pipeline contains write stage "$merge" — this server is read-only`.
- Read preference `secondaryPreferred`.
- README recommends connecting with a MongoDB user that has only the `read` role as defence in depth.

## 9. BSON → JSON conversion (`convert.go`)

Documents are decoded into `bson.M` / `bson.D` and passed through `sanitizeValue`:

| BSON type | JSON output |
|---|---|
| `bson.ObjectID` | hex string |
| `bson.DateTime`, `time.Time` | RFC3339 UTC string |
| `bson.Decimal128` | string |
| `bson.Binary` | base64 string |
| `bson.Timestamp` | `{"t": ..., "i": ...}` |
| `bson.Regex` | `"/pattern/options"` |
| `bson.D` | object (converted to `map[string]interface{}`; key order not preserved — `encoding/json` sorts keys, same as mcp-firestore) |
| `bson.A` / slices | array (recursed) |
| `bson.M` / maps | object (recursed) |
| int32/int64/float64/string/bool/null | as-is |
| other | `fmt.Sprint(v)` |

Input parsing: `parseExtJSON(arg interface{}) (interface{}, error)` — if `arg` is a string, use it; otherwise `json.Marshal` it; then `bson.UnmarshalExtJSON(data, false, &out)`. Errors: `"<argname> is not valid Extended JSON: <err>"`.

`parseID(id string, idType string) (interface{}, error)` implements `auto` / `objectid` / `string` / `int`.

## 10. Smart overflow (ported unchanged)

Same algorithm as mcp-firestore:

1. Serialize full response. If `len <= max_response_chars` → return it.
2. Otherwise save full JSON to `{export_path}/temp/_auto_{label}_{timestamp}.json`.
3. Build preview: docs ≤ `doc_preview_chars` shown in full; larger docs → `{_id, _preview: "fields_only", _fields: [sorted names]}`.
4. Halve preview docs until the preview fits.
5. Response includes `count`, `preview_count`, `total_chars`, `full_data_path`, `note`.

Label = `{database}_{collection}` sanitized for filenames.

## 11. Error handling

- All handler errors returned via `mcp.NewToolResultError(...)`; the server process never exits after startup.
- Every tool call runs under `context.WithTimeout(ctx, timeout_seconds)`.
- `password` never included in any message; config errors reference the connection name, not the credential.
- Driver errors are passed through (they do not contain the password when using `SetAuth`).
- Missing required argument → `"<arg> is required"`.

## 12. Testing

**Unit tests (no DB):**
- `config_test.go`: multi + single format, defaults, `auth_source` default, relative path resolution, validation errors.
- `connection_test.go`: `buildClientOptions` — single host+port, comma hosts, auth set/unset, direct/replica/tls flags.
- `convert_test.go`: `sanitizeValue` for every row in §9; `parseExtJSON` (string and object input, `$oid`, `$date`, invalid JSON); `parseID` for all id types.
- `pipeline_test.go`: `$out`, `$merge`, nested in `$facet`, `$lookup.pipeline`, `$unionWith`; clean pipelines pass.
- `overflow_test.go`: small response passes through; large response saved + preview; fields-only stubs; progressive truncation.
- `compare_test.go`: diff engine cases ported from mcp-firestore behaviour; `collection/id` source parsing.

**Integration tests** (`//go:build integration`, env `MONGODB_TEST_HOST`, `MONGODB_TEST_PORT`, `MONGODB_TEST_USER`, `MONGODB_TEST_PASSWORD`, `MONGODB_TEST_DB`, `MONGODB_TEST_AUTH_SOURCE`):
- A test-only client seeds a temporary collection `_mcp_test_<timestamp>` and drops it afterwards (the only writes, in test code only).
- Exercises get_document (ObjectID + string ids), get_collection, query (filter/sort/projection/limit), count (filtered + estimated), aggregate (valid + rejected `$out`), list_collections, export (single + batch), compare (mongo vs json).
- Run: `go test -tags integration ./...`

**Manual smoke test:** build `mcp-mongodb.exe`, register in `.mcp.json`, call `mongo_count` against `sam_inbound_trial`.

## 13. Deliverables

- Source files per §4, all tests passing (`go test ./...`, `go vet ./...`).
- `.mcp-mongodb-config.json.example` (with placeholder password) and `.gitignore`.
- `README.md` matching mcp-firestore's README structure: features, setup (build, configure, register), tool reference with examples, smart overflow, project structure, architecture decisions.
