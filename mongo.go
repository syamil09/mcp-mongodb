package main

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// Handlers validate every argument before resolving the connection, so bad
// input (including write stages) is refused before any network call.

func connFromArgs(ctx context.Context, args map[string]interface{}) (*Connection, error) {
	name, _ := args["connection"].(string)
	return connManager.Get(ctx, strings.TrimSpace(name))
}

// databaseName returns the "database" argument, or the connection's default.
func databaseName(conn *Connection, args map[string]interface{}) string {
	if db, _ := args["database"].(string); strings.TrimSpace(db) != "" {
		return strings.TrimSpace(db)
	}
	return conn.Config.Database
}

func requireString(args map[string]interface{}, key string) (string, error) {
	v, _ := args[key].(string)
	v = strings.TrimSpace(v)
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

// clampLimit returns the requested limit capped at max; absent, zero or
// negative means max.
func clampLimit(requested interface{}, max int) int64 {
	n, ok := requested.(float64)
	if !ok || n < 1 || n > float64(max) {
		return int64(max)
	}
	return int64(n)
}

// readDocs decodes up to max documents from cur. truncated reports that more
// documents were available.
func readDocs(ctx context.Context, cur *mongo.Cursor, max int) (docs []map[string]interface{}, truncated bool, err error) {
	defer cur.Close(context.Background())
	docs = make([]map[string]interface{}, 0)
	for cur.Next(ctx) {
		if len(docs) >= max {
			return docs, true, nil
		}
		var raw bson.M
		if err := cur.Decode(&raw); err != nil {
			return nil, false, err
		}
		docs = append(docs, docToMap(raw))
	}
	if err := cur.Err(); err != nil {
		return nil, false, err
	}
	return docs, false, nil
}

func handleGetDocument(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	collection, err := requireString(args, "collection")
	if err != nil {
		return toolError(err.Error())
	}
	idStr, err := requireString(args, "id")
	if err != nil {
		return toolError(err.Error())
	}
	idType, _ := args["id_type"].(string)
	id, err := parseID(idStr, idType)
	if err != nil {
		return toolError(err.Error())
	}

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)

	var raw bson.M
	err = conn.Client.Database(db).Collection(collection).
		FindOne(ctx, bson.D{{Key: "_id", Value: id}}).Decode(&raw)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return toolError(fmt.Sprintf("document not found: _id %q in %s.%s", idStr, db, collection))
	}
	if err != nil {
		return toolError(fmt.Sprintf("error fetching document: %v", err))
	}

	return respondDocument(conn.Config, db+"_"+collection+"_"+idStr, docToMap(raw),
		map[string]interface{}{"collection": collection, "database": db})
}

func handleGetCollection(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	collection, err := requireString(args, "collection")
	if err != nil {
		return toolError(err.Error())
	}

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)
	max := conn.Config.MaxDocuments

	cur, err := conn.Client.Database(db).Collection(collection).
		Find(ctx, bson.D{}, options.Find().SetLimit(int64(max)))
	if err != nil {
		return toolError(fmt.Sprintf("error reading collection: %v", err))
	}
	docs, _, err := readDocs(ctx, cur, max)
	if err != nil {
		return toolError(fmt.Sprintf("error reading collection: %v", err))
	}

	return respondDocuments(conn.Config, db+"_"+collection, docs, map[string]interface{}{
		"collection":    collection,
		"database":      db,
		"max_documents": max,
	})
}

func handleQuery(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	collection, err := requireString(args, "collection")
	if err != nil {
		return toolError(err.Error())
	}
	filter, err := parseExtJSONDoc(args["filter"], "filter")
	if err != nil {
		return toolError(err.Error())
	}
	projection, err := parseExtJSONDoc(args["projection"], "projection")
	if err != nil {
		return toolError(err.Error())
	}
	sortDoc, err := parseExtJSONDoc(args["sort"], "sort")
	if err != nil {
		return toolError(err.Error())
	}

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)
	limit := clampLimit(args["limit"], conn.Config.MaxDocuments)

	opts := options.Find().SetLimit(limit)
	if len(projection) > 0 {
		opts.SetProjection(projection)
	}
	if len(sortDoc) > 0 {
		opts.SetSort(sortDoc)
	}
	if skip, ok := args["skip"].(float64); ok && skip >= 1 {
		opts.SetSkip(int64(skip))
	}

	cur, err := conn.Client.Database(db).Collection(collection).Find(ctx, filter, opts)
	if err != nil {
		return toolError(fmt.Sprintf("query error: %v", err))
	}
	docs, _, err := readDocs(ctx, cur, int(limit))
	if err != nil {
		return toolError(fmt.Sprintf("query error: %v", err))
	}

	return respondDocuments(conn.Config, db+"_"+collection, docs, map[string]interface{}{
		"collection": collection,
		"database":   db,
		"limit":      limit,
	})
}

