package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// toolError returns msg as an MCP tool error. Handlers never return a Go error.
func toolError(msg string) (*mcp.CallToolResult, error) {
	return mcp.NewToolResultError(msg), nil
}

// jsonResult serializes v as indented JSON text.
func jsonResult(v interface{}) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("failed to serialize response: %v", err))
	}
	return mcp.NewToolResultText(string(b)), nil
}

// sanitizePath converts a label to a safe filename component.
// e.g. "shop/orders:1" → "shop_orders_1"
func sanitizePath(path string) string {
	r := strings.NewReplacer("/", "_", "\\", "_", ":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_")
	return r.Replace(path)
}

// saveFullData writes fullJSON to ExportPath/temp/_auto_<label>_<timestamp>.json.
// Returns the path of the written file.
func saveFullData(cfg ConnectionConfig, label string, fullJSON []byte) (string, error) {
	tempDir := filepath.Join(cfg.ExportPath, "temp")
	if err := os.MkdirAll(tempDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create temp dir: %w", err)
	}
	timestamp := time.Now().Format("20060102_150405")
	filename := fmt.Sprintf("_auto_%s_%s.json", sanitizePath(label), timestamp)
	outputFile := filepath.Join(tempDir, filename)
	if err := os.WriteFile(outputFile, fullJSON, 0644); err != nil {
		return "", fmt.Errorf("failed to write temp file: %w", err)
	}
	return outputFile, nil
}

// buildSmartPreview returns a preview slice of docs.
// Docs whose individual JSON exceeds DocPreviewChars are replaced with field-names-only stubs.
func buildSmartPreview(cfg ConnectionConfig, docs []map[string]interface{}) []map[string]interface{} {
	preview := make([]map[string]interface{}, len(docs))
	for i, doc := range docs {
		b, err := json.Marshal(doc)
		if err == nil && len(b) <= cfg.DocPreviewChars {
			preview[i] = doc
			continue
		}
		// Collect top-level field names (sorted for consistent output)
		fields := make([]string, 0, len(doc))
		for k := range doc {
			if k != "_id" {
				fields = append(fields, k)
			}
		}
		sort.Strings(fields)
		preview[i] = map[string]interface{}{
			"_id":      doc["_id"],
			"_preview": "fields_only",
			"_fields":  fields,
		}
	}
	return preview
}

// buildOverflowResponse builds a preview response when the full response exceeds MaxResponseChars.
// It progressively truncates preview docs until the response fits within MaxResponseChars.
func buildOverflowResponse(cfg ConnectionConfig, docs []map[string]interface{}, totalChars int, filePath string, saveErr error, extraFields map[string]interface{}) (*mcp.CallToolResult, error) {
	previewDocs := buildSmartPreview(cfg, docs)

	// Progressively truncate until preview fits within MaxResponseChars
	for len(previewDocs) > 1 {
		resp := buildPreviewMap(previewDocs, len(docs), totalChars, filePath, saveErr, extraFields, len(previewDocs) < len(docs))
		pb, err := json.MarshalIndent(resp, "", "  ")
		if err != nil {
			return toolError(fmt.Sprintf("failed to serialize preview: %v", err))
		}
		if len(pb) <= cfg.MaxResponseChars {
			return mcp.NewToolResultText(string(pb)), nil
		}
		// Still too large — cut preview docs in half
		previewDocs = previewDocs[:len(previewDocs)/2]
	}

	// Final attempt with remaining docs
	resp := buildPreviewMap(previewDocs, len(docs), totalChars, filePath, saveErr, extraFields, true)
	return jsonResult(resp)
}

func buildPreviewMap(previewDocs []map[string]interface{}, totalCount int, totalChars int, filePath string, saveErr error, extraFields map[string]interface{}, truncated bool) map[string]interface{} {
	resp := map[string]interface{}{
		"count":         totalCount,
		"preview_count": len(previewDocs),
		"total_chars":   totalChars,
		"note":          "Response too large. Full data saved to file. Large documents show field names only.",
		"documents":     previewDocs,
	}
	if truncated {
		resp["preview_truncated"] = true
	}
	if saveErr == nil {
		resp["full_data_path"] = filePath
	} else {
		resp["save_error"] = saveErr.Error()
	}
	for k, v := range extraFields {
		resp[k] = v
	}
	return resp
}

// respondDocuments returns a multi-document response: in full when it fits in
// MaxResponseChars, otherwise saved to file with a smart preview.
// extra holds the non-document fields (collection, database, ...).
func respondDocuments(cfg ConnectionConfig, label string, docs []map[string]interface{}, extra map[string]interface{}) (*mcp.CallToolResult, error) {
	full := make(map[string]interface{}, len(extra)+2)
	for k, v := range extra {
		full[k] = v
	}
	full["count"] = len(docs)
	full["documents"] = docs

	b, err := json.MarshalIndent(full, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("failed to serialize response: %v", err))
	}
	if len(b) <= cfg.MaxResponseChars {
		return mcp.NewToolResultText(string(b)), nil
	}
	filePath, saveErr := saveFullData(cfg, label, b)
	return buildOverflowResponse(cfg, docs, len(b), filePath, saveErr, extra)
}

// respondDocument returns a single document: in full when it fits, otherwise
// saved to file with a field-names-only preview.
func respondDocument(cfg ConnectionConfig, label string, doc map[string]interface{}, extra map[string]interface{}) (*mcp.CallToolResult, error) {
	b, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return toolError(fmt.Sprintf("failed to serialize document: %v", err))
	}
	if len(b) <= cfg.MaxResponseChars {
		return mcp.NewToolResultText(string(b)), nil
	}

	filePath, saveErr := saveFullData(cfg, label, b)
	preview := buildSmartPreview(cfg, []map[string]interface{}{doc})
	resp := map[string]interface{}{
		"total_chars": len(b),
		"note":        "Document too large. Full data saved to file. Showing field names only.",
		"document":    preview[0],
	}
	for k, v := range extra {
		resp[k] = v
	}
	if saveErr == nil {
		resp["full_data_path"] = filePath
	} else {
		resp["save_error"] = saveErr.Error()
	}
	return jsonResult(resp)
}
