//go:build integration

package main

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

const (
	itHexA = "650000000000000000000001"
	itHexB = "650000000000000000000002"
)

// setupIntegration connects to the MongoDB named by MONGODB_TEST_* env vars,
// installs connManager with connection "IT", and seeds a temporary collection
// that is dropped when the test ends. Seeding is the only write, in test code.
func setupIntegration(t *testing.T) (collection string, cfg ConnectionConfig) {
	t.Helper()
	host := os.Getenv("MONGODB_TEST_HOST")
	if host == "" {
		t.Skip("MONGODB_TEST_HOST not set; skipping integration test")
	}
	port, _ := strconv.Atoi(os.Getenv("MONGODB_TEST_PORT"))
	cfg = applyDefaults(ConnectionConfig{
		Host:             host,
		Port:             port,
		Username:         os.Getenv("MONGODB_TEST_USER"),
		Password:         os.Getenv("MONGODB_TEST_PASSWORD"),
		Database:         os.Getenv("MONGODB_TEST_DB"),
		AuthSource:       os.Getenv("MONGODB_TEST_AUTH_SOURCE"),
		DirectConnection: true,
		ExportPath:       t.TempDir(),
		TimeoutSeconds:   15,
	}, t.TempDir())
	if cfg.Database == "" {
		t.Fatal("MONGODB_TEST_DB is required")
	}

	cm := &ConnectionManager{}
	if err := cm.Init(map[string]ConnectionConfig{"IT": cfg}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	prev := connManager
	connManager = cm
	t.Cleanup(func() { connManager = prev })

	opts, err := buildClientOptions(cfg)
	if err != nil {
		t.Fatal(err)
	}
	seedClient, err := mongo.Connect(opts)
	if err != nil {
		t.Fatal(err)
	}
	collection = fmt.Sprintf("_mcp_test_%d", time.Now().UnixNano())
	coll := seedClient.Database(cfg.Database).Collection(collection)

	oidA, _ := bson.ObjectIDFromHex(itHexA)
	oidB, _ := bson.ObjectIDFromHex(itHexB)
	ctx := context.Background()
	_, err = coll.InsertMany(ctx, []interface{}{
		bson.D{{Key: "_id", Value: oidA}, {Key: "name", Value: "alpha"}, {Key: "n", Value: int32(1)}, {Key: "tags", Value: bson.A{"x"}}},
		bson.D{{Key: "_id", Value: oidB}, {Key: "name", Value: "beta"}, {Key: "n", Value: int32(2)}},
		bson.D{{Key: "_id", Value: "str-id-1"}, {Key: "name", Value: "gamma"}, {Key: "n", Value: int32(3)}},
	})
	if err != nil {
		t.Fatalf("seeding (the test user needs readWrite on %s): %v", cfg.Database, err)
	}
	t.Cleanup(func() {
		_ = coll.Drop(context.Background())
		_ = seedClient.Disconnect(context.Background())
	})
	return collection, cfg
}

func itCall(t *testing.T, h toolHandler, args map[string]interface{}) map[string]interface{} {
	t.Helper()
	text, isErr := callTool(t, h, args)
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	return decodeJSON(t, text)
}

func TestIntegration_ReadTools(t *testing.T) {
	coll, cfg := setupIntegration(t)

	t.Run("get_document objectid", func(t *testing.T) {
		m := itCall(t, handleGetDocument, map[string]interface{}{"collection": coll, "id": itHexA})
		if m["_id"] != itHexA || m["name"] != "alpha" {
			t.Errorf("got %v", m)
		}
	})
	t.Run("get_document string id", func(t *testing.T) {
		m := itCall(t, handleGetDocument, map[string]interface{}{"collection": coll, "id": "str-id-1"})
		if m["name"] != "gamma" {
			t.Errorf("got %v", m)
		}
	})
	t.Run("get_document not found", func(t *testing.T) {
		text, isErr := callTool(t, handleGetDocument, map[string]interface{}{"collection": coll, "id": "missing"})
		if !isErr || !strings.HasPrefix(text, "document not found") {
			t.Errorf("got (isErr=%v) %q", isErr, text)
		}
	})
	t.Run("get_collection", func(t *testing.T) {
		m := itCall(t, handleGetCollection, map[string]interface{}{"collection": coll})
		if m["count"] != float64(3) || m["database"] != cfg.Database {
			t.Errorf("got %v", m)
		}
	})
	t.Run("query filter sort projection limit", func(t *testing.T) {
		m := itCall(t, handleQuery, map[string]interface{}{
			"collection": coll, "filter": `{"n": {"$gte": 2}}`, "sort": `{"n": -1}`,
			"projection": `{"name": 1}`, "limit": float64(1),
		})
		docs := m["documents"].([]interface{})
		if len(docs) != 1 {
			t.Fatalf("want 1 doc, got %v", docs)
		}
		d := docs[0].(map[string]interface{})
		if d["name"] != "gamma" || d["n"] != nil {
			t.Errorf("want gamma with only name projected, got %v", d)
		}
	})
	t.Run("query by $oid", func(t *testing.T) {
		m := itCall(t, handleQuery, map[string]interface{}{"collection": coll, "filter": `{"_id": {"$oid": "` + itHexB + `"}}`})
		if m["count"] != float64(1) {
			t.Errorf("got %v", m)
		}
	})
	t.Run("count filtered", func(t *testing.T) {
		m := itCall(t, handleCount, map[string]interface{}{"collection": coll, "filter": `{"n": {"$gte": 2}}`})
		if m["count"] != float64(2) || m["estimated"] != false {
			t.Errorf("got %v", m)
		}
	})
	t.Run("count estimated", func(t *testing.T) {
		m := itCall(t, handleCount, map[string]interface{}{"collection": coll, "estimated": true, "filter": `{"n": 1}`})
		if m["count"] != float64(3) || m["estimated"] != true || m["note"] == nil {
			t.Errorf("got %v", m)
		}
	})
	t.Run("aggregate", func(t *testing.T) {
		m := itCall(t, handleAggregate, map[string]interface{}{
			"collection": coll, "pipeline": `[{"$group": {"_id": null, "total": {"$sum": "$n"}}}]`,
		})
		docs := m["documents"].([]interface{})
		if len(docs) != 1 || docs[0].(map[string]interface{})["total"] != float64(6) {
			t.Errorf("got %v", m)
		}
	})
	t.Run("aggregate rejects $out", func(t *testing.T) {
		text, isErr := callTool(t, handleAggregate, map[string]interface{}{"collection": coll, "pipeline": `[{"$out": "x"}]`})
		if !isErr || !strings.Contains(text, "read-only") {
			t.Errorf("got (isErr=%v) %q", isErr, text)
		}
	})
	t.Run("list_collections", func(t *testing.T) {
		m := itCall(t, handleListCollections, map[string]interface{}{"name_filter": "^" + coll + "$"})
		cols := m["collections"].([]interface{})
		if len(cols) != 1 || cols[0].(map[string]interface{})["name"] != coll {
			t.Errorf("got %v", m)
		}
	})
}

func TestIntegration_ExportAndCompare(t *testing.T) {
	coll, _ := setupIntegration(t)

	t.Run("export single collection", func(t *testing.T) {
		m := itCall(t, handleExportJSON, map[string]interface{}{"collection": coll})
		entry := m["exports"].([]interface{})[0].(map[string]interface{})
		if entry["count"] != float64(3) {
			t.Errorf("got %v", m)
		}
		if _, err := os.Stat(entry["file"].(string)); err != nil {
			t.Errorf("export file missing: %v", err)
		}
	})
	t.Run("export batch with one bad item", func(t *testing.T) {
		m := itCall(t, handleExportJSON, map[string]interface{}{"documents": []interface{}{
			map[string]interface{}{"collection": coll, "id": itHexA},
			map[string]interface{}{"collection": coll, "id": "str-id-1"},
			map[string]interface{}{"collection": coll, "id": "missing"},
		}})
		exports := m["exports"].([]interface{})
		if m["total_docs"] != float64(2) || len(exports) != 3 || exports[2].(map[string]interface{})["error"] == nil {
			t.Errorf("got %v", m)
		}
	})
	t.Run("compare mongo vs json", func(t *testing.T) {
		m := itCall(t, handleCompare, map[string]interface{}{
			"source_a": coll + "/" + itHexB, "source_a_type": "mongo",
			"source_b": `{"_id": "` + itHexB + `", "name": "beta", "n": 2}`, "source_b_type": "json",
		})
		if m["match"] != true {
			t.Errorf("want match, got %v", m)
		}
	})
}
