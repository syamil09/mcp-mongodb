package main

import (
	"fmt"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// writeStages are aggregation stages that write data. Any occurrence is refused.
var writeStages = map[string]bool{"$out": true, "$merge": true}

// findWriteStage walks a parsed pipeline recursively — every document and array
// at any depth, which covers $facet, $lookup.pipeline and $unionWith.pipeline —
// and reports the first write stage key it finds.
func findWriteStage(v interface{}) (stage string, found bool) {
	switch val := v.(type) {
	case bson.D:
		for _, e := range val {
			if writeStages[e.Key] {
				return e.Key, true
			}
			if s, ok := findWriteStage(e.Value); ok {
				return s, true
			}
		}
	case bson.M:
		return findWriteStageInMap(val)
	case map[string]interface{}:
		return findWriteStageInMap(val)
	case bson.A:
		return findWriteStageInSlice(val)
	case []interface{}:
		return findWriteStageInSlice(val)
	}
	return "", false
}

func findWriteStageInMap(m map[string]interface{}) (string, bool) {
	for k, v := range m {
		if writeStages[k] {
			return k, true
		}
		if s, ok := findWriteStage(v); ok {
			return s, true
		}
	}
	return "", false
}

func findWriteStageInSlice(items []interface{}) (string, bool) {
	for _, item := range items {
		if s, ok := findWriteStage(item); ok {
			return s, true
		}
	}
	return "", false
}

// checkPipelineReadOnly returns an error if the pipeline contains a write stage.
func checkPipelineReadOnly(pipeline bson.A) error {
	if stage, found := findWriteStage(pipeline); found {
		return fmt.Errorf("pipeline contains write stage %q — this server is read-only", stage)
	}
	return nil
}
