package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

// resultText returns the text of a tool result and whether it is an error.
func resultText(t *testing.T, res *mcp.CallToolResult) (string, bool) {
	t.Helper()
	if res == nil || len(res.Content) == 0 {
		t.Fatal("empty tool result")
	}
	switch c := res.Content[0].(type) {
	case mcp.TextContent:
		return c.Text, res.IsError
	case *mcp.TextContent:
		return c.Text, res.IsError
	}
	t.Fatalf("unexpected content type %T", res.Content[0])
	return "", false
}

// decodeJSON unmarshals a tool result's text into a map.
func decodeJSON(t *testing.T, text string) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(text), &m); err != nil {
		t.Fatalf("result is not JSON: %v\n%s", err, text)
	}
	return m
}

func overflowConfig(t *testing.T, maxResponseChars, docPreviewChars int) ConnectionConfig {
	t.Helper()
	return ConnectionConfig{
		ExportPath:       t.TempDir(),
		MaxDocuments:     500,
		MaxResponseChars: maxResponseChars,
		DocPreviewChars:  docPreviewChars,
	}
}

func makeDocs(n int, filler string) []map[string]interface{} {
	docs := make([]map[string]interface{}, n)
	for i := range docs {
		docs[i] = map[string]interface{}{"_id": fmt.Sprintf("id%03d", i), "name": fmt.Sprintf("doc %d", i), "filler": filler}
	}
	return docs
}

func TestSanitizePath(t *testing.T) {
	if got := sanitizePath(`db_a/b:c*d?e"f<g>h|i\j`); got != "db_a_b_c_d_e_f_g_h_i_j" {
		t.Errorf("sanitizePath = %q", got)
	}
}

func TestRespondDocuments_SmallPassesThrough(t *testing.T) {
	cfg := overflowConfig(t, 50000, 2000)
	docs := makeDocs(3, "x")
	res, err := respondDocuments(cfg, "db_orders", docs, map[string]interface{}{"collection": "orders", "database": "db"})
	if err != nil {
		t.Fatal(err)
	}
	text, isErr := resultText(t, res)
	if isErr {
		t.Fatalf("unexpected error result: %s", text)
	}
	m := decodeJSON(t, text)
	if m["collection"] != "orders" || m["database"] != "db" || m["count"] != float64(3) {
		t.Errorf("unexpected envelope: %v", m)
	}
	if _, ok := m["full_data_path"]; ok {
		t.Error("small response must not be saved to file")
	}
	if entries, _ := os.ReadDir(filepath.Join(cfg.ExportPath, "temp")); len(entries) != 0 {
		t.Errorf("no temp files expected, found %d", len(entries))
	}
}

func TestRespondDocuments_LargeSavedWithTruncatedPreview(t *testing.T) {
	cfg := overflowConfig(t, 3000, 2000)
	docs := makeDocs(50, strings.Repeat("y", 100))
	res, err := respondDocuments(cfg, "db/orders", docs, map[string]interface{}{"collection": "orders", "database": "db"})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resultText(t, res)
	if len(text) > cfg.MaxResponseChars {
		t.Errorf("preview is %d chars, limit %d", len(text), cfg.MaxResponseChars)
	}
	m := decodeJSON(t, text)
	if m["count"] != float64(50) {
		t.Errorf("count = %v, want 50", m["count"])
	}
	if pc := m["preview_count"].(float64); pc >= 50 || pc < 1 {
		t.Errorf("preview_count = %v, want between 1 and 49", pc)
	}
	if m["preview_truncated"] != true {
		t.Error("preview_truncated should be true")
	}
	if m["collection"] != "orders" || m["total_chars"] == nil || m["note"] == nil {
		t.Errorf("missing envelope fields: %v", m)
	}

	path, _ := m["full_data_path"].(string)
	if !strings.HasPrefix(filepath.Base(path), "_auto_db_orders_") || filepath.Dir(path) != filepath.Join(cfg.ExportPath, "temp") {
		t.Errorf("unexpected full_data_path %q", path)
	}
	saved, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("full data file not readable: %v", err)
	}
	full := decodeJSON(t, string(saved))
	if len(full["documents"].([]interface{})) != 50 {
		t.Error("saved file must hold all 50 documents")
	}
}

func TestBuildSmartPreview_FieldsOnlyStub(t *testing.T) {
	cfg := overflowConfig(t, 50000, 100)
	small := map[string]interface{}{"_id": "s", "a": 1}
	big := map[string]interface{}{"_id": "b", "zeta": strings.Repeat("z", 200), "alpha": 1}
	preview := buildSmartPreview(cfg, []map[string]interface{}{small, big})

	if !reflect.DeepEqual(preview[0], small) {
		t.Errorf("small doc should be shown in full, got %v", preview[0])
	}
	want := map[string]interface{}{"_id": "b", "_preview": "fields_only", "_fields": []string{"alpha", "zeta"}}
	if !reflect.DeepEqual(preview[1], want) {
		t.Errorf("big doc stub = %v, want %v", preview[1], want)
	}
}

func TestRespondDocuments_SaveErrorReported(t *testing.T) {
	cfg := overflowConfig(t, 500, 2000)
	blocker := filepath.Join(cfg.ExportPath, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	cfg.ExportPath = blocker // a file, so MkdirAll(ExportPath/temp) fails

	res, err := respondDocuments(cfg, "db_c", makeDocs(20, strings.Repeat("q", 50)), nil)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resultText(t, res)
	m := decodeJSON(t, text)
	if _, ok := m["save_error"]; !ok {
		t.Errorf("want save_error, got %v", m)
	}
	if _, ok := m["full_data_path"]; ok {
		t.Error("full_data_path must be absent when save failed")
	}
}

func TestRespondDocument_LargeSingleDocument(t *testing.T) {
	cfg := overflowConfig(t, 1000, 2000)
	doc := map[string]interface{}{"_id": "big", "payload": strings.Repeat("p", 5000), "other": 1}

	res, err := respondDocument(cfg, "db_c_big", doc, map[string]interface{}{"collection": "c", "database": "db"})
	if err != nil {
		t.Fatal(err)
	}
	text, _ := resultText(t, res)
	m := decodeJSON(t, text)
	stub := m["document"].(map[string]interface{})
	if stub["_preview"] != "fields_only" || stub["_id"] != "big" {
		t.Errorf("want fields-only stub, got %v", stub)
	}
	if _, err := os.Stat(m["full_data_path"].(string)); err != nil {
		t.Errorf("full data file missing: %v", err)
	}
	if m["collection"] != "c" {
		t.Errorf("extra fields missing: %v", m)
	}

	small := map[string]interface{}{"_id": "s"}
	res, _ = respondDocument(cfg, "db_c_s", small, nil)
	text, _ = resultText(t, res)
	if got := decodeJSON(t, text); got["_id"] != "s" || len(got) != 1 {
		t.Errorf("small document should be returned as-is, got %v", got)
	}
}
