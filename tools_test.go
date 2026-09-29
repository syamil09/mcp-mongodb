package main

import (
	"reflect"
	"sort"
	"testing"
)

func TestToolDefinitions(t *testing.T) {
	wantRequired := map[string][]string{
		"mongo_get_document":     {"collection", "id"},
		"mongo_get_collection":   {"collection"},
		"mongo_query":            {"collection"},
		"mongo_count":            {"collection"},
		"mongo_aggregate":        {"collection", "pipeline"},
		"mongo_list_collections": nil,
		"mongo_list_databases":   nil,
		"mongo_export_json":      nil,
		"mongo_compare":          {"source_a", "source_a_type", "source_b", "source_b_type"},
	}

	defs := toolDefinitions()
	if len(defs) != len(wantRequired) {
		t.Fatalf("got %d tools, want %d", len(defs), len(wantRequired))
	}
	for _, d := range defs {
		want, ok := wantRequired[d.Tool.Name]
		if !ok {
			t.Errorf("unexpected tool %q", d.Tool.Name)
			continue
		}
		if d.Handler == nil {
			t.Errorf("%s has no handler", d.Tool.Name)
		}
		got := append([]string(nil), d.Tool.InputSchema.Required...)
		sort.Strings(got)
		sort.Strings(want)
		bothEmpty := len(got) == 0 && len(want) == 0
		if !bothEmpty && !reflect.DeepEqual(got, want) {
			t.Errorf("%s required = %v, want %v", d.Tool.Name, got, want)
		}
		if d.Tool.Name != "mongo_compare" {
			if _, ok := d.Tool.InputSchema.Properties["connection"]; !ok {
				t.Errorf("%s is missing the connection argument", d.Tool.Name)
			}
		}
	}
}
