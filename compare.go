package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

// DiffType categorizes a single field difference.
type DiffType string

const (
	ValueMismatch DiffType = "value_mismatch"
	MissingInA    DiffType = "missing_in_a"
	MissingInB    DiffType = "missing_in_b"
)

// FieldDiff represents one difference at a specific field path.
type FieldDiff struct {
	Path   string      `json:"path"`
	Type   DiffType    `json:"type"`
	ValueA interface{} `json:"value_a,omitempty"`
	ValueB interface{} `json:"value_b,omitempty"`
}

// countByType counts diffs of a given type.
func countByType(diffs []FieldDiff, t DiffType) int {
	n := 0
	for _, d := range diffs {
		if d.Type == t {
			n++
		}
	}
	return n
}

// countLeaves recursively counts leaf fields in a value.
func countLeaves(v interface{}) int {
	switch val := v.(type) {
	case map[string]interface{}:
		if len(val) == 0 {
			return 1
		}
		n := 0
		for _, vv := range val {
			n += countLeaves(vv)
		}
		return n
	case []interface{}:
		if len(val) == 0 {
			return 1
		}
		n := 0
		for _, vv := range val {
			n += countLeaves(vv)
		}
		return n
	default:
		return 1
	}
}

// buildFieldPath joins parent prefix with a child key using dot notation.
func buildFieldPath(prefix, key string) string {
	if prefix == "" {
		return key
	}
	return prefix + "." + key
}

// compareValues recursively compares two values and appends diffs.
// Returns (fieldsCompared, matchedCount).
func compareValues(prefix string, a, b interface{}, diffs *[]FieldDiff) (int, int) {
	mapA, aIsMap := a.(map[string]interface{})
	mapB, bIsMap := b.(map[string]interface{})

	// Both maps — compare keys recursively
	if aIsMap && bIsMap {
		// Collect sorted union of keys
		keySet := make(map[string]bool)
		for k := range mapA {
			keySet[k] = true
		}
		for k := range mapB {
			keySet[k] = true
		}
		keys := make([]string, 0, len(keySet))
		for k := range keySet {
			keys = append(keys, k)
		}
		sort.Strings(keys)

		total, matched := 0, 0
		for _, k := range keys {
			childPath := buildFieldPath(prefix, k)
			_, inA := mapA[k]
			_, inB := mapB[k]

			if inA && inB {
				t, m := compareValues(childPath, mapA[k], mapB[k], diffs)
				total += t
				matched += m
			} else if inA {
				total += countLeaves(mapA[k])
				*diffs = append(*diffs, FieldDiff{Path: childPath, Type: MissingInB, ValueA: mapA[k]})
			} else {
				total += countLeaves(mapB[k])
				*diffs = append(*diffs, FieldDiff{Path: childPath, Type: MissingInA, ValueB: mapB[k]})
			}
		}
		return total, matched
	}

	arrA, aIsArr := a.([]interface{})
	arrB, bIsArr := b.([]interface{})

	// Both arrays — compare element by element
	if aIsArr && bIsArr {
		maxLen := len(arrA)
		if len(arrB) > maxLen {
			maxLen = len(arrB)
		}

		total, matched := 0, 0
		for i := 0; i < maxLen; i++ {
			childPath := prefix + "[" + strconv.Itoa(i) + "]"
			if i < len(arrA) && i < len(arrB) {
				t, m := compareValues(childPath, arrA[i], arrB[i], diffs)
				total += t
				matched += m
			} else if i < len(arrA) {
				total += countLeaves(arrA[i])
				*diffs = append(*diffs, FieldDiff{Path: childPath, Type: MissingInB, ValueA: arrA[i]})
			} else {
				total += countLeaves(arrB[i])
				*diffs = append(*diffs, FieldDiff{Path: childPath, Type: MissingInA, ValueB: arrB[i]})
			}
		}
		return total, matched
	}

	// Leaf comparison (scalars or type mismatch)
	// Types must agree too: "1" vs 1 or "true" vs true is drift, not a match.
	if reflect.TypeOf(a) == reflect.TypeOf(b) && fmt.Sprintf("%v", a) == fmt.Sprintf("%v", b) {
		return 1, 1
	}
	*diffs = append(*diffs, FieldDiff{Path: prefix, Type: ValueMismatch, ValueA: a, ValueB: b})
	return 1, 0
}

// compareDocs compares two documents field by field.
func compareDocs(a, b map[string]interface{}) map[string]interface{} {
	diffs := make([]FieldDiff, 0)
	total, matched := compareValues("", a, b, &diffs)

	return map[string]interface{}{
		"match":                 len(diffs) == 0,
		"total_fields_compared": total,
		"matched":               matched,
		"mismatched":            countByType(diffs, ValueMismatch),
		"missing_in_a":          countByType(diffs, MissingInA),
		"missing_in_b":          countByType(diffs, MissingInB),
		"differences":           diffs,
	}
}