func handleCount(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	collection, err := requireString(args, "collection")
	if err != nil {
		return toolError(err.Error())
	}
	filter, err := parseExtJSONDoc(args["filter"], "filter")
	if err != nil {
		return toolError(err.Error())
	}
	estimated, _ := args["estimated"].(bool)

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)
	coll := conn.Client.Database(db).Collection(collection)

	var count int64
	if estimated {
		count, err = coll.EstimatedDocumentCount(ctx)
	} else {
		count, err = coll.CountDocuments(ctx, filter)
	}
	if err != nil {
		return toolError(fmt.Sprintf("count error: %v", err))
	}

	resp := map[string]interface{}{
		"collection": collection,
		"database":   db,
		"count":      count,
		"estimated":  estimated,
		"filter":     sanitizeValue(filter),
	}
	if estimated && len(filter) > 0 {
		resp["note"] = "filter ignored: estimated count uses collection metadata"
	}
	return jsonResult(resp)
}

func handleAggregate(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	collection, err := requireString(args, "collection")
	if err != nil {
		return toolError(err.Error())
	}
	pipeline, err := parseExtJSONArray(args["pipeline"], "pipeline")
	if err != nil {
		return toolError(err.Error())
	}
	if err := checkPipelineReadOnly(pipeline); err != nil {
		return toolError(err.Error())
	}

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)
	max := conn.Config.MaxDocuments

	cur, err := conn.Client.Database(db).Collection(collection).Aggregate(ctx, pipeline)
	if err != nil {
		return toolError(fmt.Sprintf("aggregate error: %v", err))
	}
	docs, truncated, err := readDocs(ctx, cur, max)
	if err != nil {
		return toolError(fmt.Sprintf("aggregate error: %v", err))
	}

	return respondDocuments(conn.Config, db+"_"+collection, docs, map[string]interface{}{
		"collection": collection,
		"database":   db,
		"truncated":  truncated,
	})
}

func handleListCollections(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	nameFilter, _ := args["name_filter"].(string)

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)

	filter := bson.D{}
	if strings.TrimSpace(nameFilter) != "" {
		filter = bson.D{{Key: "name", Value: bson.D{{Key: "$regex", Value: nameFilter}}}}
	}
	specs, err := conn.Client.Database(db).ListCollectionSpecifications(ctx, filter)
	if err != nil {
		return toolError(fmt.Sprintf("error listing collections: %v", err))
	}
	sort.Slice(specs, func(i, j int) bool { return specs[i].Name < specs[j].Name })

	collections := make([]map[string]interface{}, 0, len(specs))
	for _, s := range specs {
		collections = append(collections, map[string]interface{}{"name": s.Name, "type": s.Type})
	}
	return jsonResult(map[string]interface{}{
		"database":    db,
		"count":       len(collections),
		"collections": collections,
	})
}

func handleListDatabases(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()
	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()

	result, err := conn.Client.ListDatabases(ctx, bson.D{}, options.ListDatabases().SetAuthorizedDatabases(true))
	if err != nil {
		return toolError(fmt.Sprintf("cannot list databases (the user may lack the listDatabases privilege): %v", err))
	}

	databases := make([]map[string]interface{}, 0, len(result.Databases))
	for _, d := range result.Databases {
		databases = append(databases, map[string]interface{}{
			"name":         d.Name,
			"size_on_disk": d.SizeOnDisk,
			"empty":        d.Empty,
		})
	}
	return jsonResult(map[string]interface{}{
		"connection": conn.Name,
		"count":      len(databases),
		"databases":  databases,
	})
}
