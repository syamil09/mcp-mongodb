package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const configFileName = ".mcp-mongodb-config.json"

// ConnectionConfig holds the settings for one named MongoDB connection.
type ConnectionConfig struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	Username         string `json:"username"`
	Password         string `json:"password"`
	Database         string `json:"database"`
	AuthSource       string `json:"auth_source"`
	DirectConnection bool   `json:"direct_connection"`
	ReplicaSet       string `json:"replica_set"`
	TLS              bool   `json:"tls"`
	ExportPath       string `json:"export_path"`
	MaxDocuments     int    `json:"max_documents"`
	MaxResponseChars int    `json:"max_response_chars"`
	DocPreviewChars  int    `json:"doc_preview_chars"`
	TimeoutSeconds   int    `json:"timeout_seconds"`
}

// String redacts the password so a config printed with %v or %+v never leaks it.
func (c ConnectionConfig) String() string {
	pw := ""
	if c.Password != "" {
		pw = "***"
	}
	return fmt.Sprintf("{host:%s port:%d username:%s password:%s database:%s auth_source:%s}",
		c.Host, c.Port, c.Username, pw, c.Database, c.AuthSource)
}

// GoString redacts the password for %#v.
func (c ConnectionConfig) GoString() string {
	return "ConnectionConfig" + c.String()
}

// Timeout returns TimeoutSeconds as a duration.
func (c ConnectionConfig) Timeout() time.Duration {
	return time.Duration(c.TimeoutSeconds) * time.Second
}

type ConfigFile struct {
	Connections map[string]ConnectionConfig `json:"connections"`
}

// configSearchPaths returns candidate config locations in priority order:
// MCP_MONGODB_CONFIG, executable directory, current working directory.
func configSearchPaths() []string {
	paths := []string{}
	if envPath := os.Getenv("MCP_MONGODB_CONFIG"); envPath != "" {
		paths = append(paths, envPath)
	}
	if exe, err := os.Executable(); err == nil {
		paths = append(paths, filepath.Join(filepath.Dir(exe), configFileName))
	}
	if cwd, err := os.Getwd(); err == nil {
		paths = append(paths, filepath.Join(cwd, configFileName))
	}
	return paths
}

// readConfigFile returns the contents and path of the first readable file in paths.
func readConfigFile(paths []string) ([]byte, string, error) {
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err == nil {
			return data, p, nil
		}
	}
	return nil, "", fmt.Errorf("config file not found in any search path: %v", paths)
}

// parseConfig decodes config JSON (multi-connection or legacy single format),
// validates each connection, and applies defaults. configDir anchors relative paths.
func parseConfig(data []byte, configDir string) (map[string]ConnectionConfig, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("invalid config JSON: %w", err)
	}

	var configs map[string]ConnectionConfig
	if _, ok := raw["connections"]; ok {
		var multi ConfigFile
		if err := json.Unmarshal(data, &multi); err != nil {
			return nil, fmt.Errorf("invalid multi-connection config: %w", err)
		}
		configs = multi.Connections
	} else {
		var single ConnectionConfig
		if err := json.Unmarshal(data, &single); err != nil {
			return nil, fmt.Errorf("invalid config JSON: %w", err)
		}
		configs = map[string]ConnectionConfig{"default": single}
	}

	if len(configs) == 0 {
		return nil, fmt.Errorf("at least one connection is required in config")
	}

	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		cfg := configs[name]
		if strings.TrimSpace(cfg.Host) == "" {
			return nil, fmt.Errorf("host is required for connection %q", name)
		}
		if strings.TrimSpace(cfg.Database) == "" {
			return nil, fmt.Errorf("database is required for connection %q", name)
		}
		configs[name] = applyDefaults(cfg, configDir)
	}
	return configs, nil
}

// applyDefaults fills zero-valued fields with their defaults and resolves a
// relative export_path against configDir.
func applyDefaults(cfg ConnectionConfig, configDir string) ConnectionConfig {
	if cfg.Port <= 0 {
		cfg.Port = 27017
	}
	if cfg.AuthSource == "" {
		cfg.AuthSource = cfg.Database
	}
	if cfg.ExportPath == "" {
		cfg.ExportPath = "."
	}
	if !filepath.IsAbs(cfg.ExportPath) {
		cfg.ExportPath = filepath.Join(configDir, cfg.ExportPath)
	}
	if cfg.MaxDocuments <= 0 {
		cfg.MaxDocuments = 500
	}
	if cfg.MaxResponseChars <= 0 {
		cfg.MaxResponseChars = 50000
	}
	if cfg.DocPreviewChars <= 0 {
		cfg.DocPreviewChars = 2000
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 30
	}
	return cfg
}
