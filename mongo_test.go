package main

import (
	"context"
	"strings"
	"testing"

	mcp "github.com/mark3labs/mcp-go/mcp"
)

type toolHandler func(context.Context, mcp.CallToolRequest) (*mcp.CallToolResult, error)

// callTool invokes a handler with args and returns its text and error flag.
func callTool(t *testing.T, h toolHandler, args map[string]interface{}) (string, bool) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Arguments = args
	res, err := h(context.Background(), req)
	if err != nil {
		t.Fatalf("handler returned Go error (must use tool errors): %v", err)
	}
	return resultText(t, res)
}

func TestClampLimit(t *testing.T) {
	cases := []struct {
		in   interface{}
		want int64
	}{
		{nil, 500}, {float64(0), 500}, {float64(-3), 500}, {float64(10), 10},
		{float64(500), 500}, {float64(501), 500}, {float64(2.7), 2}, {"10", 500},
	}
	for _, c := range cases {
		if got := clampLimit(c.in, 500); got != c.want {
			t.Errorf("clampLimit(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestDatabaseName(t *testing.T) {
	conn := &Connection{Config: ConnectionConfig{Database: "cfgdb"}}
	if got := databaseName(conn, map[string]interface{}{}); got != "cfgdb" {
		t.Errorf("default = %q", got)
	}
	if got := databaseName(conn, map[string]interface{}{"database": " other "}); got != "other" {
		t.Errorf("override = %q", got)
	}
}

// Every case here must fail on argument validation, before the (unreachable)
// connection is touched — so the error must NOT be the unreachable error.
func TestHandlers_ValidateArgumentsBeforeConnecting(t *testing.T) {
	useUnreachableManager(t)

	cases := []struct {
		name    string
		handler toolHandler
		args    map[string]interface{}
		wantErr string
	}{
		{"get_document no collection", handleGetDocument, map[string]interface{}{"id": "x"}, "collection is required"},
		{"get_document no id", handleGetDocument, map[string]interface{}{"collection": "c"}, "id is required"},
		{"get_document bad id_type", handleGetDocument, map[string]interface{}{"collection": "c", "id": "x", "id_type": "uuid"}, "invalid id_type"},
		{"get_document bad objectid", handleGetDocument, map[string]interface{}{"collection": "c", "id": "x", "id_type": "objectid"}, "not a valid ObjectID"},
		{"get_collection no collection", handleGetCollection, map[string]interface{}{}, "collection is required"},
		{"query no collection", handleQuery, map[string]interface{}{"filter": "{}"}, "collection is required"},
		{"query bad filter", handleQuery, map[string]interface{}{"collection": "c", "filter": "{bad"}, "filter is not valid Extended JSON"},
		{"query bad projection", handleQuery, map[string]interface{}{"collection": "c", "projection": "{bad"}, "projection is not valid Extended JSON"},
		{"query bad sort", handleQuery, map[string]interface{}{"collection": "c", "sort": "[1]"}, "sort is not valid Extended JSON"},
		{"count no collection", handleCount, map[string]interface{}{}, "collection is required"},
		{"count bad filter", handleCount, map[string]interface{}{"collection": "c", "filter": "{bad"}, "filter is not valid Extended JSON"},
		{"aggregate no pipeline", handleAggregate, map[string]interface{}{"collection": "c"}, "pipeline is required"},
		{"aggregate pipeline not array", handleAggregate, map[string]interface{}{"collection": "c", "pipeline": `{"$match": {}}`}, "pipeline must be a JSON array of stages"},
		{"aggregate $out", handleAggregate, map[string]interface{}{"collection": "c", "pipeline": `[{"$match": {}}, {"$out": "copy"}]`}, `pipeline contains write stage "$out" — this server is read-only`},
		{"aggregate nested $merge as array arg", handleAggregate, map[string]interface{}{"collection": "c", "pipeline": []interface{}{
			map[string]interface{}{"$facet": map[string]interface{}{"a": []interface{}{map[string]interface{}{"$merge": "x"}}}},
		}}, `pipeline contains write stage "$merge"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := callTool(t, c.handler, c.args)
			if !isErr || !strings.Contains(text, c.wantErr) {
				t.Errorf("want tool error containing %q, got (isErr=%v) %q", c.wantErr, isErr, text)
			}
			if strings.Contains(text, "unreachable") {
				t.Errorf("validation must happen before connecting, got %q", text)
			}
		})
	}
}

func TestHandlers_ConnectionErrorsAreToolErrorsWithoutPassword(t *testing.T) {
	useUnreachableManager(t)

	cases := []struct {
		name    string
		handler toolHandler
		args    map[string]interface{}
	}{
		{"list_databases", handleListDatabases, map[string]interface{}{}},
		{"aggregate clean pipeline", handleAggregate, map[string]interface{}{"collection": "c", "pipeline": `[{"$match": {}}]`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			text, isErr := callTool(t, c.handler, c.args)
			if !isErr || !strings.Contains(text, `connection "DEV" is unreachable`) {
				t.Errorf("want unreachable tool error, got (isErr=%v) %q", isErr, text)
			}
			if strings.Contains(text, testPassword) {
				t.Errorf("tool error leaks password: %q", text)
			}
		})
	}

	text, isErr := callTool(t, handleCount, map[string]interface{}{"collection": "c", "connection": "NOPE"})
	if !isErr || !strings.Contains(text, `connection "NOPE" not found. Available: [DEV]`) {
		t.Errorf("unknown connection: got (isErr=%v) %q", isErr, text)
	}
}
