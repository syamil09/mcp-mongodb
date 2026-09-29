package main

import (
	"testing"
)

func TestFindWriteStage(t *testing.T) {
	cases := []struct {
		name      string
		pipeline  string
		wantStage string
	}{
		{"top-level $out", `[{"$match": {}}, {"$out": "copy"}]`, "$out"},
		{"top-level $merge", `[{"$merge": {"into": "copy"}}]`, "$merge"},
		{"nested in $facet", `[{"$facet": {"a": [{"$match": {}}], "b": [{"$merge": {"into": "x"}}]}}]`, "$merge"},
		{"nested in $lookup.pipeline", `[{"$lookup": {"from": "o", "as": "j", "pipeline": [{"$out": "x"}]}}]`, "$out"},
		{"nested in $unionWith.pipeline", `[{"$unionWith": {"coll": "o", "pipeline": [{"$out": "x"}]}}]`, "$out"},
		{"deeply nested", `[{"$facet": {"a": [{"$lookup": {"from": "o", "as": "j", "pipeline": [{"$unionWith": {"coll": "p", "pipeline": [{"$merge": "x"}]}}]}}]}}]`, "$merge"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p, err := parseExtJSONArray(c.pipeline, "pipeline")
			if err != nil {
				t.Fatal(err)
			}
			stage, found := findWriteStage(p)
			if !found || stage != c.wantStage {
				t.Errorf("findWriteStage = %q, %v; want %q, true", stage, found, c.wantStage)
			}
			err = checkPipelineReadOnly(p)
			want := `pipeline contains write stage "` + c.wantStage + `" — this server is read-only`
			if err == nil || err.Error() != want {
				t.Errorf("checkPipelineReadOnly error = %v, want %q", err, want)
			}
		})
	}
}

func TestFindWriteStage_CleanPipelines(t *testing.T) {
	clean := []string{
		`[]`,
		`[{"$match": {"status": "active"}}, {"$group": {"_id": "$type", "n": {"$sum": 1}}}]`,
		`[{"$unionWith": "other_collection"}]`,
		`[{"$lookup": {"from": "o", "localField": "a", "foreignField": "b", "as": "j"}}]`,
		`[{"$match": {"note": "$out"}}]`,
		`[{"$facet": {"a": [{"$count": "n"}]}}]`,
	}
	for _, src := range clean {
		p, err := parseExtJSONArray(src, "pipeline")
		if err != nil {
			t.Fatalf("%s: %v", src, err)
		}
		if stage, found := findWriteStage(p); found {
			t.Errorf("%s: unexpectedly found write stage %q", src, stage)
		}
		if err := checkPipelineReadOnly(p); err != nil {
			t.Errorf("%s: unexpected error %v", src, err)
		}
	}
}

func TestFindWriteStage_PlainGoValues(t *testing.T) {
	p := []interface{}{map[string]interface{}{"$facet": map[string]interface{}{"x": []interface{}{map[string]interface{}{"$out": "y"}}}}}
	if stage, found := findWriteStage(p); !found || stage != "$out" {
		t.Errorf("got %q, %v", stage, found)
	}
}