// compareCollections compares two slices of documents matched by stringified "_id".
func compareCollections(docsA, docsB []map[string]interface{}) map[string]interface{} {
	indexA := make(map[string]map[string]interface{})
	for _, doc := range docsA {
		indexA[fmt.Sprintf("%v", doc["_id"])] = doc
	}
	indexB := make(map[string]map[string]interface{})
	for _, doc := range docsB {
		indexB[fmt.Sprintf("%v", doc["_id"])] = doc
	}

	// Sorted union of all IDs
	idSet := make(map[string]bool)
	for k := range indexA {
		idSet[k] = true
	}
	for k := range indexB {
		idSet[k] = true
	}
	ids := make([]string, 0, len(idSet))
	for k := range idSet {
		ids = append(ids, k)
	}
	sort.Strings(ids)

	documentDiffs := make([]map[string]interface{}, 0)
	matchedDocs, mismatchedDocs, missingInA, missingInB := 0, 0, 0, 0

	for _, id := range ids {
		docA, inA := indexA[id]
		docB, inB := indexB[id]

		if inA && inB {
			result := compareDocs(docA, docB)
			result["_id"] = id
			if result["match"].(bool) {
				matchedDocs++
			} else {
				mismatchedDocs++
				documentDiffs = append(documentDiffs, result)
			}
		} else if inA {
			missingInB++
			documentDiffs = append(documentDiffs, map[string]interface{}{"_id": id, "type": string(MissingInB)})
		} else {
			missingInA++
			documentDiffs = append(documentDiffs, map[string]interface{}{"_id": id, "type": string(MissingInA)})
		}
	}

	return map[string]interface{}{
		"match":                len(documentDiffs) == 0,
		"total_documents":      len(ids),
		"matched_documents":    matchedDocs,
		"mismatched_documents": mismatchedDocs,
		"missing_in_a":         missingInA,
		"missing_in_b":         missingInB,
		"document_diffs":       documentDiffs,
	}
}

// parseMongoSource splits "collection" or "collection/id" on the first "/".
func parseMongoSource(source string) (collection, id string, err error) {
	collection, id, _ = strings.Cut(strings.TrimSpace(source), "/")
	if collection == "" {
		return "", "", fmt.Errorf("invalid mongo source %q: want 'collection' or 'collection/id'", source)
	}
	return collection, id, nil
}

