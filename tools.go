package main

import (
	mcp "github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

const (
	connectionDesc = "Connection name from config (e.g. 'DEV', 'PROD'). Defaults to 'default' or the first available connection."
	databaseDesc   = "Database name. Defaults to the connection's configured database."
	extJSONNote    = " MongoDB Extended JSON, passed as a JSON string (e.g. {\"_id\": {\"$oid\": \"...\"}}, {\"at\": {\"$gte\": {\"$date\": \"2024-01-01T00:00:00Z\"}}})."
	overflowNote   = " If the response exceeds max_response_chars, full data is auto-saved to export_path/temp/ and a smart preview is returned — large documents show field names only."
)

// toolDefinitions returns every tool this server exposes with its handler.
// JSON-typed arguments are declared as strings so key order (which matters
// for sort) survives; handlers also accept objects/arrays.
func toolDefinitions() []server.ServerTool {
	return []server.ServerTool{
		{
			Tool: mcp.NewTool("mongo_get_document",
				mcp.WithDescription("Fetch a single MongoDB document by _id."),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("collection", mcp.Required(), mcp.Description("Collection name")),
				mcp.WithString("id", mcp.Required(), mcp.Description("Document _id value")),
				mcp.WithString("id_type",
					mcp.Enum("auto", "objectid", "string", "int"),
					mcp.Description("How to interpret id. 'auto' (default): 24-hex → ObjectID, otherwise string."),
				),
			),
			Handler: handleGetDocument,
		},
		{
			Tool: mcp.NewTool("mongo_get_collection",
				mcp.WithDescription("Fetch documents in a collection (up to max_documents)."+overflowNote),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("collection", mcp.Required(), mcp.Description("Collection name")),
			),
			Handler: handleGetCollection,
		},
		{
			Tool: mcp.NewTool("mongo_query",
				mcp.WithDescription("Find documents with a native MongoDB filter, projection, sort, limit and skip."+overflowNote),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("collection", mcp.Required(), mcp.Description("Collection name")),
				mcp.WithString("filter", mcp.Description("Query filter."+extJSONNote)),
				mcp.WithString("projection", mcp.Description("Projection document, e.g. {\"name\": 1}."+extJSONNote)),
				mcp.WithString("sort", mcp.Description("Sort document, e.g. {\"createdAt\": -1}. Pass as a string to keep multi-key order."+extJSONNote)),
				mcp.WithNumber("limit", mcp.Description("Maximum documents to return (default and cap: max_documents)")),
				mcp.WithNumber("skip", mcp.Description("Number of documents to skip")),
			),
			Handler: handleQuery,
		},
		{
			Tool: mcp.NewTool("mongo_count",
				mcp.WithDescription("Count documents matching a filter. With estimated=true, uses fast collection metadata and ignores the filter."),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("collection", mcp.Required(), mcp.Description("Collection name")),
				mcp.WithString("filter", mcp.Description("Query filter."+extJSONNote)),
				mcp.WithBoolean("estimated", mcp.Description("Use estimatedDocumentCount (ignores filter). Default false.")),
			),
			Handler: handleCount,
		},
		{
			Tool: mcp.NewTool("mongo_aggregate",
				mcp.WithDescription("Run a read-only aggregation pipeline. Pipelines containing $out or $merge (at any depth) are rejected. Stops after max_documents results."+overflowNote),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("collection", mcp.Required(), mcp.Description("Collection name")),
				mcp.WithString("pipeline", mcp.Required(), mcp.Description("JSON array of stages, e.g. [{\"$match\": {...}}, {\"$group\": {...}}]."+extJSONNote)),
			),
			Handler: handleAggregate,
		},
		{
			Tool: mcp.NewTool("mongo_list_collections",
				mcp.WithDescription("List collections and views in a database, sorted by name."),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("name_filter", mcp.Description("Optional regex applied to collection names")),
			),
			Handler: handleListCollections,
		},
		{
			Tool: mcp.NewTool("mongo_list_databases",
				mcp.WithDescription("List databases the configured user is authorized to see."),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
			),
			Handler: handleListDatabases,
		},
		{
			Tool: mcp.NewTool("mongo_export_json",
				mcp.WithDescription("Export MongoDB data to JSON files under export_path. Single mode: 'collection' plus optional 'id' (one document) or 'filter' (matching documents); neither exports the collection up to max_documents. Batch mode: 'documents' array of {collection, id}, each to its own file."),
				mcp.WithString("connection", mcp.Description(connectionDesc)),
				mcp.WithString("database", mcp.Description(databaseDesc)),
				mcp.WithString("collection", mcp.Description("Collection to export (single mode)")),
				mcp.WithString("id", mcp.Description("Export only this _id (single mode; auto id rules)")),
				mcp.WithString("filter", mcp.Description("Export only matching documents (single mode)."+extJSONNote)),
				mcp.WithArray("documents", mcp.Description(`Batch mode: [{"collection": "orders", "id": "650f..."}]. Each document goes to its own file.`)),
				mcp.WithString("output_path", mcp.Description("Single mode only: output file. Relative paths resolve against export_path.")),
			),
			Handler: handleExportJSON,
		},
		{
			Tool: mcp.NewTool("mongo_compare",
				mcp.WithDescription("Compare two JSON data sources field by field. Each source can be MongoDB ('collection' or 'collection/id'), a local JSON file, or raw JSON. Collection mode matches documents by _id. Reports value mismatches and missing fields with dot-notation paths."),
				mcp.WithString("connection_a", mcp.Description("Connection for source_a when source_a_type is 'mongo'. "+connectionDesc)),
				mcp.WithString("connection_b", mcp.Description("Connection for source_b when source_b_type is 'mongo'. "+connectionDesc)),
				mcp.WithString("database_a", mcp.Description("Database for source_a. "+databaseDesc)),
				mcp.WithString("database_b", mcp.Description("Database for source_b. "+databaseDesc)),
				mcp.WithString("source_a", mcp.Required(), mcp.Description("First source: 'collection' or 'collection/id', a file path, or raw JSON")),
				mcp.WithString("source_a_type", mcp.Required(), mcp.Enum("mongo", "file", "json"), mcp.Description("Type of source_a")),
				mcp.WithString("source_b", mcp.Required(), mcp.Description("Second source: 'collection' or 'collection/id', a file path, or raw JSON")),
				mcp.WithString("source_b_type", mcp.Required(), mcp.Enum("mongo", "file", "json"), mcp.Description("Type of source_b")),
				mcp.WithString("mode", mcp.Enum("document", "collection"), mcp.Description("'document' (default) compares two single objects; 'collection' matches documents by _id")),
			),
			Handler: handleCompare,
		},
	}
}

func registerTools(s *server.MCPServer) {
	s.AddTools(toolDefinitions()...)
}
