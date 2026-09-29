package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// exportItem is one entry of the batch "documents" argument.
type exportItem struct {
	Collection string `json:"collection"`
	ID         string `json:"id"`
}

// resolveOutputPath anchors a relative output_path at the export directory,
// because the MCP server's working directory is unpredictable.
func resolveOutputPath(exportPath, outputPath string) string {
	if filepath.IsAbs(outputPath) {
		return outputPath
	}
	return filepath.Join(exportPath, outputPath)
}

// exportFileName builds "{database}_{collection}[_{id}]_{timestamp}.json" with
// characters that are illegal in Windows filenames replaced.
func exportFileName(database, collection, id string, now time.Time) string {
	base := database + "_" + collection
	if id != "" {
		base += "_" + id
	}
	return sanitizePath(base) + "_" + now.Format("20060102_150405") + ".json"
}

// writeExport writes the export payload to outputFile, creating directories.
func writeExport(outputFile, database, collection string, docs []map[string]interface{}) error {
	if err := os.MkdirAll(filepath.Dir(outputFile), 0755); err != nil {
		return fmt.Errorf("failed to create output directory: %w", err)
	}
	payload := map[string]interface{}{
		"collection":  collection,
		"database":    database,
		"count":       len(docs),
		"exported_at": time.Now().UTC().Format(time.RFC3339),
		"documents":   docs,
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to serialize: %w", err)
	}
	if err := os.WriteFile(outputFile, b, 0644); err != nil {
		return fmt.Errorf("failed to write file: %w", err)
	}
	return nil
}

// fetchForExport returns one document when id is set, otherwise the documents
// matching filter (up to MaxDocuments).
func fetchForExport(ctx context.Context, conn *Connection, db, collection, id string, filter bson.D) ([]map[string]interface{}, error) {
	coll := conn.Client.Database(db).Collection(collection)
	if id != "" {
		idVal, err := parseID(id, "auto")
		if err != nil {
			return nil, err
		}
		var raw bson.M
		err = coll.FindOne(ctx, bson.D{{Key: "_id", Value: idVal}}).Decode(&raw)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, fmt.Errorf("document not found: _id %q in %s.%s", id, db, collection)
		}
		if err != nil {
			return nil, fmt.Errorf("error fetching document: %w", err)
		}
		return []map[string]interface{}{docToMap(raw)}, nil
	}

	if filter == nil {
		filter = bson.D{}
	}
	max := conn.Config.MaxDocuments
	cur, err := coll.Find(ctx, filter, options.Find().SetLimit(int64(max)))
	if err != nil {
		return nil, fmt.Errorf("error reading collection %q: %w", collection, err)
	}
	docs, _, err := readDocs(ctx, cur, max)
	if err != nil {
		return nil, fmt.Errorf("error reading collection %q: %w", collection, err)
	}
	return docs, nil
}

// handleExportJSON writes a collection, filtered subset, single document, or a
// batch of documents to JSON files under the connection's export_path.
func handleExportJSON(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := req.GetArguments()

	var items []exportItem
	docsRaw, batch := args["documents"]
	batch = batch && docsRaw != nil
	collection, _ := args["collection"].(string)
	collection = strings.TrimSpace(collection)
	id, _ := args["id"].(string)
	id = strings.TrimSpace(id)
	var filter bson.D

	if batch {
		raw, _ := json.Marshal(docsRaw)
		if err := json.Unmarshal(raw, &items); err != nil {
			return toolError("documents must be an array of {collection, id} objects")
		}
		if len(items) == 0 {
			return toolError("documents array is empty")
		}
	} else {
		if collection == "" {
			return toolError("collection or documents is required")
		}
		var err error
		filter, err = parseExtJSONDoc(args["filter"], "filter")
		if err != nil {
			return toolError(err.Error())
		}
		if id != "" && len(filter) > 0 {
			return toolError("use either id or filter, not both")
		}
	}

	conn, err := connFromArgs(ctx, args)
	if err != nil {
		return toolError(err.Error())
	}
	parentCtx := ctx
	ctx, cancel := context.WithTimeout(ctx, conn.Config.Timeout())
	defer cancel()
	db := databaseName(conn, args)

	if batch {
		results := make([]map[string]interface{}, 0, len(items))
		totalDocs := 0
		for _, item := range items {
			entry := map[string]interface{}{"collection": item.Collection, "id": item.ID}
			if strings.TrimSpace(item.Collection) == "" || strings.TrimSpace(item.ID) == "" {
				entry["error"] = "both collection and id are required"
				results = append(results, entry)
				continue
			}
			// Each item gets its own deadline so one slow item cannot starve the rest.
			itemCtx, itemCancel := context.WithTimeout(parentCtx, conn.Config.Timeout())
			docs, err := fetchForExport(itemCtx, conn, db, item.Collection, item.ID, nil)
			itemCancel()
			if err == nil {
				outFile := filepath.Join(conn.Config.ExportPath, exportFileName(db, item.Collection, item.ID, time.Now()))
				if err = writeExport(outFile, db, item.Collection, docs); err == nil {
					entry["file"] = outFile
					entry["count"] = len(docs)
					totalDocs += len(docs)
				}
			}
			if err != nil {
				entry["error"] = err.Error()
			}
			results = append(results, entry)
		}
		return jsonResult(map[string]interface{}{
			"batch":      true,
			"database":   db,
			"total":      len(items),
			"total_docs": totalDocs,
			"exports":    results,
		})
	}

	docs, err := fetchForExport(ctx, conn, db, collection, id, filter)
	if err != nil {
		return toolError(err.Error())
	}
	outFile := filepath.Join(conn.Config.ExportPath, exportFileName(db, collection, id, time.Now()))
	if customPath, _ := args["output_path"].(string); strings.TrimSpace(customPath) != "" {
		outFile = resolveOutputPath(conn.Config.ExportPath, strings.TrimSpace(customPath))
	}
	if err := writeExport(outFile, db, collection, docs); err != nil {
		return toolError(err.Error())
	}

	entry := map[string]interface{}{"collection": collection, "file": outFile, "count": len(docs)}
	if id != "" {
		entry["id"] = id
	}
	return jsonResult(map[string]interface{}{
		"batch":    false,
		"database": db,
		"exports":  []map[string]interface{}{entry},
	})
}