// normalizeDocs round-trips docs through JSON so Mongo-sourced values have the
// same Go types as file/json-sourced ones (all numbers float64, etc.).
// Without this, int64(1e18) and float64(1e18) would print differently.
func normalizeDocs(docs []map[string]interface{}) ([]map[string]interface{}, error) {
	b, err := json.Marshal(docs)
	if err != nil {
		return nil, err
	}
	var out []map[string]interface{}
	if err := decodeJSONNumbers(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// resolveSource fetches data based on source type.
// Returns either a single doc or a slice of docs.
func resolveSource(ctx context.Context, conn *Connection, db, source, sourceType string) (
	singleDoc map[string]interface{},
	docs []map[string]interface{},
	isCollection bool,
	truncated bool,
	err error,
) {
	switch sourceType {
	case "mongo":
		collection, id, err := parseMongoSource(source)
		if err != nil {
			return nil, nil, false, false, err
		}
		coll := conn.Client.Database(db).Collection(collection)
		if id == "" {
			max := conn.Config.MaxDocuments
			cur, err := coll.Find(ctx, bson.D{}, collectionFindOptions(max))
			if err != nil {
				return nil, nil, false, false, fmt.Errorf("error reading collection %q: %w", collection, err)
			}
			raw, truncated, err := readDocs(ctx, cur, max)
			if err != nil {
				return nil, nil, false, false, fmt.Errorf("error reading collection %q: %w", collection, err)
			}
			docs, err = normalizeDocs(raw)
			if err != nil {
				return nil, nil, false, false, err
			}
			return nil, docs, true, truncated, nil
		}
		idVal, err := parseID(id, "auto")
		if err != nil {
			return nil, nil, false, false, err
		}
		var raw bson.M
		err = coll.FindOne(ctx, bson.D{{Key: "_id", Value: idVal}}).Decode(&raw)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, nil, false, false, fmt.Errorf("document not found: _id %q in %s.%s", id, db, collection)
		}
		if err != nil {
			return nil, nil, false, false, fmt.Errorf("error fetching document: %w", err)
		}
		normalized, err := normalizeDocs([]map[string]interface{}{docToMap(raw)})
		if err != nil {
			return nil, nil, false, false, err
		}
		return normalized[0], nil, false, false, nil

	case "file":
		data, readErr := os.ReadFile(source)
		if readErr != nil {
			return nil, nil, false, false, fmt.Errorf("failed to read file %q: %w", source, readErr)
		}
		single, docs, isColl, err := parseJSONData(data)
		return single, docs, isColl, false, err

	case "json":
		single, docs, isColl, err := parseJSONData([]byte(source))
		return single, docs, isColl, false, err

	default:
		return nil, nil, false, false, fmt.Errorf("unknown source_type %q: must be 'mongo', 'file', or 'json'", sourceType)
	}
}

// parseJSONData handles unmarshaling JSON bytes into either a single doc or a slice.
// Detects this server's export/auto-save wrapper ("count" + "documents" +
// "collection") and mcp-firestore's ("count" + "documents" + "path").
func parseJSONData(data []byte) (map[string]interface{}, []map[string]interface{}, bool, error) {
	var obj map[string]interface{}
	if err := decodeJSONNumbers(data, &obj); err == nil {
		_, hasCount := obj["count"]
		_, hasCollection := obj["collection"]
		_, hasPath := obj["path"]
		if docsRaw, ok := obj["documents"]; ok && hasCount && (hasCollection || hasPath) {
			if docsArr, ok := docsRaw.([]interface{}); ok {
				return nil, objectsOnly(docsArr), true, nil
			}
		}
		// Plain single object
		return obj, nil, false, nil
	}

	var arr []interface{}
	if err := decodeJSONNumbers(data, &arr); err == nil {
		return nil, objectsOnly(arr), true, nil
	}

	return nil, nil, false, fmt.Errorf("failed to parse JSON: not a valid object or array")
}

func objectsOnly(items []interface{}) []map[string]interface{} {
	docs := make([]map[string]interface{}, 0, len(items))
	for _, item := range items {
		if m, ok := item.(map[string]interface{}); ok {
			docs = append(docs, m)
		}
	}
	return docs
}

// saveCompareResult writes comparison JSON to ExportPath/compare/compare_<timestamp>.json.
func saveCompareResult(cfg ConnectionConfig, data []byte) (string, error) {
	compareDir := filepath.Join(cfg.ExportPath, "compare")
	if err := os.MkdirAll(compareDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create compare dir: %w", err)
	}
	filename := fmt.Sprintf("compare_%s.json", time.Now().Format("20060102_150405"))
	outputFile := filepath.Join(compareDir, filename)
	if err := os.WriteFile(outputFile, data, 0644); err != nil {
		return "", fmt.Errorf("failed to write compare file: %w", err)
	}
	return outputFile, nil
}

// chooseCompareConn picks the first non-nil connection for file-level config use.
func chooseCompareConn(conns ...*Connection) *Connection {
	for _, c := range conns {
		if c != nil {
			return c
		}
	}
	return nil
}

// compareDatabase returns the per-side database override, or the connection default.
func compareDatabase(conn *Connection, override string) string {
	if strings.TrimSpace(override) != "" {
		return strings.TrimSpace(override)
	}
	if conn == nil {
		return ""
	}
	return conn.Config.Database
}

// handleCompare is the MCP handler for mongo_compare.
func handleCompare(ctx context.Context, req mcp.CallToolRequest) (_ *mcp.CallToolResult, err error) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "PANIC in mongo_compare: %v\n%s", r, debug.Stack())
			err = fmt.Errorf("internal error: %v", r)
		}
	}()

	args := req.GetArguments()

	sourceA, _ := args["source_a"].(string)
	sourceAType, _ := args["source_a_type"].(string)
	sourceB, _ := args["source_b"].(string)
	sourceBType, _ := args["source_b_type"].(string)
	mode, _ := args["mode"].(string)
	connNameA, _ := args["connection_a"].(string)
	connNameB, _ := args["connection_b"].(string)
	dbOverrideA, _ := args["database_a"].(string)
	dbOverrideB, _ := args["database_b"].(string)

	if sourceA == "" || sourceAType == "" {
		return toolError("source_a and source_a_type are required")
	}
	if sourceB == "" || sourceBType == "" {
		return toolError("source_b and source_b_type are required")
	}
	if mode == "" {
		mode = "document"
	}
	if mode != "document" && mode != "collection" {
		return toolError(fmt.Sprintf("invalid mode %q: must be 'document' or 'collection'", mode))
	}

	// Only resolve connections when the source type is mongo
	var connA, connB *Connection
	if sourceAType == "mongo" {
		connA, err = connManager.Get(ctx, strings.TrimSpace(connNameA))
		if err != nil {
			return toolError(fmt.Sprintf("connection_a error: %v", err))
		}
	}
	if sourceBType == "mongo" {
		connB, err = connManager.Get(ctx, strings.TrimSpace(connNameB))
		if err != nil {
			return toolError(fmt.Sprintf("connection_b error: %v", err))
		}
	}

	timeout := 30 * time.Second
	if c := chooseCompareConn(connA, connB); c != nil {
		timeout = c.Config.Timeout()
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	singleA, docsA, isCollA, truncA, errA := resolveSource(ctx, connA, compareDatabase(connA, dbOverrideA), sourceA, sourceAType)
	if errA != nil {
		return toolError(fmt.Sprintf("source_a error: %v", errA))
	}
	singleB, docsB, isCollB, truncB, errB := resolveSource(ctx, connB, compareDatabase(connB, dbOverrideB), sourceB, sourceBType)
	if errB != nil {
		return toolError(fmt.Sprintf("source_b error: %v", errB))
	}

	var result map[string]interface{}

	if mode == "collection" {
		// Ensure both are collections
		if !isCollA && singleA != nil {
			docsA = []map[string]interface{}{singleA}
		}
		if !isCollB && singleB != nil {
			docsB = []map[string]interface{}{singleB}
		}
		if docsA == nil {
			docsA = make([]map[string]interface{}, 0)
		}
		if docsB == nil {
			docsB = make([]map[string]interface{}, 0)
		}
		result = compareCollections(docsA, docsB)
	} else {
		// Document mode — use single docs
		var docA, docB map[string]interface{}
		if isCollA && len(docsA) > 0 {
			docA = docsA[0]
		} else if singleA != nil {
			docA = singleA
		} else {
			return toolError("source_a resolved to empty collection in document mode")
		}
		if isCollB && len(docsB) > 0 {
			docB = docsB[0]
		} else if singleB != nil {
			docB = singleB
		} else {
			return toolError("source_b resolved to empty collection in document mode")
		}
		result = compareDocs(docA, docB)
	}

	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("failed to serialize comparison result: %v", err))
	}

	// Always save full result to ExportPath/compare/ (use whichever connection is available)
	saveConn := chooseCompareConn(connA, connB)
	var filePath string
	var saveErr error
	if saveConn != nil {
		filePath, saveErr = saveCompareResult(saveConn.Config, b)
	}

	summary := map[string]interface{}{
		"match": result["match"],
	}
	if mode == "collection" {
		summary["total_documents"] = result["total_documents"]
		summary["matched_documents"] = result["matched_documents"]
		summary["mismatched_documents"] = result["mismatched_documents"]
		summary["missing_in_a"] = result["missing_in_a"]
		summary["missing_in_b"] = result["missing_in_b"]
	} else {
		summary["total_fields_compared"] = result["total_fields_compared"]
		summary["matched"] = result["matched"]
		summary["mismatched"] = result["mismatched"]
		summary["missing_in_a"] = result["missing_in_a"]
		summary["missing_in_b"] = result["missing_in_b"]
	}
	if truncA || truncB {
		summary["truncated_a"] = truncA
		summary["truncated_b"] = truncB
		summary["truncation_note"] = "A mongo collection source exceeded max_documents; only the first max_documents by _id were compared."
	}
	if saveErr == nil && filePath != "" {
		summary["full_data_path"] = filePath
	} else if saveErr != nil {
		summary["save_error"] = saveErr.Error()
	}

	// Try including differences inline — check if it fits
	if mode == "collection" {
		summary["document_diffs"] = result["document_diffs"]
	} else {
		summary["differences"] = result["differences"]
	}

	sb, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("failed to serialize summary: %v", err))
	}

	// Determine max response chars — prefer connA, then connB, then hardcoded default
	maxChars := 50000
	if charConn := chooseCompareConn(connA, connB); charConn != nil {
		maxChars = charConn.Config.MaxResponseChars
	}
	if len(sb) > maxChars {
		if mode == "collection" {
			delete(summary, "document_diffs")
		} else {
			delete(summary, "differences")
		}
		summary["note"] = "Full diff too large for inline response. Read the file at full_data_path for details."
		return jsonResult(summary)
	}

	return mcp.NewToolResultText(string(sb)), nil
}

// collectionFindOptions reads a whole collection deterministically (sorted by
// _id) and fetches one extra document so truncation can be detected.
func collectionFindOptions(max int) *options.FindOptionsBuilder {
	return options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}).SetLimit(int64(max) + 1)
}

// decodeJSONNumbers unmarshals with UseNumber so integers above 2^53 keep
// full precision (plain float64 would merge distinct NumberLong values).
func decodeJSONNumbers(data []byte, v interface{}) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if dec.More() {
		return fmt.Errorf("unexpected data after JSON value")
	}
	return nil
}
