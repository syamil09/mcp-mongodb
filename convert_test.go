package main

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

const testHex = "650000000000000000000001"

func TestSanitizeValue(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex(testHex)
	when := time.Date(2024, 3, 4, 5, 6, 7, 0, time.UTC)
	dec, _ := bson.ParseDecimal128("12.50")

	cases := []struct {
		name string
		in   interface{}
		want interface{}
	}{
		{"nil", nil, nil},
		{"objectid", oid, testHex},
		{"datetime", bson.NewDateTimeFromTime(when), "2024-03-04T05:06:07Z"},
		{"time non-utc", when.In(time.FixedZone("WIB", 7*3600)), "2024-03-04T05:06:07Z"},
		{"decimal128", dec, "12.50"},
		{"binary", bson.Binary{Subtype: 0, Data: []byte("hi")}, "aGk="},
		{"timestamp", bson.Timestamp{T: 10, I: 2}, map[string]interface{}{"t": uint32(10), "i": uint32(2)}},
		{"regex", bson.Regex{Pattern: "^ab", Options: "i"}, "/^ab/i"},
		{"int32", int32(7), int32(7)},
		{"int64", int64(9007199254740993), int64(9007199254740993)},
		{"float64", 1.5, 1.5},
		{"string", "s", "s"},
		{"bool", true, true},
		{"NaN", math.NaN(), "NaN"},
		{"+Inf", math.Inf(1), "+Inf"},
		{"-Inf", math.Inf(-1), "-Inf"},
		{"bson.D", bson.D{{Key: "a", Value: oid}}, map[string]interface{}{"a": testHex}},
		{"bson.M", bson.M{"a": int32(1)}, map[string]interface{}{"a": int32(1)}},
		{"bson.A nested", bson.A{bson.D{{Key: "x", Value: oid}}}, []interface{}{map[string]interface{}{"x": testHex}}},
		{"other type", bson.JavaScript("function(){}"), "function(){}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := sanitizeValue(c.in)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("sanitizeValue(%#v) = %#v, want %#v", c.in, got, c.want)
			}
		})
	}
}

func TestDocToMap_IsJSONSerializable(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex(testHex)
	raw := bson.M{
		"_id":   oid,
		"score": math.NaN(),
		"inner": bson.D{{Key: "when", Value: bson.NewDateTimeFromTime(time.Unix(0, 0))}},
	}
	doc := docToMap(raw)
	if doc["_id"] != testHex {
		t.Errorf("_id = %v, want hex string", doc["_id"])
	}
	if _, err := json.Marshal(doc); err != nil {
		t.Fatalf("sanitized doc must marshal to JSON: %v", err)
	}
}

func TestParseExtJSONDoc(t *testing.T) {
	t.Run("string with $oid and $date", func(t *testing.T) {
		d, err := parseExtJSONDoc(`{"_id": {"$oid": "`+testHex+`"}, "at": {"$date": "2024-01-01T00:00:00Z"}}`, "filter")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := d[0].Value.(bson.ObjectID); !ok {
			t.Errorf("_id decoded as %T, want bson.ObjectID", d[0].Value)
		}
		if _, ok := d[1].Value.(bson.DateTime); !ok {
			t.Errorf("at decoded as %T, want bson.DateTime", d[1].Value)
		}
	})
	t.Run("object input with $oid", func(t *testing.T) {
		arg := map[string]interface{}{"_id": map[string]interface{}{"$oid": testHex}}
		d, err := parseExtJSONDoc(arg, "filter")
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := d[0].Value.(bson.ObjectID); !ok {
			t.Errorf("_id decoded as %T, want bson.ObjectID", d[0].Value)
		}
	})
	t.Run("string preserves key order", func(t *testing.T) {
		d, err := parseExtJSONDoc(`{"b": 1, "a": -1}`, "sort")
		if err != nil {
			t.Fatal(err)
		}
		if len(d) != 2 || d[0].Key != "b" || d[1].Key != "a" {
			t.Errorf("key order lost: %v", d)
		}
	})
	t.Run("absent or blank means empty", func(t *testing.T) {
		for _, arg := range []interface{}{nil, "", "   ", "null", "{}", map[string]interface{}{}} {
			d, err := parseExtJSONDoc(arg, "filter")
			if err != nil {
				t.Errorf("arg %#v: unexpected error %v", arg, err)
			}
			if d == nil || len(d) != 0 {
				t.Errorf("arg %#v: want empty non-nil bson.D, got %#v", arg, d)
			}
		}
	})
	t.Run("invalid", func(t *testing.T) {
		for _, arg := range []interface{}{`{bad`, `[1, 2]`} {
			_, err := parseExtJSONDoc(arg, "filter")
			if err == nil || !strings.Contains(err.Error(), "filter is not valid Extended JSON") {
				t.Errorf("arg %#v: want 'filter is not valid Extended JSON' error, got %v", arg, err)
			}
		}
	})
}

