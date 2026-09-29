package main

import (
	"os"
	"testing"
)

// The shipped example must stay a valid config, and must not carry a real password.
func TestExampleConfigParses(t *testing.T) {
	data, err := os.ReadFile(".mcp-mongodb-config.json.example")
	if err != nil {
		t.Fatal(err)
	}
	configs, err := parseConfig(data, t.TempDir())
	if err != nil {
		t.Fatalf("example config does not parse: %v", err)
	}
	for name, cfg := range configs {
		if cfg.Password != "CHANGE_ME" {
			t.Errorf("connection %s: example password must be the CHANGE_ME placeholder", name)
		}
		if _, err := buildClientOptions(cfg); err != nil {
			t.Errorf("connection %s: invalid client options: %v", name, err)
		}
	}
}
