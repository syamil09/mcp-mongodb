package main

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/mongo/readpref"
)

// unreachableConfig returns a fully-defaulted config pointing at a closed port.
// Connect succeeds (the driver dials lazily); any ping fails after ~1s.
func unreachableConfig(t *testing.T) ConnectionConfig {
	t.Helper()
	return applyDefaults(ConnectionConfig{
		Host:           "127.0.0.1",
		Port:           1,
		Username:       "reader",
		Password:       testPassword,
		Database:       "testdb",
		TimeoutSeconds: 1,
		ExportPath:     t.TempDir(),
	}, t.TempDir())
}

// useUnreachableManager installs a global connManager with one unreachable
// connection named "DEV" and restores the previous manager after the test.
func useUnreachableManager(t *testing.T) ConnectionConfig {
	t.Helper()
	cfg := unreachableConfig(t)
	cm := &ConnectionManager{}
	if err := cm.Init(map[string]ConnectionConfig{"DEV": cfg}); err != nil {
		t.Fatalf("Init: %v", err)
	}
	prev := connManager
	connManager = cm
	t.Cleanup(func() { connManager = prev })
	return cfg
}

func TestParseHosts(t *testing.T) {
	cases := []struct {
		host string
		port int
		want []string
	}{
		{"127.0.0.1", 27017, []string{"127.0.0.1:27017"}},
		{"db.local", 28000, []string{"db.local:28000"}},
		{"db.local:1234", 27017, []string{"db.local:1234"}},
		{"h1:27017, h2:27018", 27017, []string{"h1:27017", "h2:27018"}},
		{"h1:27017,h2:27018,", 27017, []string{"h1:27017", "h2:27018"}},
		{"  spaced  ", 27017, []string{"spaced:27017"}},
	}
	for _, c := range cases {
		if got := parseHosts(c.host, c.port); !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseHosts(%q, %d) = %v, want %v", c.host, c.port, got, c.want)
		}
	}
}