func TestParseExtJSONArray(t *testing.T) {
	t.Run("string pipeline", func(t *testing.T) {
		p, err := parseExtJSONArray(`[{"$match": {"_id": {"$oid": "`+testHex+`"}}}, {"$limit": 1}]`, "pipeline")
		if err != nil {
			t.Fatal(err)
		}
		if len(p) != 2 {
			t.Fatalf("want 2 stages, got %d", len(p))
		}
		stage, ok := p[0].(bson.D)
		if !ok || stage[0].Key != "$match" {
			t.Fatalf("first stage = %#v", p[0])
		}
	})
	t.Run("array input", func(t *testing.T) {
		arg := []interface{}{map[string]interface{}{"$limit": 5}}
		p, err := parseExtJSONArray(arg, "pipeline")
		if err != nil || len(p) != 1 {
			t.Fatalf("got %v, %v", p, err)
		}
	})
	t.Run("empty array", func(t *testing.T) {
		p, err := parseExtJSONArray(`[]`, "pipeline")
		if err != nil || p == nil || len(p) != 0 {
			t.Fatalf("got %#v, %v", p, err)
		}
	})
	t.Run("errors", func(t *testing.T) {
		cases := []struct {
			arg     interface{}
			wantErr string
		}{
			{nil, "pipeline is required"},
			{"  ", "pipeline is required"},
			{`{"$match": {}}`, "pipeline must be a JSON array of stages"},
			{`[{"$match": ]`, "pipeline is not valid Extended JSON"},
		}
		for _, c := range cases {
			_, err := parseExtJSONArray(c.arg, "pipeline")
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("arg %#v: want %q, got %v", c.arg, c.wantErr, err)
			}
		}
	})
}

func TestParseID(t *testing.T) {
	oid, _ := bson.ObjectIDFromHex(testHex)
	cases := []struct {
		id, idType string
		want       interface{}
		wantErr    string
	}{
		{testHex, "", oid, ""},
		{testHex, "auto", oid, ""},
		{"not-an-objectid", "auto", "not-an-objectid", ""},
		{"zzzzzzzzzzzzzzzzzzzzzzzz", "auto", "zzzzzzzzzzzzzzzzzzzzzzzz", ""},
		{testHex, "string", testHex, ""},
		{testHex, "objectid", oid, ""},
		{"abc", "objectid", nil, "not a valid ObjectID"},
		{"42", "int", int64(42), ""},
		{"4x", "int", nil, "not a valid integer"},
		{"1", "uuid", nil, "invalid id_type"},
	}
	for _, c := range cases {
		got, err := parseID(c.id, c.idType)
		if c.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("parseID(%q, %q): want error %q, got %v", c.id, c.idType, c.wantErr, err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseID(%q, %q) = %#v, %v; want %#v", c.id, c.idType, got, err, c.want)
		}
	}
}
