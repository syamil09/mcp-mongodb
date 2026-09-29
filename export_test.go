package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestResolveOutputPath(t *testing.T) {
	exportDir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "x.json")
	if got := resolveOutputPath(exportDir, abs); got != abs {
		t.Errorf("absolute path changed: %q", got)
	}
	if got, want := resolveOutputPath(exportDir, "sub/x.json"), filepath.Join(exportDir, "sub", "x.json"); got != want {
		t.Errorf("relative path = %q, want %q", got, want)
	}
}

func TestExportFileName(t *testing.T) {
	now := time.Date(2026, 9, 29, 13, 4, 5, 0, time.Local)
	cases := []struct {
		db, coll, id, want string
	}{
		{"shop", "orders", "", "shop_orders_20260929_130405.json"},
		{"shop", "orders", "650000000000000000000001", "shop_orders_650000000000000000000001_20260929_130405.json"},
		{"shop", "orders", `a/b:c*d?"e<f>g|h\i`, "shop_orders_a_b_c_d__e_f_g_h_i_20260929_130405.json"},
	}
	for _, c := range cases {
		if got := exportFileName(c.db, c.coll, c.id, now); got != c.want {
			t.Errorf("exportFileName(%q, %q, %q) = %q, want %q", c.db, c.coll, c.id, got, c.want)
		}
	}
}

func TestWriteExport_CreatesDirsAndPayload(t *testing.T) {
	out := filepath.Join(t.TempDir(), "nested", "dir", "export.json")
	docs := []map[string]interface{}{{"_id": "a", "n": 1}, {"_id": "b", "n": 2}}
	if err := writeExport(out, "shop", "orders", docs); err != nil {
		t.Fatalf("writeExport: %v", err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	m := decodeJSON(t, string(data))
	if m["collection"] != "orders" || m["database"] != "shop" || m["count"] != float64(2) || m["exported_at"] == nil {
		t.Errorf("unexpected payload envelope: %v", m)
	}
	if len(m["documents"].([]interface{})) != 2 {
		t.Error("want 2 documents in file")
	}
}

func TestHandleExportJSON_ValidatesBeforeConnecting(t *testing.T) {
	useUnreachableManager(t)

	cases := []struct {
		name    string
		args    map[string]interface{}
		wantErr string
	}{
		{"nothing", map[string]interface{}{}, "collection or documents is required"},
		{"documents not array of objects", map[string]interface{}{"documents": "orders"}, "documents must be an array of {collection, id} objects"},
		{"documents empty", map[string]interface{}{"documents": []interface{}{}}, "documents array is empty"},
		{"id and filter", map[string]interface{}{"collection": "c", "id": "x", "filter": `{"a": 1}`}, "use either id or filter, not both"},
		{"bad filter", map[string]interface{}{"collection": "c", "filter": "{bad"}, "filter is not valid Extended JSON"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := callTool(t, handleExportJSON, c.args)
			if !isErr || !strings.Contains(text, c.wantErr) {
				t.Errorf("want error %q, got (isErr=%v) %q", c.wantErr, isErr, text)
			}
		})
	}
}
