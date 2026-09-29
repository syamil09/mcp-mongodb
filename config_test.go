package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPassword = "SuperSecret-DoNotLeak-123!"

func TestParseConfig_MultiConnection(t *testing.T) {
	dir := t.TempDir()
	data := []byte(`{"connections": {
		"DEV":  {"host": "127.0.0.1", "database": "devdb"},
		"PROD": {"host": "prod.example", "port": 28000, "database": "proddb"}
	}}`)

	configs, err := parseConfig(data, dir)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if len(configs) != 2 {
		t.Fatalf("want 2 connections, got %d", len(configs))
	}
	if configs["PROD"].Port != 28000 || configs["PROD"].Database != "proddb" {
		t.Errorf("PROD not parsed correctly: %+v", configs["PROD"])
	}
}

func TestParseConfig_SingleFormatNamedDefault(t *testing.T) {
	configs, err := parseConfig([]byte(`{"host": "localhost", "database": "app"}`), t.TempDir())
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	cfg, ok := configs["default"]
	if !ok || len(configs) != 1 {
		t.Fatalf("want single connection named default, got %v", configs)
	}
	if cfg.Host != "localhost" {
		t.Errorf("host = %q", cfg.Host)
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	dir := t.TempDir()
	configs, err := parseConfig([]byte(`{"connections": {"DEV": {"host": "h", "database": "appdb"}}}`), dir)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	cfg := configs["DEV"]
	checks := []struct {
		name      string
		got, want interface{}
	}{
		{"port", cfg.Port, 27017},
		{"auth_source", cfg.AuthSource, "appdb"},
		{"export_path", cfg.ExportPath, dir},
		{"max_documents", cfg.MaxDocuments, 500},
		{"max_response_chars", cfg.MaxResponseChars, 50000},
		{"doc_preview_chars", cfg.DocPreviewChars, 2000},
		{"timeout_seconds", cfg.TimeoutSeconds, 30},
		{"direct_connection", cfg.DirectConnection, false},
		{"tls", cfg.TLS, false},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

func TestParseConfig_ExplicitValuesKept(t *testing.T) {
	data := []byte(`{"connections": {"DEV": {
		"host": "h", "port": 1234, "username": "u", "password": "p", "database": "appdb",
		"auth_source": "admin", "direct_connection": true, "replica_set": "rs0", "tls": true,
		"max_documents": 10, "max_response_chars": 999, "doc_preview_chars": 50, "timeout_seconds": 5
	}}}`)
	configs, err := parseConfig(data, t.TempDir())
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	cfg := configs["DEV"]
	if cfg.Port != 1234 || cfg.AuthSource != "admin" || !cfg.DirectConnection || cfg.ReplicaSet != "rs0" ||
		!cfg.TLS || cfg.MaxDocuments != 10 || cfg.MaxResponseChars != 999 || cfg.DocPreviewChars != 50 ||
		cfg.TimeoutSeconds != 5 || cfg.Username != "u" || cfg.Password != "p" {
		t.Errorf("explicit values not kept: %#v", cfg)
	}
}

func TestParseConfig_ExportPathResolution(t *testing.T) {
	dir := t.TempDir()
	abs := filepath.Join(t.TempDir(), "abs-exports")
	data := []byte(fmt.Sprintf(`{"connections": {
		"REL": {"host": "h", "database": "d", "export_path": "exports/dev"},
		"ABS": {"host": "h", "database": "d", "export_path": %q}
	}}`, abs))
	configs, err := parseConfig(data, dir)
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if got, want := configs["REL"].ExportPath, filepath.Join(dir, "exports", "dev"); got != want {
		t.Errorf("relative export_path = %q, want %q", got, want)
	}
	if got := configs["ABS"].ExportPath; got != abs {
		t.Errorf("absolute export_path = %q, want %q", got, abs)
	}
}

func TestParseConfig_ValidationErrors(t *testing.T) {
	cases := []struct {
		name    string
		data    string
		wantErr string
	}{
		{"invalid json", `{"connections": `, "invalid config JSON"},
		{"no connections", `{"connections": {}}`, "at least one connection is required"},
		{"missing host", `{"connections": {"DEV": {"database": "d", "password": "` + testPassword + `"}}}`, `host is required for connection "DEV"`},
		{"missing database", `{"connections": {"DEV": {"host": "h", "password": "` + testPassword + `"}}}`, `database is required for connection "DEV"`},
		{"uri only is not supported", `{"connections": {"DEV": {"uri": "mongodb://u:` + testPassword + `@h/d"}}}`, `host is required for connection "DEV"`},
		{"single format missing host", `{"database": "d"}`, `host is required for connection "default"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseConfig([]byte(c.data), t.TempDir())
			if err == nil {
				t.Fatalf("want error containing %q, got nil", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error %q does not contain %q", err, c.wantErr)
			}
			if strings.Contains(err.Error(), testPassword) {
				t.Errorf("error leaks password: %q", err)
			}
		})
	}
}

func TestParseConfig_URIKeyIgnored(t *testing.T) {
	data := []byte(`{"connections": {"DEV": {"host": "h", "database": "d", "uri": "mongodb://other/x"}}}`)
	configs, err := parseConfig(data, t.TempDir())
	if err != nil {
		t.Fatalf("parseConfig: %v", err)
	}
	if configs["DEV"].Host != "h" {
		t.Errorf("host changed by uri key: %q", configs["DEV"].Host)
	}
}

func TestConnectionConfig_FormattingRedactsPassword(t *testing.T) {
	cfg := ConnectionConfig{Host: "h", Username: "u", Password: testPassword, Database: "d"}
	for _, verb := range []string{"%v", "%+v", "%#v", "%s"} {
		out := fmt.Sprintf(verb, cfg)
		if strings.Contains(out, testPassword) {
			t.Errorf("%s leaks password: %s", verb, out)
		}
	}
	if out := fmt.Sprintf("%v", &cfg); strings.Contains(out, testPassword) {
		t.Errorf("pointer %%v leaks password: %s", out)
	}
}

func TestReadConfigFile_FirstReadableWins(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.json")
	second := filepath.Join(dir, "second.json")
	if err := os.WriteFile(first, []byte(`{"n":1}`), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte(`{"n":2}`), 0644); err != nil {
		t.Fatal(err)
	}

	data, path, err := readConfigFile([]string{filepath.Join(dir, "missing.json"), first, second})
	if err != nil {
		t.Fatalf("readConfigFile: %v", err)
	}
	if path != first || string(data) != `{"n":1}` {
		t.Errorf("got path %q data %q, want first file", path, data)
	}

	if _, _, err := readConfigFile([]string{filepath.Join(dir, "missing.json")}); err == nil {
		t.Error("want error when no file exists")
	}
}

func TestConfigSearchPaths_EnvVarFirst(t *testing.T) {
	t.Setenv("MCP_MONGODB_CONFIG", `C:\custom\cfg.json`)
	paths := configSearchPaths()
	if len(paths) < 2 || paths[0] != `C:\custom\cfg.json` {
		t.Fatalf("env path must come first, got %v", paths)
	}
	if filepath.Base(paths[1]) != configFileName {
		t.Errorf("second path should be %s next to the executable, got %q", configFileName, paths[1])
	}
}