func TestBuildClientOptions(t *testing.T) {
	base := applyDefaults(ConnectionConfig{Host: "127.0.0.1", Database: "appdb", TimeoutSeconds: 7}, t.TempDir())

	t.Run("single host no auth", func(t *testing.T) {
		opts, err := buildClientOptions(base)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(opts.Hosts, []string{"127.0.0.1:27017"}) {
			t.Errorf("hosts = %v", opts.Hosts)
		}
		if opts.Auth != nil {
			t.Errorf("auth should be unset when username is empty, got %+v", opts.Auth)
		}
		if opts.ReplicaSet != nil || opts.TLSConfig != nil {
			t.Error("replica set and TLS should be unset by default")
		}
		if opts.Direct == nil || *opts.Direct {
			t.Error("direct should be explicitly false")
		}
		if opts.ReadPreference == nil || opts.ReadPreference.Mode() != readpref.SecondaryPreferredMode {
			t.Error("read preference should be secondaryPreferred")
		}
		if *opts.ServerSelectionTimeout != 7*time.Second || *opts.ConnectTimeout != 7*time.Second {
			t.Error("timeouts should equal timeout_seconds")
		}
		if *opts.AppName != "mcp-mongodb" {
			t.Errorf("app name = %q", *opts.AppName)
		}
	})

	t.Run("auth, direct, replica set, tls", func(t *testing.T) {
		cfg := base
		cfg.Username, cfg.Password, cfg.AuthSource = "u", testPassword, "admin"
		cfg.DirectConnection, cfg.ReplicaSet, cfg.TLS = true, "rs0", true
		opts, err := buildClientOptions(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if opts.Auth == nil || opts.Auth.Username != "u" || opts.Auth.Password != testPassword || opts.Auth.AuthSource != "admin" {
			t.Errorf("auth = %+v", opts.Auth)
		}
		if !*opts.Direct || *opts.ReplicaSet != "rs0" || opts.TLSConfig == nil {
			t.Error("direct/replica set/tls not applied")
		}
	})

	t.Run("comma hosts", func(t *testing.T) {
		cfg := base
		cfg.Host = "h1:27017,h2:27018"
		opts, err := buildClientOptions(cfg)
		if err != nil {
			t.Fatal(err)
		}
		if len(opts.Hosts) != 2 {
			t.Errorf("hosts = %v", opts.Hosts)
		}
	})

	t.Run("direct with multiple hosts is rejected", func(t *testing.T) {
		cfg := base
		cfg.Host = "h1:27017,h2:27018"
		cfg.DirectConnection = true
		if _, err := buildClientOptions(cfg); err == nil {
			t.Error("want error for direct_connection with multiple hosts")
		}
	})
}

func TestConnectionManager_InitRejectsBadOptionsNamingConnection(t *testing.T) {
	cfg := unreachableConfig(t)
	cfg.Host = "h1:27017,h2:27018"
	cfg.DirectConnection = true
	err := (&ConnectionManager{}).Init(map[string]ConnectionConfig{"PROD": cfg})
	if err == nil || !strings.Contains(err.Error(), `connection "PROD"`) {
		t.Fatalf("want error naming PROD, got %v", err)
	}
	if strings.Contains(err.Error(), testPassword) {
		t.Errorf("error leaks password: %v", err)
	}
}

func TestConnectionManager_DefaultName(t *testing.T) {
	cfg := unreachableConfig(t)

	cm := &ConnectionManager{}
	if err := cm.Init(map[string]ConnectionConfig{"PROD": cfg, "default": cfg, "DEV": cfg}); err != nil {
		t.Fatal(err)
	}
	if conn, err := cm.lookup(""); err != nil || conn.Name != "default" {
		t.Errorf("want 'default', got %v, %v", conn, err)
	}

	cm = &ConnectionManager{}
	if err := cm.Init(map[string]ConnectionConfig{"PROD": cfg, "DEV": cfg}); err != nil {
		t.Fatal(err)
	}
	if conn, err := cm.lookup(""); err != nil || conn.Name != "DEV" {
		t.Errorf("want alphabetically first 'DEV', got %v, %v", conn, err)
	}
}

func TestConnectionManager_UnknownNameListsAvailable(t *testing.T) {
	cfg := unreachableConfig(t)
	cm := &ConnectionManager{}
	if err := cm.Init(map[string]ConnectionConfig{"PROD": cfg, "DEV": cfg}); err != nil {
		t.Fatal(err)
	}
	_, err := cm.Get(context.Background(), "STAGING")
	if err == nil || !strings.Contains(err.Error(), `connection "STAGING" not found. Available: [DEV PROD]`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestConnectionManager_PingFailureIsNotCached(t *testing.T) {
	cfg := unreachableConfig(t)
	cm := &ConnectionManager{}
	if err := cm.Init(map[string]ConnectionConfig{"DEV": cfg}); err != nil {
		t.Fatal(err)
	}

	for attempt := 1; attempt <= 2; attempt++ {
		_, err := cm.Get(context.Background(), "DEV")
		if err == nil || !strings.Contains(err.Error(), `connection "DEV" is unreachable`) {
			t.Fatalf("attempt %d: want unreachable error, got %v", attempt, err)
		}
		if strings.Contains(err.Error(), testPassword) {
			t.Fatalf("attempt %d: error leaks password", attempt)
		}
		conn, _ := cm.lookup("DEV")
		if conn.pinged {
			t.Fatalf("attempt %d: failed ping must not be cached", attempt)
		}
	}
}

func TestLoadConfig_FromEnvVar(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cfg.json")
	data := `{"connections": {"DEV": {"host": "127.0.0.1", "port": 1, "database": "d", "export_path": "out"}}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_MONGODB_CONFIG", path)
	prev := connManager
	t.Cleanup(func() { connManager = prev })

	if err := LoadConfig(); err != nil {
		t.Fatalf("LoadConfig: %v", err)
	}
	conn, err := connManager.lookup("")
	if err != nil {
		t.Fatal(err)
	}
	if conn.Name != "DEV" || conn.Config.ExportPath != filepath.Join(dir, "out") {
		t.Errorf("unexpected connection: %s %s", conn.Name, conn.Config.ExportPath)
	}
}
