package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustJSONDoc(t *testing.T, s string) map[string]interface{} {
	t.Helper()
	single, _, isColl, err := parseJSONData([]byte(s))
	if err != nil || isColl {
		t.Fatalf("not a single JSON object: %s (%v)", s, err)
	}
	return single
}

func TestCompareDocs_Identical(t *testing.T) {
	a := mustJSONDoc(t, `{"_id": "1", "name": "x", "tags": ["a", "b"], "addr": {"city": "Bandung"}}`)
	b := mustJSONDoc(t, `{"_id": "1", "name": "x", "tags": ["a", "b"], "addr": {"city": "Bandung"}}`)
	r := compareDocs(a, b)
	if r["match"] != true || r["total_fields_compared"] != 5 || r["matched"] != 5 {
		t.Errorf("unexpected result: %v", r)
	}
}

func TestCompareDocs_Differences(t *testing.T) {
	a := mustJSONDoc(t, `{"_id": "1", "addr": {"city": "Bandung", "zip": "40111"}, "items": [1, 2, 3], "onlyA": true}`)
	b := mustJSONDoc(t, `{"_id": "1", "addr": {"city": "Jakarta"}, "items": [1, 5], "onlyB": {"x": 1, "y": 2}}`)
	r := compareDocs(a, b)

	if r["match"] != false || r["mismatched"] != 2 || r["missing_in_a"] != 1 || r["missing_in_b"] != 3 {
		t.Fatalf("unexpected counts: %v", r)
	}
	got := map[string]DiffType{}
	for _, d := range r["differences"].([]FieldDiff) {
		got[d.Path] = d.Type
	}
	want := map[string]DiffType{
		"addr.city": ValueMismatch,
		"addr.zip":  MissingInB,
		"items[1]":  ValueMismatch,
		"items[2]":  MissingInB,
		"onlyA":     MissingInB,
		"onlyB":     MissingInA,
	}
	for path, typ := range want {
		if got[path] != typ {
			t.Errorf("path %s: got %q, want %q (all: %v)", path, got[path], typ, got)
		}
	}
	// onlyB has 2 leaves; total = _id + city + zip + 3 items + onlyA + 2 onlyB leaves = 9
	if r["total_fields_compared"] != 9 {
		t.Errorf("total_fields_compared = %v, want 9", r["total_fields_compared"])
	}
}

func TestCompareCollections_MatchesByID(t *testing.T) {
	_, docsA, _, _ := parseJSONData([]byte(`[{"_id": "1", "v": 1}, {"_id": "2", "v": 2}, {"_id": "3", "v": 3}]`))
	_, docsB, _, _ := parseJSONData([]byte(`[{"_id": "2", "v": 20}, {"_id": "1", "v": 1}, {"_id": "4", "v": 4}]`))
	r := compareCollections(docsA, docsB)

	if r["match"] != false || r["total_documents"] != 4 || r["matched_documents"] != 1 ||
		r["mismatched_documents"] != 1 || r["missing_in_a"] != 1 || r["missing_in_b"] != 1 {
		t.Errorf("unexpected summary: %v", r)
	}
	diffs := r["document_diffs"].([]map[string]interface{})
	if len(diffs) != 3 || diffs[0]["_id"] != "2" || diffs[1]["_id"] != "3" || diffs[2]["_id"] != "4" {
		t.Errorf("document_diffs should be sorted by _id: %v", diffs)
	}
}

func TestParseMongoSource(t *testing.T) {
	cases := []struct {
		in, coll, id string
		wantErr      bool
	}{
		{"orders", "orders", "", false},
		{"orders/650000000000000000000001", "orders", "650000000000000000000001", false},
		{"orders/a/b", "orders", "a/b", false},
		{" orders ", "orders", "", false},
		{"/abc", "", "", true},
		{"", "", "", true},
	}
	for _, c := range cases {
		coll, id, err := parseMongoSource(c.in)
		if (err != nil) != c.wantErr || coll != c.coll || id != c.id {
			t.Errorf("parseMongoSource(%q) = %q, %q, %v", c.in, coll, id, err)
		}
	}
}

func TestParseJSONData_Shapes(t *testing.T) {
	single, docs, isColl, err := parseJSONData([]byte(`{"_id": "1", "count": 3}`))
	if err != nil || isColl || single["_id"] != "1" || docs != nil {
		t.Errorf("plain object with a count field must stay a single doc: %v %v %v %v", single, docs, isColl, err)
	}

	_, docs, isColl, err = parseJSONData([]byte(`[{"_id": "1"}, 5, {"_id": "2"}]`))
	if err != nil || !isColl || len(docs) != 2 {
		t.Errorf("array: %v %v %v", docs, isColl, err)
	}

	_, docs, isColl, err = parseJSONData([]byte(`{"path": "users", "count": 1, "documents": [{"_id": "u1"}]}`))
	if err != nil || !isColl || len(docs) != 1 {
		t.Errorf("mcp-firestore export wrapper: %v %v %v", docs, isColl, err)
	}

	if _, _, _, err := parseJSONData([]byte(`not json`)); err == nil {
		t.Error("want error for invalid JSON")
	}
}

