package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// sanitizeValue converts BSON-specific types to JSON-safe equivalents.
func sanitizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case nil:
		return nil
	case bson.ObjectID:
		return val.Hex()
	case bson.DateTime:
		return val.Time().UTC().Format(time.RFC3339)
	case time.Time:
		return val.UTC().Format(time.RFC3339)
	case bson.Decimal128:
		return val.String()
	case bson.Binary:
		return base64.StdEncoding.EncodeToString(val.Data)
	case bson.Timestamp:
		return map[string]interface{}{"t": val.T, "i": val.I}
	case bson.Regex:
		return "/" + val.Pattern + "/" + val.Options
	case bson.D:
		out := make(map[string]interface{}, len(val))
		for _, e := range val {
			out[e.Key] = sanitizeValue(e.Value)
		}
		return out
	case bson.M:
		return sanitizeMap(val)
	case map[string]interface{}:
		return sanitizeMap(val)
	case bson.A:
		return sanitizeSlice(val)
	case []interface{}:
		return sanitizeSlice(val)
	case float64:
		// encoding/json cannot represent NaN or ±Inf; emit them as strings.
		if math.IsNaN(val) || math.IsInf(val, 0) {
			return strconv.FormatFloat(val, 'g', -1, 64)
		}
		return val
	case int32, int64, int, string, bool:
		return val
	default:
		return fmt.Sprint(val)
	}
}

func sanitizeMap(m map[string]interface{}) map[string]interface{} {
	out := make(map[string]interface{}, len(m))
	for k, v := range m {
		out[k] = sanitizeValue(v)
	}
	return out
}

func sanitizeSlice(s []interface{}) []interface{} {
	out := make([]interface{}, len(s))
	for i, v := range s {
		out[i] = sanitizeValue(v)
	}
	return out
}

// docToMap converts a decoded MongoDB document to a JSON-safe map. _id is kept.
func docToMap(raw bson.M) map[string]interface{} {
	return sanitizeMap(raw)
}

// extJSONBytes turns a tool argument (a JSON string, or a value the MCP client
// already decoded) into raw JSON bytes. empty is true for nil, blank, or "null".
func extJSONBytes(arg interface{}) (data []byte, empty bool, err error) {
	switch v := arg.(type) {
	case nil:
		return nil, true, nil
	case string:
		s := strings.TrimSpace(v)
		if s == "" || s == "null" {
			return nil, true, nil
		}
		return []byte(s), false, nil
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, false, err
		}
		return b, false, nil
	}
}

// parseExtJSONDoc parses a document argument (filter, projection, sort) as
// MongoDB Extended JSON. An absent or blank argument yields an empty bson.D.
// Key order is preserved when the argument is passed as a string.
func parseExtJSONDoc(arg interface{}, name string) (bson.D, error) {
	data, empty, err := extJSONBytes(arg)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid Extended JSON: %v", name, err)
	}
	if empty {
		return bson.D{}, nil
	}
	var doc bson.D
	if err := bson.UnmarshalExtJSON(data, false, &doc); err != nil {
		return nil, fmt.Errorf("%s is not valid Extended JSON: %v", name, err)
	}
	return doc, nil
}

// parseExtJSONArray parses an array argument (an aggregation pipeline) as
// MongoDB Extended JSON. Nested documents decode as bson.D.
func parseExtJSONArray(arg interface{}, name string) (bson.A, error) {
	data, empty, err := extJSONBytes(arg)
	if err != nil {
		return nil, fmt.Errorf("%s is not valid Extended JSON: %v", name, err)
	}
	if empty {
		return nil, fmt.Errorf("%s is required", name)
	}
	if data[0] != '[' {
		return nil, fmt.Errorf("%s must be a JSON array of stages", name)
	}
	// Extended JSON parsing needs a top-level document, so wrap the array.
	wrapped := append(append([]byte(`{"v":`), data...), '}')
	var holder struct {
		V bson.A `bson:"v"`
	}
	if err := bson.UnmarshalExtJSON(wrapped, false, &holder); err != nil {
		return nil, fmt.Errorf("%s is not valid Extended JSON: %v", name, err)
	}
	if holder.V == nil {
		return bson.A{}, nil
	}
	return holder.V, nil
}

// parseID converts a string id into the value stored in _id.
// idType: "auto" (24-hex → ObjectID, else string), "objectid", "string", "int".
func parseID(id string, idType string) (interface{}, error) {
	switch idType {
	case "", "auto":
		if len(id) == 24 {
			if oid, err := bson.ObjectIDFromHex(id); err == nil {
				return oid, nil
			}
		}
		return id, nil
	case "objectid":
		oid, err := bson.ObjectIDFromHex(id)
		if err != nil {
			return nil, fmt.Errorf("id %q is not a valid ObjectID", id)
		}
		return oid, nil
	case "string":
		return id, nil
	case "int":
		n, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
		if err != nil {
			return nil, fmt.Errorf("id %q is not a valid integer", id)
		}
		return n, nil
	default:
		return nil, fmt.Errorf("invalid id_type %q: must be auto, objectid, string, or int", idType)
	}
}