// An export file written by mongo_export_json must be read back as a
// collection, not silently treated as one big document.
func TestParseJSONData_ReadsOwnExportFile(t *testing.T) {
	out := filepath.Join(t.TempDir(), "export.json")
	docs := []map[string]interface{}{{"_id": "a", "n": 1}, {"_id": "b", "n": 2}}
	if err := writeExport(out, "shop", "orders", docs); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	_, got, isColl, err := parseJSONData(data)
	if err != nil || !isColl || len(got) != 2 {
		t.Errorf("export file: isColl=%v len=%d err=%v", isColl, len(got), err)
	}
}

// Mongo int64 vs file float64 must compare equal after normalization;
// fmt %v prints int64(1e18) as 1000000000000000000 but float64(1e18) as 1e+18.
func TestNormalizeDocs_NumberTypesCompareEqual(t *testing.T) {
	mongoDoc := docToMap(map[string]interface{}{"_id": "x", "big": int64(1000000000000000000), "small": int32(5)})
	fileDoc := mustJSONDoc(t, `{"_id": "x", "big": 1000000000000000000, "small": 5}`)

	if compareDocs(mongoDoc, fileDoc)["match"] != false {
		t.Fatal("premise broken: raw int64 vs float64 comparison was expected to differ")
	}
	normalized, err := normalizeDocs([]map[string]interface{}{mongoDoc})
	if err != nil {
		t.Fatal(err)
	}
	if r := compareDocs(normalized[0], fileDoc); r["match"] != true {
		t.Errorf("normalized docs should match: %v", r["differences"])
	}
}

func TestHandleCompare_JSONSources(t *testing.T) {
	text, isErr := callTool(t, handleCompare, map[string]interface{}{
		"source_a": `{"_id": "1", "status": "active", "qty": 2}`, "source_a_type": "json",
		"source_b": `{"_id": "1", "status": "closed", "qty": 2}`, "source_b_type": "json",
	})
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	m := decodeJSON(t, text)
	if m["match"] != false || m["mismatched"] != float64(1) || m["differences"] == nil {
		t.Errorf("unexpected summary: %v", m)
	}
	if _, ok := m["full_data_path"]; ok {
		t.Error("no connection involved, so nothing should be saved")
	}

	text, isErr = callTool(t, handleCompare, map[string]interface{}{
		"source_a": `[{"_id": "1"}, {"_id": "2"}]`, "source_a_type": "json",
		"source_b": `[{"_id": "2"}, {"_id": "1"}]`, "source_b_type": "json",
		"mode": "collection",
	})
	if isErr || decodeJSON(t, text)["match"] != true {
		t.Errorf("collection mode should match: %s", text)
	}
}

func TestHandleCompare_Errors(t *testing.T) {
	useUnreachableManager(t)

	cases := []struct {
		name    string
		args    map[string]interface{}
		wantErr string
	}{
		{"missing a", map[string]interface{}{"source_b": "{}", "source_b_type": "json"}, "source_a and source_a_type are required"},
		{"missing b", map[string]interface{}{"source_a": "{}", "source_a_type": "json"}, "source_b and source_b_type are required"},
		{"bad type", map[string]interface{}{"source_a": "{}", "source_a_type": "firestore", "source_b": "{}", "source_b_type": "json"}, "unknown source_type"},
		{"bad mode", map[string]interface{}{"source_a": "{}", "source_a_type": "json", "source_b": "{}", "source_b_type": "json", "mode": "x"}, "invalid mode"},
		{"missing file", map[string]interface{}{"source_a": filepath.Join(t.TempDir(), "nope.json"), "source_a_type": "file", "source_b": "{}", "source_b_type": "json"}, "failed to read file"},
		{"mongo side unreachable", map[string]interface{}{"source_a": "orders/1", "source_a_type": "mongo", "source_b": "{}", "source_b_type": "json"}, `connection_a error: connection "DEV" is unreachable`},
		{"empty collection in document mode", map[string]interface{}{"source_a": "[]", "source_a_type": "json", "source_b": "{}", "source_b_type": "json"}, "source_a resolved to empty collection"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := callTool(t, handleCompare, c.args)
			if !isErr || !strings.Contains(text, c.wantErr) {
				t.Errorf("want error %q, got (isErr=%v) %q", c.wantErr, isErr, text)
			}
		})
	}
}
